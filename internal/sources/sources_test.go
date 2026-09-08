package sources

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDefault_EmbeddedRegistryIsComplete(t *testing.T) {
	r, err := Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}
	checks := map[string]bool{
		"Myrient.BaseURL":       r.Myrient.BaseURL == "",
		"Myrient.PlatformPaths": len(r.Myrient.PlatformPaths) == 0,
		"Vimm.BaseURL":          r.Vimm.BaseURL == "",
		"Vimm.PlatformSystems":  len(r.Vimm.PlatformSystems) == 0,
	}
	for field, empty := range checks {
		if empty {
			t.Errorf("embedded registry: %s is empty", field)
		}
	}
	// Spot-check a few platform mappings are preserved.
	if r.Myrient.PlatformPaths["gba"] != "No-Intro/Nintendo - Game Boy Advance/" {
		t.Errorf("Myrient.PlatformPaths[gba] missing or wrong: %q", r.Myrient.PlatformPaths["gba"])
	}
	if r.Vimm.PlatformSystems["psx"] != "PS1" {
		t.Errorf("Vimm.PlatformSystems[psx] missing or wrong: %q", r.Vimm.PlatformSystems["psx"])
	}
}

func TestDefault_MinervaDisabledAndConfigured(t *testing.T) {
	r, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if r.Minerva.Enabled {
		t.Fatal("Minerva must be disabled by default")
	}
	if r.Minerva.BaseURL != "https://minerva-archive.org/" {
		t.Fatalf("BaseURL=%q", r.Minerva.BaseURL)
	}
	if r.Minerva.AssetsURL != "https://minerva-archive.org/assets/" {
		t.Fatalf("AssetsURL=%q", r.Minerva.AssetsURL)
	}
	if r.Minerva.SyncIntervalHours != 24 {
		t.Fatalf("SyncIntervalHours=%d", r.Minerva.SyncIntervalHours)
	}
	if r.Minerva.PlatformPaths["nds"] != "No-Intro/Nintendo - Nintendo DS (Decrypted)/" {
		t.Fatalf("nds path=%q", r.Minerva.PlatformPaths["nds"])
	}
}

func TestLoad(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":3,"myrient":{"base_url":"https://url-example.test/","platform_paths":{"foo":"bar/"}},"vimm":{"base_url":"https://url-vimm.test/","platform_systems":{"foo":"FOO"}}}`))
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()

	dir := t.TempDir()
	goodFile := filepath.Join(dir, "good.json")
	_ = os.WriteFile(goodFile, []byte(`{"version":2,"myrient":{"base_url":"https://file-example.test/","platform_paths":{"abc":"def/"}},"vimm":{"base_url":"https://file-vimm.test/","platform_systems":{"abc":"ABC"}}}`), 0o644)
	brokenFile := filepath.Join(dir, "broken.json")
	_ = os.WriteFile(brokenFile, []byte(`{not json`), 0o644)

	cases := []struct {
		name        string
		path, url   string
		wantMyrient string // "" => embedded fallback expected
		wantVersion int    // 0 => don't care
	}{
		{"empty -> embedded", "", "", "", 0},
		{"file overrides", goodFile, "", "https://file-example.test/", 2},
		{"url overrides", "", good.URL, "https://url-example.test/", 3},
		{"path beats url", goodFile, good.URL, "https://file-example.test/", 2},
		{"bad url falls back to embedded", "", bad.URL, "", 0},
		{"bad file falls back to embedded", brokenFile, "", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Load(tc.path, tc.url)
			if r == nil {
				t.Fatal("Load returned nil")
			}
			if tc.wantMyrient == "" {
				// Embedded fallback expected
				if r.Myrient.BaseURL == "" {
					t.Errorf("expected embedded Myrient.BaseURL, got empty")
				}
				return
			}
			if r.Myrient.BaseURL != tc.wantMyrient {
				t.Errorf("Myrient.BaseURL = %q, want %q", r.Myrient.BaseURL, tc.wantMyrient)
			}
			if tc.wantVersion > 0 && r.Version != tc.wantVersion {
				t.Errorf("Version = %d, want %d", r.Version, tc.wantVersion)
			}
		})
	}
}

func TestApplyEnvOverrides(t *testing.T) {
	cases := []struct {
		name                         string
		envs                         map[string]string
		wantMyrient                  string // "" => embedded default
		wantVimm                     string
		wantMinervaEnabled           bool
		wantMinervaBaseURL           string
		wantMinervaAssetsURL         string
		wantMinervaSyncIntervalHours int
		initialMinervaEnabled        bool
	}{
		{"unset leaves values", nil, "", "", false, "", "", 0, false},
		{"MYRIENT_URL overrides", map[string]string{"MYRIENT_URL": "https://my-override.test/"}, "https://my-override.test/", "", false, "", "", 0, false},
		{"VIMM_URL overrides", map[string]string{"VIMM_URL": "https://vimm-override.test/"}, "", "https://vimm-override.test/", false, "", "", 0, false},
		{"both overridden", map[string]string{"MYRIENT_URL": "https://m.test/", "VIMM_URL": "https://v.test/"}, "https://m.test/", "https://v.test/", false, "", "", 0, false},
		{"Minerva values overridden", map[string]string{
			"MINERVA_ENABLED": "yes", "MINERVA_URL": "https://minerva-override.test/", "MINERVA_ASSETS_URL": "https://minerva-override.test/assets/", "MINERVA_SYNC_INTERVAL_HOURS": "6",
		}, "", "", true, "https://minerva-override.test/", "https://minerva-override.test/assets/", 6, false},
		{"MINERVA_ENABLED no disables source", map[string]string{"MINERVA_ENABLED": "no"}, "", "", false, "", "", 0, true},
		{"invalid Minerva booleans and intervals leave defaults", map[string]string{"MINERVA_ENABLED": "invalid", "MINERVA_SYNC_INTERVAL_HOURS": "0"}, "", "", true, "", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := Default()
			r.Minerva.Enabled = tc.initialMinervaEnabled
			origM, origV := r.Myrient.BaseURL, r.Vimm.BaseURL
			origMinervaBaseURL, origMinervaAssetsURL := r.Minerva.BaseURL, r.Minerva.AssetsURL
			origMinervaSyncIntervalHours := r.Minerva.SyncIntervalHours
			r.ApplyEnvOverrides(func(k string) string { return tc.envs[k] })
			wantM, wantV := tc.wantMyrient, tc.wantVimm
			if wantM == "" {
				wantM = origM
			}
			if wantV == "" {
				wantV = origV
			}
			if r.Myrient.BaseURL != wantM {
				t.Errorf("Myrient.BaseURL = %q, want %q", r.Myrient.BaseURL, wantM)
			}
			if r.Vimm.BaseURL != wantV {
				t.Errorf("Vimm.BaseURL = %q, want %q", r.Vimm.BaseURL, wantV)
			}
			wantMinervaBaseURL, wantMinervaAssetsURL := tc.wantMinervaBaseURL, tc.wantMinervaAssetsURL
			wantMinervaSyncIntervalHours := tc.wantMinervaSyncIntervalHours
			if wantMinervaBaseURL == "" {
				wantMinervaBaseURL = origMinervaBaseURL
			}
			if wantMinervaAssetsURL == "" {
				wantMinervaAssetsURL = origMinervaAssetsURL
			}
			if wantMinervaSyncIntervalHours == 0 {
				wantMinervaSyncIntervalHours = origMinervaSyncIntervalHours
			}
			if r.Minerva.Enabled != tc.wantMinervaEnabled {
				t.Errorf("Minerva.Enabled = %v, want %v", r.Minerva.Enabled, tc.wantMinervaEnabled)
			}
			if r.Minerva.BaseURL != wantMinervaBaseURL {
				t.Errorf("Minerva.BaseURL = %q, want %q", r.Minerva.BaseURL, wantMinervaBaseURL)
			}
			if r.Minerva.AssetsURL != wantMinervaAssetsURL {
				t.Errorf("Minerva.AssetsURL = %q, want %q", r.Minerva.AssetsURL, wantMinervaAssetsURL)
			}
			if r.Minerva.SyncIntervalHours != wantMinervaSyncIntervalHours {
				t.Errorf("Minerva.SyncIntervalHours = %d, want %d", r.Minerva.SyncIntervalHours, wantMinervaSyncIntervalHours)
			}
		})
	}
}
