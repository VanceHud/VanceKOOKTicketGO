package store

import (
	"testing"
	"time"
)

// explainPlan 返回 EXPLAIN QUERY PLAN 各步骤的 Detail 文本。
func explainPlan(t *testing.T, st *Store, query string, args ...any) []string {
	t.Helper()
	var plans []struct{ Detail string }
	if err := st.DB().Raw("EXPLAIN QUERY PLAN "+query, args...).Scan(&plans).Error; err != nil {
		t.Fatalf("执行 EXPLAIN 失败: %v", err)
	}
	details := make([]string, 0, len(plans))
	for _, plan := range plans {
		details = append(details, plan.Detail)
	}
	return details
}

func planUsesIndex(details []string, index string) bool {
	for _, detail := range details {
		if contains(detail, "USING INDEX "+index) || contains(detail, "USING COVERING INDEX "+index) {
			return true
		}
	}
	return false
}

func planHasTempSort(details []string) bool {
	for _, detail := range details {
		if contains(detail, "TEMP B-TREE") {
			return true
		}
	}
	return false
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestTicketListFiltersUseCompositeIndexes 验证列表页最常用的两类过滤
// （单状态 + 时间倒序、类型 + 时间倒序）不再产生临时排序。
func TestTicketListFiltersUseCompositeIndexes(t *testing.T) {
	st := newTestStore(t)
	now := Now()
	for i := 0; i < 3; i++ {
		if err := st.Tickets.CreateWithNo(&Ticket{
			No: "TK-261009-000" + string(rune('A'+i)), UserID: "u1", Status: TicketOpen,
			StartedAt: now.Add(-time.Duration(i) * time.Hour),
		}, now, time.UTC); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name  string
		query string
		args  []any
		index string
	}{
		{
			name:  "单状态过滤",
			query: "SELECT id FROM tickets WHERE status = ? ORDER BY started_at DESC, id DESC LIMIT 20",
			args:  []any{TicketOpen},
			index: "idx_tickets_status_started",
		},
		{
			name:  "类型过滤",
			query: "SELECT id FROM tickets WHERE type_id = ? ORDER BY started_at DESC, id DESC LIMIT 20",
			args:  []any{1},
			index: "idx_tickets_type_started",
		},
	}
	for _, tc := range cases {
		details := explainPlan(t, st, tc.query, tc.args...)
		if planHasTempSort(details) {
			t.Errorf("%s 仍使用临时排序: %v", tc.name, details)
		}
		if !planUsesIndex(details, tc.index) {
			t.Errorf("%s 未走复合索引 %s: %v", tc.name, tc.index, details)
		}
	}
}

// TestStatsRangeQueriesAvoidFullScan 验证统计的区间扫描走索引而不是全表扫描。
func TestStatsRangeQueriesAvoidFullScan(t *testing.T) {
	st := newTestStore(t)
	cutoff := Now().Add(-24 * time.Hour)
	for _, column := range []string{"started_at", "closed_at", "first_reply_at"} {
		details := explainPlan(t, st, "SELECT id FROM tickets WHERE "+column+" >= ?", cutoff.UTC())
		for _, detail := range details {
			if contains(detail, "SCAN tickets") {
				t.Errorf("按 %s 的区间查询退化为全表扫描: %s", column, detail)
			}
		}
	}
}

// TestRedundantSingleColumnIndexesDropped 验证被复合索引前缀覆盖的单列索引
// 已从旧库中清理（迁移后不应存在）。
func TestRedundantSingleColumnIndexesDropped(t *testing.T) {
	st := newTestStore(t)
	for _, name := range []string{
		"idx_tickets_status",
		"idx_tickets_type_id",
		"idx_ticket_messages_ticket_no",
		"idx_ticket_notes_ticket_no",
		"idx_sessions_user_id",
		"idx_auth_codes_kook_user_id",
		"idx_emoji_grants_kook_user_id",
	} {
		var count int64
		if err := st.DB().Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", name).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("冗余索引 %s 未被清理", name)
		}
	}
}
