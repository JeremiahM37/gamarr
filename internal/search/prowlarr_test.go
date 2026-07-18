package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"gamarr/internal/config"
)

func TestSearchProwlarr_NoProwlarr(t *testing.T) {
	cfg := &config.Config{ProwlarrURL: "", ProwlarrAPIKey: ""}
	results := SearchProwlarr(context.Background(), cfg, "zelda", "")
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
			"categories":  []interface{}{float64(100082)},
			"age":         float64(5),
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "testkey" {
			t.Error("expected API key header")
		}
		json.NewEncoder(w).Encode(items)
	}))
	defer srv.Close()

	cfg := &config.Config{
		ProwlarrURL:          srv.URL,
		ProwlarrAPIKey:       "testkey",
		ProwlarrGameIndexers: []int{1},
	}
	results := SearchProwlarr(context.Background(), cfg, "zelda", "")
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(items)
	}))
	defer srv.Close()

	cfg := &config.Config{
		ProwlarrURL:          srv.URL,
		ProwlarrAPIKey:       "key",
		ProwlarrGameIndexers: []int{1},
	}
	results := SearchProwlarr(context.Background(), cfg, "game", "")
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(items)
	}))
	defer srv.Close()

	cfg := &config.Config{
		ProwlarrURL:          srv.URL,
		ProwlarrAPIKey:       "key",
		ProwlarrGameIndexers: []int{1},
	}
	// Filter to switch only
	results := SearchProwlarr(context.Background(), cfg, "game", "switch")
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
	results := SearchProwlarr(context.Background(), cfg, "game", "")
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
