"""E2E: Classic CodeBuddy runtime hooks and the local observability dashboard.

Classic stages are orchestrated by the host Agent from Markdown rules, so the
deterministic parts we can drive end to end are the hooks the host runs:
the auto-dispatch router between role agents, the observability hooks that
record a session, and the dashboard built from what they wrote.
"""

import functools
import json
import os
import shutil
import subprocess
import threading
import unittest
import urllib.request
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer

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
        self.env = dict(os.environ, CODEBUDDY_PROJECT_DIR=str(self.project), PYTHONDONTWRITEBYTECODE="1")
        for key in list(self.env):
            if key.startswith(("CLS_", "TENCENTCLOUD_", "TC_")):
                del self.env[key]  # the dashboard must work fully offline
        self.state_path = self.project / "artifacts/e2e-classic/workflow-state.json"
        self.state_path.parent.mkdir(parents=True)
        shutil.copy(str(self.project / ".codebuddy/assets/workflow-state-template.json"), str(self.state_path))

    def hook(self, command, payload):
        result = subprocess.run(
            command, input=json.dumps(payload), cwd=str(self.project), env=self.env,
            capture_output=True, text=True, timeout=60,
        )
        self.assertEqual(0, result.returncode, result.stderr)
        return result.stdout

    def dispatch(self, sender, event, target, **extra):
        payload = {"event": event, "dispatch_prompt": "continue as " + target,
                   "next_target": {"subagent_name": target},
                   "workflow_state_path": str(self.state_path)}
        payload.update(extra)
        output = self.hook(
            ["python3", str(self.project / ".codebuddy/hooks/devflow-auto-dispatch.py")],
            {"tool_name": "SendMessage", "tool_input": {
                "type": "message", "from": sender, "recipient": "main",
                "content": json.dumps(payload)}},
        )
        return json.loads(output)

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
        skill = self.project / ".codebuddy/skills/agent-observability"
        transcript = self.project / "session.jsonl"
        shutil.copy(str(h.REPO / ".codebuddy/skills/agent-observability/tests/fixtures/transcript.jsonl"),
                    str(transcript))
        base = {"session_id": "e2e-session", "cwd": str(self.project), "transcript_path": str(transcript)}
        tool = dict(base, tool_name="Bash", tool_input={"command": "ls"}, tool_use_id="call-002")
        run_hook = [str(skill / "scripts/run_hook.sh")]
        self.hook(run_hook + ["session-start"], base)
        self.hook(run_hook + ["user-prompt-submit"], dict(base, prompt="/start-devflow fix slugify"))
        self.hook(run_hook + ["pre"], tool)
        self.hook(run_hook + ["post"], dict(tool, tool_response={"stdout": "ok"}))
        self.dispatch("requirement-owner", "TASK-01_completed", "architect")
        self.hook(run_hook + ["stop"], base)

        built = subprocess.run(
            ["python3", str(skill / "scripts/build_dashboard_data.py"), "--project-root", str(self.project)],
            cwd=str(self.project), env=self.env, capture_output=True, text=True, check=True,
        ).stdout
        self.assertIn("1 sessions, 1 devflow runs", built)

        dashboard = skill / "scripts/dashboard"
        handler = functools.partial(_QuietHandler, directory=str(dashboard))
        server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        url = "http://127.0.0.1:{}/".format(server.server_address[1])
        page = urllib.request.urlopen(url, timeout=10).read().decode("utf-8")
        self.assertIn("./dashboard-data.json", page)
        data = json.loads(urllib.request.urlopen(url + "dashboard-data.json", timeout=10).read())
        sessions = data["sessions"]
        self.assertEqual(["e2e-session"], [item["id"] for item in sessions])
        self.assertGreaterEqual(sessions[0]["toolCalls"], 1)
        self.assertGreater(sessions[0]["tokens"], 0, "token usage from the transcript is missing")
        self.assertEqual(1, data["daily"][0]["sessionCount"])
        self.assertEqual(["e2e-classic"], [run["slug"] for run in data["devflowRuns"]])
        self.assertEqual(1, data["autoDispatchStats"]["auto_dispatch"])

class _QuietHandler(SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass


if __name__ == "__main__":
    unittest.main()
