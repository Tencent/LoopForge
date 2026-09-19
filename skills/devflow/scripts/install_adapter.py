#!/usr/bin/env python3
import argparse
import json
import os
import shutil
from pathlib import Path
from typing import Any, Dict, List


SKILL_ROOT = Path(__file__).resolve().parent.parent
MANAGED_PREFIX = "managed-by: devflow-"
MANAGED_SKILLS_FILE = ".devflow-managed-skills.json"
SKILLS_MANIFEST = SKILL_ROOT.parent / "manifest.json"


def yaml_value(value: Any) -> str:
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, (int, float)):
        return str(value)
    if isinstance(value, (dict, list)):
        return json.dumps(value, ensure_ascii=False)
    return json.dumps(str(value), ensure_ascii=False)


def load_config(adapter_id: str) -> Dict[str, Any]:
    path = SKILL_ROOT / "adapters" / adapter_id / "manifest.json"
    if not path.is_file():
        raise SystemExit(f"未知 adapter: {adapter_id}")
    config = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(config.get("installation"), dict):
        raise SystemExit(f"{adapter_id} 没有声明 installation")
    return config


def load_skill_set(name: str) -> List[str]:
    data = json.loads(SKILLS_MANIFEST.read_text(encoding="utf-8"))
    skills = data.get("skill_sets", {}).get(name)
    if not isinstance(skills, list) or not skills or not all(isinstance(item, str) for item in skills):
        raise SystemExit(f"未知或无效的 Skill 集合: {name}")
    return skills


def render_agent(adapter_id: str, adapter_root: Path, spec: dict) -> str:
    fields = {"name": spec["id"], "description": spec["description"]}
    fields.update(spec.get("frontmatter", {}))
    body_path = (adapter_root / spec.get("body", f"agents/{spec['file']}")).resolve()
    adapters_root = (SKILL_ROOT / "adapters").resolve()
    if adapters_root not in body_path.parents or not body_path.is_file():
        raise ValueError(f"宿主 Agent 正文路径无效: {body_path}")
    body = body_path.read_text(encoding="utf-8").strip()
    frontmatter = [f"{key}: {yaml_value(value)}" for key, value in fields.items()]
    return "\n".join([
        "---",
        *frontmatter,
        "---",
        "",
        f"<!-- {MANAGED_PREFIX}{adapter_id}-adapter -->",
        body,
        "",
    ])


def install_agents(
    adapter_id: str, adapter_root: Path, target: Path, specs: List[dict], refresh: bool
) -> tuple:
    target.mkdir(parents=True, exist_ok=True)
    marker = f"{MANAGED_PREFIX}{adapter_id}-adapter"
    expected = {f"{spec['id']}.md" for spec in specs}
    installed = preserved = removed = 0
    if refresh:
        for destination in target.glob("*.md"):
            if destination.name not in expected and marker in destination.read_text(
                encoding="utf-8", errors="ignore"
            ):
                destination.unlink()
                removed += 1
    for spec in specs:
        destination = target / f"{spec['id']}.md"
        if destination.exists() and not (
            refresh and marker in destination.read_text(encoding="utf-8", errors="ignore")
        ):
            preserved += 1
            continue
        destination.write_text(render_agent(adapter_id, adapter_root, spec), encoding="utf-8")
        installed += 1
    return installed, preserved, removed


def load_managed_skills(host_root: Path, adapter_id: str) -> Dict[str, str]:
    path = host_root / MANAGED_SKILLS_FILE
    if not path.is_file():
        return {}
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, OSError):
        return {}
    if data.get("adapter") != adapter_id:
        return {}
    skills = data.get("skills", {})
    return skills if isinstance(skills, dict) else {}


def save_managed_skills(host_root: Path, adapter_id: str, skills: Dict[str, str]) -> None:
    path = host_root / MANAGED_SKILLS_FILE
    payload = {"version": 1, "adapter": adapter_id, "skills": dict(sorted(skills.items()))}
    path.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def install_skills(
    adapter_id: str,
    host_root: Path,
    target: Path,
    names: List[str],
    refresh: bool,
    copy_skills: bool,
) -> List[str]:
    target.mkdir(parents=True, exist_ok=True)
    if copy_skills:
        shutil.copy2(SKILLS_MANIFEST, target / "manifest.json")
    managed = load_managed_skills(host_root, adapter_id)
    results = []
    for name in names:
        source = SKILL_ROOT.parent / name
        if not (source / "SKILL.md").is_file():
            results.append(f"{name}=source-missing")
            continue
        destination = target / name
        if destination.exists() or destination.is_symlink():
            mode = managed.get(name)
            if not refresh or mode not in {"linked", "copied"}:
                results.append(f"{name}=preserved")
                continue
            if destination.is_symlink():
                destination.unlink()
            elif destination.is_dir():
                shutil.rmtree(destination)
            else:
                results.append(f"{name}=preserved-nondirectory")
                continue
        if copy_skills:
            shutil.copytree(source, destination, ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))
            results.append(f"{name}=copied")
            managed[name] = "copied"
            continue
        try:
            relative = os.path.relpath(source.resolve(), start=destination.parent.resolve())
            destination.symlink_to(relative, target_is_directory=True)
            results.append(f"{name}=linked")
            managed[name] = "linked"
        except OSError:
            shutil.copytree(source, destination, ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))
            results.append(f"{name}=copied")
            managed[name] = "copied"
    save_managed_skills(host_root, adapter_id, managed)
    return results


def main() -> int:
    parser = argparse.ArgumentParser(description="安装 DevFlow 宿主 adapter")
    parser.add_argument("--adapter", required=True)
    parser.add_argument("--project-root", type=Path, required=True)
    parser.add_argument("--refresh-managed", action="store_true")
    parser.add_argument(
        "--copy-skills",
        action="store_true",
        help="复制 Skill 而不是创建相对软链接；后续用 --refresh-managed 安全刷新",
    )
    args = parser.parse_args()
    project_root = args.project_root.resolve()
    if not project_root.is_dir():
        raise SystemExit("项目根目录不存在")
    config = load_config(args.adapter)
    install = config["installation"]
    adapter_root = SKILL_ROOT / "adapters" / args.adapter
    host_root = project_root / install["project_dir"]
    installed, preserved, removed = install_agents(
        args.adapter,
        adapter_root,
        host_root / install["agents_dir"],
        config.get("host_agents", []),
        args.refresh_managed,
    )
    skills = install_skills(
        args.adapter,
        host_root,
        host_root / install["skills_dir"],
        load_skill_set(install["skill_set"]),
        args.refresh_managed,
        args.copy_skills,
    )
    print(
        f"OK: adapter={args.adapter} agents_installed={installed} "
        f"agents_preserved={preserved} legacy_managed_removed={removed} "
        + " ".join(skills)
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
