package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"emby-lite/internal/config"
	"emby-lite/internal/database"
	"emby-lite/internal/model"
)

func TestScanBatchFallbackIsolatesSingleBadItem(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "test.db")
	movieRoot := filepath.Join(tmp, "movies")

	for i := 1; i <= 10; i++ {
		code := fmt.Sprintf("CODE-%03d", i)
		mDir := filepath.Join(movieRoot, code)
		if err := os.MkdirAll(mDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(mDir, code+".strm"), []byte("https://example.com/"+code+".mp4\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		nfo := fmt.Sprintf(`<movie><title>%s 标题</title><originaltitle>%s 原标题</originaltitle><actor><name>共通演员</name></actor></movie>`, code, code)
		if err := os.WriteFile(filepath.Join(mDir, code+".nfo"), []byte(nfo), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.Default()
	cfg.Database.Type = "sqlite"
	cfg.Database.SQLitePath = dbPath
	db, err := database.Open(cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Reject a single row mid-batch to simulate a PostgreSQL row-level failure (e.g. constraint/encoding error).
	if err := db.Gorm.Exec(`
		CREATE TRIGGER reject_bad_movie
		BEFORE INSERT ON items
		WHEN NEW.name LIKE 'CODE-005%'
		BEGIN
			SELECT RAISE(ABORT, 'simulated postgres row error');
		END;
	`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	lib := model.Library{ID: "lib-batch", Name: "批次隔离测试", Kind: "movies", PathsJSON: fmt.Sprintf(`[%q]`, movieRoot)}
	if err := db.Gorm.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewService(db, nil)
	stats, err := svc.ScanLibrary(context.Background(), lib.ID)
	if err != nil {
		t.Fatalf("scan returned unexpected error: %v", err)
	}
	if stats.FilesFound != 10 {
		t.Fatalf("expected 10 files found, got %d", stats.FilesFound)
	}
	if stats.ItemsAdded != 9 {
		t.Fatalf("expected 9 items added after isolating 1 failing item, got %d", stats.ItemsAdded)
	}

	var itemCount, personCount, linkCount int64
	db.Read().Model(&model.Item{}).Where("library_id = ? AND soft_deleted_at IS NULL", lib.ID).Count(&itemCount)
	db.Read().Model(&model.Person{}).Where("name = ?", "共通演员").Count(&personCount)
	db.Read().Model(&model.ItemPerson{}).Count(&linkCount)
	if itemCount != 9 {
		t.Fatalf("expected 9 committed items in DB, got %d", itemCount)
	}
	if personCount != 1 || linkCount != 9 {
		t.Fatalf("expected shared person preserved after batch rollback fallback (person=%d, links=%d)", personCount, linkCount)
	}
}

func TestScanLongJapaneseNFOMetadata(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "test.db")
	movieRoot := filepath.Join(tmp, "movies")

	longJP := "SONE-954-" + strings.Repeat("オッサンばっかりの職場旅行に新卒社員が参加してしまいます", 15)
	longTag := strings.Repeat("日本語タグ", 40) // 200 runes > 128

	mDir := filepath.Join(movieRoot, "SONE-954")
	if err := os.MkdirAll(mDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mDir, "SONE-954.strm"), []byte("https://example.com/sone954.mp4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nfo := fmt.Sprintf(`<movie>
		<title>%s</title>
		<originaltitle>%s</originaltitle>
		<tagline>%s</tagline>
		<genre>%s</genre>
		<tag>%s</tag>
		<actor><name>川越にこ</name><role>%s</role></actor>
	</movie>`, longJP, longJP, longJP, longTag, longTag, longTag)
	if err := os.WriteFile(filepath.Join(mDir, "SONE-954.nfo"), []byte(nfo), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Database.Type = "sqlite"
	cfg.Database.SQLitePath = dbPath
	db, err := database.Open(cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	lib := model.Library{ID: "lib-jp", Name: "日文长标题", Kind: "movies", PathsJSON: fmt.Sprintf(`[%q]`, movieRoot)}
	if err := db.Gorm.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewService(db, nil)
	stats, err := svc.ScanLibrary(context.Background(), lib.ID)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if stats.ItemsAdded != 1 {
		t.Fatalf("expected 1 added item, got %+v", stats)
	}

	var item model.Item
	if err := db.Read().Where("library_id = ?", lib.ID).First(&item).Error; err != nil {
		t.Fatalf("load item: %v", err)
	}
	for label, val := range map[string]string{
		"Name":           item.Name,
		"OriginalName":   item.OriginalName,
		"SortName":       item.SortName,
		"PinyinInitials": item.PinyinInitials,
		"Tagline":        item.Tagline,
	} {
		if !utf8.ValidString(val) {
			t.Fatalf("%s is invalid UTF-8", label)
		}
	}
	if utf8.RuneCountInString(item.PinyinInitials) > 256 {
		t.Fatalf("PinyinInitials too long: %d", utf8.RuneCountInString(item.PinyinInitials))
	}
	if utf8.RuneCountInString(item.Name) > 512 {
		t.Fatalf("Name too long: %d", utf8.RuneCountInString(item.Name))
	}

	var tag model.ItemTag
	if err := db.Read().Where("item_id = ?", item.ID).First(&tag).Error; err != nil {
		t.Fatalf("load tag: %v", err)
	}
	if !utf8.ValidString(tag.Tag) || utf8.RuneCountInString(tag.Tag) > 128 {
		t.Fatalf("unexpected tag length/utf8: %d", utf8.RuneCountInString(tag.Tag))
	}
}
