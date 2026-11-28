package image

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

const personDirCacheTTL = 5 * time.Minute

type cachedBucketDir struct {
	expiresAt time.Time
	dirs      []string // subdirectory names
	lowerMap  map[string]string
}

type cachedPersonImg struct {
	expiresAt time.Time
	path      string // empty string = confirmed negative hit
}

var (
	personCacheMu      sync.RWMutex
	bucketDirCache     = make(map[string]cachedBucketDir, 64)
	personImgPathCache = make(map[string]cachedPersonImg, 1024)
)

func readBucketDirsCached(bdir string) (cachedBucketDir, bool) {
	now := time.Now()
	personCacheMu.RLock()
	entry, ok := bucketDirCache[bdir]
	personCacheMu.RUnlock()
	if ok && now.Before(entry.expiresAt) {
		return entry, true
	}

	entries, err := os.ReadDir(bdir)
	if err != nil {
		entry = cachedBucketDir{expiresAt: now.Add(personDirCacheTTL)}
		personCacheMu.Lock()
		bucketDirCache[bdir] = entry
		personCacheMu.Unlock()
		return entry, false
	}
	dirs := make([]string, 0, len(entries))
	lowerMap := make(map[string]string, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		dirs = append(dirs, name)
		ln := strings.ToLower(name)
		if _, exists := lowerMap[ln]; !exists {
			lowerMap[ln] = name
		}
	}
	entry = cachedBucketDir{
		expiresAt: now.Add(personDirCacheTTL),
		dirs:      dirs,
		lowerMap:  lowerMap,
	}
	personCacheMu.Lock()
	if len(bucketDirCache) > 2048 {
		bucketDirCache = make(map[string]cachedBucketDir, 64)
	}
	bucketDirCache[bdir] = entry
	personCacheMu.Unlock()
	return entry, true
}

// InvalidatePersonImageCache clears in-memory Emby metadata/people directory caches.
func InvalidatePersonImageCache() {
	personCacheMu.Lock()
	bucketDirCache = make(map[string]cachedBucketDir, 64)
	personImgPathCache = make(map[string]cachedPersonImg, 1024)
	personCacheMu.Unlock()
}

var personImageNames = []string{
	"folder.jpg", "folder.jpeg", "folder.png", "folder.webp",
	"poster.jpg", "poster.jpeg", "poster.png", "poster.webp",
	"primary.jpg", "primary.jpeg", "primary.png", "primary.webp",
}

// ResolveEmbyPeopleRoot resolves the effective Emby people/ root directory whether the user pointed to
// "<emby>/metadata" or directly to "<emby>/metadata/people".
func ResolveEmbyPeopleRoot(metadataDir string) string {
	dir := strings.TrimSpace(metadataDir)
	if dir == "" {
		return ""
	}
	clean := filepath.Clean(dir)
	base := strings.ToLower(filepath.Base(clean))
	if base == "people" {
		return clean
	}
	// Check both "people" and "People" case-insensitively on Linux.
	for _, sub := range []string{"people", "People"} {
		cand := filepath.Join(clean, sub)
		if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
			return cand
		}
	}
	return filepath.Join(clean, "people")
}

// FindEmbyPersonImage looks up an actor/director portrait inside an Emby metadata/people directory.
// Supports both Emby 4.x (<initial>/<Name>/folder.jpg or <Name>-tmdb-<id>) and 3.x (<Name>/folder.jpg).
// Uses an in-memory TTL cache for bucket listings and resolved paths to avoid repeated HDD ReadDir storms.
func FindEmbyPersonImage(metadataDir, personName, tmdbID string) string {
	peopleRoot := ResolveEmbyPeopleRoot(metadataDir)
	if peopleRoot == "" {
		return ""
	}
	safeName := sanitizePersonDirName(personName)
	if safeName == "" {
		return ""
	}

	cacheKey := peopleRoot + "|" + strings.ToLower(safeName) + "|" + strings.TrimSpace(tmdbID)
	now := time.Now()
	personCacheMu.RLock()
	if cached, ok := personImgPathCache[cacheKey]; ok && now.Before(cached.expiresAt) {
		personCacheMu.RUnlock()
		return cached.path
	}
	personCacheMu.RUnlock()

	foundPath := findEmbyPersonImageUncached(peopleRoot, safeName, tmdbID)

	personCacheMu.Lock()
	if len(personImgPathCache) > 32768 {
		personImgPathCache = make(map[string]cachedPersonImg, 1024)
	}
	personImgPathCache[cacheKey] = cachedPersonImg{
		expiresAt: now.Add(personDirCacheTTL),
		path:      foundPath,
	}
	personCacheMu.Unlock()
	return foundPath
}

func findEmbyPersonImageUncached(peopleRoot, safeName, tmdbID string) string {
	initial := personInitialBucket(safeName)
	var bucketDirs []string
	if initial != "" {
		bucketDirs = append(bucketDirs,
			filepath.Join(peopleRoot, initial),
			filepath.Join(peopleRoot, strings.ToLower(initial)),
			filepath.Join(peopleRoot, strings.ToUpper(initial)),
		)
	}
	bucketDirs = append(bucketDirs, peopleRoot)

	seenBucket := map[string]bool{}
	lowerSafe := strings.ToLower(safeName)
	tmdbSuffix := ""
	if strings.TrimSpace(tmdbID) != "" {
		tmdbSuffix = "tmdb-" + strings.TrimSpace(tmdbID)
	}

	for _, bdir := range bucketDirs {
		bkey := strings.ToLower(bdir)
		if seenBucket[bkey] {
			continue
		}
		seenBucket[bkey] = true

		// Use cached bucket directory index first: avoids 12+ stat syscalls on missing folders
		// and avoids re-reading large HDD directories on every request.
		bucket, ok := readBucketDirsCached(bdir)
		if !ok || len(bucket.dirs) == 0 {
			continue
		}

		// 1. O(1) exact case-insensitive folder match.
		if dirName, exists := bucket.lowerMap[lowerSafe]; exists {
			if found := findImageInDir(filepath.Join(bdir, dirName)); found != "" {
				return found
			}
		}
		if tmdbSuffix != "" {
			if dirName, exists := bucket.lowerMap[lowerSafe+"-"+strings.ToLower(tmdbSuffix)]; exists {
				if found := findImageInDir(filepath.Join(bdir, dirName)); found != "" {
					return found
				}
			}
		}

		// 2. Scan cached in-memory directory list for tmdb-suffix matches (zero disk I/O until hit).
		lowerTMDB := strings.ToLower(tmdbSuffix)
		for _, dirName := range bucket.dirs {
			en := strings.ToLower(dirName)
			if (lowerTMDB != "" && strings.Contains(en, lowerTMDB)) ||
				strings.HasPrefix(en, lowerSafe+"-tmdb-") ||
				strings.HasPrefix(en, lowerSafe+" [tmdb-") {
				if found := findImageInDir(filepath.Join(bdir, dirName)); found != "" {
					return found
				}
			}
		}
	}
	return ""
}

// WriteEmbyPersonImageIfMissing writes a newly fetched portrait to Emby metadata/people/<initial>/<Name>/folder.jpg.
// It NEVER overwrites an existing image and degrades gracefully (returns "", false) if mounted read-only.
func WriteEmbyPersonImageIfMissing(metadataDir, personName, tmdbID string, data []byte) (string, bool) {
	if len(data) == 0 || strings.TrimSpace(metadataDir) == "" {
		return "", false
	}
	if existing := FindEmbyPersonImage(metadataDir, personName, tmdbID); existing != "" {
		return existing, false
	}
	peopleRoot := ResolveEmbyPeopleRoot(metadataDir)
	safeName := sanitizePersonDirName(personName)
	if peopleRoot == "" || safeName == "" {
		return "", false
	}

	initial := personInitialBucket(safeName)
	targetDir := filepath.Join(peopleRoot, safeName)
	if initial != "" {
		targetDir = filepath.Join(peopleRoot, initial, safeName)
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", false
	}
	ext := ".jpg"
	if len(data) >= 8 && string(data[:4]) == "\x89PNG" {
		ext = ".png"
	} else if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		ext = ".webp"
	}
	targetFile := filepath.Join(targetDir, "folder"+ext)
	tmpFile := targetFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o644); err != nil {
		return "", false
	}
	if err := os.Rename(tmpFile, targetFile); err != nil {
		_ = os.Remove(tmpFile)
		return "", false
	}
	InvalidatePersonImageCache()
	return targetFile, true
}

// CheckDirectoryWritable checks whether a metadata directory exists and is writable.
func CheckDirectoryWritable(dir string) (exists bool, writable bool) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return false, false
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return false, false
	}
	exists = true
	probe := filepath.Join(dir, ".emby-lite-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return exists, false
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return exists, true
}

func findImageInDir(dir string) string {
	for _, fname := range personImageNames {
		p := filepath.Join(dir, fname)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Size() > 0 {
			return p
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		lower := strings.ToLower(e.Name())
		for _, want := range personImageNames {
			if lower == want {
				return filepath.Join(dir, e.Name())
			}
		}
	}
	return ""
}

func sanitizePersonDirName(name string) string {
	s := strings.TrimSpace(name)
	s = strings.NewReplacer("/", " ", "\\", " ", ":", " ", "*", "", "?", "", `"`, "", "<", "", ">", "", "|", "").Replace(s)
	s = strings.Trim(s, ". ")
	if s == "" || s == "." || s == ".." {
		return ""
	}
	return s
}

func personInitialBucket(name string) string {
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Han, r) {
			return strings.ToUpper(string(r))
		}
	}
	return ""
}
