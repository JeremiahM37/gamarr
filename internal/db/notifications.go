package db

import (
	"log/slog"
	"time"

	"gamarr/internal/models"
)

// migrateNotifications creates the notifications table.
func (s *JobStore) migrateNotifications() {
	ddl := `CREATE TABLE IF NOT EXISTS notifications (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id TEXT NOT NULL DEFAULT 'default',
		type TEXT NOT NULL,
		title TEXT NOT NULL,
		message TEXT NOT NULL DEFAULT '',
		read INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`
	if _, err := s.db.Exec(ddl); err != nil {
		slog.Warn("migrate notifications", "error", err)
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_notifications_user ON notifications(user_id)`); err != nil {
		slog.Warn("migrate notifications index", "error", err)
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_notifications_read ON notifications(user_id, read)`); err != nil {
		slog.Warn("migrate notifications read index", "error", err)
	}
}

// CreateNotification inserts a new notification and returns its ID.
func (s *JobStore) CreateNotification(n *models.Notification) (int64, error) {
	result, err := s.db.Exec(
		"INSERT INTO notifications (user_id, type, title, message, read, created_at) VALUES (?, ?, ?, ?, 0, ?)",
		n.UserID, n.Type, n.Title, n.Message, n.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetUserNotifications returns notifications for a user, newest first.
func (s *JobStore) GetUserNotifications(userID string, limit int) ([]models.Notification, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		"SELECT id, user_id, type, title, message, read, created_at FROM notifications WHERE user_id = ? ORDER BY created_at DESC LIMIT ?",
		userID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notifications []models.Notification
	for rows.Next() {
		var n models.Notification
		var readInt int
		var createdAt string
		if err := rows.Scan(&n.ID, &n.UserID, &n.Type, &n.Title, &n.Message, &readInt, &createdAt); err != nil {
			continue
		}
		n.Read = readInt != 0
		n.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		notifications = append(notifications, n)
	}
	return notifications, nil
}

// MarkNotificationRead marks a single notification as read.
func (s *JobStore) MarkNotificationRead(id int64) error {
	_, err := s.db.Exec("UPDATE notifications SET read = 1 WHERE id = ?", id)
	return err
}

// MarkAllNotificationsRead marks all notifications as read for a user.
func (s *JobStore) MarkAllNotificationsRead(userID string) error {
	_, err := s.db.Exec("UPDATE notifications SET read = 1 WHERE user_id = ? AND read = 0", userID)
	return err
}

// UnreadCount returns the number of unread notifications for a user.
func (s *JobStore) UnreadCount(userID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read = 0", userID).Scan(&count)
	return count, err
}

// DeleteOldNotifications removes notifications older than the given number of days.
func (s *JobStore) DeleteOldNotifications(days int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -days).Format(time.RFC3339)
	result, err := s.db.Exec("DELETE FROM notifications WHERE created_at < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
