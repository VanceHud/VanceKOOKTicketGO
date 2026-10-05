// Package kooktest 提供一个可编程的 KOOK 平台模拟器（REST + WebSocket）。
//
// 目的：工单流程的绝大部分风险在于“与平台交互的时序与权限下发是否正确”，
// 而线上验证需要真实 Token 与服务器。这里用一个进程内的假平台，
// 让集成测试能够完整走通：按钮点击 → 建频道 → 下发权限 → 发卡片 → 归档消息 → 关闭 → 删除频道。
//
// 模拟器刻意保留了这些真实细节：
//   - 统一响应包裹 {"code":0,"message":"","data":...}
//   - 网关 HELLO / PING-PONG / 事件推送，并在压缩模式下用 zlib 压缩二进制帧
//   - 记录每一次调用（端点、参数），供测试断言权限位等关键参数
package kooktest

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook"
)

// Call 记录一次平台调用。
type Call struct {
	Endpoint string
	Method   string
	Params   map[string]any
	At       time.Time
}

// Server 是模拟的 KOOK 平台。
type Server struct {
	HTTP *httptest.Server

	mu       sync.Mutex
	calls    []Call
	channels map[string]kook.Channel
	roles    []kook.Role
	users    map[string]kook.User
	games    map[int64]kook.Game
	// messages 是 message/create 与 InjectMessage 注册的消息，供 message/view 查询。
	messages map[string]kook.Message
	// gameSystem 标记哪些游戏属于 KOOK 内置（game?type=2），用于列表过滤。
	gameSystem map[int64]bool
	nextID     int64
	nextGame   int64

	// 可编程行为
	DMBlocked         bool // 私聊被屏蔽（模拟用户未开启私聊）
	ChannelCreateFail bool // 建频道失败
	ChannelDeleteFail bool // 删频道失败
	RoleGrantFail     bool // 发放角色失败
	GameCreateFail    bool // 新建游戏失败（模拟超出每日上限）
	Compress          bool // 网关下发是否压缩（由 compress 查询参数决定）
	// RejectResume 为真时拒绝带 resume 参数的续传（会话已过期）。
	RejectResume bool
	// SuppressResumeAck 为真时受理续传但不下发 resumeOK(s=6)，
	// 用于模拟「续传实际没生效、事件仍留在旧会话」的平台异常。
	SuppressResumeAck bool
	// Delay 让每次 REST 调用慢下来，便于测试并发行为（默认 0）。
	Delay time.Duration
	// SuppressRateHeaders 为真时不返回限流响应头，
	// 用于模拟“客户端还没学到额度”的场景。
	SuppressRateHeaders bool

	// inFlight / maxInFlight 记录同时进行的 REST 调用数，
	// 供测试断言“这些接口是并发调用的”。
	inFlight    atomic.Int64
	maxInFlight atomic.Int64

	upgrader websocket.Upgrader
	conns    map[*websocket.Conn]bool
	connMu   sync.Mutex
	// wsConnects 记录每次网关连接的 query 参数，用于断言续传行为。
	wsConnects []url.Values
	// writeMu 串行化所有向客户端的写入。gorilla/websocket 不允许并发写，
	// 推送事件（Push）与心跳 PONG、HELLO 可能同时在写，需要加锁。
	writeMu  sync.Mutex
	sn       int64
	sessions int64
}

// write 在写锁保护下向连接发送一条消息。
func (s *Server) write(conn *websocket.Conn, messageType int, payload []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return conn.WriteMessage(messageType, payload)
}

// New 启动模拟平台。
func New() *Server {
	s := &Server{
		channels:   make(map[string]kook.Channel),
		users:      make(map[string]kook.User),
		games:      make(map[int64]kook.Game),
		messages:   make(map[string]kook.Message),
		gameSystem: make(map[int64]bool),
		conns:      make(map[*websocket.Conn]bool),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
	s.roles = []kook.Role{
		{RoleID: 1001, Name: "服主"},
		{RoleID: 1002, Name: "客服组"},
		{RoleID: 1003, Name: "实习客服"},
	}
	s.users["77777"] = kook.User{ID: "77777", Username: "TicketBot", Nickname: "TicketBot", Bot: true}
	s.users["9001"] = kook.User{ID: "9001", Username: "asker", Nickname: "提问用户", IdentifyNum: "0001", Roles: []int64{}}
	s.users["9002"] = kook.User{ID: "9002", Username: "admin", Nickname: "客服小林", IdentifyNum: "0002", Roles: []int64{1002}}
	s.users["9003"] = kook.User{ID: "9003", Username: "master", Nickname: "服主", IdentifyNum: "0003", Roles: []int64{1001}}
	s.users["9004"] = kook.User{ID: "9004", Username: "helper", Nickname: "实习客服", IdentifyNum: "0004", Roles: []int64{1003}}

	s.games[111111] = kook.Game{ID: 111111, Name: "CS", Type: 0, ProcessName: []string{"cstrike"}}
	s.gameSystem[111111] = true
	s.games[222222] = kook.Game{ID: 222222, Name: "KOOK", Type: 0}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/", s.handleAPI)
	mux.HandleFunc("/ws", s.handleWS)
	s.HTTP = httptest.NewServer(mux)
	return s
}

// Close 关闭模拟平台与所有 WebSocket 连接。
func (s *Server) Close() {
	s.connMu.Lock()
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.conns = map[*websocket.Conn]bool{}
	s.connMu.Unlock()
	s.HTTP.Close()
}

// BaseURL 返回模拟平台的地址（用于 kook.Options.BaseURL）。
func (s *Server) BaseURL() string { return s.HTTP.URL }

// AddUser 注册一个用户（可带角色）。
func (s *Server) AddUser(user kook.User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[user.ID] = user
}

// AddChannel 注册一个已存在的频道。
func (s *Server) AddChannel(channel kook.Channel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channels[channel.ID] = channel
}

// InjectMessage 注册一条消息详情，供 message/view 查询。
//
// 真实平台的卡片消息事件不带内容，只有 message/view 能拿到卡片 JSON，
// 因此测试需要预先注入消息详情来验证归档补全逻辑。
func (s *Server) InjectMessage(msgID string, message kook.Message) {
	if message.ID == "" {
		message.ID = msgID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages[msgID] = message
}

// Games 返回当前游戏库快照。
func (s *Server) Games() []kook.Game {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]kook.Game, 0, len(s.games))
	for _, game := range s.games {
		out = append(out, game)
	}
	return out
}

// AddGame 注册一个已存在的游戏。
func (s *Server) AddGame(game kook.Game) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.games[game.ID] = game
}

// Calls 返回所有调用记录的副本。
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Call, len(s.calls))
	copy(out, s.calls)
	return out
}

// CallsOf 返回指定端点的调用记录。
func (s *Server) CallsOf(endpoint string) []Call {
	out := []Call{}
	for _, call := range s.Calls() {
		if call.Endpoint == endpoint {
			out = append(out, call)
		}
	}
	return out
}

// MaxInFlight 返回模拟平台同时处理过的最大 REST 调用数。
func (s *Server) MaxInFlight() int64 { return s.maxInFlight.Load() }

// ResetConcurrency 清零并发度统计，便于单个用例从零开始断言。
func (s *Server) ResetConcurrency() { s.maxInFlight.Store(0) }

// Channels 返回当前频道快照。
func (s *Server) Channels() map[string]kook.Channel {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]kook.Channel, len(s.channels))
	for id, channel := range s.channels {
		out[id] = channel
	}
	return out
}

// WSConnects 返回每次网关连接的 query 参数（含 resume/session_id/sn）。
func (s *Server) WSConnects() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]url.Values, len(s.wsConnects))
	copy(out, s.wsConnects)
	return out
}

// SendReconnect 向所有连接下发 s=5（平台要求重连），用于测试会话失效场景。
func (s *Server) SendReconnect(code int, errText string) {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	frame, _ := json.Marshal(map[string]any{
		"s": kook.SignalReconnect,
		"d": map[string]any{"code": code, "err": errText},
	})
	for conn := range s.conns {
		_ = s.write(conn, websocket.TextMessage, frame)
	}
}

// Push 向所有已连接的网关客户端推送一个事件。
func (s *Server) Push(eventType int, body any) {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	if len(s.conns) == 0 {
		return
	}
	sn := atomic.AddInt64(&s.sn, 1)
	frame := map[string]any{"s": kook.SignalEvent, "sn": sn, "d": body}
	payload, err := json.Marshal(frame)
	if err != nil {
		return
	}
	for conn := range s.conns {
		if s.Compress {
			if compressed, err := compressPayload(payload); err == nil {
				_ = s.write(conn, websocket.BinaryMessage, compressed)
				continue
			}
		}
		_ = s.write(conn, websocket.TextMessage, payload)
	}
}

// ---------------------------------------------------------------------------
// REST
// ---------------------------------------------------------------------------

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	endpoint := strings.TrimPrefix(r.URL.Path, "/api/v3/")
	params := map[string]any{}

	// 记录并发度：真实的开单流程会并发调用多个接口，测试用最大并发数来断言。
	tracked := s.inFlight.Add(1)
	for {
		current := s.maxInFlight.Load()
		if tracked <= current || s.maxInFlight.CompareAndSwap(current, tracked) {
			break
		}
	}
	defer s.inFlight.Add(-1)

	if s.Delay > 0 {
		time.Sleep(s.Delay)
	}
	if !s.SuppressRateHeaders {
		// 与平台一致：每个受控响应都带限流头（客户端据此分桶限速）。
		w.Header().Set("X-Rate-Limit-Limit", "50")
		w.Header().Set("X-Rate-Limit-Remaining", "49")
		w.Header().Set("X-Rate-Limit-Reset", "1")
		w.Header().Set("X-Rate-Limit-Bucket", endpoint)
	}

	if r.Method == http.MethodPost {
		_ = json.NewDecoder(r.Body).Decode(&params)
	}
	for key, values := range r.URL.Query() {
		if len(values) > 0 {
			params[key] = values[0]
		}
	}

	s.record(endpoint, r.Method, params)
	if r.Header.Get("Authorization") == "" {
		writeJSON(w, http.StatusUnauthorized, 40100, "缺少 Token", nil)
		return
	}

	switch endpoint {
	case "user/me":
		s.mu.Lock()
		me := s.users["77777"]
		s.mu.Unlock()
		writeData(w, me)

	case "user/view":
		userID := fmt.Sprint(params["user_id"])
		s.mu.Lock()
		user, ok := s.users[userID]
		s.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, 40400, "用户不存在", nil)
			return
		}
		writeData(w, user)

	case "guild/view":
		writeData(w, kook.Guild{ID: "5000", Name: "模拟服务器", MasterID: "9003"})

	case "guild-role/list":
		s.mu.Lock()
		roles := append([]kook.Role{}, s.roles...)
		s.mu.Unlock()
		writeData(w, map[string]any{"items": roles})

	case "guild-role/grant":
		if s.RoleGrantFail {
			writeJSON(w, http.StatusForbidden, 40300, "机器人缺少管理角色权限", nil)
			return
		}
		writeData(w, map[string]any{})

	case "guild-role/revoke":
		writeData(w, map[string]any{})

	case "channel/list":
		s.mu.Lock()
		items := make([]kook.Channel, 0, len(s.channels))
		for _, channel := range s.channels {
			items = append(items, channel)
		}
		s.mu.Unlock()
		writeData(w, map[string]any{"items": items})

	case "channel/create":
		if s.ChannelCreateFail {
			writeJSON(w, http.StatusForbidden, 40300, "机器人缺少管理频道权限", nil)
			return
		}
		id := fmt.Sprintf("chan-%d", atomic.AddInt64(&s.nextID, 1))
		channel := kook.Channel{
			ID:       id,
			Name:     fmt.Sprint(params["name"]),
			GuildID:  fmt.Sprint(params["guild_id"]),
			ParentID: fmt.Sprint(params["parent_id"]),
			Type:     kook.ChannelText,
		}
		s.mu.Lock()
		s.channels[id] = channel
		s.mu.Unlock()
		writeData(w, channel)

	case "channel/delete":
		if s.ChannelDeleteFail {
			writeJSON(w, http.StatusForbidden, 40300, "机器人缺少管理频道权限", nil)
			return
		}
		channelID := fmt.Sprint(params["channel_id"])
		s.mu.Lock()
		delete(s.channels, channelID)
		s.mu.Unlock()
		writeData(w, map[string]any{})

	case "channel-role/create", "channel-role/update":
		writeData(w, map[string]any{})

	case "message/create", "direct-message/create":
		if strings.HasPrefix(endpoint, "direct-message") && s.DMBlocked {
			// 模拟“对方未开启私聊/屏蔽机器人”
			writeJSON(w, http.StatusForbidden, 40300, "用户未开启私聊", nil)
			return
		}
		prefix := "msg"
		if strings.HasPrefix(endpoint, "direct-message") {
			prefix = "dm"
		}
		message := kook.Message{
			ID:      fmt.Sprintf("%s-%d", prefix, atomic.AddInt64(&s.nextID, 1)),
			MsgID:   fmt.Sprintf("%s-%d", prefix, s.nextID),
			Type:    toInt(params["type"]),
			Content: fmt.Sprint(params["content"]),
		}
		if message.Type == 0 {
			message.Type = kook.MsgTypeKMarkdown
		}
		message.ChannelID = fmt.Sprint(params["target_id"])
		s.mu.Lock()
		s.messages[message.ID] = message
		s.mu.Unlock()
		writeData(w, message)

	case "message/view", "direct-message/view":
		msgID := fmt.Sprint(params["msg_id"])
		s.mu.Lock()
		message, ok := s.messages[msgID]
		s.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, 40400, "消息不存在", nil)
			return
		}
		writeData(w, message)

	case "message/update", "direct-message/update", "message/delete", "direct-message/delete":
		writeData(w, map[string]any{})

	case "game/activity", "game/delete-activity", "user/offline":
		writeData(w, map[string]any{})

	case "game":
		gameType := toInt(params["type"])
		s.mu.Lock()
		items := make([]kook.Game, 0, len(s.games))
		for id, game := range s.games {
			system := s.gameSystem[id]
			switch gameType {
			case kook.GameTypeUser:
				if system {
					continue
				}
			case kook.GameTypeSystem:
				if !system {
					continue
				}
			}
			items = append(items, game)
		}
		s.mu.Unlock()
		writeData(w, map[string]any{
			"items": items,
			"meta": map[string]any{
				"page": 1, "page_total": 1, "page_size": 50, "total": len(items),
			},
		})

	case "game/create":
		if s.GameCreateFail {
			writeJSON(w, http.StatusForbidden, 40300, "今日创建游戏数量已达上限", nil)
			return
		}
		s.mu.Lock()
		s.nextGame++
		game := kook.Game{
			ID:   1000000 + s.nextGame,
			Name: fmt.Sprint(params["name"]),
			Type: 0,
			Icon: fmt.Sprint(params["icon"]),
		}
		s.games[game.ID] = game
		s.mu.Unlock()
		writeData(w, game)

	case "game/update":
		id := toInt64(params["id"])
		s.mu.Lock()
		game, ok := s.games[id]
		if !ok {
			s.mu.Unlock()
			writeJSON(w, http.StatusNotFound, 40400, "游戏不存在", nil)
			return
		}
		if name := fmt.Sprint(params["name"]); name != "" && name != "<nil>" {
			game.Name = name
		}
		if icon := fmt.Sprint(params["icon"]); icon != "" && icon != "<nil>" {
			game.Icon = icon
		}
		s.games[id] = game
		s.mu.Unlock()
		writeData(w, game)

	case "game/delete":
		id := toInt64(params["id"])
		s.mu.Lock()
		delete(s.games, id)
		delete(s.gameSystem, id)
		s.mu.Unlock()
		writeData(w, map[string]any{})

	case "gateway/index":
		// 记录 compress 选择，网关据此决定是否压缩下行
		s.mu.Lock()
		s.Compress = fmt.Sprint(params["compress"]) == "1"
		sn := atomic.AddInt64(&s.sessions, 1)
		s.mu.Unlock()
		_ = sn
		wsURL := "ws" + strings.TrimPrefix(s.HTTP.URL, "http") + "/ws"
		writeData(w, map[string]any{"url": wsURL})

	default:
		writeJSON(w, http.StatusNotFound, 40400, "未知接口: "+endpoint, nil)
	}
}

func (s *Server) record(endpoint, method string, params map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, Call{Endpoint: endpoint, Method: method, Params: params, At: time.Now()})
}

// ---------------------------------------------------------------------------
// WebSocket 网关
// ---------------------------------------------------------------------------

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	query := r.URL.Query()
	s.mu.Lock()
	s.wsConnects = append(s.wsConnects, query)
	s.mu.Unlock()

	// 续传被拒：真实平台会用 HELLO 的错误码告知会话已失效（40107 session 过期）。
	if query.Get("resume") == "1" && s.RejectResume {
		hello, _ := json.Marshal(map[string]any{"s": kook.SignalHello, "d": map[string]any{"code": 40107}})
		_ = s.write(conn, websocket.TextMessage, hello)
		_ = conn.Close()
		return
	}

	s.connMu.Lock()
	s.conns[conn] = true
	s.connMu.Unlock()

	// 续传时沿用客户端给出的 session_id（与真实平台一致），否则新建会话。
	sessionID := query.Get("session_id")
	if sessionID == "" {
		sessionID = fmt.Sprintf("sess-%d", atomic.AddInt64(&s.sessions, 1))
	} else if sn, err := strconv.ParseInt(query.Get("sn"), 10, 64); err == nil {
		// 真实平台按会话继续编号：续传后的事件从客户端上报的 sn 往后排。
		atomic.StoreInt64(&s.sn, sn)
	}

	// HELLO：真实平台会带上 session_id
	hello, _ := json.Marshal(map[string]any{"s": kook.SignalHello, "d": map[string]any{
		"code":       0,
		"session_id": sessionID,
	}})
	if err := s.write(conn, websocket.TextMessage, hello); err != nil {
		return
	}

	// 续传完成后平台会补发离线事件，并以 resumeOK(s=6) 结束。
	if query.Get("resume") == "1" && !s.SuppressResumeAck {
		resumeAck, _ := json.Marshal(map[string]any{"s": kook.SignalResumeAck, "d": map[string]any{"session_id": sessionID}})
		if err := s.write(conn, websocket.TextMessage, resumeAck); err != nil {
			return
		}
	}

	// 处理客户端心跳：收到 PING 回复 PONG；收到 RESUME 回复 RESUME_ACK
	for {
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			break
		}
		raw := payload
		if messageType == websocket.BinaryMessage {
			if decompressed, err := decompressPayload(payload); err == nil {
				raw = decompressed
			}
		}
		var frame struct {
			Signal int   `json:"s"`
			SN     int64 `json:"sn"`
		}
		if err := json.Unmarshal(raw, &frame); err != nil {
			continue
		}
		switch frame.Signal {
		case kook.SignalPing:
			pong, _ := json.Marshal(map[string]any{"s": kook.SignalPong, "sn": frame.SN})
			_ = s.write(conn, websocket.TextMessage, pong)
		case kook.SignalResume:
			resumeAck, _ := json.Marshal(map[string]any{"s": kook.SignalResumeAck, "d": map[string]any{"session_id": sessionID}})
			_ = s.write(conn, websocket.TextMessage, resumeAck)
		}
	}

	s.connMu.Lock()
	delete(s.conns, conn)
	s.connMu.Unlock()
	_ = conn.Close()
}

// ---------------------------------------------------------------------------
// 事件构造辅助
// ---------------------------------------------------------------------------

// ButtonClickEvent 构造按钮点击事件（与真实平台结构一致：数据在 d.extra.body）。
func ButtonClickEvent(channelID, userID, value string, user kook.User) map[string]any {
	return map[string]any{
		"channel_type": kook.ChannelTypeGroup,
		"type":         kook.EventTypeSystem,
		"target_id":    channelID,
		"extra": map[string]any{
			"type":     kook.SystemEventButtonClick,
			"guild_id": "5000",
			"body": map[string]any{
				"value":     value,
				"msg_id":    "msg-btn",
				"user_id":   userID,
				"target_id": channelID,
				"user_info": user,
			},
		},
	}
}

// TextMessageEvent 构造频道文本消息事件。
//
// 与真实平台保持一致：extra.type 是数字（等于事件主类型），用户对象放在
// extra.author，顶层不重复给出完整 author（平台只给 author_id）。
func TextMessageEvent(channelID, userID, content string, user kook.User) map[string]any {
	return map[string]any{
		"channel_type":  kook.ChannelTypeGroup,
		"type":          kook.EventTypeText,
		"target_id":     channelID,
		"author_id":     userID,
		"content":       content,
		"msg_id":        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		"msg_timestamp": time.Now().UnixMilli(),
		"extra": map[string]any{
			"type":         kook.EventTypeText,
			"guild_id":     "5000",
			"channel_name": "工单频道",
			"author":       user,
		},
	}
}

// ImageMessageEvent 构造图片消息事件。
func ImageMessageEvent(channelID, userID, url string, user kook.User) map[string]any {
	event := TextMessageEvent(channelID, userID, url, user)
	event["type"] = kook.EventTypeImage
	if extra, ok := event["extra"].(map[string]any); ok {
		extra["type"] = kook.EventTypeImage
	}
	return event
}

// CardMessageEvent 构造卡片消息事件。
//
// 与真实平台保持一致：content 为空，卡片 JSON 只能通过 message/view 获取，
// 用于验证“事件 → 异步补全卡片内容”的归档逻辑。
func CardMessageEvent(channelID, userID, msgID string, user kook.User) map[string]any {
	event := TextMessageEvent(channelID, userID, "", user)
	event["type"] = kook.EventTypeCard
	event["msg_id"] = msgID
	if extra, ok := event["extra"].(map[string]any); ok {
		extra["type"] = kook.EventTypeCard
	}
	return event
}

// DirectMessageEvent 构造私聊消息事件。
func DirectMessageEvent(userID, content string, user kook.User) map[string]any {
	return map[string]any{
		"channel_type":  kook.ChannelTypePerson,
		"type":          kook.EventTypeText,
		"target_id":     userID,
		"author_id":     userID,
		"content":       content,
		"msg_id":        fmt.Sprintf("dm-%d", time.Now().UnixNano()),
		"msg_timestamp": time.Now().UnixMilli(),
		"extra": map[string]any{
			"type":   kook.EventTypeText,
			"author": user,
		},
	}
}

// ReactionEvent 构造表情回应事件。
func ReactionEvent(messageID, userID, channelID, emojiID string) map[string]any {
	return map[string]any{
		"channel_type": kook.ChannelTypeGroup,
		"type":         kook.EventTypeSystem,
		"target_id":    channelID,
		"extra": map[string]any{
			"type":     kook.SystemEventAddedReaction,
			"guild_id": "5000",
			"body": map[string]any{
				"msg_id":     messageID,
				"user_id":    userID,
				"channel_id": channelID,
				"emoji":      map[string]any{"id": emojiID, "name": "heart"},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

func writeData(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, 0, "操作成功", data)
}

func writeJSON(w http.ResponseWriter, status, code int, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    code,
		"message": message,
		"data":    data,
	})
}

func toInt(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case string:
		var out int
		_, _ = fmt.Sscan(v, &out)
		return out
	default:
		return 0
	}
}

func toInt64(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case string:
		var out int64
		_, _ = fmt.Sscan(v, &out)
		return out
	default:
		return 0
	}
}

func compressPayload(payload []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer := zlib.NewWriter(&buf)
	if _, err := writer.Write(payload); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decompressPayload(payload []byte) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(reader); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
