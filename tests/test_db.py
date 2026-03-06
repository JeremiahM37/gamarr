"""
Tests for JobStore and _PersistentJob in db.py.
"""
import json
import os
import sqlite3
import sys
import time

import pytest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from db import JobStore, _PersistentJob


def _make_store(tmp_path):
    db_path = str(tmp_path / "jobs" / "test.db")
    return JobStore(db_path), db_path


# ---------------------------------------------------------------------------
# 1. Persistence across instances
# ---------------------------------------------------------------------------

def test_jobstore_persists_across_instances(tmp_path):
    store1, db_path = _make_store(tmp_path)
    store1["abc"] = {"status": "completed", "title": "Zelda", "platform": "Switch"}

    store2 = JobStore(db_path)
    assert "abc" in store2
    job = store2["abc"]
    assert job["status"] == "completed"
    assert job["title"] == "Zelda"
    assert job["platform"] == "Switch"


# ---------------------------------------------------------------------------
# 2. In-progress jobs are marked interrupted on restart
# ---------------------------------------------------------------------------

def test_jobstore_marks_in_progress_as_interrupted_on_restart(tmp_path):
    store1, db_path = _make_store(tmp_path)
    store1["xyz"] = {
        "status": "downloading",
        "title": "Mario",
        "platform": "Switch",
        "error": None,
    }

    store2 = JobStore(db_path)
    assert "xyz" in store2
    job = store2["xyz"]
    assert job["status"] == "interrupted"
    assert job["error"] == "Interrupted by restart"


# ---------------------------------------------------------------------------
# 3. Completed jobs survive restart unchanged
# ---------------------------------------------------------------------------

def test_jobstore_completed_job_survives_restart_unchanged(tmp_path):
    store1, db_path = _make_store(tmp_path)
    store1["done1"] = {"status": "completed", "title": "Metroid", "platform": "Switch"}

    store2 = JobStore(db_path)
    assert "done1" in store2
    assert store2["done1"]["status"] == "completed"
    assert store2["done1"]["title"] == "Metroid"


# ---------------------------------------------------------------------------
# 4. _PersistentJob writes through on key assignment
# ---------------------------------------------------------------------------

def test_persistent_job_writes_through(tmp_path):
    store1, db_path = _make_store(tmp_path)
    store1["wt1"] = {"status": "completed", "title": "Kirby", "platform": "Switch"}

    # Mutate via store1's cached job
    store1["wt1"]["title"] = "Kirby Updated"

    # Open a fresh store — should see the written-through value
    store2 = JobStore(db_path)
    assert store2["wt1"]["title"] == "Kirby Updated"


# ---------------------------------------------------------------------------
# 5. Delete removes job
# ---------------------------------------------------------------------------

def test_jobstore_delete(tmp_path):
    store1, db_path = _make_store(tmp_path)
    store1["del1"] = {"status": "completed", "title": "Sonic", "platform": "Genesis"}

    assert "del1" in store1
    del store1["del1"]

    assert "del1" not in store1
    assert store1.items() == []


# ---------------------------------------------------------------------------
# 6. __contains__ works for present and missing keys
# ---------------------------------------------------------------------------

def test_jobstore_contains(tmp_path):
    store1, db_path = _make_store(tmp_path)
    store1["present"] = {"status": "completed", "title": "Link", "platform": "N64"}

    assert "present" in store1
    assert "absent" not in store1


# ---------------------------------------------------------------------------
# 7. cleanup() removes old finished jobs
# ---------------------------------------------------------------------------

def test_jobstore_cleanup_removes_old_finished_jobs(tmp_path):
    store1, db_path = _make_store(tmp_path)
    store1["old1"] = {"status": "completed", "title": "Old Game", "platform": "PS2"}

    # Backdating updated_at in SQLite to 40 days ago
    old_ts = time.time() - 40 * 86400
    conn = sqlite3.connect(db_path)
    conn.execute("UPDATE jobs SET updated_at = ? WHERE job_id = ?", (old_ts, "old1"))
    conn.commit()
    conn.close()

    deleted = store1.cleanup(days=30)

    assert deleted == 1
    assert "old1" not in store1


# ---------------------------------------------------------------------------
# 8. cleanup() keeps recent finished jobs
# ---------------------------------------------------------------------------

def test_jobstore_cleanup_keeps_recent_jobs(tmp_path):
    store1, db_path = _make_store(tmp_path)
    store1["recent1"] = {"status": "completed", "title": "New Game", "platform": "Switch"}

    # updated_at is fresh (just written) — well within 30 days
    deleted = store1.cleanup(days=30)

    assert deleted == 0
    assert "recent1" in store1


# ---------------------------------------------------------------------------
# 9. get() returns default for missing keys
# ---------------------------------------------------------------------------

def test_jobstore_get_default(tmp_path):
    store1, db_path = _make_store(tmp_path)

    result = store1.get("nonexistent", "fallback")
    assert result == "fallback"


# ---------------------------------------------------------------------------
# 10. values() returns all jobs
# ---------------------------------------------------------------------------

def test_jobstore_values(tmp_path):
    store1, db_path = _make_store(tmp_path)
    store1["v1"] = {"status": "completed", "title": "Game A", "platform": "PS2"}
    store1["v2"] = {"status": "completed", "title": "Game B", "platform": "PS2"}

    vals = store1.values()
    assert len(vals) == 2
