"""Prowlarr torrent search source for Gamarr."""
import logging
import os
import re

import requests

from .base import GameSource

logger = logging.getLogger("gamarr.sources.prowlarr")

# ---------------------------------------------------------------------------
# Shared utility helpers (defined locally to avoid circular imports with app.py)
# ---------------------------------------------------------------------------

def _human_size(size_bytes):
    if not size_bytes:
        return "?"
    for unit in ("B", "KB", "MB", "GB"):
        if abs(size_bytes) < 1024:
            return f"{size_bytes:.1f} {unit}"
        size_bytes /= 1024
    return f"{size_bytes:.1f} TB"


_STOPWORDS = {"the", "a", "an", "of", "in", "on", "at", "to", "for", "and", "or", "is", "it", "by"}

_SUSPICIOUS = re.compile(
    r"password|keygen|crack[\s._-]+only|warez|DevCourseWeb|sample\s+only|trailer\s+only"
    r"|\.exe\.torrent|password\.txt|clickbait|survey|free\s+download"
    r"|RAT\b|trojan|malware|crypto[\s._-]*miner|coin[\s._-]*miner",
    re.IGNORECASE,
)

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

_TRUSTED_GROUPS = {
    "fitgirl", "dodi", "gog", "codex", "plaza", "skidrow", "cpy",
    "empress", "rune", "razordox", "tinyiso", "elamigos",
}

_RISKY_EXTENSIONS = re.compile(
    r"\.(scr|bat|cmd|vbs|vbe|js|jse|wsf|wsh|ps1|msi|hta|cpl|reg)$",
    re.IGNORECASE,
)


def _is_non_english(title):
    """Check if a torrent title appears to be non-English."""
    if _CYRILLIC.search(title) or _CJK.search(title):
        return True
    if _NON_ENGLISH_TAGS.search(title):
        if re.search(r"fitgirl", title, re.IGNORECASE):
            return False
        if re.search(r"\b(USA|EN|ENG|English|NTSC|PAL)\b", title, re.IGNORECASE):
            return False
        return True
    return False


def _safety_score(result):
    """Rate result safety: higher = safer. Returns (score, warnings)."""
    score = 50
    warnings = []
    title = result.get("title", "").lower()
    seeders = result.get("seeders", 0)
    size = result.get("size", 0)

    if seeders >= 100:
        score += 30
    elif seeders >= 20:
        score += 15
    elif seeders >= 5:
        score += 5
    elif seeders <= 1:
        score -= 20
        warnings.append("Very few seeders")

    for group in _TRUSTED_GROUPS:
        if group in title:
            score += 20
            break

    if size and size < 10_000_000:
        score -= 30
        warnings.append("Suspiciously small file")

    if _RISKY_EXTENSIONS.search(title):
        score -= 40
        warnings.append("Risky file extension")

    if re.search(r"\.\w{2,4}\.\w{2,4}$", title):
        double_ext = re.search(r"\.(\w{2,4})\.(\w{2,4})$", title)
        if double_ext:
            ext2 = double_ext.group(2).lower()
            if ext2 in ("exe", "scr", "bat", "cmd", "msi"):
                score -= 40
                warnings.append("Double file extension")

    age = result.get("age", 0)
    if age < 1 and seeders < 5:
        score -= 10
        warnings.append("Brand new upload")

    return max(0, min(100, score)), warnings


class ProwlarrSource(GameSource):
    name = "prowlarr"
    label = "Prowlarr"
    color = "#f97316"
    source_type = "torrent"

    config_fields = [
        {"key": "url", "label": "Prowlarr URL", "type": "url", "required": True},
        {"key": "api_key", "label": "API Key", "type": "password", "required": True},
    ]

    def enabled(self) -> bool:
        import config
        return bool(config.PROWLARR_URL and config.PROWLARR_API_KEY)

    def search(self, query: str, platform_slug: str = None) -> list:
        import config
        from platforms import get_categories_for_platform, detect_platform

        results = []
        filter_categories = None
        if platform_slug and platform_slug != "all":
            cats = get_categories_for_platform(platform_slug)
            filter_categories = set(cats)

        indexer_ids = [
            int(x)
            for x in os.getenv("PROWLARR_GAME_INDEXERS", "7,5,15,9,8,3,4").split(",")
            if x.strip()
        ]
        all_items = []
        for indexer_id in indexer_ids:
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
                logger.warning(f"Indexer {indexer_id} error: {e}")

        for item in all_items:
            size = item.get("size", 0)
            cats = item.get("categories", [])
            detected = detect_platform(cats)
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
                "source_type": "torrent",
            })
        return results
