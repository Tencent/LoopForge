#!/usr/bin/env python3
import json
import shutil
from pathlib import Path
from typing import Dict, List


SKILL_ROOT = Path(__file__).resolve().parent.parent
TEMPLATE_ROOT = SKILL_ROOT / "templates"
MANIFEST_PATH = TEMPLATE_ROOT / "manifest.json"


def load_manifest() -> dict:
    data = json.loads(MANIFEST_PATH.read_text(encoding="utf-8"))
    if not isinstance(data.get("version"), str) or not data["version"]:
        raise ValueError("模板清单缺少 version")
    artifacts = data.get("artifacts")
    if not isinstance(artifacts, list) or not artifacts:
        raise ValueError("模板清单 artifacts 必须是非空数组")
    seen = set()
    for item in artifacts:
        required = {"path", "stage", "template", "template_version", "content_concepts"}
        if not isinstance(item, dict) or not required.issubset(item):
            raise ValueError("模板清单存在不完整的产物定义")
        relative = item["path"]
        if relative in seen or Path(relative).is_absolute() or ".." in Path(relative).parts:
            raise ValueError(f"模板清单包含不安全或重复路径: {relative}")
        seen.add(relative)
        template = TEMPLATE_ROOT / item["template"]
        if not template.is_file():
            raise ValueError(f"模板文件不存在: {item['template']}")
        if item.get("condition") not in {None, "requires_design_artifacts"}:
            raise ValueError(f"模板清单包含不支持的条件: {item.get('condition')}")
    blueprint_ids = set()
    for blueprint in data.get("blueprints", []):
        required = {"id", "target_pattern", "template", "template_version"}
        if not isinstance(blueprint, dict) or not required.issubset(blueprint):
            raise ValueError("模板清单存在不完整的蓝图定义")
        if blueprint["id"] in blueprint_ids:
            raise ValueError(f"蓝图 ID 重复: {blueprint['id']}")
        blueprint_ids.add(blueprint["id"])
        if not (TEMPLATE_ROOT / blueprint["template"]).is_file():
            raise ValueError(f"蓝图模板不存在: {blueprint['template']}")
    system_ids = set()
    for template in data.get("system_templates", []):
        required = {"id", "template", "template_version"}
        if not isinstance(template, dict) or not required.issubset(template):
            raise ValueError("模板清单存在不完整的系统模板定义")
        if template["id"] in system_ids:
            raise ValueError(f"系统模板 ID 重复: {template['id']}")
        system_ids.add(template["id"])
        if not (TEMPLATE_ROOT / template["template"]).is_file():
            raise ValueError(f"系统模板不存在: {template['template']}")
    return data


MANIFEST = load_manifest()
ARTIFACTS: Dict[str, dict] = {item["path"]: item for item in MANIFEST["artifacts"]}
SYSTEM_TEMPLATES = {item["id"]: item for item in MANIFEST.get("system_templates", [])}


def required_artifacts(stage: str, include_design_context: bool = False) -> List[str]:
    result = []
    for item in MANIFEST["artifacts"]:
        if item["stage"] != stage:
            continue
        if item.get("condition") == "requires_design_artifacts" and not include_design_context:
            continue
        result.append(item["path"])
    return result


def materialize_stage(stage: str, artifact_root: Path, include_design_context: bool = False) -> List[str]:
    created = []
    for relative in required_artifacts(stage, include_design_context):
        spec = ARTIFACTS[relative]
        destination = artifact_root / relative
        if destination.exists():
            continue
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(TEMPLATE_ROOT / spec["template"], destination)
        created.append(relative)
    return created


def system_template(template_id: str) -> Path:
    return TEMPLATE_ROOT / SYSTEM_TEMPLATES[template_id]["template"]
