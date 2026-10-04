package bot

import (
	"fmt"
	"strings"
	"time"

	"vancekookticket/internal/kook"
	"vancekookticket/internal/store"
	"vancekookticket/internal/ticket"
)

// 机器人发送的卡片文案集中在这里，便于统一调整。

// formatTime 按配置时区格式化时间。
func (b *Bot) formatTime(t time.Time) string {
	return t.In(b.deps.Location).Format("2006-01-02 15:04:05")
}

// panelCard 生成工单面板卡片（含开单按钮）。
//
// 文案会先经 kook.NormalizePanelText 归一化：KOOK 的 KMarkdown 并不支持
// "# 标题"、"__下划线__"、"- 列表" 这些 Markdown 写法（WebUI 预览支持，
// 发到 KOOK 却原样显示）。首行标题会改用卡片 header 模块展示，
// 这是 KOOK 里唯一的大标题形式。
func (b *Bot) panelCard(title, buttonText, openValue string) string {
	text := kook.NormalizePanelText(title)

	card := kook.NewCard(kook.CardThemePrimary)
	if text.Heading != "" {
		card.Header(text.Heading)
	}
	if strings.TrimSpace(text.Body) != "" {
		card.KMarkdownSection(text.Body)
	}
	card.ActionGroup(kook.Button{Text: buttonText, Value: openValue, Theme: kook.CardThemePrimary})

	content, err := kook.SingleCard(card)
	if err != nil {
		return title
	}
	return content
}

// panelOpenMessage 渲染面板自定义的开单提示。
//
// 支持变量：{user} 提及开单人、{user_name} 开单人昵称、{ticket_no} 工单编号、{time} 开单时间；
// 未识别的花括号内容原样保留，避免误伤正常文案。
// 昵称经转义处理：KOOK 昵称可能包含 (met)all(met) 之类的提及语法，不能直接回填。
//
// 该内容以 KMarkdown 文本消息发送（不是卡片，没有 header 模块可用），
// 因此 KOOK 不支持的 "# 标题"、"__下划线__"、"- 列表" 会先转换成
// 加粗、(ins) 与「• 」，避免管理员在 WebUI 里看到效果、发到 KOOK 却是原文。
func (b *Bot) panelOpenMessage(template string, t *store.Ticket) string {
	replacer := strings.NewReplacer(
		"{user}", kook.MentionUser(t.UserID),
		"{user_name}", kook.EscapeMentionText(firstNonEmpty(t.UserName, t.UserID)),
		"{ticket_no}", t.No,
		"{time}", b.formatTime(t.StartedAt),
	)
	return kook.NormalizeKMarkdown(replacer.Replace(template))
}

// ticketCard 生成工单频道内的首条卡片（含关闭与锁定按钮）。
func (b *Bot) ticketCard(t *store.Ticket, adminRoleIDs []string, closeValue, lockValue string) string {
	var mentions strings.Builder
	for _, roleID := range adminRoleIDs {
		mentions.WriteString(kook.MentionRole(roleID))
		mentions.WriteString(" ")
	}

	card := kook.NewCard(kook.CardThemePrimary).
		KMarkdownSection(fmt.Sprintf(
			"%s 发起了工单，请等待管理员回复\n工单编号：**%s**\n开启时间：%s\n%s",
			kook.MentionUser(t.UserID),
			t.No,
			b.formatTime(t.StartedAt),
			strings.TrimSpace(mentions.String()),
		)).
		Divider().
		KMarkdownSection("处理结束后请点击「关闭」；也可先「锁定」暂停用户发言（用户仍可查看频道）。").
		ActionGroup(
			kook.Button{Text: "关闭", Value: closeValue, Theme: kook.CardThemeDanger},
			kook.Button{Text: "锁定", Value: lockValue, Theme: kook.CardThemeWarning},
		)

	content, err := kook.SingleCard(card)
	if err != nil {
		return fmt.Sprintf("工单 %s 已创建", t.No)
	}
	return content
}

// closedCard 生成关闭通知卡片（同时发往日志频道与开单人私聊）。
func (b *Bot) closedCard(t *store.Ticket, actor ticket.Actor, note string) string {
	closer := actor.ID
	if closer == "" {
		closer = "system"
	}

	text := fmt.Sprintf(
		"开启时间：%s\n发起用户：%s\n关闭时间：%s\n关闭用户：%s",
		b.formatTime(t.StartedAt),
		kook.MentionUser(t.UserID),
		b.formatTime(store.Now()),
		kook.MentionUser(closer),
	)
	if strings.TrimSpace(note) != "" {
		// 备注来自管理员输入，转义其中的提及语法，避免伪造 @全体成员。
		text += "\n\n关闭说明：\n> " + kook.EscapeMentionText(note)
	}

	card := kook.NewCard(kook.CardThemeSuccess).
		Header(fmt.Sprintf("工单 ticket.%s 已关闭", t.No)).
		Divider().
		KMarkdownSection(text)

	content, err := kook.SingleCard(card)
	if err != nil {
		return fmt.Sprintf("工单 %s 已关闭", t.No)
	}
	return content
}

// lockCard 生成锁定提示卡片（含重新激活按钮）。
func (b *Bot) lockCard(t *store.Ticket, actor ticket.Actor, reopenValue, reasonText string) string {
	operator := actor.ID
	if operator == "" {
		operator = "system"
	}
	text := fmt.Sprintf(
		"当前工单已进入锁定状态，用户无法发言\n原因：%s\n操作时间：%s\n工单用户：%s\n操作用户：%s",
		kook.EscapeMentionText(reasonText),
		b.formatTime(store.Now()),
		kook.MentionUser(t.UserID),
		kook.MentionUser(operator),
	)

	card := kook.NewCard(kook.CardThemeWarning).
		Header(fmt.Sprintf("工单「%s」已锁定", t.No)).
		KMarkdownSection(text).
		ActionGroup(kook.Button{Text: "重新激活", Value: reopenValue, Theme: kook.CardThemePrimary})

	content, err := kook.SingleCard(card)
	if err != nil {
		return fmt.Sprintf("工单 %s 已锁定", t.No)
	}
	return content
}

// ticketLogCard 生成日志卡片内容，供 /tkcm 备注后刷新。
func (b *Bot) ticketLogCard(t *store.Ticket, notes []store.TicketNote) string {
	text := fmt.Sprintf(
		"开启时间：%s\n发起用户：%s\n结束时间：%s\n关闭用户：%s",
		b.formatTime(t.StartedAt),
		kook.MentionUser(t.UserID),
		b.formatTime(derefTime(t.ClosedAt, store.Now())),
		kook.MentionUser(firstNonEmpty(t.ClosedBy, "system")),
	)

	for _, note := range notes {
		text += fmt.Sprintf("\n\n来自 %s 的备注：\n> %s",
			kook.MentionUser(firstNonEmpty(note.AuthorID, "system")),
			kook.EscapeMentionText(note.Content),
		)
	}

	card := kook.NewCard(kook.CardThemeSuccess).
		Header(fmt.Sprintf("工单 ticket.%s 已关闭", t.No)).
		Divider().
		KMarkdownSection(text)

	content, err := kook.SingleCard(card)
	if err != nil {
		return fmt.Sprintf("工单 %s 已关闭", t.No)
	}
	return content
}

// helpCard 生成帮助卡片。
func (b *Bot) helpCard() string {
	text := strings.Join([]string{
		"`/ticket` 在当前频道新建一张工单按钮卡片（同一频道可多张）",
		"`/tkcm 工单编号 备注` 为已关闭的工单添加备注",
		"`/aar @角色` 把角色设为当前面板的管理员角色；加 `-g` 设为全局管理员角色",
		"`/login`（私聊）获取 WebUI 一次性登录码",
		"`/bind`（私聊）获取账号绑定码，用于把 KOOK 身份绑定到 WebUI 账号",
		"```\nID 获取方式：KOOK 设置 → 高级设置 → 打开开发者模式，然后右键复制对应 ID\n```",
	}, "\n")

	card := kook.NewCard(kook.CardThemeInfo).
		Header("KOOK Ticket 命令面板").
		KMarkdownSection(text)

	content, err := kook.SingleCard(card)
	if err != nil {
		return text
	}
	return content
}

func derefTime(value *time.Time, fallback time.Time) time.Time {
	if value == nil {
		return fallback
	}
	return *value
}
