package download

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gamarr/internal/fileops"
	"gamarr/internal/qbit"
)

// tarEntries returns a vault archive's contents keyed by entry name. Asserting
// the archive merely exists cannot tell "archived the download" from "archived
// nothing", and on the usenet path the second one deletes the download.
func tarEntries(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	out := map[string]string{}
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar %s: %v", path, err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read entry %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = string(body)
	}
	return out
}

// irregularTree builds a source dir whose first entry archives cleanly and
// whose second fails, so Archive errors after bytes have been written.
func irregularTree(t *testing.T, dir string) {
	t.Helper()
	writeFileT(t, filepath.Join(dir, "a-payload.bin"), []byte(strings.Repeat("PAYLOAD!", 8192)))
	if err := os.Symlink(filepath.Join(dir, "a-payload.bin"), filepath.Join(dir, "zz-link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
}

// GameVault indexes one file per game, so a repack folder has to reach the
// vault as a single tar rather than as the folder the torrent named.
func TestOrganizeGameArchivesVaultFolder(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = true
	cfg.ImportMode = fileops.ModeCopy
	jobs := newTestJobs(t)

	content := filepath.Join(t.TempDir(), "007 First Light (2025)")
	writeFileT(t, filepath.Join(content, "setup.exe"), []byte("installer"))
	writeFileT(t, filepath.Join(content, "fg-01.bin"), []byte("payload"))

	qm := newQbitMock(t)
	m := New(cfg, jobs, qm.client())
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

	torrent := &qbit.Torrent{Name: "007 First Light (2025)", Hash: "h1", ContentPath: content}
	m.organizeGame(jobID, torrent, "PC", "", true)

	dest := filepath.Join(cfg.GamesVaultPath, "007 First Light (2025).tar")
	got := tarEntries(t, dest)
	if got["setup.exe"] != "installer" || got["fg-01.bin"] != "payload" {
		t.Errorf("archive contents = %v, want both source files", got)
	}
	if pathExists(filepath.Join(cfg.GamesVaultPath, "007 First Light (2025)")) {
		t.Error("the folder was written to the vault as well as the archive")
	}
	if !m.Jobs().LibraryHasSourceID("torrent:h1") {
		t.Error("archive was not tracked in the library")
	}

	// Writing the archive never touches the source, and under a preserving mode
	// nothing downstream takes it either.
	if !pathExists(filepath.Join(content, "fg-01.bin")) {
		t.Error("archiving destroyed the source")
	}
	for _, call := range qm.deleteCalls() {
		if call.deleteFiles {
			t.Errorf("torrent %s was deleted with its data after an archive import", call.hash)
		}
	}
}

func TestOrganizeNZBArchivesVaultFolder(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = true
	m := New(cfg, newTestJobs(t), nil)
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

	storage := filepath.Join(t.TempDir(), "PC Game")
	writeFileT(t, filepath.Join(storage, "setup.exe"), []byte("installer"))
	writeFileT(t, filepath.Join(storage, "data", "fg-01.bin"), []byte("payload"))

	m.organizeNZBDownload(jobID, storage, "PC Game", "PC", "", true)

	// The whole string, not a substring: the archive branch leaves the download
	// in place, so claiming a move is wrong and only the verb says which
	// happened.
	job, _ := m.Jobs().Get(jobID)
	if detail, _ := job["detail"].(string); detail != "Copied to GameVault" {
		t.Errorf("detail = %q, want %q", detail, "Copied to GameVault")
	}

	// Read the archive rather than assert it exists: that is the only assertion
	// separating a real import from one that archived nothing.
	got := tarEntries(t, filepath.Join(cfg.GamesVaultPath, "PC Game.tar"))
	if got["setup.exe"] != "installer" || got["data/fg-01.bin"] != "payload" {
		t.Errorf("archive contents = %v, want both source files", got)
	}
	// A write to the vault reaches the mount's cache, not the remote behind it,
	// so nothing here can tell whether the archive is safe. The download copy
	// stays for a layer that can read the remote to release.
	if !pathExists(filepath.Join(storage, "setup.exe")) {
		t.Error("the download copy was removed on the strength of a local write")
	}
}

// libraryPathFor returns the FilePath the library recorded for a source ID.
func libraryPathFor(t *testing.T, m *Manager, sourceID string) string {
	t.Helper()
	for _, item := range m.Jobs().RecentLibraryItems(50) {
		if item.SourceID == sourceID {
			return item.FilePath
		}
	}
	t.Fatalf("no library row for %q", sourceID)
	return ""
}

// A restart can lose the job update after the archive landed. Re-running
// organize has to finish the job: otherwise every retry reports the same
// failure and the only way out is deleting the archive by hand.
func TestOrganizeGameCompletesWhenTheArchiveIsAlreadyThere(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = true
	jobs := newTestJobs(t)

	content := filepath.Join(t.TempDir(), "Recovered Game")
	writeFileT(t, filepath.Join(content, "setup.exe"), []byte("installer"))
	writeFileT(t, filepath.Join(content, "fg-01.bin"), []byte(strings.Repeat("payload", 512)))

	// A real archive of this content, not a stand-in: the accept has to be
	// distinguishable from any file happening to sit at that name.
	staged := filepath.Join(t.TempDir(), "staged.tar")
	if err := fileops.Archive(content, staged, nil); err != nil {
		t.Fatalf("build fixture archive: %v", err)
	}
	published := filepath.Join(cfg.GamesVaultPath, "Recovered Game.tar")
	if err := os.Rename(staged, published); err != nil {
		t.Fatalf("place fixture archive: %v", err)
	}

	qm := newQbitMock(t)
	m := New(cfg, jobs, qm.client())
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

	m.organizeGame(jobID, &qbit.Torrent{Name: "Recovered Game", Hash: "h9", ContentPath: content}, "PC", "", true)

	job, _ := jobs.Get(jobID)
	if status, _ := job["status"].(string); status != "completed" {
		t.Errorf("status = %q, want completed (job=%v)", status, job)
	}
	// The row has to point at something that exists. A path nothing wrote reads
	// as a stored game to whatever releases the download copy.
	recorded := libraryPathFor(t, m, "torrent:h9")
	if _, err := os.Stat(recorded); err != nil {
		t.Errorf("library row FilePath %q does not exist: %v", recorded, err)
	}
	if recorded != published {
		t.Errorf("library row FilePath = %q, want the archive at %q", recorded, published)
	}
	for _, call := range qm.deleteCalls() {
		if call.deleteFiles {
			t.Errorf("torrent %s was deleted with its data", call.hash)
		}
	}
}

// The bug this fixes: an archive import reported a copy whatever IMPORT_MODE
// said, so a user who asked for move kept the torrent and its files forever. An
// archive holds the whole download, so move means move.
func TestArchiveImportHonoursMoveMode(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = true
	cfg.ImportMode = fileops.ModeMove

	content := filepath.Join(t.TempDir(), "Moved Game")
	writeFileT(t, filepath.Join(content, "setup.exe"), []byte("installer"))
	writeFileT(t, filepath.Join(content, "fg-01.bin"), []byte(strings.Repeat("payload", 512)))

	qm := newQbitMock(t)
	m := New(cfg, newTestJobs(t), qm.client())
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

	m.organizeGame(jobID, &qbit.Torrent{Name: "Moved Game", Hash: "mv1", ContentPath: content}, "PC", "", true)

	if got := tarEntries(t, filepath.Join(cfg.GamesVaultPath, "Moved Game.tar")); got["setup.exe"] != "installer" {
		t.Fatalf("archive contents = %v, want the source files", got)
	}
	calls := qm.deleteCalls()
	if len(calls) != 1 {
		t.Fatalf("delete calls = %+v, want exactly one", calls)
	}
	if calls[0].hash != "mv1" || !calls[0].deleteFiles {
		t.Errorf("delete call = %+v, want mv1 with deleteFiles=true", calls[0])
	}
}

// An archive is written rather than linked, so the job feed has to say copied
// whatever preserving mode is configured. Reporting "Hardlinked" for a tar with
// nothing linked anywhere is the claim importDetail exists to prevent.
func TestArchiveImportReportsACopyUnderPreservingModes(t *testing.T) {
	for _, mode := range []fileops.Mode{fileops.ModeHardlink, fileops.ModeSymlink, fileops.ModeCopy} {
		t.Run(string(mode), func(t *testing.T) {
			cfg := newTestConfig(t)
			cfg.VaultArchiveEnabled = true
			cfg.ImportMode = mode
			m := New(cfg, newTestJobs(t), newQbitMock(t).client())

			content := filepath.Join(t.TempDir(), "Verb Game")
			writeFileT(t, filepath.Join(content, "setup.exe"), []byte("installer"))
			jobID := newJobID()
			m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

			m.organizeGame(jobID, &qbit.Torrent{Name: "Verb Game", Hash: "vb1", ContentPath: content}, "PC", "", true)

			job, _ := m.Jobs().Get(jobID)
			if detail, _ := job["detail"].(string); detail != "Copied to GameVault" {
				t.Errorf("detail = %q, want %q", detail, "Copied to GameVault")
			}
		})
	}
}

// The archive is the only thing that authorises dropping the download, so a
// verify that fails has to keep it even under move.
func TestArchiveImportKeepsTheDownloadWhenVerifyFails(t *testing.T) {
	verifyArchive = func(string, string, fileops.WantedFiles) error {
		return errors.New("cannot read the published archive")
	}
	t.Cleanup(func() { verifyArchive = fileops.VerifyArchive })

	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = true
	cfg.ImportMode = fileops.ModeMove
	qm := newQbitMock(t)
	m := New(cfg, newTestJobs(t), qm.client())

	content := filepath.Join(t.TempDir(), "Unverified Game")
	writeFileT(t, filepath.Join(content, "setup.exe"), []byte("installer"))
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

	m.organizeGame(jobID, &qbit.Torrent{Name: "Unverified Game", Hash: "uv1", ContentPath: content}, "PC", "", true)

	for _, call := range qm.deleteCalls() {
		if call.deleteFiles {
			t.Errorf("torrent %s was deleted with its data on an archive that failed verification", call.hash)
		}
	}
	if !pathExists(filepath.Join(content, "setup.exe")) {
		t.Error("the download was dropped on an archive that failed verification")
	}
}

// The state a library is in after its first run: the vault already holds an
// archive, so a re-grab lands on the accept rather than on a fresh write. The
// occupant is only known to be large enough to be this game, which cannot tell
// this build from another, so a newer download must not be dropped against an
// older build sitting at the same name.
func TestArchiveAlreadyInVaultKeepsTheDownload(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = true
	cfg.ImportMode = fileops.ModeMove

	content := filepath.Join(t.TempDir(), "Stored Game")
	writeFileT(t, filepath.Join(content, "setup.exe"), []byte("installer"))
	writeFileT(t, filepath.Join(content, "fg-01.bin"), []byte(strings.Repeat("payload", 512)))

	// A real archive of this content, so the accept rests on the census rather
	// than on something merely sitting at the name.
	staged := filepath.Join(t.TempDir(), "staged.tar")
	if err := fileops.Archive(content, staged, nil); err != nil {
		t.Fatalf("build fixture archive: %v", err)
	}
	if err := os.Rename(staged, filepath.Join(cfg.GamesVaultPath, "Stored Game.tar")); err != nil {
		t.Fatalf("place fixture archive: %v", err)
	}

	qm := newQbitMock(t)
	m := New(cfg, newTestJobs(t), qm.client())
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

	m.organizeGame(jobID, &qbit.Torrent{Name: "Stored Game", Hash: "st1", ContentPath: content}, "PC", "", true)

	for _, call := range qm.deleteCalls() {
		if call.deleteFiles {
			t.Errorf("torrent %s was deleted with its data against an archive this import did not write", call.hash)
		}
	}
	if !pathExists(filepath.Join(content, "fg-01.bin")) {
		t.Error("the download was dropped against an archive this import did not write")
	}
}

// The stored setting outranks VAULT_ARCHIVE_ENABLED, and a settings file
// written before the option existed leaves the environment default alone.
// Without that last part, upgrading turns archiving off for every install that
// set the variable.
func TestVaultArchiveSettingOverridesEnvDefault(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = true
	m := New(cfg, newTestJobs(t), nil)

	if !m.vaultArchiveEnabled() {
		t.Error("with nothing stored, the environment default should apply")
	}

	writeFileT(t, filepath.Join(cfg.DataDir, "settings.json"), []byte(`{"import_mode":"copy"}`))
	if !m.vaultArchiveEnabled() {
		t.Error("a settings file without the key turned archiving off")
	}
	// The settings API serialises this, so an unresolved value renders the
	// checkbox unchecked while archiving is actually on.
	if v := m.LoadSettings().VaultArchiveEnabled; v == nil || !*v {
		t.Errorf("LoadSettings reported %v for a legacy settings file, want the effective value", v)
	}

	off := false
	m.SaveSettings(&Settings{VaultArchiveEnabled: &off})
	if m.vaultArchiveEnabled() {
		t.Error("a stored false should outrank the environment default")
	}
}

// The setting has to reach the import, not just the settings API.
func TestVaultArchiveSettingDrivesTheImport(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = true
	cfg.ImportMode = fileops.ModeCopy
	m := New(cfg, newTestJobs(t), newQbitMock(t).client())
	off := false
	m.SaveSettings(&Settings{VaultArchiveEnabled: &off})

	content := filepath.Join(t.TempDir(), "Setting Game")
	writeFileT(t, filepath.Join(content, "setup.exe"), []byte("installer"))
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

	m.organizeGame(jobID, &qbit.Torrent{Name: "Setting Game", Hash: "vs1", ContentPath: content}, "PC", "", true)

	if pathExists(filepath.Join(cfg.GamesVaultPath, "Setting Game.tar")) {
		t.Error("archived despite the stored setting being off")
	}
	if !pathExists(filepath.Join(cfg.GamesVaultPath, "Setting Game", "setup.exe")) {
		t.Error("not imported as a folder with archiving off")
	}
}

// The usenet path reads the setting through the same accessor, and it is a
// separate call site: a torrent-path test cannot tell whether this one was
// converted.
func TestVaultArchiveSettingDrivesTheNZBImport(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = true
	m := New(cfg, newTestJobs(t), nil)
	off := false
	m.SaveSettings(&Settings{VaultArchiveEnabled: &off})

	storage := filepath.Join(t.TempDir(), "NZB Game")
	writeFileT(t, filepath.Join(storage, "setup.exe"), []byte("installer"))
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

	m.organizeNZBDownload(jobID, storage, "NZB Game", "PC", "", true)

	if pathExists(filepath.Join(cfg.GamesVaultPath, "NZB Game.tar")) {
		t.Error("archived despite the stored setting being off")
	}
	if !pathExists(filepath.Join(cfg.GamesVaultPath, "NZB Game", "setup.exe")) {
		t.Error("not imported as a folder with archiving off")
	}
}

// The accepted path has to be the occupant, not the name this import would have
// written, and those two only differ when the vault holds the un-suffixed name.
// An earlier single-file import of the same game lands exactly there, so a
// folder-shaped release arriving later separates them.
func TestOrganizeGameReportsTheOccupantNotTheArchiveName(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = true
	jobs := newTestJobs(t)

	content := filepath.Join(t.TempDir(), "Game")
	writeFileT(t, filepath.Join(content, "setup.exe"), []byte("installer"))

	// A regular file at the un-suffixed name, large enough to pass as this game.
	occupant := filepath.Join(cfg.GamesVaultPath, "Game")
	writeFileT(t, occupant, []byte(strings.Repeat("earlier single-file import", 64)))

	qm := newQbitMock(t)
	m := New(cfg, jobs, qm.client())
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

	m.organizeGame(jobID, &qbit.Torrent{Name: "Game", Hash: "h7", ContentPath: content}, "PC", "", true)

	job, _ := jobs.Get(jobID)
	if status, _ := job["status"].(string); status != "completed" {
		t.Fatalf("status = %q, want completed (job=%v)", status, job)
	}
	recorded := libraryPathFor(t, m, "torrent:h7")
	if recorded != occupant {
		t.Errorf("library row FilePath = %q, want the occupant %q", recorded, occupant)
	}
	if _, err := os.Stat(recorded); err != nil {
		t.Errorf("library row FilePath %q does not exist: %v", recorded, err)
	}
	if pathExists(filepath.Join(cfg.GamesVaultPath, "Game.tar")) {
		t.Error("reported a .tar the import never wrote")
	}
}

// An occupant that cannot be an archive of this content is not this import.
// Accepting one reports a game as stored that was never stored, and the library
// row is what a release decision is read from.
func TestOrganizeGameRefusesAnOccupantThatIsNotTheArchive(t *testing.T) {
	cases := map[string]func(t *testing.T, vault string) string{
		"a file too small to hold the source": func(t *testing.T, vault string) string {
			p := filepath.Join(vault, "Game.tar")
			writeFileT(t, p, []byte("not really an archive"))
			return p
		},
		"a folder from an earlier unarchived import": func(t *testing.T, vault string) string {
			p := filepath.Join(vault, "Game")
			writeFileT(t, filepath.Join(p, "setup.exe"), []byte("old"))
			return p
		},
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := newTestConfig(t)
			cfg.VaultArchiveEnabled = true
			jobs := newTestJobs(t)
			seed(t, cfg.GamesVaultPath)

			content := filepath.Join(t.TempDir(), "Game")
			writeFileT(t, filepath.Join(content, "setup.exe"), []byte(strings.Repeat("installer", 4096)))

			qm := newQbitMock(t)
			m := New(cfg, jobs, qm.client())
			jobID := newJobID()
			jobs.Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

			m.organizeGame(jobID, &qbit.Torrent{Name: "Game", Hash: "hx", ContentPath: content}, "PC", "", true)

			job, _ := jobs.Get(jobID)
			if status, _ := job["status"].(string); status != "error" {
				t.Errorf("status = %q, want error (job=%v)", status, job)
			}
			if m.Jobs().LibraryHasSourceID("torrent:hx") {
				t.Error("filed a library row for content that was never stored")
			}
		})
	}
}

// Occupancy has to hold in both flag states. With the flag off the plain import
// path used to write the folder beside an existing archive, storing one game
// twice at full size under a single title.
func TestOrganizeGameDoesNotStoreTwiceWithArchiveOff(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = false
	jobs := newTestJobs(t)

	content := filepath.Join(t.TempDir(), "Game")
	writeFileT(t, filepath.Join(content, "setup.exe"), []byte("installer"))
	writeFileT(t, filepath.Join(cfg.GamesVaultPath, "Game.tar"), []byte(strings.Repeat("archived earlier", 64)))

	qm := newQbitMock(t)
	m := New(cfg, jobs, qm.client())
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

	m.organizeGame(jobID, &qbit.Torrent{Name: "Game", Hash: "h8", ContentPath: content}, "PC", "", true)

	if pathExists(filepath.Join(cfg.GamesVaultPath, "Game")) {
		t.Error("stored the game a second time as a folder beside the archive")
	}
	entries, err := os.ReadDir(cfg.GamesVaultPath)
	if err != nil {
		t.Fatalf("read vault: %v", err)
	}
	var stored []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".gamarr.json") {
			stored = append(stored, e.Name())
		}
	}
	if len(stored) != 1 || stored[0] != "Game.tar" {
		t.Errorf("vault holds %v, want only the archive already there", stored)
	}
}

// Staging may only go once the whole download is in the tar. A failure partway
// through has to leave the download intact.
func TestOrganizeNZBKeepsStagingWhenArchiveFails(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = true
	m := New(cfg, newTestJobs(t), nil)
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

	storage := filepath.Join(t.TempDir(), "PC Game")
	irregularTree(t, storage)

	m.organizeNZBDownload(jobID, storage, "PC Game", "PC", "", true)

	job, _ := m.Jobs().Get(jobID)
	if status, _ := job["status"].(string); status != "error" {
		t.Errorf("status = %q, want error (job=%v)", status, job)
	}
	if !pathExists(filepath.Join(storage, "a-payload.bin")) {
		t.Error("a failed archive removed the staging copy")
	}
	if pathExists(filepath.Join(cfg.GamesVaultPath, "PC Game.tar")) {
		t.Error("a failed archive was published to the vault")
	}
}

// The usenet path takes the same occupancy decision as the torrent path. Without
// it, fileops.Archive refuses instead, which turns a lost job update into a
// permanent failure rather than a finished import.
func TestOrganizeNZBCompletesWhenTheArchiveIsAlreadyThere(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = true
	m := New(cfg, newTestJobs(t), nil)
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

	storage := filepath.Join(t.TempDir(), "PC Game")
	writeFileT(t, filepath.Join(storage, "setup.exe"), []byte("installer"))
	writeFileT(t, filepath.Join(storage, "fg-01.bin"), []byte(strings.Repeat("payload", 512)))

	staged := filepath.Join(t.TempDir(), "staged.tar")
	if err := fileops.Archive(storage, staged, nil); err != nil {
		t.Fatalf("build fixture archive: %v", err)
	}
	published := filepath.Join(cfg.GamesVaultPath, "PC Game.tar")
	if err := os.Rename(staged, published); err != nil {
		t.Fatalf("place fixture archive: %v", err)
	}

	m.organizeNZBDownload(jobID, storage, "PC Game", "PC", "", true)

	job, _ := m.Jobs().Get(jobID)
	if status, _ := job["status"].(string); status != "completed" {
		t.Errorf("status = %q, want completed (job=%v)", status, job)
	}
	if !m.Jobs().LibraryHasSourceID("nzb:" + published) {
		t.Error("the already-published archive was not tracked in the library")
	}
}

// The reject half of that same decision. An occupant that cannot be an archive
// of this download must not be reported as this download: a completed job plus a
// library row is exactly what a release decision is read from.
func TestOrganizeNZBRefusesAnOccupantThatIsNotTheArchive(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = true
	m := New(cfg, newTestJobs(t), nil)
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

	storage := filepath.Join(t.TempDir(), "PC Game")
	writeFileT(t, filepath.Join(storage, "setup.exe"), []byte(strings.Repeat("installer", 4096)))
	occupant := filepath.Join(cfg.GamesVaultPath, "PC Game.tar")
	writeFileT(t, occupant, []byte("tiny"))

	m.organizeNZBDownload(jobID, storage, "PC Game", "PC", "", true)

	job, _ := m.Jobs().Get(jobID)
	if status, _ := job["status"].(string); status != "error" {
		t.Errorf("status = %q, want error (job=%v)", status, job)
	}
	if m.Jobs().LibraryHasSourceID("nzb:" + occupant) {
		t.Error("filed a library row for content that was never stored")
	}
	if !pathExists(filepath.Join(storage, "setup.exe")) {
		t.Error("the download was not left in place")
	}
}

// The ROM library needs the raw file: RomM cannot read a tar and GameVault
// never sees that tree, so the option must not reach it.
func TestOrganizeNZBNeverArchivesROMs(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = true
	m := New(cfg, newTestJobs(t), nil)
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

	storage := filepath.Join(t.TempDir(), "Rom Game")
	writeFileT(t, filepath.Join(storage, "rom.sfc"), []byte("x"))

	m.organizeNZBDownload(jobID, storage, "Rom Game", "SNES", "snes", false)

	if !pathExists(filepath.Join(cfg.GamesRomsPath, "snes", "Rom Game", "rom.sfc")) {
		t.Error("ROM not imported as a raw file")
	}
	if pathExists(filepath.Join(cfg.GamesRomsPath, "snes", "Rom Game.tar")) {
		t.Error("a ROM was archived")
	}
}

// A restart can re-enter organize after the archive landed and staging went.
// That is a finished import, not a failure.
func TestOrganizeNZBCompletesFromExistingArchive(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = true
	m := New(cfg, newTestJobs(t), nil)
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

	storage := filepath.Join(t.TempDir(), "Recovered Game")
	dest := filepath.Join(cfg.GamesVaultPath, "Recovered Game.tar")
	writeFileT(t, dest, []byte("tar"))

	m.organizeNZBDownload(jobID, storage, "Recovered Game", "PC", "", true)

	job, _ := m.Jobs().Get(jobID)
	if status, _ := job["status"].(string); status != "completed" {
		t.Errorf("status = %q, want completed (job=%v)", status, job)
	}
	// An archive import is a copy on the recovery route too. Staging is gone
	// here, so the name is the only thing the mode can be derived from.
	if detail, _ := job["detail"].(string); detail != "Copied to GameVault" {
		t.Errorf("detail = %q, want %q", detail, "Copied to GameVault")
	}
	if !m.Jobs().LibraryHasSourceID("nzb:" + dest) {
		t.Error("existing archive was not tracked in the library")
	}
}

// The wanted set comes from the client's priorities at organize time, so a
// deselected pack is excluded whatever its name - including one that carries
// the client's partial suffix after a late deselection left real bytes in it.
func TestOrganizeGameArchivesOnlyPriorityWantedFiles(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = true
	jobs := newTestJobs(t)

	content := filepath.Join(t.TempDir(), "Filtered Game")
	writeFileT(t, filepath.Join(content, "setup.exe"), []byte("installer"))
	writeFileT(t, filepath.Join(content, "fg-01.bin"), []byte("payload"))
	writeFileT(t, filepath.Join(content, "fg-02.bin.!qB"), []byte("leftover fragment"))

	qm := newQbitMock(t)
	qm.setFiles([]qbit.TorrentFile{
		{Name: "Filtered Game/setup.exe", Size: int64(len("installer")), Priority: 1},
		{Name: "Filtered Game/fg-01.bin", Size: int64(len("payload")), Priority: 1},
		{Name: "Filtered Game/fg-02.bin.!qB", Size: int64(len("leftover fragment")), Priority: 0},
	})
	m := New(cfg, jobs, qm.client())
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

	torrent := &qbit.Torrent{Name: "Filtered Game", Hash: "pf1", ContentPath: content}
	m.organizeGame(jobID, torrent, "PC", "", true)

	got := tarEntries(t, filepath.Join(cfg.GamesVaultPath, "Filtered Game.tar"))
	if got["setup.exe"] != "installer" || got["fg-01.bin"] != "payload" {
		t.Errorf("archive contents = %v, want the wanted files", got)
	}
	if _, ok := got["fg-02.bin.!qB"]; ok {
		t.Error("the deselected fragment was archived despite priority 0")
	}

	// Under move the delete only fires after verify passed against the wanted
	// set - if the verify counted excluded files it would fail and degrade to
	// a keeping copy, so the delete call is the pin on that wiring.
	calls := qm.deleteCalls()
	if len(calls) != 1 || calls[0].hash != "pf1" || !calls[0].deleteFiles {
		t.Errorf("delete calls = %+v, want the torrent deleted with its files after a verified move", calls)
	}

	// The set is recorded in the sidecar for later occupancy and verify checks.
	sidecar, err := os.ReadFile(filepath.Join(cfg.GamesVaultPath, "Filtered Game.tar.gamarr.json"))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(sidecar, &meta); err != nil {
		t.Fatalf("parse sidecar: %v", err)
	}
	if _, ok := meta["wanted_files"]; !ok {
		t.Error("sidecar does not record the wanted set")
	}
	if _, ok := meta["wanted_bytes"]; !ok {
		t.Error("sidecar does not record the wanted byte total")
	}
}

// Crash-recovery face: a restart after publishing must complete the import,
// and the accept has to measure the wanted subset, not the whole tree - the
// deselected fragment on disk is not part of what the tar stands in for.
func TestOrganizeGameAcceptsAnOccupiedSubsetTar(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = true
	jobs := newTestJobs(t)

	content := filepath.Join(t.TempDir(), "Filtered Game")
	writeFileT(t, filepath.Join(content, "setup.exe"), []byte("installer"))
	writeFileT(t, filepath.Join(content, "fg-01.bin"), []byte("payload"))
	// A multi-megabyte deselected fragment: big enough that an occupancy check
	// against the WHOLE tree would refuse the published subset tar.
	fragment := make([]byte, 5*1024*1024)
	writeFileT(t, filepath.Join(content, "fg-02.bin.!qB"), fragment)

	// The tar of the wanted subset, already published by a crashed attempt.
	staged := filepath.Join(t.TempDir(), "staged")
	writeFileT(t, filepath.Join(staged, "setup.exe"), []byte("installer"))
	writeFileT(t, filepath.Join(staged, "fg-01.bin"), []byte("payload"))
	published := filepath.Join(cfg.GamesVaultPath, "Filtered Game.tar")
	if err := fileops.Archive(staged, published, nil); err != nil {
		t.Fatalf("build the crashed attempt's tar: %v", err)
	}

	qm := newQbitMock(t)
	qm.setFiles([]qbit.TorrentFile{
		{Name: "Filtered Game/setup.exe", Size: int64(len("installer")), Priority: 1},
		{Name: "Filtered Game/fg-01.bin", Size: int64(len("payload")), Priority: 1},
		{Name: "Filtered Game/fg-02.bin.!qB", Size: int64(len(fragment)), Priority: 0},
	})
	m := New(cfg, jobs, qm.client())
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

	torrent := &qbit.Torrent{Name: "Filtered Game", Hash: "occ1", ContentPath: content}
	m.organizeGame(jobID, torrent, "PC", "", true)

	job := waitJobStatus(t, jobs, jobID, "completed", minPollTimeout)
	if detail, _ := job["detail"].(string); detail != "Copied to GameVault" {
		t.Errorf("detail = %q, want the occupancy accept's copy", detail)
	}
}

// The recorded set describes the archive only: a plain folder import writes no
// tar, so its sidecar must not carry wanted paths that point at nothing.
func TestFolderImportSidecarRecordsNoWantedSet(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.VaultArchiveEnabled = false
	jobs := newTestJobs(t)

	content := filepath.Join(t.TempDir(), "Plain Game")
	writeFileT(t, filepath.Join(content, "setup.exe"), []byte("installer"))

	qm := newQbitMock(t)
	qm.setFiles([]qbit.TorrentFile{
		{Name: "Plain Game/setup.exe", Size: int64(len("installer")), Priority: 1},
	})
	m := New(cfg, jobs, qm.client())
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})

	torrent := &qbit.Torrent{Name: "Plain Game", Hash: "plain1", ContentPath: content}
	m.organizeGame(jobID, torrent, "PC", "", true)

	waitJobStatus(t, jobs, jobID, "completed", minPollTimeout)
	sidecar, err := os.ReadFile(filepath.Join(cfg.GamesVaultPath, "Plain Game", ".gamarr.json"))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if strings.Contains(string(sidecar), "wanted_files") {
		t.Error("a folder import's sidecar recorded an archive's wanted set")
	}
}
