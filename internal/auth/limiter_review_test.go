package auth

import (
	"fmt"
	"testing"
	"time"
)

func TestLimiterCapacityIsBoundedAndExpiredKeysAreReclaimed(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	window := NewWindowLimiter(2, time.Minute)
	window.now = func() time.Time { return now }
	login := NewLoginLimiter(3, time.Minute, 5*time.Minute)
	login.now = window.now
	for i := 0; i < maxLimiterKeys; i++ {
		key := fmt.Sprint(i)
		if !window.Allow(key) {
			t.Fatal("容量内应允许新键")
		}
		login.Failure(key)
	}
	if window.Allow("over-capacity") {
		t.Fatal("容量满不能无界增加 IP 键")
	}
	if locked, _ := login.Failure("over-capacity"); !locked {
		t.Fatal("失败记录容量满必须拒绝继续尝试")
	}
	login.Failure("0")
	login.Failure("0")
	now = now.Add(2 * time.Minute)
	if !window.Allow("new-key") {
		t.Fatal("过期窗口键未回收")
	}
	if locked, _ := login.Failure("new-key"); locked {
		t.Fatal("过期失败记录未回收")
	}
	if _, locked := login.Locked("0"); !locked {
		t.Fatal("清理不能解除仍有效的账号锁定")
	}
	if len(window.entries) != 1 || len(login.entries) != 2 {
		t.Fatalf("回收后的记录数量异常: window=%d login=%d", len(window.entries), len(login.entries))
	}
}
