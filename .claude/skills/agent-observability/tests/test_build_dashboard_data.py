from __future__ import annotations

import contextlib
import io
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


class ToolCallFailedTests(unittest.TestCase):
    """真实 CodeBuddy CLI transcript 里从没出现过 `is_error` 这个键——实测抓到的
    rawResponse 形如 {"exitCode": 0, "tool_error_code": "0", ...}（成功）或非 0
    exitCode（失败）。之前只认 is_error，导致这个宿主上失败统计永远是 0。"""

    def test_real_success_shape_from_codebuddy_is_not_a_failure(self):
        # 实测数据：ls 一个不存在的路径，但命令写成了 `ls ...; echo "EXIT_CODE=$?"`，
        # 复合命令整体 exitCode 被内层 echo 冲成了 0——工具调用本身没有失败。
        raw_response = {
            "exitCode": 0, "signal": None, "interrupted": False,
            "sandboxDenied": False, "stderrBytesTruncated": 0,
            "stdoutBytesTruncated": 0, "tool_error_code": "0",
        }
        self.assertFalse(bdd.tool_call_failed(raw_response))

    def test_nonzero_exit_code_is_a_failure(self):
        raw_response = {"exitCode": 1, "tool_error_code": "1"}
        self.assertTrue(bdd.tool_call_failed(raw_response))

    def test_nonzero_tool_error_code_without_exit_code_is_a_failure(self):
        raw_response = {"tool_error_code": "127"}
        self.assertTrue(bdd.tool_call_failed(raw_response))

    def test_legacy_is_error_flag_still_recognized(self):
        self.assertTrue(bdd.tool_call_failed({"is_error": True}))

    def test_missing_or_non_dict_raw_response_is_not_a_failure(self):
        self.assertFalse(bdd.tool_call_failed(None))
        self.assertFalse(bdd.tool_call_failed("not a dict"))
        self.assertFalse(bdd.tool_call_failed({}))

    def test_build_failures_and_session_status_use_exit_code(self):
        events = [
            {
                "event": "tool", "sid": "s1", "ts": 1.0, "tool": "Bash",
                "tool_details": {"raw_response": {"exitCode": 1, "tool_error_code": "1"}},
            },
        ]
        failures = bdd.build_failures(events)
        self.assertEqual(len(failures), 1)
        self.assertEqual(failures[0]["tool"], "Bash")
        self.assertEqual(failures[0]["code"], "1")

        _, sessions = bdd.build_daily_and_sessions(events)
        self.assertEqual(sessions[0]["status"], "error")


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


class TopSlowTests(unittest.TestCase):
    """`--top-slow N` 是纯增量能力：默认不开启，开启后也只往终端打印，绝不改动
    dashboard-data.json 的结构（看板前端按字段取值，多一个字段或少一个字段都会
    直接影响渲染）。这些用例把"降序取前 N""非 tool 事件排除""ms 兜底""只读"
    四条边界钉住，避免后续有人顺手把结果写进 JSON 或改了排序方向。"""

    def _events(self):
        return [
            {"event": "user_prompt_submit", "sid": "s1", "ts": 1.0, "turn_id": "t1", "agent": "main"},
            {"event": "tool", "sid": "s1", "ts": 2.0, "tool": "Read", "ms": 300, "agent": "main"},
            {"event": "usage", "sid": "s1", "ts": 3.0, "tokens": {"input": 10, "output": 5}, "agent": "main"},
            {"event": "tool", "sid": "s1", "ts": 4.0, "tool": "Bash", "ms": 900, "agent": "main"},
            {"event": "tool", "sid": "s1", "ts": 5.0, "tool": "Grep", "ms": 120, "agent": "main"},
        ]

    def _write_metrics(self, root: Path) -> Path:
        metrics = root / "metrics.ndjson"
        metrics.write_text(
            "\n".join(json.dumps(e) for e in self._events()) + "\n",
            encoding="utf-8",
        )
        return metrics

    def _run_main(self, argv: list[str]) -> str:
        """跑一次 main() 并返回捕获到的 stdout——top-slow 的产出只体现在终端上，
        不体现在 JSON 里，所以断言必须落在 stdout。"""
        old_argv = sys.argv
        sys.argv = ["build_dashboard_data.py"] + argv
        buf = io.StringIO()
        try:
            with contextlib.redirect_stdout(buf):
                rc = bdd.main()
        finally:
            sys.argv = old_argv
        self.assertEqual(rc, 0)
        return buf.getvalue()

    def _printed_tools(self, stdout: str) -> list[str]:
        tools = []
        for line in stdout.splitlines():
            line = line.strip()
            if not line or not line[0].isdigit():
                continue
            tools.append(line.split(". ", 1)[1].split()[0])
        return tools

    def test_returns_n_slowest_tool_events_descending(self):
        rows = bdd.build_top_slow(self._events(), 2)
        self.assertEqual([(r["tool"], r["ms"]) for r in rows], [("Bash", 900), ("Read", 300)])

    def test_limit_greater_than_event_count_returns_all_without_error(self):
        events = [{"event": "tool", "sid": "s1", "ts": 1.0, "tool": "Bash", "ms": 10}]
        rows = bdd.build_top_slow(events, 99)
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["tool"], "Bash")

    def test_non_tool_events_are_excluded(self):
        rows = bdd.build_top_slow(self._events(), 10)
        self.assertEqual([r["tool"] for r in rows], ["Bash", "Read", "Grep"])
        # usage 事件即使带 ms 也不能混进来。
        self.assertTrue(all(isinstance(r["ms"], (int, float)) for r in rows))

    def test_missing_or_non_numeric_ms_falls_back_to_zero(self):
        events = [
            {"event": "tool", "sid": "s1", "ts": 1.0, "tool": "Bash"},
            {"event": "tool", "sid": "s1", "ts": 2.0, "tool": "Read", "ms": "120"},
            {"event": "tool", "sid": "s1", "ts": 3.0, "tool": "Grep", "ms": None},
            {"event": "tool", "sid": "s1", "ts": 4.0, "tool": "Edit", "ms": 1},
        ]
        rows = bdd.build_top_slow(events, 4)
        self.assertEqual(len(rows), 4)
        # 非数字/缺失的 ms 一律按 0 计，不会把字符串 "120" 排到最前面。
        self.assertEqual(sum(r["ms"] for r in rows), 1)

    def test_disabled_by_default_prints_nothing_extra(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            metrics = self._write_metrics(root)
            out = root / "dashboard-data.json"
            stdout = self._run_main([
                "--project-root", td, "--out", str(out),
                "--metrics-path", str(metrics), "--state-path", str(root / ".state.json"),
            ])
            self.assertIn("wrote ", stdout)
            self.assertNotIn("top-slow", stdout)
            self.assertEqual(self._printed_tools(stdout), [])

    def test_enabled_prints_tool_and_ms_descending(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            metrics = self._write_metrics(root)
            out = root / "dashboard-data.json"
            stdout = self._run_main([
                "--project-root", td, "--out", str(out),
                "--metrics-path", str(metrics), "--state-path", str(root / ".state.json"),
                "--top-slow", "2",
            ])
            self.assertIn("wrote ", stdout)
            self.assertIn("top-slow", stdout)
            self.assertEqual(self._printed_tools(stdout), ["Bash", "Read"])
            self.assertIn("900ms", stdout)
            self.assertNotIn("120ms", stdout)  # N=2，第三名的 Grep 不该出现

    def test_enabled_does_not_change_dashboard_data_structure(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            metrics = self._write_metrics(root)
            state = root / ".state.json"
            state.write_text(json.dumps({"s1": {"skills": {}}}), encoding="utf-8")
            # 输出到两个不同文件再比对，避免"第二次跑覆盖了第一次"导致对比失真。
            out_on = root / "on.json"
            out_off = root / "off.json"
            self._run_main([
                "--project-root", td, "--out", str(out_on),
                "--metrics-path", str(metrics), "--state-path", str(state), "--top-slow", "5",
            ])
            self._run_main([
                "--project-root", td, "--out", str(out_off),
                "--metrics-path", str(metrics), "--state-path", str(state),
            ])
            data_on = json.loads(out_on.read_text("utf-8"))
            data_off = json.loads(out_off.read_text("utf-8"))
            data_on.pop("generated_at")
            data_off.pop("generated_at")
            self.assertEqual(data_on, data_off)
            # 兜底断言：结果没有以任何形式渗进 JSON（不只是"结构相同"）。
            self.assertNotIn("topSlow", json.dumps(data_on))
            self.assertNotIn("top-slow", json.dumps(data_on))


class BuildDailyAndSessionsTurnAttributionTests(unittest.TestCase):
    """真实场景复现过：AgentLens（写 state.current_turn 的那套 tracing）关闭时，
    tool/usage 事件自己的 turn_id 永远是 None，只有 user_prompt_submit 事件带真实
    turn_id。修复前所有没有 turn_id 的事件会被塌缩进同一个 "?" 占位桶，看起来
    像"整个会话只有 1 个 turn"，哪怕实际发了好几轮 prompt。"""

    def _events(self):
        return [
            {"event": "user_prompt_submit", "sid": "s1", "ts": 1.0, "turn_id": "t1", "agent": "main"},
            {"event": "tool", "sid": "s1", "ts": 2.0, "tool": "Bash", "ms": 100, "agent": "main"},
            {"event": "usage", "sid": "s1", "ts": 3.0, "tokens": {"input": 10, "output": 5}, "agent": "main"},
            {"event": "user_prompt_submit", "sid": "s1", "ts": 4.0, "turn_id": "t2", "agent": "main"},
            {"event": "tool", "sid": "s1", "ts": 5.0, "tool": "Read", "ms": 50, "agent": "main"},
            {"event": "tool", "sid": "s1", "ts": 6.0, "tool": "Edit", "ms": 80, "agent": "main"},
        ]

    def test_turns_reflect_prompt_boundaries_not_a_single_bucket(self):
        daily, sessions = bdd.build_daily_and_sessions(self._events())
        self.assertEqual(len(sessions), 1)
        sess = sessions[0]
        self.assertEqual(sess["turns"], 2)
        self.assertEqual(sess["toolCalls"], 3)
        self.assertEqual(len(sess["timeline"]), 2)
        turn1_tools = [e["tool"] for e in sess["timeline"][0]["events"] if e["kind"] == "tool"]
        turn2_tools = [e["tool"] for e in sess["timeline"][1]["events"] if e["kind"] == "tool"]
        self.assertEqual(turn1_tools, ["Bash"])
        self.assertEqual(turn2_tools, ["Read", "Edit"])

    def test_events_before_first_prompt_fall_back_to_unknown_bucket(self):
        events = [
            {"event": "tool", "sid": "s1", "ts": 1.0, "tool": "Bash", "ms": 100, "agent": "main"},
            {"event": "user_prompt_submit", "sid": "s1", "ts": 2.0, "turn_id": "t1", "agent": "main"},
            {"event": "tool", "sid": "s1", "ts": 3.0, "tool": "Read", "ms": 50, "agent": "main"},
        ]
        _, sessions = bdd.build_daily_and_sessions(events)
        sess = sessions[0]
        # 没有归属到任何真实 turn 的事件仍然单独成桶，不会被错误地并入第一个真实 turn。
        self.assertEqual(len(sess["timeline"]), 2)
        self.assertEqual(sess["timeline"][0]["turn"], 1)

    def test_prompt_only_turn_with_no_tool_calls_still_appears_in_timeline(self):
        """真实数据复现过：纯对话轮次（用户发了 prompt，但助手没有调用任何工具，
        也没有产生 usage 事件）之前完全不会在 timeline 里建桶——turns 计数是对的
        （来自 user_prompt_submit 自带的 turn_id），但展开的时间线分组数会比 turns
        少，二者对不上。"""
        events = [
            {"event": "user_prompt_submit", "sid": "s1", "ts": 1.0, "turn_id": "t1", "agent": "main"},
            {"event": "tool", "sid": "s1", "ts": 2.0, "tool": "Read", "ms": 100, "agent": "main"},
            {"event": "user_prompt_submit", "sid": "s1", "ts": 3.0, "turn_id": "t2", "agent": "main"},
            {"event": "user_prompt_submit", "sid": "s1", "ts": 4.0, "turn_id": "t3", "agent": "main"},
            {"event": "tool", "sid": "s1", "ts": 5.0, "tool": "Grep", "ms": 50, "agent": "main"},
        ]
        _, sessions = bdd.build_daily_and_sessions(events)
        sess = sessions[0]
        self.assertEqual(sess["turns"], 3)
        self.assertEqual(len(sess["timeline"]), 3)
        self.assertEqual(sess["timeline"][1]["events"], [])

    def test_prompt_only_session_still_counted_in_daily_session_count(self):
        """真实数据复现过（TC5 当天 5 个 session，"总览"页汇总出的会话数却是 4）：
        daily["_sids"]（"会话浏览"总览卡片"会话数"的数据源）之前只在 tool/usage
        分支里 add，一个全程只发了 prompt、没有触发任何工具调用也没有 usage 事件
        的 session（比如用户发了消息但被拒绝/打断，没有真正执行）会被这天的
        sessionCount 完全漏掉——尽管 sessions 列表（"会话浏览"tab）里它确实在，
        导致总览页"会话数"比实际能展开看到的会话数少。"""
        events = [
            {"event": "user_prompt_submit", "sid": "s1", "ts": 1.0, "turn_id": "t1", "agent": "main"},
            {"event": "tool", "sid": "s1", "ts": 2.0, "tool": "Read", "ms": 50, "agent": "main"},
            {"event": "user_prompt_submit", "sid": "s2", "ts": 3.0, "turn_id": "t1", "agent": "main"},
        ]
        daily, sessions = bdd.build_daily_and_sessions(events)
        self.assertEqual(len(sessions), 2)
        self.assertEqual(len(daily), 1)
        self.assertEqual(daily[0]["sessionCount"], 2)


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


class BuildDevflowRunsMediumWithLingeringSoloKeyTests(unittest.TestCase):
    """用户反馈复现：workflow-state.json 模板固定给每次运行都写一份 SOLO stage
    （见 assets/workflow-state-template.json 里 SOLO.description 的说明：
    "仅当 size_class==small 时启用，否则保持 skipped"），medium/large 任务从不会
    真正执行它，状态停在 "pending" 或 "skipped"。修复前 `"SOLO" in stages` 只看
    key 存不存在，不看状态，导致这类 medium 任务被误判成 solo 路径，阶段视图会用
    CLASSIC_SOLO_ORDER 渲染，把 TASK-01/TASK-02/TASK-03/CODE-REVIEW/TASK-04 全部
    从 stepper 里漏掉。"""

    def _run(self, solo_status: str):
        with tempfile.TemporaryDirectory() as td:
            project_root = Path(td)
            run_dir = project_root / "artifacts" / "medium-with-lingering-solo_20260915_0900"
            run_dir.mkdir(parents=True)
            (run_dir / "workflow-state.json").write_text(json.dumps({
                "version": "1.3",
                "task_slug": "medium-with-lingering-solo_20260915_0900",
                "size_class": "medium",
                "current_stage": "TASK-03",
                "last_event": "TASK-02_completed",
                "stages": {
                    "SOLO": {"status": solo_status, "executor": "solo-developer", "retry_count": 0},
                    "TASK-01": {"status": "completed", "executor": "main", "retry_count": 0},
                    "TASK-02": {"status": "completed", "executor": "architect", "retry_count": 0},
                    "TASK-03": {"status": "in_progress", "executor": "developer", "retry_count": 0},
                    "CODE-REVIEW": {"status": "pending", "executor": "code-reviewer", "retry_count": 0},
                    "TASK-04": {"status": "pending", "executor": "test-engineer", "retry_count": 0},
                    "TASK-05": {"status": "pending", "executor": "knowledge-engineer", "retry_count": 0},
                },
            }), encoding="utf-8")
            return bdd.build_devflow_runs(project_root)

    def test_medium_run_with_skipped_solo_key_uses_full_classic_order(self):
        runs = self._run("skipped")
        self.assertEqual(len(runs), 1)
        stage_by_key = {s["key"]: s["status"] for s in runs[0]["stages"]}
        # 修复前：这里会走 CLASSIC_SOLO_ORDER，TASK-01/02/03/CODE-REVIEW/TASK-04 全部消失。
        self.assertIn("TASK-01", stage_by_key)
        self.assertIn("TASK-02", stage_by_key)
        self.assertIn("TASK-03", stage_by_key)
        self.assertIn("CODE-REVIEW", stage_by_key)
        self.assertIn("TASK-04", stage_by_key)
        self.assertEqual(stage_by_key["TASK-02"], "completed")
        self.assertEqual(stage_by_key["TASK-03"], "in_progress")

    def test_medium_run_with_pending_solo_key_uses_full_classic_order(self):
        # 模板初始状态是 "pending"（不是所有实现都会显式改成 "skipped"），同样不该被判成 solo。
        runs = self._run("pending")
        stage_by_key = {s["key"]: s["status"] for s in runs[0]["stages"]}
        self.assertIn("TASK-01", stage_by_key)
        self.assertIn("CODE-REVIEW", stage_by_key)


class BuildDevflowRunsDurationTests(unittest.TestCase):
    """真实数据复现过：TASK-05 的 started_at 可能早于 SOLO 的 completed_at
    （同一次 solo-developer 执行内部的子步骤，不是真正先后发生的两个阶段）。
    total_duration 必须按真实起止跨度算，不能对逐阶段 duration 求和——否则会把
    重叠部分重复计入，虚高于源数据本身反映的运行时长（这次修复前是 600s，
    真实跨度只有 540s）。"""

    def _write_state(self, project_root: Path, stages: dict) -> None:
        run_dir = project_root / "artifacts" / "overlap-run_20260914_0620"
        run_dir.mkdir(parents=True)
        (run_dir / "workflow-state.json").write_text(json.dumps({
            "version": "1.3",
            "task_slug": "overlap-run_20260914_0620",
            "size_class": "small",
            "current_stage": "SUMMARY",
            "last_event": "workflow_completed",
            "stages": stages,
        }), encoding="utf-8")

    def test_total_duration_uses_true_span_not_sum_of_overlapping_stages(self):
        with tempfile.TemporaryDirectory() as td:
            project_root = Path(td)
            self._write_state(project_root, {
                "SOLO": {
                    "status": "completed", "executor": "solo-developer", "retry_count": 0,
                    "started_at": "2026-09-14T06:20:00Z", "completed_at": "2026-09-14T06:29:00Z",
                },
                "TASK-05": {
                    "status": "completed", "executor": "solo-developer", "retry_count": 0,
                    # 早于 SOLO 的 completed_at —— 真实数据里观测到的重叠场景。
                    "started_at": "2026-09-14T06:28:00Z", "completed_at": "2026-09-14T06:29:00Z",
                },
            })
            runs = bdd.build_devflow_runs(project_root)
            self.assertEqual(len(runs), 1)
            # 真实跨度是 06:20~06:29 = 540s，不是 540+60=600s。
            self.assertEqual(runs[0]["duration"], 540)
            stage_by_key = {s["key"]: s["duration"] for s in runs[0]["stages"]}
            # 单个阶段自身的 duration 不受影响，仍然如实反映各自的起止跨度。
            self.assertEqual(stage_by_key["SOLO"], 540)
            self.assertEqual(stage_by_key["TASK-05"], 60)

    def test_total_duration_is_zero_when_no_stage_has_valid_timestamps(self):
        with tempfile.TemporaryDirectory() as td:
            project_root = Path(td)
            self._write_state(project_root, {
                "SOLO": {"status": "completed", "executor": "solo-developer", "retry_count": 0},
                "TASK-05": {"status": "completed", "executor": "solo-developer", "retry_count": 0},
            })
            runs = bdd.build_devflow_runs(project_root)
            self.assertEqual(runs[0]["duration"], 0)


class BuildDailyAndSessionsPerEventAgentTests(unittest.TestCase):
    """真实数据复现过（TC5，size_class=small 但走了真实 team_create/send_message
    派发）：同一个 sid 下，metrics.ndjson 的 tool/usage 事件本来就各自带着准确的
    agent 字段（一次真实会话里 66 条 main、64 条 solo-developer），但展开进
    timeline 的每条 event 字典只有 kind/tool/ms/err，从不写回 agent —— 时间线里
    完全看不出某次工具调用到底是 main 自己做的还是派发给 solo-developer 后做的，
    等于白白丢弃了上游已经采集到的数据。"""

    def _events(self):
        return [
            {"event": "user_prompt_submit", "sid": "s1", "ts": 1.0, "turn_id": "t1", "agent": "main"},
            {"event": "tool", "sid": "s1", "ts": 2.0, "tool": "Agent", "ms": 50, "agent": "main"},
            {"event": "tool", "sid": "s1", "ts": 3.0, "tool": "Bash", "ms": 100, "agent": "solo-developer"},
            {"event": "usage", "sid": "s1", "ts": 4.0, "tokens": {"input": 10, "output": 5}, "agent": "solo-developer"},
            {"event": "tool", "sid": "s1", "ts": 5.0, "tool": "SendMessage", "ms": 30, "agent": "main"},
        ]

    def test_timeline_events_carry_the_agent_that_actually_ran_them(self):
        _, sessions = bdd.build_daily_and_sessions(self._events())
        sess = sessions[0]
        events = sess["timeline"][0]["events"]
        by_tool = {e.get("tool"): e for e in events if e["kind"] == "tool"}
        self.assertEqual(by_tool["Agent"]["agent"], "main")
        self.assertEqual(by_tool["Bash"]["agent"], "solo-developer")
        self.assertEqual(by_tool["SendMessage"]["agent"], "main")
        usage_events = [e for e in events if e["kind"] == "usage"]
        self.assertEqual(usage_events[0]["agent"], "solo-developer")

    def test_missing_agent_on_raw_event_falls_back_to_main(self):
        events = [
            {"event": "user_prompt_submit", "sid": "s1", "ts": 1.0, "turn_id": "t1"},
            {"event": "tool", "sid": "s1", "ts": 2.0, "tool": "Read", "ms": 10},
        ]
        _, sessions = bdd.build_daily_and_sessions(events)
        sess = sessions[0]
        self.assertEqual(sess["timeline"][0]["events"][0]["agent"], "main")


class BuildDispatchTests(unittest.TestCase):
    """真实数据复现过（TC5 的 dashboard-data-top-slow 会话，sid 01a09ef5...）：
    修复前 build_dispatch() 把 agent_history 里所有 evidence 以 "dispatch" 或
    "inbox" 开头的条目一律计数，导致两个真实问题——
    1) solo-developer 被上报回声（inbox@report:*）重复计入，真实派发只有 2 次，
       却显示成 5；
    2) team-lead 显示成一个"被 main 派发的子 agent"，但 team-lead 在
       core/agent_identity.py::normalize_role_name 里本来就和 main 归为一类
       （它是 CodeBuddy 原生 team 基础设施里 lead session 自己的 mailbox 名，
       不是真实存在的子 agent），面板标题明明叫"Main → 子 Agent 派发次数"，
       混进一个其实等价于 main 自己的假子 agent。"""

    def _real_tc5_agent_history(self):
        # 逐条取自真实会话 01a09ef5-1c6b-7928-870e-a5056ff360e1 的 .state.json。
        return [
            {"ts": 1.0, "agent": "solo-developer", "evidence": "dispatch@Agent<-main"},
            {"ts": 2.0, "agent": "team-lead", "evidence": "dispatch@SendMessage<-solo-developer"},
            {"ts": 3.0, "agent": "solo-developer", "evidence": "inbox@report:solo-developer"},
            {"ts": 4.0, "agent": "solo-developer", "evidence": "inbox@dispatch:solo-developer"},
            {"ts": 5.0, "agent": "main", "evidence": "dispatch@SendMessage<-solo-developer"},
            {"ts": 6.0, "agent": "main", "evidence": "inbox@handoff:solo-developer->main"},
            {"ts": 7.0, "agent": "solo-developer", "evidence": "dispatch@SendMessage<-main"},
            {"ts": 8.0, "agent": "solo-developer", "evidence": "inbox@report:solo-developer"},
        ]

    def test_real_tc5_history_excludes_team_lead_and_report_echoes(self):
        state = {"01a09ef5": {"agent_history": self._real_tc5_agent_history()}}
        rows = bdd.build_dispatch(state)
        # team-lead（main 的别名）完全不出现；solo-developer 只数真正代表新派发的
        # 3 条证据（2 条工具触发的 dispatch@ + 1 条 inbox 独立确认的 dispatch:），
        # 2 条上报回声（inbox@report:*）不计入。
        self.assertEqual(rows, [{"agent": "solo-developer", "count": 3}])

    def test_inbox_dispatch_evidence_alone_is_still_counted(self):
        """inbox 扫描独立确认的 "main 派给了谁"（inbox@dispatch:*）是原生 team
        基础设施里唯一能感知到、但没有经过 Agent/Task 工具调用拦截到的派发方式
        （例如 team-lead 用 mailbox 直接投递任务），必须保留，不能因为过滤
        report/handoff 回声就连这类真实派发信号也一起丢掉。"""
        state = {"s1": {"agent_history": [
            {"ts": 1.0, "agent": "architect", "evidence": "inbox@dispatch:architect"},
        ]}}
        rows = bdd.build_dispatch(state)
        self.assertEqual(rows, [{"agent": "architect", "count": 1}])

    def test_report_and_handoff_evidence_alone_are_not_dispatches(self):
        state = {"s1": {"agent_history": [
            {"ts": 1.0, "agent": "developer", "evidence": "inbox@report:developer"},
            {"ts": 2.0, "agent": "test-engineer", "evidence": "inbox@handoff:developer->test-engineer"},
        ]}}
        rows = bdd.build_dispatch(state)
        self.assertEqual(rows, [])

    def test_team_lead_is_excluded_even_without_report_noise(self):
        state = {"s1": {"agent_history": [
            {"ts": 1.0, "agent": "team-lead", "evidence": "dispatch@SendMessage<-main"},
        ]}}
        rows = bdd.build_dispatch(state)
        self.assertEqual(rows, [])


if __name__ == "__main__":
    unittest.main()
