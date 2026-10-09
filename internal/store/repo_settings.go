package store

import (
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/secure"
)

// settings 表中的键名。
const (
	// SettingKookToken 保存 AES-GCM 加密后的 KOOK token，明文不落库。
	SettingKookToken = "kook_token_enc"
	// SettingGuildID 是机器人服务的服务器 ID（单 guild）。
	SettingGuildID = "guild_id"
	// SettingCategoryID 是放置工单频道的隐藏分组 ID。
	SettingCategoryID = "category_id"
	// SettingLogChannelID 是工单日志频道 ID。
	SettingLogChannelID = "log_channel_id"
	// SettingDebugChannelID 是错误与调试信息频道 ID。
	SettingDebugChannelID = "debug_channel_id"
	// SettingOutdateHours 是工单空闲锁定阈值（小时）。
	SettingOutdateHours = "outdate_hours"
	// SettingActivityAutoRestore 控制在机器人重连后是否自动恢复上次的在玩/在听动态。
	SettingActivityAutoRestore = "activity_auto_restore"
	// SettingInitializedAt 记录首次初始化时间。
	SettingInitializedAt = "initialized_at"
	// SettingGatewaySessionID / SettingGatewaySessionSN 持久化 KOOK 网关会话，
	// 供进程重启（升级、重建容器）后带旧会话 resume 续传。
	SettingGatewaySessionID = "kook_gateway_session_id"
	SettingGatewaySessionSN = "kook_gateway_sn"
	// SettingGuildName / SettingCategoryName 等仅用于界面展示，避免每次都请求 KOOK。
	SettingGuildName      = "guild_name"
	SettingCategoryName   = "category_name"
	SettingLogChannelName = "log_channel_name"
	SettingDebugChName    = "debug_channel_name"
)

// DefaultOutdateHours 是工单空闲锁定的默认阈值。
const DefaultOutdateHours = 48

// SettingsRepo 负责键值配置读写。
type SettingsRepo struct {
	db *gorm.DB
}

// Get 读取配置；第二个返回值表示是否存在。
func (r *SettingsRepo) Get(key string) (string, bool, error) {
	var s Setting
	err := r.db.Where("key = ?", key).First(&s).Error
	if err != nil {
		if mapped := mapNotFound(err); mapped == ErrNotFound {
			return "", false, nil
		}
		return "", false, err
	}
	return s.Value, true, nil
}

// GetDefault 读取配置，不存在时返回默认值。
func (r *SettingsRepo) GetDefault(key, def string) (string, error) {
	value, ok, err := r.Get(key)
	if err != nil || !ok {
		return def, err
	}
	return value, nil
}

// Set 写入（或覆盖）配置。
func (r *SettingsRepo) Set(key, value string) error {
	return upsertSetting(r.db, key, value)
}

// SettingChange 是一次原子配置变更：Upserts 全部写入，Deletes 全部删除。
type SettingChange struct {
	Upserts map[string]string
	Deletes []string
}

// Apply 在单个事务内应用一组配置变更。
//
// 设置页一次保存会同时更新十来个键（服务器/分组/频道 ID、展示名、超时阈值、
// token），逐键独立提交时任一步失败（磁盘满、busy 超时）会留下半套配置，
// 而保存成功后会立刻按变更项触发机器人重连——必须要么全部生效、要么全部不变。
func (r *SettingsRepo) Apply(change SettingChange) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		for _, key := range change.Deletes {
			if err := tx.Where("key = ?", key).Delete(&Setting{}).Error; err != nil {
				return err
			}
		}
		for key, value := range change.Upserts {
			if err := upsertSetting(tx, key, value); err != nil {
				return err
			}
		}
		return nil
	})
}

// upsertSetting 在给定句柄（连接或事务）上写入配置项。
func upsertSetting(tx *gorm.DB, key, value string) error {
	item := Setting{Key: key, Value: value, UpdatedAt: Now()}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&item).Error
}

// Delete 删除配置项。
func (r *SettingsRepo) Delete(key string) error {
	return r.db.Where("key = ?", key).Delete(&Setting{}).Error
}

// All 返回全部配置项。
func (r *SettingsRepo) All() (map[string]string, error) {
	var items []Setting
	if err := r.db.Find(&items).Error; err != nil {
		return nil, err
	}
	out := make(map[string]string, len(items))
	for _, item := range items {
		out[item.Key] = item.Value
	}
	return out, nil
}

// GetInt 读取整型配置，缺失或非法时返回默认值。
func (r *SettingsRepo) GetInt(key string, def int) int {
	value, ok, err := r.Get(key)
	if err != nil || !ok || value == "" {
		return def
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return def
	}
	return parsed
}

// SetInt 写入整型配置。
func (r *SettingsRepo) SetInt(key string, value int) error {
	return r.Set(key, strconv.Itoa(value))
}

// SetString 写入字符串指针（空指针视为清空）。
func (r *SettingsRepo) SetString(key string, value *string) error {
	if value == nil {
		return r.Delete(key)
	}
	return r.Set(key, *value)
}

// SetSecret 使用 AES-GCM 加密后写入敏感配置。
func (r *SettingsRepo) SetSecret(key, plaintext string, appSecret []byte) error {
	if plaintext == "" {
		return r.Delete(key)
	}
	encrypted, err := secure.Encrypt(appSecret, plaintext)
	if err != nil {
		return fmt.Errorf("加密配置 %s 失败: %w", key, err)
	}
	return r.Set(key, encrypted)
}

// GetSecret 读取并解密敏感配置。
// 密钥变更或数据被篡改时返回 secure.ErrDecrypt，调用方应提示重新录入。
func (r *SettingsRepo) GetSecret(key string, appSecret []byte) (string, bool, error) {
	encrypted, ok, err := r.Get(key)
	if err != nil || !ok || encrypted == "" {
		return "", false, err
	}
	plaintext, err := secure.Decrypt(appSecret, encrypted)
	if err != nil {
		return "", true, err
	}
	return plaintext, true, nil
}

// LoadGatewaySession 读取持久化的网关会话。
//
// 返回空 sessionID 表示没有可续传的会话（首次启动，或上次会话已被平台作废）。
func (r *SettingsRepo) LoadGatewaySession() (string, int64, error) {
	sessionID, _, err := r.Get(SettingGatewaySessionID)
	if err != nil || sessionID == "" {
		return "", 0, err
	}
	raw, _, err := r.Get(SettingGatewaySessionSN)
	if err != nil {
		return "", 0, err
	}
	sn, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		// 序号损坏时按 0 处理：平台会从头补发，最多重复几条事件，
		// 好过整段会话无法续传。
		return sessionID, 0, nil
	}
	return sessionID, sn, nil
}

// SaveGatewaySession 写入网关会话；sessionID 为空表示清空（会话已失效）。
//
// 两个键在同一事务内写入：否则中途失败会留下「新 session_id + 旧 sn」的组合，
// 续传时平台会从错误的 sn 之后补发（重放或漏事件）。
func (r *SettingsRepo) SaveGatewaySession(sessionID string, sn int64) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if sessionID == "" {
			for _, key := range []string{SettingGatewaySessionID, SettingGatewaySessionSN} {
				if err := tx.Where("key = ?", key).Delete(&Setting{}).Error; err != nil {
					return err
				}
			}
			return nil
		}
		for key, value := range map[string]string{
			SettingGatewaySessionID: sessionID,
			SettingGatewaySessionSN: strconv.FormatInt(sn, 10),
		} {
			if err := upsertSetting(tx, key, value); err != nil {
				return err
			}
		}
		return nil
	})
}

// OutdateHours 返回工单空闲锁定阈值（小时）。
func (r *SettingsRepo) OutdateHours() int {
	value, _, err := r.Get(SettingOutdateHours)
	if err != nil {
		return DefaultOutdateHours
	}
	return outdateHoursOrDefault(value)
}

// outdateHoursOrDefault 解析 outdate_hours 配置；缺失、非法或非正时使用默认值。
func outdateHoursOrDefault(raw string) int {
	if raw == "" {
		return DefaultOutdateHours
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return DefaultOutdateHours
	}
	return parsed
}

// ActivityAutoRestore 返回是否在重连后自动恢复动态；默认开启。
func (r *SettingsRepo) ActivityAutoRestore() bool {
	value, ok, err := r.Get(SettingActivityAutoRestore)
	if err != nil || !ok || value == "" {
		return true
	}
	return value == "true"
}

// SetActivityAutoRestore 写入自动恢复开关。
func (r *SettingsRepo) SetActivityAutoRestore(enabled bool) error {
	if enabled {
		return r.Set(SettingActivityAutoRestore, "true")
	}
	return r.Set(SettingActivityAutoRestore, "false")
}

// RuntimeConfig 是机器人运行所需的业务配置快照。
type RuntimeConfig struct {
	GuildID         string
	CategoryID      string
	LogChannelID    string
	DebugChannelID  string
	OutdateHours    int
	HasKookToken    bool
	TokenMasked     string
	InitializedAt   time.Time
	MissingRequired []string
}

// Runtime 汇总当前业务配置，并标记缺失的必填项（供 WebUI 显示引导）。
func (r *SettingsRepo) Runtime(appSecret []byte) (*RuntimeConfig, error) {
	all, err := r.All()
	if err != nil {
		return nil, err
	}
	cfg := &RuntimeConfig{
		GuildID:        all[SettingGuildID],
		CategoryID:     all[SettingCategoryID],
		LogChannelID:   all[SettingLogChannelID],
		DebugChannelID: all[SettingDebugChannelID],
		// 直接从全量配置里取值：旧实现又调用一次 OutdateHours（多查一次库）。
		OutdateHours: outdateHoursOrDefault(all[SettingOutdateHours]),
	}
	if raw, ok := all[SettingInitializedAt]; ok && raw != "" {
		if ts, err := time.Parse(time.RFC3339, raw); err == nil {
			cfg.InitializedAt = ts
		}
	}
	if encrypted, ok := all[SettingKookToken]; ok && encrypted != "" {
		cfg.HasKookToken = true
		if token, err := secure.Decrypt(appSecret, encrypted); err == nil {
			cfg.TokenMasked = secure.MaskTail(token)
		} else {
			// 解密失败通常意味着 APP_SECRET 变更，界面需要提示重新录入。
			cfg.TokenMasked = "(无法解密，请重新填写)"
		}
	}

	for key, value := range map[string]string{
		"guild_id":         cfg.GuildID,
		"category_id":      cfg.CategoryID,
		"log_channel_id":   cfg.LogChannelID,
		"debug_channel_id": cfg.DebugChannelID,
	} {
		if value == "" {
			cfg.MissingRequired = append(cfg.MissingRequired, key)
		}
	}
	if !cfg.HasKookToken {
		cfg.MissingRequired = append(cfg.MissingRequired, "kook_token")
	}
	return cfg, nil
}

// MarkInitialized 记录首次初始化时间（幂等）。
func (r *SettingsRepo) MarkInitialized() error {
	_, ok, err := r.Get(SettingInitializedAt)
	if err != nil || ok {
		return err
	}
	return r.Set(SettingInitializedAt, Now().Format(time.RFC3339))
}
