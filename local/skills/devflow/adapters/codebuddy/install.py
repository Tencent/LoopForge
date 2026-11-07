#!/usr/bin/env python3
import argparse
import json
import shutil
from pathlib import Path
from typing import List, Tuple


SKILL_ROOT = Path(__file__).resolve().parents[2]
ADAPTER_ROOT = Path(__file__).resolve().parent
CONFIG = json.loads((ADAPTER_ROOT / "manifest.json").read_text(encoding="utf-8"))
MANAGED_MARKER = "managed-by: devflow-codebuddy-adapter"
LEGACY_AGENT_NAMES = {
    "architect.md", "code-reviewer.md", "developer.md", "knowledge-engineer.md",
    "leader.md", "solo-developer.md", "test-engineer.md",
}


def quote(value: str) -> str:
    return json.dumps(value, ensure_ascii=False)


def host_agents() -> List[dict]:
    agents = CONFIG.get("host_agents")
    if not isinstance(agents, list) or not agents:
        raise ValueError("CodeBuddy 适配器缺少 host_agents")
    return agents


def render_agent(spec: dict) -> str:
    agent = CONFIG["agent"]
    permission = agent[f"{spec['access']}_permission_mode"]
    body = (ADAPTER_ROOT / "agents" / spec["file"]).read_text(encoding="utf-8").strip()
    return "\n".join([
        "---",
        f"name: {spec['id']}",
        f"description: {quote(spec['description'])}",
        f"agentMode: {agent['agentMode']}",
        f"enabled: {str(agent['enabled']).lower()}",
        f"permissionMode: {permission}",
        f"enabledAutoRun: {str(agent['enabledAutoRun']).lower()}",
        "---",
        "",
        f"<!-- {MANAGED_MARKER} -->",
        body,
        "",
    ])


def install_agents(project_root: Path, refresh_managed: bool) -> Tuple[int, int, int]:
    target = project_root / ".codebuddy/agents"
    target.mkdir(parents=True, exist_ok=True)
    specs = host_agents()
    expected = {f"{spec['id']}.md" for spec in specs}
    installed = preserved = removed = 0
    if refresh_managed:
        for destination in target.glob("*.md"):
            if destination.name in expected:
                continue
            if MANAGED_MARKER in destination.read_text(encoding="utf-8", errors="ignore"):
                destination.unlink()
                removed += 1
    for spec in specs:
        destination = target / f"{spec['id']}.md"
        if destination.exists():
            managed = MANAGED_MARKER in destination.read_text(encoding="utf-8", errors="ignore")
            if not (refresh_managed and managed):
                preserved += 1
                continue
        destination.write_text(render_agent(spec), encoding="utf-8")
        installed += 1
    return installed, preserved, removed


def legacy_conflicts(project_root: Path) -> List[str]:
    target = project_root / ".codebuddy/agents"
    return sorted(name for name in LEGACY_AGENT_NAMES if (target / name).is_file())


def install_clarifier(project_root: Path) -> str:
    source = SKILL_ROOT.parent / "devflow-clarify-requirements"
    if not (source / "SKILL.md").is_file():
        return "clarifier_source_missing"
    target = project_root / ".codebuddy/skills/devflow-clarify-requirements"
    target.parent.mkdir(parents=True, exist_ok=True)
    if target.exists() or target.is_symlink():
        return "clarifier_preserved"
    try:
        target.symlink_to(source, target_is_directory=True)
        return "clarifier_linked"
    except OSError:
        shutil.copytree(source, target)
        return "clarifier_copied"


def render_command() -> str:
    command = CONFIG["command"]
    allowed = json.dumps(command["allowed_tools"], ensure_ascii=False)
    body = (SKILL_ROOT / "commands/devflow.md").read_text(encoding="utf-8").strip()
    return "\n".join([
        "---",
        f"description: {command['description']}",
        f"argument-hint: {command['argument_hint']}",
        f"allowed-tools: {allowed}",
        "---",
        "",
        f"<!-- {MANAGED_MARKER} -->",
        "",
        body,
        "",
    ])


def install_command(project_root: Path, refresh_managed: bool) -> str:
    target = project_root / ".codebuddy/commands/devflow.md"
    target.parent.mkdir(parents=True, exist_ok=True)
    if target.exists():
        managed = MANAGED_MARKER in target.read_text(encoding="utf-8", errors="ignore")
        if not (refresh_managed and managed):
            return "command_preserved"
    target.write_text(render_command(), encoding="utf-8")
    return "command_installed"


def main() -> int:
    parser = argparse.ArgumentParser(description="把 DevFlow CodeBuddy 适配器安装到项目")
    parser.add_argument("--project-root", type=Path, required=True)
    parser.add_argument("--refresh-managed", action="store_true")
    args = parser.parse_args()
    root = args.project_root.resolve()
    if not root.is_dir() or not (root / ".codebuddy").is_dir():
        raise SystemExit("项目根目录必须包含 .codebuddy")
    installed, preserved, removed = install_agents(root, args.refresh_managed)
    conflicts = legacy_conflicts(root)
    clarifier = install_clarifier(root)
    command = install_command(root, args.refresh_managed)
    print(
        f"OK: agents_installed={installed} agents_preserved={preserved} "
        f"legacy_managed_removed={removed} legacy_conflicts={len(conflicts)} "
        f"{clarifier} {command}"
    )
    if conflicts:
        print(
            "WARNING: 发现旧版 DevFlow Agent，运行 /devflow 时不得派发给它们："
            + ", ".join(conflicts)
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
