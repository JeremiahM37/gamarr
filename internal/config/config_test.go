package config

import (
	"os"
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	// Clear relevant env vars
	for _, k := range []string{"PROWLARR_URL", "PROWLARR_API_KEY", "QB_URL", "QB_USER", "QB_PASS",
		"QB_CONTAINER_NAME", "GAMARR_PORT", "MAX_RETRIES", "METRICS_ENABLED", "PROWLARR_GAME_INDEXERS",
		"AI_MONITOR_ENABLED", "EXTRACT_ARCHIVES", "SABNZBD_URL", "SABNZBD_API_KEY",
		"NZBGET_URL", "NZBGET_USER", "NZBGET_PASS", "NZBGET_CATEGORY", "FLARESOLVERR_URL",
		"FLARESOLVERR_MAX_TIMEOUT", "FLARESOLVERR_TABS_TILL_VERIFY"} {
		os.Unsetenv(k)
	}

	cfg := Load()

	if cfg.ProwlarrURL != "http://prowlarr:9696" {
		t.Errorf("ProwlarrURL=%q", cfg.ProwlarrURL)
	}
	if cfg.QBUser != "admin" {
		t.Errorf("QBUser=%q", cfg.QBUser)
	}
	if cfg.QBContainerName != "qbittorrent" {
		t.Errorf("QBContainerName=%q, want %q", cfg.QBContainerName, "qbittorrent")
	}
	if cfg.Port != 5001 {
		t.Errorf("Port=%d, want 5001", cfg.Port)
	}
	if cfg.MaxRetries != 2 {
		t.Errorf("MaxRetries=%d, want 2", cfg.MaxRetries)
	}
	if !cfg.MetricsEnabled {
		t.Error("MetricsEnabled should default to true")
	}
	if cfg.AIMonitorEnabled {
		t.Error("AIMonitorEnabled should default to false")
	}
	if cfg.ExtractArchives {
		t.Error("ExtractArchives should default to false")
	}
	if cfg.FlareSolverrURL != "" || cfg.FlareSolverrMaxTimeout != 55_000 || cfg.FlareSolverrTabsTillVerify != 74 {
		t.Errorf("FlareSolverr defaults = (%q, %d, %d), want disabled, 55000, 74", cfg.FlareSolverrURL, cfg.FlareSolverrMaxTimeout, cfg.FlareSolverrTabsTillVerify)
	}

	// No default indexer IDs: Prowlarr numbers indexers per install, so any
	// hardcoded list points at a different set of trackers on every one.
	// Empty means the indexers get discovered from Prowlarr's capabilities.
	if len(cfg.ProwlarrGameIndexers) != 0 {
		t.Errorf("ProwlarrGameIndexers=%v, want empty so discovery runs", cfg.ProwlarrGameIndexers)
	}
}

func TestLoad_FromEnv(t *testing.T) {
	os.Setenv("GAMARR_PORT", "8080")
	os.Setenv("PROWLARR_API_KEY", "testkey123")
	os.Setenv("QB_USER", "jam")
	os.Setenv("QB_PASS", "secret")
	os.Setenv("QB_CONTAINER_NAME", "qbit-custom")
	os.Setenv("PROWLARR_GAME_INDEXERS", "1,2,3")
	os.Setenv("AI_MONITOR_ENABLED", "true")
	os.Setenv("EXTRACT_ARCHIVES", "1")
	os.Setenv("NZBGET_URL", "http://nzbget:6789")
	os.Setenv("NZBGET_USER", "gamarr")
	os.Setenv("NZBGET_PASS", "secret")
	os.Setenv("NZBGET_CATEGORY", "roms")
	os.Setenv("FLARESOLVERR_URL", "http://flaresolverr:8191")
	os.Setenv("FLARESOLVERR_MAX_TIMEOUT", "90000")
	os.Setenv("FLARESOLVERR_TABS_TILL_VERIFY", "41")
	defer func() {
		os.Unsetenv("GAMARR_PORT")
		os.Unsetenv("PROWLARR_API_KEY")
		os.Unsetenv("QB_USER")
		os.Unsetenv("QB_PASS")
		os.Unsetenv("QB_CONTAINER_NAME")
		os.Unsetenv("PROWLARR_GAME_INDEXERS")
		os.Unsetenv("AI_MONITOR_ENABLED")
		os.Unsetenv("EXTRACT_ARCHIVES")
		os.Unsetenv("NZBGET_URL")
		os.Unsetenv("NZBGET_USER")
		os.Unsetenv("NZBGET_PASS")
		os.Unsetenv("NZBGET_CATEGORY")
		os.Unsetenv("FLARESOLVERR_URL")
		os.Unsetenv("FLARESOLVERR_MAX_TIMEOUT")
		os.Unsetenv("FLARESOLVERR_TABS_TILL_VERIFY")
	}()

	cfg := Load()

	if cfg.Port != 8080 {
		t.Errorf("Port=%d, want 8080", cfg.Port)
	}
	if cfg.ProwlarrAPIKey != "testkey123" {
		t.Errorf("ProwlarrAPIKey=%q", cfg.ProwlarrAPIKey)
	}
	if cfg.QBUser != "jam" {
		t.Errorf("QBUser=%q", cfg.QBUser)
	}
	if cfg.QBPass != "secret" {
		t.Errorf("QBPass=%q", cfg.QBPass)
	}
	if cfg.QBContainerName != "qbit-custom" {
		t.Errorf("QBContainerName=%q, want %q", cfg.QBContainerName, "qbit-custom")
	}
	if len(cfg.ProwlarrGameIndexers) != 3 || cfg.ProwlarrGameIndexers[0] != 1 {
		t.Errorf("ProwlarrGameIndexers=%v", cfg.ProwlarrGameIndexers)
	}
	if !cfg.AIMonitorEnabled {
		t.Error("AIMonitorEnabled should be true from env")
	}
	if !cfg.ExtractArchives {
		t.Error("ExtractArchives should be true from env '1'")
	}
	if cfg.NZBGetURL != "http://nzbget:6789" || cfg.NZBGetUser != "gamarr" || cfg.NZBGetPass != "secret" || cfg.NZBGetCategory != "roms" {
		t.Errorf("unexpected NZBGet config: url=%q user=%q pass=%q category=%q", cfg.NZBGetURL, cfg.NZBGetUser, cfg.NZBGetPass, cfg.NZBGetCategory)
	}
	if cfg.FlareSolverrURL != "http://flaresolverr:8191" || cfg.FlareSolverrMaxTimeout != 90_000 || cfg.FlareSolverrTabsTillVerify != 41 {
		t.Errorf("unexpected FlareSolverr config: url=%q timeout=%d tabs=%d", cfg.FlareSolverrURL, cfg.FlareSolverrMaxTimeout, cfg.FlareSolverrTabsTillVerify)
	}
}

func TestLoad_ExplicitEmptyQBURLDisablesQBittorrent(t *testing.T) {
	t.Setenv("QB_URL", "")
	cfg := Load()
	if cfg.QBURL != "" {
		t.Fatalf("QBURL=%q, want empty", cfg.QBURL)
	}
	if cfg.HasQBittorrent() {
		t.Fatal("qBittorrent should be disabled by an explicitly empty QB_URL")
	}
}

func TestLoad_InvalidFlareSolverrTimeoutUsesDefault(t *testing.T) {
	for _, value := range []string{"19999", "600001", "not-a-number"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("FLARESOLVERR_MAX_TIMEOUT", value)
			if got := Load().FlareSolverrMaxTimeout; got != 55_000 {
				t.Errorf("timeout = %d, want 55000", got)
			}
		})
	}
}

func TestLoad_InvalidFlareSolverrTabsUsesDefault(t *testing.T) {
	for _, value := range []string{"-1", "0", "1001", "not-a-number"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("FLARESOLVERR_TABS_TILL_VERIFY", value)
			if got := Load().FlareSolverrTabsTillVerify; got != 74 {
				t.Errorf("tabs till verify = %d, want 74", got)
			}
		})
	}
}

func TestHasProwlarr(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		key    string
		expect bool
	}{
		{"both set", "http://prowlarr:9696", "key", true},
		{"no key", "http://prowlarr:9696", "", false},
		{"no url", "", "key", false},
		{"both empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{ProwlarrURL: tt.url, ProwlarrAPIKey: tt.key}
			if cfg.HasProwlarr() != tt.expect {
				t.Errorf("HasProwlarr()=%v, want %v", cfg.HasProwlarr(), tt.expect)
			}
		})
	}
}

func TestHasQBittorrent(t *testing.T) {
	cfg := &Config{QBURL: "http://qbit:8080"}
	if !cfg.HasQBittorrent() {
		t.Error("expected true when URL is set")
	}
	cfg.QBURL = ""
	if cfg.HasQBittorrent() {
		t.Error("expected false when URL is empty")
	}
}

func TestHasSABnzbd(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		key    string
		expect bool
	}{
		{"both set", "http://sab:8080", "apikey", true},
		{"no key", "http://sab:8080", "", false},
		{"no url", "", "apikey", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{SABnzbdURL: tt.url, SABnzbdAPIKey: tt.key}
			if cfg.HasSABnzbd() != tt.expect {
				t.Errorf("HasSABnzbd()=%v, want %v", cfg.HasSABnzbd(), tt.expect)
			}
		})
	}
}

func TestHasNZBGet(t *testing.T) {
	cfg := &Config{NZBGetURL: "http://nzbget:6789"}
	if !cfg.HasNZBGet() {
		t.Error("expected true when URL is set")
	}
	cfg.NZBGetURL = ""
	if cfg.HasNZBGet() {
		t.Error("expected false when URL is empty")
	}
}

func TestEnvBool(t *testing.T) {
	tests := []struct {
		value    string
		fallback bool
		expect   bool
	}{
		{"true", false, true},
		{"TRUE", false, true},
		{"1", false, true},
		{"yes", false, true},
		{"false", true, false},
		{"no", true, false},
		{"0", true, false},
		{"", false, false},
		{"", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			os.Setenv("TEST_BOOL", tt.value)
			defer os.Unsetenv("TEST_BOOL")
			got := envBool("TEST_BOOL", tt.fallback)
			if got != tt.expect {
				t.Errorf("envBool(%q, %v) = %v, want %v", tt.value, tt.fallback, got, tt.expect)
			}
		})
	}
}

func TestEnvIntSlice(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		fallback []int
		expect   []int
	}{
		{"valid", "1,2,3", nil, []int{1, 2, 3}},
		{"with spaces", " 1 , 2 , 3 ", nil, []int{1, 2, 3}},
		{"empty uses fallback", "", []int{7, 5}, []int{7, 5}},
		{"invalid uses fallback", "abc,def", []int{1}, []int{1}},
		{"mixed valid invalid", "1,abc,3", nil, []int{1, 3}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("TEST_SLICE", tt.value)
			defer os.Unsetenv("TEST_SLICE")
			got := envIntSlice("TEST_SLICE", tt.fallback)
			if len(got) != len(tt.expect) {
				t.Fatalf("got %v, want %v", got, tt.expect)
			}
			for i, v := range got {
				if v != tt.expect[i] {
					t.Errorf("got[%d]=%d, want %d", i, v, tt.expect[i])
				}
			}
		})
	}
}

func TestEnvInt(t *testing.T) {
	os.Setenv("TEST_INT", "42")
	defer os.Unsetenv("TEST_INT")

	if got := envInt("TEST_INT", 0); got != 42 {
		t.Errorf("got %d, want 42", got)
	}

	os.Setenv("TEST_INT", "not_a_number")
	if got := envInt("TEST_INT", 99); got != 99 {
		t.Errorf("got %d, want fallback 99", got)
	}

	os.Unsetenv("TEST_INT")
	if got := envInt("TEST_INT", 7); got != 7 {
		t.Errorf("got %d, want fallback 7", got)
	}
}

func TestFileListScanEnabled(t *testing.T) {
	if !Load().FileListScanEnabled {
		t.Error("FileListScanEnabled should default to true")
	}

	os.Setenv("FILE_LIST_SCAN_ENABLED", "false")
	defer os.Unsetenv("FILE_LIST_SCAN_ENABLED")
	if Load().FileListScanEnabled {
		t.Error("FILE_LIST_SCAN_ENABLED=false should turn the scan off")
	}
}

func TestVaultArchiveEnabled(t *testing.T) {
	if Load().VaultArchiveEnabled {
		t.Error("VaultArchiveEnabled should default to false")
	}

	os.Setenv("VAULT_ARCHIVE_ENABLED", "true")
	defer os.Unsetenv("VAULT_ARCHIVE_ENABLED")
	if !Load().VaultArchiveEnabled {
		t.Error("VAULT_ARCHIVE_ENABLED=true should turn archiving on")
	}
}
