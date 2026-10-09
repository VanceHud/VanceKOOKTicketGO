package bot

import (
	"context"
	"log/slog"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook"
)

// 网关事件的分片处理参数。
//
// 背景：开单/关闭需要串行调用十几个 KOOK 接口，耗时可达数秒。
// 若在网关读取协程里同步处理，这段时间读不到任何帧（连心跳 PONG 都读不到），
// 心跳看门狗会判定断线并重连续传，把同一次按钮点击又投递一次——
// 表现为「按钮响应慢、还可能收到重复提示」。
//
// 因此读取协程只负责收事件，处理交给下面的分片协程：
// 同一频道（私聊按用户）的事件仍然严格保序，不同频道之间可以并行。
//
// 代价：网关在事件入队后就认为它「已处理」（会写入 sn 供断线续传），
// 若进程在流程执行到一半时崩溃，这条事件不会被重新投递；
// 未完成的工单会停在 pending/进行中状态，在 WebUI 里可见且可人工处理。
// 这比“每条长流程都把心跳 PONG 堵住 → 断线重连 → 同一次点击被重放”要合算得多。
//
// 拥塞语义：队列满时 enqueue 会拒收并返回 false，网关不推进 sn 并主动重连，
// resume 时平台会重新投递这条事件，后续事件不会越过它执行。
const (
	eventShards    = 4
	eventQueueSize = 128
	// enqueueWait 是队列满时的短暂等待上限，用于吸收瞬时突发。
	// 必须远小于网关的 PongTimeout（默认 6s）：enqueue 由网关读循环同步调用，
	// 等待期间读不到 PONG 帧，等待过长会触发心跳超时误判断线。
	enqueueWait = 250 * time.Millisecond
)

// eventDispatcher 按 key 分片、串行处理事件。
type eventDispatcher struct {
	queues []chan kook.Event
	handle func(ctx context.Context, event kook.Event)
	keyFor func(event kook.Event) string
	log    *slog.Logger
}

// newEventDispatcher 创建事件分发器。keyFor 为 nil 时使用 eventKey。
func newEventDispatcher(shards, queueSize int, handle func(context.Context, kook.Event), keyFor func(kook.Event) string, log *slog.Logger) *eventDispatcher {
	if shards <= 0 {
		shards = eventShards
	}
	if queueSize <= 0 {
		queueSize = eventQueueSize
	}
	if keyFor == nil {
		keyFor = eventKey
	}
	if log == nil {
		log = slog.Default()
	}
	dispatcher := &eventDispatcher{
		queues: make([]chan kook.Event, shards),
		handle: handle,
		keyFor: keyFor,
		log:    log,
	}
	for i := range dispatcher.queues {
		dispatcher.queues[i] = make(chan kook.Event, queueSize)
	}
	return dispatcher
}

// start 启动分片处理协程，直到 ctx 结束。
func (d *eventDispatcher) start(ctx context.Context) {
	for i := range d.queues {
		go d.consume(ctx, d.queues[i])
	}
}

// consume 串行处理一个分片里的事件。
//
// 单条事件的处理 panic（畸形卡片 JSON、平台数据结构变化等）只终止本条事件，
// 不允许杀死分片协程：分片一旦死亡，它的队列会填满，之后该分片的所有频道
// 都会持续丢事件。
func (d *eventDispatcher) consume(ctx context.Context, queue <-chan kook.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-queue:
			d.handleSafely(ctx, event)
		}
	}
}

// handleSafely 执行单条事件的处理，兜底 recover。
func (d *eventDispatcher) handleSafely(ctx context.Context, event kook.Event) {
	defer func() {
		if r := recover(); r != nil {
			d.log.Error("处理网关事件发生 panic，已跳过该事件",
				"panic", r, "type", event.Type, "channel_id", event.TargetID)
		}
	}()
	d.handle(ctx, event)
}

// shardFor 返回分片键应落入的分片号。
//
// 内联 FNV-1a 32 位实现（而不是 fnv.New32a）：事件分发在每条消息的路径上，
// 为一次哈希分配 hasher 是纯热路径开销。
func (d *eventDispatcher) shardFor(key string) int {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	hash := uint32(offset32)
	for i := 0; i < len(key); i++ {
		hash ^= uint32(key[i])
		hash *= prime32
	}
	return int(hash % uint32(len(d.queues)))
}

// enqueue 把事件放入分片队列，返回是否受理。
//
// 由网关读循环同步调用，等待上限 enqueueWait（远小于 PongTimeout），
// 不会长时间阻塞读取协程；超时后拒收并返回 false，
// 网关将不推进 sn，并主动重连续传这条事件。
func (d *eventDispatcher) enqueue(ctx context.Context, event kook.Event) bool {
	key := d.keyFor(event)
	queue := d.queues[d.shardFor(key)]

	select {
	case queue <- event:
		return true
	default:
	}

	// 队列已满：同一个频道短时间内堆了大量事件（例如刷屏）。
	// 短暂等待吸收突发，等不到就拒收（由网关保留 sn 并主动重连续传）。
	d.log.Warn("事件处理队列已满，短暂等待", "type", event.Type, "key", key)
	select {
	case queue <- event:
		return true
	case <-ctx.Done():
		return false
	case <-time.After(enqueueWait):
		d.log.Error("事件处理队列持续拥塞，已拒收事件", "type", event.Type, "key", key)
		return false
	}
}

// eventKey 是事件的分片键：同一频道的消息、按钮点击、表情回应必须保序。
func eventKey(event kook.Event) string {
	if channelID := firstNonEmpty(event.Extra.Body.TargetID, event.TargetID); channelID != "" {
		return "channel:" + channelID
	}
	if userID := firstNonEmpty(event.Extra.Body.UserID, event.AuthorID); userID != "" {
		return "user:" + userID
	}
	return "global"
}
