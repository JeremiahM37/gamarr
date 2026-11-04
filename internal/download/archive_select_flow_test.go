package download

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gamarr/internal/qbit"
)

const (
	archiveHash   = "5407ec50c81f04cacb86159c97fc5272679f6657"
	archiveMagnet = "magnet:?xt=urn:btih:" + archiveHash + "&dn=Minerva_Myrient"
	archiveTarget = "Wanted Game (USA).zip"
)

// archiveQbit models the qBittorrent behaviour selection depends on: a magnet
// reports no files until metadata arrives, and priorities change only through
// filePrio. The assertion every failure case makes is on started: an archive
// that was started has been allowed to download all of itself.
type archiveQbit struct {
	mu            sync.Mutex
	files         []qbit.TorrentFile
	noMetadata    bool
	ignorePrio    bool
	failPrio      bool
	failStart     bool
	started       bool
	startPrios    map[int]int
	addCalls      int
	stopCondition string
}

func newArchiveQbit(t *testing.T, names ...string) (*archiveQbit, *httptest.Server) {
	t.Helper()
	q := &archiveQbit{}
	for i, n := range names {
		q.files = append(q.files, qbit.TorrentFile{Name: "No-Intro/GB/" + n, Index: i, Priority: 1})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		defer q.mu.Unlock()
		r.ParseForm()
		switch r.URL.Path {
		case "/api/v2/auth/login":
			w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			q.addCalls++
			q.stopCondition = r.Form.Get("stopCondition")
			w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			w.Write([]byte("[]"))
		case "/api/v2/torrents/files":
			if q.noMetadata {
				w.Write([]byte("[]"))
				return
			}
			json.NewEncoder(w).Encode(q.files)
		case "/api/v2/torrents/filePrio":
			if q.failPrio {
				w.WriteHeader(500)
				return
			}
			if q.ignorePrio {
				return
			}
			prio, _ := strconv.Atoi(r.Form.Get("priority"))
			for _, id := range strings.Split(r.Form.Get("id"), "|") {
				i, _ := strconv.Atoi(id)
				q.files[i].Priority = prio
			}
		case "/api/v2/torrents/start":
			if q.failStart {
				w.WriteHeader(500)
				return
			}
			q.started = true
			q.startPrios = map[int]int{}
			for _, f := range q.files {
				q.startPrios[f.Index] = f.Priority
			}
		}
	}))
	t.Cleanup(srv.Close)
	return q, srv
}

func fastArchiveTimers(t *testing.T) {
	t.Helper()
	timeout, poll, attempts, verifyPoll := archiveMetadataTimeout, archiveMetadataPoll, archiveVerifyAttempts, archiveVerifyPoll
	archiveMetadataTimeout, archiveMetadataPoll, archiveVerifyAttempts, archiveVerifyPoll = 50*time.Millisecond, 5*time.Millisecond, 2, time.Millisecond
	t.Cleanup(func() {
		archiveMetadataTimeout, archiveMetadataPoll, archiveVerifyAttempts, archiveVerifyPoll = timeout, poll, attempts, verifyPoll
	})
}

func startArchiveJob(t *testing.T, srv *httptest.Server, title string) (*Manager, string) {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.QBURL = srv.URL
	m := New(cfg, newTestJobs(t), qbit.New(srv.URL, "admin", "pass"))
	jobID, err := m.DownloadTorrent(archiveMagnet, archiveHash, title, "Game Boy", "gb", false, true)
	if err != nil {
		t.Fatalf("DownloadTorrent: %v", err)
	}
	return m, jobID
}

func waitArchiveJobError(t *testing.T, m *Manager, jobID, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if row, ok := m.jobs.Get(jobID); ok && row["status"] == "error" {
			got, _ := row["error"].(string)
			if !strings.Contains(got, want) {
				t.Fatalf("error = %q, want it to mention %q", got, want)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	row, _ := m.jobs.Get(jobID)
	t.Fatalf("job never failed: %v", row)
}

func TestArchiveSelection_FailuresNeverStartTheTorrent(t *testing.T) {
	tests := []struct {
		name  string
		title string
		setup func(*archiveQbit)
		want  string
	}{
		{"metadata never arrives", archiveTarget, func(q *archiveQbit) { q.noMetadata = true }, "no file list"},
		{"no file matches the title", "Some Other Game.zip", nil, "no file matching"},
		{"priorities are rejected", archiveTarget, func(q *archiveQbit) { q.failPrio = true }, "failed to skip"},
		{"priorities are silently not applied", archiveTarget, func(q *archiveQbit) { q.ignorePrio = true }, "after selection"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fastArchiveTimers(t)
			q, srv := newArchiveQbit(t, "Other (USA).zip", archiveTarget, "Another (Japan).zip")
			if tt.setup != nil {
				tt.setup(q)
			}
			m, jobID := startArchiveJob(t, srv, tt.title)
			waitArchiveJobError(t, m, jobID, tt.want)

			q.mu.Lock()
			defer q.mu.Unlock()
			if q.started {
				t.Fatal("the archive was started despite the failed selection")
			}
			if q.stopCondition != "MetadataReceived" {
				t.Fatalf("stopCondition = %q", q.stopCondition)
			}
		})
	}
}

func TestArchiveSelection_StartFailureFailsTheJob(t *testing.T) {
	fastArchiveTimers(t)
	q, srv := newArchiveQbit(t, "Other (USA).zip", archiveTarget)
	q.failStart = true
	m, jobID := startArchiveJob(t, srv, archiveTarget)
	waitArchiveJobError(t, m, jobID, "refused to start")
}

func TestArchiveSelection_StartsWithOnlyTheTargetEnabled(t *testing.T) {
	fastArchiveTimers(t)
	q, srv := newArchiveQbit(t, "Other (USA).zip", archiveTarget, "Another (Japan).zip")
	startArchiveJob(t, srv, archiveTarget)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		started := q.started
		q.mu.Unlock()
		if started {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.started {
		t.Fatal("the archive was never started")
	}
	want := map[int]int{0: 0, 1: 1, 2: 0}
	for idx, prio := range want {
		if q.startPrios[idx] != prio {
			t.Fatalf("priorities at start = %v, want %v", q.startPrios, want)
		}
	}
}

func TestArchiveDownload_NeverFallsBackToAnotherClient(t *testing.T) {
	q, srv := newArchiveQbit(t, archiveTarget)
	var delugeCalls int
	deluge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delugeCalls++
		w.Write([]byte(`{"id":1,"result":true,"error":null}`))
	}))
	defer deluge.Close()

	cfg := newTestConfig(t)
	cfg.DelugeURL = deluge.URL
	m := New(cfg, newTestJobs(t), qbit.New(srv.URL, "admin", "pass"))
	jobID, err := m.DownloadTorrent(archiveMagnet, archiveHash, archiveTarget, "Game Boy", "gb", false, true)
	if err != nil {
		t.Fatalf("DownloadTorrent: %v", err)
	}
	row, _ := m.jobs.Get(jobID)
	if row["status"] != "error" || !strings.Contains(row["error"].(string), "need qBittorrent") {
		t.Fatalf("job = %v", row)
	}
	if delugeCalls != 0 {
		t.Fatalf("Deluge was sent a whole archive (%d calls)", delugeCalls)
	}
	if q.addCalls != 0 {
		t.Fatalf("qBittorrent is not configured but got %d adds", q.addCalls)
	}
}
