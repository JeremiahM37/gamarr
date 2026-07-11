package download

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gamarr/internal/config"
	"gamarr/internal/db"
	"gamarr/internal/qbit"
)

// newTestConfig creates a Config wired to temp directories with all external
// integrations (ClamAV, Docker) pointed at nonexistent paths so scans are
// skipped deterministically.
func newTestConfig(t *testing.T) *config.Config {
	t.Helper()
	base := t.TempDir()
	staging := filepath.Join(base, "staging")
	vault := filepath.Join(base, "vault")
	roms := filepath.Join(base, "roms")
	data := filepath.Join(base, "data")
	for _, d := range []string{staging, vault, roms, data} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	return &config.Config{
		QBSavePath:      staging,
		QBCategory:      "games",
		GamesVaultPath:  vault,
		GamesRomsPath:   roms,
		DataDir:         data,
		ClamAVContainer: "clamav",
		ClamAVSocket:    filepath.Join(base, "no-such-clamav.sock"),
		DockerSocket:    filepath.Join(base, "no-such-docker.sock"),
		MaxRetries:      2,
	}
}

// newTestJobs creates a JobStore backed by a temp SQLite file.
func newTestJobs(t *testing.T) *db.JobStore {
	t.Helper()
	jobs, err := db.New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { jobs.Close() })
	return jobs
}

// qbitMock is a fake qBittorrent WebUI server.
type qbitMock struct {
	srv *httptest.Server

	mu       sync.Mutex
	torrents []qbit.Torrent
	files    []qbit.TorrentFile
	loginOK  bool
	addOK    bool
	addCalls int
	deleted  []string
}

func newQbitMock(t *testing.T) *qbitMock {
	t.Helper()
	q := &qbitMock{loginOK: true, addOK: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		ok := q.loginOK
		q.mu.Unlock()
		if ok {
			w.Write([]byte("Ok."))
		} else {
			w.Write([]byte("Fails."))
		}
	})
	mux.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		q.addCalls++
		ok := q.addOK
		q.mu.Unlock()
		if ok {
			w.Write([]byte("Ok."))
		} else {
			w.Write([]byte("Fails."))
		}
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		list := make([]qbit.Torrent, len(q.torrents))
		copy(list, q.torrents)
		q.mu.Unlock()
		json.NewEncoder(w).Encode(list)
	})
	mux.HandleFunc("/api/v2/torrents/files", func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		list := make([]qbit.TorrentFile, len(q.files))
		copy(list, q.files)
		q.mu.Unlock()
		json.NewEncoder(w).Encode(list)
	})
	mux.HandleFunc("/api/v2/torrents/delete", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		q.mu.Lock()
		q.deleted = append(q.deleted, r.Form.Get("hashes"))
		q.mu.Unlock()
		w.WriteHeader(200)
	})
	q.srv = httptest.NewServer(mux)
	t.Cleanup(q.srv.Close)
	return q
}

func (q *qbitMock) client() *qbit.Client {
	return qbit.New(q.srv.URL, "user", "pass")
}

func (q *qbitMock) setTorrents(ts []qbit.Torrent) {
	q.mu.Lock()
	q.torrents = ts
	q.mu.Unlock()
}

func (q *qbitMock) setFiles(fs []qbit.TorrentFile) {
	q.mu.Lock()
	q.files = fs
	q.mu.Unlock()
}

func (q *qbitMock) deletedHashes() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]string, len(q.deleted))
	copy(out, q.deleted)
	return out
}

// jobFromDB reads a job's persisted state directly from SQLite, verifying the
// write-through path. (JobStore.Get now returns a copy, so reading the map
// directly is also race-safe; this helper additionally checks persistence.)
func jobFromDB(t *testing.T, jobs *db.JobStore, jobID string) (map[string]interface{}, bool) {
	t.Helper()
	var data string
	err := jobs.DB().QueryRow("SELECT data FROM jobs WHERE job_id = ?", jobID).Scan(&data)
	if err != nil {
		return nil, false
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return nil, false
	}
	return m, true
}

// waitJobStatus polls the persisted job until it reaches the wanted status.
func waitJobStatus(t *testing.T, jobs *db.JobStore, jobID, want string, timeout time.Duration) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		if m, ok := jobFromDB(t, jobs, jobID); ok {
			if s, _ := m["status"].(string); s == want {
				return m
			} else {
				last = s
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s never reached status %q (last %q)", jobID, want, last)
	return nil
}

// waitFor polls until cond returns true.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// writeFileT writes a file, creating parent dirs, and fails the test on error.
func writeFileT(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
