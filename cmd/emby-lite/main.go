package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"emby-lite/internal/cache"
	"emby-lite/internal/config"
	"emby-lite/internal/database"
	"emby-lite/internal/server"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "", "path to YAML config file")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	setupLogger(cfg.Log.Level)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := database.Open(cfg)
	if err != nil {
		slog.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	cacheStore := cache.New(ctx, cfg)
	defer func() { _ = cacheStore.Close() }()

	srv := server.New(cfg, db, cacheStore)
	defer srv.Close()
	if err := srv.Auth.BootstrapAdminFromEnv(); err != nil {
		slog.Error("failed to bootstrap initial admin from env", "err", err)
		os.Exit(1)
	}

	httpServer := &http.Server{
		Addr:              cfg.Server.ListenAddr,
		Handler:           srv.Handler,
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		slog.Info("emby-lite listening",
			"addr", cfg.Server.ListenAddr,
			"database", db.Dialect,
			"cache", cacheStore.BackendName(),
			"mode", "direct-play/302-only",
		)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down emby-lite...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}

func setupLogger(level string) {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv})))
}
