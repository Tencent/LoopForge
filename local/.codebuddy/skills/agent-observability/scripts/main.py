#!/usr/bin/env python3
"""agent-observability 的 hook 入口。"""
from __future__ import annotations

import sys
from pathlib import Path


if __package__ in (None, ""):
    _SCRIPTS_DIR = Path(__file__).resolve().parent
    if str(_SCRIPTS_DIR) not in sys.path:
        sys.path.insert(0, str(_SCRIPTS_DIR))
    from core.runtime import main as runtime_main  # type: ignore
else:
    from .core.runtime import main as runtime_main


def main() -> int:
    return runtime_main(sys.argv[1:])


if __name__ == "__main__":
    raise SystemExit(main())
