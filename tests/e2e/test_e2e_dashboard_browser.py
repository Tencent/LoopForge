"""E2E: render the local observability dashboard in a real browser (Playwright).

Records a CodeBuddy session through the installed hooks, builds the dashboard
snapshot, serves it on 127.0.0.1 and checks what a user actually sees: the
data badge, KPI strip, session table, Devflow run card and auto-dispatch
health, plus the empty and load-failure states. All requests that leave
127.0.0.1 (for example web fonts) are blocked, so the page must work offline.

Skipped when the ``playwright`` Python package is not installed, unless
``LOOPFORGE_E2E_BROWSER=1`` (set in CI), in which case a missing browser fails.
Screenshots go to ``$LOOPFORGE_E2E_ARTIFACTS`` when set.
"""

import json
import os
import tempfile
import unittest
from pathlib import Path

import harness as h

try:
    from playwright.sync_api import sync_playwright
except ImportError:  # pragma: no cover - depends on the environment
    sync_playwright = None

REQUIRED = os.environ.get("LOOPFORGE_E2E_BROWSER") == "1"


def setUpModule():
    if sync_playwright is None:
        if REQUIRED:
            raise RuntimeError("LOOPFORGE_E2E_BROWSER=1 but the playwright package is not installed")
        raise unittest.SkipTest("playwright is not installed (pip install playwright && playwright install chromium)")


class DashboardBrowserE2E(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.playwright = sync_playwright().start()
        cls.browser = cls.playwright.chromium.launch()
        cls.shots = Path(os.environ.get("LOOPFORGE_E2E_ARTIFACTS") or tempfile.mkdtemp(prefix="loopforge-e2e-shots-"))
        cls.shots.mkdir(parents=True, exist_ok=True)

    @classmethod
    def tearDownClass(cls):
        cls.browser.close()
        cls.playwright.stop()

    def setUp(self):
        self.project = h.new_project(self, with_sample=False)
        h.loopforge("install", "codebuddy", "--project-root", self.project)
        self.state_path = h.seed_classic_run(self.project)

    def open(self, directory):
        server = h.StaticServer(self, directory)
        page = self.browser.new_page(viewport={"width": 1440, "height": 1000})
        self.addCleanup(page.close)
        errors = []
        page.on("pageerror", lambda error: errors.append(str(error)))
        page.on("console", lambda msg: errors.append(msg.text) if msg.type == "error" and "ERR_FAILED" not in msg.text else None)

        def offline(route):
            if route.request.url.startswith(server.url):
                route.continue_()
            else:
                route.abort()

        page.route("**/*", offline)
        page.goto(server.url, wait_until="load")
        return page, errors

    def test_recorded_session_renders_in_every_panel(self):
        h.record_classic_session(self.project, self.state_path)
        _, dashboard = h.build_dashboard(self.project)
        page, errors = self.open(dashboard)

        badge = page.locator("#dataStatusBadge")
        badge.filter(has_text="真实数据").wait_for(timeout=10000)
        self.assertIn("条事件", badge.inner_text())
        self.assertTrue(page.locator("#kpiStrip").inner_text().strip())
        self.assertEqual(1, page.locator("#dailyTable tbody tr").count())
        page.screenshot(path=str(self.shots / "dashboard-overview.png"), full_page=False)

        page.click('button[data-tab="sessions"]')
        self.assertTrue(page.locator("#panel-sessions").is_visible())
        self.assertIn("e2e-session", page.locator("#sessionTable").inner_text())

        page.click('button[data-tab="devflow"]')
        self.assertTrue(page.locator("#panel-devflow").is_visible())
        run_card = page.locator("#devflowRuns").inner_text()
        self.assertIn("e2e-classic", run_card)
        # The seeded run has only finished Phase 0, so it must not claim to be done.
        self.assertIn("进行中", run_card)
        self.assertNotIn("已完成", run_card)
        self.assertEqual("1", page.locator("#hDispatch").inner_text().strip())
        page.screenshot(path=str(self.shots / "dashboard-devflow.png"), full_page=False)
        self.assertEqual([], errors)

    def test_empty_snapshot_explains_how_to_get_data(self):
        _, dashboard = h.build_dashboard(self.project)
        data = json.loads((dashboard / "dashboard-data.json").read_text(encoding="utf-8"))
        self.assertEqual([], data["sessions"])
        page, errors = self.open(dashboard)
        page.locator("#dataStatusBadge").filter(has_text="还没有数据").wait_for(timeout=10000)
        page.click('button[data-tab="sessions"]')
        self.assertIn("暂无会话记录", page.locator("#sessionTable").inner_text())
        self.assertEqual([], errors)

    def test_missing_snapshot_shows_a_load_error(self):
        dashboard = self.project / ".codebuddy/skills/agent-observability/scripts/dashboard"
        self.assertFalse((dashboard / "dashboard-data.json").exists())
        page, _ = self.open(dashboard)
        page.locator("#dataStatusBadge").filter(has_text="加载 dashboard-data.json 失败").wait_for(timeout=10000)
        self.assertIn("build_dashboard_data.py", page.locator("footer.note").inner_text())


if __name__ == "__main__":
    unittest.main()
