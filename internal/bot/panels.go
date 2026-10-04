package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"vancekookticket/internal/kook"
	"vancekookticket/internal/store"
)

// 面板默认文案（WebUI 未填写时使用）。
const (
	panelDefaultTitle  = "请点击右侧按钮发起工单"
	panelDefaultButton = "ticket"
)

// SendPanelCard 按面板记录发送一张工单按钮卡片，返回消息 ID。
//
// 按钮 value 由机器人签名并内嵌面板 ID，因此同一频道内的多张卡片
// 都能在点击时对应到自己的面板配置（尤其是面板级管理员角色）。
// 供 WebUI 面板管理与 /ticket 命令共用：创建/重建都必须经此流程，
// 不能由外部伪造卡片。
//
// 本方法不写数据库：消息 ID 的持久化由调用方完成，以便先落库拿到面板 ID、
// 再发卡片、最后回写 msg_id（失败时回滚记录）。
func (b *Bot) SendPanelCard(ctx context.Context, panel *store.Panel, buttonText string) (string, error) {
	client, _, err := b.ready()
	if err != nil {
		return "", err
	}
	if panel == nil {
		return "", fmt.Errorf("面板记录不能为空")
	}
	channelID := strings.TrimSpace(panel.ChannelID)
	if channelID == "" {
		return "", fmt.Errorf("频道 ID 不能为空")
	}

	// 仅在为空时替换为默认文案，不 trim 正文本身，避免破坏 Markdown 的首行缩进。
	title := panel.Title
	if strings.TrimSpace(title) == "" {
		title = panelDefaultTitle
	}
	// 按钮文字优先级：调用方传入 > 面板记录中的值 > 默认值。
	buttonText = strings.TrimSpace(buttonText)
	if buttonText == "" {
		buttonText = strings.TrimSpace(panel.ButtonText)
	}
	if buttonText == "" {
		buttonText = panelDefaultButton
	}

	openValue := b.encodeButton(actionOpen, "", channelID, panel.ID)
	content := b.panelCard(title, buttonText, openValue)

	message, err := client.SendChannelMessage(ctx, channelID, kook.MsgTypeCard, content, kook.MessageOptions{})
	if err != nil {
		return "", err
	}
	return message.ID, nil
}

// DeleteMessage 尽力删除一条消息（用于重建面板时清理旧卡片）。
//
// 消息可能已被手动删除，因此“找不到”视为成功。
func (b *Bot) DeleteMessage(ctx context.Context, msgID string) error {
	client, _, err := b.ready()
	if err != nil {
		return err
	}
	if strings.TrimSpace(msgID) == "" {
		return nil
	}
	if err := client.DeleteChannelMessage(ctx, msgID); err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

// ChannelInfo 按 ID 返回频道信息（优先使用缓存，避免频繁请求）。
func (b *Bot) ChannelInfo(ctx context.Context, channelID string) (*kook.Channel, error) {
	channels, err := b.GuildChannels(ctx)
	if err != nil {
		return nil, err
	}
	for i := range channels {
		if channels[i].ID == channelID {
			return &channels[i], nil
		}
	}
	return nil, fmt.Errorf("在服务器中找不到频道 %s（请确认机器人可见该频道）", channelID)
}

// RoleInfo 按角色 ID 返回角色名（找不到时返回空串，不视为错误）。
func (b *Bot) RoleInfo(ctx context.Context, roleID string) string {
	roles, err := b.GuildRoles(ctx)
	if err != nil {
		return ""
	}
	for _, role := range roles {
		if fmt.Sprint(role.RoleID) == roleID {
			return role.Name
		}
	}
	return ""
}

// isNotFound 判断错误是否为“资源不存在”。
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *kook.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Is(kook.ErrNotFound)
	}
	return false
}
