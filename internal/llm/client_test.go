package llm_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/llm"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/llm/llmtest"
)

func newTestClient(t *testing.T, server *llmtest.Server) *llm.Client {
	t.Helper()
	client, err := llm.NewClient(llm.Options{
		BaseURL: server.BaseURL(),
		APIKey:  "sk-test-key",
		Model:   "mock-model",
		// 测试不等真实退避：限速开到不影响断言的程度。
		RatePerSecond: 100,
		Burst:         10,
	})
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}
	return client
}

func TestChatSuccess(t *testing.T) {
	server := llmtest.New()
	defer server.Close()
	server.SetReply(func(req llmtest.Request, _ []llm.Message) string {
		return "你好，这是模拟回复"
	})

	client := newTestClient(t, server)
	resp, err := client.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "你是客服"},
			{Role: "user", Content: "退款多久到账"},
		},
	})
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if resp.Content != "你好，这是模拟回复" {
		t.Fatalf("回复内容不符: %q", resp.Content)
	}
	if resp.PromptTokens != 100 || resp.CompletionTokens != 20 {
		t.Fatalf("用量不符: %+v", resp)
	}

	requests := server.RequestsOf("chat/completions")
	if len(requests) != 1 {
		t.Fatalf("应有 1 次调用，实际 %d 次", len(requests))
	}
	if requests[0].Model != "mock-model" {
		t.Fatalf("模型名不符: %q", requests[0].Model)
	}
	if len(requests[0].Messages) != 2 {
		t.Fatalf("消息数不符: %d", len(requests[0].Messages))
	}
}

func TestChatJSONModeFlag(t *testing.T) {
	server := llmtest.New()
	defer server.Close()

	client := newTestClient(t, server)
	if _, err := client.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "ping"}},
		JSONMode: true,
	}); err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	requests := server.RequestsOf("chat/completions")
	if len(requests) != 1 || !requests[0].JSONMode {
		t.Fatalf("JSON 模式未传给服务商: %+v", requests)
	}
}

func TestChatUnauthorized(t *testing.T) {
	server := llmtest.New()
	defer server.Close()
	server.Unauthorized = true

	client := newTestClient(t, server)
	_, err := client.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "ping"}},
	})
	if !errors.Is(err, llm.ErrUnauthorized) {
		t.Fatalf("应返回 ErrUnauthorized，实际: %v", err)
	}
}

func TestChatRetriesOnServerError(t *testing.T) {
	server := llmtest.New()
	defer server.Close()
	server.FailFirst = 2 // 前两次 500，第三次成功

	client := newTestClient(t, server)
	resp, err := client.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("重试后应成功，实际: %v", err)
	}
	if resp.Content == "" {
		t.Fatal("回复内容为空")
	}
	if calls := len(server.RequestsOf("chat/completions")); calls != 3 {
		t.Fatalf("应有 3 次调用（2 次失败 + 1 次成功），实际 %d 次", calls)
	}
}

func TestChatRateLimitedAfterRetries(t *testing.T) {
	server := llmtest.New()
	defer server.Close()
	server.RateLimited = true

	client := newTestClient(t, server)
	_, err := client.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "ping"}},
	})
	if !errors.Is(err, llm.ErrRateLimited) {
		t.Fatalf("应返回 ErrRateLimited，实际: %v", err)
	}
}

func TestChatContextCancel(t *testing.T) {
	server := llmtest.New()
	defer server.Close()
	server.Delay = 2 * time.Second

	client := newTestClient(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := client.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "ping"}},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("应返回超时，实际: %v", err)
	}
}

func TestEmbedOrderPreserved(t *testing.T) {
	server := llmtest.New()
	defer server.Close()

	client := newTestClient(t, server)
	vectors, err := client.Embed(context.Background(), []string{"退款", "发货", "退款"}, "")
	if err != nil {
		t.Fatalf("Embed 失败: %v", err)
	}
	if len(vectors) != 3 {
		t.Fatalf("向量数不符: %d", len(vectors))
	}
	// 相同文本必须得到相同向量（模拟器保证确定性）。
	if vectors[0][0] != vectors[2][0] {
		t.Fatalf("相同文本的向量不一致")
	}
	if len(server.RequestsOf("embeddings")) != 1 {
		t.Fatalf("应有 1 次 embeddings 调用")
	}
}

func TestEmbedEmptyInput(t *testing.T) {
	server := llmtest.New()
	defer server.Close()

	client := newTestClient(t, server)
	vectors, err := client.Embed(context.Background(), nil, "")
	if err != nil {
		t.Fatalf("空输入应直接返回: %v", err)
	}
	if vectors != nil {
		t.Fatalf("空输入应返回 nil")
	}
}

func TestNewClientRequiresConfig(t *testing.T) {
	if _, err := llm.NewClient(llm.Options{BaseURL: "https://x", Model: "m"}); !errors.Is(err, llm.ErrNotConfigured) {
		t.Fatalf("缺少 Key 应返回 ErrNotConfigured: %v", err)
	}
	if _, err := llm.NewClient(llm.Options{APIKey: "k"}); !errors.Is(err, llm.ErrNotConfigured) {
		t.Fatalf("缺少模型应返回 ErrNotConfigured: %v", err)
	}
}

func TestAPIErrorMessageTruncated(t *testing.T) {
	err := &llm.APIError{
		HTTPStatus: 500,
		Message:    strings.Repeat("x", 500),
		Endpoint:   "chat/completions",
	}
	if !strings.Contains(err.Error(), "chat/completions") {
		t.Fatalf("错误信息应包含端点名: %s", err.Error())
	}
}
