package download

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gamarr/internal/qbit"
	"gamarr/internal/sabnzbd"
)

// sabMock is a fake SABnzbd API server.
type sabMock struct {
	srv        *httptest.Server
	addStatus  bool
	addError   string
	nzoID      string
	queueSlots []map[string]interface{}
	histSlots  []map[string]interface{}
}

type nzbgetMock struct {
	srv        *httptest.Server
	addID      int64
	addError   string
	queue      []map[string]interface{}
	history    []map[string]interface{}
	lastParams []interface{}
}

func newNZBGetMock(t *testing.T) *nzbgetMock {
	t.Helper()
	mock := &nzbgetMock{addID: 99}
	mock.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string        `json:"method"`
			Params []interface{} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode NZBGet request: %v", err)
		}
		var result interface{}
		switch req.Method {
		case "append":
			mock.lastParams = req.Params
			if mock.addError != "" {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0", "id": 1,
					"error": map[string]interface{}{"code": -1, "message": mock.addError},
				})
				return
			}
			result = mock.addID
		case "listgroups":
			result = mock.queue
		case "history":
			result = mock.history
		default:
			t.Fatalf("unexpected NZBGet method %q", req.Method)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0", "id": 1, "result": result,
		})
	}))
	t.Cleanup(mock.srv.Close)
	return mock
}

func newSabMock(t *testing.T) *sabMock {
	t.Helper()
	s := &sabMock{addStatus: true, nzoID: "SABnzbd_nzo_test1"}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("mode") {
		case "addurl":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  s.addStatus,
				"nzo_ids": []string{s.nzoID},
				"error":   s.addError,
			})
		case "queue":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"queue": map[string]interface{}{"slots": s.queueSlots},
			})
		case "history":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"history": map[string]interface{}{"slots": s.histSlots},
			})
		default:
			json.NewEncoder(w).Encode(map[string]interface{}{})
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *sabMock) client() *sabnzbd.Client {
	return sabnzbd.New(s.srv.URL, "apikey")
}

func TestDownloadNZBNilClient(t *testing.T) {
	m := New(newTestConfig(t), newTestJobs(t), nil)
	_, err := m.DownloadNZB(nil, "http://x/nzb", "Game", "PC", "", true)
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("err = %v, want not configured", err)
	}
}

func TestDownloadNZBAddError(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	sab := newSabMock(t)
	sab.addStatus = false
	sab.addError = "invalid api key"

	m := New(cfg, jobs, nil)
	jobID, err := m.DownloadNZB(sab.client(), "http://x/file.nzb", "Bad Game", "PC", "", true)
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
	if errMsg, _ := job["error"].(string); !strings.Contains(errMsg, "invalid api key") {
		t.Errorf("error = %q, want invalid api key", errMsg)
	}
	if st, _ := job["source_type"].(string); st != "nzb" {
		t.Errorf("source_type = %q, want nzb", st)
	}
}

func TestDownloadNZBCompletedFlow(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)

	storage := filepath.Join(t.TempDir(), "Usenet Game")
	writeFileT(t, filepath.Join(storage, "rom.sfc"), []byte("rom"))

	sab := newSabMock(t)
	sab.queueSlots = []map[string]interface{}{
		{"nzo_id": sab.nzoID, "mb": 100.0, "mbleft": 25.0, "status": "Downloading"},
	}
	sab.histSlots = []map[string]interface{}{
		{"nzo_id": sab.nzoID, "status": "Completed", "storage": storage},
	}

	m := New(cfg, jobs, nil)
	jobID, err := m.DownloadNZB(sab.client(), "http://x/game.nzb", "Usenet Game", "SNES", "snes", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dest := filepath.Join(cfg.GamesRomsPath, "snes", "Usenet Game")
	waitFor(t, 10*time.Second, "nzb library tracking", func() bool {
		return jobs.LibraryHasSourceID("nzb:" + dest)
	})
	job := waitJobStatus(t, jobs, jobID, "completed", 5*time.Second)
	if detail, _ := job["detail"].(string); !strings.Contains(detail, "RomM (SNES)") {
		t.Errorf("detail = %q, want RomM (SNES)", detail)
	}
	if !pathExists(filepath.Join(dest, "rom.sfc")) {
		t.Error("nzb content not moved to library")
	}
	if !pathExists(filepath.Join(dest, ".gamarr.json")) {
		t.Error("sidecar not written")
	}
}

func TestDownloadNZBFailedFlow(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	sab := newSabMock(t)
	sab.histSlots = []map[string]interface{}{
		{"nzo_id": sab.nzoID, "status": "Failed"},
	}

	m := New(cfg, jobs, nil)
	jobID, err := m.DownloadNZB(sab.client(), "http://x/game.nzb", "Doomed", "PC", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	job := waitJobStatus(t, jobs, jobID, "error", 5*time.Second)
	if errMsg, _ := job["error"].(string); !strings.Contains(errMsg, "failed") {
		t.Errorf("error = %q, want download failed", errMsg)
	}
}

func TestDownloadNZBGetCompletedFlow(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	storage := filepath.Join(t.TempDir(), "NZBGet Game")
	writeFileT(t, filepath.Join(storage, "rom.gba"), []byte("rom"))

	mock := newNZBGetMock(t)
	mock.queue = []map[string]interface{}{{
		"NZBID": mock.addID, "NZBName": "NZBGet Game", "Status": "DOWNLOADING",
		"FileSizeMB": 200, "RemainingSizeMB": 50,
	}}
	mock.history = []map[string]interface{}{{
		"NZBID": mock.addID, "Status": "SUCCESS/UNPACK", "DestDir": storage,
	}}
	cfg.NZBGetURL = mock.srv.URL
	cfg.NZBGetCategory = "games"

	m := New(cfg, jobs, nil)
	jobID, err := m.DownloadNZB(nil, "https://indexer.example/game.nzb", "NZBGet Game", "GBA", "gba", false)
	if err != nil {
		t.Fatalf("DownloadNZB: %v", err)
	}

	dest := filepath.Join(cfg.GamesRomsPath, "gba", "NZBGet Game")
	waitFor(t, 10*time.Second, "NZBGet library tracking", func() bool {
		return jobs.LibraryHasSourceID("nzb:" + dest)
	})
	job := waitJobStatus(t, jobs, jobID, "completed", 5*time.Second)
	if client, _ := job["source_client"].(string); client != "nzbget" {
		t.Errorf("source_client=%q, want nzbget", client)
	}
	if !pathExists(filepath.Join(dest, "rom.gba")) {
		t.Error("NZBGet content not moved to library")
	}
	if len(mock.lastParams) != 11 || mock.lastParams[2] != "games" {
		t.Errorf("append params=%v", mock.lastParams)
	}
}

func TestDownloadNZBGetFailures(t *testing.T) {
	t.Run("append error", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		mock := newNZBGetMock(t)
		mock.addError = "authentication failed"
		cfg.NZBGetURL = mock.srv.URL

		m := New(cfg, jobs, nil)
		jobID, err := m.DownloadNZB(nil, "https://example/game.nzb", "Bad", "PC", "", true)
		if err != nil {
			t.Fatalf("DownloadNZB: %v", err)
		}
		job, _ := jobs.Get(jobID)
		if status, _ := job["status"].(string); status != "error" {
			t.Errorf("status=%q, want error", status)
		}
	})

	t.Run("failed history", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		mock := newNZBGetMock(t)
		mock.history = []map[string]interface{}{{
			"NZBID": mock.addID, "Status": "FAILURE/UNPACK",
		}}
		cfg.NZBGetURL = mock.srv.URL

		m := New(cfg, jobs, nil)
		jobID, err := m.DownloadNZB(nil, "https://example/game.nzb", "Failed", "PC", "", true)
		if err != nil {
			t.Fatalf("DownloadNZB: %v", err)
		}
		job := waitJobStatus(t, jobs, jobID, "error", 5*time.Second)
		if errMsg, _ := job["error"].(string); !strings.Contains(errMsg, "FAILURE/UNPACK") {
			t.Errorf("error=%q", errMsg)
		}
	})
}

func TestRecoverOrphanedNZBDownloads(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	storage := filepath.Join(t.TempDir(), "Recovered NZB")
	writeFileT(t, filepath.Join(storage, "rom.gba"), []byte("rom"))

	mock := newNZBGetMock(t)
	mock.history = []map[string]interface{}{
		{"NZBID": mock.addID, "Status": "SUCCESS/UNPACK", "DestDir": storage},
	}
	cfg.NZBGetURL = mock.srv.URL
	cfg.NZBGetCategory = "games"

	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{
		"status":        "downloading",
		"title":         "Recovered NZB",
		"platform":      "Game Boy Advance",
		"platform_slug": "gba",
		"is_pc":         false,
		"source_type":   "nzb",
		"source_client": "nzbget",
		"nzb_id":        float64(mock.addID),
	})

	m := New(cfg, jobs, nil)
	m.RecoverOrphanedNZBDownloads()

	dest := filepath.Join(cfg.GamesRomsPath, "gba", "Recovered NZB")
	waitFor(t, 5*time.Second, "recovered NZBGet watcher", func() bool {
		job, ok := jobs.Get(jobID)
		return ok && job["status"] == "completed"
	})
	if !pathExists(filepath.Join(dest, "rom.gba")) {
		t.Fatal("recovered NZBGet content was not organized")
	}
}

func TestOrganizeNZBDownload(t *testing.T) {
	newFixture := func(t *testing.T) (*Manager, string) {
		m := New(newTestConfig(t), newTestJobs(t), nil)
		jobID := newJobID()
		m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})
		return m, jobID
	}

	t.Run("missing storage path", func(t *testing.T) {
		m, jobID := newFixture(t)
		m.organizeNZBDownload(jobID, "/no/such/storage", "G", "PC", "", true)
		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "error" {
			t.Errorf("status = %q, want error", status)
		}
	})

	t.Run("empty storage path", func(t *testing.T) {
		m, jobID := newFixture(t)
		m.organizeNZBDownload(jobID, "", "G", "PC", "", true)
		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "error" {
			t.Errorf("status = %q, want error", status)
		}
	})

	// A restart between moveContent and the status update leaves the staging
	// path gone while the content already sits in the library. Re-entering
	// organize must finish the job, not report the import as failed.
	t.Run("already moved content completes", func(t *testing.T) {
		m, jobID := newFixture(t)
		storage := filepath.Join(t.TempDir(), "Recovered Game")
		dest := filepath.Join(m.cfg.GamesRomsPath, "gba", "Recovered Game")
		writeFileT(t, filepath.Join(dest, "rom.gba"), []byte("rom"))

		m.organizeNZBDownloadWithClient(jobID, storage, "Recovered Game", "GBA", "gba", false, "nzbget")

		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "completed" {
			t.Errorf("status = %q, want completed", status)
		}
		if !m.Jobs().LibraryHasSourceID("nzb:" + dest) {
			t.Error("already-moved content was not tracked in the library")
		}
	})

	t.Run("pc content to vault", func(t *testing.T) {
		m, jobID := newFixture(t)
		storage := filepath.Join(t.TempDir(), "PC Game")
		writeFileT(t, filepath.Join(storage, "setup.exe"), []byte("x"))
		m.organizeNZBDownload(jobID, storage, "PC Game", "PC", "", true)
		job, _ := m.Jobs().Get(jobID)
		if detail, _ := job["detail"].(string); !strings.Contains(detail, "GameVault") {
			t.Errorf("detail = %q, want GameVault", detail)
		}
		if !pathExists(filepath.Join(m.cfg.GamesVaultPath, "PC Game", "setup.exe")) {
			t.Error("content not moved to vault")
		}
	})

	t.Run("unknown platform stays in staging", func(t *testing.T) {
		m, jobID := newFixture(t)
		storage := filepath.Join(t.TempDir(), "Mystery")
		writeFileT(t, filepath.Join(storage, "file.dat"), []byte("x"))
		m.organizeNZBDownload(jobID, storage, "Mystery", "", "", false)
		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "completed" {
			t.Errorf("status = %q, want completed", status)
		}
		if !pathExists(storage) {
			t.Error("content should remain in staging")
		}
	})

	t.Run("move failure sets error", func(t *testing.T) {
		m, jobID := newFixture(t)
		writeFileT(t, filepath.Join(m.cfg.GamesRomsPath, "snes"), []byte("blocking file"))
		storage := filepath.Join(t.TempDir(), "Rom Game")
		writeFileT(t, filepath.Join(storage, "rom.sfc"), []byte("x"))
		m.organizeNZBDownload(jobID, storage, "Rom Game", "SNES", "snes", false)
		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "error" {
			t.Errorf("status = %q, want error (job=%v)", status, job)
		}
	})
}

func TestRetryJob(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(jobID string, m *Manager)
		wantOK     bool
		wantMsg    string
		wantStatus string
	}{
		{
			name:    "job not found",
			setup:   func(jobID string, m *Manager) {},
			wantOK:  false,
			wantMsg: "not found",
		},
		{
			name: "job not in failed state",
			setup: func(jobID string, m *Manager) {
				m.Jobs().Set(jobID, map[string]interface{}{"status": "downloading"})
			},
			wantOK:  false,
			wantMsg: "not in failed state",
		},
		// A job with no torrent recorded has nothing to import again. It used to
		// report success and park at "queued", which the UI gives no button for,
		// so the click looked like it worked and stranded the row instead.
		{
			name: "no torrent recorded",
			setup: func(jobID string, m *Manager) {
				m.Jobs().Set(jobID, map[string]interface{}{"status": "error", "title": "G"})
			},
			wantOK:     false,
			wantMsg:    "no torrent recorded",
			wantStatus: "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(newTestConfig(t), newTestJobs(t), nil)
			jobID := newJobID()
			tt.setup(jobID, m)

			ok, msg := m.RetryJob(jobID)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (msg %q)", ok, tt.wantOK, msg)
			}
			if tt.wantMsg != "" && !strings.Contains(strings.ToLower(msg), strings.ToLower(tt.wantMsg)) {
				t.Errorf("msg = %q, want containing %q", msg, tt.wantMsg)
			}
			if tt.wantStatus != "" {
				job, _ := m.Jobs().Get(jobID)
				if status, _ := job["status"].(string); status != tt.wantStatus {
					t.Errorf("status = %q, want %q", status, tt.wantStatus)
				}
				if tt.wantOK && job["error"] != nil {
					t.Errorf("error = %v, want nil once a retry starts", job["error"])
				}
			}
		})
	}
}

func TestAutoRetryFailed(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.MaxRetries = 2
	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)

	jobs.Set("job-max", map[string]interface{}{
		"status": "error", "retry_count": float64(2),
	})
	jobs.Set("job-under", map[string]interface{}{
		"status": "error", "retry_count": float64(1),
	})
	jobs.Set("job-fine", map[string]interface{}{
		"status": "completed",
	})

	m.AutoRetryFailed()

	tests := []struct {
		jobID string
		want  string
	}{
		{"job-max", "dead_letter"},
		{"job-under", "error"},
		{"job-fine", "completed"},
	}
	for _, tt := range tests {
		t.Run(tt.jobID, func(t *testing.T) {
			job, _ := jobs.Get(tt.jobID)
			if status, _ := job["status"].(string); status != tt.want {
				t.Errorf("status = %q, want %q", status, tt.want)
			}
		})
	}

	t.Run("dead letter detail mentions max retries", func(t *testing.T) {
		job, _ := jobs.Get("job-max")
		if detail, _ := job["detail"].(string); !strings.Contains(detail, "Max retries") {
			t.Errorf("detail = %q, want Max retries", detail)
		}
	})
}

func TestStrVal(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]interface{}
		key  string
		want string
	}{
		{"present string", map[string]interface{}{"a": "x"}, "a", "x"},
		{"missing key", map[string]interface{}{}, "a", ""},
		{"non-string value", map[string]interface{}{"a": 3}, "a", ""},
		{"nil value", map[string]interface{}{"a": nil}, "a", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strVal(tt.m, tt.key); got != tt.want {
				t.Errorf("strVal = %q, want %q", got, tt.want)
			}
		})
	}
}

// The click that started all this: Retry set a status nothing consumed, so a
// failed import stayed failed while the UI reported success. It now re-runs the
// import on the same row, which is what stops a second row appearing for a
// torrent the job store will not dedup.
func TestRetryJobImportsOnTheSameRow(t *testing.T) {
	qm := newQbitMock(t)
	cfg := newTestConfig(t)
	cfg.QBURL = "configured"
	m := New(cfg, newTestJobs(t), qm.client())

	content := filepath.Join(cfg.QBSavePath, "Retried Game [FitGirl Repack]")
	writeFileT(t, filepath.Join(content, "setup.exe"), []byte("installer"))
	qm.setTorrents([]qbit.Torrent{{
		Name: "Retried Game", Hash: "rt-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: content,
	}})

	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{
		"status": "error", "title": "Retried Game", "info_hash": "rt-hash",
		"platform": "PC", "platform_slug": "", "is_pc": true,
		"error": "Cannot read the downloaded files at ...",
	})

	ok, msg := m.RetryJob(jobID)
	if !ok {
		t.Fatalf("RetryJob refused: %s", msg)
	}

	job := waitJobStatus(t, m.Jobs(), jobID, "completed", minPollTimeout)
	if !pathExists(filepath.Join(cfg.GamesVaultPath, "Retried Game [FitGirl Repack]", "setup.exe")) {
		t.Error("the retry reported success without importing anything")
	}
	if msg, _ := job["error"].(string); msg != "" {
		t.Errorf("completed job still carries error %q from the attempt that failed", msg)
	}
	if rc, _ := job["retry_count"].(float64); rc != 1 {
		t.Errorf("retry_count = %v, want 1", rc)
	}
	if n := len(m.Jobs().Items()); n != 1 {
		t.Errorf("job rows = %d, want the retry to reuse the one it was given", n)
	}
}

// A refusal has to say the true reason and leave the row where the UI still
// offers a button. Both cases end in a refusal either way, so the message is
// what distinguishes them: without the explicit check, a torrent the client has
// dropped is reported as one that has not finished downloading.
func TestRetryJobRefusalsLeaveTheRowActionable(t *testing.T) {
	tests := []struct {
		name     string
		torrents []qbit.Torrent
		wantMsg  string
	}{
		{"client no longer holds it", nil, "no longer holds"},
		{"download not finished", []qbit.Torrent{{Name: "P", Hash: "p-hash", Progress: 0.4}}, "has not finished"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qm := newQbitMock(t)
			qm.setTorrents(tt.torrents)
			cfg := newTestConfig(t)
			cfg.QBURL = "configured"
			m := New(cfg, newTestJobs(t), qm.client())

			jobID := newJobID()
			m.Jobs().Set(jobID, map[string]interface{}{
				"status": "error", "title": "P", "info_hash": "p-hash",
				"error": "Cannot read the downloaded files at ...",
			})

			ok, msg := m.RetryJob(jobID)
			if ok {
				t.Fatalf("retry started anyway: %s", msg)
			}
			if !strings.Contains(strings.ToLower(msg), tt.wantMsg) {
				t.Errorf("msg = %q, want containing %q", msg, tt.wantMsg)
			}
			job, _ := m.Jobs().Get(jobID)
			if status, _ := job["status"].(string); status != "error" {
				t.Errorf("status = %q, want the row left where the UI can still act on it", status)
			}
			if left, _ := job["error"].(string); left == "" {
				t.Error("the original error was cleared even though no retry started")
			}
		})
	}
}

// The give-up message tells the user to press Retry, so the row it writes has to
// be one Retry can act on. The hash reaches the job from a request parameter
// that is empty for any result carrying a .torrent URL, so recording it at the
// creation sites left whichever site nobody had patched yet still broken.
func TestGiveUpLeavesARowRetryCanActOn(t *testing.T) {
	setImportRetries(t, 2, time.Millisecond)
	qm := newQbitMock(t)
	cfg := newTestConfig(t)
	cfg.QBURL = "configured"
	m := New(cfg, newTestJobs(t), qm.client())

	torrent := qbit.Torrent{
		Name: "No Hash Recorded", Hash: "nh-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: filepath.Join(cfg.QBSavePath, "pending"),
	}
	qm.setTorrents([]qbit.Torrent{torrent})

	// The row as the download path writes it: no hash.
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{"status": "downloading", "title": torrent.Name})

	m.importFinishedTorrent("job watch", jobID, torrent, "PC", "", true)

	job, _ := m.Jobs().Get(jobID)
	if detail, _ := job["detail"].(string); !strings.Contains(detail, "Retry") {
		t.Fatalf("detail = %q, want it to point at Retry", detail)
	}
	if got, _ := job["info_hash"].(string); got != "nh-hash" {
		t.Errorf("info_hash = %q, want the torrent the message says to retry", got)
	}
	// Drop the torrent first so this refuses on the client rather than starting
	// an import: the point is which reason it gives, and a spawned import would
	// outlive the test and race the next one over the shared retry budget.
	qm.setTorrents(nil)
	if _, msg := m.RetryJob(jobID); strings.Contains(msg, "no torrent recorded") {
		t.Errorf("Retry refused the row its own message told the user to press: %s", msg)
	}
}

// A second click used to start a second import of one source. Whichever moved it
// first won; the loser stat'd the emptied path, read it as missing and overwrote
// the winner's completed row with a failure blaming the user's mounts.
//
// The status alone cannot say whether a retry is running: the import writes
// "error" on every failed attempt before writing "organizing" back, and that
// window is observable for a couple of percent of a retry's life, so a click
// landing in it reads a row that looks idle. The claim is the check.
func TestRetryJobRefusesASecondClickAndKeepsTheImportClean(t *testing.T) {
	setImportRetries(t, 400, 2*time.Second)
	qm := newQbitMock(t)
	cfg := newTestConfig(t)
	cfg.QBURL = "configured"
	m := New(cfg, newTestJobs(t), qm.client())

	content := filepath.Join(cfg.QBSavePath, "Clicked Twice [FitGirl Repack]")
	torrent := qbit.Torrent{
		Name: "Clicked Twice", Hash: "cc-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: content,
	}
	qm.setTorrents([]qbit.Torrent{torrent})
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{
		"status": "error", "title": "Clicked Twice", "info_hash": "cc-hash", "is_pc": true,
	})

	if ok, msg := m.RetryJob(jobID); !ok {
		t.Fatalf("first retry refused: %s", msg)
	}
	// Wait until the first retry is between attempts, so it will not overwrite
	// the row while the second click is made.
	waitFor(t, minPollTimeout, "the first retry to reach its wait", func() bool {
		job, _ := m.Jobs().Get(jobID)
		detail, _ := job["detail"].(string)
		return strings.Contains(detail, "Waiting for the download client")
	})

	// Put the row in the state the running import genuinely writes on a failed
	// attempt. This seeds the job row, not the claim, so the claim is still what
	// is under test.
	m.Jobs().Update(jobID, "status", "error")

	ok, msg := m.RetryJob(jobID)
	if ok {
		t.Fatal("a second retry started while the first was still running")
	}
	if !strings.Contains(msg, "already running") {
		t.Errorf("msg = %q, want it to say an import is already running", msg)
	}

	// The files land, so the first retry finishes on its own.
	writeFileT(t, filepath.Join(content, "setup.exe"), []byte("installer"))
	job := waitJobStatus(t, m.Jobs(), jobID, "completed", minPollTimeout)
	if left, _ := job["error"].(string); left != "" {
		t.Errorf("a completed import was overwritten with %q", left)
	}
	if rc, _ := job["retry_count"].(float64); rc != 1 {
		t.Errorf("retry_count = %v, want 1 after one accepted click", rc)
	}

	// The job reads completed before the import goroutine has unwound, so wait
	// for the claim to clear or the test's cleanup restores the retry budget
	// while that goroutine is still reading it.
	waitFor(t, minPollTimeout, "the retry to finish", func() bool {
		_, busy := m.importing.Load(torrent.Hash)
		return !busy
	})
}

// A Retry landing during a natural import has to be refused. The claim used to
// live in the caller, which covered two clicks and nothing else, so an import
// already running from the watcher or the job watch was invisible to it.
func TestRetryJobRefusesDuringANaturalImport(t *testing.T) {
	// Long enough between attempts that the row is not being rewritten while
	// the retry is attempted, short enough that the import gives up in seconds.
	setImportRetries(t, 3, 2*time.Second)
	qm := newQbitMock(t)
	cfg := newTestConfig(t)
	cfg.QBURL = "configured"
	m := New(cfg, newTestJobs(t), qm.client())

	torrent := qbit.Torrent{
		Name: "Natural Import", Hash: "ni-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: filepath.Join(cfg.QBSavePath, "pending"),
	}
	qm.setTorrents([]qbit.Torrent{torrent})
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{
		"status": "downloading", "title": torrent.Name, "info_hash": "ni-hash", "is_pc": true,
	})

	// A natural import, not a retry.
	go m.importFinishedTorrent("job watch", jobID, torrent, "PC", "", true)
	waitFor(t, minPollTimeout, "the natural import to reach its wait", func() bool {
		job, _ := m.Jobs().Get(jobID)
		detail, _ := job["detail"].(string)
		return strings.Contains(detail, "Waiting for the download client")
	})

	// The transient the import writes on every failed attempt.
	m.Jobs().Update(jobID, "status", "error")

	ok, msg := m.RetryJob(jobID)
	if ok {
		t.Fatal("a retry started on top of an import that was already running")
	}
	if !strings.Contains(msg, "already running") {
		t.Errorf("msg = %q, want it to say an import is already running", msg)
	}

	waitFor(t, minPollTimeout, "the natural import to finish", func() bool {
		_, busy := m.importing.Load(torrent.Hash)
		return !busy
	})
}

// A refused import must leave a row the user can still act on. Left at
// organizing it carries no button, counts as active and is never pruned, which
// is the state this has now been driven into from both directions.
func TestRefusedImportLeavesAnActionableRow(t *testing.T) {
	qm := newQbitMock(t)
	cfg := newTestConfig(t)
	cfg.QBURL = "configured"
	m := New(cfg, newTestJobs(t), qm.client())

	torrent := qbit.Torrent{
		Name: "One Download", Hash: "od-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: filepath.Join(cfg.QBSavePath, "pending"),
	}
	// A second job row naming the same physical download, carrying no hash of
	// its own - which is what DownloadTorrent writes whenever the search result
	// gave a .torrent URL rather than a magnet.
	second := newJobID()
	m.Jobs().Set(second, map[string]interface{}{"status": "organizing", "title": torrent.Name})

	m.importing.Store(torrent.Hash, struct{}{})
	defer m.importing.Delete(torrent.Hash)

	if m.importFinishedTorrent("manual organize", second, torrent, "PC", "", true) {
		t.Error("a second import ran for a download already being imported")
	}
	job, _ := m.Jobs().Get(second)
	if status, _ := job["status"].(string); status != "error" {
		t.Errorf("status = %q, want error so the row keeps a Retry button", status)
	}
	if detail, _ := job["detail"].(string); !strings.Contains(detail, "already running") {
		t.Errorf("detail = %q, want it to say why", detail)
	}
	// Actionable means the UI can offer the button, and it gates that on the
	// hash. A refusal that returned before recording it left error plus no
	// hash, which renders nothing and refuses if reached any other way.
	if got, _ := job["info_hash"].(string); got != torrent.Hash {
		t.Errorf("info_hash = %q, want %q so the row keeps a usable Retry", got, torrent.Hash)
	}
}
