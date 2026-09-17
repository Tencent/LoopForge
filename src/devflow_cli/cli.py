import argparse
import difflib
import json
import shlex
import subprocess
import sys
from pathlib import Path
from typing import Dict, List

from . import __version__, launch
from .core import (
    STATE_PATH, DevFlowError, apply_install, build_plan, doctor, status_rows, uninstall,
)
from .editions import DEFAULT_EDITION, EDITIONS, HOSTS, entrypoint_for, host_label, source_for, spec
from .targets import load


PROGRAMS = ("loopforge", "lf")
COMMANDS = (
    "install", "update", "skills", "editions", "plan", "status", "uninstall", "doctor", "run",
)


def program_name() -> str:
    """控制台脚本名，`lf` 与 `loopforge` 共用同一入口。"""
    name = Path(sys.argv[0]).name
    return name if name in PROGRAMS else "loopforge"


def normalize_argv(argv: List[str]) -> List[str]:
    """`lf "需求"` 等价于 `lf run "需求"`。"""
    if not argv or argv[0] in COMMANDS or argv[0].startswith("-"):
        return argv
    close = difflib.get_close_matches(argv[0], COMMANDS, n=1, cutoff=0.7)
    if close:
        raise DevFlowError(
            f"未知子命令 {argv[0]!r}；是否想运行 `{program_name()} {close[0]}`？"
        )
    print(f"[{program_name()}] 未匹配到子命令，按 `run <需求>` 处理", file=sys.stderr)
    return ["run", *argv]


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(
        prog=program_name(),
        description="安装和维护 DevFlow Classic / Portable 工作流",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    result.add_argument("--version", action="version", version=__version__)
    sub = result.add_subparsers(dest="command", required=True)
    for command in ("install", "update"):
        item = sub.add_parser(
            command,
            help=("安装完整的宿主原生 Classic 工作流" if command == "install"
                  else "更新已安装的 Classic 工作流"),
            formatter_class=argparse.ArgumentDefaultsHelpFormatter,
        )
        item.add_argument("host", choices=HOSTS)
        item.add_argument(
            "--edition", choices=EDITIONS, default=DEFAULT_EDITION,
            help="Portable 使用 skills/；Classic 使用完整的宿主原生 .xxx/ 包",
        )
        item.add_argument("--project-root", type=Path, default=Path.cwd())
        item.add_argument(
            "--force", action="store_true",
            help="覆盖冲突文件或软链接；install 时可替换同宿主的另一 edition",
        )
    skills = sub.add_parser("skills", help="安装和维护 Portable Agent Skills")
    skills_sub = skills.add_subparsers(dest="skills_command", required=True)
    for command in ("install", "update"):
        item = skills_sub.add_parser(
            command,
            help=("安装 Portable Agent Skills" if command == "install"
                  else "更新 Portable Agent Skills"),
        )
        item.add_argument("host", choices=HOSTS)
        item.add_argument("--project-root", type=Path, default=Path.cwd())
        item.add_argument(
            "--force", action="store_true",
            help="覆盖冲突文件或软链接；install 时可替换同宿主的另一 edition",
        )
    item = skills_sub.add_parser("status", help="查看 Portable Agent Skills 状态")
    item.add_argument("host", choices=HOSTS, nargs="?")
    item.add_argument("--project-root", type=Path, default=Path.cwd())
    item.add_argument("--json", action="store_true")
    item = skills_sub.add_parser("uninstall", help="卸载 Portable Agent Skills")
    item.add_argument("host", choices=HOSTS)
    item.add_argument("--project-root", type=Path, default=Path.cwd())
    item = sub.add_parser("editions", help="显示两个 edition 的事实源和宿主入口")
    item.add_argument("--json", action="store_true")
    item = sub.add_parser(
        "plan", help="安装前只读预览，不写入项目",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    item.add_argument("host", choices=HOSTS)
    item.add_argument(
        "--edition", choices=EDITIONS, default=DEFAULT_EDITION,
        help="要预览的 edition",
    )
    item.add_argument("--json", action="store_true")
    item = sub.add_parser("status")
    item.add_argument("host", choices=HOSTS, nargs="?")
    item.add_argument("--project-root", type=Path, default=Path.cwd())
    item.add_argument("--json", action="store_true")
    item = sub.add_parser("uninstall")
    item.add_argument("host", choices=HOSTS)
    item.add_argument("--edition", choices=EDITIONS)
    item.add_argument("--project-root", type=Path, default=Path.cwd())
    item = sub.add_parser("doctor")
    item.add_argument("--project-root", type=Path, default=Path.cwd())
    item = sub.add_parser(
        "run",
        help="探测本机 Agent CLI 并带着工作流入口提示词在终端启动它",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    item.add_argument("requirement", nargs="*", help="需求描述；省略时只发送工作流入口")
    item.add_argument("--host", choices=HOSTS, help="指定宿主，跳过探测与选择")
    item.add_argument("--project-root", type=Path, default=Path.cwd())
    item.add_argument("--dry-run", action="store_true", help="只打印将执行的命令，不启动")
    item.add_argument("--json", action="store_true")
    return result


def not_installed_message(available: Dict[str, str]) -> str:
    lines = [f"当前项目尚未安装 LoopForge 工作流（未找到 {STATE_PATH}）"]
    if available:
        lines.append("本机检测到可用 Agent CLI: " + ", ".join(available))
        lines.append(f"请先运行: {program_name()} install {next(iter(available))}")
    else:
        lines.append("本机未检测到受支持的 Agent CLI；可选宿主: " + ", ".join(HOSTS))
        lines.append(f"请先运行: {program_name()} install <宿主>")
    return "\n".join(lines)


def missing_cli_message(hosts: List[str]) -> str:
    details = "；".join(
        f"{host} 尝试过 {', '.join(launch.tried_names(host))}"
        f"（可用 {launch.override_variable(host)} 指定）"
        for host in hosts
    )
    return (
        f"已安装工作流，但未检测到对应的终端 CLI。{details}。"
        "也可以直接打开对应宿主，在聊天框里发送工作流入口提示词。"
    )


def ambiguous_message(candidates: List[launch.Candidate]) -> str:
    listing = ", ".join(item.host for item in candidates)
    return (
        f"检测到多个可用宿主（{listing}），当前不是交互终端无法选择；"
        f"请用 {program_name()} run --host <宿主> 指定"
    )


def print_plan(candidates: List[launch.Candidate], requirement: str, args) -> None:
    rows = [
        {
            "host": item.host,
            "edition": item.edition,
            "cli": item.cli,
            "prompt": launch.build_prompt(item, requirement),
            "argv": launch.build_argv(item, requirement),
            "command": shlex.join(launch.build_argv(item, requirement)),
        }
        for item in candidates
    ]
    if args.json:
        print(json.dumps(
            {"selected": rows[0] if len(rows) == 1 else None, "candidates": rows},
            ensure_ascii=False, indent=2,
        ))
        return
    for index, row in enumerate(rows, start=1):
        prefix = "" if len(rows) == 1 else f"[{index}] "
        print(f"{prefix}host={row['host']} edition={row['edition']} cli={row['cli']}")
        print(f"{prefix}prompt={row['prompt']}")
        print(f"{prefix}command={row['command']}")
    if len(rows) > 1:
        print(f"将提示选择要启动的宿主，也可用 {program_name()} run --host <宿主> 指定。")


def select_host(
    host: str, rows: List[dict], candidates: List[launch.Candidate],
) -> launch.Candidate:
    installed = next((row for row in rows if row["host"] == host), None)
    if installed is None:
        known = ", ".join(sorted(row["host"] for row in rows))
        raise DevFlowError(
            f"项目未安装 {host} 工作流（已安装: {known}）；"
            f"请先运行 {program_name()} install {host}"
        )
    candidate = next((item for item in candidates if item.host == host), None)
    if candidate is None:
        raise DevFlowError(missing_cli_message([host]))
    return candidate


def choose_host(candidates: List[launch.Candidate]) -> launch.Candidate:
    print("检测到多个可用宿主，请选择要启动的宿主：")
    for index, item in enumerate(candidates, start=1):
        print(
            f"  {index}) {host_label(item.host)} ({item.host}) edition={item.edition} "
            f"入口={launch.build_prompt(item)} cli={item.cli}"
        )
    while True:
        answer = input(f"请输入序号 [1-{len(candidates)}]（回车默认 1）: ").strip()
        if not answer:
            return candidates[0]
        if answer.isdigit() and 1 <= int(answer) <= len(candidates):
            return candidates[int(answer) - 1]
        print(f"请输入 1 到 {len(candidates)} 之间的序号。")


def run_command(args) -> int:
    project = args.project_root.resolve()
    requirement = " ".join(args.requirement).strip()
    rows = status_rows(project)
    if not rows:
        raise DevFlowError(not_installed_message(launch.detected_hosts()))
    candidates = launch.candidates(project)
    missing = [row["host"] for row in rows if not launch.resolve_cli(row["host"])]

    if args.host is not None:
        candidate = select_host(args.host, rows, candidates)
    elif not candidates:
        raise DevFlowError(missing_cli_message(missing))
    elif len(candidates) == 1:
        candidate = candidates[0]
    elif args.dry_run or args.json:
        print_plan(candidates, requirement, args)
        return 0
    elif not sys.stdin.isatty():
        raise DevFlowError(ambiguous_message(candidates))
    else:
        candidate = choose_host(candidates)

    row = next(item for item in rows if item["host"] == candidate.host)
    if row["changed"] or row["missing"]:
        print(
            f"提示: {candidate.host} 的工作流文件有改动或缺失 "
            f"(changed={row['changed']} missing={row['missing']})，"
            f"可用 {program_name()} status {candidate.host} 查看",
            file=sys.stderr,
        )
    if args.dry_run or args.json:
        print_plan([candidate], requirement, args)
        return 0
    return launch.launch(launch.build_argv(candidate, requirement))


def main(argv=None) -> int:
    argv = list(sys.argv[1:] if argv is None else argv)
    try:
        args = parser().parse_args(normalize_argv(argv))
        if args.command == "run":
            return run_command(args)
        if args.command == "editions":
            rows = []
            for edition in EDITIONS:
                metadata = spec(edition)
                rows.append({
                    "edition": edition,
                    "default": edition == DEFAULT_EDITION,
                    "description": metadata["description"],
                    "source_roots": list(metadata["source_roots"]),
                    "entrypoints": metadata["entrypoints"],
                })
            if args.json:
                print(json.dumps(rows, ensure_ascii=False, indent=2))
            else:
                for row in rows:
                    default = " (default)" if row["default"] else ""
                    print(f"{row['edition']}{default}: {row['description']}")
                    print("  sources: " + ", ".join(row["source_roots"]))
                    print("  entries: " + ", ".join(
                        f"{host}={entry}" for host, entry in row["entrypoints"].items()
                    ))
            return 0
        if args.command == "plan":
            files = sorted(build_plan(args.host, args.edition))
            row = {
                "edition": args.edition,
                "host": args.host,
                "source": source_for(args.edition, args.host),
                "entrypoint": entrypoint_for(args.edition, args.host),
                "files": files,
            }
            if args.json:
                print(json.dumps(row, ensure_ascii=False, indent=2))
            else:
                print(
                    f"edition={args.edition} host={args.host} source={row['source']} "
                    f"entry={row['entrypoint']} files={len(files)}"
                )
                for path in files:
                    print(f"  {path}")
            return 0
        if args.command in {"install", "update"}:
            target = load(args.host)
            project_root = args.project_root.resolve()
            installed, unchanged = apply_install(
                project_root,
                target.HOST,
                args.edition,
                update_only=args.command == "update",
                force=args.force,
            )
            print(
                f"OK: {args.command} edition={args.edition} host={args.host} "
                f"project={project_root} written={installed} unchanged={unchanged}"
            )
            print(
                f"Next: reopen {host_label(args.host)} and run "
                f"{entrypoint_for(args.edition, args.host)} <你的需求>"
            )
            return 0
        if args.command == "skills":
            if args.skills_command in {"install", "update"}:
                target = load(args.host)
                project_root = args.project_root.resolve()
                installed, unchanged = apply_install(
                    project_root,
                    target.HOST,
                    "portable",
                    update_only=args.skills_command == "update",
                    force=args.force,
                )
                print(
                    f"OK: skills {args.skills_command} host={args.host} "
                    f"project={project_root} written={installed} unchanged={unchanged}"
                )
                print(
                    f"Next: reopen {host_label(args.host)} and run "
                    f"{entrypoint_for('portable', args.host)} <你的需求>"
                )
                return 0
            if args.skills_command == "status":
                rows = [
                    row for row in status_rows(args.project_root, args.host)
                    if row["edition"] == "portable"
                ]
                if args.json:
                    print(json.dumps(rows, ensure_ascii=False, indent=2))
                elif not rows:
                    print("skills not installed")
                else:
                    for row in rows:
                        print(
                            f"skills/{row['host']}: files={row['files']} "
                            f"changed={row['changed']} missing={row['missing']}"
                        )
                return 0
            removed, preserved = uninstall(args.project_root, args.host, "portable")
            print(
                f"OK: skills uninstall host={args.host} "
                f"removed={removed} preserved={len(preserved)}"
            )
            if preserved:
                print("Preserved user-modified files: " + ", ".join(preserved))
            return 0
        if args.command == "status":
            rows = status_rows(args.project_root, args.host)
            if args.json:
                print(json.dumps(rows, ensure_ascii=False, indent=2))
            elif not rows:
                print("not installed")
            else:
                for row in rows:
                    print(
                        f"{row['edition']}/{row['host']}: files={row['files']} "
                        f"changed={row['changed']} missing={row['missing']}"
                    )
            return 0
        if args.command == "uninstall":
            removed, preserved = uninstall(args.project_root, args.host, args.edition)
            print(f"OK: uninstall host={args.host} removed={removed} preserved={len(preserved)}")
            if preserved:
                print("Preserved user-modified files: " + ", ".join(preserved))
            return 0
        problems = doctor(args.project_root)
        if problems:
            print("ERROR:\n- " + "\n- ".join(problems))
            return 1
        print("OK: assets and installations are healthy")
        return 0
    except (DevFlowError, subprocess.CalledProcessError) as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
