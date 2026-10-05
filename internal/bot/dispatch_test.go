package bot

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook"
)

// TestEventDispatcherKeepsSameChannelOrdered 验证同一频道的事件严格按顺序处理。
func TestEventDispatcherKeepsSameChannelOrdered(t *testing.T) {
	var (
		mu    sync.Mutex
		order []string
		done  = make(chan struct{})
	)
	dispatcher := newEventDispatcher(4, 64, func(_ context.Context, event kook.Event) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, event.Content)
		if len(order) == 20 {
			close(done)
		}
	}, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dispatcher.start(ctx)

	for i := 0; i < 20; i++ {
		dispatcher.enqueue(ctx, kook.Event{TargetID: "chan-1", Content: fmt.Sprint(i)})
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("事件未被处理完")
	}

	mu.Lock()
	defer mu.Unlock()
	for i, got := range order {
		if want := fmt.Sprint(i); got != want {
			t.Fatalf("同一频道的事件必须保序，第 %d 个是 %q，期望 %q", i, got, want)
		}
	}
}

// TestEventDispatcherRunsDifferentChannelsInParallel 验证一个频道的慢流程
// 不会阻塞其它频道的事件处理（旧实现里所有事件都在网关读取协程里排队）。
func TestEventDispatcherRunsDifferentChannelsInParallel(t *testing.T) {
	blocked := make(chan struct{})
	started := make(chan string, 4)
	dispatcher := newEventDispatcher(4, 64, func(_ context.Context, event kook.Event) {
		if event.TargetID == "slow-1" {
			started <- event.TargetID
			<-blocked
			return
		}
		started <- event.TargetID
	}, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dispatcher.start(ctx)

	// 找一个与 slow-1 不同分片的频道，保证两者应当并行。
	var other string
	for i := 0; i < 32; i++ {
		candidate := fmt.Sprintf("fast-%d", i)
		if dispatcher.shardFor(kook.Event{TargetID: candidate}) != dispatcher.shardFor(kook.Event{TargetID: "slow-1"}) {
			other = candidate
			break
		}
	}
	if other == "" {
		t.Fatal("测试环境未找到不同分片的频道")
	}

	dispatcher.enqueue(ctx, kook.Event{TargetID: "slow-1"})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("慢频道的事件未被处理")
	}

	dispatcher.enqueue(ctx, kook.Event{TargetID: other})
	select {
	case got := <-started:
		if got != other {
			t.Fatalf("期望先处理 %q，实际 %q", other, got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("另一频道的事件被慢流程阻塞了")
	}
	close(blocked)
}

// TestEventKeyUsesChannelThenUser 验证分片键的选择：优先频道，其次用户。
func TestEventKeyUsesChannelThenUser(t *testing.T) {
	cases := []struct {
		name  string
		event kook.Event
		want  string
	}{
		{"频道消息", kook.Event{TargetID: "chan-1"}, "channel:chan-1"},
		{"按钮事件", kook.Event{Extra: kook.Extra{Body: kook.Body{TargetID: "chan-2"}}}, "channel:chan-2"},
		{"私聊", kook.Event{AuthorID: "user-1"}, "user:user-1"},
		{"无归属", kook.Event{}, "global"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := eventKey(tc.event); got != tc.want {
				t.Fatalf("分片键应为 %q，实际 %q", tc.want, got)
			}
		})
	}
}
