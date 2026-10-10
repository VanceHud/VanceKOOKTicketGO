package kook

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// 平台与传输层相关错误。
var (
	// ErrTokenInvalid 表示 Token 无效或已被重置。
	ErrTokenInvalid = errors.New("KOOK Token 无效")
	// ErrPermissionDenied 表示机器人没有执行该操作的权限（例如角色位置过低）。
	ErrPermissionDenied = errors.New("机器人缺少所需权限")
	// ErrNotFound 表示目标资源不存在（频道已删除、消息已删除等）。
	ErrNotFound = errors.New("目标不存在")
	// ErrRateLimited 表示触发了平台限流，且重试后仍未成功。
	ErrRateLimited = errors.New("请求被 KOOK 限流")
)

// APIError 是 KOOK 返回的业务错误。
type APIError struct {
	// HTTPStatus 是 HTTP 状态码（429 表示限流）。
	HTTPStatus int
	// Code 是平台业务错误码（0 表示成功）。
	Code int
	// Message 是平台返回的错误描述。
	Message string
	// Endpoint 便于日志定位（不含敏感参数）。
	Endpoint string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("KOOK 接口 %s 返回错误: HTTP %d code=%d message=%s", e.Endpoint, e.HTTPStatus, e.Code, e.Message)
}

// Is 支持 errors.Is 语义，便于上层按类别判断。
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrTokenInvalid:
		// 40100/40101 为 Token 相关错误。
		return e.Code == 40100 || e.Code == 40101 || e.HTTPStatus == 401
	case ErrPermissionDenied:
		return e.HTTPStatus == 403 || e.Code == 40300
	case ErrNotFound:
		return e.HTTPStatus == 404 || e.Code == 40400
	case ErrRateLimited:
		return e.HTTPStatus == 429 || e.Code == 42900
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// 限流
// ---------------------------------------------------------------------------
//
// KOOK 的限流是**按桶（bucket）**计的：每个受控响应都会带回
//
//	X-Rate-Limit-Limit      该窗口允许的请求数
//	X-Rate-Limit-Remaining  该窗口还剩多少
//	X-Rate-Limit-Reset      距离窗口重置的秒数
//	X-Rate-Limit-Bucket     桶名（如 user/info）
//	X-Rate-Limit-Global     触犯了全局限制（仅 429 时出现）
//
// 因此这里做两层控制：
//
//  1. 分桶闸门：按平台回报的额度只对“那一个桶”排队。某个桶额度用完时，
//     其它接口照常调用，不会被拖慢。
//  2. 全局令牌桶：客户端侧的总闸门，速率可配置；被限流（429）后临时降速，
//     之后靠连续成功缓慢回升。**不做永久降速**。
//
// 反面教材（旧实现）：把每个响应里的 remaining/reset 都当作全局额度，
// 一旦某个桶剩余不足就把整个客户端停到窗口结束，并把全局速率永久改小
// （只降不升）。于是「点一下开单按钮要等好几秒」成为常态，且越跑越慢。
const (
	// defaultRatePerSecond 是全局令牌桶的默认速率（可由配置覆盖）。
	defaultRatePerSecond = 8
	// defaultBurst 是全局桶容量：允许一次性突发的请求数。
	defaultBurst = 4
	// rateCeilingMultiple 是自适应提速的上限（相对配置速率）。
	rateCeilingMultiple = 2
	// rateFloorMultiple 是自适应降速的下限（相对配置速率）。
	rateFloorMultiple = 4
	// successStreakForBoost 是连续成功多少次后允许提升速率。
	successStreakForBoost = 24
	// maxPenalty 是单次限流惩罚的上限。
	maxPenalty = 30 * time.Second
	// maxWaitSlice 是单次睡眠上限：便于及时响应 ctx 取消与新到的额度信息。
	maxWaitSlice = 2 * time.Second
	// unknownBucketPoll 是“尚无额度信息”的桶被占用时的轮询间隔。
	unknownBucketPoll = 50 * time.Millisecond
	// fallbackResetDelay 是平台响应缺 Reset 头但额度已耗尽时的保守重置等待：
	// 没有它，left 永远无法回补，该桶会陷入 50ms 轮询活锁。
	fallbackResetDelay = time.Second
)

// rateInfo 是一次响应里解析出的限流信息。
type rateInfo struct {
	// found 为真表示响应带了至少一个限流头。
	found bool
	// bucket 是平台给出的桶名（X-Rate-Limit-Bucket）。
	bucket string
	// limit 是窗口额度，<=0 表示响应里没有该字段。
	limit float64
	// remaining 是窗口剩余额度，<0 表示响应里没有该字段。
	remaining float64
	// reset 是距窗口重置的时间，<=0 表示响应里没有该字段。
	reset time.Duration
	// retryAfter 是 Retry-After 响应头（秒），429 时可能出现。
	retryAfter time.Duration
	// global 为真表示平台明确提示触发了全局限流。
	global bool
}

// routeBucket 是单个限流桶的状态。
type routeBucket struct {
	// limit 是平台给出的窗口额度（<=0 表示还没学到）。
	limit float64
	// left 是我们认为的窗口剩余额度。
	left float64
	// resetAt 是窗口重置时间（零值表示未知）。
	resetAt time.Time
	// inflight 是已放行但还没收到响应的请求数。
	inflight int
	// learned 为真表示已经从响应头学到过该桶的额度。
	learned bool
}

// refreshLocked 在窗口到期后按平台额度重新计数。
func (b *routeBucket) refreshLocked(now time.Time) {
	if !b.learned || b.limit <= 0 {
		return
	}
	if b.resetAt.IsZero() {
		// 从未学到 Reset 头（平台不回、代理剥离）：额度耗尽时按本地窗口兜底回补，
		// 否则 left 永远等不到重置，该桶会陷入 50ms 轮询活锁。
		if b.left < 1 {
			b.left = b.limit
		}
		return
	}
	if now.Before(b.resetAt) {
		return
	}
	b.left = b.limit
	b.resetAt = time.Time{}
}

// allowLocked 判断当前能否再放行一个请求。
func (b *routeBucket) allowLocked() bool {
	if !b.learned {
		// 还不知道额度：同一个桶内只放行一个请求，拿到响应头后再决定并发度。
		// 这样即使某个接口不返回限流头，也只是变慢，不会打爆平台。
		return b.inflight == 0
	}
	return b.left >= 1
}

// rateLimiter 是客户端侧限速器（全局令牌桶 + 平台分桶闸门）。
type rateLimiter struct {
	mu sync.Mutex

	// 全局令牌桶
	baseRate float64
	ceiling  float64
	floor    float64
	rate     float64
	burst    float64
	tokens   float64
	last     time.Time

	// successes 统计连续成功次数，用于在平台不反对时缓慢提速。
	successes int

	// penaltyUntil 是 429 / 全局限流后的临时停顿。
	penaltyUntil time.Time

	// buckets 按平台限流桶记录额度；endpoints 记录接口 → 桶名的映射。
	buckets   map[string]*routeBucket
	endpoints map[string]string

	now func() time.Time
}

func newRateLimiter(rate float64, burst int) *rateLimiter {
	if rate <= 0 {
		rate = defaultRatePerSecond
	}
	if burst <= 0 {
		burst = defaultBurst
	}
	return &rateLimiter{
		baseRate:  rate,
		ceiling:   rate * rateCeilingMultiple,
		floor:     math.Max(rate/rateFloorMultiple, 1),
		rate:      rate,
		burst:     float64(burst),
		tokens:    float64(burst),
		last:      time.Now(),
		buckets:   make(map[string]*routeBucket),
		endpoints: make(map[string]string),
		now:       time.Now,
	}
}

// keyFor 返回接口当前对应的桶键：已经学到真实桶名时按桶名共享限速，
// 否则退化为接口维度的临时桶。
func (l *rateLimiter) keyFor(endpoint string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if bucket, ok := l.endpoints[endpoint]; ok {
		return bucket
	}
	return "endpoint:" + endpoint
}

// acquire 阻塞直到取得一次调用许可（全局额度 + 该桶额度）。
func (l *rateLimiter) acquire(ctx context.Context, key string) error {
	for {
		l.mu.Lock()
		now := l.now()
		l.refillLocked(now)

		bucket := l.bucketLocked(key)
		bucket.refreshLocked(now)

		if l.tokens >= 1 && !now.Before(l.penaltyUntil) && bucket.allowLocked() {
			l.tokens--
			bucket.inflight++
			if bucket.learned {
				bucket.left--
			}
			l.mu.Unlock()
			return nil
		}

		waitFor := l.waitTimeLocked(now, bucket)
		l.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitFor):
		}
	}
}

// release 归还一次未被观测到的调用（请求在拿到响应前失败了）。
func (l *rateLimiter) release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	bucket := l.bucketLocked(key)
	if bucket.inflight > 0 {
		bucket.inflight--
	}
}

// observe 记录平台回报的额度。endpoint 用于学习“接口 → 桶名”的映射，
// usedKey 是 acquire 时使用的桶键（可能是临时键）。
func (l *rateLimiter) observe(endpoint, usedKey string, info rateInfo) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	used := l.bucketLocked(usedKey)
	if used.inflight > 0 {
		used.inflight--
	}

	target := used
	if info.bucket != "" && info.bucket != usedKey {
		// 平台换了个桶（同一个桶可能被多个接口共用）：后续请求直接按真实桶名限速。
		l.endpoints[endpoint] = info.bucket
		target = l.bucketLocked(info.bucket)
	}

	if info.limit > 0 {
		target.limit = info.limit
		target.learned = true
	} else if info.remaining >= 0 && target.limit <= 0 {
		// 没有 Limit 字段时，用剩余额度保守估算窗口容量，避免把桶当成“无限”。
		target.limit = info.remaining + 1
		target.learned = true
	}

	if info.remaining >= 0 {
		target.left = info.remaining
		if target.limit > 0 && target.left > target.limit {
			target.left = target.limit
		}
	}
	if info.reset > 0 {
		target.resetAt = now.Add(info.reset)
	} else if info.remaining >= 0 && info.remaining < 1 {
		// 额度已耗尽但没有 Reset 头：给一个保守的本地重置时间，
		// 不早于已知的重置时间（避免把平台明确给出的更长等待缩短）。
		fallback := now.Add(fallbackResetDelay)
		if target.resetAt.Before(fallback) {
			target.resetAt = fallback
		}
	}
}

// success 记录一次成功调用；长期未被限流时缓慢提速，让平台额度被充分利用。
func (l *rateLimiter) success() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.successes++
	if l.successes >= successStreakForBoost && l.rate < l.ceiling {
		l.rate = math.Min(l.rate+1, l.ceiling)
		l.successes = 0
	}
}

// penalize 在收到限流响应后临时降速；不做永久降速，之后会随连续成功回升。
func (l *rateLimiter) penalize(d time.Duration) {
	if d <= 0 {
		d = time.Second
	}
	if d > maxPenalty {
		d = maxPenalty
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if until := now.Add(d); until.After(l.penaltyUntil) {
		l.penaltyUntil = until
	}
	l.rate = math.Max(l.rate/2, l.floor)
	l.successes = 0
	// 清空令牌，强制排队等待
	l.tokens = 0
	l.last = now
}

// rate 返回当前全局速率，供日志与诊断使用。
func (l *rateLimiter) currentRate() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rate
}

// bucketLocked 取出（必要时创建）桶状态。
func (l *rateLimiter) bucketLocked(key string) *routeBucket {
	bucket, ok := l.buckets[key]
	if !ok {
		bucket = &routeBucket{}
		l.buckets[key] = bucket
	}
	return bucket
}

// refillLocked 按时间补充全局令牌。
func (l *rateLimiter) refillLocked(now time.Time) {
	elapsed := now.Sub(l.last).Seconds()
	if elapsed <= 0 {
		return
	}
	l.tokens = math.Min(l.burst, l.tokens+elapsed*l.rate)
	l.last = now
}

// waitTimeLocked 计算下一次检查前应睡眠多久（取各种约束里最短的等待）。
func (l *rateLimiter) waitTimeLocked(now time.Time, bucket *routeBucket) time.Duration {
	wait := maxWaitSlice

	if now.Before(l.penaltyUntil) {
		wait = minDuration(wait, l.penaltyUntil.Sub(now))
	}
	if l.tokens < 1 {
		need := time.Duration((1 - l.tokens) / l.rate * float64(time.Second))
		wait = minDuration(wait, need)
	}
	if bucket.learned && bucket.left < 1 {
		if !bucket.resetAt.IsZero() && bucket.resetAt.After(now) {
			wait = minDuration(wait, bucket.resetAt.Sub(now))
		} else {
			wait = minDuration(wait, unknownBucketPoll)
		}
	}
	if !bucket.learned && bucket.inflight > 0 {
		// 还没学到额度、且已有请求在途：等它回来（教学完成后并发度自会放开）。
		// 这里必须短轮询，否则每次串行都要白等一个 maxWaitSlice。
		wait = minDuration(wait, unknownBucketPoll)
	}
	if wait < 10*time.Millisecond {
		wait = 10 * time.Millisecond
	}
	return wait
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
