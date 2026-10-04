package store

import (
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentWritesDoNotFail 是回归测试：
//
// 机器人现在会并发处理多个工单流程（开单权限下发 / 消息归档 / 关闭），
// SQLite 在 WAL 下的默认 deferred 事务遇到并发写入会直接报
// SQLITE_BUSY_SNAPSHOT（database is locked 517），busy_timeout 对它无效。
// 连接串里的 _txlock=immediate 让写事务在 BEGIN 时就取写锁，
// 竞争退化为可重试的 busy 等待。这里用并发写入把该行为固定下来。
func TestConcurrentWritesDoNotFail(t *testing.T) {
	st := newTestStore(t)

	const (
		workers = 8
		rounds  = 12
	)

	// 先建好工单，避免把建单本身的并发写入混进断言。
	tickets := make([]*Ticket, workers)
	for i := range tickets {
		ticket := &Ticket{
			UserID:   fmt.Sprintf("9%016d", i),
			UserName: fmt.Sprintf("并发用户%d", i),
			Status:   TicketOpen,
		}
		if err := st.Tickets.CreateWithNo(ticket, Now(), testLocation(t)); err != nil {
			t.Fatalf("创建工单失败: %v", err)
		}
		tickets[i] = ticket
	}

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		errors []error
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			ticket := tickets[w]
			for r := 0; r < rounds; r++ {
				// 每条消息都是一次「读工单 → 插消息 → 更新工单」事务，并发时最容易撞锁。
				err := st.Tickets.AddMessage(&TicketMessage{
					TicketNo:  ticket.No,
					MsgID:     fmt.Sprintf("msg-%d-%d", w, r),
					UserID:    ticket.UserID,
					UserName:  ticket.UserName,
					Content:   fmt.Sprintf("并发消息 %d", r),
					MsgType:   MsgTypeText,
					CreatedAt: Now(),
				})
				if err != nil {
					mu.Lock()
					errors = append(errors, err)
					mu.Unlock()
					return
				}
				if err := st.Audit.Write(&AuditLog{
					Actor:     ticket.UserName,
					ActorType: ActorTypeBot,
					Action:    "concurrent.test",
					Target:    ticket.No,
					Detail:    fmt.Sprintf("第 %d 轮", r),
					CreatedAt: Now(),
				}); err != nil {
					mu.Lock()
					errors = append(errors, err)
					mu.Unlock()
					return
				}
			}
		}(w)
	}
	wg.Wait()

	if len(errors) > 0 {
		t.Fatalf("并发写入失败 %d 次，首个错误: %v", len(errors), errors[0])
	}

	for _, ticket := range tickets {
		updated, err := st.Tickets.ByNo(ticket.No)
		if err != nil {
			t.Fatalf("读取工单失败: %v", err)
		}
		if updated.MessageCount != rounds {
			t.Fatalf("工单 %s 的消息数应为 %d，实际 %d", ticket.No, rounds, updated.MessageCount)
		}
		messages, err := st.Tickets.Messages(ticket.No, 100, 0)
		if err != nil {
			t.Fatalf("读取消息失败: %v", err)
		}
		if len(messages) != rounds {
			t.Fatalf("工单 %s 应归档 %d 条消息，实际 %d", ticket.No, rounds, len(messages))
		}
	}
}
