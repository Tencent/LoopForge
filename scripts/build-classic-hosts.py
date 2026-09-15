#!/usr/bin/env python3
"""Build and verify Classic Cursor/Claude bundles from the reference workflow."""

import argparse
import json
import re
import shutil
from pathlib import Path
from typing import Dict


ROOT = Path(__file__).resolve().parent.parent
ROLES = (
    "architect", "code-reviewer", "developer", "knowledge-engineer",
    "leader", "solo-developer", "test-engineer",
)
HOSTS = ("cursor", "claude")


def strip_frontmatter(text: str) -> str:
    if text.startswith("---\n"):
        end = text.find("\n---\n", 4)
        if end >= 0:
            return text[end + 5:].lstrip()
    return text


def description(text: str, fallback: str) -> str:
    match = re.match(r"---\n(.*?)\n---\n", text, re.DOTALL)
    if not match:
        return fallback
    value = re.search(r'^description:\s*["\']?(.*?)["\']?\s*$', match.group(1), re.MULTILINE)
    return value.group(1) if value else fallback


def adapt(text: str, host: str) -> str:
    hidden = f".{host}"
    result = text.replace(".codebuddy", hidden).replace(".codex", hidden)
    result = result.replace("Codex", "Cursor" if host == "cursor" else "Claude Code")
    if host == "cursor":
        result = result.replace("multi_agent_v1.spawn_agent", "Task")
        result = result.replace("spawn_agent", "Task")
        result = result.replace("subagent_name=", "subagent_type=")
    else:
        result = result.replace("multi_agent_v1.spawn_agent", "Agent")
        result = result.replace("spawn_agent", "Agent")
        result = result.replace("Task(", "Agent(")
        result = result.replace("subagent_name=", "subagent_type=")
    result = result.replace("Team 模式", "独立 Subagent 模式")
    result = result.replace("team-async", "isolated-subagent")
    result = result.replace("team member", "阶段 subagent")
    result = result.replace("检查 inbox", "检查当前完整阶段提示")
    result = result.replace("保持阶段 subagent 常驻，等待下次唤醒", "结束当前执行，是否恢复由主 Agent 决定")
    result = re.sub(r',?\s*team_name="[^"]*"', "", result)
    result = re.sub(r',?\s*mode="bypassPermissions"', "", result)
    result = re.sub(r'`use_skill\("([^"]+)"\)`', r'`/\1`', result)
    tool = "Task" if host == "cursor" else "Agent"
    result = re.sub(
        r'`send_message\(recipient="?<?([a-zA-Z0-9-]+)>?"?[^`]*\)`',
        rf'`{tool}(subagent_type="devflow-\1", prompt=<完整阶段提示>)`',
        result,
    )
    result = re.sub(r'subagent_type="(?!devflow-)([a-z][a-z0-9-]+)"', r'subagent_type="devflow-\1"', result)
    return result


def command_frontmatter(source: str) -> Dict[str, str]:
    match = re.match(r"---\n(.*?)\n---\n", source, re.DOTALL)
    values = {}
    if not match:
        return values
    for key in ("description", "argument-hint"):
        found = re.search(rf'^{re.escape(key)}:\s*["\']?(.*?)["\']?\s*$', match.group(1), re.MULTILINE)
        if found:
            values[key] = found.group(1)
    return values


def render_command(source: Path, host: str) -> str:
    raw = source.read_text(encoding="utf-8")
    meta = command_frontmatter(raw)
    if source.name == "start-devflow.md":
        runtime = (ROOT / ".codex/runtime/start-workflow.md").read_text(encoding="utf-8")
        body = "# /start-devflow\n\n按以下 Classic 工作流启动用户提供的需求。\n\n" + adapt(runtime, host)
    elif source.name == "resume-devflow.md":
        body = adapt("""# /resume-devflow

读取 `artifacts/<task-slug>/workflow-state.json`，保留已有产物和 task_slug。
根据 `current_stage`、`last_event` 和 `next_target` 找到首个未完成阶段，使用宿主
Agent 工具创建该阶段对应的全新 `devflow-<role>` subagent，并把状态路径、允许输入、
必需输出和失败信息一次性放入完整提示。阶段返回后更新状态并继续路由。

不得扫描或恢复其他任务的 subagent，不得删除已有产物，不得把失败阶段标成完成。
""", host)
    elif source.name == "abort-devflow.md":
        body = """# /abort-devflow

定位指定任务的 `workflow-state.json`，将 `summary.status` 更新为 `aborted`，记录时间
和用户给出的原因，并停止继续派发新阶段。保留状态和已有产物供审计；不得删除项目
文件或其他任务数据。当前宿主仍有对应 subagent 运行时，只停止该精确任务。
"""
    else:
        body = adapt(strip_frontmatter(raw), host)
    if host == "cursor":
        return body.rstrip() + "\n"
    lines = ["---"]
    if "description" in meta:
        lines.append("description: " + json.dumps(meta["description"], ensure_ascii=False))
    if "argument-hint" in meta:
        lines.append("argument-hint: " + json.dumps(meta["argument-hint"], ensure_ascii=False))
    lines.extend(["---", "", body.rstrip(), ""])
    return "\n".join(lines)


def render_agent(role: str, host: str) -> str:
    source = ROOT / ".codex/agents" / f"{role}.md"
    raw = source.read_text(encoding="utf-8")
    body = adapt(strip_frontmatter(raw), host)
    desc = description((ROOT / ".codebuddy/agents" / f"{role}.md").read_text(encoding="utf-8"), role)
    fields = [
        "---",
        f"name: devflow-{role}",
        "description: " + json.dumps(desc, ensure_ascii=False),
        "model: inherit",
    ]
    if host == "cursor":
        fields.append("readonly: false")
    else:
        fields.append("permissionMode: acceptEdits")
    fields.extend(["---", "", "<!-- generated-by: build-classic-hosts.py -->", body.rstrip(), ""])
    return "\n".join(fields)


def add_tree(files: Dict[str, bytes], source: Path, target_prefix: str, host: str) -> None:
    for path in sorted(source.rglob("*")):
        if not path.is_file() or "__pycache__" in path.parts or path.suffix == ".pyc":
            continue
        relative = path.relative_to(source)
        target = str(Path(target_prefix) / relative)
        data = path.read_bytes()
        if path.suffix in {".md", ".mdc", ".json", ".yaml", ".yml"}:
            try:
                data = adapt(data.decode("utf-8"), host).encode("utf-8")
            except UnicodeDecodeError:
                pass
        files[target] = data


def expected(host: str) -> Dict[str, bytes]:
    files: Dict[str, bytes] = {}
    for role in ROLES:
        files[f"agents/devflow-{role}.md"] = render_agent(role, host).encode("utf-8")
    for command in sorted((ROOT / ".codebuddy/commands").glob("*.md")):
        files[f"commands/{command.name}"] = render_command(command, host).encode("utf-8")
    add_tree(files, ROOT / ".codex/assets", "assets", host)
    add_tree(files, ROOT / ".codex/checklists", "checklists", host)
    add_tree(files, ROOT / ".codex/runtime", "runtime", host)
    add_tree(files, ROOT / ".codex/workflows", "workflows", host)
    add_tree(files, ROOT / ".codebuddy/skills", "skills", host)
    for rule in sorted((ROOT / ".codex/rules").glob("*.mdc")):
        suffix = ".mdc" if host == "cursor" else ".md"
        files[f"rules/{rule.stem}{suffix}"] = adapt(rule.read_text(encoding="utf-8"), host).encode("utf-8")
    host_name = "Cursor" if host == "cursor" else "Claude Code"
    readme = f"""# DevFlow Classic for {host_name}

本目录是由 `.codebuddy` 参考工作流和 Classic 公共契约生成的 {host_name} 宿主包。
它属于 Classic edition，不依赖 `skills/devflow` Portable edition。不要直接修改；
修改参考工作流或适配规则后运行 `python3 scripts/build-classic-hosts.py --write`。

## 常用命令

- 启动：`/start-devflow <需求>`
- 恢复：`/resume-devflow <task_slug>`
- 状态：`/status-devflow <task_slug>`
- 终止：`/abort-devflow <task_slug>`

单阶段命令见 `commands/`，状态和产物协议见 `runtime/`。
"""
    files["README.md"] = readme.encode("utf-8")
    marker = {"version": 1, "edition": "classic", "host": host, "generator": "scripts/build-classic-hosts.py"}
    files[".devflow-generated.json"] = (json.dumps(marker, ensure_ascii=False, indent=2) + "\n").encode("utf-8")
    return files


def safe_to_replace(target: Path) -> bool:
    if not target.exists():
        return True
    if (target / ".devflow-generated.json").is_file():
        return True
    entries = {item.name for item in target.iterdir()}
    return entries.issubset({"agents", "skills"})


def write_host(host: str) -> None:
    target = ROOT / f".{host}"
    if not safe_to_replace(target):
        raise SystemExit(f"拒绝覆盖非生成目录: {target}")
    if target.exists():
        shutil.rmtree(target)
    for relative, data in expected(host).items():
        destination = target / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(data)


def check_host(host: str) -> list:
    target = ROOT / f".{host}"
    wanted = expected(host)
    # __pycache__/*.pyc 是运行 Python 工具（比如 validate.sh 里的 py_compile）时的
    # 正常副作用，不是宿主包内容本身；add_tree() 生成期望清单时已经排除了同样的
    # 东西（见上面 134 行），这里扫描"实际有什么"必须用一样的排除规则，否则任何
    # 在 .claude/.cursor 下跑过一次 Python 语法检查就会把这些缓存文件误判成
    # "unmanaged extra"。
    actual = {
        str(path.relative_to(target)): path.read_bytes()
        for path in target.rglob("*")
        if path.is_file() and "__pycache__" not in path.parts and path.suffix != ".pyc"
    } if target.is_dir() else {}
    errors = []
    for name in sorted(set(wanted) | set(actual)):
        if name not in actual:
            errors.append(f".{host}/{name}: missing")
        elif name not in wanted:
            errors.append(f".{host}/{name}: unmanaged extra")
        elif actual[name] != wanted[name]:
            errors.append(f".{host}/{name}: drift")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description="生成或检查 DevFlow Classic Cursor/Claude 宿主包")
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--write", action="store_true")
    mode.add_argument("--check", action="store_true")
    args = parser.parse_args()
    if args.write:
        for host in HOSTS:
            write_host(host)
    errors = [error for host in HOSTS for error in check_host(host)]
    if errors:
        print("ERROR:\n- " + "\n- ".join(errors))
        return 1
    print("OK: Classic Cursor/Claude bundles match their generator")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
