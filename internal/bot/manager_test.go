package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"vancekookticket/internal/eventbus"
	"vancekookticket/internal/kook"
	"vancekookticket/internal/kook/kooktest"
	"vancekookticket/internal/store"
	"vancekookticket/internal/ticket"
)

// newManagerEnv 搭好「模拟平台 + 数据库 + Manager」，用于验证连接生命周期。
func newManagerEnv(t *testing.T) (*Manager, *kooktest.Server, *store.Store) {
	t.Helper()

	st, err := store.Open(t.TempDir() + "/ticket.db")
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mock := kooktest.New()
	t.Cleanup(mock.Close)

	bus := eventbus.New()
	loc := time.UTC
	svc := ticket.NewService(st, bus, ticket.NewNoopPlatform(nil), loc, st.Settings.OutdateHours)

	manager, err := NewManager(Deps{
		Store:     st,
		Bus:       bus,
		Tickets:   svc,
		Location:  loc,
		AppSecret: []byte("app-secret-for-manager-tests-0123456789"),
		Config: func(context.Context) (*Config, error) {
			return &Config{
				Token:          "test-token-abcdefghijklmnop",
				APIBase:        mock.BaseURL(),
				GuildID:        "5000",
				CategoryID:     "cat-1",
				LogChannelID:   "log-1",
				DebugChannelID: "debug-1",
				// 放宽限速，避免用例在令牌桶上排队
				RatePerSecond: 200,
				RateBurst:     200,
			}, nil
		},
		HeartbeatTick: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("创建机器人管理器失败: %v", err)
	}
	t.Cleanup(manager.Stop)
	return manager, mock, st
}

// waitForStatus 轮询等待状态满足条件。
func waitForStatus(t *testing.T, read func() Status, description string, condition func(Status) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition(read()) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s（当前状态：%+v）", description, read())
}

// TestManagerStartIgnoresRequestContextCancel 是回归测试：
//
// WebUI 的「配置变更后重连 / 重新连接」传入的是 HTTP 请求上下文，
// 处理函数返回后它立即被取消。若网关连接继承该上下文，
// 建连时就会失败并打印
// “获取网关地址失败: 请求 KOOK 接口失败: Get .../gateway/index: context canceled”，
// 且连接不会再自行恢复。
func TestManagerStartIgnoresRequestContextCancel(t *testing.T) {
	manager, mock, _ := newManagerEnv(t)

	// 模拟一次 HTTP 请求触发的重连。
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()

	if err := manager.Start(requestCtx); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	// 模拟处理函数返回：请求上下文被取消。
	cancelRequest()

	waitForStatus(t, manager.Status, "网关连接建立", func(s Status) bool { return s.Connected })

	// 连接必须仍在工作：平台推送的事件能够被处理。
	before := manager.Status().EventsHandled
	mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(
		"panel-1", "9001", "invalid-value", kook.User{ID: "9001", Username: "asker"}))
	waitForStatus(t, manager.Status, "事件被处理", func(s Status) bool { return s.EventsHandled > before })

	status := manager.Status()
	if !status.Connected {
		t.Fatalf("请求上下文取消后连接已断开: %+v", status)
	}
	if strings.Contains(status.LastError, "context canceled") {
		t.Fatalf("请求上下文的取消影响了网关连接: %s", status.LastError)
	}
	if calls := mock.CallsOf("gateway/index"); len(calls) == 0 {
		t.Fatal("未观察到网关地址请求：连接可能并未真正建立")
	}
}

// TestManagerStopStillClosesGateway 确认「与请求上下文解绑」不等于无法停止：
// Stop 之后网关必须真正退出，平台再推送事件也不会被处理。
func TestManagerStopStillClosesGateway(t *testing.T) {
	manager, mock, _ := newManagerEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	waitForStatus(t, manager.Status, "网关连接建立", func(s Status) bool { return s.Connected })

	// 抓住实例，Stop 之后 Manager 不再返回它的状态。
	instance := manager.current()
	if instance == nil {
		t.Fatal("机器人实例不存在")
	}
	manager.Stop()
	waitForStatus(t, instance.Status, "连接断开", func(s Status) bool { return !s.Connected })

	handled := instance.Status().EventsHandled
	mock.Push(kook.EventTypeSystem, kooktest.ButtonClickEvent(
		"panel-1", "9001", "invalid-value", kook.User{ID: "9001", Username: "asker"}))
	time.Sleep(500 * time.Millisecond)
	if got := instance.Status().EventsHandled; got != handled {
		t.Fatalf("Stop 之后仍在处理事件：%d → %d", handled, got)
	}
}

// TestManagerRestartResumesPersistedSession 是回归测试：
//
// 重新连接（WebUI「重新连接」或容器重建）必须带上次的 session_id 续传。
// 否则平台会新建一个会话，而离线期间的事件依旧投递给尚未过期的旧会话，
// 线上表现为「WebUI 显示已连接，但点击创建工单没有任何反应」——
// 手工点一次「重新连接」又恢复正常。
func TestManagerRestartResumesPersistedSession(t *testing.T) {
	manager, mock, st := newManagerEnv(t)

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	t.Cleanup(manager.Stop)
	waitForStatus(t, manager.Status, "网关连接建立", func(s Status) bool { return s.Connected })

	sessionID, _, err := st.Settings.LoadGatewaySession()
	if err != nil {
		t.Fatalf("读取网关会话失败: %v", err)
	}
	if sessionID == "" {
		t.Fatal("连接建立后应把会话落库，供重启后续传")
	}

	if err := manager.Restart(context.Background()); err != nil {
		t.Fatalf("重连失败: %v", err)
	}
	waitForStatus(t, manager.Status, "重连完成", func(s Status) bool { return s.Connected })

	connects := mock.WSConnects()
	if len(connects) < 2 {
		t.Fatalf("应观察到至少两次网关连接，实际 %d 次", len(connects))
	}
	last := connects[len(connects)-1]
	if last.Get("resume") != "1" || last.Get("session_id") != sessionID {
		t.Fatalf("重连应续传旧会话 %s，实际连接参数: %v", sessionID, last)
	}
}
