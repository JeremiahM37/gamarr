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
	TOTPSecret   string    `json:"-"`
	TOTPEnabled  bool      `json:"totp_enabled"`
	CreatedAt    time.Time `json:"created_at"`
	LastLogin    time.Time `json:"last_login,omitempty"`
}

// migrateUsers creates the users, auth_activity_log, backup_codes, and invite_codes tables.
func (s *JobStore) migrateUsers() {
	tables := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'user',
			totp_secret TEXT NOT NULL DEFAULT '',
			totp_enabled INTEGER NOT NULL DEFAULT 0,
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
		`CREATE TABLE IF NOT EXISTS backup_codes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			code_hash TEXT NOT NULL,
			used INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS invite_codes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			code TEXT NOT NULL UNIQUE,
			created_by INTEGER NOT NULL,
			role TEXT NOT NULL DEFAULT 'user',
			max_uses INTEGER NOT NULL DEFAULT 1,
			uses INTEGER NOT NULL DEFAULT 0,
			expires_at REAL,
			created_at REAL NOT NULL DEFAULT (strftime('%s','now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_users_username ON users(username)`,
		`CREATE INDEX IF NOT EXISTS idx_auth_activity_timestamp ON auth_activity_log(timestamp)`,
	}
	for _, ddl := range tables {
		if _, err := s.db.Exec(ddl); err != nil {
			slog.Warn("migrate users table", "error", err)
		}
	}
	// Add TOTP columns if missing (upgrade path)
	for _, col := range []struct{ name, def string }{
		{"totp_secret", "TEXT NOT NULL DEFAULT ''"},
		{"totp_enabled", "INTEGER NOT NULL DEFAULT 0"},
	} {
		s.db.Exec(fmt.Sprintf("ALTER TABLE users ADD COLUMN %s %s", col.name, col.def))
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
		"SELECT id, username, password_hash, role, totp_secret, totp_enabled, created_at, last_login FROM users WHERE username = ?",
		username,
	)
	return scanUser(row)
}

// GetUser retrieves a user by ID.
func (s *JobStore) GetUser(id int64) (*User, error) {
	row := s.db.QueryRow(
		"SELECT id, username, password_hash, role, totp_secret, totp_enabled, created_at, last_login FROM users WHERE id = ?",
		id,
	)
	return scanUser(row)
}

// ListUsers returns all users.
func (s *JobStore) ListUsers() ([]User, error) {
	rows, err := s.db.Query(
		"SELECT id, username, password_hash, role, totp_secret, totp_enabled, created_at, last_login FROM users ORDER BY created_at ASC",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var createdAt, lastLogin string
		var totpEnabled int
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.TOTPSecret, &totpEnabled, &createdAt, &lastLogin); err != nil {
			continue
		}
		u.TOTPEnabled = totpEnabled != 0
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
	var totpEnabled int
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.TOTPSecret, &totpEnabled, &createdAt, &lastLogin)
	if err != nil {
		return nil, err
	}
	u.TOTPEnabled = totpEnabled != 0
	u.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
	if lastLogin != "" {
		u.LastLogin, _ = time.Parse("2006-01-02 15:04:05", lastLogin)
	}
	return &u, nil
}

// ── TOTP Methods ─────────────────────────────────────────────────────────────

// SetTOTPSecret stores the TOTP secret for a user.
func (s *JobStore) SetTOTPSecret(userID int64, secret string) error {
	_, err := s.db.Exec("UPDATE users SET totp_secret = ? WHERE id = ?", secret, userID)
	return err
}

// EnableTOTP marks TOTP as enabled for a user.
func (s *JobStore) EnableTOTP(userID int64) error {
	_, err := s.db.Exec("UPDATE users SET totp_enabled = 1 WHERE id = ?", userID)
	return err
}

// DisableTOTP disables TOTP and clears the secret and backup codes for a user.
func (s *JobStore) DisableTOTP(userID int64) error {
	_, err := s.db.Exec("UPDATE users SET totp_enabled = 0, totp_secret = '' WHERE id = ?", userID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("DELETE FROM backup_codes WHERE user_id = ?", userID)
	return err
}

// SaveBackupCodes replaces all backup codes for a user with new hashed codes.
func (s *JobStore) SaveBackupCodes(userID int64, codeHashes []string) error {
	_, _ = s.db.Exec("DELETE FROM backup_codes WHERE user_id = ?", userID)
	for _, h := range codeHashes {
		if _, err := s.db.Exec("INSERT INTO backup_codes (user_id, code_hash) VALUES (?, ?)", userID, h); err != nil {
			return err
		}
	}
	return nil
}

// UseBackupCode marks a backup code as used. Returns true if the code was valid and unused.
func (s *JobStore) UseBackupCode(userID int64, codeHash string) bool {
	result, err := s.db.Exec(
		"UPDATE backup_codes SET used = 1 WHERE user_id = ? AND code_hash = ? AND used = 0",
		userID, codeHash,
	)
	if err != nil {
		return false
	}
	affected, _ := result.RowsAffected()
	return affected > 0
}

// ── Invite Code Methods ──────────────────────────────────────────────────────

// CreateInviteCode creates a new invite code.
func (s *JobStore) CreateInviteCode(code string, createdBy int64, role string, maxUses int, expiresAt *float64) (int64, error) {
	var result sql.Result
	var err error
	if expiresAt != nil {
		result, err = s.db.Exec(
			"INSERT INTO invite_codes (code, created_by, role, max_uses, expires_at) VALUES (?, ?, ?, ?, ?)",
			code, createdBy, role, maxUses, *expiresAt,
		)
	} else {
		result, err = s.db.Exec(
			"INSERT INTO invite_codes (code, created_by, role, max_uses) VALUES (?, ?, ?, ?)",
			code, createdBy, role, maxUses,
		)
	}
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// ValidateInviteCode checks if an invite code is valid and returns its role.
func (s *JobStore) ValidateInviteCode(code string) (string, error) {
	var role string
	var maxUses, uses int
	var expiresAt sql.NullFloat64
	err := s.db.QueryRow(
		"SELECT role, max_uses, uses, expires_at FROM invite_codes WHERE code = ?", code,
	).Scan(&role, &maxUses, &uses, &expiresAt)
	if err != nil {
		return "", fmt.Errorf("invalid invite code")
	}
	if uses >= maxUses {
		return "", fmt.Errorf("invite code has been used up")
	}
	if expiresAt.Valid && expiresAt.Float64 < float64(time.Now().Unix()) {
		return "", fmt.Errorf("invite code has expired")
	}
	return role, nil
}

// UseInviteCode increments the usage count of an invite code.
func (s *JobStore) UseInviteCode(code string) error {
	_, err := s.db.Exec("UPDATE invite_codes SET uses = uses + 1 WHERE code = ?", code)
	return err
}

// ListInviteCodes returns all invite codes.
func (s *JobStore) ListInviteCodes() ([]map[string]interface{}, error) {
	rows, err := s.db.Query(
		"SELECT id, code, created_by, role, max_uses, uses, expires_at, created_at FROM invite_codes ORDER BY created_at DESC",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var codes []map[string]interface{}
	for rows.Next() {
		var id, createdBy int64
		var code, role string
		var maxUses, uses int
		var expiresAt sql.NullFloat64
		var createdAt float64
		if err := rows.Scan(&id, &code, &createdBy, &role, &maxUses, &uses, &expiresAt, &createdAt); err != nil {
			continue
		}
		entry := map[string]interface{}{
			"id":         id,
			"code":       code,
			"created_by": createdBy,
			"role":       role,
			"max_uses":   maxUses,
			"uses":       uses,
			"created_at": createdAt,
		}
		if expiresAt.Valid {
			entry["expires_at"] = expiresAt.Float64
		}
		codes = append(codes, entry)
	}
	return codes, nil
}

// DeleteInviteCode deletes an invite code by ID.
func (s *JobStore) DeleteInviteCode(id int64) error {
	_, err := s.db.Exec("DELETE FROM invite_codes WHERE id = ?", id)
	return err
}
