package llm

import (
	"context"
	"sync"
	"time"
)

// tokenBucket 是客户端侧的极简令牌桶限速器。
//
// 与 kook 包的分桶限速不同：LLM 服务商不提供按桶的额度响应头，
// 这里只需要一个总闸门，防止自动应答把服务商配额与账单打爆。
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64 // 每秒补充的令牌数
	burst  float64 // 桶容量
	tokens float64
	last   time.Time
}

func newTokenBucket(rate float64, burst int) *tokenBucket {
	if burst <= 0 {
		burst = 1
	}
	return &tokenBucket{
		rate:   rate,
		burst:  float64(burst),
		tokens: float64(burst),
		last:   time.Now(),
	}
}

// acquire 阻塞直到取得一个令牌或 ctx 结束。
//
// 等待按小片轮询而不是精确唤醒：LLM 调用频率很低（每秒个位数），
// 轮询的误差可忽略，换来的是能及时响应 ctx 取消。
func (b *tokenBucket) acquire(ctx context.Context) error {
	const slice = 100 * time.Millisecond
	for {
		b.mu.Lock()
		now := time.Now()
		b.refillLocked(now)
		if b.tokens >= 1 {
			b.tokens--
			b.mu.Unlock()
			return nil
		}
		need := time.Duration((1 - b.tokens) / b.rate * float64(time.Second))
		b.mu.Unlock()

		if need > slice {
			need = slice
		}
		if need < 5*time.Millisecond {
			need = 5 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(need):
		}
	}
}

// refillLocked 按时间补充令牌（调用方持有锁）。
func (b *tokenBucket) refillLocked(now time.Time) {
	elapsed := now.Sub(b.last).Seconds()
	if elapsed <= 0 {
		return
	}
	if b.tokens < b.burst {
		b.tokens += elapsed * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
	}
	b.last = now
}
