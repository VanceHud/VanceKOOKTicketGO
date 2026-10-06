package bot

import (
	"strings"
	"testing"
)

// TestTicketChannelNameFitsKookLimit 验证频道名格式为「类型｜短编号｜昵称」，
// 且任何输入下都不超过 KOOK 的 32 字符上限。
func TestTicketChannelNameFitsKookLimit(t *testing.T) {
	cases := []struct {
		name     string
		typeName string
		no       string
		display  string
		want     []string
	}{
		{
			name: "标准格式", typeName: "举报投诉", no: "TK-260105-A1B2", display: "小明",
			want: []string{"举报投诉", "A1B2", "小明"},
		},
		{
			// 类型缺失（历史数据）时用占位，不能出现空段。
			name: "类型为空", typeName: "  ", no: "TK-260105-A1B2", display: "小明",
			want: []string{"未分类", "A1B2", "小明"},
		},
		{
			name: "昵称为空", typeName: "充值问题", no: "TK-260105-C7QP", display: "",
			want: []string{"充值问题", "C7QP"},
		},
		{
			// 同日冲突加宽后的 5 位随机段同样要能放进去。
			name: "加宽编号", typeName: "账号问题", no: "TK-260105-A1B2C", display: "李雷",
			want: []string{"账号问题", "A1B2C", "李雷"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TicketChannelName(tc.typeName, tc.no, tc.display)
			if length := len([]rune(got)); length > ChannelNameMaxLength {
				t.Fatalf("频道名超过 %d 字符（实际 %d）: %s", ChannelNameMaxLength, length, got)
			}
			if !strings.HasPrefix(got, tc.want[0]) {
				t.Fatalf("频道名应以类型名开头，得到: %s", got)
			}
			for _, part := range tc.want {
				if !strings.Contains(got, part) {
					t.Fatalf("频道名应包含 %q，实际: %s", part, got)
				}
			}
			if tc.display == "" && strings.HasSuffix(got, channelNameSeparator) {
				t.Fatalf("昵称为空时不应留下分隔符: %s", got)
			}
		})
	}
}

// TestTicketChannelNameTruncatesLongParts 验证超长类型名与昵称按「类型优先」分配预算：
// 短编号必须完整保留，类型名最多 12 字符，昵称拿到剩余空间。
func TestTicketChannelNameTruncatesLongParts(t *testing.T) {
	typeName := strings.Repeat("类", 30)
	display := strings.Repeat("名", 40)

	got := TicketChannelName(typeName, "TK-260105-A1B2", display)
	if length := len([]rune(got)); length > ChannelNameMaxLength {
		t.Fatalf("频道名超过上限（实际 %d）: %s", length, got)
	}
	if !strings.Contains(got, "A1B2") {
		t.Fatalf("短编号必须完整保留: %s", got)
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("超长内容应带省略号: %s", got)
	}

	parts := strings.Split(got, channelNameSeparator)
	if len(parts) != 3 {
		t.Fatalf("应有三段（类型｜短编号｜昵称），实际 %d 段: %s", len(parts), got)
	}
	if length := len([]rune(parts[0])); length > channelTypeMaxLength {
		t.Fatalf("类型段不应超过 %d 字符（实际 %d）: %s", channelTypeMaxLength, length, parts[0])
	}
	// 固定开销 2 个分隔符 + 4 位短编号 → 昵称至少能拿到 14 个字符。
	if length := len([]rune(parts[2])); length < 14 {
		t.Fatalf("昵称应拿到剩余预算（至少 14 字符），实际 %d: %s", length, parts[2])
	}
}

// TestTicketChannelNamePrefersLongerNicknameWhenTypeIsShort 验证类型名短时昵称可用空间更大。
func TestTicketChannelNamePrefersLongerNicknameWhenTypeIsShort(t *testing.T) {
	name := strings.Repeat("名", 40)
	long := strings.Split(TicketChannelName("举报", "TK-260105-A1B2", name), channelNameSeparator)
	short := strings.Split(TicketChannelName(strings.Repeat("类", 12), "TK-260105-A1B2", name), channelNameSeparator)

	// 两种情况下总长度都会被 32 字符上限截平，但短类型名应把更多预算让给昵称。
	if len([]rune(long[2])) <= len([]rune(short[2])) {
		t.Fatalf("类型名更短时昵称应保留更多字符: %d vs %d", len([]rune(long[2])), len([]rune(short[2])))
	}
	if len([]rune(long[0]))+len([]rune(long[2])) != len([]rune(short[0]))+len([]rune(short[2])) {
		t.Fatalf("类型+昵称的总预算应恒定：%q vs %q", long, short)
	}
}

func TestShortTicketCode(t *testing.T) {
	cases := map[string]string{
		"TK-260105-A1B2":  "A1B2",
		"TK-260105-A1B2C": "A1B2C",
		"":                "",
		"A1B2":            "A1B2",
	}
	for input, want := range cases {
		if got := ShortTicketCode(input); got != want {
			t.Fatalf("ShortTicketCode(%q) = %q，期望 %q", input, got, want)
		}
	}
}

func TestTruncateToBudget(t *testing.T) {
	// 预算内不截断、不添加省略号。
	if got := truncateToBudget("小明", 4); got != "小明" {
		t.Fatalf("预算内不应截断: %q", got)
	}
	// 超预算时总长度不超过预算。
	got := truncateToBudget("超级长的用户昵称啊啊啊啊", 5)
	if length := len([]rune(got)); length != 5 {
		t.Fatalf("截断结果应恰好为 5 个字符（实际 %d）: %s", length, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("截断结果应以省略号结尾: %s", got)
	}
	if got := truncateToBudget("abc", 0); got != "" {
		t.Fatalf("预算为 0 时应返回空串，实际 %q", got)
	}
}
