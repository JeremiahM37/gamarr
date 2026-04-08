package db

import (
	"testing"
)

func TestBlocklist_AddAndCheck(t *testing.T) {
	store := newTestStore(t)

	id, err := store.AddBlocklistEntry(&BlocklistEntry{
		Title:       "Bad Game",
		Source:      "prowlarr",
		DownloadURL: "http://bad.example.com/download",
		InfoHash:    "abc123deadbeef",
		Reason:      "malware detected",
	})
	if err != nil {
		t.Fatalf("AddBlocklistEntry: %v", err)
	}
	if id <= 0 {
		t.Error("expected positive ID")
	}

	// Check by URL
	if !store.IsBlocklisted("http://bad.example.com/download", "") {
		t.Error("expected URL to be blocklisted")
	}

	// Check by info hash
	if !store.IsBlocklisted("", "abc123deadbeef") {
		t.Error("expected hash to be blocklisted")
	}

	// Check by both
	if !store.IsBlocklisted("http://bad.example.com/download", "abc123deadbeef") {
		t.Error("expected match by URL or hash")
	}

	// Non-matching
	if store.IsBlocklisted("http://good.example.com", "other-hash") {
		t.Error("should not be blocklisted")
	}
}

func TestBlocklist_IsBlocklisted_EmptyInputs(t *testing.T) {
	store := newTestStore(t)

	store.AddBlocklistEntry(&BlocklistEntry{
		DownloadURL: "http://block.me",
		InfoHash:    "hash123",
	})

	// Both empty should return false
	if store.IsBlocklisted("", "") {
		t.Error("empty inputs should return false")
	}
}

func TestBlocklist_GetAll(t *testing.T) {
	store := newTestStore(t)

	store.AddBlocklistEntry(&BlocklistEntry{Title: "A", DownloadURL: "http://a.com"})
	store.AddBlocklistEntry(&BlocklistEntry{Title: "B", InfoHash: "hash-b"})

	entries := store.GetBlocklist()
	if len(entries) != 2 {
		t.Errorf("GetBlocklist: len=%d, want 2", len(entries))
	}
}

func TestBlocklist_Delete(t *testing.T) {
	store := newTestStore(t)

	id, _ := store.AddBlocklistEntry(&BlocklistEntry{
		DownloadURL: "http://delete.me",
	})

	err := store.DeleteBlocklistEntry(id)
	if err != nil {
		t.Fatalf("DeleteBlocklistEntry: %v", err)
	}

	entries := store.GetBlocklist()
	if len(entries) != 0 {
		t.Errorf("after delete: len=%d, want 0", len(entries))
	}
}

func TestBlocklist_Clear(t *testing.T) {
	store := newTestStore(t)

	store.AddBlocklistEntry(&BlocklistEntry{DownloadURL: "http://a.com"})
	store.AddBlocklistEntry(&BlocklistEntry{DownloadURL: "http://b.com"})
	store.AddBlocklistEntry(&BlocklistEntry{DownloadURL: "http://c.com"})

	deleted, err := store.ClearBlocklist()
	if err != nil {
		t.Fatalf("ClearBlocklist: %v", err)
	}
	if deleted != 3 {
		t.Errorf("ClearBlocklist deleted=%d, want 3", deleted)
	}

	entries := store.GetBlocklist()
	if len(entries) != 0 {
		t.Errorf("after clear: len=%d, want 0", len(entries))
	}
}

func TestBlocklist_ClearEmpty(t *testing.T) {
	store := newTestStore(t)

	deleted, err := store.ClearBlocklist()
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Errorf("clear empty: deleted=%d, want 0", deleted)
	}
}

func TestBlocklist_CheckURLOnly(t *testing.T) {
	store := newTestStore(t)

	store.AddBlocklistEntry(&BlocklistEntry{
		DownloadURL: "http://only-url.com",
	})

	if !store.IsBlocklisted("http://only-url.com", "") {
		t.Error("should match by URL only")
	}
	if store.IsBlocklisted("", "some-hash") {
		t.Error("should not match hash when only URL was added")
	}
}

func TestBlocklist_CheckHashOnly(t *testing.T) {
	store := newTestStore(t)

	store.AddBlocklistEntry(&BlocklistEntry{
		InfoHash: "only-hash-123",
	})

	if !store.IsBlocklisted("", "only-hash-123") {
		t.Error("should match by hash only")
	}
	if store.IsBlocklisted("http://other.com", "") {
		t.Error("should not match URL when only hash was added")
	}
}
