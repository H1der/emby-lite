package scanner

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"emby-lite/internal/database"
	"emby-lite/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ConsolidateStats reports how many duplicate entities were merged in a library pass.
type ConsolidateStats struct {
	LibraryID      string `json:"library_id"`
	SeriesMerged   int    `json:"series_merged"`
	SeasonsMerged  int    `json:"seasons_merged"`
	MoviesMerged   int    `json:"movies_merged"`
	EpisodesMerged int    `json:"episodes_merged"`
}

// TotalMerged returns the total count of duplicate items folded into primaries.
func (c *ConsolidateStats) TotalMerged() int {
	if c == nil {
		return 0
	}
	return c.SeriesMerged + c.SeasonsMerged + c.MoviesMerged + c.EpisodesMerged
}

// ConsolidateLibrary merges cross-folder TV Series (when library.AutoGroupSeries is enabled)
// and multi-version Movies/Episodes (when library.AutoMergeVersions is enabled).
// It is idempotent and safe to run after scanning, scraping, migration, or on demand.
func ConsolidateLibrary(db *database.DB, libraryID string) (*ConsolidateStats, error) {
	stats := &ConsolidateStats{LibraryID: libraryID}
	if db == nil || libraryID == "" {
		return stats, nil
	}

	var lib model.Library
	if err := db.Gorm.Where("id = ?", libraryID).First(&lib).Error; err != nil {
		return stats, err
	}
	if lib.Kind == "boxsets" || (!lib.AutoGroupSeries && !lib.AutoMergeVersions) {
		return stats, nil
	}

	err := db.Gorm.Transaction(func(tx *gorm.DB) error {
		if lib.AutoGroupSeries && (lib.Kind == "tvshows" || lib.Kind == "mixed") {
			if err := consolidateSeriesTx(tx, db.HasFTS5, &lib, stats); err != nil {
				return err
			}
		}
		if lib.AutoMergeVersions {
			if lib.Kind == "movies" || lib.Kind == "mixed" {
				if err := consolidateMoviesTx(tx, db.HasFTS5, &lib, stats); err != nil {
					return err
				}
			}
			if lib.Kind == "tvshows" || lib.Kind == "mixed" {
				if err := consolidateEpisodesTx(tx, db.HasFTS5, &lib, stats); err != nil {
					return err
				}
			}
		}
		if stats.TotalMerged() > 0 {
			var totalCount int64
			tx.Model(&model.Item{}).
				Where("library_id = ? AND soft_deleted_at IS NULL AND type IN ?", lib.ID, []string{"Movie", "Series"}).
				Count(&totalCount)
			_ = tx.Model(&model.Library{}).Where("id = ?", lib.ID).Update("item_count", totalCount).Error
		}
		return nil
	})
	if err != nil {
		return stats, err
	}

	if stats.TotalMerged() > 0 {
		db.BumpLibraryRevision()
		slog.Info("library consolidation finished",
			"library", lib.Name,
			"series_merged", stats.SeriesMerged,
			"seasons_merged", stats.SeasonsMerged,
			"movies_merged", stats.MoviesMerged,
			"episodes_merged", stats.EpisodesMerged,
		)
	}
	return stats, nil
}

// MergeItemsManual merges multiple Movie or Episode items into a single multi-version item
// (compatible with Emby's POST /Videos/MergeVersions).
func MergeItemsManual(db *database.DB, itemIDs []string) (string, error) {
	if db == nil {
		return "", errors.New("database unavailable")
	}
	seen := make(map[string]bool, len(itemIDs))
	uniq := make([]string, 0, len(itemIDs))
	for _, id := range itemIDs {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) < 2 {
		return "", errors.New("at least two item IDs are required to merge versions")
	}

	var items []model.Item
	if err := db.Gorm.Where("id IN ? AND soft_deleted_at IS NULL", uniq).Find(&items).Error; err != nil {
		return "", err
	}
	if len(items) < 2 {
		return "", errors.New("fewer than two active items found")
	}

	itemType := items[0].Type
	libID := items[0].LibraryID
	if itemType != "Movie" && itemType != "Episode" {
		return "", fmt.Errorf("cannot merge versions for item type %s", itemType)
	}
	for _, it := range items[1:] {
		if it.Type != itemType {
			return "", errors.New("all items to merge must have the same type")
		}
		if it.LibraryID != libID {
			return "", errors.New("all items to merge must belong to the same library")
		}
	}

	var primaryID string
	err := db.Gorm.Transaction(func(tx *gorm.DB) error {
		scores := loadItemRichnessTx(tx, items)
		sortItemsByPriority(items, scores)
		primary := &items[0]
		primaryID = primary.ID
		if primary.DisableAutoMerge {
			primary.DisableAutoMerge = false
			_ = tx.Model(primary).Update("disable_auto_merge", false).Error
		}
		for i := 1; i < len(items); i++ {
			if err := mergePlayableItemTx(tx, db.HasFTS5, primary, &items[i]); err != nil {
				return err
			}
		}
		ensureDistinctVersionLabelsTx(tx, primary.ID)
		var totalCount int64
		tx.Model(&model.Item{}).
			Where("library_id = ? AND soft_deleted_at IS NULL AND type IN ?", libID, []string{"Movie", "Series"}).
			Count(&totalCount)
		_ = tx.Model(&model.Library{}).Where("id = ?", libID).Update("item_count", totalCount).Error
		return nil
	})
	if err != nil {
		return "", err
	}
	db.BumpLibraryRevision()
	return primaryID, nil
}

// SplitItemVersions splits a multi-version Movie or Episode back into individual items
// (compatible with Emby's DELETE /Videos/:itemId/AlternateSources) and marks the resulting
// items with DisableAutoMerge=true so subsequent scans respect the manual split.
func SplitItemVersions(db *database.DB, itemID string) ([]string, error) {
	if db == nil {
		return nil, errors.New("database unavailable")
	}
	var primary model.Item
	if err := db.Gorm.Where("id = ? AND soft_deleted_at IS NULL", itemID).First(&primary).Error; err != nil {
		return nil, err
	}
	if primary.Type != "Movie" && primary.Type != "Episode" {
		return nil, fmt.Errorf("cannot split versions for item type %s", primary.Type)
	}

	var sources []model.MediaSource
	if err := db.Gorm.Where("item_id = ?", primary.ID).Order("height DESC, file_size DESC, id ASC").Find(&sources).Error; err != nil {
		return nil, err
	}
	if len(sources) <= 1 {
		return []string{primary.ID}, nil
	}

	createdIDs := []string{primary.ID}
	err := db.Gorm.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		primary.DisableAutoMerge = true
		if err := tx.Model(&primary).Update("disable_auto_merge", true).Error; err != nil {
			return err
		}

		// Load reusable metadata relations to clone onto split siblings.
		var genres []model.ItemGenre
		var tags []model.ItemTag
		var people []model.ItemPerson
		var images []model.ItemImage
		_ = tx.Where("item_id = ?", primary.ID).Find(&genres).Error
		_ = tx.Where("item_id = ?", primary.ID).Find(&tags).Error
		_ = tx.Where("item_id = ?", primary.ID).Find(&people).Error
		_ = tx.Where("item_id = ?", primary.ID).Find(&images).Error

		prefix := "movie"
		if primary.Type == "Episode" {
			prefix = "episode"
		}

		for i := 1; i < len(sources); i++ {
			src := sources[i]
			scanKey := fmt.Sprintf("%s:%s:%s", prefix, primary.LibraryID, filepath.Clean(src.Path))
			// Clear any stale soft-deleted record holding this scan_key.
			_ = tx.Where("scan_key = ? AND soft_deleted_at IS NOT NULL", scanKey).Delete(&model.Item{}).Error

			newID := StableID("item", scanKey)
			if newID == primary.ID {
				newID = StableID("item", scanKey+"|split|"+src.ID)
				scanKey = scanKey + "|split|" + src.ID
			}

			clone := primary
			clone.ID = newID
			clone.DirPath = filepath.Dir(src.Path)
			clone.ScanKey = scanKey
			clone.RandomKey = StableID("rnd", scanKey)[:12]
			clone.DisableAutoMerge = true
			clone.SoftDeletedAt = nil
			clone.CreatedAt = now
			clone.UpdatedAt = now
			if src.DurationSecs > 0 {
				clone.RuntimeTicks = int64(src.DurationSecs * 1e7)
			}

			if err := tx.Create(&clone).Error; err != nil {
				return err
			}
			syncFTSTxStandalone(tx, db.HasFTS5, &clone)

			_ = tx.Model(&model.MediaSource{}).Where("id = ?", src.ID).Update("item_id", clone.ID).Error
			_ = tx.Model(&model.MediaSubtitle{}).Where("media_source_id = ?", src.ID).Update("item_id", clone.ID).Error
			_ = tx.Model(&model.ScanFile{}).Where("library_id = ? AND path = ?", primary.LibraryID, src.Path).Update("item_id", clone.ID).Error

			for _, g := range genres {
				_ = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.ItemGenre{ItemID: clone.ID, Genre: g.Genre}).Error
			}
			for _, t := range tags {
				_ = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.ItemTag{ItemID: clone.ID, Tag: t.Tag}).Error
			}
			for _, pe := range people {
				_ = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.ItemPerson{
					ItemID: clone.ID, PersonID: pe.PersonID, RoleType: pe.RoleType, RoleName: pe.RoleName, SortIdx: pe.SortIdx,
				}).Error
			}
			for _, img := range images {
				imgCopy := img
				imgCopy.ID = StableID("img", fmt.Sprintf("%s|%s|%d", clone.ID, img.Kind, img.IndexNum))
				imgCopy.ItemID = clone.ID
				imgCopy.UpdatedAt = now
				_ = tx.Save(&imgCopy).Error
			}

			createdIDs = append(createdIDs, clone.ID)
		}

		var totalCount int64
		tx.Model(&model.Item{}).
			Where("library_id = ? AND soft_deleted_at IS NULL AND type IN ?", primary.LibraryID, []string{"Movie", "Series"}).
			Count(&totalCount)
		_ = tx.Model(&model.Library{}).Where("id = ?", primary.LibraryID).Update("item_count", totalCount).Error
		return nil
	})
	if err != nil {
		return nil, err
	}
	db.BumpLibraryRevision()
	return createdIDs, nil
}

// --- Union-Find Grouping Helper ---

type dsu struct {
	parent []int
}

func newDSU(n int) *dsu {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	return &dsu{parent: p}
}

func (d *dsu) find(x int) int {
	for d.parent[x] != x {
		d.parent[x] = d.parent[d.parent[x]]
		x = d.parent[x]
	}
	return x
}

func (d *dsu) union(a, b int) {
	ra, rb := d.find(a), d.find(b)
	if ra != rb {
		if ra < rb {
			d.parent[rb] = ra
		} else {
			d.parent[ra] = rb
		}
	}
}

// --- Provider ID Helpers ---

func parseProviderIDMap(raw string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	var m map[string]string
	if json.Unmarshal([]byte(raw), &m) != nil {
		return out
	}
	for k, v := range m {
		nk := canonicalProviderKey(k)
		nv := strings.TrimSpace(v)
		if nk != "" && nv != "" {
			out[nk] = nv
		}
	}
	return out
}

func canonicalProviderKey(k string) string {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "tmdb":
		return "Tmdb"
	case "imdb":
		return "Imdb"
	case "tvdb":
		return "Tvdb"
	default:
		return strings.TrimSpace(k)
	}
}

// providerIDsConflict returns true when both maps contain a non-empty value for the
// same major provider (Tmdb, Imdb, Tvdb) and those values differ.
func providerIDsConflict(a, b map[string]string) bool {
	for _, key := range []string{"Tmdb", "Imdb", "Tvdb"} {
		va := strings.ToLower(strings.TrimSpace(a[key]))
		vb := strings.ToLower(strings.TrimSpace(b[key]))
		if va != "" && vb != "" && va != vb {
			return true
		}
	}
	return false
}

func mergeProviderIDMaps(dst, src map[string]string) (string, bool) {
	changed := false
	merged := make(map[string]string, len(dst)+len(src))
	for k, v := range dst {
		merged[k] = v
	}
	for k, v := range src {
		if v == "" {
			continue
		}
		if _, exists := merged[k]; !exists {
			merged[k] = v
			changed = true
		}
	}
	return mustJSON(merged), changed
}

// --- Step 1: Cross-Folder Series Consolidation ---

func consolidateSeriesTx(tx *gorm.DB, hasFTS bool, lib *model.Library, stats *ConsolidateStats) error {
	var seriesList []model.Item
	if err := tx.Where("library_id = ? AND type = ? AND soft_deleted_at IS NULL", lib.ID, "Series").
		Find(&seriesList).Error; err != nil || len(seriesList) < 2 {
		return err
	}

	n := len(seriesList)
	uf := newDSU(n)
	pids := make([]map[string]string, n)
	normNames := make([]string, n)
	hadSeason := make([]bool, n)

	// Index by provider ID (Tmdb / Tvdb / Imdb).
	byProvider := make(map[string][]int, n)
	// Index by normalized title (after stripping trailing "第一季" / "Season 2" / "S02").
	byTitle := make(map[string][]int, n)

	for i := range seriesList {
		it := &seriesList[i]
		pids[i] = parseProviderIDMap(it.ProviderIDsJSON)
		for _, pk := range []string{"Tmdb", "Tvdb", "Imdb"} {
			if val := strings.ToLower(strings.TrimSpace(pids[i][pk])); val != "" {
				key := pk + ":" + val
				byProvider[key] = append(byProvider[key], i)
			}
		}

		baseName, stripped := StripSeasonSuffix(it.Name)
		if !stripped && it.DirPath != "" {
			dirBase, dirStripped := StripSeasonSuffix(filepath.Base(it.DirPath))
			if dirStripped {
				stripped = true
				if baseName == "" {
					baseName = dirBase
				}
			}
		}
		hadSeason[i] = stripped
		norm := NormalizeMergeTitle(baseName)
		normNames[i] = norm
		if norm != "" {
			byTitle[norm] = append(byTitle[norm], i)
		}
		if origBase, origStripped := StripSeasonSuffix(it.OriginalName); origBase != "" {
			if origStripped {
				hadSeason[i] = true
			}
			if origNorm := NormalizeMergeTitle(origBase); origNorm != "" && origNorm != norm {
				byTitle[origNorm] = append(byTitle[origNorm], i)
			}
		}
	}

	// Rule 1: Exact Provider ID match (highest confidence).
	for _, idxs := range byProvider {
		if len(idxs) < 2 {
			continue
		}
		first := idxs[0]
		for _, other := range idxs[1:] {
			if !providerIDsConflict(pids[first], pids[other]) {
				uf.union(first, other)
			}
		}
	}

	// Rule 2: Same normalized title + compatible year (or explicit season suffix) with no conflicting provider IDs.
	for _, idxs := range byTitle {
		if len(idxs) < 2 {
			continue
		}
		for a := 0; a < len(idxs); a++ {
			ia := idxs[a]
			for b := a + 1; b < len(idxs); b++ {
				ib := idxs[b]
				if uf.find(ia) == uf.find(ib) {
					continue
				}
				if providerIDsConflict(pids[ia], pids[ib]) {
					continue
				}
				ya, yb := seriesList[ia].Year, seriesList[ib].Year
				yearCompat := ya == yb || ya == 0 || yb == 0 || hadSeason[ia] || hadSeason[ib]
				if yearCompat {
					uf.union(ia, ib)
				}
			}
		}
	}

	groups := make(map[int][]model.Item)
	for i := range seriesList {
		r := uf.find(i)
		groups[r] = append(groups[r], seriesList[i])
	}

	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		scores := loadItemRichnessTx(tx, group)
		sortItemsByPriority(group, scores)
		primary := &group[0]

		// Clean trailing season suffix from primary's Name only if it came from a season-named folder
		// and another sibling has the clean title (or preserve existing rich title).
		if cleanName, stripped := StripSeasonSuffix(primary.Name); stripped && cleanName != "" {
			for j := 1; j < len(group); j++ {
				if _, s2 := StripSeasonSuffix(group[j].Name); !s2 && NormalizeMergeTitle(group[j].Name) == NormalizeMergeTitle(cleanName) {
					primary.Name = group[j].Name
					primary.SortName = BuildSortName(primary.Name)
					primary.PinyinInitials = PinyinInitials(primary.Name + " " + primary.OriginalName)
					_ = tx.Save(primary).Error
					syncFTSTxStandalone(tx, hasFTS, primary)
					break
				}
			}
		}

		for i := 1; i < len(group); i++ {
			mergedSeasons, err := mergeSeriesItemTx(tx, hasFTS, lib, primary, &group[i])
			if err != nil {
				return err
			}
			stats.SeriesMerged++
			stats.SeasonsMerged += mergedSeasons
		}
	}
	return nil
}

func mergeSeriesItemTx(tx *gorm.DB, hasFTS bool, lib *model.Library, primary, dup *model.Item) (int, error) {
	seasonsMerged := 0

	// 1. Load existing seasons on primary keyed by season_num.
	var primarySeasons []model.Item
	_ = tx.Where("series_id = ? AND type = ? AND soft_deleted_at IS NULL", primary.ID, "Season").Find(&primarySeasons).Error
	primarySeasonByNum := make(map[int]*model.Item, len(primarySeasons))
	for i := range primarySeasons {
		primarySeasonByNum[primarySeasons[i].SeasonNum] = &primarySeasons[i]
	}

	// 2. Reconcile all seasons belonging to dup.
	var dupSeasons []model.Item
	_ = tx.Where("(series_id = ? OR parent_id = ?) AND type = ? AND soft_deleted_at IS NULL", dup.ID, dup.ID, "Season").Find(&dupSeasons).Error
	for i := range dupSeasons {
		ds := &dupSeasons[i]
		if ps, exists := primarySeasonByNum[ds.SeasonNum]; exists {
			// Reparent all episodes from dupSeason -> primarySeason.
			if err := tx.Model(&model.Item{}).
				Where("type = ? AND (season_id = ? OR parent_id = ?)", "Episode", ds.ID, ds.ID).
				Updates(map[string]any{
					"series_id": primary.ID,
					"season_id": ps.ID,
					"parent_id": ps.ID,
				}).Error; err != nil {
				return seasonsMerged, err
			}
			mergeItemMetadataAndAncillariesTx(tx, hasFTS, ps, ds)
			deleteDuplicateItemTx(tx, hasFTS, ds.ID)
			seasonsMerged++
		} else {
			// Move dupSeason intact under primarySeries with a conflict-free scan_key.
			newScanKey := fmt.Sprintf("season:%s:%s:%d", lib.ID, primary.ID, ds.SeasonNum)
			_ = tx.Where("scan_key = ? AND id <> ? AND soft_deleted_at IS NOT NULL", newScanKey, ds.ID).Delete(&model.Item{}).Error

			// Guard against an unlikely active scan_key collision.
			var colliding model.Item
			if err := tx.Where("scan_key = ? AND id <> ?", newScanKey, ds.ID).First(&colliding).Error; err == nil {
				if err := tx.Model(&model.Item{}).
					Where("type = ? AND (season_id = ? OR parent_id = ?)", "Episode", ds.ID, ds.ID).
					Updates(map[string]any{
						"series_id": primary.ID,
						"season_id": colliding.ID,
						"parent_id": colliding.ID,
					}).Error; err != nil {
					return seasonsMerged, err
				}
				mergeItemMetadataAndAncillariesTx(tx, hasFTS, &colliding, ds)
				deleteDuplicateItemTx(tx, hasFTS, ds.ID)
				primarySeasonByNum[ds.SeasonNum] = &colliding
				seasonsMerged++
				continue
			}

			if err := tx.Model(&model.Item{}).Where("id = ?", ds.ID).Updates(map[string]any{
				"parent_id": primary.ID,
				"series_id": primary.ID,
				"scan_key":  newScanKey,
			}).Error; err != nil {
				return seasonsMerged, err
			}
			if err := tx.Model(&model.Item{}).
				Where("type = ? AND (season_id = ? OR parent_id = ?)", "Episode", ds.ID, ds.ID).
				Update("series_id", primary.ID).Error; err != nil {
				return seasonsMerged, err
			}
			ds.ParentID = primary.ID
			ds.SeriesID = primary.ID
			ds.ScanKey = newScanKey
			primarySeasonByNum[ds.SeasonNum] = ds
		}
	}

	// 3. Reparent any remaining episodes attached directly to dupSeries.
	_ = tx.Model(&model.Item{}).
		Where("type = ? AND series_id = ?", "Episode", dup.ID).
		Update("series_id", primary.ID).Error
	_ = tx.Model(&model.Item{}).
		Where("type = ? AND parent_id = ?", "Episode", dup.ID).
		Update("parent_id", primary.ID).Error

	// 4. Reparent IntroMarkers referencing dupSeries.
	_ = tx.Model(&model.IntroMarker{}).Where("series_id = ?", dup.ID).Update("series_id", primary.ID).Error

	// 5. Merge metadata, images, people, genres, tags, boxsets, userdata & delete dupSeries.
	mergeItemMetadataAndAncillariesTx(tx, hasFTS, primary, dup)
	deleteDuplicateItemTx(tx, hasFTS, dup.ID)
	return seasonsMerged, nil
}

// --- Step 2: Multi-Version Movie Consolidation ---

func consolidateMoviesTx(tx *gorm.DB, hasFTS bool, lib *model.Library, stats *ConsolidateStats) error {
	var movies []model.Item
	if err := tx.Where("library_id = ? AND type = ? AND soft_deleted_at IS NULL AND disable_auto_merge = ?", lib.ID, "Movie", false).
		Find(&movies).Error; err != nil || len(movies) < 2 {
		return err
	}

	n := len(movies)
	movieIDs := make([]string, n)
	for i := range movies {
		movieIDs[i] = movies[i].ID
	}

	// Preload MediaSources to inspect Emby-style "<base> - <version>" filenames in the same directory.
	var sources []model.MediaSource
	_ = tx.Where("item_id IN ?", movieIDs).Find(&sources).Error
	srcByItem := make(map[string][]model.MediaSource, n)
	for _, src := range sources {
		srcByItem[src.ItemID] = append(srcByItem[src.ItemID], src)
	}

	uf := newDSU(n)
	pids := make([]map[string]string, n)
	byProvider := make(map[string][]int, n)
	byDirTitle := make(map[string][]int, n)
	byDirPrefix := make(map[string][]int, n)
	byTitleYear := make(map[string][]int, n)

	for i := range movies {
		m := &movies[i]
		pids[i] = parseProviderIDMap(m.ProviderIDsJSON)
		for _, pk := range []string{"Tmdb", "Imdb"} {
			if val := strings.ToLower(strings.TrimSpace(pids[i][pk])); val != "" {
				byProvider[pk+":"+val] = append(byProvider[pk+":"+val], i)
			}
		}

		normDir := strings.ToLower(filepath.ToSlash(filepath.Clean(m.DirPath)))
		normTitle := NormalizeMergeTitle(m.Name)
		if normDir != "" && normTitle != "" {
			byDirTitle[normDir+"|"+normTitle] = append(byDirTitle[normDir+"|"+normTitle], i)
		}
		if normTitle != "" && m.Year > 0 {
			byTitleYear[fmt.Sprintf("%s|%d", normTitle, m.Year)] = append(byTitleYear[fmt.Sprintf("%s|%d", normTitle, m.Year)], i)
		}

		// Emby official multi-version convention: files in the same folder sharing "<Base> - <Version>.ext"
		// (or matching "<FolderName> - <Version>.ext").
		if normDir != "" {
			for _, src := range srcByItem[m.ID] {
				base, _, hasSplit := ExtractVersionBaseAndLabel(filepath.Base(src.Path))
				if hasSplit {
					if nb := NormalizeMergeTitle(base); nb != "" {
						byDirPrefix[normDir+"|"+nb] = append(byDirPrefix[normDir+"|"+nb], i)
					}
				} else {
					// Also allow a plain file "<Base>.mkv" to match "<Base> - 4K.mkv" in the same directory.
					if nb := NormalizeMergeTitle(base); nb != "" {
						byDirPrefix[normDir+"|"+nb] = append(byDirPrefix[normDir+"|"+nb], i)
					}
				}
			}
		}
	}

	// Rule 1: Exact Provider ID (Tmdb / Imdb), works across folders too.
	for _, idxs := range byProvider {
		if len(idxs) < 2 {
			continue
		}
		first := idxs[0]
		for _, other := range idxs[1:] {
			if !providerIDsConflict(pids[first], pids[other]) {
				uf.union(first, other)
			}
		}
	}

	// Rule 2: Same folder + same Emby version prefix or same normalized title with compatible year.
	for _, idxs := range byDirPrefix {
		if len(idxs) < 2 {
			continue
		}
		for a := 0; a < len(idxs); a++ {
			for b := a + 1; b < len(idxs); b++ {
				ia, ib := idxs[a], idxs[b]
				if uf.find(ia) != uf.find(ib) && !providerIDsConflict(pids[ia], pids[ib]) {
					uf.union(ia, ib)
				}
			}
		}
	}
	for _, idxs := range byDirTitle {
		if len(idxs) < 2 {
			continue
		}
		for a := 0; a < len(idxs); a++ {
			for b := a + 1; b < len(idxs); b++ {
				ia, ib := idxs[a], idxs[b]
				if uf.find(ia) == uf.find(ib) || providerIDsConflict(pids[ia], pids[ib]) {
					continue
				}
				ya, yb := movies[ia].Year, movies[ib].Year
				if ya == yb || ya == 0 || yb == 0 {
					uf.union(ia, ib)
				}
			}
		}
	}

	// Rule 3: Same normalized title + identical non-zero Year in the same library (e.g. 4K folder + 1080p folder).
	for _, idxs := range byTitleYear {
		if len(idxs) < 2 {
			continue
		}
		for a := 0; a < len(idxs); a++ {
			for b := a + 1; b < len(idxs); b++ {
				ia, ib := idxs[a], idxs[b]
				if uf.find(ia) != uf.find(ib) && !providerIDsConflict(pids[ia], pids[ib]) {
					uf.union(ia, ib)
				}
			}
		}
	}

	groups := make(map[int][]model.Item)
	for i := range movies {
		r := uf.find(i)
		groups[r] = append(groups[r], movies[i])
	}

	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		scores := loadItemRichnessTx(tx, group)
		sortItemsByPriority(group, scores)
		primary := &group[0]
		for i := 1; i < len(group); i++ {
			if err := mergePlayableItemTx(tx, hasFTS, primary, &group[i]); err != nil {
				return err
			}
			stats.MoviesMerged++
		}
		ensureDistinctVersionLabelsTx(tx, primary.ID)
	}
	return nil
}

// --- Step 3: Multi-Version Episode Consolidation ---

func consolidateEpisodesTx(tx *gorm.DB, hasFTS bool, lib *model.Library, stats *ConsolidateStats) error {
	var episodes []model.Item
	if err := tx.Where("library_id = ? AND type = ? AND soft_deleted_at IS NULL AND disable_auto_merge = ? AND series_id <> '' AND episode_num > 0",
		lib.ID, "Episode", false).
		Find(&episodes).Error; err != nil || len(episodes) < 2 {
		return err
	}

	type epKey struct {
		seriesID   string
		seasonNum  int
		episodeNum int
	}
	buckets := make(map[epKey][]model.Item)
	for _, ep := range episodes {
		k := epKey{seriesID: ep.SeriesID, seasonNum: ep.SeasonNum, episodeNum: ep.EpisodeNum}
		buckets[k] = append(buckets[k], ep)
	}

	for _, group := range buckets {
		if len(group) < 2 {
			continue
		}
		// Partition via DSU so conflicting provider IDs are never forced together.
		uf := newDSU(len(group))
		pids := make([]map[string]string, len(group))
		for i := range group {
			pids[i] = parseProviderIDMap(group[i].ProviderIDsJSON)
		}
		for a := 0; a < len(group); a++ {
			for b := a + 1; b < len(group); b++ {
				if !providerIDsConflict(pids[a], pids[b]) {
					uf.union(a, b)
				}
			}
		}
		subGroups := make(map[int][]model.Item)
		for i := range group {
			r := uf.find(i)
			subGroups[r] = append(subGroups[r], group[i])
		}
		for _, sg := range subGroups {
			if len(sg) < 2 {
				continue
			}
			scores := loadItemRichnessTx(tx, sg)
			sortItemsByPriority(sg, scores)
			primary := &sg[0]
			for i := 1; i < len(sg); i++ {
				if err := mergePlayableItemTx(tx, hasFTS, primary, &sg[i]); err != nil {
					return err
				}
				stats.EpisodesMerged++
			}
			ensureDistinctVersionLabelsTx(tx, primary.ID)
		}
	}
	return nil
}

// --- Shared Merge Primitives ---

func mergePlayableItemTx(tx *gorm.DB, hasFTS bool, primary, dup *model.Item) error {
	// 1. Move MediaSources from dup -> primary.
	if err := tx.Model(&model.MediaSource{}).Where("item_id = ?", dup.ID).Update("item_id", primary.ID).Error; err != nil {
		return err
	}
	// 2. Move MediaSubtitles from dup -> primary.
	if err := tx.Model(&model.MediaSubtitle{}).Where("item_id = ?", dup.ID).Update("item_id", primary.ID).Error; err != nil {
		return err
	}
	// 3. Move ScanFiles from dup -> primary (keeps incremental skip & safe missing cleanup intact!).
	if err := tx.Model(&model.ScanFile{}).Where("item_id = ?", dup.ID).Update("item_id", primary.ID).Error; err != nil {
		return err
	}
	// 4. Move IntroMarkers referencing dup.
	_ = tx.Model(&model.IntroMarker{}).Where("item_id = ?", dup.ID).Update("item_id", primary.ID).Error

	// 5. Merge metadata, chapters, images, genres, tags, people, boxsets, userdata.
	mergeItemMetadataAndAncillariesTx(tx, hasFTS, primary, dup)

	// 6. Delete duplicate item and its remaining orphan rows.
	deleteDuplicateItemTx(tx, hasFTS, dup.ID)
	return nil
}

func mergeItemMetadataAndAncillariesTx(tx *gorm.DB, hasFTS bool, primary, dup *model.Item) {
	// Fill missing scalar metadata on primary from dup.
	changed := false
	if primary.OriginalName == "" && dup.OriginalName != "" {
		primary.OriginalName = dup.OriginalName
		changed = true
	}
	if primary.Overview == "" && dup.Overview != "" {
		primary.Overview = dup.Overview
		changed = true
	}
	if primary.Tagline == "" && dup.Tagline != "" {
		primary.Tagline = dup.Tagline
		changed = true
	}
	if primary.Year == 0 && dup.Year > 0 {
		primary.Year = dup.Year
		changed = true
	}
	if primary.PremiereDate == "" && dup.PremiereDate != "" {
		primary.PremiereDate = dup.PremiereDate
		changed = true
	}
	if primary.CommunityRating == 0 && dup.CommunityRating > 0 {
		primary.CommunityRating = dup.CommunityRating
		changed = true
	}
	if primary.OfficialRating == "" && dup.OfficialRating != "" {
		primary.OfficialRating = dup.OfficialRating
		changed = true
	}
	if primary.RuntimeTicks == 0 && dup.RuntimeTicks > 0 {
		primary.RuntimeTicks = dup.RuntimeTicks
		changed = true
	}
	if primary.StudiosJSON == "" || primary.StudiosJSON == "[]" || primary.StudiosJSON == "null" {
		if dup.StudiosJSON != "" && dup.StudiosJSON != "[]" && dup.StudiosJSON != "null" {
			primary.StudiosJSON = dup.StudiosJSON
			changed = true
		}
	}
	if mergedPIDs, pidChanged := mergeProviderIDMaps(parseProviderIDMap(primary.ProviderIDsJSON), parseProviderIDMap(dup.ProviderIDsJSON)); pidChanged {
		primary.ProviderIDsJSON = mergedPIDs
		changed = true
	}
	if dup.AddedAt.After(primary.AddedAt) {
		primary.AddedAt = dup.AddedAt
		changed = true
	}
	if changed {
		primary.PinyinInitials = PinyinInitials(primary.Name + " " + primary.OriginalName)
		_ = tx.Save(primary).Error
		syncFTSTxStandalone(tx, hasFTS, primary)
	}

	// Merge Genres & Tags (union).
	var dupGenres []model.ItemGenre
	if tx.Where("item_id = ?", dup.ID).Find(&dupGenres).Error == nil && len(dupGenres) > 0 {
		for _, g := range dupGenres {
			_ = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.ItemGenre{ItemID: primary.ID, Genre: g.Genre}).Error
		}
	}
	var dupTags []model.ItemTag
	if tx.Where("item_id = ?", dup.ID).Find(&dupTags).Error == nil && len(dupTags) > 0 {
		for _, t := range dupTags {
			_ = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.ItemTag{ItemID: primary.ID, Tag: t.Tag}).Error
		}
	}

	// Fill missing People if primary has none.
	var primaryPeopleCnt int64
	tx.Model(&model.ItemPerson{}).Where("item_id = ?", primary.ID).Count(&primaryPeopleCnt)
	if primaryPeopleCnt == 0 {
		var dupPeople []model.ItemPerson
		if tx.Where("item_id = ?", dup.ID).Find(&dupPeople).Error == nil {
			for _, pe := range dupPeople {
				_ = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.ItemPerson{
					ItemID: primary.ID, PersonID: pe.PersonID, RoleType: pe.RoleType, RoleName: pe.RoleName, SortIdx: pe.SortIdx,
				}).Error
			}
		}
	}

	// Fill missing Images by kind.
	var primaryImgs []model.ItemImage
	_ = tx.Where("item_id = ?", primary.ID).Find(&primaryImgs).Error
	hasKind := make(map[string]bool, len(primaryImgs))
	for _, img := range primaryImgs {
		hasKind[img.Kind] = true
	}
	var dupImgs []model.ItemImage
	if tx.Where("item_id = ?", dup.ID).Find(&dupImgs).Error == nil {
		for _, img := range dupImgs {
			if hasKind[img.Kind] {
				continue
			}
			hasKind[img.Kind] = true
			imgCopy := img
			imgCopy.ID = StableID("img", fmt.Sprintf("%s|%s|%d", primary.ID, img.Kind, img.IndexNum))
			imgCopy.ItemID = primary.ID
			_ = tx.Save(&imgCopy).Error
		}
	}

	// Fill missing Chapters if primary has none.
	var primaryChCnt int64
	tx.Model(&model.ItemChapter{}).Where("item_id = ?", primary.ID).Count(&primaryChCnt)
	if primaryChCnt == 0 {
		var dupChs []model.ItemChapter
		if tx.Where("item_id = ?", dup.ID).Find(&dupChs).Error == nil {
			for _, ch := range dupChs {
				ch.ItemID = primary.ID
				_ = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&ch).Error
			}
		}
	}

	// Reparent BoxSet memberships.
	var dupBoxItems []model.BoxSetItem
	if tx.Where("item_id = ?", dup.ID).Find(&dupBoxItems).Error == nil {
		for _, bi := range dupBoxItems {
			_ = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.BoxSetItem{
				BoxSetID: bi.BoxSetID, ItemID: primary.ID, SortIdx: bi.SortIdx,
			}).Error
		}
		_ = tx.Where("item_id = ?", dup.ID).Delete(&model.BoxSetItem{}).Error
	}

	// Merge UserData per user (preserve played, favorite, max play_count, max position, latest last_played_at).
	var dupUDs []model.UserData
	if tx.Where("item_id = ?", dup.ID).Find(&dupUDs).Error == nil && len(dupUDs) > 0 {
		for _, dud := range dupUDs {
			var pud model.UserData
			err := tx.Where("user_id = ? AND item_id = ?", dud.UserID, primary.ID).First(&pud).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				dud.ItemID = primary.ID
				_ = tx.Create(&dud).Error
			} else if err == nil {
				pud.Played = pud.Played || dud.Played
				pud.IsFavorite = pud.IsFavorite || dud.IsFavorite
				pud.HideFromResume = pud.HideFromResume && dud.HideFromResume
				if dud.PositionTicks > pud.PositionTicks {
					pud.PositionTicks = dud.PositionTicks
				}
				if dud.PlayCount > pud.PlayCount {
					pud.PlayCount = dud.PlayCount
				}
				if dud.LastPlayedAt != nil && (pud.LastPlayedAt == nil || dud.LastPlayedAt.After(*pud.LastPlayedAt)) {
					pud.LastPlayedAt = dud.LastPlayedAt
				}
				if dud.UpdatedAt.After(pud.UpdatedAt) {
					pud.UpdatedAt = dud.UpdatedAt
				}
				_ = tx.Save(&pud).Error
			}
		}
		_ = tx.Where("item_id = ?", dup.ID).Delete(&model.UserData{}).Error
	}
}

func deleteDuplicateItemTx(tx *gorm.DB, hasFTS bool, dupID string) {
	if hasFTS {
		_ = tx.Exec("DELETE FROM items_fts WHERE item_id = ?", dupID).Error
	}
	_ = tx.Where("item_id = ?", dupID).Delete(&model.ItemGenre{}).Error
	_ = tx.Where("item_id = ?", dupID).Delete(&model.ItemTag{}).Error
	_ = tx.Where("item_id = ?", dupID).Delete(&model.ItemPerson{}).Error
	_ = tx.Where("item_id = ?", dupID).Delete(&model.ItemImage{}).Error
	_ = tx.Where("item_id = ?", dupID).Delete(&model.ItemChapter{}).Error
	_ = tx.Where("item_id = ?", dupID).Delete(&model.ItemMetadataLock{}).Error
	_ = tx.Where("item_id = ?", dupID).Delete(&model.ScrapeMatchQueue{}).Error
	_ = tx.Where("item_id = ?", dupID).Delete(&model.BoxSetItem{}).Error
	_ = tx.Where("item_id = ?", dupID).Delete(&model.UserData{}).Error
	_ = tx.Where("id = ?", dupID).Delete(&model.Item{}).Error
}

func syncFTSTxStandalone(tx *gorm.DB, hasFTS bool, item *model.Item) {
	if !hasFTS || item == nil {
		return
	}
	_ = tx.Exec("DELETE FROM items_fts WHERE item_id = ?", item.ID).Error
	_ = tx.Exec("INSERT INTO items_fts(item_id, name, original_name, pinyin_initials) VALUES (?, ?, ?, ?)",
		item.ID, item.Name, item.OriginalName, item.PinyinInitials).Error
}

// ensureDistinctVersionLabelsTx populates empty VersionLabel fields on a multi-version item's
// MediaSources so clients and UI always have clean, human-readable labels.
func ensureDistinctVersionLabelsTx(tx *gorm.DB, itemID string) {
	var sources []model.MediaSource
	if err := tx.Where("item_id = ?", itemID).Order("height DESC, file_size DESC, id ASC").Find(&sources).Error; err != nil || len(sources) < 2 {
		return
	}
	for i := range sources {
		if strings.TrimSpace(sources[i].VersionLabel) != "" {
			continue
		}
		label := InferVersionLabelFromFilename(filepath.Base(sources[i].Path))
		if label == "" {
			label = buildSpecVersionLabel(&sources[i])
		}
		if label != "" {
			sources[i].VersionLabel = label
			_ = tx.Model(&model.MediaSource{}).Where("id = ?", sources[i].ID).Update("version_label", label).Error
		}
	}
}

func buildSpecVersionLabel(src *model.MediaSource) string {
	var parts []string
	if src.Width >= 3800 || src.Height >= 2100 {
		parts = append(parts, "4K")
	} else if src.Width >= 1900 || src.Height >= 1000 {
		parts = append(parts, "1080p")
	} else if src.Width >= 1200 || src.Height >= 700 {
		parts = append(parts, "720p")
	}
	if src.HDRType != "" && !strings.EqualFold(src.HDRType, "SDR") {
		parts = append(parts, strings.ToUpper(src.HDRType))
	}
	if src.VideoCodec != "" {
		parts = append(parts, strings.ToUpper(src.VideoCodec))
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	return ""
}

// loadItemRichnessTx scores candidate items so the richest, already-tracked item wins as primary.
func loadItemRichnessTx(tx *gorm.DB, items []model.Item) map[string]int {
	ids := make([]string, len(items))
	scores := make(map[string]int, len(items))
	for i := range items {
		it := &items[i]
		ids[i] = it.ID
		s := 0
		if len(parseProviderIDMap(it.ProviderIDsJSON)) > 0 {
			s += 20
		}
		if it.Overview != "" {
			s += 10
		}
		if it.Year > 0 {
			s += 5
		}
		if it.CommunityRating > 0 {
			s += 3
		}
		scores[it.ID] = s
	}

	type idCnt struct {
		ItemID string
		Cnt    int64
	}
	var udCounts, imgCounts, srcCounts, lockCounts []idCnt
	_ = tx.Table("user_data").Select("item_id, COUNT(*) AS cnt").Where("item_id IN ?", ids).Group("item_id").Scan(&udCounts).Error
	for _, r := range udCounts {
		scores[r.ItemID] += int(r.Cnt) * 50
	}
	_ = tx.Table("item_metadata_locks").Select("item_id, COUNT(*) AS cnt").Where("item_id IN ?", ids).Group("item_id").Scan(&lockCounts).Error
	for _, r := range lockCounts {
		scores[r.ItemID] += 40
	}
	_ = tx.Table("media_sources").Select("item_id, COUNT(*) AS cnt").Where("item_id IN ?", ids).Group("item_id").Scan(&srcCounts).Error
	for _, r := range srcCounts {
		scores[r.ItemID] += int(r.Cnt) * 25
	}
	_ = tx.Table("item_images").Select("item_id, COUNT(*) AS cnt").Where("item_id IN ?", ids).Group("item_id").Scan(&imgCounts).Error
	for _, r := range imgCounts {
		scores[r.ItemID] += int(r.Cnt) * 5
	}
	return scores
}

func sortItemsByPriority(items []model.Item, scores map[string]int) {
	sort.Slice(items, func(i, j int) bool {
		si, sj := scores[items[i].ID], scores[items[j].ID]
		if si != sj {
			return si > sj
		}
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return items[i].ID < items[j].ID
	})
}

// Keep unused import check satisfied if time is used elsewhere.
var _ = time.UTC
