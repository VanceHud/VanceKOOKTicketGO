package store

import (
	"sort"
	"time"
)

// DurationStats 是时长分位统计（单位：秒）。
type DurationStats struct {
	Count      int64   `json:"count"`
	AvgSeconds float64 `json:"avgSeconds"`
	P50Seconds float64 `json:"p50Seconds"`
	P90Seconds float64 `json:"p90Seconds"`
}

// HourBucket 是按小时（业务时区）聚合的工单量。
type HourBucket struct {
	Hour   int   `json:"hour"`
	Opened int64 `json:"opened"`
	Closed int64 `json:"closed"`
}

// CloserStat 是按关闭人聚合的处理量。
type CloserStat struct {
	Name              string  `json:"name"`
	Closed            int64   `json:"closed"`
	AvgResolutionSecs float64 `json:"avgResolutionSeconds"`
}

// SourceStat 是按来源面板频道聚合的工单量。
type SourceStat struct {
	ChannelID  string  `json:"channelId"`
	Opened     int64   `json:"opened"`
	Closed     int64   `json:"closed"`
	ClosedRate float64 `json:"closedRate"`
}

// Analytics 是细化统计看板所需的聚合结果。
type Analytics struct {
	GeneratedAt time.Time `json:"generatedAt"`
	RangeDays   int       `json:"rangeDays"`

	// Total / Closed 为区间内的工单总量与已关闭量
	Total      int64   `json:"total"`
	Closed     int64   `json:"closed"`
	ClosedRate float64 `json:"closedRate"`

	FirstReply DurationStats `json:"firstReply"`
	Resolution DurationStats `json:"resolution"`

	// Hourly 固定 24 项，便于前端直接画图（避免缺小时导致的错位）
	Hourly []HourBucket `json:"hourly"`
	// Closers 按关闭量降序，最多 10 条
	Closers []CloserStat `json:"closers"`
	// Sources 按开单量降序
	Sources []SourceStat `json:"sources"`

	ArchivedMessages     int64   `json:"archivedMessages"`
	AvgMessagesPerTicket float64 `json:"avgMessagesPerTicket"`

	OpenedToday     int64 `json:"openedToday"`
	OpenedYesterday int64 `json:"openedYesterday"`
	ClosedToday     int64 `json:"closedToday"`
	ClosedYesterday int64 `json:"closedYesterday"`
}

// Analytics 聚合细化统计。
//
// 与 Overview 的分工：
//   - Overview 服务于仪表盘首屏（今日/在办/趋势）；
//   - Analytics 服务于统计看板（分位时长、时段分布、客服处理量、面板来源）。
//
// 所有分桶都按业务时区（TICKET_TZ）计算，保证运营看到的“今天/几点”符合直觉。
func (r *TicketsRepo) Analytics(now time.Time, loc *time.Location, days int) (*Analytics, error) {
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

	localNow := now.In(loc)
	todayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
	yesterdayStart := todayStart.AddDate(0, 0, -1)
	rangeStart := todayStart.AddDate(0, 0, -(days - 1))

	result := &Analytics{
		GeneratedAt: now,
		RangeDays:   days,
		Hourly:      make([]HourBucket, 24),
		Closers:     []CloserStat{},
		Sources:     []SourceStat{},
	}
	for hour := 0; hour < 24; hour++ {
		result.Hourly[hour].Hour = hour
	}

	type analyticsRow struct {
		Status          string
		SourceChannelID string
		ClosedByName    string
		StartedAt       time.Time
		ClosedAt        *time.Time
		FirstReplyAt    *time.Time
		MessageCount    int
	}

	var rows []analyticsRow
	err := r.db.Model(&Ticket{}).
		Select("status, source_channel_id, closed_by_name, started_at, closed_at, first_reply_at, message_count").
		Where("started_at >= ? OR closed_at >= ?", rangeStart.UTC(), rangeStart.UTC()).
		Limit(maxStatRows).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	var (
		replyDurations      []float64
		resolutionDurations []float64
		closerClosed        = map[string]int64{}
		closerResolution    = map[string]float64{}
		sourceOpened        = map[string]int64{}
		sourceClosed        = map[string]int64{}
		messageSum          int64
	)

	for _, row := range rows {
		result.Total++
		messageSum += int64(row.MessageCount)

		if !row.StartedAt.Before(rangeStart.UTC()) {
			hour := row.StartedAt.In(loc).Hour()
			result.Hourly[hour].Opened++
			sourceOpened[row.SourceChannelID]++
		}
		if row.Status == TicketClosed {
			result.Closed++
		}
		if row.ClosedAt != nil && !row.ClosedAt.Before(rangeStart.UTC()) {
			hour := row.ClosedAt.In(loc).Hour()
			result.Hourly[hour].Closed++
			if row.SourceChannelID != "" {
				sourceClosed[row.SourceChannelID]++
			}
			if resolution := row.ClosedAt.Sub(row.StartedAt).Seconds(); resolution >= 0 {
				resolutionDurations = append(resolutionDurations, resolution)
				name := row.ClosedByName
				if name == "" {
					name = "(未记录)"
				}
				closerClosed[name]++
				closerResolution[name] += resolution
			}
		}
		if row.FirstReplyAt != nil && !row.FirstReplyAt.Before(rangeStart.UTC()) {
			if reply := row.FirstReplyAt.Sub(row.StartedAt).Seconds(); reply >= 0 {
				replyDurations = append(replyDurations, reply)
			}
		}
	}

	result.FirstReply = summarizeDurations(replyDurations)
	result.Resolution = summarizeDurations(resolutionDurations)
	if result.Total > 0 {
		result.ClosedRate = float64(result.Closed) / float64(result.Total)
		result.AvgMessagesPerTicket = float64(messageSum) / float64(result.Total)
	}

	for name, count := range closerClosed {
		stat := CloserStat{Name: name, Closed: count}
		if count > 0 {
			stat.AvgResolutionSecs = closerResolution[name] / float64(count)
		}
		result.Closers = append(result.Closers, stat)
	}
	sort.Slice(result.Closers, func(i, j int) bool {
		if result.Closers[i].Closed == result.Closers[j].Closed {
			return result.Closers[i].Name < result.Closers[j].Name
		}
		return result.Closers[i].Closed > result.Closers[j].Closed
	})
	if len(result.Closers) > 10 {
		result.Closers = result.Closers[:10]
	}

	for channelID, opened := range sourceOpened {
		closed := sourceClosed[channelID]
		stat := SourceStat{ChannelID: channelID, Opened: opened, Closed: closed}
		if opened > 0 {
			stat.ClosedRate = float64(closed) / float64(opened)
		}
		result.Sources = append(result.Sources, stat)
	}
	sort.Slice(result.Sources, func(i, j int) bool {
		if result.Sources[i].Opened == result.Sources[j].Opened {
			return result.Sources[i].ChannelID < result.Sources[j].ChannelID
		}
		return result.Sources[i].Opened > result.Sources[j].Opened
	})

	// 归档消息量（区间内）
	if err := r.db.Model(&TicketMessage{}).
		Where("created_at >= ?", rangeStart.UTC()).
		Count(&result.ArchivedMessages).Error; err != nil {
		return nil, err
	}

	// 今日 / 昨日对比
	if err := r.db.Model(&Ticket{}).Where("started_at >= ?", todayStart.UTC()).Count(&result.OpenedToday).Error; err != nil {
		return nil, err
	}
	if err := r.db.Model(&Ticket{}).
		Where("started_at >= ? AND started_at < ?", yesterdayStart.UTC(), todayStart.UTC()).
		Count(&result.OpenedYesterday).Error; err != nil {
		return nil, err
	}
	if err := r.db.Model(&Ticket{}).Where("closed_at >= ?", todayStart.UTC()).Count(&result.ClosedToday).Error; err != nil {
		return nil, err
	}
	if err := r.db.Model(&Ticket{}).
		Where("closed_at >= ? AND closed_at < ?", yesterdayStart.UTC(), todayStart.UTC()).
		Count(&result.ClosedYesterday).Error; err != nil {
		return nil, err
	}

	return result, nil
}

// summarizeDurations 计算平均与分位数（秒）。
func summarizeDurations(values []float64) DurationStats {
	stats := DurationStats{Count: int64(len(values))}
	if len(values) == 0 {
		return stats
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)

	var sum float64
	for _, value := range sorted {
		sum += value
	}
	stats.AvgSeconds = sum / float64(len(sorted))
	stats.P50Seconds = percentile(sorted, 0.5)
	stats.P90Seconds = percentile(sorted, 0.9)
	return stats
}

// percentile 使用最接近秩法取分位数，样本很少时也能给出稳定结果。
func percentile(sorted []float64, ratio float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	index := int(ratio*float64(len(sorted))+0.5) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}
