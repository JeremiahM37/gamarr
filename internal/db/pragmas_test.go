package db

import (
	"fmt"
	"sync"
	"testing"
)

// TestConnectionPragmas guards the DSN against silently-ignored pragma syntax:
// modernc.org/sqlite wants _pragma=name(value), and the mattn-style params this
// store once used were dropped without error, leaving the default rollback
// journal and busy_timeout=0. That made any concurrent reader/writer overlap
// fail writes instantly with SQLITE_BUSY (the intermittent "timed out waiting
// for library tracking" CI failures — a lost TrackInLibrary insert, not a slow
// one).
func TestConnectionPragmas(t *testing.T) {
	s, err := New(t.TempDir() + "/pragmas.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var journalMode string
	if err := s.DB().QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var busyTimeout int
	if err := s.DB().QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if busyTimeout < 1000 {
		t.Errorf("busy_timeout = %d, want >= 1000", busyTimeout)
	}
}

// TestConcurrentReadWrite hammers the store with parallel writers and raw-SQL
// readers (mirroring how worker goroutines write jobs while API handlers and
// tests read). With WAL + busy_timeout every write must succeed; without them
// SQLITE_BUSY drops writes often enough to fail most runs.
func TestConcurrentReadWrite(t *testing.T) {
	s, err := New(t.TempDir() + "/concurrent.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const writers, writes = 4, 50
	errs := make(chan error, writers*writes)
	stop := make(chan struct{})

	// Raw-SQL reader polling like the test helpers / API do.
	var readerWG sync.WaitGroup
	readerWG.Add(1)
	go func() {
		defer readerWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				var n int
				s.DB().QueryRow("SELECT COUNT(*) FROM library_items").Scan(&n)
			}
		}
	}()

	var writerWG sync.WaitGroup
	for w := 0; w < writers; w++ {
		writerWG.Add(1)
		go func(w int) {
			defer writerWG.Done()
			for i := 0; i < writes; i++ {
				_, err := s.AddLibraryItem(&LibraryItem{
					Title:    fmt.Sprintf("Game %d-%d", w, i),
					Platform: "SNES", PlatformSlug: "snes",
					FilePath: fmt.Sprintf("/roms/%d-%d.sfc", w, i),
					Source:   "test", SourceType: "test",
					SourceID: fmt.Sprintf("test:%d-%d", w, i),
					Metadata: "{}",
				})
				if err != nil {
					errs <- fmt.Errorf("writer %d insert %d: %w", w, i, err)
				}
			}
		}(w)
	}

	writerWG.Wait()
	close(stop)
	readerWG.Wait()

	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var n int
	if err := s.DB().QueryRow("SELECT COUNT(*) FROM library_items").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != writers*writes {
		t.Errorf("library rows = %d, want %d", n, writers*writes)
	}
}
