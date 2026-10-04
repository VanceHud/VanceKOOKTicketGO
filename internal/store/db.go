// Package store 封装 SQLite 持久层：连接、迁移、各类仓储。
//
// 安全约定：
//   - 数据目录以 0700、数据库文件以 0600 权限创建，避免同机其它用户读取聊天记录。
//   - 敏感配置（KOOK token）以 AES-GCM 密文保存，密钥来自环境变量或 0600 密钥文件。
//   - 所有时间戳统一以 UTC 存库，展示时由前端按浏览器时区转换。
package store

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ErrNotFound 表示目标记录不存在，由仓储层统一返回，便于 handler 映射为 404。
var ErrNotFound = errors.New("记录不存在")

const (
	dirMode  = 0o700
	fileMode = 0o600
)

// Store 聚合数据库连接与各仓储。
type Store struct {
	db *gorm.DB

	Settings *SettingsRepo
	Users    *UsersRepo
	Sessions *SessionsRepo
	Codes    *AuthCodesRepo
	Tickets  *TicketsRepo
	Panels   *PanelsRepo
	Roles    *RolesRepo
	Emoji    *EmojiRepo
	Audit    *AuditRepo
}

// Now 返回统一的 UTC 时间，避免各处混用本地时间。
func Now() time.Time { return time.Now().UTC() }

// Open 打开（必要时创建）SQLite 数据库并启用 WAL、外键与忙等待。
func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, dirMode); err != nil {
			return nil, fmt.Errorf("创建数据目录 %s 失败: %w", dir, err)
		}
		// 目录已存在时也收紧权限，避免历史遗留的宽松权限。
		if err := os.Chmod(dir, dirMode); err != nil {
			slog.Warn("收紧数据目录权限失败", "dir", dir, "err", err)
		}
	}

	dsn := fmt.Sprintf("%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		// 把 SQLite 的唯一约束错误翻译成 gorm.ErrDuplicatedKey，编号冲突重试依赖它。
		TranslateError: true,
		Logger: gormlogger.New(gormSlogWriter{log: slog.Default()}, gormlogger.Config{
			SlowThreshold: 500 * time.Millisecond,
			// 只上报真正的异常与慢查询；仓储层用 ErrNotFound 表达“未找到”，不算异常。
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		}),
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取数据库句柄失败: %w", err)
	}
	// WAL 允许并发读 + 单写，配合 busy_timeout 足以支撑单机部署规模。
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	sqlDB.SetConnMaxLifetime(0)

	s := &Store{db: db}
	s.Settings = &SettingsRepo{db: db}
	s.Users = &UsersRepo{db: db}
	s.Sessions = &SessionsRepo{db: db}
	s.Codes = &AuthCodesRepo{db: db}
	s.Tickets = &TicketsRepo{db: db}
	s.Panels = &PanelsRepo{db: db}
	s.Roles = &RolesRepo{db: db}
	s.Emoji = &EmojiRepo{db: db}
	s.Audit = &AuditRepo{db: db}

	hardenSQLiteFiles(path)
	return s, nil
}

// DB 暴露底层句柄，供少数需要复杂查询的场景使用。
func (s *Store) DB() *gorm.DB { return s.db }

// Migrate 执行自动迁移。
func (s *Store) Migrate() error {
	if err := s.db.AutoMigrate(AllModels()...); err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}
	if err := s.migratePanelChannelIndex(); err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}
	hardenSQLiteFiles(s.db.Name())
	return nil
}

// migratePanelChannelIndex 去掉历史版本在 panels.channel_id 上的唯一索引。
//
// 早期实现限制“每个频道最多一条工单按钮卡片”，该列带唯一约束；
// 现在同一频道允许多张卡片，AutoMigrate 不会主动把唯一索引降级为普通索引，
// 因此这里显式检测并重建。
func (s *Store) migratePanelChannelIndex() error {
	type indexInfo struct {
		Name   string `gorm:"column:name"`
		Unique int    `gorm:"column:unique"`
	}
	var indexes []indexInfo
	if err := s.db.Raw("PRAGMA index_list('panels')").Scan(&indexes).Error; err != nil {
		return err
	}
	for _, idx := range indexes {
		if idx.Name != "idx_panels_channel_id" || idx.Unique == 0 {
			continue
		}
		if err := s.db.Exec("DROP INDEX IF EXISTS idx_panels_channel_id").Error; err != nil {
			return err
		}
		if err := s.db.Exec("CREATE INDEX IF NOT EXISTS idx_panels_channel_id ON panels(channel_id)").Error; err != nil {
			return err
		}
		return nil
	}
	return nil
}

// QuickCheck 运行 PRAGMA quick_check，发现损坏时返回错误信息。
func (s *Store) QuickCheck() (string, error) {
	var result string
	if err := s.db.Raw("PRAGMA quick_check").Scan(&result).Error; err != nil {
		return "", err
	}
	if !strings.EqualFold(result, "ok") {
		return result, fmt.Errorf("数据库自检未通过: %s", result)
	}
	return result, nil
}

// Close 关闭数据库连接。
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// hardenSQLiteFiles 把数据库主文件及 WAL/SHM 附属文件权限收紧到 0600。
// 这些文件可能尚未创建，因此忽略不存在错误。
func hardenSQLiteFiles(path string) {
	if path == "" {
		return
	}
	for _, p := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := os.Chmod(p, fileMode); err != nil {
			slog.Warn("收紧数据库文件权限失败", "path", p, "err", err)
		}
	}
}

// gormSlogWriter 把 GORM 的日志交给 slog 统一输出。
//
// 直接使用 GORM 默认 logger 会把带 ANSI 颜色与本机文件路径的文本打到 stdout，
// 既污染日志格式，也把源码路径暴露给日志阅读者。
type gormSlogWriter struct {
	log *slog.Logger
}

// Printf 实现 gormlogger.Writer。
func (w gormSlogWriter) Printf(format string, args ...any) {
	message := fmt.Sprintf(strings.TrimSuffix(format, "\n"), args...)
	w.log.Warn("数据库告警", "detail", message)
}

// mapNotFound 把 GORM 的未找到错误折叠成本包的 ErrNotFound。
func mapNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}
