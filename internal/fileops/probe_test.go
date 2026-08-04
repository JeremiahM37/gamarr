package fileops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// isEmpty reports whether dir contains nothing — the probe has to clean up
// after itself, including the link it managed to create.
func isEmpty(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	return len(entries) == 0
}

func TestCheckHardlinkPasses(t *testing.T) {
	downloads, library := t.TempDir(), t.TempDir()

	if err := CheckHardlink(downloads, library); err != nil {
		t.Fatalf("CheckHardlink on one filesystem: %v", err)
	}
	if !isEmpty(t, downloads) || !isEmpty(t, library) {
		t.Error("the probe left files behind")
	}
}

// The download directory and the library are allowed to be the same directory;
// the probe must not mistake its own file for a failure.
func TestCheckHardlinkSameDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := CheckHardlink(dir, dir); err != nil {
		t.Fatalf("CheckHardlink on one directory: %v", err)
	}
	if !isEmpty(t, dir) {
		t.Error("the probe left files behind")
	}
}

// The failure this whole check exists for. The message has to name both paths
// and point at the Docker layout, because on the setup that hits it the two
// paths really are on one filesystem.
func TestCheckHardlinkReportsCrossDevice(t *testing.T) {
	downloads, library := crossDeviceDirs(t)

	err := CheckHardlink(downloads, library)
	if err == nil {
		t.Fatal("want an error across filesystems")
	}
	if !errors.Is(err, ErrCrossDevice) {
		t.Errorf("error %v does not match ErrCrossDevice", err)
	}
	if !errors.Is(err, syscall.EXDEV) {
		t.Errorf("error %v does not wrap EXDEV", err)
	}
	for _, want := range []string{downloads, library, "bind mount", "IMPORT_HARDLINK_FALLBACK"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if !isEmpty(t, downloads) || !isEmpty(t, library) {
		t.Error("a failed probe left files behind")
	}
}

// A directory that is not mounted into the container is the other way this
// setup goes wrong, and it is worth saying so rather than reporting a bare
// stat error.
func TestCheckHardlinkMissingDirectory(t *testing.T) {
	present := t.TempDir()
	missing := filepath.Join(t.TempDir(), "never-mounted")

	err := CheckHardlink(missing, present)
	if err == nil {
		t.Fatal("want an error for a missing download directory")
	}
	if !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "mounted volume") {
		t.Errorf("error %q should name the path and the mount", err)
	}

	err = CheckHardlink(present, missing)
	if err == nil || !strings.Contains(err.Error(), "library directory") {
		t.Errorf("error %v should name the library directory", err)
	}
}

func TestCheckHardlinkRejectsNonDirectories(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a-file")
	writeFile(t, file, "not a directory")

	err := CheckHardlink(file, dir)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error %v should say the path is not a directory", err)
	}
}

func TestCheckHardlinkRejectsUnconfiguredPaths(t *testing.T) {
	dir := t.TempDir()
	if err := CheckHardlink("", dir); err == nil {
		t.Error("want an error when the download directory is unset")
	}
	if err := CheckHardlink(dir, ""); err == nil {
		t.Error("want an error when the library directory is unset")
	}
}

// A download directory Gamarr cannot write to cannot be probed, and the report
// should say that rather than blaming the filesystem boundary.
func TestCheckHardlinkUnwritableSource(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	downloads, library := t.TempDir(), t.TempDir()
	if err := os.Chmod(downloads, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(downloads, 0o700) })

	err := CheckHardlink(downloads, library)
	if err == nil || !strings.Contains(err.Error(), "cannot write") {
		t.Errorf("error %v should report the unwritable download directory", err)
	}
	if errors.Is(err, ErrCrossDevice) {
		t.Error("an unwritable directory is not a cross-device failure")
	}
}
