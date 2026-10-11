package bot

import (
	"context"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// SendAIReply 把 AI 生成的回复发到工单频道并归档（实现 ai.Sender）。
//
// 顺序：先发 KOOK 消息、成功后才写时间线——与面板开单提示的约定一致，
// 避免「时间线有记录但频道里没有」的脏数据。
// 内容经提及转义：模型可能复述用户消息里的 (met) 语法，不转义会被渲染成
// 任意提及（包括 @全体成员），与归档消息的转义策略保持一致。
func (b *Bot) SendAIReply(ctx context.Context, t *store.Ticket, content string) error {
	client, _, err := b.ready()
	if err != nil {
		return err
	}
	content = kook.EscapeMentionText(content)
	if _, err := client.SendChannelMessage(ctx, t.ChannelID, kook.MsgTypeKMarkdown, content, kook.MessageOptions{}); err != nil {
		return err
	}
	_, err = b.deps.Tickets.AddAIMessage(ctx, t.No, t.ChannelID, content)
	return err
}

// notifyAIMessage 在归档一条工单频道消息后通知 AI 服务。
//
// 角色判定：
//   - 开单人（t.UserID）的消息 → OnUserMessage（防抖安排自动回复）；
//   - 其他人的消息 → 先按工单管理员判定（isTicketAdmin 带角色缓存），
//     是管理员则 OnStaffMessage（接管 + 知识捕获），否则忽略
//     （工单频道理论上只有开单人与管理员可发言）。
//
// AI 服务的判定全部在 worker/计时器里异步进行，这里只做一次带缓存的角色查询，
// 不会阻塞事件分片协程。
func (b *Bot) notifyAIMessage(ctx context.Context, t *store.Ticket, message *store.TicketMessage) {
	if b.deps.AI == nil || message == nil || message.IsBot {
		return
	}
	if message.UserID == t.UserID {
		b.deps.AI.OnUserMessage(ctx, t, message)
		return
	}
	isStaff := b.isTicketAdmin(ctx, message.UserID, t)
	b.deps.AI.OnStaffMessage(ctx, t, message, isStaff)
}

// triggerCloseSummary 在关单后按配置触发 AI 摘要（尽力而为）。
func (b *Bot) triggerCloseSummary(no string) {
	cfg, err := b.deps.Store.AI.Config(b.deps.AppSecret)
	if err != nil || !cfg.Enabled || !cfg.AutoSummaryOnClose {
		return
	}
	if err := b.deps.AI.TriggerSummary(no); err != nil {
		b.deps.Logger.Warn("关单摘要任务入队失败", "ticket_no", no, "err", err)
	}
}
