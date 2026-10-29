package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/secure"
)

// AI 相关的 settings 键名。
const (
	// SettingAIAPIKeyEnc 保存 AES-GCM 加密后的 LLM API Key，明文不落库。
	SettingAIAPIKeyEnc = "ai_api_key_enc"
	// SettingAIEnabled 是 AI 能力的全局总开关（默认关闭）。
	SettingAIEnabled = "ai_enabled"
	// SettingAIBaseURL / SettingAIModel / SettingAIEmbeddingModel 描述 OpenAI 兼容服务。
	SettingAIBaseURL         = "ai_base_url"
	SettingAIModel           = "ai_model"
	SettingAIEmbeddingModel  = "ai_embedding_model"
	SettingAITemperature     = "ai_temperature"
	SettingAISystemPrompt    = "ai_system_prompt"
	SettingAIFooter          = "ai_footer"
	SettingAIQuietSeconds    = "ai_quiet_seconds"
	SettingAIMaxReplies      = "ai_max_replies"
	SettingAIContextMessages = "ai_context_messages"
	SettingAIKnowledgeTopK   = "ai_knowledge_topk"
	// SettingAIAutoSummaryOnClose 控制关单时是否自动生成 AI 摘要。
	SettingAIAutoSummaryOnClose = "ai_auto_summary_on_close"
	// SettingAICallRetentionDays 是 AI 调用记录的保留天数。
	SettingAICallRetentionDays = "ai_call_retention_days"
)

// AI 配置的默认值。
//
// 全部默认关闭 / 保守取值：AI 是增强能力，升级后不改变现有工单流程的行为，
// 管理员在 WebUI 显式开启后才生效。
const (
	DefaultAIQuietSeconds    = 8
	DefaultAIMaxReplies      = 10
	DefaultAIContextMessages = 20
	DefaultAIKnowledgeTopK   = 5
	DefaultAICallRetention   = 90
	DefaultAITemperature     = 0.4
)

// DefaultAISystemPrompt 是自动应答的系统提示词模板。
//
// 支持占位符：{type_name} 工单类型、{ticket_no} 工单编号、{user_name} 开单人昵称。
// 知识示例与输出协议（action / JSON 契约）由服务层拼接后追加，
// 不通过模板注入（避免管理员模板破坏结构）。
const DefaultAISystemPrompt = `你是工单 {ticket_no}（类型：{type_name}）的客服助手，正在回复用户 {user_name}。
要求：
- 用与用户相同的语言回复，语气礼貌、直接了当；
- 只依据对话内容与提供的参考示例回答，严禁编造政策、时间、金额或承诺；
- 参考示例与用户当前问题不相关时必须忽略，不得套用；
- 回复控制在 300 字以内。`

// legacyDefaultAISystemPrompt 是上一版默认提示词。
//
// 管理员在 WebUI 保存设置时会把当时的默认值整体落库，导致后续修改常量
// 对存量实例不生效；Config 读取时把仍是旧默认的值视为「未自定义」，
// 自动升级到新默认（真正自定义过的提示词不受影响）。
const legacyDefaultAISystemPrompt = `你是工单 {ticket_no}（类型：{type_name}）的客服助手，正在回复用户 {user_name}。
要求：
- 用与用户相同的语言回复，语气礼貌、直接了当；
- 只依据对话内容与提供的参考示例回答，不要编造政策、时间或承诺；
- 无法确定的问题如实说明，并建议等待人工客服；
- 回复控制在 300 字以内。`

// DefaultAIFooter 是 AI 回复末尾追加的标识（KMarkdown），让用户明确知道这是自动回复。
const DefaultAIFooter = "—— AI 自动回复，人工客服稍后接管"

// AISettings 是 AI 能力的全部配置快照。
type AISettings struct {
	Enabled            bool
	BaseURL            string
	Model              string
	EmbeddingModel     string
	Temperature        float64
	SystemPrompt       string
	Footer             string
	QuietSeconds       int
	MaxReplies         int
	ContextMessages    int
	KnowledgeTopK      int
	AutoSummaryOnClose bool
	CallRetentionDays  int

	// HasAPIKey / APIKeyMasked 供界面展示；明文 Key 不出现在任何读取路径。
	HasAPIKey    bool
	APIKeyMasked string
	// APIKeyPlaintext 仅在内部装配 LLM 客户端时解密填充，不序列化、不下发。
	APIKeyPlaintext string `json:"-"`
}

// AIRepo 负责 AI 配置、知识库与调用记录的读写。
type AIRepo struct {
	db       *gorm.DB
	settings *SettingsRepo
}

// NewAIRepo 创建 AI 仓储。
//
// appSecret 按调用传入（对齐 SettingsRepo.GetSecret 的风格），
// 便于测试与多实例各自提供密钥。
func NewAIRepo(db *gorm.DB, settings *SettingsRepo) *AIRepo {
	return &AIRepo{db: db, settings: settings}
}

// Config 加载 AI 配置快照（含解密后的 API Key，仅供服务层装配客户端）。
func (r *AIRepo) Config(appSecret []byte) (*AISettings, error) {
	all, err := r.settings.All()
	if err != nil {
		return nil, err
	}
	cfg := &AISettings{
		Enabled:            all[SettingAIEnabled] == "true",
		BaseURL:            strings.TrimSpace(all[SettingAIBaseURL]),
		Model:              strings.TrimSpace(all[SettingAIModel]),
		EmbeddingModel:     strings.TrimSpace(all[SettingAIEmbeddingModel]),
		Temperature:        parseFloatOr(all[SettingAITemperature], DefaultAITemperature),
		SystemPrompt:       all[SettingAISystemPrompt],
		Footer:             all[SettingAIFooter],
		QuietSeconds:       parseIntOr(all[SettingAIQuietSeconds], DefaultAIQuietSeconds),
		MaxReplies:         parseIntOr(all[SettingAIMaxReplies], DefaultAIMaxReplies),
		ContextMessages:    parseIntOr(all[SettingAIContextMessages], DefaultAIContextMessages),
		KnowledgeTopK:      parseIntOr(all[SettingAIKnowledgeTopK], DefaultAIKnowledgeTopK),
		AutoSummaryOnClose: all[SettingAIAutoSummaryOnClose] == "true",
		CallRetentionDays:  parseIntOr(all[SettingAICallRetentionDays], DefaultAICallRetention),
	}
	if cfg.SystemPrompt == "" || cfg.SystemPrompt == legacyDefaultAISystemPrompt {
		cfg.SystemPrompt = DefaultAISystemPrompt
	}
	if cfg.Footer == "" {
		cfg.Footer = DefaultAIFooter
	}
	if cfg.QuietSeconds <= 0 {
		cfg.QuietSeconds = DefaultAIQuietSeconds
	}
	if cfg.MaxReplies <= 0 {
		cfg.MaxReplies = DefaultAIMaxReplies
	}
	if cfg.ContextMessages <= 0 {
		cfg.ContextMessages = DefaultAIContextMessages
	}
	if cfg.KnowledgeTopK <= 0 {
		cfg.KnowledgeTopK = DefaultAIKnowledgeTopK
	}
	if cfg.CallRetentionDays <= 0 {
		cfg.CallRetentionDays = DefaultAICallRetention
	}

	if encrypted, ok := all[SettingAIAPIKeyEnc]; ok && encrypted != "" {
		cfg.HasAPIKey = true
		if key, err := secure.Decrypt(appSecret, encrypted); err == nil {
			cfg.APIKeyPlaintext = key
			cfg.APIKeyMasked = secure.MaskTail(key)
		} else {
			// 解密失败通常意味着 APP_SECRET 变更，界面需要提示重新录入。
			cfg.APIKeyMasked = "(无法解密，请重新填写)"
		}
	}
	return cfg, nil
}

// SaveConfig 保存 AI 配置。
//
// APIKey 语义：非空 = 写入新 Key（加密）；空 = 保持现状；ClearAPIKey = true = 删除。
// 全部键在单个事务内写入，避免半套配置。
func (r *AIRepo) SaveConfig(cfg *AISettings, apiKey string, clearAPIKey bool, appSecret []byte) error {
	change := SettingChange{Upserts: map[string]string{
		SettingAIEnabled:            boolStr(cfg.Enabled),
		SettingAIBaseURL:            strings.TrimSpace(cfg.BaseURL),
		SettingAIModel:              strings.TrimSpace(cfg.Model),
		SettingAIEmbeddingModel:     strings.TrimSpace(cfg.EmbeddingModel),
		SettingAITemperature:        strconv.FormatFloat(cfg.Temperature, 'f', -1, 64),
		SettingAISystemPrompt:       cfg.SystemPrompt,
		SettingAIFooter:             cfg.Footer,
		SettingAIQuietSeconds:       strconv.Itoa(cfg.QuietSeconds),
		SettingAIMaxReplies:         strconv.Itoa(cfg.MaxReplies),
		SettingAIContextMessages:    strconv.Itoa(cfg.ContextMessages),
		SettingAIKnowledgeTopK:      strconv.Itoa(cfg.KnowledgeTopK),
		SettingAIAutoSummaryOnClose: boolStr(cfg.AutoSummaryOnClose),
		SettingAICallRetentionDays:  strconv.Itoa(cfg.CallRetentionDays),
	}}
	if err := r.settings.Apply(change); err != nil {
		return err
	}
	if clearAPIKey {
		return r.settings.Delete(SettingAIAPIKeyEnc)
	}
	if strings.TrimSpace(apiKey) != "" {
		return r.settings.SetSecret(SettingAIAPIKeyEnc, strings.TrimSpace(apiKey), appSecret)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 知识库
// ---------------------------------------------------------------------------

// KnowledgeFilter 是知识库列表查询条件。
type KnowledgeFilter struct {
	Query  string
	Source string
	// Enabled 为 nil 时不按启停过滤。
	Enabled *bool
	Page    int
	// PageSize 会被限制在 MaxPageSize 以内。
	PageSize int
}

// KnowledgeList 按条件分页查询知识条目，返回结果与总数。
func (r *AIRepo) KnowledgeList(f KnowledgeFilter) ([]AIKnowledge, int64, error) {
	q := r.db.Model(&AIKnowledge{})
	if term := strings.TrimSpace(f.Query); term != "" {
		contains := "%" + escapeLike(term) + "%"
		q = q.Where("question LIKE ? ESCAPE '\\' OR answer LIKE ? ESCAPE '\\'", contains, contains)
	}
	if f.Source != "" {
		q = q.Where("source = ?", f.Source)
	}
	if f.Enabled != nil {
		q = q.Where("enabled = ?", *f.Enabled)
	}

	page := f.Page
	if page < 1 {
		page = 1
	}
	size := NormalizePageSize(f.PageSize)

	var items []AIKnowledge
	err := q.Session(&gorm.Session{}).Order("created_at DESC, id DESC").
		Offset((page - 1) * size).Limit(size).Find(&items).Error
	if err != nil {
		return nil, 0, err
	}
	if page == 1 && len(items) < size {
		return items, int64(len(items)), nil
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// KnowledgeByID 按主键查询。
func (r *AIRepo) KnowledgeByID(id uint) (*AIKnowledge, error) {
	var item AIKnowledge
	if err := r.db.Where("id = ?", id).First(&item).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &item, nil
}

// KnowledgeCreate 新增知识条目（manual 来源）。
func (r *AIRepo) KnowledgeCreate(item *AIKnowledge) error {
	item.Source = KnowledgeSourceManual
	item.CreatedAt, item.UpdatedAt = Now(), Now()
	return r.db.Create(item).Error
}

// KnowledgeUpdate 更新知识条目（问题、答案、启停）。
func (r *AIRepo) KnowledgeUpdate(id uint, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	fields["updated_at"] = Now()
	result := r.db.Model(&AIKnowledge{}).Where("id = ?", id).Updates(fields)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// KnowledgeDelete 删除知识条目。
func (r *AIRepo) KnowledgeDelete(id uint) error {
	result := r.db.Where("id = ?", id).Delete(&AIKnowledge{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// CaptureKnowledge 幂等地捕获一条来自工单的知识。
//
// 幂等实现是事务内「先查后插」：本包所有写事务都用 BEGIN IMMEDIATE
// （见 sqliteDSN），同一时刻只有一个写事务在推进，检查与插入之间
// 不会被并发插入穿透。manual 条目不走这里（AnswerMsgID 为空时直接拒绝）。
// 返回 created=false 表示同一条回复已捕获过。
func (r *AIRepo) CaptureKnowledge(item *AIKnowledge) (bool, error) {
	item.Source = KnowledgeSourceTicket
	if item.TicketNo == "" || item.AnswerMsgID == "" {
		return false, errors.New("捕获知识需要工单编号与来源消息 ID")
	}
	var created bool
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&AIKnowledge{}).
			Where("ticket_no = ? AND answer_msg_id = ?", item.TicketNo, item.AnswerMsgID).
			Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		item.CreatedAt, item.UpdatedAt = Now(), Now()
		created = true
		return tx.Create(item).Error
	})
	return created, err
}

// KnowledgeEnabled 返回全部启用中的知识条目（检索用，条目量级小可全量加载）。
func (r *AIRepo) KnowledgeEnabled() ([]AIKnowledge, error) {
	var items []AIKnowledge
	err := r.db.Where("enabled = ?", true).Order("created_at ASC").Find(&items).Error
	return items, err
}

// KnowledgeSetEmbedding 回填知识条目的向量。
func (r *AIRepo) KnowledgeSetEmbedding(id uint, embedding []float32) error {
	encoded, err := json.Marshal(embedding)
	if err != nil {
		return fmt.Errorf("序列化向量失败: %w", err)
	}
	return r.db.Model(&AIKnowledge{}).Where("id = ?", id).
		Updates(map[string]any{"embedding": string(encoded), "updated_at": Now()}).Error
}

// ---------------------------------------------------------------------------
// 调用记录
// ---------------------------------------------------------------------------

// RecordAICall 写入一次 LLM 调用记录（尽力而为，失败只影响统计不影响主流程）。
func (r *AIRepo) RecordAICall(call *AICall) error {
	call.CreatedAt = Now()
	return r.db.Create(call).Error
}

// AIUsageStats 汇总近 days 天的调用情况，供设置页展示。
type AIUsageStats struct {
	Days               int   `json:"days"`
	TotalCalls         int64 `json:"totalCalls"`
	FailedCalls        int64 `json:"failedCalls"`
	PromptTokens       int64 `json:"promptTokens"`
	CompletionTokens   int64 `json:"completionTokens"`
	KnowledgeEntries   int64 `json:"knowledgeEntries"`
	AutoRepliedTickets int64 `json:"autoRepliedTickets"`
}

// AIUsage 汇总调用统计与知识库规模。
func (r *AIRepo) AIUsage(days int) (*AIUsageStats, error) {
	if days <= 0 {
		days = 7
	}
	cutoff := Now().AddDate(0, 0, -days)
	stats := &AIUsageStats{Days: days}

	if err := r.db.Model(&AICall{}).
		Where("created_at >= ?", cutoff).
		Select("COUNT(*)").Scan(&stats.TotalCalls).Error; err != nil {
		return nil, err
	}
	if err := r.db.Model(&AICall{}).
		Where("created_at >= ? AND error <> ''", cutoff).
		Select("COUNT(*)").Scan(&stats.FailedCalls).Error; err != nil {
		return nil, err
	}
	type tokenRow struct {
		Prompt     int64
		Completion int64
	}
	var tokens tokenRow
	if err := r.db.Model(&AICall{}).
		Where("created_at >= ?", cutoff).
		Select("COALESCE(SUM(prompt_tokens),0) AS prompt, COALESCE(SUM(completion_tokens),0) AS completion").
		Scan(&tokens).Error; err != nil {
		return nil, err
	}
	stats.PromptTokens, stats.CompletionTokens = tokens.Prompt, tokens.Completion

	if err := r.db.Model(&AIKnowledge{}).
		Select("COUNT(*)").Scan(&stats.KnowledgeEntries).Error; err != nil {
		return nil, err
	}
	if err := r.db.Model(&Ticket{}).
		Where("ai_state <> ''").
		Select("COUNT(*)").Scan(&stats.AutoRepliedTickets).Error; err != nil {
		return nil, err
	}
	return stats, nil
}

// CleanupAICalls 删除超过保留期的调用记录；retention 为 0 表示永久保留。
// 由后台维护任务周期调用（对齐审计日志的清理模式）。
func (r *AIRepo) CleanupAICalls(retention time.Duration) error {
	if retention <= 0 {
		return nil
	}
	cutoff := Now().Add(-retention)
	return r.db.Where("created_at < ?", cutoff).Delete(&AICall{}).Error
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

func boolStr(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func parseIntOr(raw string, def int) int {
	if raw == "" {
		return def
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return def
	}
	return parsed
}

func parseFloatOr(raw string, def float64) float64 {
	if raw == "" {
		return def
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil || parsed <= 0 || parsed > 2 {
		return def
	}
	return parsed
}
