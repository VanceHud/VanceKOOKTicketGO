package bot

import (
	"context"
	"hash/fnv"
	"log/slog"
	"time"

	"vancekookticket/internal/kook"
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
// 代价：网关在事件入队后就认为它“已处理”（会写入 sn 供断线续传），
// 若进程在流程执行到一半时崩溃，这条事件不会被重新投递；
// 未完成的工单会停在 pending/进行中状态，在 WebUI 里可见且可人工处理。
// 这比“每条长流程都把心跳 PONG 堵住 → 断线重连 → 同一次点击被重放”要合算得多。
const (
	eventShards    = 4
	eventQueueSize = 128
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
func (d *eventDispatcher) consume(ctx context.Context, queue <-chan kook.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-queue:
			d.handle(ctx, event)
		}
	}
}

// shardFor 返回事件应落入的分片号。
func (d *eventDispatcher) shardFor(event kook.Event) int {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(d.keyFor(event)))
	return int(hasher.Sum32() % uint32(len(d.queues)))
}

// enqueue 只做入队，绝不长时间阻塞读取协程。
func (d *eventDispatcher) enqueue(ctx context.Context, event kook.Event) {
	queue := d.queues[d.shardFor(event)]

	select {
	case queue <- event:
		return
	default:
	}

	// 队列已满：同一个频道短时间内堆了大量事件（例如刷屏）。
	// 这里短暂等待，而不是直接丢事件；等不到再放弃并留下错误日志。
	d.log.Warn("事件处理队列已满，等待处理", "type", event.Type, "key", d.keyFor(event))
	select {
	case queue <- event:
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		d.log.Error("事件处理队列持续拥塞，已丢弃事件", "type", event.Type, "key", d.keyFor(event))
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
