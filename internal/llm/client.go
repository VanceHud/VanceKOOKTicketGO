package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 客户端默认参数。
//
// 超时给得比 KOOK 客户端长：LLM 生成一段回复通常需要数秒到数十秒，
// 15s 会把正常的慢模型误杀；限速默认 1 req/s——自动应答是「尽力而为」的
// 增强能力，不值得为它打爆服务商配额。
const (
	defaultTimeout       = 60 * time.Second
	defaultRatePerSecond = 1.0
	defaultBurst         = 2
	defaultMaxRetries    = 2
	// retryBase 是重试的基础退避：第 1 次等 800ms，第 2 次等 1.6s。
	retryBase = 800 * time.Millisecond
	// maxErrorBody 是错误响应体的读取上限，防止异常服务返回超大页面。
	maxErrorBody = 4 * 1024
)

// Options 是客户端构造参数。
type Options struct {
	// BaseURL 形如 https://api.deepseek.com/v1（不带末尾斜杠）；
	// 留空时使用 DefaultBaseURL。
	BaseURL string
	// APIKey 必填（Bearer 认证）。
	APIKey string
	// Model 是默认模型名，可被 ChatRequest.Model 覆盖。
	Model string
	// HTTPClient 可选；默认 60s 超时 + 适度连接池。
	HTTPClient *http.Client
	// Timeout 覆盖默认 HTTP 超时（仅在未提供 HTTPClient 时生效）。
	Timeout time.Duration
	// RatePerSecond / Burst 控制客户端侧限速，默认 1/s、突发 2。
	RatePerSecond float64
	Burst         int
	// MaxRetries 是 429/5xx/网络错误的最大重试次数，默认 2。
	MaxRetries int
	Logger     *slog.Logger
}

// DefaultBaseURL 指向 OpenAI 官方地址；对接其它服务商时配置 BaseURL 覆盖。
const DefaultBaseURL = "https://api.openai.com/v1"

// Client 是 OpenAI 兼容 API 客户端。
type Client struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
	limiter    *tokenBucket
	maxRetries int
	log        *slog.Logger
}

// NewClient 创建客户端；BaseURL / APIKey / Model 任一为空返回 ErrNotConfigured。
func NewClient(opts Options) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	apiKey := strings.TrimSpace(opts.APIKey)
	model := strings.TrimSpace(opts.Model)
	if apiKey == "" || model == "" {
		return nil, fmt.Errorf("%w：需要配置 BaseURL、API Key 与模型名", ErrNotConfigured)
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = defaultTimeout
		}
		httpClient = defaultHTTPClient(timeout)
	}

	rate := opts.RatePerSecond
	if rate <= 0 {
		rate = defaultRatePerSecond
	}
	burst := opts.Burst
	if burst <= 0 {
		burst = defaultBurst
	}
	maxRetries := opts.MaxRetries
	if maxRetries <= 0 {
		maxRetries = defaultMaxRetries
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		baseURL:    baseURL,
		apiKey:     apiKey,
		model:      model,
		httpClient: httpClient,
		limiter:    newTokenBucket(rate, burst),
		maxRetries: maxRetries,
		log:        logger,
	}, nil
}

// Model 返回默认模型名。
func (c *Client) Model() string { return c.model }

// BaseURL 返回 API 基础地址。
func (c *Client) BaseURL() string { return c.baseURL }

// defaultHTTPClient 构造适合 LLM 调用的客户端：长超时 + 小连接池。
func defaultHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 8
	transport.MaxIdleConnsPerHost = 4
	transport.IdleConnTimeout = 90 * time.Second
	return &http.Client{Timeout: timeout, Transport: transport}
}

// Chat 调用 chat/completions 生成一段回复。
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.model
	}
	payload := map[string]any{
		"model":    model,
		"messages": req.Messages,
	}
	if req.Temperature > 0 {
		payload["temperature"] = req.Temperature
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	if req.JSONMode {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}

	var out chatCompletionResponse
	if err := c.post(ctx, "chat/completions", payload, &out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("LLM 返回了空的选择列表（模型 %s）", model)
	}
	resp := &ChatResponse{
		Content:          out.Choices[0].Message.Content,
		PromptTokens:     out.Usage.PromptTokens,
		CompletionTokens: out.Usage.CompletionTokens,
		FinishReason:     out.Choices[0].FinishReason,
	}
	c.log.Debug("LLM 调用完成",
		"model", model, "prompt_tokens", resp.PromptTokens,
		"completion_tokens", resp.CompletionTokens, "finish_reason", resp.FinishReason)
	return resp, nil
}

// Embed 调用 embeddings 接口把文本转向量。
//
// model 留空时使用客户端默认模型。返回顺序与输入一致。
func (c *Client) Embed(ctx context.Context, texts []string, model string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if m := strings.TrimSpace(model); m != "" {
		model = m
	} else {
		model = c.model
	}
	payload := map[string]any{
		"model": model,
		"input": texts,
	}

	var out embeddingsResponse
	if err := c.post(ctx, "embeddings", payload, &out); err != nil {
		return nil, err
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("LLM embeddings 返回数量不符：请求 %d 条，返回 %d 条", len(texts), len(out.Data))
	}
	// 服务商按 index 返回，这里按 index 归位，保证与输入顺序一致。
	vectors := make([][]float32, len(texts))
	for _, item := range out.Data {
		if item.Index < 0 || item.Index >= len(vectors) {
			return nil, fmt.Errorf("LLM embeddings 返回了越界的 index %d", item.Index)
		}
		vectors[item.Index] = item.Embedding
	}
	return vectors, nil
}

// Test 用一条极小的请求验证连通性与凭据，供 WebUI「测试连接」按钮使用。
func (c *Client) Test(ctx context.Context) error {
	_, err := c.Chat(ctx, ChatRequest{
		Messages:  []Message{{Role: "user", Content: "ping"}},
		MaxTokens: 8,
	})
	return err
}

// ---------------------------------------------------------------------------
// 传输层
// ---------------------------------------------------------------------------

// chatCompletionResponse 是 chat/completions 的响应结构（只取需要的字段）。
type chatCompletionResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// embeddingsResponse 是 embeddings 的响应结构。
type embeddingsResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// post 发送 POST 请求并解析 JSON 响应，带限速与重试。
//
// 重试策略：429（尊重 Retry-After）、5xx 与网络错误重试，最多 maxRetries 次；
// 4xx（除 429）是请求本身的问题，重试没有意义，立即返回。
// 上下文取消永不重试。
func (c *Client) post(ctx context.Context, endpoint string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化 LLM 请求失败: %w", err)
	}

	var lastErr error
	for attempt := 0; ; attempt++ {
		if err := c.limiter.acquire(ctx); err != nil {
			return err
		}
		err := c.doOnce(ctx, endpoint, body, out)
		if err == nil {
			return nil
		}
		lastErr = err
		// 上下文已结束（超时/取消）时不再重试：重试只会白等。
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !retryable(err) || attempt >= c.maxRetries {
			return lastErr
		}

		wait := backoffFor(attempt)
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.HTTPStatus == http.StatusTooManyRequests && apiErr.RetryAfter > wait {
			wait = apiErr.RetryAfter
		}
		c.log.Warn("LLM 请求失败，准备重试",
			"endpoint", endpoint, "attempt", attempt+1, "wait_ms", wait.Milliseconds(), "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// doOnce 执行一次 HTTP 调用。
func (c *Client) doOnce(ctx context.Context, endpoint string, body []byte, out any) error {
	url := c.baseURL + "/" + endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("构造 LLM 请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// 网络层错误（连接拒绝、DNS 失败等）：可安全重试。
		return fmt.Errorf("请求 LLM 接口失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		apiErr := &APIError{
			HTTPStatus: resp.StatusCode,
			Message:    truncateRunes(string(snippet), 200),
			Endpoint:   endpoint,
		}
		// Retry-After 供上层重试决策使用，挂在错误上传递。
		if v := resp.Header.Get("Retry-After"); v != "" {
			if seconds, perr := strconv.Atoi(strings.TrimSpace(v)); perr == nil {
				apiErr.RetryAfter = time.Duration(seconds) * time.Second
			}
		}
		return apiErr
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("解析 LLM 响应失败: %w", err)
	}
	return nil
}

// retryable 判断错误是否值得重试。
func retryable(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.HTTPStatus == http.StatusTooManyRequests || apiErr.HTTPStatus >= 500
	}
	// 网络错误（含超时）：LLM 调用是生成类操作，重复一次最多多花一次配额，
	// 相比直接放弃（用户等不到回复）通常更划算。
	return true
}

// backoffFor 返回第 attempt 次重试前的退避时长。
func backoffFor(attempt int) time.Duration {
	return retryBase * time.Duration(1<<attempt)
}

// truncateRunes 按字符截断字符串。
func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
