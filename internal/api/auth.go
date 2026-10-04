package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"vancekookticket/internal/auth"
	"vancekookticket/internal/config"
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
	return resp
}

// handleLogin 处理账号密码登录。
func (s *Server) handleLogin(c *gin.Context) {
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

	created, err := s.Sessions.Create(user.ID, ip, c.Request.UserAgent())
	if err != nil {
		s.failInternal(c, err, "auth.login.session")
		return
	}
	for _, key := range keys {
		s.Login.Success(key)
	}
	auth.SetSessionCookie(c, config.SessionCookieName, created.Token, s.Config.SessionMaxTTL)
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
	for _, key := range keys {
		if locked, remaining := s.Login.Failure(key); locked {
			s.audit(c, "auth.login.locked", username, fmt.Sprintf("失败次数过多触发锁定（%s）", reason))
			s.fail(c, http.StatusTooManyRequests, "too_many_attempts", "尝试次数过多，账号已临时锁定，请稍后重试")
			return
		} else {
			_ = remaining
		}
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

	if err := s.Store.Users.UpdateFields(user.ID, map[string]any{
		"password_hash":        hash,
		"must_change_password": false,
		"updated_at":           store.Now(),
	}); err != nil {
		s.failInternal(c, err, "auth.password.update")
		return
	}
	s.Login.Success(limitKey)

	if err := s.Sessions.RevokeUser(user.ID); err != nil {
		s.Log.Error("吊销旧会话失败", "user", user.Username, "err", err)
	}
	created, err := s.Sessions.Create(user.ID, auth.ClientIPOf(c), c.Request.UserAgent())
	if err != nil {
		s.failInternal(c, err, "auth.password.session")
		return
	}
	auth.SetSessionCookie(c, config.SessionCookieName, created.Token, s.Config.SessionMaxTTL)

	user.PasswordHash = hash
	user.MustChangePassword = false
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
	s.audit(c, "auth.logout", actorName(c), "登出")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleLoginCode 是一次性登录码的接口占位。
func (s *Server) handleLoginCode(c *gin.Context) {
	s.fail(c, http.StatusNotImplemented, "not_implemented",
		"一次性登录码将在后续里程碑开放：请在 KOOK 私聊机器人发送 /login 获取验证码")
}

// handleBindCode 是绑定码的接口占位。
func (s *Server) handleBindCode(c *gin.Context) {
	s.fail(c, http.StatusNotImplemented, "not_implemented",
		"账号绑定将在后续里程碑开放：请在 KOOK 私聊机器人发送 /bind 获取绑定码")
}
