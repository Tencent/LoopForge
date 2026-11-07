#!/usr/bin/env bash
# Explicit local installation, schema initialization and catalog installation.
set -euo pipefail
cd "$(dirname "$0")/.."
bash scripts/configure.sh
set -a
source .env
set +a
mkdir -p .local/bin .local/workspaces
docker compose up -d --wait mysql
bash scripts/build-binaries.sh
if [[ ! -f .local/admin-password ]]; then
  (umask 077; python3 -c 'import secrets; print(secrets.token_urlsafe(18))' > .local/admin-password)
fi
.local/bin/loopforge-account -user admin -admin < .local/admin-password
bash scripts/install-observability-schema.sh
docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge' < migrations/seed/001_public_catalog.sql
docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge' < migrations/seed/002_welcome_project.sql
printf 'Open http://localhost:%s — account: admin — password: .local/admin-password\n' "$AGENTFLOW_HTTP_PORT"
export AGENTFLOW_RUNNER_BINARY="$PWD/.local/bin/agentflow-runner-{os}-{arch}"
export AGENTFLOW_GATE_MCP_BINARY="$PWD/.local/bin/agentflow-gate-mcp-{os}-{arch}"
printf '%s\n' "$$" > .local/platform.pid
exec .local/bin/agentflow -addr "${LOOPFORGE_BIND:-0.0.0.0}:$AGENTFLOW_HTTP_PORT" -workspaces "$PWD/.local/workspaces"
