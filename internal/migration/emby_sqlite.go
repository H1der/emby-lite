package migration

import (
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"emby-lite/internal/auth"
	"emby-lite/internal/database"
	"emby-lite/internal/model"
	"emby-lite/internal/scanner"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// PathMapping rewrites an old Emby path prefix into the new emby-lite path prefix.
type PathMapping struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// EmbyMigrationRequest points to an existing Emby server's config directory or specific SQLite/root paths.
type EmbyMigrationRequest struct {
	EmbyConfigDir   string        `json:"emby_config_dir"` // optional: auto-discovers data/users.db, data/library.db, root/default
	UsersDBPath     string        `json:"users_db_path"`
	LibraryDBPath   string        `json:"library_db_path"`
	RootDefaultDir  string        `json:"root_default_dir"`
	PathMappings    []PathMapping `json:"path_mappings"`
	DefaultPassword string        `json:"default_password"` // assigned to newly imported users
	ImportLibraries *bool         `json:"import_libraries"`
	ImportItems     *bool         `json:"import_items"`
	ImportUsers     *bool         `json:"import_users"`
	ImportWatch     *bool         `json:"import_watch"`
	Overwrite       bool          `json:"overwrite"`
}

func boolOrDefault(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

type LibraryPreview struct {
	Name   string   `json:"name"`
	Kind   string   `json:"kind"`
	Paths  []string `json:"paths"`
	Exists bool     `json:"exists"`
}

type ResolvedSources struct {
	UsersDBPath    string `json:"users_db_path"`
	LibraryDBPath  string `json:"library_db_path"`
	RootDefaultDir string `json:"root_default_dir"`
}

type PreviewReport struct {
	Resolved             ResolvedSources  `json:"resolved"`
	LibrariesFound       int              `json:"libraries_found"`
	NewLibrariesToCreate int              `json:"new_libraries_to_create"`
	Libraries            []LibraryPreview `json:"libraries"`
	ItemsFound           int              `json:"items_found"`
	MoviesFound          int              `json:"movies_found"`
	SeriesFound          int              `json:"series_found"`
	SeasonsFound         int              `json:"seasons_found"`
	EpisodesFound        int              `json:"episodes_found"`
	UsersFound           int              `json:"users_found"`
	NewUsersToCreate     int              `json:"new_users_to_create"`
	WatchRecordsFound    int              `json:"watch_records_found"`
	MatchedWatchRecords  int              `json:"matched_watch_records"`
	SampleUsers          []string         `json:"sample_users"`
	Warnings             []string         `json:"warnings,omitempty"`
}

type ImportReport struct {
	LibrariesCreated  int      `json:"libraries_created"`
	LibrariesExisting int      `json:"libraries_existing"`
	ItemsImported     int      `json:"items_imported"`
	ItemsSkipped      int      `json:"items_skipped"`
	UsersCreated      int      `json:"users_created"`
	UsersExisting     int      `json:"users_existing"`
	WatchImported     int      `json:"watch_imported"`
	WatchSkipped      int      `json:"watch_skipped"`
	Warnings          []string `json:"warnings,omitempty"`
}

type Service struct {
	db *database.DB
}

func NewService(db *database.DB) *Service {
	return &Service{db: db}
}

type embyLibraryDef struct {
	EmbyID string
	Name   string
	Kind   string // movies | tvshows | mixed
	Paths  []string
}

type embyItemRow struct {
	EmbyID            string
	ParentID          string
	TopParentID       string
	SeriesID          string
	SeasonID          string
	UserDataKey       string
	ItemType          string // Movie | Series | Season | Episode
	Name              string
	OriginalTitle     string
	SortName          string
	Overview          string
	Tagline           string
	Year              int
	PremiereDate      string
	CommunityRating   float64
	OfficialRating    string
	RunTimeTicks      int64
	IndexNumber       int
	ParentIndexNumber int
	Path              string
	ProviderIDs       map[string]string
	Genres            []string
	Studios           []string
}

type embyUserRow struct {
	ID       string
	Username string
}

type embyWatchRow struct {
	UserID        string
	Username      string
	UserDataKey   string
	ItemName      string
	ItemPath      string
	Played        bool
	PositionTicks int64
	PlayCount     int
	IsFavorite    bool
	LastPlayed    *time.Time
}

// resolveSources auto-discovers users.db, library.db, and root/default from EmbyConfigDir if explicit paths are empty.
func resolveSources(req EmbyMigrationRequest) ResolvedSources {
	res := ResolvedSources{
		UsersDBPath:    strings.TrimSpace(req.UsersDBPath),
		LibraryDBPath:  strings.TrimSpace(req.LibraryDBPath),
		RootDefaultDir: strings.TrimSpace(req.RootDefaultDir),
	}
	cfgDir := strings.TrimSpace(req.EmbyConfigDir)
	if cfgDir == "" {
		return res
	}
	if res.UsersDBPath == "" {
		for _, cand := range []string{
			filepath.Join(cfgDir, "data", "users.db"),
			filepath.Join(cfgDir, "users.db"),
		} {
			if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
				res.UsersDBPath = cand
				break
			}
		}
	}
	if res.LibraryDBPath == "" {
		for _, cand := range []string{
			filepath.Join(cfgDir, "data", "library.db"),
			filepath.Join(cfgDir, "library.db"),
		} {
			if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
				res.LibraryDBPath = cand
				break
			}
		}
	}
	if res.RootDefaultDir == "" {
		for _, cand := range []string{
			filepath.Join(cfgDir, "root", "default"),
			filepath.Join(cfgDir, "default"),
		} {
			if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
				res.RootDefaultDir = cand
				break
			}
		}
	}
	return res
}

func applyPathMappings(raw string, mappings []PathMapping) string {
	p := strings.TrimSpace(raw)
	if p == "" || len(mappings) == 0 {
		return p
	}
	// Normalize slashes for reliable prefix matching across Windows/Linux containers.
	normP := strings.ReplaceAll(p, `\`, "/")
	for _, m := range mappings {
		from := strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(m.From), `\`, "/"), "/")
		to := strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(m.To), `\`, "/"), "/")
		if from == "" {
			continue
		}
		if strings.EqualFold(normP, from) {
			return filepath.FromSlash(to)
		}
		if len(normP) > len(from) && strings.EqualFold(normP[:len(from)], from) && normP[len(from)] == '/' {
			return filepath.FromSlash(to + normP[len(from):])
		}
	}
	return p
}

// Preview inspects existing Emby config & SQLite DBs read-only and returns a complete dry-run report.
func (s *Service) Preview(req EmbyMigrationRequest) (*PreviewReport, error) {
	resolved := resolveSources(req)
	if resolved.UsersDBPath == "" && resolved.LibraryDBPath == "" && resolved.RootDefaultDir == "" {
		return nil, errors.New("请指定 Emby 配置目录，或至少填写 root/default、users.db、library.db 中的一项")
	}

	libs, items, users, watches, warnings, err := s.readAllEmbyData(resolved, req.PathMappings)
	if err != nil {
		return nil, err
	}

	report := &PreviewReport{
		Resolved:          resolved,
		LibrariesFound:    len(libs),
		ItemsFound:        len(items),
		UsersFound:        len(users),
		WatchRecordsFound: len(watches),
		Warnings:          warnings,
	}

	existingLibs := s.loadExistingLibraryMap()
	for _, l := range libs {
		_, exists := existingLibs[strings.ToLower(l.Name)]
		if !exists {
			report.NewLibrariesToCreate++
		}
		report.Libraries = append(report.Libraries, LibraryPreview{
			Name:   l.Name,
			Kind:   l.Kind,
			Paths:  l.Paths,
			Exists: exists,
		})
	}

	itemKeys := map[string]bool{}
	itemNames := map[string]bool{}
	itemBaseNames := map[string]bool{}
	for _, it := range items {
		switch it.ItemType {
		case "Movie":
			report.MoviesFound++
		case "Series":
			report.SeriesFound++
		case "Season":
			report.SeasonsFound++
		case "Episode":
			report.EpisodesFound++
		}
		if it.UserDataKey != "" {
			itemKeys[it.UserDataKey] = true
		}
		if it.Name != "" {
			itemNames[strings.ToLower(it.Name)] = true
		}
		if it.Path != "" {
			itemBaseNames[strings.ToLower(baseFileName(it.Path))] = true
		}
	}

	existingUsers := s.loadExistingUserMap()
	for _, u := range users {
		if len(report.SampleUsers) < 10 {
			report.SampleUsers = append(report.SampleUsers, u.Username)
		}
		if _, ok := existingUsers[strings.ToLower(u.Username)]; !ok {
			report.NewUsersToCreate++
		}
	}

	localMatcher := s.buildLocalItemMatcher()
	importItems := boolOrDefault(req.ImportItems, true)
	for _, w := range watches {
		if localMatcher(w.ItemPath, w.ItemName) != "" {
			report.MatchedWatchRecords++
			continue
		}
		if importItems {
			if (w.UserDataKey != "" && itemKeys[w.UserDataKey]) ||
				(w.ItemPath != "" && itemBaseNames[strings.ToLower(baseFileName(w.ItemPath))]) ||
				(w.ItemName != "" && itemNames[strings.ToLower(strings.TrimSpace(w.ItemName))]) {
				report.MatchedWatchRecords++
			}
		}
	}
	return report, nil
}

// Import migrates Emby libraries, media metadata, users, and watch progress/favorites into emby-lite.
func (s *Service) Import(req EmbyMigrationRequest) (*ImportReport, error) {
	resolved := resolveSources(req)
	if resolved.UsersDBPath == "" && resolved.LibraryDBPath == "" && resolved.RootDefaultDir == "" {
		return nil, errors.New("请指定 Emby 配置目录，或至少填写 root/default、users.db、library.db 中的一项")
	}

	libs, items, users, watches, warnings, err := s.readAllEmbyData(resolved, req.PathMappings)
	if err != nil {
		return nil, err
	}

	wantLibraries := boolOrDefault(req.ImportLibraries, true)
	wantItems := boolOrDefault(req.ImportItems, true)
	wantUsers := boolOrDefault(req.ImportUsers, true)
	wantWatch := boolOrDefault(req.ImportWatch, true)

	report := &ImportReport{Warnings: warnings}

	// 1. Migrate Libraries
	existingLibs := s.loadExistingLibraryMap()
	embyLibIDToLocal := map[string]string{}
	type libPathEntry struct {
		libID string
		path  string
	}
	var allLibPaths []libPathEntry
	var fallbackLibID string

	if wantLibraries {
		for idx, l := range libs {
			lowerName := strings.ToLower(strings.TrimSpace(l.Name))
			if lowerName == "" {
				continue
			}
			var libID string
			if existing, ok := existingLibs[lowerName]; ok {
				libID = existing.ID
				report.LibrariesExisting++
			} else {
				pathsBytes, _ := json.Marshal(l.Paths)
				newLib := model.Library{
					ID:        strings.ReplaceAll(uuid.NewString(), "-", ""),
					Name:      strings.TrimSpace(l.Name),
					Kind:      l.Kind,
					PathsJSON: string(pathsBytes),
					SortOrder: idx,
					Status:    "idle",
				}
				if err := s.db.Gorm.Create(&newLib).Error; err == nil {
					libID = newLib.ID
					existingLibs[lowerName] = newLib
					report.LibrariesCreated++
				}
			}
			if libID != "" {
				if fallbackLibID == "" {
					fallbackLibID = libID
				}
				if l.EmbyID != "" {
					embyLibIDToLocal[l.EmbyID] = libID
				}
				for _, p := range l.Paths {
					allLibPaths = append(allLibPaths, libPathEntry{libID: libID, path: normalizeSlashLower(p)})
				}
			}
		}
	}

	// Also register existing libraries' paths for item-to-library matching.
	for _, ex := range existingLibs {
		if fallbackLibID == "" {
			fallbackLibID = ex.ID
		}
		var lp []string
		if json.Unmarshal([]byte(ex.PathsJSON), &lp) == nil {
			for _, p := range lp {
				allLibPaths = append(allLibPaths, libPathEntry{libID: ex.ID, path: normalizeSlashLower(p)})
			}
		}
	}

	resolveItemLibrary := func(it embyItemRow) string {
		if it.TopParentID != "" {
			if id, ok := embyLibIDToLocal[it.TopParentID]; ok {
				return id
			}
		}
		if it.ParentID != "" {
			if id, ok := embyLibIDToLocal[it.ParentID]; ok {
				return id
			}
		}
		if it.Path != "" {
			norm := normalizeSlashLower(it.Path)
			bestLen := -1
			bestLib := ""
			for _, lp := range allLibPaths {
				if lp.path != "" && strings.HasPrefix(norm, lp.path) && len(lp.path) > bestLen {
					bestLen = len(lp.path)
					bestLib = lp.libID
				}
			}
			if bestLib != "" {
				return bestLib
			}
		}
		return fallbackLibID
	}

	// 2. Migrate Media Items Metadata (Series -> Season -> Movie -> Episode to preserve hierarchy!)
	embyItemToLocal := map[string]string{}
	userDataKeyToLocal := map[string]string{}

	if wantItems && len(items) > 0 {
		if fallbackLibID == "" {
			// Create a default container library if no library existed yet.
			defaultLib := model.Library{
				ID:        strings.ReplaceAll(uuid.NewString(), "-", ""),
				Name:      "Emby 迁移媒体库",
				Kind:      "mixed",
				PathsJSON: `[]`,
				Status:    "idle",
			}
			if err := s.db.Gorm.Create(&defaultLib).Error; err == nil {
				fallbackLibID = defaultLib.ID
				report.LibrariesCreated++
			}
		}

		order := []string{"Series", "Season", "Movie", "Episode"}
		now := time.Now().UTC()
		for _, wantType := range order {
			for _, it := range items {
				if it.ItemType != wantType {
					continue
				}
				libID := resolveItemLibrary(it)
				if libID == "" {
					report.ItemsSkipped++
					continue
				}

				parentID := libID
				seriesID := ""
				seasonID := ""
				switch wantType {
				case "Season":
					if sid := embyItemToLocal[it.SeriesID]; sid != "" {
						seriesID = sid
						parentID = sid
					} else if pid := embyItemToLocal[it.ParentID]; pid != "" {
						seriesID = pid
						parentID = pid
					}
				case "Episode":
					if sid := embyItemToLocal[it.SeriesID]; sid != "" {
						seriesID = sid
					}
					if ssn := embyItemToLocal[it.SeasonID]; ssn != "" {
						seasonID = ssn
						parentID = ssn
					} else if pid := embyItemToLocal[it.ParentID]; pid != "" {
						seasonID = pid
						parentID = pid
					} else if seriesID != "" {
						parentID = seriesID
					}
				}

				scanKey := buildMigratedScanKey(wantType, libID, seriesID, it)
				localID := scanner.StableID("item", scanKey)

				var existing model.Item
				err := s.db.Gorm.Where("scan_key = ?", scanKey).First(&existing).Error
				if err == nil {
					localID = existing.ID
					if !req.Overwrite {
						embyItemToLocal[it.EmbyID] = localID
						if it.UserDataKey != "" {
							userDataKeyToLocal[it.UserDataKey] = localID
						}
						report.ItemsSkipped++
						continue
					}
				}

				sortName := it.SortName
				if sortName == "" {
					sortName = scanner.BuildSortName(it.Name)
				}
				dirPath := ""
				if it.Path != "" {
					if wantType == "Series" || wantType == "Season" {
						dirPath = it.Path
					} else {
						dirPath = filepath.Dir(it.Path)
					}
				}
				pidsJSON, _ := json.Marshal(it.ProviderIDs)
				studiosJSON, _ := json.Marshal(it.Studios)

				seasonNum := it.ParentIndexNumber
				episodeNum := it.IndexNumber
				if wantType == "Season" {
					seasonNum = it.IndexNumber
					episodeNum = 0
				}

				rec := model.Item{
					ID:              localID,
					LibraryID:       libID,
					ParentID:        parentID,
					SeriesID:        seriesID,
					SeasonID:        seasonID,
					Type:            wantType,
					Name:            it.Name,
					OriginalName:    it.OriginalTitle,
					SortName:        sortName,
					PinyinInitials:  scanner.PinyinInitials(it.Name + " " + it.OriginalTitle),
					Overview:        it.Overview,
					Tagline:         it.Tagline,
					Year:            it.Year,
					PremiereDate:    it.PremiereDate,
					SeasonNum:       seasonNum,
					EpisodeNum:      episodeNum,
					CommunityRating: it.CommunityRating,
					OfficialRating:  it.OfficialRating,
					RuntimeTicks:    it.RunTimeTicks,
					ProviderIDsJSON: string(pidsJSON),
					StudiosJSON:     string(studiosJSON),
					DirPath:         dirPath,
					ScanKey:         scanKey,
					RandomKey:       scanner.StableID("rnd", scanKey)[:12],
					AddedAt:         now,
					CreatedAt:       now,
				}
				if err := s.db.Gorm.Save(&rec).Error; err != nil {
					report.ItemsSkipped++
					continue
				}
				if s.db.HasFTS5 {
					_ = s.db.Gorm.Exec("DELETE FROM items_fts WHERE item_id = ?", rec.ID).Error
					_ = s.db.Gorm.Exec("INSERT INTO items_fts(item_id, name, original_name, pinyin_initials) VALUES (?, ?, ?, ?)",
						rec.ID, rec.Name, rec.OriginalName, rec.PinyinInitials).Error
				}
				if len(it.Genres) > 0 {
					_ = s.db.Gorm.Where("item_id = ?", rec.ID).Delete(&model.ItemGenre{}).Error
					for _, g := range it.Genres {
						if gn := strings.TrimSpace(g); gn != "" {
							_ = s.db.Gorm.Create(&model.ItemGenre{ItemID: rec.ID, Genre: gn}).Error
						}
					}
				}

				// Attach MediaSource for playable items (Movie / Episode).
				if (wantType == "Movie" || wantType == "Episode") && it.Path != "" {
					srcType := "local"
					strmURL := ""
					if scanner.IsSTRMFile(it.Path) {
						srcType = "strm"
						strmURL = scanner.ReadSTRMTarget(it.Path)
					}
					var fileSize, fileMtime int64
					if fi, statErr := os.Stat(it.Path); statErr == nil && !fi.IsDir() {
						fileSize = fi.Size()
						fileMtime = fi.ModTime().Unix()
					}
					_ = s.db.Gorm.Save(&model.MediaSource{
						ID:           scanner.StableID("src", it.Path),
						ItemID:       rec.ID,
						SourceType:   srcType,
						Path:         it.Path,
						StrmURL:      strmURL,
						FileSize:     fileSize,
						FileMtime:    fileMtime,
						Fingerprint:  scanner.Fingerprint(it.Path, fileSize),
						Container:    scanner.ContainerFromPath(it.Path, strmURL),
						DurationSecs: float64(it.RunTimeTicks) / 1e7,
					}).Error
				}

				// Discover local posters immediately if remapped path is accessible on disk!
				if dirPath != "" {
					stem := ""
					if wantType == "Movie" || wantType == "Episode" {
						stem = strings.TrimSuffix(filepath.Base(it.Path), filepath.Ext(it.Path))
					}
					for kind, imgPath := range scanner.DiscoverLocalImages(dirPath, stem) {
						if fi, statErr := os.Stat(imgPath); statErr == nil {
							tag := scanner.StableID("imgtag", fmt.Sprintf("%s|%d|%d", imgPath, fi.Size(), fi.ModTime().Unix()))[:16]
							_ = s.db.Gorm.Save(&model.ItemImage{
								ID:         scanner.StableID("img", rec.ID+"|"+kind+"|0"),
								ItemID:     rec.ID,
								Kind:       kind,
								IndexNum:   0,
								SourceType: "local",
								SourceRef:  imgPath,
								Tag:        tag,
								UpdatedAt:  now,
							}).Error
						}
					}
				}

				embyItemToLocal[it.EmbyID] = rec.ID
				if it.UserDataKey != "" {
					userDataKeyToLocal[it.UserDataKey] = rec.ID
				}
				report.ItemsImported++
			}
		}

		// Update library item counts.
		for _, ex := range existingLibs {
			var cnt int64
			s.db.Gorm.Model(&model.Item{}).
				Where("library_id = ? AND soft_deleted_at IS NULL AND type IN ?", ex.ID, []string{"Movie", "Series"}).
				Count(&cnt)
			_ = s.db.Gorm.Model(&model.Library{}).Where("id = ?", ex.ID).Update("item_count", cnt).Error
		}
	}

	// 3. Migrate Users
	defPass := strings.TrimSpace(req.DefaultPassword)
	if len(defPass) < 4 {
		defPass = "EmbyMigrate123!"
	}
	defHash, err := auth.HashPassword(defPass)
	if err != nil {
		return nil, err
	}

	existingUsers := s.loadExistingUserMap()
	embyIDToLocalUserID := map[string]string{}

	if wantUsers {
		for _, u := range users {
			lower := strings.ToLower(strings.TrimSpace(u.Username))
			if lower == "" {
				continue
			}
			if localID, ok := existingUsers[lower]; ok {
				embyIDToLocalUserID[u.ID] = localID
				report.UsersExisting++
				continue
			}
			newUser := model.User{
				ID:           strings.ReplaceAll(uuid.NewString(), "-", ""),
				Username:     strings.TrimSpace(u.Username),
				DisplayName:  strings.TrimSpace(u.Username),
				PasswordHash: defHash,
				IsAdmin:      false,
			}
			if err := s.db.Gorm.Create(&newUser).Error; err == nil {
				existingUsers[lower] = newUser.ID
				embyIDToLocalUserID[u.ID] = newUser.ID
				report.UsersCreated++
			}
		}
	}

	// 4. Migrate Watch Progress & Favorites
	if wantWatch {
		itemMatcher := s.buildLocalItemMatcher()
		now := time.Now().UTC()
		for _, w := range watches {
			localUserID := embyIDToLocalUserID[w.UserID]
			if localUserID == "" && w.Username != "" {
				localUserID = existingUsers[strings.ToLower(strings.TrimSpace(w.Username))]
			}
			if localUserID == "" {
				report.WatchSkipped++
				continue
			}
			localItemID := ""
			if w.UserDataKey != "" {
				localItemID = userDataKeyToLocal[w.UserDataKey]
			}
			if localItemID == "" {
				localItemID = itemMatcher(w.ItemPath, w.ItemName)
			}
			if localItemID == "" {
				report.WatchSkipped++
				continue
			}

			var existingUD model.UserData
			err := s.db.Gorm.Where("user_id = ? AND item_id = ?", localUserID, localItemID).First(&existingUD).Error
			if err == nil && !req.Overwrite {
				existingUD.Played = existingUD.Played || w.Played
				existingUD.IsFavorite = existingUD.IsFavorite || w.IsFavorite
				if w.PositionTicks > existingUD.PositionTicks {
					existingUD.PositionTicks = w.PositionTicks
				}
				if w.PlayCount > existingUD.PlayCount {
					existingUD.PlayCount = w.PlayCount
				}
				existingUD.UpdatedAt = now
				_ = s.db.Gorm.Save(&existingUD).Error
			} else {
				_ = s.db.Gorm.Save(&model.UserData{
					UserID:        localUserID,
					ItemID:        localItemID,
					Played:        w.Played,
					PositionTicks: w.PositionTicks,
					PlayCount:     w.PlayCount,
					IsFavorite:    w.IsFavorite,
					LastPlayedAt:  w.LastPlayed,
					UpdatedAt:     now,
				}).Error
			}
			report.WatchImported++
		}
	}

	s.db.BumpLibraryRevision()
	return report, nil
}

func (s *Service) readAllEmbyData(resolved ResolvedSources, mappings []PathMapping) ([]embyLibraryDef, []embyItemRow, []embyUserRow, []embyWatchRow, []string, error) {
	var libs []embyLibraryDef
	var items []embyItemRow
	var users []embyUserRow
	var watches []embyWatchRow
	var warnings []string

	libByLowerName := map[string]int{}
	addLib := func(def embyLibraryDef) {
		name := strings.TrimSpace(def.Name)
		if name == "" {
			return
		}
		mapped := make([]string, 0, len(def.Paths))
		seenP := map[string]bool{}
		for _, p := range def.Paths {
			mp := applyPathMappings(p, mappings)
			if mp != "" && !seenP[strings.ToLower(mp)] {
				seenP[strings.ToLower(mp)] = true
				mapped = append(mapped, mp)
			}
		}
		def.Name = name
		def.Kind = mapEmbyContentType(def.Kind)
		def.Paths = mapped

		key := strings.ToLower(name)
		if idx, exists := libByLowerName[key]; exists {
			if libs[idx].EmbyID == "" {
				libs[idx].EmbyID = def.EmbyID
			}
			for _, p := range mapped {
				found := false
				for _, ep := range libs[idx].Paths {
					if strings.EqualFold(ep, p) {
						found = true
						break
					}
				}
				if !found {
					libs[idx].Paths = append(libs[idx].Paths, p)
				}
			}
			return
		}
		libByLowerName[key] = len(libs)
		libs = append(libs, def)
	}

	// 1. Read <emby-config>/root/default libraries (options.xml + *.mblink)
	if resolved.RootDefaultDir != "" {
		dirLibs, warn := readEmbyRootDefaultLibraries(resolved.RootDefaultDir)
		warnings = append(warnings, warn...)
		for _, dl := range dirLibs {
			addLib(dl)
		}
	}

	// 2. Read users.db
	usersByID := map[string]string{}
	if resolved.UsersDBPath != "" {
		sqlDB, err := openReadOnlySQLite(resolved.UsersDBPath)
		if err != nil {
			return nil, nil, nil, nil, nil, fmt.Errorf("open Emby users.db: %w", err)
		}
		defer sqlDB.Close()

		queries := []string{
			"SELECT CAST(Id AS TEXT), Name FROM LocalUsersv2",
			"SELECT CAST(guid AS TEXT), Name FROM LocalUsersv2",
			"SELECT CAST(Id AS TEXT), Name FROM Users",
		}
		loaded := false
		for _, q := range queries {
			rows, qErr := sqlDB.Query(q)
			if qErr != nil {
				continue
			}
			for rows.Next() {
				var id, name string
				if rows.Scan(&id, &name) == nil && strings.TrimSpace(name) != "" {
					users = append(users, embyUserRow{ID: id, Username: name})
					usersByID[id] = name
				}
			}
			_ = rows.Close()
			loaded = true
			break
		}
		if !loaded {
			warnings = append(warnings, "users.db 中未识别到 LocalUsersv2 或 Users 表")
		}
	}

	// 3. Read library.db (libraries + items + watch progress)
	if resolved.LibraryDBPath != "" {
		sqlDB, err := openReadOnlySQLite(resolved.LibraryDBPath)
		if err != nil {
			return nil, nil, nil, nil, nil, fmt.Errorf("open Emby library.db: %w", err)
		}
		defer sqlDB.Close()

		dbLibs, dbItems, itemWarn := readEmbyLibraryItems(sqlDB, mappings)
		warnings = append(warnings, itemWarn...)
		for _, dl := range dbLibs {
			addLib(dl)
		}
		items = dbItems

		dbWatches, watchWarn := readEmbyWatchData(sqlDB, usersByID, mappings)
		warnings = append(warnings, watchWarn...)
		watches = dbWatches
	}

	return libs, items, users, watches, warnings, nil
}

// readEmbyRootDefaultLibraries parses Emby's <config>/root/default/<LibName>/{options.xml, *.mblink}.
func readEmbyRootDefaultLibraries(rootDefaultDir string) ([]embyLibraryDef, []string) {
	entries, err := os.ReadDir(rootDefaultDir)
	if err != nil {
		return nil, []string{"无法读取 root/default 目录: " + err.Error()}
	}
	var out []embyLibraryDef
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		libDir := filepath.Join(rootDefaultDir, e.Name())
		def := embyLibraryDef{
			Name: e.Name(),
			Kind: "mixed",
		}

		// Parse options.xml
		if raw, err := os.ReadFile(filepath.Join(libDir, "options.xml")); err == nil {
			var opts struct {
				ContentType string `xml:"ContentType"`
				PathInfos   []struct {
					Path string `xml:"Path"`
				} `xml:"PathInfos>MediaPathInfo"`
			}
			if xml.Unmarshal(raw, &opts) == nil {
				if opts.ContentType != "" {
					def.Kind = opts.ContentType
				}
				for _, pi := range opts.PathInfos {
					if p := strings.TrimSpace(pi.Path); p != "" {
						def.Paths = append(def.Paths, p)
					}
				}
			}
		}

		// Also check legacy *.mblink files inside library folder.
		if subEntries, err := os.ReadDir(libDir); err == nil {
			for _, se := range subEntries {
				if se.IsDir() || !strings.HasSuffix(strings.ToLower(se.Name()), ".mblink") {
					continue
				}
				if raw, err := os.ReadFile(filepath.Join(libDir, se.Name())); err == nil {
					if p := strings.TrimSpace(string(raw)); p != "" {
						def.Paths = append(def.Paths, p)
					}
				}
			}
		}

		if len(def.Paths) > 0 {
			out = append(out, def)
		}
	}
	return out, nil
}

func tableColumns(sqlDB *sql.DB, table string) map[string]bool {
	cols := map[string]bool{}
	rows, err := sqlDB.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return cols
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk) == nil {
			cols[strings.ToLower(name)] = true
		}
	}
	return cols
}

func readEmbyLibraryItems(sqlDB *sql.DB, mappings []PathMapping) ([]embyLibraryDef, []embyItemRow, []string) {
	table := ""
	cols := tableColumns(sqlDB, "MediaItems")
	if len(cols) > 0 {
		table = "MediaItems"
	} else {
		cols = tableColumns(sqlDB, "TypedBaseItems")
		if len(cols) > 0 {
			table = "TypedBaseItems"
		}
	}
	if table == "" {
		return nil, nil, []string{"library.db 中未找到 MediaItems 或 TypedBaseItems 表"}
	}

	colOrEmpty := func(name string) string {
		if cols[strings.ToLower(name)] {
			return "COALESCE(CAST(" + name + " AS TEXT), '')"
		}
		return "''"
	}
	colOrZero := func(name string) string {
		if cols[strings.ToLower(name)] {
			return "COALESCE(" + name + ", 0)"
		}
		return "0"
	}

	idCol := "Id"
	if !cols["id"] && cols["guid"] {
		idCol = "guid"
	}
	udKeyCol := "UserDataKeyId"
	if !cols["userdatakeyid"] && cols["userdatakey"] {
		udKeyCol = "UserDataKey"
	}

	q := fmt.Sprintf(`SELECT
		%s, %s, %s, %s, %s, %s, %s,
		%s, %s, %s, %s, %s, %s, %s,
		%s, %s, %s, %s, %s, %s, %s, %s, %s
	FROM %s`,
		colOrEmpty(idCol), colOrEmpty("ParentId"), colOrEmpty("TopParentId"),
		colOrEmpty("SeriesId"), colOrEmpty("SeasonId"), colOrEmpty(udKeyCol), colOrEmpty("type"),
		colOrEmpty("Name"), colOrEmpty("OriginalTitle"), colOrEmpty("SortName"),
		colOrEmpty("Overview"), colOrEmpty("Tagline"), colOrZero("ProductionYear"), colOrEmpty("PremiereDate"),
		colOrZero("CommunityRating"), colOrEmpty("OfficialRating"), colOrZero("RunTimeTicks"),
		colOrZero("IndexNumber"), colOrZero("ParentIndexNumber"), colOrEmpty("Path"),
		colOrEmpty("ProviderIds"), colOrEmpty("Genres"), colOrEmpty("Studios"),
		table,
	)

	rows, err := sqlDB.Query(q)
	if err != nil {
		return nil, nil, []string{"读取 " + table + " 失败: " + err.Error()}
	}
	defer rows.Close()

	var libs []embyLibraryDef
	var items []embyItemRow
	for rows.Next() {
		var id, parentID, topParentID, seriesID, seasonID, udKey, rawType string
		var name, origTitle, sortName, overview, tagline, premiere, officialRating, rawPath, rawPids, rawGenres, rawStudios string
		var year, idxNum, parentIdxNum int
		var runtimeTicks int64
		var rating float64

		if err := rows.Scan(
			&id, &parentID, &topParentID, &seriesID, &seasonID, &udKey, &rawType,
			&name, &origTitle, &sortName, &overview, &tagline, &year, &premiere,
			&rating, &officialRating, &runtimeTicks, &idxNum, &parentIdxNum, &rawPath,
			&rawPids, &rawGenres, &rawStudios,
		); err != nil {
			continue
		}

		mappedPath := applyPathMappings(rawPath, mappings)
		kind := classifyEmbyType(rawType)
		if kind == "CollectionFolder" {
			var paths []string
			if mappedPath != "" {
				paths = []string{mappedPath}
			}
			libs = append(libs, embyLibraryDef{
				EmbyID: id,
				Name:   name,
				Kind:   "mixed",
				Paths:  paths,
			})
			continue
		}
		if kind == "" || strings.TrimSpace(name) == "" {
			continue
		}

		if len(premiere) > 10 {
			premiere = premiere[:10]
		}
		items = append(items, embyItemRow{
			EmbyID:            id,
			ParentID:          parentID,
			TopParentID:       topParentID,
			SeriesID:          seriesID,
			SeasonID:          seasonID,
			UserDataKey:       udKey,
			ItemType:          kind,
			Name:              strings.TrimSpace(name),
			OriginalTitle:     strings.TrimSpace(origTitle),
			SortName:          strings.TrimSpace(sortName),
			Overview:          strings.TrimSpace(overview),
			Tagline:           strings.TrimSpace(tagline),
			Year:              year,
			PremiereDate:      premiere,
			CommunityRating:   rating,
			OfficialRating:    strings.TrimSpace(officialRating),
			RunTimeTicks:      runtimeTicks,
			IndexNumber:       idxNum,
			ParentIndexNumber: parentIdxNum,
			Path:              mappedPath,
			ProviderIDs:       parseEmbyProviderIDs(rawPids),
			Genres:            splitPipe(rawGenres),
			Studios:           splitPipe(rawStudios),
		})
	}
	return libs, items, nil
}

func readEmbyWatchData(sqlDB *sql.DB, usersByID map[string]string, mappings []PathMapping) ([]embyWatchRow, []string) {
	queries := []string{
		`SELECT CAST(u.userId AS TEXT), CAST(u.UserDataKeyId AS TEXT), COALESCE(m.Name,''), COALESCE(m.Path,''), COALESCE(u.played,0), COALESCE(u.playbackPositionTicks,0), COALESCE(u.playCount,0), COALESCE(u.isFavorite,0)
		 FROM UserDatas u JOIN MediaItems m ON m.UserDataKeyId = u.UserDataKeyId`,
		`SELECT CAST(u.userId AS TEXT), CAST(u.key AS TEXT), COALESCE(m.Name,''), COALESCE(m.Path,''), COALESCE(u.played,0), COALESCE(u.playbackPositionTicks,0), COALESCE(u.playCount,0), COALESCE(u.isFavorite,0)
		 FROM UserDatas u JOIN TypedBaseItems m ON m.UserDataKey = u.key`,
	}
	var watches []embyWatchRow
	for _, q := range queries {
		rows, qErr := sqlDB.Query(q)
		if qErr != nil {
			continue
		}
		for rows.Next() {
			var uid, udKey, name, path string
			var played, fav, playCount int
			var pos int64
			if rows.Scan(&uid, &udKey, &name, &path, &played, &pos, &playCount, &fav) == nil {
				watches = append(watches, embyWatchRow{
					UserID:        uid,
					Username:      usersByID[uid],
					UserDataKey:   udKey,
					ItemName:      name,
					ItemPath:      applyPathMappings(path, mappings),
					Played:        played != 0,
					PositionTicks: pos,
					PlayCount:     playCount,
					IsFavorite:    fav != 0,
				})
			}
		}
		_ = rows.Close()
		return watches, nil
	}
	return nil, []string{"library.db 中未识别到兼容的 UserDatas 表结构"}
}

func classifyEmbyType(raw string) string {
	s := strings.TrimSpace(raw)
	// Emby 4.x integer type enum in MediaItems
	if n, err := strconv.Atoi(s); err == nil {
		switch n {
		case 1, 2:
			return "CollectionFolder"
		case 5:
			return "Movie"
		case 6:
			return "Series"
		case 7:
			return "Season"
		case 8:
			return "Episode"
		default:
			return ""
		}
	}
	lower := strings.ToLower(s)
	switch {
	case strings.Contains(lower, "collectionfolder"):
		return "CollectionFolder"
	case strings.HasSuffix(lower, ".movie") || lower == "movie":
		return "Movie"
	case strings.HasSuffix(lower, ".series") || lower == "series":
		return "Series"
	case strings.HasSuffix(lower, ".season") || lower == "season":
		return "Season"
	case strings.HasSuffix(lower, ".episode") || lower == "episode":
		return "Episode"
	default:
		return ""
	}
}

func mapEmbyContentType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "movies":
		return "movies"
	case "tvshows":
		return "tvshows"
	default:
		return "mixed"
	}
}

func parseEmbyProviderIDs(raw string) map[string]string {
	out := map[string]string{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out
	}
	if strings.HasPrefix(raw, "{") {
		_ = json.Unmarshal([]byte(raw), &out)
		return out
	}
	for _, part := range strings.Split(raw, "|") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.TrimSpace(kv[1])
		if k != "" && v != "" {
			switch strings.ToLower(k) {
			case "tmdb":
				out["Tmdb"] = v
			case "imdb":
				out["Imdb"] = v
			case "tvdb":
				out["Tvdb"] = v
			default:
				out[k] = v
			}
		}
	}
	return out
}

func splitPipe(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, "|") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func buildMigratedScanKey(itemType, libID, seriesID string, it embyItemRow) string {
	if it.Path != "" && (itemType == "Movie" || itemType == "Episode" || itemType == "Series") {
		return fmt.Sprintf("%s:%s:%s", strings.ToLower(itemType), libID, filepath.Clean(it.Path))
	}
	if itemType == "Season" && seriesID != "" {
		return fmt.Sprintf("season:%s:%s:%d", libID, seriesID, it.IndexNumber)
	}
	return fmt.Sprintf("emby:%s:%s:%s", strings.ToLower(itemType), libID, it.EmbyID)
}

func openReadOnlySQLite(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	dsn := filepath.ToSlash(path) + "?_pragma=query_only(ON)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	return gdb.DB()
}

func (s *Service) loadExistingUserMap() map[string]string {
	m := map[string]string{}
	var users []model.User
	_ = s.db.Gorm.Select("id, username").Find(&users).Error
	for _, u := range users {
		m[strings.ToLower(u.Username)] = u.ID
	}
	return m
}

func (s *Service) loadExistingLibraryMap() map[string]model.Library {
	m := map[string]model.Library{}
	var libs []model.Library
	_ = s.db.Gorm.Find(&libs).Error
	for _, l := range libs {
		m[strings.ToLower(l.Name)] = l
	}
	return m
}

func (s *Service) buildLocalItemMatcher() func(path, name string) string {
	byBaseName := map[string]string{}
	byName := map[string]string{}

	var sources []model.MediaSource
	_ = s.db.Gorm.Select("item_id, path").Find(&sources).Error
	for _, src := range sources {
		base := strings.ToLower(baseFileName(src.Path))
		if base != "" {
			byBaseName[base] = src.ItemID
		}
	}

	var items []model.Item
	_ = s.db.Gorm.Select("id, name, original_name").Where("soft_deleted_at IS NULL").Find(&items).Error
	for _, it := range items {
		if n := strings.ToLower(strings.TrimSpace(it.Name)); n != "" {
			byName[n] = it.ID
		}
		if o := strings.ToLower(strings.TrimSpace(it.OriginalName)); o != "" {
			byName[o] = it.ID
		}
	}

	return func(embyPath, embyName string) string {
		if embyPath != "" {
			if id, ok := byBaseName[strings.ToLower(baseFileName(embyPath))]; ok {
				return id
			}
		}
		if id, ok := byName[strings.ToLower(strings.TrimSpace(embyName))]; ok {
			return id
		}
		return ""
	}
}

func baseFileName(p string) string {
	norm := strings.ReplaceAll(p, `\`, "/")
	parts := strings.Split(norm, "/")
	return parts[len(parts)-1]
}

func normalizeSlashLower(p string) string {
	return strings.ToLower(strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"), "/"))
}
