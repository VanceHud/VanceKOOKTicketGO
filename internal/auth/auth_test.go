package auth

import (
	"strings"
	"testing"
	"time"
)

func TestHashAndVerifyPassword(t *testing.T) {
	password := "KookTicket@2026"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("生成哈希失败: %v", err)
	}
	if strings.Contains(hash, password) {
		t.Fatal("哈希中不应包含明文密码")
	}
	if !strings.HasPrefix(hash, "$2a$12$") && !strings.HasPrefix(hash, "$2b$12$") {
		t.Fatalf("期望 bcrypt cost=12 的哈希，得到: %s", hash[:7])
	}
	if !VerifyPassword(hash, password) {
		t.Fatal("正确密码校验失败")
	}
	if VerifyPassword(hash, password+"x") {
		t.Fatal("错误密码不应通过校验")
	}
	if VerifyPassword("", password) || VerifyPassword(hash, "") {
		t.Fatal("空哈希或空密码不应通过校验")
	}
}

func TestValidatePasswordStrength(t *testing.T) {
	weak := []struct {
		name     string
		password string
	}{
		{"过短", "Ab1@xyz"},
		{"仅一类字符", "abcdefghijklmnop"},
		{"单一字符重复", "aaaaaaaaaaaaaaaa"},
		{"常见弱口令", "admin@123456"},
		{"超长", strings.Repeat("Ab1@", 30)},
	}
	for _, tc := range weak {
		if err := ValidatePasswordStrength(tc.password); err == nil {
			t.Errorf("弱密码未被拒绝: %s（%s）", tc.name, tc.password)
		}
	}

	strong := []string{
		"KookTicket@2026",
		"vance-ticket-2026-OK",
		"9f2b#Kq7Lm!x",
	}
	for _, password := range strong {
		if err := ValidatePasswordStrength(password); err != nil {
			t.Errorf("强密码被拒绝: %s（%v）", password, err)
		}
	}
}

func TestGeneratePasswordPolicy(t *testing.T) {
	seen := make(map[string]struct{}, 20)
	for i := 0; i < 20; i++ {
		password, err := GeneratePassword()
		if err != nil {
			t.Fatalf("生成密码失败: %v", err)
		}
		if len([]rune(password)) != 16 {
			t.Fatalf("生成的密码长度应为 16，得到 %d: %s", len([]rune(password)), password)
		}
		if err := ValidatePasswordStrength(password); err != nil {
			t.Fatalf("生成的密码未通过强度校验: %s（%v）", password, err)
		}
		// 便于运维从日志复制：不应包含引号、等号、反斜杠、百分号与空格
		if strings.ContainsAny(password, `"'=\%$`+"`"+` `) {
			t.Fatalf("生成的密码包含不便复制的字符: %s", password)
		}
		if _, duplicated := seen[password]; duplicated {
			t.Fatalf("生成的密码出现重复: %s", password)
		}
		seen[password] = struct{}{}
	}
}

func TestLoginLimiterLocksAfterThreshold(t *testing.T) {
	limiter := NewLoginLimiter(3, time.Minute, time.Minute)
	key := "user:admin"

	if _, locked := limiter.Locked(key); locked {
		t.Fatal("初始状态不应锁定")
	}
	if locked, remaining := limiter.Failure(key); locked || remaining != 2 {
		t.Fatalf("首次失败应为剩余 2 次，得到 locked=%v remaining=%d", locked, remaining)
	}
	limiter.Failure(key)
	if locked, _ := limiter.Failure(key); !locked {
		t.Fatal("达到阈值后应锁定")
	}
	if wait, locked := limiter.Locked(key); !locked || wait <= 0 {
		t.Fatalf("应处于锁定状态且剩余时间大于 0，得到 %v %v", wait, locked)
	}

	// 成功登录应清除计数
	limiter.Success(key)
	if _, locked := limiter.Locked(key); locked {
		t.Fatal("成功登录后应解除锁定")
	}
}

func TestLoginLimiterBackoffGrowsWithRepeatedFailures(t *testing.T) {
	limiter := NewLoginLimiter(1, time.Minute, time.Minute)
	key := "ip:127.0.0.1"

	limiter.Failure(key)
	first, _ := limiter.Locked(key)
	for i := 0; i < 3; i++ {
		limiter.Failure(key)
	}
	second, _ := limiter.Locked(key)

	if second <= first {
		t.Fatalf("持续失败应延长锁定时长：首次 %v，之后 %v", first, second)
	}
}

func TestWindowLimiterRateLimits(t *testing.T) {
	limiter := NewWindowLimiter(2, time.Minute)
	key := "code:user1"

	if !limiter.Allow(key) || !limiter.Allow(key) {
		t.Fatal("前两次调用应被允许")
	}
	if limiter.Allow(key) {
		t.Fatal("超过窗口上限后应被拒绝")
	}
	if !limiter.Allow("code:user2") {
		t.Fatal("不同键之间不应互相影响")
	}

	// GC 只清理窗口外记录，不会重置窗口内的计数
	limiter.GC()
	if limiter.Allow(key) {
		t.Fatal("GC 不应重置窗口内的计数")
	}

	// 窗口过期后应重新放行
	shortWindow := NewWindowLimiter(1, 40*time.Millisecond)
	if !shortWindow.Allow("k") || shortWindow.Allow("k") {
		t.Fatal("短窗口限流逻辑异常")
	}
	time.Sleep(80 * time.Millisecond)
	if !shortWindow.Allow("k") {
		t.Fatal("窗口过期后应重新允许请求")
	}
}

func TestCSRFTokenDerivationAndVerification(t *testing.T) {
	secret := []byte("csrf-secret-for-tests-0123456789")
	manager := &SessionManager{csrfKey: secret}

	tokenA := "session-token-a"
	tokenB := "session-token-b"

	csrfA := manager.CSRFToken(tokenA)
	if csrfA == "" || len(csrfA) != 64 {
		t.Fatalf("CSRF 令牌应为 64 位十六进制，得到 %d 位", len(csrfA))
	}
	// 同一会话内稳定（多标签页并发的关键）
	if manager.CSRFToken(tokenA) != csrfA {
		t.Fatal("同一会话的 CSRF 令牌应当稳定")
	}
	// 不同会话令牌不同
	if manager.CSRFToken(tokenB) == csrfA {
		t.Fatal("不同会话的 CSRF 令牌不应相同")
	}
	// 换个密钥即失效（密钥来自 APP_SECRET）
	other := &SessionManager{csrfKey: []byte("another-secret-key")}
	if other.CSRFToken(tokenA) == csrfA {
		t.Fatal("不同密钥派生的 CSRF 令牌不应相同")
	}

	if !manager.VerifyCSRF(tokenA, csrfA) {
		t.Fatal("正确令牌应通过校验")
	}
	if manager.VerifyCSRF(tokenA, "deadbeef") {
		t.Fatal("错误令牌不应通过校验")
	}
	if manager.VerifyCSRF(tokenA, "") || manager.VerifyCSRF("", csrfA) {
		t.Fatal("空值不应通过校验")
	}
	if manager.VerifyCSRF(tokenB, csrfA) {
		t.Fatal("跨会话令牌不应通过校验")
	}
}
