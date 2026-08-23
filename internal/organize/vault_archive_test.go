package organize

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gamarr/internal/config"
	"gamarr/internal/fileops"
)

func newArchivePipeline(t *testing.T) (*Pipeline, string) {
	t.Helper()
	p, vault := newFlaggedPipeline(t, true)
	return p, vault
}

func newFlaggedPipeline(t *testing.T, archive bool) (*Pipeline, string) {
	t.Helper()
	root := t.TempDir()
	vault := filepath.Join(root, "vault")
	cfg := &config.Config{
		GamesVaultPath:      vault,
		GamesRomsPath:       filepath.Join(root, "roms"),
		VaultArchiveEnabled: archive,
	}
	return NewPipeline(cfg), vault
}

// Whichever layout the vault already holds, the game is already there. Checking
// only the layout the flag currently selects stores it twice at full size, and
// cleanTitle then maps both copies to one title so the library lists it twice.
// The flag changing is exactly what an operator does when adopting it.
func TestOrganizePCRefusesEitherLayout(t *testing.T) {
	tests := []struct {
		name     string
		archive  bool
		existing string
	}{
		{"archiving, folder already there", true, "Game"},
		{"archiving, archive already there", true, "Game.tar"},
		{"not archiving, folder already there", false, "Game"},
		{"not archiving, archive already there", false, "Game.tar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, vault := newFlaggedPipeline(t, tt.archive)
			if strings.HasSuffix(tt.existing, ".tar") {
				writeFile(t, filepath.Join(vault, tt.existing), "OLD")
			} else {
				writeFile(t, filepath.Join(vault, tt.existing, "setup.exe"), "OLD")
			}

			src := filepath.Join(t.TempDir(), "Game")
			writeFile(t, filepath.Join(src, "setup.exe"), "NEW")

			dest, err := p.OrganizeGame(src, "PC", "", true)
			if err == nil {
				t.Fatal("OrganizeGame = nil, want a refusal")
			}
			// The path has to point into the vault even on the error: a caller
			// that files an already-exists result in the library would otherwise
			// record the download staging path, leaving a row aimed at something
			// meant to be released.
			if filepath.Dir(dest) != vault {
				t.Errorf("dest = %q, want a path in the vault %q", dest, vault)
			}

			entries, err := os.ReadDir(vault)
			if err != nil {
				t.Fatalf("read vault: %v", err)
			}
			if len(entries) != 1 {
				names := make([]string, len(entries))
				for i, e := range entries {
					names[i] = e.Name()
				}
				t.Errorf("vault holds %v, want only the copy that was already there", names)
			}
		})
	}
}

// Both library paths report an occupied destination with the same sentinel. They
// used to differ while carrying the same words, so a caller matching the error
// handled the PC case and silently failed the ROM one - which is the case a
// crash between organizing and filing the library row lands in.
func TestOccupiedDestinationIsTheSameErrorOnBothPaths(t *testing.T) {
	p, vault := newFlaggedPipeline(t, false)
	roms := filepath.Join(filepath.Dir(vault), "roms")

	writeFile(t, filepath.Join(vault, "Game", "setup.exe"), "OLD")
	writeFile(t, filepath.Join(roms, "snes", "Game.sfc"), "OLD")

	pcSrc := filepath.Join(t.TempDir(), "Game")
	writeFile(t, filepath.Join(pcSrc, "setup.exe"), "NEW")
	romSrc := filepath.Join(t.TempDir(), "Game.sfc")
	writeFile(t, romSrc, "NEW")

	for _, tt := range []struct {
		name string
		run  func() (string, error)
	}{
		{"pc", func() (string, error) { return p.OrganizeGame(pcSrc, "PC", "", true) }},
		{"rom", func() (string, error) { return p.OrganizeGame(romSrc, "SNES", "snes", false) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dest, err := tt.run()
			if !errors.Is(err, fileops.ErrDestinationOccupied) {
				t.Errorf("err = %v, want ErrDestinationOccupied", err)
			}
			if _, statErr := os.Stat(dest); statErr != nil {
				t.Errorf("dest = %q does not exist: %v", dest, statErr)
			}
		})
	}
}

func TestOrganizePCArchivesDirectory(t *testing.T) {
	p, vault := newArchivePipeline(t)
	src := filepath.Join(t.TempDir(), "Some.Game.v1.0-FitGirl")
	writeFile(t, filepath.Join(src, "setup.exe"), "SETUP")
	writeFile(t, filepath.Join(src, "fg-01.bin"), "PAYLOAD")

	dest, err := p.OrganizeGame(src, "PC", "", true)
	if err != nil {
		t.Fatalf("OrganizeGame: %v", err)
	}

	if filepath.Dir(dest) != vault || filepath.Ext(dest) != ".tar" {
		t.Errorf("dest = %q, want a .tar in %q", dest, vault)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("stat archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "fg-01.bin")); err != nil {
		t.Error("archiving destroyed the source")
	}
}

// A single file is already the one-file-per-game unit GameVault indexes, so
// wrapping it would only rewrite the bytes for nothing.
func TestOrganizePCLeavesSingleFileAlone(t *testing.T) {
	p, vault := newArchivePipeline(t)
	src := filepath.Join(t.TempDir(), "Lone Game.exe")
	writeFile(t, src, "SETUP")

	dest, err := p.OrganizeGame(src, "PC", "", true)
	if err != nil {
		t.Fatalf("OrganizeGame: %v", err)
	}

	if dest != filepath.Join(vault, "Lone Game.exe") {
		t.Errorf("dest = %q, want the file imported unwrapped", dest)
	}
}
