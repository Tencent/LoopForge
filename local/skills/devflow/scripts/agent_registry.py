#!/usr/bin/env python3
import json
from pathlib import Path
from typing import Dict, List


SKILL_ROOT = Path(__file__).resolve().parent.parent
AGENT_ROOT = SKILL_ROOT / "agents"
MANIFEST_PATH = AGENT_ROOT / "manifest.json"


def load_manifest() -> dict:
    data = json.loads(MANIFEST_PATH.read_text(encoding="utf-8"))
    if not isinstance(data.get("version"), str) or not data["version"]:
        raise ValueError("Agent 清单缺少 version")
    roles = data.get("roles")
    if not isinstance(roles, list) or not roles:
        raise ValueError("Agent 清单 roles 必须是非空数组")
    ids = set()
    stages = set()
    for role in roles:
        required = {"id", "kind", "file", "description", "access"}
        if not isinstance(role, dict) or not required.issubset(role):
            raise ValueError("Agent 清单存在不完整的角色定义")
        role_id = role["id"]
        if role_id in ids:
            raise ValueError(f"Agent ID 重复: {role_id}")
        ids.add(role_id)
        if role["kind"] not in {"stage", "helper"}:
            raise ValueError(f"Agent 类型无效: {role_id}")
        if role["access"] not in {"read", "report", "write", "test-write"}:
            raise ValueError(f"Agent access 无效: {role_id}")
        if role["kind"] == "stage":
            stage = role.get("stage")
            if not stage or stage in stages:
                raise ValueError(f"阶段缺失或重复: {stage}")
            stages.add(stage)
            if not isinstance(role.get("input_stages"), list) or not isinstance(
                role.get("workspace_evidence"), list
            ):
                raise ValueError(f"阶段角色缺少输入定义: {role_id}")
            if not isinstance(role.get("input_artifacts"), list):
                raise ValueError(f"阶段角色缺少精确产物输入定义: {role_id}")
            if role.get("execution", "agent") not in {"agent", "coordinator"}:
                raise ValueError(f"阶段 execution 无效: {role_id}")
        body = AGENT_ROOT / role["file"]
        if not body.is_file() or not body.read_text(encoding="utf-8").strip():
            raise ValueError(f"Agent 正文不存在或为空: {role['file']}")
    return data


MANIFEST = load_manifest()
ROLES: Dict[str, dict] = {item["id"]: item for item in MANIFEST["roles"]}
STAGE_ROLES: Dict[str, str] = {
    item["stage"]: item["id"] for item in MANIFEST["roles"] if item["kind"] == "stage"
}
STAGE_EXECUTION: Dict[str, str] = {
    item["stage"]: item.get("execution", "agent")
    for item in MANIFEST["roles"]
    if item["kind"] == "stage"
}
HELPER_ROLES = {item["id"] for item in MANIFEST["roles"] if item["kind"] == "helper"}


def role_body(role_id: str) -> str:
    role = ROLES[role_id]
    return (AGENT_ROOT / role["file"]).read_text(encoding="utf-8").strip()


def roles() -> List[dict]:
    return list(MANIFEST["roles"])
