package db

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// JobStore is a SQLite-backed dict-like store for download jobs.
// It keeps an in-memory cache and writes through to SQLite on every mutation.
type JobStore struct {
	mu    sync.RWMutex
	cache map[string]map[string]interface{}
	db    *sql.DB
	path  string
}

// New creates a new JobStore at the given path.
func New(dbPath string) (*JobStore, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=10000")
	if err != nil {
		return nil, err
	}
	s := &JobStore{
		cache: make(map[string]map[string]interface{}),
		db:    db,
		path:  dbPath,
	}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	s.migrateExtra()
	s.loadAll()
	return s, nil
}

func (s *JobStore) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS jobs (
			job_id TEXT PRIMARY KEY,
			data TEXT NOT NULL,
			created_at REAL DEFAULT (strftime('%s','now')),
			updated_at REAL DEFAULT (strftime('%s','now'))
		)
	`)
	return err
}

func (s *JobStore) loadAll() {
	rows, err := s.db.Query("SELECT job_id, data FROM jobs")
	if err != nil {
		slog.Error("failed to load jobs", "error", err)
		return
	}
	defer rows.Close()

	var stale int
	for rows.Next() {
		var jobID, dataStr string
		if err := rows.Scan(&jobID, &dataStr); err != nil {
			continue
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
			continue
		}
		status, _ := data["status"].(string)
		if status == "downloading" || status == "scanning" || status == "organizing" {
			data["status"] = "interrupted"
			data["error"] = "Interrupted by restart"
			stale++
		}
		s.cache[jobID] = data
	}
	if len(s.cache) > 0 {
		slog.Info("restored jobs", "count", len(s.cache), "interrupted", stale)
	}
}

// Set stores or updates a job.
func (s *JobStore) Set(jobID string, data map[string]interface{}) {
	s.mu.Lock()
	s.cache[jobID] = data
	s.mu.Unlock()
	s.persist(jobID, data)
}

// Get returns a job by ID.
func (s *JobStore) Get(jobID string) (map[string]interface{}, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.cache[jobID]
	return d, ok
}

// Update updates a single field on a job.
func (s *JobStore) Update(jobID, key string, value interface{}) {
	s.mu.Lock()
	job, ok := s.cache[jobID]
	if !ok {
		s.mu.Unlock()
		return
	}
	job[key] = value
	s.mu.Unlock()
	s.persist(jobID, job)
}

// UpdateMulti updates multiple fields on a job.
func (s *JobStore) UpdateMulti(jobID string, fields map[string]interface{}) {
	s.mu.Lock()
	job, ok := s.cache[jobID]
	if !ok {
		s.mu.Unlock()
		return
	}
	for k, v := range fields {
		job[k] = v
	}
	s.mu.Unlock()
	s.persist(jobID, job)
}

// Delete removes a job.
func (s *JobStore) Delete(jobID string) {
	s.mu.Lock()
	delete(s.cache, jobID)
	s.mu.Unlock()
	_, _ = s.db.Exec("DELETE FROM jobs WHERE job_id = ?", jobID)
}

// Contains checks if a job exists.
func (s *JobStore) Contains(jobID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.cache[jobID]
	return ok
}

// Items returns all jobs as (id, data) pairs.
func (s *JobStore) Items() []struct {
	ID   string
	Data map[string]interface{}
} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]struct {
		ID   string
		Data map[string]interface{}
	}, 0, len(s.cache))
	for id, data := range s.cache {
		items = append(items, struct {
			ID   string
			Data map[string]interface{}
		}{id, data})
	}
	return items
}

// Values returns all job data.
func (s *JobStore) Values() []map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vals := make([]map[string]interface{}, 0, len(s.cache))
	for _, data := range s.cache {
		vals = append(vals, data)
	}
	return vals
}

// Cleanup removes finished jobs older than the given number of days.
func (s *JobStore) Cleanup(days int) int {
	cutoff := float64(time.Now().Unix()) - float64(days*86400)
	result, err := s.db.Exec(
		`DELETE FROM jobs WHERE updated_at < ? AND json_extract(data, '$.status') IN ('completed','error','interrupted')`,
		cutoff,
	)
	if err != nil {
		return 0
	}
	deleted, _ := result.RowsAffected()

	// Sync cache
	if deleted > 0 {
		surviving := make(map[string]bool)
		rows, err := s.db.Query("SELECT job_id FROM jobs")
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id string
				rows.Scan(&id)
				surviving[id] = true
			}
		}
		s.mu.Lock()
		for id, data := range s.cache {
			status, _ := data["status"].(string)
			if (status == "completed" || status == "error" || status == "interrupted") && !surviving[id] {
				delete(s.cache, id)
			}
		}
		s.mu.Unlock()
		slog.Info("cleaned up old jobs", "count", deleted, "days", days)
	}
	return int(deleted)
}

// Close closes the database.
func (s *JobStore) Close() error {
	return s.db.Close()
}

func (s *JobStore) persist(jobID string, data map[string]interface{}) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		slog.Error("failed to marshal job", "error", err)
		return
	}
	_, err = s.db.Exec(
		"INSERT OR REPLACE INTO jobs (job_id, data, updated_at) VALUES (?, ?, strftime('%s','now'))",
		jobID, string(jsonData),
	)
	if err != nil {
		slog.Error("failed to persist job", "error", err)
	}
}
