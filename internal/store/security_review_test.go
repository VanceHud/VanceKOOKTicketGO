package store

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestSecurityUpdateAndSessionRevocationAreAtomic(t *testing.T) {
	st := newTestStore(t)
	user := &WebUser{Username: "staff", PasswordHash: "old-password-hash", Role: RoleStaff}
	if err := st.Users.Create(user); err != nil {
		t.Fatal(err)
	}
	if err := st.Sessions.Create(&Session{TokenHash: "session", UserID: user.ID, ExpiresAt: Now().Add(time.Hour), LastSeenAt: Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Exec(`CREATE TRIGGER reject_revoke BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'test revocation failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.Users.UpdateFields(user.ID, map[string]any{"password_hash": "new-password-hash"}); err == nil {
		t.Fatal("吊销失败不能提交密码变更")
	}
	stored, err := st.Users.ByID(user.ID)
	if err != nil || stored.PasswordHash != user.PasswordHash {
		t.Fatalf("密码变更未回滚: %v", err)
	}
	if _, err := st.Sessions.ByTokenHash("session"); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Exec("DROP TRIGGER reject_revoke").Error; err != nil {
		t.Fatal(err)
	}
	if err := st.Users.UpdateFields(user.ID, map[string]any{"password_hash": "new-password-hash"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Sessions.ByTokenHash("session"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("旧会话未吊销: %v", err)
	}
}

func TestConcurrentAdminRemovalPreservesAnAdministrator(t *testing.T) {
	for _, action := range []string{"delete", "disable", "demote"} {
		t.Run(action, func(t *testing.T) {
			st := newTestStore(t)
			users := []*WebUser{{Username: "one", Role: RoleAdmin}, {Username: "two", Role: RoleAdmin}}
			for _, user := range users {
				if err := st.Users.Create(user); err != nil {
					t.Fatal(err)
				}
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for _, user := range users {
				go func(id uint) {
					<-start
					if action == "delete" {
						results <- st.Users.Delete(id)
					} else if action == "disable" {
						results <- st.Users.UpdateFields(id, map[string]any{"disabled": true})
					} else {
						results <- st.Users.UpdateFields(id, map[string]any{"role": RoleStaff})
					}
				}(user.ID)
			}
			close(start)
			success, rejected := 0, 0
			for i := 0; i < 2; i++ {
				err := <-results
				if err == nil {
					success++
				} else if errors.Is(err, ErrLastAdmin) {
					rejected++
				} else {
					t.Fatal(err)
				}
			}
			if success != 1 || rejected != 1 {
				t.Fatalf("success=%d rejected=%d", success, rejected)
			}
		})
	}
}

func TestInFlightPasswordChangeCannotOverwriteAdminReset(t *testing.T) {
	st := newTestStore(t)
	verified := &WebUser{Username: "staff", Role: RoleStaff, PasswordHash: "old-hash"}
	if err := st.Users.Create(verified); err != nil {
		t.Fatal(err)
	}
	if err := st.Users.UpdateFields(verified.ID, map[string]any{"password_hash": "admin-reset-hash", "must_change_password": true}); err != nil {
		t.Fatal(err)
	}
	if err := st.Users.UpdateVerified(verified, map[string]any{"password_hash": "stale-request-hash", "must_change_password": false}); !errors.Is(err, ErrAuthenticationChanged) {
		t.Fatalf("在途旧密码改密请求不能覆盖管理员重置: %v", err)
	}
	current, err := st.Users.ByID(verified.ID)
	if err != nil || current.PasswordHash != "admin-reset-hash" || !current.MustChangePassword {
		t.Fatalf("管理员重置被覆盖: %+v %v", current, err)
	}
}

func TestConcurrentBindingDoesNotOverwriteIdentity(t *testing.T) {
	st := newTestStore(t)
	user := &WebUser{Username: "staff", Role: RoleStaff}
	if err := st.Users.Create(user); err != nil {
		t.Fatal(err)
	}
	codes := []*AuthCode{
		{CodeHash: "one", Purpose: CodePurposeBind, KookUserID: "9001", ExpiresAt: Now().Add(time.Minute)},
		{CodeHash: "two", Purpose: CodePurposeBind, KookUserID: "9002", ExpiresAt: Now().Add(time.Minute)},
	}
	for _, code := range codes {
		if err := st.Codes.Create(code); err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan error, 2)
	for _, code := range codes {
		go func(id uint) { results <- st.Codes.BindToUser(id, user.ID, Now(), "127.0.0.1") }(code.ID)
	}
	success, rejected := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, ErrAlreadyBound) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("success=%d rejected=%d", success, rejected)
	}
	var used int64
	if err := st.DB().Model(&AuthCode{}).Where("used_at IS NOT NULL").Count(&used).Error; err != nil {
		t.Fatal(err)
	}
	if used != 1 {
		t.Fatalf("冲突不能消费绑定码: %d", used)
	}
}

func TestExpiredCodeCannotBeConsumed(t *testing.T) {
	st := newTestStore(t)
	code := &AuthCode{CodeHash: "expired", Purpose: CodePurposeLogin, ExpiresAt: Now().Add(-time.Minute)}
	if err := st.Codes.Create(code); err != nil {
		t.Fatal(err)
	}
	if err := st.Codes.MarkUsed(code.ID, Now(), "127.0.0.1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("过期码被消费: %v", err)
	}
}

func TestSessionLimitIsAtomicAndTouchDoesNotMoveBackward(t *testing.T) {
	st := newTestStore(t)
	user := &WebUser{Username: "staff", Role: RoleStaff}
	if err := st.Users.Create(user); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- st.Sessions.CreateLimited(&Session{TokenHash: fmt.Sprintf("session-%d", i), UserID: user.ID, ExpiresAt: Now().Add(time.Hour), LastSeenAt: Now()}, 5)
		}(i)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	count, err := st.Sessions.CountForUser(user.ID, Now())
	if err != nil || count != 5 {
		t.Fatalf("并发会话数 %d: %v", count, err)
	}
	var session Session
	if err := st.DB().First(&session).Error; err != nil {
		t.Fatal(err)
	}
	at := Now()
	if err := st.Sessions.Touch(session.ID, at); err != nil {
		t.Fatal(err)
	}
	if err := st.Sessions.Touch(session.ID, at.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	stored, _ := st.Sessions.ByTokenHash(session.TokenHash)
	if !stored.LastSeenAt.Equal(at) {
		t.Fatalf("活跃时间倒退: %v", stored.LastSeenAt)
	}
}

func TestExportLimitIsExplicit(t *testing.T) {
	st := newTestStore(t)
	messages := make([]TicketMessage, MaxExportMessages+1)
	for i := range messages {
		messages[i] = TicketMessage{TicketNo: "TK-261005-TEST", Content: "x", CreatedAt: Now()}
	}
	if err := st.DB().CreateInBatches(messages[:MaxExportMessages], 200).Error; err != nil {
		t.Fatal(err)
	}
	if items, err := st.Tickets.ExportMessages("TK-261005-TEST"); err != nil || len(items) != MaxExportMessages {
		t.Fatalf("上限内导出失败 %d %v", len(items), err)
	}
	if err := st.DB().Create(&messages[MaxExportMessages]).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := st.Tickets.ExportMessages("TK-261005-TEST"); !errors.Is(err, ErrExportTooLarge) {
		t.Fatalf("不能静默截断: %v", err)
	}
}

func TestOverviewReusesStatusAggregationAndTimelineIndex(t *testing.T) {
	st := newTestStore(t)
	var queries atomic.Int64
	if err := st.DB().Callback().Query().Before("gorm:query").Register("count_review_queries", func(*gorm.DB) { queries.Add(1) }); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Callback().Row().Before("gorm:row").Register("count_review_rows", func(*gorm.DB) { queries.Add(1) }); err != nil {
		t.Fatal(err)
	}
	ov, err := st.Tickets.Overview(Now(), testLocation(t), 7)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Total != 0 || queries.Load() != 5 {
		t.Fatalf("重复查询未减少: %d", queries.Load())
	}
	var plans []struct{ Detail string }
	if err := st.DB().Raw("EXPLAIN QUERY PLAN SELECT * FROM ticket_messages WHERE ticket_no = ? ORDER BY created_at, id LIMIT 2000", "TK-261005-TEST").Scan(&plans).Error; err != nil {
		t.Fatal(err)
	}
	found := false
	for _, plan := range plans {
		if bytes.Contains([]byte(plan.Detail), []byte("TEMP B-TREE")) {
			t.Fatalf("时间线仍使用临时排序: %s", plan.Detail)
		}
		if bytes.Contains([]byte(plan.Detail), []byte("idx_ticket_messages_timeline")) {
			found = true
		}
	}
	if !found {
		t.Fatalf("时间线未走复合索引: %v", plans)
	}
}

func TestMigrationHardensDatabaseAndLogsOmitParameters(t *testing.T) {
	var logs bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(original)
	st := newTestStore(t)
	if err := os.Chmod(st.path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(st.path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("数据库权限错误: %v %v", info, err)
	}
	secret := "SENSITIVE-PASSWORD-HASH"
	first := &WebUser{Username: "private-review-username", Role: RoleStaff, PasswordHash: secret}
	if err := st.Users.Create(first); err != nil {
		t.Fatal(err)
	}
	if err := st.Users.Create(&WebUser{Username: "private-review-username", Role: RoleStaff, PasswordHash: secret}); err == nil {
		t.Fatal("应产生数据库告警")
	}
	if bytes.Contains(logs.Bytes(), []byte(secret)) || bytes.Contains(logs.Bytes(), []byte(first.Username)) {
		t.Fatalf("SQL 参数泄露: %s", logs.String())
	}
}
