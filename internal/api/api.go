// Package api 提供 WebUI 的 HTTP 接口。
//
// 约定：
//   - 成功响应直接返回资源对象；列表统一为 {items,total,page,pageSize}；
//   - 失败统一为 {"error":{"code","message","requestId"}}，绝不返回内部堆栈或 SQL 细节；
//   - 所有写操作都写审计日志；
//   - 日志只记录请求路径，不记录查询串（避免 token 出现在日志里）。
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/auth"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/bot"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/config"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/eventbus"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/ticket"
	"github.com/VanceHud/VanceKOOKTicketGO/web"
)

// BotController 是 API 层对机器人的最小依赖（由 bot.Manager 实现）。
//
// 采用消费方定义接口的方式：api 只声明自己需要什么，
// 便于在不启动真实连接的情况下测试 WebUI 接口。
type BotController interface {
	Status() bot.Status
	GuildRoles(ctx context.Context) ([]kook.Role, error)
	GuildChannels(ctx context.Context) ([]kook.Channel, error)
	ResolveWebRole(ctx context.Context, kookUserID string) (string, bool, error)
	Restart(ctx context.Context) error
	NotifyConfigChanged()
	// SendPanelCard 让机器人发送/重发某个面板的卡片，返回消息 ID（由调用方持久化）。
	SendPanelCard(ctx context.Context, panel *store.Panel, buttonText string) (string, error)
	// DeleteMessage 尽力删除一条消息（面板重建时清理旧卡片）。
	DeleteMessage(ctx context.Context, msgID string) error
	// ChannelInfo 返回频道信息（用于校验频道存在并记录名称）。
	ChannelInfo(ctx context.Context, channelID string) (*kook.Channel, error)
	// RoleName 返回角色名（未知时返回空串）。
	RoleName(ctx context.Context, roleID string) string
	// GameList 拉取游戏库；未连接时返回错误。
	GameList(ctx context.Context, gameType int) ([]kook.Game, error)
	// GameCreate 新建游戏；未连接时返回错误。
	GameCreate(ctx context.Context, name, icon string) (*kook.Game, error)
	// GameUpdate 更新游戏名称/图标；未连接时返回错误。
	GameUpdate(ctx context.Context, id int64, name, icon string) (*kook.Game, error)
	// GameDelete 删除游戏；未连接时返回错误。
	GameDelete(ctx context.Context, id int64) error
	// StartGameActivity 设置游戏动态；未连接时返回错误。
	StartGameActivity(ctx context.Context, gameID int64) error
	// StartMusicActivity 设置音乐动态；未连接时返回错误。
	StartMusicActivity(ctx context.Context, musicName, singer, software string) error
	// DeleteActivity 停止指定类型的动态；未连接时返回错误。
	DeleteActivity(ctx context.Context, dataType int) error
}

// Deps 是 API 层的依赖集合。
type Deps struct {
	Config   *config.Config
	Store    *store.Store
	Bus      *eventbus.Bus
	Tickets  *ticket.Service
	Sessions *auth.SessionManager
	Login    *auth.LoginLimiter
	Codes    *auth.WindowLimiter
	Web      *web.Handler
	Log      *slog.Logger
	Started  time.Time
	Version  string
	// Bot 为机器人管理器；未配置或未接入时为 nil，所有用法都必须 nil 安全。
	Bot BotController
}

// Server 承载全部 HTTP handler。
type Server struct {
	Deps
	passwordAttempts *auth.WindowLimiter
	passwordSlots    chan struct{}

	// 统计聚合的短 TTL 缓存：同一份全区间扫描不应被并发请求重复执行。
	overviewCache  *cachedStats[*store.Overview]
	analyticsCache *cachedStats[*store.Analytics]
}

// NewServer 创建 API 服务。
func NewServer(d Deps) *Server {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Server{
		Deps:             d,
		passwordAttempts: auth.NewWindowLimiter(20, time.Minute),
		passwordSlots:    make(chan struct{}, 4),
		overviewCache:    newCachedStats[*store.Overview](statsCacheTTL),
		analyticsCache:   newCachedStats[*store.Analytics](statsCacheTTL),
	}
}

// botConnected 判断机器人是否在线。
func (s *Server) botConnected() bool {
	return s.Bot != nil && s.Bot.Status().Connected
}

// botStatus 返回机器人状态；未接入时给出明确的未运行状态。
func (s *Server) botStatus() bot.Status {
	if s.Bot == nil {
		return bot.Status{Running: false, LastError: "机器人模块未启用"}
	}
	return s.Bot.Status()
}

// apiError 是统一的错误响应体。
type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId,omitempty"`
}

// fail 返回业务错误。
func (s *Server) fail(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": apiError{
		Code:      code,
		Message:   message,
		RequestID: auth.RequestIDOf(c),
	}})
}

// failInternal 记录内部错误并返回通用提示，避免泄露实现细节。
func (s *Server) failInternal(c *gin.Context, err error, action string) {
	s.Log.Error("处理请求失败",
		"action", action,
		"request_id", auth.RequestIDOf(c),
		"method", c.Request.Method,
		"path", c.Request.URL.Path,
		"actor", actorName(c),
		"err", err,
	)
	s.fail(c, http.StatusInternalServerError, "internal_error", "服务器内部错误，请稍后重试")
}

// failStore 把仓储层错误映射为合适的响应。
func (s *Server) failStore(c *gin.Context, err error, action string) {
	switch {
	case errors.Is(err, store.ErrLastAdmin):
		s.fail(c, http.StatusConflict, "last_admin", "系统必须保留至少一个可用管理员")
	case errors.Is(err, store.ErrAlreadyBound):
		s.fail(c, http.StatusConflict, "already_bound", "该账号已有其它身份绑定")
	case errors.Is(err, store.ErrNotFound):
		s.fail(c, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, gorm.ErrDuplicatedKey):
		s.fail(c, http.StatusConflict, "conflict", "记录已存在，请检查唯一字段")
	default:
		s.failInternal(c, err, action)
	}
}

// audit 追加审计日志。审计失败只记录日志，不影响主流程。
func (s *Server) audit(c *gin.Context, action, target, detail string) {
	identity := auth.IdentityOf(c)
	actor, actorType := "anonymous", store.ActorTypeWeb
	if identity != nil {
		actor, actorType = identity.Username, store.ActorTypeWeb
	}
	entry := &store.AuditLog{
		Actor:     actor,
		ActorType: actorType,
		Action:    action,
		Target:    target,
		Detail:    detail,
		IP:        auth.ClientIPOf(c),
		RequestID: auth.RequestIDOf(c),
		CreatedAt: store.Now(),
	}
	if err := s.Store.Audit.Write(entry); err != nil {
		s.Log.Error("写入审计日志失败", "action", action, "err", err)
	}
}

func actorName(c *gin.Context) string {
	if identity := auth.IdentityOf(c); identity != nil {
		return identity.Username
	}
	return "anonymous"
}

// pageParams 解析分页参数并收敛到允许区间。
func pageParams(c *gin.Context) (page, size int) {
	page = atoiDefault(c.Query("page"), 1)
	size = store.NormalizePageSize(atoiDefault(c.Query("pageSize"), store.DefaultPageSize))
	if page < 1 {
		page = 1
	}
	return page, size
}

func atoiDefault(raw string, def int) int {
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

// pathID 解析路径参数中的资源 ID：必须是正整数，非法或非正数时返回 0。
//
// 直接用 uint(atoiDefault(...)) 会把 -1 回绕成 18446744073709551615，
// 查库必然 404，但语义上应当是同一种「ID 不合法」；集中在这里处理，
// 也避免每个 handler 重复写转换。
func pathID(c *gin.Context) uint {
	value := atoiDefault(c.Param("id"), 0)
	if value <= 0 {
		return 0
	}
	return uint(value)
}

// timeParam 解析时间参数，支持两种形式：
//   - YYYY-MM-DD：按业务时区（TICKET_TZ）解释；endOfDay 为真时返回次日 00:00（右开区间）
//   - RFC3339：按给定偏移解析
//
// 返回值一律转换为 UTC，与数据库存储保持一致。
func timeParam(c *gin.Context, name string, loc *time.Location, endOfDay bool) (*time.Time, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		utc := t.UTC()
		return &utc, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", raw, loc); err == nil {
		if endOfDay {
			t = t.AddDate(0, 0, 1)
		}
		utc := t.UTC()
		return &utc, nil
	}
	return nil, errors.New("时间格式应为 YYYY-MM-DD 或 RFC3339")
}

// accessLog 记录访问日志：只包含路径，不含查询串。
func accessLog(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		level := slog.LevelInfo
		switch {
		case c.Writer.Status() >= 500:
			level = slog.LevelError
		case c.Writer.Status() >= 400:
			level = slog.LevelWarn
		case strings.HasPrefix(c.Request.URL.Path, "/assets/"):
			// 静态产物：一次页面加载十几个带哈希的请求，逐条记录只会淹没日志。
			level = slog.LevelDebug
		}
		log.Log(c.Request.Context(), level, "http 请求",
			"request_id", auth.RequestIDOf(c),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"ms", time.Since(start).Milliseconds(),
			"ip", auth.ClientIPOf(c),
			"actor", actorName(c),
			"bytes", c.Writer.Size(),
		)
	}
}
