package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/secure"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// 会话参数。
const (
	// SessionTokenBytes 是会话 token 的随机字节数（256 位熵）。
	SessionTokenBytes = 32
	// MaxSessionsPerUser 限制单账号并发会话数，超出时淘汰最旧会话。
	MaxSessionsPerUser = 5
)

// 认证相关错误，由 handler 映射为统一的未认证响应。
var (
	ErrSessionExpired = errors.New("会话已过期")
	ErrUserDisabled   = errors.New("账号已被禁用")
)

// SessionManager 负责会话的创建、校验与吊销。
type SessionManager struct {
	store   *store.Store
	idleTTL time.Duration
	maxTTL  time.Duration
	// csrfKey 用于派生 CSRF 令牌，来自 APP_SECRET。
	csrfKey []byte
}

// CreatedSession 是新建会话的返回值。
// Token 的明文只在此处返回一次，数据库仅保存其摘要。
type CreatedSession struct {
	Token   string
	Session *store.Session
}

// NewSessionManager 创建会话管理器；csrfSecret 用于派生 CSRF 令牌。
func NewSessionManager(st *store.Store, idleTTL, maxTTL time.Duration, csrfSecret []byte) *SessionManager {
	return &SessionManager{store: st, idleTTL: idleTTL, maxTTL: maxTTL, csrfKey: csrfSecret}
}

// IdleTTL 返回空闲过期时长。
func (m *SessionManager) IdleTTL() time.Duration { return m.idleTTL }

// Create 新建会话。
func (m *SessionManager) Create(userID uint, ip, userAgent string) (*CreatedSession, error) {
	user, err := m.store.Users.ByID(userID)
	if err != nil {
		return nil, err
	}
	return m.CreateForUser(user, ip, userAgent)
}

// CreateForUser 将经过认证的账号快照与创建会话时的状态作事务内比较。
func (m *SessionManager) CreateForUser(user *store.WebUser, ip, userAgent string) (*CreatedSession, error) {
	token, err := secure.RandomHex(SessionTokenBytes)
	if err != nil {
		return nil, err
	}

	now := store.Now()
	sess := &store.Session{
		TokenHash:  secure.HashToken(token),
		UserID:     user.ID,
		IP:         ip,
		UserAgent:  truncate(userAgent, 256),
		ExpiresAt:  now.Add(m.maxTTL),
		LastSeenAt: now,
	}
	if err := m.store.Sessions.CreateLimitedForUser(sess, MaxSessionsPerUser, user); err != nil {
		return nil, err
	}
	return &CreatedSession{Token: token, Session: sess}, nil
}

// Authenticate 校验 token 并返回会话与账号。
//
// 校验顺序：token 摘要存在 → 绝对过期 → 空闲过期 → 账号未禁用。
// 过期会话会被直接删除，避免残留可用记录。
func (m *SessionManager) Authenticate(token string) (*store.Session, *store.WebUser, error) {
	return m.authenticate(token, true)
}

// Check 校验长连接的权限与有效期，不把服务器推送当作用户活跃操作。
func (m *SessionManager) Check(token string) (*store.Session, *store.WebUser, error) {
	return m.authenticate(token, false)
}

func (m *SessionManager) authenticate(token string, touch bool) (*store.Session, *store.WebUser, error) {
	if len(token) != SessionTokenBytes*2 {
		return nil, nil, ErrSessionExpired
	}
	hash := secure.HashToken(token)
	sess, err := m.store.Sessions.ByTokenHash(hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, ErrSessionExpired
		}
		return nil, nil, err
	}

	now := store.Now()
	if !now.Before(sess.ExpiresAt) || !now.Before(sess.LastSeenAt.Add(m.idleTTL)) {
		_ = m.store.Sessions.DeleteByTokenHash(hash)
		return nil, nil, ErrSessionExpired
	}

	user, err := m.store.Users.ByID(sess.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			_ = m.store.Sessions.DeleteByTokenHash(hash)
			return nil, nil, ErrSessionExpired
		}
		return nil, nil, err
	}
	if user.Disabled {
		_ = m.store.Sessions.DeleteByTokenHash(hash)
		return nil, nil, ErrUserDisabled
	}

	// 按分钟续期，避免首屏多个并发 GET 都争抢 SQLite 的单写锁。
	// 短 TTL 时使用其 1/4，保持空闲过期误差有界；每次仍读库校验吊销与禁用。
	interval := min(time.Minute, m.idleTTL/4)
	if touch && now.Sub(sess.LastSeenAt) >= interval {
		if err := m.store.Sessions.Touch(sess.ID, now); err != nil {
			return nil, nil, err
		}
		sess.LastSeenAt = now
	}
	return sess, user, nil
}

// CSRFToken 派生会话对应的 CSRF 令牌。
//
// 采用 HMAC(APP_SECRET, "kt-csrf:" + 会话 token)，因此：
//   - 不需要落库：数据库副本泄露也无法伪造（服务端密钥不在库中）；
//   - 同一会话内保持稳定：多标签页、并发调用 /auth/me 都不会互相失效；
//   - 跨站攻击者既读不到 HttpOnly 会话 Cookie，也无法计算出该令牌。
func (m *SessionManager) CSRFToken(sessionToken string) string {
	mac := hmac.New(sha256.New, m.csrfKey)
	mac.Write([]byte("kt-csrf:"))
	mac.Write([]byte(sessionToken))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyCSRF 以恒定时间比较客户端提交的 CSRF 令牌。
func (m *SessionManager) VerifyCSRF(sessionToken, provided string) bool {
	if sessionToken == "" || provided == "" {
		return false
	}
	return secure.ConstantTimeEqual(m.CSRFToken(sessionToken), provided)
}

// Logout 注销单个会话。
func (m *SessionManager) Logout(token string) error {
	if token == "" {
		return nil
	}
	return m.store.Sessions.DeleteByTokenHash(secure.HashToken(token))
}

// RevokeUser 吊销账号的全部会话：改密、改角色、禁用账号时调用。
func (m *SessionManager) RevokeUser(userID uint) error {
	return m.store.Sessions.DeleteForUser(userID)
}

// GC 清理过期会话。
func (m *SessionManager) GC() (int64, error) {
	return m.store.Sessions.DeleteExpired(store.Now())
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
