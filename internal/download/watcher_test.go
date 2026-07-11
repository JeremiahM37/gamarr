package download

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gamarr/internal/db"
	"gamarr/internal/qbit"
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

func TestWatcherImportTorrentFailure(t *testing.T) {
	qm := newQbitMock(t)
	w, m := newTestWatcher(t, qm)

	notifyCalled := false
	m.NotifyFunc = func(userID, notifType, title, message string) { notifyCalled = true }

	torrent := qbit.Torrent{
		Name: "Ghost Game", Hash: "ghost-hash", Progress: 1.0,
		ContentPath: "/no/such/content",
	}
	w.importTorrent(torrent)

	// Failed imports are still marked to avoid retry loops.
	if _, ok := w.imported.Load("ghost-hash"); !ok {
		t.Error("failed import should still mark the hash")
	}
	if notifyCalled {
		t.Error("failed import must not send a completion notification")
	}

	// The job it created should be in error state.
	var errJob bool
	for _, item := range m.Jobs().Items() {
		if title, _ := item.Data["title"].(string); title == "Ghost Game" {
			if status, _ := item.Data["status"].(string); status == "error" {
				errJob = true
			}
		}
	}
	if !errJob {
		t.Error("no error job recorded for failed import")
	}
}
