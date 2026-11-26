package model

import "time"

// PermissionGroup defines reusable user access policies.
type PermissionGroup struct {
	ID                   string    `gorm:"primaryKey;size:64" json:"id"`
	Name                 string    `gorm:"uniqueIndex;size:128;not null" json:"name"`
	Description          string    `gorm:"size:512" json:"description"`
	AllowPlayback        bool      `gorm:"default:true;not null" json:"allow_playback"`
	AllowDownload        bool      `gorm:"default:false;not null" json:"allow_download"`
	MaxDevices           int       `gorm:"default:0;not null" json:"max_devices"`            // 0 = unlimited
	MaxConcurrentStreams int       `gorm:"default:0;not null" json:"max_concurrent_streams"` // 0 = unlimited
	CanViewHidden        bool      `gorm:"default:false;not null" json:"can_view_hidden"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// User represents an Emby-compatible user account.
type User struct {
	ID                string     `gorm:"primaryKey;size:64" json:"id"`
	Username          string     `gorm:"uniqueIndex;size:128;not null" json:"username"`
	DisplayName       string     `gorm:"size:128" json:"display_name"`
	PasswordHash      string     `gorm:"size:512;not null" json:"-"`
	IsAdmin           bool       `gorm:"default:false;not null" json:"is_admin"`
	IsPrimaryAdmin    bool       `gorm:"default:false;not null" json:"is_primary_admin"`
	PermissionGroupID string     `gorm:"size:64;index" json:"permission_group_id"`
	AllowPlayback     *bool      `json:"allow_playback"`
	CanDownload       *bool      `json:"can_download"`
	MaxDevices        int        `gorm:"default:0;not null" json:"max_devices"`
	CanViewHidden     *bool      `json:"can_view_hidden"`
	Disabled          bool       `gorm:"default:false;not null" json:"disabled"`
	ExpiresAt         *time.Time `json:"expires_at"`
	LastLoginAt       *time.Time `json:"last_login_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type GroupLibrary struct {
	GroupID   string `gorm:"primaryKey;size:64"`
	LibraryID string `gorm:"primaryKey;size:64"`
}

type UserLibrary struct {
	UserID    string `gorm:"primaryKey;size:64"`
	LibraryID string `gorm:"primaryKey;size:64"`
}

type Token struct {
	ID         string    `gorm:"primaryKey;size:64"`
	TokenHash  string    `gorm:"uniqueIndex;size:128;not null"`
	UserID     string    `gorm:"size:64;index;not null"`
	DeviceID   string    `gorm:"size:128;index"`
	DeviceName string    `gorm:"size:128"`
	Client     string    `gorm:"size:128"`
	Version    string    `gorm:"size:64"`
	IP         string    `gorm:"size:64"`
	ExpiresAt  time.Time `gorm:"index;not null"`
	LastUsedAt time.Time
	CreatedAt  time.Time
}

type APIKey struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	Name      string    `gorm:"size:128;not null" json:"name"`
	KeyHash   string    `gorm:"uniqueIndex;size:128;not null" json:"-"`
	CreatedBy string    `gorm:"size:64" json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

type UserDevice struct {
	UserID      string    `gorm:"primaryKey;size:64" json:"user_id"`
	DeviceID    string    `gorm:"primaryKey;size:128" json:"device_id"`
	DeviceName  string    `gorm:"size:128" json:"device_name"`
	Client      string    `gorm:"size:128" json:"client"`
	IP          string    `gorm:"size:64" json:"ip"`
	Blocked     bool      `gorm:"default:false;not null" json:"blocked"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

type UserAvatar struct {
	UserID    string `gorm:"primaryKey;size:64"`
	Mime      string `gorm:"size:64;not null"`
	Data      []byte `gorm:"not null"`
	Revision  string `gorm:"size:64"`
	UpdatedAt time.Time
}

// Library represents a physical media library with one or more root paths.
type Library struct {
	ID                string     `gorm:"primaryKey;size:64" json:"id"`
	Name              string     `gorm:"size:128;not null" json:"name"`
	Kind              string     `gorm:"size:32;not null" json:"kind"` // movies | tvshows | mixed | boxsets
	PathsJSON         string     `gorm:"type:text;not null" json:"-"`
	Hidden            bool       `gorm:"default:false;not null" json:"hidden"`
	ShowInMyMedia     bool       `gorm:"default:true;not null" json:"show_in_my_media"`
	ShowInResume      bool       `gorm:"default:true;not null" json:"show_in_resume"`
	ShowInNextUp      bool       `gorm:"default:true;not null" json:"show_in_next_up"`
	ShowInLatest      bool       `gorm:"default:true;not null" json:"show_in_latest"`
	AutoMergeVersions bool       `gorm:"default:true;not null" json:"auto_merge_versions"`
	AutoGroupSeries   bool       `gorm:"default:true;not null" json:"auto_group_series"`
	SortOrder         int        `gorm:"default:0;not null" json:"sort_order"`
	ScanSchedule      string     `gorm:"size:64" json:"scan_schedule"`
	CoverRevision     string     `gorm:"size:64" json:"cover_revision"`
	Status            string     `gorm:"size:32;default:idle" json:"status"`
	LastError         string     `gorm:"type:text" json:"last_error"`
	ItemCount         int64      `gorm:"default:0" json:"item_count"`
	LastScannedAt     *time.Time `json:"last_scanned_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type VirtualLibrary struct {
	ID            string    `gorm:"primaryKey;size:64" json:"id"`
	Name          string    `gorm:"size:128;not null" json:"name"`
	RuleJSON      string    `gorm:"type:text;not null" json:"rule_json"`
	SortOrder     int       `gorm:"default:0;not null" json:"sort_order"`
	Enabled       bool      `gorm:"default:true;not null" json:"enabled"`
	CoverRevision string    `gorm:"size:64" json:"cover_revision"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type LibraryCover struct {
	LibraryID  string    `gorm:"primaryKey;size:64"`
	Kind       string    `gorm:"size:32;not null"` // custom | collage
	Mime       string    `gorm:"size:64;not null"`
	Data       []byte    `gorm:"not null"`
	Revision   string    `gorm:"size:64;not null"`
	SourceHash string    `gorm:"size:64"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Item represents a Movie, Series, Season, Episode, or Folder.
type Item struct {
	ID                      string     `gorm:"primaryKey;size:64" json:"id"`
	LibraryID               string     `gorm:"size:64;index:idx_items_lib_type;not null" json:"library_id"`
	ParentID                string     `gorm:"size:64;index" json:"parent_id"`
	SeriesID                string     `gorm:"size:64;index" json:"series_id"`
	SeasonID                string     `gorm:"size:64;index" json:"season_id"`
	Type                    string     `gorm:"size:32;index:idx_items_lib_type;not null" json:"type"` // Movie | Series | Season | Episode | Folder | BoxSet
	Name                    string     `gorm:"size:512;index;not null" json:"name"`
	OriginalName            string     `gorm:"size:512" json:"original_name"`
	SortName                string     `gorm:"size:512;index" json:"sort_name"`
	PinyinInitials          string     `gorm:"size:256;index" json:"pinyin_initials"`
	Overview                string     `gorm:"type:text" json:"overview"`
	Tagline                 string     `gorm:"size:512" json:"tagline"`
	Year                    int        `gorm:"index" json:"year"`
	PremiereDate            string     `gorm:"size:32" json:"premiere_date"` // YYYY-MM-DD
	SeasonNum               int        `gorm:"index" json:"season_num"`
	EpisodeNum              int        `gorm:"index" json:"episode_num"`
	CommunityRating         float64    `json:"community_rating"`
	OfficialRating          string     `gorm:"size:32" json:"official_rating"`
	RuntimeTicks            int64      `json:"runtime_ticks"`
	CriticRating            float64    `json:"critic_rating"`
	CustomRating            string     `gorm:"size:64" json:"custom_rating"`
	ProductionLocationsJSON string     `gorm:"type:text" json:"-"`
	Status                  string     `gorm:"size:32" json:"status"`
	DisplayOrder            int        `gorm:"default:0" json:"display_order"`
	IsFolder                bool       `gorm:"default:false;not null" json:"is_folder"`
	ProviderIDsJSON         string     `gorm:"type:text" json:"provider_ids_json"`
	StudiosJSON             string     `gorm:"type:text" json:"studios_json"`
	DirPath                 string     `gorm:"size:1024;index" json:"dir_path"`
	ScanKey                 string     `gorm:"size:1024;uniqueIndex" json:"scan_key"`
	RandomKey               string     `gorm:"size:32;index" json:"random_key"`
	DisableAutoMerge        bool       `gorm:"default:false;not null" json:"disable_auto_merge"`
	AddedAt                 time.Time  `gorm:"index;not null" json:"added_at"`
	SoftDeletedAt           *time.Time `gorm:"index" json:"soft_deleted_at"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
}

// MediaSource represents a concrete playable media file or STRM source for an Item (supports multi-version).
type MediaSource struct {
	ID           string     `gorm:"primaryKey;size:64" json:"id"`
	ItemID       string     `gorm:"size:64;index;not null" json:"item_id"`
	SourceType   string     `gorm:"size:16;not null" json:"source_type"` // local | strm
	Path         string     `gorm:"size:1024;index;not null" json:"path"`
	StrmURL      string     `gorm:"type:text" json:"strm_url"`
	FileSize     int64      `json:"file_size"`
	FileMtime    int64      `json:"file_mtime"`
	Fingerprint  string     `gorm:"size:128;index" json:"fingerprint"`
	VersionLabel string     `gorm:"size:128" json:"version_label"`
	Container    string     `gorm:"size:32" json:"container"`
	VideoCodec   string     `gorm:"size:32" json:"video_codec"`
	Width        int        `json:"width"`
	Height       int        `json:"height"`
	HDRType      string     `gorm:"size:32" json:"hdr_type"`
	DurationSecs float64    `json:"duration_secs"`
	Bitrate      int64      `json:"bitrate"`
	ProbeJSON    string     `gorm:"type:text" json:"probe_json"`
	ProbedAt     *time.Time `json:"probed_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type MediaSubtitle struct {
	ID            string    `gorm:"primaryKey;size:64" json:"id"`
	MediaSourceID string    `gorm:"size:64;index;not null" json:"media_source_id"`
	ItemID        string    `gorm:"size:64;index;not null" json:"item_id"`
	StreamIndex   int       `json:"stream_index"`
	IsExternal    bool      `gorm:"default:true;not null" json:"is_external"`
	Path          string    `gorm:"size:1024" json:"path"`
	Language      string    `gorm:"size:32" json:"language"`
	Title         string    `gorm:"size:128" json:"title"`
	Codec         string    `gorm:"size:32" json:"codec"`
	IsDefault     bool      `gorm:"default:false" json:"is_default"`
	IsForced      bool      `gorm:"default:false" json:"is_forced"`
	CreatedAt     time.Time `json:"created_at"`
}

type ItemGenre struct {
	ItemID string `gorm:"primaryKey;size:64"`
	Genre  string `gorm:"primaryKey;size:128;index"`
}

type ItemTag struct {
	ItemID string `gorm:"primaryKey;size:64"`
	Tag    string `gorm:"primaryKey;size:128;index"`
}

type Person struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	Name      string    `gorm:"size:256;index;not null" json:"name"`
	TMDBID    string    `gorm:"size:64;index" json:"tmdb_id"`
	ThumbPath string    `gorm:"size:1024" json:"thumb_path"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ItemPerson struct {
	ItemID   string `gorm:"primaryKey;size:64"`
	PersonID string `gorm:"primaryKey;size:64;index"`
	RoleType string `gorm:"primaryKey;size:32"` // Actor | Director | Writer | Producer | GuestStar | Composer | ...
	RoleName string `gorm:"size:256"`
	SortIdx  int    `gorm:"default:0"`
}

type ItemImage struct {
	ID         string `gorm:"primaryKey;size:64"`
	ItemID     string `gorm:"size:64;index:idx_item_images_lookup;not null"`
	Kind       string `gorm:"size:32;index:idx_item_images_lookup;not null"` // Primary | Backdrop | Thumb | Logo | Banner
	IndexNum   int    `gorm:"default:0;not null"`
	SourceType string `gorm:"size:16;not null"` // local | remote | upload
	SourceRef  string `gorm:"size:1024;not null"`
	Tag        string `gorm:"size:64;not null"`
	Width      int
	Height     int
	UpdatedAt  time.Time
}

type ItemMetadataLock struct {
	ItemID           string    `gorm:"primaryKey;size:64"`
	LockedFieldsJSON string    `gorm:"type:text;not null"`
	OverrideJSON     string    `gorm:"type:text"`
	Revision         int64     `gorm:"default:1"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type ScrapeMatchQueue struct {
	ID             string    `gorm:"primaryKey;size:64" json:"id"`
	ItemID         string    `gorm:"uniqueIndex;size:64;not null" json:"item_id"`
	LibraryID      string    `gorm:"size:64;index;not null" json:"library_id"`
	ItemType       string    `gorm:"size:32;not null" json:"item_type"`
	ItemName       string    `gorm:"size:512;not null" json:"item_name"`
	ItemYear       int       `json:"item_year"`
	Status         string    `gorm:"size:32;index;not null" json:"status"` // pending | resolved | ignored
	BestScore      float64   `json:"best_score"`
	CandidatesJSON string    `gorm:"type:text;not null" json:"-"`
	UpdatedAt      time.Time `json:"updated_at"`
	CreatedAt      time.Time `json:"created_at"`
}

type UserData struct {
	UserID         string     `gorm:"primaryKey;size:64" json:"user_id"`
	ItemID         string     `gorm:"primaryKey;size:64;index" json:"item_id"`
	Played         bool       `gorm:"default:false;not null" json:"played"`
	PositionTicks  int64      `gorm:"default:0;not null" json:"position_ticks"`
	PlayCount      int        `gorm:"default:0;not null" json:"play_count"`
	IsFavorite     bool       `gorm:"default:false;not null" json:"is_favorite"`
	HideFromResume bool       `gorm:"default:false;not null" json:"hide_from_resume"`
	LastPlayedAt   *time.Time `gorm:"index" json:"last_played_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type PlaybackLease struct {
	PlaySessionID string    `gorm:"primaryKey;size:64" json:"play_session_id"`
	UserID        string    `gorm:"size:64;index;not null" json:"user_id"`
	DeviceID      string    `gorm:"size:128;index;not null" json:"device_id"`
	DeviceName    string    `gorm:"size:128" json:"device_name"`
	Client        string    `gorm:"size:128" json:"client"`
	ItemID        string    `gorm:"size:64;index;not null" json:"item_id"`
	MediaSourceID string    `gorm:"size:64" json:"media_source_id"`
	PositionTicks int64     `json:"position_ticks"`
	IsPaused      bool      `json:"is_paused"`
	ExpiresAt     time.Time `gorm:"index;not null" json:"expires_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type IntroMarker struct {
	ID                string    `gorm:"primaryKey;size:64" json:"id"`
	SeriesID          string    `gorm:"size:64;index" json:"series_id"`
	SeasonNum         int       `gorm:"index" json:"season_num"`
	ItemID            string    `gorm:"size:64;index" json:"item_id"`
	IntroStartTicks   int64     `json:"intro_start_ticks"`
	IntroEndTicks     int64     `json:"intro_end_ticks"`
	CreditsStartTicks int64     `json:"credits_start_ticks"`
	Source            string    `gorm:"size:32" json:"source"`
	Confidence        float64   `json:"confidence"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// ItemChapter stores the chapter markers embedded in a media file.
type ItemChapter struct {
	ItemID     string `gorm:"primaryKey;size:64"`
	IndexNum   int    `gorm:"primaryKey"`
	StartTicks int64  `json:"start_ticks"`
	Name       string `gorm:"size:512" json:"name"`
	MarkerType int    `json:"marker_type"`
	ImagePath  string `gorm:"size:1024" json:"-"`
}

// BoxSet mirrors an Emby collection (MediaItems.type=9).
type BoxSet struct {
	ID              string    `gorm:"primaryKey;size:64" json:"id"`
	LibraryID       string    `gorm:"size:64;index" json:"library_id"`
	Name            string    `gorm:"size:512;not null" json:"name"`
	SortName        string    `gorm:"size:512" json:"sort_name"`
	Overview        string    `gorm:"type:text" json:"overview"`
	ProviderIDsJSON string    `gorm:"type:text" json:"-"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type BoxSetItem struct {
	BoxSetID string `gorm:"primaryKey;size:64"`
	ItemID   string `gorm:"primaryKey;size:64;index"`
	SortIdx  int    `gorm:"default:0"`
}

// UserSetting stores a raw Emby user preference blob (usersettings, usernotifications, ...).
type UserSetting struct {
	UserID    string    `gorm:"primaryKey;size:64" json:"user_id"`
	Key       string    `gorm:"primaryKey;size:128" json:"key"`
	Value     string    `gorm:"type:text;not null" json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ScanFile struct {
	LibraryID   string `gorm:"primaryKey;size:64"`
	Path        string `gorm:"primaryKey;size:1024"`
	DirPath     string `gorm:"size:1024;index"`
	Size        int64
	Mtime       int64
	Fingerprint string `gorm:"size:128;index"`
	DepHash     string `gorm:"size:64"`
	ItemID      string `gorm:"size:64;index"`
	LastScanRun string `gorm:"size:64;index"`
}

type BackgroundTask struct {
	ID          string     `gorm:"primaryKey;size:64" json:"id"`
	Type        string     `gorm:"size:32;index;not null" json:"type"`
	Status      string     `gorm:"size:32;index;not null" json:"status"` // pending | running | completed | failed | cancelled
	Priority    int        `gorm:"default:0;index" json:"priority"`
	PayloadJSON string     `gorm:"type:text" json:"payload_json"`
	Progress    int        `json:"progress"`
	Total       int        `json:"total"`
	Message     string     `gorm:"size:512" json:"message"`
	Error       string     `gorm:"type:text" json:"error"`
	CreatedAt   time.Time  `gorm:"index" json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

type ProviderCache struct {
	Provider    string    `gorm:"primaryKey;size:32"`
	CacheKey    string    `gorm:"primaryKey;size:256"`
	PayloadJSON string    `gorm:"type:text;not null"`
	ExpiresAt   time.Time `gorm:"index"`
	UpdatedAt   time.Time
}

type Setting struct {
	Key       string    `gorm:"primaryKey;size:128" json:"key"`
	Value     string    `gorm:"type:text;not null" json:"value"`
	Encrypted bool      `gorm:"default:false;not null" json:"encrypted"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AuditLog struct {
	ID         string    `gorm:"primaryKey;size:64" json:"id"`
	UserID     string    `gorm:"size:64;index" json:"user_id"`
	Action     string    `gorm:"size:64;index;not null" json:"action"`
	TargetType string    `gorm:"size:64" json:"target_type"`
	TargetID   string    `gorm:"size:64" json:"target_id"`
	DetailJSON string    `gorm:"type:text" json:"detail_json"`
	IP         string    `gorm:"size:64" json:"ip"`
	CreatedAt  time.Time `gorm:"index" json:"created_at"`
}

// AllModels returns all GORM entity models in migration order.
func AllModels() []any {
	return []any{
		&PermissionGroup{},
		&User{},
		&GroupLibrary{},
		&UserLibrary{},
		&Token{},
		&APIKey{},
		&UserDevice{},
		&UserAvatar{},
		&Library{},
		&VirtualLibrary{},
		&LibraryCover{},
		&Item{},
		&MediaSource{},
		&MediaSubtitle{},
		&ItemGenre{},
		&ItemTag{},
		&Person{},
		&ItemPerson{},
		&ItemImage{},
		&ItemMetadataLock{},
		&ScrapeMatchQueue{},
		&UserData{},
		&PlaybackLease{},
		&IntroMarker{},
		&ItemChapter{},
		&BoxSet{},
		&BoxSetItem{},
		&UserSetting{},
		&ScanFile{},
		&BackgroundTask{},
		&ProviderCache{},
		&Setting{},
		&AuditLog{},
	}
}
