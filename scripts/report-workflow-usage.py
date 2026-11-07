#!/usr/bin/env python3
"""Compatibility entrypoint for the portable workflow usage skill."""
from pathlib import Path
import runpy

if __name__ == "__main__":
    script = Path(__file__).resolve().parents[1] / "skills" / "report-workflow-usage" / "scripts" / "report_usage.py"
    runpy.run_path(str(script), run_name="__main__")
