package models

import "time"

// Notification type constants.
const (
	NotifTypeRequestCompleted = "request_completed"
	NotifTypeRequestFailed    = "request_failed"
	NotifTypeRequestApproved  = "request_approved"
	NotifTypeDownloadComplete = "download_complete"
)

// Notification represents an in-app notification for a user.
type Notification struct {
	ID        int64     `json:"id"`
	UserID    string    `json:"user_id"`
	Type      string    `json:"type"`
	Title     string    `json:"title"`
	Message   string    `json:"message,omitempty"`
	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"created_at"`
}
