#!/usr/bin/env bash
# e2e coverage report against a throwaway local platform (never a deployment):
#
#   e2e/coverage/report.sh            local.sh all, then route + web/ coverage
#   e2e/coverage/report.sh --no-run   only recompute from test-results/
#
# 1. exports every registered gin route (TestRouteInventoryExport);
# 2. runs ./local.sh all with Chromium JS coverage enabled;
# 3. route coverage = routes hit in the platform access log / all routes;
# 4. web/ coverage = webtest/ unit coverage merged with the e2e browser coverage.
# Thresholds: AGENTFLOW_E2E_ROUTE_MIN and AGENTFLOW_E2E_WEB_MIN (default 85).
# The pull-request route gate runs in the platform browser job against that
# job's access log. This script still reruns the full local suite.
set -uo pipefail
cd "$(dirname "$0")/.."
E2E_DIR=$(pwd)
ROOT=$(cd .. && pwd)
OUT="$E2E_DIR/test-results"
ROUTE_MIN=${AGENTFLOW_E2E_ROUTE_MIN:-85}
WEB_MIN=${AGENTFLOW_E2E_WEB_MIN:-85}
mkdir -p "$OUT"

status=0
if [ "${1:-}" != --no-run ]; then
  (cd "$ROOT" && AGENTFLOW_ROUTES_OUT="$OUT/routes.json" go test ./internal/httpapi/ -run '^TestRouteInventoryExport$' -count=1) || exit 1
  [ -d node_modules/@playwright/test ] || npm ci --no-audit --no-fund || exit 1
  rm -rf "$OUT/web-coverage" "$OUT/platform.log"
  AGENTFLOW_E2E_JS_COVERAGE=1 AGENTFLOW_E2E_PLATFORM_LOG="$OUT/platform.log" ./local.sh all || status=1
fi

echo "== e2e route coverage"
node coverage/route-coverage.mjs --routes "$OUT/routes.json" --log "$OUT/platform.log" \
  --min "$ROUTE_MIN" --json "$OUT/route-coverage.json" || status=1

echo "== web/ coverage (webtest unit + e2e browser)"
if [ ! -f "$ROOT/webtest/coverage/coverage-final.json" ]; then
  (cd "$ROOT/webtest" && { [ -d node_modules ] || npm ci --no-audit --no-fund; } && npm run coverage) || status=1
fi
node "$ROOT/webtest/scripts/merge-coverage.mjs" --e2e "$OUT/web-coverage" --min "$WEB_MIN" \
  --json "$OUT/web-coverage-merged.json" || status=1
exit $status
