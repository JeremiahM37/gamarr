package db

import (
	"testing"
)

func TestWebhookCRUD(t *testing.T) {
	store := newTestStore(t)

	// Add
	id, err := store.AddWebhook("Discord", "https://discord.com/webhook/123", "discord", "download_complete,download_failed")
	if err != nil {
		t.Fatalf("AddWebhook: %v", err)
	}
	if id <= 0 {
		t.Error("expected positive ID")
	}

	// Get by ID
	wh, err := store.GetWebhook(id)
	if err != nil {
		t.Fatalf("GetWebhook: %v", err)
	}
	if wh.Name != "Discord" {
		t.Errorf("name=%q, want 'Discord'", wh.Name)
	}
	if wh.URL != "https://discord.com/webhook/123" {
		t.Errorf("url=%q", wh.URL)
	}
	if wh.Type != "discord" {
		t.Errorf("type=%q, want 'discord'", wh.Type)
	}
	if !wh.Enabled {
		t.Error("expected enabled=true")
	}
	if wh.Events != "download_complete,download_failed" {
		t.Errorf("events=%q", wh.Events)
	}

	// List all
	all := store.GetWebhooks()
	if len(all) != 1 {
		t.Fatalf("GetWebhooks: len=%d, want 1", len(all))
	}

	// Delete
	err = store.DeleteWebhook(id)
	if err != nil {
		t.Fatalf("DeleteWebhook: %v", err)
	}
	all = store.GetWebhooks()
	if len(all) != 0 {
		t.Errorf("after delete: len=%d, want 0", len(all))
	}
}

func TestWebhook_Defaults(t *testing.T) {
	store := newTestStore(t)

	// Empty type and events should default
	id, err := store.AddWebhook("Test", "http://example.com", "", "")
	if err != nil {
		t.Fatal(err)
	}

	wh, err := store.GetWebhook(id)
	if err != nil {
		t.Fatal(err)
	}
	if wh.Type != "generic" {
		t.Errorf("type=%q, want 'generic'", wh.Type)
	}
	if wh.Events != "*" {
		t.Errorf("events=%q, want '*'", wh.Events)
	}
}

func TestGetEnabledWebhooks(t *testing.T) {
	store := newTestStore(t)

	store.AddWebhook("Enabled", "http://a.com", "generic", "*")
	id2, _ := store.AddWebhook("ToDisable", "http://b.com", "generic", "*")

	// Disable one via direct SQL (no UpdateWebhook method)
	store.db.Exec("UPDATE webhook_configs SET enabled = 0 WHERE id = ?", id2)

	enabled := store.GetEnabledWebhooks()
	if len(enabled) != 1 {
		t.Fatalf("GetEnabledWebhooks: len=%d, want 1", len(enabled))
	}
	if enabled[0].Name != "Enabled" {
		t.Errorf("name=%q, want 'Enabled'", enabled[0].Name)
	}
}

func TestGetWebhook_NonExistent(t *testing.T) {
	store := newTestStore(t)

	_, err := store.GetWebhook(999)
	if err == nil {
		t.Error("expected error for nonexistent webhook")
	}
}

func TestWebhook_MultipleEntries(t *testing.T) {
	store := newTestStore(t)

	store.AddWebhook("A", "http://a.com", "generic", "*")
	store.AddWebhook("B", "http://b.com", "discord", "download_complete")
	store.AddWebhook("C", "http://c.com", "generic", "request_created")

	all := store.GetWebhooks()
	if len(all) != 3 {
		t.Errorf("GetWebhooks: len=%d, want 3", len(all))
	}
}
