#!/usr/bin/env python3
import argparse
from pathlib import Path

from agent_registry import HELPER_ROLES, role_body


SKILL_ROOT = Path(__file__).resolve().parent.parent


def main() -> int:
    parser = argparse.ArgumentParser(description="生成有边界的 DevFlow 检索助手提示")
    parser.add_argument("--role", choices=tuple(sorted(HELPER_ROLES)), required=True)
    parser.add_argument("--purpose", required=True)
    parser.add_argument("--scope", action="append", default=[])
    args = parser.parse_args()
    scopes = args.scope or ["仅限当前问题直接相关的项目文件或知识来源"]
    rules = (SKILL_ROOT / "rules/core.md").read_text(encoding="utf-8").strip()
    print(f"# DevFlow 检索任务\n\n- 角色：`{args.role}`\n- 问题：{args.purpose}")
    print("- 范围：")
    for scope in scopes:
        print(f"  - {scope}")
    print(f"\n## 强制规则\n\n{rules}")
    print(f"\n## 角色边界\n\n{role_body(args.role)}")
    print(
        "\n## 返回要求\n\n"
        "只返回与问题直接相关的事实、来源路径、未确认项和停止理由；"
        "不要修改文件、扩大范围、给出审查结论或继续实现。"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
