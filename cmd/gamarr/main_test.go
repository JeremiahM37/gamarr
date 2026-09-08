package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gamarr/internal/config"
	"gamarr/internal/db"
	"gamarr/internal/download"
	"gamarr/internal/minerva"
	"gamarr/internal/models"
	"gamarr/internal/qbit"
	"gamarr/internal/search"
	"gamarr/internal/sources"
)

func TestSchedulerMinervaSearch(t *testing.T) {
	search.SetVimmMinIntervalForTest(0)
	t.Cleanup(func() { search.SetVimmMinIntervalForTest(5 * time.Second) })
	for _, enabled := range []bool{true, false} {
		name := "enabled"
		if !enabled {
			name = "disabled"
		}
		t.Run(name, func(t *testing.T) {
			search.ResetCircuit("minerva")
			var networkCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/minerva" {
					networkCalls.Add(1)
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer upstream.Close()
			cfg := &config.Config{DataDir: t.TempDir(), Sources: &sources.Registry{
				Vimm: sources.VimmSpec{BaseURL: upstream.URL}, Minerva: sources.MinervaSpec{Enabled: enabled, AssetsURL: upstream.URL + "/minerva"},
			}}
			var svc *minerva.Service
			if enabled {
				var err error
				svc, err = minerva.Open(cfg.DataDir, cfg.Sources.Minerva)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = svc.Close() })
				idx, err := minerva.OpenIndex(filepath.Join(cfg.DataDir, "minerva", "index.db"))
				if err != nil {
					t.Fatal(err)
				}
				err = idx.ReplaceCollection(context.Background(), minerva.CollectionRecord{PlatformSlug: "nds", BrowsePath: "NDS/", BundleVersion: "v1", TorrentURL: "https://minerva.invalid/nds.torrent", InfoHash: "0123456789012345678901234567890123456789", ContentSHA256: "fixture"}, []minerva.FileMeta{{Index: 7, Name: "Pokemon - HeartGold (USA).nds", Path: "Nintendo - DS/Pokemon - HeartGold (USA).nds", Size: 134217728}})
				_ = idx.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			searchFn := newSchedulerSearch(cfg, svc)
			before := search.GetSourceHealth("minerva")
			done := make(chan []*models.SearchResult, 1)
			go func() { done <- searchFn("heartgold", "nds") }()
			var results []*models.SearchResult
			select {
			case results = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("scheduler fan-out did not finish")
			}
			wantCount := 0
			if enabled {
				wantCount = 1
			}
			if results == nil || len(results) != wantCount {
				t.Fatalf("results = %+v", results)
			}
			if enabled {
				hit := results[0]
				if hit.Indexer != "Minerva" || hit.SourceType != "torrent" || hit.DownloadProtocol != "torrent" || hit.Seeders != 0 || hit.SafetyScore != 95 || hit.Size != 134217728 || hit.TorrentFileIndex == nil || *hit.TorrentFileIndex != 7 || hit.TorrentFilePath != "Nintendo - DS/Pokemon - HeartGold (USA).nds" || hit.TorrentFileSize != 134217728 || hit.ScoreBreakdown == nil {
					t.Fatalf("Minerva hit = %+v", hit)
				}
			}
			after := search.GetSourceHealth("minerva")
			beforeOK, beforeFail, afterOK, afterFail := 0, 0, 0, 0
			if before != nil {
				beforeOK, beforeFail = before.SearchOK, before.SearchFail
			}
			if after != nil {
				afterOK, afterFail = after.SearchOK, after.SearchFail
			}
			if afterOK != beforeOK+wantCount || afterFail != beforeFail {
				t.Fatalf("health before=%+v after=%+v", before, after)
			}
			if hits := searchFn("heartgold", "all"); len(hits) != 0 {
				t.Fatalf("all-platform results = %+v", hits)
			}
			if networkCalls.Load() != 0 {
				t.Fatalf("search fetched Minerva metadata %d times", networkCalls.Load())
			}
			if !enabled {
				if _, err := os.Stat(filepath.Join(cfg.DataDir, "minerva")); !os.IsNotExist(err) {
					t.Fatalf("disabled source created an index: %v", err)
				}
			}
		})
	}
}

func schedulerSelection() *models.SearchResult {
	index := 0
	return &models.SearchResult{Title: "Pokemon - HeartGold (USA).nds", Platform: "DS", PlatformSlug: "nds", SourceType: "torrent", DownloadProtocol: "torrent", DownloadURL: "https://minerva.invalid/nds.torrent", InfoHash: "0123456789012345678901234567890123456789", TorrentFileIndex: &index, TorrentFilePath: "Nintendo - DS/Pokemon - HeartGold (USA).nds", TorrentFileSize: 134217728}
}

func TestSchedulerSelectiveRouting(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*models.SearchResult)
		want   string
	}{
		{"requires qbit", func(r *models.SearchResult) {}, "Minerva requires qBittorrent"},
		{"before ddl", func(r *models.SearchResult) { r.SourceType = "ddl"; r.DownloadURL = "" }, "Selective torrent requires"},
		{"before nzb", func(r *models.SearchResult) { r.DownloadProtocol = "nzb" }, "Minerva requires qBittorrent"},
		{"missing url", func(r *models.SearchResult) {
			r.DownloadURL = ""
			r.MagnetURL = "magnet:?xt=urn:btih:0123456789012345678901234567890123456789"
		}, "Selective torrent requires"},
		{"missing hash", func(r *models.SearchResult) { r.InfoHash = "" }, "Selective torrent requires"},
		{"blank path", func(r *models.SearchResult) { r.TorrentFilePath = " \t" }, "Selective torrent requires"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jobs, err := db.New(filepath.Join(t.TempDir(), "jobs.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer jobs.Close()
			mgr := download.New(&config.Config{}, jobs, nil)
			r := schedulerSelection()
			tc.change(r)
			id, err := newSchedulerDownload(mgr, nil)(r)
			if err == nil || !strings.Contains(err.Error(), tc.want) || id != "" {
				t.Fatalf("id=%q err=%v, want %q", id, err, tc.want)
			}
			if len(jobs.Items()) != 0 {
				t.Fatalf("selection reached generic routing: %+v", jobs.Items())
			}
		})
	}
}

func TestSchedulerDownloadPersistsSelection(t *testing.T) {
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			_, _ = w.Write([]byte("Ok."))
			return
		}
		http.Error(w, "client unavailable", http.StatusServiceUnavailable)
	}))
	defer qb.Close()
	jobs, err := db.New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Close()
	cfg := &config.Config{QBURL: qb.URL}
	mgr := download.New(cfg, jobs, qbit.New(qb.URL, "", ""))
	id, err := newSchedulerDownload(mgr, nil)(schedulerSelection())
	if err != nil {
		t.Fatal(err)
	}
	job, ok := jobs.Get(id)
	if !ok {
		t.Fatal("no persisted job")
	}
	encoded, _ := json.Marshal(job)
	var saved map[string]interface{}
	_ = json.Unmarshal(encoded, &saved)
	for key, want := range map[string]interface{}{"source": "minerva", "download_url": "https://minerva.invalid/nds.torrent", "info_hash": "0123456789012345678901234567890123456789", "torrent_file_index": float64(0), "torrent_file_path": "Nintendo - DS/Pokemon - HeartGold (USA).nds", "torrent_file_size": float64(134217728), "title": "Pokemon - HeartGold (USA).nds", "platform": "DS", "platform_slug": "nds"} {
		if saved[key] != want {
			t.Errorf("job[%s]=%v, want %v", key, saved[key], want)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		job, _ := jobs.Get(id)
		if job["status"] == "error" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not finish after client failure")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
