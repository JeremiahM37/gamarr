"""
Tests for filtering/safety functions in app.py and platform helpers in platforms.py.
"""
import json
import os
import sys
import tempfile

import pytest

# ── Environment setup (must happen before app import) ────────────────────────
_tmp = tempfile.mkdtemp()
os.environ.setdefault("DATA_DIR", _tmp)
os.environ.setdefault("QB_SAVE_PATH", os.path.join(_tmp, "incoming"))
os.environ.setdefault("GAMES_VAULT_PATH", os.path.join(_tmp, "vault"))
os.environ.setdefault("GAMES_ROMS_PATH", os.path.join(_tmp, "roms"))
os.environ.setdefault("AI_MONITOR_ENABLED", "false")
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import app
from app import (
    _safety_score,
    filter_game_results,
    _is_non_english,
    _title_relevant,
    scan_torrent_file_list,
)
from platforms import (
    ALL_GAME_CATEGORIES,
    detect_platform,
    get_categories_for_platform,
    _detect_platform_from_files,
    _detect_platform_from_metadata,
)


# =============================================================================
# _safety_score
# =============================================================================

def test_safety_score_trusted_group_gives_bonus():
    result = {"title": "FitGirl Repack - Some Game", "seeders": 50, "size": 5_000_000_000, "age": 10}
    score, warnings = _safety_score(result)
    # 50 base + 5 (5-19 seeders) + 20 (trusted group) = 75; size OK
    assert score >= 70


def test_safety_score_many_seeders_gives_bonus():
    result = {"title": "Some Neutral Game v1.0", "seeders": 200, "size": 5_000_000_000, "age": 10}
    score, warnings = _safety_score(result)
    # 50 base + 30 (>=100 seeders) = 80; no penalties
    assert score > 60


def test_safety_score_zero_seeders_penalizes():
    result = {"title": "Some Game", "seeders": 0, "size": 5_000_000_000, "age": 10}
    score, warnings = _safety_score(result)
    # 50 base - 20 (<=1 seeders) = 30
    assert score < 50
    assert any("Very few seeders" in w for w in warnings)


def test_safety_score_tiny_file_penalizes():
    result = {"title": "Some Game", "seeders": 50, "size": 500_000, "age": 10}
    score, warnings = _safety_score(result)
    # size < 10MB triggers penalty
    assert any("Suspiciously small file" in w for w in warnings)


def test_safety_score_risky_extension_in_title():
    # .ps1 is in _RISKY_EXTENSIONS → -40 penalty
    # seeders=3 → +5 (>=5 bracket skipped, <=1 skipped, 2-4 no bonus/penalty)
    # 50 base + 0 seeder adj - 40 risky = 10
    result = {"title": "game_installer.ps1", "seeders": 3, "size": 5_000_000_000, "age": 10}
    score, warnings = _safety_score(result)
    assert score <= 20


def test_safety_score_double_extension():
    # title ends in .iso.exe — matches double extension with exe → -40
    # seeders=3 → no seeder bonus/penalty; 50 - 40 = 10
    result = {"title": "game.iso.exe", "seeders": 3, "size": 5_000_000_000, "age": 10}
    score, warnings = _safety_score(result)
    assert any("Double file extension" in w for w in warnings)
    assert score <= 20


# =============================================================================
# _is_non_english
# =============================================================================

def test_is_non_english_cyrillic_text():
    # Cyrillic chars → True
    assert _is_non_english("Игра на русском") is True


def test_is_non_english_russian_tag():
    assert _is_non_english("Super Mario (RUS)") is True


def test_is_non_english_fitgirl_repack_exception():
    # FitGirl is in _NON_ENGLISH_TAGS regex but the exception fires → False
    assert _is_non_english("FitGirl Repack") is False


def test_is_non_english_usa_region_code():
    # (USA) triggers the USA/EN exception → False
    assert _is_non_english("Super Mario (USA)") is False


def test_is_non_english_english_game():
    assert _is_non_english("Super Mario Bros") is False


# =============================================================================
# _title_relevant
# =============================================================================

def test_title_relevant_matching_word():
    assert _title_relevant("dune", "Dune 2049 PC Game") is True


def test_title_relevant_no_match():
    assert _title_relevant("dune", "Call of Duty PC Game") is False


def test_title_relevant_stopwords_only():
    # "the" is a stopword; after removing stopwords q_words is empty → False
    assert _title_relevant("the", "of the") is False


# =============================================================================
# filter_game_results
# =============================================================================

def _good_result(title="Good Game", seeders=50, size=5_000_000_000):
    return {"title": title, "seeders": seeders, "size": size, "age": 10}


def test_filter_removes_suspicious_title():
    results = [_good_result(title="Best Game keygen download")]
    assert filter_game_results(results, "Best Game") == []


def test_filter_removes_zero_seeders():
    results = [_good_result(seeders=0)]
    assert filter_game_results(results, "Good Game") == []


def test_filter_removes_non_english():
    results = [_good_result(title="Игра для PS2")]
    assert filter_game_results(results, "Игра") == []


def test_filter_removes_tiny_files():
    # size < 1_000_000 is filtered
    results = [_good_result(size=500_000)]
    assert filter_game_results(results, "Good Game") == []


def test_filter_deduplicates_keeps_more_seeders():
    r1 = _good_result(title="Awesome Game Repack", seeders=10)
    r2 = _good_result(title="Awesome Game Repack", seeders=100)
    # Both normalize to the same string; the one with more seeders should survive
    out = filter_game_results([r1, r2], "Awesome Game")
    assert len(out) == 1
    assert out[0]["seeders"] == 100


def test_filter_passes_good_result():
    results = [_good_result()]
    out = filter_game_results(results, "Good Game")
    assert len(out) == 1
    assert out[0]["title"] == "Good Game"


# =============================================================================
# scan_torrent_file_list  (monkeypatching app.qb)
# =============================================================================

class _FakeQB:
    def __init__(self, files):
        self._files = files

    def get_torrent_files(self, torrent_hash):
        return self._files


def test_scan_torrent_file_list_dangerous_extension(monkeypatch):
    monkeypatch.setattr(app, "qb", _FakeQB([{"name": "setup.bat", "progress": 1.0}]))
    is_safe, issues = scan_torrent_file_list("deadbeef")
    assert is_safe is False
    assert len(issues) > 0


def test_scan_torrent_file_list_double_extension_exe(monkeypatch):
    monkeypatch.setattr(app, "qb", _FakeQB([{"name": "game.nfo.exe", "progress": 0.5}]))
    is_safe, issues = scan_torrent_file_list("deadbeef")
    assert is_safe is False
    assert any("Double extension" in i for i in issues)


def test_scan_torrent_file_list_safe_rom(monkeypatch):
    monkeypatch.setattr(app, "qb", _FakeQB([{"name": "game.nsp", "progress": 1.0}]))
    is_safe, issues = scan_torrent_file_list("deadbeef")
    assert is_safe is True
    assert issues == []


def test_scan_torrent_file_list_all_exe_small(monkeypatch):
    # Single .exe, no non-exe files, <=2 total files → "only executables" warning
    monkeypatch.setattr(app, "qb", _FakeQB([{"name": "malware.exe", "progress": 0.5}]))
    is_safe, issues = scan_torrent_file_list("deadbeef")
    assert is_safe is False


# =============================================================================
# detect_platform
# =============================================================================

def test_detect_platform_by_category_id_switch_4050():
    result = detect_platform([{"id": 4050}])
    assert result["slug"] == "switch"


def test_detect_platform_by_category_id_ps2():
    result = detect_platform([{"id": 100011}])
    assert result["slug"] == "ps2"


def test_detect_platform_unknown():
    result = detect_platform([{"id": 99999}])
    assert result["slug"] is None
    assert result["name"] == "Unknown"


# =============================================================================
# get_categories_for_platform
# =============================================================================

def test_get_categories_switch_returns_both_ids():
    cats = get_categories_for_platform("switch")
    assert 100082 in cats
    assert 4050 in cats


def test_get_categories_pc_returns_pc_ids():
    cats = get_categories_for_platform("pc")
    assert cats == [4000, 100010]


def test_get_categories_unknown_platform_returns_all():
    # "wiiu" is in EXTRA_PLATFORMS but not in PLATFORM_MAP → falls back to ALL_GAME_CATEGORIES
    cats = get_categories_for_platform("wiiu")
    assert cats == ALL_GAME_CATEGORIES
    assert len(cats) > 0


# =============================================================================
# _detect_platform_from_files
# =============================================================================

def test_detect_from_files_nsp_extension(tmp_path):
    rom = tmp_path / "game.nsp"
    rom.write_bytes(b"rom")
    result = _detect_platform_from_files(str(rom), "some title")
    assert result == ("Switch", "switch", False)


def test_detect_from_files_title_keyword_switch(tmp_path):
    # tmp_path is an empty directory — no matching extensions
    result = _detect_platform_from_files(str(tmp_path), "Zelda [NSP]")
    assert result == ("Switch", "switch", False)


def test_detect_from_files_unknown(tmp_path):
    readme = tmp_path / "readme.txt"
    readme.write_text("nothing useful")
    result = _detect_platform_from_files(str(tmp_path), "Some Ambiguous Title")
    assert result is None


# =============================================================================
# _detect_platform_from_metadata
# =============================================================================

def test_detect_from_metadata_switch(tmp_path):
    meta = {"platform": "nintendo switch", "title": "Zelda"}
    (tmp_path / "metadata.json").write_text(json.dumps(meta))
    result = _detect_platform_from_metadata(str(tmp_path))
    assert result == ("Switch", "switch", False)


def test_detect_from_metadata_no_file(tmp_path):
    result = _detect_platform_from_metadata(str(tmp_path))
    assert result == (None, None, None)
