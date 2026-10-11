package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/ai"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// fakeAI 是测试用的 AI 控制器实现。
type fakeAI struct {
	testErr         error
	summaries       []string
	classifications []string
	applied         []string
	toggles         map[string]bool
	searchHits      []store.AIKnowledge
	searchErr       error
}

func (f *fakeAI) TestConnection(ctx context.Context) error { return f.testErr }

func (f *fakeAI) TriggerSummary(no string) error {
	f.summaries = append(f.summaries, no)
	return nil
}

func (f *fakeAI) TriggerClassification(no string) error {
	f.classifications = append(f.classifications, no)
	return nil
}

func (f *fakeAI) ApplyClassification(no string, typeName string) error {
	if typeName == "不存在的类型" {
		return ai.ErrInvalidAIState
	}
	f.applied = append(f.applied, no+":"+typeName)
	return nil
}

func (f *fakeAI) ToggleTicket(no string, enable bool) error {
	if f.toggles == nil {
		f.toggles = map[string]bool{}
	}
	f.toggles[no] = enable
	return nil
}

func (f *fakeAI) SearchKnowledge(ctx context.Context, query string) ([]store.AIKnowledge, error) {
	return f.searchHits, f.searchErr
}

func (f *fakeAI) EmbedKnowledgeNow(ctx context.Context, id uint) error { return nil }

// withAI 注入假 AI 控制器并重建路由。
func (e *testEnv) withAI(t *testing.T, controller AIController) *fakeAI {
	t.Helper()
	e.deps.AI = controller
	e.engine = NewRouter(e.deps)
	if fake, ok := controller.(*fakeAI); ok {
		return fake
	}
	return nil
}

func TestAISettingsGetAndUpdate(t *testing.T) {
	env := newTestEnv(t)
	env.withAI(t, &fakeAI{})
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)

	// 默认配置：关闭、无 Key。
	res := env.do(t, http.MethodGet, "/api/v1/ai/settings", nil, withCookie(cookie))
	if res.status != http.StatusOK {
		t.Fatalf("读取设置失败: %d %s", res.status, res.raw)
	}
	if res.body["enabled"] != false {
		t.Fatalf("默认应关闭: %v", res.body["enabled"])
	}

	// 保存配置（含 Key）。
	res = env.do(t, http.MethodPut, "/api/v1/ai/settings", map[string]any{
		"enabled":      true,
		"baseUrl":      "https://api.deepseek.com/v1",
		"model":        "deepseek-chat",
		"apiKey":       "sk-test-ai-key",
		"temperature":  0.5,
		"quietSeconds": 6,
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("保存设置失败: %d %s", res.status, res.raw)
	}
	// 响应只应有掩码，绝不能出现明文 Key。
	if strings.Contains(res.raw, "sk-test-ai-key") {
		t.Fatal("设置响应泄露了 API Key 明文")
	}
	if res.body["hasApiKey"] != true {
		t.Fatalf("应报告已配置 Key: %v", res.body["hasApiKey"])
	}
	if masked, _ := res.body["apiKeyMasked"].(string); masked == "" || strings.Contains(masked, "sk-test") {
		t.Fatalf("掩码格式异常: %q", masked)
	}

	// 非法取值应被拒绝。
	res = env.do(t, http.MethodPut, "/api/v1/ai/settings", map[string]any{
		"quietSeconds": 999,
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("非法取值应 400: %d", res.status)
	}

	// 非管理员应被拒绝。
	env.seedUser(t, "staff", staffPassword, store.RoleStaff, false)
	staffCookie, staffCSRF := env.login(t, "staff", staffPassword)
	res = env.do(t, http.MethodPut, "/api/v1/ai/settings", map[string]any{"enabled": false},
		withCookie(staffCookie), withCSRF(staffCSRF))
	if res.status != http.StatusForbidden {
		t.Fatalf("客服不应能改 AI 设置: %d", res.status)
	}
}

func TestAISettingsEnableRequiresKey(t *testing.T) {
	env := newTestEnv(t)
	env.withAI(t, &fakeAI{})
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)

	// 未配置 Key 直接开启应被拒绝。
	res := env.do(t, http.MethodPut, "/api/v1/ai/settings", map[string]any{"enabled": true},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("无 Key 开启应 400: %d %s", res.status, res.raw)
	}
}

func TestAITestEndpoint(t *testing.T) {
	env := newTestEnv(t)
	fake := &fakeAI{}
	env.withAI(t, fake)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)

	// 未配置时提示先配置。
	res := env.do(t, http.MethodPost, "/api/v1/ai/test", nil, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("未配置应 400: %d", res.status)
	}

	// 配置后测试成功。
	if err := env.store.AI.SaveConfig(&store.AISettings{Enabled: true, Model: "m"}, "sk-key", false, env.config.AppSecret); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	res = env.do(t, http.MethodPost, "/api/v1/ai/test", nil, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK || res.body["ok"] != true {
		t.Fatalf("测试应成功: %d %s", res.status, res.raw)
	}

	// 失败路径返回可读错误。
	fake.testErr = errors.New("LLM 接口 chat/completions 返回错误: HTTP 401")
	res = env.do(t, http.MethodPost, "/api/v1/ai/test", nil, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK || res.body["ok"] != false {
		t.Fatalf("失败应返回 ok=false: %d %s", res.status, res.raw)
	}
}

func TestAIKnowledgeCRUDAndRBAC(t *testing.T) {
	env := newTestEnv(t)
	env.withAI(t, &fakeAI{})
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)

	// 新增。
	res := env.do(t, http.MethodPost, "/api/v1/ai/knowledge", map[string]any{
		"question": "退款多久到账",
		"answer":   "3-5 个工作日",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusCreated {
		t.Fatalf("新增知识失败: %d %s", res.status, res.raw)
	}
	id := "1"

	// 列表与过滤。
	res = env.do(t, http.MethodGet, "/api/v1/ai/knowledge?q=退款", nil, withCookie(cookie))
	if res.status != http.StatusOK {
		t.Fatalf("查询失败: %d", res.status)
	}
	if total := int(res.body["total"].(float64)); total != 1 {
		t.Fatalf("过滤结果不符: %v", res.body["total"])
	}

	// 更新与删除。
	res = env.do(t, http.MethodPatch, "/api/v1/ai/knowledge/"+id, map[string]any{"enabled": false},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("更新失败: %d %s", res.status, res.raw)
	}
	res = env.do(t, http.MethodDelete, "/api/v1/ai/knowledge/"+id, nil, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("删除失败: %d", res.status)
	}

	// 客服不能管理知识库。
	env.seedUser(t, "staff", staffPassword, store.RoleStaff, false)
	staffCookie, staffCSRF := env.login(t, "staff", staffPassword)
	res = env.do(t, http.MethodPost, "/api/v1/ai/knowledge", map[string]any{"question": "q", "answer": "a"},
		withCookie(staffCookie), withCSRF(staffCSRF))
	if res.status != http.StatusForbidden {
		t.Fatalf("客服不应能管理知识库: %d", res.status)
	}
}

func TestTicketAIOperations(t *testing.T) {
	env := newTestEnv(t)
	fake := &fakeAI{}
	env.withAI(t, fake)
	env.seedUser(t, "staff", staffPassword, store.RoleStaff, false)
	cookie, csrf := env.login(t, "staff", staffPassword)
	tk := env.seedTicket(t, store.TicketOpen)

	// 摘要触发（异步 202）。
	res := env.do(t, http.MethodPost, "/api/v1/tickets/"+tk.No+"/ai/summary", nil,
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusAccepted {
		t.Fatalf("触发摘要失败: %d %s", res.status, res.raw)
	}
	if len(fake.summaries) != 1 || fake.summaries[0] != tk.No {
		t.Fatalf("摘要任务未触发: %v", fake.summaries)
	}

	// 分类触发与应用。
	res = env.do(t, http.MethodPost, "/api/v1/tickets/"+tk.No+"/ai/classify", nil,
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusAccepted {
		t.Fatalf("触发分类失败: %d", res.status)
	}
	env.store.Types.Create(&store.TicketType{Name: "咨询", Enabled: true})
	res = env.do(t, http.MethodPost, "/api/v1/tickets/"+tk.No+"/ai/classify/apply", map[string]any{"type": "咨询"},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("应用分类失败: %d %s", res.status, res.raw)
	}
	if len(fake.applied) != 1 {
		t.Fatalf("应用未触发: %v", fake.applied)
	}

	// 开关。
	res = env.do(t, http.MethodPost, "/api/v1/tickets/"+tk.No+"/ai/toggle", map[string]any{"enable": false},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("切换 AI 失败: %d %s", res.status, res.raw)
	}

	// 只读账号不能操作。
	env.seedUser(t, "readonly", readonlyPassword, store.RoleReadonly, false)
	roCookie, roCSRF := env.login(t, "readonly", readonlyPassword)
	res = env.do(t, http.MethodPost, "/api/v1/tickets/"+tk.No+"/ai/summary", nil,
		withCookie(roCookie), withCSRF(roCSRF))
	if res.status != http.StatusForbidden {
		t.Fatalf("只读不应能触发 AI 操作: %d", res.status)
	}
}

func TestTicketDetailIncludesAIClassification(t *testing.T) {
	env := newTestEnv(t)
	env.withAI(t, &fakeAI{})
	env.seedUser(t, "staff", staffPassword, store.RoleStaff, false)
	cookie, _ := env.login(t, "staff", staffPassword)
	tk := env.seedTicket(t, store.TicketOpen)

	// 写入分类建议后，详情应返回解析后的结构。
	if err := env.store.Tickets.UpdateFields(tk.No, map[string]any{
		"ai_classification": `{"type":"咨询","reason":"退款咨询"}`,
		"ai_summary":        "用户咨询退款",
	}); err != nil {
		t.Fatalf("更新工单失败: %v", err)
	}
	res := env.do(t, http.MethodGet, "/api/v1/tickets/"+tk.No, nil, withCookie(cookie))
	if res.status != http.StatusOK {
		t.Fatalf("读取详情失败: %d", res.status)
	}
	suggestion, ok := res.body["aiClassificationSuggestion"].(map[string]any)
	if !ok || suggestion["type"] != "咨询" {
		t.Fatalf("分类建议未解析: %v", res.body["aiClassificationSuggestion"])
	}
	if res.body["aiSummary"] != "用户咨询退款" {
		t.Fatalf("摘要未返回: %v", res.body["aiSummary"])
	}
}
