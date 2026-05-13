package torznab

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gamarr/internal/models"
)

func emptySearch(ctx context.Context, query, slug string) []*models.SearchResult {
	return nil
}

func TestHandler_Caps(t *testing.T) {
	h := New("", emptySearch)
	req := httptest.NewRequest("GET", "/torznab/api?t=caps", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("caps HTTP %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"<caps", `title="Gamarr"`, `id="1000"`, `id="4070"`, `name="Console/NDS"`} {
		if !strings.Contains(body, want) {
			t.Errorf("caps body missing %q\n%s", want, body)
		}
	}
}

func TestHandler_NoAPIKey_AcceptsRequest(t *testing.T) {
	h := New("", emptySearch)
	req := httptest.NewRequest("GET", "/torznab/api?t=caps", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("expected 200 when no API key configured, got %d", rr.Code)
	}
}

func TestHandler_APIKey_Required(t *testing.T) {
	h := New("the-key", emptySearch)
	cases := []struct {
		name string
		url  string
		want int
	}{
		{"missing apikey", "/torznab/api?t=caps", 401},
		{"wrong apikey", "/torznab/api?t=caps&apikey=nope", 401},
		{"correct apikey", "/torznab/api?t=caps&apikey=the-key", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.url, nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Errorf("got %d, want %d", rr.Code, tc.want)
			}
		})
	}
}

func TestHandler_EmptySearch_ReturnsProbePlaceholder(t *testing.T) {
	// Prowlarr's indexer-discovery probe sends t=search with no q. We must
	// return a well-formed RSS with at least one item or Prowlarr fails the
	// indexer test.
	h := New("", emptySearch)
	req := httptest.NewRequest("GET", "/torznab/api?t=search", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("HTTP %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"<rss", "<channel", "<item", "<pubDate>", "<enclosure ", `name="category"`, `name="seeders"`, "gamarr-placeholder-"} {
		if !strings.Contains(body, want) {
			t.Errorf("probe response missing %q\n%s", want, body)
		}
	}
}

func TestHandler_Search_RunsSearchFunc(t *testing.T) {
	var gotQuery, gotPlatform string
	searchFn := func(ctx context.Context, query, slug string) []*models.SearchResult {
		gotQuery = query
		gotPlatform = slug
		return []*models.SearchResult{
			{
				Title:        "Test Game (USA).zip",
				Indexer:      "test-indexer",
				Size:         1234567,
				Seeders:      10,
				Leechers:     2,
				MagnetURL:    "magnet:?xt=urn:btih:DEADBEEF",
				InfoHash:     "DEADBEEF",
				GUID:         "deadbeef-guid",
				Platform:     "NES",
				PlatformSlug: "nes",
				SourceType:   "torrent",
			},
		}
	}
	h := New("", searchFn)
	req := httptest.NewRequest("GET", "/torznab/api?t=search&q=test&platform=nes", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("HTTP %d: %s", rr.Code, rr.Body.String())
	}
	if gotQuery != "test" {
		t.Errorf("query passed to searchFn = %q, want test", gotQuery)
	}
	if gotPlatform != "nes" {
		t.Errorf("platform passed to searchFn = %q, want nes", gotPlatform)
	}
	body := rr.Body.String()
	for _, want := range []string{"<item>", "<title>Test Game (USA).zip</title>", "DEADBEEF", `name="seeders" value="10"`, "Console/Other"} {
		if !strings.Contains(body, want) {
			t.Errorf("search response missing %q\n%s", want, body)
		}
	}
}

func TestHandler_UnknownFunction_Returns400Error(t *testing.T) {
	h := New("", emptySearch)
	req := httptest.NewRequest("GET", "/torznab/api?t=zalgo", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Errorf("expected 400 for unknown function, got %d", rr.Code)
	}
	var e Error
	if err := xml.Unmarshal(rr.Body.Bytes(), &e); err != nil {
		t.Fatalf("body not a valid Torznab error: %v", err)
	}
	if e.Code != "202" {
		t.Errorf("error code = %q, want 202", e.Code)
	}
}

func TestHandler_AliasPath(t *testing.T) {
	h := New("", emptySearch)
	req := httptest.NewRequest("GET", "/api?t=caps", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTPAlias(rr, req)
	if rr.Code != 200 {
		t.Errorf("alias path should accept caps, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "<caps") {
		t.Error("alias path should return caps document")
	}
}

func TestHandler_HostHeaderUsedInProbe(t *testing.T) {
	h := New("", emptySearch)
	req := httptest.NewRequest("GET", "/torznab/api?t=search", nil)
	req.Host = "gamarr.example.com:5001"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), "gamarr.example.com:5001") {
		t.Errorf("probe response should include Host: got %s", rr.Body.String())
	}
}

func TestResultToItem_CategoryFromPlatform(t *testing.T) {
	cases := []struct {
		slug, wantCat, wantName string
	}{
		{"pc", "4070", "PC/Games"},
		{"nds", "1010", "Console/NDS"},
		{"xbox360", "1050", "Console/Xbox 360"},
		{"nes", "1090", "Console/Other"},
		{"unknown", "1090", "Console/Other"},
	}
	for _, tc := range cases {
		t.Run(tc.slug, func(t *testing.T) {
			r := &models.SearchResult{Title: "x", PlatformSlug: tc.slug, MagnetURL: "magnet:?xt=urn:btih:X"}
			it := ResultToItem(r)
			if it.Category != tc.wantName {
				t.Errorf("category name = %q, want %q", it.Category, tc.wantName)
			}
			found := false
			for _, a := range it.Attrs {
				if a.Name == "category" && a.Value == tc.wantCat {
					found = true
				}
			}
			if !found {
				t.Errorf("attr category=%s not in attrs: %+v", tc.wantCat, it.Attrs)
			}
		})
	}
}

func TestResultToItem_LinkSelection(t *testing.T) {
	cases := []struct {
		name string
		r    *models.SearchResult
		want string
	}{
		{"magnet wins", &models.SearchResult{Title: "x", MagnetURL: "magnet:?xt=urn:btih:A", DownloadURL: "http://example.test/", InfoHash: "B"}, "magnet:?xt=urn:btih:A"},
		{"download url when no magnet", &models.SearchResult{Title: "x", DownloadURL: "http://example.test/", InfoHash: "B"}, "http://example.test/"},
		{"info_hash synthesizes magnet", &models.SearchResult{Title: "x", InfoHash: "DEAD"}, "magnet:?xt=urn:btih:DEAD&dn=x"},
		{"DDL with only GUID", &models.SearchResult{Title: "x", GUID: "https://example.test/detail/42"}, "https://example.test/detail/42"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			it := ResultToItem(tc.r)
			if it.Link != tc.want {
				t.Errorf("link = %q, want %q", it.Link, tc.want)
			}
		})
	}
}

// ensure handler is reachable via stdlib http
var _ http.Handler = (*Handler)(nil)
