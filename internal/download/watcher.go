package download

import (
	"log/slog"
	"sync"
	"time"

	"gamarr/internal/config"
	"gamarr/internal/qbit"
)

// Watcher monitors qBittorrent for completed torrents and auto-imports them.
type Watcher struct {
	cfg        *config.Config
	mgr        *Manager
	processing sync.Map // hash → struct{} (currently being processed)
	imported   sync.Map // hash → struct{} (already imported)
	stopCh     chan struct{}
}

// NewWatcher creates a torrent completion watcher.
func NewWatcher(cfg *config.Config, mgr *Manager) *Watcher {
	return &Watcher{
		cfg:    cfg,
		mgr:    mgr,
		stopCh: make(chan struct{}),
	}
}

// Start begins watching for completed torrents.
func (w *Watcher) Start() {
	if !w.cfg.WatcherEnabled || !w.cfg.HasQBittorrent() {
		slog.Info("torrent watcher disabled")
		return
	}

	interval := time.Duration(w.cfg.WatcherIntervalS) * time.Second
	if interval < 10*time.Second {
		interval = 30 * time.Second
	}

	slog.Info("torrent watcher started", "interval", interval, "category", w.cfg.QBCategory)

	// Run immediately on start.
	w.checkCompleted()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				w.checkCompleted()
			case <-w.stopCh:
				slog.Info("torrent watcher stopped")
				return
			}
		}
	}()
}

// Stop signals the watcher to stop.
func (w *Watcher) Stop() {
	select {
	case <-w.stopCh:
		// Already closed.
	default:
		close(w.stopCh)
	}
}

// checkCompleted scans qBittorrent for completed torrents that haven't been imported.
func (w *Watcher) checkCompleted() {
	torrents := w.mgr.QB().GetTorrents(w.cfg.QBCategory)

	for _, t := range torrents {
		if t.Progress < 1.0 {
			continue
		}

		// Skip if already imported or currently processing.
		if _, ok := w.imported.Load(t.Hash); ok {
			continue
		}
		if _, ok := w.processing.Load(t.Hash); ok {
			continue
		}

		// Skip if we already have a tracked job for this torrent.
		if w.hasMatchingJob(t) {
			continue
		}

		w.processing.Store(t.Hash, struct{}{})
		go w.importTorrent(t)
	}
}

// hasMatchingJob checks if there's already a job tracking this torrent.
func (w *Watcher) hasMatchingJob(t qbit.Torrent) bool {
	for _, item := range w.mgr.Jobs().Items() {
		title, _ := item.Data["title"].(string)
		if title == t.Name {
			return true
		}
	}
	return false
}

// importTorrent auto-imports a completed torrent into the library.
func (w *Watcher) importTorrent(t qbit.Torrent) {
	defer w.processing.Delete(t.Hash)

	slog.Info("watcher: auto-importing completed torrent", "name", t.Name, "hash", t.Hash)

	// Detect platform from existing library items or default to PC.
	platf := "PC"
	platSlug := "pc"
	isPC := true

	// Check if there's a library item with a matching title for platform hints.
	if existing := w.mgr.Jobs().FindLibraryByTitle(t.Name, ""); existing != nil {
		platf = existing.Platform
		platSlug = existing.PlatformSlug
		isPC = existing.IsPC
	}

	// Create a job and organize.
	jobID := newJobID()
	w.mgr.Jobs().Set(jobID, map[string]interface{}{
		"status":        "organizing",
		"title":         t.Name,
		"platform":      platf,
		"platform_slug": platSlug,
		"is_pc":         isPC,
		"error":         nil,
		"detail":        "Auto-importing completed torrent...",
	})

	w.mgr.organizeWithScan(jobID, &t, platf, platSlug, isPC)

	// Check if organize succeeded.
	job, ok := w.mgr.Jobs().Get(jobID)
	if ok {
		status, _ := job["status"].(string)
		if status == "completed" {
			w.imported.Store(t.Hash, struct{}{})
			slog.Info("watcher: auto-import completed", "name", t.Name)

			// Optionally remove the torrent after import.
			if w.cfg.RemoveAfterImport {
				w.mgr.QB().DeleteTorrent(t.Hash, false)
				slog.Info("watcher: removed torrent after import", "name", t.Name)
			}

			// Send notification.
			if w.mgr.NotifyFunc != nil {
				w.mgr.NotifyFunc("", "download_complete", t.Name,
					"Auto-imported by watcher: "+t.Name+" ("+platf+")")
			}
		} else {
			// Mark as imported to prevent retry loops on persistent errors.
			w.imported.Store(t.Hash, struct{}{})
			slog.Warn("watcher: auto-import did not complete, skipping future retries", "name", t.Name, "status", status)
		}
	}
}
