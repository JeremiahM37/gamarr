package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gamarr/internal/api"
	"gamarr/internal/config"
	"gamarr/internal/db"
	"gamarr/internal/download"
	"gamarr/internal/monitor"
	"gamarr/internal/qbit"
	"gamarr/internal/sabnzbd"
	"gamarr/internal/search"
)

var Version = "2.0.0"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	slog.Info("gamarr starting", "version", Version)

	// Load config
	cfg := config.Load()

	// Ensure directories exist
	for _, dir := range []string{cfg.DataDir, cfg.QBSavePath, cfg.GamesVaultPath, cfg.GamesRomsPath} {
		os.MkdirAll(dir, 0755)
	}

	// Initialize database
	database, err := db.New(fmt.Sprintf("%s/gamarr.db", cfg.DataDir))
	if err != nil {
		slog.Error("failed to initialize database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	// Initialize qBittorrent client
	qb := qbit.New(cfg.QBURL, cfg.QBUser, cfg.QBPass)

	// Initialize SABnzbd client (optional)
	var sab *sabnzbd.Client
	if cfg.HasSABnzbd() {
		sab = sabnzbd.New(cfg.SABnzbdURL, cfg.SABnzbdAPIKey)
		slog.Info("SABnzbd configured", "url", cfg.SABnzbdURL)
	}

	// Initialize download manager
	mgr := download.New(cfg, database, qb)

	// Initialize AI monitor
	mon := monitor.New(cfg, monitor.Callbacks{
		GetJobs: func() []struct {
			ID   string
			Data map[string]interface{}
		} {
			items := database.Items()
			result := make([]struct {
				ID   string
				Data map[string]interface{}
			}, len(items))
			for i, item := range items {
				result[i].ID = item.ID
				result[i].Data = item.Data
			}
			return result
		},
		QBReauth:          func() bool { return qb.Login() },
		ClearMyrientCache: search.ClearMyrientCache,
		RunOrphanRecovery: func() { go mgr.RecoverOrphanedTorrents() },
	})
	mon.Start()

	// Recover orphaned torrents and scan library in background
	go mgr.RecoverOrphanedTorrents()
	go mgr.ScanLibraryDirs()

	// Create HTTP router
	router := api.NewRouter(cfg, mgr, mon, sab)

	// Start HTTP server
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		slog.Info("listening", "port", cfg.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	slog.Info("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mon.Stop()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
	slog.Info("shutdown complete")
}
