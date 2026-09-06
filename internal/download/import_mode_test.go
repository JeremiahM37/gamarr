package download

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"gamarr/internal/fileops"
	"gamarr/internal/qbit"
)

// newImportTest builds a manager whose torrent imports run in the given mode,
// plus a completed torrent sitting in the download directory.
func newImportTest(t *testing.T, mode fileops.Mode) (*Manager, *qbitMock, qbit.Torrent) {
	t.Helper()
	qm := newQbitMock(t)
	cfg := newTestConfig(t)
	cfg.QBURL = "configured"
	cfg.ImportMode = mode
	m := New(cfg, newTestJobs(t), qm.client())

	content := filepath.Join(cfg.QBSavePath, "Seeded Game")
	writeFileT(t, filepath.Join(content, "setup.exe"), []byte("PAYLOAD"))
	torrent := qbit.Torrent{
		Name: "Seeded Game", Hash: "seed-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: content,
	}
	return m, qm, torrent
}

func sameInode(t *testing.T, a, b string) bool {
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

// The issue this feature exists for: after a hardlink import the torrent's
// files are still where qBittorrent left them and the torrent is still in the
// client, so it keeps seeding.
func TestOrganizeGameHardlinkKeepsTorrentSeeding(t *testing.T) {
	m, qm, torrent := newImportTest(t, fileops.ModeHardlink)
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "title": torrent.Name})

	m.organizeGame(jobID, &torrent, "PC", "pc", true, 1)

	src := filepath.Join(torrent.ContentPath, "setup.exe")
	dest := filepath.Join(m.cfg.GamesVaultPath, "Seeded Game", "setup.exe")

	if _, err := os.Stat(src); err != nil {
		t.Fatalf("seeded file gone from the download dir: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("file not imported into the vault: %v", err)
	}
	if !sameInode(t, src, dest) {
		t.Error("import did not hardlink — library and download are separate copies")
	}
	if calls := qm.deleteCalls(); len(calls) != 0 {
		t.Errorf("torrent was removed from the client (%+v); it must be left seeding", calls)
	}

	job, ok := jobFromDB(t, m.Jobs(), jobID)
	if !ok {
		t.Fatal("job not persisted")
	}
	if got := job["status"]; got != "completed" {
		t.Errorf("status = %v, want completed", got)
	}
	if detail, _ := job["detail"].(string); !strings.Contains(detail, "Hardlinked") {
		t.Errorf("detail = %q, want it to say the content was hardlinked", detail)
	}
}

// The default is unchanged: a move import takes the torrent and its files
// with it, exactly as before import modes existed.
func TestOrganizeGameMoveDeletesTorrentWithFiles(t *testing.T) {
	m, qm, torrent := newImportTest(t, fileops.ModeMove)
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "title": torrent.Name})

	m.organizeGame(jobID, &torrent, "PC", "pc", true, 1)

	if _, err := os.Stat(torrent.ContentPath); err == nil {
		t.Error("move import left the source behind")
	}
	if _, err := os.Stat(filepath.Join(m.cfg.GamesVaultPath, "Seeded Game", "setup.exe")); err != nil {
		t.Fatalf("file not imported into the vault: %v", err)
	}
	calls := qm.deleteCalls()
	if len(calls) != 1 {
		t.Fatalf("delete calls = %+v, want exactly one", calls)
	}
	if calls[0].hash != "seed-hash" || !calls[0].deleteFiles {
		t.Errorf("delete call = %+v, want seed-hash with deleteFiles=true", calls[0])
	}
}

// REMOVE_TORRENT_AFTER_IMPORT still removes the torrent under a preserving
// mode, but must never delete the data — the library links point at it.
func TestOrganizeGamePreservingModeRemoveAfterImportKeepsFiles(t *testing.T) {
	for _, mode := range []fileops.Mode{fileops.ModeHardlink, fileops.ModeSymlink, fileops.ModeCopy} {
		t.Run(string(mode), func(t *testing.T) {
			m, qm, torrent := newImportTest(t, mode)
			m.cfg.RemoveAfterImport = true
			jobID := newJobID()
			m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "title": torrent.Name})

			m.organizeGame(jobID, &torrent, "PC", "pc", true, 1)

			calls := qm.deleteCalls()
			if len(calls) != 1 {
				t.Fatalf("delete calls = %+v, want exactly one", calls)
			}
			if calls[0].deleteFiles {
				t.Error("deleteFiles=true would destroy the data the library links to")
			}
			if _, err := os.Stat(filepath.Join(torrent.ContentPath, "setup.exe")); err != nil {
				t.Errorf("source file removed: %v", err)
			}
		})
	}
}

func TestOrganizeGameROMImportModes(t *testing.T) {
	tests := []struct {
		mode           fileops.Mode
		wantSrcSurvive bool
		wantDeletes    int
	}{
		{fileops.ModeMove, false, 1},
		{fileops.ModeHardlink, true, 0},
		{fileops.ModeSymlink, true, 0},
		{fileops.ModeCopy, true, 0},
	}
	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			m, qm, torrent := newImportTest(t, tt.mode)
			jobID := newJobID()
			m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "title": torrent.Name})

			m.organizeGame(jobID, &torrent, "SNES", "snes", false, 1)

			dest := filepath.Join(m.cfg.GamesRomsPath, "snes", "Seeded Game", "setup.exe")
			if _, err := os.Stat(dest); err != nil {
				t.Fatalf("ROM not imported: %v", err)
			}
			_, err := os.Stat(filepath.Join(torrent.ContentPath, "setup.exe"))
			if survived := err == nil; survived != tt.wantSrcSurvive {
				t.Errorf("source survived = %v, want %v", survived, tt.wantSrcSurvive)
			}
			if got := len(qm.deleteCalls()); got != tt.wantDeletes {
				t.Errorf("delete calls = %d, want %d", got, tt.wantDeletes)
			}
		})
	}
}

// A cross-device hardlink import fails loudly instead of silently moving the
// seeded data out from under the torrent.
func TestOrganizeGameHardlinkCrossDeviceFailsTheJob(t *testing.T) {
	m, qm, torrent := newImportTest(t, fileops.ModeHardlink)

	// Point the vault at a different filesystem.
	other, err := os.MkdirTemp("/dev/shm", "gamarr-vault-")
	if err != nil {
		t.Skipf("no second filesystem available: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(other) })
	if sameFilesystem(t, m.cfg.QBSavePath, other) {
		t.Skip("temp dirs are on the same filesystem")
	}
	m.cfg.GamesVaultPath = other

	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "title": torrent.Name})
	m.organizeGame(jobID, &torrent, "PC", "pc", true, 1)

	job, ok := jobFromDB(t, m.Jobs(), jobID)
	if !ok {
		t.Fatal("job not persisted")
	}
	if got := job["status"]; got != "error" {
		t.Errorf("status = %v, want error", got)
	}
	if msg, _ := job["error"].(string); !strings.Contains(msg, "same filesystem") {
		t.Errorf("error = %q, want it to explain the filesystem requirement", msg)
	}
	if _, err := os.Stat(filepath.Join(torrent.ContentPath, "setup.exe")); err != nil {
		t.Errorf("failed import must not touch the seeded source: %v", err)
	}
	if calls := qm.deleteCalls(); len(calls) != 0 {
		t.Errorf("failed import must not remove the torrent, got %+v", calls)
	}
}

func sameFilesystem(t *testing.T, a, b string) bool {
	t.Helper()
	var sa, sb syscall.Stat_t
	if err := syscall.Stat(a, &sa); err != nil {
		t.Fatalf("stat %s: %v", a, err)
	}
	if err := syscall.Stat(b, &sb); err != nil {
		t.Fatalf("stat %s: %v", b, err)
	}
	return sa.Dev == sb.Dev
}

// ── mode resolution ──────────────────────────────────────────────────────────

func TestImportOptionsSettingsOverrideEnvDefault(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.ImportMode = fileops.ModeMove
	m := New(cfg, newTestJobs(t), nil)

	if got := m.importOptions().Mode; got != fileops.ModeMove {
		t.Errorf("mode without a stored setting = %q, want move", got)
	}

	m.SaveSettings(&Settings{ImportMode: string(fileops.ModeHardlink)})
	if got := m.importOptions().Mode; got != fileops.ModeHardlink {
		t.Errorf("mode after the UI saved hardlink = %q, want hardlink", got)
	}
}

func TestImportOptionsIgnoresGarbageInSettingsFile(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.ImportMode = fileops.ModeCopy
	m := New(cfg, newTestJobs(t), nil)

	writeFileT(t, filepath.Join(cfg.DataDir, "settings.json"), []byte(`{"import_mode":"teleport"}`))

	if got := m.importOptions().Mode; got != fileops.ModeCopy {
		t.Errorf("mode = %q, want the configured default (copy) when the stored value is invalid", got)
	}
}

func TestImportOptionsDefaultsToMoveOnZeroConfig(t *testing.T) {
	cfg := newTestConfig(t) // ImportMode left at its zero value
	m := New(cfg, newTestJobs(t), nil)
	if got := m.importOptions().Mode; got != fileops.ModeMove {
		t.Errorf("mode = %q, want move", got)
	}
}

func TestImportOptionsCarriesHardlinkFallback(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.ImportMode = fileops.ModeHardlink
	cfg.ImportHardlinkFallback = fileops.FallbackCopy
	m := New(cfg, newTestJobs(t), nil)

	if got := m.importOptions().HardlinkFallback; got != fileops.FallbackCopy {
		t.Errorf("fallback = %q, want copy", got)
	}
}

// The API reports the mode imports will actually use, including for settings
// files written before the option existed.
func TestLoadSettingsReportsEffectiveImportMode(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.ImportMode = fileops.ModeHardlink
	m := New(cfg, newTestJobs(t), nil)

	if got := m.LoadSettings().ImportMode; got != string(fileops.ModeHardlink) {
		t.Errorf("import mode with no settings file = %q, want hardlink", got)
	}

	// A file from an older version carries no import_mode at all.
	writeFileT(t, filepath.Join(cfg.DataDir, "settings.json"), []byte(`{"extract_archives":true}`))
	s := m.LoadSettings()
	if s.ImportMode != string(fileops.ModeHardlink) {
		t.Errorf("import mode from a legacy settings file = %q, want hardlink", s.ImportMode)
	}
	if !s.ExtractArchives {
		t.Error("extract_archives lost while filling in the import mode")
	}
}

func TestSaveSettingsRoundTripsImportMode(t *testing.T) {
	cfg := newTestConfig(t)
	m := New(cfg, newTestJobs(t), nil)
	m.SaveSettings(&Settings{ExtractArchives: true, ImportMode: string(fileops.ModeSymlink)})

	raw, err := os.ReadFile(filepath.Join(cfg.DataDir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var stored map[string]interface{}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if stored["import_mode"] != "symlink" {
		t.Errorf("stored import_mode = %v, want symlink", stored["import_mode"])
	}
	if got := m.LoadSettings().ImportMode; got != "symlink" {
		t.Errorf("reloaded import mode = %q, want symlink", got)
	}
}

func TestImportDetail(t *testing.T) {
	tests := map[fileops.Mode]string{
		fileops.ModeMove:     "Moved to GameVault",
		fileops.ModeHardlink: "Hardlinked to GameVault",
		fileops.ModeSymlink:  "Symlinked to GameVault",
		fileops.ModeCopy:     "Copied to GameVault",
	}
	for mode, want := range tests {
		if got := importDetail(mode, "GameVault"); got != want {
			t.Errorf("importDetail(%q) = %q, want %q", mode, got, want)
		}
	}
}

// Content Gamarr downloaded itself has no swarm to seed, so it always moves —
// leaving a staging copy behind would just leak disk.
func TestDDLImportAlwaysMoves(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.ImportMode = fileops.ModeHardlink
	m := New(cfg, newTestJobs(t), nil)

	staging := filepath.Join(cfg.QBSavePath, "downloaded.gb")
	writeFileT(t, staging, []byte("ROM"))
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "title": "Downloaded"})

	m.organizeDDLFile(jobID, staging, "Downloaded", "Game Boy", "gb", false)

	if _, err := os.Stat(staging); err == nil {
		t.Error("DDL staging file survived the import")
	}
	if _, err := os.Stat(filepath.Join(cfg.GamesRomsPath, "gb", "downloaded.gb")); err != nil {
		t.Errorf("DDL file not imported: %v", err)
	}
}
