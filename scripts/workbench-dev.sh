#!/usr/bin/env bash
# Rebuild the Go server (the classic web UI is embedded) and restart the
# local preview instance. Uses an isolated MySQL container and workspace root
# so its sandbox reclaimer never touches other instances on the same host.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
STATE=/tmp/lf-workbench
PORT="${PORT:-18765}"
MYSQL_PORT="${MYSQL_PORT:-33421}"
mkdir -p "$STATE/workspaces" "$STATE/cli"

if ! docker ps --format '{{.Names}}' | grep -qx lf-workbench-mysql; then
  docker start lf-workbench-mysql >/dev/null 2>&1 || docker run -d --name lf-workbench-mysql \
    -p "127.0.0.1:$MYSQL_PORT:3306" -e MYSQL_ROOT_PASSWORD=root -e MYSQL_DATABASE=loopforge mysql:8.4 >/dev/null
  until docker exec lf-workbench-mysql mysql -uroot -proot -e 'SELECT 1' >/dev/null 2>&1; do sleep 1; done
fi

(cd "$ROOT" && GOCACHE=/tmp/agentflow-gocache go build -o bin/agentflow ./cmd/agentflow)

if [ -f "$STATE/server.pid" ]; then
  kill "$(cat "$STATE/server.pid")" 2>/dev/null || true
  sleep 1
fi

cd "$ROOT"
AGENTFLOW_MYSQL_DSN="root:root@tcp(127.0.0.1:$MYSQL_PORT)/loopforge?parseTime=true&charset=utf8mb4" \
AGENTFLOW_HTTP_PORT="$PORT" AGENTFLOW_AUTH_REQUIRED=false AGENTFLOW_SCHEDULER_ENABLED=false \
AGENTFLOW_LOCAL_LOGIN_USER="${AGENTFLOW_LOCAL_LOGIN_USER:-local}" AGENTFLOW_PREVIEW_LOCAL_LOGIN=1 \
AGENTFLOW_MANAGED_CLI_ROOT="$STATE/cli" AGENTFLOW_SESSION_SECRET="${LF_SESSION_SECRET:-lf-workbench-preview}" \
  # Linux detaches the server with setsid; macOS has no setsid, where nohup
  # plus a background job is enough to survive the parent shell.
  if command -v setsid >/dev/null 2>&1; then
    setsid "$ROOT/bin/agentflow" -workspaces "$STATE/workspaces" >"$STATE/server.log" 2>&1 </dev/null &
  else
    nohup "$ROOT/bin/agentflow" -workspaces "$STATE/workspaces" >"$STATE/server.log" 2>&1 </dev/null &
  fi
echo $! >"$STATE/server.pid"
for _ in $(seq 1 30); do
  curl -fsS "http://127.0.0.1:$PORT/api/health" >/dev/null 2>&1 && break
  sleep 1
done
HOST_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
[ -n "$HOST_IP" ] || HOST_IP="$(ipconfig getifaddr en0 2>/dev/null || true)"
echo "workbench: http://${HOST_IP:-127.0.0.1}:$PORT/#/collaboration"
