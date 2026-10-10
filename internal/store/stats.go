package store

import (
	"math"
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
	ChannelID string `json:"channelId"`
	// ChannelName 是来源频道的可读名称（由 API 层补充，查不到时为空）。
	ChannelName string  `json:"channelName,omitempty"`
	Opened      int64   `json:"opened"`
	Closed      int64   `json:"closed"`
	ClosedRate  float64 `json:"closedRate"`
}

// TypeStat 是按工单类型聚合的工单量（按开单时的类型名快照归集）。
type TypeStat struct {
	TypeName   string  `json:"typeName"`
	Opened     int64   `json:"opened"`
	Closed     int64   `json:"closed"`
	ClosedRate float64 `json:"closedRate"`
}

// Analytics 是细化统计看板所需的聚合结果。
type Analytics struct {
	GeneratedAt time.Time `json:"generatedAt"`
	RangeDays   int       `json:"rangeDays"`

	// Total / Closed 为区间内开单总量及其中当前已关闭的数量。
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
	// Types 按开单量降序
	Types []TypeStat `json:"types"`

	ArchivedMessages     int64   `json:"archivedMessages"`
	AvgMessagesPerTicket float64 `json:"avgMessagesPerTicket"`

	OpenedToday     int64 `json:"openedToday"`
	OpenedYesterday int64 `json:"openedYesterday"`
	ClosedToday     int64 `json:"closedToday"`
	ClosedYesterday int64 `json:"closedYesterday"`
}

// analyticsRow 是统计扫描的工单行（Analytics 与 Overview 共用）。
type analyticsRow struct {
	ID              uint
	Status          string
	SourceChannelID string
	TypeName        string
	ClosedByName    string
	StartedAt       time.Time
	ClosedAt        *time.Time
	FirstReplyAt    *time.Time
	MessageCount    int
}

// forEachActiveTicket 遍历「区间内有活动」的工单（started_at / closed_at /
// first_reply_at 任一不早于 rangeStart），每条工单回调恰好一次。
//
// 不能把三个条件合并成 OR：SQLite 对 OR 不做索引合并，会退化为全表扫描
// （实测 SCAN tickets，工单积累后是全系统最重的查询）。
// 拆成三条独立查询后每条都走对应列上的单列索引，代价是同一条工单可能被
// 多条查询命中，这里按主键去重；fn 内的各聚合本身都有区间下界判断，
// 因此把 rangeStart 放宽到更早（见 Analytics 对昨日对比的处理）不会影响结果。
func (r *TicketsRepo) forEachActiveTicket(rangeStart time.Time, fn func(row analyticsRow)) error {
	const cols = "id, status, source_channel_id, type_name, closed_by_name, started_at, closed_at, first_reply_at, message_count"
	seen := make(map[uint]struct{})
	for _, cond := range []string{"started_at >= ?", "closed_at >= ?", "first_reply_at >= ?"} {
		rows, err := r.db.Model(&Ticket{}).Select(cols).Where(cond, rangeStart.UTC()).Rows()
		if err != nil {
			return err
		}
		for rows.Next() {
			var row analyticsRow
			if err := r.db.ScanRows(rows, &row); err != nil {
				rows.Close()
				return err
			}
			if _, dup := seen[row.ID]; dup {
				continue
			}
			seen[row.ID] = struct{}{}
			fn(row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
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
		Types:       []TypeStat{},
	}
	for hour := 0; hour < 24; hour++ {
		result.Hourly[hour].Hour = hour
	}

	var (
		replyDurations      []float64
		resolutionDurations []float64
		closerClosed        = map[string]int64{}
		closerResolution    = map[string]float64{}
		sourceOpened        = map[string]int64{}
		sourceClosed        = map[string]int64{}
		typeOpened          = map[string]int64{}
		typeClosed          = map[string]int64{}
		messageSum          int64
	)

	// days==1 时 rangeStart 即今日零点，昨日区间落在扫描范围之外；
	// 把扫描下界放宽到昨日零点即可在循环里一并统计昨日对比
	// （各聚合都有独立的区间判断，放宽不会改变口径）。
	scanStart := rangeStart
	if yesterdayStart.Before(scanStart) {
		scanStart = yesterdayStart
	}

	err := r.forEachActiveTicket(scanStart, func(row analyticsRow) {
		// 开单量、关闭率、来源、单均消息数按同一批区间内开单计算。
		inCohort := !row.StartedAt.Before(rangeStart.UTC())
		if inCohort {
			result.Total++
			messageSum += int64(row.MessageCount)
			hour := row.StartedAt.In(loc).Hour()
			result.Hourly[hour].Opened++
			sourceOpened[row.SourceChannelID]++
			typeName := row.TypeName
			if typeName == "" {
				typeName = UnnamedTypeLabel
			}
			typeOpened[typeName]++
			if row.Status == TicketClosed {
				result.Closed++
				sourceClosed[row.SourceChannelID]++
				typeClosed[typeName]++
			}
		}
		if row.ClosedAt != nil && !row.ClosedAt.Before(rangeStart.UTC()) {
			hour := row.ClosedAt.In(loc).Hour()
			result.Hourly[hour].Closed++
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
		// 今日 / 昨日对比与主扫描合并，省掉 4 条独立 COUNT。
		if !row.StartedAt.Before(todayStart.UTC()) {
			result.OpenedToday++
		} else if !row.StartedAt.Before(yesterdayStart.UTC()) {
			result.OpenedYesterday++
		}
		if row.ClosedAt != nil {
			if !row.ClosedAt.Before(todayStart.UTC()) {
				result.ClosedToday++
			} else if !row.ClosedAt.Before(yesterdayStart.UTC()) {
				result.ClosedYesterday++
			}
		}
	})
	if err != nil {
		return nil, err
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

	for typeName, opened := range typeOpened {
		closed := typeClosed[typeName]
		stat := TypeStat{TypeName: typeName, Opened: opened, Closed: closed}
		if opened > 0 {
			stat.ClosedRate = float64(closed) / float64(opened)
		}
		result.Types = append(result.Types, stat)
	}
	sort.Slice(result.Types, func(i, j int) bool {
		if result.Types[i].Opened == result.Types[j].Opened {
			return result.Types[i].TypeName < result.Types[j].TypeName
		}
		return result.Types[i].Opened > result.Types[j].Opened
	})

	// 归档消息量（区间内）
	if err := r.db.Model(&TicketMessage{}).
		Where("created_at >= ?", rangeStart.UTC()).
		Count(&result.ArchivedMessages).Error; err != nil {
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
	// values 由本次统计独占，直接原地排序，避免再复制全部时长样本。
	sorted := values
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
	index := int(math.Ceil(ratio*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}
