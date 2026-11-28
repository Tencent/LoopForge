#!/usr/bin/env bash
# Explicit local installation, schema initialization and catalog installation.
set -euo pipefail
cd "$(dirname "$0")/.."
bash scripts/configure.sh
# The .env is sourced with 'set +u': a hand-written .env may reference a
# variable that is not set, and an unbound reference would abort the script
# under 'set -u'. 'set -e' stays on, so a command in the .env that really
# fails still stops the script.
set +u
set -a
source .env
set +a
set -u
mkdir -p .local/bin .local/workspaces
# The DSN decides the dialect. AGENTFLOW_DATABASE_URL accepts a Postgres URL
# and a MySQL DSN alike, so the presence of the variable is not enough.
database_is_postgres() {
  case "${AGENTFLOW_DATABASE_URL:-}" in
    postgres://*|postgresql://*) return 0 ;;
    *) return 1 ;;
  esac
}
if ! database_is_postgres; then
  # Upstream default path: the database is a service of the Compose stack.
  # This fork's compose.yaml defines no mysql service, so an installation
  # against an own MySQL server seeds its catalog by hand.
  compose_mysql=0
  if docker compose config --services 2>/dev/null | grep -qx mysql; then
    compose_mysql=1
    docker compose up -d --wait mysql
    docker compose --profile platform run --rm --no-deps artifact-migration
  fi
fi
bash scripts/build-binaries.sh
if [[ ! -f .local/admin-password ]]; then
  (umask 077; python3 -c 'import secrets; print(secrets.token_urlsafe(18))' > .local/admin-password)
fi
# The account command opens the store, so the tables exist before the
# schema extensions and the catalog are installed.
.local/bin/loopforge-account -user admin -admin < .local/admin-password
if database_is_postgres; then
  bash scripts/install-schema-extensions-pg.sh
else
  if [[ "${compose_mysql:-0}" == 1 ]]; then
    bash scripts/install-observability-schema.sh
    docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge' < migrations/seed/001_public_catalog.sql
    docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge' < migrations/seed/002_welcome_project.sql
  else
    # The observability script and the catalog seed drive the Compose stack's
    # own database service. This fork's stack has none, so an installation
    # against an own MySQL server applies them by hand.
    printf "This fork's Compose stack defines no mysql service.\n"
    printf 'Apply migrations/001_run_obs_generated_columns.sql, migrations/seed/001_public_catalog.sql\n'
    printf 'and migrations/seed/002_welcome_project.sql to your MySQL server.\n'
  fi
fi
printf 'Open http://localhost:%s — account: admin — password: .local/admin-password\n' "$AGENTFLOW_HTTP_PORT"
export AGENTFLOW_RUNNER_BINARY="$PWD/.local/bin/agentflow-runner-{os}-{arch}"
export AGENTFLOW_GATE_MCP_BINARY="$PWD/.local/bin/agentflow-gate-mcp-{os}-{arch}"
printf '%s\n' "$$" > .local/platform.pid
exec .local/bin/agentflow -addr "${LOOPFORGE_BIND:-0.0.0.0}:$AGENTFLOW_HTTP_PORT" -workspaces "$PWD/.local/workspaces"
