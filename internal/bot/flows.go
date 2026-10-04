package bot

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"vancekookticket/internal/eventbus"
	"vancekookticket/internal/kook"
	"vancekookticket/internal/store"
	"vancekookticket/internal/ticket"
)

// handleEvent 是网关事件的统一入口。
func (b *Bot) handleEvent(ctx context.Context, event kook.Event) {
	b.markEvent()

	// 单服务器白名单：丢弃来自其它服务器的频道消息事件。
	if event.ChannelType == kook.ChannelTypeGroup && event.Extra.GuildID != "" {
		if cfg, err := b.currentConfig(ctx); err == nil && cfg.GuildID != "" && event.Extra.GuildID != cfg.GuildID {
			b.deps.Logger.Debug("忽略非配置服务器的事件",
				"guild_id", event.Extra.GuildID, "expected", cfg.GuildID)
			return
		}
	}

	switch {
	case event.IsSystem():
		b.handleSystemEvent(ctx, event)
	case event.IsTextMessage():
		b.handleTextMessage(ctx, event)
	case isMediaMessage(event.Type):
		b.archiveMessage(ctx, event)
	default:
		// 其它事件类型（成员变动、频道变动等）当前不处理。
	}
}

// isMediaMessage 判断是否为需要归档的非文本消息。
func isMediaMessage(eventType int) bool {
	switch eventType {
	case kook.EventTypeImage, kook.EventTypeVideo, kook.EventTypeFile, kook.EventTypeAudio, kook.EventTypeCard:
		return true
	default:
		return false
	}
}

// handleSystemEvent 处理系统事件：按钮点击、表情回应。
func (b *Bot) handleSystemEvent(ctx context.Context, event kook.Event) {
	switch event.Extra.Type {
	case kook.SystemEventButtonClick:
		b.handleButtonClick(ctx, event)
	case kook.SystemEventAddedReaction:
		b.handleReaction(ctx, event)
	default:
		b.deps.Logger.Debug("忽略系统事件", "type", event.Extra.Type)
	}
}

// handleButtonClick 分发卡片按钮点击。
//
// 安全要点：按钮 value 由客户端回传，一律视为不可信输入——
// 只从中取出“动作类型 + 工单编号”，并且必须通过 HMAC 签名校验；
// 频道、用户等关键信息全部取自服务端字段与数据库记录。
func (b *Bot) handleButtonClick(ctx context.Context, event kook.Event) {
	body := event.Extra.Body
	channelID := firstNonEmpty(body.TargetID, event.TargetID)
	userID := firstNonEmpty(body.UserID, event.AuthorID)
	if channelID == "" || userID == "" {
		b.deps.Logger.Warn("按钮事件缺少频道或用户信息", "value_len", len(body.Value))
		return
	}

	value, err := b.decodeButton(body.Value, channelID)
	if err != nil {
		b.deps.Logger.Warn("拒绝可疑的按钮回调", "channel_id", channelID, "user_id", userID, "err", err)
		return
	}

	switch value.Action {
	case actionOpen:
		b.openTicket(ctx, channelID, userID, body.UserInfo, value.PanelID)
	case actionClose:
		b.closeTicket(ctx, channelID, userID, value.TicketNo)
	case actionLock:
		b.lockTicket(ctx, channelID, userID, value.TicketNo)
	case actionReopen:
		b.reopenTicket(ctx, channelID, userID, value.TicketNo)
	default:
		b.deps.Logger.Warn("未知按钮动作", "action", value.Action)
	}
}

// ---------------------------------------------------------------------------
// 开单
// ---------------------------------------------------------------------------

// maxChannelNameLength 是频道名中昵称部分的长度上限，避免超长频道名。
const maxChannelNameLength = 20

// grantTarget 描述一次频道权限下发。
//
// kind 只用于日志/提示文案（例如“全局管理员角色”），subjectType 是 KOOK 需要的
// 主体类型（role_id / user_id）。
type grantTarget struct {
	subjectType string
	value       string
	kind        string
}

// openTicket 执行开单流程。
//
// 步骤：
//  1. 校验按钮来自已启用的面板；一人同时只能有一个未关闭工单
//  2. 分配工单编号并落库（pending）
//  3. 在配置的隐藏分组下创建工单频道，并把工单置为进行中
//  4. 发含「关闭 / 锁定」按钮的卡片，同时并发下发频道权限（全局管理员角色、
//     面板管理员角色、开单人）与面板开单提示
//  5. 回到按钮所在频道发「仅开单人可见」的完成提示
//
// 第 4 步之所以并发：这些动作互不依赖，串行执行会让用户白等好几个往返；
// 对平台的调用频率交给限流器（按桶额度）控制，而不是用固定 sleep 拖延。
//
// 这里刻意不做「私信可用性探测」：KOOK 没有探测接口，只能真发一条私信，
// 用户会看到它一闪而过（删除失败时还会长期留在私信里）。
// 私信送达问题改到关闭环节兜底，见 platform.NotifyClosed。
//
// panelID 来自按钮签名（旧卡片为 0），用于在同一频道的多张面板卡片中定位实际点击的那一张。
func (b *Bot) openTicket(ctx context.Context, panelChannelID, userID string, userInfo kook.User, panelID uint) {
	started := time.Now()

	// 同一个用户同时只能有一个未关闭工单：按用户加锁，避免连点重复建频道；
	// 不同用户的开单流程互不阻塞（旧实现用全局锁，第二个用户要等第一个人跑完）。
	unlock := b.openLocks.Lock("open:" + userID)
	defer unlock()

	client, cfg, err := b.ready()
	if err != nil {
		b.deps.Logger.Warn("开单失败：机器人未连接", "err", err)
		return
	}

	panel, err := b.panelForOpen(panelChannelID, panelID)
	if err != nil || !panel.Enabled {
		b.sendEphemeral(ctx, panelChannelID, userID, "该频道没有可用的工单面板")
		return
	}
	if cfg.CategoryID == "" {
		b.sendEphemeral(ctx, panelChannelID, userID, "机器人尚未配置工单分组，请联系管理员")
		b.notifyDebug(ctx, "开单失败：未配置工单分组（category_id）")
		return
	}

	// 一人一单
	if existing, err := b.deps.Store.Tickets.ActiveByUser(userID); err == nil && existing != nil {
		b.sendEphemeral(ctx, panelChannelID, userID, fmt.Sprintf(
			"你已有一个未关闭的工单：%s\n请在已有工单频道中继续沟通",
			kook.MentionChannel(existing.ChannelID),
		))
		return
	}

	name := userInfo.DisplayName()
	if name == "" {
		name = userID
	}
	name = truncateRunes(name, maxChannelNameLength)

	pending, err := b.deps.Tickets.CreatePending(ctx, userID, userInfo.FullName(), panelChannelID, &panel.ID)
	if err != nil {
		b.deps.Logger.Error("分配工单编号失败", "user_id", userID, "err", err)
		b.sendEphemeral(ctx, panelChannelID, userID, "创建工单失败，请稍后重试")
		b.notifyDebug(ctx, "分配工单编号失败："+err.Error())
		return
	}

	channel, err := client.ChannelCreate(ctx, cfg.GuildID, cfg.CategoryID, fmt.Sprintf("%s | %s", pending.No, name))
	if err != nil {
		b.deps.Logger.Error("创建工单频道失败", "ticket_no", pending.No, "err", err)
		if discardErr := b.deps.Tickets.DiscardPending(ctx, pending.No); discardErr != nil {
			b.deps.Logger.Error("回收工单编号失败", "ticket_no", pending.No, "err", discardErr)
		}
		b.sendEphemeral(ctx, panelChannelID, userID, "创建工单频道失败，请联系管理员查看机器人日志")
		b.notifyDebug(ctx, fmt.Sprintf("创建工单频道失败：%s（请确认机器人拥有「管理频道」权限，且分组 ID 正确）", err.Error()))
		return
	}
	channelElapsed := time.Since(started)

	// 频道建好即把工单置为进行中：后面的权限下发与消息发送都不影响“工单已可用”，
	// 用户的等待时间因此只取决于建频道 + 发卡片 + 发提示这几步。
	activated, err := b.deps.Tickets.Activate(ctx, pending.No, channel.ID)
	if err != nil {
		b.deps.Logger.Error("激活工单失败", "ticket_no", pending.No, "err", err)
		b.notifyDebug(ctx, "激活工单失败："+err.Error())
		return
	}

	// 权限下发目标：全局管理员角色 + 面板角色 + 开单人本身。
	adminRoles, err := b.deps.Store.Roles.ListAdmin()
	if err != nil {
		b.deps.Logger.Error("读取全局管理员角色失败", "err", err)
	}
	grants := make([]grantTarget, 0, len(adminRoles)+len(panel.Roles)+1)
	roleIDs := make([]string, 0, len(adminRoles)+len(panel.Roles))
	for _, role := range adminRoles {
		grants = append(grants, grantTarget{"role_id", role.RoleID, "全局管理员角色"})
		roleIDs = append(roleIDs, role.RoleID)
	}
	for _, role := range panel.Roles {
		grants = append(grants, grantTarget{"role_id", role.RoleID, "面板角色"})
		roleIDs = append(roleIDs, role.RoleID)
	}
	grants = append(grants, grantTarget{"user_id", userID, "开单人"})

	closeValue := b.encodeButton(actionClose, pending.No, channel.ID, 0)
	lockValue := b.encodeButton(actionLock, pending.No, channel.ID, 0)
	card := b.ticketCard(activated, roleIDs, closeValue, lockValue)

	// 卡片先发：频道里出现工单卡片是“开单成功”最直观的信号。
	if _, err := client.SendChannelMessage(ctx, channel.ID, kook.MsgTypeCard, card, kook.MessageOptions{}); err != nil {
		b.deps.Logger.Warn("发送工单卡片失败", "ticket_no", activated.No, "err", err)
	}
	cardElapsed := time.Since(started)

	// 权限下发与面板开单提示并发：互不依赖，没必要串行等待。
	// 并发度由限流器按平台的桶额度控制，不再用固定 sleep 硬拖延。
	var wg sync.WaitGroup
	for _, grant := range grants {
		wg.Add(1)
		go func(grant grantTarget) {
			defer wg.Done()
			if err := b.grantChannelAccess(ctx, channel.ID, grant.subjectType, grant.value); err != nil {
				b.deps.Logger.Warn("下发频道权限失败",
					"ticket_no", activated.No, "kind", grant.kind, "type", grant.subjectType,
					"value", grant.value, "err", err)
				if grant.subjectType == "user_id" {
					b.notifyDebug(ctx, "下发开单人频道权限失败："+err.Error())
				}
			}
		}(grant)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		b.sendPanelOpenMessage(ctx, client, activated, panel)
	}()
	wg.Wait()
	grantsElapsed := time.Since(started)

	// 最后回到按钮所在的面板频道，给开单人一条「仅自己可见」的完成提示与频道跳转链接。
	b.notifyTicketCreated(ctx, panelChannelID, userID, activated)

	b.deps.Store.Audit.Write(&store.AuditLog{
		Actor:     userInfo.FullName(),
		ActorType: store.ActorTypeKook,
		Action:    "ticket.open",
		Target:    activated.No,
		Detail:    fmt.Sprintf("创建工单频道 %s", channel.ID),
		CreatedAt: store.Now(),
	})
	b.deps.Logger.Info("工单已创建",
		"ticket_no", activated.No, "channel_id", channel.ID, "user", userInfo.FullName(),
		"roles", len(grants), "elapsed", time.Since(started).Round(time.Millisecond).String(),
		"channel_ms", channelElapsed.Milliseconds(), "card_ms", cardElapsed.Milliseconds(),
		"grants_ms", grantsElapsed.Milliseconds(), "notice_ms", (time.Since(started) - grantsElapsed).Milliseconds())
}

// panelForOpen 定位开单按钮所属的面板。
//
// panelID 非 0 时按主键查询，并校验其确实属于按钮所在频道；
// 否则（升级前的旧卡片）回退到频道内第一条面板。
func (b *Bot) panelForOpen(channelID string, panelID uint) (*store.Panel, error) {
	if panelID == 0 {
		return b.deps.Store.Panels.ByChannel(channelID)
	}
	panel, err := b.deps.Store.Panels.ByID(panelID)
	if err != nil {
		return nil, err
	}
	if panel.ChannelID != channelID {
		return nil, fmt.Errorf("面板 %d 不属于频道 %s", panelID, channelID)
	}
	return panel, nil
}

// sendPanelOpenMessage 在工单频道内发送面板自定义的开单提示（独立 KMarkdown 文本消息）。
//
// 内容为空时不发送。只有 KOOK 侧发送成功才写入时间线，
// 避免出现“WebUI 有记录但频道内看不到消息”的不一致。
func (b *Bot) sendPanelOpenMessage(ctx context.Context, client *kook.Client, t *store.Ticket, panel *store.Panel) {
	if strings.TrimSpace(panel.OpenMessage) == "" {
		return
	}
	content := b.panelOpenMessage(panel.OpenMessage, t)
	if _, err := client.SendChannelMessage(ctx, t.ChannelID, kook.MsgTypeKMarkdown, content, kook.MessageOptions{}); err != nil {
		b.deps.Logger.Warn("发送面板开单提示失败", "ticket_no", t.No, "panel_id", panel.ID, "err", err)
		return
	}
	if _, err := b.deps.Tickets.AddBotMessage(ctx, t.No, t.ChannelID, content); err != nil {
		b.deps.Logger.Warn("写入开单提示时间线失败", "ticket_no", t.No, "err", err)
	}
}

// grantChannelAccess 为频道内的角色/用户下发“可看可发”权限。
//
// KOOK 需要先 create 出权限覆写记录，再 update 权限位，两步必须按顺序；
// 但不同主体之间可以并发，具体并发度由限流器按平台回报的桶额度决定
// （旧实现在每个主体之间硬 sleep 120ms，纯粹是白等）。
func (b *Bot) grantChannelAccess(ctx context.Context, channelID, subjectType, value string) error {
	client, _, err := b.ready()
	if err != nil {
		return err
	}
	if err := client.ChannelRoleCreate(ctx, channelID, subjectType, value); err != nil {
		b.deps.Logger.Debug("创建频道权限覆写失败（可能已存在）",
			"channel_id", channelID, "type", subjectType, "err", err)
	}
	return client.ChannelRoleUpdate(ctx, channelID, subjectType, value, kook.PermissionAllText, 0)
}

// ---------------------------------------------------------------------------
// 关闭 / 锁定 / 重新激活
// ---------------------------------------------------------------------------

// closeTicket 处理关闭按钮。
//
// 同一张工单的并发操作由 ticket.Service 内部的按工单锁拦下，这里不再用全局锁：
// 两位管理员关不同工单时不应该彼此等待。
func (b *Bot) closeTicket(ctx context.Context, channelID, userID, ticketNo string) {
	started := time.Now()
	t, ok := b.ticketForButton(ctx, channelID, userID, ticketNo)
	if !ok {
		return
	}
	if !b.isAdmin(ctx, userID, t.SourceChannelID) {
		b.sendEphemeral(ctx, channelID, userID, "只有管理员可以关闭工单")
		return
	}

	actor := b.ticketActor(ctx, userID, channelID)
	if _, err := b.deps.Tickets.Close(ctx, t.No, actor, ""); err != nil {
		b.deps.Logger.Error("关闭工单失败", "ticket_no", t.No, "err", err)
		b.sendEphemeral(ctx, channelID, userID, "关闭工单失败："+friendlyError(err))
		return
	}
	b.deps.Logger.Info("工单已关闭", "ticket_no", t.No, "by", actor.Name,
		"elapsed", time.Since(started).Round(time.Millisecond).String())
}

// lockTicket 处理锁定按钮。
func (b *Bot) lockTicket(ctx context.Context, channelID, userID, ticketNo string) {
	t, ok := b.ticketForButton(ctx, channelID, userID, ticketNo)
	if !ok {
		return
	}
	if !b.isAdmin(ctx, userID, t.SourceChannelID) {
		b.sendEphemeral(ctx, channelID, userID, "只有管理员可以锁定工单")
		return
	}
	actor := b.ticketActor(ctx, userID, channelID)
	if _, err := b.deps.Tickets.Lock(ctx, t.No, actor, store.LockReasonManual); err != nil {
		b.sendEphemeral(ctx, channelID, userID, "锁定工单失败："+friendlyError(err))
		return
	}
	b.deps.Logger.Info("工单已锁定", "ticket_no", t.No, "by", actor.Name)
}

// reopenTicket 处理重新激活按钮。
func (b *Bot) reopenTicket(ctx context.Context, channelID, userID, ticketNo string) {
	t, ok := b.ticketForButton(ctx, channelID, userID, ticketNo)
	if !ok {
		return
	}
	if !b.isAdmin(ctx, userID, t.SourceChannelID) {
		b.sendEphemeral(ctx, channelID, userID, "只有管理员可以重新激活工单")
		return
	}
	actor := b.ticketActor(ctx, userID, channelID)
	if _, err := b.deps.Tickets.Reopen(ctx, t.No, actor); err != nil {
		b.sendEphemeral(ctx, channelID, userID, "重新激活失败："+friendlyError(err))
		return
	}
	b.deps.Logger.Info("工单已重新激活", "ticket_no", t.No, "by", actor.Name)
}

// ticketForButton 依据按钮所在的频道与编号取出工单，并校验二者匹配。
//
// 这一步是按钮安全模型的关键：即使攻击者伪造了通过签名的编号，
// 也必须与频道记录一致才会被受理。
func (b *Bot) ticketForButton(ctx context.Context, channelID, userID, ticketNo string) (*store.Ticket, bool) {
	if !strings.HasPrefix(ticketNo, "TK-") {
		b.deps.Logger.Warn("按钮携带的工单编号非法", "channel_id", channelID)
		return nil, false
	}
	t, err := b.deps.Store.Tickets.ByNo(ticketNo)
	if err != nil {
		b.sendEphemeral(ctx, channelID, userID, "找不到该工单记录")
		return nil, false
	}
	if t.ChannelID != channelID {
		b.deps.Logger.Warn("按钮与工单频道不匹配，已拒绝",
			"ticket_no", t.No, "button_channel", channelID, "ticket_channel", t.ChannelID)
		return nil, false
	}
	return t, true
}

// ticketActor 构造业务操作者。
//
// 昵称优先从带缓存的用户信息里取（权限判定刚刚查过，正常情况不会额外发请求），
// 拿不到时退化为直接调用 user/view，最后退化为用 ID 展示。
func (b *Bot) ticketActor(ctx context.Context, userID, channelID string) ticket.Actor {
	name := ""
	if user, err := b.userInfo(ctx, userID); err == nil {
		name = user.FullName()
	}
	if name == "" {
		if client, _, err := b.ready(); err == nil {
			if user, err := client.UserView(ctx, userID, ""); err == nil {
				name = user.FullName()
			}
		}
	}
	if name == "" {
		name = userID
	}
	return ticket.Actor{ID: userID, Name: name, Source: "kook"}
}

// ---------------------------------------------------------------------------
// 消息归档
// ---------------------------------------------------------------------------

// handleTextMessage 处理文本消息：命令优先，其次归档。
func (b *Bot) handleTextMessage(ctx context.Context, event kook.Event) {
	content := strings.TrimSpace(event.Content)
	if strings.HasPrefix(content, "/") {
		b.handleCommand(ctx, event)
		return
	}
	b.archiveMessage(ctx, event)
}

// archiveMessage 把工单频道内的消息写入数据库。
func (b *Bot) archiveMessage(ctx context.Context, event kook.Event) {
	channelID := event.TargetID
	if channelID == "" {
		return
	}
	t, err := b.deps.Store.Tickets.ByChannel(channelID)
	if err != nil {
		// 不是工单频道（或工单已关闭），忽略。
		return
	}
	if event.Author.Bot || event.AuthorID == "" {
		return
	}
	if t.Status == store.TicketClosed {
		return
	}

	message := archivedMessage(event, t.No)
	if err := b.deps.Store.Tickets.AddMessage(message); err != nil {
		b.deps.Logger.Warn("归档工单消息失败", "ticket_no", t.No, "err", err)
		return
	}
	b.publishMessage(t.No, message)

	// 卡片消息的事件推送不带内容（用户上传的文件也被平台转成卡片消息下发），
	// 需要异步调用 message/view 补全卡片 JSON 与媒体地址。
	if event.Type == kook.EventTypeCard && event.MsgID != "" && strings.TrimSpace(event.Content) == "" {
		go b.enrichCardMessage(t.No, message.ID, event.MsgID)
	}
}

// publishMessage 把消息推送给 WebUI（列表与详情自动刷新）。
func (b *Bot) publishMessage(ticketNo string, message *store.TicketMessage) {
	if b.deps.Bus == nil || message == nil {
		return
	}
	b.deps.Bus.Publish(eventbus.Event{
		Type:     eventbus.EventTicketMessage,
		TicketNo: ticketNo,
		Data:     message,
		At:       store.Now(),
	})
}

// archivedMessage 把平台事件转换成入库记录。
//
// 媒体消息的 content 是资源地址，附件（extra.attachments）作为兜底；
// 归档时同时写入 media_* 字段，WebUI 可以直接渲染图片 / 播放器 / 下载链接。
func archivedMessage(event kook.Event, ticketNo string) *store.TicketMessage {
	message := &store.TicketMessage{
		TicketNo:  ticketNo,
		MsgID:     event.MsgID,
		ChannelID: event.TargetID,
		UserID:    event.AuthorID,
		UserName:  event.Author.FullName(),
		Content:   archivedContent(event),
		MsgType:   archivedType(event.Type),
		IsBot:     false,
		CreatedAt: messageTime(event),
	}

	switch message.MsgType {
	case store.MsgTypeImage, store.MsgTypeVideo, store.MsgTypeFile, store.MsgTypeAudio:
		if attachment, ok := eventAttachment(event); ok {
			message.MediaURL = attachment.URL
			message.MediaName = attachment.Name
			message.MediaType = mediaTypeName(attachment)
		}
	case store.MsgTypeCard:
		// 事件偶尔会带上卡片 JSON（大多数情况下为空），能解析就直接落库。
		if content := strings.TrimSpace(event.Content); content != "" {
			if _, err := kook.ParseCards(content); err == nil {
				message.CardJSON = content
				message.Content = cardContent(content)
				if attachment, ok := cardPrimaryAttachment(content); ok {
					message.MediaURL = attachment.URL
					message.MediaName = attachment.Name
					message.MediaType = mediaTypeName(attachment)
				}
			}
		}
	}
	return message
}

// enrichCardMessage 异步补全卡片消息内容。
//
// 平台下发的卡片消息事件 content 为空，需要再调 message/view 取卡片 JSON。
// 事件到达时平台侧可能尚未落库，因此做几次短重试；失败只记录日志，
// 记录里至少保留「[卡片消息]」兜底文案。
func (b *Bot) enrichCardMessage(ticketNo string, messageID uint, msgID string) {
	client := b.Client()
	if client == nil {
		return
	}

	backoffs := []time.Duration{0, 800 * time.Millisecond, 2 * time.Second}
	for attempt, wait := range backoffs {
		if wait > 0 {
			time.Sleep(wait)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		detail, err := client.MessageView(ctx, msgID)
		cancel()
		if err != nil {
			b.deps.Logger.Debug("拉取消息详情失败", "msg_id", msgID, "attempt", attempt+1, "err", err)
			continue
		}

		cardJSON := strings.TrimSpace(detail.Content)
		if cardJSON == "" {
			// 详情里也没有内容：说明卡片确实无文本（或已被删除），不再重试。
			return
		}

		patch := store.MessagePatch{CardJSON: cardJSON, Content: cardContent(cardJSON)}
		if attachment, ok := cardPrimaryAttachment(cardJSON); ok {
			patch.MediaURL = attachment.URL
			patch.MediaName = attachment.Name
			patch.MediaType = mediaTypeName(attachment)
		}
		changed, err := b.deps.Store.Tickets.UpdateMessageRich(messageID, patch)
		if err != nil {
			b.deps.Logger.Warn("补全卡片消息失败", "msg_id", msgID, "err", err)
			return
		}
		if !changed {
			return
		}
		if message, err := b.deps.Store.Tickets.MessageByID(messageID); err == nil {
			b.publishMessage(ticketNo, message)
		} else {
			b.deps.Logger.Debug("读取补全后的消息失败", "msg_id", msgID, "err", err)
		}
		return
	}
}

// cardContent 生成卡片消息的入库文本：保留「[卡片消息]」前缀便于检索，并附上摘要。
func cardContent(cardJSON string) string {
	summary := kook.EscapeMentionText(kook.CardSummary(cardJSON))
	if strings.TrimSpace(summary) == "" {
		return "[卡片消息]"
	}
	return "[卡片消息] " + summary
}

// eventAttachment 提取媒体事件的附件信息。
//
// 优先使用事件顶层 content（它本身就是资源地址），为空时回退到
// extra.attachments.url；名称与 MIME 类型同理。
func eventAttachment(event kook.Event) (kook.Attachment, bool) {
	attachment := event.Extra.Attachments.Primary()
	url := strings.TrimSpace(event.Content)
	if url == "" {
		url = strings.TrimSpace(attachment.URL)
	}
	if url == "" {
		return kook.Attachment{}, false
	}
	attachment.URL = url
	if strings.TrimSpace(attachment.Name) == "" {
		attachment.Name = fileNameFromURL(url)
	}
	return attachment, true
}

// cardPrimaryAttachment 返回卡片中最值得展示的媒体（优先文件/音视频，其次图片）。
func cardPrimaryAttachment(cardJSON string) (kook.Attachment, bool) {
	attachments := kook.CardAttachments(cardJSON)
	if len(attachments) == 0 {
		return kook.Attachment{}, false
	}
	for _, attachment := range attachments {
		if attachment.Type != "image" {
			return attachment, true
		}
	}
	return attachments[0], true
}

// mediaTypeName 归一化附件的类型描述（MIME 优先，其次平台类别）。
func mediaTypeName(attachment kook.Attachment) string {
	if value := strings.TrimSpace(attachment.FileType); value != "" {
		return value
	}
	return strings.TrimSpace(attachment.Type)
}

// fileNameFromURL 从资源地址中推断文件名（无文件名时的兜底展示）。
func fileNameFromURL(rawURL string) string {
	if parsed, err := url.Parse(rawURL); err == nil && parsed.Path != "" {
		if name := path.Base(parsed.Path); name != "." && name != "/" {
			return name
		}
	}
	return ""
}

// archivedType 把 KOOK 消息类型映射到内部类型。
func archivedType(eventType int) string {
	switch eventType {
	case kook.EventTypeText, kook.EventTypeKMarkdown:
		return store.MsgTypeText
	case kook.EventTypeImage:
		return store.MsgTypeImage
	case kook.EventTypeVideo:
		return store.MsgTypeVideo
	case kook.EventTypeFile:
		return store.MsgTypeFile
	case kook.EventTypeAudio:
		return store.MsgTypeAudio
	case kook.EventTypeCard:
		return store.MsgTypeCard
	default:
		return store.MsgTypeUnknown
	}
}

// archivedContent 生成入库内容：媒体消息记录为「[类型] 地址」。
func archivedContent(event kook.Event) string {
	label := map[int]string{
		kook.EventTypeImage: "[图片] ",
		kook.EventTypeVideo: "[视频] ",
		kook.EventTypeFile:  "[文件] ",
		kook.EventTypeAudio: "[语音] ",
	}
	if prefix, ok := label[event.Type]; ok {
		attachment, hasAttachment := eventAttachment(event)
		if hasAttachment {
			return prefix + attachment.URL
		}
		return prefix + strings.TrimSpace(event.Content)
	}
	if event.Type == kook.EventTypeCard {
		return "[卡片消息]"
	}
	// 文本内容同样转义提及语法，避免记录页被用于伪造 @全体成员。
	return kook.EscapeMentionText(event.Content)
}

// messageTime 依据事件时间戳生成 UTC 时间。
func messageTime(event kook.Event) time.Time {
	if event.MsgTimestamp > 0 {
		return time.UnixMilli(event.MsgTimestamp).UTC()
	}
	return store.Now()
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// notifyTicketCreated 在面板频道发送「仅开单人可见」的开单完成提示。
//
// KOOK 的临时消息（temp_target_id）只能在消息所在频道内对指定用户可见，
// 因此提示必须发回按钮所在的面板频道；卡片内的 (chn) 频道提及即跳转链接。
// 提示失败不影响工单本身（工单频道卡片已经发出），只记录警告。
func (b *Bot) notifyTicketCreated(ctx context.Context, panelChannelID, userID string, t *store.Ticket) {
	client, _, err := b.ready()
	if err != nil {
		return
	}
	if _, err := client.SendChannelMessage(ctx, panelChannelID, kook.MsgTypeCard, b.ticketCreatedCard(t), kook.MessageOptions{
		TempTargetID: userID,
	}); err != nil {
		b.deps.Logger.Warn("发送开单完成提示失败",
			"ticket_no", t.No, "channel_id", panelChannelID, "user_id", userID, "err", err)
	}
}

// sendEphemeral 在频道内发送仅指定用户可见的提示。
func (b *Bot) sendEphemeral(ctx context.Context, channelID, userID, markdown string) {
	client, _, err := b.ready()
	if err != nil {
		return
	}
	content := kook.NoticeCard(kook.CardThemeInfo, "", markdown)
	if _, err := client.SendChannelMessage(ctx, channelID, kook.MsgTypeCard, content, kook.MessageOptions{
		TempTargetID: userID,
	}); err != nil {
		b.deps.Logger.Debug("发送临时提示失败", "channel_id", channelID, "err", err)
	}
}

// notifyDebug 向调试频道发送错误提示（未配置时只写日志）。
func (b *Bot) notifyDebug(ctx context.Context, message string) {
	client, cfg, err := b.ready()
	if err != nil {
		return
	}
	if cfg.DebugChannelID == "" {
		b.deps.Logger.Info("未配置调试频道，错误仅记录日志", "message", message)
		return
	}
	content := kook.NoticeCard(kook.CardThemeDanger, "机器人错误提示", "```\n"+truncateRunes(message, 900)+"\n```")
	if _, err := client.SendChannelMessage(ctx, cfg.DebugChannelID, kook.MsgTypeCard, content, kook.MessageOptions{}); err != nil {
		b.deps.Logger.Warn("发送调试信息失败", "channel_id", cfg.DebugChannelID, "err", err)
	}
}

// friendlyError 把业务错误转成用户可读的短句。
func friendlyError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ticket.ErrInvalidState):
		return strings.TrimPrefix(err.Error(), ticket.ErrInvalidState.Error()+"：")
	case errors.Is(err, store.ErrNotFound):
		return "工单不存在"
	default:
		return "请稍后重试，管理员可在日志中查看详情"
	}
}

// truncateRunes 按字符截断字符串。
func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
