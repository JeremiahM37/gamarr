"""Base class for Gamarr search sources.

To add a new source, create a .py file in this directory and subclass GameSource.
It will be auto-discovered at startup.
"""
import os
from abc import ABC, abstractmethod


class GameSource(ABC):
    """Base class for all game search sources."""

    name: str = ""          # Unique internal ID, e.g. "prowlarr"
    label: str = ""         # Display label, e.g. "Prowlarr"
    color: str = "#888888"  # CSS hex color for the badge in the UI
    source_type: str = "torrent"  # "torrent" or "ddl"

    # Optional settings shown in the UI / stored in env.
    # Each entry: {"key": "api_key", "label": "API Key", "type": "text|password|url", "required": False}
    config_fields: list = []

    def get_config(self, key: str, default: str = "") -> str:
        """Read a source-specific config value from the environment.

        Env var name is: SOURCE_{NAME}_{KEY} (uppercase).
        Example: SOURCE_PROWLARR_API_KEY
        """
        env_key = f"SOURCE_{self.name.upper()}_{key.upper()}"
        return os.getenv(env_key, default)

    def enabled(self) -> bool:
        """Return True if this source is configured and ready to use."""
        return True

    @abstractmethod
    def search(self, query: str, platform_slug: str = None) -> list:
        """Search for games matching query.

        Args:
            query: Free-text search string.
            platform_slug: Optional platform filter, e.g. "switch", "ps2", "nes".
                           Pass None to search all platforms.

        Returns:
            List of result dicts. All results should include at minimum:
                title       (str)  - display title
                source_type (str)  - "torrent" or "ddl"
                platform    (str)  - display name, e.g. "Nintendo Switch"
                platform_slug (str|None) - internal slug for organisation

            Torrent results should also include:
                download_url (str)  - .torrent URL (may be empty)
                magnet_url   (str)  - magnet link (may be empty)
                info_hash    (str)  - 40-char hex hash (may be empty)
                seeders      (int)
                leechers     (int)
                size         (int)  - bytes
                size_human   (str)  - e.g. "4.7 GB"
                indexer      (str)  - indexer name
                age          (int)  - days since upload

            DDL results should also include:
                download_url (str)  - direct HTTP link to the file
                size_human   (str)  - e.g. "512 MB" or "?"
                vimm_id      (str)  - Vimm game ID if source is Vimm

        Raise no exceptions -- catch internally and return [] on failure.
        """
        ...
