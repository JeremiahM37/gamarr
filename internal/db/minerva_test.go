package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestMinervaOwnershipSurvivesJobRemovalAndReopen(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(fmt.Sprint(cleanup), func(t *testing.T) {
			s := newTestStore(t)
			s.Set("selected", map[string]interface{}{"source": "minerva", "info_hash": " \tABCDEF1234\n", "status": "completed"})
			s.Set("normal", map[string]interface{}{"source": "torrent", "info_hash": "normal", "status": "completed"})
			if cleanup {
				if _, err := s.db.Exec("UPDATE jobs SET updated_at = 0"); err != nil {
					t.Fatal(err)
				}
				if n := s.Cleanup(7); n != 2 {
					t.Fatalf("cleanup count: %d", n)
				}
			} else {
				s.Delete("selected")
				s.Delete("normal")
			}
			if len(s.Items()) != 0 {
				t.Fatal("jobs not cleared")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err := New(s.path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var hash string
			if err := s.db.QueryRow("SELECT info_hash FROM minerva_torrent_ownership").Scan(&hash); err != nil || hash != "abcdef1234" {
				t.Fatalf("ownership lost after job removal: %q %v", hash, err)
			}
			var n int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM minerva_torrent_ownership").Scan(&n); err != nil || n != 1 {
				t.Fatalf("normal torrent acquired ownership: %d %v", n, err)
			}
		})
	}
}

func TestMinervaOwnershipMigrationBackfillsLegacyRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE jobs (job_id TEXT PRIMARY KEY, data TEXT NOT NULL, created_at REAL, updated_at REAL);
INSERT INTO jobs VALUES ('selected', '{"source":"minerva","info_hash":" ABCDEF1234 ","status":"completed"}', 0, 0);
INSERT INTO jobs VALUES ('same', '{"source":"minerva","info_hash":"abcdef1234","status":"interrupted"}', 0, 0);
INSERT INTO jobs VALUES ('normal', '{"source":"torrent","info_hash":"normal"}', 0, 0);
INSERT INTO jobs VALUES ('empty', '{"source":"minerva","info_hash":" "}', 0, 0);`)
	if err != nil {
		t.Fatal(err)
	}
	raw.Close()
	for range 2 {
		s, err := New(path)
		if err != nil {
			t.Fatal(err)
		}
		var hash string
		err = s.db.QueryRow("SELECT info_hash FROM minerva_torrent_ownership").Scan(&hash)
		if err != nil || hash != "abcdef1234" {
			s.Close()
			t.Fatalf("legacy ownership not backfilled: %q %v", hash, err)
		}
		var n int
		err = s.db.QueryRow("SELECT COUNT(*) FROM minerva_torrent_ownership").Scan(&n)
		s.Close()
		if err != nil || n != 1 {
			t.Fatalf("migration duplicated or claimed empty/normal hash: %d %v", n, err)
		}
	}
}

func TestMinervaOwnershipNormalizationConcurrencyAndErrors(t *testing.T) {
	s := newTestStore(t)
	for _, empty := range []string{"", " \t\n"} {
		if err := s.MarkMinervaTorrent(empty); err == nil {
			t.Fatal("empty hash claimed")
		}
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.MarkMinervaTorrent(" \tAbCdEf1234\n"); err != nil {
				t.Error(err)
				return
			}
			if owned, err := s.IsMinervaTorrent("ABCDEF1234"); err != nil || !owned {
				t.Errorf("concurrent lookup: %v %v", owned, err)
			}
		}()
	}
	wg.Wait()
	if owned, err := s.IsMinervaTorrent("ordinary"); err != nil || owned {
		t.Fatalf("normal torrent: %v %v", owned, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkMinervaTorrent("new"); err == nil {
		t.Fatal("persistence error swallowed")
	}
	if _, err := s.IsMinervaTorrent("abcdef1234"); err == nil {
		t.Fatal("lookup error swallowed")
	}
}

func TestMinervaOwnershipMigrationFailsClosed(t *testing.T) {
	s := newTestStore(t)
	// Simulate upgrading with a retained Minerva row and a failing marker write.
	if _, err := s.db.Exec(`INSERT INTO jobs (job_id, data) VALUES ('legacy', '{"source":"minerva","info_hash":"abc"}');
CREATE TRIGGER reject_ownership BEFORE INSERT ON minerva_torrent_ownership BEGIN SELECT RAISE(ABORT, 'disk write rejected'); END;`); err != nil {
		t.Fatal(err)
	}
	path := s.path
	s.Close()
	reopened, err := New(path)
	if err == nil {
		reopened.Close()
		t.Fatal("store opened with unprotected legacy collection")
	}
	// The existing row remains recoverable after the failed transaction.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec("DROP TRIGGER reject_ownership"); err != nil {
		t.Fatal(err)
	}
	reopened, err = New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if owned, err := reopened.IsMinervaTorrent("ABC"); err != nil || !owned {
		t.Fatalf("failed migration lost legacy job: %v %v", owned, err)
	}
}
