package ai

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// summaryMaxMessages 是摘要读取的消息条数上限。
//
// 比自动应答的上下文窗口更大：摘要的价值在于覆盖整段对话，
// 超长工单截断到最近 200 条已足够（更早的内容通常已被前文覆盖）。
const summaryMaxMessages = 200

// TriggerSummary 异步生成（或刷新）工单摘要：WebUI 按钮 / 关单流程调用。
func (s *Service) TriggerSummary(no string) error {
	return s.enqueue(job{kind: jobSummary, ticketNo: no})
}

// TriggerClassification 异步生成工单的类型建议。
func (s *Service) TriggerClassification(no string) error {
	return s.enqueue(job{kind: jobClassify, ticketNo: no})
}

// generateSummary 生成并保存工单摘要（worker 内执行）。
func (s *Service) generateSummary(ctx context.Context, no string) {
	cfg, err := s.config()
	if err != nil {
		s.deps.Logger.Warn("读取 AI 配置失败", "ticket_no", no, "err", err)
		return
	}
	client, err := s.clientFor(cfg)
	if err != nil {
		return
	}
	t, err := s.ticketByNo(no)
	if err != nil {
		return
	}
	messages, err := s.deps.Store.Tickets.MessagesTail(no, summaryMaxMessages)
	if err != nil {
		s.deps.Logger.Warn("读取工单消息失败（摘要）", "ticket_no", no, "err", err)
		return
	}
	if len(messages) == 0 {
		return
	}

	req := buildSummaryRequest(cfg, t, messages)
	started := time.Now()
	resp, err := client.Chat(ctx, req)
	call := &store.AICall{
		TicketNo: no, Kind: "summary", Model: cfg.Model, Error: errText(err),
	}
	if resp != nil {
		call.PromptTokens, call.CompletionTokens = resp.PromptTokens, resp.CompletionTokens
	}
	s.recordCall(cfg, call, started)
	if err != nil {
		s.deps.Logger.Warn("生成摘要失败", "ticket_no", no, "err", err)
		return
	}

	summary := strings.TrimSpace(resp.Content)
	if summary == "" {
		s.deps.Logger.Warn("摘要内容为空", "ticket_no", no)
		return
	}
	now := store.Now()
	if err := s.deps.Store.Tickets.UpdateFields(no, map[string]any{
		"ai_summary":    truncateRunes(summary, 4000),
		"ai_summary_at": now,
	}); err != nil {
		s.deps.Logger.Warn("保存摘要失败", "ticket_no", no, "err", err)
		return
	}
	s.publish(no, map[string]any{"aiSummary": summary, "aiSummaryAt": now})
	s.deps.Logger.Info("已生成工单摘要", "ticket_no", no)
}

// generateClassification 生成工单的类型建议（worker 内执行）。
func (s *Service) generateClassification(ctx context.Context, no string) {
	cfg, err := s.config()
	if err != nil {
		s.deps.Logger.Warn("读取 AI 配置失败", "ticket_no", no, "err", err)
		return
	}
	client, err := s.clientFor(cfg)
	if err != nil {
		return
	}
	t, err := s.ticketByNo(no)
	if err != nil {
		return
	}
	typeNames := s.typeNamesForClassification(t)
	if len(typeNames) == 0 {
		return
	}
	messages, err := s.deps.Store.Tickets.MessagesTail(no, cfg.ContextMessages)
	if err != nil {
		s.deps.Logger.Warn("读取工单消息失败（分类）", "ticket_no", no, "err", err)
		return
	}

	req := buildClassifyRequest(cfg, t, messages, typeNames)
	started := time.Now()
	resp, err := client.Chat(ctx, req)
	call := &store.AICall{
		TicketNo: no, Kind: "classify", Model: cfg.Model, Error: errText(err),
	}
	if resp != nil {
		call.PromptTokens, call.CompletionTokens = resp.PromptTokens, resp.CompletionTokens
	}
	s.recordCall(cfg, call, started)
	if err != nil {
		s.deps.Logger.Warn("生成分类建议失败", "ticket_no", no, "err", err)
		return
	}

	classification := parseClassificationJSON(resp.Content)
	if classification == nil || classification.Type == "" {
		s.deps.Logger.Warn("分类建议解析失败", "ticket_no", no)
		return
	}
	// 建议的类型必须真实存在，防止模型编造类型名。
	if !s.typeExists(classification.Type) {
		s.deps.Logger.Warn("分类建议的类型不存在", "ticket_no", no, "type", classification.Type)
		return
	}
	encoded, err := marshalClassification(classification)
	if err != nil {
		return
	}
	if err := s.deps.Store.Tickets.UpdateFields(no, map[string]any{"ai_classification": encoded}); err != nil {
		s.deps.Logger.Warn("保存分类建议失败", "ticket_no", no, "err", err)
		return
	}
	s.publish(no, map[string]any{"aiClassification": encoded})
	s.deps.Logger.Info("已生成工单分类建议", "ticket_no", no, "type", classification.Type)
}

// ApplyClassification 把分类建议应用到工单（WebUI「应用」按钮）。
//
// 只改类型标签（TypeID/TypeName 快照）：频道权限以开单时下发为准，不追溯变更
// ——重新按新类型下发权限需要重建频道，代价与风险都不成比例。
func (s *Service) ApplyClassification(no string, typeName string) error {
	t, err := s.ticketByNo(no)
	if err != nil {
		return err
	}
	if !t.IsActive() {
		return errInvalidState("工单已关闭，不能修改类型")
	}
	item, err := s.deps.Store.Types.ByName(typeName)
	if err != nil {
		return errInvalidState("工单类型不存在: " + typeName)
	}
	if err := s.deps.Store.Tickets.UpdateFields(no, map[string]any{
		"type_id":           item.ID,
		"type_name":         item.Name,
		"ai_classification": "",
	}); err != nil {
		return err
	}
	updated, err := s.ticketByNo(no)
	if err == nil {
		s.publish(no, updated)
	}
	return nil
}

// typeNamesForClassification 返回可用于分类建议的启用中类型名列表。
func (s *Service) typeNamesForClassification(t *store.Ticket) []string {
	types, err := s.deps.Store.Types.List()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(types))
	for _, item := range types {
		if item.Enabled {
			names = append(names, item.Name)
		}
	}
	return names
}

// typeExists 判断类型名是否存在（分类建议合法性校验）。
func (s *Service) typeExists(name string) bool {
	_, err := s.deps.Store.Types.ByName(name)
	return err == nil
}

// Classification 是 AI 的类型建议。
type Classification struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// marshalClassification 序列化建议（存入 tickets.ai_classification）。
func marshalClassification(c *Classification) (string, error) {
	encoded, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// ParseClassification 解析工单上存的分类建议 JSON（供 API 层展示）。
func ParseClassification(raw string) *Classification {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var c Classification
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil
	}
	return &c
}
