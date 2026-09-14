from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = ROOT / "scripts"
if str(SCRIPTS) not in sys.path:
    sys.path.insert(0, str(SCRIPTS))

import build_dashboard_data as bdd  # type: ignore


class ResolveInputPathsTests(unittest.TestCase):
    def setUp(self):
        self.skill_root = Path("/tmp/fake-skill-root")

    def test_defaults_fall_back_to_skill_root_logs(self):
        metrics, state = bdd.resolve_input_paths(self.skill_root)
        self.assertEqual(metrics, self.skill_root / "logs" / "metrics.ndjson")
        self.assertEqual(state, self.skill_root / "logs" / ".state.json")

    def test_none_args_equivalent_to_empty_args(self):
        m1, s1 = bdd.resolve_input_paths(self.skill_root, None, None)
        m2, s2 = bdd.resolve_input_paths(self.skill_root, "", "")
        self.assertEqual((m1, s1), (m2, s2))

    def test_both_args_override(self):
        metrics, state = bdd.resolve_input_paths(
            self.skill_root, "/abs/a/metrics.ndjson", "/abs/b/.state.json"
        )
        self.assertEqual(metrics, Path("/abs/a/metrics.ndjson").absolute())
        self.assertEqual(state, Path("/abs/b/.state.json").absolute())

    def test_metrics_only_override_keeps_default_state(self):
        metrics, state = bdd.resolve_input_paths(
            self.skill_root, "/abs/a/metrics.ndjson", None
        )
        self.assertEqual(metrics, Path("/abs/a/metrics.ndjson").absolute())
        self.assertEqual(state, self.skill_root / "logs" / ".state.json")

    def test_state_only_override_keeps_default_metrics(self):
        metrics, state = bdd.resolve_input_paths(
            self.skill_root, None, "/abs/b/.state.json"
        )
        self.assertEqual(metrics, self.skill_root / "logs" / "metrics.ndjson")
        self.assertEqual(state, Path("/abs/b/.state.json").absolute())

    def test_tilde_expansion(self):
        metrics, state = bdd.resolve_input_paths(
            self.skill_root, "~/x/metrics.ndjson", "~/y/.state.json"
        )
        self.assertEqual(metrics, (Path.home() / "x" / "metrics.ndjson").absolute())
        self.assertEqual(state, (Path.home() / "y" / ".state.json").absolute())


class MainEndToEndTests(unittest.TestCase):
    def _run_main(self, argv: list[str]) -> None:
        old_argv = sys.argv
        sys.argv = ["build_dashboard_data.py"] + argv
        try:
            self.assertEqual(bdd.main(), 0)
        finally:
            sys.argv = old_argv

    def test_default_paths_unchanged_and_reflected_in_source(self):
        with tempfile.TemporaryDirectory() as td:
            out = Path(td) / "dashboard-data.json"
            self._run_main(["--project-root", td, "--out", str(out)])
            self.assertTrue(out.is_file())
            data = json.loads(out.read_text("utf-8"))
            expected_metrics = str(bdd.SCRIPTS_DIR.parent / "logs" / "metrics.ndjson")
            expected_state = str(bdd.SCRIPTS_DIR.parent / "logs" / ".state.json")
            self.assertEqual(data["source"]["metrics_ndjson"], expected_metrics)
            self.assertEqual(data["source"]["state_json"], expected_state)

    def test_overridden_paths_read_and_reflected_in_source(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            metrics_file = root / "metrics.ndjson"
            state_file = root / ".state.json"
            metrics_file.write_text(
                '{"event":"usage","sid":"s1","ts":1,"tokens":{"input":10,"output":20}}\n',
                encoding="utf-8",
            )
            state_file.write_text(json.dumps({"s1": {"skills": {}}}), encoding="utf-8")
            out = root / "dashboard-data.json"

            self._run_main([
                "--project-root", td,
                "--out", str(out),
                "--metrics-path", str(metrics_file),
                "--state-path", str(state_file),
            ])
            self.assertTrue(out.is_file())

            data = json.loads(out.read_text("utf-8"))
            # resolve_input_paths 对覆盖路径执行 .expanduser().absolute()，断言需对齐（macOS 下不用 .resolve() 以避免 /tmp → /private/tmp 符号链接错位）。
            self.assertEqual(data["source"]["metrics_ndjson"], str(metrics_file.absolute()))
            self.assertEqual(data["source"]["state_json"], str(state_file.absolute()))
            # 覆盖文件确实被读取：metrics 行被解析进了 event_count。
            self.assertEqual(data["source"]["event_count"], 1)


class BuildDevflowRunsClassicSoloTests(unittest.TestCase):
    """真实运行验证过：Classic small 任务会经过 PHASE-0 -> SOLO -> TASK-05
    （knowledge 由 solo-developer 合并执行），不是只有 PHASE-0/SOLO 两步。"""

    def test_solo_run_includes_task05_and_marks_phase0_completed(self):
        with tempfile.TemporaryDirectory() as td:
            project_root = Path(td)
            run_dir = project_root / "artifacts" / "solo-with-knowledge_20260914_0600"
            run_dir.mkdir(parents=True)
            (run_dir / "workflow-state.json").write_text(json.dumps({
                "version": "1.3",
                "task_slug": "solo-with-knowledge_20260914_0600",
                "size_class": "small",
                "current_stage": "SUMMARY",
                "last_event": "workflow_completed",
                # 注意：真实 schema 里 stages{} 不含 PHASE-0 —— Phase 0 是隐式完成的。
                "stages": {
                    "SOLO": {"status": "completed", "executor": "solo-developer", "retry_count": 0},
                    "TASK-02": {"status": "skipped", "executor": "architect", "retry_count": 0},
                    "TASK-03": {"status": "skipped", "executor": "developer", "retry_count": 0},
                    "CODE-REVIEW": {"status": "skipped", "executor": "code-reviewer", "retry_count": 0},
                    "TASK-04": {"status": "skipped", "executor": "test-engineer", "retry_count": 0},
                    "TASK-05": {"status": "completed", "executor": "solo-developer", "retry_count": 0},
                },
            }), encoding="utf-8")

            runs = bdd.build_devflow_runs(project_root)
            self.assertEqual(len(runs), 1)
            stage_by_key = {s["key"]: s["status"] for s in runs[0]["stages"]}
            self.assertIn("TASK-05", stage_by_key)
            self.assertEqual(stage_by_key["TASK-05"], "completed")
            self.assertEqual(stage_by_key["PHASE-0"], "completed")
            self.assertNotIn("TASK-01", stage_by_key)  # SOLO 路径不该混进 medium/large 的阶段


if __name__ == "__main__":
    unittest.main()
