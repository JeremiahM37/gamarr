package scheduler

import (
	"sync"
	"testing"
	"time"

	"gamarr/internal/config"
	"gamarr/internal/models"
)

func TestStopWaitsForActiveSearch(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.AddWishlistItem("HeartGold", "DS", "nds"); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	s := New(&config.Config{}, store, func(string, string) []*models.SearchResult {
		close(entered)
		<-release
		return nil
	}, noopDownload, nil)
	s.RunNow()
	<-entered
	stopped := make(chan struct{})
	go func() { s.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Error("Stop returned while search still uses its source")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-stopped
	if s.Status()["running"] != false {
		t.Fatal("Stop left scheduler running")
	}
	var callers sync.WaitGroup
	for range 5 {
		callers.Add(1)
		go func() { defer callers.Done(); s.Stop() }()
	}
	callers.Wait()
	s.RunNow()
	s.Stop()
	if s.Status()["running"] != false {
		t.Fatal("RunNow restarted stopped scheduler")
	}
}
