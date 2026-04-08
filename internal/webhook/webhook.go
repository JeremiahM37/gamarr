package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// WebhookType identifies the webhook format.
const (
	TypeDiscord = "discord"
	TypeGeneric = "generic"
)

// WebhookConfig represents a stored webhook configuration.
type WebhookConfig struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Type      string `json:"type"` // "discord" or "generic"
	Enabled   bool   `json:"enabled"`
	Events    string `json:"events"` // comma-separated event names, or "*" for all
	CreatedAt string `json:"created_at"`
}

// Event names that can trigger webhooks.
const (
	EventDownloadComplete = "download_complete"
	EventDownloadFailed   = "download_failed"
	EventRequestCreated   = "request_created"
	EventRequestApproved  = "request_approved"
	EventRequestCompleted = "request_completed"
	EventRequestFailed    = "request_failed"
	EventSchedulerMatch   = "scheduler_match"
)

// Payload is the data sent to webhooks.
type Payload struct {
	Event    string `json:"event"`
	Title    string `json:"title"`
	Platform string `json:"platform,omitempty"`
	Status   string `json:"status,omitempty"`
	Message  string `json:"message,omitempty"`
	Error    string `json:"error,omitempty"`
	Time     string `json:"time"`
}

// Send dispatches a webhook payload asynchronously. Does not block.
func Send(configs []WebhookConfig, payload Payload) {
	payload.Time = time.Now().UTC().Format(time.RFC3339)
	for _, cfg := range configs {
		if !cfg.Enabled {
			continue
		}
		if !matchesEvent(cfg.Events, payload.Event) {
			continue
		}
		go sendOne(cfg, payload)
	}
}

func matchesEvent(events, event string) bool {
	if events == "" || events == "*" {
		return true
	}
	for _, e := range splitCSV(events) {
		if e == event {
			return true
		}
	}
	return false
}

func splitCSV(s string) []string {
	var result []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			v := s[start:i]
			// trim spaces
			for len(v) > 0 && v[0] == ' ' {
				v = v[1:]
			}
			for len(v) > 0 && v[len(v)-1] == ' ' {
				v = v[:len(v)-1]
			}
			if v != "" {
				result = append(result, v)
			}
			start = i + 1
		}
	}
	return result
}

func sendOne(cfg WebhookConfig, payload Payload) {
	var body []byte
	var err error

	if cfg.Type == TypeDiscord {
		body, err = buildDiscordPayload(payload)
	} else {
		body, err = json.Marshal(payload)
	}
	if err != nil {
		slog.Warn("webhook marshal error", "name", cfg.Name, "error", err)
		return
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(cfg.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Warn("webhook send error", "name", cfg.Name, "error", err)
		return
	}
	resp.Body.Close()

	if resp.StatusCode >= 400 {
		slog.Warn("webhook bad status", "name", cfg.Name, "status", resp.StatusCode)
	} else {
		slog.Debug("webhook sent", "name", cfg.Name, "event", payload.Event)
	}
}

func buildDiscordPayload(p Payload) ([]byte, error) {
	color := 0x6366f1 // indigo default
	switch p.Status {
	case "completed":
		color = 0x10b981 // green
	case "error", "failed":
		color = 0xef4444 // red
	case "approved":
		color = 0xf59e0b // amber
	case "downloading":
		color = 0x3b82f6 // blue
	}

	fields := []map[string]interface{}{}
	if p.Platform != "" {
		fields = append(fields, map[string]interface{}{"name": "Platform", "value": p.Platform, "inline": true})
	}
	if p.Status != "" {
		fields = append(fields, map[string]interface{}{"name": "Status", "value": p.Status, "inline": true})
	}
	if p.Error != "" {
		fields = append(fields, map[string]interface{}{"name": "Error", "value": p.Error, "inline": false})
	}

	embed := map[string]interface{}{
		"title":       fmt.Sprintf("Gamarr: %s", p.Title),
		"description": p.Message,
		"color":       color,
		"fields":      fields,
		"footer":      map[string]interface{}{"text": "Gamarr"},
		"timestamp":   p.Time,
	}

	return json.Marshal(map[string]interface{}{
		"embeds": []interface{}{embed},
	})
}

// SendTest sends a test webhook to verify the configuration works.
func SendTest(cfg WebhookConfig) error {
	payload := Payload{
		Event:   "test",
		Title:   "Webhook Test",
		Status:  "completed",
		Message: "This is a test notification from Gamarr.",
		Time:    time.Now().UTC().Format(time.RFC3339),
	}

	var body []byte
	var err error
	if cfg.Type == TypeDiscord {
		body, err = buildDiscordPayload(payload)
	} else {
		body, err = json.Marshal(payload)
	}
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(cfg.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return nil
}
