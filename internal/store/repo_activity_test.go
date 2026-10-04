package store

import (
	"errors"
	"testing"
	"time"
)

func TestBotActivityReplaceAndClear(t *testing.T) {
	st := newTestStore(t)

	if _, err := st.Activity.Current(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("初始应无动态，得到 %v", err)
	}

	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := st.Activity.Replace(&BotActivity{
		DataType: 1, GameID: 111111, GameName: "CS", Actor: "admin", StartedAt: started,
	}); err != nil {
		t.Fatalf("写入游戏动态失败: %v", err)
	}

	got, err := st.Activity.Current()
	if err != nil {
		t.Fatalf("读取动态失败: %v", err)
	}
	if got.DataType != 1 || got.GameID != 111111 || got.GameName != "CS" || got.Actor != "admin" {
		t.Fatalf("游戏动态字段不符: %+v", got)
	}
	if !got.StartedAt.Equal(started) {
		t.Fatalf("startedAt 应保留传入的 UTC 时间，得到 %v", got.StartedAt)
	}

	// 覆盖为音乐动态：单行表，不应产生第二行。
	replacedAt := started.Add(time.Hour)
	if err := st.Activity.Replace(&BotActivity{
		DataType: 2, MusicName: "山水之间", Singer: "许嵩", Software: "kugou", StartedAt: replacedAt,
	}); err != nil {
		t.Fatalf("覆盖为音乐动态失败: %v", err)
	}
	got, err = st.Activity.Current()
	if err != nil {
		t.Fatalf("读取动态失败: %v", err)
	}
	if got.DataType != 2 || got.MusicName != "山水之间" || got.GameID != 0 {
		t.Fatalf("覆盖后字段不符: %+v", got)
	}
	if !got.StartedAt.Equal(replacedAt) {
		t.Fatalf("覆盖后 startedAt 不符: %v", got.StartedAt)
	}

	var count int64
	if err := st.DB().Model(&BotActivity{}).Count(&count).Error; err != nil {
		t.Fatalf("统计动态行数失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("动态表应恒为单行，得到 %d 行", count)
	}

	if err := st.Activity.Clear(); err != nil {
		t.Fatalf("清除动态失败: %v", err)
	}
	if _, err := st.Activity.Current(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("清除后应无动态，得到 %v", err)
	}
	// 幂等：重复清除不应报错。
	if err := st.Activity.Clear(); err != nil {
		t.Fatalf("重复清除不应报错: %v", err)
	}
}

func TestActivityAutoRestoreSettingDefaultsToEnabled(t *testing.T) {
	st := newTestStore(t)

	if !st.Settings.ActivityAutoRestore() {
		t.Fatal("默认应开启自动恢复")
	}
	if err := st.Settings.SetActivityAutoRestore(false); err != nil {
		t.Fatalf("关闭自动恢复失败: %v", err)
	}
	if st.Settings.ActivityAutoRestore() {
		t.Fatal("关闭后应返回 false")
	}
	if err := st.Settings.SetActivityAutoRestore(true); err != nil {
		t.Fatalf("开启自动恢复失败: %v", err)
	}
	if !st.Settings.ActivityAutoRestore() {
		t.Fatal("重新开启后应返回 true")
	}
}
