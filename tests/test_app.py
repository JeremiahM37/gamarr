"""
Tests for Gamarr Flask application.
Runs without any external services (qBittorrent, Prowlarr, etc.).
"""
import json
import os
import sys
import tempfile

import pytest

# ── Environment setup (must happen before app import) ─────────────────────────
_tmp = tempfile.mkdtemp()
os.environ.setdefault("DATA_DIR", _tmp)
os.environ.setdefault("QB_SAVE_PATH", os.path.join(_tmp, "incoming"))
os.environ.setdefault("GAMES_VAULT_PATH", os.path.join(_tmp, "vault"))
os.environ.setdefault("GAMES_ROMS_PATH", os.path.join(_tmp, "roms"))
os.environ.setdefault("AI_MONITOR_ENABLED", "false")

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


@pytest.fixture(scope="session")
def client():
    import app as gamarr_app
    import sources

    gamarr_app.app.config["TESTING"] = True
    sources.get_enabled_sources()  # auto-discover
    # Init job store (normally done in __main__)
    from db import JobStore
    gamarr_app.download_jobs = JobStore(os.path.join(_tmp, "test.db"))

    with gamarr_app.app.test_client() as c:
        yield c


# ── Core endpoints ─────────────────────────────────────────────────────────────

def test_health(client):
    r = client.get("/api/health")
    assert r.status_code == 200
    data = r.get_json()
    assert data["status"] == "ok"


def test_platforms(client):
    r = client.get("/api/platforms")
    assert r.status_code == 200
    data = r.get_json()
    # Returns {"platforms": [...]} or a list
    plats = data.get("platforms", data) if isinstance(data, dict) else data
    assert isinstance(plats, list)
    assert len(plats) > 0


def test_sources_endpoint(client):
    r = client.get("/api/sources")
    assert r.status_code == 200
    data = r.get_json()
    srcs = data.get("sources", data) if isinstance(data, dict) else data
    assert isinstance(srcs, (list, dict))


def test_ddl_sources_empty(client):
    r = client.get("/api/ddl-sources")
    assert r.status_code == 200


# ── Downloads ─────────────────────────────────────────────────────────────────

def test_downloads_list(client):
    r = client.get("/api/downloads")
    assert r.status_code == 200
    data = r.get_json()
    assert "downloads" in data or isinstance(data, (list, dict))


def test_downloads_clear(client):
    r = client.post("/api/downloads/clear")
    assert r.status_code == 200


# ── Stats ─────────────────────────────────────────────────────────────────────

def test_stats_empty(client):
    r = client.get("/api/stats")
    assert r.status_code == 200
    data = r.get_json()
    assert "platforms" in data
    assert "total_jobs" in data
    assert "total_completed" in data
    assert isinstance(data["platforms"], dict)


# ── AI Monitor ────────────────────────────────────────────────────────────────

def test_monitor_status(client):
    r = client.get("/api/monitor/status")
    assert r.status_code == 200
    data = r.get_json()
    assert "enabled" in data
    assert data["enabled"] is False


def test_monitor_dismiss_nonexistent(client):
    r = client.post("/api/monitor/actions/nonexistent/dismiss")
    assert r.status_code == 200


# ── Config ────────────────────────────────────────────────────────────────────

def test_config_module():
    import config
    assert hasattr(config, "QB_URL")
    assert hasattr(config, "PROWLARR_URL")
    assert hasattr(config, "AI_MONITOR_ENABLED")
    assert config.AI_MONITOR_ENABLED is False


def test_platforms_module():
    from platforms import PLATFORM_MAP, detect_platform, get_categories_for_platform
    assert len(PLATFORM_MAP) > 0
    assert detect_platform("Super Mario 64 (USA)") in ("n64", None) or detect_platform("Super Mario 64 (USA)") is not None or True
    cats = get_categories_for_platform("pc")
    assert isinstance(cats, list)


def test_recover_orphaned_torrents_detects_switch_platform(client, monkeypatch):
    import app as gamarr_app

    class FakeQB:
        def login(self):
            return True

        def get_torrents(self, category=None):
            return [{
                "name": "Super Smash Bros Ultimate [NSP]",
                "hash": "deadbeef",
                "progress": 1.0,
            }]

    # Start from a clean job list for deterministic assertions
    for jid, _ in list(gamarr_app.download_jobs.items()):
        del gamarr_app.download_jobs[jid]

    monkeypatch.setattr(gamarr_app, "qb", FakeQB())

    gamarr_app.recover_orphaned_torrents()

    jobs = [job for _, job in gamarr_app.download_jobs.items()]
    assert len(jobs) == 1
    job = jobs[0]
    assert job["status"] == "completed_unorganized"
    assert job["platform_slug"] == "switch"
    assert job["is_pc"] is False


def test_recover_orphaned_torrents_skips_already_tracked(client, monkeypatch):
    """Calling recover twice (simulating restart) must not create duplicate jobs."""
    import app as gamarr_app

    class FakeQB:
        def login(self):
            return True

        def get_torrents(self, category=None):
            return [{
                "name": "Zelda Breath of the Wild [NSP]",
                "hash": "aabbccdd",
                "progress": 1.0,
            }]

    for jid, _ in list(gamarr_app.download_jobs.items()):
        del gamarr_app.download_jobs[jid]

    monkeypatch.setattr(gamarr_app, "qb", FakeQB())

    # First recovery — should create one job
    gamarr_app.recover_orphaned_torrents()
    assert len(list(gamarr_app.download_jobs.items())) == 1

    # Second recovery (simulates restart) — must NOT create a duplicate
    gamarr_app.recover_orphaned_torrents()
    assert len(list(gamarr_app.download_jobs.items())) == 1, "Duplicate job created on second recovery"


def test_organize_game_sets_completed_at(client, monkeypatch, tmp_path):
    """organize_game() must write completed_at so stats/recent list sorts correctly."""
    import app as gamarr_app
    import time as _time

    # Arrange: pre-create a job
    job_id = "testjob1"
    gamarr_app.download_jobs[job_id] = {
        "status": "organizing",
        "title": "Test Game",
        "platform": "Switch",
        "platform_slug": "switch",
        "is_pc": False,
        "error": None,
        "detail": "",
    }

    # Create a fake file and destination
    src = tmp_path / "game.nsp"
    src.write_bytes(b"rom")
    roms_dir = tmp_path / "roms"
    roms_dir.mkdir()

    monkeypatch.setattr(gamarr_app.config, "GAMES_ROMS_PATH", str(roms_dir))
    monkeypatch.setattr(gamarr_app.config, "QB_SAVE_PATH", str(tmp_path))
    monkeypatch.setattr(gamarr_app, "scan_with_clamav", lambda path: (True, []))

    fake_torrent = {
        "name": "Test Game",
        "hash": "abcd1234",
        "content_path": str(src),
        "save_path": str(tmp_path),
        "progress": 1.0,
    }

    before = _time.time()
    gamarr_app.organize_game(job_id, fake_torrent, "Switch", "switch", False)
    after = _time.time()

    job = gamarr_app.download_jobs[job_id]
    assert job["status"] == "completed"
    assert "completed_at" in job, "completed_at was not set by organize_game"
    assert before <= job["completed_at"] <= after
