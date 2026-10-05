// Package eventbus 提供进程内的发布/订阅，用于把业务事件推送给 WebUI 的 SSE 连接。
//
// 设计取舍：慢消费者不会被阻塞，而是丢弃事件——实时推送只是体验优化，
// 前端在断线或丢事件后会重新拉取列表，因此不允许推送影响主流程。
package eventbus

import (
	"sync"
	"time"
)

// 事件类型。
const (
	// EventTicketCreated 表示新工单创建。
	EventTicketCreated = "ticket.created"
	// EventTicketUpdated 表示工单状态变化（锁定、重开、关闭等）。
	EventTicketUpdated = "ticket.updated"
	// EventTicketMessage 表示工单频道有新消息。
	EventTicketMessage = "ticket.message"
	// EventTicketNote 表示新增备注。
	EventTicketNote = "ticket.note"
	// EventStatsInvalidated 表示统计数据需要刷新。
	EventStatsInvalidated = "stats.invalidated"
	// EventBotStatus 表示机器人连接状态变化。
	EventBotStatus = "bot.status"
)

// Event 是推送给前端的实时事件。
type Event struct {
	Type     string    `json:"type"`
	TicketNo string    `json:"ticketNo,omitempty"`
	Data     any       `json:"data,omitempty"`
	At       time.Time `json:"at"`
}

// Bus 是进程内事件总线。
type Bus struct {
	mu     sync.RWMutex
	nextID int
	subs   map[int]chan Event
	owners map[int]string
	counts map[string]int
}

// New 创建事件总线。
func New() *Bus {
	return &Bus{subs: make(map[int]chan Event), owners: make(map[int]string), counts: make(map[string]int)}
}

// Subscribe 注册订阅者，返回订阅 ID 与只读通道。
// buffer 决定单订阅者允许积压的事件数，超出后新事件被丢弃。
func (b *Bus) Subscribe(buffer int) (int, <-chan Event) {
	id, ch, _ := b.SubscribeLimited("", 0, buffer)
	return id, ch
}

// SubscribeLimited 在同一把锁内检查并登记单账号订阅上限，避免并发连接绕过。
func (b *Bus) SubscribeLimited(owner string, limit, buffer int) (int, <-chan Event, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if limit > 0 && b.counts[owner] >= limit {
		return 0, nil, false
	}
	if buffer <= 0 {
		buffer = 16
	}
	ch := make(chan Event, buffer)
	b.nextID++
	id := b.nextID
	b.subs[id] = ch
	b.owners[id] = owner
	b.counts[owner]++
	return id, ch, true
}

// Unsubscribe 注销订阅者并关闭其通道。
func (b *Bus) Unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch, ok := b.subs[id]
	if !ok {
		return
	}
	delete(b.subs, id)
	owner := b.owners[id]
	delete(b.owners, id)
	b.counts[owner]--
	if b.counts[owner] == 0 {
		delete(b.counts, owner)
	}
	close(ch)
}

// Publish 广播事件；订阅者通道满时丢弃该事件。
func (b *Bus) Publish(event Event) {
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- event:
		default:
			// 订阅者过慢：丢弃事件，避免拖慢业务路径。
		}
	}
}

// Subscribers 返回当前订阅者数量，用于健康检查与界面展示。
func (b *Bus) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}
