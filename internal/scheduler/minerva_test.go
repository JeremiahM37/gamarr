package scheduler

import (
	"context"
	"path/filepath"
	"testing"

	"gamarr/internal/config"
	"gamarr/internal/minerva"
	"gamarr/internal/models"
	"gamarr/internal/search"
	"gamarr/internal/sources"
)

func TestRunMinervaSourceHealthIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, degraded              string
		wantDownloads, wantWishlist int
	}{
		{"Prowlarr failure does not suppress Minerva", "prowlarr", 1, 0},
		{"Minerva failure suppresses Minerva", "minerva", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, source := range []string{"minerva", "prowlarr"} {
				search.ResetCircuit(source)
				t.Cleanup(func() { search.ResetCircuit(source) })
			}
			for range 3 {
				search.RecordDownloadFail(tc.degraded, "fixture download failure")
			}
			// A successful metadata query recovers search while download degradation
			// persists. This lets the real local adapter return the hit under test.
			search.RecordSearchSuccess(tc.degraded)
			if !search.IsDownloadDegraded(tc.degraded) {
				t.Fatal("source is not download-degraded")
			}

			dir := t.TempDir()
			svc, err := minerva.Open(dir, sources.MinervaSpec{Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = svc.Close() })
			idx, err := minerva.OpenIndex(filepath.Join(dir, "minerva", "index.db"))
			if err != nil {
				t.Fatal(err)
			}
			err = idx.ReplaceCollection(context.Background(), minerva.CollectionRecord{PlatformSlug: "nds", BrowsePath: "NDS/", BundleVersion: "v1", TorrentURL: "https://minerva.invalid/nds.torrent", InfoHash: "0123456789012345678901234567890123456789", ContentSHA256: "fixture"}, []minerva.FileMeta{{Index: 0, Name: "Pokemon - HeartGold (USA).nds", Path: "Nintendo - DS/Pokemon - HeartGold (USA).nds", Size: 134217728}})
			_ = idx.Close()
			if err != nil {
				t.Fatal(err)
			}
			store := newTestStore(t)
			if _, err := store.AddWishlistItem("Pokemon - HeartGold (USA).nds", "DS", "nds"); err != nil {
				t.Fatal(err)
			}
			searchFn := func(query, slug string) []*models.SearchResult {
				return search.ScoreResults(search.SearchMinerva(svc, query, slug), query, slug)
			}
			// Only the outbound downloader is substituted; run, ranking, source
			// health, the local index, wishlist persistence and activity are real.
			var selected *models.SearchResult
			downloadFn := func(result *models.SearchResult) (string, error) { selected = result; return "selected-job", nil }
			s := New(&config.Config{SchedulerEnabled: true, SchedulerAutoDownload: true, SchedulerMinScore: 70}, store, searchFn, downloadFn, nil)
			s.run()
			status := s.Status()
			if status["last_results"] != 1 {
				t.Fatalf("local Minerva hit missing: status=%+v", status)
			}
			if status["auto_downloads"] != tc.wantDownloads {
				t.Errorf("auto_downloads=%v, want %d", status["auto_downloads"], tc.wantDownloads)
			}
			if got := len(store.GetWishlist()); got != tc.wantWishlist {
				t.Errorf("wishlist count=%d, want %d", got, tc.wantWishlist)
			}
			if tc.wantDownloads == 1 {
				if selected == nil || selected.Indexer != "Minerva" || selected.TorrentFileIndex == nil || *selected.TorrentFileIndex != 0 {
					t.Fatalf("selected result=%+v", selected)
				}
			} else if selected != nil {
				t.Errorf("downloaded degraded Minerva result: %+v", selected)
			}
		})
	}
}
