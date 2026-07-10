package download

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gamarr/internal/config"
)

// dlRecorder captures Deluge JSON-RPC requests.
type dlRecorder struct {
	mu       sync.Mutex
	requests []delugeRequest
	cookies  []string
}

func (r *dlRecorder) record(req *http.Request) delugeRequest {
	var body delugeRequest
	json.NewDecoder(req.Body).Decode(&body)
	r.mu.Lock()
	r.requests = append(r.requests, body)
	r.cookies = append(r.cookies, req.Header.Get("Cookie"))
	r.mu.Unlock()
	return body
}

func (r *dlRecorder) methods() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.requests))
	for i, req := range r.requests {
		out[i] = req.Method
	}
	return out
}

func newDeluge(url, pass string) *DelugeClient {
	return NewDelugeClient(&config.Config{DelugeURL: url, DelugePass: pass})
}

func TestDelugeLogin(t *testing.T) {
	tests := []struct {
		name        string
		response    string
		closeServer bool
		wantErr     string
	}{
		{"success", `{"id":1,"result":true,"error":null}`, false, ""},
		{"bad password", `{"id":1,"result":false,"error":null}`, false, "authentication failed"},
		{"non-bool result", `{"id":1,"result":"nope","error":null}`, false, "invalid response"},
		{"invalid json", `garbage`, false, "invalid JSON response"},
		{"unreachable", ``, true, "deluge request"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &dlRecorder{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/json" {
					t.Errorf("path = %q, want /json", r.URL.Path)
				}
				body := rec.record(r)
				if body.Method != "auth.login" {
					t.Errorf("method = %q, want auth.login", body.Method)
				}
				if len(body.Params) != 1 || body.Params[0] != "secret" {
					t.Errorf("params = %v, want [secret]", body.Params)
				}
				fmt.Fprint(w, tt.response)
			}))
			if tt.closeServer {
				srv.Close()
			} else {
				defer srv.Close()
			}

			c := newDeluge(srv.URL, "secret")
			err := c.Login()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDelugeSessionCookie(t *testing.T) {
	rec := &dlRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := rec.record(r)
		if body.Method == "auth.login" {
			http.SetCookie(w, &http.Cookie{Name: "_session_id", Value: "dcookie123"})
			fmt.Fprint(w, `{"id":1,"result":true,"error":null}`)
			return
		}
		fmt.Fprint(w, `{"id":2,"result":{},"error":null}`)
	}))
	defer srv.Close()

	c := newDeluge(srv.URL, "secret")
	if err := c.Login(); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := c.GetTorrentsStatus(nil, nil); err != nil {
		t.Fatalf("get torrents: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.cookies) != 2 {
		t.Fatalf("request count = %d, want 2", len(rec.cookies))
	}
	if rec.cookies[0] != "" {
		t.Errorf("first request cookie = %q, want empty", rec.cookies[0])
	}
	if rec.cookies[1] != "_session_id=dcookie123" {
		t.Errorf("second request cookie = %q, want _session_id=dcookie123", rec.cookies[1])
	}
}

func TestDelugeAddTorrent(t *testing.T) {
	t.Run("success returns torrent id", func(t *testing.T) {
		rec := &dlRecorder{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body := rec.record(r)
			if body.Method != "core.add_torrent_url" {
				t.Errorf("method = %q, want core.add_torrent_url", body.Method)
			}
			fmt.Fprint(w, `{"id":1,"result":"hash123","error":null}`)
		}))
		defer srv.Close()

		c := newDeluge(srv.URL, "secret")
		id, err := c.AddTorrent("magnet:x", map[string]interface{}{"download_location": "/dl"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "hash123" {
			t.Errorf("id = %q, want hash123", id)
		}
		rec.mu.Lock()
		defer rec.mu.Unlock()
		if rec.requests[0].Params[0] != "magnet:x" {
			t.Errorf("url param = %v", rec.requests[0].Params[0])
		}
		opts, _ := rec.requests[0].Params[1].(map[string]interface{})
		if opts["download_location"] != "/dl" {
			t.Errorf("options = %v, want download_location=/dl", opts)
		}
	})

	t.Run("nil options sent as empty object", func(t *testing.T) {
		rec := &dlRecorder{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec.record(r)
			fmt.Fprint(w, `{"id":1,"result":"h","error":null}`)
		}))
		defer srv.Close()

		c := newDeluge(srv.URL, "secret")
		if _, err := c.AddTorrent("magnet:x", nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		rec.mu.Lock()
		defer rec.mu.Unlock()
		if opts, ok := rec.requests[0].Params[1].(map[string]interface{}); !ok || len(opts) != 0 {
			t.Errorf("options param = %v, want empty map", rec.requests[0].Params[1])
		}
	})

	t.Run("rpc error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"id":1,"result":null,"error":{"message":"Not authenticated","code":1}}`)
		}))
		defer srv.Close()

		c := newDeluge(srv.URL, "secret")
		_, err := c.AddTorrent("magnet:x", nil)
		if err == nil || !strings.Contains(err.Error(), "Not authenticated") {
			t.Fatalf("err = %v, want Not authenticated", err)
		}
	})

	t.Run("non-string torrent id", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"id":1,"result":42,"error":null}`)
		}))
		defer srv.Close()

		c := newDeluge(srv.URL, "secret")
		_, err := c.AddTorrent("magnet:x", nil)
		if err == nil || !strings.Contains(err.Error(), "invalid torrent ID") {
			t.Fatalf("err = %v, want invalid torrent ID", err)
		}
	})

	t.Run("re-auth after transport failure then success", func(t *testing.T) {
		rec := &dlRecorder{}
		var calls int
		var mu sync.Mutex
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body := rec.record(r)
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			switch {
			case n == 1: // First add attempt: broken response forces re-auth.
				fmt.Fprint(w, `garbage`)
			case body.Method == "auth.login":
				fmt.Fprint(w, `{"id":2,"result":true,"error":null}`)
			default: // Retried add.
				fmt.Fprint(w, `{"id":3,"result":"hash-after-reauth","error":null}`)
			}
		}))
		defer srv.Close()

		c := newDeluge(srv.URL, "secret")
		id, err := c.AddTorrent("magnet:x", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "hash-after-reauth" {
			t.Errorf("id = %q, want hash-after-reauth", id)
		}
		want := []string{"core.add_torrent_url", "auth.login", "core.add_torrent_url"}
		got := rec.methods()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("call sequence = %v, want %v", got, want)
		}
	})

	t.Run("re-auth failure surfaces login error", func(t *testing.T) {
		var calls int
		var mu sync.Mutex
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			if n == 1 {
				fmt.Fprint(w, `garbage`)
				return
			}
			fmt.Fprint(w, `{"id":2,"result":false,"error":null}`) // login rejected
		}))
		defer srv.Close()

		c := newDeluge(srv.URL, "wrong")
		_, err := c.AddTorrent("magnet:x", nil)
		if err == nil || !strings.Contains(err.Error(), "re-auth") {
			t.Fatalf("err = %v, want re-auth error", err)
		}
	})
}

func TestDelugeGetTorrentStatus(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		rec := &dlRecorder{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body := rec.record(r)
			if body.Method != "core.get_torrent_status" {
				t.Errorf("method = %q", body.Method)
			}
			fmt.Fprint(w, `{"id":1,"result":{"name":"Game","progress":100.0},"error":null}`)
		}))
		defer srv.Close()

		c := newDeluge(srv.URL, "secret")
		status, err := c.GetTorrentStatus("hash1", []string{"name", "progress"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if status["name"] != "Game" {
			t.Errorf("name = %v, want Game", status["name"])
		}
	})

	t.Run("rpc error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"id":1,"result":null,"error":{"message":"no such torrent","code":4}}`)
		}))
		defer srv.Close()

		c := newDeluge(srv.URL, "secret")
		_, err := c.GetTorrentStatus("bad", nil)
		if err == nil || !strings.Contains(err.Error(), "no such torrent") {
			t.Fatalf("err = %v, want no such torrent", err)
		}
	})
}

func TestDelugeGetTorrentsStatus(t *testing.T) {
	t.Run("defaults for nil filter and keys", func(t *testing.T) {
		rec := &dlRecorder{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec.record(r)
			fmt.Fprint(w, `{"id":1,"result":{"h1":{"name":"A"}},"error":null}`)
		}))
		defer srv.Close()

		c := newDeluge(srv.URL, "secret")
		torrents, err := c.GetTorrentsStatus(nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(torrents) != 1 {
			t.Errorf("torrents = %v, want 1 entry", torrents)
		}
		rec.mu.Lock()
		defer rec.mu.Unlock()
		filter, _ := rec.requests[0].Params[0].(map[string]interface{})
		if len(filter) != 0 {
			t.Errorf("filter = %v, want empty map", filter)
		}
		keys := fmt.Sprint(rec.requests[0].Params[1])
		for _, k := range []string{"name", "state", "progress", "save_path"} {
			if !strings.Contains(keys, k) {
				t.Errorf("default keys missing %q: %v", k, keys)
			}
		}
	})

	t.Run("rpc error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"id":1,"result":null,"error":{"message":"boom","code":9}}`)
		}))
		defer srv.Close()

		c := newDeluge(srv.URL, "secret")
		if _, err := c.GetTorrentsStatus(nil, nil); err == nil {
			t.Fatal("want error, got nil")
		}
	})
}

func TestDelugeRemoveTorrent(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		rec := &dlRecorder{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body := rec.record(r)
			if body.Method != "core.remove_torrent" {
				t.Errorf("method = %q", body.Method)
			}
			fmt.Fprint(w, `{"id":1,"result":true,"error":null}`)
		}))
		defer srv.Close()

		c := newDeluge(srv.URL, "secret")
		if err := c.RemoveTorrent("hash1", true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		rec.mu.Lock()
		defer rec.mu.Unlock()
		if rec.requests[0].Params[0] != "hash1" || rec.requests[0].Params[1] != true {
			t.Errorf("params = %v, want [hash1 true]", rec.requests[0].Params)
		}
	})

	t.Run("rpc error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"id":1,"result":null,"error":{"message":"not found","code":4}}`)
		}))
		defer srv.Close()

		c := newDeluge(srv.URL, "secret")
		if err := c.RemoveTorrent("bad", false); err == nil {
			t.Fatal("want error, got nil")
		}
	})
}

func TestDelugeDiagnose(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"id":1,"result":true,"error":null}`)
		}))
		defer srv.Close()

		got := newDeluge(srv.URL, "secret").Diagnose()
		if success, _ := got["success"].(bool); !success {
			t.Errorf("Diagnose = %v, want success", got)
		}
	})

	t.Run("failure includes error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"id":1,"result":false,"error":null}`)
		}))
		defer srv.Close()

		got := newDeluge(srv.URL, "wrong").Diagnose()
		if success, _ := got["success"].(bool); success {
			t.Errorf("Diagnose = %v, want failure", got)
		}
		if errStr, _ := got["error"].(string); !strings.Contains(errStr, "authentication failed") {
			t.Errorf("error = %q, want authentication failed", errStr)
		}
	})
}

func TestDelugeRequestIDIncrements(t *testing.T) {
	rec := &dlRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		fmt.Fprint(w, `{"id":1,"result":true,"error":null}`)
	}))
	defer srv.Close()

	c := newDeluge(srv.URL, "secret")
	c.Login()
	c.Login()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) != 2 || rec.requests[1].ID <= rec.requests[0].ID {
		t.Errorf("request IDs should increment: %d then %d", rec.requests[0].ID, rec.requests[1].ID)
	}
}
