#!/usr/bin/env python3
import argparse
import re
import shutil
from pathlib import Path


TEAM_NAME = re.compile(r"multi-agents-devflow-[A-Za-z0-9][A-Za-z0-9._-]*")


def cleanup(project_root: Path, team_name: str) -> str:
    if not TEAM_NAME.fullmatch(team_name) or ".." in team_name:
        raise ValueError("unsafe DevFlow Team name")
    team_path = project_root.resolve() / ".codebuddy" / "teams" / team_name
    if team_path.is_symlink():
        team_path.unlink()
        return "deleted"
    if team_path.exists():
        if not team_path.is_dir():
            raise ValueError("Team path is not a directory")
        shutil.rmtree(team_path)
        return "deleted"
    return "absent"


def main() -> int:
    parser = argparse.ArgumentParser(description="删除一个确定名称的 CodeBuddy DevFlow Team")
    parser.add_argument("--project-root", type=Path, required=True)
    parser.add_argument("--team-name", required=True)
    args = parser.parse_args()
    print(f"team={args.team_name} result={cleanup(args.project_root, args.team_name)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
