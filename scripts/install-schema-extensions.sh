#!/usr/bin/env bash
# Bring the Compose database up to what this release reads but gorm
# AutoMigrate cannot create: the generated columns from migration 001 and
# the INSERT IGNORE starter catalog. Both steps are safe to repeat, so the
# installer and `./start.sh pull` run it after the platform is healthy.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/docker-access.sh

if ! bash scripts/install-observability-schema.sh; then
  printf '\n✗ Could not add the generated columns run details need.\n' >&2
  printf '  Run migrations/001_run_obs_generated_columns.sql against the LoopForge database, then retry.\n' >&2
  exit 1
fi
cat migrations/seed/*.sql | docker_access compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge'
