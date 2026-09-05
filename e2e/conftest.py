"""Hermetic end-to-end test harness for the gamarr web UI.

Boots the real gamarr binary against a single local stub server that
impersonates every external service gamarr talks to, so the full user
journey — search, DDL download, organize, library, wishlist, settings —
runs with ZERO external network access:

    [chromium] -> [gamarr binary] -> [stub qBittorrent/Prowlarr/Myrient on 127.0.0.1]

The injected sources registry points Myrient at the stub (which serves a
real ZIP so the DDL pipeline completes for real) and Vimm at the stub too,
where every vault page is the Cloudflare Turnstile challenge the real site
serves a non-browser client (issue #37) — fast, deterministic, and true to
what production actually returns.

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


# The stub Prowlarr's indexers, shaped like the instance in issue #17: the one
# tracker that carries games sits at a high ID no hardcoded default would guess,
# beside a books-only tracker and a disabled games tracker. Only ID 16 answers
# searches, so a run that queries the wrong indexers comes back empty.
PROWLARR_INDEXERS = [
    {
        "id": 3, "name": "BooksOnly", "enable": True,
        "capabilities": {"categories": [{"id": 7000, "name": "Books"}]},
    },
    {
        "id": 9, "name": "RetiredGames", "enable": False,
        "capabilities": {"categories": [{"id": 4000, "name": "PC"}]},
    },
    {
        "id": 16, "name": "GamesTracker", "enable": True,
        "capabilities": {"categories": [
            {"id": 4000, "name": "PC", "subCategories": [{"id": 4050, "name": "PC/Games"}]},
        ]},
    },
]
GAME_INDEXER_ID = 16


# What vimm.net serves a plain HTTP client for any vault page: the Turnstile
# widget and no download form. Trimmed from a live fetch; the markers gamarr
# keys on (the widget class and the "human" prompt) are verbatim.
VIMM_CHALLENGE_HTML = """<!DOCTYPE html><html><head><title>Vimm's Lair</title></head><body>
<div id="challenge"><p>Checking if you are human.</p>
<div class="cf-turnstile" data-sitekey="0x4AAAAAAAcFgS2_wvnSBZF1" data-callback="onTurnstileSuccess"></div>
<form method="post"><input type="hidden" name="cf-turnstile-response" value=""></form></div>
</body></html>"""


# Torrents the stub client is "seeding", plus every delete gamarr asked for.
# Tests arm these through /stub/* so the watcher sees a real completed torrent.
TORRENTS: list[dict] = []
DELETE_CALLS: list[dict] = []
FLARESOLVERR_CALLS: list[dict] = []
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
        base = f"http://127.0.0.1:{self.server.server_address[1]}"
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
                FLARESOLVERR_CALLS.clear()
            self._send(200, b'{"ok":true}', "application/json")
        # ── FlareSolverr: return a rendered Vimm page with a JS media ID ──
        elif path == "/v1":
            request = json.loads(body.decode())
            with SWARM_LOCK:
                FLARESOLVERR_CALLS.append(request)
            if request.get("tabs_till_verify") == 41:
                page = (
                    f'<html><body><form action="{base}/vimm-download" id="dl_form"></form>'
                    '<script>let allMedia = [{"ID":3811,"Region":"USA"}];</script>'
                    '</body></html>'
                )
            else:
                page = VIMM_CHALLENGE_HTML
            response = json.dumps({
                "status": "ok", "message": "Challenge solved!",
                "solution": {"status": 200, "response": page, "userAgent": "e2e-solver"},
            }).encode()
            self._send(200, response, "application/json")
        else:
            self._send(404, b"not found", "text/plain")

    def do_GET(self):  # noqa: N802
        base = f"http://127.0.0.1:{self.server.server_address[1]}"
        path = self.path.split("?")[0]

        if path == "/":
            self._send(
                200,
                b'{"msg":"FlareSolverr is ready!","version":"3.5.0-e2e"}',
                "application/json",
            )
        elif path == "/api/v2/torrents/info":
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
        elif path == "/stub/flaresolverr-calls":
            with SWARM_LOCK:
                body = json.dumps(list(FLARESOLVERR_CALLS)).encode()
            self._send(200, body, "application/json")
        # ── Prowlarr ──────────────────────────────────────────────────────
        elif path == "/api/v1/search":
            q, indexer_ids = "", ""
            if "?" in self.path:
                from urllib.parse import parse_qs, urlparse
                params = parse_qs(urlparse(self.path).query)
                q = params.get("query", [""])[0].lower()
                indexer_ids = params.get("indexerIds", [""])[0]
            # Real Prowlarr behavior: an indexer that carries nothing (or does
            # not exist) answers HTTP 200 with an empty list, not an error.
            if indexer_ids != str(GAME_INDEXER_ID):
                self._send(200, b"[]", "application/json")
                return
            hits = [r for r in PROWLARR_RELEASES if q and q.split()[0] in r["title"].lower()]
            self._send(200, json.dumps(hits).encode(), "application/json")
        elif path == "/api/v1/indexer":
            self._send(200, json.dumps(PROWLARR_INDEXERS).encode(), "application/json")
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
        elif path == "/vimm-download":
            params = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query)
            if params.get("mediaId") != ["3811"]:
                self._send(400, b"missing mediaId", "text/plain")
                return
            body = _rom_zip("Solved Vimm.zip")
            self.send_response(200)
            self.send_header("Content-Type", "application/zip")
            self.send_header("Content-Disposition", 'attachment; filename="Solved Vimm.zip"')
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        # ── Vimm's Lair: every vault page (search or game) is the gate ────
        elif path.startswith("/vault/"):
            self._send(200, VIMM_CHALLENGE_HTML.encode(), "text/html; charset=UTF-8")
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


def _boot_gamarr(stub_server, gamarr_binary, data, env_overrides: dict) -> dict:
    """Boot one gamarr with an injected registry: Myrient -> stub, Vimm -> the
    stub's Turnstile-gated vault.

    env_overrides let a test represent a deployment the defaults cannot — a
    library on a second filesystem, say — without a second harness.
    """
    registry = {
        "version": 1,
        "myrient": {
            "base_url": f"{stub_server}/files/",
            "platform_paths": {"gb": "gb/"},
        },
        "vimm": {"base_url": f"{stub_server}/vault/", "platform_systems": {}},
    }
    reg_path = data / "sources.json"
    reg_path.write_text(json.dumps(registry))

    port = _free_port()
    vault = data / "vault"
    roms = data / "roms"
    incoming = data / "incoming"
    vault.mkdir(exist_ok=True)
    roms.mkdir(exist_ok=True)
    incoming.mkdir(exist_ok=True)

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
        # Keep host-level solver configuration from making tests contact an
        # external service. Individual tests override these values as needed.
        "FLARESOLVERR_URL": "",
        "FLARESOLVERR_MAX_TIMEOUT": "55000",
        "FLARESOLVERR_TABS_TILL_VERIFY": "37",
        **env_overrides,
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

    return {
        "base": base, "data": data, "roms_dir": roms,
        "vault_dir": vault, "incoming_dir": incoming,
        "log_path": data / "gamarr.log", "_proc": proc, "_log": log,
    }


def _stop_gamarr(inst: dict) -> None:
    inst["_proc"].terminate()
    try:
        inst["_proc"].wait(timeout=5)
    except subprocess.TimeoutExpired:
        inst["_proc"].kill()
    inst["_log"].close()


@pytest.fixture(scope="session")
def gamarr_factory(stub_server, gamarr_binary, tmp_path_factory):
    """Boot extra gamarr instances with custom env, cleaned up at session end."""
    started: list[dict] = []

    def boot(name: str, **env_overrides) -> dict:
        data = tmp_path_factory.mktemp(name)
        inst = _boot_gamarr(stub_server, gamarr_binary, data, env_overrides)
        started.append(inst)
        return inst

    yield boot
    for inst in started:
        _stop_gamarr(inst)


@pytest.fixture(scope="session")
def app(gamarr_factory):
    return gamarr_factory("data")


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


def watch_page(page) -> dict:
    """Attach the recorders every UI test asserts on: JS errors, and rejected
    API responses.

    The 401 list exists because a swallowed 401 is invisible from the DOM —
    gamarr's frontend catches its own fetch errors, so an unauthenticated app
    renders a complete, entirely inert UI (issue #21). Watching the wire is the
    only way a test can tell that apart from a working page.
    """
    errors: list[str] = []
    unauthorized: list[str] = []

    page.on("pageerror", lambda e: errors.append(f"pageerror: {e}"))
    page.on(
        "console",
        lambda m: errors.append(f"console: {m.text}")
        if m.type == "error" and "Failed to load resource" not in m.text
        else None,
    )
    page.on(
        "response",
        lambda r: unauthorized.append(f"{r.status} {r.url}") if r.status == 401 else None,
    )
    return {"errors": errors, "unauthorized": unauthorized}


@pytest.fixture()
def ui(app, page):
    """A page on the gamarr UI that records every JS error. Tests assert the
    journey stays error-free (the strongest 'the frontend works' invariant)."""
    watched = watch_page(page)
    page.goto(app["base"], wait_until="networkidle")
    yield {"page": page, **watched, **app}
    assert watched["errors"] == [], f"JS errors during journey: {watched['errors']}"
    # This instance is unauthenticated by design, so nothing should ever 401.
    # A test suite that tolerates 401s cannot see an auth regression at all.
    assert watched["unauthorized"] == [], (
        f"unexpected 401s during journey: {watched['unauthorized']}")
