package kook

import "testing"

func TestNormalizePanelTextSplitsLeadingHeading(t *testing.T) {
	got := NormalizePanelText("# 请点击按钮发起工单TEST1\n123123\n**加粗**、__下划线__")

	if got.Heading != "请点击按钮发起工单TEST1" {
		t.Fatalf("首行标题应作为卡片大标题，实际: %q", got.Heading)
	}
	want := "123123\n**加粗**、(ins)下划线(ins)"
	if got.Body != want {
		t.Fatalf("正文不符\n期望: %q\n实际: %q", want, got.Body)
	}
}

func TestNormalizePanelTextKeepsHeadingInBodyWhenNotFirst(t *testing.T) {
	got := NormalizePanelText("说明\n# 二级说明\n- 第一项")

	if got.Heading != "" {
		t.Fatalf("非首行标题不应成为卡片大标题，实际: %q", got.Heading)
	}
	want := "说明\n**二级说明**\n• 第一项"
	if got.Body != want {
		t.Fatalf("正文不符\n期望: %q\n实际: %q", want, got.Body)
	}
}

func TestNormalizePanelTextHeadingOnlyFallsBackToBody(t *testing.T) {
	// 卡片必须保留内容模块，只有标题时退化为加粗正文。
	got := NormalizePanelText("# 仅标题")

	if got.Heading != "" {
		t.Fatalf("正文为空时不应拆出标题，实际: %q", got.Heading)
	}
	if got.Body != "**仅标题**" {
		t.Fatalf("正文不符: %q", got.Body)
	}
}

func TestNormalizePanelTextIgnoresLeadingBlankLines(t *testing.T) {
	got := NormalizePanelText("\n\n# 标题\n正文")

	if got.Heading != "标题" || got.Body != "正文" {
		t.Fatalf("应跳过前导空行，实际: %+v", got)
	}
}

func TestNormalizePanelTextPlainTextHasNoHeading(t *testing.T) {
	got := NormalizePanelText("请点击右侧按钮发起工单")

	if got.Heading != "" || got.Body != "请点击右侧按钮发起工单" {
		t.Fatalf("无标题文案不应被改写，实际: %+v", got)
	}
}

func TestNormalizeKMarkdownConversions(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "标题降级为加粗",
			src:  "## 处理范围",
			want: "**处理范围**",
		},
		{
			name: "已加粗的标题不重复包裹",
			src:  "# **处理范围**",
			want: "**处理范围**",
		},
		{
			name: "下划线转 (ins)",
			src:  "__下划线__ 中间 __也是__",
			want: "(ins)下划线(ins) 中间 (ins)也是(ins)",
		},
		{
			name: "列表转项目符号并保留缩进",
			src:  "- 账号问题\n  * 充值问题\n+ 其它",
			want: "• 账号问题\n  • 充值问题\n• 其它",
		},
		{
			name: "加粗斜体删除线行内代码不受影响",
			src:  "**加粗**、*斜体*、~~删除线~~、`行内代码`",
			want: "**加粗**、*斜体*、~~删除线~~、`行内代码`",
		},
		{
			name: "代码块内的标记不转换",
			src:  "```\n# 注释\n- 参数\n__name__\n```",
			want: "```\n# 注释\n- 参数\n__name__\n```",
		},
		{
			name: "行内代码里的下划线不转换",
			src:  "`__init__` 与 __文本__",
			want: "`__init__` 与 (ins)文本(ins)",
		},
		{
			name: "分隔线与引用不受影响",
			src:  "---\n> 引用",
			want: "---\n> 引用",
		},
		{
			name: "无空格的话题标签与行中井号不当标题",
			src:  "#话题 与 行中的 # 井号",
			want: "#话题 与 行中的 # 井号",
		},
		{
			name: "Windows 换行被归一化",
			src:  "# 标题\r\n- 项目",
			want: "**标题**\n• 项目",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeKMarkdown(tc.src); got != tc.want {
				t.Fatalf("转换结果不符\n期望: %q\n实际: %q", tc.want, got)
			}
		})
	}
}

func TestNormalizePanelTextPlainifiesHeadingMarkers(t *testing.T) {
	// header 模块只能展示纯文本，标记必须去掉，提及/链接退化成可读文字。
	got := NormalizePanelText("# **重要** (met)123(met) [帮助](https://example.com) `code`\n正文")

	if got.Heading != "重要 @123 帮助 code" {
		t.Fatalf("标题纯文本化不符: %q", got.Heading)
	}
}

func TestNormalizePanelTextTruncatesLongHeading(t *testing.T) {
	long := ""
	for i := 0; i < MaxHeaderRunes+20; i++ {
		long += "标"
	}
	got := NormalizePanelText("# " + long + "\n正文")

	runes := []rune(got.Heading)
	if len(runes) != MaxHeaderRunes {
		t.Fatalf("标题应截断到 %d 个字符，实际 %d", MaxHeaderRunes, len(runes))
	}
	if runes[len(runes)-1] != '…' {
		t.Fatalf("截断后应有省略号: %q", got.Heading)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := TruncateRunes("abc", 5); got != "abc" {
		t.Fatalf("未超长应原样返回: %q", got)
	}
	if got := TruncateRunes("abcd", 3); got != "ab…" {
		t.Fatalf("超长应截断并补省略号: %q", got)
	}
}
