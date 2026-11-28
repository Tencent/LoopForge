#!/usr/bin/env bash
# Install the Postgres equivalents of the Compose MySQL extension steps:
# the observability generated columns and the starter catalog seed.
# The DSN comes from AGENTFLOW_DATABASE_URL in .env, unless the caller
# exported one already — the caller's DSN wins.
#
# This script is idempotent: run it as many times as you like.
#
# Precondition: the platform tables must already exist. They are created by
# gorm AutoMigrate when the platform opens the store — scripts/dev.sh runs the
# loopforge-account step before this script for exactly that reason. Running
# this against an empty database fails with 'relation "run_telemetry" does not
# exist'.
#
# Limitation: the MySQL counterpart (scripts/install-schema-extensions.sh) also
# replays migrations 033/038/043. The Postgres side does not port them — the
# seed files already encode their end state — so an existing Postgres
# installation will not pick up their effects when the seed is updated.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/pg-connection-env.sh

# The .env holds the runtime DSN. The caller may point the installer at
# another database — the scratch test database of docs/testing/postgres.md —
# so the caller's DSN wins over the .env default. The .env is still read, so
# the settings it carries keep working.
#
# The .env is sourced with 'set +u': a hand-written .env may reference a
# variable that is not set, and an unbound reference would otherwise abort the
# script even though the caller exported a usable DSN. 'set -e' stays on, so a
# command in the .env that really fails still stops the script.
caller_dsn="${AGENTFLOW_DATABASE_URL:-}"
if [[ -f .env ]]; then
  set +u
  set -a
  source .env
  set +a
  set -u
fi
if [[ -n "$caller_dsn" ]]; then
  export AGENTFLOW_DATABASE_URL="$caller_dsn"
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

# The DSN becomes the libpq environment variables, so that the password
# travels in PGPASSWORD instead of on the command line: 'ps' shows the
# command line of every process on the machine, but the environment of a
# process is readable only by its owner and by root. This mirrors the
# upstream MySQL path, which passes its password in MYSQL_PWD.
pg_connection_env "$dsn"

apply_sql() {
  local file="$1"
  printf 'Applying %s\n' "$file"
  if ! psql -w -v ON_ERROR_STOP=1 --single-transaction -f "$file"; then
    printf 'Failed to apply %s\n' "$file" >&2
    exit 1
  fi
}

# The generated columns come first: the seeded rows are computed against them.
apply_sql migrations/001_run_obs_generated_columns.pg.sql
apply_sql migrations/seed/pg/001_public_catalog.sql
apply_sql migrations/seed/pg/002_welcome_project.sql
printf 'Postgres schema extensions installed.\n'
