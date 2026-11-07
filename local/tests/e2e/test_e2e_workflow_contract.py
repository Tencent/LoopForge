"""E2E: workflow-contract guards the fake-Agent suite does not exercise.

``test_e2e_portable_workflow.py`` drives the happy paths and the approval /
rejection flows through ``fake_agent.py``. These cases assert the low-level
contract of the *installed* ``workflow_state.py`` CLI that the Agent relies on:
read-only ``resume``, artifact presence on ``finish``, the isolated executor and
Team-name guards, research-helper registration and recovery after a genuinely
failing project test command.

The project is installed by the real CLI and every state transition goes
through the public ``workflow_state.py`` interface; only the placeholder text of
materialised artifacts is filled in, exactly as a host Agent would.
"""

import json
import re
import subprocess
import sys
import unittest
from pathlib import Path

import harness as h

# Placeholder -> usable evidence, matching what a host Agent writes into the
# real templates. The generic fallback below handles every other slot.
EVIDENCE = {
    "{{说明当前实现、关键模块、仓库相对路径、约束和非目标}}": "当前实现位于 `app.py`，仅修改该文件。",
    "{{动作导向的任务名}}": "实现目标行为",
    "{{无，或任务 ID}}": "无",
    "{{仓库相对路径}}": "app.py",
    "{{工作目录}}": ".",
    "{{项目原生验证命令}}": "python3 -m unittest -v",
    "{{明确写“通过”或“不通过”；只有无阻断问题时才能写“通过”}}": "通过，无阻断问题。",
    "{{无接口变动时写“无接口变动”；有新增、修改或删除时生成文档并写“已生成：`03-code/api-docs.md`”}}": "无接口变动",
    "{{无接口变动时写“无接口变动”；有新增、修改或删除时生成文档并写“已生成：`01-solo/api-docs.md`”}}": "无接口变动",
    "{{列出仓库实际已有的测试层级及证据路径/配置/命令；若已有 E2E，明确其覆盖边界；若没有，不要为流程强行引入新框架}}": "现有 `unittest` 与 `python3 -m unittest`；没有浏览器 E2E 框架。",
    "{{逐项记录“验收标准或风险 | 项目已有的合适测试层级 | 用例或命令 | 结果”；项目已有 E2E 且本次行为属于其覆盖边界时，必须包含相应 E2E 用例}}": "核心逻辑 | unit | `python3 -m unittest -v` | passed",
}


def load_state(state_file):
    return json.loads(Path(state_file).read_text(encoding="utf-8"))


def init_workflow(project, slug, size, *, mode="auto", execution_mode="single-context",
                  host_adapter=None, fallback_reason=None, coordinator_id="e2e-main"):
    args = [
        "init", "--project-root", project, "--slug", slug, "--size", size,
        "--mode", mode, "--execution-mode", execution_mode,
        "--coordinator-id", coordinator_id,
    ]
    if host_adapter:
        args += ["--host-adapter", host_adapter]
    if fallback_reason:
        args += ["--fallback-reason", fallback_reason]
    return Path(h.run_workflow(project, *args).stdout.strip())


def required_artifacts(project, stage, include_design_context=False):
    skill_root = h.installed_workflow(project).parent.parent
    manifest = json.loads((skill_root / "templates/manifest.json").read_text(encoding="utf-8"))
    paths = []
    for item in manifest["artifacts"]:
        if item["stage"] != stage:
            continue
        if item.get("condition") == "requires_design_artifacts" and not include_design_context:
            continue
        paths.append(item["path"])
    return paths


def fill_stage_artifacts(project, state_file, stage, extra=None):
    """Write every artifact of ``stage`` afresh, like a real executor."""
    state = load_state(state_file)
    skill_root = h.installed_workflow(project).parent.parent
    manifest = json.loads((skill_root / "templates/manifest.json").read_text(encoding="utf-8"))
    templates = {item["path"]: item["template"] for item in manifest["artifacts"]}
    artifact_root = Path(state["artifacts_dir"])
    replacements = dict(EVIDENCE)
    if extra:
        replacements.update(extra)
    for relative in required_artifacts(project, stage, bool(state.get("requires_design_artifacts"))):
        text = (skill_root / "templates" / templates[relative]).read_text(encoding="utf-8")
        for source, target in replacements.items():
            text = text.replace(source, target)
        text = re.sub(r"\{\{[^{}]+\}\}", "已根据当前证据完成并验证。", text)
        path = artifact_root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")


def complete_stage(project, state_file, stage, extra=None):
    """prepare -> start -> fill artifacts -> finish completed for one stage."""
    h.run_workflow(project, "prepare", "--state", state_file, "--stage", stage)
    h.run_workflow(project, "start", "--state", state_file, "--stage", stage)
    fill_stage_artifacts(project, state_file, stage, extra)
    h.run_workflow(project, "finish", "--state", state_file, "--stage", stage, "--result", "completed")


def reach_review_ready(project, state_file):
    """Drive REQUIREMENT (confirmed), DESIGN and IMPLEMENT so REVIEW is next."""
    complete_stage(project, state_file, "REQUIREMENT")
    approve_requirement(project, state_file)
    complete_stage(project, state_file, "DESIGN")
    complete_stage(project, state_file, "IMPLEMENT")


def approve_requirement(project, state_file):
    h.run_workflow(
        project, "approve", "--state", state_file, "--stage", "REQUIREMENT", "--user-confirmed"
    )


def advance_to_design(project, state_file):
    """Finish REQUIREMENT, confirm the gate and prepare the DESIGN stage."""
    complete_stage(project, state_file, "REQUIREMENT")
    approve_requirement(project, state_file)
    h.run_workflow(project, "prepare", "--state", state_file, "--stage", "DESIGN")


def write_sample_project(root, broken=False):
    (root / "app.py").write_text(
        "def normalize(value):\n    return value\n" if broken
        else "def normalize(value):\n    return value.strip().lower()\n",
        encoding="utf-8",
    )
    (root / "test_app.py").write_text(
        "import unittest\nfrom app import normalize\n\n"
        "class NormalizeTest(unittest.TestCase):\n"
        "    def test_normalize(self):\n"
        "        self.assertEqual('hello', normalize(' Hello '))\n",
        encoding="utf-8",
    )


def run_project_tests(root):
    return subprocess.run(
        [sys.executable, "-m", "unittest", "-v"], cwd=str(root),
        text=True, capture_output=True,
    )


class WorkflowContractE2E(unittest.TestCase):
    def install(self, host, with_sample=False):
        project = h.new_project(self, with_sample=with_sample)
        h.loopforge("skills", "install", host, "--project-root", project)
        return project

    def isolated_medium(self, host, slug):
        project = self.install(host)
        state = init_workflow(
            project, slug, "medium",
            execution_mode="isolated", host_adapter=host,
        )
        return project, state

    def test_resume_is_read_only_and_reports_next_action(self):
        project = self.install("claude")
        state = init_workflow(project, "resume-readonly", "medium",
                              fallback_reason="e2e profile has no host agent")
        before = state.read_bytes()
        resume = json.loads(h.run_workflow(project, "resume", "--state", state).stdout)
        self.assertTrue(resume["ok"])
        self.assertEqual("prepare", resume["action"])
        self.assertEqual("REQUIREMENT", resume["stage"])
        self.assertEqual(before, state.read_bytes(), "resume must not mutate the state file")

    def test_finish_rejects_missing_artifact(self):
        project = self.install("claude")
        state = init_workflow(project, "artifact-gate", "small")
        h.run_workflow(project, "prepare", "--state", state, "--stage", "SOLO")
        h.run_workflow(project, "start", "--state", state, "--stage", "SOLO")
        report = Path(load_state(state)["artifacts_dir"]) / "01-solo/solo-report.md"
        report.unlink()
        missing = h.run_workflow(
            project, "finish", "--state", state, "--stage", "SOLO",
            "--result", "completed", check=False,
        )
        self.assertNotEqual(0, missing.returncode)
        self.assertIn("required artifacts missing", missing.stderr)
        self.assertEqual("in_progress", load_state(state)["stages"]["SOLO"]["status"])

    def test_isolated_start_requires_registered_executor(self):
        project, state = self.isolated_medium("codebuddy", "executor-guard")
        advance_to_design(project, state)
        blocked = h.run_workflow(project, "start", "--state", state, "--stage", "DESIGN", check=False)
        self.assertNotEqual(0, blocked.returncode)
        self.assertIn("configure the required devflow-architect executor", blocked.stderr)

    def test_isolated_assign_rejects_wrong_team(self):
        project, state = self.isolated_medium("codebuddy", "team-guard")
        advance_to_design(project, state)
        wrong = h.run_workflow(
            project, "assign", "--state", state, "--stage", "DESIGN",
            "--role", "devflow-architect",
            "--executor-type", "devflow-stage-team-member",
            "--executor-id", "agent-design",
            "--dispatch-tool", "task",
            "--team-name", "wrong-team",
            check=False,
        )
        self.assertNotEqual(0, wrong.returncode)
        self.assertIn("require --team-name", wrong.stderr)

    def test_helper_registration_records_research_executor(self):
        project, state = self.isolated_medium("codebuddy", "helper-audit")
        advance_to_design(project, state)
        team = load_state(state)["team_name"]
        h.run_workflow(
            project, "helper", "--state", state, "--stage", "DESIGN",
            "--role", "devflow-code-explorer",
            "--executor-type", "devflow-research-team-member",
            "--executor-id", "helper-design",
            "--purpose", "核对设计与需求输入",
            "--dispatch-tool", "task",
            "--team-name", team,
        )
        helper = load_state(state)["stages"]["DESIGN"]["helpers"][0]
        self.assertEqual("devflow-code-explorer", helper["role"])
        self.assertEqual("devflow-research-team-member", helper["executor_type"])
        self.assertEqual("helper-design", helper["executor_id"])
        self.assertEqual("team", helper["executor_topology"])
        self.assertEqual(team, helper["team_name"])
        self.assertEqual("task", helper["dispatch_tool"])

    def test_test_stage_recovers_from_real_failing_command(self):
        project = self.install("claude", with_sample=False)
        write_sample_project(project, broken=True)
        state = init_workflow(project, "real-failure", "medium",
                              fallback_reason="e2e profile has no host agent")
        reach_review_ready(project, state)
        complete_stage(project, state, "REVIEW")

        h.run_workflow(project, "prepare", "--state", state, "--stage", "TEST")
        h.run_workflow(project, "start", "--state", state, "--stage", "TEST")
        self.assertNotEqual(0, run_project_tests(project).returncode, "the broken project must fail")
        h.run_workflow(project, "finish", "--state", state, "--stage", "TEST", "--result", "failed")
        after = load_state(state)
        self.assertEqual("TEST", after["next_stage"])
        self.assertEqual("in_progress", after["status"])

        write_sample_project(project, broken=False)
        self.assertIn("OK", run_project_tests(project).stderr)
        h.run_workflow(project, "prepare", "--state", state, "--stage", "TEST")
        h.run_workflow(project, "start", "--state", state, "--stage", "TEST")
        fill_stage_artifacts(project, state, "TEST", {
            "{{记录实际执行的完整命令和工作目录}}": "`{} -m unittest -v`（工作目录 `{}`）".format(
                sys.executable, project
            ),
            "{{区分通过、失败和阻断，并附关键证据}}": "failed -> passed；修复后真实执行通过",
        })
        h.run_workflow(project, "finish", "--state", state, "--stage", "TEST", "--result", "completed")
        h.run_workflow(project, "decide-knowledge", "--state", state, "--decision", "skip",
                       "--reason", "只有常规变更和测试结果")
        complete_stage(project, state, "SUMMARY")
        self.assertIn("OK", h.run_workflow(project, "validate", "--state", state).stdout)
        self.assertEqual("completed", load_state(state)["status"])


if __name__ == "__main__":
    unittest.main()
