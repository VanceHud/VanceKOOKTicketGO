package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/bot"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// ---------------------------------------------------------------------------
// 假机器人：用于在离线环境下测试面板管理与统计接口
// ---------------------------------------------------------------------------

// fakeBot 只实现 API 层需要的方法，避免在单元测试中启动真实连接。
type fakeBot struct {
	status        bot.Status
	panels        []string
	buttonTexts   []string
	deleted       []string
	createErr     error
	channels      map[string]kook.Channel
	roles         map[string]string
	restartCalled bool

	// 游戏库与在玩动态的可编程状态
	games        []kook.Game
	gameErr      error
	lastGameType int
	createdGames []string
	updatedGames []int64
	deletedGames []int64
	startedGames []int64
	startedMusic []string
	deletedTypes []int
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

func (f *fakeBot) SendPanelCard(_ any, panel *store.Panel, buttonText string) (string, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	f.panels = append(f.panels, panel.ChannelID)
	f.buttonTexts = append(f.buttonTexts, buttonText)
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

func (f *fakeBot) GameList(_ any, gameType int) ([]kook.Game, error) {
	f.lastGameType = gameType
	if f.gameErr != nil {
		return nil, f.gameErr
	}
	out := make([]kook.Game, len(f.games))
	copy(out, f.games)
	return out, nil
}

func (f *fakeBot) GameCreate(_ any, name, icon string) (*kook.Game, error) {
	if f.gameErr != nil {
		return nil, f.gameErr
	}
	f.createdGames = append(f.createdGames, name)
	game := kook.Game{ID: int64(1000000 + len(f.games)), Name: name, Icon: icon}
	f.games = append(f.games, game)
	return &game, nil
}

func (f *fakeBot) GameUpdate(_ any, id int64, name, icon string) (*kook.Game, error) {
	if f.gameErr != nil {
		return nil, f.gameErr
	}
	f.updatedGames = append(f.updatedGames, id)
	return &kook.Game{ID: id, Name: name, Icon: icon}, nil
}

func (f *fakeBot) GameDelete(_ any, id int64) error {
	if f.gameErr != nil {
		return f.gameErr
	}
	f.deletedGames = append(f.deletedGames, id)
	return nil
}

func (f *fakeBot) StartGameActivity(_ any, gameID int64) error {
	if f.gameErr != nil {
		return f.gameErr
	}
	f.startedGames = append(f.startedGames, gameID)
	return nil
}

func (f *fakeBot) StartMusicActivity(_ any, musicName, singer, software string) error {
	if f.gameErr != nil {
		return f.gameErr
	}
	f.startedMusic = append(f.startedMusic, musicName+"|"+singer+"|"+software)
	return nil
}

func (f *fakeBot) DeleteActivity(_ any, dataType int) error {
	if f.gameErr != nil {
		return f.gameErr
	}
	f.deletedTypes = append(f.deletedTypes, dataType)
	return nil
}

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

func (a botAdapter) SendPanelCard(ctx context.Context, panel *store.Panel, buttonText string) (string, error) {
	return a.inner.SendPanelCard(ctx, panel, buttonText)
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

func (a botAdapter) GameList(ctx context.Context, gameType int) ([]kook.Game, error) {
	return a.inner.GameList(ctx, gameType)
}

func (a botAdapter) GameCreate(ctx context.Context, name, icon string) (*kook.Game, error) {
	return a.inner.GameCreate(ctx, name, icon)
}

func (a botAdapter) GameUpdate(ctx context.Context, id int64, name, icon string) (*kook.Game, error) {
	return a.inner.GameUpdate(ctx, id, name, icon)
}

func (a botAdapter) GameDelete(ctx context.Context, id int64) error {
	return a.inner.GameDelete(ctx, id)
}

func (a botAdapter) StartGameActivity(ctx context.Context, gameID int64) error {
	return a.inner.StartGameActivity(ctx, gameID)
}

func (a botAdapter) StartMusicActivity(ctx context.Context, musicName, singer, software string) error {
	return a.inner.StartMusicActivity(ctx, musicName, singer, software)
}

func (a botAdapter) DeleteActivity(ctx context.Context, dataType int) error {
	return a.inner.DeleteActivity(ctx, dataType)
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

// seedTicketType 创建测试用工单类型（面板必须挂在类型下）。
func (e *testEnv) seedTicketType(t *testing.T, name string) *store.TicketType {
	t.Helper()
	item := &store.TicketType{Name: name, Enabled: true}
	if err := e.store.Types.Create(item); err != nil {
		t.Fatalf("创建工单类型失败: %v", err)
	}
	return item
}

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
	ticketType := env.seedTicketType(t, "账号与充值")

	// 创建面板
	res := env.do(t, http.MethodPost, "/api/v1/panels", map[string]any{
		"typeId": ticketType.ID, "channelId": "30001", "title": "点我开单", "buttonText": "开单", "openMessage": "请提供订单号",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusCreated {
		t.Fatalf("创建面板失败: %d %s", res.status, res.raw)
	}
	if res.body["typeId"] != float64(ticketType.ID) {
		t.Fatalf("面板应记录所属类型，得到 %v", res.body["typeId"])
	}
	if len(fake.panels) != 1 || fake.panels[0] != "30001" {
		t.Fatalf("应向频道 3001 发送面板卡片，实际 %v", fake.panels)
	}
	panelID := uint(res.body["id"].(float64))
	if res.body["channelName"] != "工单面板" {
		t.Fatalf("应记录频道名，得到 %v", res.body["channelName"])
	}
	if res.body["openMessage"] != "请提供订单号" {
		t.Fatalf("开单提示应被保存，得到 %v", res.body["openMessage"])
	}

	// 非法参数：分组不能作为面板
	res = env.do(t, http.MethodPost, "/api/v1/panels", map[string]any{"typeId": ticketType.ID, "channelId": "30002"},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("分组频道应被拒绝，得到 %d", res.status)
	}
	res = env.do(t, http.MethodPost, "/api/v1/panels", map[string]any{"typeId": ticketType.ID, "channelId": "not-a-id"},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("非法频道 ID 应被拒绝，得到 %d", res.status)
	}
	res = env.do(t, http.MethodPost, "/api/v1/panels", map[string]any{"typeId": ticketType.ID, "channelId": "9999"},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("不存在的频道应被拒绝，得到 %d", res.status)
	}
	// 不存在的工单类型应被拒绝
	res = env.do(t, http.MethodPost, "/api/v1/panels", map[string]any{"typeId": 999, "channelId": "30001"},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("不存在的类型应被拒绝，得到 %d %s", res.status, res.raw)
	}

	// 更新（禁用并改文案、按钮文字与开单提示）
	disabled := false
	res = env.do(t, http.MethodPatch, fmt.Sprintf("/api/v1/panels/%d", panelID), map[string]any{
		"enabled": disabled, "title": "新文案", "buttonText": "联系客服", "openMessage": "  新提示  ",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK || res.body["enabled"] != false {
		t.Fatalf("更新面板失败: %d %s", res.status, res.raw)
	}
	if res.body["buttonText"] != "联系客服" {
		t.Fatalf("按钮文字应被保存，得到 %v", res.body["buttonText"])
	}
	if res.body["openMessage"] != "新提示" {
		t.Fatalf("开单提示应被保存并去除首尾空白，得到 %v", res.body["openMessage"])
	}

	// 类型角色：真实角色应被接受并补全名称
	res = env.do(t, http.MethodPost, fmt.Sprintf("/api/v1/types/%d/roles", ticketType.ID), map[string]any{
		"roleId": "10003",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusCreated {
		t.Fatalf("添加类型角色失败: %d %s", res.status, res.raw)
	}
	stored, err := env.store.Types.ByID(ticketType.ID)
	if err != nil {
		t.Fatalf("读取工单类型失败: %v", err)
	}
	if len(stored.Roles) != 1 || stored.Roles[0].RoleName != "实习客服" {
		t.Fatalf("类型角色记录异常: %+v", stored.Roles)
	}
	// 不存在的角色应被拒绝
	res = env.do(t, http.MethodPost, fmt.Sprintf("/api/v1/types/%d/roles", ticketType.ID), map[string]any{
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
	// 重建时必须沿用面板记录里的按钮文字，不能回退成默认的 ticket。
	if len(fake.buttonTexts) != 2 || fake.buttonTexts[1] != "联系客服" {
		t.Fatalf("重建应保留自定义按钮文字，实际 %v", fake.buttonTexts)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != "msg-1" {
		t.Fatalf("重建应删除旧卡片，实际 %v", fake.deleted)
	}

	// 删除角色
	res = env.do(t, http.MethodDelete, fmt.Sprintf("/api/v1/types/%d/roles/10003", ticketType.ID), nil,
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("删除类型角色失败: %d", res.status)
	}
	stored, _ = env.store.Types.ByID(ticketType.ID)
	if len(stored.Roles) != 0 {
		t.Fatalf("类型角色应被移除，剩余 %+v", stored.Roles)
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

func TestPanelCreateAllowsMultiplePerChannel(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)
	fake := env.withFakeBot(t)
	ticketType := env.seedTicketType(t, "账号与充值")

	for i := 0; i < 2; i++ {
		res := env.do(t, http.MethodPost, "/api/v1/panels", map[string]any{
			"typeId": ticketType.ID, "channelId": "30001", "title": fmt.Sprintf("面板 %d", i+1), "buttonText": "开单",
		}, withCookie(cookie), withCSRF(csrf))
		if res.status != http.StatusCreated {
			t.Fatalf("创建第 %d 张面板失败: %d %s", i+1, res.status, res.raw)
		}
	}
	if len(fake.panels) != 2 {
		t.Fatalf("应在同一频道发送 2 张卡片，实际 %v", fake.panels)
	}
	panels, err := env.store.Panels.ListByChannel("30001")
	if err != nil {
		t.Fatalf("查询面板失败: %v", err)
	}
	if len(panels) != 2 {
		t.Fatalf("同一频道应保留 2 条面板记录，实际 %d 条", len(panels))
	}
}

// TestPanelOpenMessageValidation 验证“开单后发送内容”的长度限制与清空，
// 并确保单独更新该字段不会触发卡片重建（不重发、不删卡）。
func TestPanelOpenMessageValidation(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)
	fake := env.withFakeBot(t)
	ticketType := env.seedTicketType(t, "账号与充值")

	// 超长内容应被拒绝
	tooLong := strings.Repeat("啊", maxPanelOpenMessageLength+1)
	res := env.do(t, http.MethodPost, "/api/v1/panels", map[string]any{
		"typeId": ticketType.ID, "channelId": "30001", "openMessage": tooLong,
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("超长开单提示应被拒绝，得到 %d %s", res.status, res.raw)
	}

	// 创建时允许留空
	res = env.do(t, http.MethodPost, "/api/v1/panels", map[string]any{
		"typeId": ticketType.ID, "channelId": "30001",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusCreated {
		t.Fatalf("创建面板失败: %d %s", res.status, res.raw)
	}
	panelID := uint(res.body["id"].(float64))
	if res.body["openMessage"] != "" {
		t.Fatalf("未配置时开单提示应为空，得到 %v", res.body["openMessage"])
	}
	if len(fake.panels) != 1 {
		t.Fatalf("创建面板应只发送一次卡片，实际 %v", fake.panels)
	}
	stored, err := env.store.Panels.ByID(panelID)
	if err != nil {
		t.Fatalf("读取面板失败: %v", err)
	}
	originalMsgID := stored.MsgID

	// 可单独更新（只写入数据库：不应重建卡片、不应删除原消息）
	res = env.do(t, http.MethodPatch, fmt.Sprintf("/api/v1/panels/%d", panelID), map[string]any{
		"openMessage": "注意：请勿泄露密码",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK || res.body["openMessage"] != "注意：请勿泄露密码" {
		t.Fatalf("开单提示应可单独更新: %d %s", res.status, res.raw)
	}
	if len(fake.panels) != 1 || len(fake.deleted) != 0 {
		t.Fatalf("仅更新开单提示不应重建卡片，实际发送 %v 删除 %v", fake.panels, fake.deleted)
	}
	stored, _ = env.store.Panels.ByID(panelID)
	if stored.MsgID != originalMsgID {
		t.Fatalf("仅更新开单提示不应改变消息 ID，原 %s 现 %s", originalMsgID, stored.MsgID)
	}

	// 可清空
	res = env.do(t, http.MethodPatch, fmt.Sprintf("/api/v1/panels/%d", panelID), map[string]any{
		"openMessage": "   ",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK || res.body["openMessage"] != "" {
		t.Fatalf("开单提示应可清空: %d %s", res.status, res.raw)
	}
	if len(fake.panels) != 1 || len(fake.deleted) != 0 {
		t.Fatalf("清空开单提示不应重建卡片，实际发送 %v 删除 %v", fake.panels, fake.deleted)
	}
}

func TestPanelManagementRequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "staff", staffPassword, store.RoleStaff, false)
	cookie, csrf := env.login(t, "staff", staffPassword)
	env.withFakeBot(t)
	ticketType := env.seedTicketType(t, "账号与充值")

	res := env.do(t, http.MethodPost, "/api/v1/panels", map[string]any{"typeId": ticketType.ID, "channelId": "30001"},
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

	// 样例按「本地自然日」摆放，而不是相对 now 的偏移：
	// 在凌晨（本地时间 0～3 点）跑测试时，now-3h 这类时间会落到昨天，
	// 让「今日/昨日」断言随机失败。这里以今天零点为基准，偏移量固定。
	localNow := now.In(loc)
	dayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
	at := func(offset time.Duration) time.Time { return dayStart.Add(offset) }

	// 构造 4 个工单：3 个已关闭（两个由“客服小林”关闭）、1 个进行中；
	// 分属两个工单类型，用于验证按类型聚合。
	accountType := env.seedTicketType(t, "账号与充值")
	reportType := env.seedTicketType(t, "举报与投诉")
	type seedSpec struct {
		status       string
		startedAt    time.Time
		closedAt     *time.Time
		firstReplyAt *time.Time
		closedBy     string
		source       string
		ticketType   *store.TicketType
	}
	closedAt1 := at(25 * time.Minute)
	closedAt2 := at(40 * time.Minute)
	closedAt3 := at(-1 * time.Hour)
	replyAt := at(15 * time.Minute)
	specs := []seedSpec{
		{store.TicketClosed, at(5 * time.Minute), &closedAt1, &replyAt, "客服小林", "30001", accountType},
		{store.TicketClosed, at(30 * time.Minute), &closedAt2, nil, "客服小林", "30001", accountType},
		{store.TicketClosed, at(-2 * time.Hour), &closedAt3, nil, "客服小张", "30002", reportType},
		{store.TicketOpen, at(50 * time.Minute), nil, nil, "", "30001", accountType},
	}

	for i, spec := range specs {
		ticket := &store.Ticket{
			UserID:          fmt.Sprintf("900%d", i),
			UserName:        fmt.Sprintf("用户%d", i),
			SourceChannelID: spec.source,
			ChannelID:       fmt.Sprintf("chan-%d", i),
			Status:          spec.status,
			StartedAt:       spec.startedAt,
			TypeID:          &spec.ticketType.ID,
			TypeName:        spec.ticketType.Name,
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

	// 类型分布：账号与充值 3 单、举报与投诉 1 单
	typeStats := res.body["types"].([]any)
	if len(typeStats) != 2 {
		t.Fatalf("应统计 2 个工单类型，得到 %d", len(typeStats))
	}
	topType := typeStats[0].(map[string]any)
	if topType["typeName"] != "账号与充值" || topType["opened"].(float64) != 3 {
		t.Fatalf("工单类型排行异常: %+v", topType)
	}
	if topType["closed"].(float64) != 2 {
		t.Fatalf("账号与充值应已关闭 2 单，得到 %v", topType["closed"])
	}
	if typeStats[1].(map[string]any)["typeName"] != "举报与投诉" {
		t.Fatalf("第二个类型应为举报与投诉: %+v", typeStats[1])
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

// ---------------------------------------------------------------------------
// 工单类型
// ---------------------------------------------------------------------------

func TestTicketTypeCRUDAndRoles(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)
	env.withFakeBot(t)

	// 创建：名称折叠空白，默认启用
	res := env.do(t, http.MethodPost, "/api/v1/types", map[string]any{
		"name": "  账号 与  充值 ", "description": "订单与账号问题",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusCreated {
		t.Fatalf("创建工单类型失败: %d %s", res.status, res.raw)
	}
	typeID := uint(res.body["id"].(float64))
	if res.body["name"] != "账号 与 充值" {
		t.Fatalf("类型名称应折叠多余空白，得到 %v", res.body["name"])
	}
	if res.body["description"] != "订单与账号问题" {
		t.Fatalf("类型备注应被保存，得到 %v", res.body["description"])
	}
	if res.body["enabled"] != true {
		t.Fatalf("新建类型应默认启用，得到 %v", res.body["enabled"])
	}

	// 同名冲突与空名称应被拒绝
	res = env.do(t, http.MethodPost, "/api/v1/types", map[string]any{"name": "账号 与 充值"},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusConflict {
		t.Fatalf("同名类型应返回 409，得到 %d %s", res.status, res.raw)
	}
	res = env.do(t, http.MethodPost, "/api/v1/types", map[string]any{"name": "   "},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("空名称应返回 400，得到 %d", res.status)
	}

	// 列表
	res = env.do(t, http.MethodGet, "/api/v1/types", nil, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK || len(res.body["items"].([]any)) != 1 {
		t.Fatalf("类型列表异常: %d %s", res.status, res.raw)
	}

	// 类型角色：真实角色应被接受并补全名称
	res = env.do(t, http.MethodPost, fmt.Sprintf("/api/v1/types/%d/roles", typeID), map[string]any{
		"roleId": "10003",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusCreated {
		t.Fatalf("添加类型角色失败: %d %s", res.status, res.raw)
	}
	stored, err := env.store.Types.ByID(typeID)
	if err != nil {
		t.Fatalf("读取类型失败: %v", err)
	}
	if len(stored.Roles) != 1 || stored.Roles[0].RoleName != "实习客服" {
		t.Fatalf("类型角色记录异常: %+v", stored.Roles)
	}
	// 不存在的角色应被拒绝
	res = env.do(t, http.MethodPost, fmt.Sprintf("/api/v1/types/%d/roles", typeID), map[string]any{
		"roleId": "8888",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("不存在的角色应被拒绝，得到 %d", res.status)
	}

	// 更新：改名并停用
	res = env.do(t, http.MethodPatch, fmt.Sprintf("/api/v1/types/%d", typeID), map[string]any{
		"name": "账号问题", "enabled": false,
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK || res.body["enabled"] != false || res.body["name"] != "账号问题" {
		t.Fatalf("更新类型失败: %d %s", res.status, res.raw)
	}

	// 删除角色
	res = env.do(t, http.MethodDelete, fmt.Sprintf("/api/v1/types/%d/roles/10003", typeID), nil,
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("删除类型角色失败: %d", res.status)
	}
	stored, _ = env.store.Types.ByID(typeID)
	if len(stored.Roles) != 0 {
		t.Fatalf("类型角色应被移除，剩余 %+v", stored.Roles)
	}

	// 删除类型
	res = env.do(t, http.MethodDelete, fmt.Sprintf("/api/v1/types/%d", typeID), nil,
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("删除类型失败: %d %s", res.status, res.raw)
	}
	if _, err := env.store.Types.ByID(typeID); err == nil {
		t.Fatal("类型应被删除")
	}
	for _, action := range []string{"type.create", "type.update", "type.delete"} {
		if env.auditCount(t, action) == 0 {
			t.Fatalf("%s 应写入审计", action)
		}
	}
}

// TestPanelMovesBetweenTypesAndTypeDeleteIsBlocked 验证：
// 面板可以改属到其它类型；类型下仍有面板时不允许删除。
func TestPanelMovesBetweenTypesAndTypeDeleteIsBlocked(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)
	fake := env.withFakeBot(t)

	first := env.seedTicketType(t, "账号与充值")
	second := env.seedTicketType(t, "举报与投诉")

	res := env.do(t, http.MethodPost, "/api/v1/panels", map[string]any{
		"typeId": first.ID, "channelId": "30001", "title": "开单",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusCreated {
		t.Fatalf("创建面板失败: %d %s", res.status, res.raw)
	}
	panelID := uint(res.body["id"].(float64))

	// 类型下仍有面板：拒绝删除并给出可读提示
	res = env.do(t, http.MethodDelete, fmt.Sprintf("/api/v1/types/%d", first.ID), nil,
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("类型下仍有面板时应拒绝删除，得到 %d %s", res.status, res.raw)
	}
	apiError, _ := res.body["error"].(map[string]any)
	if apiError["code"] != "type_in_use" {
		t.Fatalf("错误码应为 type_in_use，得到 %v", apiError)
	}

	// 改属到另一个类型：立即生效且无需重建卡片
	res = env.do(t, http.MethodPatch, fmt.Sprintf("/api/v1/panels/%d", panelID), map[string]any{
		"typeId": second.ID,
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK || res.body["typeId"] != float64(second.ID) {
		t.Fatalf("面板改属失败: %d %s", res.status, res.raw)
	}
	if len(fake.panels) != 1 || len(fake.deleted) != 0 {
		t.Fatalf("改属类型不应重建卡片，实际发送 %v 删除 %v", fake.panels, fake.deleted)
	}

	// 改属后原类型可删除
	res = env.do(t, http.MethodDelete, fmt.Sprintf("/api/v1/types/%d", first.ID), nil,
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("改属后应可删除原类型: %d %s", res.status, res.raw)
	}
	// 面板仍然可用并挂在新类型下
	stored, err := env.store.Panels.ByID(panelID)
	if err != nil || stored.TypeID != second.ID {
		t.Fatalf("面板应仍挂在新类型下: %+v（err=%v）", stored, err)
	}
}

// TestTicketListFiltersByType 验证工单列表支持按工单类型筛选。
func TestTicketListFiltersByType(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)

	accountType := env.seedTicketType(t, "账号与充值")
	reportType := env.seedTicketType(t, "举报与投诉")
	seed := func(item *store.TicketType, name string) {
		t.Helper()
		ticket := &store.Ticket{
			UserID: "9001", UserName: name, ChannelID: "chan-" + name,
			Status: store.TicketOpen, TypeID: &item.ID, TypeName: item.Name,
		}
		if err := env.store.Tickets.CreateWithNo(ticket, store.Now(), env.config.Location); err != nil {
			t.Fatalf("创建工单失败: %v", err)
		}
	}
	seed(accountType, "小明")
	seed(reportType, "李雷")

	res := env.do(t, http.MethodGet, "/api/v1/tickets", nil, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK || res.body["total"].(float64) != 2 {
		t.Fatalf("未筛选时应返回 2 条工单: %d %s", res.status, res.raw)
	}

	res = env.do(t, http.MethodGet, fmt.Sprintf("/api/v1/tickets?typeId=%d", reportType.ID), nil,
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK || res.body["total"].(float64) != 1 {
		t.Fatalf("按类型筛选应返回 1 条工单: %d %s", res.status, res.raw)
	}
	items := res.body["items"].([]any)
	first := items[0].(map[string]any)
	if first["typeName"] != "举报与投诉" || first["userName"] != "李雷" {
		t.Fatalf("筛选结果异常: %+v", first)
	}

	// 非法参数应被拒绝
	res = env.do(t, http.MethodGet, "/api/v1/tickets?typeId=abc", nil, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("非法 typeId 应返回 400，得到 %d", res.status)
	}
}
