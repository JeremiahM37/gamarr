"""Hermetic Playwright journeys for import modes (issue #15).

A private-tracker user's complaint is simple to state and easy to regress:
after gamarr imports a torrent, is the data still where the client is seeding
it from, and is the torrent still in the client? These tests answer that by
driving the real UI — pick the mode in Settings, let the watcher import a
finished torrent from the stubbed client — and then looking at the filesystem
and at what gamarr asked the client to delete.
"""
import os
import time

from playwright.sync_api import expect

SLOW_MS = 15_000
IMPORT_MS = 60_000  # the watcher polls the client every 10s in the harness


def _goto_tab(page, tab: str):
    page.locator(f'#main-nav button[data-tab="{tab}"]').click()
    expect(page.locator(f"#tab-{tab}")).to_be_visible(timeout=SLOW_MS)


def _set_import_mode(page, mode: str):
    """Pick an import mode the way a user does, and wait for it to stick."""
    _goto_tab(page, "settings")
    select = page.locator("#setting-import-mode")
    expect(select).to_be_visible(timeout=SLOW_MS)
    select.select_option(mode)
    # The hint reflects the mode the server confirmed, so waiting on it (rather
    # than on the select) proves the save round-tripped.
    expect(page.locator("#import-mode-hint")).to_have_attribute("data-mode", mode, timeout=SLOW_MS)
    expect(select).to_have_value(mode)


def _seed_a_finished_torrent(app, swarm, name: str):
    """Put real bytes in the download dir and tell the client it finished."""
    content = app["incoming_dir"] / name
    content.mkdir(parents=True, exist_ok=True)
    payload = content / "game.bin"
    payload.write_bytes(b"SEEDED-PAYLOAD" * 64)
    digest = swarm.seed(name, content, app["incoming_dir"])
    return content, payload, digest


def _imported_file(app, name: str):
    return app["vault_dir"] / name / "game.bin"


def _wait_for_import(page, name: str):
    """The library grid loads when the tab is entered, so re-enter it until the
    watcher's auto-import shows up."""
    deadline = time.monotonic() + IMPORT_MS / 1000
    while time.monotonic() < deadline:
        _goto_tab(page, "library")
        if name in page.locator("#library-grid").inner_text():
            return
        page.wait_for_timeout(2000)
    # Out of patience — assert for the readable failure message.
    expect(page.locator("#library-grid")).to_contain_text(name, timeout=SLOW_MS)


# ── the setting itself ────────────────────────────────────────────────────────

def test_import_mode_setting_persists(ui):
    """The mode is a real, persisted setting — not a per-page-load default."""
    page = ui["page"]
    _set_import_mode(page, "hardlink")
    expect(page.locator("#import-mode-hint")).to_contain_text("keep seeding")

    page.reload(wait_until="networkidle")
    _goto_tab(page, "settings")
    expect(page.locator("#setting-import-mode")).to_have_value("hardlink", timeout=SLOW_MS)

    # And back again, so the rest of the suite starts from the default.
    _set_import_mode(page, "move")
    expect(page.locator("#setting-import-mode")).to_have_value("move")


def test_import_mode_options_cover_every_mode(ui):
    page = ui["page"]
    _goto_tab(page, "settings")
    values = page.locator("#setting-import-mode option").evaluate_all(
        "opts => opts.map(o => o.value)")
    assert values == ["move", "hardlink", "symlink", "copy"], values


# ── the flagship journey: hardlink import keeps the torrent seeding ───────────

def test_hardlink_import_keeps_torrent_seeding(ui, app, swarm):
    page = ui["page"]
    _set_import_mode(page, "hardlink")

    name = "Seed Keeper Deluxe"
    content, payload, digest = _seed_a_finished_torrent(app, swarm, name)

    _wait_for_import(page, name)

    imported = _imported_file(app, name)
    assert imported.exists(), f"{imported} was never imported"

    # 1. The seeded data is still exactly where the client expects it.
    assert payload.exists(), "import moved the file out of the download directory"
    assert payload.read_bytes() == b"SEEDED-PAYLOAD" * 64

    # 2. It is one file on disk, not two — a real hardlink, not a copy.
    assert os.stat(payload).st_ino == os.stat(imported).st_ino, \
        "library entry is a separate copy, not a hardlink"

    # 3. The torrent is still in the client, so it is still seeding.
    assert swarm.deletes() == [], f"gamarr removed the torrent: {swarm.deletes()}"
    assert any(t["hash"] == digest for t in swarm.torrents()), \
        "torrent disappeared from the client"

    # 4. And deleting the library entry does not take the seeded data with it.
    imported.unlink()
    assert payload.read_bytes() == b"SEEDED-PAYLOAD" * 64


# ── the default is unchanged ─────────────────────────────────────────────────

def test_move_import_still_moves_and_removes_the_torrent(ui, app, swarm):
    """The pre-existing behavior stays the default, for users who want it."""
    page = ui["page"]
    _set_import_mode(page, "move")

    name = "Ratio Burner"
    content, payload, digest = _seed_a_finished_torrent(app, swarm, name)

    _wait_for_import(page, name)

    assert _imported_file(app, name).exists()
    assert not payload.exists(), "move import left the source behind"

    deletes = swarm.deletes()
    assert [d for d in deletes if d["hash"] == digest and d["delete_files"]], \
        f"move import should remove the torrent and its files, got {deletes}"


# ── copy mode, for filesystems that cannot hardlink ──────────────────────────

def test_copy_import_leaves_the_source_seedable(ui, app, swarm):
    page = ui["page"]
    _set_import_mode(page, "copy")

    name = "Double Disk Dungeon"
    content, payload, digest = _seed_a_finished_torrent(app, swarm, name)

    _wait_for_import(page, name)

    imported = _imported_file(app, name)
    assert imported.exists()
    assert payload.exists(), "copy import removed the source"
    assert os.stat(payload).st_ino != os.stat(imported).st_ino, \
        "copy import should produce an independent file"
    assert swarm.deletes() == [], "copy import must leave the torrent seeding"

    _set_import_mode(page, "move")
