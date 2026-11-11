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
# 知识库 schema 由脚本先建 002 基线，再按 026 起的编号执行；控制段进主库，
# 内容段进 AGENTFLOW_KNOWLEDGE_MYSQL_DSN 指向的内容库，并检查 ngram 全文索引。
bash scripts/knowledge-schema.sh apply --native
bash scripts/knowledge-schema.sh check --native
# seed 也走同一个 DSN 解析，不再写死 loopforge 库名。
bash scripts/knowledge-schema.sh seed --native
printf 'Open http://localhost:%s — account: admin — password: .local/admin-password\n' "$AGENTFLOW_HTTP_PORT"
export AGENTFLOW_RUNNER_BINARY="$PWD/.local/bin/agentflow-runner-{os}-{arch}"
export AGENTFLOW_GATE_MCP_BINARY="$PWD/.local/bin/agentflow-gate-mcp-{os}-{arch}"
printf '%s\n' "$$" > .local/platform.pid
exec .local/bin/agentflow -addr "${LOOPFORGE_BIND:-0.0.0.0}:$AGENTFLOW_HTTP_PORT" -workspaces "$PWD/.local/workspaces"
