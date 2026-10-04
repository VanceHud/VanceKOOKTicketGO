package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"vancekookticket/internal/kook"
	"vancekookticket/internal/store"
	"vancekookticket/internal/ticket"
)

// platform 实现 ticket.Platform：把工单业务需要 KOOK 执行的动作落到真实接口上。
type platform struct {
	b *Bot
}

// SetUserSpeak 设置开单人在工单频道的发言权限。
//
// 权限位说明：2048 = 查看频道，4096 = 发送消息。
//   - allow=true：allow=2048|4096（可看可发）
//   - allow=false：allow=2048, deny=4096（可看不可发）
func (p *platform) SetUserSpeak(ctx context.Context, channelID, userID string, allow bool) error {
	client, _, err := p.b.ready()
	if err != nil {
		return err
	}
	allowBits := kook.PermissionAllText
	denyBits := 0
	if !allow {
		allowBits = kook.PermissionViewChannel
		denyBits = kook.PermissionSendMessage
	}

	// 权限覆写可能已存在（例如重复锁定/解锁），创建失败不必中断流程。
	if err := client.ChannelRoleCreate(ctx, channelID, "user_id", userID); err != nil {
		p.b.deps.Logger.Debug("创建频道用户权限覆写失败（可能已存在）", "channel_id", channelID, "err", err)
	}
	return client.ChannelRoleUpdate(ctx, channelID, "user_id", userID, allowBits, denyBits)
}

// NotifyLocked 在工单频道发送锁定提示卡片（含“重新激活”按钮）。
func (p *platform) NotifyLocked(ctx context.Context, t *store.Ticket, actor ticket.Actor, reason string) error {
	client, _, err := p.b.ready()
	if err != nil {
		return err
	}
	reasonText := "人工锁定"
	if reason == store.LockReasonTimeout {
		hours := store.DefaultOutdateHours
		if cfg, cfgErr := p.b.currentConfig(ctx); cfgErr == nil && cfg.OutdateHours > 0 {
			hours = cfg.OutdateHours
		}
		reasonText = fmt.Sprintf("超过 %d 小时无活动，已自动锁定", hours)
	}

	reopenValue := p.b.encodeButton(actionReopen, t.No, t.ChannelID, 0)
	card := p.b.lockCard(t, actor, reopenValue, reasonText)
	_, err = client.SendChannelMessage(ctx, t.ChannelID, kook.MsgTypeCard, card, kook.MessageOptions{})
	return err
}

// NotifyReopened 在工单频道发送重新激活提示卡片。
func (p *platform) NotifyReopened(ctx context.Context, t *store.Ticket, actor ticket.Actor) error {
	client, _, err := p.b.ready()
	if err != nil {
		return err
	}
	content := kook.NoticeCard(kook.CardThemeSuccess,
		fmt.Sprintf("工单「%s」已重新激活", t.No),
		fmt.Sprintf("操作人：%s\n时间：%s\n%s 恢复发言权限",
			kook.MentionUser(firstNonEmpty(actor.ID, "system")),
			p.b.formatTime(store.Now()),
			kook.MentionUser(t.UserID),
		))
	_, err = client.SendChannelMessage(ctx, t.ChannelID, kook.MsgTypeCard, content, kook.MessageOptions{})
	return err
}

// NotifyClosed 发送关闭通知：日志频道 + 私聊开单人。
//
// 容错策略：两处通知都是尽力而为——
// 通知失败不应阻止“工单关闭”这一事实，否则会出现权限/频道状态与数据库不一致。
// 因此这里只记录警告，并把能拿到的消息 ID 返回给业务层（用于后续 /tkcm 更新卡片）。
//
// 开单人私信被屏蔽时，会在日志频道额外发一张提醒卡片并写入时间线——
// 这是开单环节不再发「私信探测」消息后的兜底，否则开单人会拿不到任何关闭记录。
func (p *platform) NotifyClosed(ctx context.Context, t *store.Ticket, actor ticket.Actor, note string) (string, string, error) {
	client, cfg, err := p.b.ready()
	if err != nil {
		return "", "", err
	}

	content := p.b.closedCard(t, actor, note)
	logMsgID, userMsgID := "", ""

	if cfg.LogChannelID != "" {
		msg, err := client.SendChannelMessage(ctx, cfg.LogChannelID, kook.MsgTypeCard, content, kook.MessageOptions{})
		if err != nil {
			p.b.deps.Logger.Warn("向日志频道发送关闭通知失败",
				"ticket_no", t.No, "channel_id", cfg.LogChannelID, "err", err)
		} else {
			logMsgID = msg.ID
		}
	}

	if t.UserID != "" {
		msg, err := client.SendDirectMessage(ctx, t.UserID, kook.MsgTypeCard, content, kook.MessageOptions{})
		switch {
		case err == nil:
			userMsgID = msg.ID
		case isDirectMessageBlocked(err):
			p.b.deps.Logger.Info("开单人未开启私聊，跳过关闭通知", "ticket_no", t.No, "user_id", t.UserID)
			p.warnDirectMessageUndeliverable(ctx, t)
		default:
			p.b.deps.Logger.Warn("向开单人发送关闭通知失败",
				"ticket_no", t.No, "user_id", t.UserID, "err", err)
		}
	}

	return logMsgID, userMsgID, nil
}

// warnDirectMessageUndeliverable 告知管理员「关闭通知没能私信送到开单人」。
//
// 关闭卡片已经发往日志频道，但开单人不会看到它；这里再补一张卡片，
// 把需要人工转达这件事显式摆到管理员面前，并写进时间线供 WebUI 追溯。
func (p *platform) warnDirectMessageUndeliverable(ctx context.Context, t *store.Ticket) {
	client, cfg, err := p.b.ready()
	if err != nil {
		return
	}
	const timeline = "开单人未开启私聊，关闭通知未能私信送达"

	if cfg.LogChannelID != "" {
		notice := kook.NoticeCard(kook.CardThemeWarning,
			fmt.Sprintf("工单「%s」的关闭通知未能私信送达", t.No),
			fmt.Sprintf("开单人 %s 未开启私聊，收不到关闭记录。\n请人工转达，或让其先私聊机器人任意一条消息后再开单。",
				kook.MentionUser(t.UserID)))
		if _, err := client.SendChannelMessage(ctx, cfg.LogChannelID, kook.MsgTypeCard, notice, kook.MessageOptions{}); err != nil {
			p.b.deps.Logger.Warn("发送私信未送达提醒失败", "ticket_no", t.No, "err", err)
		}
	}

	if _, err := p.b.deps.Tickets.AddBotMessage(ctx, t.No, t.ChannelID, timeline); err != nil {
		p.b.deps.Logger.Debug("写入私信未送达时间线失败", "ticket_no", t.No, "err", err)
	}
}

// CloseTicketChannel 删除工单频道。
func (p *platform) CloseTicketChannel(ctx context.Context, channelID string) error {
	client, _, err := p.b.ready()
	if err != nil {
		return err
	}
	if err := client.ChannelDelete(ctx, channelID); err != nil {
		// 频道已经不存在时视为成功，避免重复关闭时报错。
		if errors.Is(err, kook.ErrNotFound) {
			return nil
		}
		return err
	}
	return nil
}

// isDirectMessageBlocked 判断私信失败是否因为对方未开启私聊。
//
// KOOK 在用户未开启私聊或屏蔽机器人时返回权限类错误；
// 这里按错误类别判断，而不是像参考实现那样匹配中文错误文案。
func isDirectMessageBlocked(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *kook.APIError
	if errors.As(err, &apiErr) {
		if errors.Is(apiErr, kook.ErrPermissionDenied) {
			return true
		}
		// KOOK 在私聊被屏蔽时可能返回 400 与特定文案，这里做保守匹配。
		if apiErr.HTTPStatus == 400 && strings.Contains(apiErr.Message, "私聊") {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
