package kook

import (
	"context"
	"testing"
	"time"
)

func TestRateLimiterAllowsBurstThenThrottles(t *testing.T) {
	// 注意：这里使用真实时钟。若把 now 冻结，令牌永远不会补充，
	// wait() 将无限重试（该写法曾在本地把测试卡死，故加此注释警示）。
	limiter := newRateLimiter(10, 2)

	ctx := context.Background()

	// 桶容量为 2，前两次应立即通过
	for i := 0; i < 2; i++ {
		if err := limiter.wait(ctx); err != nil {
			t.Fatalf("第 %d 次请求不应被阻塞: %v", i+1, err)
		}
	}

	// 第三次需要等待补桶：10/s 表示约 100ms
	start := time.Now()
	if err := limiter.wait(ctx); err != nil {
		t.Fatalf("等待令牌失败: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("第三次请求应被限速，实际仅耗时 %v", elapsed)
	}
}

func TestRateLimiterRespectsContextCancellation(t *testing.T) {
	limiter := newRateLimiter(1, 1)
	limiter.tokens = 0

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	if err := limiter.wait(ctx); err == nil {
		t.Fatal("上下文取消时应返回错误")
	}
}

func TestRateLimiterPenalizeBlocksRequests(t *testing.T) {
	limiter := newRateLimiter(100, 10)
	limiter.penalize(200 * time.Millisecond)

	start := time.Now()
	if err := limiter.wait(context.Background()); err != nil {
		t.Fatalf("等待失败: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("惩罚期内应被阻塞，实际耗时 %v", elapsed)
	}
}

func TestRateLimiterObserveRemainingSlowsDown(t *testing.T) {
	limiter := newRateLimiter(100, 10)

	// 剩余额度只剩 5%，窗口 1s：速率应被调低
	limiter.observeRemaining(0.5, 10, time.Second)

	limiter.mu.Lock()
	rate := limiter.rate
	penalty := time.Until(limiter.penaltyUntil)
	limiter.mu.Unlock()

	if rate >= 100 {
		t.Fatalf("剩余额度不足时应降低速率，当前 %v", rate)
	}
	if penalty <= 0 {
		t.Fatal("应进入惩罚期")
	}
}

func TestGatewayBackoffGrowsAndCaps(t *testing.T) {
	gateway, err := NewGateway(GatewayOptions{Client: &Client{}, MaxBackoff: 10 * time.Second})
	if err != nil {
		t.Fatalf("创建网关失败: %v", err)
	}

	expected := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	for i, want := range expected {
		gateway.attempt.Store(int32(i))
		if got := gateway.backoff(); got != want {
			t.Fatalf("第 %d 次退避应为 %v，得到 %v", i+1, want, got)
		}
	}
}
