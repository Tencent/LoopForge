from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = ROOT / "scripts"
if str(SCRIPTS) not in sys.path:
    sys.path.insert(0, str(SCRIPTS))

from core import agent_identity, devflow, state as st  # type: ignore


def _write_team_config(team_dir: Path, *, lead_session_id: str, member_cwd: str) -> None:
    team_dir.mkdir(parents=True, exist_ok=True)
    (team_dir / "config.json").write_text(
        json.dumps({
            "leadSessionId": lead_session_id,
            "createdAt": 1,
            "members": [{"cwd": member_cwd}],
        }),
        encoding="utf-8",
    )


class TaskSlugFromTeamDirTests(unittest.TestCase):
    def test_strips_fixed_prefix(self):
        team_dir = Path("/tmp/.codebuddy/teams/multi-agents-devflow-my-task_20260911_1200")
        self.assertEqual(devflow.task_slug_from_team_dir(team_dir), "my-task_20260911_1200")

    def test_returns_none_for_non_devflow_team_dir(self):
        team_dir = Path("/tmp/.codebuddy/teams/some-other-team")
        self.assertIsNone(devflow.task_slug_from_team_dir(team_dir))


class StageSnapshotTests(unittest.TestCase):
    def test_projects_expected_fields_and_ignores_non_dict_stage(self):
        workflow_state = {
            "current_stage": "TASK-03",
            "size_class": "medium",
            "run_mode": "auto",
            "stages": {
                "TASK-02": {"status": "completed", "executor": "architect", "retry_count": 0, "review_result": None},
                "CODE-REVIEW": {"status": "failed", "executor": "code-reviewer", "retry_count": 1, "review_result": "failed"},
                "garbage": "not-a-dict",
            },
        }
        snap = devflow.stage_snapshot(workflow_state)
        self.assertEqual(snap["current_stage"], "TASK-03")
        self.assertEqual(snap["size_class"], "medium")
        self.assertNotIn("garbage", snap["stages"])
        self.assertEqual(snap["stages"]["CODE-REVIEW"]["retry_count"], 1)
        self.assertEqual(snap["stages"]["CODE-REVIEW"]["review_result"], "failed")

    def test_handles_missing_or_malformed_input(self):
        self.assertEqual(devflow.stage_snapshot(None), {})
        self.assertEqual(devflow.stage_snapshot({"stages": "not-a-dict"}), {
            "current_stage": None, "size_class": None, "run_mode": None,
            "schema_version": None, "execution_mode": None, "host_adapter": None, "run_id": None,
            "stages": {},
        })


class DiffStageChangesTests(unittest.TestCase):
    def test_first_observation_produces_no_changes(self):
        curr = devflow.stage_snapshot({"stages": {"TASK-02": {"status": "completed", "retry_count": 0}}})
        self.assertEqual(devflow.diff_stage_changes(None, curr), [])

    def test_detects_retry_count_increase_as_a_change(self):
        prev = devflow.stage_snapshot({"stages": {"CODE-REVIEW": {"status": "in_progress", "retry_count": 0}}})
        curr = devflow.stage_snapshot({"stages": {"CODE-REVIEW": {"status": "failed", "retry_count": 1, "review_result": "failed"}}})
        changes = devflow.diff_stage_changes(prev, curr)
        self.assertEqual(len(changes), 1)
        self.assertEqual(changes[0]["stage"], "CODE-REVIEW")
        self.assertEqual(changes[0]["retry_count"], 1)
        self.assertEqual(changes[0]["review_result"], "failed")

    def test_unchanged_stage_produces_no_entry(self):
        snap = devflow.stage_snapshot({"stages": {"TASK-02": {"status": "completed", "retry_count": 0}}})
        self.assertEqual(devflow.diff_stage_changes(snap, snap), [])


class PtIdFromTranscriptPathTests(unittest.TestCase):
    def test_extracts_pt_track_from_subagent_transcript_name(self):
        path = "/tmp/session-123/subagents/sub-developer-PT-01.jsonl"
        self.assertEqual(agent_identity.pt_id_from_transcript_path(path), "PT-01")

    def test_returns_none_for_non_subagent_or_non_pt_transcript(self):
        self.assertIsNone(agent_identity.pt_id_from_transcript_path("/tmp/session-123.jsonl"))
        self.assertIsNone(agent_identity.pt_id_from_transcript_path("/tmp/session-123/subagents/architect.jsonl"))


class ResolveAndDiffTests(unittest.TestCase):
    def test_returns_none_when_no_devflow_team_found(self):
        with tempfile.TemporaryDirectory() as td:
            state_path = Path(td) / ".state.json"
            result = devflow.resolve_and_diff(state_path, "sid-1", "/tmp/some-project")
            self.assertIsNone(result)
            state = st.load_state(state_path)
            self.assertIsNone(state["sid-1"]["_devflow"]["context"])

    def test_late_appearing_devflow_run_is_discovered_on_a_later_call(self):
        """真实 bug 回归测试：同一个长生命周期 sid，会话开始时探测不到 devflow
        （此时既没有 team 目录也没有 artifacts/），之后才真正跑起 devflow
        （比如通过 Agent 工具后台 spawn，而不是从一开始就是独立 team 成员 sid）。
        早期的"没找到"不能被永久缓存，必须在 artifacts/ 出现之后的下一次调用里发现它。"""
        with tempfile.TemporaryDirectory() as td:
            project_dir = Path(td) / "project"
            project_dir.mkdir()
            state_path = project_dir / ".codebuddy" / "skills" / "agent-observability" / "logs" / ".state.json"
            sid = "sid-long-lived"

            # 会话早期：还没有任何 devflow 痕迹。
            first = devflow.resolve_and_diff(state_path, sid, str(project_dir))
            self.assertIsNone(first)

            # 同一个 sid，会话中途才出现 devflow 产物（没有 team 目录，走 artifacts 扫描兜底）。
            artifacts_dir = project_dir / "artifacts" / "late-appearing-task_20260914_1200"
            artifacts_dir.mkdir(parents=True)
            (artifacts_dir / "workflow-state.json").write_text(json.dumps({
                "version": "1.3", "current_stage": "SOLO", "size_class": "small",
                "stages": {"SOLO": {"status": "completed", "executor": "solo-developer", "retry_count": 0}},
            }), encoding="utf-8")

            second = devflow.resolve_and_diff(state_path, sid, str(project_dir))
            self.assertIsNotNone(second)
            self.assertEqual(second["task_slug"], "late-appearing-task_20260914_1200")

    def test_end_to_end_with_fake_team_and_workflow_state(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            project_dir = root / "project"
            project_dir.mkdir()
            config_home = root / "codebuddy-home"
            teams_root = config_home / "teams"
            team_dir = teams_root / "multi-agents-devflow-fix-token-bypass_20260911_0900"
            _write_team_config(team_dir, lead_session_id="sid-42", member_cwd=str(project_dir))

            artifacts_dir = project_dir / "artifacts" / "fix-token-bypass_20260911_0900"
            artifacts_dir.mkdir(parents=True)
            workflow_state_path = artifacts_dir / "workflow-state.json"
            workflow_state_path.write_text(json.dumps({
                "current_stage": "TASK-03",
                "size_class": "medium",
                "stages": {
                    "TASK-02": {"status": "completed", "executor": "architect", "retry_count": 0},
                    "TASK-03": {"status": "in_progress", "executor": "developer", "retry_count": 0},
                },
            }), encoding="utf-8")

            import os
            old_env = os.environ.get("CODEBUDDY_CONFIG_DIR")
            os.environ["CODEBUDDY_CONFIG_DIR"] = str(config_home)
            try:
                state_path = project_dir / ".codebuddy" / "skills" / "agent-observability" / "logs" / ".state.json"
                sid = "sid-42"

                # 第一次调用：发现 devflow 上下文，但因为是首次观测不产出 changes。
                first = devflow.resolve_and_diff(state_path, sid, str(project_dir))
                self.assertIsNotNone(first)
                self.assertEqual(first["task_slug"], "fix-token-bypass_20260911_0900")
                self.assertEqual(first["current_stage"], "TASK-03")
                self.assertEqual(first["changes"], [])

                # workflow-state.json 更新：TASK-03 打回重试。
                workflow_state_path.write_text(json.dumps({
                    "current_stage": "TASK-03",
                    "size_class": "medium",
                    "stages": {
                        "TASK-02": {"status": "completed", "executor": "architect", "retry_count": 0},
                        "TASK-03": {"status": "failed", "executor": "developer", "retry_count": 1},
                    },
                }), encoding="utf-8")

                second = devflow.resolve_and_diff(state_path, sid, str(project_dir))
                self.assertEqual(len(second["changes"]), 1)
                self.assertEqual(second["changes"][0]["stage"], "TASK-03")
                self.assertEqual(second["changes"][0]["retry_count"], 1)
                self.assertEqual(second["changes"][0]["status"], "failed")

                # 没有变化时，第三次调用应该不再产出 changes。
                third = devflow.resolve_and_diff(state_path, sid, str(project_dir))
                self.assertEqual(third["changes"], [])
            finally:
                if old_env is None:
                    os.environ.pop("CODEBUDDY_CONFIG_DIR", None)
                else:
                    os.environ["CODEBUDDY_CONFIG_DIR"] = old_env


class PortableSchemaStageSnapshotTests(unittest.TestCase):
    """Portable（v2.0）用 `executor_role` 而不是 `executor`，且没有 `review_result`。"""

    def test_reads_executor_role_and_top_level_execution_context(self):
        workflow_state = {
            "version": "2.0",
            "current_stage": "IMPLEMENT",
            "size_class": "medium",
            "execution_mode": "isolated",
            "host_adapter": "codebuddy",
            "run_id": "run-abc",
            "stages": {
                "DESIGN": {"status": "completed", "executor_role": "devflow-architect", "retry_count": 0},
                "IMPLEMENT": {"status": "failed", "executor_role": "devflow-developer", "retry_count": 1},
            },
        }
        snap = devflow.stage_snapshot(workflow_state)
        self.assertEqual(snap["schema_version"], "2.0")
        self.assertEqual(snap["execution_mode"], "isolated")
        self.assertEqual(snap["stages"]["DESIGN"]["executor"], "devflow-architect")
        self.assertEqual(snap["stages"]["IMPLEMENT"]["retry_count"], 1)
        self.assertIsNone(snap["stages"]["IMPLEMENT"]["review_result"])

    def test_diff_detects_retry_on_portable_shape_same_as_classic(self):
        prev = devflow.stage_snapshot({
            "version": "2.0",
            "stages": {"IMPLEMENT": {"status": "in_progress", "executor_role": "devflow-developer", "retry_count": 0}},
        })
        curr = devflow.stage_snapshot({
            "version": "2.0",
            "stages": {"IMPLEMENT": {"status": "failed", "executor_role": "devflow-developer", "retry_count": 1}},
        })
        changes = devflow.diff_stage_changes(prev, curr)
        self.assertEqual(len(changes), 1)
        self.assertEqual(changes[0]["stage"], "IMPLEMENT")
        self.assertEqual(changes[0]["retry_count"], 1)
        self.assertEqual(changes[0]["executor"], "devflow-developer")


class ArtifactsScanFallbackTests(unittest.TestCase):
    """`topology: spawn` 宿主没有 team 目录，靠扫 artifacts/ 兜底发现 task_slug。"""

    def test_no_artifacts_dir_returns_none(self):
        with tempfile.TemporaryDirectory() as td:
            self.assertIsNone(devflow._scan_artifacts_for_active_run(td))

    def test_ignores_non_devflow_json_and_picks_in_progress_run(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            artifacts = root / "artifacts"

            # 不是 devflow 产物的 JSON（没有 stages 字段），不能被误当成一次运行。
            noise_dir = artifacts / "not-a-devflow-run"
            noise_dir.mkdir(parents=True)
            (noise_dir / "workflow-state.json").write_text(json.dumps({"hello": "world"}), encoding="utf-8")

            done_dir = artifacts / "finished-task_20260910_0900"
            done_dir.mkdir(parents=True)
            (done_dir / "workflow-state.json").write_text(json.dumps({
                "status": "completed", "stages": {"DESIGN": {"status": "completed"}},
            }), encoding="utf-8")

            active_dir = artifacts / "active-task_20260911_1000"
            active_dir.mkdir(parents=True)
            (active_dir / "workflow-state.json").write_text(json.dumps({
                "status": "in_progress", "stages": {"IMPLEMENT": {"status": "in_progress"}},
            }), encoding="utf-8")

            result = devflow._scan_artifacts_for_active_run(str(root))
            self.assertIsNotNone(result)
            self.assertEqual(result["task_slug"], "active-task_20260911_1000")
            self.assertIsNone(result["team_dir"])

    def test_finished_detection_works_for_classic_schema_without_top_level_status(self):
        """Classic（v1.3）没有顶层 status 字段，"跑完没跑完"只能看 last_event。"""
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            artifacts = root / "artifacts"

            done_dir = artifacts / "classic-done_20260910_0900"
            done_dir.mkdir(parents=True)
            (done_dir / "workflow-state.json").write_text(json.dumps({
                "version": "1.3", "last_event": "workflow_completed",
                "stages": {"SOLO": {"status": "completed"}},
            }), encoding="utf-8")

            active_dir = artifacts / "classic-active_20260911_1000"
            active_dir.mkdir(parents=True)
            (active_dir / "workflow-state.json").write_text(json.dumps({
                "version": "1.3", "last_event": "TASK-02_completed",
                "stages": {"TASK-02": {"status": "completed"}, "TASK-03": {"status": "in_progress"}},
            }), encoding="utf-8")

            result = devflow._scan_artifacts_for_active_run(str(root))
            self.assertIsNotNone(result)
            self.assertEqual(result["task_slug"], "classic-active_20260911_1000")

    def test_resolve_and_diff_switches_task_slug_when_a_newer_run_appears(self):
        """真实 bug 回归测试：同一个长生命周期 sid 先后归属两次不同的 devflow 运行
        （没有真正 team_create 时的降级场景——第二次 `/start-devflow` 复用了同一个
        session）。第一次探测缓存下的 task_slug 不能在第二次运行开始后继续沿用；
        必须切换到新的那个，而且两个 task_slug 各自的 diff 历史不能互相污染。"""
        with tempfile.TemporaryDirectory() as td:
            project_dir = Path(td) / "project"
            state_path = project_dir / ".codebuddy" / "skills" / "agent-observability" / "logs" / ".state.json"
            sid = "sid-reused-across-two-runs"

            first_dir = project_dir / "artifacts" / "first-task_20260914_0900"
            first_dir.mkdir(parents=True)
            (first_dir / "workflow-state.json").write_text(json.dumps({
                "version": "1.3", "last_event": "workflow_completed", "current_stage": "SOLO",
                "stages": {"SOLO": {"status": "completed", "retry_count": 0}},
            }), encoding="utf-8")

            first = devflow.resolve_and_diff(state_path, sid, str(project_dir))
            self.assertEqual(first["task_slug"], "first-task_20260914_0900")

            # 同一个 sid，第二次运行出现，且比第一次更新（mtime 更新）。
            import time
            time.sleep(0.01)
            second_dir = project_dir / "artifacts" / "second-task_20260914_1100"
            second_dir.mkdir(parents=True)
            (second_dir / "workflow-state.json").write_text(json.dumps({
                "version": "1.3", "last_event": "workflow_completed", "current_stage": "SOLO",
                "stages": {"SOLO": {"status": "completed", "retry_count": 1}},
            }), encoding="utf-8")

            second = devflow.resolve_and_diff(state_path, sid, str(project_dir))
            self.assertEqual(second["task_slug"], "second-task_20260914_1100")
            self.assertEqual(second["changes"], [])  # 对 second 是首次观测，不产出变更

            # 第一个 task_slug 的历史没有被污染：如果它重新变成"最新"（比如被再次修改），
            # 应该正确切回，并且不会把 second 的历史错当成 first 的基线。
            (first_dir / "workflow-state.json").write_text(json.dumps({
                "version": "1.3", "last_event": "workflow_completed", "current_stage": "SOLO",
                "stages": {"SOLO": {"status": "completed", "retry_count": 1}},
            }), encoding="utf-8")
            time.sleep(0.01)
            first_dir.joinpath("workflow-state.json").touch()

            third = devflow.resolve_and_diff(state_path, sid, str(project_dir))
            self.assertEqual(third["task_slug"], "first-task_20260914_0900")
            # first 上一次被观测到时 retry_count 还是 0，现在变成 1——应该被识别为变化，
            # 而不是被 second 的快照历史污染成"首次观测"或者对不上的 diff。
            self.assertEqual(len(third["changes"]), 1)
            self.assertEqual(third["changes"][0]["retry_count"], 1)

    def test_resolve_and_diff_end_to_end_via_artifacts_scan_no_team(self):
        with tempfile.TemporaryDirectory() as td:
            project_dir = Path(td) / "project"
            artifacts_dir = project_dir / "artifacts" / "portable-task_20260911_1100"
            artifacts_dir.mkdir(parents=True)
            state_file = artifacts_dir / "workflow-state.json"
            state_file.write_text(json.dumps({
                "version": "2.0",
                "status": "in_progress",
                "current_stage": "REVIEW",
                "execution_mode": "isolated",
                "stages": {"REVIEW": {"status": "in_progress", "executor_role": "devflow-code-reviewer", "retry_count": 0}},
            }), encoding="utf-8")

            import os
            old_env = os.environ.get("CODEBUDDY_CONFIG_DIR")
            os.environ["CODEBUDDY_CONFIG_DIR"] = str(Path(td) / "empty-codebuddy-home")
            try:
                state_path = project_dir / ".codebuddy" / "skills" / "agent-observability" / "logs" / ".state.json"
                sid = "sid-spawn-1"

                first = devflow.resolve_and_diff(state_path, sid, str(project_dir))
                self.assertIsNotNone(first)
                self.assertEqual(first["task_slug"], "portable-task_20260911_1100")
                self.assertEqual(first["schema_version"], "2.0")
                self.assertEqual(first["execution_mode"], "isolated")
                self.assertEqual(first["changes"], [])

                state_file.write_text(json.dumps({
                    "version": "2.0",
                    "status": "in_progress",
                    "current_stage": "REVIEW",
                    "execution_mode": "isolated",
                    "stages": {"REVIEW": {"status": "failed", "executor_role": "devflow-code-reviewer", "retry_count": 1}},
                }), encoding="utf-8")

                second = devflow.resolve_and_diff(state_path, sid, str(project_dir))
                self.assertEqual(len(second["changes"]), 1)
                self.assertEqual(second["changes"][0]["retry_count"], 1)
                self.assertEqual(second["changes"][0]["status"], "failed")
            finally:
                if old_env is None:
                    os.environ.pop("CODEBUDDY_CONFIG_DIR", None)
                else:
                    os.environ["CODEBUDDY_CONFIG_DIR"] = old_env


if __name__ == "__main__":
    unittest.main()
