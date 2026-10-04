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

	first := &Panel{ChannelID: "30001", ChannelName: "工单面板", Title: "第一张", Enabled: true}
	if err := st.Panels.Create(first); err != nil {
		t.Fatalf("创建第一张面板失败: %v", err)
	}
	second := &Panel{ChannelID: "30001", ChannelName: "工单面板", Title: "第二张", Enabled: true}
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
	if err := st.Panels.Create(&Panel{ChannelID: "30001", Title: "第一张", Enabled: true}); err != nil {
		t.Fatalf("创建面板失败: %v", err)
	}
	if err := st.Panels.Create(&Panel{ChannelID: "30001", Title: "第二张", Enabled: true}); err == nil {
		t.Fatal("唯一索引下不应允许同频道第二张面板")
	}

	// 重新迁移后应可写入第二张。
	if err := st.Migrate(); err != nil {
		t.Fatalf("再次迁移失败: %v", err)
	}
	if err := st.Panels.Create(&Panel{ChannelID: "30001", Title: "第二张", Enabled: true}); err != nil {
		t.Fatalf("迁移后应允许同频道第二张面板: %v", err)
	}
}
