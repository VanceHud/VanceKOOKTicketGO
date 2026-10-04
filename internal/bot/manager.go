package bot

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"vancekookticket/internal/kook"
	"vancekookticket/internal/store"
)

// Manager 管理机器人实例的生命周期。
//
// 存在意义：WebUI 允许运行中修改 Token / 服务器 / 频道配置，
// 这些变更必须体现为“断开旧连接 → 用新配置重连”，
// 而连接过程可能失败（Token 错、网络不通），因此需要一个可重试、可观测的包装。
type Manager struct {
	deps Deps

	mu        sync.Mutex
	bot       *Bot
	running   bool
	lastError string
}

// NewManager 创建管理器。
func NewManager(deps Deps) (*Manager, error) {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Manager{deps: deps}, nil
}

// Start 读取配置并建立连接（幂等：已运行时不会重复启动）。
//
// ctx 仅用于本次建连的准备工作（见 Bot.Start）；连接建立后由 Stop 负责断开，
// 因此调用方用 HTTP 请求上下文调用它是安全的。
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	instance, err := New(m.deps)
	if err != nil {
		m.setError(err)
		return err
	}
	if err := instance.Start(ctx); err != nil {
		m.setError(err)
		return err
	}

	m.mu.Lock()
	m.bot = instance
	m.running = true
	m.lastError = ""
	m.mu.Unlock()
	return nil
}

// Stop 断开当前连接。
func (m *Manager) Stop() {
	m.mu.Lock()
	instance := m.bot
	m.bot, m.running = nil, false
	m.mu.Unlock()

	if instance != nil {
		instance.Stop()
	}
}

// Restart 使用最新配置重新连接；返回错误表示新配置连接失败（旧连接已断开）。
func (m *Manager) Restart(ctx context.Context) error {
	m.deps.Logger.Info("正在按最新配置重启机器人连接")
	m.Stop()

	// Start 会构造新的 Bot 实例，角色/频道缓存随之重建，无需额外清理。
	return m.Start(ctx)
}

// Status 返回机器人状态；未连接时给出原因。
func (m *Manager) Status() Status {
	m.mu.Lock()
	instance, lastError, running := m.bot, m.lastError, m.running
	m.mu.Unlock()

	if instance == nil {
		status := Status{Running: false, LastError: lastError}
		if !running && lastError == "" {
			status.LastError = "机器人尚未启动"
		}
		return status
	}
	status := instance.Status()
	if lastError != "" {
		status.LastError = lastError
	}
	return status
}

// GuildRoles 返回服务器角色；未连接时报错。
func (m *Manager) GuildRoles(ctx context.Context) ([]kook.Role, error) {
	instance := m.current()
	if instance == nil {
		return nil, errors.New("机器人尚未连接 KOOK")
	}
	return instance.GuildRoles(ctx)
}

// GuildChannels 返回服务器频道；未连接时报错。
func (m *Manager) GuildChannels(ctx context.Context) ([]kook.Channel, error) {
	instance := m.current()
	if instance == nil {
		return nil, errors.New("机器人尚未连接 KOOK")
	}
	return instance.GuildChannels(ctx)
}

// ResolveWebRole 用当前 KOOK 角色解析用户应得的 WebUI 权限。
// 未连接时返回 ok=false，调用方应回退到签发验证码时的角色快照。
func (m *Manager) ResolveWebRole(ctx context.Context, kookUserID string) (string, bool, error) {
	instance := m.current()
	if instance == nil {
		return "", false, errors.New("机器人尚未连接 KOOK")
	}
	role, ok := instance.ResolveWebRole(ctx, kookUserID)
	return role, ok, nil
}

// SendPanelCard 让机器人发送/重发某个面板的卡片。
func (m *Manager) SendPanelCard(ctx context.Context, panel *store.Panel, buttonText string) (string, error) {
	instance := m.current()
	if instance == nil {
		return "", errors.New("机器人尚未连接 KOOK，无法发送面板卡片")
	}
	return instance.SendPanelCard(ctx, panel, buttonText)
}

// DeleteMessage 尽力删除一条消息。
func (m *Manager) DeleteMessage(ctx context.Context, msgID string) error {
	instance := m.current()
	if instance == nil {
		return errors.New("机器人尚未连接 KOOK")
	}
	return instance.DeleteMessage(ctx, msgID)
}

// ChannelInfo 返回频道信息。
func (m *Manager) ChannelInfo(ctx context.Context, channelID string) (*kook.Channel, error) {
	instance := m.current()
	if instance == nil {
		return nil, errors.New("机器人尚未连接 KOOK")
	}
	return instance.ChannelInfo(ctx, channelID)
}

// RoleName 返回角色名（未知返回空串）。
func (m *Manager) RoleName(ctx context.Context, roleID string) string {
	instance := m.current()
	if instance == nil {
		return ""
	}
	return instance.RoleInfo(ctx, roleID)
}

// NotifyConfigChanged 在 WebUI 修改面板等配置后清缓存。
func (m *Manager) NotifyConfigChanged() {
	if instance := m.current(); instance != nil {
		instance.RefreshPanels()
	}
}

func (m *Manager) current() *Bot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bot
}

func (m *Manager) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.lastError = err.Error()
	} else {
		m.lastError = ""
	}
}
