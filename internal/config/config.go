package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	// Prowlarr
	ProwlarrURL    string
	ProwlarrAPIKey string

	// qBittorrent
	QBURL      string
	QBUser     string
	QBPass     string
	QBSavePath string
	QBCategory string

	// Library destination paths
	GamesVaultPath string
	GamesRomsPath  string

	// Data directory
	DataDir string

	// ClamAV
	ClamAVContainer string
	ClamAVSocket    string
	DockerSocket    string

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

	// Retry
	MaxRetries          int
	RetryBackoffSeconds int

	// Auth
	AuthUsername string
	AuthPassword string
	APIKey       string

	// Server
	Port           int
	MetricsEnabled bool

	// Prowlarr game indexer IDs
	ProwlarrGameIndexers []int
}

func Load() *Config {
	return &Config{
		ProwlarrURL:    envStr("PROWLARR_URL", "http://prowlarr:9696"),
		ProwlarrAPIKey: envStr("PROWLARR_API_KEY", ""),

		QBURL:      envStr("QB_URL", "http://qbittorrent:8080"),
		QBUser:     envStr("QB_USER", "admin"),
		QBPass:     envStr("QB_PASS", ""),
		QBSavePath: envStr("QB_SAVE_PATH", "/data/incoming/"),
		QBCategory: envStr("QB_CATEGORY", "games"),

		GamesVaultPath: envStr("GAMES_VAULT_PATH", "/data/vault"),
		GamesRomsPath:  envStr("GAMES_ROMS_PATH", "/data/roms"),

		DataDir: envStr("DATA_DIR", "/data/gamarr"),

		ClamAVContainer: envStr("CLAMAV_CONTAINER", "clamav"),
		ClamAVSocket:    envStr("CLAMAV_SOCKET", "/run/clamav/clamd.sock"),
		DockerSocket:    envStr("DOCKER_SOCKET", "/var/run/docker.sock"),

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

		DelugeURL:  envStr("DELUGE_URL", ""),
		DelugePass: envStr("DELUGE_PASS", ""),

		TransmissionURL:  envStr("TRANSMISSION_URL", ""),
		TransmissionUser: envStr("TRANSMISSION_USER", ""),
		TransmissionPass: envStr("TRANSMISSION_PASS", ""),

		RAWGAPIKey: envStr("RAWG_API_KEY", ""),

		GameVaultURL: envStr("GAMEVAULT_URL", ""),
		RomMURL:      envStr("ROMM_URL", ""),

		AuthUsername: envStr("AUTH_USERNAME", ""),
		AuthPassword: envStr("AUTH_PASSWORD", ""),
		APIKey:       envStr("API_KEY", ""),

		MaxRetries:          envInt("MAX_RETRIES", 2),
		RetryBackoffSeconds: envInt("RETRY_BACKOFF_SECONDS", 60),

		Port:           envInt("GAMARR_PORT", 5001),
		MetricsEnabled: envBool("METRICS_ENABLED", true),

		ProwlarrGameIndexers: envIntSlice("PROWLARR_GAME_INDEXERS", []int{7, 5, 15, 9, 8, 3, 4}),
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

func (c *Config) HasTransmission() bool {
	return c.TransmissionURL != ""
}

func (c *Config) HasDeluge() bool {
	return c.DelugeURL != ""
}

func (c *Config) HasRAWG() bool {
	return c.RAWGAPIKey != ""
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
