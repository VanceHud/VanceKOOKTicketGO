package ai

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// captureKnowledge 把一条人工回复连同它回答的用户问题写入知识库（worker 内执行）。
//
// 问题段 = 该回复之前、开单人的连续消息（遇到人工/AI/系统消息即止）；
// 若紧邻的上一条非开单人消息是 AI 回复，标记 IsCorrection——
// 人工在 AI 回复之后给出了不同答案，是最直接的学习信号，检索时加权。
func (s *Service) captureKnowledge(ctx context.Context, no string, staffMsg *store.TicketMessage) {
	if staffMsg == nil || strings.TrimSpace(staffMsg.Content) == "" {
		return
	}
	t, err := s.ticketByNo(no)
	if err != nil {
		return
	}

	messages, err := s.deps.Store.Tickets.MessagesTail(no, 100)
	if err != nil {
		s.deps.Logger.Warn("读取工单消息失败（知识捕获）", "ticket_no", no, "err", err)
		return
	}

	// 定位触发消息在时间线中的位置（按主键匹配；重放场景下消息已入库）。
	index := -1
	for i := range messages {
		if messages[i].ID == staffMsg.ID {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}

	// 向前收集开单人的连续消息。
	//
	// 边界规则：人工客服的消息是问题段的终点；AI / 系统消息被跳过
	// （「用户提问 → AI 回复 → 人工纠正」是最有价值的知识，
	// 问题段必须穿过 AI 回复去拿用户的原始提问）；遇到 AI 回复标记纠正。
	questions := make([]string, 0, 4)
	sawAI := false
	for i := index - 1; i >= 0; i-- {
		m := messages[i]
		if m.IsBot {
			if m.IsAI {
				sawAI = true
			}
			continue
		}
		if m.UserID != t.UserID {
			break // 另一位人工客服的回复：到此为止
		}
		if content := strings.TrimSpace(m.Content); content != "" {
			questions = append([]string{content}, questions...)
		}
	}
	if len(questions) == 0 {
		return
	}

	entry := &store.AIKnowledge{
		Question:     truncateRunes(strings.Join(questions, "\n"), 2000),
		Answer:       truncateRunes(strings.TrimSpace(staffMsg.Content), 2000),
		TicketNo:     no,
		AnswerMsgID:  staffMsg.MsgID,
		IsCorrection: sawAI,
		Enabled:      true,
	}
	// 平台消息 ID 缺失（理论不应发生）时退化为内部定位键，保证幂等。
	if entry.AnswerMsgID == "" {
		entry.AnswerMsgID = "db-" + strconv.FormatUint(uint64(staffMsg.ID), 10)
	}

	created, err := s.deps.Store.AI.CaptureKnowledge(entry)
	if err != nil {
		s.deps.Logger.Warn("捕获知识失败", "ticket_no", no, "err", err)
		return
	}
	if !created {
		return // 已捕获过
	}
	s.deps.Logger.Info("已从人工回复捕获知识", "ticket_no", no, "correction", sawAI)

	// 配置了 embedding 模型时补全向量（检索质量的关键，失败只降级为关键词检索）。
	s.embedEntry(ctx, entry)
}

// embedEntry 为知识条目生成并保存向量。
func (s *Service) embedEntry(ctx context.Context, entry *store.AIKnowledge) {
	cfg, err := s.config()
	if err != nil || cfg.EmbeddingModel == "" {
		return
	}
	client, err := s.clientFor(cfg)
	if err != nil {
		return
	}
	vectors, err := client.Embed(ctx, []string{entry.Question}, cfg.EmbeddingModel)
	if err != nil || len(vectors) != 1 {
		s.deps.Logger.Warn("生成知识向量失败（退化为关键词检索）", "ticket_no", entry.TicketNo, "err", err)
		return
	}
	if err := s.deps.Store.AI.KnowledgeSetEmbedding(entry.ID, vectors[0]); err != nil {
		s.deps.Logger.Warn("保存知识向量失败", "knowledge_id", entry.ID, "err", err)
	}
}

// knowledgeHit 是一次检索命中。
type knowledgeHit struct {
	entry store.AIKnowledge
	score float64
}

// retrieveKnowledge 按查询检索 TopK 条启用中的知识。
//
// 配置了 embedding 模型：查询与条目都向量化后按余弦相似度排序，
// 缺向量的存量条目在同一次调用里批量补齐（自愈，避免「先建库后配嵌入」留下永久退化）；
// 未配置：退化为关键词计分（查询按行拆词，命中问题或答案即计分）。
// 纠正类条目（IsCorrection）加权 1.25 倍——人工纠正过 AI 的答案优先级最高。
func (s *Service) retrieveKnowledge(ctx context.Context, cfg *store.AISettings, query string) []store.AIKnowledge {
	entries, err := s.deps.Store.AI.KnowledgeEnabled()
	if err != nil {
		s.deps.Logger.Warn("读取知识库失败", "err", err)
		return nil
	}
	if len(entries) == 0 || strings.TrimSpace(query) == "" {
		return nil
	}

	var hits []knowledgeHit
	if cfg.EmbeddingModel != "" {
		hits = s.retrieveByEmbedding(ctx, cfg, entries, query)
	} else {
		hits = retrieveByKeyword(entries, query)
	}

	// 取 TopK（通常 ≤ 5，选择排序足够）。
	topK := cfg.KnowledgeTopK
	if topK > len(hits) {
		topK = len(hits)
	}
	for i := 0; i < topK; i++ {
		best := i
		for j := i + 1; j < len(hits); j++ {
			if hits[j].score > hits[best].score {
				best = j
			}
		}
		hits[i], hits[best] = hits[best], hits[i]
	}
	out := make([]store.AIKnowledge, 0, topK)
	for _, hit := range hits[:topK] {
		if hit.score <= 0 {
			break
		}
		out = append(out, hit.entry)
	}
	return out
}

// retrieveByEmbedding 用向量相似度检索，并顺手补齐缺失的条目向量。
func (s *Service) retrieveByEmbedding(ctx context.Context, cfg *store.AISettings, entries []store.AIKnowledge, query string) []knowledgeHit {
	client, err := s.clientFor(cfg)
	if err != nil {
		return retrieveByKeyword(entries, query)
	}

	// 收集缺向量的条目，与查询一起批量向量化。
	texts := []string{query}
	missing := make([]int, 0, len(entries))
	for i := range entries {
		if strings.TrimSpace(entries[i].Embedding) == "" {
			missing = append(missing, i)
			texts = append(texts, entries[i].Question)
		}
	}
	vectors, err := client.Embed(ctx, texts, cfg.EmbeddingModel)
	if err != nil || len(vectors) != len(texts) {
		s.deps.Logger.Warn("查询向量化失败（退化为关键词检索）", "err", err)
		return retrieveByKeyword(entries, query)
	}

	// 每个条目的最终向量：刚补齐的用本次结果，其余解析存量 JSON。
	vectorFor := make([][]float32, len(entries))
	for k, index := range missing {
		vectorFor[index] = vectors[k+1]
		if err := s.deps.Store.AI.KnowledgeSetEmbedding(entries[index].ID, vectors[k+1]); err != nil {
			s.deps.Logger.Warn("补齐知识向量失败", "knowledge_id", entries[index].ID, "err", err)
		}
	}
	for i := range entries {
		if vectorFor[i] == nil {
			vectorFor[i] = decodeVector(entries[i].Embedding)
		}
	}

	hits := make([]knowledgeHit, 0, len(entries))
	for i := range entries {
		if len(vectorFor[i]) == 0 {
			continue
		}
		score := cosine(vectors[0], vectorFor[i])
		if entries[i].IsCorrection {
			score *= 1.25
		}
		hits = append(hits, knowledgeHit{entry: entries[i], score: score})
	}
	return hits
}

// retrieveByKeyword 用关键词计分检索（无 embedding 配置时的兜底）。
func retrieveByKeyword(entries []store.AIKnowledge, query string) []knowledgeHit {
	terms := splitQueryTerms(query)
	if len(terms) == 0 {
		return nil
	}
	hits := make([]knowledgeHit, 0, len(entries))
	for i := range entries {
		entry := entries[i]
		haystack := entry.Question + "\n" + entry.Answer
		var score float64
		for _, term := range terms {
			if strings.Contains(haystack, term) {
				score++
			}
		}
		if score <= 0 {
			continue
		}
		if entry.IsCorrection {
			score *= 1.25
		}
		hits = append(hits, knowledgeHit{entry: entry, score: score})
	}
	return hits
}

// splitQueryTerms 把查询拆成检索词：按换行拆分，
// 过长的行再按句读切分（中文提问常常整段无空格）。
func splitQueryTerms(query string) []string {
	var terms []string
	for _, line := range strings.Split(query, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len([]rune(line)) <= 12 {
			terms = append(terms, line)
			continue
		}
		for _, part := range strings.FieldsFunc(line, func(r rune) bool {
			return r == '。' || r == '，' || r == '？' || r == '！' || r == '；' || r == ' ' || r == ','
		}) {
			if part = strings.TrimSpace(part); part != "" {
				terms = append(terms, part)
			}
		}
	}
	return terms
}

// decodeVector 解析存储的 JSON 向量。
func decodeVector(raw string) []float32 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var vector []float32
	if err := json.Unmarshal([]byte(raw), &vector); err != nil {
		return nil
	}
	return vector
}

// cosine 计算两个向量的余弦相似度。
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// SearchKnowledge 是检索的公开入口（WebUI 知识库页「检索测试」）。
func (s *Service) SearchKnowledge(ctx context.Context, query string) ([]store.AIKnowledge, error) {
	cfg, err := s.config()
	if err != nil {
		return nil, err
	}
	return s.retrieveKnowledge(ctx, cfg, query), nil
}

// EmbedKnowledgeNow 为指定条目立即生成向量（编辑知识后调用）。
func (s *Service) EmbedKnowledgeNow(ctx context.Context, id uint) error {
	entry, err := s.deps.Store.AI.KnowledgeByID(id)
	if err != nil {
		return err
	}
	cfg, err := s.config()
	if err != nil {
		return err
	}
	if cfg.EmbeddingModel == "" {
		return errInvalidState("未配置 embedding 模型")
	}
	client, err := s.clientFor(cfg)
	if err != nil {
		return err
	}
	vectors, err := client.Embed(ctx, []string{entry.Question}, cfg.EmbeddingModel)
	if err != nil {
		return err
	}
	if len(vectors) != 1 {
		return errInvalidState("向量返回数量不符")
	}
	return s.deps.Store.AI.KnowledgeSetEmbedding(id, vectors[0])
}
