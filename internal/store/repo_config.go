package store

import (
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PanelsRepo 负责工单面板（工单类型下的按钮卡片）。
type PanelsRepo struct {
	db *gorm.DB
}

// List 返回全部面板，按类型与创建顺序排列。
func (r *PanelsRepo) List() ([]Panel, error) {
	var panels []Panel
	err := r.db.Order("type_id ASC, id ASC").Find(&panels).Error
	return panels, err
}

// ListByType 返回某个工单类型下的全部面板。
func (r *PanelsRepo) ListByType(typeID uint) ([]Panel, error) {
	if typeID == 0 {
		return nil, nil
	}
	var panels []Panel
	err := r.db.Where("type_id = ?", typeID).Order("id ASC").Find(&panels).Error
	return panels, err
}

// CountByType 统计某个工单类型下的面板数量。
func (r *PanelsRepo) CountByType(typeID uint) (int64, error) {
	var count int64
	err := r.db.Model(&Panel{}).Where("type_id = ?", typeID).Count(&count).Error
	return count, err
}

// ByID 按主键查询面板。
func (r *PanelsRepo) ByID(id uint) (*Panel, error) {
	var p Panel
	if err := r.db.First(&p, id).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &p, nil
}

// ByChannel 按频道查询面板，返回该频道最早创建的一条记录。
//
// 主要供无法携带面板 ID 的旧卡片事件与统计用途；新流程应优先使用 ByID。
func (r *PanelsRepo) ByChannel(channelID string) (*Panel, error) {
	if channelID == "" {
		return nil, ErrNotFound
	}
	var p Panel
	if err := r.db.Where("channel_id = ?", channelID).Order("id ASC").First(&p).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &p, nil
}

// ListByChannel 返回频道内全部面板，按创建顺序排列。
func (r *PanelsRepo) ListByChannel(channelID string) ([]Panel, error) {
	if channelID == "" {
		return nil, nil
	}
	var panels []Panel
	err := r.db.Where("channel_id = ?", channelID).Order("id ASC").Find(&panels).Error
	return panels, err
}

// Create 新增一条面板记录（同一频道允许多条）。
func (r *PanelsRepo) Create(p *Panel) error {
	now := Now()
	p.CreatedAt, p.UpdatedAt = now, now
	return r.db.Create(p).Error
}

// UpdateFields 局部更新面板。
func (r *PanelsRepo) UpdateFields(id uint, fields map[string]any) error {
	fields["updated_at"] = Now()
	return r.db.Model(&Panel{}).Where("id = ?", id).Updates(fields).Error
}

// Delete 删除面板，并把引用它的工单解绑（保留历史记录与类型快照）。
func (r *PanelsRepo) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		// 面板 ID 可能被后续新建的面板复用，直接清空引用避免工单被错误归属。
		if err := tx.Model(&Ticket{}).Where("panel_id = ?", id).Update("panel_id", nil).Error; err != nil {
			return err
		}
		return tx.Delete(&Panel{}, id).Error
	})
}

// TypesRepo 负责工单类型与其类型级管理员角色。
type TypesRepo struct {
	db *gorm.DB
}

// List 返回全部工单类型（含角色与面板）。
func (r *TypesRepo) List() ([]TicketType, error) {
	var types []TicketType
	err := r.db.Preload("Roles").Preload("Panels").Order("id ASC").Find(&types).Error
	return types, err
}

// ByID 按主键查询工单类型（含角色与面板）。
func (r *TypesRepo) ByID(id uint) (*TicketType, error) {
	var item TicketType
	if err := r.db.Preload("Roles").Preload("Panels").First(&item, id).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &item, nil
}

// ByName 按名称查询工单类型。
func (r *TypesRepo) ByName(name string) (*TicketType, error) {
	var item TicketType
	err := r.db.Where("name = ?", strings.TrimSpace(name)).
		Preload("Roles").Preload("Panels").First(&item).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &item, nil
}

// ListByChannel 返回某频道内所有面板所属的工单类型（含角色，去重）。
//
// 供只能拿到频道 ID 的场景使用：/aar 命令、按来源频道判定管理员权限。
func (r *TypesRepo) ListByChannel(channelID string) ([]TicketType, error) {
	if channelID == "" {
		return nil, nil
	}
	var types []TicketType
	err := r.db.Preload("Roles").
		Where("id IN (?)", r.db.Model(&Panel{}).Select("type_id").Where("channel_id = ?", channelID)).
		Order("id ASC").Find(&types).Error
	return types, err
}

// Create 新增工单类型。
func (r *TypesRepo) Create(item *TicketType) error {
	now := Now()
	item.CreatedAt, item.UpdatedAt = now, now
	return r.db.Create(item).Error
}

// UpdateFields 局部更新工单类型。
func (r *TypesRepo) UpdateFields(id uint, fields map[string]any) error {
	fields["updated_at"] = Now()
	return r.db.Model(&TicketType{}).Where("id = ?", id).Updates(fields).Error
}

// Delete 删除工单类型及其角色。
//
// 调用方必须先确保该类型下没有面板：面板卡片仍在频道里，
// 直接级联删除会让卡片点击后找不到类型；因此这里不提供隐式级联。
// 已开出的历史工单保留 type_name 快照，type_id 置空。
func (r *TypesRepo) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("type_id = ?", id).Delete(&TicketTypeRole{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&Ticket{}).Where("type_id = ?", id).Update("type_id", nil).Error; err != nil {
			return err
		}
		return tx.Delete(&TicketType{}, id).Error
	})
}

// AddRole 为类型绑定管理员角色（幂等）。
func (r *TypesRepo) AddRole(typeID uint, roleID, roleName string) error {
	var existing TicketTypeRole
	err := r.db.Where("type_id = ? AND role_id = ?", typeID, roleID).First(&existing).Error
	if err == nil {
		return nil
	}
	if mapped := mapNotFound(err); mapped != ErrNotFound {
		return mapped
	}
	return r.db.Create(&TicketTypeRole{TypeID: typeID, RoleID: roleID, RoleName: roleName, CreatedAt: Now()}).Error
}

// RemoveRole 解除类型与角色的绑定。
func (r *TypesRepo) RemoveRole(typeID uint, roleID string) error {
	return r.db.Where("type_id = ? AND role_id = ?", typeID, roleID).Delete(&TicketTypeRole{}).Error
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

// RuleByID 按主键查询规则。
func (r *EmojiRepo) RuleByID(id uint) (*EmojiRule, error) {
	var rule EmojiRule
	if err := r.db.First(&rule, id).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &rule, nil
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
