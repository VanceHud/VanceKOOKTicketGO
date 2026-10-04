// 时间存储不变式（务必遵守）
//
// glebarez/go-sqlite 把 time.Time 绑定成文本，格式为
// "2006-01-02 15:04:05.999999999-07:00"，且**不做时区归一化**。
// 由于该格式定宽、零填充，文本比较等价于时间比较——前提是写入值全部为 UTC（偏移恒为 +00:00）。
// 因此：所有写入数据库的时间必须是 UTC，统一使用 store.Now()；
// 解析自前端的本地时间（如 TICKET_TZ）必须先 .UTC() 再参与查询或写入。
package store

import "time"

// 工单状态。
const (
	// TicketPending 表示编号已占用、工单频道尚未创建完成。
	TicketPending = "pending"
	// TicketOpen 表示工单频道已创建，等待处理。
	TicketOpen = "open"
	// TicketLocked 表示工单已锁定：开单人可见但无法发言。
	TicketLocked = "locked"
	// TicketClosed 表示工单已关闭、频道已删除。
	TicketClosed = "closed"
	// TicketFailed 表示开单流程失败（例如建频道失败），占号作废。
	TicketFailed = "failed"
)

// 锁定原因。
const (
	LockReasonManual  = "manual"
	LockReasonTimeout = "timeout"
)

// 工单频道内的消息类型。
const (
	MsgTypeText    = "text"
	MsgTypeImage   = "image"
	MsgTypeCard    = "card"
	MsgTypeFile    = "file"
	MsgTypeAudio   = "audio"
	MsgTypeVideo   = "video"
	MsgTypeSystem  = "system"
	MsgTypeUnknown = "unknown"
)

// WebUI 账号角色，权限从高到低。
const (
	RoleAdmin    = "admin"
	RoleStaff    = "staff"
	RoleReadonly = "readonly"
)

// 角色等级，用于比较权限高低。
var roleRank = map[string]int{RoleAdmin: 3, RoleStaff: 2, RoleReadonly: 1}

// RoleAtLeast 判断 role 是否达到 required 的权限等级。
func RoleAtLeast(role, required string) bool {
	return roleRank[role] >= roleRank[required]
}

// 一次性码用途。
const (
	CodePurposeLogin = "login"
	CodePurposeBind  = "bind"
)

// 审计主体类型。
const (
	ActorTypeWeb  = "web"
	ActorTypeKook = "kook"
	ActorTypeBot  = "bot"
)

// Setting 是以键值方式保存的业务配置（频道 ID、超时小时数等）。
// 所有 UpdatedAt 等时间字段均为 UTC。
// 敏感值（如 KOOK token）以 AES-GCM 密文形式存放，密钥不落库。
type Setting struct {
	Key       string    `gorm:"primaryKey;size:64" json:"key"`
	Value     string    `gorm:"type:text" json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Ticket 是一条工单记录。
//
// No 是对外唯一标识（TK-YYMMDD-XXXX），ID 仅内部使用，接口一律以 No 寻址。
type Ticket struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// No 为对外编号，唯一索引同时承担随机段的冲突检测。
	No string `gorm:"uniqueIndex;size:20;not null" json:"no"`

	UserID   string `gorm:"index;size:64;not null" json:"userId"`
	UserName string `gorm:"size:128" json:"userName"`

	// SourceChannelID 是工单按钮所在的面板频道，用于判定该工单归属哪个面板。
	SourceChannelID string `gorm:"index;size:64" json:"sourceChannelId"`
	// ChannelID 是机器人创建的工单频道，关闭后频道被删除但记录保留。
	ChannelID string `gorm:"index;size:64" json:"channelId"`
	// PanelID 关联面板，面板删除后置空。
	PanelID *uint `gorm:"index" json:"panelId,omitempty"`

	Status     string     `gorm:"index;size:16;not null" json:"status"`
	StartedAt  time.Time  `gorm:"index" json:"startedAt"`
	LockedAt   *time.Time `json:"lockedAt,omitempty"`
	LockReason string     `gorm:"size:16" json:"lockReason,omitempty"`
	ClosedAt   *time.Time `gorm:"index" json:"closedAt,omitempty"`
	ClosedBy   string     `gorm:"size:64" json:"closedBy,omitempty"`

	ClosedByName string `gorm:"size:128" json:"closedByName,omitempty"`
	// FirstReplyAt 记录首条非开单人、非机器人消息的时间，用于统计响应时长。
	FirstReplyAt *time.Time `json:"firstReplyAt,omitempty"`
	// MessageCount 冗余字段，避免列表页统计每条工单的消息数时产生 N+1 查询。
	MessageCount int `json:"messageCount"`

	// LogChannelMsgID / LogUserMsgID 保存关闭通知卡片的 ID，供 /tkcm 备注更新卡片。
	LogChannelMsgID string `gorm:"size:64" json:"-"`
	LogUserMsgID    string `gorm:"size:64" json:"-"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// IsActive 表示工单仍在处理中（频道存在）。
func (t Ticket) IsActive() bool {
	return t.Status == TicketOpen || t.Status == TicketLocked || t.Status == TicketPending
}

// TicketMessage 是工单频道内的一条聊天记录。
type TicketMessage struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	TicketNo string `gorm:"index;size:20;not null" json:"ticketNo"`
	// MsgID 是 KOOK 消息 ID，可为空（系统生成的事件）。
	MsgID     string `gorm:"index;size:64" json:"msgId"`
	ChannelID string `gorm:"size:64" json:"channelId"`

	UserID   string `gorm:"index;size:64" json:"userId"`
	UserName string `gorm:"size:128" json:"userName"`

	Content string `gorm:"type:text" json:"content"`
	MsgType string `gorm:"size:16" json:"type"`
	IsBot   bool   `json:"isBot"`

	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// TicketNote 是管理员对工单写下的备注（对应原项目的 /tkcm）。
type TicketNote struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	TicketNo string `gorm:"index;size:20;not null" json:"ticketNo"`

	AuthorID   string `gorm:"size:64" json:"authorId"`
	AuthorName string `gorm:"size:128" json:"authorName"`
	// Source 标记备注来源：web 或 kook。
	Source    string    `gorm:"size:8" json:"source"`
	Content   string    `gorm:"type:text" json:"content"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// Panel 是某个频道里的一条工单按钮卡片（对应原项目 TicketConf 的 channel_id 段）。
//
// 同一频道允许存在多条记录：每一张卡片对应一条 Panel，卡片按钮内嵌自己的 ID，
// 因此点击不同卡片可以携带不同的面板管理员角色。
type Panel struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	ChannelID   string `gorm:"index;size:64;not null" json:"channelId"`
	ChannelName string `gorm:"size:128" json:"channelName"`
	// MsgID 是当前生效的面板卡片消息 ID，重新生成面板时更新。
	MsgID string `gorm:"size:64" json:"msgId"`
	// Title 是卡片正文，按 KMarkdown 渲染，允许多行。
	Title string `gorm:"type:text" json:"title"`
	// ButtonText 是开单按钮上的文字；为空时使用默认值。
	//
	// 必须持久化：重建卡片（refresh）或编辑文案后需要原样恢复按钮文字，
	// 否则会把自定义按钮文字冲成默认的 "ticket"。
	ButtonText string    `gorm:"size:32" json:"buttonText"`
	Enabled    bool      `gorm:"default:true" json:"enabled"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`

	Roles []PanelRole `gorm:"foreignKey:PanelID" json:"roles,omitempty"`
}

// PanelRole 是面板级（单频道）管理员角色，对应原项目的 /aar 单频道管理员。
type PanelRole struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	PanelID   uint      `gorm:"index;not null" json:"panelId"`
	RoleID    string    `gorm:"size:64;not null" json:"roleId"`
	RoleName  string    `gorm:"size:128" json:"roleName"`
	CreatedAt time.Time `json:"createdAt"`
}

// AdminRole 是全局管理员角色，对应原项目 TicketConf["ticket"]["admin_role"]。
type AdminRole struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	RoleID    string    `gorm:"uniqueIndex;size:64;not null" json:"roleId"`
	RoleName  string    `gorm:"size:128" json:"roleName"`
	CreatedAt time.Time `json:"createdAt"`
}

// EmojiRule 描述一条“表情回应 → 角色”规则。
type EmojiRule struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	MessageID string `gorm:"index;size:64;not null" json:"messageId"`
	ChannelID string `gorm:"index;size:64" json:"channelId"`
	EmojiID   string `gorm:"size:64;not null" json:"emojiId"`
	RoleID    string `gorm:"size:64;not null" json:"roleId"`
	Label     string `gorm:"size:64" json:"label"`
	Enabled   bool   `gorm:"default:true" json:"enabled"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// EmojiGrant 记录用户最近一次通过表情获得的角色，用于换色时撤销旧角色。
type EmojiGrant struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	KookUserID string    `gorm:"index;size:64;not null" json:"kookUserId"`
	RuleID     uint      `gorm:"index" json:"ruleId"`
	EmojiID    string    `gorm:"size:64" json:"emojiId"`
	RoleID     string    `gorm:"size:64" json:"roleId"`
	GrantedAt  time.Time `json:"grantedAt"`
}

// WebUser 是 WebUI 账号。
//
// 账号可以来自两种途径：
//   - 本地创建（用户名 + 密码）
//   - KOOK 绑定码绑定（KookUserID 非空），权限由 KOOK 角色映射决定
type WebUser struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	Username     string `gorm:"uniqueIndex;size:64;not null" json:"username"`
	DisplayName  string `gorm:"size:64" json:"displayName"`
	PasswordHash string `gorm:"size:100" json:"-"`

	Role string `gorm:"size:16;not null" json:"role"`
	// KookUserID 使用指针以允许存在多个 NULL（SQLite 唯一索引允许多个 NULL，但不允许多个空串）。
	KookUserID   *string `gorm:"uniqueIndex;size:64" json:"kookUserId,omitempty"`
	KookUserName string  `gorm:"size:128" json:"kookUserName,omitempty"`

	Disabled bool `gorm:"default:false" json:"disabled"`
	// MustChangePassword 用于首次启动随机密码场景，未改密前只允许访问改密接口。
	MustChangePassword bool `gorm:"default:false" json:"mustChangePassword"`

	// TOTP 为第二版预留：字段已建，第一版不启用。
	TOTPSecret  string `gorm:"size:128" json:"-"`
	TOTPEnabled bool   `gorm:"default:false" json:"totpEnabled"`

	LastLoginAt *time.Time `json:"lastLoginAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// IsAdmin 判断账号是否为管理员。
func (u WebUser) IsAdmin() bool { return u.Role == RoleAdmin }

// CanOperate 判断账号是否具备处理工单的写权限（管理员或客服）。
func (u WebUser) CanOperate() bool { return RoleAtLeast(u.Role, RoleStaff) }

// Session 是 WebUI 会话。数据库中只保存 token 的哈希。
type Session struct {
	ID uint `gorm:"primaryKey" json:"-"`
	// TokenHash 是会话 token 的 SHA-256，唯一索引用于按 token 查会话。
	TokenHash string `gorm:"uniqueIndex;size:64;not null" json:"-"`
	UserID    uint   `gorm:"index;not null" json:"-"`

	IP        string `gorm:"size:64" json:"-"`
	UserAgent string `gorm:"size:256" json:"-"`

	ExpiresAt  time.Time `gorm:"index;not null" json:"-"`
	LastSeenAt time.Time `json:"-"`
	CreatedAt  time.Time `json:"-"`
}

// AuthCode 是 KOOK 侧签发的一次性码（/login 登录码、/bind 绑定码）。
type AuthCode struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// CodeHash 是码的 SHA-256；明文只在 KOOK 私聊中出现，不落库。
	CodeHash string `gorm:"uniqueIndex;size:64;not null" json:"-"`

	Purpose    string `gorm:"size:16;not null" json:"purpose"`
	KookUserID string `gorm:"index;size:64;not null" json:"kookUserId"`
	// KookUserName 与 RoleHint 便于审计与首登提示。
	KookUserName string `gorm:"size:128" json:"kookUserName"`
	RoleHint     string `gorm:"size:16" json:"roleHint,omitempty"`

	// CreatedByUserID 在 /bind 场景记录发起绑定的已登录账号。
	CreatedByUserID *uint `gorm:"index" json:"createdByUserId,omitempty"`

	ExpiresAt time.Time  `gorm:"index;not null" json:"expiresAt"`
	UsedAt    *time.Time `json:"usedAt,omitempty"`
	UsedIP    string     `gorm:"size:64" json:"usedIp,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

// IsUsable 判断一次性码是否仍可使用。
func (c AuthCode) IsUsable(now time.Time) bool {
	return c.UsedAt == nil && now.Before(c.ExpiresAt)
}

// RoleMapping 是 KOOK 角色 → WebUI 角色的映射规则。
//
// 一个人命中多条规则时取权限最高的一条；未命中任何规则的用户无法登录。
type RoleMapping struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	KookRoleID   string `gorm:"uniqueIndex;size:64;not null" json:"kookRoleId"`
	KookRoleName string `gorm:"size:128" json:"kookRoleName"`
	WebRole      string `gorm:"size:16;not null" json:"webRole"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// AuditLog 是审计日志。仅追加，不提供删除接口。
type AuditLog struct {
	ID uint `gorm:"primaryKey" json:"id"`

	Actor     string `gorm:"index;size:128" json:"actor"`
	ActorType string `gorm:"size:8" json:"actorType"`
	Action    string `gorm:"index;size:64" json:"action"`
	Target    string `gorm:"index;size:128" json:"target"`
	Detail    string `gorm:"type:text" json:"detail"`

	IP        string `gorm:"size:64" json:"ip"`
	RequestID string `gorm:"size:32" json:"requestId"`

	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// AllModels 返回需要自动迁移的全部模型。
func AllModels() []any {
	return []any{
		&Setting{},
		&Ticket{},
		&TicketMessage{},
		&TicketNote{},
		&Panel{},
		&PanelRole{},
		&AdminRole{},
		&EmojiRule{},
		&EmojiGrant{},
		&WebUser{},
		&Session{},
		&AuthCode{},
		&RoleMapping{},
		&AuditLog{},
	}
}
