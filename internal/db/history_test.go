package db

import (
	"testing"
)

func TestPlayHistory_CRUD(t *testing.T) {
	store := newTestStore(t)

	// Add
	entry := &PlayHistoryEntry{
		GameTitle:    "Zelda",
		Platform:     "Switch",
		PlatformSlug: "switch",
		Rating:       9,
		Notes:        "Great game",
		HoursPlayed:  45.5,
	}
	id, err := store.AddPlayHistory(entry)
	if err != nil {
		t.Fatalf("AddPlayHistory: %v", err)
	}
	if id <= 0 {
		t.Error("expected positive ID")
	}

	// Get
	entries, total, err := store.GetPlayHistory("default", "", 50, 0)
	if err != nil {
		t.Fatalf("GetPlayHistory: %v", err)
	}
	if total != 1 {
		t.Errorf("total=%d, want 1", total)
	}
	if len(entries) != 1 {
		t.Fatalf("entries=%d, want 1", len(entries))
	}
	if entries[0].GameTitle != "Zelda" {
		t.Errorf("GameTitle=%q", entries[0].GameTitle)
	}
	if entries[0].Rating != 9 {
		t.Errorf("Rating=%d, want 9", entries[0].Rating)
	}
	if entries[0].HoursPlayed != 45.5 {
		t.Errorf("HoursPlayed=%f, want 45.5", entries[0].HoursPlayed)
	}
	if entries[0].UserID != "default" {
		t.Errorf("UserID=%q, want 'default'", entries[0].UserID)
	}

	// Update
	err = store.UpdatePlayHistory(id, map[string]interface{}{
		"rating":       10,
		"hours_played": 60.0,
		"notes":        "Masterpiece",
	})
	if err != nil {
		t.Fatalf("UpdatePlayHistory: %v", err)
	}
	entries, _, _ = store.GetPlayHistory("default", "", 50, 0)
	if entries[0].Rating != 10 {
		t.Errorf("after update: Rating=%d, want 10", entries[0].Rating)
	}
	if entries[0].HoursPlayed != 60.0 {
		t.Errorf("after update: HoursPlayed=%f, want 60.0", entries[0].HoursPlayed)
	}
	if entries[0].Notes != "Masterpiece" {
		t.Errorf("after update: Notes=%q, want 'Masterpiece'", entries[0].Notes)
	}

	// Delete
	err = store.DeletePlayHistory(id)
	if err != nil {
		t.Fatalf("DeletePlayHistory: %v", err)
	}
	_, total, _ = store.GetPlayHistory("default", "", 50, 0)
	if total != 0 {
		t.Errorf("after delete: total=%d, want 0", total)
	}
}

func TestPlayHistory_DefaultUserID(t *testing.T) {
	store := newTestStore(t)

	entry := &PlayHistoryEntry{
		GameTitle: "Game",
	}
	store.AddPlayHistory(entry)

	entries, _, _ := store.GetPlayHistory("", "", 50, 0)
	if len(entries) != 1 {
		t.Fatalf("entries=%d, want 1", len(entries))
	}
	if entries[0].UserID != "default" {
		t.Errorf("UserID=%q, want 'default'", entries[0].UserID)
	}
}

func TestPlayHistory_StatusFilter(t *testing.T) {
	store := newTestStore(t)

	// Playing (no finished_at)
	store.AddPlayHistory(&PlayHistoryEntry{GameTitle: "Playing Game"})

	// Finished
	store.AddPlayHistory(&PlayHistoryEntry{
		GameTitle:  "Finished Game",
		FinishedAt: "2026-01-01T00:00:00Z",
	})

	// Filter: playing
	entries, total, _ := store.GetPlayHistory("default", "playing", 50, 0)
	if total != 1 {
		t.Errorf("playing filter: total=%d, want 1", total)
	}
	if len(entries) != 1 {
		t.Fatalf("playing filter: entries=%d, want 1", len(entries))
	}
	if entries[0].GameTitle != "Playing Game" {
		t.Errorf("playing filter: title=%q", entries[0].GameTitle)
	}

	// Filter: finished
	entries, total, _ = store.GetPlayHistory("default", "finished", 50, 0)
	if total != 1 {
		t.Errorf("finished filter: total=%d, want 1", total)
	}
	if entries[0].GameTitle != "Finished Game" {
		t.Errorf("finished filter: title=%q", entries[0].GameTitle)
	}
}

func TestPlayHistory_Pagination(t *testing.T) {
	store := newTestStore(t)

	for i := 0; i < 5; i++ {
		store.AddPlayHistory(&PlayHistoryEntry{
			GameTitle: "Game " + string(rune('A'+i)),
		})
	}

	entries, total, _ := store.GetPlayHistory("default", "", 2, 0)
	if total != 5 {
		t.Errorf("total=%d, want 5", total)
	}
	if len(entries) != 2 {
		t.Errorf("entries=%d, want 2", len(entries))
	}

	entries, _, _ = store.GetPlayHistory("default", "", 2, 3)
	if len(entries) != 2 {
		t.Errorf("offset 3: entries=%d, want 2", len(entries))
	}
}

func TestPlayHistory_Stats(t *testing.T) {
	store := newTestStore(t)

	store.AddPlayHistory(&PlayHistoryEntry{
		GameTitle:   "Game A",
		Platform:    "Switch",
		Rating:      8,
		HoursPlayed: 20,
	})
	store.AddPlayHistory(&PlayHistoryEntry{
		GameTitle:   "Game B",
		Platform:    "PC",
		Rating:      6,
		HoursPlayed: 10,
	})
	store.AddPlayHistory(&PlayHistoryEntry{
		GameTitle:   "Game C",
		Platform:    "Switch",
		Rating:      0, // no rating
		HoursPlayed: 5,
	})

	stats := store.GetPlayHistoryStats("default")
	if stats.GamesTotal != 3 {
		t.Errorf("GamesTotal=%d, want 3", stats.GamesTotal)
	}
	if stats.TotalHours != 35 {
		t.Errorf("TotalHours=%f, want 35", stats.TotalHours)
	}
	// AvgRating should exclude zeros: (8+6)/2 = 7
	if stats.AvgRating != 7 {
		t.Errorf("AvgRating=%f, want 7", stats.AvgRating)
	}
	if stats.ByPlatform["Switch"] != 2 {
		t.Errorf("ByPlatform[Switch]=%d, want 2", stats.ByPlatform["Switch"])
	}
	if stats.ByPlatform["PC"] != 1 {
		t.Errorf("ByPlatform[PC]=%d, want 1", stats.ByPlatform["PC"])
	}
}

func TestPlayHistory_Stats_Empty(t *testing.T) {
	store := newTestStore(t)

	stats := store.GetPlayHistoryStats("default")
	if stats.GamesTotal != 0 {
		t.Errorf("GamesTotal=%d, want 0", stats.GamesTotal)
	}
	if stats.TotalHours != 0 {
		t.Errorf("TotalHours=%f, want 0", stats.TotalHours)
	}
	if stats.AvgRating != 0 {
		t.Errorf("AvgRating=%f, want 0", stats.AvgRating)
	}
	if stats.ByPlatform == nil {
		t.Error("ByPlatform should not be nil")
	}
}

func TestPlayHistory_UpdateNoFields(t *testing.T) {
	store := newTestStore(t)

	id, _ := store.AddPlayHistory(&PlayHistoryEntry{GameTitle: "Game"})
	err := store.UpdatePlayHistory(id, map[string]interface{}{})
	if err != nil {
		t.Errorf("update with no fields should not error: %v", err)
	}
}

func TestPlayHistory_LibraryItemID(t *testing.T) {
	store := newTestStore(t)

	var libID int64 = 42
	id, _ := store.AddPlayHistory(&PlayHistoryEntry{
		GameTitle:     "Game",
		LibraryItemID: &libID,
	})

	entries, _, _ := store.GetPlayHistory("default", "", 50, 0)
	if len(entries) != 1 {
		t.Fatalf("entries=%d", len(entries))
	}
	if entries[0].LibraryItemID == nil {
		t.Fatal("LibraryItemID should not be nil")
	}
	if *entries[0].LibraryItemID != 42 {
		t.Errorf("LibraryItemID=%d, want 42", *entries[0].LibraryItemID)
	}
	_ = id
}
