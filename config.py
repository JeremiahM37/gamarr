import os

# Prowlarr
PROWLARR_URL = os.getenv("PROWLARR_URL", "http://prowlarr:9696")
PROWLARR_API_KEY = os.getenv("PROWLARR_API_KEY", "")

# qBittorrent (via Gluetun VPN - localhost because same network namespace)
QB_URL = os.getenv("QB_URL", "http://localhost:8080")
QB_USER = os.getenv("QB_USER", "admin")
QB_PASS = os.getenv("QB_PASS", "")
QB_SAVE_PATH = os.getenv("QB_SAVE_PATH", "/games-incoming/")
QB_CATEGORY = "games"

# Library destination paths (as seen inside the Gamarr container)
GAMES_VAULT_PATH = os.getenv("GAMES_VAULT_PATH", "/vault")
GAMES_ROMS_PATH = os.getenv("GAMES_ROMS_PATH", "/roms")
