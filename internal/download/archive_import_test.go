package download

import (
	"path/filepath"
	"testing"

	"gamarr/internal/qbit"
)

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
		{Name: "No-Intro/Nintendo - Game Boy/Trip World (Europe).zip", Index: 0},
		{Name: "No-Intro/Nintendo - Game Boy/Other.zip", Index: 1},
	})

	got := m.resolveImportContentPath(jobID, torrent)
	want := filepath.Join(torrent.ContentPath, "No-Intro", "Nintendo - Game Boy", "Trip World (Europe).zip")
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
