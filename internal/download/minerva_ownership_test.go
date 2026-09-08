package download

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gamarr/internal/db"
	"gamarr/internal/minerva"
	"gamarr/internal/search"
	"gamarr/internal/sources"
)

// The Docker API barrier holds the real generic importer inside ScanWithClamAV,
// after it has claimed the hash. Selective registration must not take ownership
// or launch work while that import can still publish or clean up the torrent.
func TestMinervaRegistrationRefusesActiveGenericImport(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprint(retry), func(t *testing.T) {
			m, q := newSelectiveTest(t)
			q.exists = true
			torrent := q.torrent
			torrent.Hash = " \tABCDEF1234 "
			id := "legacy"
			if retry {
				m.jobs.Set(id, map[string]interface{}{
					"status": "error", "source": "minerva", "download_url": "https://example.test/collection.torrent", "info_hash": "abcdef1234",
					"torrent_file_index": 7, "torrent_file_path": "Collection/HeartGold.nds", "torrent_file_size": int64(3),
					"title": "HeartGold", "platform": "DS", "platform_slug": "nds", "is_pc": false,
				})
				if _, err := m.jobs.DB().Exec("DELETE FROM minerva_torrent_ownership"); err != nil {
					t.Fatal(err)
				}
			}
			// Keep the Unix socket path below its platform length limit.
			dir, err := os.MkdirTemp("", "scan-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			m.cfg.DockerSocket = filepath.Join(dir, "docker.sock")
			m.cfg.ClamAVSocket = filepath.Join(dir, "clam.sock")
			listener, err := net.Listen("unix", m.cfg.DockerSocket)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var enterOnce sync.Once
			srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/start") {
					enterOnce.Do(func() { close(entered) })
					<-release
					if err := os.WriteFile(m.cfg.ClamAVSocket, nil, 0600); err != nil {
						t.Error(err)
					}
				}
				w.WriteHeader(http.StatusNoContent)
			})}
			go srv.Serve(listener)
			defer srv.Close()
			m.jobs.Set("generic", map[string]interface{}{"status": "organizing", "info_hash": torrent.Hash, "title": torrent.Name})
			finished := make(chan bool, 1)
			go func() { finished <- m.importFinishedTorrent("test", "generic", torrent, "DS", "nds", false) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				close(release)
				t.Fatal("generic import did not reach scan barrier")
			}
			// Isolate registration from payload setup and inspect its synchronous
			// result. Even a broken registration cannot mutate qB during assertions.
			m.selectiveMu.Lock()
			accepted := false
			if retry {
				var reason string
				accepted, reason = m.RetryJob(id)
				if accepted || !strings.Contains(strings.ToLower(reason), "import") {
					t.Errorf("retry ignored active generic import: accepted=%v reason=%s", accepted, reason)
				}
				if job, _ := m.jobs.Get(id); job["status"] != "error" {
					t.Errorf("refused retry changed job: %v", job)
				}
			} else {
				id, err = m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
				accepted = err == nil
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), "import") {
					t.Errorf("fresh selection ignored active generic import: id=%s error=%v", id, err)
				}
				if len(m.jobs.Items()) != 1 {
					t.Error("refused selection created a job")
				}
			}
			if owned, err := m.jobs.IsMinervaTorrent("abcdef1234"); err != nil || owned {
				t.Errorf("ownership changed during generic import: %v %v", owned, err)
			}
			if _, active := m.activeSelective.Load(id); active {
				t.Error("selective worker launched during generic import")
			}
			if calls := q.callLog(); calls != "" {
				t.Errorf("selective registration contacted qB: %s", calls)
			}
			close(release)
			select {
			case ok := <-finished:
				if !ok {
					t.Error("existing generic import failed")
				}
			case <-time.After(3 * time.Second):
				t.Error("generic import deadlocked")
			}
			m.selectiveMu.Unlock()
			if accepted {
				selectiveJobDone(t, m, id)
			}
			if _, busy := m.importing.Load("abcdef1234"); busy {
				t.Error("generic import did not release normalized hash")
			}
			// A completed generic import no longer owns the claim. The refused
			// registration can now proceed, proving refusal did not leak locks.
			if !accepted {
				if retry {
					if ok, reason := m.RetryJob(id); !ok {
						t.Fatal(reason)
					}
				} else {
					id, err = m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
					if err != nil {
						t.Fatal(err)
					}
				}
				if job := selectiveJobDone(t, m, id); job["status"] != "completed" {
					t.Fatal(job)
				}
			}
		})
	}
}

// This starts with real bencoded metadata, not manually rooted index rows.
// Omitting info.name in ParseTorrent must fail at the real qB validation boundary.
func TestMinervaParsedCollectionSelectiveDownload(t *testing.T) {
	for _, livePath := range []string{"Collection/dir/HeartGold.nds", "Wrong/dir/HeartGold.nds", "Collection/../Collection/dir/HeartGold.nds"} {
		t.Run(livePath, func(t *testing.T) {
			m, q := newSelectiveTest(t)
			search.ResetCircuit("minerva")
			meta, err := minerva.ParseTorrent([]byte("d4:infod5:filesld6:lengthi5e4:pathl9:Other.ndseed6:lengthi3e4:pathl3:dir13:HeartGold.ndseed6:lengthi3e4:pathl12:unwanted.cmdeee4:name10:Collectionee"))
			if err != nil {
				t.Fatal(err)
			}
			svc, err := minerva.Open(m.cfg.DataDir, sources.MinervaSpec{Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			idx, err := minerva.OpenIndex(filepath.Join(m.cfg.DataDir, "minerva", "index.db"))
			if err != nil {
				t.Fatal(err)
			}
			err = idx.ReplaceCollection(context.Background(), minerva.CollectionRecord{
				PlatformSlug: "nds", InfoHash: meta.InfoHash, TorrentURL: "https://example.test/collection.torrent",
			}, meta.Files)
			idx.Close()
			if err != nil {
				t.Fatal(err)
			}
			hits := search.SearchMinerva(svc, "HeartGold", "nds")
			if len(hits) != 1 || hits[0].TorrentFileIndex == nil {
				t.Fatalf("search hits: %+v", hits)
			}
			hit := hits[0]
			q.torrent.Hash = meta.InfoHash
			for i := range q.files {
				q.files[i].Index = i
			}
			q.files[1].Name = livePath
			writeFileT(t, filepath.Join(m.cfg.QBSavePath, "Collection", "dir", "HeartGold.nds"), []byte("rom"))
			id, err := m.DownloadSelectiveTorrent(hit.DownloadURL, hit.InfoHash, *hit.TorrentFileIndex, hit.TorrentFilePath, hit.TorrentFileSize, hit.Title, hit.Platform, hit.PlatformSlug, hit.IsPC)
			if err != nil {
				t.Fatal(err)
			}
			job := selectiveJobDone(t, m, id)
			if livePath != "Collection/dir/HeartGold.nds" {
				if job["status"] != "error" || len(m.jobs.RecentLibraryItems(10)) != 0 || strings.Contains(q.callLog(), "priority:") || strings.Contains(q.callLog(), "start:") {
					t.Fatalf("invalid live path crossed selection boundary: job=%v calls=%s", job, q.callLog())
				}
				return
			}
			if job["status"] != "completed" {
				t.Fatalf("parsed path %q rejected by qB path %q: %v", hit.TorrentFilePath, livePath, job)
			}
			if hit.TorrentFilePath != "Collection/dir/HeartGold.nds" || *hit.TorrentFileIndex != 1 || hit.TorrentFileSize != 3 {
				t.Fatalf("selection metadata: %+v", hit)
			}
			wantCalls := fmt.Sprintf("add:true:true,files:%s,priority:%s:0|1|2:0,priority:%s:1:7,start:%s", meta.InfoHash, meta.InfoHash, meta.InfoHash, meta.InfoHash)
			if !strings.Contains(q.callLog(), wantCalls) || strings.Contains(q.callLog(), "delete:") {
				t.Fatalf("qB boundary: %s", q.callLog())
			}
			items := m.jobs.RecentLibraryItems(10)
			if len(items) != 1 || items[0].Source != "minerva" || items[0].SourceID != "minerva:"+meta.InfoHash+":1" {
				t.Fatalf("library: %+v", items)
			}
			data, err := os.ReadFile(items[0].FilePath)
			if err != nil || string(data) != "rom" {
				t.Fatalf("target payload: %q %v", data, err)
			}
			entries, err := os.ReadDir(filepath.Join(m.cfg.GamesRomsPath, "nds"))
			if err != nil || len(entries) != 2 {
				t.Fatalf("imported siblings: %v %v", entries, err)
			}
		})
	}
}

func completedMinervaWithoutJob(t *testing.T) (*Manager, *selectiveQbit) {
	t.Helper()
	m, q := newSelectiveTest(t)
	id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", " ABCDEF1234 ", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
	if err != nil {
		t.Fatal(err)
	}
	if job := selectiveJobDone(t, m, id); job["status"] != "completed" {
		t.Fatal(job)
	}
	m.jobs.Delete(id)
	if len(m.jobs.Items()) != 0 {
		t.Fatal("fixture job was not cleared")
	}
	q.mu.Lock()
	q.torrent.Hash = "ABCDEF1234"
	q.torrent.Progress = 1
	q.calls = nil
	q.mu.Unlock()
	m.cfg.RemoveAfterImport = true
	m.cfg.FileListScanEnabled = false
	return m, q
}

func assertNoGenericCollectionImport(t *testing.T, m *Manager, q *selectiveQbit) {
	t.Helper()
	if jobs := m.jobs.Items(); len(jobs) != 0 {
		t.Errorf("generic jobs created: %+v", jobs)
	}
	if items := m.jobs.RecentLibraryItems(10); len(items) != 1 || items[0].Source != "minerva" {
		t.Errorf("generic collection import: %+v", items)
	}
	if calls := q.callLog(); strings.Contains(calls, "delete:") || strings.Contains(calls, "priority:") || strings.Contains(calls, "start:") {
		t.Errorf("generic qB mutation: %s", calls)
	}
	if data, err := os.ReadFile(filepath.Join(m.cfg.QBSavePath, "Collection", "Other.nds")); err != nil || string(data) != "other" {
		t.Errorf("unselected sibling changed: %q %v", data, err)
	}
}

func TestMinervaOwnershipSurvivesClearedJobWatcher(t *testing.T) {
	m, q := completedMinervaWithoutJob(t)
	w := NewWatcher(m.cfg, m)
	w.checkCompleted()
	// A mistaken dispatch stores processing before checkCompleted returns.
	// Join that worker so a failing regression never leaks imports into cleanup.
	waitFor(t, minPollTimeout, "watcher worker to finish", func() bool { _, busy := w.processing.Load(q.torrent.Hash); return !busy })
	assertNoGenericCollectionImport(t, m, q)
	// Cover a stale dispatch already queued when ownership became durable.
	w.importTorrent(q.torrent)
	assertNoGenericCollectionImport(t, m, q)
}

func TestMinervaOwnershipSurvivesClearedJobRestart(t *testing.T) {
	for _, progress := range []float64{1, .5} {
		t.Run(fmt.Sprint(progress), func(t *testing.T) {
			m, q := completedMinervaWithoutJob(t)
			var err error
			var seq int
			var database, jobsPath string
			if err = m.jobs.DB().QueryRow("PRAGMA database_list").Scan(&seq, &database, &jobsPath); err != nil {
				t.Fatal(err)
			}
			if err = m.jobs.Close(); err != nil {
				t.Fatal(err)
			}
			m.jobs, err = db.New(jobsPath)
			if err != nil {
				t.Fatal(err)
			}
			defer m.jobs.Close()
			q.mu.Lock()
			q.torrent.Progress = progress
			q.mu.Unlock()
			m.RecoverOrphanedTorrents()
			// A faulty recovery can start a generic watcher: let it observe completion.
			q.mu.Lock()
			q.torrent.Progress = 1
			q.mu.Unlock()
			if len(m.jobs.Items()) != 0 {
				waitFor(t, 7*time.Second, "incorrect recovery worker to finish", func() bool {
					for _, job := range m.jobs.Items() {
						if job.Data["status"] == "downloading" || job.Data["status"] == "organizing" {
							return false
						}
					}
					return true
				})
			}
			assertNoGenericCollectionImport(t, m, q)
		})
	}
}

func TestMinervaOwnershipRefusesManualOrganizeAfterClear(t *testing.T) {
	m, q := completedMinervaWithoutJob(t)
	id, err := m.OrganizeTorrent(q.torrent.Hash, "DS", "nds", false)
	if err == nil {
		waitFor(t, minPollTimeout, "incorrect manual import to finish", func() bool { job, _ := m.jobs.Get(id); return job["status"] == "completed" || job["status"] == "error" })
		t.Error("manual organize accepted a retained Minerva collection")
	}
	assertNoGenericCollectionImport(t, m, q)
}

func TestMinervaOwnershipPersistedBeforeSelectiveWorker(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprint(retry), func(t *testing.T) {
			m, _ := newSelectiveTest(t)
			id := "legacy"
			if retry {
				m.jobs.Set(id, map[string]interface{}{
					"status": "error", "source": "minerva", "download_url": "https://example.test/collection.torrent", "info_hash": " ABCDEF1234 ",
					"torrent_file_index": 7, "torrent_file_path": "Collection/HeartGold.nds", "torrent_file_size": int64(3),
					"title": "HeartGold", "platform": "DS", "platform_slug": "nds", "is_pc": false,
				})
				// Simulate a legacy row whose marker still needs to be established.
				_, _ = m.jobs.DB().Exec("DELETE FROM minerva_torrent_ownership")
			}
			m.selectiveMu.Lock()
			if retry {
				if ok, reason := m.RetryJob(id); !ok {
					m.selectiveMu.Unlock()
					t.Fatal(reason)
				}
			} else {
				var err error
				id, err = m.DownloadSelectiveTorrent("https://example.test/collection.torrent", " ABCDEF1234 ", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
				if err != nil {
					m.selectiveMu.Unlock()
					t.Fatal(err)
				}
			}
			var hash string
			err := m.jobs.DB().QueryRow("SELECT info_hash FROM minerva_torrent_ownership").Scan(&hash)
			m.selectiveMu.Unlock()
			selectiveJobDone(t, m, id)
			if err != nil || hash != "abcdef1234" {
				t.Fatalf("ownership was not durably recorded before worker: %q %v", hash, err)
			}
		})
	}
}

func TestMinervaOwnershipWriteFailureRefusesSelectiveWork(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprint(retry), func(t *testing.T) {
			m, q := newSelectiveTest(t)
			id := "failed"
			if retry {
				m.jobs.Set(id, map[string]interface{}{
					"status": "error", "source": "minerva", "download_url": "https://example.test/collection.torrent", "info_hash": "abcdef1234",
					"torrent_file_index": 7, "torrent_file_path": "Collection/HeartGold.nds", "torrent_file_size": int64(3),
					"title": "HeartGold", "platform": "DS", "platform_slug": "nds", "is_pc": false,
				})
			}
			m.jobs.Close()
			if retry {
				if ok, reason := m.RetryJob(id); ok {
					t.Errorf("retry accepted failed persistence: %s", reason)
				}
			} else {
				var err error
				id, err = m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
				if err == nil {
					t.Error("selective download accepted failed persistence")
				}
			}
			waitFor(t, minPollTimeout, "any incorrect worker to exit", func() bool { _, active := m.activeSelective.Load(id); return !active })
			if calls := q.callLog(); calls != "" {
				t.Errorf("selective worker used qB without durable ownership: %s", calls)
			}
		})
	}
}

func TestMinervaOwnershipLookupFailureBlocksGenericPaths(t *testing.T) {
	for _, via := range []string{"watcher", "recovery", "manual"} {
		t.Run(via, func(t *testing.T) {
			m, q := completedMinervaWithoutJob(t)
			// The jobs/library tables stay healthy so accidental generic writes are visible.
			if _, err := m.jobs.DB().Exec("DROP TABLE IF EXISTS minerva_torrent_ownership"); err != nil {
				t.Fatal(err)
			}
			switch via {
			case "watcher":
				w := NewWatcher(m.cfg, m)
				w.checkCompleted()
				waitFor(t, minPollTimeout, "watcher to finish", func() bool { _, busy := w.processing.Load(q.torrent.Hash); return !busy })
				w.importTorrent(q.torrent)
			case "recovery":
				m.RecoverOrphanedTorrents()
			case "manual":
				id, err := m.OrganizeTorrent(q.torrent.Hash, "DS", "nds", false)
				if err == nil {
					waitFor(t, minPollTimeout, "manual worker to finish", func() bool { job, _ := m.jobs.Get(id); return job["status"] == "completed" || job["status"] == "error" })
					t.Error("manual organization swallowed ownership read error")
				}
			}
			assertNoGenericCollectionImport(t, m, q)
		})
	}
}

func TestMinervaOwnershipBlocksAlreadyDispatchedGenericImport(t *testing.T) {
	for _, lookupFails := range []bool{false, true} {
		t.Run(fmt.Sprint(lookupFails), func(t *testing.T) {
			m, q := completedMinervaWithoutJob(t)
			m.jobs.Set("queued", map[string]interface{}{"status": "organizing", "info_hash": q.torrent.Hash, "title": q.torrent.Name})
			if lookupFails {
				if _, err := m.jobs.DB().Exec("DROP TABLE minerva_torrent_ownership"); err != nil {
					t.Fatal(err)
				}
			}
			if m.importFinishedTorrent("queued", "queued", q.torrent, "DS", "nds", false) {
				t.Error("queued generic import accepted Minerva collection")
			}
			job, _ := m.jobs.Get("queued")
			if job["status"] != "error" {
				t.Errorf("queued job did not report refusal: %v", job)
			}
			m.jobs.Delete("queued")
			assertNoGenericCollectionImport(t, m, q)
		})
	}
}
