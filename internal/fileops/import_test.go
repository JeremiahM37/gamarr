package fileops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// sameFile reports whether two paths are the same inode — the property that
// makes a hardlink import cost no extra disk.
func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	fa, err := os.Stat(a)
	if err != nil {
		t.Fatalf("stat %s: %v", a, err)
	}
	fb, err := os.Stat(b)
	if err != nil {
		t.Fatalf("stat %s: %v", b, err)
	}
	return os.SameFile(fa, fb)
}

// ── mode plumbing ────────────────────────────────────────────────────────────

func TestParseMode(t *testing.T) {
	tests := []struct {
		in      string
		want    Mode
		wantErr bool
	}{
		{"", ModeMove, false}, // unset keeps the pre-existing behavior
		{"move", ModeMove, false},
		{"hardlink", ModeHardlink, false},
		{"symlink", ModeSymlink, false},
		{"copy", ModeCopy, false},
		{"hardlinks", "", true},
		{"link", "", true},
		{"MOVE", "", true}, // callers lowercase first
	}
	for _, tt := range tests {
		got, err := ParseMode(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseMode(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ParseMode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseModeErrorNamesTheOptions(t *testing.T) {
	_, err := ParseMode("hardlnk")
	if err == nil {
		t.Fatal("want an error for a typo'd mode")
	}
	for _, want := range []string{"move", "hardlink", "symlink", "copy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestParseFallback(t *testing.T) {
	if got, err := ParseFallback(""); err != nil || got != FallbackError {
		t.Errorf("ParseFallback(\"\") = %q, %v; want error fallback", got, err)
	}
	for _, in := range []string{"error", "copy", "symlink", "move"} {
		if _, err := ParseFallback(in); err != nil {
			t.Errorf("ParseFallback(%q) unexpected error: %v", in, err)
		}
	}
	if _, err := ParseFallback("nonsense"); err == nil {
		t.Error("want an error for an unknown fallback")
	}
}

func TestPreservesSource(t *testing.T) {
	tests := map[Mode]bool{
		ModeMove:     false,
		ModeHardlink: true,
		ModeSymlink:  true,
		ModeCopy:     true,
	}
	for mode, want := range tests {
		if got := mode.PreservesSource(); got != want {
			t.Errorf("%q.PreservesSource() = %v, want %v", mode, got, want)
		}
	}
}

// ── single files ─────────────────────────────────────────────────────────────

func TestImportFileByMode(t *testing.T) {
	tests := []struct {
		mode           Mode
		wantSrcSurvive bool
		wantSameInode  bool
		wantSymlink    bool
	}{
		{ModeMove, false, false, false},
		{ModeHardlink, true, true, false},
		{ModeSymlink, true, false, true},
		{ModeCopy, true, false, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "downloads", "game.rom")
			dest := filepath.Join(dir, "library", "game.rom")
			writeFile(t, src, "ROMDATA")

			if err := Import(src, dest, Options{Mode: tt.mode}); err != nil {
				t.Fatalf("Import: %v", err)
			}

			if got := read(t, dest); got != "ROMDATA" {
				t.Errorf("dest content = %q, want ROMDATA", got)
			}
			_, err := os.Lstat(src)
			if survived := err == nil; survived != tt.wantSrcSurvive {
				t.Errorf("source survived = %v, want %v", survived, tt.wantSrcSurvive)
			}
			if tt.wantSameInode && !sameFile(t, src, dest) {
				t.Error("hardlink import did not produce the same inode")
			}
			if tt.mode == ModeCopy && sameFile(t, src, dest) {
				t.Error("copy import must produce an independent file")
			}
			fi, err := os.Lstat(dest)
			if err != nil {
				t.Fatalf("lstat dest: %v", err)
			}
			isLink := fi.Mode()&os.ModeSymlink != 0
			if isLink != tt.wantSymlink {
				t.Errorf("dest is symlink = %v, want %v", isLink, tt.wantSymlink)
			}
		})
	}
}

// The regression the seeding issue reports: a hardlink import must leave the
// download client's copy of the data byte-for-byte in place.
func TestHardlinkImportLeavesSeedableSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "downloads", "Game", "disc.bin")
	dest := filepath.Join(dir, "library", "Game", "disc.bin")
	writeFile(t, src, "PAYLOAD")

	if err := Import(filepath.Dir(src), filepath.Dir(dest), Options{Mode: ModeHardlink}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	if got := read(t, src); got != "PAYLOAD" {
		t.Errorf("source content after import = %q, want PAYLOAD", got)
	}
	if !sameFile(t, src, dest) {
		t.Error("library entry is not the same inode as the seeded file")
	}

	// Removing the library entry must not take the seeded data with it.
	if err := os.Remove(dest); err != nil {
		t.Fatalf("remove dest: %v", err)
	}
	if got := read(t, src); got != "PAYLOAD" {
		t.Errorf("source after removing the library link = %q, want PAYLOAD", got)
	}
}

func TestSymlinkImportPointsAtAnAbsoluteSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "downloads", "game.rom")
	dest := filepath.Join(dir, "library", "game.rom")
	writeFile(t, src, "ROMDATA")

	if err := Import(src, dest, Options{Mode: ModeSymlink}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	target, err := os.Readlink(dest)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if !filepath.IsAbs(target) {
		t.Errorf("symlink target %q is not absolute", target)
	}
	if target != src {
		t.Errorf("symlink target = %q, want %q", target, src)
	}
}

func TestImportMissingSource(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range Modes {
		if err := Import(filepath.Join(dir, "nope"), filepath.Join(dir, "out"), Options{Mode: mode}); err == nil {
			t.Errorf("%s: want an error for a missing source", mode)
		}
	}
}

func TestImportUnknownMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a")
	writeFile(t, src, "x")
	if err := Import(src, filepath.Join(dir, "b"), Options{Mode: "teleport"}); err == nil {
		t.Error("want an error for an unknown mode")
	}
}

func TestImportZeroModeMoves(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a")
	dest := filepath.Join(dir, "b")
	writeFile(t, src, "x")

	if err := Import(src, dest, Options{}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if _, err := os.Lstat(src); err == nil {
		t.Error("the zero Options must behave as a move")
	}
}

// Import creates missing parent directories rather than failing on a library
// subdirectory that does not exist yet.
func TestImportCreatesDestParent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "game.rom")
	writeFile(t, src, "x")
	dest := filepath.Join(dir, "library", "snes", "game.rom")

	if err := Import(src, dest, Options{Mode: ModeCopy}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("dest not created: %v", err)
	}
}

// ── directories ──────────────────────────────────────────────────────────────

func TestImportDirectoryByMode(t *testing.T) {
	for _, mode := range Modes {
		t.Run(string(mode), func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "downloads", "Big Game")
			dest := filepath.Join(dir, "library", "Big Game")
			writeFile(t, filepath.Join(src, "setup.exe"), "SETUP")
			writeFile(t, filepath.Join(src, "data", "assets.pak"), "ASSETS")

			if err := Import(src, dest, Options{Mode: mode}); err != nil {
				t.Fatalf("Import: %v", err)
			}

			if mode == ModeSymlink {
				// One link stands in for the whole tree.
				fi, err := os.Lstat(dest)
				if err != nil {
					t.Fatalf("lstat: %v", err)
				}
				if fi.Mode()&os.ModeSymlink == 0 {
					t.Fatal("directory symlink import did not create a symlink")
				}
			}

			if got := read(t, filepath.Join(dest, "setup.exe")); got != "SETUP" {
				t.Errorf("setup.exe = %q, want SETUP", got)
			}
			if got := read(t, filepath.Join(dest, "data", "assets.pak")); got != "ASSETS" {
				t.Errorf("assets.pak = %q, want ASSETS", got)
			}

			_, err := os.Lstat(src)
			if survived := err == nil; survived != mode.PreservesSource() {
				t.Errorf("source survived = %v, want %v", survived, mode.PreservesSource())
			}
			if mode == ModeHardlink {
				if !sameFile(t, filepath.Join(src, "setup.exe"), filepath.Join(dest, "setup.exe")) {
					t.Error("top-level file was not hardlinked")
				}
				if !sameFile(t, filepath.Join(src, "data", "assets.pak"), filepath.Join(dest, "data", "assets.pak")) {
					t.Error("nested file was not hardlinked")
				}
			}
		})
	}
}

func TestHardlinkImportPreservesEmptyDirs(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dest := filepath.Join(dir, "dest")
	writeFile(t, filepath.Join(src, "rom.bin"), "x")
	if err := os.MkdirAll(filepath.Join(src, "saves"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := Import(src, dest, Options{Mode: ModeHardlink}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dest, "saves"))
	if err != nil || !fi.IsDir() {
		t.Errorf("empty subdirectory not mirrored: %v", err)
	}
}

// ── cross-filesystem behavior ────────────────────────────────────────────────

// crossDeviceDirs returns two directories on different filesystems, skipping
// the test when the machine cannot provide them.
func crossDeviceDirs(t *testing.T) (string, string) {
	t.Helper()
	local := t.TempDir()

	other, err := os.MkdirTemp("/dev/shm", "gamarr-xdev-")
	if err != nil {
		t.Skipf("no second filesystem available: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(other) })

	var a, b syscall.Stat_t
	if err := syscall.Stat(local, &a); err != nil {
		t.Skipf("stat %s: %v", local, err)
	}
	if err := syscall.Stat(other, &b); err != nil {
		t.Skipf("stat %s: %v", other, err)
	}
	if a.Dev == b.Dev {
		t.Skip("temp dirs are on the same filesystem")
	}
	return local, other
}

func TestHardlinkCrossDeviceErrorsByDefault(t *testing.T) {
	downloads, library := crossDeviceDirs(t)
	src := filepath.Join(downloads, "game.rom")
	dest := filepath.Join(library, "game.rom")
	writeFile(t, src, "ROMDATA")

	err := Import(src, dest, Options{Mode: ModeHardlink})
	if err == nil {
		t.Fatal("want an error when hardlinking across filesystems")
	}
	if !errors.Is(err, ErrCrossDevice) {
		t.Errorf("error %v does not match ErrCrossDevice", err)
	}
	if !errors.Is(err, syscall.EXDEV) {
		t.Errorf("error %v does not wrap EXDEV", err)
	}
	// The message has to tell the user how to fix it.
	if !strings.Contains(err.Error(), "IMPORT_HARDLINK_FALLBACK") {
		t.Errorf("error %q does not name the fallback setting", err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("a failed hardlink import must not leave a destination behind")
	}
	if read(t, src) != "ROMDATA" {
		t.Error("a failed import must not touch the source")
	}
}

func TestHardlinkCrossDeviceFallbacks(t *testing.T) {
	tests := []struct {
		fallback       Fallback
		wantSrcSurvive bool
	}{
		{FallbackCopy, true},
		{FallbackSymlink, true},
		{FallbackMove, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.fallback), func(t *testing.T) {
			downloads, library := crossDeviceDirs(t)
			src := filepath.Join(downloads, "game.rom")
			dest := filepath.Join(library, "game.rom")
			writeFile(t, src, "ROMDATA")

			if err := Import(src, dest, Options{Mode: ModeHardlink, HardlinkFallback: tt.fallback}); err != nil {
				t.Fatalf("Import: %v", err)
			}
			if got := read(t, dest); got != "ROMDATA" {
				t.Errorf("dest content = %q, want ROMDATA", got)
			}
			_, err := os.Lstat(src)
			if survived := err == nil; survived != tt.wantSrcSurvive {
				t.Errorf("source survived = %v, want %v", survived, tt.wantSrcSurvive)
			}
		})
	}
}

// A directory import that hits the boundary part-way through must not leave a
// half-linked tree behind for the fallback to merge into.
func TestHardlinkCrossDeviceDirectoryFallbackIsClean(t *testing.T) {
	downloads, library := crossDeviceDirs(t)
	src := filepath.Join(downloads, "Big Game")
	dest := filepath.Join(library, "Big Game")
	writeFile(t, filepath.Join(src, "setup.exe"), "SETUP")
	writeFile(t, filepath.Join(src, "data", "assets.pak"), "ASSETS")

	if err := Import(src, dest, Options{Mode: ModeHardlink, HardlinkFallback: FallbackCopy}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := read(t, filepath.Join(dest, "setup.exe")); got != "SETUP" {
		t.Errorf("setup.exe = %q, want SETUP", got)
	}
	if got := read(t, filepath.Join(dest, "data", "assets.pak")); got != "ASSETS" {
		t.Errorf("assets.pak = %q, want ASSETS", got)
	}
	if got := read(t, filepath.Join(src, "setup.exe")); got != "SETUP" {
		t.Error("copy fallback must leave the seeded source intact")
	}
}

// A cross-device move is the pre-existing behavior and must keep working.
func TestMoveCrossDevice(t *testing.T) {
	downloads, library := crossDeviceDirs(t)
	src := filepath.Join(downloads, "Game")
	dest := filepath.Join(library, "Game")
	writeFile(t, filepath.Join(src, "rom.bin"), "ROM")

	if err := Import(src, dest, Options{Mode: ModeMove}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := read(t, filepath.Join(dest, "rom.bin")); got != "ROM" {
		t.Errorf("dest content = %q, want ROM", got)
	}
	if _, err := os.Stat(src); err == nil {
		t.Error("move must remove the source")
	}
}

// ── helpers used by the rest of the tree ─────────────────────────────────────

func TestCopyFilePreservesMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "run.sh")
	writeFile(t, src, "#!/bin/sh\n")
	if err := os.Chmod(src, 0755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	dest := filepath.Join(dir, "copy.sh")
	if err := CopyFile(src, dest); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}
	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0755 {
		t.Errorf("dest mode = %v, want 0755", fi.Mode().Perm())
	}
}

func TestCopyFileMissingSource(t *testing.T) {
	if err := CopyFile("/no/such/file", filepath.Join(t.TempDir(), "out")); err == nil {
		t.Error("want an error for a missing source")
	}
}

func TestCopyFileTruncatesExistingDest(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dest := filepath.Join(dir, "dest")
	writeFile(t, src, "new")
	writeFile(t, dest, "much longer old content")

	if err := CopyFile(src, dest); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}
	if got := read(t, dest); got != "new" {
		t.Errorf("dest = %q, want %q", got, "new")
	}
}
