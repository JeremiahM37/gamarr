package db

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *JobStore {
	t.Helper()
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestJobStore_SetGet(t *testing.T) {
	store := newTestStore(t)

	data := map[string]interface{}{
		"status": "downloading",
		"title":  "Test Game",
	}
	store.Set("job1", data)

	got, ok := store.Get("job1")
	if !ok {
		t.Fatal("expected job to exist")
	}
	if got["status"] != "downloading" {
		t.Errorf("status=%v, want 'downloading'", got["status"])
	}
	if got["title"] != "Test Game" {
		t.Errorf("title=%v, want 'Test Game'", got["title"])
	}
}

func TestJobStore_GetMissing(t *testing.T) {
	store := newTestStore(t)
	_, ok := store.Get("nonexistent")
	if ok {
		t.Error("expected false for missing job")
	}
}

func TestJobStore_Update(t *testing.T) {
	store := newTestStore(t)
	store.Set("job1", map[string]interface{}{"status": "downloading"})

	store.Update("job1", "status", "completed")
	got, _ := store.Get("job1")
	if got["status"] != "completed" {
		t.Errorf("status=%v, want 'completed'", got["status"])
	}
}

func TestJobStore_UpdateNonExistent(t *testing.T) {
	store := newTestStore(t)
	// Should not panic
	store.Update("missing", "status", "completed")
}

func TestJobStore_UpdateMulti(t *testing.T) {
	store := newTestStore(t)
	store.Set("job1", map[string]interface{}{"status": "downloading", "title": "Game"})

	store.UpdateMulti("job1", map[string]interface{}{
		"status": "error",
		"error":  "something went wrong",
	})

	got, _ := store.Get("job1")
	if got["status"] != "error" {
		t.Errorf("status=%v", got["status"])
	}
	if got["error"] != "something went wrong" {
		t.Errorf("error=%v", got["error"])
	}
	// title should be unchanged
	if got["title"] != "Game" {
		t.Errorf("title=%v, want 'Game'", got["title"])
	}
}

func TestJobStore_UpdateMultiNonExistent(t *testing.T) {
	store := newTestStore(t)
	store.UpdateMulti("missing", map[string]interface{}{"status": "x"})
}

func TestJobStore_Delete(t *testing.T) {
	store := newTestStore(t)
	store.Set("job1", map[string]interface{}{"status": "done"})
	store.Delete("job1")

	_, ok := store.Get("job1")
	if ok {
		t.Error("expected job to be deleted")
	}
}

func TestJobStore_Contains(t *testing.T) {
	store := newTestStore(t)
	store.Set("job1", map[string]interface{}{"status": "done"})

	if !store.Contains("job1") {
		t.Error("expected Contains=true")
	}
	if store.Contains("missing") {
		t.Error("expected Contains=false for missing")
	}
}

func TestJobStore_Items(t *testing.T) {
	store := newTestStore(t)
	store.Set("a", map[string]interface{}{"status": "downloading"})
	store.Set("b", map[string]interface{}{"status": "completed"})

	items := store.Items()
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
}

func TestJobStore_Values(t *testing.T) {
	store := newTestStore(t)
	store.Set("a", map[string]interface{}{"status": "downloading"})
	store.Set("b", map[string]interface{}{"status": "completed"})

	vals := store.Values()
	if len(vals) != 2 {
		t.Fatalf("expected 2 values, got %d", len(vals))
	}
}

func TestJobStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create and populate
	store1, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store1.Set("job1", map[string]interface{}{
		"status": "completed",
		"title":  "Persisted Game",
	})
	store1.Close()

	// Reopen and verify
	store2, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()

	got, ok := store2.Get("job1")
	if !ok {
		t.Fatal("expected job to persist across restarts")
	}
	if got["title"] != "Persisted Game" {
		t.Errorf("title=%v", got["title"])
	}
}

func TestJobStore_InterruptedOnLoad(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create with downloading status
	store1, _ := New(dbPath)
	store1.Set("job1", map[string]interface{}{"status": "downloading"})
	store1.Set("job2", map[string]interface{}{"status": "scanning"})
	store1.Set("job3", map[string]interface{}{"status": "completed"})
	store1.Close()

	// Reopen - downloading/scanning should become interrupted
	store2, _ := New(dbPath)
	defer store2.Close()

	got1, _ := store2.Get("job1")
	if got1["status"] != "interrupted" {
		t.Errorf("downloading job should be interrupted, got %v", got1["status"])
	}
	got2, _ := store2.Get("job2")
	if got2["status"] != "interrupted" {
		t.Errorf("scanning job should be interrupted, got %v", got2["status"])
	}
	got3, _ := store2.Get("job3")
	if got3["status"] != "completed" {
		t.Errorf("completed job should stay completed, got %v", got3["status"])
	}
}

func TestJobStore_Cleanup(t *testing.T) {
	store := newTestStore(t)
	// Add some old completed jobs
	store.Set("old1", map[string]interface{}{"status": "completed"})
	store.Set("old2", map[string]interface{}{"status": "error"})
	store.Set("active", map[string]interface{}{"status": "downloading"})

	// Force update timestamps to be old
	store.db.Exec("UPDATE jobs SET updated_at = 0 WHERE job_id IN ('old1', 'old2')")

	deleted := store.Cleanup(1) // 1 day cutoff
	if deleted != 2 {
		t.Errorf("expected 2 deleted, got %d", deleted)
	}

	// Active job should remain
	if !store.Contains("active") {
		t.Error("active job should not be cleaned up")
	}
	if store.Contains("old1") {
		t.Error("old completed job should be cleaned up")
	}
}

func TestJobStore_DBDir(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "nested", "path")

	store, err := New(filepath.Join(subdir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store with nested path: %v", err)
	}
	defer store.Close()

	// Verify the directory was created
	if _, err := os.Stat(subdir); os.IsNotExist(err) {
		t.Error("expected nested directory to be created")
	}
}
