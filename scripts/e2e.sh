#!/usr/bin/env bash
# End-to-end tests: install the packaged CLI into throw-away projects and drive
# every host through install, the full Portable workflow (with a fake host
# Agent), resume, failure paths, Classic hooks and the local dashboard.
# Needs Python 3.8+, Node.js 20+ and npm; no network access or API keys.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export PYTHONDONTWRITEBYTECODE=1

python3 -m unittest discover -s "$ROOT_DIR/tests/e2e" -t "$ROOT_DIR/tests/e2e" -p 'test_e2e_*.py' -v "$@"
echo "E2E tests passed."
