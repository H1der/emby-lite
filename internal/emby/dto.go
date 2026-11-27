package emby

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"emby-lite/internal/auth"
	"emby-lite/internal/model"
	"emby-lite/internal/playback"
)

const ProtocolVersion = "4.10.2.0"

const (
	embyLocalAuthProviderID     = "Emby.Server.Implementations.LocalAuthenticationProvider"
	embyLocalPasswordResetID    = "Emby.Server.Implementations.LocalPasswordResetProvider"
)

// stableNumericID produces a deterministic positive 52-bit int64 ID for Emby NameLongIdPair
// objects (GenreItems, Studios, TagItems). Keeping it <= 52 bits guarantees exact representation
// in both int64 (Kotlin/Java/Go/Swift) and IEEE-754 double (JavaScript) clients.
func stableNumericID(prefix, name string) int64 {
	sum := sha256.Sum256([]byte(prefix + ":" + strings.ToLower(strings.TrimSpace(name))))
	v, err := strconv.ParseInt(hex.EncodeToString(sum[:])[:13], 16, 64)
	if err != nil || v <= 0 {
		return 1
	}
	return v
}

func (h *Handler) serverID() string {
	return h.db.GetSetting("server_id", "emby-lite-server")
}

func (h *Handler) serverName() string {
	return h.db.GetSetting("server_name", h.cfg.Server.ServerName)
}

func (h *Handler) userDTO(u *model.User, policy auth.EffectivePolicy) map[string]any {
	m := map[string]any{
		"Name":                      u.Username,
		"ServerId":                  h.serverID(),
		"ServerName":                h.serverName(),
		"Id":                        u.ID,
		"HasPassword":               true,
		"HasConfiguredPassword":     true,
		"HasConfiguredEasyPassword": false,
		"EnableAutoLogin":           false,
		"Configuration": h.effectiveUserConfigMap(u.ID),
		"Policy": map[string]any{
			"IsAdministrator":                  u.IsAdmin,
			"IsHidden":                         false,
			"IsHiddenRemotely":                 false,
			"IsHiddenFromUnusedDevices":        false,
			"IsDisabled":                       u.Disabled,
			"EnableUserPreferenceAccess":       true,
			"EnableRemoteAccess":               true,
			"EnableMediaPlayback":              policy.AllowPlayback,
			"EnableAudioPlaybackTranscoding":   false,
			"EnableVideoPlaybackTranscoding":   false,
			"EnablePlaybackRemuxing":           false,
			"EnableContentDeletion":            u.IsAdmin,
			"EnableContentDownloading":         policy.AllowDownload,
			"EnableSubtitleDownloading":        false,
			"EnableSubtitleManagement":         false,
			"EnableSyncTranscoding":            false,
			"EnableMediaConversion":            false,
			"EnableLiveTvAccess":               false,
			"EnableLiveTvManagement":           false,
			"EnablePublicSharing":              false,
			"EnableCollectionManagement":       u.IsAdmin,
			"EnableRemoteControlOfOtherUsers":  false,
			"EnableSharedDeviceControl":        false,
			"EnableAllChannels":                true,
			"EnableAllFolders":                 u.IsAdmin,
			"EnableAllDevices":                 true,
			"SimultaneousStreamLimit":          policy.MaxConcurrentStreams,
			"RemoteClientBitrateLimit":         0,
			"InvalidLoginAttemptCount":         0,
			"BlockedTags":                      []string{},
			"IncludeTags":                      []string{},
			"AccessSchedules":                  []any{},
			"BlockUnratedItems":                []string{},
			"EnabledFolders":                   []string{},
			"EnabledDevices":                   []string{},
			"EnabledChannels":                  []string{},
			"ExcludedSubFolders":               []string{},
			"EnableContentDeletionFromFolders": []string{},
			"AuthenticationProviderId":         embyLocalAuthProviderID,
			"PasswordResetProviderId":          embyLocalPasswordResetID,
		},
	}
	if u.LastLoginAt != nil {
		ts := u.LastLoginAt.UTC().Format(time.RFC3339)
		m["LastLoginDate"] = ts
		m["LastActivityDate"] = ts
	}
	return m
}

func (h *Handler) libraryViewDTO(lib *model.Library, p *auth.Principal) map[string]any {
	return h.libraryViewDTOWithFallback(lib, p, "", true)
}

func (h *Handler) libraryViewDTOWithFallback(lib *model.Library, p *auth.Principal, fallbackTag string, allowQuery bool) map[string]any {
	collectionType := "movies"
	switch lib.Kind {
	case "tvshows":
		collectionType = "tvshows"
	case "mixed":
		collectionType = "mixed"
	case "boxsets":
		collectionType = "boxsets"
	}

	imageTags := map[string]string{}
	if lib.CoverRevision != "" {
		imageTags["Primary"] = h.signTag(p, lib.ID, "Primary", lib.CoverRevision)
	} else if fallbackTag != "" {
		imageTags["Primary"] = h.signTag(p, lib.ID, "Primary", fallbackTag)
	} else if allowQuery {
		var firstImg model.ItemImage
		err := h.db.Read().Table("item_images").
			Joins("JOIN items ON items.id = item_images.item_id").
			Where("items.library_id = ? AND items.soft_deleted_at IS NULL AND item_images.kind = ?", lib.ID, "Primary").
			Order("items.added_at DESC").
			First(&firstImg).Error
		if err == nil {
			imageTags["Primary"] = h.signTag(p, lib.ID, "Primary", firstImg.Tag)
		}
	}

	return map[string]any{
		"Name":                     lib.Name,
		"SortName":                 strings.ToLower(lib.Name),
		"ServerId":                 h.serverID(),
		"Id":                       lib.ID,
		"ParentId":                 "root",
		"Type":                     "CollectionFolder",
		"CollectionType":           collectionType,
		"IsFolder":                 true,
		"LocationType":             "Virtual",
		"DisplayPreferencesId":     lib.ID,
		"PlayAccess":               "Full",
		"CanDelete":                false,
		"CanDownload":              false,
		"EnableMediaSourceDisplay": true,
		"PrimaryImageAspectRatio":  16.0 / 9.0,
		"ImageTags":                imageTags,
		"BackdropImageTags":        []string{},
		"Genres":                   []string{},
		"GenreItems":               []map[string]any{},
		"Tags":                     []string{},
		"TagItems":                 []map[string]any{},
		"Studios":                  []map[string]any{},
		"ExternalUrls":             []any{},
		"ProviderIds":              map[string]string{},
		"ChildCount":               lib.ItemCount,
		"RecursiveItemCount":       lib.ItemCount,
		"DateCreated":              lib.CreatedAt.UTC().Format(time.RFC3339),
		"UserData": map[string]any{
			"PlaybackPositionTicks": 0,
			"PlayCount":             0,
			"IsFavorite":            false,
			"Played":                false,
			"Key":                   lib.ID,
			"ItemId":                lib.ID,
		},
	}
}

func (h *Handler) libraryViewsDTOs(libs []model.Library, p *auth.Principal) []map[string]any {
	if len(libs) == 0 {
		return []map[string]any{}
	}
	var needFallback []string
	for i := range libs {
		if libs[i].CoverRevision == "" {
			needFallback = append(needFallback, libs[i].ID)
		}
	}
	fallbackTags := make(map[string]string, len(needFallback))
	if len(needFallback) > 0 {
		type libTagRow struct {
			LibraryID string
			Tag       string
		}
		var rows []libTagRow
		_ = h.db.Read().Table("item_images").
			Select("items.library_id, item_images.tag").
			Joins("JOIN items ON items.id = item_images.item_id").
			Where("items.library_id IN ? AND items.soft_deleted_at IS NULL AND item_images.kind = ?", needFallback, "Primary").
			Order("items.added_at DESC").
			Scan(&rows).Error
		for _, r := range rows {
			if _, exists := fallbackTags[r.LibraryID]; !exists {
				fallbackTags[r.LibraryID] = r.Tag
			}
		}
	}
	out := make([]map[string]any, 0, len(libs))
	for i := range libs {
		out = append(out, h.libraryViewDTOWithFallback(&libs[i], p, fallbackTags[libs[i].ID], false))
	}
	return out
}

func (h *Handler) virtualLibraryViewDTO(vl *model.VirtualLibrary, p *auth.Principal) map[string]any {
	imageTags := map[string]string{}
	if vl.CoverRevision != "" {
		imageTags["Primary"] = h.signTag(p, vl.ID, "Primary", vl.CoverRevision)
	}
	return map[string]any{
		"Name":                     vl.Name,
		"SortName":                 strings.ToLower(vl.Name),
		"ServerId":                 h.serverID(),
		"Id":                       vl.ID,
		"ParentId":                 "root",
		"Type":                     "CollectionFolder",
		"CollectionType":           "mixed",
		"IsFolder":                 true,
		"LocationType":             "Virtual",
		"DisplayPreferencesId":     vl.ID,
		"PlayAccess":               "Full",
		"CanDelete":                false,
		"CanDownload":              false,
		"EnableMediaSourceDisplay": true,
		"PrimaryImageAspectRatio":  16.0 / 9.0,
		"ImageTags":                imageTags,
		"BackdropImageTags":        []string{},
		"Genres":                   []string{},
		"GenreItems":               []map[string]any{},
		"Tags":                     []string{},
		"TagItems":                 []map[string]any{},
		"Studios":                  []map[string]any{},
		"ExternalUrls":             []any{},
		"ProviderIds":              map[string]string{},
		"DateCreated":              vl.CreatedAt.UTC().Format(time.RFC3339),
		"UserData": map[string]any{
			"PlaybackPositionTicks": 0,
			"PlayCount":             0,
			"IsFavorite":            false,
			"Played":                false,
			"Key":                   vl.ID,
			"ItemId":                vl.ID,
		},
	}
}

func (h *Handler) itemDTO(r *http.Request, p *auth.Principal, item *model.Item, detail bool) map[string]any {
	if item == nil {
		return map[string]any{}
	}
	list := h.itemsToDTOs(r, p, []model.Item{*item}, detail)
	if len(list) == 0 {
		return map[string]any{}
	}
	return list[0]
}

// itemsToDTOs converts a batch of Items into Emby-compatible DTOs using O(1) batched SQL queries
// instead of per-item N+1 queries.
func (h *Handler) itemsToDTOs(r *http.Request, p *auth.Principal, items []model.Item, detail bool) []map[string]any {
	if len(items) == 0 {
		return []map[string]any{}
	}

	rdb := h.db.Read()
	serverID := h.serverID()
	uid := ""
	if p != nil && p.User != nil {
		uid = p.User.ID
	}
	canDownload := p != nil && p.Policy.AllowDownload
	canDelete := p != nil && p.User != nil && p.User.IsAdmin

	itemIDs := make([]string, 0, len(items))
	parentCandidateSet := map[string]bool{}
	var playableItems []model.Item
	var allSeriesIDs []string
	var allSeasonIDs []string
	var seriesTypeIDs []string
	var seasonTypeIDs []string
	var boxsetTypeIDs []string
	for i := range items {
		it := &items[i]
		itemIDs = append(itemIDs, it.ID)
		if it.Type == "Series" {
			allSeriesIDs = append(allSeriesIDs, it.ID)
		} else if it.Type == "Season" {
			allSeasonIDs = append(allSeasonIDs, it.ID)
		}
		if it.Type == "Season" {
			if it.SeriesID != "" {
				parentCandidateSet[it.SeriesID] = true
			}
			if it.ParentID != "" {
				parentCandidateSet[it.ParentID] = true
			}
		}
		if it.Type == "Episode" {
			if it.SeasonID != "" {
				parentCandidateSet[it.SeasonID] = true
			}
			if it.SeriesID != "" {
				parentCandidateSet[it.SeriesID] = true
			}
			if it.ParentID != "" {
				parentCandidateSet[it.ParentID] = true
			}
		}
		if detail {
			if it.Type == "Movie" || it.Type == "Episode" {
				playableItems = append(playableItems, *it)
			} else if it.Type == "Series" {
				seriesTypeIDs = append(seriesTypeIDs, it.ID)
			} else if it.Type == "Season" {
				seasonTypeIDs = append(seasonTypeIDs, it.ID)
			} else if it.Type == "BoxSet" {
				boxsetTypeIDs = append(boxsetTypeIDs, it.ID)
			}
		}
	}

	// 1. Batch-load parent Series/Season hierarchy first so Episodes/Seasons can recover
	// missing or dangling SeriesID/SeasonID links and load the right parent artwork.
	type parentRow struct {
		ID            string
		Type          string
		Name          string
		SeriesID      string
		ParentID      string
		SoftDeletedAt *time.Time
	}
	parentRowsByID := make(map[string]parentRow, len(parentCandidateSet))
	loadParents := func(idSet map[string]bool) {
		if len(idSet) == 0 {
			return
		}
		pIDs := make([]string, 0, len(idSet))
		for pid := range idSet {
			if pid != "" {
				if _, loaded := parentRowsByID[pid]; !loaded {
					pIDs = append(pIDs, pid)
				}
			}
		}
		if len(pIDs) == 0 {
			return
		}
		var rows []parentRow
		_ = rdb.Table("items").
			Select("id, type, name, series_id, parent_id, soft_deleted_at").
			Where("id IN ?", pIDs).
			Scan(&rows).Error
		for _, pr := range rows {
			parentRowsByID[pr.ID] = pr
		}
	}
	loadParents(parentCandidateSet)

	// One optional second-hop batch lookup when an Episode/Season only points to a Season/Folder
	// whose own SeriesID/ParentID was not in the initial candidate set.
	secondHopSet := map[string]bool{}
	for _, pr := range parentRowsByID {
		if pr.SeriesID != "" {
			if _, ok := parentRowsByID[pr.SeriesID]; !ok {
				secondHopSet[pr.SeriesID] = true
			}
		}
		if pr.ParentID != "" && pr.Type != "Series" {
			if _, ok := parentRowsByID[pr.ParentID]; !ok {
				secondHopSet[pr.ParentID] = true
			}
		}
	}
	loadParents(secondHopSet)

	resolveSeriesFor := func(item *model.Item) (string, string) {
		// Try active Series rows first, then soft-deleted Series rows, then path fallback.
		candidates := make([]string, 0, 4)
		addCand := func(id string) {
			if id == "" {
				return
			}
			for _, existing := range candidates {
				if existing == id {
					return
				}
			}
			candidates = append(candidates, id)
		}
		addCand(item.SeriesID)
		if item.SeasonID != "" {
			if sRow, ok := parentRowsByID[item.SeasonID]; ok {
				addCand(sRow.SeriesID)
				if sRow.ParentID != "" {
					if gRow, ok := parentRowsByID[sRow.ParentID]; ok && gRow.Type == "Series" {
						addCand(gRow.ID)
					}
				}
			}
		}
		if item.ParentID != "" {
			if pRow, ok := parentRowsByID[item.ParentID]; ok {
				if pRow.Type == "Series" {
					addCand(pRow.ID)
				} else {
					addCand(pRow.SeriesID)
					if pRow.ParentID != "" {
						if gRow, ok := parentRowsByID[pRow.ParentID]; ok && gRow.Type == "Series" {
							addCand(gRow.ID)
						}
					}
				}
			}
		}

		// Pass 1: active Series row with non-empty Name.
		for _, cid := range candidates {
			if r, ok := parentRowsByID[cid]; ok && r.SoftDeletedAt == nil && r.Type == "Series" && strings.TrimSpace(r.Name) != "" {
				return r.ID, strings.TrimSpace(r.Name)
			}
		}
		// Pass 2: any active row matching SeriesID candidate (e.g. legacy custom type).
		for _, cid := range candidates {
			if r, ok := parentRowsByID[cid]; ok && r.SoftDeletedAt == nil && r.Type != "Season" && strings.TrimSpace(r.Name) != "" {
				return r.ID, strings.TrimSpace(r.Name)
			}
		}
		// Pass 3: soft-deleted Series row (still preserves real show title over omitting SeriesName).
		for _, cid := range candidates {
			if r, ok := parentRowsByID[cid]; ok && r.Type == "Series" && strings.TrimSpace(r.Name) != "" {
				return r.ID, strings.TrimSpace(r.Name)
			}
		}
		// Pass 4: directory path fallback (matching MediaStationGo).
		resolvedID := item.SeriesID
		if resolvedID == "" && len(candidates) > 0 {
			resolvedID = candidates[0]
		}
		return resolvedID, inferSeriesNameFromDirPath(item.DirPath)
	}

	resolveSeasonFor := func(item *model.Item) (string, string) {
		seasonID := item.SeasonID
		if seasonID == "" && item.ParentID != "" {
			if pRow, ok := parentRowsByID[item.ParentID]; ok && pRow.Type == "Season" {
				seasonID = pRow.ID
			}
		}
		if seasonID != "" {
			if sRow, ok := parentRowsByID[seasonID]; ok && strings.TrimSpace(sRow.Name) != "" {
				return seasonID, strings.TrimSpace(sRow.Name)
			}
		}
		return seasonID, defaultSeasonName(item.SeasonNum)
	}

	type resolvedHierarchy struct {
		seriesID   string
		seriesName string
		seasonID   string
		seasonName string
	}
	resolvedByIdx := make([]resolvedHierarchy, len(items))
	seriesIDSet := map[string]bool{}
	seasonIDSet := map[string]bool{}
	for i := range items {
		it := &items[i]
		if it.Type == "Season" {
			sid, sname := resolveSeriesFor(it)
			resolvedByIdx[i] = resolvedHierarchy{seriesID: sid, seriesName: sname}
			if sid != "" {
				seriesIDSet[sid] = true
			}
		} else if it.Type == "Episode" {
			sid, sname := resolveSeriesFor(it)
			ssnID, ssnName := resolveSeasonFor(it)
			resolvedByIdx[i] = resolvedHierarchy{
				seriesID:   sid,
				seriesName: sname,
				seasonID:   ssnID,
				seasonName: ssnName,
			}
			if sid != "" {
				seriesIDSet[sid] = true
			}
			if ssnID != "" {
				seasonIDSet[ssnID] = true
			}
		}
	}

	// 2. Batch-load images (items + resolved parent Series & Seasons for artwork inheritance).
	imgLookupIDs := make([]string, 0, len(itemIDs)+len(seriesIDSet)+len(seasonIDSet))
	imgLookupSet := make(map[string]bool, len(itemIDs)+len(seriesIDSet)+len(seasonIDSet))
	addImgLookup := func(id string) {
		if id != "" && !imgLookupSet[id] {
			imgLookupSet[id] = true
			imgLookupIDs = append(imgLookupIDs, id)
		}
	}
	for _, id := range itemIDs {
		addImgLookup(id)
	}
	for sid := range seriesIDSet {
		addImgLookup(sid)
	}
	for ssnID := range seasonIDSet {
		addImgLookup(ssnID)
	}
	imagesByItem := make(map[string][]model.ItemImage, len(imgLookupIDs))
	var allImgs []model.ItemImage
	_ = rdb.Where("item_id IN ?", imgLookupIDs).Order("index_num ASC").Find(&allImgs).Error
	for _, img := range allImgs {
		imagesByItem[img.ItemID] = append(imagesByItem[img.ItemID], img)
	}

	// 3. Batch-load UserData + Series/Season UnplayedItemCount.
	userDataByItem := make(map[string]model.UserData, len(itemIDs))
	if uid != "" {
		var uds []model.UserData
		_ = rdb.Where("user_id = ? AND item_id IN ?", uid, itemIDs).Find(&uds).Error
		for _, ud := range uds {
			userDataByItem[ud.ItemID] = ud
		}
	}
	unplayedBySeries := make(map[string]int64, len(allSeriesIDs))
	if len(allSeriesIDs) > 0 {
		type unplayedRow struct {
			OwnerID string
			Cnt     int64
		}
		var rows []unplayedRow
		uq := rdb.Table("items").
			Select("series_id AS owner_id, COUNT(*) AS cnt").
			Where("series_id IN ? AND type = ? AND soft_deleted_at IS NULL", allSeriesIDs, "Episode")
		if uid != "" {
			uq = uq.Where("id NOT IN (SELECT item_id FROM user_data WHERE user_id = ? AND played = ?)", uid, true)
		}
		_ = uq.Group("series_id").Scan(&rows).Error
		for _, ur := range rows {
			unplayedBySeries[ur.OwnerID] = ur.Cnt
		}
	}
	unplayedBySeason := make(map[string]int64, len(allSeasonIDs))
	if len(allSeasonIDs) > 0 {
		type unplayedRow struct {
			OwnerID string
			Cnt     int64
		}
		var rows []unplayedRow
		uq := rdb.Table("items").
			Select("season_id AS owner_id, COUNT(*) AS cnt").
			Where("season_id IN ? AND type = ? AND soft_deleted_at IS NULL", allSeasonIDs, "Episode")
		if uid != "" {
			uq = uq.Where("id NOT IN (SELECT item_id FROM user_data WHERE user_id = ? AND played = ?)", uid, true)
		}
		_ = uq.Group("season_id").Scan(&rows).Error
		for _, ur := range rows {
			unplayedBySeason[ur.OwnerID] = ur.Cnt
		}
	}

	// 4. Batch-load Genres.
	genresByItem := make(map[string][]string, len(itemIDs))
	var allGenres []model.ItemGenre
	_ = rdb.Where("item_id IN ?", itemIDs).Find(&allGenres).Error
	for _, g := range allGenres {
		genresByItem[g.ItemID] = append(genresByItem[g.ItemID], g.Genre)
	}

	// 5. Batch-load detail relations when detail == true.
	var (
		tagsByItem         map[string][]string
		peopleByItem       map[string][]map[string]any
		mediaSourcesByItem map[string][]map[string]any
		introByItem        map[string]model.IntroMarker
		introBySeriesSeas  map[string]model.IntroMarker
		seriesSeasonCounts map[string]int64
		seriesEpCounts     map[string]int64
		seasonEpCounts     map[string]int64
		boxsetMemberCounts map[string]int64
		chaptersByItem     map[string][]model.ItemChapter
	)

	if detail {
		tagsByItem = make(map[string][]string, len(itemIDs))
		var allTags []model.ItemTag
		_ = rdb.Where("item_id IN ?", itemIDs).Find(&allTags).Error
		for _, t := range allTags {
			tagsByItem[t.ItemID] = append(tagsByItem[t.ItemID], t.Tag)
		}
		chaptersByItem = make(map[string][]model.ItemChapter, len(itemIDs))
		var allChapters []model.ItemChapter
		_ = rdb.Where("item_id IN ?", itemIDs).Order("item_id, index_num").Find(&allChapters).Error
		for _, c := range allChapters {
			chaptersByItem[c.ItemID] = append(chaptersByItem[c.ItemID], c)
		}

		type personRow struct {
			ItemID    string
			PersonID  string
			Name      string
			TMDBID    string
			RoleType  string
			RoleName  string
			ThumbPath string
		}
		var pRows []personRow
		_ = rdb.Table("item_people").
			Select("item_people.item_id, item_people.person_id, people.name, people.tmdb_id, item_people.role_type, item_people.role_name, people.thumb_path").
			Joins("JOIN people ON people.id = item_people.person_id").
			Where("item_people.item_id IN ?", itemIDs).
			Order("CASE item_people.role_type WHEN 'Actor' THEN 0 WHEN 'GuestStar' THEN 1 WHEN 'Director' THEN 2 WHEN 'Writer' THEN 3 WHEN 'Producer' THEN 4 ELSE 5 END ASC, item_people.sort_idx ASC, item_people.person_id ASC").
			Scan(&pRows).Error
		metaDir := h.embyMetadataDir()
		canTMDB := h.canLookupTMDBPortraits()
		reqCtx := context.Background()
		if r != nil {
			reqCtx = r.Context()
		}
		embyPersonImgCache := make(map[string]bool)
		peopleByItem = make(map[string][]map[string]any, len(itemIDs))
		for _, pr := range pRows {
			pe := map[string]any{
				"Id":   pr.PersonID,
				"Name": pr.Name,
				"Role": pr.RoleName,
				"Type": pr.RoleType,
			}
			if h.personHasPrimaryImage(reqCtx, metaDir, canTMDB, pr.PersonID, pr.Name, pr.TMDBID, pr.ThumbPath, embyPersonImgCache) {
				pe["PrimaryImageTag"] = h.signTag(p, pr.PersonID, "Primary", "person")
			}
			peopleByItem[pr.ItemID] = append(peopleByItem[pr.ItemID], pe)
		}

		if len(playableItems) > 0 {
			mediaSourcesByItem = h.playback.BuildMediaSourcesBatch(r, p, playableItems)

			playableIDs := make([]string, 0, len(playableItems))
			playSeriesSet := map[string]bool{}
			for i := range playableItems {
				playableIDs = append(playableIDs, playableItems[i].ID)
				if playableItems[i].SeriesID != "" {
					playSeriesSet[playableItems[i].SeriesID] = true
				}
			}
			introByItem = make(map[string]model.IntroMarker)
			introBySeriesSeas = make(map[string]model.IntroMarker)
			var markers []model.IntroMarker
			mq := rdb.Where("item_id IN ?", playableIDs)
			if len(playSeriesSet) > 0 {
				sIDs := make([]string, 0, len(playSeriesSet))
				for sid := range playSeriesSet {
					sIDs = append(sIDs, sid)
				}
				mq = rdb.Where("item_id IN ? OR (series_id IN ? AND item_id = '')", playableIDs, sIDs)
			}
			_ = mq.Find(&markers).Error
			for _, mk := range markers {
				if mk.ItemID != "" {
					introByItem[mk.ItemID] = mk
				} else if mk.SeriesID != "" {
					introBySeriesSeas[seriesSeasonKey(mk.SeriesID, mk.SeasonNum)] = mk
				}
			}
		}

		if len(seriesTypeIDs) > 0 {
			seriesSeasonCounts = make(map[string]int64, len(seriesTypeIDs))
			seriesEpCounts = make(map[string]int64, len(seriesTypeIDs))
			type cntRow struct {
				SeriesID string
				Type     string
				Cnt      int64
			}
			var cnts []cntRow
			_ = rdb.Table("items").
				Select("series_id, type, COUNT(*) AS cnt").
				Where("series_id IN ? AND type IN ? AND soft_deleted_at IS NULL", seriesTypeIDs, []string{"Season", "Episode"}).
				Group("series_id, type").
				Scan(&cnts).Error
			for _, cr := range cnts {
				if cr.Type == "Season" {
					seriesSeasonCounts[cr.SeriesID] = cr.Cnt
				} else if cr.Type == "Episode" {
					seriesEpCounts[cr.SeriesID] = cr.Cnt
				}
			}
		}

		if len(seasonTypeIDs) > 0 {
			seasonEpCounts = make(map[string]int64, len(seasonTypeIDs))
			type cntRow struct {
				SeasonID string
				Cnt      int64
			}
			var cnts []cntRow
			_ = rdb.Table("items").
				Select("season_id, COUNT(*) AS cnt").
				Where("season_id IN ? AND type = ? AND soft_deleted_at IS NULL", seasonTypeIDs, "Episode").
				Group("season_id").
				Scan(&cnts).Error
			for _, cr := range cnts {
				seasonEpCounts[cr.SeasonID] = cr.Cnt
			}
		}

		if len(boxsetTypeIDs) > 0 {
			boxsetMemberCounts = make(map[string]int64, len(boxsetTypeIDs))
			type boxCntRow struct {
				BoxSetID string
				Cnt      int64
			}
			var cnts []boxCntRow
			_ = rdb.Table("box_set_items").
				Select("box_set_id, COUNT(*) AS cnt").
				Where("box_set_id IN ?", boxsetTypeIDs).
				Group("box_set_id").
				Scan(&cnts).Error
			for _, cr := range cnts {
				boxsetMemberCounts[cr.BoxSetID] = cr.Cnt
			}
		}
	}

	out := make([]map[string]any, 0, len(items))
	for i := range items {
		item := &items[i]

		imageTags := map[string]string{}
		backdropTags := []string{}
		for _, img := range imagesByItem[item.ID] {
			signed := h.signTag(p, item.ID, img.Kind, img.Tag)
			if strings.EqualFold(img.Kind, "Backdrop") {
				backdropTags = append(backdropTags, signed)
			} else if _, exists := imageTags[img.Kind]; !exists {
				imageTags[img.Kind] = signed
			}
		}

		ud := userDataByItem[item.ID]
		userDataMap := map[string]any{
			"PlaybackPositionTicks": ud.PositionTicks,
			"PlayCount":             ud.PlayCount,
			"IsFavorite":            ud.IsFavorite,
			"Played":                ud.Played,
			"Key":                   item.ID,
			"ItemId":                item.ID,
		}
		if item.Type == "Series" {
			userDataMap["UnplayedItemCount"] = unplayedBySeries[item.ID]
		} else if item.Type == "Season" {
			userDataMap["UnplayedItemCount"] = unplayedBySeason[item.ID]
		}
		if ud.LastPlayedAt != nil {
			userDataMap["LastPlayedDate"] = ud.LastPlayedAt.UTC().Format(time.RFC3339)
		}
		if item.RuntimeTicks > 0 && ud.PositionTicks > 0 {
			pct := float64(ud.PositionTicks) / float64(item.RuntimeTicks) * 100
			if pct > 100 {
				pct = 100
			}
			userDataMap["PlayedPercentage"] = pct
		}

		isFolder := item.Type == "Series" || item.Type == "Season" || item.Type == "Folder" || item.Type == "CollectionFolder" || item.Type == "BoxSet"
		mediaType := ""
		if item.Type == "Movie" || item.Type == "Episode" {
			mediaType = "Video"
		}
		aspectRatio := 2.0 / 3.0
		if item.Type == "Episode" {
			aspectRatio = 16.0 / 9.0
		}

		m := map[string]any{
			"Name":                    item.Name,
			"OriginalTitle":           item.OriginalName,
			"SortName":                item.SortName,
			"ServerId":                serverID,
			"Id":                      item.ID,
			"Etag":                    itemEtag(item),
			"ParentId":                item.ParentID,
			"Type":                    item.Type,
			"IsFolder":                isFolder,
			"Overview":                item.Overview,
			"DateCreated":             item.AddedAt.UTC().Format(time.RFC3339),
			"ImageTags":               imageTags,
			"BackdropImageTags":       backdropTags,
			"PrimaryImageAspectRatio": aspectRatio,
			"UserData":                userDataMap,
			"CanDownload":             canDownload,
			"CanDelete":               canDelete,
			"LocationType":            "FileSystem",
			"PlayAccess":              "Full",
			"DisplayPreferencesId":    item.ID,
			"ExternalUrls":            []any{},
			"RemoteTrailers":          []any{},
			"SupportsSync":            false,
		}
		if primaryTag := imageTags["Primary"]; primaryTag != "" {
			m["PrimaryImageItemId"] = item.ID
			m["PrimaryImageTag"] = primaryTag
		}
		if mediaType != "" {
			m["MediaType"] = mediaType
		}

		if item.Year > 0 {
			m["ProductionYear"] = item.Year
		}
		if item.PremiereDate != "" {
			if len(item.PremiereDate) == 10 {
				m["PremiereDate"] = item.PremiereDate + "T00:00:00.0000000Z"
			} else {
				m["PremiereDate"] = item.PremiereDate
			}
		}
		if item.CommunityRating > 0 {
			m["CommunityRating"] = item.CommunityRating
		}
		if item.OfficialRating != "" {
			m["OfficialRating"] = item.OfficialRating
		}
		if item.RuntimeTicks > 0 {
			m["RunTimeTicks"] = item.RuntimeTicks
		}
		if item.CustomRating != "" {
			m["CustomRating"] = item.CustomRating
		}
		if item.CriticRating != 0 {
			m["CriticRating"] = item.CriticRating
		}
		if item.Status != "" {
			m["Status"] = embySeriesStatus(item.Status)
		}
		if dispOrder := embyDisplayOrder(item.DisplayOrder); dispOrder != "" {
			m["DisplayOrder"] = dispOrder
		}
		if item.ProductionLocationsJSON != "" {
			var places []string
			if json.Unmarshal([]byte(item.ProductionLocationsJSON), &places) == nil && len(places) > 0 {
				m["ProductionLocations"] = places
			}
		}
		if item.Tagline != "" {
			m["Taglines"] = []string{item.Tagline}
		}

		if item.Type == "Season" {
			rh := resolvedByIdx[i]
			m["IndexNumber"] = item.SeasonNum
			if rh.seriesID != "" {
				m["SeriesId"] = rh.seriesID
			}
			if rh.seriesName != "" {
				m["SeriesName"] = rh.seriesName
			}
			var parentBackdrops []string
			for _, sImg := range imagesByItem[rh.seriesID] {
				switch {
				case strings.EqualFold(sImg.Kind, "Primary"):
					if _, exists := m["SeriesPrimaryImageTag"]; !exists {
						tag := h.signTag(p, rh.seriesID, "Primary", sImg.Tag)
						m["SeriesPrimaryImageTag"] = tag
						m["ParentPrimaryImageItemId"] = rh.seriesID
						m["ParentPrimaryImageTag"] = tag
						if _, hasOwnPrimary := m["PrimaryImageItemId"]; !hasOwnPrimary {
							m["PrimaryImageItemId"] = rh.seriesID
							m["PrimaryImageTag"] = tag
						}
					}
				case strings.EqualFold(sImg.Kind, "Backdrop"):
					parentBackdrops = append(parentBackdrops, h.signTag(p, rh.seriesID, sImg.Kind, sImg.Tag))
				case strings.EqualFold(sImg.Kind, "Thumb"):
					if imageTags["Thumb"] == "" {
						if _, exists := m["ParentThumbImageTag"]; !exists {
							m["ParentThumbItemId"] = rh.seriesID
							m["ParentThumbImageTag"] = h.signTag(p, rh.seriesID, "Thumb", sImg.Tag)
						}
					}
				case strings.EqualFold(sImg.Kind, "Logo"):
					if imageTags["Logo"] == "" {
						if _, exists := m["ParentLogoImageTag"]; !exists {
							m["ParentLogoItemId"] = rh.seriesID
							m["ParentLogoImageTag"] = h.signTag(p, rh.seriesID, "Logo", sImg.Tag)
						}
					}
				}
			}
			if len(parentBackdrops) > 0 {
				m["ParentBackdropItemId"] = rh.seriesID
				m["ParentBackdropImageTags"] = parentBackdrops
			}
		}
		if item.Type == "Episode" {
			rh := resolvedByIdx[i]
			m["IndexNumber"] = item.EpisodeNum
			m["ParentIndexNumber"] = item.SeasonNum
			if rh.seasonID != "" {
				m["SeasonId"] = rh.seasonID
			}
			if rh.seriesID != "" {
				m["SeriesId"] = rh.seriesID
			}
			if rh.seasonName != "" {
				m["SeasonName"] = rh.seasonName
			}
			if rh.seriesName != "" {
				m["SeriesName"] = rh.seriesName
			}
			var seasonPrimaryTag string
			if rh.seasonID != "" {
				for _, ssnImg := range imagesByItem[rh.seasonID] {
					if strings.EqualFold(ssnImg.Kind, "Primary") {
						seasonPrimaryTag = h.signTag(p, rh.seasonID, "Primary", ssnImg.Tag)
						break
					}
				}
			}
			var parentBackdrops []string
			var seriesPrimaryTag string
			if rh.seriesID != "" {
				for _, sImg := range imagesByItem[rh.seriesID] {
					switch {
					case strings.EqualFold(sImg.Kind, "Primary"):
						if seriesPrimaryTag == "" {
							seriesPrimaryTag = h.signTag(p, rh.seriesID, "Primary", sImg.Tag)
							m["SeriesPrimaryImageTag"] = seriesPrimaryTag
						}
					case strings.EqualFold(sImg.Kind, "Backdrop"):
						parentBackdrops = append(parentBackdrops, h.signTag(p, rh.seriesID, sImg.Kind, sImg.Tag))
					case strings.EqualFold(sImg.Kind, "Thumb"):
						if imageTags["Thumb"] == "" {
							if _, exists := m["ParentThumbImageTag"]; !exists {
								m["ParentThumbItemId"] = rh.seriesID
								m["ParentThumbImageTag"] = h.signTag(p, rh.seriesID, "Thumb", sImg.Tag)
							}
						}
					case strings.EqualFold(sImg.Kind, "Logo"):
						if imageTags["Logo"] == "" {
							if _, exists := m["ParentLogoImageTag"]; !exists {
								m["ParentLogoItemId"] = rh.seriesID
								m["ParentLogoImageTag"] = h.signTag(p, rh.seriesID, "Logo", sImg.Tag)
							}
						}
					}
				}
			}
			if seasonPrimaryTag != "" {
				m["ParentPrimaryImageItemId"] = rh.seasonID
				m["ParentPrimaryImageTag"] = seasonPrimaryTag
			} else if seriesPrimaryTag != "" {
				m["ParentPrimaryImageItemId"] = rh.seriesID
				m["ParentPrimaryImageTag"] = seriesPrimaryTag
			}
			if _, hasOwnPrimary := m["PrimaryImageItemId"]; !hasOwnPrimary && rh.seriesID != "" {
				m["PrimaryImageItemId"] = rh.seriesID
				if seriesPrimaryTag != "" {
					m["PrimaryImageTag"] = seriesPrimaryTag
				}
			}
			if len(parentBackdrops) > 0 {
				m["ParentBackdropItemId"] = rh.seriesID
				m["ParentBackdropImageTags"] = parentBackdrops
			}
		}

		providerIDs := map[string]string{}
		if item.ProviderIDsJSON != "" {
			_ = json.Unmarshal([]byte(item.ProviderIDsJSON), &providerIDs)
		}
		m["ProviderIds"] = providerIDs

		gNames := genresByItem[item.ID]
		if gNames == nil {
			gNames = []string{}
		}
		// Emby OpenAPI BaseItemDto.GenreItems is NameLongIdPair[] (Id MUST be int64).
		genreItems := make([]map[string]any, 0, len(gNames))
		for _, g := range gNames {
			genreItems = append(genreItems, map[string]any{"Name": g, "Id": stableNumericID("genre", g)})
		}
		m["Genres"] = gNames
		m["GenreItems"] = genreItems

		if detail {
			tNames := tagsByItem[item.ID]
			if tNames == nil {
				tNames = []string{}
			}
			tagItems := make([]map[string]any, 0, len(tNames))
			for _, t := range tNames {
				tagItems = append(tagItems, map[string]any{"Name": t, "Id": stableNumericID("tag", t)})
			}
			m["Tags"] = tNames
			m["TagItems"] = tagItems

			var studios []string
			if item.StudiosJSON != "" {
				_ = json.Unmarshal([]byte(item.StudiosJSON), &studios)
			}
			// Emby OpenAPI BaseItemDto.Studios is NameLongIdPair[] (Id MUST be int64; Yamby enforces numeric IDs).
			studioObjs := make([]map[string]any, 0, len(studios))
			for _, st := range studios {
				st = strings.TrimSpace(st)
				if st == "" {
					continue
				}
				studioObjs = append(studioObjs, map[string]any{"Name": st, "Id": stableNumericID("studio", st)})
			}
			m["Studios"] = studioObjs

			pList := peopleByItem[item.ID]
			if pList == nil {
				pList = []map[string]any{}
			}
			m["People"] = pList

			if item.Type == "Movie" || item.Type == "Episode" {
				sources := mediaSourcesByItem[item.ID]
				if sources == nil {
					sources = []map[string]any{}
				}
				m["MediaSources"] = sources
				m["MediaSourceCount"] = len(sources)
				if len(sources) > 0 {
					applyMediaSourceSummaryToItemDTO(m, sources[0])
				}
				if cs := chaptersByItem[item.ID]; len(cs) > 0 {
					chapters := make([]map[string]any, 0, len(cs))
					for idx, c := range cs {
						chIdx := c.IndexNum
						if chIdx < 0 {
							chIdx = idx
						}
						chapters = append(chapters, map[string]any{
							"StartPositionTicks": c.StartTicks,
							"Name":               c.Name,
							"MarkerType":         embyChapterMarkerName(c.MarkerType),
							"ChapterIndex":       chIdx,
						})
					}
					m["Chapters"] = chapters
				} else if mk, ok := introByItem[item.ID]; ok {
					if chapters := introMarkerToChapters(&mk); len(chapters) > 0 {
						m["Chapters"] = chapters
					}
				} else if item.SeriesID != "" {
					if mk, ok := introBySeriesSeas[seriesSeasonKey(item.SeriesID, item.SeasonNum)]; ok {
						if chapters := introMarkerToChapters(&mk); len(chapters) > 0 {
							m["Chapters"] = chapters
						}
					}
				}
			}

			if item.Type == "Series" {
				if strings.TrimSpace(item.DirPath) != "" {
					m["Path"] = item.DirPath
				}
				m["ChildCount"] = seriesSeasonCounts[item.ID]
				m["RecursiveItemCount"] = seriesEpCounts[item.ID]
			} else if item.Type == "Season" {
				if strings.TrimSpace(item.DirPath) != "" {
					m["Path"] = item.DirPath
				}
				m["ChildCount"] = seasonEpCounts[item.ID]
			} else if item.Type == "BoxSet" {
				m["ChildCount"] = boxsetMemberCounts[item.ID]
			} else if item.Type == "Folder" && strings.TrimSpace(item.DirPath) != "" {
				m["Path"] = item.DirPath
			}
		}

		out = append(out, m)
	}
	return out
}

func seriesSeasonKey(seriesID string, seasonNum int) string {
	return seriesID + ":" + string(rune(seasonNum+48)) + ":" + itoaSimple(seasonNum)
}

func itoaSimple(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func (h *Handler) getItemByID(id string) *model.Item {
	if id == "" {
		return nil
	}
	var item model.Item
	if err := h.db.Read().Where("id = ? AND soft_deleted_at IS NULL", id).First(&item).Error; err != nil {
		return nil
	}
	return &item
}

func (h *Handler) signTag(p *auth.Principal, itemID, kind, rawTag string) string {
	uid := "anon"
	if p != nil && p.User != nil {
		uid = p.User.ID
	}
	return auth.SignImageTag(h.auth.ImageSecret(), uid, itemID, kind, rawTag)
}

func introMarkerToChapters(marker *model.IntroMarker) []map[string]any {
	var chapters []map[string]any
	if marker.IntroEndTicks > marker.IntroStartTicks {
		chapters = append(chapters,
			map[string]any{
				"StartPositionTicks": marker.IntroStartTicks,
				"Name":               "Intro Start",
				"MarkerType":         "IntroStart",
				"ChapterIndex":       len(chapters),
			},
			map[string]any{
				"StartPositionTicks": marker.IntroEndTicks,
				"Name":               "Intro End",
				"MarkerType":         "IntroEnd",
				"ChapterIndex":       len(chapters) + 1,
			},
		)
	}
	if marker.CreditsStartTicks > 0 {
		chapters = append(chapters, map[string]any{
			"StartPositionTicks": marker.CreditsStartTicks,
			"Name":               "Credits",
			"MarkerType":         "CreditsStart",
			"ChapterIndex":       len(chapters),
		})
	}
	return chapters
}

func embySeriesStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "continuing", "0":
		return "Continuing"
	case "2", "ended":
		return "Ended"
	default:
		return strings.TrimSpace(raw)
	}
}

func embyChapterMarkerName(markerType int) string {
	switch markerType {
	case 1:
		return "IntroStart"
	case 2:
		return "IntroEnd"
	case 3:
		return "CreditsStart"
	default:
		return "Chapter"
	}
}

func embyDisplayOrder(raw int) string {
	switch raw {
	case 1:
		return "aired"
	case 2:
		return "dvd"
	case 3:
		return "absolute"
	default:
		return ""
	}
}

func itemEtag(item *model.Item) string {
	if item == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(item.ID + "|" + strconv.FormatInt(item.UpdatedAt.UnixNano(), 10)))
	return hex.EncodeToString(sum[:8])
}

func applyMediaSourceSummaryToItemDTO(m map[string]any, src map[string]any) {
	delete(m, "Path")
	delete(m, "MediaStreams")
	delete(m, "HasSubtitles")
	delete(m, "Container")
	delete(m, "Width")
	delete(m, "Height")
	delete(m, "IsHD")
	if src == nil {
		return
	}
	if pathStr, _ := src["Path"].(string); strings.TrimSpace(pathStr) != "" {
		m["Path"] = pathStr
	}
	if streams, ok := src["MediaStreams"]; ok {
		m["MediaStreams"] = streams
		hasSubs := false
		if sList, ok := streams.([]map[string]any); ok {
			for _, st := range sList {
				if stType, _ := st["Type"].(string); strings.EqualFold(stType, "Subtitle") {
					hasSubs = true
					break
				}
			}
		}
		m["HasSubtitles"] = hasSubs
	}
	if container, ok := src["Container"]; ok {
		m["Container"] = container
	}
	w, _ := src["Width"].(int)
	h, _ := src["Height"].(int)
	if w > 0 {
		m["Width"] = w
	}
	if h > 0 {
		m["Height"] = h
	}
	if w > 0 || h > 0 {
		m["IsHD"] = w >= 1280 || h >= 720
	}
	if rt, ok := src["RunTimeTicks"].(int64); ok && rt > 0 {
		m["RunTimeTicks"] = rt
	}
}

func applySelectedMediaSourceToItemDTO(m map[string]any, itemID, selectedSourceID string) {
	selectedSourceID = strings.TrimSpace(selectedSourceID)
	if m == nil || selectedSourceID == "" {
		return
	}
	sources, ok := m["MediaSources"].([]map[string]any)
	if !ok {
		return
	}
	filtered := playback.FilterMediaSources(sources, itemID, selectedSourceID)
	m["MediaSources"] = filtered
	if len(filtered) > 0 {
		applyMediaSourceSummaryToItemDTO(m, filtered[0])
	} else {
		applyMediaSourceSummaryToItemDTO(m, nil)
		m["MediaStreams"] = []map[string]any{}
		m["HasSubtitles"] = false
	}
}
