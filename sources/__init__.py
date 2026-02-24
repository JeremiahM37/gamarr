"""Auto-discover and load GameSource subclasses from this directory."""
import importlib
import inspect
import logging
import os

from .base import GameSource

logger = logging.getLogger("gamarr.sources")
_sources: list = []


def _load_sources():
    global _sources
    _sources = []
    src_dir = os.path.dirname(__file__)
    for fname in sorted(os.listdir(src_dir)):
        if not fname.endswith(".py") or fname.startswith("_"):
            continue
        mod_name = f"sources.{fname[:-3]}"
        try:
            mod = importlib.import_module(mod_name)
            for _, cls in inspect.getmembers(mod, inspect.isclass):
                if (issubclass(cls, GameSource) and cls is not GameSource
                        and cls.__module__ == mod_name):
                    _sources.append(cls())
                    logger.info(f"Loaded source: {cls.name}")
        except Exception as e:
            logger.error(f"Failed to load source from {fname}: {e}")


def get_enabled_sources():
    return [s for s in _sources if s.enabled()]


def get_source_metadata():
    return [
        {
            "name": s.name,
            "label": s.label,
            "color": s.color,
            "source_type": s.source_type,
            "enabled": s.enabled(),
            "config_fields": s.config_fields,
        }
        for s in _sources
    ]


_load_sources()
