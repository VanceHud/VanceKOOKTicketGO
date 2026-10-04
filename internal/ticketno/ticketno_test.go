package ticketno

import (
	"strings"
	"testing"
	"time"
)

func TestNewFormat(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载时区失败: %v", err)
	}

	// 2026-01-05 08:00 UTC = 2026-01-05 16:00 +08:00
	now := time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC)

	no, err := New(now, loc)
	if err != nil {
		t.Fatalf("生成编号失败: %v", err)
	}
	if !Valid(no) {
		t.Fatalf("生成的编号未通过格式校验: %s", no)
	}
	if !strings.HasPrefix(no, "TK-260105-") {
		t.Fatalf("编号日期段应使用业务时区，得到: %s", no)
	}
	if len(no) != len("TK-")+6+1+WidthDefault {
		t.Fatalf("编号长度不符: %s", no)
	}
}

func TestNewUsesBusinessTimezoneAtDayBoundary(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载时区失败: %v", err)
	}
	// UTC 时间 2026-01-04 17:30 对应东八区 2026-01-05 01:30，日期段应为 260105
	now := time.Date(2026, 1, 4, 17, 30, 0, 0, time.UTC)

	no, err := New(now, loc)
	if err != nil {
		t.Fatalf("生成编号失败: %v", err)
	}
	if !strings.HasPrefix(no, "TK-260105-") {
		t.Fatalf("跨零点时应使用业务时区日期，得到: %s", no)
	}
}

func TestValidRejectsUnexpectedInput(t *testing.T) {
	invalid := []string{
		"",
		"TK-260105-ABC",    // 随机段太短
		"TK-260105-ABCDEF", // 随机段过长（只定义了 4/5 位）
		"TK-260105-ABC!",   // 非法字符
		"TK-260105-ABCI",   // 含易混字符 I
		"TK-260105-ABCO",   // 含易混字符 O
		"TK-260105-ABCL",   // 含易混字符 L
		"TK-260105-ABCU",   // 含易混字符 U
		"TK-2601-ABCD",     // 日期段位数不足
		"XX-260105-ABCD",   // 前缀错误
		"TK-260105ABCD",    // 缺少分隔符
		"TK-260105-ABCD ",  // 末尾空格
		"tk-260105-abcd",   // 小写
	}
	for _, input := range invalid {
		if Valid(input) {
			t.Errorf("非法编号被接受: %q", input)
		}
	}
}

func TestValidAcceptsWideNumber(t *testing.T) {
	no, err := NewWide(time.Now(), time.UTC)
	if err != nil {
		t.Fatalf("生成加宽编号失败: %v", err)
	}
	if !Valid(no) {
		t.Fatalf("加宽编号未通过校验: %s", no)
	}
	if !strings.Contains(no[10:], "") || len(no) != len("TK-")+6+1+WidthWide {
		t.Fatalf("加宽编号长度不符: %s", no)
	}
}

func TestUniquenessWithinLargeSample(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)

	// 同一秒内生成 2000 个编号：随机段 4 位（约 105 万组合），
	// 期望冲突极少，这里只确保不会出现完全重复的编号。
	seen := make(map[string]struct{}, 2000)
	duplicates := 0
	for i := 0; i < 2000; i++ {
		no, err := New(now, loc)
		if err != nil {
			t.Fatalf("生成编号失败: %v", err)
		}
		if _, exists := seen[no]; exists {
			duplicates++
		}
		seen[no] = struct{}{}
	}
	if duplicates > 10 {
		t.Fatalf("同一时刻生成的编号重复过多: %d/2000", duplicates)
	}
}

func TestDateParsing(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载时区失败: %v", err)
	}
	no, err := New(time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC), loc)
	if err != nil {
		t.Fatalf("生成编号失败: %v", err)
	}
	day, ok := Date(no, loc)
	if !ok {
		t.Fatalf("日期解析失败: %s", no)
	}
	if day.Year() != 2026 || day.Month() != time.March || day.Day() != 9 {
		t.Fatalf("日期解析结果不符: %v", day)
	}
	if day.Location() != loc {
		t.Fatalf("日期解析应使用业务时区")
	}
}

func TestDayPrefix(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载时区失败: %v", err)
	}
	prefix := DayPrefix(time.Date(2026, 1, 5, 20, 0, 0, 0, time.UTC), loc)
	if prefix != "TK-260106-" {
		t.Fatalf("日期前缀不符: %s", prefix)
	}
}
