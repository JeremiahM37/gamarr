package search

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"gamarr/internal/minerva"
	"gamarr/internal/sources"
)

func seededMinerva(t *testing.T, seed bool) (*minerva.Service, string) {
	t.Helper()
	dir := t.TempDir()
	svc, err := minerva.Open(dir, sources.MinervaSpec{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	dbPath := filepath.Join(dir, "minerva", "index.db")
	if seed {
		idx, err := minerva.OpenIndex(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer idx.Close()
		err = idx.ReplaceCollection(context.Background(), minerva.CollectionRecord{
			PlatformSlug: "nds", BrowsePath: "No-Intro/Nintendo - DS/", BundleVersion: "v1",
			TorrentURL: "https://minerva.invalid/nds.torrent", InfoHash: "0123456789012345678901234567890123456789", ContentSHA256: "fixture",
		}, []minerva.FileMeta{
			{Index: 7, Name: "Pokemon - HeartGold (USA).nds", Path: "Nintendo - DS/Pokemon - HeartGold (USA).nds", Size: 134217728},
			{Index: 8, Name: "Pokemon - SoulSilver (USA).nds", Path: "Nintendo - DS/Pokemon - SoulSilver (USA).nds", Size: 268435456},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return svc, dbPath
}

func TestSearchMinervaMapsSelectedFile(t *testing.T) {
	resetHealthStore()
	t.Cleanup(resetHealthStore)
	svc, _ := seededMinerva(t, true)
	hits := SearchMinerva(svc, "pokemon heartgold", "nds")
	if len(hits) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	hit := hits[0]
	if hit.Title != "Pokemon - HeartGold (USA).nds" || hit.Platform != "DS" || hit.PlatformSlug != "nds" || hit.IsPC {
		t.Fatalf("title/platform mapping: %+v", hit)
	}
	if hit.Indexer != "Minerva" || hit.SourceType != "torrent" || hit.DownloadProtocol != "torrent" || hit.Seeders != 0 || hit.SafetyScore != 95 {
		t.Fatalf("source/safety mapping: %+v", hit)
	}
	if hit.DownloadURL != "https://minerva.invalid/nds.torrent" || hit.InfoHash != "0123456789012345678901234567890123456789" || hit.MagnetURL != "" {
		t.Fatalf("torrent identity: %+v", hit)
	}
	if hit.TorrentFileIndex == nil || *hit.TorrentFileIndex != 7 || hit.TorrentFilePath != "Nintendo - DS/Pokemon - HeartGold (USA).nds" || hit.TorrentFileSize != 134217728 || hit.Size != 134217728 || hit.SizeHuman != "128.0 MB" {
		t.Fatalf("selected file: %+v", hit)
	}
	if hit.GUID != "minerva:0123456789012345678901234567890123456789:7" {
		t.Fatalf("GUID = %q", hit.GUID)
	}
	if health := GetSourceHealth("minerva"); health == nil || health.SearchOK != 1 || health.SearchFail != 0 {
		t.Fatalf("health = %+v", health)
	}
}

func TestSearchMinervaSkipsUnavailableWithoutHealthChanges(t *testing.T) {
	ready, _ := seededMinerva(t, true)
	empty, _ := seededMinerva(t, false)
	for _, tc := range []struct {
		name    string
		svc     *minerva.Service
		slug    string
		circuit bool
	}{
		{"nil", nil, "nds", false}, {"not ready", empty, "nds", false},
		{"empty platform", ready, "", false}, {"all", ready, "all", false}, {"circuit", ready, "nds", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetHealthStore()
			t.Cleanup(resetHealthStore)
			if tc.circuit {
				for range circuitThreshold {
					RecordSearchFail("minerva", "fixture")
				}
			}
			before := GetSourceHealth("minerva")
			if hits := SearchMinerva(tc.svc, "pokemon", tc.slug); hits != nil {
				t.Fatalf("hits = %+v", hits)
			}
			after := GetSourceHealth("minerva")
			if before == nil {
				if after != nil {
					t.Fatalf("skip recorded health: %+v", after)
				}
			} else if after.SearchOK != before.SearchOK || after.SearchFail != before.SearchFail {
				t.Fatalf("skip changed health: %+v", after)
			}
		})
	}
}

func TestSearchMinervaHealthTracksLocalQueries(t *testing.T) {
	resetHealthStore()
	t.Cleanup(resetHealthStore)
	svc, dbPath := seededMinerva(t, true)
	if hits := SearchMinerva(svc, "missing", "nds"); hits != nil {
		t.Fatalf("hits = %+v", hits)
	}
	if h := GetSourceHealth("minerva"); h == nil || h.SearchOK != 1 || h.SearchFail != 0 {
		t.Fatalf("empty successful query: %+v", h)
	}
	// Keep readiness counts intact while making the real file query fail.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("ALTER TABLE minerva_files RENAME COLUMN name TO broken_name"); err != nil {
		t.Fatal(err)
	}
	if hits := SearchMinerva(svc, "pokemon", "nds"); hits != nil {
		t.Fatalf("hits = %+v", hits)
	}
	if h := GetSourceHealth("minerva"); h == nil || h.SearchOK != 1 || h.SearchFail != 1 || h.LastError == "" {
		t.Fatalf("failed local query: %+v", h)
	}
}
