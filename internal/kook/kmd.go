package kook

import (
	"regexp"
	"strings"
)

// KOOK 的 KMarkdown 只实现了 Markdown 的一部分语法。官方文档里支持的写法只有
// **加粗**、*斜体*、~~删除线~~、`行内代码`、```代码块```、> 引用、--- 分隔线、
// [链接](url)，以及 (ins)/(spl)/(met)/(rol)/(chn)/(font) 这些自定义标签；
// 常见 Markdown 的 # 标题、__下划线__、- 列表 KOOK 并不认识，会原样显示。
//
// WebUI 的实时预览用的是通用 Markdown，所以这些写法“预览正常、发到 KOOK 却失效”。
// 本文件负责在发送前把面板文案归一化成 KOOK 真正能渲染的写法：
//
//	# 标题      → 卡片 header 模块（KOOK 唯一的大标题形式）；不在正文首行时退化为 **标题**
//	__下划线__  → (ins)下划线(ins)
//	- 项目      → • 项目
//
// 其余写法（加粗、斜体、删除线、行内代码、代码块、引用、链接、提及）KOOK 本身支持，
// 原样透传；代码块（``` 围栏）内的内容不做任何转换，避免把代码注释当成语法处理。

// MaxHeaderRunes 是卡片 header 模块的内容上限：KOOK 限制为 100 个字。
const MaxHeaderRunes = 100

var (
	// headingPattern 匹配 Markdown 标题行（# 后必须跟空白，避免把 #话题 当成标题）。
	headingPattern = regexp.MustCompile(`^(#{1,6})[ \t]+(.*)$`)
	// listPattern 匹配 Markdown 无序列表项，捕获缩进与内容。
	listPattern = regexp.MustCompile(`^([ \t]*)[-*+][ \t]+(.*)$`)
	// underlinePattern 匹配 Markdown 下划线语法。
	underlinePattern = regexp.MustCompile(`__([^_]+)__`)
	// linkPattern 匹配 Markdown 链接。
	linkPattern = regexp.MustCompile(`\[([^\]\n]*)\]\((?:https?://)[^\s)]*\)`)
	// fontPattern 匹配 KOOK 彩色文本标签。
	fontPattern = regexp.MustCompile(`\(font\)([^()]*?)\(font\)(?:\[[^\]]*\])?`)
	// mentionPattern 匹配 KOOK 各类提及标签（用户/角色/频道）。
	mentionPattern = regexp.MustCompile(`\((met|rol|chn)\)\s*([^)\s]+)\s*\((met|rol|chn)\)`)
)

// PanelText 是面板文案归一化后的结果。
//
// Heading 交给卡片的 header 模块（纯文本），Body 交给 section 的 kmarkdown 模块。
type PanelText struct {
	Heading string
	Body    string
}

// NormalizePanelText 归一化面板文案，并把正文首行的标题拆出来作为卡片大标题。
//
// 只有正文首行（允许前面有空行）的 "# 标题" 才会成为 header 模块：
// 卡片只应有一个标题，且 header 模块必须是纯文本、长度受限。
func NormalizePanelText(src string) PanelText {
	lines := strings.Split(normalizeNewlines(src), "\n")

	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}

	if start < len(lines) {
		if match := headingPattern.FindStringSubmatch(lines[start]); match != nil {
			heading := plainText(match[2])
			body := NormalizeKMarkdown(strings.Join(lines[start+1:], "\n"))
			// 标题被标记清空（例如只有 "**"）或没有正文时，不做拆分：
			// 卡片必须保留一个内容模块，且这行退化成加粗更安全。
			if heading != "" && strings.TrimSpace(body) != "" {
				return PanelText{Heading: TruncateRunes(heading, MaxHeaderRunes), Body: body}
			}
		}
	}

	return PanelText{Body: NormalizeKMarkdown(strings.Join(lines[start:], "\n"))}
}

// NormalizeKMarkdown 把 Markdown 风格的写法转换成 KOOK KMarkdown 支持的写法。
func NormalizeKMarkdown(src string) string {
	lines := strings.Split(normalizeNewlines(src), "\n")
	out := make([]string, 0, len(lines))
	inFence := false

	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			out = append(out, line)
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}

		if match := headingPattern.FindStringSubmatch(line); match != nil {
			out = append(out, bold(convertUnderline(strings.TrimSpace(match[2]))))
			continue
		}
		if match := listPattern.FindStringSubmatch(line); match != nil {
			out = append(out, match[1]+"• "+match[2])
			continue
		}
		out = append(out, convertUnderline(line))
	}

	return strings.Join(out, "\n")
}

// plainText 去掉常见 KMarkdown 标记，提及与链接保留可读文字。
// 用于只能展示纯文本的位置（卡片 header 模块）。
func plainText(src string) string {
	text := linkPattern.ReplaceAllString(src, "$1")
	text = fontPattern.ReplaceAllString(text, "$1")
	text = mentionPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := mentionPattern.FindStringSubmatch(match)
		if parts[1] != parts[3] {
			// 起止标签不匹配（用户手写的乱码），原样保留。
			return match
		}
		switch parts[1] {
		case "met", "rol":
			return "@" + parts[2]
		default:
			return "#" + parts[2]
		}
	})
	text = strings.NewReplacer(
		"**", "",
		"__", "",
		"~~", "",
		"(ins)", "",
		"(spl)", "",
		"`", "",
	).Replace(text)
	return strings.TrimSpace(text)
}

// convertUnderline 把 __下划线__ 转成 KOOK 的 (ins)下划线(ins)。
//
// 行内代码里的内容原样保留：`__init__` 这类代码不应被改写。
func convertUnderline(line string) string {
	parts := strings.Split(line, "`")
	for i := 0; i < len(parts); i += 2 {
		parts[i] = underlinePattern.ReplaceAllString(parts[i], "(ins)$1(ins)")
	}
	return strings.Join(parts, "`")
}

// bold 把文本包成加粗，已经是加粗的内容原样返回。
func bold(text string) string {
	if len(text) >= 4 && strings.HasPrefix(text, "**") && strings.HasSuffix(text, "**") {
		return text
	}
	return "**" + text + "**"
}

// TruncateRunes 按字符数截断文本，超出时补省略号。
func TruncateRunes(text string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max-1]) + "…"
}

// normalizeNewlines 统一换行符，避免 Windows 换行导致的行首匹配失败。
func normalizeNewlines(src string) string {
	return strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(src)
}
