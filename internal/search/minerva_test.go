package search

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gamarr/internal/sources"
)

const minervaBrowseHTML = `<div class="listing">
<div class="entry" data-name="chrono trigger (japan).zip">
<a href="/rom?id=2895660">Chrono Trigger (Japan).zip</a>
<span>2.95 MB</span>
<a href="javascript:void(0)" onclick="downloadMagnet('magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&amp;dn=Minerva_Myrient')"></a>
</div>
<div class="entry" data-name="chrono trigger (usa).zip">
<a href="/rom?id=2898398">Chrono Trigger (USA).zip</a>
<span>2.93 MB</span>
<a href="javascript:void(0)" onclick="downloadMagnet('magnet:?xt=urn:btih:C5C78AB0432DCC14FEC568BA5DAAD2D9CC98F4DC&amp;dn=Minerva_Myrient')"></a>
</div>
<div class="entry" data-name="chrono trigger (europe).zip">
<a href="/rom?id=2">Chrono Trigger (Europe).zip</a>
<span>2.90 MB</span>
</div>
<div class="entry" data-name="some folder">
<a href="/browse/./No-Intro/other/">other/</a>
</div>
</div>`

func minervaTestRegistry(t *testing.T, base string) *sources.Registry {
	t.Helper()
	reg := testRegistry(t)
	reg.Minerva.BaseURL = base
	reg.Minerva.PlatformPaths = map[string]string{
		"snes": "No-Intro/Nintendo - Super Nintendo Entertainment System/",
		"gba":  "No-Intro/Nintendo - Game Boy Advance/",
		"nes":  "No-Intro/Nintendo - Nintendo Entertainment System (Headered)/",
	}
	reg.Myrient.BaseURL = "https://myrient.example.test/files/"
	return reg
}

func TestMinervaPlatformSlugs(t *testing.T) {
	reg := testRegistry(t)
	slugs := MinervaPlatformSlugs(reg)
	if len(slugs) == 0 {
		t.Fatal("expected non-empty slug list")
	}
	found := make(map[string]bool)
	for _, s := range slugs {
		found[s] = true
	}
	for _, want := range []string{"nes", "snes", "n64", "psx", "gba", "wiiu", "psvita"} {
		if !found[want] {
			t.Errorf("expected slug %q in Minerva platforms", want)
		}
	}
}

func TestSearchMinerva_RequiresPlatform(t *testing.T) {
	reg := minervaTestRegistry(t, "http://127.0.0.1:1/")
	if results := SearchMinerva(reg, "mario", ""); results != nil {
		t.Error("expected nil when no platform specified")
	}
}

func TestSearchMinerva_EmptyQuery(t *testing.T) {
	reg := minervaTestRegistry(t, "http://127.0.0.1:1/")
	if results := SearchMinerva(reg, "", "snes"); results != nil {
		t.Error("empty query should yield nil")
	}
}

func TestSearchMinerva_UnknownPlatform(t *testing.T) {
	reg := minervaTestRegistry(t, "http://127.0.0.1:1/")
	if results := SearchMinerva(reg, "mario", "pc"); results != nil {
		t.Error("unmapped platform should yield nil")
	}
}

func TestSearchMinerva_EmptyRegistry(t *testing.T) {
	if results := SearchMinerva(nil, "mario", "snes"); results != nil {
		t.Error("nil registry should yield nil")
	}
	reg := &sources.Registry{}
	if results := SearchMinerva(reg, "mario", "snes"); results != nil {
		t.Error("empty base URL should yield nil")
	}
}

func TestParseMinervaListing(t *testing.T) {
	hits := parseMinervaListing(minervaBrowseHTML, "No-Intro/Nintendo - Super Nintendo Entertainment System/")
	if len(hits) != 3 {
		t.Fatalf("got %d hits, want 3 ROM entries (folder skipped)", len(hits))
	}
	if hits[1].ID != 2898398 || hits[1].Title != "Chrono Trigger (USA).zip" {
		t.Errorf("usa hit = %+v", hits[1])
	}
	if hits[1].SizeHuman != "2.93 MB" || hits[1].Size < 2_000_000 {
		t.Errorf("usa size = %q %d", hits[1].SizeHuman, hits[1].Size)
	}
	if !strings.Contains(hits[1].Magnet, "c5c78ab0432dcc14fec568ba5daad2d9cc98f4dc") &&
		!strings.Contains(strings.ToLower(hits[1].Magnet), "c5c78ab0432dcc14fec568ba5daad2d9cc98f4dc") {
		t.Errorf("magnet = %q", hits[1].Magnet)
	}
	if hits[1].Path != "No-Intro/Nintendo - Super Nintendo Entertainment System/Chrono Trigger (USA).zip" {
		t.Errorf("path = %q", hits[1].Path)
	}
}

func TestSearchMinerva_ParsesResults(t *testing.T) {
	ClearMinervaCache()
	t.Cleanup(func() {
		ClearMinervaCache()
		RecordSearchSuccess("minerva")
	})
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if !strings.Contains(r.URL.Path, "/browse/./No-Intro/") {
			t.Errorf("path = %q, want a browse listing", r.URL.Path)
		}
		if !strings.Contains(r.URL.Path, "Super%20Nintendo") && !strings.Contains(r.URL.Path, "Super Nintendo") {
			t.Errorf("path missing SNES folder: %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(minervaBrowseHTML))
	}))
	t.Cleanup(srv.Close)

	reg := minervaTestRegistry(t, srv.URL+"/")
	results := SearchMinerva(reg, "Chrono Trigger", "snes")
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (Japan dump dropped)", len(results))
	}
	r := results[0]
	if r.Indexer != "Minerva" || r.SourceType != "torrent" {
		t.Errorf("indexer/type = %q %q", r.Indexer, r.SourceType)
	}
	if r.Title != "Chrono Trigger (USA).zip" {
		t.Errorf("title = %q", r.Title)
	}
	if r.PlatformSlug != "snes" || r.Platform != "SNES" {
		t.Errorf("platform = %q %q", r.Platform, r.PlatformSlug)
	}
	if r.InfoHash != "c5c78ab0432dcc14fec568ba5daad2d9cc98f4dc" {
		t.Errorf("infohash = %q", r.InfoHash)
	}
	if r.GUID != srv.URL+"/rom?id=2898398" {
		t.Errorf("guid = %q", r.GUID)
	}
	if r.DownloadURL != "" {
		t.Errorf("download URL = %q, want empty (magnet is the download)", r.DownloadURL)
	}
	if r.MagnetURL == "" || r.InfoHash == "" {
		t.Errorf("magnet/hash missing: %q %q", r.MagnetURL, r.InfoHash)
	}
	if results[1].Title != "Chrono Trigger (Europe).zip" {
		t.Errorf("second title = %q", results[1].Title)
	}

	_ = SearchMinerva(reg, "Chrono Trigger", "snes")
	if hits != 1 {
		t.Errorf("listing fetched %d times, want 1 (second search uses cache)", hits)
	}
}

func TestSearchMinerva_HTTPError(t *testing.T) {
	ClearMinervaCache()
	t.Cleanup(func() {
		ClearMinervaCache()
		RecordSearchSuccess("minerva")
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	t.Cleanup(srv.Close)
	reg := minervaTestRegistry(t, srv.URL+"/")
	if results := SearchMinerva(reg, "mario", "snes"); len(results) != 0 {
		t.Errorf("HTTP 500 should yield no results, got %d", len(results))
	}
}

func TestMinervaFileURL(t *testing.T) {
	got := minervaFileURL("https://myrient.example.test/files/", "./No-Intro/Nintendo - SNES/Foo (USA).zip")
	want := "https://myrient.example.test/files/No-Intro/Nintendo%20-%20SNES/Foo%20%28USA%29.zip"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if minervaFileURL("", "x.zip") != "" || minervaFileURL("http://x/", "") != "" {
		t.Error("empty inputs should yield empty URL")
	}
}

func TestMinervaListingURL(t *testing.T) {
	got := minervaListingURL("https://minerva-archive.org/", "No-Intro/Nintendo - Super Nintendo Entertainment System/")
	want := "https://minerva-archive.org/browse/./No-Intro/Nintendo%20-%20Super%20Nintendo%20Entertainment%20System/"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMinervaInfoHashAndTitle(t *testing.T) {
	if got := minervaInfoHash("magnet:?xt=urn:btih:AbCdEf0123456789AbCdEf0123456789AbCdEf01&dn=x"); got != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Errorf("hash = %q", got)
	}
	if minervaInfoHash("not-a-magnet") != "" {
		t.Error("non-magnet should be empty")
	}
	if minervaTitle("./a/b/Game.zip") != "Game.zip" {
		t.Errorf("title = %q", minervaTitle("./a/b/Game.zip"))
	}
	if minervaTitle("./") != "" {
		t.Error("bare ./ should be empty")
	}
}

func TestSearchMinerva_CapsAt20(t *testing.T) {
	ClearMinervaCache()
	t.Cleanup(func() {
		ClearMinervaCache()
		RecordSearchSuccess("minerva")
	})
	html := ""
	for i := 1; i <= 30; i++ {
		html += `<div class="entry" data-name="game (usa).zip"><a href="/rom?id=` + itoaForTest(i) + `">Game (USA).zip</a><span>1 MB</span></div>`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(html))
	}))
	t.Cleanup(srv.Close)
	reg := minervaTestRegistry(t, srv.URL+"/")
	results := SearchMinerva(reg, "game", "snes")
	if len(results) != 20 {
		t.Errorf("got %d results, want 20", len(results))
	}
}

func itoaForTest(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func TestParseMinervaSize(t *testing.T) {
	if got := parseMinervaSize("2.93 MB"); got < 3_000_000 || got > 3_200_000 {
		t.Errorf("2.93 MB = %d", got)
	}
	if parseMinervaSize("?") != 0 {
		t.Error("unknown size should be 0")
	}
}

func TestMinervaGUID(t *testing.T) {
	if got := minervaGUID("https://minerva-archive.org/", 42); got != "https://minerva-archive.org/rom?id=42" {
		t.Errorf("guid = %q", got)
	}
	if minervaGUID("https://x/", 0) != "" {
		t.Error("id 0 should be empty")
	}
}

func TestClearMinervaCache(t *testing.T) {
	ClearMinervaCache()
}
