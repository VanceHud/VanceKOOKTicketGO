package ai

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/llm"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// fakeClient 记录调用并返回预设结果。
type fakeClient struct {
	mu        sync.Mutex
	chats     []llm.ChatRequest
	embeds    [][]string
	chatFn    func(req llm.ChatRequest) (string, error)
	embedFn   func(texts []string) ([][]float32, error)
	embedCall int
}

func (f *fakeClient) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.mu.Lock()
	f.chats = append(f.chats, req)
	fn := f.chatFn
	f.mu.Unlock()
	if fn == nil {
		return &llm.ChatResponse{Content: "默认回复"}, nil
	}
	content, err := fn(req)
	if err != nil {
		return nil, err
	}
	return &llm.ChatResponse{Content: content, PromptTokens: 10, CompletionTokens: 5}, nil
}

func (f *fakeClient) Embed(ctx context.Context, texts []string, model string) ([][]float32, error) {
	f.mu.Lock()
	f.embeds = append(f.embeds, texts)
	fn := f.embedFn
	f.mu.Unlock()
	if fn != nil {
		return fn(texts)
	}
	// 默认：所有文本返回相同向量（相似度恒为 1）。
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0, 0}
	}
	return out, nil
}

// fakeSender 记录发送的回复。
type fakeSender struct {
	mu      sync.Mutex
	replies []string
	fail    bool
}

func (f *fakeSender) SendAIReply(ctx context.Context, t *store.Ticket, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("发送失败")
	}
	f.replies = append(f.replies, content)
	return nil
}

func (f *fakeSender) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.replies...)
}

// newTestService 构造带假客户端的服务与启用 AI 的配置。
func newTestService(t *testing.T, client *fakeClient, sender *fakeSender) (*Service, *store.Store) {
	t.Helper()
	st := newAIStore(t)
	if err := st.AI.SaveConfig(&store.AISettings{
		Enabled:         true,
		BaseURL:         "https://fake",
		Model:           "fake-model",
		SystemPrompt:    "你是客服",
		Footer:          "—— AI",
		QuietSeconds:    1,
		MaxReplies:      10,
		ContextMessages: 20,
		KnowledgeTopK:   5,
	}, "fake-key", false, testAISecret()); err != nil {
		t.Fatalf("保存 AI 配置失败: %v", err)
	}
	svc := New(Deps{
		Store:     st,
		Logger:    testLogger(),
		AppSecret: testAISecret(),
		ClientFactory: func(cfg *store.AISettings) (LLMClient, error) {
			return client, nil
		},
	})
	svc.SetSender(sender)
	svc.Start()
	t.Cleanup(svc.Stop)
	return svc, st
}

func newAIStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := t.TempDir() + "/ticket.db"
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func testAISecret() []byte {
	return []byte("0123456789abcdef0123456789abcdef")
}

// testLogger 输出到 io.Discard，保持测试输出干净。
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// seedTicket 建一张启用 AI 的工单并返回。
func seedTicket(t *testing.T, st *store.Store, no string, aiAutoReply bool) (*store.Ticket, *store.TicketType) {
	t.Helper()
	typeItem := &store.TicketType{Name: "咨询", Enabled: true, AIAutoReply: aiAutoReply}
	if err := st.Types.Create(typeItem); err != nil {
		t.Fatalf("创建类型失败: %v", err)
	}
	ticket := &store.Ticket{
		No: no, UserID: "u1", UserName: "提问者",
		TypeID: &typeItem.ID, TypeName: typeItem.Name,
		Status: store.TicketOpen, StartedAt: store.Now(),
		ChannelID: "chan-" + no,
	}
	if err := st.Tickets.CreateWithNo(ticket, store.Now(), time.UTC); err != nil {
		// 编号冲突（固定编号重复建）：直接更新状态。
		if err := st.Tickets.UpdateFields(no, map[string]any{"status": store.TicketOpen}); err != nil {
			t.Fatalf("准备工单失败: %v", err)
		}
		got, gerr := st.Tickets.ByNo(no)
		if gerr != nil {
			t.Fatalf("读取工单失败: %v", gerr)
		}
		return got, typeItem
	}
	return ticket, typeItem
}

func addMessage(t *testing.T, st *store.Store, no, userID, content string, isBot, isAI bool) *store.TicketMessage {
	t.Helper()
	msg := &store.TicketMessage{
		TicketNo: no, ChannelID: "chan-" + no, UserID: userID, UserName: userID,
		Content: content, MsgType: store.MsgTypeText, IsBot: isBot, IsAI: isAI,
		CreatedAt: store.Now(),
	}
	if err := st.Tickets.AddMessage(msg); err != nil {
		t.Fatalf("写入消息失败: %v", err)
	}
	return msg
}

// waitFor 轮询断言（异步任务的时序不可控）。
func waitFor(t *testing.T, timeout time.Duration, check func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestAutoReplyFlow(t *testing.T) {
	client := &fakeClient{}
	sender := &fakeSender{}
	svc, st := newTestService(t, client, sender)

	ticket, _ := seedTicket(t, st, "TK-260101-TEST", true)
	svc.OnTicketOpened(context.Background(), ticket)

	// 开单人发消息 → 防抖（1s 静默）→ 生成 → 发送。
	msg := addMessage(t, st, ticket.No, "u1", "退款多久到账", false, false)
	svc.OnUserMessage(context.Background(), ticket, msg)

	if !waitFor(t, 5*time.Second, func() bool { return len(sender.sent()) == 1 }) {
		t.Fatal("等待 AI 回复超时")
	}
	if got := sender.sent()[0]; !strings.Contains(got, "默认回复") || !strings.Contains(got, "—— AI") {
		t.Fatalf("回复应含正文与尾注: %q", got)
	}

	// 请求里应包含系统提示词与用户消息。
	if len(client.chats) != 1 {
		t.Fatalf("应有 1 次 LLM 调用: %d", len(client.chats))
	}
	req := client.chats[0]
	if req.Messages[0].Role != "system" || !strings.Contains(req.Messages[0].Content, "你是客服") {
		t.Fatalf("系统提示词不符: %+v", req.Messages[0])
	}
	found := false
	for _, m := range req.Messages {
		if m.Role == "user" && strings.Contains(m.Content, "退款多久到账") {
			found = true
		}
	}
	if !found {
		t.Fatal("用户消息未进入上下文")
	}
}

func TestStaffReplyTakesOverAndCapturesKnowledge(t *testing.T) {
	client := &fakeClient{}
	sender := &fakeSender{}
	svc, st := newTestService(t, client, sender)

	ticket, _ := seedTicket(t, st, "TK-260102-TEST", true)
	svc.OnTicketOpened(context.Background(), ticket)

	// 用户提问后人工立刻回复：防抖被取消，AI 不再回复。
	userMsg := addMessage(t, st, ticket.No, "u1", "服务器进不去怎么办", false, false)
	svc.OnUserMessage(context.Background(), ticket, userMsg)
	staffMsg := addMessage(t, st, ticket.No, "u2", "正在维护，10 分钟后恢复", false, false)
	svc.OnStaffMessage(context.Background(), ticket, staffMsg, true)

	time.Sleep(1500 * time.Millisecond) // 超过防抖窗口
	if got := sender.sent(); len(got) != 0 {
		t.Fatalf("人工接管后 AI 不应回复: %v", got)
	}

	updated, _ := st.Tickets.ByNo(ticket.No)
	if updated.AIState != store.AITicketTakeover {
		t.Fatalf("应标记接管: %q", updated.AIState)
	}

	// 知识捕获：问题 + 人工回答入库。
	if !waitFor(t, 3*time.Second, func() bool {
		items, _, _ := st.AI.KnowledgeList(store.KnowledgeFilter{})
		return len(items) == 1
	}) {
		t.Fatal("等待知识捕获超时")
	}
	items, _, _ := st.AI.KnowledgeList(store.KnowledgeFilter{})
	if !strings.Contains(items[0].Question, "服务器进不去") || !strings.Contains(items[0].Answer, "维护") {
		t.Fatalf("知识内容不符: %+v", items[0])
	}
	if items[0].Source != store.KnowledgeSourceTicket || items[0].TicketNo != ticket.No {
		t.Fatalf("知识来源不符: %+v", items[0])
	}
}

func TestCorrectionCaptureAfterAIReply(t *testing.T) {
	client := &fakeClient{}
	sender := &fakeSender{}
	svc, st := newTestService(t, client, sender)

	ticket, _ := seedTicket(t, st, "TK-260103-TEST", true)
	svc.OnTicketOpened(context.Background(), ticket)

	// AI 先回复（直接写一条 AI 消息模拟），人工再纠正。
	addMessage(t, st, ticket.No, "u1", "退款多久到账", false, false)
	aiMsg := &store.TicketMessage{
		TicketNo: ticket.No, ChannelID: ticket.ChannelID, UserID: "ai", UserName: "AI 助手",
		Content: "大概 30 天", MsgType: store.MsgTypeText, IsBot: true, IsAI: true, CreatedAt: store.Now(),
	}
	if err := st.Tickets.AddMessage(aiMsg); err != nil {
		t.Fatalf("写入 AI 消息失败: %v", err)
	}
	staffMsg := addMessage(t, st, ticket.No, "u2", "抱歉，正确的是 3-5 个工作日", false, false)
	svc.OnStaffMessage(context.Background(), ticket, staffMsg, true)

	if !waitFor(t, 3*time.Second, func() bool {
		items, _, _ := st.AI.KnowledgeList(store.KnowledgeFilter{})
		return len(items) == 1 && items[0].IsCorrection
	}) {
		t.Fatal("等待纠正类知识捕获超时")
	}
}

func TestMaxRepliesGuard(t *testing.T) {
	client := &fakeClient{}
	sender := &fakeSender{}
	svc, st := newTestService(t, client, sender)

	ticket, _ := seedTicket(t, st, "TK-260104-TEST", true)
	// 手动把回复数顶到上限（MaxReplies=10）。
	for i := 0; i < 10; i++ {
		addMessage(t, st, ticket.No, "ai", "回复", true, true)
	}
	svc.OnTicketOpened(context.Background(), ticket)
	msg := addMessage(t, st, ticket.No, "u1", "还在吗", false, false)
	svc.OnUserMessage(context.Background(), ticket, msg)

	time.Sleep(1500 * time.Millisecond)
	if got := sender.sent(); len(got) != 0 {
		t.Fatalf("达到上限后不应再回复: %v", got)
	}
	updated, _ := st.Tickets.ByNo(ticket.No)
	if updated.AIState != store.AITicketTakeover {
		t.Fatalf("达到上限应转人工: %q", updated.AIState)
	}
}

func TestDisabledTypeSkipsReply(t *testing.T) {
	client := &fakeClient{}
	sender := &fakeSender{}
	svc, st := newTestService(t, client, sender)

	ticket, _ := seedTicket(t, st, "TK-260105-TEST", false) // 类型未开 AI
	msg := addMessage(t, st, ticket.No, "u1", "你好", false, false)
	svc.OnUserMessage(context.Background(), ticket, msg)

	time.Sleep(1500 * time.Millisecond)
	if got := sender.sent(); len(got) != 0 {
		t.Fatalf("类型未开启不应回复: %v", got)
	}
}

func TestSummaryGeneration(t *testing.T) {
	client := &fakeClient{chatFn: func(req llm.ChatRequest) (string, error) {
		return "用户询问退款时效，已答复 3-5 个工作日。", nil
	}}
	sender := &fakeSender{}
	svc, st := newTestService(t, client, sender)

	ticket, _ := seedTicket(t, st, "TK-260106-TEST", false)
	addMessage(t, st, ticket.No, "u1", "退款多久到账", false, false)
	addMessage(t, st, ticket.No, "u2", "3-5 个工作日", false, false)

	if err := svc.TriggerSummary(ticket.No); err != nil {
		t.Fatalf("触发摘要失败: %v", err)
	}
	if !waitFor(t, 3*time.Second, func() bool {
		got, err := st.Tickets.ByNo(ticket.No)
		return err == nil && got.AISummary != ""
	}) {
		t.Fatal("等待摘要生成超时")
	}
	got, _ := st.Tickets.ByNo(ticket.No)
	if !strings.Contains(got.AISummary, "退款") || got.AISummaryAt == nil {
		t.Fatalf("摘要不符: %+v", got)
	}
}

func TestClassificationAndApply(t *testing.T) {
	client := &fakeClient{chatFn: func(req llm.ChatRequest) (string, error) {
		return `{"type": "咨询", "reason": "用户在咨询退款政策"}`, nil
	}}
	sender := &fakeSender{}
	svc, st := newTestService(t, client, sender)

	// 再建一个类型供建议选择。
	other := &store.TicketType{Name: "投诉", Enabled: true}
	if err := st.Types.Create(other); err != nil {
		t.Fatalf("创建类型失败: %v", err)
	}

	ticket, _ := seedTicket(t, st, "TK-260107-TEST", false)
	addMessage(t, st, ticket.No, "u1", "退款太慢了", false, false)

	if err := svc.TriggerClassification(ticket.No); err != nil {
		t.Fatalf("触发分类失败: %v", err)
	}
	if !waitFor(t, 3*time.Second, func() bool {
		got, err := st.Tickets.ByNo(ticket.No)
		return err == nil && got.AIClassification != ""
	}) {
		t.Fatal("等待分类建议超时")
	}

	if err := svc.ApplyClassification(ticket.No, "投诉"); err != nil {
		t.Fatalf("应用建议失败: %v", err)
	}
	got, _ := st.Tickets.ByNo(ticket.No)
	if got.TypeName != "投诉" || got.AIClassification != "" {
		t.Fatalf("应用后类型不符: %+v", got)
	}
	if got.TypeID == nil || *got.TypeID != other.ID {
		t.Fatalf("TypeID 不符: %+v", got)
	}

	// 不存在的类型应拒绝。
	if err := svc.ApplyClassification(ticket.No, "不存在的类型"); err == nil {
		t.Fatal("不存在的类型应报错")
	}
}

func TestParseReplyJSONFallbacks(t *testing.T) {
	// 纯 JSON。
	action, reply, class := parseReplyJSON(`{"action":"reply","reply":"你好","suggested_type":"咨询","type_reason":"r"}`)
	if action != replyActionReply || reply != "你好" || class == nil || class.Type != "咨询" {
		t.Fatalf("纯 JSON 解析不符: %q %q %+v", action, reply, class)
	}
	// action 缺省按 reply 处理。
	action, reply, _ = parseReplyJSON(`{"reply":"你好"}`)
	if action != replyActionReply || reply != "你好" {
		t.Fatalf("action 缺省应按 reply: %q %q", action, reply)
	}
	// silent：无回复正文。
	action, reply, _ = parseReplyJSON(`{"action":"silent","reply":""}`)
	if action != replyActionSilent || reply != "" {
		t.Fatalf("silent 解析不符: %q %q", action, reply)
	}
	// escalate。
	action, reply, _ = parseReplyJSON(`{"action":"escalate","reply":"转人工"}`)
	if action != replyActionEscalate || reply != "转人工" {
		t.Fatalf("escalate 解析不符: %q %q", action, reply)
	}
	// 未知 action 值按 reply 处理。
	action, reply, _ = parseReplyJSON(`{"action":"chat","reply":"你好"}`)
	if action != replyActionReply || reply != "你好" {
		t.Fatalf("未知 action 应按 reply: %q %q", action, reply)
	}
	// 代码块包裹。
	action, reply, class = parseReplyJSON("```json\n{\"reply\":\"好的\"}\n```")
	if action != replyActionReply || reply != "好的" || class != nil {
		t.Fatalf("代码块解析不符: %q %q %+v", action, reply, class)
	}
	// 前后有说明文字。
	_, reply, _ = parseReplyJSON(`以下是回复：{"reply":"就这样"} 希望有帮助`)
	if reply != "就这样" {
		t.Fatalf("前后缀解析不符: %q", reply)
	}
	// 完全不是 JSON：整段作为回复。
	action, reply, _ = parseReplyJSON("直接回答你")
	if action != replyActionReply || reply != "直接回答你" {
		t.Fatalf("兜底解析不符: %q %q", action, reply)
	}
	// JSON 但无 reply 与 action 字段：整段作为回复。
	action, reply, _ = parseReplyJSON(`{"foo":"bar"}`)
	if action != replyActionReply || reply != `{"foo":"bar"}` {
		t.Fatalf("无 reply 字段应兜底: %q %q", action, reply)
	}
}

func TestSilentActionSkipsSend(t *testing.T) {
	client := &fakeClient{chatFn: func(req llm.ChatRequest) (string, error) {
		return `{"action":"silent","reply":""}`, nil
	}}
	sender := &fakeSender{}
	svc, st := newTestService(t, client, sender)

	ticket, _ := seedTicket(t, st, "TK-260108-TEST", true)
	svc.OnTicketOpened(context.Background(), ticket)
	msg := addMessage(t, st, ticket.No, "u1", "好的，谢谢", false, false)
	svc.OnUserMessage(context.Background(), ticket, msg)

	// 等到 LLM 被调用过，再确认没有发送任何消息。
	if !waitFor(t, 5*time.Second, func() bool { return len(client.chats) == 1 }) {
		t.Fatal("等待 LLM 调用超时")
	}
	time.Sleep(300 * time.Millisecond)
	if got := sender.sent(); len(got) != 0 {
		t.Fatalf("silent 不应发送消息: %v", got)
	}
	// 状态应保持 active（静默不等于接管）。
	updated, _ := st.Tickets.ByNo(ticket.No)
	if updated.AIState != store.AITicketActive {
		t.Fatalf("silent 后应保持 active: %q", updated.AIState)
	}
}

func TestEscalateActionSendsAndTakesOver(t *testing.T) {
	client := &fakeClient{chatFn: func(req llm.ChatRequest) (string, error) {
		return `{"action":"escalate","reply":"这个问题我需要转交人工客服处理"}`, nil
	}}
	sender := &fakeSender{}
	svc, st := newTestService(t, client, sender)

	ticket, _ := seedTicket(t, st, "TK-260109-TEST", true)
	svc.OnTicketOpened(context.Background(), ticket)
	msg := addMessage(t, st, ticket.No, "u1", "你们的赔偿政策到底怎么算", false, false)
	svc.OnUserMessage(context.Background(), ticket, msg)

	if !waitFor(t, 5*time.Second, func() bool { return len(sender.sent()) == 1 }) {
		t.Fatal("等待 AI 回复超时")
	}
	if got := sender.sent()[0]; !strings.Contains(got, "转交人工客服") || !strings.Contains(got, "—— AI") {
		t.Fatalf("转人工话术应含正文与尾注: %q", got)
	}
	if !waitFor(t, 3*time.Second, func() bool {
		updated, err := st.Tickets.ByNo(ticket.No)
		return err == nil && updated.AIState == store.AITicketTakeover
	}) {
		t.Fatal("escalate 后应转为接管")
	}

	// escalate 但未给话术：用兜底文案。
	client2 := &fakeClient{chatFn: func(req llm.ChatRequest) (string, error) {
		return `{"action":"escalate","reply":""}`, nil
	}}
	sender2 := &fakeSender{}
	svc2, st2 := newTestService(t, client2, sender2)
	ticket2, _ := seedTicket(t, st2, "TK-260110-TEST", true)
	svc2.OnTicketOpened(context.Background(), ticket2)
	msg2 := addMessage(t, st2, ticket2.No, "u1", "赔偿到底怎么算", false, false)
	svc2.OnUserMessage(context.Background(), ticket2, msg2)
	if !waitFor(t, 5*time.Second, func() bool { return len(sender2.sent()) == 1 }) {
		t.Fatal("等待兜底转人工回复超时")
	}
	if got := sender2.sent()[0]; !strings.Contains(got, "人工客服") {
		t.Fatalf("escalate 空话术应使用兜底文案: %q", got)
	}
}

func TestReplyRequestContract(t *testing.T) {
	cfg := &store.AISettings{SystemPrompt: "你是客服", Temperature: 0.4}
	ticket := &store.Ticket{No: "TK-1", TypeName: "咨询", UserID: "u1", UserName: "提问者"}
	messages := []store.TicketMessage{
		{TicketNo: "TK-1", UserID: "u1", Content: "退款多久", MsgType: store.MsgTypeText},
	}

	// 回复请求始终开启 JSON mode，并携带输出协议。
	req := buildReplyRequest(cfg, ticket, messages, nil, nil, false)
	if !req.JSONMode {
		t.Fatal("回复请求应始终开启 JSONMode")
	}
	system := req.Messages[0].Content
	for _, want := range []string{`"action"`, "silent", "escalate", "suggested_type 与 type_reason 留空"} {
		if !strings.Contains(system, want) {
			t.Fatalf("输出协议缺少 %q: %s", want, system)
		}
	}

	// 需要分类时给出类型候选。
	req = buildReplyRequest(cfg, ticket, messages, nil, []string{"咨询", "投诉"}, true)
	if !strings.Contains(req.Messages[0].Content, "咨询、投诉") {
		t.Fatalf("分类候选未进入协议: %s", req.Messages[0].Content)
	}

	// 知识示例带相关度提示。
	req = buildReplyRequest(cfg, ticket, messages, []store.AIKnowledge{
		{Question: "退款多久到账", Answer: "3-5 个工作日"},
	}, nil, false)
	system = req.Messages[0].Content
	if !strings.Contains(system, "确实相关时才参考") || !strings.Contains(system, "3-5 个工作日") {
		t.Fatalf("知识示例提示不符: %s", system)
	}
}

func TestKeywordRetrievalAndCorrectionBoost(t *testing.T) {
	entries := []store.AIKnowledge{
		{Question: "退款多久到账", Answer: "3-5 个工作日"},
		{Question: "怎么改密码", Answer: "设置里改"},
	}
	hits := retrieveByKeyword(entries, "退款\n多久")
	if len(hits) != 1 || hits[0].score != 2 {
		t.Fatalf("关键词计分不符: %+v", hits)
	}

	// 同样命中两条关键词的纠正条目，加权后分数应更高（排序在 retrieveKnowledge 里做）。
	corrected := retrieveByKeyword([]store.AIKnowledge{
		{Question: "退款多久到账（再次询问）", Answer: "以人工答复为准", IsCorrection: true},
	}, "退款\n多久")
	if len(corrected) != 1 || corrected[0].score <= hits[0].score {
		t.Fatalf("纠正条目应加权: %+v vs %+v", corrected, hits)
	}
}

func TestRetrievalRelevanceThresholds(t *testing.T) {
	// 关键词模式：4 个查询词只命中 1 个（覆盖率 < 0.5）应被过滤。
	hits := retrieveByKeyword([]store.AIKnowledge{
		{Question: "怎么改密码", Answer: "设置里改"},
	}, "退款\n多久\n到账\n密码")
	if len(hits) != 0 {
		t.Fatalf("覆盖率不足应被过滤: %+v", hits)
	}

	// 向量模式：余弦相似度低于阈值应被过滤。
	client := &fakeClient{embedFn: func(texts []string) ([][]float32, error) {
		// 查询向量固定为 [1,0]；条目向量已落库，不参与本次嵌入。
		return [][]float32{{1, 0}}, nil
	}}
	sender := &fakeSender{}
	svc, st := newTestService(t, client, sender)
	if err := st.AI.SaveConfig(&store.AISettings{
		Enabled: true, BaseURL: "https://fake", Model: "fake-model",
		EmbeddingModel: "fake-emb", SystemPrompt: "你是客服",
		QuietSeconds: 1, MaxReplies: 10, ContextMessages: 20, KnowledgeTopK: 5,
	}, "fake-key", false, testAISecret()); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	entries := []store.AIKnowledge{
		{Question: "无关问题", Answer: "无关答案", Embedding: "[0,1]"},           // 相似度 0
		{Question: "退款多久到账", Answer: "3-5 个工作日", Embedding: "[0.6,0.8]"}, // 相似度 0.6
		{Question: "退款时效", Answer: "同上", Embedding: "[1,0]"},             // 相似度 1
	}
	cfg, err := svc.config()
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	hits = svc.retrieveByEmbedding(context.Background(), cfg, entries, "退款多久到账")
	if len(hits) != 1 || hits[0].entry.Question != "退款时效" {
		t.Fatalf("低相似度条目应被过滤: %+v", hits)
	}
}
