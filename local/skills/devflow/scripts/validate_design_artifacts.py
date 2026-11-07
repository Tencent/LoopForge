#!/usr/bin/env python3
import argparse
from pathlib import Path
from typing import List

from validate_artifacts import find_section, is_placeholder, sections


VISUAL_SUFFIXES = {".png", ".jpg", ".jpeg", ".webp", ".gif", ".pdf"}


def is_visual_file(path: Path) -> bool:
    if path.suffix.lower() not in VISUAL_SUFFIXES:
        return False
    with path.open("rb") as handle:
        header = handle.read(12)
    return (
        header.startswith(b"\x89PNG\r\n\x1a\n")
        or header.startswith(b"\xff\xd8\xff")
        or header.startswith((b"GIF87a", b"GIF89a"))
        or header.startswith(b"%PDF-")
        or (header.startswith(b"RIFF") and header[8:12] == b"WEBP")
    )


def validate_design_context(root: Path) -> List[str]:
    if not root.is_dir():
        return [f"design context not found: {root}"]
    errors = []
    for name in ("overview.md", "components.md"):
        path = root / name
        if not path.is_file() or is_placeholder(path.read_text(encoding="utf-8")):
            errors.append(f"missing or empty {name}")
    section_files = sorted((root / "sections").glob("*.md")) if (root / "sections").is_dir() else []
    if not section_files:
        errors.append("missing sections/*.md")
    for path in section_files:
        items = sections(path.read_text(encoding="utf-8"))
        for aliases, label in (
            (["layout", "布局"], "layout"),
            (["content", "文案", "内容"], "content"),
            (["validation", "回验", "验证"], "validation"),
        ):
            found = find_section(items, aliases)
            if found is None or is_placeholder(found[1]):
                errors.append(f"{path.name}: missing or empty {label} section")
    evidence_root = root / "evidence"
    evidence = [
        path for path in evidence_root.glob("**/*")
        if path.is_file() and is_visual_file(path)
    ] if evidence_root.is_dir() else []
    if not evidence:
        errors.append("missing image or PDF visual evidence")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description="Validate DevFlow design implementation artifacts")
    parser.add_argument("design_context", type=Path)
    args = parser.parse_args()
    errors = validate_design_context(args.design_context)
    if errors:
        print("ERROR:\n- " + "\n- ".join(errors))
        return 1
    print("OK: design context artifacts are valid")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
