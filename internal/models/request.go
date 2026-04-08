package models

import "time"

// Request status constants.
const (
	RequestStatusPending     = "pending"
	RequestStatusApproved    = "approved"
	RequestStatusSearching   = "searching"
	RequestStatusDownloading = "downloading"
	RequestStatusCompleted   = "completed"
	RequestStatusFailed      = "failed"
	RequestStatusCancelled   = "cancelled"
)

// GameRequest represents a user's game request.
type GameRequest struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id"`
	Title        string    `json:"title"`
	Platform     string    `json:"platform"`
	PlatformSlug string    `json:"platform_slug"`
	Status       string    `json:"status"`
	Notes        string    `json:"notes,omitempty"`
	AdminNotes   string    `json:"admin_notes,omitempty"`
	CoverURL     string    `json:"cover_url,omitempty"`
	Year         string    `json:"year,omitempty"`
	Genre        string    `json:"genre,omitempty"`
	RetryCount   int       `json:"retry_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
