// Package ticket 实现工单业务：开单、关闭、锁定、重开、超时扫描与备注。
//
// 与 KOOK 交互的部分通过 Platform 接口隔离：
//   - 里程碑 3 注入真实 KOOK 客户端实现；
//   - KOOK_DRYRUN=1 时注入 NoopPlatform，使业务逻辑与 WebUI 可以脱离 KOOK 完整跑通。
//
// 所有操作都会写审计日志并广播 SSE 事件（工单状态与仪表盘数据实时刷新）。
package ticket

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/eventbus"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/keyedlock"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// Platform 抽象工单流程需要机器人执行的 KOOK 侧动作。
type Platform interface {
	// SetUserSpeak 允许或禁止开单人在工单频道发言（锁定 / 重开使用）。
	SetUserSpeak(ctx context.Context, channelID, userID string, allow bool) error
	// NotifyLocked 在工单频道内发送“已锁定”提示卡片（含重新激活按钮）。
	// 该通知属于尽力而为：失败只记录警告，不影响锁定结果。
	NotifyLocked(ctx context.Context, t *store.Ticket, actor Actor, reason string) error
	// NotifyReopened 在工单频道内发送“已重新激活”提示卡片。
	NotifyReopened(ctx context.Context, t *store.Ticket, actor Actor) error
	// NotifyClosed 通知工单已关闭：写日志频道 + 私聊开单人，返回两条消息 ID。
	// actor 是关闭操作者，通知卡片需要展示“由谁关闭”。
	NotifyClosed(ctx context.Context, t *store.Ticket, actor Actor, note string) (logMsgID, userMsgID string, err error)
	// CloseTicketChannel 删除工单频道。
	CloseTicketChannel(ctx context.Context, channelID string) error
}

// 业务错误。
var (
	// ErrInvalidState 表示工单当前状态不允许该操作。
	ErrInvalidState = errors.New("工单当前状态不允许该操作")
	// ErrNoPlatform 表示没有可用的平台实现（KOOK 未连接且非 DryRun）。
	ErrNoPlatform = errors.New("机器人未连接到 KOOK，操作已拒绝")
)

// MaxCloseNoteLen 是关闭说明的最大长度（字符数）。
// WebUI 关闭弹窗与 KOOK 的 /tkclose 命令共用这一上限。
const MaxCloseNoteLen = 1000

// Actor 是操作发起者。
//
// IP 与 RequestID 会一并写入审计日志，便于把界面操作与访问日志对应起来。
type Actor struct {
	ID        string
	Name      string
	Role      string
	Source    string // web | kook | system
	IP        string
	RequestID string
}

// SystemActor 返回系统操作者（超时自动锁定等）。
func SystemActor() Actor {
	return Actor{ID: "system", Name: "系统", Role: store.RoleAdmin, Source: "system"}
}

// Service 是工单业务服务。
type Service struct {
	store      *store.Store
	bus        *eventbus.Bus
	platform   Platform
	platformMu sync.RWMutex
	loc        *time.Location
	// outdateHours 返回工单空闲锁定阈值（小时），从配置实时读取。
	outdateHours func() int
	// locks 按工单编号串行化状态变更：KOOK 按钮与 WebUI 可能同时操作同一张工单，
	// 而不同工单之间互不阻塞（旧实现在机器人侧用一把全局锁，第二个工单要等第一个跑完）。
	locks keyedlock.Locks
}

// NewService 创建工单服务。
func NewService(st *store.Store, bus *eventbus.Bus, platform Platform, loc *time.Location, outdateHours func() int) *Service {
	if outdateHours == nil {
		outdateHours = func() int { return store.DefaultOutdateHours }
	}
	return &Service{store: st, bus: bus, platform: platform, loc: loc, outdateHours: outdateHours}
}

// SetPlatform 在 KOOK 连接建立后注入真实平台实现。
func (s *Service) SetPlatform(p Platform) {
	s.platformMu.Lock()
	defer s.platformMu.Unlock()
	s.platform = p
}

// WithPlatform 在给定平台实现下执行 fn，用于“本地事务 + 远端调用”的清晰边界。
func (s *Service) platformOrErr() (Platform, error) {
	s.platformMu.RLock()
	defer s.platformMu.RUnlock()
	if s.platform == nil {
		return nil, ErrNoPlatform
	}
	return s.platform, nil
}

// Get 返回工单详情。
func (s *Service) Get(no string) (*store.Ticket, error) { return s.store.Tickets.ByNo(no) }

// OpenParams 是一次开单所需的上下文，来自被点击的面板及其所属工单类型。
type OpenParams struct {
	UserID   string
	UserName string
	// SourceChannelID 是按钮所在的面板频道。
	SourceChannelID string
	// PanelID / TypeID 可为空（面板或类型已被删除的容错场景）；
	// TypeName 是开单时的类型名快照，用于历史展示。
	PanelID  *uint
	TypeID   *uint
	TypeName string
}

// CreatePending 分配工单编号并写入占位记录（状态 pending）。
//
// 由机器人在“点击按钮 → 建频道”流程的最前面调用，编号一旦分配即入库，
// 保证频道名中的编号与数据库一致；建频道失败时用 DiscardPending 回收。
func (s *Service) CreatePending(ctx context.Context, params OpenParams) (*store.Ticket, error) {
	now := store.Now()
	t := &store.Ticket{
		UserID:          params.UserID,
		UserName:        params.UserName,
		SourceChannelID: params.SourceChannelID,
		PanelID:         params.PanelID,
		TypeID:          params.TypeID,
		TypeName:        params.TypeName,
		Status:          store.TicketPending,
		StartedAt:       now,
	}
	if err := s.store.Tickets.CreateWithNo(t, now, s.loc); err != nil {
		return nil, err
	}
	detail := "发起工单（等待创建频道）"
	if params.TypeName != "" {
		detail = fmt.Sprintf("发起工单（类型：%s，等待创建频道）", params.TypeName)
	}
	s.audit(Actor{ID: params.UserID, Name: params.UserName, Source: "kook"}, "ticket.open", t.No, detail)
	return t, nil
}

// Activate 在建频道与权限下发完成后把工单置为进行中。
func (s *Service) Activate(ctx context.Context, no, channelID string) (*store.Ticket, error) {
	if err := s.store.Tickets.UpdateFields(no, map[string]any{
		"channel_id": channelID,
		"status":     store.TicketOpen,
		"started_at": store.Now(),
	}); err != nil {
		return nil, err
	}
	updated, err := s.store.Tickets.ByNo(no)
	if err != nil {
		return nil, err
	}
	if _, err := s.AddBotMessage(ctx, no, channelID, "工单已创建，等待管理员处理"); err != nil {
		s.logWarn("写入开单系统消息失败", no, err)
	}
	s.publish(eventbus.EventTicketCreated, no, updated)
	return updated, nil
}

// AddBotMessage 记录一条机器人发送的系统消息（如面板自定义的开单提示），并广播消息事件。
//
// 消息内容原样入库供 WebUI 时间线展示；KOOK 侧的发送由调用方完成，
// 以便发送失败时不产生“时间线有记录但频道内没有”的脏数据。
func (s *Service) AddBotMessage(ctx context.Context, no, channelID, content string) (*store.TicketMessage, error) {
	message := &store.TicketMessage{
		TicketNo:  no,
		ChannelID: channelID,
		UserID:    "bot",
		UserName:  "TicketBot",
		Content:   content,
		MsgType:   store.MsgTypeSystem,
		IsBot:     true,
		CreatedAt: store.Now(),
	}
	if err := s.store.Tickets.AddMessage(message); err != nil {
		return nil, err
	}
	s.publish(eventbus.EventTicketMessage, no, message)
	return message, nil
}

// DiscardPending 回收占号：仅在状态仍为 pending 时生效（编号尚未对外暴露）。
func (s *Service) DiscardPending(ctx context.Context, no string) error {
	if err := s.store.Tickets.Delete(no); err != nil {
		return err
	}
	s.audit(Actor{ID: "bot", Name: "系统", Source: "system"}, "ticket.discard", no, "开单流程失败，回收工单编号")
	return nil
}

// logWarn 输出带工单编号的警告。
func (s *Service) logWarn(message, no string, err error) {
	slog.Warn(message, "ticket_no", no, "err", err)
}

// Close 关闭工单：先通知，再删除频道，最后落库。
//
// 顺序说明：通知与删除失败会直接返回错误、不改数据库状态，
// 避免出现“记录已关闭但频道仍在”的不一致；重复点击关闭会被状态校验拦下。
func (s *Service) Close(ctx context.Context, no string, actor Actor, note string) (*store.Ticket, error) {
	unlock := s.locks.Lock("ticket:" + no)
	defer unlock()

	t, err := s.store.Tickets.ByNo(no)
	if err != nil {
		return nil, err
	}
	if t.Status == store.TicketClosed {
		return nil, fmt.Errorf("%w：工单已关闭", ErrInvalidState)
	}
	if t.Status == store.TicketPending {
		return nil, fmt.Errorf("%w：工单频道尚未创建完成", ErrInvalidState)
	}

	// 说明长度由调用方先行校验；这里再兜底一次，避免其它入口写入超长文本。
	note = strings.TrimSpace(note)
	if len([]rune(note)) > MaxCloseNoteLen {
		note = string([]rune(note)[:MaxCloseNoteLen])
	}

	platform, err := s.platformOrErr()
	if err != nil {
		return nil, err
	}

	logMsgID, userMsgID, err := platform.NotifyClosed(ctx, t, actor, note)
	if err != nil {
		return nil, fmt.Errorf("发送关闭通知失败: %w", err)
	}
	if t.ChannelID != "" {
		if err := platform.CloseTicketChannel(ctx, t.ChannelID); err != nil {
			return nil, fmt.Errorf("删除工单频道失败: %w", err)
		}
	}

	now := store.Now()
	fields := map[string]any{
		"status":         store.TicketClosed,
		"closed_at":      now,
		"closed_by":      actor.ID,
		"closed_by_name": actor.Name,
		"close_note":     note,
		"locked_at":      nil,
		"lock_reason":    "",
	}
	if logMsgID != "" {
		fields["log_channel_msg_id"] = logMsgID
	}
	if userMsgID != "" {
		fields["log_user_msg_id"] = userMsgID
	}
	if err := s.store.Tickets.UpdateFields(no, fields); err != nil {
		return nil, err
	}

	updated, err := s.store.Tickets.ByNo(no)
	if err != nil {
		return nil, err
	}
	s.after(updated, actor, "ticket.close", fmt.Sprintf("关闭工单 %s", no))
	return updated, nil
}

// Lock 锁定工单：开单人不可发言，工单仍可见。
// reason 取 store.LockReasonManual 或 store.LockReasonTimeout。
func (s *Service) Lock(ctx context.Context, no string, actor Actor, reason string) (*store.Ticket, error) {
	unlock := s.locks.Lock("ticket:" + no)
	defer unlock()

	if reason == "" {
		reason = store.LockReasonManual
	}
	t, err := s.store.Tickets.ByNo(no)
	if err != nil {
		return nil, err
	}
	switch t.Status {
	case store.TicketLocked:
		return nil, fmt.Errorf("%w：工单已是锁定状态", ErrInvalidState)
	case store.TicketClosed:
		return nil, fmt.Errorf("%w：工单已关闭", ErrInvalidState)
	case store.TicketPending:
		return nil, fmt.Errorf("%w：工单频道尚未创建完成", ErrInvalidState)
	}

	platform, err := s.platformOrErr()
	if err != nil {
		return nil, err
	}
	if t.ChannelID != "" {
		if err := platform.SetUserSpeak(ctx, t.ChannelID, t.UserID, false); err != nil {
			return nil, fmt.Errorf("设置频道发言权限失败: %w", err)
		}
	}

	// 通知失败不回滚：权限位已生效，这里仅记录警告（见 Platform.NotifyLocked 注释）。
	if err := platform.NotifyLocked(ctx, t, actor, reason); err != nil {
		s.logWarn("发送锁定通知失败", no, err)
	}

	now := store.Now()
	if err := s.store.Tickets.UpdateFields(no, map[string]any{
		"status":      store.TicketLocked,
		"locked_at":   now,
		"lock_reason": reason,
	}); err != nil {
		return nil, err
	}

	updated, err := s.store.Tickets.ByNo(no)
	if err != nil {
		return nil, err
	}
	s.after(updated, actor, "ticket.lock", fmt.Sprintf("锁定工单 %s（原因：%s）", no, reason))
	return updated, nil
}

// Reopen 重新激活已锁定的工单。
func (s *Service) Reopen(ctx context.Context, no string, actor Actor) (*store.Ticket, error) {
	unlock := s.locks.Lock("ticket:" + no)
	defer unlock()

	t, err := s.store.Tickets.ByNo(no)
	if err != nil {
		return nil, err
	}
	if t.Status != store.TicketLocked {
		return nil, fmt.Errorf("%w：只有已锁定的工单可以重新激活", ErrInvalidState)
	}

	platform, err := s.platformOrErr()
	if err != nil {
		return nil, err
	}
	if t.ChannelID != "" {
		if err := platform.SetUserSpeak(ctx, t.ChannelID, t.UserID, true); err != nil {
			return nil, fmt.Errorf("恢复频道发言权限失败: %w", err)
		}
	}

	if err := platform.NotifyReopened(ctx, t, actor); err != nil {
		s.logWarn("发送重新激活通知失败", no, err)
	}

	if err := s.store.Tickets.UpdateFields(no, map[string]any{
		"status":      store.TicketOpen,
		"locked_at":   nil,
		"lock_reason": "",
	}); err != nil {
		return nil, err
	}

	updated, err := s.store.Tickets.ByNo(no)
	if err != nil {
		return nil, err
	}
	s.after(updated, actor, "ticket.reopen", fmt.Sprintf("重新激活工单 %s", no))
	return updated, nil
}

// AddNote 为工单添加备注。
func (s *Service) AddNote(no string, actor Actor, content string) (*store.TicketNote, error) {
	if _, err := s.store.Tickets.ByNo(no); err != nil {
		return nil, err
	}
	note := &store.TicketNote{
		TicketNo:   no,
		AuthorID:   actor.ID,
		AuthorName: actor.Name,
		Source:     actor.Source,
		Content:    content,
		CreatedAt:  store.Now(),
	}
	if err := s.store.Tickets.AddNote(note); err != nil {
		return nil, err
	}
	s.publish(eventbus.EventTicketNote, no, nil)
	s.audit(actor, "ticket.note", no, "新增备注")
	return note, nil
}

// Notes 返回工单备注。
func (s *Service) Notes(no string) ([]store.TicketNote, error) { return s.store.Tickets.Notes(no) }

// Messages 返回工单消息。
func (s *Service) Messages(no string, limit, offset int) ([]store.TicketMessage, error) {
	return s.store.Tickets.Messages(no, limit, offset)
}

// ScanTimeout 扫描并锁定超时未活动的工单，返回被锁定的工单列表。
//
// 判定依据是工单的 updated_at：写入消息时会刷新该字段，
// 因此“从未发言”的工单也会从开单时间开始计时。
func (s *Service) ScanTimeout(ctx context.Context) ([]string, error) {
	hours := s.outdateHours()
	if hours <= 0 {
		return nil, nil
	}
	cutoff := store.Now().Add(-time.Duration(hours) * time.Hour)

	var candidates []store.Ticket
	if err := s.store.DB().
		Where("status = ? AND updated_at < ?", store.TicketOpen, cutoff).
		Order("updated_at ASC").
		Limit(200).
		Find(&candidates).Error; err != nil {
		return nil, err
	}

	locked := make([]string, 0, len(candidates))
	for i := range candidates {
		no := candidates[i].No
		if _, err := s.Lock(ctx, no, SystemActor(), store.LockReasonTimeout); err != nil {
			// 单个工单失败不影响其余工单；状态可能已被人工改变。
			if errors.Is(err, ErrInvalidState) || errors.Is(err, store.ErrNotFound) {
				continue
			}
			return locked, fmt.Errorf("锁定工单 %s 失败: %w", no, err)
		}
		locked = append(locked, no)
	}
	return locked, nil
}

// after 在状态变更后广播事件并记录审计。
func (s *Service) after(t *store.Ticket, actor Actor, action, detail string) {
	s.publish(eventbus.EventTicketUpdated, t.No, t)
	s.audit(actor, action, t.No, detail)
}

func (s *Service) publish(eventType, ticketNo string, data any) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(eventbus.Event{Type: eventType, TicketNo: ticketNo, Data: data, At: store.Now()})
}

func (s *Service) audit(actor Actor, action, target, detail string) {
	if s.store == nil {
		return
	}
	_ = s.store.Audit.Write(&store.AuditLog{
		Actor:     actor.Name,
		ActorType: actor.Source,
		Action:    action,
		Target:    target,
		Detail:    detail,
		IP:        actor.IP,
		RequestID: actor.RequestID,
		CreatedAt: store.Now(),
	})
}
