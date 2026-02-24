# Gamarr — Claude Context

Gamarr is a self-hosted game/ROM acquisition app (Flask/Python) that searches multiple sources and downloads games to a home media server. Sister project to Librarr (books). Currently private/homelab-only but intended to be open-sourced.

---

## What It Does

Search and download:
- **PC games** (torrents): via Prowlarr, filtered to reputable release groups (FitGirl, DODI, CODEX, etc.)
- **ROMs** (all console platforms): via Prowlarr torrents + Myrient direct download
- **Safety scanning**: file extension checks + on-demand ClamAV scan before import

After download, routes to:
- **RoMM** — ROM manager, organized by platform slug
- **GameVault** — PC game library server
- **Bazzite VM** — final install destination (via game-sync.sh rsync)

---

## Architecture

### Stack
- **Python 3 / Flask** — single `app.py` + `config.py`
- **qBittorrent** — all torrent downloads, `QBittorrentClient` class (same pattern as Librarr)
- **Myrient** — direct HTTP download (Apache directory listing scraper), no auth needed
- **Vimm** — attempted DDL, blocked by anti-bot JS (don't use — use Myrient instead)
- **ClamAV** — on-demand virus scanning via Docker socket (container stopped when idle to save RAM)
- **Docker socket** — `/var/run/docker.sock` — for starting/stopping ClamAV container
- In-memory `download_jobs` dict + background worker thread

### Deployment Context (homelab)
- Runs inside **LXC 200** on MediaServer (153.90.84.207), Docker Compose at `/opt/docker/`
- Container name: `gamarr`
- Port: 5057 on host (5001 inside gluetun network) — `network_mode: service:gluetun`
- Like Librarr, routes through **gluetun** Mullvad VPN
- API from host: `curl http://localhost:5057/...`
- API from inside gluetun: `docker exec gluetun wget -qO- http://localhost:5001/...`

### Key Paths (inside LXC 200)
```
/mnt/storage/games/vault/          → PC game repacks (→ rsync to Bazzite)
/mnt/storage/games/roms/{platform}/ → ROM files (→ RoMM)
/data/media/games/                 → symlink into DAS
```

---

## Source Layout

```
app.py          Main Flask app (~1758 lines)
config.py       Config (env-overridable)
ddl_sources.json  User-added custom DDL sources (CRUD)
Dockerfile      Multi-stage build
clamd.conf      ClamAV client config (socket path)
requirements.txt
```

---

## Platform Mapping

Prowlarr category ID → `(display_name, romm_slug, is_pc)`:

| Category | Platform | RoMM slug |
|---|---|---|
| 4000, 100010 | PC | — |
| 100011 | PS2 | ps2 |
| 100012 | PSP | psp |
| 100015 | PS1 | psx |
| 100043 | PS3 | ps3 |
| 100044 | Wii | wii |
| 100045 | DS | nds |
| 100046 | GameCube | ngc |
| 100072 | 3DS | 3ds |
| 100077 | PS4 | ps4 |
| 100082 | Switch | switch |
| 100013/14 | Xbox/360 | xbox/xbox360 |

Extra platforms (no Prowlarr category, DDL-only): `n64`, `snes`, `nes`, `gb`, `gba`, `genesis`, `saturn`, `wiiu`, `psvita`

---

## API Endpoints

All JSON. Base URL: `http://localhost:5057` (from host) or `http://localhost:5001` (inside gluetun).

### Search
```
GET /api/search?q=<query>&platform=<slug>
```
- Searches Prowlarr (torrent) + Myrient (DDL) for the given platform
- Results include safety score, warnings, release group detection

### Platforms
```
GET /api/platforms
```
Returns all supported platform slugs and display names.

### Download
```
POST /api/download
Body: { "url": "...", "title": "...", "platform": "...", "source": "torrent|myrient|ddl" }
```
- `torrent`: Prowlarr download URL or magnet → qBit
- `myrient`: Myrient direct download URL → HTTP download worker
- `ddl`: Custom DDL source URL → HTTP download worker

Returns `{ "job_id": "..." }`.

### Downloads
```
GET    /api/downloads              → list all active/recent jobs
GET    /api/downloads/<job_id>     → status of one job
DELETE /api/downloads/<job_id>     → cancel/delete
DELETE /api/downloads              → clear all
POST   /api/downloads/<job_id>/organize → manually trigger file organization
```

### DDL Sources (custom user-added download sites)
```
GET    /api/ddl-sources            → list custom DDL sources
POST   /api/ddl-sources            → add new source { name, url, platform }
DELETE /api/ddl-sources/<id>       → remove source
```

---

## Download Flows

### Torrent (Prowlarr)
1. `POST /api/download` with Prowlarr URL/magnet
2. `QBittorrentClient.add_torrent()` → qBit with `games` category
3. File extension safety scan on torrent file list (before download completes)
4. On completion: ClamAV scan → organize to RoMM/vault path

### Myrient Direct Download
1. `GET /api/search` triggers `search_myrient(query, platform)`
2. Fetches Apache directory listing for the platform path, fuzzy-matches filename
3. `POST /api/download` with direct `.zip`/`.iso` URL
4. Worker streams download with progress tracking
5. After download: ClamAV scan → extract if archive → move to ROM directory

**Myrient platform paths:**
```python
MYRIENT_BASE = "https://myrient.erista.me/files/"

"nes":    "No-Intro/Nintendo - Nintendo Entertainment System (Headered)/"
"n64":    "No-Intro/Nintendo - Nintendo 64 (BigEndian)/"
"nds":    "No-Intro/Nintendo - Nintendo DS (Decrypted)/"
"3ds":    "No-Intro/Nintendo - Nintendo 3DS (Decrypted)/"
"psx":    "Redump/Sony - PlayStation/"
"ps2":    "Redump/Sony - PlayStation 2/"
"ngc":    "Redump/Nintendo - GameCube - NKit RVZ [zstd-19-128k]/"
"wii":    "Redump/Nintendo - Wii - NKit RVZ [zstd-19-128k]/"
```

**Note**: Myrient has full ROM sets. Filename matching uses fuzzy search (normalized title, ignore region/version tags).

### Vimm's Lair (AVOID)
Vimm has a JS-based anti-bot system (`blocked_*` variable + fuse.js). The form action is `//dl2.vimm.net/` with method POST. **Cannot be bypassed without real JS execution** — always returns 400 or empty. Use Myrient instead.

---

## Safety Scanning

### File Extension Check (pre-download)
`scan_torrent_file_list(torrent_hash)` — checks qBit file list for:
- Dangerous extensions: `.scr`, `.bat`, `.cmd`, `.vbs`, `.ps1`, `.hta`, `.reg`, etc.
- Double extensions: `game.pdf.exe`
- "All executables" heuristic: torrent with only `.exe`/`.dll` files is suspicious

### Safety Score
`_safety_score(result)` rates each search result 0-100:
- **+30** if seeders ≥ 100
- **+20** if trusted release group (FitGirl, DODI, GOG, CODEX, PLAZA, SKIDROW, etc.)
- **-30** if size < 10MB (suspiciously small)
- **-40** if dangerous extension in title
- **-20** if very few seeders

### ClamAV (post-download)
`scan_with_clamav(path)` — on-demand container lifecycle:
1. Start ClamAV container via Docker socket `POST /containers/clamav/start`
2. Wait up to 150s for `/run/clamav/clamd.sock` to appear (ClamAV DB loading)
3. Run `clamdscan --stream -r <path>`
4. **Stop ClamAV container** after scan to free ~500MB RAM

ClamAV container is not auto-started — only runs during scan. Docker socket at `/var/run/docker.sock` must be mounted into gamarr container.

---

## Result Filtering

```python
# Non-English detection
_is_non_english(title) → True if Cyrillic/CJK chars, or non-English region tags

# Exceptions: FitGirl Repack is fine even if flagged (multi-language)
# Exceptions: USA / EN / ENG / English in title = fine

# Suspicious pattern check
_SUSPICIOUS = re.compile("password|keygen|crack only|warez|DevCourseWeb|...")
```

Default behavior: filter non-English, filter suspicious titles. Both can be toggled.

---

## qBittorrent Auth Pattern

Same as Librarr — session-based, cookie jar, re-auth on 403.

qBit runs inside gluetun network namespace — only accessible from other containers on the same Docker network.

**Orphan recovery** (on startup): scans existing qBit torrents with `games` category, re-links them to in-memory `download_jobs` so they don't get lost on container restart.

---

## Game Pipeline (Bazzite Integration)

After gamarr downloads a game to vault:
1. `game-sync.sh` runs every 15 min on Bazzite VM (systemd timer) — rsync from `/mnt/storage/games/vault/` to `~/Games/vault/`
2. Auto-extract: FitGirl repacks (`setup.exe` + `fg-*.bin`), FreeArc (`Setup.exe` + `.dxn`/`.ftp`), NSIS (`data0.bin`)
3. Auto-install via `umu-run` (Proton/Wine)
4. `steam-rom-import.py` adds to Steam library via `shortcuts.vdf`

---

## Known Issues / Gotchas

- **Prowlarr download URLs use `localhost:9696`** internally — when calling from the host (outside Docker), replace with `prowlarr:9696` (container hostname) or use `153.90.84.222:9696`
- **qBit PUID/PGID=1000** — game download directories must be owned by `1000:1000`
- **Container restarts kill in-progress downloads** — job state is in-memory only
- **stoppedUP state in qBit 5.x** = completed seeding — gamarr maps this to "completed" in its `status_map`
- **Myrient directory caching** — 1 hour TTL; first search per platform is slow (fetches full dir listing)
- **ClamAV takes 2-3 minutes to start** (DB loading) — expected, don't assume it's broken
- **Vimm anti-bot** is unbypassable without JS — don't waste time on it
- **LimeTorrents seeder counts** are inflated/fake — verify on TPB or check actual connected peers in qBit

---

## Development Setup (for local work in project-env)

```bash
cd /projects/gamarr
python3 -m venv venv
source venv/bin/activate
pip install flask requests

# Override config for local testing
export PROWLARR_URL=http://153.90.84.222:9696
export PROWLARR_API_KEY=e9dd950af1c643719493d0c6aa6f009f
export QB_URL=http://localhost:8080  # won't work without gluetun

python3 app.py  # runs on port 5001
```

Most search functions (Prowlarr, Myrient scraping) work without qBit. ClamAV and Docker socket features are optional.

---

## Open-Source Cleanup Checklist

- [ ] Remove hardcoded API keys and credentials from `config.py`
- [ ] Remove homelab-specific IP addresses
- [ ] Add README with installation instructions
- [ ] Add `docker-compose.example.yml`
- [ ] Document all env variables
- [ ] Add `.env.example`
- [ ] Decide on ClamAV — optional feature or required? Document setup
- [ ] Add LICENSE
- [ ] Review `ddl_sources.json` for any sensitive URLs before publishing
- [ ] Consider moving platform map to a separate data file for easier community contribution

---

## Related Projects

- **Librarr** (`/projects/librarr/`) — same pattern but for books. Nearly identical `QBittorrentClient` class, same Flask structure.
- **Homelab docker-compose**: `/opt/docker/docker-compose.yml` on LXC 200 (153.90.84.222)
- **Rebuild command** (homelab):
  ```bash
  ssh root@153.90.84.207 'pct exec 200 -- bash -c "cd /opt/docker && docker compose build gamarr && docker compose up -d gamarr"'
  ```
- **RoMM** at `http://153.90.84.222:8086` — ROM library manager
- **GameVault** at `http://153.90.84.222:8087` — PC game library
- **Bazzite VM** at `bazzite@100.66.185.87` — gaming VM where games actually run
