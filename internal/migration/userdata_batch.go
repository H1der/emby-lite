package migration

import (
	"emby-lite/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// userDataKey identifies one user/item pair, matching the composite primary key of
// model.UserData.
func userDataKey(userID, itemID string) string {
	return userID + "|" + itemID
}

// loadUserDataMap reads the playback state that already exists so the watch phase can
// merge into it without one SELECT per migrated record.
func (s *Service) loadUserDataMap(users, items []string) (map[string]model.UserData, error) {
	out := map[string]model.UserData{}
	query := s.db.Gorm.Model(&model.UserData{})
	if len(users) > 0 {
		query = query.Where("user_id IN ?", users)
	}
	var rows []model.UserData
	if err := query.Find(&rows).Error; err != nil {
		return out, err
	}
	for _, row := range rows {
		out[userDataKey(row.UserID, row.ItemID)] = row
	}
	return out, nil
}

// userDataBatch buffers watch-state rows and commits them a batch at a time, for the same
// reason itemBatch does: one transaction per row spends its time in WAL checkpoint fsync.
type userDataBatch struct {
	db   *gorm.DB
	rows []model.UserData
}

func newUserDataBatch(db *gorm.DB) *userDataBatch {
	return &userDataBatch{db: db, rows: make([]model.UserData, 0, batchSize)}
}

func (b *userDataBatch) add(row model.UserData) { b.rows = append(b.rows, row) }

func (b *userDataBatch) length() int { return len(b.rows) }

// flush upserts the buffered rows and returns how many were rejected.
func (b *userDataBatch) flush() int {
	if len(b.rows) == 0 {
		return 0
	}
	rows := b.rows
	b.rows = b.rows[:0]

	err := b.db.Transaction(func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "item_id"}},
			UpdateAll: true,
		}).CreateInBatches(rows, batchSize).Error
	})
	if err == nil {
		return 0
	}

	failed := 0
	for i := range rows {
		one := rows[i : i+1]
		if err := b.db.Transaction(func(tx *gorm.DB) error {
			return tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "user_id"}, {Name: "item_id"}},
				UpdateAll: true,
			}).Create(&one).Error
		}); err != nil {
			failed++
		}
	}
	return failed
}
