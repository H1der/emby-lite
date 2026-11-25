package emby

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"emby-lite/internal/auth"
	imgpipe "emby-lite/internal/image"
	"emby-lite/internal/model"
)

const ProtocolVersion = "4.8.0.80"

func (h *Handler) serverID() string {
	return h.db.GetSetting("server_id", "emby-lite-server")
}

func (h *Handler) serverName() string {
	return h.db.GetSetting("server_name", h.cfg.Server.ServerName)
}

func (h *Handler) userDTO(u *model.User, policy auth.EffectivePolicy) map[string]any {
	lastLogin := ""
	if u.LastLoginAt != nil {
		lastLogin = u.LastLoginAt.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"Name":                  u.Username,
		"ServerId":              h.serverID(),
		"Id":                    u.ID,
		"HasPassword":           true,
		"HasConfiguredPassword": true,
		"LastLoginDate":         lastLogin,
		"LastActivityDate":      lastLogin,
		"Configuration": map[string]any{
			"PlayDefaultAudioTrack":      true,
			"DisplayMissingEpisodes":     false,
			"SubtitleMode":               "Default",
			"EnableLocalPassword":        false,
			"OrderedViews":               []string{},
			"LatestItemsExcludes":        []string{},
			"MyMediaExcludes":            []string{},
			"HidePlayedInLatest":         true,
			"RememberAudioSelections":    true,
			"RememberSubtitleSelections": true,
			"EnableNextEpisodeAutoPlay":  true,
		},
		"Policy": map[string]any{
			"IsAdministrator":                u.IsAdmin,
			"IsHidden":                       false,
			"IsDisabled":                     u.Disabled,
			"EnableMediaPlayback":            policy.AllowPlayback,
			"EnableAudioPlaybackTranscoding": false,
			"EnableVideoPlaybackTranscoding": false,
			"EnablePlaybackRemuxing":         false,
			"EnableContentDownloading":       policy.AllowDownload,
			"EnableSyncTranscoding":          false,
			"EnableAllFolders":               u.IsAdmin,
			"SimultaneousStreamLimit":        policy.MaxConcurrentStreams,
		},
	}
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
		"Name":           lib.Name,
		"ServerId":       h.serverID(),
		"Id":             lib.ID,
		"Type":           "CollectionFolder",
		"CollectionType": collectionType,
		"IsFolder":       true,
		"ImageTags":      imageTags,
		"ChildCount":     lib.ItemCount,
		"DateCreated":    lib.CreatedAt.UTC().Format(time.RFC3339),
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
		"Name":           vl.Name,
		"ServerId":       h.serverID(),
		"Id":             vl.ID,
		"Type":           "CollectionFolder",
		"CollectionType": "mixed",
		"IsFolder":       true,
		"ImageTags":      imageTags,
		"DateCreated":    vl.CreatedAt.UTC().Format(time.RFC3339),
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
	parentIDSet := map[string]bool{}
	seriesIDSet := map[string]bool{}
	seasonIDSet := map[string]bool{}
	var playableItems []model.Item
	var seriesTypeIDs []string
	var seasonTypeIDs []string

	for i := range items {
		it := &items[i]
		itemIDs = append(itemIDs, it.ID)
		if it.Type == "Season" && it.SeriesID != "" {
			parentIDSet[it.SeriesID] = true
			seriesIDSet[it.SeriesID] = true
		}
		if it.Type == "Episode" {
			if it.SeasonID != "" {
				parentIDSet[it.SeasonID] = true
				seasonIDSet[it.SeasonID] = true
			}
			if it.SeriesID != "" {
				parentIDSet[it.SeriesID] = true
				seriesIDSet[it.SeriesID] = true
			}
		}
		if detail {
			if it.Type == "Movie" || it.Type == "Episode" {
				playableItems = append(playableItems, *it)
			} else if it.Type == "Series" {
				seriesTypeIDs = append(seriesTypeIDs, it.ID)
			} else if it.Type == "Season" {
				seasonTypeIDs = append(seasonTypeIDs, it.ID)
			}
		}
	}

	// 1. Batch-load images (items + parent series for episode SeriesPrimaryImageTag).
	imgLookupIDs := make([]string, 0, len(itemIDs)+len(seriesIDSet))
	imgLookupSet := make(map[string]bool, len(itemIDs)+len(seriesIDSet))
	for _, id := range itemIDs {
		imgLookupSet[id] = true
		imgLookupIDs = append(imgLookupIDs, id)
	}
	for sid := range seriesIDSet {
		if !imgLookupSet[sid] {
			imgLookupSet[sid] = true
			imgLookupIDs = append(imgLookupIDs, sid)
		}
	}
	imagesByItem := make(map[string][]model.ItemImage, len(imgLookupIDs))
	var allImgs []model.ItemImage
	_ = rdb.Where("item_id IN ?", imgLookupIDs).Order("index_num ASC").Find(&allImgs).Error
	for _, img := range allImgs {
		imagesByItem[img.ItemID] = append(imagesByItem[img.ItemID], img)
	}

	// 2. Batch-load UserData.
	userDataByItem := make(map[string]model.UserData, len(itemIDs))
	if uid != "" {
		var uds []model.UserData
		_ = rdb.Where("user_id = ? AND item_id IN ?", uid, itemIDs).Find(&uds).Error
		for _, ud := range uds {
			userDataByItem[ud.ItemID] = ud
		}
	}

	// 3. Batch-load Genres.
	genresByItem := make(map[string][]string, len(itemIDs))
	var allGenres []model.ItemGenre
	_ = rdb.Where("item_id IN ?", itemIDs).Find(&allGenres).Error
	for _, g := range allGenres {
		genresByItem[g.ItemID] = append(genresByItem[g.ItemID], g.Genre)
	}

	// 4. Batch-load parent Series/Season names if needed.
	parentNames := make(map[string]string, len(parentIDSet))
	if len(parentIDSet) > 0 {
		pIDs := make([]string, 0, len(parentIDSet))
		for pid := range parentIDSet {
			pIDs = append(pIDs, pid)
		}
		type nameRow struct {
			ID   string
			Name string
		}
		var pRows []nameRow
		_ = rdb.Table("items").Select("id, name").Where("id IN ? AND soft_deleted_at IS NULL", pIDs).Scan(&pRows).Error
		for _, pr := range pRows {
			parentNames[pr.ID] = pr.Name
		}
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
	)

	if detail {
		tagsByItem = make(map[string][]string, len(itemIDs))
		var allTags []model.ItemTag
		_ = rdb.Where("item_id IN ?", itemIDs).Find(&allTags).Error
		for _, t := range allTags {
			tagsByItem[t.ItemID] = append(tagsByItem[t.ItemID], t.Tag)
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
			Order("item_people.sort_idx ASC").
			Scan(&pRows).Error
		metaDir := h.embyMetadataDir()
		embyPersonImgCache := make(map[string]bool)
		peopleByItem = make(map[string][]map[string]any, len(itemIDs))
		for _, pr := range pRows {
			pe := map[string]any{
				"Id":   pr.PersonID,
				"Name": pr.Name,
				"Role": pr.RoleName,
				"Type": pr.RoleType,
			}
			hasImg := pr.ThumbPath != ""
			if !hasImg && metaDir != "" {
				if cached, ok := embyPersonImgCache[pr.PersonID]; ok {
					hasImg = cached
				} else {
					hasImg = imgpipe.FindEmbyPersonImage(metaDir, pr.Name, pr.TMDBID) != ""
					embyPersonImgCache[pr.PersonID] = hasImg
				}
			}
			if hasImg {
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

		isFolder := item.Type == "Series" || item.Type == "Season" || item.Type == "Folder" || item.Type == "CollectionFolder"
		mediaType := ""
		if item.Type == "Movie" || item.Type == "Episode" {
			mediaType = "Video"
		}

		m := map[string]any{
			"Name":              item.Name,
			"OriginalTitle":     item.OriginalName,
			"SortName":          item.SortName,
			"ServerId":          serverID,
			"Id":                item.ID,
			"ParentId":          item.ParentID,
			"Type":              item.Type,
			"IsFolder":          isFolder,
			"MediaType":         mediaType,
			"Overview":          item.Overview,
			"DateCreated":       item.AddedAt.UTC().Format(time.RFC3339),
			"ImageTags":         imageTags,
			"BackdropImageTags": backdropTags,
			"UserData":          userDataMap,
			"CanDownload":       canDownload,
			"CanDelete":         canDelete,
			"LocationType":      "FileSystem",
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
		if item.Tagline != "" {
			m["Taglines"] = []string{item.Tagline}
		}

		if item.Type == "Season" {
			m["IndexNumber"] = item.SeasonNum
			m["SeriesId"] = item.SeriesID
			if sn := parentNames[item.SeriesID]; sn != "" {
				m["SeriesName"] = sn
			}
		}
		if item.Type == "Episode" {
			m["IndexNumber"] = item.EpisodeNum
			m["ParentIndexNumber"] = item.SeasonNum
			m["SeasonId"] = item.SeasonID
			m["SeriesId"] = item.SeriesID
			if sn := parentNames[item.SeasonID]; sn != "" {
				m["SeasonName"] = sn
			}
			if sn := parentNames[item.SeriesID]; sn != "" {
				m["SeriesName"] = sn
			}
			for _, sImg := range imagesByItem[item.SeriesID] {
				if sImg.Kind == "Primary" {
					m["SeriesPrimaryImageTag"] = h.signTag(p, item.SeriesID, "Primary", sImg.Tag)
					break
				}
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
		genreItems := make([]map[string]any, 0, len(gNames))
		for _, g := range gNames {
			genreItems = append(genreItems, map[string]any{"Name": g, "Id": "genre-" + g})
		}
		m["Genres"] = gNames
		m["GenreItems"] = genreItems

		if detail {
			tNames := tagsByItem[item.ID]
			if tNames == nil {
				tNames = []string{}
			}
			m["Tags"] = tNames

			var studios []string
			if item.StudiosJSON != "" {
				_ = json.Unmarshal([]byte(item.StudiosJSON), &studios)
			}
			studioObjs := make([]map[string]any, 0, len(studios))
			for _, st := range studios {
				studioObjs = append(studioObjs, map[string]any{"Name": st})
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
					if streams, ok := sources[0]["MediaStreams"]; ok {
						m["MediaStreams"] = streams
					}
					if container, ok := sources[0]["Container"]; ok {
						m["Container"] = container
					}
				}
				if mk, ok := introByItem[item.ID]; ok {
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
				m["ChildCount"] = seriesSeasonCounts[item.ID]
				m["RecursiveItemCount"] = seriesEpCounts[item.ID]
			} else if item.Type == "Season" {
				m["ChildCount"] = seasonEpCounts[item.ID]
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
			},
			map[string]any{
				"StartPositionTicks": marker.IntroEndTicks,
				"Name":               "Intro End",
				"MarkerType":         "IntroEnd",
			},
		)
	}
	if marker.CreditsStartTicks > 0 {
		chapters = append(chapters, map[string]any{
			"StartPositionTicks": marker.CreditsStartTicks,
			"Name":               "Credits",
			"MarkerType":         "CreditsStart",
		})
	}
	return chapters
}
