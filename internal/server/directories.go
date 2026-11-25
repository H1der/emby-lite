package server

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

type dirEntryDTO struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// BrowseDirectories lists subdirectories on the host/container for the library path picker.
// When AllowedMediaRoots is configured, browsing is strictly confined within those roots.
func (a *AdminHandler) BrowseDirectories(c *gin.Context) {
	rawPath := strings.TrimSpace(c.Query("path"))
	showHidden := strings.EqualFold(c.Query("show_hidden"), "true")
	roots := a.browseRoots()

	// Empty path: return available root entries (drives on Windows, allowed roots or "/" on Unix).
	if rawPath == "" {
		if len(a.cfg.Server.AllowedMediaRoots) > 0 || runtime.GOOS == "windows" {
			c.JSON(http.StatusOK, gin.H{
				"current": "",
				"parent":  "",
				"roots":   roots,
				"entries": roots,
			})
			return
		}
		rawPath = "/"
	}

	absPath, err := filepath.Abs(rawPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid directory path"})
		return
	}
	absPath = filepath.Clean(absPath)

	if len(a.cfg.Server.AllowedMediaRoots) > 0 && !a.isInsideAllowedRoots(absPath) {
		c.JSON(http.StatusForbidden, gin.H{"error": "directory is outside allowed media roots"})
		return
	}

	fi, err := os.Stat(absPath)
	if err != nil || !fi.IsDir() {
		c.JSON(http.StatusNotFound, gin.H{"error": "directory not found"})
		return
	}

	dirEntries, err := os.ReadDir(absPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot read directory: " + err.Error()})
		return
	}

	entries := make([]dirEntryDTO, 0, len(dirEntries))
	for _, de := range dirEntries {
		name := de.Name()
		if !showHidden && isSkippedDirName(name) {
			continue
		}
		full := filepath.Join(absPath, name)
		isDir := de.IsDir()
		if !isDir && de.Type()&os.ModeSymlink != 0 {
			if targetInfo, statErr := os.Stat(full); statErr == nil && targetInfo.IsDir() {
				isDir = true
			}
		}
		if !isDir {
			continue
		}
		entries = append(entries, dirEntryDTO{
			Name: name,
			Path: full,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	parent := computeParentDir(absPath, a.cfg.Server.AllowedMediaRoots)
	c.JSON(http.StatusOK, gin.H{
		"current": absPath,
		"parent":  parent,
		"roots":   roots,
		"entries": entries,
	})
}

func (a *AdminHandler) browseRoots() []dirEntryDTO {
	if len(a.cfg.Server.AllowedMediaRoots) > 0 {
		out := make([]dirEntryDTO, 0, len(a.cfg.Server.AllowedMediaRoots))
		for _, r := range a.cfg.Server.AllowedMediaRoots {
			abs, err := filepath.Abs(strings.TrimSpace(r))
			if err != nil {
				continue
			}
			if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
				out = append(out, dirEntryDTO{Name: abs, Path: abs})
			}
		}
		return out
	}

	if runtime.GOOS == "windows" {
		var drives []dirEntryDTO
		for ch := 'A'; ch <= 'Z'; ch++ {
			root := string(ch) + `:\`
			if fi, err := os.Stat(root); err == nil && fi.IsDir() {
				drives = append(drives, dirEntryDTO{
					Name: string(ch) + ":",
					Path: root,
				})
			}
		}
		return drives
	}

	candidates := []string{"/", "/media", "/mnt", "/data"}
	var out []dirEntryDTO
	seen := map[string]bool{}
	for _, p := range candidates {
		if seen[p] {
			continue
		}
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			seen[p] = true
			out = append(out, dirEntryDTO{Name: p, Path: p})
		}
	}
	return out
}

func (a *AdminHandler) isInsideAllowedRoots(absTarget string) bool {
	realTarget := absTarget
	if resolved, err := filepath.EvalSymlinks(absTarget); err == nil {
		realTarget = resolved
	}
	for _, r := range a.cfg.Server.AllowedMediaRoots {
		absRoot, err := filepath.Abs(strings.TrimSpace(r))
		if err != nil {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(absRoot); err == nil {
			absRoot = resolved
		}
		rel, err := filepath.Rel(absRoot, realTarget)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func computeParentDir(current string, allowedRoots []string) string {
	parent := filepath.Dir(current)
	if parent == current {
		// At drive root (e.g. D:\) on Windows, return "" so clicking Up goes back to drive list.
		if runtime.GOOS == "windows" {
			return ""
		}
		return ""
	}
	if len(allowedRoots) > 0 {
		for _, r := range allowedRoots {
			absRoot, err := filepath.Abs(strings.TrimSpace(r))
			if err == nil && filepath.Clean(absRoot) == filepath.Clean(current) {
				return ""
			}
		}
	}
	return parent
}

func isSkippedDirName(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$") {
		return true
	}
	lower := strings.ToLower(name)
	return lower == "@eadir" || lower == "system volume information" || lower == "recovery" || lower == "lost+found"
}
