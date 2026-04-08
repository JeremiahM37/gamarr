package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"gamarr/internal/models"
)

// migrateRequests creates the game_requests table.
func (s *JobStore) migrateRequests() {
	ddl := `CREATE TABLE IF NOT EXISTS game_requests (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL DEFAULT 'default',
		title TEXT NOT NULL,
		platform TEXT NOT NULL DEFAULT '',
		platform_slug TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'pending',
		notes TEXT NOT NULL DEFAULT '',
		admin_notes TEXT NOT NULL DEFAULT '',
		cover_url TEXT NOT NULL DEFAULT '',
		year TEXT NOT NULL DEFAULT '',
		genre TEXT NOT NULL DEFAULT '',
		retry_count INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`
	if _, err := s.db.Exec(ddl); err != nil {
		slog.Warn("migrate game_requests", "error", err)
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_requests_status ON game_requests(status)`); err != nil {
		slog.Warn("migrate game_requests index", "error", err)
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_requests_user ON game_requests(user_id)`); err != nil {
		slog.Warn("migrate game_requests user index", "error", err)
	}
}

// GenerateRequestID returns a random hex ID.
func GenerateRequestID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// CreateRequest inserts a new game request.
func (s *JobStore) CreateRequest(req *models.GameRequest) error {
	_, err := s.db.Exec(
		`INSERT INTO game_requests (id, user_id, title, platform, platform_slug, status, notes, admin_notes, cover_url, year, genre, retry_count, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID, req.UserID, req.Title, req.Platform, req.PlatformSlug,
		req.Status, req.Notes, req.AdminNotes, req.CoverURL, req.Year, req.Genre,
		req.RetryCount, req.CreatedAt.Format(time.RFC3339), req.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

// GetRequest retrieves a game request by ID.
func (s *JobStore) GetRequest(id string) (*models.GameRequest, error) {
	row := s.db.QueryRow(
		`SELECT id, user_id, title, platform, platform_slug, status, notes, admin_notes, cover_url, year, genre, retry_count, created_at, updated_at
		 FROM game_requests WHERE id = ?`, id,
	)
	return scanRequest(row)
}

// ListRequests returns requests with optional filters.
// If userID is empty, returns all requests. If status is empty, returns all statuses.
func (s *JobStore) ListRequests(userID, status string, limit, offset int) ([]models.GameRequest, error) {
	query := "SELECT id, user_id, title, platform, platform_slug, status, notes, admin_notes, cover_url, year, genre, retry_count, created_at, updated_at FROM game_requests"
	var conditions []string
	var args []interface{}

	if userID != "" {
		conditions = append(conditions, "user_id = ?")
		args = append(args, userID)
	}
	if status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, status)
	}

	if len(conditions) > 0 {
		query += " WHERE "
		for i, c := range conditions {
			if i > 0 {
				query += " AND "
			}
			query += c
		}
	}
	query += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []models.GameRequest
	for rows.Next() {
		var req models.GameRequest
		var createdAt, updatedAt string
		if err := rows.Scan(&req.ID, &req.UserID, &req.Title, &req.Platform, &req.PlatformSlug,
			&req.Status, &req.Notes, &req.AdminNotes, &req.CoverURL, &req.Year, &req.Genre,
			&req.RetryCount, &createdAt, &updatedAt); err != nil {
			continue
		}
		req.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		req.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		requests = append(requests, req)
	}
	return requests, nil
}

// UpdateRequestStatus updates just the status and updated_at of a request.
func (s *JobStore) UpdateRequestStatus(id, status string) error {
	result, err := s.db.Exec(
		"UPDATE game_requests SET status = ?, updated_at = ? WHERE id = ?",
		status, time.Now().Format(time.RFC3339), id,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("request not found")
	}
	return nil
}

// UpdateRequest updates all mutable fields of a request.
func (s *JobStore) UpdateRequest(req *models.GameRequest) error {
	result, err := s.db.Exec(
		`UPDATE game_requests SET status = ?, notes = ?, admin_notes = ?, retry_count = ?, updated_at = ?
		 WHERE id = ?`,
		req.Status, req.Notes, req.AdminNotes, req.RetryCount, req.UpdatedAt.Format(time.RFC3339), req.ID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("request not found")
	}
	return nil
}

// DeleteRequest removes a request by ID.
func (s *JobStore) DeleteRequest(id string) error {
	result, err := s.db.Exec("DELETE FROM game_requests WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("request not found")
	}
	return nil
}

// GetUserRequests returns all requests for a given user.
func (s *JobStore) GetUserRequests(userID string) ([]models.GameRequest, error) {
	return s.ListRequests(userID, "", 200, 0)
}

// CountPendingRequests returns the number of pending requests.
func (s *JobStore) CountPendingRequests() int {
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM game_requests WHERE status = 'pending'").Scan(&count)
	return count
}

// CountRequests returns the total count of requests with optional filters.
func (s *JobStore) CountRequests(userID, status string) (int, error) {
	query := "SELECT COUNT(*) FROM game_requests"
	var conditions []string
	var args []interface{}

	if userID != "" {
		conditions = append(conditions, "user_id = ?")
		args = append(args, userID)
	}
	if status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, status)
	}

	if len(conditions) > 0 {
		query += " WHERE "
		for i, c := range conditions {
			if i > 0 {
				query += " AND "
			}
			query += c
		}
	}

	var count int
	err := s.db.QueryRow(query, args...).Scan(&count)
	return count, err
}

func scanRequest(row *sql.Row) (*models.GameRequest, error) {
	var req models.GameRequest
	var createdAt, updatedAt string
	err := row.Scan(&req.ID, &req.UserID, &req.Title, &req.Platform, &req.PlatformSlug,
		&req.Status, &req.Notes, &req.AdminNotes, &req.CoverURL, &req.Year, &req.Genre,
		&req.RetryCount, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	req.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	req.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &req, nil
}
