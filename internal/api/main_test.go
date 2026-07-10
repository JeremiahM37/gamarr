package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gamarr/internal/config"
	"gamarr/internal/db"
	"gamarr/internal/download"
	"gamarr/internal/qbit"
	"gamarr/internal/sabnzbd"
	"gamarr/internal/sources"
)

// Silence slog during test runs — production code paths intentionally emit
// INFO/WARN logs that look like noise in `go test -v` output but are not
// test failures.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

// testEnv wires up a full Server + router against a real SQLite JobStore in
// a temp dir. External services (qBittorrent, Prowlarr, SABnzbd, webhooks)
// are either unconfigured (empty URLs → fast local failure, no network) or
// pointed at httptest mock servers by the caller via the mutate callback.
type testEnv struct {
	t      *testing.T
	cfg    *config.Config
	jobs   *db.JobStore
	mgr    *download.Manager
	router http.Handler
}

// newTestEnv builds a router with everything unconfigured by default:
// no auth, no API key, no users → the auth middleware passes every request
// through as admin. mutate may adjust the config before wiring.
func newTestEnv(t *testing.T, mutate func(*config.Config)) *testEnv {
	t.Helper()

	cfg := &config.Config{
		Sources:        sources.Load("", ""),
		DataDir:        t.TempDir(),
		QBCategory:     "games",
		MetricsEnabled: true,
	}
	if mutate != nil {
		mutate(cfg)
	}

	store, err := db.New(filepath.Join(t.TempDir(), "gamarr.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	qb := qbit.New(cfg.QBURL, cfg.QBUser, cfg.QBPass)
	mgr := download.New(cfg, store, qb)

	var sab *sabnzbd.Client
	if cfg.HasSABnzbd() {
		sab = sabnzbd.New(cfg.SABnzbdURL, cfg.SABnzbdAPIKey)
	}

	router := NewRouter(cfg, mgr, nil, sab, nil)
	return &testEnv{t: t, cfg: cfg, jobs: store, mgr: mgr, router: router}
}

// reqOpt mutates an outgoing test request.
type reqOpt func(*http.Request)

func withHeader(key, value string) reqOpt {
	return func(r *http.Request) { r.Header.Set(key, value) }
}

func withSession(token string) reqOpt {
	return func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "gamarr_session", Value: token})
	}
}

// do runs a request through the full middleware chain + router. A non-empty
// body defaults the Content-Type to application/json (override via opts).
func (e *testEnv) do(method, path, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}
	rr := httptest.NewRecorder()
	e.router.ServeHTTP(rr, req)
	return rr
}

// decodeMap parses a JSON object response body.
func decodeMap(t *testing.T, rr *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not a JSON object: %v\nbody: %s", err, rr.Body.String())
	}
	return m
}

// wantStatus fails the test when the recorded status differs.
func wantStatus(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, want, rr.Body.String())
	}
}
