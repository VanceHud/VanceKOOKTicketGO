// Package llmtest 提供一个可编程的 OpenAI 兼容 LLM 服务模拟器。
//
// 目的：AI 自动应答 / 摘要 / 分类 / 知识闭环的行为取决于「LLM 返回了什么、
// 失败时怎么降级」，这些用进程内假服务可以完整验证，不需要真实 API Key。
//
// 模拟器保留的真实细节：
//   - chat/completions 与 embeddings 的请求/响应结构；
//   - Bearer 认证（缺失或错误返回 401）；
//   - 429 限流（带 Retry-After）与 5xx 可编程故障；
//   - 记录每次请求的完整消息列表，供测试断言提示词组装是否正确。
package llmtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/llm"
)

// Request 记录一次 LLM 调用。
type Request struct {
	// Endpoint 是 chat/completions 或 embeddings。
	Endpoint string
	// Model 是请求里的模型名。
	Model string
	// Messages 是对话消息（仅 chat/completions）。
	Messages []llm.Message
	// JSONMode 标记请求是否要求 JSON 输出。
	JSONMode bool
	// At 是请求时间。
	At time.Time
}

// Server 是模拟的 LLM 服务。
type Server struct {
	HTTP *httptest.Server

	mu       sync.Mutex
	requests []Request
	// reply 是 chat/completions 的应答函数；nil 时返回固定文本 "（模拟回复）"。
	reply func(req Request, messages []llm.Message) string
	// embedFn 是 embeddings 的应答函数；nil 时按文本哈希生成确定性向量。
	embedFn func(texts []string) [][]float32

	// 可编程行为
	// Unauthorized 为真时对一切请求返回 401（模拟 Key 无效）。
	Unauthorized bool
	// RateLimited 为真时返回 429 + Retry-After（模拟限流）。
	RateLimited bool
	// FailFirst 是剩余的失败次数：>0 时该次请求返回 500（模拟瞬时故障，验证重试）。
	FailFirst int
	// Delay 让每次请求慢下来，便于测试超时与并发行为。
	Delay time.Duration
}

// New 启动模拟 LLM 服务。
func New() *Server {
	s := &Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", s.handleChat)
	mux.HandleFunc("/embeddings", s.handleEmbeddings)
	s.HTTP = httptest.NewServer(mux)
	return s
}

// Close 关闭模拟服务。
func (s *Server) Close() { s.HTTP.Close() }

// BaseURL 返回模拟服务地址（用于 llm.Options.BaseURL）。
func (s *Server) BaseURL() string { return s.HTTP.URL }

// SetReply 设置 chat/completions 的应答函数。
func (s *Server) SetReply(fn func(req Request, messages []llm.Message) string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reply = fn
}

// SetEmbedder 设置 embeddings 的应答函数。
func (s *Server) SetEmbedder(fn func(texts []string) [][]float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.embedFn = fn
}

// Requests 返回全部调用记录的副本。
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.requests))
	copy(out, s.requests)
	return out
}

// RequestsOf 返回指定端点的调用记录。
func (s *Server) RequestsOf(endpoint string) []Request {
	out := []Request{}
	for _, req := range s.Requests() {
		if req.Endpoint == endpoint {
			out = append(out, req)
		}
	}
	return out
}

// handleChat 处理 chat/completions。
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Model          string          `json:"model"`
		Messages       []llm.Message   `json:"messages"`
		ResponseFormat json.RawMessage `json:"response_format"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// 先记录再注入故障：测试需要断言「失败后重试了几次」，
	// 只记录成功请求会把这些尝试藏起来。
	record := Request{
		Endpoint: "chat/completions",
		Model:    payload.Model,
		Messages: payload.Messages,
		JSONMode: string(payload.ResponseFormat) != "",
		At:       time.Now(),
	}
	s.mu.Lock()
	s.requests = append(s.requests, record)
	reply := s.reply
	s.mu.Unlock()

	if s.preflight(w, r) {
		return
	}

	content := "（模拟回复）"
	if reply != nil {
		content = reply(record, payload.Messages)
	}
	writeJSON(w, map[string]any{
		"id":     "chatcmpl-mock",
		"object": "chat.completion",
		"model":  payload.Model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     100,
			"completion_tokens": 20,
		},
	})
}

// handleEmbeddings 处理 embeddings。
func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.requests = append(s.requests, Request{
		Endpoint: "embeddings",
		Model:    payload.Model,
		At:       time.Now(),
	})
	embedFn := s.embedFn
	s.mu.Unlock()

	if s.preflight(w, r) {
		return
	}

	vectors := deterministicVectors(payload.Input)
	if embedFn != nil {
		vectors = embedFn(payload.Input)
	}
	data := make([]map[string]any, len(vectors))
	for i, vector := range vectors {
		data[i] = map[string]any{"index": i, "embedding": vector}
	}
	writeJSON(w, map[string]any{
		"object": "list",
		"model":  payload.Model,
		"data":   data,
	})
}

// preflight 执行鉴权、限流、故障注入与延迟；返回 true 表示已写响应。
func (s *Server) preflight(w http.ResponseWriter, r *http.Request) bool {
	if s.Delay > 0 {
		time.Sleep(s.Delay)
	}
	if r.Header.Get("Authorization") == "" {
		http.Error(w, `{"error":{"message":"missing api key"}}`, http.StatusUnauthorized)
		return true
	}
	if s.Unauthorized {
		http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
		return true
	}
	if s.RateLimited {
		w.Header().Set("Retry-After", "1")
		http.Error(w, `{"error":{"message":"rate limited"}}`, http.StatusTooManyRequests)
		return true
	}
	s.mu.Lock()
	fail := s.FailFirst > 0
	if fail {
		s.FailFirst--
	}
	s.mu.Unlock()
	if fail {
		http.Error(w, `{"error":{"message":"internal error"}}`, http.StatusInternalServerError)
		return true
	}
	return false
}

// deterministicVectors 为每段文本生成确定性向量：相同文本得到相同向量，
// 相似前缀的文本向量也相似（首维放文本哈希、其余维度按字节填充），
// 足以在测试里验证「按相似度检索知识」的排序逻辑。
func deterministicVectors(texts []string) [][]float32 {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		vector := make([]float32, 8)
		var sum float32
		for j, b := range []byte(text) {
			vector[j%len(vector)] += float32(b) / 255
			sum += float32(b)
		}
		vector[0] = sum / 255 / 10
		out[i] = vector
	}
	return out
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
