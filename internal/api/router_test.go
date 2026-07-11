package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gamarr/internal/config"
)

// ── Core endpoints ─────────────────────────────────────────────────────────────

func TestHealthEndpoint(t *testing.T) {
	env := newTestEnv(t, nil)
	rr := env.do("GET", "/api/health", "")
	wantStatus(t, rr, 200)
	m := decodeMap(t, rr)
	if m["status"] != "ok" {
		t.Errorf("status field = %v, want ok", m["status"])
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestConfigEndpointReportsConfiguredServices(t *testing.T) {
	env := newTestEnv(t, nil)
	rr := env.do("GET", "/api/config", "")
	wantStatus(t, rr, 200)
	m := decodeMap(t, rr)

	for _, svc := range []string{"prowlarr", "qbittorrent", "sabnzbd", "transmission", "deluge", "rawg"} {
		section, ok := m[svc].(map[string]interface{})
		if !ok {
			t.Fatalf("config missing %q section: %v", svc, m)
		}
		if section["configured"] != false {
			t.Errorf("%s.configured = %v, want false (nothing configured in test env)", svc, section["configured"])
		}
	}
	if m["version"] == "" {
		t.Error("config missing version")
	}
}

func TestPlatformsEndpoint(t *testing.T) {
	env := newTestEnv(t, nil)
	rr := env.do("GET", "/api/platforms", "")
	wantStatus(t, rr, 200)
	m := decodeMap(t, rr)

	platforms, ok := m["platforms"].([]interface{})
	if !ok || len(platforms) < 3 {
		t.Fatalf("expected several platforms, got %v", m["platforms"])
	}
	first, _ := platforms[0].(map[string]interface{})
	if first["id"] != "all" {
		t.Errorf("first platform id = %v, want all", first["id"])
	}
	ids := map[string]bool{}
	for _, p := range platforms {
		pm, _ := p.(map[string]interface{})
		id, _ := pm["id"].(string)
		if ids[id] {
			t.Errorf("duplicate platform id %q", id)
		}
		ids[id] = true
	}
	if !ids["pc"] {
		t.Error("platforms missing pc")
	}
}

func TestSourcesEndpoint(t *testing.T) {
	env := newTestEnv(t, nil)
	rr := env.do("GET", "/api/sources", "")
	wantStatus(t, rr, 200)
	m := decodeMap(t, rr)

	srcs, ok := m["sources"].([]interface{})
	if !ok || len(srcs) != 3 {
		t.Fatalf("expected 3 sources, got %v", m["sources"])
	}
	names := map[string]bool{}
	for _, s := range srcs {
		sm, _ := s.(map[string]interface{})
		name, _ := sm["name"].(string)
		names[name] = true
	}
	for _, want := range []string{"prowlarr", "myrient", "vimm"} {
		if !names[want] {
			t.Errorf("sources missing %q (got %v)", want, names)
		}
	}
}

func TestStatsEndpointEmpty(t *testing.T) {
	env := newTestEnv(t, nil)
	rr := env.do("GET", "/api/stats", "")
	wantStatus(t, rr, 200)
	m := decodeMap(t, rr)
	if m["library_total"] != float64(0) {
		t.Errorf("library_total = %v, want 0", m["library_total"])
	}
	if m["total_jobs"] != float64(0) {
		t.Errorf("total_jobs = %v, want 0", m["total_jobs"])
	}
}

func TestActivityEndpointEmpty(t *testing.T) {
	env := newTestEnv(t, nil)
	rr := env.do("GET", "/api/activity", "")
	wantStatus(t, rr, 200)
	m := decodeMap(t, rr)
	if m["total"] != float64(0) {
		t.Errorf("total = %v, want 0", m["total"])
	}
	if _, ok := m["entries"].([]interface{}); !ok {
		t.Errorf("entries should be a JSON array even when empty, got %T", m["entries"])
	}
}

func TestOpenAPISpec(t *testing.T) {
	env := newTestEnv(t, nil)
	rr := env.do("GET", "/api/openapi.json", "")
	wantStatus(t, rr, 200)
	m := decodeMap(t, rr)
	if _, ok := m["openapi"]; !ok {
		t.Error("openapi.json missing top-level openapi field")
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		env := newTestEnv(t, nil)
		rr := env.do("GET", "/metrics", "")
		wantStatus(t, rr, 200)
		body := rr.Body.String()
		for _, want := range []string{"gamarr_jobs_total", "gamarr_library_total 0"} {
			if !strings.Contains(body, want) {
				t.Errorf("metrics body missing %q", want)
			}
		}
	})
	t.Run("disabled returns 403", func(t *testing.T) {
		env := newTestEnv(t, func(c *config.Config) { c.MetricsEnabled = false })
		rr := env.do("GET", "/metrics", "")
		wantStatus(t, rr, 403)
	})
}

func TestUnknownRouteAndMethod(t *testing.T) {
	env := newTestEnv(t, nil)

	t.Run("unknown route is 404", func(t *testing.T) {
		rr := env.do("GET", "/api/definitely-not-a-route", "")
		wantStatus(t, rr, 404)
	})
	t.Run("wrong method is 405", func(t *testing.T) {
		rr := env.do("DELETE", "/api/health", "")
		wantStatus(t, rr, 405)
	})
}

func TestTorznabCapsThroughRouter(t *testing.T) {
	// /torznab/api is auth-exempt and must answer caps through the full
	// middleware chain (this is what Prowlarr probes during discovery).
	env := newTestEnv(t, nil)
	rr := env.do("GET", "/torznab/api?t=caps", "")
	wantStatus(t, rr, 200)
	if !strings.Contains(rr.Body.String(), "<caps") {
		t.Errorf("expected caps XML, got: %s", rr.Body.String())
	}
}

// ── Middleware: security headers, CORS, body size limit ───────────────────────

func TestSecurityHeadersPresent(t *testing.T) {
	env := newTestEnv(t, nil)
	rr := env.do("GET", "/api/health", "")
	wantStatus(t, rr, 200)

	headers := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"X-XSS-Protection":       "1; mode=block",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	}
	for name, want := range headers {
		if got := rr.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	// Note: no Content-Security-Policy header is set by
	// securityHeadersMiddleware (internal/api/security.go) — if one is added
	// later, extend this test.
}

func TestCORSMiddleware(t *testing.T) {
	env := newTestEnv(t, nil)

	t.Run("preflight OPTIONS returns 204", func(t *testing.T) {
		rr := env.do("OPTIONS", "/api/health", "", withHeader("Origin", "http://example.com"))
		wantStatus(t, rr, 204)
		if got := rr.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "GET") {
			t.Errorf("Allow-Methods = %q, want it to include GET", got)
		}
	})

	t.Run("same-origin request reflects origin", func(t *testing.T) {
		// httptest.NewRequest sets Host to example.com for this URL form.
		rr := env.do("GET", "/api/health", "", withHeader("Origin", "http://example.com"))
		wantStatus(t, rr, 200)
		if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "http://example.com" {
			t.Errorf("Allow-Origin = %q, want http://example.com", got)
		}
	})

	t.Run("cross-origin request without API key gets no ACAO", func(t *testing.T) {
		rr := env.do("GET", "/api/health", "", withHeader("Origin", "http://evil.test"))
		wantStatus(t, rr, 200)
		if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("Allow-Origin = %q, want empty for non-matching origin", got)
		}
	})

	t.Run("cross-origin request with API key header gets ACAO", func(t *testing.T) {
		rr := env.do("GET", "/api/health", "",
			withHeader("Origin", "http://pwa.test"),
			withHeader("X-Api-Key", "anything"))
		wantStatus(t, rr, 200)
		if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "http://pwa.test" {
			t.Errorf("Allow-Origin = %q, want http://pwa.test", got)
		}
	})
}

func TestRequestSizeLimit(t *testing.T) {
	env := newTestEnv(t, nil)
	huge := `{"title":"` + strings.Repeat("A", (1<<20)+1024) + `"}`

	t.Run("oversized Content-Length is 413 from middleware", func(t *testing.T) {
		// >1MB declared body must be rejected up front by
		// requestSizeLimitMiddleware with 413, not surfaced as a 400
		// decode failure.
		rr := env.do("POST", "/api/wishlist", huge)
		wantStatus(t, rr, http.StatusRequestEntityTooLarge)
		if m := decodeMap(t, rr); m["success"] != false {
			t.Errorf("413 body should be a JSON error envelope, got: %v", m)
		}
	})

	t.Run("oversized body without declared length is 413 via decode", func(t *testing.T) {
		// Wrap the reader so httptest.NewRequest cannot infer ContentLength
		// (-1 / chunked): the middleware cannot pre-reject, so the
		// MaxBytesReader trips mid-decode and decodeJSONBody must map
		// *http.MaxBytesError to 413.
		body := struct{ io.Reader }{strings.NewReader(huge)}
		req := httptest.NewRequest("POST", "/api/wishlist", body)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		env.router.ServeHTTP(rr, req)
		wantStatus(t, rr, http.StatusRequestEntityTooLarge)
	})

	t.Run("multipart bodies bypass the size cap", func(t *testing.T) {
		// The multipart exemption must survive: a huge multipart upload is
		// not 413'd by the middleware (it reaches the handler, which rejects
		// the content type itself).
		rr := env.do("POST", "/api/wishlist", huge,
			withHeader("Content-Type", "multipart/form-data; boundary=x"))
		wantStatus(t, rr, http.StatusUnsupportedMediaType)
	})

	t.Run("body under the limit parses fine", func(t *testing.T) {
		// Proves the limiter did not truncate a legitimate body.
		okBody := `{"title":"` + strings.Repeat("B", 400) + `","platform":"NES","platform_slug":"nes"}`
		rr := env.do("POST", "/api/wishlist", okBody)
		wantStatus(t, rr, 200)
	})
}
