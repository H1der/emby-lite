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
			"IsAdministrator":            u.IsAdmin,
			"IsHidden":                   false,
			"IsDisabled":                 u.Disabled,
			"EnableMediaPlayback":        policy.AllowPlayback,
			"EnableAudioPlaybackTranscoding": false,
			"EnableVideoPlaybackTranscoding": false,
			"EnablePlaybackRemuxing":     false,
			"EnableContentDownloading":   policy.AllowDownload,
			"EnableSyncTranscoding":      false,
			"EnableAllFolders":           u.IsAdmin,
			"SimultaneousStreamLimit":    policy.MaxConcurrentStreams,
		},
	}
}

func (h *Handler) libraryViewDTO(lib *model.Library, p *auth.Principal) map[string]any {
	collectionType := "movies"
	switch lib.Kind {
	case "tvshows":
		collectionType = "tvshows"
	case "mixed":
		collectionType = "mixed"
	}

	imageTags := map[string]string{}
	// If library has a custom cover or a first item poster, expose Primary tag.
	if lib.CoverRevision != "" {
		imageTags["Primary"] = h.signTag(p, lib.ID, "Primary", lib.CoverRevision)
	} else {
		var firstImg model.ItemImage
		err := h.db.Gorm.Table("item_images").
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
	uid := ""
	if p != nil && p.User != nil {
		uid = p.User.ID
	}

	// Load images.
	var imgs []model.ItemImage
	_ = h.db.Gorm.Where("item_id = ?", item.ID).Order("index_num ASC").Find(&imgs).Error
	imageTags := map[string]string{}
	backdropTags := []string{}
	for _, img := range imgs {
		signed := h.signTag(p, item.ID, img.Kind, img.Tag)
		if strings.EqualFold(img.Kind, "Backdrop") {
			backdropTags = append(backdropTags, signed)
		} else if _, exists := imageTags[img.Kind]; !exists {
			imageTags[img.Kind] = signed
		}
	}

	// Load UserData.
	var ud model.UserData
	if uid != "" {
		_ = h.db.Gorm.Where("user_id = ? AND item_id = ?", uid, item.ID).First(&ud).Error
	}
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
		"ServerId":          h.serverID(),
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
		"CanDownload":       p != nil && p.Policy.AllowDownload,
		"CanDelete":         p != nil && p.User != nil && p.User.IsAdmin,
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

	// Hierarchy fields for Seasons & Episodes.
	if item.Type == "Season" {
		m["IndexNumber"] = item.SeasonNum
		m["SeriesId"] = item.SeriesID
		if series := h.getItemByID(item.SeriesID); series != nil {
			m["SeriesName"] = series.Name
		}
	}
	if item.Type == "Episode" {
		m["IndexNumber"] = item.EpisodeNum
		m["ParentIndexNumber"] = item.SeasonNum
		m["SeasonId"] = item.SeasonID
		m["SeriesId"] = item.SeriesID
		if season := h.getItemByID(item.SeasonID); season != nil {
			m["SeasonName"] = season.Name
		}
		if series := h.getItemByID(item.SeriesID); series != nil {
			m["SeriesName"] = series.Name
			var sImg model.ItemImage
			if h.db.Gorm.Where("item_id = ? AND kind = ?", series.ID, "Primary").First(&sImg).Error == nil {
				m["SeriesPrimaryImageTag"] = h.signTag(p, series.ID, "Primary", sImg.Tag)
			}
		}
	}

	// Provider IDs.
	providerIDs := map[string]string{}
	if item.ProviderIDsJSON != "" {
		_ = json.Unmarshal([]byte(item.ProviderIDsJSON), &providerIDs)
	}
	m["ProviderIds"] = providerIDs

	// Genres.
	var genres []model.ItemGenre
	_ = h.db.Gorm.Where("item_id = ?", item.ID).Find(&genres).Error
	genreNames := make([]string, 0, len(genres))
	genreItems := make([]map[string]any, 0, len(genres))
	for _, g := range genres {
		genreNames = append(genreNames, g.Genre)
		genreItems = append(genreItems, map[string]any{"Name": g.Genre, "Id": "genre-" + g.Genre})
	}
	m["Genres"] = genreNames
	m["GenreItems"] = genreItems

	if detail {
		// Tags.
		var tags []model.ItemTag
		_ = h.db.Gorm.Where("item_id = ?", item.ID).Find(&tags).Error
		tagNames := make([]string, 0, len(tags))
		for _, t := range tags {
			tagNames = append(tagNames, t.Tag)
		}
		m["Tags"] = tagNames

		// Studios.
		var studios []string
		if item.StudiosJSON != "" {
			_ = json.Unmarshal([]byte(item.StudiosJSON), &studios)
		}
		studioObjs := make([]map[string]any, 0, len(studios))
		for _, st := range studios {
			studioObjs = append(studioObjs, map[string]any{"Name": st})
		}
		m["Studios"] = studioObjs

		// People.
		type personRow struct {
			PersonID  string
			Name      string
			TMDBID    string
			RoleType  string
			RoleName  string
			ThumbPath string
		}
		var rows []personRow
		_ = h.db.Gorm.Table("item_people").
			Select("item_people.person_id, people.name, people.tmdb_id, item_people.role_type, item_people.role_name, people.thumb_path").
			Joins("JOIN people ON people.id = item_people.person_id").
			Where("item_people.item_id = ?", item.ID).
			Order("item_people.sort_idx ASC").
			Scan(&rows).Error
		metaDir := h.embyMetadataDir()
		peopleList := make([]map[string]any, 0, len(rows))
		for _, pr := range rows {
			pe := map[string]any{
				"Id":   pr.PersonID,
				"Name": pr.Name,
				"Role": pr.RoleName,
				"Type": pr.RoleType,
			}
			if pr.ThumbPath != "" || (metaDir != "" && imgpipe.FindEmbyPersonImage(metaDir, pr.Name, pr.TMDBID) != "") {
				pe["PrimaryImageTag"] = h.signTag(p, pr.PersonID, "Primary", "person")
			}
			peopleList = append(peopleList, pe)
		}
		m["People"] = peopleList

		// MediaSources & Intro/Credits Chapters for playable items.
		if item.Type == "Movie" || item.Type == "Episode" {
			sources := h.playback.BuildMediaSources(r, p, item)
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
			if chapters := h.resolveIntroChapters(item); len(chapters) > 0 {
				m["Chapters"] = chapters
			}
		}

		// Child counts for Series / Season.
		if item.Type == "Series" {
			var seasonCount, epCount int64
			h.db.Gorm.Model(&model.Item{}).Where("series_id = ? AND type = ? AND soft_deleted_at IS NULL", item.ID, "Season").Count(&seasonCount)
			h.db.Gorm.Model(&model.Item{}).Where("series_id = ? AND type = ? AND soft_deleted_at IS NULL", item.ID, "Episode").Count(&epCount)
			m["ChildCount"] = seasonCount
			m["RecursiveItemCount"] = epCount
		} else if item.Type == "Season" {
			var epCount int64
			h.db.Gorm.Model(&model.Item{}).Where("season_id = ? AND type = ? AND soft_deleted_at IS NULL", item.ID, "Episode").Count(&epCount)
			m["ChildCount"] = epCount
		}
	}

	return m
}

func (h *Handler) getItemByID(id string) *model.Item {
	if id == "" {
		return nil
	}
	var item model.Item
	if err := h.db.Gorm.Where("id = ? AND soft_deleted_at IS NULL", id).First(&item).Error; err != nil {
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

func (h *Handler) resolveIntroChapters(item *model.Item) []map[string]any {
	var marker model.IntroMarker
	err := h.db.Gorm.Where("item_id = ?", item.ID).First(&marker).Error
	if err != nil && item.SeriesID != "" {
		err = h.db.Gorm.Where("series_id = ? AND season_num = ? AND item_id = ''", item.SeriesID, item.SeasonNum).First(&marker).Error
	}
	if err != nil {
		return nil
	}
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
