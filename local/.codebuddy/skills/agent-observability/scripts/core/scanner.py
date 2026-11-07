"""Skill / Rule 静态扫描层。

这一层只负责扫描项目里的 `.codebuddy/skills` 与 `.codebuddy/rules`：
- 识别有哪些可用 skill / rule
- 提取它们的基础元数据与 frontmatter
- 提供 path-based 命中推断依赖的 inventory 信息

它不依赖具体 workflow 语义，本身是通用层。
"""
from __future__ import annotations

import os
import re
from pathlib import Path
from typing import Any

# --- 用于解析 SKILL.md / RULE.mdc frontmatter 的正则 ---
FRONTMATTER_RE = re.compile(r"^---\s*\n(.*?)\n---", re.S)
NAME_FIELD_RE = re.compile(r"^name:\s*(.+)$", re.M)
VERSION_FIELD_RE = re.compile(r"^version:\s*(.+)$", re.M)
AUTHOR_FIELD_RE = re.compile(r"^author:\s*(.+)$", re.M)
TAGS_FIELD_RE = re.compile(r"^tags:\s*\[([^\]]+)\]", re.M)
DESC_FIELD_RE = re.compile(r"^description:\s*(.+?)(?=\n\w+:|\n---|\Z)", re.S | re.M)
ALWAYS_APPLY_RE = re.compile(r"^alwaysApply:\s*(true|false)\s*$", re.M | re.I)
ENABLED_RE = re.compile(r"^enabled:\s*(true|false)\s*$", re.M | re.I)
GLOBS_RE = re.compile(r"^globs:\s*(.+?)(?=\n\w+:|\n---|\Z)", re.S | re.M)
APPLY_AGENTS_RE = re.compile(r"^applyAgents:\s*(.+?)(?=\n\w+:|\n---|\Z)", re.S | re.M)
EXCLUDE_AGENTS_RE = re.compile(r"^excludeAgents:\s*(.+?)(?=\n\w+:|\n---|\Z)", re.S | re.M)

# 固定的 inventory 根目录（仅限项目内）
PROJECT_SKILLS_REL = Path(".codebuddy/skills")
PROJECT_RULES_REL = Path(".codebuddy/rules")

# 支持识别的 skill 子模块根目录
SUBMODULE_DIRS = {"references", "scripts", "templates", "examples", "checklists", "roles"}

# 允许严格提取的路径类字段键名（不扫描自由文本）
PATH_VALUE_KEYS = {
    "file_path", "filePath", "path", "target_file", "target_directory",
    "cwd", "rule", "rule_path", "skill_path",
}
PATH_LIST_KEYS = {"paths", "files", "rules", "skills"}

# 扫描时要跳过的目录
SKIP_DIR_PARTS = {"skills-by-node", "node_modules", ".git", "__pycache__", "dist"}

# 可能触发 skill 的工具名
USE_SKILL_TOOLS = {"use_skill", "UseSkill", "load_skill", "Skill", "skill"}


def _is_skipped_path(p: Path) -> bool:
    """判断路径是否命中扫描时应跳过的目录集合。"""
    parts = set(p.parts)
    return bool(parts & SKIP_DIR_PARTS)


def _parse_frontmatter(path: Path) -> dict[str, Any]:
    """解析 `SKILL.md` 或 `RULE.mdc` 中的 frontmatter。"""
    try:
        text = path.read_text("utf-8", errors="ignore")
    except Exception:
        return {}
    m = FRONTMATTER_RE.match(text)
    if not m:
        return {}
    block = m.group(1)
    info: dict = {}

    def _grab(regex, key, post=None):
        mm = regex.search(block)
        if mm:
            v = mm.group(1).strip().strip('"\'').strip()
            info[key] = post(v) if post else v

    _grab(NAME_FIELD_RE, "name")
    _grab(VERSION_FIELD_RE, "version")
    _grab(AUTHOR_FIELD_RE, "author")
    _grab(TAGS_FIELD_RE, "tags",
          lambda v: [t.strip().strip('"\'') for t in v.split(",") if t.strip()])
    _grab(DESC_FIELD_RE, "description", lambda v: v.strip().replace("\n", " ")[:200])
    return info


def _parse_list_field(block: str, regex: re.Pattern[str]) -> list[str]:
    """从 frontmatter 文本块中解析一个逗号分隔的列表字段。"""
    mm = regex.search(block)
    if not mm:
        return []
    raw = mm.group(1).strip().strip('"\'')
    if raw.startswith("["):
        raw = raw.strip("[]")
    return [g.strip().strip('"\'').lower() for g in raw.split(",") if g.strip()]


def _parse_rule_frontmatter(rule_path: Path) -> dict[str, Any]:
    """解析 `RULE.mdc` 中和规则生效相关的 frontmatter 字段。"""
    try:
        text = rule_path.read_text("utf-8", errors="ignore")
    except Exception:
        return {}
    m = FRONTMATTER_RE.match(text)
    if not m:
        return {}
    block = m.group(1)
    info: dict = {}

    mm = ALWAYS_APPLY_RE.search(block)
    if mm:
        info["alwaysApply"] = mm.group(1).lower() == "true"
    mm = ENABLED_RE.search(block)
    if mm:
        info["enabled"] = mm.group(1).lower() == "true"
    globs = _parse_list_field(block, GLOBS_RE)
    if globs:
        info["globs"] = globs

    apply_agents = _parse_list_field(block, APPLY_AGENTS_RE)
    if apply_agents:
        info["applyAgents"] = apply_agents

    exclude_agents = _parse_list_field(block, EXCLUDE_AGENTS_RE)
    if exclude_agents:
        info["excludeAgents"] = exclude_agents

    mm = DESC_FIELD_RE.search(block)
    if mm:
        info["description"] = mm.group(1).strip().strip('"\'').replace("\n", " ")[:200]
    return info


def _classify_source(path: Path, cwd: Path | None) -> str:
    """识别来源类型；当前只支持项目内 `.codebuddy` 路径。"""
    if not cwd:
        return "unknown"
    try:
        path_str = str(path.resolve())
        cwd_str = str(cwd.resolve())
    except Exception:
        return "unknown"
    if path_str.startswith(cwd_str + "/.codebuddy/"):
        return "project"
    return "unknown"


def _collect_submodules(skill_dir: Path) -> dict[str, list]:
    """收集一个 skill 内部的子模块目录，如 references/scripts/templates。"""
    sub: dict = {}
    if not skill_dir.is_dir():
        return sub
    for kind in ("references", "scripts", "templates", "examples", "checklists", "roles"):
        d = skill_dir / kind
        if d.is_dir():
            files = [str(f.relative_to(skill_dir))
                     for f in d.rglob("*") if f.is_file()]
            if files:
                sub[kind] = sorted(files)[:30]
    return sub


def scan_skills_and_rules(cwd: str) -> tuple[dict[str, Any], dict[str, Any]]:
    """扫描项目固定根目录：`.codebuddy/skills` 与 `.codebuddy/rules`。"""
    skills: dict = {}
    rules: dict = {}
    cwd_path = Path(cwd) if cwd else None
    if not cwd_path or not cwd_path.exists():
        return skills, rules

    skills_root = (cwd_path / PROJECT_SKILLS_REL).resolve()
    rules_root = (cwd_path / PROJECT_RULES_REL).resolve()

    if skills_root.is_dir() and not _is_skipped_path(skills_root):
        for p in skills_root.rglob("SKILL.md"):
            if _is_skipped_path(p):
                continue
            skill_dir = p.parent
            name = skill_dir.name
            if not name or name in skills:
                continue
            meta = _parse_frontmatter(p)
            skills[name] = {
                "source": _classify_source(skill_dir, cwd_path),
                "path": str(skill_dir),
                "has_skill_md": True,
                "version": meta.get("version"),
                "author": meta.get("author"),
                "description": meta.get("description"),
                "submodules": _collect_submodules(skill_dir),
            }

    if rules_root.is_dir() and not _is_skipped_path(rules_root):
        for p in rules_root.rglob("*.mdc"):
            if _is_skipped_path(p):
                continue
            name = p.parent.name if p.name == "RULE.mdc" else p.stem
            if not name or name in rules:
                continue
            meta = _parse_rule_frontmatter(p)
            rules[name] = {
                "source": _classify_source(p.parent, cwd_path),
                "path": str(p),
                "alwaysApply": meta.get("alwaysApply", False),
                "enabled": meta.get("enabled", True),
                "globs": meta.get("globs") or [],
                "applyAgents": meta.get("applyAgents") or [],
                "excludeAgents": meta.get("excludeAgents") or [],
                "description": meta.get("description"),
            }

    return skills, rules


def lookup_skill_meta(skills_meta: dict[str, Any], name: str) -> dict[str, Any] | None:
    """从缓存的 inventory 字典里按名称查找 skill 元数据。"""
    meta = (skills_meta or {}).get(name)
    return meta if isinstance(meta, dict) else None


def lookup_rule_meta(rules_meta: dict[str, Any], name: str) -> dict[str, Any] | None:
    """从缓存的 inventory 字典里按名称查找 rule 元数据。"""
    meta = (rules_meta or {}).get(name)
    return meta if isinstance(meta, dict) else None


def _collect_path_strings(obj: Any) -> list[str]:
    """只收集结构化路径字段，绝不解析自由文本。"""
    out: list[str] = []

    def _walk(x: Any) -> None:
        if isinstance(x, dict):
            for k, v in x.items():
                if k in PATH_VALUE_KEYS and isinstance(v, str) and v.strip():
                    out.append(v.strip())
                elif k in PATH_LIST_KEYS:
                    if isinstance(v, str) and v.strip():
                        out.append(v.strip())
                    elif isinstance(v, list):
                        out.extend([
                            s.strip() for s in v
                            if isinstance(s, str) and s.strip()
                        ])
                elif isinstance(v, (dict, list)):
                    _walk(v)
        elif isinstance(x, list):
            for it in x:
                if isinstance(it, (dict, list)):
                    _walk(it)

    _walk(obj)
    return list(dict.fromkeys(out))


def _path_parts(path_str: str) -> list[str]:
    """把路径拆成标准化片段列表，便于后续命中判断。"""
    p = path_str.replace("\\", "/").strip()
    return [part for part in p.split("/") if part]


def _extract_skill_from_path(path_str: str) -> str | None:
    """从路径中抽取被引用的 skill 名。"""
    parts = _path_parts(path_str)
    for i in range(len(parts) - 2):
        if parts[i] == ".codebuddy" and parts[i + 1] == "skills":
            name = parts[i + 2]
            if name and re.fullmatch(r"[a-zA-Z0-9_\-]+", name):
                return name
    return None


def _extract_rule_from_path(path_str: str) -> str | None:
    """从路径中抽取被引用的 rule 名。"""
    parts = _path_parts(path_str)
    for i in range(len(parts) - 2):
        if parts[i] == ".codebuddy" and parts[i + 1] == "rules":
            name = parts[i + 2]
            if name.lower().endswith(".mdc"):
                name = Path(name).stem
            if name and re.fullmatch(r"[a-zA-Z0-9_\-]+", name):
                return name
    return None


def _is_rule_allowed_for_agent(name: str, meta: dict[str, Any], active_agent: str | None) -> bool:
    """按角色判断 rule 是否可用；`global` 永远保持可选。"""
    if str(name).strip().lower() == "global":
        return True

    agent = str(active_agent or "").strip().lower()
    if not agent:
        return True

    apply_agents = [str(a).strip().lower() for a in (meta.get("applyAgents") or []) if str(a).strip()]
    exclude_agents = [str(a).strip().lower() for a in (meta.get("excludeAgents") or []) if str(a).strip()]

    if apply_agents and agent not in apply_agents:
        return False
    if exclude_agents and agent in exclude_agents:
        return False
    return True


def _glob_matches_any_path(globs: list[str], paths_to_check: list[str]) -> bool:
    """判断配置的 glob 是否命中任一收集到的路径。"""
    if not globs or not paths_to_check:
        return False
    import fnmatch

    for p in paths_to_check:
        basename = os.path.basename(p)
        if any(fnmatch.fnmatch(p, g) or fnmatch.fnmatch(basename, g) for g in globs):
            return True
    return False


def evaluate_rules_for_call(
    data: dict,
    rules_meta: dict,
    active_agent: str | None = None,
) -> dict[str, dict[str, Any]]:
    """评估当前工具调用下所有已知 rule，并保留未命中的原因。"""
    if not rules_meta:
        return {}

    tool_input = data.get("tool_input") or {}
    paths_to_check = _collect_path_strings(tool_input)
    _, path_rules = extract_paths_from_tool_call(data)
    path_rule_set = set(path_rules)

    evaluations: dict[str, dict[str, Any]] = {}
    for name, meta in (rules_meta or {}).items():
        if not isinstance(meta, dict):
            continue

        globs = [str(g).strip() for g in (meta.get("globs") or []) if str(g).strip()]
        apply_agents = [str(a).strip().lower() for a in (meta.get("applyAgents") or []) if str(a).strip()]
        exclude_agents = [str(a).strip().lower() for a in (meta.get("excludeAgents") or []) if str(a).strip()]
        evaluation: dict[str, Any] = {"matched": False}

        if name in path_rule_set:
            evaluation["matched"] = True
            evaluation["via"] = "path_inferred"
        elif meta.get("enabled") is False:
            evaluation["reason"] = "disabled"
        elif not _is_rule_allowed_for_agent(name, meta, active_agent):
            evaluation["reason"] = "agent_filtered"
            if apply_agents:
                evaluation["applyAgents"] = apply_agents
            if exclude_agents:
                evaluation["excludeAgents"] = exclude_agents
        elif meta.get("alwaysApply"):
            evaluation["matched"] = True
            evaluation["via"] = "always_apply"
        elif globs:
            evaluation["globs"] = globs
            evaluation["paths_checked"] = len(paths_to_check)
            if not paths_to_check:
                evaluation["reason"] = "no_paths"
            elif _glob_matches_any_path(globs, paths_to_check):
                evaluation["matched"] = True
                evaluation["via"] = "glob"
            else:
                evaluation["reason"] = "glob_not_matched"
                evaluation["paths_sample"] = paths_to_check[:3]
        else:
            evaluation["reason"] = "no_globs"

        evaluations[name] = evaluation

    return evaluations


def unmatched_rule_diagnostics_for_call(
    data: dict,
    rules_meta: dict,
    active_agent: str | None = None,
) -> dict[str, dict[str, Any]]:
    """返回当前工具调用下未命中的 rule 的精简诊断信息。"""
    diagnostics: dict[str, dict[str, Any]] = {}
    for name, evaluation in evaluate_rules_for_call(data, rules_meta, active_agent=active_agent).items():
        if evaluation.get("matched"):
            continue
        item: dict[str, Any] = {"reason": evaluation.get("reason") or "unknown"}
        for key in ("globs", "paths_checked", "paths_sample", "applyAgents", "excludeAgents"):
            value = evaluation.get(key)
            if value is not None and value != []:
                item[key] = value
        diagnostics[name] = item
    return diagnostics


def filtered_rules_for_agent(rules_meta: dict, active_agent: str | None = None) -> list[str]:
    """列出因 agent 过滤而被排除的 rule（仅看已启用项）。"""
    filtered: list[str] = []
    for name, evaluation in evaluate_rules_for_call({}, rules_meta, active_agent=active_agent).items():
        if evaluation.get("reason") == "agent_filtered":
            filtered.append(name)
    return sorted(set(filtered))


def active_rules_for_call(data: dict, rules_meta: dict, active_agent: str | None = None) -> list[str]:
    """判断给定工具调用下哪些 rule 处于生效状态。"""
    active: list[str] = []
    for name, evaluation in evaluate_rules_for_call(data, rules_meta, active_agent=active_agent).items():
        if evaluation.get("matched") and evaluation.get("via") != "path_inferred":
            active.append(name)
    return sorted(set(active))


def extract_skill_from_tool_call(data: dict) -> str | None:
    """从类似 use_skill 的工具调用里提取 skill 名称。"""
    tool = data.get("tool_name", "")
    if tool not in USE_SKILL_TOOLS:
        return None
    tool_input = data.get("tool_input") or {}
    if isinstance(tool_input, dict):
        return tool_input.get("command") or tool_input.get("name") or tool_input.get("skill")
    return None


def is_brainstorming_call(data: dict) -> bool:
    """判断当前工具调用是否正在激活或使用 brainstorming skill。"""
    direct = extract_skill_from_tool_call(data)
    if isinstance(direct, str) and direct.strip().lower() == "brainstorming":
        return True

    path_skills, _ = extract_paths_from_tool_call(data)
    return any(str(s).strip().lower() == "brainstorming" for s in path_skills)


def extract_paths_from_tool_call(data: dict) -> tuple[list[str], list[str]]:
    """只根据 `.codebuddy` 根目录下的结构化路径推断 skill/rule。"""
    tool_input = data.get("tool_input") or {}
    if not isinstance(tool_input, dict):
        return [], []

    skills: list[str] = []
    rules: list[str] = []
    for p in _collect_path_strings(tool_input):
        s = _extract_skill_from_path(p)
        if s:
            skills.append(s)
        r = _extract_rule_from_path(p)
        if r:
            rules.append(r)

    return list(dict.fromkeys(skills)), list(dict.fromkeys(rules))


def extract_submodule_hits(data: dict) -> list[dict[str, str]]:
    """仅从结构化 skill 路径中提取子模块命中结果，不扫描自由文本。"""
    tool_input = data.get("tool_input") or {}
    if not isinstance(tool_input, dict):
        return []

    seen: set[tuple[str, str]] = set()
    hits: list[dict[str, str]] = []
    path_values = _collect_path_strings(tool_input)

    for p in path_values:
        parts = _path_parts(p)
        for i in range(len(parts) - 3):
            if parts[i] == ".codebuddy" and parts[i + 1] == "skills":
                skill_name = parts[i + 2]
                sub_root = parts[i + 3]
                if not re.fullmatch(r"[a-zA-Z0-9_\-]+", skill_name):
                    continue
                if sub_root not in SUBMODULE_DIRS:
                    continue
                sub_path = "/".join(parts[i + 3:]).rstrip("/")
                key = (skill_name, sub_path)
                if key in seen:
                    continue
                seen.add(key)
                hits.append({"skill": skill_name, "submodule": sub_path})
                break

    return hits


def extract_bash_skill_scripts(data: dict) -> list[str]:
    """从命令文本中的显式 `.codebuddy/skills` 路径识别 skill 脚本。"""
    if data.get("tool_name") not in ("Bash", "bash", "execute_command"):
        return []
    tool_input = data.get("tool_input") or {}
    if not isinstance(tool_input, dict):
        return []
    cmd = tool_input.get("command") or ""
    if not isinstance(cmd, str) or not cmd.strip():
        return []

    skills: list[str] = []
    for token in cmd.split():
        t = token.strip("\"'`;,()[]{}")
        s = _extract_skill_from_path(t)
        if s:
            skills.append(s)
    return list(dict.fromkeys(skills))
