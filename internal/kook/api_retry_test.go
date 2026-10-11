package kook_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook"
)

func retryTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newRetryTestClient 基于可编程假服务构造 REST 客户端。
func newRetryTestClient(server *httptest.Server) (*kook.Client, error) {
	return kook.NewClient(kook.Options{
		Token:   "test-token-abcdefghijklmnop",
		BaseURL: server.URL,
		Logger:  retryTestLogger(),
	})
}

func writeEnvelope(w http.ResponseWriter, status int, data string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"code":0,"message":"操作成功","data":` + data + `}`))
}

// TestClientBucketScoped429DoesNotStallOtherEndpoints 验证桶级 429 不触发全局惩罚。
//
// 官方文档语义：X-Rate-Limit-Global 才表示触犯全局限制；桶级限流按桶排队。
// 旧实现对任何 429 都清零全局令牌并降速，一个非关键接口被限流
// 会让所有工单关键调用一起停摆。
//
// 观察方式：全局惩罚只在退避等待窗口内对并发调用者可见（等待结束后令牌
// 已回补），因此在 429 的退避期间发起另一个桶的调用，断言它不被拖慢。
func TestClientBucketScoped429DoesNotStallOtherEndpoints(t *testing.T) {
	var meCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/api/v3/") {
		case "user/me":
			if atomic.AddInt32(&meCalls, 1) == 1 {
				w.Header().Set("X-Rate-Limit-Bucket", "user/me")
				w.Header().Set("X-Rate-Limit-Remaining", "0")
				w.Header().Set("X-Rate-Limit-Reset", "0.3")
				w.Header().Set("Retry-After", "0.3")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"code":42900,"message":"被限流","data":[]}`))
				return
			}
			writeEnvelope(w, http.StatusOK, `{}`)
		case "guild/view":
			writeEnvelope(w, http.StatusOK, `{"id":"5000"}`)
		default:
			t.Errorf("意外的请求: %s", r.URL.Path)
			writeEnvelope(w, http.StatusOK, `{}`)
		}
	}))
	t.Cleanup(server.Close)

	client, err := newRetryTestClient(server)
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}
	ctx := context.Background()

	// 后台触发一次桶级 429：客户端会退避约 2s 后重试。
	done := make(chan error, 1)
	go func() {
		_, err := client.Me(ctx)
		done <- err
	}()
	waitForCondition(t, "首次 user/me 请求到达", func() bool { return atomic.LoadInt32(&meCalls) >= 1 })
	time.Sleep(100 * time.Millisecond) // 留出 429 响应处理与退避进入的时间

	// 关键断言：退避窗口内另一个桶的接口不应被全局惩罚阻塞
	//（若误施全局惩罚，这里会停顿到惩罚期结束，≥1s）。
	start := time.Now()
	if _, err := client.GuildView(ctx, "5000"); err != nil {
		t.Fatalf("guild/view 应成功: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("桶级 429 不应拖慢其它接口，guild/view 耗时 %v", elapsed)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("user/me 重试后应成功: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("user/me 重试超时")
	}
	if atomic.LoadInt32(&meCalls) != 2 {
		t.Fatalf("user/me 应被调用 2 次（429 + 重试），实际 %d 次", meCalls)
	}
}

// TestClientGlobal429AppliesGlobalPenalty 验证平台明确提示的全局限流
// 仍会施加全局惩罚（停顿 + 降速），与桶级限流区别对待。
func TestClientGlobal429AppliesGlobalPenalty(t *testing.T) {
	var meCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/api/v3/") {
		case "user/me":
			if atomic.AddInt32(&meCalls, 1) == 1 {
				w.Header().Set("X-Rate-Limit-Bucket", "user/me")
				w.Header().Set("X-Rate-Limit-Global", "true")
				w.Header().Set("Retry-After", "0.3")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"code":42900,"message":"全局限流","data":[]}`))
				return
			}
			writeEnvelope(w, http.StatusOK, `{}`)
		case "guild/view":
			writeEnvelope(w, http.StatusOK, `{"id":"5000"}`)
		default:
			writeEnvelope(w, http.StatusOK, `{}`)
		}
	}))
	t.Cleanup(server.Close)

	client, err := newRetryTestClient(server)
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}
	ctx := context.Background()

	done := make(chan error, 1)
	go func() {
		_, err := client.Me(ctx)
		done <- err
	}()
	waitForCondition(t, "首次 user/me 请求到达", func() bool { return atomic.LoadInt32(&meCalls) >= 1 })
	time.Sleep(100 * time.Millisecond) // 留出 penalize 生效的时间

	// 全局惩罚清零令牌并停顿到惩罚期结束（约 2s）：
	// 退避窗口内发起的其它接口调用必须一起等待。
	start := time.Now()
	if _, err := client.GuildView(ctx, "5000"); err != nil {
		t.Fatalf("guild/view 应成功: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 800*time.Millisecond {
		t.Fatalf("全局 429 后应观察到全局停顿，guild/view 仅耗时 %v", elapsed)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("user/me 重试后应成功: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("user/me 重试超时")
	}
}

// waitForCondition 轮询等待条件成立（上限 5s）。
func waitForCondition(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

// TestClientRetriesIdempotentGetOnServerError 验证幂等 GET 遇平台 5xx
// 会退避重试自愈，而不是一次失败就放弃。
func TestClientRetriesIdempotentGetOnServerError(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":50000,"message":"内部错误","data":[]}`))
			return
		}
		writeEnvelope(w, http.StatusOK, `{}`)
	}))
	t.Cleanup(server.Close)

	client, err := newRetryTestClient(server)
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}

	start := time.Now()
	if _, err := client.Me(context.Background()); err != nil {
		t.Fatalf("5xx 后应重试成功: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("应调用 2 次（失败 + 重试），实际 %d 次", calls)
	}
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond {
		t.Fatalf("重试应带退避（≥500ms 基础退避），实际仅 %v", elapsed)
	}
}

// TestClientRetriesIdempotentPost 验证幂等 POST（如 message/update）
// 遇 5xx 同样重试：重复更新为相同内容无副作用。
func TestClientRetriesIdempotentPost(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"code":50200,"message":"网关错误","data":[]}`))
			return
		}
		writeEnvelope(w, http.StatusOK, `[]`)
	}))
	t.Cleanup(server.Close)

	client, err := newRetryTestClient(server)
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}

	if err := client.UpdateChannelMessage(context.Background(), "msg-1", "内容"); err != nil {
		t.Fatalf("message/update 重试后应成功: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("应调用 2 次（失败 + 重试），实际 %d 次", calls)
	}
}

// TestClientDoesNotRetryNonIdempotentCreate 验证创建类端点（发消息、建频道）
// 遇 5xx 不自动重试：网络层无法确认平台是否已执行，重试可能重复发送。
func TestClientDoesNotRetryNonIdempotentCreate(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":50000,"message":"内部错误","data":[]}`))
	}))
	t.Cleanup(server.Close)

	client, err := newRetryTestClient(server)
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}

	_, err = client.SendChannelMessage(context.Background(), "chan-1", kook.MsgTypeText, "你好", kook.MessageOptions{})
	if err == nil {
		t.Fatal("message/create 失败应返回错误")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("创建类端点不应自动重试，实际调用 %d 次", calls)
	}
}
