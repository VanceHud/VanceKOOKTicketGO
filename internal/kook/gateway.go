package kook

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// GatewayStatus 描述网关连接状态，供 WebUI 展示。
type GatewayStatus struct {
	Connected      bool       `json:"connected"`
	SessionID      string     `json:"sessionId,omitempty"`
	LastSN         int64      `json:"lastSn"`
	Attempt        int        `json:"attempt"`
	LastError      string     `json:"lastError,omitempty"`
	ConnectedAt    *time.Time `json:"connectedAt,omitempty"`
	EventsReceived int64      `json:"eventsReceived"`
}

// GatewayOptions 是网关构造参数。
type GatewayOptions struct {
	Client *Client
	// Compress 为真时向平台申请 zlib 压缩下行数据（推荐）。
	Compress bool
	Logger   *slog.Logger

	// OnEvent 在收到事件时被调用；实现应当快速返回，耗时操作请自行起协程。
	OnEvent func(ctx context.Context, event Event)
	// OnStatus 在连接状态变化时被调用。
	OnStatus func(status GatewayStatus)

	// SessionStore 可选：持久化 session_id 与已处理到的 sn。
	//
	// KOOK 的事件按会话投递：进程重启（升级、重建容器）时若不带上旧会话 resume，
	// 平台会新建一个会话，而离线期间的事件仍会继续投递到尚未过期的旧会话，
	// 表现为「WebUI 显示已连接，但点击按钮、发消息都没有任何反应」。
	// 官方文档同样建议把 session_id 与 sn 落盘，以便代码升级重启后恢复会话。
	// 传 nil 表示不做持久化（仅进程内断线续传）。
	SessionStore SessionStore

	// HeartbeatInterval 默认 30s（与官方文档一致）。
	HeartbeatInterval time.Duration
	// PongTimeout 默认 6s：超过该时间未收到 PONG 视为断线。
	PongTimeout time.Duration
	// ResumeSilenceTimeout 默认 60s：带旧会话续传时，若这段时间内既没有事件也收不到
	// 平台的 resumeOK(s=6)，就认为这次续传并未生效（事件仍在旧会话里），
	// 主动断开并改用全新会话重连。
	ResumeSilenceTimeout time.Duration
	// BaseBackoff 默认 2s：重连退避序列 2s、4s、8s…（与官方文档一致）。
	BaseBackoff time.Duration
	// MaxBackoff 默认 60s。
	MaxBackoff time.Duration
	// DialTimeout 默认 15s。
	DialTimeout time.Duration
}

// SessionStore 持久化网关会话，用于跨进程重启（升级、重建容器）恢复会话。
//
// 网关在 HELLO 之后、以及每次 OnEvent 回调返回后写入 sn；续传时平台从该 sn
// 之后补发事件。注意：若 OnEvent 只做入队（异步处理，见 bot 包的事件分发），
// 崩溃时队列里尚未处理的事件不会被平台重新投递，这个取舍由调用方决定。
type SessionStore interface {
	LoadGatewaySession() (sessionID string, sn int64, err error)
	// SaveGatewaySession 写入会话；sessionID 为空表示清空（会话已失效）。
	SaveGatewaySession(sessionID string, sn int64) error
}

// Gateway 是 KOOK WebSocket 网关客户端。
//
// 行为对齐官方文档：
//  1. 获取网关地址（可带压缩）
//  2. 连接后等待 HELLO，从中取得 session_id
//  3. 每 30s 发送一次心跳 PING，6s 内未收到 PONG 视为超时
//  4. 断线时使用 resume=1&session_id=..&sn=.. 续传，避免丢事件；
//     会话可通过 SessionStore 落库，进程重启后同样续传
//  5. 失败按 2s、4s、8s… 指数退避重试，上限 60s
//  6. 续传失败时（HELLO 错误码，或握手阶段/连接中收到 reconnect(s=5)）
//     清空本地 session_id 与 sn，以全新会话重连
type Gateway struct {
	opts GatewayOptions

	mu        sync.Mutex
	sessionID string
	lastSN    int64

	connectedAt    atomic.Int64
	eventsReceived atomic.Int64
	attempt        atomic.Int32
	lastError      atomic.Value // string

	// 以下三个字段只服务于「续传是否真的生效」的检测，每次连接前重置：
	// resumeAck 表示已收到平台补发完成信号 resumeOK(s=6)；
	// lastEventAt 是最近一次收到事件或 resumeOK 的时间（心跳 PONG 不算）；
	// resumeSilent 由看门狗置位，表示续传连接长时间没有任何下行数据。
	resumeAck    atomic.Bool
	lastEventAt  atomic.Int64
	resumeSilent atomic.Bool

	pongCh chan struct{}
}

// NewGateway 创建网关客户端。
func NewGateway(opts GatewayOptions) (*Gateway, error) {
	if opts.Client == nil {
		return nil, fmt.Errorf("必须提供 KOOK 客户端")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = 30 * time.Second
	}
	if opts.PongTimeout <= 0 {
		opts.PongTimeout = 6 * time.Second
	}
	if opts.ResumeSilenceTimeout <= 0 {
		opts.ResumeSilenceTimeout = 60 * time.Second
	}
	if opts.BaseBackoff <= 0 {
		opts.BaseBackoff = 2 * time.Second
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 60 * time.Second
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 15 * time.Second
	}
	g := &Gateway{opts: opts, pongCh: make(chan struct{}, 1)}
	g.lastError.Store("")
	g.restoreSession()
	return g, nil
}

// restoreSession 载入上次持久化的会话，让进程重启后仍能续传。
func (g *Gateway) restoreSession() {
	if g.opts.SessionStore == nil {
		return
	}
	sessionID, sn, err := g.opts.SessionStore.LoadGatewaySession()
	if err != nil {
		g.opts.Logger.Warn("读取持久化的网关会话失败，将以全新会话连接", "err", err)
		return
	}
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	g.sessionID, g.lastSN = sessionID, sn
	g.opts.Logger.Info("已恢复上次的网关会话，将断点续传", "session_id", sessionID, "sn", sn)
}

// Status 返回当前连接状态。
func (g *Gateway) Status() GatewayStatus {
	g.mu.Lock()
	sessionID, sn := g.sessionID, g.lastSN
	g.mu.Unlock()

	status := GatewayStatus{
		SessionID:      sessionID,
		LastSN:         sn,
		Attempt:        int(g.attempt.Load()),
		EventsReceived: g.eventsReceived.Load(),
	}
	if raw := g.lastError.Load(); raw != nil {
		status.LastError, _ = raw.(string)
	}
	if ts := g.connectedAt.Load(); ts > 0 {
		connectedAt := time.Unix(0, ts).UTC()
		status.ConnectedAt = &connectedAt
		status.Connected = true
	}
	return status
}

// Run 启动网关：持续重连直到 ctx 结束。
func (g *Gateway) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			g.setDisconnected("客户端已停止")
			return err
		}

		err := g.serve(ctx)
		if ctx.Err() != nil {
			// 主动停止：关闭 socket 引发的错误不属于故障，不写入 LastError
			g.setDisconnected("客户端已停止")
			return ctx.Err()
		}
		g.setDisconnected(errorText(err))

		g.attempt.Add(1)
		backoff := g.backoff()
		g.opts.Logger.Warn("KOOK 网关连接中断，准备重连",
			"attempt", g.attempt.Load(), "backoff", backoff.String(), "err", err)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
}

// backoff 计算指数退避时长：2s、4s、8s…上限 MaxBackoff。
func (g *Gateway) backoff() time.Duration {
	// attempt 记录的是「已失败次数」，因此第 1 次失败退避 BaseBackoff（2s），
	// 与官方文档的 2、4、8… 序列一致。
	failures := g.attempt.Load()
	if failures < 1 {
		failures = 1
	}
	exponent := failures - 1
	if exponent > 5 {
		exponent = 5
	}
	d := g.opts.BaseBackoff * time.Duration(1<<uint(exponent))
	if d > g.opts.MaxBackoff {
		d = g.opts.MaxBackoff
	}
	return d
}

// persistSession 把当前会话与已处理到的 sn 写入持久化存储。
//
// 失败只记日志：落库是「重启后仍能收到事件」的增强，不应影响当前连接。
func (g *Gateway) persistSession() {
	if g.opts.SessionStore == nil {
		return
	}
	g.mu.Lock()
	sessionID, sn := g.sessionID, g.lastSN
	g.mu.Unlock()
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	if err := g.opts.SessionStore.SaveGatewaySession(sessionID, sn); err != nil {
		g.opts.Logger.Warn("持久化网关会话失败", "session_id", sessionID, "sn", sn, "err", err)
	}
}

// clearSession 清空本地与持久化的会话记录。
//
// 使用场景：平台要求重连（s=5）、续传被平台拒绝（会话已过期）。
// 此时必须彻底丢弃旧会话，否则每次重连都会带着同一个失效会话，
// 平台侧看起来「已连接」，却永远收不到任何事件。
func (g *Gateway) clearSession() {
	g.mu.Lock()
	g.sessionID, g.lastSN = "", 0
	g.mu.Unlock()
	if g.opts.SessionStore != nil {
		if err := g.opts.SessionStore.SaveGatewaySession("", 0); err != nil {
			g.opts.Logger.Warn("清空持久化的网关会话失败", "err", err)
		}
	}
}

// watchResumeSilence 监控一次续传是否真的生效。
//
// 平台受理 resume 后会补发离线事件，并以 resumeOK(s=6) 收尾。如果整条连接
// 长时间连一个事件、一个 resumeOK 都没有，说明续传实际上挂在了旧会话上，
// 平台侧依旧是「已连接」但永远不会投递事件（线上表现为点击按钮毫无反应）。
// 这里只负责断开连接：清空会话与重建连接由 serve / Run 完成，
// 这样即使看门狗晚一步醒来，也只会关掉自己那条已经废弃的连接。
func (g *Gateway) watchResumeSilence(ctx context.Context, conn *websocket.Conn) {
	interval := g.opts.ResumeSilenceTimeout / 4
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	if interval > 5*time.Second {
		interval = 5 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if g.resumeAck.Load() {
			return
		}
		last := g.lastEventAt.Load()
		if last > 0 && time.Since(time.Unix(0, last)) < g.opts.ResumeSilenceTimeout {
			continue
		}
		g.resumeSilent.Store(true)
		_ = conn.Close()
		return
	}
}

// serve 建立一次连接并处理消息，直到连接断开。
func (g *Gateway) serve(ctx context.Context) error {
	// 重置续传检测状态：resumeOK 与事件都会刷新 lastEventAt。
	g.resumeAck.Store(false)
	g.resumeSilent.Store(false)
	g.lastEventAt.Store(time.Now().UnixNano())

	gatewayURL, err := g.opts.Client.GatewayURL(ctx, g.opts.Compress)
	if err != nil {
		return fmt.Errorf("获取网关地址失败: %w", err)
	}

	// 断线续传：带上 session_id 与 sn，平台会补发缺失事件。
	g.mu.Lock()
	sessionID, lastSN := g.sessionID, g.lastSN
	g.mu.Unlock()
	if sessionID != "" {
		gatewayURL = appendQuery(gatewayURL, url.Values{
			"resume":     {"1"},
			"session_id": {sessionID},
			"sn":         {fmt.Sprint(lastSN)},
		})
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: g.opts.DialTimeout,
		Proxy:            nil,
	}
	conn, resp, err := dialer.DialContext(ctx, gatewayURL, nil)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("连接网关失败（HTTP %d）: %w", resp.StatusCode, err)
		}
		return fmt.Errorf("连接网关失败: %w", err)
	}
	defer func() { _ = conn.Close() }()
	conn.SetReadLimit(maxGatewayFrameBytes)
	if err := conn.SetReadDeadline(time.Now().Add(g.opts.DialTimeout)); err != nil {
		return err
	}

	// ctx 结束时主动关闭连接：ReadMessage 在没有读超时的情况下不会因 ctx
	// 被取消而返回，不主动关闭的话，Stop/重连后旧连接会一直挂着
	// （在平台上表现为一个永不消失的会话）。
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()

	// 先读取 HELLO，确认握手成功并拿到 session_id。
	// 注意：带失效会话续传时，平台会直接下发 reconnect(s=5) 而不是
	// 带错误码的 HELLO，必须在这里识别并丢弃旧会话（见 readHello）。
	hello, err := g.readHello(conn)
	if err != nil {
		return err
	}
	var helloData struct {
		Code      int    `json:"code"`
		SessionID string `json:"session_id"`
	}
	if len(hello.Data) > 0 {
		_ = json.Unmarshal(hello.Data, &helloData)
	}
	if helloData.Code != 0 {
		if sessionID != "" {
			// 续传被拒（会话已过期、sn 无效等）：清空本地会话，
			// 下一次连接改用全新会话，否则会永远带着同一个失效会话重试。
			g.opts.Logger.Warn("续传网关会话被平台拒绝，将改用全新会话",
				"session_id", sessionID, "code", helloData.Code)
			g.clearSession()
		}
		return fmt.Errorf("握手被平台拒绝：code=%d", helloData.Code)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return err
	}

	g.mu.Lock()
	g.sessionID = helloData.SessionID
	g.mu.Unlock()
	// 握手成功即落库：此时起的任何事件丢失都能靠下一次 resume 补回来。
	g.persistSession()

	g.connectedAt.Store(time.Now().UnixNano())
	g.attempt.Store(0)
	g.lastError.Store("")
	g.opts.Logger.Info("KOOK 网关已连接", "session_id", helloData.SessionID, "resume", sessionID != "")
	g.emitStatus()

	// 心跳协程：超时未收到 PONG 则主动断开，触发重连
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	go g.heartbeatLoop(heartbeatCtx, conn)

	// 续传保护：平台受理 resume 后会补发离线事件并以 resumeOK 收尾。
	// 若一直什么都没有，说明续传没生效，主动断开改用全新会话。
	if sessionID != "" {
		watchCtx, stopWatch := context.WithCancel(ctx)
		defer stopWatch()
		go g.watchResumeSilence(watchCtx, conn)
	}

	for {
		frame, err := g.readFrame(conn)
		if err != nil {
			if g.resumeSilent.Load() {
				g.opts.Logger.Warn("续传会话长时间没有任何下行数据，改用全新会话重连",
					"session_id", sessionID, "silence_timeout", g.opts.ResumeSilenceTimeout.String())
				g.clearSession()
			}
			return err
		}

		switch frame.Signal {
		case SignalEvent:
			g.lastEventAt.Store(time.Now().UnixNano())
			g.mu.Lock()
			if frame.SN > g.lastSN {
				g.lastSN = frame.SN
			}
			g.mu.Unlock()

			if len(frame.Data) == 0 {
				continue
			}
			var event Event
			if err := json.Unmarshal(frame.Data, &event); err != nil {
				g.opts.Logger.Warn("解析事件失败", "err", err)
				continue
			}
			g.eventsReceived.Add(1)
			if g.opts.OnEvent != nil {
				g.opts.OnEvent(ctx, event)
			}
			// 回调返回后立即落库：崩溃/重启时从这条事件之后续传。
			// （OnEvent 若只是入队异步处理，见 SessionStore 的说明。）
			g.persistSession()

		case SignalPong:
			select {
			case g.pongCh <- struct{}{}:
			default:
			}

		case SignalReconnect:
			// 平台要求重连：按官方文档清空 sn 与会话后回到第 1 步。
			// 带着已失效的会话反复 resume，平台侧依旧显示已连接，
			// 但不会再有任何事件——必须彻底重建会话。
			reason := reconnectReason(frame.Data)
			g.opts.Logger.Warn("平台要求重新建立连接，已清空本地网关会话", "reason", reason)
			g.clearSession()
			return fmt.Errorf("平台要求重连（s=5，%s）", reason)

		case SignalResumeAck:
			g.resumeAck.Store(true)
			g.lastEventAt.Store(time.Now().UnixNano())
			g.opts.Logger.Info("断线续传成功")

		case SignalHello:
			// 续传过程中可能再次收到 HELLO，忽略即可
			continue

		default:
			g.opts.Logger.Debug("收到未处理的网关信号", "signal", frame.Signal)
		}
	}
}

// readHello 等待并解析 HELLO。
//
// 续传失败时平台的回复有两种形态：带错误码的 HELLO，或直接下发
// reconnect（s=5，d.code 为 40106/40107/40108；官方文档「信令[5] RECONNECT」
// 把 resume 失败归在这条信令下）。后者如果不在握手阶段识别，就会带着同一个
// 失效会话反复重连，日志里不断出现「握手失败：期望 HELLO(s=1)，收到 s=5」，
// 机器人永远收不到事件。
//
// 因此这里对 s=5 一律按「会话已失效」处理：清空本地会话后返回错误，
// 由 Run 退避后以全新会话重连。其余非 HELLO 信令（PONG 等）在握手阶段本不该
// 出现，忽略并继续等待即可——读取截止时间在整段握手期间始终生效，不会挂死。
func (g *Gateway) readHello(conn *websocket.Conn) (rawFrame, error) {
	for {
		frame, err := g.readFrame(conn)
		if err != nil {
			return rawFrame{}, fmt.Errorf("读取 HELLO 失败: %w", err)
		}
		switch frame.Signal {
		case SignalHello:
			return frame, nil
		case SignalReconnect:
			reason := reconnectReason(frame.Data)
			g.opts.Logger.Warn("连接建立后即收到 reconnect（s=5），本地会话已失效，将改用全新会话重连", "reason", reason)
			g.clearSession()
			return rawFrame{}, fmt.Errorf("平台要求重连（s=5，%s）", reason)
		default:
			g.opts.Logger.Debug("等待 HELLO 期间收到其他信令，继续等待", "signal", frame.Signal)
		}
	}
}

// reconnectReason 解析 reconnect（s=5）信令的数据体，用日志与错误信息说明原因。
// 常见 code：40106 缺少参数、40107 会话已过期、40108 sn 无效。
func reconnectReason(data json.RawMessage) string {
	var detail struct {
		Code int    `json:"code"`
		Err  string `json:"err"`
	}
	if len(data) == 0 || json.Unmarshal(data, &detail) != nil {
		return "平台未说明原因"
	}
	switch {
	case detail.Code == 0 && detail.Err == "":
		return "平台未说明原因"
	case detail.Err == "":
		return fmt.Sprintf("code=%d", detail.Code)
	case detail.Code == 0:
		return detail.Err
	default:
		return fmt.Sprintf("code=%d %s", detail.Code, detail.Err)
	}
}

// heartbeatLoop 发送心跳并校验 PONG。
func (g *Gateway) heartbeatLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(g.opts.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		g.mu.Lock()
		sn := g.lastSN
		g.mu.Unlock()

		payload, err := json.Marshal(map[string]any{"s": SignalPing, "sn": sn})
		if err != nil {
			continue
		}
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			g.opts.Logger.Debug("发送心跳失败", "err", err)
			_ = conn.Close()
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-g.pongCh:
		case <-time.After(g.opts.PongTimeout):
			g.opts.Logger.Warn("心跳超时未收到 PONG，主动断开以触发重连", "pong_timeout", g.opts.PongTimeout.String())
			_ = conn.Close()
			return
		}
	}
}

// rawFrame 是网关下发的原始帧。
type rawFrame struct {
	Signal int             `json:"s"`
	SN     int64           `json:"sn"`
	Data   json.RawMessage `json:"d"`
}

const maxGatewayFrameBytes = 8 << 20

// readFrame 读取并解析一帧（必要时解压）。
func (g *Gateway) readFrame(conn *websocket.Conn) (rawFrame, error) {
	messageType, payload, err := conn.ReadMessage()
	if err != nil {
		return rawFrame{}, err
	}

	raw := payload
	if messageType == websocket.BinaryMessage {
		decompressed, err := decompress(payload)
		if err != nil {
			return rawFrame{}, fmt.Errorf("解压网关数据失败: %w", err)
		}
		raw = decompressed
	}

	var frame rawFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		return rawFrame{}, fmt.Errorf("解析网关数据失败: %w", err)
	}
	return frame, nil
}

// decompress 解压 KOOK 下发的 zlib(deflate) 数据。
//
// 文档说明数据是 zlib 压缩，但为兼容不同实现，这里先按 zlib（含头）解压，
// 失败再回退到 raw deflate。
func decompress(payload []byte) ([]byte, error) {
	if reader, err := zlib.NewReader(bytes.NewReader(payload)); err == nil {
		defer func() { _ = reader.Close() }()
		return readGatewayPayload(reader)
	}
	reader := flate.NewReader(bytes.NewReader(payload))
	defer func() { _ = reader.Close() }()
	return readGatewayPayload(reader)
}

func readGatewayPayload(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxGatewayFrameBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxGatewayFrameBytes {
		return nil, fmt.Errorf("网关数据超过 %d 字节上限", maxGatewayFrameBytes)
	}
	return data, nil
}

func (g *Gateway) setDisconnected(reason string) {
	g.connectedAt.Store(0)
	if reason != "" {
		g.lastError.Store(reason)
	}
	g.emitStatus()
}

func (g *Gateway) emitStatus() {
	if g.opts.OnStatus != nil {
		g.opts.OnStatus(g.Status())
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return text
}

// appendQuery 在保留原 query 的前提下追加参数。
func appendQuery(rawURL string, params url.Values) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		// 解析失败时退化为字符串拼接，至少保证不断链
		separator := "?"
		if bytes.ContainsRune([]byte(rawURL), '?') {
			separator = "&"
		}
		return rawURL + separator + params.Encode()
	}
	query := parsed.Query()
	for key, values := range params {
		for _, value := range values {
			query.Set(key, value)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
