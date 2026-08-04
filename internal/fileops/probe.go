package fileops

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// CheckHardlink reports whether a hardlink import from srcDir into destDir
// would succeed, by attempting one link and removing it again. A nil return
// means hardlink imports between these two directories work.
//
// Comparing the two directories' st_dev is not a valid substitute. The kernel
// refuses to link across mount points, not merely across devices, so two Docker
// bind mounts of a single filesystem report an identical device and still fail
// with EXDEV — which is exactly what a compose file with one volume per
// directory produces. Only a trial link tells the truth.
//
// The probe doubles as a mount check: a download directory that is not visible
// inside the container fails here, at a point where the message can say so,
// rather than at import time as a missing-content error.
func CheckHardlink(srcDir, destDir string) error {
	for _, dir := range []struct{ role, path string }{
		{"download directory", srcDir},
		{"library directory", destDir},
	} {
		if dir.path == "" {
			return fmt.Errorf("no %s is configured", dir.role)
		}
		fi, err := os.Stat(dir.path)
		if err != nil {
			return fmt.Errorf("%s %s is not readable — in Docker it must be a "+
				"mounted volume: %w", dir.role, dir.path, err)
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s %s is not a directory", dir.role, dir.path)
		}
	}

	src, err := os.CreateTemp(srcDir, ".gamarr-linkcheck-*")
	if err != nil {
		return fmt.Errorf("cannot write to the download directory %s: %w", srcDir, err)
	}
	srcPath := src.Name()
	src.Close()
	defer os.Remove(srcPath)

	// The suffix keeps the link distinct from the probe file itself, so the
	// check still reports the truth when both paths are the same directory.
	destPath := filepath.Join(destDir, filepath.Base(srcPath)+".link")
	defer os.Remove(destPath)

	if err := os.Link(srcPath, destPath); err != nil {
		if isCrossDevice(err) {
			// EXDEV rather than the LinkError: the report already names both
			// directories, and the wrapped error would otherwise trail a
			// throwaway probe filename through the UI and the logs.
			return crossDeviceError(srcDir, destDir, syscall.EXDEV)
		}
		return fmt.Errorf("cannot hardlink into the library directory %s: %w", destDir, err)
	}
	return nil
}
