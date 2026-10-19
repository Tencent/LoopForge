"""持久化状态层。

这一层负责跨 hook 调用保存与协调状态：
- session 级 skill/rule 使用统计
- 当前 agent、派发关系与历史
- transcript offset、去重 claim、pending emits
- AgentLens sidecar 状态
"""
from __future__ import annotations

import json
import time
from contextlib import contextmanager
from pathlib import Path
from typing import Any

try:
    import fcntl
except Exception:  # pragma: no cover - non-posix fallback
    fcntl = None  # type: ignore


def get_state_path(base_dir: Path) -> Path:
    """返回主状态文件路径。"""
    log_dir = base_dir / "logs"
    log_dir.mkdir(parents=True, exist_ok=True)
    return log_dir / ".state.json"


def get_pending_path(base_dir: Path) -> Path:
    """返回 Pre/PostToolUse 配对使用的 pending 文件路径。"""
    log_dir = base_dir / "logs"
    log_dir.mkdir(parents=True, exist_ok=True)
    return log_dir / ".pending_calls.json"


def load_state(state_path: Path) -> dict[str, Any]:
    """从磁盘加载状态；任何异常都回退为空字典。"""
    if state_path.exists():
        try:
            return json.loads(state_path.read_text("utf-8"))
        except Exception:
            return {}
    return {}


def save_state(state_path: Path, state: dict[str, Any]) -> None:
    """把状态写回磁盘。"""
    state_path.write_text(json.dumps(state, ensure_ascii=False), encoding="utf-8")


def get_state_lock_path(state_path: Path) -> Path:
    """返回状态写锁文件路径。"""
    return state_path.parent / ".state.lock"


def get_transcript_seen_path(state_path: Path) -> Path:
    """返回 transcript usage 事件 claim 的 sidecar 台账文件。

    这部分单独存放，不混进 `.state.json`，是为了避免并发 hook 写状态时，
    旧 state 覆盖掉已经 claim 过的 transcript offset。
    """
    return state_path.parent / ".transcript_events_seen.json"


@contextmanager
def state_lock(state_path: Path):
    """为状态修改提供跨进程建议锁（独占）。"""
    lock_path = get_state_lock_path(state_path)
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    with lock_path.open("a+", encoding="utf-8") as fp:
        if fcntl is not None:
            fcntl.flock(fp.fileno(), fcntl.LOCK_EX)
        try:
            yield
        finally:
            if fcntl is not None:
                fcntl.flock(fp.fileno(), fcntl.LOCK_UN)


@contextmanager
def state_read_lock(state_path: Path):
    """为状态一致性读取提供跨进程共享读锁。"""
    lock_path = get_state_lock_path(state_path)
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    with lock_path.open("a+", encoding="utf-8") as fp:
        if fcntl is not None:
            fcntl.flock(fp.fileno(), fcntl.LOCK_SH)
        try:
            yield
        finally:
            if fcntl is not None:
                fcntl.flock(fp.fileno(), fcntl.LOCK_UN)


def update_state_locked(state_path: Path, updater):
    """在跨进程锁保护下完成 load -> mutate -> save。"""
    with state_lock(state_path):
        state = load_state(state_path)
        result = updater(state)
        save_state(state_path, state)
        return result


def load_pending(pending_path: Path) -> dict[str, Any]:
    """加载待配对的 pre/post 数据。"""
    if pending_path.exists():
        try:
            return json.loads(pending_path.read_text("utf-8"))
        except Exception:
            return {}
    return {}


def save_pending(pending_path: Path, data: dict[str, Any]) -> None:
    """保存待配对的 pre/post 数据。"""
    pending_path.write_text(json.dumps(data), encoding="utf-8")


def ensure_session(state: dict, sid: str) -> dict:
    """确保某个 session 的状态结构存在，并返回该 session 字典。

    Session 结构：
    {
        "skills": {name: {count, first_ts, last_ts, tools, via, source, by_agent}},
        "rules": {name: {count, first_ts, last_ts, tools, via, source, by_agent}},
        "started_at": float,
        "current_agent": str,
        "agent_history": [{ts, agent, evidence}],
        "dispatched": {agent_name: count},
        "_skills_meta": {...},   # SessionStart 时缓存的 inventory
        "_rules_meta": {...},
    }
    """
    if sid not in state:
        state[sid] = {
            "skills": {},
            "rules": {},
            "started_at": time.time(),
            "current_agent": "main",
            "agent_history": [],
            "dispatched": {},
        }
    sess = state[sid]
    # 保证字段类型正确，兼容旧数据
    if not isinstance(sess.get("skills"), dict):
        sess["skills"] = {}
    if not isinstance(sess.get("rules"), dict):
        sess["rules"] = {}
    sess.setdefault("current_agent", "main")
    sess.setdefault("agent_history", [])
    sess.setdefault("dispatched", {})
    if not isinstance(sess.get("_pending_tool_emits"), list):
        sess["_pending_tool_emits"] = []
    return sess


def get_devflow_state(state: dict[str, Any], sid: str) -> dict[str, Any]:
    """返回某个 session 缓存的 devflow 上下文与最近一次 stage 快照（见 devflow.py）。"""
    sess = state.get(sid)
    if not isinstance(sess, dict):
        return {}
    dv = sess.get("_devflow")
    return dv if isinstance(dv, dict) else {}


def set_devflow_state(state: dict[str, Any], sid: str, updates: dict[str, Any]) -> None:
    """合并写入某个 session 的 devflow 缓存字段（`checked` / `context` / `last_snapshot`）。"""
    sess = ensure_session(state, sid)
    dv = sess.get("_devflow")
    if not isinstance(dv, dict):
        dv = {}
        sess["_devflow"] = dv
    dv.update(updates)


def get_agentlens_session(state: dict[str, Any], sid: str) -> dict[str, Any]:
    """返回某个 session 的 AgentLens sidecar payload。"""
    sess = ensure_session(state, sid)
    payload = sess.setdefault("_agentlens", {})
    if not isinstance(payload, dict):
        payload = {}
        sess["_agentlens"] = payload
    return payload


AGENTLENS_SCHEMA_VERSION = 2
_TURN_HISTORY_MAX = 5


def _migrate_agentlens_v1_to_v2(payload: dict[str, Any], sess: dict[str, Any] | None) -> dict[str, Any]:
    """把 v1 版 ``_agentlens`` payload 转成以 turn 为中心的 v2 布局。

    v1 布局（pre-turn）：
        {"carrier": {...}, "enabled": bool, "session_id": "...", "app_name": "..."}
    v2 布局（turn-centric）：
        {
          "version": 2,
          "session_id": "...",
          "enabled": bool,
          "app_name": "...",
          "current_turn": {"turn_id": "legacy", "started_at": ..., "carrier": {...}, "subagent_spans": {}, "agent_spans": {}, "step_spans": {}},
          "turn_history": [],
        }

    幂等约束：如果已经带有 ``version``，就原样返回。
    """
    if not isinstance(payload, dict):
        return {"version": AGENTLENS_SCHEMA_VERSION, "enabled": False, "current_turn": None, "turn_history": []}
    if payload.get("version") == AGENTLENS_SCHEMA_VERSION:
        payload.setdefault("current_turn", None)
        payload.setdefault("turn_history", [])
        current = payload.get("current_turn")
        if isinstance(current, dict):
            current.setdefault("subagent_spans", {})
            current.setdefault("agent_spans", {})
            current.setdefault("step_spans", {})
        return payload

    migrated: dict[str, Any] = {
        "version": AGENTLENS_SCHEMA_VERSION,
        "session_id": payload.get("session_id"),
        "enabled": bool(payload.get("enabled", False)),
    }
    if "app_name" in payload:
        migrated["app_name"] = payload.get("app_name")
    if "last_error" in payload:
        migrated["last_error"] = payload.get("last_error")

    legacy_carrier = payload.get("carrier")
    if isinstance(legacy_carrier, dict) and legacy_carrier:
        started_at = None
        if isinstance(sess, dict):
            try:
                started_at = float(sess.get("started_at") or 0) or None
            except Exception:
                started_at = None
        migrated["current_turn"] = {
            "turn_id": "legacy",
            "started_at": started_at or time.time(),
            "carrier": dict(legacy_carrier),
            "subagent_spans": {},
            "agent_spans": {},
            "step_spans": {},
        }
    else:
        migrated["current_turn"] = None
    migrated["turn_history"] = []
    return migrated


def load_agentlens_session(state_path: Path, sid: str) -> dict[str, Any]:
    """加载某个 session 已持久化的 AgentLens sidecar payload。

    这里使用共享读锁，避免并发 ``update_agentlens_session`` 写入时读到撕裂数据。
    返回值会透明地把 v1 payload 迁移成 v2 副本；真正落盘迁移会在下一次写入时完成。
    """
    with state_read_lock(state_path):
        state = load_state(state_path)
    sess = state.get(sid)
    if not isinstance(sess, dict):
        return {}
    payload = sess.get("_agentlens")
    if not isinstance(payload, dict):
        return {}
    # 先给调用方返回迁移后的副本，真正持久化迁移留到后续写入时完成。
    return _migrate_agentlens_v1_to_v2(dict(payload), sess)


def update_agentlens_session(
    state_path: Path,
    sid: str,
    updates: dict[str, Any],
    *,
    clear_keys: list[str] | None = None,
) -> dict[str, Any]:
    """原子地持久化某个 session 的 AgentLens sidecar 字段。

    在合并 updates 之前会先做 v1→v2 迁移，这样磁盘上的 payload 会随着写入自然收敛到 v2。
    """

    def _update(state: dict[str, Any]) -> dict[str, Any]:
        sess = ensure_session(state, sid)
        payload = get_agentlens_session(state, sid)
        # 如果仍是 v1，就原地迁移。
        if payload.get("version") != AGENTLENS_SCHEMA_VERSION:
            migrated = _migrate_agentlens_v1_to_v2(dict(payload), sess)
            payload.clear()
            payload.update(migrated)
        for key in clear_keys or []:
            payload.pop(key, None)
        for key, value in updates.items():
            if value is None:
                payload.pop(key, None)
            else:
                payload[key] = value
        payload["version"] = AGENTLENS_SCHEMA_VERSION
        return dict(payload)

    return update_state_locked(state_path, _update)


def begin_turn(
    state_path: Path,
    sid: str,
    turn_id: str,
    carrier: dict[str, str],
    *,
    started_at: float | None = None,
    max_history: int = _TURN_HISTORY_MAX,
) -> dict[str, Any]:
    """在独占锁下打开一个新的 turn。

    语义：
    - 如果存在 ``current_turn``，先把它归档进有上限的 ``turn_history``。
    - 为新 turn 重置 ``subagent_spans`` / ``agent_spans`` / ``step_spans``。
    - 对相同 ``turn_id`` 保持幂等：重复调用直接返回已有 ``current_turn``，
      不重新生成 trace_id。

    返回最终生成的 ``current_turn`` 字典。
    """
    ts = float(started_at if started_at is not None else time.time())

    def _update(state: dict[str, Any]) -> dict[str, Any]:
        sess = ensure_session(state, sid)
        payload = get_agentlens_session(state, sid)
        if payload.get("version") != AGENTLENS_SCHEMA_VERSION:
            migrated = _migrate_agentlens_v1_to_v2(dict(payload), sess)
            payload.clear()
            payload.update(migrated)

        current = payload.get("current_turn")
        # 幂等：如果同一个 turn_id 还在处理中，就原样返回。
        if isinstance(current, dict) and str(current.get("turn_id") or "") == str(turn_id):
            return dict(current)

        # 归档上一个 turn。
        if isinstance(current, dict) and current.get("turn_id"):
            history = payload.setdefault("turn_history", [])
            if not isinstance(history, list):
                history = []
                payload["turn_history"] = history
            archived = {
                "turn_id": current.get("turn_id"),
                "started_at": current.get("started_at"),
                "ended_at": ts,
                "carrier_traceparent": (current.get("carrier") or {}).get("traceparent"),
            }
            history.append(archived)
            if len(history) > max_history:
                del history[: len(history) - max_history]

        new_turn = {
            "turn_id": str(turn_id),
            "started_at": ts,
            "carrier": dict(carrier or {}),
            "subagent_spans": {},
            "agent_spans": {},
            "step_spans": {},
        }
        payload["current_turn"] = new_turn
        payload["version"] = AGENTLENS_SCHEMA_VERSION
        return dict(new_turn)

    return update_state_locked(state_path, _update)


def upsert_subagent_span(
    state_path: Path,
    sid: str,
    role: str,
    carrier: dict[str, str],
) -> dict[str, str]:
    """在当前 turn 下登记 subagent 的 ``invoke_agent`` 子 span carrier。

    对 `(turn, role)` 维度保持幂等：如果活跃 turn 下已经记录了该角色的 carrier，
    就直接返回已有值，不再重复创建。
    """
    now = time.time()

    def _update(state: dict[str, Any]) -> dict[str, str]:
        payload = get_agentlens_session(state, sid)
        if payload.get("version") != AGENTLENS_SCHEMA_VERSION:
            migrated = _migrate_agentlens_v1_to_v2(dict(payload), state.get(sid) if isinstance(state.get(sid), dict) else None)
            payload.clear()
            payload.update(migrated)
        current = payload.get("current_turn")
        if not isinstance(current, dict):
            # 没有活跃 turn，无法登记 subagent span。
            return {}
        subs = current.setdefault("subagent_spans", {})
        if not isinstance(subs, dict):
            subs = {}
            current["subagent_spans"] = subs
        existing = subs.get(role)
        if isinstance(existing, dict) and isinstance(existing.get("carrier"), dict) and existing["carrier"]:
            return dict(existing["carrier"])
        subs[role] = {"carrier": dict(carrier or {}), "opened_at": now}
        payload["version"] = AGENTLENS_SCHEMA_VERSION
        return dict(carrier or {})

    return update_state_locked(state_path, _update)


def upsert_subagent_span_atomic(
    state_path: Path,
    sid: str,
    role: str,
    *,
    carrier_factory: Any,
) -> dict[str, str]:
    """在写锁内原子地获取或创建 subagent 的 ``invoke_agent`` carrier。

    `carrier_factory` 是一个无参可调用对象，用来生成 carrier 字典。
    只有在 span 还不存在时才会被调用，从而避免这样的 TOCTOU 竞争：
    ``_generate_subagent_carrier`` 先产生了不可逆的 zhiyanllm ``invoke_agent`` span，
    随后另一个 hook 进程已经写入同一角色，导致本次创建变成孤儿。
    """
    now = time.time()

    def _update(state: dict[str, Any]) -> dict[str, str]:
        payload = get_agentlens_session(state, sid)
        if payload.get("version") != AGENTLENS_SCHEMA_VERSION:
            migrated = _migrate_agentlens_v1_to_v2(dict(payload), state.get(sid) if isinstance(state.get(sid), dict) else None)
            payload.clear()
            payload.update(migrated)
        current = payload.get("current_turn")
        if not isinstance(current, dict):
            return {}
        subs = current.setdefault("subagent_spans", {})
        if not isinstance(subs, dict):
            subs = {}
            current["subagent_spans"] = subs
        existing = subs.get(role)
        if isinstance(existing, dict) and isinstance(existing.get("carrier"), dict) and existing["carrier"]:
            return dict(existing["carrier"])
        new_carrier = carrier_factory()
        if not isinstance(new_carrier, dict) or not new_carrier.get("traceparent"):
            return {}
        subs[role] = {"carrier": dict(new_carrier), "opened_at": now}
        payload["version"] = AGENTLENS_SCHEMA_VERSION
        return dict(new_carrier)

    return update_state_locked(state_path, _update)


def get_current_turn(state_path: Path, sid: str) -> dict[str, Any] | None:
    """返回当前活跃的 turn 字典；如果没有打开 turn，就返回 None。"""
    payload = load_agentlens_session(state_path, sid)
    current = payload.get("current_turn")
    return current if isinstance(current, dict) else None


def get_subagent_span_carrier(state_path: Path, sid: str, role: str) -> dict[str, str] | None:
    """返回活跃 turn 下已持久化的 subagent span carrier；如果没有则返回 None。"""
    turn = get_current_turn(state_path, sid)
    if not isinstance(turn, dict):
        return None
    subs = turn.get("subagent_spans")
    if not isinstance(subs, dict):
        return None
    rec = subs.get(role)
    if not isinstance(rec, dict):
        return None
    carrier = rec.get("carrier")
    return dict(carrier) if isinstance(carrier, dict) and carrier else None


def _empty_agent_aggregate() -> dict[str, Any]:
    """创建一份空的 agent 聚合指标骨架。"""
    return {
        "tokens": {
            "input": 0,
            "output": 0,
            "cache_read": 0,
            "cache_creation": 0,
            "total": 0,
        },
        "event_count": 0,
        "cost_usd": 0.0,
        "tool_duration_ms": 0,
        "llm_call_count": 0,
        "tool_call_count": 0,
        "step_count": 0,
    }


def _normalize_agent(agent: str | None) -> str:
    """把 agent 名归一化，空值统一落到 ``main``。"""
    return str(agent or "main").strip() or "main"


def _ensure_turn_agent_spans(current: dict[str, Any]) -> dict[str, Any]:
    """确保当前 turn 下存在 `agent_spans` 容器。"""
    agent_spans = current.setdefault("agent_spans", {})
    if not isinstance(agent_spans, dict):
        agent_spans = {}
        current["agent_spans"] = agent_spans
    return agent_spans


def _ensure_agent_span_rec(current: dict[str, Any], agent: str) -> dict[str, Any]:
    """确保指定 agent 在当前 turn 下有一条完整聚合记录。"""
    agent_spans = _ensure_turn_agent_spans(current)
    rec = agent_spans.get(agent)
    if not isinstance(rec, dict):
        rec = {}
        agent_spans[agent] = rec
    carrier = rec.get("carrier")
    if not isinstance(carrier, dict):
        rec["carrier"] = {}
    aggregate = rec.get("aggregate")
    if not isinstance(aggregate, dict):
        rec["aggregate"] = _empty_agent_aggregate()
    return rec


def get_agent_span_carrier(state_path: Path, sid: str, agent: str) -> dict[str, str] | None:
    """返回活跃 turn 下已持久化的 agent 分组 span carrier；如果没有则返回 None。"""
    turn = get_current_turn(state_path, sid)
    if not isinstance(turn, dict):
        return None
    agent_spans = turn.get("agent_spans")
    if not isinstance(agent_spans, dict):
        return None
    rec = agent_spans.get(_normalize_agent(agent))
    if not isinstance(rec, dict):
        return None
    carrier = rec.get("carrier")
    return dict(carrier) if isinstance(carrier, dict) and carrier else None


def upsert_agent_span_atomic(
    state_path: Path,
    sid: str,
    agent: str,
    *,
    carrier_factory: Any,
) -> dict[str, str]:
    """原子地获取或创建 agent 分组 span carrier。"""
    now = time.time()
    normalized_agent = _normalize_agent(agent)

    def _update(state: dict[str, Any]) -> dict[str, str]:
        payload = get_agentlens_session(state, sid)
        if payload.get("version") != AGENTLENS_SCHEMA_VERSION:
            migrated = _migrate_agentlens_v1_to_v2(
                dict(payload),
                state.get(sid) if isinstance(state.get(sid), dict) else None,
            )
            payload.clear()
            payload.update(migrated)
        current = payload.get("current_turn")
        if not isinstance(current, dict):
            return {}
        rec = _ensure_agent_span_rec(current, normalized_agent)
        existing = rec.get("carrier")
        if isinstance(existing, dict) and existing.get("traceparent"):
            return dict(existing)
        new_carrier = carrier_factory()
        if not isinstance(new_carrier, dict) or not new_carrier.get("traceparent"):
            return {}
        rec["carrier"] = dict(new_carrier)
        rec.setdefault("opened_at", now)
        payload["version"] = AGENTLENS_SCHEMA_VERSION
        return dict(new_carrier)

    return update_state_locked(state_path, _update)


def bump_agent_aggregate(
    state_path: Path,
    sid: str,
    agent: str,
    *,
    usage_event: dict[str, Any] | None = None,
    tool_event: dict[str, Any] | None = None,
    step_created: bool = False,
) -> dict[str, Any]:
    """增加活跃 turn 下按 agent 聚合的统计指标。"""
    normalized_agent = _normalize_agent(agent)

    def _int(value: Any) -> int:
        try:
            return int(value or 0)
        except Exception:
            return 0

    def _float(value: Any) -> float:
        try:
            return float(value or 0)
        except Exception:
            return 0.0

    def _update(state: dict[str, Any]) -> dict[str, Any]:
        payload = get_agentlens_session(state, sid)
        if payload.get("version") != AGENTLENS_SCHEMA_VERSION:
            migrated = _migrate_agentlens_v1_to_v2(
                dict(payload),
                state.get(sid) if isinstance(state.get(sid), dict) else None,
            )
            payload.clear()
            payload.update(migrated)
        current = payload.get("current_turn")
        if not isinstance(current, dict):
            return {}
        rec = _ensure_agent_span_rec(current, normalized_agent)
        aggregate = rec.get("aggregate")
        if not isinstance(aggregate, dict):
            aggregate = _empty_agent_aggregate()
            rec["aggregate"] = aggregate
        tokens = aggregate.setdefault("tokens", {})
        if not isinstance(tokens, dict):
            tokens = {}
            aggregate["tokens"] = tokens

        if step_created:
            aggregate["step_count"] = _int(aggregate.get("step_count")) + 1

        if isinstance(tool_event, dict):
            aggregate["tool_call_count"] = _int(aggregate.get("tool_call_count")) + 1
            ms = tool_event.get("ms")
            if ms is not None:
                aggregate["tool_duration_ms"] = _int(aggregate.get("tool_duration_ms")) + _int(ms)

        if isinstance(usage_event, dict):
            raw_tokens = usage_event.get("tokens")
            if isinstance(raw_tokens, dict):
                # 聚合 agent 摘要里需要暴露的字段。
                for key in ("input", "output", "cache_read", "cache_creation"):
                    tokens[key] = _int(tokens.get(key)) + _int(raw_tokens.get(key))
                # total = input + output，其中 input 已经包含 cache_read。
                tokens["total"] = _int(tokens.get("input")) + _int(tokens.get("output"))
            aggregate["llm_call_count"] = _int(aggregate.get("llm_call_count")) + 1
            aggregate["event_count"] = _int(aggregate.get("event_count")) + 1
            aggregate["cost_usd"] = round(_float(aggregate.get("cost_usd")) + _float(usage_event.get("cost_usd")), 10)

        payload["version"] = AGENTLENS_SCHEMA_VERSION
        return json.loads(json.dumps(aggregate, ensure_ascii=False))

    return update_state_locked(state_path, _update)


def get_subagent_aggregate(state_path: Path, sid: str, role: str) -> dict[str, Any] | None:
    """返回活跃 turn 下当前 agent 的聚合摘要。"""
    turn = get_current_turn(state_path, sid)
    if not isinstance(turn, dict):
        return None
    agent_spans = turn.get("agent_spans")
    if not isinstance(agent_spans, dict):
        return None
    rec = agent_spans.get(_normalize_agent(role))
    if not isinstance(rec, dict):
        return None
    aggregate = rec.get("aggregate")
    if not isinstance(aggregate, dict):
        return None
    return json.loads(json.dumps(aggregate, ensure_ascii=False))


def _step_span_key(agent: str, message_id: str) -> str:
    """为 step span 生成按 agent 与 message_id 唯一定位的 key。"""
    return f"{agent}|{message_id}"


def upsert_step_span(
    state_path: Path,
    sid: str,
    agent: str,
    message_id: str,
    carrier: dict[str, str],
    *,
    transcript_path: str | None = None,
) -> dict[str, str]:
    """在当前 turn 下登记一个 step span carrier，并对 `(turn, agent, message_id)` 保持幂等。"""
    now = time.time()
    normalized_agent = str(agent or "main").strip() or "main"
    normalized_message_id = str(message_id or "").strip()
    if not normalized_message_id:
        return {}

    def _update(state: dict[str, Any]) -> dict[str, str]:
        payload = get_agentlens_session(state, sid)
        if payload.get("version") != AGENTLENS_SCHEMA_VERSION:
            migrated = _migrate_agentlens_v1_to_v2(dict(payload), state.get(sid) if isinstance(state.get(sid), dict) else None)
            payload.clear()
            payload.update(migrated)
        current = payload.get("current_turn")
        if not isinstance(current, dict):
            return {}
        steps = current.setdefault("step_spans", {})
        if not isinstance(steps, dict):
            steps = {}
            current["step_spans"] = steps
        key = _step_span_key(normalized_agent, normalized_message_id)
        existing = steps.get(key)
        if isinstance(existing, dict) and isinstance(existing.get("carrier"), dict) and existing["carrier"]:
            return dict(existing["carrier"])
        steps[key] = {
            "carrier": dict(carrier or {}),
            "opened_at": now,
            "agent": normalized_agent,
            "message_id": normalized_message_id,
            "transcript_path": transcript_path or "",
        }
        payload["version"] = AGENTLENS_SCHEMA_VERSION
        return dict(carrier or {})

    return update_state_locked(state_path, _update)


def upsert_step_span_atomic(
    state_path: Path,
    sid: str,
    agent: str,
    message_id: str,
    *,
    carrier_factory: Any,
    transcript_path: str | None = None,
) -> dict[str, str]:
    """在写锁内原子地获取或创建一个 step span carrier。

    `carrier_factory` 是一个无参可调用对象，用来生成 carrier 字典。
    只有在 span 还不存在时才会被调用，从而避免这样的 TOCTOU 竞争：
    ``_generate_step_carrier`` 已经先产生了不可逆的 zhiyanllm span，
    但随后另一个 hook 进程已经写入同一 step，导致本次创建失去归属。
    """
    now = time.time()
    normalized_agent = str(agent or "main").strip() or "main"
    normalized_message_id = str(message_id or "").strip()
    if not normalized_message_id:
        return {}

    def _update(state: dict[str, Any]) -> dict[str, str]:
        payload = get_agentlens_session(state, sid)
        if payload.get("version") != AGENTLENS_SCHEMA_VERSION:
            migrated = _migrate_agentlens_v1_to_v2(dict(payload), state.get(sid) if isinstance(state.get(sid), dict) else None)
            payload.clear()
            payload.update(migrated)
        current = payload.get("current_turn")
        if not isinstance(current, dict):
            return {}
        steps = current.setdefault("step_spans", {})
        if not isinstance(steps, dict):
            steps = {}
            current["step_spans"] = steps
        key = _step_span_key(normalized_agent, normalized_message_id)
        existing = steps.get(key)
        if isinstance(existing, dict) and isinstance(existing.get("carrier"), dict) and existing["carrier"]:
            return dict(existing["carrier"])
        new_carrier = carrier_factory()
        if not isinstance(new_carrier, dict) or not new_carrier.get("traceparent"):
            return {}
        steps[key] = {
            "carrier": dict(new_carrier),
            "opened_at": now,
            "agent": normalized_agent,
            "message_id": normalized_message_id,
            "transcript_path": transcript_path or "",
        }
        payload["version"] = AGENTLENS_SCHEMA_VERSION
        return dict(new_carrier)

    return update_state_locked(state_path, _update)


def get_step_span_carrier(state_path: Path, sid: str, agent: str, message_id: str) -> dict[str, str] | None:
    """返回活跃 turn 下已持久化的 step span carrier；如果没有则返回 None。"""
    turn = get_current_turn(state_path, sid)
    if not isinstance(turn, dict):
        return None
    steps = turn.get("step_spans")
    if not isinstance(steps, dict):
        return None
    key = _step_span_key(str(agent or "main").strip() or "main", str(message_id or "").strip())
    rec = steps.get(key)
    if not isinstance(rec, dict):
        return None
    carrier = rec.get("carrier")
    return dict(carrier) if isinstance(carrier, dict) and carrier else None


def get_latest_session_id(state: dict[str, Any]) -> str | None:
    """返回 state 中最近启动的 session id。"""
    latest_sid: str | None = None
    latest_started_at = -1.0
    for sid, sess in state.items():
        if sid.startswith("_") or not isinstance(sess, dict):
            continue
        started_at = float(sess.get("started_at", 0) or 0)
        if started_at >= latest_started_at:
            latest_started_at = started_at
            latest_sid = sid
    return latest_sid


def get_pending_tool_emits(state_path: Path, sid: str) -> list[dict[str, Any]]:
    """返回等待与 transcript/message_id 关联的缓冲 tool 事件。"""
    with state_read_lock(state_path):
        state = load_state(state_path)
    sess = state.get(sid)
    if not isinstance(sess, dict):
        return []
    pending = sess.get("_pending_tool_emits")
    if not isinstance(pending, list):
        return []
    return [dict(item) for item in pending if isinstance(item, dict)]


def append_pending_tool_emit(state_path: Path, sid: str, tool_event: dict[str, Any]) -> list[dict[str, Any]]:
    """缓存一条 tool 事件，延后再做 message_id 关联。"""

    def _update(state: dict[str, Any]) -> list[dict[str, Any]]:
        sess = ensure_session(state, sid)
        pending = sess.setdefault("_pending_tool_emits", [])
        if not isinstance(pending, list):
            pending = []
            sess["_pending_tool_emits"] = pending
        pending.append(dict(tool_event))
        return [dict(item) for item in pending if isinstance(item, dict)]

    return update_state_locked(state_path, _update)


def replace_pending_tool_emits(state_path: Path, sid: str, tool_events: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """覆盖某个 session 的缓冲 tool 事件队列。"""

    def _update(state: dict[str, Any]) -> list[dict[str, Any]]:
        sess = ensure_session(state, sid)
        normalized = [dict(item) for item in tool_events if isinstance(item, dict)]
        sess["_pending_tool_emits"] = normalized
        return [dict(item) for item in normalized]

    return update_state_locked(state_path, _update)

def bump_skill(
    sess: dict,
    name: str,
    *,
    via: str,
    tool: str | None,
    meta: dict | None = None,
    submodule: str | None = None,
    agent: str | None = None,
) -> None:
    """增加 skill 使用计数，并按需记录子模块与 agent 维度。"""
    if not name:
        return
    now = time.time()
    rec = sess["skills"].setdefault(name, {
        "count": 0,
        "first_ts": now,
        "last_ts": now,
        "tools": [],
        "via": [],
        "source": None,
        "version": None,
        "submodule_hits": {},
        "by_agent": {},
    })
    rec["count"] += 1
    rec["last_ts"] = now
    if tool and tool not in rec["tools"]:
        rec["tools"].append(tool)
    if via not in rec.get("via", []):
        rec.setdefault("via", []).append(via)
    if meta:
        if meta.get("source") and not rec.get("source"):
            rec["source"] = meta["source"]
        if meta.get("version") and not rec.get("version"):
            rec["version"] = meta["version"]
    # 子模块命中统计
    if submodule:
        sub_dict = rec.setdefault("submodule_hits", {})
        if not isinstance(sub_dict, dict):
            sub_dict = {}
            rec["submodule_hits"] = sub_dict
        sh = sub_dict.setdefault(submodule, {
            "count": 0, "first_ts": now, "last_ts": now, "tools": [],
        })
        sh["count"] += 1
        sh["last_ts"] = now
        if tool and tool not in sh["tools"]:
            sh["tools"].append(tool)
    # 按 agent 维度统计
    if agent:
        ag_dict = rec.setdefault("by_agent", {})
        if not isinstance(ag_dict, dict):
            ag_dict = {}
            rec["by_agent"] = ag_dict
        ar = ag_dict.setdefault(agent, {
            "count": 0, "first_ts": now, "last_ts": now,
            "tools": [], "submodules": [],
        })
        ar["count"] += 1
        ar["last_ts"] = now
        if tool and tool not in ar["tools"]:
            ar["tools"].append(tool)
        if submodule and submodule not in ar["submodules"]:
            ar["submodules"].append(submodule)


def bump_rule(
    sess: dict,
    name: str,
    *,
    via: str,
    tool: str | None,
    meta: dict | None = None,
    agent: str | None = None,
) -> None:
    """增加 rule 使用计数。"""
    if not name:
        return
    now = time.time()
    rec = sess["rules"].setdefault(name, {
        "count": 0,
        "first_ts": now,
        "last_ts": now,
        "tools": [],
        "via": [],
        "source": None,
        "by_agent": {},
    })
    rec["count"] += 1
    rec["last_ts"] = now
    if tool and tool not in rec["tools"]:
        rec["tools"].append(tool)
    if via not in rec.get("via", []):
        rec.setdefault("via", []).append(via)
    if meta and meta.get("source") and not rec.get("source"):
        rec["source"] = meta["source"]
    if agent:
        ag = rec.setdefault("by_agent", {})
        if not isinstance(ag, dict):
            ag = {}
            rec["by_agent"] = ag
        ar = ag.setdefault(agent, {"count": 0, "last_ts": now})
        ar["count"] += 1
        ar["last_ts"] = now


def prune_sessions(state: dict, max_sessions: int = 5) -> None:
    """在 state 中只保留最近的 N 个 session。"""
    # 先过滤掉非 session 键（以下划线开头）
    session_keys = [k for k in state if not k.startswith("_")]
    if len(session_keys) > max_sessions:
        to_remove = sorted(
            session_keys,
            key=lambda s: state[s].get("started_at", 0) if isinstance(state[s], dict) else 0,
        )[:len(session_keys) - max_sessions]
        for k in to_remove:
            del state[k]


def _transcript_scope_key(transcript_path: str | None = None) -> str:
    """把 transcript 路径归一化成内部 scope key。"""
    path = str(transcript_path or "").strip()
    return path or "__session__"


def _ensure_transcript_scopes(sess: dict) -> dict[str, dict[str, Any]]:
    """确保 session 下存在 transcript scope 容器，并兼容旧字段迁移。"""
    scopes = sess.setdefault("_transcripts", {})
    if not isinstance(scopes, dict):
        scopes = {}
        sess["_transcripts"] = scopes

    legacy_scope = scopes.get("__session__")
    if not isinstance(legacy_scope, dict):
        legacy_scope = {}
        scopes["__session__"] = legacy_scope

    if "offset" not in legacy_scope and "_transcript_offset" in sess:
        legacy_scope["offset"] = int(sess.get("_transcript_offset", 0) or 0)

    legacy_usage = sess.get("_last_cumulative_usage")
    if "last_cumulative_usage" not in legacy_scope and isinstance(legacy_usage, dict) and legacy_usage:
        legacy_scope["last_cumulative_usage"] = legacy_usage

    legacy_dedup = sess.get("_last_usage_event")
    if "last_usage_event" not in legacy_scope and isinstance(legacy_dedup, dict) and legacy_dedup:
        legacy_scope["last_usage_event"] = legacy_dedup

    return scopes


def _get_transcript_scope(
    sess: dict,
    transcript_path: str | None = None,
    *,
    create: bool = False,
) -> dict[str, Any] | None:
    """获取指定 transcript 的 scope；必要时按需创建。"""
    scopes = _ensure_transcript_scopes(sess)
    key = _transcript_scope_key(transcript_path)
    scope = scopes.get(key)
    if isinstance(scope, dict):
        return scope
    if create:
        scope = {}
        scopes[key] = scope
        return scope
    return None


def get_transcript_offset(state: dict, sid: str, transcript_path: str | None = None) -> int:
    """获取某个 session/transcript 最近一次上报的 transcript 文件 offset。"""
    sess = state.get(sid)
    if not isinstance(sess, dict):
        return 0

    scope = _get_transcript_scope(sess, transcript_path)
    if isinstance(scope, dict):
        return int(scope.get("offset", 0) or 0)

    if _transcript_scope_key(transcript_path) == "__session__":
        return int(sess.get("_transcript_offset", 0) or 0)
    return 0


def set_transcript_offset(
    state: dict,
    sid: str,
    offset: int,
    transcript_path: str | None = None,
) -> None:
    """在成功上报后持久化 transcript 文件 offset。"""
    sess = state.get(sid)
    if not isinstance(sess, dict):
        return

    scope = _get_transcript_scope(sess, transcript_path, create=True)
    if isinstance(scope, dict):
        scope["offset"] = int(offset or 0)

    if _transcript_scope_key(transcript_path) == "__session__":
        sess["_transcript_offset"] = int(offset or 0)


def get_last_cumulative_usage(
    state: dict,
    sid: str,
    transcript_path: str | None = None,
) -> dict[str, int]:
    """获取某个 transcript 最近一次上报的累计 token usage。"""
    sess = state.get(sid)
    if not isinstance(sess, dict):
        return {}

    scope = _get_transcript_scope(sess, transcript_path)
    if isinstance(scope, dict):
        v = scope.get("last_cumulative_usage")
        if isinstance(v, dict):
            return v

    if _transcript_scope_key(transcript_path) == "__session__":
        v = sess.get("_last_cumulative_usage")
        if isinstance(v, dict):
            return v
    return {}


def get_last_llm_offset(
    state: dict,
    sid: str,
    transcript_path: str | None = None,
) -> int:
    """获取最近一次处理到的 LLM source_offset，用于增量构建 I/O。"""
    sess = state.get(sid)
    if not isinstance(sess, dict):
        return 0
    scope = _get_transcript_scope(sess, transcript_path)
    if isinstance(scope, dict):
        v = scope.get("last_llm_offset")
        if isinstance(v, (int, float)):
            return int(v)
    return 0


def set_last_llm_offset(
    state: dict,
    sid: str,
    offset: int,
    transcript_path: str | None = None,
) -> None:
    """持久化某个 transcript 最近处理到的 LLM source_offset。"""
    sess = state.get(sid)
    if not isinstance(sess, dict):
        return
    scope = _get_transcript_scope(sess, transcript_path, create=True)
    if isinstance(scope, dict):
        scope["last_llm_offset"] = int(offset or 0)


def set_last_cumulative_usage(
    state: dict,
    sid: str,
    usage: dict[str, int],
    transcript_path: str | None = None,
) -> None:
    """在处理完一个 turn 后持久化累计 token usage。"""
    sess = state.get(sid)
    if not isinstance(sess, dict):
        return

    scope = _get_transcript_scope(sess, transcript_path, create=True)
    if isinstance(scope, dict):
        scope["last_cumulative_usage"] = usage

    if _transcript_scope_key(transcript_path) == "__session__":
        sess["_last_cumulative_usage"] = usage


def get_last_usage_event(
    state: dict,
    sid: str,
    transcript_path: str | None = None,
) -> dict[str, Any] | None:
    """获取某个 transcript 最近一次用于去重的 usage 快照。"""
    sess = state.get(sid)
    if not isinstance(sess, dict):
        return None

    scope = _get_transcript_scope(sess, transcript_path)
    if isinstance(scope, dict):
        event = scope.get("last_usage_event")
        if isinstance(event, dict):
            return event

    if _transcript_scope_key(transcript_path) == "__session__":
        event = sess.get("_last_usage_event")
        if isinstance(event, dict):
            return event
    return None


def set_last_usage_event(
    state: dict,
    sid: str,
    usage_event: dict[str, Any],
    transcript_path: str | None = None,
) -> None:
    """持久化某个 transcript 最近一次用于去重的 usage 快照。"""
    sess = state.get(sid)
    if not isinstance(sess, dict):
        return

    scope = _get_transcript_scope(sess, transcript_path, create=True)
    if isinstance(scope, dict):
        scope["last_usage_event"] = usage_event

    if _transcript_scope_key(transcript_path) == "__session__":
        sess["_last_usage_event"] = usage_event


def claim_transcript_event(
    state_path: Path,
    sid: str,
    event_key: str,
    transcript_path: str | None = None,
    *,
    max_seen_keys: int = 512,
) -> bool:
    """跨进程原子地声明一个 transcript 作用域内的事件键。"""
    seen_path = get_transcript_seen_path(state_path)
    scope_key = f"{sid}|{_transcript_scope_key(transcript_path)}"

    with state_lock(state_path):
        if seen_path.exists():
            try:
                ledger = json.loads(seen_path.read_text("utf-8"))
            except Exception:
                ledger = {}
        else:
            ledger = {}
        if not isinstance(ledger, dict):
            ledger = {}

        seen = ledger.setdefault(scope_key, {})
        if not isinstance(seen, dict):
            seen = {}
            ledger[scope_key] = seen
        if event_key in seen:
            return False
        seen[event_key] = time.time()
        if len(seen) > max_seen_keys:
            stale_keys = sorted(seen, key=lambda k: float(seen.get(k, 0) or 0))[:-max_seen_keys]
            for key in stale_keys:
                seen.pop(key, None)
        seen_path.write_text(json.dumps(ledger, ensure_ascii=False), encoding="utf-8")
        return True


def commit_transcript_progress(
    state_path: Path,
    sid: str,
    transcript_path: str | None = None,
    *,
    offset: int | None = None,
    last_cumulative_usage: dict[str, int] | None = None,
) -> None:
    """在处理完一个窗口后，原子地持久化 transcript 解析进度。"""

    def _update(state: dict[str, Any]) -> None:
        sess = ensure_session(state, sid)
        scope = _get_transcript_scope(sess, transcript_path, create=True)
        if not isinstance(scope, dict):
            return
        if offset is not None:
            current_offset = int(scope.get("offset", 0) or 0)
            next_offset = int(offset or 0)
            if next_offset > current_offset:
                scope["offset"] = next_offset
                if _transcript_scope_key(transcript_path) == "__session__":
                    sess["_transcript_offset"] = next_offset
        if isinstance(last_cumulative_usage, dict) and last_cumulative_usage:
            scope["last_cumulative_usage"] = last_cumulative_usage
            if _transcript_scope_key(transcript_path) == "__session__":
                sess["_last_cumulative_usage"] = last_cumulative_usage

    update_state_locked(state_path, _update)
