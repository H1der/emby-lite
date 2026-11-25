package config

import (
	"path/filepath"
	"testing"
)

func TestLoadUnprefixedEnvVars(t *testing.T) {
	t.Setenv("PORT", "9096")
	t.Setenv("SERVER_NAME", "MyEmby")
	t.Setenv("DATA_DIR", "/data")
	t.Setenv("CACHE_DIR", "/cache")
	t.Setenv("BACKUP_DIR", "/backups")
	t.Setenv("DB_TYPE", "postgres")
	t.Setenv("DSN", "postgres://u:p@pg:5432/emby?sslmode=disable")
	t.Setenv("REDIS_URL", "redis://redis:6379/0")
	t.Setenv("EMBY_METADATA_DIR", "/emby-metadata")
	t.Setenv("EMBY_METADATA_WRITEBACK", "false")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Server.ListenAddr != ":9096" {
		t.Fatalf("expected :9096, got %q", cfg.Server.ListenAddr)
	}
	if cfg.Server.ServerName != "MyEmby" {
		t.Fatalf("expected MyEmby, got %q", cfg.Server.ServerName)
	}
	if cfg.Database.Type != "postgres" || cfg.Database.DSN != "postgres://u:p@pg:5432/emby?sslmode=disable" {
		t.Fatalf("unexpected db config: %+v", cfg.Database)
	}
	if cfg.Redis.URL != "redis://redis:6379/0" {
		t.Fatalf("unexpected redis url: %q", cfg.Redis.URL)
	}
	if cfg.Storage.EmbyMetadataDir != "/emby-metadata" || cfg.Storage.EmbyMetadataWriteback {
		t.Fatalf("unexpected metadata config: %+v", cfg.Storage)
	}
}

func TestLoadSQLiteRelativeDSN(t *testing.T) {
	t.Setenv("DATA_DIR", "/data")
	t.Setenv("DB_TYPE", "sqlite")
	t.Setenv("DSN", "custom.db")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	want := filepath.Join("/data", "custom.db")
	if cfg.Database.Type != "sqlite" || cfg.Database.SQLitePath != want {
		t.Fatalf("expected sqlite %q, got type=%q path=%q", want, cfg.Database.Type, cfg.Database.SQLitePath)
	}
}
