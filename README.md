# Gamarr

A self-hosted game and ROM acquisition manager. Search multiple sources and download directly to your media server — PC game repacks, ROM files, and more.

Sister project to [Librarr](https://github.com/JeremiahM37/librarr) (book downloads).

---

## Educational Use Disclaimer

Gamarr is intended for **personal archival, preservation, and educational use** of games that you own or have the legal right to access. This tool is provided for legal use cases only.

Downloading copyrighted games without authorisation may violate copyright law in your jurisdiction. Users are solely responsible for ensuring their use complies with all applicable laws and licensing terms. **The developers do not condone piracy and accept no liability for misuse of this software.**

---

## Features

- **Search** — Prowlarr (torrent indexers) + Myrient (direct download, no signup) + Vimm's Lair
- **Plugin-based sources** — drop a Python file into `sources/` to add any new search source
- **Multi-platform** — PC games, PS1/PS2/PS3, GameCube, Wii, Nintendo DS/3DS/64/Switch, Xbox/360, and more
- **Safety scanning** — file extension checks + optional ClamAV antivirus on download
- **Persistent jobs** — download state survives container restarts (SQLite)
- **Custom DDL sources** — add your own direct-download sites via the UI
- **ROM routing** — auto-organise into platform slugs compatible with RomM
- **PC game routing** — send repacks to a vault folder for extraction/install

## Getting Started

```bash
# Clone
git clone https://github.com/your-username/gamarr.git
cd gamarr

# Copy and edit env
cp .env.example .env
# Fill in PROWLARR_URL, PROWLARR_API_KEY, QB_URL, QB_PASS

# Copy and edit compose file
cp docker-compose.example.yml docker-compose.yml
# Adjust volume paths to your setup

# Run
docker compose up -d
```

Web UI available at `http://localhost:5001` (or the mapped host port).

## Docker Compose

A fully documented example is in `docker-compose.example.yml`. The key points:

- Gamarr runs on port **5001** inside the container
- It needs access to qBittorrent (via a VPN container network or direct)
- Mount your game storage paths into the container
- A named volume (`gamarr_data`) stores the SQLite database and custom source config

### Running behind a VPN (gluetun)

Gamarr is designed to run inside a gluetun network namespace so all traffic exits through your VPN:

```yaml
network_mode: "service:gluetun"
```

When doing this, expose ports via the gluetun service, not the gamarr service directly.

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `PROWLARR_URL` | `http://prowlarr:9696` | Prowlarr base URL |
| `PROWLARR_API_KEY` | _(required)_ | Prowlarr API key |
| `QB_URL` | `http://qbittorrent:8080` | qBittorrent URL |
| `QB_USER` | `admin` | qBittorrent username |
| `QB_PASS` | _(required)_ | qBittorrent password |
| `QB_SAVE_PATH` | `/data/incoming/` | Where qBit saves game downloads |
| `QB_CATEGORY` | `games` | qBittorrent category for game downloads |
| `GAMES_VAULT_PATH` | `/data/vault` | PC game repack destination |
| `GAMES_ROMS_PATH` | `/data/roms` | ROM destination (sub-dirs per platform) |
| `DATA_DIR` | `/data/gamarr` | SQLite DB and custom source config location |
| `PROWLARR_GAME_INDEXERS` | `7,5,15,9,8,3,4` | Comma-separated Prowlarr indexer IDs |
| `CLAMAV_CONTAINER` | `clamav` | ClamAV container name (optional) |
| `CLAMAV_SOCKET` | `/run/clamav/clamd.sock` | ClamAV Unix socket path |
| `DOCKER_SOCKET` | `/var/run/docker.sock` | Docker socket for on-demand ClamAV |

See `.env.example` for a copy-paste template with explanatory comments.

## Sources

Gamarr uses a plugin-based source system. Each source is a Python file in the `sources/` directory that subclasses `GameSource`. Sources are auto-discovered at startup — no registration required.

### Built-in Sources

| Source | Type | Platforms | Notes |
|---|---|---|---|
| **Prowlarr** | Torrent | All | Requires Prowlarr + configured indexers |
| **Myrient** | DDL | Most retro consoles | Public site, no account needed |
| **Vimm's Lair** | DDL | Classic consoles | No account needed; downloads may fail anti-bot |

### Adding a Custom Source

Create `sources/my_source.py`:

```python
from sources.base import GameSource

class MySource(GameSource):
    name = "mysource"
    label = "My Source"
    color = "#e11d48"
    source_type = "ddl"   # or "torrent"

    def enabled(self):
        return bool(self.get_config("url"))  # reads SOURCE_MYSOURCE_URL env var

    def search(self, query, platform_slug=None):
        # ... your search logic ...
        return [
            {
                "title": "Game Title",
                "download_url": "https://example.com/game.zip",
                "size_human": "512 MB",
                "platform": "SNES",
                "platform_slug": "snes",
                "is_pc": False,
                "source_type": "ddl",
                "safety_score": 80,
                "safety_warnings": [],
            }
        ]
```

Restart the container and the source appears automatically in the UI and `/api/sources`.

## Supported Platforms

| Platform | Source(s) |
|---|---|
| PC | Prowlarr (FitGirl, DODI, GOG, etc.) |
| PlayStation 1/2/3 | Prowlarr + Myrient |
| GameCube / Wii | Prowlarr + Myrient |
| Nintendo DS / 3DS | Prowlarr + Myrient |
| Nintendo 64 / NES / SNES | Myrient + Prowlarr |
| Nintendo Switch | Prowlarr (Nyaa) |
| Xbox / Xbox 360 | Prowlarr + Myrient |
| GBA / Game Boy / GBC | Myrient |
| Sega Genesis / Saturn | Myrient |
| Dreamcast | Prowlarr + Myrient |
| Classic consoles (general) | Vimm's Lair |

## ClamAV (Optional)

Gamarr can run an on-demand ClamAV scan after every download. The ClamAV container is started only during scans and stopped immediately after (saves ~500 MB RAM at idle).

To enable:
1. Add a `clamav` service to your compose file (see `docker-compose.example.yml`)
2. Mount the Docker socket (`/var/run/docker.sock`) into the gamarr container
3. Set `CLAMAV_CONTAINER=clamav` in your `.env` if using a different container name

ClamAV is **optional** — if the Docker socket is not mounted, scanning is skipped silently.

## Persistent Jobs

Download job state is stored in a SQLite database at `DATA_DIR/gamarr.db`. Jobs survive container restarts — any jobs that were `downloading`, `scanning`, or `organizing` at restart are marked `interrupted` so you can see what was in progress.

Jobs older than 30 days are cleaned up automatically on next startup.

## API

All endpoints return JSON.

| Method | Path | Description |
|---|---|---|
| GET | `/api/search?q=&platform=` | Search all enabled sources |
| GET | `/api/platforms` | List supported platforms |
| GET | `/api/sources` | List all sources and their status |
| POST | `/api/download` | Start a download |
| GET | `/api/downloads` | List active/recent downloads |
| DELETE | `/api/downloads/<id>` | Cancel/remove a job |
| DELETE | `/api/downloads/torrent/<hash>` | Remove a torrent from qBittorrent |
| POST | `/api/downloads/organize/<hash>` | Manually organize a completed torrent |
| POST | `/api/downloads/clear` | Clear all completed/errored jobs |
| GET/POST/DELETE | `/api/ddl-sources` | Manage custom DDL sources |

## License

MIT
