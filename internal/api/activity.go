package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/bot"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// 游戏与动态的输入限制。
const (
	maxGameNameLength = 64
	maxGameIconLength = 512
	maxMusicNameLen   = 200
	maxSingerLength   = 200
)

// ---------------------------------------------------------------------------
// 游戏库
// ---------------------------------------------------------------------------

// handleGameList 返回游戏库（只读）。
//
// 与角色/频道列表一致：机器人在线时读取平台数据；离线时返回 available=false
// 并给出提示，保证界面在离线状态下依然可用。
func (s *Server) handleGameList(c *gin.Context) {
	gameType := atoiDefault(c.Query("type"), kook.GameTypeAll)
	if !validGameType(gameType) {
		s.fail(c, http.StatusBadRequest, "invalid_request", "type 只能是 0（全部）、1（用户创建）或 2（系统创建）")
		return
	}

	if s.Bot != nil && s.botConnected() {
		games, err := s.Bot.GameList(c.Request.Context(), gameType)
		if err != nil {
			s.failKook(c, err, "games.list")
			return
		}
		if games == nil {
			games = []kook.Game{}
		}
		c.JSON(http.StatusOK, gin.H{"available": true, "dryRun": false, "type": gameType, "items": games})
		return
	}

	if s.Config.DryRun {
		c.JSON(http.StatusOK, gin.H{
			"available": true,
			"dryRun":    true,
			"type":      gameType,
			"items": []kook.Game{
				{ID: 111111, Name: "CS", Type: 0, ProcessName: []string{"cstrike"}},
				{ID: 222222, Name: "KOOK", Type: 0},
			},
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"available": false,
		"dryRun":    false,
		"type":      gameType,
		"items":     []kook.Game{},
		"note":      "游戏库需要机器人连接 KOOK 后获取",
	})
}

// handleGameCreate 新建游戏（仅管理员）。
func (s *Server) handleGameCreate(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
		Icon string `json:"icon"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		s.fail(c, http.StatusBadRequest, "invalid_request", "游戏名称不能为空")
		return
	}
	if len([]rune(name)) > maxGameNameLength {
		s.fail(c, http.StatusBadRequest, "invalid_request", fmt.Sprintf("游戏名称不能超过 %d 个字符", maxGameNameLength))
		return
	}
	icon, ok := normalizeIcon(req.Icon)
	if !ok {
		s.fail(c, http.StatusBadRequest, "invalid_request", "图标必须是 http(s) 链接")
		return
	}
	if err := s.requireBotOnline(c, "新建游戏需要机器人连接 KOOK"); err != nil {
		return
	}

	game, err := s.Bot.GameCreate(c.Request.Context(), name, icon)
	if err != nil {
		s.failKook(c, err, "games.create")
		return
	}
	s.audit(c, "game.create", fmt.Sprint(game.ID), "新建游戏："+name)
	c.JSON(http.StatusCreated, game)
}

// handleGameUpdate 更新游戏名称/图标（仅管理员）。
func (s *Server) handleGameUpdate(c *gin.Context) {
	id, ok := parseGameID(c.Param("id"))
	if !ok {
		s.fail(c, http.StatusBadRequest, "invalid_request", "游戏 ID 不合法")
		return
	}
	var req struct {
		Name *string `json:"name"`
		Icon *string `json:"icon"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	if req.Name == nil && req.Icon == nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "至少需要提供 name 或 icon")
		return
	}

	var name string
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if name == "" {
			s.fail(c, http.StatusBadRequest, "invalid_request", "游戏名称不能为空")
			return
		}
		if len([]rune(name)) > maxGameNameLength {
			s.fail(c, http.StatusBadRequest, "invalid_request", fmt.Sprintf("游戏名称不能超过 %d 个字符", maxGameNameLength))
			return
		}
	}
	var icon string
	if req.Icon != nil {
		normalized, valid := normalizeIcon(*req.Icon)
		if !valid {
			s.fail(c, http.StatusBadRequest, "invalid_request", "图标必须是 http(s) 链接")
			return
		}
		icon = normalized
	}
	if err := s.requireBotOnline(c, "更新游戏需要机器人连接 KOOK"); err != nil {
		return
	}

	game, err := s.Bot.GameUpdate(c.Request.Context(), id, name, icon)
	if err != nil {
		s.failKook(c, err, "games.update")
		return
	}
	s.audit(c, "game.update", fmt.Sprint(id), "更新游戏："+game.Name)
	c.JSON(http.StatusOK, game)
}

// handleGameDelete 删除游戏（仅管理员）。
func (s *Server) handleGameDelete(c *gin.Context) {
	id, ok := parseGameID(c.Param("id"))
	if !ok {
		s.fail(c, http.StatusBadRequest, "invalid_request", "游戏 ID 不合法")
		return
	}
	if err := s.requireBotOnline(c, "删除游戏需要机器人连接 KOOK"); err != nil {
		return
	}
	if err := s.Bot.GameDelete(c.Request.Context(), id); err != nil {
		s.failKook(c, err, "games.delete")
		return
	}
	s.audit(c, "game.delete", fmt.Sprint(id), "删除游戏")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---------------------------------------------------------------------------
// 在玩动态
// ---------------------------------------------------------------------------

// activityResponse 是当前动态的接口表示。
type activityResponse struct {
	DataType  int    `json:"dataType"`
	GameID    int64  `json:"gameId,omitempty"`
	GameName  string `json:"gameName,omitempty"`
	MusicName string `json:"musicName,omitempty"`
	Singer    string `json:"singer,omitempty"`
	Software  string `json:"software,omitempty"`
	Actor     string `json:"actor,omitempty"`
	StartedAt string `json:"startedAt"`
}

// handleActivityGet 返回当前动态与自动恢复开关（只读）。
func (s *Server) handleActivityGet(c *gin.Context) {
	resp := gin.H{
		"connected":   s.botConnected(),
		"dryRun":      s.Config.DryRun,
		"autoRestore": s.Store.Settings.ActivityAutoRestore(),
		"current":     nil,
	}
	activity, err := s.Store.Activity.Current()
	switch {
	case err == nil:
		resp["current"] = activityResponse{
			DataType:  activity.DataType,
			GameID:    activity.GameID,
			GameName:  activity.GameName,
			MusicName: activity.MusicName,
			Singer:    activity.Singer,
			Software:  activity.Software,
			Actor:     activity.Actor,
			StartedAt: activity.StartedAt.Format(time.RFC3339),
		}
	case errors.Is(err, store.ErrNotFound):
		// 没有动态属于正常状态。
	default:
		s.failInternal(c, err, "activity.get")
		return
	}
	c.JSON(http.StatusOK, resp)
}

// handleActivityStart 设置机器人在玩/在听动态（仅管理员）。
func (s *Server) handleActivityStart(c *gin.Context) {
	var req struct {
		DataType  int    `json:"dataType"`
		GameID    int64  `json:"gameId"`
		GameName  string `json:"gameName"`
		MusicName string `json:"musicName"`
		Singer    string `json:"singer"`
		Software  string `json:"software"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	if req.DataType != kook.ActivityTypeGame && req.DataType != kook.ActivityTypeMusic {
		s.fail(c, http.StatusBadRequest, "invalid_request", "dataType 只能是 1（游戏）或 2（音乐）")
		return
	}
	if err := s.requireBotOnline(c, "设置动态需要机器人连接 KOOK"); err != nil {
		return
	}

	activity := &store.BotActivity{DataType: req.DataType, Actor: actorName(c), StartedAt: store.Now()}
	switch req.DataType {
	case kook.ActivityTypeGame:
		if req.GameID <= 0 {
			s.fail(c, http.StatusBadRequest, "invalid_request", "gameId 必须是正整数")
			return
		}
		gameName := strings.TrimSpace(req.GameName)
		if len([]rune(gameName)) > maxGameNameLength {
			s.fail(c, http.StatusBadRequest, "invalid_request", "游戏名称过长")
			return
		}
		if gameName == "" {
			gameName = s.resolveGameName(c, req.GameID)
		}
		if err := s.Bot.StartGameActivity(c.Request.Context(), req.GameID); err != nil {
			s.failKook(c, err, "activity.start.game")
			return
		}
		activity.GameID, activity.GameName = req.GameID, gameName
		s.audit(c, "bot.activity.start", fmt.Sprint(req.GameID), "开始玩游戏："+firstNonEmptyString(gameName, fmt.Sprint(req.GameID)))

	case kook.ActivityTypeMusic:
		musicName := strings.TrimSpace(req.MusicName)
		singer := strings.TrimSpace(req.Singer)
		if musicName == "" || singer == "" {
			s.fail(c, http.StatusBadRequest, "invalid_request", "歌曲名与歌手不能为空")
			return
		}
		if len([]rune(musicName)) > maxMusicNameLen || len([]rune(singer)) > maxSingerLength {
			s.fail(c, http.StatusBadRequest, "invalid_request", "歌曲名或歌手过长")
			return
		}
		software := strings.TrimSpace(req.Software)
		if software == "" {
			software = kook.MusicSoftwareCloudMusic
		}
		if !kook.ValidMusicSoftware(software) {
			s.fail(c, http.StatusBadRequest, "invalid_request", "音乐软件只能是 cloudmusic、qqmusic 或 kugou")
			return
		}
		if err := s.Bot.StartMusicActivity(c.Request.Context(), musicName, singer, software); err != nil {
			s.failKook(c, err, "activity.start.music")
			return
		}
		activity.MusicName, activity.Singer, activity.Software = musicName, singer, software
		s.audit(c, "bot.activity.start", musicName, "开始听歌："+musicName+" - "+singer)
	}

	if err := s.Store.Activity.Replace(activity); err != nil {
		// 平台上已生效，本地持久化失败只影响自动恢复，不向用户报错。
		s.Log.Error("保存动态状态失败", "err", err)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleActivityStop 停止当前动态并清除待恢复状态（仅管理员）。
//
// 机器人离线时仍会清除本地记录：否则重连后会自动恢复一个用户已经“停止”的动态。
func (s *Server) handleActivityStop(c *gin.Context) {
	dataType := atoiDefault(c.Query("type"), 0)
	if dataType != 0 && dataType != kook.ActivityTypeGame && dataType != kook.ActivityTypeMusic {
		s.fail(c, http.StatusBadRequest, "invalid_request", "type 只能是 0（全部）、1（游戏）或 2（音乐）")
		return
	}

	// 平台上删除动态：离线时跳过，仅清理本地状态。
	var platformErr error
	if s.Bot != nil && s.botConnected() {
		targets := []int{kook.ActivityTypeGame, kook.ActivityTypeMusic}
		if dataType != 0 {
			targets = []int{dataType}
		}
		for _, target := range targets {
			if err := s.Bot.DeleteActivity(c.Request.Context(), target); err != nil {
				platformErr = err
			}
		}
	}

	if err := s.Store.Activity.Clear(); err != nil {
		s.failInternal(c, err, "activity.stop")
		return
	}
	s.audit(c, "bot.activity.stop", "activity", "停止机器人动态")

	if platformErr != nil {
		// 本地已清除；平台侧失败给出提示，让管理员重试。
		s.failKook(c, platformErr, "activity.stop")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleActivitySettings 更新动态相关设置（仅管理员）。
func (s *Server) handleActivitySettings(c *gin.Context) {
	var req struct {
		AutoRestore *bool `json:"autoRestore"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	if req.AutoRestore == nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "autoRestore 不能为空")
		return
	}
	if err := s.Store.Settings.SetActivityAutoRestore(*req.AutoRestore); err != nil {
		s.failInternal(c, err, "activity.settings")
		return
	}
	s.audit(c, "bot.activity.settings", "activity",
		fmt.Sprintf("自动恢复在玩状态：%v", *req.AutoRestore))
	c.JSON(http.StatusOK, gin.H{"ok": true, "autoRestore": *req.AutoRestore})
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

// requireBotOnline 在机器人未连接时返回 503；返回非 nil 表示已写入响应。
func (s *Server) requireBotOnline(c *gin.Context, message string) error {
	if s.Bot == nil {
		s.fail(c, http.StatusServiceUnavailable, "bot_disabled", "机器人模块未启用")
		return errors.New("bot disabled")
	}
	if !s.botConnected() {
		s.fail(c, http.StatusServiceUnavailable, "bot_offline", message+"。请先在「系统设置」完成配置并重连。")
		return errors.New("bot offline")
	}
	return nil
}

// resolveGameName 尽力把游戏 ID 解析为名称（失败返回空串）。
func (s *Server) resolveGameName(c *gin.Context, gameID int64) string {
	if s.Bot == nil {
		return ""
	}
	games, err := s.Bot.GameList(c.Request.Context(), kook.GameTypeAll)
	if err != nil {
		return ""
	}
	for _, game := range games {
		if game.ID == gameID {
			return game.Name
		}
	}
	return ""
}

// failKook 把平台错误映射为合适的 HTTP 状态码。
func (s *Server) failKook(c *gin.Context, err error, action string) {
	if errors.Is(err, bot.ErrNotConnected) {
		s.fail(c, http.StatusServiceUnavailable, "bot_offline", "机器人未连接 KOOK，请先在「系统设置」完成配置并重连")
		return
	}
	var apiErr *kook.APIError
	if errors.As(err, &apiErr) {
		status := http.StatusBadGateway
		switch {
		case apiErr.Is(kook.ErrTokenInvalid):
			status = http.StatusBadGateway
		case apiErr.Is(kook.ErrPermissionDenied):
			status = http.StatusForbidden
		case apiErr.Is(kook.ErrNotFound):
			status = http.StatusNotFound
		case apiErr.Is(kook.ErrRateLimited):
			status = http.StatusTooManyRequests
		case apiErr.HTTPStatus >= 400 && apiErr.HTTPStatus < 500:
			status = http.StatusBadRequest
		}
		message := strings.TrimSpace(apiErr.Message)
		if message == "" {
			message = err.Error()
		}
		s.fail(c, status, "kook_error", message)
		return
	}
	s.failInternal(c, err, action)
}

func validGameType(value int) bool {
	switch value {
	case kook.GameTypeAll, kook.GameTypeUser, kook.GameTypeSystem:
		return true
	default:
		return false
	}
}

// parseGameID 解析游戏 ID（平台为整数主键）。
func parseGameID(raw string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// normalizeIcon 校验并归一化图标链接；空值合法（表示不设置图标）。
func normalizeIcon(raw string) (string, bool) {
	icon := strings.TrimSpace(raw)
	if icon == "" {
		return "", true
	}
	if len(icon) > maxGameIconLength {
		return "", false
	}
	parsed, err := url.Parse(icon)
	if err != nil {
		return "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	if parsed.Host == "" {
		return "", false
	}
	return icon, true
}
