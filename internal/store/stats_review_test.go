package store

import (
	"testing"
	"time"
)

func TestStatisticsDoNotSilentlyTruncateLargeRanges(t *testing.T) {
	st := newTestStore(t)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	const count = 50001
	if err := st.DB().Exec(`WITH RECURSIVE review_rows(n) AS (
		VALUES(1) UNION ALL SELECT n + 1 FROM review_rows WHERE n < ?
	) INSERT INTO tickets (no, user_id, status, started_at)
	SELECT printf('review-%d', n), 'user', 'open', ? FROM review_rows`, count, now).Error; err != nil {
		t.Fatal(err)
	}
	ov, err := st.Tickets.Overview(now, time.UTC, 7)
	if err != nil {
		t.Fatal(err)
	}
	var opened int64
	for _, point := range ov.Trend {
		opened += point.Opened
	}
	if ov.Total != count || opened != count {
		t.Fatalf("大区间仪表盘漏算: total=%d trend=%d", ov.Total, opened)
	}
	analytics, err := st.Tickets.Analytics(now, time.UTC, 7)
	if err != nil {
		t.Fatal(err)
	}
	if analytics.Total != count || len(analytics.Sources) != 1 || analytics.Sources[0].Opened != count {
		t.Fatalf("大区间统计漏算: %+v", analytics)
	}
}

func TestStatisticsSeparateOpenedCohortFromResponseAndClosureEvents(t *testing.T) {
	st := newTestStore(t)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -10)
	started := now.Add(-time.Hour)
	tickets := []Ticket{
		{No: "current-open", UserID: "u1", Status: TicketOpen, StartedAt: started, SourceChannelID: "source", MessageCount: 10},
		{No: "current-closed", UserID: "u2", Status: TicketClosed, StartedAt: started, ClosedAt: &now, SourceChannelID: "source", MessageCount: 20},
		{No: "old-closed-one", UserID: "u3", Status: TicketClosed, StartedAt: old, ClosedAt: &now, SourceChannelID: "source", MessageCount: 100},
		{No: "old-closed-two", UserID: "u4", Status: TicketClosed, StartedAt: old, ClosedAt: &now, SourceChannelID: "source", MessageCount: 100},
		{No: "old-first-reply", UserID: "u5", Status: TicketOpen, StartedAt: old, FirstReplyAt: &now, SourceChannelID: "source", MessageCount: 100},
	}
	if err := st.DB().Create(&tickets).Error; err != nil {
		t.Fatal(err)
	}
	analytics, err := st.Tickets.Analytics(now, time.UTC, 7)
	if err != nil {
		t.Fatal(err)
	}
	if analytics.Total != 2 || analytics.Closed != 1 || analytics.ClosedRate != 0.5 || analytics.AvgMessagesPerTicket != 15 {
		t.Fatalf("开单口径混入历史工单: %+v", analytics)
	}
	if len(analytics.Sources) != 1 || analytics.Sources[0].Opened != 2 || analytics.Sources[0].Closed != 1 || analytics.Sources[0].ClosedRate != 0.5 {
		t.Fatalf("来源关闭率分子分母不一致: %+v", analytics.Sources)
	}
	if analytics.Resolution.Count != 3 || analytics.FirstReply.Count != 1 || analytics.FirstReply.AvgSeconds != now.Sub(old).Seconds() {
		t.Fatalf("事件统计漏掉历史开单: %+v", analytics)
	}
	ov, err := st.Tickets.Overview(now, time.UTC, 7)
	if err != nil {
		t.Fatal(err)
	}
	if ov.AvgFirstReplySeconds != now.Sub(old).Seconds() {
		t.Fatalf("历史开单首次响应漏算: %v", ov.AvgFirstReplySeconds)
	}
}

func TestDurationPercentilesUseNearestRank(t *testing.T) {
	stats := summarizeDurations([]float64{6, 5, 4, 3, 2, 1})
	if stats.Count != 6 || stats.AvgSeconds != 3.5 || stats.P50Seconds != 3 || stats.P90Seconds != 6 {
		t.Fatalf("分位数未使用向上取整的秩: %+v", stats)
	}
}
