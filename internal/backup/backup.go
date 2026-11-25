package backup

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"emby-lite/internal/database"
	"emby-lite/internal/model"

	"gorm.io/gorm"
)

const portableManifestVersion = 1

// ArchiveInfo describes a stored backup archive on disk.
type ArchiveInfo struct {
	Filename  string    `json:"filename"`
	Format    string    `json:"format"` // portable-zip | sqlite-db
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

// PortableManifest is the cross-database (SQLite <-> PostgreSQL) backup payload.
type PortableManifest struct {
	Version          int                     `json:"version"`
	CreatedAt        time.Time               `json:"created_at"`
	SourceDialect    string                  `json:"source_dialect"`
	PermissionGroups []model.PermissionGroup `json:"permission_groups"`
	Users            []model.User            `json:"users"`
	GroupLibraries   []model.GroupLibrary    `json:"group_libraries"`
	UserLibraries    []model.UserLibrary     `json:"user_libraries"`
	Libraries        []model.Library         `json:"libraries"`
	VirtualLibraries []model.VirtualLibrary  `json:"virtual_libraries"`
	Items            []model.Item            `json:"items"`
	MediaSources     []model.MediaSource     `json:"media_sources"`
	MediaSubtitles   []model.MediaSubtitle   `json:"media_subtitles"`
	ItemGenres       []model.ItemGenre       `json:"item_genres"`
	ItemTags         []model.ItemTag         `json:"item_tags"`
	People           []model.Person          `json:"people"`
	ItemPeople       []model.ItemPerson      `json:"item_people"`
	ItemImages       []model.ItemImage       `json:"item_images"`
	MetadataLocks    []model.ItemMetadataLock `json:"metadata_locks"`
	UserData         []model.UserData        `json:"user_data"`
	IntroMarkers     []model.IntroMarker     `json:"intro_markers"`
	Settings         []model.Setting         `json:"settings"`
}

type Service struct {
	db        *database.DB
	backupDir string
}

func NewService(db *database.DB, backupDir string) *Service {
	_ = os.MkdirAll(backupDir, 0o755)
	return &Service{db: db, backupDir: backupDir}
}

func (s *Service) BackupDir() string { return s.backupDir }

func (s *Service) List() ([]ArchiveInfo, error) {
	entries, err := os.ReadDir(s.backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []ArchiveInfo{}, nil
		}
		return nil, err
	}
	var out []ArchiveInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		format := ""
		if strings.HasSuffix(strings.ToLower(name), ".zip") {
			format = "portable-zip"
		} else if strings.HasSuffix(strings.ToLower(name), ".db") {
			format = "sqlite-db"
		} else {
			continue
		}
		if fi, err := e.Info(); err == nil {
			out = append(out, ArchiveInfo{
				Filename:  name,
				Format:    format,
				Size:      fi.Size(),
				CreatedAt: fi.ModTime().UTC(),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// CreatePortable creates a cross-database portable ZIP snapshot of all persistent data.
func (s *Service) CreatePortable() (*ArchiveInfo, error) {
	if err := os.MkdirAll(s.backupDir, 0o755); err != nil {
		return nil, err
	}
	manifest := PortableManifest{
		Version:       portableManifestVersion,
		CreatedAt:     time.Now().UTC(),
		SourceDialect: s.db.Dialect,
	}
	_ = s.db.Gorm.Find(&manifest.PermissionGroups).Error
	_ = s.db.Gorm.Find(&manifest.Users).Error
	_ = s.db.Gorm.Find(&manifest.GroupLibraries).Error
	_ = s.db.Gorm.Find(&manifest.UserLibraries).Error
	_ = s.db.Gorm.Find(&manifest.Libraries).Error
	_ = s.db.Gorm.Find(&manifest.VirtualLibraries).Error
	_ = s.db.Gorm.Find(&manifest.Items).Error
	_ = s.db.Gorm.Find(&manifest.MediaSources).Error
	_ = s.db.Gorm.Find(&manifest.MediaSubtitles).Error
	_ = s.db.Gorm.Find(&manifest.ItemGenres).Error
	_ = s.db.Gorm.Find(&manifest.ItemTags).Error
	_ = s.db.Gorm.Find(&manifest.People).Error
	_ = s.db.Gorm.Find(&manifest.ItemPeople).Error
	_ = s.db.Gorm.Find(&manifest.ItemImages).Error
	_ = s.db.Gorm.Find(&manifest.MetadataLocks).Error
	_ = s.db.Gorm.Find(&manifest.UserData).Error
	_ = s.db.Gorm.Find(&manifest.IntroMarkers).Error
	_ = s.db.Gorm.Find(&manifest.Settings).Error

	filename := fmt.Sprintf("emby-lite-portable-%s.zip", time.Now().UTC().Format("20060102-150405"))
	fullPath := filepath.Join(s.backupDir, filename)

	f, err := os.Create(fullPath)
	if err != nil {
		return nil, err
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("manifest.json")
	if err != nil {
		_ = zw.Close()
		_ = f.Close()
		return nil, err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(&manifest); err != nil {
		_ = zw.Close()
		_ = f.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	fi, _ := os.Stat(fullPath)
	return &ArchiveInfo{
		Filename:  filename,
		Format:    "portable-zip",
		Size:      fi.Size(),
		CreatedAt: fi.ModTime().UTC(),
	}, nil
}

// CreateSQLiteNativeSnapshot uses SQLite VACUUM INTO for a consistent online snapshot.
func (s *Service) CreateSQLiteNativeSnapshot() (*ArchiveInfo, error) {
	if s.db.Dialect != "sqlite" {
		return nil, errors.New("native sqlite snapshot is only available when running on sqlite")
	}
	if err := os.MkdirAll(s.backupDir, 0o755); err != nil {
		return nil, err
	}
	filename := fmt.Sprintf("emby-lite-sqlite-%s.db", time.Now().UTC().Format("20060102-150405"))
	fullPath := filepath.Join(s.backupDir, filename)
	escaped := strings.ReplaceAll(fullPath, "'", "''")
	if err := s.db.Gorm.Exec("VACUUM INTO '" + escaped + "'").Error; err != nil {
		return nil, err
	}
	fi, _ := os.Stat(fullPath)
	return &ArchiveInfo{
		Filename:  filename,
		Format:    "sqlite-db",
		Size:      fi.Size(),
		CreatedAt: fi.ModTime().UTC(),
	}, nil
}

// ResolveArchivePath safely resolves a backup filename inside backupDir.
func (s *Service) ResolveArchivePath(filename string) (string, error) {
	base := filepath.Base(strings.TrimSpace(filename))
	if base == "" || base == "." || base == ".." || base != filename {
		return "", errors.New("invalid backup filename")
	}
	full := filepath.Join(s.backupDir, base)
	if _, err := os.Stat(full); err != nil {
		return "", err
	}
	return full, nil
}

// RestorePortable restores a portable ZIP archive into the current database (SQLite or PostgreSQL) atomically.
func (s *Service) RestorePortable(filename string) (map[string]int, error) {
	fullPath, err := s.ResolveArchivePath(filename)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(strings.ToLower(fullPath), ".zip") {
		return nil, errors.New("only portable .zip backups can be hot-restored across databases")
	}

	zr, err := zip.OpenReader(fullPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	var manifest PortableManifest
	found := false
	for _, zf := range zr.File {
		if zf.Name == "manifest.json" {
			rc, err := zf.Open()
			if err != nil {
				return nil, err
			}
			raw, readErr := io.ReadAll(io.LimitReader(rc, 256<<20))
			_ = rc.Close()
			if readErr != nil {
				return nil, readErr
			}
			if err := json.Unmarshal(raw, &manifest); err != nil {
				return nil, fmt.Errorf("invalid manifest.json: %w", err)
			}
			found = true
			break
		}
	}
	if !found || manifest.Version != portableManifestVersion {
		return nil, errors.New("unrecognized or incompatible portable backup archive")
	}

	err = s.db.Gorm.Transaction(func(tx *gorm.DB) error {
		// Upsert all entities safely in dependency order.
		for i := range manifest.PermissionGroups {
			if err := tx.Save(&manifest.PermissionGroups[i]).Error; err != nil {
				return err
			}
		}
		for i := range manifest.Users {
			if err := tx.Save(&manifest.Users[i]).Error; err != nil {
				return err
			}
		}
		for i := range manifest.Libraries {
			if err := tx.Save(&manifest.Libraries[i]).Error; err != nil {
				return err
			}
		}
		for i := range manifest.VirtualLibraries {
			if err := tx.Save(&manifest.VirtualLibraries[i]).Error; err != nil {
				return err
			}
		}
		for i := range manifest.GroupLibraries {
			_ = tx.Save(&manifest.GroupLibraries[i]).Error
		}
		for i := range manifest.UserLibraries {
			_ = tx.Save(&manifest.UserLibraries[i]).Error
		}
		for i := range manifest.Items {
			if err := tx.Save(&manifest.Items[i]).Error; err != nil {
				return err
			}
		}
		for i := range manifest.MediaSources {
			_ = tx.Save(&manifest.MediaSources[i]).Error
		}
		for i := range manifest.MediaSubtitles {
			_ = tx.Save(&manifest.MediaSubtitles[i]).Error
		}
		for i := range manifest.ItemGenres {
			_ = tx.Save(&manifest.ItemGenres[i]).Error
		}
		for i := range manifest.ItemTags {
			_ = tx.Save(&manifest.ItemTags[i]).Error
		}
		for i := range manifest.People {
			_ = tx.Save(&manifest.People[i]).Error
		}
		for i := range manifest.ItemPeople {
			_ = tx.Save(&manifest.ItemPeople[i]).Error
		}
		for i := range manifest.ItemImages {
			_ = tx.Save(&manifest.ItemImages[i]).Error
		}
		for i := range manifest.MetadataLocks {
			_ = tx.Save(&manifest.MetadataLocks[i]).Error
		}
		for i := range manifest.UserData {
			_ = tx.Save(&manifest.UserData[i]).Error
		}
		for i := range manifest.IntroMarkers {
			_ = tx.Save(&manifest.IntroMarkers[i]).Error
		}
		for i := range manifest.Settings {
			_ = tx.Save(&manifest.Settings[i]).Error
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.db.BumpLibraryRevision()
	return map[string]int{
		"users":     len(manifest.Users),
		"libraries": len(manifest.Libraries),
		"items":     len(manifest.Items),
		"user_data": len(manifest.UserData),
	}, nil
}

func (s *Service) Delete(filename string) error {
	full, err := s.ResolveArchivePath(filename)
	if err != nil {
		return err
	}
	return os.Remove(full)
}
