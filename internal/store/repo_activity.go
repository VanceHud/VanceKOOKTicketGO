package store

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ActivitySingletonID 是 bot_activities 表唯一一行的主键。
//
// 机器人同一时刻只会有一种在玩/在听动态，因此用单行表保存“期望状态”。
const ActivitySingletonID = 1

// ActivityRepo 负责机器人当前动态的读写。
type ActivityRepo struct {
	db *gorm.DB
}

// Current 返回当前动态；不存在时返回 ErrNotFound。
func (r *ActivityRepo) Current() (*BotActivity, error) {
	var activity BotActivity
	if err := r.db.First(&activity, ActivitySingletonID).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &activity, nil
}

// Replace 覆盖当前动态（不存在时创建）。
func (r *ActivityRepo) Replace(activity *BotActivity) error {
	now := Now()
	activity.ID = ActivitySingletonID
	activity.UpdatedAt = now
	if activity.StartedAt.IsZero() {
		activity.StartedAt = now
	}
	// 时间存储不变式：统一写入 UTC。
	activity.StartedAt = activity.StartedAt.UTC()
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"data_type", "game_id", "game_name", "music_name", "singer", "software", "actor", "started_at", "updated_at"}),
	}).Create(activity).Error
}

// Clear 删除当前动态（幂等）。
func (r *ActivityRepo) Clear() error {
	return r.db.Where("id = ?", ActivitySingletonID).Delete(&BotActivity{}).Error
}
