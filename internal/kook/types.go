// Package kook 是 KOOK（原开黑啦）开放平台的轻量客户端实现。
//
// 设计取舍：
//   - 不依赖社区 SDK：KOOK 没有官方 Go SDK，而社区库多为个人维护、star 极少，
//     且本项目需要的 channel-role、guild-role、game 等接口仍需自行封装；
//   - 自带令牌桶限速与 429 退避，避免触发平台风控；
//   - WebSocket 网关自行实现 zlib 解压、30s 心跳、session resume 与 sn 序号重放。
//
// 安全约定：
//   - token 只保存在内存中，不写入日志；
//   - 所有错误都带 HTTP 状态与平台错误码，便于上层区分“无权限/不存在/被限流”。
package kook

import (
	"bytes"
	"encoding/json"
)

// 平台的 API 基础地址。
const DefaultBaseURL = "https://www.kookapp.cn"

// 消息类型（用于 message/create 的 type 字段）。
const (
	MsgTypeText      = 1
	MsgTypeImage     = 2
	MsgTypeVideo     = 3
	MsgTypeFile      = 4
	MsgTypeAudio     = 8
	MsgTypeKMarkdown = 9
	MsgTypeCard      = 10
)

// 会话类型（事件中的 channel_type 字段）。
const (
	ChannelTypeGroup  = "GROUP"
	ChannelTypePerson = "PERSON"
)

// 事件主类型（d.type）。
const (
	EventTypeText        = 1
	EventTypeImage       = 2
	EventTypeVideo       = 3
	EventTypeFile        = 4
	EventTypeAudio       = 8
	EventTypeKMarkdown   = 9
	EventTypeCard        = 10
	EventTypeSystem      = 255
	EventTypeJoinedGuild = 255
)

// 网关信号（s 字段）。
const (
	SignalEvent     = 0
	SignalHello     = 1
	SignalPing      = 2
	SignalPong      = 3
	SignalResume    = 4
	SignalReconnect = 5
	SignalResumeAck = 6
)

// 系统事件（type=255）的 extra.type 取值。
const (
	SystemEventButtonClick     = "message_btn_click"
	SystemEventAddedReaction   = "added_reaction"
	SystemEventRemovedReaction = "removed_reaction"
	SystemEventJoinedGuild     = "joined_guild"
	SystemEventExitedGuild     = "exited_guild"
)

// 频道类型。
const (
	ChannelText  = 1
	ChannelVoice = 2
)

// 频道权限位（channel-role/update 的 allow / deny 位掩码）。
const (
	// PermissionViewChannel 查看文字、语音频道。
	PermissionViewChannel = 2048
	// PermissionSendMessage 发送消息。
	PermissionSendMessage = 4096
	// PermissionAllText 可查看且可发言。
	PermissionAllText = PermissionViewChannel | PermissionSendMessage
)

// User 是用户对象（作者信息、成员信息等）。
type User struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	IdentifyNum string `json:"identify_num"`
	Nickname    string `json:"nickname"`
	Avatar      string `json:"avatar"`
	Bot         bool   `json:"bot"`
	Online      bool   `json:"online"`
	// Roles 是用户在服务器中的角色 ID 列表（数值，与 guild-role/list 的 role_id 一致）。
	Roles []int64 `json:"roles"`
}

// DisplayName 返回优先展示的昵称。
func (u User) DisplayName() string {
	if u.Nickname != "" {
		return u.Nickname
	}
	if u.Username != "" {
		return u.Username
	}
	return u.ID
}

// FullName 返回 "昵称#识别号" 形式，便于人工区分同名用户。
func (u User) FullName() string {
	name := u.DisplayName()
	if u.IdentifyNum != "" {
		return name + "#" + u.IdentifyNum
	}
	return name
}

// Guild 是服务器对象（仅保留本项目用到的字段）。
type Guild struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	UserID   string `json:"user_id"`
	MasterID string `json:"master_id"`
}

// Role 是服务器角色。
type Role struct {
	RoleID      int64  `json:"role_id"`
	Name        string `json:"name"`
	Color       int    `json:"color"`
	Position    int    `json:"position"`
	Hoist       int    `json:"hoist"`
	Mentionable int    `json:"mentionable"`
	Permissions int    `json:"permissions"`
}

// Channel 是频道对象。
type Channel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	GuildID    string `json:"guild_id"`
	UserID     string `json:"user_id"`
	ParentID   string `json:"parent_id"`
	Type       int    `json:"type"`
	Level      int    `json:"level"`
	IsCategory bool   `json:"is_category"`
}

// Message 是消息对象（发送成功后返回）。
type Message struct {
	ID        string `json:"id"`
	MsgID     string `json:"msg_id"`
	Content   string `json:"content"`
	Type      int    `json:"type"`
	ChannelID string `json:"channel_id"`
	AuthorID  string `json:"author_id"`
}

// Emoji 是表情对象。
type Emoji struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Game 是游戏/状态对象（game 列表、创建、更新接口返回）。
//
// 对应官方文档的 object-game：Type 0 表示游戏、1 表示 VUP、2 表示进程；
// Options / ProcessName / ProductName / KMHookAdmin 由 KOOK 客户端用于识别进程，
// 通过接口创建的记录这几项通常为空。
type Game struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Type        int      `json:"type"`
	Options     string   `json:"options"`
	KMHookAdmin bool     `json:"kmhook_admin"`
	ProcessName []string `json:"process_name"`
	ProductName []string `json:"product_name"`
	Icon        string   `json:"icon"`
}

// 游戏列表的类型过滤（game?type=）。
const (
	// GameTypeAll 查询全部游戏。
	GameTypeAll = 0
	// GameTypeUser 只查询用户创建的游戏（WebUI 可增删改的那些）。
	GameTypeUser = 1
	// GameTypeSystem 只查询 KOOK 内置的游戏。
	GameTypeSystem = 2
)

// 音乐动态支持的软件枚举（game/activity 的 software 字段）。
const (
	MusicSoftwareCloudMusic = "cloudmusic"
	MusicSoftwareQQMusic    = "qqmusic"
	MusicSoftwareKugou      = "kugou"
)

// ValidMusicSoftware 判断软件名是否属于平台支持的枚举值。
func ValidMusicSoftware(software string) bool {
	switch software {
	case MusicSoftwareCloudMusic, MusicSoftwareQQMusic, MusicSoftwareKugou:
		return true
	default:
		return false
	}
}

// ActivityTypeName 返回动态类型的中文名，便于日志与提示。
func ActivityTypeName(dataType int) string {
	switch dataType {
	case ActivityTypeGame:
		return "游戏"
	case ActivityTypeMusic:
		return "音乐"
	default:
		return "未知"
	}
}

// ExtraType 兼容 extra.type 的两种平台形态：
//
//   - 普通消息事件（type=1/2/3/…）：数字，取值与事件主类型一致；
//   - 系统事件（type=255）：字符串，如 message_btn_click、added_reaction。
//
// 注意：该字段若只声明为 string，普通消息事件会在 json.Unmarshal 时因
// 类型不匹配报错，整条事件被网关丢弃（表现为「聊天记录没有记录」）。
type ExtraType string

// String 返回底层字符串，便于日志输出与比较。
func (t ExtraType) String() string { return string(t) }

// UnmarshalJSON 同时接受数字与字符串形态。
//
// 无法识别的字面量按原文保存而不是返回错误：归档聊天记录不应因为
// 平台新增了一种 extra.type 取值就整条事件解析失败。
func (t *ExtraType) UnmarshalJSON(data []byte) error {
	raw := bytes.TrimSpace(data)
	if len(raw) == 0 || string(raw) == "null" {
		*t = ""
		return nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
		*t = ExtraType(text)
		return nil
	}
	*t = ExtraType(string(raw))
	return nil
}

// Extra 是事件的可变部分。
type Extra struct {
	// Type 在系统事件中是子类型（message_btn_click、added_reaction），
	// 在普通消息事件中是数字（与事件主类型一致）。
	Type ExtraType `json:"type"`
	// GuildID 是消息所属服务器（服务器内消息事件会带上，用于单服务器白名单校验）。
	GuildID string `json:"guild_id"`
	// ChannelName 是消息所在频道名（用于面板展示）。
	ChannelName string `json:"channel_name"`
	// Body 保持原始 JSON，由上层按 Type 反序列化成具体结构。
	Body Body `json:"body"`
	// Author 是系统事件中的作者信息。
	Author User `json:"author"`
}

// Body 是事件体中常见的字段集合。
//
// KOOK 的不同事件共用同一套字段名，因此这里用一个大结构承接，
// 避免为每种事件定义一套类型；不存在的字段保持零值。
type Body struct {
	Value     string `json:"value"`
	MsgID     string `json:"msg_id"`
	UserID    string `json:"user_id"`
	TargetID  string `json:"target_id"`
	UserInfo  User   `json:"user_info"`
	ChannelID string `json:"channel_id"`
	Emoji     Emoji  `json:"emoji"`
	// Content 是系统事件（如加入服务器）中的附加内容。
	Content string `json:"content"`
}

// Event 是网关下发的一次事件。
type Event struct {
	// Type 为事件主类型（1 文字消息、9 KMarkdown、10 卡片、255 系统事件等）。
	Type int `json:"type"`
	// ChannelType 为 GROUP（服务器内）或 PERSON（私聊）。
	ChannelType string `json:"channel_type"`
	// TargetID 在频道消息中是频道 ID，在私聊中是对方用户 ID。
	TargetID string `json:"target_id"`
	// AuthorID 与 Author 描述消息发送者。
	AuthorID string `json:"author_id"`
	Author   User   `json:"author"`
	// Content 是纯文本内容；MsgID 是该消息的 ID。
	Content string `json:"content"`
	MsgID   string `json:"msg_id"`
	// MsgTimestamp 是消息发送时间（毫秒）。
	MsgTimestamp int64 `json:"msg_timestamp"`
	// Extra 承载系统事件的子类型与数据。
	Extra Extra `json:"extra"`
}

// UnmarshalJSON 在标准反序列化之后修正平台的字段布局差异：
//
// 普通消息事件并不带顶层 author，用户对象只出现在 extra.author；
// 若直接使用零值 event.Author，归档出来的用户名会是空串。
//
// 同时把 extra.author.id 回填到 AuthorID：私聊消息事件里平台偶尔只给出
// extra.author，缺少顶层 author_id，而命令分发完全依赖 AuthorID
// （表现为「私聊机器人没有反应」）。
func (e *Event) UnmarshalJSON(data []byte) error {
	// 借助别名类型避免递归调用本方法。
	type plain Event
	var parsed plain
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	*e = Event(parsed)
	if e.Author.ID == "" && e.Extra.Author.ID != "" {
		e.Author = e.Extra.Author
	}
	if e.AuthorID == "" {
		e.AuthorID = e.Author.ID
	}
	return nil
}

// IsSystem 判断是否为系统事件。
func (e Event) IsSystem() bool { return e.Type == EventTypeSystem }

// IsDirect 判断是否来自私聊。
func (e Event) IsDirect() bool { return e.ChannelType == ChannelTypePerson }

// IsTextMessage 判断是否是可解析命令的文本类消息。
func (e Event) IsTextMessage() bool {
	switch e.Type {
	case EventTypeText, EventTypeKMarkdown:
		return true
	default:
		return false
	}
}
