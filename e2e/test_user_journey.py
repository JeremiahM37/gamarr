"""Hermetic Playwright journey tests for the gamarr web UI.

Every test runs against the real compiled binary + real Chromium, with all
external services stubbed on loopback (see conftest.py). The `ui` fixture
fails any test that produced a JS error, so each journey doubles as a
zero-console-error assertion.
"""
import re
import urllib.request

from playwright.sync_api import expect

SLOW_MS = 15_000  # generous single-action timeout; suite stays fast when green


def _goto_tab(page, tab: str):
    page.locator(f'#main-nav button[data-tab="{tab}"]').click()
    expect(page.locator(f"#tab-{tab}")).to_be_visible(timeout=SLOW_MS)


# ── boot & shell ──────────────────────────────────────────────────────────────

def test_boot_shell_and_tabs(ui):
    page = ui["page"]
    expect(page).to_have_title("Gamarr")
    # All five tabs switch and render their panel.
    for tab in ("library", "downloads", "wishlist", "settings", "search"):
        _goto_tab(page, tab)


def test_no_cdn_and_csp(ui, app):
    """The UI must be self-contained: no third-party origins, strict CSP."""
    page = ui["page"]
    html = page.content()
    assert "cdn.tailwindcss.com" not in html, "UI must not depend on a CDN"
    assert "onclick=" not in html, "inline handlers violate the strict CSP"

    req = urllib.request.urlopen(app["base"], timeout=5)
    csp = req.headers.get("Content-Security-Policy", "")
    assert "script-src 'self'" in csp, f"weak or missing CSP: {csp!r}"


# ── search ────────────────────────────────────────────────────────────────────

def test_ddl_search_renders_results(ui):
    page = ui["page"]
    _goto_tab(page, "search")
    page.locator("#search-input").fill("tetris")
    page.locator("#platform-filter").select_option("gb")
    page.locator("#search-btn").click()
    results = page.locator("#results")
    expect(results).to_contain_text("Tetris (World) (Rev 1)", timeout=SLOW_MS)
    expect(results).to_contain_text("Tetris Attack", timeout=SLOW_MS)


def test_torrent_search_renders_results(ui):
    page = ui["page"]
    _goto_tab(page, "search")
    page.locator("#search-input").fill("stardew")
    page.locator("#platform-filter").select_option("all")
    page.locator("#search-btn").click()
    results = page.locator("#results")
    expect(results).to_contain_text("Stardew Harvest Deluxe Edition", timeout=SLOW_MS)
    expect(results).to_contain_text("StubIndexer", timeout=SLOW_MS)


def test_search_no_results_is_clean(ui):
    page = ui["page"]
    _goto_tab(page, "search")
    page.locator("#search-input").fill("zzzznotagame")
    page.locator("#platform-filter").select_option("gb")
    page.locator("#search-btn").click()
    # Whatever empty-state copy is used, no result rows and no JS errors.
    page.wait_for_timeout(1500)
    assert page.locator("#results").locator("[id^=dl-btn-]").count() == 0


# ── the flagship journey: DDL download -> organize -> library ─────────────────

def test_ddl_download_pipeline_to_library(ui, app):
    page = ui["page"]
    _goto_tab(page, "search")
    page.locator("#search-input").fill("tetris")
    page.locator("#platform-filter").select_option("gb")
    page.locator("#search-btn").click()
    expect(page.locator("#results")).to_contain_text(
        "Tetris (World) (Rev 1)", timeout=SLOW_MS)

    # Download the first result.
    page.locator("[id^=dl-btn-]").first.click()

    # The job must reach the downloads view and complete. The downloads tab
    # self-refreshes every 5s; expect() retries until the status lands.
    _goto_tab(page, "downloads")
    expect(page.locator("#downloads")).to_contain_text("completed", timeout=60_000)

    # The ROM physically landed in the roms dir.
    rom_files = list(app["roms_dir"].rglob("*Tetris*"))
    assert rom_files, f"no Tetris file under {app['roms_dir']}"

    # And the library shows it.
    _goto_tab(page, "library")
    expect(page.locator("#library-grid")).to_contain_text("Tetris", timeout=SLOW_MS)


# ── wishlist CRUD ─────────────────────────────────────────────────────────────

def test_wishlist_add_and_delete(ui):
    page = ui["page"]
    _goto_tab(page, "wishlist")
    page.locator("#wish-title").fill("Chrono Quest E2E")
    page.locator("#wish-platform").select_option(index=0)
    page.locator("#tab-wishlist button", has_text=re.compile("add", re.I)).click()
    expect(page.locator("#wishlist")).to_contain_text("Chrono Quest E2E", timeout=SLOW_MS)

    # Delete it again (the item's delete/remove control).
    item = page.locator("#wishlist > *", has_text="Chrono Quest E2E").first
    item.locator("button", has_text=re.compile("delete|remove|✕|×", re.I)).first.click()
    expect(page.locator("#wishlist")).not_to_contain_text(
        "Chrono Quest E2E", timeout=SLOW_MS)


# ── settings: connection tests against the stubs ──────────────────────────────

def test_settings_connection_tests(ui):
    page = ui["page"]
    _goto_tab(page, "settings")

    page.locator("#test-qbittorrent-status").locator("xpath=ancestor-or-self::button").click()
    expect(page.locator("#test-qbittorrent-status")).to_have_text(
        re.compile("connected", re.I), timeout=SLOW_MS)

    page.locator("#test-prowlarr-status").locator("xpath=ancestor-or-self::button").click()
    expect(page.locator("#test-prowlarr-status")).to_have_text(
        re.compile("connected", re.I), timeout=SLOW_MS)

    # SABnzbd is deliberately unconfigured in the harness.
    page.locator("#test-sabnzbd-status").locator("xpath=ancestor-or-self::button").click()
    expect(page.locator("#test-sabnzbd-status")).to_have_text(
        re.compile("fail|not configured", re.I), timeout=SLOW_MS)


def test_settings_shows_sources_and_stats(ui):
    page = ui["page"]
    _goto_tab(page, "settings")
    expect(page.locator("#settings-sources")).to_contain_text(
        re.compile("myrient", re.I), timeout=SLOW_MS)
    expect(page.locator("#settings-stats")).to_be_visible(timeout=SLOW_MS)
