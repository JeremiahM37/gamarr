package download

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gamarr/internal/qbit"
)

// minervaRoot is where every Minerva archive downloads to, whatever the torrent
// is called after maybeRenameArchiveTorrent relabels it with its platform.
const minervaRoot = "/data/incoming/Minerva_Myrient"

func TestRecoverOrphanedTorrentsLeavesArchivesAlone(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	qm.setTorrents([]qbit.Torrent{
		// Relabelled "Wii": the name no longer says it is an archive.
		{Name: "Wii", Hash: "a1", Progress: 1.0, ContentPath: minervaRoot},
		{Name: "Minerva_Myrient", Hash: "a2", Progress: 1.0, ContentPath: minervaRoot},
		// An ordinary torrent is still recovered.
		{Name: "Zelda Collection wii pack", Hash: "h1", Progress: 1.0, ContentPath: "/data/incoming/Zelda Collection wii pack"},
	})
	cfg.QBURL = qm.srv.URL

	m := New(cfg, jobs, qm.client())
	m.RecoverOrphanedTorrents()

	items := jobs.Items()
	if len(items) != 1 {
		var titles []string
		for _, it := range items {
			title, _ := it.Data["title"].(string)
			titles = append(titles, title)
		}
		t.Fatalf("recovered %d jobs %v, want only the ordinary torrent", len(items), titles)
	}
	if title, _ := items[0].Data["title"].(string); title != "Zelda Collection wii pack" {
		t.Errorf("recovered %q, want the ordinary torrent", title)
	}
}

func TestRecoverOrphanedTorrentsDropsArchiveShells(t *testing.T) {
	tests := []struct {
		name      string
		shell     string // the shell row's title
		withROM   bool   // a ROM job from the same archive is still listed
		wantTitle []string
	}{
		{"lone shell named after the relabelled torrent", "Wii", false, nil},
		{"lone shell named Minerva_Myrient", "Minerva_Myrient", false, nil},
		{"shell beside a finished ROM job", "Wii", true, []string{"Super Smash Bros. Brawl (USA) (Rev 1).zip"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newTestConfig(t)
			jobs := newTestJobs(t)
			qm := newQbitMock(t)
			qm.setTorrents([]qbit.Torrent{{Name: "Wii", Hash: "a1", Progress: 1.0, ContentPath: minervaRoot}})
			cfg.QBURL = qm.srv.URL
			// The row a pass before this change minted, after a failed organize.
			jobs.Set("shell1", map[string]interface{}{
				"status": "error", "title": tt.shell, "info_hash": "a1", "platform": "PC", "is_pc": true,
				"error": "Organize failed: already exists at destination: /data/vault/Minerva_Myrient",
			})
			if tt.withROM {
				jobs.Set("rom1", map[string]interface{}{
					"status": "completed", "title": "Super Smash Bros. Brawl (USA) (Rev 1).zip", "info_hash": "a1",
					"platform": "Wii", "platform_slug": "wii",
				})
			}

			m := New(cfg, jobs, qm.client())
			m.RecoverOrphanedTorrents()

			var got []string
			for _, it := range jobs.Items() {
				title, _ := it.Data["title"].(string)
				got = append(got, title)
			}
			if len(got) != len(tt.wantTitle) || (len(got) == 1 && got[0] != tt.wantTitle[0]) {
				t.Errorf("jobs after recovery = %v, want %v", got, tt.wantTitle)
			}
		})
	}
}

func TestOrganizeRefusesArchiveRoot(t *testing.T) {
	for _, isPC := range []bool{true, false} {
		name := "vault"
		platf, slug := "PC", ""
		if !isPC {
			name, platf, slug = "roms", "Wii", "wii"
		}
		t.Run(name, func(t *testing.T) {
			m, _, jobID := setupOrganizeJob(t)
			content := filepath.Join(t.TempDir(), "Minerva_Myrient")
			writeFileT(t, filepath.Join(content, "Redump", "Nintendo - Wii", "Game (USA).zip"), []byte("zip"))
			torrent := &qbit.Torrent{Name: "Wii", Hash: "a1", ContentPath: content}

			m.organizeGame(jobID, torrent, platf, slug, isPC, 1)

			job, _ := m.Jobs().Get(jobID)
			if status, _ := job["status"].(string); status != "error" {
				t.Errorf("status = %q, want error", status)
			}
			if msg, _ := job["error"].(string); !strings.Contains(msg, "root folder of a shared archive") {
				t.Errorf("error = %q, want the archive-root refusal", msg)
			}
			for _, dest := range []string{
				filepath.Join(m.cfg.GamesVaultPath, "Minerva_Myrient"),
				filepath.Join(m.cfg.GamesRomsPath, "wii", "Minerva_Myrient"),
			} {
				if pathExists(dest) {
					t.Errorf("%s was created: the archive root was imported", dest)
				}
			}
		})
	}
}

func TestWatcherSkipsArchiveTorrents(t *testing.T) {
	for _, name := range []string{"Wii", "Minerva_Myrient"} {
		t.Run(name, func(t *testing.T) {
			qm := newQbitMock(t)
			w, m := newTestWatcher(t, qm)
			content := filepath.Join(t.TempDir(), "Minerva_Myrient")
			writeFileT(t, filepath.Join(content, "Redump", "Nintendo - Wii", "Game (USA).zip"), []byte("zip"))
			qm.setTorrents([]qbit.Torrent{{Name: name, Hash: "a1", Progress: 1.0, ContentPath: content}})

			w.checkCompleted()

			// An auto-import runs in a goroutine; give one the time to start.
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) && len(m.Jobs().Items()) == 0 {
				time.Sleep(50 * time.Millisecond)
			}
			if n := len(m.Jobs().Items()); n != 0 {
				t.Errorf("watcher created %d job(s) for a shared archive, want none", n)
			}
			if pathExists(filepath.Join(w.cfg.GamesVaultPath, "Minerva_Myrient")) {
				t.Error("the archive root was auto-imported into the vault")
			}
		})
	}
}
