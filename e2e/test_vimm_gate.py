"""Playwright coverage for issue #37: Vimm's Lair downloads behind Turnstile.

The site still answers searches, but every vault page a non-browser client
fetches is a Cloudflare Turnstile challenge with no download form. Before this
the job died as "Could not find download form on Vimm" — a parsing complaint
about a page that was never going to carry a form — while the sources panel
kept reporting the source healthy at score 100 with zero failed downloads.

So the flow is driven the way the reporter hit it: a Vimm download is started,
the Downloads tab is read as a user would read it, and the health the API
reports is checked against what actually happened.
"""
import json
import re
import urllib.request

from playwright.sync_api import expect

SLOW_MS = 15_000


def _api(base: str, path: str, payload: dict | None = None):
    data = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(
        f"{base}{path}", data=data,
        headers={"Content-Type": "application/json"} if data else {},
        method="POST" if data else "GET",
    )
    return json.loads(urllib.request.urlopen(req, timeout=10).read())


def _vimm_health(base: str) -> dict:
    sources = _api(base, "/api/sources")["sources"]
    vimm = next(s for s in sources if s["name"] == "vimm")
    return vimm.get("health") or {}


def _start_vimm_download(base: str, title: str) -> str:
    r = _api(base, "/api/download", {
        "source_type": "ddl", "vimm_id": "1654",
        "title": title, "platform": "SNES", "platform_slug": "snes",
    })
    assert r.get("success") and r.get("job_id"), r
    return r["job_id"]


def test_vimm_download_names_the_gate(ui):
    page, base = ui["page"], ui["base"]
    title = "Super Metroid (Japan, USA)"
    _start_vimm_download(base, title)

    page.locator('#main-nav button[data-tab="downloads"]').click()
    expect(page.locator("#tab-downloads")).to_be_visible(timeout=SLOW_MS)
    card = page.locator("#downloads > div", has_text=title)
    expect(card).to_contain_text("error", timeout=SLOW_MS)
    # The user reads the cause, not a selector complaint.
    expect(card).to_contain_text("Turnstile", timeout=SLOW_MS)
    assert "Could not find download form" not in card.inner_text()

    health = _vimm_health(base)
    assert health.get("download_fail", 0) >= 1, health
    assert health.get("last_error_kind") == "download", health
    assert "Turnstile" in health.get("last_error", ""), health
    assert health.get("score", 100) < 100, health


def test_repeated_vimm_failures_degrade_the_source(ui):
    base = ui["base"]
    for i in range(3):
        _start_vimm_download(base, f"Gated Game {i}")

    page = ui["page"]
    page.locator('#main-nav button[data-tab="downloads"]').click()
    expect(page.locator("#tab-downloads")).to_be_visible(timeout=SLOW_MS)
    for i in range(3):
        expect(page.locator("#downloads > div", has_text=f"Gated Game {i}")).to_contain_text(
            "Turnstile", timeout=SLOW_MS)

    health = _vimm_health(base)
    assert health.get("download_degraded") is True, health
    assert health.get("circuit_open") is True, health

    # Settings shows it too — an enabled dot alone would call Vimm healthy.
    page.locator('#main-nav button[data-tab="settings"]').click()
    expect(page.locator("#tab-settings")).to_be_visible(timeout=SLOW_MS)
    badge = page.locator('#settings-sources [data-source-degraded="vimm"]')
    expect(badge).to_have_text("downloads failing", timeout=SLOW_MS)
    expect(badge).to_have_attribute("title", re.compile("Turnstile"))
    expect(page.locator('#settings-sources [data-source-degraded="myrient"]')).to_have_count(0)
