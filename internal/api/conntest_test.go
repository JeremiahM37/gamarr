package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gamarr/internal/config"
)

// Connection tests hit only httptest mock servers — never live services.

func TestConnectionTestQBittorrent(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		env := newTestEnv(t, nil) // QBURL empty
		rr := env.do("POST", "/api/test/qbittorrent", "")
		wantStatus(t, rr, 200)
		m := decodeMap(t, rr)
		if m["success"] != false || m["error"] != "Not configured" {
			t.Errorf("got %v, want success=false error=Not configured", m)
		}
	})

	t.Run("successful login", func(t *testing.T) {
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v2/auth/login" {
				w.Write([]byte("Ok."))
				return
			}
			w.WriteHeader(404)
		}))
		defer mock.Close()

		env := newTestEnv(t, func(c *config.Config) {
			c.QBURL = mock.URL
			c.QBUser = "jam"
			c.QBPass = "pw"
		})
		rr := env.do("POST", "/api/test/qbittorrent", "")
		wantStatus(t, rr, 200)
		if m := decodeMap(t, rr); m["success"] != true {
			t.Errorf("got %v, want success=true", m)
		}
	})

	t.Run("bad credentials", func(t *testing.T) {
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// qBittorrent replies 200 "Fails." on bad credentials.
			w.Write([]byte("Fails."))
		}))
		defer mock.Close()

		env := newTestEnv(t, func(c *config.Config) { c.QBURL = mock.URL })
		rr := env.do("POST", "/api/test/qbittorrent", "")
		wantStatus(t, rr, 200)
		if m := decodeMap(t, rr); m["success"] != false {
			t.Errorf("got %v, want success=false", m)
		}
	})
}

func TestConnectionTestProwlarr(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		env := newTestEnv(t, nil)
		rr := env.do("POST", "/api/test/prowlarr", "")
		wantStatus(t, rr, 200)
		m := decodeMap(t, rr)
		if m["success"] != false || m["error"] != "Not configured" {
			t.Errorf("got %v, want success=false error=Not configured", m)
		}
	})

	t.Run("success with API key forwarded", func(t *testing.T) {
		var gotKey string
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/indexer" {
				w.WriteHeader(404)
				return
			}
			gotKey = r.Header.Get("X-Api-Key")
			w.Write([]byte("[]"))
		}))
		defer mock.Close()

		env := newTestEnv(t, func(c *config.Config) {
			c.ProwlarrURL = mock.URL
			c.ProwlarrAPIKey = "prowlarr-key"
		})
		rr := env.do("POST", "/api/test/prowlarr", "")
		wantStatus(t, rr, 200)
		m := decodeMap(t, rr)
		if m["success"] != true || m["status"] != float64(200) {
			t.Errorf("got %v, want success=true status=200", m)
		}
		if gotKey != "prowlarr-key" {
			t.Errorf("Prowlarr received X-Api-Key %q, want prowlarr-key", gotKey)
		}
	})

	t.Run("unauthorized upstream reports failure", func(t *testing.T) {
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(401)
		}))
		defer mock.Close()

		env := newTestEnv(t, func(c *config.Config) {
			c.ProwlarrURL = mock.URL
			c.ProwlarrAPIKey = "bad-key"
		})
		rr := env.do("POST", "/api/test/prowlarr", "")
		wantStatus(t, rr, 200)
		m := decodeMap(t, rr)
		if m["success"] != false || m["status"] != float64(401) {
			t.Errorf("got %v, want success=false status=401", m)
		}
	})
}

func TestConnectionTestSABnzbd(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		env := newTestEnv(t, nil) // sab client is nil
		rr := env.do("POST", "/api/test/sabnzbd", "")
		wantStatus(t, rr, 200)
		m := decodeMap(t, rr)
		if m["success"] != false || m["error"] != "Not configured" {
			t.Errorf("got %v, want success=false error=Not configured", m)
		}
	})

	t.Run("success", func(t *testing.T) {
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("mode") != "version" {
				w.WriteHeader(400)
				return
			}
			w.Write([]byte(`{"version":"4.0.0"}`))
		}))
		defer mock.Close()

		env := newTestEnv(t, func(c *config.Config) {
			c.SABnzbdURL = mock.URL
			c.SABnzbdAPIKey = "sab-key"
		})
		rr := env.do("POST", "/api/test/sabnzbd", "")
		wantStatus(t, rr, 200)
		if m := decodeMap(t, rr); m["success"] != true {
			t.Errorf("got %v, want success=true", m)
		}
	})

	t.Run("upstream error reports failure", func(t *testing.T) {
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
		}))
		defer mock.Close()

		env := newTestEnv(t, func(c *config.Config) {
			c.SABnzbdURL = mock.URL
			c.SABnzbdAPIKey = "sab-key"
		})
		rr := env.do("POST", "/api/test/sabnzbd", "")
		wantStatus(t, rr, 200)
		m := decodeMap(t, rr)
		if m["success"] != false {
			t.Errorf("got %v, want success=false", m)
		}
	})
}

// Transmission and Deluge connectivity tests are admin-gated identically to
// their qBittorrent/Prowlarr/SABnzbd siblings (see the 403 assertions in
// TestMultiUserSessionFlow). Here we cover the pass-through-admin responses.
func TestConnectionTestTransmissionDeluge(t *testing.T) {
	t.Run("transmission not configured", func(t *testing.T) {
		env := newTestEnv(t, nil)
		rr := env.do("POST", "/api/test/transmission", "")
		wantStatus(t, rr, 200)
		m := decodeMap(t, rr)
		if m["success"] != false || m["error"] != "Not configured" {
			t.Errorf("got %v, want success=false error=Not configured", m)
		}
	})

	t.Run("deluge not configured", func(t *testing.T) {
		env := newTestEnv(t, nil)
		rr := env.do("POST", "/api/test/deluge", "")
		wantStatus(t, rr, 200)
		m := decodeMap(t, rr)
		if m["success"] != false || m["error"] != "Not configured" {
			t.Errorf("got %v, want success=false error=Not configured", m)
		}
	})
}

func TestWebhookTestEndpointAgainstMock(t *testing.T) {
	env := newTestEnv(t, nil)

	t.Run("reachable webhook succeeds", func(t *testing.T) {
		received := false
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			received = true
			w.WriteHeader(200)
		}))
		defer mock.Close()

		rr := env.do("POST", "/api/webhooks/test", `{"url":"`+mock.URL+`","type":"generic"}`)
		wantStatus(t, rr, 200)
		if m := decodeMap(t, rr); m["success"] != true {
			t.Errorf("got %v, want success=true", m)
		}
		if !received {
			t.Error("mock webhook endpoint was never called")
		}
	})

	t.Run("unreachable webhook reports failure", func(t *testing.T) {
		// Closed server → immediate connection refused, no live network.
		dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		deadURL := dead.URL
		dead.Close()

		rr := env.do("POST", "/api/webhooks/test", `{"url":"`+deadURL+`","type":"generic"}`)
		wantStatus(t, rr, 200)
		if m := decodeMap(t, rr); m["success"] != false {
			t.Errorf("got %v, want success=false", m)
		}
	})
}
