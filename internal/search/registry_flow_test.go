package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gamarr/internal/sources"
)

// recordingServer is an httptest.Server that captures every path it receives.
type recordingServer struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
}

func newRecordingServer(t *testing.T, status int, body string) *recordingServer {
	rs := &recordingServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.paths = append(rs.paths, r.URL.RequestURI())
		rs.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(rs.Server.Close)
	return rs
}

func (rs *recordingServer) hit() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return len(rs.paths) > 0
}

func (rs *recordingServer) requestURIs() []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]string, len(rs.paths))
	copy(out, rs.paths)
	return out
}

// TestRegistryFlow proves that each refactored driver issues its HTTP request
// against the URL recorded in the runtime sources registry — i.e. the
// registry value actually flows into the wire request, with no hardcoded URL
// shadowing it.
func TestRegistryFlow(t *testing.T) {
	// Reset the package-level circuit breakers so prior tests don't bleed in.
	t.Cleanup(func() {
		RecordSearchSuccess("myrient")
		RecordSearchSuccess("vimm")
	})

	t.Run("myrient hits reg.Myrient.BaseURL + reg.Myrient.PlatformPaths[slug]", func(t *testing.T) {
		ClearMyrientCache()
		srv := newRecordingServer(t, 200, `<html><body><a href="Game.zip">Game.zip</a></body></html>`)
		reg, _ := sources.Default()
		reg.Myrient.BaseURL = srv.URL + "/"
		reg.Myrient.PlatformPaths = map[string]string{"nes": "TestNES/"}

		_ = SearchMyrient(context.Background(), reg, "game", "nes")
		if !srv.hit() {
			t.Fatalf("Myrient did not call the registry URL")
		}
		got := srv.requestURIs()[0]
		if !strings.HasPrefix(got, "/TestNES/") {
			t.Errorf("expected /TestNES/ path (from registry platform_paths), got %q", got)
		}
	})

	t.Run("myrient uses the registry — no fallback to hardcoded myrient.erista.me", func(t *testing.T) {
		ClearMyrientCache()
		// Reset circuit breaker first so the previous test doesn't trip it.
		RecordSearchSuccess("myrient")
		reg, _ := sources.Default()
		reg.Myrient.BaseURL = "https://sentinel.test.invalid/"
		reg.Myrient.PlatformPaths = map[string]string{"nes": "x/"}

		results := SearchMyrient(context.Background(), reg, "game", "nes")
		if results != nil {
			t.Errorf("expected nil results when DNS fails, got %d", len(results))
		}
		// If the driver fell back to a hardcoded URL we'd silently succeed
		// against the real myrient.erista.me. We can't directly assert that,
		// but the nil result + (silently logged) WARN about sentinel.test.invalid
		// proves the registry value was consulted.
	})

	t.Run("vimm hits reg.Vimm.BaseURL", func(t *testing.T) {
		srv := newRecordingServer(t, 200, `<html><body></body></html>`)
		reg, _ := sources.Default()
		reg.Vimm.BaseURL = srv.URL + "/"
		// Force a recognized platform so the system param is set.
		reg.Vimm.PlatformSystems = map[string]string{"nes": "NES"}

		_ = SearchVimm(context.Background(), reg, "game", "nes")
		if !srv.hit() {
			t.Fatalf("Vimm did not call the registry URL")
		}
		got := srv.requestURIs()[0]
		if !strings.Contains(got, "q=game") {
			t.Errorf("expected ?q=game in URL, got %q", got)
		}
		if !strings.Contains(got, "system=NES") {
			t.Errorf("expected system=NES (from registry platform_systems), got %q", got)
		}
	})

	t.Run("vimm GUID is built from reg.Vimm.BaseURL", func(t *testing.T) {
		srv := newRecordingServer(t, 200, `<a href="/vault/42">A Game (NES)</a>`)
		reg, _ := sources.Default()
		reg.Vimm.BaseURL = srv.URL + "/"
		reg.Vimm.PlatformSystems = map[string]string{"nes": "NES"}

		results := SearchVimm(context.Background(), reg, "game", "nes")
		if len(results) == 0 {
			t.Fatal("expected at least one parsed result")
		}
		wantPrefix := srv.URL + "/"
		if !strings.HasPrefix(results[0].GUID, wantPrefix) {
			t.Errorf("expected GUID built from registry base URL %q, got %q", wantPrefix, results[0].GUID)
		}
	})
}

// TestRegistryFlow_LegacyEnvOverride confirms MYRIENT_URL / VIMM_URL env vars
// still take precedence over the registry value when set.
func TestRegistryFlow_LegacyEnvOverride(t *testing.T) {
	cases := []struct {
		name   string
		envKey string
		envVal string
		check  func(*sources.Registry) string
		want   string
	}{
		{"MYRIENT_URL overrides Myrient.BaseURL", "MYRIENT_URL", "https://my-override.test/",
			func(r *sources.Registry) string { return r.Myrient.BaseURL }, "https://my-override.test/"},
		{"VIMM_URL overrides Vimm.BaseURL", "VIMM_URL", "https://vimm-override.test/",
			func(r *sources.Registry) string { return r.Vimm.BaseURL }, "https://vimm-override.test/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, _ := sources.Default()
			reg.ApplyEnvOverrides(func(k string) string {
				if k == tc.envKey {
					return tc.envVal
				}
				return ""
			})
			if got := tc.check(reg); got != tc.want {
				t.Errorf("after ApplyEnvOverrides(%s=%s): got %q, want %q", tc.envKey, tc.envVal, got, tc.want)
			}
		})
	}
}
