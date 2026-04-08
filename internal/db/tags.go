package db

import (
	"log/slog"
)

// Tag represents a user-defined tag.
type Tag struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

func (s *JobStore) migrateTags() {
	tables := []string{
		`CREATE TABLE IF NOT EXISTS tags (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			color TEXT NOT NULL DEFAULT '#6366f1'
		)`,
		`CREATE TABLE IF NOT EXISTS item_tags (
			item_id INTEGER NOT NULL,
			tag_id INTEGER NOT NULL,
			PRIMARY KEY (item_id, tag_id),
			FOREIGN KEY (item_id) REFERENCES library_items(id) ON DELETE CASCADE,
			FOREIGN KEY (tag_id) REFERENCES tags(id) ON DELETE CASCADE
		)`,
	}
	for _, ddl := range tables {
		if _, err := s.db.Exec(ddl); err != nil {
			slog.Warn("migrate tags", "error", err)
		}
	}

	// Seed predefined tags
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM tags").Scan(&count)
	if count == 0 {
		defaults := []struct {
			name  string
			color string
		}{
			{"favorites", "#ef4444"},
			{"backlog", "#f59e0b"},
			{"completed", "#10b981"},
			{"multiplayer", "#3b82f6"},
			{"couch-coop", "#8b5cf6"},
		}
		for _, d := range defaults {
			s.db.Exec("INSERT INTO tags (name, color) VALUES (?, ?)", d.name, d.color)
		}
	}
}

// GetTags returns all tags.
func (s *JobStore) GetTags() []Tag {
	rows, err := s.db.Query("SELECT id, name, color FROM tags ORDER BY id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var tags []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Color); err != nil {
			continue
		}
		tags = append(tags, t)
	}
	return tags
}

// AddTag creates a new tag.
func (s *JobStore) AddTag(name, color string) (int64, error) {
	if color == "" {
		color = "#6366f1"
	}
	result, err := s.db.Exec("INSERT INTO tags (name, color) VALUES (?, ?)", name, color)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// DeleteTag removes a tag and its associations.
func (s *JobStore) DeleteTag(id int64) error {
	s.db.Exec("DELETE FROM item_tags WHERE tag_id = ?", id)
	_, err := s.db.Exec("DELETE FROM tags WHERE id = ?", id)
	return err
}

// AddItemTag associates a tag with a library item.
func (s *JobStore) AddItemTag(itemID, tagID int64) error {
	_, err := s.db.Exec("INSERT OR IGNORE INTO item_tags (item_id, tag_id) VALUES (?, ?)", itemID, tagID)
	return err
}

// RemoveItemTag removes a tag association from a library item.
func (s *JobStore) RemoveItemTag(itemID, tagID int64) error {
	_, err := s.db.Exec("DELETE FROM item_tags WHERE item_id = ? AND tag_id = ?", itemID, tagID)
	return err
}

// GetItemTags returns all tags for a library item.
func (s *JobStore) GetItemTags(itemID int64) []Tag {
	rows, err := s.db.Query(
		"SELECT t.id, t.name, t.color FROM tags t INNER JOIN item_tags it ON t.id = it.tag_id WHERE it.item_id = ? ORDER BY t.name",
		itemID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var tags []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Color); err != nil {
			continue
		}
		tags = append(tags, t)
	}
	return tags
}

// GetLibraryItemIDsByTag returns library item IDs that have a specific tag.
func (s *JobStore) GetLibraryItemIDsByTag(tagName string) []int64 {
	rows, err := s.db.Query(
		"SELECT it.item_id FROM item_tags it INNER JOIN tags t ON it.tag_id = t.id WHERE t.name = ?",
		tagName,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}
