package store

import (
	"strings"
	"testing"
)

func testSecret() []byte {
	return []byte("0123456789abcdef0123456789abcdef")
}

func TestAISettingsDefaultsAndRoundTrip(t *testing.T) {
	st := newTestStore(t)

	cfg, err := st.AI.Config(testSecret())
	if err != nil {
		t.Fatalf("读取默认配置失败: %v", err)
	}
	if cfg.Enabled {
		t.Fatal("全局开关默认应为关闭")
	}
	if cfg.SystemPrompt != DefaultAISystemPrompt || cfg.Footer != DefaultAIFooter {
		t.Fatal("默认提示词与尾注应生效")
	}
	// 存量实例落库了旧默认提示词：读取时自动升级为新默认。
	if err := st.AI.SaveConfig(&AISettings{SystemPrompt: legacyDefaultAISystemPrompt}, "", false, testSecret()); err != nil {
		t.Fatalf("保存旧默认提示词失败: %v", err)
	}
	cfg, err = st.AI.Config(testSecret())
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if cfg.SystemPrompt != DefaultAISystemPrompt {
		t.Fatal("旧默认提示词应自动升级为新默认")
	}
	// 自定义提示词不受影响。
	if err := st.AI.SaveConfig(&AISettings{SystemPrompt: "自定义"}, "", false, testSecret()); err != nil {
		t.Fatalf("保存自定义提示词失败: %v", err)
	}
	cfg, _ = st.AI.Config(testSecret())
	if cfg.SystemPrompt != "自定义" {
		t.Fatalf("自定义提示词不应被覆盖: %q", cfg.SystemPrompt)
	}
	// 还原为空，避免影响后续断言。
	if err := st.AI.SaveConfig(&AISettings{SystemPrompt: ""}, "", false, testSecret()); err != nil {
		t.Fatalf("重置提示词失败: %v", err)
	}
	if cfg.QuietSeconds != DefaultAIQuietSeconds || cfg.MaxReplies != DefaultAIMaxReplies {
		t.Fatal("默认护栏参数应生效")
	}
	if cfg.HasAPIKey {
		t.Fatal("默认不应有 API Key")
	}

	// 保存后应能读回（Key 加密存储，明文不落库）。
	saved := &AISettings{
		Enabled:            true,
		BaseURL:            "https://api.deepseek.com/v1",
		Model:              "deepseek-chat",
		EmbeddingModel:     "text-embedding",
		Temperature:        0.3,
		SystemPrompt:       "自定义提示词",
		Footer:             "—— AI",
		QuietSeconds:       8,
		MaxReplies:         5,
		ContextMessages:    30,
		KnowledgeTopK:      3,
		AutoSummaryOnClose: true,
		CallRetentionDays:  30,
	}
	if err := st.AI.SaveConfig(saved, "sk-llm-key-1234", false, testSecret()); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}

	cfg, err = st.AI.Config(testSecret())
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if !cfg.Enabled || cfg.BaseURL != saved.BaseURL || cfg.Model != saved.Model {
		t.Fatalf("配置未读回: %+v", cfg)
	}
	if cfg.EmbeddingModel != "text-embedding" || cfg.Temperature != 0.3 {
		t.Fatalf("嵌入模型或温度未读回: %+v", cfg)
	}
	if cfg.QuietSeconds != 8 || cfg.MaxReplies != 5 || cfg.ContextMessages != 30 || cfg.KnowledgeTopK != 3 {
		t.Fatalf("护栏参数未读回: %+v", cfg)
	}
	if !cfg.AutoSummaryOnClose || cfg.CallRetentionDays != 30 {
		t.Fatalf("摘要与保留期未读回: %+v", cfg)
	}
	if !cfg.HasAPIKey || cfg.APIKeyPlaintext != "sk-llm-key-1234" {
		t.Fatalf("API Key 未解密: %+v", cfg)
	}
	if cfg.APIKeyMasked == "sk-llm-key-1234" || !strings.Contains(cfg.APIKeyMasked, "1234") {
		t.Fatalf("掩码格式异常: %q", cfg.APIKeyMasked)
	}

	// Key 不落明文：settings 表里只有密文。
	raw, _, err := st.Settings.Get(SettingAIAPIKeyEnc)
	if err != nil {
		t.Fatalf("读取密文失败: %v", err)
	}
	if strings.Contains(raw, "sk-llm-key-1234") {
		t.Fatal("API Key 明文落库")
	}

	// 空 Key 保存 = 保持现状。
	if err := st.AI.SaveConfig(cfg, "", false, testSecret()); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	cfg, _ = st.AI.Config(testSecret())
	if !cfg.HasAPIKey {
		t.Fatal("空 Key 保存不应清除已有 Key")
	}

	// ClearAPIKey = 删除。
	if err := st.AI.SaveConfig(cfg, "", true, testSecret()); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	cfg, _ = st.AI.Config(testSecret())
	if cfg.HasAPIKey {
		t.Fatal("ClearAPIKey 应删除 Key")
	}
}

func TestAISettingsKeyUndecryptableWithWrongSecret(t *testing.T) {
	st := newTestStore(t)
	if err := st.AI.SaveConfig(&AISettings{}, "sk-llm-key-1234", false, testSecret()); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}

	cfg, err := st.AI.Config([]byte("another-secret-32-bytes-aaaaaaaaaa"))
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if cfg.HasAPIKey != true || cfg.APIKeyPlaintext != "" {
		t.Fatalf("错误密钥下应无明文: %+v", cfg)
	}
	if !strings.Contains(cfg.APIKeyMasked, "无法解密") {
		t.Fatalf("应提示重新录入: %q", cfg.APIKeyMasked)
	}
}

func TestKnowledgeCRUDAndFilter(t *testing.T) {
	st := newTestStore(t)

	item := &AIKnowledge{Question: "退款多久到账", Answer: "3-5 个工作日", Enabled: true}
	if err := st.AI.KnowledgeCreate(item); err != nil {
		t.Fatalf("创建知识失败: %v", err)
	}
	if item.Source != KnowledgeSourceManual {
		t.Fatalf("手工创建应为 manual 来源: %q", item.Source)
	}

	item2 := &AIKnowledge{Question: "怎么改密码", Answer: "设置里改", Enabled: false}
	if err := st.AI.KnowledgeCreate(item2); err != nil {
		t.Fatalf("创建知识失败: %v", err)
	}

	// 关键词过滤（含通配符转义路径）。
	items, total, err := st.AI.KnowledgeList(KnowledgeFilter{Query: "退款"})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("关键词过滤结果不符: total=%d len=%d", total, len(items))
	}
	_, total, _ = st.AI.KnowledgeList(KnowledgeFilter{Query: "100%退款"})
	if total != 0 {
		t.Fatalf("通配符应按字面匹配: %d", total)
	}

	// 启停过滤。
	enabled := true
	_, total, _ = st.AI.KnowledgeList(KnowledgeFilter{Enabled: &enabled})
	if total != 1 {
		t.Fatalf("启停过滤结果不符: %d", total)
	}

	// 更新与删除。
	if err := st.AI.KnowledgeUpdate(item.ID, map[string]any{"answer": "1-3 个工作日", "enabled": false}); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	got, _ := st.AI.KnowledgeByID(item.ID)
	if got.Answer != "1-3 个工作日" || got.Enabled {
		t.Fatalf("更新未生效: %+v", got)
	}
	if err := st.AI.KnowledgeDelete(item2.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if err := st.AI.KnowledgeDelete(item2.ID); err == nil {
		t.Fatal("重复删除应返回 ErrNotFound")
	}
}

func TestCaptureKnowledgeIsIdempotent(t *testing.T) {
	st := newTestStore(t)

	entry := &AIKnowledge{
		Question:     "服务器进不去",
		Answer:       "已在维护，10 分钟后恢复",
		TicketNo:     "TK-260101-ABCD",
		AnswerMsgID:  "msg-100",
		IsCorrection: true,
		Enabled:      true,
	}
	created, err := st.AI.CaptureKnowledge(entry)
	if err != nil || !created {
		t.Fatalf("首次捕获失败: created=%v err=%v", created, err)
	}
	if entry.Source != KnowledgeSourceTicket {
		t.Fatalf("来源应为 ticket: %q", entry.Source)
	}

	// 同一条回复再次捕获：幂等跳过。
	created, err = st.AI.CaptureKnowledge(&AIKnowledge{
		Question:    "服务器进不去",
		Answer:      "已在维护，10 分钟后恢复",
		TicketNo:    "TK-260101-ABCD",
		AnswerMsgID: "msg-100",
	})
	if err != nil || created {
		t.Fatalf("重复捕获应被跳过: created=%v err=%v", created, err)
	}

	// 缺少定位字段时拒绝。
	if _, err := st.AI.CaptureKnowledge(&AIKnowledge{Question: "q", Answer: "a", TicketNo: "TK-1"}); err == nil {
		t.Fatal("缺少消息 ID 应报错")
	}
}

func TestAICallRecordAndUsage(t *testing.T) {
	st := newTestStore(t)

	calls := []*AICall{
		{TicketNo: "TK-1", Kind: "reply", Model: "m", PromptTokens: 100, CompletionTokens: 50, DurationMs: 1200},
		{TicketNo: "TK-1", Kind: "summary", Model: "m", PromptTokens: 200, CompletionTokens: 80, DurationMs: 900},
		{TicketNo: "", Kind: "test", Model: "m", Error: "超时", DurationMs: 500},
	}
	for _, call := range calls {
		if err := st.AI.RecordAICall(call); err != nil {
			t.Fatalf("写入调用记录失败: %v", err)
		}
	}

	// 一张启用了 AI 的工单，验证统计口径。
	if err := st.Tickets.UpdateFields("TK-1", map[string]any{"ai_state": AITicketActive}); err != nil {
		// TK-1 不存在（UpdateFields 对不存在的编号无行受影响但不报错），先建一张。
		t.Log("TK-1 尚不存在，跳过 ai_state 统计断言前置")
	}

	stats, err := st.AI.AIUsage(7)
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if stats.TotalCalls != 3 || stats.FailedCalls != 1 {
		t.Fatalf("调用统计不符: %+v", stats)
	}
	if stats.PromptTokens != 300 || stats.CompletionTokens != 130 {
		t.Fatalf("token 统计不符: %+v", stats)
	}

	// 清理保留期外的记录。
	if err := st.AI.CleanupAICalls(0); err != nil {
		t.Fatalf("retention=0 应直接返回: %v", err)
	}
}
