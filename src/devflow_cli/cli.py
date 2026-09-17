import argparse
import json
import subprocess
import sys
from pathlib import Path

from . import __version__
from .core import DevFlowError, apply_install, build_plan, doctor, status_rows, uninstall, validate_choice
from .editions import DEFAULT_EDITION, EDITIONS, HOSTS, entrypoint_for, host_label, source_for, spec
from .targets import load


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(
        prog="loopforge",
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
    return result


def main(argv=None) -> int:
    args = parser().parse_args(argv)
    try:
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
            validate_choice(args.host, args.edition)
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
            if args.host == "pi" and args.edition == "portable":
                print("Note: Pi isolated mode also needs the public extension: pi install npm:pi-subagents")
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
                if args.host == "pi":
                    print("Note: Pi isolated mode also needs the public extension: pi install npm:pi-subagents")
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
