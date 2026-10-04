package keyedlock

import (
	"sync"
	"testing"
	"time"
)

// TestLockSerializesSameKey 验证同一个 key 上的调用严格串行。
func TestLockSerializesSameKey(t *testing.T) {
	var locks Locks

	var (
		mu        sync.Mutex
		active    int
		maxActive int
		wg        sync.WaitGroup
	)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := locks.Lock("ticket:1")
			defer unlock()

			mu.Lock()
			active++
			if active > maxActive {
				maxActive = active
			}
			mu.Unlock()

			time.Sleep(2 * time.Millisecond)

			mu.Lock()
			active--
			mu.Unlock()
		}()
	}
	wg.Wait()

	if maxActive != 1 {
		t.Fatalf("同一 key 应串行执行，实际同时有 %d 个", maxActive)
	}
}

// TestLockAllowsDifferentKeysInParallel 验证不同 key 互不阻塞：
// 这正是「两位管理员关不同工单」「两名用户同时开单」不再彼此等待的前提。
func TestLockAllowsDifferentKeysInParallel(t *testing.T) {
	var locks Locks

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	go func() {
		unlock := locks.Lock("ticket:A")
		defer unlock()
		started <- struct{}{}
		<-release
	}()
	<-started

	done := make(chan struct{})
	go func() {
		unlock := locks.Lock("ticket:B")
		defer unlock()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("不同 key 不应互相阻塞")
	}
	close(release)
}

// TestLockReclaimsUnusedKeys 验证释放后 key 会被回收，避免长跑进程里 map 无限增长。
func TestLockReclaimsUnusedKeys(t *testing.T) {
	var locks Locks

	unlock := locks.Lock("ticket:1")
	unlock()

	locks.mu.Lock()
	size := len(locks.locks)
	locks.mu.Unlock()

	if size != 0 {
		t.Fatalf("释放后应回收 key 状态，实际仍有 %d 项", size)
	}
}
