"""通用采集层。

这一层负责最基础的观测数据采集：
- 维护 Pre/PostToolUse 的配对关系，计算工具耗时
- 解析 transcript 增量，提取 usage / model / tool 记录
- 把 skill/rule 命中情况写入 session state
"""
from __future__ import annotations

import json
import os
import time
from pathlib import Path
from typing import Any

from . import agent_identity, scanner, state as st

# 需要从 transcript 中提取的 token 字段
TOKEN_KEYS = (
    "input_tokens", "output_tokens", "cache_read_input_tokens",
    "cache_creation_input_tokens", "total_tokens",
)

# transcript 记录里可能携带模型名的字段
MODEL_KEYS = ("model", "requestModelName", "requestModelId")
MESSAGE_ID_KEYS = ("messageId", "responseId", "requestId")


def transcript_path(data: dict[str, Any]) -> str:
    """从 hook payload 中提取 transcript 路径字段。"""
    return str(data.get("transcript_path") or "")


def read_stdin_json() -> dict:
    """从 stdin 读取 JSON；失败时返回空字典。"""
    import sys
    try:
        raw = sys.stdin.read()
        if raw.strip():
            d = json.loads(raw)
            if isinstance(d, dict):
                return d
        return {}
    except Exception as e:
        return {"_parse_error": str(e)}


def record_pre(pending_path: Path, data: dict) -> None:
    """记录一条 PreToolUse，供后续 PostToolUse 计算耗时。"""
    pending = st.load_pending(pending_path)
    sid = data.get("session_id", "?")
    tool = data.get("tool_name", "?")
    key = f"{sid}::{tool}::{time.time_ns()}"
    pending[key] = {
        "start": time.time(),
        "tool_name": tool,
        "session_id": sid,
    }
    # 只保留最近 20 条 pending 记录
    if len(pending) > 20:
        for k in list(pending.keys())[:-20]:
            pending.pop(k, None)
    st.save_pending(pending_path, pending)


def record_post(pending_path: Path, data: dict) -> int | None:
    """把 PostToolUse 与之前的 PreToolUse 配对，并返回耗时毫秒数。"""
    pending = st.load_pending(pending_path)
    tool = data.get("tool_name")
    sid = data.get("session_id", "?")
    candidates = [(k, v) for k, v in pending.items()
                  if v.get("session_id") == sid and v.get("tool_name") == tool]
    if not candidates:
        return None
    candidates.sort(key=lambda kv: kv[1]["start"])
    key, item = candidates[-1]
    duration_ms = int((time.time() - item["start"]) * 1000)
    pending.pop(key, None)
    st.save_pending(pending_path, pending)
    return duration_ms


def parse_transcript_tail(
    path: str,
    max_lines: int = 50,
    last_offset: int = 0,
) -> dict[str, Any]:
    """解析 transcript 的增量 JSONL 内容，提取 usage、model 与 tool 元数据。

    返回结构：
        {
            "tokens": {...} or None,        # 最新累计 usage
            "prev_tokens": {...} or None,   # 倒数第二条累计 usage（兼容字段）
            "model": str or None,
            "offset": int,                  # 当前 EOF
            "tool_records": [
                {"offset": int, "timestamp_ms": int | None, "tool": str, "call_id": str | None, "message_id": str | None, "kind": str},
                ...
            ],
            "usage_records": [
                {"offset": int, "tokens": {...}, "model": str | None, "message_id": str | None},
                ...
            ],
        }

    ``usage_records`` 会保留 ``last_offset`` 之后发现的全部 usage 项，
    这样调用方可以回放窗口内每一次模型请求，而不只是最后一个快照。
    """
    summary: dict[str, Any] = {
        "tokens": None,
        "prev_tokens": None,
        "model": None,
        "offset": 0,
        "tool_records": [],
        "usage_records": [],
    }
    if not path or not os.path.isfile(path):
        return summary

    try:
        file_size = os.path.getsize(path)
        summary["offset"] = file_size
        if file_size <= last_offset:
            return summary

        with open(path, "rb") as fp:
            read_start = max(0, int(last_offset or 0))
            fp.seek(read_start)
            tail_bytes = fp.read()
    except Exception:
        return summary

    last_model_seen: str | None = None
    usage_history: list[dict[str, Any]] = []
    pending_tool_records: list[dict[str, Any]] = []
    cursor = max(0, int(last_offset or 0))

    for raw_line in tail_bytes.splitlines(keepends=True):
        cursor += len(raw_line)
        if not raw_line.lstrip().startswith(b"{"):
            continue
        try:
            obj = json.loads(raw_line.decode("utf-8", errors="ignore"))
        except Exception:
            continue

        rec_type = str(obj.get("type") or "").strip().lower() if isinstance(obj, dict) else ""
        message_id = _find_message_id(obj)
        m = _find_model(obj)
        if m:
            last_model_seen = m
        line_tool_records = _find_tool_records(obj)
        if message_id and rec_type not in {"function_call", "function_call_result"}:
            for pending_record in pending_tool_records:
                pending_record["next_message_id"] = message_id
            pending_tool_records = []
        for tool_record in line_tool_records:
            tool_record["offset"] = cursor
            tool_record["timestamp_ms"] = _find_timestamp_ms(obj)
            tool_record["message_id"] = message_id
            summary["tool_records"].append(tool_record)
            pending_tool_records.append(tool_record)
        usage = _find_usage(obj)
        if not usage:
            continue

        usage_history.append({
            "offset": cursor,
            "tokens": usage,
            "model": m or last_model_seen,
            "message_id": message_id,
        })
        if m:
            summary["model"] = m

    if usage_history:
        summary["usage_records"] = usage_history
        summary["tokens"] = usage_history[-1]["tokens"]
        if len(usage_history) >= 2:
            summary["prev_tokens"] = usage_history[-2]["tokens"]

    if not summary.get("model") and last_model_seen:
        summary["model"] = last_model_seen
    return summary


def record_tool_usage(
    state_path: Path,
    sid: str,
    data: dict,
    skills_meta: dict,
    rules_meta: dict,
    active_agent: str | None = None,
    collect_skills: bool = False,
) -> tuple[list[str], list[str]]:
    """把一次 tool 调用命中的 skill/rule 写入 session state。

    Returns:
        (used_skills, used_rules): 本次工具调用命中的 skill/rule 名称列表。
    """
    tool = data.get("tool_name")

    def _update(state: dict[str, Any]) -> tuple[list[str], list[str]]:
        sess = st.ensure_session(state, sid)
        agent = active_agent or sess.get("current_agent") or "main"

        used_skills: set[str] = set()
        used_rules: set[str] = set()

        # 1) 可选的 skill 收集
        if collect_skills:
            # 直接 use_skill 调用
            direct = scanner.extract_skill_from_tool_call(data)
            if direct:
                st.bump_skill(sess, direct, via="use_skill", tool=tool,
                              meta=skills_meta.get(direct), agent=agent)
                used_skills.add(direct)

            # 路径推断
            path_skills, _ = scanner.extract_paths_from_tool_call(data)
            for s in path_skills:
                st.bump_skill(sess, s, via="path-inferred", tool=tool,
                              meta=skills_meta.get(s), agent=agent)
                used_skills.add(s)

            # 子模块命中
            for hit in scanner.extract_submodule_hits(data):
                skill_name = hit["skill"]
                # unknown 仅代表“命中子模块但无法确定 skill 名称”，不写入实时 skill 字段
                if not skill_name or skill_name == "unknown":
                    continue
                st.bump_skill(sess, skill_name, via="submodule", tool=tool,
                              meta=skills_meta.get(skill_name),
                              submodule=hit["submodule"], agent=agent)
                used_skills.add(skill_name)

            # Bash skill 脚本
            for s in scanner.extract_bash_skill_scripts(data):
                st.bump_skill(sess, s, via="bash-script", tool=tool,
                              meta=skills_meta.get(s), agent=agent)
                used_skills.add(s)

        # 2) 基于路径推断的 rule（始终开启）
        _, path_rules = scanner.extract_paths_from_tool_call(data)
        for r in path_rules:
            st.bump_rule(sess, r, via="path-inferred", tool=tool, meta=rules_meta.get(r), agent=agent)
            used_rules.add(r)

        # 3) 当前激活的 rule（始终开启）
        for r in scanner.active_rules_for_call(data, rules_meta, active_agent=agent):
            st.bump_rule(sess, r, via="active-rule", tool=tool, meta=rules_meta.get(r), agent=agent)
            used_rules.add(r)

        st.prune_sessions(state)
        return sorted(used_skills), sorted(used_rules)

    return st.update_state_locked(state_path, _update)



def cache_inventory(
    state_path: Path,
    sid: str,
    skills_meta: dict,
    rules_meta: dict,
) -> None:
    """把完整的 skill/rule inventory 缓存在 session state 中（在 SessionStart 调用）。"""
    def _update(state: dict[str, Any]) -> None:
        sess = st.ensure_session(state, sid)
        sess["_skills_meta"] = skills_meta
        sess["_rules_meta"] = rules_meta
        sess["_inventory_scanned_at"] = time.time()

        # 为所有已知 skill/rule 预先初始化 count=0 的 usage 记录
        for name, meta in skills_meta.items():
            rec = sess["skills"].setdefault(name, {
                "count": 0, "first_ts": None, "last_ts": None,
                "tools": [], "via": ["static-scanned"],
                "source": meta.get("source"), "version": meta.get("version"),
            })
            if not rec.get("source"):
                rec["source"] = meta.get("source")
            if "static-scanned" not in rec.get("via", []):
                rec.setdefault("via", []).append("static-scanned")

        for name, meta in rules_meta.items():
            rec = sess["rules"].setdefault(name, {
                "count": 0, "first_ts": None, "last_ts": None,
                "tools": [], "via": ["static-scanned"], "source": meta.get("source"),
            })
            if "static-scanned" not in rec.get("via", []):
                rec.setdefault("via", []).append("static-scanned")
            if not rec.get("source"):
                rec["source"] = meta.get("source")

        st.prune_sessions(state)

    st.update_state_locked(state_path, _update)


def load_cached_inventory(state_path: Path, sid: str) -> tuple[dict, dict]:
    """从 state 中读取缓存的 skill/rule inventory，避免在热路径里执行 rglob。"""
    state = st.load_state(state_path)
    sess = state.get(sid, {})
    if not isinstance(sess, dict):
        return {}, {}
    return (
        sess.get("_skills_meta") or {},
        sess.get("_rules_meta") or {},
    )


def get_session_usage(state_path: Path, sid: str) -> tuple[dict, dict]:
    """返回某个 session 的 skill/rule usage 字典。"""
    state = st.load_state(state_path)
    sess = state.get(sid, {})
    if not isinstance(sess, dict):
        return {}, {}
    return sess.get("skills", {}) or {}, sess.get("rules", {}) or {}

def related_transcript_paths(sid: str, transcript_path: str) -> list[str]:
    """收集当前 session 的主 transcript 以及所有 subagent transcript。"""
    paths: list[Path] = []
    current = Path(transcript_path).expanduser() if transcript_path else None
    if current and current.is_file():
        paths.append(current)

    bundle_dir: Path | None = None
    if current:
        if current.name == f"{sid}.jsonl":
            candidate = current.with_suffix("")
            if candidate.is_dir():
                bundle_dir = candidate
        else:
            for parent in [current.parent, *current.parents]:
                if parent.name == sid and parent.is_dir():
                    bundle_dir = parent
                    break

    if bundle_dir is None and current:
        candidate = current.parent / sid
        if candidate.is_dir():
            bundle_dir = candidate

    if bundle_dir is not None:
        main_transcript = bundle_dir.with_suffix(".jsonl")
        if main_transcript.is_file():
            paths.append(main_transcript)
        subagents_dir = bundle_dir / "subagents"
        if subagents_dir.is_dir():
            paths.extend(sorted(path for path in subagents_dir.glob("*.jsonl") if path.is_file()))

    out: list[str] = []
    seen: set[str] = set()
    for path in paths:
        resolved = str(path.resolve())
        if resolved in seen:
            continue
        seen.add(resolved)
        out.append(resolved)
    return out


def _transcript_size_signature(paths: list[str]) -> tuple[tuple[str, int], ...]:
    """基于路径和文件大小生成 transcript 稳定性签名。"""
    signature: list[tuple[str, int]] = []
    for path in paths:
        try:
            size = Path(path).stat().st_size
        except Exception:
            size = -1
        signature.append((path, int(size)))
    return tuple(signature)


def settled_related_transcript_paths(
    sid: str,
    transcript_path: str,
    *,
    max_wait_s: float = 2.0,
    interval_s: float = 0.4,
    stable_rounds: int = 2,
) -> list[str]:
    """在 replay 前短暂等待 transcript 文件停止增长。"""
    deadline = time.monotonic() + max(0.0, max_wait_s)
    previous: tuple[tuple[str, int], ...] | None = None
    stable_count = 0
    paths = related_transcript_paths(sid, transcript_path)

    while True:
        paths = related_transcript_paths(sid, transcript_path)
        current = _transcript_size_signature(paths)
        if current == previous:
            stable_count += 1
        else:
            previous = current
            stable_count = 0
        if stable_count >= stable_rounds or time.monotonic() >= deadline:
            return paths
        sleep_for = min(interval_s, max(0.0, deadline - time.monotonic()))
        if sleep_for <= 0:
            return paths
        time.sleep(sleep_for)


def session_id_for_transcript_path(transcript_path: str) -> str | None:
    """从 transcript 文件内容中反查 session id。"""
    path = Path(transcript_path)
    try:
        with path.open("r", encoding="utf-8") as fp:
            for _ in range(8):
                line = fp.readline()
                if not line:
                    break
                try:
                    record = json.loads(line)
                except Exception:
                    continue
                session_id = record.get("sessionId")
                if isinstance(session_id, str) and session_id.strip():
                    return session_id.strip()
    except Exception:
        return None
    return None


def resolve_transcript_path_alias(sid: str, transcript_path: str) -> str:
    """把别名 transcript 路径解析成当前 session 下的真实文件路径。"""
    if not transcript_path:
        return ""
    current = Path(transcript_path).expanduser()
    if current.is_file():
        return str(current.resolve())

    alias_session_id = current.stem if current.suffix == ".jsonl" else ""
    if not alias_session_id or alias_session_id == sid:
        return str(current)

    for candidate in related_transcript_paths(sid, transcript_path):
        if session_id_for_transcript_path(candidate) == alias_session_id:
            return candidate
    return str(current)

def compute_usage_delta(current: dict[str, int], prev: dict[str, int] | None) -> dict[str, int]:
    """把一条 usage 记录归一化成当前要发出的 event payload。"""
    _ = prev
    out = dict(current)
    out["total"] = int(out.get("input", 0) or 0) + int(out.get("output", 0) or 0)
    return out


def normalize_tokens(tokens: dict[str, Any] | None) -> dict[str, int] | None:
    """兼容多种 token 字段命名，并统一折叠成一套 schema。"""
    if not isinstance(tokens, dict):
        return None

    def _int(value: Any) -> int:
        try:
            return int(value or 0)
        except Exception:
            return 0

    input_t = _int(tokens.get("input") if "input" in tokens else tokens.get("input_tokens"))
    output_t = _int(tokens.get("output") if "output" in tokens else tokens.get("output_tokens"))
    cache_read = _int(tokens.get("cache_read") if "cache_read" in tokens else tokens.get("cache_read_input_tokens"))
    cache_creation = _int(tokens.get("cache_creation") if "cache_creation" in tokens else tokens.get("cache_creation_input_tokens"))
    total = input_t + output_t
    if input_t <= 0 and output_t <= 0 and total <= 0:
        return None
    out = {"input": input_t, "output": output_t, "cache_read": cache_read, "total": total}
    if cache_creation > 0:
        out["cache_creation"] = cache_creation
    return out


def collect_transcript_entries(
    state_path: Path,
    sid: str,
    transcript_path: str,
    *,
    max_lines: int,
) -> dict[str, Any]:
    """读取 transcript 增量，并返回带 source offset 的解析结果。"""
    state_data = st.load_state(state_path)
    last_offset = st.get_transcript_offset(state_data, sid, transcript_path)
    tail = parse_transcript_tail(transcript_path, max_lines=max_lines, last_offset=last_offset)
    new_offset = int(tail.get("offset", 0) or 0)

    entries: list[dict[str, Any]] = []
    tool_records: list[dict[str, Any]] = []
    last_cumulative = st.get_last_cumulative_usage(state_data, sid, transcript_path) or None
    usage_records = tail.get("usage_records") if isinstance(tail, dict) else None
    if not isinstance(usage_records, list):
        usage_records = []
    raw_tool_records = tail.get("tool_records") if isinstance(tail, dict) else None
    if not isinstance(raw_tool_records, list):
        raw_tool_records = []

    for raw_entry in usage_records:
        if not isinstance(raw_entry, dict):
            continue
        current = normalize_tokens(raw_entry.get("tokens"))
        if not current:
            continue
        delta = compute_usage_delta(current, last_cumulative)
        entries.append({
            "offset": int(raw_entry.get("offset", 0) or 0),
            "tokens": delta,
            "model": str(raw_entry.get("model") or tail.get("model") or "") or None,
            "message_id": str(raw_entry.get("message_id") or "").strip() or None,
        })
        last_cumulative = current

    for raw_tool_record in raw_tool_records:
        if not isinstance(raw_tool_record, dict):
            continue
        tool_name = str(raw_tool_record.get("tool") or "").strip()
        if not tool_name:
            continue
        tool_records.append({
            "offset": int(raw_tool_record.get("offset", 0) or 0),
            "timestamp_ms": int(raw_tool_record.get("timestamp_ms", 0) or 0) or None,
            "tool": tool_name,
            "call_id": str(raw_tool_record.get("call_id") or "").strip() or None,
            "kind": str(raw_tool_record.get("kind") or "").strip() or None,
            "message_id": str(raw_tool_record.get("message_id") or "").strip() or None,
            "next_message_id": str(raw_tool_record.get("next_message_id") or "").strip() or None,
            "arguments": raw_tool_record.get("arguments"),
            "arguments_display_text": raw_tool_record.get("arguments_display_text"),
            "result_content": raw_tool_record.get("result_content"),
            "raw_response": raw_tool_record.get("raw_response"),
            "output_text": raw_tool_record.get("output_text"),
        })

    return {
        "entries": entries,
        "tool_records": tool_records,
        "new_offset": new_offset,
        "last_cumulative": last_cumulative,
    }


def find_current_tool_message_id(
    state_path: Path,
    sid: str,
    transcript_path: str,
    tool_name: str,
    *,
    max_lines: int = 80,
) -> str | None:
    """找到当前 tool 事件应该归属的 message id。"""
    if not transcript_path or not tool_name:
        return None
    scan = collect_transcript_entries(
        state_path=state_path,
        sid=sid,
        transcript_path=transcript_path,
        max_lines=max_lines,
    )
    tool_records = scan.get("tool_records") or []
    latest_message_id: str | None = None
    latest_offset = -1
    for rec in tool_records:
        if not isinstance(rec, dict):
            continue
        if str(rec.get("tool") or "") != str(tool_name):
            continue
        message_id = (
            str(rec.get("next_message_id") or "").strip()
            or str(rec.get("message_id") or "").strip()
            or None
        )
        if not message_id:
            continue
        offset = int(rec.get("offset", 0) or 0)
        if offset >= latest_offset:
            latest_offset = offset
            latest_message_id = message_id
    return latest_message_id


def tool_details_from_record(rec: dict[str, Any]) -> dict[str, Any]:
    """把原始 tool 记录投影成日志里使用的紧凑 detail 结构。"""
    details: dict[str, Any] = {}
    call_id = str(rec.get("call_id") or "").strip() or None
    if call_id:
        details["call_id"] = call_id
    arguments = rec.get("arguments")
    if arguments is not None:
        details["arguments"] = arguments
    arguments_display_text = rec.get("arguments_display_text")
    if arguments_display_text is not None:
        details["arguments_display_text"] = arguments_display_text
    result_content = rec.get("result_content")
    if result_content is not None:
        details["result_content"] = result_content
    raw_response = rec.get("raw_response")
    if isinstance(raw_response, dict) and raw_response:
        details["raw_response"] = raw_response
    output_text = rec.get("output_text")
    if output_text is not None:
        details["output_text"] = output_text
    next_message_id = str(rec.get("next_message_id") or "").strip() or None
    original_message_id = str(rec.get("message_id") or "").strip() or None
    if next_message_id and original_message_id:
        details["original_message_id"] = original_message_id
    if next_message_id:
        details["next_message_id"] = next_message_id
    if original_message_id and next_message_id and original_message_id != next_message_id:
        details["message_id_reassigned"] = True
    return details


def tool_record_merge_key(rec: dict[str, Any]) -> str:
    """为一条 tool record 生成去重合并时使用的稳定 key。"""
    call_id = str(rec.get("call_id") or "").strip()
    if call_id:
        return f"call_id:{call_id}"
    offset = int(rec.get("offset", 0) or 0)
    tool = str(rec.get("tool") or "").strip()
    message_id = str(rec.get("message_id") or "").strip()
    timestamp_ms = int(rec.get("timestamp_ms", 0) or 0)
    return f"fallback:{tool}:{message_id}:{timestamp_ms}:{offset}"


def merge_tool_records(tool_records: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """对指向同一次调用的 tool 记录做去重合并。"""
    merged: dict[str, dict[str, Any]] = {}
    order: list[str] = []
    for rec in tool_records:
        if not isinstance(rec, dict):
            continue
        key = tool_record_merge_key(rec)
        if key not in merged:
            merged[key] = {
                "offset": int(rec.get("offset", 0) or 0),
                "timestamp_ms": int(rec.get("timestamp_ms", 0) or 0) or None,
                "tool": str(rec.get("tool") or "").strip(),
                "call_id": str(rec.get("call_id") or "").strip() or None,
                "kind": str(rec.get("kind") or "").strip() or None,
                "message_id": str(rec.get("message_id") or "").strip() or None,
                "next_message_id": str(rec.get("next_message_id") or "").strip() or None,
                "arguments": rec.get("arguments"),
                "arguments_display_text": rec.get("arguments_display_text"),
                "result_content": rec.get("result_content"),
                "raw_response": rec.get("raw_response"),
                "output_text": rec.get("output_text"),
            }
            order.append(key)
            continue

        current = merged[key]
        current["offset"] = max(int(current.get("offset", 0) or 0), int(rec.get("offset", 0) or 0))
        current_ts = int(current.get("timestamp_ms", 0) or 0)
        rec_ts = int(rec.get("timestamp_ms", 0) or 0)
        if current_ts <= 0 and rec_ts > 0:
            current["timestamp_ms"] = rec_ts
        for field in ("tool", "call_id", "kind", "message_id", "next_message_id", "arguments", "arguments_display_text", "result_content", "raw_response", "output_text"):
            if current.get(field) is None and rec.get(field) is not None:
                current[field] = rec.get(field)
        if rec.get("arguments") is not None:
            current["arguments"] = rec.get("arguments")
        if rec.get("arguments_display_text") is not None:
            current["arguments_display_text"] = rec.get("arguments_display_text")
        if rec.get("result_content") is not None:
            current["result_content"] = rec.get("result_content")
        if rec.get("raw_response") is not None:
            current["raw_response"] = rec.get("raw_response")
        if rec.get("output_text") is not None:
            current["output_text"] = rec.get("output_text")

    return [merged[key] for key in order]


def tool_context_claim_key(rec: dict[str, Any]) -> str:
    """为 tool context 生成 claim 去重键，避免重复消费。"""
    call_id = str(rec.get("call_id") or "").strip()
    if call_id:
        return f"tool_call|{call_id}"
    tool = str(rec.get("tool") or "").strip()
    message_id = str(rec.get("message_id") or "").strip()
    timestamp_ms = int(rec.get("timestamp_ms", 0) or 0)
    offset = int(rec.get("offset", 0) or 0)
    return f"tool_call|{tool}|{message_id}|{timestamp_ms}|{offset}"


def find_current_tool_context(
    state_path: Path,
    sid: str,
    transcript_path: str,
    tool_name: str,
    *,
    call_id: str | None = None,
    event_ts: float | None = None,
    max_lines: int = 80,
    claim: bool = False,
) -> dict[str, Any] | None:
    """为当前 tool 调用解析最合适的 transcript 上下文块。"""
    if not tool_name:
        return None
    event_ts_ms = int(float(event_ts) * 1000) if event_ts else None
    normalized_call_id = str(call_id or "").strip() or None

    resolved_path = resolve_transcript_path_alias(sid, transcript_path)
    candidate_paths: list[str] = []
    if resolved_path:
        candidate_paths.append(resolved_path)
    for path in related_transcript_paths(sid, transcript_path):
        if path not in candidate_paths:
            candidate_paths.append(path)

    candidates: list[tuple[tuple[int, int, int], dict[str, Any]]] = []
    for path in candidate_paths:
        scan = collect_transcript_entries(
            state_path=state_path,
            sid=sid,
            transcript_path=path,
            max_lines=max_lines,
        )
        tool_records = merge_tool_records(scan.get("tool_records") or [])
        for rec in tool_records:
            if not isinstance(rec, dict):
                continue
            if str(rec.get("tool") or "") != str(tool_name):
                continue
            rec_call_id = str(rec.get("call_id") or "").strip() or None
            if normalized_call_id and rec_call_id != normalized_call_id:
                continue
            message_id = (
                str(rec.get("next_message_id") or "").strip()
                or str(rec.get("message_id") or "").strip()
                or None
            )
            if not message_id:
                continue
            offset = int(rec.get("offset", 0) or 0)
            timestamp_ms = int(rec.get("timestamp_ms", 0) or 0)
            if normalized_call_id:
                score = (0, 0, -offset)
            elif event_ts_ms and timestamp_ms > 0:
                delta = timestamp_ms - event_ts_ms
                score = (abs(delta), 0 if delta >= 0 else 1, -offset)
            else:
                score = (10**12, 1, -offset)
            candidates.append((
                score,
                {
                    "message_id": message_id,
                    "transcript_path": path,
                    "agent": agent_identity.agent_for_transcript_path(path, "main"),
                    "pt_id": agent_identity.pt_id_from_transcript_path(path),
                    "tool_details": tool_details_from_record(rec),
                    "_claim_key": tool_context_claim_key(rec),
                },
            ))

    candidates.sort(key=lambda item: item[0])
    saw_duplicate = False
    for _, candidate in candidates:
        if not claim:
            out = {
                "message_id": str(candidate.get("message_id") or ""),
                "transcript_path": str(candidate.get("transcript_path") or ""),
                "agent": str(candidate.get("agent") or "main"),
                "tool_details": dict(candidate.get("tool_details") or {}),
            }
            pt_id = candidate.get("pt_id")
            if pt_id:
                out["pt_id"] = pt_id
            return out
        if st.claim_transcript_event(
            state_path,
            sid,
            str(candidate.get("_claim_key") or ""),
            str(candidate.get("transcript_path") or "") or None,
        ):
            out = {
                "message_id": str(candidate.get("message_id") or ""),
                "transcript_path": str(candidate.get("transcript_path") or ""),
                "agent": str(candidate.get("agent") or "main"),
                "tool_details": dict(candidate.get("tool_details") or {}),
            }
            pt_id = candidate.get("pt_id")
            if pt_id:
                out["pt_id"] = pt_id
            return out
        saw_duplicate = True

    if saw_duplicate:
        return {"duplicate": True}
    return None


def extract_tool_call_id(data: dict[str, Any]) -> str | None:
    """从事件 payload 或 tool_input 中提取 tool call id。"""
    for key in ("call_id", "callId", "tool_call_id", "toolCallId"):
        value = data.get(key)
        if isinstance(value, str) and value.strip():
            return value.strip()

    tool_input = data.get("tool_input")
    if isinstance(tool_input, dict):
        for key in ("call_id", "callId", "tool_call_id", "toolCallId", "id"):
            value = tool_input.get(key)
            if isinstance(value, str) and value.strip():
                return value.strip()
    return None


def find_fallback_usage_event(
    tool_event: dict[str, Any],
    usage_events: list[dict[str, Any]],
) -> dict[str, Any] | None:
    """当找不到直接匹配的 tool usage 时，为 AgentLens 选一条兜底 usage。"""
    tool_name = str(tool_event.get("tool") or "").strip()
    transcript_path = str(tool_event.get("transcript_path") or "").strip()
    agent = str(tool_event.get("agent") or "main").strip() or "main"

    strong_matches: list[dict[str, Any]] = []
    weak_matches: list[dict[str, Any]] = []
    for usage_event in usage_events:
        if not isinstance(usage_event, dict):
            continue
        if (str(usage_event.get("agent") or "main").strip() or "main") != agent:
            continue
        if transcript_path and str(usage_event.get("transcript_path") or "").strip() != transcript_path:
            continue
        if not str(usage_event.get("message_id") or "").strip():
            continue

        weak_matches.append(usage_event)
        event_tool = str(usage_event.get("tool") or "").strip()
        if tool_name and event_tool in {tool_name, "model_request"}:
            strong_matches.append(usage_event)

    if strong_matches:
        return strong_matches[-1]
    if weak_matches:
        return weak_matches[-1]
    return None


def build_transcript_event_key(*, kind: str, tool: str, entry: dict[str, Any]) -> str:
    """为 transcript 重放出的事件生成幂等键。"""
    _ = (kind, tool)
    return "|".join(["model_request", str(entry.get("offset", 0) or 0)])

def _find_usage(obj: Any, _depth: int = 0) -> dict | None:
    """递归查找 JSON 对象里的 token usage。"""
    if _depth > 10:
        return None
    if isinstance(obj, dict):
        if "usage" in obj and isinstance(obj["usage"], dict):
            u = obj["usage"]
            picked = {k: u[k] for k in TOKEN_KEYS if k in u}
            if picked:
                return picked
        for v in obj.values():
            r = _find_usage(v, _depth + 1)
            if r:
                return r
    elif isinstance(obj, list):
        for v in obj:
            r = _find_usage(v, _depth + 1)
            if r:
                return r
    return None


def _find_model(obj: Any, _depth: int = 0) -> str | None:
    """递归查找 JSON 对象里的非空模型名。

    优先使用 `providerData.model` 或顶层 `model`，
    其次回退到 `requestModelName` / `requestModelId`。
    """
    if _depth > 10:
        return None
    if isinstance(obj, dict):
        for k in MODEL_KEYS:
            v = obj.get(k)
            if isinstance(v, str) and v.strip():
                return v.strip()
        for v in obj.values():
            r = _find_model(v, _depth + 1)
            if r:
                return r
    elif isinstance(obj, list):
        for v in obj:
            r = _find_model(v, _depth + 1)
            if r:
                return r
    return None


def _find_message_id(obj: Any, _depth: int = 0) -> str | None:
    """递归查找稳定的 message/request 分组标识。"""
    if _depth > 10:
        return None
    if isinstance(obj, dict):
        provider = obj.get("providerData")
        if isinstance(provider, dict):
            for key in MESSAGE_ID_KEYS:
                value = provider.get(key)
                if isinstance(value, str) and value.strip():
                    return value.strip()
        extra = obj.get("extra")
        if isinstance(extra, dict):
            for key in MESSAGE_ID_KEYS:
                value = extra.get(key)
                if isinstance(value, str) and value.strip():
                    return value.strip()
        for key in MESSAGE_ID_KEYS:
            value = obj.get(key)
            if isinstance(value, str) and value.strip():
                return value.strip()
        for v in obj.values():
            r = _find_message_id(v, _depth + 1)
            if r:
                return r
    elif isinstance(obj, list):
        for v in obj:
            r = _find_message_id(v, _depth + 1)
            if r:
                return r
    return None


def _find_timestamp_ms(obj: Any, _depth: int = 0) -> int | None:
    """递归查找 transcript 中的毫秒级时间戳。"""
    if _depth > 10:
        return None
    if isinstance(obj, dict):
        value = obj.get("timestamp")
        if isinstance(value, (int, float)) and value > 0:
            return int(value)
        provider = obj.get("providerData")
        if isinstance(provider, dict):
            value = provider.get("timestamp")
            if isinstance(value, (int, float)) and value > 0:
                return int(value)
        for v in obj.values():
            r = _find_timestamp_ms(v, _depth + 1)
            if r:
                return r
    elif isinstance(obj, list):
        for v in obj:
            r = _find_timestamp_ms(v, _depth + 1)
            if r:
                return r
    return None


def _find_tool_records(obj: Any) -> list[dict[str, Any]]:
    """从 transcript 记录里提取 tool 调用与 tool 结果片段。"""
    out: list[dict[str, Any]] = []
    if not isinstance(obj, dict):
        return out

    rec_type = str(obj.get("type") or "").strip().lower()
    provider = obj.get("providerData") if isinstance(obj.get("providerData"), dict) else {}
    if rec_type in {"function_call", "function_call_result"}:
        tool_name = str(obj.get("name") or "").strip()
        if tool_name:
            record = {
                "tool": tool_name,
                "call_id": str(obj.get("callId") or "").strip() or None,
                "kind": rec_type,
            }
            if rec_type == "function_call":
                arguments = obj.get("arguments")
                if isinstance(arguments, str) and arguments.strip():
                    record["arguments"] = arguments
                arguments_display_text = provider.get("argumentsDisplayText")
                if isinstance(arguments_display_text, str) and arguments_display_text.strip():
                    record["arguments_display_text"] = arguments_display_text.strip()
            if rec_type == "function_call_result":
                tool_result = provider.get("toolResult")
                if isinstance(tool_result, dict):
                    content = tool_result.get("content")
                    if content is not None:
                        record["result_content"] = content
                    raw_response = tool_result.get("rawResponse")
                    if isinstance(raw_response, dict):
                        record["raw_response"] = raw_response
                output = obj.get("output")
                if isinstance(output, dict):
                    output_text = output.get("text")
                    if output_text is not None:
                        record["output_text"] = output_text
            out.append(record)

    content = obj.get("content")
    if isinstance(content, list):
        for item in content:
            if not isinstance(item, dict):
                continue
            if str(item.get("type") or "").strip().lower() != "tool_use":
                continue
            tool_name = str(item.get("name") or "").strip()
            if not tool_name:
                continue
            out.append({
                "tool": tool_name,
                "call_id": str(item.get("callId") or "").strip() or None,
                "kind": "tool_use",
            })

    return out
