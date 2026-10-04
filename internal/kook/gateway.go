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

	// HeartbeatInterval 默认 30s（与官方文档一致）。
	HeartbeatInterval time.Duration
	// PongTimeout 默认 6s：超过该时间未收到 PONG 视为断线。
	PongTimeout time.Duration
	// MaxBackoff 默认 60s。
	MaxBackoff time.Duration
	// DialTimeout 默认 15s。
	DialTimeout time.Duration
}

// Gateway 是 KOOK WebSocket 网关客户端。
//
// 行为对齐官方文档：
//  1. 获取网关地址（可带压缩）
//  2. 连接后等待 HELLO，从中取得 session_id
//  3. 每 30s 发送一次心跳 PING，6s 内未收到 PONG 视为超时
//  4. 断线时使用 resume=1&session_id=..&sn=.. 续传，避免丢事件
//  5. 失败按 2s、4s、8s… 指数退避重试，上限 60s
type Gateway struct {
	opts GatewayOptions

	mu        sync.Mutex
	sessionID string
	lastSN    int64

	connectedAt    atomic.Int64
	eventsReceived atomic.Int64
	attempt        atomic.Int32
	lastError      atomic.Value // string

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
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 60 * time.Second
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 15 * time.Second
	}
	g := &Gateway{opts: opts, pongCh: make(chan struct{}, 1)}
	g.lastError.Store("")
	return g, nil
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
		g.setDisconnected(errorText(err))

		if ctx.Err() != nil {
			return ctx.Err()
		}

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
	attempt := g.attempt.Load()
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 5 {
		attempt = 5
	}
	d := 2 * time.Second * time.Duration(1<<uint(attempt))
	if d > g.opts.MaxBackoff {
		d = g.opts.MaxBackoff
	}
	return d
}

// serve 建立一次连接并处理消息，直到连接断开。
func (g *Gateway) serve(ctx context.Context) error {
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

	// 先读取 HELLO，确认握手成功并拿到 session_id
	hello, err := g.readFrame(conn)
	if err != nil {
		return fmt.Errorf("读取 HELLO 失败: %w", err)
	}
	if hello.Signal != SignalHello {
		return fmt.Errorf("握手失败：期望 HELLO(s=1)，收到 s=%d", hello.Signal)
	}
	var helloData struct {
		Code      int    `json:"code"`
		SessionID string `json:"session_id"`
	}
	if len(hello.Data) > 0 {
		_ = json.Unmarshal(hello.Data, &helloData)
	}
	if helloData.Code != 0 {
		return fmt.Errorf("握手被平台拒绝：code=%d", helloData.Code)
	}

	g.mu.Lock()
	g.sessionID = helloData.SessionID
	g.mu.Unlock()

	g.connectedAt.Store(time.Now().UnixNano())
	g.attempt.Store(0)
	g.lastError.Store("")
	g.opts.Logger.Info("KOOK 网关已连接", "session_id", helloData.SessionID, "resume", sessionID != "")
	g.emitStatus()

	// 心跳协程：超时未收到 PONG 则主动断开，触发重连
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	go g.heartbeatLoop(heartbeatCtx, conn)

	for {
		frame, err := g.readFrame(conn)
		if err != nil {
			return err
		}

		switch frame.Signal {
		case SignalEvent:
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

		case SignalPong:
			select {
			case g.pongCh <- struct{}{}:
			default:
			}

		case SignalReconnect:
			// 平台要求重连，保留 session 以便续传
			return fmt.Errorf("平台要求重连（s=5）")

		case SignalResumeAck:
			g.opts.Logger.Info("断线续传成功")

		case SignalHello:
			// 续传过程中可能再次收到 HELLO，忽略即可
			continue

		default:
			g.opts.Logger.Debug("收到未处理的网关信号", "signal", frame.Signal)
		}
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
		if data, err := io.ReadAll(io.LimitReader(reader, 8<<20)); err == nil {
			return data, nil
		}
	}
	reader := flate.NewReader(bytes.NewReader(payload))
	defer func() { _ = reader.Close() }()
	return io.ReadAll(io.LimitReader(reader, 8<<20))
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
