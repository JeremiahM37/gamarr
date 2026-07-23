package qbit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	result := c.GetTorrents("games")
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
	result := c.GetTorrents("")
	if len(result) != 1 {
		t.Fatalf("expected 1 torrent after reauth, got %d", len(result))
	}
}

func TestGetTorrentFiles(t *testing.T) {
	files := []TorrentFile{
		{Name: "game/setup.exe"},
		{Name: "game/data.bin"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.Write([]byte("Ok."))
			return
		}
		if r.URL.Path == "/api/v2/torrents/files" {
			json.NewEncoder(w).Encode(files)
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
