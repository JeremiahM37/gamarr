package download

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gamarr/internal/fileops"
	"gamarr/internal/platform"
	"gamarr/internal/qbit"
	"gamarr/internal/search"
)

// selectiveQbit exercises the real Web API client while keeping the collection
// and its on-disk payload independent from the requested selection.
type selectiveQbit struct {
	mu              sync.Mutex
	srv             *httptest.Server
	torrent         qbit.Torrent
	exists          bool
	files           []qbit.TorrentFile
	calls           []string
	infoFails       bool
	fileReads       int
	emptyReads      int
	readyAfter      int
	changeAfter     int
	failAction      string
	outsideCategory bool
	filesEntered    chan struct{}
	filesRelease    chan struct{}
}

func newSelectiveTest(t *testing.T) (*Manager, *selectiveQbit) {
	t.Helper()
	oldInterval, oldTimeout := selectiveMetadataInterval, selectiveMetadataTimeout
	oldPoll, oldDownload := selectivePollInterval, selectiveDownloadTimeout
	selectiveMetadataInterval, selectiveMetadataTimeout = time.Millisecond, time.Second
	selectivePollInterval, selectiveDownloadTimeout = time.Millisecond, time.Second
	t.Cleanup(func() {
		selectiveMetadataInterval, selectiveMetadataTimeout = oldInterval, oldTimeout
		selectivePollInterval, selectiveDownloadTimeout = oldPoll, oldDownload
	})
	cfg := newTestConfig(t)
	cfg.QBURL = "configured"
	cfg.ImportMode = fileops.ModeCopy
	q := &selectiveQbit{torrent: qbit.Torrent{
		Hash: "abcdef1234", Name: "Collection", SavePath: cfg.QBSavePath,
		ContentPath: filepath.Join(cfg.QBSavePath, "Collection"), State: "stoppedDL", Progress: .2,
	}, files: []qbit.TorrentFile{
		{Index: 2, Name: "Collection/Other.nds", Size: 5, Progress: 0, Priority: 1},
		{Index: 7, Name: "Collection/HeartGold.nds", Size: 3, Progress: 1, Priority: 1},
		{Index: 9, Name: "Collection/unwanted.cmd", Size: 3, Progress: 0, Priority: 1},
	}}
	for _, f := range q.files {
		data := []byte("rom")
		if f.Index == 2 {
			data = []byte("other")
		}
		writeFileT(t, filepath.Join(cfg.QBSavePath, filepath.FromSlash(f.Name)), data)
	}
	q.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		defer q.mu.Unlock()
		r.ParseForm()
		switch r.URL.Path {
		case "/api/v2/auth/login":
			fmt.Fprint(w, "Ok.")
		case "/api/v2/torrents/info":
			q.calls = append(q.calls, "info")
			if q.infoFails {
				w.WriteHeader(500)
				return
			}
			if q.outsideCategory && r.Form.Get("category") != "" {
				fmt.Fprint(w, "[]")
				return
			}
			if q.exists {
				json.NewEncoder(w).Encode([]qbit.Torrent{q.torrent})
			} else {
				fmt.Fprint(w, "[]")
			}
		case "/api/v2/torrents/add":
			q.calls = append(q.calls, "add:"+r.Form.Get("paused")+":"+r.Form.Get("stopped"))
			if q.failAction == "add" {
				fmt.Fprint(w, "Fails.")
				return
			}
			q.exists = true
			fmt.Fprint(w, "Ok.")
		case "/api/v2/torrents/files":
			q.calls = append(q.calls, "files:"+r.Form.Get("hash"))
			q.fileReads++
			if q.fileReads == 1 && q.filesRelease != nil {
				entered, release := q.filesEntered, q.filesRelease
				q.mu.Unlock()
				close(entered)
				<-release
				q.mu.Lock()
			}
			if q.fileReads <= q.emptyReads {
				fmt.Fprint(w, "[]")
				return
			}
			files := append([]qbit.TorrentFile(nil), q.files...)
			if q.fileReads < q.readyAfter {
				files[1].Progress = .4
			}
			if q.changeAfter > 0 && q.fileReads >= q.changeAfter {
				files[1].Name = "Collection/Different.nds"
			}
			json.NewEncoder(w).Encode(files)
		case "/api/v2/torrents/filePrio":
			q.calls = append(q.calls, "priority:"+r.Form.Get("hash")+":"+r.Form.Get("id")+":"+r.Form.Get("priority"))
			if q.failAction == "priority:"+r.Form.Get("priority") {
				w.WriteHeader(500)
				return
			}
			priority, _ := strconv.Atoi(r.Form.Get("priority"))
			for _, id := range strings.Split(r.Form.Get("id"), "|") {
				for i := range q.files {
					if strconv.Itoa(q.files[i].Index) == id {
						q.files[i].Priority = priority
					}
				}
			}
		case "/api/v2/torrents/start", "/api/v2/torrents/resume":
			q.calls = append(q.calls, "start:"+r.Form.Get("hashes"))
			if q.failAction == "start" {
				w.WriteHeader(500)
				return
			}
			q.torrent.State = "downloading"
		case "/api/v2/torrents/delete":
			q.calls = append(q.calls, "delete:"+r.Form.Get("hashes")+":"+r.Form.Get("deleteFiles"))
			q.exists = false
		default:
			t.Errorf("unexpected qB request: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(q.srv.Close)
	return New(cfg, newTestJobs(t), qbit.New(q.srv.URL, "user", "pass")), q
}

func (q *selectiveQbit) callLog() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return strings.Join(q.calls, ",")
}

func selectiveJobDone(t *testing.T, m *Manager, id string) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, _ := jobFromDB(t, m.jobs, id)
		if job["status"] == "completed" || job["status"] == "error" {
			if _, active := m.activeSelective.Load(id); !active {
				return job
			}
		}
		time.Sleep(time.Millisecond)
	}
	job, _ := m.jobs.Get(id)
	t.Fatalf("selective job did not finish: %#v", job)
	return nil
}

func TestDownloadSelectiveTorrentMismatchBeforeStart(t *testing.T) {
	m, q := newSelectiveTest(t)
	q.files[1].Name = "Collection/Different.nds"
	id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
	if err != nil {
		t.Fatal(err)
	}
	job := selectiveJobDone(t, m, id)
	if job["status"] != "error" {
		t.Fatalf("mismatched live file imported: %#v", job)
	}
	if calls := q.callLog(); strings.Contains(calls, "priority:") || strings.Contains(calls, "start:") {
		t.Fatalf("mismatch mutated selection or started transfer: %s", calls)
	}
	if items := m.jobs.RecentLibraryItems(10); len(items) != 0 {
		t.Fatalf("mismatch imported library items: %#v", items)
	}
}

func TestDownloadSelectiveTorrentSuccessfulSelection(t *testing.T) {
	m, q := newSelectiveTest(t)
	id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
	if err != nil {
		t.Fatal(err)
	}
	job := selectiveJobDone(t, m, id)
	if job["status"] != "completed" {
		t.Fatalf("selection failed: %#v", job)
	}
	want := "add:true:true,files:abcdef1234,priority:abcdef1234:2|7|9:0,priority:abcdef1234:7:7,start:abcdef1234"
	if calls := q.callLog(); !strings.Contains(calls, want) {
		t.Fatalf("unsafe order: %s; want %s", calls, want)
	}
	dest := filepath.Join(m.cfg.GamesRomsPath, "nds", "HeartGold.nds")
	if data, err := os.ReadFile(dest); err != nil || string(data) != "rom" {
		t.Fatalf("target payload = %q, %v", data, err)
	}
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil || len(entries) != 2 {
		t.Fatalf("want only target and sidecar, got %v (%v)", entries, err)
	}
	if items := m.jobs.RecentLibraryItems(10); len(items) != 1 || items[0].FilePath != dest {
		t.Fatalf("target-only library import: %#v", items)
	}
}

func TestDownloadSelectiveTorrentExistingPreservesSelection(t *testing.T) {
	for _, state := range []string{"downloading", "stalledDL", "stoppedDL", "pausedDL", "stoppedUP", "pausedUP"} {
		t.Run(state, func(t *testing.T) {
			m, q := newSelectiveTest(t)
			q.exists = true
			q.torrent.State = state
			q.torrent.Hash = "ABCDEF1234"
			q.files[0].Priority = 6
			id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", " ABCDEF1234 ", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
			if err != nil {
				t.Fatal(err)
			}
			job := selectiveJobDone(t, m, id)
			if job["status"] != "completed" {
				t.Fatalf("existing selection failed: %#v", job)
			}
			calls := q.callLog()
			if strings.Contains(calls, "add:") || strings.Contains(calls, ":0") || strings.Contains(calls, "delete:") {
				t.Fatalf("existing torrent changed: %s", calls)
			}
			if !strings.Contains(calls, "priority:abcdef1234:7:7") {
				t.Fatalf("hash was not normalized: %s", calls)
			}
			wantStart := strings.HasPrefix(state, "stopped") || strings.HasPrefix(state, "paused")
			if strings.Contains(calls, "start:") != wantStart {
				t.Fatalf("start for state %s: %s", state, calls)
			}
			q.mu.Lock()
			priority := q.files[0].Priority
			q.mu.Unlock()
			if priority != 6 {
				t.Fatalf("other selected priority changed to %d", priority)
			}
		})
	}
}

func TestDownloadSelectiveTorrentConcurrentSelectionsCoexist(t *testing.T) {
	m, q := newSelectiveTest(t)
	q.files[0].Progress = 1
	var ids [2]string
	var errs [2]error
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			index, path, size := 7, "Collection/HeartGold.nds", int64(3)
			if i == 1 {
				index, path, size = 2, "Collection/Other.nds", 5
			}
			ids[i], errs[i] = m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", index, path, size, "Selected", "DS", "nds", false)
		}(i)
	}
	wg.Wait()
	for i, id := range ids {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if job := selectiveJobDone(t, m, id); job["status"] != "completed" {
			t.Fatalf("concurrent selection failed: %#v", job)
		}
	}
	calls := q.callLog()
	if strings.Count(calls, "add:") != 1 || strings.Count(calls, "2|7|9:0") != 1 {
		t.Fatalf("collection initialized more than once: %s", calls)
	}
	q.mu.Lock()
	p0, p1 := q.files[0].Priority, q.files[1].Priority
	q.mu.Unlock()
	if p0 != 7 || p1 != 7 {
		t.Fatalf("selected files clobbered: priorities = %d, %d", p0, p1)
	}
	if items := m.jobs.RecentLibraryItems(10); len(items) != 2 {
		t.Fatalf("wanted two file-specific library records: %#v", items)
	}
}

func TestDownloadSelectiveTorrentLiveValidation(t *testing.T) {
	for _, tc := range []struct {
		name, requested, live string
		index                 int
		size                  int64
		wantOK                bool
	}{
		{"wrong_index", "Collection/HeartGold.nds", "Collection/HeartGold.nds", 8, 3, false},
		{"size_mismatch", "Collection/HeartGold.nds", "Collection/HeartGold.nds", 7, 4, false},
		{"unknown_size", "Collection/HeartGold.nds", "Collection/HeartGold.nds", 7, 0, true},
		{"clean_paths", "./Collection//HeartGold.nds", "Collection/./HeartGold.nds", 7, 3, true},
		{"case_mismatch", "Collection/heartgold.nds", "Collection/HeartGold.nds", 7, 3, false},
		{"absolute", "/Collection/HeartGold.nds", "/Collection/HeartGold.nds", 7, 3, false},
		{"drive", "C:/Collection/HeartGold.nds", "C:/Collection/HeartGold.nds", 7, 3, false},
		{"backslash", `Collection\HeartGold.nds`, `Collection\HeartGold.nds`, 7, 3, false},
		{"traversal", "Collection/../Collection/HeartGold.nds", "Collection/../Collection/HeartGold.nds", 7, 3, false},
		{"parent", "../HeartGold.nds", "../HeartGold.nds", 7, 3, false},
		{"empty", "", "", 7, 3, false},
		{"directory", ".", ".", 7, 3, false},
		{"live_traversal", "Collection/HeartGold.nds", "Collection/../Collection/HeartGold.nds", 7, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, q := newSelectiveTest(t)
			q.files[1].Name = tc.live
			id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", tc.index, tc.requested, tc.size, "HeartGold", "DS", "nds", false)
			if err != nil {
				if tc.wantOK {
					t.Fatal(err)
				}
				return
			}
			job := selectiveJobDone(t, m, id)
			if (job["status"] == "completed") != tc.wantOK {
				t.Errorf("validation outcome: %#v", job)
			}
			if !tc.wantOK {
				if calls := q.callLog(); strings.Contains(calls, "priority:") || strings.Contains(calls, "start:") {
					t.Errorf("invalid target mutated transfer: %s", calls)
				}
				if items := m.jobs.RecentLibraryItems(10); len(items) != 0 {
					t.Errorf("invalid target imported: %#v", items)
				}
			}
		})
	}
}

func TestDownloadSelectiveTorrentMismatchCleanupCreatedOnly(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_%v", exists), func(t *testing.T) {
			m, q := newSelectiveTest(t)
			q.exists = exists
			q.files[1].Name = "Different.nds"
			id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
			if err != nil {
				t.Fatal(err)
			}
			if job := selectiveJobDone(t, m, id); job["status"] != "error" {
				t.Fatal(job)
			}
			calls := q.callLog()
			if strings.Contains(calls, "delete:abcdef1234:true") != !exists {
				t.Fatalf("created-only cleanup: %s", calls)
			}
			if exists && (strings.Contains(calls, "delete:") || strings.Contains(calls, "priority:") || strings.Contains(calls, "start:")) {
				t.Fatalf("mismatch changed pre-existing torrent: %s", calls)
			}
		})
	}
}

func TestDownloadSelectiveTorrentWaitsForMetadata(t *testing.T) {
	m, q := newSelectiveTest(t)
	q.emptyReads = 2
	id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
	if err != nil {
		t.Fatal(err)
	}
	if job := selectiveJobDone(t, m, id); job["status"] != "completed" {
		t.Fatalf("failed before metadata arrived: %#v", job)
	}
	if calls := q.callLog(); !strings.Contains(calls, "add:true:true,files:abcdef1234,files:abcdef1234,files:abcdef1234,priority:") {
		t.Fatalf("priority preceded metadata: %s", calls)
	}
}

func TestDownloadSelectiveTorrentUnreadableListingDoesNotAdd(t *testing.T) {
	m, q := newSelectiveTest(t)
	q.infoFails = true
	id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
	if err != nil {
		t.Fatal(err)
	}
	if job := selectiveJobDone(t, m, id); job["status"] != "error" {
		t.Fatal(job)
	}
	if calls := q.callLog(); calls != "info" {
		t.Fatalf("unknown torrent state caused writes: %s", calls)
	}
}

func TestDownloadSelectiveTorrentWaitsOnlyForTarget(t *testing.T) {
	m, q := newSelectiveTest(t)
	q.torrent.Progress = 1
	q.readyAfter = 3
	id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
	if err != nil {
		t.Fatal(err)
	}
	if job := selectiveJobDone(t, m, id); job["status"] != "completed" {
		t.Fatal(job)
	}
	q.mu.Lock()
	reads := q.fileReads
	q.mu.Unlock()
	if reads < 3 {
		t.Fatalf("imported incomplete target based on collection progress: %s", q.callLog())
	}
}

func TestDownloadSelectiveTorrentRevalidatesBeforeImport(t *testing.T) {
	m, q := newSelectiveTest(t)
	q.readyAfter, q.changeAfter = 2, 2
	id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
	if err != nil {
		t.Fatal(err)
	}
	if job := selectiveJobDone(t, m, id); job["status"] != "error" {
		t.Fatalf("changed target imported: %#v", job)
	}
	if items := m.jobs.RecentLibraryItems(10); len(items) != 0 {
		t.Fatal(items)
	}
}

func TestDownloadSelectiveTorrentRejectsNonFileAndMissingSavePath(t *testing.T) {
	for _, kind := range []string{"directory", "empty_save_path", "relative_save_path"} {
		t.Run(kind, func(t *testing.T) {
			m, q := newSelectiveTest(t)
			name := "Collection/HeartGold.nds"
			if kind == "directory" {
				name = "Collection"
				q.files[1].Name = name
			}
			if kind == "empty_save_path" {
				q.torrent.SavePath = ""
			}
			if kind == "relative_save_path" {
				q.torrent.SavePath = "."
			}
			id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, name, 3, "HeartGold", "DS", "nds", false)
			if err != nil {
				t.Fatal(err)
			}
			if job := selectiveJobDone(t, m, id); job["status"] != "error" {
				t.Fatalf("non-file target imported: %#v", job)
			}
			if items := m.jobs.RecentLibraryItems(10); len(items) != 0 {
				t.Fatal(items)
			}
		})
	}
}

func TestDownloadSelectiveTorrentSavePathSymlinkEscape(t *testing.T) {
	m, q := newSelectiveTest(t)
	outside := filepath.Join(t.TempDir(), "outside.nds")
	writeFileT(t, outside, []byte("outside"))
	link := filepath.Join(m.cfg.QBSavePath, "escape")
	if err := os.Symlink(filepath.Dir(outside), link); err != nil {
		if runtime.GOOS != "windows" {
			t.Fatal(err)
		}
		if output, err := exec.Command("cmd", "/c", "mklink", "/J", link, filepath.Dir(outside)).CombinedOutput(); err != nil {
			t.Fatalf("create junction: %v: %s", err, output)
		}
	}
	q.files[1].Name = "escape/outside.nds"
	id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "escape/outside.nds", 3, "HeartGold", "DS", "nds", false)
	if err != nil {
		t.Fatal(err)
	}
	if job := selectiveJobDone(t, m, id); job["status"] != "error" {
		t.Fatalf("SavePath escape imported: %#v", job)
	}
	if items := m.jobs.RecentLibraryItems(10); len(items) != 0 {
		t.Fatal(items)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "outside" {
		t.Fatalf("outside file modified: %q, %v", data, err)
	}
}

func minervaDownloadCounts() (int, int) {
	if h := search.GetSourceHealth("minerva"); h != nil {
		return h.DownloadOK, h.DownloadFail
	}
	return 0, 0
}

func TestDownloadSelectiveTorrentScanAndHealth(t *testing.T) {
	for _, kind := range []string{"success", "infected", "import_failure"} {
		t.Run(kind, func(t *testing.T) {
			m, q := newSelectiveTest(t)
			q.exists = true
			oldScan := selectiveScan
			var scanned []string
			selectiveScan = func(src, container, socket, docker string) (bool, []string) {
				scanned = append(scanned, src)
				if container != m.cfg.ClamAVContainer || socket != m.cfg.ClamAVSocket || docker != m.cfg.DockerSocket {
					t.Error("scan did not use configured ClamAV")
				}
				if data, err := os.ReadFile(src); err != nil || string(data) != "rom" {
					t.Errorf("scanned something other than selected file: %q, %v", data, err)
				}
				if kind == "infected" {
					return false, []string{"selected file: Eicar FOUND"}
				}
				return true, nil
			}
			t.Cleanup(func() { selectiveScan = oldScan })
			if kind == "import_failure" {
				writeFileT(t, filepath.Join(m.cfg.GamesRomsPath, "nds"), []byte("blocked directory"))
			}
			okBefore, failBefore := minervaDownloadCounts()
			id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
			if err != nil {
				t.Fatal(err)
			}
			job := selectiveJobDone(t, m, id)
			wantSuccess := kind == "success"
			if (job["status"] == "completed") != wantSuccess {
				t.Fatalf("scan/import outcome: %#v", job)
			}
			if len(scanned) != 1 || filepath.Base(scanned[0]) != "HeartGold.nds" || !strings.HasPrefix(filepath.Base(filepath.Dir(scanned[0])), ".minerva-") {
				t.Fatalf("scan targets: %v", scanned)
			}
			oks, fails := minervaDownloadCounts()
			if wantSuccess {
				if oks != okBefore+1 || fails != failBefore {
					t.Fatalf("success health delta = %d/%d", oks-okBefore, fails-failBefore)
				}
				items := m.jobs.RecentLibraryItems(10)
				if len(items) != 1 || items[0].Source != "minerva" || items[0].FileSize != 3 || items[0].PlatformSlug != "nds" || items[0].Title != "HeartGold" {
					t.Fatalf("Minerva metadata: %#v", items)
				}
				data, err := os.ReadFile(items[0].FilePath + ".gamarr.json")
				if err != nil {
					t.Fatal(err)
				}
				var sidecar map[string]interface{}
				if err := json.Unmarshal(data, &sidecar); err != nil {
					t.Fatal(err)
				}
				if sidecar["source"] != "minerva" || sidecar["title"] != "HeartGold" || sidecar["platform_slug"] != "nds" {
					t.Fatalf("sidecar: %#v", sidecar)
				}
			} else {
				if oks != okBefore || fails != failBefore+1 {
					t.Fatalf("failure health delta = %d/%d", oks-okBefore, fails-failBefore)
				}
				if items := m.jobs.RecentLibraryItems(10); len(items) != 0 {
					t.Fatalf("failed scan/import recorded success: %#v", items)
				}
			}
			if strings.Contains(q.callLog(), "delete:") {
				t.Fatalf("deleted an existing collection: %s", q.callLog())
			}
		})
	}
}

func TestDownloadSelectiveTorrentSetupFailures(t *testing.T) {
	for _, action := range []string{"add", "priority:0", "priority:7", "start", "metadata_timeout", "mismatch"} {
		t.Run(action, func(t *testing.T) {
			m, q := newSelectiveTest(t)
			q.failAction = action
			if action == "metadata_timeout" {
				selectiveMetadataTimeout = 20 * time.Millisecond
				q.emptyReads = 10000
			}
			if action == "mismatch" {
				q.files[1].Size = 42
			}
			okBefore, failBefore := minervaDownloadCounts()
			id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
			if err != nil {
				t.Fatal(err)
			}
			if job := selectiveJobDone(t, m, id); job["status"] != "error" {
				t.Fatalf("failed setup continued: %#v", job)
			}
			// Metadata timeout reports failure before its owned cleanup finishes.
			m.selectiveMu.Lock()
			m.selectiveMu.Unlock()
			calls := q.callLog()
			if action != "start" && strings.Contains(calls, "start:") {
				t.Fatalf("started despite failed setup: %s", calls)
			}
			if strings.Contains(calls, "delete:abcdef1234:true") != (action != "add") {
				t.Fatalf("incorrect created-only cleanup: %s", calls)
			}
			if items := m.jobs.RecentLibraryItems(10); len(items) != 0 {
				t.Fatal(items)
			}
			oks, fails := minervaDownloadCounts()
			if oks != okBefore || fails != failBefore+1 {
				t.Fatalf("setup failure health delta = %d/%d", oks-okBefore, fails-failBefore)
			}
		})
	}
}

func TestDownloadSelectiveTorrentImportModes(t *testing.T) {
	for _, mode := range []fileops.Mode{fileops.ModeMove, fileops.ModeCopy, fileops.ModeHardlink, fileops.ModeSymlink} {
		t.Run(string(mode), func(t *testing.T) {
			m, q := newSelectiveTest(t)
			if mode == fileops.ModeSymlink && runtime.GOOS == "windows" {
				probe := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(m.cfg.QBSavePath, probe); err != nil {
					t.Skipf("symlink privilege unavailable: %v", err)
				}
			}
			m.SaveSettings(&Settings{ImportMode: string(mode)})
			id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
			if err != nil {
				t.Fatal(err)
			}
			if job := selectiveJobDone(t, m, id); job["status"] != "completed" {
				t.Fatal(job)
			}
			src := filepath.Join(m.cfg.QBSavePath, "Collection", "HeartGold.nds")
			dest := filepath.Join(m.cfg.GamesRomsPath, "nds", "HeartGold.nds")
			if !pathExists(src) {
				t.Fatalf("mode %s removed qB's mutable source", mode)
			}
			if mode == fileops.ModeHardlink {
				a, _ := os.Stat(src)
				b, _ := os.Stat(dest)
				if os.SameFile(a, b) {
					t.Fatal("hardlink shares qB's mutable source rather than the scanned snapshot")
				}
			}
			if mode == fileops.ModeSymlink {
				info, err := os.Lstat(dest)
				if err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("not a symlink: %v", err)
				}
			}
			if !pathExists(filepath.Join(m.cfg.QBSavePath, "Collection", "Other.nds")) {
				t.Fatal("unselected source removed")
			}
			if strings.Contains(q.callLog(), "delete:") {
				t.Fatalf("removed shared collection after import: %s", q.callLog())
			}
		})
	}
}

func TestDownloadSelectiveTorrentPersistsAndRetriesSelection(t *testing.T) {
	m, q := newSelectiveTest(t)
	q.files[1].Size = 42
	id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", " ABCDEF1234 ", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
	if err != nil {
		t.Fatal(err)
	}
	job := selectiveJobDone(t, m, id)
	if job["status"] != "error" {
		t.Fatal(job)
	}
	for key, want := range map[string]interface{}{
		"source": "minerva", "download_url": "https://example.test/collection.torrent", "info_hash": "abcdef1234",
		"torrent_file_index": float64(7), "torrent_file_path": "Collection/HeartGold.nds", "torrent_file_size": float64(3),
		"title": "HeartGold", "platform": "DS", "platform_slug": "nds", "is_pc": false,
	} {
		if job[key] != want {
			t.Fatalf("persisted %s = %#v; want %#v", key, job[key], want)
		}
	}
	// Reload the persisted JSON representation: numeric fields are float64,
	// unlike the int/int64 values held immediately after the original request.
	m.jobs.Set(id, job)
	q.mu.Lock()
	q.files[1].Size = 3
	q.calls = nil
	q.mu.Unlock()
	if ok, reason := m.RetryJob(id); !ok {
		t.Fatalf("selective retry rejected: %s", reason)
	}
	job = selectiveJobDone(t, m, id)
	if job["status"] != "completed" || jobRetryCount(job) != 1 {
		t.Fatalf("retry did not complete on same row: %#v", job)
	}
	if len(m.jobs.Items()) != 1 {
		t.Fatal("retry created a duplicate job")
	}
	if calls := q.callLog(); !strings.Contains(calls, "add:true:true,files:abcdef1234,priority:abcdef1234:2|7|9:0,priority:abcdef1234:7:7,start:abcdef1234") {
		t.Fatalf("retry bypassed selective path: %s", calls)
	}
	if items := m.jobs.RecentLibraryItems(10); len(items) != 1 || items[0].FilePath != filepath.Join(m.cfg.GamesRomsPath, "nds", "HeartGold.nds") {
		t.Fatal(items)
	}
}

func TestDownloadSelectiveTorrentRetryRequiresAllFields(t *testing.T) {
	for _, key := range []string{"download_url", "info_hash", "torrent_file_index", "torrent_file_path", "torrent_file_size", "title", "platform", "platform_slug", "is_pc"} {
		t.Run(key, func(t *testing.T) {
			m, q := newSelectiveTest(t)
			q.exists = true
			q.torrent.Progress = 1
			job := map[string]interface{}{
				"status": "error", "source": "minerva", "download_url": "https://example.test/collection.torrent", "info_hash": "abcdef1234",
				"torrent_file_index": 7, "torrent_file_path": "Collection/HeartGold.nds", "torrent_file_size": int64(3),
				"title": "HeartGold", "platform": "DS", "platform_slug": "nds", "is_pc": false,
			}
			delete(job, key)
			m.jobs.Set("missing", job)
			if ok, reason := m.RetryJob("missing"); ok {
				selectiveJobDone(t, m, "missing")
				t.Fatalf("retried without %s: %s", key, reason)
			}
			if calls := q.callLog(); calls != "" {
				t.Fatalf("missing selection fell through to generic torrent: %s", calls)
			}
			if job, _ := m.jobs.Get("missing"); job["status"] != "error" {
				t.Fatalf("refusal changed failed row: %#v", job)
			}
		})
	}
}

func TestDownloadSelectiveTorrentRetryRejectsInvalidNumericFields(t *testing.T) {
	for _, value := range []interface{}{-1, 7.5, "7", nil} {
		t.Run(fmt.Sprintf("%v", value), func(t *testing.T) {
			m, q := newSelectiveTest(t)
			m.jobs.Set("invalid", map[string]interface{}{
				"status": "error", "source": "minerva", "download_url": "https://example.test/collection.torrent", "info_hash": "abcdef1234",
				"torrent_file_index": value, "torrent_file_path": "Collection/HeartGold.nds", "torrent_file_size": int64(3),
				"title": "HeartGold", "platform": "DS", "platform_slug": "nds", "is_pc": false,
			})
			if ok, reason := m.RetryJob("invalid"); ok {
				t.Fatalf("invalid index retried: %s", reason)
			}
			if calls := q.callLog(); calls != "" {
				t.Fatalf("invalid selection contacted client: %s", calls)
			}
		})
	}
}

func TestDownloadSelectiveTorrentRetryRefusesActiveWorker(t *testing.T) {
	m, q := newSelectiveTest(t)
	q.torrent.Progress = 1
	entered, release := make(chan struct{}), make(chan struct{})
	oldScan := selectiveScan
	selectiveScan = func(string, string, string, string) (bool, []string) {
		close(entered)
		<-release
		return true, nil
	}
	t.Cleanup(func() { selectiveScan = oldScan })
	id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("scan not reached")
	}
	m.jobs.Update(id, "status", "error")
	before := q.callLog()
	ok, reason := m.RetryJob(id)
	close(release)
	// Wait for the held worker even if the assertion fails, keeping test-owned
	// globals and temp files alive until it exits.
	selectiveJobDone(t, m, id)
	if ok {
		t.Fatalf("active selective worker retried: %s", reason)
	}
	if calls := q.callLog(); calls != before {
		t.Fatalf("active retry touched collection: %s", calls)
	}
}

func TestDownloadSelectiveTorrentExistingOutsideCategory(t *testing.T) {
	m, q := newSelectiveTest(t)
	q.exists, q.outsideCategory = true, true
	q.torrent.State = "downloading"
	id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
	if err != nil {
		t.Fatal(err)
	}
	job := selectiveJobDone(t, m, id)
	if calls := q.callLog(); strings.Contains(calls, "add:") || strings.Contains(calls, ":0") || strings.Contains(calls, "delete:") {
		t.Fatalf("existing torrent outside category treated as new: %s", calls)
	}
	if job["status"] != "completed" {
		t.Fatal(job)
	}
}

func TestDownloadSelectiveTorrentRecoveryPreservesSelection(t *testing.T) {
	m, q := newSelectiveTest(t)
	q.exists = true
	q.torrent.Progress = 1
	m.jobs.Set("interrupted", map[string]interface{}{
		"status": "interrupted", "source": "minerva", "download_url": "https://example.test/collection.torrent", "info_hash": "abcdef1234",
		"torrent_file_index": 7, "torrent_file_path": "Collection/HeartGold.nds", "torrent_file_size": int64(3),
		"title": "HeartGold", "platform": "DS", "platform_slug": "nds", "is_pc": false,
	})
	m.RecoverOrphanedTorrents()
	job, _ := m.jobs.Get("interrupted")
	if job["source"] != "minerva" || job["status"] != "interrupted" || job["torrent_file_index"] != 7 {
		t.Fatalf("recovery discarded selected-file identity: %#v", job)
	}
	if calls := q.callLog(); calls != "info" {
		t.Fatalf("recovery touched selective collection: %s", calls)
	}
}

func TestDownloadSelectiveTorrentRejectsIncompleteRequest(t *testing.T) {
	for _, missing := range []string{"url", "hash", "index", "size", "title", "platform", "slug"} {
		t.Run(missing, func(t *testing.T) {
			m, q := newSelectiveTest(t)
			url, hash, index, size, title, platf, slug := "https://example.test/collection.torrent", "abcdef1234", 7, int64(3), "HeartGold", "DS", "nds"
			switch missing {
			case "url":
				url = ""
			case "hash":
				hash = ""
			case "index":
				index = -1
			case "size":
				size = -1
			case "title":
				title = ""
			case "platform":
				platf = ""
			case "slug":
				slug = ""
			}
			id, err := m.DownloadSelectiveTorrent(url, hash, index, "Collection/HeartGold.nds", size, title, platf, slug, false)
			if err == nil {
				selectiveJobDone(t, m, id)
				t.Fatalf("accepted incomplete %s request", missing)
			}
			if calls := q.callLog(); calls != "" {
				t.Fatalf("invalid request touched qB: %s", calls)
			}
		})
	}
}

func TestDownloadSelectiveTorrentRequiresQBittorrent(t *testing.T) {
	m, q := newSelectiveTest(t)
	m.cfg.QBURL = ""
	id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
	if err != nil {
		return
	}
	if job := selectiveJobDone(t, m, id); job["status"] != "error" {
		t.Fatalf("download accepted without configured qB: %#v", job)
	}
	if calls := q.callLog(); calls != "" {
		t.Fatalf("unconfigured qB used: %s", calls)
	}
}

func TestDownloadSelectiveTorrentImportRefusesOccupiedDestination(t *testing.T) {
	m, q := newSelectiveTest(t)
	dest := filepath.Join(m.cfg.GamesRomsPath, "nds", "HeartGold.nds")
	writeFileT(t, dest, []byte("existing library ROM"))
	id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
	if err != nil {
		t.Fatal(err)
	}
	if job := selectiveJobDone(t, m, id); job["status"] != "error" {
		t.Fatalf("occupied destination overwritten: %#v", job)
	}
	if data, err := os.ReadFile(dest); err != nil || string(data) != "existing library ROM" {
		t.Fatalf("existing ROM modified: %q, %v", data, err)
	}
	if items := m.jobs.RecentLibraryItems(10); len(items) != 0 {
		t.Fatal(items)
	}
	if strings.Contains(q.callLog(), "delete:") {
		t.Fatalf("deleted shared collection: %s", q.callLog())
	}
}

func TestDownloadSelectiveTorrentImportFailureClearsOwnPartial(t *testing.T) {
	m, _ := newSelectiveTest(t)
	dest := filepath.Join(m.cfg.GamesRomsPath, "nds", "HeartGold.nds")
	oldImport := fileImport
	fileImport = func(src, target string, opt fileops.Options) error {
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte("partial"), 0644); err != nil {
			return err
		}
		return errors.New("simulated disk write failure")
	}
	t.Cleanup(func() { fileImport = oldImport })
	id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
	if err != nil {
		t.Fatal(err)
	}
	if job := selectiveJobDone(t, m, id); job["status"] != "error" {
		t.Fatal(job)
	}
	if destPresent(dest) {
		t.Fatal("failed import left its own partial destination")
	}
	fileImport = oldImport
	if ok, reason := m.RetryJob(id); !ok {
		t.Fatal(reason)
	}
	if job := selectiveJobDone(t, m, id); job["status"] != "completed" {
		t.Fatalf("partial debris blocked retry: %#v", job)
	}
}

func TestDownloadSelectiveTorrentSnapshotSurvivesSourceReplacement(t *testing.T) {
	for _, replacement := range []string{"in_place", "ancestor_junction"} {
		t.Run(replacement, func(t *testing.T) {
			m, _ := newSelectiveTest(t)
			original := filepath.Join(m.cfg.QBSavePath, "Collection", "HeartGold.nds")
			outside := t.TempDir()
			writeFileT(t, filepath.Join(outside, "HeartGold.nds"), []byte("unscanned outside bytes"))
			oldScan := selectiveScan
			selectiveScan = func(src, _, _, _ string) (bool, []string) {
				data, err := os.ReadFile(src)
				if err != nil || string(data) != "rom" {
					t.Errorf("scanner received %q, %v", data, err)
				}
				if replacement == "in_place" {
					if err := os.WriteFile(original, []byte("unscanned replacement"), 0644); err != nil {
						t.Error(err)
					}
				} else {
					collection := filepath.Dir(original)
					if err := os.Rename(collection, collection+"-saved"); err != nil {
						t.Error(err)
						return false, []string{err.Error()}
					}
					if err := os.Symlink(outside, collection); err != nil {
						if runtime.GOOS != "windows" {
							t.Error(err)
							return false, []string{err.Error()}
						}
						if out, err := exec.Command("cmd", "/c", "mklink", "/J", collection, outside).CombinedOutput(); err != nil {
							t.Errorf("junction: %v: %s", err, out)
							return false, []string{err.Error()}
						}
					}
				}
				return true, nil
			}
			t.Cleanup(func() { selectiveScan = oldScan })
			id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
			if err != nil {
				t.Fatal(err)
			}
			if job := selectiveJobDone(t, m, id); job["status"] != "completed" {
				t.Fatal(job)
			}
			data, err := os.ReadFile(filepath.Join(m.cfg.GamesRomsPath, "nds", "HeartGold.nds"))
			if err != nil || string(data) != "rom" {
				t.Fatalf("imported bytes differ from scanned bytes: %q, %v", data, err)
			}
		})
	}
}

func TestDownloadSelectiveTorrentRejectsRelativeLeafSymlink(t *testing.T) {
	m, q := newSelectiveTest(t)
	m.cfg.ImportMode = fileops.ModeMove
	leaf := filepath.Join(m.cfg.QBSavePath, "Collection", "Selected.nds")
	if err := os.Symlink("HeartGold.nds", leaf); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("file symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
	q.files[1].Name = "Collection/Selected.nds"
	id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/Selected.nds", 3, "Selected", "DS", "nds", false)
	if err != nil {
		t.Fatal(err)
	}
	if job := selectiveJobDone(t, m, id); job["status"] != "error" {
		t.Fatalf("accepted relative leaf symlink for move import: %#v", job)
	}
	if items := m.jobs.RecentLibraryItems(10); len(items) != 0 {
		t.Fatal(items)
	}
	if data, err := os.ReadFile(leaf); err != nil || string(data) != "rom" {
		t.Fatalf("symlink referent changed: %q, %v", data, err)
	}
}

func TestDownloadSelectiveTorrentMetadataDeadlineRejectsLateResponse(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_%v", existing), func(t *testing.T) {
			m, q := newSelectiveTest(t)
			selectiveMetadataTimeout = 20 * time.Millisecond
			q.exists = existing
			q.filesEntered, q.filesRelease = make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(q.filesRelease) }) }
			t.Cleanup(release)
			id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-q.filesEntered:
			case <-time.After(time.Second):
				t.Fatal("metadata request not reached")
			}
			returned := false
			deadline := time.Now().Add(200 * time.Millisecond)
			for time.Now().Before(deadline) {
				job, _ := m.jobs.Get(id)
				if job["status"] == "error" {
					returned = true
					break
				}
				time.Sleep(time.Millisecond)
			}
			release()
			job := selectiveJobDone(t, m, id)
			if !returned {
				t.Error("metadata timeout waited for the blocked HTTP response")
			}
			if job["status"] != "error" {
				t.Errorf("late metadata accepted: %#v", job)
			}
			// Synchronize with any created-only timeout cleanup before inspecting
			// calls or allowing test-owned fixture state to be destroyed.
			m.selectiveMu.Lock()
			m.selectiveMu.Unlock()
			if calls := q.callLog(); strings.Contains(calls, "priority:") || strings.Contains(calls, "start:") {
				t.Errorf("late response changed selection: %s", calls)
			}
			if existing && strings.Contains(q.callLog(), "delete:") {
				t.Fatal("timeout deleted existing collection")
			}
			if !existing && !strings.Contains(q.callLog(), "delete:abcdef1234:true") {
				t.Fatal("timeout did not clean up the newly created collection")
			}
		})
	}
}

func TestDownloadSelectiveTorrentSnapshotLifecycle(t *testing.T) {
	for _, kind := range []string{"move", "copy", "hardlink", "symlink", "infected", "import_failure"} {
		t.Run(kind, func(t *testing.T) {
			m, _ := newSelectiveTest(t)
			mode := fileops.Mode(kind)
			if !mode.Valid() {
				mode = fileops.ModeCopy
			}
			m.cfg.ImportMode = mode
			if mode == fileops.ModeSymlink && runtime.GOOS == "windows" {
				if err := os.Symlink(m.cfg.QBSavePath, filepath.Join(t.TempDir(), "probe")); err != nil {
					t.Skipf("symlink privilege unavailable: %v", err)
				}
			}
			if kind == "import_failure" {
				writeFileT(t, filepath.Join(m.cfg.GamesRomsPath, "nds"), []byte("block"))
			}
			var scanned string
			var scannedInfo os.FileInfo
			oldScan := selectiveScan
			selectiveScan = func(src, _, _, _ string) (bool, []string) {
				scanned = src
				file, err := os.Open(src)
				if err != nil {
					t.Error(err)
					return false, []string{err.Error()}
				}
				scannedInfo, _ = file.Stat()
				file.Close()
				if kind == "infected" {
					return false, []string{"infected"}
				}
				return true, nil
			}
			t.Cleanup(func() { selectiveScan = oldScan })
			id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
			if err != nil {
				t.Fatal(err)
			}
			job := selectiveJobDone(t, m, id)
			success := kind != "infected" && kind != "import_failure"
			if (job["status"] == "completed") != success {
				t.Fatalf("snapshot import result: %#v", job)
			}
			rel, err := filepath.Rel(m.cfg.GamesRomsPath, scanned)
			if err != nil || !filepath.IsLocal(rel) || !strings.HasPrefix(rel, ".minerva-") {
				t.Fatalf("scan did not use protected library snapshot: %q", scanned)
			}
			if destPresent(filepath.Dir(scanned)) != (mode == fileops.ModeSymlink && success) {
				t.Fatalf("incorrect staging lifetime for %s: %s", kind, scanned)
			}
			if !success {
				return
			}
			dest := filepath.Join(m.cfg.GamesRomsPath, "nds", "HeartGold.nds")
			info, err := os.Stat(dest)
			if err != nil {
				t.Fatal(err)
			}
			if mode == fileops.ModeHardlink && !os.SameFile(scannedInfo, info) {
				t.Fatal("library is not a hardlink to the scanned snapshot")
			}
			original := filepath.Join(m.cfg.QBSavePath, "Collection", "HeartGold.nds")
			if !pathExists(original) {
				t.Fatalf("mode %s removed qB's mutable source", mode)
			}
			writeFileT(t, original, []byte("later qB change"))
			if data, err := os.ReadFile(dest); err != nil || string(data) != "rom" {
				t.Fatalf("library changed after qB source update: %q, %v", data, err)
			}
		})
	}
}

func TestDownloadSelectiveTorrentMoveRetainsMutablePayload(t *testing.T) {
	for _, mutation := range []string{"unchanged", "in_place", "leaf_replacement", "ancestor_replacement"} {
		t.Run(mutation, func(t *testing.T) {
			m, q := newSelectiveTest(t)
			m.cfg.ImportMode = fileops.ModeMove
			original := filepath.Join(m.cfg.QBSavePath, "Collection", "HeartGold.nds")
			want := "rom"
			if mutation != "unchanged" {
				want = "new qB payload"
			}
			oldScan := selectiveScan
			selectiveScan = func(src, _, _, _ string) (bool, []string) {
				if data, err := os.ReadFile(src); err != nil || string(data) != "rom" {
					t.Errorf("snapshot contents: %q, %v", data, err)
				}
				switch mutation {
				case "leaf_replacement":
					if err := os.Rename(original, original+"-saved"); err != nil {
						t.Error(err)
						return false, []string{err.Error()}
					}
				case "ancestor_replacement":
					ancestor := filepath.Dir(original)
					if err := os.Rename(ancestor, ancestor+"-saved"); err != nil {
						t.Error(err)
						return false, []string{err.Error()}
					}
					if err := os.Mkdir(ancestor, 0755); err != nil {
						t.Error(err)
						return false, []string{err.Error()}
					}
				}
				if mutation != "unchanged" {
					if err := os.WriteFile(original, []byte(want), 0644); err != nil {
						t.Error(err)
						return false, []string{err.Error()}
					}
				}
				return true, nil
			}
			t.Cleanup(func() { selectiveScan = oldScan })
			id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
			if err != nil {
				t.Fatal(err)
			}
			if job := selectiveJobDone(t, m, id); job["status"] != "completed" {
				t.Fatal(job)
			}
			if data, err := os.ReadFile(original); err != nil || string(data) != want {
				t.Fatalf("move removed or changed qB payload: %q, %v", data, err)
			}
			if data, err := os.ReadFile(filepath.Join(m.cfg.GamesRomsPath, "nds", "HeartGold.nds")); err != nil || string(data) != "rom" {
				t.Fatalf("published unscanned bytes: %q, %v", data, err)
			}
			if strings.Contains(q.callLog(), "delete:") {
				t.Fatal("move deleted the shared collection")
			}
		})
	}
}

func TestDownloadSelectiveTorrentPublicationPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits and symlink traversal require a Unix host")
	}
	for _, mode := range []fileops.Mode{fileops.ModeMove, fileops.ModeCopy, fileops.ModeHardlink, fileops.ModeSymlink} {
		for _, permission := range []os.FileMode{0644, 0754} {
			t.Run(fmt.Sprintf("%s_%o", mode, permission), func(t *testing.T) {
				m, _ := newSelectiveTest(t)
				m.cfg.ImportMode = mode
				original := filepath.Join(m.cfg.QBSavePath, "Collection", "HeartGold.nds")
				if err := os.Chmod(original, permission); err != nil {
					t.Fatal(err)
				}
				var scanned string
				oldScan := selectiveScan
				selectiveScan = func(src, _, _, _ string) (bool, []string) {
					scanned = src
					info, err := os.Stat(src)
					if err != nil {
						t.Error(err)
						return false, []string{err.Error()}
					}
					if info.Mode().Perm() != permission {
						t.Errorf("snapshot mode = %o, want original %o", info.Mode().Perm(), permission)
					}
					parent, err := os.Stat(filepath.Dir(src))
					if err != nil {
						t.Error(err)
						return false, []string{err.Error()}
					}
					if parent.Mode().Perm() != 0700 {
						t.Errorf("snapshot parent not private during scan: %o", parent.Mode().Perm())
					}
					return true, nil
				}
				t.Cleanup(func() { selectiveScan = oldScan })
				id, err := m.DownloadSelectiveTorrent("https://example.test/collection.torrent", "abcdef1234", 7, "Collection/HeartGold.nds", 3, "HeartGold", "DS", "nds", false)
				if err != nil {
					t.Fatal(err)
				}
				if job := selectiveJobDone(t, m, id); job["status"] != "completed" {
					t.Fatal(job)
				}
				dest := filepath.Join(m.cfg.GamesRomsPath, "nds", "HeartGold.nds")
				info, err := os.Stat(dest)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != permission {
					t.Errorf("published mode = %o, want original %o", info.Mode().Perm(), permission)
				}
				if mode == fileops.ModeSymlink {
					parent, err := os.Stat(filepath.Dir(scanned))
					if err != nil {
						t.Fatal(err)
					}
					if parent.Mode().Perm() != 0755 {
						t.Errorf("symlink backing directory inaccessible to library readers: %o", parent.Mode().Perm())
					}
					if data, err := os.ReadFile(dest); err != nil || string(data) != "rom" {
						t.Fatalf("symlink backing unavailable: %q, %v", data, err)
					}
				} else if destPresent(filepath.Dir(scanned)) {
					t.Fatal("non-symlink publication left staging behind")
				}
			})
		}
	}
}

func TestNewJobID(t *testing.T) {
	seen := map[string]bool{}
	hexRe := regexp.MustCompile(`^[0-9a-f]{8}$`)
	for i := 0; i < 100; i++ {
		id := newJobID()
		if !hexRe.MatchString(id) {
			t.Fatalf("newJobID() = %q, want 8 hex chars", id)
		}
		if seen[id] {
			t.Fatalf("duplicate job ID %q", id)
		}
		seen[id] = true
	}
}

func TestNewManager(t *testing.T) {
	t.Run("no optional clients", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		m := New(cfg, jobs, nil)
		if m.Transmission() != nil {
			t.Error("Transmission client should be nil when not configured")
		}
		if m.Deluge() != nil {
			t.Error("Deluge client should be nil when not configured")
		}
		if m.Jobs() != jobs {
			t.Error("Jobs() should return the injected store")
		}
	})

	t.Run("all clients configured", func(t *testing.T) {
		cfg := newTestConfig(t)
		cfg.TransmissionURL = "http://127.0.0.1:1"
		cfg.DelugeURL = "http://127.0.0.1:1"
		qb := qbit.New("http://127.0.0.1:1", "u", "p")
		m := New(cfg, newTestJobs(t), qb)
		if m.Transmission() == nil {
			t.Error("Transmission client should be initialized")
		}
		if m.Deluge() == nil {
			t.Error("Deluge client should be initialized")
		}
		if m.QB() != qb {
			t.Error("QB() should return the injected client")
		}
	})
}

func TestDownloadTorrentValidation(t *testing.T) {
	cfg := newTestConfig(t)
	m := New(cfg, newTestJobs(t), nil)
	if _, err := m.DownloadTorrent("", "", "Title", "PC", "", true); err == nil {
		t.Fatal("empty URL should return an error")
	}
}

func TestDownloadTorrentNoClientAvailable(t *testing.T) {
	// No qBittorrent, Transmission, or Deluge configured: the job errors.
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)

	jobID, err := m.DownloadTorrent("magnet:x", "", "Some Game", "PC", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	job, ok := jobs.Get(jobID)
	if !ok {
		t.Fatal("job not created")
	}
	if status, _ := job["status"].(string); status != "error" {
		t.Errorf("status = %q, want error", status)
	}
	if errMsg, _ := job["error"].(string); !strings.Contains(errMsg, "any download client") {
		t.Errorf("error = %q, want failed-to-add message", errMsg)
	}
}

func TestDownloadTorrentQBitFullFlow(t *testing.T) {
	// End-to-end: add via qBittorrent, file-list scan passes, ClamAV
	// unavailable (skipped), content organized into the ROM library,
	// torrent deleted.
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	cfg.QBURL = "configured"

	content := filepath.Join(t.TempDir(), "Super Game (USA)")
	writeFileT(t, filepath.Join(content, "game.sfc"), []byte("rom-data"))

	qm := newQbitMock(t)
	qm.setFiles([]qbit.TorrentFile{{Name: "Super Game (USA)/game.sfc"}})
	qm.setTorrents([]qbit.Torrent{{
		Name:        "Super Game (USA)",
		Hash:        "hash-full-flow",
		Progress:    1.0,
		ContentPath: content,
	}})

	m := New(cfg, jobs, qm.client())
	jobID, err := m.DownloadTorrent("magnet:x", "", "Super Game (USA)", "SNES", "snes", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	waitFor(t, 10*time.Second, "torrent deletion after organize", func() bool {
		return len(qm.deletedHashes()) > 0
	})

	job := waitJobStatus(t, jobs, jobID, "completed", 5*time.Second)
	if detail, _ := job["detail"].(string); !strings.Contains(detail, "RomM (SNES)") {
		t.Errorf("detail = %q, want RomM (SNES)", detail)
	}

	dest := filepath.Join(cfg.GamesRomsPath, "snes", "Super Game (USA)")
	if !pathExists(filepath.Join(dest, "game.sfc")) {
		t.Errorf("game file not moved to %s", dest)
	}
	if !pathExists(filepath.Join(dest, ".gamarr.json")) {
		t.Error("metadata sidecar not written")
	}
	if pathExists(content) {
		t.Error("source content should be removed after move")
	}
	if !jobs.LibraryHasSourceID("torrent:hash-full-flow") {
		t.Error("library item not tracked")
	}
	if got := qm.deletedHashes(); got[0] != "hash-full-flow" {
		t.Errorf("deleted hash = %q, want hash-full-flow", got[0])
	}
}

func TestDownloadTorrentTracksRenamedTorrentByHash(t *testing.T) {
	// The release title we grabbed and the name the tracker gives the torrent
	// need not contain one another, so titlesMatch alone loses the download.
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	cfg.QBURL = "configured"

	content := filepath.Join(t.TempDir(), "Chrono Quest")
	writeFileT(t, filepath.Join(content, "game.sfc"), []byte("rom-data"))

	qm := newQbitMock(t)
	qm.setFiles([]qbit.TorrentFile{{Name: "Chrono Quest/game.sfc"}})
	qm.setTorrents([]qbit.Torrent{{
		Name:        "Chrono Quest [Repack]",
		Hash:        "hash-renamed",
		Progress:    1.0,
		ContentPath: content,
	}})

	title := "Chrono Quest (v1.2 + Bonus OST, MULTi9) [Repack]"
	if titlesMatch(title, "Chrono Quest [Repack]") {
		t.Fatal("fixture no longer exercises a title mismatch")
	}

	m := New(cfg, jobs, qm.client())
	jobID, err := m.DownloadTorrent("magnet:x", "hash-renamed", title, "SNES", "snes", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	job := waitJobStatus(t, jobs, jobID, "completed", 10*time.Second)
	if got, _ := job["info_hash"].(string); got != "hash-renamed" {
		t.Errorf("job info_hash = %q, want hash-renamed", got)
	}
	// The import writes the completed status before it tracks the library item,
	// so reaching that status does not mean the row exists yet.
	waitFor(t, minPollTimeout, "the library item to be tracked", func() bool {
		return jobs.LibraryHasSourceID("torrent:hash-renamed")
	})
}

func TestDownloadTorrentBlocksDangerousFiles(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	cfg.QBURL = "configured"

	qm := newQbitMock(t)
	qm.setFiles([]qbit.TorrentFile{{Name: "Game/keygen.bat"}, {Name: "Game/setup.scr"}})
	qm.setTorrents([]qbit.Torrent{{
		Name:     "Evil Game",
		Hash:     "hash-evil",
		Progress: 0.5, // metadata available, still downloading
	}})

	m := New(cfg, jobs, qm.client())
	jobID, err := m.DownloadTorrent("magnet:x", "", "Evil Game", "PC", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	waitFor(t, 10*time.Second, "dangerous torrent stop", func() bool {
		return len(qm.stoppedHashes()) > 0
	})
	// Plenty of legitimate repacks ship a .bat next to their checksum tooling,
	// so the download has to survive for the operator to judge it.
	if calls := qm.deleteCalls(); len(calls) != 0 {
		t.Errorf("download was deleted (%+v); it must be left in place for review", calls)
	}
	job := waitJobStatus(t, jobs, jobID, "error", 5*time.Second)
	if errMsg, _ := job["error"].(string); !strings.Contains(errMsg, "keygen.bat") {
		t.Errorf("error = %q, want the offending filename in it", errMsg)
	}
}

func TestDownloadTorrentFallbacks(t *testing.T) {
	newFallbackQbit := func(t *testing.T) *qbitMock {
		qm := newQbitMock(t)
		qm.mu.Lock()
		qm.addOK = false // qBittorrent add always fails
		qm.mu.Unlock()
		return qm
	}

	t.Run("transmission used when qbit fails", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		cfg.QBURL = "configured"
		qm := newFallbackQbit(t)

		trSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"result":"success","arguments":{"torrent-added":{"id":1}}}`)
		}))
		defer trSrv.Close()
		cfg.TransmissionURL = trSrv.URL

		m := New(cfg, jobs, qm.client())
		jobID, err := m.DownloadTorrent("magnet:x", "", "Fallback Game", "PC", "", true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		job, _ := jobFromDB(t, jobs, jobID)
		if detail, _ := job["detail"].(string); !strings.Contains(detail, "Transmission") {
			t.Errorf("detail = %q, want Transmission", detail)
		}
	})

	t.Run("deluge used when qbit and transmission fail", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		cfg.QBURL = "configured"
		qm := newFallbackQbit(t)

		trSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"result":"duplicate torrent"}`)
		}))
		defer trSrv.Close()
		cfg.TransmissionURL = trSrv.URL

		dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"id":1,"result":"deluge-hash","error":null}`)
		}))
		defer dlSrv.Close()
		cfg.DelugeURL = dlSrv.URL

		m := New(cfg, jobs, qm.client())
		jobID, err := m.DownloadTorrent("magnet:x", "", "Fallback Game 2", "PC", "", true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		job, _ := jobFromDB(t, jobs, jobID)
		if detail, _ := job["detail"].(string); !strings.Contains(detail, "Deluge") {
			t.Errorf("detail = %q, want Deluge", detail)
		}
	})

	t.Run("all clients fail", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		cfg.QBURL = "configured"
		qm := newFallbackQbit(t)

		deadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		deadSrv.Close()
		cfg.TransmissionURL = deadSrv.URL
		cfg.DelugeURL = deadSrv.URL

		m := New(cfg, jobs, qm.client())
		jobID, err := m.DownloadTorrent("magnet:x", "", "Doomed Game", "PC", "", true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		job, _ := jobs.Get(jobID)
		if status, _ := job["status"].(string); status != "error" {
			t.Errorf("status = %q, want error", status)
		}
	})
}

func TestOrganizeTorrent(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		cfg := newTestConfig(t)
		qm := newQbitMock(t)
		m := New(cfg, newTestJobs(t), qm.client())
		if _, err := m.OrganizeTorrent("missing-hash", "PC", "", true); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("err = %v, want not found", err)
		}
	})

	t.Run("not complete", func(t *testing.T) {
		cfg := newTestConfig(t)
		qm := newQbitMock(t)
		qm.setTorrents([]qbit.Torrent{{Name: "G", Hash: "h1", Progress: 0.4}})
		m := New(cfg, newTestJobs(t), qm.client())
		if _, err := m.OrganizeTorrent("h1", "PC", "", true); err == nil || !strings.Contains(err.Error(), "not yet complete") {
			t.Fatalf("err = %v, want not yet complete", err)
		}
	})

	t.Run("organizes completed PC torrent", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		content := filepath.Join(t.TempDir(), "Cool.Game-FitGirl")
		writeFileT(t, filepath.Join(content, "setup.exe"), []byte("installer"))

		qm := newQbitMock(t)
		qm.setTorrents([]qbit.Torrent{{
			Name: "Cool.Game-FitGirl", Hash: "h2", Progress: 1.0, ContentPath: content,
		}})
		m := New(cfg, jobs, qm.client())

		jobID, err := m.OrganizeTorrent("h2", "PC", "", true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		waitFor(t, 10*time.Second, "torrent deletion", func() bool {
			return len(qm.deletedHashes()) > 0
		})
		job := waitJobStatus(t, jobs, jobID, "completed", 5*time.Second)
		if detail, _ := job["detail"].(string); !strings.Contains(detail, "GameVault") {
			t.Errorf("detail = %q, want GameVault", detail)
		}
		if !pathExists(filepath.Join(cfg.GamesVaultPath, "Cool.Game-FitGirl", "setup.exe")) {
			t.Error("game not moved to vault")
		}
	})
}

// setupOrganizeJob creates a manager plus a pre-seeded organizing job.
func setupOrganizeJob(t *testing.T) (*Manager, *qbitMock, string) {
	t.Helper()
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	m := New(cfg, jobs, qm.client())
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{
		"status": "organizing", "title": "T", "error": nil, "detail": "",
	})
	return m, qm, jobID
}

func TestOrganizeGame(t *testing.T) {
	t.Run("missing content path errors", func(t *testing.T) {
		m, _, jobID := setupOrganizeJob(t)
		torrent := &qbit.Torrent{Name: "Ghost", Hash: "gh", ContentPath: "/nonexistent/nope"}
		m.organizeGame(jobID, torrent, "PC", "", true, 1)
		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "error" {
			t.Errorf("status = %q, want error", status)
		}
	})

	t.Run("unknown platform left in staging", func(t *testing.T) {
		m, qm, jobID := setupOrganizeJob(t)
		content := filepath.Join(t.TempDir(), "mysterious-thing")
		writeFileT(t, filepath.Join(content, "data.dat"), []byte("???"))
		torrent := &qbit.Torrent{Name: "mysterious-thing", Hash: "mh", ContentPath: content}

		m.organizeGame(jobID, torrent, "", "", false, 1)

		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "completed" {
			t.Errorf("status = %q, want completed", status)
		}
		if detail, _ := job["detail"].(string); !strings.Contains(detail, "unknown platform") {
			t.Errorf("detail = %q, want unknown platform", detail)
		}
		if !pathExists(content) {
			t.Error("content should stay in staging")
		}
		if len(qm.deletedHashes()) != 0 {
			t.Error("torrent must not be deleted for unknown platform")
		}
	})

	t.Run("platform detected from file extension", func(t *testing.T) {
		m, _, jobID := setupOrganizeJob(t)
		content := filepath.Join(t.TempDir(), "handheld-game")
		writeFileT(t, filepath.Join(content, "game.gba"), []byte("gba-rom"))
		torrent := &qbit.Torrent{Name: "handheld-game", Hash: "dh", ContentPath: content}

		m.organizeGame(jobID, torrent, "", "", false, 1)

		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "completed" {
			t.Fatalf("status = %q, want completed (job=%v)", status, job)
		}
		if slug, _ := job["platform_slug"].(string); slug != "gba" {
			t.Errorf("platform_slug = %q, want gba", slug)
		}
		if !pathExists(filepath.Join(m.cfg.GamesRomsPath, "gba", "handheld-game", "game.gba")) {
			t.Error("ROM not moved to gba library dir")
		}
	})

	t.Run("platform detected from metadata.json", func(t *testing.T) {
		m, _, jobID := setupOrganizeJob(t)
		content := filepath.Join(t.TempDir(), "meta-game")
		writeFileT(t, filepath.Join(content, "metadata.json"), []byte(`{"platform":"snes"}`))
		writeFileT(t, filepath.Join(content, "game.bin2"), []byte("rom"))
		torrent := &qbit.Torrent{Name: "meta-game", Hash: "mm", ContentPath: content}

		m.organizeGame(jobID, torrent, "", "", false, 1)

		job, _ := m.Jobs().Get(jobID)
		if slug, _ := job["platform_slug"].(string); slug != "snes" {
			t.Errorf("platform_slug = %q, want snes", slug)
		}
	})

	t.Run("falls back to save path when content path empty", func(t *testing.T) {
		m, _, jobID := setupOrganizeJob(t)
		savePath := t.TempDir()
		writeFileT(t, filepath.Join(savePath, "SavedGame", "rom.sfc"), []byte("rom"))
		torrent := &qbit.Torrent{Name: "SavedGame", Hash: "sp", SavePath: savePath}

		m.organizeGame(jobID, torrent, "SNES", "snes", false, 1)

		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "completed" {
			t.Errorf("status = %q, want completed", status)
		}
		if !pathExists(filepath.Join(m.cfg.GamesRomsPath, "snes", "SavedGame", "rom.sfc")) {
			t.Error("ROM not moved from save path")
		}
	})

	t.Run("move failure sets error", func(t *testing.T) {
		m, _, jobID := setupOrganizeJob(t)
		// Make the vault path a regular file so MkdirAll/copy fails.
		os.RemoveAll(m.cfg.GamesVaultPath)
		writeFileT(t, m.cfg.GamesVaultPath, []byte("not a dir"))

		content := filepath.Join(t.TempDir(), "Blocked.Game-CODEX")
		writeFileT(t, filepath.Join(content, "setup.exe"), []byte("x"))
		torrent := &qbit.Torrent{Name: "Blocked.Game-CODEX", Hash: "bf", ContentPath: content}

		m.organizeGame(jobID, torrent, "PC", "", true, 1)

		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "error" {
			t.Errorf("status = %q, want error", status)
		}
		if errMsg, _ := job["error"].(string); !strings.Contains(errMsg, "Organize failed") {
			t.Errorf("error = %q, want Organize failed", errMsg)
		}
	})
}

func TestDownloadDDL(t *testing.T) {
	t.Run("full flow with content-disposition filename", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Disposition", `attachment; filename="Mario World.sfc"`)
			w.Write([]byte("rom-bytes"))
		}))
		defer srv.Close()

		m := New(cfg, jobs, nil)
		jobID := m.DownloadDDL(srv.URL+"/dl", "", "Mario World", "SNES", "snes", false)

		waitFor(t, 10*time.Second, "library tracking", func() bool {
			return jobs.LibraryHasSourceID("ddl:" + filepath.Join(cfg.GamesRomsPath, "snes", "Mario World.sfc"))
		})
		job := waitJobStatus(t, jobs, jobID, "completed", 5*time.Second)
		if detail, _ := job["detail"].(string); !strings.Contains(detail, "RomM (SNES)") {
			t.Errorf("detail = %q, want RomM (SNES)", detail)
		}
		dest := filepath.Join(cfg.GamesRomsPath, "snes", "Mario World.sfc")
		data, err := os.ReadFile(dest)
		if err != nil || string(data) != "rom-bytes" {
			t.Errorf("dest content = %q err=%v, want rom-bytes", data, err)
		}
		if !pathExists(dest + ".gamarr.json") {
			t.Error("sidecar not written for file dest")
		}
	})

	t.Run("http error fails the job with cause", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		m := New(cfg, jobs, nil)
		jobID := m.DownloadDDL(srv.URL+"/gone", "", "Missing Game", "PC", "", true)
		job := waitJobStatus(t, jobs, jobID, "error", 5*time.Second)
		errMsg, _ := job["error"].(string)
		if !strings.Contains(errMsg, "Download failed") {
			t.Errorf("error = %q, want Download failed", errMsg)
		}
		if !strings.Contains(errMsg, "404") {
			t.Errorf("error = %q, want HTTP 404 cause included", errMsg)
		}
	})

	t.Run("uncreatable staging dir fails the job with cause", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		// A regular file where the staging dir's parent should be makes
		// os.MkdirAll fail with ENOTDIR.
		blocker := filepath.Join(t.TempDir(), "blocker")
		writeFileT(t, blocker, []byte("not a dir"))
		cfg.QBSavePath = filepath.Join(blocker, "staging")

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("rom-bytes"))
		}))
		defer srv.Close()

		m := New(cfg, jobs, nil)
		jobID := m.DownloadDDL(srv.URL+"/dl", "", "Blocked Game", "PC", "", true)
		job := waitJobStatus(t, jobs, jobID, "error", 5*time.Second)
		errMsg, _ := job["error"].(string)
		if !strings.Contains(errMsg, "cannot create staging dir") {
			t.Errorf("error = %q, want 'cannot create staging dir' cause", errMsg)
		}
		if !strings.Contains(errMsg, cfg.QBSavePath) {
			t.Errorf("error = %q, want staging path %q included", errMsg, cfg.QBSavePath)
		}
	})

	t.Run("no url and no vimm id fails", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		m := New(cfg, jobs, nil)
		jobID := m.DownloadDDL("", "", "Nothing", "PC", "", true)
		waitJobStatus(t, jobs, jobID, "error", 5*time.Second)
	})
}

func TestDownloadDDLFilenameFromURL(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data")) // no Content-Disposition
	}))
	defer srv.Close()

	m := New(cfg, jobs, nil)
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	got, err := m.downloadDDL(srv.URL+"/files/zelda.gba?token=1", cfg.QBSavePath, jobID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(got) != "zelda.gba" {
		t.Errorf("filename = %q, want zelda.gba", filepath.Base(got))
	}
	if !pathExists(got) {
		t.Error("downloaded file missing")
	}
}

func TestDownloadDDLCreateFileFailure(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data"))
	}))
	defer srv.Close()

	m := New(cfg, jobs, nil)
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	// Destination dir does not exist, so os.Create fails.
	dest := filepath.Join(t.TempDir(), "no-such-dir")
	got, err := m.downloadDDL(srv.URL+"/files/game.bin", dest, jobID)
	if got != "" {
		t.Errorf("path = %q, want empty on create failure", got)
	}
	if err == nil || !strings.Contains(err.Error(), "cannot create file") {
		t.Errorf("err = %v, want 'cannot create file' cause", err)
	}
}

// TestDownloadDDLTruncatedDownloadIsError verifies that a server which declares
// a Content-Length but drops the connection early does not leave a partial file
// reported as a finished download. Without the guard the truncated archive would
// pass to the scan/organize pipeline as complete.
func TestDownloadDDLTruncatedDownloadIsError(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="Truncated.sfc"`)
		w.Header().Set("Content-Length", "1000") // claim far more than we send
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("only-a-few-bytes"))
		// Return without sending the rest; the client sees an unexpected EOF.
	}))
	defer srv.Close()

	m := New(cfg, jobs, nil)
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	dest := t.TempDir()
	got, err := m.downloadDDL(srv.URL+"/dl", dest, jobID)
	if err == nil {
		t.Fatal("expected an error for a truncated download, got nil")
	}
	if got != "" {
		t.Errorf("path = %q, want empty on truncated download", got)
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 0 {
		t.Errorf("partial file left behind: %v", entries)
	}
}

func TestOrganizeDDLFile(t *testing.T) {
	newFixture := func(t *testing.T) (*Manager, string, string) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		m := New(cfg, jobs, nil)
		jobID := newJobID()
		jobs.Set(jobID, map[string]interface{}{"status": "organizing", "error": nil})
		src := filepath.Join(t.TempDir(), "game-file.bin")
		writeFileT(t, src, []byte("payload"))
		return m, jobID, src
	}

	t.Run("pc file goes to vault", func(t *testing.T) {
		m, jobID, src := newFixture(t)
		m.organizeDDLFile(jobID, src, "Great Game", "PC", "", true)
		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "completed" {
			t.Fatalf("status = %q, want completed", status)
		}
		if !pathExists(filepath.Join(m.cfg.GamesVaultPath, "game-file.bin")) {
			t.Error("file not moved to vault")
		}
		if !m.Jobs().LibraryHasSourceID("ddl:" + filepath.Join(m.cfg.GamesVaultPath, "game-file.bin")) {
			t.Error("library item not tracked")
		}
	})

	t.Run("rom goes to platform dir", func(t *testing.T) {
		m, jobID, src := newFixture(t)
		m.organizeDDLFile(jobID, src, "Great Game", "PSP", "psp", false)
		if !pathExists(filepath.Join(m.cfg.GamesRomsPath, "psp", "game-file.bin")) {
			t.Error("file not moved to psp dir")
		}
	})

	t.Run("unknown platform left in staging", func(t *testing.T) {
		m, jobID, src := newFixture(t)
		m.organizeDDLFile(jobID, src, "Great Game", "", "", false)
		job, _ := m.Jobs().Get(jobID)
		if detail, _ := job["detail"].(string); !strings.Contains(detail, "unknown platform") {
			t.Errorf("detail = %q, want unknown platform", detail)
		}
		if !pathExists(src) {
			t.Error("file should remain in place")
		}
	})

	t.Run("move failure sets error", func(t *testing.T) {
		m, jobID, src := newFixture(t)
		os.RemoveAll(m.cfg.GamesVaultPath) // vault dir gone: os.Create fails
		m.organizeDDLFile(jobID, src, "Great Game", "PC", "", true)
		job, _ := m.Jobs().Get(jobID)
		if status, _ := job["status"].(string); status != "error" {
			t.Errorf("status = %q, want error", status)
		}
	})

	// A DDL source need not label its rows either, and the file itself is then
	// the only thing that can say where the download belongs.
	t.Run("platform comes from the file when the request carried none", func(t *testing.T) {
		m, jobID, _ := newFixture(t)
		src := filepath.Join(t.TempDir(), "Some Cartridge.nes")
		writeFileT(t, src, []byte("rom"))

		m.organizeDDLFile(jobID, src, "Some Cartridge", "", "", false)

		dest := filepath.Join(m.cfg.GamesRomsPath, "nes", "Some Cartridge.nes")
		if !pathExists(dest) {
			t.Errorf("DDL import did not detect the platform from its file: %s not written", dest)
		}
	})
}

func TestRecoverOrphanedTorrents(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	// All torrents complete so no watch goroutines are spawned.
	qm.setTorrents([]qbit.Torrent{
		{Name: "Awesome.Game.v1.2-FitGirl", Hash: "h1", Progress: 1.0},
		{Name: "Zelda Collection wii pack", Hash: "h2", Progress: 1.0},
		{Name: "Totally Mysterious Thing", Hash: "h3", Progress: 1.0},
	})
	cfg.QBURL = qm.srv.URL

	m := New(cfg, jobs, qm.client())
	m.RecoverOrphanedTorrents()

	byTitle := map[string]map[string]interface{}{}
	for _, item := range jobs.Items() {
		title, _ := item.Data["title"].(string)
		byTitle[title] = item.Data
	}
	if len(byTitle) != 3 {
		t.Fatalf("recovered %d jobs, want 3", len(byTitle))
	}

	tests := []struct {
		title    string
		platform string
		isPC     bool
	}{
		{"Awesome.Game.v1.2-FitGirl", "PC", true},
		{"Zelda Collection wii pack", "Wii", false},
		{"Totally Mysterious Thing", "Unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			job, ok := byTitle[tt.title]
			if !ok {
				t.Fatalf("no job recovered for %q", tt.title)
			}
			if status, _ := job["status"].(string); status != "completed_unorganized" {
				t.Errorf("status = %q, want completed_unorganized", status)
			}
			if platf, _ := job["platform"].(string); platf != tt.platform {
				t.Errorf("platform = %q, want %q", platf, tt.platform)
			}
			if isPC, _ := job["is_pc"].(bool); isPC != tt.isPC {
				t.Errorf("is_pc = %v, want %v", isPC, tt.isPC)
			}
		})
	}
}

func TestRecoverOrphanedTorrentsIsIdempotent(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	// One complete, one still going: the two branches record a job by
	// different routes and both have to be idempotent.
	qm.setTorrents([]qbit.Torrent{
		{Name: "Awesome.Game.v1.2-FitGirl", Hash: "h1", Progress: 1.0},
		{Name: "Zelda Collection wii pack", Hash: "h2", Progress: 0.4},
	})
	cfg.QBURL = qm.srv.URL

	m := New(cfg, jobs, qm.client())

	// Recovery runs at startup and again whenever the monitor fires
	// run_orphan_recovery, so a second pass is ordinary, not pathological.
	m.RecoverOrphanedTorrents()
	after1 := len(jobs.Items())
	m.RecoverOrphanedTorrents()
	after2 := len(jobs.Items())

	if after1 != 2 {
		t.Fatalf("first pass recorded %d jobs, want 2", after1)
	}
	// Counting rows, not titles: a map keyed by title hides the duplicates.
	if after2 != after1 {
		t.Errorf("second pass took jobs from %d to %d, want them unchanged", after1, after2)
	}

	hashes := map[string]int{}
	for _, item := range jobs.Items() {
		h, _ := item.Data["info_hash"].(string)
		hashes[h]++
	}
	for h, n := range hashes {
		if n != 1 {
			t.Errorf("hash %q has %d job rows, want 1", h, n)
		}
	}
}

func TestRecoverOrphanedTorrentsLeavesImportedGamesAlone(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	// finishTorrent leaves a torrent seeding after a source-preserving import,
	// so a game that is already in the library is still in the category the next
	// time recovery runs. That is the ordinary case, not an odd one.
	qm.setTorrents([]qbit.Torrent{
		{Name: "Imported.Game-FitGirl", Hash: "h1", Progress: 1.0},
	})
	cfg.QBURL = qm.srv.URL

	m := New(cfg, jobs, qm.client())
	jobs.Set("job-done", map[string]interface{}{
		"status":    "completed",
		"title":     "Imported.Game-FitGirl",
		"info_hash": "h1",
		"detail":    "Moved to GameVault",
	})

	m.RecoverOrphanedTorrents()

	job, ok := jobs.Get("job-done")
	if !ok {
		t.Fatal("recovery dropped the completed job")
	}
	// completed_unorganized is what draws the Organize button, so a downgrade
	// here offers to import a game that is already in the library.
	if status, _ := job["status"].(string); status != "completed" {
		t.Errorf("status = %q, want it left at completed", status)
	}
	if detail, _ := job["detail"].(string); detail != "Moved to GameVault" {
		t.Errorf("detail = %q, want the import's own detail kept", detail)
	}
	if n := len(jobs.Items()); n != 1 {
		t.Errorf("%d job rows, want 1 - recovery added a row for an imported game", n)
	}
}

func TestWatchGameTorrentRunsOneWatcherPerTorrent(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	// Never completes, so a watcher that takes the claim stays parked in its
	// poll loop and holds it for the duration of the test.
	qm.setTorrents([]qbit.Torrent{
		{Name: "Held.Game-FitGirl", Hash: "h1", Progress: 0.4},
	})
	cfg.QBURL = qm.srv.URL
	cfg.FileListScanEnabled = false

	m := New(cfg, jobs, qm.client())

	go m.watchGameTorrent("job-1", "h1", "Held.Game-FitGirl", "PC", "", true)

	// Wait for the first watcher to register rather than sleeping a fixed
	// guess at how long it takes to get there.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, held := m.watching.Load("h1"); held {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first watcher never claimed the torrent")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A second watcher on the same torrent must decline and return, not sit
	// in a rival poll loop racing the first one to import.
	done := make(chan struct{})
	go func() {
		m.watchGameTorrent("job-2", "h1", "Held.Game-FitGirl", "PC", "", true)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("second watcher did not return - it is running alongside the first")
	}
}

func TestDownloadTorrentResolvesHashFromClient(t *testing.T) {
	const (
		release = "Cyberpunk 2077: Ultimate Edition - v2.3 [FitGirl Repack]"
		torrent = "Setup.CP2077.UltimateEdition"
		hash    = "c9d3921bf7017490c74d546c3d0fd6f1e0694422"
	)
	// The premise of the test: with no infohash, nothing else binds this job to
	// this torrent. If the titles ever did match, the test would pass without
	// exercising the resolution at all.
	if titlesMatch(release, torrent) {
		t.Fatalf("test premise broken: %q and %q match on title", release, torrent)
	}

	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	qm := newQbitMock(t)
	cfg.QBURL = qm.srv.URL
	cfg.FileListScanEnabled = false
	qm.appearOnAdd(qbit.Torrent{Name: torrent, Hash: hash, Progress: 0.2})

	m := New(cfg, jobs, qm.client())
	// A Prowlarr redirect link, which is what gamarr actually receives - there
	// is no magnet to read the hash out of.
	jobID, err := m.DownloadTorrent(
		"http://prowlarr:9696/75/download?link=abc", "", release, "PC", "", true)
	if err != nil {
		t.Fatalf("DownloadTorrent: %v", err)
	}

	// Resolution runs off the request path, so poll for it rather than
	// assuming it has already happened.
	deadline := time.Now().Add(10 * time.Second)
	for {
		job, ok := jobs.Get(jobID)
		if !ok {
			t.Fatalf("job %s vanished", jobID)
		}
		got, _ := job["info_hash"].(string)
		if strings.EqualFold(got, hash) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("info_hash = %q, want %q - the client was never asked", got, hash)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMoveHelpers(t *testing.T) {
	t.Run("moveFile renames", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "a.txt")
		dest := filepath.Join(dir, "b.txt")
		writeFileT(t, src, []byte("hello"))
		if err := moveFile(src, dest); err != nil {
			t.Fatalf("moveFile: %v", err)
		}
		if pathExists(src) || !pathExists(dest) {
			t.Error("moveFile did not move the file")
		}
	})

	t.Run("moveFile missing source", func(t *testing.T) {
		dir := t.TempDir()
		if err := moveFile(filepath.Join(dir, "nope"), filepath.Join(dir, "out")); err == nil {
			t.Error("want error for missing source")
		}
	})

	t.Run("moveContent directory", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "gamedir")
		writeFileT(t, filepath.Join(src, "sub", "file.bin"), []byte("data"))
		dest := filepath.Join(dir, "moved")
		if err := moveContent(src, dest); err != nil {
			t.Fatalf("moveContent: %v", err)
		}
		if pathExists(src) {
			t.Error("source dir should be removed")
		}
		data, err := os.ReadFile(filepath.Join(dest, "sub", "file.bin"))
		if err != nil || string(data) != "data" {
			t.Errorf("moved content = %q err=%v", data, err)
		}
	})

	t.Run("moveContent single file", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "single.rom")
		writeFileT(t, src, []byte("x"))
		dest := filepath.Join(dir, "single-moved.rom")
		if err := moveContent(src, dest); err != nil {
			t.Fatalf("moveContent: %v", err)
		}
		if !pathExists(dest) {
			t.Error("file not moved")
		}
	})

	t.Run("moveContent missing source", func(t *testing.T) {
		if err := moveContent("/no/such/path", t.TempDir()); err == nil {
			t.Error("want error for missing source")
		}
	})

	t.Run("copyFile missing source", func(t *testing.T) {
		if err := copyFile("/no/such/file", filepath.Join(t.TempDir(), "out")); err == nil {
			t.Error("want error for missing source")
		}
	})

	t.Run("pathExists", func(t *testing.T) {
		dir := t.TempDir()
		if !pathExists(dir) {
			t.Error("existing dir reported missing")
		}
		if pathExists(filepath.Join(dir, "ghost")) {
			t.Error("missing path reported existing")
		}
	})
}

func TestWriteMetadataSidecar(t *testing.T) {
	t.Run("directory dest writes inside", func(t *testing.T) {
		dir := t.TempDir()
		writeMetadataSidecar(dir, "My Game", "SNES", "snes", false, "torrent")
		data, err := os.ReadFile(filepath.Join(dir, ".gamarr.json"))
		if err != nil {
			t.Fatalf("sidecar missing: %v", err)
		}
		var meta map[string]interface{}
		if err := json.Unmarshal(data, &meta); err != nil {
			t.Fatalf("invalid sidecar JSON: %v", err)
		}
		if meta["title"] != "My Game" || meta["platform_slug"] != "snes" || meta["source"] != "torrent" {
			t.Errorf("sidecar meta = %v", meta)
		}
		if meta["is_pc"] != false {
			t.Errorf("is_pc = %v, want false", meta["is_pc"])
		}
	})

	t.Run("file dest writes sibling", func(t *testing.T) {
		fp := filepath.Join(t.TempDir(), "rom.sfc")
		writeFileT(t, fp, []byte("rom"))
		writeMetadataSidecar(fp, "Rom Game", "SNES", "snes", false, "ddl")
		if !pathExists(fp + ".gamarr.json") {
			t.Error("sibling sidecar missing")
		}
	})
}

func TestSettingsRoundTrip(t *testing.T) {
	cfg := newTestConfig(t)
	m := New(cfg, newTestJobs(t), nil)

	t.Run("defaults when no file", func(t *testing.T) {
		s := m.LoadSettings()
		if s.ExtractArchives != cfg.ExtractArchives {
			t.Errorf("ExtractArchives = %v, want config default %v", s.ExtractArchives, cfg.ExtractArchives)
		}
	})

	t.Run("save and load", func(t *testing.T) {
		m.SaveSettings(&Settings{ExtractArchives: true})
		s := m.LoadSettings()
		if !s.ExtractArchives {
			t.Error("ExtractArchives = false after saving true")
		}
	})

	t.Run("corrupt file falls back to default", func(t *testing.T) {
		writeFileT(t, filepath.Join(cfg.DataDir, "settings.json"), []byte("{{{"))
		s := m.LoadSettings()
		if s.ExtractArchives != cfg.ExtractArchives {
			t.Errorf("corrupt settings should fall back to config default")
		}
	})
}

func TestDDLSourcesRoundTrip(t *testing.T) {
	cfg := newTestConfig(t)
	m := New(cfg, newTestJobs(t), nil)

	if got := m.LoadDDLSources(); got != nil {
		t.Errorf("LoadDDLSources with no file = %v, want nil", got)
	}

	sources := []map[string]interface{}{
		{"name": "Myrient", "url": "https://example.test/roms"},
		{"name": "Other", "enabled": true},
	}
	m.SaveDDLSources(sources)
	got := m.LoadDDLSources()
	if len(got) != 2 {
		t.Fatalf("loaded %d sources, want 2", len(got))
	}
	if got[0]["name"] != "Myrient" {
		t.Errorf("first source = %v", got[0])
	}
}

func TestExtractArchives(t *testing.T) {
	t.Run("corrupt archive removed and skipped", func(t *testing.T) {
		dir := t.TempDir()
		writeFileT(t, filepath.Join(dir, "broken.zip"), []byte("this is not a zip"))
		extracted := extractArchives(dir)
		if len(extracted) != 0 {
			t.Errorf("extracted = %v, want none for corrupt archive", extracted)
		}
		if pathExists(filepath.Join(dir, "broken.zip.extracted")) {
			t.Error("failed extraction dir should be cleaned up")
		}
	})

	t.Run("already extracted archive skipped", func(t *testing.T) {
		dir := t.TempDir()
		writeFileT(t, filepath.Join(dir, "done.zip"), []byte("junk"))
		if err := os.MkdirAll(filepath.Join(dir, "done.zip.extracted"), 0755); err != nil {
			t.Fatal(err)
		}
		if extracted := extractArchives(dir); len(extracted) != 0 {
			t.Errorf("extracted = %v, want none (already extracted)", extracted)
		}
	})

	t.Run("recurses into subdirectories", func(t *testing.T) {
		dir := t.TempDir()
		writeFileT(t, filepath.Join(dir, "sub", "inner.rar"), []byte("not a rar"))
		// Should not panic and should not extract anything (unrar fails/missing).
		if extracted := extractArchives(dir); len(extracted) != 0 {
			t.Errorf("extracted = %v, want none", extracted)
		}
	})

	t.Run("valid zip extracted when 7z available", func(t *testing.T) {
		if _, err := exec.LookPath("7z"); err != nil {
			t.Skip("7z not installed")
		}
		dir := t.TempDir()
		zipPath := filepath.Join(dir, "good.zip")
		f, err := os.Create(zipPath)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(f)
		w, _ := zw.Create("inside.txt")
		w.Write([]byte("hello"))
		zw.Close()
		f.Close()

		extracted := extractArchives(dir)
		if len(extracted) != 1 {
			t.Fatalf("extracted = %v, want 1", extracted)
		}
		if !pathExists(filepath.Join(dir, "good.zip.extracted", "inside.txt")) {
			t.Error("extracted file missing")
		}
	})
}

func TestMaybeExtractArchives(t *testing.T) {
	t.Run("disabled is a no-op", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		m := New(cfg, jobs, nil)
		dir := t.TempDir()
		writeFileT(t, filepath.Join(dir, "a.zip"), []byte("junk"))
		jobID := newJobID()
		jobs.Set(jobID, map[string]interface{}{"status": "completed", "detail": "Moved"})

		m.maybeExtractArchives(jobID, dir)

		job, _ := jobs.Get(jobID)
		if detail, _ := job["detail"].(string); detail != "Moved" {
			t.Errorf("detail changed when extraction disabled: %q", detail)
		}
	})

	t.Run("enabled with missing dest returns", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		m := New(cfg, jobs, nil)
		m.SaveSettings(&Settings{ExtractArchives: true})
		m.maybeExtractArchives("nojob", filepath.Join(t.TempDir(), "ghost"))
	})

	t.Run("enabled with file dest scans parent dir", func(t *testing.T) {
		cfg := newTestConfig(t)
		jobs := newTestJobs(t)
		m := New(cfg, jobs, nil)
		m.SaveSettings(&Settings{ExtractArchives: true})
		dir := t.TempDir()
		fp := filepath.Join(dir, "rom.sfc")
		writeFileT(t, fp, []byte("rom"))
		writeFileT(t, filepath.Join(dir, "bad.zip"), []byte("junk"))
		// Corrupt zip fails extraction; the call must not panic or alter the job.
		m.maybeExtractArchives("nojob", fp)
		if pathExists(filepath.Join(dir, "bad.zip.extracted")) {
			t.Error("failed extraction dir left behind")
		}
	})
}

// Some sources legitimately ship a .bat next to the payload, and the operator
// has no other way past a filename match.
func TestDownloadTorrentFileListScanDisabled(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	cfg.QBURL = "configured"
	cfg.FileListScanEnabled = false

	qm := newQbitMock(t)
	qm.setFiles([]qbit.TorrentFile{{Name: "Game/keygen.bat"}, {Name: "Game/setup.scr"}})
	qm.setTorrents([]qbit.Torrent{{
		Name:     "Repack Game",
		Hash:     "hash-optout",
		Progress: 0.5, // metadata available, still downloading
	}})

	m := New(cfg, jobs, qm.client())
	jobID, err := m.DownloadTorrent("magnet:x", "", "Repack Game", "PC", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With the scan on, the watcher acts on its first pass, so a short wait is
	// enough to catch it having done so.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(qm.deleteCalls()) != 0 {
			t.Fatalf("torrent was acted on despite the scan being disabled: %+v", qm.deleteCalls())
		}
		if job, ok := jobs.Get(jobID); ok {
			if status, _ := job["status"].(string); status == "error" {
				t.Fatalf("job errored despite the scan being disabled: %v", job["error"])
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Prowlarr maps Nyaa's only games category to Newznab 4050 PC/Games, so a
// Switch ROM from Nyaa reaches the downloader tagged PC. It has to still land
// in the Switch ROM library, not GameVault.
func TestNyaaSwitchROMTaggedPCImportsAsSwitch(t *testing.T) {
	info := platform.DetectPlatform([]interface{}{float64(4050)})
	if !info.IsPC {
		t.Fatalf("fixture stale: 4050 no longer detects as PC (%+v)", info)
	}

	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	cfg.QBURL = "configured"

	content := filepath.Join(t.TempDir(), "Zelda TOTK")
	writeFileT(t, filepath.Join(content, "Zelda.TOTK.nsp"), []byte("switch-rom"))

	qm := newQbitMock(t)
	qm.setFiles([]qbit.TorrentFile{{Name: "Zelda TOTK/Zelda.TOTK.nsp"}})
	qm.setTorrents([]qbit.Torrent{{
		Name: "Zelda TOTK", Hash: "h-switch", Progress: 1.0, ContentPath: content,
	}})

	m := New(cfg, jobs, qm.client())
	// exactly what a 4050-tagged search hit hands the downloader
	jobID, err := m.DownloadTorrent("magnet:x", "h-switch", "Zelda TOTK", info.Name, info.Slug, info.IsPC)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	job := waitJobStatus(t, jobs, jobID, "completed", 10*time.Second)
	if got, _ := job["platform_slug"].(string); got != "switch" {
		t.Errorf("platform_slug = %q, want switch", got)
	}
	if isPC, _ := job["is_pc"].(bool); isPC {
		t.Error("job still marked is_pc after a .nsp was found")
	}
	wantPath := filepath.Join(cfg.GamesRomsPath, "switch", "Zelda TOTK", "Zelda.TOTK.nsp")
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("ROM not in the Switch library: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.GamesVaultPath, "Zelda TOTK")); err == nil {
		t.Error("ROM was imported into GameVault as a PC game")
	}
}

// The other half of the same category: a genuine PC release tagged 4050 has to
// keep going to GameVault.
func TestPCGameTaggedPCGamesImportsToVault(t *testing.T) {
	cfg := newTestConfig(t)
	jobs := newTestJobs(t)
	cfg.QBURL = "configured"

	content := filepath.Join(t.TempDir(), "Terraria")
	writeFileT(t, filepath.Join(content, "setup.exe"), []byte("installer"))
	// a Doom-engine asset and a bundled NES ROM: neither may reroute a PC game
	writeFileT(t, filepath.Join(content, "base.wad"), []byte("doom"))
	writeFileT(t, filepath.Join(content, "extras", "bonus.nes"), []byte("rom"))

	qm := newQbitMock(t)
	// list the real payload: a lone .exe trips the scanner's "only executables"
	// heuristic, which is not what this test is about
	qm.setFiles([]qbit.TorrentFile{
		{Name: "Terraria/setup.exe"},
		{Name: "Terraria/base.wad"},
		{Name: "Terraria/extras/bonus.nes"},
	})
	qm.setTorrents([]qbit.Torrent{{
		Name: "Terraria", Hash: "h-pc", Progress: 1.0, ContentPath: content,
	}})

	m := New(cfg, jobs, qm.client())
	jobID, err := m.DownloadTorrent("magnet:x", "h-pc", "Terraria", "PC", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	job := waitJobStatus(t, jobs, jobID, "completed", 10*time.Second)
	if isPC, _ := job["is_pc"].(bool); !isPC {
		t.Errorf("PC game was reclassified: platform=%v slug=%v", job["platform"], job["platform_slug"])
	}
	if _, err := os.Stat(filepath.Join(cfg.GamesVaultPath, "Terraria", "setup.exe")); err != nil {
		t.Errorf("PC game not in GameVault: %v", err)
	}
}

// The per-job watch imports through the same path the watcher does, so a
// content path that is not there yet has to recover on both. It did not: the
// watch computed the retryable signal and discarded it, erroring the job
// permanently, and an errored job then stops the watcher rescuing it.
func TestWatchGameTorrentRetriesAPathThatIsNotThereYet(t *testing.T) {
	setImportRetries(t, 400, 5*time.Millisecond)
	qm := newQbitMock(t)
	cfg := newTestConfig(t)
	cfg.QBURL = "configured"
	m := New(cfg, newTestJobs(t), qm.client())

	// A FitGirl release: the folder the client publishes is not the torrent's
	// display name, so the save-path guess never resolves either.
	content := filepath.Join(cfg.QBSavePath, "Spider-Man - Miles Morales [FitGirl Repack]")
	torrent := qbit.Torrent{
		Name:        "Marvel's Spider-Man - Miles Morales (v3.1.0 + DLC, MULTi19) [FitGirl Repack]",
		Hash:        "mm-hash",
		Progress:    1.0,
		SavePath:    cfg.QBSavePath,
		ContentPath: content,
	}
	qm.setTorrents([]qbit.Torrent{torrent})

	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{
		"status": "downloading", "title": torrent.Name, "info_hash": "mm-hash",
	})

	staged := make(chan error, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		if err := os.MkdirAll(content, 0755); err != nil {
			staged <- err
			return
		}
		staged <- os.WriteFile(filepath.Join(content, "setup.exe"), []byte("installer"), 0644)
	}()

	m.watchGameTorrent(jobID, "mm-hash", torrent.Name, "PC", "", true)

	if err := <-staged; err != nil {
		t.Fatalf("stage the published files: %v", err)
	}
	assertImportedCleanly(t, m, jobID)
}

// The manual organize is the path used to unstick a game by hand, and it hit the
// transient without a retry. Worse than the immediate failure: the error job it
// left behind is what makes the watcher skip the torrent afterwards, so one
// mistimed manual organize stranded it from the automatic path for good.
func TestOrganizeTorrentRetriesAPathThatIsNotThereYet(t *testing.T) {
	setImportRetries(t, 400, 5*time.Millisecond)
	qm := newQbitMock(t)
	cfg := newTestConfig(t)
	cfg.QBURL = "configured"
	m := New(cfg, newTestJobs(t), qm.client())

	content := filepath.Join(cfg.QBSavePath, "Manual Game [FitGirl Repack]")
	qm.setTorrents([]qbit.Torrent{{
		Name: "Manual Game", Hash: "man-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: content,
	}})

	staged := make(chan error, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		if err := os.MkdirAll(content, 0755); err != nil {
			staged <- err
			return
		}
		staged <- os.WriteFile(filepath.Join(content, "setup.exe"), []byte("installer"), 0644)
	}()

	jobID, err := m.OrganizeTorrent("man-hash", "PC", "", true)
	if err != nil {
		t.Fatalf("OrganizeTorrent: %v", err)
	}

	waitJobStatus(t, m.Jobs(), jobID, "completed", minPollTimeout)
	if err := <-staged; err != nil {
		t.Fatalf("stage the published files: %v", err)
	}
	assertImportedCleanly(t, m, jobID)
}

// The client publishes a finished torrent by moving it to its final home right
// after progress reads complete, so the first import can lose its tree
// mid-walk even though the content path itself resolved. The failure is not
// always an ENOENT - a cross-device publish that copies instead of renaming
// surfaces as a census or write error - so the classification re-stats the
// tree the attempt was reading. The staging tree holds nothing importable, so
// only the re-read of the torrent's published path can complete the job.
func TestImportRetriesWhenTheClientMovesThePayloadMidWalk(t *testing.T) {
	setImportRetries(t, 400, 5*time.Millisecond)
	qm := newQbitMock(t)
	cfg := newTestConfig(t)
	cfg.QBURL = "configured"
	// The vault archive is where the walk loses the race on a live FitGirl grab.
	cfg.VaultArchiveEnabled = true
	m := New(cfg, newTestJobs(t), qm.client())

	stale := filepath.Join(cfg.QBSavePath, "temp", "The Witcher 3 [FitGirl Repack]")
	published := filepath.Join(cfg.QBSavePath, "The Witcher 3 [FitGirl Repack]")
	if err := os.MkdirAll(stale, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", stale, err)
	}
	if err := os.MkdirAll(published, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", published, err)
	}
	if err := os.WriteFile(filepath.Join(published, "setup.exe"), []byte("installer"), 0644); err != nil {
		t.Fatalf("write setup.exe: %v", err)
	}

	// The client answers the loop's re-read with the published path, while the
	// import still holds the struct captured at progress 1.0.
	qm.setTorrents([]qbit.Torrent{{
		Name: "The Witcher 3 [FitGirl Repack]", Hash: "w3-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: published,
	}})
	qm.setFiles([]qbit.TorrentFile{
		{Name: "The Witcher 3 [FitGirl Repack]/setup.exe", Size: int64(len("installer")), Priority: 1},
	})
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{
		"status": "downloading", "title": "The Witcher 3 [FitGirl Repack]", "info_hash": "w3-hash",
	})

	// First attempt reads the staging tree; the move takes the tree away under
	// it and the error surfaces as a mismatch, not an ENOENT.
	realArchive := archive
	attempts := 0
	archive = func(src, dest string, wanted fileops.WantedFiles) error {
		attempts++
		if attempts == 1 {
			os.RemoveAll(src)
			return fmt.Errorf("archive of %s holds 0 files and 0 bytes, source has 1 and 9", src)
		}
		return realArchive(src, dest, wanted)
	}
	t.Cleanup(func() { archive = realArchive })

	m.importFinishedTorrent("job watch", jobID, qbit.Torrent{
		Name: "The Witcher 3 [FitGirl Repack]", Hash: "w3-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: stale,
	}, "PC", "", true)

	if attempts < 2 {
		t.Errorf("archive attempts = %d, want the failed one retried at the published path", attempts)
	}
	assertImportedCleanly(t, m, jobID)
}

// A failure while the content tree is still in place is a real defect, not the
// publish race: it has to terminate on the first attempt with the real cause
// in the row, never burn the retry loop or end in the give-up detail.
func TestImportWithTheContentStillPresentStaysTerminal(t *testing.T) {
	setImportRetries(t, 400, 5*time.Millisecond)
	qm := newQbitMock(t)
	cfg := newTestConfig(t)
	cfg.QBURL = "configured"
	// The archive walk refuses a non-regular entry loudly; a symlink pointing
	// nowhere is one such refusal, and it reads the same on every attempt.
	cfg.VaultArchiveEnabled = true
	m := New(cfg, newTestJobs(t), qm.client())

	content := filepath.Join(cfg.QBSavePath, "Broken Game [FitGirl Repack]")
	if err := os.MkdirAll(content, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", content, err)
	}
	if err := os.WriteFile(filepath.Join(content, "setup.exe"), []byte("installer"), 0644); err != nil {
		t.Fatalf("write setup.exe: %v", err)
	}
	if err := os.Symlink(filepath.Join(content, "missing"), filepath.Join(content, "fg-01.bin")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	qm.setTorrents([]qbit.Torrent{{
		Name: "Broken Game [FitGirl Repack]", Hash: "broken-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: content,
	}})
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{
		"status": "downloading", "title": "Broken Game [FitGirl Repack]", "info_hash": "broken-hash",
	})

	m.importFinishedTorrent("job watch", jobID, qbit.Torrent{
		Name: "Broken Game [FitGirl Repack]", Hash: "broken-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: content,
	}, "PC", "", true)

	job, ok := m.Jobs().Get(jobID)
	if !ok {
		t.Fatalf("job %s not found", jobID)
	}
	status, _ := job["status"].(string)
	detail, _ := job["detail"].(string)
	errMsg, _ := job["error"].(string)
	if status != "error" {
		t.Errorf("status = %q, want a terminal error", status)
	}
	if !strings.Contains(errMsg, "not a regular file") {
		t.Errorf("error = %q, want the real cause named", errMsg)
	}
	if strings.Contains(detail, "Gave up") {
		t.Errorf("detail = %q, want no give-up: the tree was in place, so the first attempt is terminal", detail)
	}
}

// The ROM arm classifies the publish race the same way the vault arm does, and
// a retry there lands the published tree in the ROM library.
func TestRomImportRetriesWhenTheClientMovesThePayload(t *testing.T) {
	setImportRetries(t, 400, 5*time.Millisecond)
	qm := newQbitMock(t)
	cfg := newTestConfig(t)
	cfg.QBURL = "configured"
	m := New(cfg, newTestJobs(t), qm.client())

	stale := filepath.Join(cfg.QBSavePath, "temp", "Super Game (USA)")
	published := filepath.Join(cfg.QBSavePath, "Super Game (USA)")
	if err := os.MkdirAll(stale, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", stale, err)
	}
	if err := os.MkdirAll(published, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", published, err)
	}
	if err := os.WriteFile(filepath.Join(published, "game.sfc"), []byte("rom-data"), 0644); err != nil {
		t.Fatalf("write game.sfc: %v", err)
	}

	qm.setTorrents([]qbit.Torrent{{
		Name: "Super Game (USA)", Hash: "rom-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: published,
	}})
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{
		"status": "downloading", "title": "Super Game (USA)", "info_hash": "rom-hash",
	})

	// First attempt reads the staging tree, which the client's move takes away
	// under it; the published tree is the only one that can complete.
	realImport := fileImport
	attempts := 0
	fileImport = func(src, dest string, opt fileops.Options) error {
		attempts++
		if attempts == 1 {
			os.RemoveAll(src)
			return fmt.Errorf("copy of %s interrupted by the client", src)
		}
		return realImport(src, dest, opt)
	}
	t.Cleanup(func() { fileImport = realImport })

	m.importFinishedTorrent("job watch", jobID, qbit.Torrent{
		Name: "Super Game (USA)", Hash: "rom-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: stale,
	}, "SNES", "snes", false)

	if attempts < 2 {
		t.Errorf("import attempts = %d, want the failed one retried at the published path", attempts)
	}
	job := waitJobStatus(t, m.Jobs(), jobID, "completed", minPollTimeout)
	detail, _ := job["detail"].(string)
	errMsg, _ := job["error"].(string)
	if detail != "Moved to RomM (SNES)" || errMsg != "" {
		t.Errorf("job = {detail:%q error:%q}, want the moved receipt and nothing left over from a failed attempt",
			detail, errMsg)
	}
	if !pathExists(filepath.Join(cfg.GamesRomsPath, "snes", "Super Game (USA)", "game.sfc")) {
		t.Error("game file not at the ROM destination after the retried import")
	}
}

// A cross-device publish depletes the source over minutes, so the first
// attempt can hit an ENOENT while the tree is still standing. That attempt
// holds one retry cycle: the re-read hands back the published path and the
// next attempt lands it.
func TestImportHoldsOneCycleWhenTheSourceDepletes(t *testing.T) {
	setImportRetries(t, 400, 5*time.Millisecond)
	qm := newQbitMock(t)
	cfg := newTestConfig(t)
	cfg.QBURL = "configured"
	m := New(cfg, newTestJobs(t), qm.client())

	stale := filepath.Join(cfg.QBSavePath, "temp", "Super Game (USA)")
	published := filepath.Join(cfg.QBSavePath, "Super Game (USA)")
	if err := os.MkdirAll(stale, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", stale, err)
	}
	if err := os.MkdirAll(published, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", published, err)
	}
	if err := os.WriteFile(filepath.Join(stale, "game1.sfc"), []byte("rom-data"), 0644); err != nil {
		t.Fatalf("write game1.sfc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(published, "game1.sfc"), []byte("rom-data"), 0644); err != nil {
		t.Fatalf("write game1.sfc: %v", err)
	}

	qm.setTorrents([]qbit.Torrent{{
		Name: "Super Game (USA)", Hash: "depl-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: published,
	}})
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{
		"status": "downloading", "title": "Super Game (USA)", "info_hash": "depl-hash",
	})

	// First attempt hits a file the copy-publish has already deleted, with the
	// tree still standing around it.
	realImport := fileImport
	attempts := 0
	fileImport = func(src, dest string, opt fileops.Options) error {
		attempts++
		if attempts == 1 {
			os.Remove(filepath.Join(src, "game1.sfc"))
			return &fs.PathError{Op: "open", Path: filepath.Join(src, "game1.sfc"), Err: os.ErrNotExist}
		}
		return realImport(src, dest, opt)
	}
	t.Cleanup(func() { fileImport = realImport })

	m.importFinishedTorrent("job watch", jobID, qbit.Torrent{
		Name: "Super Game (USA)", Hash: "depl-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: stale,
	}, "SNES", "snes", false)

	if attempts < 2 {
		t.Errorf("import attempts = %d, want the held one retried at the published path", attempts)
	}
	waitJobStatus(t, m.Jobs(), jobID, "completed", minPollTimeout)
	if !pathExists(filepath.Join(cfg.GamesRomsPath, "snes", "Super Game (USA)", "game1.sfc")) {
		t.Error("game file not at the ROM destination after the held import")
	}
}

// The hold is one cycle, not a policy: an ENOENT that keeps recurring with the
// tree standing is a real defect and terminates on the second attempt, with
// the real cause in the row and no give-up detail.
func TestImportWithAPersistingBrokenSourceStopsAfterTheHold(t *testing.T) {
	setImportRetries(t, 400, 5*time.Millisecond)
	qm := newQbitMock(t)
	cfg := newTestConfig(t)
	cfg.QBURL = "configured"
	m := New(cfg, newTestJobs(t), qm.client())

	content := filepath.Join(cfg.QBSavePath, "Super Game (USA)")
	if err := os.MkdirAll(content, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", content, err)
	}
	if err := os.WriteFile(filepath.Join(content, "game1.sfc"), []byte("rom-data"), 0644); err != nil {
		t.Fatalf("write game1.sfc: %v", err)
	}

	qm.setTorrents([]qbit.Torrent{{
		Name: "Super Game (USA)", Hash: "persist-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: content,
	}})
	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{
		"status": "downloading", "title": "Super Game (USA)", "info_hash": "persist-hash",
	})

	realImport := fileImport
	attempts := 0
	fileImport = func(src, dest string, opt fileops.Options) error {
		attempts++
		return &fs.PathError{Op: "open", Path: filepath.Join(src, "game1.sfc"), Err: os.ErrNotExist}
	}
	t.Cleanup(func() { fileImport = realImport })

	m.importFinishedTorrent("job watch", jobID, qbit.Torrent{
		Name: "Super Game (USA)", Hash: "persist-hash", Progress: 1.0,
		SavePath: cfg.QBSavePath, ContentPath: content,
	}, "SNES", "snes", false)

	if attempts != 2 {
		t.Errorf("import attempts = %d, want exactly 2: the first held, the second terminal", attempts)
	}
	job, ok := m.Jobs().Get(jobID)
	if !ok {
		t.Fatalf("job %s not found", jobID)
	}
	status, _ := job["status"].(string)
	detail, _ := job["detail"].(string)
	errMsg, _ := job["error"].(string)
	if status != "error" {
		t.Errorf("status = %q, want a terminal error", status)
	}
	if !strings.Contains(errMsg, "does not exist") {
		t.Errorf("error = %q, want the real cause named", errMsg)
	}
	if !strings.Contains(detail, "use Retry") {
		t.Errorf("detail = %q, want the retry hint a terminal row carries", detail)
	}
	if strings.Contains(detail, "Gave up") {
		t.Errorf("detail = %q, want no give-up: the second attempt is terminal", detail)
	}
}

// The partial-destination cleanup has to carry real debris for its guard to
// mean anything: a retry against uncleared debris dies on the existing tree,
// and a dest that predated the import must survive the failure untouched.
func TestRomImportClearsOnlyItsOwnDebrisBeforeTheRetry(t *testing.T) {
	setImportRetries(t, 400, 5*time.Millisecond)

	newFixture := func(t *testing.T, preSeed string) (*Manager, string, string, qbit.Torrent, *int) {
		qm := newQbitMock(t)
		cfg := newTestConfig(t)
		cfg.QBURL = "configured"
		m := New(cfg, newTestJobs(t), qm.client())

		stale := filepath.Join(cfg.QBSavePath, "temp", "Super Game (USA)")
		published := filepath.Join(cfg.QBSavePath, "Super Game (USA)")
		if err := os.MkdirAll(stale, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", stale, err)
		}
		if err := os.MkdirAll(published, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", published, err)
		}
		if err := os.WriteFile(filepath.Join(published, "game1.sfc"), []byte("rom-data"), 0644); err != nil {
			t.Fatalf("write game1.sfc: %v", err)
		}

		dest := filepath.Join(cfg.GamesRomsPath, "snes", "Super Game (USA)")
		if preSeed != "" {
			if err := os.MkdirAll(dest, 0755); err != nil {
				t.Fatalf("mkdir dest: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dest, preSeed), []byte("keep me"), 0644); err != nil {
				t.Fatalf("write %s: %v", preSeed, err)
			}
		}

		// The client answers the loop's re-read with the published path, while
		// the import holds the struct captured against the staging path.
		captured := qbit.Torrent{
			Name: "Super Game (USA)", Hash: "debris-hash", Progress: 1.0,
			SavePath: cfg.QBSavePath, ContentPath: stale,
		}
		qm.setTorrents([]qbit.Torrent{{
			Name: "Super Game (USA)", Hash: "debris-hash", Progress: 1.0,
			SavePath: cfg.QBSavePath, ContentPath: published,
		}})
		jobID := newJobID()
		m.Jobs().Set(jobID, map[string]interface{}{
			"status": "downloading", "title": "Super Game (USA)", "info_hash": "debris-hash",
		})

		// The first attempt lands one debris file in dest, then the client's
		// move takes the staging tree away under it.
		realImport := fileImport
		attempts := 0
		fileImport = func(src, dest string, opt fileops.Options) error {
			attempts++
			if attempts == 1 {
				os.MkdirAll(dest, 0755)
				os.WriteFile(filepath.Join(dest, "partial.sfc"), []byte("debris"), 0644)
				os.RemoveAll(src)
				return fmt.Errorf("copy of %s interrupted by the client", src)
			}
			return realImport(src, dest, opt)
		}
		t.Cleanup(func() { fileImport = realImport })
		return m, jobID, dest, captured, &attempts
	}

	t.Run("debris is cleared before the retry", func(t *testing.T) {
		m, jobID, dest, captured, attempts := newFixture(t, "")

		m.importFinishedTorrent("job watch", jobID, captured, "SNES", "snes", false)

		if *attempts < 2 {
			t.Fatalf("import attempts = %d, want the debris attempt retried", *attempts)
		}
		waitJobStatus(t, m.Jobs(), jobID, "completed", minPollTimeout)
		if pathExists(filepath.Join(dest, "partial.sfc")) {
			t.Error("the failed attempt's debris survived into the published import")
		}
		if !pathExists(filepath.Join(dest, "game1.sfc")) {
			t.Error("game file not at the ROM destination after the retried import")
		}
	})

	t.Run("a pre-existing dest is left alone", func(t *testing.T) {
		m, jobID, dest, captured, attempts := newFixture(t, "marker.txt")

		m.importFinishedTorrent("job watch", jobID, captured, "SNES", "snes", false)

		if *attempts < 2 {
			t.Fatalf("import attempts = %d, want the failure handled and the import retried", *attempts)
		}
		waitJobStatus(t, m.Jobs(), jobID, "completed", minPollTimeout)
		if !pathExists(filepath.Join(dest, "marker.txt")) {
			t.Error("the pre-existing dest was removed by the failure handling")
		}
		if !pathExists(filepath.Join(dest, "game1.sfc")) {
			t.Error("game file not at the ROM destination after the retried import")
		}
	})
}

// assertImportedCleanly checks the whole job row rather than the status alone.
// A status assertion on its own passes while the row is wrong: the error field
// the UI renders regardless of status is exactly what a retried import was
// leaving behind from its failed attempts.
func assertImportedCleanly(t *testing.T, m *Manager, jobID string) {
	t.Helper()
	job, ok := m.Jobs().Get(jobID)
	if !ok {
		t.Fatalf("job %s not found", jobID)
	}
	status, _ := job["status"].(string)
	detail, _ := job["detail"].(string)
	errMsg, _ := job["error"].(string)
	if status != "completed" || detail != "Moved to GameVault" || errMsg != "" {
		t.Errorf("job = {status:%q detail:%q error:%q}, want completed, moved to the vault, and nothing left over from a failed attempt",
			status, detail, errMsg)
	}
}

// A terminal failure is where clearing this attempt's debris matters most: the
// row tells the operator to press Retry, and a retry against a half-written
// destination dies on an existing link instead of on the real cause. Gating
// the cleanup on transience left exactly that trap.
func TestTerminalRomFailureClearsItsOwnDebrisSoRetryWorks(t *testing.T) {
	cfg := newTestConfig(t)
	qm := newQbitMock(t)
	m := New(cfg, newTestJobs(t), qm.client())

	content := filepath.Join(cfg.QBSavePath, "Broken ROM")
	if err := os.MkdirAll(content, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(content, "a.sfc"), []byte("rom"), 0644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(cfg.GamesRomsPath, "snes", "Broken ROM")

	// A terminal failure - the content tree stays put, so this is not the
	// publish race - that has already written part of its destination.
	realImport := fileImport
	attempts := 0
	fileImport = func(src, destPath string, opt fileops.Options) error {
		attempts++
		if attempts == 1 {
			if err := os.MkdirAll(destPath, 0755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(destPath, "a.sfc"), []byte("half"), 0644); err != nil {
				return err
			}
			return errors.New("locked: permission denied")
		}
		return realImport(src, destPath, opt)
	}
	t.Cleanup(func() { fileImport = realImport })

	jobID := newJobID()
	m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing"})
	torrent := &qbit.Torrent{Name: "Broken ROM", Hash: "brk1", ContentPath: content}
	m.organizeGame(jobID, torrent, "SNES", "snes", false, 1)

	if pathExists(dest) {
		t.Fatalf("debris left at %s after a terminal failure", dest)
	}

	// The Retry the row advised must now reach the real import, not "file exists".
	jobID2 := newJobID()
	m.Jobs().Set(jobID2, map[string]interface{}{"status": "organizing"})
	m.organizeGame(jobID2, torrent, "SNES", "snes", false, 1)

	job, _ := m.Jobs().Get(jobID2)
	if status, _ := job["status"].(string); status != "completed" {
		t.Fatalf("retry job = %+v, want completed", job)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "a.sfc")); err != nil || string(got) != "rom" {
		t.Errorf("destination = %q (err %v), want the real content", got, err)
	}
}

// Two torrents whose content folders share a basename resolve to one
// destination, and m.importing is keyed by torrent hash, so nothing else keeps
// them apart. Without a claim on the path, one job's absence check, import and
// cleanup interleave with the other's, and the loser's cleanup deletes the
// winner's finished import. Assert the imports never overlap.
func TestConcurrentImportsToOneDestinationAreSerialised(t *testing.T) {
	cfg := newTestConfig(t)
	qm := newQbitMock(t)
	m := New(cfg, newTestJobs(t), qm.client())

	mk := func(root string) string {
		p := filepath.Join(cfg.QBSavePath, root, "Same Name")
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "game.sfc"), []byte(root), 0644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	sources := []string{mk("relA"), mk("relB")}

	var inFlight, peak atomic.Int32
	realImport := fileImport
	fileImport = func(src, destPath string, opt fileops.Options) error {
		n := inFlight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		// Widen the window so an unserialised pair actually overlaps.
		time.Sleep(20 * time.Millisecond)
		defer inFlight.Add(-1)
		return realImport(src, destPath, opt)
	}
	t.Cleanup(func() { fileImport = realImport })

	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func(src, hash string) {
			defer wg.Done()
			jobID := newJobID()
			m.Jobs().Set(jobID, map[string]interface{}{"status": "organizing"})
			m.organizeGame(jobID, &qbit.Torrent{Name: "Same Name", Hash: hash, ContentPath: src}, "SNES", "snes", false, 1)
		}(src, fmt.Sprintf("conc%d", i))
	}
	wg.Wait()

	if got := peak.Load(); got != 1 {
		t.Fatalf("peak concurrent imports to one destination = %d, want 1", got)
	}
	// And the destination holds one import's content, not a mixture.
	if _, err := os.Stat(filepath.Join(cfg.GamesRomsPath, "snes", "Same Name", "game.sfc")); err != nil {
		t.Errorf("destination content missing after two colliding imports: %v", err)
	}
}
