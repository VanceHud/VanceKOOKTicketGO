package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"vancekookticket/internal/auth"
	"vancekookticket/internal/config"
	"vancekookticket/internal/secure"
	"vancekookticket/internal/store"
)

// notFoundHash 是账号不存在时用于比对的占位 bcrypt 哈希。
// 目的是让“账号不存在”和“密码错误”两条分支的耗时接近，降低账号枚举风险。
const notFoundHash = "$2a$12$ZbW7vaZfawwTLgOqbeE8V.k.rm9mTWjYRHunAFd.gaIW8ecOlpxe."

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type meUser struct {
	ID                 uint       `json:"id"`
	Username           string     `json:"username"`
	DisplayName        string     `json:"displayName"`
	Role               string     `json:"role"`
	MustChangePassword bool       `json:"mustChangePassword"`
	KookUserName       string     `json:"kookUserName,omitempty"`
	LastLoginAt        *time.Time `json:"lastLoginAt,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
}

type meResponse struct {
	User      meUser `json:"user"`
	CSRFToken string `json:"csrfToken"`
	Server    struct {
		Version      string `json:"version"`
		DryRun       bool   `json:"dryRun"`
		BotConnected bool   `json:"botConnected"`
		IdleTimeout  int    `json:"idleTimeoutSeconds"`
		// TicketTimezone 是业务时区（默认 Asia/Shanghai），前端据此展示所有时间。
		TicketTimezone string `json:"ticketTimezone"`
	} `json:"server"`
}

func toMeUser(u *store.WebUser) meUser {
	return meUser{
		ID:                 u.ID,
		Username:           u.Username,
		DisplayName:        u.DisplayName,
		Role:               u.Role,
		MustChangePassword: u.MustChangePassword,
		KookUserName:       u.KookUserName,
		LastLoginAt:        u.LastLoginAt,
		CreatedAt:          u.CreatedAt,
	}
}

func (s *Server) buildMeResponse(c *gin.Context, user *store.WebUser, csrf string) meResponse {
	resp := meResponse{User: toMeUser(user), CSRFToken: csrf}
	resp.Server.Version = s.Version
	resp.Server.DryRun = s.Config.DryRun
	resp.Server.BotConnected = s.botConnected()
	resp.Server.IdleTimeout = int(s.Config.SessionIdleTTL.Seconds())
	resp.Server.TicketTimezone = s.Config.TicketTZ
	return resp
}

// handleLogin 处理账号密码登录。
func (s *Server) handleLogin(c *gin.Context) {
	if !s.passwordAttempts.Allow(auth.ClientIPOf(c)) {
		s.fail(c, http.StatusTooManyRequests, "too_many_attempts", "尝试过于频繁，请稍后再试")
		return
	}
	// 在昂贵的 bcrypt 比对前限制并发，失败次数限流无法拦住同时到达的请求。
	select {
	case s.passwordSlots <- struct{}{}:
		defer func() { <-s.passwordSlots }()
	default:
		s.fail(c, http.StatusTooManyRequests, "too_many_attempts", "登录请求过多，请稍后再试")
		return
	}
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	username := store.NormalizeUsername(req.Username)
	password := req.Password
	if username == "" || password == "" {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请输入用户名与密码")
		return
	}
	if len(username) > 64 || len(password) > auth.MaxPasswordLength {
		s.fail(c, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}

	ip := auth.ClientIPOf(c)
	keys := []string{"ip:" + ip, "user:" + username}
	for _, key := range keys {
		if wait, locked := s.Login.Locked(key); locked {
			s.audit(c, "auth.login.blocked", username, fmt.Sprintf("登录限流锁定中，剩余 %s", wait.Round(time.Second)))
			s.fail(c, http.StatusTooManyRequests, "too_many_attempts",
				fmt.Sprintf("尝试次数过多，请在 %d 秒后重试", int(wait.Seconds())+1))
			return
		}
	}

	user, err := s.Store.Users.ByUsername(username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.failInternal(c, err, "auth.login")
		return
	}

	// 账号不存在时仍执行一次 bcrypt 比对，保持响应时间一致。
	if user == nil {
		auth.VerifyPassword(notFoundHash, password)
		s.recordLoginFailure(c, keys, username, "账号不存在")
		return
	}
	if user.Disabled {
		auth.VerifyPassword(user.PasswordHash, password)
		s.recordLoginFailure(c, keys, username, "账号已禁用")
		return
	}
	if !auth.VerifyPassword(user.PasswordHash, password) {
		s.recordLoginFailure(c, keys, username, "密码错误")
		return
	}

	created, err := s.Sessions.CreateForUser(user, ip, c.Request.UserAgent())
	if err != nil {
		if errors.Is(err, store.ErrAuthenticationChanged) {
			s.recordLoginFailure(c, keys, username, "账号认证信息已变更")
			return
		}
		s.failInternal(c, err, "auth.login.session")
		return
	}
	for _, key := range keys {
		s.Login.Success(key)
	}
	auth.SetSessionCookie(c, config.SessionCookieName, created.Token, s.Config.SessionMaxTTL)
	// 写入“可能已登录”提示 Cookie，让前端在未登录时跳过 /auth/me（避免无意义的 401 噪音）。
	auth.SetLoggedInMarker(c, s.Config.SessionMaxTTL)

	now := store.Now()
	if err := s.Store.Users.TouchLogin(user.ID, now); err != nil {
		s.Log.Error("更新最近登录时间失败", "user", user.Username, "err", err)
	}
	user.LastLoginAt = &now

	s.audit(c, "auth.login.success", user.Username, "登录成功")
	c.JSON(http.StatusOK, s.buildMeResponse(c, user, s.Sessions.CSRFToken(created.Token)))
}

// recordLoginFailure 记录失败计数、审计并返回统一错误。
func (s *Server) recordLoginFailure(c *gin.Context, keys []string, username, reason string) {
	locked := false
	for _, key := range keys {
		keyLocked, _ := s.Login.Failure(key)
		locked = locked || keyLocked
	}
	if locked {
		s.audit(c, "auth.login.locked", username, fmt.Sprintf("失败次数过多触发锁定（%s）", reason))
		s.fail(c, http.StatusTooManyRequests, "too_many_attempts", "尝试次数过多，账号已临时锁定，请稍后重试")
		return
	}
	s.audit(c, "auth.login.failed", username, "登录失败："+reason)
	// 统一提示，不区分账号不存在与密码错误。
	s.fail(c, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
}

// handleMe 返回当前身份与 CSRF 令牌。
//
// CSRF 令牌由服务端密钥派生，同一会话内稳定，前端可随时重新获取，
// 因此刷新页面、多标签页并行调用都不会互相失效。
func (s *Server) handleMe(c *gin.Context) {
	identity := auth.IdentityOf(c)
	user, err := s.Store.Users.ByID(identity.UserID)
	if err != nil {
		s.failStore(c, err, "auth.me")
		return
	}
	c.JSON(http.StatusOK, s.buildMeResponse(c, user, identity.CSRFToken))
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// handleChangePassword 修改当前账号密码。
//
// 安全处理：改密成功后吊销该账号全部会话（含当前会话），再签发一个新会话，
// 这样既能让其它设备立即失效，又不会打断当前操作。
func (s *Server) handleChangePassword(c *gin.Context) {
	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	identity := auth.IdentityOf(c)
	user, err := s.Store.Users.ByID(identity.UserID)
	if err != nil {
		s.failStore(c, err, "auth.password")
		return
	}

	limitKey := "user:" + user.Username
	if wait, locked := s.Login.Locked(limitKey); locked {
		s.fail(c, http.StatusTooManyRequests, "too_many_attempts",
			fmt.Sprintf("操作过于频繁，请在 %d 秒后重试", int(wait.Seconds())+1))
		return
	}
	if !auth.VerifyPassword(user.PasswordHash, req.CurrentPassword) {
		s.Login.Failure(limitKey)
		s.audit(c, "auth.password.failed", user.Username, "当前密码校验失败")
		s.fail(c, http.StatusBadRequest, "invalid_credentials", "当前密码不正确")
		return
	}
	if req.NewPassword == req.CurrentPassword {
		s.fail(c, http.StatusBadRequest, "invalid_request", "新密码不能与当前密码相同")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		// 强度不足等校验错误直接反馈给用户。
		s.fail(c, http.StatusBadRequest, "weak_password", err.Error())
		return
	}

	if err := s.Store.Users.UpdateVerified(user, map[string]any{
		"password_hash":        hash,
		"must_change_password": false,
		"updated_at":           store.Now(),
	}); err != nil {
		if errors.Is(err, store.ErrAuthenticationChanged) {
			s.fail(c, http.StatusUnauthorized, "auth_state_changed", "账号信息已变更，请重新登录")
			return
		}
		s.failInternal(c, err, "auth.password.update")
		return
	}
	s.Login.Success(limitKey)

	// UpdateFields 已在同一事务中完成旧会话吊销。
	user.PasswordHash = hash
	user.MustChangePassword = false
	created, err := s.Sessions.CreateForUser(user, auth.ClientIPOf(c), c.Request.UserAgent())
	if err != nil {
		if errors.Is(err, store.ErrAuthenticationChanged) {
			s.fail(c, http.StatusUnauthorized, "auth_state_changed", "账号信息已变更，请重新登录")
			return
		}
		s.failInternal(c, err, "auth.password.session")
		return
	}
	auth.SetSessionCookie(c, config.SessionCookieName, created.Token, s.Config.SessionMaxTTL)
	auth.SetLoggedInMarker(c, s.Config.SessionMaxTTL)

	s.audit(c, "auth.password.changed", user.Username, "修改密码，已吊销该账号其它会话")
	c.JSON(http.StatusOK, s.buildMeResponse(c, user, s.Sessions.CSRFToken(created.Token)))
}

// handleLogout 注销当前会话。
func (s *Server) handleLogout(c *gin.Context) {
	token, _ := c.Cookie(config.SessionCookieName)
	if err := s.Sessions.Logout(token); err != nil {
		s.Log.Error("注销会话失败", "err", err)
	}
	auth.ClearSessionCookie(c, config.SessionCookieName)
	auth.ClearLoggedInMarker(c)
	s.audit(c, "auth.logout", actorName(c), "登出")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type loginCodeRequest struct {
	Code string `json:"code"`
}

// handleLoginCode 使用 KOOK 一次性登录码登录。
//
// 安全要点：
//   - 验证码在数据库只存哈希，且一次性使用（MarkUsed 带 used_at IS NULL 条件，天然防并发重放）；
//   - 按 IP 限流，避免暴力枚举；
//   - 所有失败原因（不存在/已过期/已使用/用途不符）统一提示，不泄露细节；
//   - 登录时必须验证当前 KOOK 角色；只有显式 DryRun 且无机器人时才使用测试快照；
//   - 未命中角色映射的账号一律拒绝，符合“未命中映射拒绝登录”的约定。
func (s *Server) handleLoginCode(c *gin.Context) {
	if !s.Codes.Allow("login-code:" + auth.ClientIPOf(c)) {
		s.fail(c, http.StatusTooManyRequests, "too_many_attempts", "尝试过于频繁，请稍后再试")
		return
	}

	var req loginCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if code == "" {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请输入登录码")
		return
	}

	record, err := s.Store.Codes.ByCodeHash(secure.HashToken(code))
	if err != nil || record.Purpose != store.CodePurposeLogin || !record.IsUsable(store.Now()) {
		s.audit(c, "auth.login.failed", "login-code", "一次性登录码校验失败")
		s.fail(c, http.StatusUnauthorized, "invalid_code", "登录码无效或已过期")
		return
	}
	role := ""
	if s.Bot != nil {
		resolved, ok, err := s.Bot.ResolveWebRole(c.Request.Context(), record.KookUserID)
		if err != nil {
			s.Log.Warn("登录码权限校验不可用", "err", err)
			s.fail(c, http.StatusServiceUnavailable, "role_verification_unavailable", "暂时无法验证 KOOK 权限，请稍后重试或使用密码登录")
			return
		}
		if ok {
			role = resolved
		}
	} else if s.Config.DryRun {
		role = record.RoleHint
	} else {
		s.fail(c, http.StatusServiceUnavailable, "role_verification_unavailable", "暂时无法验证 KOOK 权限，请稍后重试或使用密码登录")
		return
	}
	if !validRole(role) {
		s.audit(c, "auth.login.failed", record.KookUserID, "KOOK 账号未命中角色映射")
		s.fail(c, http.StatusForbidden, "no_role_mapping", "该 KOOK 账号没有控制台权限，请联系管理员配置角色映射")
		return
	}
	if err := s.Store.Codes.MarkUsed(record.ID, store.Now(), auth.ClientIPOf(c)); err != nil {
		s.audit(c, "auth.login.failed", "login-code", "一次性登录码已被使用或过期")
		s.fail(c, http.StatusUnauthorized, "invalid_code", "登录码无效或已过期")
		return
	}

	user, err := s.Store.Users.ByKookID(record.KookUserID)
	switch {
	case err == nil:
		if user.Disabled {
			s.audit(c, "auth.login.failed", user.Username, "账号已被禁用")
			s.fail(c, http.StatusForbidden, "account_disabled", "账号已被禁用，请联系管理员")
			return
		}
		// 已绑定账号：同步最新角色与显示名
		updates := map[string]any{"updated_at": store.Now()}
		if role != user.Role {
			updates["role"] = role
		}
		if record.KookUserName != "" && record.KookUserName != user.KookUserName {
			updates["kook_user_name"] = record.KookUserName
		}
		if err := s.Store.Users.UpdateFields(user.ID, updates); err != nil {
			s.failStore(c, err, "auth.login_code.update")
			return
		}
		user.Role = role

	case errors.Is(err, store.ErrNotFound):
		// 首次登录：自动创建账号（随机密码，凭据仅用于会话，不对外暴露）
		password, err := secure.RandomHex(24)
		if err != nil {
			s.failInternal(c, err, "auth.login_code.password")
			return
		}
		hash, err := auth.HashPassword(password + "Aa1!")
		if err != nil {
			s.failInternal(c, err, "auth.login_code.hash")
			return
		}
		kookID := record.KookUserID
		created := &store.WebUser{
			Username:     "kook_" + kookID,
			DisplayName:  firstNonEmptyString(record.KookUserName, "KOOK 用户"),
			PasswordHash: hash,
			Role:         role,
			KookUserID:   &kookID,
			KookUserName: record.KookUserName,
		}
		if err := s.Store.Users.Create(created); err != nil {
			s.failStore(c, err, "auth.login_code.create")
			return
		}
		user = created
		s.audit(c, "user.create", user.Username, "KOOK 账号首次登录，自动创建账号，角色："+role)

	default:
		s.failInternal(c, err, "auth.login_code.lookup")
		return
	}

	session, err := s.Sessions.CreateForUser(user, auth.ClientIPOf(c), c.Request.UserAgent())
	if err != nil {
		if errors.Is(err, store.ErrAuthenticationChanged) {
			s.fail(c, http.StatusUnauthorized, "auth_state_changed", "账号信息已变更，请重新登录")
			return
		}
		s.failInternal(c, err, "auth.login_code.session")
		return
	}
	auth.SetSessionCookie(c, config.SessionCookieName, session.Token, s.Config.SessionMaxTTL)
	auth.SetLoggedInMarker(c, s.Config.SessionMaxTTL)

	now := store.Now()
	if err := s.Store.Users.TouchLogin(user.ID, now); err != nil {
		s.Log.Error("更新最近登录时间失败", "user", user.Username, "err", err)
	}
	user.LastLoginAt = &now

	s.audit(c, "auth.login.success", user.Username, "使用 KOOK 一次性登录码登录")
	c.JSON(http.StatusOK, s.buildMeResponse(c, user, s.Sessions.CSRFToken(session.Token)))
}

type bindCodeRequest struct {
	Code string `json:"code"`
}

// handleBindCode 把 KOOK 身份绑定到当前登录的 WebUI 账号。
//
// 绑定本身不改变权限（权限仍由角色映射决定），因此这里只做身份关联与冲突校验。
func (s *Server) handleBindCode(c *gin.Context) {
	if !s.Codes.Allow("bind-code:" + auth.ClientIPOf(c)) {
		s.fail(c, http.StatusTooManyRequests, "too_many_attempts", "尝试过于频繁，请稍后再试")
		return
	}

	var req bindCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if code == "" {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请输入绑定码")
		return
	}

	record, err := s.Store.Codes.ByCodeHash(secure.HashToken(code))
	if err != nil || record.Purpose != store.CodePurposeBind || !record.IsUsable(store.Now()) {
		s.audit(c, "auth.bind.failed", "bind-code", "绑定码校验失败")
		s.fail(c, http.StatusUnauthorized, "invalid_code", "绑定码无效或已过期")
		return
	}

	identity := auth.IdentityOf(c)
	current, err := s.Store.Users.ByID(identity.UserID)
	if err != nil {
		s.failStore(c, err, "auth.bind.user")
		return
	}

	// 该 KOOK 身份不能已绑定其它账号
	if existing, err := s.Store.Users.ByKookID(record.KookUserID); err == nil && existing.ID != current.ID {
		s.audit(c, "auth.bind.failed", current.Username, "该 KOOK 身份已绑定其它账号")
		s.fail(c, http.StatusConflict, "already_bound", "该 KOOK 账号已绑定其它控制台账号")
		return
	}
	// 当前账号不能同时绑定两个 KOOK 身份
	if current.KookUserID != nil && *current.KookUserID != record.KookUserID {
		s.fail(c, http.StatusConflict, "already_bound", "当前账号已绑定其它 KOOK 身份，请先解绑")
		return
	}

	if err := s.Store.Codes.BindToUser(record.ID, current.ID, store.Now(), auth.ClientIPOf(c)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.fail(c, http.StatusUnauthorized, "invalid_code", "绑定码无效或已过期")
		} else {
			s.failStore(c, err, "auth.bind.update")
		}
		return
	}

	updated, err := s.Store.Users.ByID(current.ID)
	if err != nil {
		s.failStore(c, err, "auth.bind.reload")
		return
	}
	s.audit(c, "auth.bind", updated.Username, "绑定 KOOK 身份："+firstNonEmptyString(record.KookUserName, record.KookUserID))
	c.JSON(http.StatusOK, gin.H{"user": toMeUser(updated)})
}

// firstNonEmptyString 返回第一个非空字符串。
func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
