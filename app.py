import logging
import os
import re
import shutil
import sys
import threading
import time
import uuid

import requests
import urllib3
urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
from flask import Flask, jsonify, request, send_file

import config

app = Flask(__name__)
logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s", stream=sys.stderr)
logger = logging.getLogger("gamarr")

# In-memory download job tracker
download_jobs = {}


# =============================================================================
# Platform Mapping
# =============================================================================
# Prowlarr category ID -> (display_name, romm_slug, is_pc)
PLATFORM_MAP = {
    4000:   ("PC",        None,       True),
    100010: ("PC",        None,       True),
    100011: ("PS2",       "ps2",      False),
    100012: ("PSP",       "psp",      False),
    100013: ("Xbox",      "xbox",     False),
    100014: ("Xbox 360",  "xbox360",  False),
    100015: ("PS1",       "psx",      False),
    100016: ("Dreamcast", "dc",       False),
    100017: ("Other",     None,       False),
    100043: ("PS3",       "ps3",      False),
    100044: ("Wii",       "wii",      False),
    100045: ("DS",        "nds",      False),
    100046: ("GameCube",  "ngc",      False),
    100072: ("3DS",       "3ds",      False),
    100077: ("PS4",       "ps4",      False),
    100082: ("Switch",    "switch",   False),
    4050:   ("Switch",    "switch",   False),  # Nyaa Switch category
}

# Extra platforms for user override (not in Prowlarr categories)
EXTRA_PLATFORMS = [
    ("n64",     "Nintendo 64"),
    ("snes",    "SNES"),
    ("nes",     "NES"),
    ("gb",      "Game Boy"),
    ("gba",     "Game Boy Advance"),
    ("genesis", "Sega Genesis"),
    ("saturn",  "Sega Saturn"),
    ("wiiu",    "Wii U"),
    ("psvita",  "PS Vita"),
]

ALL_GAME_CATEGORIES = list(PLATFORM_MAP.keys())


def detect_platform(categories):
    for cat in categories:
        cat_id = cat.get("id") if isinstance(cat, dict) else cat
        if cat_id in PLATFORM_MAP:
            name, slug, is_pc = PLATFORM_MAP[cat_id]
            return {"name": name, "slug": slug, "is_pc": is_pc}
    return {"name": "Unknown", "slug": None, "is_pc": False}


def get_categories_for_platform(platform_filter):
    if platform_filter == "pc":
        return [4000, 100010]
    matches = [cat_id for cat_id, (name, slug, is_pc) in PLATFORM_MAP.items()
               if slug == platform_filter or str(cat_id) == platform_filter]
    if matches:
        return matches
    return ALL_GAME_CATEGORIES


# =============================================================================
# qBittorrent Client (adapted from BookBounty)
# =============================================================================
class QBittorrentClient:
    def __init__(self):
        self.session = requests.Session()
        self.authenticated = False

    def login(self):
        try:
            resp = self.session.post(
                f"{config.QB_URL}/api/v2/auth/login",
                data={"username": config.QB_USER, "password": config.QB_PASS},
                timeout=10,
            )
            self.authenticated = resp.text == "Ok."
            return self.authenticated
        except Exception as e:
            logger.error(f"qBittorrent login failed: {e}")
            return False

    def _ensure_auth(self):
        if not self.authenticated:
            self.login()

    def add_torrent(self, url, title="", save_path=None, category=None):
        self._ensure_auth()
        data = {
            "urls": url,
            "savepath": save_path or config.QB_SAVE_PATH,
            "category": category or config.QB_CATEGORY,
        }
        try:
            resp = self.session.post(
                f"{config.QB_URL}/api/v2/torrents/add", data=data, timeout=15
            )
            if resp.status_code == 403:
                self.login()
                resp = self.session.post(
                    f"{config.QB_URL}/api/v2/torrents/add", data=data, timeout=15
                )
            return resp.text == "Ok."
        except Exception as e:
            logger.error(f"qBittorrent add torrent failed: {e}")
            return False

    def get_torrents(self, category=None):
        self._ensure_auth()
        try:
            params = {}
            if category:
                params["category"] = category
            resp = self.session.get(
                f"{config.QB_URL}/api/v2/torrents/info", params=params, timeout=10
            )
            if resp.status_code == 403:
                self.login()
                resp = self.session.get(
                    f"{config.QB_URL}/api/v2/torrents/info", params=params, timeout=10
                )
            return resp.json()
        except Exception:
            return []

    def get_torrent_files(self, torrent_hash):
        self._ensure_auth()
        try:
            resp = self.session.get(
                f"{config.QB_URL}/api/v2/torrents/files",
                params={"hash": torrent_hash},
                timeout=10,
            )
            if resp.status_code == 403:
                self.login()
                resp = self.session.get(
                    f"{config.QB_URL}/api/v2/torrents/files",
                    params={"hash": torrent_hash},
                    timeout=10,
                )
            return resp.json() if resp.status_code == 200 else []
        except Exception:
            return []

    def delete_torrent(self, torrent_hash, delete_files=True):
        self._ensure_auth()
        try:
            resp = self.session.post(
                f"{config.QB_URL}/api/v2/torrents/delete",
                data={
                    "hashes": torrent_hash,
                    "deleteFiles": str(delete_files).lower(),
                },
                timeout=10,
            )
            if resp.status_code == 403:
                self.login()
                resp = self.session.post(
                    f"{config.QB_URL}/api/v2/torrents/delete",
                    data={
                        "hashes": torrent_hash,
                        "deleteFiles": str(delete_files).lower(),
                    },
                    timeout=10,
                )
            return resp.status_code == 200
        except Exception:
            return False


qb = QBittorrentClient()


# =============================================================================
# File Safety Scanning
# =============================================================================
# Extensions that are NEVER expected in game downloads
_DANGEROUS_EXTENSIONS = {
    ".scr", ".bat", ".cmd", ".vbs", ".vbe", ".js", ".jse",
    ".wsf", ".wsh", ".ps1", ".hta", ".cpl", ".reg", ".pif",
    ".com", ".msp", ".mst", ".inf",
}

# Extensions that are expected in game downloads
_SAFE_GAME_EXTENSIONS = {
    # Archives
    ".zip", ".rar", ".7z", ".tar", ".gz", ".xz", ".bz2",
    # Disc images
    ".iso", ".img", ".bin", ".cue", ".mdf", ".mds", ".nrg", ".cdi",
    # ROM files
    ".nes", ".snes", ".smc", ".sfc", ".gb", ".gbc", ".gba",
    ".nds", ".3ds", ".n64", ".z64", ".v64", ".gcm", ".gcz",
    ".wbfs", ".wad", ".nsp", ".xci", ".cia", ".pbp", ".cso",
    ".chd", ".gdi",
    # PC game files
    ".exe", ".dll", ".msi",
    # Data / media
    ".pak", ".dat", ".cfg", ".ini", ".txt", ".nfo", ".md",
    ".jpg", ".jpeg", ".png", ".bmp", ".ico", ".svg",
    ".mp3", ".ogg", ".wav", ".flac", ".mp4", ".avi", ".mkv",
    ".pdf", ".xml", ".json", ".yaml", ".yml", ".lua", ".py",
    ".so", ".dylib",
}


def scan_torrent_file_list(torrent_hash):
    """Check the torrent's file list for dangerous file extensions.
    Returns (is_safe, issues) where issues is a list of warning strings."""
    files = qb.get_torrent_files(torrent_hash)
    if not files:
        return True, []  # Can't check, assume OK (metadata may not be ready)

    issues = []
    dangerous_files = []
    exe_count = 0
    total_files = len(files)

    for f in files:
        name = f.get("name", "").lower()
        ext = os.path.splitext(name)[1]

        # Check for dangerous extensions
        if ext in _DANGEROUS_EXTENSIONS:
            dangerous_files.append(name)

        # Count .exe files
        if ext == ".exe":
            exe_count += 1

        # Double extensions (e.g., game.pdf.exe)
        # Use basename only — directory names with version numbers (v1.3.4)
        # create false positives when splitting the full path by dots
        basename = os.path.basename(name)
        parts = basename.rsplit(".", 2)
        if len(parts) >= 3:
            double_ext = "." + parts[-1]
            if double_ext in (".exe", ".scr", ".bat", ".cmd", ".msi"):
                issues.append(f"Double extension: {basename}")

    if dangerous_files:
        issues.append(f"Dangerous files found: {', '.join(os.path.basename(f) for f in dangerous_files[:5])}")

    # Heuristic: a ROM download with .exe files is suspicious
    # (ROMs should never contain executables)
    if exe_count > 0 and total_files <= 3 and any(
        os.path.splitext(f.get("name", ""))[1].lower() in {".exe", ".scr"}
        for f in files
    ):
        # Check if ALL files are executables (very suspicious for a "game" torrent)
        non_exe = sum(1 for f in files if os.path.splitext(f.get("name", ""))[1].lower() not in {".exe", ".dll", ".msi"})
        if non_exe == 0 and total_files <= 2:
            issues.append("Torrent contains only executables - likely malware")

    is_safe = len(issues) == 0
    return is_safe, issues


CLAMD_SOCKET = "/run/clamav/clamd.sock"
DOCKER_SOCKET = "/var/run/docker.sock"


def _docker_api(method, path, **kwargs):
    """Call Docker Engine API via Unix socket."""
    import http.client
    conn = http.client.HTTPConnection("localhost")
    conn.sock = __import__("socket").socket(__import__("socket").AF_UNIX, __import__("socket").SOCK_STREAM)
    conn.sock.connect(DOCKER_SOCKET)
    conn.request(method, path, **kwargs)
    resp = conn.getresponse()
    data = resp.read()
    conn.close()
    return resp.status, data


def _start_clamav():
    """Start ClamAV container and wait for socket to be ready."""
    if os.path.exists(CLAMD_SOCKET):
        return True  # Already running

    if not os.path.exists(DOCKER_SOCKET):
        logger.warning("Docker socket not available, cannot start ClamAV")
        return False

    try:
        status, _ = _docker_api("POST", "/containers/clamav/start")
        if status not in (204, 304):  # 204=started, 304=already running
            logger.warning(f"Failed to start ClamAV container: HTTP {status}")
            return False
        logger.info("Starting ClamAV container, waiting for socket...")
        for _ in range(150):  # Wait up to ~150s for DB to load
            if os.path.exists(CLAMD_SOCKET):
                logger.info("ClamAV socket ready")
                return True
            time.sleep(1)
        logger.warning("ClamAV socket never appeared after start")
        return False
    except Exception as e:
        logger.warning(f"Failed to start ClamAV: {e}")
        return False


def _stop_clamav():
    """Stop ClamAV container to free RAM."""
    try:
        _docker_api("POST", "/containers/clamav/stop")
        logger.info("ClamAV container stopped")
    except Exception as e:
        logger.warning(f"Failed to stop ClamAV: {e}")


def scan_with_clamav(path):
    """Start ClamAV on demand, scan, then stop it. Returns (is_clean, details)."""
    import subprocess

    if not _start_clamav():
        logger.warning("ClamAV not available, skipping virus scan")
        return True, []

    try:
        result = subprocess.run(
            ["clamdscan", "--config-file=/app/clamd.conf", "--no-summary", "--infected", "--stream", "-r", path],
            capture_output=True, text=True, timeout=600,
        )
        infected_lines = [l.strip() for l in result.stdout.strip().split("\n") if "FOUND" in l]
        if infected_lines:
            return False, infected_lines
        return True, []
    except FileNotFoundError:
        logger.warning("clamdscan not installed, skipping virus scan")
        return True, []
    except subprocess.TimeoutExpired:
        logger.warning("ClamAV scan timed out (10min)")
        return True, []
    except Exception as e:
        logger.warning(f"ClamAV scan error: {e}")
        return True, []
    finally:
        _stop_clamav()


# =============================================================================
# Utility Functions
# =============================================================================
def _human_size(size_bytes):
    if not size_bytes:
        return "?"
    for unit in ("B", "KB", "MB", "GB"):
        if abs(size_bytes) < 1024:
            return f"{size_bytes:.1f} {unit}"
        size_bytes /= 1024
    return f"{size_bytes:.1f} TB"


_STOPWORDS = {"the", "a", "an", "of", "in", "on", "at", "to", "for", "and", "or", "is", "it", "by"}


def _title_relevant(query, title):
    q_words = set(re.findall(r"\w+", query.lower())) - _STOPWORDS
    t_words = set(re.findall(r"\w+", title.lower())) - _STOPWORDS
    if not q_words or not t_words:
        return False
    return len(q_words & t_words) >= 1


_SUSPICIOUS = re.compile(
    r"password|keygen|crack[\s._-]+only|warez|DevCourseWeb|sample\s+only|trailer\s+only"
    r"|\.exe\.torrent|password\.txt|clickbait|survey|free\s+download"
    r"|RAT\b|trojan|malware|crypto[\s._-]*miner|coin[\s._-]*miner",
    re.IGNORECASE,
)

# Non-English content detection
_CYRILLIC = re.compile(r"[\u0400-\u04FF]")
_CJK = re.compile(r"[\u4E00-\u9FFF\u3040-\u309F\u30A0-\u30FF]")
_NON_ENGLISH_TAGS = re.compile(
    r"\b(RUS|RUSSIAN|RU|MULTI\d*RUS|RePack[\s._-]+by[\s._-]+R\.G|R\.G\.|"
    r"Repack[\s._-]+by[\s._-]+xatab|xatab|decepticon|механики|FitGirl[\s._-]+Repack"
    r"|KaOs|FRENCH|GERMAN|ITALIAN|SPANISH|PORTUGUESE|POLISH|CZECH|HUNGARIAN"
    r"|KOREAN|JAPANESE|CHINESE|ARABIC|TURKISH|THAI|VIETNAMESE"
    r"|JPN|KOR|CHN|FRA|DEU|ITA|ESP|POR|POL|CZE)\b",
    re.IGNORECASE,
)

def _is_non_english(title):
    """Check if a torrent title appears to be non-English."""
    if _CYRILLIC.search(title) or _CJK.search(title):
        return True
    # Allow "Multi" if it includes English, but flag pure non-English
    if _NON_ENGLISH_TAGS.search(title):
        # Exception: FitGirl repacks are fine (English with multi-lang options)
        if re.search(r"fitgirl", title, re.IGNORECASE):
            return False
        # Exception: if title contains (USA) or (EN) or (English), it's fine
        if re.search(r"\b(USA|EN|ENG|English|NTSC|PAL)\b", title, re.IGNORECASE):
            return False
        return True
    return False

# Suspicious file extensions that shouldn't be in game torrents
_RISKY_EXTENSIONS = re.compile(
    r"\.(scr|bat|cmd|vbs|vbe|js|jse|wsf|wsh|ps1|msi|hta|cpl|reg)$",
    re.IGNORECASE,
)

# Known reputable release groups for games
_TRUSTED_GROUPS = {
    "fitgirl", "dodi", "gog", "codex", "plaza", "skidrow", "cpy",
    "empress", "rune", "razordox", "tinyiso", "elamigos",
}

def _safety_score(result):
    """Rate result safety: higher = safer. Returns (score, warnings)."""
    score = 50
    warnings = []
    title = result.get("title", "").lower()
    seeders = result.get("seeders", 0)
    size = result.get("size", 0)

    # Seeder count is the best safety signal
    if seeders >= 100:
        score += 30
    elif seeders >= 20:
        score += 15
    elif seeders >= 5:
        score += 5
    elif seeders <= 1:
        score -= 20
        warnings.append("Very few seeders")

    # Trusted release groups
    for group in _TRUSTED_GROUPS:
        if group in title:
            score += 20
            break

    # Size sanity check - suspiciously small "games"
    if size and size < 10_000_000:  # < 10MB
        score -= 30
        warnings.append("Suspiciously small file")

    # Risky filename patterns
    if _RISKY_EXTENSIONS.search(title):
        score -= 40
        warnings.append("Risky file extension")

    # Multiple file extensions (e.g., game.iso.exe)
    if re.search(r"\.\w{2,4}\.\w{2,4}$", title):
        double_ext = re.search(r"\.(\w{2,4})\.(\w{2,4})$", title)
        if double_ext:
            ext2 = double_ext.group(2).lower()
            if ext2 in ("exe", "scr", "bat", "cmd", "msi"):
                score -= 40
                warnings.append("Double file extension")

    # Age - very new uploads with few seeds are riskier
    age = result.get("age", 0)
    if age < 1 and seeders < 5:
        score -= 10
        warnings.append("Brand new upload")

    return max(0, min(100, score)), warnings


# =============================================================================
# Direct Download Sources
# =============================================================================
import json
from html.parser import HTMLParser
from urllib.parse import unquote, urljoin, quote

DDL_SOURCES_FILE = os.getenv("DDL_SOURCES_FILE", "/app/ddl_sources.json")

# Myrient platform directory mapping
MYRIENT_BASE = "https://myrient.erista.me/files/"
MYRIENT_PLATFORMS = {
    "gba": "No-Intro/Nintendo - Game Boy Advance/",
    "gb": "No-Intro/Nintendo - Game Boy/",
    "gbc": "No-Intro/Nintendo - Game Boy Color/",
    "nes": "No-Intro/Nintendo - Nintendo Entertainment System (Headered)/",
    "snes": "No-Intro/Nintendo - Super Nintendo Entertainment System/",
    "n64": "No-Intro/Nintendo - Nintendo 64 (BigEndian)/",
    "nds": "No-Intro/Nintendo - Nintendo DS (Decrypted)/",
    "3ds": "No-Intro/Nintendo - Nintendo 3DS (Decrypted)/",
    "psx": "Redump/Sony - PlayStation/",
    "ps2": "Redump/Sony - PlayStation 2/",
    "ps3": "Redump/Sony - PlayStation 3/",
    "psp": "Redump/Sony - PlayStation Portable/",
    "dc": "Redump/Sega - Dreamcast/",
    "saturn": "Redump/Sega - Saturn/",
    "genesis": "No-Intro/Sega - Mega Drive - Genesis/",
    "ngc": "Redump/Nintendo - GameCube - NKit RVZ [zstd-19-128k]/",
    "wii": "Redump/Nintendo - Wii - NKit RVZ [zstd-19-128k]/",
    "xbox": "Redump/Microsoft - Xbox/",
    "xbox360": "Redump/Microsoft - Xbox 360/",
}

# Directory listing cache (platform_slug -> [(filename, url)])
_dir_cache = {}
_dir_cache_time = {}
DIR_CACHE_TTL = 3600  # 1 hour


class DirListingParser(HTMLParser):
    """Parse Apache/nginx directory listing for file links."""
    def __init__(self):
        super().__init__()
        self.files = []
        self._in_a = False
        self._href = None

    def handle_starttag(self, tag, attrs):
        if tag == "a":
            for name, val in attrs:
                if name == "href" and val and not val.startswith("?") and not val.startswith("/") and val != "../":
                    self._href = val
                    self._in_a = True

    def handle_data(self, data):
        if self._in_a and self._href:
            self.files.append((data.strip(), self._href))
            self._in_a = False
            self._href = None

    def handle_endtag(self, tag):
        if tag == "a":
            self._in_a = False
            self._href = None


def _get_myrient_listing(platform_slug):
    """Fetch and cache directory listing for a Myrient platform."""
    now = time.time()
    if platform_slug in _dir_cache and now - _dir_cache_time.get(platform_slug, 0) < DIR_CACHE_TTL:
        return _dir_cache[platform_slug]

    path = MYRIENT_PLATFORMS.get(platform_slug)
    if not path:
        return []

    url = MYRIENT_BASE + path
    try:
        resp = requests.get(url, timeout=30, headers={"User-Agent": "Gamarr/1.0"})
        if resp.status_code != 200:
            logger.warning(f"Myrient listing failed for {platform_slug}: HTTP {resp.status_code}")
            return []
        parser = DirListingParser()
        parser.feed(resp.text)
        files = [(unquote(name), urljoin(url, href)) for name, href in parser.files if not name.endswith("/")]
        _dir_cache[platform_slug] = files
        _dir_cache_time[platform_slug] = now
        logger.info(f"Myrient: cached {len(files)} files for {platform_slug}")
        return files
    except Exception as e:
        logger.warning(f"Myrient listing error for {platform_slug}: {e}")
        return []


def search_myrient(query, platform_slug=None):
    """Search Myrient for a game across one or all platforms."""
    results = []
    q_words = set(re.findall(r"\w+", query.lower())) - _STOPWORDS

    if platform_slug and platform_slug not in MYRIENT_PLATFORMS:
        return []  # Platform not supported by Myrient
    slugs = [platform_slug] if platform_slug else list(MYRIENT_PLATFORMS.keys())
    # Limit to 3 platforms if searching all (too slow otherwise)
    if len(slugs) > 3 and not platform_slug:
        return []  # Require platform filter for Myrient

    for slug in slugs:
        files = _get_myrient_listing(slug)
        for filename, url in files:
            f_words = set(re.findall(r"\w+", filename.lower())) - _STOPWORDS
            overlap = len(q_words & f_words)
            if overlap < max(1, len(q_words) - 1) or overlap < 1:
                continue
            # Skip non-English regions
            if re.search(r"\(Japan\)|\(Korea\)|\(China\)|\(Taiwan\)|\(France\)|\(Germany\)|\(Spain\)|\(Italy\)", filename) and not re.search(r"\(USA|World|Europe|En\)", filename):
                continue
            plat_name, _, is_pc = PLATFORM_MAP.get(
                next((k for k, v in PLATFORM_MAP.items() if v[1] == slug), 0),
                ("Unknown", slug, False)
            )
            results.append({
                "title": filename,
                "size": 0,
                "size_human": "?",
                "seeders": 0,
                "leechers": 0,
                "indexer": "Myrient",
                "download_url": url,
                "magnet_url": "",
                "info_hash": "",
                "guid": url,
                "platform": plat_name,
                "platform_slug": slug,
                "is_pc": is_pc,
                "age": 0,
                "source_type": "ddl",
                "safety_score": 95,
                "safety_warnings": [],
            })
    # Sort by relevance (title match quality)
    results.sort(key=lambda r: -len(set(re.findall(r"\w+", r["title"].lower())) & q_words))
    return results[:20]


def search_vimm(query, platform_slug=None):
    """Search Vimm's Lair for a game."""
    results = []
    vimm_system_map = {
        "nes": "NES", "snes": "SNES", "n64": "N64", "ngc": "GameCube",
        "wii": "Wii", "gb": "GB", "gbc": "GBC", "gba": "GBA", "nds": "DS",
        "genesis": "Genesis", "saturn": "Saturn", "dc": "Dreamcast",
        "psx": "PS1", "ps2": "PS2", "ps3": "PS3", "psp": "PSP",
        "xbox": "Xbox", "xbox360": "Xbox360",
    }

    params = {"p": "list", "q": query}
    if platform_slug and platform_slug in vimm_system_map:
        params["system"] = vimm_system_map[platform_slug]

    try:
        resp = requests.get(
            "https://vimm.net/vault/",
            params=params,
            headers={"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"},
            timeout=15,
            verify=False,
        )
        if resp.status_code != 200:
            return []

        # Parse game links: <a href="/vault/{ID}" ...>Game Name</a>
        reverse_map = {v: k for k, v in vimm_system_map.items()}
        system_from_filter = vimm_system_map.get(platform_slug, "")

        for match in re.finditer(
            r'<a\s+href=\s*"/vault/(\d+)"[^>]*>([^<]+)</a>',
            resp.text,
        ):
            game_id = match.group(1)
            game_name = match.group(2).strip()

            slug = platform_slug
            system_clean = system_from_filter or "Unknown"
            is_pc = False

            # Detect platform from title when no filter is set, e.g. "Castlevania (NES)"
            if not slug:
                title_system = re.search(r'\(([A-Za-z0-9]+)\)\s*$', game_name)
                if title_system:
                    sys_name = title_system.group(1)
                    reverse_map_lower = {v.lower(): k for k, v in vimm_system_map.items()}
                    if sys_name.lower() in reverse_map_lower:
                        slug = reverse_map_lower[sys_name.lower()]
                        system_clean = sys_name

            results.append({
                "title": f"{game_name}" + (f" ({system_clean})" if system_clean and system_clean != "Unknown" and f"({system_clean})" not in game_name else ""),
                "size": 0,
                "size_human": "?",
                "seeders": 0,
                "leechers": 0,
                "indexer": "Vimm's Lair",
                "download_url": "",
                "magnet_url": "",
                "info_hash": "",
                "guid": f"https://vimm.net/vault/{game_id}",
                "platform": system_clean,
                "platform_slug": slug,
                "is_pc": is_pc,
                "age": 0,
                "source_type": "ddl",
                "vimm_id": game_id,
                "safety_score": 90,
                "safety_warnings": [],
            })
    except Exception as e:
        logger.warning(f"Vimm search error: {e}")
    return results[:20]


def download_ddl(url, dest_path, job_id):
    """Download a file via direct HTTP with progress tracking."""
    try:
        resp = requests.get(url, stream=True, timeout=30,
                          headers={"User-Agent": "Gamarr/1.0"})
        resp.raise_for_status()
        total = int(resp.headers.get("Content-Length", 0))

        # Get filename from Content-Disposition or URL
        cd = resp.headers.get("Content-Disposition", "")
        fname_match = re.search(r'filename="?([^";\n]+)"?', cd)
        if fname_match:
            filename = fname_match.group(1).strip()
        else:
            filename = unquote(url.split("/")[-1].split("?")[0])

        filepath = os.path.join(dest_path, filename)
        downloaded = 0
        last_update = 0

        with open(filepath, "wb") as f:
            for chunk in resp.iter_content(chunk_size=1024 * 256):
                f.write(chunk)
                downloaded += len(chunk)
                now = time.time()
                if now - last_update > 2 and total > 0:
                    pct = round(downloaded / total * 100, 1)
                    speed = _human_size(downloaded / max(now - last_update, 1))
                    download_jobs[job_id]["detail"] = f"Downloading... {pct}% ({_human_size(downloaded)}/{_human_size(total)})"
                    last_update = now

        download_jobs[job_id]["detail"] = f"Downloaded {_human_size(downloaded)}"
        return filepath
    except Exception as e:
        logger.error(f"DDL download failed: {e}")
        return None


def download_vimm_game(game_id, dest_path, job_id):
    """Download a game from Vimm's Lair by game ID."""
    session = requests.Session()
    ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

    try:
        # Step 1: Get game page
        game_url = f"https://vimm.net/vault/{game_id}"
        download_jobs[job_id]["detail"] = "Fetching game page..."
        resp = session.get(game_url, headers={"User-Agent": ua}, timeout=15, verify=False)
        if "unavailable at the request of" in resp.text:
            download_jobs[job_id]["status"] = "error"
            download_jobs[job_id]["error"] = "Game removed by DMCA takedown"
            return None

        # Step 2: Extract mediaId and download URL
        # Find the dl_form tag first (action may appear before or after id)
        form_tag = re.search(r'<form[^>]*id="dl_form"[^>]*>', resp.text)
        if not form_tag:
            # Try alternate: id may use single quotes or be in different order
            form_tag = re.search(r'<form[^>]*id=["\']dl_form["\'][^>]*>', resp.text)
        logger.info(f"Vimm form_tag found: {bool(form_tag)}, page length: {len(resp.text)}, status: {resp.status_code}")
        action_match = re.search(r'action="([^"]+)"', form_tag.group(0)) if form_tag else None
        media_match = re.search(r'name="mediaId"\s+value="(\d+)"', resp.text)
        if not action_match or not media_match:
            # Log what we found for debugging
            logger.error(f"Vimm parse failure: form_tag={bool(form_tag)}, action={bool(action_match)}, media={bool(media_match)}")
            if form_tag:
                logger.error(f"Vimm form tag: {form_tag.group(0)[:200]}")
            # Try to find mediaId from JavaScript media array as fallback
            js_media = re.search(r'"ID":(\d+)', resp.text)
            if js_media and form_tag:
                logger.info(f"Vimm: Found mediaId from JS: {js_media.group(1)}")
                media_match = js_media
            if not form_tag:
                # Last resort: look for dl2.vimm.net anywhere in the page
                dl2_match = re.search(r'(//dl\d*\.vimm\.net/[^"\']*)', resp.text)
                if dl2_match and js_media:
                    logger.info(f"Vimm: Found dl URL from page: {dl2_match.group(1)}")
                    action_match = dl2_match
                    media_match = js_media
            if not action_match or not media_match:
                download_jobs[job_id]["status"] = "error"
                download_jobs[job_id]["error"] = "Could not find download form on Vimm"
                return None

        action_url = action_match.group(1)
        if action_url.startswith("//"):
            action_url = "https:" + action_url
        media_id = media_match.group(1)
        logger.info(f"Vimm download: action={action_url}, mediaId={media_id}")

        # Step 3: Download via POST (Vimm form uses POST with mediaId)
        download_jobs[job_id]["detail"] = "Starting download from Vimm..."
        time.sleep(3)  # Respectful delay

        # Vimm download servers: try the action URL first, then alternates
        dl_urls = [action_url]
        # Extract server number from action URL (e.g., dl2 -> download2)
        dl_num = re.search(r'dl(\d+)', action_url)
        if dl_num:
            dl_urls.append(f"https://download{dl_num.group(1)}.vimm.net/download/")

        dl_resp = None
        for dl_url in dl_urls:
            try:
                dl_resp = session.post(
                    dl_url,
                    data={"mediaId": media_id},
                    headers={
                        "User-Agent": ua,
                        "Referer": game_url,
                        "Origin": "https://vimm.net",
                        "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
                    },
                    stream=True, timeout=60, verify=False,
                    allow_redirects=True,
                )
                if dl_resp.status_code == 200:
                    logger.info(f"Vimm download started from {dl_url}")
                    break
                logger.warning(f"Vimm download {dl_url} returned {dl_resp.status_code}")
                dl_resp = None
            except Exception as e:
                logger.warning(f"Vimm download {dl_url} failed: {e}")
                dl_resp = None

        if not dl_resp:
            download_jobs[job_id]["status"] = "error"
            download_jobs[job_id]["error"] = f"Vimm download server rejected request (tried {len(dl_urls)} URLs)"
            return None

        total = int(dl_resp.headers.get("Content-Length", 0))
        cd = dl_resp.headers.get("Content-Disposition", "")
        fname_match = re.search(r'filename="?([^";\n]+)"?', cd)
        filename = fname_match.group(1).strip() if fname_match else f"{game_id}.7z"
        filepath = os.path.join(dest_path, filename)

        downloaded = 0
        start_time = time.time()
        with open(filepath, "wb") as f:
            for chunk in dl_resp.iter_content(chunk_size=1024 * 256):
                f.write(chunk)
                downloaded += len(chunk)
                elapsed = time.time() - start_time
                if elapsed > 0 and total > 0:
                    pct = round(downloaded / total * 100, 1)
                    download_jobs[job_id]["detail"] = f"Downloading... {pct}% ({_human_size(downloaded)}/{_human_size(total)})"

        download_jobs[job_id]["detail"] = f"Downloaded {_human_size(downloaded)}"
        return filepath
    except Exception as e:
        logger.error(f"Vimm download failed: {e}")
        return None


# Load custom DDL sources from config file
def _load_ddl_sources():
    """Load custom DDL sources (user-configurable)."""
    if not os.path.exists(DDL_SOURCES_FILE):
        return []
    try:
        with open(DDL_SOURCES_FILE) as f:
            return json.load(f)
    except Exception:
        return []


def _save_ddl_sources(sources):
    with open(DDL_SOURCES_FILE, "w") as f:
        json.dump(sources, f, indent=2)


# =============================================================================
# Prowlarr Search
# =============================================================================
def search_prowlarr_games(query, platform_filter=None):
    results = []
    filter_categories = None
    if platform_filter and platform_filter != "all":
        filter_categories = set(get_categories_for_platform(platform_filter))

    # Search game-friendly indexers individually to avoid slow indexer timeouts
    # 1337x(7), ThePirateBay(5), KickAssTorrents(15), BitSearch(9), Knaben(8)
    game_indexers = [7, 5, 15, 9, 8, 3, 4]  # 3=Nyaa, 4=LimeTorrents
    all_items = []

    for indexer_id in game_indexers:
        try:
            resp = requests.get(
                f"{config.PROWLARR_URL}/api/v1/search",
                params={
                    "query": query,
                    "indexerIds": indexer_id,
                    "type": "search",
                    "limit": 50,
                },
                headers={"X-Api-Key": config.PROWLARR_API_KEY},
                timeout=15,
            )
            if resp.status_code == 200:
                all_items.extend(resp.json())
        except Exception as e:
            logger.warning(f"Indexer {indexer_id} timed out: {e}")

    try:
        for item in all_items:
            size = item.get("size", 0)
            cats = item.get("categories", [])
            detected = detect_platform(cats)

            # Local category filtering
            if filter_categories:
                cat_ids = {c.get("id") if isinstance(c, dict) else c for c in cats}
                if not cat_ids & filter_categories:
                    continue

            results.append({
                "title": item.get("title", ""),
                "size": size,
                "size_human": _human_size(size),
                "seeders": item.get("seeders", 0),
                "leechers": item.get("leechers", 0),
                "indexer": item.get("indexer", ""),
                "download_url": item.get("downloadUrl", ""),
                "magnet_url": item.get("magnetUrl", ""),
                "info_hash": item.get("infoHash", ""),
                "guid": item.get("guid", ""),
                "platform": detected["name"],
                "platform_slug": detected["slug"],
                "is_pc": detected["is_pc"],
                "age": item.get("age", 0),
            })
    except Exception as e:
        logger.error(f"Prowlarr game search failed: {e}")
    return results


def filter_game_results(results, query):
    filtered = []
    seen_titles = {}
    for r in results:
        title = r.get("title", "")
        if _SUSPICIOUS.search(title):
            continue
        if _is_non_english(title):
            continue
        if r.get("seeders", 0) < 1:
            continue
        size = r.get("size", 0)
        if size and (size < 1_000_000 or size > 200_000_000_000):
            continue
        if not _title_relevant(query, title):
            continue

        # Add safety scoring
        score, warnings = _safety_score(r)
        r["safety_score"] = score
        r["safety_warnings"] = warnings
        if score <= 10:
            continue  # Auto-hide extremely unsafe results

        norm = re.sub(r"[^a-z0-9]", "", title.lower())[:60]
        if norm in seen_titles:
            existing = seen_titles[norm]
            if r.get("seeders", 0) > existing.get("seeders", 0):
                filtered.remove(existing)
                seen_titles[norm] = r
                filtered.append(r)
            continue
        seen_titles[norm] = r
        filtered.append(r)
    return filtered


# =============================================================================
# Download & Organize
# =============================================================================
def download_game(data):
    url = data.get("download_url") or data.get("magnet_url", "")
    if not url and data.get("info_hash"):
        url = f"magnet:?xt=urn:btih:{data['info_hash']}"
    title = data.get("title", "Unknown")
    platform = data.get("platform", "PC")
    platform_slug = data.get("platform_slug")
    is_pc = data.get("is_pc", True)

    if not url:
        return None, "No download URL"

    job_id = str(uuid.uuid4())[:8]
    download_jobs[job_id] = {
        "status": "downloading",
        "title": title,
        "platform": platform,
        "platform_slug": platform_slug,
        "is_pc": is_pc,
        "error": None,
        "detail": "Sending to qBittorrent...",
    }

    success = qb.add_torrent(url, title)
    if not success:
        download_jobs[job_id]["status"] = "error"
        download_jobs[job_id]["error"] = "Failed to add torrent to qBittorrent"
        return job_id, None

    download_jobs[job_id]["detail"] = "Downloading..."
    threading.Thread(
        target=watch_game_torrent,
        args=(job_id, title, platform, platform_slug, is_pc),
        daemon=True,
    ).start()
    return job_id, None


def watch_game_torrent(job_id, title, platform, platform_slug, is_pc):
    logger.info(f"Watching game torrent: {title} (platform: {platform})")
    title_lower = title.lower()
    max_wait = 86400 * 7  # 7 days max
    start = time.time()
    file_scan_done = False
    matched_hash = None

    while time.time() - start < max_wait:
        try:
            torrents = qb.get_torrents(category=config.QB_CATEGORY)
            for t in torrents:
                t_name = t.get("name", "")
                is_match = (
                    t_name == title or title_lower in t_name.lower() or t_name.lower() in title_lower
                )
                if not is_match:
                    continue

                t_hash = t.get("hash", "")

                # Layer 1: Scan file list once metadata is available
                if not file_scan_done and t.get("progress", 0) > 0:
                    download_jobs[job_id]["detail"] = "Scanning file list..."
                    is_safe, issues = scan_torrent_file_list(t_hash)
                    file_scan_done = True
                    matched_hash = t_hash
                    if not is_safe:
                        logger.warning(f"File list scan failed for {title}: {issues}")
                        download_jobs[job_id]["status"] = "error"
                        download_jobs[job_id]["error"] = f"Blocked: {'; '.join(issues)}"
                        download_jobs[job_id]["detail"] = "Dangerous files detected - download cancelled"
                        qb.delete_torrent(t_hash, delete_files=True)
                        return
                    download_jobs[job_id]["detail"] = "File list clean. Downloading..."

                # Wait for download to complete (stoppedUP = finished + stopped seeding)
                if t.get("progress", 0) >= 1.0 or t.get("state", "") == "stoppedUP":
                    # Layer 2: ClamAV scan before organizing
                    content_path = t.get("content_path", "")
                    save_path = t.get("save_path", config.QB_SAVE_PATH)
                    scan_path = content_path or os.path.join(save_path, t_name)

                    download_jobs[job_id]["status"] = "scanning"
                    download_jobs[job_id]["detail"] = "Running virus scan..."
                    is_clean, infected = scan_with_clamav(scan_path)
                    if not is_clean:
                        logger.warning(f"ClamAV found infections in {title}: {infected}")
                        download_jobs[job_id]["status"] = "error"
                        download_jobs[job_id]["error"] = f"Virus detected: {'; '.join(infected[:3])}"
                        download_jobs[job_id]["detail"] = "Infected files found - download quarantined"
                        qb.delete_torrent(t_hash, delete_files=True)
                        return

                    download_jobs[job_id]["status"] = "organizing"
                    download_jobs[job_id]["detail"] = "Scans passed. Moving to library..."
                    organize_game(job_id, t, platform, platform_slug, is_pc)
                    return
        except Exception as e:
            logger.error(f"Watch error: {e}")
        time.sleep(5)
    download_jobs[job_id]["status"] = "error"
    download_jobs[job_id]["error"] = "Timed out waiting for download"


def _detect_platform_from_metadata(content_path):
    """Try to read metadata.json in content folder for platform info."""
    import json
    meta_path = None
    if os.path.isdir(content_path):
        meta_path = os.path.join(content_path, "metadata.json")
    if not meta_path or not os.path.exists(meta_path):
        return None, None, None
    try:
        with open(meta_path) as f:
            meta = json.load(f)
        plat = meta.get("platform", "").lower().strip()
        if not plat:
            return None, None, None
        # Map common metadata platform names to RomM slugs
        meta_map = {
            "gamecube": ("GameCube", "ngc", False),
            "ngc": ("GameCube", "ngc", False),
            "wii": ("Wii", "wii", False),
            "switch": ("Switch", "switch", False),
            "nintendo switch": ("Switch", "switch", False),
            "ps1": ("PS1", "psx", False), "psx": ("PS1", "psx", False), "playstation": ("PS1", "psx", False),
            "ps2": ("PS2", "ps2", False), "playstation 2": ("PS2", "ps2", False),
            "ps3": ("PS3", "ps3", False), "playstation 3": ("PS3", "ps3", False),
            "ps4": ("PS4", "ps4", False),
            "psp": ("PSP", "psp", False),
            "xbox": ("Xbox", "xbox", False),
            "xbox 360": ("Xbox 360", "xbox360", False), "xbox360": ("Xbox 360", "xbox360", False),
            "ds": ("DS", "nds", False), "nds": ("DS", "nds", False), "nintendo ds": ("DS", "nds", False),
            "3ds": ("3DS", "3ds", False), "nintendo 3ds": ("3DS", "3ds", False),
            "dreamcast": ("Dreamcast", "dc", False),
            "n64": ("Nintendo 64", "n64", False), "nintendo 64": ("Nintendo 64", "n64", False),
            "snes": ("SNES", "snes", False), "super nintendo": ("SNES", "snes", False),
            "nes": ("NES", "nes", False),
            "gba": ("Game Boy Advance", "gba", False), "game boy advance": ("Game Boy Advance", "gba", False),
            "gb": ("Game Boy", "gb", False), "game boy": ("Game Boy", "gb", False),
            "genesis": ("Sega Genesis", "genesis", False), "sega genesis": ("Sega Genesis", "genesis", False),
            "saturn": ("Sega Saturn", "saturn", False), "sega saturn": ("Sega Saturn", "saturn", False),
            "pc": ("PC", None, True), "windows": ("PC", None, True),
        }
        if plat in meta_map:
            return meta_map[plat]
        logger.info(f"Unknown metadata platform: {plat}")
        return None, None, None
    except Exception as e:
        logger.warning(f"Failed to read metadata.json: {e}")
        return None, None, None


def _detect_platform_from_files(content_path, title):
    """Detect platform from file extensions and title keywords."""
    # Collect all file extensions in the content
    exts = set()
    if os.path.isdir(content_path):
        for root, dirs, files in os.walk(content_path):
            for f in files:
                ext = os.path.splitext(f)[1].lower()
                if ext:
                    exts.add(ext)
    else:
        ext = os.path.splitext(content_path)[1].lower()
        if ext:
            exts.add(ext)

    # Extension -> platform mapping
    ext_map = {
        ".nsp": ("Switch", "switch", False),
        ".xci": ("Switch", "switch", False),
        ".nsz": ("Switch", "switch", False),
        ".3ds": ("3DS", "3ds", False),
        ".cia": ("3DS", "3ds", False),
        ".nds": ("DS", "nds", False),
        ".gba": ("Game Boy Advance", "gba", False),
        ".gbc": ("Game Boy Color", "gbc", False),
        ".gb": ("Game Boy", "gb", False),
        ".nes": ("NES", "nes", False),
        ".sfc": ("SNES", "snes", False),
        ".smc": ("SNES", "snes", False),
        ".n64": ("Nintendo 64", "n64", False),
        ".z64": ("Nintendo 64", "n64", False),
        ".v64": ("Nintendo 64", "n64", False),
        ".gcm": ("GameCube", "ngc", False),
        ".gcz": ("GameCube", "ngc", False),
        ".wbfs": ("Wii", "wii", False),
        ".wad": ("Wii", "wii", False),
        ".pbp": ("PSP", "psp", False),
        ".cso": ("PSP", "psp", False),
        ".chd": None,  # Ambiguous - could be PS1, PS2, Saturn, Dreamcast
        ".gdi": ("Dreamcast", "dc", False),
        ".cdi": ("Dreamcast", "dc", False),
    }

    for ext in exts:
        if ext in ext_map and ext_map[ext] is not None:
            logger.info(f"Platform detected from extension {ext}")
            return ext_map[ext]

    # Title keyword detection
    title_lower = title.lower()
    title_hints = [
        (r"\[nsp\]|\bnsp\b|switch", ("Switch", "switch", False)),
        (r"\[xci\]|\bxci\b", ("Switch", "switch", False)),
        (r"\bwiiu\b|wii\s*u", ("Wii U", "wiiu", False)),
        (r"\bwii\b", ("Wii", "wii", False)),
        (r"\bgamecube\b|\bngc\b|\bgcn\b", ("GameCube", "ngc", False)),
        (r"\b3ds\b", ("3DS", "3ds", False)),
        (r"\bnds\b|\bnintendo\s*ds\b", ("DS", "nds", False)),
        (r"\bgba\b", ("Game Boy Advance", "gba", False)),
        (r"\bps3\b|playstation\s*3", ("PS3", "ps3", False)),
        (r"\bps2\b|playstation\s*2", ("PS2", "ps2", False)),
        (r"\bps1\b|\bpsx\b|playstation(?!\s*[2345])", ("PS1", "psx", False)),
        (r"\bpsp\b", ("PSP", "psp", False)),
        (r"\bxbox\s*360", ("Xbox 360", "xbox360", False)),
        (r"\bxbox\b(?!\s*360)", ("Xbox", "xbox", False)),
        (r"\bdreamcast\b", ("Dreamcast", "dc", False)),
        (r"\bn64\b|nintendo\s*64", ("Nintendo 64", "n64", False)),
        (r"\bsnes\b|super\s*nintendo", ("SNES", "snes", False)),
        (r"\bnes\b(?!\w)", ("NES", "nes", False)),
        (r"\bgenesis\b|mega\s*drive", ("Sega Genesis", "genesis", False)),
    ]
    for pattern, result in title_hints:
        if re.search(pattern, title_lower):
            logger.info(f"Platform detected from title keyword: {pattern}")
            return result

    return None


def organize_game(job_id, torrent, platform, platform_slug, is_pc):
    content_path = torrent.get("content_path", "")
    torrent_name = torrent.get("name", "")
    torrent_hash = torrent.get("hash", "")

    # Map qBit's save path to our container's mount
    # qBit saves to /games-incoming/..., we see it as /games-incoming/...
    if not content_path or not os.path.exists(content_path):
        # Try constructing path from save_path + name
        save_path = torrent.get("save_path", config.QB_SAVE_PATH)
        content_path = os.path.join(save_path, torrent_name)

    if not os.path.exists(content_path):
        download_jobs[job_id]["status"] = "error"
        download_jobs[job_id]["error"] = f"Cannot find downloaded files at {content_path}"
        logger.error(f"Content path not found: {content_path}")
        return

    # If platform is unknown, try reading metadata.json
    if not platform_slug and not is_pc:
        meta_name, meta_slug, meta_is_pc = _detect_platform_from_metadata(content_path)
        if meta_slug or meta_is_pc:
            platform, platform_slug, is_pc = meta_name, meta_slug, meta_is_pc
            download_jobs[job_id]["platform"] = platform
            download_jobs[job_id]["platform_slug"] = platform_slug
            download_jobs[job_id]["is_pc"] = is_pc
            logger.info(f"Detected platform from metadata: {platform} ({platform_slug})")

    # Fallback: detect platform from file extensions and title keywords
    if not platform_slug and not is_pc:
        detected = _detect_platform_from_files(content_path, torrent_name)
        if detected:
            platform, platform_slug, is_pc = detected
            download_jobs[job_id]["platform"] = platform
            download_jobs[job_id]["platform_slug"] = platform_slug
            download_jobs[job_id]["is_pc"] = is_pc
            logger.info(f"Detected platform from files/title: {platform} ({platform_slug})")

    try:
        if is_pc:
            dest_dir = config.GAMES_VAULT_PATH
            dest = os.path.join(dest_dir, os.path.basename(content_path))
            if os.path.isdir(content_path):
                shutil.copytree(content_path, dest, dirs_exist_ok=True)
                shutil.rmtree(content_path)
            else:
                shutil.move(content_path, dest)
            download_jobs[job_id]["status"] = "completed"
            download_jobs[job_id]["detail"] = "Moved to GameVault"
            logger.info(f"PC game organized: {torrent_name} -> {dest}")

        elif platform_slug:
            dest_dir = os.path.join(config.GAMES_ROMS_PATH, platform_slug)
            os.makedirs(dest_dir, exist_ok=True)
            dest = os.path.join(dest_dir, os.path.basename(content_path))
            if os.path.isdir(content_path):
                shutil.copytree(content_path, dest, dirs_exist_ok=True)
                shutil.rmtree(content_path)
            else:
                shutil.move(content_path, dest)
            download_jobs[job_id]["status"] = "completed"
            download_jobs[job_id]["detail"] = f"Moved to RomM ({platform})"
            logger.info(f"ROM organized: {torrent_name} -> {dest}")

        else:
            download_jobs[job_id]["status"] = "completed"
            download_jobs[job_id]["detail"] = "Downloaded (unknown platform, left in staging)"
            logger.warning(f"No platform slug for {torrent_name}, left in downloads")
            return  # Don't delete torrent if we didn't move the files

        # Clean up torrent from qBit (files already moved)
        qb.delete_torrent(torrent_hash, delete_files=True)

    except Exception as e:
        download_jobs[job_id]["status"] = "error"
        download_jobs[job_id]["error"] = f"Organize failed: {str(e)}"
        logger.error(f"Organize error for {torrent_name}: {e}")


# =============================================================================
# API Routes
# =============================================================================
@app.route("/")
def index():
    return send_file("templates/index.html")


@app.route("/api/search")
def api_search():
    query = request.args.get("q", "").strip()
    platform = request.args.get("platform", "all")
    if not query:
        return jsonify({"results": [], "error": "No query"})
    start = time.time()

    # Torrent results from Prowlarr
    results = search_prowlarr_games(query, platform_filter=platform)
    results = filter_game_results(results, query)
    for r in results:
        r.setdefault("source_type", "torrent")

    # DDL results from Myrient (only when a platform is selected)
    ddl_slug = platform if platform != "all" and platform != "pc" else None
    if ddl_slug:
        try:
            myrient_results = search_myrient(query, platform_slug=ddl_slug)
            results.extend(myrient_results)
        except Exception as e:
            logger.warning(f"Myrient search failed: {e}")

    # DDL results from Vimm's Lair
    try:
        vimm_results = search_vimm(query, platform_slug=ddl_slug)
        results.extend(vimm_results)
    except Exception as e:
        logger.warning(f"Vimm search failed: {e}")

    # Sort: torrents with seeders first, then DDL
    results.sort(key=lambda r: (r.get("seeders", 0) * 10 if r.get("source_type") == "torrent" else 5), reverse=True)
    elapsed = int((time.time() - start) * 1000)
    return jsonify({"results": results, "search_time_ms": elapsed})


@app.route("/api/platforms")
def api_platforms():
    platforms = [{"id": "all", "name": "All Platforms"}, {"id": "pc", "name": "PC"}]
    seen = {"PC"}
    for cat_id, (name, slug, is_pc) in sorted(PLATFORM_MAP.items()):
        if is_pc or name in seen or name == "Other" or not slug:
            continue
        seen.add(name)
        platforms.append({"id": slug, "name": name})
    for slug, name in EXTRA_PLATFORMS:
        if name not in seen:
            platforms.append({"id": slug, "name": name})
    return jsonify({"platforms": platforms})


@app.route("/api/download", methods=["POST"])
def api_download():
    data = request.json
    source_type = data.get("source_type", "torrent")

    if source_type == "ddl":
        # Direct download
        job_id = str(uuid.uuid4())[:8]
        title = data.get("title", "Unknown")
        platform = data.get("platform", "Unknown")
        platform_slug = data.get("platform_slug")
        is_pc = data.get("is_pc", False)
        url = data.get("download_url", "")
        vimm_id = data.get("vimm_id")

        if not url and not vimm_id:
            return jsonify({"success": False, "error": "No download URL"}), 400

        download_jobs[job_id] = {
            "status": "downloading",
            "title": title,
            "platform": platform,
            "platform_slug": platform_slug,
            "is_pc": is_pc,
            "error": None,
            "detail": "Starting direct download...",
        }

        threading.Thread(
            target=_ddl_download_worker,
            args=(job_id, url, vimm_id, title, platform, platform_slug, is_pc),
            daemon=True,
        ).start()
        return jsonify({"success": True, "job_id": job_id})

    # Torrent download (existing logic)
    job_id, error = download_game(data)
    if error and not job_id:
        return jsonify({"success": False, "error": error}), 400
    return jsonify({"success": True, "job_id": job_id})


def _ddl_download_worker(job_id, url, vimm_id, title, platform, platform_slug, is_pc):
    """Worker thread for direct downloads."""
    staging = config.QB_SAVE_PATH
    os.makedirs(staging, exist_ok=True)

    try:
        if vimm_id:
            filepath = download_vimm_game(vimm_id, staging, job_id)
        elif url:
            filepath = download_ddl(url, staging, job_id)
        else:
            filepath = None

        if not filepath or not os.path.exists(filepath):
            if download_jobs[job_id]["status"] != "error":
                download_jobs[job_id]["status"] = "error"
                download_jobs[job_id]["error"] = "Download failed"
            return

        # ClamAV scan
        download_jobs[job_id]["status"] = "scanning"
        download_jobs[job_id]["detail"] = "Running virus scan..."
        is_clean, infected = scan_with_clamav(filepath)
        if not is_clean:
            logger.warning(f"ClamAV found infections in DDL {title}: {infected}")
            download_jobs[job_id]["status"] = "error"
            download_jobs[job_id]["error"] = f"Virus detected: {'; '.join(infected[:3])}"
            try:
                os.remove(filepath)
            except OSError:
                pass
            return

        # Organize to library
        download_jobs[job_id]["status"] = "organizing"
        download_jobs[job_id]["detail"] = "Moving to library..."
        _organize_ddl_file(job_id, filepath, title, platform, platform_slug, is_pc)

    except Exception as e:
        logger.error(f"DDL worker error: {e}")
        download_jobs[job_id]["status"] = "error"
        download_jobs[job_id]["error"] = str(e)


def _organize_ddl_file(job_id, filepath, title, platform, platform_slug, is_pc):
    """Move a downloaded DDL file to the correct library."""
    filename = os.path.basename(filepath)
    try:
        if is_pc:
            dest = os.path.join(config.GAMES_VAULT_PATH, filename)
            shutil.move(filepath, dest)
            download_jobs[job_id]["status"] = "completed"
            download_jobs[job_id]["detail"] = "Moved to GameVault"
            logger.info(f"DDL PC game: {filename} -> {dest}")
        elif platform_slug:
            dest_dir = os.path.join(config.GAMES_ROMS_PATH, platform_slug)
            os.makedirs(dest_dir, exist_ok=True)
            dest = os.path.join(dest_dir, filename)
            shutil.move(filepath, dest)
            download_jobs[job_id]["status"] = "completed"
            download_jobs[job_id]["detail"] = f"Moved to RomM ({platform})"
            logger.info(f"DDL ROM: {filename} -> {dest}")
        else:
            download_jobs[job_id]["status"] = "completed"
            download_jobs[job_id]["detail"] = "Downloaded (unknown platform, left in staging)"
    except Exception as e:
        download_jobs[job_id]["status"] = "error"
        download_jobs[job_id]["error"] = f"Organize failed: {str(e)}"
        logger.error(f"DDL organize error: {e}")


@app.route("/api/ddl-sources")
def api_ddl_sources():
    """List all DDL sources (built-in + custom)."""
    built_in = [
        {"name": "Myrient", "url": MYRIENT_BASE, "type": "myrient", "builtin": True,
         "platforms": list(MYRIENT_PLATFORMS.keys())},
        {"name": "Vimm's Lair", "url": "https://vimm.net/vault/", "type": "vimm", "builtin": True,
         "platforms": ["nes", "snes", "n64", "ngc", "wii", "gb", "gbc", "gba", "nds",
                       "genesis", "saturn", "dc", "psx", "ps2", "ps3", "psp", "xbox", "xbox360"]},
    ]
    custom = _load_ddl_sources()
    return jsonify({"sources": built_in + custom})


@app.route("/api/ddl-sources", methods=["POST"])
def api_add_ddl_source():
    """Add a custom DDL source."""
    data = request.json
    name = data.get("name", "").strip()
    url = data.get("url", "").strip()
    if not name or not url:
        return jsonify({"success": False, "error": "Name and URL required"}), 400
    sources = _load_ddl_sources()
    sources.append({"name": name, "url": url, "type": "custom"})
    _save_ddl_sources(sources)
    return jsonify({"success": True})


@app.route("/api/ddl-sources/<int:idx>", methods=["DELETE"])
def api_delete_ddl_source(idx):
    """Delete a custom DDL source."""
    sources = _load_ddl_sources()
    if 0 <= idx < len(sources):
        sources.pop(idx)
        _save_ddl_sources(sources)
        return jsonify({"success": True})
    return jsonify({"success": False, "error": "Not found"}), 404


@app.route("/api/downloads")
def api_downloads():
    downloads = []

    # Collect job titles for dedup
    job_titles = {j["title"].lower() for j in download_jobs.values()}

    # Active torrents from qBit
    status_map = {
        "downloading": "downloading", "stalledDL": "stalled",
        "metaDL": "metadata", "forcedDL": "downloading",
        "pausedDL": "paused", "queuedDL": "queued",
        "uploading": "seeding", "stalledUP": "seeding",
        "forcedUP": "seeding", "stoppedUP": "completed",
        "pausedUP": "completed", "queuedUP": "completed",
        "checkingDL": "checking", "checkingUP": "checking",
        "stoppedDL": "paused",
        "error": "error", "missingFiles": "error",
    }
    try:
        torrents = qb.get_torrents(category=config.QB_CATEGORY)
        for t in torrents:
            t_name = t.get("name", "")
            state = t.get("state", "")
            progress = round(t.get("progress", 0) * 100, 1)
            speed = _human_size(t.get("dlspeed", 0)) + "/s"

            # If there's an active job tracking this torrent, merge torrent info into the job
            matched_job = None
            for jid, job in download_jobs.items():
                if job["title"].lower() in t_name.lower() or t_name.lower() in job["title"].lower():
                    matched_job = (jid, job)
                    break

            if matched_job:
                jid, job = matched_job
                downloads.append({
                    "type": "job",
                    "title": job["title"],
                    "platform": job.get("platform", ""),
                    "status": job["status"] if job["status"] != "downloading" else status_map.get(state, state),
                    "job_id": jid,
                    "error": job.get("error"),
                    "detail": job.get("detail"),
                    "progress": progress,
                    "size": _human_size(t.get("total_size", 0)),
                    "speed": speed,
                    "eta": t.get("eta", 0),
                    "hash": t.get("hash", ""),
                })
            else:
                downloads.append({
                    "type": "torrent",
                    "title": t_name,
                    "progress": progress,
                    "status": status_map.get(state, state),
                    "size": _human_size(t.get("total_size", 0)),
                    "speed": speed,
                    "eta": t.get("eta", 0),
                    "hash": t.get("hash", ""),
                })
    except Exception:
        pass

    # Show unmatched jobs (scanning, organizing, completed, error)
    matched_job_ids = {d.get("job_id") for d in downloads if d.get("job_id")}
    for job_id, job in list(download_jobs.items()):
        if job_id not in matched_job_ids:
            downloads.append({
                "type": "job",
                "title": job["title"],
                "platform": job.get("platform", ""),
                "status": job["status"],
                "job_id": job_id,
                "error": job.get("error"),
                "detail": job.get("detail"),
            })

    return jsonify({"downloads": downloads})


@app.route("/api/downloads/torrent/<torrent_hash>", methods=["DELETE"])
def api_delete_torrent(torrent_hash):
    success = qb.delete_torrent(torrent_hash, delete_files=True)
    return jsonify({"success": success})


@app.route("/api/downloads/<job_id>", methods=["DELETE"])
def api_delete_job(job_id):
    if job_id in download_jobs:
        del download_jobs[job_id]
        return jsonify({"success": True})
    return jsonify({"success": False, "error": "Not found"}), 404


@app.route("/api/downloads/clear", methods=["POST"])
def api_clear_finished():
    to_remove = [jid for jid, j in download_jobs.items() if j["status"] in ("completed", "error")]
    for jid in to_remove:
        del download_jobs[jid]
    return jsonify({"success": True, "cleared": len(to_remove)})


@app.route("/api/downloads/organize/<torrent_hash>", methods=["POST"])
def api_organize_torrent(torrent_hash):
    """Manually trigger organize for a completed torrent."""
    data = request.json or {}
    platform_slug = data.get("platform_slug")
    is_pc = data.get("is_pc", False)
    platform = data.get("platform", "Unknown")

    try:
        torrents = qb.get_torrents(category=config.QB_CATEGORY)
        torrent = None
        for t in torrents:
            if t.get("hash", "") == torrent_hash:
                torrent = t
                break
        if not torrent:
            return jsonify({"success": False, "error": "Torrent not found"}), 404

        if torrent.get("progress", 0) < 1.0:
            return jsonify({"success": False, "error": "Torrent not yet complete"}), 400

        job_id = str(uuid.uuid4())[:8]
        download_jobs[job_id] = {
            "status": "organizing",
            "title": torrent.get("name", ""),
            "platform": platform,
            "platform_slug": platform_slug,
            "is_pc": is_pc,
            "error": None,
            "detail": "Scanning and organizing...",
        }

        threading.Thread(
            target=_organize_with_scan,
            args=(job_id, torrent, platform, platform_slug, is_pc),
            daemon=True,
        ).start()
        return jsonify({"success": True, "job_id": job_id})
    except Exception as e:
        return jsonify({"success": False, "error": str(e)}), 500


def _organize_with_scan(job_id, torrent, platform, platform_slug, is_pc):
    """Run ClamAV scan then organize."""
    content_path = torrent.get("content_path", "")
    save_path = torrent.get("save_path", config.QB_SAVE_PATH)
    t_name = torrent.get("name", "")
    scan_path = content_path or os.path.join(save_path, t_name)

    download_jobs[job_id]["status"] = "scanning"
    download_jobs[job_id]["detail"] = "Running virus scan..."
    is_clean, infected = scan_with_clamav(scan_path)
    if not is_clean:
        logger.warning(f"ClamAV found infections in {t_name}: {infected}")
        download_jobs[job_id]["status"] = "error"
        download_jobs[job_id]["error"] = f"Virus detected: {'; '.join(infected[:3])}"
        download_jobs[job_id]["detail"] = "Infected files found - download quarantined"
        qb.delete_torrent(torrent.get("hash", ""), delete_files=True)
        return

    download_jobs[job_id]["status"] = "organizing"
    download_jobs[job_id]["detail"] = "Scans passed. Moving to library..."
    organize_game(job_id, torrent, platform, platform_slug, is_pc)


def recover_orphaned_torrents():
    """Check for completed game torrents with no active job and resume watching them."""
    try:
        # Retry login for up to 60s — qBit may not be ready at container startup
        for attempt in range(12):
            if qb.login():
                break
            logger.info(f"Orphan recovery: waiting for qBit (attempt {attempt+1}/12)...")
            time.sleep(5)
        else:
            logger.warning("Cannot check orphaned torrents - qBit login failed after retries")
            return
        torrents = qb.get_torrents(category=config.QB_CATEGORY)
        for t in torrents:
            t_name = t.get("name", "")
            t_hash = t.get("hash", "")
            progress = t.get("progress", 0)

            # Create a recovery job for any existing torrent
            job_id = str(uuid.uuid4())[:8]
            # Try to guess platform from name (basic heuristic)
            platform = "Unknown"
            platform_slug = None
            is_pc = False
            name_lower = t_name.lower()
            # Known PC release groups — if present, it's definitely a PC game
            _PC_RELEASE_GROUPS = {
                "skidrow", "codex", "fitgirl", "dodi", "gog", "plaza", "cpy",
                "empress", "rune", "razordox", "tinyiso", "elamigos", "repack",
            }
            if any(grp in name_lower for grp in _PC_RELEASE_GROUPS):
                platform, platform_slug, is_pc = "PC", None, True
            else:
                platform_hints = {
                    "wii": ("Wii", "wii", False), "gamecube": ("GameCube", "ngc", False),
                    "ngc": ("GameCube", "ngc", False), "switch": ("Switch", "switch", False),
                    "nsp": ("Switch", "switch", False), "xci": ("Switch", "switch", False),
                    "ps2": ("PS2", "ps2", False), "ps3": ("PS3", "ps3", False),
                    "psp": ("PSP", "psp", False), "nds": ("DS", "nds", False),
                    "3ds": ("3DS", "3ds", False), "dreamcast": ("Dreamcast", "dc", False),
                    "gba": ("Game Boy Advance", "gba", False),
                }
                for hint, (p_name, p_slug, p_is_pc) in platform_hints.items():
                    if hint in name_lower:
                        platform, platform_slug, is_pc = p_name, p_slug, p_is_pc
                    break

            if progress >= 1.0:
                # Completed but not organized - create a job for it
                download_jobs[job_id] = {
                    "status": "completed_unorganized",
                    "title": t_name,
                    "platform": platform,
                    "platform_slug": platform_slug,
                    "is_pc": is_pc,
                    "error": None,
                    "detail": "Completed - needs organizing (use organize button)",
                }
                logger.info(f"Recovered completed torrent: {t_name}")
            else:
                # Still downloading - create a watcher
                download_jobs[job_id] = {
                    "status": "downloading",
                    "title": t_name,
                    "platform": platform,
                    "platform_slug": platform_slug,
                    "is_pc": is_pc,
                    "error": None,
                    "detail": "Recovered - watching download...",
                }
                threading.Thread(
                    target=watch_game_torrent,
                    args=(job_id, t_name, platform, platform_slug, is_pc),
                    daemon=True,
                ).start()
                logger.info(f"Recovered in-progress torrent: {t_name} ({progress*100:.0f}%)")
    except Exception as e:
        logger.error(f"Orphan recovery failed: {e}")


# =============================================================================
# Main
# =============================================================================
if __name__ == "__main__":
    os.makedirs(config.QB_SAVE_PATH, exist_ok=True)
    os.makedirs(config.GAMES_VAULT_PATH, exist_ok=True)
    os.makedirs(config.GAMES_ROMS_PATH, exist_ok=True)
    logger.info("Gamarr starting on port 5001")
    # Recover any orphaned torrents from before restart
    threading.Thread(target=recover_orphaned_torrents, daemon=True).start()
    app.run(host="0.0.0.0", port=5001, debug=False)
