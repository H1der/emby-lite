package migration

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// snapshotEmbyDatabases keeps a raw, self-contained copy of the source Emby
// databases before any native migration runs. VACUUM INTO produces a consistent
// snapshot even while Emby keeps a WAL open; a plain file copy is the fallback.
func (s *Service) snapshotEmbyDatabases(req EmbyMigrationRequest, resolved ResolvedSources) (string, []string, string) {
	root := strings.TrimSpace(req.SnapshotDir)
	if root == "" {
		return "", nil, ""
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", nil, "创建快照目录失败: " + err.Error()
	}
	destDir := filepath.Join(root, "emby-"+time.Now().UTC().Format("20060102-150405"))
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", nil, "创建快照目录失败: " + err.Error()
	}

	type source struct{ path, name string }
	sources := []source{
		{resolved.LibraryDBPath, "library.db"},
		{resolved.UsersDBPath, "users.db"},
	}
	var files []string
	var warnings []string
	for _, src := range sources {
		if strings.TrimSpace(src.path) == "" {
			continue
		}
		if _, err := os.Stat(src.path); err != nil {
			continue
		}
		dst := filepath.Join(destDir, src.name)
		if err := vacuumSQLite(src.path, dst); err != nil {
			if copyErr := copyDatabaseFile(src.path, dst); copyErr != nil {
				warnings = append(warnings, fmt.Sprintf("快照 %s 失败: %v", src.name, err))
				continue
			}
		}
		files = append(files, dst)
	}
	if len(files) == 0 {
		return destDir, nil, "未生成任何 Emby 数据库快照: " + strings.Join(warnings, "; ")
	}
	if len(warnings) > 0 {
		return destDir, files, strings.Join(warnings, "; ")
	}
	return destDir, files, ""
}

func vacuumSQLite(src, dst string) error {
	db, err := gorm.Open(sqlite.Open(src), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	_, err = sqlDB.Exec("VACUUM INTO ?", dst)
	return err
}

func copyDatabaseFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
