package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"gamarr/internal/api"
	"gamarr/internal/config"
	"gamarr/internal/db"
	"gamarr/internal/download"
	"gamarr/internal/fileops"
	"gamarr/internal/minerva"
	"gamarr/internal/models"
	"gamarr/internal/monitor"
	"gamarr/internal/qbit"
	"gamarr/internal/sabnzbd"
	"gamarr/internal/scheduler"
	"gamarr/internal/search"
	"gamarr/internal/webhook"
)

// Version is overridden at release time via -ldflags -X main.Version.
// The literal is the fallback for builds from source.
var Version = "1.3.0"

// enabledWebhooks returns the enabled webhook configs stored in the database,
// plus the env-configured default webhook (WEBHOOK_URL) if one is set.
func enabledWebhooks(database *db.JobStore, cfg *config.Config) []webhook.WebhookConfig {
	configs := database.GetEnabledWebhooks()
	if cfg.WebhookURL != "" {
		configs = append(configs, webhook.WebhookConfig{
			Name:    "default",
			URL:     cfg.WebhookURL,
			Type:    cfg.WebhookType,
			Enabled: true,
			Events:  "*",
		})
	}
	return configs
}

// warnIfHardlinkImportCannotWork probes the configured library paths at boot
// when hardlink imports are selected, so a layout that cannot link is reported
// once at startup instead of as a failed job the first time something finishes
// downloading — possibly days later.
func warnIfHardlinkImportCannotWork(cfg *config.Config, mgr *download.Manager) {
	settings := mgr.LoadSettings()
	if settings == nil {
		return
	}
	mode, err := fileops.ParseMode(settings.ImportMode)
	if err != nil || mode != fileops.ModeHardlink {
		return
	}
	for _, dest := range []string{cfg.GamesVaultPath, cfg.GamesRomsPath} {
		if dest == "" {
			continue
		}
		if err := fileops.CheckHardlink(cfg.QBSavePath, dest); err != nil {
			slog.Warn("hardlink import will fail with this layout",
				"downloads", cfg.QBSavePath, "library", dest, "error", err)
		}
	}
}

func newSchedulerSearch(cfg *config.Config, minervaSvc *minerva.Service) func(string, string) []*models.SearchResult {
	return func(query, platformSlug string) []*models.SearchResult {
		var allResults []*models.SearchResult
		var mu sync.Mutex
		var wg sync.WaitGroup
		slug := platformSlug
		if slug == "all" {
			slug = ""
		}
		wg.Add(3)
		go func() {
			defer wg.Done()
			results := search.SearchProwlarr(cfg, query, slug)
			mu.Lock()
			allResults = append(allResults, results...)
			mu.Unlock()
		}()
		go func() {
			defer wg.Done()
			results := search.SearchMyrient(cfg.Sources, query, slug)
			mu.Lock()
			allResults = append(allResults, results...)
			mu.Unlock()
		}()
		go func() {
			defer wg.Done()
			results := search.SearchVimm(cfg.Sources, query, slug)
			mu.Lock()
			allResults = append(allResults, results...)
			mu.Unlock()
		}()
		if minervaSvc != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results := search.SearchMinerva(minervaSvc, query, slug)
				mu.Lock()
				allResults = append(allResults, results...)
				mu.Unlock()
			}()
		}
		wg.Wait()
		// Filter and score
		var torrentResults, ddlResults []*models.SearchResult
		for _, r := range allResults {
			if r.SourceType == "torrent" && r.TorrentFileIndex == nil {
				torrentResults = append(torrentResults, r)
			} else {
				ddlResults = append(ddlResults, r)
			}
		}
		filtered := search.FilterGameResults(torrentResults, query)
		var results []*models.SearchResult
		results = append(results, filtered...)
		results = append(results, ddlResults...)
		if results == nil {
			results = []*models.SearchResult{}
		}
		results = search.ScoreResults(results, query, platformSlug)
		return results
	}
}

func newSchedulerDownload(mgr *download.Manager, sab *sabnzbd.Client) func(*models.SearchResult) (string, error) {
	return func(result *models.SearchResult) (string, error) {
		if result.TorrentFileIndex != nil {
			if strings.TrimSpace(result.DownloadURL) == "" || strings.TrimSpace(result.InfoHash) == "" || strings.TrimSpace(result.TorrentFilePath) == "" {
				return "", fmt.Errorf("Selective torrent requires URL, info hash and file path")
			}
			return mgr.DownloadSelectiveTorrent(result.DownloadURL, result.InfoHash, *result.TorrentFileIndex,
				result.TorrentFilePath, result.TorrentFileSize, result.Title, result.Platform, result.PlatformSlug, result.IsPC)
		}
		if result.SourceType == "ddl" {
			jobID := mgr.DownloadDDL(result.DownloadURL, result.VimmID, result.Title, result.Platform, result.PlatformSlug, result.IsPC)
			return jobID, nil
		}
		if result.DownloadProtocol == "nzb" {
			return mgr.DownloadNZB(sab, result.DownloadURL, result.Title, result.Platform, result.PlatformSlug, result.IsPC)
		}
		url := result.DownloadURL
		if url == "" {
			url = result.MagnetURL
		}
		if url == "" && result.InfoHash != "" {
			url = fmt.Sprintf("magnet:?xt=urn:btih:%s", result.InfoHash)
		}
		return mgr.DownloadTorrent(url, result.InfoHash, result.Title, result.Platform, result.PlatformSlug, result.IsPC)
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	slog.Info("gamarr starting", "version", Version)

	// Load config
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Ensure directories exist. DataDir holds the database, so the binary
	// cannot run without it. The download/library paths default to /data/*
	// volume mounts that only exist in the Docker deployment — downloads and
	// organizing fail loudly later if they're missing, so a warning suffices.
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		slog.Error("failed to create data directory", "dir", cfg.DataDir, "error", err)
		os.Exit(1)
	}
	for _, dir := range []string{cfg.QBSavePath, cfg.GamesVaultPath, cfg.GamesRomsPath} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			slog.Warn("could not create directory (downloads/organizing to it will fail)", "dir", dir, "error", err)
		}
	}

	// Initialize database
	database, err := db.New(fmt.Sprintf("%s/gamarr.db", cfg.DataDir))
	if err != nil {
		slog.Error("failed to initialize database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	// The optional index is opened only when enabled. Initial metadata sync
	// claims its guard now and runs independently of HTTP server startup.
	minervaSvc, stopMinerva, err := startMinerva(ctx, cfg, newSyncTicker)
	if err != nil {
		slog.Error("failed to initialize Minerva index", "error", err)
		os.Exit(1)
	}
	// Runs after HTTP handlers and scheduler searches have drained below.
	defer stopMinerva()

	// Initialize qBittorrent client (API key preferred when set — qB ≥ 5.2).
	var qb *qbit.Client
	if cfg.QBAPIKey != "" {
		qb = qbit.NewWithAPIKey(cfg.QBURL, cfg.QBAPIKey)
		slog.Info("qBittorrent configured with API key")
	} else {
		qb = qbit.New(cfg.QBURL, cfg.QBUser, cfg.QBPass)
	}

	// Initialize SABnzbd client (optional)
	var sab *sabnzbd.Client
	if cfg.HasSABnzbd() {
		sab = sabnzbd.New(cfg.SABnzbdURL, cfg.SABnzbdAPIKey)
		slog.Info("SABnzbd configured", "url", cfg.SABnzbdURL)
	}

	// Initialize download manager
	mgr := download.New(cfg, database, qb)

	warnIfHardlinkImportCannotWork(cfg, mgr)

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
		RetryJob: mgr.RetryJob,
		QBReauth: func() bool {
			return cfg.HasQBittorrent() && qb.Login()
		},
		ClearMyrientCache: search.ClearMyrientCache,
		RunOrphanRecovery: func() { go mgr.RecoverOrphanedTorrents() },
	})
	mon.Start()

	// Set up webhook notification callback
	mgr.NotifyFunc = func(userID, notifType, title, message string) {
		configs := enabledWebhooks(database, cfg)
		if len(configs) > 0 {
			status := "info"
			if notifType == models.NotifTypeDownloadComplete || notifType == models.NotifTypeRequestCompleted {
				status = "completed"
			} else if notifType == models.NotifTypeRequestFailed {
				status = "failed"
			} else if notifType == models.NotifTypeRequestApproved {
				status = "approved"
			}
			webhook.Send(configs, webhook.Payload{
				Event:   notifType,
				Title:   title,
				Status:  status,
				Message: message,
			})
		}
	}

	// Initialize scheduler
	searchFn := newSchedulerSearch(cfg, minervaSvc)
	downloadFn := newSchedulerDownload(mgr, sab)

	webhookFn := func() []webhook.WebhookConfig {
		return enabledWebhooks(database, cfg)
	}

	sched := scheduler.New(cfg, database, searchFn, downloadFn, webhookFn)
	sched.Start()

	// Configure circuit breaker
	search.InitHealthConfig(cfg.CircuitBreakerThreshold, cfg.CircuitBreakerTimeoutS)

	// A partial vault archive is named uniquely per attempt, so one abandoned by
	// a crash is never reclaimed by a later import. Sweep with no age floor,
	// which is only safe here: this runs before any import goroutine starts, so
	// nothing is mid-write rather than merely assumed not to be. A floor would
	// defeat the sweep instead of guarding it, since a container restarts in
	// seconds and the partial it left is that young.
	if cfg.GamesVaultPath != "" {
		fileops.SweepPartials(cfg.GamesVaultPath, 0)
	}

	// Recover orphaned torrents and scan library in background
	if cfg.HasQBittorrent() {
		go mgr.RecoverOrphanedTorrents()
	}
	go mgr.RecoverOrphanedNZBDownloads()
	go mgr.ScanLibraryDirs()

	// Start torrent completion watcher
	watcher := download.NewWatcher(cfg, mgr)
	watcher.Start()

	// Periodic cleanup: remove stale downloading jobs (>24h) and old finished jobs (>7d)
	go func() {
		// Initial cleanup on startup
		database.CleanupStaleDownloads(24)
		database.Cleanup(7)

		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			stale := database.CleanupStaleDownloads(24)
			old := database.Cleanup(7)
			if stale > 0 || old > 0 {
				slog.Info("periodic cleanup", "stale_downloads", stale, "old_jobs", old)
			}
		}
	}()

	// Create HTTP router. The API reports this over /api/health and /api/config,
	// so an image pulled from the registry can say which build it is.
	api.Version = Version
	router := &drainingHandler{next: api.NewRouter(cfg, mgr, mon, sab, sched, minervaSvc)}

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
	<-ctx.Done()
	slog.Info("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	watcher.Stop()
	sched.Stop()
	mon.Stop()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
		_ = server.Close()
	}
	router.Drain()
	stopMinerva()
	slog.Info("shutdown complete")
}
