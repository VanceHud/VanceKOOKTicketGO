package auth

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
	"gorm.io/gorm"
)

func TestSessionReadBurstDoesNotWriteAndRevocationRemainsImmediate(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/ticket.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	user := &store.WebUser{Username: "staff", Role: store.RoleStaff}
	if err := st.Users.Create(user); err != nil {
		t.Fatal(err)
	}
	manager := NewSessionManager(st, 12*time.Hour, 7*24*time.Hour, []byte("0123456789abcdef0123456789abcdef"))
	created, err := manager.Create(user.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	var updates atomic.Int64
	if err := st.DB().Callback().Update().Before("gorm:update").Register("count_review_updates", func(*gorm.DB) { updates.Add(1) }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if _, _, err := manager.Authenticate(created.Token); err != nil {
			t.Fatal(err)
		}
	}
	if updates.Load() != 0 {
		t.Fatalf("短时间 50 次读取不应写库: %d", updates.Load())
	}
	old := store.Now().Add(-2 * time.Minute)
	if err := st.DB().Model(&store.Session{}).Where("id = ?", created.Session.ID).Update("last_seen_at", old).Error; err != nil {
		t.Fatal(err)
	}
	updates.Store(0)
	if _, _, err := manager.Check(created.Token); err != nil {
		t.Fatal(err)
	}
	if updates.Load() != 0 {
		t.Fatal("SSE 校验不能续期")
	}
	if _, _, err := manager.Authenticate(created.Token); err != nil {
		t.Fatal(err)
	}
	if updates.Load() != 1 {
		t.Fatalf("超过一分钟应续期一次: %d", updates.Load())
	}
	if err := manager.RevokeUser(user.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Authenticate(created.Token); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("吊销未立即生效: %v", err)
	}
}

func TestInFlightLoginCannotCreateSessionAfterSecurityChange(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/ticket.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	user := &store.WebUser{Username: "staff", Role: store.RoleStaff, PasswordHash: "old-hash"}
	if err := st.Users.Create(user); err != nil {
		t.Fatal(err)
	}
	manager := NewSessionManager(st, time.Hour, 24*time.Hour, []byte("0123456789abcdef0123456789abcdef"))
	for _, fields := range []map[string]any{
		{"password_hash": "new-hash"},
		{"role": store.RoleReadonly},
		{"must_change_password": true},
		{"disabled": true},
	} {
		verified, err := st.Users.ByID(user.ID)
		if err != nil {
			t.Fatal(err)
		}
		// 模拟旧密码已经通过 bcrypt，但登录请求尚未写入会话。
		if err := st.Users.UpdateFields(user.ID, fields); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.CreateForUser(verified, "127.0.0.1", "test"); !errors.Is(err, store.ErrAuthenticationChanged) {
			t.Fatalf("旧认证快照被重新授予会话: %v", err)
		}
		count, err := st.Sessions.CountForUser(user.ID, store.Now())
		if err != nil || count != 0 {
			t.Fatalf("不应留下可用会话: %d %v", count, err)
		}
	}
}
