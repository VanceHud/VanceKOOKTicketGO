package bot

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/secure"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/ticket"
)

// roleMentionPattern 匹配 KMarkdown 中的角色提及：(rol)12345(rol)。
var roleMentionPattern = regexp.MustCompile(`\(rol\)\s*(\d+)\s*\(rol\)`)

// codeTTL 是一次性码的有效期（与 WebUI 文案保持一致）。
const codeTTL = 5 * time.Minute

// codeCooldown 是同一用户重复申请一次性码的最小间隔。
const codeCooldown = 30 * time.Second

// handleCommand 解析并执行命令。
func (b *Bot) handleCommand(ctx context.Context, event kook.Event) {
	fields := strings.Fields(strings.TrimSpace(event.Content))
	if len(fields) == 0 {
		return
	}
	command := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	args := fields[1:]

	// 私聊专属命令
	if event.IsDirect() {
		switch command {
		case "login":
			b.cmdLogin(ctx, event)
		case "bind":
			b.cmdBind(ctx, event)
		case "tkhelp", "help":
			b.replyDirectCard(ctx, event.AuthorID, b.helpCard())
		default:
			b.replyDirect(ctx, event.AuthorID, "私聊仅支持 `/login`、`/bind` 与 `/tkhelp`")
		}
		return
	}

	switch command {
	case "hello":
		b.replyEphemeral(ctx, event, "world!")
	case "tkhelp", "help":
		b.replyEphemeralCard(ctx, event, b.helpCard())
	case "ticket":
		b.cmdTicketPanel(ctx, event, args)
	case "tkcm":
		b.cmdTicketComment(ctx, event, args)
	case "tkclose":
		b.cmdTicketClose(ctx, event, args)
	case "aar", "add_admin_role":
		b.cmdAddAdminRole(ctx, event, args)
	case "kill":
		b.cmdKill(ctx, event)
	default:
		// 未识别的命令静默忽略，避免刷屏。
	}
}

// ---------------------------------------------------------------------------
// 面板与备注
// ---------------------------------------------------------------------------

// cmdTicketPanel 在当前频道新建一张工单面板卡片。
//
// 用法：/ticket [类型名]
//   - 指定类型名：面板挂在同名工单类型下，类型不存在时自动创建；
//   - 省略类型名：优先复用当前频道已有面板的类型，否则使用「默认工单类型」。
//
// 同一频道允许存在多张卡片：每次执行都会新建一条面板记录并发送一张新卡片，
// 面板类型可在 WebUI 的「工单类型」页里随时调整。
func (b *Bot) cmdTicketPanel(ctx context.Context, event kook.Event, args []string) {
	channelID := event.TargetID
	if channelID == "" {
		return
	}
	if !b.isAdmin(ctx, event.AuthorID, channelID) {
		b.replyEphemeral(ctx, event, "你没有权限执行该命令")
		return
	}
	if _, _, err := b.ready(); err != nil {
		b.replyEphemeral(ctx, event, "机器人尚未连接 KOOK")
		return
	}

	ticketType, err := b.ticketTypeForPanel(ctx, channelID, strings.Join(args, " "))
	if err != nil {
		b.replyEphemeral(ctx, event, err.Error())
		return
	}
	if !ticketType.Enabled {
		b.replyEphemeral(ctx, event, fmt.Sprintf("工单类型「%s」已停用，请先启用或换一个类型", ticketType.Name))
		return
	}

	panel := &store.Panel{
		TypeID:      ticketType.ID,
		ChannelID:   channelID,
		ChannelName: event.Extra.ChannelName,
		Title:       DefaultPanelTitle,
		ButtonText:  DefaultPanelButton,
		Enabled:     true,
	}
	if err := b.deps.Store.Panels.Create(panel); err != nil {
		b.deps.Logger.Error("保存面板配置失败", "channel_id", channelID, "err", err)
		b.replyEphemeral(ctx, event, "创建工单面板失败，请稍后重试")
		return
	}

	msgID, err := b.SendPanelCard(ctx, panel, DefaultPanelButton)
	if err != nil {
		b.deps.Logger.Error("发送工单面板失败", "channel_id", channelID, "err", err)
		// 卡片未能发出时回滚记录，避免留下无法点击的空面板。
		if delErr := b.deps.Store.Panels.Delete(panel.ID); delErr != nil {
			b.deps.Logger.Error("回滚面板记录失败", "panel_id", panel.ID, "err", delErr)
		}
		b.replyEphemeral(ctx, event, "发送工单面板失败，请确认机器人拥有发送消息权限")
		return
	}
	if err := b.deps.Store.Panels.UpdateFields(panel.ID, map[string]any{"msg_id": msgID}); err != nil {
		b.deps.Logger.Error("回写面板消息 ID 失败", "panel_id", panel.ID, "err", err)
	}

	b.deps.Store.Audit.Write(&store.AuditLog{
		Actor:     event.Author.FullName(),
		ActorType: store.ActorTypeKook,
		Action:    "panel.create",
		Target:    channelID,
		Detail:    fmt.Sprintf("创建面板（工单类型：%s），消息 %s", ticketType.Name, msgID),
		CreatedAt: store.Now(),
	})
	b.replyEphemeral(ctx, event, fmt.Sprintf("已为工单类型「%s」创建面板，成员点击按钮即可开单", ticketType.Name))
}

// maxTicketTypeNameLength 是工单类型名称的长度上限（与数据库列宽一致）。
const maxTicketTypeNameLength = 64

// ticketTypeForPanel 解析 /ticket 命令要使用的工单类型。
//
// 显式给出名称时按名称复用、不存在则创建；省略时优先复用当前频道已有面板的类型，
// 否则退化为「默认工单类型」，保证旧习惯的 `/ticket` 仍然可用。
func (b *Bot) ticketTypeForPanel(ctx context.Context, channelID, rawName string) (*store.TicketType, error) {
	name := strings.Join(strings.Fields(rawName), " ")
	if name != "" {
		if len([]rune(name)) > maxTicketTypeNameLength {
			return nil, fmt.Errorf("类型名称不能超过 %d 个字符", maxTicketTypeNameLength)
		}
		return b.ensureTicketType(name)
	}
	if types, err := b.deps.Store.Types.ListByChannel(channelID); err == nil && len(types) > 0 {
		return &types[0], nil
	}
	return b.ensureTicketType(store.DefaultTypeName)
}

// ensureTicketType 按名称取工单类型，不存在则创建（并发时依赖名称唯一索引兜底）。
func (b *Bot) ensureTicketType(name string) (*store.TicketType, error) {
	item, err := b.deps.Store.Types.ByName(name)
	if err == nil {
		return item, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	created := &store.TicketType{Name: name, Enabled: true}
	if err := b.deps.Store.Types.Create(created); err != nil {
		// 并发创建同名类型：以库里的记录为准。
		if again, lookupErr := b.deps.Store.Types.ByName(name); lookupErr == nil {
			return again, nil
		}
		return nil, err
	}
	return created, nil
}

// cmdTicketComment 为已关闭的工单添加备注（对应 /tkcm）。
func (b *Bot) cmdTicketComment(ctx context.Context, event kook.Event, args []string) {
	if !b.isAdmin(ctx, event.AuthorID, "") {
		b.replyEphemeral(ctx, event, "你没有权限执行该命令")
		return
	}
	if len(args) < 2 {
		b.replyEphemeral(ctx, event, "用法：`/tkcm 工单编号 备注内容`")
		return
	}
	ticketNo := strings.TrimSpace(args[0])
	content := strings.TrimSpace(strings.Join(args[1:], " "))

	t, err := b.deps.Store.Tickets.ByNo(ticketNo)
	if err != nil {
		b.replyEphemeral(ctx, event, "找不到该工单编号")
		return
	}
	if t.LogChannelMsgID == "" {
		b.replyEphemeral(ctx, event, "工单尚未结束，暂时无法添加备注")
		return
	}

	actor := b.ticketActor(ctx, event.AuthorID, event.TargetID)
	if _, err := b.deps.Tickets.AddNote(t.No, actor, truncateRunes(content, 2000)); err != nil {
		b.replyEphemeral(ctx, event, "添加备注失败："+friendlyError(err))
		return
	}

	// 刷新日志卡片，让备注同步显示在日志频道。
	notes, err := b.deps.Store.Tickets.Notes(t.No)
	if err == nil {
		if client, _, err := b.ready(); err == nil {
			card := b.ticketLogCard(t, notes)
			if err := client.UpdateChannelMessage(ctx, t.LogChannelMsgID, card); err != nil {
				b.deps.Logger.Warn("刷新日志卡片失败", "ticket_no", t.No, "err", err)
			}
		}
	}

	b.replyEphemeral(ctx, event, fmt.Sprintf("工单「%s」备注成功", t.No))
}

// cmdTicketClose 在工单频道内关闭工单并可附带关闭说明（对应 /tkclose 命令）。
//
// 与工单卡片上的「关闭」按钮互为补充：按钮一键关闭（不写说明），
// 本命令用于需要留下处理结论的场景。工单编号从当前频道推导，
// 因此命令不会误关其它工单。不带说明时只提示用法，不会直接关闭。
func (b *Bot) cmdTicketClose(ctx context.Context, event kook.Event, args []string) {
	t, err := b.deps.Store.Tickets.ByChannel(event.TargetID)
	if err != nil {
		b.replyEphemeral(ctx, event, "该命令只能在工单频道内使用")
		return
	}
	if !b.isTicketAdmin(ctx, event.AuthorID, t) {
		b.replyEphemeral(ctx, event, "只有管理员可以关闭工单")
		return
	}
	if t.Status == store.TicketClosed {
		b.replyEphemeral(ctx, event, "工单已关闭")
		return
	}

	note := strings.TrimSpace(strings.Join(args, " "))
	if note == "" {
		b.replyEphemeral(ctx, event, "用法：`/tkclose 关闭说明`\n不写说明时，请直接点击工单卡片上的「关闭」按钮。")
		return
	}
	if len([]rune(note)) > ticket.MaxCloseNoteLen {
		b.replyEphemeral(ctx, event, fmt.Sprintf("关闭说明不能超过 %d 个字符", ticket.MaxCloseNoteLen))
		return
	}

	actor := b.ticketActor(ctx, event.AuthorID, event.TargetID)
	if _, err := b.deps.Tickets.Close(ctx, t.No, actor, note); err != nil {
		b.deps.Logger.Error("关闭工单失败", "ticket_no", t.No, "err", err)
		b.replyEphemeral(ctx, event, "关闭工单失败："+friendlyError(err))
		return
	}
	b.deps.Logger.Info("工单已关闭", "ticket_no", t.No, "by", actor.Name, "note_len", len([]rune(note)))
}

// cmdAddAdminRole 把角色加入面板管理员或全局管理员（对应 /aar）。
func (b *Bot) cmdAddAdminRole(ctx context.Context, event kook.Event, args []string) {
	if !b.isAdmin(ctx, event.AuthorID, event.TargetID) {
		b.replyEphemeral(ctx, event, "你没有权限执行该命令")
		return
	}
	matches := roleMentionPattern.FindStringSubmatch(strings.Join(args, " "))
	if len(matches) < 2 {
		b.replyEphemeral(ctx, event, "用法：`/aar @角色`（加 `-g` 设为全局管理员角色）")
		return
	}
	roleID := matches[1]
	isGlobal := false
	for _, arg := range args {
		if arg == "-g" || arg == "-G" {
			isGlobal = true
		}
	}

	roleName := b.lookupRoleName(ctx, roleID)

	if isGlobal {
		if err := b.deps.Store.Roles.AddAdmin(roleID, roleName); err != nil {
			b.replyEphemeral(ctx, event, "添加全局管理员角色失败："+friendlyError(err))
			return
		}
		b.deps.Store.Audit.Write(&store.AuditLog{
			Actor: event.Author.FullName(), ActorType: store.ActorTypeKook,
			Action: "role.admin.add", Target: roleID, Detail: "新增全局管理员角色",
			CreatedAt: store.Now(),
		})
		b.replyEphemeral(ctx, event, fmt.Sprintf("已把「%s」设为全局管理员角色", roleName))
		return
	}

	// 类型级管理员：同一频道可能有多张面板，它们所属的类型全部生效。
	types, err := b.deps.Store.Types.ListByChannel(event.TargetID)
	if err != nil || len(types) == 0 {
		b.replyEphemeral(ctx, event, "当前频道还没有工单面板，无法设置类型管理员；若需全局管理员请在命令末尾加 `-g`")
		return
	}
	for _, item := range types {
		if err := b.deps.Store.Types.AddRole(item.ID, roleID, roleName); err != nil {
			b.replyEphemeral(ctx, event, "添加工单类型管理员角色失败："+friendlyError(err))
			return
		}
	}
	b.deps.Store.Audit.Write(&store.AuditLog{
		Actor: event.Author.FullName(), ActorType: store.ActorTypeKook,
		Action: "role.type.add", Target: roleID, Detail: "新增工单类型管理员角色：" + event.TargetID,
		CreatedAt: store.Now(),
	})
	b.replyEphemeral(ctx, event, fmt.Sprintf("已把「%s」设为本频道 %d 个工单类型的管理员角色", roleName, len(types)))
}

// lookupRoleName 按 ID 查角色名（失败时退化为 ID）。
func (b *Bot) lookupRoleName(ctx context.Context, roleID string) string {
	roles, err := b.GuildRoles(ctx)
	if err != nil {
		return roleID
	}
	for _, role := range roles {
		if strconv.FormatInt(role.RoleID, 10) == roleID {
			return role.Name
		}
	}
	return roleID
}

// ---------------------------------------------------------------------------
// 运维命令
// ---------------------------------------------------------------------------

// cmdKill 优雅退出机器人（需要 @ 机器人，避免误触）。
func (b *Bot) cmdKill(ctx context.Context, event kook.Event) {
	if !b.isAdmin(ctx, event.AuthorID, "") {
		b.replyEphemeral(ctx, event, "你没有权限执行该命令")
		return
	}
	botID := b.botUserID()
	if botID != "" && !strings.Contains(event.Content, botID) {
		b.replyEphemeral(ctx, event, "为保证命令唯一性，执行本命令必须 @ 机器人：`/kill @机器人`")
		return
	}

	b.deps.Store.Audit.Write(&store.AuditLog{
		Actor: event.Author.FullName(), ActorType: store.ActorTypeKook,
		Action: "bot.kill", Target: botID, Detail: "管理员执行 /kill，机器人开始退出",
		CreatedAt: store.Now(),
	})
	b.replyEphemeral(ctx, event, "机器人正在退出，容器会自动重启")
	b.deps.Logger.Warn("收到 /kill 命令，触发优雅退出", "by", event.Author.FullName())

	if b.deps.RequestStop != nil {
		// 给回复消息留出发送时间。
		go func() {
			time.Sleep(500 * time.Millisecond)
			b.deps.RequestStop()
		}()
	}
}

// ---------------------------------------------------------------------------
// 一次性登录码 / 绑定码
// ---------------------------------------------------------------------------

// cmdLogin 为私聊用户签发 WebUI 登录码。
func (b *Bot) cmdLogin(ctx context.Context, event kook.Event) {
	role, ok := b.resolveWebRole(ctx, event.AuthorID)
	if !ok {
		b.replyDirect(ctx, event.AuthorID,
			"你的 KOOK 角色未配置 WebUI 权限，无法登录控制台。请联系管理员在「角色与权限」中配置映射。")
		return
	}
	b.issueCode(ctx, event, store.CodePurposeLogin, role)
}

// cmdBind 为私聊用户签发账号绑定码。
func (b *Bot) cmdBind(ctx context.Context, event kook.Event) {
	// 绑定本身不授予权限，但仍要求命中角色映射，避免把无关账号绑进系统。
	if _, ok := b.resolveWebRole(ctx, event.AuthorID); !ok {
		b.replyDirect(ctx, event.AuthorID, "你的 KOOK 角色未配置 WebUI 权限，无法绑定控制台账号。")
		return
	}
	b.issueCode(ctx, event, store.CodePurposeBind, "")
}

// issueCode 生成一次性码并回复用户。
func (b *Bot) issueCode(ctx context.Context, event kook.Event, purpose, roleHint string) {
	// 频率限制：同一用户 30s 内只能申请一次（基于数据库记录，重启不失效）。
	if latest, err := b.deps.Store.Codes.Latest(event.AuthorID, purpose); err == nil && latest != nil {
		if store.Now().Sub(latest.CreatedAt) < codeCooldown {
			b.replyDirect(ctx, event.AuthorID, "请求过于频繁，请稍后再试")
			return
		}
	}

	code, err := secure.RandomCrockford(6)
	if err != nil {
		b.deps.Logger.Error("生成一次性码失败", "err", err)
		b.replyDirect(ctx, event.AuthorID, "生成验证码失败，请稍后重试")
		return
	}

	// 作废该用户此前未使用的同类码，保证同一时间只有一个有效码。
	if err := b.deps.Store.Codes.InvalidateActive(event.AuthorID, purpose, store.Now()); err != nil {
		b.deps.Logger.Warn("作废旧验证码失败", "err", err)
	}

	record := &store.AuthCode{
		CodeHash:     secure.HashToken(strings.ToUpper(code)),
		Purpose:      purpose,
		KookUserID:   event.AuthorID,
		KookUserName: event.Author.FullName(),
		RoleHint:     roleHint,
		ExpiresAt:    store.Now().Add(codeTTL),
		CreatedAt:    store.Now(),
	}
	if err := b.deps.Store.Codes.Create(record); err != nil {
		b.deps.Logger.Error("保存一次性码失败", "err", err)
		b.replyDirect(ctx, event.AuthorID, "生成验证码失败，请稍后重试")
		return
	}

	b.deps.Store.Audit.Write(&store.AuditLog{
		Actor: event.Author.FullName(), ActorType: store.ActorTypeKook,
		Action: "auth.code.issue", Target: purpose,
		Detail: fmt.Sprintf("%s 码已签发，有效期 %s", purpose, codeTTL), CreatedAt: store.Now(),
	})

	if purpose == store.CodePurposeLogin {
		b.replyDirect(ctx, event.AuthorID, fmt.Sprintf(
			"你的 WebUI 登录码：**%s**\n有效期 %d 分钟，仅可使用一次。\n在控制台登录页选择「登录码」并输入即可。",
			code, int(codeTTL.Minutes()),
		))
		return
	}
	b.replyDirect(ctx, event.AuthorID, fmt.Sprintf(
		"你的账号绑定码：**%s**\n有效期 %d 分钟，仅可使用一次。\n请登录控制台后，在账号页输入该码完成绑定。",
		code, int(codeTTL.Minutes()),
	))
}

// ResolveWebRole 依据 KOOK 角色计算 WebUI 权限（供 WebUI 登录时校验权限）。
func (b *Bot) ResolveWebRole(ctx context.Context, userID string) (string, bool, error) {
	client, cfg, err := b.ready()
	if err != nil {
		return "", false, err
	}
	// Web 登录授予持久会话，必须绕过操作流程使用的 30 秒角色缓存。
	user, err := client.UserView(ctx, userID, cfg.GuildID)
	if err != nil {
		return "", false, err
	}
	return b.webRoleFor(userID, user.Roles)
}

// resolveWebRole 依据 KOOK 角色计算 WebUI 权限。
func (b *Bot) resolveWebRole(ctx context.Context, userID string) (string, bool) {
	roles, err := b.userRoles(ctx, userID)
	if err != nil {
		b.deps.Logger.Warn("读取用户角色失败", "user_id", userID, "err", err)
		return "", false
	}
	role, ok, err := b.webRoleFor(userID, roles)
	if err != nil {
		b.deps.Logger.Error("解析角色映射失败", "err", err)
	}
	return role, ok && err == nil
}

func (b *Bot) webRoleFor(userID string, roles []int64) (string, bool, error) {
	roleIDs := make([]string, 0, len(roles))
	for _, roleID := range roles {
		roleIDs = append(roleIDs, strconv.FormatInt(roleID, 10))
	}

	// 服务器创建者始终视为管理员。
	b.mu.RLock()
	masterID := b.guild.MasterID
	b.mu.RUnlock()
	if masterID != "" && userID == masterID {
		return store.RoleAdmin, true, nil
	}

	return b.deps.Store.Roles.ResolveWebRole(roleIDs)
}

// ---------------------------------------------------------------------------
// 表情上角色
// ---------------------------------------------------------------------------

// handleReaction 处理表情回应：按规则给用户上角色。
func (b *Bot) handleReaction(ctx context.Context, event kook.Event) {
	body := event.Extra.Body
	userID := firstNonEmpty(body.UserID, event.AuthorID)
	messageID := firstNonEmpty(body.MsgID, event.MsgID)
	emojiID := body.Emoji.ID
	channelID := firstNonEmpty(body.ChannelID, body.TargetID, event.TargetID)

	if userID == "" || messageID == "" || emojiID == "" {
		return
	}
	if b.botUserID() == userID {
		return
	}

	rule, err := b.deps.Store.Emoji.MatchRule(messageID, emojiID)
	if err != nil {
		// 不是配置过的消息/表情，忽略。
		return
	}

	client, cfg, err := b.ready()
	if err != nil {
		return
	}

	// 需要服务器 ID：优先用事件里的，其次用配置
	guildID := firstNonEmpty(event.Extra.GuildID, cfg.GuildID)

	// 撤销该用户上一次通过表情获得的角色（避免同时持有多个颜色角色）
	if previous, err := b.deps.Store.Emoji.LastGrant(userID); err == nil && previous != nil {
		if previous.RoleID != rule.RoleID {
			if roleID, convErr := strconv.ParseInt(previous.RoleID, 10, 64); convErr == nil {
				if err := client.GuildRoleRevoke(ctx, guildID, userID, roleID); err != nil {
					b.deps.Logger.Warn("撤销旧角色失败", "user_id", userID, "role_id", previous.RoleID, "err", err)
				}
			}
		}
	}

	roleID, err := strconv.ParseInt(rule.RoleID, 10, 64)
	if err != nil {
		b.deps.Logger.Warn("表情规则中的角色 ID 非法", "role_id", rule.RoleID)
		return
	}
	if err := client.GuildRoleGrant(ctx, guildID, userID, roleID); err != nil {
		b.deps.Logger.Error("发放角色失败", "user_id", userID, "role_id", rule.RoleID, "err", err)
		if channelID != "" {
			b.sendEphemeral(ctx, channelID, userID,
				"发放角色失败，请确认机器人角色位置高于目标角色，且拥有「管理角色」权限")
		}
		return
	}

	// 记录发放结果
	if previous, err := b.deps.Store.Emoji.LastGrant(userID); err == nil && previous != nil {
		if err := b.deps.Store.Emoji.UpdateGrant(previous.ID, rule.ID, rule.EmojiID, rule.RoleID); err != nil {
			b.deps.Logger.Warn("更新发放记录失败", "err", err)
		}
	} else {
		if err := b.deps.Store.Emoji.RecordGrant(&store.EmojiGrant{
			KookUserID: userID,
			RuleID:     rule.ID,
			EmojiID:    rule.EmojiID,
			RoleID:     rule.RoleID,
			GrantedAt:  store.Now(),
		}); err != nil {
			b.deps.Logger.Warn("记录发放结果失败", "err", err)
		}
	}

	if channelID != "" {
		label := rule.Label
		if label == "" {
			label = rule.EmojiID
		}
		b.sendEphemeral(ctx, channelID, userID, fmt.Sprintf("已为你发放角色：**%s**", label))
	}
	b.deps.Logger.Info("表情上角色完成", "user_id", userID, "role_id", rule.RoleID, "emoji", rule.EmojiID)
}

// ---------------------------------------------------------------------------
// 回复辅助
// ---------------------------------------------------------------------------

// replyEphemeral 在频道内以“仅本人可见”的方式回复。
func (b *Bot) replyEphemeral(ctx context.Context, event kook.Event, content string) {
	client, _, err := b.ready()
	if err != nil {
		return
	}
	if _, err := client.SendChannelMessage(ctx, event.TargetID, kook.MsgTypeKMarkdown, content, kook.MessageOptions{
		TempTargetID: event.AuthorID,
	}); err != nil {
		b.deps.Logger.Debug("回复命令失败", "channel_id", event.TargetID, "err", err)
	}
}

// replyEphemeralCard 在频道内以“仅本人可见”的方式回复卡片。
//
// 卡片 JSON 必须用 MsgTypeCard 发送：用 KMarkdown（type 9）发送时平台不会
// 渲染卡片，而是把整段 JSON 当纯文本原样显示给用户。
func (b *Bot) replyEphemeralCard(ctx context.Context, event kook.Event, content string) {
	client, _, err := b.ready()
	if err != nil {
		return
	}
	if _, err := client.SendChannelMessage(ctx, event.TargetID, kook.MsgTypeCard, content, kook.MessageOptions{
		TempTargetID: event.AuthorID,
	}); err != nil {
		b.deps.Logger.Debug("回复卡片失败", "channel_id", event.TargetID, "err", err)
	}
}

// replyDirect 私聊回复。
func (b *Bot) replyDirect(ctx context.Context, userID, content string) {
	client, _, err := b.ready()
	if err != nil {
		return
	}
	if _, err := client.SendDirectMessage(ctx, userID, kook.MsgTypeKMarkdown, content, kook.MessageOptions{}); err != nil {
		b.deps.Logger.Warn("私聊回复失败", "user_id", userID, "err", err)
	}
}

// replyDirectCard 私聊回复卡片（同样必须用 MsgTypeCard，理由见 replyEphemeralCard）。
func (b *Bot) replyDirectCard(ctx context.Context, userID, content string) {
	client, _, err := b.ready()
	if err != nil {
		return
	}
	if _, err := client.SendDirectMessage(ctx, userID, kook.MsgTypeCard, content, kook.MessageOptions{}); err != nil {
		b.deps.Logger.Warn("私聊卡片回复失败", "user_id", userID, "err", err)
	}
}

// botUserID 返回机器人自身 ID。
func (b *Bot) botUserID() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.botUser.ID
}
