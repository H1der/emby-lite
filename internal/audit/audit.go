package audit

import (
	"encoding/json"
	"strings"
	"time"

	"emby-lite/internal/database"
	"emby-lite/internal/model"

	"github.com/google/uuid"
)

type Logger struct {
	db *database.DB
}

func NewLogger(db *database.DB) *Logger {
	return &Logger{db: db}
}

// Record stores an audit log entry. Callers must never pass raw passwords, tokens, or API keys in detail.
func (l *Logger) Record(userID, action, targetType, targetID, ip string, detail map[string]any) {
	if l == nil || l.db == nil {
		return
	}
	detailStr := "{}"
	if len(detail) > 0 {
		if b, err := json.Marshal(detail); err == nil {
			detailStr = string(b)
		}
	}
	_ = l.db.Gorm.Create(&model.AuditLog{
		ID:         strings.ReplaceAll(uuid.NewString(), "-", ""),
		UserID:     userID,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		DetailJSON: detailStr,
		IP:         ip,
		CreatedAt:  time.Now().UTC(),
	}).Error
}

type LogRow struct {
	model.AuditLog
	Username string `json:"username"`
}

func (l *Logger) List(limit int) ([]LogRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var rows []LogRow
	err := l.db.Gorm.Table("audit_logs").
		Select("audit_logs.*, users.username").
		Joins("LEFT JOIN users ON users.id = audit_logs.user_id").
		Order("audit_logs.created_at DESC").
		Limit(limit).
		Scan(&rows).Error
	return rows, err
}
