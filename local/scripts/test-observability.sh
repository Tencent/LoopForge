#!/usr/bin/env bash
# Unit tests for the Classic agent-observability skill (dashboard data, hooks,
# CLS sink) in every Classic host bundle, then a check that running them left no
# run output behind: logs written into the source tree would end up in installs.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export PYTHONDONTWRITEBYTECODE=1

for host in codebuddy cursor claude; do
  tests="$ROOT_DIR/.$host/skills/agent-observability/tests"
  echo "== agent-observability unit tests (.$host)"
  python3 -m unittest discover -s "$tests" -t "$tests"
done

leftovers="$(find "$ROOT_DIR"/.codebuddy "$ROOT_DIR"/.cursor "$ROOT_DIR"/.claude \
  -path '*/agent-observability/logs/*' -type f ! -name .gitkeep -print)"
if [[ -n "$leftovers" ]]; then
  echo "Tests wrote run output into the source tree:" >&2
  echo "$leftovers" >&2
  exit 1
fi
echo "Observability tests passed and left the source tree clean."
