package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds the full application configuration.
// Precedence: built-in defaults < YAML config file < environment variables.
// Standard env vars have no app prefix (e.g. DB_TYPE, DSN, PORT, DATA_DIR);
// legacy EMBY_LITE_* names remain accepted as fallbacks.
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Redis    RedisConfig    `yaml:"redis"`
	Storage  StorageConfig  `yaml:"storage"`
	Scanner  ScannerConfig  `yaml:"scanner"`
	Auth     AuthConfig     `yaml:"auth"`
	Log      LogConfig      `yaml:"log"`
}

type ScannerConfig struct {
	Concurrency int `yaml:"concurrency"`
}

type ServerConfig struct {
	ListenAddr        string   `yaml:"listen_addr"`
	ServerName        string   `yaml:"server_name"`
	PublicURL         string   `yaml:"public_url"`
	AllowedMediaRoots []string `yaml:"allowed_media_roots"`
	Mode              string   `yaml:"mode"` // release | debug
}

type DatabaseConfig struct {
	Type         string `yaml:"type"` // sqlite | postgres
	SQLitePath   string `yaml:"sqlite_path"`
	DSN          string `yaml:"dsn"`
	MaxOpenConns int    `yaml:"max_open_conns"`
	MaxIdleConns int    `yaml:"max_idle_conns"`
}

type RedisConfig struct {
	URL      string `yaml:"url"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	Prefix   string `yaml:"prefix"`
}

type StorageConfig struct {
	DataDir               string `yaml:"data_dir"`
	CacheDir              string `yaml:"cache_dir"`
	BackupDir             string `yaml:"backup_dir"`
	EmbyMetadataDir       string `yaml:"emby_metadata_dir"`
	EmbyMetadataWriteback bool   `yaml:"emby_metadata_writeback"`
}

type AuthConfig struct {
	TokenTTLHours       int    `yaml:"token_ttl_hours"`
	LoginRateLimitMax   int    `yaml:"login_rate_limit_max"`
	LoginRateLimitSecs  int    `yaml:"login_rate_limit_secs"`
	InitialAdminUser    string `yaml:"initial_admin_user"`
	InitialAdminPass    string `yaml:"initial_admin_pass"`
	DefaultCanDownload  bool   `yaml:"default_can_download"`
	LeaseTTLSeconds     int    `yaml:"lease_ttl_seconds"`
	ImageSecretOverride string `yaml:"image_secret"`
}

type LogConfig struct {
	Level string `yaml:"level"`
}

func Default() *Config {
	return &Config{
		Server: ServerConfig{
			ListenAddr: ":8096",
			ServerName: "Emby-Lite",
			Mode:       "release",
		},
		Database: DatabaseConfig{
			Type:         "sqlite",
			SQLitePath:   "", // resolved under DataDir if empty
			MaxOpenConns: 20,
			MaxIdleConns: 5,
		},
		Redis: RedisConfig{
			Prefix: "embylite:",
		},
		Storage: StorageConfig{
			DataDir:               "./data",
			CacheDir:              "./cache",
			BackupDir:             "./backups",
			EmbyMetadataDir:       "",
			EmbyMetadataWriteback: true,
		},
		Auth: AuthConfig{
			TokenTTLHours:      24 * 30,
			LoginRateLimitMax:  10,
			LoginRateLimitSecs: 300,
			InitialAdminUser:   "admin",
			DefaultCanDownload: false,
			LeaseTTLSeconds:    90,
		},
		Log: LogConfig{
			Level: "info",
		},
	}
}

func Load(configPath string) (*Config, error) {
	cfg := Default()

	if configPath == "" {
		if envPath := envFirst("CONFIG_FILE", "EMBY_LITE_CONFIG"); envPath != "" {
			configPath = envPath
		}
	}
	if configPath != "" {
		raw, err := os.ReadFile(configPath)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if len(raw) > 0 {
			if err := yaml.Unmarshal(raw, cfg); err != nil {
				return nil, err
			}
		}
	}

	applyEnv(cfg)
	finalize(cfg)
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if v := envFirst("LISTEN", "EMBY_LITE_LISTEN"); v != "" {
		cfg.Server.ListenAddr = v
	} else if port := envFirst("PORT"); port != "" {
		if strings.Contains(port, ":") {
			cfg.Server.ListenAddr = port
		} else {
			cfg.Server.ListenAddr = ":" + port
		}
	}
	if v := envFirst("SERVER_NAME", "EMBY_LITE_SERVER_NAME"); v != "" {
		cfg.Server.ServerName = v
	}
	if v := envFirst("PUBLIC_URL", "EMBY_LITE_PUBLIC_URL"); v != "" {
		cfg.Server.PublicURL = strings.TrimRight(v, "/")
	}
	if v := envFirst("MEDIA_ROOTS", "EMBY_LITE_MEDIA_ROOTS"); v != "" {
		parts := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' })
		var roots []string
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				roots = append(roots, t)
			}
		}
		cfg.Server.AllowedMediaRoots = roots
	}
	if v := envFirst("MODE", "EMBY_LITE_MODE"); v != "" {
		cfg.Server.Mode = v
	}

	explicitDBType := envFirst("DB_TYPE", "EMBY_LITE_DB_TYPE")
	if explicitDBType != "" {
		cfg.Database.Type = strings.ToLower(explicitDBType)
	}
	if v := envFirst("SQLITE_PATH", "EMBY_LITE_SQLITE_PATH"); v != "" {
		cfg.Database.SQLitePath = v
	}
	if dsn := envFirst("DSN", "DATABASE_DSN", "EMBY_LITE_DATABASE_DSN"); dsn != "" {
		if strings.EqualFold(explicitDBType, "sqlite") {
			if cfg.Database.SQLitePath == "" {
				cfg.Database.SQLitePath = dsn
			}
		} else if explicitDBType == "" {
			if looksLikePostgresDSN(dsn) {
				cfg.Database.Type = "postgres"
				cfg.Database.DSN = dsn
			} else if cfg.Database.SQLitePath == "" {
				cfg.Database.Type = "sqlite"
				cfg.Database.SQLitePath = dsn
			}
		} else {
			cfg.Database.DSN = dsn
		}
	}
	if v, ok := envIntFirst("DB_MAX_CONNS", "EMBY_LITE_DB_MAX_CONNS"); ok && v > 0 {
		cfg.Database.MaxOpenConns = v
	}

	if v := envFirst("REDIS_URL", "EMBY_LITE_REDIS_URL"); v != "" {
		cfg.Redis.URL = v
	}
	if v := envFirst("REDIS_PASSWORD", "EMBY_LITE_REDIS_PASSWORD"); v != "" {
		cfg.Redis.Password = v
	}
	if v, ok := envIntFirst("REDIS_DB", "EMBY_LITE_REDIS_DB"); ok && v >= 0 {
		cfg.Redis.DB = v
	}
	if v := envFirst("REDIS_PREFIX", "EMBY_LITE_REDIS_PREFIX"); v != "" {
		cfg.Redis.Prefix = v
	}

	if v := envFirst("DATA_DIR", "CONFIG_DIR", "EMBY_LITE_DATA_DIR"); v != "" {
		cfg.Storage.DataDir = v
	}
	if v := envFirst("CACHE_DIR", "EMBY_LITE_CACHE_DIR"); v != "" {
		cfg.Storage.CacheDir = v
	}
	if v := envFirst("BACKUP_DIR", "EMBY_LITE_BACKUP_DIR"); v != "" {
		cfg.Storage.BackupDir = v
	}
	if v := envFirst("EMBY_METADATA_DIR", "EMBY_LITE_EMBY_METADATA_DIR"); v != "" {
		cfg.Storage.EmbyMetadataDir = v
	}
	if v := envFirst("EMBY_METADATA_WRITEBACK", "EMBY_LITE_EMBY_METADATA_WRITEBACK"); v != "" {
		cfg.Storage.EmbyMetadataWriteback = strings.EqualFold(v, "true") || v == "1"
	}
	if v, ok := envIntFirst("SCAN_CONCURRENCY", "EMBY_LITE_SCAN_CONCURRENCY"); ok && v > 0 {
		cfg.Scanner.Concurrency = v
	}

	if v := envFirst("ADMIN_USER", "EMBY_LITE_ADMIN_USER"); v != "" {
		cfg.Auth.InitialAdminUser = v
	}
	if v := envFirst("ADMIN_PASSWORD", "EMBY_LITE_ADMIN_PASSWORD"); v != "" {
		cfg.Auth.InitialAdminPass = v
	}
	if v, ok := envIntFirst("TOKEN_TTL_HOURS", "EMBY_LITE_TOKEN_TTL_HOURS"); ok && v > 0 {
		cfg.Auth.TokenTTLHours = v
	}
	if v := envFirst("LOG_LEVEL", "EMBY_LITE_LOG_LEVEL"); v != "" {
		cfg.Log.Level = strings.ToLower(v)
	}
}

func finalize(cfg *Config) {
	if cfg.Database.Type == "" {
		if cfg.Database.DSN != "" {
			cfg.Database.Type = "postgres"
		} else {
			cfg.Database.Type = "sqlite"
		}
	}
	if cfg.Database.SQLitePath == "" {
		cfg.Database.SQLitePath = filepath.Join(cfg.Storage.DataDir, "emby-lite.db")
	} else if !filepath.IsAbs(cfg.Database.SQLitePath) &&
		!strings.HasPrefix(cfg.Database.SQLitePath, ".") &&
		!strings.ContainsAny(cfg.Database.SQLitePath, `/\`) {
		// Relative filename like "emby-lite.db" resolves under DataDir (matches drive-tools convention).
		cfg.Database.SQLitePath = filepath.Join(cfg.Storage.DataDir, cfg.Database.SQLitePath)
	}
	if cfg.Redis.Prefix == "" {
		cfg.Redis.Prefix = "embylite:"
	}
	if cfg.Auth.TokenTTLHours <= 0 {
		cfg.Auth.TokenTTLHours = 24 * 30
	}
	if cfg.Auth.LeaseTTLSeconds <= 0 {
		cfg.Auth.LeaseTTLSeconds = 90
	}
}

func looksLikePostgresDSN(dsn string) bool {
	lower := strings.ToLower(strings.TrimSpace(dsn))
	return strings.HasPrefix(lower, "postgres://") ||
		strings.HasPrefix(lower, "postgresql://") ||
		strings.Contains(lower, "host=")
}

func envFirst(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func envIntFirst(keys ...string) (int, bool) {
	for _, k := range keys {
		raw := strings.TrimSpace(os.Getenv(k))
		if raw == "" {
			continue
		}
		v, err := strconv.Atoi(raw)
		if err == nil {
			return v, true
		}
	}
	return 0, false
}
