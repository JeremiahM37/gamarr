"""Playwright coverage for the sign-in UI (issue #21).

Every other e2e module boots gamarr with no credentials configured, which
sends the auth middleware straight down its "no users, no legacy auth, no API
key -> pass through as admin" branch. That deployment cannot fail the way a
configured one does, so the whole suite was blind to a frontend that never
authenticated: the UI rendered, every protected request 401'd, and each caller
swallowed its own error.

These tests boot gamarr the way the reporter did — AUTH_USERNAME/AUTH_PASSWORD
set — plus a multi-user instance and an API-key-only instance, and assert on
both the DOM and the wire.
"""
import json
import urllib.request

import pytest
from playwright.sync_api import expect

from conftest import watch_page

SLOW_MS = 15_000

USERNAME = "e2e-admin"
PASSWORD = "e2e-password"

# Every instance in the session shares one stub download client, and every
# gamarr runs a watcher that imports whatever it finds finished there. An extra
# instance therefore races the import tests for their armed torrents, so these
# ones get a dead client: nothing here needs a download, and an auth test must
# not be able to steal another test's torrent.
ISOLATED_CLIENT = {"QB_URL": "http://127.0.0.1:1/"}


# ── instances ─────────────────────────────────────────────────────────────────

@pytest.fixture(scope="session")
def secured_app(gamarr_factory):
    """Legacy single-user mode: exactly the reporter's deployment."""
    return gamarr_factory(
        "secured", AUTH_USERNAME=USERNAME, AUTH_PASSWORD=PASSWORD, **ISOLATED_CLIENT)


@pytest.fixture(scope="session")
def multiuser_app(gamarr_factory):
    """Multi-user mode: an unclaimed instance that registers its first user,
    which flips auth on for everyone."""
    inst = gamarr_factory("multiuser", **ISOLATED_CLIENT)
    req = urllib.request.Request(
        f"{inst['base']}/api/register",
        data=json.dumps({"username": "alice", "password": "secret123"}).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=5) as r:
        assert r.status == 201, f"first-user registration failed: {r.status}"
    return inst


@pytest.fixture(scope="session")
def apikey_app(gamarr_factory):
    """API-key-only: protected, but with no password a browser could submit."""
    return gamarr_factory("apikey", API_KEY="e2e-api-key", **ISOLATED_CLIENT)


def open_ui(page, inst):
    """Load an instance with the JS-error and 401 recorders attached."""
    watched = watch_page(page)
    page.goto(inst["base"], wait_until="networkidle")
    return {"page": page, **watched, **inst}


def sign_in(page, username=USERNAME, password=PASSWORD):
    page.locator("#login-username").fill(username)
    page.locator("#login-password").fill(password)
    page.locator("#login-btn").click()


# ── the gate itself ───────────────────────────────────────────────────────────

def test_configured_instance_shows_a_login_form(page, secured_app):
    """The bug as filed: the app shell rendered and no login prompt appeared."""
    ui = open_ui(page, secured_app)
    expect(page.locator("#auth-gate")).to_be_visible(timeout=SLOW_MS)
    expect(page.locator("#login-form")).to_be_visible()
    expect(page.locator("#login-username")).to_be_visible()
    expect(page.locator("#login-password")).to_be_visible()
    # The app shell must not be reachable behind the gate.
    expect(page.locator("#app-root")).to_be_hidden()
    expect(page.locator("#main-nav")).to_be_hidden()
    assert ui["errors"] == [], ui["errors"]


def test_gate_fires_no_protected_requests(page, secured_app):
    """The heart of #21: on load the frontend asked for /api/platforms and
    /api/config before it had a session, got 401s, and hid them. Nothing
    protected may leave the browser until the user is authenticated."""
    ui = open_ui(page, secured_app)
    expect(page.locator("#auth-gate")).to_be_visible(timeout=SLOW_MS)
    page.wait_for_timeout(1000)  # let any stray on-load fetches land
    assert ui["unauthorized"] == [], (
        f"UI fired protected requests before sign-in: {ui['unauthorized']}")


def test_wrong_password_is_reported(page, secured_app):
    ui = open_ui(page, secured_app)
    sign_in(page, USERNAME, "not-the-password")
    expect(page.locator("#auth-error")).to_contain_text("Invalid credentials", timeout=SLOW_MS)
    expect(page.locator("#auth-gate")).to_be_visible()
    expect(page.locator("#app-root")).to_be_hidden()
    assert ui["errors"] == [], ui["errors"]


def test_empty_credentials_are_rejected_client_side(page, secured_app):
    open_ui(page, secured_app)
    page.locator("#login-btn").click()
    expect(page.locator("#auth-error")).to_contain_text("username and password", timeout=SLOW_MS)


# ── signing in ────────────────────────────────────────────────────────────────

def test_login_reveals_a_working_app(page, secured_app):
    """Past the gate the app must be genuinely functional — the issue's other
    symptom was an empty platform list behind a UI that looked fine."""
    ui = open_ui(page, secured_app)
    sign_in(page)

    expect(page.locator("#app-root")).to_be_visible(timeout=SLOW_MS)
    expect(page.locator("#auth-gate")).to_be_hidden()
    expect(page.locator("#user-name")).to_have_text(USERNAME)

    # Data that only a session can fetch actually arrived.
    expect(page.locator("#platform-filter option")).not_to_have_count(0, timeout=SLOW_MS)

    # And a real journey works end to end.
    page.locator('#main-nav button[data-tab="wishlist"]').click()
    page.locator("#wish-title").fill("Authenticated Quest")
    page.locator("#tab-wishlist button", has_text="Add").click()
    expect(page.locator("#wishlist")).to_contain_text("Authenticated Quest", timeout=SLOW_MS)

    assert ui["errors"] == [], ui["errors"]
    assert ui["unauthorized"] == [], ui["unauthorized"]


def test_session_survives_a_reload(page, secured_app):
    ui = open_ui(page, secured_app)
    sign_in(page)
    expect(page.locator("#app-root")).to_be_visible(timeout=SLOW_MS)

    page.reload(wait_until="networkidle")
    expect(page.locator("#app-root")).to_be_visible(timeout=SLOW_MS)
    expect(page.locator("#auth-gate")).to_be_hidden()
    assert ui["unauthorized"] == [], ui["unauthorized"]


def test_logout_returns_to_the_gate(page, secured_app):
    ui = open_ui(page, secured_app)
    sign_in(page)
    expect(page.locator("#app-root")).to_be_visible(timeout=SLOW_MS)

    page.locator("#logout-btn").click()
    expect(page.locator("#auth-gate")).to_be_visible(timeout=SLOW_MS)
    expect(page.locator("#app-root")).to_be_hidden()

    # The session is really gone, not just hidden.
    page.reload(wait_until="networkidle")
    expect(page.locator("#auth-gate")).to_be_visible(timeout=SLOW_MS)
    assert ui["errors"] == [], ui["errors"]


def test_expired_session_re_prompts(page, secured_app):
    """A session that dies mid-visit must raise the gate, not silently empty
    the panels — that failure mode is what made #21 so hard to diagnose."""
    ui = open_ui(page, secured_app)
    sign_in(page)
    expect(page.locator("#app-root")).to_be_visible(timeout=SLOW_MS)

    page.context.clear_cookies()
    page.locator('#main-nav button[data-tab="library"]').click()

    expect(page.locator("#auth-gate")).to_be_visible(timeout=SLOW_MS)
    expect(page.locator("#auth-error")).to_contain_text("session expired")
    assert ui["errors"] == [], ui["errors"]


# ── multi-user mode ───────────────────────────────────────────────────────────

def test_multiuser_instance_shows_a_login_form(page, multiuser_app):
    ui = open_ui(page, multiuser_app)
    expect(page.locator("#auth-gate")).to_be_visible(timeout=SLOW_MS)
    expect(page.locator("#app-root")).to_be_hidden()
    assert ui["unauthorized"] == [], ui["unauthorized"]

    sign_in(page, "alice", "secret123")
    expect(page.locator("#app-root")).to_be_visible(timeout=SLOW_MS)
    expect(page.locator("#user-name")).to_have_text("alice")
    assert ui["errors"] == [], ui["errors"]


def test_multiuser_gate_offers_invite_signup(page, multiuser_app):
    """An invited user needs a registration form; there was none anywhere."""
    open_ui(page, multiuser_app)
    toggle = page.locator("#switch-auth-mode")
    expect(toggle).to_be_visible(timeout=SLOW_MS)
    toggle.click()

    expect(page.locator("#register-form")).to_be_visible()
    expect(page.locator("#register-invite")).to_be_visible()
    expect(page.locator("#login-form")).to_be_hidden()

    # Registering without a valid invite is refused, and says so.
    page.locator("#register-username").fill("mallory")
    page.locator("#register-password").fill("secret123")
    page.locator("#register-btn").click()
    expect(page.locator("#auth-error")).to_contain_text("invite", timeout=SLOW_MS)

    toggle.click()
    expect(page.locator("#login-form")).to_be_visible()


# ── API-key-only mode ─────────────────────────────────────────────────────────

def test_apikey_only_instance_explains_itself(page, apikey_app):
    """No password exists on this deployment, so a login form would be a dead
    end. The gate says what the instance wants instead."""
    ui = open_ui(page, apikey_app)
    expect(page.locator("#auth-gate")).to_be_visible(timeout=SLOW_MS)
    expect(page.locator("#auth-notice")).to_contain_text("API key", timeout=SLOW_MS)
    expect(page.locator("#login-form")).to_be_hidden()
    expect(page.locator("#app-root")).to_be_hidden()
    assert ui["errors"] == [], ui["errors"]


# ── the open deployment must stay open ────────────────────────────────────────

def test_unconfigured_instance_never_gates(page, app):
    """Most gamarr installs configure no auth at all. They must keep loading
    straight into the app — the gate is not allowed to appear there."""
    ui = open_ui(page, app)
    expect(page.locator("#app-root")).to_be_visible(timeout=SLOW_MS)
    expect(page.locator("#auth-gate")).to_be_hidden()
    expect(page.locator("#user-chip")).to_be_hidden()
    expect(page.locator("#platform-filter option")).not_to_have_count(0, timeout=SLOW_MS)
    assert ui["errors"] == [], ui["errors"]
    assert ui["unauthorized"] == [], ui["unauthorized"]
