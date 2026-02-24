"""Vimm's Lair direct-download source for Gamarr."""
import logging
import re

import requests
import urllib3

urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)

from .base import GameSource

logger = logging.getLogger("gamarr.sources.vimm")

_VIMM_SYSTEM_MAP = {
    "nes": "NES", "snes": "SNES", "n64": "N64", "ngc": "GameCube",
    "wii": "Wii", "gb": "GB", "gbc": "GBC", "gba": "GBA", "nds": "DS",
    "genesis": "Genesis", "saturn": "Saturn", "dc": "Dreamcast",
    "psx": "PS1", "ps2": "PS2", "ps3": "PS3", "psp": "PSP",
    "xbox": "Xbox", "xbox360": "Xbox360",
}


class VimmSource(GameSource):
    name = "vimm"
    label = "Vimm's Lair"
    color = "#6366f1"
    source_type = "ddl"

    config_fields = []

    def enabled(self) -> bool:
        return True

    def search(self, query: str, platform_slug: str = None) -> list:
        results = []
        reverse_map = {v: k for k, v in _VIMM_SYSTEM_MAP.items()}

        params = {"p": "list", "q": query}
        if platform_slug and platform_slug in _VIMM_SYSTEM_MAP:
            params["system"] = _VIMM_SYSTEM_MAP[platform_slug]

        system_from_filter = _VIMM_SYSTEM_MAP.get(platform_slug, "")

        try:
            resp = requests.get(
                "https://vimm.net/vault/",
                params=params,
                headers={
                    "User-Agent": (
                        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
                        "AppleWebKit/537.36 (KHTML, like Gecko) "
                        "Chrome/120.0.0.0 Safari/537.36"
                    )
                },
                timeout=15,
                verify=False,
            )
            if resp.status_code != 200:
                return []

            # Parse game links: <a href="/vault/{ID}" ...>Game Name</a>
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
                        reverse_map_lower = {v.lower(): k for k, v in _VIMM_SYSTEM_MAP.items()}
                        if sys_name.lower() in reverse_map_lower:
                            slug = reverse_map_lower[sys_name.lower()]
                            system_clean = sys_name

                display_title = game_name
                if system_clean and system_clean != "Unknown" and f"({system_clean})" not in game_name:
                    display_title = f"{game_name} ({system_clean})"

                results.append({
                    "title": display_title,
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
