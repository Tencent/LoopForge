"""AgentLens 运行时编排层。

这一层保留对外暴露的 4 个入口：
- `emit_session_start`
- `emit_turn_start`
- `emit_post_step`
- `emit_session_stop`

它本身不负责底层清洗或 carrier 生成，而是把调用编排到
`bootstrap / normalize / tracing` 三层上。
"""
from __future__ import annotations

from pathlib import Path
from typing import Any

try:
    from opentelemetry import trace as _otel_trace
except Exception:  # pragma: no cover - 可选依赖缺失时优雅降级
    _otel_trace = None

from .. import state as st
from .bootstrap import (
    debug_write_span as _debug_write_span,
    ensure_initialized as _ensure_initialized,
    get_session_context as _get_session_context,
    load_config,
    project_root as _project_root,
    set_failure as _set_failure,
)
from .normalize import (
    association_properties as _association_properties,
    build_llm_io_from_transcript as _build_llm_io_from_transcript,
    find_tool_usage_event as _find_tool_usage_event,
    infer_vendor as _infer_vendor,
    sanitize_payload as _sanitize_payload,
    to_openai_usage as _to_openai_usage,
)
from .tracing import (
    annotate as _annotate,
    emit_assistant_span as _emit_assistant_span,
    emit_tool_span as _emit_tool_span,
    generate_subagent_carrier as _generate_subagent_carrier,
    generate_turn_carrier as _generate_turn_carrier,
    resolve_agent_carrier as _resolve_agent_carrier,
    resolve_step_carrier as _resolve_step_carrier,
    set_agent_aggregate_on_span as _set_agent_aggregate_on_span,
    span_context_ids as _span_context_ids,
    traceparent_parts as _traceparent_parts,
)


def emit_session_start(
    *,
    state_path: Path,
    sid: str,
    cwd: str,
    agent: str = "main",
) -> None:
    """初始化 v2 版本的 `_agentlens` session payload。

    在 turn-centric 模型下，这里**不再**生成 trace_id。
    它只负责记录 session 级元数据（如 `enabled`、`session_id`、`app_name`），
    并清理上一次运行遗留的 carrier / error 状态。
    真正的新 trace 会在第一次 `UserPromptSubmit` 时由 `emit_turn_start` 打开。
    """
    _ = agent
    config = load_config(_project_root(cwd))
    # 注意：这里不要清空 current_turn / turn_history。
    # 同一个 sid 下 SessionStart 可能多次触发（例如 subagent 启动、IDE 刷新、
    # workspace 切换）。如果这里清空，会把进行中的 turn 擦掉，后续 PostToolUse
    # 会全部看到 no_active_turn，最终把这一轮观测链路打断。
    base_updates: dict[str, Any] = {
        "session_id": sid,
        "app_name": config.app_name,
    }
    if not config.enabled:
        base_updates["enabled"] = False
        st.update_agentlens_session(
            state_path,
            sid,
            base_updates,
            clear_keys=["carrier", "last_error"],
        )
        return

    try:
        runtime = _ensure_initialized(config)
        if runtime is None:
            _set_failure(state_path, sid, "zhiyanllm runtime unavailable")
            return
        base_updates["enabled"] = True
        st.update_agentlens_session(
            state_path,
            sid,
            base_updates,
            clear_keys=["carrier", "last_error"],
        )
    except Exception as exc:
        _set_failure(state_path, sid, str(exc))


def emit_turn_start(
    *,
    state_path: Path,
    sid: str,
    cwd: str,
    turn_id: str,
    prompt_meta: dict[str, Any] | None = None,
    business_scenario: str | None = None,
) -> None:
    """打开一个新的 turn，生成新的 trace_id 并写入 `_agentlens.current_turn`。

    这是当前实现里**唯一**允许生成新 trace_id 的地方。
    `emit_post_step` / `emit_session_stop` 都不会重新造 carrier；
    找不到时只会优雅降级，不会私自开新链路。
    """
    _ = prompt_meta
    config = load_config(_project_root(cwd))
    if not config.enabled:
        return

    try:
        runtime = _ensure_initialized(config)
        if runtime is None:
            _set_failure(state_path, sid, "zhiyanllm runtime unavailable")
            return
        # 先把 per-turn business scenario 写进 state，
        # 这样 `_generate_turn_carrier -> _association_properties`
        # 才能在生成 trace 时把这次 turn 的业务场景烘焙进去。
        if business_scenario:
            st.update_agentlens_session(
                state_path,
                sid,
                {"enabled": True, "session_id": sid, "app_name": config.app_name, "_turn_business_scenario": business_scenario},
                clear_keys=["last_error"],
            )
        else:
            st.update_agentlens_session(
                state_path,
                sid,
                {"enabled": True, "session_id": sid, "app_name": config.app_name},
                clear_keys=["last_error", "_turn_business_scenario"],
            )
        carrier = _generate_turn_carrier(runtime, config=config, sid=sid, turn_id=turn_id, state_path=state_path)
        st.begin_turn(state_path, sid, turn_id, carrier)
    except Exception as exc:
        _set_failure(state_path, sid, str(exc))


def emit_post_step(
    *,
    state_path: Path,
    sid: str,
    tool_event: dict[str, Any] | None,
    usage_events: list[dict[str, Any]],
) -> None:
    """发出扁平化的 LLM span，只保留最小 TASK 包装层来传播 carrier 上下文。

    面向 turn 的 v2 约束：
    - 活跃的 ``current_turn.carrier`` 是唯一的 trace 上下文来源。
    - 如果不存在 ``current_turn``（例如 UserPromptSubmit hook 还没触发），
      这里只会记录 ``last_error`` 并返回，不会私自生成新的 trace_id。
    - subagent（``active_agent != "main"``）会拿到当前 turn 下的
      ``invoke_agent`` 子 span，后续 chat span 都挂到这个子 span 下，
      以保持因果链路连续。
    """
    if not isinstance(tool_event, dict) and not usage_events:
        return

    cwd_hint = ""
    if isinstance(tool_event, dict):
        cwd_hint = str(tool_event.get("cwd") or "")
    config = load_config(_project_root(cwd_hint))
    if not config.enabled:
        return

    session_ctx = _get_session_context(state_path, sid)
    # 快路径：尊重 SessionStart 阶段已经写入的 enabled 标记。
    if session_ctx.get("enabled") is False:
        return

    current_turn = session_ctx.get("current_turn")
    if not isinstance(current_turn, dict):
        _set_failure(state_path, sid, "no_active_turn")
        return
    parent_carrier = current_turn.get("carrier")
    if not isinstance(parent_carrier, dict) or not parent_carrier.get("traceparent"):
        _set_failure(state_path, sid, "no_active_turn_carrier")
        return

    try:
        runtime = _ensure_initialized(config)
        if runtime is None:
            _set_failure(state_path, sid, "zhiyanllm runtime unavailable")
            return

        tool_name = str((tool_event or {}).get("tool") or "unknown_tool")
        active_agent = str((tool_event or {}).get("agent") or "main")
        transcript_path = str((tool_event or {}).get("transcript_path") or "")
        skills = (tool_event or {}).get("skill") or []
        rules = (tool_event or {}).get("rule") or []

        # 解析这次事件真正要用的 carrier。
        # main agent 直接使用 turn 根 carrier；subagent 则使用按角色拆分的
        # 子 span carrier，并在当前 turn 下做幂等注册。
        carrier = dict(parent_carrier)
        if active_agent and active_agent != "main":
            existing = None
            subs = current_turn.get("subagent_spans")
            if isinstance(subs, dict):
                rec = subs.get(active_agent)
                if isinstance(rec, dict) and isinstance(rec.get("carrier"), dict):
                    existing = rec["carrier"]
            if isinstance(existing, dict) and existing.get("traceparent"):
                carrier = dict(existing)
            else:
                # 原子化的 get-or-create：只有确认 subagent span 不存在时，
                # 才会在写锁内部调用 carrier_factory。
                # 这样可以避免并发 hook 进程之间的 TOCTOU 竞争，产生孤儿
                # invoke_agent.TASK span。
                def _factory() -> dict[str, str]:
                    return _generate_subagent_carrier(
                        runtime, parent_carrier=parent_carrier, role=active_agent
                    )

                registered = st.upsert_subagent_span_atomic(
                    state_path, sid, active_agent, carrier_factory=_factory
                )
                if isinstance(registered, dict) and registered.get("traceparent"):
                    carrier = dict(registered)
        runtime["Zhiyanllm"].set_association_properties(_association_properties(config, sid, state_path))
        # 静默挂载 carrier 上下文（不额外创建 TASK span），让内部 span
        # 继承正确的 trace_id / parent_id。
        _ctx = runtime["TracerWrapper"].extract_context(carrier)
        _token = runtime["otel_attach"](_ctx)
        try:
            preferred_tool_message_id = str((tool_event or {}).get("message_id") or "").strip() or None
            _assistant_emitted_mids: set[str] = set()
            pre_bumped_steps: set[tuple[str, str]] = set()
            committed_steps: set[tuple[str, str]] = set()

            if isinstance(tool_event, dict):
                if preferred_tool_message_id and st.get_step_span_carrier(
                    state_path, sid, active_agent, preferred_tool_message_id
                ) is None:
                    pre_bumped_steps.add((active_agent, preferred_tool_message_id))
            for usage_event in usage_events:
                event_agent = str(usage_event.get("agent") or active_agent or "main")
                event_message_id = str(usage_event.get("message_id") or "").strip() or None
                if event_message_id:
                    step_key = (event_agent, event_message_id)
                    if (
                        step_key not in pre_bumped_steps
                        and st.get_step_span_carrier(state_path, sid, event_agent, event_message_id) is None
                    ):
                        pre_bumped_steps.add(step_key)
            if not usage_events and isinstance(tool_event, dict):
                tool_parent_carrier = _resolve_agent_carrier(
                    state_path=state_path,
                    sid=sid,
                    runtime=runtime,
                    parent_carrier=carrier,
                    agent=active_agent,
                )
                if preferred_tool_message_id:
                    step_carrier = _resolve_step_carrier(
                        state_path=state_path,
                        sid=sid,
                        runtime=runtime,
                        parent_carrier=tool_parent_carrier,
                        agent=active_agent,
                        message_id=preferred_tool_message_id,
                        transcript_path=transcript_path,
                    )
                    if isinstance(step_carrier, dict) and step_carrier.get("traceparent"):
                        tool_parent_carrier = dict(step_carrier)
                _emit_tool_span(
                    runtime,
                    carrier=tool_parent_carrier,
                    tool_name=tool_name,
                    active_agent=active_agent,
                    skills=skills,
                    rules=rules,
                    duration_ms=tool_event.get("ms") if isinstance(tool_event.get("ms"), (int, float)) else None,
                    tool_details=tool_event.get("tool_details") if isinstance(tool_event.get("tool_details"), dict) else None,
                    message_id=preferred_tool_message_id,
                    step_grouping="message_id" if preferred_tool_message_id else "fallback",
                    state_path=state_path,
                    sid=sid,
                )
                st.bump_agent_aggregate(
                    state_path,
                    sid,
                    active_agent,
                    tool_event=tool_event,
                )
                if (
                    preferred_tool_message_id
                    and (active_agent, preferred_tool_message_id) in pre_bumped_steps
                    and (active_agent, preferred_tool_message_id) not in committed_steps
                ):
                    st.bump_agent_aggregate(
                        state_path,
                        sid,
                        active_agent,
                        step_created=True,
                    )
                    committed_steps.add((active_agent, preferred_tool_message_id))
                st.update_agentlens_session(state_path, sid, {"enabled": True}, clear_keys=["last_error"])
                return

            matched_usage = _find_tool_usage_event(tool_event, usage_events) if isinstance(tool_event, dict) else None
            tool_message_id = preferred_tool_message_id or str((matched_usage or {}).get("message_id") or "").strip() or None
            if isinstance(tool_event, dict):
                # 先构造 assistant 预览，再打开 agent/step 父 span。
                # 这样即便 transcript 重建失败，也不会留下空的分组 TASK。
                _assistant_preview_content: str | None = None
                _assistant_preview_transcript = transcript_path
                _assistant_preview_model = ""
                if tool_message_id:
                    for _ue in usage_events:
                        if str(_ue.get("message_id") or "").strip() != tool_message_id:
                            continue
                        _ue_transcript = str(_ue.get("transcript_path") or transcript_path)
                        _ue_offset = int(_ue.get("source_offset") or 0)
                        _, _llm_out_pre, _ = _build_llm_io_from_transcript(
                            transcript_path=_ue_transcript,
                            source_offset=_ue_offset,
                            tokens=_ue.get("tokens"),
                            model=str(_ue.get("model") or ""),
                        )
                        _pre_content = ""
                        if _llm_out_pre.get("choices"):
                            _pre_msg = _llm_out_pre["choices"][0].get("message") or {}
                            _pre_content = str(_pre_msg.get("content") or "")
                        if _pre_content and _pre_content.strip():
                            _assistant_preview_content = _pre_content
                            _assistant_preview_transcript = _ue_transcript
                            _assistant_preview_model = str(_ue.get("model") or "")
                        break

                tool_parent_carrier = _resolve_agent_carrier(
                    state_path=state_path,
                    sid=sid,
                    runtime=runtime,
                    parent_carrier=carrier,
                    agent=active_agent,
                )
                if tool_message_id:
                    step_carrier = _resolve_step_carrier(
                        state_path=state_path,
                        sid=sid,
                        runtime=runtime,
                        parent_carrier=tool_parent_carrier,
                        agent=active_agent,
                        message_id=tool_message_id,
                        transcript_path=transcript_path,
                    )
                    if isinstance(step_carrier, dict) and step_carrier.get("traceparent"):
                        tool_parent_carrier = dict(step_carrier)
                if _assistant_preview_content and tool_message_id:
                    _emit_assistant_span(
                        runtime,
                        carrier=tool_parent_carrier,
                        agent=active_agent,
                        message_id=tool_message_id,
                        assistant_text=_assistant_preview_content,
                        model=_assistant_preview_model,
                        transcript_path=_assistant_preview_transcript,
                        state_path=state_path,
                        sid=sid,
                    )
                    _assistant_emitted_mids.add(tool_message_id)

                _emit_tool_span(
                    runtime,
                    carrier=tool_parent_carrier,
                    tool_name=tool_name,
                    active_agent=active_agent,
                    skills=skills,
                    rules=rules,
                    duration_ms=tool_event.get("ms") if isinstance(tool_event.get("ms"), (int, float)) else None,
                    tool_details=tool_event.get("tool_details") if isinstance(tool_event.get("tool_details"), dict) else None,
                    message_id=tool_message_id,
                    step_grouping="message_id" if tool_message_id else "fallback",
                    state_path=state_path,
                    sid=sid,
                )
                st.bump_agent_aggregate(
                    state_path,
                    sid,
                    active_agent,
                    tool_event=tool_event,
                )
                if tool_message_id and (active_agent, tool_message_id) in pre_bumped_steps:
                    step_key = (active_agent, tool_message_id)
                    if step_key not in committed_steps:
                        st.bump_agent_aggregate(
                            state_path,
                            sid,
                            active_agent,
                            step_created=True,
                        )
                        committed_steps.add(step_key)

            _prev_llm_offsets: dict[str, int] = {}
            for usage_event in usage_events:
                model = str(usage_event.get("model") or "")
                ev_transcript = str(usage_event.get("transcript_path") or transcript_path)
                ev_offset = int(usage_event.get("source_offset") or 0)
                raw_tokens = usage_event.get("tokens") or {}
                event_message_id = str(usage_event.get("message_id") or "").strip() or None
                event_agent = str(usage_event.get("agent") or active_agent or "main")
                start_offset = _prev_llm_offsets.get(ev_transcript) or st.get_last_llm_offset(
                    st.load_state(state_path), sid, ev_transcript
                )
                llm_input, llm_output, _turn_title = _build_llm_io_from_transcript(
                    transcript_path=ev_transcript,
                    source_offset=ev_offset,
                    start_offset=int(start_offset or 0),
                    current_message_id=event_message_id,
                    tokens=raw_tokens,
                    model=model or None,
                )
                _prev_llm_offsets[ev_transcript] = ev_offset
                # 原子写回 state，供跨进程增量跟踪复用
                def _update_llm_offset(state: dict[str, Any], _ev=ev_transcript, _off=ev_offset) -> None:
                    st.set_last_llm_offset(state, sid, _off, _ev)
                st.update_state_locked(state_path, _update_llm_offset)
                # --- 把 skill/rule/agent 作为独立消息标签注入 input_data ---
                _meta_messages: list[dict[str, str]] = []
                _ev_agent = str(usage_event.get("agent") or active_agent)
                if _ev_agent and _ev_agent != "main":
                    _meta_messages.append({"role": "agent", "content": _ev_agent})
                if skills:
                    _meta_messages.append({"role": "skill", "content": ", ".join(skills)})
                if rules:
                    _meta_messages.append({"role": "rule", "content": ", ".join(rules)})
                if _meta_messages:
                    llm_input.setdefault("messages", [])
                    for msg in reversed(_meta_messages):
                        llm_input["messages"].insert(0, msg)
                # --- 构造 usage ---
                llm_output["usage"] = _to_openai_usage(raw_tokens)
                # --- 只附带成本细节来构造 output 内容 ---
                _cost_info = {
                    "input_tokens": raw_tokens.get("input"),
                    "output_tokens": raw_tokens.get("output"),
                    "cache_read": raw_tokens.get("cache_read"),
                    "cache_creation": raw_tokens.get("cache_creation"),
                    "total_tokens": raw_tokens.get("total"),
                    "cost_usd": usage_event.get("cost_usd"),
                }
                _cost_info = {k: v for k, v in _cost_info.items() if v is not None}
                _existing_content = ""
                if llm_output.get("choices"):
                    _msg = llm_output["choices"][0].get("message") or {}
                    _existing_content = str(_msg.get("content") or "")
                _output_content = _existing_content
                if _cost_info:
                    _cost_line = " | ".join(f"{k}: {v}" for k, v in _cost_info.items())
                    _output_content = f"[{_cost_line}]\n{_existing_content}" if _existing_content else f"[{_cost_line}]"
                llm_output["choices"] = [{
                    "index": 0,
                    "finish_reason": "stop",
                    "message": {"role": "assistant", "content": _output_content},
                }]
                event_parent_carrier = carrier
                if event_message_id:
                    event_parent_carrier = _resolve_agent_carrier(
                        state_path=state_path,
                        sid=sid,
                        runtime=runtime,
                        parent_carrier=carrier,
                        agent=event_agent,
                    )
                    step_carrier = _resolve_step_carrier(
                        state_path=state_path,
                        sid=sid,
                        runtime=runtime,
                        parent_carrier=event_parent_carrier,
                        agent=event_agent,
                        message_id=event_message_id,
                        transcript_path=ev_transcript,
                    )
                    if isinstance(step_carrier, dict) and step_carrier.get("traceparent"):
                        event_parent_carrier = dict(step_carrier)
                ev_ctx = runtime["TracerWrapper"].extract_context(event_parent_carrier)
                ev_token = runtime["otel_attach"](ev_ctx)
                # 先累计聚合值，再发 LLM span，这样 span 上带的是最新累计属性。
                st.bump_agent_aggregate(
                    state_path,
                    sid,
                    event_agent,
                    usage_event=usage_event,
                )
                _current_agg = st.get_subagent_aggregate(state_path, sid, event_agent)
                try:
                    with runtime["track_llm_call"](_infer_vendor(model), "model_request") as llm_span:
                        # track_llm_call 已经设置了 gen_ai.span.kind/system/operation.name。
                        # 下方 _annotate 会通过 handle_llm_response 设置 gen_ai.usage.*。
                        # LLMSpan 包装层没有 set_attribute，所以这里改用原生 span 写 ID。
                        _native_span = _otel_trace.get_current_span()
                        _set_agent_aggregate_on_span(_native_span, _current_agg)
                        span_ids = _span_context_ids(_native_span)
                        parent_parts = _traceparent_parts(event_parent_carrier)
                        _debug_write_span(
                            state_path,
                            sid,
                            {
                                "event": "span_emit",
                                "span_kind": "llm",
                                "span_name": "model_request",
                                "model": model,
                                "message_id": str(event_message_id or ""),
                                "step_grouping": "message_id" if event_message_id else "fallback",
                                "agent": event_agent,
                                "trace_id": span_ids.get("trace_id", parent_parts.get("trace_id", "")),
                                "span_id": span_ids.get("span_id", ""),
                                "parent_span_id": parent_parts.get("span_id", ""),
                                "parent_traceparent": parent_parts.get("traceparent", ""),
                                "transcript_path": ev_transcript,
                                "source_offset": ev_offset,
                            },
                            sanitizer=_sanitize_payload,
                        )
                        _annotate(
                            runtime,
                            span=llm_span,
                            input_data=llm_input,
                            output_data=llm_output,
                            tags={
                                "cost_usd": str(usage_event.get("cost_usd") or "-"),
                                "input_tokens": str(raw_tokens.get("input", "-")),
                                "output_tokens": str(raw_tokens.get("output", "-")),
                                "cache_read": str(raw_tokens.get("cache_read", "-")),
                                "cache_creation": str(raw_tokens.get("cache_creation", "-")),
                                "total_tokens": str(raw_tokens.get("total", "-")),
                                "message_id": str(event_message_id or ""),
                                "step_grouping": "message_id" if event_message_id else "fallback",
                            },
                        )
                finally:
                    runtime["otel_detach"](ev_token)
                if event_message_id and (event_agent, event_message_id) in pre_bumped_steps:
                    step_key = (event_agent, event_message_id)
                    if step_key not in committed_steps:
                        st.bump_agent_aggregate(
                            state_path,
                            sid,
                            event_agent,
                            step_created=True,
                        )
                        committed_steps.add(step_key)
                if _existing_content and _existing_content.strip() and event_message_id not in _assistant_emitted_mids:
                    _emit_assistant_span(
                        runtime,
                        carrier=event_parent_carrier,
                        agent=event_agent,
                        message_id=event_message_id,
                        assistant_text=_existing_content,
                        model=model,
                        transcript_path=ev_transcript,
                        state_path=state_path,
                        sid=sid,
                    )
        finally:
            runtime["otel_detach"](_token)
        st.update_agentlens_session(state_path, sid, {"enabled": True}, clear_keys=["last_error"])
    except Exception as exc:
        _set_failure(state_path, sid, str(exc))


def emit_session_stop(
    *,
    state_path: Path,
    sid: str,
    cwd: str,
    active_agent: str,
    transcript_path: str,
    stop_events: list[dict[str, Any]],
) -> None:
    """在 session stop 阶段发出扁平化 LLM span，只保留最小 TASK 包装层传播上下文。"""
    config = load_config(_project_root(cwd))
    if not config.enabled:
        return

    session_ctx = _get_session_context(state_path, sid)
    if session_ctx.get("enabled") is False:
        return

    current_turn = session_ctx.get("current_turn")
    if not isinstance(current_turn, dict):
        _set_failure(state_path, sid, "no_active_turn")
        return
    carrier = current_turn.get("carrier")
    if not isinstance(carrier, dict) or not carrier.get("traceparent"):
        _set_failure(state_path, sid, "no_active_turn_carrier")
        return

    try:
        runtime = _ensure_initialized(config)
        if runtime is None:
            _set_failure(state_path, sid, "zhiyanllm runtime unavailable")
            return

        runtime["Zhiyanllm"].set_association_properties(_association_properties(config, sid, state_path))
        if not stop_events:
            return
        # 静默挂载 carrier 上下文（不额外创建 TASK span）。
        _ctx = runtime["TracerWrapper"].extract_context(carrier)
        _token = runtime["otel_attach"](_ctx)
        try:
            _prev_stop_offsets: dict[str, int] = {}
            for stop_event in stop_events:
                model = str(stop_event.get("model") or "")
                ev_transcript = str(stop_event.get("transcript_path") or transcript_path)
                ev_offset = int(stop_event.get("source_offset") or 0)
                raw_tokens = stop_event.get("tokens") or {}
                event_message_id = str(stop_event.get("message_id") or "").strip() or None
                event_agent = str(stop_event.get("agent") or active_agent or "main")
                start_offset = _prev_stop_offsets.get(ev_transcript) or st.get_last_llm_offset(
                    st.load_state(state_path), sid, ev_transcript
                )
                llm_input, llm_output, _turn_title = _build_llm_io_from_transcript(
                    transcript_path=ev_transcript,
                    source_offset=ev_offset,
                    start_offset=int(start_offset or 0),
                    current_message_id=event_message_id,
                    tokens=raw_tokens,
                    model=model or None,
                )
                _prev_stop_offsets[ev_transcript] = ev_offset
                # 原子写回 state，供跨进程增量跟踪复用
                def _update_llm_offset_stop(state: dict[str, Any], _ev=ev_transcript, _off=ev_offset) -> None:
                    st.set_last_llm_offset(state, sid, _off, _ev)
                st.update_state_locked(state_path, _update_llm_offset_stop)
                # --- 把 agent 作为独立消息标签注入 input_data ---
                _ev_agent = str(stop_event.get("agent") or active_agent)
                if _ev_agent and _ev_agent != "main":
                    llm_input.setdefault("messages", []).insert(0, {"role": "agent", "content": _ev_agent})
                # --- 构造 usage ---
                llm_output["usage"] = _to_openai_usage(raw_tokens)
                # --- 构造带上下文和成本细节的 output 内容 ---
                _cost_info = {
                    "input_tokens": raw_tokens.get("input"),
                    "output_tokens": raw_tokens.get("output"),
                    "cache_read": raw_tokens.get("cache_read"),
                    "cache_creation": raw_tokens.get("cache_creation"),
                    "total_tokens": raw_tokens.get("total"),
                    "cost_usd": stop_event.get("cost_usd"),
                    "cost_session_usd": stop_event.get("cost_session_usd"),
                }
                _cost_info = {k: v for k, v in _cost_info.items() if v is not None}
                _existing_content = ""
                if llm_output.get("choices"):
                    _msg = llm_output["choices"][0].get("message") or {}
                    _existing_content = str(_msg.get("content") or "")
                _output_content = _existing_content
                if _cost_info:
                    _cost_line = " | ".join(f"{k}: {v}" for k, v in _cost_info.items())
                    _output_content = f"[{_cost_line}]\n{_existing_content}" if _existing_content else f"[{_cost_line}]"
                llm_output["choices"] = [{
                    "index": 0,
                    "finish_reason": "stop",
                    "message": {"role": "assistant", "content": _output_content},
                }]
                event_parent_carrier = carrier
                # 先解析 agent carrier，让 step 挂在 agent 的 react_agent span 下，
                # 而不是直接挂在 turn 根 span 下。
                if event_agent and event_agent != "main":
                    agent_carrier = _resolve_agent_carrier(
                        state_path=state_path,
                        sid=sid,
                        runtime=runtime,
                        parent_carrier=carrier,
                        agent=event_agent,
                    )
                    if isinstance(agent_carrier, dict) and agent_carrier.get("traceparent"):
                        event_parent_carrier = dict(agent_carrier)
                if event_message_id:
                    step_carrier = _resolve_step_carrier(
                        state_path=state_path,
                        sid=sid,
                        runtime=runtime,
                        parent_carrier=event_parent_carrier,
                        agent=event_agent,
                        message_id=event_message_id,
                        transcript_path=ev_transcript,
                    )
                    if isinstance(step_carrier, dict) and step_carrier.get("traceparent"):
                        event_parent_carrier = dict(step_carrier)
                ev_ctx = runtime["TracerWrapper"].extract_context(event_parent_carrier)
                ev_token = runtime["otel_attach"](ev_ctx)
                # 这里不要再 bump，emit_post_step 已经累计过这条 usage_event。
                # SessionStop 会为同一次调用补发一个重复的 LLM span，再累加就会双算。
                # 因此这里只读取当前聚合值。
                _current_agg = st.get_subagent_aggregate(state_path, sid, event_agent)
                try:
                    with runtime["track_llm_call"](_infer_vendor(model), "model_request") as llm_span:
                        # track_llm_call 已经设置了 gen_ai.span.kind/system/operation.name。
                        # 下方 _annotate 会通过 handle_llm_response 设置 gen_ai.usage.*。
                        # LLMSpan 包装层没有 set_attribute，所以这里改用原生 span 写 ID。
                        _native_span = _otel_trace.get_current_span()
                        _set_agent_aggregate_on_span(_native_span, _current_agg)
                        span_ids = _span_context_ids(_native_span)
                        parent_parts = _traceparent_parts(event_parent_carrier)
                        _debug_write_span(
                            state_path,
                            sid,
                            {
                                "event": "span_emit",
                                "span_kind": "llm",
                                "span_name": "model_request",
                                "model": model,
                                "message_id": str(event_message_id or ""),
                                "step_grouping": "message_id" if event_message_id else "fallback",
                                "agent": event_agent,
                                "trace_id": span_ids.get("trace_id", parent_parts.get("trace_id", "")),
                                "span_id": span_ids.get("span_id", ""),
                                "parent_span_id": parent_parts.get("span_id", ""),
                                "parent_traceparent": parent_parts.get("traceparent", ""),
                                "transcript_path": ev_transcript,
                                "source_offset": ev_offset,
                            },
                            sanitizer=_sanitize_payload,
                        )
                        _annotate(
                            runtime,
                            span=llm_span,
                            input_data=llm_input,
                            output_data=llm_output,
                            tags={
                                "cost_usd": str(stop_event.get("cost_usd") or "-"),
                                "cost_session_usd": str(stop_event.get("cost_session_usd") or "-"),
                                "input_tokens": str(raw_tokens.get("input", "-")),
                                "output_tokens": str(raw_tokens.get("output", "-")),
                                "cache_read": str(raw_tokens.get("cache_read", "-")),
                                "cache_creation": str(raw_tokens.get("cache_creation", "-")),
                                "total_tokens": str(raw_tokens.get("total", "-")),
                                "message_id": str(event_message_id or ""),
                                "step_grouping": "message_id" if event_message_id else "fallback",
                            },
                        )
                finally:
                    runtime["otel_detach"](ev_token)
                if _existing_content and _existing_content.strip():
                    _emit_assistant_span(
                        runtime,
                        carrier=event_parent_carrier,
                        agent=event_agent,
                        message_id=event_message_id,
                        assistant_text=_existing_content,
                        model=model,
                        transcript_path=ev_transcript,
                        state_path=state_path,
                        sid=sid,
                    )
        finally:
            runtime["otel_detach"](_token)
        st.update_agentlens_session(state_path, sid, {"enabled": True}, clear_keys=["last_error"])
    except Exception as exc:
        _set_failure(state_path, sid, str(exc))
