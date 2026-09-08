package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gamarr/internal/config"
	"gamarr/internal/minerva"
	"gamarr/internal/search"
	"gamarr/internal/sources"
)

func minervaStatusEnv(t *testing.T, upstream string, seed bool) (*testEnv, *minerva.Service) {
	t.Helper()
	env := newTestEnv(t, func(c *config.Config) {
		c.Sources.Minerva = sources.MinervaSpec{Enabled: true, AssetsURL: upstream, BaseURL: upstream}
	})
	svc, err := minerva.Open(env.cfg.DataDir, env.cfg.Sources.Minerva)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if seed {
		idx, err := minerva.OpenIndex(filepath.Join(env.cfg.DataDir, "minerva", "index.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer idx.Close()
		err = idx.ReplaceCollection(context.Background(), minerva.CollectionRecord{PlatformSlug: "nds", BrowsePath: "NDS/", BundleVersion: "v1.0", TorrentURL: "https://minerva.invalid/nds.torrent", InfoHash: strings.Repeat("a", 40), ContentSHA256: "fixture"}, []minerva.FileMeta{{Index: 0, Path: "HeartGold.nds", Name: "HeartGold.nds", Size: 3}})
		if err != nil {
			t.Fatal(err)
		}
		if err := idx.SetState(context.Background(), "assets_etag", `"previous"`); err != nil {
			t.Fatal(err)
		}
		if err := idx.SetState(context.Background(), "last_sync", "2026-09-08T12:34:56Z"); err != nil {
			t.Fatal(err)
		}
	}
	env.router = NewRouter(env.cfg, env.mgr, nil, nil, nil, svc)
	return env, svc
}

func TestMinervaDisabledStatusHasNoSideEffects(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/minerva") {
			calls.Add(1)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()
	search.SetVimmMinIntervalForTest(0)
	t.Cleanup(func() { search.SetVimmMinIntervalForTest(5 * time.Second) })
	env := newTestEnv(t, func(c *config.Config) {
		c.Sources = &sources.Registry{Vimm: sources.VimmSpec{BaseURL: upstream.URL}, Minerva: sources.MinervaSpec{AssetsURL: upstream.URL + "/minerva", BaseURL: upstream.URL + "/minerva"}}
	})
	rr := env.do("GET", "/api/minerva/status", "")
	wantStatus(t, rr, 200)
	want := map[string]interface{}{"enabled": false, "ready": false, "syncing": false, "collections": float64(0), "files": float64(0)}
	if got := decodeMap(t, rr); !reflect.DeepEqual(got, want) {
		t.Fatalf("disabled status = %v, want %v", got, want)
	}
	for _, path := range []string{"/api/search?q=heartgold&platform=nds", "/api/sources", "/api/config", "/api/admin/dashboard"} {
		wantStatus(t, env.do("GET", path, ""), 200)
	}
	wantStatus(t, env.do("POST", "/api/minerva/sync", ""), 400)
	if calls.Load() != 0 {
		t.Fatalf("disabled Minerva made %d HTTP calls", calls.Load())
	}
	if _, err := os.Stat(filepath.Join(env.cfg.DataDir, "minerva")); !os.IsNotExist(err) {
		t.Fatalf("disabled Minerva created index: %v", err)
	}
}

func TestMinervaStatusAndSourceStates(t *testing.T) {
	for _, tc := range []struct {
		name, want          string
		seed, fail, syncing bool
	}{
		{"disabled", "not_configured", false, false, false},
		{"empty", "degraded", false, false, false},
		{"ready", "ok", true, false, false},
		{"failed empty", "degraded", false, true, false},
		{"failed usable", "degraded", true, true, false},
		{"syncing", "syncing", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.syncing {
					<-r.Context().Done()
					return
				}
				http.Error(w, "unavailable", 503)
			}))
			t.Cleanup(upstream.Close)
			var env *testEnv
			if tc.name == "disabled" {
				env = newTestEnv(t, nil)
			} else {
				var svc *minerva.Service
				env, svc = minervaStatusEnv(t, upstream.URL, tc.seed)
				if tc.fail {
					if _, err := svc.Sync(context.Background(), false); err == nil {
						t.Fatal("expected upstream failure")
					}
				}
				if tc.syncing {
					if err := svc.StartSync(context.Background(), false); err != nil {
						t.Fatal(err)
					}
				}
			}
			rr := env.do("GET", "/api/minerva/status", "")
			wantStatus(t, rr, 200)
			status := decodeMap(t, rr)
			count := float64(0)
			if tc.seed {
				count = 1
			}
			if status["enabled"] != (tc.name != "disabled") || status["ready"] != tc.seed || status["syncing"] != tc.syncing || status["collections"] != count || status["files"] != count {
				t.Fatalf("status = %v", status)
			}
			lastError, _ := status["last_error"].(string)
			if tc.fail && lastError == "" {
				t.Fatalf("missing sync error: %v", status)
			}
			if tc.seed && status["last_sync"] != "2026-09-08T12:34:56Z" {
				t.Fatalf("last sync = %v", status["last_sync"])
			}
			for _, endpoint := range []struct{ path, field string }{{"/api/sources", "sources"}, {"/api/config", "minerva"}, {"/api/admin/dashboard", "sources_health"}} {
				rr := env.do("GET", endpoint.path, "")
				wantStatus(t, rr, 200)
				body := decodeMap(t, rr)
				var entry map[string]interface{}
				if endpoint.field == "minerva" {
					entry, _ = body[endpoint.field].(map[string]interface{})
				} else {
					for _, raw := range body[endpoint.field].([]interface{}) {
						candidate := raw.(map[string]interface{})
						if candidate["name"] == "minerva" {
							entry = candidate
						}
					}
				}
				if entry == nil || entry["status"] != tc.want {
					t.Errorf("%s Minerva = %v, want %s", endpoint.path, entry, tc.want)
				}
				if endpoint.field == "minerva" && entry["configured"] != (tc.name != "disabled") {
					t.Errorf("Minerva configuration = %v", entry)
				}
			}
		})
	}
}

func TestMinervaManualSyncConcurrent(t *testing.T) {
	entered := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-r.Context().Done() }))
	t.Cleanup(upstream.Close)
	env, _ := minervaStatusEnv(t, upstream.URL, false)
	start := make(chan struct{})
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rr := httptest.NewRecorder()
			env.router.ServeHTTP(rr, httptest.NewRequest("POST", "/api/minerva/sync", strings.NewReader(`{"full":false}`)))
			codes <- rr.Code
		}()
	}
	close(start)
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[202] != 1 || counts[409] != 1 {
		t.Fatalf("simultaneous status codes = %v", counts)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("sync did not start")
	}
	wantStatus(t, env.do("POST", "/api/minerva/sync", ""), 409)
}

func TestMinervaManualSyncBodyAndStartErrors(t *testing.T) {
	for _, tc := range []struct{ body, validator string }{{"", `"previous"`}, {`{"full":false}`, `"previous"`}, {`{"full":true}`, ""}} {
		t.Run(tc.body, func(t *testing.T) {
			requests := make(chan string, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.Header.Get("If-None-Match")
				<-r.Context().Done()
			}))
			t.Cleanup(upstream.Close)
			env, _ := minervaStatusEnv(t, upstream.URL, true)
			wantStatus(t, env.do("POST", "/api/minerva/sync", tc.body), 202)
			select {
			case got := <-requests:
				if got != tc.validator {
					t.Fatalf("validator = %q, want %q", got, tc.validator)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("sync did not start")
			}
		})
	}
	env, svc := minervaStatusEnv(t, "http://unused.invalid", false)
	wantStatus(t, env.do("POST", "/api/minerva/sync", `{"full":"yes"}`), 400)
	if svc.Status(context.Background()).Syncing {
		t.Fatal("malformed body started sync")
	}
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, env.do("POST", "/api/minerva/sync", ""), 500)
}

func TestMinervaManualSyncRequiresAdmin(t *testing.T) {
	env, svc := minervaStatusEnv(t, "http://unused.invalid", false)
	hash, err := hashPassword("secret123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.jobs.CreateUser("reader", hash, "user"); err != nil {
		t.Fatal(err)
	}
	rr := env.do("POST", "/api/login", `{"username":"reader","password":"secret123"}`)
	wantStatus(t, rr, 200)
	token, _ := decodeMap(t, rr)["token"].(string)
	wantStatus(t, env.do("POST", "/api/minerva/sync", "", withSession(token)), 403)
	wantStatus(t, env.do("GET", "/api/minerva/status", "", withSession(token)), 200)
	if svc.Status(context.Background()).Syncing {
		t.Fatal("non-admin started sync")
	}
}
