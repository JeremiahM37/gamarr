// Package scheduler runs periodic wishlist searches, auto-grabbing matching
// releases and notifying webhooks on results.
package scheduler

import (
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"gamarr/internal/config"
	"gamarr/internal/db"
	"gamarr/internal/models"
	"gamarr/internal/webhook"
)

// SearchFunc searches all sources for a query and platform, returning scored results.
type SearchFunc func(query, platformSlug string) []*models.SearchResult

// DownloadFunc initiates a download and returns a job ID.
type DownloadFunc func(result *models.SearchResult) (string, error)

// WebhookFunc returns enabled webhook configs for sending notifications.
type WebhookFunc func() []webhook.WebhookConfig

// Scheduler runs periodic wishlist searches.
type Scheduler struct {
	mu            sync.RWMutex
	cfg           *config.Config
	jobs          *db.JobStore
	searchFn      SearchFunc
	downloadFn    DownloadFunc
	webhookFn     WebhookFunc
	running       bool
	lastRun       time.Time
	lastResults   int
	autoDownloads int
	stopCh        chan struct{}
	rateLimit     time.Duration // wait between wishlist searches
}

// New creates a new Scheduler.
func New(cfg *config.Config, jobs *db.JobStore, searchFn SearchFunc, downloadFn DownloadFunc, webhookFn WebhookFunc) *Scheduler {
	return &Scheduler{
		cfg:        cfg,
		jobs:       jobs,
		searchFn:   searchFn,
		downloadFn: downloadFn,
		webhookFn:  webhookFn,
		stopCh:     make(chan struct{}),
		rateLimit:  2 * time.Second,
	}
}

// Start begins the scheduler loop if enabled.
func (s *Scheduler) Start() {
	if !s.cfg.SchedulerEnabled {
		slog.Info("scheduler disabled")
		return
	}
	go s.loop()
	slog.Info("scheduler started", "interval_hours", s.cfg.SchedulerIntervalHours)
}

// Stop halts the scheduler.
func (s *Scheduler) Stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
}

// RunNow triggers an immediate search cycle.
func (s *Scheduler) RunNow() {
	go s.run()
}

// Status returns the current scheduler status.
func (s *Scheduler) Status() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]interface{}{
		"enabled":        s.cfg.SchedulerEnabled,
		"interval_hours": s.cfg.SchedulerIntervalHours,
		"auto_download":  s.cfg.SchedulerAutoDownload,
		"min_score":      s.cfg.SchedulerMinScore,
		"running":        s.running,
		"last_run":       s.lastRun.Format(time.RFC3339),
		"last_results":   s.lastResults,
		"auto_downloads": s.autoDownloads,
	}
}

func (s *Scheduler) loop() {
	interval := time.Duration(s.cfg.SchedulerIntervalHours) * time.Hour
	if interval < time.Hour {
		interval = 24 * time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.run()
		case <-s.stopCh:
			return
		}
	}
}

func (s *Scheduler) run() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	slog.Info("scheduler: starting wishlist search")

	wishlist := s.jobs.GetWishlist()
	if len(wishlist) == 0 {
		slog.Info("scheduler: wishlist empty, nothing to do")
		s.mu.Lock()
		s.lastRun = time.Now()
		s.lastResults = 0
		s.mu.Unlock()
		return
	}

	totalResults := 0
	autoDownloads := 0
	minScore := s.cfg.SchedulerMinScore
	if minScore <= 0 {
		minScore = 70
	}

	for i, item := range wishlist {
		// Check for stop signal between items, and rate-limit before every
		// item after the first. Waiting at the top of the loop (rather than
		// sleeping at the bottom) applies the rate limit on every iteration,
		// including ones that end early via continue, and a Stop() call
		// interrupts the wait immediately.
		if i == 0 {
			select {
			case <-s.stopCh:
				return
			default:
			}
		} else {
			select {
			case <-s.stopCh:
				return
			case <-time.After(s.rateLimit):
			}
		}

		results := s.searchFn(item.Title, item.PlatformSlug)
		if len(results) == 0 {
			continue
		}

		// Sort by score descending
		sort.SliceStable(results, func(i, j int) bool {
			return results[i].Score > results[j].Score
		})

		totalResults += len(results)

		// Auto-download best match if score is high enough
		if s.cfg.SchedulerAutoDownload && len(results) > 0 && results[0].Score >= minScore {
			best := results[0]
			slog.Info("scheduler: auto-downloading", "title", best.Title, "score", best.Score,
				"wishlist_title", item.Title, "platform", best.Platform)

			jobID, err := s.downloadFn(best)
			if err != nil {
				slog.Warn("scheduler: download failed", "title", best.Title, "error", err)
				continue
			}

			autoDownloads++

			// Log activity
			s.jobs.LogActivity("scheduler_download", item.Title,
				"Auto-downloaded from wishlist search: "+best.Title, jobID, nil)

			// Send webhook
			if s.webhookFn != nil {
				configs := s.webhookFn()
				webhook.Send(configs, webhook.Payload{
					Event:    webhook.EventSchedulerMatch,
					Title:    item.Title,
					Platform: best.Platform,
					Status:   "downloading",
					Message:  "Scheduler found match: " + best.Title + " (score: " + itoa(best.Score) + ")",
				})
			}

			// Remove from wishlist after successful download
			s.jobs.DeleteWishlistItem(item.ID)
		}
	}

	s.mu.Lock()
	s.lastRun = time.Now()
	s.lastResults = totalResults
	s.autoDownloads += autoDownloads
	s.mu.Unlock()

	slog.Info("scheduler: completed", "wishlist_items", len(wishlist),
		"results", totalResults, "auto_downloads", autoDownloads)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

// NormalizeTitle returns a lowered, trimmed title for comparison.
func NormalizeTitle(title string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	// Remove common suffixes/prefixes
	for _, suffix := range []string{" (usa)", " (world)", " (europe)", " (japan)", " [!]"} {
		t = strings.TrimSuffix(t, suffix)
	}
	// Remove non-alphanumeric for comparison
	var b strings.Builder
	for _, r := range t {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == ' ' {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
