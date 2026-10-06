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
	db   *gorm.DB
	path string

	Settings *SettingsRepo
	Users    *UsersRepo
	Sessions *SessionsRepo
	Codes    *AuthCodesRepo
	Tickets  *TicketsRepo
	Panels   *PanelsRepo
	Types    *TypesRepo
	Roles    *RolesRepo
	Emoji    *EmojiRepo
	Audit    *AuditRepo
	Activity *ActivityRepo
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

	// _txlock=immediate：让每个写事务在 BEGIN 时就拿到写锁。
	// WAL 下默认的 deferred 事务会在「先读后写」时因期间已有其它连接提交而直接报
	// SQLITE_BUSY_SNAPSHOT（database is locked 517），busy_timeout 对它是无效的。
	// 机器人现在会并发处理多个工单流程（开单权限下发 / 消息归档 / 关闭），这个参数
	// 让写入竞争回到可重试的 busy 等待上。
	db, err := openGorm(path, true)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	s := &Store{db: db, path: path}
	s.Settings = &SettingsRepo{db: db}
	s.Users = &UsersRepo{db: db}
	s.Sessions = &SessionsRepo{db: db}
	s.Codes = &AuthCodesRepo{db: db}
	s.Tickets = &TicketsRepo{db: db}
	s.Panels = &PanelsRepo{db: db}
	s.Types = &TypesRepo{db: db}
	s.Roles = &RolesRepo{db: db}
	s.Emoji = &EmojiRepo{db: db}
	s.Audit = &AuditRepo{db: db}
	s.Activity = &ActivityRepo{db: db}

	hardenSQLiteFiles(path)
	return s, nil
}

// sqliteDSN 拼装 SQLite 连接串。
//
// _txlock=immediate：让每个写事务在 BEGIN 时就拿到写锁。
// WAL 下默认的 deferred 事务会在「先读后写」时因期间已有其它连接提交而直接报
// SQLITE_BUSY_SNAPSHOT（database is locked 517），busy_timeout 对它是无效的。
// 机器人现在会并发处理多个工单流程（开单权限下发 / 消息归档 / 关闭），这个参数
// 让写入竞争回到可重试的 busy 等待上。
//
// foreignKeys 控制 PRAGMA foreign_keys：业务连接必须打开；
// 迁移连接需要关闭，原因见 Migrate 的注释。
func sqliteDSN(path string, foreignKeys bool) string {
	flag := "0"
	if foreignKeys {
		flag = "1"
	}
	return fmt.Sprintf(
		"%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(%s)&_pragma=synchronous(NORMAL)&_txlock=immediate",
		path, flag)
}

// openGorm 按统一配置打开 GORM 句柄。
//
// maxOpenConns 为 0 时使用默认值 4（WAL 允许并发读 + 单写，足以支撑单机部署）。
func openGorm(path string, foreignKeys bool, maxOpenConns ...int) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(sqliteDSN(path, foreignKeys)), &gorm.Config{
		// 把 SQLite 的唯一约束错误翻译成 gorm.ErrDuplicatedKey，编号冲突重试依赖它。
		TranslateError: true,
		Logger: gormlogger.New(gormSlogWriter{log: slog.Default()}, gormlogger.Config{
			SlowThreshold: 500 * time.Millisecond,
			// 只上报真正的异常与慢查询；仓储层用 ErrNotFound 表达“未找到”，不算异常。
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
			ParameterizedQueries:      true,
		}),
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取数据库句柄失败: %w", err)
	}
	connections := 4
	if len(maxOpenConns) > 0 && maxOpenConns[0] > 0 {
		connections = maxOpenConns[0]
	}
	sqlDB.SetMaxOpenConns(connections)
	sqlDB.SetMaxIdleConns(connections)
	sqlDB.SetConnMaxLifetime(0)
	return db, nil
}

// DB 暴露底层句柄，供少数需要复杂查询的场景使用。
func (s *Store) DB() *gorm.DB { return s.db }

// Migrate 执行自动迁移。
//
// 为什么建表要用「关闭外键」的独立连接：
// SQLite 不支持直接修改列定义或约束，GORM 在需要变更列（例如新增外键、去掉默认值）
// 时会整表重建：CREATE `t__temp` → 拷贝数据 → DROP TABLE `t` → RENAME。
// 而 DROP 父表在 foreign_keys=ON 时会被子表的外键引用挡住：旧版本的 panel_roles
// 就带着 FOREIGN KEY (panel_id) REFERENCES panels(id)，升级时 AutoMigrate
// 会以 “violates foreign key constraint” 直接启动失败（现场实测）。
//
// PRAGMA foreign_keys 是每连接设置，且不能在事务内切换，用主连接（连接池）执行并不可靠，
// 因此这里单独开一个 foreign_keys=0 的句柄只做建表/重建；业务数据迁移仍在主连接上执行，
// 结束后立即用主连接跑 foreign_key_check 自检。
func (s *Store) Migrate() error {
	migrator, err := openGorm(s.path, false, 1)
	if err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}
	defer func() {
		if sqlDB, dbErr := migrator.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	}()

	if err := migrator.AutoMigrate(AllModels()...); err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}
	if err := migratePanelChannelIndex(migrator); err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}
	if err := s.migrateTicketTypes(); err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}
	s.reportForeignKeyViolations()
	hardenSQLiteFiles(s.path)
	return nil
}

// reportForeignKeyViolations 迁移后自检外键一致性。
//
// 只告警不阻断：旧版本可能已经写入了与工单对不上的面板角色等历史脏数据，
// 此时拒绝启动会让用户无法进入 WebUI 修复。
func (s *Store) reportForeignKeyViolations() {
	rows, err := s.db.Raw("PRAGMA foreign_key_check").Rows()
	if err != nil {
		slog.Warn("外键自检执行失败", "err", err)
		return
	}
	defer func() { _ = rows.Close() }()

	count := 0
	details := make([]string, 0, 4)
	for rows.Next() {
		var (
			table  string
			rowID  int64
			parent string
			fkID   int64
		)
		if scanErr := rows.Scan(&table, &rowID, &parent, &fkID); scanErr != nil {
			slog.Warn("外键自检结果解析失败", "err", scanErr)
			return
		}
		count++
		if len(details) < 5 {
			details = append(details, fmt.Sprintf("%s.rowid=%d → %s", table, rowID, parent))
		}
	}
	if count > 0 {
		slog.Warn("迁移后发现外键不一致（多为旧版本历史脏数据），建议检查", "count", count, "detail", details)
	}
}

// migrateTicketTypes 把旧版「面板 + 面板级角色」升级为「工单类型 + 类型级角色」。
//
// 策略（行为兼容优先）：为每个尚未归属类型的面板建一个同名类型，
// 并把该面板的角色复制为类型角色——升级前后“谁能处理这些工单”完全一致。
// 管理员随后可以在 WebUI 里把面板挂到同一个类型下（多个面板共享一套角色）。
//
// 历史工单按 panel_id（已删除面板则退化为 source_channel_id 中最早的面板）
// 回填 type_id / type_name 快照；角色表内容复制完成后删除，避免留下死数据。
//
// 幂等：已有类型的面板不会再处理；panel_roles 表不存在时跳过复制。
func (s *Store) migrateTicketTypes() error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var panels []Panel
		if err := tx.Where("type_id IS NULL OR type_id = 0").Order("id ASC").Find(&panels).Error; err != nil {
			return err
		}

		hasPanelRoles, err := tableExists(tx, "panel_roles")
		if err != nil {
			return err
		}

		if len(panels) > 0 {
			// 已有类型名参与去重，避免生成出重名类型（name 上有唯一索引）。
			usedNames := map[string]struct{}{}
			var existingNames []string
			if err := tx.Model(&TicketType{}).Pluck("name", &existingNames).Error; err != nil {
				return err
			}
			for _, name := range existingNames {
				usedNames[name] = struct{}{}
			}

			created := 0
			for _, panel := range panels {
				name := uniqueTypeName(panel, usedNames)
				typeRow := &TicketType{Name: name, Enabled: true, CreatedAt: Now(), UpdatedAt: Now()}
				if err := tx.Create(typeRow).Error; err != nil {
					return err
				}

				if hasPanelRoles {
					if err := copyPanelRoles(tx, panel.ID, typeRow.ID); err != nil {
						return err
					}
				}

				if err := tx.Model(&Panel{}).Where("id = ?", panel.ID).
					Update("type_id", typeRow.ID).Error; err != nil {
					return err
				}
				// 该面板开出的历史工单回填类型快照。
				if err := tx.Model(&Ticket{}).
					Where("panel_id = ? AND (type_id IS NULL OR type_id = 0)", panel.ID).
					Updates(map[string]any{"type_id": typeRow.ID, "type_name": name}).Error; err != nil {
					return err
				}
				created++
			}
			slog.Info("已为存量面板创建工单类型", "panels", created)
		}

		// 面板已被删除的历史工单：退化为按来源频道匹配该频道最早的面板所属类型。
		if err := tx.Exec(`
			UPDATE tickets SET
				type_id = (SELECT p.type_id FROM panels p WHERE p.channel_id = tickets.source_channel_id ORDER BY p.id ASC LIMIT 1),
				type_name = (SELECT t.name FROM panels p JOIN ticket_types t ON t.id = p.type_id
					WHERE p.channel_id = tickets.source_channel_id ORDER BY p.id ASC LIMIT 1)
			WHERE (type_id IS NULL OR type_id = 0) AND source_channel_id <> ''`).Error; err != nil {
			return err
		}

		if hasPanelRoles {
			if err := tx.Exec("DROP TABLE IF EXISTS panel_roles").Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// uniqueTypeName 为迁移生成的类型挑选一个不与现有类型冲突的名称。
func uniqueTypeName(panel Panel, used map[string]struct{}) string {
	base := strings.TrimSpace(panel.ChannelName)
	if base == "" {
		base = fmt.Sprintf("工单面板 #%d", panel.ID)
	}
	base = truncateRunes(base, 64)

	name := base
	if _, taken := used[name]; taken {
		name = truncateRunes(base, 48) + fmt.Sprintf(" #%d", panel.ID)
	}
	for index := 2; ; index++ {
		if _, taken := used[name]; !taken {
			break
		}
		name = truncateRunes(base, 48) + fmt.Sprintf(" #%d-%d", panel.ID, index)
	}
	used[name] = struct{}{}
	return name
}

// copyPanelRoles 把某个面板的角色复制成类型角色（同一角色只保留一条）。
func copyPanelRoles(tx *gorm.DB, panelID, typeID uint) error {
	type panelRoleRow struct {
		RoleID   string
		RoleName string
	}
	var rows []panelRoleRow
	if err := tx.Table("panel_roles").Where("panel_id = ?", panelID).Order("id ASC").Find(&rows).Error; err != nil {
		return err
	}
	seen := map[string]struct{}{}
	for _, row := range rows {
		if _, ok := seen[row.RoleID]; ok {
			continue
		}
		seen[row.RoleID] = struct{}{}
		err := tx.Create(&TicketTypeRole{
			TypeID: typeID, RoleID: row.RoleID, RoleName: row.RoleName, CreatedAt: Now(),
		}).Error
		if err != nil {
			return err
		}
	}
	return nil
}

// tableExists 判断表是否存在（迁移兼容旧版本时需要）。
func tableExists(tx *gorm.DB, name string) (bool, error) {
	var count int64
	if err := tx.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).
		Scan(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// truncateRunes 按字符截断字符串（超出时追加省略号）。
func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

// migratePanelChannelIndex 去掉历史版本在 panels.channel_id 上的唯一索引。
//
// 早期实现限制“每个频道最多一条工单按钮卡片”，该列带唯一约束；
// 现在同一频道允许多张卡片，AutoMigrate 不会主动把唯一索引降级为普通索引，
// 因此这里显式检测并重建。
func migratePanelChannelIndex(db *gorm.DB) error {
	type indexInfo struct {
		Name   string `gorm:"column:name"`
		Unique int    `gorm:"column:unique"`
	}
	var indexes []indexInfo
	if err := db.Raw("PRAGMA index_list('panels')").Scan(&indexes).Error; err != nil {
		return err
	}
	for _, idx := range indexes {
		if idx.Name != "idx_panels_channel_id" || idx.Unique == 0 {
			continue
		}
		if err := db.Exec("DROP INDEX IF EXISTS idx_panels_channel_id").Error; err != nil {
			return err
		}
		if err := db.Exec("CREATE INDEX IF NOT EXISTS idx_panels_channel_id ON panels(channel_id)").Error; err != nil {
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
