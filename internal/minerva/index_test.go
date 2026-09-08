package minerva

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIndexCollectionStateAndCounts(t *testing.T) {
	ctx := context.Background()
	idx, err := OpenIndex(filepath.Join(t.TempDir(), "minerva.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	record := CollectionRecord{
		PlatformSlug:  "nds",
		BrowsePath:    "/roms/nds",
		BundleVersion: "2026.09.08",
		TorrentURL:    "https://example.test/nds.torrent",
		InfoHash:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ETag:          "\"v1\"",
		LastModified:  "Mon, 08 Sep 2026 12:00:00 GMT",
		ContentSHA256: "sha256-a",
	}
	files := []FileMeta{{Index: 7, Path: "Pokemon HeartGold.nds", Name: "Pokemon HeartGold.nds", Size: 134217728}}
	if err := idx.ReplaceCollection(ctx, record, files); err != nil {
		t.Fatal(err)
	}

	got, found, err := idx.Collection(ctx, "nds")
	if err != nil || !found || got != record {
		t.Fatalf("Collection() = (%+v, %t, %v), want (%+v, true, nil)", got, found, err, record)
	}
	if err := idx.SetState(ctx, "minerva.sync_cursor", "next"); err != nil {
		t.Fatal(err)
	}
	value, found, err := idx.State(ctx, "minerva.sync_cursor")
	if err != nil || !found || value != "next" {
		t.Fatalf("State() = (%q, %t, %v), want (next, true, nil)", value, found, err)
	}
	collections, fileCount, err := idx.Counts(ctx)
	if err != nil || collections != 1 || fileCount != 1 {
		t.Fatalf("Counts() = (%d, %d, %v), want (1, 1, nil)", collections, fileCount, err)
	}
}

func TestSearchReturnsTorrentDetailsAndIsolatesPlatform(t *testing.T) {
	ctx := context.Background()
	idx, err := OpenIndex(filepath.Join(t.TempDir(), "minerva.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	nds := CollectionRecord{
		PlatformSlug: "nds", BrowsePath: "/nds", BundleVersion: "1", TorrentURL: "https://example.test/nds.torrent",
		InfoHash: strings.Repeat("a", 40), ContentSHA256: "sha256-nds",
	}
	if err := idx.ReplaceCollection(ctx, nds, []FileMeta{
		{Index: 7, Path: "Pokemon HeartGold.nds", Name: "Pokemon HeartGold.nds", Size: 134217728},
		{Index: 8, Path: "Pokemon Crystal.nds", Name: "Pokemon Crystal.nds", Size: 111},
	}); err != nil {
		t.Fatal(err)
	}
	snes := CollectionRecord{
		PlatformSlug: "snes", BrowsePath: "/snes", BundleVersion: "1", TorrentURL: "https://example.test/snes.torrent",
		InfoHash: strings.Repeat("b", 40), ContentSHA256: "sha256-snes",
	}
	if err := idx.ReplaceCollection(ctx, snes, []FileMeta{{Index: 9, Path: "Pokemon HeartGold.sfc", Name: "Pokemon HeartGold.sfc", Size: 222}}); err != nil {
		t.Fatal(err)
	}

	hits, err := idx.Search(ctx, "PoKeMoN\t HEARTgold", "nds", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].FileIndex != 7 || hits[0].Path != "Pokemon HeartGold.nds" || hits[0].InfoHash != strings.Repeat("a", 40) || hits[0].TorrentURL != nds.TorrentURL || hits[0].Size != 134217728 {
		t.Fatalf("hits=%+v", hits)
	}
	hits, err = idx.Search(ctx, "pokemon crystal", "nds", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].FileIndex != 8 {
		t.Fatalf("AND search hits=%+v", hits)
	}
	hits, err = idx.Search(ctx, "pokemon heartgold", "snes", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].PlatformSlug != "snes" || hits[0].InfoHash != strings.Repeat("b", 40) {
		t.Fatalf("platform search hits=%+v", hits)
	}
	if _, err := idx.Search(ctx, "pokemon", "", 20); err == nil {
		t.Fatal("Search with empty platform slug succeeded")
	}
}

func TestIndexForeignKeysApplyToNewConnections(t *testing.T) {
	ctx := context.Background()
	idx, err := OpenIndex(filepath.Join(t.TempDir(), "minerva.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	record := CollectionRecord{
		PlatformSlug: "nds", BrowsePath: "/nds", BundleVersion: "1", TorrentURL: "https://example.test/nds.torrent",
		InfoHash: strings.Repeat("a", 40), ContentSHA256: "sha256-nds",
	}
	if err := idx.ReplaceCollection(ctx, record, []FileMeta{{Index: 0, Path: "Game.nds", Name: "Game.nds", Size: 1}}); err != nil {
		t.Fatal(err)
	}

	idx.db.SetConnMaxLifetime(time.Nanosecond)
	time.Sleep(time.Millisecond)
	if _, err := idx.db.ExecContext(ctx, `DELETE FROM minerva_collections WHERE platform_slug = ?`, "nds"); err != nil {
		t.Fatal(err)
	}
	collections, files, err := idx.Counts(ctx)
	if err != nil || collections != 0 || files != 0 {
		t.Fatalf("Counts after cascade = (%d, %d, %v), want (0, 0, nil)", collections, files, err)
	}
	if _, err := idx.db.ExecContext(ctx, `
INSERT INTO minerva_files (platform_slug, file_index, path, name, size)
VALUES (?, ?, ?, ?, ?)`, "missing", 0, "Orphan.nds", "Orphan.nds", 1); err == nil {
		t.Fatal("orphan file insert succeeded after connection replacement")
	}
}

func TestSearchTreatsLikeWildcardsAsLiteralTokens(t *testing.T) {
	ctx := context.Background()
	idx, err := OpenIndex(filepath.Join(t.TempDir(), "minerva.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	if err := idx.ReplaceCollection(ctx, CollectionRecord{
		PlatformSlug: "nds", BrowsePath: "/nds", BundleVersion: "1", TorrentURL: "https://example.test/nds.torrent",
		InfoHash: strings.Repeat("a", 40), ContentSHA256: "sha256-nds",
	}, []FileMeta{
		{Index: 1, Path: "Percent%_Back\\Slash.nds", Name: "Percent%_Back\\Slash.nds", Size: 1},
		{Index: 2, Path: "PercentZZBack\\Slash.nds", Name: "PercentZZBack\\Slash.nds", Size: 2},
		{Index: 3, Path: "Percent%XBack\\Slash.nds", Name: "Percent%XBack\\Slash.nds", Size: 3},
	}); err != nil {
		t.Fatal(err)
	}

	hits, err := idx.Search(ctx, `percent%_back\slash`, "nds", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].FileIndex != 1 || hits[0].Name != "Percent%_Back\\Slash.nds" {
		t.Fatalf("literal wildcard hits=%+v", hits)
	}
}

func TestSearchUsesDefaultAndCappedLimits(t *testing.T) {
	ctx := context.Background()
	idx, err := OpenIndex(filepath.Join(t.TempDir(), "minerva.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	files := make([]FileMeta, 105)
	for n := range files {
		name := fmt.Sprintf("Pokemon HeartGold %03d.nds", n)
		files[n] = FileMeta{Index: n, Path: name, Name: name, Size: int64(n)}
	}
	if err := idx.ReplaceCollection(ctx, CollectionRecord{
		PlatformSlug: "nds", BrowsePath: "/nds", BundleVersion: "1", TorrentURL: "https://example.test/nds.torrent",
		InfoHash: strings.Repeat("a", 40), ContentSHA256: "sha256-nds",
	}, files); err != nil {
		t.Fatal(err)
	}

	defaultHits, err := idx.Search(ctx, "pokemon heartgold", "nds", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultHits) != 20 {
		t.Fatalf("default hits=%d, want 20", len(defaultHits))
	}
	cappedHits, err := idx.Search(ctx, "pokemon heartgold", "nds", 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(cappedHits) != 100 {
		t.Fatalf("capped hits=%d, want 100", len(cappedHits))
	}
}

func TestIndexReplaceCollectionRollsBackOnFileInsertFailure(t *testing.T) {
	ctx := context.Background()
	idx, err := OpenIndex(filepath.Join(t.TempDir(), "minerva.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	old := CollectionRecord{
		PlatformSlug: "nds", BrowsePath: "/old", BundleVersion: "old", TorrentURL: "https://example.test/old.torrent",
		InfoHash: strings.Repeat("a", 40), ETag: "old-etag", LastModified: "old-time", ContentSHA256: "old-sha",
	}
	if err := idx.ReplaceCollection(ctx, old, []FileMeta{{Index: 1, Path: "Old Game.nds", Name: "Old Game.nds", Size: 1}}); err != nil {
		t.Fatal(err)
	}
	replacement := CollectionRecord{
		PlatformSlug: "nds", BrowsePath: "/new", BundleVersion: "new", TorrentURL: "https://example.test/new.torrent",
		InfoHash: strings.Repeat("b", 40), ETag: "new-etag", LastModified: "new-time", ContentSHA256: "new-sha",
	}
	err = idx.ReplaceCollection(ctx, replacement, []FileMeta{
		{Index: 0, Path: "New One.nds", Name: "New One.nds", Size: 2},
		{Index: 0, Path: "New Duplicate.nds", Name: "New Duplicate.nds", Size: 3},
	})
	if err == nil {
		t.Fatal("ReplaceCollection with duplicate file index succeeded")
	}

	got, found, err := idx.Collection(ctx, "nds")
	if err != nil || !found || got != old {
		t.Fatalf("Collection after rollback = (%+v, %t, %v), want (%+v, true, nil)", got, found, err, old)
	}
	hits, err := idx.Search(ctx, "old game", "nds", 20)
	if err != nil || len(hits) != 1 || hits[0].FileIndex != 1 || hits[0].Name != "Old Game.nds" {
		t.Fatalf("old file rows after rollback = (%+v, %v)", hits, err)
	}
}

func TestIndexUpdateValidators(t *testing.T) {
	ctx := context.Background()
	idx, err := OpenIndex(filepath.Join(t.TempDir(), "minerva.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	record := CollectionRecord{
		PlatformSlug: "nds", BrowsePath: "/nds", BundleVersion: "1", TorrentURL: "https://example.test/nds.torrent",
		InfoHash: strings.Repeat("a", 40), ETag: "old", LastModified: "old-time", ContentSHA256: "sha256-nds",
	}
	if err := idx.ReplaceCollection(ctx, record, nil); err != nil {
		t.Fatal(err)
	}
	if err := idx.UpdateValidators(ctx, "nds", "new", "new-time"); err != nil {
		t.Fatal(err)
	}
	got, found, err := idx.Collection(ctx, "nds")
	if err != nil || !found {
		t.Fatalf("Collection() = (%+v, %t, %v)", got, found, err)
	}
	record.ETag, record.LastModified = "new", "new-time"
	if got != record {
		t.Fatalf("Collection() = %+v, want %+v", got, record)
	}
}
