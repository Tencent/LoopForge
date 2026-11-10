"""Hermes Agent command plugin for LoopForge Local.

The plugin deliberately keeps its state in the current project.  It calls the
same ``workflow_state.py`` contract as the ``devflow`` Skill and never reads or
writes Hermes' profile configuration.
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from pathlib import Path
from typing import List, Optional, Tuple


_SAFE_SLUG = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$")
_APPROVAL_STAGES = {"REQUIREMENT", "DESIGN", "REVIEW", "TEST"}
_MAX_RESULT_FILES = 40


def _project_root() -> Path:
    """Resolve the Hermes session project without touching Hermes config.

    Hermes normally starts from the active project directory.  The explicit
    environment override is useful for gateway hosts that keep a different
    process working directory and makes the behavior testable.
    """

    override = os.environ.get("LOOPFORGE_PROJECT_ROOT")
    if override:
        return Path(override).resolve()
    try:
        # Hermes documents this as the public way for plugins to access the
        # invoking session's working directory.  The gateway process cwd is
        # the Hermes home and is therefore not a safe fallback.
        from agent.runtime_cwd import resolve_agent_cwd

        return Path(resolve_agent_cwd()).resolve()
    except (ImportError, RuntimeError, TypeError):
        return Path(os.getcwd()).resolve()


def _workflow_script(root: Path) -> Path:
    path = root / ".hermes" / "skills" / "devflow" / "scripts" / "workflow_state.py"
    if not path.is_file():
        raise RuntimeError(
            "LoopForge Skill is not installed in this project; run "
            "`loopforge skills install hermes` first."
        )
    return path


def _run(root: Path, script: Path, *args: str) -> str:
    result = subprocess.run(
        [sys.executable, str(script), *args],
        cwd=str(root),
        capture_output=True,
        text=True,
        check=False,
    )
    output = (result.stdout or result.stderr).strip()
    if result.returncode:
        raise RuntimeError(output or f"workflow_state.py exited with {result.returncode}")
    return output


def _state_files(root: Path) -> List[Path]:
    artifacts = root / "artifacts"
    if not artifacts.is_dir():
        return []
    return sorted(
        (
            path
            for path in artifacts.glob("*/workflow-state.json")
            if path.is_file()
        ),
        key=lambda path: path.stat().st_mtime_ns,
        reverse=True,
    )


def _record(root: Path, slug: Optional[str] = None) -> Optional[Tuple[Path, dict]]:
    if slug is not None and not _SAFE_SLUG.fullmatch(slug):
        raise RuntimeError("Task slug may contain only letters, digits, dots, hyphens, and underscores.")
    path = root / "artifacts" / slug / "workflow-state.json" if slug else None
    if path is None:
        files = _state_files(root)
        path = files[0] if files else None
    if path is None or not path.is_file():
        return None
    try:
        return path, json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise RuntimeError(f"Cannot read LoopForge state: {path}: {exc}") from exc


def _render(record: Optional[Tuple[Path, dict]]) -> str:
    if record is None:
        return "No LoopForge task found in this project."
    path, state = record
    completed = [
        name
        for name, stage in (state.get("stages") or {}).items()
        if stage.get("status") == "completed"
    ]
    lines = [
        f"Task: {state.get('task_slug', 'unknown')}",
        f"Status: {state.get('status', 'unknown')}",
        f"Size: {state.get('size_class', 'unknown')}",
        f"State: {path}",
    ]
    if completed:
        lines.append("Completed stages: " + ", ".join(completed))
    if state.get("awaiting_approval"):
        lines.append(f"Awaiting approval: {state['awaiting_approval']}")
    if state.get("next_stage"):
        lines.append(f"Next stage: {state['next_stage']}")
    return "\n".join(lines)


def _result_files(root: Path, slug: str) -> List[str]:
    artifact_root = root / "artifacts" / slug
    if not artifact_root.is_dir():
        return []
    return sorted(
        str(path.relative_to(artifact_root))
        for path in artifact_root.rglob("*")
        if path.is_file()
    )[:_MAX_RESULT_FILES]


def _parse(raw: str) -> Tuple[str, str]:
    value = raw.strip()
    match = re.match(r"^(start|status|result|approve|help)(?:\s+([\s\S]*))?$", value)
    return (match.group(1), (match.group(2) or "").strip()) if match else ("help", "")


def _handle(raw: str) -> str:
    command, value = _parse(raw)
    root = _project_root()
    if command == "help":
        return (
            "Usage: /loopforge start <request> | status [slug] | result [slug] | "
            "approve [stage] | help"
        )
    if command == "status":
        return _render(_record(root, value or None))
    record = _record(root, value or None) if command == "result" else _record(root)
    if command == "result":
        if record is None:
            return "No LoopForge task found in this project."
        _path, state = record
        files = _result_files(root, state.get("task_slug", ""))
        listing = "\n\nArtifact files:\n" + "\n".join(f"- {item}" for item in files)
        return _render(record) + (listing if files else "\n\nNo artifact files have been written yet.")
    script = _workflow_script(root)
    if command == "approve":
        if record is None:
            return "No LoopForge task is waiting for approval."
        state_path, state = record
        stage = value.upper() if value else state.get("awaiting_approval")
        if not stage:
            return "No approval stage is pending."
        if stage not in _APPROVAL_STAGES:
            return "Unknown approval stage. Use REQUIREMENT, DESIGN, REVIEW, or TEST."
        args = ["approve", "--state", str(state_path), "--stage", stage]
        if stage == "REQUIREMENT":
            args.append("--user-confirmed")
        _run(root, script, *args)
        return f"Approved {stage}. Resume the Hermes workflow to continue."
    if not value:
        return "A request is required: /loopforge start <request>"
    slug = re.sub(r"[^a-z0-9]+", "-", value.lower()).strip("-")[:48] or "hermes-task"
    state_path = root / "artifacts" / slug / "workflow-state.json"
    output = _run(
        root,
        script,
        "init",
        "--project-root",
        str(root),
        "--slug",
        slug,
        "--size",
        "medium",
        "--mode",
        "auto",
        "--execution-mode",
        "isolated",
        "--host-adapter",
        "hermes",
        "--coordinator-id",
        "hermes-main",
    )
    return (
        f"LoopForge task initialized: {slug}\nRequest: {value}\nState: {state_path}\n"
        f"{output}\nExecution mode: the Hermes adapter will use delegate_task when available "
        "and record a single-context fallback with its reason when it is not. "
        "Continue with /devflow or resume the task in Hermes."
    )


def register(ctx) -> None:
    """Register the command on Hermes' documented PluginContext surface."""

    ctx.register_command(
        "loopforge",
        _handle,
        description="Start, inspect, and approve LoopForge Local workflows",
        args_hint="[start <request>|status [slug]|result [slug]|approve [stage]|help]",
    )
