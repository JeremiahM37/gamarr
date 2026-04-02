# Gamarr

[![Build & Test](https://github.com/JeremiahM37/gamarr/actions/workflows/test.yml/badge.svg)](https://github.com/JeremiahM37/gamarr/actions/workflows/test.yml)
[![Release](https://img.shields.io/github/v/release/JeremiahM37/gamarr?include_prereleases)](https://github.com/JeremiahM37/gamarr/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

**The missing *arr for games.** Self-hosted game and ROM search and download manager.

Gamarr searches across Prowlarr torrent indexers, Myrient (verified ROM sets via direct download), and Vimm (fallback DDL) to find games for 24 platforms. It scores results for safety, manages downloads via qBittorrent or SABnzbd, and organizes files into your game vault and ROM library.

## Features

- **24 gaming platforms** -- PC, Switch, PS1-PS4, PSP, PS Vita, Xbox, Xbox 360, Wii, Wii U, NES, SNES, N64, Game Boy, Game Boy Advance, Sega Genesis, Sega Saturn, DS, 3DS, and more
- **3 search sources** -- Prowlarr (torrent indexers), Myrient (direct download, verified No-Intro/Redump sets), Vimm (fallback DDL)
- **Safety scoring** -- analyzes file names, sizes, and sources to detect malware, crack-only uploads, and suspicious downloads
- **Library management** -- SQLite-backed game library with platform tagging and deduplication
- **Wishlist** -- save wanted games, track availability
- **Download monitoring** -- real-time progress tracking with auto-organize on completion
- **DDL source management** -- add and manage custom direct download sources
- **Dual download clients** -- qBittorrent (torrents) and SABnzbd (Usenet/NZB)
- **AI monitor** (optional) -- LLM-powered download analysis and auto-fix suggestions
- **ClamAV integration** (optional) -- scan downloaded files for malware
- **Archive extraction** -- auto-extract 7z, zip, and rar archives
- **Prometheus metrics** at `/metrics`
- **Modern dark UI** -- Tailwind CSS, responsive, platform filters
- **43 automated end-to-end tests**
- **Single static binary** -- ~15 MB, zero CGO, pure-Go SQLite (`modernc.org/sqlite`)
- **Docker-ready** -- minimal Alpine image with p7zip, runs as non-root user

## Supported Platforms

| Platform | Slug | Myrient | Prowlarr |
|----------|------|---------|----------|
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

## Screenshots

<!-- Add screenshots of the search, library, and download views here -->

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

### Prowlarr

| Variable | Default | Description |
|----------|---------|-------------|
| `PROWLARR_URL` | `http://prowlarr:9696` | Prowlarr URL |
| `PROWLARR_API_KEY` | | Prowlarr API key |
| `PROWLARR_GAME_INDEXERS` | `7,5,15,9,8,3,4` | Comma-separated Prowlarr indexer IDs to search |

### qBittorrent

| Variable | Default | Description |
|----------|---------|-------------|
| `QB_URL` | `http://qbittorrent:8080` | qBittorrent Web UI URL |
| `QB_USER` | `admin` | qBittorrent username |
| `QB_PASS` | | qBittorrent password |
| `QB_SAVE_PATH` | `/data/incoming/` | Download save path (inside qBit container) |
| `QB_CATEGORY` | `games` | Torrent category |

### SABnzbd (Usenet)

| Variable | Default | Description |
|----------|---------|-------------|
| `SABNZBD_URL` | | SABnzbd URL |
| `SABNZBD_API_KEY` | | SABnzbd API key |
| `SABNZBD_CATEGORY` | `games` | NZB download category |

### Library Paths

| Variable | Default | Description |
|----------|---------|-------------|
| `GAMES_VAULT_PATH` | `/data/vault` | PC game storage directory |
| `GAMES_ROMS_PATH` | `/data/roms` | ROM storage directory |

### External Services

| Variable | Default | Description |
|----------|---------|-------------|
| `GAMEVAULT_URL` | | GameVault server URL (for library links) |
| `ROMM_URL` | | RomM server URL (for library links) |

### ClamAV (optional)

| Variable | Default | Description |
|----------|---------|-------------|
| `CLAMAV_CONTAINER` | `clamav` | ClamAV Docker container name |
| `CLAMAV_SOCKET` | `/run/clamav/clamd.sock` | ClamAV socket path |
| `DOCKER_SOCKET` | `/var/run/docker.sock` | Docker socket (for ClamAV container access) |

### AI Monitor (optional)

| Variable | Default | Description |
|----------|---------|-------------|
| `AI_MONITOR_ENABLED` | `false` | Enable AI-powered download monitoring |
| `AI_PROVIDER` | `ollama` | AI provider (`ollama`, `openai`) |
| `AI_API_URL` | `http://localhost:11434/v1` | AI API endpoint |
| `AI_API_KEY` | | API key (if required by provider) |
| `AI_MODEL` | `llama3.2` | Model name |
| `AI_MONITOR_INTERVAL` | `300` | Analysis interval in seconds |
| `AI_AUTO_FIX` | `true` | Automatically apply AI suggestions |

### Downloads

| Variable | Default | Description |
|----------|---------|-------------|
| `EXTRACT_ARCHIVES` | `false` | Auto-extract downloaded archives |
| `MAX_RETRIES` | `2` | Download retry attempts |
| `RETRY_BACKOFF_SECONDS` | `60` | Seconds between retries |

## API Endpoints

### Search

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/search?q=&platform=` | Search games (optional platform filter) |
| GET | `/api/platforms` | List supported platforms |
| GET | `/api/sources` | List search sources and status |

### Downloads

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/download` | Start a download |
| GET | `/api/downloads` | List active/completed downloads |
| DELETE | `/api/downloads/torrent/{hash}` | Remove a torrent download |
| DELETE | `/api/downloads/{jobID}` | Remove a download job |
| POST | `/api/downloads/clear` | Clear finished downloads |
| POST | `/api/downloads/organize/{hash}` | Manually organize a completed torrent |
| POST | `/api/downloads/{jobID}/retry` | Retry a failed download |

### Library

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/library?q=&platform=` | Browse library (filterable, paginated) |
| DELETE | `/api/library/{id}` | Remove item from library |

### Wishlist

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/wishlist` | List wishlist items |
| POST | `/api/wishlist` | Add game to wishlist |
| DELETE | `/api/wishlist/{id}` | Remove from wishlist |

### DDL Sources

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/ddl-sources` | List custom DDL sources |
| POST | `/api/ddl-sources` | Add a DDL source |
| DELETE | `/api/ddl-sources/{idx}` | Remove a DDL source |

### Configuration

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/settings` | Get persistent settings |
| PUT | `/api/settings` | Update persistent settings |
| GET | `/api/config` | Current configuration summary |
| GET | `/api/stats` | Library and download statistics |
| GET | `/api/activity` | Recent activity log |

### Connection Tests

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/test/prowlarr` | Test Prowlarr connection |
| POST | `/api/test/qbittorrent` | Test qBittorrent connection |
| POST | `/api/test/sabnzbd` | Test SABnzbd connection |

### AI Monitor

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/monitor/status` | Monitor status and pending actions |
| POST | `/api/monitor/analyze` | Trigger manual analysis |
| POST | `/api/monitor/actions/{actionID}/approve` | Approve a suggested action |
| POST | `/api/monitor/actions/{actionID}/dismiss` | Dismiss a suggested action |

### System

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/health` | Health check |
| GET | `/metrics` | Prometheus metrics |

## Architecture

Single static binary, zero CGO dependencies, pure-Go SQLite via `modernc.org/sqlite`.

```
cmd/gamarr/main.go              Entry point
internal/
  config/config.go               Env var configuration
  db/                            SQLite persistence + migrations
  models/                        Core types (games, downloads, wishlist)
  api/                           HTTP handlers + chi router
    api.go                       Route registration + handlers
    handlers_extra.go            Library/wishlist pagination
    web/                         Embedded web UI assets
  search/                        Search source implementations (Prowlarr, Myrient, Vimm)
  download/                      Download manager (qBit + SABnzbd + DDL)
  monitor/                       AI-powered download monitoring
  platform/                      Platform definitions, detection, category mapping
  qbit/                          qBittorrent API client
  sabnzbd/                       SABnzbd API client
  safety/                        Safety scoring engine
web/
  index.html                     Single-page web UI (Tailwind CSS)
tests/
  e2e_test.py                    43 end-to-end tests
Dockerfile                       Multi-stage Alpine build
```

## Testing

Gamarr includes 43 automated end-to-end tests covering search, downloads, library management, and platform detection.

```bash
# Run tests (requires a running Gamarr instance)
cd tests
python e2e_test.py
```

## License

MIT

## Disclaimer

This software is provided for **educational and personal use only**. Users are responsible for ensuring their use complies with all applicable laws and regulations in their jurisdiction. The developers do not condone or encourage copyright infringement or any illegal activity. This tool does not host, store, or distribute any copyrighted content.
