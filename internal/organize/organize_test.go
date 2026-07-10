package organize

import (
	"archive/zip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gamarr/internal/config"
)

func newTestPipeline(t *testing.T) (*Pipeline, string, string) {
	t.Helper()
	root := t.TempDir()
	vault := filepath.Join(root, "vault")
	roms := filepath.Join(root, "roms")
	cfg := &config.Config{GamesVaultPath: vault, GamesRomsPath: roms}
	return NewPipeline(cfg), vault, roms
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip %s: %v", path, err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create entry: %v", err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write entry: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
}

// ── OrganizeGame ─────────────────────────────────────────────────────────────

func TestOrganizeGameROM(t *testing.T) {
	p, _, roms := newTestPipeline(t)
	src := filepath.Join(t.TempDir(), "Chrono Trigger (USA).sfc")
	writeFile(t, src, "romdata")

	dest, err := p.OrganizeGame(src, "SNES", "snes", false)
	if err != nil {
		t.Fatalf("OrganizeGame: %v", err)
	}
	want := filepath.Join(roms, "snes", "Chrono Trigger (USA).sfc")
	if dest != want {
		t.Errorf("dest = %q, want %q", dest, want)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("destination file missing: %v", err)
	}
	if string(data) != "romdata" {
		t.Errorf("content = %q, want romdata", data)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("source file was not removed")
	}
}

func TestOrganizeGamePCCleansSceneName(t *testing.T) {
	p, vault, _ := newTestPipeline(t)
	src := filepath.Join(t.TempDir(), "Cool.Game.v1.2.3-CODEX.exe")
	writeFile(t, src, "exedata")

	dest, err := p.OrganizeGame(src, "PC", "", true)
	if err != nil {
		t.Fatalf("OrganizeGame: %v", err)
	}
	if filepath.Dir(dest) != vault {
		t.Errorf("dest dir = %q, want vault %q", filepath.Dir(dest), vault)
	}
	base := filepath.Base(dest)
	if strings.Contains(base, "CODEX") {
		t.Errorf("scene group not stripped: %q", base)
	}
	if !strings.HasSuffix(base, ".exe") {
		t.Errorf("extension not preserved: %q", base)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("destination missing: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("source file was not removed")
	}
}

func TestOrganizeGameDirectorySource(t *testing.T) {
	p, vault, _ := newTestPipeline(t)
	srcRoot := t.TempDir()
	src := filepath.Join(srcRoot, "CoolGame")
	writeFile(t, filepath.Join(src, "setup.exe"), "installer")
	writeFile(t, filepath.Join(src, "data", "level1.dat"), "level")

	dest, err := p.OrganizeGame(src, "PC", "", true)
	if err != nil {
		t.Fatalf("OrganizeGame: %v", err)
	}
	want := filepath.Join(vault, "CoolGame")
	if dest != want {
		t.Errorf("dest = %q, want %q", dest, want)
	}
	for _, rel := range []string{"setup.exe", filepath.Join("data", "level1.dat")} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
			t.Errorf("missing %s in destination: %v", rel, err)
		}
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("source directory was not removed")
	}
}

func TestOrganizeGameDuplicate(t *testing.T) {
	t.Run("ROM", func(t *testing.T) {
		p, _, roms := newTestPipeline(t)
		src := filepath.Join(t.TempDir(), "Mario.nes")
		writeFile(t, src, "new")
		existing := filepath.Join(roms, "nes", "Mario.nes")
		writeFile(t, existing, "old")

		dest, err := p.OrganizeGame(src, "NES", "nes", false)
		if err == nil {
			t.Fatal("expected duplicate error, got nil")
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Errorf("error = %q, want already-exists", err)
		}
		if dest != existing {
			t.Errorf("dest = %q, want existing path %q", dest, existing)
		}
		if _, statErr := os.Stat(src); statErr != nil {
			t.Error("source should be untouched on duplicate")
		}
		data, _ := os.ReadFile(existing)
		if string(data) != "old" {
			t.Error("existing file was overwritten")
		}
	})

	t.Run("PC", func(t *testing.T) {
		p, vault, _ := newTestPipeline(t)
		src := filepath.Join(t.TempDir(), "Doom.exe")
		writeFile(t, src, "new")
		writeFile(t, filepath.Join(vault, "Doom.exe"), "old")

		_, err := p.OrganizeGame(src, "PC", "", true)
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Errorf("expected already-exists error, got %v", err)
		}
	})
}

func TestOrganizeGameMissingSource(t *testing.T) {
	p, _, _ := newTestPipeline(t)
	_, err := p.OrganizeGame(filepath.Join(t.TempDir(), "nope.nes"), "NES", "nes", false)
	if err == nil || !strings.Contains(err.Error(), "source path not found") {
		t.Errorf("expected source-not-found error, got %v", err)
	}
}

func TestOrganizeGameUnknownPlatform(t *testing.T) {
	p, _, _ := newTestPipeline(t)
	src := filepath.Join(t.TempDir(), "mystery.bin")
	writeFile(t, src, "data")

	dest, err := p.OrganizeGame(src, "", "", false)
	if err == nil || !strings.Contains(err.Error(), "unknown platform") {
		t.Errorf("expected unknown-platform error, got %v", err)
	}
	if dest != src {
		t.Errorf("dest = %q, want unchanged source %q", dest, src)
	}
	if _, statErr := os.Stat(src); statErr != nil {
		t.Error("source should be untouched")
	}
}

// ── DetectPlatform ───────────────────────────────────────────────────────────

func TestDetectPlatform(t *testing.T) {
	tests := []struct {
		filename string
		platform string
		slug     string
		isPC     bool
	}{
		{"game.nsp", "Switch", "switch", false},
		{"game.xci", "Switch", "switch", false},
		{"GAME.NSP", "Switch", "switch", false}, // case-insensitive extension
		{"game.3ds", "3DS", "3ds", false},
		{"game.cia", "3DS", "3ds", false},
		{"game.nds", "DS", "nds", false},
		{"game.gba", "Game Boy Advance", "gba", false},
		{"game.gb", "Game Boy", "gb", false},
		{"game.gbc", "Game Boy Color", "gbc", false},
		{"game.z64", "N64", "n64", false},
		{"game.nes", "NES", "nes", false},
		{"game.sfc", "SNES", "snes", false},
		{"game.smc", "SNES", "snes", false},
		{"game.gcz", "GameCube", "ngc", false},
		{"game.wbfs", "Wii", "wii", false},
		{"game.rpx", "Wii U", "wiiu", false},
		{"game.cso", "PSP", "psp", false},
		{"game.pkg", "PS3", "ps3", false},
		{"game.gdi", "Dreamcast", "dc", false},
		{"setup.exe", "PC", "", true},
		{"installer.msi", "PC", "", true},
		// ISO heuristics.
		{"Cool Game (PS2).iso", "PS2", "ps2", false},
		{"Cool Game PSP.iso", "PSP", "psp", false},
		{"Cool Game psx.iso", "PS1", "psx", false},
		{"Cool Game xbox.iso", "Xbox", "xbox", false},
		{"Cool Game wii.iso", "Wii", "wii", false},
		{"Cool Game gamecube.iso", "GameCube", "ngc", false},
		{"mystery.iso", "", "", false},
		// Unknown.
		{"readme.txt", "", "", false},
		{"noextension", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			platform, slug, isPC := DetectPlatform(tt.filename)
			if platform != tt.platform || slug != tt.slug || isPC != tt.isPC {
				t.Errorf("DetectPlatform(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.filename, platform, slug, isPC, tt.platform, tt.slug, tt.isPC)
			}
		})
	}
}

// ── CleanFilename ────────────────────────────────────────────────────────────

func TestCleanFilename(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain name kept", "Chrono Trigger.sfc", "Chrono Trigger.sfc"},
		{"scene dots to spaces", "Cool.Game.v1.2.3.exe", "Cool Game v1 2 3.exe"},
		{"scene group stripped", "Cool Game SKIDROW.exe", "Cool Game.exe"},
		{"dash group stripped", "Cool Game - RELOADED.exe", "Cool Game.exe"},
		{"brackets stripped", "Mario [USA] [!].nes", "Mario.nes"},
		{"unsafe chars stripped", `Ga<me>:Na"me.zip`, "GameName.zip"},
		{"repack tag stripped", "Cool Game REPACK.exe", "Cool Game.exe"},
		{"multi tag scene name", "Cool.Game.MULTI5.PROPER.v2.0-CODEX.exe", "Cool Game v2 0.exe"},
		{"all tags becomes unknown", "CODEX.exe", "Unknown.exe"},
		{"no extension", "Some Game", "Some Game"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CleanFilename(tt.in); got != tt.want {
				t.Errorf("CleanFilename(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ── DuplicateCheck ───────────────────────────────────────────────────────────

func TestDuplicateCheck(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing path", func(t *testing.T) {
		exists, err := DuplicateCheck(filepath.Join(dir, "nothing-here"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if exists {
			t.Error("exists = true for missing path")
		}
	})

	t.Run("existing file", func(t *testing.T) {
		f := filepath.Join(dir, "present.bin")
		writeFile(t, f, "x")
		exists, err := DuplicateCheck(f)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !exists {
			t.Error("exists = false for existing file")
		}
	})

	t.Run("existing directory", func(t *testing.T) {
		exists, err := DuplicateCheck(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !exists {
			t.Error("exists = false for existing directory")
		}
	})
}

// ── ExtractArchives ──────────────────────────────────────────────────────────

func TestExtractArchivesSkipsAlreadyExtracted(t *testing.T) {
	dir := t.TempDir()
	writeZip(t, filepath.Join(dir, "game.zip"), map[string]string{"rom.nes": "data"})
	// Pre-existing .extracted dir means the archive is skipped without running 7z.
	if err := os.MkdirAll(filepath.Join(dir, "game.zip.extracted"), 0755); err != nil {
		t.Fatal(err)
	}

	extracted := ExtractArchives(dir)
	if len(extracted) != 0 {
		t.Errorf("extracted = %v, want none (already extracted)", extracted)
	}
}

func TestExtractArchivesEmptyDir(t *testing.T) {
	if got := ExtractArchives(t.TempDir()); len(got) != 0 {
		t.Errorf("extracted = %v, want none for empty dir", got)
	}
}

func TestExtractArchivesZip(t *testing.T) {
	dir := t.TempDir()
	top := filepath.Join(dir, "game.zip")
	writeZip(t, top, map[string]string{"rom.nes": "topdata"})

	// A zip in a subdirectory exercises the recursion path.
	sub := filepath.Join(dir, "nested")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(sub, "inner.zip")
	writeZip(t, inner, map[string]string{"rom2.gba": "innerdata"})

	extracted := ExtractArchives(dir)

	if _, err := exec.LookPath("7z"); err != nil {
		// Without 7z the extraction fails: nothing reported and the temp
		// .extracted dirs must be cleaned up.
		if len(extracted) != 0 {
			t.Errorf("extracted = %v, want none without 7z", extracted)
		}
		for _, p := range []string{top + ".extracted", inner + ".extracted"} {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Errorf("%s should be removed after failed extraction", p)
			}
		}
		return
	}

	// With 7z available both archives extract.
	if len(extracted) != 2 {
		t.Fatalf("extracted = %v, want 2 archives", extracted)
	}
	for path, want := range map[string]string{
		filepath.Join(top+".extracted", "rom.nes"):    "topdata",
		filepath.Join(inner+".extracted", "rom2.gba"): "innerdata",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("missing extracted file: %v", err)
			continue
		}
		if string(data) != want {
			t.Errorf("%s content = %q, want %q", path, data, want)
		}
	}

	// A second pass skips everything already extracted.
	if again := ExtractArchives(dir); len(again) != 0 {
		t.Errorf("second pass extracted = %v, want none", again)
	}
}
