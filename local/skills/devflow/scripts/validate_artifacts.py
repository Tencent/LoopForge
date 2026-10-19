#!/usr/bin/env python3
import argparse
import json
import re
from pathlib import Path
from typing import Dict, List, Optional, Tuple

from template_registry import ARTIFACTS, required_artifacts


SKILL_ROOT = Path(__file__).resolve().parent.parent
CONTRACT: Dict[str, List[List[str]]] = {
    relative: spec["content_concepts"] for relative, spec in ARTIFACTS.items()
}
DEFAULTS = json.loads((SKILL_ROOT / "config/workflow.json").read_text(encoding="utf-8"))
PLACEHOLDER_PATTERN = re.compile(
    r"^(?:tbd|todo|fixme|placeholder|待补充|待完善|待定)(?:\s*[:：-].*)?$",
    re.IGNORECASE,
)
TEMPLATE_SLOT_PATTERN = re.compile(r"\{\{[^{}]+\}\}")


def markdown_headings(text: str) -> List[Tuple[int, int, int, str]]:
    matches = []
    fence_char = None
    fence_length = 0
    offset = 0
    for line in text.splitlines(keepends=True):
        fence = re.match(r"^\s{0,3}(`{3,}|~{3,})", line)
        if fence:
            marker = fence.group(1)
            if fence_char is None:
                fence_char = marker[0]
                fence_length = len(marker)
            elif marker[0] == fence_char and len(marker) >= fence_length:
                fence_char = None
                fence_length = 0
            offset += len(line)
            continue
        if fence_char is None:
            heading = re.match(r"^\s{0,3}(#{1,6})\s+(.+?)\s*#*\s*(?:\r?\n)?$", line)
            if heading:
                matches.append((offset, offset + len(line.rstrip("\r\n")), len(heading.group(1)), heading.group(2).strip()))
        offset += len(line)
    return matches


def sections(text: str) -> List[Tuple[str, str]]:
    matches = markdown_headings(text)
    result = []
    for index, match in enumerate(matches):
        start, content_start, level, title = match
        end = len(text)
        for following in matches[index + 1:]:
            if following[2] <= level:
                end = following[0]
                break
        result.append((title.lower(), text[content_start:end].strip()))
    return result


def normalize_heading(value: str) -> str:
    value = re.sub(r"^\s*(?:\d+(?:\.\d+)*[.)、]?|[（(]?[一二三四五六七八九十]+[）)、.])\s*", "", value)
    value = re.sub(r"[()（）\[\]【】{}]", " ", value)
    value = re.sub(r"\s*(?:/|&|、|和|与|及|:|：|—|–|-)+\s*", " ", value)
    return re.sub(r"\s+", " ", value.strip().lower())


def heading_matches(heading: str, alias: str) -> bool:
    heading = normalize_heading(heading)
    alias = normalize_heading(alias)
    return heading == alias or bool(re.search(rf"(?:^|\s){re.escape(alias)}(?:$|\s)", heading))


def find_section(items: List[Tuple[str, str]], aliases: List[str]) -> Optional[Tuple[str, str]]:
    return next((item for item in items if any(heading_matches(item[0], alias) for alias in aliases)), None)


def is_placeholder(body: str) -> bool:
    normalized = re.sub(r"^[\s`*_#>-]+|[\s`*_#-]+$", "", body.strip())
    if not normalized:
        return True
    lines = [re.sub(r"^[\s`*_#>-]+|[\s`*_#-]+$", "", line) for line in normalized.splitlines()]
    meaningful = [line for line in lines if line]
    return bool(meaningful) and all(PLACEHOLDER_PATTERN.fullmatch(line) for line in meaningful)


def validate_file(path: Path, relative: str) -> List[str]:
    errors = []
    if not path.is_file():
        return [f"{relative}: missing file"]
    text = path.read_text(encoding="utf-8").strip()
    if not text:
        return [f"{relative}: empty file"]
    items = sections(text)
    slots = TEMPLATE_SLOT_PATTERN.findall(text)
    if slots:
        errors.append(f"{relative}: 仍有 {len(slots)} 个模板占位项未填写")
    substantive = re.sub(r"[#*_`>\s\-]+", "", text)
    if len(substantive) < 40:
        errors.append(f"{relative}: report has too little substantive content")
    for aliases in CONTRACT.get(relative, []):
        found = find_section(items, aliases)
        label = aliases[0]
        if not found:
            errors.append(f"{relative}: 缺少必要章节：{label}")
            continue
        if is_placeholder(found[1]):
            errors.append(f"{relative}: 模板内容为空或仍是占位内容：{label}")
    if relative == "03-code/review-report.md":
        verdict = find_section(items, ["verdict", "审查结论"])
        if verdict is None:
            errors.append(f"{relative}: review verdict is required")
        elif not is_placeholder(verdict[1]):
            body = verdict[1].lower()
            negative = re.search(r"\b(?:fail(?:ed|ure)?|changes requested|not approved)\b|不通过|未批准|有阻断", body)
            positive = re.search(r"\b(?:pass(?:ed)?|approve(?:d)?|no blockers?|no blocking findings)\b|(?<!不)通过|已批准|无阻断", body)
            if negative or not positive:
                errors.append(f"{relative}: 审查结论必须明确表示通过")
    if relative in {"01-solo/solo-report.md", "03-code/change-report.md"}:
        api_docs = find_section(items, ["API 接口文档", "api documentation"])
        if api_docs is not None and not is_placeholder(api_docs[1]):
            body = api_docs[1].strip()
            no_change = re.search(r"无\s*(?:HTTP\s*)?(?:API\s*)?接口变动|no api changes?", body, re.IGNORECASE)
            if not no_change:
                expected = (
                    "01-solo/api-docs.md"
                    if relative == "01-solo/solo-report.md"
                    else "03-code/api-docs.md"
                )
                if expected not in re.findall(r"`([^`]+)`", body):
                    errors.append(f"{relative}: 有接口变动时必须记录已生成 `{expected}`")
                else:
                    target = path.parents[1] / expected
                    if not target.is_file() or not target.read_text(encoding="utf-8").strip():
                        errors.append(f"{relative}: API 接口文档不存在或为空：{expected}")
                    else:
                        errors.extend(validate_file(target, expected))
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description="Validate DevFlow report contents")
    parser.add_argument("artifact_root", type=Path)
    parser.add_argument("--path", choices=sorted(CONTRACT), action="append")
    args = parser.parse_args()
    if args.path:
        selected = args.path
    else:
        state_path = args.artifact_root / "workflow-state.json"
        if state_path.is_file():
            state = json.loads(state_path.read_text(encoding="utf-8"))
            route = state.get("route")
            known_stages = {item["stage"] for item in ARTIFACTS.values()}
            if not isinstance(route, list) or any(stage not in known_stages for stage in route):
                print("ERROR:\n- workflow-state.json: invalid route")
                return 1
            selected = [
                relative
                for stage in route
                for relative in required_artifacts(
                    stage, bool(state.get("requires_design_artifacts", False))
                )
            ]
        else:
            print("ERROR:\n- workflow-state.json is required when --path is omitted")
            return 1
    errors = []
    for relative in selected:
        errors.extend(validate_file(args.artifact_root / relative, relative))
    if errors:
        print("ERROR:\n- " + "\n- ".join(errors))
        return 1
    print(f"OK: validated {len(selected)} artifact reports")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
