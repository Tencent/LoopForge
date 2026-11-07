"""持久化状态层。

这一层负责跨 hook 调用保存与协调状态：
- session 级 skill/rule 使用统计
- 当前 agent、派发关系与历史
- transcript offset、去重 claim、pending emits
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
