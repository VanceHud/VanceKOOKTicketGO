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
}

// Element 是模块内的元素（文本、按钮等）。
type Element struct {
	Type  string    `json:"type"`
	Text  *TextElem `json:"text,omitempty"`
	Value string    `json:"value,omitempty"`
	Click string    `json:"click,omitempty"`
	Theme string    `json:"theme,omitempty"`
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

// EscapeMentionText 转义用户内容中的提及语法，避免开单人伪造 @全体成员。
//
// 说明：KMarkdown 的提及写法是 (met)…(met)，如果直接把用户输入拼进卡片，
// 用户就能构造出任意提及（包括 @全体成员）。这里统一做字符替换。
func EscapeMentionText(text string) string {
	replacer := strings.NewReplacer(
		"(met)", "( met )",
		"(rol)", "( rol )",
		"(chn)", "( chn )",
	)
	return replacer.Replace(text)
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
