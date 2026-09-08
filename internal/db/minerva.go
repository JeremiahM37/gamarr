package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// Collection ownership outlives job history and qB removal/re-addition. A
// collection hash can never safely become a generic whole-torrent import.
func migrateMinervaOwnership(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS minerva_torrent_ownership (
		info_hash TEXT PRIMARY KEY NOT NULL CHECK (info_hash <> '')
	)`); err != nil {
		return err
	}
	rows, err := tx.Query("SELECT data FROM jobs")
	if err != nil {
		return err
	}
	var hashes []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var job map[string]interface{}
		if json.Unmarshal([]byte(raw), &job) == nil {
			if hash := minervaJobHash(job); hash != "" {
				hashes = append(hashes, hash)
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, hash := range hashes {
		if _, err := tx.Exec("INSERT INTO minerva_torrent_ownership (info_hash) VALUES (?) ON CONFLICT DO NOTHING", hash); err != nil {
			return err
		}
	}
	return nil
}

func minervaJobHash(job map[string]interface{}) string {
	if job["source"] != "minerva" {
		return ""
	}
	hash, _ := job["info_hash"].(string)
	return strings.ToLower(strings.TrimSpace(hash))
}

// MarkMinervaTorrent commits ownership synchronously. Callers must propagate
// errors and must not start selective work until it succeeds.
func (s *JobStore) MarkMinervaTorrent(hash string) error {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" {
		return errors.New("Minerva ownership requires a nonempty info hash")
	}
	_, err := s.db.Exec("INSERT INTO minerva_torrent_ownership (info_hash) VALUES (?) ON CONFLICT DO NOTHING", hash)
	return err
}

// IsMinervaTorrent reads SQLite directly so concurrent callers and reopened
// stores share durable ownership without a second cache to synchronize.
// An unavailable database is an error, never evidence of generic ownership.
func (s *JobStore) IsMinervaTorrent(hash string) (bool, error) {
	hash = strings.ToLower(strings.TrimSpace(hash))
	var owned bool
	err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM minerva_torrent_ownership WHERE info_hash = ?)", hash).Scan(&owned)
	return owned, err
}
