#!/usr/bin/env bash
# Start LoopForge from the published GHCR image pinned in compose.yaml
# (default), or build this checkout for development.
#
# Usage:
#   ./start.sh            install/start in the background (same as -d)
#   ./start.sh -d         install/start in the background
#   ./start.sh logs       follow platform logs
#   ./start.sh stop       stop containers, keep them and their data
#   ./start.sh down       remove containers, keep data volumes
#   ./start.sh restart    restart the platform container
#   ./start.sh pull       refresh images and recreate changed containers
#   ./start.sh dev        build this checkout (compose.dev.yaml) and start it
#   ./start.sh release    switch a source-built installation back to GHCR
#   MIRROR=ghcr.nju.edu.cn ./start.sh
#                         pull ghcr.io images from that host (and MySQL from
#                         docker.nju.edu.cn) and tag them back; a failed mirror
#                         pull falls back to the original registry
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source scripts/docker-access.sh
source scripts/ghcr-mirror.sh

compose() {
  [[ -f .env ]] || docker_access_fail 'Not installed yet. Run ./start.sh first.'
  select_docker_access || docker_access_fail 'Docker is not running, or this user cannot access it.'
  docker_access compose --profile platform "$@"
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
    # An empty MIRROR keeps the original refresh. A registry host pulls each image
    # it can serve and tags it back; the rest refresh from their own registry.
    if [[ -n "${MIRROR:-}${DOCKERHUB_MIRROR:-}" ]]; then
      [[ -f .env ]] || docker_access_fail 'Not installed yet. Run ./start.sh first.'
      select_docker_access || docker_access_fail 'Docker is not running, or this user cannot access it.'
    fi
    services=()
    mirror_published_ghcr always || services+=(platform)
    mirror_dockerhub_mysql always || services+=(mysql)
    if (( ${#services[@]} )); then
      compose pull --ignore-buildable --policy always "${services[@]}"
    fi
    compose up -d --no-build --wait --wait-timeout 180
    ;;
  logs) compose logs -f "${@:-platform}" ;;
  stop) compose stop ;;
  down) compose down --remove-orphans ;;
  restart) compose restart platform ;;
  build)
    echo 'The default start uses the published GHCR image (no local build).' >&2
    echo 'For a source build: ./start.sh dev' >&2
    exit 1
    ;;
  *)
    echo "unknown: $cmd" >&2
    echo 'usage: ./start.sh [-d|logs|stop|down|restart|pull|dev|release]' >&2
    exit 1
    ;;
esac
