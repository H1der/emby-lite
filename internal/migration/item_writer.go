package migration

import (
	"emby-lite/internal/model"
	"gorm.io/gorm"
)

// existingScanKeys loads the scan keys that are already present so the import loop can tell
// an update apart from an insert without one lookup per item.
func (s *Service) existingScanKeys() (map[string]string, error) {
	ids := map[string]string{}
	var rows []struct {
		ID      string
		ScanKey string
	}
	if err := s.db.Gorm.Model(&model.Item{}).Select("id, scan_key").Find(&rows).Error; err != nil {
		return ids, err
	}
	for _, row := range rows {
		if row.ScanKey != "" {
			ids[row.ScanKey] = row.ID
		}
	}
	return ids, nil
}

// itemWriter is the append-only write path used by the migration: rows are buffered and
// committed a batch at a time instead of one transaction per statement.
type itemWriter struct {
	batch    *itemBatch
	reporter func(failed int)
}

func newItemWriter(db *gorm.DB, hasFTS bool, report func(failed int)) *itemWriter {
	return &itemWriter{batch: newItemBatch(db, hasFTS), reporter: report}
}

func (w *itemWriter) add(row itemBatchRow) {
	w.batch.add(row)
	if w.batch.len() >= batchSize {
		w.flush()
	}
}

func (w *itemWriter) flush() {
	failed := w.batch.flush()
	if failed > 0 && w.reporter != nil {
		w.reporter(failed)
	}
}
