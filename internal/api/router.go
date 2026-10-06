package api

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/auth"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/config"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// NewRouter 装配路由与中间件。
//
// 中间件顺序（自外向内）：
//
//	请求 ID → 客户端 IP 解析 → 访问日志 → panic 恢复 → 安全响应头 → HSTS
//	→ 会话装载（可选）→ 认证 → CSRF → 强制改密 → 角色校验
func NewRouter(d Deps) *gin.Engine {
	// 兵底：任何调用方即使漏传 logger 也不应导致空指针 panic（访问日志/panic 恢复都依赖它）。
	if d.Log == nil {
		d.Log = slog.Default()
	}
	gin.SetMode(gin.ReleaseMode)
	server := NewServer(d)

	engine := gin.New()
	// 关闭 gin 自带的代理信任：客户端 IP 由 auth.ClientIP 统一解析，
	// 避免两套逻辑对 X-Forwarded-For 的信任策略不一致。
	_ = engine.SetTrustedProxies(nil)

	engine.Use(
		auth.RequestID(),
		auth.ClientIP(d.Config.TrustedProxies),
		accessLog(d.Log),
		auth.Recover(d.Log),
		auth.SecurityHeaders(),
		auth.HSTS,
	)

	// 健康检查不需要认证。
	engine.GET("/healthz", server.handleHealth)

	api := engine.Group("/api/v1", requestBodyLimit())

	// —— 认证相关 ——
	api.POST("/auth/login", server.handleLogin)
	api.POST("/auth/logout", server.loadSession(), auth.RequireAuth(), d.Sessions.RequireCSRF(), server.handleLogout)
	api.GET("/auth/me", server.loadSession(), auth.RequireAuth(), server.handleMe)
	api.POST("/auth/password", server.loadSession(), auth.RequireAuth(), d.Sessions.RequireCSRF(), server.handleChangePassword)
	// KOOK 一次性码：/login 签发的码在 KOOK 内产生，这里只负责校验与消费。
	api.POST("/auth/login-code", server.handleLoginCode)
	api.POST("/auth/bind-code", server.loadSession(), auth.RequireAuth(), d.Sessions.RequireCSRF(), auth.RequirePasswordChanged(), server.handleBindCode)

	// —— 已登录（且已完成强制改密）——
	secured := api.Group("",
		server.loadSession(),
		auth.RequireAuth(),
		d.Sessions.RequireCSRF(),
		auth.RequirePasswordChanged(),
	)

	// 只读：任意已登录角色
	readonly := secured.Group("", auth.RequireRole(store.RoleReadonly))
	readonly.GET("/tickets", server.handleTicketList)
	readonly.GET("/tickets/:no", server.handleTicketDetail)
	readonly.GET("/tickets/:no/messages", server.handleTicketMessages)
	readonly.GET("/tickets/:no/notes", server.handleTicketNotes)
	readonly.GET("/tickets/:no/export", server.handleTicketExport)
	readonly.GET("/stats/overview", server.handleStatsOverview)
	readonly.GET("/stats/analytics", server.handleStatsAnalytics)
	readonly.GET("/panels", server.handlePanelList)
	readonly.GET("/types", server.handleTypeList)
	readonly.GET("/emoji/rules", server.handleEmojiRuleList)
	readonly.GET("/emoji/grants", server.handleEmojiGrantList)
	readonly.GET("/meta/runtime", server.handleRuntimeInfo)
	readonly.GET("/meta/guild/roles", server.handleGuildRoles)
	readonly.GET("/meta/guild/channels", server.handleGuildChannels)
	// 游戏库与在玩动态：读取对任意已登录角色开放
	readonly.GET("/games", server.handleGameList)
	readonly.GET("/bot/activity", server.handleActivityGet)
	readonly.GET("/events", server.handleEvents)

	// 工单操作：客服及以上
	operator := secured.Group("", auth.RequireRole(store.RoleStaff))
	operator.POST("/tickets/:no/close", server.handleTicketClose)
	operator.POST("/tickets/:no/lock", server.handleTicketLock)
	operator.POST("/tickets/:no/reopen", server.handleTicketReopen)
	operator.POST("/tickets/:no/notes", server.handleTicketAddNote)

	// 管理：仅管理员
	admin := secured.Group("", auth.RequireRole(store.RoleAdmin))
	admin.GET("/settings", server.handleSettingsGet)
	admin.PUT("/settings", server.handleSettingsUpdate)
	admin.GET("/users", server.handleUserList)
	admin.POST("/users", server.handleUserCreate)
	admin.PATCH("/users/:id", server.handleUserUpdate)
	admin.DELETE("/users/:id", server.handleUserDelete)
	admin.GET("/roles/admin", server.handleAdminRoleList)
	admin.POST("/roles/admin", server.handleAdminRoleAdd)
	admin.DELETE("/roles/admin/:roleId", server.handleAdminRoleRemove)
	admin.GET("/roles/mappings", server.handleRoleMappingList)
	// 面板与工单类型管理：需要机器人发出卡片或校验角色，因此归入管理员权限
	admin.POST("/panels", server.handlePanelCreate)
	admin.PATCH("/panels/:id", server.handlePanelUpdate)
	admin.DELETE("/panels/:id", server.handlePanelDelete)
	admin.POST("/panels/:id/refresh", server.handlePanelRefresh)
	// 工单类型：一个类型可对应多个面板，角色挂在类型上
	admin.POST("/types", server.handleTypeCreate)
	admin.PATCH("/types/:id", server.handleTypeUpdate)
	admin.DELETE("/types/:id", server.handleTypeDelete)
	admin.POST("/types/:id/roles", server.handleTypeRoleAdd)
	admin.DELETE("/types/:id/roles/:roleId", server.handleTypeRoleRemove)
	admin.POST("/emoji/rules", server.handleEmojiRuleCreate)
	admin.PATCH("/emoji/rules/:id", server.handleEmojiRuleUpdate)
	admin.DELETE("/emoji/rules/:id", server.handleEmojiRuleDelete)
	admin.PUT("/roles/mappings", server.handleRoleMappingUpsert)
	admin.DELETE("/roles/mappings/:id", server.handleRoleMappingDelete)
	admin.GET("/audit", server.handleAuditList)
	admin.POST("/bot/restart", server.handleBotRestart)
	// 游戏库增删改与在玩动态控制：会变更 KOOK 侧状态，归入管理员权限
	admin.POST("/games", server.handleGameCreate)
	admin.PATCH("/games/:id", server.handleGameUpdate)
	admin.DELETE("/games/:id", server.handleGameDelete)
	admin.POST("/bot/activity", server.handleActivityStart)
	admin.DELETE("/bot/activity", server.handleActivityStop)
	admin.PUT("/bot/activity/settings", server.handleActivitySettings)

	// 静态资源与 SPA 前端。
	if d.Web != nil {
		engine.NoRoute(d.Web.Serve)
	}

	return engine
}

// loadSession 装载会话（未登录不拦截）。
func (s *Server) loadSession() gin.HandlerFunc {
	return s.Sessions.Load(config.SessionCookieName)
}
