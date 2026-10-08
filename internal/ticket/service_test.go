package ticket

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

func TestConcurrentPlatformReplacementIsSafe(t *testing.T) {
	service := NewService(nil, nil, nil, nil, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 1000; n++ {
				service.SetPlatform(NewNoopPlatform(nil))
				_, _ = service.platformOrErr()
				service.SetPlatform(nil)
			}
		}()
	}
	wg.Wait()
	service.SetPlatform(nil)
	if _, err := service.platformOrErr(); !errors.Is(err, ErrNoPlatform) {
		t.Fatalf("离线平台应报错: %v", err)
	}
}

// TestClearPlatformKeepsReplacement 是回归测试：
//
// 重启机器人时旧网关协程可能晚于新连接退出（Bot.Stop 只等 5 秒），
// 旧协程迟到调用 ClearPlatform(old) 不能把新 Bot 注入的实现清掉，
// 否则关单/锁单会一直报 ErrNoPlatform。
func TestClearPlatformKeepsReplacement(t *testing.T) {
	service := NewService(nil, nil, nil, nil, nil)
	oldImpl := NewNoopPlatform(nil)
	newImpl := NewNoopPlatform(nil)

	service.SetPlatform(oldImpl)
	service.SetPlatform(newImpl)

	// 迟到的旧清理：当前实现已是新实例，不应被清空。
	service.ClearPlatform(oldImpl)
	if got, err := service.platformOrErr(); err != nil || got != newImpl {
		t.Fatalf("旧实例的迟到清理不应影响新实现: got=%v err=%v", got, err)
	}

	// 新实例自己退出时才应真正清空。
	service.ClearPlatform(newImpl)
	if _, err := service.platformOrErr(); !errors.Is(err, ErrNoPlatform) {
		t.Fatalf("新实例清理后应回到离线状态: %v", err)
	}
}

// TestScanPendingRemovesStaleTickets 是回归测试：
//
// 进程在建频道途中崩溃会留下永久 pending 的工单，而“一人一单”把 pending
// 也算作未关闭，该用户此后无法再开单。ScanPending 必须回收超时残留、
// 且不能误伤正在创建中的新工单。
func TestScanPendingRemovesStaleTickets(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/ticket.db")
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	service := NewService(st, nil, NewNoopPlatform(nil), time.UTC, nil)

	stale := &store.Ticket{UserID: "u-stale", UserName: "滞留", Status: store.TicketPending}
	if err := st.Tickets.CreateWithNo(stale, store.Now().Add(-time.Hour), time.UTC); err != nil {
		t.Fatalf("创建滞留工单失败: %v", err)
	}
	fresh := &store.Ticket{UserID: "u-fresh", UserName: "新建", Status: store.TicketPending}
	if err := st.Tickets.CreateWithNo(fresh, store.Now(), time.UTC); err != nil {
		t.Fatalf("创建新工单失败: %v", err)
	}

	if err := service.ScanPending(context.Background()); err != nil {
		t.Fatalf("回收滞留占号失败: %v", err)
	}

	if _, err := st.Tickets.ByNo(stale.No); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("滞留的 pending 工单应被回收，got err=%v", err)
	}
	if _, err := st.Tickets.ByNo(fresh.No); err != nil {
		t.Fatalf("未超时的 pending 工单不应被回收: %v", err)
	}
	// 回收后该用户可以正常开新单（一人一单不再被残留阻塞）。
	if existing, err := st.Tickets.ActiveByUser("u-stale"); err == nil && existing != nil {
		t.Fatalf("回收后不应再有活跃工单: %+v", existing)
	}
}
