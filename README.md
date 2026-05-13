# Gamarr

[![Build & Test](https://github.com/JeremiahM37/gamarr/actions/workflows/test.yml/badge.svg)](https://github.com/JeremiahM37/gamarr/actions/workflows/test.yml)
[![Release](https://img.shields.io/github/v/release/JeremiahM37/gamarr?include_prereleases)](https://github.com/JeremiahM37/gamarr/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

**The missing *arr for games.** Self-hosted game and ROM search, download, and library manager.

Gamarr searches across all configured indexers (Torznab proxies, direct-download archive listings, web-scrape sources) in parallel for 24 platforms. Results are scored for safety and quality, downloads are managed through your choice of torrent or Usenet client, and files are automatically organized into your game vault and ROM library.

## Features

### Search and Discovery

- **Pluggable indexer registry** -- driver kinds (Torznab proxy, DDL archive listing, web-scrape) loaded at runtime from an embedded JSON registry; optionally overrideable via `GAMARR_SOURCES_URL` / `GAMARR_SOURCES_PATH`
- **24 gaming platforms** -- PC, Switch, PS1-PS5, PSP, PS Vita, Xbox, Xbox 360, Wii, Wii U, NES, SNES, N64, GameCube, Game Boy, GBA, DS, 3DS, Genesis, Saturn, Dreamcast, Atari 2600
- **Search scoring** -- composite 0-100 score based on title match, platform relevance, seeder count, file size, and safety analysis
- **Safety scoring** -- analyzes file names, sizes, and scene group trust to detect malware, crack-only uploads, and suspicious downloads
- **Duplicate detection** -- search results show an `in_library` flag when a game already exists in your library
- **Release calendar** -- browse upcoming and recently released games via RAWG.io API integration

### Quality Control

- **Quality profiles** -- rank sources by preference, enable auto-upgrade when a better release appears
- **Release profiles** -- preferred and excluded words to filter and boost search results
- **Blocklist** -- failed or unwanted releases are auto-added, filtered from future results

### Downloads

- **4 download clients** -- qBittorrent, Transmission, Deluge (torrents), SABnzbd (Usenet/NZB)
- **Download monitoring** -- real-time progress tracking with auto-organize on completion
- **Retry and recovery** -- configurable retry attempts with backoff, orphan torrent recovery on startup
- **Archive extraction** -- auto-extract 7z, zip, and rar archives after download

### Library Management

- **SQLite-backed library** with platform tagging, search, and pagination
- **Tags** -- create and assign tags for custom organization
- **Rename on import** -- configurable pattern (e.g., `{title} ({platform}).{ext}`), scene tag cleanup
- **File organization** -- auto-sort ROMs by platform directory, PC games to vault
- **Manual import** -- scan directories and import existing files
- **Import/export** -- JSON and CSV for library, wishlist, and requests
- **Backup and restore** -- full database backup with admin-only access

### Requests and Wishlist

- **Request workflow** -- pending, approved, searching, downloading, completed states
- **Wishlist** -- save wanted games, track availability
- **Scheduled searches** -- automatic wishlist searches with configurable interval, auto-download best match

### Notifications

- **In-app notifications** -- unread count, mark read, per-event tracking
- **Webhooks** -- Discord and generic webhook support with per-event filtering
- **Event types** -- download complete, request approved/completed/failed

### Integrations

- **RAWG.io metadata** -- cover art, descriptions, ratings, release dates
- **GameVault** -- link library items to your GameVault server
- **RomM** -- link library items to your RomM instance
- **ClamAV** (optional) -- scan downloaded files for malware

### Administration

- **Multi-user auth** -- session-based login with API key support (`X-Api-Key` header)
- **Admin dashboard** -- system overview, user management, connection tests
- **Rate limiting** -- per-category limits (login, search, download, general API)
- **Security headers** -- request size limits, CORS, standard hardening
- **Prometheus metrics** at `/metrics`
- **AI monitor** (optional) -- Ollama/OpenAI-powered download analysis and auto-fix suggestions

### Technical

- **Single static binary** -- ~17 MB, zero CGO, pure-Go SQLite (`modernc.org/sqlite`)
- **Docker-ready** -- minimal Alpine image with p7zip, runs as non-root user
- **Mobile-responsive UI** -- Tailwind CSS, dark theme, platform filters
- **43 automated end-to-end tests**

## Supported Platforms

| Platform | Slug | DDL archive | Torznab |
|----------|------|-------------|---------|
| PC | `pc` | -- | Yes |
| Nintendo Switch | `switch` | -- | Yes |
| PS1 | `psx` | Yes | Yes |
| PS2 | `ps2` | Yes | Yes |
| PS3 | `ps3` | Yes | Yes |
| PS4 | `ps4` | -- | Yes |
| PSP | `psp` | Yes | Yes |
| PS Vita | `psvita` | Yes | Yes |
| Xbox | `xbox` | Yes | Yes |
| Xbox 360 | `xbox360` | Yes | Yes |
| Wii | `wii` | Yes | Yes |
| Wii U | `wiiu` | Yes | Yes |
| NES | `nes` | Yes | Yes |
| SNES | `snes` | Yes | Yes |
| Nintendo 64 | `n64` | Yes | Yes |
| Nintendo DS | `nds` | Yes | Yes |
| Nintendo 3DS | `3ds` | Yes | Yes |
| Game Boy | `gb` | Yes | Yes |
| Game Boy Advance | `gba` | Yes | Yes |
| Sega Genesis | `genesis` | Yes | Yes |
| Sega Saturn | `saturn` | Yes | Yes |
| Dreamcast | `dreamcast` | Yes | Yes |
| GameCube | `gamecube` | Yes | Yes |
| Atari 2600 | `atari2600` | Yes | Yes |

## Quick Start

### Docker (recommended)

```yaml
services:
  gamarr:
    build: .
    ports:
      - "5001:5001"
    volumes:
      - ./data:/data/gamarr
      - /path/to/games/vault:/data/vault
      - /path/to/roms:/data/roms
    environment:
      - PROWLARR_URL=http://prowlarr:9696
      - PROWLARR_API_KEY=your-prowlarr-api-key
      - QB_URL=http://qbittorrent:8080
      - QB_USER=admin
      - QB_PASS=changeme
    restart: unless-stopped
```

```bash
docker compose up -d
```

### Binary

```bash
# Build
go build -o gamarr ./cmd/gamarr/

# Configure
export PROWLARR_URL=http://localhost:9696
export PROWLARR_API_KEY=your-prowlarr-api-key
export QB_URL=http://localhost:8080
# ... set other env vars as needed

# Run
./gamarr
```

Open `http://localhost:5001` in your browser.

## Configuration

All configuration is via environment variables.

### Server

| Variable | Default | Description |
|----------|---------|-------------|
| `GAMARR_PORT` | `5001` | HTTP listen port |
| `DATA_DIR` | `/data/gamarr` | Data directory (SQLite DB, settings) |
| `METRICS_ENABLED` | `true` | Enable Prometheus metrics endpoint |
| `AUTH_USERNAME` | | Admin username (enables auth when set) |
| `AUTH_PASSWORD` | | Admin password |

### Sources Registry

The active indexer list (base URLs, per-platform path mappings) is loaded at startup from, in order: `GAMARR_SOURCES_PATH`, `GAMARR_SOURCES_URL`, or an embedded fallback. Legacy per-source env vars below continue to take precedence over registry values.

| Variable | Default | Description |
|----------|---------|-------------|
| `GAMARR_SOURCES_URL` | | URL of a JSON sources registry; fetched at startup, falls back to embedded default if unreachable |
| `GAMARR_SOURCES_PATH` | | Local path to a sources registry JSON file; takes precedence over URL |
| `MYRIENT_URL` | | Override base URL of the DDL archive-listing source |
| `VIMM_URL` | | Override base URL of the web-scrape source |

### Search Sources

| Variable | Default | Description |
|----------|---------|-------------|
| `PROWLARR_URL` | `http://prowlarr:9696` | Prowlarr URL |
| `PROWLARR_API_KEY` | | Prowlarr API key |
| `PROWLARR_GAME_INDEXERS` | `7,5,15,9,8,3,4` | Comma-separated indexer IDs to search |
| `RAWG_API_KEY` | | RAWG.io API key (enables metadata, calendar) |

### Download Clients

| Variable | Default | Description |
|----------|---------|-------------|
| `QB_URL` | `http://qbittorrent:8080` | qBittorrent Web UI URL |
| `QB_USER` | `admin` | qBittorrent username |
| `QB_PASS` | | qBittorrent password |
| `QB_SAVE_PATH` | `/data/incoming/` | Download save path |
| `QB_CATEGORY` | `games` | Torrent category |
| `TRANSMISSION_URL` | | Transmission RPC URL |
| `TRANSMISSION_USER` | | Transmission username |
| `TRANSMISSION_PASS` | | Transmission password |
| `DELUGE_URL` | | Deluge Web UI URL |
| `DELUGE_PASS` | | Deluge password |
| `SABNZBD_URL` | | SABnzbd URL |
| `SABNZBD_API_KEY` | | SABnzbd API key |
| `SABNZBD_CATEGORY` | `games` | NZB download category |

### Library and Organization

| Variable | Default | Description |
|----------|---------|-------------|
| `GAMES_VAULT_PATH` | `/data/vault` | PC game storage directory |
| `GAMES_ROMS_PATH` | `/data/roms` | ROM storage directory |
| `RENAME_ENABLED` | `false` | Rename files on import |
| `RENAME_PATTERN` | `{title} ({platform}).{ext}` | Rename pattern |
| `GAMEVAULT_URL` | | GameVault server URL |
| `ROMM_URL` | | RomM server URL |

### Notifications

| Variable | Default | Description |
|----------|---------|-------------|
| `WEBHOOK_URL` | | Default webhook URL |
| `WEBHOOK_TYPE` | `generic` | Webhook type (`discord`, `generic`) |

### Downloads

| Variable | Default | Description |
|----------|---------|-------------|
| `EXTRACT_ARCHIVES` | `false` | Auto-extract downloaded archives |
| `MAX_RETRIES` | `2` | Download retry attempts |
| `RETRY_BACKOFF_SECONDS` | `60` | Seconds between retries |

### AI Monitor (optional)

| Variable | Default | Description |
|----------|---------|-------------|
| `AI_MONITOR_ENABLED` | `false` | Enable AI-powered download monitoring |
| `AI_PROVIDER` | `ollama` | AI provider (`ollama`, `openai`) |
| `AI_API_URL` | `http://localhost:11434/v1` | AI API endpoint |
| `AI_MODEL` | `llama3.2` | Model name |
| `AI_MONITOR_INTERVAL` | `300` | Analysis interval in seconds |
| `AI_AUTO_FIX` | `true` | Automatically apply AI suggestions |

### ClamAV (optional)

| Variable | Default | Description |
|----------|---------|-------------|
| `CLAMAV_CONTAINER` | `clamav` | ClamAV Docker container name |
| `CLAMAV_SOCKET` | `/run/clamav/clamd.sock` | ClamAV socket path |

## Architecture

Single static binary, zero CGO dependencies, pure-Go SQLite via `modernc.org/sqlite`.

```
cmd/gamarr/main.go              Entry point
internal/
  config/                        Environment variable configuration
  db/                            SQLite persistence + migrations
  models/                        Core types (games, downloads, requests, notifications)
  api/                           HTTP handlers + chi router + auth + rate limiting
    web/                         Embedded web UI assets
  search/                        Source drivers (Torznab, DDL archive, web-scrape)
  sources/                       Runtime sources registry (embedded defaults + loader)
  download/                      Download manager (qBit, Transmission, Deluge, DDL)
  sabnzbd/                       SABnzbd client
  safety/                        Safety scoring engine
  scheduler/                     Scheduled wishlist searches
  monitor/                       AI-powered download monitoring
  metadata/                      RAWG.io API client
  organize/                      File organization and rename
  platform/                      Platform definitions, detection, category mapping
  qbit/                          qBittorrent API client
  webhook/                       Discord + generic webhook delivery
web/
  index.html                     Single-page web UI (Tailwind CSS)
tests/
  e2e_test.py                    43 end-to-end tests
Dockerfile                       Multi-stage Alpine build
```

## Testing

```bash
# Run tests (requires a running Gamarr instance)
cd tests
python e2e_test.py
```

## License

MIT

## Disclaimer

This software is provided for **educational and personal use only**. Users are responsible for ensuring their use complies with all applicable laws and regulations in their jurisdiction. The developers do not condone or encourage copyright infringement or any illegal activity. This tool does not host, store, or distribute any copyrighted content, and ships with no built-in catalog of indexers -- the list of endpoints to query comes from a user-overridable registry.
