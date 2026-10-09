package kook_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook/kooktest"
)

// TestGatewayResumesRejectedEventsInOrder 模拟一次拥塞：sn=1 已受理，
// sn=2 首次被拒收，sn=3 已在同一连接上发送。网关必须从 sn=1 续传，
// 在受理 sn=2 前不能处理 sn=3，且恢复后仍可推进持久化位点。
func TestGatewayResumesRejectedEventsInOrder(t *testing.T) {
	const sessionID = "sess-backpressure"
	platform := kooktest.New()
	t.Cleanup(platform.Close)
	platform.GatewayEvents = []kook.Event{
		{Type: kook.EventTypeText, TargetID: "chan-1", Content: "1"},
		{Type: kook.EventTypeText, TargetID: "chan-1", Content: "2"},
		{Type: kook.EventTypeText, TargetID: "chan-1", Content: "3"},
	}
	store := &memorySessionStore{session: sessionID}
	accepted := make(chan string, 4)
	observed := make(chan string, 8)
	var rejected atomic.Bool
	startGateway(t, platform, store, func(options *kook.GatewayOptions) {
		options.OnEvent = func(_ context.Context, event kook.Event) bool {
			observed <- event.Content
			if event.Content == "2" && rejected.CompareAndSwap(false, true) {
				return false
			}
			accepted <- event.Content
			return true
		}
	})
	for _, want := range []string{"1", "2", "3"} {
		select {
		case got := <-accepted:
			if got != want {
				t.Fatalf("事件受理顺序错误：得到 %q，期望 %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("被拒收事件未恢复，等待 sn=%s 超时", want)
		}
	}
	for _, want := range []string{"1", "2", "2", "3"} {
		select {
		case got := <-observed:
			if got != want {
				t.Fatalf("事件投递顺序错误：得到 %q，期望 %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("未收到预期投递 sn=%s", want)
		}
	}
	connects := platform.WSConnects()
	if len(connects) != 2 {
		t.Fatalf("应主动重连一次，实际次数=%d", len(connects))
	}
	query := connects[1]
	if query.Get("resume") != "1" || query.Get("session_id") != sessionID || query.Get("sn") != "1" {
		t.Fatalf("应从最后受理的 sn=1 续传原会话，实际 query=%v", query)
	}
	waitFor(t, "恢复后的续传位点落库", func() bool {
		session, sn := store.snapshot()
		return session == sessionID && sn == 3
	})
}
