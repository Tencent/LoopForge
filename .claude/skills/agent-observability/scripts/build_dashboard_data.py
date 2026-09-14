#!/usr/bin/env python3
"""把 `logs/metrics.ndjson` + `artifacts/*/workflow-state.json` + `.state.json` +
`hooks/logs/auto-dispatch.log` 聚合成看板需要的一份 `dashboard-data.json` 快照。

这是本地看板的数据源，不是 hook 链路的一部分——hook 只管往 metrics.ndjson 追加事件，
这个脚本单独、按需运行（比如每次想看一眼看板之前手动跑一次，或者配合文件监听器）。
复用 core.devflow 的 stage_snapshot 做 workflow-state.json 的 schema 归一化，
不在这里重新实现一遍 Classic/Portable 的字段差异。

用法：
    python3 build_dashboard_data.py --project-root <项目根目录> --out dashboard/dashboard-data.json
    python3 build_dashboard_data.py --metrics-path ~/other/metrics.ndjson --state-path ~/other/.state.json

不传 --metrics-path / --state-path 时，仍从 <skill_root>/logs/ 下的默认文件读取；
两者均支持 ~ 展开，输出 source 会如实反映实际读取路径。

没有任何真实数据时（hook 刚接上、还没跑过 session），会输出一份全空但结构合法的快照，
不会报错、也不会伪造数据。
"""
from __future__ import annotations

import argparse
import json
import sys
from collections import defaultdict
from pathlib import Path
from typing import Any

SCRIPTS_DIR = Path(__file__).resolve().parent
if str(SCRIPTS_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPTS_DIR))

from core import devflow as dv  # type: ignore

# devflow 两套 schema 的 stage 名 -> 展示用 (label, name)。取不到的 stage 用 key 本身兜底。
STAGE_META: dict[str, tuple[str, str]] = {
    "PHASE-0": ("P0", "初始化 + 判定大小"),
    "SOLO": ("SOLO", "单 agent 全流程"),
    "TASK-01": ("T01", "需求分析 + 澄清"),
    "REQUIREMENT": ("REQ", "需求分析 + 澄清"),
    "TASK-02": ("T02", "技术方案"),
    "DESIGN": ("DES", "技术方案"),
    "TASK-03": ("T03", "代码实现"),
    "IMPLEMENT": ("IMPL", "代码实现"),
    "CODE-REVIEW": ("CR", "代码审查"),
    "REVIEW": ("REV", "代码审查"),
    "TASK-04": ("T04", "E2E 测试"),
    "TEST": ("TEST", "E2E 测试"),
    "TASK-05": ("T05", "知识沉淀"),
    "KNOWLEDGE": ("KNOW", "知识沉淀"),
    "SUMMARY": ("汇总", "最终汇总"),
}
# 两套 schema 各自的阶段顺序，用来按正确顺序渲染 stepper（不能直接遍历 dict，顺序不保证）。
CLASSIC_ORDER = ["PHASE-0", "TASK-01", "TASK-02", "TASK-03", "CODE-REVIEW", "TASK-04", "TASK-05"]
# SOLO 完成后，small 任务会由 solo-developer 合并执行 TASK-05 知识沉淀（真实运行验证过，
# 不是理论上可选的分支）；stepper 顺序必须把它列进去，否则会把已完成的阶段悄悄漏掉。
CLASSIC_SOLO_ORDER = ["PHASE-0", "SOLO", "TASK-05"]
PORTABLE_ORDER = ["PHASE-0", "REQUIREMENT", "DESIGN", "IMPLEMENT", "REVIEW", "TEST", "KNOWLEDGE", "SUMMARY"]
PORTABLE_SOLO_ORDER = ["PHASE-0", "SOLO", "SUMMARY"]


def read_ndjson(path: Path) -> list[dict[str, Any]]:
    records: list[dict[str, Any]] = []
    if not path.is_file():
        return records
    with path.open("r", encoding="utf-8") as fp:
        for line in fp:
            line = line.strip()
            if not line.startswith("{"):
                continue
            try:
                obj = json.loads(line)
            except Exception:
                continue
            if isinstance(obj, dict):
                records.append(obj)
    return records


def safe_load_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text("utf-8"))
    except Exception:
        return None


def day_of(ts: float) -> str:
    from datetime import datetime, timezone
    return datetime.fromtimestamp(ts, tz=timezone.utc).strftime("%Y-%m-%d")


def build_daily_and_sessions(events: list[dict[str, Any]]) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    by_day: dict[str, dict[str, Any]] = {}
    by_sid: dict[str, dict[str, Any]] = {}
    by_sid_turn: dict[tuple[str, str], dict[str, Any]] = {}
    # AgentLens（写 state.current_turn 的那套 tracing）默认关闭时，tool/usage 事件
    # 自身的 turn_id 永远是 None——但 user_prompt_submit 事件不依赖 AgentLens，
    # 始终携带真实 turn_id（见 emitter.build_prompt_submit_event）。按 ts 排序后，
    # 把它当作 turn 边界，让后续没有自带 turn_id 的 tool/usage 事件归入"当前 sid
    # 最近一次打开的 turn"，而不是全部塌缩进同一个 "?" 占位桶。
    current_turn_by_sid: dict[str, str] = {}

    def day_bucket(iso: str) -> dict[str, Any]:
        return by_day.setdefault(iso, {
            "iso": iso, "sessionCount": 0, "input": 0, "output": 0, "cache": 0,
            "cost": 0.0, "toolCalls": 0, "failures": 0, "skillHits": defaultdict(int),
            "_sids": set(),
        })

    def sess_bucket(sid: str) -> dict[str, Any]:
        return by_sid.setdefault(sid, {
            "id": sid, "date": None, "agents": set(), "turns": set(), "toolCalls": 0,
            "tokens": 0, "cost": 0.0, "first_ts": None, "last_ts": None, "models": defaultdict(int),
            "status": "ok", "timeline": {},
        })

    for rec in sorted(events, key=lambda r: r.get("ts") if isinstance(r.get("ts"), (int, float)) else 0):
        event = rec.get("event")
        sid = str(rec.get("sid") or "")
        ts = rec.get("ts")
        if not sid or not isinstance(ts, (int, float)):
            continue
        iso = day_of(ts)
        sess = sess_bucket(sid)
        sess["_seen_day"] = iso
        if sess["first_ts"] is None or ts < sess["first_ts"]:
            sess["first_ts"] = ts
            sess["date"] = iso
        if sess["last_ts"] is None or ts > sess["last_ts"]:
            sess["last_ts"] = ts
        agent = rec.get("agent")
        if agent:
            sess["agents"].add(str(agent))
        raw_turn_id = rec.get("turn_id")
        if event == "user_prompt_submit" and raw_turn_id:
            current_turn_by_sid[sid] = str(raw_turn_id)
        turn_id = raw_turn_id or current_turn_by_sid.get(sid)
        if turn_id:
            sess["turns"].add(str(turn_id))

        if event == "tool":
            day = day_bucket(iso)
            day["_sids"].add(sid)
            day["toolCalls"] += 1
            sess["toolCalls"] += 1
            for s in rec.get("skill") or []:
                day["skillHits"][str(s)] += 1
            tool_details = rec.get("tool_details") or {}
            raw_response = tool_details.get("raw_response") if isinstance(tool_details, dict) else None
            is_err = bool(isinstance(raw_response, dict) and raw_response.get("is_error"))
            if is_err:
                day["failures"] += 1
                sess["status"] = "error"
            key = (sid, str(turn_id or "?"))
            turn = by_sid_turn.setdefault(key, {"turn": turn_id or "?", "agent": agent or "main", "events": []})
            turn["events"].append({
                "kind": "tool", "tool": rec.get("tool"),
                "ms": rec.get("ms") if isinstance(rec.get("ms"), (int, float)) else 0,
                "err": is_err,
            })
        elif event == "usage":
            day = day_bucket(iso)
            day["_sids"].add(sid)
            tokens = rec.get("tokens") or {}
            day["input"] += int(tokens.get("input") or 0)
            day["output"] += int(tokens.get("output") or 0)
            day["cache"] += int(tokens.get("cache_read") or 0)
            cost = rec.get("cost_usd")
            if isinstance(cost, (int, float)):
                day["cost"] += float(cost)
                sess["cost"] += float(cost)
            total_tok = int(tokens.get("input") or 0) + int(tokens.get("output") or 0)
            sess["tokens"] += total_tok
            model = rec.get("model")
            if model:
                sess["models"][str(model)] += 1
            key = (sid, str(turn_id or "?"))
            turn = by_sid_turn.setdefault(key, {"turn": turn_id or "?", "agent": agent or "main", "events": []})
            turn["events"].append({"kind": "usage", "tokens": total_tok})
        elif event == "error":
            sess["status"] = "error"

    for (sid, _turn_key), turn in by_sid_turn.items():
        sess = by_sid.setdefault(sid, sess_bucket(sid))
        sess["timeline"].setdefault(_turn_key, turn)

    daily = []
    for iso in sorted(by_day.keys()):
        d = by_day[iso]
        daily.append({
            "iso": iso, "sessionCount": len(d["_sids"]), "input": d["input"], "output": d["output"],
            "cache": d["cache"], "cost": round(d["cost"], 4), "toolCalls": d["toolCalls"],
            "failures": d["failures"], "skillHits": dict(d["skillHits"]),
        })

    sessions = []
    for sid, s in by_sid.items():
        if s["first_ts"] is None:
            continue
        duration = int((s["last_ts"] or s["first_ts"]) - s["first_ts"])
        model = max(s["models"].items(), key=lambda kv: kv[1])[0] if s["models"] else None
        timeline_sorted = sorted(s["timeline"].values(), key=lambda t: str(t.get("turn") or ""))
        for idx, t in enumerate(timeline_sorted, start=1):
            t["turn"] = idx
        sessions.append({
            "id": sid, "date": s["date"], "agent": ", ".join(sorted(s["agents"])) or "main",
            "turns": len(s["turns"]) or len(timeline_sorted), "toolCalls": s["toolCalls"],
            "tokens": s["tokens"], "cost": round(s["cost"], 4), "duration": duration,
            "model": model, "status": s["status"], "timeline": timeline_sorted,
        })
    sessions.sort(key=lambda s: s["date"] or "", reverse=True)
    return daily, sessions


def build_model_costs(events: list[dict[str, Any]]) -> list[dict[str, Any]]:
    totals: dict[str, float] = defaultdict(float)
    for rec in events:
        if rec.get("event") != "usage":
            continue
        model = rec.get("model")
        cost = rec.get("cost_usd")
        if model and isinstance(cost, (int, float)):
            totals[str(model)] += float(cost)
    rows = [{"name": name, "cost": round(cost, 4)} for name, cost in totals.items()]
    rows.sort(key=lambda r: r["cost"], reverse=True)
    return rows


def build_failures(events: list[dict[str, Any]]) -> list[dict[str, Any]]:
    agg: dict[tuple[str, str], dict[str, Any]] = {}
    for rec in events:
        if rec.get("event") != "tool":
            continue
        tool_details = rec.get("tool_details") or {}
        raw_response = tool_details.get("raw_response") if isinstance(tool_details, dict) else None
        if not isinstance(raw_response, dict) or not raw_response.get("is_error"):
            continue
        tool = str(rec.get("tool") or "unknown")
        code = str(raw_response.get("tool_error_code") or raw_response.get("exitCode") or "error")
        key = (tool, code)
        row = agg.setdefault(key, {"tool": tool, "code": code, "count": 0, "last": None, "_last_ts": 0.0})
        row["count"] += 1
        ts = rec.get("ts")
        if isinstance(ts, (int, float)) and ts > row["_last_ts"]:
            row["_last_ts"] = ts
            row["last"] = day_of(ts)
    rows = list(agg.values())
    for row in rows:
        row.pop("_last_ts", None)
    rows.sort(key=lambda r: r["count"], reverse=True)
    return rows


def build_skills(state: dict[str, Any]) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    totals: dict[str, int] = defaultdict(int)
    never_used: dict[str, dict[str, Any]] = {}
    for sid, sess in state.items():
        if sid.startswith("_") or not isinstance(sess, dict):
            continue
        for name, rec in (sess.get("skills") or {}).items():
            if not isinstance(rec, dict):
                continue
            totals[name] += int(rec.get("count") or 0)
        for name, rec in (sess.get("rules") or {}).items():
            if not isinstance(rec, dict) or int(rec.get("count") or 0) > 0:
                continue
            never_used[name] = {"name": name, "reason": "no_paths", "detail": "从未被路径推断或 active-rule 命中"}
    for name, count in totals.items():
        if count == 0:
            never_used.setdefault(name, {"name": name, "reason": "no_paths", "detail": "静态扫描到，但从未被任何工具调用命中"})
    skill_rows = [{"name": name, "count": count} for name, count in totals.items() if count > 0]
    skill_rows.sort(key=lambda r: r["count"], reverse=True)
    return skill_rows, sorted(never_used.values(), key=lambda r: r["name"])


def build_dispatch(state: dict[str, Any]) -> list[dict[str, Any]]:
    totals: dict[str, int] = defaultdict(int)
    for sid, sess in state.items():
        if sid.startswith("_") or not isinstance(sess, dict):
            continue
        for entry in sess.get("agent_history") or []:
            if not isinstance(entry, dict):
                continue
            evidence = str(entry.get("evidence") or "")
            agent = entry.get("agent")
            if agent and agent != "main" and (evidence.startswith("dispatch") or evidence.startswith("inbox")):
                totals[str(agent)] += 1
    rows = [{"agent": agent, "count": count} for agent, count in totals.items()]
    rows.sort(key=lambda r: r["count"], reverse=True)
    return rows


def build_auto_dispatch_stats(project_root: Path) -> dict[str, int]:
    log_path = project_root / ".codebuddy" / "hooks" / "logs" / "auto-dispatch.log"
    stats = {"auto_dispatch": 0, "fallback_to_main": 0, "passthrough": 0}
    if not log_path.is_file():
        return stats
    with log_path.open("r", encoding="utf-8") as fp:
        for line in fp:
            line = line.strip()
            if not line.startswith("{"):
                continue
            try:
                rec = json.loads(line)
            except Exception:
                continue
            decision = str(rec.get("decision") or "")
            if decision == "auto_dispatch":
                stats["auto_dispatch"] += 1
            elif decision == "fallback_to_main":
                stats["fallback_to_main"] += 1
            elif decision in {"passthrough"}:
                stats["passthrough"] += 1
    return stats


def build_cost_by_task_slug(events: list[dict[str, Any]]) -> dict[str, float]:
    """按 task_slug 汇总 usage/stop 事件的 cost_usd——workflow-state.json 自己不知道成本，
    这是唯一能把 devflow 运行和真实花费对上的地方（需要 emitter 已经把 task_slug 挂到事件上，
    见 core/runtime.py 的 devflow 接入）。"""
    totals: dict[str, float] = defaultdict(float)
    for rec in events:
        if rec.get("event") not in {"usage", "stop"}:
            continue
        task_slug = rec.get("task_slug")
        cost = rec.get("cost_usd")
        if task_slug and isinstance(cost, (int, float)):
            totals[str(task_slug)] += float(cost)
    return dict(totals)


def build_devflow_runs(project_root: Path, cost_by_slug: dict[str, float] | None = None) -> list[dict[str, Any]]:
    artifacts_root = project_root / "artifacts"
    runs: list[dict[str, Any]] = []
    cost_by_slug = cost_by_slug or {}
    if not artifacts_root.is_dir():
        return runs
    for child in sorted(artifacts_root.iterdir()):
        state_path = child / "workflow-state.json"
        raw = dv.read_workflow_state(str(state_path))
        if not isinstance(raw, dict) or not isinstance(raw.get("stages"), dict):
            continue
        snap = dv.stage_snapshot(raw)
        size_class = str(snap.get("size_class") or "medium")
        is_solo = size_class == "small" or "SOLO" in snap["stages"]
        if snap.get("schema_version") == "2.0":
            order = PORTABLE_SOLO_ORDER if is_solo else PORTABLE_ORDER
        else:
            order = CLASSIC_SOLO_ORDER if is_solo else CLASSIC_ORDER
        stages_out = []
        total_cost = cost_by_slug.get(child.name, 0.0)
        run_start = None
        run_end = None
        overall = "completed"
        for key in order:
            info = snap["stages"].get(key)
            if info is None:
                # PHASE-0 在两套 schema 里都不会出现在 stages{} 里（它是隐式完成的：
                # workflow-state.json 一旦存在，就说明 Phase 0 已经跑完了），
                # 不能用"没有条目"直接兜底成 pending，那样会把已完成的阶段显示错。
                info = {"status": "completed", "retry_count": 0, "executor": None} if key == "PHASE-0" \
                    else {"status": "pending", "retry_count": 0, "executor": None}
            label, name = STAGE_META.get(key, (key[:4], key))
            status = str(info.get("status") or "pending")
            retry_count = int(info.get("retry_count") or 0)
            raw_stage = raw.get("stages", {}).get(key) if isinstance(raw.get("stages"), dict) else {}
            duration = 0
            if isinstance(raw_stage, dict):
                started = raw_stage.get("started_at")
                completed = raw_stage.get("completed_at")
                if isinstance(started, str) and isinstance(completed, str):
                    try:
                        from datetime import datetime
                        t0 = datetime.fromisoformat(started.replace("Z", "+00:00"))
                        t1 = datetime.fromisoformat(completed.replace("Z", "+00:00"))
                        duration = max(0, int((t1 - t0).total_seconds()))
                        if run_start is None or t0 < run_start:
                            run_start = t0
                        if run_end is None or t1 > run_end:
                            run_end = t1
                    except Exception:
                        duration = 0
            if status == "failed":
                overall = "paused"
            elif status == "in_progress" and overall != "paused":
                overall = "running"
            stages_out.append({
                "key": key, "label": label, "name": name,
                "exec": info.get("executor") or "-", "status": status,
                "retry_count": retry_count, "duration": duration,
            })
        # 按真实起止时间跨度算总时长（而不是逐阶段 duration 相加）——阶段之间可能
        # 有重叠（比如 TASK-05 的 started_at 早于 SOLO 的 completed_at），相加会
        # 把重叠部分重复计入，虚高于源数据本身反映的运行时长。
        total_duration = int((run_end - run_start).total_seconds()) if run_start and run_end else 0
        runs.append({
            "slug": child.name, "size": size_class, "stages": stages_out,
            "cost": round(total_cost, 4), "duration": total_duration, "overall": overall,
        })
    runs.sort(key=lambda r: r["slug"], reverse=True)
    return runs


def resolve_input_paths(
    skill_root: Path,
    metrics_path: str | None = None,
    state_path: str | None = None,
) -> tuple[Path, Path]:
    """解析 metrics.ndjson 与 .state.json 的实际读取路径。

    任一参数为 None 时回退到 ``<skill_root>/logs/`` 下的默认路径，保证未传参时
    行为与旧版本完全一致（向后兼容，不会破坏现有看板数据源）。

    非 None 时按用户给定路径（支持 ``~`` 展开）取绝对路径，便于看板从非默认
    位置（如其它会话/项目的日志目录）聚合数据。
    """
    default_metrics = skill_root / "logs" / "metrics.ndjson"
    default_state = skill_root / "logs" / ".state.json"
    if metrics_path:
        metrics = Path(metrics_path).expanduser().absolute()
    else:
        metrics = default_metrics
    if state_path:
        state = Path(state_path).expanduser().absolute()
    else:
        state = default_state
    return metrics, state


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--project-root", default=".", help="devflow 项目根目录（含 .codebuddy/ 和 artifacts/）")
    parser.add_argument("--out", default=None, help="输出路径，默认 <skill_root>/scripts/dashboard/dashboard-data.json")
    parser.add_argument("--metrics-path", default=None, help="覆盖 metrics.ndjson 的读取路径（默认 <skill_root>/logs/metrics.ndjson，支持 ~ 展开）")
    parser.add_argument("--state-path", default=None, help="覆盖 .state.json 的读取路径（默认 <skill_root>/logs/.state.json，支持 ~ 展开）")
    args = parser.parse_args()

    project_root = Path(args.project_root).expanduser().resolve()
    skill_root = SCRIPTS_DIR.parent
    log_path, state_path = resolve_input_paths(skill_root, args.metrics_path, args.state_path)

    events = read_ndjson(log_path)
    state = safe_load_json(state_path) or {}
    if not isinstance(state, dict):
        state = {}

    daily, sessions = build_daily_and_sessions(events)
    model_costs = build_model_costs(events)
    failures = build_failures(events)
    skills, never_used = build_skills(state)
    dispatch = build_dispatch(state)
    auto_dispatch_stats = build_auto_dispatch_stats(project_root)
    cost_by_slug = build_cost_by_task_slug(events)
    devflow_runs = build_devflow_runs(project_root, cost_by_slug)

    from datetime import datetime, timezone
    out_data = {
        "generated_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "source": {
            "metrics_ndjson": str(log_path), "state_json": str(state_path),
            "project_root": str(project_root), "event_count": len(events),
        },
        "daily": daily, "sessions": sessions, "modelCosts": model_costs, "failures": failures,
        "skills": skills, "neverUsed": never_used, "dispatch": dispatch,
        "autoDispatchStats": auto_dispatch_stats, "devflowRuns": devflow_runs,
    }

    out_path = Path(args.out).expanduser().resolve() if args.out else (SCRIPTS_DIR / "dashboard" / "dashboard-data.json")
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(json.dumps(out_data, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"wrote {out_path} ({len(events)} events, {len(sessions)} sessions, {len(devflow_runs)} devflow runs)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
