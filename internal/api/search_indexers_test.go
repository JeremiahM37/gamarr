package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"gamarr/internal/config"
	"gamarr/internal/search"
	"gamarr/internal/sources"
)

// deadRegistry points the DDL sources at a port that refuses connections, so a
// search test exercises the Prowlarr path without touching the network.
func deadRegistry(t *testing.T) *sources.Registry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sources.json")
	body := `{"version":1,
	  "myrient":{"base_url":"http://127.0.0.1:1/","platform_paths":{}},
	  "vimm":{"base_url":"http://127.0.0.1:1/","platform_systems":{}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	return sources.Load(path, "")
}

// A search response has to say which indexers were consulted: an empty result
// list otherwise cannot distinguish "not on your trackers" from "your trackers
// were never asked".
func TestSearchResponseNamesTheIndexersItQueried(t *testing.T) {
	search.ClearIndexerCache()
	t.Cleanup(search.ClearIndexerCache)

	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/indexer" {
			json.NewEncoder(w).Encode([]map[string]interface{}{
				{"id": 3, "name": "BooksOnly", "enable": true,
					"capabilities": map[string]interface{}{
						"categories": []map[string]interface{}{{"id": 7000}}}},
				{"id": 16, "name": "GamesTracker", "enable": true,
					"capabilities": map[string]interface{}{
						"categories": []map[string]interface{}{{"id": 4000}}}},
			})
			return
		}
		if got := r.URL.Query().Get("indexerIds"); got != "16" {
			t.Errorf("searched indexerIds=%q, want only the games tracker 16", got)
		}
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{"title": "Stardew Harvest [FitGirl Repack]", "size": float64(1 << 30),
				"seeders": float64(9), "indexer": "GamesTracker",
				"downloadUrl": "http://127.0.0.1:1/x.torrent",
				"categories":  []interface{}{float64(4050)}},
		})
	}))
	defer prowlarr.Close()

	env := newTestEnv(t, func(c *config.Config) {
		c.ProwlarrURL, c.ProwlarrAPIKey = prowlarr.URL, "key"
		c.Sources = deadRegistry(t)
	})

	rr := env.do("GET", "/api/search?q=stardew&platform=pc", "")
	wantStatus(t, rr, 200)
	body := decodeMap(t, rr)

	srcs, ok := body["sources"].([]interface{})
	if !ok || len(srcs) == 0 {
		t.Fatalf("sources missing: %v", body)
	}
	prowlarrMeta, _ := srcs[0].(map[string]interface{})
	indexers, ok := prowlarrMeta["indexers"].([]interface{})
	if !ok {
		t.Fatalf("prowlarr source carries no indexers: %v", prowlarrMeta)
	}
	if len(indexers) != 1 {
		t.Fatalf("reported %d indexers, want 1: %v", len(indexers), indexers)
	}
	first, _ := indexers[0].(map[string]interface{})
	if first["name"] != "GamesTracker" || first["id"] != float64(16) {
		t.Errorf("reported %v, want the games tracker", first)
	}
}

// With Prowlarr unconfigured the field is present and empty rather than absent,
// so the UI has one shape to render.
func TestSearchResponseWithoutProwlarr(t *testing.T) {
	env := newTestEnv(t, func(c *config.Config) { c.Sources = deadRegistry(t) })

	rr := env.do("GET", "/api/search?q=stardew&platform=pc", "")
	wantStatus(t, rr, 200)

	srcs, _ := decodeMap(t, rr)["sources"].([]interface{})
	prowlarrMeta, _ := srcs[0].(map[string]interface{})
	indexers, ok := prowlarrMeta["indexers"].([]interface{})
	if !ok || len(indexers) != 0 {
		t.Errorf("indexers = %v, want an empty list", prowlarrMeta["indexers"])
	}
	if prowlarrMeta["enabled"] != false {
		t.Errorf("enabled = %v, want false", prowlarrMeta["enabled"])
	}
}
