// Package api 提供 WebUI 的 HTTP 接口。
//
// 约定：
//   - 成功响应直接返回资源对象；列表统一为 {items,total,page,pageSize}；
//   - 失败统一为 {"error":{"code","message","requestId"}}，绝不返回内部堆栈或 SQL 细节；
//   - 所有写操作都写审计日志；
//   - 日志只记录请求路径，不记录查询串（避免 token 出现在日志里）。
package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"vancekookticket/internal/auth"
	"vancekookticket/internal/config"
	"vancekookticket/internal/eventbus"
	"vancekookticket/internal/store"
	"vancekookticket/internal/ticket"
	"vancekookticket/web"
)

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

	// BotConnected 返回 KOOK 连接是否在线；里程碑 3 起由 KOOK 客户端提供。
	BotConnected func() bool
}

// Server 承载全部 HTTP handler。
type Server struct {
	Deps
}

// NewServer 创建 API 服务。
func NewServer(d Deps) *Server {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Server{Deps: d}
}

func (s *Server) botConnected() bool {
	if s.BotConnected == nil {
		return false
	}
	return s.BotConnected()
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
