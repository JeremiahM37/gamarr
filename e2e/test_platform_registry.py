"""Real-browser coverage for Newznab and generic retro-platform searches."""
import conftest
from playwright.sync_api import expect


def test_console_categories_and_retro_context(ui, monkeypatch):
    releases = []
    for label, category in [("DS", 1010), ("Generic", 1090), ("WrongPlatform", 1110)]:
        releases.append({
            "title": f"RegistryFixture {label}", "size": 32_000_000,
            "indexer": "StubIndexer", "protocol": "usenet",
            "downloadUrl": "http://127.0.0.1:1/fixture.nzb",
            "guid": f"registry-{label}", "categories": [{"id": category}],
        })
    monkeypatch.setattr(conftest, "PROWLARR_RELEASES", releases)
    page = ui["page"]
    page.locator("#search-input").fill("RegistryFixture")
    for slug, expected in [("nds", {"DS", "Generic"}), ("gbc", {"Generic"})]:
        page.locator("#platform-filter").select_option(slug)
        with page.expect_response(lambda r: "/api/search?" in r.url) as reply:
            page.locator("#search-btn").click()
        data = reply.value.json()["results"]
        assert {r["title"].split()[-1] for r in data} == expected
        assert all(r["platform_slug"] == slug for r in data)
        assert all(r["download_protocol"] == "nzb" for r in data)
        expect(page.locator("#results")).to_contain_text("RegistryFixture Generic")
        expect(page.locator("#results")).not_to_contain_text("WrongPlatform")
    expect(page.locator("#results")).to_contain_text("Game Boy Color")


def test_manual_organize_lists_registry_platforms(ui):
    # Keep the unfinished download deterministic; the platform API and UI are real.
    page = ui["page"]
    page.route("**/api/downloads", lambda route: route.fulfill(json={"downloads": [{
        "title": "Manual Platform Fixture", "hash": "registry-fixture",
        "status": "completed_unorganized", "progress": 100,
    }]}))
    with page.expect_response(lambda r: r.url.endswith("/api/platforms")) as reply:
        page.locator('#main-nav button[data-tab="downloads"]').click()
    slugs = [p["id"] for p in reply.value.json()["platforms"] if p["id"] != "all"]
    dialogs = []

    def inspect(dialog):
        dialogs.append(dialog.message)
        dialog.dismiss()

    page.once("dialog", inspect)
    page.get_by_role("button", name="Organize", exact=True).click()
    assert dialogs == [f"Platform? ({', '.join(slugs)})"]
    assert "gbc" in slugs and "sms" in slugs
