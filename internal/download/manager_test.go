package download

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"gamarr/internal/qbit"
)

func TestNewJobID(t *testing.T) {
	seen := map[string]bool{}
	hexRe := regexp.MustCompile(`^[0-9a-f]{8}$`)
	for i := 0; i < 100; i++ {
		id := newJobID()
		if !hexRe.MatchString(id) {
			t.Fatalf("newJobID() = %q, want 8 hex chars", id)
		}
		if seen[id] {
			t.Fatalf("duplicate job ID %q", id)
		}
		seen[id] = true
	}
}

func TestNewManager(t *testing.T) {
	t.Run("no optional clients", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		m := New(cfg, jobs, nil)
		if m.Transmission() != nil {
			t.Error("Transmission client should be nil when not configured")
		}
		if m.Deluge() != nil {
			t.Error("Deluge client should be nil when not configured")
		}
		if m.Jobs() != jobs {
			t.Error("Jobs() should return the injected store")
		}
	})

	t.Run("all clients configured", func(t *testing.T) {
		cfg := newTestConfig(t)
		cfg.TransmissionURL = "http://127.0.0.1:1"
		cfg.DelugeURL = "http://127.0.0.1:1"
		qb := qbit.New("http://127.0.0.1:1", "u", "p")
		m := New(cfg, newTestJobs(t), qb)
		if m.Transmission() == nil {
			t.Error("Transmission client should be initialized")
		}
		if m.Deluge() == nil {
			t.Error("Deluge client should be initialized")
		}
		if m.QB() != qb {
			t.Error("QB() should return the injected client")
		}
	})
}

func TestDownloadTorrentValidation(t *testing.T) {
	cfg := newTestConfig(t)
	m := New(cfg, newTestJobs(t), nil)
	if _, err := m.DownloadTorrent("", "Title", "PC", "", true); err == nil {
		t.Fatal("empty URL should return an error")
	}
}

func TestDownloadTorrentNoClientAvailable(t *testing.T) {
	// No qBittorrent, Transmission, or Deluge configured: the job errors.
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)

	jobID, err := m.DownloadTorrent("magnet:x", "Some Game", "PC", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	job, ok := jobs.Get(jobID)
	if !ok {
		t.Fatal("job not created")
	}
	if status, _ := job["status"].(string); status != "error" {
		t.Errorf("status = %q, want error", status)
	}
	if errMsg, _ := job["error"].(string); !strings.Contains(errMsg, "any download client") {
		t.Errorf("error = %q, want failed-to-add message", errMsg)
	}
}

func TestDownloadTorrentQBitFullFlow(t *testing.T) {
	// End-to-end: add via qBittorrent, file-list scan passes, ClamAV
	// unavailable (skipped), content organized into the ROM library,
	// torrent deleted.
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	cfg.QBURL = "configured"

	content := filepath.Join(t.TempDir(), "Super Game (USA)")
	writeFileT(t, filepath.Join(content, "game.sfc"), []byte("rom-data"))

	qm := newQbitMock(t)
	qm.setFiles([]qbit.TorrentFile{{Name: "Super Game (USA)/game.sfc"}})
	qm.setTorrents([]qbit.Torrent{{
		Name:        "Super Game (USA)",
		Hash:        "hash-full-flow",
		Progress:    1.0,
		ContentPath: content,
	}})

	m := New(cfg, jobs, qm.client())
	jobID, err := m.DownloadTorrent("magnet:x", "Super Game (USA)", "SNES", "snes", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	waitFor(t, 10*time.Second, "torrent deletion after organize", func() bool {
		return len(qm.deletedHashes()) > 0
	})

	job := waitJobStatus(t, jobs, jobID, "completed", 5*time.Second)
	if detail, _ := job["detail"].(string); !strings.Contains(detail, "RomM (SNES)") {
		t.Errorf("detail = %q, want RomM (SNES)", detail)
	}

	dest := filepath.Join(cfg.GamesRomsPath, "snes", "Super Game (USA)")
	if !pathExists(filepath.Join(dest, "game.sfc")) {
		t.Errorf("game file not moved to %s", dest)
	}
	if !pathExists(filepath.Join(dest, ".gamarr.json")) {
		t.Error("metadata sidecar not written")
	}
	if pathExists(content) {
		t.Error("source content should be removed after move")
	}
	if !jobs.LibraryHasSourceID("torrent:hash-full-flow") {
		t.Error("library item not tracked")
	}
	if got := qm.deletedHashes(); got[0] != "hash-full-flow" {
		t.Errorf("deleted hash = %q, want hash-full-flow", got[0])
	}
}

func TestDownloadTorrentBlocksDangerousFiles(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	cfg.QBURL = "configured"

	qm := newQbitMock(t)
	qm.setFiles([]qbit.TorrentFile{{Name: "Game/keygen.bat"}, {Name: "Game/setup.scr"}})
	qm.setTorrents([]qbit.Torrent{{
		Name:     "Evil Game",
		Hash:     "hash-evil",
		Progress: 0.5, // metadata available, still downloading
	}})

	m := New(cfg, jobs, qm.client())
	jobID, err := m.DownloadTorrent("magnet:x", "Evil Game", "PC", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	waitFor(t, 10*time.Second, "dangerous torrent deletion", func() bool {
		return len(qm.deletedHashes()) > 0
	})
	job := waitJobStatus(t, jobs, jobID, "error", 5*time.Second)
	if errMsg, _ := job["error"].(string); !strings.Contains(errMsg, "Blocked") {
		t.Errorf("error = %q, want Blocked message", errMsg)
	}
}

func TestDownloadTorrentFallbacks(t *testing.T) {
	newFallbackQbit := func(t *testing.T) *qbitMock {
		qm := newQbitMock(t)
		qm.mu.Lock()
		qm.addOK = false // qBittorrent add always fails
		qm.mu.Unlock()
		return qm
	}

	t.Run("transmission used when qbit fails", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		cfg.QBURL = "configured"
		qm := newFallbackQbit(t)

		trSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"result":"success","arguments":{"torrent-added":{"id":1}}}`)
		}))
		defer trSrv.Close()
		cfg.TransmissionURL = trSrv.URL

		m := New(cfg, jobs, qm.client())
		jobID, err := m.DownloadTorrent("magnet:x", "Fallback Game", "PC", "", true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		job, _ := jobFromDB(t, jobs, jobID)
		if detail, _ := job["detail"].(string); !strings.Contains(detail, "Transmission") {
			t.Errorf("detail = %q, want Transmission", detail)
		}
	})

	t.Run("deluge used when qbit and transmission fail", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		cfg.QBURL = "configured"
		qm := newFallbackQbit(t)

		trSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"result":"duplicate torrent"}`)
		}))
		defer trSrv.Close()
		cfg.TransmissionURL = trSrv.URL

		dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"id":1,"result":"deluge-hash","error":null}`)
		}))
		defer dlSrv.Close()
		cfg.DelugeURL = dlSrv.URL

		m := New(cfg, jobs, qm.client())
		jobID, err := m.DownloadTorrent("magnet:x", "Fallback Game 2", "PC", "", true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		job, _ := jobFromDB(t, jobs, jobID)
		if detail, _ := job["detail"].(string); !strings.Contains(detail, "Deluge") {
			t.Errorf("detail = %q, want Deluge", detail)
		}
	})

	t.Run("all clients fail", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		cfg.QBURL = "configured"
		qm := newFallbackQbit(t)

		deadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		deadSrv.Close()
		cfg.TransmissionURL = deadSrv.URL
		cfg.DelugeURL = deadSrv.URL

		m := New(cfg, jobs, qm.client())
		jobID, err := m.DownloadTorrent("magnet:x", "Doomed Game", "PC", "", true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		job, _ := jobs.Get(jobID)
		if status, _ := job["status"].(string); status != "error" {
			t.Errorf("status = %q, want error", status)
		}
	})
}

func TestOrganizeTorrent(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		cfg := newTestConfig(t)
		qm := newQbitMock(t)
		m := New(cfg, newTestJobs(t), qm.client())
		if _, err := m.OrganizeTorrent("missing-hash", "PC", "", true); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("err = %v, want not found", err)
		}
	})

	t.Run("not complete", func(t *testing.T) {
		cfg := newTestConfig(t)
		qm := newQbitMock(t)
		qm.setTorrents([]qbit.Torrent{{Name: "G", Hash: "h1", Progress: 0.4}})
		m := New(cfg, newTestJobs(t), qm.client())
		if _, err := m.OrganizeTorrent("h1", "PC", "", true); err == nil || !strings.Contains(err.Error(), "not yet complete") {
			t.Fatalf("err = %v, want not yet complete", err)
		}
	})

	t.Run("organizes completed PC torrent", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		content := filepath.Join(t.TempDir(), "Cool.Game-FitGirl")
		writeFileT(t, filepath.Join(content, "setup.exe"), []byte("installer"))

		qm := newQbitMock(t)
		qm.setTorrents([]qbit.Torrent{{
			Name: "Cool.Game-FitGirl", Hash: "h2", Progress: 1.0, ContentPath: content,
		}})
		m := New(cfg, jobs, qm.client())

		jobID, err := m.OrganizeTorrent("h2", "PC", "", true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		waitFor(t, 10*time.Second, "torrent deletion", func() bool {
			return len(qm.deletedHashes()) > 0
		})
		job := waitJobStatus(t, jobs, jobID, "completed", 5*time.Second)
		if detail, _ := job["detail"].(string); !strings.Contains(detail, "GameVault") {
			t.Errorf("detail = %q, want GameVault", detail)
		}
		if !pathExists(filepath.Join(cfg.GamesVaultPath, "Cool.Game-FitGirl", "setup.exe")) {
			t.Error("game not moved to vault")
		}
	})
}

// setupOrganizeJob creates a manager plus a pre-seeded organizing job.
func setupOrganizeJob(t *testing.T) (*Manager, *qbitMock, string) {
	t.Helper()
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	m := New(cfg, jobs, qm.client())
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{
		"status": "organizing", "title": "T", "error": nil, "detail": "",
	})
	return m, qm, jobID
}

func TestOrganizeGame(t *testing.T) {
	t.Run("missing content path errors", func(t *testing.T) {
		m, _, jobID := setupOrganizeJob(t)
		torrent := &qbit.Torrent{Name: "Ghost", Hash: "gh", ContentPath: "/nonexistent/nope"}
		m.organizeGame(jobID, torrent, "PC", "", true)
		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "error" {
			t.Errorf("status = %q, want error", status)
		}
	})

	t.Run("unknown platform left in staging", func(t *testing.T) {
		m, qm, jobID := setupOrganizeJob(t)
		content := filepath.Join(t.TempDir(), "mysterious-thing")
		writeFileT(t, filepath.Join(content, "data.dat"), []byte("???"))
		torrent := &qbit.Torrent{Name: "mysterious-thing", Hash: "mh", ContentPath: content}

		m.organizeGame(jobID, torrent, "", "", false)

		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "completed" {
			t.Errorf("status = %q, want completed", status)
		}
		if detail, _ := job["detail"].(string); !strings.Contains(detail, "unknown platform") {
			t.Errorf("detail = %q, want unknown platform", detail)
		}
		if !pathExists(content) {
			t.Error("content should stay in staging")
		}
		if len(qm.deletedHashes()) != 0 {
			t.Error("torrent must not be deleted for unknown platform")
		}
	})

	t.Run("platform detected from file extension", func(t *testing.T) {
		m, _, jobID := setupOrganizeJob(t)
		content := filepath.Join(t.TempDir(), "handheld-game")
		writeFileT(t, filepath.Join(content, "game.gba"), []byte("gba-rom"))
		torrent := &qbit.Torrent{Name: "handheld-game", Hash: "dh", ContentPath: content}

		m.organizeGame(jobID, torrent, "", "", false)

		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "completed" {
			t.Fatalf("status = %q, want completed (job=%v)", status, job)
		}
		if slug, _ := job["platform_slug"].(string); slug != "gba" {
			t.Errorf("platform_slug = %q, want gba", slug)
		}
		if !pathExists(filepath.Join(m.cfg.GamesRomsPath, "gba", "handheld-game", "game.gba")) {
			t.Error("ROM not moved to gba library dir")
		}
	})

	t.Run("platform detected from metadata.json", func(t *testing.T) {
		m, _, jobID := setupOrganizeJob(t)
		content := filepath.Join(t.TempDir(), "meta-game")
		writeFileT(t, filepath.Join(content, "metadata.json"), []byte(`{"platform":"snes"}`))
		writeFileT(t, filepath.Join(content, "game.bin2"), []byte("rom"))
		torrent := &qbit.Torrent{Name: "meta-game", Hash: "mm", ContentPath: content}

		m.organizeGame(jobID, torrent, "", "", false)

		job, _ := m.Jobs().Get(jobID)
		if slug, _ := job["platform_slug"].(string); slug != "snes" {
			t.Errorf("platform_slug = %q, want snes", slug)
		}
	})

	t.Run("falls back to save path when content path empty", func(t *testing.T) {
		m, _, jobID := setupOrganizeJob(t)
		savePath := t.TempDir()
		writeFileT(t, filepath.Join(savePath, "SavedGame", "rom.sfc"), []byte("rom"))
		torrent := &qbit.Torrent{Name: "SavedGame", Hash: "sp", SavePath: savePath}

		m.organizeGame(jobID, torrent, "SNES", "snes", false)

		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "completed" {
			t.Errorf("status = %q, want completed", status)
		}
		if !pathExists(filepath.Join(m.cfg.GamesRomsPath, "snes", "SavedGame", "rom.sfc")) {
			t.Error("ROM not moved from save path")
		}
	})

	t.Run("move failure sets error", func(t *testing.T) {
		m, _, jobID := setupOrganizeJob(t)
		// Make the vault path a regular file so MkdirAll/copy fails.
		os.RemoveAll(m.cfg.GamesVaultPath)
		writeFileT(t, m.cfg.GamesVaultPath, []byte("not a dir"))

		content := filepath.Join(t.TempDir(), "Blocked.Game-CODEX")
		writeFileT(t, filepath.Join(content, "setup.exe"), []byte("x"))
		torrent := &qbit.Torrent{Name: "Blocked.Game-CODEX", Hash: "bf", ContentPath: content}

		m.organizeGame(jobID, torrent, "PC", "", true)

		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "error" {
			t.Errorf("status = %q, want error", status)
		}
		if errMsg, _ := job["error"].(string); !strings.Contains(errMsg, "Organize failed") {
			t.Errorf("error = %q, want Organize failed", errMsg)
		}
	})
}

func TestDownloadDDL(t *testing.T) {
	t.Run("full flow with content-disposition filename", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Disposition", `attachment; filename="Mario World.sfc"`)
			w.Write([]byte("rom-bytes"))
		}))
		defer srv.Close()

		m := New(cfg, jobs, nil)
		jobID := m.DownloadDDL(srv.URL+"/dl", "", "Mario World", "SNES", "snes", false)

		waitFor(t, 10*time.Second, "library tracking", func() bool {
			return jobs.LibraryHasSourceID("ddl:" + filepath.Join(cfg.GamesRomsPath, "snes", "Mario World.sfc"))
		})
		job := waitJobStatus(t, jobs, jobID, "completed", 5*time.Second)
		if detail, _ := job["detail"].(string); !strings.Contains(detail, "RomM (SNES)") {
			t.Errorf("detail = %q, want RomM (SNES)", detail)
		}
		dest := filepath.Join(cfg.GamesRomsPath, "snes", "Mario World.sfc")
		data, err := os.ReadFile(dest)
		if err != nil || string(data) != "rom-bytes" {
			t.Errorf("dest content = %q err=%v, want rom-bytes", data, err)
		}
		if !pathExists(dest + ".gamarr.json") {
			t.Error("sidecar not written for file dest")
		}
	})

	t.Run("http error fails the job", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		m := New(cfg, jobs, nil)
		jobID := m.DownloadDDL(srv.URL+"/gone", "", "Missing Game", "PC", "", true)
		job := waitJobStatus(t, jobs, jobID, "error", 5*time.Second)
		if errMsg, _ := job["error"].(string); errMsg != "Download failed" {
			t.Errorf("error = %q, want Download failed", errMsg)
		}
	})

	t.Run("no url and no vimm id fails", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		m := New(cfg, jobs, nil)
		jobID := m.DownloadDDL("", "", "Nothing", "PC", "", true)
		waitJobStatus(t, jobs, jobID, "error", 5*time.Second)
	})
}

func TestDownloadDDLFilenameFromURL(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data")) // no Content-Disposition
	}))
	defer srv.Close()

	m := New(cfg, jobs, nil)
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	got := m.downloadDDL(srv.URL+"/files/zelda.gba?token=1", cfg.QBSavePath, jobID)
	if filepath.Base(got) != "zelda.gba" {
		t.Errorf("filename = %q, want zelda.gba", filepath.Base(got))
	}
	if !pathExists(got) {
		t.Error("downloaded file missing")
	}
}

func TestOrganizeDDLFile(t *testing.T) {
	newFixture := func(t *testing.T) (*Manager, string, string) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		m := New(cfg, jobs, nil)
		jobID := newJobID()
		jobs.Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})
		src := filepath.Join(t.TempDir(), "game-file.bin")
		writeFileT(t, src, []byte("payload"))
		return m, jobID, src
	}

	t.Run("pc file goes to vault", func(t *testing.T) {
		m, jobID, src := newFixture(t)
		m.organizeDDLFile(jobID, src, "Great Game", "PC", "", true)
		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "completed" {
			t.Fatalf("status = %q, want completed", status)
		}
		if !pathExists(filepath.Join(m.cfg.GamesVaultPath, "game-file.bin")) {
			t.Error("file not moved to vault")
		}
		if !m.Jobs().LibraryHasSourceID("ddl:" + filepath.Join(m.cfg.GamesVaultPath, "game-file.bin")) {
			t.Error("library item not tracked")
		}
	})

	t.Run("rom goes to platform dir", func(t *testing.T) {
		m, jobID, src := newFixture(t)
		m.organizeDDLFile(jobID, src, "Great Game", "PSP", "psp", false)
		if !pathExists(filepath.Join(m.cfg.GamesRomsPath, "psp", "game-file.bin")) {
			t.Error("file not moved to psp dir")
		}
	})

	t.Run("unknown platform left in staging", func(t *testing.T) {
		m, jobID, src := newFixture(t)
		m.organizeDDLFile(jobID, src, "Great Game", "", "", false)
		job, _ := m.Jobs().Get(jobID)
		if detail, _ := job["detail"].(string); !strings.Contains(detail, "unknown platform") {
			t.Errorf("detail = %q, want unknown platform", detail)
		}
		if !pathExists(src) {
			t.Error("file should remain in place")
		}
	})

	t.Run("move failure sets error", func(t *testing.T) {
		m, jobID, src := newFixture(t)
		os.RemoveAll(m.cfg.GamesVaultPath) // vault dir gone: os.Create fails
		m.organizeDDLFile(jobID, src, "Great Game", "PC", "", true)
		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "error" {
			t.Errorf("status = %q, want error", status)
		}
	})
}

func TestRecoverOrphanedTorrents(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	// All torrents complete so no watch goroutines are spawned.
	qm.setTorrents([]qbit.Torrent{
		{Name: "Awesome.Game.v1.2-FitGirl", Hash: "h1", Progress: 1.0},
		{Name: "Zelda Collection wii pack", Hash: "h2", Progress: 1.0},
		{Name: "Totally Mysterious Thing", Hash: "h3", Progress: 1.0},
	})

	m := New(cfg, jobs, qm.client())
	m.RecoverOrphanedTorrents()

	byTitle := map[string]map[string]interface{}{}
	for _, item := range jobs.Items() {
		title, _ := item.Data["title"].(string)
		byTitle[title] = item.Data
	}
	if len(byTitle) != 3 {
		t.Fatalf("recovered %d jobs, want 3", len(byTitle))
	}

	tests := []struct {
		title    string
		platform string
		isPC     bool
	}{
		{"Awesome.Game.v1.2-FitGirl", "PC", true},
		{"Zelda Collection wii pack", "Wii", false},
		{"Totally Mysterious Thing", "Unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			job, ok := byTitle[tt.title]
			if !ok {
				t.Fatalf("no job recovered for %q", tt.title)
			}
			if status, _ := job["status"].(string); status != "completed_unorganized" {
				t.Errorf("status = %q, want completed_unorganized", status)
			}
			if platf, _ := job["platform"].(string); platf != tt.platform {
				t.Errorf("platform = %q, want %q", platf, tt.platform)
			}
			if isPC, _ := job["is_pc"].(bool); isPC != tt.isPC {
				t.Errorf("is_pc = %v, want %v", isPC, tt.isPC)
			}
		})
	}
}

func TestMoveHelpers(t *testing.T) {
	t.Run("moveFile renames", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "a.txt")
		dest := filepath.Join(dir, "b.txt")
		writeFileT(t, src, []byte("hello"))
		if err := moveFile(src, dest); err != nil {
			t.Fatalf("moveFile: %v", err)
		}
		if pathExists(src) || !pathExists(dest) {
			t.Error("moveFile did not move the file")
		}
	})

	t.Run("moveFile missing source", func(t *testing.T) {
		dir := t.TempDir()
		if err := moveFile(filepath.Join(dir, "nope"), filepath.Join(dir, "out")); err == nil {
			t.Error("want error for missing source")
		}
	})

	t.Run("moveContent directory", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "gamedir")
		writeFileT(t, filepath.Join(src, "sub", "file.bin"), []byte("data"))
		dest := filepath.Join(dir, "moved")
		if err := moveContent(src, dest); err != nil {
			t.Fatalf("moveContent: %v", err)
		}
		if pathExists(src) {
			t.Error("source dir should be removed")
		}
		data, err := os.ReadFile(filepath.Join(dest, "sub", "file.bin"))
		if err != nil || string(data) != "data" {
			t.Errorf("moved content = %q err=%v", data, err)
		}
	})

	t.Run("moveContent single file", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "single.rom")
		writeFileT(t, src, []byte("x"))
		dest := filepath.Join(dir, "single-moved.rom")
		if err := moveContent(src, dest); err != nil {
			t.Fatalf("moveContent: %v", err)
		}
		if !pathExists(dest) {
			t.Error("file not moved")
		}
	})

	t.Run("moveContent missing source", func(t *testing.T) {
		if err := moveContent("/no/such/path", t.TempDir()); err == nil {
			t.Error("want error for missing source")
		}
	})

	t.Run("copyFile missing source", func(t *testing.T) {
		if err := copyFile("/no/such/file", filepath.Join(t.TempDir(), "out")); err == nil {
			t.Error("want error for missing source")
		}
	})

	t.Run("pathExists", func(t *testing.T) {
		dir := t.TempDir()
		if !pathExists(dir) {
			t.Error("existing dir reported missing")
		}
		if pathExists(filepath.Join(dir, "ghost")) {
			t.Error("missing path reported existing")
		}
	})
}

func TestWriteMetadataSidecar(t *testing.T) {
	t.Run("directory dest writes inside", func(t *testing.T) {
		dir := t.TempDir()
		writeMetadataSidecar(dir, "My Game", "SNES", "snes", false, "torrent")
		data, err := os.ReadFile(filepath.Join(dir, ".gamarr.json"))
		if err != nil {
			t.Fatalf("sidecar missing: %v", err)
		}
		var meta map[string]interface{}
		if err := json.Unmarshal(data, &meta); err != nil {
			t.Fatalf("invalid sidecar JSON: %v", err)
		}
		if meta["title"] != "My Game" || meta["platform_slug"] != "snes" || meta["source"] != "torrent" {
			t.Errorf("sidecar meta = %v", meta)
		}
		if meta["is_pc"] != false {
			t.Errorf("is_pc = %v, want false", meta["is_pc"])
		}
	})

	t.Run("file dest writes sibling", func(t *testing.T) {
		fp := filepath.Join(t.TempDir(), "rom.sfc")
		writeFileT(t, fp, []byte("rom"))
		writeMetadataSidecar(fp, "Rom Game", "SNES", "snes", false, "ddl")
		if !pathExists(fp + ".gamarr.json") {
			t.Error("sibling sidecar missing")
		}
	})
}

func TestSettingsRoundTrip(t *testing.T) {
	cfg := newTestConfig(t)
	m := New(cfg, newTestJobs(t), nil)

	t.Run("defaults when no file", func(t *testing.T) {
		s := m.LoadSettings()
		if s.ExtractArchives != cfg.ExtractArchives {
			t.Errorf("ExtractArchives = %v, want config default %v", s.ExtractArchives, cfg.ExtractArchives)
		}
	})

	t.Run("save and load", func(t *testing.T) {
		m.SaveSettings(&Settings{ExtractArchives: true})
		s := m.LoadSettings()
		if !s.ExtractArchives {
			t.Error("ExtractArchives = false after saving true")
		}
	})

	t.Run("corrupt file falls back to default", func(t *testing.T) {
		writeFileT(t, filepath.Join(cfg.DataDir, "settings.json"), []byte("{{{"))
		s := m.LoadSettings()
		if s.ExtractArchives != cfg.ExtractArchives {
			t.Errorf("corrupt settings should fall back to config default")
		}
	})
}

func TestDDLSourcesRoundTrip(t *testing.T) {
	cfg := newTestConfig(t)
	m := New(cfg, newTestJobs(t), nil)

	if got := m.LoadDDLSources(); got != nil {
		t.Errorf("LoadDDLSources with no file = %v, want nil", got)
	}

	sources := []map[string]interface{}{
		{"name": "Myrient", "url": "https://example.test/roms"},
		{"name": "Other", "enabled": true},
	}
	m.SaveDDLSources(sources)
	got := m.LoadDDLSources()
	if len(got) != 2 {
		t.Fatalf("loaded %d sources, want 2", len(got))
	}
	if got[0]["name"] != "Myrient" {
		t.Errorf("first source = %v", got[0])
	}
}

func TestExtractArchives(t *testing.T) {
	t.Run("corrupt archive removed and skipped", func(t *testing.T) {
		dir := t.TempDir()
		writeFileT(t, filepath.Join(dir, "broken.zip"), []byte("this is not a zip"))
		extracted := extractArchives(dir)
		if len(extracted) != 0 {
			t.Errorf("extracted = %v, want none for corrupt archive", extracted)
		}
		if pathExists(filepath.Join(dir, "broken.zip.extracted")) {
			t.Error("failed extraction dir should be cleaned up")
		}
	})

	t.Run("already extracted archive skipped", func(t *testing.T) {
		dir := t.TempDir()
		writeFileT(t, filepath.Join(dir, "done.zip"), []byte("junk"))
		if err := os.MkdirAll(filepath.Join(dir, "done.zip.extracted"), 0755); err != nil {
			t.Fatal(err)
		}
		if extracted := extractArchives(dir); len(extracted) != 0 {
			t.Errorf("extracted = %v, want none (already extracted)", extracted)
		}
	})

	t.Run("recurses into subdirectories", func(t *testing.T) {
		dir := t.TempDir()
		writeFileT(t, filepath.Join(dir, "sub", "inner.rar"), []byte("not a rar"))
		// Should not panic and should not extract anything (unrar fails/missing).
		if extracted := extractArchives(dir); len(extracted) != 0 {
			t.Errorf("extracted = %v, want none", extracted)
		}
	})

	t.Run("valid zip extracted when 7z available", func(t *testing.T) {
		if _, err := exec.LookPath("7z"); err != nil {
			t.Skip("7z not installed")
		}
		dir := t.TempDir()
		zipPath := filepath.Join(dir, "good.zip")
		f, err := os.Create(zipPath)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(f)
		w, _ := zw.Create("inside.txt")
		w.Write([]byte("hello"))
		zw.Close()
		f.Close()

		extracted := extractArchives(dir)
		if len(extracted) != 1 {
			t.Fatalf("extracted = %v, want 1", extracted)
		}
		if !pathExists(filepath.Join(dir, "good.zip.extracted", "inside.txt")) {
			t.Error("extracted file missing")
		}
	})
}

func TestMaybeExtractArchives(t *testing.T) {
	t.Run("disabled is a no-op", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		m := New(cfg, jobs, nil)
		dir := t.TempDir()
		writeFileT(t, filepath.Join(dir, "a.zip"), []byte("junk"))
		jobID := newJobID()
		jobs.Set(jobID, map[string]interface{}{"status": "completed", "detail": "Moved"})

		m.maybeExtractArchives(jobID, dir)

		job, _ := jobs.Get(jobID)
		if detail, _ := job["detail"].(string); detail != "Moved" {
			t.Errorf("detail changed when extraction disabled: %q", detail)
		}
	})

	t.Run("enabled with missing dest returns", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		m := New(cfg, jobs, nil)
		m.SaveSettings(&Settings{ExtractArchives: true})
		m.maybeExtractArchives("nojob", filepath.Join(t.TempDir(), "ghost"))
	})

	t.Run("enabled with file dest scans parent dir", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		m := New(cfg, jobs, nil)
		m.SaveSettings(&Settings{ExtractArchives: true})
		dir := t.TempDir()
		fp := filepath.Join(dir, "rom.sfc")
		writeFileT(t, fp, []byte("rom"))
		writeFileT(t, filepath.Join(dir, "bad.zip"), []byte("junk"))
		// Corrupt zip fails extraction; the call must not panic or alter the job.
		m.maybeExtractArchives("nojob", fp)
		if pathExists(filepath.Join(dir, "bad.zip.extracted")) {
			t.Error("failed extraction dir left behind")
		}
	})
}
