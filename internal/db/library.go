package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// LibraryItem represents a game/ROM in the library.
type LibraryItem struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	Platform     string `json:"platform"`
	PlatformSlug string `json:"platform_slug"`
	IsPC         bool   `json:"is_pc"`
	FilePath     string `json:"file_path"`
	FileSize     int64  `json:"file_size"`
	Source       string `json:"source"`       // "torrent", "ddl", "scan"
	SourceType   string `json:"source_type"`  // "prowlarr", "myrient", "vimm", "manual"
	SourceID     string `json:"source_id"`    // dedup key (hash, url, etc.)
	Metadata     string `json:"metadata"`     // JSON blob
	AddedAt      string `json:"added_at"`
}

// WishlistItem represents a game on the wishlist.
type WishlistItem struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	Platform     string `json:"platform"`
	PlatformSlug string `json:"platform_slug"`
	AddedAt      string `json:"added_at"`
}

// ActivityEntry represents an activity log entry.
type ActivityEntry struct {
	ID            int64  `json:"id"`
	EventType     string `json:"event_type"`
	Title         string `json:"title"`
	Detail        string `json:"detail"`
	LibraryItemID *int64 `json:"library_item_id,omitempty"`
	JobID         string `json:"job_id,omitempty"`
	Timestamp     string `json:"timestamp"`
}

// LibraryPage is a paginated library result.
type LibraryPage struct {
	Items      []LibraryItem `json:"items"`
	Total      int           `json:"total"`
	Page       int           `json:"page"`
	PageSize   int           `json:"page_size"`
	TotalPages int           `json:"total_pages"`
}

func (s *JobStore) migrateExtra() {
	s.migrateRequests()
	s.migrateNotifications()

	tables := []string{
		`CREATE TABLE IF NOT EXISTS library_items (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			platform TEXT NOT NULL DEFAULT '',
			platform_slug TEXT NOT NULL DEFAULT '',
			is_pc INTEGER NOT NULL DEFAULT 0,
			file_path TEXT NOT NULL DEFAULT '',
			file_size INTEGER NOT NULL DEFAULT 0,
			source TEXT NOT NULL DEFAULT '',
			source_type TEXT NOT NULL DEFAULT '',
			source_id TEXT NOT NULL DEFAULT '',
			metadata TEXT NOT NULL DEFAULT '{}',
			added_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS wishlist (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			platform TEXT NOT NULL DEFAULT '',
			platform_slug TEXT NOT NULL DEFAULT '',
			added_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS activity_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_type TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL DEFAULT '',
			library_item_id INTEGER,
			job_id TEXT NOT NULL DEFAULT '',
			timestamp TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_library_platform ON library_items(platform_slug)`,
		`CREATE INDEX IF NOT EXISTS idx_library_source_id ON library_items(source_id)`,
		`CREATE INDEX IF NOT EXISTS idx_activity_timestamp ON activity_log(timestamp)`,
	}
	for _, ddl := range tables {
		if _, err := s.db.Exec(ddl); err != nil {
			slog.Warn("migrate extra table", "error", err)
		}
	}
}

// DB returns the underlying sql.DB for direct use.
func (s *JobStore) DB() *sql.DB {
	return s.db
}

// ── Library Items ──────────────────────────────────────────────────────────────

// AddLibraryItem inserts a new library item.
func (s *JobStore) AddLibraryItem(item *LibraryItem) (int64, error) {
	result, err := s.db.Exec(
		`INSERT INTO library_items (title, platform, platform_slug, is_pc, file_path, file_size, source, source_type, source_id, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.Title, item.Platform, item.PlatformSlug, boolToInt(item.IsPC),
		item.FilePath, item.FileSize, item.Source, item.SourceType, item.SourceID, item.Metadata,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetLibraryPage returns a paginated library.
func (s *JobStore) GetLibraryPage(page, pageSize int, query, platformSlug string) LibraryPage {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}

	where, args := buildLibraryWhere(query, platformSlug)

	var total int
	row := s.db.QueryRow("SELECT COUNT(*) FROM library_items "+where, args...)
	row.Scan(&total)

	totalPages := (total + pageSize - 1) / pageSize
	offset := (page - 1) * pageSize

	rows, err := s.db.Query(
		"SELECT id, title, platform, platform_slug, is_pc, file_path, file_size, source, source_type, source_id, metadata, added_at FROM library_items "+
			where+" ORDER BY added_at DESC LIMIT ? OFFSET ?",
		append(args, pageSize, offset)...,
	)
	if err != nil {
		return LibraryPage{Page: page, PageSize: pageSize}
	}
	defer rows.Close()

	var items []LibraryItem
	for rows.Next() {
		var item LibraryItem
		var isPC int
		rows.Scan(&item.ID, &item.Title, &item.Platform, &item.PlatformSlug,
			&isPC, &item.FilePath, &item.FileSize, &item.Source, &item.SourceType,
			&item.SourceID, &item.Metadata, &item.AddedAt)
		item.IsPC = isPC != 0
		items = append(items, item)
	}
	if items == nil {
		items = []LibraryItem{}
	}

	return LibraryPage{
		Items:      items,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	}
}

// GetLibraryItem returns a single library item by ID.
func (s *JobStore) GetLibraryItem(id int64) (*LibraryItem, error) {
	row := s.db.QueryRow(
		"SELECT id, title, platform, platform_slug, is_pc, file_path, file_size, source, source_type, source_id, metadata, added_at FROM library_items WHERE id = ?",
		id,
	)
	var item LibraryItem
	var isPC int
	err := row.Scan(&item.ID, &item.Title, &item.Platform, &item.PlatformSlug,
		&isPC, &item.FilePath, &item.FileSize, &item.Source, &item.SourceType,
		&item.SourceID, &item.Metadata, &item.AddedAt)
	if err != nil {
		return nil, err
	}
	item.IsPC = isPC != 0
	return &item, nil
}

// UpdateLibraryItemMetadata updates the metadata JSON blob for a library item.
func (s *JobStore) UpdateLibraryItemMetadata(id int64, metadata string) error {
	_, err := s.db.Exec("UPDATE library_items SET metadata = ? WHERE id = ?", metadata, id)
	return err
}

// DeleteLibraryItem deletes a library item by ID.
func (s *JobStore) DeleteLibraryItem(id int64) error {
	_, err := s.db.Exec("DELETE FROM library_items WHERE id = ?", id)
	return err
}

// LibraryHasSourceID checks if a source_id already exists in the library.
func (s *JobStore) LibraryHasSourceID(sourceID string) bool {
	if sourceID == "" {
		return false
	}
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM library_items WHERE source_id = ?", sourceID).Scan(&count)
	return count > 0
}

// LibraryStats returns counts by platform.
func (s *JobStore) LibraryStats() map[string]int {
	stats := make(map[string]int)
	rows, err := s.db.Query(`SELECT COALESCE(NULLIF(platform_slug,''), CASE WHEN is_pc THEN 'pc' ELSE 'unknown' END) as p, COUNT(*) FROM library_items GROUP BY p`)
	if err != nil {
		return stats
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var c int
		rows.Scan(&p, &c)
		stats[p] = c
	}
	return stats
}

// LibraryTotal returns the total number of library items.
func (s *JobStore) LibraryTotal() int {
	var total int
	s.db.QueryRow("SELECT COUNT(*) FROM library_items").Scan(&total)
	return total
}

// ScanLibraryDir scans a directory and adds new items to the library.
func (s *JobStore) ScanLibraryDir(dir, platform, platformSlug string, isPC bool) int {
	// Implemented in download/manager.go or called from main
	return 0
}

func buildLibraryWhere(query, platformSlug string) (string, []interface{}) {
	var conditions []string
	var args []interface{}
	if query != "" {
		conditions = append(conditions, "title LIKE ?")
		args = append(args, "%"+query+"%")
	}
	if platformSlug != "" && platformSlug != "all" {
		if platformSlug == "pc" {
			conditions = append(conditions, "is_pc = 1")
		} else {
			conditions = append(conditions, "platform_slug = ?")
			args = append(args, platformSlug)
		}
	}
	if len(conditions) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(conditions, " AND "), args
}

// ── Wishlist ───────────────────────────────────────────────────────────────────

// AddWishlistItem adds an item to the wishlist.
func (s *JobStore) AddWishlistItem(title, platform, platformSlug string) (int64, error) {
	result, err := s.db.Exec(
		"INSERT INTO wishlist (title, platform, platform_slug) VALUES (?, ?, ?)",
		title, platform, platformSlug,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetWishlist returns all wishlist items.
func (s *JobStore) GetWishlist() []WishlistItem {
	rows, err := s.db.Query("SELECT id, title, platform, platform_slug, added_at FROM wishlist ORDER BY added_at DESC")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var items []WishlistItem
	for rows.Next() {
		var item WishlistItem
		rows.Scan(&item.ID, &item.Title, &item.Platform, &item.PlatformSlug, &item.AddedAt)
		items = append(items, item)
	}
	return items
}

// DeleteWishlistItem removes a wishlist item.
func (s *JobStore) DeleteWishlistItem(id int64) error {
	_, err := s.db.Exec("DELETE FROM wishlist WHERE id = ?", id)
	return err
}

// ── Activity Log ───────────────────────────────────────────────────────────────

// LogActivity writes an activity log entry.
func (s *JobStore) LogActivity(eventType, title, detail, jobID string, libraryItemID *int64) {
	_, err := s.db.Exec(
		"INSERT INTO activity_log (event_type, title, detail, library_item_id, job_id) VALUES (?, ?, ?, ?, ?)",
		eventType, title, detail, libraryItemID, jobID,
	)
	if err != nil {
		slog.Warn("failed to log activity", "error", err)
	}
}

// GetActivity returns recent activity with pagination.
func (s *JobStore) GetActivity(page, pageSize int) ([]ActivityEntry, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	var total int
	s.db.QueryRow("SELECT COUNT(*) FROM activity_log").Scan(&total)

	offset := (page - 1) * pageSize
	rows, err := s.db.Query(
		"SELECT id, event_type, title, detail, library_item_id, job_id, timestamp FROM activity_log ORDER BY timestamp DESC LIMIT ? OFFSET ?",
		pageSize, offset,
	)
	if err != nil {
		return nil, total
	}
	defer rows.Close()

	var entries []ActivityEntry
	for rows.Next() {
		var e ActivityEntry
		var libID sql.NullInt64
		rows.Scan(&e.ID, &e.EventType, &e.Title, &e.Detail, &libID, &e.JobID, &e.Timestamp)
		if libID.Valid {
			e.LibraryItemID = &libID.Int64
		}
		entries = append(entries, e)
	}
	return entries, total
}

// ── Helpers ────────────────────────────────────────────────────────────────────

// ClearScanEntries removes all library items added by directory scanning.
// This is called before a rescan to ensure accuracy.
func (s *JobStore) ClearScanEntries() {
	result, _ := s.db.Exec("DELETE FROM library_items WHERE source = 'scan'")
	if n, _ := result.RowsAffected(); n > 0 {
		slog.Info("cleared scan entries for rescan", "count", n)
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// FormatSize formats bytes into a human-readable string.
func FormatSize(size int64) string {
	if size == 0 {
		return "?"
	}
	s := float64(size)
	for _, unit := range []string{"B", "KB", "MB", "GB"} {
		if s < 1024 {
			return fmt.Sprintf("%.1f %s", s, unit)
		}
		s /= 1024
	}
	return fmt.Sprintf("%.1f TB", s)
}

// RecentLibraryItems returns the most recently added items.
func (s *JobStore) RecentLibraryItems(limit int) []LibraryItem {
	rows, err := s.db.Query(
		"SELECT id, title, platform, platform_slug, is_pc, file_path, file_size, source, source_type, source_id, metadata, added_at FROM library_items ORDER BY added_at DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var items []LibraryItem
	for rows.Next() {
		var item LibraryItem
		var isPC int
		rows.Scan(&item.ID, &item.Title, &item.Platform, &item.PlatformSlug,
			&isPC, &item.FilePath, &item.FileSize, &item.Source, &item.SourceType,
			&item.SourceID, &item.Metadata, &item.AddedAt)
		item.IsPC = isPC != 0
		items = append(items, item)
	}
	return items
}

// init extra tables during New()
func init() {
	// We'll call migrateExtra from New after the initial migrate
	_ = time.Now // avoid unused import
}
