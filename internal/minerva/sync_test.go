package minerva

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"gamarr/internal/sources"
)

const assetsListing = `<a href="Minerva_Myrient_v9.99/">old</a><a href='Minerva_Myrient_v10.2/'>new</a><a href="Minerva_Myrient_v10.1/">older</a>`
const alphaTorrentPath = "/assets/Minerva_Myrient_v10.2/Minerva_Myrient - No-Intro - Alpha & Beta.torrent"
const betaTorrentPath = "/assets/Minerva_Myrient_v10.2/Minerva_Myrient - Redump - Beta.torrent"

func syncTorrent(name string) string {
	return fmt.Sprintf("d4:infod6:lengthi12e4:name%d:%see", len(name), name)
}

// Catches replacement on failure, redundant reparsing, and conditional requests during force.
func TestSyncCollectionResponses(t *testing.T) {
	for _, mode := range []string{"changed", "304", "same-body", "404", "malformed", "oversized", "500", "truncated", "force"} {
		t.Run(mode, func(t *testing.T) {
			phase := 0
			requests := 0
			s := newSyncService(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/assets/" {
					if mode == "force" && phase > 0 && (r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "") {
						t.Errorf("force assets validators = %v", r.Header)
					}
					w.Header().Set("ETag", fmt.Sprintf(`"assets-%d"`, phase))
					w.Header().Set("Last-Modified", "Mon, 07 Sep 2026 12:00:00 GMT")
					fmt.Fprint(w, assetsListing)
					if mode != "force" {
						fmt.Fprintf(w, "<!--%d-->", phase)
					}
					return
				}
				requests++
				w.Header().Set("ETag", fmt.Sprintf(`"torrent-%d"`, phase))
				w.Header().Set("Last-Modified", fmt.Sprintf("Mon, 07 Sep 2026 12:0%d:00 GMT", phase))
				if phase == 0 {
					fmt.Fprint(w, syncTorrent("Old.zip"))
					return
				}
				if mode == "force" {
					if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
						t.Errorf("force torrent validators = %v", r.Header)
					}
					fmt.Fprint(w, syncTorrent("Old.zip"))
					return
				}
				if r.Header.Get("If-None-Match") != `"torrent-0"` || r.Header.Get("If-Modified-Since") != "Mon, 07 Sep 2026 12:00:00 GMT" {
					t.Errorf("torrent validators = %v", r.Header)
				}
				if r.URL.Path == betaTorrentPath {
					w.WriteHeader(http.StatusNotModified)
					return
				}
				switch mode {
				case "changed":
					fmt.Fprint(w, syncTorrent("New.zip"))
				case "304":
					w.WriteHeader(http.StatusNotModified)
				case "same-body":
					fmt.Fprint(w, syncTorrent("Old.zip"))
				case "404":
					http.NotFound(w, r)
				case "malformed":
					fmt.Fprint(w, "not a torrent")
				case "oversized":
					_, _ = io.CopyN(w, zeroReader{}, 256*1024*1024+1)
				case "500":
					w.WriteHeader(http.StatusInternalServerError)
				case "truncated":
					w.Header().Set("Content-Length", "1000")
					fmt.Fprint(w, "short")
				}
			})
			ctx := context.Background()
			if _, err := s.Sync(ctx, false); err != nil {
				t.Fatal(err)
			}
			old, _, _ := s.index.Collection(ctx, "alpha")
			oldSync, _, _ := s.index.State(ctx, "last_sync")
			phase, requests = 1, 0
			report, err := s.Sync(ctx, mode == "force")
			failure := mode == "malformed" || mode == "oversized" || mode == "500" || mode == "truncated"
			if failure {
				if err == nil {
					t.Fatal("expected sync error")
				}
				if mode == "oversized" && !strings.Contains(err.Error(), "256 MiB") {
					t.Fatalf("overflow error = %v", err)
				}
				if report != (SyncReport{Checked: 1}) {
					t.Errorf("failed report = %+v", report)
				}
				lastSync, _, _ := s.index.State(ctx, "last_sync")
				if lastSync != oldSync {
					t.Errorf("failed sync advanced last_sync")
				}
				etag, _, _ := s.index.State(ctx, "assets_etag")
				if etag != `"assets-0"` {
					t.Errorf("failed sync cached assets = %q", etag)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				want := SyncReport{Checked: 2, Unchanged: 2}
				if mode == "changed" {
					want = SyncReport{Checked: 2, Updated: 1, Unchanged: 1, Files: 1}
				}
				if mode == "404" {
					want = SyncReport{Checked: 2, Missing: 1, Unchanged: 1}
				}
				if mode == "force" {
					want = SyncReport{Checked: 2, Updated: 2, Files: 2}
				}
				if report != want || requests != 2 {
					t.Errorf("report = %+v, requests = %d; want %+v, 2", report, requests, want)
				}
			}
			rec, _, _ := s.index.Collection(ctx, "alpha")
			if failure || mode == "404" || mode == "304" {
				if rec != old {
					t.Errorf("old record changed: %+v", rec)
				}
			}
			if mode == "same-body" {
				if rec.ETag != `"torrent-1"` || rec.LastModified != "Mon, 07 Sep 2026 12:01:00 GMT" || rec.ContentSHA256 != old.ContentSHA256 || rec.InfoHash != old.InfoHash {
					t.Errorf("same-body record = %+v", rec)
				}
			}
			hits, searchErr := s.Search(ctx, "", "alpha", 10)
			wantName := "Old.zip"
			if mode == "changed" {
				wantName = "New.zip"
			}
			if searchErr != nil || len(hits) != 1 || hits[0].Name != wantName {
				t.Errorf("hits = %+v, err = %v; want %s", hits, searchErr, wantName)
			}
		})
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// Catches stale readiness/error state and lost persisted status on restart.
func TestSyncStatusAndRecovery(t *testing.T) {
	broken := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if broken {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/assets/" {
			fmt.Fprint(w, assetsListing)
			return
		}
		fmt.Fprint(w, syncTorrent("Game.zip"))
	}))
	defer server.Close()
	dir := t.TempDir()
	spec := sources.MinervaSpec{Enabled: true, AssetsURL: server.URL + "/assets/", PlatformPaths: map[string]string{"alpha": "No-Intro/Alpha & Beta/"}}
	s, err := Open(dir, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	initial := s.Status(ctx)
	if !initial.Enabled || initial.Ready || initial.Syncing || !initial.LastSync.IsZero() || initial.LastError != "" || s.Ready(ctx) {
		t.Fatalf("initial = %+v", initial)
	}
	broken = true
	if _, err := s.Sync(ctx, false); err == nil {
		t.Fatal("expected initial sync failure")
	}
	if status := s.Status(ctx); status.Ready || status.LastError == "" || status.Syncing {
		t.Fatalf("initial failure = %+v", status)
	}
	broken = false
	if _, err := s.Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	good := s.Status(ctx)
	if !good.Ready || !s.Ready(ctx) || good.Syncing || good.LastError != "" || good.LastSync.IsZero() || good.Collections != 1 || good.Files != 1 {
		t.Fatalf("good = %+v", good)
	}
	broken = true
	if _, err := s.Sync(ctx, false); err == nil {
		t.Fatal("expected refresh failure")
	}
	bad := s.Status(ctx)
	if !bad.Ready || bad.LastError == "" || bad.Syncing || !bad.LastSync.Equal(good.LastSync) || bad.Files != 1 {
		t.Fatalf("refresh failure = %+v", bad)
	}
	broken = false
	if _, err := s.Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	if s.Status(ctx).LastError != "" {
		t.Fatal("last error not cleared after recovery")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if status := reopened.Status(ctx); !status.Ready || status.LastSync.IsZero() || status.Files != 1 {
		t.Fatalf("reopened = %+v", status)
	}
}

// The server barrier guarantees the first sync is still active during every rejection.
func TestSyncSharedAtomicGuard(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async=%v", async), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			s := newSyncService(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/assets/" {
					close(entered)
					<-release
					fmt.Fprint(w, assetsListing)
					return
				}
				fmt.Fprint(w, syncTorrent("Game.zip"))
			})
			ctx := context.Background()
			result := make(chan error, 1)
			if async {
				if err := s.StartSync(ctx, false); err != nil {
					t.Fatal(err)
				}
				// Assert immediately, before waiting for the HTTP handler to enter.
				if err := s.StartSync(ctx, false); !errors.Is(err, ErrSyncInProgress) {
					t.Errorf("immediate second start = %v", err)
				}
			} else {
				go func() { _, err := s.Sync(ctx, false); result <- err }()
			}
			<-entered
			if err := s.StartSync(ctx, false); !errors.Is(err, ErrSyncInProgress) {
				t.Errorf("concurrent start = %v", err)
			}
			if _, err := s.Sync(ctx, false); !errors.Is(err, ErrSyncInProgress) {
				t.Errorf("concurrent blocking sync = %v", err)
			}
			if status := s.Status(ctx); !status.Syncing || status.LastError != "" {
				t.Errorf("active = %+v", status)
			}
			s.mu.Lock()
			done := s.done
			s.mu.Unlock()
			close(release)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("sync did not finish")
			}
			if !async {
				if err := <-result; err != nil {
					t.Fatal(err)
				}
			}
			if status := s.Status(ctx); status.Syncing || status.LastError != "" || !status.Ready {
				t.Fatalf("finished = %+v", status)
			}
		})
	}
}

// Catches an abandoned background request or guard when the service is shut down.
func TestSyncCloseCancelsActiveRequest(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	s := newSyncService(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
	})
	if err := s.StartSync(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel and finish sync")
	}
	<-canceled
	if err := s.StartSync(context.Background(), false); err == nil {
		t.Fatal("closed service accepted a sync")
	}
}

// Catches validators leaking across URLs and unchanged bytes retaining obsolete download URLs.
func TestSyncNewBundleUpdatesCollectionIdentity(t *testing.T) {
	phase := 0
	s := newSyncService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/assets/" {
			if phase == 0 {
				fmt.Fprint(w, assetsListing)
			} else {
				fmt.Fprint(w, `<a href="Minerva_Myrient_v10.3/">new</a>`)
			}
			return
		}
		if phase == 1 {
			if !strings.Contains(r.URL.Path, "/Minerva_Myrient_v10.3/") {
				t.Errorf("new bundle URL = %s", r.URL.Path)
			}
			if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
				t.Errorf("old URL validators = %v", r.Header)
			}
		}
		w.Header().Set("ETag", `"same-etag"`)
		fmt.Fprint(w, syncTorrent("Game.zip"))
	})
	ctx := context.Background()
	if _, err := s.Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	phase = 1
	report, err := s.Sync(ctx, false)
	if err != nil || report != (SyncReport{Checked: 2, Updated: 2, Files: 2}) {
		t.Fatalf("new bundle report = %+v, err = %v", report, err)
	}
	hits, err := s.Search(ctx, "", "alpha", 10)
	if err != nil || len(hits) != 1 || !strings.Contains(hits[0].TorrentURL, "/Minerva_Myrient_v10.3/") {
		t.Fatalf("new bundle hits = %+v, err = %v", hits, err)
	}
}

// Catches failure to release the async guard or record a caller cancellation.
func TestSyncAsyncCallerCancellation(t *testing.T) {
	entered := make(chan struct{})
	s := newSyncService(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.StartSync(ctx, false); err != nil {
		t.Fatal(err)
	}
	<-entered
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled sync did not finish")
	}
	status := s.Status(context.Background())
	if status.Syncing || status.Ready || !strings.Contains(status.LastError, "context canceled") || !status.LastSync.IsZero() {
		t.Fatalf("canceled = %+v", status)
	}
}

// Catches rejecting the allowed boundary itself instead of the first excess byte.
func TestSyncAcceptsTorrentAtSizeLimit(t *testing.T) {
	const size = 256 * 1024 * 1024
	torrent := strings.TrimSuffix(syncTorrent("Game.zip"), "e")
	// The padding length has nine digits; subtract its framing and the final dictionary terminator.
	padding := int64(size - len(torrent) - len("7:padding000000000:") - 1)
	s := newSyncService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/assets/" {
			fmt.Fprint(w, assetsListing)
			return
		}
		if r.URL.Path == betaTorrentPath {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, "%s7:padding%d:", torrent, padding)
		_, _ = io.CopyN(w, zeroReader{}, padding)
		fmt.Fprint(w, "e")
	})
	report, err := s.Sync(context.Background(), false)
	if err != nil || report != (SyncReport{Checked: 2, Updated: 1, Missing: 1, Files: 1}) {
		t.Fatalf("boundary report = %+v, err = %v", report, err)
	}
}

func TestSyncNewURLCannotReuseOld304(t *testing.T) {
	phase := 0
	s := newSyncService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/assets/" {
			if phase == 0 {
				fmt.Fprint(w, assetsListing)
			} else {
				fmt.Fprint(w, `<a href="Minerva_Myrient_v10.3/">new</a>`)
			}
			return
		}
		if phase == 1 {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		fmt.Fprint(w, syncTorrent("Old.zip"))
	})
	ctx := context.Background()
	if _, err := s.Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	old, _, _ := s.index.Collection(ctx, "alpha")
	phase = 1
	if _, err := s.Sync(ctx, false); err == nil {
		t.Fatal("accepted 304 for a URL with no cached representation")
	}
	if rec, _, _ := s.index.Collection(ctx, "alpha"); rec != old {
		t.Fatalf("record changed: %+v", rec)
	}
}

func TestSyncEndpointQueryAndFilenameEscaping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mirror") != "test" {
			t.Errorf("lost endpoint query: %s", r.RequestURI)
		}
		if r.URL.Path == "/assets/" {
			fmt.Fprint(w, assetsListing)
			return
		}
		if r.URL.Path != alphaTorrentPath || !strings.Contains(r.RequestURI, "%20") {
			t.Errorf("escaped torrent URI = %s", r.RequestURI)
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, syncTorrent("Game.zip"))
	}))
	defer server.Close()
	s, err := Open(t.TempDir(), sources.MinervaSpec{Enabled: true, AssetsURL: server.URL + "/assets/?mirror=test", PlatformPaths: map[string]string{"alpha": "No-Intro/Alpha & Beta/"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	report, err := s.Sync(context.Background(), false)
	if err != nil || report.Updated != 1 {
		t.Fatalf("report = %+v, err = %v", report, err)
	}
}

func newSyncService(t *testing.T, handler http.HandlerFunc) *Service {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	s, err := Open(t.TempDir(), sources.MinervaSpec{
		Enabled: true, AssetsURL: server.URL + "/assets/",
		PlatformPaths: map[string]string{"alpha": "No-Intro/Alpha & Beta/", "beta": "Redump/Beta/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

// Catches missing discovery, numeric version ordering, URL escaping, and persistence.
func TestSyncFirstIndexesConfiguredCollections(t *testing.T) {
	var requests []string
	s := newSyncService(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		if r.Header.Get("User-Agent") != "Gamarr/1.0" {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("ETag", `"version-1"`)
		w.Header().Set("Last-Modified", "Mon, 07 Sep 2026 12:00:00 GMT")
		switch r.URL.Path {
		case "/assets/":
			fmt.Fprint(w, assetsListing)
		case alphaTorrentPath:
			fmt.Fprint(w, syncTorrent("Alpha.zip"))
		case betaTorrentPath:
			fmt.Fprint(w, syncTorrent("Beta.zip"))
		default:
			t.Errorf("unexpected URI %s", r.RequestURI)
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	report, err := s.Sync(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if report != (SyncReport{Checked: 2, Updated: 2, Files: 2}) {
		t.Fatalf("report = %+v", report)
	}
	if !reflect.DeepEqual(requests, []string{"/assets/", alphaTorrentPath, betaTorrentPath}) {
		t.Fatalf("requests = %v", requests)
	}
	for slug, name := range map[string]string{"alpha": "Alpha.zip", "beta": "Beta.zip"} {
		hits, err := s.Search(ctx, "", slug, 10)
		if err != nil || len(hits) != 1 || hits[0].Name != name || hits[0].InfoHash == "" {
			t.Fatalf("%s hits = %+v, err = %v", slug, hits, err)
		}
		rec, found, err := s.index.Collection(ctx, slug)
		if err != nil || !found || rec.BundleVersion != "v10.2" || rec.ETag != `"version-1"` || rec.ContentSHA256 == "" {
			t.Fatalf("record = %+v, found = %v, err = %v", rec, found, err)
		}
	}
	for _, key := range []string{"assets_etag", "assets_last_modified", "assets_body_sha256", "bundle_version", "last_sync"} {
		value, found, err := s.index.State(ctx, key)
		if err != nil || !found || value == "" {
			t.Errorf("state %s = %q, found = %v, err = %v", key, value, found, err)
		}
	}
}

// Catches unnecessary torrent requests and lost validators on lightweight checks.
func TestSyncAssetsShortCircuit(t *testing.T) {
	for _, mode := range []string{"304", "same-body", "different-stored-bundle"} {
		t.Run(mode, func(t *testing.T) {
			assetsCalls, torrentCalls := 0, 0
			s := newSyncService(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/assets/" {
					torrentCalls++
					fmt.Fprint(w, syncTorrent("Game.zip"))
					return
				}
				assetsCalls++
				if assetsCalls > 1 {
					if r.Header.Get("If-None-Match") != `"assets-1"` || r.Header.Get("If-Modified-Since") != "Mon, 07 Sep 2026 12:00:00 GMT" {
						t.Errorf("validators = %v", r.Header)
					}
					if mode == "304" {
						w.WriteHeader(http.StatusNotModified)
						return
					}
				}
				w.Header().Set("ETag", fmt.Sprintf(`"assets-%d"`, assetsCalls))
				w.Header().Set("Last-Modified", "Mon, 07 Sep 2026 12:00:00 GMT")
				fmt.Fprint(w, assetsListing)
			})
			ctx := context.Background()
			if _, err := s.Sync(ctx, false); err != nil {
				t.Fatal(err)
			}
			if mode == "different-stored-bundle" {
				if err := s.index.SetState(ctx, "bundle_version", "v1.0"); err != nil {
					t.Fatal(err)
				}
			}
			torrentCalls = 0
			if _, err := s.Sync(ctx, false); err != nil {
				t.Fatal(err)
			}
			want := 0
			if mode == "different-stored-bundle" {
				want = 2
			}
			if assetsCalls != 2 || torrentCalls != want {
				t.Fatalf("assets = %d, torrents = %d; want 2, %d", assetsCalls, torrentCalls, want)
			}
			etag, _, err := s.index.State(ctx, "assets_etag")
			wantETag := `"assets-2"`
			if mode == "304" {
				wantETag = `"assets-1"`
			}
			if err != nil || etag != wantETag {
				t.Fatalf("assets ETag = %q, err = %v", etag, err)
			}
		})
	}
}
