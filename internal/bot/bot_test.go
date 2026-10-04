package bot

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"vancekookticket/internal/eventbus"
	"vancekookticket/internal/kook"
	"vancekookticket/internal/kook/kooktest"
	"vancekookticket/internal/store"
	"vancekookticket/internal/ticket"
)

// ---------------------------------------------------------------------------
// 测试环境
// ---------------------------------------------------------------------------

const (
	testGuildID    = "5000"
	testCategory   = "cat-1"
	testPanelChan  = "panel-1"
	testLogChan    = "log-1"
	testDebugChan  = "debug-1"
	testToken      = "test-token-abcdefghijklmnop"
	roleAdmin      = int64(1002) // 客服组（全局管理员角色）
	rolePanel      = int64(1003) // 实习客服（面板管理员角色）
	roleColorA     = int64(2001)
	roleColorB     = int64(2002)
	userAsker      = "9001"
	userStaff      = "9002"
	userMaster     = "9003"
	userReadonlyKm = "9004"
)

type botEnv struct {
	t       *testing.T
	store   *store.Store
	bus     *eventbus.Bus
	svc     *ticket.Service
	bot     *Bot
	mock    *kooktest.Server
	loc     *time.Location
	stopCh  atomic.Bool
	panelID uint
}

func newBotEnv(t *testing.T) *botEnv {
	t.Helper()

	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载时区失败: %v", err)
	}

	st, err := store.Open(t.TempDir() + "/ticket.db")
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// 业务配置
	mustSet := func(key, value string) {
		if err := st.Settings.Set(key, value); err != nil {
			t.Fatalf("写入配置失败: %v", err)
		}
	}
	mustSet(store.SettingGuildID, testGuildID)
	mustSet(store.SettingCategoryID, testCategory)
	mustSet(store.SettingLogChannelID, testLogChan)
	mustSet(store.SettingDebugChannelID, testDebugChan)

	// 全局管理员角色：客服组
	if err := st.Roles.AddAdmin(fmt.Sprint(roleAdmin), "客服组"); err != nil {
		t.Fatalf("写入管理员角色失败: %v", err)
	}
	// 角色映射：客服组 → staff
	if err := st.Roles.UpsertMapping(&store.RoleMapping{
		KookRoleID: fmt.Sprint(roleAdmin), KookRoleName: "客服组", WebRole: store.RoleStaff,
	}); err != nil {
		t.Fatalf("写入角色映射失败: %v", err)
	}

	// 面板（含面板级管理员角色）
	seededPanel := &store.Panel{
		ChannelID: testPanelChan, ChannelName: "工单面板", MsgID: "msg-panel", Title: "点击按钮发起工单", Enabled: true,
	}
	if err := st.Panels.Create(seededPanel); err != nil {
		t.Fatalf("写入面板失败: %v", err)
	}
	if err := st.Panels.AddRole(seededPanel.ID, fmt.Sprint(rolePanel), "实习客服"); err != nil {
		t.Fatalf("写入面板角色失败: %v", err)
	}

	mock := kooktest.New()
	t.Cleanup(mock.Close)

	bus := eventbus.New()
	svc := ticket.NewService(st, bus, ticket.NewNoopPlatform(nil), loc, st.Settings.OutdateHours)

	env := &botEnv{t: t, store: st, bus: bus, svc: svc, mock: mock, loc: loc, panelID: seededPanel.ID}

	b, err := New(Deps{
		Store:     st,
		Bus:       bus,
		Tickets:   svc,
		Location:  loc,
		AppSecret: []byte("app-secret-for-bot-tests-0123456789"),
		Config: func(context.Context) (*Config, error) {
			return &Config{
				Token:          testToken,
				APIBase:        mock.BaseURL(),
				GuildID:        testGuildID,
				CategoryID:     testCategory,
				LogChannelID:   testLogChan,
				DebugChannelID: testDebugChan,
				OutdateHours:   48,
				// 测试环境放宽限速，避免每个用例都在令牌桶上排队
				RatePerSecond: 200,
				RateBurst:     200,
			}, nil
		},
		RequestStop:   func() { env.stopCh.Store(true) },
		HeartbeatTick: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("创建机器人失败: %v", err)
	}
	env.bot = b

	startCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := b.Start(startCtx); err != nil {
		t.Fatalf("启动机器人失败: %v", err)
	}
	t.Cleanup(b.Stop)

	// 等待网关连接建立
	env.waitFor("网关连接", func() bool { return b.Status().Connected })
	return env
}

func (e *botEnv) waitFor(description string, condition func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("等待超时：%s", description)
}

func (e *botEnv) user(id string) kook.User {
	e.mock.AddUser(e.mockUser(id))
	return e.mockUser(id)
}

func (e *botEnv) mockUser(id string) kook.User {
	switch id {
	case userAsker:
		return kook.User{ID: id, Username: "asker", Nickname: "提问用户", IdentifyNum: "0001"}
	case userStaff:
		return kook.User{ID: id, Username: "admin", Nickname: "客服小林", IdentifyNum: "0002", Roles: []int64{roleAdmin}}
	case userMaster:
		return kook.User{ID: id, Username: "master", Nickname: "服主", IdentifyNum: "0003", Roles: []int64{roleAdmin}}
	default:
		return kook.User{ID: id, Username: "helper", Nickname: "实习客服", IdentifyNum: "0004", Roles: []int64{rolePanel}}
	}
}

// openValue 生成合法签名的开单按钮值。
func (e *botEnv) openValue() string {
	return e.bot.encodeButton(actionOpen, "", testPanelChan, e.panelID)
}

// ticketValue 生成指定动作的按钮值。
func (e *botEnv) ticketValue(action, ticketNo, channelID string) string {
	return e.bot.encodeButton(action, ticketNo, channelID, 0)
}

// ticketChannel 返回当前唯一工单的频道 ID。
func (e *botEnv) ticketChannel() string {
	items, _, err := e.store.Tickets.List(store.TicketFilter{Page: 1, PageSize: 10})
	if err != nil || len(items) == 0 {
		return ""
	}
	return items[0].ChannelID
}

func (e *botEnv) firstTicket() *store.Ticket {
	items, _, err := e.store.Tickets.List(store.TicketFilter{Page: 1, PageSize: 10})
	if err != nil || len(items) == 0 {
		return nil
	}
	return &items[0]
}

// ---------------------------------------------------------------------------
// 开单
// ---------------------------------------------------------------------------

func TestOpenTicketCreatesChannelPermissionsAndCard(t *testing.T) {
	env := newBotEnv(t)

	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(testPanelChan, userAsker, env.openValue(), env.user(userAsker)))

	env.waitFor("工单创建完成", func() bool {
		ticket := env.firstTicket()
		return ticket != nil && ticket.Status == store.TicketOpen
	})

	opened := env.firstTicket()
	if !strings.HasPrefix(opened.No, "TK-") {
		t.Fatalf("工单编号格式异常: %s", opened.No)
	}
	if opened.UserID != userAsker {
		t.Fatalf("开单人记录错误: %s", opened.UserID)
	}

	// 频道应在配置的隐藏分组下创建，名称包含工单编号
	channels := env.mock.Channels()
	if len(channels) != 1 {
		t.Fatalf("应创建 1 个频道，实际 %d 个", len(channels))
	}
	var created kook.Channel
	for _, channel := range channels {
		created = channel
	}
	if !strings.Contains(created.Name, opened.No) {
		t.Fatalf("频道名应包含工单编号，实际: %s", created.Name)
	}
	if created.ParentID != testCategory {
		t.Fatalf("频道应创建在配置的分组下，实际: %s", created.ParentID)
	}

	// 权限：全局管理员角色、面板管理员角色、开单人各一次，且都是“可看可发”
	updates := env.mock.CallsOf("channel-role/update")
	if len(updates) < 3 {
		t.Fatalf("权限下发次数不足: %d", len(updates))
	}
	subjects := map[string]bool{}
	for _, call := range updates {
		allow := int(toFloat(call.Params["allow"]))
		deny := int(toFloat(call.Params["deny"]))
		if allow != kook.PermissionAllText || deny != 0 {
			t.Fatalf("开单时权限应为 allow=6144 deny=0，实际 allow=%d deny=%d", allow, deny)
		}
		subjects[fmt.Sprint(call.Params["type"])+":"+fmt.Sprint(call.Params["value"])] = true
	}
	for _, expected := range []string{
		"role_id:" + fmt.Sprint(roleAdmin),
		"role_id:" + fmt.Sprint(rolePanel),
		"user_id:" + userAsker,
	} {
		if !subjects[expected] {
			t.Fatalf("缺少权限下发目标: %s（实际 %v）", expected, subjects)
		}
	}

	// 工单卡片应发到新频道内
	var cardSent bool
	for _, call := range env.mock.CallsOf("message/create") {
		if fmt.Sprint(call.Params["target_id"]) == created.ID && int(toFloat(call.Params["type"])) == kook.MsgTypeCard {
			cardSent = true
			content := fmt.Sprint(call.Params["content"])
			if !strings.Contains(content, actionClose) || !strings.Contains(content, actionLock) {
				t.Fatalf("卡片应包含关闭与锁定按钮: %s", content)
			}
		}
	}
	if !cardSent {
		t.Fatal("未在新频道发送工单卡片")
	}

	// 开单系统消息应入库，便于记录页展示
	messages, err := env.store.Tickets.Messages(opened.No, 10, 0)
	if err != nil {
		t.Fatalf("查询消息失败: %v", err)
	}
	if len(messages) != 1 || !messages[0].IsBot {
		t.Fatalf("应写入 1 条开单系统消息，实际 %d 条", len(messages))
	}

	// 审计
	count, err := env.store.Audit.Count()
	if err != nil || count == 0 {
		t.Fatalf("应有审计记录: %v", err)
	}
}

// TestOpenTicketFromSecondPanelUsesItsOwnRoles 验证同一频道内多张面板卡片各自携带角色：
// 点击第二张卡片的按钮时，只应下发第二张面板的管理员角色。
func TestOpenTicketFromSecondPanelUsesItsOwnRoles(t *testing.T) {
	env := newBotEnv(t)

	const secondRole = "3003"
	second := &store.Panel{ChannelID: testPanelChan, ChannelName: "工单面板", Title: "第二张", Enabled: true}
	if err := env.store.Panels.Create(second); err != nil {
		t.Fatalf("创建第二张面板失败: %v", err)
	}
	if err := env.store.Panels.AddRole(second.ID, secondRole, "二线客服"); err != nil {
		t.Fatalf("写入第二张面板角色失败: %v", err)
	}

	value := env.bot.encodeButton(actionOpen, "", testPanelChan, second.ID)
	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(testPanelChan, userAsker, value, env.user(userAsker)))

	env.waitFor("工单创建完成", func() bool {
		ticket := env.firstTicket()
		return ticket != nil && ticket.Status == store.TicketOpen
	})

	subjects := map[string]bool{}
	for _, call := range env.mock.CallsOf("channel-role/update") {
		subjects[fmt.Sprint(call.Params["type"])+":"+fmt.Sprint(call.Params["value"])] = true
	}
	if !subjects["role_id:"+secondRole] {
		t.Fatalf("应下发第二张面板的角色 %s，实际 %v", secondRole, subjects)
	}
	if subjects["role_id:"+fmt.Sprint(rolePanel)] {
		t.Fatalf("不应下发第一张面板的角色，实际 %v", subjects)
	}

	opened := env.firstTicket()
	if opened.PanelID == nil || *opened.PanelID != second.ID {
		t.Fatalf("工单应关联第二张面板 %d，实际 %v", second.ID, opened.PanelID)
	}
}

// TestLegacyOpenButtonStillWorks 验证升级前发出的按钮（不含面板 ID、旧签名）仍可开单。
func TestLegacyOpenButtonStillWorks(t *testing.T) {
	env := newBotEnv(t)

	legacy := fmt.Sprintf(`{"a":%q,"s":%q}`, actionOpen, env.bot.legacySignature(actionOpen, "", testPanelChan))
	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(testPanelChan, userAsker, legacy, env.user(userAsker)))

	env.waitFor("工单创建完成", func() bool {
		ticket := env.firstTicket()
		return ticket != nil && ticket.Status == store.TicketOpen
	})

	// 回退路径下应关联频道内最早创建的面板。
	opened := env.firstTicket()
	if opened.PanelID == nil || *opened.PanelID != env.panelID {
		t.Fatalf("旧按钮应回退到频道首个面板 %d，实际 %v", env.panelID, opened.PanelID)
	}
}

func TestOneActiveTicketPerUser(t *testing.T) {
	env := newBotEnv(t)

	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(testPanelChan, userAsker, env.openValue(), env.user(userAsker)))
	env.waitFor("首个工单创建", func() bool {
		ticket := env.firstTicket()
		return ticket != nil && ticket.Status == store.TicketOpen
	})
	first := env.firstTicket()

	// 再次点击开单：应被拒绝并给出临时提示
	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(testPanelChan, userAsker, env.openValue(), env.user(userAsker)))
	env.waitFor("重复开单提示", func() bool {
		for _, call := range env.mock.CallsOf("message/create") {
			if fmt.Sprint(call.Params["temp_target_id"]) == userAsker &&
				strings.Contains(fmt.Sprint(call.Params["content"]), "已有") {
				return true
			}
		}
		return false
	})

	items, total, err := env.store.Tickets.List(store.TicketFilter{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("查询工单失败: %v", err)
	}
	if total != 1 {
		t.Fatalf("同一用户不应创建第二个工单，当前 %d 个", total)
	}
	if items[0].No != first.No {
		t.Fatal("工单记录被意外替换")
	}
}

func TestOpenTicketWhenPrivateMessageBlockedStillCreatesTicket(t *testing.T) {
	env := newBotEnv(t)
	env.mock.DMBlocked = true

	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(testPanelChan, userAsker, env.openValue(), env.user(userAsker)))

	env.waitFor("开单完成", func() bool {
		ticket := env.firstTicket()
		return ticket != nil && ticket.Status == store.TicketOpen
	})

	// 应提示用户先开启私聊
	var noticed bool
	for _, call := range env.mock.CallsOf("message/create") {
		if fmt.Sprint(call.Params["temp_target_id"]) == userAsker &&
			strings.Contains(fmt.Sprint(call.Params["content"]), "私聊") {
			noticed = true
		}
	}
	if !noticed {
		t.Fatal("私信被屏蔽时应提示用户开启私聊")
	}
}

func TestOpenTicketFailureRecyclesTicketNumber(t *testing.T) {
	env := newBotEnv(t)
	env.mock.ChannelCreateFail = true

	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(testPanelChan, userAsker, env.openValue(), env.user(userAsker)))

	// 应通知调试频道
	env.waitFor("调试频道收到错误提示", func() bool {
		for _, call := range env.mock.CallsOf("message/create") {
			if fmt.Sprint(call.Params["target_id"]) == testDebugChan {
				return true
			}
		}
		return false
	})

	// 占号应被回收：库中不应留下任何工单
	_, total, err := env.store.Tickets.List(store.TicketFilter{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("查询工单失败: %v", err)
	}
	if total != 0 {
		t.Fatalf("开单失败后应回收编号，当前仍有 %d 条记录", total)
	}
}

// ---------------------------------------------------------------------------
// 消息归档
// ---------------------------------------------------------------------------

func TestMessagesAreArchivedWithTypeMapping(t *testing.T) {
	env := newBotEnv(t)
	channelID := env.openTicketForTest()

	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(channelID, userAsker, "充值没有到账", env.user(userAsker)))
	env.mock.Push(kook.EventTypeImage, kooktest.ImageMessageEvent(channelID, userAsker, "https://img.example/a.png", env.user(userAsker)))

	ticket := env.firstTicket()
	env.waitFor("消息归档", func() bool {
		messages, err := env.store.Tickets.Messages(ticket.No, 20, 0)
		return err == nil && len(messages) == 3
	})

	messages, _ := env.store.Tickets.Messages(ticket.No, 20, 0)
	if messages[1].MsgType != store.MsgTypeText || messages[1].Content != "充值没有到账" {
		t.Fatalf("文本消息归档异常: %+v", messages[1])
	}
	if messages[2].MsgType != store.MsgTypeImage || !strings.Contains(messages[2].Content, "https://img.example/a.png") {
		t.Fatalf("图片消息归档异常: %+v", messages[2])
	}

	// 机器人消息不应入库（避免通知卡片污染记录）
	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(channelID, userAsker, "另一条", kook.User{
		ID: userAsker, Username: "asker", Nickname: "提问用户", Bot: true,
	}))
	time.Sleep(200 * time.Millisecond)
	messages, _ = env.store.Tickets.Messages(ticket.No, 20, 0)
	if len(messages) != 3 {
		t.Fatalf("机器人消息不应被归档，当前 %d 条", len(messages))
	}

	// 关键词可检索到归档内容
	_, total, err := env.store.Tickets.List(store.TicketFilter{Query: "充值"})
	if err != nil || total != 1 {
		t.Fatalf("按聊天内容检索失败: total=%d err=%v", total, err)
	}
}

func TestMessagesFromOtherGuildAreIgnored(t *testing.T) {
	env := newBotEnv(t)
	channelID := env.openTicketForTest()

	event := kooktest.TextMessageEvent(channelID, userAsker, "来自其它服务器的消息", env.user(userAsker))
	event["extra"].(map[string]any)["guild_id"] = "9999"
	env.mock.Push(kook.EventTypeText, event)

	time.Sleep(300 * time.Millisecond)
	ticket := env.firstTicket()
	messages, _ := env.store.Tickets.Messages(ticket.No, 20, 0)
	if len(messages) != 1 {
		t.Fatalf("非配置服务器的消息不应入库，当前 %d 条", len(messages))
	}
}

// TestRealPlatformMessageShapeIsArchived 是回归测试：
//
// 真实平台的普通消息事件中，extra.type 是数字、用户对象位于 extra.author
// 且没有顶层 author。早期实现按字符串解析 extra.type 会让整条事件解析失败，
// 从而所有聊天记录都不入库。
func TestRealPlatformMessageShapeIsArchived(t *testing.T) {
	env := newBotEnv(t)
	channelID := env.openTicketForTest()

	env.mock.Push(kook.EventTypeText, map[string]any{
		"channel_type":  kook.ChannelTypeGroup,
		"type":          kook.EventTypeText,
		"target_id":     channelID,
		"author_id":     userAsker,
		"content":       "真实平台结构消息",
		"msg_id":        "msg-real-shape-1",
		"msg_timestamp": time.Now().UnixMilli(),
		"extra": map[string]any{
			"type":         1, // 数字：普通消息事件
			"guild_id":     testGuildID,
			"channel_name": "工单频道",
			"author": map[string]any{
				"id": userAsker, "username": "asker", "nickname": "提问用户", "identify_num": "0001",
			},
		},
	})

	ticket := env.firstTicket()
	env.waitFor("真实结构消息归档", func() bool {
		messages, err := env.store.Tickets.Messages(ticket.No, 20, 0)
		return err == nil && len(messages) == 2
	})

	messages, _ := env.store.Tickets.Messages(ticket.No, 20, 0)
	archived := messages[1]
	if archived.Content != "真实平台结构消息" || archived.MsgType != store.MsgTypeText {
		t.Fatalf("消息内容归档异常: %+v", archived)
	}
	// 顶层没有 author，昵称必须从 extra.author 回退得到。
	if archived.UserName != "提问用户#0001" {
		t.Fatalf("归档用户名异常（extra.author 回退失败）: %q", archived.UserName)
	}
}

// ---------------------------------------------------------------------------
// 关闭
// ---------------------------------------------------------------------------

func TestCloseTicketNotifiesAndDeletesChannel(t *testing.T) {
	env := newBotEnv(t)
	channelID := env.openTicketForTest()
	ticket := env.firstTicket()

	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(
		channelID, userStaff, env.ticketValue(actionClose, ticket.No, channelID), env.user(userStaff)))

	env.waitFor("工单关闭", func() bool {
		updated, err := env.store.Tickets.ByNo(ticket.No)
		return err == nil && updated.Status == store.TicketClosed
	})

	closed, err := env.store.Tickets.ByNo(ticket.No)
	if err != nil {
		t.Fatalf("读取工单失败: %v", err)
	}
	if closed.ClosedBy != userStaff {
		t.Fatalf("关闭人记录错误: %s", closed.ClosedBy)
	}
	if closed.ClosedAt == nil {
		t.Fatal("缺少关闭时间")
	}

	// 频道应被删除
	if _, exists := env.mock.Channels()[channelID]; exists {
		t.Fatal("关闭后应删除工单频道")
	}

	// 日志频道与开单人私聊各收到一条卡片
	var logNotified, userNotified bool
	for _, call := range env.mock.CallsOf("message/create") {
		if fmt.Sprint(call.Params["target_id"]) == testLogChan {
			logNotified = true
		}
	}
	for _, call := range env.mock.CallsOf("direct-message/create") {
		if fmt.Sprint(call.Params["target_id"]) == userAsker &&
			strings.Contains(fmt.Sprint(call.Params["content"]), ticket.No) {
			userNotified = true
		}
	}
	if !logNotified {
		t.Fatal("应在日志频道发送关闭通知")
	}
	if !userNotified {
		t.Fatal("应私聊通知开单人")
	}

	// 记录日志卡片消息 ID，供 /tkcm 更新
	if closed.LogChannelMsgID == "" {
		t.Fatal("应记录日志卡片消息 ID")
	}
}

func TestNonAdminCannotCloseOrLock(t *testing.T) {
	env := newBotEnv(t)
	channelID := env.openTicketForTest()
	ticket := env.firstTicket()

	// 开单人（无任何角色）尝试关闭
	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(
		channelID, userAsker, env.ticketValue(actionClose, ticket.No, channelID), env.user(userAsker)))

	env.waitFor("越权提示", func() bool {
		for _, call := range env.mock.CallsOf("message/create") {
			if fmt.Sprint(call.Params["temp_target_id"]) == userAsker &&
				strings.Contains(fmt.Sprint(call.Params["content"]), "管理员") {
				return true
			}
		}
		return false
	})

	current, err := env.store.Tickets.ByNo(ticket.No)
	if err != nil {
		t.Fatalf("读取工单失败: %v", err)
	}
	if current.Status != store.TicketOpen {
		t.Fatalf("越权关闭不应改变状态，当前 %s", current.Status)
	}
	if _, exists := env.mock.Channels()[channelID]; !exists {
		t.Fatal("越权关闭不应删除频道")
	}

	// 锁定同样应被拒绝
	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(
		channelID, userAsker, env.ticketValue(actionLock, ticket.No, channelID), env.user(userAsker)))
	time.Sleep(300 * time.Millisecond)

	current, _ = env.store.Tickets.ByNo(ticket.No)
	if current.Status != store.TicketOpen {
		t.Fatalf("越权锁定不应改变状态，当前 %s", current.Status)
	}
}

func TestForgedButtonValueIsRejected(t *testing.T) {
	env := newBotEnv(t)
	channelID := env.openTicketForTest()
	ticket := env.firstTicket()

	forgedValues := []string{
		fmt.Sprintf(`{"a":"%s","n":"%s","s":"deadbeef"}`, actionClose, ticket.No),
		fmt.Sprintf(`{"a":"%s","n":"%s"}`, actionClose, ticket.No),
		`not-json`,
		// 用其它频道签名的值（重放到本频道）
		env.ticketValue(actionClose, ticket.No, "another-channel"),
	}

	for _, value := range forgedValues {
		env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(channelID, userStaff, value, env.user(userStaff)))
	}
	time.Sleep(400 * time.Millisecond)

	current, err := env.store.Tickets.ByNo(ticket.No)
	if err != nil {
		t.Fatalf("读取工单失败: %v", err)
	}
	if current.Status != store.TicketOpen {
		t.Fatalf("伪造按钮不应生效，当前状态 %s", current.Status)
	}
	if _, exists := env.mock.Channels()[channelID]; !exists {
		t.Fatal("伪造按钮不应删除频道")
	}
}

// ---------------------------------------------------------------------------
// 锁定与重新激活
// ---------------------------------------------------------------------------

func TestLockAndReopenAdjustPermissions(t *testing.T) {
	env := newBotEnv(t)
	channelID := env.openTicketForTest()
	ticket := env.firstTicket()

	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(
		channelID, userStaff, env.ticketValue(actionLock, ticket.No, channelID), env.user(userStaff)))
	env.waitFor("工单锁定", func() bool {
		updated, err := env.store.Tickets.ByNo(ticket.No)
		return err == nil && updated.Status == store.TicketLocked
	})

	locked, _ := env.store.Tickets.ByNo(ticket.No)
	if locked.LockReason != store.LockReasonManual {
		t.Fatalf("锁定原因应为 manual，实际 %s", locked.LockReason)
	}

	// 锁定后：开单人可看不可发（allow=2048, deny=4096）
	var lockApplied bool
	for _, call := range env.mock.CallsOf("channel-role/update") {
		if fmt.Sprint(call.Params["value"]) == userAsker &&
			int(toFloat(call.Params["allow"])) == kook.PermissionViewChannel &&
			int(toFloat(call.Params["deny"])) == kook.PermissionSendMessage {
			lockApplied = true
		}
	}
	if !lockApplied {
		t.Fatal("锁定时应下发 allow=2048 deny=4096")
	}

	// 重新激活
	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(
		channelID, userStaff, env.ticketValue(actionReopen, ticket.No, channelID), env.user(userStaff)))
	env.waitFor("工单重新激活", func() bool {
		updated, err := env.store.Tickets.ByNo(ticket.No)
		return err == nil && updated.Status == store.TicketOpen
	})

	reopened, _ := env.store.Tickets.ByNo(ticket.No)
	if reopened.LockReason != "" {
		t.Fatalf("重新激活后应清空锁定原因，实际 %s", reopened.LockReason)
	}
}

func TestTimeoutScanLocksIdleTicket(t *testing.T) {
	env := newBotEnv(t)
	channelID := env.openTicketForTest()
	ticket := env.firstTicket()

	// 把工单的最近活动时间改到 72 小时前
	stale := store.Now().Add(-72 * time.Hour)
	if err := env.store.Tickets.UpdateFields(ticket.No, map[string]any{"updated_at": stale}); err != nil {
		t.Fatalf("更新时间失败: %v", err)
	}

	locked, err := env.svc.ScanTimeout(context.Background())
	if err != nil {
		t.Fatalf("超时扫描失败: %v", err)
	}
	if len(locked) != 1 || locked[0] != ticket.No {
		t.Fatalf("应锁定 1 个工单，实际 %v", locked)
	}

	updated, _ := env.store.Tickets.ByNo(ticket.No)
	if updated.Status != store.TicketLocked || updated.LockReason != store.LockReasonTimeout {
		t.Fatalf("超时锁定结果异常: status=%s reason=%s", updated.Status, updated.LockReason)
	}

	// 应在工单频道内发送锁定提示（含重新激活按钮）
	var noticed bool
	for _, call := range env.mock.CallsOf("message/create") {
		if fmt.Sprint(call.Params["target_id"]) == channelID &&
			strings.Contains(fmt.Sprint(call.Params["content"]), actionReopen) {
			noticed = true
		}
	}
	if !noticed {
		t.Fatal("超时锁定应发送提示卡片")
	}
}

// ---------------------------------------------------------------------------
// 命令
// ---------------------------------------------------------------------------

func TestPanelCommandCreatesPanel(t *testing.T) {
	env := newBotEnv(t)
	newChannel := "chan-panel-new"

	// 非管理员执行 → 被拒绝
	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(newChannel, userAsker, "/ticket", env.user(userAsker)))
	time.Sleep(250 * time.Millisecond)
	if _, err := env.store.Panels.ByChannel(newChannel); err == nil {
		t.Fatal("非管理员不应创建面板")
	}

	// 管理员执行 → 创建面板
	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(newChannel, userStaff, "/ticket", env.user(userStaff)))
	env.waitFor("面板创建", func() bool {
		_, err := env.store.Panels.ByChannel(newChannel)
		return err == nil
	})

	panel, err := env.store.Panels.ByChannel(newChannel)
	if err != nil {
		t.Fatalf("读取面板失败: %v", err)
	}
	if panel.MsgID == "" {
		t.Fatal("应记录面板消息 ID")
	}

	var panelCardSent bool
	for _, call := range env.mock.CallsOf("message/create") {
		if fmt.Sprint(call.Params["target_id"]) == newChannel &&
			strings.Contains(fmt.Sprint(call.Params["content"]), actionOpen) {
			panelCardSent = true
		}
	}
	if !panelCardSent {
		t.Fatal("应在频道内发送面板卡片")
	}

	// 再次执行 /ticket：同一频道应新增一张卡片，而不是覆盖上一张。
	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(newChannel, userStaff, "/ticket", env.user(userStaff)))
	env.waitFor("第二张面板创建", func() bool {
		panels, err := env.store.Panels.ListByChannel(newChannel)
		return err == nil && len(panels) == 2
	})
	panels, err := env.store.Panels.ListByChannel(newChannel)
	if err != nil {
		t.Fatalf("读取频道面板失败: %v", err)
	}
	if panels[0].MsgID == "" || panels[1].MsgID == "" {
		t.Fatalf("两张面板都应记录消息 ID: %+v", panels)
	}
	if panels[0].MsgID == panels[1].MsgID {
		t.Fatal("两次 /ticket 应发送不同的卡片消息")
	}

	cards := 0
	for _, call := range env.mock.CallsOf("message/create") {
		if fmt.Sprint(call.Params["target_id"]) == newChannel &&
			strings.Contains(fmt.Sprint(call.Params["content"]), actionOpen) {
			cards++
		}
	}
	if cards != 2 {
		t.Fatalf("应在频道内发送 2 张面板卡片，实际 %d 张", cards)
	}
}

// TestSendPanelCardKeepsStoredButtonText 验证重建卡片时沿用面板记录里的按钮文字，
// 不会因为调用方没传而回退成默认的 "ticket"。
func TestSendPanelCardKeepsStoredButtonText(t *testing.T) {
	env := newBotEnv(t)
	panel := &store.Panel{ChannelID: "chan-btn", Title: "请联系我们", ButtonText: "联系客服", Enabled: true}
	if err := env.store.Panels.Create(panel); err != nil {
		t.Fatalf("创建面板失败: %v", err)
	}

	if _, err := env.bot.SendPanelCard(context.Background(), panel, ""); err != nil {
		t.Fatalf("发送面板卡片失败: %v", err)
	}

	var content string
	for _, call := range env.mock.CallsOf("message/create") {
		if fmt.Sprint(call.Params["target_id"]) == "chan-btn" {
			content = fmt.Sprint(call.Params["content"])
		}
	}
	if !strings.Contains(content, "联系客服") {
		t.Fatalf("卡片应使用面板记录中的按钮文字，实际: %s", content)
	}
	if !strings.Contains(content, actionOpen) {
		t.Fatalf("卡片应包含开单按钮: %s", content)
	}
}

func TestAdminRoleCommandAddsPanelAndGlobalRoles(t *testing.T) {
	env := newBotEnv(t)

	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(
		testPanelChan, userStaff, "/aar (rol)3001(rol)", env.user(userStaff)))
	env.waitFor("面板角色添加", func() bool {
		panel, err := env.store.Panels.ByChannel(testPanelChan)
		if err != nil {
			return false
		}
		for _, role := range panel.Roles {
			if role.RoleID == "3001" {
				return true
			}
		}
		return false
	})

	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(
		testPanelChan, userStaff, "/aar (rol)3002(rol) -g", env.user(userStaff)))
	env.waitFor("全局角色添加", func() bool {
		roles, err := env.store.Roles.ListAdmin()
		if err != nil {
			return false
		}
		for _, role := range roles {
			if role.RoleID == "3002" {
				return true
			}
		}
		return false
	})
}

func TestTicketCommentUpdatesLogCard(t *testing.T) {
	env := newBotEnv(t)
	channelID := env.openTicketForTest()
	ticket := env.firstTicket()

	// 先关闭工单，产生日志卡片
	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(
		channelID, userStaff, env.ticketValue(actionClose, ticket.No, channelID), env.user(userStaff)))
	env.waitFor("工单关闭", func() bool {
		updated, err := env.store.Tickets.ByNo(ticket.No)
		return err == nil && updated.Status == store.TicketClosed
	})

	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(
		testPanelChan, userStaff, fmt.Sprintf("/tkcm %s 已核对订单并发货", ticket.No), env.user(userStaff)))

	env.waitFor("备注写入", func() bool {
		notes, err := env.store.Tickets.Notes(ticket.No)
		return err == nil && len(notes) == 1
	})

	notes, _ := env.store.Tickets.Notes(ticket.No)
	if !strings.Contains(notes[0].Content, "已核对订单") {
		t.Fatalf("备注内容异常: %+v", notes[0])
	}
	env.waitFor("刷新日志卡片", func() bool { return len(env.mock.CallsOf("message/update")) > 0 })
}

func TestHelloCommandReplies(t *testing.T) {
	env := newBotEnv(t)
	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(testPanelChan, userAsker, "/hello", env.user(userAsker)))

	env.waitFor("hello 回复", func() bool {
		for _, call := range env.mock.CallsOf("message/create") {
			if strings.Contains(fmt.Sprint(call.Params["content"]), "world") {
				return true
			}
		}
		return false
	})
}

func TestKillCommandRequiresMention(t *testing.T) {
	env := newBotEnv(t)

	// 未 @ 机器人 → 拒绝
	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(testPanelChan, userStaff, "/kill", env.user(userStaff)))
	env.waitFor("拒绝提示", func() bool {
		for _, call := range env.mock.CallsOf("message/create") {
			if strings.Contains(fmt.Sprint(call.Params["content"]), "必须 @ 机器人") {
				return true
			}
		}
		return false
	})
	if env.stopCh.Load() {
		t.Fatal("未确认时不应退出")
	}

	// 正确 @ 机器人 → 触发退出
	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(
		testPanelChan, userStaff, "/kill (met)77777(met)", env.user(userStaff)))
	env.waitFor("触发退出", func() bool { return env.stopCh.Load() })
}

// ---------------------------------------------------------------------------
// 一次性码
// ---------------------------------------------------------------------------

func TestLoginCommandIssuesCode(t *testing.T) {
	env := newBotEnv(t)

	// 命中角色映射的客服
	env.mock.Push(kook.EventTypeText, kooktest.DirectMessageEvent(userStaff, "/login", env.user(userStaff)))
	env.waitFor("登录码签发", func() bool {
		code, err := env.store.Codes.Latest(userStaff, store.CodePurposeLogin)
		return err == nil && code != nil
	})

	code, _ := env.store.Codes.Latest(userStaff, store.CodePurposeLogin)
	if code.RoleHint != store.RoleStaff {
		t.Fatalf("角色提示应为 staff，实际 %s", code.RoleHint)
	}
	if code.ExpiresAt.Before(store.Now().Add(4 * time.Minute)) {
		t.Fatal("有效期应约为 5 分钟")
	}

	// 应私聊回复验证码（回复是异步发送的，这里等待其出现）
	env.waitFor("私聊回复验证码", func() bool {
		for _, call := range env.mock.CallsOf("direct-message/create") {
			if fmt.Sprint(call.Params["target_id"]) == userStaff &&
				strings.Contains(fmt.Sprint(call.Params["content"]), "登录码") {
				return true
			}
		}
		return false
	})

	// 未命中角色映射的用户不应获得验证码
	env.mock.Push(kook.EventTypeText, kooktest.DirectMessageEvent(userAsker, "/login", env.user(userAsker)))
	env.waitFor("无权限提示", func() bool {
		for _, call := range env.mock.CallsOf("direct-message/create") {
			if fmt.Sprint(call.Params["target_id"]) == userAsker &&
				strings.Contains(fmt.Sprint(call.Params["content"]), "未配置 WebUI 权限") {
				return true
			}
		}
		return false
	})
	if issued, _ := env.store.Codes.Latest(userAsker, store.CodePurposeLogin); issued != nil {
		t.Fatal("未命中角色映射的用户不应获得验证码")
	}
}

// ---------------------------------------------------------------------------
// 表情上角色
// ---------------------------------------------------------------------------

func TestReactionGrantsAndSwapsRoles(t *testing.T) {
	env := newBotEnv(t)

	ruleA := &store.EmojiRule{MessageID: "role-msg", ChannelID: testPanelChan, EmojiID: "em-heart", RoleID: fmt.Sprint(roleColorA), Label: "红色组", Enabled: true}
	ruleB := &store.EmojiRule{MessageID: "role-msg", ChannelID: testPanelChan, EmojiID: "em-blue", RoleID: fmt.Sprint(roleColorB), Label: "蓝色组", Enabled: true}
	if err := env.store.Emoji.CreateRule(ruleA); err != nil {
		t.Fatalf("创建规则失败: %v", err)
	}
	if err := env.store.Emoji.CreateRule(ruleB); err != nil {
		t.Fatalf("创建规则失败: %v", err)
	}

	env.mock.Push(kook.EventTypeSystem, kooktest.ReactionEvent("role-msg", userAsker, testPanelChan, "em-heart"))
	env.waitFor("角色发放", func() bool {
		return len(env.mock.CallsOf("guild-role/grant")) == 1
	})

	grant := env.mock.CallsOf("guild-role/grant")[0]
	if fmt.Sprint(grant.Params["role_id"]) != fmt.Sprint(roleColorA) {
		t.Fatalf("发放角色错误: %v", grant.Params["role_id"])
	}

	// 换一个表情：应先撤销旧角色，再发放新角色
	env.mock.Push(kook.EventTypeSystem, kooktest.ReactionEvent("role-msg", userAsker, testPanelChan, "em-blue"))
	env.waitFor("角色替换", func() bool {
		return len(env.mock.CallsOf("guild-role/grant")) == 2
	})

	revokes := env.mock.CallsOf("guild-role/revoke")
	if len(revokes) == 0 || fmt.Sprint(revokes[0].Params["role_id"]) != fmt.Sprint(roleColorA) {
		t.Fatalf("应撤销旧角色，实际 %+v", revokes)
	}

	// 未配置规则的消息不应触发任何调用
	before := len(env.mock.CallsOf("guild-role/grant"))
	env.mock.Push(kook.EventTypeSystem, kooktest.ReactionEvent("unknown-msg", userAsker, testPanelChan, "em-heart"))
	time.Sleep(250 * time.Millisecond)
	if len(env.mock.CallsOf("guild-role/grant")) != before {
		t.Fatal("未配置规则的消息不应发放角色")
	}
}

func TestReactionFailureNotifiesUser(t *testing.T) {
	env := newBotEnv(t)
	env.mock.RoleGrantFail = true

	rule := &store.EmojiRule{MessageID: "role-msg-2", ChannelID: testPanelChan, EmojiID: "em-heart", RoleID: fmt.Sprint(roleColorA), Label: "红色组", Enabled: true}
	if err := env.store.Emoji.CreateRule(rule); err != nil {
		t.Fatalf("创建规则失败: %v", err)
	}

	env.mock.Push(kook.EventTypeSystem, kooktest.ReactionEvent("role-msg-2", userAsker, testPanelChan, "em-heart"))
	env.waitFor("失败提示", func() bool {
		for _, call := range env.mock.CallsOf("message/create") {
			if fmt.Sprint(call.Params["temp_target_id"]) == userAsker &&
				strings.Contains(fmt.Sprint(call.Params["content"]), "发放角色失败") {
				return true
			}
		}
		return false
	})
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// openTicketForTest 走一次完整开单流程并返回工单频道 ID。
func (e *botEnv) openTicketForTest() string {
	e.t.Helper()
	e.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(testPanelChan, userAsker, e.openValue(), e.user(userAsker)))
	e.waitFor("工单创建完成", func() bool {
		ticket := e.firstTicket()
		return ticket != nil && ticket.Status == store.TicketOpen
	})
	return e.ticketChannel()
}

func toFloat(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		var out float64
		_, _ = fmt.Sscan(v, &out)
		return out
	default:
		return 0
	}
}

// TestHelpCommandRepliesWithCard 是回归测试：
//
// 帮助内容是卡片 JSON，必须用 type=10（卡片）发送。早期实现复用了纯文本
// 回复通道（type=9 KMarkdown），KOOK 会直接把整段 JSON 当文本显示出来。
func TestHelpCommandRepliesWithCard(t *testing.T) {
	env := newBotEnv(t)

	// 频道内 /help
	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(testPanelChan, userAsker, "/help", env.user(userAsker)))
	env.waitFor("频道内帮助卡片", func() bool {
		for _, call := range env.mock.CallsOf("message/create") {
			if fmt.Sprint(call.Params["target_id"]) != testPanelChan {
				continue
			}
			if int(toFloat(call.Params["type"])) != kook.MsgTypeCard {
				continue
			}
			if content := fmt.Sprint(call.Params["content"]); strings.Contains(content, "命令面板") && strings.HasPrefix(content, "[") {
				return true
			}
		}
		return false
	})

	// 私聊 /tkhelp
	env.mock.Push(kook.EventTypeText, kooktest.DirectMessageEvent(userAsker, "/tkhelp", env.user(userAsker)))
	env.waitFor("私聊帮助卡片", func() bool {
		for _, call := range env.mock.CallsOf("direct-message/create") {
			if fmt.Sprint(call.Params["target_id"]) != userAsker {
				continue
			}
			if int(toFloat(call.Params["type"])) != kook.MsgTypeCard {
				continue
			}
			if content := fmt.Sprint(call.Params["content"]); strings.Contains(content, "命令面板") && strings.HasPrefix(content, "[") {
				return true
			}
		}
		return false
	})
}
