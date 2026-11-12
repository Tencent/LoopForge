"""E2E: Classic CodeBuddy runtime hooks and the local observability dashboard.

Classic stages are orchestrated by the host Agent from Markdown rules, so the
deterministic parts we can drive end to end are the hooks the host runs:
the auto-dispatch router between role agents, the observability hooks that
record a session, and the dashboard built from what they wrote.
"""

import json
import sys
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

    def dispatch_without_yaml(self, event="TASK-02_completed", target="developer"):
        hook = self.project / ".codebuddy/hooks/devflow-auto-dispatch.py"
        output = h.run_hook(
            self.project,
            [sys.executable, "-c",
             "import runpy, sys; sys.modules['yaml'] = None; "
             "runpy.run_path(sys.argv[1], run_name='__main__')", hook],
            {"tool_name": "SendMessage", "tool_input": {
                "type": "message", "from": "architect", "recipient": "main",
                "content": json.dumps({"event": event, "dispatch_prompt": "continue",
                                       "next_target": {"subagent_name": target}})}},
        )
        return json.loads(output)

    def test_manual_gates_without_yaml(self):
        config = self.project / ".codebuddy/devflow.config.yaml"
        for value in ("false", "true"):
            config.write_text("multi:\n  auto_mode: " + value + " # user choice\n",
                              encoding="utf-8")
            for _, event, target in (ROUTE[1], ROUTE[3], ROUTE[4]):
                with self.subTest(value=value, event=event):
                    output = self.dispatch_without_yaml(event, target)
                    self.assertEqual(value == "true", "hookSpecificOutput" in output)

    def test_unreadable_config_keeps_manual_gates_without_yaml(self):
        config = self.project / ".codebuddy/devflow.config.yaml"
        for content in ("multi: [", "multi:\n  auto_mode: perhaps\n",
                        "multi: {auto_mode: false}\n",
                        "multi:\n  auto_mode: true#invalid\n",
                        "multi:\n  auto_mode: true\n  auto_mode: false\n"):
            config.write_text(content, encoding="utf-8")
            with self.subTest(content=content):
                self.assertEqual({"continue": True}, self.dispatch_without_yaml())
                records = (self.project / ".codebuddy/hooks/logs/auto-dispatch.log").read_text()
                record = json.loads(records.splitlines()[-1])
                self.assertEqual("config_unreadable", record["reason"])
                self.assertEqual("fallback_to_main", record["decision"])
                self.assertIn("hookSpecificOutput", self.dispatch_without_yaml(
                    "TASK-01_completed", "architect"))

    def test_partial_override_preserves_default_manual_mode_without_yaml(self):
        defaults = self.project / ".codebuddy/assets/devflow.defaults.yaml"
        defaults.write_text("multi:\n  auto_mode: false\n", encoding="utf-8")
        (self.project / ".codebuddy/devflow.config.yaml").write_text(
            "multi:\n  compact_after_brainstorming: true\n", encoding="utf-8")
        self.assertEqual({"continue": True}, self.dispatch_without_yaml())

    def test_invalid_config_keeps_manual_gates_with_normal_python(self):
        config = self.project / ".codebuddy/devflow.config.yaml"
        for content in ("multi: [", "multi: false\n", "multi:\n  auto_mode: 'false'\n"):
            config.write_text(content, encoding="utf-8")
            with self.subTest(content=content):
                self.assertEqual({"continue": True},
                                 self.dispatch("architect", "TASK-02_completed", "developer"))
                records = (self.project / ".codebuddy/hooks/logs/auto-dispatch.log").read_text()
                self.assertEqual("config_unreadable", json.loads(records.splitlines()[-1])["reason"])

    def test_config_read_error_keeps_manual_gates_without_yaml(self):
        (self.project / ".codebuddy/devflow.config.yaml").mkdir()
        self.assertEqual({"continue": True}, self.dispatch_without_yaml())
        records = (self.project / ".codebuddy/hooks/logs/auto-dispatch.log").read_text()
        self.assertEqual("config_unreadable", json.loads(records.splitlines()[-1])["reason"])

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
