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

    retry = card.get_by_role("button", name="Retry")
    expect(retry).to_be_visible()
    retry.click()
    expect(page.locator("#toast-container")).to_contain_text("Retrying (#1)")
    expect(card).to_contain_text("Turnstile", timeout=SLOW_MS)
    # A retry reuses the failed row instead of creating a duplicate card.
    expect(page.locator("#downloads > div", has_text=title)).to_have_count(1)

    health = _vimm_health(base)
    assert health.get("download_fail", 0) >= 2, health
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


def test_flaresolverr_env_restores_vimm_download(gamarr_factory, stub_server, page):
    """A stale first solve is recovered in-session, then feeds the DDL path."""
    _api(stub_server, "/stub/flaresolverr-reset", {})
    # Extra session-lived instances must not watch the suite's shared torrent
    # stub; this test exercises only the direct-download path.
    app = gamarr_factory(
        "vimm-flaresolverr",
        QB_URL="http://127.0.0.1:1/",
        FLARESOLVERR_URL=stub_server,
        FLARESOLVERR_MAX_TIMEOUT="55000",
        FLARESOLVERR_TABS_TILL_VERIFY="74",
    )
    page.goto(app["base"], wait_until="networkidle")
    page.locator('#main-nav button[data-tab="settings"]').click()
    page.locator("#test-flaresolverr-status").locator("xpath=ancestor-or-self::button").click()
    expect(page.locator("#test-flaresolverr-status")).to_have_text(
        "Connected (3.5.0-e2e)", timeout=SLOW_MS)

    title = "FlareSolverr Vimm Game"
    _start_vimm_download(app["base"], title)
    page.locator('#main-nav button[data-tab="downloads"]').click()
    card = page.locator("#downloads > div", has_text=title)
    expect(card).to_contain_text("completed", timeout=SLOW_MS)
    assert "Turnstile" not in card.inner_text()

    calls = json.loads(urllib.request.urlopen(
        f"{stub_server}/stub/flaresolverr-calls", timeout=5).read())
    assert [call["cmd"] for call in calls] == [
        "sessions.create", "request.get", "request.get", "sessions.destroy",
    ]
    session = calls[0]["session"]
    assert session
    assert all(call["session"] == session for call in calls)

    first, followup = calls[1], calls[2]
    assert first["url"].endswith("/vault/1654")
    assert followup["url"] == first["url"]
    assert first["maxTimeout"] == followup["maxTimeout"] == 55000
    assert first["waitInSeconds"] == 5
    assert first["tabs_till_verify"] == 74
    assert followup["waitInSeconds"] == 2
    assert "tabs_till_verify" not in followup
