package download

import (
	"path/filepath"
	"testing"

	"gamarr/internal/qbit"
)

func TestJobFileReadyArchiveMember(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	cfg.QBURL = qm.srv.URL
	m := New(cfg, jobs, qm.client())

	hash := "wii-hash"
	qm.setFiles([]qbit.TorrentFile{
		{Name: "Minerva_Myrient/Redump/Wii/Animal Crossing.zip", Priority: 1, Progress: 1.0, Index: 0},
		{Name: "Minerva_Myrient/Redump/Wii/Other.zip", Priority: 0, Progress: 0.5, Index: 1},
	})
	tor := qbit.Torrent{Name: "Wii", Hash: hash, Progress: 0.78}
	job := map[string]interface{}{"title": "Animal Crossing.zip"}
	if !m.jobFileReady(job, tor) {
		t.Fatal("selected archive file at 100% should be ready")
	}
	job = map[string]interface{}{"title": "Other.zip"}
	if m.jobFileReady(job, tor) {
		t.Fatal("deselected/incomplete file should not be ready")
	}

	// Whole-torrent progress reads 100% once the wanted subset is done, so a
	// matched file short of it is not ready on the strength of the torrent alone.
	qm.setFiles([]qbit.TorrentFile{
		{Name: "Minerva_Myrient/Redump/Wii/Animal Crossing.zip", Priority: 0, Progress: 0.4, Index: 0},
		{Name: "Minerva_Myrient/Redump/Wii/Other.zip", Priority: 1, Progress: 1.0, Index: 1},
	})
	tor = qbit.Torrent{Name: "Wii", Hash: hash, Progress: 1.0}
	if m.jobFileReady(map[string]interface{}{"title": "Animal Crossing.zip"}, tor) {
		t.Fatal("a matched file at 40% should not report ready on the strength of the torrent reading 100%")
	}
}

// A Prowlarr release name is not the name of any file inside the torrent, so
// matching it against the file list says nothing about whether the download
// finished.
func TestJobFileReadyTitleMatchingNoFile(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	cfg.QBURL = qm.srv.URL
	m := New(cfg, jobs, qm.client())

	hash := "repack-hash"
	name := "CONTROL Resonant [FitGirl Repack]"
	job := map[string]interface{}{
		"title":         "CONTROL Resonant: Deluxe Edition, v0.563.737.9 + 6 DLCs (with PS5 DLC Unlocker) [FitGirl Repack]",
		"whole_torrent": true,
	}

	qm.setFiles([]qbit.TorrentFile{
		{Name: name + "/setup.exe", Priority: 1, Progress: 1.0, Index: 0},
		{Name: name + "/fg-01.bin", Priority: 1, Progress: 0.4, Index: 1},
	})
	tor := qbit.Torrent{Name: name, Hash: hash, Progress: 0.7}
	if m.jobFileReady(job, tor) {
		t.Fatal("an unfinished torrent should not report ready: a title matching no file leaves the progress test as the only evidence")
	}

	qm.setFiles([]qbit.TorrentFile{
		{Name: name + "/setup.exe", Priority: 1, Progress: 1.0, Index: 0},
		{Name: name + "/fg-01.bin", Priority: 1, Progress: 1.0, Index: 1},
	})
	tor.Progress = 1.0
	if !m.jobFileReady(job, tor) {
		t.Fatal("a complete torrent should report ready even though the release-name title matches no file in it")
	}
}

// The stall this guards against: a release-name title matches no file in the
// torrent, so the job sat at "Downloading" while the client seeded a finished
// download.
func TestWatchGameTorrentImportsReleaseNameTitle(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	cfg.QBURL = qm.srv.URL
	cfg.FileListScanEnabled = false
	m := New(cfg, jobs, qm.client())

	hash := "repack-watch-hash"
	name := "CONTROL Resonant [FitGirl Repack]"
	contentRoot := filepath.Join(t.TempDir(), name)
	writeFileT(t, filepath.Join(contentRoot, "setup.exe"), []byte("exe"))
	writeFileT(t, filepath.Join(contentRoot, "fg-01.bin"), []byte("bin"))

	qm.setFiles([]qbit.TorrentFile{
		{Name: name + "/setup.exe", Priority: 1, Progress: 1.0, Index: 0},
		{Name: name + "/fg-01.bin", Priority: 1, Progress: 1.0, Index: 1},
	})
	tor := qbit.Torrent{Name: name, Hash: hash, Progress: 1.0, ContentPath: contentRoot}
	qm.setTorrents([]qbit.Torrent{tor})

	release := "CONTROL Resonant: Deluxe Edition, v0.563.737.9 + 6 DLCs (with PS5 DLC Unlocker) [FitGirl Repack]"
	jobs.Set("job-repack", map[string]interface{}{
		"status": "downloading", "title": release,
		"info_hash": hash, "platform": "PC", "platform_slug": "pc", "is_pc": true,
		"whole_torrent": true,
	})

	go m.watchGameTorrent("job-repack", hash, release, "PC", "pc", true)
	waitJobStatus(t, jobs, "job-repack", "completed", minPollTimeout)
}

// A miss on a shared archive magnet means the ROM is not in it: widening would
// land the whole tree as one library entry and let the torrent be dropped with
// its data.
func TestJobFileReadyArchiveMemberAbsentFromTorrent(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	cfg.QBURL = qm.srv.URL
	m := New(cfg, jobs, qm.client())

	hash := "gb-shared"
	contentRoot := filepath.Join(t.TempDir(), "Minerva_Myrient")
	writeFileT(t, filepath.Join(contentRoot, "No-Intro", "Nintendo - Game Boy", "Trip World (Europe).zip"), []byte("rom"))

	qm.setFiles([]qbit.TorrentFile{
		{Name: "Minerva_Myrient/No-Intro/Nintendo - Game Boy/Trip World (Europe).zip", Priority: 1, Progress: 1.0, Index: 0},
		{Name: "Minerva_Myrient/No-Intro/Nintendo - Game Boy/Other Game (Europe).zip", Priority: 1, Progress: 1.0, Index: 1},
	})
	tor := qbit.Torrent{Name: "Game Boy", Hash: hash, Progress: 1.0, ContentPath: contentRoot}

	job := map[string]interface{}{
		"status": "downloading", "title": "Absent Game (Europe).zip",
		"info_hash": hash, "platform": "Game Boy", "platform_slug": "gb",
		// Set rather than omitted: an archive magnet's job names a member, not the
		// download, so this job is the shape the guard has to keep unready.
		"whole_torrent": false,
	}
	jobs.Set("job-absent", job)

	if m.jobFileReady(job, tor) {
		t.Fatal("a job naming a ROM the archive does not hold should not report ready: the whole tree would be imported in its place")
	}
}

func TestJobCompletedFields(t *testing.T) {
	fields := jobCompleted("Moved to RomM (Game Boy)")
	if fields["status"] != "completed" || fields["detail"] != "Moved to RomM (Game Boy)" {
		t.Fatalf("unexpected fields: %#v", fields)
	}
}

func TestResolveImportContentPathArchiveMember(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	cfg.QBURL = qm.srv.URL
	m := New(cfg, jobs, qm.client())

	hash := "archive-hash"
	torrent := &qbit.Torrent{
		Name:        "Game Boy",
		Hash:        hash,
		ContentPath: "/data/torrents/console-incomplete/Minerva_Myrient",
	}
	jobID := "job-1"
	jobs.Set(jobID, map[string]interface{}{
		"title":     "Trip World (Europe).zip",
		"info_hash": hash,
	})
	qm.setFiles([]qbit.TorrentFile{
		{Name: "Minerva_Myrient/No-Intro/Nintendo - Game Boy/Trip World (Europe).zip", Index: 0},
		{Name: "Minerva_Myrient/No-Intro/Nintendo - Game Boy/Other.zip", Index: 1},
	})

	got := m.resolveImportContentPath(jobID, torrent)
	want := filepath.Join(torrent.ContentPath, "No-Intro", "Nintendo - Game Boy", "Trip World (Europe).zip")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTorrentFileContentPathStripsTorrentRoot(t *testing.T) {
	root := "/data/torrents/console-incomplete/Minerva_Myrient"
	got := torrentFileContentPath(root, "Minerva_Myrient/No-Intro/Nintendo - Game Boy/Trip World (Europe).zip")
	want := filepath.Join(root, "No-Intro", "Nintendo - Game Boy", "Trip World (Europe).zip")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTorrentFileContentPathWithoutTorrentRootPrefix(t *testing.T) {
	root := "/data/torrents/console-incomplete/Minerva_Myrient"
	got := torrentFileContentPath(root, "No-Intro/Nintendo - Game Boy/Trip World (Europe).zip")
	want := filepath.Join(root, "No-Intro", "Nintendo - Game Boy", "Trip World (Europe).zip")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTorrentWantedCompleteSelectedFiles(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	cfg.QBURL = qm.srv.URL
	m := New(cfg, jobs, qm.client())

	hash := "sel-hash"
	qm.setFiles([]qbit.TorrentFile{
		{Name: "a.zip", Priority: 1, Progress: 1.0, Index: 0},
		{Name: "b.zip", Priority: 0, Progress: 0.0, Index: 1},
	})
	tor := &qbit.Torrent{Hash: hash, Progress: 0.5}
	if !m.torrentWantedComplete(tor) {
		t.Fatal("wanted file complete should report ready")
	}
	qm.setFiles([]qbit.TorrentFile{
		{Name: "a.zip", Priority: 1, Progress: 0.5, Index: 0},
		{Name: "b.zip", Priority: 0, Progress: 0.0, Index: 1},
	})
	if m.torrentWantedComplete(tor) {
		t.Fatal("incomplete wanted file should not report ready")
	}
}

func TestWatchGameTorrentImportsReadyArchiveJobWithoutSibling(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	cfg.QBURL = qm.srv.URL
	cfg.FileListScanEnabled = false
	m := New(cfg, jobs, qm.client())

	hash := "gb-multi"
	contentRoot := filepath.Join(t.TempDir(), "Minerva_Myrient")
	romDir := filepath.Join(contentRoot, "No-Intro", "Nintendo - Game Boy")
	writeFileT(t, filepath.Join(romDir, "Trip World (Europe).zip"), []byte("rom"))

	qm.setFiles([]qbit.TorrentFile{
		{Name: "Minerva_Myrient/No-Intro/Nintendo - Game Boy/Trip World (Europe).zip", Priority: 1, Progress: 1.0, Index: 0},
		{Name: "Minerva_Myrient/No-Intro/Nintendo - Game Boy/Other Game (Europe).zip", Priority: 1, Progress: 0.5, Index: 1},
	})
	tor := qbit.Torrent{Name: "Game Boy", Hash: hash, Progress: 0.75, ContentPath: contentRoot}
	qm.setTorrents([]qbit.Torrent{tor})

	jobs.Set("job-a", map[string]interface{}{
		"status": "downloading", "title": "Trip World (Europe).zip",
		"info_hash": hash, "platform": "Game Boy", "platform_slug": "gb",
	})
	jobs.Set("job-b", map[string]interface{}{
		"status": "downloading", "title": "Other Game (Europe).zip",
		"info_hash": hash, "platform": "Game Boy", "platform_slug": "gb",
	})

	go m.watchGameTorrent("job-a", hash, "Trip World (Europe).zip", "Game Boy", "gb", false)
	waitJobStatus(t, jobs, "job-a", "completed", minPollTimeout)

	jobB, ok := jobs.Get("job-b")
	if !ok {
		t.Fatal("job-b missing")
	}
	if status, _ := jobB["status"].(string); status != "downloading" {
		t.Errorf("job-b status = %q, want downloading while its file is incomplete", status)
	}
}

func TestImportReadyHashJobsSkipsNotReadySibling(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	cfg.QBURL = qm.srv.URL
	m := New(cfg, jobs, qm.client())

	hash := "ready-skip"
	contentRoot := filepath.Join(t.TempDir(), "Minerva_Myrient")
	romDir := filepath.Join(contentRoot, "No-Intro", "Nintendo - Game Boy")
	writeFileT(t, filepath.Join(romDir, "Trip World (Europe).zip"), []byte("rom"))

	qm.setFiles([]qbit.TorrentFile{
		{Name: "Minerva_Myrient/No-Intro/Nintendo - Game Boy/Trip World (Europe).zip", Priority: 1, Progress: 1.0, Index: 0},
		{Name: "Minerva_Myrient/No-Intro/Nintendo - Game Boy/Other Game (Europe).zip", Priority: 1, Progress: 0.5, Index: 1},
	})
	tor := qbit.Torrent{Name: "Game Boy", Hash: hash, Progress: 0.75, ContentPath: contentRoot}

	jobs.Set("job-a", map[string]interface{}{
		"status": "downloading", "title": "Trip World (Europe).zip",
		"info_hash": hash, "platform": "Game Boy", "platform_slug": "gb",
	})
	jobs.Set("job-b", map[string]interface{}{
		"status": "downloading", "title": "Other Game (Europe).zip",
		"info_hash": hash, "platform": "Game Boy", "platform_slug": "gb",
	})

	if done := m.importReadyHashJobs(tor, "job-a", "Game Boy", "gb", false); done {
		t.Fatal("importReadyHashJobs returned done while sibling still active")
	}
	waitJobStatus(t, jobs, "job-a", "completed", minPollTimeout)

	jobB, _ := jobs.Get("job-b")
	if status, _ := jobB["status"].(string); status != "downloading" {
		t.Errorf("job-b status = %q, want downloading", status)
	}
}
