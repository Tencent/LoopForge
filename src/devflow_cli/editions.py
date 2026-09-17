"""Canonical metadata for the two independently maintained DevFlow editions."""

from typing import Dict, Tuple


DEFAULT_EDITION = "classic"
HOSTS: Tuple[str, ...] = ("codebuddy", "codex", "cursor", "claude")
HOST_LABELS = {
    "codebuddy": "CodeBuddy",
    "codex": "Codex",
    "cursor": "Cursor",
    "claude": "Claude Code",
}
HOST_PRIORITY: Tuple[str, ...] = ("claude", "codex", "cursor", "codebuddy")

# 入口提示词与需求之间需要额外关键字的宿主；Codex 用
# `$devflow-codex start|status|resume|abort` 区分动作，启动必须带 start。
LAUNCH_SUBCOMMANDS: Dict[Tuple[str, str], Tuple[str, ...]] = {
    ("classic", "codex"): ("start",),
}

EDITION_SPECS: Dict[str, dict] = {
    "portable": {
        "description": "Agent Skills based portable workflow",
        "source_roots": ("skills/",),
        "entrypoints": {
            "codebuddy": "/devflow",
            "codex": "$devflow",
            "cursor": "/devflow",
            "claude": "/devflow",
        },
    },
    "classic": {
        "description": "Full host-native workflow derived from .codebuddy behavior",
        "source_roots": (".codebuddy/", ".codex/", ".cursor/", ".claude/"),
        "entrypoints": {
            "codebuddy": "/start-devflow",
            "codex": "$devflow-codex",
            "cursor": "/start-devflow",
            "claude": "/start-devflow",
        },
    },
}

EDITIONS: Tuple[str, ...] = tuple(EDITION_SPECS)


def spec(edition: str) -> dict:
    return EDITION_SPECS[edition]


def source_for(edition: str, host: str) -> str:
    if edition == "classic":
        return f".{host}/"
    return "skills/"


def entrypoint_for(edition: str, host: str) -> str:
    return str(spec(edition)["entrypoints"][host])


def launch_args(edition: str, host: str) -> Tuple[str, ...]:
    """入口提示词之后、需求之前需要插入的关键字。"""
    return LAUNCH_SUBCOMMANDS.get((edition, host), ())


def host_label(host: str) -> str:
    return str(HOST_LABELS[host])
