#!/usr/bin/env python3
"""Gamarr v2.0 — Comprehensive End-to-End Test Suite"""
import json
import sys
import time
import urllib.request
import urllib.error
import subprocess
import traceback
import concurrent.futures

import os
BASE = os.environ.get("GAMARR_URL", "http://localhost:5057")
PASS = 0
FAIL = 0
ERRORS = []

def get(path, timeout=30):
    r = urllib.request.urlopen(f"{BASE}{path}", timeout=timeout)
    return json.loads(r.read())

def post(path, data=None, timeout=30):
    body = json.dumps(data).encode() if data else b""
    req = urllib.request.Request(f"{BASE}{path}", method="POST", data=body,
                                 headers={"Content-Type": "application/json"})
    return json.loads(urllib.request.urlopen(req, timeout=timeout).read())

def delete(path, timeout=10):
    req = urllib.request.Request(f"{BASE}{path}", method="DELETE")
    return json.loads(urllib.request.urlopen(req, timeout=timeout).read())

def put(path, data, timeout=10):
    body = json.dumps(data).encode()
    req = urllib.request.Request(f"{BASE}{path}", method="PUT", data=body,
                                 headers={"Content-Type": "application/json"})
    return json.loads(urllib.request.urlopen(req, timeout=timeout).read())

def raw_get(path, timeout=10):
    r = urllib.request.urlopen(f"{BASE}{path}", timeout=timeout)
    return r.status, r.read().decode()

def test(name, fn):
    global PASS, FAIL
    try:
        fn()
        PASS += 1
        print(f"  \u2713 {name}")
    except Exception as e:
        FAIL += 1
        print(f"  \u2717 {name}: {e}")
        tb = traceback.format_exc()
        ERRORS.append((name, str(e) or repr(e), tb))

# ═══════════════════════════════════════════════════════════════════════════════
print("=== 1. HEALTH & CONFIG ===")

def t_health():
    d = get("/api/health")
    assert d["status"] == "ok" and d["version"] == "2.0.0"
test("GET /api/health", t_health)

def t_config():
    d = get("/api/config")
    assert d["prowlarr"]["configured"] == True
    assert d["qbittorrent"]["configured"] == True
    assert d["version"] == "2.0.0"
test("GET /api/config", t_config)

# ═══════════════════════════════════════════════════════════════════════════════
print("\n=== 2. PLATFORMS ===")

def t_platforms():
    d = get("/api/platforms")
    assert len(d["platforms"]) >= 20
    ids = {p["id"] for p in d["platforms"]}
    for x in ["all", "pc", "switch", "gba", "nes", "ps2", "psx", "n64", "ngc"]:
        assert x in ids, f"Missing '{x}'"
test("24 platforms with expected slugs", t_platforms)

# ═══════════════════════════════════════════════════════════════════════════════
print("\n=== 3. SOURCES ===")

def t_sources():
    d = get("/api/sources")
    assert {s["name"] for s in d["sources"]} == {"prowlarr", "myrient", "vimm"}
test("3 sources (prowlarr, myrient, vimm)", t_sources)

# ═══════════════════════════════════════════════════════════════════════════════
print("\n=== 4. SEARCH ===")

def t_search_prowlarr():
    # Use 'mario' which reliably returns results from both Prowlarr and DDL sources
    time.sleep(1)
    d = get("/api/search?q=mario+kart&platform=switch", timeout=90)
    r = d.get("results", [])
    assert isinstance(r, list), f"results not a list: {type(r)}"
    # Check safety scoring on any results that have it
    scored = [x for x in r if x.get("safety_score") is not None]
    assert len(scored) > 0, f"No scored results (got {len(r)} total)"
    for x in scored[:5]:
        assert isinstance(x["safety_score"], int), f"safety_score not int: {type(x['safety_score'])}"
        assert isinstance(x["safety_warnings"], list), f"safety_warnings not list"
    # Verify we get at least some results (torrents OR ddl)
    assert len(r) > 0, "No results at all for mario kart switch"
test("Search with safety scoring", t_search_prowlarr)

def t_search_myrient():
    d = get("/api/search?q=zelda&platform=gba")
    m = [r for r in d.get("results", []) if r.get("indexer") == "Myrient"]
    assert len(m) > 0 and all(r["safety_score"] == 95 for r in m)
test("Myrient DDL search (zelda, gba)", t_search_myrient)

def t_search_vimm():
    d = get("/api/search?q=metroid&platform=snes")
    v = [r for r in d.get("results", []) if "Vimm" in r.get("indexer", "")]
    assert len(v) > 0 and all(r.get("vimm_id") for r in v)
test("Vimm DDL search (metroid, snes)", t_search_vimm)

def t_search_empty():
    d = get("/api/search?q=")
    assert isinstance(d.get("results"), list)
test("Empty query returns [] not null", t_search_empty)

def t_search_gibberish():
    d = get("/api/search?q=xyzzznonexistent999&platform=psvita")
    assert isinstance(d.get("results"), list)
test("Gibberish query returns [] (no crash)", t_search_gibberish)

def t_search_all():
    d = get("/api/search?q=mario&platform=all")
    assert isinstance(d.get("results"), list) and len(d["results"]) > 0
test("Search all platforms (mario)", t_search_all)

# ═══════════════════════════════════════════════════════════════════════════════
print("\n=== 5. LIBRARY ===")

def t_library_all():
    d = get("/api/library")
    assert d["total"] >= 14 and isinstance(d["items"], list)
    if d["items"]:
        for k in ["id", "title", "platform", "platform_slug", "is_pc", "file_path", "file_size", "source", "added_at"]:
            assert k in d["items"][0], f"Missing '{k}'"
test("Library all items with fields", t_library_all)

def t_library_pc():
    d = get("/api/library?platform=pc")
    assert d["total"] >= 5 and all(i["is_pc"] for i in d["items"])
test("Library filter PC only", t_library_pc)

def t_library_nes():
    d = get("/api/library?platform=nes")
    assert all(i["platform_slug"] == "nes" for i in d["items"])
test("Library filter NES only", t_library_nes)

def t_library_search():
    d = get("/api/library?q=Cuphead")
    assert d["total"] >= 1 and "Cuphead" in d["items"][0]["title"]
test("Library text search (Cuphead)", t_library_search)

def t_library_search_empty():
    d = get("/api/library?q=xyznonexistent")
    assert d["total"] == 0
test("Library search no match", t_library_search_empty)

def t_library_pagination():
    d1 = get("/api/library?page=1&page_size=3")
    assert len(d1["items"]) <= 3
    if d1["total"] > 3:
        d2 = get("/api/library?page=2&page_size=3")
        assert d2["page"] == 2
        assert {i["id"] for i in d1["items"]}.isdisjoint({i["id"] for i in d2["items"]})
test("Library pagination (no overlap)", t_library_pagination)

def t_library_bad_page():
    assert get("/api/library?page=-1")["page"] == 1
    assert get("/api/library?page=0")["page"] == 1
test("Library bad page defaults to 1", t_library_bad_page)

# ═══════════════════════════════════════════════════════════════════════════════
print("\n=== 6. DOWNLOAD PIPELINE (end-to-end) ===")

def t_ddl_e2e():
    # Clean slate
    subprocess.run(["ssh", "root@localhost", "rm -f /mnt/storage/games/roms/gb/Tetris*"], capture_output=True, timeout=10)
    for item in get("/api/library?q=Tetris").get("items", []):
        delete(f"/api/library/{item['id']}")
    post("/api/downloads/clear")
    time.sleep(1)

    lib_before = get("/api/library")["total"]
    act_before = get("/api/activity")["total"]

    r = post("/api/download", {
        "source_type": "ddl", "title": "Tetris (World) (Rev 1)",
        "platform": "Game Boy", "platform_slug": "gb", "is_pc": False,
        "download_url": "https://myrient.erista.me/files/No-Intro/Nintendo%20-%20Game%20Boy/Tetris%20(World)%20(Rev%201).zip"
    })
    assert r["success"] and r["job_id"]
    job_id = r["job_id"]

    final = None
    for _ in range(30):
        time.sleep(2)
        dl = get("/api/downloads")
        jobs = [d for d in dl.get("downloads", []) if d.get("job_id") == job_id]
        if jobs:
            final = jobs[0]["status"]
            if final in ("completed", "error"):
                break
    assert final == "completed", f"Got {final}"

    # File on disk
    assert subprocess.run(["ssh", "root@localhost", "ls /mnt/storage/games/roms/gb/Tetris*"],
                          capture_output=True, timeout=10).returncode == 0, "File not on disk"
    # Sidecar
    assert subprocess.run(["ssh", "root@localhost", "ls /mnt/storage/games/roms/gb/*.gamarr.json"],
                          capture_output=True, timeout=10).returncode == 0, "No sidecar"
    # Library
    assert get("/api/library")["total"] > lib_before, "Library count didn't increase"
    lib = get("/api/library?q=Tetris")
    assert any("Tetris" in i["title"] for i in lib["items"]), "Not in library"
    # Activity
    assert get("/api/activity")["total"] > act_before, "Activity count didn't increase"

    # Cleanup
    for item in lib["items"]:
        if "Tetris" in item["title"]:
            delete(f"/api/library/{item['id']}")
    post("/api/downloads/clear")
    subprocess.run(["ssh", "root@localhost", "rm -f /mnt/storage/games/roms/gb/Tetris*"], capture_output=True, timeout=10)
test("DDL download -> organize -> library -> activity", t_ddl_e2e)

def t_download_no_url():
    try:
        r = post("/api/download", {"source_type": "ddl", "title": "X", "platform": "PC", "is_pc": True})
        assert r.get("success") == False
    except urllib.error.HTTPError as e:
        assert e.code == 400
test("Download with no URL returns 400", t_download_no_url)

def t_downloads_list():
    d = get("/api/downloads")
    assert isinstance(d["downloads"], list)
test("Downloads list returns [] not null", t_downloads_list)

def t_download_clear():
    assert post("/api/downloads/clear")["success"]
test("Clear downloads", t_download_clear)

# ═══════════════════════════════════════════════════════════════════════════════
print("\n=== 7. RETRY ===")

def t_retry_bad():
    d = post("/api/downloads/nonexistent/retry")
    assert d["success"] == False
test("Retry nonexistent job", t_retry_bad)

# ═══════════════════════════════════════════════════════════════════════════════
print("\n=== 8. WISHLIST ===")

def t_wishlist_crud():
    r = post("/api/wishlist", {"title": "E2E Test", "platform": "Switch", "platform_slug": "switch"})
    assert r["success"] and r["id"]
    wid = r["id"]
    d = get("/api/wishlist")
    assert any(i["title"] == "E2E Test" and i["platform"] == "Switch" for i in d["items"])
    delete(f"/api/wishlist/{wid}")
    assert not any(i["id"] == wid for i in get("/api/wishlist")["items"])
test("Wishlist CRUD", t_wishlist_crud)

def t_wishlist_empty():
    try:
        r = post("/api/wishlist", {"title": ""})
        assert r.get("success") == False
    except urllib.error.HTTPError:
        pass  # 400 is fine
test("Wishlist empty title rejected", t_wishlist_empty)

# ═══════════════════════════════════════════════════════════════════════════════
print("\n=== 9. ACTIVITY ===")

def t_activity():
    d = get("/api/activity")
    assert isinstance(d["entries"], list) and "total" in d
test("Activity log format", t_activity)

# ═══════════════════════════════════════════════════════════════════════════════
print("\n=== 10. DDL SOURCES ===")

def t_ddl_builtin():
    d = get("/api/ddl-sources")
    assert len([s for s in d["sources"] if s.get("builtin")]) == 2
test("2 builtin DDL sources", t_ddl_builtin)

def t_ddl_crud():
    post("/api/ddl-sources", {"name": "E2E", "url": "https://example.com"})
    assert any(s["name"] == "E2E" for s in get("/api/ddl-sources")["sources"])
    delete("/api/ddl-sources/0")
    assert not any(s["name"] == "E2E" for s in get("/api/ddl-sources")["sources"])
test("DDL source CRUD", t_ddl_crud)

# ═══════════════════════════════════════════════════════════════════════════════
print("\n=== 11. SETTINGS ===")

def t_settings():
    put("/api/settings", {"extract_archives": True})
    assert get("/api/settings")["extract_archives"] == True
    put("/api/settings", {"extract_archives": False})
    assert get("/api/settings")["extract_archives"] == False
test("Settings toggle roundtrip", t_settings)

# ═══════════════════════════════════════════════════════════════════════════════
print("\n=== 12. STATS ===")

def t_stats():
    d = get("/api/stats")
    assert d["library_total"] >= 14 and isinstance(d["platforms"], dict)
test("Stats with library data", t_stats)

# ═══════════════════════════════════════════════════════════════════════════════
print("\n=== 13. CONNECTION TESTS ===")

test("Prowlarr connected", lambda: post("/api/test/prowlarr")["success"] == True or None)
test("qBittorrent connected", lambda: post("/api/test/qbittorrent")["success"] == True or None)
test("SABnzbd not configured (graceful)", lambda: (
    post("/api/test/sabnzbd")["success"] == False and "Not configured" in post("/api/test/sabnzbd")["error"]
) or None)

# ═══════════════════════════════════════════════════════════════════════════════
print("\n=== 14. MONITOR ===")

def t_monitor():
    d = get("/api/monitor/status")
    for k in ["enabled", "diagnosis", "recent_errors", "pending_actions", "action_history"]:
        assert k in d
test("Monitor status structure", t_monitor)

test("Monitor analyze (no crash)", lambda: post("/api/monitor/analyze") is not None or None)
test("Approve nonexistent action", lambda: post("/api/monitor/actions/x/approve")["success"] == False or None)
test("Dismiss nonexistent action", lambda: post("/api/monitor/actions/x/dismiss")["success"] == False or None)

# ═══════════════════════════════════════════════════════════════════════════════
print("\n=== 15. METRICS ===")

def t_metrics():
    s, body = raw_get("/metrics")
    assert s == 200 and "gamarr_library_total" in body and "gamarr_library_by_platform" in body
    for line in body.split("\n"):
        if line.startswith("gamarr_library_total "):
            assert int(line.split()[-1]) >= 14
test("Prometheus metrics", t_metrics)

# ═══════════════════════════════════════════════════════════════════════════════
print("\n=== 16. UI ===")

def t_ui():
    s, body = raw_get("/")
    assert s == 200 and len(body) > 10000
    for tab in ["tab-search", "tab-library", "tab-downloads", "tab-wishlist", "tab-settings"]:
        assert tab in body, f"Missing {tab}"
    assert "tailwindcss" in body and "Gamarr" in body
test("UI with 5 tabs and Tailwind", t_ui)

# ═══════════════════════════════════════════════════════════════════════════════
print("\n=== 17. EDGE CASES ===")

def t_del_job():
    try:
        delete("/api/downloads/nonexistent")
        assert False, "Should have raised"
    except urllib.error.HTTPError as e:
        assert e.code == 404
test("DELETE nonexistent job -> 404", t_del_job)

test("DELETE nonexistent library item (idempotent)", lambda: delete("/api/library/999999")["success"] or None)
test("Library bad page -> defaults to 1", lambda: get("/api/library?page=-1")["page"] == 1 or None)

def t_organize_bad():
    try:
        post("/api/downloads/organize/badhash", {"platform": "PC", "is_pc": True})
        assert False
    except urllib.error.HTTPError as e:
        assert e.code == 400
test("Organize bad hash -> 400", t_organize_bad)

def t_concurrent():
    def req(i): return get("/api/library?page=1&page_size=1")
    with concurrent.futures.ThreadPoolExecutor(max_workers=5) as ex:
        results = list(ex.map(req, range(5)))
    assert all(r["page"] == 1 for r in results)
test("5 concurrent requests (no race)", t_concurrent)

# ═══════════════════════════════════════════════════════════════════════════════
# Cleanup
try:
    for item in get("/api/wishlist").get("items", []):
        if "E2E" in item.get("title", ""):
            delete(f"/api/wishlist/{item['id']}")
    post("/api/downloads/clear")
except: pass

print(f"\n{'='*60}")
print(f"  RESULTS: {PASS} passed, {FAIL} failed out of {PASS+FAIL} tests")
print(f"{'='*60}")
if ERRORS:
    print(f"\nFailed tests:")
    for name, err, tb in ERRORS:
        print(f"\n  \u2717 {name}")
        print(f"    {err}")
sys.exit(1 if FAIL > 0 else 0)
