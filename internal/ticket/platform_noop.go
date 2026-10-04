package ticket

import (
	"context"
	"log/slog"

	"vancekookticket/internal/store"
)

// NoopPlatform 是 KOOK 不可用时的平台实现。
//
// 用途：KOOK_DRYRUN=1 的离线演示模式，以及 KOOK 尚未连接时的降级运行。
// 它不产生任何副作用，只把将要执行的动作写入调试日志，便于核对业务流程。
type NoopPlatform struct {
	log *slog.Logger
}

// NewNoopPlatform 创建空实现。
func NewNoopPlatform(log *slog.Logger) *NoopPlatform {
	if log == nil {
		log = slog.Default()
	}
	return &NoopPlatform{log: log}
}

// SetUserSpeak 记录一次“设置发言权限”的意图。
func (p *NoopPlatform) SetUserSpeak(_ context.Context, channelID, userID string, allow bool) error {
	p.log.Debug("dry-run: 跳过频道发言权限设置",
		"channel_id", channelID, "user_id", userID, "allow", allow)
	return nil
}

// NotifyClosed 记录一次“关闭通知”意图并返回空消息 ID。
func (p *NoopPlatform) NotifyClosed(_ context.Context, t *store.Ticket, actor Actor, note string) (string, string, error) {
	p.log.Debug("dry-run: 跳过关闭通知发送",
		"ticket_no", t.No, "channel_id", t.ChannelID, "actor", actor.Name, "note", note)
	return "", "", nil
}

// NotifyLocked 记录一次“锁定通知”意图。
func (p *NoopPlatform) NotifyLocked(_ context.Context, t *store.Ticket, actor Actor, reason string) error {
	p.log.Debug("dry-run: 跳过锁定通知发送",
		"ticket_no", t.No, "channel_id", t.ChannelID, "actor", actor.Name, "reason", reason)
	return nil
}

// NotifyReopened 记录一次“重新激活通知”意图。
func (p *NoopPlatform) NotifyReopened(_ context.Context, t *store.Ticket, actor Actor) error {
	p.log.Debug("dry-run: 跳过重新激活通知发送",
		"ticket_no", t.No, "channel_id", t.ChannelID, "actor", actor.Name)
	return nil
}

// CloseTicketChannel 记录一次“删除频道”意图。
func (p *NoopPlatform) CloseTicketChannel(_ context.Context, channelID string) error {
	p.log.Debug("dry-run: 跳过工单频道删除", "channel_id", channelID)
	return nil
}
