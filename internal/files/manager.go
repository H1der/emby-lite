package files

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"emby-lite/internal/config"
	"emby-lite/internal/database"
	"emby-lite/internal/model"
)

var (
	ErrOutsideBoundary = errors.New("path is outside allowed media roots")
	ErrInvalidName     = errors.New("invalid file or directory name")
)

type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

type Manager struct {
	db  *database.DB
	cfg *config.Config
}

func NewManager(db *database.DB, cfg *config.Config) *Manager {
	return &Manager{db: db, cfg: cfg}
}

// AllowedRoots collects configured AllowedMediaRoots plus all registered physical library root paths.
func (m *Manager) AllowedRoots() []string {
	seen := map[string]bool{}
	var roots []string
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		abs, err := filepath.Abs(raw)
		if err != nil {
			return
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		clean := filepath.Clean(abs)
		key := strings.ToLower(clean)
		if seen[key] {
			return
		}
		if fi, err := os.Stat(clean); err == nil && fi.IsDir() {
			seen[key] = true
			roots = append(roots, clean)
		}
	}
	for _, r := range m.cfg.Server.AllowedMediaRoots {
		add(r)
	}
	var libs []model.Library
	if err := m.db.Gorm.Select("paths_json").Find(&libs).Error; err == nil {
		for _, l := range libs {
			var lp []string
			if json.Unmarshal([]byte(l.PathsJSON), &lp) == nil {
				for _, p := range lp {
					add(p)
				}
			}
		}
	}
	sort.Strings(roots)
	return roots
}

// ResolveSafe validates that target (existing or newly created under an existing parent) lies strictly within AllowedRoots.
func (m *Manager) ResolveSafe(target string, mustExist bool) (string, error) {
	roots := m.AllowedRoots()
	if len(roots) == 0 {
		return "", ErrOutsideBoundary
	}
	abs, err := filepath.Abs(strings.TrimSpace(target))
	if err != nil {
		return "", ErrOutsideBoundary
	}
	abs = filepath.Clean(abs)

	var resolved string
	if mustExist {
		resolved, err = filepath.EvalSymlinks(abs)
		if err != nil {
			return "", err
		}
	} else {
		// Validate existing parent directory via EvalSymlinks so symlinks cannot escape on creation.
		parent := filepath.Dir(abs)
		resolvedParent, err := filepath.EvalSymlinks(parent)
		if err != nil {
			return "", err
		}
		resolved = filepath.Join(resolvedParent, filepath.Base(abs))
	}

	for _, root := range roots {
		rel, err := filepath.Rel(root, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return resolved, nil
		}
	}
	return "", ErrOutsideBoundary
}

func (m *Manager) List(rawPath string) (current, parent string, roots []string, entries []Entry, err error) {
	roots = m.AllowedRoots()
	if strings.TrimSpace(rawPath) == "" {
		entries = make([]Entry, 0, len(roots))
		for _, r := range roots {
			fi, _ := os.Stat(r)
			var mt time.Time
			if fi != nil {
				mt = fi.ModTime().UTC()
			}
			entries = append(entries, Entry{
				Name:    r,
				Path:    r,
				IsDir:   true,
				ModTime: mt,
			})
		}
		return "", "", roots, entries, nil
	}

	safeDir, err := m.ResolveSafe(rawPath, true)
	if err != nil {
		return "", "", roots, nil, err
	}
	fi, err := os.Stat(safeDir)
	if err != nil || !fi.IsDir() {
		return "", "", roots, nil, errors.New("not a directory")
	}

	dirEntries, err := os.ReadDir(safeDir)
	if err != nil {
		return "", "", roots, nil, err
	}
	entries = make([]Entry, 0, len(dirEntries))
	for _, de := range dirEntries {
		info, statErr := de.Info()
		if statErr != nil {
			continue
		}
		entries = append(entries, Entry{
			Name:    de.Name(),
			Path:    filepath.Join(safeDir, de.Name()),
			IsDir:   de.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().UTC(),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	// Determine parent only if still inside allowed roots.
	candParent := filepath.Dir(safeDir)
	if candParent != safeDir {
		if _, pErr := m.ResolveSafe(candParent, true); pErr == nil {
			parent = candParent
		}
	}
	return safeDir, parent, roots, entries, nil
}

func (m *Manager) Mkdir(parentPath, name string) (string, error) {
	if !isSafeBaseName(name) {
		return "", ErrInvalidName
	}
	safeParent, err := m.ResolveSafe(parentPath, true)
	if err != nil {
		return "", err
	}
	target := filepath.Join(safeParent, name)
	safeTarget, err := m.ResolveSafe(target, false)
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(safeTarget, 0o755); err != nil {
		return "", err
	}
	return safeTarget, nil
}

func (m *Manager) Rename(oldPath, newName string) (string, error) {
	if !isSafeBaseName(newName) {
		return "", ErrInvalidName
	}
	safeOld, err := m.ResolveSafe(oldPath, true)
	if err != nil {
		return "", err
	}
	if m.isRoot(safeOld) {
		return "", ErrOutsideBoundary
	}
	target := filepath.Join(filepath.Dir(safeOld), newName)
	safeTarget, err := m.ResolveSafe(target, false)
	if err != nil {
		return "", err
	}
	if err := os.Rename(safeOld, safeTarget); err != nil {
		return "", err
	}
	return safeTarget, nil
}

func (m *Manager) Move(srcPath, dstDir string) (string, error) {
	safeSrc, err := m.ResolveSafe(srcPath, true)
	if err != nil {
		return "", err
	}
	if m.isRoot(safeSrc) {
		return "", ErrOutsideBoundary
	}
	safeDstDir, err := m.ResolveSafe(dstDir, true)
	if err != nil {
		return "", err
	}
	target := filepath.Join(safeDstDir, filepath.Base(safeSrc))
	safeTarget, err := m.ResolveSafe(target, false)
	if err != nil {
		return "", err
	}
	if err := os.Rename(safeSrc, safeTarget); err != nil {
		return "", err
	}
	return safeTarget, nil
}

func (m *Manager) SaveUpload(dstDir, filename string, r io.Reader) (string, int64, error) {
	if !isSafeBaseName(filename) {
		return "", 0, ErrInvalidName
	}
	safeDir, err := m.ResolveSafe(dstDir, true)
	if err != nil {
		return "", 0, err
	}
	target := filepath.Join(safeDir, filename)
	safeTarget, err := m.ResolveSafe(target, false)
	if err != nil {
		return "", 0, err
	}
	f, err := os.Create(safeTarget)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(r, 256<<20))
	return safeTarget, n, err
}

func (m *Manager) Delete(targetPath string) error {
	safe, err := m.ResolveSafe(targetPath, true)
	if err != nil {
		return err
	}
	if m.isRoot(safe) {
		return ErrOutsideBoundary
	}
	return os.Remove(safe)
}

func (m *Manager) isRoot(absPath string) bool {
	for _, r := range m.AllowedRoots() {
		if strings.EqualFold(filepath.Clean(r), filepath.Clean(absPath)) {
			return true
		}
	}
	return false
}

func isSafeBaseName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, `/\:*?"<>|`)
}
