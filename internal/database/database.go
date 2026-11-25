package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"emby-lite/internal/config"
	"emby-lite/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type DB struct {
	Gorm     *gorm.DB
	SQL      *sql.DB
	Dialect  string // "sqlite" | "postgres"
	HasTrgm  bool
	HasFTS5  bool
}

func Open(cfg *config.Config) (*DB, error) {
	gormCfg := &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	}

	var gdb *gorm.DB
	var err error
	dialect := strings.ToLower(cfg.Database.Type)
	if dialect == "" {
		dialect = "sqlite"
	}

	switch dialect {
	case "sqlite":
		dir := filepath.Dir(cfg.Database.SQLitePath)
		if dir != "" && dir != "." && cfg.Database.SQLitePath != ":memory:" && !strings.HasPrefix(cfg.Database.SQLitePath, "file:") {
			if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
				return nil, fmt.Errorf("create sqlite directory: %w", mkErr)
			}
		}
		dsn := cfg.Database.SQLitePath
		if dsn != ":memory:" && !strings.Contains(dsn, "?") {
			dsn += "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
		}
		gdb, err = gorm.Open(sqlite.Open(dsn), gormCfg)
		if err != nil {
			return nil, fmt.Errorf("open sqlite: %w", err)
		}
	case "postgres", "postgresql":
		dialect = "postgres"
		if cfg.Database.DSN == "" {
			return nil, fmt.Errorf("postgres database selected but DSN is empty")
		}
		gdb, err = gorm.Open(postgres.Open(cfg.Database.DSN), gormCfg)
		if err != nil {
			return nil, fmt.Errorf("open postgres: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported database type: %s", dialect)
	}

	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("unwrap sql.DB: %w", err)
	}

	if dialect == "sqlite" {
		// Single writer connection prevents SQLITE_BUSY under concurrent scans/requests.
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
	} else {
		sqlDB.SetMaxOpenConns(cfg.Database.MaxOpenConns)
		sqlDB.SetMaxIdleConns(cfg.Database.MaxIdleConns)
		sqlDB.SetConnMaxLifetime(30 * time.Minute)
	}

	db := &DB{
		Gorm:    gdb,
		SQL:     sqlDB,
		Dialect: dialect,
	}

	if err := db.Migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := db.SeedDefaults(cfg); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	return db, nil
}

func (d *DB) Migrate() error {
	if err := d.Gorm.AutoMigrate(model.AllModels()...); err != nil {
		return fmt.Errorf("auto migrate: %w", err)
	}

	// Dialect-specific search acceleration (best-effort, degrades cleanly).
	if d.Dialect == "postgres" {
		if err := d.Gorm.Exec("CREATE EXTENSION IF NOT EXISTS pg_trgm").Error; err == nil {
			d.HasTrgm = true
			_ = d.Gorm.Exec("CREATE INDEX IF NOT EXISTS idx_items_name_trgm ON items USING gin (name gin_trgm_ops)").Error
			_ = d.Gorm.Exec("CREATE INDEX IF NOT EXISTS idx_items_orig_name_trgm ON items USING gin (original_name gin_trgm_ops)").Error
		} else {
			slog.Warn("pg_trgm extension unavailable, falling back to ILIKE search", "err", err)
		}
	} else if d.Dialect == "sqlite" {
		if err := d.Gorm.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS items_fts USING fts5(item_id UNINDEXED, name, original_name, pinyin_initials)`).Error; err == nil {
			d.HasFTS5 = true
		} else {
			slog.Warn("sqlite FTS5 unavailable, falling back to LIKE search", "err", err)
		}
	}

	return nil
}

func (d *DB) SeedDefaults(cfg *config.Config) error {
	if err := d.ensureSetting("server_id", randomHex(16)); err != nil {
		return err
	}
	if err := d.ensureSetting("server_name", cfg.Server.ServerName); err != nil {
		return err
	}
	secret := cfg.Auth.ImageSecretOverride
	if secret == "" {
		secret = randomHex(32)
	}
	if err := d.ensureSetting("image_secret", secret); err != nil {
		return err
	}
	if err := d.ensureSetting("library_revision", "1"); err != nil {
		return err
	}

	// Seed default permission group templates if none exist.
	var count int64
	if err := d.Gorm.Model(&model.PermissionGroup{}).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		defaults := []model.PermissionGroup{
			{
				ID:                   "group-standard",
				Name:                 "标准用户",
				Description:          "允许直连播放所有非隐藏媒体库，默认禁止下载",
				AllowPlayback:        true,
				AllowDownload:        false,
				MaxDevices:           5,
				MaxConcurrentStreams: 3,
				CanViewHidden:        false,
			},
			{
				ID:                   "group-family",
				Name:                 "家庭成员",
				Description:          "允许播放与离线下载，支持多设备同时观看",
				AllowPlayback:        true,
				AllowDownload:        true,
				MaxDevices:           10,
				MaxConcurrentStreams: 5,
				CanViewHidden:        false,
			},
		}
		for _, g := range defaults {
			if err := d.Gorm.Create(&g).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *DB) ensureSetting(key, value string) error {
	var existing model.Setting
	err := d.Gorm.Where("key = ?", key).First(&existing).Error
	if err == nil {
		return nil
	}
	if err != gorm.ErrRecordNotFound {
		return err
	}
	return d.Gorm.Create(&model.Setting{
		Key:       key,
		Value:     value,
		UpdatedAt: time.Now().UTC(),
	}).Error
}

func (d *DB) GetSetting(key, fallback string) string {
	var s model.Setting
	if err := d.Gorm.Where("key = ?", key).First(&s).Error; err != nil {
		return fallback
	}
	return s.Value
}

func (d *DB) SetSetting(key, value string, encrypted bool) error {
	return d.Gorm.Save(&model.Setting{
		Key:       key,
		Value:     value,
		Encrypted: encrypted,
		UpdatedAt: time.Now().UTC(),
	}).Error
}

func (d *DB) BumpLibraryRevision() {
	now := fmt.Sprintf("%d", time.Now().UnixNano())
	_ = d.SetSetting("library_revision", now, false)
}

func (d *DB) Ping(ctx context.Context) error {
	return d.SQL.PingContext(ctx)
}

func (d *DB) Close() error {
	if d.SQL != nil {
		return d.SQL.Close()
	}
	return nil
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
