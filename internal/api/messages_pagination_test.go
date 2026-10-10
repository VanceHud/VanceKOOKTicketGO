package api

import (
	"net/http"
	"testing"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// TestMessagePaginationTotal 验证越过末尾的空页返回真实总数，
// 并覆盖空工单、完整页、末尾不足一页及 tail 模式的原有行为。
func TestMessagePaginationTotal(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "reader", "ReaderPass123", store.RoleReadonly, false)
	cookie, csrf := env.login(t, "reader", "ReaderPass123")
	ticket := env.seedTicket(t, store.TicketOpen)
	check := func(t *testing.T, query string, total, count int) {
		t.Helper()
		res := env.do(t, http.MethodGet, "/api/v1/tickets/"+ticket.No+"/messages?"+query, nil, withCookie(cookie), withCSRF(csrf))
		if res.status != http.StatusOK {
			t.Fatalf("查询失败：status=%d body=%s", res.status, res.raw)
		}
		items, ok := res.body["items"].([]any)
		if !ok || len(items) != count || res.body["total"] != float64(total) {
			t.Fatalf("期望 total=%d、items=%d，实际 body=%s", total, count, res.raw)
		}
	}
	t.Run("空工单第一页", func(t *testing.T) { check(t, "limit=10", 0, 0) })
	t.Run("空工单越界", func(t *testing.T) { check(t, "limit=10&offset=50", 0, 0) })
	for i := 0; i < 3; i++ {
		if err := env.store.Tickets.AddMessage(&store.TicketMessage{
			TicketNo: ticket.No, UserID: "u1", Content: "消息", MsgType: store.MsgTypeText,
		}); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name  string
		query string
		count int
	}{
		{"第一页不足一页", "limit=10", 3},
		{"完整页", "limit=2", 2},
		{"末尾不足一页", "limit=10&offset=2", 1},
		{"恰好到末尾", "limit=10&offset=3", 0},
		{"越过末尾", "limit=10&offset=50", 0},
		{"尾部不足一页", "limit=10&tail=1&offset=50", 3},
		{"尾部截断", "limit=2&tail=1", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { check(t, tc.query, 3, tc.count) })
	}
}
