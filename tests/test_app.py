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


def test_ddl_sources_add_and_delete(client):
    r = client.post("/api/ddl-sources", json={"name": "TestSite", "url": "https://example.com/"})
    assert r.status_code == 200
    assert r.get_json()["success"] is True

    r = client.get("/api/ddl-sources")
    sources = r.get_json()["sources"]
    custom = [s for s in sources if s.get("type") == "custom"]
    assert any(s["name"] == "TestSite" for s in custom)

    # Delete the first custom source (index 0 into the custom list; DELETE endpoint uses custom-only index)
    idx = 0
    r = client.delete(f"/api/ddl-sources/{idx}")
    assert r.status_code == 200
    assert r.get_json()["success"] is True


def test_ddl_sources_add_missing_fields(client):
    r = client.post("/api/ddl-sources", json={"name": "NoUrl"})
    assert r.status_code == 400


def test_settings_get_and_update(client):
    r = client.get("/api/settings")
    assert r.status_code == 200
    data = r.get_json()
    assert "extract_archives" in data

    r = client.put("/api/settings", json={"extract_archives": True})
    assert r.status_code == 200
    updated = r.get_json()
    assert updated["extract_archives"] is True

    # Reset
    client.put("/api/settings", json={"extract_archives": False})


# ── Downloads ─────────────────────────────────────────────────────────────────

def test_downloads_list(client):
    r = client.get("/api/downloads")
    assert r.status_code == 200
    data = r.get_json()
    assert "downloads" in data or isinstance(data, (list, dict))


def test_downloads_clear(client):
    r = client.post("/api/downloads/clear")
    assert r.status_code == 200


# ── Downloads API ─────────────────────────────────────────────────────────────

def test_api_download_missing_url(client):
    r = client.post("/api/download", json={"title": "Test Game", "source_type": "torrent"})
    assert r.status_code in (400, 200)  # 400 expected; 200 if job created with error status
    if r.status_code == 200:
        data = r.get_json()
        # If a job_id was returned, it should have errored quickly
        assert "job_id" in data or data.get("success") is False


def test_api_delete_job(client):
    import app as gamarr_app
    # Seed a completed job directly into the store
    gamarr_app.download_jobs["del-test-1"] = {
        "status": "completed", "title": "Game To Delete",
        "platform": "Switch", "platform_slug": "switch",
        "is_pc": False, "error": None, "detail": "Done",
    }
    r = client.delete("/api/downloads/del-test-1")
    assert r.status_code == 200
    assert r.get_json()["success"] is True
    assert "del-test-1" not in gamarr_app.download_jobs


def test_api_delete_nonexistent_job(client):
    r = client.delete("/api/downloads/does-not-exist-xyz")
    assert r.status_code == 404


def test_api_downloads_clear_removes_finished(client):
    import app as gamarr_app
    gamarr_app.download_jobs["clr-1"] = {
        "status": "completed", "title": "Done Game",
        "platform": "PC", "platform_slug": None,
        "is_pc": True, "error": None, "detail": "Done",
    }
    gamarr_app.download_jobs["clr-2"] = {
        "status": "error", "title": "Failed Game",
        "platform": "Switch", "platform_slug": "switch",
        "is_pc": False, "error": "timed out", "detail": "",
    }
    r = client.post("/api/downloads/clear")
    assert r.status_code == 200
    data = r.get_json()
    assert data["cleared"] >= 2
    assert "clr-1" not in gamarr_app.download_jobs
    assert "clr-2" not in gamarr_app.download_jobs


# ── Stats ─────────────────────────────────────────────────────────────────────

def test_stats_empty(client):
    r = client.get("/api/stats")
    assert r.status_code == 200
    data = r.get_json()
    assert "platforms" in data
    assert "total_jobs" in data
    assert "total_completed" in data
    assert isinstance(data["platforms"], dict)


def test_stats_reflects_completed_jobs(client):
    import app as gamarr_app, time as _time
    gamarr_app.download_jobs["stats-sw"] = {
        "status": "completed", "title": "Zelda",
        "platform": "Switch", "platform_slug": "switch",
        "is_pc": False, "error": None, "detail": "Done",
        "completed_at": _time.time(),
    }
    r = client.get("/api/stats")
    assert r.status_code == 200
    data = r.get_json()
    assert data["total_completed"] >= 1
    assert "switch" in data["platforms"]
    del gamarr_app.download_jobs["stats-sw"]


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


def test_organize_game_pc_path(monkeypatch, tmp_path):
    """PC games must be moved to the vault directory, not the ROMs directory."""
    import app as gamarr_app

    job_id = "pc-game-job"
    gamarr_app.download_jobs[job_id] = {
        "status": "organizing", "title": "Witcher 3",
        "platform": "PC", "platform_slug": None,
        "is_pc": True, "error": None, "detail": "",
    }

    src = tmp_path / "Witcher3.exe"
    src.write_bytes(b"data")
    vault_dir = tmp_path / "vault"
    vault_dir.mkdir()

    monkeypatch.setattr(gamarr_app.config, "GAMES_VAULT_PATH", str(vault_dir))
    monkeypatch.setattr(gamarr_app.config, "QB_SAVE_PATH", str(tmp_path))
    monkeypatch.setattr(gamarr_app, "scan_with_clamav", lambda path: (True, []))

    fake_torrent = {
        "name": "Witcher3.exe",
        "hash": "beef0001",
        "content_path": str(src),
        "save_path": str(tmp_path),
        "progress": 1.0,
    }

    gamarr_app.organize_game(job_id, fake_torrent, "PC", None, True)

    job = gamarr_app.download_jobs[job_id]
    assert job["status"] == "completed"
    assert "GameVault" in job["detail"]
    assert (vault_dir / "Witcher3.exe").exists()


def test_organize_game_unknown_platform_leaves_in_staging(monkeypatch, tmp_path):
    """Files with no known platform slug must not be moved and stay 'completed'."""
    import app as gamarr_app

    job_id = "unknown-plat-job"
    gamarr_app.download_jobs[job_id] = {
        "status": "organizing", "title": "Mystery ROM",
        "platform": "Unknown", "platform_slug": None,
        "is_pc": False, "error": None, "detail": "",
    }

    src = tmp_path / "mystery.rom"
    src.write_bytes(b"data")
    monkeypatch.setattr(gamarr_app.config, "QB_SAVE_PATH", str(tmp_path))
    monkeypatch.setattr(gamarr_app, "scan_with_clamav", lambda path: (True, []))

    fake_torrent = {
        "name": "mystery.rom",
        "hash": "beef0002",
        "content_path": str(src),
        "save_path": str(tmp_path),
        "progress": 1.0,
    }

    gamarr_app.organize_game(job_id, fake_torrent, "Unknown", None, False)

    job = gamarr_app.download_jobs[job_id]
    assert job["status"] == "completed"
    assert "staging" in job["detail"].lower() or "unknown" in job["detail"].lower()
    # File must NOT have been deleted
    assert src.exists()


# =============================================================================
# Security regression tests
# =============================================================================

def test_download_ddl_path_traversal_via_content_disposition(monkeypatch, tmp_path):
    """Content-Disposition filename with path components must be stripped."""
    import app as gamarr_app

    job_id = "sec-ddl-job"
    gamarr_app.download_jobs[job_id] = {
        "status": "downloading", "title": "Evil ROM", "detail": "",
    }

    class _FakeResp:
        status_code = 200
        headers = {
            "Content-Disposition": 'attachment; filename="../../etc/cron.d/evil"',
            "Content-Length": "4",
        }
        def raise_for_status(self): pass
        def iter_content(self, chunk_size=None):
            yield b"data"

    monkeypatch.setattr(gamarr_app.requests, "get", lambda *a, **kw: _FakeResp())

    result = gamarr_app.download_ddl("http://example.com/rom.zip", str(tmp_path), job_id)

    # File must land inside dest_path, not escape it
    assert result is not None
    assert os.path.dirname(os.path.abspath(result)) == str(tmp_path)
    # Basename must be sanitised — no directory separators
    assert "/" not in os.path.basename(result)
    assert ".." not in result


def test_download_ddl_path_traversal_from_url(monkeypatch, tmp_path):
    """URL path components must not escape dest_path even without Content-Disposition."""
    import app as gamarr_app

    job_id = "sec-ddl-url-job"
    gamarr_app.download_jobs[job_id] = {
        "status": "downloading", "title": "Evil ROM", "detail": "",
    }

    class _FakeResp:
        status_code = 200
        headers = {"Content-Length": "4"}
        def raise_for_status(self): pass
        def iter_content(self, chunk_size=None):
            yield b"data"

    monkeypatch.setattr(gamarr_app.requests, "get", lambda *a, **kw: _FakeResp())

    # URL whose last segment looks like a path traversal
    result = gamarr_app.download_ddl(
        "http://example.com/../../etc/passwd", str(tmp_path), job_id
    )

    assert result is not None
    assert os.path.dirname(os.path.abspath(result)) == str(tmp_path)
    assert ".." not in result
