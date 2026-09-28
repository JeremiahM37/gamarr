package download

import (
	"path/filepath"
	"testing"
)

func TestHacksSuffixRouting(t *testing.T) {
	const hack = "Super Mario World (USA) [h Kaizo].sfc"

	t.Run("off by default", func(t *testing.T) {
		m, jobID, src := ddlFixture(t, hack, nil)
		m.organizeDDLFile(jobID, src, "Kaizo Mario", "SNES", "snes", false)
		if !pathExists(filepath.Join(m.cfg.GamesRomsPath, "snes", hack)) {
			t.Error("with routing off a hack belongs under snes")
		}
	})

	t.Run("on routes hacks", func(t *testing.T) {
		m, jobID, src := ddlFixture(t, hack, nil)
		m.cfg.HacksSuffixRouting = true
		m.organizeDDLFile(jobID, src, "Kaizo Mario", "SNES", "snes", false)
		if !pathExists(filepath.Join(m.cfg.GamesRomsPath, "snes-hacks", hack)) {
			t.Error("with routing on a hack belongs under snes-hacks")
		}
	})

	t.Run("on leaves clean ROMs alone", func(t *testing.T) {
		m, jobID, src := ddlFixture(t, "Super Mario World (USA).sfc", nil)
		m.cfg.HacksSuffixRouting = true
		m.organizeDDLFile(jobID, src, "Super Mario World", "SNES", "snes", false)
		if !pathExists(filepath.Join(m.cfg.GamesRomsPath, "snes", "Super Mario World (USA).sfc")) {
			t.Error("a clean ROM belongs under snes")
		}
	})

	t.Run("on uses the release name too", func(t *testing.T) {
		m, jobID, src := ddlFixture(t, "Mother 3.gba", nil)
		m.cfg.HacksSuffixRouting = true
		m.organizeDDLFile(jobID, src, "Mother 3 [T+Eng1.3]", "GBA", "gba", false)
		if !pathExists(filepath.Join(m.cfg.GamesRomsPath, "gba-hacks", "Mother 3.gba")) {
			t.Error("a translation named only in the release belongs under gba-hacks")
		}
	})

	t.Run("nzb destination honours routing", func(t *testing.T) {
		m, _, _ := ddlFixture(t, "unused.bin", nil)
		m.cfg.HacksSuffixRouting = true
		dest, ok := m.nzbDestPath("/staging/Super Metroid Redesign", "Super Metroid (Hack) Redesign", "snes", false)
		if !ok || dest != filepath.Join(m.cfg.GamesRomsPath, "snes-hacks", "Super Metroid Redesign") {
			t.Errorf("nzb dest = %q, %v", dest, ok)
		}
	})
}

func TestScanLibraryHacksFolder(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)
	writeFileT(t, filepath.Join(cfg.GamesRomsPath, "snes-hacks", "Kaizo Mario [h1].sfc"), bigROM())

	m.ScanLibraryDirs()

	if item := jobs.FindLibraryByTitle("Kaizo Mario [h1]", "snes-hacks"); item == nil || item.Platform != "SNES Hacks" {
		t.Errorf("hacks folder not scanned with its platform name: %+v", item)
	}
	if got := platformNameFromSlug("gbc-hacks"); got != "Game Boy Color Hacks" {
		t.Errorf("platformNameFromSlug(gbc-hacks) = %q", got)
	}
}
