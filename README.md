# Gamarr

A self-hosted game and ROM acquisition manager. Search multiple sources and download directly to your media server — PC game repacks, ROM files, and more.

Sister project to [Librarr](https://github.com/JeremiahM37/librarr) (book downloads).

## Features

- **Search** — Prowlarr (torrent indexers) + Myrient (direct download, no signup)
- **Multi-platform** — PC games, PS1/PS2/PS3, GameCube, Wii, Nintendo DS/3DS/64/Switch, Xbox/360, and more
- **Safety scanning** — file extension checks + optional ClamAV antivirus on download
- **Custom DDL sources** — add your own direct-download sites via the UI
- **ROM routing** — auto-organise into platform slugs compatible with RoMM
- **PC game routing** — send repacks to a vault folder for extraction/install

## Quick Start

```bash
# Clone
git clone https://github.com/your-username/gamarr.git
cd gamarr

# Copy and edit env
cp .env.example .env
# Fill in PROWLARR_URL, PROWLARR_API_KEY, QB_URL, QB_PASS

# Run with Docker Compose
docker compose up -d
```

Web UI available at `http://localhost:5001` (or the mapped host port).

## Docker Compose

A minimal example is in `docker-compose.example.yml`. The key points:

- Gamarr runs on port **5001** inside the container
- It needs access to qBittorrent (via a VPN container network or direct)
- Mount your game storage paths into the container

### Running behind a VPN (gluetun)

Gamarr is designed to run inside a gluetun network namespace so all traffic exits through your VPN:

```yaml
network_mode: "service:gluetun"
```

When doing this, expose the port via the gluetun service, not gamarr directly.

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `PROWLARR_URL` | `http://prowlarr:9696` | Prowlarr base URL |
| `PROWLARR_API_KEY` | _(required)_ | Prowlarr API key |
| `QB_URL` | `http://localhost:8080` | qBittorrent URL |
| `QB_USER` | `admin` | qBittorrent username |
| `QB_PASS` | _(required)_ | qBittorrent password |
| `QB_SAVE_PATH` | `/games-incoming/` | Where qBit saves game downloads |
| `GAMES_VAULT_PATH` | `/vault` | PC game repack destination |
| `GAMES_ROMS_PATH` | `/roms` | ROM destination (sub-dirs per platform) |

See `.env.example` for a copy-paste template.

## Supported Platforms

| Platform | Source(s) |
|---|---|
| PC | Prowlarr (FitGirl, DODI, GOG, etc.) |
| PlayStation 1/2/3 | Prowlarr + Myrient |
| GameCube / Wii | Prowlarr + Myrient |
| Nintendo DS / 3DS | Prowlarr + Myrient |
| Nintendo 64 / NES / SNES | Myrient |
| Nintendo Switch | Prowlarr (Nyaa) |
| Xbox / Xbox 360 | Prowlarr + Myrient |
| GBA / Game Boy | Myrient |
| Sega Genesis / Saturn | Myrient |
| PS Vita / Wii U | Myrient |

## ClamAV (Optional)

Gamarr can run an on-demand ClamAV scan after every download. The ClamAV container is started only during scans and stopped immediately after (saves ~500 MB RAM).

To enable:
1. Add a `clamav` service to your compose file
2. Mount the Docker socket (`/var/run/docker.sock`) into the gamarr container

ClamAV is **optional** — if the Docker socket is not mounted, scanning is skipped silently.

## API

All endpoints return JSON.

| Method | Path | Description |
|---|---|---|
| GET | `/api/search?q=&platform=` | Search all sources |
| GET | `/api/platforms` | List supported platforms |
| POST | `/api/download` | Start a download |
| GET | `/api/downloads` | List active/recent downloads |
| GET | `/api/downloads/<id>` | Download status |
| DELETE | `/api/downloads/<id>` | Cancel/remove download |
| GET/POST/DELETE | `/api/ddl-sources` | Manage custom DDL sources |

## License

MIT
