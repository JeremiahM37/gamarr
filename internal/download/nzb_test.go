package download

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		{
			name: "error job requeued",
			setup: func(jobID string, m *Manager) {
				m.Jobs().Set(jobID, map[string]interface{}{"status": "error", "title": "G"})
			},
			wantOK:     true,
			wantMsg:    "retry #1",
			wantStatus: "queued",
		},
		{
			name: "interrupted job requeued with count",
			setup: func(jobID string, m *Manager) {
				m.Jobs().Set(jobID, map[string]interface{}{
					"status": "interrupted", "title": "G", "retry_count": float64(2),
				})
			},
			wantOK:     true,
			wantMsg:    "retry #3",
			wantStatus: "queued",
		},
		{
			name: "dead letter job requeued",
			setup: func(jobID string, m *Manager) {
				m.Jobs().Set(jobID, map[string]interface{}{"status": "dead_letter", "title": "G"})
			},
			wantOK:     true,
			wantStatus: "queued",
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
				if job["error"] != nil {
					t.Errorf("error = %v, want nil after retry", job["error"])
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
