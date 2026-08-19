# Gamarr

[![Build & Test](https://github.com/JeremiahM37/gamarr/actions/workflows/test.yml/badge.svg)](https://github.com/JeremiahM37/gamarr/actions/workflows/test.yml)
[![Release](https://img.shields.io/github/v/release/JeremiahM37/gamarr?include_prereleases)](https://github.com/JeremiahM37/gamarr/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

**The missing *arr for games.** Self-hosted game and ROM search, download, and library manager.

Gamarr searches across all configured indexers (Torznab proxies, direct-download archive listings, web-scrape sources) in parallel for 24 platforms. Results are scored for safety and quality, downloads are managed through your choice of torrent or Usenet client, and files are automatically organized into your game vault and ROM library.

Single ~17MB Go binary, no runtime dependencies — **~9MB RSS idle** in a real homelab[^1], typically 10-30× lower than other self-hosted game library / ROM tools. Comfortable on a Pi or any thermally-constrained mini-PC.

[^1]: Measured on the current main in an LXC on Debian 12 (Mar 2026). Reference: ROMM ≈ 320MB, GameVault backend ≈ 157MB on the same host.

![Gamarr library view — game cards across PC, NES, SNES, GBA and Genesis with platform badges, file sizes, and RomM/GameVault links](docs/screenshot.png)

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

- **5 download clients** -- qBittorrent, Transmission, Deluge (torrents), SABnzbd and NZBGet (Usenet/NZB)
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

- **Multi-user auth** -- session-based login with API key support (`X-Api-Key` header), TOTP, and OIDC/SSO
- **Sign-in UI** -- the web UI prompts for credentials whenever auth is configured, and re-prompts when a session expires
- **Admin dashboard** -- system overview, user management, connection tests
- **Rate limiting** -- per-category limits (login, search, download, general API)
- **Security headers** -- request size limits, CORS, standard hardening
- **Prometheus metrics** at `/metrics`
- **AI monitor** (optional) -- Ollama/OpenAI-powered download analysis and auto-fix suggestions

### Technical

- **Single static binary** -- ~17 MB on disk, ~9 MB RSS idle, zero CGO, pure-Go SQLite (`modernc.org/sqlite`)
- **Prebuilt Docker image** -- minimal Alpine image with p7zip, published to GHCR for `linux/amd64` and `linux/arm64`; no local build required
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
    image: ghcr.io/jeremiahm37/gamarr:latest
    container_name: gamarr
    ports:
      - "5001:5001"
    volumes:
      # One mount spanning the download directory and the library, so hardlink
      # imports work. Gamarr's defaults live under /data: the database in
      # /data/gamarr, downloads in /data/incoming, the library in /data/vault
      # and /data/roms. See "Hardlink import: volume layout" below before
      # splitting this into a volume per directory.
      - /srv/gamarr:/data
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

Open `http://localhost:5001`.

#### Docker images

Images are published to the GitHub Container Registry at
[`ghcr.io/jeremiahm37/gamarr`](https://github.com/JeremiahM37/gamarr/pkgs/container/gamarr).
The registry is public — pulling needs no login.

| Tag | Points at | Use it when |
|-----|-----------|-------------|
| `latest` | the newest release | you want the current stable build |
| `vX.Y.Z` | that release, forever | you want a pin that never moves |
| `edge` | the current `main` | you want unreleased fixes and can take churn |

```bash
docker pull ghcr.io/jeremiahm37/gamarr:latest
```

Every image is a multi-arch manifest covering `linux/amd64` and `linux/arm64`,
so the same tag works on x86 hosts and on a Raspberry Pi 4/5 or Apple-silicon
Docker Desktop — the daemon picks the right one. (Releases up to and including
`v1.3.0` predate multi-arch support and are amd64-only. `edge` and every release
after `v1.3.0` carry both architectures.)

#### Building from source instead

The prebuilt image is the supported path; build locally only if you are
modifying Gamarr. Swap the `image:` line above for `build: .` from a clone of
this repo, or:

```bash
docker build -t gamarr:local .
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
| `API_KEY` | | API key for programmatic access (`X-Api-Key` header or `?apikey=`) |

Auth is off until one of `AUTH_USERNAME`/`AUTH_PASSWORD`, `API_KEY`, or a registered
user exists. With any of them set, the UI shows a sign-in form on load.

On an instance that is already protected this way, creating the **first** user
account requires credentials — an admin session or the API key. Only a wholly
unconfigured instance can be claimed anonymously. To move from `AUTH_USERNAME`
to multi-user accounts, sign in with the legacy password first, then register.

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
| `PROWLARR_GAME_INDEXERS` | *(auto)* | Comma-separated indexer IDs to search. Unset means every enabled indexer that advertises game categories (see [Prowlarr indexers](#prowlarr-indexers)) |
| `RAWG_API_KEY` | | RAWG.io API key (enables metadata, calendar) |

#### Prowlarr indexers

Gamarr asks Prowlarr which of its indexers carry games — every enabled one
advertising a category in the Newznab game ranges (1000–1999 Console, 4000–4999
PC, including subcategories) — and searches those. Nothing to configure.

Set `PROWLARR_GAME_INDEXERS` to a comma-separated list of indexer IDs only to
narrow that set: to skip a slow tracker, or one you would rather query by hand.
IDs in the list that the instance does not have are logged and skipped rather
than searched, because Prowlarr numbers indexers in the order each user added
them — an ID copied from another install points at a different tracker, or at
none, and a search of an indexer that does not exist returns HTTP 200 with an
empty list rather than an error.

Search results name the indexers that were queried, so an empty result set
shows whether the right trackers were consulted.

### Download Clients

| Variable | Default | Description |
|----------|---------|-------------|
| `QB_URL` | `http://qbittorrent:8080` | qBittorrent Web UI URL |
| `QB_USER` | `admin` | qBittorrent username |
| `QB_PASS` | | qBittorrent password |
| `QB_SAVE_PATH` | `/data/incoming/` | Download save path |
| `QB_CATEGORY` | `games` | Torrent category. Gamarr only ever enumerates torrents in this category, so a dedicated one keeps it clear of everything else in the client |
| `TRANSMISSION_URL` | | Transmission RPC URL |
| `TRANSMISSION_USER` | | Transmission username |
| `TRANSMISSION_PASS` | | Transmission password |
| `DELUGE_URL` | | Deluge Web UI URL |
| `DELUGE_PASS` | | Deluge password |
| `SABNZBD_URL` | | SABnzbd URL |
| `SABNZBD_API_KEY` | | SABnzbd API key |
| `SABNZBD_CATEGORY` | `games` | NZB download category |
| `NZBGET_URL` | | NZBGet URL (for example `http://nzbget:6789`) |
| `NZBGET_USER` | | NZBGet control username |
| `NZBGET_PASS` | | NZBGet control password |
| `NZBGET_CATEGORY` | `games` | NZB download category |

Configure either SABnzbd or NZBGet for NZB downloads. If both are configured, SABnzbd is used first to preserve existing deployments.

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
| `IMPORT_MODE` | `move` | How finished torrents enter the library: `move`, `hardlink`, `symlink`, `copy` (see [Import modes](#import-modes)) |
| `IMPORT_HARDLINK_FALLBACK` | `error` | What a hardlink import does when source and library are on different filesystems: `error`, `copy`, `symlink`, `move` |
| `REMOVE_TORRENT_AFTER_IMPORT` | `false` | Remove the torrent from the client once imported (never deletes the data under a source-preserving mode) |
| `EXTRACT_ARCHIVES` | `false` | Auto-extract downloaded archives |
| `FILE_LIST_SCAN_ENABLED` | `true` | Block a download whose file names carry a dangerous extension. Set `false` if your sources legitimately ship `.bat`/`.cmd` next to the payload |
| `MAX_RETRIES` | `2` | Download retry attempts |
| `RETRY_BACKOFF_SECONDS` | `60` | Seconds between retries |

### Import modes

By default Gamarr **moves** finished content into the library. That empties the
download client's completed directory, so the torrent stops seeding — on a
private tracker that costs ratio and can earn a hit-and-run penalty.

Set `IMPORT_MODE` (or change it under **Settings → Options** at runtime) to keep
seeding:

| Mode | Library entry | Source | Extra disk |
|------|---------------|--------|-----------|
| `move` (default) | the files themselves | gone — seeding stops | none |
| `hardlink` | a second name for the same data | untouched, keeps seeding | none |
| `symlink` | a link to the download | untouched, keeps seeding | none |
| `copy` | an independent copy | untouched, keeps seeding | 2× the release |

**Hardlink is the one to pick.** It costs no extra disk, and the library entry
survives even after the torrent is removed from the client. Its one requirement
is that the download directory and the library be reachable through the **same
mount** — see the volume layout section below, because in Docker "same
filesystem" is not enough.

If they are not, a hardlink import fails with a message naming the two paths
instead of silently doing something else. Set `IMPORT_HARDLINK_FALLBACK=copy`
(or `symlink`, or `move`) if you would rather it degrade automatically.

#### Hardlink import: volume layout

Selecting `hardlink` in **Settings → Options** runs a real trial link between
the download directory and the library and reports the result underneath the
setting, so a layout that cannot hardlink is visible immediately rather than at
import time. The same check runs at startup and logs a warning.

Two things have to be true, and Docker's usual volume layout breaks both.

**1. One mount, not two.** This looks correct and does not work:

```yaml
volumes:
  - /data/downloads/games:/data/incoming    # separate mount
  - /data/games/library:/data/vault         # separate mount
```

Both paths can be on one filesystem — the same disk, the same pool, `stat`
even reports the same device for each — and `ln` still fails with
`Cross-device link`. The kernel refuses hardlinks across *mount points*, which
two bind mounts are, however they are backed. Mount the common parent once
instead:

```yaml
volumes:
  - /data:/data          # one mount spanning downloads and library
```

**2. Gamarr's paths must match the download client's.** There is no remote path
mapping (the equivalent of Sonarr/Radarr's Remote Path Mappings). Whatever
qBittorrent reports as a torrent's content path is used verbatim, so it has to
resolve to the same content inside the Gamarr container — mount the same parent
at the same place in both containers. Mounting the download directory at
`/data/incoming` in Gamarr while the client calls it `/data/downloads/games`
breaks imports even before hardlinks enter into it.

The single-parent layout above satisfies both at once.

Under a source-preserving mode Gamarr leaves the torrent in the client so it
keeps seeding. `REMOVE_TORRENT_AFTER_IMPORT=true` still removes it if you want
that, but never deletes the data the library points at.

Two paths always move, whatever the mode is set to: direct downloads and Usenet
downloads. Gamarr fetched those itself and nothing is seeding them, so leaving a
staging copy behind would only leak disk.

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
  search/                        Source drivers (Torznab, DDL archive, web-scrape)
  sources/                       Runtime sources registry (embedded defaults + loader)
  download/                      Download manager (qBit, Transmission, Deluge, Usenet, DDL)
  sabnzbd/                       SABnzbd client
  nzbget/                        NZBGet JSON-RPC client
  safety/                        Safety scoring engine
  scheduler/                     Scheduled wishlist searches
  monitor/                       AI-powered download monitoring
  metadata/                      RAWG.io API client
  organize/                      File organization and rename
  platform/                      Platform definitions, detection, category mapping
  qbit/                          qBittorrent API client
  webhook/                       Discord + generic webhook delivery
web/
  embed.go                       go:embed of the UI into the binary
  index.html                     Single-page web UI markup
  static/js/app.js               UI logic (strict-CSP event delegation)
  static/js/vendor/tailwind.js   Vendored Tailwind runtime (no CDN)
  static/css/app.css             Custom styles
e2e/
  conftest.py                    Hermetic Playwright harness (stubbed services)
  test_user_journey.py           Browser e2e: search, download, library, wishlist
tests/
  e2e_test.py                    43 end-to-end API tests
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
