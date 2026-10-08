package store

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

// AuditRepo 负责审计日志。
//
// 审计日志只追加：本包不提供任何删除单条记录的接口，
// 仅保留按保留期整体清理的能力（后台任务按 AUDIT_RETENTION_DAYS 调用）。
type AuditRepo struct {
	db *gorm.DB
}

// Write 追加一条审计记录。写入失败只返回错误，不 panic，避免影响主流程。
func (r *AuditRepo) Write(entry *AuditLog) error {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = Now()
	} else {
		entry.CreatedAt = entry.CreatedAt.UTC()
	}
	return r.db.Create(entry).Error
}

// AuditFilter 是审计日志查询条件。
type AuditFilter struct {
	Action   string
	Actor    string
	Target   string
	From     *time.Time
	To       *time.Time
	Page     int
	PageSize int
}

// List 分页查询审计日志。
func (r *AuditRepo) List(f AuditFilter) ([]AuditLog, int64, error) {
	q := r.db.Model(&AuditLog{})
	if f.Action != "" {
		q = q.Where("action = ?", f.Action)
	}
	if f.Actor != "" {
		q = q.Where("actor LIKE ? ESCAPE '\\'", "%"+escapeLike(f.Actor)+"%")
	}
	if f.Target != "" {
		q = q.Where("target LIKE ? ESCAPE '\\'", "%"+escapeLike(strings.TrimSpace(f.Target))+"%")
	}
	if f.From != nil {
		q = q.Where("created_at >= ?", f.From.UTC())
	}
	if f.To != nil {
		q = q.Where("created_at < ?", f.To.UTC())
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	page := f.Page
	if page < 1 {
		page = 1
	}
	size := NormalizePageSize(f.PageSize)

	var items []AuditLog
	err := q.Order("created_at DESC, id DESC").
		Offset((page - 1) * size).Limit(size).
		Find(&items).Error
	return items, total, err
}

// Count 返回审计日志总条数。
func (r *AuditRepo) Count() (int64, error) {
	var n int64
	err := r.db.Model(&AuditLog{}).Count(&n).Error
	return n, err
}

// PurgeBefore 按保留期清理历史审计日志。
//
// 由后台维护任务按 AUDIT_RETENTION_DAYS 周期调用（保留期配置为 0 时不清理）。
func (r *AuditRepo) PurgeBefore(before time.Time) (int64, error) {
	res := r.db.Where("created_at < ?", before.UTC()).Delete(&AuditLog{})
	return res.RowsAffected, res.Error
}
