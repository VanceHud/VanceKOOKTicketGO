package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// clearEnv 清空可能影响用例的环境变量，保证测试与外部环境隔离。
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"PORT", "DATA_DIR", "DB_PATH", "LOG_LEVEL", "KOOK_DRYRUN", "KOOK_TOKEN",
		"KOOK_GUILD_ID", "KOOK_API_BASE", "APP_SECRET", "COOKIE_SECURE", "TRUSTED_PROXIES",
		"ADMIN_USERNAME", "ADMIN_PASSWORD", "TICKET_TZ",
		"SESSION_IDLE_HOURS", "SESSION_MAX_DAYS",
		"LOGIN_MAX_FAILS", "LOGIN_WINDOW_MINUTES", "LOGIN_LOCK_MINUTES",
	} {
		t.Setenv(key, "")
		_ = os.Unsetenv(key)
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATA_DIR", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("装载配置失败: %v", err)
	}

	if cfg.Addr != ":9235" {
		t.Errorf("默认端口应为 9235，得到 %s", cfg.Addr)
	}
	if cfg.TicketTZ != "Asia/Shanghai" || cfg.Location.String() != "Asia/Shanghai" {
		t.Errorf("默认业务时区应为北京时间，得到 %s", cfg.TicketTZ)
	}
	if cfg.SessionIdleTTL != 12*time.Hour {
		t.Errorf("默认空闲过期应为 12 小时，得到 %v", cfg.SessionIdleTTL)
	}
	if cfg.SessionMaxTTL != 7*24*time.Hour {
		t.Errorf("默认绝对有效期应为 7 天，得到 %v", cfg.SessionMaxTTL)
	}
	if cfg.SecureCookieMode != SecureCookieAuto {
		t.Errorf("Cookie Secure 策略默认应为 auto，得到 %s", cfg.SecureCookieMode)
	}
	if cfg.DryRun {
		t.Error("默认不应开启 DryRun")
	}
	if len(cfg.AppSecret) < 32 {
		t.Errorf("密钥长度应不少于 32 字节，得到 %d", len(cfg.AppSecret))
	}
}

// TestLoadInterpretsBareNumbersByVariableUnit 是本仓库曾经的真实缺陷的回归测试：
// 早期实现把裸数字一律当作分钟，导致 SESSION_IDLE_HOURS=12 / SESSION_MAX_DAYS=7
// 被解析为 12 分钟与 7 分钟，触发“空闲过期不能大于绝对过期”，Docker Compose 部署直接启动失败。
func TestLoadInterpretsBareNumbersByVariableUnit(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("SESSION_IDLE_HOURS", "12")
	t.Setenv("SESSION_MAX_DAYS", "7")
	t.Setenv("LOGIN_WINDOW_MINUTES", "15")
	t.Setenv("LOGIN_LOCK_MINUTES", "20")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("compose 默认值（SESSION_IDLE_HOURS=12 / SESSION_MAX_DAYS=7）必须能正常启动: %v", err)
	}
	if cfg.SessionIdleTTL != 12*time.Hour {
		t.Errorf("SESSION_IDLE_HOURS=12 应表示 12 小时，得到 %v", cfg.SessionIdleTTL)
	}
	if cfg.SessionMaxTTL != 7*24*time.Hour {
		t.Errorf("SESSION_MAX_DAYS=7 应表示 7 天，得到 %v", cfg.SessionMaxTTL)
	}
	if cfg.LoginWindow != 15*time.Minute || cfg.LoginLockFor != 20*time.Minute {
		t.Errorf("分钟类变量解析错误: window=%v lock=%v", cfg.LoginWindow, cfg.LoginLockFor)
	}
}

func TestLoadAcceptsDurationLiterals(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("SESSION_IDLE_HOURS", "90m")
	t.Setenv("SESSION_MAX_DAYS", "336h") // 14 天

	cfg, err := Load()
	if err != nil {
		t.Fatalf("装载配置失败: %v", err)
	}
	if cfg.SessionIdleTTL != 90*time.Minute {
		t.Errorf("显式 duration 应生效，得到 %v", cfg.SessionIdleTTL)
	}
	if cfg.SessionMaxTTL != 14*24*time.Hour {
		t.Errorf("显式 duration 应生效，得到 %v", cfg.SessionMaxTTL)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{"时长非法", "SESSION_IDLE_HOURS", "abc"},
		{"时长为负", "SESSION_IDLE_HOURS", "-5"},
		{"绝对有效期非法", "SESSION_MAX_DAYS", "一周"},
		{"时区非法", "TICKET_TZ", "Mars/Base"},
		{"日志级别非法", "LOG_LEVEL", "verbose"},
		{"DryRun 非法", "KOOK_DRYRUN", "maybe"},
		{"Cookie 策略非法", "COOKIE_SECURE", "sometimes"},
		{"端口非法", "PORT", "not-a-port"},
		{"限流次数非法", "LOGIN_MAX_FAILS", "0"},
		{"限流窗口为负", "LOGIN_WINDOW_MINUTES", "-1"},
		{"锁定时长为零", "LOGIN_LOCK_MINUTES", "0"},
		{"弱密钥", "APP_SECRET", "short-secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("DATA_DIR", t.TempDir())
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%s 应当被拒绝", tc.key, tc.value)
			}
		})
	}
}

func TestLoadRejectsIdleLongerThanAbsoluteTTL(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("SESSION_IDLE_HOURS", "48")
	t.Setenv("SESSION_MAX_DAYS", "1")

	if _, err := Load(); err == nil {
		t.Fatal("空闲过期大于绝对有效期时应拒绝启动")
	}
}

func TestLoadParsesProxiesDryRunAndPort(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PORT", "18080")
	t.Setenv("KOOK_DRYRUN", "1")
	t.Setenv("KOOK_API_BASE", "http://127.0.0.1:9999")
	t.Setenv("TRUSTED_PROXIES", "127.0.0.1, 172.16.0.0/12;10.0.0.5")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("装载配置失败: %v", err)
	}
	if cfg.Addr != ":18080" {
		t.Errorf("端口解析错误: %s", cfg.Addr)
	}
	if !cfg.DryRun {
		t.Error("KOOK_DRYRUN=1 应开启演示模式")
	}
	if cfg.KookAPIBase != "http://127.0.0.1:9999" {
		t.Errorf("API 地址覆盖失败: %s", cfg.KookAPIBase)
	}
	want := []string{"127.0.0.1", "172.16.0.0/12", "10.0.0.5"}
	if len(cfg.TrustedProxies) != len(want) {
		t.Fatalf("可信代理解析数量不符: %v", cfg.TrustedProxies)
	}
	for i, value := range want {
		if cfg.TrustedProxies[i] != value {
			t.Errorf("可信代理第 %d 项应为 %s，得到 %s", i+1, value, cfg.TrustedProxies[i])
		}
	}
}

// TestSecretFileLifecycle 验证自动生成的密钥文件：权限 0600，且重启后复用同一密钥。
func TestSecretFileLifecycle(t *testing.T) {
	dataDir := t.TempDir()

	clearEnv(t)
	t.Setenv("DATA_DIR", dataDir)
	first, err := Load()
	if err != nil {
		t.Fatalf("首次装载失败: %v", err)
	}

	secretPath := filepath.Join(dataDir, "app_secret")
	info, err := os.Stat(secretPath)
	if err != nil {
		t.Fatalf("应生成密钥文件: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("密钥文件权限应为 0600，实际 %o", perm)
	}
	if !strings.Contains(first.SecretSource, "app_secret") {
		t.Fatalf("密钥来源应指向文件，得到 %s", first.SecretSource)
	}

	// 第二次装载应复用同一密钥（否则已加密的 KOOK Token 会无法解密）
	clearEnv(t)
	t.Setenv("DATA_DIR", dataDir)
	second, err := Load()
	if err != nil {
		t.Fatalf("二次装载失败: %v", err)
	}
	if string(first.AppSecret) != string(second.AppSecret) {
		t.Fatal("重启后应复用同一密钥")
	}

	// 显式提供 APP_SECRET 时优先使用环境变量
	clearEnv(t)
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("APP_SECRET", "an-explicit-secret-from-env-0123456789")
	third, err := Load()
	if err != nil {
		t.Fatalf("三次装载失败: %v", err)
	}
	if third.SecretSource != "env:APP_SECRET" {
		t.Fatalf("应优先使用环境变量密钥，得到 %s", third.SecretSource)
	}
	if string(third.AppSecret) == string(first.AppSecret) {
		t.Fatal("环境变量密钥应覆盖文件密钥")
	}
}

func TestSecretFileRejectsWeakInputWithoutOverwriting(t *testing.T) {
	for _, raw := range []string{"", "short-secret"} {
		t.Run(fmt.Sprintf("length-%d", len(raw)), func(t *testing.T) {
			clearEnv(t)
			dataDir := t.TempDir()
			t.Setenv("DATA_DIR", dataDir)
			path := filepath.Join(dataDir, secretFileName)
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(); err == nil {
				t.Fatal("哈希后的固定长度不能掩盖弱密钥")
			}
			contents, err := os.ReadFile(path)
			if err != nil || string(contents) != raw {
				t.Fatalf("弱密钥不能被自动覆盖: %v", err)
			}
		})
	}
}

func TestExistingSecretFilePermissionsAreHardened(t *testing.T) {
	clearEnv(t)
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	path := filepath.Join(dataDir, secretFileName)
	raw := strings.Repeat("random-secret-example-", 3)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("密钥文件权限未收紧: %v", err)
	}
	if string(cfg.AppSecret) != string(deriveSecret(raw)) {
		t.Fatal("收紧权限不能更换原密钥")
	}
}

func TestSecureCookiePolicy(t *testing.T) {
	cfg := &Config{SecureCookieMode: SecureCookieAuto}
	if cfg.SecureCookie(false) || !cfg.SecureCookie(true) {
		t.Error("auto 模式应跟随请求是否 HTTPS")
	}
	cfg.SecureCookieMode = SecureCookieAlways
	if !cfg.SecureCookie(false) {
		t.Error("always 模式应始终带 Secure")
	}
	cfg.SecureCookieMode = SecureCookieNever
	if cfg.SecureCookie(true) {
		t.Error("never 模式不应带 Secure")
	}
}

func TestRedactsToken(t *testing.T) {
	if got := RedactsToken("1/MTk5/abcdefgh=="); got != "****gh==" {
		t.Errorf("掩码应保留末四位，得到 %s", got)
	}
	if got := RedactsToken(""); got != "(未配置)" {
		t.Errorf("空 Token 应给出明确提示，得到 %s", got)
	}
	if got := RedactsToken("ab"); got != "****" {
		t.Errorf("极短 Token 应完全掩码，得到 %s", got)
	}
}
