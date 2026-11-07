#!/bin/sh
# Agent browser sidecar: Chromium exposes CDP on the shared loopback, Playwright
# MCP drives it for the agent, and the task panel attaches through cdp-relay.
set -u
profile=/tmp/agentflow-browser
output="${AGENTFLOW_BROWSER_OUTPUT:-/tmp/agentflow-browser-output}"
mkdir -p "$profile" "$output"
(
  while :; do
    chromium --headless=new --no-sandbox --disable-dev-shm-usage --disable-gpu \
      --no-first-run --no-default-browser-check --disable-background-networking \
      --remote-debugging-address=127.0.0.1 --remote-debugging-port="${AGENTFLOW_BROWSER_CDP_PORT:-9222}" \
      --user-data-dir="$profile" --window-size=1280,800 --lang=zh-CN about:blank >/tmp/chromium.log 2>&1
    sleep 1
  done
) &
node /opt/agentflow/cdp-relay.mjs --wait || exit 1
exec playwright-mcp --cdp-endpoint "http://127.0.0.1:${AGENTFLOW_BROWSER_CDP_PORT:-9222}" \
  --host 127.0.0.1 --port "${AGENTFLOW_BROWSER_MCP_PORT:-8931}" \
  --allowed-hosts "127.0.0.1:${AGENTFLOW_BROWSER_MCP_PORT:-8931},localhost:${AGENTFLOW_BROWSER_MCP_PORT:-8931}" \
  --shared-browser-context --output-dir "$output"
