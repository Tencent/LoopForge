"""E2E: drive the installed Portable workflow end to end with a fake host Agent.

Each test installs the packaged CLI into a sample project, then runs
``fake_agent.py``, which follows the Skill's ``resume``-driven protocol exactly
as a host Agent would, using recorded stage outputs instead of an LLM.
"""

import json
import subprocess
import sys
import unittest

import harness as h

AGENT_STAGES = ("DESIGN", "IMPLEMENT", "REVIEW", "TEST")
STAGE_ORDER = ["REQUIREMENT", "DESIGN", "IMPLEMENT", "REVIEW", "TEST", "SUMMARY"]


def installed_adapter(project, host):
    from fake_agent import HOST_SKILL_DIRS
    path = project / HOST_SKILL_DIRS[host] / "devflow/adapters" / host / "manifest.json"
    return json.loads(path.read_text(encoding="utf-8"))


def events(project, kind):
    return [item for item in h.transcript(project) if item["kind"] == kind]


class PortableWorkflowE2E(unittest.TestCase):
    def install(self, host="claude"):
        project = h.new_project(self)
        h.loopforge("skills", "install", host, "--project-root", project)
        return project

    def assert_delivered(self, project):
        state = h.workflow_state(project)
        self.assertEqual("completed", state["status"], h.transcript_text(project))
        for stage in STAGE_ORDER:
            self.assertEqual("completed", state["stages"][stage]["status"], stage)
        self.assertEqual("skipped", state["stages"]["KNOWLEDGE"]["status"])
        verify = subprocess.run(
            [sys.executable, "-m", "unittest", "check_slugify"], cwd=str(project),
            capture_output=True, text=True,
        )
        self.assertEqual(0, verify.returncode, verify.stderr)
        self.assertIn("OK", h.loopforge_validate(project))
        self.assertEqual("user file that must survive\n",
                         (project / "USER_NOTES.md").read_text(encoding="utf-8"))
        return state

    def test_medium_task_runs_every_stage_on_every_host(self):
        for host in h.hosts_for("portable"):
            with self.subTest(host=host):
                project = self.install(host)
                h.fake_agent(host, project, check_rc=0)
                state = self.assert_delivered(project)

                finished = [e["stage"] for e in events(project, "finish") if e["result"] == "completed"]
                self.assertEqual(STAGE_ORDER, finished)
                prompts = events(project, "prompt")
                self.assertEqual(STAGE_ORDER, [p["stage"] for p in prompts])
                self.assertTrue(all(p["mentions_role"] for p in prompts), prompts)

                adapter = installed_adapter(project, host)
                spawned = {e["stage"]: e for e in events(project, "spawned")}
                self.assertEqual(set(AGENT_STAGES), set(spawned))
                ids = set()
                for stage in AGENT_STAGES:
                    recorded = state["stages"][stage]
                    self.assertEqual(adapter["stage_executor"], recorded["executor_type"])
                    self.assertEqual(adapter["dispatch_tool"], recorded["dispatch_tool"])
                    self.assertEqual(spawned[stage]["executor_id"], recorded["executor_id"])
                    ids.add(recorded["executor_id"])
                # Independent review: every agent stage has its own executor.
                self.assertEqual(len(AGENT_STAGES), len(ids))
                for stage in ("REQUIREMENT", "SUMMARY"):
                    self.assertEqual("coordinator", state["stages"][stage]["executor_type"])

    def test_resume_after_crash_inside_a_stage(self):
        for host in ("claude", "codebuddy"):
            with self.subTest(host=host):
                project = self.install(host)
                h.fake_agent(host, project, "--crash-in", "IMPLEMENT", check_rc=75)
                state = h.workflow_state(project)
                self.assertEqual("in_progress", state["stages"]["IMPLEMENT"]["status"])
                self.assertEqual("in_progress", state["status"])

                h.fake_agent(host, project, "--resume", check_rc=0)
                self.assert_delivered(project)
                sessions = []
                for item in h.transcript(project):
                    if item["session"] not in sessions:
                        sessions.append(item["session"])
                self.assertEqual(2, len(sessions))
                second = [e for e in events(project, "action") if e["session"] == sessions[1]]
                if host == "codebuddy":
                    # A new CodeBuddy session must drop the old Team and re-dispatch.
                    self.assertEqual("rotate-team", second[0]["action"])
                    self.assertEqual(1, len(events(project, "team-rotated")))
                else:
                    self.assertEqual(("continue-executor", "IMPLEMENT"),
                                     (second[0]["action"], second[0]["stage"]))
                self.assertFalse(any(e["kind"] == "init" and e["session"] == sessions[1]
                                     for e in h.transcript(project)))

    def test_resume_after_interruption_between_actions(self):
        project = self.install("codex")
        h.fake_agent("codex", project, "--stop-after", "7", check_rc=75)
        self.assertNotEqual("completed", h.workflow_state(project)["status"])
        h.fake_agent("codex", project, "--resume", check_rc=0)
        self.assert_delivered(project)
        finished = [e["stage"] for e in events(project, "finish") if e["result"] == "completed"]
        self.assertEqual(STAGE_ORDER, finished, "no stage may run twice after resume")

    def test_requirement_gate_waits_for_explicit_user_confirmation(self):
        project = self.install("cursor")
        h.fake_agent("cursor", project, "--no-user-confirmation", check_rc=3)
        state = h.workflow_state(project)
        self.assertEqual("awaiting_approval", state["status"])
        self.assertEqual("REQUIREMENT", state["awaiting_approval"])
        self.assertIsNone(state["stages"]["DESIGN"]["prepared_at"])
        self.assertFalse(events(project, "spawned"), "no DESIGN executor before approval")

        h.fake_agent("cursor", project, "--resume", check_rc=0)
        self.assert_delivered(project)

    def test_rejected_review_sends_work_back_to_implement(self):
        project = self.install("claude")
        h.fake_agent("claude", project, "--review-reject", "1", check_rc=0)
        self.assert_delivered(project)
        outcomes = [(e["stage"], e["result"]) for e in events(project, "finish")
                    if e["stage"] in ("IMPLEMENT", "REVIEW")]
        self.assertEqual([("IMPLEMENT", "completed"), ("REVIEW", "failed"),
                          ("IMPLEMENT", "completed"), ("REVIEW", "completed")], outcomes)

    def test_unfilled_artifact_is_rejected_and_workflow_does_not_advance(self):
        project = self.install("claude")
        result = h.fake_agent("claude", project, "--leave-placeholders", "DESIGN", check_rc=2)
        self.assertIn("artifact", result.stderr)
        state = h.workflow_state(project)
        self.assertEqual("in_progress", state["stages"]["DESIGN"]["status"])
        self.assertEqual("pending", state["stages"]["IMPLEMENT"]["status"])

    def test_repeated_stage_failures_block_the_workflow(self):
        project = self.install("pi")
        h.fake_agent("pi", project, "--fail-stage", "TEST:2", check_rc=4)
        state = h.workflow_state(project)
        self.assertEqual("blocked", state["status"])
        self.assertEqual("failed", state["stages"]["TEST"]["status"])
        self.assertEqual(2, state["stages"]["TEST"]["retry_count"])


if __name__ == "__main__":
    unittest.main()
