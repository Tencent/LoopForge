#!/usr/bin/env bash
# Install the Postgres equivalents of the Compose MySQL extension steps:
# the observability generated columns and the starter catalog seed.
# The DSN comes from AGENTFLOW_DATABASE_URL in .env.
#
# This script is idempotent: run it as many times as you like.
#
# Limitation: the MySQL counterpart (scripts/install-schema-extensions.sh) also
# replays migrations 033/038/043. The Postgres side does not port them — the
# seed files already encode their end state — so an existing Postgres
# installation will not pick up their effects when the seed is updated.
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ -f .env ]]; then
  set -a
  source .env
  set +a
fi

dsn="${AGENTFLOW_DATABASE_URL:-}"
if [[ -z "$dsn" ]]; then
  printf 'AGENTFLOW_DATABASE_URL is not set. Run scripts/configure.sh first.\n' >&2
  exit 1
fi

if ! command -v psql >/dev/null 2>&1; then
  printf 'psql is not installed. Install the PostgreSQL client tools first.\n' >&2
  exit 1
fi

apply_sql() {
  local file="$1"
  printf 'Applying %s\n' "$file"
  if ! psql "$dsn" -v ON_ERROR_STOP=1 -f "$file"; then
    printf 'Failed to apply %s\n' "$file" >&2
    exit 1
  fi
}

# The generated columns come first: the seeded rows are computed against them.
apply_sql migrations/001_run_obs_generated_columns.pg.sql
apply_sql migrations/seed/pg/001_public_catalog.sql
apply_sql migrations/seed/pg/002_welcome_project.sql
printf 'Postgres schema extensions installed.\n'
