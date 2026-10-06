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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/eventbus"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/keyedlock"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/ticket"
)

// ErrNotConnected 表示机器人当前没有可用的 KOOK 连接。
//
// 上层（API 层）依赖这个哨兵错误把“未连接”映射为 503，而不是 500，
// 以便 WebUI 提示用户先完成配置并重连。
var ErrNotConnected = errors.New("机器人尚未连接 KOOK")

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

	// openLocks 保证「同一个用户同时只开一单」：按用户加锁，不同用户可以并行开单。
	openLocks keyedlock.Locks

	// events 把网关事件从读取协程解耦到分片处理协程（见 dispatch.go）。
	events *eventDispatcher

	// gatewayDone 在网关协程退出时关闭，供 Stop 等待连接真正断开。
	gatewayDone chan struct{}
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

// 网关事件的分片处理见 dispatch.go。

// enqueueEvent 是网关的 OnEvent 回调：只做入队，绝不阻塞读取协程。
func (b *Bot) enqueueEvent(ctx context.Context, event kook.Event) {
	b.mu.RLock()
	dispatcher := b.events
	b.mu.RUnlock()
	if dispatcher == nil {
		// 尚未初始化（理论上不会发生）：退化为同步处理，保证不丢事件。
		b.handleEvent(ctx, event)
		return
	}
	dispatcher.enqueue(ctx, event)
}

// Start 建立连接并开始处理事件。
//
// 返回错误表示启动失败（例如 Token 无效）；调用方可以稍后重试。
//
// ctx 只约束本次建连的准备工作（读配置、校验 Token、拉取服务器信息）：
// 调用方取消或超时会让 Start 立刻失败。连接建立后网关在后台独立运行，
// 直到 Stop 被调用 —— 这一点非常关键：WebUI 的「配置变更后重连 / 重新连接」
// 传入的是 HTTP 请求上下文，请求处理结束时它就会被取消；
// 若网关直接继承该上下文，建连过程中就会收到 context canceled
// （表现为“获取网关地址失败: 请求 KOOK 接口失败: ... context canceled”）
// 并且连接永久停止，直到管理员再次点击重连。
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
		OnEvent:           b.enqueueEvent,
		// 会话落库：升级/重建容器后仍带旧会话 resume。
		// 若不以旧会话续传，平台会把事件继续投递到尚未过期的旧会话上，
		// 表现为「WebUI 显示已连接，但点击按钮没有任何反应」。
		SessionStore: b.deps.Store.Settings,
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
			// 每次连接建立（含网关自动重连）都尝试恢复在玩动态：
			// KOOK 的动态绑定在网关上，重新握手后会丢失。
			if status.Connected {
				go b.restoreActivity()
			}
		},
	})
	if err != nil {
		return err
	}

	// 建连准备工作已完成，从调用方上下文中“解绑”取消信号（保留其取值）：
	// 连接的存活只由 Stop 决定，不再随触发启动的请求结束而中断。
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	gatewayDone := make(chan struct{})
	dispatcher := newEventDispatcher(eventShards, eventQueueSize, b.handleEvent, nil, b.deps.Logger)
	dispatcher.start(runCtx)

	b.mu.Lock()
	b.client = client
	b.gateway = gateway
	b.stop = cancel
	b.gatewayDone = gatewayDone
	b.events = dispatcher
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
		defer close(gatewayDone)
		defer func() {
			b.deps.Tickets.SetPlatform(nil)
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
//
// 会等到网关协程真正退出（socket 已关闭）再返回：WebUI 的「重新连接」
// 紧随其后就会建立新连接，若旧连接仍在，平台上会同时存在两个会话，
// 事件可能被投递到即将消失的那一个。超时（5s）只记录日志，不阻塞退出。
func (b *Bot) Stop() {
	b.mu.Lock()
	stop := b.stop
	done := b.gatewayDone
	b.stop = nil
	b.gatewayDone = nil
	b.mu.Unlock()
	if stop != nil {
		stop()
	}
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		b.deps.Logger.Warn("等待网关断开超时，继续后续操作")
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

// ---------------------------------------------------------------------------
// 游戏库与在玩动态（供 WebUI 调用）
// ---------------------------------------------------------------------------

// GameList 拉取游戏库；未连接时报错。
func (b *Bot) GameList(ctx context.Context, gameType int) ([]kook.Game, error) {
	client, _, err := b.ready()
	if err != nil {
		return nil, err
	}
	return client.GameList(ctx, gameType)
}

// GameCreate 新建游戏；未连接时报错。
func (b *Bot) GameCreate(ctx context.Context, name, icon string) (*kook.Game, error) {
	client, _, err := b.ready()
	if err != nil {
		return nil, err
	}
	return client.GameCreate(ctx, name, icon)
}

// GameUpdate 更新游戏名称或图标；未连接时报错。
func (b *Bot) GameUpdate(ctx context.Context, id int64, name, icon string) (*kook.Game, error) {
	client, _, err := b.ready()
	if err != nil {
		return nil, err
	}
	return client.GameUpdate(ctx, id, name, icon)
}

// GameDelete 删除游戏；未连接时报错。
func (b *Bot) GameDelete(ctx context.Context, id int64) error {
	client, _, err := b.ready()
	if err != nil {
		return err
	}
	return client.GameDelete(ctx, id)
}

// StartGameActivity 让机器人开始玩游戏；未连接时报错。
func (b *Bot) StartGameActivity(ctx context.Context, gameID int64) error {
	client, _, err := b.ready()
	if err != nil {
		return err
	}
	return client.StartGameActivity(ctx, gameID)
}

// StartMusicActivity 让机器人开始听歌；未连接时报错。
func (b *Bot) StartMusicActivity(ctx context.Context, musicName, singer, software string) error {
	client, _, err := b.ready()
	if err != nil {
		return err
	}
	return client.StartMusicActivity(ctx, musicName, singer, software)
}

// DeleteActivity 停止指定类型的动态；未连接时报错。
func (b *Bot) DeleteActivity(ctx context.Context, dataType int) error {
	client, _, err := b.ready()
	if err != nil {
		return err
	}
	return client.DeleteActivity(ctx, dataType)
}

// restoreActivity 在连接建立后按持久化的“期望动态”重新设置在玩状态。
//
// 失败只记日志：恢复是尽力而为，不应影响机器人其它功能。
func (b *Bot) restoreActivity() {
	if !b.deps.Store.Settings.ActivityAutoRestore() {
		return
	}
	activity, err := b.deps.Store.Activity.Current()
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			b.deps.Logger.Warn("读取待恢复的动态失败", "err", err)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var restoreErr error
	switch activity.DataType {
	case kook.ActivityTypeGame:
		restoreErr = b.StartGameActivity(ctx, activity.GameID)
	case kook.ActivityTypeMusic:
		restoreErr = b.StartMusicActivity(ctx, activity.MusicName, activity.Singer, activity.Software)
	default:
		return
	}
	if restoreErr != nil {
		b.deps.Logger.Warn("自动恢复在玩状态失败",
			"data_type", kook.ActivityTypeName(activity.DataType), "err", restoreErr)
		return
	}
	b.deps.Logger.Info("已自动恢复在玩状态",
		"data_type", kook.ActivityTypeName(activity.DataType),
		"game_id", activity.GameID, "music", activity.MusicName)
}

// ready 返回可用的客户端与配置，未连接时返回错误。
func (b *Bot) ready() (*kook.Client, *Config, error) {
	b.mu.RLock()
	client, cfg := b.client, b.config
	b.mu.RUnlock()
	if client == nil || cfg == nil {
		return nil, nil, ErrNotConnected
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
// 因此这里只放“动作类型 + 工单编号 + 面板 ID”，其余一律以服务端记录为准，
// 并用 HMAC 签名防止伪造（签名参与频道 ID 与面板 ID，串改即失效）。
type buttonValue struct {
	// Action 是动作类型：tk_open / tk_close / tk_lock / tk_reopen。
	Action string `json:"a"`
	// TicketNo 是工单编号（开单时为空，开单按钮与面板频道绑定）。
	TicketNo string `json:"n,omitempty"`
	// PanelID 是开单按钮所属的面板记录 ID（旧卡片为空，回退到频道内第一条面板）。
	PanelID uint `json:"p,omitempty"`
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

// signature 对“动作 + 工单编号 + 频道 + 面板 ID”做 HMAC 签名。
//
// panelID 为 0 时与 legacySignature 不同（多一段分隔符），这是有意为之：
// 新旧签名不会互相伪造，兼容逻辑见 decodeButton。
func (b *Bot) signature(action, ticketNo, channelID string, panelID uint) string {
	return b.macHex(action, ticketNo, channelID, strconv.FormatUint(uint64(panelID), 10))
}

// legacySignature 是旧版本（不含面板 ID）的签名算法，仅用于验证升级前发出的卡片。
func (b *Bot) legacySignature(action, ticketNo, channelID string) string {
	return b.macHex(action, ticketNo, channelID)
}

func (b *Bot) macHex(parts ...string) string {
	mac := hmac.New(sha256.New, b.secret)
	mac.Write([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// encodeButton 生成按钮 value 字符串。panelID 仅开单按钮使用，其它动作传 0。
func (b *Bot) encodeButton(action, ticketNo, channelID string, panelID uint) string {
	value := buttonValue{
		Action:    action,
		TicketNo:  ticketNo,
		PanelID:   panelID,
		Signature: b.signature(action, ticketNo, channelID, panelID),
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// decodeButton 解析并校验按钮 value。
//
// 兼容性：升级前发出的卡片不含面板 ID，签名也采用旧算法。
// 此时回退到旧签名验证，并把 PanelID 清零，由调用方按频道解析面板，
// 避免伪造的 panelID 绕过签名。
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
	if hmac.Equal([]byte(b.signature(value.Action, value.TicketNo, channelID, value.PanelID)), []byte(value.Signature)) {
		return &value, nil
	}
	if hmac.Equal([]byte(b.legacySignature(value.Action, value.TicketNo, channelID)), []byte(value.Signature)) {
		value.PanelID = 0
		return &value, nil
	}
	return nil, errors.New("按钮签名校验失败（可能来自伪造的客户端）")
}

// ---------------------------------------------------------------------------
// 权限判定
// ---------------------------------------------------------------------------

// userInfo 返回用户在服务器中的信息（30s 缓存）。
//
// 权限判定与卡片文案都要用到它，缓存后一次工单操作只需一次 user/view。
func (b *Bot) userInfo(ctx context.Context, userID string) (*kook.User, error) {
	key := "user:" + userID
	if cached, ok := b.roleCache.get(key); ok {
		return cached.(*kook.User), nil
	}
	client, cfg, err := b.ready()
	if err != nil {
		return nil, err
	}
	user, err := client.UserView(ctx, userID, cfg.GuildID)
	if err != nil {
		return nil, err
	}
	b.roleCache.set(key, user)
	return user, nil
}

// userRoles 返回用户在服务器中的角色 ID（30s 缓存）。
func (b *Bot) userRoles(ctx context.Context, userID string) ([]int64, error) {
	user, err := b.userInfo(ctx, userID)
	if err != nil {
		return nil, err
	}
	return user.Roles, nil
}

// isAdmin 判断用户是否具备处理某频道工单的权限。
//
// 命中任一条件即通过：服务器创建者、全局管理员角色、该频道内面板所属工单类型的类型角色。
// panelChannelID 为开单按钮所在频道（面板），可为空（此时只检查全局管理员）。
func (b *Bot) isAdmin(ctx context.Context, userID, panelChannelID string) bool {
	var types []store.TicketType
	if panelChannelID != "" {
		items, err := b.deps.Store.Types.ListByChannel(panelChannelID)
		if err != nil {
			b.deps.Logger.Error("读取频道内的工单类型失败", "channel_id", panelChannelID, "err", err)
		}
		types = items
	}
	return b.isAdminWithTypes(ctx, userID, types)
}

// isTicketAdmin 判断用户是否可以处理某张具体工单。
//
// 优先用开单时落库的类型快照判定（类型即使被停用，处理中的工单仍需能关闭）；
// 类型已被删除或旧数据缺失时，退化为按来源频道解析类型。
func (b *Bot) isTicketAdmin(ctx context.Context, userID string, t *store.Ticket) bool {
	var types []store.TicketType
	if t.TypeID != nil {
		if item, err := b.deps.Store.Types.ByID(*t.TypeID); err == nil {
			types = append(types, *item)
		} else if !errors.Is(err, store.ErrNotFound) {
			b.deps.Logger.Warn("读取工单类型失败", "ticket_no", t.No, "type_id", *t.TypeID, "err", err)
		}
	}
	if len(types) == 0 && t.SourceChannelID != "" {
		items, err := b.deps.Store.Types.ListByChannel(t.SourceChannelID)
		if err != nil {
			b.deps.Logger.Error("读取来源频道的工单类型失败", "channel_id", t.SourceChannelID, "err", err)
		}
		types = items
	}
	return b.isAdminWithTypes(ctx, userID, types)
}

// isAdminWithTypes 是权限判定的核心：服务器创建者 / 全局管理员角色 / 给定类型的管理员角色。
func (b *Bot) isAdminWithTypes(ctx context.Context, userID string, types []store.TicketType) bool {
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

	// 工单类型角色：命中任一类型的任一角色即视为管理员。
	for _, item := range types {
		for _, typeRole := range item.Roles {
			for _, roleID := range roles {
				if typeRole.RoleID == fmt.Sprint(roleID) {
					return true
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
	mu        sync.Mutex
	ttl       time.Duration
	items     map[string]cacheItem
	nextSweep time.Time
}

const maxCacheItems = 2048

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
	if !ok {
		return nil, false
	}
	if !time.Now().Before(item.expires) {
		delete(c.items, key)
		return nil, false
	}
	return item.value, true
}

func (c *ttlCache) set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if !now.Before(c.nextSweep) || len(c.items) >= maxCacheItems {
		for k, item := range c.items {
			if !now.Before(item.expires) {
				delete(c.items, k)
			}
		}
		c.nextSweep = now.Add(time.Minute)
	}
	if _, exists := c.items[key]; !exists && len(c.items) >= maxCacheItems {
		var oldest string
		var expires time.Time
		for k, item := range c.items {
			if expires.IsZero() || item.expires.Before(expires) {
				oldest, expires = k, item.expires
			}
		}
		delete(c.items, oldest)
	}
	c.items[key] = cacheItem{value: value, expires: now.Add(c.ttl)}
}

func (c *ttlCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]cacheItem)
}
