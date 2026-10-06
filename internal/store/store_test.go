package store

import (
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := t.TempDir() + "/ticket.db"
	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func testLocation(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载时区失败: %v", err)
	}
	return loc
}

// seedTicketType 创建测试用工单类型：面板必须挂在类型下（外键约束）。
func seedTicketType(t *testing.T, st *Store, name string) *TicketType {
	t.Helper()
	item := &TicketType{Name: name, Enabled: true}
	if err := st.Types.Create(item); err != nil {
		t.Fatalf("创建工单类型失败: %v", err)
	}
	return item
}

func TestOpenCreatesPrivateFilesAndPassesQuickCheck(t *testing.T) {
	st := newTestStore(t)

	result, err := st.QuickCheck()
	if err != nil {
		t.Fatalf("自检失败: %v", err)
	}
	if !strings.EqualFold(result, "ok") {
		t.Fatalf("自检结果异常: %s", result)
	}
}

func TestTicketNumberAllocationIsUnique(t *testing.T) {
	st := newTestStore(t)
	loc := testLocation(t)
	now := Now()

	seen := make(map[string]struct{}, 200)
	for i := 0; i < 200; i++ {
		ticket := &Ticket{UserID: "90000000000000001", UserName: "测试用户", Status: TicketPending}
		if err := st.Tickets.CreateWithNo(ticket, now, loc); err != nil {
			t.Fatalf("创建工单失败: %v", err)
		}
		if _, exists := seen[ticket.No]; exists {
			t.Fatalf("编号重复: %s", ticket.No)
		}
		seen[ticket.No] = struct{}{}
		if !strings.HasPrefix(ticket.No, "TK-") {
			t.Fatalf("编号格式异常: %s", ticket.No)
		}
	}

	var count int64
	if err := st.DB().Model(&Ticket{}).Count(&count).Error; err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if count != 200 {
		t.Fatalf("工单数量不符: %d", count)
	}
}

func TestSearchEscapesLikeWildcards(t *testing.T) {
	st := newTestStore(t)
	loc := testLocation(t)
	now := Now()

	for _, name := range []string{"小张", "李雷", "韩梅梅"} {
		ticket := &Ticket{UserID: "9000000000000000" + name[:1], UserName: name, Status: TicketOpen}
		if err := st.Tickets.CreateWithNo(ticket, now, loc); err != nil {
			t.Fatalf("创建工单失败: %v", err)
		}
	}

	// 通配符必须被转义：% 不应匹配所有记录
	items, total, err := st.Tickets.List(TicketFilter{Query: "%"})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Fatalf("通配符注入未被转义，匹配到 %d 条", total)
	}

	// 下划线同样是通配符
	if _, total, err := st.Tickets.List(TicketFilter{Query: "_"}); err != nil || total != 0 {
		t.Fatalf("下划线注入未被转义，匹配到 %d 条（err=%v）", total, err)
	}

	// 正常关键词仍可命中
	if _, total, err := st.Tickets.List(TicketFilter{Query: "李雷"}); err != nil || total != 1 {
		t.Fatalf("正常关键词匹配失败: total=%d err=%v", total, err)
	}
}

func TestSearchMatchesMessageContent(t *testing.T) {
	st := newTestStore(t)
	loc := testLocation(t)

	ticket := &Ticket{UserID: "90000000000000001", UserName: "小张", Status: TicketOpen}
	if err := st.Tickets.CreateWithNo(ticket, Now(), loc); err != nil {
		t.Fatalf("创建工单失败: %v", err)
	}
	if err := st.Tickets.AddMessage(&TicketMessage{
		TicketNo:  ticket.No,
		UserID:    ticket.UserID,
		UserName:  ticket.UserName,
		Content:   "充值没有到账，订单号 2601058891",
		MsgType:   MsgTypeText,
		CreatedAt: Now(),
	}); err != nil {
		t.Fatalf("写入消息失败: %v", err)
	}

	if _, total, err := st.Tickets.List(TicketFilter{Query: "充值"}); err != nil || total != 1 {
		t.Fatalf("按聊天内容搜索失败: total=%d err=%v", total, err)
	}
}

func TestAddMessageTracksCountAndFirstReply(t *testing.T) {
	st := newTestStore(t)
	loc := testLocation(t)
	now := Now()

	ticket := &Ticket{UserID: "90000000000000001", UserName: "小张", Status: TicketOpen}
	if err := st.Tickets.CreateWithNo(ticket, now, loc); err != nil {
		t.Fatalf("创建工单失败: %v", err)
	}

	// 机器人消息与开单人消息都不算首次响应
	_ = st.Tickets.AddMessage(&TicketMessage{TicketNo: ticket.No, UserID: "bot", IsBot: true, Content: "工单已创建", MsgType: MsgTypeSystem, CreatedAt: now})
	_ = st.Tickets.AddMessage(&TicketMessage{TicketNo: ticket.No, UserID: ticket.UserID, Content: "求助", MsgType: MsgTypeText, CreatedAt: now.Add(time.Minute)})

	loaded, err := st.Tickets.ByNo(ticket.No)
	if err != nil {
		t.Fatalf("查询工单失败: %v", err)
	}
	if loaded.MessageCount != 2 {
		t.Fatalf("消息数应为 2，得到 %d", loaded.MessageCount)
	}
	if loaded.FirstReplyAt != nil {
		t.Fatal("机器人消息与开单人消息不应计入首次响应")
	}

	// 客服回复才算首次响应
	_ = st.Tickets.AddMessage(&TicketMessage{TicketNo: ticket.No, UserID: "90000000000000101", UserName: "客服", Content: "收到", MsgType: MsgTypeText, CreatedAt: now.Add(5 * time.Minute)})

	loaded, err = st.Tickets.ByNo(ticket.No)
	if err != nil {
		t.Fatalf("查询工单失败: %v", err)
	}
	if loaded.MessageCount != 3 {
		t.Fatalf("消息数应为 3，得到 %d", loaded.MessageCount)
	}
	if loaded.FirstReplyAt == nil {
		t.Fatal("客服回复后应记录首次响应时间")
	}
	if got := loaded.FirstReplyAt.Sub(loaded.StartedAt).Minutes(); got < 4 || got > 6 {
		t.Fatalf("首次响应时间偏差过大: %.1f 分钟", got)
	}
}

func TestTicketLifecycleFiltersAndStats(t *testing.T) {
	st := newTestStore(t)
	loc := testLocation(t)
	now := Now()

	// 3 条已关闭 + 2 条进行中
	for i := 0; i < 5; i++ {
		ticket := &Ticket{UserID: "9000000000000000" + string(rune('1'+i)), UserName: "用户", Status: TicketOpen, StartedAt: now.Add(-time.Duration(i) * time.Hour)}
		if err := st.Tickets.CreateWithNo(ticket, now.Add(-time.Duration(i)*time.Hour), loc); err != nil {
			t.Fatalf("创建工单失败: %v", err)
		}
		if i < 3 {
			closedAt := now.Add(-time.Duration(i) * 30 * time.Minute)
			if err := st.Tickets.UpdateFields(ticket.No, map[string]any{
				"status":    TicketClosed,
				"closed_at": closedAt,
				"closed_by": "admin",
			}); err != nil {
				t.Fatalf("更新工单失败: %v", err)
			}
		}
	}

	overview, err := st.Tickets.Overview(now, loc, 7)
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if overview.Total != 5 {
		t.Fatalf("总数应为 5，得到 %d", overview.Total)
	}
	if overview.Closed != 3 {
		t.Fatalf("已关闭应为 3，得到 %d", overview.Closed)
	}
	if overview.Open != 2 {
		t.Fatalf("进行中应为 2，得到 %d", overview.Open)
	}
	if overview.StatusCounts[TicketClosed] != 3 || overview.StatusCounts[TicketOpen] != 2 {
		t.Fatalf("状态分布不符: %+v", overview.StatusCounts)
	}
	if len(overview.Trend) != 7 {
		t.Fatalf("趋势点应为 7 个（含空日），得到 %d", len(overview.Trend))
	}
	today := overview.Trend[len(overview.Trend)-1]
	if today.Opened == 0 {
		t.Fatal("今天的趋势点应统计到新建工单")
	}

	// 状态筛选
	items, total, err := st.Tickets.List(TicketFilter{Statuses: []string{TicketClosed}})
	if err != nil || total != 3 || len(items) != 3 {
		t.Fatalf("状态筛选失败: total=%d len=%d err=%v", total, len(items), err)
	}
}

func TestGatewaySessionRoundTrip(t *testing.T) {
	st := newTestStore(t)

	// 初始没有会话：首次启动应建立全新连接
	sessionID, sn, err := st.Settings.LoadGatewaySession()
	if err != nil {
		t.Fatalf("读取网关会话失败: %v", err)
	}
	if sessionID != "" || sn != 0 {
		t.Fatalf("初始应无网关会话，实际 session=%q sn=%d", sessionID, sn)
	}

	// 连接建立后落库，供进程重启续传
	if err := st.Settings.SaveGatewaySession("sess-1", 42); err != nil {
		t.Fatalf("写入网关会话失败: %v", err)
	}
	sessionID, sn, err = st.Settings.LoadGatewaySession()
	if err != nil || sessionID != "sess-1" || sn != 42 {
		t.Fatalf("网关会话读取不符: session=%q sn=%d err=%v", sessionID, sn, err)
	}

	// 会话失效（续传被拒 / 平台要求重连）时必须能彻底清空
	if err := st.Settings.SaveGatewaySession("", 0); err != nil {
		t.Fatalf("清空网关会话失败: %v", err)
	}
	sessionID, sn, err = st.Settings.LoadGatewaySession()
	if err != nil || sessionID != "" || sn != 0 {
		t.Fatalf("清空后不应再读到会话: session=%q sn=%d err=%v", sessionID, sn, err)
	}
}

func TestSettingsSecretRoundTripAndWrongKey(t *testing.T) {
	st := newTestStore(t)
	secret := []byte("app-secret-for-tests-0123456789abcdef")

	if err := st.Settings.SetSecret(SettingKookToken, "kook-token-value", secret); err != nil {
		t.Fatalf("写入密钥失败: %v", err)
	}

	// 数据库中不应出现明文
	raw, _, err := st.Settings.Get(SettingKookToken)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if strings.Contains(raw, "kook-token-value") {
		t.Fatal("数据库中不应出现明文 token")
	}

	value, ok, err := st.Settings.GetSecret(SettingKookToken, secret)
	if err != nil || !ok {
		t.Fatalf("解密失败: ok=%v err=%v", ok, err)
	}
	if value != "kook-token-value" {
		t.Fatalf("解密结果不符: %s", value)
	}

	// 更换密钥后必须报错，而不是返回错误数据
	if _, _, err := st.Settings.GetSecret(SettingKookToken, []byte("rotated-secret")); err == nil {
		t.Fatal("使用错误密钥解密应当报错")
	}

	// Runtime 汇总中只暴露掩码
	runtimeCfg, err := st.Settings.Runtime(secret)
	if err != nil {
		t.Fatalf("汇总配置失败: %v", err)
	}
	if runtimeCfg.TokenMasked == "kook-token-value" || strings.Contains(runtimeCfg.TokenMasked, "kook-token") {
		t.Fatalf("掩码处理异常: %s", runtimeCfg.TokenMasked)
	}
	if !strings.HasSuffix(runtimeCfg.TokenMasked, "alue") {
		t.Fatalf("掩码应保留末四位: %s", runtimeCfg.TokenMasked)
	}
	if len(runtimeCfg.MissingRequired) == 0 {
		t.Fatal("缺少频道配置时应提示待完成项")
	}
}

func TestUserUniquenessAndSessionRevocation(t *testing.T) {
	st := newTestStore(t)

	first := &WebUser{Username: "Admin", PasswordHash: "x", Role: RoleAdmin}
	if err := st.Users.Create(first); err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}
	if first.Username != "admin" {
		t.Fatalf("用户名应被归一化为小写，得到 %s", first.Username)
	}

	// 大小写不同视为同一用户名
	if err := st.Users.Create(&WebUser{Username: "ADMIN", PasswordHash: "y", Role: RoleStaff}); err == nil {
		t.Fatal("重复用户名（大小写不同）应被拒绝")
	}

	// 会话：创建后可查询，按用户吊销后消失
	session := &Session{
		TokenHash:  "hash-1",
		UserID:     first.ID,
		ExpiresAt:  Now().Add(time.Hour),
		LastSeenAt: Now(),
	}
	if err := st.Sessions.Create(session); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	if _, err := st.Sessions.ByTokenHash("hash-1"); err != nil {
		t.Fatalf("查询会话失败: %v", err)
	}
	if err := st.Sessions.DeleteForUser(first.ID); err != nil {
		t.Fatalf("吊销会话失败: %v", err)
	}
	if _, err := st.Sessions.ByTokenHash("hash-1"); err != ErrNotFound {
		t.Fatalf("吊销后应查不到会话，得到 %v", err)
	}

	// 删除账号会连带清理会话
	// 删除管理员前保留另一可用管理员，符合仓储层的事务保护。
	if err := st.Users.Create(&WebUser{Username: "backup-admin", PasswordHash: "z", Role: RoleAdmin}); err != nil {
		t.Fatalf("创建备用管理员失败: %v", err)
	}
	if err := st.Sessions.Create(&Session{TokenHash: "hash-2", UserID: first.ID, ExpiresAt: Now().Add(time.Hour), LastSeenAt: Now()}); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	if err := st.Users.Delete(first.ID); err != nil {
		t.Fatalf("删除账号失败: %v", err)
	}
	if _, err := st.Sessions.ByTokenHash("hash-2"); err != ErrNotFound {
		t.Fatalf("删除账号后会话应被清理，得到 %v", err)
	}
}

func TestResolveWebRolePicksHighestPermission(t *testing.T) {
	st := newTestStore(t)

	_ = st.Roles.UpsertMapping(&RoleMapping{KookRoleID: "1000000000000001", KookRoleName: "实习客服", WebRole: RoleReadonly})
	_ = st.Roles.UpsertMapping(&RoleMapping{KookRoleID: "1000000000000002", KookRoleName: "客服组", WebRole: RoleStaff})

	role, ok, err := st.Roles.ResolveWebRole([]string{"1000000000000001", "1000000000000002"})
	if err != nil || !ok {
		t.Fatalf("解析失败: ok=%v err=%v", ok, err)
	}
	if role != RoleStaff {
		t.Fatalf("应取权限最高的角色，得到 %s", role)
	}

	// 未命中任何规则
	if _, ok, err := st.Roles.ResolveWebRole([]string{"9999999999999999"}); err != nil || ok {
		t.Fatalf("未命中规则时应返回 ok=false，得到 ok=%v err=%v", ok, err)
	}
	if _, ok, _ := st.Roles.ResolveWebRole(nil); ok {
		t.Fatal("空角色列表不应解析出权限")
	}
}

func TestAuditLogIsAppendOnly(t *testing.T) {
	st := newTestStore(t)

	for i := 0; i < 5; i++ {
		if err := st.Audit.Write(&AuditLog{
			Actor:     "admin",
			ActorType: ActorTypeWeb,
			Action:    "ticket.close",
			Target:    "TK-260105-ABCD",
			Detail:    "关闭工单",
			IP:        "127.0.0.1",
		}); err != nil {
			t.Fatalf("写入审计失败: %v", err)
		}
	}

	items, total, err := st.Audit.List(AuditFilter{Action: "ticket.close", Page: 1, PageSize: 10})
	if err != nil || total != 5 {
		t.Fatalf("审计查询失败: total=%d err=%v", total, err)
	}
	if items[0].Action != "ticket.close" {
		t.Fatalf("审计内容异常: %+v", items[0])
	}

	// 通配符注入同样需要被转义
	if _, total, err := st.Audit.List(AuditFilter{Actor: "%"}); err != nil || total != 0 {
		t.Fatalf("审计查询的通配符未被转义: total=%d err=%v", total, err)
	}
}

func TestPanelsAllowMultiplePerChannel(t *testing.T) {
	st := newTestStore(t)
	ticketType := seedTicketType(t, st, "工单面板")

	first := &Panel{TypeID: ticketType.ID, ChannelID: "30001", ChannelName: "工单面板", Title: "第一张", Enabled: true}
	if err := st.Panels.Create(first); err != nil {
		t.Fatalf("创建第一张面板失败: %v", err)
	}
	second := &Panel{TypeID: ticketType.ID, ChannelID: "30001", ChannelName: "工单面板", Title: "第二张", Enabled: true}
	if err := st.Panels.Create(second); err != nil {
		t.Fatalf("同一频道应允许创建第二张面板: %v", err)
	}

	panels, err := st.Panels.ListByChannel("30001")
	if err != nil {
		t.Fatalf("查询频道面板失败: %v", err)
	}
	if len(panels) != 2 {
		t.Fatalf("应返回 2 张面板，实际 %d 张", len(panels))
	}
	if panels[0].ID >= panels[1].ID {
		t.Fatalf("面板应按创建顺序返回: %+v", panels)
	}

	// ByChannel 作为旧卡片回退路径，返回最早创建的一张。
	oldest, err := st.Panels.ByChannel("30001")
	if err != nil {
		t.Fatalf("ByChannel 查询失败: %v", err)
	}
	if oldest.ID != first.ID {
		t.Fatalf("ByChannel 应返回最早创建的面板，实际 %d", oldest.ID)
	}
}

// TestMigrateDowngradesLegacyPanelChannelIndex 验证历史库中的唯一索引会被降级，
// 否则升级后同一频道仍然只能保留一张面板卡片。
func TestMigrateDowngradesLegacyPanelChannelIndex(t *testing.T) {
	st := newTestStore(t)

	// 模拟旧版本：channel_id 上是唯一索引。
	if err := st.db.Exec("DROP INDEX IF EXISTS idx_panels_channel_id").Error; err != nil {
		t.Fatalf("删除索引失败: %v", err)
	}
	if err := st.db.Exec("CREATE UNIQUE INDEX idx_panels_channel_id ON panels(channel_id)").Error; err != nil {
		t.Fatalf("创建旧唯一索引失败: %v", err)
	}
	ticketType := seedTicketType(t, st, "工单面板")
	if err := st.Panels.Create(&Panel{TypeID: ticketType.ID, ChannelID: "30001", Title: "第一张", Enabled: true}); err != nil {
		t.Fatalf("创建面板失败: %v", err)
	}
	if err := st.Panels.Create(&Panel{TypeID: ticketType.ID, ChannelID: "30001", Title: "第二张", Enabled: true}); err == nil {
		t.Fatal("唯一索引下不应允许同频道第二张面板")
	}

	// 重新迁移后应可写入第二张。
	if err := st.Migrate(); err != nil {
		t.Fatalf("再次迁移失败: %v", err)
	}
	if err := st.Panels.Create(&Panel{TypeID: ticketType.ID, ChannelID: "30001", Title: "第二张", Enabled: true}); err != nil {
		t.Fatalf("迁移后应允许同频道第二张面板: %v", err)
	}
}

// TestTicketTypeEnabledFalsePersists 验证“新建即停用”能如实入库。
//
// GORM 对带 default 标签的字段会忽略零值，Enabled 因此不能带 default:true，
// 否则停用状态会被静默写成启用。
func TestTicketTypeEnabledFalsePersists(t *testing.T) {
	st := newTestStore(t)

	disabledType := &TicketType{Name: "停用的类型", Enabled: false}
	if err := st.Types.Create(disabledType); err != nil {
		t.Fatalf("创建工单类型失败: %v", err)
	}
	storedType, err := st.Types.ByID(disabledType.ID)
	if err != nil {
		t.Fatalf("读取工单类型失败: %v", err)
	}
	if storedType.Enabled {
		t.Fatal("Enabled=false 的工单类型不应被写成启用")
	}

	panel := &Panel{TypeID: disabledType.ID, ChannelID: "30001", Title: "第一张", Enabled: false}
	if err := st.Panels.Create(panel); err != nil {
		t.Fatalf("创建面板失败: %v", err)
	}
	storedPanel, err := st.Panels.ByID(panel.ID)
	if err != nil {
		t.Fatalf("读取面板失败: %v", err)
	}
	if storedPanel.Enabled {
		t.Fatal("Enabled=false 的面板不应被写成启用")
	}
}

// TestMigrateTicketTypesAdoptsLegacyPanels 验证旧版「面板 + 面板级角色」平滑升级为
// 「工单类型 + 类型级角色」：每个面板生成一个同名类型、面板角色被复制去重、
// 历史工单回填类型快照（面板已删除时退化为来源频道），旧角色表被清理。
func TestMigrateTicketTypesAdoptsLegacyPanels(t *testing.T) {
	st := newTestStore(t)

	// 模拟旧版本：panel_roles 表、没有 type_id 的面板。
	if err := st.db.Exec(`CREATE TABLE panel_roles (
		id integer PRIMARY KEY AUTOINCREMENT,
		panel_id integer, role_id text, role_name text, created_at datetime)`).Error; err != nil {
		t.Fatalf("创建旧角色表失败: %v", err)
	}
	insertPanel := func(channelID, channelName, title string) uint {
		t.Helper()
		err := st.db.Exec(
			`INSERT INTO panels (channel_id, channel_name, title, enabled, created_at, updated_at)
			 VALUES (?, ?, ?, 1, '2026-01-05 08:00:00+00:00', '2026-01-05 08:00:00+00:00')`,
			channelID, channelName, title).Error
		if err != nil {
			t.Fatalf("插入旧面板失败: %v", err)
		}
		var id uint
		if err := st.db.Raw("SELECT id FROM panels WHERE title = ?", title).Scan(&id).Error; err != nil {
			t.Fatalf("读取旧面板 ID 失败: %v", err)
		}
		return id
	}
	panelA := insertPanel("30001", "工单面板", "第一张")
	panelB := insertPanel("30002", "充值面板", "第二张")

	insertLegacyRole := func(panelID uint, roleID, roleName string) {
		t.Helper()
		err := st.db.Exec(
			`INSERT INTO panel_roles (panel_id, role_id, role_name, created_at)
			 VALUES (?, ?, ?, '2026-01-05 08:00:00+00:00')`,
			panelID, roleID, roleName).Error
		if err != nil {
			t.Fatalf("插入旧面板角色失败: %v", err)
		}
	}
	insertLegacyRole(panelA, "1003", "实习客服")
	// 重复角色：迁移时应去重，只保留一条。
	insertLegacyRole(panelA, "1003", "实习客服")
	insertLegacyRole(panelB, "1004", "二线客服")

	// 历史工单：一条记录着面板，另一条面板已删除、只剩来源频道。
	withPanel := &Ticket{UserID: "9001", UserName: "小明", SourceChannelID: "30001", Status: TicketClosed}
	if err := st.Tickets.CreateWithNo(withPanel, Now(), time.UTC); err != nil {
		t.Fatalf("创建历史工单失败: %v", err)
	}
	if err := st.db.Model(&Ticket{}).Where("no = ?", withPanel.No).Update("panel_id", panelA).Error; err != nil {
		t.Fatalf("写入历史工单面板失败: %v", err)
	}
	channelOnly := &Ticket{UserID: "9002", UserName: "李雷", SourceChannelID: "30001", Status: TicketClosed}
	if err := st.Tickets.CreateWithNo(channelOnly, Now(), time.UTC); err != nil {
		t.Fatalf("创建历史工单失败: %v", err)
	}

	// 再次迁移等价于升级到新版本。
	if err := st.Migrate(); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	var types []TicketType
	if err := st.db.Order("id ASC").Find(&types).Error; err != nil {
		t.Fatalf("查询工单类型失败: %v", err)
	}
	if len(types) != 2 {
		t.Fatalf("应为每个存量面板各建一个类型，实际 %d 个: %+v", len(types), types)
	}
	if types[0].Name != "工单面板" || types[1].Name != "充值面板" {
		t.Fatalf("迁移生成的类型名应取自频道名: %q / %q", types[0].Name, types[1].Name)
	}
	for _, item := range types {
		if !item.Enabled {
			t.Fatalf("迁移生成的类型应默认启用: %+v", item)
		}
	}

	// 面板已挂到各自类型上。
	storedA, err := st.Panels.ByID(panelA)
	if err != nil || storedA.TypeID != types[0].ID {
		t.Fatalf("面板 A 应归属类型 %d，实际 %+v（err=%v）", types[0].ID, storedA, err)
	}
	storedB, err := st.Panels.ByID(panelB)
	if err != nil || storedB.TypeID != types[1].ID {
		t.Fatalf("面板 B 应归属类型 %d，实际 %+v（err=%v）", types[1].ID, storedB, err)
	}

	// 面板角色复制为类型角色，且按 role_id 去重。
	withRoles, err := st.Types.ByID(types[0].ID)
	if err != nil {
		t.Fatalf("读取类型失败: %v", err)
	}
	if len(withRoles.Roles) != 1 || withRoles.Roles[0].RoleID != "1003" || withRoles.Roles[0].RoleName != "实习客服" {
		t.Fatalf("类型角色复制异常（应去重为 1 条）: %+v", withRoles.Roles)
	}
	otherType, err := st.Types.ByID(types[1].ID)
	if err != nil || len(otherType.Roles) != 1 || otherType.Roles[0].RoleID != "1004" {
		t.Fatalf("第二个类型的角色复制异常: %+v（err=%v）", otherType.Roles, err)
	}

	// 历史工单回填类型快照：按面板与按来源频道两条路径都要命中。
	first, err := st.Tickets.ByNo(withPanel.No)
	if err != nil {
		t.Fatalf("读取历史工单失败: %v", err)
	}
	if first.TypeID == nil || *first.TypeID != types[0].ID || first.TypeName != "工单面板" {
		t.Fatalf("按面板归属的工单未回填类型: %+v", first)
	}
	second, err := st.Tickets.ByNo(channelOnly.No)
	if err != nil {
		t.Fatalf("读取历史工单失败: %v", err)
	}
	if second.TypeID == nil || *second.TypeID != types[0].ID || second.TypeName != "工单面板" {
		t.Fatalf("按来源频道归属的工单未回填类型: %+v", second)
	}

	// 旧角色表已清理，避免留下死数据。
	exists, err := tableExists(st.db, "panel_roles")
	if err != nil {
		t.Fatalf("检查旧表失败: %v", err)
	}
	if exists {
		t.Fatal("升级后旧面板角色表应被删除")
	}
}

// TestTypesListByChannel 验证按频道反查工单类型（/aar 与权限判定依赖）。
func TestTypesListByChannel(t *testing.T) {
	st := newTestStore(t)
	first := seedTicketType(t, st, "账号与充值")
	second := seedTicketType(t, st, "举报与投诉")

	if err := st.Panels.Create(&Panel{TypeID: first.ID, ChannelID: "30001", Title: "A", Enabled: true}); err != nil {
		t.Fatalf("创建面板失败: %v", err)
	}
	if err := st.Panels.Create(&Panel{TypeID: second.ID, ChannelID: "30001", Title: "B", Enabled: true}); err != nil {
		t.Fatalf("创建面板失败: %v", err)
	}
	if err := st.Panels.Create(&Panel{TypeID: second.ID, ChannelID: "30001", Title: "C", Enabled: true}); err != nil {
		t.Fatalf("创建面板失败: %v", err)
	}

	types, err := st.Types.ListByChannel("30001")
	if err != nil {
		t.Fatalf("按频道查询类型失败: %v", err)
	}
	if len(types) != 2 || types[0].ID != first.ID || types[1].ID != second.ID {
		t.Fatalf("同频道的类型应去重并按创建顺序返回: %+v", types)
	}
	if types, err := st.Types.ListByChannel(""); err != nil || types != nil {
		t.Fatalf("空频道应返回空结果: %+v（err=%v）", types, err)
	}
}
