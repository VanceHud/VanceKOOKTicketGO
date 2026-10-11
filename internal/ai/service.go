// Package ai 编排 LLM 在工单流程中的全部用法：自动应答、摘要、分类与知识闭环。
//
// 分层约定（对齐 ticket.Platform 的思路）：
//   - 本包不依赖 kook/bot：发送 AI 回复经 Sender 接口由 bot 包实现注入；
//   - LLM 调用经 LLMClient 接口（默认实现是 internal/llm 的 OpenAI 兼容客户端），
//     测试注入假客户端；
//   - 所有 LLM 调用都走固定 worker 池（有界队列），绝不阻塞事件分发协程：
//     生成一段回复需要数秒到数十秒，若在网关分片协程里同步等待，
//     会复现「心跳 PONG 被堵住 → 误判断线 → 事件重放」的问题（见 bot/dispatch.go）。
//
// 自动应答的防抖：开单人连续发消息时，只有静默超过 QuietSeconds 才生成回复，
// 期间每条新消息都会重置计时；人工客服发言立即取消计时并接管（AIState=takeover）。
package ai

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/eventbus"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/llm"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// 错误类别。
var (
	// ErrDisabled 表示 AI 能力未启用或未配置（调用方应静默跳过，不视为故障）。
	ErrDisabled = errors.New("AI 未启用或未配置")
	// ErrBusy 表示任务队列已满（刷屏保护），本次放弃。
	ErrBusy = errors.New("AI 任务队列已满")
)

// Sender 把 AI 回复投递到 KOOK 工单频道（bot 包实现：发送 + 归档 + SSE 推送）。
type Sender interface {
	SendAIReply(ctx context.Context, t *store.Ticket, content string) error
}

// LLMClient 是服务需要的 LLM 能力（*llm.Client 天然满足；测试注入假实现）。
type LLMClient interface {
	Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error)
	Embed(ctx context.Context, texts []string, model string) ([][]float32, error)
}

// ClientFactory 按配置构造 LLM 客户端。
type ClientFactory func(cfg *store.AISettings) (LLMClient, error)

// DefaultClientFactory 是默认工厂：OpenAI 兼容客户端。
func DefaultClientFactory(cfg *store.AISettings) (LLMClient, error) {
	return llm.NewClient(llm.Options{
		BaseURL: cfg.BaseURL,
		APIKey:  cfg.APIKeyPlaintext,
		Model:   cfg.Model,
		Logger:  slog.Default(),
	})
}

// Deps 是服务依赖。
type Deps struct {
	Store     *store.Store
	Bus       *eventbus.Bus
	Logger    *slog.Logger
	AppSecret []byte
	// ClientFactory 留空时使用 DefaultClientFactory。
	ClientFactory ClientFactory
}

// 任务队列规模：worker 数与队列容量。
//
// worker=2：LLM 客户端自带 1 req/s 限速，更多 worker 只是在限速器前排队；
// 队列=64：正常工单量远用不满，刷屏时宁可丢弃（ErrBusy）也不无限堆积。
const (
	workerCount = 2
	queueSize   = 64
)

// job 是一次异步任务。
type job struct {
	kind     jobKind
	ticketNo string
	// message 仅 capture 任务使用（触发捕获的那条人工回复）。
	message *store.TicketMessage
}

type jobKind int

const (
	jobReply jobKind = iota
	jobSummary
	jobClassify
	jobCapture
)

// Service 是 AI 业务服务。
type Service struct {
	deps Deps

	// 生命周期
	mu      sync.Mutex
	started bool
	stop    context.CancelFunc
	wg      sync.WaitGroup
	jobs    chan job

	// pending 是防抖计时器（工单编号 → 计时器）。
	pending map[string]*time.Timer

	// client 是缓存的 LLM 客户端；clientKey 是配置指纹（BaseURL|Key|Model），
	// 配置变更后下次调用自动重建。
	client    LLMClient
	clientKey string

	// sender 由 bot 装配后注入（DryRun / 未连接时为 nil，发送类任务优雅降级）。
	senderMu sync.RWMutex
	sender   Sender
}

// New 创建服务。
func New(deps Deps) *Service {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.ClientFactory == nil {
		deps.ClientFactory = DefaultClientFactory
	}
	return &Service{deps: deps, pending: map[string]*time.Timer{}}
}

// SetSender 注入回复发送通道（bot.Manager 装配完成后调用）。
func (s *Service) SetSender(sender Sender) {
	s.senderMu.Lock()
	defer s.senderMu.Unlock()
	s.sender = sender
}

// Start 启动 worker 池；重复调用无副作用。
func (s *Service) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.started = true
	s.stop = cancel
	s.jobs = make(chan job, queueSize)
	s.wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go s.worker(ctx)
	}
}

// Stop 停止服务并等待 worker 退出。
func (s *Service) Stop() {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	stop := s.stop
	s.started = false
	s.jobs = nil
	s.mu.Unlock()

	// 取消全部待发的防抖计时器。
	s.mu.Lock()
	for no, timer := range s.pending {
		timer.Stop()
		delete(s.pending, no)
	}
	s.mu.Unlock()

	stop()
	s.wg.Wait()
}

// worker 消费任务队列。
func (s *Service) worker(ctx context.Context) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-s.jobs:
			s.runJob(ctx, j)
		}
	}
}

// runJob 执行单个任务；单任务 panic 不允许杀死 worker（对齐事件分发的兜底策略）。
func (s *Service) runJob(ctx context.Context, j job) {
	defer func() {
		if r := recover(); r != nil {
			s.deps.Logger.Error("AI 任务发生 panic，已跳过", "panic", r, "kind", j.kind, "ticket_no", j.ticketNo)
		}
	}()
	// 任务与触发它的请求生命周期解耦：请求可能早已返回，
	// 这里给每个任务独立的超时（LLM 生成 + 发送）。
	taskCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskTimeout)
	defer cancel()

	switch j.kind {
	case jobReply:
		s.generateReply(taskCtx, j.ticketNo)
	case jobSummary:
		s.generateSummary(taskCtx, j.ticketNo)
	case jobClassify:
		s.generateClassification(taskCtx, j.ticketNo)
	case jobCapture:
		s.captureKnowledge(taskCtx, j.ticketNo, j.message)
	}
}

// taskTimeout 是单个 AI 任务的端到端上限（含 LLM 调用与发送）。
const taskTimeout = 120 * time.Second

// enqueue 把任务放入队列；队列满时返回 ErrBusy（调用方记日志放弃）。
func (s *Service) enqueue(j job) error {
	s.mu.Lock()
	jobs := s.jobs
	started := s.started
	s.mu.Unlock()
	if !started || jobs == nil {
		return ErrDisabled
	}
	select {
	case jobs <- j:
		return nil
	default:
		return ErrBusy
	}
}

// ---------------------------------------------------------------------------
// 配置与客户端
// ---------------------------------------------------------------------------

// config 读取当前配置快照。
func (s *Service) config() (*store.AISettings, error) {
	return s.deps.Store.AI.Config(s.deps.AppSecret)
}

// clientFor 返回可用的 LLM 客户端；未启用或未配置时返回 ErrDisabled。
//
// 客户端按配置指纹缓存：WebUI 改配置后无需重启，下一次调用自动用新客户端。
func (s *Service) clientFor(cfg *store.AISettings) (LLMClient, error) {
	if !cfg.Enabled || cfg.APIKeyPlaintext == "" || cfg.Model == "" {
		return nil, ErrDisabled
	}
	key := cfg.BaseURL + "|" + cfg.APIKeyPlaintext + "|" + cfg.Model
	s.mu.Lock()
	client, cachedKey := s.client, s.clientKey
	s.mu.Unlock()
	if client != nil && cachedKey == key {
		return client, nil
	}
	client, err := s.deps.ClientFactory(cfg)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.client, s.clientKey = client, key
	s.mu.Unlock()
	return client, nil
}

// TestConnection 用当前配置做一次最小调用（WebUI「测试连接」）。
func (s *Service) TestConnection(ctx context.Context) error {
	cfg, err := s.config()
	if err != nil {
		return err
	}
	client, err := s.clientFor(cfg)
	if err != nil {
		return err
	}
	started := time.Now()
	_, err = client.Chat(ctx, llm.ChatRequest{
		Messages:  []llm.Message{{Role: "user", Content: "ping"}},
		MaxTokens: 8,
	})
	s.recordCall(cfg, &store.AICall{Kind: "test", Model: cfg.Model, Error: errText(err)}, started)
	return err
}

// recordCall 记录一次 LLM 调用（尽力而为）。
func (s *Service) recordCall(cfg *store.AISettings, call *store.AICall, started time.Time) {
	call.DurationMs = time.Since(started).Milliseconds()
	if err := s.deps.Store.AI.RecordAICall(call); err != nil {
		s.deps.Logger.Warn("写入 AI 调用记录失败", "kind", call.Kind, "err", err)
	}
}

// errText 把错误转成截断文本（调用记录里只存摘要）。
func errText(err error) string {
	if err == nil {
		return ""
	}
	return truncateRunes(err.Error(), 200)
}

// truncateRunes 按字符截断。
func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

// publish 广播工单更新事件（摘要 / AI 状态变化后让前端刷新详情）。
func (s *Service) publish(ticketNo string, data any) {
	if s.deps.Bus == nil {
		return
	}
	s.deps.Bus.Publish(eventbus.Event{
		Type:     eventbus.EventTicketUpdated,
		TicketNo: ticketNo,
		Data:     data,
		At:       store.Now(),
	})
}

// ticketByNo 读取工单，不存在时返回错误。
func (s *Service) ticketByNo(no string) (*store.Ticket, error) {
	return s.deps.Store.Tickets.ByNo(no)
}
