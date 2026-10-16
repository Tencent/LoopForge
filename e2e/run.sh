#!/usr/bin/env bash
# Runs the live e2e suite against AGENTFLOW_E2E_BASE (default: the test env).
#   ./run.sh           API scenarios, then the Playwright UI specs
#   ./run.sh api       API only
#   ./run.sh ui        UI only
# A final sweep removes anything an interrupted scenario left behind.
set -uo pipefail
cd "$(dirname "$0")"

[ -d node_modules/@playwright/test ] || npm install --no-audit --no-fund
what="${1:-all}"
status=0
# One identity for this invocation, shared by API/UI subprocesses and sweep.
export AGENTFLOW_E2E_RUN_TAG="${AGENTFLOW_E2E_RUN_TAG:-$(date -u +%y%m%d%H%M%S)-$$}"

if [ "$what" = all ] || [ "$what" = api ]; then
  node --test --test-concurrency=1 --test-reporter=spec api/*.test.mjs || status=1
fi
if [ "$what" = all ] || [ "$what" = ui ]; then
  npx playwright test || status=1
fi
if [ "${AGENTFLOW_E2E_KEEP:-0}" != 1 ]; then
  node cleanup.mjs || true
fi
exit $status
