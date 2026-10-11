package ai

import (
	"context"
	"strings"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// OnTicketOpened 在工单激活后初始化 AI 状态。
//
// 类型开了自动应答且全局启用时置为 active；否则保持空（未启用）。
// 已存在的工单在管理员后来开启类型开关时，由 OnUserMessage 懒初始化补上。
func (s *Service) OnTicketOpened(ctx context.Context, t *store.Ticket) {
	if t.TypeID == nil {
		return
	}
	cfg, err := s.config()
	if err != nil || !cfg.Enabled {
		return
	}
	if !s.typeAIReplyEnabled(*t.TypeID) {
		return
	}
	s.setAIState(t.No, store.AITicketActive)
}

// OnUserMessage 处理工单频道内开单人的一条新消息：安排防抖计时。
//
// 调用方（bot 归档链路）保证这是开单人发的非机器人消息。
// 未启用 / 已接管的工单直接返回；AIState 为空时先做懒初始化
// （覆盖「工单创建时 AI 未开、之后管理员打开了类型开关」的场景）。
func (s *Service) OnUserMessage(ctx context.Context, t *store.Ticket, message *store.TicketMessage) {
	if t.Status != store.TicketOpen {
		return
	}
	switch t.AIState {
	case store.AITicketTakeover:
		return
	case "":
		cfg, err := s.config()
		if err != nil || !cfg.Enabled {
			return
		}
		if t.TypeID == nil || !s.typeAIReplyEnabled(*t.TypeID) {
			return
		}
		s.setAIState(t.No, store.AITicketActive)
		t.AIState = store.AITicketActive
	case store.AITicketActive:
	default:
		return
	}

	cfg, err := s.config()
	if err != nil {
		return
	}
	s.scheduleReply(t.No, time.Duration(cfg.QuietSeconds)*time.Second)
}

// OnStaffMessage 处理工单频道内人工客服的一条新消息：接管 + 触发知识捕获。
//
// isStaff 由调用方判定（bot.isTicketAdmin）；非管理员消息忽略。
// 接管是单向的：AI 停止自动回复，直到 /tkai on 或工单重新激活。
func (s *Service) OnStaffMessage(ctx context.Context, t *store.Ticket, message *store.TicketMessage, isStaff bool) {
	if !isStaff || message == nil {
		return
	}
	if t.Status != store.TicketOpen && t.Status != store.TicketLocked {
		return
	}

	if t.AIState == store.AITicketActive {
		s.cancelPending(t.No)
		s.setAIState(t.No, store.AITicketTakeover)
	}

	// 知识捕获：人工回复本身就是最宝贵的学习素材。
	if err := s.enqueue(job{kind: jobCapture, ticketNo: t.No, message: message}); err != nil {
		s.deps.Logger.Warn("知识捕获任务入队失败", "ticket_no", t.No, "err", err)
	}
}

// OnTicketReopened 在工单重新激活后重置接管状态（按类型开关决定是否恢复 AI）。
func (s *Service) OnTicketReopened(ctx context.Context, t *store.Ticket) {
	if t.AIState != store.AITicketTakeover {
		return
	}
	cfg, err := s.config()
	if err != nil || !cfg.Enabled {
		return
	}
	if t.TypeID == nil || !s.typeAIReplyEnabled(*t.TypeID) {
		return
	}
	s.setAIState(t.No, store.AITicketActive)
}

// ToggleTicket 由 /tkai 命令或 WebUI 调用：手动暂停（off）或恢复（on）某工单的 AI。
//
// 恢复要求全局与类型开关都处于开启状态。
func (s *Service) ToggleTicket(no string, enable bool) error {
	t, err := s.ticketByNo(no)
	if err != nil {
		return err
	}
	if !t.IsActive() {
		return errInvalidState("工单已关闭，不能切换 AI 状态")
	}
	if !enable {
		s.cancelPending(no)
		s.setAIState(no, store.AITicketTakeover)
		return nil
	}
	cfg, err := s.config()
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return errInvalidState("AI 全局开关未开启")
	}
	if t.TypeID == nil || !s.typeAIReplyEnabled(*t.TypeID) {
		return errInvalidState("该工单类型未开启 AI 自动应答")
	}
	s.setAIState(no, store.AITicketActive)
	return nil
}

// scheduleReply 安排（或重置）防抖计时器。
func (s *Service) scheduleReply(no string, quiet time.Duration) {
	if quiet <= 0 {
		quiet = time.Duration(store.DefaultAIQuietSeconds) * time.Second
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if timer, ok := s.pending[no]; ok {
		timer.Reset(quiet)
		return
	}
	s.pending[no] = time.AfterFunc(quiet, func() {
		s.mu.Lock()
		delete(s.pending, no)
		s.mu.Unlock()
		if err := s.enqueue(job{kind: jobReply, ticketNo: no}); err != nil && err != ErrBusy {
			s.deps.Logger.Warn("AI 回复任务入队失败", "ticket_no", no, "err", err)
		}
	})
}

// cancelPending 取消某工单的待发回复。
func (s *Service) cancelPending(no string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if timer, ok := s.pending[no]; ok {
		timer.Stop()
		delete(s.pending, no)
	}
}

// setAIState 更新工单的 AI 状态并广播（前端徽章实时刷新）。
func (s *Service) setAIState(no string, state string) {
	if err := s.deps.Store.Tickets.UpdateFields(no, map[string]any{"ai_state": state}); err != nil {
		s.deps.Logger.Warn("更新工单 AI 状态失败", "ticket_no", no, "state", state, "err", err)
		return
	}
	s.publish(no, map[string]any{"aiState": state})
}

// typeAIReplyEnabled 判断某工单类型是否开启自动应答。
func (s *Service) typeAIReplyEnabled(typeID uint) bool {
	item, err := s.deps.Store.Types.ByID(typeID)
	if err != nil {
		return false
	}
	return item.AIAutoReply
}

// generateReply 是自动应答的主流程（worker 内执行）。
func (s *Service) generateReply(ctx context.Context, no string) {
	cfg, err := s.config()
	if err != nil {
		s.deps.Logger.Warn("读取 AI 配置失败", "ticket_no", no, "err", err)
		return
	}
	client, err := s.clientFor(cfg)
	if err != nil {
		return // 未启用：静默跳过
	}

	t, err := s.ticketByNo(no)
	if err != nil {
		return
	}
	// 生成期间状态可能已变化：人工接管 / 工单关闭都放弃本次回复。
	if t.Status != store.TicketOpen || t.AIState != store.AITicketActive {
		return
	}

	// 每单回复上限护栏。
	replied, err := s.deps.Store.Tickets.CountAIMessages(no)
	if err != nil {
		s.deps.Logger.Warn("统计 AI 回复数失败", "ticket_no", no, "err", err)
		return
	}
	if replied >= int64(cfg.MaxReplies) {
		s.deps.Logger.Info("工单达到 AI 回复上限，转为等待人工", "ticket_no", no, "limit", cfg.MaxReplies)
		s.setAIState(no, store.AITicketTakeover)
		return
	}

	messages, err := s.deps.Store.Tickets.MessagesTail(no, cfg.ContextMessages)
	if err != nil {
		s.deps.Logger.Warn("读取工单消息失败", "ticket_no", no, "err", err)
		return
	}

	// 知识检索 + 类型列表（分类建议）。
	query := recentUserQuery(t, messages)
	knowledge := s.retrieveKnowledge(ctx, cfg, query)
	typeNames := s.typeNamesForClassification(t)

	req := buildReplyRequest(cfg, t, messages, knowledge, typeNames, t.AIClassification == "")

	started := time.Now()
	resp, err := client.Chat(ctx, req)
	call := &store.AICall{
		TicketNo: no, Kind: "reply", Model: cfg.Model, Error: errText(err),
	}
	if resp != nil {
		call.PromptTokens, call.CompletionTokens = resp.PromptTokens, resp.CompletionTokens
	}
	s.recordCall(cfg, call, started)
	if err != nil {
		s.deps.Logger.Warn("生成 AI 回复失败", "ticket_no", no, "err", err)
		return
	}

	reply, classification := parseReplyJSON(resp.Content)
	reply = strings.TrimSpace(reply)
	if reply == "" {
		s.deps.Logger.Warn("AI 回复内容为空，跳过发送", "ticket_no", no)
		return
	}

	// 分类建议（best-effort，失败不影响回复）。
	if classification != nil && t.AIClassification == "" {
		if encoded, err := marshalClassification(classification); err == nil {
			if err := s.deps.Store.Tickets.UpdateFields(no, map[string]any{"ai_classification": encoded}); err != nil {
				s.deps.Logger.Warn("保存 AI 分类建议失败", "ticket_no", no, "err", err)
			} else {
				s.publish(no, map[string]any{"aiClassification": encoded})
			}
		}
	}

	// 发送（含可配置尾注）。
	content := reply
	if footer := strings.TrimSpace(cfg.Footer); footer != "" {
		content = reply + "\n\n" + footer
	}
	s.senderMu.RLock()
	sender := s.sender
	s.senderMu.RUnlock()
	if sender == nil {
		s.deps.Logger.Warn("AI 回复发送通道未就绪（机器人未连接），已跳过", "ticket_no", no)
		return
	}
	if err := sender.SendAIReply(ctx, t, content); err != nil {
		s.deps.Logger.Warn("发送 AI 回复失败", "ticket_no", no, "err", err)
		return
	}
	s.deps.Logger.Info("AI 已回复工单", "ticket_no", no, "reply_no", replied+1)
}

// recentUserQuery 拼接最近的开单人消息作为知识检索查询。
func recentUserQuery(t *store.Ticket, messages []store.TicketMessage) string {
	parts := make([]string, 0, 4)
	for i := len(messages) - 1; i >= 0 && len(parts) < 4; i-- {
		m := messages[i]
		if m.IsBot || m.UserID != t.UserID || strings.TrimSpace(m.Content) == "" {
			continue
		}
		parts = append([]string{strings.TrimSpace(m.Content)}, parts...)
	}
	return strings.Join(parts, "\n")
}
