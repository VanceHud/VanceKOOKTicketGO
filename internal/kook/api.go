package kook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client 是 KOOK REST 客户端。
type Client struct {
	token      string
	baseURL    string
	httpClient *http.Client
	limiter    *rateLimiter
	log        *slog.Logger
}

// Options 是客户端构造参数。
type Options struct {
	// Token 是机器人 Token（必填）。
	Token string
	// BaseURL 默认 https://www.kookapp.cn；测试时可指向本地 mock 服务。
	BaseURL string
	// HTTPClient 可选，默认带 15s 超时。
	HTTPClient *http.Client
	// RatePerSecond 与 Burst 控制客户端侧限速，默认 4/s、容量 4。
	RatePerSecond float64
	Burst         int
	Logger        *slog.Logger
}

// NewClient 创建客户端。
func NewClient(opts Options) (*Client, error) {
	token := strings.TrimSpace(opts.Token)
	if token == "" {
		return nil, fmt.Errorf("KOOK Token 不能为空")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	// 默认 4/s：低于 KOOK 的常见上限，留出余量给 WebUI 触发的主动操作。
	rate := opts.RatePerSecond
	if rate <= 0 {
		rate = 4
	}
	burst := opts.Burst
	if burst <= 0 {
		burst = 4
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		token:      token,
		baseURL:    baseURL,
		httpClient: httpClient,
		limiter:    newRateLimiter(rate, burst),
		log:        logger,
	}, nil
}

// BaseURL 返回当前 API 基础地址。
func (c *Client) BaseURL() string { return c.baseURL }

// apiEnvelope 是 KOOK 的统一响应结构。
type apiEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// maxAttempts 是遇到限流时的最大尝试次数。
const maxAttempts = 3

// call 执行一次接口调用。
//
// 说明：
//   - POST 使用 JSON 请求体（KOOK 同时接受表单与 JSON，官方多语言 SDK 均使用 JSON）；
//   - 遇到 429 会依据 Retry-After 退避重试，重试仍失败则返回 ErrRateLimited 包装的错误。
func (c *Client) call(ctx context.Context, method, endpoint string, params map[string]any, out any) error {
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := c.limiter.wait(ctx); err != nil {
			return err
		}

		err := c.do(ctx, method, endpoint, params, out)
		if err == nil {
			return nil
		}
		lastErr = err

		var apiErr *APIError
		if !errors.As(err, &apiErr) || !apiErr.Is(ErrRateLimited) {
			return err
		}

		// 被限流：惩罚性降速后重试
		backoff := time.Duration(attempt) * 2 * time.Second
		c.limiter.penalize(backoff)
		c.log.Warn("KOOK 接口被限流，稍后重试",
			"endpoint", endpoint, "attempt", attempt, "backoff", backoff.String())

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
	return fmt.Errorf("%w: %v", ErrRateLimited, lastErr)
}

func (c *Client) do(ctx context.Context, method, endpoint string, params map[string]any, out any) error {
	var (
		req *http.Request
		err error
	)

	fullURL := c.baseURL + "/api/v3/" + strings.TrimPrefix(endpoint, "/")
	if method == http.MethodPost {
		payload, marshalErr := json.Marshal(nonNilMap(params))
		if marshalErr != nil {
			return fmt.Errorf("序列化请求参数失败: %w", marshalErr)
		}
		req, err = http.NewRequestWithContext(ctx, method, fullURL, bytes.NewReader(payload))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	} else {
		query := url.Values{}
		for key, value := range params {
			if value == nil {
				continue
			}
			query.Set(key, fmt.Sprint(value))
		}
		if encoded := query.Encode(); encoded != "" {
			fullURL += "?" + encoded
		}
		req, err = http.NewRequestWithContext(ctx, method, fullURL, nil)
	}
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}

	req.Header.Set("Authorization", "Bot "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("请求 KOOK 接口失败: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()

	c.observeRateHeaders(resp.Header)

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("读取响应失败: %w", err)
	}

	var envelope apiEnvelope
	if len(body) > 0 {
		if err := json.Unmarshal(body, &envelope); err != nil {
			return &APIError{
				HTTPStatus: resp.StatusCode,
				Code:       -1,
				Message:    fmt.Sprintf("响应不是合法 JSON（前 120 字节：%s）", truncateForLog(body)),
				Endpoint:   endpoint,
			}
		}
	}

	if resp.StatusCode >= 400 || envelope.Code != 0 {
		return &APIError{
			HTTPStatus: resp.StatusCode,
			Code:       envelope.Code,
			Message:    strings.TrimSpace(envelope.Message),
			Endpoint:   endpoint,
		}
	}

	if out != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("解析 %s 响应失败: %w", endpoint, err)
		}
	}
	return nil
}

// observeRateHeaders 读取 KOOK 的限流响应头（若存在），据此动态降速。
func (c *Client) observeRateHeaders(header http.Header) {
	limit, errLimit := strconv.ParseFloat(header.Get("X-Rate-Limit-Limit"), 64)
	remaining, errRemaining := strconv.ParseFloat(header.Get("X-Rate-Limit-Remaining"), 64)
	reset, errReset := strconv.ParseFloat(header.Get("X-Rate-Limit-Reset"), 64)
	if errLimit != nil || errRemaining != nil {
		return
	}
	window := 3 * time.Second
	if errReset == nil && reset > 0 {
		window = time.Duration(reset * float64(time.Second))
	}
	c.limiter.observeRemaining(remaining, limit, window)
}

// asAPIError 是 errors.As 的薄封装，便于在包内直接判断错误类别。
func asAPIError(err error, target **APIError) bool {
	return errors.As(err, target)
}

// nonNilMap 保证 POST 时序列化出 {} 而不是 null。
func nonNilMap(params map[string]any) map[string]any {
	if params == nil {
		return map[string]any{}
	}
	return params
}

func truncateForLog(body []byte) string {
	const limit = 120
	if len(body) <= limit {
		return string(body)
	}
	return string(body[:limit]) + "…"
}

// ---------------------------------------------------------------------------
// 用户与服务器
// ---------------------------------------------------------------------------

// Me 获取机器人自身信息。
func (c *Client) Me(ctx context.Context) (*User, error) {
	var out User
	if err := c.call(ctx, http.MethodGet, "user/me", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UserView 获取用户在服务器中的信息（包含角色列表，用于权限判定）。
func (c *Client) UserView(ctx context.Context, userID, guildID string) (*User, error) {
	var out User
	err := c.call(ctx, http.MethodGet, "user/view", map[string]any{
		"user_id":  userID,
		"guild_id": guildID,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GuildView 获取服务器信息。
func (c *Client) GuildView(ctx context.Context, guildID string) (*Guild, error) {
	var out Guild
	if err := c.call(ctx, http.MethodGet, "guild/view", map[string]any{"guild_id": guildID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GuildRoles 获取服务器角色列表。
func (c *Client) GuildRoles(ctx context.Context, guildID string) ([]Role, error) {
	var out struct {
		Items []Role `json:"items"`
	}
	if err := c.call(ctx, http.MethodGet, "guild-role/list", map[string]any{"guild_id": guildID}, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// GuildRoleGrant 给成员授予角色。
func (c *Client) GuildRoleGrant(ctx context.Context, guildID, userID string, roleID int64) error {
	return c.call(ctx, http.MethodPost, "guild-role/grant", map[string]any{
		"guild_id": guildID,
		"user_id":  userID,
		"role_id":  roleID,
	}, nil)
}

// GuildRoleRevoke 移除成员的角色。
func (c *Client) GuildRoleRevoke(ctx context.Context, guildID, userID string, roleID int64) error {
	return c.call(ctx, http.MethodPost, "guild-role/revoke", map[string]any{
		"guild_id": guildID,
		"user_id":  userID,
		"role_id":  roleID,
	}, nil)
}

// UserOffline 让机器人下线（仅 WebSocket 模式可用）。
func (c *Client) UserOffline(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "user/offline", nil, nil)
}

// ---------------------------------------------------------------------------
// 频道与频道权限
// ---------------------------------------------------------------------------

// ChannelList 获取服务器的频道列表。
func (c *Client) ChannelList(ctx context.Context, guildID string) ([]Channel, error) {
	var out struct {
		Items []Channel `json:"items"`
	}
	if err := c.call(ctx, http.MethodGet, "channel/list", map[string]any{
		"guild_id":  guildID,
		"page_size": 200,
	}, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// ChannelCreate 在指定分组下创建文字频道。
func (c *Client) ChannelCreate(ctx context.Context, guildID, parentID, name string) (*Channel, error) {
	var out Channel
	if err := c.call(ctx, http.MethodPost, "channel/create", map[string]any{
		"guild_id":  guildID,
		"parent_id": parentID,
		"name":      name,
		"type":      ChannelText,
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ChannelDelete 删除频道。
func (c *Client) ChannelDelete(ctx context.Context, channelID string) error {
	return c.call(ctx, http.MethodPost, "channel/delete", map[string]any{"channel_id": channelID}, nil)
}

// ChannelRoleCreate 为频道创建角色/用户权限覆写。
func (c *Client) ChannelRoleCreate(ctx context.Context, channelID, subjectType, value string) error {
	return c.call(ctx, http.MethodPost, "channel-role/create", map[string]any{
		"channel_id": channelID,
		"type":       subjectType,
		"value":      value,
	}, nil)
}

// ChannelRoleUpdate 设置频道内角色/用户的权限位。
//
// allow / deny 是位掩码（见 Permission* 常量），二者不应包含同一位。
func (c *Client) ChannelRoleUpdate(ctx context.Context, channelID, subjectType, value string, allow, deny int) error {
	params := map[string]any{
		"channel_id": channelID,
		"type":       subjectType,
		"value":      value,
		"allow":      allow,
	}
	if deny > 0 {
		params["deny"] = deny
	}
	return c.call(ctx, http.MethodPost, "channel-role/update", params, nil)
}

// ---------------------------------------------------------------------------
// 消息
// ---------------------------------------------------------------------------

// MessageOptions 是发送消息的可选参数。
type MessageOptions struct {
	// TempTargetID 使消息仅对该用户可见（"只有你能看到"）。
	TempTargetID string
	// Quote 引用某条消息（回复）。
	Quote string
}

// SendChannelMessage 向频道发送消息。
//
// msgType 取 MsgType* 常量；卡片消息（MsgTypeCard）的 content 需要是卡片 JSON 数组字符串。
func (c *Client) SendChannelMessage(ctx context.Context, channelID string, msgType int, content string, opts MessageOptions) (*Message, error) {
	params := map[string]any{
		"target_id": channelID,
		"type":      msgType,
		"content":   content,
	}
	if opts.TempTargetID != "" {
		params["temp_target_id"] = opts.TempTargetID
	}
	if opts.Quote != "" {
		params["quote"] = opts.Quote
	}

	var out Message
	if err := c.call(ctx, http.MethodPost, "message/create", params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SendDirectMessage 向用户发送私聊消息。
func (c *Client) SendDirectMessage(ctx context.Context, userID string, msgType int, content string, opts MessageOptions) (*Message, error) {
	params := map[string]any{
		"target_id": userID,
		"type":      msgType,
		"content":   content,
	}
	if opts.Quote != "" {
		params["quote"] = opts.Quote
	}

	var out Message
	if err := c.call(ctx, http.MethodPost, "direct-message/create", params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MessageView 获取频道消息详情。
//
// 卡片消息（type=10）的事件推送不带内容（content 为空），平台把用户上传的
// 文件也统一转成了卡片消息；只有该接口才能拿到卡片 JSON 与附件信息，
// 用于把聊天记录归档成可在 WebUI 渲染/下载的形态。
func (c *Client) MessageView(ctx context.Context, msgID string) (*Message, error) {
	var out Message
	if err := c.call(ctx, http.MethodGet, "message/view", map[string]any{"msg_id": msgID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateChannelMessage 更新频道消息（用于刷新日志卡片）。
func (c *Client) UpdateChannelMessage(ctx context.Context, msgID, content string) error {
	return c.call(ctx, http.MethodPost, "message/update", map[string]any{
		"msg_id":  msgID,
		"content": content,
	}, nil)
}

// UpdateDirectMessage 更新私聊消息。
func (c *Client) UpdateDirectMessage(ctx context.Context, msgID, content string) error {
	return c.call(ctx, http.MethodPost, "direct-message/update", map[string]any{
		"msg_id":  msgID,
		"content": content,
	}, nil)
}

// DeleteChannelMessage 删除频道消息。
func (c *Client) DeleteChannelMessage(ctx context.Context, msgID string) error {
	return c.call(ctx, http.MethodPost, "message/delete", map[string]any{"msg_id": msgID}, nil)
}

// DeleteDirectMessage 删除私聊消息。
func (c *Client) DeleteDirectMessage(ctx context.Context, msgID string) error {
	return c.call(ctx, http.MethodPost, "direct-message/delete", map[string]any{"msg_id": msgID}, nil)
}

// ---------------------------------------------------------------------------
// 游戏库与机器人在玩状态
// ---------------------------------------------------------------------------

// 动态类型。
const (
	ActivityTypeGame  = 1
	ActivityTypeMusic = 2
)

// gameListResponse 对应 game 列表接口的分页结构。
type gameListResponse struct {
	Items []Game `json:"items"`
	Meta  struct {
		Page      int `json:"page"`
		PageTotal int `json:"page_total"`
		PageSize  int `json:"page_size"`
		Total     int `json:"total"`
	} `json:"meta"`
}

// gamePageSize 是拉取游戏列表时使用的分页大小（平台上限通常为 50）。
const gamePageSize = 50

// GameList 拉取游戏列表。
//
// gameType 取 GameType* 常量（0 全部 / 1 用户创建 / 2 系统创建），
// 返回当前第一页（最多 50 条）的结果。
func (c *Client) GameList(ctx context.Context, gameType int) ([]Game, error) {
	var out gameListResponse
	err := c.call(ctx, http.MethodGet, "game", map[string]any{
		"type":      gameType,
		"page":      1,
		"page_size": gamePageSize,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.Items, nil
}

// GameCreate 新建游戏。
//
// 注意：平台限制单日最多创建 5 个游戏，超限时返回明确的业务错误。
func (c *Client) GameCreate(ctx context.Context, name, icon string) (*Game, error) {
	params := map[string]any{"name": strings.TrimSpace(name)}
	if icon = strings.TrimSpace(icon); icon != "" {
		params["icon"] = icon
	}
	var out Game
	if err := c.call(ctx, http.MethodPost, "game/create", params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GameUpdate 更新游戏名称或图标。
//
// name / icon 为空表示不修改；调用方应保证至少提供一项。
func (c *Client) GameUpdate(ctx context.Context, id int64, name, icon string) (*Game, error) {
	params := map[string]any{"id": id}
	if name = strings.TrimSpace(name); name != "" {
		params["name"] = name
	}
	if icon = strings.TrimSpace(icon); icon != "" {
		params["icon"] = icon
	}
	var out Game
	if err := c.call(ctx, http.MethodPost, "game/update", params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GameDelete 删除游戏。
func (c *Client) GameDelete(ctx context.Context, id int64) error {
	return c.call(ctx, http.MethodPost, "game/delete", map[string]any{"id": id}, nil)
}

// StartGameActivity 设置机器人正在玩某个游戏（需先在开发者后台或 WebUI 创建游戏并获得 ID）。
func (c *Client) StartGameActivity(ctx context.Context, gameID int64) error {
	return c.call(ctx, http.MethodPost, "game/activity", map[string]any{
		"id":        gameID,
		"data_type": ActivityTypeGame,
	}, nil)
}

// StartMusicActivity 设置机器人正在听歌。
//
// software 取 MusicSoftware* 常量，为空时使用 cloudmusic；singer / musicName 必填。
func (c *Client) StartMusicActivity(ctx context.Context, musicName, singer, software string) error {
	musicName = strings.TrimSpace(musicName)
	singer = strings.TrimSpace(singer)
	if musicName == "" || singer == "" {
		return fmt.Errorf("歌曲名与歌手不能为空")
	}
	if software = strings.TrimSpace(software); software == "" {
		software = MusicSoftwareCloudMusic
	}
	if !ValidMusicSoftware(software) {
		return fmt.Errorf("不支持的音乐软件 %q", software)
	}
	return c.call(ctx, http.MethodPost, "game/activity", map[string]any{
		"data_type":  ActivityTypeMusic,
		"software":   software,
		"singer":     singer,
		"music_name": musicName,
	}, nil)
}

// DeleteActivity 停止游戏或音乐动态。
func (c *Client) DeleteActivity(ctx context.Context, dataType int) error {
	return c.call(ctx, http.MethodPost, "game/delete-activity", map[string]any{"data_type": dataType}, nil)
}

// ---------------------------------------------------------------------------
// 网关
// ---------------------------------------------------------------------------

// GatewayURL 获取 WebSocket 网关地址。
//
// compress 为真时下行数据使用 zlib 压缩（推荐，可显著减少带宽）。
func (c *Client) GatewayURL(ctx context.Context, compress bool) (string, error) {
	flag := 0
	if compress {
		flag = 1
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := c.call(ctx, http.MethodGet, "gateway/index", map[string]any{"compress": flag}, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", fmt.Errorf("网关地址为空")
	}
	return out.URL, nil
}
