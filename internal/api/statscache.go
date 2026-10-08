package api

import (
	"sync"
	"time"
)

// statsCacheTTL 是统计聚合结果的内存缓存时长。
//
// Overview/Analytics 需要扫描统计区间内的全部工单并做分桶与分位计算，
// 前端多个入口（仪表盘、统计看板、命令面板）会在相近时刻请求同一份数据。
// 缓存 10 秒把重复聚合压成一次，看板最多滞后 10 秒——与前端自身的
// staleTime（15s）同一量级，用户无感知。
const statsCacheTTL = 10 * time.Second

// cachedStats 按 key 缓存统计结果（key 例如 "days=30"）。
//
// 计算在锁内进行：并发请求只会有一个真正执行聚合，其余等待并复用结果，
// 避免事件风暴时同一份全表扫描被并行执行多次。
type cachedStats[T any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]cachedStatsEntry[T]
}

type cachedStatsEntry[T any] struct {
	value   T
	expires time.Time
}

func newCachedStats[T any](ttl time.Duration) *cachedStats[T] {
	return &cachedStats[T]{ttl: ttl, entries: map[string]cachedStatsEntry[T]{}}
}

// get 返回缓存结果；未命中或已过期时调用 compute 重新计算并缓存。
func (c *cachedStats[T]) get(key string, compute func() (T, error)) (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.entries[key]; ok && time.Now().Before(entry.expires) {
		return entry.value, nil
	}
	value, err := compute()
	if err != nil {
		var zero T
		return zero, err
	}
	// 顺带清理过期条目：key 取值有限（天数为 1–365），不会无限增长。
	now := time.Now()
	for k, entry := range c.entries {
		if now.After(entry.expires) {
			delete(c.entries, k)
		}
	}
	c.entries[key] = cachedStatsEntry[T]{value: value, expires: now.Add(c.ttl)}
	return value, nil
}
