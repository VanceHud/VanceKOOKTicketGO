package eventbus

import (
	"sync"
	"testing"
)

func TestConcurrentSubscriptionsRespectAccountLimitAndRelease(t *testing.T) {
	bus := New()
	var wg sync.WaitGroup
	ids := make(chan int, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if id, _, ok := bus.SubscribeLimited("user", 5, 1); ok {
				ids <- id
			}
		}()
	}
	wg.Wait()
	close(ids)
	if bus.Subscribers() != 5 {
		t.Fatalf("连接上限未生效: %d", bus.Subscribers())
	}
	for id := range ids {
		bus.Unsubscribe(id)
		bus.Unsubscribe(id)
	}
	if _, _, ok := bus.SubscribeLimited("user", 5, 1); !ok {
		t.Fatal("断线后额度未释放")
	}
	if _, _, ok := bus.SubscribeLimited("another", 5, 1); !ok {
		t.Fatal("不同账号不能共享限额")
	}
}

// TestCloseAllReleasesSubscribers 验证优雅退出时关闭全部订阅通道：
// SSE handler 依赖通道关闭立即返回，否则 http.Server.Shutdown 会等满超时。
func TestCloseAllReleasesSubscribers(t *testing.T) {
	bus := New()
	id, ch, ok := bus.SubscribeLimited("user", 5, 1)
	if !ok {
		t.Fatal("订阅失败")
	}
	bus.Publish(Event{Type: EventTicketCreated})
	if _, ok := <-ch; !ok {
		t.Fatal("关闭前应能收到事件")
	}

	bus.CloseAll()
	if _, ok := <-ch; ok {
		t.Fatal("CloseAll 后订阅通道应已关闭")
	}
	if bus.Subscribers() != 0 {
		t.Fatalf("CloseAll 后不应残留订阅者: %d", bus.Subscribers())
	}
	// 重复退订（SSE handler 的 defer）不能 panic。
	bus.Unsubscribe(id)
	// 新订阅仍可正常工作（关闭后再有连接不应受影响）。
	if _, ch, ok := bus.SubscribeLimited("user", 5, 1); !ok {
		t.Fatal("CloseAll 后应能继续订阅")
	} else {
		bus.Publish(Event{Type: EventTicketUpdated})
		if _, ok := <-ch; !ok {
			t.Fatal("新订阅者应能收到事件")
		}
	}
}
