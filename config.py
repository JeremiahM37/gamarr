import os

# Prowlarr
PROWLARR_URL = os.getenv("PROWLARR_URL", "http://prowlarr:9696")
PROWLARR_API_KEY = os.getenv("PROWLARR_API_KEY", "")

# qBittorrent
QB_URL = os.getenv("QB_URL", "http://qbittorrent:8080")
QB_USER = os.getenv("QB_USER", "admin")
QB_PASS = os.getenv("QB_PASS", "")
QB_SAVE_PATH = os.getenv("QB_SAVE_PATH", "/data/incoming/")
QB_CATEGORY = os.getenv("QB_CATEGORY", "games")

# Library destination paths (as seen inside the Gamarr container)
GAMES_VAULT_PATH = os.getenv("GAMES_VAULT_PATH", "/data/vault")
GAMES_ROMS_PATH = os.getenv("GAMES_ROMS_PATH", "/data/roms")

# Data directory for SQLite database and custom sources file
DATA_DIR = os.getenv("DATA_DIR", "/data/gamarr")

# ClamAV (optional antivirus scanning)
CLAMAV_CONTAINER = os.getenv("CLAMAV_CONTAINER", "clamav")
CLAMAV_SOCKET = os.getenv("CLAMAV_SOCKET", "/run/clamav/clamd.sock")
DOCKER_SOCKET = os.getenv("DOCKER_SOCKET", "/var/run/docker.sock")


def has_prowlarr():
    return bool(PROWLARR_URL and PROWLARR_API_KEY)


def has_qbittorrent():
    return bool(QB_URL)


def has_clamav():
    return bool(DOCKER_SOCKET and os.path.exists(DOCKER_SOCKET))

# Experimental: extract archives after organizing ROMs (default off)
EXTRACT_ARCHIVES = os.getenv("EXTRACT_ARCHIVES", "false").lower() in ("true", "1", "yes")

# ── AI Monitor ─────────────────────────────────────────────────────────────────
AI_MONITOR_ENABLED = os.getenv("AI_MONITOR_ENABLED", "false").lower() in ("true", "1", "yes")
AI_PROVIDER = os.getenv("AI_PROVIDER", "ollama")      # ollama | openai | anthropic
AI_API_URL = os.getenv("AI_API_URL", "http://localhost:11434/v1")
AI_API_KEY = os.getenv("AI_API_KEY", "")
AI_MODEL = os.getenv("AI_MODEL", "llama3.2")
AI_MONITOR_INTERVAL = int(os.getenv("AI_MONITOR_INTERVAL", "300"))
AI_AUTO_FIX = os.getenv("AI_AUTO_FIX", "true").lower() in ("true", "1", "yes")
