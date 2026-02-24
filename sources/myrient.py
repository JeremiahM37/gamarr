"""Myrient direct-download source for Gamarr."""
import logging
import re
import time
from html.parser import HTMLParser
from urllib.parse import unquote, urljoin

import requests

from .base import GameSource

logger = logging.getLogger("gamarr.sources.myrient")

# ---------------------------------------------------------------------------
# Myrient constants
# ---------------------------------------------------------------------------
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

# Directory listing cache: platform_slug -> [(filename, url)]
_dir_cache: dict = {}
_dir_cache_time: dict = {}
DIR_CACHE_TTL = 3600  # 1 hour

_STOPWORDS = {"the", "a", "an", "of", "in", "on", "at", "to", "for", "and", "or", "is", "it", "by"}


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
                if (name == "href" and val
                        and not val.startswith("?")
                        and not val.startswith("/")
                        and val != "../"):
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
        files = [
            (unquote(name), urljoin(url, href))
            for name, href in parser.files
            if not name.endswith("/")
        ]
        _dir_cache[platform_slug] = files
        _dir_cache_time[platform_slug] = now
        logger.info(f"Myrient: cached {len(files)} files for {platform_slug}")
        return files
    except Exception as e:
        logger.warning(f"Myrient listing error for {platform_slug}: {e}")
        return []


class MyrientSource(GameSource):
    name = "myrient"
    label = "Myrient"
    color = "#10b981"
    source_type = "ddl"

    config_fields = []

    def enabled(self) -> bool:
        return True

    def search(self, query: str, platform_slug: str = None) -> list:
        from platforms import PLATFORM_MAP

        results = []
        q_words = set(re.findall(r"\w+", query.lower())) - _STOPWORDS

        if platform_slug and platform_slug not in MYRIENT_PLATFORMS:
            return []  # Platform not supported by Myrient

        slugs = [platform_slug] if platform_slug else list(MYRIENT_PLATFORMS.keys())
        # Require a platform filter — searching all is too slow
        if len(slugs) > 3 and not platform_slug:
            return []

        for slug in slugs:
            files = _get_myrient_listing(slug)
            for filename, url in files:
                f_words = set(re.findall(r"\w+", filename.lower())) - _STOPWORDS
                overlap = len(q_words & f_words)
                if overlap < max(1, len(q_words) - 1) or overlap < 1:
                    continue
                # Skip non-English regions
                if (re.search(
                        r"\(Japan\)|\(Korea\)|\(China\)|\(Taiwan\)|\(France\)|\(Germany\)|\(Spain\)|\(Italy\)",
                        filename)
                        and not re.search(r"\(USA|World|Europe|En\)", filename)):
                    continue
                plat_name, _, is_pc = PLATFORM_MAP.get(
                    next((k for k, v in PLATFORM_MAP.items() if v[1] == slug), 0),
                    ("Unknown", slug, False),
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
