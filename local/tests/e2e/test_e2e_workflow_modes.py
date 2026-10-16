"""E2E: the other Portable routes and modes, driven by the fake host Agent.

Complements test_e2e_portable_workflow.py (medium/auto) with small/SOLO, SOLO
overflow escalation, large with a knowledge stage, manual approval gates and
design-implementation tasks.
"""

import subprocess
import sys
import unittest

import harness as h

MEDIUM = ["REQUIREMENT", "DESIGN", "IMPLEMENT", "REVIEW", "TEST", "SUMMARY"]


def events(project, kind):
    return [item for item in h.transcript(project) if item["kind"] == kind]


def completed_stages(project):
    return [e["stage"] for e in events(project, "finish") if e["result"] == "completed"]


class WorkflowModesE2E(unittest.TestCase):
    def install(self, host):
        project = h.new_project(self)
        h.loopforge("skills", "install", host, "--project-root", project)
        return project

    def assert_code_fixed(self, project):
        verify = subprocess.run(
            [sys.executable, "-m", "unittest", "check_slugify"], cwd=str(project),
            capture_output=True, text=True,
        )
        self.assertEqual(0, verify.returncode, verify.stderr)
        self.assertIn("OK", h.loopforge_validate(project))

    def test_small_task_uses_single_solo_agent(self):
        for host in ("claude", "codebuddy"):
            with self.subTest(host=host):
                project = self.install(host)
                h.fake_agent(host, project, "--size", "small", check_rc=0)
                state = h.workflow_state(project)
                self.assertEqual(["SOLO", "SUMMARY"], state["route"])
                self.assertEqual("completed", state["status"])
                self.assertEqual(["SOLO", "SUMMARY"], completed_stages(project))
                # No requirement gate and no multi-agent pipeline on the small route.
                self.assertFalse(events(project, "approved"))
                self.assertEqual(["SOLO"], [e["stage"] for e in events(project, "spawned")])
                self.assertTrue((project / "artifacts/e2e-task/01-solo/solo-report.md").is_file())
                self.assert_code_fixed(project)

    def test_solo_overflow_escalates_to_the_medium_route(self):
        project = self.install("claude")
        h.fake_agent("claude", project, "--size", "small", "--solo-overflow", check_rc=0)
        state = h.workflow_state(project)
        self.assertEqual("medium", state["size_class"])
        self.assertEqual("skipped", state["stages"]["SOLO"]["status"])
        self.assertEqual("completed", state["status"])
        self.assertEqual(MEDIUM, completed_stages(project))
        self.assertEqual(["REQUIREMENT"], [e["stage"] for e in events(project, "approved")])
        self.assert_code_fixed(project)

    def test_large_task_runs_the_knowledge_stage_when_it_has_value(self):
        project = self.install("codex")
        h.fake_agent("codex", project, "--size", "large", "--knowledge", "run", check_rc=0)
        state = h.workflow_state(project)
        self.assertEqual("large", state["size_class"])
        self.assertEqual("run", state["stages"]["KNOWLEDGE"]["decision"])
        self.assertEqual("completed", state["stages"]["KNOWLEDGE"]["status"])
        self.assertEqual(MEDIUM[:-1] + ["KNOWLEDGE", "SUMMARY"], completed_stages(project))
        self.assertIn("KNOWLEDGE", [e["stage"] for e in events(project, "spawned")])
        self.assertTrue((project / "artifacts/e2e-task/05-knowledge").is_dir())
        self.assert_code_fixed(project)

    def test_manual_mode_stops_at_every_manual_gate(self):
        project = self.install("cursor")
        h.fake_agent("cursor", project, "--mode", "manual", check_rc=0)
        approvals = [(e["stage"], e["user_confirmed"]) for e in events(project, "approved")]
        self.assertEqual(
            [("REQUIREMENT", True), ("DESIGN", False), ("REVIEW", False), ("TEST", False)],
            approvals,
        )
        self.assertEqual("completed", h.workflow_state(project)["status"])
        self.assert_code_fixed(project)

    def test_manual_gate_holds_the_next_stage_until_approved(self):
        project = self.install("cursor")
        # The user does not answer the DESIGN gate: the run must wait there.
        h.fake_agent("cursor", project, "--mode", "manual", "--hold-gate", "DESIGN", check_rc=3)
        state = h.workflow_state(project)
        self.assertEqual(("awaiting_approval", "DESIGN"), (state["status"], state["awaiting_approval"]))
        self.assertIsNone(state["stages"]["IMPLEMENT"]["prepared_at"])
        h.fake_agent("cursor", project, "--resume", "--mode", "manual", check_rc=0)
        self.assertEqual("completed", h.workflow_state(project)["status"])

    def test_design_implementation_task_requires_design_context(self):
        project = self.install("claude")
        h.fake_agent("claude", project, "--require-design-artifacts", check_rc=0)
        context = project / "artifacts/e2e-task/02-design/design-context"
        for relative in ("overview.md", "components.md", "sections/result-list.md", "evidence/mockup.png"):
            self.assertTrue((context / relative).is_file(), relative)
        self.assertEqual("completed", h.workflow_state(project)["status"])

    def test_design_task_without_visual_evidence_is_rejected(self):
        project = self.install("claude")
        result = h.fake_agent("claude", project, "--require-design-artifacts",
                              "--leave-placeholders", "DESIGN", check_rc=2)
        self.assertIn("finish DESIGN rejected", result.stderr)
        self.assertEqual("in_progress", h.workflow_state(project)["stages"]["DESIGN"]["status"])


if __name__ == "__main__":
    unittest.main()
