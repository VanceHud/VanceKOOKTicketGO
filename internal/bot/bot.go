// Package bot 把 KOOK 客户端与工单业务连接起来。
//
// 职责划分：
//   - kook 包只负责与平台通信（REST + WebSocket）；
//   - ticket 包只负责工单业务与状态机；
//   - 本包负责事件分发、按钮回调、命令处理、KOOK 侧副作用（建频道/权限/通知），
//     并实现 ticket.Platform 接口。
package bot

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"vancekookticket/internal/eventbus"
	"vancekookticket/internal/kook"
	"vancekookticket/internal/store"
	"vancekookticket/internal/ticket"
)

// Config 是机器人运行配置快照（来自数据库 settings 表）。
type Config struct {
	Token string
	// APIBase 可覆盖 KOOK API 地址（默认官方地址；可用于代理或测试）。
	APIBase        string
	GuildID        string
	CategoryID     string
	LogChannelID   string
	DebugChannelID string
	OutdateHours   int
	// RatePerSecond 与 RateBurst 控制对 KOOK 的调用速率；留空使用默认值（4/s，容量 4）。
	RatePerSecond float64
	RateBurst     int
}

// ConfigFunc 返回当前配置快照；实现方应带短缓存，避免每次事件都查库。
type ConfigFunc func(ctx context.Context) (*Config, error)

// Status 是机器人运行状态，供 WebUI 展示。
//
// 时间字段使用指针并配合 omitempty：未连接时不输出零值时间，
// 避免前端把 0001-01-01 当成真实时间渲染。
type Status struct {
	Running       bool       `json:"running"`
	Connected     bool       `json:"connected"`
	BotID         string     `json:"botId,omitempty"`
	BotName       string     `json:"botName,omitempty"`
	GuildID       string     `json:"guildId,omitempty"`
	GuildName     string     `json:"guildName,omitempty"`
	SessionID     string     `json:"sessionId,omitempty"`
	EventsHandled int64      `json:"eventsHandled"`
	LastError     string     `json:"lastError,omitempty"`
	ConnectedAt   *time.Time `json:"connectedAt,omitempty"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	LastEventAt   *time.Time `json:"lastEventAt,omitempty"`
}

// Deps 是 Bot 的依赖。
type Deps struct {
	Store         *store.Store
	Bus           *eventbus.Bus
	Tickets       *ticket.Service
	Config        ConfigFunc
	Logger        *slog.Logger
	Location      *time.Location
	AppSecret     []byte
	RequestStop   func() // /kill 命令触发优雅退出
	HeartbeatTick time.Duration
}

// Bot 是机器人实例：一个实例对应一次 KOOK 连接。
type Bot struct {
	deps Deps

	// csrfSecret 复用了 APP_SECRET，用于按钮 value 的 HMAC 签名。
	secret []byte

	mu       sync.RWMutex
	client   *kook.Client
	gateway  *kook.Gateway
	stop     context.CancelFunc
	status   Status
	botUser  kook.User
	guild    kook.Guild
	config   *Config
	configAt time.Time

	roleCache    *ttlCache
	channelCache *ttlCache

	// ticketLocks 保证同一频道/用户的工单操作串行执行。
	openLock  sync.Mutex
	closeLock sync.Mutex
}

// New 创建机器人实例。
func New(deps Deps) (*Bot, error) {
	if deps.Store == nil || deps.Tickets == nil {
		return nil, fmt.Errorf("必须提供 store 与 ticket 服务")
	}
	if deps.Bus == nil {
		deps.Bus = eventbus.New()
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Location == nil {
		deps.Location = time.UTC
	}
	if deps.Config == nil {
		return nil, fmt.Errorf("必须提供配置读取函数")
	}
	if len(deps.AppSecret) == 0 {
		return nil, fmt.Errorf("必须提供 APP_SECRET 用于按钮签名")
	}
	if deps.HeartbeatTick <= 0 {
		deps.HeartbeatTick = 30 * time.Second
	}
	return &Bot{
		deps:         deps,
		secret:       deps.AppSecret,
		roleCache:    newTTLCache(30 * time.Second),
		channelCache: newTTLCache(60 * time.Second),
	}, nil
}

// Start 建立连接并开始处理事件。
//
// 返回错误表示启动失败（例如 Token 无效）；调用方可以稍后重试。
func (b *Bot) Start(ctx context.Context) error {
	cfg, err := b.deps.Config(ctx)
	if err != nil {
		return fmt.Errorf("读取配置失败: %w", err)
	}
	if strings.TrimSpace(cfg.Token) == "" {
		b.setStatus(func(s *Status) {
			s.Running = false
			s.LastError = "尚未配置 KOOK Token"
		})
		return errors.New("尚未配置 KOOK Token")
	}
	if cfg.GuildID == "" {
		return errors.New("尚未配置服务器 ID")
	}

	client, err := kook.NewClient(kook.Options{
		Token:         cfg.Token,
		BaseURL:       cfg.APIBase,
		RatePerSecond: cfg.RatePerSecond,
		Burst:         cfg.RateBurst,
		Logger:        b.deps.Logger,
	})
	if err != nil {
		return err
	}

	// 校验 Token 并缓存机器人自身信息
	me, err := client.Me(ctx)
	if err != nil {
		return fmt.Errorf("校验 KOOK Token 失败: %w", err)
	}
	var guild kook.Guild
	if fetched, err := client.GuildView(ctx, cfg.GuildID); err != nil {
		b.deps.Logger.Warn("读取服务器信息失败（不影响连接）", "guild_id", cfg.GuildID, "err", err)
	} else {
		guild = *fetched
	}

	gateway, err := kook.NewGateway(kook.GatewayOptions{
		Client:            client,
		Compress:          true,
		Logger:            b.deps.Logger,
		HeartbeatInterval: b.deps.HeartbeatTick,
		OnEvent:           b.handleEvent,
		OnStatus: func(status kook.GatewayStatus) {
			b.setStatus(func(s *Status) {
				s.Connected = status.Connected
				s.SessionID = status.SessionID
				setStatusTime(&s.ConnectedAt, status.ConnectedAt)
				if status.Connected {
					setStatusNow(&s.StartedAt)
				}
				if status.LastError != "" {
					s.LastError = status.LastError
				}
			})
		},
	})
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)

	b.mu.Lock()
	b.client = client
	b.gateway = gateway
	b.stop = cancel
	b.botUser = *me
	b.guild = guild
	b.config = cfg
	b.configAt = store.Now()
	b.mu.Unlock()

	b.setStatus(func(s *Status) {
		s.Running = true
		s.BotID = me.ID
		s.BotName = me.FullName()
		s.GuildID = cfg.GuildID
		s.GuildName = guild.Name
		setStatusNow(&s.StartedAt)
		s.LastError = ""
	})

	// 把真实平台实现注入工单服务
	b.deps.Tickets.SetPlatform(&platform{b: b})

	go func() {
		defer func() {
			b.deps.Tickets.SetPlatform(ticket.NewNoopPlatform(b.deps.Logger))
			b.setStatus(func(s *Status) {
				s.Running = false
				s.Connected = false
			})
		}()
		if err := gateway.Run(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			b.deps.Logger.Warn("KOOK 网关已停止", "err", err)
			b.setStatus(func(s *Status) { s.LastError = err.Error() })
		}
	}()

	b.deps.Logger.Info("机器人已启动",
		"bot", me.FullName(), "guild", guild.Name, "guild_id", cfg.GuildID)
	return nil
}

// Stop 断开连接。
func (b *Bot) Stop() {
	b.mu.Lock()
	stop := b.stop
	b.stop = nil
	b.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// Status 返回当前状态。
func (b *Bot) Status() Status {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.status
}

// Client 返回底层客户端（未连接时为 nil）。
func (b *Bot) Client() *kook.Client {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.client
}

// GuildRoles 返回服务器角色（带 30s 缓存），供 WebUI 选择。
func (b *Bot) GuildRoles(ctx context.Context) ([]kook.Role, error) {
	client, cfg, err := b.ready()
	if err != nil {
		return nil, err
	}
	if cached, ok := b.roleCache.get("roles"); ok {
		return cached.([]kook.Role), nil
	}
	roles, err := client.GuildRoles(ctx, cfg.GuildID)
	if err != nil {
		return nil, err
	}
	b.roleCache.set("roles", roles)
	return roles, nil
}

// GuildChannels 返回服务器频道（带 60s 缓存），供 WebUI 选择。
func (b *Bot) GuildChannels(ctx context.Context) ([]kook.Channel, error) {
	client, cfg, err := b.ready()
	if err != nil {
		return nil, err
	}
	if cached, ok := b.channelCache.get("channels"); ok {
		return cached.([]kook.Channel), nil
	}
	channels, err := client.ChannelList(ctx, cfg.GuildID)
	if err != nil {
		return nil, err
	}
	b.channelCache.set("channels", channels)
	return channels, nil
}

// ready 返回可用的客户端与配置，未连接时返回错误。
func (b *Bot) ready() (*kook.Client, *Config, error) {
	b.mu.RLock()
	client, cfg := b.client, b.config
	b.mu.RUnlock()
	if client == nil || cfg == nil {
		return nil, nil, errors.New("机器人尚未连接 KOOK")
	}
	return client, cfg, nil
}

// currentConfig 返回配置快照；超过 10s 会重新读取，保证 WebUI 修改能生效。
func (b *Bot) currentConfig(ctx context.Context) (*Config, error) {
	b.mu.RLock()
	cfg, at := b.config, b.configAt
	b.mu.RUnlock()
	if cfg != nil && store.Now().Sub(at) < 10*time.Second {
		return cfg, nil
	}
	fresh, err := b.deps.Config(ctx)
	if err != nil {
		if cfg != nil {
			return cfg, nil
		}
		return nil, err
	}
	b.mu.Lock()
	b.config = fresh
	b.configAt = store.Now()
	b.mu.Unlock()
	return fresh, nil
}

func (b *Bot) setStatus(mutate func(*Status)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	mutate(&b.status)
}

// setStatusTime 安全地写入时间指针：零值置空，避免前端渲染 0001-01-01。
func setStatusTime(target **time.Time, value *time.Time) {
	if value == nil || value.IsZero() {
		*target = nil
		return
	}
	utc := value.UTC()
	*target = &utc
}

// setStatusNow 记录当前时间。
func setStatusNow(target **time.Time) {
	now := store.Now()
	*target = &now
}

// markEvent 记录一次事件处理。
func (b *Bot) markEvent() {
	b.setStatus(func(s *Status) {
		s.EventsHandled++
		setStatusNow(&s.LastEventAt)
	})
}

// ---------------------------------------------------------------------------
// 按钮 value 的签名与校验
// ---------------------------------------------------------------------------

// buttonValue 是按钮携带的数据。
//
// 设计原则：按钮的 value 来自客户端回传，**不可信**。
// 因此这里只放“动作类型 + 工单编号”，其余一律以服务端记录为准，
// 并用 HMAC 签名防止伪造（签名参与频道 ID，换频道即失效）。
type buttonValue struct {
	// Action 是动作类型：tk_open / tk_close / tk_lock / tk_reopen。
	Action string `json:"a"`
	// TicketNo 是工单编号（开单时为空，开单按钮与面板频道绑定）。
	TicketNo string `json:"n,omitempty"`
	// Signature 是 HMAC 签名。
	Signature string `json:"s"`
}

// 按钮动作类型。
const (
	actionOpen   = "tk_open"
	actionClose  = "tk_close"
	actionLock   = "tk_lock"
	actionReopen = "tk_reopen"
)

// signButton 为按钮值签名。
func (b *Bot) signButton(action, ticketNo, channelID string) string {
	return b.signature(action, ticketNo, channelID)
}

func (b *Bot) signature(action, ticketNo, channelID string) string {
	mac := hmac.New(sha256.New, b.secret)
	mac.Write([]byte(strings.Join([]string{action, ticketNo, channelID}, "|")))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// encodeButton 生成按钮 value 字符串。
func (b *Bot) encodeButton(action, ticketNo, channelID string) string {
	value := buttonValue{
		Action:    action,
		TicketNo:  ticketNo,
		Signature: b.signButton(action, ticketNo, channelID),
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// decodeButton 解析并校验按钮 value。
func (b *Bot) decodeButton(raw, channelID string) (*buttonValue, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("按钮数据为空")
	}
	var value buttonValue
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, fmt.Errorf("按钮数据格式非法: %w", err)
	}
	if value.Action == "" {
		return nil, errors.New("按钮数据缺少动作类型")
	}
	expected := b.signature(value.Action, value.TicketNo, channelID)
	if !hmac.Equal([]byte(expected), []byte(value.Signature)) {
		return nil, errors.New("按钮签名校验失败（可能来自伪造的客户端）")
	}
	return &value, nil
}

// ---------------------------------------------------------------------------
// 权限判定
// ---------------------------------------------------------------------------

// userRoles 返回用户在服务器中的角色 ID（30s 缓存）。
func (b *Bot) userRoles(ctx context.Context, userID string) ([]int64, error) {
	key := "user:" + userID
	if cached, ok := b.roleCache.get(key); ok {
		return cached.([]int64), nil
	}
	client, cfg, err := b.ready()
	if err != nil {
		return nil, err
	}
	user, err := client.UserView(ctx, userID, cfg.GuildID)
	if err != nil {
		return nil, err
	}
	b.roleCache.set(key, user.Roles)
	return user.Roles, nil
}

// isAdmin 判断用户是否具备处理工单的权限。
//
// 命中任一条件即通过：服务器创建者、全局管理员角色、该面板的频道级管理员角色。
// panelChannelID 为开单按钮所在频道（面板），可为空（此时只检查全局管理员）。
func (b *Bot) isAdmin(ctx context.Context, userID, panelChannelID string) bool {
	roles, err := b.userRoles(ctx, userID)
	if err != nil {
		b.deps.Logger.Warn("读取用户角色失败，默认无权限", "user_id", userID, "err", err)
		return false
	}

	b.mu.RLock()
	masterID := b.guild.MasterID
	b.mu.RUnlock()
	if masterID != "" && userID == masterID {
		return true
	}

	adminRoles, err := b.deps.Store.Roles.ListAdmin()
	if err != nil {
		b.deps.Logger.Error("读取全局管理员角色失败", "err", err)
		return false
	}
	if intersects(roles, adminRoles) {
		return true
	}

	if panelChannelID != "" {
		panel, err := b.deps.Store.Panels.ByChannel(panelChannelID)
		if err == nil {
			for _, panelRole := range panel.Roles {
				for _, roleID := range roles {
					if panelRole.RoleID == fmt.Sprint(roleID) {
						return true
					}
				}
			}
		}
	}
	return false
}

// intersects 判断用户角色与管理员角色是否相交。
func intersects(userRoles []int64, adminRoles []store.AdminRole) bool {
	if len(adminRoles) == 0 {
		return false
	}
	adminSet := make(map[string]struct{}, len(adminRoles))
	for _, role := range adminRoles {
		adminSet[role.RoleID] = struct{}{}
	}
	for _, roleID := range userRoles {
		if _, ok := adminSet[fmt.Sprint(roleID)]; ok {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 对外操作（供 WebUI 调用）
// ---------------------------------------------------------------------------

// RefreshPanels 清理频道缓存，在 WebUI 修改面板后调用。
func (b *Bot) RefreshPanels() { b.channelCache.clear() }

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

// ttlCache 是极简的带过期缓存。
type ttlCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]cacheItem
}

type cacheItem struct {
	value   any
	expires time.Time
}

func newTTLCache(ttl time.Duration) *ttlCache {
	return &ttlCache{ttl: ttl, items: make(map[string]cacheItem)}
}

func (c *ttlCache) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.items[key]
	if !ok || time.Now().After(item.expires) {
		return nil, false
	}
	return item.value, true
}

func (c *ttlCache) set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = cacheItem{value: value, expires: time.Now().Add(c.ttl)}
}

func (c *ttlCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]cacheItem)
}
