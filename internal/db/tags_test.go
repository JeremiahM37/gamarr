package db

import (
	"testing"
)

func TestTags_CRUD(t *testing.T) {
	store := newTestStore(t)

	// Default tags are seeded on creation
	tags := store.GetTags()
	defaultCount := len(tags)
	if defaultCount == 0 {
		t.Fatal("expected default tags to be seeded")
	}

	// Add a custom tag
	id, err := store.AddTag("speedrun", "#ff0000")
	if err != nil {
		t.Fatalf("AddTag: %v", err)
	}
	if id <= 0 {
		t.Error("expected positive ID")
	}

	tags = store.GetTags()
	if len(tags) != defaultCount+1 {
		t.Errorf("after add: len=%d, want %d", len(tags), defaultCount+1)
	}

	// Delete the custom tag
	err = store.DeleteTag(id)
	if err != nil {
		t.Fatalf("DeleteTag: %v", err)
	}
	tags = store.GetTags()
	if len(tags) != defaultCount {
		t.Errorf("after delete: len=%d, want %d", len(tags), defaultCount)
	}
}

func TestTags_DefaultColor(t *testing.T) {
	store := newTestStore(t)

	id, err := store.AddTag("nocolor", "")
	if err != nil {
		t.Fatal(err)
	}

	// Find the tag
	tags := store.GetTags()
	for _, tag := range tags {
		if tag.ID == id {
			if tag.Color != "#6366f1" {
				t.Errorf("default color=%q, want '#6366f1'", tag.Color)
			}
			return
		}
	}
	t.Error("tag not found")
}

func TestTags_DuplicateName(t *testing.T) {
	store := newTestStore(t)

	store.AddTag("unique", "#ff0000")
	_, err := store.AddTag("unique", "#00ff00")
	if err == nil {
		t.Error("expected error for duplicate tag name")
	}
}

func TestItemTags_AddRemove(t *testing.T) {
	store := newTestStore(t)

	// Create a library item
	itemID, err := store.AddLibraryItem(&LibraryItem{
		Title:    "Test Game",
		Metadata: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create a tag
	tagID, err := store.AddTag("test-tag", "#123456")
	if err != nil {
		t.Fatal(err)
	}

	// Add tag to item
	err = store.AddItemTag(itemID, tagID)
	if err != nil {
		t.Fatalf("AddItemTag: %v", err)
	}

	// Get item tags
	tags := store.GetItemTags(itemID)
	if len(tags) != 1 {
		t.Fatalf("GetItemTags: len=%d, want 1", len(tags))
	}
	if tags[0].Name != "test-tag" {
		t.Errorf("tag name=%q, want 'test-tag'", tags[0].Name)
	}

	// Remove tag
	err = store.RemoveItemTag(itemID, tagID)
	if err != nil {
		t.Fatalf("RemoveItemTag: %v", err)
	}
	tags = store.GetItemTags(itemID)
	if len(tags) != 0 {
		t.Errorf("after remove: len=%d, want 0", len(tags))
	}
}

func TestItemTags_DuplicateIgnored(t *testing.T) {
	store := newTestStore(t)

	itemID, _ := store.AddLibraryItem(&LibraryItem{Title: "Game", Metadata: "{}"})
	tagID, _ := store.AddTag("dup-tag", "#000")

	store.AddItemTag(itemID, tagID)
	err := store.AddItemTag(itemID, tagID) // duplicate
	if err != nil {
		t.Errorf("duplicate AddItemTag should not error (INSERT OR IGNORE): %v", err)
	}

	tags := store.GetItemTags(itemID)
	if len(tags) != 1 {
		t.Errorf("duplicate should not create extra: len=%d, want 1", len(tags))
	}
}

func TestItemTags_MultipleTagsOnItem(t *testing.T) {
	store := newTestStore(t)

	itemID, _ := store.AddLibraryItem(&LibraryItem{Title: "Game", Metadata: "{}"})
	tag1, _ := store.AddTag("tag-alpha", "#111")
	tag2, _ := store.AddTag("tag-beta", "#222")

	store.AddItemTag(itemID, tag1)
	store.AddItemTag(itemID, tag2)

	tags := store.GetItemTags(itemID)
	if len(tags) != 2 {
		t.Errorf("GetItemTags: len=%d, want 2", len(tags))
	}
}

func TestGetLibraryItemIDsByTag(t *testing.T) {
	store := newTestStore(t)

	item1, _ := store.AddLibraryItem(&LibraryItem{Title: "Game A", Metadata: "{}"})
	item2, _ := store.AddLibraryItem(&LibraryItem{Title: "Game B", Metadata: "{}"})
	_ = item2

	tagID, _ := store.AddTag("my-tag", "#000")
	store.AddItemTag(item1, tagID)

	ids := store.GetLibraryItemIDsByTag("my-tag")
	if len(ids) != 1 {
		t.Fatalf("GetLibraryItemIDsByTag: len=%d, want 1", len(ids))
	}
	if ids[0] != item1 {
		t.Errorf("id=%d, want %d", ids[0], item1)
	}

	// Non-existent tag
	ids = store.GetLibraryItemIDsByTag("nonexistent")
	if len(ids) != 0 {
		t.Errorf("nonexistent tag: len=%d, want 0", len(ids))
	}
}

func TestDeleteTag_CascadesItemTags(t *testing.T) {
	store := newTestStore(t)

	itemID, _ := store.AddLibraryItem(&LibraryItem{Title: "Game", Metadata: "{}"})
	tagID, _ := store.AddTag("cascade-tag", "#000")
	store.AddItemTag(itemID, tagID)

	// Delete the tag
	store.DeleteTag(tagID)

	// Item tags should be cleaned up
	tags := store.GetItemTags(itemID)
	if len(tags) != 0 {
		t.Errorf("after tag deletion, item tags len=%d, want 0", len(tags))
	}
}

func TestGetItemTags_NoItem(t *testing.T) {
	store := newTestStore(t)

	tags := store.GetItemTags(99999)
	if len(tags) != 0 {
		t.Errorf("nonexistent item: len=%d, want 0", len(tags))
	}
}

func TestDefaultTags_Seeded(t *testing.T) {
	store := newTestStore(t)

	tags := store.GetTags()
	expected := map[string]bool{
		"favorites":   false,
		"backlog":     false,
		"completed":   false,
		"multiplayer": false,
		"couch-coop":  false,
	}
	for _, tag := range tags {
		if _, ok := expected[tag.Name]; ok {
			expected[tag.Name] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("expected default tag %q to be seeded", name)
		}
	}
}
