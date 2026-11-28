"""
Test dashboard and navigation for OVO.
Tests dashboard rendering, sidebar navigation, and user interactions.
"""

import pytest
from playwright.sync_api import Page, expect

from pages import ConfigPage, ForwardPage, LoginPage, NodePage, TunnelPage, UserPage


@pytest.mark.e2e
@pytest.mark.reuse_login
class TestDashboard:
    """Dashboard E2E tests."""

    @pytest.fixture(autouse=True)
    def login(self, page: Page, frontend_url: str):
        """Start authenticated via the reused storage state (reuse_login).

        The class does no per-test form login: the suite's ~20 logins
        in ~2 min hit the backend's 10/min login bucket, so form
        logins get 429'd mid-run. goto("/") redirects an authenticated
        session to /dashboard (LoginRoute); if the state is stale the
        wait times out and the test fails loudly instead of silently
        running logged out.
        """
        login_page = LoginPage(page, frontend_url)
        login_page.goto()
        page.wait_for_url("**/dashboard**", timeout=15000, wait_until="commit")

    def test_login_redirects_to_dashboard(self, page: Page, frontend_url: str):
        """Test that login with a changed admin password redirects to dashboard."""
        assert "/dashboard" in page.url

        # count() does not auto-wait: wait for the sidebar to render
        # first, then assert it exists.
        expect(page.locator("nav, [data-testid='sidebar'], aside").first).to_be_visible()
        assert page.locator("nav, [data-testid='sidebar'], aside").count() > 0

    def test_dashboard_shows_overview(self, page: Page):
        """Test that the dashboard renders its overview for the session.

        The routed dashboard (vite-frontend/src/pages/dashboard/
        admin-overview.tsx) renders no username greeting — the greeting
        in src/pages/dashboard.tsx belongs to an unrouted file — so
        this asserts what the page actually shows: the 概览 heading
        and the session's stat cards once its data has loaded.
        The expects below auto-wait for the content; no networkidle
        gate is needed (or valid) here.
        """
        expect(page.locator("h1").filter(has_text="概览").first).to_be_visible()
        expect(page.locator("text=节点在线").first).to_be_visible()

    def test_sidebar_navigation(self, page: Page, frontend_url: str):
        """Test sidebar navigation links."""
        # Sidebar items are <nav> entries with Chinese labels and onClick
        # navigation (layouts/admin.tsx) — not <a href> links, so locate
        # them by label inside the nav and assert each landing URL.
        # All four must be admin-visible: /forward ("转发") is
        # userOnly, so an admin never sees it — use /inbound ("节点")
        # instead. The link expects auto-wait for visibility, and the
        # URL wait below is the real signal that the SPA navigation
        # landed — no networkidle gate.
        nav_items = [
            ("节点", "/inbound"),
            ("服务器", "/node"),
            ("用户", "/user"),
            ("设置", "/config"),
        ]

        for label, path in nav_items:
            link = page.locator(f"nav >> text={label}")
            expect(link.first).to_be_visible()
            link.first.click()
            page.wait_for_url(f"**{path}**", timeout=15000)
            assert path in page.url, (
                f"After clicking sidebar {label!r}, expected {path} in URL, got {page.url}"
            )

    def test_dashboard_responsive_layout(self, page: Page, frontend_url: str):
        """Test dashboard responsive layout."""
        page.set_viewport_size({"width": 375, "height": 667})
        page.wait_for_timeout(500)

        # No horizontal overflow at mobile width, and the dashboard
        # heading is visible. (The routed dashboard renders no username
        # greeting — see test_dashboard_shows_overview — so assert the
        # 概览 heading instead of a "admin@user" text that never appears.)
        assert page.evaluate("document.documentElement.scrollWidth") <= 375, (
            "dashboard must not overflow horizontally at 375px width"
        )
        expect(page.locator("h1").filter(has_text="概览").first).to_be_visible()

        page.set_viewport_size({"width": 1920, "height": 1080})
        page.wait_for_timeout(500)

        expect(page.locator("nav").first).to_be_visible()


@pytest.mark.e2e
@pytest.mark.reuse_login
class TestNavigation:
    """Navigation E2E tests."""

    @pytest.fixture(autouse=True)
    def login(self, page: Page, frontend_url: str):
        """Start authenticated via the reused storage state (reuse_login).

        The class does no per-test form login: the suite's ~20 logins
        in ~2 min hit the backend's 10/min login bucket, so form
        logins get 429'd mid-run. goto("/") redirects an authenticated
        session to /dashboard (LoginRoute); if the state is stale the
        wait times out and the test fails loudly instead of silently
        running logged out.
        """
        login_page = LoginPage(page, frontend_url)
        login_page.goto()
        page.wait_for_url("**/dashboard**", timeout=15000, wait_until="commit")

    def test_navigate_to_user_page(self, page: Page, frontend_url: str):
        """Test navigation to user management page."""
        UserPage(page, frontend_url).goto()

        assert "/user" in page.url

    def test_navigate_to_node_page(self, page: Page, frontend_url: str):
        """Test navigation to node management page."""
        NodePage(page, frontend_url).goto()

        assert "/node" in page.url

    def test_navigate_to_tunnel_page(self, page: Page, frontend_url: str):
        """Test navigation to tunnel management page."""
        TunnelPage(page, frontend_url).goto()

        assert "/tunnel" in page.url

    def test_navigate_to_forward_page(self, page: Page, frontend_url: str):
        """Test navigation to forward management page."""
        ForwardPage(page, frontend_url).goto()

        assert "/forward" in page.url

    def test_navigate_to_config_page(self, page: Page, frontend_url: str):
        """Test navigation to config management page."""
        ConfigPage(page, frontend_url).goto()

        assert "/config" in page.url
