"""Playwright coverage for Prowlarr indexer discovery (issue #17).

Prowlarr numbers indexers in the order each user added them, so the old default
list of IDs addressed a different set of trackers on every install. Querying an
ID the instance does not have returns HTTP 200 and an empty list, so the search
looked like "that game isn't available" rather than "the tracker that has it was
never asked".

The stub Prowlarr here is shaped like the reported instance: the only indexer
carrying games sits at ID 16, next to a books-only tracker and a disabled one,
and only ID 16 answers searches. A fresh install with nothing configured has to
find it, and the UI has to say which indexers it used.
"""
from playwright.sync_api import expect

SLOW_MS = 15_000


def _search(page, query: str, platform: str = "all"):
    page.locator('#main-nav button[data-tab="search"]').click()
    expect(page.locator("#tab-search")).to_be_visible(timeout=SLOW_MS)
    page.locator("#search-input").fill(query)
    page.locator("#platform-filter").select_option(platform)
    page.locator("#search-btn").click()
    expect(page.locator("#search-info")).to_contain_text("results in", timeout=SLOW_MS)


def test_search_finds_a_tracker_no_default_would_have_guessed(ui):
    """Out of the box, with no PROWLARR_GAME_INDEXERS set."""
    page = ui["page"]
    _search(page, "stardew")

    expect(page.locator("#results")).to_contain_text(
        "Stardew Harvest Deluxe Edition", timeout=SLOW_MS)


def test_search_names_the_indexers_it_queried(ui):
    """An empty result list is ambiguous unless the page says who was asked."""
    page = ui["page"]
    _search(page, "stardew")

    line = page.locator("#search-indexers")
    expect(line).to_contain_text("GamesTracker", timeout=SLOW_MS)
    # The trackers that carry no games are not searched, and not claimed to be.
    assert "BooksOnly" not in line.inner_text()
    assert "RetiredGames" not in line.inner_text()
    expect(line).to_have_attribute("data-count", "1")


def test_a_search_with_no_hits_still_shows_what_was_searched(ui):
    """The diagnostic that matters: nothing found, but the right tracker asked."""
    page = ui["page"]
    _search(page, "nosuchgametitle")

    expect(page.locator("#results")).to_contain_text("No results found", timeout=SLOW_MS)
    expect(page.locator("#search-indexers")).to_contain_text("GamesTracker")
