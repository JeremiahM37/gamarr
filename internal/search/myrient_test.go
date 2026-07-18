package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gamarr/internal/sources"
)

// testRegistry returns the embedded default registry for tests.
func testRegistry(t *testing.T) *sources.Registry {
	t.Helper()
	r, err := sources.Default()
	if err != nil {
		t.Fatalf("load default registry: %v", err)
	}
	return r
}

func TestMyrientPlatformSlugs(t *testing.T) {
	reg := testRegistry(t)
	slugs := MyrientPlatformSlugs(reg)
	if len(slugs) == 0 {
		t.Fatal("expected non-empty slug list")
	}
	// Check known platforms
	found := make(map[string]bool)
	for _, s := range slugs {
		found[s] = true
	}
	for _, want := range []string{"gba", "nes", "snes", "n64", "psx", "ngc"} {
		if !found[want] {
			t.Errorf("expected slug %q in Myrient platforms", want)
		}
	}
}

func TestSearchMyrient_RequiresPlatform(t *testing.T) {
	reg := testRegistry(t)
	// Without a platform, SearchMyrient returns nil
	results := SearchMyrient(context.Background(), reg, "mario", "")
	if results != nil {
		t.Error("expected nil when no platform specified")
	}
}

func TestSearchMyrient_UnknownPlatform(t *testing.T) {
	reg := testRegistry(t)
	results := SearchMyrient(context.Background(), reg, "mario", "atari2600")
	if results != nil {
		t.Error("expected nil for unknown platform")
	}
}

func TestSearchMyrient_EmptyQuery(t *testing.T) {
	reg := testRegistry(t)
	results := SearchMyrient(context.Background(), reg, "", "nes")
	if results != nil {
		t.Error("expected nil for empty query")
	}
}

func TestSearchMyrient_ParsesDirectoryListing(t *testing.T) {
	html := `<html><body>
<a href="../">../</a>
<a href="Super%20Mario%20Bros.%20%28USA%29.zip">Super Mario Bros. (USA).zip</a>
<a href="Zelda%20%28USA%29.zip">Zelda (USA).zip</a>
<a href="Mario%20%28Japan%29.zip">Mario (Japan).zip</a>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(html))
	}))
	defer srv.Close()

	// We need to temporarily override the myrientBase and cache.
	// Since we can't easily do that with the current architecture,
	// we test the href regex and filtering logic directly.
	ClearMyrientCache()

	// Test hrefRe parsing
	matches := hrefRe.FindAllStringSubmatch(html, -1)
	if len(matches) != 4 { // ../, plus 3 files
		t.Fatalf("expected 4 href matches, got %d", len(matches))
	}

	// Test non-English region filtering
	if !nonEnglishRegionRe.MatchString("Mario (Japan).zip") {
		t.Error("expected Japan to match nonEnglishRegionRe")
	}
	if nonEnglishRegionRe.MatchString("Mario (USA).zip") {
		t.Error("USA should not match nonEnglishRegionRe")
	}

	// Test English region detection
	if !englishRegionRe.MatchString("Game (USA).zip") {
		t.Error("expected USA to match englishRegionRe")
	}
	if !englishRegionRe.MatchString("Game (World).zip") {
		t.Error("expected World to match englishRegionRe")
	}
}

func TestCountOverlap(t *testing.T) {
	tests := []struct {
		name string
		a, b map[string]bool
		want int
	}{
		{
			name: "full overlap",
			a:    map[string]bool{"mario": true, "bros": true},
			b:    map[string]bool{"mario": true, "bros": true, "super": true},
			want: 2,
		},
		{
			name: "no overlap",
			a:    map[string]bool{"zelda": true},
			b:    map[string]bool{"mario": true},
			want: 0,
		},
		{
			name: "partial overlap",
			a:    map[string]bool{"super": true, "mario": true},
			b:    map[string]bool{"mario": true, "kart": true},
			want: 1,
		},
		{
			name: "empty sets",
			a:    map[string]bool{},
			b:    map[string]bool{},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countOverlap(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("countOverlap = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestClearMyrientCache(t *testing.T) {
	// Just ensure it doesn't panic
	ClearMyrientCache()
}
