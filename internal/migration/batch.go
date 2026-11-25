package migration

import (
	"strings"

	"emby-lite/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// batchSize is how many items share one transaction during a migration.
//
// Every standalone statement in SQLite is its own transaction, and each commit has to
// checkpoint the WAL and fsync it. Writing 58k items one statement at a time therefore
// spent most of its wall clock inside fsync (see docs/ARCHITECTURE.md). Grouping writes
// into transactions of a few hundred rows removes almost all of that cost while keeping
// the rollback unit small enough that a failure only discards one batch.
const batchSize = 500

// itemBatch accumulates the rows belonging to a run of items and flushes them to the
// database in one transaction.
type itemBatch struct {
	db     *gorm.DB
	hasFTS bool
	rows   []itemBatchRow
}

// itemBatchRow is one migrated item together with the ancillary rows written with it.
type itemBatchRow struct {
	item model.Item

	// existed marks an item that was already in the database. Only those can carry a stale
	// FTS entry: items_fts keeps item_id UNINDEXED, so deleting by it scans the whole index,
	// and doing that for every freshly inserted row would make a full migration quadratic.
	existed bool

	genres []model.ItemGenre
	source *model.MediaSource
	images []model.ItemImage
}

func newItemBatch(db *gorm.DB, hasFTS bool) *itemBatch {
	return &itemBatch{db: db, hasFTS: hasFTS, rows: make([]itemBatchRow, 0, batchSize)}
}

func (b *itemBatch) add(row itemBatchRow) { b.rows = append(b.rows, row) }

func (b *itemBatch) len() int { return len(b.rows) }

// flush writes every queued row in a single transaction and returns how many items could
// not be written. When the batch as a whole is rejected (one oversized value, a stray
// constraint), each row is retried on its own so a single bad item does not drop the
// hundreds of good ones around it.
func (b *itemBatch) flush() int {
	if len(b.rows) == 0 {
		return 0
	}

	rows := b.rows
	b.rows = b.rows[:0]

	if err := b.writeRows(rows); err == nil {
		return 0
	}

	failed := 0
	for i := range rows {
		if err := b.writeRows(rows[i : i+1]); err != nil {
			failed++
		}
	}
	return failed
}

func (b *itemBatch) writeRows(rows []itemBatchRow) error {
	return b.db.Transaction(func(tx *gorm.DB) error {
		if err := b.saveItems(tx, rows); err != nil {
			return err
		}
		if err := b.syncFTS(tx, rows); err != nil {
			return err
		}
		if err := b.replaceGenres(tx, rows); err != nil {
			return err
		}
		if err := b.saveSources(tx, rows); err != nil {
			return err
		}
		return b.saveImages(tx, rows)
	})
}

// saveItems upserts items by scan_key.
//
// The migration used to do First(scan_key) + Save per item: two round trips plus a wasted
// UPDATE for every new row. One INSERT ... ON CONFLICT(scan_key) DO UPDATE covers both
// cases in a single statement, and the primary key stays stable because it is derived from
// scan_key (scanner.StableID).
func (b *itemBatch) saveItems(tx *gorm.DB, rows []itemBatchRow) error {
	// Chunked because SQLite binds at most 32766 variables per statement (~60 columns).
	const chunk = 50
	for start := 0; start < len(rows); start += chunk {
		end := start + chunk
		if end > len(rows) {
			end = len(rows)
		}
		items := make([]model.Item, 0, end-start)
		for _, row := range rows[start:end] {
			items = append(items, row.item)
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "scan_key"}},
			UpdateAll: true,
		}).Create(&items).Error; err != nil {
			return err
		}
	}
	return nil
}

// syncFTS refreshes items_fts for the batch: one index scan deletes the replaced rows and a
// single multi-row insert adds all of them back.
func (b *itemBatch) syncFTS(tx *gorm.DB, rows []itemBatchRow) error {
	if !b.hasFTS {
		return nil
	}

	var replaced []string
	for _, row := range rows {
		if row.existed {
			replaced = append(replaced, row.item.ID)
		}
	}
	if len(replaced) > 0 {
		if err := tx.Exec("DELETE FROM items_fts WHERE item_id IN ?", replaced).Error; err != nil {
			return err
		}
	}

	values, args := ftsValues(rows)
	return tx.Exec(values, args...).Error
}

// replaceGenres rewrites the genre links of the batch: one DELETE ... IN plus one batched
// insert instead of a delete and an insert per item and genre.
func (b *itemBatch) replaceGenres(tx *gorm.DB, rows []itemBatchRow) error {
	itemIDs := make([]string, 0, len(rows))
	genres := make([]model.ItemGenre, 0, len(rows))
	for _, row := range rows {
		itemIDs = append(itemIDs, row.item.ID)
		genres = append(genres, row.genres...)
	}
	if len(genres) == 0 {
		return nil
	}
	if err := tx.Where("item_id IN ?", itemIDs).Delete(&model.ItemGenre{}).Error; err != nil {
		return err
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(genres, batchSize).Error
}

func (b *itemBatch) saveSources(tx *gorm.DB, rows []itemBatchRow) error {
	sources := make([]model.MediaSource, 0, len(rows))
	for _, row := range rows {
		if row.source != nil {
			sources = append(sources, *row.source)
		}
	}
	if len(sources) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		UpdateAll: true,
	}).CreateInBatches(sources, batchSize).Error
}

func (b *itemBatch) saveImages(tx *gorm.DB, rows []itemBatchRow) error {
	images := make([]model.ItemImage, 0, len(rows))
	for _, row := range rows {
		images = append(images, row.images...)
	}
	if len(images) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		UpdateAll: true,
	}).CreateInBatches(images, batchSize).Error
}

// ftsValues renders a single multi-row FTS5 insert for the given rows.
func ftsValues(rows []itemBatchRow) (sql string, args []any) {
	var sb strings.Builder
	sb.WriteString("INSERT INTO items_fts(item_id, name, original_name, pinyin_initials) VALUES ")
	args = make([]any, 0, len(rows)*4)
	for i, row := range rows {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?,?,?,?)")
		args = append(args, row.item.ID, row.item.Name, row.item.OriginalName, row.item.PinyinInitials)
	}
	return sb.String(), args
}
