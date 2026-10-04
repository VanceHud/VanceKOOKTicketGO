package api

import (
	"errors"
	"net/http"
	"testing"

	"vancekookticket/internal/kook"
	"vancekookticket/internal/store"
)

// ---------------------------------------------------------------------------
// 游戏库
// ---------------------------------------------------------------------------

func TestGameLibraryCRUD(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)
	fake := env.withFakeBot(t)
	fake.games = []kook.Game{{ID: 222222, Name: "KOOK", Type: 0}}

	// 列表：type 应透传给平台
	res := env.do(t, http.MethodGet, "/api/v1/games?type=1", nil, withCookie(cookie))
	if res.status != http.StatusOK {
		t.Fatalf("拉取游戏列表失败: %d %s", res.status, res.raw)
	}
	if res.body["available"] != true {
		t.Fatalf("在线时应返回 available=true，得到 %v", res.body)
	}
	if items, ok := res.body["items"].([]any); !ok || len(items) != 1 {
		t.Fatalf("期望 1 个游戏，得到 %v", res.body["items"])
	}
	if fake.lastGameType != kook.GameTypeUser {
		t.Fatalf("type 应透传为 1，得到 %d", fake.lastGameType)
	}

	// 非法 type 应被拒绝
	res = env.do(t, http.MethodGet, "/api/v1/games?type=9", nil, withCookie(cookie))
	if res.status != http.StatusBadRequest {
		t.Fatalf("非法 type 应返回 400，得到 %d", res.status)
	}

	// 新建
	res = env.do(t, http.MethodPost, "/api/v1/games", map[string]any{
		"name": "我的世界", "icon": "https://example.com/icon.png",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusCreated {
		t.Fatalf("新建游戏失败: %d %s", res.status, res.raw)
	}
	if len(fake.createdGames) != 1 || fake.createdGames[0] != "我的世界" {
		t.Fatalf("应调用平台新建游戏，得到 %v", fake.createdGames)
	}

	// 参数校验
	res = env.do(t, http.MethodPost, "/api/v1/games", map[string]any{"name": "  "},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("空名称应返回 400，得到 %d", res.status)
	}
	res = env.do(t, http.MethodPost, "/api/v1/games", map[string]any{
		"name": "坏图标", "icon": "ftp://example.com/icon.png",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("非 http(s) 图标应返回 400，得到 %d", res.status)
	}

	// 更新
	res = env.do(t, http.MethodPatch, "/api/v1/games/123", map[string]any{"name": "MC"},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("更新游戏失败: %d %s", res.status, res.raw)
	}
	if len(fake.updatedGames) != 1 || fake.updatedGames[0] != 123 {
		t.Fatalf("应调用平台更新游戏，得到 %v", fake.updatedGames)
	}
	// 未提供任何字段应被拒绝
	res = env.do(t, http.MethodPatch, "/api/v1/games/123", map[string]any{},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusBadRequest {
		t.Fatalf("空更新应返回 400，得到 %d", res.status)
	}

	// 删除
	res = env.do(t, http.MethodDelete, "/api/v1/games/123", nil, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("删除游戏失败: %d %s", res.status, res.raw)
	}
	if len(fake.deletedGames) != 1 || fake.deletedGames[0] != 123 {
		t.Fatalf("应调用平台删除游戏，得到 %v", fake.deletedGames)
	}

	// 审计
	for _, action := range []string{"game.create", "game.update", "game.delete"} {
		if env.auditCount(t, action) == 0 {
			t.Fatalf("%s 应写入审计", action)
		}
	}
}

func TestGameMutationsRequireAdminAndOnlineBot(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "staff", staffPassword, store.RoleStaff, false)
	staffCookie, staffCSRF := env.login(t, "staff", staffPassword)

	// 客服无权新建游戏
	res := env.do(t, http.MethodPost, "/api/v1/games", map[string]any{"name": "x"},
		withCookie(staffCookie), withCSRF(staffCSRF))
	if res.status != http.StatusForbidden {
		t.Fatalf("客服新建游戏应返回 403，得到 %d", res.status)
	}

	// 管理员但机器人离线：写操作返回 503（DryRun 不影响写操作的在线要求）
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)
	res = env.do(t, http.MethodPost, "/api/v1/games", map[string]any{"name": "x"},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusServiceUnavailable {
		t.Fatalf("机器人离线时新建游戏应返回 503，得到 %d", res.status)
	}
}

func TestGameListDryRunReturnsDemoData(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "readonly", readonlyPassword, store.RoleReadonly, false)
	cookie, _ := env.login(t, "readonly", readonlyPassword)

	// newTestEnv 的 DryRun=true 且未注入机器人：应返回演示数据供界面验收。
	res := env.do(t, http.MethodGet, "/api/v1/games?type=0", nil, withCookie(cookie))
	if res.status != http.StatusOK {
		t.Fatalf("DryRun 下拉取游戏失败: %d %s", res.status, res.raw)
	}
	if res.body["dryRun"] != true {
		t.Fatalf("DryRun 下应标记 dryRun=true，得到 %v", res.body)
	}
	if items, ok := res.body["items"].([]any); !ok || len(items) == 0 {
		t.Fatalf("DryRun 下应返回演示游戏，得到 %v", res.body["items"])
	}
}

// ---------------------------------------------------------------------------
// 在玩动态
// ---------------------------------------------------------------------------

func TestActivityLifecycle(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)
	fake := env.withFakeBot(t)
	fake.games = []kook.Game{{ID: 111111, Name: "CS", Type: 0}}

	// 初始：无动态，默认开启自动恢复
	res := env.do(t, http.MethodGet, "/api/v1/bot/activity", nil, withCookie(cookie))
	if res.status != http.StatusOK {
		t.Fatalf("读取动态失败: %d %s", res.status, res.raw)
	}
	if res.body["current"] != nil {
		t.Fatalf("初始不应有动态，得到 %v", res.body["current"])
	}
	if res.body["autoRestore"] != true {
		t.Fatalf("默认应开启自动恢复，得到 %v", res.body["autoRestore"])
	}

	// 开始游戏：名称应由服务端从游戏库解析
	res = env.do(t, http.MethodPost, "/api/v1/bot/activity", map[string]any{
		"dataType": 1, "gameId": 111111,
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("开始游戏失败: %d %s", res.status, res.raw)
	}
	if len(fake.startedGames) != 1 || fake.startedGames[0] != 111111 {
		t.Fatalf("应调用平台设置游戏动态，得到 %v", fake.startedGames)
	}
	stored, err := env.store.Activity.Current()
	if err != nil {
		t.Fatalf("读取本地动态失败: %v", err)
	}
	if stored.DataType != 1 || stored.GameID != 111111 || stored.GameName != "CS" {
		t.Fatalf("本地动态记录不符: %+v", stored)
	}
	if stored.Actor != "admin" {
		t.Fatalf("应记录操作者，得到 %q", stored.Actor)
	}

	// 切换为音乐动态
	res = env.do(t, http.MethodPost, "/api/v1/bot/activity", map[string]any{
		"dataType": 2, "musicName": "山水之间", "singer": "许嵩", "software": "kugou",
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("开始听歌失败: %d %s", res.status, res.raw)
	}
	if len(fake.startedMusic) != 1 || fake.startedMusic[0] != "山水之间|许嵩|kugou" {
		t.Fatalf("音乐参数不符: %v", fake.startedMusic)
	}
	stored, _ = env.store.Activity.Current()
	if stored.DataType != 2 || stored.MusicName != "山水之间" || stored.Software != "kugou" {
		t.Fatalf("音乐动态记录不符: %+v", stored)
	}

	// 参数校验
	for name, payload := range map[string]map[string]any{
		"非法类型":   {"dataType": 3},
		"缺少游戏ID": {"dataType": 1},
		"缺少歌手":   {"dataType": 2, "musicName": "x"},
		"非法软件":   {"dataType": 2, "musicName": "x", "singer": "y", "software": "spotify"},
	} {
		res = env.do(t, http.MethodPost, "/api/v1/bot/activity", payload, withCookie(cookie), withCSRF(csrf))
		if res.status != http.StatusBadRequest {
			t.Fatalf("%s 应返回 400，得到 %d", name, res.status)
		}
	}

	// 更新自动恢复设置
	res = env.do(t, http.MethodPut, "/api/v1/bot/activity/settings", map[string]any{"autoRestore": false},
		withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("更新动态设置失败: %d %s", res.status, res.raw)
	}
	if env.store.Settings.ActivityAutoRestore() {
		t.Fatal("自动恢复应已关闭")
	}

	// 停止动态：应清除本地记录并调用平台删除两种类型
	res = env.do(t, http.MethodDelete, "/api/v1/bot/activity", nil, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("停止动态失败: %d %s", res.status, res.raw)
	}
	if _, err := env.store.Activity.Current(); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("停止后本地应无动态，得到 %v", err)
	}
	if len(fake.deletedTypes) != 2 {
		t.Fatalf("应停止游戏与音乐两种动态，得到 %v", fake.deletedTypes)
	}

	for _, action := range []string{"bot.activity.start", "bot.activity.stop", "bot.activity.settings"} {
		if env.auditCount(t, action) == 0 {
			t.Fatalf("%s 应写入审计", action)
		}
	}
}

func TestActivityStopClearsStateWhenBotOffline(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)
	fake := env.withFakeBot(t)

	// 预置一条本地动态，然后把机器人标记为离线。
	if err := env.store.Activity.Replace(&store.BotActivity{DataType: 1, GameID: 111111, GameName: "CS"}); err != nil {
		t.Fatalf("预置动态失败: %v", err)
	}
	fake.status.Connected = false

	res := env.do(t, http.MethodDelete, "/api/v1/bot/activity", nil, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusOK {
		t.Fatalf("离线停止应成功清理本地状态: %d %s", res.status, res.raw)
	}
	if _, err := env.store.Activity.Current(); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("离线停止后本地应无动态，得到 %v", err)
	}
	if len(fake.deletedTypes) != 0 {
		t.Fatalf("离线时不应调用平台，得到 %v", fake.deletedTypes)
	}
}

func TestActivityStartRequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "readonly", readonlyPassword, store.RoleReadonly, false)
	cookie, csrf := env.login(t, "readonly", readonlyPassword)
	env.withFakeBot(t)

	res := env.do(t, http.MethodPost, "/api/v1/bot/activity", map[string]any{
		"dataType": 1, "gameId": 111111,
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusForbidden {
		t.Fatalf("只读账号应返回 403，得到 %d", res.status)
	}
}

func TestActivityStartMapsPlatformError(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)
	fake := env.withFakeBot(t)
	fake.gameErr = &kook.APIError{HTTPStatus: 403, Code: 40300, Message: "机器人缺少所需权限"}

	res := env.do(t, http.MethodPost, "/api/v1/bot/activity", map[string]any{
		"dataType": 1, "gameId": 111111,
	}, withCookie(cookie), withCSRF(csrf))
	if res.status != http.StatusForbidden {
		t.Fatalf("平台无权限应映射为 403，得到 %d %s", res.status, res.raw)
	}
}
