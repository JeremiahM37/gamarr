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
