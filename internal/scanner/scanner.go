package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	LibraryID      string    `json:"library_id"`
	RunID          string    `json:"run_id"`
	FilesFound     int       `json:"files_found"`
	ItemsAdded     int       `json:"items_added"`
	ItemsUpdated   int       `json:"items_updated"`
	ItemsMoved     int       `json:"items_moved"`
	ItemsSkipped   int       `json:"items_skipped"`
	ItemsRemoved   int       `json:"items_removed"`
	SeriesMerged   int       `json:"series_merged"`
	SeasonsMerged  int       `json:"seasons_merged"`
	MoviesMerged   int       `json:"movies_merged"`
	EpisodesMerged int       `json:"episodes_merged"`
	DurationMs     int64     `json:"duration_ms"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
	Error          string    `json:"error,omitempty"`
}

type Service struct {
	db          *database.DB
	prober      *nativeprobe.Prober
	concurrency int
	mu          sync.Mutex
	running     map[string]bool
}

func defaultScanConcurrency() int {
	n := runtime.NumCPU()
	if n < 4 {
		n = 4
	}
	if n > 12 {
		n = 12
	}
	return n
}

func NewService(db *database.DB, prober *nativeprobe.Prober) *Service {
	if prober == nil {
		prober = nativeprobe.New(nativeprobe.Options{EnableFFprobeFallback: true})
	}
	return &Service{
		db:          db,
		prober:      prober,
		concurrency: defaultScanConcurrency(),
		running:     make(map[string]bool),
	}
}

func (s *Service) SetConcurrency(n int) {
	if n <= 0 {
		return
	}
	if n > 32 {
		n = 32
	}
	s.concurrency = n
}

func (s *Service) Concurrency() int {
	if s.concurrency <= 0 {
		return defaultScanConcurrency()
	}
	return s.concurrency
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
	if err := s.db.Read().Find(&libs).Error; err != nil {
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

// ProbeLibrarySources probes unprobed MediaSources (local + optional STRM via bounded HTTP Range) concurrently.
func (s *Service) ProbeLibrarySources(ctx context.Context, libraryID string, includeSTRM bool, report ProgressFunc) (int, error) {
	var sources []model.MediaSource
	q := s.db.Read().Table("media_sources").
		Joins("JOIN items ON items.id = media_sources.item_id").
		Where("items.library_id = ? AND items.soft_deleted_at IS NULL AND media_sources.probed_at IS NULL", libraryID)
	if !includeSTRM {
		q = q.Where("media_sources.source_type = ?", "local")
	}
	if err := q.Find(&sources).Error; err != nil {
		return 0, err
	}
	total := len(sources)
	if total == 0 {
		if report != nil {
			report(0, 0, "探测完成：无待探测条目")
		}
		return 0, nil
	}

	workers := s.Concurrency()
	if workers > 6 {
		workers = 6
	}
	if workers > total {
		workers = total
	}

	type probeOutcome struct {
		src model.MediaSource
		res *nativeprobe.Result
	}
	jobs := make(chan model.MediaSource, total)
	results := make(chan probeOutcome, total)
	var wg sync.WaitGroup
	var doneCount atomic.Int64

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for src := range jobs {
				if ctx.Err() != nil {
					return
				}
				res := s.probeSourceOnly(ctx, &src)
				n := int(doneCount.Add(1))
				if report != nil && (n == 1 || n%5 == 0 || n == total) {
					report(n, total, fmt.Sprintf("正在探测 (%d/%d): %s", n, total, filepath.Base(src.Path)))
				}
				if res != nil {
					results <- probeOutcome{src: src, res: res}
				}
			}
		}()
	}

	for _, src := range sources {
		jobs <- src
	}
	close(jobs)
	wg.Wait()
	close(results)

	var outcomes []probeOutcome
	for o := range results {
		outcomes = append(outcomes, o)
	}

	probed := 0
	const chunkSize = 50
	for start := 0; start < len(outcomes); start += chunkSize {
		end := start + chunkSize
		if end > len(outcomes) {
			end = len(outcomes)
		}
		chunk := outcomes[start:end]
		_ = s.db.Gorm.Transaction(func(tx *gorm.DB) error {
			now := time.Now().UTC()
			for i := range chunk {
				src := chunk[i].src
				res := chunk[i].res
				src.Container = res.Container
				src.VideoCodec = res.VideoCodec
				src.Width = res.Width
				src.Height = res.Height
				src.HDRType = res.HDRType
				src.DurationSecs = res.DurationSecs
				src.Bitrate = res.Bitrate
				src.ProbeJSON = mustJSON(res.Streams)
				src.ProbedAt = &now
				if err := tx.Save(&src).Error; err == nil {
					probed++
				}
				if res.DurationSecs > 0 {
					ticks := int64(res.DurationSecs * 1e7)
					_ = tx.Model(&model.Item{}).
						Where("id = ? AND runtime_ticks = 0", src.ItemID).
						Update("runtime_ticks", ticks).Error
				}
			}
			return nil
		})
	}

	if report != nil {
		report(total, total, fmt.Sprintf("探测完成：成功 %d / %d", probed, total))
	}
	return probed, ctx.Err()
}

func (s *Service) probeSourceOnly(ctx context.Context, src *model.MediaSource) *nativeprobe.Result {
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
	if err != nil {
		return nil
	}
	return res
}

// --- Single-Pass Directory Snapshot & Concurrent Walker ---

type fileMeta struct {
	name  string
	path  string
	size  int64
	mtime int64
}

type dirSnapshot struct {
	dir           string
	imagesByLower map[string]fileMeta
	nfosByLower   map[string]string
	subtitles     []fileMeta
	actorsByLower map[string]string
	depHash       string
}

type scanCandidate struct {
	root  string
	path  string
	dir   string
	name  string
	size  int64
	mtime int64
}

func isIgnoredDirName(name string) bool {
	return strings.HasPrefix(name, ".") ||
		strings.EqualFold(name, "@eaDir") ||
		strings.EqualFold(name, "extrafanart") ||
		strings.EqualFold(name, "extrathumbs")
}

// walkRootConcurrent traverses root using a bounded worker pool, calling os.ReadDir at most ONCE
// per directory and building a reusable dirSnapshot for NFOs, images, subtitles, and .actors/.
func walkRootConcurrent(ctx context.Context, absRoot string, workers int) ([]scanCandidate, map[string]*dirSnapshot, error) {
	if workers < 1 {
		workers = 1
	}

	var (
		mu         sync.Mutex
		candidates []scanCandidate
		snapshots  = make(map[string]*dirSnapshot, 64)
		firstErr   error
	)

	// Work queue with WaitGroup tracking outstanding directory tasks.
	var wg sync.WaitGroup
	dirCh := make(chan string, 256)

	var enqueue func(string)
	enqueue = func(dir string) {
		wg.Add(1)
		select {
		case dirCh <- dir:
		default:
			// Avoid deadlock if channel fills on deep trees: spawn feeder goroutine.
			go func(d string) {
				dirCh <- d
			}(dir)
		}
	}

	processDir := func(dir string) {
		defer wg.Done()
		if ctx.Err() != nil {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}

		snap := &dirSnapshot{
			dir:           dir,
			imagesByLower: make(map[string]fileMeta),
			nfosByLower:   make(map[string]string),
		}
		var depLines []string
		var localVideos []scanCandidate
		var subDirs []string
		hasActorsDir := false

		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				if strings.EqualFold(name, ".actors") {
					hasActorsDir = true
					continue
				}
				if isIgnoredDirName(name) {
					continue
				}
				subDirs = append(subDirs, filepath.Join(dir, name))
				continue
			}

			fullPath := filepath.Join(dir, name)
			ext := strings.ToLower(filepath.Ext(name))
			isVid := videoExts[ext]
			isNFO := ext == ".nfo"
			isImg := imageExts[ext]
			_, isSub := subtitleExts[ext]

			if !isVid && !isNFO && !isImg && !isSub {
				continue
			}

			fi, statErr := e.Info()
			if statErr != nil {
				continue
			}
			sz := fi.Size()
			mt := fi.ModTime().Unix()

			if isVid {
				localVideos = append(localVideos, scanCandidate{
					root:  absRoot,
					path:  fullPath,
					dir:   dir,
					name:  name,
					size:  sz,
					mtime: mt,
				})
				continue
			}

			lower := strings.ToLower(name)
			if isNFO {
				snap.nfosByLower[lower] = fullPath
			} else if isImg {
				snap.imagesByLower[lower] = fileMeta{name: name, path: fullPath, size: sz, mtime: mt}
			} else if isSub {
				snap.subtitles = append(snap.subtitles, fileMeta{name: name, path: fullPath, size: sz, mtime: mt})
			}
			depLines = append(depLines, fmt.Sprintf("%s|%d|%d\n", name, sz, mt))
		}

		if hasActorsDir {
			if aEntries, aErr := os.ReadDir(filepath.Join(dir, ".actors")); aErr == nil {
				snap.actorsByLower = make(map[string]string, len(aEntries))
				for _, ae := range aEntries {
					if !ae.IsDir() && IsImageFile(ae.Name()) {
						snap.actorsByLower[strings.ToLower(ae.Name())] = filepath.Join(dir, ".actors", ae.Name())
					}
				}
			}
		}

		if len(depLines) > 0 {
			sort.Strings(depLines)
			h := sha256.New()
			for _, line := range depLines {
				_, _ = h.Write([]byte(line))
			}
			snap.depHash = hex.EncodeToString(h.Sum(nil))[:16]
		}

		mu.Lock()
		snapshots[dir] = snap
		if len(localVideos) > 0 {
			candidates = append(candidates, localVideos...)
		}
		mu.Unlock()

		for _, sd := range subDirs {
			if ctx.Err() != nil {
				return
			}
			enqueue(sd)
		}
	}

	var workerWG sync.WaitGroup
	for i := 0; i < workers; i++ {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			for dir := range dirCh {
				processDir(dir)
			}
		}()
	}

	enqueue(absRoot)
	wg.Wait()
	close(dirCh)
	workerWG.Wait()

	if ctx.Err() != nil {
		firstErr = ctx.Err()
	}
	return candidates, snapshots, firstErr
}

func (snap *dirSnapshot) discoverImages(mediaStem string) map[string]fileMeta {
	found := map[string]fileMeta{}
	if snap == nil || len(snap.imagesByLower) == 0 {
		return found
	}
	pick := func(kind string, bases []string) {
		if _, ok := found[kind]; ok {
			return
		}
		for _, base := range bases {
			for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
				if fm, ok := snap.imagesByLower[strings.ToLower(base+ext)]; ok {
					found[kind] = fm
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
	return found
}

func (snap *dirSnapshot) discoverEpisodeThumb(stem string) map[string]fileMeta {
	found := map[string]fileMeta{}
	if snap == nil || len(snap.imagesByLower) == 0 {
		return found
	}
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
		for _, base := range []string{stem + "-thumb", stem} {
			if fm, ok := snap.imagesByLower[strings.ToLower(base+ext)]; ok {
				found["Primary"] = fm
				found["Thumb"] = fm
				return found
			}
		}
	}
	return found
}

func discoverSeasonPosterFromSnapshots(seasonSnap, seriesSnap *dirSnapshot, seasonNum int) *fileMeta {
	if seasonSnap != nil {
		imgs := seasonSnap.discoverImages("")
		if fm, ok := imgs["Primary"]; ok {
			return &fm
		}
	}
	if seriesSnap == nil || len(seriesSnap.imagesByLower) == 0 {
		return nil
	}
	var names []string
	if seasonNum == 0 {
		names = []string{"season-specials-poster", "season00-poster"}
	} else {
		names = []string{
			"season" + pad2(seasonNum) + "-poster",
			"season" + strconv.Itoa(seasonNum) + "-poster",
		}
	}
	for _, want := range names {
		for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
			if fm, ok := seriesSnap.imagesByLower[want+ext]; ok {
				cp := fm
				return &cp
			}
		}
	}
	return nil
}

func (snap *dirSnapshot) readNFO(lowerNames ...string) (*NFO, bool) {
	if snap == nil || len(snap.nfosByLower) == 0 {
		return nil, false
	}
	var paths []string
	for _, ln := range lowerNames {
		if p, ok := snap.nfosByLower[strings.ToLower(ln)]; ok {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return nil, false
	}
	return ReadFirstNFO(paths...)
}

func (snap *dirSnapshot) findSidecarSubtitles(mediaStem string) []fileMeta {
	if snap == nil || len(snap.subtitles) == 0 {
		return nil
	}
	lowerStem := strings.ToLower(mediaStem)
	var out []fileMeta
	for _, sub := range snap.subtitles {
		lowerName := strings.ToLower(sub.name)
		if !strings.HasPrefix(lowerName, lowerStem) {
			continue
		}
		rest := lowerName[len(lowerStem):]
		if len(rest) > 0 && rest[0] != '.' && rest[0] != '_' && rest[0] != '-' && rest[0] != ' ' {
			continue
		}
		out = append(out, sub)
	}
	return out
}

func (snap *dirSnapshot) findLocalActorThumb(name string) string {
	if snap == nil || len(snap.actorsByLower) == 0 {
		return ""
	}
	for _, variant := range []string{strings.ReplaceAll(name, " ", "_"), name} {
		lv := strings.ToLower(variant)
		for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
			if p, ok := snap.actorsByLower[lv+ext]; ok {
				return p
			}
		}
	}
	return ""
}

// --- Prepared Ingest Payload (Computed Concurrently Without DB Lock) ---

type preparedMediaSource struct {
	sourceType   string
	strmURL      string
	container    string
	videoCodec   string
	hdrType      string
	width        int
	height       int
	durationSecs float64
	bitrate      int64
	probeJSON    string
	probedAt     *time.Time
	versionLabel string
}

type preparedIngest struct {
	cand         scanCandidate
	depHash      string
	isTV         bool
	seriesDir    string
	seasonDir    string
	seasonNum    int
	episodeNum   int
	stem         string
	title        string
	origTitle    string
	sortTitle    string
	pinyin       string
	overview     string
	tagline      string
	year         int
	premiereDate string
	rating       float64
	officialRat  string
	runtimeTicks int64
	providerIDs  map[string]string
	studios      []string
	genres       []string
	tags         []string
	nfo          *NFO
	hasNFO       bool
	images       map[string]fileMeta
	subtitles    []fileMeta
	snap         *dirSnapshot
	mediaSource  preparedMediaSource
}

type preparedSeries struct {
	dir          string
	title        string
	origTitle    string
	sortTitle    string
	pinyin       string
	overview     string
	tagline      string
	year         int
	premiereDate string
	rating       float64
	officialRat  string
	providerIDs  map[string]string
	studios      []string
	genres       []string
	tags         []string
	nfo          *NFO
	hasNFO       bool
	images       map[string]fileMeta
	snap         *dirSnapshot
}

type preparedSeason struct {
	seriesDir string
	seasonDir string
	seasonNum int
	name      string
	overview  string
	poster    *fileMeta
}

func (s *Service) scanLibraryLocked(ctx context.Context, libraryID string, report ProgressFunc) (*ScanStats, error) {
	var lib model.Library
	if err := s.db.Read().Where("id = ?", libraryID).First(&lib).Error; err != nil {
		return nil, err
	}

	var paths []string
	_ = json.Unmarshal([]byte(lib.PathsJSON), &paths)
	if lib.Kind == "boxsets" {
		// Collections are materialized by the Emby migration and have no
		// filesystem paths, so there is nothing to scan.
		return &ScanStats{LibraryID: libraryID, StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()}, nil
	}
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
		report(0, 0, fmt.Sprintf("开始并发扫描媒体库：%s", lib.Name))
	}

	// Stage 1: Concurrent single-pass directory walk + directory snapshot cache.
	workers := s.Concurrency()
	var candidates []scanCandidate
	allSnapshots := make(map[string]*dirSnapshot, 128)
	seenPaths := make(map[string]bool, 256)
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

		cands, snaps, walkErr := walkRootConcurrent(ctx, absRoot, workers)
		if walkErr != nil && scanErr == nil {
			scanErr = walkErr
		}
		for k, v := range snaps {
			allSnapshots[k] = v
		}
		for _, c := range cands {
			seenPaths[c.path] = true
			candidates = append(candidates, c)
		}
	}

	// Deterministic ordering for reproducible scan behavior.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].path < candidates[j].path
	})

	// Stage 2: Preload existing ScanFiles and Item ancestor map ONCE into memory!
	scanFileByPath := make(map[string]model.ScanFile, len(candidates))
	movedCandidates := make(map[string]model.ScanFile)
	var existingScanFiles []model.ScanFile
	if err := s.db.Read().Where("library_id = ?", lib.ID).Find(&existingScanFiles).Error; err == nil {
		for _, sf := range existingScanFiles {
			scanFileByPath[sf.Path] = sf
			if !seenPaths[sf.Path] && sf.Fingerprint != "" && sf.ItemID != "" {
				movedCandidates[sf.Fingerprint] = sf
			}
		}
	}

	type ancestorRow struct {
		ID       string
		ParentID string
		SeriesID string
		SeasonID string
	}
	ancestorsByID := make(map[string]ancestorRow, len(existingScanFiles))
	var ancRows []ancestorRow
	_ = s.db.Read().Table("items").
		Select("id, parent_id, series_id, season_id").
		Where("library_id = ? AND soft_deleted_at IS NULL", lib.ID).
		Scan(&ancRows).Error
	for _, ar := range ancRows {
		ancestorsByID[ar.ID] = ar
	}

	markAncestorsMem := func(itemID string, seen map[string]bool) {
		if ar, ok := ancestorsByID[itemID]; ok {
			if ar.ParentID != "" {
				seen[ar.ParentID] = true
			}
			if ar.SeriesID != "" {
				seen[ar.SeriesID] = true
			}
			if ar.SeasonID != "" {
				seen[ar.SeasonID] = true
			}
		}
	}

	stats.FilesFound = len(candidates)
	seenItems := make(map[string]bool, len(candidates))

	// Stage 3: Fast-skip unchanged files in memory (ZERO DB writes!) and separate moves vs ingests.
	type moveJob struct {
		cand    scanCandidate
		oldSF   model.ScanFile
		depHash string
	}
	var toMove []moveJob
	var toIngest []scanCandidate

	for _, cand := range candidates {
		if ctx.Err() != nil {
			scanErr = ctx.Err()
			break
		}
		depHash := ""
		if snap := allSnapshots[cand.dir]; snap != nil {
			depHash = snap.depHash
		}
		if existingScan, ok := scanFileByPath[cand.path]; ok {
			if existingScan.Size == cand.size &&
				existingScan.Mtime == cand.mtime &&
				existingScan.DepHash == depHash &&
				existingScan.ItemID != "" {
				seenItems[existingScan.ItemID] = true
				markAncestorsMem(existingScan.ItemID, seenItems)
				stats.ItemsSkipped++
				continue
			}
		} else {
			fp := Fingerprint(cand.path, cand.size)
			if oldSF, ok := movedCandidates[fp]; ok {
				delete(movedCandidates, fp)
				toMove = append(toMove, moveJob{cand: cand, oldSF: oldSF, depHash: depHash})
				continue
			}
		}
		toIngest = append(toIngest, cand)
	}

	if report != nil && stats.ItemsSkipped > 0 {
		report(stats.ItemsSkipped, len(candidates), fmt.Sprintf("快速跳过 %d 个未变更文件，准备处理 %d 个变更...", stats.ItemsSkipped, len(toMove)+len(toIngest)))
	}

	// Stage 4: Relocate moved items in a single transaction.
	if len(toMove) > 0 && ctx.Err() == nil {
		_ = s.db.Gorm.Transaction(func(tx *gorm.DB) error {
			for _, mj := range toMove {
				if relocated := s.relocateMovedItemTx(tx, &lib, &mj.oldSF, mj.cand, allSnapshots[mj.cand.dir], mj.depHash, runID); relocated {
					seenItems[mj.oldSF.ItemID] = true
					markAncestorsMem(mj.oldSF.ItemID, seenItems)
					stats.ItemsMoved++
				} else {
					toIngest = append(toIngest, mj.cand)
				}
			}
			return nil
		})
	}

	// Stage 5: Concurrent I/O preparation (NFO parsing, pure-Go native header probing, image/sub discovery).
	if len(toIngest) > 0 && ctx.Err() == nil {
		prepared := make([]*preparedIngest, len(toIngest))
		seriesDirs := make(map[string]bool)
		type seasonKey struct {
			seriesDir string
			seasonDir string
			seasonNum int
		}
		seasonKeys := make(map[seasonKey]bool)
		var metaMu sync.Mutex

		var prepDone atomic.Int64
		jobCh := make(chan int, len(toIngest))
		for i := range toIngest {
			jobCh <- i
		}
		close(jobCh)

		prepWorkers := workers
		if prepWorkers > len(toIngest) {
			prepWorkers = len(toIngest)
		}
		var prepWG sync.WaitGroup
		for w := 0; w < prepWorkers; w++ {
			prepWG.Add(1)
			go func() {
				defer prepWG.Done()
				for idx := range jobCh {
					if ctx.Err() != nil {
						return
					}
					cand := toIngest[idx]
					snap := allSnapshots[cand.dir]
					p := s.prepareCandidate(ctx, &lib, cand, snap)
					prepared[idx] = p
					if p.isTV {
						metaMu.Lock()
						seriesDirs[p.seriesDir] = true
						seasonKeys[seasonKey{seriesDir: p.seriesDir, seasonDir: p.seasonDir, seasonNum: p.seasonNum}] = true
						metaMu.Unlock()
					}
					done := int(prepDone.Add(1))
					if report != nil && (done == 1 || done%10 == 0 || done == len(toIngest)) {
						overall := stats.ItemsSkipped + stats.ItemsMoved + done/2
						report(overall, len(candidates), fmt.Sprintf("并发解析 %s (%d/%d)", lib.Name, done, len(toIngest)))
					}
				}
			}()
		}
		prepWG.Wait()

		// Prepare unique Series & Seasons ONCE (instead of once per episode!).
		preparedSeriesMap := make(map[string]*preparedSeries, len(seriesDirs))
		for sdir := range seriesDirs {
			snap := allSnapshots[sdir]
			if snap == nil {
				// Series dir might have had no direct videos; build a quick snapshot if needed.
				snap = snapshotSingleDir(sdir)
				allSnapshots[sdir] = snap
			}
			preparedSeriesMap[sdir] = prepareSeriesData(sdir, snap)
		}
		preparedSeasonMap := make(map[seasonKey]*preparedSeason, len(seasonKeys))
		for sk := range seasonKeys {
			seasonSnap := allSnapshots[sk.seasonDir]
			seriesSnap := allSnapshots[sk.seriesDir]
			preparedSeasonMap[sk] = prepareSeasonData(sk.seriesDir, sk.seasonDir, sk.seasonNum, seasonSnap, seriesSnap)
		}

		// Stage 6: Batched transactional DB writer!
		ensuredSeries := make(map[string]*model.Item, len(seriesDirs))
		ensuredSeasons := make(map[seasonKey]*model.Item, len(seasonKeys))
		personCache := make(map[string]string, 128)

		// Pre-commit all unique Series & Seasons in a single transaction first.
		if len(preparedSeriesMap) > 0 && ctx.Err() == nil {
			_ = s.db.Gorm.Transaction(func(tx *gorm.DB) error {
				for sdir, ps := range preparedSeriesMap {
					seriesItem, err := s.ensureSeriesTx(tx, &lib, ps, personCache)
					if err == nil && seriesItem != nil {
						ensuredSeries[sdir] = seriesItem
						seenItems[seriesItem.ID] = true
					}
				}
				for sk, pSeas := range preparedSeasonMap {
					seriesItem := ensuredSeries[sk.seriesDir]
					if seriesItem == nil {
						continue
					}
					seasonItem, err := s.ensureSeasonTx(tx, &lib, seriesItem, pSeas)
					if err == nil && seasonItem != nil {
						ensuredSeasons[sk] = seasonItem
						seenItems[seasonItem.ID] = true
					}
				}
				return nil
			})
		}

		const batchSize = 64
		written := 0
		for start := 0; start < len(prepared); start += batchSize {
			if ctx.Err() != nil {
				scanErr = ctx.Err()
				break
			}
			end := start + batchSize
			if end > len(prepared) {
				end = len(prepared)
			}
			chunk := prepared[start:end]
			_ = s.db.Gorm.Transaction(func(tx *gorm.DB) error {
				for _, p := range chunk {
					if p == nil {
						continue
					}
					var itemID string
					var added bool
					var err error
					if p.isTV {
						seriesItem := ensuredSeries[p.seriesDir]
						seasonItem := ensuredSeasons[seasonKey{seriesDir: p.seriesDir, seasonDir: p.seasonDir, seasonNum: p.seasonNum}]
						if seriesItem == nil || seasonItem == nil {
							continue
						}
						itemID, added, err = s.writeEpisodeTx(tx, &lib, seriesItem, seasonItem, p, runID)
					} else {
						itemID, added, err = s.writeMovieTx(tx, &lib, p, runID, personCache)
					}
					if err != nil {
						slog.Warn("failed to ingest media file", "path", p.cand.path, "err", err)
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
					written++
				}
				return nil
			})
			if report != nil {
				overall := stats.ItemsSkipped + stats.ItemsMoved + written
				if overall > len(candidates) {
					overall = len(candidates)
				}
				report(overall, len(candidates), fmt.Sprintf("批量入库 %s (%d/%d)", lib.Name, overall, len(candidates)))
			}
		}
	}

	// Stage 7: Soft-delete items whose files truly disappeared (batched in transaction).
	if scanErr == nil {
		removed := s.cleanupMissing(&lib, existingScanFiles, seenPaths, seenItems)
		stats.ItemsRemoved = removed
	}

	// Stage 8: Auto-group cross-folder TV series & auto-merge multi-version movies/episodes.
	mergedTotal := 0
	if scanErr == nil && (lib.AutoMergeVersions || lib.AutoGroupSeries) && (stats.ItemsAdded > 0 || stats.ItemsUpdated > 0 || stats.ItemsMoved > 0 || stats.ItemsRemoved > 0 || lib.LastScannedAt == nil) {
		if cStats, cErr := ConsolidateLibrary(s.db, lib.ID); cErr != nil {
			slog.Warn("library consolidation warning", "library", lib.Name, "err", cErr)
		} else if cStats != nil {
			stats.SeriesMerged = cStats.SeriesMerged
			stats.SeasonsMerged = cStats.SeasonsMerged
			stats.MoviesMerged = cStats.MoviesMerged
			stats.EpisodesMerged = cStats.EpisodesMerged
			mergedTotal = cStats.TotalMerged()
		}
	}

	var totalCount int64
	s.db.Read().Model(&model.Item{}).
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

	// Only bump library revision if something actually changed or on initial scan.
	if stats.ItemsAdded > 0 || stats.ItemsUpdated > 0 || stats.ItemsMoved > 0 || stats.ItemsRemoved > 0 || mergedTotal > 0 || lib.LastScannedAt == nil {
		s.db.BumpLibraryRevision()
	}

	if report != nil {
		msg := fmt.Sprintf("完成 (%dms)：新增 %d，移动 %d，更新 %d，跳过 %d，移除 %d",
			stats.DurationMs, stats.ItemsAdded, stats.ItemsMoved, stats.ItemsUpdated, stats.ItemsSkipped, stats.ItemsRemoved)
		if mergedTotal > 0 {
			msg += fmt.Sprintf("，自动合并 %d", mergedTotal)
		}
		report(len(candidates), len(candidates), msg)
	}

	slog.Info("library scan finished",
		"library", lib.Name,
		"workers", workers,
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

func snapshotSingleDir(dir string) *dirSnapshot {
	snap := &dirSnapshot{
		dir:           dir,
		imagesByLower: make(map[string]fileMeta),
		nfosByLower:   make(map[string]string),
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return snap
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			if strings.EqualFold(name, ".actors") {
				if aEntries, aErr := os.ReadDir(filepath.Join(dir, ".actors")); aErr == nil {
					snap.actorsByLower = make(map[string]string, len(aEntries))
					for _, ae := range aEntries {
						if !ae.IsDir() && IsImageFile(ae.Name()) {
							snap.actorsByLower[strings.ToLower(ae.Name())] = filepath.Join(dir, ".actors", ae.Name())
						}
					}
				}
			}
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		fullPath := filepath.Join(dir, name)
		if ext == ".nfo" {
			snap.nfosByLower[strings.ToLower(name)] = fullPath
		} else if imageExts[ext] {
			if fi, err := e.Info(); err == nil {
				snap.imagesByLower[strings.ToLower(name)] = fileMeta{
					name: name, path: fullPath, size: fi.Size(), mtime: fi.ModTime().Unix(),
				}
			}
		}
	}
	return snap
}

func (s *Service) prepareCandidate(ctx context.Context, lib *model.Library, cand scanCandidate, snap *dirSnapshot) *preparedIngest {
	dir := cand.dir
	fileName := cand.name
	stem := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	parentName := filepath.Base(dir)

	depHash := ""
	if snap != nil {
		depHash = snap.depHash
	}

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

	p := &preparedIngest{
		cand:        cand,
		depHash:     depHash,
		isTV:        isTV,
		stem:        stem,
		snap:        snap,
		providerIDs: map[string]string{},
	}

	if isTV {
		seasonNum := ParseSeasonDir(parentName)
		var seriesDir, seasonDir string
		if seasonNum >= 0 {
			seasonDir = dir
			seriesDir = filepath.Dir(dir)
		} else {
			seriesDir = dir
			seasonDir = dir
		}
		if !isSubPath(cand.root, seriesDir) || filepath.Clean(seriesDir) == filepath.Clean(cand.root) {
			seriesDir = dir
			seasonDir = dir
		}
		p.seriesDir = seriesDir
		p.seasonDir = seasonDir

		parsed := ParseEpisodeName(fileName, parentName)
		epNFO, hasEpNFO := snap.readNFO(stem + ".nfo")
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
		p.seasonNum = parsed.Season
		p.episodeNum = parsed.Episode
		p.nfo = epNFO
		p.hasNFO = hasEpNFO

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
		p.title = epTitle
		p.sortTitle = fmt.Sprintf("%03d-%04d-%s", parsed.Season, parsed.Episode, BuildSortName(epTitle))
		p.pinyin = PinyinInitials(epTitle)
		if hasEpNFO {
			p.overview = epNFO.EffectivePlot()
			p.premiereDate = epNFO.EffectivePremiereDate()
			p.rating = epNFO.EffectiveRating()
			if secs := epNFO.EffectiveDurationSeconds(); secs > 0 {
				p.runtimeTicks = int64(secs * 1e7)
			}
			p.providerIDs = epNFO.ProviderIDs()
		}
		p.images = snap.discoverEpisodeThumb(stem)
		p.subtitles = snap.findSidecarSubtitles(stem)
		p.mediaSource = s.prepareMediaSource(ctx, cand.path, "", epNFO)
		if p.runtimeTicks == 0 && p.mediaSource.durationSecs > 0 {
			p.runtimeTicks = int64(p.mediaSource.durationSecs * 1e7)
		}
		return p
	}

	// Movie branch
	parsed := ParseMovieName(fileName, parentName)
	nfo, hasNFO := snap.readNFO(stem+".nfo", "movie.nfo")
	p.nfo = nfo
	p.hasNFO = hasNFO
	p.title = parsed.Title
	p.year = parsed.Year
	if hasNFO {
		if t := strings.TrimSpace(nfo.Title); t != "" {
			p.title = t
		}
		p.origTitle = strings.TrimSpace(nfo.OriginalTitle)
		p.sortTitle = strings.TrimSpace(nfo.SortTitle)
		p.overview = nfo.EffectivePlot()
		p.tagline = strings.TrimSpace(nfo.Tagline)
		if y := nfo.EffectiveYear(); y > 0 {
			p.year = y
		}
		p.premiereDate = nfo.EffectivePremiereDate()
		p.rating = nfo.EffectiveRating()
		p.officialRat = strings.TrimSpace(nfo.MPAA)
		if secs := nfo.EffectiveDurationSeconds(); secs > 0 {
			p.runtimeTicks = int64(secs * 1e7)
		}
		p.providerIDs = nfo.ProviderIDs()
		p.studios = nfo.Studios
		p.genres = nfo.Genres
		p.tags = nfo.Tags
	}
	if p.sortTitle == "" {
		p.sortTitle = BuildSortName(p.title)
	}
	p.pinyin = PinyinInitials(p.title + " " + p.origTitle)
	p.images = snap.discoverImages(stem)
	p.subtitles = snap.findSidecarSubtitles(stem)
	p.mediaSource = s.prepareMediaSource(ctx, cand.path, parsed.VersionLabel, nfo)
	if p.runtimeTicks == 0 && p.mediaSource.durationSecs > 0 {
		p.runtimeTicks = int64(p.mediaSource.durationSecs * 1e7)
	}
	return p
}

func (s *Service) prepareMediaSource(ctx context.Context, filePath, versionLabel string, nfo *NFO) preparedMediaSource {
	ms := preparedMediaSource{
		sourceType:   "local",
		versionLabel: versionLabel,
	}
	if IsSTRMFile(filePath) {
		ms.sourceType = "strm"
		ms.strmURL = ReadSTRMTarget(filePath)
	}
	ms.container = ContainerFromPath(filePath, ms.strmURL)

	// Fast pure-Go container header probe only during scan (never spawns slow external ffprobe).
	if ms.sourceType == "local" && s.prober != nil {
		if res, err := s.prober.ProbeLocalNativeOnly(ctx, filePath); err == nil && res != nil {
			now := time.Now().UTC()
			ms.container = res.Container
			ms.videoCodec = res.VideoCodec
			ms.hdrType = res.HDRType
			ms.width = res.Width
			ms.height = res.Height
			ms.durationSecs = res.DurationSecs
			ms.bitrate = res.Bitrate
			ms.probeJSON = mustJSON(res.Streams)
			ms.probedAt = &now
		}
	}

	if ms.probeJSON == "" && nfo != nil {
		sd := nfo.FileInfo.StreamDetails
		var probeStreams []map[string]any
		idx := 0
		for _, v := range sd.Video {
			if ms.videoCodec == "" {
				ms.videoCodec = v.Codec
				ms.width = v.Width
				ms.height = v.Height
			}
			if v.Duration > 0 && ms.durationSecs == 0 {
				ms.durationSecs = v.Duration
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
		if ms.durationSecs == 0 {
			ms.durationSecs = nfo.EffectiveDurationSeconds()
		}
		if len(probeStreams) > 0 {
			ms.probeJSON = mustJSON(probeStreams)
		}
	}
	return ms
}

func prepareSeriesData(seriesDir string, snap *dirSnapshot) *preparedSeries {
	dirName := filepath.Base(seriesDir)
	title, year := extractTitleAndYear(dirName)
	if title == "" {
		title = dirName
	}
	nfo, hasNFO := snap.readNFO("tvshow.nfo")
	ps := &preparedSeries{
		dir:         seriesDir,
		title:       title,
		year:        year,
		providerIDs: map[string]string{},
		nfo:         nfo,
		hasNFO:      hasNFO,
		snap:        snap,
	}
	if hasNFO {
		if t := strings.TrimSpace(nfo.Title); t != "" {
			ps.title = t
		}
		ps.origTitle = strings.TrimSpace(nfo.OriginalTitle)
		ps.sortTitle = strings.TrimSpace(nfo.SortTitle)
		ps.overview = nfo.EffectivePlot()
		ps.tagline = strings.TrimSpace(nfo.Tagline)
		if y := nfo.EffectiveYear(); y > 0 {
			ps.year = y
		}
		ps.premiereDate = nfo.EffectivePremiereDate()
		ps.rating = nfo.EffectiveRating()
		ps.officialRat = strings.TrimSpace(nfo.MPAA)
		ps.providerIDs = nfo.ProviderIDs()
		ps.studios = nfo.Studios
		ps.genres = nfo.Genres
		ps.tags = nfo.Tags
	}
	if ps.sortTitle == "" {
		ps.sortTitle = BuildSortName(ps.title)
	}
	ps.pinyin = PinyinInitials(ps.title + " " + ps.origTitle)
	ps.images = snap.discoverImages("")
	return ps
}

func prepareSeasonData(seriesDir, seasonDir string, seasonNum int, seasonSnap, seriesSnap *dirSnapshot) *preparedSeason {
	name := fmt.Sprintf("第 %d 季", seasonNum)
	if seasonNum == 0 {
		name = "特别篇"
	}
	overview := ""
	if nfo, ok := seasonSnap.readNFO("season.nfo"); ok {
		if t := strings.TrimSpace(nfo.Title); t != "" {
			name = t
		}
		overview = nfo.EffectivePlot()
	}
	return &preparedSeason{
		seriesDir: seriesDir,
		seasonDir: seasonDir,
		seasonNum: seasonNum,
		name:      name,
		overview:  overview,
		poster:    discoverSeasonPosterFromSnapshots(seasonSnap, seriesSnap, seasonNum),
	}
}

// --- Transactional DB Writers ---

func (s *Service) relocateMovedItemTx(tx *gorm.DB, lib *model.Library, oldSF *model.ScanFile, cand scanCandidate, snap *dirSnapshot, depHash, runID string) bool {
	var item model.Item
	if err := tx.Where("id = ?", oldSF.ItemID).First(&item).Error; err != nil {
		return false
	}
	var srcCount int64
	tx.Model(&model.MediaSource{}).Where("item_id = ?", item.ID).Count(&srcCount)
	prefix := "movie"
	if item.Type == "Episode" {
		prefix = "episode"
	}
	updates := map[string]any{"soft_deleted_at": nil}
	if srcCount <= 1 || item.ScanKey == fmt.Sprintf("%s:%s:%s", prefix, lib.ID, filepath.Clean(oldSF.Path)) {
		updates["scan_key"] = fmt.Sprintf("%s:%s:%s", prefix, lib.ID, filepath.Clean(cand.path))
		updates["dir_path"] = cand.dir
	}
	if err := tx.Model(&item).Updates(updates).Error; err != nil {
		return false
	}

	var src model.MediaSource
	if err := tx.Where("item_id = ? AND path = ?", item.ID, oldSF.Path).First(&src).Error; err == nil {
		src.Path = cand.path
		src.FileMtime = cand.mtime
		if IsSTRMFile(cand.path) {
			src.StrmURL = ReadSTRMTarget(cand.path)
		}
		_ = tx.Save(&src).Error
		stem := strings.TrimSuffix(cand.name, filepath.Ext(cand.name))
		s.syncSidecarSubtitlesTx(tx, item.ID, src.ID, stem, snap.findSidecarSubtitles(stem))
		if item.Type == "Movie" {
			s.syncImagesTx(tx, item.ID, snap.discoverImages(stem))
		}
	}

	_ = tx.Where("library_id = ? AND path = ?", lib.ID, oldSF.Path).Delete(&model.ScanFile{}).Error
	s.upsertScanFileTx(tx, lib.ID, cand, depHash, item.ID, runID)
	slog.Info("detected moved media file, preserved item id", "item_id", item.ID, "from", oldSF.Path, "to", cand.path)
	return true
}

func (s *Service) ensureSeriesTx(tx *gorm.DB, lib *model.Library, ps *preparedSeries, personCache map[string]string) (*model.Item, error) {
	scanKey := fmt.Sprintf("series:%s:%s", lib.ID, filepath.Clean(ps.dir))
	id := StableID("item", scanKey)
	now := time.Now().UTC()

	var existing model.Item
	err := tx.Where("scan_key = ?", scanKey).First(&existing).Error
	series := model.Item{
		ID:              id,
		LibraryID:       lib.ID,
		ParentID:        lib.ID,
		Type:            "Series",
		Name:            ps.title,
		OriginalName:    ps.origTitle,
		SortName:        ps.sortTitle,
		PinyinInitials:  ps.pinyin,
		Overview:        ps.overview,
		Tagline:         ps.tagline,
		Year:            ps.year,
		PremiereDate:    ps.premiereDate,
		CommunityRating: ps.rating,
		OfficialRating:  ps.officialRat,
		ProviderIDsJSON: mustJSON(ps.providerIDs),
		StudiosJSON:     mustJSON(ps.studios),
		DirPath:         ps.dir,
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
		s.applyLockedFieldsTx(tx, &series, &existing)
	} else {
		series.CreatedAt = now
	}

	if err := tx.Save(&series).Error; err != nil {
		return nil, err
	}
	s.syncFTSTx(tx, &series)
	s.syncImagesTx(tx, series.ID, ps.images)
	locks := s.loadLockedFieldsTx(tx, series.ID)
	if !locks["genres"] {
		s.syncGenresAndTagsTx(tx, series.ID, ps.genres, ps.tags)
	}
	if ps.hasNFO {
		s.syncPeopleTx(tx, series.ID, ps.dir, ps.snap, ps.nfo, personCache)
	}
	return &series, nil
}

func (s *Service) ensureSeasonTx(tx *gorm.DB, lib *model.Library, series *model.Item, pSeas *preparedSeason) (*model.Item, error) {
	scanKey := fmt.Sprintf("season:%s:%s:%d", lib.ID, series.ID, pSeas.seasonNum)
	id := StableID("item", scanKey)
	now := time.Now().UTC()

	var existing model.Item
	err := tx.Where("scan_key = ?", scanKey).First(&existing).Error
	season := model.Item{
		ID:            id,
		LibraryID:     lib.ID,
		ParentID:      series.ID,
		SeriesID:      series.ID,
		Type:          "Season",
		Name:          pSeas.name,
		SortName:      fmt.Sprintf("%03d", pSeas.seasonNum),
		Overview:      pSeas.overview,
		Year:          series.Year,
		SeasonNum:     pSeas.seasonNum,
		DirPath:       pSeas.seasonDir,
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
	if err := tx.Save(&season).Error; err != nil {
		return nil, err
	}
	if pSeas.poster != nil {
		s.syncImagesTx(tx, season.ID, map[string]fileMeta{"Primary": *pSeas.poster})
	}
	return &season, nil
}

func (s *Service) writeMovieTx(tx *gorm.DB, lib *model.Library, p *preparedIngest, runID string, personCache map[string]string) (string, bool, error) {
	scanKey := fmt.Sprintf("movie:%s:%s", lib.ID, filepath.Clean(p.cand.path))
	itemID := StableID("item", scanKey)
	now := time.Now().UTC()

	var existing model.Item
	err := tx.Where("scan_key = ?", scanKey).First(&existing).Error
	added := errors.Is(err, gorm.ErrRecordNotFound)

	item := model.Item{
		ID:              itemID,
		LibraryID:       lib.ID,
		ParentID:        lib.ID,
		Type:            "Movie",
		Name:            p.title,
		OriginalName:    p.origTitle,
		SortName:        p.sortTitle,
		PinyinInitials:  p.pinyin,
		Overview:        p.overview,
		Tagline:         p.tagline,
		Year:            p.year,
		PremiereDate:    p.premiereDate,
		CommunityRating: p.rating,
		OfficialRating:  p.officialRat,
		RuntimeTicks:    p.runtimeTicks,
		ProviderIDsJSON: mustJSON(p.providerIDs),
		StudiosJSON:     mustJSON(p.studios),
		DirPath:         p.cand.dir,
		ScanKey:         scanKey,
		RandomKey:       StableID("rnd", scanKey)[:12],
		AddedAt:         time.Unix(p.cand.mtime, 0).UTC(),
		SoftDeletedAt:   nil,
	}
	if !added {
		item.ID = existing.ID
		item.AddedAt = existing.AddedAt
		item.CreatedAt = existing.CreatedAt
		item.RandomKey = existing.RandomKey
		s.applyLockedFieldsTx(tx, &item, &existing)
	} else {
		item.CreatedAt = now
	}

	if err := tx.Save(&item).Error; err != nil {
		return "", false, err
	}

	s.syncFTSTx(tx, &item)
	sourceID := s.saveMediaSourceTx(tx, &item, p.cand, p.mediaSource)
	s.syncSidecarSubtitlesTx(tx, item.ID, sourceID, p.stem, p.subtitles)
	s.syncImagesTx(tx, item.ID, p.images)
	locks := s.loadLockedFieldsTx(tx, item.ID)
	if !locks["genres"] {
		s.syncGenresAndTagsTx(tx, item.ID, p.genres, p.tags)
	}
	if p.hasNFO {
		s.syncPeopleTx(tx, item.ID, p.cand.dir, p.snap, p.nfo, personCache)
	}
	s.upsertScanFileTx(tx, lib.ID, p.cand, p.depHash, item.ID, runID)
	return item.ID, added, nil
}

func (s *Service) writeEpisodeTx(tx *gorm.DB, lib *model.Library, seriesItem, seasonItem *model.Item, p *preparedIngest, runID string) (string, bool, error) {
	scanKey := fmt.Sprintf("episode:%s:%s", lib.ID, filepath.Clean(p.cand.path))
	itemID := StableID("item", scanKey)
	now := time.Now().UTC()

	var existing model.Item
	err := tx.Where("scan_key = ?", scanKey).First(&existing).Error
	added := errors.Is(err, gorm.ErrRecordNotFound)

	epItem := model.Item{
		ID:              itemID,
		LibraryID:       lib.ID,
		ParentID:        seasonItem.ID,
		SeriesID:        seriesItem.ID,
		SeasonID:        seasonItem.ID,
		Type:            "Episode",
		Name:            p.title,
		SortName:        p.sortTitle,
		PinyinInitials:  p.pinyin,
		Overview:        p.overview,
		Year:            seriesItem.Year,
		PremiereDate:    p.premiereDate,
		SeasonNum:       p.seasonNum,
		EpisodeNum:      p.episodeNum,
		CommunityRating: p.rating,
		RuntimeTicks:    p.runtimeTicks,
		ProviderIDsJSON: mustJSON(p.providerIDs),
		DirPath:         p.cand.dir,
		ScanKey:         scanKey,
		RandomKey:       StableID("rnd", scanKey)[:12],
		AddedAt:         time.Unix(p.cand.mtime, 0).UTC(),
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

	if err := tx.Save(&epItem).Error; err != nil {
		return "", false, err
	}

	s.syncFTSTx(tx, &epItem)
	sourceID := s.saveMediaSourceTx(tx, &epItem, p.cand, p.mediaSource)
	s.syncSidecarSubtitlesTx(tx, epItem.ID, sourceID, p.stem, p.subtitles)
	s.syncImagesTx(tx, epItem.ID, p.images)
	s.upsertScanFileTx(tx, lib.ID, p.cand, p.depHash, epItem.ID, runID)
	return epItem.ID, added, nil
}

func (s *Service) saveMediaSourceTx(tx *gorm.DB, item *model.Item, cand scanCandidate, ms preparedMediaSource) string {
	sourceID := StableID("src", cand.path)
	src := model.MediaSource{
		ID:           sourceID,
		ItemID:       item.ID,
		SourceType:   ms.sourceType,
		Path:         cand.path,
		StrmURL:      ms.strmURL,
		FileSize:     cand.size,
		FileMtime:    cand.mtime,
		Fingerprint:  Fingerprint(cand.path, cand.size),
		VersionLabel: ms.versionLabel,
		Container:    ms.container,
		VideoCodec:   ms.videoCodec,
		Width:        ms.width,
		Height:       ms.height,
		HDRType:      ms.hdrType,
		DurationSecs: ms.durationSecs,
		Bitrate:      ms.bitrate,
		ProbeJSON:    ms.probeJSON,
		ProbedAt:     ms.probedAt,
	}
	_ = tx.Save(&src).Error
	return sourceID
}

func (s *Service) syncSidecarSubtitlesTx(tx *gorm.DB, itemID, sourceID, mediaStem string, subs []fileMeta) {
	_ = tx.Where("media_source_id = ? AND is_external = ?", sourceID, true).Delete(&model.MediaSubtitle{}).Error
	if len(subs) == 0 {
		return
	}
	idx := 100
	batch := make([]model.MediaSubtitle, 0, len(subs))
	for _, sub := range subs {
		codec, ok := SubtitleCodec(sub.name)
		if !ok {
			continue
		}
		lang, title, isDef, isForced := ParseSubtitleMeta(mediaStem, sub.name)
		batch = append(batch, model.MediaSubtitle{
			ID:            StableID("sub", sub.path),
			MediaSourceID: sourceID,
			ItemID:        itemID,
			StreamIndex:   idx,
			IsExternal:    true,
			Path:          sub.path,
			Language:      lang,
			Title:         title,
			Codec:         codec,
			IsDefault:     isDef,
			IsForced:      isForced,
		})
		idx++
	}
	if len(batch) > 0 {
		_ = tx.Create(&batch).Error
	}
}

func (s *Service) syncImagesTx(tx *gorm.DB, itemID string, images map[string]fileMeta) {
	if len(images) == 0 {
		return
	}
	now := time.Now().UTC()
	for kind, fm := range images {
		tag := StableID("imgtag", fmt.Sprintf("%s|%d|%d", fm.path, fm.size, fm.mtime))[:16]
		img := model.ItemImage{
			ID:         StableID("img", itemID+"|"+kind+"|0"),
			ItemID:     itemID,
			Kind:       kind,
			IndexNum:   0,
			SourceType: "local",
			SourceRef:  fm.path,
			Tag:        tag,
			UpdatedAt:  now,
		}
		_ = tx.Save(&img).Error
	}
}

func (s *Service) syncGenresAndTagsTx(tx *gorm.DB, itemID string, genres, tags []string) {
	_ = tx.Where("item_id = ?", itemID).Delete(&model.ItemGenre{}).Error
	if len(genres) > 0 {
		seenG := make(map[string]bool, len(genres))
		batchG := make([]model.ItemGenre, 0, len(genres))
		for _, g := range genres {
			g = strings.TrimSpace(g)
			if g == "" || seenG[g] {
				continue
			}
			seenG[g] = true
			batchG = append(batchG, model.ItemGenre{ItemID: itemID, Genre: g})
		}
		if len(batchG) > 0 {
			_ = tx.Create(&batchG).Error
		}
	}

	_ = tx.Where("item_id = ?", itemID).Delete(&model.ItemTag{}).Error
	if len(tags) > 0 {
		seenT := make(map[string]bool, len(tags))
		batchT := make([]model.ItemTag, 0, len(tags))
		for _, t := range tags {
			t = strings.TrimSpace(t)
			if t == "" || seenT[t] {
				continue
			}
			seenT[t] = true
			batchT = append(batchT, model.ItemTag{ItemID: itemID, Tag: t})
		}
		if len(batchT) > 0 {
			_ = tx.Create(&batchT).Error
		}
	}
}

func (s *Service) syncPeopleTx(tx *gorm.DB, itemID, dir string, snap *dirSnapshot, nfo *NFO, personCache map[string]string) {
	_ = tx.Where("item_id = ?", itemID).Delete(&model.ItemPerson{}).Error
	if nfo == nil || (len(nfo.Actors) == 0 && len(nfo.Directors) == 0) {
		return
	}

	now := time.Now().UTC()
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
		if localActor := snap.findLocalActorThumb(name); localActor != "" {
			thumb = localActor
		}

		// Skip redundant DB write if we already synced this person in this scan run with no better thumb.
		if cachedThumb, ok := personCache[pid]; ok {
			if thumb == "" || cachedThumb == thumb || (cachedThumb != "" && !filepath.IsAbs(thumb)) {
				return pid
			}
		}

		var existing model.Person
		if err := tx.Where("id = ?", pid).First(&existing).Error; err == nil {
			updates := map[string]any{"name": name, "updated_at": now}
			finalThumb := existing.ThumbPath
			if thumb != "" && (existing.ThumbPath == "" || filepath.IsAbs(thumb)) {
				updates["thumb_path"] = thumb
				finalThumb = thumb
			}
			_ = tx.Model(&existing).Updates(updates).Error
			personCache[pid] = finalThumb
		} else {
			_ = tx.Create(&model.Person{
				ID:        pid,
				Name:      name,
				ThumbPath: thumb,
				UpdatedAt: now,
			}).Error
			personCache[pid] = thumb
		}
		return pid
	}

	seenLink := map[string]bool{}
	idx := 0
	for _, a := range nfo.Actors {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			continue
		}
		pid := upsertPerson(name, a.Thumb)
		k := pid + "|Actor"
		if seenLink[k] {
			continue
		}
		seenLink[k] = true
		_ = tx.Save(&model.ItemPerson{
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
		k := pid + "|Director"
		if seenLink[k] {
			continue
		}
		seenLink[k] = true
		_ = tx.Save(&model.ItemPerson{
			ItemID:   itemID,
			PersonID: pid,
			RoleType: "Director",
			SortIdx:  idx,
		}).Error
		idx++
	}
}

func (s *Service) syncFTSTx(tx *gorm.DB, item *model.Item) {
	if !s.db.HasFTS5 {
		return
	}
	_ = tx.Exec("DELETE FROM items_fts WHERE item_id = ?", item.ID).Error
	_ = tx.Exec("INSERT INTO items_fts(item_id, name, original_name, pinyin_initials) VALUES (?, ?, ?, ?)",
		item.ID, item.Name, item.OriginalName, item.PinyinInitials).Error
}

func (s *Service) upsertScanFileTx(tx *gorm.DB, libID string, cand scanCandidate, depHash, itemID, runID string) {
	_ = tx.Save(&model.ScanFile{
		LibraryID:   libID,
		Path:        cand.path,
		DirPath:     cand.dir,
		Size:        cand.size,
		Mtime:       cand.mtime,
		Fingerprint: Fingerprint(cand.path, cand.size),
		DepHash:     depHash,
		ItemID:      itemID,
		LastScanRun: runID,
	}).Error
}

func (s *Service) cleanupMissing(lib *model.Library, oldFiles []model.ScanFile, seenPaths, seenItems map[string]bool) int {
	var vanished []model.ScanFile
	for _, sf := range oldFiles {
		if !seenPaths[sf.Path] {
			vanished = append(vanished, sf)
		}
	}
	if len(vanished) == 0 {
		return 0
	}
	removed := 0
	now := time.Now().UTC()
	_ = s.db.Gorm.Transaction(func(tx *gorm.DB) error {
		for _, sf := range vanished {
			_ = tx.Where("library_id = ? AND path = ?", lib.ID, sf.Path).Delete(&model.ScanFile{}).Error
			var deadSrcs []model.MediaSource
			if tx.Where("path = ?", sf.Path).Find(&deadSrcs).Error == nil && len(deadSrcs) > 0 {
				deadIDs := make([]string, len(deadSrcs))
				for i, ds := range deadSrcs {
					deadIDs[i] = ds.ID
				}
				_ = tx.Where("media_source_id IN ?", deadIDs).Delete(&model.MediaSubtitle{}).Error
			}
			_ = tx.Where("path = ?", sf.Path).Delete(&model.MediaSource{}).Error
			if sf.ItemID != "" && !seenItems[sf.ItemID] {
				_ = tx.Model(&model.Item{}).Where("id = ?", sf.ItemID).Update("soft_deleted_at", now).Error
				removed++
			}
		}
		return nil
	})
	return removed
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

func (s *Service) loadLockedFieldsTx(tx *gorm.DB, itemID string) map[string]bool {
	out := map[string]bool{}
	var lock model.ItemMetadataLock
	if err := tx.Where("item_id = ?", itemID).First(&lock).Error; err != nil {
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

func (s *Service) applyLockedFieldsTx(tx *gorm.DB, next, existing *model.Item) {
	locks := s.loadLockedFieldsTx(tx, existing.ID)
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
