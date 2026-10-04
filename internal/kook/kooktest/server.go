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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"vancekookticket/internal/kook"
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
	nextID   int64

	// 可编程行为
	DMBlocked         bool // 私聊被屏蔽（模拟用户未开启私聊）
	ChannelCreateFail bool // 建频道失败
	ChannelDeleteFail bool // 删频道失败
	RoleGrantFail     bool // 发放角色失败
	Compress          bool // 网关下发是否压缩（由 compress 查询参数决定）

	upgrader websocket.Upgrader
	conns    map[*websocket.Conn]bool
	connMu   sync.Mutex
	sn       int64
	sessions int64
}

// New 启动模拟平台。
func New() *Server {
	s := &Server{
		channels: make(map[string]kook.Channel),
		users:    make(map[string]kook.User),
		conns:    make(map[*websocket.Conn]bool),
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
				_ = conn.WriteMessage(websocket.BinaryMessage, compressed)
				continue
			}
		}
		_ = conn.WriteMessage(websocket.TextMessage, payload)
	}
}

// ---------------------------------------------------------------------------
// REST
// ---------------------------------------------------------------------------

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	endpoint := strings.TrimPrefix(r.URL.Path, "/api/v3/")
	params := map[string]any{}

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
		writeData(w, kook.Message{
			ID:      fmt.Sprintf("%s-%d", prefix, atomic.AddInt64(&s.nextID, 1)),
			MsgID:   fmt.Sprintf("%s-%d", prefix, s.nextID),
			Type:    toInt(params["type"]),
			Content: fmt.Sprint(params["content"]),
		})

	case "message/update", "direct-message/update", "message/delete", "direct-message/delete":
		writeData(w, map[string]any{})

	case "game/activity", "game/delete-activity", "user/offline":
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

	s.connMu.Lock()
	s.conns[conn] = true
	s.connMu.Unlock()

	sessionID := fmt.Sprintf("sess-%d", atomic.AddInt64(&s.sessions, 1))

	// HELLO：真实平台会带上 session_id
	hello, _ := json.Marshal(map[string]any{"s": kook.SignalHello, "d": map[string]any{
		"code":       0,
		"session_id": sessionID,
	}})
	if err := conn.WriteMessage(websocket.TextMessage, hello); err != nil {
		return
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
			_ = conn.WriteMessage(websocket.TextMessage, pong)
		case kook.SignalResume:
			resumeAck, _ := json.Marshal(map[string]any{"s": kook.SignalResumeAck, "d": map[string]any{"session_id": sessionID}})
			_ = conn.WriteMessage(websocket.TextMessage, resumeAck)
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
func TextMessageEvent(channelID, userID, content string, user kook.User) map[string]any {
	return map[string]any{
		"channel_type":  kook.ChannelTypeGroup,
		"type":          kook.EventTypeText,
		"target_id":     channelID,
		"author_id":     userID,
		"author":        user,
		"content":       content,
		"msg_id":        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		"msg_timestamp": time.Now().UnixMilli(),
		"extra": map[string]any{
			"type":         "kmarkdown",
			"guild_id":     "5000",
			"channel_name": "工单频道",
		},
	}
}

// ImageMessageEvent 构造图片消息事件。
func ImageMessageEvent(channelID, userID, url string, user kook.User) map[string]any {
	event := TextMessageEvent(channelID, userID, url, user)
	event["type"] = kook.EventTypeImage
	return event
}

// DirectMessageEvent 构造私聊消息事件。
func DirectMessageEvent(userID, content string, user kook.User) map[string]any {
	return map[string]any{
		"channel_type":  kook.ChannelTypePerson,
		"type":          kook.EventTypeText,
		"target_id":     userID,
		"author_id":     userID,
		"author":        user,
		"content":       content,
		"msg_id":        fmt.Sprintf("dm-%d", time.Now().UnixNano()),
		"msg_timestamp": time.Now().UnixMilli(),
		"extra":         map[string]any{"type": "kmarkdown"},
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
