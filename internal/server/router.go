package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"emby-lite/internal/audit"
	"emby-lite/internal/auth"
	"emby-lite/internal/backup"
	"emby-lite/internal/cache"
	"emby-lite/internal/config"
	"emby-lite/internal/database"
	"emby-lite/internal/emby"
	"emby-lite/internal/files"
	imgpipe "emby-lite/internal/image"
	"emby-lite/internal/migration"
	"emby-lite/internal/nativeprobe"
	"emby-lite/internal/playback"
	"emby-lite/internal/scanner"
	"emby-lite/internal/scraper"
	"emby-lite/internal/task"
	"emby-lite/web"

	"github.com/gin-gonic/gin"
)

type Server struct {
	Engine   *gin.Engine
	Handler  http.Handler
	DB       *database.DB
	Cache    cache.Store
	Cfg      *config.Config
	Auth     *auth.Service
	Scanner  *scanner.Service
	Playback *playback.Service
	Tasks    *task.Queue
	Images   *imgpipe.Pipeline
	Scraper  *scraper.Service
}

func New(cfg *config.Config, db *database.DB, cacheStore cache.Store) *Server {
	if cfg.Server.Mode == "debug" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	prober := nativeprobe.New(nativeprobe.Options{EnableFFprobeFallback: true})
	imgPipeline := imgpipe.NewPipeline(cfg.Storage.CacheDir)
	authSvc := auth.NewService(db, cacheStore, cfg)
	scanSvc := scanner.NewService(db, prober)
	if cfg.Scanner.Concurrency > 0 {
		scanSvc.SetConcurrency(cfg.Scanner.Concurrency)
	}
	pbSvc := playback.NewService(db, authSvc, cfg)
	scraperSvc := scraper.NewService(db)
	fileMgr := files.NewManager(db, cfg)
	backupSvc := backup.NewService(db, cfg.Storage.BackupDir)
	migSvc := migration.NewService(db)
	auditLog := audit.NewLogger(db)
	taskQueue := task.NewQueue(db)

	taskQueue.Register("scan", func(ctx context.Context, payload []byte, report task.ProgressReporter) error {
		var p struct {
			LibraryID string `json:"library_id"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return err
		}
		_, err := scanSvc.ScanLibraryWithProgress(ctx, p.LibraryID, func(progress, total int, msg string) {
			report(progress, total, msg)
		})
		return err
	})
	taskQueue.Register("probe", func(ctx context.Context, payload []byte, report task.ProgressReporter) error {
		var p struct {
			LibraryID   string `json:"library_id"`
			IncludeSTRM bool   `json:"include_strm"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return err
		}
		_, err := scanSvc.ProbeLibrarySources(ctx, p.LibraryID, p.IncludeSTRM, func(progress, total int, msg string) {
			report(progress, total, msg)
		})
		return err
	})
	taskQueue.Register("scrape", func(ctx context.Context, payload []byte, report task.ProgressReporter) error {
		var p struct {
			LibraryID string `json:"library_id"`
			Force     bool   `json:"force"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return err
		}
		_, err := scraperSvc.ScrapeLibrary(ctx, p.LibraryID, p.Force, func(progress, total int, msg string) {
			report(progress, total, msg)
		})
		return err
	})
	taskQueue.Register("migrate_emby", func(ctx context.Context, payload []byte, report task.ProgressReporter) error {
		var req migration.EmbyMigrationRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return err
		}
		// The wizard can take many minutes on a real library, so it always runs here
		// instead of inside the HTTP request: closing the browser tab cannot abort it,
		// and the admin page can reattach to the live progress at any time.
		lastProgress, lastTotal := 0, 0
		rep, err := migSvc.ImportWithProgress(ctx, req, func(progress, total int, msg string) {
			lastProgress, lastTotal = progress, total
			report(progress, total, msg)
		})
		if err != nil {
			return err
		}
		// Leave the final counts on the task row: the wizard and the 后台任务 tab both
		// render task.message when the run completes.
		summary := fmt.Sprintf(
			"迁移完成：媒体库 +%d（已存在 %d），条目 +%d（跳过 %d；标签 %d、章节 %d、媒体流 %d、合集 %d），用户 +%d（已存在 %d，设置 %d），观看记录 +%d（跳过 %d）",
			rep.LibrariesCreated, rep.LibrariesExisting, rep.ItemsImported, rep.ItemsSkipped,
			rep.TagsImported, rep.ChaptersImported, rep.StreamsImported, rep.BoxSetsImported,
			rep.UsersCreated, rep.UsersExisting, rep.SettingsImported,
			rep.WatchImported, rep.WatchSkipped,
		)
		if merged := rep.SeriesMerged + rep.MoviesMerged + rep.EpisodesMerged; merged > 0 {
			summary += fmt.Sprintf("，自动合并：剧集 %d、电影 %d、单集 %d", rep.SeriesMerged, rep.MoviesMerged, rep.EpisodesMerged)
		}
		if rep.SnapshotDir != "" {
			summary += fmt.Sprintf("，原始库快照：%s（%d 个文件）", rep.SnapshotDir, len(rep.SnapshotFiles))
		}
		if rep.ItemsFailed > 0 || rep.WatchFailed > 0 {
			// Batched writes report how many rows had to be dropped; surfacing them keeps a
			// silently lossy migration from looking like a clean one.
			summary += fmt.Sprintf("，写入失败：条目 %d 条、观看记录 %d 条", rep.ItemsFailed, rep.WatchFailed)
		}
		report(lastProgress, lastTotal, summary)
		if len(rep.Warnings) > 0 {
			slog.Warn("emby migration finished with warnings", "task", "migrate_emby", "warnings", rep.Warnings)
		}
		return nil
	})
	taskQueue.Start(2)

	embyHandler := emby.NewHandler(db, cacheStore, cfg, authSvc, scanSvc, pbSvc, imgPipeline, scraperSvc)
	adminHandler := NewAdminHandler(db, cacheStore, cfg, authSvc, scanSvc, pbSvc, taskQueue, imgPipeline, scraperSvc, fileMgr, backupSvc, migSvc, auditLog)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(corsMiddleware())
	r.Use(gzipMiddleware())

	// Public health check
	r.GET("/health", embyHandler.Health)

	// Register Emby API under root and /emby (normalized to lowercase static segments by middleware).
	registerEmbyRoutes(r, embyHandler)

	// Web helper APIs
	webAPI := r.Group("/api/web")
	{
		webAPI.GET("/bootstrap", adminHandler.Bootstrap)
		webAPI.POST("/setup", adminHandler.Setup)
		webAPI.GET("/home", embyHandler.RequireAuth(), embyHandler.WebHome)
		webAPI.GET("/home-settings", embyHandler.RequireAuth(), embyHandler.GetWebHomeSettings)
		webAPI.PUT("/home-settings", embyHandler.RequireAuth(), embyHandler.UpdateWebHomeSettings)
		webAPI.GET("/external-play/:itemId", adminHandler.ExternalPlay)
		webAPI.GET("/intro/:itemId", adminHandler.GetItemIntroWeb)
	}

	// Admin console APIs (strictly admin session only)
	adminAPI := r.Group("/api/admin", adminHandler.RequireAdmin())
	{
		adminAPI.GET("/dashboard", adminHandler.Dashboard)
		adminAPI.GET("/directories", adminHandler.BrowseDirectories)
		adminAPI.GET("/libraries", adminHandler.ListLibraries)
		adminAPI.POST("/libraries", adminHandler.CreateLibrary)
		adminAPI.PUT("/libraries/reorder", adminHandler.ReorderLibraries)
		adminAPI.PUT("/libraries/:id", adminHandler.UpdateLibrary)
		adminAPI.DELETE("/libraries/:id", adminHandler.DeleteLibrary)
		adminAPI.POST("/libraries/:id/scan", adminHandler.ScanLibrary)
		adminAPI.POST("/libraries/:id/consolidate", adminHandler.ConsolidateLibrary)
		adminAPI.POST("/libraries/:id/probe", adminHandler.ProbeLibrary)
		adminAPI.POST("/libraries/:id/scrape", adminHandler.ScrapeLibrary)
		adminAPI.POST("/libraries/:id/cover/generate", adminHandler.GenerateLibraryCover)
		adminAPI.POST("/libraries/:id/cover", adminHandler.UploadLibraryCover)
		adminAPI.DELETE("/libraries/:id/cover", adminHandler.DeleteLibraryCover)
		adminAPI.POST("/items/:id/scrape", adminHandler.ScrapeSingleItem)
		adminAPI.POST("/scan-all", adminHandler.ScanAll)

		adminAPI.GET("/virtual-libraries", adminHandler.ListVirtualLibraries)
		adminAPI.POST("/virtual-libraries", adminHandler.CreateVirtualLibrary)
		adminAPI.PUT("/virtual-libraries/:id", adminHandler.UpdateVirtualLibrary)
		adminAPI.DELETE("/virtual-libraries/:id", adminHandler.DeleteVirtualLibrary)

		adminAPI.GET("/intro-markers", adminHandler.ListIntroMarkers)
		adminAPI.PUT("/intro-markers", adminHandler.UpsertIntroMarker)
		adminAPI.DELETE("/intro-markers/:id", adminHandler.DeleteIntroMarker)

		adminAPI.GET("/devices", adminHandler.ListDevices)
		adminAPI.POST("/devices/:userId/:deviceId/block", adminHandler.ToggleBlockDevice)
		adminAPI.DELETE("/devices/:userId/:deviceId", adminHandler.DeleteDevice)
		adminAPI.GET("/sessions", adminHandler.ListActiveSessions)
		adminAPI.DELETE("/sessions/:sessionId", adminHandler.TerminateSession)

		adminAPI.GET("/tmdb", adminHandler.GetTMDBConfig)
		adminAPI.PUT("/tmdb", adminHandler.UpdateTMDBConfig)
		adminAPI.POST("/tmdb/test", adminHandler.TestTMDBConnection)
		adminAPI.GET("/scraper/queue", adminHandler.ListMatchQueue)
		adminAPI.POST("/scraper/search", adminHandler.ManualTMDBSearch)
		adminAPI.POST("/scraper/queue/:itemId/resolve", adminHandler.ResolveManualMatch)
		adminAPI.POST("/scraper/queue/:itemId/ignore", adminHandler.IgnoreManualMatch)

		adminAPI.GET("/metadata/:itemId", adminHandler.GetItemMetadata)
		adminAPI.PUT("/metadata/:itemId", adminHandler.UpdateItemMetadata)

		adminAPI.GET("/tasks", adminHandler.ListTasks)
		adminAPI.GET("/tasks/events", adminHandler.TaskEventsSSE)
		adminAPI.GET("/tasks/:id", adminHandler.GetTask)
		adminAPI.POST("/tasks/:id/cancel", adminHandler.CancelTask)

		adminAPI.GET("/cache/stats", adminHandler.CacheStats)
		adminAPI.POST("/cache/clear", adminHandler.ClearCache)

		adminAPI.GET("/users", adminHandler.ListUsers)
		adminAPI.POST("/users", adminHandler.CreateUser)
		adminAPI.PUT("/users/:id", adminHandler.UpdateUser)
		adminAPI.DELETE("/users/:id", adminHandler.DeleteUser)
		adminAPI.GET("/permission-groups", adminHandler.ListPermissionGroupsFull)
		adminAPI.POST("/permission-groups", adminHandler.CreatePermissionGroup)
		adminAPI.PUT("/permission-groups/:id", adminHandler.UpdatePermissionGroup)
		adminAPI.DELETE("/permission-groups/:id", adminHandler.DeletePermissionGroup)

		adminAPI.GET("/settings", adminHandler.GetSettings)
		adminAPI.PUT("/settings", adminHandler.UpdateSettings)

		adminAPI.GET("/backups", adminHandler.ListBackups)
		adminAPI.POST("/backups", adminHandler.CreateBackup)
		adminAPI.GET("/backups/:filename/download", adminHandler.DownloadBackup)
		adminAPI.POST("/backups/:filename/restore", adminHandler.RestoreBackup)
		adminAPI.DELETE("/backups/:filename", adminHandler.DeleteBackup)

		adminAPI.POST("/migration/emby/preview", adminHandler.PreviewEmbyMigration)
		adminAPI.POST("/migration/emby/import", adminHandler.ImportEmbyMigration)

		adminAPI.GET("/keys", adminHandler.ListAPIKeys)
		adminAPI.POST("/keys", adminHandler.CreateAPIKey)
		adminAPI.DELETE("/keys/:id", adminHandler.DeleteAPIKey)
		adminAPI.GET("/audit-logs", adminHandler.ListAuditLogs)
	}

	// Restricted Admin File Manager (/api/files/*)
	filesAPI := r.Group("/api/files", adminHandler.RequireAdmin())
	{
		filesAPI.GET("", adminHandler.FilesList)
		filesAPI.POST("/mkdir", adminHandler.FilesMkdir)
		filesAPI.POST("/rename", adminHandler.FilesRename)
		filesAPI.POST("/move", adminHandler.FilesMove)
		filesAPI.POST("/upload", adminHandler.FilesUpload)
		filesAPI.GET("/download", adminHandler.FilesDownload)
		filesAPI.HEAD("/download", adminHandler.FilesDownload)
		filesAPI.DELETE("", adminHandler.FilesDelete)
	}

	// Embedded Vue 3 SPA fallback
	attachSPA(r)

	return &Server{
		Engine:   r,
		Handler:  embyPathNormalizerHandler(r),
		DB:       db,
		Cache:    cacheStore,
		Cfg:      cfg,
		Auth:     authSvc,
		Scanner:  scanSvc,
		Playback: pbSvc,
		Tasks:    taskQueue,
		Images:   imgPipeline,
		Scraper:  scraperSvc,
	}
}

func (s *Server) Close() {
	if s.Tasks != nil {
		s.Tasks.Stop()
	}
}

func registerEmbyRoutes(r *gin.Engine, h *emby.Handler) {
	// Public Emby endpoints (static segments lowercase because normalizer folds Emby paths).
	r.GET("/emby-root", h.SystemInfoPublic)
	r.HEAD("/emby-root", h.SystemInfoPublic)
	r.GET("/system/ping", h.Ping)
	r.HEAD("/system/ping", h.Ping)
	r.POST("/system/ping", h.Ping)
	r.GET("/system/info/public", h.SystemInfoPublic)
	r.HEAD("/system/info/public", h.SystemInfoPublic)
	r.GET("/system/endpoint", h.SystemEndpoint)
	r.HEAD("/system/endpoint", h.SystemEndpoint)
	r.GET("/system/configuration/public", h.SystemConfigurationPublic)
	r.HEAD("/system/configuration/public", h.SystemConfigurationPublic)
	r.GET("/startup/configuration", h.SystemConfigurationPublic)
	r.HEAD("/startup/configuration", h.SystemConfigurationPublic)
	r.POST("/startup/complete", h.NoContent)
	r.GET("/quickconnect/enabled", h.QuickConnectEnabled)
	r.HEAD("/quickconnect/enabled", h.QuickConnectEnabled)
	r.GET("/branding/configuration", h.BrandingConfiguration)
	r.HEAD("/branding/configuration", h.BrandingConfiguration)
	r.GET("/branding/css", h.BrandingCSS)
	r.HEAD("/branding/css", h.BrandingCSS)
	r.GET("/branding/css.css", h.BrandingCSS)
	r.HEAD("/branding/css.css", h.BrandingCSS)
	r.GET("/localization/options", h.LocalizationOptions)
	r.GET("/localization/cultures", h.LocalizationCultures)
	r.GET("/localization/countries", h.LocalizationCountries)
	r.GET("/localization/parentalratings", h.EmptyArray)
	r.GET("/web/manifest.json", h.WebManifest)
	r.HEAD("/web/manifest.json", h.WebManifest)
	r.GET("/users/public", h.UsersPublic)
	r.POST("/users/authenticatebyname", h.AuthenticateByName)
	r.GET("/embywebsocket", h.EmbyWebSocket)
	r.HEAD("/embywebsocket", h.EmbyWebSocket)
	r.GET("/displaypreferences/:id", h.DisplayPreferences)
	r.POST("/displaypreferences/:id", h.SaveDisplayPreferences)

	// Images can authenticate via token OR signed tag (handled inside handler).
	r.GET("/items/:itemId/images/:type", h.GetItemImage)
	r.HEAD("/items/:itemId/images/:type", h.GetItemImage)
	r.GET("/items/:itemId/images/:type/:index", h.GetItemImage)
	r.HEAD("/items/:itemId/images/:type/:index", h.GetItemImage)
	r.GET("/items/:itemId/images/:type/:index/:tag", h.GetItemImage)
	r.HEAD("/items/:itemId/images/:type/:index/:tag", h.GetItemImage)
	r.GET("/persons/:personId/images/:type", h.GetPersonImage)
	r.HEAD("/persons/:personId/images/:type", h.GetPersonImage)
	r.GET("/persons/:personId/images/:type/:index", h.GetPersonImage)
	r.HEAD("/persons/:personId/images/:type/:index", h.GetPersonImage)
	r.GET("/users/:userId/images/:type", h.GetUserImage)
	r.HEAD("/users/:userId/images/:type", h.GetUserImage)
	r.GET("/users/:userId/images/:type/:index", h.GetUserImage)
	r.HEAD("/users/:userId/images/:type/:index", h.GetUserImage)

	// Authenticated Emby endpoints.
	authed := r.Group("", h.RequireAuth())
	{
		authed.GET("/system/info", h.SystemInfo)
		authed.HEAD("/system/info", h.SystemInfo)
		authed.GET("/system/configuration", h.SystemConfiguration)
		authed.GET("/system/wakeonlaninfo", h.EmptyArray)
		authed.GET("/system/activitylog/entries", h.EmptyItemsResult)
		authed.GET("/web/configurationpages", h.EmptyArray)
		authed.GET("/users/me", h.UserMe)
		authed.GET("/users/:userId", h.GetUser)
		authed.POST("/users/:userId/configuration", h.UpdateUserConfiguration)
		authed.POST("/sessions/logout", h.Logout)

		// Libraries, Views & Emby 4.10 Home Sections
		authed.GET("/users/:userId/views", h.UserViews)
		authed.GET("/users/:userId/homesections", h.GetHomeSections)
		authed.GET("/users/:userId/sections/:sectionName/items", h.GetSectionItems)
		authed.GET("/library/mediafolders", h.UserViews)
		authed.GET("/library/virtualfolders", h.VirtualFolders)
		authed.GET("/library/virtualfolders/query", h.VirtualFolders)
		authed.GET("/library/selectablemediafolders", h.SelectableMediaFolders)
		authed.GET("/users/:userId/items/root", h.RootItem)
		authed.GET("/users/:userId/groupingoptions", h.EmptyArray)

		// Items browsing, search & detail
		authed.GET("/items", h.GetItems)
		authed.GET("/users/:userId/items", h.GetItems)
		authed.GET("/items/:itemId/children", h.GetItems)
		authed.GET("/items/latest", h.GetLatest)
		authed.GET("/users/:userId/items/latest", h.GetLatest)
		authed.GET("/items/resume", h.GetResume)
		authed.GET("/users/:userId/items/resume", h.GetResume)
		authed.GET("/items/counts", h.GetCounts)
		authed.GET("/users/:userId/items/counts", h.GetCounts)
		authed.GET("/items/filters", h.GetFilters)
		authed.GET("/items/filters2", h.GetFilters)
		authed.GET("/items/:itemId", h.GetItemDetail)
		authed.GET("/users/:userId/items/:itemId", h.GetItemDetail)
		authed.GET("/items/:itemId/images", h.GetItemImagesList)
		authed.GET("/items/:itemId/similar", h.GetSimilar)
		authed.GET("/users/:userId/items/:itemId/similar", h.GetSimilar)
		authed.GET("/items/:itemId/specialfeatures", h.EmptyArray)
		authed.GET("/users/:userId/items/:itemId/specialfeatures", h.EmptyArray)
		authed.GET("/items/:itemId/intros", h.EmptyItemsResult)
		authed.GET("/users/:userId/items/:itemId/intros", h.EmptyItemsResult)
		authed.GET("/items/:itemId/localtrailers", h.EmptyArray)
		authed.GET("/users/:userId/items/:itemId/localtrailers", h.EmptyArray)
		authed.GET("/items/:itemId/thememedia", h.GetThemeMedia)
		authed.GET("/items/:itemId/themesongs", h.EmptyItemsResult)
		authed.GET("/items/:itemId/themevideos", h.EmptyItemsResult)
		authed.GET("/items/:itemId/thumbnailset", h.EmptyItemsResult)
		authed.GET("/mediasegments/:itemId", h.GetMediaSegments)
		authed.GET("/search/hints", h.SearchHints)

		// TV Shows, Genres, Tags, Studios, Persons
		authed.GET("/shows/:seriesId/seasons", h.GetSeasons)
		authed.GET("/users/:userId/shows/:seriesId/seasons", h.GetSeasons)
		authed.GET("/shows/:seriesId/episodes", h.GetEpisodes)
		authed.GET("/users/:userId/shows/:seriesId/episodes", h.GetEpisodes)
		authed.GET("/shows/nextup", h.GetNextUp)
		authed.GET("/users/:userId/shows/nextup", h.GetNextUp)
		authed.GET("/shows/upcoming", h.EmptyItemsResult)
		authed.GET("/users/:userId/shows/upcoming", h.EmptyItemsResult)
		authed.GET("/movies/recommendations", h.EmptyArray)
		authed.GET("/genres", h.GetGenres)
		authed.GET("/tags", h.GetTags)
		authed.GET("/studios", h.GetStudios)
		authed.GET("/artists", h.EmptyItemsResult)
		authed.GET("/artists/albumartists", h.EmptyItemsResult)
		authed.GET("/livetv/recordings", h.EmptyItemsResult)
		authed.GET("/persons", h.GetPersons)
		authed.GET("/persons/:personId", h.GetPersonDetail)

		// Playback & Subtitles
		authed.GET("/items/:itemId/playbackinfo", h.PlaybackInfo)
		authed.POST("/items/:itemId/playbackinfo", h.PlaybackInfo)
		authed.GET("/users/:userId/items/:itemId/playbackinfo", h.PlaybackInfo)
		authed.POST("/users/:userId/items/:itemId/playbackinfo", h.PlaybackInfo)
		authed.GET("/videos/:itemId/additionalparts", h.EmptyItemsResult)
		authed.GET("/videos/:itemId/stream", h.StreamVideo)
		authed.HEAD("/videos/:itemId/stream", h.StreamVideo)
		authed.GET("/videos/:itemId/stream.:ext", h.StreamVideo)
		authed.HEAD("/videos/:itemId/stream.:ext", h.StreamVideo)
		authed.GET("/videos/:itemId/original", h.StreamVideo)
		authed.HEAD("/videos/:itemId/original", h.StreamVideo)
		authed.GET("/videos/:itemId/original.:ext", h.StreamVideo)
		authed.HEAD("/videos/:itemId/original.:ext", h.StreamVideo)
		authed.GET("/items/:itemId/download", h.DownloadVideo)
		authed.HEAD("/items/:itemId/download", h.DownloadVideo)
		authed.GET("/users/:userId/items/:itemId/download", h.DownloadVideo)
		authed.HEAD("/users/:userId/items/:itemId/download", h.DownloadVideo)
		authed.GET("/videos/:itemId/master.m3u8", h.RejectTranscode)
		authed.GET("/videos/:itemId/main.m3u8", h.RejectTranscode)
		authed.GET("/videos/:itemId/:sourceId/subtitles/:index/*subpath", h.GetSubtitleStream)
		authed.HEAD("/videos/:itemId/:sourceId/subtitles/:index/*subpath", h.GetSubtitleStream)

		// Playback progress & UserData
		authed.POST("/sessions/playing", h.ReportPlaying)
		authed.POST("/sessions/playing/progress", h.ReportProgress)
		authed.POST("/sessions/playing/stopped", h.ReportStopped)
		authed.POST("/sessions/capabilities", h.Capabilities)
		authed.POST("/sessions/capabilities/full", h.Capabilities)

		authed.GET("/users/:userId/items/:itemId/userdata", h.GetUserData)
		authed.POST("/users/:userId/items/:itemId/userdata", h.UpdateUserData)
		authed.POST("/users/:userId/playeditems/:itemId", h.SetPlayed(true))
		authed.DELETE("/users/:userId/playeditems/:itemId", h.SetPlayed(false))
		authed.POST("/users/:userId/playeditems/:itemId/delete", h.SetPlayed(false))
		authed.POST("/users/:userId/favoriteitems/:itemId", h.SetFavorite(true))
		authed.DELETE("/users/:userId/favoriteitems/:itemId", h.SetFavorite(false))
		authed.POST("/users/:userId/favoriteitems/:itemId/delete", h.SetFavorite(false))
		authed.POST("/users/:userId/items/:itemId/hidefromresume", h.HideFromResume)
		authed.DELETE("/items/:itemId/resume", h.ClearResume)
		authed.DELETE("/items/resume", h.ClearResume)

		// Standard Admin compat
		authed.GET("/users", h.ListUsersCompat)
		authed.GET("/users/query", h.ListUsersCompat)
		authed.GET("/sessions", h.ListSessionsCompat)
		authed.GET("/scheduledtasks", h.ScheduledTasksCompat)
		authed.POST("/library/refresh", h.RefreshLibraryCompat)
		authed.POST("/items/:itemId/refresh", h.RefreshLibraryCompat)
		authed.POST("/videos/mergeversions", h.MergeVersionsCompat)
		authed.DELETE("/videos/:itemId/alternatesources", h.SplitVersionsCompat)
	}
}

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			origin = "*"
		}
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Allow-Private-Network", "true")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Accept, Accept-Language, Authorization, Cache-Control, Content-Type, If-Modified-Since, If-None-Match, Origin, Pragma, Range, X-Emby-Token, X-Emby-Authorization, X-Emby-Client, X-Emby-Client-Version, X-Emby-Device-Id, X-Emby-Device-Name, X-Emby-Language, X-Emby-Api-Key, X-MediaBrowser-Token, X-MediaBrowser-Authorization")
		c.Header("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges, X-Next-Cursor")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

type embyPrefixCtxKeyType struct{}

var embyPrefixCtxKey = embyPrefixCtxKeyType{}

// embyPathNormalizerMiddleware strips /emby prefix, collapses duplicate slashes and trailing slashes,
// and folds known Emby route keywords to lowercase while preserving dynamic IDs intact.
var embyKeywords = map[string]bool{
	"system": true, "ping": true, "info": true, "public": true, "endpoint": true, "configuration": true, "wakeonlaninfo": true, "activitylog": true, "entries": true,
	"startup": true, "complete": true, "quickconnect": true, "enabled": true,
	"branding": true, "css": true, "css.css": true,
	"localization": true, "options": true, "cultures": true, "countries": true, "parentalratings": true,
	"web": true, "configurationpages": true, "manifest.json": true, "embywebsocket": true,
	"users": true, "authenticatebyname": true, "me": true, "views": true, "query": true, "homesections": true, "sections": true,
	"sessions": true, "logout": true, "playing": true, "progress": true, "stopped": true, "capabilities": true, "full": true,
	"library": true, "mediafolders": true, "virtualfolders": true, "selectablemediafolders": true, "refresh": true,
	"items": true, "root": true, "children": true, "latest": true, "resume": true, "counts": true,
	"filters": true, "filters2": true, "similar": true, "images": true, "playbackinfo": true, "download": true,
	"specialfeatures": true, "intros": true, "localtrailers": true, "thememedia": true, "themesongs": true, "themevideos": true, "thumbnailset": true,
	"mediasegments": true, "search": true, "hints": true,
	"userdata": true, "playeditems": true, "favoriteitems": true, "delete": true, "hidefromresume": true, "groupingoptions": true,
	"shows": true, "seasons": true, "episodes": true, "nextup": true, "upcoming": true,
	"movies": true, "recommendations": true, "genres": true, "tags": true, "studios": true, "persons": true,
	"artists": true, "albumartists": true, "livetv": true, "recordings": true,
	"displaypreferences": true, "videos": true, "additionalparts": true, "subtitles": true, "scheduledtasks": true,
	"mergeversions": true, "alternatesources": true,
	"stream": true, "original": true, "master.m3u8": true, "main.m3u8": true,
}

func embyPathNormalizerHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		// Do not touch /api/* or /assets/*
		if !strings.HasPrefix(p, "/api/") && !strings.HasPrefix(p, "/assets/") {
			orig := p
			for strings.Contains(p, "//") {
				p = strings.ReplaceAll(p, "//", "/")
			}
			if len(p) > 1 {
				p = strings.TrimRight(p, "/")
			}

			hasEmbyPrefix := false
			for {
				lower := strings.ToLower(p)
				if lower == "/emby" {
					hasEmbyPrefix = true
					p = "/emby-root"
					break
				}
				if strings.HasPrefix(lower, "/emby/") {
					hasEmbyPrefix = true
					p = p[5:]
					if len(p) > 1 {
						p = strings.TrimRight(p, "/")
					}
					continue
				}
				break
			}

			parts := strings.Split(p, "/")
			changed := p != orig
			for i, seg := range parts {
				if seg == "" {
					continue
				}
				ls := strings.ToLower(seg)
				if embyKeywords[ls] || strings.HasPrefix(ls, "stream.") || strings.HasPrefix(ls, "original.") {
					if seg != ls {
						parts[i] = ls
						changed = true
					}
				}
			}
			if changed {
				req.URL.Path = strings.Join(parts, "/")
			}
			if hasEmbyPrefix {
				req = req.WithContext(context.WithValue(req.Context(), embyPrefixCtxKey, true))
			}
		}
		next.ServeHTTP(w, req)
	})
}

func isEmbyAPIRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if v, ok := r.Context().Value(embyPrefixCtxKey).(bool); ok && v {
		return true
	}
	for _, h := range []string{"X-Emby-Token", "X-Emby-Authorization", "X-MediaBrowser-Token", "X-MediaBrowser-Authorization", "X-Emby-Client", "X-Emby-Device-Id", "X-Emby-Api-Key"} {
		if strings.TrimSpace(r.Header.Get(h)) != "" {
			return true
		}
	}
	for k := range r.URL.Query() {
		if strings.EqualFold(k, "api_key") || strings.EqualFold(k, "X-Emby-Token") || strings.EqualFold(k, "X-MediaBrowser-Token") {
			return true
		}
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	if strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html") {
		return true
	}
	return false
}

func attachSPA(r *gin.Engine) {
	distFS, err := fs.Sub(web.DistFS, "dist")
	if err != nil {
		return
	}
	fileServer := http.FileServer(http.FS(distFS))

	r.NoRoute(func(c *gin.Context) {
		p := strings.TrimPrefix(c.Request.URL.Path, "/")
		if (c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead) || strings.HasPrefix(p, "api/") || isEmbyAPIRequest(c.Request) {
			c.JSON(http.StatusNotFound, gin.H{"error": "endpoint not found", "Message": "endpoint not found"})
			return
		}
		// Serve static asset if it exists in embedded dist.
		if p != "" {
			if f, err := distFS.Open(p); err == nil {
				_ = f.Close()
				if strings.HasPrefix(p, "assets/") {
					c.Header("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					c.Header("Cache-Control", "public, max-age=3600")
				}
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}
		}
		// Fallback to SPA index.html.
		f, err := distFS.Open("index.html")
		if err != nil {
			c.String(http.StatusNotFound, "Frontend bundle not found")
			return
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, "text/html; charset=utf-8", data)
	})
}
