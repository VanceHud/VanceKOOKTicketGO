package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/secure"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// issueCode 直接在数据库中写入一次性码（等价于机器人在 KOOK 私聊中签发）。
func issueCode(t *testing.T, env *testEnv, code, purpose, kookUserID, kookUserName, roleHint string, ttl time.Duration) *store.AuthCode {
	t.Helper()
	record := &store.AuthCode{
		CodeHash:     secure.HashToken(strings.ToUpper(code)),
		Purpose:      purpose,
		KookUserID:   kookUserID,
		KookUserName: kookUserName,
		RoleHint:     roleHint,
		ExpiresAt:    store.Now().Add(ttl),
		CreatedAt:    store.Now(),
	}
	if err := env.store.Codes.Create(record); err != nil {
		t.Fatalf("写入一次性码失败: %v", err)
	}
	return record
}

func TestLoginCodeRejectsInvalidExpiredAndReused(t *testing.T) {
	env := newTestEnv(t)

	// 1) 不存在的码
	res := env.do(t, http.MethodPost, "/api/v1/auth/login-code", map[string]string{"code": "ZZZZZZ"})
	if res.status != http.StatusUnauthorized || !strings.Contains(res.raw, "invalid_code") {
		t.Fatalf("无效码应返回 401 invalid_code，得到 %d %s", res.status, res.raw)
	}

	// 2) 已过期
	issueCode(t, env, "EXPIRD", store.CodePurposeLogin, "9001", "过期用户", store.RoleStaff, -time.Minute)
	res = env.do(t, http.MethodPost, "/api/v1/auth/login-code", map[string]string{"code": "EXPIRD"})
	if res.status != http.StatusUnauthorized {
		t.Fatalf("过期码应返回 401，得到 %d", res.status)
	}

	// 3) 正常码可登录（大小写不敏感：签发时统一大写）
	issueCode(t, env, "ABC234", store.CodePurposeLogin, "9001", "客服小林", store.RoleStaff, 5*time.Minute)
	res = env.do(t, http.MethodPost, "/api/v1/auth/login-code", map[string]string{"code": "abc234"})
	if res.status != http.StatusOK {
		t.Fatalf("有效码应登录成功，得到 %d %s", res.status, res.raw)
	}
	user, _ := res.body["user"].(map[string]any)
	if user["role"] != store.RoleStaff {
		t.Fatalf("角色应为 staff，得到 %v", user["role"])
	}
	if !strings.HasPrefix(user["username"].(string), "kook_9001") {
		t.Fatalf("首次登录应自动创建 kook_ 前缀账号，得到 %v", user["username"])
	}
	if csrf, _ := res.body["csrfToken"].(string); csrf == "" {
		t.Fatal("应返回 CSRF 令牌")
	}

	// 会话可用
	var sessionCookie *http.Cookie
	for _, cookie := range res.cookies {
		if cookie.Name == "kt_session" {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil {
		t.Fatal("应下发会话 Cookie")
	}
	if tickets := env.do(t, http.MethodGet, "/api/v1/tickets", nil, withCookie(sessionCookie)); tickets.status != http.StatusOK {
		t.Fatalf("一次性码登录后的会话应可用，得到 %d", tickets.status)
	}

	// 4) 重放同一个码必须失败（一次性）
	replay := env.do(t, http.MethodPost, "/api/v1/auth/login-code", map[string]string{"code": "ABC234"})
	if replay.status != http.StatusUnauthorized {
		t.Fatalf("重复使用应失败，得到 %d", replay.status)
	}

	// 审计：应记录失败与成功
	if env.auditCount(t, "auth.login.failed") == 0 {
		t.Fatal("失败的登录码尝试应写入审计")
	}
	if env.auditCount(t, "auth.login.success") == 0 {
		t.Fatal("成功的登录码登录应写入审计")
	}
}

func TestLoginCodeWithoutRoleMappingIsRejected(t *testing.T) {
	env := newTestEnv(t)

	// 签发时未命中映射（RoleHint 为空）
	issueCode(t, env, "NOROLE", store.CodePurposeLogin, "9002", "无权限用户", "", 5*time.Minute)

	res := env.do(t, http.MethodPost, "/api/v1/auth/login-code", map[string]string{"code": "NOROLE"})
	if res.status != http.StatusForbidden || !strings.Contains(res.raw, "no_role_mapping") {
		t.Fatalf("未命中角色映射应返回 403 no_role_mapping，得到 %d %s", res.status, res.raw)
	}

	// 不应创建账号
	if _, err := env.store.Users.ByKookID("9002"); err == nil {
		t.Fatal("未命中映射的账号不应被创建")
	}
}

func TestLoginCodeForbiddenForDisabledAccount(t *testing.T) {
	env := newTestEnv(t)

	// 预置一个已绑定的禁用账号
	kookID := "9003"
	user := env.seedUser(t, "kook_9003", adminPassword, store.RoleStaff, false)
	user.KookUserID = &kookID
	if err := env.store.Users.Save(user); err != nil {
		t.Fatalf("绑定 KOOK 身份失败: %v", err)
	}
	if err := env.store.Users.UpdateFields(user.ID, map[string]any{"disabled": true}); err != nil {
		t.Fatalf("禁用账号失败: %v", err)
	}

	issueCode(t, env, "DISABL", store.CodePurposeLogin, kookID, "被禁用用户", store.RoleStaff, 5*time.Minute)
	res := env.do(t, http.MethodPost, "/api/v1/auth/login-code", map[string]string{"code": "DISABL"})
	if res.status != http.StatusForbidden || !strings.Contains(res.raw, "account_disabled") {
		t.Fatalf("禁用账号应返回 403 account_disabled，得到 %d %s", res.status, res.raw)
	}
}

func TestBindCodeLinksKookIdentity(t *testing.T) {
	env := newTestEnv(t)
	admin := env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)

	// 绑定码用途不符（登录码不能用于绑定）
	issueCode(t, env, "LOGINX", store.CodePurposeLogin, "9001", "客服小林", store.RoleStaff, 5*time.Minute)
	res := env.do(t, http.MethodPost, "/api/v1/auth/bind-code", map[string]string{"code": "LOGINX"},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusUnauthorized {
		t.Fatalf("登录码不能用于绑定，得到 %d", res.status)
	}

	// 正常绑定
	issueCode(t, env, "BIND01", store.CodePurposeBind, "9001", "客服小林", "", 5*time.Minute)
	res = env.do(t, http.MethodPost, "/api/v1/auth/bind-code", map[string]string{"code": "bind01"},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("绑定失败: %d %s", res.status, res.raw)
	}

	updated, err := env.store.Users.ByID(admin.ID)
	if err != nil {
		t.Fatalf("读取账号失败: %v", err)
	}
	if updated.KookUserID == nil || *updated.KookUserID != "9001" {
		t.Fatalf("KOOK 身份未绑定成功: %v", updated.KookUserID)
	}

	// 同一绑定码不能重复使用
	res = env.do(t, http.MethodPost, "/api/v1/auth/bind-code", map[string]string{"code": "BIND01"},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusUnauthorized {
		t.Fatalf("绑定码应一次性，得到 %d", res.status)
	}

	// 其它账号尝试绑定同一个 KOOK 身份 → 409
	env.seedUser(t, "staff2", staffPassword, store.RoleStaff, false)
	cookie2, csrf2 := env.login(t, "staff2", staffPassword)
	issueCode(t, env, "BIND02", store.CodePurposeBind, "9001", "客服小林", "", 5*time.Minute)
	res = env.do(t, http.MethodPost, "/api/v1/auth/bind-code", map[string]string{"code": "BIND02"},
		withCookie(cookie2), withCSRF(csrf2))
	if res.status != http.StatusConflict || !strings.Contains(res.raw, "already_bound") {
		t.Fatalf("同一 KOOK 身份不应绑定多个账号，得到 %d %s", res.status, res.raw)
	}

	// 绑定操作需要 CSRF
	issueCode(t, env, "BIND03", store.CodePurposeBind, "9009", "另一个用户", "", 5*time.Minute)
	res = env.do(t, http.MethodPost, "/api/v1/auth/bind-code", map[string]string{"code": "BIND03"}, withCookie(cookie))
	if res.status != http.StatusForbidden {
		t.Fatalf("绑定需要 CSRF 保护，得到 %d", res.status)
	}
	if env.auditCount(t, "auth.bind") == 0 {
		t.Fatal("绑定成功应写入审计")
	}
}

func TestBindCodeRequiresAuthentication(t *testing.T) {
	env := newTestEnv(t)
	issueCode(t, env, "BINDNA", store.CodePurposeBind, "9001", "客服小林", "", 5*time.Minute)

	res := env.do(t, http.MethodPost, "/api/v1/auth/bind-code", map[string]string{"code": "BINDNA"})
	if res.status != http.StatusUnauthorized {
		t.Fatalf("未登录不应能绑定，得到 %d", res.status)
	}
}

func TestLoginCodeIsRateLimited(t *testing.T) {
	env := newTestEnv(t)

	// 窗口限流为 10 次/10 分钟（见 newTestEnv）
	last := response{}
	for i := 0; i < 12; i++ {
		last = env.do(t, http.MethodPost, "/api/v1/auth/login-code", map[string]string{"code": "AAAAAA"})
	}
	if last.status != http.StatusTooManyRequests {
		t.Fatalf("连续尝试应触发限流，最后一次状态 %d", last.status)
	}
}
