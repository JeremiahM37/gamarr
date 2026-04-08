package db

import (
	"log/slog"

	"gamarr/internal/webhook"
)

// migrateWebhooks creates the webhook_configs table.
func (s *JobStore) migrateWebhooks() {
	ddl := `CREATE TABLE IF NOT EXISTS webhook_configs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		url TEXT NOT NULL,
		type TEXT NOT NULL DEFAULT 'generic',
		enabled INTEGER NOT NULL DEFAULT 1,
		events TEXT NOT NULL DEFAULT '*',
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`
	if _, err := s.db.Exec(ddl); err != nil {
		slog.Warn("migrate webhook_configs", "error", err)
	}
}

// GetWebhooks returns all webhook configurations.
func (s *JobStore) GetWebhooks() []webhook.WebhookConfig {
	rows, err := s.db.Query("SELECT id, name, url, type, enabled, events, created_at FROM webhook_configs ORDER BY id")
	if err != nil {
		return nil
	}
	defer rows.Close()

	var configs []webhook.WebhookConfig
	for rows.Next() {
		var c webhook.WebhookConfig
		var enabled int
		if err := rows.Scan(&c.ID, &c.Name, &c.URL, &c.Type, &enabled, &c.Events, &c.CreatedAt); err != nil {
			continue
		}
		c.Enabled = enabled != 0
		configs = append(configs, c)
	}
	return configs
}

// GetEnabledWebhooks returns only enabled webhook configurations.
func (s *JobStore) GetEnabledWebhooks() []webhook.WebhookConfig {
	rows, err := s.db.Query("SELECT id, name, url, type, enabled, events, created_at FROM webhook_configs WHERE enabled = 1 ORDER BY id")
	if err != nil {
		return nil
	}
	defer rows.Close()

	var configs []webhook.WebhookConfig
	for rows.Next() {
		var c webhook.WebhookConfig
		var enabled int
		if err := rows.Scan(&c.ID, &c.Name, &c.URL, &c.Type, &enabled, &c.Events, &c.CreatedAt); err != nil {
			continue
		}
		c.Enabled = enabled != 0
		configs = append(configs, c)
	}
	return configs
}

// AddWebhook inserts a new webhook configuration.
func (s *JobStore) AddWebhook(name, url, whType, events string) (int64, error) {
	if whType == "" {
		whType = "generic"
	}
	if events == "" {
		events = "*"
	}
	result, err := s.db.Exec(
		"INSERT INTO webhook_configs (name, url, type, enabled, events) VALUES (?, ?, ?, 1, ?)",
		name, url, whType, events,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// DeleteWebhook removes a webhook by ID.
func (s *JobStore) DeleteWebhook(id int64) error {
	_, err := s.db.Exec("DELETE FROM webhook_configs WHERE id = ?", id)
	return err
}

// GetWebhook returns a single webhook by ID.
func (s *JobStore) GetWebhook(id int64) (*webhook.WebhookConfig, error) {
	row := s.db.QueryRow("SELECT id, name, url, type, enabled, events, created_at FROM webhook_configs WHERE id = ?", id)
	var c webhook.WebhookConfig
	var enabled int
	err := row.Scan(&c.ID, &c.Name, &c.URL, &c.Type, &enabled, &c.Events, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	c.Enabled = enabled != 0
	return &c, nil
}
