// Package db provides the SQLite-backed persistence layer: the JobStore for
// download jobs plus library items, users, webhooks, requests, and history.
package db

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// JobStore is a SQLite-backed dict-like store for download jobs.
// It keeps an in-memory cache and writes through to SQLite on every mutation.
type JobStore struct {
	mu    sync.RWMutex
	cache map[string]Job
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
		cache: make(map[string]Job),
		db:    db,
		path:  dbPath,
	}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	s.migrateExtra()
	s.migrateUsers()
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
		var data Job
		if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
			continue
		}
		switch data.Status() {
		case StatusDownloading, StatusScanning, StatusOrganizing:
			data[JobStatus] = StatusInterrupted
			data[JobError] = "Interrupted by restart"
			stale++
		}
		s.cache[jobID] = data
	}
	if len(s.cache) > 0 {
		slog.Info("restored jobs", "count", len(s.cache), "interrupted", stale)
	}
}

// copyJob returns a copy of a job map. Job values are scalars (strings,
// bools, numbers, nil), so a per-map shallow copy fully detaches the result.
func copyJob(data Job) Job {
	cp := make(Job, len(data))
	for k, v := range data {
		cp[k] = v
	}
	return cp
}

// Set stores or updates a job. The input map is copied, so later caller
// mutations do not affect the store.
func (s *JobStore) Set(jobID string, data Job) {
	s.mu.Lock()
	s.cache[jobID] = copyJob(data)
	snap := copyJob(data)
	s.mu.Unlock()
	s.persist(jobID, snap)
}

// Get returns a copy of a job by ID. Callers may read or mutate the result
// without racing writers that update the cached map under the store lock.
func (s *JobStore) Get(jobID string) (Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.cache[jobID]
	if !ok {
		return nil, false
	}
	return copyJob(d), true
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
	snap := copyJob(job)
	s.mu.Unlock()
	s.persist(jobID, snap)
}

// UpdateMulti updates multiple fields on a job.
func (s *JobStore) UpdateMulti(jobID string, fields Job) {
	s.mu.Lock()
	job, ok := s.cache[jobID]
	if !ok {
		s.mu.Unlock()
		return
	}
	for k, v := range fields {
		job[k] = v
	}
	snap := copyJob(job)
	s.mu.Unlock()
	s.persist(jobID, snap)
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

// Items returns all jobs as (id, data) pairs. Data maps are copies, safe to
// read or mutate without racing writers.
func (s *JobStore) Items() []JobItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]JobItem, 0, len(s.cache))
	for id, data := range s.cache {
		items = append(items, JobItem{ID: id, Data: copyJob(data)})
	}
	return items
}

// Values returns copies of all job data.
func (s *JobStore) Values() []Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vals := make([]Job, 0, len(s.cache))
	for _, data := range s.cache {
		vals = append(vals, copyJob(data))
	}
	return vals
}

// CleanupStaleDownloads removes "downloading" jobs that haven't been updated in the given hours.
// These are phantom entries where the actual download was lost (container restart, qBit cleared, etc).
func (s *JobStore) CleanupStaleDownloads(hours int) int {
	cutoff := float64(time.Now().Unix()) - float64(hours*3600)
	result, err := s.db.Exec(
		`DELETE FROM jobs WHERE updated_at < ? AND json_extract(data, '$.status') = 'downloading'`,
		cutoff,
	)
	if err != nil {
		return 0
	}
	deleted, _ := result.RowsAffected()
	if deleted > 0 {
		// Sync cache
		s.mu.Lock()
		for id, data := range s.cache {
			if data.Status() == StatusDownloading {
				if updated, ok := data[JobUpdatedAt].(float64); ok && updated < cutoff {
					delete(s.cache, id)
				}
			}
		}
		s.mu.Unlock()
		slog.Info("cleaned up stale downloading jobs", "count", deleted, "hours", hours)
	}
	return int(deleted)
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
			status := data.Status()
			if (status == StatusCompleted || status == StatusError || status == StatusInterrupted) && !surviving[id] {
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

func (s *JobStore) persist(jobID string, data Job) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		slog.Error("failed to marshal job", "error", err)
		return
	}
	// Retry on SQLITE_BUSY (lock contention)
	for attempt := 0; attempt < 5; attempt++ {
		_, err = s.db.Exec(
			"INSERT OR REPLACE INTO jobs (job_id, data, updated_at) VALUES (?, ?, strftime('%s','now'))",
			jobID, string(jsonData),
		)
		if err == nil {
			return
		}
		// Only retry on busy/locked errors
		errStr := err.Error()
		if !strings.Contains(errStr, "locked") && !strings.Contains(errStr, "busy") {
			break
		}
		time.Sleep(time.Duration(50*(attempt+1)) * time.Millisecond)
	}
	slog.Error("failed to persist job", "error", err)
}
