#!/usr/bin/env python3
import json
import re
from pathlib import Path

from adapter_registry import ADAPTERS
from agent_registry import MANIFEST as AGENT_MANIFEST, STAGE_ROLES
from template_registry import MANIFEST as TEMPLATE_MANIFEST, required_artifacts


SKILL_ROOT = Path(__file__).resolve().parent.parent
DEFAULTS = json.loads((SKILL_ROOT / "config/workflow.json").read_text(encoding="utf-8"))


def errors() -> list:
    result = []
    route_stages = {stage for route in DEFAULTS["routes"].values() for stage in route}
    if route_stages != set(STAGE_ROLES):
        result.append("routes 与阶段角色清单不一致")
    for stage in route_stages:
        if not required_artifacts(stage):
            result.append(f"阶段没有固定产物模板: {stage}")
    for role in AGENT_MANIFEST["roles"]:
        for relative in role.get("input_artifacts", []):
            if relative not in {item["path"] for item in TEMPLATE_MANIFEST["artifacts"]}:
                result.append(f"{role['id']}: 未知输入产物 {relative}")
        for stage in role.get("input_stages", []):
            if stage != "*" and stage not in route_stages:
                result.append(f"{role['id']}: 未知上游阶段 {stage}")
            if stage != "*" and role.get("stage"):
                for route_name, route in DEFAULTS["routes"].items():
                    if role["stage"] in route and (
                        stage not in route or route.index(stage) >= route.index(role["stage"])
                    ):
                        result.append(
                            f"{role['id']}: {route_name} 中上游阶段顺序无效 {stage}"
                        )
    if not set(DEFAULTS["manual_gates"]).issubset(route_stages):
        result.append("manual_gates 包含未知阶段")
    if not set(DEFAULTS.get("required_approval_gates", [])).issubset(route_stages):
        result.append("required_approval_gates 包含未知阶段")
    state_template = json.loads(
        (SKILL_ROOT / "templates/workflow-state.json").read_text(encoding="utf-8")
    )
    if state_template.get("version") != DEFAULTS.get("version"):
        result.append("workflow 配置与状态模板版本不一致")
    if not (SKILL_ROOT / "rules/core.md").is_file():
        result.append("缺少 rules/core.md")
    if not (SKILL_ROOT / "commands/devflow.md").is_file():
        result.append("缺少 commands/devflow.md")
    for adapter_id in ADAPTERS:
        if not (SKILL_ROOT / "adapters" / adapter_id / "adapter.md").is_file():
            result.append(f"适配器说明缺失: {adapter_id}")
    link_pattern = re.compile(r"\]\(([^)#]+)(?:#[^)]+)?\)")
    for source in SKILL_ROOT.rglob("*.md"):
        for target in link_pattern.findall(source.read_text(encoding="utf-8")):
            if "://" in target or target.startswith("/"):
                continue
            if not (source.parent / target).resolve().exists():
                result.append(f"失效链接: {source.relative_to(SKILL_ROOT)} -> {target}")
    return result


def main() -> int:
    found = errors()
    if found:
        print("ERROR:\n- " + "\n- ".join(found))
        return 1
    print(
        "OK: "
        f"adapters={len(ADAPTERS)} logical_roles={len(AGENT_MANIFEST['roles'])} "
        f"host_agents={sum(len(item.get('host_agents', [])) for item in ADAPTERS.values())} "
        f"artifacts={len(TEMPLATE_MANIFEST['artifacts'])}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
