package auth

import (
	"sync"
	"time"
)

const maxLimiterKeys = 16384

// LoginLimiter 实现“IP + 账号”双维度登录失败限流与锁定。
//
// 判定逻辑：窗口内失败次数达到阈值即锁定一段时间；成功登录后清零。
// 状态保存在内存中——单进程部署下足够，且进程重启即清空（不构成持久化攻击面）。
type LoginLimiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	lockFor time.Duration
	entries map[string]*failureEntry
	now     func() time.Time
	nextGC  time.Time
}

type failureEntry struct {
	fails     int
	firstSeen time.Time
	lockedTil time.Time
}

// NewLoginLimiter 创建限流器。
func NewLoginLimiter(max int, window, lockFor time.Duration) *LoginLimiter {
	if max < 1 {
		max = 5
	}
	return &LoginLimiter{
		max:     max,
		window:  window,
		lockFor: lockFor,
		entries: make(map[string]*failureEntry),
		now:     time.Now,
	}
}

// Locked 返回该键是否处于锁定期，以及剩余锁定时间。
func (l *LoginLimiter) Locked(key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[key]
	if !ok {
		return 0, false
	}
	now := l.now()
	if now.Before(entry.lockedTil) {
		return entry.lockedTil.Sub(now), true
	}
	// 锁定已过期且窗口已过，清理条目。
	if now.Sub(entry.firstSeen) > l.window {
		delete(l.entries, key)
	}
	return 0, false
}

// Failure 记录一次失败，返回是否因此触发锁定与剩余可尝试次数。
func (l *LoginLimiter) Failure(key string) (locked bool, remaining int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if !now.Before(l.nextGC) {
		l.gc(now)
		l.nextGC = now.Add(min(time.Minute, l.window))
	}
	entry, ok := l.entries[key]
	if !ok || now.Sub(entry.firstSeen) > l.window {
		if !ok && len(l.entries) >= maxLimiterKeys {
			return true, 0
		}
		entry = &failureEntry{firstSeen: now}
		l.entries[key] = entry
	}
	entry.fails++
	if entry.fails >= l.max {
		// 超出阈值后锁定时间按超出次数倍增，抑制持续爆破。
		over := entry.fails - l.max
		backoff := l.lockFor << uint(min(over, 6))
		entry.lockedTil = now.Add(backoff)
		return true, 0
	}
	return false, l.max - entry.fails
}

// Success 登录成功后清除失败计数。
func (l *LoginLimiter) Success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// GC 清理过期条目，避免长期运行后内存增长。
func (l *LoginLimiter) GC() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gc(l.now())
}

func (l *LoginLimiter) gc(now time.Time) {
	for key, entry := range l.entries {
		if now.Before(entry.lockedTil) {
			continue
		}
		if now.Sub(entry.firstSeen) > l.window {
			delete(l.entries, key)
		}
	}
}

// WindowLimiter 是通用的滑动窗口计数器，用于一次性码生成等接口的频率限制。
type WindowLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	max     int
	entries map[string][]time.Time
	now     func() time.Time
	nextGC  time.Time
}

// NewWindowLimiter 创建滑动窗口限流器。
func NewWindowLimiter(max int, window time.Duration) *WindowLimiter {
	if max < 1 {
		max = 10
	}
	return &WindowLimiter{
		window:  window,
		max:     max,
		entries: make(map[string][]time.Time),
		now:     time.Now,
	}
}

// Allow 判断该键当前是否允许通过，并记录本次调用。
func (l *WindowLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if !now.Before(l.nextGC) {
		l.gc(now)
		l.nextGC = now.Add(min(time.Minute, l.window))
	}
	if _, exists := l.entries[key]; !exists && len(l.entries) >= maxLimiterKeys {
		return false
	}
	cutoff := now.Add(-l.window)
	times := l.entries[key][:0]
	for _, t := range l.entries[key] {
		if t.After(cutoff) {
			times = append(times, t)
		}
	}
	if len(times) >= l.max {
		l.entries[key] = times
		return false
	}
	l.entries[key] = append(times, now)
	return true
}

// GC 清理窗口外的记录。
func (l *WindowLimiter) GC() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gc(l.now())
}

func (l *WindowLimiter) gc(now time.Time) {
	cutoff := now.Add(-l.window)
	for key, times := range l.entries {
		kept := times[:0]
		for _, t := range times {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(l.entries, key)
			continue
		}
		l.entries[key] = kept
	}
}
