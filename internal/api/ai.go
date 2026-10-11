package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/ai"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// AIController 是 API 层对 AI 服务的最小依赖（由 ai.Service 实现）。
//
// 与 BotController 同理：消费方定义接口，接口测试不需要真实 LLM。
type AIController interface {
	// TestConnection 用当前配置做一次最小 LLM 调用。
	TestConnection(ctx context.Context) error
	// TriggerSummary 异步生成工单摘要。
	TriggerSummary(no string) error
	// TriggerClassification 异步生成工单类型建议。
	TriggerClassification(no string) error
	// ApplyClassification 把类型建议应用到工单。
	ApplyClassification(no string, typeName string) error
	// ToggleTicket 暂停或恢复某工单的 AI 自动应答。
	ToggleTicket(no string, enable bool) error
	// SearchKnowledge 检索知识库（预览检索效果）。
	SearchKnowledge(ctx context.Context, query string) ([]store.AIKnowledge, error)
	// EmbedKnowledgeNow 为指定知识条目立即生成向量。
	EmbedKnowledgeNow(ctx context.Context, id uint) error
}

// aiSettingsResponse 是设置页的响应体：不含 API Key 明文，只有掩码。
type aiSettingsResponse struct {
	Enabled            bool                `json:"enabled"`
	BaseURL            string              `json:"baseUrl"`
	Model              string              `json:"model"`
	EmbeddingModel     string              `json:"embeddingModel"`
	Temperature        float64             `json:"temperature"`
	SystemPrompt       string              `json:"systemPrompt"`
	Footer             string              `json:"footer"`
	QuietSeconds       int                 `json:"quietSeconds"`
	MaxReplies         int                 `json:"maxReplies"`
	ContextMessages    int                 `json:"contextMessages"`
	KnowledgeTopK      int                 `json:"knowledgeTopK"`
	AutoSummaryOnClose bool                `json:"autoSummaryOnClose"`
	CallRetentionDays  int                 `json:"callRetentionDays"`
	HasAPIKey          bool                `json:"hasApiKey"`
	APIKeyMasked       string              `json:"apiKeyMasked"`
	Usage              *store.AIUsageStats `json:"usage,omitempty"`
}

// aiSettingsUpdateRequest 是保存设置的请求体。
//
// APIKey 语义：非空 = 写入新 Key；空 = 保持现状；ClearAPIKey = true = 删除。
type aiSettingsUpdateRequest struct {
	Enabled            *bool    `json:"enabled"`
	BaseURL            *string  `json:"baseUrl"`
	Model              *string  `json:"model"`
	EmbeddingModel     *string  `json:"embeddingModel"`
	APIKey             *string  `json:"apiKey"`
	ClearAPIKey        bool     `json:"clearApiKey"`
	Temperature        *float64 `json:"temperature"`
	SystemPrompt       *string  `json:"systemPrompt"`
	Footer             *string  `json:"footer"`
	QuietSeconds       *int     `json:"quietSeconds"`
	MaxReplies         *int     `json:"maxReplies"`
	ContextMessages    *int     `json:"contextMessages"`
	KnowledgeTopK      *int     `json:"knowledgeTopK"`
	AutoSummaryOnClose *bool    `json:"autoSummaryOnClose"`
	CallRetentionDays  *int     `json:"callRetentionDays"`
}

// handleAISettingsGet 返回 AI 配置（管理员）。
func (s *Server) handleAISettingsGet(c *gin.Context) {
	cfg, err := s.Store.AI.Config(s.Config.AppSecret)
	if err != nil {
		s.failInternal(c, err, "ai.settings.get")
		return
	}
	resp := &aiSettingsResponse{
		Enabled:            cfg.Enabled,
		BaseURL:            cfg.BaseURL,
		Model:              cfg.Model,
		EmbeddingModel:     cfg.EmbeddingModel,
		Temperature:        cfg.Temperature,
		SystemPrompt:       cfg.SystemPrompt,
		Footer:             cfg.Footer,
		QuietSeconds:       cfg.QuietSeconds,
		MaxReplies:         cfg.MaxReplies,
		ContextMessages:    cfg.ContextMessages,
		KnowledgeTopK:      cfg.KnowledgeTopK,
		AutoSummaryOnClose: cfg.AutoSummaryOnClose,
		CallRetentionDays:  cfg.CallRetentionDays,
		HasAPIKey:          cfg.HasAPIKey,
		APIKeyMasked:       cfg.APIKeyMasked,
	}
	if usage, err := s.Store.AI.AIUsage(7); err == nil {
		resp.Usage = usage
	}
	c.JSON(http.StatusOK, resp)
}

// handleAISettingsUpdate 保存 AI 配置（管理员）。
func (s *Server) handleAISettingsUpdate(c *gin.Context) {
	var req aiSettingsUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "bad_request", "请求体格式错误")
		return
	}

	// 以当前配置为基底做部分更新：未提供的字段保持原值。
	cfg, err := s.Store.AI.Config(s.Config.AppSecret)
	if err != nil {
		s.failInternal(c, err, "ai.settings.get")
		return
	}
	applyAISettingsUpdate(cfg, &req)

	apiKey := ""
	if req.APIKey != nil {
		apiKey = strings.TrimSpace(*req.APIKey)
	}
	// 校验发生在保存前：请求携带的新 Key 视为已配置（保存语义：非空 = 写入）。
	// 只影响本次校验，HasAPIKey 不参与落库。
	if apiKey != "" {
		cfg.HasAPIKey = true
	}
	if err := validateAISettings(cfg); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_settings", err.Error())
		return
	}

	if err := s.Store.AI.SaveConfig(cfg, apiKey, req.ClearAPIKey, s.Config.AppSecret); err != nil {
		s.failInternal(c, err, "ai.settings.update")
		return
	}

	s.audit(c, "ai.settings.update", "ai", "更新 AI 设置")
	s.handleAISettingsGet(c)
}

// applyAISettingsUpdate 把请求里提供的字段覆盖到配置快照上。
func applyAISettingsUpdate(cfg *store.AISettings, req *aiSettingsUpdateRequest) {
	if req.Enabled != nil {
		cfg.Enabled = *req.Enabled
	}
	if req.BaseURL != nil {
		cfg.BaseURL = strings.TrimSpace(*req.BaseURL)
	}
	if req.Model != nil {
		cfg.Model = strings.TrimSpace(*req.Model)
	}
	if req.EmbeddingModel != nil {
		cfg.EmbeddingModel = strings.TrimSpace(*req.EmbeddingModel)
	}
	if req.Temperature != nil {
		cfg.Temperature = *req.Temperature
	}
	if req.SystemPrompt != nil {
		cfg.SystemPrompt = strings.TrimSpace(*req.SystemPrompt)
	}
	if req.Footer != nil {
		cfg.Footer = strings.TrimSpace(*req.Footer)
	}
	if req.QuietSeconds != nil {
		cfg.QuietSeconds = *req.QuietSeconds
	}
	if req.MaxReplies != nil {
		cfg.MaxReplies = *req.MaxReplies
	}
	if req.ContextMessages != nil {
		cfg.ContextMessages = *req.ContextMessages
	}
	if req.KnowledgeTopK != nil {
		cfg.KnowledgeTopK = *req.KnowledgeTopK
	}
	if req.AutoSummaryOnClose != nil {
		cfg.AutoSummaryOnClose = *req.AutoSummaryOnClose
	}
	if req.CallRetentionDays != nil {
		cfg.CallRetentionDays = *req.CallRetentionDays
	}
}

// validateAISettings 校验配置取值范围。
//
// 开启状态下必须具备可用的连接三要素（BaseURL 可留空走默认 OpenAI 地址，
// 但 Key 与模型名不能缺），否则自动应答会静默失效。
func validateAISettings(cfg *store.AISettings) error {
	if cfg.Temperature < 0 || cfg.Temperature > 2 {
		return errors.New("温度取值范围为 0–2")
	}
	if cfg.QuietSeconds < 1 || cfg.QuietSeconds > 120 {
		return errors.New("防抖静默秒数范围为 1–120")
	}
	if cfg.MaxReplies < 1 || cfg.MaxReplies > 100 {
		return errors.New("每单回复上限范围为 1–100")
	}
	if cfg.ContextMessages < 5 || cfg.ContextMessages > 100 {
		return errors.New("上下文消息条数范围为 5–100")
	}
	if cfg.KnowledgeTopK < 1 || cfg.KnowledgeTopK > 20 {
		return errors.New("知识示例条数范围为 1–20")
	}
	if cfg.CallRetentionDays < 0 || cfg.CallRetentionDays > 3650 {
		return errors.New("调用记录保留天数范围为 0–3650（0 表示永久）")
	}
	if len([]rune(cfg.SystemPrompt)) > 4000 {
		return errors.New("系统提示词不能超过 4000 字")
	}
	if len([]rune(cfg.Footer)) > 200 {
		return errors.New("回复尾注不能超过 200 字")
	}
	if cfg.BaseURL != "" {
		parsed, err := url.Parse(cfg.BaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return errors.New("BaseURL 必须是合法的 http(s) 地址")
		}
	}
	if len([]rune(cfg.Model)) > 128 {
		return errors.New("模型名过长")
	}
	if cfg.Enabled {
		if !cfg.HasAPIKey && cfg.APIKeyPlaintext == "" {
			return errors.New("开启 AI 前需要先填写 API Key")
		}
		if cfg.Model == "" {
			return errors.New("开启 AI 前需要先填写模型名")
		}
	}
	return nil
}

// handleAITest 用当前配置做一次连通性测试（管理员）。
func (s *Server) handleAITest(c *gin.Context) {
	if s.AI == nil {
		s.fail(c, http.StatusServiceUnavailable, "ai_unavailable", "AI 服务未启用")
		return
	}
	cfg, err := s.Store.AI.Config(s.Config.AppSecret)
	if err != nil {
		s.failInternal(c, err, "ai.test")
		return
	}
	if !cfg.Enabled || !cfg.HasAPIKey || cfg.Model == "" {
		s.fail(c, http.StatusBadRequest, "ai_not_configured", "请先填写并保存 API Key 与模型名，再进行测试")
		return
	}
	if err := s.AI.TestConnection(c.Request.Context()); err != nil {
		s.audit(c, "ai.test", "ai", "AI 连通性测试失败")
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": friendlyAIError(err)})
		return
	}
	s.audit(c, "ai.test", "ai", "AI 连通性测试成功")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// friendlyAIError 把 LLM 错误转成用户可读的提示（不含 Key）。
func friendlyAIError(err error) string {
	message := err.Error()
	if strings.Contains(message, "401") || strings.Contains(message, "无效") {
		return "API Key 无效或未授权，请检查后重试"
	}
	if strings.Contains(message, "429") || strings.Contains(message, "限流") {
		return "请求被服务商限流，请稍后重试"
	}
	if len([]rune(message)) > 200 {
		return string([]rune(message)[:200]) + "…"
	}
	return message
}

// ---------------------------------------------------------------------------
// 知识库
// ---------------------------------------------------------------------------

// handleAIKnowledgeList 分页查询知识条目（管理员）。
func (s *Server) handleAIKnowledgeList(c *gin.Context) {
	page, size := pageParams(c)
	var enabled *bool
	switch strings.ToLower(strings.TrimSpace(c.Query("enabled"))) {
	case "true", "1":
		value := true
		enabled = &value
	case "false", "0":
		value := false
		enabled = &value
	}
	items, total, err := s.Store.AI.KnowledgeList(store.KnowledgeFilter{
		Query:    strings.TrimSpace(c.Query("q")),
		Source:   strings.TrimSpace(c.Query("source")),
		Enabled:  enabled,
		Page:     page,
		PageSize: size,
	})
	if err != nil {
		s.failInternal(c, err, "ai.knowledge.list")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "pageSize": size})
}

// handleAIKnowledgeCreate 手动新增知识条目（管理员）。
func (s *Server) handleAIKnowledgeCreate(c *gin.Context) {
	var req struct {
		Question string `json:"question"`
		Answer   string `json:"answer"`
		Enabled  *bool  `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "bad_request", "请求体格式错误")
		return
	}
	req.Question, req.Answer = strings.TrimSpace(req.Question), strings.TrimSpace(req.Answer)
	if req.Question == "" || req.Answer == "" {
		s.fail(c, http.StatusBadRequest, "bad_request", "问题与答案都不能为空")
		return
	}
	if len([]rune(req.Question)) > 2000 || len([]rune(req.Answer)) > 2000 {
		s.fail(c, http.StatusBadRequest, "bad_request", "问题与答案不能超过 2000 字")
		return
	}
	item := &store.AIKnowledge{Question: req.Question, Answer: req.Answer, Enabled: true}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if err := s.Store.AI.KnowledgeCreate(item); err != nil {
		s.failStore(c, err, "ai.knowledge.create")
		return
	}
	s.audit(c, "ai.knowledge.create", strconv.FormatUint(uint64(item.ID), 10), "新增知识条目")
	// 配置了 embedding 模型时立即向量化（失败不影响创建结果）。
	if s.AI != nil {
		_ = s.AI.EmbedKnowledgeNow(c.Request.Context(), item.ID)
	}
	c.JSON(http.StatusCreated, item)
}

// handleAIKnowledgeUpdate 更新知识条目（管理员）。
func (s *Server) handleAIKnowledgeUpdate(c *gin.Context) {
	id := pathID(c)
	if id == 0 {
		s.fail(c, http.StatusBadRequest, "bad_request", "ID 不合法")
		return
	}
	var req struct {
		Question *string `json:"question"`
		Answer   *string `json:"answer"`
		Enabled  *bool   `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "bad_request", "请求体格式错误")
		return
	}
	fields := map[string]any{}
	if req.Question != nil {
		question := strings.TrimSpace(*req.Question)
		if question == "" || len([]rune(question)) > 2000 {
			s.fail(c, http.StatusBadRequest, "bad_request", "问题不能为空且不超过 2000 字")
			return
		}
		fields["question"] = question
	}
	if req.Answer != nil {
		answer := strings.TrimSpace(*req.Answer)
		if answer == "" || len([]rune(answer)) > 2000 {
			s.fail(c, http.StatusBadRequest, "bad_request", "答案不能为空且不超过 2000 字")
			return
		}
		fields["answer"] = answer
	}
	if req.Enabled != nil {
		fields["enabled"] = *req.Enabled
	}
	if err := s.Store.AI.KnowledgeUpdate(id, fields); err != nil {
		s.failStore(c, err, "ai.knowledge.update")
		return
	}
	s.audit(c, "ai.knowledge.update", strconv.FormatUint(uint64(id), 10), "更新知识条目")
	item, err := s.Store.AI.KnowledgeByID(id)
	if err != nil {
		s.failStore(c, err, "ai.knowledge.get")
		return
	}
	c.JSON(http.StatusOK, item)
}

// handleAIKnowledgeDelete 删除知识条目（管理员）。
func (s *Server) handleAIKnowledgeDelete(c *gin.Context) {
	id := pathID(c)
	if id == 0 {
		s.fail(c, http.StatusBadRequest, "bad_request", "ID 不合法")
		return
	}
	if err := s.Store.AI.KnowledgeDelete(id); err != nil {
		s.failStore(c, err, "ai.knowledge.delete")
		return
	}
	s.audit(c, "ai.knowledge.delete", strconv.FormatUint(uint64(id), 10), "删除知识条目")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleAIKnowledgeSearch 检索测试（管理员）：预览当前检索命中的条目。
func (s *Server) handleAIKnowledgeSearch(c *gin.Context) {
	if s.AI == nil {
		s.fail(c, http.StatusServiceUnavailable, "ai_unavailable", "AI 服务未启用")
		return
	}
	var req struct {
		Query string `json:"query"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Query) == "" {
		s.fail(c, http.StatusBadRequest, "bad_request", "需要提供检索关键词")
		return
	}
	hits, err := s.AI.SearchKnowledge(c.Request.Context(), strings.TrimSpace(req.Query))
	if err != nil {
		s.failInternal(c, err, "ai.knowledge.search")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": hits})
}

// ---------------------------------------------------------------------------
// 工单级 AI 操作（客服及以上）
// ---------------------------------------------------------------------------

// handleTicketAISummary 触发工单摘要生成（异步，结果经 SSE / 详情刷新呈现）。
func (s *Server) handleTicketAISummary(c *gin.Context) {
	if s.AI == nil {
		s.fail(c, http.StatusServiceUnavailable, "ai_unavailable", "AI 服务未启用")
		return
	}
	no := c.Param("no")
	if _, err := s.Tickets.Get(no); err != nil {
		s.failStore(c, err, "ticket.ai.summary")
		return
	}
	if err := s.AI.TriggerSummary(no); err != nil {
		if errors.Is(err, ai.ErrDisabled) {
			s.fail(c, http.StatusBadRequest, "ai_disabled", "AI 未启用或未配置")
			return
		}
		s.fail(c, http.StatusConflict, "ai_busy", "AI 任务队列繁忙，请稍后重试")
		return
	}
	s.audit(c, "ticket.ai.summary", no, "触发 AI 摘要生成")
	c.JSON(http.StatusAccepted, gin.H{"ok": true})
}

// handleTicketAIClassify 触发工单分类建议（异步）。
func (s *Server) handleTicketAIClassify(c *gin.Context) {
	if s.AI == nil {
		s.fail(c, http.StatusServiceUnavailable, "ai_unavailable", "AI 服务未启用")
		return
	}
	no := c.Param("no")
	if _, err := s.Tickets.Get(no); err != nil {
		s.failStore(c, err, "ticket.ai.classify")
		return
	}
	if err := s.AI.TriggerClassification(no); err != nil {
		if errors.Is(err, ai.ErrDisabled) {
			s.fail(c, http.StatusBadRequest, "ai_disabled", "AI 未启用或未配置")
			return
		}
		s.fail(c, http.StatusConflict, "ai_busy", "AI 任务队列繁忙，请稍后重试")
		return
	}
	s.audit(c, "ticket.ai.classify", no, "触发 AI 分类建议")
	c.JSON(http.StatusAccepted, gin.H{"ok": true})
}

// handleTicketAIClassifyApply 应用分类建议（同步修改工单类型标签）。
func (s *Server) handleTicketAIClassifyApply(c *gin.Context) {
	if s.AI == nil {
		s.fail(c, http.StatusServiceUnavailable, "ai_unavailable", "AI 服务未启用")
		return
	}
	no := c.Param("no")
	var req struct {
		Type string `json:"type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Type) == "" {
		s.fail(c, http.StatusBadRequest, "bad_request", "需要提供要应用的类型名")
		return
	}
	if err := s.AI.ApplyClassification(no, strings.TrimSpace(req.Type)); err != nil {
		if errors.Is(err, ai.ErrInvalidAIState) {
			s.fail(c, http.StatusConflict, "invalid_state", err.Error())
			return
		}
		s.failStore(c, err, "ticket.ai.classify.apply")
		return
	}
	s.audit(c, "ticket.ai.classify.apply", no, "应用 AI 分类建议："+req.Type)
	ticket, err := s.Tickets.Get(no)
	if err != nil {
		s.failStore(c, err, "ticket.get")
		return
	}
	c.JSON(http.StatusOK, ticket)
}

// handleTicketAIToggle 暂停或恢复某工单的 AI 自动应答。
func (s *Server) handleTicketAIToggle(c *gin.Context) {
	if s.AI == nil {
		s.fail(c, http.StatusServiceUnavailable, "ai_unavailable", "AI 服务未启用")
		return
	}
	no := c.Param("no")
	var req struct {
		Enable *bool `json:"enable"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enable == nil {
		s.fail(c, http.StatusBadRequest, "bad_request", "需要提供 enable 字段")
		return
	}
	if err := s.AI.ToggleTicket(no, *req.Enable); err != nil {
		if errors.Is(err, ai.ErrInvalidAIState) {
			s.fail(c, http.StatusConflict, "invalid_state", err.Error())
			return
		}
		s.failStore(c, err, "ticket.ai.toggle")
		return
	}
	action := "暂停"
	if *req.Enable {
		action = "恢复"
	}
	s.audit(c, "ticket.ai.toggle", no, action+" AI 自动应答")
	ticket, err := s.Tickets.Get(no)
	if err != nil {
		s.failStore(c, err, "ticket.get")
		return
	}
	c.JSON(http.StatusOK, ticket)
}
