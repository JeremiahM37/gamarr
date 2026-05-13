package db

import (
	"database/sql"
	"log/slog"
	"time"
)

// PlayHistoryEntry represents a play history record.
type PlayHistoryEntry struct {
	ID            int64   `json:"id"`
	UserID        string  `json:"user_id"`
	GameTitle     string  `json:"game_title"`
	Platform      string  `json:"platform"`
	PlatformSlug  string  `json:"platform_slug"`
	StartedAt     string  `json:"started_at"`
	FinishedAt    string  `json:"finished_at,omitempty"`
	Rating        int     `json:"rating,omitempty"`
	Notes         string  `json:"notes,omitempty"`
	HoursPlayed   float64 `json:"hours_played"`
	LibraryItemID *int64  `json:"library_item_id,omitempty"`
}

// PlayHistoryStats holds aggregate play history statistics.
type PlayHistoryStats struct {
	GamesThisMonth int            `json:"games_this_month"`
	GamesThisYear  int            `json:"games_this_year"`
	GamesTotal     int            `json:"games_total"`
	AvgRating      float64        `json:"avg_rating"`
	TotalHours     float64        `json:"total_hours"`
	ByPlatform     map[string]int `json:"by_platform"`
}

// migrateHistory creates the play_history table.
func (s *JobStore) migrateHistory() {
	ddl := `CREATE TABLE IF NOT EXISTS play_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id TEXT NOT NULL DEFAULT 'default',
		game_title TEXT NOT NULL,
		platform TEXT NOT NULL DEFAULT '',
		platform_slug TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL DEFAULT (datetime('now')),
		finished_at TEXT,
		rating INTEGER DEFAULT 0,
		notes TEXT NOT NULL DEFAULT '',
		hours_played REAL NOT NULL DEFAULT 0,
		library_item_id INTEGER
	)`
	if _, err := s.db.Exec(ddl); err != nil {
		slog.Warn("migrate play_history", "error", err)
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_history_user ON play_history(user_id)`); err != nil {
		slog.Warn("migrate play_history index", "error", err)
	}
}

// AddPlayHistory inserts a new play history entry.
func (s *JobStore) AddPlayHistory(entry *PlayHistoryEntry) (int64, error) {
	if entry.UserID == "" {
		entry.UserID = "default"
	}
	if entry.StartedAt == "" {
		entry.StartedAt = time.Now().Format(time.RFC3339)
	}
	result, err := s.db.Exec(
		`INSERT INTO play_history (user_id, game_title, platform, platform_slug, started_at, finished_at, rating, notes, hours_played, library_item_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.UserID, entry.GameTitle, entry.Platform, entry.PlatformSlug,
		entry.StartedAt, nullStr(entry.FinishedAt), entry.Rating, entry.Notes,
		entry.HoursPlayed, entry.LibraryItemID,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetPlayHistory returns play history entries with pagination and filtering.
func (s *JobStore) GetPlayHistory(userID, status string, limit, offset int) ([]PlayHistoryEntry, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if userID == "" {
		userID = "default"
	}

	where := "WHERE user_id = ?"
	args := []interface{}{userID}

	switch status {
	case "playing":
		where += " AND finished_at IS NULL"
	case "finished":
		where += " AND finished_at IS NOT NULL"
	}

	var total int
	s.db.QueryRow("SELECT COUNT(*) FROM play_history "+where, args...).Scan(&total)

	rows, err := s.db.Query(
		"SELECT id, user_id, game_title, platform, platform_slug, started_at, finished_at, rating, notes, hours_played, library_item_id FROM play_history "+
			where+" ORDER BY started_at DESC LIMIT ? OFFSET ?",
		append(args, limit, offset)...,
	)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()

	var entries []PlayHistoryEntry
	for rows.Next() {
		var e PlayHistoryEntry
		var finishedAt sql.NullString
		var libID sql.NullInt64
		if err := rows.Scan(&e.ID, &e.UserID, &e.GameTitle, &e.Platform, &e.PlatformSlug,
			&e.StartedAt, &finishedAt, &e.Rating, &e.Notes, &e.HoursPlayed, &libID); err != nil {
			continue
		}
		if finishedAt.Valid {
			e.FinishedAt = finishedAt.String
		}
		if libID.Valid {
			e.LibraryItemID = &libID.Int64
		}
		entries = append(entries, e)
	}
	return entries, total, nil
}

// UpdatePlayHistory updates a play history entry.
func (s *JobStore) UpdatePlayHistory(id int64, fields map[string]interface{}) error {
	setClauses := []string{}
	args := []interface{}{}

	if v, ok := fields["game_title"]; ok {
		setClauses = append(setClauses, "game_title = ?")
		args = append(args, v)
	}
	if v, ok := fields["platform"]; ok {
		setClauses = append(setClauses, "platform = ?")
		args = append(args, v)
	}
	if v, ok := fields["platform_slug"]; ok {
		setClauses = append(setClauses, "platform_slug = ?")
		args = append(args, v)
	}
	if v, ok := fields["finished_at"]; ok {
		setClauses = append(setClauses, "finished_at = ?")
		args = append(args, v)
	}
	if v, ok := fields["rating"]; ok {
		setClauses = append(setClauses, "rating = ?")
		args = append(args, v)
	}
	if v, ok := fields["notes"]; ok {
		setClauses = append(setClauses, "notes = ?")
		args = append(args, v)
	}
	if v, ok := fields["hours_played"]; ok {
		setClauses = append(setClauses, "hours_played = ?")
		args = append(args, v)
	}

	if len(setClauses) == 0 {
		return nil
	}

	query := "UPDATE play_history SET "
	for i, c := range setClauses {
		if i > 0 {
			query += ", "
		}
		query += c
	}
	query += " WHERE id = ?"
	args = append(args, id)

	_, err := s.db.Exec(query, args...)
	return err
}

// DeletePlayHistory removes a play history entry.
func (s *JobStore) DeletePlayHistory(id int64) error {
	_, err := s.db.Exec("DELETE FROM play_history WHERE id = ?", id)
	return err
}

// GetPlayHistoryStats returns aggregate play statistics.
func (s *JobStore) GetPlayHistoryStats(userID string) PlayHistoryStats {
	if userID == "" {
		userID = "default"
	}
	stats := PlayHistoryStats{
		ByPlatform: make(map[string]int),
	}

	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	yearStart := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)

	s.db.QueryRow("SELECT COUNT(*) FROM play_history WHERE user_id = ? AND started_at >= ?", userID, monthStart).Scan(&stats.GamesThisMonth)
	s.db.QueryRow("SELECT COUNT(*) FROM play_history WHERE user_id = ? AND started_at >= ?", userID, yearStart).Scan(&stats.GamesThisYear)
	s.db.QueryRow("SELECT COUNT(*) FROM play_history WHERE user_id = ?", userID).Scan(&stats.GamesTotal)
	s.db.QueryRow("SELECT COALESCE(AVG(NULLIF(rating, 0)), 0) FROM play_history WHERE user_id = ?", userID).Scan(&stats.AvgRating)
	s.db.QueryRow("SELECT COALESCE(SUM(hours_played), 0) FROM play_history WHERE user_id = ?", userID).Scan(&stats.TotalHours)

	rows, err := s.db.Query("SELECT COALESCE(NULLIF(platform, ''), 'Unknown'), COUNT(*) FROM play_history WHERE user_id = ? GROUP BY platform", userID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var p string
			var c int
			rows.Scan(&p, &c)
			stats.ByPlatform[p] = c
		}
	}

	return stats
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
