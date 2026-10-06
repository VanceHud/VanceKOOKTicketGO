// Package dryrun 在 KOOK_DRYRUN=1 时写入演示数据。
//
// 目的：让 WebUI 在完全不接入 KOOK（无 token、无公网、无服务器）的情况下
// 也能完整演示列表、筛选、时间线、统计看板等界面，便于先验收再接入。
//
// 幂等：仅当工单表为空时写入，重复启动不会产生重复数据。
package dryrun

import (
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// demoUserID 生成演示用的 KOOK 风格 ID（未接入 KOOK 时不存在冲突风险）。
func demoUserID(n int) string { return fmt.Sprintf("90%015d", n) }

var (
	demoNames = []string{"小张", "李雷", "韩梅梅", "王芳", "赵强", "孙悦", "周涛", "吴敏", "郑凯", "冯雨"}
	demoAsks  = []string{
		"充值没有到账，订单号 2601058891",
		"机器人 /ticket 命令没有反应",
		"我在子频道无法发言，提示权限不足",
		"申请开通音乐频道",
		"举报一个刷屏的用户",
		"角色颜色设置不生效",
		"想申请加入管理组，需要什么条件？",
		"服务器最近有点卡，麻烦看下",
		"我的表情角色没有发放成功",
		"频道名字打错了，可以帮忙改吗",
	}
	demoReplies = []string{
		"收到，我看一下，请稍等",
		"麻烦提供一下订单号，我这边帮你查",
		"已经为你恢复了权限，请刷新后重试",
		"处理完成，有问题随时再开单",
		"这个问题需要上级确认，稍后回复你",
	}
	demoNotes = []string{
		"用户已确认解决，可以关闭",
		"需要财务核对订单，已转交",
		"重复开单，已合并处理",
	}
)

// Seed 写入演示数据。当工单表非空时直接返回。
func Seed(st *store.Store, loc *time.Location, log *slog.Logger) error {
	var existing int64
	if err := st.DB().Model(&store.Ticket{}).Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		log.Debug("演示数据已存在，跳过写入", "tickets", existing)
		return nil
	}

	// 使用固定种子的伪随机，保证每次演示数据的形态一致，便于对照界面。
	rng := rand.New(rand.NewSource(20260105))
	now := store.Now()

	if err := seedPanelsAndRoles(st, log); err != nil {
		return err
	}
	if err := seedActivity(st, log); err != nil {
		return err
	}
	if err := seedTickets(st, loc, rng, now, log); err != nil {
		return err
	}
	if err := seedEmojiRules(st, log); err != nil {
		return err
	}
	if err := seedAudit(st, now); err != nil {
		return err
	}
	log.Info("已写入 DryRun 演示数据", "tickets", 30)
	return nil
}

func seedPanelsAndRoles(st *store.Store, log *slog.Logger) error {
	// 演示三类工单类型，其中一类默认停用，便于在线验收启用开关与“停用后不能开单”的效果。
	types := []*store.TicketType{
		{Name: "账号与充值", Description: "账号异常、充值未到账、订单问题", Enabled: true},
		{Name: "举报与投诉", Description: "处理刷屏、骚扰、违规内容", Enabled: true},
		{Name: "功能建议", Description: "服务器与机器人的改进建议", Enabled: false},
	}
	for _, item := range types {
		if err := st.Types.Create(item); err != nil {
			return err
		}
	}
	if err := st.Types.AddRole(types[0].ID, demoUserID(11), "客服组"); err != nil {
		return err
	}
	if err := st.Types.AddRole(types[1].ID, demoUserID(13), "版主组"); err != nil {
		return err
	}

	openMessage := "你好 {user}，工单 **{ticket_no}**（类型：{type}）已创建（{time}）。\n请提供以下信息，便于我们尽快处理：\n1. 订单号或问题截图\n2. 问题发生的大致时间\n\n补充信息可直接在本频道回复。"
	panels := []*store.Panel{
		{
			TypeID:      types[0].ID,
			ChannelID:   demoUserID(2),
			ChannelName: "工单面板",
			MsgID:       demoUserID(3),
			Title:       "# 点击按钮发起工单\n\n**处理范围**\n- 账号问题\n- 充值问题",
			ButtonText:  "ticket",
			OpenMessage: openMessage,
			Enabled:     true,
		},
		{
			TypeID:      types[0].ID,
			ChannelID:   demoUserID(2),
			ChannelName: "工单面板",
			MsgID:       demoUserID(4),
			Title:       "# 充值问题专用入口",
			ButtonText:  "充值工单",
			Enabled:     true,
		},
		{
			TypeID:      types[1].ID,
			ChannelID:   demoUserID(5),
			ChannelName: "举报与投诉",
			MsgID:       demoUserID(6),
			Title:       "# 举报入口\n\n请准备好截图或聊天记录，管理员会尽快处理。",
			ButtonText:  "举报",
			Enabled:     true,
		},
	}
	for _, panel := range panels {
		if err := st.Panels.Create(panel); err != nil {
			return err
		}
	}

	if err := st.Roles.AddAdmin(demoUserID(10), "服主"); err != nil {
		return err
	}
	mappings := []struct{ role, name, web string }{
		{demoUserID(10), "服主", store.RoleAdmin},
		{demoUserID(11), "客服组", store.RoleStaff},
		{demoUserID(12), "实习客服", store.RoleReadonly},
	}
	for _, m := range mappings {
		if err := st.Roles.UpsertMapping(&store.RoleMapping{
			KookRoleID:   m.role,
			KookRoleName: m.name,
			WebRole:      m.web,
		}); err != nil {
			return err
		}
	}
	log.Debug("演示数据：工单类型、面板与角色已写入", "types", len(types), "panels", len(panels))
	return nil
}

// seedActivity 写入一条演示用的在玩动态，便于离线验收「机器人动态」页。
//
// 游戏 ID / 名称与 handleGameList 的 DryRun 演示数据保持一致。
func seedActivity(st *store.Store, log *slog.Logger) error {
	if err := st.Activity.Replace(&store.BotActivity{
		DataType:  1,
		GameID:    111111,
		GameName:  "CS",
		Actor:     "demo",
		StartedAt: store.Now(),
	}); err != nil {
		return err
	}
	log.Debug("演示数据：在玩动态已写入")
	return nil
}

func seedTickets(st *store.Store, loc *time.Location, rng *rand.Rand, now time.Time, log *slog.Logger) error {
	// 演示工单按顺序轮流归属于已创建的类型，便于统计看板看到类型分布。
	seededTypes, err := st.Types.List()
	if err != nil {
		return err
	}

	for i := 0; i < 30; i++ {
		userID := demoUserID(100 + i)
		userName := demoNames[i%len(demoNames)]
		// 前 3 条固定在今天之内，保证仪表盘的“今日”指标不为空；其余分散在最近 14 天。
		var started time.Time
		if i < 3 {
			started = now.Add(-time.Duration(i*70+5) * time.Minute)
		} else {
			started = now.Add(-time.Duration(12+rng.Intn(14*24-12)) * time.Hour).
				Add(-time.Duration(rng.Intn(60)) * time.Minute)
		}

		t := &store.Ticket{
			UserID:          userID,
			UserName:        userName,
			SourceChannelID: demoUserID(2),
			ChannelID:       demoUserID(500 + i),
			Status:          store.TicketPending,
			StartedAt:       started,
		}
		typeLabel := ""
		if len(seededTypes) > 0 {
			item := seededTypes[i%len(seededTypes)]
			t.TypeID = &item.ID
			t.TypeName = item.Name
			typeLabel = item.Name
		}
		if err := st.Tickets.CreateWithNo(t, started, loc); err != nil {
			return err
		}
		t.PanelID = nil

		// 第一条：机器人开单卡片（系统消息）。
		summary := fmt.Sprintf("%s 发起了工单，等待管理员处理", userName)
		if typeLabel != "" {
			summary = fmt.Sprintf("[%s] %s", typeLabel, summary)
		}
		if err := st.Tickets.AddMessage(&store.TicketMessage{
			TicketNo:  t.No,
			MsgID:     demoUserID(9000 + i),
			ChannelID: t.ChannelID,
			UserID:    "bot",
			UserName:  "TicketBot",
			Content:   summary,
			MsgType:   store.MsgTypeSystem,
			IsBot:     true,
			CreatedAt: started,
		}); err != nil {
			return err
		}
		// 用户描述问题。
		if err := st.Tickets.AddMessage(&store.TicketMessage{
			TicketNo:  t.No,
			MsgID:     demoUserID(9100 + i),
			ChannelID: t.ChannelID,
			UserID:    userID,
			UserName:  userName,
			Content:   demoAsks[i%len(demoAsks)],
			MsgType:   store.MsgTypeText,
			CreatedAt: started.Add(time.Minute),
		}); err != nil {
			return err
		}

		// 三分之二的工单有客服回复（因此 first_reply_at 会被自动写入）。
		hasReply := i%3 != 2
		if hasReply {
			replyAt := started.Add(time.Duration(5+rng.Intn(55)) * time.Minute)
			if err := st.Tickets.AddMessage(&store.TicketMessage{
				TicketNo:  t.No,
				MsgID:     demoUserID(9200 + i),
				ChannelID: t.ChannelID,
				UserID:    demoUserID(101),
				UserName:  "客服小林",
				Content:   demoReplies[i%len(demoReplies)],
				MsgType:   store.MsgTypeText,
				CreatedAt: replyAt,
			}); err != nil {
				return err
			}
			// 部分工单追加一条图片消息，验证记录页对非文本类型的展示。
			if i%4 == 0 {
				imageURL := "https://placehold.co/640x360/png?text=KOOK+Ticket"
				if err := st.Tickets.AddMessage(&store.TicketMessage{
					TicketNo:  t.No,
					MsgID:     demoUserID(9300 + i),
					ChannelID: t.ChannelID,
					UserID:    userID,
					UserName:  userName,
					Content:   "[图片] " + imageURL,
					MsgType:   store.MsgTypeImage,
					MediaURL:  imageURL,
					MediaName: "screenshot.png",
					MediaType: "image/png",
					CreatedAt: replyAt.Add(2 * time.Minute),
				}); err != nil {
					return err
				}
			}
			// 部分工单追加一条卡片消息，验证卡片渲染（含文件模块）。
			if i%5 == 0 {
				cardJSON := `[{"type":"card","theme":"info","modules":[` +
					`{"type":"header","text":{"type":"plain-text","content":"处理进度"}},` +
					`{"type":"section","text":{"type":"kmarkdown","content":"已定位到问题原因，**预计今天内完成**"}},` +
					`{"type":"context","elements":[{"type":"plain-text","content":"TicketBot 自动同步"}]},` +
					`{"type":"file","src":"https://example.com/report.pdf","title":"处理报告.pdf"}]}]`
				if err := st.Tickets.AddMessage(&store.TicketMessage{
					TicketNo:  t.No,
					MsgID:     demoUserID(9400 + i),
					ChannelID: t.ChannelID,
					UserID:    "bot",
					UserName:  "TicketBot",
					Content:   "[卡片消息] 处理进度\n已定位到问题原因，**预计今天内完成**\nTicketBot 自动同步\n[文件] 处理报告.pdf",
					MsgType:   store.MsgTypeCard,
					CardJSON:  cardJSON,
					MediaURL:  "https://example.com/report.pdf",
					MediaName: "处理报告.pdf",
					MediaType: "file",
					IsBot:     true,
					CreatedAt: replyAt.Add(4 * time.Minute),
				}); err != nil {
					return err
				}
			}
		}

		fields := map[string]any{}
		switch i % 5 {
		case 0:
			// 进行中
			fields["status"] = store.TicketOpen
		case 1:
			// 已锁定（人工）
			lockedAt := started.Add(2 * time.Hour)
			fields["status"] = store.TicketLocked
			fields["locked_at"] = lockedAt
			fields["lock_reason"] = store.LockReasonManual
		default:
			// 已关闭
			closedAt := started.Add(time.Duration(1+rng.Intn(20)) * time.Hour)
			fields["status"] = store.TicketClosed
			fields["closed_at"] = closedAt
			fields["closed_by"] = demoUserID(101)
			fields["closed_by_name"] = "客服小林"
			fields["log_channel_msg_id"] = demoUserID(8000 + i)
		}
		if err := st.Tickets.UpdateFields(t.No, fields); err != nil {
			return err
		}

		// 部分已关闭工单带备注。
		if fields["status"] == store.TicketClosed && i%3 == 0 {
			if err := st.Tickets.AddNote(&store.TicketNote{
				TicketNo:   t.No,
				AuthorID:   demoUserID(101),
				AuthorName: "客服小林",
				Source:     "web",
				Content:    demoNotes[i%len(demoNotes)],
				CreatedAt:  started.Add(25 * time.Hour),
			}); err != nil {
				return err
			}
		}
	}
	log.Debug("演示数据：工单与消息已写入")
	return nil
}

func seedEmojiRules(st *store.Store, log *slog.Logger) error {
	rules := []store.EmojiRule{
		{MessageID: demoUserID(7001), ChannelID: demoUserID(2), EmojiID: "❤", RoleID: demoUserID(20), Label: "红色组", Enabled: true},
		{MessageID: demoUserID(7001), ChannelID: demoUserID(2), EmojiID: "💙", RoleID: demoUserID(21), Label: "蓝色组", Enabled: true},
	}
	for i := range rules {
		if err := st.Emoji.CreateRule(&rules[i]); err != nil {
			return err
		}
	}
	log.Debug("演示数据：表情上角色规则已写入")
	return nil
}

func seedAudit(st *store.Store, now time.Time) error {
	entries := []store.AuditLog{
		{Actor: "system", ActorType: store.ActorTypeBot, Action: "system.bootstrap", Target: "admin", Detail: "首次启动创建初始管理员账号"},
		{Actor: "admin", ActorType: store.ActorTypeWeb, Action: "auth.login.success", Target: "admin", Detail: "登录成功"},
		{Actor: "admin", ActorType: store.ActorTypeWeb, Action: "settings.update", Target: "settings", Detail: "更新配置项：outdate_hours"},
		{Actor: "客服小林", ActorType: store.ActorTypeWeb, Action: "ticket.close", Target: demoUserID(0), Detail: "关闭工单（演示数据）"},
		{Actor: "客服小林", ActorType: store.ActorTypeWeb, Action: "ticket.export", Target: demoUserID(0), Detail: "导出聊天记录（格式：csv，消息数：4）"},
	}
	for i := range entries {
		entries[i].CreatedAt = now.Add(-time.Duration(i) * 37 * time.Minute)
		entries[i].IP = "127.0.0.1"
		if err := st.Audit.Write(&entries[i]); err != nil {
			return err
		}
	}
	return nil
}
