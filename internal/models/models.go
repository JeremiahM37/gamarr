package models

// SearchResult represents a search result from any source.
type SearchResult struct {
	Title          string   `json:"title"`
	Size           int64    `json:"size"`
	SizeHuman      string   `json:"size_human"`
	Seeders        int      `json:"seeders"`
	Leechers       int      `json:"leechers"`
	Indexer        string   `json:"indexer"`
	DownloadURL    string   `json:"download_url"`
	MagnetURL      string   `json:"magnet_url"`
	InfoHash       string   `json:"info_hash"`
	GUID           string   `json:"guid"`
	Platform       string   `json:"platform"`
	PlatformSlug   string   `json:"platform_slug"`
	IsPC           bool     `json:"is_pc"`
	Age            int      `json:"age"`
	SourceType     string   `json:"source_type"` // "torrent" or "ddl"
	SafetyScore    int      `json:"safety_score"`
	SafetyWarnings []string `json:"safety_warnings"`
	VimmID         string   `json:"vimm_id,omitempty"`

	// Library duplicate detection
	InLibrary bool `json:"in_library"`

	// Scoring fields (populated by scorer).
	Score          int             `json:"score"`
	ScoreBreakdown *ScoreBreakdown `json:"score_breakdown,omitempty"`
}

// ScoreBreakdown provides a detailed breakdown of a search result's confidence score.
type ScoreBreakdown struct {
	TitleMatch    int    `json:"title_match"`    // 0-40
	PlatformMatch int    `json:"platform_match"` // 0-15
	SeederScore   int    `json:"seeder_score"`   // 0-15
	SizeScore     int    `json:"size_score"`     // 0-15
	SafetyScore   int    `json:"safety_score"`   // 0-15
	Total         int    `json:"total"`          // 0-100
	Confidence    string `json:"confidence"`     // "high", "medium", "low"
}

// Job represents a download job.
type Job struct {
	Status       string `json:"status"`
	Title        string `json:"title"`
	Platform     string `json:"platform"`
	PlatformSlug string `json:"platform_slug"`
	IsPC         bool   `json:"is_pc"`
	Error        string `json:"error,omitempty"`
	Detail       string `json:"detail,omitempty"`
	CompletedAt  string `json:"completed_at,omitempty"`
}

// DownloadEntry represents an entry in the downloads list (merged job + torrent info).
type DownloadEntry struct {
	Type     string  `json:"type"` // "job" or "torrent"
	Title    string  `json:"title"`
	Platform string  `json:"platform,omitempty"`
	Status   string  `json:"status"`
	JobID    string  `json:"job_id,omitempty"`
	Error    string  `json:"error,omitempty"`
	Detail   string  `json:"detail,omitempty"`
	Progress float64 `json:"progress,omitempty"`
	Size     string  `json:"size,omitempty"`
	Speed    string  `json:"speed,omitempty"`
	ETA      int     `json:"eta,omitempty"`
	Hash     string  `json:"hash,omitempty"`
}

// DownloadRequest is the POST body for /api/download.
type DownloadRequest struct {
	DownloadURL      string `json:"download_url"`
	MagnetURL        string `json:"magnet_url"`
	InfoHash         string `json:"info_hash"`
	Title            string `json:"title"`
	Platform         string `json:"platform"`
	PlatformSlug     string `json:"platform_slug"`
	IsPC             bool   `json:"is_pc"`
	SourceType       string `json:"source_type"`
	VimmID           string `json:"vimm_id"`
	DownloadProtocol string `json:"download_protocol"` // "torrent" or "nzb"
}

// DDLSource represents a custom direct-download source.
type DDLSource struct {
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Type      string   `json:"type"`
	Builtin   bool     `json:"builtin"`
	Platforms []string `json:"platforms,omitempty"`
}

// Settings represents user-configurable settings.
type Settings struct {
	ExtractArchives bool `json:"extract_archives"`
}

// MonitorAction represents a pending/completed monitor action.
type MonitorAction struct {
	ID          string      `json:"id"`
	Action      string      `json:"action"`
	Params      interface{} `json:"params"`
	Description string      `json:"description"`
	Risk        string      `json:"risk"`
	CreatedAt   float64     `json:"created_at,omitempty"`
	Result      string      `json:"result,omitempty"`
	DoneAt      float64     `json:"done_at,omitempty"`
}

// MonitorStatus is the response from /api/monitor/status.
type MonitorStatus struct {
	Enabled        bool             `json:"enabled"`
	Provider       string           `json:"provider"`
	Model          string           `json:"model"`
	AutoFix        bool             `json:"auto_fix"`
	Interval       int              `json:"interval"`
	LastChecked    *float64         `json:"last_checked"`
	Diagnosis      string           `json:"diagnosis"`
	RecentErrors   []ErrorEntry     `json:"recent_errors"`
	PendingActions []*MonitorAction `json:"pending_actions"`
	ActionHistory  []*MonitorAction `json:"action_history"`
}

// ErrorEntry is a captured warning/error log entry.
type ErrorEntry struct {
	Timestamp float64 `json:"ts"`
	TimeStr   string  `json:"ts_str"`
	Level     string  `json:"level"`
	Logger    string  `json:"logger"`
	Message   string  `json:"message"`
}
