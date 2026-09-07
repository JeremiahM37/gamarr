package search

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gamarr/internal/sources"
)

// vimmListHTML is a stripped-down Vimm search-results table: hidden
// rating-sort sentinels, the same game listed per region, and a duplicate
// vault ID that the old regex counted twice.
const vimmListHTML = `<table class="hovertable">
<tr><td style="width:auto"><a href="/vault/999999" style="display:  none">9</a><a href= "/vault/43356">Super Metroid</a></td><td><img src="/images/flags/europe.png" class="flag" title="Europe"></td><td>1.0</td></tr>
<tr><td style="width:auto"><a href="/vault/999999" style="display:  none">9</a><a href= "/vault/1654" onmouseover="buildTooltip(this, 1654, 256, 222)">Super Metroid</a></td><td><img src="/images/flags/usa.png" class="flag" title="USA"><img src="/images/flags/japan.png" class="flag" title="Japan"></td><td>1.0</td></tr>
<tr><td style="width:auto"><a href="/vault/999999" style="display:  none">9</a><a href= "/vault/43356">Super Metroid</a></td><td><img src="/images/flags/europe.png" class="flag" title="Europe"></td><td>1.0</td></tr>
</table>`

const vimmCrossSystemHTML = `<table class="hovertable">
<tr><td style="width:80px; text-align:center">SNES</td><td><a href="/vault/999999" style="display:  none">9</a><a href= "/vault/1654">Super Metroid</a></td><td><img src="/images/flags/usa.png" class="flag" title="USA"></td></tr>
<tr><td style="width:80px; text-align:center">WiiWare</td><td><a href="/vault/999999" style="display:  none">9</a><a href= "/vault/37486">Super Metroid (SNES)</a></td><td><img src="/images/flags/europe.png" class="flag" title="Europe"></td></tr>
</table>`

func vimmTestServer(t *testing.T, html string) (*httptest.Server, *sources.Registry) {
	t.Helper()
	t.Cleanup(func() { RecordSearchSuccess("vimm") })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(html))
	}))
	t.Cleanup(srv.Close)
	reg := testRegistry(t)
	reg.Vimm.BaseURL = srv.URL + "/"
	return srv, reg
}

func TestVimmPlatformSlugs(t *testing.T) {
	reg := testRegistry(t)
	slugs := VimmPlatformSlugs(reg)
	if len(slugs) == 0 {
		t.Fatal("expected non-empty slug list")
	}
	found := make(map[string]bool)
	for _, s := range slugs {
		found[s] = true
	}
	for _, want := range []string{"nes", "snes", "n64", "psx", "ps2"} {
		if !found[want] {
			t.Errorf("expected slug %q in Vimm platforms", want)
		}
	}
}

func TestVimmSystemMap(t *testing.T) {
	reg := testRegistry(t)
	if reg.Vimm.PlatformSystems["nes"] != "NES" {
		t.Error("nes should map to NES")
	}
	if reg.Vimm.PlatformSystems["psx"] != "PS1" {
		t.Error("psx should map to PS1")
	}
	if reg.Vimm.PlatformSystems["ngc"] != "GameCube" {
		t.Error("ngc should map to GameCube")
	}
	// Aliases live in their own table; putting them in PlatformSystems would
	// make the system->slug inversion ambiguous.
	if reg.Vimm.PlatformAliases["gamecube"] != "ngc" {
		t.Error("gamecube alias should resolve to ngc")
	}
	if reg.Vimm.PlatformAliases["dreamcast"] != "dc" {
		t.Error("dreamcast alias should resolve to dc")
	}
	if _, dup := reg.Vimm.PlatformSystems["gamecube"]; dup {
		t.Error("alias slug must not also be a PlatformSystems key")
	}
}

func TestSearchVimm_ParsesResults(t *testing.T) {
	html := `<html><body>
<a href="/vault/12345">Super Mario World</a>
<a href="/vault/67890">Zelda: A Link to the Past</a>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("system") != "SNES" {
			t.Errorf("expected system=SNES, got %q", r.URL.Query().Get("system"))
		}
		_, _ = w.Write([]byte(html))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { RecordSearchSuccess("vimm") })

	reg := testRegistry(t)
	reg.Vimm.BaseURL = srv.URL + "/"
	results := SearchVimm(reg, "mario", "snes")
	if len(results) != 2 {
		t.Fatalf("expected 2 vimm matches, got %d", len(results))
	}
	if results[0].VimmID != "12345" {
		t.Errorf("first game ID = %q, want '12345'", results[0].VimmID)
	}
	if !strings.Contains(results[0].Title, "Super Mario World") {
		t.Errorf("first game name = %q", results[0].Title)
	}
	if results[1].VimmID != "67890" {
		t.Errorf("second game ID = %q, want '67890'", results[1].VimmID)
	}
}

func TestSearchVimm_SkipsRatingSentinelsAndDuplicateIDs(t *testing.T) {
	_, reg := vimmTestServer(t, vimmListHTML)
	results := SearchVimm(reg, "metroid", "snes")

	ids := make([]string, 0, len(results))
	for _, r := range results {
		ids = append(ids, r.VimmID)
		if r.VimmID == "999999" || r.Title == "9" || strings.HasPrefix(r.Title, "9 (") {
			t.Errorf("rating-sort sentinel leaked into results: id=%q title=%q", r.VimmID, r.Title)
		}
	}
	if len(results) != 2 {
		t.Fatalf("got %d results (ids %v), want 2 unique games", len(results), ids)
	}
	if results[0].VimmID != "43356" || results[1].VimmID != "1654" {
		t.Errorf("ids = %v, want [43356 1654]", ids)
	}
}

func TestSearchVimm_IncludesRegionInTitle(t *testing.T) {
	_, reg := vimmTestServer(t, vimmListHTML)
	results := SearchVimm(reg, "metroid", "snes")
	if len(results) < 2 {
		t.Fatalf("got %d results, want at least 2", len(results))
	}
	if !strings.Contains(results[0].Title, "Europe") {
		t.Errorf("Europe dump title = %q, want region in title", results[0].Title)
	}
	if !strings.Contains(results[1].Title, "USA") || !strings.Contains(results[1].Title, "Japan") {
		t.Errorf("USA/Japan dump title = %q, want both regions", results[1].Title)
	}
}

func TestSearchVimm_KeepsNumericTitles(t *testing.T) {
	_, reg := vimmTestServer(t, `<a href="/vault/999999" style="display: none">9</a><a href="/vault/111">1942</a>`)
	results := SearchVimm(reg, "1942", "nes")
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (1942 kept, sentinel dropped)", len(results))
	}
	if results[0].VimmID != "111" || !strings.Contains(results[0].Title, "1942") {
		t.Errorf("result = id %q title %q, want vault/111 1942", results[0].VimmID, results[0].Title)
	}
}

func TestSearchVimm_SystemColumnBeatsTitleSuffix(t *testing.T) {
	_, reg := vimmTestServer(t, vimmCrossSystemHTML)
	results := SearchVimm(reg, "metroid", "")
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	byID := map[string]string{}
	for _, r := range results {
		byID[r.VimmID] = r.Platform
	}
	if byID["1654"] != "SNES" {
		t.Errorf("vault/1654 platform = %q, want SNES from system column", byID["1654"])
	}
	if byID["37486"] != "WiiWare" {
		t.Errorf("vault/37486 platform = %q, want WiiWare (not SNES from title suffix)", byID["37486"])
	}
}

func TestVimmGameRe(t *testing.T) {
	tests := []struct {
		name    string
		html    string
		wantLen int
	}{
		{
			name:    "standard link",
			html:    `<a href="/vault/123">Game Name</a>`,
			wantLen: 1,
		},
		{
			name:    "link with extra attributes",
			html:    `<a href= "/vault/456" class="game">Other Game</a>`,
			wantLen: 1,
		},
		{
			name:    "non-vault link",
			html:    `<a href="/other/123">Not a game</a>`,
			wantLen: 0,
		},
		{
			name:    "empty body",
			html:    `<html></html>`,
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := vimmGameRe.FindAllStringSubmatch(tt.html, -1)
			if len(matches) != tt.wantLen {
				t.Errorf("got %d matches, want %d", len(matches), tt.wantLen)
			}
		})
	}
}

func TestSearchVimm_HTTPError(t *testing.T) {
	t.Cleanup(func() { RecordSearchSuccess("vimm") })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	t.Cleanup(srv.Close)
	reg := testRegistry(t)
	reg.Vimm.BaseURL = srv.URL + "/"
	if results := SearchVimm(reg, "mario", "snes"); len(results) != 0 {
		t.Errorf("HTTP 500 should yield no results, got %d", len(results))
	}
}

func TestSearchVimm_HTTP404IsEmptyNotFailure(t *testing.T) {
	// Vimm uses 404 for "no matching titles". That must not open the circuit.
	t.Cleanup(func() { RecordSearchSuccess("vimm") })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	reg := testRegistry(t)
	reg.Vimm.BaseURL = srv.URL + "/"
	for i := 0; i < 5; i++ {
		if results := SearchVimm(reg, "zzzz-no-such-title", "psp"); len(results) != 0 {
			t.Fatalf("call %d: HTTP 404 should yield no results, got %d", i, len(results))
		}
	}
	if IsCircuitOpen("vimm") {
		t.Fatal("HTTP 404 empty results must not open the vimm circuit")
	}
}

func TestParseRetryAfter(t *testing.T) {
	if d := ParseRetryAfter("45", time.Minute); d != 45*time.Second {
		t.Errorf("seconds: got %v", d)
	}
	if d := ParseRetryAfter("", 30*time.Second); d != 30*time.Second {
		t.Errorf("empty: got %v", d)
	}
	if d := ParseRetryAfter("not-a-date", 12*time.Second); d != 12*time.Second {
		t.Errorf("bad: got %v", d)
	}
}

func TestSearchVimm_HTTP429RespectsRetryAfter(t *testing.T) {
	t.Cleanup(func() { ResetCircuit("vimm") })
	resetHealthStore()
	// Avoid sleeping in the gate during this test.
	old := vimmMinInterval
	vimmMinInterval = 0
	t.Cleanup(func() { vimmMinInterval = old })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	reg := testRegistry(t)
	reg.Vimm.BaseURL = srv.URL + "/"

	if results := SearchVimm(reg, "mario", "nes"); len(results) != 0 {
		t.Fatalf("429 should yield no results, got %d", len(results))
	}
	if !IsCircuitOpen("vimm") {
		t.Fatal("429 should open the vimm circuit immediately")
	}
	h := GetSourceHealth("vimm")
	if h.LastErrorKind != "rate_limit" {
		t.Errorf("LastErrorKind=%q", h.LastErrorKind)
	}
	if h.CircuitRetryInSec < 2 || h.CircuitRetryInSec > 3 {
		t.Errorf("CircuitRetryInSec=%d, want ~3", h.CircuitRetryInSec)
	}
}

func TestWaitVimmRateLimit_SpacesRequests(t *testing.T) {
	old := vimmMinInterval
	vimmMinInterval = 40 * time.Millisecond
	vimmLastReq = time.Time{}
	t.Cleanup(func() {
		vimmMinInterval = old
		vimmLastReq = time.Time{}
	})

	start := time.Now()
	WaitVimmRateLimit()
	WaitVimmRateLimit()
	elapsed := time.Since(start)
	if elapsed < 40*time.Millisecond {
		t.Fatalf("expected >=40ms between gated calls, got %v", elapsed)
	}
}

func TestVimmPlatformSystemsIsInjective(t *testing.T) {
	// The search path inverts this map to label results. Two slugs sharing one
	// system make that inversion depend on Go's randomised map iteration.
	reg := testRegistry(t)
	seen := map[string]string{}
	for slug, sys := range reg.Vimm.PlatformSystems {
		if prev, dup := seen[strings.ToLower(sys)]; dup {
			t.Errorf("system %q is claimed by both %q and %q; an alias belongs in PlatformAliases", sys, prev, slug)
		}
		seen[strings.ToLower(sys)] = slug
	}
}

func TestSearchVimm_AliasSlugIsCanonicalisedAndDeterministic(t *testing.T) {
	// A GameCube hit must always come back as the canonical "ngc", whether the
	// caller asked with "ngc", the "gamecube" alias, or no filter at all.
	// Before aliases were split out this returned "ngc" or "gamecube" at random
	// per call, which split the search-merge de-dup key and lost the ngc size
	// band in scoring.
	const html = `<table><tr><td>GameCube</td><td><a href="/vault/12345" >Super Mario Sunshine</a></td></tr></table>`
	var gotSystem string
	t.Cleanup(func() { RecordSearchSuccess("vimm") })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSystem = r.URL.Query().Get("system")
		_, _ = w.Write([]byte(html))
	}))
	t.Cleanup(srv.Close)
	reg := testRegistry(t)
	reg.Vimm.BaseURL = srv.URL + "/"

	for _, filter := range []string{"ngc", "gamecube", ""} {
		for i := 0; i < 50; i++ {
			results := SearchVimm(reg, "mario", filter)
			if len(results) != 1 {
				t.Fatalf("filter %q call %d: got %d results, want 1", filter, i, len(results))
			}
			if results[0].PlatformSlug != "ngc" {
				t.Fatalf("filter %q call %d: PlatformSlug=%q, want ngc", filter, i, results[0].PlatformSlug)
			}
		}
		if filter != "" && gotSystem != "GameCube" {
			t.Errorf("filter %q sent ?system=%q, want GameCube", filter, gotSystem)
		}
	}
}
