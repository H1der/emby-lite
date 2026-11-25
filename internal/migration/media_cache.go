package migration

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"emby-lite/internal/scanner"
)

// probeCacheTTL bounds how long a filesystem lookup stays valid. A migration typically
// finishes well inside it, so each directory/stat is paid once per run; a very long run or
// a tree another process writes to still re-probes eventually instead of caching forever.
const probeCacheTTL = 5 * time.Minute

type dirEntryCache struct {
	byLower map[string]string // lower-cased file name -> full path
	fetched time.Time
}

// mediaProbeCache memoizes the filesystem lookups a migration does per item.
//
// Those lookups dominate everything except SQL writes: a season folder is listed once per
// episode, and each poster/backdrop candidate is stat'ed again for every item that shares
// the folder. On a NAS mount every redundant call is a network round trip.
type mediaProbeCache struct {
	mu  sync.Mutex
	dir map[string]dirEntryCache  // directory -> image file listing
	img map[string]imgEntryCache  // directory|stem -> discovered image kinds
	st  map[string]statEntryCache // path -> os.Stat result
}

type imgEntryCache struct {
	byKind  map[string]string
	fetched time.Time
}

type statEntryCache struct {
	size    int64
	mtime   int64
	modTime time.Time
	exists  bool
	isDir   bool
	fetched time.Time
}

func newMediaProbeCache() *mediaProbeCache {
	return &mediaProbeCache{
		dir: map[string]dirEntryCache{},
		img: map[string]imgEntryCache{},
		st:  map[string]statEntryCache{},
	}
}

// statFile reports the size, mtime and modification time of a regular file, reusing a
// recent probe. ok is false when the path is missing or is not a regular file.
func (c *mediaProbeCache) statFile(path string) (size int64, mtime int64, modTime time.Time, ok bool) {
	if path == "" {
		return 0, 0, time.Time{}, false
	}

	c.mu.Lock()
	entry, hit := c.st[path]
	c.mu.Unlock()
	if hit && time.Since(entry.fetched) < probeCacheTTL {
		return entry.size, entry.mtime, entry.modTime, entry.exists && !entry.isDir
	}

	entry = statEntryCache{fetched: time.Now()}
	if fi, err := os.Stat(path); err == nil {
		entry.size = fi.Size()
		entry.mtime = fi.ModTime().Unix()
		entry.modTime = fi.ModTime()
		entry.exists = true
		entry.isDir = fi.IsDir()
	}

	c.mu.Lock()
	c.st[path] = entry
	c.mu.Unlock()
	return entry.size, entry.mtime, entry.modTime, entry.exists && !entry.isDir
}

// dirImages lists the image files directly inside dir, keyed by lower-cased file name.
// Unreadable directories (for example a path only the old server mounted) are cached as
// empty so the dead mount is probed once instead of once per item.
func (c *mediaProbeCache) dirImages(dir string) map[string]string {
	c.mu.Lock()
	entry, hit := c.dir[dir]
	c.mu.Unlock()
	if hit && time.Since(entry.fetched) < probeCacheTTL {
		return entry.byLower
	}

	byLower := map[string]string{}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !scanner.IsImageFile(e.Name()) {
				continue
			}
			byLower[strings.ToLower(e.Name())] = filepath.Join(dir, e.Name())
		}
	}

	c.mu.Lock()
	c.dir[dir] = dirEntryCache{byLower: byLower, fetched: time.Now()}
	c.mu.Unlock()
	return byLower
}

// discoverImages mirrors scanner.DiscoverLocalImages but resolves the directory listing
// through the cache, so a folder is read once per run instead of once per item.
func (c *mediaProbeCache) discoverImages(dir, mediaStem string) map[string]string {
	if dir == "" {
		return nil
	}

	key := dir + "|" + strings.ToLower(mediaStem)
	c.mu.Lock()
	entry, hit := c.img[key]
	c.mu.Unlock()
	if hit && time.Since(entry.fetched) < probeCacheTTL {
		return entry.byKind
	}

	byLower := c.dirImages(dir)
	found := make(map[string]string, 5)
	pick := func(kind string, bases []string) {
		for _, base := range bases {
			for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
				if p, ok := byLower[strings.ToLower(base+ext)]; ok {
					found[kind] = p
					return
				}
			}
		}
	}

	var primaryBases, backdropBases, thumbBases, logoBases, bannerBases []string
	if mediaStem != "" {
		primaryBases = append(primaryBases, mediaStem+"-poster", mediaStem+"-thumb", mediaStem)
		backdropBases = append(backdropBases, mediaStem+"-fanart", mediaStem+"-backdrop")
		thumbBases = append(thumbBases, mediaStem+"-thumb", mediaStem+"-landscape")
		logoBases = append(logoBases, mediaStem+"-clearlogo", mediaStem+"-logo")
		bannerBases = append(bannerBases, mediaStem+"-banner")
	}
	primaryBases = append(primaryBases, "poster", "folder", "cover", "movie", "show")
	backdropBases = append(backdropBases, "fanart", "backdrop", "backdrop1", "background")
	thumbBases = append(thumbBases, "thumb", "landscape")
	logoBases = append(logoBases, "clearlogo", "logo")
	bannerBases = append(bannerBases, "banner")

	pick("Primary", primaryBases)
	pick("Backdrop", backdropBases)
	pick("Thumb", thumbBases)
	pick("Logo", logoBases)
	pick("Banner", bannerBases)

	c.mu.Lock()
	c.img[key] = imgEntryCache{byKind: found, fetched: time.Now()}
	c.mu.Unlock()
	return found
}
