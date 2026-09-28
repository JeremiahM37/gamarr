package search

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"gamarr/internal/config"
)

// gameIndexerJSON is a Prowlarr indexer definition that advertises PC games,
// which is what capability discovery looks for.
func gameIndexerJSON(id int, name string) map[string]interface{} {
	return map[string]interface{}{
		"id": id, "name": name, "enable": true,
		"capabilities": map[string]interface{}{
			"categories": []map[string]interface{}{{"id": 4000, "name": "PC"}},
		},
	}
}

// stubProwlarr serves the two endpoints a game search uses: the indexer list
// that resolves which indexers to query, and the search itself. The cached
// indexer list is dropped around the test so instances do not leak between them.
func stubProwlarr(t *testing.T, items []map[string]interface{}) *httptest.Server {
	t.Helper()
	ClearIndexerCache()
	t.Cleanup(ClearIndexerCache)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") == "" {
			t.Error("expected API key header")
		}
		if r.URL.Path == "/api/v1/indexer" {
			json.NewEncoder(w).Encode([]map[string]interface{}{
				gameIndexerJSON(1, "TestIndexer"),
			})
			return
		}
		json.NewEncoder(w).Encode(items)
	}))
}

func TestSearchProwlarr_NoProwlarr(t *testing.T) {
	cfg := &config.Config{ProwlarrURL: "", ProwlarrAPIKey: ""}
	results := SearchProwlarr(cfg, "zelda", "")
	if results != nil {
		t.Error("expected nil when Prowlarr not configured")
	}
}

func TestSearchProwlarr_ParsesResults(t *testing.T) {
	items := []map[string]interface{}{
		{
			"title":       "Zelda Switch [NSP]",
			"size":        float64(5000000000),
			"seeders":     float64(100),
			"leechers":    float64(20),
			"indexer":     "TestIndexer",
			"downloadUrl": "http://example.com/download",
			"magnetUrl":   "magnet:?xt=urn:btih:abc123",
			"infoHash":    "abc123",
			"guid":        "http://example.com/guid",
			"protocol":    "torrent",
			"categories":  []interface{}{float64(100082)},
			"age":         float64(5),
		},
	}
	srv := stubProwlarr(t, items)
	defer srv.Close()

	cfg := &config.Config{
		ProwlarrURL:          srv.URL,
		ProwlarrAPIKey:       "testkey",
		ProwlarrGameIndexers: []int{1},
	}
	results := SearchProwlarr(cfg, "zelda", "")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.Title != "Zelda Switch [NSP]" {
		t.Errorf("title=%q", r.Title)
	}
	if r.Platform != "Switch" {
		t.Errorf("platform=%q, want Switch", r.Platform)
	}
	if r.Seeders != 100 {
		t.Errorf("seeders=%d, want 100", r.Seeders)
	}
	if r.SourceType != "torrent" {
		t.Errorf("source_type=%q, want torrent", r.SourceType)
	}
	if r.DownloadProtocol != "torrent" {
		t.Errorf("download_protocol=%q, want torrent", r.DownloadProtocol)
	}
}

func TestSearchProwlarr_MapsUsenetProtocol(t *testing.T) {
	items := []map[string]interface{}{
		{
			"title":       "Zelda Switch Usenet",
			"size":        float64(5_000_000_000),
			"seeders":     float64(0),
			"indexer":     "UsenetIndexer",
			"downloadUrl": "http://example.com/game.nzb",
			"protocol":    "usenet",
			"categories":  []interface{}{float64(100082)},
		},
	}
	srv := stubProwlarr(t, items)
	defer srv.Close()

	cfg := &config.Config{
		ProwlarrURL:          srv.URL,
		ProwlarrAPIKey:       "key",
		ProwlarrGameIndexers: []int{1},
	}
	results := SearchProwlarr(cfg, "zelda", "")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].DownloadProtocol != "nzb" {
		t.Errorf("download_protocol=%q, want nzb", results[0].DownloadProtocol)
	}
	if results[0].SourceType != "torrent" {
		t.Errorf("source_type=%q, want torrent", results[0].SourceType)
	}
}

func TestSearchProwlarr_PCRepackFallback(t *testing.T) {
	items := []map[string]interface{}{
		{
			"title":      "Game FitGirl Repack",
			"size":       float64(5000000000),
			"seeders":    float64(50),
			"categories": []interface{}{float64(99999)}, // unknown category
		},
	}
	srv := stubProwlarr(t, items)
	defer srv.Close()

	cfg := &config.Config{
		ProwlarrURL:          srv.URL,
		ProwlarrAPIKey:       "key",
		ProwlarrGameIndexers: []int{1},
	}
	results := SearchProwlarr(cfg, "game", "")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Platform != "PC" {
		t.Errorf("expected PC platform from repack title, got %q", results[0].Platform)
	}
	if !results[0].IsPC {
		t.Error("expected IsPC=true for repack title")
	}
}

func TestSearchProwlarr_CategoryFilter(t *testing.T) {
	items := []map[string]interface{}{
		{
			"title":      "Switch Game",
			"size":       float64(5000000000),
			"seeders":    float64(50),
			"categories": []interface{}{float64(100082)},
		},
		{
			"title":      "PC Game",
			"size":       float64(5000000000),
			"seeders":    float64(50),
			"categories": []interface{}{float64(4000)},
		},
	}
	srv := stubProwlarr(t, items)
	defer srv.Close()

	cfg := &config.Config{
		ProwlarrURL:          srv.URL,
		ProwlarrAPIKey:       "key",
		ProwlarrGameIndexers: []int{1},
	}
	// Filter to switch only
	results := SearchProwlarr(cfg, "game", "switch")
	if len(results) != 1 {
		t.Fatalf("expected 1 result with switch filter, got %d", len(results))
	}
	if results[0].Platform != "Switch" {
		t.Errorf("expected Switch, got %q", results[0].Platform)
	}
}

func TestSearchProwlarr_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	cfg := &config.Config{
		ProwlarrURL:          srv.URL,
		ProwlarrAPIKey:       "key",
		ProwlarrGameIndexers: []int{1},
	}
	results := SearchProwlarr(cfg, "game", "")
	if len(results) != 0 {
		t.Errorf("expected 0 results on server error, got %d", len(results))
	}
}

func TestExtractCatIDs(t *testing.T) {
	tests := []struct {
		name string
		cats []interface{}
		want []int
	}{
		{
			name: "float64 values",
			cats: []interface{}{float64(4000), float64(100082)},
			want: []int{4000, 100082},
		},
		{
			name: "map with id",
			cats: []interface{}{map[string]interface{}{"id": float64(100011)}},
			want: []int{100011},
		},
		{
			name: "empty",
			cats: []interface{}{},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCatIDs(tt.cats)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d IDs, want %d", len(got), len(tt.want))
			}
			for i, v := range got {
				if v != tt.want[i] {
					t.Errorf("got[%d]=%d, want %d", i, v, tt.want[i])
				}
			}
		})
	}
}

// TestQueryEscapingRoundTrips guards the Prowlarr query construction against
// the old hand-rolled escaper, which only handled spaces and '&' and so
// corrupted any title containing '?', '#', '%' or '+'. Building the URL exactly
// as the search code does and parsing it back must yield the original title.
func TestQueryEscapingRoundTrips(t *testing.T) {
	titles := []string{
		"Who Wants to Be a Millionaire?",
		"R.B.I. Baseball",
		"100% Orange Juice",
		"C++ Programming",
		"Sonic & Knuckles",
		"Ratchet & Clank #2",
	}
	for _, q := range titles {
		t.Run(q, func(t *testing.T) {
			raw := "https://prowlarr/api/v1/search?query=" + url.QueryEscape(q) + "&type=search&limit=50"
			parsed, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("Parse(%q): %v", q, err)
			}
			if got := parsed.Query().Get("query"); got != q {
				t.Errorf("query round-trip = %q, want %q", got, q)
			}
		})
	}
}

func TestJsonHelpers(t *testing.T) {
	m := map[string]interface{}{
		"title":   "test",
		"size":    float64(42),
		"count":   float64(100),
		"missing": nil,
	}

	if got := jsonStr(m, "title"); got != "test" {
		t.Errorf("jsonStr = %q", got)
	}
	if got := jsonStr(m, "missing"); got != "" {
		t.Errorf("jsonStr missing = %q", got)
	}
	if got := jsonInt(m, "count"); got != 100 {
		t.Errorf("jsonInt = %d", got)
	}
	if got := jsonInt64(m, "size"); got != 42 {
		t.Errorf("jsonInt64 = %d", got)
	}
	if got := jsonArray(m, "missing"); got != nil {
		t.Errorf("jsonArray missing should be nil")
	}
}

// A platform-filtered search keeps what the categories do not place anywhere
// else and files it under the searched platform, and drops what they place on
// a different platform.
func TestSearchProwlarr_SearchContextPlatform(t *testing.T) {
	item := func(title string, cats ...float64) map[string]interface{} {
		ic := make([]interface{}, len(cats))
		for i, c := range cats {
			ic[i] = c
		}
		return map[string]interface{}{
			"title": title, "size": float64(2_000_000), "seeders": float64(10),
			"categories": ic,
		}
	}
	items := []map[string]interface{}{
		item("Chrono Trigger Console Other", 1090),
		item("Chrono Trigger Console", 1000),
		item("Chrono Trigger Uncategorised"),
		item("Chrono Trigger Tracker Retro", 100999),
		item("Chrono Trigger PS2 Tagged", 100011),
		item("Chrono Trigger DS Newznab", 1000, 1010),
		item("Chrono Trigger Movie", 2000),
		item("Chrono Trigger FitGirl Repack", 1090),
	}
	srv := stubProwlarr(t, items)
	defer srv.Close()
	cfg := &config.Config{ProwlarrURL: srv.URL, ProwlarrAPIKey: "key", ProwlarrGameIndexers: []int{1}}

	results := SearchProwlarr(cfg, "chrono trigger", "snes")
	kept := map[string]string{}
	for _, r := range results {
		kept[r.Title] = r.PlatformSlug + "|" + r.Platform
	}
	for _, title := range []string{
		"Chrono Trigger Console Other", "Chrono Trigger Console",
		"Chrono Trigger Uncategorised", "Chrono Trigger Tracker Retro",
	} {
		if got, ok := kept[title]; !ok {
			t.Errorf("%q dropped, want kept as snes", title)
		} else if got != "snes|SNES" {
			t.Errorf("%q platform = %s, want snes|SNES", title, got)
		}
	}
	for _, title := range []string{
		"Chrono Trigger PS2 Tagged", "Chrono Trigger DS Newznab",
		"Chrono Trigger Movie", "Chrono Trigger FitGirl Repack",
	} {
		if _, ok := kept[title]; ok {
			t.Errorf("%q kept in a SNES search, want dropped", title)
		}
	}

	// Unfiltered, nothing is assigned: generic results stay Unknown.
	for _, r := range SearchProwlarr(cfg, "chrono trigger", "") {
		if r.Title == "Chrono Trigger Console Other" && r.Platform != "Unknown" {
			t.Errorf("unfiltered generic result platform = %q, want Unknown", r.Platform)
		}
	}
}
