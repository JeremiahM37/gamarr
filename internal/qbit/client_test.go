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
