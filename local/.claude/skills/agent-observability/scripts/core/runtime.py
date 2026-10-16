from __future__ import annotations
"""Hook 主编排入口。

这一层负责把外部 hook phase 分发到内部能力：
- session-start / user-prompt-submit / pre / post / stop
- 组装统一运行时路径
- 串联 collector / emitter / agentlens，并承担 transcript 运行时编排
"""

import os
import re
import time
import traceback
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from . import agent_identity, agentlens, collector, devflow, emitter, scanner, state as st


@dataclass(frozen=True)
class RuntimePaths:
    """运行时常用路径集合，避免每个分支重复计算。"""
    base_dir: Path
    state_path: Path
    pending_path: Path
    log_path: Path


def base_dir() -> Path:
    """返回 hook 运行时使用的根目录。"""
    # 运行时产物统一写到 skill 根目录下的 logs/，保持与旧路径兼容。
    return Path(__file__).resolve().parents[2]


def build_runtime_paths() -> RuntimePaths:
    """构造本次 hook 运行要用到的路径集合。"""
    base = base_dir()
    return RuntimePaths(
        base_dir=base,
        state_path=st.get_state_path(base),
        pending_path=st.get_pending_path(base),
        log_path=emitter.get_log_path(base),
    )


def session_id_of(data: dict[str, Any]) -> str:
    """兼容不同字段名，提取 session id。"""
    return str(data.get("session_id") or data.get("sid") or "")


def cwd(data: dict[str, Any]) -> str:
    """解析当前项目工作目录。"""
    return str(data.get("cwd") or os.environ.get("CODEBUDDY_PROJECT_DIR") or os.getcwd())


def normalize_phase(raw: str | None) -> str:
    """把多种 phase 写法折叠成统一内部枚举。"""
    phase = (raw or "").strip().lower()
    return {
        "sessionstart": "session-start",
        "session_start": "session-start",
        "session-start": "session-start",
        "pretooluse": "pre",
        "pre": "pre",
        "posttooluse": "post",
        "post": "post",
        "stop": "stop",
        "userpromptsubmit": "user-prompt-submit",
        "user_prompt_submit": "user-prompt-submit",
        "user-prompt-submit": "user-prompt-submit",
        "prompt": "user-prompt-submit",
    }.get(phase, phase)


def emit_error(phase: str, sid: str, err: Exception) -> None:
    """把运行时异常写成 error 事件，而不是直接中断 hook。"""
    rec = emitter.build_error_event(phase=phase, error=f"{type(err).__name__}: {err}", sid=sid)
    rec["x_traceback"] = traceback.format_exc(limit=5)[-1200:]
    emitter.emit(build_runtime_paths().log_path, rec)


def derive_turn_id(data: dict[str, Any]) -> str:
    """优先复用外部已有 id，否则生成一个本地 turn id。"""
    for key in ("turn_id", "prompt_id", "request_id", "message_id"):
        raw = data.get(key)
        if isinstance(raw, str) and raw.strip():
            return raw.strip()
        if isinstance(raw, (int, float)) and raw:
            return str(raw)
    return f"turn-{int(time.time() * 1000)}"


def prompt_text_length(data: dict[str, Any]) -> int | None:
    """统计用户 prompt 的文本长度，用于 turn 边界指标。"""
    for key in ("prompt", "user_prompt", "message", "content", "text"):
        val = data.get(key)
        if isinstance(val, str):
            return len(val)
        if isinstance(val, list):
            total = 0
            for item in val:
                if isinstance(item, dict):
                    txt = item.get("text")
                    if isinstance(txt, str):
                        total += len(txt)
                elif isinstance(item, str):
                    total += len(item)
            if total:
                return total
    return None


def extract_prompt_text(data: dict[str, Any]) -> str:
    """从 hook payload 中提取可读的 prompt 文本。"""
    for key in ("prompt", "user_prompt", "message", "text"):
        val = data.get(key)
        if isinstance(val, str) and val.strip():
            return val.strip()
        if isinstance(val, list):
            parts = []
            for item in val:
                if isinstance(item, dict):
                    txt = item.get("text")
                    if isinstance(txt, str):
                        parts.append(txt)
                elif isinstance(item, str):
                    parts.append(item)
            if parts:
                return "\n".join(parts).strip()
    return ""


def derive_business_scenario(prompt_text: str) -> str | None:
    """从 prompt 文本中派生一个简短业务场景标识。"""
    if not prompt_text:
        return None

    m = re.search(r"标题[：:]\s*(.+?)(?:\s*描述[：:]|\s*$)", prompt_text, re.DOTALL)
    if m:
        title = m.group(1).strip()[:60]
    else:
        title = prompt_text[:50].strip()

    sanitized = re.sub(r"[^\w\u4e00-\u9fff\-]", "-", title)
    sanitized = re.sub(r"-{2,}", "-", sanitized).strip("-")
    return sanitized if sanitized else None


def handle_session_start(data: dict[str, Any]) -> None:
    """处理 SessionStart：预热 inventory，写 start 事件，初始化 AgentLens。"""
    current_sid = session_id_of(data)
    if not current_sid:
        return
    paths = build_runtime_paths()
    cwd_value = cwd(data)
    skills_meta, rules_meta = scanner.scan_skills_and_rules(cwd_value)
    collector.cache_inventory(paths.state_path, current_sid, skills_meta, rules_meta)
    emitter.emit(paths.log_path, emitter.build_start_event(sid=current_sid, agent="main"))
    agentlens.emit_session_start(state_path=paths.state_path, sid=current_sid, cwd=cwd_value, agent="main")


def handle_user_prompt_submit(data: dict[str, Any]) -> None:
    """处理 UserPromptSubmit：建立 turn 边界，并打开 AgentLens turn。"""
    current_sid = session_id_of(data)
    if not current_sid:
        return
    paths = build_runtime_paths()
    cwd_value = cwd(data)
    turn_id = derive_turn_id(data)
    prompt_len = prompt_text_length(data)
    prompt_text = extract_prompt_text(data)
    business_scenario = derive_business_scenario(prompt_text)

    emitter.emit(
        paths.log_path,
        emitter.build_prompt_submit_event(sid=current_sid, turn_id=turn_id, agent="main", prompt_len=prompt_len),
    )
    agentlens.emit_turn_start(
        state_path=paths.state_path,
        sid=current_sid,
        cwd=cwd_value,
        turn_id=turn_id,
        prompt_meta={"prompt_len": prompt_len} if prompt_len is not None else None,
        business_scenario=business_scenario,
    )


def handle_pre(data: dict[str, Any]) -> None:
    """处理 PreToolUse：仅记录 pending，等待 post 配对。"""
    collector.record_pre(build_runtime_paths().pending_path, data)


def handle_post(data: dict[str, Any]) -> None:
    """处理 PostToolUse：计算耗时、归因 agent、发 tool/usage 事件。"""
    current_sid = session_id_of(data)
    if not current_sid:
        return
    paths = build_runtime_paths()
    cwd_value = cwd(data)
    duration_ms = collector.record_post(paths.pending_path, data)

    skills_meta, rules_meta = collector.load_cached_inventory(paths.state_path, current_sid)
    if not skills_meta and not rules_meta:
        skills_meta, rules_meta = scanner.scan_skills_and_rules(cwd_value)
        collector.cache_inventory(paths.state_path, current_sid, skills_meta, rules_meta)

    active_agent, _ = agent_identity.resolve_active_agent_for_event(
        state_path=paths.state_path,
        sid=current_sid,
        cwd=cwd_value,
        data=data,
    )
    used_skills, used_rules = collector.record_tool_usage(
        state_path=paths.state_path,
        sid=current_sid,
        data=data,
        skills_meta=skills_meta,
        rules_meta=rules_meta,
        active_agent=active_agent,
        collect_skills=True,
    )

    tool_name = str(data.get("tool_name") or "")
    transcript_path = collector.transcript_path(data)
    turn_id = current_turn_id(paths.state_path, current_sid)
    event_ts = time.time()
    tool_call_id = collector.extract_tool_call_id(data)
    dv_ctx = resolve_devflow(paths.state_path, current_sid, cwd_value, paths.log_path)
    task_slug = str(dv_ctx.get("task_slug") or "").strip() if dv_ctx else None
    stage = str(dv_ctx.get("current_stage") or "").strip() if dv_ctx else None
    flush_pending_tool_events(
        state_path=paths.state_path,
        log_path=paths.log_path,
        sid=current_sid,
    )
    tool_context = collector.find_current_tool_context(
        state_path=paths.state_path,
        sid=current_sid,
        transcript_path=transcript_path,
        tool_name=tool_name,
        call_id=tool_call_id,
        event_ts=event_ts,
        claim=True,
    )
    duplicate_tool_context = bool(isinstance(tool_context, dict) and tool_context.get("duplicate"))
    if duplicate_tool_context:
        tool_context = None
    if isinstance(tool_context, dict):
        resolved_transcript_path = str(tool_context.get("transcript_path") or "").strip()
        if resolved_transcript_path:
            transcript_path = resolved_transcript_path
        resolved_agent = str(tool_context.get("agent") or "").strip()
        if resolved_agent:
            active_agent = resolved_agent

    tool_pt_id = str(tool_context.get("pt_id") or "").strip() if isinstance(tool_context, dict) else None
    tool_event = emitter.build_tool_event(
        sid=current_sid,
        tool=tool_name,
        duration_ms=duration_ms,
        active_agent=active_agent,
        transcript_path=transcript_path,
        turn_id=turn_id,
        task_slug=task_slug,
        stage=stage,
        pt_id=tool_pt_id,
    )
    tool_event["ts"] = event_ts
    tool_event["skill"] = used_skills
    tool_event["rule"] = used_rules
    tool_event["cwd"] = cwd_value
    if tool_call_id:
        tool_event["call_id"] = tool_call_id
    if isinstance(tool_context, dict):
        tool_message_id = str(tool_context.get("message_id") or "").strip() or None
        if tool_message_id:
            tool_event["message_id"] = tool_message_id
        tool_details = tool_context.get("tool_details")
        if isinstance(tool_details, dict) and tool_details:
            tool_event["tool_details"] = dict(tool_details)

    usage_events = emit_transcript_events(
        state_path=paths.state_path,
        log_path=paths.log_path,
        sid=current_sid,
        transcript_paths=collector.related_transcript_paths(current_sid, transcript_path),
        current_transcript_path=transcript_path,
        active_agent=active_agent,
        tool_name=tool_name,
        event_kind="usage",
        max_lines=80,
        turn_id=turn_id,
        task_slug=task_slug,
        stage=stage,
    )
    if duplicate_tool_context:
        if not usage_events:
            return
        agentlens.emit_post_step(
            state_path=paths.state_path,
            sid=current_sid,
            tool_event=None,
            usage_events=usage_events,
        )
        return
    if str(tool_event.get("message_id") or "").strip():
        emitter.emit(paths.log_path, tool_event)
        agentlens.emit_post_step(
            state_path=paths.state_path,
            sid=current_sid,
            tool_event=tool_event,
            usage_events=usage_events,
        )
        return

    fallback_usage = collector.find_fallback_usage_event(tool_event, usage_events)
    fallback_usage_mid = str((fallback_usage or {}).get("message_id") or "").strip() or None
    if fallback_usage_mid:
        tool_event["message_id"] = fallback_usage_mid
        emitter.emit(paths.log_path, tool_event)
        agentlens.emit_post_step(
            state_path=paths.state_path,
            sid=current_sid,
            tool_event=tool_event,
            usage_events=usage_events,
        )
        return

    st.append_pending_tool_emit(paths.state_path, current_sid, tool_event)


def handle_stop(data: dict[str, Any]) -> None:
    """处理 Stop：补发 transcript 事件、汇总 session 成本并关闭 AgentLens。"""
    current_sid = session_id_of(data)
    if not current_sid:
        return
    paths = build_runtime_paths()
    cwd_value = cwd(data)
    active_agent, _ = agent_identity.resolve_active_agent_for_event(
        state_path=paths.state_path,
        sid=current_sid,
        cwd=cwd_value,
        data=data,
    )
    transcript_path = collector.transcript_path(data)
    turn_id = current_turn_id(paths.state_path, current_sid)
    dv_ctx = resolve_devflow(paths.state_path, current_sid, cwd_value, paths.log_path)
    task_slug = str(dv_ctx.get("task_slug") or "").strip() if dv_ctx else None
    stage = str(dv_ctx.get("current_stage") or "").strip() if dv_ctx else None
    stop_events = emit_transcript_events(
        state_path=paths.state_path,
        log_path=paths.log_path,
        sid=current_sid,
        transcript_paths=collector.settled_related_transcript_paths(current_sid, transcript_path),
        current_transcript_path=transcript_path,
        active_agent=active_agent,
        tool_name="__stop__",
        event_kind="stop",
        max_lines=200,
        cost_session=emitter.sum_session_cost(paths.log_path, current_sid),
        turn_id=turn_id,
        task_slug=task_slug,
        stage=stage,
    )
    flush_pending_tool_events(
        state_path=paths.state_path,
        log_path=paths.log_path,
        sid=current_sid,
        force_emit_fallback=True,
    )
    agentlens.emit_session_stop(
        state_path=paths.state_path,
        sid=current_sid,
        cwd=cwd_value,
        active_agent=active_agent,
        transcript_path=transcript_path,
        stop_events=stop_events,
    )


def resolve_devflow(state_path: Path, sid: str, cwd_value: str, log_path: Path) -> dict[str, Any] | None:
    """解析 devflow 上下文，并把本次观测到的 stage 变更立即发成 `stage_transition` 事件。

    任何异常都吞掉、返回 None——devflow 感知是可选增强，绝不能让不跑 devflow 的
    项目或 workflow-state.json 格式变化导致 hook 报错。
    """
    try:
        dv_ctx = devflow.resolve_and_diff(state_path, sid, cwd_value)
    except Exception:
        return None
    if not isinstance(dv_ctx, dict):
        return None
    task_slug = str(dv_ctx.get("task_slug") or "").strip()
    if not task_slug:
        return None
    for change in dv_ctx.get("changes") or []:
        if not isinstance(change, dict):
            continue
        event = emitter.build_stage_transition_event(
            sid=sid,
            task_slug=task_slug,
            stage=str(change.get("stage") or ""),
            status=change.get("status"),
            executor=change.get("executor"),
            retry_count=int(change.get("retry_count") or 0),
            review_result=change.get("review_result"),
        )
        emitter.emit(log_path, event)
    return dv_ctx


def current_turn_id(state_path: Path, sid: str) -> str | None:
    """从 state 中读取当前 session 激活中的 turn id。"""
    try:
        turn = st.get_current_turn(state_path, sid)
    except Exception:
        turn = None
    if isinstance(turn, dict):
        raw = turn.get("turn_id")
        if isinstance(raw, str) and raw.strip():
            return raw.strip()
    return None


def flush_pending_tool_events(
    *,
    state_path: Path,
    log_path: Path,
    sid: str,
    force_emit_fallback: bool = False,
) -> None:
    """强制 flush 延迟的 tool 事件，常用于 stop/replay 路径。"""
    pending = st.get_pending_tool_emits(state_path, sid)
    if not pending:
        return

    remaining: list[dict[str, Any]] = []
    for tool_event in pending:
        tool_name = str(tool_event.get("tool") or "").strip()
        transcript_path = str(tool_event.get("transcript_path") or "").strip()
        call_id = str(tool_event.get("call_id") or "").strip() or None
        event_ts = tool_event.get("ts")
        try:
            event_ts_float = float(event_ts) if event_ts is not None else None
        except Exception:
            event_ts_float = None

        tool_context = collector.find_current_tool_context(
            state_path=state_path,
            sid=sid,
            transcript_path=transcript_path,
            tool_name=tool_name,
            call_id=call_id,
            event_ts=event_ts_float,
            claim=True,
        )
        if isinstance(tool_context, dict) and tool_context.get("duplicate"):
            continue
        if isinstance(tool_context, dict):
            resolved_transcript_path = str(tool_context.get("transcript_path") or "").strip()
            if resolved_transcript_path:
                tool_event["transcript_path"] = resolved_transcript_path
            resolved_agent = str(tool_context.get("agent") or "").strip()
            if resolved_agent:
                tool_event["agent"] = resolved_agent
            tool_message_id = str(tool_context.get("message_id") or "").strip() or None
            if tool_message_id:
                tool_event["message_id"] = tool_message_id
            tool_details = tool_context.get("tool_details")
            if isinstance(tool_details, dict) and tool_details:
                tool_event["tool_details"] = dict(tool_details)
            emitter.emit(log_path, tool_event)
            agentlens.emit_post_step(
                state_path=state_path,
                sid=sid,
                tool_event=tool_event,
                usage_events=[],
            )
            continue
        if force_emit_fallback:
            emitter.emit(log_path, tool_event)
            agentlens.emit_post_step(
                state_path=state_path,
                sid=sid,
                tool_event=tool_event,
                usage_events=[],
            )
            continue
        remaining.append(tool_event)

    st.replace_pending_tool_emits(state_path, sid, remaining)


def emit_transcript_events(
    *,
    state_path: Path,
    log_path: Path,
    sid: str,
    transcript_paths: list[str],
    current_transcript_path: str,
    active_agent: str,
    tool_name: str,
    event_kind: str,
    max_lines: int,
    cost_session: float | None = None,
    turn_id: str | None = None,
    task_slug: str | None = None,
    stage: str | None = None,
) -> list[dict[str, Any]]:
    """发出基于 transcript 增量重建得到的 usage/stop 事件。"""
    emitted_events: list[dict[str, Any]] = []
    current_resolved = str(Path(current_transcript_path).resolve()) if current_transcript_path else ""
    for path in transcript_paths:
        scan = collector.collect_transcript_entries(
            state_path=state_path,
            sid=sid,
            transcript_path=path,
            max_lines=max_lines,
        )
        entries = scan.get("entries", []) or []
        event_tool = tool_name if event_kind == "usage" and path == current_resolved else "model_request"
        event_agent = active_agent if path == current_resolved else agent_identity.agent_for_transcript_path(path, active_agent)

        for idx, entry in enumerate(entries):
            event_key = collector.build_transcript_event_key(kind=event_kind, tool=event_tool, entry=entry)
            if not st.claim_transcript_event(state_path, sid, event_key, path):
                continue
            tokens = entry.get("tokens")
            model = entry.get("model")
            if event_kind == "stop":
                event = emitter.build_stop_event(
                    sid=sid,
                    tokens=tokens,
                    model=model,
                    cost_usd=emitter.cost_of(tokens, model),
                    cost_session_usd=cost_session if idx == len(entries) - 1 else None,
                    active_agent=event_agent,
                    transcript_path=path,
                    source_offset=int(entry.get("offset", 0) or 0),
                    turn_id=turn_id,
                    message_id=str(entry.get("message_id") or "").strip() or None,
                    task_slug=task_slug,
                    stage=stage,
                )
            else:
                event = emitter.build_usage_event(
                    sid=sid,
                    tool=event_tool,
                    tokens=tokens,
                    active_agent=event_agent,
                    model=model,
                    cost_usd=emitter.cost_of(tokens, model),
                    transcript_path=path,
                    source_offset=int(entry.get("offset", 0) or 0),
                    turn_id=turn_id,
                    message_id=str(entry.get("message_id") or "").strip() or None,
                    task_slug=task_slug,
                    stage=stage,
                )
            emitter.emit(log_path, event)
            emitted_events.append(event)

        st.commit_transcript_progress(
            state_path,
            sid,
            path,
            offset=int(scan.get("new_offset", 0) or 0),
            last_cumulative_usage=scan.get("last_cumulative"),
        )
    return emitted_events


def main(argv: list[str]) -> int:
    """读取 stdin 输入并按 phase 执行对应 hook 处理分支。"""
    phase = normalize_phase(argv[0] if argv else "")
    current_sid = ""
    try:
        data = collector.read_stdin_json()
        current_sid = session_id_of(data)
        if phase == "session-start":
            handle_session_start(data)
        elif phase == "user-prompt-submit":
            handle_user_prompt_submit(data)
        elif phase == "pre":
            handle_pre(data)
        elif phase == "post":
            handle_post(data)
        elif phase == "stop":
            handle_stop(data)
        else:
            emitter.emit(
                build_runtime_paths().log_path,
                emitter.build_error_event(phase=phase or "unknown", error=f"Unsupported phase: {phase!r}", sid=current_sid),
            )
    except Exception as exc:
        emit_error(phase=phase or "unknown", sid=current_sid, err=exc)
    return 0
