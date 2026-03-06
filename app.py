import logging
import os
import re
import shutil
import sys
import threading
import time
import uuid

import requests
from flask import Flask, jsonify, request, send_file

import config
import monitor
import sources
from db import JobStore
from platforms import (
    PLATFORM_MAP,
    EXTRA_PLATFORMS,
    ALL_GAME_CATEGORIES,
    detect_platform,
    get_categories_for_platform,
    _detect_platform_from_metadata,
    _detect_platform_from_files,
)

app = Flask(__name__)
logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s", stream=sys.stderr)
logger = logging.getLogger("gamarr")

# Persistent job store — initialised at startup once DATA_DIR is available.
# Declared at module level so all functions can reference it before __main__
# creates the real instance.
download_jobs: JobStore = None  # type: ignore[assignment]
_monitor = None  # initialized in __main__


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


def _docker_api(method, path, **kwargs):
    """Call Docker Engine API via Unix socket."""
    import http.client
    conn = http.client.HTTPConnection("localhost")
    conn.sock = __import__("socket").socket(__import__("socket").AF_UNIX, __import__("socket").SOCK_STREAM)
    conn.sock.connect(config.DOCKER_SOCKET)
    conn.request(method, path, **kwargs)
    resp = conn.getresponse()
    data = resp.read()
    conn.close()
    return resp.status, data


def _start_clamav():
    """Start ClamAV container and wait for socket to be ready."""
    if os.path.exists(config.CLAMAV_SOCKET):
        return True  # Already running

    if not os.path.exists(config.DOCKER_SOCKET):
        logger.warning("Docker socket not available, cannot start ClamAV")
        return False

    try:
        status, _ = _docker_api("POST", f"/containers/{config.CLAMAV_CONTAINER}/start")
        if status not in (204, 304):  # 204=started, 304=already running
            logger.warning(f"Failed to start ClamAV container: HTTP {status}")
            return False
        logger.info("Starting ClamAV container, waiting for socket...")
        for _ in range(150):  # Wait up to ~150s for DB to load
            if os.path.exists(config.CLAMAV_SOCKET):
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
        _docker_api("POST", f"/containers/{config.CLAMAV_CONTAINER}/stop")
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
# Direct Download helpers
# =============================================================================
import json
from html.parser import HTMLParser
from urllib.parse import unquote, urljoin, quote

DDL_SOURCES_FILE = os.path.join(config.DATA_DIR, "ddl_sources.json")

# ── Settings persistence ────────────────────────────────────────────────────
SETTINGS_FILE = os.path.join(config.DATA_DIR, "settings.json")

DEFAULT_SETTINGS = {
    "extract_archives": config.EXTRACT_ARCHIVES,
}


def _load_settings():
    if not os.path.exists(SETTINGS_FILE):
        return dict(DEFAULT_SETTINGS)
    try:
        with open(SETTINGS_FILE) as f:
            saved = json.load(f)
        return {**DEFAULT_SETTINGS, **saved}
    except Exception:
        return dict(DEFAULT_SETTINGS)


def _save_settings(settings):
    os.makedirs(config.DATA_DIR, exist_ok=True)
    with open(SETTINGS_FILE, "w") as f:
        json.dump(settings, f, indent=2)


# ── Post-download helpers ───────────────────────────────────────────────────
def extract_archives(directory):
    """Extract RAR/ZIP/7z archives in a directory (recursive). Returns list of extracted archive paths."""
    import subprocess
    import glob as _glob
    extracted = []
    for pattern in ("*.rar", "*.RAR", "*.zip", "*.ZIP", "*.7z"):
        for archive in _glob.glob(os.path.join(directory, pattern)):
            extract_dir = archive + ".extracted"
            if os.path.isdir(extract_dir):
                continue
            os.makedirs(extract_dir, exist_ok=True)
            ext = archive.lower().rsplit(".", 1)[-1]
            try:
                if ext == "rar":
                    subprocess.run(
                        ["unrar", "x", "-o+", "-y", archive, extract_dir + "/"],
                        capture_output=True, timeout=3600, check=True,
                    )
                else:
                    subprocess.run(
                        ["7z", "x", f"-o{extract_dir}", "-y", archive],
                        capture_output=True, timeout=3600, check=True,
                    )
                extracted.append(archive)
                logger.info(f"Extracted: {os.path.basename(archive)}")
            except Exception as e:
                logger.warning(f"Extraction failed for {os.path.basename(archive)}: {e}")
                shutil.rmtree(extract_dir, ignore_errors=True)
    for entry in os.listdir(directory):
        subdir = os.path.join(directory, entry)
        if os.path.isdir(subdir) and not subdir.endswith(".extracted"):
            extracted.extend(extract_archives(subdir))
    return extracted


def write_metadata_sidecar(dest_path, title, platform, platform_slug, is_pc, source_type="torrent"):
    """Write a .gamarr.json metadata sidecar next to the organized file/dir."""
    meta = {
        "title": title,
        "platform": platform or "",
        "platform_slug": platform_slug or "",
        "is_pc": is_pc,
        "source": source_type,
        "organized_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    if os.path.isdir(dest_path):
        sidecar = os.path.join(dest_path, ".gamarr.json")
    else:
        sidecar = dest_path + ".gamarr.json"
    try:
        with open(sidecar, "w") as f:
            json.dump(meta, f, indent=2)
    except Exception as e:
        logger.warning(f"Failed to write metadata sidecar: {e}")


def download_ddl(url, dest_path, job_id):
    """Download a file via direct HTTP with progress tracking."""
    try:
        resp = requests.get(url, stream=True, timeout=30,
                          headers={"User-Agent": "Gamarr/1.0"})
        resp.raise_for_status()
        total = int(resp.headers.get("Content-Length", 0))

        # Get filename from Content-Disposition or URL.
        # Use os.path.basename to strip any path traversal sequences
        # (e.g. Content-Disposition: filename="../../etc/cron.d/evil").
        cd = resp.headers.get("Content-Disposition", "")
        fname_match = re.search(r'filename="?([^";\n]+)"?', cd)
        if fname_match:
            filename = os.path.basename(fname_match.group(1).strip())
        else:
            filename = os.path.basename(unquote(url.split("/")[-1].split("?")[0]))
        if not filename:
            filename = "downloaded_file"

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
        resp = session.get(game_url, headers={"User-Agent": ua}, timeout=15)
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
                    stream=True, timeout=60,
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
        # Strip path components to prevent Content-Disposition path traversal
        filename = os.path.basename(fname_match.group(1).strip()) if fname_match else f"{game_id}.7z"
        if not filename:
            filename = f"{game_id}.7z"
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
    sources_file = os.path.join(config.DATA_DIR, "ddl_sources.json")
    if not os.path.exists(sources_file):
        return []
    try:
        with open(sources_file) as f:
            return json.load(f)
    except Exception:
        return []


def _save_ddl_sources(custom_sources):
    sources_file = os.path.join(config.DATA_DIR, "ddl_sources.json")
    os.makedirs(config.DATA_DIR, exist_ok=True)
    with open(sources_file, "w") as f:
        json.dump(custom_sources, f, indent=2)


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
            download_jobs[job_id]["completed_at"] = time.time()
            download_jobs[job_id]["detail"] = "Moved to GameVault"
            write_metadata_sidecar(dest, torrent_name, platform, platform_slug, is_pc, "torrent")
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
            download_jobs[job_id]["completed_at"] = time.time()
            download_jobs[job_id]["detail"] = f"Moved to RomM ({platform})"
            write_metadata_sidecar(dest, torrent_name, platform, platform_slug, is_pc, "torrent")
            logger.info(f"ROM organized: {torrent_name} -> {dest}")

            # Experimental: extract archives in ROM downloads
            settings = _load_settings()
            if settings.get("extract_archives"):
                try:
                    target = dest if os.path.isdir(dest) else os.path.dirname(dest)
                    extracted = extract_archives(target)
                    if extracted:
                        download_jobs[job_id]["detail"] += f" (extracted {len(extracted)} archive(s))"
                        logger.info(f"Extracted {len(extracted)} archive(s) for {torrent_name}")
                except Exception as e:
                    logger.warning(f"Post-download extraction failed for {torrent_name}: {e}")

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

    from concurrent.futures import ThreadPoolExecutor, as_completed
    all_results = []
    enabled = sources.get_enabled_sources()

    with ThreadPoolExecutor(max_workers=max(len(enabled), 1)) as executor:
        futures = {
            executor.submit(s.search, query, platform if platform != "all" else None): s
            for s in enabled
        }
        for future in as_completed(futures, timeout=30):
            src = futures[future]
            try:
                res = future.result()
                all_results.extend(res)
            except Exception as e:
                logger.error(f"Source {src.name} search error: {e}")

    # Filter torrent results
    torrent_results = filter_game_results(
        [r for r in all_results if r.get("source_type") == "torrent"], query
    )
    ddl_results = [r for r in all_results if r.get("source_type") == "ddl"]
    results = torrent_results + ddl_results

    results.sort(
        key=lambda r: (r.get("seeders", 0) * 10 if r.get("source_type") == "torrent" else 5),
        reverse=True,
    )
    elapsed = int((time.time() - start) * 1000)
    return jsonify({
        "results": results,
        "search_time_ms": elapsed,
        "sources": sources.get_source_metadata(),
    })


@app.route("/api/sources")
def api_sources():
    return jsonify({"sources": sources.get_source_metadata()})


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
            download_jobs[job_id]["completed_at"] = time.time()
            download_jobs[job_id]["detail"] = "Moved to GameVault"
            write_metadata_sidecar(dest, title, platform, platform_slug, is_pc, "ddl")
            logger.info(f"DDL PC game: {filename} -> {dest}")
        elif platform_slug:
            dest_dir = os.path.join(config.GAMES_ROMS_PATH, platform_slug)
            os.makedirs(dest_dir, exist_ok=True)
            dest = os.path.join(dest_dir, filename)
            shutil.move(filepath, dest)
            download_jobs[job_id]["status"] = "completed"
            download_jobs[job_id]["completed_at"] = time.time()
            download_jobs[job_id]["detail"] = f"Moved to RomM ({platform})"
            write_metadata_sidecar(dest, title, platform, platform_slug, is_pc, "ddl")
            logger.info(f"DDL ROM: {filename} -> {dest}")

            # Experimental: extract archives in ROM downloads
            settings = _load_settings()
            if settings.get("extract_archives"):
                try:
                    target = dest if os.path.isdir(dest) else os.path.dirname(dest)
                    extracted = extract_archives(target)
                    if extracted:
                        download_jobs[job_id]["detail"] += f" (extracted {len(extracted)} archive(s))"
                        logger.info(f"Extracted {len(extracted)} archive(s) for {filename}")
                except Exception as e:
                    logger.warning(f"Post-download extraction failed for {filename}: {e}")
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
    from sources.myrient import MYRIENT_BASE, MYRIENT_PLATFORMS
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
    custom_sources = _load_ddl_sources()
    custom_sources.append({"name": name, "url": url, "type": "custom"})
    _save_ddl_sources(custom_sources)
    return jsonify({"success": True})


@app.route("/api/ddl-sources/<int:idx>", methods=["DELETE"])
def api_delete_ddl_source(idx):
    """Delete a custom DDL source."""
    custom_sources = _load_ddl_sources()
    if 0 <= idx < len(custom_sources):
        custom_sources.pop(idx)
        _save_ddl_sources(custom_sources)
        return jsonify({"success": True})
    return jsonify({"success": False, "error": "Not found"}), 404


@app.route("/api/settings", methods=["GET"])
def api_get_settings():
    return jsonify(_load_settings())


@app.route("/api/settings", methods=["PUT"])
def api_update_settings():
    data = request.json or {}
    settings = _load_settings()
    for key in DEFAULT_SETTINGS:
        if key in data:
            settings[key] = data[key]
    _save_settings(settings)
    return jsonify(settings)


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

            # Skip torrents already tracked by a persistent job (avoids duplicates after restart)
            already_tracked = any(
                job.get("title", "").lower() in t_name.lower()
                or t_name.lower() in job.get("title", "").lower()
                for _, job in download_jobs.items()
            )
            if already_tracked:
                logger.debug(f"Orphan recovery: skipping already-tracked torrent: {t_name}")
                continue

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



# =============================================================================
# Collection Stats
# =============================================================================

@app.route("/api/stats")
def api_stats():
    """Collection statistics: completed games by platform, recent additions."""
    if download_jobs is None:
        return jsonify({"platforms": {}, "total_completed": 0, "total_jobs": 0, "recent": [], "by_status": {}})

    jobs = list(download_jobs.items())
    by_platform = {}
    by_status = {}
    recent_completed = []

    for _, job in jobs:
        status = job.get("status", "unknown")
        by_status[status] = by_status.get(status, 0) + 1

        if status in ("completed", "organized"):
            slug = job.get("platform_slug") or job.get("platform") or "unknown"
            by_platform[slug] = by_platform.get(slug, 0) + 1
            recent_completed.append({
                "title": job.get("title", ""),
                "platform": job.get("platform", slug),
                "platform_slug": slug,
                "completed_at": job.get("completed_at"),
            })

    sorted_platforms = dict(sorted(by_platform.items(), key=lambda x: -x[1]))
    recent_completed.sort(key=lambda x: x.get("completed_at") or 0, reverse=True)

    return jsonify({
        "platforms": sorted_platforms,
        "by_status": by_status,
        "total_completed": sum(by_platform.values()),
        "total_jobs": len(jobs),
        "recent": recent_completed[:10],
    })


# =============================================================================
# Health endpoint
# =============================================================================

@app.route("/api/health")
def api_health():
    return jsonify({"status": "ok", "version": "1.0.0"})


# =============================================================================
# AI Monitor helpers
# =============================================================================

def _clear_myrient_cache():
    """Clear the Myrient directory listing cache."""
    from sources import myrient as _myrient_src
    _myrient_src._dir_cache.clear()
    _myrient_src._dir_cache_time.clear()
    logger.info("Myrient directory cache cleared by AI monitor")


# =============================================================================
# AI Monitor API
# =============================================================================

@app.route("/api/monitor/status")
def api_monitor_status():
    if _monitor is None:
        return jsonify({"enabled": False, "error": "Monitor not initialized"})
    return jsonify(_monitor.get_status())


@app.route("/api/monitor/analyze", methods=["POST"])
def api_monitor_analyze():
    if _monitor is None:
        return jsonify({"success": False, "error": "Monitor not initialized"})
    _monitor.trigger_manual()
    import time as _time; _time.sleep(0.1)
    return jsonify({"success": True, **_monitor.get_status()})


@app.route("/api/monitor/actions/<action_id>/approve", methods=["POST"])
def api_monitor_approve(action_id):
    if _monitor is None:
        return jsonify({"success": False, "error": "Monitor not initialized"})
    success, message = _monitor.execute_approved(action_id)
    return jsonify({"success": success, "message": message})


@app.route("/api/monitor/actions/<action_id>/dismiss", methods=["POST"])
def api_monitor_dismiss(action_id):
    if _monitor is None:
        return jsonify({"success": False, "error": "Monitor not initialized"})
    dismissed = _monitor.action_queue.dismiss(action_id)
    return jsonify({"success": dismissed})


if __name__ == "__main__":
    # Ensure all required directories exist
    os.makedirs(config.DATA_DIR, exist_ok=True)
    os.makedirs(config.QB_SAVE_PATH, exist_ok=True)
    os.makedirs(config.GAMES_VAULT_PATH, exist_ok=True)
    os.makedirs(config.GAMES_ROMS_PATH, exist_ok=True)

    # Initialise persistent job store
    download_jobs = JobStore(os.path.join(config.DATA_DIR, "gamarr.db"))

    logger.info("Gamarr starting on port 5001")
    # Recover any orphaned torrents from before restart
    threading.Thread(target=recover_orphaned_torrents, daemon=True).start()

    # Initialize and start AI monitor
    _monitor = monitor.GamarrMonitor(
        get_jobs=lambda: list(download_jobs.items()),
        qb_reauth=lambda: qb.login(),
        clear_myrient_cache=_clear_myrient_cache,
        run_orphan_recovery=recover_orphaned_torrents,
        docker_socket=config.DOCKER_SOCKET,
    )
    _monitor.start()

    app.run(host="0.0.0.0", port=5001, debug=False)
