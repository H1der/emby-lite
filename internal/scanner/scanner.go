package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"emby-lite/internal/database"
	"emby-lite/internal/model"
	"emby-lite/internal/nativeprobe"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ProgressFunc receives live scan/probe progress updates.
type ProgressFunc func(progress, total int, message string)

// ScanStats summarizes the outcome of a library scan.
type ScanStats struct {
	LibraryID    string    `json:"library_id"`
	RunID        string    `json:"run_id"`
	FilesFound   int       `json:"files_found"`
	ItemsAdded   int       `json:"items_added"`
	ItemsUpdated int       `json:"items_updated"`
	ItemsMoved   int       `json:"items_moved"`
	ItemsSkipped int       `json:"items_skipped"`
	ItemsRemoved int       `json:"items_removed"`
	DurationMs   int64     `json:"duration_ms"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	Error        string    `json:"error,omitempty"`
}

type Service struct {
	db      *database.DB
	prober  *nativeprobe.Prober
	mu      sync.Mutex
	running map[string]bool
}

func NewService(db *database.DB, prober *nativeprobe.Prober) *Service {
	if prober == nil {
		prober = nativeprobe.New(nativeprobe.Options{EnableFFprobeFallback: true})
	}
	return &Service{
		db:      db,
		prober:  prober,
		running: make(map[string]bool),
	}
}

func (s *Service) Prober() *nativeprobe.Prober {
	return s.prober
}

func (s *Service) IsScanning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running) > 0
}

func (s *Service) IsLibraryScanning(libraryID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[libraryID]
}

// ScanLibraryAsync starts a library scan in a background goroutine if not already running.
func (s *Service) ScanLibraryAsync(libraryID string) bool {
	s.mu.Lock()
	if s.running[libraryID] {
		s.mu.Unlock()
		return false
	}
	s.running[libraryID] = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.running, libraryID)
			s.mu.Unlock()
		}()
		if _, err := s.scanLibraryLocked(context.Background(), libraryID, nil); err != nil {
			slog.Error("library scan failed", "library_id", libraryID, "err", err)
		}
	}()
	return true
}

// ScanLibrary runs a synchronous scan of a library (used in tests, CLI, and task queue worker).
func (s *Service) ScanLibrary(ctx context.Context, libraryID string) (*ScanStats, error) {
	return s.ScanLibraryWithProgress(ctx, libraryID, nil)
}

func (s *Service) ScanLibraryWithProgress(ctx context.Context, libraryID string, report ProgressFunc) (*ScanStats, error) {
	s.mu.Lock()
	if s.running[libraryID] {
		s.mu.Unlock()
		return nil, errors.New("library scan already running")
	}
	s.running[libraryID] = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.running, libraryID)
		s.mu.Unlock()
	}()
	return s.scanLibraryLocked(ctx, libraryID, report)
}

// ScanAllAsync triggers scans for all configured libraries.
func (s *Service) ScanAllAsync() int {
	var libs []model.Library
	if err := s.db.Gorm.Find(&libs).Error; err != nil {
		return 0
	}
	started := 0
	for _, l := range libs {
		if s.ScanLibraryAsync(l.ID) {
			started++
		}
	}
	return started
}

// ProbeLibrarySources probes unprobed MediaSources (local + optional STRM via bounded HTTP Range) in a library.
func (s *Service) ProbeLibrarySources(ctx context.Context, libraryID string, includeSTRM bool, report ProgressFunc) (int, error) {
	var sources []model.MediaSource
	q := s.db.Gorm.Table("media_sources").
		Joins("JOIN items ON items.id = media_sources.item_id").
		Where("items.library_id = ? AND items.soft_deleted_at IS NULL AND media_sources.probed_at IS NULL", libraryID)
	if !includeSTRM {
		q = q.Where("media_sources.source_type = ?", "local")
	}
	if err := q.Find(&sources).Error; err != nil {
		return 0, err
	}
	total := len(sources)
	probed := 0
	for i, src := range sources {
		if ctx.Err() != nil {
			return probed, ctx.Err()
		}
		if report != nil {
			report(i, total, fmt.Sprintf("正在探测 (%d/%d): %s", i+1, total, filepath.Base(src.Path)))
		}
		if s.probeAndSaveSource(ctx, &src) {
			probed++
		}
	}
	if report != nil {
		report(total, total, fmt.Sprintf("探测完成：成功 %d / %d", probed, total))
	}
	return probed, nil
}

func (s *Service) probeAndSaveSource(ctx context.Context, src *model.MediaSource) bool {
	var res *nativeprobe.Result
	var err error
	if src.SourceType == "strm" {
		if strings.HasPrefix(strings.ToLower(src.StrmURL), "http://") || strings.HasPrefix(strings.ToLower(src.StrmURL), "https://") {
			res, err = s.prober.ProbeRemoteURL(ctx, src.StrmURL, src.Container)
		} else if src.StrmURL != "" {
			res, err = s.prober.ProbeLocal(ctx, src.StrmURL)
		}
	} else {
		res, err = s.prober.ProbeLocal(ctx, src.Path)
	}
	if err != nil || res == nil {
		return false
	}
	now := time.Now().UTC()
	src.Container = res.Container
	src.VideoCodec = res.VideoCodec
	src.Width = res.Width
	src.Height = res.Height
	src.HDRType = res.HDRType
	src.DurationSecs = res.DurationSecs
	src.Bitrate = res.Bitrate
	src.ProbeJSON = mustJSON(res.Streams)
	src.ProbedAt = &now
	_ = s.db.Gorm.Save(src).Error

	if res.DurationSecs > 0 {
		ticks := int64(res.DurationSecs * 1e7)
		_ = s.db.Gorm.Model(&model.Item{}).
			Where("id = ? AND runtime_ticks = 0", src.ItemID).
			Update("runtime_ticks", ticks).Error
	}
	return true
}

func (s *Service) scanLibraryLocked(ctx context.Context, libraryID string, report ProgressFunc) (*ScanStats, error) {
	var lib model.Library
	if err := s.db.Gorm.Where("id = ?", libraryID).First(&lib).Error; err != nil {
		return nil, err
	}

	var paths []string
	_ = json.Unmarshal([]byte(lib.PathsJSON), &paths)
	if len(paths) == 0 {
		return nil, errors.New("library has no paths configured")
	}

	runID := strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	stats := &ScanStats{
		LibraryID: libraryID,
		RunID:     runID,
		StartedAt: time.Now().UTC(),
	}

	_ = s.db.Gorm.Model(&lib).Updates(map[string]any{
		"status":     "scanning",
		"last_error": "",
	}).Error

	if report != nil {
		report(0, 0, fmt.Sprintf("开始扫描媒体库：%s", lib.Name))
	}

	// Stage 1: Discover all candidate media files so we have an accurate total & move-detection view.
	type candidate struct {
		root string
		path string
		info fs.FileInfo
	}
	var candidates []candidate
	seenPaths := map[string]bool{}
	var scanErr error

	for _, root := range paths {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			scanErr = err
			continue
		}
		info, err := os.Stat(absRoot)
		if err != nil || !info.IsDir() {
			scanErr = fmt.Errorf("library root inaccessible: %s", absRoot)
			continue
		}

		walkErr := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if strings.HasPrefix(name, ".") || strings.EqualFold(name, "@eaDir") || strings.EqualFold(name, "extrafanart") || strings.EqualFold(name, "extrathumbs") {
					return filepath.SkipDir
				}
				return nil
			}
			if !IsVideoFile(d.Name()) {
				return nil
			}
			fi, statErr := d.Info()
			if statErr != nil {
				return nil
			}
			seenPaths[path] = true
			candidates = append(candidates, candidate{root: absRoot, path: path, info: fi})
			return nil
		})
		if walkErr != nil && scanErr == nil {
			scanErr = walkErr
		}
	}

	// Pre-index vanished ScanFiles in this library by fingerprint for zero-loss move detection!
	movedCandidates := map[string]model.ScanFile{}
	var existingScanFiles []model.ScanFile
	if err := s.db.Gorm.Where("library_id = ?", lib.ID).Find(&existingScanFiles).Error; err == nil {
		for _, sf := range existingScanFiles {
			if !seenPaths[sf.Path] && sf.Fingerprint != "" && sf.ItemID != "" {
				movedCandidates[sf.Fingerprint] = sf
			}
		}
	}

	stats.FilesFound = len(candidates)
	seenItems := map[string]bool{}

	// Stage 2: Process each candidate incrementally.
	for idx, cand := range candidates {
		if ctx.Err() != nil {
			scanErr = ctx.Err()
			break
		}
		if report != nil && (idx == 0 || (idx+1)%5 == 0 || idx+1 == len(candidates)) {
			report(idx+1, len(candidates), fmt.Sprintf("扫描 %s (%d/%d)", lib.Name, idx+1, len(candidates)))
		}

		dirPath := filepath.Dir(cand.path)
		depHash := computeDirDepHash(dirPath)

		var existingScan model.ScanFile
		scanRecErr := s.db.Gorm.Where("library_id = ? AND path = ?", lib.ID, cand.path).First(&existingScan).Error
		if scanRecErr == nil &&
			existingScan.Size == cand.info.Size() &&
			existingScan.Mtime == cand.info.ModTime().Unix() &&
			existingScan.DepHash == depHash &&
			existingScan.ItemID != "" {
			_ = s.db.Gorm.Model(&existingScan).Update("last_scan_run", runID).Error
			seenItems[existingScan.ItemID] = true
			s.markAncestorsSeen(existingScan.ItemID, seenItems)
			stats.ItemsSkipped++
			continue
		}

		// Move detection: if this path is new but matches a vanished file's fingerprint in the same library,
		// relocate the existing Item & MediaSource in-place so watch progress/favorites are preserved!
		fp := Fingerprint(cand.path, cand.info.Size())
		if scanRecErr != nil {
			if oldSF, ok := movedCandidates[fp]; ok {
				if relocated := s.relocateMovedItem(&lib, &oldSF, cand.path, dirPath, cand.info, depHash, runID); relocated {
					delete(movedCandidates, fp)
					seenItems[oldSF.ItemID] = true
					s.markAncestorsSeen(oldSF.ItemID, seenItems)
					stats.ItemsMoved++
					continue
				}
			}
		}

		itemID, added, upsertErr := s.ingestMediaFile(ctx, &lib, cand.root, cand.path, cand.info, depHash, runID, seenItems)
		if upsertErr != nil {
			slog.Warn("failed to ingest media file", "path", cand.path, "err", upsertErr)
			continue
		}
		if added {
			stats.ItemsAdded++
		} else {
			stats.ItemsUpdated++
		}
		if itemID != "" {
			seenItems[itemID] = true
		}
	}

	// Stage 3: Soft-delete items whose files truly disappeared.
	if scanErr == nil {
		removed := s.cleanupMissing(&lib, seenPaths, seenItems)
		stats.ItemsRemoved = removed
	}

	var totalCount int64
	s.db.Gorm.Model(&model.Item{}).
		Where("library_id = ? AND soft_deleted_at IS NULL AND type IN ?", lib.ID, []string{"Movie", "Series"}).
		Count(&totalCount)

	now := time.Now().UTC()
	stats.FinishedAt = now
	stats.DurationMs = now.Sub(stats.StartedAt).Milliseconds()
	updates := map[string]any{
		"status":          "idle",
		"item_count":      totalCount,
		"last_scanned_at": now,
	}
	if scanErr != nil {
		stats.Error = scanErr.Error()
		updates["status"] = "error"
		updates["last_error"] = scanErr.Error()
	}
	_ = s.db.Gorm.Model(&lib).Updates(updates).Error
	s.db.BumpLibraryRevision()

	if report != nil {
		report(len(candidates), len(candidates), fmt.Sprintf("完成：新增 %d，移动 %d，更新 %d，跳过 %d，移除 %d",
			stats.ItemsAdded, stats.ItemsMoved, stats.ItemsUpdated, stats.ItemsSkipped, stats.ItemsRemoved))
	}

	slog.Info("library scan finished",
		"library", lib.Name,
		"files", stats.FilesFound,
		"added", stats.ItemsAdded,
		"moved", stats.ItemsMoved,
		"updated", stats.ItemsUpdated,
		"skipped", stats.ItemsSkipped,
		"removed", stats.ItemsRemoved,
		"ms", stats.DurationMs,
	)
	return stats, scanErr
}

func (s *Service) relocateMovedItem(lib *model.Library, oldSF *model.ScanFile, newPath, newDir string, fi fs.FileInfo, depHash, runID string) bool {
	var item model.Item
	if err := s.db.Gorm.Where("id = ?", oldSF.ItemID).First(&item).Error; err != nil {
		return false
	}
	prefix := "movie"
	if item.Type == "Episode" {
		prefix = "episode"
	}
	newScanKey := fmt.Sprintf("%s:%s:%s", prefix, lib.ID, filepath.Clean(newPath))
	if err := s.db.Gorm.Model(&item).Updates(map[string]any{
		"scan_key":        newScanKey,
		"dir_path":        newDir,
		"soft_deleted_at": nil,
	}).Error; err != nil {
		return false
	}

	// Update MediaSource path & STRM target if applicable.
	var src model.MediaSource
	if err := s.db.Gorm.Where("item_id = ? AND path = ?", item.ID, oldSF.Path).First(&src).Error; err == nil {
		src.Path = newPath
		src.FileMtime = fi.ModTime().Unix()
		if IsSTRMFile(newPath) {
			src.StrmURL = ReadSTRMTarget(newPath)
		}
		_ = s.db.Gorm.Save(&src).Error
		stem := strings.TrimSuffix(filepath.Base(newPath), filepath.Ext(newPath))
		s.syncSidecarSubtitles(item.ID, src.ID, newDir, stem)
		if item.Type == "Movie" {
			s.syncImages(item.ID, DiscoverLocalImages(newDir, stem))
		}
	}

	// Replace old ScanFile record with new path.
	_ = s.db.Gorm.Where("library_id = ? AND path = ?", lib.ID, oldSF.Path).Delete(&model.ScanFile{}).Error
	s.upsertScanFile(lib.ID, newPath, newDir, fi, depHash, item.ID, runID)
	slog.Info("detected moved media file, preserved item id", "item_id", item.ID, "from", oldSF.Path, "to", newPath)
	return true
}

func (s *Service) markAncestorsSeen(itemID string, seen map[string]bool) {
	var item model.Item
	if err := s.db.Gorm.Select("id, parent_id, series_id, season_id").Where("id = ?", itemID).First(&item).Error; err != nil {
		return
	}
	if item.ParentID != "" {
		seen[item.ParentID] = true
	}
	if item.SeriesID != "" {
		seen[item.SeriesID] = true
	}
	if item.SeasonID != "" {
		seen[item.SeasonID] = true
	}
}

func (s *Service) ingestMediaFile(ctx context.Context, lib *model.Library, root, filePath string, fi fs.FileInfo, depHash, runID string, seenItems map[string]bool) (string, bool, error) {
	dir := filepath.Dir(filePath)
	fileName := filepath.Base(filePath)
	parentName := filepath.Base(dir)

	isTV := false
	switch lib.Kind {
	case "tvshows":
		isTV = true
	case "mixed":
		ep := ParseEpisodeName(fileName, parentName)
		if ep.Found || ParseSeasonDir(parentName) >= 0 {
			isTV = true
		}
	}

	if isTV {
		return s.ingestEpisode(ctx, lib, root, filePath, fi, depHash, runID, seenItems)
	}
	return s.ingestMovie(ctx, lib, filePath, fi, depHash, runID)
}

func (s *Service) ingestMovie(ctx context.Context, lib *model.Library, filePath string, fi fs.FileInfo, depHash, runID string) (string, bool, error) {
	dir := filepath.Dir(filePath)
	fileName := filepath.Base(filePath)
	stem := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	parentName := filepath.Base(dir)

	parsed := ParseMovieName(fileName, parentName)
	nfo, hasNFO := ReadFirstNFO(
		filepath.Join(dir, stem+".nfo"),
		filepath.Join(dir, "movie.nfo"),
	)

	title := parsed.Title
	origTitle := ""
	sortTitle := ""
	overview := ""
	tagline := ""
	year := parsed.Year
	premiereDate := ""
	rating := 0.0
	officialRating := ""
	var runtimeTicks int64
	providerIDs := map[string]string{}
	var studios, genres, tags []string

	if hasNFO {
		if t := strings.TrimSpace(nfo.Title); t != "" {
			title = t
		}
		origTitle = strings.TrimSpace(nfo.OriginalTitle)
		sortTitle = strings.TrimSpace(nfo.SortTitle)
		overview = nfo.EffectivePlot()
		tagline = strings.TrimSpace(nfo.Tagline)
		if y := nfo.EffectiveYear(); y > 0 {
			year = y
		}
		premiereDate = nfo.EffectivePremiereDate()
		rating = nfo.EffectiveRating()
		officialRating = strings.TrimSpace(nfo.MPAA)
		if secs := nfo.EffectiveDurationSeconds(); secs > 0 {
			runtimeTicks = int64(secs * 1e7)
		}
		providerIDs = nfo.ProviderIDs()
		studios = nfo.Studios
		genres = nfo.Genres
		tags = nfo.Tags
	}

	if sortTitle == "" {
		sortTitle = BuildSortName(title)
	}

	scanKey := fmt.Sprintf("movie:%s:%s", lib.ID, filepath.Clean(filePath))
	itemID := StableID("item", scanKey)
	now := time.Now().UTC()

	var existing model.Item
	err := s.db.Gorm.Where("scan_key = ?", scanKey).First(&existing).Error
	added := errors.Is(err, gorm.ErrRecordNotFound)

	item := model.Item{
		ID:              itemID,
		LibraryID:       lib.ID,
		ParentID:        lib.ID,
		Type:            "Movie",
		Name:            title,
		OriginalName:    origTitle,
		SortName:        sortTitle,
		PinyinInitials:  PinyinInitials(title + " " + origTitle),
		Overview:        overview,
		Tagline:         tagline,
		Year:            year,
		PremiereDate:    premiereDate,
		CommunityRating: rating,
		OfficialRating:  officialRating,
		RuntimeTicks:    runtimeTicks,
		ProviderIDsJSON: mustJSON(providerIDs),
		StudiosJSON:     mustJSON(studios),
		DirPath:         dir,
		ScanKey:         scanKey,
		RandomKey:       StableID("rnd", scanKey)[:12],
		AddedAt:         fi.ModTime().UTC(),
		SoftDeletedAt:   nil,
	}
	if !added {
		item.ID = existing.ID
		item.AddedAt = existing.AddedAt
		item.CreatedAt = existing.CreatedAt
		item.RandomKey = existing.RandomKey
		s.applyLockedFields(&item, &existing)
	} else {
		item.CreatedAt = now
	}

	if err := s.db.Gorm.Save(&item).Error; err != nil {
		return "", false, err
	}

	s.syncFTS(&item)
	sourceID := s.upsertMediaSource(ctx, &item, filePath, fi, parsed.VersionLabel, nfo)
	s.syncSidecarSubtitles(item.ID, sourceID, dir, stem)
	s.syncImages(item.ID, DiscoverLocalImages(dir, stem))
	locks := s.loadLockedFields(item.ID)
	if !locks["genres"] {
		s.syncGenresAndTags(item.ID, genres, tags)
	}
	if hasNFO {
		s.syncPeople(item.ID, dir, nfo)
	}

	s.upsertScanFile(lib.ID, filePath, dir, fi, depHash, item.ID, runID)
	return item.ID, added, nil
}

func (s *Service) ingestEpisode(ctx context.Context, lib *model.Library, root, filePath string, fi fs.FileInfo, depHash, runID string, seenItems map[string]bool) (string, bool, error) {
	dir := filepath.Dir(filePath)
	fileName := filepath.Base(filePath)
	stem := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	parentName := filepath.Base(dir)

	seasonNum := ParseSeasonDir(parentName)
	var seriesDir, seasonDir string
	if seasonNum >= 0 {
		seasonDir = dir
		seriesDir = filepath.Dir(dir)
	} else {
		seriesDir = dir
		seasonDir = dir
	}
	if !isSubPath(root, seriesDir) || filepath.Clean(seriesDir) == filepath.Clean(root) {
		seriesDir = dir
		seasonDir = dir
	}

	parsed := ParseEpisodeName(fileName, parentName)
	epNFO, hasEpNFO := ReadFirstNFO(filepath.Join(dir, stem+".nfo"))
	if hasEpNFO {
		if epNFO.Season > 0 || (epNFO.Season == 0 && seasonNum == 0) {
			parsed.Season = epNFO.Season
		}
		if epNFO.Episode > 0 {
			parsed.Episode = epNFO.Episode
		}
	}
	if !parsed.Found && seasonNum >= 0 {
		parsed.Season = seasonNum
	}
	if parsed.Season < 0 {
		parsed.Season = 1
	}

	seriesItem, err := s.ensureSeries(lib, seriesDir)
	if err != nil {
		return "", false, err
	}
	seenItems[seriesItem.ID] = true

	seasonItem, err := s.ensureSeason(lib, seriesItem, seasonDir, parsed.Season)
	if err != nil {
		return "", false, err
	}
	seenItems[seasonItem.ID] = true

	epTitle := parsed.Title
	if hasEpNFO && strings.TrimSpace(epNFO.Title) != "" {
		epTitle = strings.TrimSpace(epNFO.Title)
	}
	if epTitle == "" {
		if parsed.Episode > 0 {
			epTitle = fmt.Sprintf("第 %d 集", parsed.Episode)
		} else {
			epTitle = stem
		}
	}

	overview := ""
	premiereDate := ""
	rating := 0.0
	var runtimeTicks int64
	providerIDs := map[string]string{}
	if hasEpNFO {
		overview = epNFO.EffectivePlot()
		premiereDate = epNFO.EffectivePremiereDate()
		rating = epNFO.EffectiveRating()
		if secs := epNFO.EffectiveDurationSeconds(); secs > 0 {
			runtimeTicks = int64(secs * 1e7)
		}
		providerIDs = epNFO.ProviderIDs()
	}

	scanKey := fmt.Sprintf("episode:%s:%s", lib.ID, filepath.Clean(filePath))
	itemID := StableID("item", scanKey)
	now := time.Now().UTC()

	var existing model.Item
	err = s.db.Gorm.Where("scan_key = ?", scanKey).First(&existing).Error
	added := errors.Is(err, gorm.ErrRecordNotFound)

	epItem := model.Item{
		ID:              itemID,
		LibraryID:       lib.ID,
		ParentID:        seasonItem.ID,
		SeriesID:        seriesItem.ID,
		SeasonID:        seasonItem.ID,
		Type:            "Episode",
		Name:            epTitle,
		SortName:        fmt.Sprintf("%03d-%04d-%s", parsed.Season, parsed.Episode, BuildSortName(epTitle)),
		PinyinInitials:  PinyinInitials(epTitle),
		Overview:        overview,
		Year:            seriesItem.Year,
		PremiereDate:    premiereDate,
		SeasonNum:       parsed.Season,
		EpisodeNum:      parsed.Episode,
		CommunityRating: rating,
		RuntimeTicks:    runtimeTicks,
		ProviderIDsJSON: mustJSON(providerIDs),
		DirPath:         dir,
		ScanKey:         scanKey,
		RandomKey:       StableID("rnd", scanKey)[:12],
		AddedAt:         fi.ModTime().UTC(),
		SoftDeletedAt:   nil,
	}
	if !added {
		epItem.ID = existing.ID
		epItem.AddedAt = existing.AddedAt
		epItem.CreatedAt = existing.CreatedAt
		epItem.RandomKey = existing.RandomKey
	} else {
		epItem.CreatedAt = now
	}

	if err := s.db.Gorm.Save(&epItem).Error; err != nil {
		return "", false, err
	}

	s.syncFTS(&epItem)
	sourceID := s.upsertMediaSource(ctx, &epItem, filePath, fi, "", epNFO)
	s.syncSidecarSubtitles(epItem.ID, sourceID, dir, stem)

	epImages := map[string]string{}
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
		for _, cand := range []string{filepath.Join(dir, stem+"-thumb"+ext), filepath.Join(dir, stem+ext)} {
			if _, statErr := os.Stat(cand); statErr == nil {
				epImages["Primary"] = cand
				epImages["Thumb"] = cand
				break
			}
		}
		if _, ok := epImages["Primary"]; ok {
			break
		}
	}
	s.syncImages(epItem.ID, epImages)
	s.upsertScanFile(lib.ID, filePath, dir, fi, depHash, epItem.ID, runID)
	return epItem.ID, added, nil
}

func (s *Service) ensureSeries(lib *model.Library, seriesDir string) (*model.Item, error) {
	scanKey := fmt.Sprintf("series:%s:%s", lib.ID, filepath.Clean(seriesDir))
	id := StableID("item", scanKey)

	dirName := filepath.Base(seriesDir)
	title, year := extractTitleAndYear(dirName)
	if title == "" {
		title = dirName
	}

	nfo, hasNFO := ReadFirstNFO(filepath.Join(seriesDir, "tvshow.nfo"))
	origTitle := ""
	sortTitle := ""
	overview := ""
	tagline := ""
	premiereDate := ""
	rating := 0.0
	officialRating := ""
	providerIDs := map[string]string{}
	var studios, genres, tags []string

	if hasNFO {
		if t := strings.TrimSpace(nfo.Title); t != "" {
			title = t
		}
		origTitle = strings.TrimSpace(nfo.OriginalTitle)
		sortTitle = strings.TrimSpace(nfo.SortTitle)
		overview = nfo.EffectivePlot()
		tagline = strings.TrimSpace(nfo.Tagline)
		if y := nfo.EffectiveYear(); y > 0 {
			year = y
		}
		premiereDate = nfo.EffectivePremiereDate()
		rating = nfo.EffectiveRating()
		officialRating = strings.TrimSpace(nfo.MPAA)
		providerIDs = nfo.ProviderIDs()
		studios = nfo.Studios
		genres = nfo.Genres
		tags = nfo.Tags
	}
	if sortTitle == "" {
		sortTitle = BuildSortName(title)
	}

	now := time.Now().UTC()
	var existing model.Item
	err := s.db.Gorm.Where("scan_key = ?", scanKey).First(&existing).Error
	series := model.Item{
		ID:              id,
		LibraryID:       lib.ID,
		ParentID:        lib.ID,
		Type:            "Series",
		Name:            title,
		OriginalName:    origTitle,
		SortName:        sortTitle,
		PinyinInitials:  PinyinInitials(title + " " + origTitle),
		Overview:        overview,
		Tagline:         tagline,
		Year:            year,
		PremiereDate:    premiereDate,
		CommunityRating: rating,
		OfficialRating:  officialRating,
		ProviderIDsJSON: mustJSON(providerIDs),
		StudiosJSON:     mustJSON(studios),
		DirPath:         seriesDir,
		ScanKey:         scanKey,
		RandomKey:       StableID("rnd", scanKey)[:12],
		AddedAt:         now,
		SoftDeletedAt:   nil,
	}
	if err == nil {
		series.ID = existing.ID
		series.AddedAt = existing.AddedAt
		series.CreatedAt = existing.CreatedAt
		series.RandomKey = existing.RandomKey
		s.applyLockedFields(&series, &existing)
	} else {
		series.CreatedAt = now
	}

	if err := s.db.Gorm.Save(&series).Error; err != nil {
		return nil, err
	}
	s.syncFTS(&series)
	s.syncImages(series.ID, DiscoverLocalImages(seriesDir, ""))
	locks := s.loadLockedFields(series.ID)
	if !locks["genres"] {
		s.syncGenresAndTags(series.ID, genres, tags)
	}
	if hasNFO {
		s.syncPeople(series.ID, seriesDir, nfo)
	}
	return &series, nil
}

func (s *Service) ensureSeason(lib *model.Library, series *model.Item, seasonDir string, seasonNum int) (*model.Item, error) {
	scanKey := fmt.Sprintf("season:%s:%s:%d", lib.ID, series.ID, seasonNum)
	id := StableID("item", scanKey)

	name := fmt.Sprintf("第 %d 季", seasonNum)
	if seasonNum == 0 {
		name = "特别篇"
	}
	overview := ""
	if nfo, ok := ReadFirstNFO(filepath.Join(seasonDir, "season.nfo")); ok {
		if t := strings.TrimSpace(nfo.Title); t != "" {
			name = t
		}
		overview = nfo.EffectivePlot()
	}

	now := time.Now().UTC()
	var existing model.Item
	err := s.db.Gorm.Where("scan_key = ?", scanKey).First(&existing).Error
	season := model.Item{
		ID:            id,
		LibraryID:     lib.ID,
		ParentID:      series.ID,
		SeriesID:      series.ID,
		Type:          "Season",
		Name:          name,
		SortName:      fmt.Sprintf("%03d", seasonNum),
		Overview:      overview,
		Year:          series.Year,
		SeasonNum:     seasonNum,
		DirPath:       seasonDir,
		ScanKey:       scanKey,
		RandomKey:     StableID("rnd", scanKey)[:12],
		AddedAt:       now,
		SoftDeletedAt: nil,
	}
	if err == nil {
		season.ID = existing.ID
		season.AddedAt = existing.AddedAt
		season.CreatedAt = existing.CreatedAt
		season.RandomKey = existing.RandomKey
	} else {
		season.CreatedAt = now
	}
	if err := s.db.Gorm.Save(&season).Error; err != nil {
		return nil, err
	}
	if poster := DiscoverSeasonPoster(seasonDir, series.DirPath, seasonNum); poster != "" {
		s.syncImages(season.ID, map[string]string{"Primary": poster})
	}
	return &season, nil
}

func (s *Service) upsertMediaSource(ctx context.Context, item *model.Item, filePath string, fi fs.FileInfo, versionLabel string, nfo *NFO) string {
	sourceID := StableID("src", filePath)
	sourceType := "local"
	strmURL := ""
	if IsSTRMFile(filePath) {
		sourceType = "strm"
		strmURL = ReadSTRMTarget(filePath)
	}

	container := ContainerFromPath(filePath, strmURL)
	var videoCodec, hdrType string
	var width, height int
	var durationSecs float64
	var bitrate int64
	var probeJSON string
	var probedAt *time.Time

	// Fast native header probe for local video files (zero network I/O, reads only container headers).
	if sourceType == "local" && s.prober != nil {
		if res, err := s.prober.ProbeLocal(ctx, filePath); err == nil && res != nil {
			now := time.Now().UTC()
			container = res.Container
			videoCodec = res.VideoCodec
			hdrType = res.HDRType
			width = res.Width
			height = res.Height
			durationSecs = res.DurationSecs
			bitrate = res.Bitrate
			probeJSON = mustJSON(res.Streams)
			probedAt = &now
			if item.RuntimeTicks == 0 && res.DurationSecs > 0 {
				item.RuntimeTicks = int64(res.DurationSecs * 1e7)
				_ = s.db.Gorm.Model(item).Update("runtime_ticks", item.RuntimeTicks).Error
			}
		}
	}

	// Fallback to NFO streamdetails if nativeprobe didn't yield streams.
	if probeJSON == "" && nfo != nil {
		sd := nfo.FileInfo.StreamDetails
		var probeStreams []map[string]any
		idx := 0
		for _, v := range sd.Video {
			if videoCodec == "" {
				videoCodec = v.Codec
				width = v.Width
				height = v.Height
			}
			if v.Duration > 0 && durationSecs == 0 {
				durationSecs = v.Duration
			}
			probeStreams = append(probeStreams, map[string]any{
				"Type": "Video", "Index": idx, "Codec": v.Codec,
				"Width": v.Width, "Height": v.Height, "AspectRatio": v.Aspect, "IsExternal": false,
			})
			idx++
		}
		for _, a := range sd.Audio {
			probeStreams = append(probeStreams, map[string]any{
				"Type": "Audio", "Index": idx, "Codec": a.Codec,
				"Language": a.Language, "Channels": a.Channels, "IsExternal": false,
			})
			idx++
		}
		for _, sub := range sd.Subtitle {
			probeStreams = append(probeStreams, map[string]any{
				"Type": "Subtitle", "Index": idx, "Codec": sub.Codec,
				"Language": sub.Language, "IsExternal": false,
			})
			idx++
		}
		if durationSecs == 0 {
			durationSecs = nfo.EffectiveDurationSeconds()
		}
		if len(probeStreams) > 0 {
			probeJSON = mustJSON(probeStreams)
		}
	}

	src := model.MediaSource{
		ID:           sourceID,
		ItemID:       item.ID,
		SourceType:   sourceType,
		Path:         filePath,
		StrmURL:      strmURL,
		FileSize:     fi.Size(),
		FileMtime:    fi.ModTime().Unix(),
		Fingerprint:  Fingerprint(filePath, fi.Size()),
		VersionLabel: versionLabel,
		Container:    container,
		VideoCodec:   videoCodec,
		Width:        width,
		Height:       height,
		HDRType:      hdrType,
		DurationSecs: durationSecs,
		Bitrate:      bitrate,
		ProbeJSON:    probeJSON,
		ProbedAt:     probedAt,
	}
	_ = s.db.Gorm.Save(&src).Error
	return sourceID
}

func (s *Service) syncSidecarSubtitles(itemID, sourceID, dir, mediaStem string) {
	_ = s.db.Gorm.Where("media_source_id = ? AND is_external = ?", sourceID, true).Delete(&model.MediaSubtitle{}).Error
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	lowerStem := strings.ToLower(mediaStem)
	idx := 100
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		codec, ok := SubtitleCodec(e.Name())
		if !ok {
			continue
		}
		lowerName := strings.ToLower(e.Name())
		if !strings.HasPrefix(lowerName, lowerStem) {
			continue
		}
		rest := lowerName[len(lowerStem):]
		if len(rest) > 0 && rest[0] != '.' && rest[0] != '_' && rest[0] != '-' && rest[0] != ' ' {
			continue
		}
		lang, title, isDef, isForced := ParseSubtitleMeta(mediaStem, e.Name())
		subPath := filepath.Join(dir, e.Name())
		_ = s.db.Gorm.Create(&model.MediaSubtitle{
			ID:            StableID("sub", subPath),
			MediaSourceID: sourceID,
			ItemID:        itemID,
			StreamIndex:   idx,
			IsExternal:    true,
			Path:          subPath,
			Language:      lang,
			Title:         title,
			Codec:         codec,
			IsDefault:     isDef,
			IsForced:      isForced,
		}).Error
		idx++
	}
}

func (s *Service) syncImages(itemID string, images map[string]string) {
	for kind, p := range images {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		tag := StableID("imgtag", fmt.Sprintf("%s|%d|%d", p, fi.Size(), fi.ModTime().Unix()))[:16]
		img := model.ItemImage{
			ID:         StableID("img", itemID+"|"+kind+"|0"),
			ItemID:     itemID,
			Kind:       kind,
			IndexNum:   0,
			SourceType: "local",
			SourceRef:  p,
			Tag:        tag,
			UpdatedAt:  time.Now().UTC(),
		}
		_ = s.db.Gorm.Save(&img).Error
	}
}

func (s *Service) syncGenresAndTags(itemID string, genres, tags []string) {
	_ = s.db.Gorm.Where("item_id = ?", itemID).Delete(&model.ItemGenre{}).Error
	seenG := map[string]bool{}
	for _, g := range genres {
		g = strings.TrimSpace(g)
		if g == "" || seenG[g] {
			continue
		}
		seenG[g] = true
		_ = s.db.Gorm.Create(&model.ItemGenre{ItemID: itemID, Genre: g}).Error
	}

	_ = s.db.Gorm.Where("item_id = ?", itemID).Delete(&model.ItemTag{}).Error
	seenT := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seenT[t] {
			continue
		}
		seenT[t] = true
		_ = s.db.Gorm.Create(&model.ItemTag{ItemID: itemID, Tag: t}).Error
	}
}

func (s *Service) syncPeople(itemID, dir string, nfo *NFO) {
	_ = s.db.Gorm.Where("item_id = ?", itemID).Delete(&model.ItemPerson{}).Error
	idx := 0
	upsertPerson := func(name, rawThumb string) string {
		pid := StableID("person", strings.ToLower(name))
		thumb := strings.TrimSpace(rawThumb)
		if thumb != "" && !strings.HasPrefix(strings.ToLower(thumb), "http://") && !strings.HasPrefix(strings.ToLower(thumb), "https://") && !filepath.IsAbs(thumb) {
			cand := filepath.Join(dir, thumb)
			if _, err := os.Stat(cand); err == nil {
				thumb = cand
			} else {
				thumb = ""
			}
		}
		// Check local .actors/ folder (supports both underscore & space variants and jpg/png/webp).
		for _, variant := range []string{strings.ReplaceAll(name, " ", "_"), name} {
			for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
				localActor := filepath.Join(dir, ".actors", variant+ext)
				if _, err := os.Stat(localActor); err == nil {
					thumb = localActor
					break
				}
			}
			if thumb != "" && filepath.IsAbs(thumb) {
				break
			}
		}

		// Non-destructive upsert: never wipe an existing ThumbPath or TMDBID with an empty string!
		var existing model.Person
		if err := s.db.Gorm.Where("id = ?", pid).First(&existing).Error; err == nil {
			updates := map[string]any{"name": name, "updated_at": time.Now().UTC()}
			if thumb != "" && (existing.ThumbPath == "" || filepath.IsAbs(thumb)) {
				updates["thumb_path"] = thumb
			}
			_ = s.db.Gorm.Model(&existing).Updates(updates).Error
		} else {
			_ = s.db.Gorm.Create(&model.Person{
				ID:        pid,
				Name:      name,
				ThumbPath: thumb,
				UpdatedAt: time.Now().UTC(),
			}).Error
		}
		return pid
	}

	for _, a := range nfo.Actors {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			continue
		}
		pid := upsertPerson(name, a.Thumb)
		_ = s.db.Gorm.Save(&model.ItemPerson{
			ItemID:   itemID,
			PersonID: pid,
			RoleType: "Actor",
			RoleName: strings.TrimSpace(a.Role),
			SortIdx:  idx,
		}).Error
		idx++
	}
	for _, d := range nfo.Directors {
		name := strings.TrimSpace(d)
		if name == "" {
			continue
		}
		pid := upsertPerson(name, "")
		_ = s.db.Gorm.Save(&model.ItemPerson{
			ItemID:   itemID,
			PersonID: pid,
			RoleType: "Director",
			SortIdx:  idx,
		}).Error
		idx++
	}
}

func (s *Service) syncFTS(item *model.Item) {
	if !s.db.HasFTS5 {
		return
	}
	_ = s.db.Gorm.Exec("DELETE FROM items_fts WHERE item_id = ?", item.ID).Error
	_ = s.db.Gorm.Exec("INSERT INTO items_fts(item_id, name, original_name, pinyin_initials) VALUES (?, ?, ?, ?)",
		item.ID, item.Name, item.OriginalName, item.PinyinInitials).Error
}

func (s *Service) upsertScanFile(libID, filePath, dir string, fi fs.FileInfo, depHash, itemID, runID string) {
	_ = s.db.Gorm.Save(&model.ScanFile{
		LibraryID:   libID,
		Path:        filePath,
		DirPath:     dir,
		Size:        fi.Size(),
		Mtime:       fi.ModTime().Unix(),
		Fingerprint: Fingerprint(filePath, fi.Size()),
		DepHash:     depHash,
		ItemID:      itemID,
		LastScanRun: runID,
	}).Error
}

func (s *Service) cleanupMissing(lib *model.Library, seenPaths, seenItems map[string]bool) int {
	var oldFiles []model.ScanFile
	if err := s.db.Gorm.Where("library_id = ?", lib.ID).Find(&oldFiles).Error; err != nil {
		return 0
	}
	removed := 0
	now := time.Now().UTC()
	for _, sf := range oldFiles {
		if seenPaths[sf.Path] {
			continue
		}
		_ = s.db.Gorm.Where("library_id = ? AND path = ?", lib.ID, sf.Path).Delete(&model.ScanFile{}).Error
		_ = s.db.Gorm.Where("path = ?", sf.Path).Delete(&model.MediaSource{}).Error
		if sf.ItemID != "" && !seenItems[sf.ItemID] {
			_ = s.db.Gorm.Model(&model.Item{}).Where("id = ?", sf.ItemID).Update("soft_deleted_at", now).Error
			removed++
		}
	}
	return removed
}

func computeDirDepHash(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	h := sha256.New()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".nfo" || IsImageFile(e.Name()) || subtitleExts[ext] != "" {
			if fi, err := e.Info(); err == nil {
				_, _ = fmt.Fprintf(h, "%s|%d|%d\n", e.Name(), fi.Size(), fi.ModTime().Unix())
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func isSubPath(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func (s *Service) loadLockedFields(itemID string) map[string]bool {
	out := map[string]bool{}
	var lock model.ItemMetadataLock
	if err := s.db.Gorm.Where("item_id = ?", itemID).First(&lock).Error; err != nil {
		return out
	}
	var fields []string
	if json.Unmarshal([]byte(lock.LockedFieldsJSON), &fields) == nil {
		for _, f := range fields {
			out[strings.ToLower(strings.TrimSpace(f))] = true
		}
	}
	return out
}

func (s *Service) applyLockedFields(next, existing *model.Item) {
	locks := s.loadLockedFields(existing.ID)
	if len(locks) == 0 {
		return
	}
	if locks["name"] {
		next.Name = existing.Name
		next.PinyinInitials = existing.PinyinInitials
	}
	if locks["original_name"] {
		next.OriginalName = existing.OriginalName
	}
	if locks["sort_name"] {
		next.SortName = existing.SortName
	}
	if locks["overview"] {
		next.Overview = existing.Overview
	}
	if locks["tagline"] {
		next.Tagline = existing.Tagline
	}
	if locks["year"] {
		next.Year = existing.Year
	}
	if locks["premiere_date"] {
		next.PremiereDate = existing.PremiereDate
	}
	if locks["community_rating"] {
		next.CommunityRating = existing.CommunityRating
	}
	if locks["official_rating"] {
		next.OfficialRating = existing.OfficialRating
	}
}
