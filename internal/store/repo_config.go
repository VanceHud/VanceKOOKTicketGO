package store

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PanelsRepo 负责工单面板与其面板级管理员角色。
type PanelsRepo struct {
	db *gorm.DB
}

// List 返回全部面板，附带面板级角色。
func (r *PanelsRepo) List() ([]Panel, error) {
	var panels []Panel
	err := r.db.Preload("Roles").Order("id ASC").Find(&panels).Error
	return panels, err
}

// ByID 按主键查询面板。
func (r *PanelsRepo) ByID(id uint) (*Panel, error) {
	var p Panel
	if err := r.db.Preload("Roles").First(&p, id).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &p, nil
}

// ByChannel 按频道查询面板（开单事件需要判断按钮所属面板）。
func (r *PanelsRepo) ByChannel(channelID string) (*Panel, error) {
	if channelID == "" {
		return nil, ErrNotFound
	}
	var p Panel
	if err := r.db.Preload("Roles").Where("channel_id = ?", channelID).First(&p).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &p, nil
}

// Upsert 以频道 ID 为唯一键写入或更新面板。
func (r *PanelsRepo) Upsert(p *Panel) error {
	now := Now()
	p.CreatedAt, p.UpdatedAt = now, now
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"channel_name", "msg_id", "title", "enabled", "updated_at"}),
	}).Create(p).Error
}

// UpdateFields 局部更新面板。
func (r *PanelsRepo) UpdateFields(id uint, fields map[string]any) error {
	fields["updated_at"] = Now()
	return r.db.Model(&Panel{}).Where("id = ?", id).Updates(fields).Error
}

// Delete 删除面板及其角色绑定。
func (r *PanelsRepo) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("panel_id = ?", id).Delete(&PanelRole{}).Error; err != nil {
			return err
		}
		return tx.Delete(&Panel{}, id).Error
	})
}

// AddRole 为面板绑定管理员角色（幂等）。
func (r *PanelsRepo) AddRole(panelID uint, roleID, roleName string) error {
	var existing PanelRole
	err := r.db.Where("panel_id = ? AND role_id = ?", panelID, roleID).First(&existing).Error
	if err == nil {
		return nil
	}
	if mapped := mapNotFound(err); mapped != ErrNotFound {
		return mapped
	}
	return r.db.Create(&PanelRole{PanelID: panelID, RoleID: roleID, RoleName: roleName, CreatedAt: Now()}).Error
}

// RemoveRole 解除面板与角色的绑定。
func (r *PanelsRepo) RemoveRole(panelID uint, roleID string) error {
	return r.db.Where("panel_id = ? AND role_id = ?", panelID, roleID).Delete(&PanelRole{}).Error
}

// RolesRepo 负责全局管理员角色与 KOOK → WebUI 角色映射。
type RolesRepo struct {
	db *gorm.DB
}

// ListAdmin 返回全局管理员角色。
func (r *RolesRepo) ListAdmin() ([]AdminRole, error) {
	var roles []AdminRole
	err := r.db.Order("id ASC").Find(&roles).Error
	return roles, err
}

// AddAdmin 新增全局管理员角色（幂等）。
func (r *RolesRepo) AddAdmin(roleID, roleName string) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "role_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"role_name"}),
	}).Create(&AdminRole{RoleID: roleID, RoleName: roleName, CreatedAt: Now()}).Error
}

// RemoveAdmin 删除全局管理员角色。
func (r *RolesRepo) RemoveAdmin(roleID string) error {
	return r.db.Where("role_id = ?", roleID).Delete(&AdminRole{}).Error
}

// ListMappings 返回全部角色映射。
func (r *RolesRepo) ListMappings() ([]RoleMapping, error) {
	var items []RoleMapping
	err := r.db.Order("id ASC").Find(&items).Error
	return items, err
}

// UpsertMapping 写入或更新角色映射。
func (r *RolesRepo) UpsertMapping(m *RoleMapping) error {
	now := Now()
	m.CreatedAt, m.UpdatedAt = now, now
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "kook_role_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"kook_role_name", "web_role", "updated_at"}),
	}).Create(m).Error
}

// DeleteMapping 删除角色映射。
func (r *RolesRepo) DeleteMapping(id uint) error {
	return r.db.Delete(&RoleMapping{}, id).Error
}

// ResolveWebRole 依据用户持有的 KOOK 角色 ID 列表计算 WebUI 角色。
//
// 命中多条规则时取权限最高者；未命中任何规则时返回 ok=false，
// 调用方应拒绝登录（不提示“账号是否存在”）。
func (r *RolesRepo) ResolveWebRole(kookRoleIDs []string) (string, bool, error) {
	if len(kookRoleIDs) == 0 {
		return "", false, nil
	}
	var items []RoleMapping
	if err := r.db.Where("kook_role_id IN ?", kookRoleIDs).Find(&items).Error; err != nil {
		return "", false, err
	}
	best := ""
	for _, item := range items {
		if roleRank[item.WebRole] > roleRank[best] {
			best = item.WebRole
		}
	}
	if best == "" {
		return "", false, nil
	}
	return best, true, nil
}

// EmojiRepo 负责表情回应上角色规则与发放记录。
type EmojiRepo struct {
	db *gorm.DB
}

// ListRules 返回全部规则。
func (r *EmojiRepo) ListRules() ([]EmojiRule, error) {
	var rules []EmojiRule
	err := r.db.Order("message_id ASC, id ASC").Find(&rules).Error
	return rules, err
}

// CreateRule 新增规则。
func (r *EmojiRepo) CreateRule(rule *EmojiRule) error {
	now := Now()
	rule.CreatedAt, rule.UpdatedAt = now, now
	return r.db.Create(rule).Error
}

// UpdateRule 保存规则全量字段。
func (r *EmojiRepo) UpdateRule(rule *EmojiRule) error {
	rule.UpdatedAt = Now()
	return r.db.Save(rule).Error
}

// DeleteRule 删除规则。
func (r *EmojiRepo) DeleteRule(id uint) error {
	return r.db.Delete(&EmojiRule{}, id).Error
}

// MatchRule 查询某条消息上某个表情对应的启用规则。
func (r *EmojiRepo) MatchRule(messageID, emojiID string) (*EmojiRule, error) {
	var rule EmojiRule
	err := r.db.Where("message_id = ? AND emoji_id = ? AND enabled = ?", messageID, emojiID, true).
		First(&rule).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &rule, nil
}

// LastGrant 返回用户最近一次通过表情获得的角色。
func (r *EmojiRepo) LastGrant(kookUserID string) (*EmojiGrant, error) {
	var g EmojiGrant
	err := r.db.Where("kook_user_id = ?", kookUserID).Order("granted_at DESC, id DESC").First(&g).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &g, nil
}

// RecordGrant 记录一次角色发放。
func (r *EmojiRepo) RecordGrant(g *EmojiGrant) error {
	g.GrantedAt = g.GrantedAt.UTC()
	return r.db.Create(g).Error
}

// UpdateGrant 更新发放记录（同一用户重复回应时覆盖最近一条）。
func (r *EmojiRepo) UpdateGrant(id uint, ruleID uint, emojiID, roleID string) error {
	return r.db.Model(&EmojiGrant{}).Where("id = ?", id).Updates(map[string]any{
		"rule_id":    ruleID,
		"emoji_id":   emojiID,
		"role_id":    roleID,
		"granted_at": Now(),
	}).Error
}

// ListGrants 返回最近的发放记录。
func (r *EmojiRepo) ListGrants(limit int) ([]EmojiGrant, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var grants []EmojiGrant
	err := r.db.Order("granted_at DESC, id DESC").Limit(limit).Find(&grants).Error
	return grants, err
}
