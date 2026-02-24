"""Persistent SQLite-backed job store for Gamarr.

Replaces the in-memory download_jobs dict so that job state survives
container restarts. The API is dict-like: set, get, delete, iterate.
"""
import json
import logging
import os
import sqlite3
import threading
import time

logger = logging.getLogger("gamarr.db")


class JobStore:
    """SQLite-backed dict-like store for download jobs."""

    def __init__(self, db_path: str):
        self._db_path = db_path
        self._lock = threading.Lock()
        self._cache: dict = {}
        os.makedirs(os.path.dirname(db_path), exist_ok=True)
        self._init_db()
        self._load_all()

    def _connect(self):
        conn = sqlite3.connect(self._db_path, timeout=10)
        conn.execute("PRAGMA journal_mode=WAL")
        return conn

    def _init_db(self):
        with self._connect() as conn:
            conn.execute("""
                CREATE TABLE IF NOT EXISTS jobs (
                    job_id TEXT PRIMARY KEY,
                    data TEXT NOT NULL,
                    created_at REAL DEFAULT (strftime('%s','now')),
                    updated_at REAL DEFAULT (strftime('%s','now'))
                )
            """)

    def _load_all(self):
        with self._connect() as conn:
            rows = conn.execute("SELECT job_id, data FROM jobs").fetchall()
        stale = 0
        for job_id, data_str in rows:
            try:
                data = json.loads(data_str)
            except Exception:
                continue
            # Mark in-progress jobs as interrupted — the threads that were
            # watching them no longer exist after the restart.
            if data.get("status") in ("downloading", "scanning", "organizing"):
                data["status"] = "interrupted"
                data["error"] = "Interrupted by restart"
                stale += 1
            self._cache[job_id] = _PersistentJob(self, job_id, data)
        if self._cache:
            logger.info(
                f"Restored {len(self._cache)} jobs ({stale} marked interrupted)"
            )

    def _persist(self, job_id: str, data: dict):
        with self._lock:
            with self._connect() as conn:
                conn.execute(
                    "INSERT OR REPLACE INTO jobs (job_id, data, updated_at) "
                    "VALUES (?, ?, strftime('%s','now'))",
                    (job_id, json.dumps(data)),
                )

    def _delete(self, job_id: str):
        with self._lock:
            self._cache.pop(job_id, None)
            with self._connect() as conn:
                conn.execute("DELETE FROM jobs WHERE job_id = ?", (job_id,))

    # ------------------------------------------------------------------
    # Dict-like interface
    # ------------------------------------------------------------------

    def __setitem__(self, job_id: str, value: dict):
        job = _PersistentJob(self, job_id, dict(value))
        self._cache[job_id] = job
        self._persist(job_id, job._data)

    def __getitem__(self, job_id: str) -> "_PersistentJob":
        return self._cache[job_id]

    def __contains__(self, job_id: str) -> bool:
        return job_id in self._cache

    def __delitem__(self, job_id: str):
        self._delete(job_id)

    def items(self):
        return list(self._cache.items())

    def values(self):
        return list(self._cache.values())

    def get(self, job_id: str, default=None):
        return self._cache.get(job_id, default)

    # ------------------------------------------------------------------
    # Maintenance
    # ------------------------------------------------------------------

    def cleanup(self, days: int = 30) -> int:
        """Delete finished jobs older than *days* days. Returns count deleted."""
        cutoff = time.time() - days * 86400
        with self._lock:
            with self._connect() as conn:
                deleted = conn.execute(
                    "DELETE FROM jobs "
                    "WHERE updated_at < ? "
                    "AND json_extract(data, '$.status') IN ('completed','error','interrupted')",
                    (cutoff,),
                ).rowcount
        # Evict from in-memory cache as well
        stale_ids = [
            jid for jid, j in list(self._cache.items())
            if j.get("status") in ("completed", "error", "interrupted")
            # Only remove cache entries that are actually gone from DB
        ]
        # Re-check which ones were deleted from DB
        with self._connect() as conn:
            surviving = {
                row[0]
                for row in conn.execute("SELECT job_id FROM jobs").fetchall()
            }
        for jid in stale_ids:
            if jid not in surviving:
                self._cache.pop(jid, None)
        if deleted:
            logger.info(f"Cleaned up {deleted} old jobs (>{days} days)")
        return deleted


class _PersistentJob:
    """A job dict that writes through to SQLite on every mutation."""

    def __init__(self, store: JobStore, job_id: str, data: dict):
        self._store = store
        self._job_id = job_id
        self._data = data

    def __getitem__(self, key):
        return self._data[key]

    def __setitem__(self, key, value):
        self._data[key] = value
        self._store._persist(self._job_id, self._data)

    def get(self, key, default=None):
        return self._data.get(key, default)

    def __contains__(self, key):
        return key in self._data

    def __repr__(self):
        return repr(self._data)
