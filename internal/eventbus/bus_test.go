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
