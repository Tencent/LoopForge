#!/usr/bin/env python3
import argparse
import json
import os
import subprocess
from pathlib import Path


MANIFESTS = (
    "package.json", "pnpm-workspace.yaml", "pyproject.toml", "requirements.txt",
    "go.mod", "Cargo.toml", "pom.xml", "build.gradle", "build.gradle.kts",
    "Gemfile", "composer.json", "Makefile",
)
INSTRUCTIONS = (
    "AGENTS.md", "CLAUDE.md", ".cursorrules", ".github/copilot-instructions.md",
)
IGNORED_DIRECTORIES = {".git", "node_modules", "vendor", "dist", "build", ".venv", "venv"}


def discover(root: Path, names: tuple, max_depth: int = 4) -> list:
    found = []
    for current, directories, files in os.walk(root):
        current_path = Path(current)
        depth = len(current_path.relative_to(root).parts)
        directories[:] = [
            name for name in directories
            if name not in IGNORED_DIRECTORIES and depth < max_depth
        ]
        for name in files:
            if name in names:
                found.append(str((current_path / name).relative_to(root)))
    return sorted(set(found))


def git_info(root: Path) -> dict:
    result = subprocess.run(
        ["git", "-C", str(root), "rev-parse", "--show-toplevel"],
        text=True, capture_output=True, check=False,
    )
    if result.returncode != 0:
        return {"is_repository": False, "root": None, "current_directory_is_root": False}
    detected = Path(result.stdout.strip()).resolve()
    return {
        "is_repository": True,
        "root": str(detected),
        "current_directory_is_root": detected == root,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="Inspect, but do not constrain, the current project directory")
    parser.add_argument("project_root", nargs="?", default=".", type=Path)
    args = parser.parse_args()
    root = args.project_root.resolve()
    if not root.is_dir():
        print(json.dumps({"ok": False, "error": "execution directory does not exist", "project_root": str(root)}, ensure_ascii=False))
        return 1

    output = {
        "ok": True,
        "project_root": str(root),
        "git": git_info(root),
        "instructions": [name for name in INSTRUCTIONS if (root / name).is_file()],
        "manifests": discover(root, MANIFESTS),
    }
    print(json.dumps(output, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
