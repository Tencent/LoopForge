"""E2E: Classic CodeBuddy runtime hooks and the local observability dashboard.

Classic stages are orchestrated by the host Agent from Markdown rules, so the
deterministic parts we can drive end to end are the hooks the host runs:
the auto-dispatch router between role agents, the observability hooks that
record a session, and the dashboard built from what they wrote.
"""

import json
import unittest
import urllib.request

import harness as h

ROUTE = [
    ("requirement-owner", "TASK-01_completed", "architect"),
    ("architect", "TASK-02_completed", "developer"),
    ("developer", "TASK-03_completed", "code-reviewer"),
    ("code-reviewer", "CODE-REVIEW_passed", "test-engineer"),
    ("test-engineer", "TASK-04_completed", "knowledge-engineer"),
    ("knowledge-engineer", "TASK-05_completed", "leader"),
]


class ClassicCodeBuddyE2E(unittest.TestCase):
    def setUp(self):
        self.project = h.new_project(self)
        h.loopforge("install", "codebuddy", "--project-root", self.project)
        self.state_path = h.seed_classic_run(self.project)

    def dispatch(self, sender, event, target):
        return h.send_dispatch(self.project, sender, event, target, self.state_path)

    def test_auto_dispatch_routes_each_completed_stage_to_the_next_role(self):
        for sender, event, target in ROUTE:
            with self.subTest(event=event):
                decision = json.dumps(self.dispatch(sender, event, target))
                self.assertIn('"recipient": "{}"'.format(target), decision)
        decisions = json.loads(self.state_path.read_text(encoding="utf-8"))["decisions"]
        self.assertEqual([event for _, event, _ in ROUTE], [d["event"] for d in decisions])

    def test_auto_dispatch_falls_back_to_main_when_routing_is_unsafe(self):
        cases = {
            "mismatched target": ("architect", "TASK-02_completed", "leader"),
            "unknown event": ("developer", "TASK-03_failed", "code-reviewer"),
            "workflow end": ("leader", "workflow_completed", "main"),
        }
        for name, (sender, event, target) in cases.items():
            with self.subTest(name):
                self.assertNotIn("modifiedInput", json.dumps(self.dispatch(sender, event, target)))
        (self.project / ".codebuddy/devflow.config.yaml").write_text(
            "multi:\n  auto_mode: false\n", encoding="utf-8")
        with self.subTest("manual gate"):
            gated = self.dispatch("architect", "TASK-02_completed", "developer")
            self.assertNotIn("modifiedInput", json.dumps(gated))

    def test_recorded_session_shows_up_in_the_local_dashboard(self):
        h.record_classic_session(self.project, self.state_path)
        built, dashboard = h.build_dashboard(self.project)
        self.assertIn("1 sessions, 1 devflow runs", built)

        server = h.StaticServer(self, dashboard)
        page = urllib.request.urlopen(server.url, timeout=10).read().decode("utf-8")
        self.assertIn("./dashboard-data.json", page)
        data = json.loads(urllib.request.urlopen(server.url + "dashboard-data.json", timeout=10).read())
        sessions = data["sessions"]
        self.assertEqual(["e2e-session"], [item["id"] for item in sessions])
        self.assertGreaterEqual(sessions[0]["toolCalls"], 1)
        self.assertGreater(sessions[0]["tokens"], 0, "token usage from the transcript is missing")
        self.assertEqual(1, data["daily"][0]["sessionCount"])
        self.assertEqual(["e2e-classic"], [run["slug"] for run in data["devflowRuns"]])
        self.assertEqual(1, data["autoDispatchStats"]["auto_dispatch"])

if __name__ == "__main__":
    unittest.main()
