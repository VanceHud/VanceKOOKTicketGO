package bot

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/ai"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook/kooktest"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/llm"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/llm/llmtest"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// botTestSecret 与 newBotEnv 里 bot.Deps.AppSecret 保持一致：
// AI 配置的加密 Key 用同一密钥解密。
var botTestSecret = []byte("app-secret-for-bot-tests-0123456789")

// newAIEnv 创建带 AI 服务的测试环境：LLM 指向 llmtest 模拟服务，
// 发送通道指向机器人本体（生产中是 Manager，测试直接用 Bot 实例）。
func newAIEnv(t *testing.T, llmServer *llmtest.Server) *botEnv {
	t.Helper()
	env := newBotEnv(t, withAI(func(env *botEnv) *ai.Service {
		svc := ai.New(ai.Deps{
			Store:     env.store,
			Bus:       env.bus,
			Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
			AppSecret: botTestSecret,
			ClientFactory: func(cfg *store.AISettings) (ai.LLMClient, error) {
				return llm.NewClient(llm.Options{
					BaseURL:       llmServer.BaseURL(),
					APIKey:        cfg.APIKeyPlaintext,
					Model:         cfg.Model,
					RatePerSecond: 100,
					Burst:         10,
					Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
				})
			},
		})
		svc.Start()
		t.Cleanup(svc.Stop)
		return svc
	}))
	// AI 回复的发送通道：Bot 实现 ai.Sender。
	env.aiService.SetSender(env.bot)

	// 启用 AI：全局开关 + 类型开关 + 防抖缩短到 1 秒。
	if err := env.store.AI.SaveConfig(&store.AISettings{
		Enabled:         true,
		BaseURL:         llmServer.BaseURL(),
		Model:           "mock-model",
		SystemPrompt:    "你是客服",
		Footer:          "—— AI 自动回复",
		QuietSeconds:    1,
		MaxReplies:      10,
		ContextMessages: 20,
		KnowledgeTopK:   5,
	}, "sk-llm-test", false, botTestSecret); err != nil {
		t.Fatalf("保存 AI 配置失败: %v", err)
	}
	if err := env.store.Types.UpdateFields(env.typeID, map[string]any{"ai_auto_reply": true}); err != nil {
		t.Fatalf("开启类型 AI 失败: %v", err)
	}
	return env
}

// TestAutoReplyEndToEnd 验证完整链路：
// 开单（AI 状态激活）→ 开单人消息（防抖）→ LLM 生成 → 发到 KOOK 频道 → 归档带 IsAI。
func TestAutoReplyEndToEnd(t *testing.T) {
	llmServer := llmtest.New()
	defer llmServer.Close()
	llmServer.SetReply(func(req llmtest.Request, _ []llm.Message) string {
		return "退款一般 3-5 个工作日到账"
	})
	env := newAIEnv(t, llmServer)

	channelID := env.openTicketForTest()

	// 开单后 AI 状态应为 active。
	env.waitFor("AI 状态激活", func() bool {
		tk := env.firstTicket()
		return tk != nil && tk.AIState == store.AITicketActive
	})

	// 开单人发消息 → 防抖 1s → 生成并发送。
	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(channelID, userAsker, "退款多久到账", env.user(userAsker)))

	env.waitFor("AI 回复归档", func() bool {
		count, err := env.store.Tickets.CountAIMessages(env.firstTicket().No)
		return err == nil && count == 1
	})

	// KOOK 侧确实收到了回复（含尾注）。
	ticketNo := env.firstTicket().No
	var sent []string
	for _, call := range env.mock.CallsOf("message/create") {
		if call.Params["target_id"] == channelID {
			if content, ok := call.Params["content"].(string); ok {
				sent = append(sent, content)
			}
		}
	}
	found := false
	for _, content := range sent {
		if strings.Contains(content, "3-5 个工作日") && strings.Contains(content, "—— AI 自动回复") {
			found = true
		}
	}
	if !found {
		t.Fatalf("KOOK 侧未收到带尾注的 AI 回复: %v", sent)
	}

	// LLM 收到的请求应包含系统提示词与用户消息。
	requests := llmServer.RequestsOf("chat/completions")
	if len(requests) == 0 {
		t.Fatal("LLM 未收到请求")
	}
	hasSystem, hasUser := false, false
	for _, m := range requests[len(requests)-1].Messages {
		if m.Role == "system" && strings.Contains(m.Content, "你是客服") {
			hasSystem = true
		}
		if m.Role == "user" && strings.Contains(m.Content, "退款多久到账") {
			hasUser = true
		}
	}
	if !hasSystem || !hasUser {
		t.Fatalf("提示词组装不完整: system=%v user=%v", hasSystem, hasUser)
	}
	_ = ticketNo
}

// TestStaffReplyTakesOverEndToEnd 验证人工接管：
// 开单人提问后人工客服立即回复 → 防抖取消、AI 不再回复、知识被捕获。
func TestStaffReplyTakesOverEndToEnd(t *testing.T) {
	llmServer := llmtest.New()
	defer llmServer.Close()
	env := newAIEnv(t, llmServer)

	channelID := env.openTicketForTest()

	// 用户提问，人工（客服组角色）抢在防抖窗口内回复。
	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(channelID, userAsker, "服务器进不去怎么办", env.user(userAsker)))
	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(channelID, userStaff, "正在维护，10 分钟后恢复", env.user(userStaff)))

	env.waitFor("人工接管", func() bool {
		tk := env.firstTicket()
		return tk != nil && tk.AIState == store.AITicketTakeover
	})

	// 等超过防抖窗口，确认 AI 没有回复。
	time.Sleep(1500 * time.Millisecond)
	count, err := env.store.Tickets.CountAIMessages(env.firstTicket().No)
	if err != nil || count != 0 {
		t.Fatalf("人工接管后 AI 不应回复: count=%d err=%v", count, err)
	}

	// 人工回复被捕获为知识。
	env.waitFor("知识捕获", func() bool {
		items, _, err := env.store.AI.KnowledgeList(store.KnowledgeFilter{})
		return err == nil && len(items) == 1
	})
	items, _, _ := env.store.AI.KnowledgeList(store.KnowledgeFilter{})
	if !strings.Contains(items[0].Question, "服务器进不去") || !strings.Contains(items[0].Answer, "维护") {
		t.Fatalf("知识内容不符: %+v", items[0])
	}
}

// TestTKAICommand 验证 /tkai 命令的状态切换。
func TestTKAICommand(t *testing.T) {
	llmServer := llmtest.New()
	defer llmServer.Close()
	env := newAIEnv(t, llmServer)

	channelID := env.openTicketForTest()
	env.waitFor("AI 状态激活", func() bool {
		tk := env.firstTicket()
		return tk != nil && tk.AIState == store.AITicketActive
	})

	// 管理员暂停。
	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(channelID, userStaff, "/tkai off", env.user(userStaff)))
	env.waitFor("AI 已暂停", func() bool {
		tk := env.firstTicket()
		return tk != nil && tk.AIState == store.AITicketTakeover
	})

	// 恢复。
	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(channelID, userStaff, "/tkai on", env.user(userStaff)))
	env.waitFor("AI 已恢复", func() bool {
		tk := env.firstTicket()
		return tk != nil && tk.AIState == store.AITicketActive
	})

	// 开单人无权切换。
	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(channelID, userAsker, "/tkai off", env.user(userAsker)))
	time.Sleep(300 * time.Millisecond)
	if tk := env.firstTicket(); tk.AIState != store.AITicketActive {
		t.Fatalf("开单人不应能切换 AI 状态: %q", tk.AIState)
	}
}
