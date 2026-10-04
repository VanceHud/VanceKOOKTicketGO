// Package config 负责从环境变量装载运行配置。
//
// 安全约定：
//   - 密钥类配置（APP_SECRET / 管理员初始密码）只从环境变量或数据目录下的 0600 文件读取，
//     不落库、不返回给 WebUI、不写入日志。
//   - KOOK_TOKEN 只用于装载进数据库的加密字段；所有日志都必须避免打印其内容。
package config

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 会话 Cookie 名称。
const SessionCookieName = "kt_session"

// SecureCookieMode 描述会话 Cookie 的 Secure 属性策略。
type SecureCookieMode string

const (
	// SecureCookieAuto 依据当前请求是否为 HTTPS（直连或可信反代）动态决定。
	SecureCookieAuto SecureCookieMode = "auto"
	// SecureCookieAlways 始终带上 Secure，适用于强制 HTTPS 的部署。
	SecureCookieAlways SecureCookieMode = "always"
	// SecureCookieNever 始终不带 Secure，仅用于本地 http 调试。
	SecureCookieNever SecureCookieMode = "never"
)

// Config 是服务运行所需的全部非持久化配置。
//
// 可持久化的业务配置（频道 ID、超时小时数等）保存在数据库 settings 表中，
// 由 WebUI 维护，不放在这里。
type Config struct {
	Addr     string
	DataDir  string
	DBPath   string
	LogLevel slog.Level
	DryRun   bool

	// KookToken 仅在提供 KOOK_TOKEN 时非空，用于首次装载到数据库加密字段。
	KookToken string
	// KookTokenFromEnv 标记 token 来自环境变量（环境变量优先于数据库中的旧值）。
	KookTokenFromEnv bool
	KookGuildID      string
	// KookAPIBase 可覆盖 KOOK API 地址（默认官方地址）。
	KookAPIBase string

	// AppSecret 用于加密存储敏感字段（KOOK token）与派生会话相关密钥。
	AppSecret []byte
	// SecretSource 记录密钥来源，便于运维排查（env 或文件路径）。
	SecretSource string

	SecureCookieMode SecureCookieMode
	TrustedProxies   []string

	AdminUsername string
	// AdminPassword 仅在首次初始化管理员时使用；已存在管理员时忽略。
	AdminPassword string
	// AdminPasswordGenerated 标记密码是本次随机生成的，需要打印一次并强制改密。
	AdminPasswordGenerated bool

	// TicketTZ 与 Location 决定工单编号日期段使用的时区。
	TicketTZ string
	Location *time.Location

	SessionIdleTTL time.Duration
	SessionMaxTTL  time.Duration

	LoginMaxFails int
	LoginWindow   time.Duration
	LoginLockFor  time.Duration
}

const secretFileMode = 0o600
const secretFileName = "app_secret"

// Load 解析环境变量并校验配置。
func Load() (*Config, error) {
	c := &Config{
		Addr:             ":" + env("PORT", "8080"),
		DataDir:          env("DATA_DIR", "./data"),
		SecureCookieMode: SecureCookieMode(strings.ToLower(env("COOKIE_SECURE", string(SecureCookieAuto)))),
		AdminUsername:    env("ADMIN_USERNAME", "admin"),
		AdminPassword:    os.Getenv("ADMIN_PASSWORD"),
		KookToken:        strings.TrimSpace(os.Getenv("KOOK_TOKEN")),
		KookGuildID:      strings.TrimSpace(os.Getenv("KOOK_GUILD_ID")),
		KookAPIBase:      strings.TrimSpace(os.Getenv("KOOK_API_BASE")),
		TicketTZ:         env("TICKET_TZ", "Asia/Shanghai"),
	}

	c.KookTokenFromEnv = c.KookToken != ""
	c.DBPath = env("DB_PATH", filepath.Join(c.DataDir, "ticket.db"))

	level, err := parseLogLevel(env("LOG_LEVEL", "info"))
	if err != nil {
		return nil, err
	}
	c.LogLevel = level

	dryRun, err := envBool("KOOK_DRYRUN", false)
	if err != nil {
		return nil, err
	}
	c.DryRun = dryRun

	switch c.SecureCookieMode {
	case SecureCookieAuto, SecureCookieAlways, SecureCookieNever:
	default:
		return nil, fmt.Errorf("COOKIE_SECURE 只能是 auto/always/never，当前为 %q", c.SecureCookieMode)
	}

	c.TrustedProxies = splitList(os.Getenv("TRUSTED_PROXIES"))

	if c.Location, err = time.LoadLocation(c.TicketTZ); err != nil {
		return nil, fmt.Errorf("TICKET_TZ=%q 无法加载，请确认容器内已安装 tzdata: %w", c.TicketTZ, err)
	}

	if c.SessionIdleTTL, err = envDuration("SESSION_IDLE_HOURS", 12*time.Hour); err != nil {
		return nil, err
	}
	if c.SessionMaxTTL, err = envDuration("SESSION_MAX_DAYS", 7*24*time.Hour); err != nil {
		return nil, err
	}
	if c.LoginMaxFails, err = envInt("LOGIN_MAX_FAILS", 5); err != nil {
		return nil, err
	}
	if c.LoginWindow, err = envDuration("LOGIN_WINDOW_MINUTES", 15*time.Minute); err != nil {
		return nil, err
	}
	if c.LoginLockFor, err = envDuration("LOGIN_LOCK_MINUTES", 15*time.Minute); err != nil {
		return nil, err
	}

	if c.AppSecret, c.SecretSource, err = loadSecret(c.DataDir); err != nil {
		return nil, err
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	if c.LoginMaxFails < 1 {
		return fmt.Errorf("LOGIN_MAX_FAILS 必须 >= 1")
	}
	if c.SessionIdleTTL <= 0 || c.SessionMaxTTL <= 0 {
		return fmt.Errorf("会话有效期必须为正数")
	}
	if c.SessionIdleTTL > c.SessionMaxTTL {
		return fmt.Errorf("SESSION_IDLE_HOURS 不能大于 SESSION_MAX_DAYS")
	}
	if len(c.AppSecret) < 32 {
		return fmt.Errorf("APP_SECRET 太短，至少需要 32 字节")
	}
	return nil
}

// loadSecret 依次从 APP_SECRET 环境变量、数据目录的 app_secret 文件读取密钥；
// 两者都不存在时生成一个随机密钥并以 0600 权限落盘，保证重启后仍能解密已存储的 token。
func loadSecret(dataDir string) ([]byte, string, error) {
	if raw := strings.TrimSpace(os.Getenv("APP_SECRET")); raw != "" {
		secret := deriveSecret(raw)
		return secret, "env:APP_SECRET", nil
	}

	path := filepath.Join(dataDir, secretFileName)
	if data, err := os.ReadFile(path); err == nil {
		if secret := deriveSecret(strings.TrimSpace(string(data))); len(secret) >= 32 {
			return secret, "file:" + path, nil
		}
		return nil, "", fmt.Errorf("%s 内容过短，请删除该文件后重新启动以生成新密钥", path)
	} else if !os.IsNotExist(err) {
		return nil, "", fmt.Errorf("读取 %s 失败: %w", path, err)
	}

	raw := make([]byte, 48)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("生成 APP_SECRET 失败: %w", err)
	}
	encoded := hex.EncodeToString(raw)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, "", fmt.Errorf("创建数据目录 %s 失败: %w", dataDir, err)
	}
	if err := os.WriteFile(path, []byte(encoded+"\n"), secretFileMode); err != nil {
		return nil, "", fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	return deriveSecret(encoded), "file:" + path + " (本次生成)", nil
}

// deriveSecret 把任意长度的口令拉伸成 32 字节密钥。
func deriveSecret(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// RedactsToken 返回可安全打印的 token 摘要，例如 "Bot ****a1b2"。
func RedactsToken(token string) string {
	if token == "" {
		return "(未配置)"
	}
	if len(token) <= 4 {
		return "****"
	}
	return "****" + token[len(token)-4:]
}

// SecureCookie 判断当前请求是否应当携带 Secure 属性。
// https 由调用方判定（包含可信反代场景），避免在此处信任请求头。
func (c *Config) SecureCookie(isHTTPS bool) bool {
	switch c.SecureCookieMode {
	case SecureCookieAlways:
		return true
	case SecureCookieNever:
		return false
	default:
		return isHTTPS
	}
}

// ConstantTimeEqual 长度不等时同样保持时间无关的比较。
func ConstantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		// 仍然执行一次比较，避免通过耗时区分长度。
		subtle.ConstantTimeCompare([]byte(a), []byte(a))
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s 必须是整数: %w", key, err)
	}
	return v, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	if v, err := strconv.Atoi(raw); err == nil {
		return time.Duration(v) * time.Minute, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s 需要是分钟数或 Go duration（如 12h）: %w", key, err)
	}
	return d, nil
}

func envBool(key string, def bool) (bool, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch raw {
	case "":
		return def, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s 需要是布尔值（1/0/true/false）", key)
	}
}

func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("LOG_LEVEL 只能为 debug/info/warn/error，当前为 %q", raw)
	}
}

func splitList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' '
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
