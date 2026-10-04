package store

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

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
	return r.db.Model(&WebUser{}).Where("id = ?", id).Updates(fields).Error
}

// Delete 删除账号；同时清理其会话，避免残留会话继续可用。
func (r *UsersRepo) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
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
	return r.db.Model(&Session{}).Where("id = ?", id).Update("last_seen_at", seenAt.UTC()).Error
}

// DeleteOldestForUser 仅保留最近 keep 个会话，删除更旧的会话（含已过期者）。
func (r *SessionsRepo) DeleteOldestForUser(userID uint, keep int) (int64, error) {
	var ids []uint
	err := r.db.Model(&Session{}).Where("user_id = ?", userID).
		Order("last_seen_at DESC, id DESC").
		Offset(keep).Pluck("id", &ids).Error
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	res := r.db.Where("id IN ?", ids).Delete(&Session{})
	return res.RowsAffected, res.Error
}

// DeleteByTokenHash 注销单个会话。
func (r *SessionsRepo) DeleteByTokenHash(hash string) error {
	return r.db.Where("token_hash = ?", hash).Delete(&Session{}).Error
}

// DeleteForUser 吊销某个账号的全部会话，用于改密、改角色与禁用账号。
func (r *SessionsRepo) DeleteForUser(userID uint) error {
	return r.db.Where("user_id = ?", userID).Delete(&Session{}).Error
}

// DeleteExpired 清理过期会话，返回清理数量。
func (r *SessionsRepo) DeleteExpired(now time.Time) (int64, error) {
	res := r.db.Where("expires_at < ?", now.UTC()).Delete(&Session{})
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
	res := r.db.Model(&AuthCode{}).Where("id = ? AND used_at IS NULL", id).Updates(map[string]any{
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
