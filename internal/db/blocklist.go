package db

import (
	"log/slog"
)

// BlocklistEntry represents a blocked download URL/hash.
type BlocklistEntry struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Source      string `json:"source"`
	DownloadURL string `json:"download_url"`
	InfoHash    string `json:"info_hash"`
	Reason      string `json:"reason"`
	CreatedAt   string `json:"created_at"`
}

func (s *JobStore) migrateBlocklist() {
	ddl := `CREATE TABLE IF NOT EXISTS blocklist (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		title TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL DEFAULT '',
		download_url TEXT NOT NULL DEFAULT '',
		info_hash TEXT NOT NULL DEFAULT '',
		reason TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`
	if _, err := s.db.Exec(ddl); err != nil {
		slog.Warn("migrate blocklist", "error", err)
	}
	_, _ = s.db.Exec("CREATE INDEX IF NOT EXISTS idx_blocklist_hash ON blocklist(info_hash)")
	_, _ = s.db.Exec("CREATE INDEX IF NOT EXISTS idx_blocklist_url ON blocklist(download_url)")
}

// GetBlocklist returns all blocklist entries.
func (s *JobStore) GetBlocklist() []BlocklistEntry {
	rows, err := s.db.Query("SELECT id, title, source, download_url, info_hash, reason, created_at FROM blocklist ORDER BY created_at DESC")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var entries []BlocklistEntry
	for rows.Next() {
		var e BlocklistEntry
		if err := rows.Scan(&e.ID, &e.Title, &e.Source, &e.DownloadURL, &e.InfoHash, &e.Reason, &e.CreatedAt); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries
}

// AddBlocklistEntry adds a new blocklist entry.
func (s *JobStore) AddBlocklistEntry(entry *BlocklistEntry) (int64, error) {
	result, err := s.db.Exec(
		"INSERT INTO blocklist (title, source, download_url, info_hash, reason) VALUES (?, ?, ?, ?, ?)",
		entry.Title, entry.Source, entry.DownloadURL, entry.InfoHash, entry.Reason,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// DeleteBlocklistEntry removes a blocklist entry by ID.
func (s *JobStore) DeleteBlocklistEntry(id int64) error {
	_, err := s.db.Exec("DELETE FROM blocklist WHERE id = ?", id)
	return err
}

// ClearBlocklist removes all blocklist entries.
func (s *JobStore) ClearBlocklist() (int64, error) {
	result, err := s.db.Exec("DELETE FROM blocklist")
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// IsBlocklisted checks if a URL or info hash is in the blocklist.
func (s *JobStore) IsBlocklisted(downloadURL, infoHash string) bool {
	if downloadURL != "" {
		var count int
		s.db.QueryRow("SELECT COUNT(*) FROM blocklist WHERE download_url = ? AND download_url != ''", downloadURL).Scan(&count)
		if count > 0 {
			return true
		}
	}
	if infoHash != "" {
		var count int
		s.db.QueryRow("SELECT COUNT(*) FROM blocklist WHERE info_hash = ? AND info_hash != ''", infoHash).Scan(&count)
		if count > 0 {
			return true
		}
	}
	return false
}
