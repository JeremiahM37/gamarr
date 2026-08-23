// Package config loads Gamarr's runtime configuration from environment
// variables, including the source-driver registry consumed by search drivers.
package config

import (
	"log/slog"
	"os"
	"strconv"
	"strings"

	"gamarr/internal/fileops"
	"gamarr/internal/sources"
)

type Config struct {
	// Sources is the runtime source-driver registry. Drivers read base URLs
	// and per-platform mappings from here instead of from hardcoded constants.
	// Always non-nil after Load() returns.
	Sources *sources.Registry

	// Prowlarr
	ProwlarrURL    string
	ProwlarrAPIKey string

	// qBittorrent
	QBURL           string
	QBUser          string
	QBPass          string
	QBSavePath      string
	QBCategory      string
	QBContainerName string

	// Library destination paths
	GamesVaultPath string
	GamesRomsPath  string

	// Data directory
	DataDir string

	// ClamAV
	ClamAVContainer string
	ClamAVSocket    string
	DockerSocket    string

	// FileListScanEnabled gates the pre-download file name check.
	FileListScanEnabled bool

	// Import behavior. ImportMode decides whether a finished download is
	// moved into the library or preserved in place (hardlink/symlink/copy)
	// so a torrent can keep seeding. ImportHardlinkFallback applies when a
	// hardlink import turns out to cross a filesystem boundary.
	ImportMode             fileops.Mode
	ImportHardlinkFallback fileops.Fallback

	// VaultArchiveEnabled writes a PC game into the vault as one .tar rather
	// than as a folder. GameVault indexes one file per game and takes the
	// title from the file name, so a folder is read as a single game named
	// after whichever loose file inside it has a supported extension.
	VaultArchiveEnabled bool

	// Experimental
	ExtractArchives bool

	// AI Monitor
	AIMonitorEnabled  bool
	AIProvider        string
	AIAPIUrl          string
	AIAPIKey          string
	AIModel           string
	AIMonitorInterval int
	AIAutoFix         bool

	// SABnzbd (Usenet)
	SABnzbdURL      string
	SABnzbdAPIKey   string
	SABnzbdCategory string

	// NZBGet (Usenet)
	NZBGetURL      string
	NZBGetUser     string
	NZBGetPass     string
	NZBGetCategory string

	// Deluge
	DelugeURL  string
	DelugePass string

	// Transmission
	TransmissionURL  string
	TransmissionUser string
	TransmissionPass string

	// RAWG.io metadata
	RAWGAPIKey string

	// External services
	GameVaultURL string
	RomMURL      string

	// Webhooks (default from env, additional via DB)
	WebhookURL  string
	WebhookType string // "discord" or "generic"

	// Scheduler
	SchedulerEnabled       bool
	SchedulerIntervalHours int
	SchedulerAutoDownload  bool
	SchedulerMinScore      int

	// Retry
	MaxRetries          int
	RetryBackoffSeconds int

	// Auth
	AuthUsername string
	AuthPassword string
	APIKey       string

	// OIDC / SSO
	OIDCEnabled         bool
	OIDCProviderName    string
	OIDCIssuer          string
	OIDCClientID        string
	OIDCClientSecret    string
	OIDCRedirectURI     string
	OIDCAutoCreateUsers bool
	OIDCDefaultRole     string

	// Circuit breaker
	CircuitBreakerThreshold int
	CircuitBreakerTimeoutS  int

	// Torrent completion watcher
	WatcherEnabled    bool
	WatcherIntervalS  int
	RemoveAfterImport bool

	// Server
	Port           int
	MetricsEnabled bool

	// Torznab — when set, Gamarr exposes /torznab/api (and /api alias) as
	// a Torznab indexer so Prowlarr / Sonarr / other *arr apps can query
	// Gamarr for game results. Empty key disables the API-key check.
	TorznabAPIKey string

	// Prowlarr game indexer IDs
	ProwlarrGameIndexers []int

	// Rename on import
	RenameEnabled bool
	RenamePattern string

	// Auto-upgrade
	AutoUpgradeEnabled bool
}

func Load() *Config {
	registry := sources.Load(
		envStr("GAMARR_SOURCES_PATH", ""),
		envStr("GAMARR_SOURCES_URL", ""),
	).ApplyEnvOverrides(os.Getenv)

	return &Config{
		Sources: registry,

		ProwlarrURL:    envStr("PROWLARR_URL", "http://prowlarr:9696"),
		ProwlarrAPIKey: envStr("PROWLARR_API_KEY", ""),

		QBURL:           envStrAllowEmpty("QB_URL", "http://qbittorrent:8080"),
		QBUser:          envStr("QB_USER", "admin"),
		QBPass:          envStr("QB_PASS", ""),
		QBSavePath:      envStr("QB_SAVE_PATH", "/data/incoming/"),
		QBCategory:      envStr("QB_CATEGORY", "games"),
		QBContainerName: envStr("QB_CONTAINER_NAME", "qbittorrent"),

		GamesVaultPath: envStr("GAMES_VAULT_PATH", "/data/vault"),
		GamesRomsPath:  envStr("GAMES_ROMS_PATH", "/data/roms"),

		DataDir: envStr("DATA_DIR", "/data/gamarr"),

		ClamAVContainer: envStr("CLAMAV_CONTAINER", "clamav"),
		ClamAVSocket:    envStr("CLAMAV_SOCKET", "/run/clamav/clamd.sock"),
		DockerSocket:    envStr("DOCKER_SOCKET", "/var/run/docker.sock"),

		FileListScanEnabled: envBool("FILE_LIST_SCAN_ENABLED", true),

		ImportMode:             envImportMode("IMPORT_MODE"),
		ImportHardlinkFallback: envHardlinkFallback("IMPORT_HARDLINK_FALLBACK"),

		VaultArchiveEnabled: envBool("VAULT_ARCHIVE_ENABLED", false),

		ExtractArchives: envBool("EXTRACT_ARCHIVES", false),

		AIMonitorEnabled:  envBool("AI_MONITOR_ENABLED", false),
		AIProvider:        envStr("AI_PROVIDER", "ollama"),
		AIAPIUrl:          envStr("AI_API_URL", "http://localhost:11434/v1"),
		AIAPIKey:          envStr("AI_API_KEY", ""),
		AIModel:           envStr("AI_MODEL", "llama3.2"),
		AIMonitorInterval: envInt("AI_MONITOR_INTERVAL", 300),
		AIAutoFix:         envBool("AI_AUTO_FIX", true),

		SABnzbdURL:      envStr("SABNZBD_URL", ""),
		SABnzbdAPIKey:   envStr("SABNZBD_API_KEY", ""),
		SABnzbdCategory: envStr("SABNZBD_CATEGORY", "games"),

		NZBGetURL:      envStr("NZBGET_URL", ""),
		NZBGetUser:     envStr("NZBGET_USER", ""),
		NZBGetPass:     envStr("NZBGET_PASS", ""),
		NZBGetCategory: envStr("NZBGET_CATEGORY", "games"),

		DelugeURL:  envStr("DELUGE_URL", ""),
		DelugePass: envStr("DELUGE_PASS", ""),

		TransmissionURL:  envStr("TRANSMISSION_URL", ""),
		TransmissionUser: envStr("TRANSMISSION_USER", ""),
		TransmissionPass: envStr("TRANSMISSION_PASS", ""),

		RAWGAPIKey: envStr("RAWG_API_KEY", ""),

		GameVaultURL: envStr("GAMEVAULT_URL", ""),
		RomMURL:      envStr("ROMM_URL", ""),

		WebhookURL:  envStr("WEBHOOK_URL", ""),
		WebhookType: envStr("WEBHOOK_TYPE", "generic"),

		SchedulerEnabled:       envBool("SCHEDULER_ENABLED", false),
		SchedulerIntervalHours: envInt("SCHEDULER_INTERVAL_HOURS", 24),
		SchedulerAutoDownload:  envBool("SCHEDULER_AUTO_DOWNLOAD", true),
		SchedulerMinScore:      envInt("SCHEDULER_MIN_SCORE", 70),

		AuthUsername: envStr("AUTH_USERNAME", ""),
		AuthPassword: envStr("AUTH_PASSWORD", ""),
		APIKey:       envStr("API_KEY", ""),

		OIDCEnabled:         envBool("OIDC_ENABLED", false),
		OIDCProviderName:    envStr("OIDC_PROVIDER_NAME", "SSO"),
		OIDCIssuer:          envStr("OIDC_ISSUER", ""),
		OIDCClientID:        envStr("OIDC_CLIENT_ID", ""),
		OIDCClientSecret:    envStr("OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURI:     envStr("OIDC_REDIRECT_URI", ""),
		OIDCAutoCreateUsers: envBool("OIDC_AUTO_CREATE_USERS", true),
		OIDCDefaultRole:     envStr("OIDC_DEFAULT_ROLE", "user"),

		CircuitBreakerThreshold: envInt("CIRCUIT_BREAKER_THRESHOLD", 3),
		CircuitBreakerTimeoutS:  envInt("CIRCUIT_BREAKER_TIMEOUT", 300),

		WatcherEnabled:    envBool("WATCHER_ENABLED", true),
		WatcherIntervalS:  envInt("WATCHER_INTERVAL", 30),
		RemoveAfterImport: envBool("REMOVE_TORRENT_AFTER_IMPORT", false),

		MaxRetries:          envInt("MAX_RETRIES", 2),
		RetryBackoffSeconds: envInt("RETRY_BACKOFF_SECONDS", 60),

		Port:           envInt("GAMARR_PORT", 5001),
		MetricsEnabled: envBool("METRICS_ENABLED", true),
		TorznabAPIKey:  envStr("TORZNAB_API_KEY", ""),

		// Empty by default: Prowlarr numbers indexers per install, so a
		// hardcoded list addresses a different set of trackers on every one.
		// Unset means "discover them from Prowlarr's capabilities".
		ProwlarrGameIndexers: envIntSlice("PROWLARR_GAME_INDEXERS", nil),

		RenameEnabled: envBool("RENAME_ENABLED", false),
		RenamePattern: envStr("RENAME_PATTERN", "{title} ({platform}).{ext}"),

		AutoUpgradeEnabled: envBool("AUTO_UPGRADE_ENABLED", false),
	}
}

func (c *Config) HasAuth() bool {
	return c.AuthUsername != "" && c.AuthPassword != ""
}

func (c *Config) HasAPIKey() bool {
	return c.APIKey != ""
}

func (c *Config) HasProwlarr() bool {
	return c.ProwlarrURL != "" && c.ProwlarrAPIKey != ""
}

func (c *Config) HasQBittorrent() bool {
	return c.QBURL != ""
}

func (c *Config) HasSABnzbd() bool {
	return c.SABnzbdURL != "" && c.SABnzbdAPIKey != ""
}

func (c *Config) HasNZBGet() bool {
	return c.NZBGetURL != ""
}

func (c *Config) HasTransmission() bool {
	return c.TransmissionURL != ""
}

func (c *Config) HasDeluge() bool {
	return c.DelugeURL != ""
}

func (c *Config) HasRAWG() bool {
	return c.RAWGAPIKey != ""
}

func (c *Config) HasOIDC() bool {
	return c.OIDCEnabled && c.OIDCIssuer != "" && c.OIDCClientID != ""
}

func (c *Config) HasClamAV() bool {
	if c.DockerSocket == "" {
		return false
	}
	_, err := os.Stat(c.DockerSocket)
	return err == nil
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envStrAllowEmpty distinguishes an unset variable from an explicitly empty
// one. This lets deployments disable integrations which otherwise have a
// non-empty default (notably QB_URL).
func envStrAllowEmpty(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	s := os.Getenv(key)
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

// envImportMode reads an import mode, falling back to the default (move) with
// a warning rather than refusing to boot on a typo.
func envImportMode(key string) fileops.Mode {
	mode, err := fileops.ParseMode(strings.ToLower(strings.TrimSpace(os.Getenv(key))))
	if err != nil {
		slog.Warn("invalid import mode, using default", "var", key, "error", err, "default", string(fileops.ModeMove))
		return fileops.ModeMove
	}
	return mode
}

func envHardlinkFallback(key string) fileops.Fallback {
	fb, err := fileops.ParseFallback(strings.ToLower(strings.TrimSpace(os.Getenv(key))))
	if err != nil {
		slog.Warn("invalid hardlink fallback, using default", "var", key, "error", err, "default", string(fileops.FallbackError))
		return fileops.FallbackError
	}
	return fb
}

func envBool(key string, fallback bool) bool {
	s := strings.ToLower(os.Getenv(key))
	if s == "" {
		return fallback
	}
	return s == "true" || s == "1" || s == "yes"
}

func envIntSlice(key string, fallback []int) []int {
	s := os.Getenv(key)
	if s == "" {
		return fallback
	}
	parts := strings.Split(s, ",")
	result := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			continue
		}
		result = append(result, n)
	}
	if len(result) == 0 {
		return fallback
	}
	return result
}
