"""Agent 身份解析层。

这一层负责统一解析“当前是谁在工作、又把任务派给了谁”：
- 从 tool 调用参数里提取 agent 线索
- 从 team inbox / mailbox 中补偿推断 agent 身份
- 把解析结果写回 session state，供后续日志归因复用
"""
from __future__ import annotations

import json
import os
import re
import time
from datetime import datetime, timezone
from functools import lru_cache
from pathlib import Path
from typing import Any, Optional

from . import scanner, state as st

AGENT_PATH_RE = re.compile(r"\.codebuddy/agents/([a-zA-Z0-9_\-]+)(?:\.md)?", re.I)
DISPATCH_TOOLS = {"Task", "task", "Agent", "DeferExecuteTool"}
MESSAGE_TOOLS = {"send_message", "SendMessage"}
TEAM_PREFIX = "multi-agents-devflow-"
ROLE_INSTANCE_RE = re.compile(r"^(?P<base>[a-z0-9][a-z0-9\-]*?)-\d+$", re.I)
INITIAL_ASSIGNMENT_RE = re.compile(
    r"Initial task assignment for (?P<role>[a-z0-9][a-z0-9\-]*)",
    re.I,
)
ROLE_DECLARATION_RE = re.compile(
    r"角色[：:]\s*(?P<role>[a-z0-9][a-z0-9\-]*)",
    re.I,
)
# devflow TASK-03 并行 fan-out 时，developer 会以 Task(name="sub-developer-PT-01", ...)
# 派发多条并行轨道。轨道号只在 transcript 文件名（= Task 的 name 参数）里出现，
# 按 subagent_name 归一化后会被折成同一个 "developer" —— 这是有意的（用于按角色汇总），
# 轨道号需要单独提取，见 pt_id_from_transcript_path。
PT_TRACK_RE = re.compile(r"-((?:PT|pt)-\d+)$")


def normalize_role_name(name: str | None) -> Optional[str]:
    """把角色名归一化成稳定标识，例如把实例名折叠回基础角色名。"""
    if not isinstance(name, str):
        return None
    cand = name.strip().lower()
    if not cand:
        return None
    if cand.endswith(".json"):
        cand = cand[:-5]
    if "@" in cand:
        cand = cand.split("@", 1)[0]
    if cand in {"team-lead", "main"}:
        return "main"
    match = ROLE_INSTANCE_RE.match(cand)
    if match:
        cand = match.group("base")
    return cand or None


def resolve_teams_root() -> Path:
    """解析 CodeBuddy team inbox 的根目录。"""
    config_dir = (os.environ.get("CODEBUDDY_CONFIG_DIR") or "").strip()
    if config_dir:
        return Path(config_dir).expanduser() / "teams"
    return Path.home() / ".codebuddy" / "teams"


def resolve_team_dir(cwd: str, sid: str, cached_team_dir: str | None = None) -> Path | None:
    """根据 session id 和 cwd 找到当前会话对应的 team 目录。"""
    if cached_team_dir:
        cached = Path(cached_team_dir).expanduser()
        if (cached / "config.json").is_file():
            cfg = _load_json(cached / "config.json")
            if isinstance(cfg, dict) and sid and str(cfg.get("leadSessionId") or "") == sid:
                return cached

    if not sid:
        return None

    teams_root = resolve_teams_root()
    if not teams_root.is_dir():
        return None

    best_dir: Path | None = None
    best_created = -1
    for team_dir in teams_root.iterdir():
        if not team_dir.is_dir() or not team_dir.name.startswith(TEAM_PREFIX):
            continue
        config = _load_json(team_dir / "config.json")
        if not isinstance(config, dict):
            continue
        if str(config.get("leadSessionId") or "") != sid:
            continue
        if not _config_matches_cwd(config, cwd):
            continue

        created_at = _safe_int(config.get("createdAt"))
        if created_at <= 0:
            try:
                created_at = int(team_dir.stat().st_mtime * 1000)
            except Exception:
                created_at = 0
        if created_at > best_created:
            best_dir = team_dir
            best_created = created_at
    return best_dir


def read_recent_messages(
    team_dir: Path,
    offsets: dict[str, int] | None = None,
    *,
    max_per_mailbox: int = 30,
) -> tuple[list[dict[str, Any]], dict[str, int]]:
    """按 mailbox 增量读取最近消息，并返回新的 offset 游标。"""
    inbox_dir = team_dir / "inboxes"
    if not inbox_dir.is_dir():
        return [], offsets or {}

    prev_offsets = offsets or {}
    new_offsets: dict[str, int] = {}
    messages: list[dict[str, Any]] = []

    for inbox_file in sorted(inbox_dir.glob("*.json")):
        raw = _load_json(inbox_file)
        if not isinstance(raw, list):
            raw = []

        total = len(raw)
        prev = _safe_int(prev_offsets.get(inbox_file.name))
        if prev < 0 or prev > total:
            prev = 0

        start = prev
        if start == 0 and total > max_per_mailbox:
            start = total - max_per_mailbox

        mailbox_name = inbox_file.stem
        mailbox_role = normalize_role_name(mailbox_name)
        for idx, item in enumerate(raw[start:], start=start):
            norm = _normalize_message(item, mailbox_name, mailbox_role, idx)
            if norm:
                messages.append(norm)

        new_offsets[inbox_file.name] = total

    messages.sort(key=lambda x: (x.get("ts") or 0.0, x.get("mailbox_name") or "", x.get("index") or 0))
    return messages, new_offsets


def infer_identity_from_messages(messages: list[dict[str, Any]]) -> tuple[Optional[str], Optional[str], dict[str, Any]]:
    """根据 inbox 消息流推断当前 agent 与派发目标。"""
    current: Optional[str] = None
    dispatched: Optional[str] = None
    evidence: Optional[str] = None

    for msg in messages:
        mailbox_name = str(msg.get("mailbox_name") or "")
        mailbox_role = normalize_role_name(msg.get("mailbox_role"))
        from_role = normalize_role_name(msg.get("from_role"))
        payload = msg.get("payload") if isinstance(msg.get("payload"), dict) else {}
        next_target = _extract_next_target(payload)

        if from_role == "main" and mailbox_role and mailbox_role != "main":
            if not msg.get("is_shutdown"):
                current = mailbox_role
                dispatched = mailbox_role
                evidence = f"dispatch:{mailbox_name}"
            continue

        if mailbox_name == "team-lead" and from_role and from_role not in {"main", "system"}:
            report_role = normalize_role_name(
                payload.get("from_role") if isinstance(payload, dict) else None
            ) or from_role
            if next_target:
                current = next_target
                dispatched = next_target
                evidence = f"handoff:{report_role}->{next_target}"
            else:
                current = report_role
                evidence = f"report:{report_role}"

    meta: dict[str, Any] = {"messages_seen": len(messages)}
    if evidence:
        meta["evidence"] = evidence
    return current, dispatched, meta


def safe_agent_from_provider(record: dict[str, Any]) -> str | None:
    """当 transcript 自己带有 providerData 时，直接读取其中的 agent。"""
    provider = record.get("providerData")
    if not isinstance(provider, dict):
        return None
    agent = provider.get("agent")
    if isinstance(agent, str) and agent.strip():
        return normalize_role_name(agent.strip())
    return None


def role_from_content_items(content: Any) -> str | None:
    """从 transcript content 文本中提取更具体的业务角色名。"""
    if not isinstance(content, list):
        return None
    for item in content:
        if not isinstance(item, dict):
            continue
        text = item.get("text")
        if not isinstance(text, str):
            continue
        for regex in (INITIAL_ASSIGNMENT_RE, ROLE_DECLARATION_RE):
            match = regex.search(text)
            if not match:
                continue
            role = normalize_role_name(match.group("role"))
            if role:
                return role
    return None


@lru_cache(maxsize=256)
def role_for_subagent_transcript(transcript_path: str) -> str | None:
    """从 subagent transcript 前几行推断更准确的角色名。"""
    path = Path(transcript_path)
    try:
        with path.open("r", encoding="utf-8") as fp:
            for _ in range(6):
                line = fp.readline()
                if not line:
                    break
                try:
                    record = json.loads(line)
                except Exception:
                    continue

                # 对 subagent transcript，优先信任务分配文本里的具体角色，
                # 再回退到 providerData.agent，避免 general-purpose 覆盖业务角色。
                content_role = role_from_content_items(record.get("content"))
                if content_role:
                    return content_role

                provider_agent = safe_agent_from_provider(record)
                if provider_agent:
                    return provider_agent
    except Exception:
        return None
    return None


def agent_for_transcript_path(transcript_path: str, fallback: str | None = None) -> str:
    """把 transcript 路径映射回实际对应的 agent 身份。"""
    parts = Path(transcript_path).parts
    if "subagents" in parts:
        return role_for_subagent_transcript(transcript_path) or Path(transcript_path).stem
    return fallback or "main"


def pt_id_from_transcript_path(transcript_path: str) -> str | None:
    """从并行 sub-developer 的 transcript 文件名里提取 PT 轨道号（如 "PT-01"）。

    按事件自己的 transcript_path 推断，而不是写一个共享的 session 级"当前 PT"指针——
    devflow 一次可以并行派发最多 6 条 sub-developer 轨道，它们在同一个 sid 下
    真·并发运行，任何"当前是哪条轨道"的可变共享状态在并发场景下都是错的。
    """
    parts = Path(transcript_path).parts
    if "subagents" not in parts:
        return None
    match = PT_TRACK_RE.search(Path(transcript_path).stem)
    return match.group(1).upper() if match else None


def merge_agent_identity_from_inbox(
    state_path: Path,
    sid: str,
    cwd: str,
    active_agent: str | None,
    dispatched: str | None,
) -> tuple[str, str | None]:
    """在基于 tool 的推断之上，再叠加 mailbox/inbox 的证据。"""

    def _update(state: dict[str, Any]) -> tuple[str, str | None]:
        sess = st.ensure_session(state, sid)
        cached_team_dir = str(sess.get("_team_dir") or "").strip() or None
        offsets = sess.get("_inbox_offsets")
        if not isinstance(offsets, dict):
            offsets = {}

        effective_agent = active_agent or str(sess.get("current_agent") or "main")
        effective_dispatched = dispatched
        team_dir = resolve_team_dir(cwd, sid, cached_team_dir)
        if team_dir is None:
            return effective_agent, effective_dispatched

        sess["_team_dir"] = str(team_dir)
        messages, new_offsets = read_recent_messages(team_dir, offsets)
        sess["_inbox_offsets"] = new_offsets
        inferred_current, inferred_dispatched, meta = infer_identity_from_messages(messages)
        if inferred_current:
            effective_agent = inferred_current
        if inferred_dispatched:
            effective_dispatched = inferred_dispatched
        if inferred_current or inferred_dispatched:
            target_agent = inferred_current or inferred_dispatched
            if target_agent:
                sess["current_agent"] = target_agent
                hist = sess.setdefault("agent_history", [])
                hist.append({
                    "ts": time.time(),
                    "agent": target_agent,
                    "evidence": f"inbox@{meta.get('evidence') or 'inbox'}",
                })
        return effective_agent, effective_dispatched

    return st.update_state_locked(state_path, _update)


def resolve_active_agent_for_event(
    *,
    state_path: Path,
    sid: str,
    cwd: str,
    data: dict[str, Any],
) -> tuple[str, str | None]:
    """优先用 tool 线索、其次用 inbox 线索，解析事件对应的 agent。"""
    tracker = AgentIdentityResolver()
    active_agent, dispatched = tracker.track(state_path, sid, data)
    if scanner.is_brainstorming_call(data):

        def _update(state_data: dict[str, Any]) -> None:
            sess = st.ensure_session(state_data, sid)
            sess["current_agent"] = "main"

        st.update_state_locked(state_path, _update)
        return "main", dispatched
    return merge_agent_identity_from_inbox(
        state_path,
        sid,
        cwd,
        active_agent,
        dispatched,
    )


class AgentIdentityResolver:
    """基于 tool 调用内容做一轮 agent 身份推断。"""
    def __init__(self, known_agents: set[str] | None = None):
        self.known_agents = known_agents or set()

    def extract_identity(self, data: dict) -> tuple[Optional[str], Optional[str]]:
        tool = data.get("tool_name", "")
        tool_input = data.get("tool_input") or {}
        if not isinstance(tool_input, dict):
            tool_input = {}

        current: Optional[str] = None
        dispatched: Optional[str] = None

        if tool in MESSAGE_TOOLS:
            extracted_current, extracted_dispatched = self._extract_from_message(tool_input)
            if extracted_current and not current:
                current = extracted_current
            if extracted_dispatched and not dispatched:
                dispatched = extracted_dispatched

        if tool in DISPATCH_TOOLS and not dispatched:
            dispatched = self._extract_from_task(tool_input)

        if not current:
            current = self._extract_from_path(data, tool_input)

        if self.known_agents:
            if current and current not in self.known_agents:
                current = None
            if dispatched and dispatched not in self.known_agents:
                dispatched = None

        return current, dispatched

    def track(self, state_path: Path, sid: str, data: dict) -> tuple[str, Optional[str]]:
        tool = data.get("tool_name", "")
        cur_agent, dispatched = self.extract_identity(data)

        def _update(state: dict[str, Any]) -> tuple[str, Optional[str]]:
            sess = st.ensure_session(state, sid)
            hist = sess.setdefault("agent_history", [])
            prev_agent = sess.get("current_agent") or "main"

            if cur_agent and cur_agent != prev_agent:
                sess["current_agent"] = cur_agent
                hist.append({"ts": time.time(), "agent": cur_agent, "evidence": f"from_role@{tool}"})
                active_agent = cur_agent
            else:
                active_agent = prev_agent

            if dispatched and dispatched != active_agent:
                dis = sess.setdefault("dispatched", {})
                if not isinstance(dis, dict):
                    dis = {}
                    sess["dispatched"] = dis
                dis[dispatched] = dis.get(dispatched, 0) + 1
                sess["current_agent"] = dispatched
                hist.append({"ts": time.time(), "agent": dispatched, "evidence": f"dispatch@{tool}<-{active_agent}"})

            if len(hist) > 100:
                sess["agent_history"] = hist[-100:]

            return active_agent, dispatched

        return st.update_state_locked(state_path, _update)

    def _extract_from_message(self, tool_input: dict) -> tuple[Optional[str], Optional[str]]:
        current: Optional[str] = None
        dispatched: Optional[str] = None
        content = tool_input.get("content")

        if isinstance(content, str) and content.strip().startswith("{"):
            try:
                payload = json.loads(content)
                current = self._extract_str(payload, "from_role")
                next_target = payload.get("next_target") or {}
                if isinstance(next_target, dict):
                    dispatched = (
                        self._extract_str(next_target, "role_name")
                        or self._extract_str(next_target, "subagent_name")
                    )
            except Exception:
                pass
        elif isinstance(content, dict):
            current = self._extract_str(content, "from_role")
            next_target = content.get("next_target") or {}
            if isinstance(next_target, dict):
                dispatched = (
                    self._extract_str(next_target, "role_name")
                    or self._extract_str(next_target, "subagent_name")
                )

        if not dispatched:
            recipient = tool_input.get("recipient")
            if isinstance(recipient, str):
                normalized = recipient.strip().lower()
                if normalized and normalized != "main":
                    dispatched = normalized

        return current, dispatched

    def _extract_from_task(self, tool_input: dict) -> Optional[str]:
        sub = (
            # Claude Code 的 Agent 工具（非 CodeBuddy 原生 Task/team_create）用的是
            # subagent_type 字段，不是 subagent_name——不认这个字段会导致 dispatch
            # 统计漏掉所有走 Agent 工具派发的场景。
            tool_input.get("subagent_type")
            or tool_input.get("subagent_name")
            or tool_input.get("name")
            or tool_input.get("agent")
            or tool_input.get("role")
        )
        if isinstance(sub, str) and sub.strip():
            candidate = sub.strip().lower()
            if "/" in candidate:
                candidate = candidate.rsplit("/", 1)[1].replace(".md", "")
            return candidate
        return None

    def _extract_from_path(self, data: dict, tool_input: dict) -> Optional[str]:
        text = json.dumps(tool_input, ensure_ascii=False) + " " + (data.get("cwd") or "")
        match = AGENT_PATH_RE.search(text)
        if match:
            return match.group(1).lower()
        return None

    @staticmethod
    def _extract_str(payload: dict, key: str) -> Optional[str]:
        value = payload.get(key)
        if isinstance(value, str) and value.strip():
            return value.strip().lower()
        return None


def _config_matches_cwd(config: dict[str, Any], cwd: str) -> bool:
    """判断 team 配置中的成员 cwd 是否覆盖当前工作目录。"""
    members = config.get("members")
    if not isinstance(members, list):
        return False
    for member in members:
        if not isinstance(member, dict):
            continue
        member_cwd = member.get("cwd")
        if isinstance(member_cwd, str) and _paths_equal(member_cwd, cwd):
            return True
    return False


def _paths_equal(a: str, b: str) -> bool:
    """按归一化绝对路径语义比较两个路径是否相等。"""
    try:
        return Path(a).expanduser().resolve() == Path(b).expanduser().resolve()
    except Exception:
        return os.path.abspath(os.path.expanduser(a)) == os.path.abspath(os.path.expanduser(b))


def _normalize_message(item: Any, mailbox_name: str, mailbox_role: str | None, index: int) -> dict[str, Any] | None:
    """把原始 inbox 消息归一化成统一可推断的结构。"""
    if not isinstance(item, dict):
        return None

    text = item.get("text")
    payload = _coerce_payload(text)
    summary = item.get("summary") if isinstance(item.get("summary"), str) else ""
    from_raw = item.get("from") if isinstance(item.get("from"), str) else None
    from_role = normalize_role_name((payload.get("from") if isinstance(payload, dict) else None) or from_raw)
    ts = _parse_timestamp(item.get("timestamp"))

    return {
        "mailbox_name": mailbox_name,
        "mailbox_role": mailbox_role,
        "from": from_raw,
        "from_role": from_role,
        "text": text if isinstance(text, str) else "",
        "summary": summary,
        "payload": payload,
        "timestamp": item.get("timestamp"),
        "ts": ts,
        "index": index,
        "is_shutdown": _is_shutdown_message(payload, summary, text),
    }


def _extract_next_target(payload: dict[str, Any] | None) -> Optional[str]:
    """从 payload 中提取下一跳要派发给的角色。"""
    if not isinstance(payload, dict):
        return None
    next_target = payload.get("next_target") or {}
    if not isinstance(next_target, dict):
        return None
    return normalize_role_name(next_target.get("role_name") or next_target.get("subagent_name"))


def _coerce_payload(text: Any) -> dict[str, Any] | None:
    """把文本内容尽量解析成 JSON 字典。"""
    if isinstance(text, dict):
        return text
    if not isinstance(text, str):
        return None
    stripped = text.strip()
    if not stripped.startswith("{"):
        return None
    try:
        data = json.loads(stripped)
        if isinstance(data, dict):
            return data
    except Exception:
        return None
    return None


def _is_shutdown_message(payload: dict[str, Any] | None, summary: str, text: Any) -> bool:
    """识别一条 inbox 消息是否表示 agent 关闭或退出。"""
    if isinstance(payload, dict) and str(payload.get("type") or "").lower() == "shutdown_request":
        return True
    summary_low = summary.lower() if isinstance(summary, str) else ""
    text_low = text.lower() if isinstance(text, str) else ""
    return "shutdown" in summary_low or "shutdown request" in text_low


def _parse_timestamp(value: Any) -> float:
    """把消息时间字段解析成 Unix 时间戳。"""
    if not isinstance(value, str) or not value.strip():
        return datetime.now(timezone.utc).timestamp()
    try:
        return datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()
    except Exception:
        return datetime.now(timezone.utc).timestamp()


def _load_json(path: Path) -> Any:
    """安全读取 JSON 文件，失败时返回 ``None``。"""
    try:
        return json.loads(path.read_text("utf-8"))
    except Exception:
        return None


def _safe_int(value: Any) -> int:
    """把任意值尽量转成整数，失败时返回 0。"""
    try:
        return int(value or 0)
    except Exception:
        return 0
