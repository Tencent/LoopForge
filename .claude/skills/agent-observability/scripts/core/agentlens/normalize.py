from __future__ import annotations
"""AgentLens 负载清洗层。

这一层负责“清洗与整形”：
- 在发送或调试前把任意 payload 转成安全的 JSON 结构
- 把本地 token 结构归一化成类似 OpenAI 的 usage 结构
- 从 transcript 中重建轻量的 message / tool-call 上下文
"""

import json
import os
from pathlib import Path
from typing import Any

from .. import state as st
from .bootstrap import AgentLensConfig


def sanitize_payload(value: Any) -> Any:
    """把任意 Python 值转换成适合调试/打点的 JSON 安全结构。"""
    try:
        return json.loads(json.dumps(value, ensure_ascii=False, default=str))
    except Exception:
        return str(value)


def association_properties(config: AgentLensConfig, sid: str, state_path: Path | None = None) -> dict[str, str]:
    """构造 trace 级关联属性，并允许按 turn 覆盖业务场景。"""
    scenario = config.business_scenario
    if state_path:
        try:
            session_ctx = st.load_agentlens_session(state_path, sid)
            turn_scenario = session_ctx.get("_turn_business_scenario")
            if isinstance(turn_scenario, str) and turn_scenario.strip():
                scenario = turn_scenario.strip()
        except Exception:
            pass
    return {
        "session_id": sid,
        "business_scenario": scenario,
        "user": config.user,
    }


def to_openai_usage(tokens: dict[str, Any] | None) -> dict[str, int]:
    """把本地 token 结构转换成 zhiyan 期望的 usage 形状。"""
    if not isinstance(tokens, dict):
        return {}

    def _int(v: Any) -> int:
        try:
            return int(v or 0)
        except Exception:
            return 0

    prompt = _int(tokens.get("input"))
    completion = _int(tokens.get("output"))
    if prompt <= 0 and completion <= 0:
        return {}
    return {"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": prompt + completion}


def infer_vendor(model: str | None) -> str:
    """根据模型名推断一个粗粒度的 provider/vendor 标签。"""
    text = str(model or "").strip().lower()
    if not text:
        return "unknown"
    if "gpt" in text or "openai" in text:
        return "openai"
    if "claude" in text or "anthropic" in text:
        return "anthropic"
    if "gemini" in text or "google" in text:
        return "google"
    if text.startswith("hy") or "hunyuan" in text:
        return "tencent"
    return text.split("/", 1)[0].split("-", 1)[0] or "unknown"


def extract_message_text(content: Any) -> str:
    """把嵌套的 transcript message 内容压平成纯文本。"""
    if content is None:
        return ""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts: list[str] = []
        for item in content:
            if isinstance(item, dict):
                txt = item.get("text")
                if isinstance(txt, str):
                    parts.append(txt)
                elif item.get("type") == "tool_use":
                    parts.append(f"[tool_use {item.get('name', '')}]")
                elif item.get("type") == "tool_result":
                    parts.append(extract_message_text(item.get("content")))
            else:
                parts.append(str(item))
        return "\n".join(p for p in parts if p)
    if isinstance(content, dict):
        return extract_message_text(content.get("text") or content.get("content"))
    return str(content)


def find_message_id_in_record(obj: Any) -> str | None:
    """在 transcript 记录里递归查找稳定的 message 标识。"""
    message_id_keys = ("messageId", "responseId", "requestId")
    if isinstance(obj, dict):
        provider = obj.get("providerData")
        if isinstance(provider, dict):
            for key in message_id_keys:
                value = provider.get(key)
                if isinstance(value, str) and value.strip():
                    return value.strip()
        extra = obj.get("extra")
        if isinstance(extra, dict):
            for key in message_id_keys:
                value = extra.get(key)
                if isinstance(value, str) and value.strip():
                    return value.strip()
        for key in message_id_keys:
            value = obj.get(key)
            if isinstance(value, str) and value.strip():
                return value.strip()
        for value in obj.values():
            found = find_message_id_in_record(value)
            if found:
                return found
    elif isinstance(obj, list):
        for value in obj:
            found = find_message_id_in_record(value)
            if found:
                return found
    return None


def collect_tool_calls_for_message(transcript_path: str, message_id: str) -> list[dict[str, Any]]:
    """为指定 assistant message 重建它挂载的 tool-call 摘要。"""
    if not transcript_path or not message_id or not os.path.isfile(transcript_path):
        return []
    try:
        with open(transcript_path, "rb") as fp:
            blob = fp.read()
    except Exception:
        return []

    tool_calls: list[dict[str, Any]] = []
    for raw_line in blob.splitlines(keepends=True):
        if not raw_line.lstrip().startswith(b"{"):
            continue
        try:
            rec = json.loads(raw_line.decode("utf-8", errors="ignore"))
        except Exception:
            continue
        if not isinstance(rec, dict):
            continue
        if str(rec.get("type") or "").strip().lower() != "function_call":
            continue
        rec_mid = find_message_id_in_record(rec)
        if rec_mid != message_id:
            continue
        call_id = str(rec.get("callId") or "").strip()
        name = str(rec.get("name") or "").strip()
        arguments = rec.get("arguments")
        entry: dict[str, Any] = {"id": call_id, "name": name}
        if arguments is not None:
            entry["arguments"] = arguments if isinstance(arguments, str) else json.dumps(arguments, ensure_ascii=False)
        tool_calls.append(entry)
    return tool_calls


def find_tool_usage_event(tool_event: dict[str, Any], usage_events: list[dict[str, Any]]) -> dict[str, Any] | None:
    """从 usage 列表里挑出最可能属于当前 tool 事件的那一条。"""
    tool_name = str(tool_event.get("tool") or "")
    transcript_path = str(tool_event.get("transcript_path") or "")
    agent = str(tool_event.get("agent") or "main")
    for usage_event in usage_events:
        if not isinstance(usage_event, dict):
            continue
        if str(usage_event.get("agent") or "main") != agent:
            continue
        if transcript_path and str(usage_event.get("transcript_path") or "") != transcript_path:
            continue
        if tool_name and str(usage_event.get("tool") or "") not in {tool_name, "model_request"}:
            continue
        if str(usage_event.get("message_id") or "").strip():
            return usage_event
    for usage_event in usage_events:
        if not isinstance(usage_event, dict):
            continue
        if str(usage_event.get("agent") or "main") != agent:
            continue
        if transcript_path and str(usage_event.get("transcript_path") or "") != transcript_path:
            continue
        return usage_event
    return None


def build_llm_io_from_transcript(
    *,
    transcript_path: str,
    source_offset: int,
    tokens: dict[str, Any] | None,
    model: str | None,
    start_offset: int = 0,
    current_message_id: str | None = None,
) -> tuple[dict[str, Any], dict[str, Any], str]:
    """从 transcript 历史中构造紧凑版的 LLM 输入/输出负载。"""
    input_data: dict[str, Any] = {"model": str(model or "unknown"), "messages": []}
    output_data: dict[str, Any] = {"choices": [], "usage": to_openai_usage(tokens)}
    if not transcript_path or not os.path.isfile(transcript_path):
        return input_data, output_data, ""
    try:
        with open(transcript_path, "rb") as fp:
            fp.seek(0)
            blob = fp.read(source_offset) if source_offset else fp.read()
    except Exception:
        return input_data, output_data, ""

    messages: list[dict[str, Any]] = []
    system_prompts: list[str] = []
    cursor = 0
    last_asst_text: str | None = None

    def _truncate(text: str, limit: int = 4000) -> str:
        if len(text) <= limit:
            return text
        return text[:limit] + "...(truncated)"

    for raw_line in blob.splitlines(keepends=True):
        cursor += len(raw_line)
        if not raw_line.lstrip().startswith(b"{"):
            continue
        try:
            rec = json.loads(raw_line.decode("utf-8", errors="ignore"))
        except Exception:
            continue
        if not isinstance(rec, dict):
            continue

        rec_type = str(rec.get("type") or "").strip().lower()
        is_incremental = cursor > start_offset

        # 重建 prompt 上下文时要跳过“当前正在发出的 assistant message”，
        # 否则同一段回复会同时出现在 input 和 output 两边。
        if current_message_id and is_incremental:
            rec_mid = find_message_id_in_record(rec)
            if rec_mid == current_message_id:
                if rec_type == "message" and rec.get("role") == "assistant":
                    text = extract_message_text(rec.get("content"))
                    if text:
                        last_asst_text = text
                continue

        if rec_type == "message":
            role = rec.get("role")
            text = extract_message_text(rec.get("content"))
            if not text:
                continue
            if role == "system":
                system_prompts.append(text)
            elif is_incremental:
                if role == "user":
                    messages.append({"role": "user", "content": _truncate(text, 8000)})
                    last_asst_text = None
                elif role == "assistant":
                    messages.append({"role": "assistant", "content": _truncate(text, 4000)})
                    last_asst_text = text
        elif is_incremental:
            # 把 function call / result 记录转换成 Chat Completions 风格的
            # assistant/tool message，方便 tracing UI 按对话链路展示。
            if rec_type == "function_call":
                name = str(rec.get("name") or "").strip()
                call_id = str(rec.get("callId") or "").strip()
                arguments = rec.get("arguments", "")
                if isinstance(arguments, dict):
                    arguments = json.dumps(arguments, ensure_ascii=False)
                tool_call: dict[str, Any] = {"type": "function", "function": {"name": name}}
                if name and arguments:
                    tool_call["function"]["arguments"] = _truncate(str(arguments), 2000)
                if call_id:
                    tool_call["id"] = call_id
                if (
                    messages
                    and messages[-1].get("role") == "assistant"
                    and "tool_calls" not in messages[-1]
                    and messages[-1].get("content")
                ):
                    messages[-1]["tool_calls"] = [tool_call]
                else:
                    msg: dict[str, Any] = {"role": "assistant", "content": ""}
                    msg["tool_calls"] = [tool_call]
                    messages.append(msg)
            elif rec_type == "function_call_result":
                call_id = str(rec.get("callId") or "").strip()
                provider = rec.get("providerData", {})
                result_content = ""
                if isinstance(provider, dict):
                    tool_result = provider.get("toolResult", {})
                    if isinstance(tool_result, dict):
                        result_content = extract_message_text(tool_result.get("content"))
                if not result_content:
                    output = rec.get("output")
                    result_content = str(output.get("text", "")) if isinstance(output, dict) else ""
                tool_msg: dict[str, Any] = {"role": "tool", "content": _truncate(result_content, 2000)}
                if call_id:
                    tool_msg["tool_call_id"] = call_id
                messages.append(tool_msg)

    final_messages: list[dict[str, Any]] = []
    for prompt in system_prompts[-2:]:
        if prompt:
            final_messages.append({"role": "system", "content": _truncate(prompt, 2000)})
    final_messages.extend(messages)
    input_data["messages"] = final_messages

    if last_asst_text is not None:
        output_data["choices"] = [{
            "index": 0,
            "finish_reason": "stop",
            "message": {"role": "assistant", "content": last_asst_text[:8000]},
        }]

    return input_data, output_data, ""
