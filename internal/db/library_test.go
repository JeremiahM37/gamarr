package db

import (
	"testing"
)

func TestLibraryItem_CRUD(t *testing.T) {
	store := newTestStore(t)

	// Add item
	item := &LibraryItem{
		Title:        "Super Mario",
		Platform:     "Switch",
		PlatformSlug: "switch",
		IsPC:         false,
		FilePath:     "/data/roms/switch/mario.nsp",
		FileSize:     5000000000,
		Source:       "torrent",
		SourceType:   "prowlarr",
		SourceID:     "torrent:abc123",
		Metadata:     "{}",
	}
	id, err := store.AddLibraryItem(item)
	if err != nil {
		t.Fatalf("AddLibraryItem failed: %v", err)
	}
	if id <= 0 {
		t.Error("expected positive ID")
	}

	// Get page
	page := store.GetLibraryPage(1, 50, "", "")
	if page.Total != 1 {
		t.Errorf("total=%d, want 1", page.Total)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items=%d, want 1", len(page.Items))
	}
	if page.Items[0].Title != "Super Mario" {
		t.Errorf("title=%q", page.Items[0].Title)
	}
	if page.Items[0].IsPC {
		t.Error("expected IsPC=false")
	}

	// Delete
	err = store.DeleteLibraryItem(id)
	if err != nil {
		t.Fatalf("DeleteLibraryItem failed: %v", err)
	}
	page = store.GetLibraryPage(1, 50, "", "")
	if page.Total != 0 {
		t.Errorf("total=%d after delete, want 0", page.Total)
	}
}

func TestLibraryPage_Pagination(t *testing.T) {
	store := newTestStore(t)

	for i := 0; i < 5; i++ {
		store.AddLibraryItem(&LibraryItem{
			Title:    "Game " + string(rune('A'+i)),
			Platform: "PC",
			IsPC:     true,
			Metadata: "{}",
		})
	}

	// Page 1, size 2
	page := store.GetLibraryPage(1, 2, "", "")
	if page.Total != 5 {
		t.Errorf("total=%d, want 5", page.Total)
	}
	if page.TotalPages != 3 {
		t.Errorf("total_pages=%d, want 3", page.TotalPages)
	}
	if len(page.Items) != 2 {
		t.Errorf("items=%d, want 2", len(page.Items))
	}

	// Page 3
	page3 := store.GetLibraryPage(3, 2, "", "")
	if len(page3.Items) != 1 {
		t.Errorf("page 3 items=%d, want 1", len(page3.Items))
	}
}

func TestLibraryPage_QueryFilter(t *testing.T) {
	store := newTestStore(t)
	store.AddLibraryItem(&LibraryItem{Title: "Super Mario Bros", Platform: "NES", PlatformSlug: "nes", Metadata: "{}"})
	store.AddLibraryItem(&LibraryItem{Title: "Zelda", Platform: "NES", PlatformSlug: "nes", Metadata: "{}"})

	page := store.GetLibraryPage(1, 50, "mario", "")
	if page.Total != 1 {
		t.Errorf("query filter: total=%d, want 1", page.Total)
	}
}

func TestLibraryPage_PlatformFilter(t *testing.T) {
	store := newTestStore(t)
	store.AddLibraryItem(&LibraryItem{Title: "Mario", Platform: "NES", PlatformSlug: "nes", Metadata: "{}"})
	store.AddLibraryItem(&LibraryItem{Title: "Halo", Platform: "PC", PlatformSlug: "", IsPC: true, Metadata: "{}"})

	// Filter by nes
	page := store.GetLibraryPage(1, 50, "", "nes")
	if page.Total != 1 {
		t.Errorf("platform filter: total=%d, want 1", page.Total)
	}

	// Filter by pc
	page = store.GetLibraryPage(1, 50, "", "pc")
	if page.Total != 1 {
		t.Errorf("pc filter: total=%d, want 1", page.Total)
	}
}

func TestLibraryPage_Defaults(t *testing.T) {
	store := newTestStore(t)
	// page < 1 and pageSize < 1 should use defaults
	page := store.GetLibraryPage(0, 0, "", "")
	if page.Page != 1 {
		t.Errorf("page=%d, want 1", page.Page)
	}
	if page.PageSize != 50 {
		t.Errorf("page_size=%d, want 50", page.PageSize)
	}
}

func TestLibraryHasSourceID(t *testing.T) {
	store := newTestStore(t)

	// Empty sourceID always returns false
	if store.LibraryHasSourceID("") {
		t.Error("empty sourceID should return false")
	}

	store.AddLibraryItem(&LibraryItem{
		Title:    "Game",
		SourceID: "torrent:xyz",
		Metadata: "{}",
	})

	if !store.LibraryHasSourceID("torrent:xyz") {
		t.Error("expected source ID to exist")
	}
	if store.LibraryHasSourceID("torrent:missing") {
		t.Error("expected missing source ID to not exist")
	}
}

func TestLibraryStats(t *testing.T) {
	store := newTestStore(t)
	store.AddLibraryItem(&LibraryItem{Title: "A", Platform: "NES", PlatformSlug: "nes", Metadata: "{}"})
	store.AddLibraryItem(&LibraryItem{Title: "B", Platform: "NES", PlatformSlug: "nes", Metadata: "{}"})
	store.AddLibraryItem(&LibraryItem{Title: "C", Platform: "PC", PlatformSlug: "", IsPC: true, Metadata: "{}"})

	stats := store.LibraryStats()
	if stats["nes"] != 2 {
		t.Errorf("nes count=%d, want 2", stats["nes"])
	}
	if stats["pc"] != 1 {
		t.Errorf("pc count=%d, want 1", stats["pc"])
	}
}

func TestLibraryTotal(t *testing.T) {
	store := newTestStore(t)
	if store.LibraryTotal() != 0 {
		t.Error("expected 0 initially")
	}
	store.AddLibraryItem(&LibraryItem{Title: "Game", Metadata: "{}"})
	if store.LibraryTotal() != 1 {
		t.Error("expected 1 after add")
	}
}

func TestRecentLibraryItems(t *testing.T) {
	store := newTestStore(t)
	for i := 0; i < 5; i++ {
		store.AddLibraryItem(&LibraryItem{Title: "Game " + string(rune('A'+i)), Metadata: "{}"})
	}

	recent := store.RecentLibraryItems(3)
	if len(recent) != 3 {
		t.Errorf("recent items=%d, want 3", len(recent))
	}
}

func TestWishlist_CRUD(t *testing.T) {
	store := newTestStore(t)

	// Add
	id, err := store.AddWishlistItem("Zelda", "Switch", "switch")
	if err != nil {
		t.Fatalf("AddWishlistItem failed: %v", err)
	}
	if id <= 0 {
		t.Error("expected positive ID")
	}

	// Get
	items := store.GetWishlist()
	if len(items) != 1 {
		t.Fatalf("wishlist items=%d, want 1", len(items))
	}
	if items[0].Title != "Zelda" {
		t.Errorf("title=%q", items[0].Title)
	}

	// Delete
	store.DeleteWishlistItem(id)
	items = store.GetWishlist()
	if len(items) != 0 {
		t.Errorf("wishlist items=%d after delete, want 0", len(items))
	}
}

func TestActivityLog(t *testing.T) {
	store := newTestStore(t)

	var libID int64 = 42
	store.LogActivity("download_started", "Game A", "Starting download", "job1", &libID)
	store.LogActivity("download_completed", "Game A", "Completed", "job1", nil)

	entries, total := store.GetActivity(1, 50)
	if total != 2 {
		t.Errorf("total=%d, want 2", total)
	}
	if len(entries) != 2 {
		t.Fatalf("entries=%d, want 2", len(entries))
	}
	// Most recent first
	if entries[0].EventType != "download_completed" {
		t.Errorf("first entry event=%q, want download_completed", entries[0].EventType)
	}
}

func TestActivityLog_Pagination(t *testing.T) {
	store := newTestStore(t)
	for i := 0; i < 5; i++ {
		store.LogActivity("test", "Game", "detail", "", nil)
	}

	entries, total := store.GetActivity(1, 2)
	if total != 5 {
		t.Errorf("total=%d, want 5", total)
	}
	if len(entries) != 2 {
		t.Errorf("entries=%d, want 2", len(entries))
	}
}

func TestClearScanEntries(t *testing.T) {
	store := newTestStore(t)
	store.AddLibraryItem(&LibraryItem{Title: "Scanned", Source: "scan", Metadata: "{}"})
	store.AddLibraryItem(&LibraryItem{Title: "Downloaded", Source: "torrent", Metadata: "{}"})

	store.ClearScanEntries()

	if store.LibraryTotal() != 1 {
		t.Errorf("expected 1 item after clearing scan entries, got %d", store.LibraryTotal())
	}
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		size int64
		want string
	}{
		{0, "?"},
		{500, "500.0 B"},
		{1024, "1.0 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
		{1099511627776, "1.0 TB"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := FormatSize(tt.size)
			if got != tt.want {
				t.Errorf("FormatSize(%d) = %q, want %q", tt.size, got, tt.want)
			}
		})
	}
}

func TestBoolToInt(t *testing.T) {
	if boolToInt(true) != 1 {
		t.Error("boolToInt(true) should be 1")
	}
	if boolToInt(false) != 0 {
		t.Error("boolToInt(false) should be 0")
	}
}
