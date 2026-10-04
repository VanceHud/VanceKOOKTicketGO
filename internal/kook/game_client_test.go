package kook_test

import (
	"context"
	"errors"
	"testing"

	"vancekookticket/internal/kook"
	"vancekookticket/internal/kook/kooktest"
)

// newGameClient 启动模拟平台并返回指向它的客户端。
func newGameClient(t *testing.T) (*kook.Client, *kooktest.Server) {
	t.Helper()
	server := kooktest.New()
	t.Cleanup(server.Close)

	client, err := kook.NewClient(kook.Options{
		Token:   "test-token-abcdefghijklmnop",
		BaseURL: server.BaseURL(),
	})
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}
	return client, server
}

func TestGameListFiltersByType(t *testing.T) {
	client, _ := newGameClient(t)
	ctx := context.Background()

	all, err := client.GameList(ctx, kook.GameTypeAll)
	if err != nil {
		t.Fatalf("拉取全部游戏失败: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("期望 2 个游戏，得到 %d", len(all))
	}

	userGames, err := client.GameList(ctx, kook.GameTypeUser)
	if err != nil {
		t.Fatalf("拉取用户游戏失败: %v", err)
	}
	if len(userGames) != 1 || userGames[0].ID != 222222 {
		t.Fatalf("用户创建的游戏应只有 222222，得到 %+v", userGames)
	}

	systemGames, err := client.GameList(ctx, kook.GameTypeSystem)
	if err != nil {
		t.Fatalf("拉取系统游戏失败: %v", err)
	}
	if len(systemGames) != 1 || systemGames[0].ID != 111111 {
		t.Fatalf("系统创建的游戏应只有 111111，得到 %+v", systemGames)
	}
}

func TestGameCRUD(t *testing.T) {
	client, server := newGameClient(t)
	ctx := context.Background()

	created, err := client.GameCreate(ctx, "我的世界", "https://example.com/icon.png")
	if err != nil {
		t.Fatalf("新建游戏失败: %v", err)
	}
	if created.Name != "我的世界" {
		t.Fatalf("返回名称不符: %+v", created)
	}

	createCalls := server.CallsOf("game/create")
	if len(createCalls) != 1 {
		t.Fatalf("期望一次 game/create 调用，得到 %d", len(createCalls))
	}
	if got := createCalls[0].Params["name"]; got != "我的世界" {
		t.Fatalf("请求参数 name 不符: %v", got)
	}

	updated, err := client.GameUpdate(ctx, created.ID, "MC", "")
	if err != nil {
		t.Fatalf("更新游戏失败: %v", err)
	}
	if updated.Name != "MC" {
		t.Fatalf("更新后名称不符: %+v", updated)
	}
	updateCalls := server.CallsOf("game/update")
	if _, ok := updateCalls[0].Params["icon"]; ok {
		t.Fatal("icon 为空时不应发送该字段")
	}

	if err := client.GameDelete(ctx, created.ID); err != nil {
		t.Fatalf("删除游戏失败: %v", err)
	}
	after, err := client.GameList(ctx, kook.GameTypeAll)
	if err != nil {
		t.Fatalf("删除后拉取失败: %v", err)
	}
	for _, game := range after {
		if game.ID == created.ID {
			t.Fatalf("游戏应已被删除: %+v", game)
		}
	}

	// 平台返回 404 时应映射为 ErrNotFound。
	if _, err := client.GameUpdate(ctx, 999999, "x", ""); !errors.Is(err, kook.ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，得到 %v", err)
	}
}

func TestGameCreateSurfacesPlatformError(t *testing.T) {
	client, server := newGameClient(t)
	server.GameCreateFail = true

	_, err := client.GameCreate(context.Background(), "超出上限", "")
	if !errors.Is(err, kook.ErrPermissionDenied) {
		t.Fatalf("期望权限类错误，得到 %v", err)
	}
	var apiErr *kook.APIError
	if !errors.As(err, &apiErr) || apiErr.Message == "" {
		t.Fatalf("应保留平台的错误信息，得到 %v", err)
	}
}

func TestActivityStartAndStop(t *testing.T) {
	client, server := newGameClient(t)
	ctx := context.Background()

	if err := client.StartGameActivity(ctx, 111111); err != nil {
		t.Fatalf("设置游戏动态失败: %v", err)
	}
	gameCalls := server.CallsOf("game/activity")
	if len(gameCalls) != 1 {
		t.Fatalf("期望一次 game/activity 调用，得到 %d", len(gameCalls))
	}
	if got := gameCalls[0].Params["data_type"]; got != float64(kook.ActivityTypeGame) {
		t.Fatalf("data_type 应为 1，得到 %v", got)
	}

	if err := client.StartMusicActivity(ctx, "山水之间", "许嵩", kook.MusicSoftwareKugou); err != nil {
		t.Fatalf("设置音乐动态失败: %v", err)
	}
	musicCalls := server.CallsOf("game/activity")
	last := musicCalls[len(musicCalls)-1]
	if last.Params["software"] != kook.MusicSoftwareKugou {
		t.Fatalf("software 参数不符: %v", last.Params["software"])
	}
	if last.Params["music_name"] != "山水之间" || last.Params["singer"] != "许嵩" {
		t.Fatalf("音乐参数不符: %+v", last.Params)
	}

	if err := client.StartMusicActivity(ctx, "x", "y", ""); err != nil {
		t.Fatalf("software 为空应回退到默认值: %v", err)
	}
	defaultCall := server.CallsOf("game/activity")
	if defaultCall[len(defaultCall)-1].Params["software"] != kook.MusicSoftwareCloudMusic {
		t.Fatalf("默认软件应为 cloudmusic")
	}

	if err := client.StartMusicActivity(ctx, "x", "y", "spotify"); err == nil {
		t.Fatal("不支持的软件应被拒绝")
	}
	if err := client.StartMusicActivity(ctx, "", "许嵩", ""); err == nil {
		t.Fatal("歌曲名为空应被拒绝")
	}

	if err := client.DeleteActivity(ctx, kook.ActivityTypeMusic); err != nil {
		t.Fatalf("停止音乐动态失败: %v", err)
	}
	deleteCalls := server.CallsOf("game/delete-activity")
	if len(deleteCalls) != 1 || deleteCalls[0].Params["data_type"] != float64(kook.ActivityTypeMusic) {
		t.Fatalf("delete-activity 参数不符: %+v", deleteCalls)
	}
}

func TestValidMusicSoftware(t *testing.T) {
	for _, software := range []string{kook.MusicSoftwareCloudMusic, kook.MusicSoftwareQQMusic, kook.MusicSoftwareKugou} {
		if !kook.ValidMusicSoftware(software) {
			t.Fatalf("%s 应为合法软件", software)
		}
	}
	if kook.ValidMusicSoftware("spotify") {
		t.Fatal("spotify 不应合法")
	}
}
