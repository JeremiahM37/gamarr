"""Platform data and detection helpers for Gamarr.

All Prowlarr category ID mappings, extra platform definitions, and
platform-detection functions live here so they can be imported from
both app.py and the source plugins without circular dependencies.
"""
import json
import logging
import os
import re

logger = logging.getLogger("gamarr.platforms")

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
    """Detect platform from a list of Prowlarr category dicts or IDs."""
    for cat in categories:
        cat_id = cat.get("id") if isinstance(cat, dict) else cat
        if cat_id in PLATFORM_MAP:
            name, slug, is_pc = PLATFORM_MAP[cat_id]
            return {"name": name, "slug": slug, "is_pc": is_pc}
    return {"name": "Unknown", "slug": None, "is_pc": False}


def get_categories_for_platform(platform_filter):
    """Return all Prowlarr category IDs that match the given platform slug."""
    if platform_filter == "pc":
        return [4000, 100010]
    matches = [cat_id for cat_id, (name, slug, is_pc) in PLATFORM_MAP.items()
               if slug == platform_filter or str(cat_id) == platform_filter]
    if matches:
        return matches
    return ALL_GAME_CATEGORIES


def _detect_platform_from_metadata(content_path):
    """Try to read metadata.json in content folder for platform info.

    Returns (display_name, romm_slug, is_pc) or (None, None, None).
    """
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
            "ps1": ("PS1", "psx", False),
            "psx": ("PS1", "psx", False),
            "playstation": ("PS1", "psx", False),
            "ps2": ("PS2", "ps2", False),
            "playstation 2": ("PS2", "ps2", False),
            "ps3": ("PS3", "ps3", False),
            "playstation 3": ("PS3", "ps3", False),
            "ps4": ("PS4", "ps4", False),
            "psp": ("PSP", "psp", False),
            "xbox": ("Xbox", "xbox", False),
            "xbox 360": ("Xbox 360", "xbox360", False),
            "xbox360": ("Xbox 360", "xbox360", False),
            "ds": ("DS", "nds", False),
            "nds": ("DS", "nds", False),
            "nintendo ds": ("DS", "nds", False),
            "3ds": ("3DS", "3ds", False),
            "nintendo 3ds": ("3DS", "3ds", False),
            "dreamcast": ("Dreamcast", "dc", False),
            "n64": ("Nintendo 64", "n64", False),
            "nintendo 64": ("Nintendo 64", "n64", False),
            "snes": ("SNES", "snes", False),
            "super nintendo": ("SNES", "snes", False),
            "nes": ("NES", "nes", False),
            "gba": ("Game Boy Advance", "gba", False),
            "game boy advance": ("Game Boy Advance", "gba", False),
            "gb": ("Game Boy", "gb", False),
            "game boy": ("Game Boy", "gb", False),
            "genesis": ("Sega Genesis", "genesis", False),
            "sega genesis": ("Sega Genesis", "genesis", False),
            "saturn": ("Sega Saturn", "saturn", False),
            "sega saturn": ("Sega Saturn", "saturn", False),
            "pc": ("PC", None, True),
            "windows": ("PC", None, True),
        }
        if plat in meta_map:
            return meta_map[plat]
        logger.info(f"Unknown metadata platform: {plat}")
        return None, None, None
    except Exception as e:
        logger.warning(f"Failed to read metadata.json: {e}")
        return None, None, None


def _detect_platform_from_files(content_path, title):
    """Detect platform from file extensions and title keywords.

    Returns (display_name, romm_slug, is_pc) or None.
    """
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
