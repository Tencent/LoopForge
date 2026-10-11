from pathlib import Path

from setuptools import setup


ROOT = Path(__file__).parent
ASSET_ROOTS = ["skills", ".codebuddy", ".codex", ".cursor", ".claude"]


def data_files():
    result = [("share/devflow", ["THIRD_PARTY_NOTICES.md"])]
    for root_name in ASSET_ROOTS:
        source_root = ROOT / root_name
        grouped = {}
        for path in source_root.rglob("*"):
            relative = path.relative_to(source_root)
            if (
                not path.is_file()
                or "tests" in relative.parts
                or "__pycache__" in relative.parts
                or path.suffix == ".pyc"
                or (
                    "logs" in relative.parts
                    and "agent-observability" in relative.parts
                    and path.name != ".gitkeep"
                )
            ):
                continue
            target = Path("share/devflow") / path.parent.relative_to(ROOT)
            grouped.setdefault(str(target), []).append(str(path.relative_to(ROOT)))
        result.extend((target, paths) for target, paths in sorted(grouped.items()))
    return result


setup(data_files=data_files())
