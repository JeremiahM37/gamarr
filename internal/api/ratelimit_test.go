package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ── RateLimiter unit behavior ──────────────────────────────────────────────────

func TestRateLimiterRuleForPath(t *testing.T) {
	rl := NewRateLimiter(60, map[string]int{
		"login": 1, "search": 2, "download": 3, "api": 4, "default": 5,
	})
	cases := []struct {
		path string
		want string
	}{
		{"/api/login", "login"},
		{"/api/search", "search"},
		{"/api/search/advanced", "search"},
		{"/api/download", "download"},
		{"/api/downloads", "download"},
		{"/api/wishlist", "api"},
		{"/torznab/api", "default"},
		{"/", "default"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := rl.ruleForPath(tc.path); got != tc.want {
				t.Errorf("ruleForPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestRateLimiterCheck(t *testing.T) {
	rl := NewRateLimiter(60, map[string]int{"login": 2, "default": 3})

	t.Run("allows up to the limit then denies", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			allowed, _, rule, limit := rl.Check("1.2.3.4", "/api/login")
			if !allowed || rule != "login" || limit != 2 {
				t.Fatalf("request %d: allowed=%v rule=%q limit=%d, want allowed login/2", i+1, allowed, rule, limit)
			}
		}
		allowed, retryAfter, _, _ := rl.Check("1.2.3.4", "/api/login")
		if allowed {
			t.Fatal("3rd request should be denied")
		}
		if retryAfter < 1 || retryAfter > 60 {
			t.Errorf("retryAfter = %d, want within (0,60]", retryAfter)
		}
	})

	t.Run("identities are independent", func(t *testing.T) {
		if allowed, _, _, _ := rl.Check("5.6.7.8", "/api/login"); !allowed {
			t.Error("different identity should have its own bucket")
		}
	})

	t.Run("rules are independent per identity", func(t *testing.T) {
		// 1.2.3.4 exhausted "login" but "default" is untouched.
		if allowed, _, _, _ := rl.Check("1.2.3.4", "/other"); !allowed {
			t.Error("different rule should have its own bucket")
		}
	})

	t.Run("missing rule falls back to default", func(t *testing.T) {
		rl2 := NewRateLimiter(60, map[string]int{"default": 1})
		if _, _, _, limit := rl2.Check("x", "/api/wishlist"); limit != 1 {
			t.Errorf("limit = %d, want default fallback 1", limit)
		}
	})

	t.Run("no rules at all falls back to 600", func(t *testing.T) {
		rl3 := NewRateLimiter(60, map[string]int{})
		if _, _, _, limit := rl3.Check("x", "/anything"); limit != 600 {
			t.Errorf("limit = %d, want hardcoded fallback 600", limit)
		}
	})
}

// ── rateLimitMiddleware ────────────────────────────────────────────────────────

func TestRateLimitMiddleware(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})

	t.Run("nil limiter passes through", func(t *testing.T) {
		h := rateLimitMiddleware(nil, ok)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/x", nil))
		if rr.Code != 200 {
			t.Errorf("status = %d, want 200", rr.Code)
		}
	})

	t.Run("returns 429 with Retry-After when exceeded", func(t *testing.T) {
		rl := NewRateLimiter(60, map[string]int{"api": 2, "default": 2})
		h := rateLimitMiddleware(rl, ok)

		for i := 0; i < 2; i++ {
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/x", nil))
			if rr.Code != 200 {
				t.Fatalf("request %d: status = %d, want 200", i+1, rr.Code)
			}
		}

		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/x", nil))
		if rr.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429", rr.Code)
		}
		if rr.Header().Get("Retry-After") == "" {
			t.Error("429 response missing Retry-After header")
		}
		m := decodeMap(t, rr)
		if m["rule"] != "api" || m["limit"] != float64(2) {
			t.Errorf("429 body = %v, want rule=api limit=2", m)
		}
	})

	t.Run("health and metrics are exempt", func(t *testing.T) {
		rl := NewRateLimiter(60, map[string]int{"api": 1, "default": 1})
		h := rateLimitMiddleware(rl, ok)
		for _, path := range []string{"/api/health", "/api/health", "/metrics", "/metrics", "/health"} {
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
			if rr.Code != 200 {
				t.Errorf("%s: status = %d, want 200 (exempt)", path, rr.Code)
			}
		}
	})

	t.Run("X-Forwarded-For distinguishes clients", func(t *testing.T) {
		rl := NewRateLimiter(60, map[string]int{"api": 1, "default": 1})
		h := rateLimitMiddleware(rl, ok)

		req1 := httptest.NewRequest("GET", "/api/x", nil)
		req1.Header.Set("X-Forwarded-For", "10.0.0.1")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req1)
		if rr.Code != 200 {
			t.Fatalf("first client: status = %d", rr.Code)
		}

		// Same client again → limited.
		rr = httptest.NewRecorder()
		h.ServeHTTP(rr, req1)
		if rr.Code != 429 {
			t.Fatalf("first client second request: status = %d, want 429", rr.Code)
		}

		// Different client → fresh bucket.
		req2 := httptest.NewRequest("GET", "/api/x", nil)
		req2.Header.Set("X-Forwarded-For", "10.0.0.2")
		rr = httptest.NewRecorder()
		h.ServeHTTP(rr, req2)
		if rr.Code != 200 {
			t.Errorf("second client: status = %d, want 200", rr.Code)
		}
	})
}

// Full-router integration: the login rule (20/min) trips on the 21st request
// from the same client.
func TestLoginRateLimitThroughRouter(t *testing.T) {
	env := newTestEnv(t, nil) // auth unconfigured → login answers 200 cheaply

	for i := 0; i < 20; i++ {
		rr := env.do("POST", "/api/login", `{"username":"a","password":"b"}`)
		if rr.Code != 200 {
			t.Fatalf("login %d: status = %d, want 200", i+1, rr.Code)
		}
	}
	rr := env.do("POST", "/api/login", `{"username":"a","password":"b"}`)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("21st login: status = %d, want 429 (body: %s)", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Error("429 response missing Retry-After header")
	}
}
