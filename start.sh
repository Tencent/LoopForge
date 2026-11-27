#!/usr/bin/env bash
# Start LoopForge from the published GHCR image pinned in compose.yaml
# (default), or build this checkout for development.
#
# Usage:
#   ./start.sh            install/start in the background (same as -d)
#   ./start.sh -d         install/start in the background
#   ./start.sh logs       follow platform logs
#   ./start.sh stop [service]
#                         stop containers and keep their data
#   ./start.sh down       remove containers, keep data volumes
#   ./start.sh restart    restart the platform container
#   ./start.sh pull       refresh images and recreate changed containers
#                         migrate a legacy sandbox catalog before startup
#   ./start.sh migrate-sandboxes
#                         apply migration 041 while the platform is stopped
#   ./start.sh dev        build this checkout (compose.dev.yaml) and start it
#   ./start.sh release    switch a source-built installation back to GHCR
#   ./start.sh            time ghcr.io and ghcr.nju.edu.cn, then pull the faster one
#   MIRROR=ghcr.io ./start.sh
#                         pull ghcr.io directly
#   MIRROR=ghcr.nju.edu.cn ./start.sh
#                         pull ghcr.io images from that host and tag them back
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source scripts/docker-access.sh
source scripts/ghcr-mirror.sh

compose() {
  [[ -f .env ]] || docker_access_fail 'Not installed yet. Run ./start.sh first.'
  select_docker_access || docker_access_fail 'Docker is not running, or this user cannot access it.'
  docker_access compose --profile platform "$@"
}

lock_compose_upgrade() {
  (umask 077 && mkdir -p .local)
  # Keep the lock on the shell's file descriptor until the command exits.
  exec 9>>.local/compose-upgrade.lock
  python3 -c 'import fcntl; fcntl.flock(9, fcntl.LOCK_EX | fcntl.LOCK_NB)' 2>/dev/null ||
    docker_access_fail 'Could not lock the installation. Wait for any running upgrade to finish and retry.'
}

sandbox_mysql() {
  compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" exec mysql --batch --skip-column-names -u loopforge loopforge'
}

apply_sandbox_migration() {
  local active_platform
  active_platform="$(compose ps --all --status running --status restarting --status paused --services platform)"
  if [[ -n "$active_platform" ]]; then
    docker_access_fail 'Stop the platform with ./start.sh stop platform. Keep MySQL running.'
  fi
  if ! sandbox_mysql < migrations/041_native_sandbox_inline_assets.sql; then
    docker_access_fail 'Sandbox migration 041 failed. Fix the migration error and retry with the platform stopped.'
  fi
  printf 'Applied sandbox migration 041.\n'
}

upgrade_sandbox_catalog() {
  local image sandbox_catalog legacy_tables backup_dir backup
  image="$(compose config --format json platform | python3 -c 'import json, sys; print(json.load(sys.stdin)["services"]["platform"]["image"])')"
  sandbox_catalog="$(docker_access image inspect --format '{{index .Config.Labels "io.loopforge.sandbox-catalog"}}' "$image")"
  [[ "$sandbox_catalog" == inline ]] || return 0

  # Start MySQL by itself if the installation is stopped. Keep a running
  # database in place while the old platform is still using it.
  compose up -d --no-build --no-recreate --wait --wait-timeout 180 --pull never mysql
  legacy_tables="$(sandbox_mysql <<'SQL'
SELECT COUNT(*) FROM information_schema.tables
WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'
  AND table_name IN ('dockerfiles', 'environments');
SQL
)"
  case "$legacy_tables" in
    0) return 0 ;;
    1|2) ;;
    *) docker_access_fail 'Could not determine whether the database needs sandbox migration 041.' ;;
  esac

  compose stop platform
  backup_dir="$(mktemp -d .local/loopforge-before-041.XXXXXX)"
  backup="$backup_dir/loopforge.sql"
  if ! (
    umask 077
    compose exec -T mysql sh -c '
      MYSQL_PWD="$MYSQL_PASSWORD" exec mysqldump --single-transaction --quick \
        --no-tablespaces --set-gtid-purged=OFF --hex-blob -u loopforge loopforge
    ' > "$backup.partial"
  ); then
    rm -f -- "$backup.partial"
    rmdir "$backup_dir"
    docker_access_fail 'Database backup failed. Fix the backup error and rerun ./start.sh pull.'
  fi
  mv "$backup.partial" "$backup"
  printf 'Saved the database backup at %s.\n' "$PWD/$backup"
  apply_sandbox_migration
}

cmd="${1:--d}"
shift || true
case "$cmd" in
  up|-d|detach) bash scripts/install.sh ;;
  dev) LOOPFORGE_BUILD_FROM_SOURCE=1 bash scripts/install.sh ;;
  release)
    [[ -f .env ]] && python3 - <<'PY'
import pathlib,re
p=pathlib.Path('.env')
p.write_text(re.sub(r'(?m)^COMPOSE_FILE=.*\n?','',p.read_text()))
PY
    COMPOSE_FILE=compose.yaml bash scripts/install.sh
    ;;
  pull)
    # Docker is selected before the speed test: a faster Nanjing mirror pulls
    # immediately, and the direct ghcr.io path needs it too.
    [[ -f .env ]] || docker_access_fail 'Not installed yet. Run ./start.sh first.'
    select_docker_access || docker_access_fail 'Docker is not running, or this user cannot access it.'
    lock_compose_upgrade
    bash scripts/configure.sh --project-credential-key
    # An exported empty value would override the configured key in Compose.
    [[ -n "${AGENTFLOW_PROJECT_CREDENTIAL_KEY:-}" ]] || unset AGENTFLOW_PROJECT_CREDENTIAL_KEY
    if mirror_published_ghcr always; then
      compose pull --policy always mysql
    else
      compose pull --ignore-buildable --policy always
    fi
    upgrade_sandbox_catalog
    # Use the images just pulled and inspected, including for mutable tags.
    compose up -d --no-build --wait --wait-timeout 180 --pull never
    # A newer image may read generated columns the old volume never got.
    bash scripts/install-schema-extensions.sh
    ;;
  migrate-sandboxes)
    [[ -f .env ]] || docker_access_fail 'Not installed yet. Run ./start.sh first.'
    lock_compose_upgrade
    apply_sandbox_migration
    ;;
  logs) compose logs -f "${@:-platform}" ;;
  stop) compose stop "$@" ;;
  down) compose down --remove-orphans ;;
  restart) compose restart platform ;;
  build)
    echo 'The default start uses the published GHCR image (no local build).' >&2
    echo 'For a source build: ./start.sh dev' >&2
    exit 1
    ;;
  *)
    echo "unknown: $cmd" >&2
    echo 'usage: ./start.sh [-d|logs|stop|down|restart|pull|migrate-sandboxes|dev|release]' >&2
    exit 1
    ;;
esac
