#!/usr/bin/env bash
# Explicit operator step for the Compose database, after base tables exist.
# Apply each absent generated-column definition from the upstream migration.
set -euo pipefail
cd "$(dirname "$0")/.."

mysql_query() {
  docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" exec mysql --batch --skip-column-names -u loopforge loopforge'
}

for entry in 'run_telemetry adapter' 'run_logs level'; do
  read -r table_name column_name <<< "$entry"
  column_count="$(printf "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='%s' AND column_name='%s';\n" "$table_name" "$column_name" | mysql_query)"
  if [[ "$column_count" == '1' ]]; then
    printf 'Already installed: %s.%s\n' "$table_name" "$column_name"
    continue
  fi
  if [[ "$column_count" != '0' ]]; then
    printf 'Could not inspect %s.%s\n' "$table_name" "$column_name" >&2
    exit 1
  fi
  python3 - "$table_name" <<'PY' | mysql_query
from pathlib import Path
import re
import sys
sql = Path('migrations/001_run_obs_generated_columns.sql').read_text()
match = re.search(r'ALTER TABLE ' + re.escape(sys.argv[1]) + r'\b.*?;', sql, re.S)
if not match:
    raise SystemExit('Missing upstream generated-column definition')
print(match.group())
PY
  printf 'Installed: %s.%s\n' "$table_name" "$column_name"
done
