from __future__ import annotations
"""项目 `.env` / `.env.local` 的轻量读取辅助。"""

import os
from pathlib import Path


def project_root(cwd: str | None = None) -> Path:
    """解析用于查找 `.env` 的项目根目录。"""
    if cwd:
        return Path(cwd).expanduser().resolve()
    env_cwd = str(os.environ.get("CODEBUDDY_PROJECT_DIR") or "").strip()
    if env_cwd:
        return Path(env_cwd).expanduser().resolve()
    return Path.cwd().resolve()


def read_env_file(path: Path) -> dict[str, str]:
    """以轻量方式解析 shell 风格的 env 文件，不做 source。"""
    values: dict[str, str] = {}
    if not path.is_file():
        return values
    try:
        lines = path.read_text("utf-8").splitlines()
    except Exception:
        return values

    for raw_line in lines:
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[7:].strip()
        if "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        value = value.strip()
        if not key:
            continue
        if value and value[0] == value[-1] and value[0] in {"'", '"'}:
            value = value[1:-1]
        values[key] = value
    return values
