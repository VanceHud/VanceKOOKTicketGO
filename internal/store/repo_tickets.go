package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"vancekookticket/internal/ticketno"
)

// TicketsRepo 负责工单、消息、备注与统计。
type TicketsRepo struct {
	db *gorm.DB
}

// AllocateAttempts 是编号冲突重试次数；超过该次数仍冲突时自动加宽随机段。
const AllocateAttempts = 5

// CreateWithNo 为工单分配编号并写入占位记录。
//
// 冲突处理：随机段落在同一天同一值时会触发唯一索引冲突（gorm.ErrDuplicatedKey），
// 前 3 次重试仍用 4 位随机段，之后自动加宽到 5 位，避免大流量下反复失败。
func (r *TicketsRepo) CreateWithNo(t *Ticket, now time.Time, loc *time.Location) error {
	now = now.UTC()
	if t.StartedAt.IsZero() {
		t.StartedAt = now
	} else {
		t.StartedAt = t.StartedAt.UTC()
	}
	if t.Status == "" {
		t.Status = TicketPending
	}

	var lastErr error
	for attempt := 0; attempt < AllocateAttempts; attempt++ {
		var (
			no  string
			err error
		)
		if attempt >= 3 {
			no, err = ticketno.NewWide(now, loc)
		} else {
			no, err = ticketno.New(now, loc)
		}
		if err != nil {
			return fmt.Errorf("生成工单编号失败: %w", err)
		}
		t.No = no
		t.CreatedAt, t.UpdatedAt = now, now

		err = r.db.Create(t).Error
		if err == nil {
			return nil
		}
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			lastErr = err
			continue
		}
		return err
	}
	return fmt.Errorf("生成工单编号失败：连续 %d 次冲突: %w", AllocateAttempts, lastErr)
}

// ByNo 按工单编号查询。
func (r *TicketsRepo) ByNo(no string) (*Ticket, error) {
	var t Ticket
	if err := r.db.Where("no = ?", no).First(&t).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &t, nil
}

// ByChannel 按工单频道查询（消息归档需要）。
func (r *TicketsRepo) ByChannel(channelID string) (*Ticket, error) {
	if channelID == "" {
		return nil, ErrNotFound
	}
	var t Ticket
	if err := r.db.Where("channel_id = ?", channelID).Order("id DESC").First(&t).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &t, nil
}

// ActiveByUser 查询某用户尚未关闭的工单，用于“一人一单”限制。
func (r *TicketsRepo) ActiveByUser(userID string) (*Ticket, error) {
	var t Ticket
	err := r.db.Where("user_id = ? AND status IN ?", userID, []string{TicketPending, TicketOpen, TicketLocked}).
		Order("id DESC").First(&t).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &t, nil
}

// UpdateFields 局部更新工单字段。
func (r *TicketsRepo) UpdateFields(no string, fields map[string]any) error {
	if _, ok := fields["updated_at"]; !ok {
		fields["updated_at"] = Now()
	}
	return r.db.Model(&Ticket{}).Where("no = ?", no).Updates(normalizeTimes(fields)).Error
}

// normalizeTimes 把 map 里的时间字段统一转成 UTC。
//
// 必须做这一步：SQLite 没有原生时间类型，时间以文本存储并按字典序比较。
// 若写入的是带 +08:00 偏移的本地时间，它与 UTC 边界（统计的“今日/昨日”等）
// 比较时会整体偏移一个时区，虽然文本看起来“更晚”，实际却可能是昨天。
func normalizeTimes(fields map[string]any) map[string]any {
	for key, value := range fields {
		switch v := value.(type) {
		case time.Time:
			fields[key] = v.UTC()
		case *time.Time:
			if v != nil {
				utc := v.UTC()
				fields[key] = &utc
			}
		}
	}
	return fields
}

// Delete 删除工单记录（仅用于占号回收：建频道失败且编号未对外暴露）。// 已对外暴露的工单不允许删除，避免审计链断裂。
func (r *TicketsRepo) Delete(no string) error {
	return r.db.Where("no = ? AND status = ?", no, TicketPending).Delete(&Ticket{}).Error
}

// TicketFilter 是工单列表查询条件。
type TicketFilter struct {
	Statuses []string
	// Query 支持编号前缀、用户昵称、用户 ID 三种匹配。
	Query string
	From  *time.Time
	To    *time.Time
	Page  int
	// PageSize 会被限制在 MaxPageSize 以内。
	PageSize int
}

// 分页上限，防止单次请求拉取过多数据。
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// NormalizePageSize 把请求的分页大小收敛到允许区间。
func NormalizePageSize(size int) int {
	if size <= 0 {
		return DefaultPageSize
	}
	if size > MaxPageSize {
		return MaxPageSize
	}
	return size
}

// List 按条件分页查询工单，返回结果与总数。
func (r *TicketsRepo) List(f TicketFilter) ([]Ticket, int64, error) {
	q := r.db.Model(&Ticket{})

	if len(f.Statuses) > 0 {
		q = q.Where("status IN ?", f.Statuses)
	}
	if term := strings.TrimSpace(f.Query); term != "" {
		prefix := escapeLike(term) + "%"
		contains := "%" + escapeLike(term) + "%"
		// 关键词同时匹配编号前缀、用户昵称、用户 ID，以及聊天内容。
		// 聊天内容用 EXISTS 子查询（表上有 ticket_no 索引），避免 JOIN 造成的行放大。
		q = q.Where(
			`no LIKE ? ESCAPE '\' OR user_name LIKE ? ESCAPE '\' OR user_id = ?`+
				` OR EXISTS (SELECT 1 FROM ticket_messages m WHERE m.ticket_no = tickets.no AND m.content LIKE ? ESCAPE '\')`,
			prefix, contains, term, contains,
		)
	}
	if f.From != nil {
		q = q.Where("started_at >= ?", f.From.UTC())
	}
	if f.To != nil {
		q = q.Where("started_at < ?", f.To.UTC())
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	page := f.Page
	if page < 1 {
		page = 1
	}
	size := NormalizePageSize(f.PageSize)

	var items []Ticket
	err := q.Order("started_at DESC, id DESC").
		Offset((page - 1) * size).Limit(size).
		Find(&items).Error
	return items, total, err
}

// escapeLike 转义 LIKE 通配符，避免用户输入的 % 与 _ 变成通配匹配。
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Messages 按时间顺序返回工单消息。
func (r *TicketsRepo) Messages(no string, limit, offset int) ([]TicketMessage, error) {
	if limit <= 0 || limit > 2000 {
		limit = 2000
	}
	var msgs []TicketMessage
	err := r.db.Where("ticket_no = ?", no).
		Order("created_at ASC, id ASC").
		Limit(limit).Offset(offset).Find(&msgs).Error
	return msgs, err
}

// MaxExportMessages 是导出聊天记录时的消息条数上限。
const MaxExportMessages = 20000

// AddMessage 写入一条工单消息，并维护工单的消息数与首次响应时间。
//
// 首次响应时间定义：首条既非开单人、也非机器人发送的消息时间（即人工客服的第一次回复）。
func (r *TicketsRepo) AddMessage(m *TicketMessage) error {
	m.CreatedAt = m.CreatedAt.UTC()
	if m.MsgType == "" {
		m.MsgType = MsgTypeUnknown
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		var ticket Ticket
		if err := tx.Where("no = ?", m.TicketNo).First(&ticket).Error; err != nil {
			return mapNotFound(err)
		}
		if err := tx.Create(m).Error; err != nil {
			return err
		}

		fields := map[string]any{
			"message_count": ticket.MessageCount + 1,
			"updated_at":    Now(),
		}
		if ticket.FirstReplyAt == nil && !m.IsBot && m.UserID != "" && m.UserID != ticket.UserID {
			replyAt := m.CreatedAt
			fields["first_reply_at"] = replyAt
		}
		return tx.Model(&Ticket{}).Where("no = ?", m.TicketNo).Updates(fields).Error
	})
}

// MessagePatch 描述对已归档消息的补全内容；空字符串字段不会被写入。
type MessagePatch struct {
	Content   string
	MediaURL  string
	MediaName string
	MediaType string
	CardJSON  string
}

// UpdateMessageRich 补全已归档消息的富内容（媒体地址、卡片 JSON 等）。
//
// 卡片消息的事件推送不带内容，需要异步调用 message/view 后回填；
// 回填失败不影响原始记录，因此这里的错误只需记录日志。
// 返回值 changed 表示是否确实写入了字段。
func (r *TicketsRepo) UpdateMessageRich(id uint, patch MessagePatch) (bool, error) {
	fields := map[string]any{}
	if patch.Content != "" {
		fields["content"] = patch.Content
	}
	if patch.MediaURL != "" {
		fields["media_url"] = patch.MediaURL
	}
	if patch.MediaName != "" {
		fields["media_name"] = patch.MediaName
	}
	if patch.MediaType != "" {
		fields["media_type"] = patch.MediaType
	}
	if patch.CardJSON != "" {
		fields["card_json"] = patch.CardJSON
	}
	if len(fields) == 0 {
		return false, nil
	}
	result := r.db.Model(&TicketMessage{}).Where("id = ?", id).Updates(fields)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// MessageByID 按主键返回一条消息（用于补全后推送 SSE 事件）。
func (r *TicketsRepo) MessageByID(id uint) (*TicketMessage, error) {
	var msg TicketMessage
	if err := r.db.Where("id = ?", id).First(&msg).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &msg, nil
}

// Notes 返回工单备注，按时间正序。
func (r *TicketsRepo) Notes(no string) ([]TicketNote, error) {
	var notes []TicketNote
	err := r.db.Where("ticket_no = ?", no).Order("created_at ASC, id ASC").Find(&notes).Error
	return notes, err
}

// AddNote 写入工单备注。
func (r *TicketsRepo) AddNote(n *TicketNote) error {
	n.CreatedAt = n.CreatedAt.UTC()
	return r.db.Create(n).Error
}

// TrendPoint 是按天聚合的工单趋势点。
type TrendPoint struct {
	// Date 为 loc 时区下的 YYYY-MM-DD。
	Date   string `json:"date"`
	Opened int64  `json:"opened"`
	Closed int64  `json:"closed"`
}

// Overview 是仪表盘统计数据。
type Overview struct {
	GeneratedAt          time.Time        `json:"generatedAt"`
	RangeDays            int              `json:"rangeDays"`
	Total                int64            `json:"total"`
	Active               int64            `json:"active"`
	Open                 int64            `json:"open"`
	Locked               int64            `json:"locked"`
	Closed               int64            `json:"closed"`
	OpenedToday          int64            `json:"openedToday"`
	ClosedToday          int64            `json:"closedToday"`
	UniqueUsers          int64            `json:"uniqueUsers"`
	AvgFirstReplySeconds float64          `json:"avgFirstReplySeconds"`
	AvgResolutionSeconds float64          `json:"avgResolutionSeconds"`
	Trend                []TrendPoint     `json:"trend"`
	StatusCounts         map[string]int64 `json:"statusCounts"`
}

// maxStatRows 限制统计扫描的行数，避免极端数据量下内存膨胀。
const maxStatRows = 50000

// Overview 汇总仪表盘所需的全部指标。
// 统计在 Go 侧按天分桶，避免依赖 SQLite 的日期函数（驱动以文本存储时间）。
func (r *TicketsRepo) Overview(now time.Time, loc *time.Location, days int) (*Overview, error) {
	if loc == nil {
		loc = time.UTC
	}
	if days < 1 {
		days = 7
	}
	if days > 365 {
		days = 365
	}
	now = now.UTC()

	// 以 loc 的 00:00 为基准切分自然日。
	localNow := now.In(loc)
	todayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
	rangeStart := todayStart.AddDate(0, 0, -(days - 1))

	ov := &Overview{
		GeneratedAt:  now,
		RangeDays:    days,
		StatusCounts: map[string]int64{},
		Trend:        make([]TrendPoint, 0, days),
	}

	if err := r.db.Model(&Ticket{}).Count(&ov.Total).Error; err != nil {
		return nil, err
	}
	if err := r.db.Model(&Ticket{}).
		Where("status IN ?", []string{TicketPending, TicketOpen, TicketLocked}).
		Count(&ov.Active).Error; err != nil {
		return nil, err
	}
	if err := r.db.Model(&Ticket{}).Where("status = ?", TicketOpen).Count(&ov.Open).Error; err != nil {
		return nil, err
	}
	if err := r.db.Model(&Ticket{}).Where("status = ?", TicketLocked).Count(&ov.Locked).Error; err != nil {
		return nil, err
	}
	if err := r.db.Model(&Ticket{}).Where("status = ?", TicketClosed).Count(&ov.Closed).Error; err != nil {
		return nil, err
	}
	if err := r.db.Model(&Ticket{}).Where("started_at >= ?", todayStart.UTC()).Count(&ov.OpenedToday).Error; err != nil {
		return nil, err
	}
	if err := r.db.Model(&Ticket{}).Where("closed_at >= ?", todayStart.UTC()).Count(&ov.ClosedToday).Error; err != nil {
		return nil, err
	}
	if err := r.db.Model(&Ticket{}).
		Select("COUNT(DISTINCT user_id)").Scan(&ov.UniqueUsers).Error; err != nil {
		return nil, err
	}

	// 状态分布是全局口径（不限统计区间），与 Total/Open/Closed 保持一致；
	// 按区间统计的部分是趋势图。
	type statusRow struct {
		Status string
		Count  int64
	}
	var statusRows []statusRow
	if err := r.db.Model(&Ticket{}).
		Select("status, COUNT(*) AS count").Group("status").Find(&statusRows).Error; err != nil {
		return nil, err
	}
	for _, row := range statusRows {
		ov.StatusCounts[row.Status] = row.Count
	}

	type statRow struct {
		StartedAt    time.Time
		ClosedAt     *time.Time
		FirstReplyAt *time.Time
	}
	var rows []statRow
	err := r.db.Model(&Ticket{}).
		Select("started_at, closed_at, first_reply_at").
		Where("started_at >= ? OR closed_at >= ?", rangeStart.UTC(), rangeStart.UTC()).
		Limit(maxStatRows).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	type bucket struct{ opened, closed int64 }
	buckets := make(map[string]*bucket, days)
	order := make([]string, 0, days)
	for i := 0; i < days; i++ {
		key := rangeStart.AddDate(0, 0, i).Format("2006-01-02")
		buckets[key] = &bucket{}
		order = append(order, key)
	}

	var replySum, resolutionSum float64
	var replyCount, resolutionCount int64

	for _, row := range rows {
		if !row.StartedAt.IsZero() && !row.StartedAt.Before(rangeStart.UTC()) {
			if b, ok := buckets[row.StartedAt.In(loc).Format("2006-01-02")]; ok {
				b.opened++
			}
		}
		if row.ClosedAt != nil && !row.ClosedAt.Before(rangeStart.UTC()) {
			if b, ok := buckets[row.ClosedAt.In(loc).Format("2006-01-02")]; ok {
				b.closed++
			}
			if resolution := row.ClosedAt.Sub(row.StartedAt).Seconds(); resolution >= 0 {
				resolutionSum += resolution
				resolutionCount++
			}
		}
		if row.FirstReplyAt != nil && !row.FirstReplyAt.Before(rangeStart.UTC()) {
			if reply := row.FirstReplyAt.Sub(row.StartedAt).Seconds(); reply >= 0 {
				replySum += reply
				replyCount++
			}
		}
	}

	for _, key := range order {
		b := buckets[key]
		ov.Trend = append(ov.Trend, TrendPoint{Date: key, Opened: b.opened, Closed: b.closed})
	}
	if replyCount > 0 {
		ov.AvgFirstReplySeconds = replySum / float64(replyCount)
	}
	if resolutionCount > 0 {
		ov.AvgResolutionSeconds = resolutionSum / float64(resolutionCount)
	}
	return ov, nil
}
