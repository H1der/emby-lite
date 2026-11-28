package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"emby-lite/internal/cache"
	"emby-lite/internal/config"
	"emby-lite/internal/database"
	"emby-lite/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrUserDisabled       = errors.New("user account is disabled or expired")
	ErrRateLimited        = errors.New("too many failed login attempts, please try again later")
	ErrDeviceLimitReached = errors.New("maximum device or concurrent stream limit reached")
	ErrAlreadyInitialized = errors.New("server is already initialized")
)

// ClientMeta holds parsed client/device details from X-Emby-Authorization or query/headers.
type ClientMeta struct {
	Client     string
	Device     string
	DeviceID   string
	Version    string
	Token      string
	RemoteAddr string
}

// EffectivePolicy holds resolved effective permissions for a user.
type EffectivePolicy struct {
	AllowPlayback        bool
	AllowDownload        bool
	MaxDevices           int
	MaxConcurrentStreams int
	CanViewHidden        bool
}

// Principal represents the authenticated caller of an HTTP request.
type Principal struct {
	User      *model.User
	Token     string
	TokenID   string
	IsAPIKey  bool
	APIKeyID  string
	Client    ClientMeta
	Policy    EffectivePolicy
}

const authMemCacheTTL = 15 * time.Second

type cachedUserEntry struct {
	user      model.User
	policy    EffectivePolicy
	expiresAt time.Time
}

type cachedVisibleLibsEntry struct {
	ids       []string
	expiresAt time.Time
}

type cachedAllLibsEntry struct {
	libs      []model.Library
	idSet     map[string]bool
	libRev    string
	expiresAt time.Time
}

type Service struct {
	db    *database.DB
	cache cache.Store
	cfg   *config.Config

	memMu        sync.RWMutex
	userCache    map[string]cachedUserEntry
	visCache     map[string]cachedVisibleLibsEntry
	allLibsCache cachedAllLibsEntry
	userEpoch    atomic.Uint64
}

func NewService(db *database.DB, cacheStore cache.Store, cfg *config.Config) *Service {
	return &Service{
		db:        db,
		cache:     cacheStore,
		cfg:       cfg,
		userCache: make(map[string]cachedUserEntry, 16),
		visCache:  make(map[string]cachedVisibleLibsEntry, 16),
	}
}

func (s *Service) ImageSecret() string {
	return s.db.GetSetting("image_secret", "default-secret")
}

// IsInitialized returns true when at least one user exists.
func (s *Service) IsInitialized() (bool, error) {
	var count int64
	if err := s.db.Gorm.Model(&model.User{}).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// BootstrapAdminFromEnv creates the initial admin user if configured via env and DB has no users.
func (s *Service) BootstrapAdminFromEnv() error {
	if strings.TrimSpace(s.cfg.Auth.InitialAdminPass) == "" {
		return nil
	}
	initialized, err := s.IsInitialized()
	if err != nil || initialized {
		return err
	}
	username := s.cfg.Auth.InitialAdminUser
	if username == "" {
		username = "admin"
	}
	_, err = s.CreateInitialAdmin(username, s.cfg.Auth.InitialAdminPass, s.cfg.Server.ServerName)
	return err
}

// CreateInitialAdmin creates the first primary administrator account during setup.
func (s *Service) CreateInitialAdmin(username, password, serverName string) (*model.User, error) {
	initialized, err := s.IsInitialized()
	if err != nil {
		return nil, err
	}
	if initialized {
		return nil, ErrAlreadyInitialized
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("username is required")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	allow := true
	user := &model.User{
		ID:             newID(),
		Username:       username,
		DisplayName:    username,
		PasswordHash:   hash,
		IsAdmin:        true,
		IsPrimaryAdmin: true,
		AllowPlayback:  &allow,
		CanDownload:    &allow,
		CanViewHidden:  &allow,
	}
	if err := s.db.Gorm.Create(user).Error; err != nil {
		return nil, err
	}
	if strings.TrimSpace(serverName) != "" {
		_ = s.db.SetSetting("server_name", strings.TrimSpace(serverName), false)
	}
	return user, nil
}

// CreateUser creates a regular or admin user account.
func (s *Service) CreateUser(username, password string, isAdmin bool, groupID string, canDownload *bool) (*model.User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("username is required")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	user := &model.User{
		ID:                newID(),
		Username:          username,
		DisplayName:       username,
		PasswordHash:      hash,
		IsAdmin:           isAdmin,
		PermissionGroupID: groupID,
		CanDownload:       canDownload,
	}
	if err := s.db.Gorm.Create(user).Error; err != nil {
		return nil, err
	}
	return user, nil
}

// AuthenticateByName validates username/password, enforces rate limits, records device, and mints an Emby AccessToken.
func (s *Service) AuthenticateByName(ctx context.Context, username, password string, meta ClientMeta) (*model.User, string, error) {
	username = strings.TrimSpace(username)
	rlKey := fmt.Sprintf("rl:login:%s:%s", meta.RemoteAddr, strings.ToLower(username))
	if s.cfg.Auth.LoginRateLimitMax > 0 {
		if raw, ok := s.cache.Get(ctx, rlKey+":blocked"); ok && len(raw) > 0 {
			return nil, "", ErrRateLimited
		}
	}

	var user model.User
	err := s.db.Gorm.Where("LOWER(username) = ?", strings.ToLower(username)).First(&user).Error
	if err != nil || !VerifyPassword(password, user.PasswordHash) {
		s.recordFailedLogin(ctx, rlKey)
		return nil, "", ErrInvalidCredentials
	}
	if user.Disabled || (user.ExpiresAt != nil && time.Now().After(*user.ExpiresAt)) {
		return nil, "", ErrUserDisabled
	}

	// Clear rate limit failure marker on success.
	_ = s.cache.Delete(ctx, rlKey)

	if meta.DeviceID == "" {
		meta.DeviceID = "dev-" + newID()[:12]
	}
	if meta.Client == "" {
		meta.Client = "UnknownClient"
	}
	if meta.Device == "" {
		meta.Device = meta.Client
	}

	// Enforce max distinct devices if configured (admins exempt).
	policy := s.ResolvePolicy(&user)
	if !user.IsAdmin && policy.MaxDevices > 0 {
		var existing model.UserDevice
		err := s.db.Gorm.Where("user_id = ? AND device_id = ?", user.ID, meta.DeviceID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			var count int64
			s.db.Gorm.Model(&model.UserDevice{}).Where("user_id = ? AND blocked = ?", user.ID, false).Count(&count)
			if int(count) >= policy.MaxDevices {
				return nil, "", ErrDeviceLimitReached
			}
		} else if err == nil && existing.Blocked {
			return nil, "", ErrUserDisabled
		}
	}

	plain, tokenHash, err := GenerateToken()
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	tok := &model.Token{
		ID:         newID(),
		TokenHash:  tokenHash,
		UserID:     user.ID,
		DeviceID:   meta.DeviceID,
		DeviceName: meta.Device,
		Client:     meta.Client,
		Version:    meta.Version,
		IP:         meta.RemoteAddr,
		ExpiresAt:  now.Add(time.Duration(s.cfg.Auth.TokenTTLHours) * time.Hour),
		LastUsedAt: now,
	}
	if err := s.db.Gorm.Create(tok).Error; err != nil {
		return nil, "", err
	}

	// Upsert device history.
	var dev model.UserDevice
	if err := s.db.Gorm.Where("user_id = ? AND device_id = ?", user.ID, meta.DeviceID).First(&dev).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		_ = s.db.Gorm.Create(&model.UserDevice{
			UserID:      user.ID,
			DeviceID:    meta.DeviceID,
			DeviceName:  meta.Device,
			Client:      meta.Client,
			IP:          meta.RemoteAddr,
			FirstSeenAt: now,
			LastSeenAt:  now,
		}).Error
	} else if err == nil {
		_ = s.db.Gorm.Model(&dev).Updates(map[string]any{
			"device_name":  meta.Device,
			"client":       meta.Client,
			"ip":           meta.RemoteAddr,
			"last_seen_at": now,
		}).Error
	}

	_ = s.db.Gorm.Model(&user).Update("last_login_at", now).Error
	user.LastLoginAt = &now

	return &user, plain, nil
}

func (s *Service) recordFailedLogin(ctx context.Context, rlKey string) {
	if s.cfg.Auth.LoginRateLimitMax <= 0 {
		return
	}
	ttl := time.Duration(s.cfg.Auth.LoginRateLimitSecs) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	n, err := s.cache.IncrWithTTL(ctx, rlKey, ttl)
	if err == nil && int(n) >= s.cfg.Auth.LoginRateLimitMax {
		_ = s.cache.Set(ctx, rlKey+":blocked", []byte("1"), ttl)
	}
}

// Logout revokes the given plaintext token.
func (s *Service) Logout(ctx context.Context, plainToken string) error {
	if plainToken == "" {
		return nil
	}
	hash := HashToken(plainToken)
	_ = s.cache.Delete(ctx, "tok:"+hash)
	return s.db.Gorm.Where("token_hash = ?", hash).Delete(&model.Token{}).Error
}

// InvalidateUserCache clears cached auth/browse entries when user permissions change.
func (s *Service) InvalidateUserCache(ctx context.Context, userID string) {
	s.userEpoch.Add(1)
	s.memMu.Lock()
	if userID != "" {
		delete(s.userCache, userID)
	} else {
		s.userCache = make(map[string]cachedUserEntry, 16)
	}
	s.visCache = make(map[string]cachedVisibleLibsEntry, 16)
	s.memMu.Unlock()
	_ = s.cache.DeletePrefix(ctx, "tok:")
	_ = s.cache.DeletePrefix(ctx, "scope:"+userID)
}

// AuthenticateRequest extracts credentials from an HTTP request and resolves the Principal.
func (s *Service) AuthenticateRequest(r *http.Request) (*Principal, error) {
	meta := ExtractClientMeta(r)
	if meta.Token == "" {
		return nil, ErrInvalidCredentials
	}
	return s.ResolveToken(r.Context(), meta.Token, meta)
}

type cachedTokenInfo struct {
	UserID   string `json:"user_id"`
	TokenID  string `json:"token_id"`
	DeviceID string `json:"device_id"`
	IsAPIKey bool   `json:"is_api_key"`
	APIKeyID string `json:"api_key_id"`
}

// ResolveToken resolves a plaintext Emby user token or API key.
func (s *Service) ResolveToken(ctx context.Context, plain string, meta ClientMeta) (*Principal, error) {
	hash := HashToken(plain)
	cacheKey := "tok:" + hash

	var info cachedTokenInfo
	if raw, ok := s.cache.Get(ctx, cacheKey); ok && json.Unmarshal(raw, &info) == nil {
		return s.hydratePrincipal(plain, info, meta)
	}

	var tok model.Token
	if err := s.db.Gorm.Where("token_hash = ? AND expires_at > ?", hash, time.Now().UTC()).First(&tok).Error; err == nil {
		info = cachedTokenInfo{
			UserID:   tok.UserID,
			TokenID:  tok.ID,
			DeviceID: tok.DeviceID,
		}
		if b, err := json.Marshal(info); err == nil {
			_ = s.cache.Set(ctx, cacheKey, b, 2*time.Minute)
		}
		return s.hydratePrincipal(plain, info, meta)
	}

	var apiKey model.APIKey
	if err := s.db.Gorm.Where("key_hash = ?", hash).First(&apiKey).Error; err == nil {
		info = cachedTokenInfo{
			IsAPIKey: true,
			APIKeyID: apiKey.ID,
		}
		if b, err := json.Marshal(info); err == nil {
			_ = s.cache.Set(ctx, cacheKey, b, 2*time.Minute)
		}
		return s.hydratePrincipal(plain, info, meta)
	}

	return nil, ErrInvalidCredentials
}

func (s *Service) hydratePrincipal(plain string, info cachedTokenInfo, meta ClientMeta) (*Principal, error) {
	if info.IsAPIKey {
		return &Principal{
			Token:    plain,
			IsAPIKey: true,
			APIKeyID: info.APIKeyID,
			Client:   meta,
			Policy: EffectivePolicy{
				AllowPlayback: true,
				CanViewHidden: true,
			},
		}, nil
	}
	now := time.Now()
	s.memMu.RLock()
	cached, ok := s.userCache[info.UserID]
	s.memMu.RUnlock()
	if ok && now.Before(cached.expiresAt) {
		user := cached.user
		if user.Disabled || (user.ExpiresAt != nil && now.After(*user.ExpiresAt)) {
			return nil, ErrUserDisabled
		}
		if meta.DeviceID == "" {
			meta.DeviceID = info.DeviceID
		}
		return &Principal{
			User:    &user,
			Token:   plain,
			TokenID: info.TokenID,
			Client:  meta,
			Policy:  cached.policy,
		}, nil
	}

	var user model.User
	if err := s.db.Read().Where("id = ?", info.UserID).First(&user).Error; err != nil {
		return nil, ErrInvalidCredentials
	}
	if user.Disabled || (user.ExpiresAt != nil && now.After(*user.ExpiresAt)) {
		return nil, ErrUserDisabled
	}
	policy := s.ResolvePolicy(&user)
	s.memMu.Lock()
	if s.userCache == nil {
		s.userCache = make(map[string]cachedUserEntry, 16)
	}
	s.userCache[info.UserID] = cachedUserEntry{
		user:      user,
		policy:    policy,
		expiresAt: now.Add(authMemCacheTTL),
	}
	s.memMu.Unlock()

	if meta.DeviceID == "" {
		meta.DeviceID = info.DeviceID
	}
	return &Principal{
		User:    &user,
		Token:   plain,
		TokenID: info.TokenID,
		Client:  meta,
		Policy:  policy,
	}, nil
}

// ResolvePolicy calculates the effective user policy (user overrides > permission group > system defaults).
func (s *Service) ResolvePolicy(user *model.User) EffectivePolicy {
	policy := EffectivePolicy{
		AllowPlayback: true,
		AllowDownload: s.cfg.Auth.DefaultCanDownload,
		CanViewHidden: user.IsAdmin,
	}
	if user.IsAdmin {
		policy.AllowPlayback = true
		policy.AllowDownload = true
		policy.CanViewHidden = true
		return policy
	}
	if user.PermissionGroupID != "" {
		var group model.PermissionGroup
		if err := s.db.Gorm.Where("id = ?", user.PermissionGroupID).First(&group).Error; err == nil {
			policy.AllowPlayback = group.AllowPlayback
			policy.AllowDownload = group.AllowDownload
			policy.MaxDevices = group.MaxDevices
			policy.MaxConcurrentStreams = group.MaxConcurrentStreams
			policy.CanViewHidden = group.CanViewHidden
		}
	}
	if user.AllowPlayback != nil {
		policy.AllowPlayback = *user.AllowPlayback
	}
	if user.CanDownload != nil {
		policy.AllowDownload = *user.CanDownload
	}
	if user.MaxDevices > 0 {
		policy.MaxDevices = user.MaxDevices
	}
	if user.CanViewHidden != nil {
		policy.CanViewHidden = *user.CanViewHidden
	}
	return policy
}

func (s *Service) cachedAllLibraries() ([]model.Library, map[string]bool, error) {
	libRev := s.db.GetLibraryRevision()
	now := time.Now()
	s.memMu.RLock()
	if s.allLibsCache.libRev == libRev && now.Before(s.allLibsCache.expiresAt) && s.allLibsCache.idSet != nil {
		libs := s.allLibsCache.libs
		idSet := s.allLibsCache.idSet
		s.memMu.RUnlock()
		return libs, idSet, nil
	}
	s.memMu.RUnlock()

	var allLibs []model.Library
	if err := s.db.Read().Order("sort_order ASC, name ASC").Find(&allLibs).Error; err != nil {
		return nil, nil, err
	}
	idSet := make(map[string]bool, len(allLibs))
	for _, l := range allLibs {
		idSet[l.ID] = true
	}
	s.memMu.Lock()
	s.allLibsCache = cachedAllLibsEntry{
		libs:      allLibs,
		idSet:     idSet,
		libRev:    libRev,
		expiresAt: now.Add(authMemCacheTTL),
	}
	s.memMu.Unlock()
	return allLibs, idSet, nil
}

// IsKnownLibraryID reports whether id matches any configured Library without hitting the DB on every request.
func (s *Service) IsKnownLibraryID(id string) bool {
	if id == "" {
		return false
	}
	_, idSet, err := s.cachedAllLibraries()
	if err != nil {
		return true // fail open so caller checks DB
	}
	return idSet[id]
}

// VisibleLibraryIDs resolves the list of libraries visible to a Principal.
// Precedence: explicit UserLibrary rules > GroupLibrary rules > all libraries (filtered by Hidden unless CanViewHidden).
func (s *Service) VisibleLibraryIDs(p *Principal) ([]string, error) {
	if p == nil {
		return nil, nil
	}
	libRev := s.db.GetLibraryRevision()
	cacheKey := fmt.Sprintf("%s:%d:api:%v", libRev, s.userEpoch.Load(), p.IsAPIKey)
	if p.User != nil {
		cacheKey = fmt.Sprintf("%s:%d:u:%s:%v:%v", libRev, s.userEpoch.Load(), p.User.ID, p.User.IsAdmin, p.Policy.CanViewHidden)
	}
	now := time.Now()
	s.memMu.RLock()
	if cached, ok := s.visCache[cacheKey]; ok && now.Before(cached.expiresAt) {
		out := make([]string, len(cached.ids))
		copy(out, cached.ids)
		s.memMu.RUnlock()
		return out, nil
	}
	s.memMu.RUnlock()

	allLibs, _, err := s.cachedAllLibraries()
	if err != nil {
		return nil, err
	}
	if p.IsAPIKey || (p.User != nil && p.User.IsAdmin) {
		ids := make([]string, 0, len(allLibs))
		for _, l := range allLibs {
			ids = append(ids, l.ID)
		}
		s.storeVisibleCache(cacheKey, ids, now)
		return ids, nil
	}

	user := p.User
	allowedSet := map[string]bool{}
	hasExplicit := false

	var userLibs []model.UserLibrary
	if err := s.db.Read().Where("user_id = ?", user.ID).Find(&userLibs).Error; err == nil && len(userLibs) > 0 {
		hasExplicit = true
		for _, ul := range userLibs {
			allowedSet[ul.LibraryID] = true
		}
	} else if user.PermissionGroupID != "" {
		var groupLibs []model.GroupLibrary
		if err := s.db.Read().Where("group_id = ?", user.PermissionGroupID).Find(&groupLibs).Error; err == nil && len(groupLibs) > 0 {
			hasExplicit = true
			for _, gl := range groupLibs {
				allowedSet[gl.LibraryID] = true
			}
		}
	}

	ids := make([]string, 0, len(allLibs))
	for _, l := range allLibs {
		if l.Hidden && !p.Policy.CanViewHidden {
			continue
		}
		if hasExplicit && !allowedSet[l.ID] {
			continue
		}
		ids = append(ids, l.ID)
	}
	s.storeVisibleCache(cacheKey, ids, now)
	return ids, nil
}

func (s *Service) storeVisibleCache(key string, ids []string, now time.Time) {
	cp := make([]string, len(ids))
	copy(cp, ids)
	s.memMu.Lock()
	if s.visCache == nil || len(s.visCache) > 256 {
		s.visCache = make(map[string]cachedVisibleLibsEntry, 16)
	}
	s.visCache[key] = cachedVisibleLibsEntry{
		ids:       cp,
		expiresAt: now.Add(authMemCacheTTL),
	}
	s.memMu.Unlock()
}

// CanAccessLibrary returns true if the Principal has read access to libraryID.
func (s *Service) CanAccessLibrary(p *Principal, libraryID string) bool {
	ids, err := s.VisibleLibraryIDs(p)
	if err != nil {
		return false
	}
	for _, id := range ids {
		if id == libraryID {
			return true
		}
	}
	return false
}

// AcquireOrRenewLease checks concurrent stream limits and refreshes a playback lease.
func (s *Service) AcquireOrRenewLease(p *Principal, playSessionID, itemID, mediaSourceID string, posTicks int64, isPaused bool) error {
	if p == nil || p.IsAPIKey || p.User == nil {
		return nil
	}
	now := time.Now().UTC()
	// Clean expired leases for this user.
	_ = s.db.Gorm.Where("user_id = ? AND expires_at < ?", p.User.ID, now).Delete(&model.PlaybackLease{}).Error

	if playSessionID == "" {
		playSessionID = p.User.ID + ":" + p.Client.DeviceID + ":" + itemID
	}

	if !p.User.IsAdmin && p.Policy.MaxConcurrentStreams > 0 {
		var active []model.PlaybackLease
		_ = s.db.Gorm.Where("user_id = ? AND expires_at >= ?", p.User.ID, now).Find(&active).Error
		distinctDevices := map[string]bool{}
		for _, l := range active {
			if l.PlaySessionID == playSessionID || l.DeviceID == p.Client.DeviceID {
				continue
			}
			distinctDevices[l.DeviceID] = true
		}
		if len(distinctDevices) >= p.Policy.MaxConcurrentStreams {
			return ErrDeviceLimitReached
		}
	}

	lease := model.PlaybackLease{
		PlaySessionID: playSessionID,
		UserID:        p.User.ID,
		DeviceID:      p.Client.DeviceID,
		DeviceName:    p.Client.Device,
		Client:        p.Client.Client,
		ItemID:        itemID,
		MediaSourceID: mediaSourceID,
		PositionTicks: posTicks,
		IsPaused:      isPaused,
		ExpiresAt:     now.Add(time.Duration(s.cfg.Auth.LeaseTTLSeconds) * time.Second),
		UpdatedAt:     now,
	}
	return s.db.Gorm.Save(&lease).Error
}

// ReleaseLease removes an active playback lease on stop.
func (s *Service) ReleaseLease(userID, playSessionID, deviceID, itemID string) {
	if playSessionID != "" {
		_ = s.db.Gorm.Where("user_id = ? AND play_session_id = ?", userID, playSessionID).Delete(&model.PlaybackLease{}).Error
		return
	}
	_ = s.db.Gorm.Where("user_id = ? AND device_id = ? AND item_id = ?", userID, deviceID, itemID).Delete(&model.PlaybackLease{}).Error
}

var authFieldRe = regexp.MustCompile(`([A-Za-z0-9_]+)\s*=\s*(?:"([^"]*)"|([^,\s]+))`)

func queryCI(r *http.Request, key string) string {
	if r == nil || r.URL == nil {
		return ""
	}
	if v := r.URL.Query().Get(key); v != "" {
		return v
	}
	for k, vals := range r.URL.Query() {
		if strings.EqualFold(k, key) && len(vals) > 0 && vals[0] != "" {
			return vals[0]
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// ExtractClientMeta parses Emby's multi-source authentication headers and query parameters.
func ExtractClientMeta(r *http.Request) ClientMeta {
	meta := ClientMeta{
		RemoteAddr: clientIP(r),
	}

	// Parse MediaBrowser / Emby auth headers (X-Emby-Authorization, X-MediaBrowser-Authorization, Authorization, or query).
	authHeaders := []string{
		r.Header.Get("X-Emby-Authorization"),
		r.Header.Get("X-MediaBrowser-Authorization"),
		r.Header.Get("Authorization"),
		queryCI(r, "X-Emby-Authorization"),
		queryCI(r, "X-MediaBrowser-Authorization"),
	}
	var headerAuthToken string
	for _, h := range authHeaders {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(h), "bearer ") && headerAuthToken == "" {
			headerAuthToken = strings.TrimSpace(h[7:])
			continue
		}
		for _, m := range authFieldRe.FindAllStringSubmatch(h, -1) {
			k := strings.ToLower(m[1])
			v := strings.TrimSpace(m[2])
			if v == "" {
				v = strings.TrimSpace(m[3])
			}
			switch k {
			case "client":
				if meta.Client == "" {
					meta.Client = v
				}
			case "device":
				if meta.Device == "" {
					meta.Device = v
				}
			case "deviceid":
				if meta.DeviceID == "" {
					meta.DeviceID = v
				}
			case "version":
				if meta.Version == "" {
					meta.Version = v
				}
			case "token":
				if headerAuthToken == "" {
					headerAuthToken = v
				}
			}
		}
	}

	// Fallback client/device/version metadata from Emby 4.8–4.10 dedicated headers and query parameters.
	if meta.Client == "" {
		meta.Client = firstNonEmpty(
			r.Header.Get("X-Emby-Client"),
			r.Header.Get("X-MediaBrowser-Client"),
			queryCI(r, "X-Emby-Client"),
			queryCI(r, "Client"),
		)
	}
	if meta.Version == "" {
		meta.Version = firstNonEmpty(
			r.Header.Get("X-Emby-Client-Version"),
			r.Header.Get("X-MediaBrowser-Client-Version"),
			queryCI(r, "X-Emby-Client-Version"),
			queryCI(r, "Version"),
		)
	}
	if meta.Device == "" {
		meta.Device = firstNonEmpty(
			r.Header.Get("X-Emby-Device-Name"),
			r.Header.Get("X-Emby-DeviceName"),
			r.Header.Get("X-MediaBrowser-Device-Name"),
			queryCI(r, "X-Emby-Device-Name"),
			queryCI(r, "DeviceName"),
			queryCI(r, "Device"),
		)
	}
	if meta.DeviceID == "" {
		meta.DeviceID = firstNonEmpty(
			r.Header.Get("X-Emby-Device-Id"),
			r.Header.Get("X-Emby-DeviceId"),
			r.Header.Get("X-MediaBrowser-Device-Id"),
			queryCI(r, "X-Emby-Device-Id"),
			queryCI(r, "DeviceId"),
		)
	}

	// User-Agent fallback for client identification when headers omit Client.
	if meta.Client == "" {
		ua := strings.ToLower(strings.TrimSpace(r.Header.Get("User-Agent")))
		switch {
		case strings.Contains(ua, "yamby"):
			meta.Client = "Yamby"
		case strings.Contains(ua, "infuse"):
			meta.Client = "Infuse"
		case strings.Contains(ua, "vidhub"):
			meta.Client = "VidHub"
		case strings.Contains(ua, "senplayer"):
			meta.Client = "SenPlayer"
		case strings.Contains(ua, "hills"):
			meta.Client = "Hills"
		case strings.Contains(ua, "afusekt"):
			meta.Client = "AfuseKt"
		case strings.Contains(ua, "fileball"):
			meta.Client = "Fileball"
		case strings.Contains(ua, "filmly"):
			meta.Client = "Filmly"
		case strings.Contains(ua, "emby"):
			meta.Client = "Emby"
		}
	}

	// Token priority per Emby 4.10 spec (headers first, then query params, case-insensitive).
	meta.Token = firstNonEmpty(
		r.Header.Get("X-Emby-Token"),
		r.Header.Get("X-MediaBrowser-Token"),
		headerAuthToken,
		r.Header.Get("X-Emby-Api-Key"),
		queryCI(r, "api_key"),
		queryCI(r, "X-Emby-Token"),
		queryCI(r, "X-MediaBrowser-Token"),
		queryCI(r, "X-Emby-Api-Key"),
		queryCI(r, "ApiKey"),
		queryCI(r, "Token"),
	)

	return meta
}

func clientIP(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[0]); ip != "" {
			return ip
		}
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		return xri
	}
	host := r.RemoteAddr
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		return host[:idx]
	}
	return host
}

func newID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}
