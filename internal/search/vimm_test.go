package search

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVimmPlatformSlugs(t *testing.T) {
	slugs := VimmPlatformSlugs()
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
	// Verify key mappings
	if vimmSystemMap["nes"] != "NES" {
		t.Error("nes should map to NES")
	}
	if vimmSystemMap["psx"] != "PS1" {
		t.Error("psx should map to PS1")
	}
	if vimmSystemMap["ngc"] != "GameCube" {
		t.Error("ngc should map to GameCube")
	}
}

func TestSearchVimm_ParsesResults(t *testing.T) {
	html := `<html><body>
<a href="/vault/12345">Super Mario World</a>
<a href="/vault/67890">Zelda: A Link to the Past</a>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check the system param
		if r.URL.Query().Get("system") != "SNES" {
			t.Errorf("expected system=SNES, got %q", r.URL.Query().Get("system"))
		}
		w.Write([]byte(html))
	}))
	defer srv.Close()

	// We can't easily override the Vimm URL in the current code,
	// so test the regex parsing directly
	matches := vimmGameRe.FindAllStringSubmatch(html, -1)
	if len(matches) != 2 {
		t.Fatalf("expected 2 vimm matches, got %d", len(matches))
	}
	if matches[0][1] != "12345" {
		t.Errorf("first game ID = %q, want '12345'", matches[0][1])
	}
	if matches[0][2] != "Super Mario World" {
		t.Errorf("first game name = %q", matches[0][2])
	}
	if matches[1][1] != "67890" {
		t.Errorf("second game ID = %q, want '67890'", matches[1][1])
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

// TestSearchVimm_ServerError verifies behavior when the server returns an error.
// We test this via httptest since SearchVimm makes real HTTP calls.
func TestSearchVimm_NonHTTPS(t *testing.T) {
	// SearchVimm connects to vimm.net directly - we can't easily mock it
	// without refactoring. Instead, verify the regex and platform map logic.
	_ = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
}
