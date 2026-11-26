package emby

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"emby-lite/internal/auth"
	"emby-lite/internal/model"

	"github.com/gin-gonic/gin"
)

const userConfigSettingKey = "user_configuration"

// UserConfiguration mirrors Emby's UserConfiguration schema plus per-section
// library exclusion lists (ResumeExcludes, NextUpExcludes) persisted per user.
type UserConfiguration struct {
	PlayDefaultAudioTrack      bool     `json:"PlayDefaultAudioTrack"`
	DisplayCollectionsView     bool     `json:"DisplayCollectionsView"`
	DisplayMissingEpisodes     bool     `json:"DisplayMissingEpisodes"`
	SubtitleMode               string   `json:"SubtitleMode"`
	EnableLocalPassword        bool     `json:"EnableLocalPassword"`
	OrderedViews               []string `json:"OrderedViews"`
	LatestItemsExcludes        []string `json:"LatestItemsExcludes"`
	MyMediaExcludes            []string `json:"MyMediaExcludes"`
	ResumeExcludes             []string `json:"ResumeExcludes"`
	NextUpExcludes             []string `json:"NextUpExcludes"`
	HidePlayedInLatest         bool     `json:"HidePlayedInLatest"`
	HidePlayedInMoreLikeThis   bool     `json:"HidePlayedInMoreLikeThis"`
	HidePlayedInSuggestions    bool     `json:"HidePlayedInSuggestions"`
	RememberAudioSelections    bool     `json:"RememberAudioSelections"`
	RememberSubtitleSelections bool     `json:"RememberSubtitleSelections"`
	EnableNextEpisodeAutoPlay  bool     `json:"EnableNextEpisodeAutoPlay"`
	AudioLanguagePreference    string   `json:"AudioLanguagePreference"`
	SubtitleLanguagePreference string   `json:"SubtitleLanguagePreference"`
	ResumeRewindSeconds        int      `json:"ResumeRewindSeconds"`
	IntroSkipMode              string   `json:"IntroSkipMode"`
}

func defaultUserConfig() UserConfiguration {
	return UserConfiguration{
		PlayDefaultAudioTrack:      true,
		DisplayCollectionsView:     true,
		DisplayMissingEpisodes:     false,
		SubtitleMode:               "Default",
		EnableLocalPassword:        false,
		OrderedViews:               []string{},
		LatestItemsExcludes:        []string{},
		MyMediaExcludes:            []string{},
		ResumeExcludes:             []string{},
		NextUpExcludes:             []string{},
		HidePlayedInLatest:         false,
		HidePlayedInMoreLikeThis:   false,
		HidePlayedInSuggestions:    false,
		RememberAudioSelections:    true,
		RememberSubtitleSelections: true,
		EnableNextEpisodeAutoPlay:  true,
		AudioLanguagePreference:    "",
		SubtitleLanguagePreference: "",
		ResumeRewindSeconds:        0,
		IntroSkipMode:              "ShowButton",
	}
}

func cleanIDList(ids []string) []string {
	if len(ids) == 0 {
		return []string{}
	}
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func (c *UserConfiguration) normalize() {
	if c.SubtitleMode == "" {
		c.SubtitleMode = "Default"
	}
	if c.IntroSkipMode == "" {
		c.IntroSkipMode = "ShowButton"
	}
	c.OrderedViews = cleanIDList(c.OrderedViews)
	c.LatestItemsExcludes = cleanIDList(c.LatestItemsExcludes)
	c.MyMediaExcludes = cleanIDList(c.MyMediaExcludes)
	c.ResumeExcludes = cleanIDList(c.ResumeExcludes)
	c.NextUpExcludes = cleanIDList(c.NextUpExcludes)
}

func (h *Handler) loadUserConfig(userID string) UserConfiguration {
	cfg := defaultUserConfig()
	if strings.TrimSpace(userID) == "" {
		return cfg
	}
	var row model.UserSetting
	if err := h.db.Read().Where("user_id = ? AND key = ?", userID, userConfigSettingKey).First(&row).Error; err != nil {
		return cfg
	}
	if strings.TrimSpace(row.Value) != "" {
		_ = json.Unmarshal([]byte(row.Value), &cfg)
	}
	cfg.normalize()
	return cfg
}

func (h *Handler) saveUserConfig(userID string, cfg UserConfiguration) error {
	cfg.normalize()
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return h.db.Gorm.Save(&model.UserSetting{
		UserID:    userID,
		Key:       userConfigSettingKey,
		Value:     string(raw),
		UpdatedAt: time.Now().UTC(),
	}).Error
}

func unionIDLists(a, b []string) []string {
	if len(a) == 0 && len(b) == 0 {
		return []string{}
	}
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, id := range list {
			id = strings.TrimSpace(id)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func idSliceToSet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" {
			m[id] = true
		}
	}
	return m
}

// effectiveUserConfigMap merges user-specific exclusions with library-level global
// disabled flags so standard Emby clients reading User.Configuration automatically
// respect global library visibility settings as well.
func (h *Handler) effectiveUserConfigMap(userID string) map[string]any {
	cfg := h.loadUserConfig(userID)

	var libs []model.Library
	_ = h.db.Read().Select("id, show_in_my_media, show_in_resume, show_in_next_up, show_in_latest").Find(&libs).Error
	var globalMyMediaExc, globalLatestExc, globalResumeExc, globalNextUpExc []string
	for _, l := range libs {
		if !l.ShowInMyMedia {
			globalMyMediaExc = append(globalMyMediaExc, l.ID)
		}
		if !l.ShowInLatest {
			globalLatestExc = append(globalLatestExc, l.ID)
		}
		if !l.ShowInResume {
			globalResumeExc = append(globalResumeExc, l.ID)
		}
		if !l.ShowInNextUp {
			globalNextUpExc = append(globalNextUpExc, l.ID)
		}
	}

	return map[string]any{
		"PlayDefaultAudioTrack":      cfg.PlayDefaultAudioTrack,
		"DisplayCollectionsView":     cfg.DisplayCollectionsView,
		"DisplayMissingEpisodes":     cfg.DisplayMissingEpisodes,
		"SubtitleMode":               cfg.SubtitleMode,
		"EnableLocalPassword":        cfg.EnableLocalPassword,
		"OrderedViews":               cfg.OrderedViews,
		"LatestItemsExcludes":        unionIDLists(cfg.LatestItemsExcludes, globalLatestExc),
		"MyMediaExcludes":            unionIDLists(cfg.MyMediaExcludes, globalMyMediaExc),
		"ResumeExcludes":             unionIDLists(cfg.ResumeExcludes, globalResumeExc),
		"NextUpExcludes":             unionIDLists(cfg.NextUpExcludes, globalNextUpExc),
		"HidePlayedInLatest":         cfg.HidePlayedInLatest,
		"HidePlayedInMoreLikeThis":   cfg.HidePlayedInMoreLikeThis,
		"HidePlayedInSuggestions":    cfg.HidePlayedInSuggestions,
		"RememberAudioSelections":    cfg.RememberAudioSelections,
		"RememberSubtitleSelections": cfg.RememberSubtitleSelections,
		"EnableNextEpisodeAutoPlay":  cfg.EnableNextEpisodeAutoPlay,
		"AudioLanguagePreference":    cfg.AudioLanguagePreference,
		"SubtitleLanguagePreference": cfg.SubtitleLanguagePreference,
		"ResumeRewindSeconds":        cfg.ResumeRewindSeconds,
		"IntroSkipMode":              cfg.IntroSkipMode,
	}
}

// filterLibraryIDsForSection removes libraries disabled globally or in the user's
// per-section exclude list for "mymedia", "resume", "nextup", or "latest".
func (h *Handler) filterLibraryIDsForSection(p *auth.Principal, visibleIDs []string, section string) []string {
	if len(visibleIDs) == 0 {
		return nil
	}
	var cfg UserConfiguration
	if p != nil && p.User != nil {
		cfg = h.loadUserConfig(p.User.ID)
	}
	var userExcludes map[string]bool
	switch section {
	case "mymedia":
		userExcludes = idSliceToSet(cfg.MyMediaExcludes)
	case "resume":
		userExcludes = idSliceToSet(cfg.ResumeExcludes)
	case "nextup":
		userExcludes = idSliceToSet(cfg.NextUpExcludes)
	case "latest":
		userExcludes = idSliceToSet(cfg.LatestItemsExcludes)
	}

	var libs []model.Library
	_ = h.db.Read().
		Select("id, show_in_my_media, show_in_resume, show_in_next_up, show_in_latest").
		Where("id IN ?", visibleIDs).
		Find(&libs).Error
	libFlags := make(map[string]model.Library, len(libs))
	for _, l := range libs {
		libFlags[l.ID] = l
	}

	out := make([]string, 0, len(visibleIDs))
	for _, id := range visibleIDs {
		if userExcludes[id] {
			continue
		}
		if l, ok := libFlags[id]; ok {
			switch section {
			case "mymedia":
				if !l.ShowInMyMedia {
					continue
				}
			case "resume":
				if !l.ShowInResume {
					continue
				}
			case "nextup":
				if !l.ShowInNextUp {
					continue
				}
			case "latest":
				if !l.ShowInLatest {
					continue
				}
			}
		}
		out = append(out, id)
	}
	return out
}

func sortViewsByOrderedViews(items []map[string]any, orderedViews []string) {
	if len(items) <= 1 || len(orderedViews) == 0 {
		return
	}
	rank := make(map[string]int, len(orderedViews))
	for i, id := range orderedViews {
		if _, exists := rank[id]; !exists {
			rank[id] = i
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		idA, _ := items[i]["Id"].(string)
		idB, _ := items[j]["Id"].(string)
		rA, okA := rank[idA]
		rB, okB := rank[idB]
		if okA && okB {
			return rA < rB
		}
		if okA != okB {
			return okA
		}
		return false
	})
}

// UpdateUserConfiguration implements Emby POST /Users/:userId/Configuration.
func (h *Handler) UpdateUserConfiguration(c *gin.Context) {
	p := getPrincipal(c)
	targetID := c.Param("userId")
	if !h.checkUserScope(c, p, targetID) {
		return
	}
	if targetID == "" && p != nil && p.User != nil {
		targetID = p.User.ID
	}
	if targetID == "" {
		jsonErr(c, http.StatusBadRequest, "user id required")
		return
	}
	var targetUser model.User
	if err := h.db.Read().Where("id = ?", targetID).First(&targetUser).Error; err != nil {
		jsonErr(c, http.StatusNotFound, "user not found")
		return
	}

	var raw map[string]json.RawMessage
	if err := c.ShouldBindJSON(&raw); err != nil {
		jsonErr(c, http.StatusBadRequest, "invalid configuration payload")
		return
	}

	cfg := h.loadUserConfig(targetID)
	// Merge only fields present in payload so partial Emby client updates don't wipe other settings.
	if b, err := json.Marshal(cfg); err == nil {
		var merged map[string]json.RawMessage
		if json.Unmarshal(b, &merged) == nil {
			for k, v := range raw {
				merged[k] = v
			}
			if mb, err := json.Marshal(merged); err == nil {
				_ = json.Unmarshal(mb, &cfg)
			}
		}
	}
	if err := h.saveUserConfig(targetID, cfg); err != nil {
		jsonErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	h.bumpUserDataRev(c.Request.Context(), targetID)
	c.Status(http.StatusNoContent)
}

// --- Web Home Screen Settings API (/api/web/home-settings) ---

type webHomeLibrarySetting struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	IsVirtual       bool   `json:"is_virtual"`
	ShowInMyMedia   bool   `json:"show_in_my_media"`
	ShowInResume    bool   `json:"show_in_resume"`
	ShowInNextUp    bool   `json:"show_in_next_up"`
	ShowInLatest    bool   `json:"show_in_latest"`
	GlobalMyMedia   bool   `json:"global_show_in_my_media"`
	GlobalResume    bool   `json:"global_show_in_resume"`
	GlobalNextUp    bool   `json:"global_show_in_next_up"`
	GlobalLatest    bool   `json:"global_show_in_latest"`
}

func (h *Handler) resolveHomeSettingsTarget(c *gin.Context) (*auth.Principal, *model.User, bool) {
	p := getPrincipal(c)
	if p == nil || p.User == nil {
		jsonErr(c, http.StatusUnauthorized, "user login required")
		return nil, nil, false
	}
	reqUID := strings.TrimSpace(c.Query("user_id"))
	if reqUID == "" || reqUID == p.User.ID {
		return p, p.User, true
	}
	if !p.User.IsAdmin {
		jsonErr(c, http.StatusForbidden, "admin required to edit another user's home settings")
		return nil, nil, false
	}
	var target model.User
	if err := h.db.Read().Where("id = ?", reqUID).First(&target).Error; err != nil {
		jsonErr(c, http.StatusNotFound, "user not found")
		return nil, nil, false
	}
	targetPrincipal := &auth.Principal{
		User:   &target,
		Policy: h.auth.ResolvePolicy(&target),
	}
	return targetPrincipal, &target, true
}

func (h *Handler) GetWebHomeSettings(c *gin.Context) {
	targetPrincipal, targetUser, ok := h.resolveHomeSettingsTarget(c)
	if !ok {
		return
	}
	visibleIDs, err := h.auth.VisibleLibraryIDs(targetPrincipal)
	if err != nil {
		jsonErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	cfg := h.loadUserConfig(targetUser.ID)
	myMediaExc := idSliceToSet(cfg.MyMediaExcludes)
	resumeExc := idSliceToSet(cfg.ResumeExcludes)
	nextUpExc := idSliceToSet(cfg.NextUpExcludes)
	latestExc := idSliceToSet(cfg.LatestItemsExcludes)

	var libs []model.Library
	if len(visibleIDs) > 0 {
		_ = h.db.Read().Where("id IN ?", visibleIDs).Order("sort_order ASC, name ASC").Find(&libs).Error
	}
	var vlibs []model.VirtualLibrary
	_ = h.db.Read().Where("enabled = ?", true).Order("sort_order ASC, name ASC").Find(&vlibs).Error

	entries := make([]webHomeLibrarySetting, 0, len(libs)+len(vlibs))
	for _, l := range libs {
		entries = append(entries, webHomeLibrarySetting{
			ID:            l.ID,
			Name:          l.Name,
			Kind:          l.Kind,
			IsVirtual:     false,
			ShowInMyMedia: !myMediaExc[l.ID],
			ShowInResume:  !resumeExc[l.ID],
			ShowInNextUp:  !nextUpExc[l.ID],
			ShowInLatest:  !latestExc[l.ID],
			GlobalMyMedia: l.ShowInMyMedia,
			GlobalResume:  l.ShowInResume,
			GlobalNextUp:  l.ShowInNextUp,
			GlobalLatest:  l.ShowInLatest,
		})
	}
	for _, vl := range vlibs {
		entries = append(entries, webHomeLibrarySetting{
			ID:            vl.ID,
			Name:          vl.Name,
			Kind:          "virtual",
			IsVirtual:     true,
			ShowInMyMedia: !myMediaExc[vl.ID],
			ShowInResume:  !resumeExc[vl.ID],
			ShowInNextUp:  !nextUpExc[vl.ID],
			ShowInLatest:  !latestExc[vl.ID],
			GlobalMyMedia: true,
			GlobalResume:  true,
			GlobalNextUp:  true,
			GlobalLatest:  true,
		})
	}

	if len(cfg.OrderedViews) > 0 {
		rank := make(map[string]int, len(cfg.OrderedViews))
		for i, id := range cfg.OrderedViews {
			if _, exists := rank[id]; !exists {
				rank[id] = i
			}
		}
		sort.SliceStable(entries, func(i, j int) bool {
			rA, okA := rank[entries[i].ID]
			rB, okB := rank[entries[j].ID]
			if okA && okB {
				return rA < rB
			}
			if okA != okB {
				return okA
			}
			return false
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"user_id":               targetUser.ID,
		"username":              targetUser.Username,
		"has_custom_order":      len(cfg.OrderedViews) > 0,
		"ordered_views":         cfg.OrderedViews,
		"hide_played_in_latest": cfg.HidePlayedInLatest,
		"libraries":             entries,
	})
}

func (h *Handler) UpdateWebHomeSettings(c *gin.Context) {
	_, targetUser, ok := h.resolveHomeSettingsTarget(c)
	if !ok {
		return
	}
	var body struct {
		OrderedViews       *[]string `json:"ordered_views"`
		ResetOrder         bool      `json:"reset_order"`
		HidePlayedInLatest *bool     `json:"hide_played_in_latest"`
		Libraries          []struct {
			ID            string `json:"id"`
			ShowInMyMedia bool   `json:"show_in_my_media"`
			ShowInResume  bool   `json:"show_in_resume"`
			ShowInNextUp  bool   `json:"show_in_next_up"`
			ShowInLatest  bool   `json:"show_in_latest"`
		} `json:"libraries"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonErr(c, http.StatusBadRequest, err.Error())
		return
	}

	cfg := h.loadUserConfig(targetUser.ID)
	if body.ResetOrder {
		cfg.OrderedViews = []string{}
	} else if body.OrderedViews != nil {
		cfg.OrderedViews = cleanIDList(*body.OrderedViews)
	}
	if body.HidePlayedInLatest != nil {
		cfg.HidePlayedInLatest = *body.HidePlayedInLatest
	}
	if body.Libraries != nil {
		var myExc, resExc, nextExc, latExc []string
		for _, item := range body.Libraries {
			id := strings.TrimSpace(item.ID)
			if id == "" {
				continue
			}
			if !item.ShowInMyMedia {
				myExc = append(myExc, id)
			}
			if !item.ShowInResume {
				resExc = append(resExc, id)
			}
			if !item.ShowInNextUp {
				nextExc = append(nextExc, id)
			}
			if !item.ShowInLatest {
				latExc = append(latExc, id)
			}
		}
		cfg.MyMediaExcludes = cleanIDList(myExc)
		cfg.ResumeExcludes = cleanIDList(resExc)
		cfg.NextUpExcludes = cleanIDList(nextExc)
		cfg.LatestItemsExcludes = cleanIDList(latExc)
	}

	if err := h.saveUserConfig(targetUser.ID, cfg); err != nil {
		jsonErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	h.bumpUserDataRev(c.Request.Context(), targetUser.ID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
