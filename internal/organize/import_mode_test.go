package organize

import (
	"os"
	"path/filepath"
	"testing"

	"gamarr/internal/config"
	"gamarr/internal/fileops"
)

func newModePipeline(t *testing.T, mode fileops.Mode) (*Pipeline, string, string) {
	t.Helper()
	root := t.TempDir()
	vault := filepath.Join(root, "vault")
	roms := filepath.Join(root, "roms")
	cfg := &config.Config{GamesVaultPath: vault, GamesRomsPath: roms, ImportMode: mode}
	return NewPipeline(cfg), vault, roms
}

// A manual import is usually pointed at a directory that is still seeding, so
// it honors the import mode too.
func TestManualImportHonorsImportMode(t *testing.T) {
	tests := []struct {
		mode           fileops.Mode
		wantSrcSurvive bool
	}{
		{fileops.ModeMove, false},
		{fileops.ModeHardlink, true},
		{fileops.ModeSymlink, true},
		{fileops.ModeCopy, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			p, _, roms := newModePipeline(t, tt.mode)
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
			if got, err := os.ReadFile(dest); err != nil || string(got) != "romdata" {
				t.Fatalf("dest content = %q, %v", got, err)
			}
			_, err = os.Lstat(src)
			if survived := err == nil; survived != tt.wantSrcSurvive {
				t.Errorf("source survived = %v, want %v", survived, tt.wantSrcSurvive)
			}
			if tt.mode == fileops.ModeHardlink {
				fs, _ := os.Stat(src)
				fd, _ := os.Stat(dest)
				if !os.SameFile(fs, fd) {
					t.Error("manual hardlink import did not share the inode")
				}
			}
		})
	}
}

// An unset ImportMode on a hand-built config must behave exactly as before.
func TestManualImportZeroModeMoves(t *testing.T) {
	p, vault, _ := newModePipeline(t, "")
	src := filepath.Join(t.TempDir(), "Some Game")
	writeFile(t, filepath.Join(src, "setup.exe"), "x")

	dest, err := p.OrganizeGame(src, "PC", "", true)
	if err != nil {
		t.Fatalf("OrganizeGame: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "setup.exe")); err != nil {
		t.Fatalf("not imported into the vault: %v", err)
	}
	if _, err := os.Stat(src); err == nil {
		t.Error("default mode must move the source")
	}
	if filepath.Dir(dest) != vault {
		t.Errorf("dest = %q, want it under %q", dest, vault)
	}
}
