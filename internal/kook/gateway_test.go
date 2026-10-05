package kook_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook/kooktest"
)

// memorySessionStore 是 SessionStore 的内存实现，用于断言会话读写行为。
type memorySessionStore struct {
	mu      sync.Mutex
	session string
	sn      int64
}

func (m *memorySessionStore) LoadGatewaySession() (string, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.session, m.sn, nil
}

func (m *memorySessionStore) SaveGatewaySession(sessionID string, sn int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.session, m.sn = sessionID, sn
	return nil
}

func (m *memorySessionStore) snapshot() (string, int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.session, m.sn
}

func gatewayTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

// startGateway 在模拟平台上启动一个网关，返回事件通道。
// startGateway 在模拟平台上启动一个网关，返回事件通道。
// tune 可选，用于覆盖网关参数（例如把续传静默超时压短）。
func startGateway(t *testing.T, platform *kooktest.Server, store kook.SessionStore, tune ...func(*kook.GatewayOptions)) chan kook.Event {
	t.Helper()

	client, err := kook.NewClient(kook.Options{
		Token:   "test-token-abcdefghijklmnop",
		BaseURL: platform.BaseURL(),
		Logger:  gatewayTestLogger(),
	})
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}

	events := make(chan kook.Event, 16)
	options := kook.GatewayOptions{
		Client:       client,
		Logger:       gatewayTestLogger(),
		OnEvent:      func(_ context.Context, event kook.Event) { events <- event },
		SessionStore: store,
		// 测试里把间隔压到毫秒级：既能跑通心跳/pong，又不拖慢用例。
		HeartbeatInterval: 50 * time.Millisecond,
		BaseBackoff:       20 * time.Millisecond,
		MaxBackoff:        50 * time.Millisecond,
	}
	for _, apply := range tune {
		apply(&options)
	}

	gateway, err := kook.NewGateway(options)
	if err != nil {
		t.Fatalf("创建网关失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = gateway.Run(ctx) }()
	return events
}

// TestGatewayResumesPersistedSession 是回归测试：
//
// 进程重启（升级、重建容器）后必须带着上次的 session_id 与 sn 续传。
// 否则平台会新建会话，而事件仍可能投递到尚未过期的旧会话上——
// 线上表现为「WebUI 显示已连接，但点击创建工单没有任何反应」。
func TestGatewayResumesPersistedSession(t *testing.T) {
	platform := kooktest.New()
	t.Cleanup(platform.Close)

	store := &memorySessionStore{session: "sess-old", sn: 42}
	events := startGateway(t, platform, store)

	waitFor(t, "建立连接", func() bool { return len(platform.WSConnects()) >= 1 })
	query := platform.WSConnects()[0]
	if query.Get("resume") != "1" || query.Get("session_id") != "sess-old" || query.Get("sn") != "42" {
		t.Fatalf("重启后应带旧会话续传，实际连接参数: %v", query)
	}

	// 回调返回后要把 sn 落库：崩溃重启后才能从这条之后补发。
	platform.Push(kook.EventTypeText, kooktest.TextMessageEvent("chan-1", "9001", "你好", kook.User{ID: "9001"}))
	select {
	case <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("未收到推送的事件")
	}
	waitFor(t, "会话进度落库", func() bool {
		session, sn := store.snapshot()
		return session == "sess-old" && sn == 43
	})
}

// TestGatewayConnectsFreshWithoutSession 验证首次启动（没有持久化会话）时不带
// resume 参数，并把新拿到的会话落库，供后续重启续传。
func TestGatewayConnectsFreshWithoutSession(t *testing.T) {
	platform := kooktest.New()
	t.Cleanup(platform.Close)

	store := &memorySessionStore{}
	startGateway(t, platform, store)

	waitFor(t, "建立连接", func() bool { return len(platform.WSConnects()) >= 1 })
	if query := platform.WSConnects()[0]; query.Get("resume") != "" {
		t.Fatalf("没有可续传的会话时不应携带 resume，实际参数: %v", query)
	}
	waitFor(t, "新会话落库", func() bool {
		session, _ := store.snapshot()
		return session != ""
	})
}

// TestGatewayReconnectSignalRebuildsSession 验证收到平台 s=5（当前连接失效）时，
// 会清空本地会话并以全新会话重连，而不是拿着失效会话反复 resume。
func TestGatewayReconnectSignalRebuildsSession(t *testing.T) {
	platform := kooktest.New()
	t.Cleanup(platform.Close)

	store := &memorySessionStore{session: "sess-old", sn: 5}
	startGateway(t, platform, store)

	waitFor(t, "建立连接", func() bool { return len(platform.WSConnects()) >= 1 })
	platform.SendReconnect(40107, "session expired")

	waitFor(t, "以全新会话重连", func() bool { return len(platform.WSConnects()) >= 2 })
	if query := platform.WSConnects()[1]; query.Get("resume") != "" {
		t.Fatalf("s=5 后应以全新会话连接，实际参数: %v", query)
	}
}

// TestGatewayFallsBackWhenResumeRejected 验证续传被平台拒绝（会话已过期）时，
// 会自动改用全新会话，避免永远卡在同一个失效会话上。
func TestGatewayFallsBackWhenResumeRejected(t *testing.T) {
	platform := kooktest.New()
	t.Cleanup(platform.Close)
	platform.RejectResume = true

	store := &memorySessionStore{session: "sess-stale", sn: 3}
	startGateway(t, platform, store)

	waitFor(t, "首次连接", func() bool { return len(platform.WSConnects()) >= 1 })
	if query := platform.WSConnects()[0]; query.Get("resume") != "1" {
		t.Fatalf("第一次连接应尝试续传，实际参数: %v", query)
	}

	waitFor(t, "改用全新会话重连", func() bool { return len(platform.WSConnects()) >= 2 })
	if query := platform.WSConnects()[1]; query.Get("resume") != "" {
		t.Fatalf("续传被拒后应改用全新会话，实际参数: %v", query)
	}
}

// TestGatewayRebuildsSessionWhenResumeSilent 是回归测试：
//
// 平台受理了 resume 却既不补发事件、也不回 resumeOK(s=6) 时，连接看起来完全正常
// （心跳照常、WebUI 显示已连接），但永远不会收到任何事件——线上表现为
// 「点击创建工单提示操作成功，但机器人没有任何反应」。
// 网关必须在静默超时后丢弃旧会话，改用全新会话连接。
func TestGatewayRebuildsSessionWhenResumeSilent(t *testing.T) {
	platform := kooktest.New()
	t.Cleanup(platform.Close)
	platform.SuppressResumeAck = true

	store := &memorySessionStore{session: "sess-stuck", sn: 9}
	startGateway(t, platform, store, func(options *kook.GatewayOptions) {
		options.ResumeSilenceTimeout = 300 * time.Millisecond
	})

	waitFor(t, "首次带旧会话续传", func() bool { return len(platform.WSConnects()) >= 1 })
	if query := platform.WSConnects()[0]; query.Get("resume") != "1" {
		t.Fatalf("第一次连接应尝试续传，实际参数: %v", query)
	}

	waitFor(t, "改用全新会话重连", func() bool { return len(platform.WSConnects()) >= 2 })
	if query := platform.WSConnects()[1]; query.Get("resume") != "" {
		t.Fatalf("续传静默后应改用全新会话，实际参数: %v", query)
	}
}
