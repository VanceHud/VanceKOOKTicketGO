// Package llm 提供对 OpenAI 兼容 API（chat/completions + embeddings）的客户端封装。
//
// 一套协议可对接 DeepSeek、GLM、Kimi、Qwen、OpenAI 等主流服务，
// 只需配置 BaseURL + API Key + 模型名。
//
// 安全约定（对齐 internal/kook）：
//   - API Key 只出现在 Authorization 头，绝不写入日志；
//   - 客户端内置限速与超时，调用方无需自行节流。
package llm

import (
	"errors"
	"fmt"
	"time"
)

// 错误类别，便于上层按语义处理（errors.Is）。
var (
	// ErrUnauthorized 表示 API Key 无效或没有权限。
	ErrUnauthorized = errors.New("LLM API Key 无效或未授权")
	// ErrRateLimited 表示触发服务商限流，且重试后仍未成功。
	ErrRateLimited = errors.New("LLM 请求被限流")
	// ErrNotConfigured 表示客户端缺少必填配置（BaseURL / Key / 模型）。
	ErrNotConfigured = errors.New("LLM 未配置")
)

// APIError 是服务商返回的非 2xx 响应。
type APIError struct {
	// HTTPStatus 是 HTTP 状态码。
	HTTPStatus int
	// Message 是响应体里的错误描述（截断到安全长度）。
	Message string
	// Endpoint 是请求路径（不含查询参数与请求体）。
	Endpoint string
	// RetryAfter 是 429 响应的 Retry-After 头（0 表示未提供），供重试决策使用。
	RetryAfter time.Duration `json:"-"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("LLM 接口 %s 返回错误: HTTP %d message=%s", e.Endpoint, e.HTTPStatus, e.Message)
}

// Is 支持 errors.Is 语义。
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.HTTPStatus == 401 || e.HTTPStatus == 403
	case ErrRateLimited:
		return e.HTTPStatus == 429
	default:
		return false
	}
}

// Message 是一条对话消息（OpenAI 兼容格式）。
type Message struct {
	// Role 取 system / user / assistant。
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest 是一次对话补全请求。
type ChatRequest struct {
	Messages []Message
	// Model 留空时使用客户端配置的默认模型。
	Model string
	// Temperature 0 表示使用服务商默认值。
	Temperature float64
	// MaxTokens 0 表示使用服务商默认值。
	MaxTokens int
	// JSONMode 要求模型返回合法 JSON（response_format=json_object）。
	// 部分服务商不支持该字段时会忽略；调用方必须对输出做容错解析。
	JSONMode bool
}

// ChatResponse 是一次对话补全的结果。
type ChatResponse struct {
	Content string
	// PromptTokens / CompletionTokens 是服务商回报的用量（可能为 0）。
	PromptTokens     int
	CompletionTokens int
	// FinishReason 是停止原因（stop / length 等，可能为空）。
	FinishReason string
}
