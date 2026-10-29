package download

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"gamarr/internal/sources"
)

func shortStallTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := downloadStallTimeout
	downloadStallTimeout = d
	t.Cleanup(func() { downloadStallTimeout = orig })
}

// writeSlowly sends chunks with a gap after each, so the whole body takes
// several stall windows to arrive while no single gap reaches one.
func writeSlowly(w http.ResponseWriter, chunks int, gap time.Duration) {
	w.Header().Set("Content-Length", fmt.Sprint(chunks*len("chunk")))
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)
	for i := 0; i < chunks; i++ {
		_, _ = io.WriteString(w, "chunk")
		flusher.Flush()
		time.Sleep(gap)
	}
}

// writeThenStall declares a body it never finishes and holds the connection
// open until the client gives up.
func writeThenStall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Length", "1000")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "only-a-few-bytes")
	w.(http.Flusher).Flush()
	select {
	case <-r.Context().Done():
	case <-time.After(5 * time.Second):
	}
}

func TestDownloadDDLSlowStreamOutlastsStallWindow(t *testing.T) {
	shortStallTimeout(t, 150*time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="Slow.iso"`)
		writeSlowly(w, 10, 50*time.Millisecond)
	}))
	defer srv.Close()

	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	got, err := m.downloadDDL(srv.URL+"/dl", t.TempDir(), jobID)
	if err != nil {
		t.Fatalf("a steady transfer longer than the stall window failed: %v", err)
	}
	body, _ := os.ReadFile(got)
	if string(body) != strings.Repeat("chunk", 10) {
		t.Errorf("file contents = %q", body)
	}
}

func TestDownloadDDLStalledStreamIsError(t *testing.T) {
	shortStallTimeout(t, 150*time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="Stalled.iso"`)
		writeThenStall(w, r)
	}))
	defer srv.Close()

	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	dest := t.TempDir()
	start := time.Now()
	got, err := m.downloadDDL(srv.URL+"/dl", dest, jobID)
	if err == nil || !strings.Contains(err.Error(), "no data for") {
		t.Fatalf("err = %v, want a stall error", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("stall detected after %s, want about the stall window", elapsed)
	}
	if got != "" {
		t.Errorf("path = %q, want empty", got)
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 0 {
		t.Errorf("stalled download left %d file(s) behind", len(entries))
	}
}

func TestDownloadDDLNoResponseIsError(t *testing.T) {
	shortStallTimeout(t, 150*time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	_, err := m.downloadDDL(srv.URL+"/dl", t.TempDir(), jobID)
	if err == nil || !strings.Contains(err.Error(), "no response from server") {
		t.Fatalf("err = %v, want a no-response error", err)
	}
}

func newVimmStreamManager(t *testing.T, stream http.HandlerFunc) (*Manager, func(string) map[string]interface{}) {
	t.Helper()
	orig := vimmDownloadPause
	vimmDownloadPause = 0
	t.Cleanup(func() { vimmDownloadPause = orig })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/vault/"):
			_, _ = io.WriteString(w, vimmGamePageHTML)
		case r.URL.Path == "/download":
			w.Header().Set("Content-Disposition", `attachment; filename="Disc.7z"`)
			w.Header().Set("Content-Type", "application/x-7z-compressed")
			stream(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	cfg := newTestConfig(t)
	reg, err := sources.Default()
	if err != nil {
		t.Fatal(err)
	}
	reg.Vimm.BaseURL = srv.URL + "/vault/"
	cfg.Sources = reg
	jobs := newTestJobs(t)
	return New(cfg, jobs, nil), func(jobID string) map[string]interface{} {
		job, _ := jobs.Get(jobID)
		return job
	}
}

func TestDownloadVimmGame_SlowStreamOutlastsStallWindow(t *testing.T) {
	shortStallTimeout(t, 150*time.Millisecond)
	m, job := newVimmStreamManager(t, func(w http.ResponseWriter, r *http.Request) {
		writeSlowly(w, 10, 50*time.Millisecond)
	})
	jobID := newJobID()
	m.jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	got := m.downloadVimmGame("7571", m.cfg.QBSavePath, jobID)
	if got == "" {
		t.Fatalf("a steady Vimm transfer longer than the stall window failed: %+v", job(jobID))
	}
	body, _ := os.ReadFile(got)
	if string(body) != strings.Repeat("chunk", 10) {
		t.Errorf("file contents = %q", body)
	}
}

func TestDownloadVimmGame_StalledStreamIsError(t *testing.T) {
	shortStallTimeout(t, 150*time.Millisecond)
	m, job := newVimmStreamManager(t, writeThenStall)
	jobID := newJobID()
	m.jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	if got := m.downloadVimmGame("7571", m.cfg.QBSavePath, jobID); got != "" {
		t.Fatalf("stalled stream produced a file: %q", got)
	}
	j := job(jobID)
	errMsg, _ := j["error"].(string)
	if j["status"] != "error" || !strings.HasPrefix(errMsg, "Vimm download stalled: no data for") {
		t.Errorf("job = %+v, want a stall error", j)
	}
}
