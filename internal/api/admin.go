package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/auth"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/config"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// kookIDPattern 校验 KOOK 的 ID（雪花 ID，十进制数字串）。
var kookIDPattern = regexp.MustCompile(`^[0-9]{5,32}$`)

// usernamePattern 校验 WebUI 用户名（已归一化为小写）。
var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,31}$`)

func validKookID(id string) bool { return kookIDPattern.MatchString(strings.TrimSpace(id)) }

func validRole(role string) bool {
	switch role {
	case store.RoleAdmin, store.RoleStaff, store.RoleReadonly:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// 健康检查
// ---------------------------------------------------------------------------

// handleHealth 返回服务与数据库状态；不需要认证，仅暴露最少信息。
func (s *Server) handleHealth(c *gin.Context) {
	dbStatus := "ok"
	if sqlDB, err := s.Store.DB().DB(); err != nil {
		dbStatus = "error"
	} else {
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		if err := sqlDB.PingContext(ctx); err != nil {
			dbStatus = "error"
		}
	}

	status := http.StatusOK
	label := "ok"
	if dbStatus != "ok" {
		status = http.StatusServiceUnavailable
		label = "degraded"
	}
	c.JSON(status, gin.H{
		"status":        label,
		"version":       s.Version,
		"db":            dbStatus,
		"uptimeSeconds": int64(time.Since(s.Started).Seconds()),
	})
}

// ---------------------------------------------------------------------------
// 系统设置
// ---------------------------------------------------------------------------

type settingsResponse struct {
	GuildID          string   `json:"guildId"`
	GuildName        string   `json:"guildName"`
	CategoryID       string   `json:"categoryId"`
	CategoryName     string   `json:"categoryName"`
	LogChannelID     string   `json:"logChannelId"`
	LogChannelName   string   `json:"logChannelName"`
	DebugChannelID   string   `json:"debugChannelId"`
	DebugChannelName string   `json:"debugChannelName"`
	OutdateHours     int      `json:"outdateHours"`
	KookTokenMasked  string   `json:"kookTokenMasked"`
	HasKookToken     bool     `json:"hasKookToken"`
	MissingRequired  []string `json:"missingRequired"`
	InitializedAt    string   `json:"initializedAt,omitempty"`
	DryRun           bool     `json:"dryRun"`
	BotConnected     bool     `json:"botConnected"`
}

// handleSettingsGet 返回当前业务配置；token 只返回掩码，绝不返回明文。
func (s *Server) handleSettingsGet(c *gin.Context) {
	all, err := s.Store.Settings.All()
	if err != nil {
		s.failInternal(c, err, "settings.get")
		return
	}
	runtimeCfg, err := s.Store.Settings.Runtime(s.Config.AppSecret)
	if err != nil {
		s.failInternal(c, err, "settings.runtime")
		return
	}

	resp := settingsResponse{
		GuildID:          all[store.SettingGuildID],
		GuildName:        all[store.SettingGuildName],
		CategoryID:       all[store.SettingCategoryID],
		CategoryName:     all[store.SettingCategoryName],
		LogChannelID:     all[store.SettingLogChannelID],
		LogChannelName:   all[store.SettingLogChannelName],
		DebugChannelID:   all[store.SettingDebugChannelID],
		DebugChannelName: all[store.SettingDebugChName],
		OutdateHours:     runtimeCfg.OutdateHours,
		KookTokenMasked:  runtimeCfg.TokenMasked,
		HasKookToken:     runtimeCfg.HasKookToken,
		MissingRequired:  runtimeCfg.MissingRequired,
		DryRun:           s.Config.DryRun,
		BotConnected:     s.botConnected(),
	}
	if !runtimeCfg.InitializedAt.IsZero() {
		resp.InitializedAt = runtimeCfg.InitializedAt.Format(time.RFC3339)
	}
	if resp.MissingRequired == nil {
		resp.MissingRequired = []string{}
	}
	c.JSON(http.StatusOK, resp)
}

type settingsUpdateRequest struct {
	KookToken        *string `json:"kookToken"`
	GuildID          *string `json:"guildId"`
	GuildName        *string `json:"guildName"`
	CategoryID       *string `json:"categoryId"`
	CategoryName     *string `json:"categoryName"`
	LogChannelID     *string `json:"logChannelId"`
	LogChannelName   *string `json:"logChannelName"`
	DebugChannelID   *string `json:"debugChannelId"`
	DebugChannelName *string `json:"debugChannelName"`
	OutdateHours     *int    `json:"outdateHours"`
}

// handleSettingsUpdate 更新业务配置。
//
// 采用“字段指针”语义：只有显式传入的字段才会被修改，
// 传入空字符串表示清空该配置项。token 为只写字段，写入时立即加密，任何接口都不回显。
func (s *Server) handleSettingsUpdate(c *gin.Context) {
	var req settingsUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}

	changed := make([]string, 0, 8)
	updatedNames := false

	setPlain := func(key, label string, value *string, requireKookID bool) bool {
		if value == nil {
			return true
		}
		v := strings.TrimSpace(*value)
		if v != "" && requireKookID && !validKookID(v) {
			s.fail(c, http.StatusBadRequest, "invalid_request", fmt.Sprintf("%s 必须是 KOOK 的 ID（数字串）", label))
			return false
		}
		if err := s.Store.Settings.Set(key, v); err != nil {
			s.failInternal(c, err, "settings.update."+key)
			return false
		}
		changed = append(changed, key)
		return true
	}

	if req.KookToken != nil {
		token := strings.TrimSpace(*req.KookToken)
		if token != "" && len(token) < 10 {
			s.fail(c, http.StatusBadRequest, "invalid_request", "KOOK token 长度不合法")
			return
		}
		if err := s.Store.Settings.SetSecret(store.SettingKookToken, token, s.Config.AppSecret); err != nil {
			s.failInternal(c, err, "settings.update.token")
			return
		}
		// 审计只记录“是否更新”，绝不记录内容。
		changed = append(changed, "kook_token")
	}

	if !setPlain(store.SettingGuildID, "服务器 ID", req.GuildID, true) {
		return
	}
	if !setPlain(store.SettingCategoryID, "工单分组 ID", req.CategoryID, true) {
		return
	}
	if !setPlain(store.SettingLogChannelID, "日志频道 ID", req.LogChannelID, true) {
		return
	}
	if !setPlain(store.SettingDebugChannelID, "调试频道 ID", req.DebugChannelID, true) {
		return
	}

	// 展示名允许任意文本，仅做长度限制。
	for _, item := range []struct {
		key   string
		value *string
		mark  *bool
	}{
		{store.SettingGuildName, req.GuildName, &updatedNames},
		{store.SettingCategoryName, req.CategoryName, &updatedNames},
		{store.SettingLogChannelName, req.LogChannelName, &updatedNames},
		{store.SettingDebugChName, req.DebugChannelName, &updatedNames},
	} {
		if item.value == nil {
			continue
		}
		v := strings.TrimSpace(*item.value)
		if len([]rune(v)) > 64 {
			s.fail(c, http.StatusBadRequest, "invalid_request", "名称长度不能超过 64 个字符")
			return
		}
		if err := s.Store.Settings.Set(item.key, v); err != nil {
			s.failInternal(c, err, "settings.update.name")
			return
		}
		*item.mark = true
	}
	if updatedNames {
		changed = append(changed, "display_names")
	}

	if req.OutdateHours != nil {
		hours := *req.OutdateHours
		if hours < 0 || hours > 24*30 {
			s.fail(c, http.StatusBadRequest, "invalid_request", "超时小时数必须在 0–720 之间（0 表示不自动锁定）")
			return
		}
		if err := s.Store.Settings.SetInt(store.SettingOutdateHours, hours); err != nil {
			s.failInternal(c, err, "settings.update.outdate")
			return
		}
		changed = append(changed, store.SettingOutdateHours)
	}

	s.audit(c, "settings.update", "settings", "更新配置项："+strings.Join(changed, ", "))

	// Token / 服务器等关键配置变化需要重连才生效。
	// 连接失败不影响配置保存，把结果一并返回给界面提示。
	response := gin.H{"ok": true, "changed": changed}
	if s.Bot != nil && requiresBotRestart(changed) {
		if err := s.Bot.Restart(c.Request.Context()); err != nil {
			s.audit(c, "bot.restart", "bot", "配置变更后重连失败："+err.Error())
			response["botRestart"] = gin.H{"ok": false, "error": err.Error()}
		} else {
			s.audit(c, "bot.restart", "bot", "配置变更后已重新连接")
			response["botRestart"] = gin.H{"ok": true}
		}
	} else if s.Bot != nil {
		// 仅名称等展示字段变化：清缓存即可
		s.Bot.NotifyConfigChanged()
	}
	c.JSON(http.StatusOK, response)
}

// requiresBotRestart 判断哪些配置项变化后需要重新建立 KOOK 连接。
func requiresBotRestart(changed []string) bool {
	for _, key := range changed {
		switch key {
		case "kook_token", store.SettingGuildID, store.SettingCategoryID,
			store.SettingLogChannelID, store.SettingDebugChannelID:
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 账号管理
// ---------------------------------------------------------------------------

type userResponse struct {
	ID                 uint       `json:"id"`
	Username           string     `json:"username"`
	DisplayName        string     `json:"displayName"`
	Role               string     `json:"role"`
	KookUserID         *string    `json:"kookUserId,omitempty"`
	KookUserName       string     `json:"kookUserName,omitempty"`
	Disabled           bool       `json:"disabled"`
	MustChangePassword bool       `json:"mustChangePassword"`
	TOTPEnabled        bool       `json:"totpEnabled"`
	LastLoginAt        *time.Time `json:"lastLoginAt,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
}

func toUserResponse(u store.WebUser) userResponse {
	return userResponse{
		ID:                 u.ID,
		Username:           u.Username,
		DisplayName:        u.DisplayName,
		Role:               u.Role,
		KookUserID:         u.KookUserID,
		KookUserName:       u.KookUserName,
		Disabled:           u.Disabled,
		MustChangePassword: u.MustChangePassword,
		TOTPEnabled:        u.TOTPEnabled,
		LastLoginAt:        u.LastLoginAt,
		CreatedAt:          u.CreatedAt,
	}
}

func (s *Server) handleUserList(c *gin.Context) {
	users, err := s.Store.Users.List()
	if err != nil {
		s.failInternal(c, err, "users.list")
		return
	}
	items := make([]userResponse, 0, len(users))
	for _, u := range users {
		items = append(items, toUserResponse(u))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

type userCreateRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	Password    string `json:"password"`
}

func (s *Server) handleUserCreate(c *gin.Context) {
	var req userCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	username := store.NormalizeUsername(req.Username)
	if !usernamePattern.MatchString(username) {
		s.fail(c, http.StatusBadRequest, "invalid_request", "用户名需为 3–32 位小写字母、数字或 . _ -，且以字母或数字开头")
		return
	}
	if !validRole(req.Role) {
		s.fail(c, http.StatusBadRequest, "invalid_request", "角色取值不合法")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		s.fail(c, http.StatusBadRequest, "weak_password", err.Error())
		return
	}

	user := &store.WebUser{
		Username:     username,
		DisplayName:  strings.TrimSpace(req.DisplayName),
		PasswordHash: hash,
		Role:         req.Role,
		// 管理员代建的账号首次登录后应自行改密。
		MustChangePassword: true,
	}
	if user.DisplayName == "" {
		user.DisplayName = username
	}
	if err := s.Store.Users.Create(user); err != nil {
		s.failStore(c, err, "users.create")
		return
	}
	s.audit(c, "user.create", username, "创建账号，角色："+req.Role)
	c.JSON(http.StatusCreated, toUserResponse(*user))
}

type userUpdateRequest struct {
	DisplayName *string `json:"displayName"`
	Role        *string `json:"role"`
	Disabled    *bool   `json:"disabled"`
	Password    *string `json:"password"`
}

func (s *Server) handleUserUpdate(c *gin.Context) {
	id := pathID(c)
	if id == 0 {
		s.fail(c, http.StatusBadRequest, "invalid_request", "账号 ID 不合法")
		return
	}
	var req userUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	target, err := s.Store.Users.ByID(id)
	if err != nil {
		s.failStore(c, err, "users.update.target")
		return
	}
	identity := auth.IdentityOf(c)

	updates := map[string]any{"updated_at": store.Now()}
	details := make([]string, 0, 4)

	if req.DisplayName != nil {
		name := strings.TrimSpace(*req.DisplayName)
		if len([]rune(name)) > 64 {
			s.fail(c, http.StatusBadRequest, "invalid_request", "显示名不能超过 64 个字符")
			return
		}
		updates["display_name"] = name
		details = append(details, "显示名")
	}

	if req.Role != nil && *req.Role != target.Role {
		if !validRole(*req.Role) {
			s.fail(c, http.StatusBadRequest, "invalid_request", "角色取值不合法")
			return
		}
		if target.ID == identity.UserID {
			s.fail(c, http.StatusConflict, "self_modification", "不能修改自己的角色")
			return
		}
		if target.Role == store.RoleAdmin && *req.Role != store.RoleAdmin {
			remaining, err := s.adminCountExcluding(target.ID)
			if err != nil {
				s.failInternal(c, err, "users.update.admin_count")
				return
			}
			if remaining == 0 {
				s.fail(c, http.StatusConflict, "last_admin", "系统必须保留至少一个可用管理员")
				return
			}
		}
		updates["role"] = *req.Role
		details = append(details, "角色 → "+*req.Role)
	}

	if req.Disabled != nil && *req.Disabled != target.Disabled {
		if target.ID == identity.UserID {
			s.fail(c, http.StatusConflict, "self_modification", "不能禁用自己的账号")
			return
		}
		if *req.Disabled && target.Role == store.RoleAdmin {
			remaining, err := s.adminCountExcluding(target.ID)
			if err != nil {
				s.failInternal(c, err, "users.update.admin_count")
				return
			}
			if remaining == 0 {
				s.fail(c, http.StatusConflict, "last_admin", "系统必须保留至少一个可用管理员")
				return
			}
		}
		updates["disabled"] = *req.Disabled
		details = append(details, fmt.Sprintf("禁用=%t", *req.Disabled))
	}

	if req.Password != nil && *req.Password != "" {
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			s.fail(c, http.StatusBadRequest, "weak_password", err.Error())
			return
		}
		updates["password_hash"] = hash
		updates["must_change_password"] = true
		details = append(details, "重置密码")
	}

	if len(details) == 0 {
		c.JSON(http.StatusOK, toUserResponse(*target))
		return
	}
	if err := s.Store.Users.UpdateFields(target.ID, updates); err != nil {
		s.failStore(c, err, "users.update")
		return
	}

	updated, err := s.Store.Users.ByID(target.ID)
	if err != nil {
		s.failStore(c, err, "users.update.reload")
		return
	}
	s.audit(c, "user.update", target.Username, "更新账号："+strings.Join(details, "，"))
	c.JSON(http.StatusOK, toUserResponse(*updated))
}

func (s *Server) handleUserDelete(c *gin.Context) {
	id := pathID(c)
	if id == 0 {
		s.fail(c, http.StatusBadRequest, "invalid_request", "账号 ID 不合法")
		return
	}
	target, err := s.Store.Users.ByID(id)
	if err != nil {
		s.failStore(c, err, "users.delete.target")
		return
	}
	identity := auth.IdentityOf(c)
	if target.ID == identity.UserID {
		s.fail(c, http.StatusConflict, "self_modification", "不能删除自己的账号")
		return
	}
	if target.Role == store.RoleAdmin && !target.Disabled {
		remaining, err := s.adminCountExcluding(target.ID)
		if err != nil {
			s.failInternal(c, err, "users.delete.admin_count")
			return
		}
		if remaining == 0 {
			s.fail(c, http.StatusConflict, "last_admin", "系统必须保留至少一个可用管理员")
			return
		}
	}
	if err := s.Store.Users.Delete(target.ID); err != nil {
		s.failStore(c, err, "users.delete")
		return
	}
	s.audit(c, "user.delete", target.Username, "删除账号")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// adminCountExcluding 统计除指定账号外的可用管理员数量。
func (s *Server) adminCountExcluding(id uint) (int64, error) {
	var n int64
	err := s.Store.DB().Model(&store.WebUser{}).
		Where("role = ? AND disabled = ? AND id <> ?", store.RoleAdmin, false, id).
		Count(&n).Error
	return n, err
}

// ---------------------------------------------------------------------------
// 角色与权限映射
// ---------------------------------------------------------------------------

func (s *Server) handleAdminRoleList(c *gin.Context) {
	roles, err := s.Store.Roles.ListAdmin()
	if err != nil {
		s.failInternal(c, err, "roles.admin.list")
		return
	}
	if roles == nil {
		roles = []store.AdminRole{}
	}
	c.JSON(http.StatusOK, gin.H{"items": roles})
}

type adminRoleRequest struct {
	RoleID   string `json:"roleId"`
	RoleName string `json:"roleName"`
}

func (s *Server) handleAdminRoleAdd(c *gin.Context) {
	var req adminRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	if !validKookID(req.RoleID) {
		s.fail(c, http.StatusBadRequest, "invalid_request", "角色 ID 必须是 KOOK 的数字 ID")
		return
	}
	roleName := strings.TrimSpace(req.RoleName)
	if len([]rune(roleName)) > 64 {
		s.fail(c, http.StatusBadRequest, "invalid_request", "角色名不能超过 64 个字符")
		return
	}
	if err := s.Store.Roles.AddAdmin(strings.TrimSpace(req.RoleID), roleName); err != nil {
		s.failStore(c, err, "roles.admin.add")
		return
	}
	s.audit(c, "role.admin.add", req.RoleID, "新增全局管理员角色："+roleName)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleAdminRoleRemove(c *gin.Context) {
	roleID := strings.TrimSpace(c.Param("roleId"))
	if !validKookID(roleID) {
		s.fail(c, http.StatusBadRequest, "invalid_request", "角色 ID 不合法")
		return
	}
	if err := s.Store.Roles.RemoveAdmin(roleID); err != nil {
		s.failStore(c, err, "roles.admin.remove")
		return
	}
	s.audit(c, "role.admin.remove", roleID, "移除全局管理员角色")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleRoleMappingList(c *gin.Context) {
	items, err := s.Store.Roles.ListMappings()
	if err != nil {
		s.failInternal(c, err, "roles.mapping.list")
		return
	}
	if items == nil {
		items = []store.RoleMapping{}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

type roleMappingRequest struct {
	KookRoleID   string `json:"kookRoleId"`
	KookRoleName string `json:"kookRoleName"`
	WebRole      string `json:"webRole"`
}

func (s *Server) handleRoleMappingUpsert(c *gin.Context) {
	var req roleMappingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	if !validKookID(req.KookRoleID) {
		s.fail(c, http.StatusBadRequest, "invalid_request", "角色 ID 必须是 KOOK 的数字 ID")
		return
	}
	if !validRole(req.WebRole) {
		s.fail(c, http.StatusBadRequest, "invalid_request", "WebUI 角色取值不合法")
		return
	}
	mapping := &store.RoleMapping{
		KookRoleID:   strings.TrimSpace(req.KookRoleID),
		KookRoleName: strings.TrimSpace(req.KookRoleName),
		WebRole:      req.WebRole,
	}
	if err := s.Store.Roles.UpsertMapping(mapping); err != nil {
		s.failStore(c, err, "roles.mapping.upsert")
		return
	}
	s.audit(c, "role.mapping.upsert", mapping.KookRoleID, "映射为 WebUI 角色："+req.WebRole)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleRoleMappingDelete(c *gin.Context) {
	id := pathID(c)
	if id == 0 {
		s.fail(c, http.StatusBadRequest, "invalid_request", "ID 不合法")
		return
	}
	if err := s.Store.Roles.DeleteMapping(id); err != nil {
		s.failStore(c, err, "roles.mapping.delete")
		return
	}
	s.audit(c, "role.mapping.delete", fmt.Sprint(id), "删除角色映射")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---------------------------------------------------------------------------
// 面板、表情规则、审计、统计
// ---------------------------------------------------------------------------

func (s *Server) handlePanelList(c *gin.Context) {
	panels, err := s.Store.Panels.List()
	if err != nil {
		s.failInternal(c, err, "panels.list")
		return
	}
	if panels == nil {
		panels = []store.Panel{}
	}
	c.JSON(http.StatusOK, gin.H{"items": panels})
}

func (s *Server) handleEmojiRuleList(c *gin.Context) {
	rules, err := s.Store.Emoji.ListRules()
	if err != nil {
		s.failInternal(c, err, "emoji.rules.list")
		return
	}
	if rules == nil {
		rules = []store.EmojiRule{}
	}
	c.JSON(http.StatusOK, gin.H{"items": rules})
}

func (s *Server) handleEmojiGrantList(c *gin.Context) {
	grants, err := s.Store.Emoji.ListGrants(atoiDefault(c.Query("limit"), 100))
	if err != nil {
		s.failInternal(c, err, "emoji.grants.list")
		return
	}
	if grants == nil {
		grants = []store.EmojiGrant{}
	}
	c.JSON(http.StatusOK, gin.H{"items": grants})
}

func (s *Server) handleStatsOverview(c *gin.Context) {
	days := atoiDefault(c.Query("days"), 7)
	if days < 1 || days > 365 {
		days = 7
	}
	overview, err := s.overviewCache.get(strconv.Itoa(days), func() (*store.Overview, error) {
		return s.Store.Tickets.Overview(store.Now(), s.Config.Location, days)
	})
	if err != nil {
		s.failInternal(c, err, "stats.overview")
		return
	}
	c.JSON(http.StatusOK, overview)
}

func (s *Server) handleAuditList(c *gin.Context) {
	page, size := pageParams(c)
	filter := store.AuditFilter{
		Action:   strings.TrimSpace(c.Query("action")),
		Actor:    strings.TrimSpace(c.Query("actor")),
		Target:   strings.TrimSpace(c.Query("target")),
		Page:     page,
		PageSize: size,
	}
	from, err := timeParam(c, "from", s.Config.Location, false)
	if err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	to, err := timeParam(c, "to", s.Config.Location, true)
	if err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	filter.From, filter.To = from, to

	items, total, err := s.Store.Audit.List(filter)
	if err != nil {
		s.failInternal(c, err, "audit.list")
		return
	}
	if items == nil {
		items = []store.AuditLog{}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "pageSize": size})
}

// ---------------------------------------------------------------------------
// 运行信息与 KOOK 元数据
// ---------------------------------------------------------------------------

func (s *Server) handleRuntimeInfo(c *gin.Context) {
	runtimeCfg, err := s.Store.Settings.Runtime(s.Config.AppSecret)
	if err != nil {
		s.failInternal(c, err, "meta.runtime")
		return
	}
	var dbSize int64
	if info, err := os.Stat(s.Config.DBPath); err == nil {
		dbSize = info.Size()
	}

	botStatus := s.botStatus()
	c.JSON(http.StatusOK, gin.H{
		"dryRun":          s.Config.DryRun,
		"botConnected":    botStatus.Connected,
		"bot":             botStatus,
		"version":         s.Version,
		"goVersion":       runtime.Version(),
		"startedAt":       s.Started,
		"uptimeSeconds":   int64(time.Since(s.Started).Seconds()),
		"sseSubscribers":  s.Bus.Subscribers(),
		"sessionIdleSecs": int(s.Config.SessionIdleTTL.Seconds()),
		"sessionMaxSecs":  int(s.Config.SessionMaxTTL.Seconds()),
		"ticketTimezone":  s.Config.TicketTZ,
		"database": gin.H{
			"path":      s.Config.DBPath,
			"sizeBytes": dbSize,
		},
		"configuration": gin.H{
			"guildId":         runtimeCfg.GuildID,
			"categoryId":      runtimeCfg.CategoryID,
			"logChannelId":    runtimeCfg.LogChannelID,
			"debugChannelId":  runtimeCfg.DebugChannelID,
			"outdateHours":    runtimeCfg.OutdateHours,
			"kookTokenMasked": runtimeCfg.TokenMasked,
			"hasKookToken":    runtimeCfg.HasKookToken,
			"missingRequired": func() []string {
				if runtimeCfg.MissingRequired == nil {
					return []string{}
				}
				return runtimeCfg.MissingRequired
			}(),
		},
	})
}

type kookOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
}

// handleGuildRoles 返回可选角色列表。
//
// 机器人在线时读取真实角色；DryRun 或未连接时返回演示数据/空列表，
// 保证界面在离线状态下依然可用。
func (s *Server) handleGuildRoles(c *gin.Context) {
	if s.botConnected() {
		roles, err := s.Bot.GuildRoles(c.Request.Context())
		if err != nil {
			s.failInternal(c, err, "meta.guild.roles")
			return
		}
		items := make([]kookOption, 0, len(roles))
		for _, role := range roles {
			items = append(items, kookOption{ID: strconv.FormatInt(role.RoleID, 10), Name: role.Name, Kind: "role"})
		}
		c.JSON(http.StatusOK, gin.H{"available": true, "dryRun": false, "items": items})
		return
	}
	if s.Config.DryRun {
		c.JSON(http.StatusOK, gin.H{
			"available": true,
			"dryRun":    true,
			"items": []kookOption{
				{ID: "1001", Name: "服主", Kind: "role"},
				{ID: "1002", Name: "客服组", Kind: "role"},
				{ID: "1003", Name: "实习客服", Kind: "role"},
			},
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"available": false,
		"dryRun":    false,
		"items":     []kookOption{},
		"note":      "角色列表需要机器人连接 KOOK 后获取",
	})
}

// handleGuildChannels 返回可选频道/分组列表。
func (s *Server) handleGuildChannels(c *gin.Context) {
	if s.botConnected() {
		channels, err := s.Bot.GuildChannels(c.Request.Context())
		if err != nil {
			s.failInternal(c, err, "meta.guild.channels")
			return
		}
		items := make([]kookOption, 0, len(channels))
		for _, channel := range channels {
			kind := "text"
			switch {
			case channel.IsCategory:
				kind = "category"
			case channel.Type == kook.ChannelVoice:
				kind = "voice"
			}
			items = append(items, kookOption{ID: channel.ID, Name: channel.Name, Kind: kind})
		}
		c.JSON(http.StatusOK, gin.H{"available": true, "dryRun": false, "items": items})
		return
	}
	if s.Config.DryRun {
		c.JSON(http.StatusOK, gin.H{
			"available": true,
			"dryRun":    true,
			"items": []kookOption{
				{ID: "chan-panel", Name: "工单面板", Kind: "text"},
				{ID: "chan-log", Name: "工单日志", Kind: "text"},
				{ID: "chan-debug", Name: "机器人调试", Kind: "text"},
				{ID: "cat-hidden", Name: "隐藏工单分组", Kind: "category"},
			},
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"available": false,
		"dryRun":    false,
		"items":     []kookOption{},
		"note":      "频道列表需要机器人连接 KOOK 后获取",
	})
}

// handleBotRestart 使用最新配置重新建立 KOOK 连接（仅管理员）。
func (s *Server) handleBotRestart(c *gin.Context) {
	if s.Bot == nil {
		s.fail(c, http.StatusServiceUnavailable, "bot_disabled", "机器人模块未启用")
		return
	}
	if err := s.Bot.Restart(c.Request.Context()); err != nil {
		s.audit(c, "bot.restart", "bot", "重启失败："+err.Error())
		s.fail(c, http.StatusBadGateway, "bot_restart_failed", "连接 KOOK 失败："+err.Error())
		return
	}
	s.audit(c, "bot.restart", "bot", "已按最新配置重新连接")
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": s.Bot.Status()})
}

// ---------------------------------------------------------------------------
// 实时事件（SSE）
// ---------------------------------------------------------------------------

// heartbeatInterval 是 SSE 心跳间隔，用于穿透反代与检测断线。
const heartbeatInterval = 25 * time.Second

// readonlyEventTypes 是只读角色可以接收的事件类型白名单。
func readonlyEventTypes(eventType string) bool {
	switch eventType {
	case "ticket.created", "ticket.updated", "ticket.message", "ticket.note", "stats.invalidated":
		return true
	default:
		return false
	}
}

// handleEvents 建立 SSE 长连接推送实时事件。
func (s *Server) handleEvents(c *gin.Context) {
	identity := auth.IdentityOf(c)
	role := identity.Role
	token, _ := c.Cookie(config.SessionCookieName)

	subID, ch, allowed := s.Bus.SubscribeLimited(strconv.FormatUint(uint64(identity.UserID), 10), 5, 64)
	if !allowed {
		s.fail(c, http.StatusTooManyRequests, "too_many_streams", "每个账号最多同时建立 5 条实时连接")
		return
	}
	defer s.Bus.Unsubscribe(subID)
	controller := http.NewResponseController(c.Writer)
	defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	send := func(eventType string, data any) bool {
		if err := controller.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return false
		}
		c.SSEvent(eventType, data)
		return controller.Flush() == nil
	}
	// 事件路径上的会话校验做降频：每条事件都查 sessions + users 两次库，
	// 事件风暴时 N 个订阅者 × M 条事件会挤占仅 4 个连接池的连接。
	// 心跳路径（25s）仍保持每次全量校验，“禁用 / 改密”最迟 25 秒内生效。
	const checkInterval = 10 * time.Second
	lastCheckAt := time.Now().Add(-checkInterval)
	check := func() bool {
		lastCheckAt = time.Now()
		_, user, err := s.Sessions.Check(token)
		if err != nil || user.MustChangePassword || !store.RoleAtLeast(user.Role, store.RoleReadonly) {
			send("auth.expired", gin.H{"at": store.Now()})
			return false
		}
		role = user.Role
		return true
	}
	checkThrottled := func() bool {
		if time.Since(lastCheckAt) < checkInterval {
			return true
		}
		return check()
	}

	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	if !send("ready", gin.H{"role": role, "subscriberId": subID, "at": store.Now()}) {
		return
	}

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			if !checkThrottled() {
				return
			}
			if role == store.RoleReadonly && !readonlyEventTypes(event.Type) {
				continue
			}
			if !send(event.Type, event) {
				return
			}
		case <-heartbeat.C:
			if !check() || !send("ping", gin.H{"at": store.Now()}) {
				return
			}
		}
	}
}
