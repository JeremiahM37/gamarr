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

// trRecorder captures Transmission RPC requests.
type trRecorder struct {
	mu       sync.Mutex
	requests []transmissionRequest
	headers  []http.Header
}

func (r *trRecorder) record(req *http.Request) transmissionRequest {
	var body transmissionRequest
	json.NewDecoder(req.Body).Decode(&body)
	r.mu.Lock()
	r.requests = append(r.requests, body)
	r.headers = append(r.headers, req.Header.Clone())
	r.mu.Unlock()
	return body
}

func (r *trRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func newTransmission(url, user, pass string) *TransmissionClient {
	return NewTransmissionClient(&config.Config{
		TransmissionURL:  url,
		TransmissionUser: user,
		TransmissionPass: pass,
	})
}

func TestTransmissionAddTorrent(t *testing.T) {
	tests := []struct {
		name     string
		response string
		wantErr  string
		wantName string
	}{
		{
			name:     "torrent added",
			response: `{"result":"success","arguments":{"torrent-added":{"id":1,"name":"Game A","hashString":"abc"}}}`,
			wantName: "Game A",
		},
		{
			name:     "torrent duplicate",
			response: `{"result":"success","arguments":{"torrent-duplicate":{"id":2,"name":"Game B","hashString":"def"}}}`,
			wantName: "Game B",
		},
		{
			name:     "success without torrent info returns arguments",
			response: `{"result":"success","arguments":{}}`,
		},
		{
			name:     "error result",
			response: `{"result":"invalid or corrupt torrent file"}`,
			wantErr:  "invalid or corrupt torrent file",
		},
		{
			name:     "invalid json",
			response: `not json at all`,
			wantErr:  "invalid JSON response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &trRecorder{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := rec.record(r)
				if body.Method != "torrent-add" {
					t.Errorf("method = %q, want torrent-add", body.Method)
				}
				fmt.Fprint(w, tt.response)
			}))
			defer srv.Close()

			c := newTransmission(srv.URL, "", "")
			got, err := c.AddTorrent("magnet:?xt=urn:btih:abc", "/downloads")

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantName != "" {
				if name, _ := got["name"].(string); name != tt.wantName {
					t.Errorf("name = %q, want %q", name, tt.wantName)
				}
			}
			// Verify request arguments.
			rec.mu.Lock()
			args := rec.requests[0].Arguments
			rec.mu.Unlock()
			if args["filename"] != "magnet:?xt=urn:btih:abc" {
				t.Errorf("filename arg = %v", args["filename"])
			}
			if args["download-dir"] != "/downloads" {
				t.Errorf("download-dir arg = %v", args["download-dir"])
			}
		})
	}
}

func TestTransmissionAddTorrentNoDownloadDir(t *testing.T) {
	rec := &trRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		fmt.Fprint(w, `{"result":"success","arguments":{}}`)
	}))
	defer srv.Close()

	c := newTransmission(srv.URL, "", "")
	if _, err := c.AddTorrent("magnet:x", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if _, ok := rec.requests[0].Arguments["download-dir"]; ok {
		t.Error("download-dir should be omitted when empty")
	}
}

func TestTransmissionSessionID(t *testing.T) {
	t.Run("409 retried with session header", func(t *testing.T) {
		rec := &trRecorder{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec.record(r)
			if r.Header.Get("X-Transmission-Session-Id") != "sess-42" {
				w.Header().Set("X-Transmission-Session-Id", "sess-42")
				w.WriteHeader(http.StatusConflict)
				return
			}
			fmt.Fprint(w, `{"result":"success","arguments":{}}`)
		}))
		defer srv.Close()

		c := newTransmission(srv.URL, "", "")
		if _, err := c.AddTorrent("magnet:x", ""); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rec.count() != 2 {
			t.Fatalf("request count = %d, want 2 (409 then retry)", rec.count())
		}
		rec.mu.Lock()
		second := rec.headers[1].Get("X-Transmission-Session-Id")
		rec.mu.Unlock()
		if second != "sess-42" {
			t.Errorf("retry session header = %q, want sess-42", second)
		}

		// Session ID is cached: the next call succeeds in one request.
		if _, err := c.AddTorrent("magnet:y", ""); err != nil {
			t.Fatalf("second add: %v", err)
		}
		if rec.count() != 3 {
			t.Errorf("request count = %d, want 3 (cached session)", rec.count())
		}
	})

	t.Run("409 without session header errors", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
		}))
		defer srv.Close()

		c := newTransmission(srv.URL, "", "")
		_, err := c.AddTorrent("magnet:x", "")
		if err == nil || !strings.Contains(err.Error(), "no session ID") {
			t.Fatalf("err = %v, want no-session-ID error", err)
		}
	})
}

func TestTransmissionAuth(t *testing.T) {
	t.Run("401 returns auth error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()

		c := newTransmission(srv.URL, "u", "p")
		_, err := c.AddTorrent("magnet:x", "")
		if err == nil || !strings.Contains(err.Error(), "authentication failed") {
			t.Fatalf("err = %v, want authentication failed", err)
		}
	})

	t.Run("basic auth header sent when user set", func(t *testing.T) {
		var gotUser, gotPass string
		var hadAuth bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUser, gotPass, hadAuth = r.BasicAuth()
			fmt.Fprint(w, `{"result":"success","arguments":{}}`)
		}))
		defer srv.Close()

		c := newTransmission(srv.URL, "admin", "secret")
		if _, err := c.AddTorrent("magnet:x", ""); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !hadAuth || gotUser != "admin" || gotPass != "secret" {
			t.Errorf("basic auth = (%q,%q,%v), want (admin,secret,true)", gotUser, gotPass, hadAuth)
		}
	})

	t.Run("no basic auth when user empty", func(t *testing.T) {
		var hadAuth bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _, hadAuth = r.BasicAuth()
			fmt.Fprint(w, `{"result":"success","arguments":{}}`)
		}))
		defer srv.Close()

		c := newTransmission(srv.URL, "", "")
		if _, err := c.AddTorrent("magnet:x", ""); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if hadAuth {
			t.Error("basic auth header sent despite empty user")
		}
	})
}

func TestTransmissionNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // Closed immediately: all requests fail.

	c := newTransmission(srv.URL, "", "")
	if _, err := c.AddTorrent("magnet:x", ""); err == nil {
		t.Error("AddTorrent: want error on dead server")
	}
	if _, err := c.GetTorrents(nil); err == nil {
		t.Error("GetTorrents: want error on dead server")
	}
	if err := c.RemoveTorrent([]int{1}, false); err == nil {
		t.Error("RemoveTorrent: want error on dead server")
	}
}

func TestTransmissionGetTorrents(t *testing.T) {
	tests := []struct {
		name     string
		response string
		wantErr  bool
		wantLen  int
	}{
		{
			name:     "two torrents",
			response: `{"result":"success","arguments":{"torrents":[{"name":"A","percentDone":1.0},{"name":"B","percentDone":0.5}]}}`,
			wantLen:  2,
		},
		{
			name:     "no torrents key",
			response: `{"result":"success","arguments":{}}`,
			wantLen:  0,
		},
		{
			name:     "error result",
			response: `{"result":"method not allowed"}`,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &trRecorder{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := rec.record(r)
				if body.Method != "torrent-get" {
					t.Errorf("method = %q, want torrent-get", body.Method)
				}
				fmt.Fprint(w, tt.response)
			}))
			defer srv.Close()

			c := newTransmission(srv.URL, "", "")
			got, err := c.GetTorrents(nil)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tt.wantLen {
				t.Errorf("len = %d, want %d", len(got), tt.wantLen)
			}

			// Default fields must be requested when fields is nil.
			rec.mu.Lock()
			fields, _ := rec.requests[0].Arguments["fields"].([]interface{})
			rec.mu.Unlock()
			joined := fmt.Sprint(fields)
			for _, f := range []string{"name", "percentDone", "hashString"} {
				if !strings.Contains(joined, f) {
					t.Errorf("default fields missing %q: %v", f, fields)
				}
			}
		})
	}
}

func TestTransmissionGetTorrentsCustomFields(t *testing.T) {
	rec := &trRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		fmt.Fprint(w, `{"result":"success","arguments":{"torrents":[]}}`)
	}))
	defer srv.Close()

	c := newTransmission(srv.URL, "", "")
	if _, err := c.GetTorrents([]string{"id"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rec.mu.Lock()
	fields, _ := rec.requests[0].Arguments["fields"].([]interface{})
	rec.mu.Unlock()
	if len(fields) != 1 || fields[0] != "id" {
		t.Errorf("fields = %v, want [id]", fields)
	}
}

func TestTransmissionRemoveTorrent(t *testing.T) {
	t.Run("success sends ids and delete flag", func(t *testing.T) {
		rec := &trRecorder{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body := rec.record(r)
			if body.Method != "torrent-remove" {
				t.Errorf("method = %q, want torrent-remove", body.Method)
			}
			fmt.Fprint(w, `{"result":"success"}`)
		}))
		defer srv.Close()

		c := newTransmission(srv.URL, "", "")
		if err := c.RemoveTorrent([]int{3, 7}, true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		rec.mu.Lock()
		defer rec.mu.Unlock()
		args := rec.requests[0].Arguments
		if args["delete-local-data"] != true {
			t.Errorf("delete-local-data = %v, want true", args["delete-local-data"])
		}
		ids, _ := args["ids"].([]interface{})
		if len(ids) != 2 {
			t.Errorf("ids = %v, want 2 entries", ids)
		}
	})

	t.Run("error result", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"result":"torrent not found"}`)
		}))
		defer srv.Close()

		c := newTransmission(srv.URL, "", "")
		err := c.RemoveTorrent([]int{1}, false)
		if err == nil || !strings.Contains(err.Error(), "torrent not found") {
			t.Fatalf("err = %v, want torrent not found", err)
		}
	})
}

func TestTransmissionDiagnose(t *testing.T) {
	tests := []struct {
		name        string
		handler     http.HandlerFunc
		closeServer bool
		wantSuccess bool
	}{
		{
			name: "healthy session",
			handler: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"result":"success","arguments":{"version":"4.0"}}`)
			},
			wantSuccess: true,
		},
		{
			name: "error result",
			handler: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"result":"something broke"}`)
			},
			wantSuccess: false,
		},
		{
			name:        "unreachable",
			handler:     func(w http.ResponseWriter, r *http.Request) {},
			closeServer: true,
			wantSuccess: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			if tt.closeServer {
				srv.Close()
			} else {
				defer srv.Close()
			}
			c := newTransmission(srv.URL, "", "")
			got := c.Diagnose()
			if success, _ := got["success"].(bool); success != tt.wantSuccess {
				t.Errorf("success = %v, want %v (result %v)", success, tt.wantSuccess, got)
			}
			if !tt.wantSuccess {
				if errStr, _ := got["error"].(string); errStr == "" {
					t.Error("failed diagnose should include an error message")
				}
			}
		})
	}
}
