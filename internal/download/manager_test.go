package download

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"gamarr/internal/fileops"
	"gamarr/internal/platform"
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
	if _, err := m.DownloadTorrent("", "", "Title", "PC", "", true); err == nil {
		t.Fatal("empty URL should return an error")
	}
}

func TestDownloadTorrentNoClientAvailable(t *testing.T) {
	// No qBittorrent, Transmission, or Deluge configured: the job errors.
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)

	jobID, err := m.DownloadTorrent("magnet:x", "", "Some Game", "PC", "", true)
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
	jobID, err := m.DownloadTorrent("magnet:x", "", "Super Game (USA)", "SNES", "snes", false)
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

func TestDownloadTorrentTracksRenamedTorrentByHash(t *testing.T) {
	// The release title we grabbed and the name the tracker gives the torrent
	// need not contain one another, so titlesMatch alone loses the download.
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	cfg.QBURL = "configured"

	content := filepath.Join(t.TempDir(), "Chrono Quest")
	writeFileT(t, filepath.Join(content, "game.sfc"), []byte("rom-data"))

	qm := newQbitMock(t)
	qm.setFiles([]qbit.TorrentFile{{Name: "Chrono Quest/game.sfc"}})
	qm.setTorrents([]qbit.Torrent{{
		Name:        "Chrono Quest [Repack]",
		Hash:        "hash-renamed",
		Progress:    1.0,
		ContentPath: content,
	}})

	title := "Chrono Quest (v1.2 + Bonus OST, MULTi9) [Repack]"
	if titlesMatch(title, "Chrono Quest [Repack]") {
		t.Fatal("fixture no longer exercises a title mismatch")
	}

	m := New(cfg, jobs, qm.client())
	jobID, err := m.DownloadTorrent("magnet:x", "hash-renamed", title, "SNES", "snes", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	job := waitJobStatus(t, jobs, jobID, "completed", 10*time.Second)
	if got, _ := job["info_hash"].(string); got != "hash-renamed" {
		t.Errorf("job info_hash = %q, want hash-renamed", got)
	}
	// The import writes the completed status before it tracks the library item,
	// so reaching that status does not mean the row exists yet.
	waitFor(t, minPollTimeout, "the library item to be tracked", func() bool {
		return jobs.LibraryHasSourceID("torrent:hash-renamed")
	})
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
	jobID, err := m.DownloadTorrent("magnet:x", "", "Evil Game", "PC", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	waitFor(t, 10*time.Second, "dangerous torrent stop", func() bool {
		return len(qm.stoppedHashes()) > 0
	})
	// Plenty of legitimate repacks ship a .bat next to their checksum tooling,
	// so the download has to survive for the operator to judge it.
	if calls := qm.deleteCalls(); len(calls) != 0 {
		t.Errorf("download was deleted (%+v); it must be left in place for review", calls)
	}
	job := waitJobStatus(t, jobs, jobID, "error", 5*time.Second)
	if errMsg, _ := job["error"].(string); !strings.Contains(errMsg, "keygen.bat") {
		t.Errorf("error = %q, want the offending filename in it", errMsg)
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
		jobID, err := m.DownloadTorrent("magnet:x", "", "Fallback Game", "PC", "", true)
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
		jobID, err := m.DownloadTorrent("magnet:x", "", "Fallback Game 2", "PC", "", true)
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
		jobID, err := m.DownloadTorrent("magnet:x", "", "Doomed Game", "PC", "", true)
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

	t.Run("http error fails the job with cause", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		m := New(cfg, jobs, nil)
		jobID := m.DownloadDDL(srv.URL+"/gone", "", "Missing Game", "PC", "", true)
		job := waitJobStatus(t, jobs, jobID, "error", 5*time.Second)
		errMsg, _ := job["error"].(string)
		if !strings.Contains(errMsg, "Download failed") {
			t.Errorf("error = %q, want Download failed", errMsg)
		}
		if !strings.Contains(errMsg, "404") {
			t.Errorf("error = %q, want HTTP 404 cause included", errMsg)
		}
	})

	t.Run("uncreatable staging dir fails the job with cause", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		// A regular file where the staging dir's parent should be makes
		// os.MkdirAll fail with ENOTDIR.
		blocker := filepath.Join(t.TempDir(), "blocker")
		writeFileT(t, blocker, []byte("not a dir"))
		cfg.QBSavePath = filepath.Join(blocker, "staging")

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("rom-bytes"))
		}))
		defer srv.Close()

		m := New(cfg, jobs, nil)
		jobID := m.DownloadDDL(srv.URL+"/dl", "", "Blocked Game", "PC", "", true)
		job := waitJobStatus(t, jobs, jobID, "error", 5*time.Second)
		errMsg, _ := job["error"].(string)
		if !strings.Contains(errMsg, "cannot create staging dir") {
			t.Errorf("error = %q, want 'cannot create staging dir' cause", errMsg)
		}
		if !strings.Contains(errMsg, cfg.QBSavePath) {
			t.Errorf("error = %q, want staging path %q included", errMsg, cfg.QBSavePath)
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

	got, err := m.downloadDDL(srv.URL+"/files/zelda.gba?token=1", cfg.QBSavePath, jobID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(got) != "zelda.gba" {
		t.Errorf("filename = %q, want zelda.gba", filepath.Base(got))
	}
	if !pathExists(got) {
		t.Error("downloaded file missing")
	}
}

func TestDownloadDDLCreateFileFailure(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data"))
	}))
	defer srv.Close()

	m := New(cfg, jobs, nil)
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	// Destination dir does not exist, so os.Create fails.
	dest := filepath.Join(t.TempDir(), "no-such-dir")
	got, err := m.downloadDDL(srv.URL+"/files/game.bin", dest, jobID)
	if got != "" {
		t.Errorf("path = %q, want empty on create failure", got)
	}
	if err == nil || !strings.Contains(err.Error(), "cannot create file") {
		t.Errorf("err = %v, want 'cannot create file' cause", err)
	}
}

// TestDownloadDDLTruncatedDownloadIsError verifies that a server which declares
// a Content-Length but drops the connection early does not leave a partial file
// reported as a finished download. Without the guard the truncated archive would
// pass to the scan/organize pipeline as complete.
func TestDownloadDDLTruncatedDownloadIsError(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="Truncated.sfc"`)
		w.Header().Set("Content-Length", "1000") // claim far more than we send
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("only-a-few-bytes"))
		// Return without sending the rest; the client sees an unexpected EOF.
	}))
	defer srv.Close()

	m := New(cfg, jobs, nil)
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	dest := t.TempDir()
	got, err := m.downloadDDL(srv.URL+"/dl", dest, jobID)
	if err == nil {
		t.Fatal("expected an error for a truncated download, got nil")
	}
	if got != "" {
		t.Errorf("path = %q, want empty on truncated download", got)
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 0 {
		t.Errorf("partial file left behind: %v", entries)
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

	// A DDL source need not label its rows either, and the file itself is then
	// the only thing that can say where the download belongs.
	t.Run("platform comes from the file when the request carried none", func(t *testing.T) {
		m, jobID, _ := newFixture(t)
		src := filepath.Join(t.TempDir(), "Some Cartridge.nes")
		writeFileT(t, src, []byte("rom"))

		m.organizeDDLFile(jobID, src, "Some Cartridge", "", "", false)

		dest := filepath.Join(m.cfg.GamesRomsPath, "nes", "Some Cartridge.nes")
		if !pathExists(dest) {
			t.Errorf("DDL import did not detect the platform from its file: %s not written", dest)
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
	cfg.QBURL = qm.srv.URL

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

func TestRecoverOrphanedTorrentsIsIdempotent(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	// One complete, one still going: the two branches record a job by
	// different routes and both have to be idempotent.
	qm.setTorrents([]qbit.Torrent{
		{Name: "Awesome.Game.v1.2-FitGirl", Hash: "h1", Progress: 1.0},
		{Name: "Zelda Collection wii pack", Hash: "h2", Progress: 0.4},
	})
	cfg.QBURL = qm.srv.URL

	m := New(cfg, jobs, qm.client())

	// Recovery runs at startup and again whenever the monitor fires
	// run_orphan_recovery, so a second pass is ordinary, not pathological.
	m.RecoverOrphanedTorrents()
	after1 := len(jobs.Items())
	m.RecoverOrphanedTorrents()
	after2 := len(jobs.Items())

	if after1 != 2 {
		t.Fatalf("first pass recorded %d jobs, want 2", after1)
	}
	// Counting rows, not titles: a map keyed by title hides the duplicates.
	if after2 != after1 {
		t.Errorf("second pass took jobs from %d to %d, want them unchanged", after1, after2)
	}

	hashes := map[string]int{}
	for _, item := range jobs.Items() {
		h, _ := item.Data["info_hash"].(string)
		hashes[h]++
	}
	for h, n := range hashes {
		if n != 1 {
			t.Errorf("hash %q has %d job rows, want 1", h, n)
		}
	}
}

func TestRecoverOrphanedTorrentsLeavesImportedGamesAlone(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	// finishTorrent leaves a torrent seeding after a source-preserving import,
	// so a game that is already in the library is still in the category the next
	// time recovery runs. That is the ordinary case, not an odd one.
	qm.setTorrents([]qbit.Torrent{
		{Name: "Imported.Game-FitGirl", Hash: "h1", Progress: 1.0},
	})
	cfg.QBURL = qm.srv.URL

	m := New(cfg, jobs, qm.client())
	jobs.Set("job-done", map[string]interface{}{
		"status":    "completed",
		"title":     "Imported.Game-FitGirl",
		"info_hash": "h1",
		"detail":    "Moved to GameVault",
	})

	m.RecoverOrphanedTorrents()

	job, ok := jobs.Get("job-done")
	if !ok {
		t.Fatal("recovery dropped the completed job")
	}
	// completed_unorganized is what draws the Organize button, so a downgrade
	// here offers to import a game that is already in the library.
	if status, _ := job["status"].(string); status != "completed" {
		t.Errorf("status = %q, want it left at completed", status)
	}
	if detail, _ := job["detail"].(string); detail != "Moved to GameVault" {
		t.Errorf("detail = %q, want the import's own detail kept", detail)
	}
	if n := len(jobs.Items()); n != 1 {
		t.Errorf("%d job rows, want 1 - recovery added a row for an imported game", n)
	}
}

func TestWatchGameTorrentRunsOneWatcherPerTorrent(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	// Never completes, so a watcher that takes the claim stays parked in its
	// poll loop and holds it for the duration of the test.
	qm.setTorrents([]qbit.Torrent{
		{Name: "Held.Game-FitGirl", Hash: "h1", Progress: 0.4},
	})
	cfg.QBURL = qm.srv.URL
	cfg.FileListScanEnabled = false

	m := New(cfg, jobs, qm.client())

	go m.watchGameTorrent("job-1", "h1", "Held.Game-FitGirl", "PC", "", true)

	// Wait for the first watcher to register rather than sleeping a fixed
	// guess at how long it takes to get there.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, held := m.watching.Load("h1"); held {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first watcher never claimed the torrent")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A second watcher on the same torrent must decline and return, not sit
	// in a rival poll loop racing the first one to import.
	done := make(chan struct{})
	go func() {
		m.watchGameTorrent("job-2", "h1", "Held.Game-FitGirl", "PC", "", true)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("second watcher did not return - it is running alongside the first")
	}
}

func TestDownloadTorrentResolvesHashFromClient(t *testing.T) {
	const (
		release = "Cyberpunk 2077: Ultimate Edition - v2.3 [FitGirl Repack]"
		torrent = "Setup.CP2077.UltimateEdition"
		hash    = "c9d3921bf7017490c74d546c3d0fd6f1e0694422"
	)
	// The premise of the test: with no infohash, nothing else binds this job to
	// this torrent. If the titles ever did match, the test would pass without
	// exercising the resolution at all.
	if titlesMatch(release, torrent) {
		t.Fatalf("test premise broken: %q and %q match on title", release, torrent)
	}

	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	cfg.QBURL = qm.srv.URL
	cfg.FileListScanEnabled = false
	qm.appearOnAdd(qbit.Torrent{Name: torrent, Hash: hash, Progress: 0.2})

	m := New(cfg, jobs, qm.client())
	// A Prowlarr redirect link, which is what gamarr actually receives - there
	// is no magnet to read the hash out of.
	jobID, err := m.DownloadTorrent(
		"http://prowlarr:9696/75/download?link=abc", "", release, "PC", "", true)
	if err != nil {
		t.Fatalf("DownloadTorrent: %v", err)
	}

	// Resolution runs off the request path, so poll for it rather than
	// assuming it has already happened.
	deadline := time.Now().Add(10 * time.Second)
	for {
		job, ok := jobs.Get(jobID)
		if !ok {
			t.Fatalf("job %s vanished", jobID)
		}
		got, _ := job["info_hash"].(string)
		if strings.EqualFold(got, hash) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("info_hash = %q, want %q - the client was never asked", got, hash)
		}
		time.Sleep(20 * time.Millisecond)
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

// Some sources legitimately ship a .bat next to the payload, and the operator
// has no other way past a filename match.
func TestDownloadTorrentFileListScanDisabled(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	cfg.QBURL = "configured"
	cfg.FileListScanEnabled = false

	qm := newQbitMock(t)
	qm.setFiles([]qbit.TorrentFile{{Name: "Game/keygen.bat"}, {Name: "Game/setup.scr"}})
	qm.setTorrents([]qbit.Torrent{{
		Name:     "Repack Game",
		Hash:     "hash-optout",
		Progress: 0.5, // metadata available, still downloading
	}})

	m := New(cfg, jobs, qm.client())
	jobID, err := m.DownloadTorrent("magnet:x", "", "Repack Game", "PC", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With the scan on, the watcher acts on its first pass, so a short wait is
	// enough to catch it having done so.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(qm.deleteCalls()) != 0 {
			t.Fatalf("torrent was acted on despite the scan being disabled: %+v", qm.deleteCalls())
		}
		if job, ok := jobs.Get(jobID); ok {
			if status, _ := job["status"].(string); status == "error" {
				t.Fatalf("job errored despite the scan being disabled: %v", job["error"])
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Prowlarr maps Nyaa's only games category to Newznab 4050 PC/Games, so a
// Switch ROM from Nyaa reaches the downloader tagged PC. It has to still land
// in the Switch ROM library, not GameVault.
func TestNyaaSwitchROMTaggedPCImportsAsSwitch(t *testing.T) {
	info := platform.DetectPlatform([]interface{}{float64(4050)})
	if !info.IsPC {
		t.Fatalf("fixture stale: 4050 no longer detects as PC (%+v)", info)
	}

	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	cfg.QBURL = "configured"

	content := filepath.Join(t.TempDir(), "Zelda TOTK")
	writeFileT(t, filepath.Join(content, "Zelda.TOTK.nsp"), []byte("switch-rom"))

	qm := newQbitMock(t)
	qm.setFiles([]qbit.TorrentFile{{Name: "Zelda TOTK/Zelda.TOTK.nsp"}})
	qm.setTorrents([]qbit.Torrent{{
		Name: "Zelda TOTK", Hash: "h-switch", Progress: 1.0, ContentPath: content,
	}})

	m := New(cfg, jobs, qm.client())
	// exactly what a 4050-tagged search hit hands the downloader
	jobID, err := m.DownloadTorrent("magnet:x", "h-switch", "Zelda TOTK", info.Name, info.Slug, info.IsPC)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	job := waitJobStatus(t, jobs, jobID, "completed", 10*time.Second)
	if got, _ := job["platform_slug"].(string); got != "switch" {
		t.Errorf("platform_slug = %q, want switch", got)
	}
	if isPC, _ := job["is_pc"].(bool); isPC {
		t.Error("job still marked is_pc after a .nsp was found")
	}
	wantPath := filepath.Join(cfg.GamesRomsPath, "switch", "Zelda TOTK", "Zelda.TOTK.nsp")
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("ROM not in the Switch library: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.GamesVaultPath, "Zelda TOTK")); err == nil {
		t.Error("ROM was imported into GameVault as a PC game")
	}
}

// The other half of the same category: a genuine PC release tagged 4050 has to
// keep going to GameVault.
func TestPCGameTaggedPCGamesImportsToVault(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	cfg.QBURL = "configured"

	content := filepath.Join(t.TempDir(), "Terraria")
	writeFileT(t, filepath.Join(content, "setup.exe"), []byte("installer"))
	// a Doom-engine asset and a bundled NES ROM: neither may reroute a PC game
	writeFileT(t, filepath.Join(content, "base.wad"), []byte("doom"))
	writeFileT(t, filepath.Join(content, "extras", "bonus.nes"), []byte("rom"))

	qm := newQbitMock(t)
	// list the real payload: a lone .exe trips the scanner's "only executables"
	// heuristic, which is not what this test is about
	qm.setFiles([]qbit.TorrentFile{
		{Name: "Terraria/setup.exe"},
		{Name: "Terraria/base.wad"},
		{Name: "Terraria/extras/bonus.nes"},
	})
	qm.setTorrents([]qbit.Torrent{{
		Name: "Terraria", Hash: "h-pc", Progress: 1.0, ContentPath: content,
	}})

	m := New(cfg, jobs, qm.client())
	jobID, err := m.DownloadTorrent("magnet:x", "h-pc", "Terraria", "PC", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	job := waitJobStatus(t, jobs, jobID, "completed", 10*time.Second)
	if isPC, _ := job["is_pc"].(bool); !isPC {
		t.Errorf("PC game was reclassified: platform=%v slug=%v", job["platform"], job["platform_slug"])
	}
	if _, err := os.Stat(filepath.Join(cfg.GamesVaultPath, "Terraria", "setup.exe")); err != nil {
		t.Errorf("PC game not in GameVault: %v", err)
	}
}

// The per-job watch imports through the same path the watcher does, so a
// content path that is not there yet has to recover on both. It did not: the
// watch computed the retryable signal and discarded it, erroring the job
// permanently, and an errored job then stops the watcher rescuing it.
func TestWatchGameTorrentRetriesAPathThatIsNotThereYet(t *testing.T) {
	setImportRetries(t, 400, 5*time.Millisecond)
	qm := newQbitMock(t)
	cfg := newTestConfig(t)
	cfg.QBURL = "configured"
	m := New(cfg, newTestJobs(t), qm.client())

	// A FitGirl release: the folder the client publishes is not the torrent's
	// display name, so the save-path guess never resolves either.
	content := filepath.Join(cfg.QBSavePath, "Spider-Man - Miles Morales [FitGirl Repack]")
	torrent := qbit.Torrent{
		Name:        "Marvel's Spider-Man - Miles Morales (v3.1.0 + DLC, MULTi19) [FitGirl Repack]",
		Hash:        "mm-hash",
		Progress:    1.0,
		SavePath:    cfg.QBSavePath,
		ContentPath: content,
	}
	qm.setTorrents([]qbit.Torrent{torrent})

	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{
		"status": "downloading", "title": torrent.Name, "info_hash": "mm-hash",
	})

	staged := make(chan error, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		if err := os.MkdirAll(content, 0755); err != nil {
			staged <- err
			return
		}
		staged <- os.WriteFile(filepath.Join(content, "setup.exe"), []byte("installer"), 0644)
	}()

	m.watchGameTorrent(jobID, "mm-hash", torrent.Name, "PC", "", true)

	if err := <-staged; err != nil {
		t.Fatalf("stage the published files: %v", err)
	}
	assertImportedCleanly(t, m, jobID)
}

// The manual organize is the path used to unstick a game by hand, and it hit the
// transient without a retry. Worse than the immediate failure: the error job it
// left behind is what makes the watcher skip the torrent afterwards, so one
// mistimed manual organize stranded it from the automatic path for good.
func TestOrganizeTorrentRetriesAPathThatIsNotThereYet(t *testing.T) {
	setImportRetries(t, 400, 5*time.Millisecond)
	qm := newQbitMock(t)
	cfg := newTestConfig(t)
	cfg.QBURL = "configured"
	m := New(cfg, newTestJobs(t), qm.client())

	content := filepath.Join(cfg.QBSavePath, "Manual Game [FitGirl Repack]")
	qm.setTorrents([]qbit.Torrent{{
		Name: "Manual Game", Hash: "man-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: content,
	}})

	staged := make(chan error, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		if err := os.MkdirAll(content, 0755); err != nil {
			staged <- err
			return
		}
		staged <- os.WriteFile(filepath.Join(content, "setup.exe"), []byte("installer"), 0644)
	}()

	jobID, err := m.OrganizeTorrent("man-hash", "PC", "", true)
	if err != nil {
		t.Fatalf("OrganizeTorrent: %v", err)
	}

	waitJobStatus(t, m.Jobs(), jobID, "completed", minPollTimeout)
	if err := <-staged; err != nil {
		t.Fatalf("stage the published files: %v", err)
	}
	assertImportedCleanly(t, m, jobID)
}

// The client moves a finished torrent from the staging path to its final home
// right after progress reads complete, so the first import can lose files
// mid-walk even though the content path itself resolves. That failure has to
// read as retryable: the loop re-reads the torrent and lands the published
// payload, which is what the manual retry does by hand.
func TestImportRetriesWhenTheClientMovesThePayloadMidWalk(t *testing.T) {
	setImportRetries(t, 400, 5*time.Millisecond)
	qm := newQbitMock(t)
	cfg := newTestConfig(t)
	cfg.QBURL = "configured"
	// The vault archive is where the walk loses the race on a live FitGirl grab.
	cfg.VaultArchiveEnabled = true
	m := New(cfg, newTestJobs(t), qm.client())

	stale := filepath.Join(cfg.QBSavePath, "temp", "The Witcher 3 [FitGirl Repack]")
	published := filepath.Join(cfg.QBSavePath, "The Witcher 3 [FitGirl Repack]")
	for _, d := range []string{stale, published} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
		if err := os.WriteFile(filepath.Join(d, "setup.exe"), []byte("installer"), 0644); err != nil {
			t.Fatalf("write setup.exe: %v", err)
		}
	}

	// The client answers the loop's re-read with the published path, while the
	// import still holds the struct captured at progress 1.0.
	qm.setTorrents([]qbit.Torrent{{
		Name: "The Witcher 3 [FitGirl Repack]", Hash: "w3-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: published,
	}})
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{
		"status": "downloading", "title": "The Witcher 3 [FitGirl Repack]", "info_hash": "w3-hash",
	})

	realArchive := archive
	attempts := 0
	archive = func(src, dest string, wanted fileops.WantedFiles) error {
		attempts++
		if attempts == 1 {
			return &fs.PathError{Op: "lstat", Path: filepath.Join(src, "fg-02.bin"), Err: os.ErrNotExist}
		}
		return realArchive(src, dest, wanted)
	}
	t.Cleanup(func() { archive = realArchive })

	m.importFinishedTorrent("job watch", jobID, qbit.Torrent{
		Name: "The Witcher 3 [FitGirl Repack]", Hash: "w3-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: stale,
	}, "PC", "", true)

	if attempts < 2 {
		t.Errorf("archive attempts = %d, want the failed one retried at the published path", attempts)
	}
	assertImportedCleanly(t, m, jobID)
}

// assertImportedCleanly checks the whole job row rather than the status alone.
// A status assertion on its own passes while the row is wrong: the error field
// the UI renders regardless of status is exactly what a retried import was
// leaving behind from its failed attempts.
func assertImportedCleanly(t *testing.T, m *Manager, jobID string) {
	t.Helper()
	job, ok := m.Jobs().Get(jobID)
	if !ok {
		t.Fatalf("job %s not found", jobID)
	}
	status, _ := job["status"].(string)
	detail, _ := job["detail"].(string)
	errMsg, _ := job["error"].(string)
	if status != "completed" || detail != "Moved to GameVault" || errMsg != "" {
		t.Errorf("job = {status:%q detail:%q error:%q}, want completed, moved to the vault, and nothing left over from a failed attempt",
			status, detail, errMsg)
	}
}
