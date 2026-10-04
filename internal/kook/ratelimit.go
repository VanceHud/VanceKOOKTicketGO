package kook

import (
	"context"
	"errors"
	"fmt"
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

// rateLimiter 是简单的令牌桶限速器。
//
// KOOK 对机器人有调用频率限制（约每秒数次），超限会返回 429。
// 这里用令牌桶做客户端自我保护，并额外读取平台返回的限流响应头动态降速。
type rateLimiter struct {
	mu sync.Mutex
	// rate 是每秒补充的令牌数。
	rate float64
	// burst 是桶容量。
	burst  float64
	tokens float64
	last   time.Time
	// penaltyUntil 在被限流后的一段时间内强制降速。
	penaltyUntil time.Time
	now          func() time.Time
}

func newRateLimiter(rate float64, burst int) *rateLimiter {
	if rate <= 0 {
		rate = 5
	}
	if burst <= 0 {
		burst = 5
	}
	return &rateLimiter{
		rate:   rate,
		burst:  float64(burst),
		tokens: float64(burst),
		last:   time.Now(),
		now:    time.Now,
	}
}

// wait 阻塞直到取得一个令牌或 ctx 结束。
func (l *rateLimiter) wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := l.now()
		// 令牌补充
		elapsed := now.Sub(l.last).Seconds()
		if elapsed > 0 {
			l.tokens = minFloat(l.burst, l.tokens+elapsed*l.rate)
			l.last = now
		}
		if l.tokens >= 1 && !now.Before(l.penaltyUntil) {
			l.tokens--
			l.mu.Unlock()
			return nil
		}

		waitFor := time.Duration(float64(time.Second) / l.rate)
		if now.Before(l.penaltyUntil) {
			waitFor = l.penaltyUntil.Sub(now)
		} else {
			waitFor = time.Duration(float64(time.Second) * (1 - l.tokens) / l.rate)
		}
		l.mu.Unlock()

		if waitFor < 10*time.Millisecond {
			waitFor = 10 * time.Millisecond
		}
		if waitFor > 5*time.Second {
			waitFor = 5 * time.Second
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitFor):
		}
	}
}

// penalize 在收到限流响应后临时降低速率。
func (l *rateLimiter) penalize(d time.Duration) {
	if d <= 0 {
		d = time.Second
	}
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	until := l.now().Add(d)
	if until.After(l.penaltyUntil) {
		l.penaltyUntil = until
	}
	// 清空令牌，强制排队等待
	l.tokens = 0
	l.last = l.now()
}

// observeRemaining 依据平台返回的剩余额度动态调整速率，避免撞上限流。
func (l *rateLimiter) observeRemaining(remaining, limit float64, reset time.Duration) {
	if limit <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// 剩余不足 20% 时，按照 reset 窗口均摊剩余额度
	if remaining <= limit*0.2 && reset > 0 {
		rate := remaining / reset.Seconds()
		if rate > 0 && rate < l.rate {
			l.rate = rate
		}
		l.penaltyUntil = l.now().Add(reset)
	}
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
