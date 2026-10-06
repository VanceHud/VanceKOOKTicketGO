package bot

import (
	"strings"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// 工单频道名格式：
//
//	类型｜短编号｜昵称     例：举报投诉｜A1B2｜小明
//
// 设计说明：
//   - KOOK 频道名上限为 32 个字符（官方 API 文档未标注，但服务端按 32 校验）。
//     旧格式「TK-260105-A1B2 | 昵称」固定部分就占 17 字符，昵称再留 20 字符必然超限；
//   - 类型置前：频道列表里同类型工单命名一致，管理员扫一眼即可按类型辨认；
//   - 频道名只保留编号的随机段（不可枚举，便于口头指代）；完整编号
//     TK-260105-A1B2 仍显示在工单卡片、关闭通知与 WebUI 中。
const (
	// ChannelNameMaxLength 是 KOOK 频道名长度上限（按字符计，不是字节）。
	ChannelNameMaxLength = 32
	// channelNameSeparator 用全角竖线：比 " | " 省两个字符，视觉分隔依然清晰。
	channelNameSeparator = "｜"
	// channelTypeMaxLength 是频道名中类型名的长度上限（完整类型名仍显示在卡片中）。
	channelTypeMaxLength = 12
)

// TicketChannelName 拼装工单频道名：类型｜短编号｜昵称。
//
// 类型名与昵称按剩余预算安全截断（按字符计），保证结果不超过 ChannelNameMaxLength；
// 类型名与昵称同时很长时优先保全类型名，昵称拿到剩余空间。
// 类型名为空时退化为「短编号｜昵称」，昵称为空时省略最后一段。
func TicketChannelName(typeName, ticketNo, displayName string) string {
	typePart := truncateToBudget(strings.TrimSpace(typeName), channelTypeMaxLength)
	if typePart == "" {
		typePart = store.UnnamedTypeLabel
	}
	code := ShortTicketCode(ticketNo)
	name := strings.TrimSpace(displayName)

	parts := make([]string, 0, 3)
	parts = append(parts, typePart)
	if code != "" {
		parts = append(parts, code)
	}
	if name != "" {
		// 剩余预算全部给昵称：类型名越短，昵称能保留的字符越多。
		used := 0
		for _, part := range parts {
			used += len([]rune(part)) + len([]rune(channelNameSeparator))
		}
		if available := ChannelNameMaxLength - used; available > 0 {
			parts = append(parts, truncateToBudget(name, available))
		}
	}

	// 兜底：任何意外输入都不会突破平台上限。
	return truncateToBudget(strings.Join(parts, channelNameSeparator), ChannelNameMaxLength)
}

// ShortTicketCode 从完整编号中取出短编号（最后一个 '-' 之后的部分）。
//
// 例：TK-260105-A1B2 → A1B2。编号由 ticketno 包生成、格式固定；
// 解析不到分隔符时原样返回，再由截断兜底。
func ShortTicketCode(ticketNo string) string {
	value := strings.TrimSpace(ticketNo)
	if value == "" {
		return ""
	}
	if index := strings.LastIndex(value, "-"); index >= 0 && index < len(value)-1 {
		return value[index+1:]
	}
	return value
}

// truncateToBudget 按字符截断，超长时用「…」占位并保证总长度不超过 limit。
//
// 与 truncateRunes 的区别：后者是在 limit 之外追加省略号（结果可能有 limit+1 个字符），
// 适合“内容截断”；频道名有硬上限，必须用这里的预算内截断。
func truncateToBudget(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}
