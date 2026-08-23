package download

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gamarr/internal/db"
	"gamarr/internal/qbit"
	"os"
)

func newTestWatcher(t *testing.T, qm *qbitMock) (*Watcher, *Manager) {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.QBURL = "configured"
	jobs := newTestJobs(t)
	m := New(cfg, jobs, qm.client())
	return NewWatcher(cfg, m), m
}

func TestWatcherStartDisabled(t *testing.T) {
	t.Run("watcher disabled by flag", func(t *testing.T) {
		qm := newQbitMock(t)
		w, _ := newTestWatcher(t, qm)
		w.cfg.WatcherEnabled = false
		w.Start() // must return immediately without panicking
		w.Stop()
	})

	t.Run("no qbittorrent configured", func(t *testing.T) {
		cfg := newTestConfig(t)
		cfg.WatcherEnabled = true
		cfg.QBURL = ""
		m := New(cfg, newTestJobs(t), nil)
		w := NewWatcher(cfg, m)
		w.Start()
		w.Stop()
	})
}

func TestWatcherStartStop(t *testing.T) {
	qm := newQbitMock(t) // empty torrent list
	w, _ := newTestWatcher(t, qm)
	w.cfg.WatcherEnabled = true
	w.cfg.WatcherIntervalS = 0 // clamps to 30s minimum

	w.Start()
	w.Stop()
	w.Stop() // double stop must not panic
}

func TestWatcherCheckCompletedImports(t *testing.T) {
	qm := newQbitMock(t)
	w, m := newTestWatcher(t, qm)

	content := filepath.Join(t.TempDir(), "Auto Game")
	writeFileT(t, filepath.Join(content, "setup.exe"), []byte("x"))
	torrent := qbit.Torrent{
		Name: "Auto Game", Hash: "auto-hash", Progress: 1.0, ContentPath: content,
	}
	qm.setTorrents([]qbit.Torrent{torrent})

	notifyCh := make(chan string, 1)
	m.NotifyFunc = func(userID, notifType, title, message string) {
		notifyCh <- notifType + ":" + title
	}

	w.checkCompleted()

	select {
	case got := <-notifyCh:
		if got != "download_complete:Auto Game" {
			t.Errorf("notify = %q, want download_complete:Auto Game", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for import notification")
	}

	if _, ok := w.imported.Load("auto-hash"); !ok {
		t.Error("hash not marked imported")
	}
	// Defaults to PC when no library hint exists.
	if !pathExists(filepath.Join(w.cfg.GamesVaultPath, "Auto Game", "setup.exe")) {
		t.Error("content not auto-imported into vault")
	}
}

func TestWatcherCheckCompletedSkips(t *testing.T) {
	t.Run("incomplete torrent skipped", func(t *testing.T) {
		qm := newQbitMock(t)
		w, m := newTestWatcher(t, qm)
		qm.setTorrents([]qbit.Torrent{{Name: "Partial", Hash: "p1", Progress: 0.6}})
		w.checkCompleted()
		if n := len(m.Jobs().Items()); n != 0 {
			t.Errorf("jobs created = %d, want 0", n)
		}
	})

	t.Run("already imported hash skipped", func(t *testing.T) {
		qm := newQbitMock(t)
		w, m := newTestWatcher(t, qm)
		qm.setTorrents([]qbit.Torrent{{Name: "Done", Hash: "d1", Progress: 1.0}})
		w.imported.Store("d1", struct{}{})
		w.checkCompleted()
		if n := len(m.Jobs().Items()); n != 0 {
			t.Errorf("jobs created = %d, want 0", n)
		}
	})

	t.Run("currently processing hash skipped", func(t *testing.T) {
		qm := newQbitMock(t)
		w, m := newTestWatcher(t, qm)
		qm.setTorrents([]qbit.Torrent{{Name: "Busy", Hash: "b1", Progress: 1.0}})
		w.processing.Store("b1", struct{}{})
		w.checkCompleted()
		if n := len(m.Jobs().Items()); n != 0 {
			t.Errorf("jobs created = %d, want 0", n)
		}
	})

	t.Run("torrent with existing job skipped", func(t *testing.T) {
		qm := newQbitMock(t)
		w, m := newTestWatcher(t, qm)
		m.Jobs().Set("existing", map[string]interface{}{
			"status": "downloading", "title": "Tracked Game",
		})
		qm.setTorrents([]qbit.Torrent{{Name: "Tracked Game", Hash: "t1", Progress: 1.0}})
		w.checkCompleted()
		if n := len(m.Jobs().Items()); n != 1 {
			t.Errorf("jobs = %d, want only the pre-existing job", n)
		}
	})
}

func TestWatcherHasMatchingJob(t *testing.T) {
	qm := newQbitMock(t)
	w, m := newTestWatcher(t, qm)
	m.Jobs().Set("j1", map[string]interface{}{"title": "Known Game"})

	tests := []struct {
		name    string
		torrent qbit.Torrent
		want    bool
	}{
		{"exact title match", qbit.Torrent{Name: "Known Game"}, true},
		{"different title", qbit.Torrent{Name: "Other Game"}, false},
		// Tracker-renamed torrents must match the same way watchGameTorrent
		// matches, or the watcher double-imports a job's torrent.
		{"tracker rename containing job title", qbit.Torrent{Name: "Known Game [FitGirl Repack] v1.02"}, true},
		{"torrent name contained in job title", qbit.Torrent{Name: "known game"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := w.hasMatchingJob(tt.torrent); got != tt.want {
				t.Errorf("hasMatchingJob = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWatcherImportTorrentPlatformHint(t *testing.T) {
	qm := newQbitMock(t)
	w, m := newTestWatcher(t, qm)

	// Seed the library with a SNES title so the import inherits its platform.
	if _, err := m.Jobs().AddLibraryItem(&db.LibraryItem{
		Title: "Super Metroid", Platform: "SNES", PlatformSlug: "snes",
		IsPC: false, FilePath: "/old/path", Source: "scan", SourceType: "scan",
		SourceID: "seed", Metadata: "{}",
	}); err != nil {
		t.Fatalf("seed library: %v", err)
	}

	content := filepath.Join(t.TempDir(), "Super Metroid")
	writeFileT(t, filepath.Join(content, "rom.sfc"), []byte("rom"))
	torrent := qbit.Torrent{
		Name: "Super Metroid", Hash: "sm-hash", Progress: 1.0, ContentPath: content,
	}

	var notified string
	m.NotifyFunc = func(userID, notifType, title, message string) {
		notified = message
	}

	w.importTorrent(torrent) // synchronous

	if !pathExists(filepath.Join(w.cfg.GamesRomsPath, "snes", "Super Metroid", "rom.sfc")) {
		t.Error("content not imported into snes library dir")
	}
	if _, ok := w.imported.Load("sm-hash"); !ok {
		t.Error("hash not marked imported")
	}
	if !strings.Contains(notified, "SNES") {
		t.Errorf("notification = %q, want SNES platform mentioned", notified)
	}
}

func TestWatcherImportTorrentRemoveAfterImport(t *testing.T) {
	qm := newQbitMock(t)
	w, _ := newTestWatcher(t, qm)
	w.cfg.RemoveAfterImport = true

	content := filepath.Join(t.TempDir(), "Removable Game")
	writeFileT(t, filepath.Join(content, "setup.exe"), []byte("x"))
	torrent := qbit.Torrent{
		Name: "Removable Game", Hash: "rm-hash", Progress: 1.0, ContentPath: content,
	}

	w.importTorrent(torrent)

	// organizeGame deletes once, RemoveAfterImport deletes again.
	if got := qm.deletedHashes(); len(got) < 1 || got[0] != "rm-hash" {
		t.Errorf("deleted hashes = %v, want rm-hash", got)
	}
}

// setImportRetries scopes the retry budget to one test.
func setImportRetries(t *testing.T, attempts int, delay time.Duration) {
	t.Helper()
	a, d := importAttempts, importRetryDelay
	importAttempts, importRetryDelay = attempts, delay
	t.Cleanup(func() { importAttempts, importRetryDelay = a, d })
}

// jobByTitle finds the job importTorrent created for a torrent.
func jobByTitle(t *testing.T, m *Manager, title string) map[string]interface{} {
	t.Helper()
	for _, item := range m.Jobs().Items() {
		if got, _ := item.Data["title"].(string); got == title {
			return item.Data
		}
	}
	t.Fatalf("no job for %q", title)
	return nil
}

// The save-path-plus-name guess is dangerous when it RESOLVES, not when it
// fails: anything already sitting at the torrent's display name gets imported
// in place of the download's own files, which are simply not written yet.
func TestWatcherDoesNotImportALookalikeWhileContentPathIsPending(t *testing.T) {
	setImportRetries(t, 400, 5*time.Millisecond)
	qm := newQbitMock(t)
	w, _ := newTestWatcher(t, qm)

	// Something unrelated already at save_path + name.
	decoy := filepath.Join(w.cfg.QBSavePath, "Some Game")
	writeFileT(t, filepath.Join(decoy, "wrong.exe"), []byte("not this one"))

	// The torrent's own folder, which the client has not finished writing.
	content := filepath.Join(w.cfg.QBSavePath, "Some Game [FitGirl Repack]")
	torrent := qbit.Torrent{
		Name: "Some Game", Hash: "sg-hash", Progress: 1.0,
		SavePath: w.cfg.QBSavePath, ContentPath: content,
	}
	qm.setTorrents([]qbit.Torrent{torrent})

	staged := make(chan error, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		if err := os.MkdirAll(content, 0755); err != nil {
			staged <- err
			return
		}
		staged <- os.WriteFile(filepath.Join(content, "setup.exe"), []byte("installer"), 0644)
	}()

	w.importTorrent(torrent)

	if err := <-staged; err != nil {
		t.Fatalf("stage the late files: %v", err)
	}
	if !pathExists(filepath.Join(w.cfg.GamesVaultPath, "Some Game [FitGirl Repack]", "setup.exe")) {
		t.Error("did not import the torrent's own files")
	}
	if pathExists(filepath.Join(w.cfg.GamesVaultPath, "Some Game", "wrong.exe")) {
		t.Error("imported the lookalike that happened to sit at the torrent's display name")
	}
}

// content_path moves when the client finishes relocating a download, so a retry
// has to ask the client again rather than reuse the value that just failed.
func TestWatcherRetriesAgainstTheClientsCurrentPath(t *testing.T) {
	setImportRetries(t, 400, 5*time.Millisecond)
	qm := newQbitMock(t)
	w, m := newTestWatcher(t, qm)

	moved := filepath.Join(w.cfg.QBSavePath, "complete", "Game")
	writeFileT(t, filepath.Join(moved, "setup.exe"), []byte("installer"))

	// What the watcher was handed, and what the client says now.
	stale := qbit.Torrent{
		Name: "Relocated Game", Hash: "rl-hash", Progress: 1.0,
		SavePath:    w.cfg.QBSavePath,
		ContentPath: filepath.Join(w.cfg.QBSavePath, "incomplete", "Game"),
	}
	current := stale
	current.ContentPath = moved
	qm.setTorrents([]qbit.Torrent{current})

	w.importTorrent(stale)

	if got, _ := jobByTitle(t, m, stale.Name)["status"].(string); got != "completed" {
		t.Errorf("status = %q, want completed", got)
	}
}

// A failed listing is not evidence the client dropped the torrent. Reading it
// as absence turns one bad request into a permanent give-up, which is the same
// failure this exists to fix arriving by another route.
func TestWatcherSpendsAnAttemptWhenTheClientCannotBeRead(t *testing.T) {
	setImportRetries(t, 3, time.Millisecond)
	qm := newQbitMock(t)
	w, m := newTestWatcher(t, qm)

	torrent := qbit.Torrent{
		Name: "Unreadable", Hash: "ur-hash", Progress: 1.0,
		SavePath: w.cfg.QBSavePath, ContentPath: filepath.Join(w.cfg.QBSavePath, "pending"),
	}
	qm.setTorrents([]qbit.Torrent{torrent})
	qm.failInfo()

	w.importTorrent(torrent)

	detail, _ := jobByTitle(t, m, "Unreadable")["detail"].(string)
	if !strings.Contains(detail, "Gave up after 3 attempts") {
		t.Errorf("detail = %q, want the whole budget spent rather than a stop on one failed read", detail)
	}
}

// The budget is what stands between a transient miss and a permanent one, so a
// change to it should surface here rather than in an incident.
func TestImportRetryBudget(t *testing.T) {
	if prodImportAttempts != 20 || prodImportRetryDelay != 30*time.Second {
		t.Errorf("retry budget = %d attempts every %v, want 20 every 30s",
			prodImportAttempts, prodImportRetryDelay)
	}
}

// A restart during the wait has to leave a row the job store recovers on
// startup. Left at error, the row survives verbatim and reads as retrying with
// nothing retrying it.
func TestWatcherMarksAWaitingImportAsInFlight(t *testing.T) {
	setImportRetries(t, 3, 200*time.Millisecond)
	qm := newQbitMock(t)
	w, m := newTestWatcher(t, qm)

	torrent := qbit.Torrent{
		Name: "Waiting Game", Hash: "wg-hash", Progress: 1.0,
		SavePath: w.cfg.QBSavePath, ContentPath: filepath.Join(w.cfg.QBSavePath, "pending"),
	}
	qm.setTorrents([]qbit.Torrent{torrent})

	done := make(chan struct{})
	go func() { defer close(done); w.importTorrent(torrent) }()

	waitFor(t, minPollTimeout, "the waiting job to read as in flight", func() bool {
		for _, item := range m.Jobs().Items() {
			if title, _ := item.Data["title"].(string); title != "Waiting Game" {
				continue
			}
			detail, _ := item.Data["detail"].(string)
			status, _ := item.Data["status"].(string)
			if strings.Contains(detail, "Waiting for the download client") && status == "organizing" {
				return true
			}
		}
		return false
	})
	<-done
}

// A path that never appears has to stop, and stop somewhere a human can see.
// A silently abandoned job is what made the incident need a person.
func TestWatcherStopsRetryingAndSaysSoOnTheJob(t *testing.T) {
	setImportRetries(t, 3, time.Millisecond)
	qm := newQbitMock(t)
	w, m := newTestWatcher(t, qm)

	torrent := qbit.Torrent{
		Name: "Never Lands", Hash: "nl-hash", Progress: 1.0,
		SavePath: w.cfg.QBSavePath, ContentPath: filepath.Join(w.cfg.QBSavePath, "never"),
	}
	qm.setTorrents([]qbit.Torrent{torrent})

	done := make(chan struct{})
	go func() { defer close(done); w.importTorrent(torrent) }()
	select {
	case <-done:
	case <-time.After(minPollTimeout):
		t.Fatal("importTorrent never stopped retrying")
	}

	job := jobByTitle(t, m, torrent.Name)
	if got, _ := job["status"].(string); got != "error" {
		t.Errorf("status = %q, want error", got)
	}
	if detail, _ := job["detail"].(string); !strings.Contains(detail, "Gave up after 3 attempts") {
		t.Errorf("detail = %q, want it to say it gave up and how many attempts it made", detail)
	}
	if _, ok := w.imported.Load("nl-hash"); !ok {
		t.Error("a torrent it gave up on must not be picked up again on the next tick")
	}
}

// A path error that will read the same way forever must stop on the first
// attempt, and must not have its own terminal state overwritten by the
// give-up message: a quarantined download has already had its files deleted.
func TestWatcherStopsOnAPermanentPathError(t *testing.T) {
	setImportRetries(t, 5, time.Millisecond)
	qm := newQbitMock(t)
	w, m := newTestWatcher(t, qm)

	notifyCalled := false
	m.NotifyFunc = func(userID, notifType, title, message string) { notifyCalled = true }

	// A regular file where a directory belongs: ENOTDIR, not ENOENT, so no
	// amount of waiting changes the answer.
	blocker := filepath.Join(t.TempDir(), "blocker")
	writeFileT(t, blocker, []byte("x"))
	torrent := qbit.Torrent{
		Name: "Ghost Game", Hash: "ghost-hash", Progress: 1.0,
		ContentPath: filepath.Join(blocker, "content"),
	}
	qm.setTorrents([]qbit.Torrent{torrent})

	w.importTorrent(torrent)

	if _, ok := w.imported.Load("ghost-hash"); !ok {
		t.Error("failed import should still mark the hash")
	}
	if notifyCalled {
		t.Error("failed import must not send a completion notification")
	}
	job := jobByTitle(t, m, "Ghost Game")
	if status, _ := job["status"].(string); status != "error" {
		t.Errorf("status = %q, want error", status)
	}
	// Assert what should be there as well as what should not: an empty detail
	// satisfies the negative on its own, so a give-up write that blanked the
	// terminal state would slip through it.
	detail, _ := job["detail"].(string)
	if strings.Contains(detail, "Gave up after") {
		t.Errorf("detail = %q; a permanent failure already wrote its own terminal state", detail)
	}
	if detail == "" {
		t.Error("detail was blanked, so whatever the import wrote about this failure is gone")
	}
	if msg, _ := job["error"].(string); !strings.Contains(msg, "not a directory") {
		t.Errorf("error = %q, want the real errno rather than a generic miss", msg)
	}
}
