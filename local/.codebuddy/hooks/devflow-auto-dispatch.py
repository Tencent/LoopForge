#!/usr/bin/env python3
"""
devflow Auto-Dispatch Hook (PreToolUse)

拦截 team member 的 send_message 调用。若消息是 *_completed 事件且
routing_table 中有确定性下一目标，则用 modifiedInput 把 recipient 从 "main"
改成下一个角色，并注入 dispatch_prompt——消息直达下游，main 不被唤醒。

Fallback（仍发 main）的场景：
  - *_failed（需 main 判断 retry_count）
  - manual_gates 事件（auto_mode=false 时需用户确认）
  - workflow_completed（需 main 执行清理）
  - SOLO_overflow（需 main 重新 Phase 0）
  - shutdown_request / shutdown_response
  - payload 缺少 dispatch_prompt 或 next_target
"""
import json
import os
import sys
from datetime import datetime, timezone
from pathlib import Path


# ── 诊断日志 ──
# 写入 .codebuddy/hooks/logs/auto-dispatch.log（JSONL，每行一条事件）
# 记录每次 hook 拦截的：tool_name / sender / recipient / event / 决策（dispatch|fallback|passthrough）/ 原因 / 时间戳
# 便于排查"消息送达但 member 不执行"等问题


def _project_root() -> str:
    return os.environ.get("CODEBUDDY_PROJECT_DIR") or os.getcwd()


_LOG_DIR = Path(_project_root()) / ".codebuddy" / "hooks" / "logs"


def _log_hook(record: dict) -> None:
    """追加一条 JSONL 诊断日志。失败静默，绝不影响派发流程。"""
    try:
        _LOG_DIR.mkdir(parents=True, exist_ok=True)
        log_path = _LOG_DIR / "auto-dispatch.log"
        record.setdefault("ts", datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f%z"))
        with open(log_path, "a", encoding="utf-8") as f:
            f.write(json.dumps(record, ensure_ascii=False) + "\n")
    except Exception:
        pass


# ── routing_table（与 devflow.defaults.yaml 同步，SSOT 副本） ──
ROUTING_TABLE = {
    "TASK-01_completed": "architect",
    "TASK-02_completed": "developer",
    "TASK-03_completed": "code-reviewer",
    "CODE-REVIEW_passed": "test-engineer",
    "TASK-04_completed": "knowledge-engineer",
    "TASK-05_completed": "leader",
}

# manual_gates（与 devflow.defaults.yaml 同步）
MANUAL_GATES = {
    "TASK-02_completed",
    "CODE-REVIEW_passed",
    "TASK-04_completed",
}

# 必须回 main 的事件（不可自动派发）
FALLBACK_EVENTS = {
    "workflow_completed",
    "SOLO_overflow",
}


def _read_config() -> dict:
    """读取 devflow 配置，合并 defaults + 用户覆写。"""
    root = Path(_project_root())
    defaults_path = root / ".codebuddy" / "assets" / "devflow.defaults.yaml"
    config_path = root / ".codebuddy" / "devflow.config.yaml"

    config = {}
    for p in [defaults_path, config_path]:
        if not p.is_file():
            continue
        try:
            import yaml  # type: ignore
            with open(p, "r", encoding="utf-8") as f:
                data = yaml.safe_load(f) or {}
                if isinstance(data, dict):
                    config.update(data)
        except Exception:
            pass
    return config


def _append_decision(state_path: str, event: str, action: str) -> None:
    """向 workflow-state.json 的 decisions[] 追加审计记录。"""
    if not state_path:
        return
    try:
        p = Path(state_path)
        if not p.is_file():
            return
        with open(p, "r", encoding="utf-8") as f:
            state = json.load(f)
        if not isinstance(state, dict):
            return
        decisions = state.setdefault("decisions", [])
        decisions.append({
            "kind": "auto_dispatch",
            "event": event,
            "action": action,
            "ts": _now_iso(),
        })
        state["updated_at"] = _now_iso()
        with open(p, "w", encoding="utf-8") as f:
            json.dump(state, f, ensure_ascii=False, indent=2)
    except Exception:
        pass  # 审计失败不阻塞派发


def _now_iso() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S%z")


def main() -> int:
    # ── 解析 hook 输入 ──
    try:
        data = json.loads(sys.stdin.read())
    except (json.JSONDecodeError, IOError) as e:
        _log_hook({"decision": "passthrough", "reason": "stdin_json_parse_failed", "error": str(e)})
        print(json.dumps({"continue": True}))
        return 0

    tool_name = data.get("tool_name", "")
    if tool_name not in ("send_message", "SendMessage"):
        # 非 send_message 工具，不干预
        print(json.dumps({"continue": True}))
        return 0

    tool_input = data.get("tool_input", {})
    msg_type = tool_input.get("type", "")
    sender = tool_input.get("from", "") or data.get("agent_name", "") or "unknown"
    original_recipient = tool_input.get("recipient", "")

    # shutdown_request / shutdown_response 照常发给 main
    if msg_type != "message":
        _log_hook({
            "decision": "passthrough", "reason": f"non_message_type:{msg_type}",
            "sender": sender, "recipient": original_recipient,
        })
        print(json.dumps({"continue": True}))
        return 0

    content_raw = tool_input.get("content", "")
    if not isinstance(content_raw, str):
        _log_hook({
            "decision": "passthrough", "reason": "content_not_string",
            "sender": sender, "recipient": original_recipient,
        })
        print(json.dumps({"continue": True}))
        return 0

    # 解析 content JSON
    try:
        payload = json.loads(content_raw)
    except (json.JSONDecodeError, ValueError) as e:
        _log_hook({
            "decision": "passthrough", "reason": "content_json_parse_failed",
            "sender": sender, "recipient": original_recipient,
            "error": str(e), "content_preview": content_raw[:200],
        })
        print(json.dumps({"continue": True}))
        return 0

    if not isinstance(payload, dict):
        _log_hook({
            "decision": "passthrough", "reason": "content_not_dict",
            "sender": sender, "recipient": original_recipient,
        })
        print(json.dumps({"continue": True}))
        return 0

    event = str(payload.get("event", ""))

    # ── 判定是否可自动派发 ──

    # 1. event 必须在 routing_table 中
    next_role = ROUTING_TABLE.get(event)
    if not next_role:
        _log_hook({
            "decision": "passthrough", "reason": "event_not_in_routing_table",
            "sender": sender, "recipient": original_recipient, "event": event,
        })
        print(json.dumps({"continue": True}))
        return 0

    # 2. failed 事件 → 回 main
    if event.endswith("_failed"):
        _log_hook({
            "decision": "fallback_to_main", "reason": "failed_event_needs_main_retry",
            "sender": sender, "recipient": original_recipient, "event": event,
        })
        print(json.dumps({"continue": True}))
        return 0

    # 3. fallback 事件 → 回 main
    if event in FALLBACK_EVENTS:
        _log_hook({
            "decision": "fallback_to_main", "reason": f"fallback_event:{event}",
            "sender": sender, "recipient": original_recipient, "event": event,
        })
        print(json.dumps({"continue": True}))
        return 0

    # 4. 读配置：检查 auto_mode 和 manual_gates
    config = _read_config()
    multi_cfg = config.get("multi", {}) or {}
    auto_mode = multi_cfg.get("auto_mode", True)

    # auto_mode=false 时，manual_gates 事件需回 main 等用户确认
    if not auto_mode and event in MANUAL_GATES:
        _log_hook({
            "decision": "fallback_to_main", "reason": "manual_gate_auto_mode_off",
            "sender": sender, "recipient": original_recipient, "event": event,
        })
        print(json.dumps({"continue": True}))
        return 0

    # 5. payload 必须含 dispatch_prompt
    dispatch_prompt = payload.get("dispatch_prompt")
    if not dispatch_prompt or not isinstance(dispatch_prompt, str):
        _log_hook({
            "decision": "fallback_to_main", "reason": "missing_dispatch_prompt",
            "sender": sender, "recipient": original_recipient, "event": event,
        })
        print(json.dumps({"continue": True}))
        return 0

    # 6. next_target.subagent_name 必须与 routing_table 一致
    next_target = payload.get("next_target") or {}
    payload_target = next_target.get("subagent_name", "")
    if payload_target != next_role:
        _log_hook({
            "decision": "fallback_to_main", "reason": "next_target_mismatch",
            "sender": sender, "recipient": original_recipient, "event": event,
            "expected": next_role, "actual": payload_target,
        })
        print(json.dumps({"continue": True}))
        return 0

    # ── 自动派发：modifiedInput 重定向 recipient + 注入 dispatch_prompt ──

    state_path = payload.get("workflow_state_path", "")
    _append_decision(state_path, event, f"auto-dispatched → {next_role}")

    modified = dict(tool_input)
    modified["recipient"] = next_role
    modified["content"] = dispatch_prompt
    modified["summary"] = f"[auto-dispatch] {event} → {next_role}"

    _log_hook({
        "decision": "auto_dispatch",
        "sender": sender,
        "original_recipient": original_recipient,
        "redirected_to": next_role,
        "event": event,
        "dispatch_prompt_length": len(dispatch_prompt),
        "workflow_state_path": state_path,
    })

    output = {
        "continue": True,
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "allow",
            "modifiedInput": modified,
        },
    }
    print(json.dumps(output))
    return 0


if __name__ == "__main__":
    sys.exit(main())
