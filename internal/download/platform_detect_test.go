package download

import (
	"path/filepath"
	"testing"
)

func ddlFixture(t *testing.T, fileName string, job map[string]interface{}) (*Manager, string, string) {
	t.Helper()
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)
	jobID := newJobID()
	row := map[string]interface{}{"status": "organizing", "error": nil}
	for k, v := range job {
		row[k] = v
	}
	jobs.Set(jobID, row)
	src := filepath.Join(t.TempDir(), fileName)
	writeFileT(t, src, []byte("rom"))
	return m, jobID, src
}

// With no platform on the job, the ROM extension files the download - for the
// retro platforms the registry added as much as for the old ones.
func TestOrganizeDDLDetectsRetroPlatformFromExtension(t *testing.T) {
	for file, slug := range map[string]string{
		"Zelda DX (USA).gbc":          "gbc",
		"Alex Kidd (USA).sms":         "sms",
		"Sonic Chaos (USA).gg":        "gamegear",
		"Bonk (USA).pce":              "tg16",
		"Ninja Golf (USA).a78":        "atari7800",
		"Metal Slug (Europe).ngc":     "ngp",
		"Knuckles Chaotix (USA).32x":  "sega32",
		"Lemmings (Europe) Disk1.adf": "amiga",
	} {
		t.Run(slug, func(t *testing.T) {
			m, jobID, src := ddlFixture(t, file, nil)
			m.organizeDDLFile(jobID, src, "Some Game", "Unknown", "", false)
			if dest := filepath.Join(m.cfg.GamesRomsPath, slug, file); !pathExists(dest) {
				t.Errorf("%s not filed under %s", file, slug)
			}
			job, _ := m.Jobs().Get(jobID)
			if got, _ := job["platform_slug"].(string); got != slug {
				t.Errorf("job platform_slug = %q, want %s", got, slug)
			}
		})
	}
}

// A platform assigned from search context alone is overruled when the files
// plainly belong to another platform.
func TestOrganizeDDLSanityCheckReclassifies(t *testing.T) {
	m, jobID, src := ddlFixture(t, "Pokemon Emerald (USA).gba", nil)
	m.organizeDDLFile(jobID, src, "Pokemon Emerald", "SNES", "snes", false)
	if !pathExists(filepath.Join(m.cfg.GamesRomsPath, "gba", "Pokemon Emerald (USA).gba")) {
		t.Error("GBA ROM on a SNES job was not refiled under gba")
	}
	if pathExists(filepath.Join(m.cfg.GamesRomsPath, "snes", "Pokemon Emerald (USA).gba")) {
		t.Error("GBA ROM filed under snes")
	}
}

func TestOrganizeDDLSanityCheckKeepsMatchingPlatform(t *testing.T) {
	m, jobID, src := ddlFixture(t, "Chrono Trigger (USA).sfc", nil)
	m.organizeDDLFile(jobID, src, "Chrono Trigger", "SNES", "snes", false)
	if !pathExists(filepath.Join(m.cfg.GamesRomsPath, "snes", "Chrono Trigger (USA).sfc")) {
		t.Error("SNES ROM not filed under snes")
	}
}

// A platform the operator picked by hand is not second-guessed.
func TestManualPlatformSkipsSanityCheck(t *testing.T) {
	m, jobID, src := ddlFixture(t, "Tetris DX (World).gbc", map[string]interface{}{
		"platform_source": platformSourceManual,
	})
	m.organizeDDLFile(jobID, src, "Tetris DX", "Game Boy", "gb", false)
	if !pathExists(filepath.Join(m.cfg.GamesRomsPath, "gb", "Tetris DX (World).gbc")) {
		t.Error("manually chosen platform was overridden")
	}
}
