#!/usr/bin/env bash
# Split a Postgres DSN into the libpq environment variables psql reads, so
# that a connection string carrying a password never travels on a command
# line. 'ps' shows the command line of every process on the machine, while
# the environment of a process is readable only by its owner and by root —
# the reason the upstream MySQL path passes its password in MYSQL_PWD.
#
# Usage:
#   source scripts/pg-connection-env.sh
#   pg_connection_env "$AGENTFLOW_DATABASE_URL"
#   psql -v ON_ERROR_STOP=1 --single-transaction -f migrations/seed/pg/001_public_catalog.sql
#
# The helper exports PGHOST, PGPORT, PGUSER, PGPASSWORD, PGDATABASE and the
# libpq variables the DSN's query parameters map to. A query parameter
# without a libpq environment variable is reported as an error rather than
# dropped silently.

# The parser lives next to this file. The path is resolved once, while the
# caller's working directory is still the one it sourced this file from.
PG_CONNECTION_ENV_PARSER="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/pg-connection-env.py"

pg_connection_env() {
  local dsn="${1:-}"
  local assignments
  # The DSN travels on stdin, never as an argument: an argument would show up
  # in the process list of the parser itself.
  assignments="$(printf '%s' "$dsn" | python3 "$PG_CONNECTION_ENV_PARSER")" || return 1
  # The parser prints `export NAME='value'` lines whose values are quoted
  # with shlex.quote, so every value is a shell literal and cannot break out
  # of its assignment.
  eval "$assignments"
}
