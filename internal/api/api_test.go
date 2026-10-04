package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"vancekookticket/internal/auth"
	"vancekookticket/internal/config"
	"vancekookticket/internal/eventbus"
	"vancekookticket/internal/store"
	"vancekookticket/internal/ticket"
	"vancekookticket/web"
)

// ---------------------------------------------------------------------------
// 测试脚手架
// ---------------------------------------------------------------------------

type testEnv struct {
	engine  *gin.Engine
	store   *store.Store
	config  *config.Config
	tickets *ticket.Service
}

const (
	adminPassword    = "AdminTicket@2026"
	staffPassword    = "StaffTicket@2026"
	readonlyPassword = "ReadonlyTicket@2026"
)

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载时区失败: %v", err)
	}

	st, err := store.Open(t.TempDir() + "/ticket.db")
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{
		Addr:             ":0",
		DataDir:          t.TempDir(),
		DBPath:           "test.db",
		Location:         loc,
		TicketTZ:         "Asia/Shanghai",
		AppSecret:        []byte("0123456789abcdef0123456789abcdef"),
		SecureCookieMode: config.SecureCookieNever,
		SessionIdleTTL:   12 * time.Hour,
		SessionMaxTTL:    7 * 24 * time.Hour,
		LoginMaxFails:    3,
		LoginWindow:      time.Minute,
		LoginLockFor:     time.Minute,
		DryRun:           true,
	}

	bus := eventbus.New()
	svc := ticket.NewService(st, bus, ticket.NewNoopPlatform(nil), loc, st.Settings.OutdateHours)
	webHandler, err := web.New()
	if err != nil {
		t.Fatalf("初始化静态资源失败: %v", err)
	}

	// 测试期间静默日志，保持输出可读
	silentLog := slog.New(slog.NewTextHandler(io.Discard, nil))

	engine := NewRouter(Deps{
		Log:      silentLog,
		Config:   cfg,
		Store:    st,
		Bus:      bus,
		Tickets:  svc,
		Sessions: auth.NewSessionManager(st, cfg.SessionIdleTTL, cfg.SessionMaxTTL, cfg.AppSecret),
		Login:    auth.NewLoginLimiter(cfg.LoginMaxFails, cfg.LoginWindow, cfg.LoginLockFor),
		Codes:    auth.NewWindowLimiter(10, time.Minute),
		Web:      webHandler,
		Started:  store.Now(),
		Version:  "test",
	})

	return &testEnv{engine: engine, store: st, config: cfg, tickets: svc}
}

func (e *testEnv) seedUser(t *testing.T, username, password, role string, mustChange bool) *store.WebUser {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("生成密码哈希失败: %v", err)
	}
	user := &store.WebUser{
		Username:           username,
		DisplayName:        username,
		PasswordHash:       hash,
		Role:               role,
		MustChangePassword: mustChange,
	}
	if err := e.store.Users.Create(user); err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}
	return user
}

func (e *testEnv) seedTicket(t *testing.T, status string) *store.Ticket {
	t.Helper()
	ticket := &store.Ticket{
		UserID:    "90000000000000001",
		UserName:  "测试用户",
		ChannelID: "90000000000000099",
		Status:    status,
	}
	if err := e.store.Tickets.CreateWithNo(ticket, store.Now(), e.config.Location); err != nil {
		t.Fatalf("创建工单失败: %v", err)
	}
	return ticket
}

type response struct {
	status  int
	body    map[string]any
	raw     string
	headers http.Header
	cookies []*http.Cookie
}

func (e *testEnv) do(t *testing.T, method, path string, body any, opts ...func(*http.Request)) response {
	t.Helper()

	var payload *strings.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		payload = strings.NewReader(string(encoded))
	} else {
		payload = strings.NewReader("")
	}

	req := httptest.NewRequest(method, path, payload)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, opt := range opts {
		opt(req)
	}

	recorder := httptest.NewRecorder()
	e.engine.ServeHTTP(recorder, req)

	res := response{
		status:  recorder.Code,
		raw:     recorder.Body.String(),
		headers: recorder.Header(),
		cookies: recorder.Result().Cookies(),
	}
	if strings.Contains(recorder.Header().Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(recorder.Body.Bytes(), &res.body)
	}
	return res
}

func withCookie(cookie *http.Cookie) func(*http.Request) {
	return func(req *http.Request) {
		if cookie != nil {
			req.AddCookie(cookie)
		}
	}
}

func withCSRF(token string) func(*http.Request) {
	return func(req *http.Request) { req.Header.Set(auth.HeaderCSRF, token) }
}

// login 登录并返回带会话与 CSRF 令牌的请求选项。
func (e *testEnv) login(t *testing.T, username, password string) (sessionCookie *http.Cookie, csrf string) {
	t.Helper()

	res := e.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": username,
		"password": password,
	})
	if res.status != http.StatusOK {
		t.Fatalf("登录失败: status=%d body=%s", res.status, res.raw)
	}
	for _, cookie := range res.cookies {
		if cookie.Name == config.SessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil {
		t.Fatal("登录响应未下发会话 Cookie")
	}
	csrf, _ = res.body["csrfToken"].(string)
	if csrf == "" {
		t.Fatal("登录响应未返回 CSRF 令牌")
	}
	return sessionCookie, csrf
}

func (e *testEnv) auditCount(t *testing.T, action string) int64 {
	t.Helper()
	var count int64
	if err := e.store.DB().Model(&store.AuditLog{}).Where("action = ?", action).Count(&count).Error; err != nil {
		t.Fatalf("统计审计失败: %v", err)
	}
	return count
}

// ---------------------------------------------------------------------------
// 健康检查与安全响应头
// ---------------------------------------------------------------------------

func TestHealthzIsPublicAndSetsSecurityHeaders(t *testing.T) {
	env := newTestEnv(t)

	res := env.do(t, http.MethodGet, "/healthz", nil)
	if res.status != http.StatusOK {
		t.Fatalf("健康检查应返回 200，得到 %d", res.status)
	}
	if res.body["status"] != "ok" || res.body["db"] != "ok" {
		t.Fatalf("健康检查内容异常: %s", res.raw)
	}

	expected := map[string]string{
		"X-Content-Type-Options":     "nosniff",
		"X-Frame-Options":            "DENY",
		"Referrer-Policy":            "no-referrer",
		"Cross-Origin-Opener-Policy": "same-origin",
	}
	for header, want := range expected {
		if got := res.headers.Get(header); got != want {
			t.Errorf("响应头 %s 应为 %q，得到 %q", header, want, got)
		}
	}
	csp := res.headers.Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("CSP 不符合预期: %s", csp)
	}
	if res.headers.Get(auth.HeaderRequestID) == "" {
		t.Error("响应缺少请求 ID")
	}
}

// ---------------------------------------------------------------------------
// 认证
// ---------------------------------------------------------------------------

func TestProtectedEndpointsRequireAuthentication(t *testing.T) {
	env := newTestEnv(t)
	ticket := env.seedTicket(t, store.TicketOpen)

	paths := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/tickets"},
		{http.MethodGet, "/api/v1/tickets/" + ticket.No},
		{http.MethodGet, "/api/v1/stats/overview"},
		{http.MethodGet, "/api/v1/settings"},
		{http.MethodGet, "/api/v1/users"},
		{http.MethodGet, "/api/v1/audit"},
		{http.MethodGet, "/api/v1/events"},
		{http.MethodPost, "/api/v1/tickets/" + ticket.No + "/close"},
	}
	for _, tc := range paths {
		res := env.do(t, tc.method, tc.path, nil)
		if res.status != http.StatusUnauthorized {
			t.Errorf("%s %s 未登录应返回 401，得到 %d", tc.method, tc.path, res.status)
		}
		if res.body["error"] == nil {
			t.Errorf("%s %s 未返回结构化错误", tc.method, tc.path)
		}
	}
}

func TestLoginFailureIsUniformAndAudited(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)

	// 账号不存在与密码错误必须返回完全相同的信息，避免账号枚举
	notFound := env.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "ghost", "password": "Whatever@2026x",
	})
	wrongPassword := env.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "WrongPassword@2026",
	})

	if notFound.status != http.StatusUnauthorized || wrongPassword.status != http.StatusUnauthorized {
		t.Fatalf("失败登录应返回 401：%d / %d", notFound.status, wrongPassword.status)
	}
	// requestId 每次请求都不同，这里只比较错误码与文案
	notFoundError, _ := notFound.body["error"].(map[string]any)
	wrongPasswordError, _ := wrongPassword.body["error"].(map[string]any)
	if notFoundError["code"] != wrongPasswordError["code"] || notFoundError["message"] != wrongPasswordError["message"] {
		t.Fatalf("账号不存在与密码错误的响应必须一致：\n%s\n%s", notFound.raw, wrongPassword.raw)
	}
	if notFoundError["code"] != "invalid_credentials" {
		t.Fatalf("错误码应为 invalid_credentials，得到 %v", notFoundError["code"])
	}
	if env.auditCount(t, "auth.login.failed") < 2 {
		t.Fatal("失败登录应写入审计日志")
	}
}

func TestLoginIsRateLimitedAfterRepeatedFailures(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)

	// 配置为 3 次失败；前 2 次返回 401，第 3 次达到阈值即触发锁定（返回 429）
	for i := 0; i < 2; i++ {
		res := env.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
			"username": "admin", "password": "WrongPassword@2026",
		})
		if res.status != http.StatusUnauthorized {
			t.Fatalf("第 %d 次失败登录应返回 401，得到 %d", i+1, res.status)
		}
	}
	threshold := env.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "WrongPassword@2026",
	})
	if threshold.status != http.StatusTooManyRequests {
		t.Fatalf("达到失败阈值时应返回 429，得到 %d", threshold.status)
	}

	locked := env.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": adminPassword,
	})
	if locked.status != http.StatusTooManyRequests {
		t.Fatalf("达到阈值后即使是正确密码也应被限流，得到 %d", locked.status)
	}
	if env.auditCount(t, "auth.login.locked") == 0 {
		t.Fatal("触发锁定应写入审计日志")
	}
}

// ---------------------------------------------------------------------------
// CSRF
// ---------------------------------------------------------------------------

func TestWritesRequireCSRFToken(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)
	ticket := env.seedTicket(t, store.TicketOpen)

	// 缺少 CSRF 头
	res := env.do(t, http.MethodPost, "/api/v1/tickets/"+ticket.No+"/lock",
		map[string]string{"reason": "manual"}, withCookie(cookie))
	if res.status != http.StatusForbidden {
		t.Fatalf("缺少 CSRF 令牌的写请求应返回 403，得到 %d", res.status)
	}
	if !strings.Contains(res.raw, "csrf_failed") {
		t.Fatalf("应返回 csrf_failed，得到 %s", res.raw)
	}

	// 伪造 CSRF 头
	res = env.do(t, http.MethodPost, "/api/v1/tickets/"+ticket.No+"/lock",
		map[string]string{"reason": "manual"}, withCookie(cookie), withCSRF("deadbeef"))
	if res.status != http.StatusForbidden {
		t.Fatalf("伪造 CSRF 令牌应返回 403，得到 %d", res.status)
	}

	// 正确 CSRF 头
	res = env.do(t, http.MethodPost, "/api/v1/tickets/"+ticket.No+"/lock",
		map[string]string{"reason": "manual"}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("携带正确 CSRF 令牌应成功，得到 %d body=%s", res.status, res.raw)
	}

	// GET 请求不需要 CSRF
	if res := env.do(t, http.MethodGet, "/api/v1/tickets", nil, withCookie(cookie)); res.status != http.StatusOK {
		t.Fatalf("GET 请求不应要求 CSRF，得到 %d", res.status)
	}
}

func TestCSRFTokenIsStableWithinSession(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)

	// 多次调用 /auth/me 应返回同一令牌（多标签页并发不会互相失效）
	for i := 0; i < 3; i++ {
		res := env.do(t, http.MethodGet, "/api/v1/auth/me", nil, withCookie(cookie))
		if res.status != http.StatusOK {
			t.Fatalf("/auth/me 失败: %d", res.status)
		}
		if got, _ := res.body["csrfToken"].(string); got != csrf {
			t.Fatalf("第 %d 次调用返回的 CSRF 令牌发生变化", i+1)
		}
	}
}

// ---------------------------------------------------------------------------
// 强制改密与会话吊销
// ---------------------------------------------------------------------------

func TestForcedPasswordChangeBlocksOtherEndpoints(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "helper", staffPassword, store.RoleStaff, true)

	cookie, csrf := env.login(t, "helper", staffPassword)

	res := env.do(t, http.MethodGet, "/api/v1/tickets", nil, withCookie(cookie))
	if res.status != http.StatusForbidden || !strings.Contains(res.raw, "password_change_required") {
		t.Fatalf("未改密前应被拦截，得到 %d %s", res.status, res.raw)
	}

	// 改密后放行
	newPassword := "HelperTicket@2026x"
	res = env.do(t, http.MethodPost, "/api/v1/auth/password", map[string]string{
		"currentPassword": staffPassword,
		"newPassword":     newPassword,
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("改密失败: %d %s", res.status, res.raw)
	}
	if mustChange, _ := res.body["user"].(map[string]any)["mustChangePassword"].(bool); mustChange {
		t.Fatal("改密后不应再要求改密")
	}

	// 旧会话必须失效
	if res := env.do(t, http.MethodGet, "/api/v1/tickets", nil, withCookie(cookie)); res.status != http.StatusUnauthorized {
		t.Fatalf("改密后旧会话应失效，得到 %d", res.status)
	}

	// 新会话可用
	newCookie, _ := env.login(t, "helper", newPassword)
	if res := env.do(t, http.MethodGet, "/api/v1/tickets", nil, withCookie(newCookie)); res.status != http.StatusOK {
		t.Fatalf("新会话应可用，得到 %d", res.status)
	}
}

func TestPasswordChangeRequiresCurrentPasswordAndStrength(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)

	res := env.do(t, http.MethodPost, "/api/v1/auth/password", map[string]string{
		"currentPassword": "WrongCurrent@2026",
		"newPassword":     "BrandNewTicket@2026",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("当前密码错误应返回 400，得到 %d", res.status)
	}

	res = env.do(t, http.MethodPost, "/api/v1/auth/password", map[string]string{
		"currentPassword": adminPassword,
		"newPassword":     "123456789012",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest || !strings.Contains(res.raw, "weak_password") {
		t.Fatalf("弱密码应被拒绝，得到 %d %s", res.status, res.raw)
	}

	res = env.do(t, http.MethodPost, "/api/v1/auth/password", map[string]string{
		"currentPassword": adminPassword,
		"newPassword":     adminPassword,
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("新旧密码相同应被拒绝，得到 %d", res.status)
	}
}

// ---------------------------------------------------------------------------
// RBAC
// ---------------------------------------------------------------------------

func TestReadonlyRoleCannotOperateTickets(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "viewer", readonlyPassword, store.RoleReadonly, false)
	cookie, csrf := env.login(t, "viewer", readonlyPassword)
	ticket := env.seedTicket(t, store.TicketOpen)

	if res := env.do(t, http.MethodGet, "/api/v1/tickets", nil, withCookie(cookie)); res.status != http.StatusOK {
		t.Fatalf("只读账号应能查看列表，得到 %d", res.status)
	}

	writes := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/v1/tickets/" + ticket.No + "/close", map[string]string{"note": "x"}},
		{http.MethodPost, "/api/v1/tickets/" + ticket.No + "/lock", map[string]string{"reason": "manual"}},
		{http.MethodPost, "/api/v1/tickets/" + ticket.No + "/reopen", nil},
		{http.MethodPost, "/api/v1/tickets/" + ticket.No + "/notes", map[string]string{"content": "x"}},
	}
	for _, tc := range writes {
		res := env.do(t, tc.method, tc.path, tc.body, withCookie(cookie), withCSRF(csrf))
		if res.status != http.StatusForbidden {
			t.Errorf("只读账号执行 %s 应返回 403，得到 %d", tc.path, res.status)
		}
	}

	// 只读账号不能访问管理接口
	for _, path := range []string{"/api/v1/settings", "/api/v1/users", "/api/v1/audit"} {
		if res := env.do(t, http.MethodGet, path, nil, withCookie(cookie)); res.status != http.StatusForbidden {
			t.Errorf("只读账号访问 %s 应返回 403，得到 %d", path, res.status)
		}
	}

	// 工单状态不应被改动
	loaded, err := env.store.Tickets.ByNo(ticket.No)
	if err != nil {
		t.Fatalf("查询工单失败: %v", err)
	}
	if loaded.Status != store.TicketOpen {
		t.Fatalf("越权请求不应改变工单状态，当前为 %s", loaded.Status)
	}
}

func TestStaffCannotManageAccounts(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "staff", staffPassword, store.RoleStaff, false)
	cookie, csrf := env.login(t, "staff", staffPassword)

	if res := env.do(t, http.MethodGet, "/api/v1/users", nil, withCookie(cookie)); res.status != http.StatusForbidden {
		t.Fatalf("客服不应能查看账号列表，得到 %d", res.status)
	}

	res := env.do(t, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "hacker01", "password": "Hacker@2026x", "role": "admin",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusForbidden {
		t.Fatalf("客服不应能创建账号，得到 %d", res.status)
	}

	var count int64
	if err := env.store.DB().Model(&store.WebUser{}).Where("username = ?", "hacker01").Count(&count).Error; err != nil {
		t.Fatalf("统计账号失败: %v", err)
	}
	if count != 0 {
		t.Fatal("越权创建的账号不应存在")
	}
}

func TestAdminCannotLockSelfOut(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)

	// 不能自我降级
	res := env.do(t, http.MethodPatch, "/api/v1/users/"+itoa(admin.ID), map[string]string{
		"role": store.RoleReadonly,
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusConflict {
		t.Fatalf("自我降级应返回 409，得到 %d %s", res.status, res.raw)
	}

	// 不能自我禁用
	res = env.do(t, http.MethodPatch, "/api/v1/users/"+itoa(admin.ID), map[string]any{
		"disabled": true,
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusConflict {
		t.Fatalf("自我禁用应返回 409，得到 %d", res.status)
	}

	// 不能自删
	res = env.do(t, http.MethodDelete, "/api/v1/users/"+itoa(admin.ID), nil, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusConflict {
		t.Fatalf("自我删除应返回 409，得到 %d", res.status)
	}
}

func TestCannotRemoveLastActiveAdmin(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	other := env.seedUser(t, "admin2", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)

	// 先禁用第二个管理员
	res := env.do(t, http.MethodPatch, "/api/v1/users/"+itoa(other.ID), map[string]any{
		"disabled": true,
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("禁用其它管理员应成功，得到 %d %s", res.status, res.raw)
	}

	// 现在只剩自己一个可用管理员，把自己降级或删除都应被拒绝
	res = env.do(t, http.MethodPatch, "/api/v1/users/"+itoa(admin.ID), map[string]string{
		"role": store.RoleStaff,
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusConflict {
		t.Fatalf("降级最后一个管理员应返回 409，得到 %d", res.status)
	}
}

// ---------------------------------------------------------------------------
// 工单状态机与导出
// ---------------------------------------------------------------------------

func TestTicketStateMachineThroughAPI(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)
	ticket := env.seedTicket(t, store.TicketOpen)

	// 锁定
	res := env.do(t, http.MethodPost, "/api/v1/tickets/"+ticket.No+"/lock",
		map[string]string{"reason": "manual"}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK || res.body["status"] != store.TicketLocked {
		t.Fatalf("锁定失败: %d %s", res.status, res.raw)
	}

	// 重复锁定 → 409
	res = env.do(t, http.MethodPost, "/api/v1/tickets/"+ticket.No+"/lock",
		map[string]string{"reason": "manual"}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusConflict {
		t.Fatalf("重复锁定应返回 409，得到 %d", res.status)
	}

	// 重新激活
	res = env.do(t, http.MethodPost, "/api/v1/tickets/"+ticket.No+"/reopen", nil,
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK || res.body["status"] != store.TicketOpen {
		t.Fatalf("重新激活失败: %d %s", res.status, res.raw)
	}

	// 未锁定工单重新激活 → 409
	res = env.do(t, http.MethodPost, "/api/v1/tickets/"+ticket.No+"/reopen", nil,
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusConflict {
		t.Fatalf("对未锁定工单执行激活应返回 409，得到 %d", res.status)
	}

	// 关闭
	res = env.do(t, http.MethodPost, "/api/v1/tickets/"+ticket.No+"/close",
		map[string]string{"note": "已解决"}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK || res.body["status"] != store.TicketClosed {
		t.Fatalf("关闭失败: %d %s", res.status, res.raw)
	}

	// 重复关闭 → 409
	res = env.do(t, http.MethodPost, "/api/v1/tickets/"+ticket.No+"/close",
		map[string]string{"note": "再关一次"}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusConflict {
		t.Fatalf("重复关闭应返回 409，得到 %d", res.status)
	}

	// 关闭后不能锁定
	res = env.do(t, http.MethodPost, "/api/v1/tickets/"+ticket.No+"/lock",
		map[string]string{"reason": "manual"}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusConflict {
		t.Fatalf("已关闭工单不应能再锁定，得到 %d", res.status)
	}

	// 状态变更都应有审计
	for _, action := range []string{"ticket.lock", "ticket.reopen", "ticket.close"} {
		if env.auditCount(t, action) == 0 {
			t.Errorf("缺少审计记录: %s", action)
		}
	}
}

func TestInvalidTicketNumberIsReportedAsNotFound(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, _ := env.login(t, "admin", adminPassword)

	for _, no := range []string{"INVALID", "TK-260105-ABC", "TK-260105-ABCDEF", "1"} {
		res := env.do(t, http.MethodGet, "/api/v1/tickets/"+no, nil, withCookie(cookie))
		if res.status != http.StatusNotFound {
			t.Errorf("非法编号 %q 应返回 404，得到 %d", no, res.status)
		}
	}
}

func TestTicketExportFormats(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, _ := env.login(t, "admin", adminPassword)
	ticket := env.seedTicket(t, store.TicketOpen)

	if err := env.store.Tickets.AddMessage(&store.TicketMessage{
		TicketNo:  ticket.No,
		MsgID:     "90000000000000777",
		UserID:    ticket.UserID,
		UserName:  "测试用户",
		Content:   "我的订单没有到账 <script>alert(1)</script>",
		MsgType:   store.MsgTypeText,
		CreatedAt: store.Now(),
	}); err != nil {
		t.Fatalf("写入消息失败: %v", err)
	}

	// JSON
	res := env.do(t, http.MethodGet, "/api/v1/tickets/"+ticket.No+"/export?format=json", nil, withCookie(cookie))
	if res.status != http.StatusOK {
		t.Fatalf("JSON 导出失败: %d", res.status)
	}
	if !strings.Contains(res.headers.Get("Content-Disposition"), ticket.No) {
		t.Error("导出应设置文件名")
	}

	// CSV：需带 UTF-8 BOM，便于 Excel 识别
	res = env.do(t, http.MethodGet, "/api/v1/tickets/"+ticket.No+"/export?format=csv", nil, withCookie(cookie))
	if res.status != http.StatusOK || !strings.HasPrefix(res.raw, "\ufeff") {
		t.Fatalf("CSV 导出应带 BOM: %d", res.status)
	}

	// HTML：聊天内容必须被转义，不能原样输出脚本
	res = env.do(t, http.MethodGet, "/api/v1/tickets/"+ticket.No+"/export?format=html", nil, withCookie(cookie))
	if res.status != http.StatusOK {
		t.Fatalf("HTML 导出失败: %d", res.status)
	}
	if strings.Contains(res.raw, "<script>alert(1)</script>") {
		t.Fatal("HTML 导出未对聊天内容转义，存在 XSS 风险")
	}
	if !strings.Contains(res.raw, "&lt;script&gt;") {
		t.Fatal("HTML 导出应包含转义后的内容")
	}

	// 不支持的格式
	res = env.do(t, http.MethodGet, "/api/v1/tickets/"+ticket.No+"/export?format=pdf", nil, withCookie(cookie))
	if res.status != http.StatusBadRequest {
		t.Fatalf("不支持的导出格式应返回 400，得到 %d", res.status)
	}

	// 导出行为需审计
	if env.auditCount(t, "ticket.export") == 0 {
		t.Fatal("导出操作应写入审计日志")
	}
}

// ---------------------------------------------------------------------------
// 设置与敏感字段
// ---------------------------------------------------------------------------

func TestKookTokenIsWriteOnly(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)

	plaintext := "kook-bot-token-abcdefghijklmnop"

	res := env.do(t, http.MethodPut, "/api/v1/settings", map[string]any{
		"kookToken":    plaintext,
		"guildId":      "1000000000000001",
		"guildName":    "演示服务器",
		"outdateHours": 24,
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("更新设置失败: %d %s", res.status, res.raw)
	}

	// 响应与读取接口都不得包含明文
	got := env.do(t, http.MethodGet, "/api/v1/settings", nil, withCookie(cookie))
	if strings.Contains(got.raw, plaintext) {
		t.Fatal("设置接口不得回显 token 明文")
	}
	if masked, _ := got.body["kookTokenMasked"].(string); masked == plaintext || masked == "" {
		t.Fatalf("应返回掩码：%q", masked)
	}
	if has, _ := got.body["hasKookToken"].(bool); !has {
		t.Fatal("应标记已配置 token")
	}
	if hours, _ := got.body["outdateHours"].(float64); hours != 24 {
		t.Fatalf("超时配置未保存：%v", got.body["outdateHours"])
	}

	// 数据库中也不得出现明文
	raw, _, err := env.store.Settings.Get(store.SettingKookToken)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if strings.Contains(raw, plaintext) {
		t.Fatal("数据库中不得保存 token 明文")
	}

	// 审计只记录“已更新”，不含内容
	var entry store.AuditLog
	if err := env.store.DB().Where("action = ?", "settings.update").First(&entry).Error; err != nil {
		t.Fatalf("读取审计失败: %v", err)
	}
	if strings.Contains(entry.Detail, plaintext) {
		t.Fatal("审计日志不得包含 token 明文")
	}
}

func TestSettingsRejectsInvalidValues(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"非法服务器 ID", map[string]any{"guildId": "not-a-number"}},
		{"负数超时时间", map[string]any{"outdateHours": -1}},
		{"超时时间越界", map[string]any{"outdateHours": 9999}},
		{"名称过长", map[string]any{"guildName": strings.Repeat("a", 65)}},
	}
	for _, tc := range cases {
		res := env.do(t, http.MethodPut, "/api/v1/settings", tc.body, withCookie(cookie), withCSRF(csrf))
		if res.status != http.StatusBadRequest {
			t.Errorf("%s 应返回 400，得到 %d %s", tc.name, res.status, res.raw)
		}
	}
}

// ---------------------------------------------------------------------------
// 路由兜底
// ---------------------------------------------------------------------------

func TestUnknownAPIPathReturnsJSONNotFound(t *testing.T) {
	env := newTestEnv(t)

	res := env.do(t, http.MethodGet, "/api/v1/does-not-exist", nil)
	if res.status != http.StatusNotFound {
		t.Fatalf("未知接口应返回 404，得到 %d", res.status)
	}
	if !strings.Contains(res.raw, "not_found") {
		t.Fatalf("未知接口应返回结构化 404，得到 %s", res.raw)
	}
}

func TestNonAPIPathFallsBackToSPA(t *testing.T) {
	env := newTestEnv(t)

	// 测试环境未内嵌前端产物，web 处理器会返回引导页（503）；关键是不要返回 404 JSON
	res := env.do(t, http.MethodGet, "/tickets/TK-260105-ABCD", nil)
	if res.status == http.StatusNotFound {
		t.Fatalf("SPA 深链接不应返回 404，得到 %d", res.status)
	}
	if !strings.Contains(res.headers.Get("Content-Type"), "text/html") {
		t.Fatalf("SPA 兜底应返回 HTML，得到 %s", res.headers.Get("Content-Type"))
	}
	if res.headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("SPA 入口不应被缓存，得到 %s", res.headers.Get("Cache-Control"))
	}
}

func TestPanicIsRecoveredWithoutLeakingDetails(t *testing.T) {
	env := newTestEnv(t)

	// 注册一个必定 panic 的路由，验证 recover 中间件的行为
	env.engine.GET("/api/v1/panic-test", func(*gin.Context) {
		panic("内部实现细节：数据库连接串 postgres://user:pass@host/db")
	})

	res := env.do(t, http.MethodGet, "/api/v1/panic-test", nil)
	if res.status != http.StatusInternalServerError {
		t.Fatalf("panic 应返回 500，得到 %d", res.status)
	}
	if strings.Contains(res.raw, "postgres://") || strings.Contains(res.raw, "goroutine") {
		t.Fatalf("响应不得泄露内部细节: %s", res.raw)
	}
	if !strings.Contains(res.raw, "internal_error") {
		t.Fatalf("应返回通用错误码，得到 %s", res.raw)
	}
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

func itoa(value uint) string { return strconv.FormatUint(uint64(value), 10) }
