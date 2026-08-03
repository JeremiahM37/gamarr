package config

import (
	"os"
	"testing"

	"gamarr/internal/fileops"
)

func TestLoadImportMode(t *testing.T) {
	tests := []struct {
		name         string
		mode         string
		fallback     string
		wantMode     fileops.Mode
		wantFallback fileops.Fallback
	}{
		{"unset keeps the previous behavior", "", "", fileops.ModeMove, fileops.FallbackError},
		{"hardlink", "hardlink", "", fileops.ModeHardlink, fileops.FallbackError},
		{"symlink", "symlink", "", fileops.ModeSymlink, fileops.FallbackError},
		{"copy", "copy", "", fileops.ModeCopy, fileops.FallbackError},
		{"uppercase is accepted", "HARDLINK", "", fileops.ModeHardlink, fileops.FallbackError},
		{"surrounding space is trimmed", " hardlink ", "", fileops.ModeHardlink, fileops.FallbackError},
		{"explicit fallback", "hardlink", "copy", fileops.ModeHardlink, fileops.FallbackCopy},
		{"a typo falls back to the default", "hardlnk", "", fileops.ModeMove, fileops.FallbackError},
		{"a bad fallback falls back to error", "hardlink", "shrug", fileops.ModeHardlink, fileops.FallbackError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("IMPORT_MODE", tt.mode)
			t.Setenv("IMPORT_HARDLINK_FALLBACK", tt.fallback)

			cfg := Load()

			if cfg.ImportMode != tt.wantMode {
				t.Errorf("ImportMode = %q, want %q", cfg.ImportMode, tt.wantMode)
			}
			if cfg.ImportHardlinkFallback != tt.wantFallback {
				t.Errorf("ImportHardlinkFallback = %q, want %q", cfg.ImportHardlinkFallback, tt.wantFallback)
			}
		})
	}
}

func TestLoadImportModeDefaultsWithNoEnv(t *testing.T) {
	os.Unsetenv("IMPORT_MODE")
	os.Unsetenv("IMPORT_HARDLINK_FALLBACK")

	cfg := Load()

	if cfg.ImportMode != fileops.ModeMove {
		t.Errorf("ImportMode = %q, want move", cfg.ImportMode)
	}
	if cfg.ImportMode.PreservesSource() {
		t.Error("the default must not silently start preserving sources")
	}
}
