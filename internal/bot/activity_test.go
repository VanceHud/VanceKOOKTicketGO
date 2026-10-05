package bot

import (
	"testing"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/kook"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// activityCalls 返回模拟平台上 game/activity 的调用次数。
func activityCalls(env *botEnv) int { return len(env.mock.CallsOf("game/activity")) }

func TestRestoreActivityReappliesPersistedGame(t *testing.T) {
	env := newBotEnv(t)
	before := activityCalls(env)

	if err := env.store.Activity.Replace(&store.BotActivity{
		DataType: kook.ActivityTypeGame, GameID: 111111, GameName: "CS",
	}); err != nil {
		t.Fatalf("写入待恢复动态失败: %v", err)
	}

	env.bot.restoreActivity()

	calls := env.mock.CallsOf("game/activity")
	if len(calls) != before+1 {
		t.Fatalf("应恢复一次游戏动态，得到 %d 次调用", len(calls)-before)
	}
	last := calls[len(calls)-1]
	if last.Params["data_type"] != float64(kook.ActivityTypeGame) {
		t.Fatalf("data_type 应为 1，得到 %v", last.Params["data_type"])
	}
	if last.Params["id"] != float64(111111) {
		t.Fatalf("游戏 ID 应为 111111，得到 %v", last.Params["id"])
	}
}

func TestRestoreActivityReappliesPersistedMusic(t *testing.T) {
	env := newBotEnv(t)
	before := activityCalls(env)

	if err := env.store.Activity.Replace(&store.BotActivity{
		DataType: kook.ActivityTypeMusic, MusicName: "山水之间", Singer: "许嵩", Software: kook.MusicSoftwareKugou,
	}); err != nil {
		t.Fatalf("写入待恢复动态失败: %v", err)
	}

	env.bot.restoreActivity()

	calls := env.mock.CallsOf("game/activity")
	if len(calls) != before+1 {
		t.Fatalf("应恢复一次音乐动态，得到 %d 次调用", len(calls)-before)
	}
	last := calls[len(calls)-1]
	if last.Params["data_type"] != float64(kook.ActivityTypeMusic) {
		t.Fatalf("data_type 应为 2，得到 %v", last.Params["data_type"])
	}
	if last.Params["music_name"] != "山水之间" || last.Params["software"] != kook.MusicSoftwareKugou {
		t.Fatalf("音乐参数不符: %+v", last.Params)
	}
}

func TestRestoreActivitySkipsWhenDisabledOrEmpty(t *testing.T) {
	env := newBotEnv(t)

	// 没有持久化动态时不调用平台
	before := activityCalls(env)
	env.bot.restoreActivity()
	if activityCalls(env) != before {
		t.Fatal("没有动态时不应调用平台")
	}

	// 有动态但关闭了自动恢复：同样不调用平台
	if err := env.store.Activity.Replace(&store.BotActivity{
		DataType: kook.ActivityTypeGame, GameID: 111111, GameName: "CS",
	}); err != nil {
		t.Fatalf("写入待恢复动态失败: %v", err)
	}
	if err := env.store.Settings.SetActivityAutoRestore(false); err != nil {
		t.Fatalf("关闭自动恢复失败: %v", err)
	}
	env.bot.restoreActivity()
	if activityCalls(env) != before {
		t.Fatal("关闭自动恢复后不应调用平台")
	}
}
