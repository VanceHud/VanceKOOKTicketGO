package kook

import (
	"context"
	"testing"
	"time"
)

func TestRateLimiterAllowsBurstThenThrottles(t *testing.T) {
	// 注意：这里使用真实时钟。若把 now 冻结，令牌永远不会补充，
	// acquire() 将无限重试（该写法曾在本地把测试卡死，故加此注释警示）。
	limiter := newRateLimiter(10, 2)

	ctx := context.Background()

	// 桶容量为 2，前两次应立即通过（每次调用结束都归还许可）。
	for i := 0; i < 2; i++ {
		start := time.Now()
		if err := limiter.acquire(ctx, "message/create"); err != nil {
			t.Fatalf("第 %d 次请求不应被阻塞: %v", i+1, err)
		}
		limiter.release("message/create")
		if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
			t.Fatalf("突发额度内的第 %d 次请求不应被限速，实际 %v", i+1, elapsed)
		}
	}

	// 第三次需要等待补桶：10/s 表示约 100ms
	start := time.Now()
	if err := limiter.acquire(ctx, "message/create"); err != nil {
		t.Fatalf("等待令牌失败: %v", err)
	}
	limiter.release("message/create")
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("第三次请求应被限速，实际仅耗时 %v", elapsed)
	}
}

func TestRateLimiterRespectsContextCancellation(t *testing.T) {
	limiter := newRateLimiter(1, 1)
	limiter.tokens = 0

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	if err := limiter.acquire(ctx, "message/create"); err == nil {
		t.Fatal("上下文取消时应返回错误")
	}
}

func TestRateLimiterPenalizeBlocksRequests(t *testing.T) {
	limiter := newRateLimiter(100, 10)
	limiter.penalize(200 * time.Millisecond)

	start := time.Now()
	if err := limiter.acquire(context.Background(), "message/create"); err != nil {
		t.Fatalf("等待失败: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("惩罚期内应被阻塞，实际耗时 %v", elapsed)
	}
}

// TestRateLimiterBucketExhaustionDoesNotStallOthers 是回归测试：
//
// 旧实现把每个响应里的 remaining/reset 都当成全局额度，只要有一个桶
// 剩余不足，整个客户端会被停到窗口结束——线上表现为「点一下按钮要等十几秒」。
// 现在额度不足只影响它自己那个桶。
func TestRateLimiterBucketExhaustionDoesNotStallOthers(t *testing.T) {
	limiter := newRateLimiter(100, 10)
	exhausted := rateInfo{found: true, bucket: "channel-role", limit: 5, remaining: 0, reset: 5 * time.Second}
	limiter.observe("channel-role/create", "endpoint:channel-role/create", exhausted)

	// 同桶：额度已用完，必须等待窗口重置
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := limiter.acquire(ctx, limiter.keyFor("channel-role/create")); err == nil {
		t.Fatal("额度耗尽的桶不应放行请求")
	}

	// 其它桶：立刻放行，不能被拖慢
	start := time.Now()
	if err := limiter.acquire(context.Background(), limiter.keyFor("message/create")); err != nil {
		t.Fatalf("其它桶不应被阻塞: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("其它桶被无用拖慢了 %v", elapsed)
	}
}

// TestRateLimiterLearnsBucketQuota 验证平台回报的额度会被记住并生效。
func TestRateLimiterLearnsBucketQuota(t *testing.T) {
	limiter := newRateLimiter(100, 10)
	key := limiter.keyFor("channel-role/update")
	limiter.observe("channel-role/update", key, rateInfo{
		found: true, bucket: "channel-role/update", limit: 2, remaining: 1, reset: 2 * time.Second,
	})
	if got := limiter.keyFor("channel-role/update"); got != "channel-role/update" {
		t.Fatalf("学到真实桶名后应按桶名限速，得到 %q", got)
	}

	if err := limiter.acquire(context.Background(), key); err != nil {
		t.Fatalf("剩余 1 个额度时应放行: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := limiter.acquire(ctx, key); err == nil {
		t.Fatal("额度用完后不应继续放行")
	}
}

// TestRateLimiterUnknownBucketSerializes 验证「还没拿到额度信息」的桶只放行一个请求，
// 避免在不了解平台额度时打爆它，同时不影响其它接口。
func TestRateLimiterUnknownBucketSerializes(t *testing.T) {
	limiter := newRateLimiter(100, 10)

	if err := limiter.acquire(context.Background(), "channel-role/create"); err != nil {
		t.Fatalf("首个请求应放行: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	if err := limiter.acquire(ctx, "channel-role/create"); err == nil {
		t.Fatal("未知额度的桶不应并发放行多个请求")
	}

	// 请求结束（未拿到限流头）后应恢复放行
	limiter.release("channel-role/create")
	if err := limiter.acquire(context.Background(), "channel-role/create"); err != nil {
		t.Fatalf("释放后应可再次放行: %v", err)
	}
}

// TestRateLimiterRateRecoversAfterPenalty 验证降速只是临时的：
//
// 旧实现一旦读到低额度就把速率永久改小（只降不升），进程越跑越慢。
func TestRateLimiterRateRecoversAfterPenalty(t *testing.T) {
	limiter := newRateLimiter(10, 10)
	base := limiter.currentRate()

	limiter.penalize(time.Millisecond)
	if got := limiter.currentRate(); got >= base {
		t.Fatalf("被限流后应临时降速，得到 %v", got)
	}

	for i := 0; i < successStreakForBoost; i++ {
		limiter.success()
	}
	if got := limiter.currentRate(); got <= base/2 {
		t.Fatalf("连续成功后速率应回升，得到 %v", got)
	}

	// 无论如何都不应超过上限
	for i := 0; i < successStreakForBoost*20; i++ {
		limiter.success()
	}
	if got := limiter.currentRate(); got > limiter.ceiling {
		t.Fatalf("速率不应超过上限 %v，得到 %v", limiter.ceiling, got)
	}
}

func TestExtractRateInfo(t *testing.T) {
	header := make(map[string][]string)
	for key, value := range map[string]string{
		"X-Rate-Limit-Limit":     "5",
		"X-Rate-Limit-Remaining": "0",
		"X-Rate-Limit-Reset":     "14",
		"X-Rate-Limit-Bucket":    "user/info",
		"X-Rate-Limit-Global":    "true",
		"Retry-After":            "3",
	} {
		header[key] = []string{value}
	}

	info := extractRateInfo(header)
	if !info.found {
		t.Fatal("应识别出限流头")
	}
	if info.bucket != "user/info" || info.limit != 5 || info.remaining != 0 {
		t.Fatalf("限流头解析不正确: %+v", info)
	}
	if info.reset != 14*time.Second {
		t.Fatalf("Reset 应为 14s，得到 %v", info.reset)
	}
	if !info.global {
		t.Fatal("应识别出全局限流标记")
	}
	if info.retryAfter != 3*time.Second {
		t.Fatalf("Retry-After 应为 3s，得到 %v", info.retryAfter)
	}

	// 没有限流头时不应误判
	empty := extractRateInfo(map[string][]string{})
	if empty.found {
		t.Fatal("没有限流头时 found 应为 false")
	}
	if empty.remaining != -1 {
		t.Fatalf("缺失 remaining 时应为 -1，得到 %v", empty.remaining)
	}
}
