package scheduler

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gamarr/internal/config"
	"gamarr/internal/db"
	"gamarr/internal/models"
	"gamarr/internal/webhook"
)

func newTestStore(t *testing.T) *db.JobStore {
	t.Helper()
	store, err := db.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func noopSearch(query, platformSlug string) []*models.SearchResult { return nil }

func noopDownload(result *models.SearchResult) (string, error) { return "", nil }

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func TestNewAndStatusDefaults(t *testing.T) {
	cfg := &config.Config{
		SchedulerEnabled:       true,
		SchedulerIntervalHours: 12,
		SchedulerAutoDownload:  true,
		SchedulerMinScore:      80,
	}
	s := New(cfg, newTestStore(t), noopSearch, noopDownload, nil)

	status := s.Status()
	if status["enabled"] != true {
		t.Errorf("enabled = %v, want true", status["enabled"])
	}
	if status["interval_hours"] != 12 {
		t.Errorf("interval_hours = %v, want 12", status["interval_hours"])
	}
	if status["auto_download"] != true {
		t.Errorf("auto_download = %v, want true", status["auto_download"])
	}
	if status["min_score"] != 80 {
		t.Errorf("min_score = %v, want 80", status["min_score"])
	}
	if status["running"] != false {
		t.Errorf("running = %v, want false", status["running"])
	}
	if status["last_results"] != 0 {
		t.Errorf("last_results = %v, want 0", status["last_results"])
	}
	if status["auto_downloads"] != 0 {
		t.Errorf("auto_downloads = %v, want 0", status["auto_downloads"])
	}
}

func TestStartDisabled(t *testing.T) {
	cfg := &config.Config{SchedulerEnabled: false}
	s := New(cfg, newTestStore(t), noopSearch, noopDownload, nil)
	// Must return without launching the loop; nothing to observe beyond not hanging.
	s.Start()
	s.Stop()
}

func TestStartEnabledAndStop(t *testing.T) {
	// Interval 0 exercises the <1h fallback to 24h inside loop().
	cfg := &config.Config{SchedulerEnabled: true, SchedulerIntervalHours: 0}
	s := New(cfg, newTestStore(t), noopSearch, noopDownload, nil)
	s.Start()
	time.Sleep(20 * time.Millisecond) // let the loop goroutine start
	s.Stop()
	// Stop must be idempotent.
	s.Stop()
}

func TestRunEmptyWishlist(t *testing.T) {
	var searches int64
	searchFn := func(q, p string) []*models.SearchResult {
		atomic.AddInt64(&searches, 1)
		return nil
	}
	cfg := &config.Config{SchedulerEnabled: true, SchedulerAutoDownload: true}
	s := New(cfg, newTestStore(t), searchFn, noopDownload, nil)

	s.run()

	if got := atomic.LoadInt64(&searches); got != 0 {
		t.Errorf("searchFn called %d times for empty wishlist, want 0", got)
	}
	status := s.Status()
	if status["last_results"] != 0 {
		t.Errorf("last_results = %v, want 0", status["last_results"])
	}
	zeroTime := time.Time{}.Format(time.RFC3339)
	if status["last_run"] == zeroTime {
		t.Error("last_run was not updated")
	}
}

func TestRunNowAsync(t *testing.T) {
	cfg := &config.Config{}
	s := New(cfg, newTestStore(t), noopSearch, noopDownload, nil)

	before := time.Time{}.Format(time.RFC3339)
	s.RunNow()
	waitFor(t, 3*time.Second, func() bool {
		return s.Status()["last_run"] != before
	})
}

func TestRunAutoDownload(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	if _, err := store.AddWishlistItem("Chrono Trigger", "SNES", "snes"); err != nil {
		t.Fatalf("AddWishlistItem: %v", err)
	}

	// Webhook receiver.
	hit := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case hit <- string(body):
		default:
		}
	}))
	t.Cleanup(srv.Close)

	var searchedQuery, searchedSlug string
	searchFn := func(q, p string) []*models.SearchResult {
		searchedQuery, searchedSlug = q, p
		// Deliberately unsorted: best match is second.
		return []*models.SearchResult{
			{Title: "Chrono Trigger [bad dump]", Platform: "SNES", Score: 40},
			{Title: "Chrono Trigger (USA)", Platform: "SNES", Score: 90},
		}
	}

	var downloaded *models.SearchResult
	downloadFn := func(r *models.SearchResult) (string, error) {
		downloaded = r
		return "job-123", nil
	}

	webhookFn := func() []webhook.WebhookConfig {
		return []webhook.WebhookConfig{{
			Name: "test", URL: srv.URL, Type: "generic", Enabled: true, Events: "*",
		}}
	}

	cfg := &config.Config{
		SchedulerEnabled:      true,
		SchedulerAutoDownload: true,
		SchedulerMinScore:     0, // exercises the default-to-70 fallback
	}
	s := New(cfg, store, searchFn, downloadFn, webhookFn)

	s.run()

	if searchedQuery != "Chrono Trigger" || searchedSlug != "snes" {
		t.Errorf("searched (%q, %q), want (Chrono Trigger, snes)", searchedQuery, searchedSlug)
	}
	if downloaded == nil {
		t.Fatal("downloadFn was not called")
	}
	if downloaded.Title != "Chrono Trigger (USA)" || downloaded.Score != 90 {
		t.Errorf("downloaded %+v, want the highest-scored result", downloaded)
	}

	status := s.Status()
	if status["auto_downloads"] != 1 {
		t.Errorf("auto_downloads = %v, want 1", status["auto_downloads"])
	}
	if status["last_results"] != 2 {
		t.Errorf("last_results = %v, want 2", status["last_results"])
	}
	if status["running"] != false {
		t.Errorf("running = %v after run, want false", status["running"])
	}

	// Item removed from wishlist after successful download.
	if items := store.GetWishlist(); len(items) != 0 {
		t.Errorf("wishlist has %d items after auto-download, want 0", len(items))
	}

	// Webhook fired (async).
	select {
	case body := <-hit:
		if !strings.Contains(body, "scheduler_match") {
			t.Errorf("webhook body missing event: %s", body)
		}
		if !strings.Contains(body, "Chrono Trigger (USA)") {
			t.Errorf("webhook body missing matched title: %s", body)
		}
	case <-time.After(3 * time.Second):
		t.Error("webhook was never delivered")
	}
}

func TestRunScoreBelowThreshold(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	if _, err := store.AddWishlistItem("Obscure Game", "N64", "n64"); err != nil {
		t.Fatalf("AddWishlistItem: %v", err)
	}

	searchFn := func(q, p string) []*models.SearchResult {
		return []*models.SearchResult{{Title: "Obscure Game", Platform: "N64", Score: 90}}
	}
	var downloads int64
	downloadFn := func(r *models.SearchResult) (string, error) {
		atomic.AddInt64(&downloads, 1)
		return "job-x", nil
	}

	cfg := &config.Config{
		SchedulerAutoDownload: true,
		SchedulerMinScore:     95, // above the result's 90
	}
	s := New(cfg, store, searchFn, downloadFn, nil)

	s.run()

	if got := atomic.LoadInt64(&downloads); got != 0 {
		t.Errorf("downloadFn called %d times, want 0 (score below threshold)", got)
	}
	if items := store.GetWishlist(); len(items) != 1 {
		t.Errorf("wishlist has %d items, want 1 (item kept)", len(items))
	}
	status := s.Status()
	if status["last_results"] != 1 {
		t.Errorf("last_results = %v, want 1", status["last_results"])
	}
	if status["auto_downloads"] != 0 {
		t.Errorf("auto_downloads = %v, want 0", status["auto_downloads"])
	}
}

func TestRunDownloadError(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	if _, err := store.AddWishlistItem("Broken Game", "PC", ""); err != nil {
		t.Fatalf("AddWishlistItem: %v", err)
	}

	searchFn := func(q, p string) []*models.SearchResult {
		return []*models.SearchResult{{Title: "Broken Game", Platform: "PC", Score: 99}}
	}
	downloadFn := func(r *models.SearchResult) (string, error) {
		return "", errors.New("no seeders")
	}

	cfg := &config.Config{SchedulerAutoDownload: true, SchedulerMinScore: 70}
	// webhookFn nil covers the nil-webhook branch.
	s := New(cfg, store, searchFn, downloadFn, nil)

	s.run()

	status := s.Status()
	if status["auto_downloads"] != 0 {
		t.Errorf("auto_downloads = %v, want 0 after failed download", status["auto_downloads"])
	}
	if items := store.GetWishlist(); len(items) != 1 {
		t.Errorf("wishlist has %d items, want 1 (kept after failed download)", len(items))
	}
}

func TestRunAbortsAfterStop(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.AddWishlistItem("Some Game", "NES", "nes"); err != nil {
		t.Fatalf("AddWishlistItem: %v", err)
	}

	var searches int64
	searchFn := func(q, p string) []*models.SearchResult {
		atomic.AddInt64(&searches, 1)
		return nil
	}

	cfg := &config.Config{SchedulerAutoDownload: true}
	s := New(cfg, store, searchFn, noopDownload, nil)

	s.Stop() // stopCh closed before run
	s.run()

	if got := atomic.LoadInt64(&searches); got != 0 {
		t.Errorf("searchFn called %d times after Stop, want 0", got)
	}
}

func TestStopInterruptsRateLimitSleep(t *testing.T) {
	store := newTestStore(t)
	for _, title := range []string{"Game One", "Game Two", "Game Three"} {
		if _, err := store.AddWishlistItem(title, "NES", "nes"); err != nil {
			t.Fatalf("AddWishlistItem: %v", err)
		}
	}

	firstSearch := make(chan struct{})
	var searches int64
	searchFn := func(q, p string) []*models.SearchResult {
		if atomic.AddInt64(&searches, 1) == 1 {
			close(firstSearch)
		}
		// Non-empty results (below any threshold) so the iteration reaches
		// the inter-item rate limit rather than an early continue.
		return []*models.SearchResult{{Title: q, Platform: "NES", Score: 10}}
	}

	cfg := &config.Config{SchedulerAutoDownload: true, SchedulerMinScore: 95}
	s := New(cfg, store, searchFn, noopDownload, nil)

	go func() {
		<-firstSearch
		s.Stop()
	}()

	start := time.Now()
	s.run()
	elapsed := time.Since(start)

	// The full rate-limit budget for 3 items is 2 sleeps x 2s = 4s. A stop
	// after the first item must interrupt the pending sleep, returning well
	// under a single 2s sleep.
	if elapsed >= 1500*time.Millisecond {
		t.Errorf("run took %v after Stop, want well under the 2s rate-limit sleep", elapsed)
	}
	if got := atomic.LoadInt64(&searches); got != 1 {
		t.Errorf("searchFn called %d times, want 1 (stopped after first item)", got)
	}
}

func TestRateLimitAppliedOnContinuePaths(t *testing.T) {
	store := newTestStore(t)
	for _, title := range []string{"No Results One", "No Results Two", "No Results Three"} {
		if _, err := store.AddWishlistItem(title, "NES", "nes"); err != nil {
			t.Fatalf("AddWishlistItem: %v", err)
		}
	}

	var searches int64
	// Zero results exercises the continue branch, which previously skipped
	// the inter-item rate limit entirely.
	searchFn := func(q, p string) []*models.SearchResult {
		atomic.AddInt64(&searches, 1)
		return nil
	}

	cfg := &config.Config{SchedulerAutoDownload: true}
	s := New(cfg, store, searchFn, noopDownload, nil)
	s.rateLimit = 80 * time.Millisecond

	start := time.Now()
	s.run()
	elapsed := time.Since(start)

	if got := atomic.LoadInt64(&searches); got != 3 {
		t.Errorf("searchFn called %d times, want 3", got)
	}
	// Two inter-item waits must elapse even though every iteration continues.
	if want := 160 * time.Millisecond; elapsed < want {
		t.Errorf("run took %v, want >= %v (rate limit skipped on continue)", elapsed, want)
	}
}

func TestRunGuardPreventsConcurrentRuns(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.AddWishlistItem("Guarded Game", "GBA", "gba"); err != nil {
		t.Fatalf("AddWishlistItem: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var searches int64
	searchFn := func(q, p string) []*models.SearchResult {
		atomic.AddInt64(&searches, 1)
		once.Do(func() { close(started) })
		<-release
		return nil // zero results: run skips the inter-item sleep
	}

	cfg := &config.Config{SchedulerAutoDownload: false}
	s := New(cfg, store, searchFn, noopDownload, nil)

	firstDone := make(chan struct{})
	go func() {
		s.run()
		close(firstDone)
	}()
	<-started

	if s.Status()["running"] != true {
		t.Error("running should be true while a run is in flight")
	}

	// A second run must bail out immediately while the first holds the guard.
	secondDone := make(chan struct{})
	go func() {
		s.run()
		close(secondDone)
	}()
	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("second run did not return immediately; guard failed")
	}

	close(release)
	select {
	case <-firstDone:
	case <-time.After(3 * time.Second):
		t.Fatal("first run never finished")
	}

	if got := atomic.LoadInt64(&searches); got != 1 {
		t.Errorf("searchFn called %d times, want 1 (second run blocked by guard)", got)
	}
	if s.Status()["running"] != false {
		t.Error("running should be false after run completes")
	}
}

func TestItoa(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{5, "5"},
		{42, "42"},
		{100, "100"},
		{987654, "987654"},
		{-7, "-7"},
		{-120, "-120"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := itoa(tt.in); got != tt.want {
				t.Errorf("itoa(%d) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeTitle(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple", "Super Mario World", "super mario world"},
		{"trims whitespace", "  Zelda  ", "zelda"},
		{"strips usa suffix", "Chrono Trigger (USA)", "chrono trigger"},
		{"strips europe suffix", "F-Zero (Europe)", "fzero"},
		{"strips japan suffix", "Mother 3 (Japan)", "mother 3"},
		{"strips world suffix", "Tetris (World)", "tetris"},
		{"strips bang tag", "Metroid [!]", "metroid"},
		{"drops punctuation", "Pokémon: Red-Blue!", "pokmon redblue"},
		{"keeps digits", "Final Fantasy 7", "final fantasy 7"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeTitle(tt.in); got != tt.want {
				t.Errorf("NormalizeTitle(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
