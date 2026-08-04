"""Playwright coverage for the hardlink preflight (follow-up on issue #15).

The complaint behind these tests is that "hardlink needs one filesystem" is
advice a user cannot check by looking. A Docker deployment with one bind mount
per directory puts the download directory and the library on the same disk,
reports the same device for both, and still cannot link — so the setting looks
accepted and only fails hours later, on the first import.

So the UI is driven the way a user drives it: pick hardlink in Settings, and
read what the page says about the layout it is running on. One instance here
can hardlink; the other has its library on a second filesystem and cannot.
"""
import os
import shutil
import tempfile

import pytest
from playwright.sync_api import expect

SLOW_MS = 15_000


def _open_settings(page, base: str):
    page.goto(base, wait_until="networkidle")
    page.locator('#main-nav button[data-tab="settings"]').click()
    expect(page.locator("#tab-settings")).to_be_visible(timeout=SLOW_MS)


def _select_mode(page, mode: str):
    page.locator("#setting-import-mode").select_option(mode)
    expect(page.locator("#import-mode-hint")).to_have_attribute(
        "data-mode", mode, timeout=SLOW_MS)


@pytest.fixture(scope="session")
def split_mount_app(gamarr_factory):
    """A gamarr whose library sits on a second filesystem — what one bind mount
    per directory produces, without needing Docker to reproduce it."""
    if not os.path.isdir("/dev/shm"):
        pytest.skip("no /dev/shm to stand in for a separate mount")
    if os.stat(tempfile.gettempdir()).st_dev == os.stat("/dev/shm").st_dev:
        pytest.skip("temp dir and /dev/shm are one filesystem")

    vault = tempfile.mkdtemp(prefix="gamarr-e2e-split-", dir="/dev/shm")
    inst = gamarr_factory(
        "split",
        GAMES_VAULT_PATH=vault,
        GAMES_ROMS_PATH=vault,
        IMPORT_MODE="hardlink",
    )
    yield {**inst, "vault": vault}
    shutil.rmtree(vault, ignore_errors=True)


# ── a layout that works says so ──────────────────────────────────────────────

def test_hardlink_check_verifies_a_working_layout(ui):
    page = ui["page"]
    _open_settings(page, ui["base"])
    _select_mode(page, "hardlink")

    check = page.locator("#hardlink-check")
    expect(check).to_have_attribute("data-status", "ok", timeout=SLOW_MS)
    expect(check).to_contain_text(str(ui["incoming_dir"]))

    # Leave the shared instance on the default for the rest of the suite.
    _select_mode(page, "move")


def test_no_check_for_modes_that_cannot_hit_a_boundary(ui):
    """Only hardlink imports can fail on a mount boundary, so only hardlink
    gets a verdict — an empty status for the rest, not a stale one."""
    page = ui["page"]
    _open_settings(page, ui["base"])
    _select_mode(page, "hardlink")
    expect(page.locator("#hardlink-check")).to_have_attribute(
        "data-status", "ok", timeout=SLOW_MS)

    _select_mode(page, "copy")
    expect(page.locator("#hardlink-check")).to_have_attribute("data-status", "")
    expect(page.locator("#hardlink-check")).to_have_text("")

    _select_mode(page, "move")


# ── the layout from the report: same disk, separate mounts, cannot link ──────

def test_split_mount_layout_is_reported_in_the_ui(page, split_mount_app):
    _open_settings(page, split_mount_app["base"])
    expect(page.locator("#setting-import-mode")).to_have_value(
        "hardlink", timeout=SLOW_MS)

    check = page.locator("#hardlink-check")
    expect(check).to_have_attribute("data-status", "failed", timeout=SLOW_MS)
    # The user needs all three: what failed, where, and the Docker cause.
    expect(check).to_contain_text(str(split_mount_app["incoming_dir"]))
    expect(check).to_contain_text(split_mount_app["vault"])
    expect(check).to_contain_text("bind mount")


def test_split_mount_layout_is_warned_about_at_startup(split_mount_app):
    """An install that never opens Settings still finds out, from the log."""
    log = split_mount_app["log_path"].read_text()
    assert "hardlink import will fail with this layout" in log, log[-2000:]
    assert split_mount_app["vault"] in log


def test_a_split_mount_can_still_use_a_mode_that_works(page, split_mount_app):
    """Switching to copy clears the verdict: the layout is only a problem for
    the one mode that needs a single mount."""
    _open_settings(page, split_mount_app["base"])
    _select_mode(page, "copy")
    expect(page.locator("#hardlink-check")).to_have_attribute("data-status", "")

    _select_mode(page, "hardlink")
    expect(page.locator("#hardlink-check")).to_have_attribute(
        "data-status", "failed", timeout=SLOW_MS)
