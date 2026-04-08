package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// User represents a registered user.
type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
	LastLogin    time.Time `json:"last_login,omitempty"`
}

// migrateUsers creates the users and auth_activity_log tables.
func (s *JobStore) migrateUsers() {
	tables := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'user',
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			last_login TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS auth_activity_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL,
			action TEXT NOT NULL,
			target TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL DEFAULT '',
			timestamp TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_users_username ON users(username)`,
		`CREATE INDEX IF NOT EXISTS idx_auth_activity_timestamp ON auth_activity_log(timestamp)`,
	}
	for _, ddl := range tables {
		if _, err := s.db.Exec(ddl); err != nil {
			slog.Warn("migrate users table", "error", err)
		}
	}
}

// CreateUser inserts a new user.
func (s *JobStore) CreateUser(username, passwordHash, role string) (int64, error) {
	result, err := s.db.Exec(
		"INSERT INTO users (username, password_hash, role) VALUES (?, ?, ?)",
		username, passwordHash, role,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetUserByUsername retrieves a user by username.
func (s *JobStore) GetUserByUsername(username string) (*User, error) {
	row := s.db.QueryRow(
		"SELECT id, username, password_hash, role, created_at, last_login FROM users WHERE username = ?",
		username,
	)
	return scanUser(row)
}

// GetUser retrieves a user by ID.
func (s *JobStore) GetUser(id int64) (*User, error) {
	row := s.db.QueryRow(
		"SELECT id, username, password_hash, role, created_at, last_login FROM users WHERE id = ?",
		id,
	)
	return scanUser(row)
}

// ListUsers returns all users.
func (s *JobStore) ListUsers() ([]User, error) {
	rows, err := s.db.Query(
		"SELECT id, username, password_hash, role, created_at, last_login FROM users ORDER BY created_at ASC",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var createdAt, lastLogin string
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &createdAt, &lastLogin); err != nil {
			continue
		}
		u.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		if lastLogin != "" {
			u.LastLogin, _ = time.Parse("2006-01-02 15:04:05", lastLogin)
		}
		users = append(users, u)
	}
	return users, nil
}

// CountUsers returns the total number of users.
func (s *JobStore) CountUsers() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	return count, err
}

// UpdateUser updates a user's username and role.
func (s *JobStore) UpdateUser(id int64, username, role string) error {
	_, err := s.db.Exec("UPDATE users SET username = ?, role = ? WHERE id = ?", username, role, id)
	return err
}

// UpdateUserPassword updates a user's password hash.
func (s *JobStore) UpdateUserPassword(id int64, passwordHash string) error {
	_, err := s.db.Exec("UPDATE users SET password_hash = ? WHERE id = ?", passwordHash, id)
	return err
}

// DeleteUser removes a user by ID.
func (s *JobStore) DeleteUser(id int64) error {
	result, err := s.db.Exec("DELETE FROM users WHERE id = ?", id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

// UpdateLastLogin updates the last_login timestamp for a user.
func (s *JobStore) UpdateLastLogin(id int64) {
	_, _ = s.db.Exec(
		"UPDATE users SET last_login = datetime('now') WHERE id = ?", id,
	)
}

// LogAuthActivity logs an authentication-related activity.
func (s *JobStore) LogAuthActivity(username, action, target, detail string) {
	_, err := s.db.Exec(
		"INSERT INTO auth_activity_log (username, action, target, detail) VALUES (?, ?, ?, ?)",
		username, action, target, detail,
	)
	if err != nil {
		slog.Warn("failed to log auth activity", "error", err)
	}
}

func scanUser(row *sql.Row) (*User, error) {
	var u User
	var createdAt, lastLogin string
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &createdAt, &lastLogin)
	if err != nil {
		return nil, err
	}
	u.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
	if lastLogin != "" {
		u.LastLogin, _ = time.Parse("2006-01-02 15:04:05", lastLogin)
	}
	return &u, nil
}
