package download

import (
	"bytes"
	"path/filepath"
	"testing"
)

// bigROM is >1MB so ROM scans don't skip it as a small sidecar file.
func bigROM() []byte {
	return bytes.Repeat([]byte("R"), 1_100_000)
}

func TestScanLibraryDirs(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)

	// Vault: every top-level entry is one PC game.
	writeFileT(t, filepath.Join(cfg.GamesVaultPath, "Game One", "setup.exe"), []byte("x"))
	writeFileT(t, filepath.Join(cfg.GamesVaultPath, "Game Two.zip"), []byte("archive"))
	writeFileT(t, filepath.Join(cfg.GamesVaultPath, ".hidden"), []byte("x"))
	writeFileT(t, filepath.Join(cfg.GamesVaultPath, "loose.gamarr.json"), []byte("{}"))

	// ROMs: platform dirs with mixed content.
	snes := filepath.Join(cfg.GamesRomsPath, "snes")
	writeFileT(t, filepath.Join(snes, "Mario World.sfc"), bigROM())
	writeFileT(t, filepath.Join(snes, "tiny.sfc"), []byte("small"))              // <1MB skipped
	writeFileT(t, filepath.Join(snes, "[Update] Game v2.sfc"), bigROM())         // update skipped
	writeFileT(t, filepath.Join(snes, "Hero costume pack.sfc"), bigROM())        // DLC skipped
	writeFileT(t, filepath.Join(snes, "readme.txt"), []byte("not a game"))       // wrong ext
	writeFileT(t, filepath.Join(snes, "Nested Pack", "inner.sfc"), bigROM())     // game folder
	writeFileT(t, filepath.Join(snes, "org-only", "notes.md"), []byte("no rom")) // recursed, empty

	m.ScanLibraryDirs()

	wantSourceIDs := []string{
		"scan:" + filepath.Join(cfg.GamesVaultPath, "Game One"),
		"scan:" + filepath.Join(cfg.GamesVaultPath, "Game Two.zip"),
		"scan:" + filepath.Join(snes, "Mario World.sfc"),
		"scan:" + filepath.Join(snes, "Nested Pack"),
	}
	for _, id := range wantSourceIDs {
		if !jobs.LibraryHasSourceID(id) {
			t.Errorf("missing library entry %s", id)
		}
	}
	if total := jobs.LibraryTotal(); total != len(wantSourceIDs) {
		t.Errorf("library total = %d, want %d", total, len(wantSourceIDs))
	}

	t.Run("platform derived from slug", func(t *testing.T) {
		item := jobs.FindLibraryByTitle("Mario World", "snes")
		if item == nil {
			t.Fatal("Mario World not found in library")
		}
		if item.Platform != "SNES" || item.IsPC {
			t.Errorf("platform = %q isPC=%v, want SNES/false", item.Platform, item.IsPC)
		}
		if item.FileSize < 1_000_000 {
			t.Errorf("file size = %d, want >1MB", item.FileSize)
		}
	})

	t.Run("vault entries are PC", func(t *testing.T) {
		item := jobs.FindLibraryByTitle("Game Two", "")
		if item == nil {
			t.Fatal("Game Two not found (cleanTitle should strip .zip)")
		}
		if !item.IsPC || item.Platform != "PC" {
			t.Errorf("platform = %q isPC=%v, want PC/true", item.Platform, item.IsPC)
		}
	})

	t.Run("rescan does not duplicate", func(t *testing.T) {
		m.ScanLibraryDirs()
		if total := jobs.LibraryTotal(); total != len(wantSourceIDs) {
			t.Errorf("library total after rescan = %d, want %d", total, len(wantSourceIDs))
		}
	})
}

func TestScanLibraryDirsEmptyPaths(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.GamesVaultPath = ""
	cfg.GamesRomsPath = ""
	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)
	m.ScanLibraryDirs() // must not panic
	if total := jobs.LibraryTotal(); total != 0 {
		t.Errorf("library total = %d, want 0", total)
	}
}

func TestScanVaultMissingDir(t *testing.T) {
	cfg := newTestConfig(t)
	m := New(cfg, newTestJobs(t), nil)
	if n := m.scanVault(filepath.Join(t.TempDir(), "ghost")); n != 0 {
		t.Errorf("scanVault(missing) = %d, want 0", n)
	}
}

func TestScanDir(t *testing.T) {
	t.Run("missing dir", func(t *testing.T) {
		m := New(newTestConfig(t), newTestJobs(t), nil)
		if n := m.scanDir("/no/such/dir", "SNES", "snes", false); n != 0 {
			t.Errorf("scanDir = %d, want 0", n)
		}
	})

	t.Run("pc scan keeps small files", func(t *testing.T) {
		m := New(newTestConfig(t), newTestJobs(t), nil)
		dir := t.TempDir()
		writeFileT(t, filepath.Join(dir, "small-installer.exe"), []byte("tiny"))
		if n := m.scanDir(dir, "PC", "", true); n != 1 {
			t.Errorf("scanDir = %d, want 1 (PC files bypass the size filter)", n)
		}
	})

	t.Run("extracted dirs skipped", func(t *testing.T) {
		m := New(newTestConfig(t), newTestJobs(t), nil)
		dir := t.TempDir()
		writeFileT(t, filepath.Join(dir, "game.zip.extracted", "rom.sfc"), bigROM())
		if n := m.scanDir(dir, "SNES", "snes", false); n != 0 {
			t.Errorf("scanDir = %d, want 0 (.extracted skipped)", n)
		}
	})
}

func TestContainsGameFiles(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) string
		want  bool
	}{
		{
			name: "direct rom file",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				writeFileT(t, filepath.Join(dir, "game.nds"), []byte("rom"))
				return dir
			},
			want: true,
		},
		{
			name: "only non-game files",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				writeFileT(t, filepath.Join(dir, "readme.txt"), []byte("x"))
				return dir
			},
			want: false,
		},
		{
			name: "game files only in subdirectory",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				writeFileT(t, filepath.Join(dir, "sub", "game.gba"), []byte("rom"))
				return dir
			},
			want: false,
		},
		{
			name:  "missing directory",
			setup: func(t *testing.T) string { return "/no/such/dir" },
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := containsGameFiles(tt.setup(t)); got != tt.want {
				t.Errorf("containsGameFiles = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAddLibraryEntryDedup(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)

	fp := filepath.Join(t.TempDir(), "Game.sfc")
	writeFileT(t, fp, []byte("rom"))

	if n := m.addLibraryEntry(fp, "Game.sfc", "SNES", "snes", false); n != 1 {
		t.Fatalf("first add = %d, want 1", n)
	}
	if n := m.addLibraryEntry(fp, "Game.sfc", "SNES", "snes", false); n != 0 {
		t.Errorf("duplicate add = %d, want 0", n)
	}
}

func TestAddLibraryEntryDirectorySize(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)

	dir := filepath.Join(t.TempDir(), "Multi Disc Game")
	writeFileT(t, filepath.Join(dir, "disc1.iso"), []byte("12345"))
	writeFileT(t, filepath.Join(dir, "sub", "disc2.iso"), []byte("678"))

	if n := m.addLibraryEntry(dir, "Multi Disc Game", "PS1", "psx", false); n != 1 {
		t.Fatalf("add = %d, want 1", n)
	}
	item := jobs.FindLibraryByTitle("Multi Disc Game", "psx")
	if item == nil {
		t.Fatal("item not found")
	}
	if item.FileSize != 8 {
		t.Errorf("dir file size = %d, want 8", item.FileSize)
	}
}

func TestTrackInLibrary(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)

	m.TrackInLibrary("Tracked Game", "SNES", "snes", false, "/roms/snes/g.sfc", 42, "torrent", "prowlarr", "torrent:abc")

	if !jobs.LibraryHasSourceID("torrent:abc") {
		t.Fatal("library item not added")
	}
	item := jobs.FindLibraryByTitle("Tracked Game", "snes")
	if item == nil {
		t.Fatal("item not found by title")
	}
	if item.FileSize != 42 || item.Source != "torrent" || item.SourceType != "prowlarr" {
		t.Errorf("item = %+v", item)
	}

	t.Run("activity logged", func(t *testing.T) {
		entries, total := jobs.GetActivity(1, 50)
		if total == 0 {
			t.Fatal("no activity logged")
		}
		var found bool
		for _, e := range entries {
			if e.EventType == "import_completed" && e.Title == "Tracked Game" {
				found = true
			}
		}
		if !found {
			t.Errorf("import_completed activity missing: %+v", entries)
		}
	})

	t.Run("duplicate source id ignored", func(t *testing.T) {
		before := jobs.LibraryTotal()
		m.TrackInLibrary("Tracked Game", "SNES", "snes", false, "/other", 1, "torrent", "prowlarr", "torrent:abc")
		if after := jobs.LibraryTotal(); after != before {
			t.Errorf("library total = %d, want %d (dedup)", after, before)
		}
	})
}

func TestDirSize(t *testing.T) {
	t.Run("nested files summed", func(t *testing.T) {
		dir := t.TempDir()
		writeFileT(t, filepath.Join(dir, "a"), []byte("123"))
		writeFileT(t, filepath.Join(dir, "sub", "b"), []byte("45678"))
		if got := dirSize(dir); got != 8 {
			t.Errorf("dirSize = %d, want 8", got)
		}
	})

	t.Run("empty dir", func(t *testing.T) {
		if got := dirSize(t.TempDir()); got != 0 {
			t.Errorf("dirSize = %d, want 0", got)
		}
	})

	t.Run("missing dir", func(t *testing.T) {
		if got := dirSize("/no/such/dir"); got != 0 {
			t.Errorf("dirSize = %d, want 0", got)
		}
	})
}

func TestScanVaultSkipsHiddenAndSidecars(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)

	dir := t.TempDir()
	writeFileT(t, filepath.Join(dir, ".DS_Store"), []byte("x"))
	writeFileT(t, filepath.Join(dir, "Game.zip.gamarr.json"), []byte("{}"))
	writeFileT(t, filepath.Join(dir, "Real Game.zip"), []byte("x"))

	if n := m.scanVault(dir); n != 1 {
		t.Errorf("scanVault = %d, want 1", n)
	}
	if jobs.LibraryHasSourceID("scan:" + filepath.Join(dir, ".DS_Store")) {
		t.Error("hidden file should be skipped")
	}
	if jobs.LibraryHasSourceID("scan:" + filepath.Join(dir, "Game.zip.gamarr.json")) {
		t.Error("sidecar should be skipped")
	}
}
