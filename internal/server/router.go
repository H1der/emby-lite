package server

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
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
	taskQueue.Start(2)

	embyHandler := emby.NewHandler(db, cacheStore, cfg, authSvc, scanSvc, pbSvc, imgPipeline, scraperSvc)
	adminHandler := NewAdminHandler(db, cacheStore, cfg, authSvc, scanSvc, pbSvc, taskQueue, imgPipeline, scraperSvc, fileMgr, backupSvc, migSvc, auditLog)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(corsMiddleware())

	// Public health check
	r.GET("/health", embyHandler.Health)

	// Register Emby API under root and /emby (normalized to lowercase static segments by middleware).
	registerEmbyRoutes(r, embyHandler)

	// Web helper APIs
	webAPI := r.Group("/api/web")
	{
		webAPI.GET("/bootstrap", adminHandler.Bootstrap)
		webAPI.POST("/setup", adminHandler.Setup)
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
		adminAPI.PUT("/libraries/:id", adminHandler.UpdateLibrary)
		adminAPI.DELETE("/libraries/:id", adminHandler.DeleteLibrary)
		adminAPI.POST("/libraries/:id/scan", adminHandler.ScanLibrary)
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
		adminAPI.POST("/tasks/:id/cancel", adminHandler.CancelTask)
		adminAPI.GET("/tasks/events", adminHandler.TaskEventsSSE)

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
	r.GET("/system/ping", h.Ping)
	r.POST("/system/ping", h.Ping)
	r.GET("/system/info/public", h.SystemInfoPublic)
	r.GET("/users/public", h.UsersPublic)
	r.POST("/users/authenticatebyname", h.AuthenticateByName)

	// Images can authenticate via token OR signed tag (handled inside handler).
	r.GET("/items/:itemId/images/:type", h.GetItemImage)
	r.HEAD("/items/:itemId/images/:type", h.GetItemImage)
	r.GET("/items/:itemId/images/:type/:index", h.GetItemImage)
	r.HEAD("/items/:itemId/images/:type/:index", h.GetItemImage)
	r.GET("/persons/:personId/images/:type", h.GetPersonImage)
	r.HEAD("/persons/:personId/images/:type", h.GetPersonImage)

	// Authenticated Emby endpoints.
	authed := r.Group("", h.RequireAuth())
	{
		authed.GET("/system/info", h.SystemInfo)
		authed.GET("/system/endpoint", h.SystemEndpoint)
		authed.GET("/users/me", h.UserMe)
		authed.GET("/users/:userId", h.GetUser)
		authed.POST("/sessions/logout", h.Logout)

		// Libraries & Views
		authed.GET("/users/:userId/views", h.UserViews)
		authed.GET("/library/mediafolders", h.UserViews)
		authed.GET("/library/virtualfolders", h.VirtualFolders)
		authed.GET("/library/virtualfolders/query", h.VirtualFolders)
		authed.GET("/library/selectablemediafolders", h.SelectableMediaFolders)
		authed.GET("/users/:userId/items/root", h.RootItem)
		authed.GET("/users/:userId/groupingoptions", h.EmptyArray)

		// Items browsing & detail
		authed.GET("/items", h.GetItems)
		authed.GET("/users/:userId/items", h.GetItems)
		authed.GET("/items/:itemId/children", h.GetItems)
		authed.GET("/items/latest", h.GetLatest)
		authed.GET("/users/:userId/items/latest", h.GetLatest)
		authed.GET("/items/resume", h.GetResume)
		authed.GET("/users/:userId/items/resume", h.GetResume)
		authed.GET("/items/counts", h.GetCounts)
		authed.GET("/items/filters", h.GetFilters)
		authed.GET("/items/filters2", h.GetFilters)
		authed.GET("/items/:itemId", h.GetItemDetail)
		authed.GET("/users/:userId/items/:itemId", h.GetItemDetail)
		authed.GET("/items/:itemId/similar", h.GetSimilar)

		// TV Shows, Genres, Tags, DisplayPreferences
		authed.GET("/shows/:seriesId/seasons", h.GetSeasons)
		authed.GET("/shows/:seriesId/episodes", h.GetEpisodes)
		authed.GET("/shows/nextup", h.GetNextUp)
		authed.GET("/movies/recommendations", h.EmptyArray)
		authed.GET("/genres", h.GetGenres)
		authed.GET("/tags", h.GetTags)
		authed.GET("/persons", h.GetPersons)
		authed.GET("/persons/:personId", h.GetPersonDetail)
		authed.GET("/displaypreferences/:id", h.DisplayPreferences)

		// Playback & Subtitles
		authed.GET("/items/:itemId/playbackinfo", h.PlaybackInfo)
		authed.POST("/items/:itemId/playbackinfo", h.PlaybackInfo)
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
		authed.GET("/videos/:itemId/master.m3u8", h.RejectTranscode)
		authed.GET("/videos/:itemId/main.m3u8", h.RejectTranscode)
		authed.GET("/videos/:itemId/:sourceId/subtitles/:index/stream.:ext", h.GetSubtitleStream)
		authed.HEAD("/videos/:itemId/:sourceId/subtitles/:index/stream.:ext", h.GetSubtitleStream)

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
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Emby-Token, X-Emby-Authorization, X-MediaBrowser-Token, X-Emby-Api-Key, X-Emby-Device-Id")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// embyPathNormalizerMiddleware strips /emby prefix and folds known Emby route keywords to lowercase
// while preserving dynamic IDs (itemId, userId, sourceId) intact.
var embyKeywords = map[string]bool{
	"system": true, "ping": true, "info": true, "public": true, "endpoint": true,
	"users": true, "authenticatebyname": true, "me": true, "views": true, "query": true,
	"sessions": true, "logout": true, "playing": true, "progress": true, "stopped": true, "capabilities": true, "full": true,
	"library": true, "mediafolders": true, "virtualfolders": true, "selectablemediafolders": true, "refresh": true,
	"items": true, "root": true, "children": true, "latest": true, "resume": true, "counts": true,
	"filters": true, "filters2": true, "similar": true, "images": true, "playbackinfo": true, "download": true,
	"userdata": true, "playeditems": true, "favoriteitems": true, "delete": true, "hidefromresume": true, "groupingoptions": true,
	"shows": true, "seasons": true, "episodes": true, "nextup": true,
	"movies": true, "recommendations": true, "genres": true, "tags": true, "persons": true,
	"displaypreferences": true, "videos": true, "subtitles": true, "scheduledtasks": true,
	"stream": true, "original": true, "master.m3u8": true, "main.m3u8": true,
}

func embyPathNormalizerHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		// Do not touch /api/* or /assets/*
		if !strings.HasPrefix(p, "/api/") && !strings.HasPrefix(p, "/assets/") {
			lower := strings.ToLower(p)
			if lower == "/emby" {
				p = "/"
			} else if strings.HasPrefix(lower, "/emby/") {
				p = p[5:]
			}

			parts := strings.Split(p, "/")
			changed := p != req.URL.Path
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
		}
		next.ServeHTTP(w, req)
	})
}

func attachSPA(r *gin.Engine) {
	distFS, err := fs.Sub(web.DistFS, "dist")
	if err != nil {
		return
	}
	fileServer := http.FileServer(http.FS(distFS))

	r.NoRoute(func(c *gin.Context) {
		p := strings.TrimPrefix(c.Request.URL.Path, "/")
		if (c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead) || strings.HasPrefix(p, "api/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "endpoint not found", "Message": "endpoint not found"})
			return
		}
		// Serve static asset if it exists in embedded dist.
		if p != "" {
			if f, err := distFS.Open(p); err == nil {
				_ = f.Close()
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
		c.Data(http.StatusOK, "text/html; charset=utf-8", data)
	})
}
