package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"vancekookticket/internal/kook"
)

// 面板默认文案（WebUI 未填写时使用）。
const (
	panelDefaultTitle  = "请点击右侧按钮发起工单"
	panelDefaultButton = "ticket"
)

// CreatePanel 在指定频道创建工单面板卡片，返回消息 ID。
//
// 供 WebUI 的面板管理使用：创建/重建都由机器人发送卡片，
// 按钮 value 由机器人签名，因此面板必须经此流程生成（不能由外部伪造）。
func (b *Bot) CreatePanel(ctx context.Context, channelID, title, buttonText string) (string, error) {
	client, _, err := b.ready()
	if err != nil {
		return "", err
	}
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return "", fmt.Errorf("频道 ID 不能为空")
	}
	if strings.TrimSpace(title) == "" {
		title = panelDefaultTitle
	}
	if strings.TrimSpace(buttonText) == "" {
		buttonText = panelDefaultButton
	}

	openValue := b.encodeButton(actionOpen, "", channelID)
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
