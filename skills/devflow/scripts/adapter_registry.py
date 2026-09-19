#!/usr/bin/env python3
import json
from pathlib import Path
from typing import Dict


SKILL_ROOT = Path(__file__).resolve().parent.parent
ADAPTER_ROOT = SKILL_ROOT / "adapters"
SKILLS_MANIFEST = json.loads((SKILL_ROOT.parent / "manifest.json").read_text(encoding="utf-8"))


def load_adapters() -> Dict[str, dict]:
    result = {}
    for path in sorted(ADAPTER_ROOT.glob("*/manifest.json")):
        data = json.loads(path.read_text(encoding="utf-8"))
        required = {
            "version", "id", "dispatch_tool", "topology", "required_tools",
            "stage_executor", "research_executor",
        }
        if not required.issubset(data):
            raise ValueError(f"适配器清单不完整: {path}")
        adapter_id = data["id"]
        if data["version"] != "1.0":
            raise ValueError(f"不支持的适配器清单版本: {adapter_id}/{data['version']}")
        if adapter_id in result or path.parent.name != adapter_id:
            raise ValueError(f"适配器 ID 重复或目录不匹配: {adapter_id}")
        if not isinstance(data["required_tools"], list) or not data["required_tools"]:
            raise ValueError(f"适配器工具列表无效: {adapter_id}")
        if data["topology"] not in {"spawn", "team"}:
            raise ValueError(f"适配器拓扑无效: {adapter_id}")
        if data["topology"] == "team" and not isinstance(data.get("team_name_prefix"), str):
            raise ValueError(f"Team 适配器缺少 team_name_prefix: {adapter_id}")
        if not all(isinstance(data[key], str) and data[key] for key in ("stage_executor", "research_executor")):
            raise ValueError(f"适配器执行器定义无效: {adapter_id}")
        if not (path.parent / "adapter.md").is_file():
            raise ValueError(f"适配器说明缺失: {adapter_id}")
        host_ids = set()
        for agent in data.get("host_agents", []):
            required_agent = {"id", "file", "description", "access"}
            if not isinstance(agent, dict) or not required_agent.issubset(agent):
                raise ValueError(f"宿主 Agent 定义不完整: {adapter_id}")
            if agent["id"] in host_ids:
                raise ValueError(f"宿主 Agent ID 重复: {adapter_id}/{agent['id']}")
            if agent["access"] not in {"read", "report", "write"}:
                raise ValueError(f"宿主 Agent access 无效: {adapter_id}/{agent['id']}")
            body = (path.parent / agent.get("body", f"agents/{agent['file']}")).resolve()
            if ADAPTER_ROOT.resolve() not in body.parents or not body.is_file():
                raise ValueError(f"宿主 Agent 正文缺失或越界: {adapter_id}/{agent['file']}")
            host_ids.add(agent["id"])
        expected_ids = {data["stage_executor"], data["research_executor"]}
        if data.get("installation") and data.get("host_agents") and host_ids != expected_ids:
            raise ValueError(f"宿主执行器与清单不一致: {adapter_id}")
        if data.get("installation"):
            installation = data["installation"]
            required_install = {"project_dir", "agents_dir", "skills_dir", "skill_set"}
            if not isinstance(installation, dict) or not required_install.issubset(installation):
                raise ValueError(f"安装声明不完整: {adapter_id}")
            skill_set = SKILLS_MANIFEST.get("skill_sets", {}).get(installation["skill_set"])
            if not isinstance(skill_set, list) or not skill_set:
                raise ValueError(f"未知或空的 Skill 集合: {adapter_id}")
            for skill in skill_set:
                if not (SKILL_ROOT.parent / skill / "SKILL.md").is_file():
                    raise ValueError(f"Skill 集合引用不存在: {adapter_id}/{skill}")
        if adapter_id == "cursor":
            for agent in data["host_agents"]:
                frontmatter = agent.get("frontmatter", {})
                if set(frontmatter) - {"model", "readonly", "is_background"}:
                    raise ValueError(f"Cursor Agent frontmatter 无效: {agent['id']}")
                if frontmatter.get("readonly") != (agent["access"] == "read"):
                    raise ValueError(f"Cursor Agent 读写权限不一致: {agent['id']}")
        if adapter_id == "claude":
            for agent in data["host_agents"]:
                frontmatter = agent.get("frontmatter", {})
                allowed = {"model", "tools", "disallowedTools", "permissionMode", "skills"}
                if set(frontmatter) - allowed:
                    raise ValueError(f"Claude Agent frontmatter 无效: {agent['id']}")
                if agent["access"] == "read" and frontmatter.get("permissionMode") != "plan":
                    raise ValueError(f"Claude 只读 Agent 必须使用 plan: {agent['id']}")
        if adapter_id == "opencode":
            for agent in data["host_agents"]:
                frontmatter = agent.get("frontmatter", {})
                if frontmatter.get("mode") != "subagent":
                    raise ValueError(f"OpenCode Agent 必须使用 subagent: {agent['id']}")
                permission = frontmatter.get("permission", {})
                if not isinstance(permission, dict):
                    raise ValueError(f"OpenCode Agent permission 无效: {agent['id']}")
                if agent["access"] == "read" and permission.get("edit") != "deny":
                    raise ValueError(f"OpenCode 只读 Agent 必须禁止 edit: {agent['id']}")
                if agent["access"] == "read" and permission.get("bash") != "deny":
                    raise ValueError(f"OpenCode 只读 Agent 必须禁止 bash: {agent['id']}")
        result[adapter_id] = data
    if not result:
        raise ValueError("没有可用适配器")
    return result


ADAPTERS = load_adapters()
DISPATCH_TOOLS = {adapter_id: item["dispatch_tool"] for adapter_id, item in ADAPTERS.items()}
STAGE_EXECUTORS = {adapter_id: item["stage_executor"] for adapter_id, item in ADAPTERS.items()}
RESEARCH_EXECUTORS = {adapter_id: item["research_executor"] for adapter_id, item in ADAPTERS.items()}
TOPOLOGIES = {adapter_id: item["topology"] for adapter_id, item in ADAPTERS.items()}
TEAM_NAME_PREFIXES = {
    adapter_id: item.get("team_name_prefix") for adapter_id, item in ADAPTERS.items()
}
