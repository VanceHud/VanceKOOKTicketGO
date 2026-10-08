package kook

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 卡片主题。
const (
	CardThemePrimary   = "primary"
	CardThemeSuccess   = "success"
	CardThemeDanger    = "danger"
	CardThemeWarning   = "warning"
	CardThemeInfo      = "info"
	CardThemeSecondary = "secondary"
	CardThemeNone      = "none"
)

// 按钮点击行为：返回 value 给机器人（本项目全部按钮都用这种方式）。
const (
	ClickReturnValue = "return-val"
)

// Card 是卡片消息中的一个卡片。
type Card struct {
	Type    string   `json:"type"`
	Theme   string   `json:"theme,omitempty"`
	Size    string   `json:"size,omitempty"`
	Modules []Module `json:"modules"`
}

// Module 是卡片中的模块（标题/内容/分隔线/按钮组等）。
type Module struct {
	Type      string    `json:"type"`
	Text      *TextElem `json:"text,omitempty"`
	Elements  []Element `json:"elements,omitempty"`
	Accessory *Element  `json:"accessory,omitempty"`
	Mode      string    `json:"mode,omitempty"`
	// Src / Title / Cover 用于 file、audio、video 模块。
	Src   string `json:"src,omitempty"`
	Title string `json:"title,omitempty"`
	Cover string `json:"cover,omitempty"`
	// EndTime / StartTime 用于 countdown 模块。
	EndTime   int64 `json:"endTime,omitempty"`
	StartTime int64 `json:"startTime,omitempty"`
}

// Element 是模块内的元素（文本、按钮、图片等）。
type Element struct {
	Type  string    `json:"type"`
	Text  *TextElem `json:"text,omitempty"`
	Value string    `json:"value,omitempty"`
	Click string    `json:"click,omitempty"`
	Theme string    `json:"theme,omitempty"`
	// Content 是 context 模块中 plain-text / kmarkdown 元素的直接内容。
	Content string `json:"content,omitempty"`
	// Src / Alt / Circle / FallbackURL / Size 是图片元素字段。
	Src         string `json:"src,omitempty"`
	Alt         string `json:"alt,omitempty"`
	Circle      bool   `json:"circle,omitempty"`
	FallbackURL string `json:"fallbackUrl,omitempty"`
	Size        string `json:"size,omitempty"`
}

// TextElem 是文本元素。
type TextElem struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// PlainText 构造普通文本。
func PlainText(content string) *TextElem {
	return &TextElem{Type: "plain-text", Content: content}
}

// KMarkdown 构造 KMarkdown 文本（支持 (met)id(met) 这类提及语法）。
func KMarkdown(content string) *TextElem {
	return &TextElem{Type: "kmarkdown", Content: content}
}

// MentionUser 生成用户提及语法。
func MentionUser(userID string) string { return fmt.Sprintf("(met)%s(met)", userID) }

// MentionRole 生成角色提及语法。
func MentionRole(roleID string) string { return fmt.Sprintf("(rol)%s(rol)", roleID) }

// MentionChannel 生成频道提及语法。
func MentionChannel(channelID string) string { return fmt.Sprintf("(chn)%s(chn)", channelID) }

// IsUserID 判断字符串是否形如 KOOK 用户 ID。
//
// KOOK 的 ID 是雪花 ID（纯十进制数字，见 internal/api 的同名校验规则）。
// WebUI 账号名（如 admin）、"system" 这类标识不是 ID，
// 套进提及语法后客户端只能显示「@用户不存在」这样的占位文案。
func IsUserID(id string) bool {
	if len(id) < 5 || len(id) > 32 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}
	return true
}

// DisplayUser 生成卡片里展示用户的文本：
//   - id 是 KOOK 用户 ID 时返回提及语法，客户端会渲染成 @昵称；
//   - 否则退回纯文本名字——WebUI 操作者的 id 是账号名、系统任务用 "system"，
//     这些都不是 KOOK ID，直接拼 (met)…(met) 会显示成「@用户不存在」。
//
// 名字来自数据库或用户输入，同样需要做提及转义。
func DisplayUser(id, name string) string {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if IsUserID(id) {
		return MentionUser(id)
	}
	if name != "" {
		return EscapeMentionText(name)
	}
	if id == "" || id == "system" || id == "bot" {
		return "系统"
	}
	return EscapeMentionText(id)
}

// mentionEscaper 是包级复用的转义器：每条归档消息与卡片文案都要经过它，
// 每次调用重建 Replacer 会带来不必要的分配。
var mentionEscaper = strings.NewReplacer(
	"(met)", "( met )",
	"(rol)", "( rol )",
	"(chn)", "( chn )",
)

// EscapeMentionText 转义用户内容中的提及语法，避免开单人伪造 @全体成员。
//
// 说明：KMarkdown 的提及写法是 (met)…(met)，如果直接把用户输入拼进卡片，
// 用户就能构造出任意提及（包括 @全体成员）。这里统一做字符替换。
func EscapeMentionText(text string) string {
	return mentionEscaper.Replace(text)
}

// NewCard 创建一张卡片。
func NewCard(theme string) *Card {
	if theme == "" {
		theme = CardThemePrimary
	}
	return &Card{Type: "card", Theme: theme, Size: "lg", Modules: []Module{}}
}

// Header 追加标题模块。
func (c *Card) Header(text string) *Card {
	c.Modules = append(c.Modules, Module{Type: "header", Text: PlainText(text)})
	return c
}

// KMarkdownSection 追加 KMarkdown 内容模块。
func (c *Card) KMarkdownSection(content string) *Card {
	c.Modules = append(c.Modules, Module{Type: "section", Text: KMarkdown(content)})
	return c
}

// Divider 追加分隔线。
func (c *Card) Divider() *Card {
	c.Modules = append(c.Modules, Module{Type: "divider"})
	return c
}

// Context 追加说明行。
func (c *Card) Context(content string) *Card {
	c.Modules = append(c.Modules, Module{
		Type:     "context",
		Elements: []Element{{Type: "kmarkdown", Text: KMarkdown(content)}},
	})
	return c
}

// Button 是构建按钮组的参数。
type Button struct {
	Text  string
	Value string
	Theme string
}

// ActionGroup 追加按钮组。
func (c *Card) ActionGroup(buttons ...Button) *Card {
	elements := make([]Element, 0, len(buttons))
	for _, button := range buttons {
		theme := button.Theme
		if theme == "" {
			theme = CardThemePrimary
		}
		elements = append(elements, Element{
			Type:  "button",
			Text:  PlainText(button.Text),
			Value: button.Value,
			Click: ClickReturnValue,
			Theme: theme,
		})
	}
	c.Modules = append(c.Modules, Module{Type: "action-group", Elements: elements})
	return c
}

// MarshalCards 把卡片数组序列化成 message/create 需要的 content 字符串。
func MarshalCards(cards ...Card) (string, error) {
	if len(cards) == 0 {
		return "", fmt.Errorf("卡片内容为空")
	}
	payload, err := json.Marshal(cards)
	if err != nil {
		return "", fmt.Errorf("序列化卡片失败: %w", err)
	}
	return string(payload), nil
}

// SingleCard 便捷方法：把一张卡片序列化为 content。
func SingleCard(card *Card) (string, error) {
	if card == nil {
		return "", fmt.Errorf("卡片为空")
	}
	return MarshalCards(*card)
}

// NoticeCard 构造只含一段 KMarkdown 的提示卡片。
func NoticeCard(theme, heading, content string) string {
	card := NewCard(theme)
	if heading != "" {
		card.Header(heading)
	}
	if content != "" {
		card.KMarkdownSection(content)
	}
	encoded, err := SingleCard(card)
	if err != nil {
		// 兜底：序列化失败时返回纯文本，保证调用方不会因为卡片异常而静默失败。
		return heading + "\n" + content
	}
	return encoded
}

// ---------------------------------------------------------------------------
// 卡片解析（用于聊天记录归档与渲染兜底）
// ---------------------------------------------------------------------------

// ParseCards 解析卡片 JSON，兼容「单个对象」与「卡片数组」两种写法。
func ParseCards(content string) ([]Card, error) {
	raw := strings.TrimSpace(content)
	if raw == "" {
		return nil, fmt.Errorf("卡片内容为空")
	}
	if raw[0] == '[' {
		var cards []Card
		if err := json.Unmarshal([]byte(raw), &cards); err != nil {
			return nil, err
		}
		return cards, nil
	}
	var card Card
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		return nil, err
	}
	return []Card{card}, nil
}

// CardSummary 从卡片 JSON 中提取可读文本摘要。
//
// 用途：聊天记录检索、导出与不支持卡片渲染时的兜底展示。
// 无法解析或没有文本时返回空串，由调用方决定兜底文案。
func CardSummary(content string) string {
	cards, err := ParseCards(content)
	if err != nil {
		return ""
	}
	var lines []string
	for _, card := range cards {
		for _, module := range card.Modules {
			if text := modulePlainText(module); text != "" {
				lines = append(lines, text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// CardAttachments 提取卡片中的媒体资源（文件/音频/视频/图片模块）。
//
// 平台已把文件消息转为卡片消息下发，事件的 content 为空，
// 只有解析卡片才能拿到可下载的地址与文件名。
func CardAttachments(content string) []Attachment {
	cards, err := ParseCards(content)
	if err != nil {
		return nil
	}
	var out []Attachment
	for _, card := range cards {
		for _, module := range card.Modules {
			switch module.Type {
			case "file", "audio", "video":
				if module.Src != "" {
					out = append(out, Attachment{Type: module.Type, URL: module.Src, Name: module.Title})
				}
			case "image-group", "container":
				for _, element := range module.Elements {
					if element.Type == "image" && element.Src != "" {
						out = append(out, Attachment{Type: "image", URL: element.Src, Name: element.Alt})
					}
				}
			}
		}
	}
	return out
}

// modulePlainText 提取模块中的可读文本。
func modulePlainText(module Module) string {
	switch module.Type {
	case "header", "section":
		if module.Text != nil {
			return strings.TrimSpace(module.Text.Content)
		}
	case "context":
		var parts []string
		for _, element := range module.Elements {
			if text := elementPlainText(element); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	case "action-group":
		var parts []string
		for _, element := range module.Elements {
			if text := elementPlainText(element); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	case "file", "audio", "video":
		title := strings.TrimSpace(module.Title)
		if title == "" {
			title = "附件"
		}
		return "[" + attachmentTypeName(module.Type) + "] " + title
	}
	return ""
}

// elementPlainText 提取元素中的文本（兼容 text 嵌套与直接 content 两种形态）。
func elementPlainText(element Element) string {
	if element.Text != nil && strings.TrimSpace(element.Text.Content) != "" {
		return strings.TrimSpace(element.Text.Content)
	}
	return strings.TrimSpace(element.Content)
}

// attachmentTypeName 把卡片附件模块类型转成中文名。
func attachmentTypeName(moduleType string) string {
	switch moduleType {
	case "file":
		return "文件"
	case "audio":
		return "语音"
	case "video":
		return "视频"
	case "image":
		return "图片"
	default:
		return "附件"
	}
}
