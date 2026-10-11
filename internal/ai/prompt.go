package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/llm"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// ErrInvalidAIState 表示当前状态不允许该操作（API 层映射为 409）。
var ErrInvalidAIState = errors.New("当前状态不允许该 AI 操作")

func errInvalidState(message string) error {
	return fmt.Errorf("%w：%s", ErrInvalidAIState, message)
}

// buildReplyRequest 组装自动应答的对话请求。
//
// 消息结构：system（模板 + 知识示例 + 可选的分类指令）→ 对话历史
// （开单人 = user，人工/AI 回复 = assistant，媒体消息转占位符）。
func buildReplyRequest(cfg *store.AISettings, t *store.Ticket, messages []store.TicketMessage, knowledge []store.AIKnowledge, typeNames []string, wantClassification bool) llm.ChatRequest {
	system := renderSystemPrompt(cfg.SystemPrompt, t)
	if len(knowledge) > 0 {
		system += "\n\n以下是历史工单中人工客服的参考回复示例（优先参考，尤其是标注「人工纠正」的）："
		for i, entry := range knowledge {
			label := "示例"
			if entry.IsCorrection {
				label = "人工纠正"
			}
			system += fmt.Sprintf("\n【%s%d】用户：%s\n客服：%s",
				label, i+1, truncateRunes(entry.Question, 300), truncateRunes(entry.Answer, 300))
		}
	}

	// 分类指令：让模型在同一次调用里返回建议类型（省一次调用）。
	// JSON 输出对部分服务商不可用，解析端做了完整容错（见 parseReplyJSON）。
	jsonMode := false
	if wantClassification && len(typeNames) > 0 {
		system += "\n\n同时判断该工单最匹配的类型。必须只返回一个 JSON 对象，格式："
		system += `{"reply": "给用户的回复", "suggested_type": "类型名", "type_reason": "一句话理由"}`
		system += "\nsuggested_type 只能从这些类型中选择：" + strings.Join(typeNames, "、")
		jsonMode = true
	}

	chatMessages := make([]llm.Message, 0, len(messages)+1)
	chatMessages = append(chatMessages, llm.Message{Role: "system", Content: system})
	for _, m := range messages {
		content := messageTextForPrompt(m)
		if content == "" {
			continue
		}
		role := "assistant"
		if !m.IsBot {
			if m.UserID == t.UserID {
				role = "user"
			}
		}
		chatMessages = append(chatMessages, llm.Message{Role: role, Content: content})
	}

	return llm.ChatRequest{
		Messages:    chatMessages,
		Temperature: cfg.Temperature,
		MaxTokens:   1024,
		JSONMode:    jsonMode,
	}
}

// buildSummaryRequest 组装摘要请求。
func buildSummaryRequest(cfg *store.AISettings, t *store.Ticket, messages []store.TicketMessage) llm.ChatRequest {
	system := "你是工单系统的助手。请用中文总结这段工单对话，输出要点：\n" +
		"1. 用户的核心诉求（1-2 句）；\n" +
		"2. 处理过程与结论（按时间顺序的关键节点）；\n" +
		"3. 待办事项（如有，没有则写「无」）。\n" +
		"控制在 200 字以内，不要输出多余的前后缀。"
	transcript := buildTranscript(t, messages)
	return llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: transcript},
		},
		Temperature: 0.3,
		MaxTokens:   768,
	}
}

// buildClassifyRequest 组装分类请求。
func buildClassifyRequest(cfg *store.AISettings, t *store.Ticket, messages []store.TicketMessage, typeNames []string) llm.ChatRequest {
	system := "你是工单分类助手。根据对话内容判断该工单最匹配的类型。\n" +
		"只能从这些类型中选择：" + strings.Join(typeNames, "、") + "\n" +
		`必须只返回一个 JSON 对象：{"type": "类型名", "reason": "一句话理由"}`
	return llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: buildTranscript(t, messages)},
		},
		Temperature: 0.2,
		MaxTokens:   256,
		JSONMode:    true,
	}
}

// buildTranscript 把工单消息拼成对话文稿（摘要与分类共用）。
func buildTranscript(t *store.Ticket, messages []store.TicketMessage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "工单编号 %s，类型 %s，开单人 %s。\n", t.No, typeNameOrEmpty(t), t.UserName)
	for _, m := range messages {
		content := messageTextForPrompt(m)
		if content == "" {
			continue
		}
		speaker := "客服"
		switch {
		case m.IsAI:
			speaker = "AI"
		case m.IsBot:
			speaker = "系统"
		case m.UserID == t.UserID:
			speaker = "用户"
		}
		fmt.Fprintf(&b, "%s：%s\n", speaker, truncateRunes(content, 500))
	}
	return b.String()
}

// messageTextForPrompt 把一条归档消息转成提示词里的文本。
//
// 媒体消息的 content 是资源地址，对 LLM 没有意义且有提示注入风险，
// 统一替换为占位符；卡片消息已由归档层生成文本摘要，原样使用。
func messageTextForPrompt(m store.TicketMessage) string {
	switch m.MsgType {
	case store.MsgTypeImage, store.MsgTypeVideo, store.MsgTypeFile, store.MsgTypeAudio:
		return "[" + mediaLabel(m.MsgType) + "]"
	}
	return strings.TrimSpace(m.Content)
}

func mediaLabel(msgType string) string {
	switch msgType {
	case store.MsgTypeImage:
		return "图片"
	case store.MsgTypeVideo:
		return "视频"
	case store.MsgTypeFile:
		return "文件"
	case store.MsgTypeAudio:
		return "语音"
	}
	return "附件"
}

// renderSystemPrompt 渲染系统提示词模板的占位符。
func renderSystemPrompt(template string, t *store.Ticket) string {
	replacer := strings.NewReplacer(
		"{type_name}", typeNameOrEmpty(t),
		"{ticket_no}", t.No,
		"{user_name}", t.UserName,
	)
	return replacer.Replace(template)
}

func typeNameOrEmpty(t *store.Ticket) string {
	if t.TypeName == "" {
		return store.UnnamedTypeLabel
	}
	return t.TypeName
}

// ---------------------------------------------------------------------------
// 模型输出的容错解析
// ---------------------------------------------------------------------------

// replyJSON 是自动应答请求的期望输出结构。
type replyJSON struct {
	Reply         string `json:"reply"`
	SuggestedType string `json:"suggested_type"`
	TypeReason    string `json:"type_reason"`
}

// parseReplyJSON 解析自动应答的模型输出。
//
// 容错策略：模型可能忽略 JSON 指令、包一层 ```json 代码块、或在 JSON 前后
// 加说明文字。先尝试提取首个平衡的 JSON 对象；解析成功且含 reply 字段就用
// 结构化结果，否则把整段输出当作回复正文——**回复主链路绝不能因解析失败而中断**。
func parseReplyJSON(content string) (string, *Classification) {
	raw := extractJSONObject(content)
	if raw != "" {
		var parsed replyJSON
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil && strings.TrimSpace(parsed.Reply) != "" {
			var classification *Classification
			if strings.TrimSpace(parsed.SuggestedType) != "" {
				classification = &Classification{
					Type:   strings.TrimSpace(parsed.SuggestedType),
					Reason: strings.TrimSpace(parsed.TypeReason),
				}
			}
			return parsed.Reply, classification
		}
	}
	return content, nil
}

// parseClassificationJSON 解析分类请求的模型输出。
func parseClassificationJSON(content string) *Classification {
	raw := extractJSONObject(content)
	if raw == "" {
		return nil
	}
	var parsed Classification
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	parsed.Type = strings.TrimSpace(parsed.Type)
	parsed.Reason = strings.TrimSpace(parsed.Reason)
	if parsed.Type == "" {
		return nil
	}
	return &parsed
}

// extractJSONObject 提取文本中第一个平衡的花括号对象（容忍代码块围栏与前后缀）。
func extractJSONObject(content string) string {
	content = strings.TrimSpace(content)
	// 去掉 Markdown 代码块围栏。
	if strings.HasPrefix(content, "```") {
		if idx := strings.Index(content, "\n"); idx >= 0 {
			content = content[idx+1:]
		}
		content = strings.TrimSuffix(strings.TrimSpace(content), "```")
		content = strings.TrimSpace(content)
	}

	start := strings.Index(content, "{")
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(content); i++ {
		c := content[i]
		if escaped {
			escaped = false
			continue
		}
		switch c {
		case '\\':
			if inString {
				escaped = true
			}
		case '"':
			inString = !inString
		case '{':
			if !inString {
				depth++
			}
		case '}':
			if !inString {
				depth--
				if depth == 0 {
					return content[start : i+1]
				}
			}
		}
	}
	return ""
}
