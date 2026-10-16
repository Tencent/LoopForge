#!/usr/bin/env python3
import argparse
import re
from pathlib import Path
from typing import Dict, List

from validate_artifacts import find_section, sections, validate_file


HEADINGS: Dict[str, List[str]] = {
    "scope": ["上下文与范围", "scope", "范围", "file scope", "文件范围"],
    "tasks": ["执行任务", "tasks", "任务", "implementation", "实施步骤"],
    "validation": ["验证与验收", "validation", "verification", "验证", "验收命令"],
    "risks": ["风险、恢复与回滚", "risks", "rollback", "风险", "回滚"],
}
COMMAND_PREFIXES = {
    "npm", "npx", "pnpm", "yarn", "bun", "node", "python", "python3", "pytest",
    "go", "cargo", "mvn", "gradle", "make", "just", "dotnet", "ruby", "bundle",
    "composer", "php", "git", "bash", "sh", "java", "swift", "xcodebuild",
    "curl", "wget", "docker", "kubectl", "helm", "terraform", "ansible",
}


def contains_command(text: str) -> bool:
    fenced = re.findall(r"```(?:bash|sh|shell|zsh|powershell|cmd)?\s*\n([\s\S]*?)```", text, re.IGNORECASE)
    for block in fenced:
        commands = [
            line.strip() for line in block.splitlines()
            if line.strip() and not line.lstrip().startswith(("#", "//", "REM "))
        ]
        if commands:
            return True
    for value in re.findall(r"`([^`\n]+)`", text):
        command = value.strip()
        first = command.split(maxsplit=1)[0]
        if first in COMMAND_PREFIXES or first.startswith(("./", ".\\")):
            return True
    return False


def validate_execution_plan(plan: Path, require_pending: bool = False) -> tuple:
    label = "02-design/execution-plan.md"
    if not plan.is_file():
        return [f"计划不存在: {plan}"], {}
    text = plan.read_text(encoding="utf-8")
    artifact_errors = validate_file(plan, "02-design/execution-plan.md")
    items = sections(text)
    found = {key: find_section(items, aliases) for key, aliases in HEADINGS.items()}
    errors = list(artifact_errors)
    errors.extend(f"{label}: 缺少计划必要内容：{key}" for key, value in found.items() if value is None)
    for key, value in found.items():
        if value is not None and not value[1].strip():
            errors.append(f"{label}: 计划内容为空：{key}")

    scope_body = found["scope"][1] if found["scope"] else ""
    if not re.findall(r"`([^`\n]+)`", scope_body):
        errors.append(f"{label}: 上下文与范围中没有明确的目标文件或目录")

    task_matches = list(re.finditer(r"^###\s+任务\s+[^\n]+$", text, re.MULTILINE))
    tasks = []
    for index, match in enumerate(task_matches):
        end = task_matches[index + 1].start() if index + 1 < len(task_matches) else len(text)
        tasks.append((match.group(0), text[match.end():end]))
    if not tasks:
        errors.append(f"{label}: 执行任务中没有符合模板的 `### 任务 <ID>：<名称>`")
    for title, body in tasks:
        target = re.search(r"^-\s*目标文件：\s*`([^`\n]+)`", body, re.MULTILINE)
        status = re.search(r"^-\s*状态：\s*`(pending|in_progress|completed|blocked)`", body, re.MULTILINE)
        if not target:
            errors.append(f"{label}: 任务没有 `- 目标文件：`：{title}")
        if not status:
            errors.append(f"{label}: 任务没有有效 `- 状态：`：{title}")
    pending = [title for title, body in tasks if re.search(r"^-\s*状态：\s*`pending`", body, re.MULTILINE)]
    completed = [title for title, body in tasks if re.search(r"^-\s*状态：\s*`completed`", body, re.MULTILINE)]
    if require_pending and not pending:
        errors.append(f"{label}: 执行任务中没有状态为 `pending` 的任务")

    validation_body = found["validation"][1] if found["validation"] else ""
    if not contains_command(validation_body):
        errors.append(f"{label}: 验证与验收部分没有可执行命令")

    metrics = {
        "scope_targets": len(re.findall(r"`([^`]+)`", scope_body)),
        "pending": len(pending),
        "completed": len(completed),
    }
    return errors, metrics


def main() -> int:
    parser = argparse.ArgumentParser(description="校验 DevFlow 执行计划")
    parser.add_argument("plan", type=Path)
    parser.add_argument("--require-pending", action="store_true")
    args = parser.parse_args()
    errors, metrics = validate_execution_plan(args.plan, args.require_pending)
    if errors:
        print("ERROR:\n- " + "\n- ".join(errors))
        return 1
    print(
        f"OK: scope_targets={metrics['scope_targets']} "
        f"pending={metrics['pending']} completed={metrics['completed']}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
