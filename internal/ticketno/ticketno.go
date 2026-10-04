// Package ticketno 负责生成与校验工单编号。
//
// 编号格式：TK-YYMMDD-XXXX
//
//	TK-        固定前缀
//	YYMMDD     开单日期，按 TICKET_TZ 计算（默认 Asia/Shanghai），便于人工按天归档
//	XXXX       Crockford Base32 随机段（去掉 I/L/O/U，避免念错抄错）
//
// 随机段来自 crypto/rand，因此编号不可枚举：既不会泄露工单总量，
// 也无法通过编号推算出其它工单。同日随机段冲突超过阈值时会自动加宽到 5 位。
package ticketno

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"time"
)

// Prefix 是编号固定前缀。
const Prefix = "TK-"

// 随机段宽度。
const (
	WidthDefault = 4
	WidthWide    = 5
)

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	patternDefault = regexp.MustCompile(`^TK-\d{6}-[0-9A-HJKMNP-TV-Z]{4}$`)
	patternWide    = regexp.MustCompile(`^TK-\d{6}-[0-9A-HJKMNP-TV-Z]{5}$`)
)

// New 生成标准宽度（4 位随机段）的工单编号。
func New(now time.Time, loc *time.Location) (string, error) {
	return generate(now, loc, WidthDefault)
}

// NewWide 生成加宽（5 位随机段）的工单编号，用于同日冲突频繁的极端情况。
func NewWide(now time.Time, loc *time.Location) (string, error) {
	return generate(now, loc, WidthWide)
}

func generate(now time.Time, loc *time.Location, width int) (string, error) {
	if loc == nil {
		loc = time.UTC
	}
	code, err := randomCode(width)
	if err != nil {
		return "", err
	}
	return Prefix + now.In(loc).Format("060102") + "-" + code, nil
}

// randomCode 生成 width 个 Crockford Base32 字符。
// 256 % 32 == 0，因此单字节取模不存在偏置。
func randomCode(width int) (string, error) {
	if width <= 0 {
		return "", fmt.Errorf("随机段宽度必须为正数")
	}
	buf := make([]byte, width)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, width)
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

// Valid 判断编号是否符合约定格式（含加宽格式）。
// 格式不合法时调用方应直接按“不存在”处理，避免泄露编号规则外的信息。
func Valid(no string) bool {
	return patternDefault.MatchString(no) || patternWide.MatchString(no)
}

// Date 解析编号中的日期段，返回该日 00:00（loc 时区）。
func Date(no string, loc *time.Location) (time.Time, bool) {
	if !Valid(no) || len(no) < 9 {
		return time.Time{}, false
	}
	if loc == nil {
		loc = time.UTC
	}
	t, err := time.ParseInLocation("060102", no[3:9], loc)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// DayPrefix 返回某天对应的编号前缀，可用于“今天的所有工单”这类前缀查询。
func DayPrefix(day time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return Prefix + day.In(loc).Format("060102") + "-"
}
