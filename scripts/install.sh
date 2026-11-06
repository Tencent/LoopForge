#!/usr/bin/env bash
# One-command container installation. Installs Docker when it is missing
# (Linux, or macOS with Homebrew) and needs Python 3. Re-running is safe:
# secrets, data and catalog edits are kept.
set -euo pipefail
cd "$(dirname "$0")/.."

fail() { printf '\n✗ %s\n' "$1" >&2; exit 1; }
step() { printf '\n▸ %s\n' "$1"; }
source scripts/docker-access.sh

confirm() {
  [[ "${LOOPFORGE_YES:-}" == 1 ]] && return 0
  [[ -t 0 ]] || return 1
  read -r -p "$1 [Y/n] " answer
  [[ -z "$answer" || "$answer" =~ ^[Yy] ]]
}

install_docker() {
  case "$(uname -s)" in
    Linux)
      confirm 'Docker is not installed. Install it now with the official script (get.docker.com)?' \
        || fail 'Docker is required. Install it from https://docs.docker.com/engine/install/ or rerun with LOOPFORGE_YES=1.'
      command -v curl >/dev/null || fail 'curl is required to install Docker.'
      installer=$(mktemp)
      curl --connect-timeout 10 --max-time 120 -fsSL https://get.docker.com -o "$installer" || { rm -f "$installer"; fail 'Could not download the Docker installer.'; }
      as_root sh "$installer"
      rm -f "$installer"
      as_root systemctl enable --now docker 2>/dev/null || as_root service docker start
      ;;
    Darwin)
      command -v brew >/dev/null && confirm 'Docker is not installed. Install Docker Desktop with Homebrew?' \
        || fail 'Install Docker Desktop from https://docs.docker.com/desktop/setup/install/mac-install/ and run this script again.'
      brew install --cask docker
      open -a Docker
      ;;
    *) fail 'Install Docker from https://docs.docker.com/get-docker/ and run this script again.' ;;
  esac
}

wait_for_docker() {
  for _ in $(seq 1 30); do select_docker_access && return 0; sleep 2; done
  return 1
}

step 'Checking prerequisites'
if ! command -v python3 >/dev/null; then
  if command -v apt-get >/dev/null && confirm 'Python 3 is required. Install it now?'; then
    as_root apt-get update
    as_root apt-get install -y python3
  else
    fail 'Install Python 3, then rerun this script. No deployment has been started.'
  fi
fi
command -v docker >/dev/null || install_docker
if ! select_docker_access; then
  case "$(uname -s)" in
    Linux) as_root systemctl start docker 2>/dev/null || as_root service docker start 2>/dev/null || true ;;
    Darwin) open -a Docker 2>/dev/null || true ;;
  esac
  wait_for_docker || fail 'Docker is installed but not running, or this user cannot access it (try: sudo usermod -aG docker "$USER", then log in again).'
fi
docker_access compose version >/dev/null 2>&1 || fail 'Docker Compose v2 is required (the "docker compose" command).'

requested_public_url="${LOOPFORGE_PUBLIC_URL:-}"
[[ ! -e .env || -r .env ]] || fail 'The existing .env is not readable by this user. Use the installation owner or ask an administrator to correct its ownership.'
bash scripts/configure.sh
[[ -r .env ]] || fail 'The existing .env is not readable by this user. Use the installation owner or ask an administrator to correct its ownership.'
set -a; source .env; set +a
export LOOPFORGE_BIND="${LOOPFORGE_BIND:-127.0.0.1}"
if [[ -n "$requested_public_url" ]]; then export LOOPFORGE_PUBLIC_URL="$requested_public_url"; fi
python3 scripts/install-address.py --prepare "${AGENTFLOW_HTTP_PORT:-8090}"
set -a; source .env; set +a
if [[ -z "${LOOPFORGE_DOCKER_SOCKET:-}" ]]; then
  docker_host="${DOCKER_HOST:-$(docker_access context inspect --format '{{(index .Endpoints "docker").Host}}' 2>/dev/null || true)}"
  case "$docker_host" in
    unix://*) export LOOPFORGE_DOCKER_SOCKET="${docker_host#unix://}" ;;
    '') export LOOPFORGE_DOCKER_SOCKET=/var/run/docker.sock ;;
    *) fail 'This installer requires a local Docker socket for sandbox bind mounts. Select a local/rootless Docker context or configure remote workers manually.' ;;
  esac
  python3 - <<'PY'
import os,pathlib,shlex
with pathlib.Path('.env').open('a') as config:
    config.write('\nLOOPFORGE_DOCKER_SOCKET='+shlex.quote(os.environ['LOOPFORGE_DOCKER_SOCKET'])+'\n')
PY
fi
python3 - <<'PY'
import runpy
runpy.run_path('scripts/install-address.py')['access_addresses']('8090')
PY
mkdir -p "$LOOPFORGE_DATA_DIR/home" "$LOOPFORGE_DATA_DIR/workspaces" || fail 'The data directory is not writable. Set LOOPFORGE_DATA_DIR in .env to a directory owned by this user.'
port="${AGENTFLOW_HTTP_PORT:-8090}"
if ! docker_access compose ps --status running --services 2>/dev/null | grep -qx platform; then
  if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
    fail "Port $port is already in use. Set AGENTFLOW_HTTP_PORT in .env to a free port and run again."
  fi
fi

if [[ -n "${LOOPFORGE_IMAGE:-}" ]]; then
  step 'Pulling the prebuilt LoopForge image'
  docker_access compose --profile platform pull --policy "${LOOPFORGE_PULL_POLICY:-missing}" platform mysql
  docker_access compose --profile platform up -d --no-build --wait --wait-timeout 180
else
  step 'Building LoopForge from source (first install takes several minutes; frontend, Go and runtime stages build in parallel)'
  docker_access compose --profile platform build platform
  step 'Starting LoopForge'
  docker_access compose --profile platform up -d --no-build --wait --wait-timeout 180
fi
step 'Installing the schema extensions and the starter catalog'
bash scripts/install-observability-schema.sh
cat migrations/seed/*.sql | docker_access compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge'

python3 scripts/install-address.py "$port"
users=$(docker_access compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -N -u loopforge loopforge -e "select count(*) from users"' 2>/dev/null || echo 0)
if [[ "${users//[^0-9]/}" == 0 ]]; then
  printf '  Open it in your browser and create the administrator account.\n'
else
  printf '  Open it in your browser and sign in.\n'
fi
printf '  Open “一键体验” on the home page to run a real SOP without an API key.\n'
printf '  Default sandboxes reuse the installed runtime image; no second CLI image build is needed.\n'
printf '  Stop: docker compose --profile platform stop   ·   Logs: docker compose logs -f platform\n'
