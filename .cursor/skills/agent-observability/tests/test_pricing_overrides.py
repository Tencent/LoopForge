"""自定义模型定价覆盖 + 看板未定价模型提示。

覆盖文件是**人手写的常驻配置**，而且加载失败要静默降级——这类"错了也不许
报错"的代码最容易在半年后被悄悄改坏（比如有人顺手把 try/except 去掉、把合并
改成整 key 覆盖、或让降级路径开始 print）。这里把字段级合并、四种/五种降级
路径、缓存语义、未定价口径、看板聚合与"不重算历史成本"逐条钉住。

两个容易踩的隔离坑，本文件统一在 setUp/tearDown 处理：
1. `load_prices()` 带 `lru_cache`，不 `clear_price_cache()` 用例之间会串味；
2. `AOBS_PRICING_OVERRIDES_PATH` 是进程级环境变量，不还原会污染同进程里
   其它测试模块（尤其是会真的去读默认覆盖文件的 hook 链路）。

真实日志对账（hy4-preview-ioa / hy3-ioa 的计数）不写进单测：日志会持续增长，
写死数字会变成 flaky 用例。那部分以人工冒烟的方式在 TASK-04 报告里记录。
"""
from __future__ import annotations

import contextlib
import io
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = ROOT / "scripts"
if str(SCRIPTS) not in sys.path:
    sys.path.insert(0, str(SCRIPTS))

import build_dashboard_data as bdd  # type: ignore
from core import emitter  # type: ignore

OVERRIDES_ENV = "AOBS_PRICING_OVERRIDES_PATH"


class _OverridesTestCase(unittest.TestCase):
    """统一处理环境变量与 `load_prices()` 缓存的隔离。"""

    def setUp(self):
        self._env_backup = os.environ.get(OVERRIDES_ENV)
        os.environ.pop(OVERRIDES_ENV, None)
        emitter.clear_price_cache()
        self._tmp = tempfile.TemporaryDirectory()
        self.tmp = Path(self._tmp.name)

    def tearDown(self):
        # 顺序要紧：先还原 env，再清缓存，最后清目录，避免留下一个指向已删除
        # 目录的环境变量给下一个用例。
        if self._env_backup is None:
            os.environ.pop(OVERRIDES_ENV, None)
        else:
            os.environ[OVERRIDES_ENV] = self._env_backup
        emitter.clear_price_cache()
        self._tmp.cleanup()

    def use_overrides(self, payload, name: str = "overrides.json") -> Path:
        """把 payload 写成覆盖文件并指向它（JSON 文本原样写入，便于构造非法输入）。"""
        path = self.tmp / name
        text = payload if isinstance(payload, str) else json.dumps(payload)
        path.write_text(text, encoding="utf-8")
        os.environ[OVERRIDES_ENV] = str(path)
        emitter.clear_price_cache()
        return path

    def pure_builtin(self) -> dict:
        """"纯内置表"的唯一权威来源——不把 pricing.json 的内容抄进断言里，
        否则内置表一调整这里就假红；但内置表本身必须是非空的，否则下面的
        "降级后 == 纯内置表" 断言会退化成空表比空表。"""
        builtin = emitter._load_builtin_prices()
        self.assertTrue(builtin, "内置价格表不应为空，否则降级断言失去意义")
        self.assertIn("gpt-4o", builtin)
        return builtin


class OverridesPathResolutionTests(_OverridesTestCase):
    """AC4：路径可覆盖。默认位置刻意放在 skills 树**外**——
    `scripts/build-classic-hosts.py` 会整棵同步 `.codebuddy/skills`，放树内会让
    用户每次改价都产生一次 `--check` drift，并被复制进 .claude/.cursor 宿主包。"""

    def test_env_var_wins_over_default(self):
        path = self.use_overrides({"zz-model": {"output": 1.0}})
        self.assertEqual(emitter.resolve_overrides_path(), path)

    def test_tilde_is_expanded(self):
        os.environ[OVERRIDES_ENV] = "~/aobs-overrides-test.json"
        emitter.clear_price_cache()
        self.assertEqual(emitter.resolve_overrides_path(), Path.home() / "aobs-overrides-test.json")

    def test_unset_falls_back_to_default_path(self):
        self.assertEqual(emitter.resolve_overrides_path(), emitter.DEFAULT_OVERRIDES_PATH)

    def test_blank_env_falls_back_to_default(self):
        # 空字符串/纯空白都等同于"没设置"，不能变成指向 CWD 的相对路径。
        os.environ[OVERRIDES_ENV] = "   "
        emitter.clear_price_cache()
        self.assertEqual(emitter.resolve_overrides_path(), emitter.DEFAULT_OVERRIDES_PATH)

    def test_default_path_is_outside_the_skills_tree(self):
        default = emitter.DEFAULT_OVERRIDES_PATH
        self.assertIsNotNone(default)
        self.assertEqual(
            default,
            ROOT.parents[2] / ".codebuddy" / "agent-observability" / "pricing.overrides.json",
        )
        self.assertNotIn("skills", default.parts[-3:])
        self.assertFalse(str(default).startswith(str(ROOT)))


class MergePricesTests(unittest.TestCase):
    """AC2：字段级合并。整 key 覆盖被刻意否决过——漏写字段会把该字段按 0 计，
    静默把成本算没，风险高于收益。"""

    def test_explicit_field_overrides_builtin_while_others_are_kept(self):
        merged = emitter.merge_prices(
            {"gpt-4o": {"input": 2.5, "output": 10.0, "cache_read": 1.25}},
            {"gpt-4o": {"output": 99.0}},
        )
        self.assertEqual(merged["gpt-4o"]["output"], 99.0)
        self.assertEqual(merged["gpt-4o"]["input"], 2.5)
        self.assertEqual(merged["gpt-4o"]["cache_read"], 1.25)

    def test_new_model_can_be_added(self):
        merged = emitter.merge_prices({"gpt-4o": {"output": 10.0}}, {"zz-new": {"output": 9.0}})
        self.assertEqual(merged["zz-new"], {"output": 9.0})

    def test_merge_does_not_mutate_its_arguments(self):
        base = {"gpt-4o": {"input": 2.5, "output": 10.0}}
        overrides = {"gpt-4o": {"output": 99.0}}
        merged = emitter.merge_prices(base, overrides)
        merged["gpt-4o"]["input"] = 0.0
        # 内置表是模块级共享数据（lru_cache 之外还有调用方持有引用），
        # 被就地改过的话一次合并会污染整条 hook 链路。
        self.assertEqual(base["gpt-4o"], {"input": 2.5, "output": 10.0})
        self.assertEqual(overrides["gpt-4o"], {"output": 99.0})

    def test_empty_or_none_overrides_return_base_copy(self):
        base = {"gpt-4o": {"output": 10.0}}
        self.assertEqual(emitter.merge_prices(base, None), base)
        self.assertEqual(emitter.merge_prices(base, {}), base)

    def test_none_base_returns_overrides_only(self):
        self.assertEqual(emitter.merge_prices(None, {"zz-new": {"output": 1.0}}), {"zz-new": {"output": 1.0}})

    def test_non_dict_rows_are_ignored(self):
        merged = emitter.merge_prices({"gpt-4o": {"output": 10.0}}, {"zz-bad": 9.0, "zz-ok": {"output": 2.0}})
        self.assertNotIn("zz-bad", merged)
        self.assertEqual(merged["zz-ok"], {"output": 2.0})


class LoadPricesOverrideTests(_OverridesTestCase):
    """AC2 + AC3：加载与降级。"""

    def test_override_applies_field_level_and_adds_model(self):
        self.use_overrides({"zz-custom": {"input": 1.0, "output": 9.0}, "gpt-4o": {"output": 99.0}})
        prices = emitter.load_prices()
        self.assertEqual(prices["zz-custom"], {"input": 1.0, "output": 9.0})
        self.assertEqual(prices["gpt-4o"]["output"], 99.0)
        self.assertEqual(prices["gpt-4o"]["input"], self.pure_builtin()["gpt-4o"]["input"])

    def test_missing_file_degrades_to_builtin(self):
        os.environ[OVERRIDES_ENV] = str(self.tmp / "does-not-exist.json")
        emitter.clear_price_cache()
        self.assertEqual(emitter.load_prices(), self.pure_builtin())

    def test_invalid_json_degrades_to_builtin(self):
        self.use_overrides('{"gpt-4o": {"output": 99.0}')
        self.assertEqual(emitter.load_prices(), self.pure_builtin())

    def test_top_level_non_dict_degrades_to_builtin(self):
        self.use_overrides("[1, 2, 3]")
        self.assertEqual(emitter.load_prices(), self.pure_builtin())

    def test_non_numeric_field_is_skipped_but_siblings_apply(self):
        self.use_overrides({"gpt-4o": {"output": "abc", "input": 3.0}})
        prices = emitter.load_prices()
        # 写错一个字段不该把同模型其它字段、其它模型的正确覆盖一起丢掉。
        self.assertEqual(prices["gpt-4o"]["input"], 3.0)
        self.assertEqual(prices["gpt-4o"]["output"], self.pure_builtin()["gpt-4o"]["output"])

    def test_model_with_only_bad_fields_is_dropped_and_stays_unpriced(self):
        """计划外决策（已在 code review 接受）：全坏字段的模型不落表。
        若落表成 `{}`，它既算不出成本又不算"未定价"，看板上会变成一个查不到
        原因的空洞；丢弃后至少能在"模型定价"提醒里暴露出来。"""
        self.use_overrides({"zz-all-bad": {"output": "abc"}})
        prices = emitter.load_prices()
        self.assertNotIn("zz-all-bad", prices)
        self.assertTrue(emitter.is_unpriced("zz-all-bad"))

    def test_bool_is_not_treated_as_a_price(self):
        # bool 是 int 的子类，float(True) == 1.0 会静默变成"单价 1 美元"。
        self.use_overrides({"gpt-4o": {"output": True}})
        prices = emitter.load_prices()
        self.assertEqual(prices["gpt-4o"]["output"], self.pure_builtin()["gpt-4o"]["output"])

    def test_every_degradation_path_is_silent(self):
        """静默是刻意的：hook 每次都是新进程，一旦因格式问题 print/抛异常，
        整条 hook 链路都会变得不可用。"""
        for payload in ('{"broken": ', "[1,2,3]", '{"gpt-4o": {"output": "abc"}}', '{"gpt-4o": 1}'):
            with self.subTest(payload=payload):
                self.use_overrides(payload)
                out, err = io.StringIO(), io.StringIO()
                with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                    prices = emitter.load_prices()
                self.assertEqual(prices, self.pure_builtin())
                self.assertEqual(out.getvalue(), "")
                self.assertEqual(err.getvalue(), "")

    def test_load_prices_is_cached_until_cleared(self):
        path = self.use_overrides({"zz-cache-model": {"output": 1.0}})
        self.assertEqual(emitter.load_prices()["zz-cache-model"]["output"], 1.0)
        path.write_text(json.dumps({"zz-cache-model": {"output": 2.0}}), encoding="utf-8")
        # 未清缓存：同进程内仍是旧值（hook 是短命进程，这个行为是可接受的）。
        self.assertEqual(emitter.load_prices()["zz-cache-model"]["output"], 1.0)
        emitter.clear_price_cache()
        self.assertEqual(emitter.load_prices()["zz-cache-model"]["output"], 2.0)

    def test_clear_price_cache_picks_up_env_change(self):
        self.assertTrue(hasattr(emitter.load_prices, "cache_clear"))
        self.use_overrides({"zz-a": {"output": 1.0}})
        self.assertIn("zz-a", emitter.load_prices())
        self.use_overrides({"zz-b": {"output": 2.0}}, name="second.json")
        emitter.clear_price_cache()
        prices = emitter.load_prices()
        self.assertIn("zz-b", prices)
        self.assertNotIn("zz-a", prices)


class IsUnpricedTests(_OverridesTestCase):
    """D4：未定价的唯一口径是 `lookup_price(...) is None`。
    `lookup_price` 会做"最长子串"模糊兜底，所以 `gpt-4o-2024-11-20` 能算出成本，
    不该出现在"建议补价"里——口径一旦漂移，用户会去给明明已经命中的模型补价。"""

    def test_unknown_model_is_unpriced(self):
        self.assertTrue(emitter.is_unpriced("zz-totally-unknown"))

    def test_fuzzy_substring_match_is_not_unpriced(self):
        self.assertFalse(emitter.is_unpriced("gpt-4o-2024-11-20"))

    def test_missing_or_blank_model_is_unpriced(self):
        for model in (None, "", "   "):
            with self.subTest(model=model):
                self.assertTrue(emitter.is_unpriced(model))

    def test_override_makes_a_model_priced(self):
        self.assertTrue(emitter.is_unpriced("zz-then-priced"))
        self.use_overrides({"zz-then-priced": {"output": 9.0}})
        self.assertFalse(emitter.is_unpriced("zz-then-priced"))


class BuildUnpricedModelsTests(_OverridesTestCase):
    """AC5：只看板**统计**，绝不回头重算 cost_usd（D3：覆盖只对新事件生效）。"""

    def _usage(self, model, ts=1.0):
        return {"event": "usage", "sid": "s1", "ts": ts, "model": model, "tokens": {"input": 1, "output": 1}}

    def test_counts_only_unpriced_usage_events(self):
        events = [
            self._usage("zz-unpriced-a", 1.0),
            self._usage("zz-unpriced-a", 2.0),
            self._usage("zz-unpriced-b", 3.0),
            self._usage("gpt-4o-2024-11-20", 4.0),   # 模糊命中，不算未定价
            {"event": "usage", "sid": "s1", "ts": 5.0, "tokens": {"input": 1}},  # model 缺失
            self._usage("   ", 6.0),                  # model 空白
            {"event": "tool", "sid": "s1", "ts": 7.0, "tool": "Bash", "ms": 1},  # 非 usage
        ]
        rows = bdd.build_unpriced_models(events)
        self.assertEqual(rows, [
            {"name": "zz-unpriced-a", "usageEvents": 2},
            {"name": "zz-unpriced-b", "usageEvents": 1},
        ])

    def test_empty_input_returns_empty_list(self):
        self.assertEqual(bdd.build_unpriced_models([]), [])

    def test_sorted_by_event_count_desc_then_name(self):
        events = (
            [self._usage("zz-b", float(i)) for i in range(2)]
            + [self._usage("zz-a", 9.0)]
            + [self._usage("zz-c", float(i)) for i in range(2)]
        )
        rows = bdd.build_unpriced_models(events)
        # 计数相同的 zz-b / zz-c 必须按名字定序，否则两次生成的快照无法逐字节比对。
        self.assertEqual([(r["name"], r["usageEvents"]) for r in rows],
                         [("zz-b", 2), ("zz-c", 2), ("zz-a", 1)])

    def test_pricing_a_model_removes_it_from_the_list(self):
        events = [self._usage("zz-unpriced-a", 1.0), self._usage("zz-unpriced-b", 2.0)]
        self.assertEqual(len(bdd.build_unpriced_models(events)), 2)
        self.use_overrides({"zz-unpriced-a": {"output": 9.0}})
        rows = bdd.build_unpriced_models(events)
        self.assertEqual(rows, [{"name": "zz-unpriced-b", "usageEvents": 1}])

    def test_emitter_import_failure_degrades_to_empty_list(self):
        """目标机可能没装 emitter 的间接依赖；此时只该退化"未定价"这一项，
        看板仍要能出片。"""
        with mock.patch.object(bdd, "em", None):
            self.assertEqual(bdd.build_unpriced_models([self._usage("zz-unpriced-a")]), [])


class MainSnapshotTests(_OverridesTestCase):
    """main() 级别：键位、空列表、不重算成本、无覆盖时与改动前一致（AC1）。"""

    def _events(self):
        return [
            {"event": "user_prompt_submit", "sid": "s1", "ts": 1.0, "turn_id": "t1", "agent": "main"},
            {"event": "usage", "sid": "s1", "ts": 2.0, "model": "gpt-4o-2024-11-20",
             "tokens": {"input": 100, "output": 10}, "cost_usd": 0.0035, "agent": "main"},
            {"event": "usage", "sid": "s1", "ts": 3.0, "model": "zz-unpriced-a",
             "tokens": {"input": 100, "output": 10}, "agent": "main"},
            {"event": "usage", "sid": "s1", "ts": 4.0, "model": "zz-unpriced-a",
             "tokens": {"input": 100, "output": 10}, "agent": "main"},
        ]

    def _run_main(self, root: Path, out_name: str) -> tuple[dict, str]:
        metrics = root / "metrics.ndjson"
        metrics.write_text("\n".join(json.dumps(e) for e in self._events()) + "\n", encoding="utf-8")
        state = root / ".state.json"
        state.write_text("{}", encoding="utf-8")
        out = root / out_name
        old_argv = sys.argv
        sys.argv = ["build_dashboard_data.py", "--project-root", str(root),
                    "--out", str(out), "--metrics-path", str(metrics), "--state-path", str(state)]
        buf = io.StringIO()
        try:
            with contextlib.redirect_stdout(buf):
                rc = bdd.main()
        finally:
            sys.argv = old_argv
        self.assertEqual(rc, 0)
        self.assertIn("wrote ", buf.getvalue())
        return json.loads(out.read_text("utf-8")), buf.getvalue()

    def test_snapshot_exposes_unpriced_models_right_after_model_costs(self):
        with tempfile.TemporaryDirectory() as td:
            data, _ = self._run_main(Path(td), "out.json")
            keys = list(data.keys())
            self.assertEqual(keys[keys.index("modelCosts") + 1], "unpricedModels")
            self.assertEqual(data["unpricedModels"], [{"name": "zz-unpriced-a", "usageEvents": 2}])
            self.assertEqual([m["name"] for m in data["modelCosts"]], ["gpt-4o-2024-11-20"])

    def test_no_unpriced_models_yields_empty_list_not_null(self):
        with tempfile.TemporaryDirectory() as td:
            self.use_overrides({"zz-unpriced-a": {"output": 9.0}})
            data, _ = self._run_main(Path(td), "out.json")
            self.assertEqual(data["unpricedModels"], [])

    def test_override_never_recomputes_already_written_costs(self):
        """D3 的回归护栏：给未定价模型补价后，只有 unpricedModels 会变，
        modelCosts / daily / sessions 必须逐字节不变（历史成本不回溯）。"""
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            before, _ = self._run_main(root, "before.json")
            self.use_overrides({"zz-unpriced-a": {"output": 9.0}})
            after, _ = self._run_main(root, "after.json")
            before.pop("generated_at")
            after.pop("generated_at")
            self.assertEqual(before["unpricedModels"], [{"name": "zz-unpriced-a", "usageEvents": 2}])
            self.assertEqual(after["unpricedModels"], [])
            before.pop("unpricedModels")
            after.pop("unpricedModels")
            self.assertEqual(before, after)

    def test_broken_override_file_keeps_cli_at_exit_zero_and_silent(self):
        """AC3 的 CLI 层护栏：覆盖文件坏掉时看板仍然出片（exit 0、stderr 为空），
        而不是把整条构建链路搞挂。"""
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            self.use_overrides('{"gpt-4o": {"output": 99.0}')  # 故意截断的非法 JSON
            err = io.StringIO()
            with contextlib.redirect_stderr(err):
                data, _ = self._run_main(root, "out.json")
            self.assertEqual(err.getvalue(), "")
            self.assertEqual(data["unpricedModels"], [{"name": "zz-unpriced-a", "usageEvents": 2}])

    def test_no_override_file_matches_nonexistent_override_path(self):
        """AC1 的核心回归：不传覆盖文件时价格表就是纯内置表，
        与"指向一个不存在的路径"的输出完全一致（忽略 generated_at）。"""
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            unset, _ = self._run_main(root, "unset.json")
            os.environ[OVERRIDES_ENV] = str(root / "nope.json")
            emitter.clear_price_cache()
            missing, _ = self._run_main(root, "missing.json")
            unset.pop("generated_at")
            missing.pop("generated_at")
            self.assertEqual(unset, missing)
            self.assertEqual(unset["unpricedModels"], [{"name": "zz-unpriced-a", "usageEvents": 2}])


if __name__ == "__main__":
    unittest.main()
