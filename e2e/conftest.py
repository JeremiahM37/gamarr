"""Hermetic end-to-end test harness for the gamarr web UI.

Boots the real gamarr binary against a single local stub server that
impersonates every external service gamarr talks to, so the full user
journey — search, DDL download, organize, library, wishlist, settings —
runs with ZERO external network access:

    [chromium] -> [gamarr binary] -> [stub qBittorrent/Prowlarr/Myrient on 127.0.0.1]

The injected sources registry points Myrient at the stub (which serves a
real ZIP so the DDL pipeline completes for real) and Vimm at a dead local
port that refuses connections instantly, keeping runs fast and deterministic.

Requires: the gamarr binary (built automatically, or set GAMARR_E2E_BIN),
pytest-playwright with chromium installed.
"""
import io
import json
import os
import socket
import subprocess
import threading
import time
import urllib.parse
import urllib.request
import zipfile
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).resolve().parent.parent

# Myrient fixture files for the "gb" platform. Names mirror No-Intro naming
# so region filtering and title matching run the same code paths as prod.
GB_FILES = [
    "Tetris (World) (Rev 1).zip",
    "Tetris Attack (USA, Europe) (SGB Enhanced).zip",
    "Wario Land - Super Mario Land 3 (World).zip",
]

# Prowlarr fixture releases: one obviously-good seeded release and one
# zero-seeder release so the UI renders both shapes.
PROWLARR_RELEASES = [
    {
        "title": "Stardew Harvest Deluxe Edition [FitGirl Repack]",
        "size": 1_500_000_000,
        "seeders": 120,
        "leechers": 4,
        "indexer": "StubIndexer",
        "downloadUrl": "http://127.0.0.1:1/stub.torrent",
        "guid": "stub-guid-1",
        "categories": [{"id": 4050}],
        "age": 12,
    },
    {
        "title": "Stardew Harvest (Repack, dead)",
        "size": 900_000_000,
        "seeders": 0,
        "leechers": 0,
        "indexer": "StubIndexer",
        "downloadUrl": "http://127.0.0.1:1/dead.torrent",
        "guid": "stub-guid-2",
        "categories": [{"id": 4050}],
        "age": 400,
    },
]


# Torrents the stub client is "seeding", plus every delete gamarr asked for.
# Tests arm these through /stub/* so the watcher sees a real completed torrent.
TORRENTS: list[dict] = []
DELETE_CALLS: list[dict] = []
SWARM_LOCK = threading.Lock()


def _free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _rom_zip(name: str) -> bytes:
    """A small but genuine ZIP containing a fake .gb ROM."""
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr(name.replace(".zip", ".gb"), b"GAMARR-E2E-ROM" * 64)
    return buf.getvalue()


class _StubHandler(BaseHTTPRequestHandler):
    """One handler impersonating qBittorrent + Prowlarr + Myrient."""

    def _send(self, code: int, body: bytes, ctype: str):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    # ── qBittorrent ────────────────────────────────────────────────────────
    def do_POST(self):  # noqa: N802 (http.server API)
        body = self.rfile.read(int(self.headers.get("Content-Length", 0) or 0))
        path = self.path.split("?")[0]
        if path == "/api/v2/auth/login":
            # Real qBittorrent 5.x behavior: HTTP 200 + "Ok." on success.
            self._send(200, b"Ok.", "text/plain")
        elif path == "/api/v2/torrents/add":
            self._send(200, b"Ok.", "text/plain")
        elif path == "/api/v2/torrents/delete":
            # Record what gamarr asked for. "deleteFiles=true" is the call that
            # destroys seeded data; a seed-safe import must not make it.
            form = urllib.parse.parse_qs(body.decode())
            hashes = form.get("hashes", [""])[0]
            with SWARM_LOCK:
                DELETE_CALLS.append(
                    {"hash": hashes, "delete_files": form.get("deleteFiles", [""])[0] == "true"}
                )
                TORRENTS[:] = [t for t in TORRENTS if t["hash"] != hashes]
            self._send(200, b"", "text/plain")
        # ── harness control: arm a completed torrent in the client ─────────
        elif path == "/stub/torrents":
            t = json.loads(body.decode())
            with SWARM_LOCK:
                TORRENTS.append({
                    "name": t["name"],
                    "hash": t["hash"],
                    "progress": 1.0,
                    "state": "stoppedUP",
                    "total_size": t.get("total_size", 1024),
                    "save_path": t["save_path"],
                    "content_path": t["content_path"],
                })
            self._send(200, b'{"ok":true}', "application/json")
        elif path == "/stub/reset":
            with SWARM_LOCK:
                TORRENTS.clear()
                DELETE_CALLS.clear()
            self._send(200, b'{"ok":true}', "application/json")
        else:
            self._send(404, b"not found", "text/plain")

    def do_GET(self):  # noqa: N802
        base = f"http://127.0.0.1:{self.server.server_address[1]}"
        path = self.path.split("?")[0]

        if path == "/api/v2/torrents/info":
            with SWARM_LOCK:
                body = json.dumps(list(TORRENTS)).encode()
            self._send(200, body, "application/json")
        elif path == "/api/v2/torrents/files":
            self._send(200, b"[]", "application/json")
        # ── harness control: what did gamarr do to the swarm? ──────────────
        elif path == "/stub/deletes":
            with SWARM_LOCK:
                body = json.dumps(list(DELETE_CALLS)).encode()
            self._send(200, body, "application/json")
        elif path == "/stub/torrents":
            with SWARM_LOCK:
                body = json.dumps(list(TORRENTS)).encode()
            self._send(200, body, "application/json")
        # ── Prowlarr ──────────────────────────────────────────────────────
        elif path == "/api/v1/search":
            q = ""
            if "?" in self.path:
                from urllib.parse import parse_qs, urlparse
                q = parse_qs(urlparse(self.path).query).get("query", [""])[0].lower()
            hits = [r for r in PROWLARR_RELEASES if q and q.split()[0] in r["title"].lower()]
            self._send(200, json.dumps(hits).encode(), "application/json")
        elif path == "/api/v1/indexer":
            self._send(200, b"[]", "application/json")
        # ── Myrient directory listing + ROM files ─────────────────────────
        elif path == "/files/gb/":
            rows = "".join(
                f'<tr><td><a href="{urllib.parse.quote(n)}" title="{n}">{n}</a></td></tr>'
                for n in GB_FILES
            )
            html = f'<html><body><table><tr><td><a href="../">Parent</a></td></tr>{rows}</table></body></html>'
            self._send(200, html.encode(), "text/html")
        elif path.startswith("/files/gb/"):
            name = urllib.parse.unquote(path.split("/files/gb/")[1])
            if name in GB_FILES:
                self._send(200, _rom_zip(name), "application/zip")
            else:
                self._send(404, b"no such rom", "text/plain")
        else:
            self._send(404, b"not found", "text/plain")

    def log_message(self, *args):  # keep pytest output clean
        pass


@pytest.fixture(scope="session")
def stub_server():
    port = _free_port()
    httpd = ThreadingHTTPServer(("127.0.0.1", port), _StubHandler)
    t = threading.Thread(target=httpd.serve_forever, daemon=True)
    t.start()
    yield f"http://127.0.0.1:{port}"
    httpd.shutdown()


@pytest.fixture(scope="session")
def gamarr_binary(tmp_path_factory) -> Path:
    env_bin = os.environ.get("GAMARR_E2E_BIN")
    if env_bin:
        return Path(env_bin).resolve()
    out = tmp_path_factory.mktemp("bin") / "gamarr"
    subprocess.run(
        ["go", "build", "-o", str(out), "./cmd/gamarr"],
        cwd=REPO_ROOT, check=True,
    )
    return out


@pytest.fixture(scope="session")
def app(stub_server, gamarr_binary, tmp_path_factory):
    """Boot gamarr with an injected registry: Myrient -> stub, Vimm -> dead
    port that refuses connections instantly."""
    data = tmp_path_factory.mktemp("data")
    dead = "http://127.0.0.1:1/"
    registry = {
        "version": 1,
        "myrient": {
            "base_url": f"{stub_server}/files/",
            "platform_paths": {"gb": "gb/"},
        },
        "vimm": {"base_url": dead, "platform_systems": {}},
    }
    reg_path = data / "sources.json"
    reg_path.write_text(json.dumps(registry))

    port = _free_port()
    vault = data / "vault"
    roms = data / "roms"
    incoming = data / "incoming"
    vault.mkdir()
    roms.mkdir()
    incoming.mkdir()

    env = {
        **os.environ,
        "GAMARR_PORT": str(port),
        "DATA_DIR": str(data / "gamarr"),
        "GAMES_VAULT_PATH": str(vault),
        "GAMES_ROMS_PATH": str(roms),
        "GAMARR_SOURCES_PATH": str(reg_path),
        "QB_URL": stub_server,
        "QB_USER": "e2e",
        "QB_PASS": "e2e",
        # DDL staging dir — defaults to /data/incoming/ which won't exist on
        # a dev box; without this the pipeline dies as a bare "Download failed".
        "QB_SAVE_PATH": str(incoming),
        # Poll the (stubbed) client often enough that an armed torrent gets
        # auto-imported inside a test's patience. 10s is the floor gamarr
        # accepts before it clamps back to 30s.
        "WATCHER_INTERVAL": "10",
        "PROWLARR_URL": stub_server,
        "PROWLARR_API_KEY": "e2e-stub-key",
    }
    log = open(data / "gamarr.log", "w")
    proc = subprocess.Popen([str(gamarr_binary)], env=env, stdout=log, stderr=log)

    base = f"http://127.0.0.1:{port}"
    for _ in range(60):
        try:
            urllib.request.urlopen(f"{base}/api/health", timeout=1)
            break
        except Exception:
            if proc.poll() is not None:
                log.close()
                raise RuntimeError(
                    "gamarr exited during startup:\n" + (data / "gamarr.log").read_text())
            time.sleep(0.5)
    else:
        proc.kill()
        raise RuntimeError("gamarr did not become healthy within 30s")

    yield {
        "base": base, "data": data, "roms_dir": roms,
        "vault_dir": vault, "incoming_dir": incoming,
    }

    proc.terminate()
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        proc.kill()
    log.close()


class _Swarm:
    """The stubbed download client, as a test sees it: arm a completed torrent,
    then ask what gamarr did to it."""

    def __init__(self, base: str):
        self.base = base

    def _post(self, path: str, payload: dict) -> None:
        req = urllib.request.Request(
            f"{self.base}{path}", data=json.dumps(payload).encode(),
            headers={"Content-Type": "application/json"}, method="POST",
        )
        urllib.request.urlopen(req, timeout=5).read()

    def _get(self, path: str):
        return json.loads(urllib.request.urlopen(f"{self.base}{path}", timeout=5).read())

    def seed(self, name: str, content_path, save_path) -> str:
        """Arm a finished torrent whose data is already on disk."""
        digest = f"{abs(hash(name)):x}".ljust(40, "0")[:40]
        self._post("/stub/torrents", {
            "name": name, "hash": digest,
            "content_path": str(content_path), "save_path": str(save_path),
        })
        return digest

    def torrents(self):
        return self._get("/stub/torrents")

    def deletes(self):
        return self._get("/stub/deletes")

    def reset(self):
        self._post("/stub/reset", {})


@pytest.fixture()
def swarm(stub_server):
    s = _Swarm(stub_server)
    s.reset()
    yield s
    s.reset()


@pytest.fixture()
def ui(app, page):
    """A page on the gamarr UI that records every JS error. Tests assert the
    journey stays error-free (the strongest 'the frontend works' invariant)."""
    errors: list[str] = []
    page.on("pageerror", lambda e: errors.append(f"pageerror: {e}"))
    page.on(
        "console",
        lambda m: errors.append(f"console: {m.text}")
        if m.type == "error" and "Failed to load resource" not in m.text
        else None,
    )
    page.goto(app["base"], wait_until="networkidle")
    yield {"page": page, "errors": errors, **app}
    assert errors == [], f"JS errors during journey: {errors}"
