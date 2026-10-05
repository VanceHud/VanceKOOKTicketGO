package bot

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/ticket"
)

func TestWebLoginRoleVerificationBypassesCachedRoles(t *testing.T) {
	env := newBotEnv(t)
	ctx := context.Background()
	if _, err := env.bot.userInfo(ctx, "9002"); err != nil {
		t.Fatal(err)
	}
	env.mock.AddUser(kook.User{ID: "9002", Username: "former-staff", Roles: []int64{}})
	role, ok, err := env.bot.ResolveWebRole(ctx, "9002")
	if err != nil || ok || role != "" {
		t.Fatalf("移除角色后不能使用缓存权限: %s %v %v", role, ok, err)
	}
	env.mock.AddUser(kook.User{ID: "9002", Username: "staff", Roles: []int64{roleAdmin}})
	role, ok, err = env.bot.ResolveWebRole(ctx, "9002")
	if err != nil || !ok || role != store.RoleStaff {
		t.Fatalf("当前角色验证失败: %s %v %v", role, ok, err)
	}
	env.bot.Stop()
	record := &store.Ticket{UserID: "9001", Status: store.TicketOpen, ChannelID: "test-channel"}
	if err := env.store.Tickets.CreateWithNo(record, store.Now(), env.loc); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.Close(ctx, record.No, ticket.SystemActor(), ""); !errors.Is(err, ticket.ErrNoPlatform) {
		t.Fatalf("机器人停止后应拒绝操作: %v", err)
	}
}

func TestTTLCacheRemovesExpiredAndBoundsEntries(t *testing.T) {
	cache := newTTLCache(time.Minute)
	cache.items["expired"] = cacheItem{value: "old", expires: time.Now().Add(-time.Second)}
	if _, ok := cache.get("expired"); ok || len(cache.items) != 0 {
		t.Fatal("过期缓存未释放")
	}
	for i := 0; i < maxCacheItems+100; i++ {
		cache.set(fmt.Sprint(i), i)
	}
	if len(cache.items) != maxCacheItems {
		t.Fatalf("缓存超过上限: %d", len(cache.items))
	}
	cache.clear()
	if len(cache.items) != 0 {
		t.Fatal("缓存未清空")
	}
}
