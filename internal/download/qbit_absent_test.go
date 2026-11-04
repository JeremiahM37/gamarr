package download

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gamarr/internal/qbit"
)

// With QB_URL empty and Deluge configured, nothing may reach for qBittorrent:
// not the duplicate check before the add, and not a watcher after it.
func TestDownloadTorrent_DelugeOnlyNeverContactsQBittorrent(t *testing.T) {
	var qbCalls atomic.Int64
	qb := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		qbCalls.Add(1)
	}))
	defer qb.Close()
	deluge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":1,"result":"hash123","error":null}`)
	}))
	defer deluge.Close()

	cfg := newTestConfig(t)
	cfg.DelugeURL = deluge.URL
	// The client exists whatever the config says; only the config says not to use it.
	m := New(cfg, newTestJobs(t), qbit.New(qb.URL, "admin", "pass"))

	jobID, err := m.DownloadTorrent("magnet:?xt=urn:btih:abc", "abc", "Game", "Xbox 360", "xbox360", false, false)
	if err != nil {
		t.Fatalf("DownloadTorrent: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := qbCalls.Load(); n != 0 {
		t.Fatalf("qBittorrent contacted %d times", n)
	}
	row, _ := m.jobs.Get(jobID)
	if detail, _ := row["detail"].(string); !strings.Contains(detail, "Sent to Deluge") {
		t.Fatalf("detail = %q", detail)
	}
}
