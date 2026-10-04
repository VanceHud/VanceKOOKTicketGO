// Package auth 实现 WebUI 的认证、授权与会话管理。
//
// 安全基线（每条都有对应实现，请勿绕过）：
//   - 密码使用 bcrypt(cost=12) 哈希，数据库不保存明文；
//   - 会话与一次性码在数据库中只保存 SHA-256 摘要；
//   - 会话登录后轮换、空闲与绝对双过期、账号级并发会话上限；
//   - 改密 / 改角色 / 禁用账号会吊销该账号全部会话；
//   - 登录失败按 IP 与账号两个维度限流并指数退避；
//   - 非幂等请求强制 CSRF 校验（服务端存储令牌哈希 + 自定义头）。
package auth

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

// BcryptCost 是 bcrypt 代价因子；12 在本项目规模下兼顾安全与登录延迟（约 200ms）。
const BcryptCost = 12

// 密码策略。
const (
	MinPasswordLength = 12
	// MaxPasswordLength 受 bcrypt 72 字节上限约束，显式限制避免 GenerateFromPassword 报错。
	MaxPasswordLength = 72
)

// ErrPasswordTooWeak 表示密码不符合强度要求。
var ErrPasswordTooWeak = errors.New("密码强度不足")

// weakPasswords 是常见弱密码样本，用于拦截“Admin@123456”这类看似复杂但极易命中的口令。
var weakPasswords = map[string]struct{}{
	"password":      {},
	"password123":   {},
	"passw0rd":      {},
	"p@ssw0rd":      {},
	"administrator": {},
	"admin123456":   {},
	"admin@123456":  {},
	"123456789012":  {},
	"qwertyuiop123": {},
	"1qaz2wsx3edc":  {},
	"iloveyou123":   {},
	"letmein12345":  {},
	"welcome12345":  {},
	"changeme1234":  {},
	"kookticket123": {},
}

// HashPassword 生成 bcrypt 哈希。
func HashPassword(plain string) (string, error) {
	if err := ValidatePasswordStrength(plain); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), BcryptCost)
	if err != nil {
		return "", fmt.Errorf("生成密码哈希失败: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword 校验密码；哈希非法或密码错误都返回 false。
func VerifyPassword(hash, plain string) bool {
	if hash == "" || plain == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// ValidatePasswordStrength 校验密码强度：
// 长度 12–72 字节、至少包含两类字符、不能是常见弱密码、不能是单一字符重复。
func ValidatePasswordStrength(plain string) error {
	if plain == "" {
		return fmt.Errorf("%w：密码不能为空", ErrPasswordTooWeak)
	}
	if len(plain) < MinPasswordLength {
		return fmt.Errorf("%w：至少需要 %d 个字符", ErrPasswordTooWeak, MinPasswordLength)
	}
	// bcrypt 只处理前 72 字节，超长部分会被静默忽略，因此必须显式拒绝。
	if len(plain) > MaxPasswordLength {
		return fmt.Errorf("%w：不能超过 %d 字节", ErrPasswordTooWeak, MaxPasswordLength)
	}

	var hasUpper, hasLower, hasDigit, hasSymbol bool
	for _, r := range plain {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		default:
			hasSymbol = true
		}
	}
	classes := 0
	for _, ok := range []bool{hasUpper, hasLower, hasDigit, hasSymbol} {
		if ok {
			classes++
		}
	}
	if classes < 2 {
		return fmt.Errorf("%w：至少包含大写字母、小写字母、数字、符号中的两类", ErrPasswordTooWeak)
	}
	if isSingleRuneRepeated(plain) {
		return fmt.Errorf("%w：不能是同一个字符的重复", ErrPasswordTooWeak)
	}
	if _, weak := weakPasswords[strings.ToLower(strings.TrimSpace(plain))]; weak {
		return fmt.Errorf("%w：该密码过于常见，请更换", ErrPasswordTooWeak)
	}
	return nil
}

func isSingleRuneRepeated(s string) bool {
	runes := []rune(s)
	if len(runes) < 2 {
		return true
	}
	for _, r := range runes[1:] {
		if r != runes[0] {
			return false
		}
	}
	return true
}

// 生成随机密码使用的字符集。
//
// 符号集刻意排除了 = 双引号 单引号 反斜杠 $ 反引号 % 与空格：
// 这些字符会让日志输出被引号包裹，或在 shell / printf / 配置文件中需要转义，
// 影响运维从容器日志里直接复制初始密码。
const (
	pwUpper   = "ABCDEFGHJKLMNPQRSTUVWXYZ"  // 去掉 I、O，避免与 1、0 混淆
	pwLower   = "abcdefghijkmnopqrstuvwxyz" // 去掉 l，避免与 1 混淆
	pwDigits  = "23456789"                  // 去掉 0、1
	pwSymbols = "!@#^*()-_+.:?~"
)

// GeneratePassword 生成 16 位随机初始密码，保证四类字符各至少出现一次。
func GeneratePassword() (string, error) {
	const length = 16
	classes := []string{pwUpper, pwLower, pwDigits, pwSymbols}

	out := make([]byte, 0, length)
	for _, class := range classes {
		ch, err := randomChar(class)
		if err != nil {
			return "", err
		}
		out = append(out, ch)
	}

	all := pwUpper + pwLower + pwDigits + pwSymbols
	for len(out) < length {
		ch, err := randomChar(all)
		if err != nil {
			return "", err
		}
		out = append(out, ch)
	}

	// Fisher–Yates 洗牌，避免“前四位固定为每类一个”的可预测前缀。
	for i := len(out) - 1; i > 0; i-- {
		j, err := randomIndex(i + 1)
		if err != nil {
			return "", err
		}
		out[i], out[j] = out[j], out[i]
	}
	return string(out), nil
}

// randomChar 使用拒绝采样从字符集中取一个字符，避免取模偏置。
func randomChar(alphabet string) (byte, error) {
	n := len(alphabet)
	if n == 0 {
		return 0, fmt.Errorf("字符集为空")
	}
	limit := 256 - (256 % n)
	buf := make([]byte, 1)
	for {
		if _, err := rand.Read(buf); err != nil {
			return 0, err
		}
		if int(buf[0]) < limit {
			return alphabet[int(buf[0])%n], nil
		}
	}
}

// randomIndex 使用拒绝采样返回 [0, n) 内的随机下标。
func randomIndex(n int) (int, error) {
	if n <= 0 {
		return 0, fmt.Errorf("取值范围必须为正数")
	}
	limit := 256 - (256 % n)
	buf := make([]byte, 1)
	for {
		if _, err := rand.Read(buf); err != nil {
			return 0, err
		}
		if int(buf[0]) < limit {
			return int(buf[0]) % n, nil
		}
	}
}
