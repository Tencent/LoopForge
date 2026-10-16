from __future__ import annotations
"""AgentLens trace 拓扑层。

这一层负责“trace 结构本身”：
- 创建或派生 carrier
- 生成 agent / step / assistant / tool span
- 把后续聚合需要的 span 元数据写回 state
"""

import json
import secrets
from pathlib import Path
from typing import Any

from .. import state as st
from .bootstrap import AgentLensConfig, debug_write_span
from .normalize import association_properties, collect_tool_calls_for_message, sanitize_payload


def annotate(runtime: dict[str, Any], *, span: Any | None = None, input_data: Any = None, output_data: Any = None, tags: dict[str, Any] | None = None) -> None:
    """在调用 zhiyan annotate 前，先统一清洗 payload。"""
    _ = span
    runtime["Zhiyanllm"].annotate(
        input_data=sanitize_payload(input_data),
        output_data=sanitize_payload(output_data),
        tags=sanitize_payload(tags) if tags else None,
    )


def traceparent_parts(carrier: dict[str, str] | None) -> dict[str, str]:
    """把 W3C `traceparent` 拆成调试日志里常用的几个字段。"""
    traceparent = str((carrier or {}).get("traceparent") or "").strip()
    parts = traceparent.split("-")
    if len(parts) >= 4:
        return {
            "traceparent": traceparent,
            "trace_id": parts[1],
            "span_id": parts[2],
            "flags": parts[3],
        }
    return {"traceparent": traceparent}


def span_context_ids(span: Any) -> dict[str, str]:
    """从 OTel span 对象中提取十六进制的 trace/span id。"""
    try:
        ctx = span.get_span_context()
        return {
            "trace_id": f"{int(ctx.trace_id):032x}",
            "span_id": f"{int(ctx.span_id):016x}",
        }
    except Exception:
        return {}


def ensure_traceparent(carrier: dict[str, str]) -> None:
    """当上游没有成功注入时，补一个最小可用的 `traceparent`。"""
    if str(carrier.get("traceparent") or "").strip():
        return
    trace_id = secrets.token_hex(16)
    parent_id = secrets.token_hex(8)
    carrier["traceparent"] = f"00-{trace_id}-{parent_id}-01"


def carrier_from_span(parent_carrier: dict[str, str], span: Any) -> dict[str, str]:
    """基于父 carrier 和当前 span 生成一个仍在同一 trace 上的子 carrier。"""
    try:
        span_ctx = span.get_span_context()
        trace_id = f"{int(span_ctx.trace_id):032x}"
        span_id = f"{int(span_ctx.span_id):016x}"
    except Exception:
        return {}
    parent_tp = str((parent_carrier or {}).get("traceparent") or "").strip()
    flags = "01"
    if parent_tp:
        parts = parent_tp.split("-")
        if len(parts) >= 4 and parts[3]:
            flags = parts[3]
    child_carrier: dict[str, str] = dict(parent_carrier or {})
    child_carrier["traceparent"] = f"00-{trace_id}-{span_id}-{flags}"
    return child_carrier


def carrier_from_task_span(parent_carrier: dict[str, str], task_span: Any) -> dict[str, str]:
    """兼容包装过的 task span 和原始 OTel span 两种形态。"""
    span = getattr(task_span, "_span", None)
    if span is None and hasattr(task_span, "get_span_context"):
        span = task_span
    if span is None:
        return {}
    return carrier_from_span(parent_carrier, span)


def zhiyan_attr(runtime: dict[str, Any], name: str, fallback: str) -> str:
    """防御式读取 zhiyan 语义约定常量。"""
    return str(getattr(runtime.get("ZhiyanSpanAttributes"), name, fallback))


def generate_turn_carrier(
    runtime: dict[str, Any],
    *,
    config: AgentLensConfig,
    sid: str,
    turn_id: str,
    state_path: Path | None = None,
) -> dict[str, str]:
    """为一个用户 turn 生成根 carrier。"""
    from opentelemetry import trace

    carrier: dict[str, str] = {}
    span = trace.get_tracer(__name__).start_span(f"turn.{sid}.{turn_id}")
    try:
        runtime["Zhiyanllm"].set_association_properties(association_properties(config, sid, state_path))
        runtime["Zhiyanllm"].inject_context(carrier)
    finally:
        span.end()
    ensure_traceparent(carrier)
    return carrier


def generate_subagent_carrier(runtime: dict[str, Any], *, parent_carrier: dict[str, str], role: str) -> dict[str, str]:
    """在当前 turn 下面创建 `invoke_agent` 子 carrier。"""
    try:
        with runtime["track_task_server_call"]("invoke_agent", carrier=parent_carrier) as task_span:
            annotate(runtime, input_data={"agent": role}, output_data={"result": "dispatched"}, tags={"agent": role})
            child_carrier = carrier_from_task_span(parent_carrier, task_span)
            if child_carrier:
                return child_carrier
    except Exception:
        return {}
    return {}


def generate_step_carrier(
    runtime: dict[str, Any],
    *,
    parent_carrier: dict[str, str],
    agent: str,
    message_id: str,
    transcript_path: str,
) -> dict[str, str]:
    """创建用于归并同一条 message 工作的 synthetic step span。"""
    try:
        with runtime["track_task_server_call"]("react_step", carrier=parent_carrier) as task_span:
            annotate(
                runtime,
                input_data={"agent": agent, "message_id": message_id},
                output_data={"result": "grouped"},
                tags={"message_id": message_id, "agent": agent, "transcript_path": transcript_path or ""},
            )
            child_carrier = carrier_from_task_span(parent_carrier, task_span)
            if child_carrier:
                return child_carrier
    except Exception:
        return {}
    return {}


def generate_agent_carrier(runtime: dict[str, Any], *, parent_carrier: dict[str, str], agent: str) -> dict[str, str]:
    """在当前 turn 下创建稳定的 per-agent 聚合 span。"""
    try:
        with runtime["track_task_server_call"]("react_agent", carrier=parent_carrier) as task_span:
            span = getattr(task_span, "_span", None)
            if span is not None and hasattr(span, "set_attribute"):
                span.set_attribute("agent.name", agent)
            annotate(runtime, input_data={"agent": agent}, output_data={"agent": agent}, tags={"agent": agent})
            child_carrier = carrier_from_task_span(parent_carrier, task_span)
            if child_carrier:
                return child_carrier
    except Exception:
        return {}
    return {}


def resolve_agent_carrier(
    *,
    state_path: Path,
    sid: str,
    runtime: dict[str, Any],
    parent_carrier: dict[str, str],
    agent: str,
) -> dict[str, str]:
    """从共享状态里获取或创建持久化的 agent 聚合 carrier。"""
    normalized_agent = str(agent or "main").strip() or "main"
    existing = st.get_agent_span_carrier(state_path, sid, normalized_agent)
    if isinstance(existing, dict) and existing.get("traceparent"):
        return dict(existing)

    def _factory() -> dict[str, str]:
        return generate_agent_carrier(runtime, parent_carrier=parent_carrier, agent=normalized_agent)

    effective_carrier = st.upsert_agent_span_atomic(state_path, sid, normalized_agent, carrier_factory=_factory)
    if not isinstance(effective_carrier, dict) or not effective_carrier.get("traceparent"):
        return dict(parent_carrier)
    agent_parts = traceparent_parts(effective_carrier)
    parent_parts = traceparent_parts(parent_carrier)
    debug_write_span(
        state_path,
        sid,
        {
            "event": "span_emit",
            "span_kind": "agent",
            "span_name": "react_agent",
            "agent": normalized_agent,
            "trace_id": agent_parts.get("trace_id", parent_parts.get("trace_id", "")),
            "span_id": agent_parts.get("span_id", ""),
            "parent_span_id": parent_parts.get("span_id", ""),
            "parent_traceparent": parent_parts.get("traceparent", ""),
        },
        sanitizer=sanitize_payload,
    )
    return dict(effective_carrier)


def resolve_step_carrier(
    *,
    state_path: Path,
    sid: str,
    runtime: dict[str, Any],
    parent_carrier: dict[str, str],
    agent: str,
    message_id: str | None,
    transcript_path: str,
) -> dict[str, str] | None:
    """获取或创建按 message 分组使用的 step carrier。"""
    normalized_message_id = str(message_id or "").strip()
    if not normalized_message_id:
        return None
    existing = st.get_step_span_carrier(state_path, sid, agent, normalized_message_id)
    if isinstance(existing, dict) and existing.get("traceparent"):
        return dict(existing)

    def _factory() -> dict[str, str]:
        return generate_step_carrier(
            runtime,
            parent_carrier=parent_carrier,
            agent=agent,
            message_id=normalized_message_id,
            transcript_path=transcript_path,
        )

    effective_carrier = st.upsert_step_span_atomic(
        state_path,
        sid,
        agent,
        normalized_message_id,
        carrier_factory=_factory,
        transcript_path=transcript_path,
    )
    if not isinstance(effective_carrier, dict) or not effective_carrier.get("traceparent"):
        return None
    step_parts = traceparent_parts(effective_carrier)
    parent_parts = traceparent_parts(parent_carrier)
    debug_write_span(
        state_path,
        sid,
        {
            "event": "span_emit",
            "span_kind": "step",
            "span_name": "react_step",
            "message_id": normalized_message_id,
            "agent": agent,
            "trace_id": step_parts.get("trace_id", parent_parts.get("trace_id", "")),
            "span_id": step_parts.get("span_id", ""),
            "parent_span_id": parent_parts.get("span_id", ""),
            "parent_traceparent": parent_parts.get("traceparent", ""),
            "transcript_path": transcript_path or "",
        },
        sanitizer=sanitize_payload,
    )
    return dict(effective_carrier)


def set_agent_aggregate_on_span(span: Any, aggregate: dict[str, Any] | None) -> None:
    """把 agent 的累计指标投影成当前 span 的属性。"""
    if span is None or not hasattr(span, "set_attribute") or not isinstance(aggregate, dict):
        return
    tokens = aggregate.get("tokens") or {}
    if isinstance(tokens, dict):
        for key in ("input", "output", "cache_read", "cache_creation", "total"):
            if key in tokens:
                span.set_attribute(f"agent.cumulative_tokens.{key}", int(tokens.get(key) or 0))
    for key in ("cost_usd", "tool_duration_ms", "llm_call_count", "tool_call_count", "step_count", "event_count"):
        if key in aggregate:
            try:
                span.set_attribute(f"agent.cumulative.{key}", int(aggregate.get(key) or 0))
            except (TypeError, ValueError):
                span.set_attribute(f"agent.cumulative.{key}", str(aggregate.get(key)))


def emit_assistant_span(
    runtime: dict[str, Any],
    *,
    carrier: dict[str, str],
    agent: str,
    message_id: str | None,
    assistant_text: str,
    model: str | None = None,
    transcript_path: str | None = None,
    state_path: Path | None = None,
    sid: str | None = None,
) -> None:
    """发出一个轻量 assistant span，并按需补上 tool calls。"""
    if not assistant_text or not assistant_text.strip():
        return
    tool_calls: list[dict[str, Any]] = []
    if message_id and transcript_path:
        tool_calls = collect_tool_calls_for_message(transcript_path, message_id)
    output_data: dict[str, Any] = {"role": "assistant", "content": assistant_text[:8000]}
    if tool_calls:
        output_data["tool_calls"] = tool_calls
    ctx = runtime["TracerWrapper"].extract_context(carrier)
    token = runtime["otel_attach"](ctx)
    try:
        with runtime["get_tracer"]() as tracer:
            with tracer.start_as_current_span(name="assistant_message") as span:
                span.set_attribute(runtime["ZhiyanSpanAttributes"].LLM_SPAN_KIND, runtime["ZhiyanllmSpanKindValues"].LLM.value)
                span_ids = span_context_ids(span)
                parent_parts = traceparent_parts(carrier)
                debug_write_span(
                    state_path,
                    sid,
                    {
                        "event": "span_emit",
                        "span_kind": "assistant",
                        "span_name": "assistant_message",
                        "message_id": str(message_id or ""),
                        "agent": agent,
                        "model": str(model or ""),
                        "tool_call_count": len(tool_calls),
                        "trace_id": span_ids.get("trace_id", parent_parts.get("trace_id", "")),
                        "span_id": span_ids.get("span_id", ""),
                        "parent_span_id": parent_parts.get("span_id", ""),
                        "parent_traceparent": parent_parts.get("traceparent", ""),
                    },
                    sanitizer=sanitize_payload,
                )
                annotate(
                    runtime,
                    input_data={"agent": agent, "message_id": message_id or "", "model": model or ""},
                    output_data=output_data,
                    tags={"message_id": str(message_id or ""), "agent": agent},
                )
    finally:
        runtime["otel_detach"](token)


def emit_tool_span(
    runtime: dict[str, Any],
    *,
    carrier: dict[str, str],
    tool_name: str,
    active_agent: str,
    skills: list[str],
    rules: list[str],
    duration_ms: int | None = None,
    tool_details: dict[str, Any] | None = None,
    message_id: str | None = None,
    step_grouping: str = "fallback",
    state_path: Path | None = None,
    sid: str | None = None,
) -> None:
    """发出 tool span，并以受控大小挂载 tool payload。"""
    def _truncate(value: Any, limit: int = 1200) -> Any:
        text = value if isinstance(value, str) else None
        if text is None:
            return value
        if len(text) <= limit:
            return text
        return text[:limit] + "...(truncated)"

    def _maybe_parse_json_text(value: Any) -> Any:
        if not isinstance(value, str):
            return value
        text = value.strip()
        if not text or text[0] not in "{[":
            return value
        try:
            return json.loads(text)
        except Exception:
            return value

    def _parse_bash_result_content(value: Any) -> dict[str, Any] | None:
        if not isinstance(value, str) or "Command:" not in value:
            return None
        normalized_value = value.replace("\\n", "\n")
        markers = [
            ("Command:", "command"),
            ("Stdout:", "stdout"),
            ("Stderr:", "stderr"),
            ("Exit Code:", "exit_code"),
            ("Signal:", "signal"),
        ]
        parsed: dict[str, Any] = {}
        for idx, (marker, key) in enumerate(markers):
            start = normalized_value.find(marker)
            if start < 0:
                continue
            start += len(marker)
            end = len(normalized_value)
            for next_marker, _ in markers[idx + 1:]:
                pos = normalized_value.find(next_marker, start)
                if pos >= 0:
                    end = min(end, pos)
            segment = normalized_value[start:end].strip()
            if not segment:
                continue
            parsed[key] = _truncate(segment, 1200 if key in {"command", "stdout", "stderr"} else 200)
        return parsed or None

    tool_input: dict[str, Any] = {"tool": tool_name, "agent": active_agent}
    if skills:
        tool_input["skill"] = ", ".join(skills)
    if rules:
        tool_input["rule"] = ", ".join(rules)
    if message_id:
        tool_input["message_id"] = message_id
    details = dict(tool_details or {})
    call_id = str(details.get("call_id") or "").strip() or None
    if call_id:
        tool_input["call_id"] = call_id
    arguments_display_text = details.get("arguments_display_text")
    if arguments_display_text:
        tool_input["arguments_display_text"] = _truncate(arguments_display_text, 600)
    arguments = details.get("arguments")
    if arguments:
        parsed_arguments = _maybe_parse_json_text(arguments)
        tool_input["arguments"] = _truncate(parsed_arguments, 1200)
        if isinstance(parsed_arguments, dict):
            if parsed_arguments.get("command"):
                tool_input["command"] = _truncate(parsed_arguments.get("command"), 1200)
            if parsed_arguments.get("description"):
                tool_input["description"] = _truncate(parsed_arguments.get("description"), 400)

    tool_output: dict[str, Any] = {"result": "executed"}
    result_content = details.get("result_content")
    if result_content is not None:
        structured_result = _parse_bash_result_content(result_content)
        if structured_result:
            tool_output["result"] = structured_result
        else:
            tool_output["result_content"] = _truncate(result_content, 1600)
    output_text = details.get("output_text")
    if output_text:
        tool_output["output_text"] = _truncate(output_text, 1600)
    raw_response = details.get("raw_response")
    if isinstance(raw_response, dict):
        tool_output["raw_response"] = {
            "exitCode": raw_response.get("exitCode"),
            "signal": raw_response.get("signal"),
            "interrupted": raw_response.get("interrupted"),
            "sandboxDenied": raw_response.get("sandboxDenied"),
            "tool_error_code": raw_response.get("tool_error_code"),
        }

    ctx = runtime["TracerWrapper"].extract_context(carrier)
    token = runtime["otel_attach"](ctx)
    try:
        with runtime["get_tracer"]() as tracer:
            with tracer.start_as_current_span(name=f"{tool_name}.TOOL") as span:
                span_kind_value = runtime["ZhiyanllmSpanKindValues"].TOOL.value
                span.set_attribute(zhiyan_attr(runtime, "LLM_SPAN_KIND", "gen_ai.span.kind"), span_kind_value)
                span.set_attribute("gen_ai.span.kind", span_kind_value)
                span.set_attribute(zhiyan_attr(runtime, "TOOL_NAME", "tool.name"), tool_name)
                if isinstance(tool_details, dict):
                    desc = tool_details.get("description") or tool_details.get("tool_description")
                    if isinstance(desc, str) and desc.strip():
                        span.set_attribute(zhiyan_attr(runtime, "TOOL_DESCRIPTION", "tool.description"), desc.strip())
                tool_params = tool_input.get("arguments")
                if tool_params is not None:
                    if not isinstance(tool_params, str):
                        tool_params = json.dumps(tool_params, ensure_ascii=False, default=str)
                    span.set_attribute(zhiyan_attr(runtime, "TOOL_PARAMETERS", "tool.parameters"), tool_params)
                if duration_ms is not None:
                    span.set_attribute("tool.duration_ms", int(duration_ms))
                span_ids = span_context_ids(span)
                parent_parts = traceparent_parts(carrier)
                debug_write_span(
                    state_path,
                    sid,
                    {
                        "event": "span_emit",
                        "span_kind": "tool",
                        "span_name": f"{tool_name}.TOOL",
                        "tool": tool_name,
                        "message_id": str(message_id or ""),
                        "call_id": call_id or "",
                        "step_grouping": step_grouping,
                        "agent": active_agent,
                        "trace_id": span_ids.get("trace_id", parent_parts.get("trace_id", "")),
                        "span_id": span_ids.get("span_id", ""),
                        "parent_span_id": parent_parts.get("span_id", ""),
                        "parent_traceparent": parent_parts.get("traceparent", ""),
                    },
                    sanitizer=sanitize_payload,
                )
                annotate(
                    runtime,
                    input_data=tool_input,
                    output_data=tool_output,
                    tags={"step_grouping": step_grouping, "message_id": str(message_id or "")},
                )
    finally:
        runtime["otel_detach"](token)
