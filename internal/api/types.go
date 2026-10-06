package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// 工单类型字段限制（与数据库列宽保持一致）。
const (
	maxTicketTypeNameLength = 64
	maxTicketTypeDescLength = 256
)

// ---------------------------------------------------------------------------
// 工单类型
// ---------------------------------------------------------------------------

type ticketTypeRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Enabled     *bool   `json:"enabled"`
}

// normalizeTicketTypeName 规整并校验类型名称。
//
// 名称用于频道名与卡片展示，因此把换行、制表等空白折叠成单个空格，
// 避免出现“界面上是一行、频道名里却是两行”的怪现象。
func normalizeTicketTypeName(value string) (string, error) {
	name := strings.Join(strings.Fields(value), " ")
	if name == "" {
		return "", fmt.Errorf("类型名称不能为空")
	}
	if len([]rune(name)) > maxTicketTypeNameLength {
		return "", fmt.Errorf("类型名称不能超过 %d 个字符", maxTicketTypeNameLength)
	}
	return name, nil
}

// normalizeTicketTypeDescription 规整并校验类型备注。
func normalizeTicketTypeDescription(value string) (string, error) {
	description := strings.TrimSpace(value)
	if len([]rune(description)) > maxTicketTypeDescLength {
		return "", fmt.Errorf("类型备注不能超过 %d 个字符", maxTicketTypeDescLength)
	}
	return description, nil
}

// handleTypeList 返回全部工单类型（含类型角色与所属面板）。
//
// 类型树界面需要一次拿到「类型 → 角色 → 面板」的完整结构，
// 因此这里直接返回带预加载的数据，避免前端逐类型再各查一次。
func (s *Server) handleTypeList(c *gin.Context) {
	types, err := s.Store.Types.List()
	if err != nil {
		s.failInternal(c, err, "type.list")
		return
	}
	if types == nil {
		types = []store.TicketType{}
	}
	c.JSON(http.StatusOK, gin.H{"items": types})
}

// handleTypeCreate 新建工单类型。
func (s *Server) handleTypeCreate(c *gin.Context) {
	var req ticketTypeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	name, err := normalizeTicketTypeName(derefString(req.Name))
	if err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	description, err := normalizeTicketTypeDescription(derefString(req.Description))
	if err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if _, err := s.Store.Types.ByName(name); err == nil {
		s.fail(c, http.StatusConflict, "type_exists", "已存在同名工单类型")
		return
	} else if err != store.ErrNotFound {
		s.failStore(c, err, "type.create.lookup")
		return
	}

	item := &store.TicketType{Name: name, Description: description, Enabled: true}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if err := s.Store.Types.Create(item); err != nil {
		s.failStore(c, err, "type.create")
		return
	}
	s.audit(c, "type.create", name, "新建工单类型")
	s.respondType(c, http.StatusCreated, item.ID, "type.create.reload")
}

// handleTypeUpdate 更新类型名称、备注与启用状态。
//
// 停用类型后，其所有面板都会拒绝开单（已在处理中的工单不受影响）；
// 改名只影响之后新开的工单，历史工单保留开单时的类型名快照。
func (s *Server) handleTypeUpdate(c *gin.Context) {
	item, ok := s.lookupType(c)
	if !ok {
		return
	}

	var req ticketTypeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}

	updates := map[string]any{}
	if req.Name != nil {
		name, err := normalizeTicketTypeName(*req.Name)
		if err != nil {
			s.fail(c, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if name != item.Name {
			if existing, err := s.Store.Types.ByName(name); err == nil {
				if existing.ID != item.ID {
					s.fail(c, http.StatusConflict, "type_exists", "已存在同名工单类型")
					return
				}
			} else if err != store.ErrNotFound {
				s.failStore(c, err, "type.update.lookup")
				return
			}
			updates["name"] = name
		}
	}
	if req.Description != nil {
		description, err := normalizeTicketTypeDescription(*req.Description)
		if err != nil {
			s.fail(c, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		updates["description"] = description
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}

	if len(updates) > 0 {
		if err := s.Store.Types.UpdateFields(item.ID, updates); err != nil {
			s.failStore(c, err, "type.update")
			return
		}
		s.audit(c, "type.update", item.Name, "更新工单类型")
	}
	s.respondType(c, http.StatusOK, item.ID, "type.update.reload")
}

// handleTypeDelete 删除工单类型。
//
// 类型下仍有面板时拒绝删除：面板卡片还在频道里，删除后点击按钮会找不到类型。
// 管理员需要先把面板改属到其它类型或删除面板。
func (s *Server) handleTypeDelete(c *gin.Context) {
	item, ok := s.lookupType(c)
	if !ok {
		return
	}
	count, err := s.Store.Panels.CountByType(item.ID)
	if err != nil {
		s.failStore(c, err, "type.delete.count")
		return
	}
	if count > 0 {
		s.fail(c, http.StatusBadRequest, "type_in_use",
			fmt.Sprintf("该类型下还有 %d 个面板，请先删除面板或把它们改属到其它类型", count))
		return
	}
	if err := s.Store.Types.Delete(item.ID); err != nil {
		s.failStore(c, err, "type.delete")
		return
	}
	s.audit(c, "type.delete", item.Name, "删除工单类型")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type typeRoleRequest struct {
	RoleID   string `json:"roleId"`
	RoleName string `json:"roleName"`
}

// handleTypeRoleAdd 添加类型级管理员角色（等价于 /aar @角色）。
func (s *Server) handleTypeRoleAdd(c *gin.Context) {
	item, ok := s.lookupType(c)
	if !ok {
		return
	}

	var req typeRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	roleID := strings.TrimSpace(req.RoleID)
	if !validKookID(roleID) {
		s.fail(c, http.StatusBadRequest, "invalid_request", "角色 ID 必须是 KOOK 的数字 ID")
		return
	}

	roleName := strings.TrimSpace(req.RoleName)
	// 机器人在线时校验角色是否存在并补全名称
	if s.Bot != nil && s.botConnected() {
		if name := s.Bot.RoleName(c.Request.Context(), roleID); name != "" {
			roleName = name
		} else if roleName == "" {
			s.fail(c, http.StatusBadRequest, "invalid_role", "该角色不存在于当前服务器")
			return
		}
	}
	if roleName == "" {
		roleName = roleID
	}

	if err := s.Store.Types.AddRole(item.ID, roleID, roleName); err != nil {
		s.failStore(c, err, "type.role.add")
		return
	}
	s.audit(c, "role.type.add", roleID, "工单类型「"+item.Name+"」新增管理员角色")
	s.respondType(c, http.StatusCreated, item.ID, "type.role.add.reload")
}

// handleTypeRoleRemove 移除类型级管理员角色。
func (s *Server) handleTypeRoleRemove(c *gin.Context) {
	item, ok := s.lookupType(c)
	if !ok {
		return
	}
	roleID := strings.TrimSpace(c.Param("roleId"))
	if !validKookID(roleID) {
		s.fail(c, http.StatusBadRequest, "invalid_request", "参数不合法")
		return
	}
	if err := s.Store.Types.RemoveRole(item.ID, roleID); err != nil {
		s.failStore(c, err, "type.role.remove")
		return
	}
	s.audit(c, "role.type.remove", roleID, "工单类型「"+item.Name+"」移除管理员角色")
	s.respondType(c, http.StatusOK, item.ID, "type.role.remove.reload")
}

// lookupType 按路径参数取工单类型。
func (s *Server) lookupType(c *gin.Context) (*store.TicketType, bool) {
	id := uint(atoiDefault(c.Param("id"), 0))
	if id == 0 {
		s.fail(c, http.StatusBadRequest, "invalid_request", "工单类型 ID 不合法")
		return nil, false
	}
	item, err := s.Store.Types.ByID(id)
	if err != nil {
		s.failStore(c, err, "type.lookup")
		return nil, false
	}
	return item, true
}

// respondType 返回类型的最新完整数据（含角色与面板）。
func (s *Server) respondType(c *gin.Context, status int, id uint, op string) {
	updated, err := s.Store.Types.ByID(id)
	if err != nil {
		s.failStore(c, err, op)
		return
	}
	c.JSON(status, updated)
}
