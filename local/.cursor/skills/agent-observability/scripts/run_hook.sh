#!/bin/sh
set -eu

PROJECT_DIR="${CODEBUDDY_PROJECT_DIR:-$(cd "$(dirname "$0")"/../../../.. && pwd -P)}"
MAIN_PY="$PROJECT_DIR/.codebuddy/skills/agent-observability/scripts/main.py"

exec python3 "$MAIN_PY" "$@"
