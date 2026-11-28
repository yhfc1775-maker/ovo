"""
Page Object Models for FLVX E2E tests.
"""

import time
from typing import Optional

from playwright.sync_api import Page, Locator, expect


class BasePage:
    """Base page object with common functionality.

    Readiness: every navigation waits for domcontentloaded plus a
    concrete rendered signal (wait_ready), never for networkidle.
    networkidle is the wrong signal for this app: after login the
    dashboard polls every 5s (use-dashboard-data), warms 13 lazy
    route chunks ~2s after login (App.tsx scheduleRoutePreload),
    and the login page pulls an external Google Fonts stylesheet —
    any one slow/stuck request (Vite dev transforms under load, a
    hanging font fetch) then fails the whole wait even though the
    app itself is usable. Under CPU load that produced ~30
    Playwright TimeoutErrors in CI (run 45204820492); locally the
    same authenticated dashboard wait measured 8-11s under severe
    load, i.e. no margin left on a slower runner. A rendered
    element proves the page is ready; the network never has to go
    quiet.
    """

    def __init__(self, page: Page, base_url: str):
        self.page = page
        self.base_url = base_url

    def wait_ready(self):
        """Wait until the React app has mounted into #root.

        Subclasses extend this with a page-specific visible element.
        """
        self.page.wait_for_function(
            "document.getElementById('root') && "
            "document.getElementById('root').children.length > 0"
        )

    def navigate(self, path: str = ""):
        """Navigate to a specific path and wait until it is ready."""
        url = f"{self.base_url}{path}"
        self.page.goto(url, wait_until="domcontentloaded")
        self.wait_ready()

    def wait_for_url(self, pattern: str, timeout: int = 5000):
        """Wait for URL to match pattern."""
        self.page.wait_for_url(f"**{pattern}**", timeout=timeout)

    def screenshot(self, name: str):
        """Take a screenshot."""
        self.page.screenshot(path=f"/tmp/{name}.png")


class LoginPage(BasePage):
    """Login page object."""

    def __init__(self, page: Page, base_url: str):
        super().__init__(page, base_url)
        # Login form is English (vite-frontend/src/pages/index.tsx):
        # placeholders "admin@user" / "••••••••", button "Sign In".
        # The old Chinese placeholder selectors matched nothing, so every
        # LoginPage.login call failed at the fill step before any
        # assertion was reached (found while verifying R18-R2-A5-01:
        # the two seed tests never got as far as the /change-password
        # assertion the re-audit predicted).
        self.username_input: Locator = page.locator('input[placeholder="admin@user"]')
        self.password_input: Locator = page.locator('input[placeholder="••••••••"]')
        self.login_button: Locator = page.locator('button:has-text("Sign In")')
        # Error toasts are react-hot-toast (vite-frontend/src/pages/
        # index.tsx calls toast.error(...)); the library renders every
        # toast with role="status" (error toasts live 4s). The old
        # '[role="alert"], .toast' selector matched nothing.
        self.error_toast: Locator = page.locator('[role="status"]')
        # Last login failure, for the fixture assert message: a bare
        # False hides whether the POST was rejected (error toast) or the
        # app never navigated (still on the login page with no toast).
        self.last_login_error: str = ""

    def goto(self):
        """Navigate to login page."""
        self.navigate("/")

    def wait_ready(self):
        """Login form rendered — or the dashboard, if already logged in.

        goto("/") is used both logged-out (the login form renders)
        and logged-in via the reused storage state (LoginRoute
        redirects to /dashboard, so the form never appears). Wait for
        whichever landing the app takes, then for that page's rendered
        element. Waiting for the form unconditionally timed out the
        whole reuse_login setup under load (30s per test).
        """
        super().wait_ready()
        self.page.wait_for_function(
            "() => location.pathname !== '/' || "
            "!!document.querySelector('input[placeholder=\"admin@user\"]')"
        )
        if self.page.url.rstrip("/").endswith("/dashboard") or "/dashboard" in self.page.url:
            self.page.locator("nav").first.wait_for(state="visible")
        else:
            self.username_input.wait_for(state="visible")

    def login(self, username: str, password: str) -> bool:
        """Perform login action.

        Retries on the login rate limit (429 "请求过于频繁"): the
        suite performs ~20 logins in ~2 min against the backend's
        10/min IP bucket (login_rate_limit.go: the 11th attempt in a
        60s window is rejected; WriteJSON always answers HTTP 200, so
        the rejection is only visible as the toast), so a login can be
        rejected even with correct credentials — that is what killed
        test_navigate_to_user_page in full-suite runs. The landing
        wait polls URL + toast so a rejected attempt fails fast
        (~1s) instead of burning the whole wait; retries sleep 20s
        (three attempts cover any 60s window: a rejection lands at
        most ~40s after window start at suite pace). Worst case fits
        the 60s per-test timeout: goto + 2s + 20s + 2s + 20s + ~3s.
        """
        for attempt in range(3):
            self.username_input.fill(username)
            self.password_input.fill(password)
            self.login_button.click()
            outcome = self._wait_for_landing(timeout_ms=15000 if attempt == 0 else 10000)
            if outcome in ("dashboard", "change-password"):
                self.last_login_error = ""
                return True
            if outcome == "rate-limit" and attempt < 2:
                self.last_login_error = (
                    f"url={self.page.url} toast=rate-limit (retry {attempt + 2}/3 after 20s)"
                )
                self.page.wait_for_timeout(20000)
                continue
            toast = self.get_error_message() or "(no error toast)"
            self.last_login_error = (
                f"url={self.page.url} toast={toast} "
                "(landing timed out: still not on /dashboard or /change-password)"
            )
            return False
        toast = self.get_error_message() or "(no error toast)"
        self.last_login_error = (
            f"url={self.page.url} toast={toast} "
            "(landing timed out: still not on /dashboard or /change-password)"
        )
        return False

    def _wait_for_landing(self, timeout_ms: int) -> str:
        """Poll for the post-login landing.

        Returns "dashboard" / "change-password" on navigation,
        "rate-limit" if the 429 toast appears, "toast" for any other
        error toast, "timeout" otherwise. Polling (instead of
        wait_for_url with the default 'load' wait) also keeps a slow
        subresource (e.g. the hanging Google Fonts request) from
        failing a login whose POST already succeeded.

        The success toast ("登录成功") is NOT a failure: index.tsx
        shows it before navigate("/dashboard"), and under load the
        dashboard chunk can take a while to arrive, so the toast is
        already visible while the URL is still "/". Treating any
        visible [role="status"] toast as an error made every login
        lose that race under load (reproduced locally: the
        storage-state fixture failed 14/14 with
        "toast=登录成功" on /dashboard). Only a toast that is not
        the success toast counts as an error; a success toast just
        means navigation is in flight, so keep polling for the URL.
        """
        deadline = time.monotonic() + timeout_ms / 1000
        while True:
            url = self.page.url
            if "/dashboard" in url:
                return "dashboard"
            if "/change-password" in url:
                return "change-password"
            # The backend writes "请求过于频繁，请稍后再试" for 429, but the
            # login page surfaces its own wording ("登录尝试过于频繁，
            # 请稍后再试", index.tsx) — match the shared substring.
            if self.page.locator("text=过于频繁").count() > 0:
                return "rate-limit"
            if self._error_toast_visible():
                return "toast"
            if time.monotonic() >= deadline:
                return "timeout"
            self.page.wait_for_timeout(300)

    def _error_toast_visible(self) -> bool:
        """True only if a non-success toast is visible.

        react-hot-toast renders success and error toasts alike with
        role="status", so visibility alone cannot tell them apart —
        read the text (see _wait_for_landing).
        """
        try:
            toast = self.error_toast.first
            if not toast.is_visible():
                return False
            text = toast.text_content() or ""
            return "登录成功" not in text
        except Exception:
            return False

    def get_error_message(self) -> Optional[str]:
        """Get error message if present (ignores the success toast)."""
        try:
            toast = self.error_toast.first
            if toast.is_visible():
                text = toast.text_content()
                if text and "登录成功" in text:
                    return None
                return text
        except Exception:
            pass
        return None


class DashboardPage(BasePage):
    """Dashboard page object."""

    def __init__(self, page: Page, base_url: str):
        super().__init__(page, base_url)
        self.sidebar: Locator = page.locator("nav, [data-testid='sidebar']")
        # The real <button> lives on /profile (profile.tsx). The
        # dashboard's own "退出登录" is a DropdownItem and the sidebar
        # entry is a div with onClick — neither is a <button>, so a
        # button: selector must not be used against those pages.
        self.logout_button: Locator = page.locator('button:has-text("退出登录")')

    def goto(self):
        """Navigate to dashboard."""
        self.navigate("/dashboard")

    def wait_ready(self):
        """Admin layout sidebar rendered."""
        super().wait_ready()
        self.page.locator("nav").first.wait_for(state="visible")

    def is_authenticated(self) -> bool:
        """Check if user is authenticated on this page."""
        return self.page.url.endswith("/dashboard") or "/dashboard" in self.page.url

    def navigate_to(self, menu_item: str):
        """Navigate to a menu item.

        Sidebar entries are onClick SPA navigations: there is no
        document load to wait for, so wait for the URL itself to
        change (the real signal) instead of networkidle.
        """
        old_url = self.page.url
        self.page.click(f'text="{menu_item}"')
        self.page.wait_for_url(lambda url: url != old_url, timeout=15000)


class UserPage(BasePage):
    """User management page object."""

    def __init__(self, page: Page, base_url: str):
        super().__init__(page, base_url)
        self.create_button: Locator = page.locator('button:has-text("创建"), button:has-text("新增")')
        self.user_table: Locator = page.locator("table")

    def goto(self):
        """Navigate to user management page."""
        self.navigate("/user")

    def wait_ready(self):
        """User page shell rendered (create button visible).

        The page defaults to grid view, so no <table> exists until
        the list toggle is pressed — the create button is the stable
        rendered signal, not the table.
        """
        super().wait_ready()
        self.create_button.first.wait_for(state="visible")

    def create_user(self, username: str, password: str, **kwargs):
        """Create a new user.

        Success is the created row rendering in the list, not the
        network going quiet.
        """
        self.create_button.click()
        page = self.page

        page.fill('input[placeholder*="用户名"], input[name="username"]', username)
        page.fill('input[placeholder*="密码"], input[name="password"]', password)

        if kwargs.get("name"):
            page.fill('input[placeholder*="名称"], input[name="name"]', kwargs["name"])

        page.click('button:has-text("确定"), button:has-text("提交")')
        # The created user renders as a card in the default grid view
        # (or a row in list view) — wait for its name to appear.
        page.locator(f"text={username}").first.wait_for(state="visible")

    def delete_user(self, username: str):
        """Delete a user by username.

        Success is the row leaving the list, not the network going
        quiet.
        """
        row = self.page.locator(f"tr:has-text('{username}')")
        row.locator('button:has-text("删除")').click()
        self.page.click('button:has-text("确认")')
        row.first.wait_for(state="detached")


class NodePage(BasePage):
    """Node management page object."""

    def __init__(self, page: Page, base_url: str):
        super().__init__(page, base_url)
        self.create_button: Locator = page.locator('button:has-text("创建"), button:has-text("新增")')
        self.node_list: Locator = page.locator("[data-testid='node-list'], table, .node-item")

    def goto(self):
        """Navigate to node management page."""
        self.navigate("/node")

    def wait_ready(self):
        """Admin layout sidebar rendered."""
        super().wait_ready()
        self.page.locator("nav").first.wait_for(state="visible")

    def get_nodes(self) -> list[str]:
        """Get list of node names."""
        nodes = []
        for item in self.page.locator("tr td:first-child, .node-name").all():
            text = item.text_content()
            if text and text.strip():
                nodes.append(text.strip())
        return nodes


class TunnelPage(BasePage):
    """Tunnel management page object."""

    def __init__(self, page: Page, base_url: str):
        super().__init__(page, base_url)
        self.create_button: Locator = page.locator('button:has-text("创建"), button:has-text("新增")')

    def goto(self):
        """Navigate to tunnel management page."""
        self.navigate("/tunnel")

    def wait_ready(self):
        """Admin layout sidebar rendered."""
        super().wait_ready()
        self.page.locator("nav").first.wait_for(state="visible")


class ForwardPage(BasePage):
    """Forward management page object."""

    def __init__(self, page: Page, base_url: str):
        super().__init__(page, base_url)
        self.create_button: Locator = page.locator('button:has-text("创建"), button:has-text("新增")')

    def goto(self):
        """Navigate to forward management page."""
        self.navigate("/forward")

    def wait_ready(self):
        """Admin layout sidebar rendered."""
        super().wait_ready()
        self.page.locator("nav").first.wait_for(state="visible")


class ConfigPage(BasePage):
    """Configuration page object."""

    def __init__(self, page: Page, base_url: str):
        super().__init__(page, base_url)
        self.save_button: Locator = page.locator('button:has-text("保存"), button:has-text("提交")')

    def goto(self):
        """Navigate to config page."""
        self.navigate("/config")

    def wait_ready(self):
        """Admin layout sidebar rendered."""
        super().wait_ready()
        self.page.locator("nav").first.wait_for(state="visible")
