package api

import (
	"os"
	"strings"
	"testing"

	"gamarr/internal/config"
	"gamarr/internal/fileops"
)

// checksFrom pulls the checks array out of an /api/settings/import-check body.
func checksFrom(t *testing.T, body map[string]interface{}) []map[string]interface{} {
	t.Helper()
	raw, ok := body["checks"].([]interface{})
	if !ok {
		t.Fatalf("checks missing or not an array: %v", body)
	}
	out := make([]map[string]interface{}, 0, len(raw))
	for _, c := range raw {
		m, ok := c.(map[string]interface{})
		if !ok {
			t.Fatalf("check is not an object: %v", c)
		}
		out = append(out, m)
	}
	return out
}

// Only hardlink imports can hit a filesystem boundary, so the other modes have
// nothing to preflight and must not pay for a probe.
func TestImportCheckSkipsNonHardlinkModes(t *testing.T) {
	env := newTestEnv(t, func(c *config.Config) {
		c.ImportMode = fileops.ModeMove
		c.QBSavePath, c.GamesVaultPath = t.TempDir(), t.TempDir()
	})

	rr := env.do("GET", "/api/settings/import-check", "")
	wantStatus(t, rr, 200)
	body := decodeMap(t, rr)

	if body["import_mode"] != "move" {
		t.Errorf("import_mode = %v, want move", body["import_mode"])
	}
	if body["applies"] != false {
		t.Errorf("applies = %v, want false", body["applies"])
	}
	if got := checksFrom(t, body); len(got) != 0 {
		t.Errorf("checks = %v, want none", got)
	}
}

func TestImportCheckPassesOnOneFilesystem(t *testing.T) {
	env := newTestEnv(t, func(c *config.Config) {
		c.ImportMode = fileops.ModeHardlink
		c.QBSavePath = t.TempDir()
		c.GamesVaultPath = t.TempDir()
		c.GamesRomsPath = t.TempDir()
	})

	rr := env.do("GET", "/api/settings/import-check", "")
	wantStatus(t, rr, 200)
	body := decodeMap(t, rr)

	if body["applies"] != true {
		t.Errorf("applies = %v, want true", body["applies"])
	}
	checks := checksFrom(t, body)
	if len(checks) != 2 {
		t.Fatalf("got %d checks, want one per library directory", len(checks))
	}
	for _, c := range checks {
		if c["ok"] != true {
			t.Errorf("check %v failed unexpectedly", c)
		}
		if c["source"] != env.cfg.QBSavePath {
			t.Errorf("source = %v, want %s", c["source"], env.cfg.QBSavePath)
		}
	}
	if checks[0]["target"] != "library" || checks[1]["target"] != "roms" {
		t.Errorf("targets = %v, %v", checks[0]["target"], checks[1]["target"])
	}
}

// The real report: a layout that cannot hardlink says so, names both paths,
// and explains the Docker cause.
func TestImportCheckReportsCrossDeviceLayout(t *testing.T) {
	other, err := os.MkdirTemp("/dev/shm", "gamarr-api-xdev-")
	if err != nil {
		t.Skipf("no second filesystem available: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(other) })

	env := newTestEnv(t, func(c *config.Config) {
		c.ImportMode = fileops.ModeHardlink
		c.QBSavePath = t.TempDir()
		c.GamesVaultPath = other
	})
	if err := fileops.CheckHardlink(env.cfg.QBSavePath, other); err == nil {
		t.Skip("temp dirs are on the same filesystem")
	}

	rr := env.do("GET", "/api/settings/import-check", "")
	wantStatus(t, rr, 200)
	checks := checksFrom(t, decodeMap(t, rr))

	if len(checks) != 1 {
		t.Fatalf("got %d checks, want 1", len(checks))
	}
	if checks[0]["ok"] != false {
		t.Fatal("check passed, want the cross-device failure")
	}
	msg, _ := checks[0]["error"].(string)
	for _, want := range []string{env.cfg.QBSavePath, other, "bind mount"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

// A download directory missing from the container is reported here rather than
// as a mystery import failure later.
func TestImportCheckReportsUnmountedDownloadDir(t *testing.T) {
	env := newTestEnv(t, func(c *config.Config) {
		c.ImportMode = fileops.ModeHardlink
		c.QBSavePath = "/definitely/not/mounted"
		c.GamesVaultPath = t.TempDir()
	})

	rr := env.do("GET", "/api/settings/import-check", "")
	wantStatus(t, rr, 200)
	checks := checksFrom(t, decodeMap(t, rr))

	if len(checks) != 1 || checks[0]["ok"] != false {
		t.Fatalf("want one failing check, got %v", checks)
	}
	if msg, _ := checks[0]["error"].(string); !strings.Contains(msg, "mounted volume") {
		t.Errorf("error %q should point at the missing mount", msg)
	}
}
