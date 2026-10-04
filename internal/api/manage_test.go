package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"vancekookticket/internal/bot"
	"vancekookticket/internal/kook"
	"vancekookticket/internal/store"
)

// ---------------------------------------------------------------------------
// 假机器人：用于在离线环境下测试面板管理与统计接口
// ---------------------------------------------------------------------------

// fakeBot 只实现 API 层需要的方法，避免在单元测试中启动真实连接。
type fakeBot struct {
	status        bot.Status
	panels        []string
	deleted       []string
	createErr     error
	channels      map[string]kook.Channel
	roles         map[string]string
	restartCalled bool
}

func newFakeBot() *fakeBot {
	return &fakeBot{
		status: bot.Status{Running: true, Connected: true, BotName: "测试机器人", GuildID: "5000"},
		channels: map[string]kook.Channel{
			"30001": {ID: "30001", Name: "工单面板", Type: kook.ChannelText},
			"30002": {ID: "30002", Name: "隐藏分组", IsCategory: true},
		},
		roles: map[string]string{"10002": "客服组", "10003": "实习客服"},
	}
}

func (f *fakeBot) Status() bot.Status { return f.status }

func (f *fakeBot) GuildRoles(_ any) ([]kook.Role, error) { return nil, nil }

func (f *fakeBot) CreatePanel(_ any, channelID, _, _ string) (string, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	f.panels = append(f.panels, channelID)
	return fmt.Sprintf("msg-%d", len(f.panels)), nil
}

func (f *fakeBot) DeleteMessage(_ any, msgID string) error {
	f.deleted = append(f.deleted, msgID)
	return nil
}

func (f *fakeBot) ChannelInfo(_ any, channelID string) (*kook.Channel, error) {
	channel, ok := f.channels[channelID]
	if !ok {
		return nil, fmt.Errorf("频道不存在")
	}
	return &channel, nil
}

func (f *fakeBot) RoleName(_ any, roleID string) string { return f.roles[roleID] }

func (f *fakeBot) NotifyConfigChanged() {}

func (f *fakeBot) Restart(_ any) error {
	f.restartCalled = true
	return nil
}

// 说明：fakeBot 的 ctx 参数用 any 会被接口签名拒绝，因此下面用适配器包装。
type botAdapter struct{ inner *fakeBot }

func (a botAdapter) Status() bot.Status { return a.inner.Status() }

func (a botAdapter) GuildRoles(ctx context.Context) ([]kook.Role, error) { return nil, nil }

func (a botAdapter) GuildChannels(ctx context.Context) ([]kook.Channel, error) {
	items := make([]kook.Channel, 0, len(a.inner.channels))
	for _, channel := range a.inner.channels {
		items = append(items, channel)
	}
	return items, nil
}

func (a botAdapter) ResolveWebRole(ctx context.Context, _ string) (string, bool, error) {
	return store.RoleStaff, true, nil
}

func (a botAdapter) Restart(ctx context.Context) error { return a.inner.Restart(ctx) }

func (a botAdapter) NotifyConfigChanged() { a.inner.NotifyConfigChanged() }

func (a botAdapter) CreatePanel(ctx context.Context, channelID, title, buttonText string) (string, error) {
	return a.inner.CreatePanel(ctx, channelID, title, buttonText)
}

func (a botAdapter) DeleteMessage(ctx context.Context, msgID string) error {
	return a.inner.DeleteMessage(ctx, msgID)
}

func (a botAdapter) ChannelInfo(ctx context.Context, channelID string) (*kook.Channel, error) {
	return a.inner.ChannelInfo(ctx, channelID)
}

func (a botAdapter) RoleName(ctx context.Context, roleID string) string {
	return a.inner.RoleName(ctx, roleID)
}

// withFakeBot 注入假机器人（在线状态），用于测试面板管理等依赖机器人的接口。
func (e *testEnv) withFakeBot(t *testing.T) *fakeBot {
	t.Helper()
	fake := newFakeBot()
	deps := e.deps
	deps.Bot = botAdapter{inner: fake}
	e.engine = NewRouter(deps)
	return fake
}

// ---------------------------------------------------------------------------
// 面板管理
// ---------------------------------------------------------------------------

func TestPanelCreateRequiresBotOnline(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)

	// 未注入机器人：应返回 503 而不是写入一个不会生效的配置
	res := env.do(t, http.MethodPost, "/api/v1/panels", map[string]string{"channelId": "30001"},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusServiceUnavailable {
		t.Fatalf("机器人离线时应返回 503，得到 %d %s", res.status, res.raw)
	}

	list, err := env.store.Panels.List()
	if err != nil {
		t.Fatalf("查询面板失败: %v", err)
	}
	if len(list) != 0 {
		t.Fatal("机器人离线时不应写入面板配置")
	}
}

func TestPanelCRUDWithBotOnline(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)
	fake := env.withFakeBot(t)

	// 创建面板
	res := env.do(t, http.MethodPost, "/api/v1/panels", map[string]any{
		"channelId": "30001", "title": "点我开单", "buttonText": "开单",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusCreated {
		t.Fatalf("创建面板失败: %d %s", res.status, res.raw)
	}
	if len(fake.panels) != 1 || fake.panels[0] != "30001" {
		t.Fatalf("应向频道 3001 发送面板卡片，实际 %v", fake.panels)
	}
	panelID := uint(res.body["id"].(float64))
	if res.body["channelName"] != "工单面板" {
		t.Fatalf("应记录频道名，得到 %v", res.body["channelName"])
	}

	// 非法参数：分组不能作为面板
	res = env.do(t, http.MethodPost, "/api/v1/panels", map[string]any{"channelId": "30002"},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("分组频道应被拒绝，得到 %d", res.status)
	}
	res = env.do(t, http.MethodPost, "/api/v1/panels", map[string]any{"channelId": "not-a-id"},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("非法频道 ID 应被拒绝，得到 %d", res.status)
	}
	res = env.do(t, http.MethodPost, "/api/v1/panels", map[string]any{"channelId": "9999"},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("不存在的频道应被拒绝，得到 %d", res.status)
	}

	// 更新（禁用）
	disabled := false
	res = env.do(t, http.MethodPatch, fmt.Sprintf("/api/v1/panels/%d", panelID), map[string]any{
		"enabled": disabled, "title": "新文案",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK || res.body["enabled"] != false {
		t.Fatalf("更新面板失败: %d %s", res.status, res.raw)
	}

	// 面板角色：真实角色应被接受并补全名称
	res = env.do(t, http.MethodPost, fmt.Sprintf("/api/v1/panels/%d/roles", panelID), map[string]any{
		"roleId": "10003",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusCreated {
		t.Fatalf("添加面板角色失败: %d %s", res.status, res.raw)
	}
	stored, err := env.store.Panels.ByID(panelID)
	if err != nil {
		t.Fatalf("读取面板失败: %v", err)
	}
	if len(stored.Roles) != 1 || stored.Roles[0].RoleName != "实习客服" {
		t.Fatalf("面板角色记录异常: %+v", stored.Roles)
	}
	// 不存在的角色应被拒绝
	res = env.do(t, http.MethodPost, fmt.Sprintf("/api/v1/panels/%d/roles", panelID), map[string]any{
		"roleId": "8888",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("不存在的角色应被拒绝，得到 %d", res.status)
	}

	// 重建：应发送新卡片并删除旧卡片
	res = env.do(t, http.MethodPost, fmt.Sprintf("/api/v1/panels/%d/refresh", panelID), nil,
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("重建面板失败: %d %s", res.status, res.raw)
	}
	if len(fake.panels) != 2 {
		t.Fatalf("重建应再发送一次卡片，实际 %v", fake.panels)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != "msg-1" {
		t.Fatalf("重建应删除旧卡片，实际 %v", fake.deleted)
	}

	// 删除角色
	res = env.do(t, http.MethodDelete, fmt.Sprintf("/api/v1/panels/%d/roles/10003", panelID), nil,
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("删除面板角色失败: %d", res.status)
	}
	stored, _ = env.store.Panels.ByID(panelID)
	if len(stored.Roles) != 0 {
		t.Fatalf("面板角色应被移除，剩余 %+v", stored.Roles)
	}

	// 删除面板
	res = env.do(t, http.MethodDelete, fmt.Sprintf("/api/v1/panels/%d", panelID), nil,
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("删除面板失败: %d", res.status)
	}
	if list, _ := env.store.Panels.List(); len(list) != 0 {
		t.Fatal("面板应被删除")
	}
	if env.auditCount(t, "panel.delete") == 0 {
		t.Fatal("面板删除应写入审计")
	}
}

func TestPanelManagementRequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "staff", staffPassword, store.RoleStaff, false)
	cookie, csrf := env.login(t, "staff", staffPassword)
	env.withFakeBot(t)

	res := env.do(t, http.MethodPost, "/api/v1/panels", map[string]any{"channelId": "30001"},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusForbidden {
		t.Fatalf("客服不应能管理面板，得到 %d", res.status)
	}
}

// ---------------------------------------------------------------------------
// 表情规则
// ---------------------------------------------------------------------------

func TestEmojiRuleCRUD(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)

	messageID := "8c1e0d2f3a4b5c6d7e8f9012"
	label := "红色组"
	res := env.do(t, http.MethodPost, "/api/v1/emoji/rules", map[string]any{
		"messageId": messageID,
		"channelId": "30001",
		"emojiId":   "❤",
		"roleId":    "10003",
		"label":     label,
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusCreated {
		t.Fatalf("创建规则失败: %d %s", res.status, res.raw)
	}
	ruleID := uint(res.body["id"].(float64))
	if res.body["enabled"] != true {
		t.Fatalf("默认应启用: %v", res.body["enabled"])
	}

	// 非法输入
	invalid := []map[string]any{
		{"messageId": "短", "emojiId": "❤", "roleId": "10003"},
		{"messageId": messageID, "emojiId": "", "roleId": "10003"},
		{"messageId": messageID, "emojiId": "❤", "roleId": "abc"},
		{"messageId": messageID, "emojiId": "❤", "roleId": "10003", "channelId": "xyz"},
	}
	for i, body := range invalid {
		res := env.do(t, http.MethodPost, "/api/v1/emoji/rules", body, withCookie(cookie), withCSRF(csrf))
		if res.status != http.StatusBadRequest {
			t.Errorf("第 %d 组非法输入应返回 400，得到 %d %s", i+1, res.status, res.raw)
		}
	}

	// 更新
	disabled := false
	newLabel := "蓝色组"
	res = env.do(t, http.MethodPatch, fmt.Sprintf("/api/v1/emoji/rules/%d", ruleID), map[string]any{
		"roleId": "10002", "label": newLabel, "enabled": disabled,
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("更新规则失败: %d %s", res.status, res.raw)
	}
	if res.body["roleId"] != "10002" || res.body["enabled"] != false {
		t.Fatalf("更新结果异常: %s", res.raw)
	}

	// 删除
	res = env.do(t, http.MethodDelete, fmt.Sprintf("/api/v1/emoji/rules/%d", ruleID), nil,
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("删除规则失败: %d", res.status)
	}
	rules, _ := env.store.Emoji.ListRules()
	if len(rules) != 0 {
		t.Fatal("规则应被删除")
	}
	if env.auditCount(t, "emoji.rule.delete") == 0 {
		t.Fatal("删除规则应写入审计")
	}
}

// ---------------------------------------------------------------------------
// 统计细化
// ---------------------------------------------------------------------------

func TestStatsAnalyticsAggregates(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, _ := env.login(t, "admin", adminPassword)

	now := store.Now()
	loc := env.config.Location

	// 构造 4 个工单：3 个已关闭（两个由“客服小林”关闭）、1 个进行中
	type seedSpec struct {
		status       string
		startedAt    time.Time
		closedAt     *time.Time
		firstReplyAt *time.Time
		closedBy     string
		source       string
	}
	closedAt1 := now.Add(-2 * time.Hour)
	closedAt2 := now.Add(-5 * time.Hour)
	replyAt := now.Add(-time.Hour)
	specs := []seedSpec{
		{store.TicketClosed, now.Add(-3 * time.Hour), &closedAt1, &replyAt, "客服小林", "30001"},
		{store.TicketClosed, now.Add(-8 * time.Hour), &closedAt2, nil, "客服小林", "30001"},
		{store.TicketClosed, now.Add(-26 * time.Hour), &closedAt2, nil, "客服小张", "30002"},
		{store.TicketOpen, now.Add(-30 * time.Minute), nil, nil, "", "30001"},
	}

	for i, spec := range specs {
		ticket := &store.Ticket{
			UserID:          fmt.Sprintf("900%d", i),
			UserName:        fmt.Sprintf("用户%d", i),
			SourceChannelID: spec.source,
			ChannelID:       fmt.Sprintf("chan-%d", i),
			Status:          spec.status,
			StartedAt:       spec.startedAt,
		}
		if err := env.store.Tickets.CreateWithNo(ticket, spec.startedAt, loc); err != nil {
			t.Fatalf("创建工单失败: %v", err)
		}
		fields := map[string]any{"status": spec.status, "source_channel_id": spec.source, "started_at": spec.startedAt}
		if spec.closedAt != nil {
			fields["closed_at"] = *spec.closedAt
			fields["closed_by_name"] = spec.closedBy
		}
		if spec.firstReplyAt != nil {
			fields["first_reply_at"] = *spec.firstReplyAt
		}
		if err := env.store.Tickets.UpdateFields(ticket.No, fields); err != nil {
			t.Fatalf("更新工单失败: %v", err)
		}
		// 消息作者必须是开单人本人，否则 AddMessage 会把它记为“首次响应”
		if err := env.store.Tickets.AddMessage(&store.TicketMessage{
			TicketNo: ticket.No, UserID: ticket.UserID, UserName: ticket.UserName,
			Content: "消息", MsgType: store.MsgTypeText, CreatedAt: spec.startedAt,
		}); err != nil {
			t.Fatalf("写入消息失败: %v", err)
		}
	}

	res := env.do(t, http.MethodGet, "/api/v1/stats/analytics?days=30", nil, withCookie(cookie))
	if res.status != http.StatusOK {
		t.Fatalf("统计接口失败: %d %s", res.status, res.raw)
	}

	if total := res.body["total"].(float64); total != 4 {
		t.Fatalf("区间内工单数应为 4，得到 %v", total)
	}
	closed := res.body["closed"].(float64)
	if closed != 3 {
		t.Fatalf("已关闭应为 3，得到 %v", closed)
	}
	if rate := res.body["closedRate"].(float64); rate < 0.74 || rate > 0.76 {
		t.Fatalf("关闭率应约为 0.75，得到 %v", rate)
	}

	// 分位统计
	resolution := res.body["resolution"].(map[string]any)
	if resolution["count"].(float64) != 3 {
		t.Fatalf("解决时长样本应为 3，得到 %v", resolution["count"])
	}
	if resolution["p50Seconds"].(float64) <= 0 {
		t.Fatal("应给出 p50 解决时长")
	}
	firstReply := res.body["firstReply"].(map[string]any)
	if firstReply["count"].(float64) != 1 {
		t.Fatalf("首次响应样本应为 1，得到 %v", firstReply["count"])
	}

	// 时段分布固定 24 项
	hourly := res.body["hourly"].([]any)
	if len(hourly) != 24 {
		t.Fatalf("时段分布应为 24 项，得到 %d", len(hourly))
	}
	totalHourlyOpened := 0.0
	for _, item := range hourly {
		totalHourlyOpened += item.(map[string]any)["opened"].(float64)
	}
	if totalHourlyOpened != 4 {
		t.Fatalf("时段分布的开单总数应为 4，得到 %v", totalHourlyOpened)
	}

	// 客服处理量：客服小林 2 单，客服小张 1 单
	closers := res.body["closers"].([]any)
	if len(closers) != 2 {
		t.Fatalf("应统计 2 名关闭人，得到 %d", len(closers))
	}
	first := closers[0].(map[string]any)
	if first["name"] != "客服小林" || first["closed"].(float64) != 2 {
		t.Fatalf("关闭人排行异常: %+v", first)
	}

	// 来源分布：通道 3001 开单 3 单
	sources := res.body["sources"].([]any)
	if len(sources) != 2 {
		t.Fatalf("应统计 2 个来源，得到 %d", len(sources))
	}
	topSource := sources[0].(map[string]any)
	if !strings.HasPrefix(topSource["channelId"].(string), "30001") {
		t.Fatalf("来源应包含频道名：%+v", topSource)
	}
	if topSource["opened"].(float64) != 3 {
		t.Fatalf("频道 3001 应开单 3 次，得到 %v", topSource["opened"])
	}

	// 归档消息数与今日对比
	if res.body["archivedMessages"].(float64) != 4 {
		t.Fatalf("归档消息数应为 4，得到 %v", res.body["archivedMessages"])
	}
	if res.body["openedToday"].(float64) != 3 {
		t.Fatalf("今日新增应为 3（不含 26 小时前的工单），得到 %v", res.body["openedToday"])
	}
	if res.body["openedYesterday"].(float64) != 1 {
		t.Fatalf("昨日新增应为 1，得到 %v", res.body["openedYesterday"])
	}
}

func TestStatsAnalyticsIsAvailableToReadonly(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "viewer", readonlyPassword, store.RoleReadonly, false)
	cookie, _ := env.login(t, "viewer", readonlyPassword)

	res := env.do(t, http.MethodGet, "/api/v1/stats/analytics?days=7", nil, withCookie(cookie))
	if res.status != http.StatusOK {
		t.Fatalf("只读账号应能查看统计，得到 %d", res.status)
	}
}
