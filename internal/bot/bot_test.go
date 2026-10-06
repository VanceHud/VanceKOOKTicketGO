package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/eventbus"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook/kooktest"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/ticket"
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
	// typeID 是默认面板所属的工单类型（带 rolePanel 类型角色）。
	typeID uint
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

	// 工单类型（含类型级管理员角色）
	seededType := &store.TicketType{Name: "工单面板", Enabled: true}
	if err := st.Types.Create(seededType); err != nil {
		t.Fatalf("写入工单类型失败: %v", err)
	}
	if err := st.Types.AddRole(seededType.ID, fmt.Sprint(rolePanel), "实习客服"); err != nil {
		t.Fatalf("写入类型角色失败: %v", err)
	}

	// 面板（挂在上述工单类型下）
	seededPanel := &store.Panel{
		TypeID:    seededType.ID,
		ChannelID: testPanelChan, ChannelName: "工单面板", MsgID: "msg-panel", Title: "点击按钮发起工单", Enabled: true,
	}
	if err := st.Panels.Create(seededPanel); err != nil {
		t.Fatalf("写入面板失败: %v", err)
	}

	mock := kooktest.New()
	t.Cleanup(mock.Close)

	bus := eventbus.New()
	svc := ticket.NewService(st, bus, ticket.NewNoopPlatform(nil), loc, st.Settings.OutdateHours)

	env := &botEnv{t: t, store: st, bus: bus, svc: svc, mock: mock, loc: loc, panelID: seededPanel.ID, typeID: seededType.ID}

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

// waitTicketOpened 等待开单流程完整跑完，并返回工单记录。
//
// 工单在「建频道 → 下发权限 → 发卡片 → 激活」之后就已经是 open，
// 但收尾还有「仅本人可见的完成提示」「面板自定义开单提示」与审计写入。
// 只等 status == open 会在这些副作用落库前提前返回，让断言读到半成品状态
// （-race 等较慢环境下必现）。审计里 ticket.open 有两条记录，
// 只有流程末尾的「创建工单频道 …」才代表开单全部完成。
func (e *botEnv) waitTicketOpened() *store.Ticket {
	e.t.Helper()
	var opened *store.Ticket
	e.waitFor("开单流程完成", func() bool {
		ticket := e.firstTicket()
		if ticket == nil || ticket.Status != store.TicketOpen {
			return false
		}
		opened = ticket
		entries, _, err := e.store.Audit.List(store.AuditFilter{
			Action: "ticket.open", Target: ticket.No, Page: 1, PageSize: 5,
		})
		if err != nil {
			return false
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Detail, "创建工单频道") {
				return true
			}
		}
		return false
	})
	return opened
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

// TestOpenTicketGrantsPermissionsConcurrently 是性能回归测试：
//
// 开单原本是纯串行的：建频道 → 每个主体「create + update + sleep 120ms」→ 发卡片 →
// 发提示，一个开单要好几秒（线上实测 3～8 秒）。现在建完频道就置为 open，
// 卡片、权限下发、面板提示并发执行，并发度交给平台限流头决定。
// 这里用模拟平台记录的最大并发数把这一行为固定下来。
func TestOpenTicketGrantsPermissionsConcurrently(t *testing.T) {
	env := newBotEnv(t)
	// 让每次调用慢一点，否则并发窗口太短、看不出来。
	env.mock.Delay = 40 * time.Millisecond
	env.mock.ResetConcurrency()

	env.openTicketForTest()

	if got := env.mock.MaxInFlight(); got < 2 {
		t.Fatalf("权限下发应与其它接口并发执行，实际最大并发 %d", got)
	}
}

func TestOpenTicketCreatesChannelPermissionsAndCard(t *testing.T) {
	env := newBotEnv(t)

	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(testPanelChan, userAsker, env.openValue(), env.user(userAsker)))

	opened := env.waitTicketOpened()
	if !strings.HasPrefix(opened.No, "TK-") {
		t.Fatalf("工单编号格式异常: %s", opened.No)
	}
	if opened.UserID != userAsker {
		t.Fatalf("开单人记录错误: %s", opened.UserID)
	}

	// 频道应在配置的隐藏分组下创建，名称包含类型名与短编号（完整编号在卡片中）
	channels := env.mock.Channels()
	if len(channels) != 1 {
		t.Fatalf("应创建 1 个频道，实际 %d 个", len(channels))
	}
	var created kook.Channel
	for _, channel := range channels {
		created = channel
	}
	if !strings.Contains(created.Name, "工单面板") || !strings.Contains(created.Name, ShortTicketCode(opened.No)) {
		t.Fatalf("频道名应包含类型名与短编号，实际: %s", created.Name)
	}
	if len([]rune(created.Name)) > ChannelNameMaxLength {
		t.Fatalf("频道名不应超过 %d 字符，实际 %d: %s", ChannelNameMaxLength, len([]rune(created.Name)), created.Name)
	}
	if created.ParentID != testCategory {
		t.Fatalf("频道应创建在配置的分组下，实际: %s", created.ParentID)
	}

	// 权限：全局管理员角色、工单类型管理员角色、开单人各一次，且都是“可看可发”
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
			// 工单开启时应展示类型，便于管理员分类
			if !strings.Contains(content, "工单类型") || !strings.Contains(content, "工单面板") {
				t.Fatalf("工单卡片应展示工单类型: %s", content)
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

// panelOpenMessageCalls 返回发往指定频道的 KMarkdown 文本消息内容。
func (e *botEnv) panelOpenMessageCalls(channelID string) []string {
	var out []string
	for _, call := range e.mock.CallsOf("message/create") {
		if fmt.Sprint(call.Params["target_id"]) != channelID {
			continue
		}
		if int(toFloat(call.Params["type"])) != kook.MsgTypeKMarkdown {
			continue
		}
		out = append(out, fmt.Sprint(call.Params["content"]))
	}
	return out
}

// TestOpenTicketSendsPanelOpenMessage 验证面板自定义的“开单后发送内容”：
// 以独立 KMarkdown 消息发送、占位符被替换，并写入工单时间线。
func TestOpenTicketSendsPanelOpenMessage(t *testing.T) {
	env := newBotEnv(t)

	template := "你好 {user}（{user_name}），工单 {ticket_no} 创建于 {time}，类型为 {type}（{type_name}）。\n请提供订单号与截图。"
	if err := env.store.Panels.UpdateFields(env.panelID, map[string]any{"open_message": template}); err != nil {
		t.Fatalf("写入开单提示失败: %v", err)
	}

	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(testPanelChan, userAsker, env.openValue(), env.user(userAsker)))

	opened := env.waitTicketOpened()
	sent := env.panelOpenMessageCalls(opened.ChannelID)
	if len(sent) != 1 {
		t.Fatalf("应在新频道发送 1 条开单提示，实际 %d 条: %v", len(sent), sent)
	}
	for _, expected := range []string{kook.MentionUser(userAsker), "提问用户", opened.No, "请提供订单号与截图", "工单面板"} {
		if !strings.Contains(sent[0], expected) {
			t.Fatalf("开单提示应包含 %q，实际: %s", expected, sent[0])
		}
	}
	if strings.Contains(sent[0], "{") {
		t.Fatalf("占位符应被全部替换: %s", sent[0])
	}

	// 时间线：开单系统消息 + 自定义提示。
	messages, err := env.store.Tickets.Messages(opened.No, 10, 0)
	if err != nil {
		t.Fatalf("查询消息失败: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("应写入 2 条机器人消息，实际 %d 条", len(messages))
	}
	last := messages[len(messages)-1]
	if !last.IsBot || last.MsgType != store.MsgTypeSystem || last.Content != sent[0] {
		t.Fatalf("时间线应记录自定义提示，实际 %+v", last)
	}
}

// TestOpenTicketSendsEphemeralCreatedNotice 验证开单成功后会在面板频道发送
// 一条仅开单人可见的完成提示，并带工单频道跳转链接。
func TestOpenTicketSendsEphemeralCreatedNotice(t *testing.T) {
	env := newBotEnv(t)

	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(testPanelChan, userAsker, env.openValue(), env.user(userAsker)))

	opened := env.waitTicketOpened()

	var notices []string
	for _, call := range env.mock.CallsOf("message/create") {
		if fmt.Sprint(call.Params["target_id"]) != testPanelChan {
			continue
		}
		if fmt.Sprint(call.Params["temp_target_id"]) != userAsker {
			continue
		}
		if int(toFloat(call.Params["type"])) != kook.MsgTypeCard {
			t.Fatalf("完成提示应以卡片消息发送，实际 type=%v", call.Params["type"])
		}
		notices = append(notices, fmt.Sprint(call.Params["content"]))
	}
	if len(notices) != 1 {
		t.Fatalf("应在面板频道发送 1 条仅开单人可见的完成提示，实际 %d 条: %v", len(notices), notices)
	}
	for _, want := range []string{"已创建完成", opened.No, kook.MentionChannel(opened.ChannelID)} {
		if !strings.Contains(notices[0], want) {
			t.Fatalf("完成提示应包含 %q，实际: %s", want, notices[0])
		}
	}

	// KOOK 的临时消息只在所在频道内可见，因此提示必须发回面板频道而非工单频道。
	for _, call := range env.mock.CallsOf("message/create") {
		if fmt.Sprint(call.Params["target_id"]) == opened.ChannelID &&
			fmt.Sprint(call.Params["temp_target_id"]) == userAsker {
			t.Fatal("完成提示不应发往工单频道")
		}
	}
}

// TestPanelOpenMessageNormalizesMarkdown 验证开单提示也做 KMarkdown 归一化：
// 开单提示以普通文本消息发送（没有卡片 header 模块），标题只能降级成加粗。
func TestPanelOpenMessageNormalizesMarkdown(t *testing.T) {
	env := newBotEnv(t)
	ticket := &store.Ticket{No: "ticket.1", UserID: userAsker, StartedAt: store.Now()}

	got := env.bot.panelOpenMessage("# 标题\n- 账号问题\n__下划线__", ticket)

	if strings.Contains(got, "#") || strings.Contains(got, "__") {
		t.Fatalf("开单提示不应残留 KOOK 不支持的写法: %q", got)
	}
	for _, want := range []string{"**标题**", "• 账号问题", "(ins)下划线(ins)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("开单提示应包含 %q，实际: %q", want, got)
		}
	}
}

// TestOpenTicketOpenMessageEscapesNickname 验证昵称中的提及语法不会被回填执行：
// KOOK 昵称可能包含 (met)all(met)，直接回填会导致伪造 @全体成员。
func TestOpenTicketOpenMessageEscapesNickname(t *testing.T) {
	env := newBotEnv(t)

	if err := env.store.Panels.UpdateFields(env.panelID, map[string]any{"open_message": "{user_name} 你好"}); err != nil {
		t.Fatalf("写入开单提示失败: %v", err)
	}
	// 伪造一个含提及语法的昵称。
	asker := env.user(userAsker)
	asker.Nickname = "(met)all(met)"

	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(testPanelChan, userAsker, env.openValue(), asker))

	opened := env.waitTicketOpened()

	sent := env.panelOpenMessageCalls(opened.ChannelID)
	if len(sent) != 1 {
		t.Fatalf("应发送 1 条开单提示，实际 %v", sent)
	}
	if strings.Contains(sent[0], "(met)all(met)") {
		t.Fatalf("昵称中的提及语法应被转义: %s", sent[0])
	}
}

// TestOpenTicketWithoutPanelOpenMessageSendsNoText 验证面板未配置时不在工单频道内发文本消息。
func TestOpenTicketWithoutPanelOpenMessageSendsNoText(t *testing.T) {
	env := newBotEnv(t)

	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(testPanelChan, userAsker, env.openValue(), env.user(userAsker)))

	opened := env.waitTicketOpened()

	if sent := env.panelOpenMessageCalls(opened.ChannelID); len(sent) != 0 {
		t.Fatalf("未配置开单提示时不应发送文本消息，实际 %v", sent)
	}
}

// TestOpenTicketFromSecondPanelUsesItsOwnRoles 验证同一频道内多张面板卡片各自携带角色：
// 点击第二张卡片的按钮时，只应下发第二张面板的管理员角色。
// TestOpenTicketFromSecondPanelUsesItsOwnType 验证同一频道内多张面板可以分属不同工单类型：
// 点击第二张卡片的按钮时，只应下发第二张面板所属类型的类型角色。
func TestOpenTicketFromSecondPanelUsesItsOwnType(t *testing.T) {
	env := newBotEnv(t)

	const secondRole = "3003"
	secondType := &store.TicketType{Name: "二线工单", Enabled: true}
	if err := env.store.Types.Create(secondType); err != nil {
		t.Fatalf("创建第二个工单类型失败: %v", err)
	}
	if err := env.store.Types.AddRole(secondType.ID, secondRole, "二线客服"); err != nil {
		t.Fatalf("写入第二个类型角色失败: %v", err)
	}
	second := &store.Panel{TypeID: secondType.ID, ChannelID: testPanelChan, ChannelName: "工单面板", Title: "第二张", Enabled: true}
	if err := env.store.Panels.Create(second); err != nil {
		t.Fatalf("创建第二张面板失败: %v", err)
	}

	value := env.bot.encodeButton(actionOpen, "", testPanelChan, second.ID)
	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(testPanelChan, userAsker, value, env.user(userAsker)))

	env.waitFor("工单创建完成", func() bool {
		ticket := env.firstTicket()
		return ticket != nil && ticket.Status == store.TicketOpen
	})
	// 权限下发在工单置为 open 之后并发执行，需单独等它出现。
	env.waitFor("第二个类型的角色已下发", func() bool {
		for _, call := range env.mock.CallsOf("channel-role/update") {
			if fmt.Sprint(call.Params["type"]) == "role_id" && fmt.Sprint(call.Params["value"]) == secondRole {
				return true
			}
		}
		return false
	})

	subjects := map[string]bool{}
	for _, call := range env.mock.CallsOf("channel-role/update") {
		subjects[fmt.Sprint(call.Params["type"])+":"+fmt.Sprint(call.Params["value"])] = true
	}
	if !subjects["role_id:"+secondRole] {
		t.Fatalf("应下发第二个类型的角色 %s，实际 %v", secondRole, subjects)
	}
	if subjects["role_id:"+fmt.Sprint(rolePanel)] {
		t.Fatalf("不应下发第一个类型的角色，实际 %v", subjects)
	}

	opened := env.firstTicket()
	if opened.PanelID == nil || *opened.PanelID != second.ID {
		t.Fatalf("工单应关联第二张面板 %d，实际 %v", second.ID, opened.PanelID)
	}
	if opened.TypeID == nil || *opened.TypeID != secondType.ID || opened.TypeName != secondType.Name {
		t.Fatalf("工单应记录第二个类型 %d/%s，实际 %v/%q", secondType.ID, secondType.Name, opened.TypeID, opened.TypeName)
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

// TestOpenTicketSendsNoDirectMessageProbe 是回归测试：
//
// 开单时不再发送「工单私信通道测试，消息将被自动删除」这类探测私信，
// 也不会因为私信被屏蔽而在面板频道提示用户开启私聊。
func TestOpenTicketSendsNoDirectMessageProbe(t *testing.T) {
	env := newBotEnv(t)
	env.mock.DMBlocked = true

	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(testPanelChan, userAsker, env.openValue(), env.user(userAsker)))

	env.waitTicketOpened()

	if calls := env.mock.CallsOf("direct-message/create"); len(calls) != 0 {
		t.Fatalf("开单不应发送私信探测消息，实际发送 %d 条", len(calls))
	}
	for _, call := range env.mock.CallsOf("message/create") {
		if strings.Contains(fmt.Sprint(call.Params["content"]), "私聊") {
			t.Fatal("开单环节不应再提示用户开启私聊")
		}
	}
}

// TestCloseNotifiesAdminWhenPrivateMessageBlocked 校验私信兜底：
//
// 开单人未开启私聊时，关闭通知送不到，必须在日志频道提醒管理员人工转达，
// 并把这件事写进时间线。
func TestCloseNotifiesAdminWhenPrivateMessageBlocked(t *testing.T) {
	env := newBotEnv(t)
	env.mock.DMBlocked = true

	channelID := env.openTicketForTest()
	ticket := env.firstTicket()

	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(
		channelID, userStaff, env.ticketValue(actionClose, ticket.No, channelID), env.user(userStaff)))

	env.waitFor("工单关闭", func() bool {
		updated, err := env.store.Tickets.ByNo(ticket.No)
		return err == nil && updated.Status == store.TicketClosed
	})

	// 提醒卡片应发往日志频道，而不是开单人私聊
	var warned bool
	for _, call := range env.mock.CallsOf("message/create") {
		if fmt.Sprint(call.Params["target_id"]) == testLogChan &&
			strings.Contains(fmt.Sprint(call.Params["content"]), "未能私信送达") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("开单人私信不可达时应提醒管理员手动转达")
	}

	msgs, err := env.store.Tickets.Messages(ticket.No, 50, 0)
	if err != nil {
		t.Fatalf("读取时间线失败: %v", err)
	}
	var logged bool
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "关闭通知未能私信送达") {
			logged = true
		}
	}
	if !logged {
		t.Fatal("时间线应记录关闭通知未送达")
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

// TestMediaMessageStoresMediaFields 验证媒体消息的结构化字段：
//
// WebUI 需要根据 media_url 直接渲染图片/播放器/下载链接。
// 事件顶层 content 为空时（平台个别场景），必须回退到 extra.attachments.url。
func TestMediaMessageStoresMediaFields(t *testing.T) {
	env := newBotEnv(t)
	channelID := env.openTicketForTest()

	// 常规图片消息：content 就是图片地址。
	env.mock.Push(kook.EventTypeImage, kooktest.ImageMessageEvent(channelID, userAsker, "https://img.example/a.png", env.user(userAsker)))

	// content 为空的图片消息：地址只在 extra.attachments 里。
	fallback := kooktest.ImageMessageEvent(channelID, userAsker, "", env.user(userAsker))
	fallback["extra"].(map[string]any)["attachments"] = map[string]any{
		"type": "image", "name": "b.png", "url": "https://img.example/b.png",
	}
	env.mock.Push(kook.EventTypeImage, fallback)

	ticket := env.firstTicket()
	env.waitFor("媒体消息归档", func() bool {
		messages, err := env.store.Tickets.Messages(ticket.No, 20, 0)
		return err == nil && len(messages) >= 3
	})

	messages, _ := env.store.Tickets.Messages(ticket.No, 20, 0)
	first := messages[len(messages)-2]
	if first.MediaURL != "https://img.example/a.png" || first.MediaName != "a.png" {
		t.Fatalf("图片消息未保存媒体字段: %+v", first)
	}
	second := messages[len(messages)-1]
	if second.MediaURL != "https://img.example/b.png" || second.MediaName != "b.png" {
		t.Fatalf("附件兜底的图片消息未保存媒体字段: %+v", second)
	}
}

// TestCardMessageContentIsEnrichedFromMessageView 是回归测试：
//
// 真实平台的卡片消息事件 content 为空，必须再调 message/view 才能拿到卡片 JSON；
// 平台也把用户上传的文件转成了卡片消息。这里验证卡片 JSON、文本摘要以及
// 卡片内文件附件（media_* 字段）都会被补全，保证 WebUI 能正常展示与下载。
func TestCardMessageContentIsEnrichedFromMessageView(t *testing.T) {
	env := newBotEnv(t)
	channelID := env.openTicketForTest()

	cardJSON := `[{"type":"card","theme":"info","modules":[` +
		`{"type":"header","text":{"type":"plain-text","content":"服务器维护公告"}},` +
		`{"type":"section","text":{"type":"kmarkdown","content":"维护时间 **22:00-23:00**"}},` +
		`{"type":"file","src":"https://files.example/guide.pdf","title":"guide.pdf"}]}]`

	env.mock.InjectMessage("msg-card-1", kook.Message{
		ID:      "msg-card-1",
		Type:    kook.EventTypeCard,
		Content: cardJSON,
	})
	env.mock.Push(kook.EventTypeCard, kooktest.CardMessageEvent(channelID, userAsker, "msg-card-1", env.user(userAsker)))

	ticket := env.firstTicket()
	env.waitFor("卡片消息补全", func() bool {
		messages, err := env.store.Tickets.Messages(ticket.No, 20, 0)
		if err != nil || len(messages) < 2 {
			return false
		}
		last := messages[len(messages)-1]
		return last.MsgType == store.MsgTypeCard && last.CardJSON != "" && last.MediaURL != ""
	})

	messages, _ := env.store.Tickets.Messages(ticket.No, 20, 0)
	last := messages[len(messages)-1]
	if last.CardJSON != cardJSON {
		t.Fatalf("卡片 JSON 未入库: %q", last.CardJSON)
	}
	if !strings.Contains(last.Content, "服务器维护公告") || !strings.Contains(last.Content, "维护时间") {
		t.Fatalf("卡片文本摘要未入库: %q", last.Content)
	}
	if last.MediaURL != "https://files.example/guide.pdf" || last.MediaName != "guide.pdf" {
		t.Fatalf("卡片文件附件未入库: %+v", last)
	}
	if last.MediaType != "file" {
		t.Fatalf("附件类型异常: %+v", last)
	}
	if len(env.mock.CallsOf("message/view")) == 0 {
		t.Fatalf("应调用 message/view 补全卡片内容")
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
	panel := &store.Panel{TypeID: env.typeID, ChannelID: "chan-btn", Title: "请联系我们", ButtonText: "联系客服", Enabled: true}
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

// TestPanelCardNormalizesMarkdown 验证 KOOK 不支持的 Markdown 写法在发送前被转换：
// 首行 "# 标题" 改用卡片 header 模块（KOOK 唯一的大标题形式，纯文本），
// __下划线__ 与 "- 列表" 降级为 KOOK 真正支持的写法。
func TestPanelCardNormalizesMarkdown(t *testing.T) {
	env := newBotEnv(t)

	content := env.bot.panelCard(
		"# 请点击按钮发起工单TEST1\n123123\n**加粗**、__下划线__\n- 账号问题",
		"发起工单", "open-value",
	)

	var cards []kook.Card
	if err := json.Unmarshal([]byte(content), &cards); err != nil {
		t.Fatalf("面板卡片不是合法 JSON: %v（%s）", err, content)
	}
	if len(cards) != 1 || len(cards[0].Modules) != 3 {
		t.Fatalf("面板卡片应含标题/内容/按钮三个模块: %s", content)
	}

	header := cards[0].Modules[0]
	if header.Type != "header" || header.Text == nil || header.Text.Type != "plain-text" {
		t.Fatalf("首行标题应使用 plain-text 的 header 模块: %+v", header)
	}
	if header.Text.Content != "请点击按钮发起工单TEST1" {
		t.Fatalf("header 内容不符: %q", header.Text.Content)
	}

	section := cards[0].Modules[1]
	if section.Type != "section" || section.Text == nil || section.Text.Type != "kmarkdown" {
		t.Fatalf("正文应使用 kmarkdown 的 section 模块: %+v", section)
	}
	body := section.Text.Content
	if strings.Contains(body, "#") || strings.Contains(body, "__") {
		t.Fatalf("正文不应残留 KOOK 不支持的写法: %q", body)
	}
	for _, want := range []string{"123123", "**加粗**", "(ins)下划线(ins)", "• 账号问题"} {
		if !strings.Contains(body, want) {
			t.Fatalf("正文应包含 %q: %q", want, body)
		}
	}

	action := cards[0].Modules[2]
	if action.Type != "action-group" || len(action.Elements) != 1 {
		t.Fatalf("按钮组异常: %+v", action)
	}
	if action.Elements[0].Value != "open-value" || action.Elements[0].Text.Content != "发起工单" {
		t.Fatalf("按钮内容异常: %+v", action.Elements[0])
	}
}

// TestPanelCardKeepsPlainTitleInSection 验证未写标题语法的面板文案行为不变：
// 整段文案仍然放在 kmarkdown 模块里。
func TestPanelCardKeepsPlainTitleInSection(t *testing.T) {
	env := newBotEnv(t)

	content := env.bot.panelCard("请点击右侧按钮发起工单", "ticket", "open-value")

	var cards []kook.Card
	if err := json.Unmarshal([]byte(content), &cards); err != nil {
		t.Fatalf("面板卡片不是合法 JSON: %v（%s）", err, content)
	}
	if len(cards) != 1 || len(cards[0].Modules) != 2 {
		t.Fatalf("无标题文案应只有内容与按钮模块: %s", content)
	}
	if cards[0].Modules[0].Type != "section" {
		t.Fatalf("首个模块应为 section: %+v", cards[0].Modules[0])
	}
}

// TestAdminRoleCommandAddsTypeAndGlobalRoles 验证 /aar 把角色加到当前频道面板所属工单类型上，
// 加 -g 时则加为全局管理员角色。
func TestAdminRoleCommandAddsTypeAndGlobalRoles(t *testing.T) {
	env := newBotEnv(t)

	env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(
		testPanelChan, userStaff, "/aar (rol)3001(rol)", env.user(userStaff)))
	env.waitFor("类型角色添加", func() bool {
		types, err := env.store.Types.ListByChannel(testPanelChan)
		if err != nil {
			return false
		}
		for _, item := range types {
			for _, role := range item.Roles {
				if role.RoleID == "3001" {
					return true
				}
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

// TestWebUICloseCardShowsActorName 是回归测试：
//
// WebUI 关闭工单时 actor.ID 是账号名（如 admin）而不是 KOOK 用户 ID。
// 早期实现直接把它拼成 (met)admin(met)，KOOK 客户端渲染为「@用户不存在」。
func TestWebUICloseCardShowsActorName(t *testing.T) {
	env := newBotEnv(t)
	env.openTicketForTest()
	opened := env.firstTicket()

	actor := ticket.Actor{ID: "admin", Name: "客服小林", Role: store.RoleStaff, Source: "web"}
	if _, err := env.svc.Close(context.Background(), opened.No, actor, "已处理完毕"); err != nil {
		t.Fatalf("关闭工单失败: %v", err)
	}

	closed, err := env.store.Tickets.ByNo(opened.No)
	if err != nil || closed.Status != store.TicketClosed {
		t.Fatalf("工单状态应为 closed: %+v err=%v", closed, err)
	}
	if closed.ClosedByName != "客服小林" {
		t.Fatalf("应记录关闭人昵称: %+v", closed)
	}

	content := logCardContent(t, env)
	if strings.Contains(content, "(met)admin(met)") {
		t.Fatalf("WebUI 账号名不应当作 KOOK 用户提及: %s", content)
	}
	if !strings.Contains(content, "关闭用户：客服小林") {
		t.Fatalf("关闭用户应展示昵称: %s", content)
	}

	// /tkcm 刷新日志卡片时同样不能把账号名当提及。
	if _, err := env.svc.AddNote(opened.No, actor, "补充说明"); err != nil {
		t.Fatalf("添加备注失败: %v", err)
	}
	card := env.bot.ticketLogCard(closed, []store.TicketNote{{AuthorID: "admin", AuthorName: "客服小林", Content: "补充说明"}})
	if strings.Contains(card, "(met)admin(met)") {
		t.Fatalf("备注作者不应当作 KOOK 用户提及: %s", card)
	}
	if !strings.Contains(card, "来自 客服小林 的备注") {
		t.Fatalf("备注作者应展示昵称: %s", card)
	}
}

// logCardContent 返回发往日志频道的最后一条卡片内容。
func logCardContent(t *testing.T, env *botEnv) string {
	t.Helper()
	content := ""
	for _, call := range env.mock.CallsOf("message/create") {
		if fmt.Sprint(call.Params["target_id"]) == testLogChan {
			content = fmt.Sprint(call.Params["content"])
		}
	}
	if content == "" {
		t.Fatal("日志频道没有收到卡片")
	}
	return content
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
	e.waitTicketOpened()
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

// TestOpenTicketRejectsDisabledType 验证类型停用后，其面板不再开单并给出明确提示。
func TestOpenTicketRejectsDisabledType(t *testing.T) {
	env := newBotEnv(t)
	if err := env.store.Types.UpdateFields(env.typeID, map[string]any{"enabled": false}); err != nil {
		t.Fatalf("停用类型失败: %v", err)
	}

	env.mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(testPanelChan, userAsker, env.openValue(), env.user(userAsker)))
	env.waitFor("停用提示", func() bool {
		for _, call := range env.mock.CallsOf("message/create") {
			if fmt.Sprint(call.Params["temp_target_id"]) == userAsker &&
				strings.Contains(fmt.Sprint(call.Params["content"]), "已停用") {
				return true
			}
		}
		return false
	})
	if ticket := env.firstTicket(); ticket != nil {
		t.Fatalf("停用类型不应创建工单: %+v", ticket)
	}
}

// TestTicketCommandBindsPanelToType 验证 /ticket 命令与工单类型的关系：
//   - `/ticket 举报投诉` 创建（或复用）同名类型并把面板挂上去；
//   - 省略类型名时复用当前频道已有面板的类型，保证旧习惯可用。
func TestTicketCommandBindsPanelToType(t *testing.T) {
	env := newBotEnv(t)
	const channel = "chan-type-cmd"

	click := func(content string) {
		t.Helper()
		env.mock.Push(kook.EventTypeText, kooktest.TextMessageEvent(channel, userStaff, content, env.user(userStaff)))
	}

	click("/ticket 举报投诉")
	env.waitFor("第一个面板创建完成", func() bool {
		panels, err := env.store.Panels.ListByChannel(channel)
		return err == nil && len(panels) == 1
	})
	panels, err := env.store.Panels.ListByChannel(channel)
	if err != nil || len(panels) != 1 {
		t.Fatalf("读取面板失败: %v", err)
	}
	reportType, err := env.store.Types.ByID(panels[0].TypeID)
	if err != nil {
		t.Fatalf("面板应挂在类型上: %v", err)
	}
	if reportType.Name != "举报投诉" {
		t.Fatalf("类型名应为命令参数，实际 %q", reportType.Name)
	}

	// 同名命令：复用类型，不重复创建。
	click("/ticket 举报投诉")
	env.waitFor("第二个面板创建完成", func() bool {
		panels, err := env.store.Panels.ListByChannel(channel)
		return err == nil && len(panels) == 2
	})
	panels, _ = env.store.Panels.ListByChannel(channel)
	if panels[1].TypeID != reportType.ID {
		t.Fatalf("同名类型应被复用（类型 %d），实际 %d", reportType.ID, panels[1].TypeID)
	}
	all, err := env.store.Types.List()
	if err != nil {
		t.Fatalf("读取类型失败: %v", err)
	}
	sameName := 0
	for _, item := range all {
		if item.Name == "举报投诉" {
			sameName++
		}
	}
	if sameName != 1 {
		t.Fatalf("同名类型只应存在一个，实际 %d 个", sameName)
	}

	// 省略类型名：复用本频道已有类型。
	click("/ticket")
	env.waitFor("第三个面板创建完成", func() bool {
		panels, err := env.store.Panels.ListByChannel(channel)
		return err == nil && len(panels) == 3
	})
	panels, _ = env.store.Panels.ListByChannel(channel)
	if panels[2].TypeID != reportType.ID {
		t.Fatalf("省略类型名时应复用本频道已有类型 %d，实际 %d", reportType.ID, panels[2].TypeID)
	}
}
