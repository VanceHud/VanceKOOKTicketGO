package store

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

var ErrLastAdmin = errors.New("系统必须保留至少一个可用管理员")
var ErrAlreadyBound = errors.New("账号已绑定其它 KOOK 身份")
var ErrAuthenticationChanged = errors.New("账号认证信息已变更，请重新登录")

func checkAuthenticatedUser(tx *gorm.DB, verified *WebUser) error {
	var current WebUser
	if err := tx.First(&current, verified.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrAuthenticationChanged
		}
		return err
	}
	if current.Disabled || current.PasswordHash != verified.PasswordHash || current.Role != verified.Role || current.MustChangePassword != verified.MustChangePassword {
		return ErrAuthenticationChanged
	}
	return nil
}

func ensureAdminRemains(tx *gorm.DB, id uint) error {
	var user WebUser
	if err := tx.First(&user, id).Error; err != nil {
		return mapNotFound(err)
	}
	if user.Role != RoleAdmin || user.Disabled {
		return nil
	}
	var count int64
	if err := tx.Model(&WebUser{}).Where("id <> ? AND role = ? AND disabled = ?", id, RoleAdmin, false).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrLastAdmin
	}
	return nil
}

// UsersRepo 负责 WebUI 账号。
type UsersRepo struct {
	db *gorm.DB
}

// NormalizeUsername 统一小写并去除首尾空白。
// SQLite 的默认比较区分大小写，统一归一化才能保证“Admin”与“admin”不会同时存在。
func NormalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// Count 返回账号总数，用于判断是否需要初始化管理员。
func (r *UsersRepo) Count() (int64, error) {
	var n int64
	err := r.db.Model(&WebUser{}).Count(&n).Error
	return n, err
}

// Create 创建账号。
func (r *UsersRepo) Create(u *WebUser) error {
	u.Username = NormalizeUsername(u.Username)
	return r.db.Create(u).Error
}

// ByID 按主键查询账号。
func (r *UsersRepo) ByID(id uint) (*WebUser, error) {
	var u WebUser
	if err := r.db.First(&u, id).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &u, nil
}

// ByUsername 按用户名查询账号（大小写不敏感）。
func (r *UsersRepo) ByUsername(username string) (*WebUser, error) {
	var u WebUser
	if err := r.db.Where("username = ?", NormalizeUsername(username)).First(&u).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &u, nil
}

// ByKookID 按绑定的 KOOK 用户 ID 查询账号。
func (r *UsersRepo) ByKookID(kookUserID string) (*WebUser, error) {
	if kookUserID == "" {
		return nil, ErrNotFound
	}
	var u WebUser
	if err := r.db.Where("kook_user_id = ?", kookUserID).First(&u).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &u, nil
}

// List 返回全部账号，按创建时间倒序。
func (r *UsersRepo) List() ([]WebUser, error) {
	var users []WebUser
	if err := r.db.Order("created_at DESC, id DESC").Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

// Save 保存账号全量字段。
func (r *UsersRepo) Save(u *WebUser) error {
	u.Username = NormalizeUsername(u.Username)
	return r.db.Save(u).Error
}

// UpdateFields 局部更新指定字段。
func (r *UsersRepo) UpdateFields(id uint, fields map[string]any) error {
	return r.updateFields(id, fields, nil)
}

// UpdateVerified 用于用户改密，避免在旧密码校验后覆盖管理员刚重置的新密码。
func (r *UsersRepo) UpdateVerified(verified *WebUser, fields map[string]any) error {
	return r.updateFields(verified.ID, fields, verified)
}

func (r *UsersRepo) updateFields(id uint, fields map[string]any, verified *WebUser) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if verified != nil {
			if err := checkAuthenticatedUser(tx, verified); err != nil {
				return err
			}
		}
		role, roleChanged := fields["role"]
		if (roleChanged && role != RoleAdmin) || fields["disabled"] == true {
			if err := ensureAdminRemains(tx, id); err != nil {
				return err
			}
		}
		res := tx.Model(&WebUser{}).Where("id = ?", id).Updates(fields)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		// 安全字段变更与会话吊销必须同事务提交，失败时不能只更新账号。
		for _, field := range []string{"password_hash", "role", "disabled", "must_change_password"} {
			if _, ok := fields[field]; ok {
				return tx.Where("user_id = ?", id).Delete(&Session{}).Error
			}
		}
		return nil
	})
}

// Delete 删除账号；同时清理其会话，避免残留会话继续可用。
func (r *UsersRepo) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := ensureAdminRemains(tx, id); err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", id).Delete(&Session{}).Error; err != nil {
			return err
		}
		return tx.Delete(&WebUser{}, id).Error
	})
}

// TouchLogin 记录最近登录时间。
func (r *UsersRepo) TouchLogin(id uint, at time.Time) error {
	return r.UpdateFields(id, map[string]any{"last_login_at": at.UTC()})
}

// SessionsRepo 负责 WebUI 会话。
type SessionsRepo struct {
	db *gorm.DB
}

// Create 新建会话记录（只保存 token 与 CSRF 令牌的哈希）。
func (r *SessionsRepo) Create(s *Session) error {
	return r.db.Create(s).Error
}

// CreateLimited 把创建与淘汰合并为一个事务，并发登录也不会突破会话数上限。
func (r *SessionsRepo) CreateLimited(s *Session, keep int) error {
	return r.createLimited(s, keep, nil)
}

// CreateLimitedForUser 保证密码校验期间发生的改密/禁用/角色变更不能被在途登录绕过。
func (r *SessionsRepo) CreateLimitedForUser(s *Session, keep int, verified *WebUser) error {
	return r.createLimited(s, keep, verified)
}

func (r *SessionsRepo) createLimited(s *Session, keep int, verified *WebUser) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if verified != nil {
			if s.UserID != verified.ID {
				return ErrAuthenticationChanged
			}
			if err := checkAuthenticatedUser(tx, verified); err != nil {
				return err
			}
		}
		if err := tx.Create(s).Error; err != nil {
			return err
		}
		old := tx.Model(&Session{}).Select("id").Where("user_id = ?", s.UserID).
			Order("last_seen_at DESC, id DESC").Offset(keep).Limit(-1)
		return tx.Where("id IN (?)", old).Delete(&Session{}).Error
	})
}

// ByTokenHash 按 token 哈希查询会话。
func (r *SessionsRepo) ByTokenHash(hash string) (*Session, error) {
	var s Session
	if err := r.db.Where("token_hash = ?", hash).First(&s).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &s, nil
}

// Touch 刷新会话的最近活跃时间。
// 注意：绝对过期时间（ExpiresAt）建立后不再延长，避免“一直在线”导致会话永不过期。
func (r *SessionsRepo) Touch(id uint, seenAt time.Time) error {
	return r.db.Model(&Session{}).Where("id = ? AND last_seen_at < ?", id, seenAt.UTC()).Update("last_seen_at", seenAt.UTC()).Error
}

// DeleteByTokenHash 注销单个会话。
func (r *SessionsRepo) DeleteByTokenHash(hash string) error {
	return r.db.Where("token_hash = ?", hash).Delete(&Session{}).Error
}

// DeleteForUser 吊销某个账号的全部会话，用于改密、改角色与禁用账号。
func (r *SessionsRepo) DeleteForUser(userID uint) error {
	return r.db.Where("user_id = ?", userID).Delete(&Session{}).Error
}

// DeleteExpired 清理过期会话：绝对过期（expires_at）与空闲过期（idleCutoff 之前
// 不再活跃）两种。空闲过期若不清理，只能等用户再次访问时惰性删除，
// 会一直占用 MaxSessionsPerUser 的额度。
func (r *SessionsRepo) DeleteExpired(now, idleCutoff time.Time) (int64, error) {
	res := r.db.Where("expires_at < ? OR last_seen_at < ?", now.UTC(), idleCutoff.UTC()).
		Delete(&Session{})
	return res.RowsAffected, res.Error
}

// CountForUser 统计账号当前有效会话数，用于限制并发会话。
func (r *SessionsRepo) CountForUser(userID uint, now time.Time) (int64, error) {
	var n int64
	err := r.db.Model(&Session{}).Where("user_id = ? AND expires_at > ?", userID, now.UTC()).Count(&n).Error
	return n, err
}

// AuthCodesRepo 负责一次性登录码 / 绑定码。
type AuthCodesRepo struct {
	db *gorm.DB
}

// Create 写入一次性码（仅存哈希）。
func (r *AuthCodesRepo) Create(code *AuthCode) error {
	return r.db.Create(code).Error
}

// ByCodeHash 按哈希查询。
func (r *AuthCodesRepo) ByCodeHash(hash string) (*AuthCode, error) {
	var c AuthCode
	if err := r.db.Where("code_hash = ?", hash).First(&c).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &c, nil
}

// MarkUsed 标记为已使用（一次性）。
func (r *AuthCodesRepo) MarkUsed(id uint, at time.Time, ip string) error {
	res := r.db.Model(&AuthCode{}).Where("id = ? AND used_at IS NULL AND expires_at > ?", id, at.UTC()).Updates(map[string]any{
		"used_at": at.UTC(),
		"used_ip": ip,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// 并发场景下已被其它请求消费。
		return ErrNotFound
	}
	return nil
}

// BindToUser 在事务内检查身份关联并消费绑定码，防止两个绑定请求覆盖同一账号。
func (r *AuthCodesRepo) BindToUser(id, userID uint, at time.Time, ip string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var code AuthCode
		if err := tx.Where("id = ? AND purpose = ? AND used_at IS NULL AND expires_at > ?", id, CodePurposeBind, at.UTC()).First(&code).Error; err != nil {
			return mapNotFound(err)
		}
		res := tx.Model(&WebUser{}).
			Where("id = ? AND disabled = ? AND must_change_password = ? AND (kook_user_id IS NULL OR kook_user_id = ?)", userID, false, false, code.KookUserID).
			Updates(map[string]any{"kook_user_id": code.KookUserID, "kook_user_name": code.KookUserName, "updated_at": at.UTC()})
		if errors.Is(res.Error, gorm.ErrDuplicatedKey) || (res.Error == nil && res.RowsAffected == 0) {
			return ErrAlreadyBound
		}
		if res.Error != nil {
			return res.Error
		}
		return tx.Model(&AuthCode{}).Where("id = ?", id).Updates(map[string]any{"used_at": at.UTC(), "used_ip": ip}).Error
	})
}

// Latest 返回某 KOOK 用户最近签发的一次性码，用于生成频率限制。
func (r *AuthCodesRepo) Latest(kookUserID, purpose string) (*AuthCode, error) {
	var c AuthCode
	if err := r.db.Where("kook_user_id = ? AND purpose = ?", kookUserID, purpose).
		Order("created_at DESC, id DESC").First(&c).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &c, nil
}

// InvalidateActive 作废某 KOOK 用户未使用的同类码。
func (r *AuthCodesRepo) InvalidateActive(kookUserID, purpose string, at time.Time) error {
	return r.db.Model(&AuthCode{}).
		Where("kook_user_id = ? AND purpose = ? AND used_at IS NULL", kookUserID, purpose).
		Update("used_at", at.UTC()).Error
}

// DeleteExpired 清理过期或已使用的一次性码。
func (r *AuthCodesRepo) DeleteExpired(now time.Time) (int64, error) {
	res := r.db.Where("expires_at < ? OR used_at IS NOT NULL", now.UTC()).Delete(&AuthCode{})
	return res.RowsAffected, res.Error
}
