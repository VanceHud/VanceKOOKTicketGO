package store

import (
	"strings"
	"testing"
	"time"
)

// TestUpdateFieldsStoresTimesInUTC 是回归测试：
//
// SQLite 没有原生时间类型，时间以文本保存并按字典序比较。
// 早先 UpdateFields 会把调用方传入的本地时间（如 +08:00）原样写入，
// 与 UTC 边界比较时会整体偏移一个时区——「今日/昨日」这类统计因此算错，
// 而且同一列里会同时出现 +08:00 与 +00:00 两种写法。
func TestUpdateFieldsStoresTimesInUTC(t *testing.T) {
	st := newTestStore(t)
	loc := testLocation(t)

	ticket := &Ticket{UserID: "9001", UserName: "测试用户", Status: TicketOpen}
	if err := st.Tickets.CreateWithNo(ticket, Now(), loc); err != nil {
		t.Fatalf("创建工单失败: %v", err)
	}

	local := time.Date(2026, 3, 8, 22, 0, 0, 0, loc) // 本地时间 22:00，等价 UTC 14:00
	closedAt := local.Add(time.Hour)
	if err := st.Tickets.UpdateFields(ticket.No, map[string]any{
		"started_at": local,
		"closed_at":  &closedAt,
	}); err != nil {
		t.Fatalf("更新工单失败: %v", err)
	}

	var raw struct {
		StartedAt string
		ClosedAt  string
	}
	if err := st.DB().Raw(
		"select cast(started_at as text) as started_at, cast(closed_at as text) as closed_at from tickets where no = ?",
		ticket.No,
	).Scan(&raw).Error; err != nil {
		t.Fatalf("读取原始时间失败: %v", err)
	}
	if !strings.Contains(raw.StartedAt, "+00:00") || !strings.Contains(raw.ClosedAt, "+00:00") {
		t.Fatalf("时间字段应以 UTC 存储，实际 started_at=%q closed_at=%q", raw.StartedAt, raw.ClosedAt)
	}

	// 边界查询必须按真实时刻比较：本地 22:00 属于当天，不应被算成 UTC 22:00。
	utc := local.UTC()
	before := utc.Add(-time.Second)
	after := utc.Add(time.Second)

	var matched int64
	if err := st.DB().Model(&Ticket{}).Where("started_at >= ? AND started_at < ?", before, after).Count(&matched).Error; err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if matched != 1 {
		t.Fatalf("按 UTC 时刻比较应命中 1 条，实际 %d", matched)
	}

	// 更早一小时之前开单的查询不应命中
	matched = 0
	if err := st.DB().Model(&Ticket{}).Where("started_at < ?", before).Count(&matched).Error; err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if matched != 0 {
		t.Fatalf("边界以下的查询不应命中，实际 %d", matched)
	}
}
