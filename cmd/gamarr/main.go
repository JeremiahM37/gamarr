package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"gamarr/internal/api"
	"gamarr/internal/config"
	"gamarr/internal/db"
	"gamarr/internal/download"
	"gamarr/internal/models"
	"gamarr/internal/monitor"
	"gamarr/internal/qbit"
	"gamarr/internal/sabnzbd"
	"gamarr/internal/scheduler"
	"gamarr/internal/search"
	"gamarr/internal/webhook"
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

	// Set up webhook notification callback
	mgr.NotifyFunc = func(userID, notifType, title, message string) {
		configs := database.GetEnabledWebhooks()
		// Also add env-configured webhook if set
		if cfg.WebhookURL != "" {
			configs = append(configs, webhook.WebhookConfig{
				Name:    "default",
				URL:     cfg.WebhookURL,
				Type:    cfg.WebhookType,
				Enabled: true,
				Events:  "*",
			})
		}
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
	searchFn := func(query, platformSlug string) []*models.SearchResult {
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
			results := search.SearchMyrient(query, slug)
			mu.Lock()
			allResults = append(allResults, results...)
			mu.Unlock()
		}()
		go func() {
			defer wg.Done()
			results := search.SearchVimm(query, slug)
			mu.Lock()
			allResults = append(allResults, results...)
			mu.Unlock()
		}()
		wg.Wait()
		// Filter and score
		var torrentResults, ddlResults []*models.SearchResult
		for _, r := range allResults {
			if r.SourceType == "torrent" {
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

	downloadFn := func(result *models.SearchResult) (string, error) {
		if result.SourceType == "ddl" {
			jobID := mgr.DownloadDDL(result.DownloadURL, result.VimmID, result.Title, result.Platform, result.PlatformSlug, result.IsPC)
			return jobID, nil
		}
		url := result.DownloadURL
		if url == "" {
			url = result.MagnetURL
		}
		if url == "" && result.InfoHash != "" {
			url = fmt.Sprintf("magnet:?xt=urn:btih:%s", result.InfoHash)
		}
		return mgr.DownloadTorrent(url, result.Title, result.Platform, result.PlatformSlug, result.IsPC)
	}

	webhookFn := func() []webhook.WebhookConfig {
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

	sched := scheduler.New(cfg, database, searchFn, downloadFn, webhookFn)
	sched.Start()

	// Recover orphaned torrents and scan library in background
	go mgr.RecoverOrphanedTorrents()
	go mgr.ScanLibraryDirs()

	// Create HTTP router
	router := api.NewRouter(cfg, mgr, mon, sab, sched)

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

	sched.Stop()
	mon.Stop()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
	slog.Info("shutdown complete")
}
