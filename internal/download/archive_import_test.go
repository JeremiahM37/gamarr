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
