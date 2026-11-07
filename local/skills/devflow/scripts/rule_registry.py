#!/usr/bin/env python3
import json
import os
import re
from pathlib import Path
from typing import Dict, Iterable, List, Optional, Sequence, Tuple


SKILL_ROOT = Path(__file__).resolve().parent.parent
RULES_ROOT = SKILL_ROOT / "rules"
MANIFEST_PATH = RULES_ROOT / "manifest.json"
IGNORED_DIRECTORIES = {
    ".git", ".idea", ".vscode", "node_modules", "vendor", "dist", "build",
    ".venv", "venv", "coverage", "artifacts",
}


def _rule_path(relative: str) -> Path:
    path = (RULES_ROOT / relative).resolve()
    if SKILL_ROOT.resolve() not in (path, *path.parents):
        raise ValueError(f"规则文件越出 Skill 根目录: {relative}")
    return path


def load_manifest() -> dict:
    data = json.loads(MANIFEST_PATH.read_text(encoding="utf-8"))
    if not isinstance(data.get("version"), str) or not data["version"]:
        raise ValueError("规则清单缺少 version")
    stage_rules = data.get("stage_rules")
    profiles = data.get("profiles")
    if not isinstance(stage_rules, dict) or not isinstance(profiles, list):
        raise ValueError("规则清单缺少 stage_rules 或 profiles")
    ids = set()
    for relative in stage_rules.values():
        if not _rule_path(relative).is_file():
            raise ValueError(f"阶段规则不存在: {relative}")
    for profile in profiles:
        required = {"id", "title", "file", "stages", "signals"}
        if not isinstance(profile, dict) or not required.issubset(profile):
            raise ValueError("专项规则定义不完整")
        if profile["id"] in ids:
            raise ValueError(f"专项规则 ID 重复: {profile['id']}")
        ids.add(profile["id"])
        if not _rule_path(profile["file"]).is_file():
            raise ValueError(f"专项规则文件不存在: {profile['file']}")
        if not isinstance(profile["stages"], list) or not isinstance(profile["signals"], dict):
            raise ValueError(f"专项规则 stages/signals 无效: {profile['id']}")
    return data


MANIFEST = load_manifest()
PROFILES: Dict[str, dict] = {item["id"]: item for item in MANIFEST["profiles"]}


def stage_rule(stage: str) -> str:
    relative = MANIFEST["stage_rules"].get(stage)
    if relative is None:
        raise ValueError(f"阶段没有规则定义: {stage}")
    return _rule_path(relative).read_text(encoding="utf-8").strip()


def _walk_project(root: Path, max_depth: int = 4) -> Iterable[Path]:
    for current, directories, files in os.walk(root):
        current_path = Path(current)
        depth = len(current_path.relative_to(root).parts)
        directories[:] = [
            name for name in directories
            if name not in IGNORED_DIRECTORIES and depth < max_depth
        ]
        for name in files:
            yield current_path / name


def _package_dependencies(root: Path, paths: Sequence[Path]) -> set:
    dependencies = set()
    for path in paths:
        if path.name != "package.json" or path.parent != root:
            continue
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, UnicodeDecodeError, json.JSONDecodeError):
            continue
        for key in ("dependencies", "devDependencies", "peerDependencies"):
            values = data.get(key, {})
            if isinstance(values, dict):
                dependencies.update(str(name).lower() for name in values)
    return dependencies


def _evidence_text(request: str, upstream_paths: Sequence[Path]) -> str:
    chunks = [request]
    for path in upstream_paths:
        try:
            chunks.append(path.read_text(encoding="utf-8")[:200_000])
        except (OSError, UnicodeDecodeError):
            continue
    return "\n".join(chunks).lower()


def _referenced_project_file(
    project_root: Path, project_files: Sequence[Path], evidence_text: str, extensions: set
) -> Optional[Path]:
    for path in project_files:
        if path.suffix.lower() not in extensions:
            continue
        relative = str(path.relative_to(project_root)).lower()
        if relative in evidence_text or path.name.lower() in evidence_text:
            return path
    return None


def _matches(
    profile: dict,
    state: dict,
    project_root: Path,
    project_files: Sequence[Path],
    package_dependencies: set,
    request_text: str,
    evidence_text: str,
) -> List[str]:
    signals = profile["signals"]
    reasons = []
    state_flag = signals.get("state_flag")
    if state_flag and bool(state.get(state_flag)):
        reasons.append(f"state:{state_flag}=true")

    root_files = [path for path in project_files if path.parent == project_root]
    names = {path.name for path in root_files}
    for name in signals.get("filenames", []):
        if name in names:
            match = next(path for path in root_files if path.name == name)
            reasons.append(f"file:{match.relative_to(project_root)}")
            break

    configured_dependencies = {item.lower() for item in signals.get("package_dependencies", [])}
    matched_dependencies = sorted(configured_dependencies & package_dependencies)
    if matched_dependencies:
        reasons.append(f"dependency:{matched_dependencies[0]}")

    evidence_extensions = {item.lower() for item in signals.get("evidence_extensions", [])}
    if evidence_extensions:
        match = _referenced_project_file(
            project_root, project_files, evidence_text, evidence_extensions
        )
        if match is not None:
            reasons.append(f"evidence-file:{match.relative_to(project_root)}")
        else:
            mentioned = next(
                (
                    extension for extension in sorted(evidence_extensions)
                    if re.search(rf"[\w./-]+{re.escape(extension)}(?:\b|`)", evidence_text)
                ),
                None,
            )
            if mentioned:
                reasons.append(f"evidence-extension:{mentioned}")

    configured_parts = {item.lower() for item in signals.get("path_parts", [])}
    matched_parts = sorted(part for part in configured_parts if part in evidence_text)
    if matched_parts:
        reasons.append(f"evidence-path:{matched_parts[0]}")

    term_text = request_text.lower() if signals.get("request_terms_only") else evidence_text
    matched_terms = [term for term in signals.get("terms", []) if term.lower() in term_text]
    if matched_terms:
        reasons.append(f"evidence-term:{matched_terms[0]}")
    return reasons


def applicable_rules(
    state: dict,
    stage: str,
    request: str,
    upstream_paths: Sequence[Path],
    explicit_profiles: Optional[Sequence[str]] = None,
) -> List[Tuple[str, str, str, List[str]]]:
    project_root = Path(state["project_root"])
    project_files = list(_walk_project(project_root))
    dependencies = _package_dependencies(project_root, project_files)
    evidence = _evidence_text(request, upstream_paths)
    explicit = list(dict.fromkeys(explicit_profiles or []))
    unknown = [profile_id for profile_id in explicit if profile_id not in PROFILES]
    if unknown:
        raise ValueError(f"未知专项规则: {', '.join(unknown)}")

    selected = []
    for profile in MANIFEST["profiles"]:
        if stage not in profile["stages"]:
            continue
        reasons = _matches(
            profile, state, project_root, project_files, dependencies, request, evidence
        )
        if profile["id"] in explicit:
            reasons.insert(0, "explicit")
        if not reasons:
            continue
        body = _rule_path(profile["file"]).read_text(encoding="utf-8").strip()
        selected.append((profile["id"], profile["title"], body, reasons))
    return selected
