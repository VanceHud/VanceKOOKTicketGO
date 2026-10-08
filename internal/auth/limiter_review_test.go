package auth

import (
	"fmt"
	"testing"
	"time"
)

// TestLimiterCapacityEvictsOldest 验证容量满时淘汰最早的键，而不是拒绝新键。
//
// 旧实现在满容量时直接拒绝（登录维度还伪装成“已锁定”）：攻击者用大量随机
// 用户名/伪造 IP 就能填满容量，把所有正常用户的登录全部挡在门外。
func TestLimiterCapacityEvictsOldest(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	window := NewWindowLimiter(2, time.Hour)
	window.now = func() time.Time { return now }
	login := NewLoginLimiter(3, time.Hour, 5*time.Minute)
	login.now = window.now

	// 前 maxLimiterKeys-1 个键依次建立（每个键的时间戳不同，淘汰顺序才是确定的），
	// 最后一个键稍晚建立。
	for i := 0; i < maxLimiterKeys-1; i++ {
		key := fmt.Sprint(i)
		if !window.Allow(key) {
			t.Fatal("容量内应允许新键")
		}
		login.Failure(key)
		now = now.Add(time.Nanosecond)
	}
	now = now.Add(30 * time.Second)
	if !window.Allow("newest") {
		t.Fatal("容量内应允许新键")
	}
	login.Failure("newest")

	// 容量已满：新键必须被接受，并淘汰最早的 "0"。
	if !window.Allow("over-capacity") {
		t.Fatal("容量满时应淘汰最旧的键，而不是拒绝新键")
	}
	if locked, _ := login.Failure("over-capacity"); locked {
		t.Fatal("容量满时的新键不应被当作锁定拒绝")
	}
	if _, exists := window.entries["0"]; exists {
		t.Fatal("容量满时窗口限流应淘汰最早的键")
	}
	if _, exists := login.entries["0"]; exists {
		t.Fatal("容量满时失败记录应淘汰最早的键")
	}
	if len(window.entries) != maxLimiterKeys || len(login.entries) != maxLimiterKeys {
		t.Fatalf("容量必须有界: window=%d login=%d", len(window.entries), len(login.entries))
	}
	// 较新的键不受影响。
	if _, exists := window.entries["newest"]; !exists {
		t.Fatal("淘汰不应影响较新的键")
	}
}

// TestLoginLockBackoffIsCapped 验证账号级锁定时长封顶。
//
// user:<name> 键跨 IP 生效，攻击者只需周期性失败几次即可把管理员账号
// 无限期锁在门外（未封顶时退避可达 15 分钟 × 64 ≈ 16 小时）。
func TestLoginLockBackoffIsCapped(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	login := NewLoginLimiter(3, time.Minute, 15*time.Minute)
	login.now = func() time.Time { return now }

	for i := 0; i < 12; i++ {
		login.Failure("user:admin")
	}
	wait, locked := login.Locked("user:admin")
	if !locked {
		t.Fatal("持续失败后账号应处于锁定状态")
	}
	if wait > maxLockBackoff {
		t.Fatalf("锁定时长应封顶在 %s，实际 %s", maxLockBackoff, wait)
	}
	if wait < time.Minute {
		t.Fatalf("锁定时长不应短于配置的基础时长: %s", wait)
	}

	// 配置的基础锁定时长大于上限时，以配置为准。
	longLived := NewLoginLimiter(2, time.Minute, 3*time.Hour)
	longLived.now = login.now
	longLived.Failure("user:other")
	longLived.Failure("user:other")
	wait, locked = longLived.Locked("user:other")
	if !locked || wait < time.Hour {
		t.Fatalf("配置的基础时长应优先: locked=%v wait=%s", locked, wait)
	}
}

// TestLimiterReclaimsExpiredKeys 验证窗口过期回收与仍有效的锁定保留。
func TestLimiterReclaimsExpiredKeys(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	window := NewWindowLimiter(2, time.Minute)
	window.now = func() time.Time { return now }
	login := NewLoginLimiter(3, time.Minute, 5*time.Minute)
	login.now = window.now

	for i := 0; i < 4; i++ {
		key := fmt.Sprint(i)
		window.Allow(key)
		login.Failure(key)
	}
	// "0" 达到锁定阈值。
	login.Failure("0")
	login.Failure("0")
	if _, locked := login.Locked("0"); !locked {
		t.Fatal("达到阈值应锁定")
	}

	now = now.Add(2 * time.Minute)
	if !window.Allow("new-key") {
		t.Fatal("过期窗口键未回收")
	}
	if locked, _ := login.Failure("new-key"); locked {
		t.Fatal("过期失败记录未回收")
	}
	if _, locked := login.Locked("0"); !locked {
		t.Fatal("回收不能解除仍有效的账号锁定")
	}
	if len(window.entries) != 1 || len(login.entries) != 2 {
		t.Fatalf("回收后的记录数量异常: window=%d login=%d", len(window.entries), len(login.entries))
	}
}
