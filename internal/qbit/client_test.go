package qbit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestNew(t *testing.T) {
	c := New("http://localhost:8080", "admin", "pass")
	if c.baseURL != "http://localhost:8080" {
		t.Errorf("baseURL=%q", c.baseURL)
	}
	if c.user != "admin" {
		t.Errorf("user=%q", c.user)
	}
	if c.authenticated {
		t.Error("should not be authenticated initially")
	}
}

func TestNew_TrailingSlash(t *testing.T) {
	c := New("http://localhost:8080/", "admin", "pass")
	if c.baseURL != "http://localhost:8080" {
		t.Errorf("expected trailing slash stripped, got %q", c.baseURL)
	}
}

func TestLogin_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.Write([]byte("Ok."))
			return
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	if !c.Login() {
		t.Error("expected login to succeed")
	}
	if !c.authenticated {
		t.Error("expected authenticated=true after login")
	}
}

func TestLogin_Success_NoContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	if !c.Login() {
		t.Error("expected login to succeed on 204 with empty body")
	}
}

func TestLogin_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Fails."))
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "wrongpass")
	if c.Login() {
		t.Error("expected login to fail")
	}
}

func TestAddTorrent_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.Write([]byte("Ok."))
			return
		}
		if r.URL.Path == "/api/v2/torrents/add" {
			w.Write([]byte("Ok."))
			return
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	ok := c.AddTorrent("magnet:?xt=urn:btih:abc", "Test", "/downloads", "games")
	if !ok {
		t.Error("expected AddTorrent to succeed")
	}
}

func TestAddTorrent_ReauthOn403(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.Write([]byte("Ok."))
			return
		}
		if r.URL.Path == "/api/v2/torrents/add" {
			callCount++
			if callCount == 1 {
				w.WriteHeader(403)
				return
			}
			w.Write([]byte("Ok."))
			return
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	c.authenticated = true
	ok := c.AddTorrent("magnet:?xt=urn:btih:abc", "Test", "/downloads", "games")
	if !ok {
		t.Error("expected AddTorrent to succeed after reauth")
	}
}

func TestAddTorrentPaused_Contract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			if r.Method != http.MethodPost {
				t.Errorf("method=%s, want POST", r.Method)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			for key, want := range map[string]string{
				"urls":     "magnet:?xt=urn:btih:abc",
				"savepath": "/downloads",
				"category": "games",
				"stopped":  "true",
				"paused":   "true",
			} {
				if got := r.Form.Get(key); got != want {
					t.Errorf("%s=%q, want %q", key, got, want)
				}
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	if !c.AddTorrentPaused("magnet:?xt=urn:btih:abc", "Test", "/downloads", "games") {
		t.Fatal("expected paused add to accept a 204 response")
	}
}

func TestAddTorrentPaused_ReauthOn403(t *testing.T) {
	adds := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			adds++
			if adds == 1 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Write([]byte("Ok."))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	c.authenticated = true
	if !c.AddTorrentPaused("magnet:?xt=urn:btih:abc", "Test", "/downloads", "games") {
		t.Fatal("expected paused add to retry after a 403")
	}
	if adds != 2 {
		t.Errorf("add attempts=%d, want 2", adds)
	}
}

func TestAddTorrentPaused_QBit52JSONResponses(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "accepted",
			body: `{"added_torrent_ids":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],"failure_count":0,"pending_count":0,"success_count":1}`,
			want: true,
		},
		{
			name: "rejected",
			body: `{"added_torrent_ids":[],"failure_count":1,"pending_count":0,"success_count":0}`,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					w.WriteHeader(http.StatusNoContent)
				case "/api/v2/torrents/add":
					w.Header().Set("Content-Type", "application/json")
					w.Write([]byte(tc.body))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			c := New(srv.URL, "admin", "pass")
			if got := c.AddTorrentPaused("magnet:?xt=urn:btih:abc", "Test", "/downloads", "games"); got != tc.want {
				t.Errorf("AddTorrentPaused()=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestGetTorrents(t *testing.T) {
	torrents := []Torrent{
		{Name: "Game1", Hash: "abc", Progress: 0.5, State: "downloading"},
		{Name: "Game2", Hash: "def", Progress: 1.0, State: "stoppedUP"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.Write([]byte("Ok."))
			return
		}
		if r.URL.Path == "/api/v2/torrents/info" {
			json.NewEncoder(w).Encode(torrents)
			return
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	result, err := c.GetTorrents("games")
	if err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 torrents, got %d", len(result))
	}
	if result[0].Name != "Game1" {
		t.Errorf("name=%q", result[0].Name)
	}
}

func TestGetTorrents_ReauthOn403(t *testing.T) {
	callCount := 0
	torrents := []Torrent{{Name: "Game1", Hash: "abc"}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.Write([]byte("Ok."))
			return
		}
		if r.URL.Path == "/api/v2/torrents/info" {
			callCount++
			if callCount == 1 {
				w.WriteHeader(403)
				return
			}
			json.NewEncoder(w).Encode(torrents)
			return
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	c.authenticated = true
	result, err := c.GetTorrents("")
	if err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 torrent after reauth, got %d", len(result))
	}
}

func TestGetTorrentFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.Write([]byte("Ok."))
			return
		}
		if r.URL.Path == "/api/v2/torrents/files" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"name":"game/setup.exe","size":42,"priority":1,"index":3,"progress":0.75},{"name":"game/data.bin","size":0,"priority":0,"index":4,"progress":0}]`))
			return
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	result := c.GetTorrentFiles("abc123")
	if len(result) != 2 {
		t.Fatalf("expected 2 files, got %d", len(result))
	}
	if result[0].Name != "game/setup.exe" {
		t.Errorf("name=%q", result[0].Name)
	}
	if result[0].Size != 42 {
		t.Errorf("size=%d, want 42", result[0].Size)
	}
	if result[0].Priority != 1 {
		t.Errorf("priority=%d, want 1", result[0].Priority)
	}
	if result[0].Index != 3 {
		t.Errorf("index=%d, want 3", result[0].Index)
	}
	if result[0].Progress != 0.75 {
		t.Errorf("progress=%v, want 0.75", result[0].Progress)
	}
}

func TestSetFilePriority_Contract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			w.Write([]byte("Ok."))
		case "/api/v2/torrents/filePrio":
			if r.Method != http.MethodPost {
				t.Errorf("method=%s, want POST", r.Method)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			for key, want := range map[string]string{"hash": "abc123", "id": "0|1|2", "priority": "0"} {
				if got := r.Form.Get(key); got != want {
					t.Errorf("%s=%q, want %q", key, got, want)
				}
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	if !c.SetFilePriority("abc123", []int{0, 1, 2}, 0) {
		t.Fatal("expected file priority to accept a 204 response")
	}
}

func TestSetFilePriority_RejectsEmptyIDs(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	c.authenticated = true
	if c.SetFilePriority("abc123", nil, 0) {
		t.Fatal("expected an empty id list to be rejected")
	}
	if requests != 0 {
		t.Errorf("requests=%d, want 0 for empty ids", requests)
	}
}

func TestSetFilePriority_ReauthOn403(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			w.Write([]byte("Ok."))
		case "/api/v2/torrents/filePrio":
			attempts++
			if attempts == 1 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	c.authenticated = true
	if !c.SetFilePriority("abc123", []int{4}, 1) {
		t.Fatal("expected file priority to retry after a 403")
	}
	if attempts != 2 {
		t.Errorf("file priority attempts=%d, want 2", attempts)
	}
}

func TestDeleteTorrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.Write([]byte("Ok."))
			return
		}
		if r.URL.Path == "/api/v2/torrents/delete" {
			w.WriteHeader(200)
			return
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	ok := c.DeleteTorrent("abc123", true)
	if !ok {
		t.Error("expected DeleteTorrent to succeed")
	}
}

func TestDeleteTorrent_ReauthOn403(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.Write([]byte("Ok."))
			return
		}
		if r.URL.Path == "/api/v2/torrents/delete" {
			callCount++
			if callCount == 1 {
				w.WriteHeader(403)
				return
			}
			w.WriteHeader(200)
			return
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	c.authenticated = true
	ok := c.DeleteTorrent("abc123", false)
	if !ok {
		t.Error("expected DeleteTorrent to succeed after reauth")
	}
}

// ── qBittorrent >= 5.2 WebAPI responses (issue #11) ────────────────────────────
// qBittorrent 5.2.0 changed the WebAPI to send 204 for responses with no
// body: login success became 204/empty (was 200 "Ok."), bad credentials
// became 401 (was 200 "Fails."), torrents/add success became 200 with a JSON
// body (was 200 "Ok."), and rejected adds became 409. Response shapes below
// were captured verbatim from qBittorrent 5.2.3.

func qbit52Server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			if r.FormValue("password") == "goodpass" {
				w.WriteHeader(http.StatusNoContent)
			} else {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte("Unauthorized"))
			}
		case "/api/v2/torrents/add":
			if r.FormValue("urls") == "" {
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte("Conflict"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"added_torrent_ids":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],"failure_count":0,"pending_count":0,"success_count":1}`))
		case "/api/v2/torrents/delete":
			w.WriteHeader(http.StatusNoContent)
		}
	}))
}

func TestLogin_QBit52_Success(t *testing.T) {
	srv := qbit52Server(t)
	defer srv.Close()

	c := New(srv.URL, "admin", "goodpass")
	if !c.Login() {
		t.Error("expected login to succeed on 5.2-style 204 response")
	}
}

func TestLogin_QBit52_BadCredentials(t *testing.T) {
	srv := qbit52Server(t)
	defer srv.Close()

	c := New(srv.URL, "admin", "wrongpass")
	if c.Login() {
		t.Error("expected login to fail on 5.2-style 401 response")
	}
}

func TestAddTorrent_QBit52_JSONResponse(t *testing.T) {
	srv := qbit52Server(t)
	defer srv.Close()

	c := New(srv.URL, "admin", "goodpass")
	if !c.AddTorrent("magnet:?xt=urn:btih:abc", "Test", "/downloads", "games") {
		t.Error("expected AddTorrent to succeed on 5.2-style JSON response")
	}
}

func TestAddTorrent_QBit52_Rejected409(t *testing.T) {
	srv := qbit52Server(t)
	defer srv.Close()

	c := New(srv.URL, "admin", "goodpass")
	if c.AddTorrent("", "Test", "/downloads", "games") {
		t.Error("expected AddTorrent to fail on 5.2-style 409 response")
	}
}

func TestDeleteTorrent_QBit52_NoContent(t *testing.T) {
	srv := qbit52Server(t)
	defer srv.Close()

	c := New(srv.URL, "admin", "goodpass")
	if !c.DeleteTorrent("abc123", true) {
		t.Error("expected DeleteTorrent to succeed on a 204 response")
	}
}

func TestAddAccepted(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"legacy Ok", 200, "Ok.", true},
		{"legacy Fails", 200, "Fails.", false},
		{"52 json success", 200, `{"added_torrent_ids":["a"],"failure_count":0,"pending_count":0,"success_count":1}`, true},
		{"52 json pending", 200, `{"added_torrent_ids":[],"failure_count":0,"pending_count":1,"success_count":0}`, true},
		{"52 json all failed", 200, `{"added_torrent_ids":[],"failure_count":1,"pending_count":0,"success_count":0}`, false},
		{"52 conflict", 409, "Conflict", false},
		{"bare 204", 204, "", true},
		{"server error", 500, "", false},
	}
	for _, tc := range cases {
		if got := addAccepted(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("%s: addAccepted(%d, %q)=%v, want %v", tc.name, tc.status, tc.body, got, tc.want)
		}
	}
}

func TestStopTorrent(t *testing.T) {
	var gotHashes string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.Write([]byte("Ok."))
			return
		}
		if r.URL.Path == "/api/v2/torrents/stop" {
			r.ParseForm()
			gotHashes = r.Form.Get("hashes")
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	if !c.StopTorrent("abc123") {
		t.Error("expected StopTorrent to succeed")
	}
	if gotHashes != "abc123" {
		t.Errorf("hashes=%q", gotHashes)
	}
}

// qBittorrent < 5.0 only has torrents/pause.
func TestStopTorrent_FallsBackToPause(t *testing.T) {
	paused := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.Write([]byte("Ok."))
			return
		}
		if r.URL.Path == "/api/v2/torrents/pause" {
			paused = true
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	if !c.StopTorrent("abc123") {
		t.Error("expected StopTorrent to fall back to pause")
	}
	if !paused {
		t.Error("pause endpoint was never called")
	}
}

func TestStopTorrent_ReauthOn403(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.Write([]byte("Ok."))
			return
		}
		if r.URL.Path == "/api/v2/torrents/stop" {
			callCount++
			if callCount == 1 {
				w.WriteHeader(403)
				return
			}
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	c.authenticated = true
	if !c.StopTorrent("abc123") {
		t.Error("expected StopTorrent to succeed after reauth")
	}
}

func TestStartTorrent_Contract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			w.Write([]byte("Ok."))
		case "/api/v2/torrents/start":
			if r.Method != http.MethodPost {
				t.Errorf("method=%s, want POST", r.Method)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			if got := r.Form.Get("hashes"); got != "abc123" {
				t.Errorf("hashes=%q, want abc123", got)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	if !c.StartTorrent("abc123") {
		t.Fatal("expected start to accept a 201 response")
	}
}

func TestStartTorrent_FallsBackToResumeOn404(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			w.Write([]byte("Ok."))
		case "/api/v2/torrents/start", "/api/v2/torrents/resume":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			if got := r.Form.Get("hashes"); got != "abc123" {
				t.Errorf("%s hashes=%q, want abc123", r.URL.Path, got)
			}
			paths = append(paths, r.URL.Path)
			if r.URL.Path == "/api/v2/torrents/start" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	if !c.StartTorrent("abc123") {
		t.Fatal("expected start to fall back to resume")
	}
	if got, want := len(paths), 2; got != want {
		t.Fatalf("endpoint calls=%d, want %d", got, want)
	}
	if paths[0] != "/api/v2/torrents/start" || paths[1] != "/api/v2/torrents/resume" {
		t.Errorf("endpoint order=%v, want [start resume]", paths)
	}
}

func TestStartTorrent_DoesNotResumeOnNon404Failure(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			w.Write([]byte("Ok."))
		case "/api/v2/torrents/start":
			paths = append(paths, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		case "/api/v2/torrents/resume":
			paths = append(paths, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	if c.StartTorrent("abc123") {
		t.Fatal("expected start to fail on a 500 response")
	}
	if got, want := len(paths), 1; got != want {
		t.Fatalf("endpoint calls=%d, want %d (%v)", got, want, paths)
	}
	if paths[0] != "/api/v2/torrents/start" {
		t.Errorf("endpoint path=%q, want /api/v2/torrents/start", paths[0])
	}
}

func TestStartTorrent_ReauthOn403(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			w.Write([]byte("Ok."))
		case "/api/v2/torrents/start":
			attempts++
			if attempts == 1 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pass")
	c.authenticated = true
	if !c.StartTorrent("abc123") {
		t.Fatal("expected start to retry after a 403")
	}
	if attempts != 2 {
		t.Errorf("start attempts=%d, want 2", attempts)
	}
}

func TestLogin_APIKey(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			t.Error("API key auth must not call /auth/login")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path == "/api/v2/app/version" {
			sawAuth = r.Header.Get("Authorization")
			w.Write([]byte("v5.2.0"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewWithAPIKey(srv.URL, "qbt_testkey0123456789abcdefghij")
	if !c.Login() {
		t.Fatal("expected API key login to succeed")
	}
	if sawAuth != "Bearer qbt_testkey0123456789abcdefghij" {
		t.Errorf("Authorization=%q", sawAuth)
	}
}

func TestLogin_APIKey_Rejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewWithAPIKey(srv.URL, "qbt_bad")
	if c.Login() {
		t.Error("expected API key login to fail")
	}
}

func TestAddTorrent_APIKey(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/app/version" {
			w.Write([]byte("v5.2.0"))
			return
		}
		if r.URL.Path == "/api/v2/torrents/add" {
			sawAuth = r.Header.Get("Authorization")
			w.Write([]byte("Ok."))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewWithAPIKey(srv.URL, "qbt_testkey0123456789abcdefghij")
	if !c.AddTorrent("magnet:?xt=urn:btih:abc", "t", "/data", "console") {
		t.Fatal("expected AddTorrent to succeed")
	}
	if sawAuth != "Bearer qbt_testkey0123456789abcdefghij" {
		t.Errorf("Authorization=%q", sawAuth)
	}
}

// Every exported call must carry the key, not just the ones the feature was
// written against. A method that builds its own request instead of going
// through the auth helpers still compiles, still passes its own test against a
// permissive mock, and only fails against a real qBittorrent - as a 403 that
// reads like a bad key rather than a missing header.
func TestAPIKeyOnEveryExportedCall(t *testing.T) {
	const key = "bearer-key-under-test"

	var mu sync.Mutex
	seen := map[string]string{} // request path -> Authorization header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.Header.Get("Authorization")
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v2/torrents/info":
			w.Write([]byte("[]"))
		case "/api/v2/torrents/files":
			w.Write([]byte("[]"))
		case "/api/v2/app/version":
			w.Write([]byte("v5.2.0"))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := NewWithAPIKey(srv.URL, key)
	c.Login()
	c.AddTorrent("magnet:?xt=urn:btih:abc", "Title", "/downloads", "games")
	c.AddTorrentPaused("magnet:?xt=urn:btih:def", "Title", "/downloads", "games")
	if _, err := c.GetTorrents("games"); err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	c.GetTorrentFiles("abc")
	c.DeleteTorrent("abc", true)
	c.StopTorrent("abc")
	c.SetFilePriority("abc", []int{0}, 0)
	c.StartTorrent("abc")

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("no requests reached the server")
	}
	for path, auth := range seen {
		if auth != "Bearer "+key {
			t.Errorf("%s sent Authorization=%q, want the Bearer key", path, auth)
		}
	}
	// The calls above must actually have exercised distinct endpoints, or this
	// test would pass while covering almost nothing.
	for _, want := range []string{
		"/api/v2/app/version",
		"/api/v2/torrents/add",
		"/api/v2/torrents/info",
		"/api/v2/torrents/files",
		"/api/v2/torrents/delete",
		"/api/v2/torrents/filePrio",
		"/api/v2/torrents/start",
	} {
		if _, ok := seen[want]; !ok {
			t.Errorf("%s was never requested; the test is not covering it", want)
		}
	}
}

// A Bearer key is fixed for the life of the process, so a 403 cannot be cured
// by authenticating again. Retrying re-sends the same rejected key: it cannot
// succeed, and at the watcher's 30s poll it turns one mistyped key into
// thousands of ERROR lines a day.
func TestRejectedAPIKeyIsNotRetried(t *testing.T) {
	var mu sync.Mutex
	var adds, probes int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		switch r.URL.Path {
		case "/api/v2/torrents/add":
			adds++
		case "/api/v2/app/version":
			probes++
		}
		mu.Unlock()
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewWithAPIKey(srv.URL, "qbt_rejected")
	if c.AddTorrent("magnet:?xt=urn:btih:abc", "T", "/downloads", "games") {
		t.Fatal("AddTorrent reported success against a rejecting server")
	}

	mu.Lock()
	defer mu.Unlock()
	if adds != 1 {
		t.Errorf("add attempts = %d, want 1: a rejected key cannot be re-authenticated", adds)
	}
	// One probe from the initial ensureAuth; none from a pointless re-login.
	if probes > 1 {
		t.Errorf("version probes = %d, want at most 1", probes)
	}
}

func TestNewMutatorsRejectedAPIKeyAreNotRetried(t *testing.T) {
	var mu sync.Mutex
	attempts := map[string]int{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts[r.URL.Path]++
		mu.Unlock()
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewWithAPIKey(srv.URL, "qbt_rejected")
	c.authenticated = true
	if c.AddTorrentPaused("magnet:?xt=urn:btih:abc", "T", "/downloads", "games") {
		t.Fatal("AddTorrentPaused reported success against a rejecting API key")
	}
	if c.SetFilePriority("abc", []int{0}, 0) {
		t.Fatal("SetFilePriority reported success against a rejecting API key")
	}
	if c.StartTorrent("abc") {
		t.Fatal("StartTorrent reported success against a rejecting API key")
	}

	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{
		"/api/v2/torrents/add",
		"/api/v2/torrents/filePrio",
		"/api/v2/torrents/start",
	} {
		if got := attempts[path]; got != 1 {
			t.Errorf("%s attempts=%d, want 1 for a rejected API key", path, got)
		}
	}
	if got := attempts["/api/v2/torrents/resume"]; got != 0 {
		t.Errorf("resume attempts=%d, want 0 after a 403", got)
	}
	if got := attempts["/api/v2/app/version"]; got != 0 {
		t.Errorf("version probes=%d, want 0 after pre-authentication", got)
	}
}

// The cookie path must keep its retry: a session really can expire, and
// logging in again really can fix it.
func TestCookieAuthStillRetriesOnce(t *testing.T) {
	var mu sync.Mutex
	var adds, logins int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v2/auth/login":
			logins++
			w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			adds++
			if adds == 1 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "admin", "pw")
	if !c.AddTorrent("magnet:?xt=urn:btih:abc", "T", "/downloads", "games") {
		t.Fatal("expected the expired-session retry to succeed")
	}
	mu.Lock()
	defer mu.Unlock()
	if adds != 2 {
		t.Errorf("add attempts = %d, want 2 (original plus the post-relogin retry)", adds)
	}
	if logins < 2 {
		t.Errorf("logins = %d, want the 403 to trigger a re-login", logins)
	}
}
