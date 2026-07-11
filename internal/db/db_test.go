package db

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

func TestJobStore_GetAndItemsReturnCopies(t *testing.T) {
	store := newTestStore(t)
	store.Set("job1", map[string]interface{}{"status": "downloading", "title": "Copy Game"})

	t.Run("Get returns a copy", func(t *testing.T) {
		got, ok := store.Get("job1")
		if !ok {
			t.Fatal("expected job to exist")
		}
		got["status"] = "hacked"
		again, _ := store.Get("job1")
		if again["status"] != "downloading" {
			t.Errorf("mutating Get result leaked into store: status=%v", again["status"])
		}
	})

	t.Run("Items returns copies", func(t *testing.T) {
		items := store.Items()
		if len(items) != 1 {
			t.Fatalf("expected 1 item, got %d", len(items))
		}
		items[0].Data["title"] = "hacked"
		again, _ := store.Get("job1")
		if again["title"] != "Copy Game" {
			t.Errorf("mutating Items result leaked into store: title=%v", again["title"])
		}
	})

	t.Run("Values returns copies", func(t *testing.T) {
		vals := store.Values()
		if len(vals) != 1 {
			t.Fatalf("expected 1 value, got %d", len(vals))
		}
		vals[0]["detail"] = "hacked"
		again, _ := store.Get("job1")
		if again["detail"] != nil {
			t.Errorf("mutating Values result leaked into store: detail=%v", again["detail"])
		}
	})

	t.Run("Set detaches from caller map", func(t *testing.T) {
		data := map[string]interface{}{"status": "queued"}
		store.Set("job2", data)
		data["status"] = "hacked"
		got, _ := store.Get("job2")
		if got["status"] != "queued" {
			t.Errorf("mutating Set input leaked into store: status=%v", got["status"])
		}
	})
}

// TestJobStore_ConcurrentReadersAndWriters exercises Get/Items readers racing
// Update/UpdateMulti writers on the same job. Run under -race: the old
// implementation handed out live references to the cached maps, so readers
// outside the store lock raced writer mutations.
func TestJobStore_ConcurrentReadersAndWriters(t *testing.T) {
	store := newTestStore(t)
	store.Set("job1", map[string]interface{}{
		"status": "downloading", "title": "Race Game", "error": nil,
	})

	const iters = 100
	var wg sync.WaitGroup

	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				store.Update("job1", "detail", fmt.Sprintf("progress %d/%d", n, i))
				store.UpdateMulti("job1", map[string]interface{}{
					"status": "downloading", "progress": i,
				})
			}
		}(w)
	}

	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				if job, ok := store.Get("job1"); ok {
					_, _ = job["detail"].(string)
					job["reader_scratch"] = i // returned copy must be safe to mutate
				}
				for _, item := range store.Items() {
					_, _ = item.Data["status"].(string)
				}
			}
		}()
	}

	wg.Wait()

	got, ok := store.Get("job1")
	if !ok {
		t.Fatal("job disappeared during concurrent access")
	}
	if got["title"] != "Race Game" {
		t.Errorf("title=%v, want 'Race Game'", got["title"])
	}
	if _, leaked := got["reader_scratch"]; leaked {
		t.Error("reader mutation of a returned map leaked into the store")
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

func TestJobStore_CleanupStaleDownloads_RemovesOld(t *testing.T) {
	store := newTestStore(t)

	// Add a downloading job with updated_at in both DB and cache
	store.Set("stale1", map[string]interface{}{
		"status":     "downloading",
		"title":      "Stale Game",
		"updated_at": float64(0), // epoch 0 is definitely > 24h ago
	})
	store.db.Exec("UPDATE jobs SET updated_at = ? WHERE job_id = 'stale1'",
		float64(0))

	deleted := store.CleanupStaleDownloads(24)
	if deleted != 1 {
		t.Errorf("expected 1 stale job deleted, got %d", deleted)
	}
	if store.Contains("stale1") {
		t.Error("stale downloading job should be removed from cache")
	}
}

func TestJobStore_CleanupStaleDownloads_KeepsRecent(t *testing.T) {
	store := newTestStore(t)

	// Add a recent downloading job (default updated_at is now)
	store.Set("recent1", map[string]interface{}{"status": "downloading", "title": "Recent Game"})

	deleted := store.CleanupStaleDownloads(24)
	if deleted != 0 {
		t.Errorf("expected 0 deleted (recent job), got %d", deleted)
	}
	if !store.Contains("recent1") {
		t.Error("recent downloading job should be kept")
	}
}

func TestJobStore_CleanupStaleDownloads_IgnoresNonDownloading(t *testing.T) {
	store := newTestStore(t)

	// Add completed and error jobs with old timestamps
	store.Set("completed1", map[string]interface{}{"status": "completed", "title": "Done"})
	store.Set("error1", map[string]interface{}{"status": "error", "title": "Failed"})
	store.db.Exec("UPDATE jobs SET updated_at = 0 WHERE job_id IN ('completed1', 'error1')")

	deleted := store.CleanupStaleDownloads(24)
	if deleted != 0 {
		t.Errorf("expected 0 deleted (non-downloading), got %d", deleted)
	}
	if !store.Contains("completed1") {
		t.Error("completed job should not be affected")
	}
	if !store.Contains("error1") {
		t.Error("error job should not be affected")
	}
}

func TestJobStore_CleanupStaleDownloads_Mixed(t *testing.T) {
	store := newTestStore(t)

	// Mix of stale downloading, recent downloading, and old non-downloading
	// Include updated_at in cache data so cache sync works
	store.Set("stale-dl", map[string]interface{}{"status": "downloading", "updated_at": float64(0)})
	store.Set("recent-dl", map[string]interface{}{"status": "downloading"})
	store.Set("old-completed", map[string]interface{}{"status": "completed"})

	store.db.Exec("UPDATE jobs SET updated_at = 0 WHERE job_id IN ('stale-dl', 'old-completed')")

	deleted := store.CleanupStaleDownloads(24)
	if deleted != 1 {
		t.Errorf("expected 1 deleted (only stale downloading), got %d", deleted)
	}
	if store.Contains("stale-dl") {
		t.Error("stale downloading should be removed")
	}
	if !store.Contains("recent-dl") {
		t.Error("recent downloading should be kept")
	}
	if !store.Contains("old-completed") {
		t.Error("old completed should be kept")
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
