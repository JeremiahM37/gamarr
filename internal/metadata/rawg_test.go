package metadata

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// rewriteTransport redirects every request to the given test server host,
// so no live network calls are ever made.
type rewriteTransport struct {
	host string // host:port of the httptest server
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = rt.host
	return http.DefaultTransport.RoundTrip(clone)
}

// failTransport errors on any request — used to prove a code path makes no
// HTTP calls at all.
type failTransport struct{}

func (failTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected network call")
}

// newTestClient returns a Client wired to an httptest server running handler.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := NewClient("test-key")
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	c.httpClient.Transport = rewriteTransport{host: u.Host}
	return c
}

const searchResponseJSON = `{
	"count": 2,
	"results": [
		{
			"id": 123,
			"name": "Chrono Trigger",
			"slug": "chrono-trigger",
			"released": "1995-03-11",
			"rating": 4.6,
			"metacritic": 92,
			"background_image": "https://example.test/ct.jpg",
			"genres": [{"id": 1, "name": "RPG"}, {"id": 2, "name": "Adventure"}],
			"platforms": [{"platform": {"id": 79, "name": "SNES"}}],
			"esrb_rating": {"id": 1, "name": "Everyone"}
		},
		{"id": 456, "name": "Chrono Cross", "slug": "chrono-cross"}
	]
}`

func TestNewClientEnabled(t *testing.T) {
	tests := []struct {
		name   string
		apiKey string
		want   bool
	}{
		{"with key", "abc123", true},
		{"empty key", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClient(tt.apiKey)
			if got := c.Enabled(); got != tt.want {
				t.Errorf("Enabled() = %v, want %v", got, tt.want)
			}
			if c.cache == nil {
				t.Error("cache not initialized")
			}
			if c.httpClient == nil {
				t.Error("httpClient not initialized")
			}
		})
	}
}

func TestDisabledClientMakesNoRequests(t *testing.T) {
	c := NewClient("")
	c.httpClient.Transport = failTransport{}

	t.Run("SearchGame", func(t *testing.T) {
		meta, err := c.SearchGame("mario", "snes")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if meta != nil {
			t.Errorf("expected nil metadata, got %+v", meta)
		}
	})

	t.Run("GetGame", func(t *testing.T) {
		meta, err := c.GetGame(42)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if meta != nil {
			t.Errorf("expected nil metadata, got %+v", meta)
		}
	})
}

func TestSearchGameSuccess(t *testing.T) {
	var gotQuery url.Values
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/games" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(searchResponseJSON))
	})

	meta, err := c.SearchGame("chrono trigger", "snes")
	if err != nil {
		t.Fatalf("SearchGame: %v", err)
	}
	if meta == nil {
		t.Fatal("expected metadata, got nil")
	}

	// Request params.
	if got := gotQuery.Get("key"); got != "test-key" {
		t.Errorf("key param = %q, want %q", got, "test-key")
	}
	if got := gotQuery.Get("search"); got != "chrono trigger" {
		t.Errorf("search param = %q", got)
	}
	if got := gotQuery.Get("page_size"); got != "5" {
		t.Errorf("page_size param = %q, want 5", got)
	}
	if got := gotQuery.Get("search_precise"); got != "true" {
		t.Errorf("search_precise param = %q, want true", got)
	}
	if got := gotQuery.Get("platforms"); got != "79" {
		t.Errorf("platforms param = %q, want 79 (snes)", got)
	}

	// First result mapped.
	if meta.ID != 123 || meta.Name != "Chrono Trigger" || meta.Slug != "chrono-trigger" {
		t.Errorf("unexpected identity fields: %+v", meta)
	}
	if meta.Released != "1995-03-11" || meta.Rating != 4.6 || meta.Metacritic != 92 {
		t.Errorf("unexpected detail fields: %+v", meta)
	}
	if meta.BackgroundImage != "https://example.test/ct.jpg" {
		t.Errorf("BackgroundImage = %q", meta.BackgroundImage)
	}
	if len(meta.Genres) != 2 || meta.Genres[0] != "RPG" || meta.Genres[1] != "Adventure" {
		t.Errorf("Genres = %v", meta.Genres)
	}
	if len(meta.Platforms) != 1 || meta.Platforms[0] != "SNES" {
		t.Errorf("Platforms = %v", meta.Platforms)
	}
	if meta.ESRB != "Everyone" {
		t.Errorf("ESRB = %q", meta.ESRB)
	}
}

func TestSearchGameUnknownPlatformOmitsParam(t *testing.T) {
	var gotQuery url.Values
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Write([]byte(searchResponseJSON))
	})

	if _, err := c.SearchGame("chrono trigger", "amiga"); err != nil {
		t.Fatalf("SearchGame: %v", err)
	}
	if _, present := gotQuery["platforms"]; present {
		t.Errorf("platforms param should be absent for unknown slug, got %q", gotQuery.Get("platforms"))
	}
}

func TestSearchGameNoResults(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"count": 0, "results": []}`))
	})

	meta, err := c.SearchGame("nonexistent game xyz", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta != nil {
		t.Errorf("expected nil for no results, got %+v", meta)
	}
}

func TestSearchGameHTTPError(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantSub string
	}{
		{"server error", http.StatusInternalServerError, "HTTP 500"},
		{"unauthorized", http.StatusUnauthorized, "HTTP 401"},
		{"rate limited", http.StatusTooManyRequests, "HTTP 429"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
			})
			meta, err := c.SearchGame("mario", "")
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantSub)
			}
			if meta != nil {
				t.Errorf("expected nil metadata on error, got %+v", meta)
			}
		})
	}
}

func TestSearchGameMalformedJSON(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"count": 1, "results": [{`))
	})
	meta, err := c.SearchGame("mario", "")
	if err == nil {
		t.Fatal("expected JSON decode error, got nil")
	}
	if meta != nil {
		t.Errorf("expected nil metadata, got %+v", meta)
	}
}

func TestSearchGameCache(t *testing.T) {
	var requests int64
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		w.Write([]byte(searchResponseJSON))
	})

	first, err := c.SearchGame("Chrono Trigger", "snes")
	if err != nil {
		t.Fatalf("first search: %v", err)
	}

	// Same query (different case) + same platform must be a cache hit.
	second, err := c.SearchGame("chrono TRIGGER", "snes")
	if err != nil {
		t.Fatalf("second search: %v", err)
	}
	if got := atomic.LoadInt64(&requests); got != 1 {
		t.Errorf("expected 1 HTTP request after cache hit, got %d", got)
	}
	if first != second {
		t.Error("cache hit should return the same cached entry")
	}

	// Different platform slug is a different cache key.
	if _, err := c.SearchGame("chrono trigger", "nds"); err != nil {
		t.Fatalf("third search: %v", err)
	}
	if got := atomic.LoadInt64(&requests); got != 2 {
		t.Errorf("expected 2 HTTP requests after new platform, got %d", got)
	}
}

func TestGetGameSuccess(t *testing.T) {
	longDesc := strings.Repeat("a", 1500)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/games/123" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("key"); got != "test-key" {
			t.Errorf("key param = %q", got)
		}
		w.Write([]byte(`{
			"id": 123,
			"name": "Chrono Trigger",
			"slug": "chrono-trigger",
			"description_raw": "` + longDesc + `",
			"released": "1995-03-11",
			"rating": 4.6,
			"metacritic": 92,
			"background_image": "https://example.test/ct.jpg",
			"genres": [{"id": 1, "name": "RPG"}],
			"platforms": [{"platform": {"id": 79, "name": "SNES"}}],
			"developers": [{"id": 10, "name": "Square"}],
			"publishers": [{"id": 20, "name": "Square Enix"}, {"id": 21, "name": "Nintendo"}],
			"esrb_rating": {"id": 1, "name": "Everyone"}
		}`))
	})

	meta, err := c.GetGame(123)
	if err != nil {
		t.Fatalf("GetGame: %v", err)
	}
	if meta == nil {
		t.Fatal("expected metadata, got nil")
	}
	if meta.ID != 123 || meta.Name != "Chrono Trigger" {
		t.Errorf("unexpected identity: %+v", meta)
	}
	// Description truncated to 1000 chars + "...".
	if len(meta.Description) != 1003 || !strings.HasSuffix(meta.Description, "...") {
		t.Errorf("description not truncated: len=%d", len(meta.Description))
	}
	if len(meta.Developers) != 1 || meta.Developers[0] != "Square" {
		t.Errorf("Developers = %v", meta.Developers)
	}
	if len(meta.Publishers) != 2 || meta.Publishers[0] != "Square Enix" || meta.Publishers[1] != "Nintendo" {
		t.Errorf("Publishers = %v", meta.Publishers)
	}
	if meta.ESRB != "Everyone" {
		t.Errorf("ESRB = %q", meta.ESRB)
	}
}

func TestGetGameNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	meta, err := c.GetGame(999)
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if !strings.Contains(err.Error(), "game not found") {
		t.Errorf("error %q should mention game not found", err.Error())
	}
	if meta != nil {
		t.Errorf("expected nil metadata, got %+v", meta)
	}
}

func TestGetGameHTTPError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	_, err := c.GetGame(1)
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("expected HTTP 502 error, got %v", err)
	}
}

func TestGetGameMalformedJSON(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not json at all`))
	})
	meta, err := c.GetGame(1)
	if err == nil {
		t.Fatal("expected JSON decode error, got nil")
	}
	if meta != nil {
		t.Errorf("expected nil metadata, got %+v", meta)
	}
}

func TestGetGameCache(t *testing.T) {
	var requests int64
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		w.Write([]byte(`{"id": 7, "name": "Doom", "slug": "doom"}`))
	})

	first, err := c.GetGame(7)
	if err != nil {
		t.Fatalf("first GetGame: %v", err)
	}
	second, err := c.GetGame(7)
	if err != nil {
		t.Fatalf("second GetGame: %v", err)
	}
	if got := atomic.LoadInt64(&requests); got != 1 {
		t.Errorf("expected 1 HTTP request after cache hit, got %d", got)
	}
	if first != second {
		t.Error("cache hit should return the same cached entry")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		maxLen int
		want   string
	}{
		{"shorter than max", "hello", 10, "hello"},
		{"exactly max", "hello", 5, "hello"},
		{"longer than max", "hello world", 5, "hello..."},
		{"empty", "", 5, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncate(tt.in, tt.maxLen); got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestMapPlatformSlugToRAWG(t *testing.T) {
	tests := []struct {
		slug string
		want string
	}{
		{"pc", "4"},
		{"snes", "79"},
		{"switch", "7"},
		{"ps5", "187"},
		{"n64", "83"},
		{"gamegear", "77"},
		{"amiga", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run("slug_"+tt.slug, func(t *testing.T) {
			if got := mapPlatformSlugToRAWG(tt.slug); got != tt.want {
				t.Errorf("mapPlatformSlugToRAWG(%q) = %q, want %q", tt.slug, got, tt.want)
			}
		})
	}
}

func TestMapRAWGPlatformToSlug(t *testing.T) {
	// NOTE: only unambiguous names are tested here. Names where one map key is
	// a substring of another matching name (e.g. "Xbox 360" contains "xbox",
	// "SNES" contains "nes") resolve nondeterministically because the function
	// iterates a map — see bug report.
	tests := []struct {
		name string
		want string
	}{
		{"PC", "pc"},
		{"Nintendo 64", "n64"},
		{"Dreamcast", "dc"},
		{"PSP", "psp"},
		{"GameCube", "ngc"},
		{"Sega Saturn", "saturn"},
		{"Game Gear", "gamegear"},
		{"Nintendo%2064", "n64"}, // URL-encoded input is decoded
		{"Atari 2600", ""},       // unmapped
		{"", ""},
	}
	for _, tt := range tests {
		t.Run("name_"+tt.name, func(t *testing.T) {
			if got := MapRAWGPlatformToSlug(tt.name); got != tt.want {
				t.Errorf("MapRAWGPlatformToSlug(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}
