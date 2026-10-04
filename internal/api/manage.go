package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"vancekookticket/internal/store"
)

// 面板与表情规则的校验规则。
var (
	// messageIDPattern 校验 KOOK 消息 ID（十六进制串，长度较固定，但放宽以兼容平台变化）。
	messageIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{16,64}$`)
)

// maxPanelTitleLength 是面板文案的长度上限（按字符计）。
//
// 面板正文按 KMarkdown 渲染且允许多行，因此上限比普通输入框宽松得多；
// 仍保留一个上限，避免超出 KOOK 卡片内容限制。
const maxPanelTitleLength = 2000

// ---------------------------------------------------------------------------
// 工单面板
// ---------------------------------------------------------------------------

type panelCreateRequest struct {
	ChannelID  string `json:"channelId"`
	Title      string `json:"title"`
	ButtonText string `json:"buttonText"`
}

// handlePanelCreate 在指定频道创建工单面板。
//
// 面板卡片必须由机器人发送（按钮 value 需要签名），因此该接口依赖机器人在线：
// 未连接时返回 503 并提示先完成连接，避免写入一个永远不会生效的配置。
func (s *Server) handlePanelCreate(c *gin.Context) {
	var req panelCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	channelID := strings.TrimSpace(req.ChannelID)
	if !validKookID(channelID) {
		s.fail(c, http.StatusBadRequest, "invalid_request", "频道 ID 必须是 KOOK 的数字 ID")
		return
	}
	if s.Bot == nil || !s.botConnected() {
		s.fail(c, http.StatusServiceUnavailable, "bot_offline",
			"机器人未连接 KOOK，无法发送面板卡片。请先在「系统设置」完成配置并重连。")
		return
	}

	title := strings.TrimSpace(req.Title)
	if len([]rune(title)) > maxPanelTitleLength {
		s.fail(c, http.StatusBadRequest, "invalid_request", fmt.Sprintf("面板文案不能超过 %d 个字符", maxPanelTitleLength))
		return
	}
	buttonText := strings.TrimSpace(req.ButtonText)
	if len([]rune(buttonText)) > 32 {
		s.fail(c, http.StatusBadRequest, "invalid_request", "按钮文字不能超过 32 个字符")
		return
	}

	channel, err := s.Bot.ChannelInfo(c.Request.Context(), channelID)
	if err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_channel", err.Error())
		return
	}
	if channel.IsCategory {
		s.fail(c, http.StatusBadRequest, "invalid_channel", "分组不能作为工单面板频道")
		return
	}

	// 先落库拿到面板 ID，再发送卡片：按钮 value 会内嵌该 ID，
	// 使同一频道内的多张卡片能各自携带独立的角色配置。
	panel := &store.Panel{
		ChannelID:   channelID,
		ChannelName: channel.Name,
		Title:       firstNonEmptyString(title, "请点击右侧按钮发起工单"),
		ButtonText:  firstNonEmptyString(buttonText, "ticket"),
		Enabled:     true,
	}
	if err := s.Store.Panels.Create(panel); err != nil {
		s.failStore(c, err, "panel.create.save")
		return
	}

	msgID, err := s.Bot.SendPanelCard(c.Request.Context(), panel, buttonText)
	if err != nil {
		// 卡片未能发出时回滚记录，避免留下“看不到卡片但配置存在”的脏数据。
		if delErr := s.Store.Panels.Delete(panel.ID); delErr != nil {
			s.Log.Warn("回滚面板记录失败", "panel_id", panel.ID, "err", delErr)
		}
		s.failInternal(c, err, "panel.create")
		return
	}
	if err := s.Store.Panels.UpdateFields(panel.ID, map[string]any{"msg_id": msgID}); err != nil {
		s.failStore(c, err, "panel.create.save")
		return
	}
	s.Bot.NotifyConfigChanged()

	stored, err := s.Store.Panels.ByID(panel.ID)
	if err != nil {
		s.failStore(c, err, "panel.create.reload")
		return
	}
	s.audit(c, "panel.create", channelID, "创建面板，频道："+channel.Name+"，消息："+msgID)
	c.JSON(http.StatusCreated, stored)
}

type panelUpdateRequest struct {
	Enabled    *bool   `json:"enabled"`
	Title      *string `json:"title"`
	ButtonText *string `json:"buttonText"`
}

// handlePanelUpdate 更新面板的可编辑字段。
//
// 说明：文案与按钮文字的变更需要重建卡片才会生效（调用 refresh），
// 这里只更新数据库记录，重建时会按记录内容恢复。
func (s *Server) handlePanelUpdate(c *gin.Context) {
	id := uint(atoiDefault(c.Param("id"), 0))
	if id == 0 {
		s.fail(c, http.StatusBadRequest, "invalid_request", "面板 ID 不合法")
		return
	}
	panel, err := s.Store.Panels.ByID(id)
	if err != nil {
		s.failStore(c, err, "panel.update.lookup")
		return
	}

	var req panelUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}

	updates := map[string]any{}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if len([]rune(title)) > maxPanelTitleLength {
			s.fail(c, http.StatusBadRequest, "invalid_request", fmt.Sprintf("面板文案不能超过 %d 个字符", maxPanelTitleLength))
			return
		}
		updates["title"] = title
	}
	if req.ButtonText != nil {
		buttonText := strings.TrimSpace(*req.ButtonText)
		if len([]rune(buttonText)) > 32 {
			s.fail(c, http.StatusBadRequest, "invalid_request", "按钮文字不能超过 32 个字符")
			return
		}
		updates["button_text"] = firstNonEmptyString(buttonText, "ticket")
	}
	if len(updates) == 0 {
		c.JSON(http.StatusOK, panel)
		return
	}
	if err := s.Store.Panels.UpdateFields(panel.ID, updates); err != nil {
		s.failStore(c, err, "panel.update")
		return
	}
	s.audit(c, "panel.update", panel.ChannelID, "更新面板配置")

	updated, err := s.Store.Panels.ByID(panel.ID)
	if err != nil {
		s.failStore(c, err, "panel.update.reload")
		return
	}
	c.JSON(http.StatusOK, updated)
}

// handlePanelRefresh 重新发送面板卡片（删除旧卡片并更新消息 ID）。
func (s *Server) handlePanelRefresh(c *gin.Context) {
	id := uint(atoiDefault(c.Param("id"), 0))
	if id == 0 {
		s.fail(c, http.StatusBadRequest, "invalid_request", "面板 ID 不合法")
		return
	}
	panel, err := s.Store.Panels.ByID(id)
	if err != nil {
		s.failStore(c, err, "panel.refresh.lookup")
		return
	}
	if s.Bot == nil || !s.botConnected() {
		s.fail(c, http.StatusServiceUnavailable, "bot_offline", "机器人未连接 KOOK，无法重建面板卡片")
		return
	}

	msgID, err := s.Bot.SendPanelCard(c.Request.Context(), panel, panel.ButtonText)
	if err != nil {
		s.failInternal(c, err, "panel.refresh")
		return
	}
	// 旧卡片删除失败不影响结果（可能已被手动删除）
	if panel.MsgID != "" && panel.MsgID != msgID {
		if err := s.Bot.DeleteMessage(c.Request.Context(), panel.MsgID); err != nil {
			s.Log.Warn("删除旧面板卡片失败", "panel_id", panel.ID, "msg_id", panel.MsgID, "err", err)
		}
	}
	if err := s.Store.Panels.UpdateFields(panel.ID, map[string]any{"msg_id": msgID, "enabled": true}); err != nil {
		s.failStore(c, err, "panel.refresh.save")
		return
	}
	s.audit(c, "panel.refresh", panel.ChannelID, "重建面板卡片")

	updated, err := s.Store.Panels.ByID(panel.ID)
	if err != nil {
		s.failStore(c, err, "panel.refresh.reload")
		return
	}
	c.JSON(http.StatusOK, updated)
}

// handlePanelDelete 删除面板配置（并尽力删除频道内的卡片消息）。
func (s *Server) handlePanelDelete(c *gin.Context) {
	id := uint(atoiDefault(c.Param("id"), 0))
	if id == 0 {
		s.fail(c, http.StatusBadRequest, "invalid_request", "面板 ID 不合法")
		return
	}
	panel, err := s.Store.Panels.ByID(id)
	if err != nil {
		s.failStore(c, err, "panel.delete.lookup")
		return
	}

	if s.Bot != nil && s.botConnected() && panel.MsgID != "" {
		if err := s.Bot.DeleteMessage(c.Request.Context(), panel.MsgID); err != nil {
			s.Log.Warn("删除面板卡片失败", "panel_id", panel.ID, "err", err)
		}
	}
	if err := s.Store.Panels.Delete(panel.ID); err != nil {
		s.failStore(c, err, "panel.delete")
		return
	}
	s.Bot.NotifyConfigChanged()
	s.audit(c, "panel.delete", panel.ChannelID, "删除面板配置")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type panelRoleRequest struct {
	RoleID   string `json:"roleId"`
	RoleName string `json:"roleName"`
}

// handlePanelRoleAdd 添加面板级管理员角色（等价于 /aar @角色）。
func (s *Server) handlePanelRoleAdd(c *gin.Context) {
	id := uint(atoiDefault(c.Param("id"), 0))
	if id == 0 {
		s.fail(c, http.StatusBadRequest, "invalid_request", "面板 ID 不合法")
		return
	}
	panel, err := s.Store.Panels.ByID(id)
	if err != nil {
		s.failStore(c, err, "panel.role.lookup")
		return
	}

	var req panelRoleRequest
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

	if err := s.Store.Panels.AddRole(panel.ID, roleID, roleName); err != nil {
		s.failStore(c, err, "panel.role.add")
		return
	}
	s.audit(c, "role.panel.add", roleID, "面板 "+panel.ChannelID+" 新增管理员角色")
	c.JSON(http.StatusCreated, gin.H{"ok": true})
}

// handlePanelRoleRemove 移除面板级管理员角色。
func (s *Server) handlePanelRoleRemove(c *gin.Context) {
	id := uint(atoiDefault(c.Param("id"), 0))
	roleID := strings.TrimSpace(c.Param("roleId"))
	if id == 0 || !validKookID(roleID) {
		s.fail(c, http.StatusBadRequest, "invalid_request", "参数不合法")
		return
	}
	if _, err := s.Store.Panels.ByID(id); err != nil {
		s.failStore(c, err, "panel.role.lookup")
		return
	}
	if err := s.Store.Panels.RemoveRole(id, roleID); err != nil {
		s.failStore(c, err, "panel.role.remove")
		return
	}
	s.audit(c, "role.panel.remove", roleID, "移除面板管理员角色")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---------------------------------------------------------------------------
// 表情上角色规则
// ---------------------------------------------------------------------------

type emojiRuleRequest struct {
	MessageID string  `json:"messageId"`
	ChannelID string  `json:"channelId"`
	EmojiID   string  `json:"emojiId"`
	RoleID    string  `json:"roleId"`
	Label     *string `json:"label"`
	Enabled   *bool   `json:"enabled"`
}

// handleEmojiRuleCreate 新增表情上角色规则。
//
// 使用方式：管理员先在频道里发出“角色选择”消息（可用 KOOK 卡片编辑器），
// 复制该消息 ID 后在此登记“表情 → 角色”的对应关系。
func (s *Server) handleEmojiRuleCreate(c *gin.Context) {
	var req emojiRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	if err := validateEmojiRuleInput(req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	rule := &store.EmojiRule{
		MessageID: strings.TrimSpace(req.MessageID),
		ChannelID: strings.TrimSpace(req.ChannelID),
		EmojiID:   strings.TrimSpace(req.EmojiID),
		RoleID:    strings.TrimSpace(req.RoleID),
		Label:     strings.TrimSpace(derefString(req.Label)),
		Enabled:   true,
	}
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}
	if err := s.Store.Emoji.CreateRule(rule); err != nil {
		s.failStore(c, err, "emoji.rule.create")
		return
	}
	s.audit(c, "emoji.rule.create", rule.EmojiID, "新增表情上角色规则 → 角色 "+rule.RoleID)
	c.JSON(http.StatusCreated, rule)
}

// handleEmojiRuleUpdate 更新规则（表情、角色、备注、启用状态）。
func (s *Server) handleEmojiRuleUpdate(c *gin.Context) {
	id := uint(atoiDefault(c.Param("id"), 0))
	if id == 0 {
		s.fail(c, http.StatusBadRequest, "invalid_request", "规则 ID 不合法")
		return
	}
	rule, err := s.Store.Emoji.RuleByID(id)
	if err != nil {
		s.failStore(c, err, "emoji.rule.lookup")
		return
	}

	var req emojiRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}

	if value := strings.TrimSpace(req.EmojiID); value != "" {
		if len([]rune(value)) > 32 {
			s.fail(c, http.StatusBadRequest, "invalid_request", "表情 ID 过长")
			return
		}
		rule.EmojiID = value
	}
	if value := strings.TrimSpace(req.RoleID); value != "" {
		if !validKookID(value) {
			s.fail(c, http.StatusBadRequest, "invalid_request", "角色 ID 必须是 KOOK 的数字 ID")
			return
		}
		rule.RoleID = value
	}
	if req.Label != nil {
		label := strings.TrimSpace(*req.Label)
		if len([]rune(label)) > 64 {
			s.fail(c, http.StatusBadRequest, "invalid_request", "备注不能超过 64 个字符")
			return
		}
		rule.Label = label
	}
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}
	if err := s.Store.Emoji.UpdateRule(rule); err != nil {
		s.failStore(c, err, "emoji.rule.update")
		return
	}
	s.audit(c, "emoji.rule.update", rule.EmojiID, "更新表情上角色规则")
	c.JSON(http.StatusOK, rule)
}

// handleEmojiRuleDelete 删除规则。
func (s *Server) handleEmojiRuleDelete(c *gin.Context) {
	id := uint(atoiDefault(c.Param("id"), 0))
	if id == 0 {
		s.fail(c, http.StatusBadRequest, "invalid_request", "规则 ID 不合法")
		return
	}
	if _, err := s.Store.Emoji.RuleByID(id); err != nil {
		s.failStore(c, err, "emoji.rule.lookup")
		return
	}
	if err := s.Store.Emoji.DeleteRule(id); err != nil {
		s.failStore(c, err, "emoji.rule.delete")
		return
	}
	s.audit(c, "emoji.rule.delete", strconv.FormatUint(uint64(id), 10), "删除表情上角色规则")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// derefString 安全解引用字符串指针。
func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// validateEmojiRuleInput 校验新增规则的必填字段。
func validateEmojiRuleInput(req emojiRuleRequest) error {
	messageID := strings.TrimSpace(req.MessageID)
	if !messageIDPattern.MatchString(messageID) {
		return errors.New("消息 ID 格式不合法（应为一串十六进制字符）")
	}
	if value := strings.TrimSpace(req.ChannelID); value != "" && !validKookID(value) {
		return errors.New("频道 ID 必须是 KOOK 的数字 ID")
	}
	emojiID := strings.TrimSpace(req.EmojiID)
	if emojiID == "" || len([]rune(emojiID)) > 32 {
		return errors.New("请填写表情 ID（1–32 个字符）")
	}
	if !validKookID(strings.TrimSpace(req.RoleID)) {
		return errors.New("角色 ID 必须是 KOOK 的数字 ID")
	}
	if len([]rune(strings.TrimSpace(derefString(req.Label)))) > 64 {
		return errors.New("备注不能超过 64 个字符")
	}
	return nil
}

// ---------------------------------------------------------------------------
// 统计细化
// ---------------------------------------------------------------------------

// handleStatsAnalytics 返回细化统计（分位时长、时段分布、客服处理量、来源分布）。
func (s *Server) handleStatsAnalytics(c *gin.Context) {
	days := atoiDefault(c.Query("days"), 30)
	if days < 1 || days > 365 {
		days = 30
	}
	analytics, err := s.Store.Tickets.Analytics(store.Now(), s.Config.Location, days)
	if err != nil {
		s.failInternal(c, err, "stats.analytics")
		return
	}

	// 把来源频道 ID 换成可读名称，避免前端再查一次
	panels, err := s.Store.Panels.List()
	if err == nil {
		names := make(map[string]string, len(panels))
		for _, panel := range panels {
			names[panel.ChannelID] = firstNonEmptyString(panel.ChannelName, panel.ChannelID)
		}
		for i := range analytics.Sources {
			if name, ok := names[analytics.Sources[i].ChannelID]; ok {
				analytics.Sources[i].ChannelID = analytics.Sources[i].ChannelID + "|" + name
			}
		}
	}

	c.JSON(http.StatusOK, analytics)
}
