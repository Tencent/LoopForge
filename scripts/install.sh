#!/usr/bin/env bash
# One-command container installation. Installs Docker when it is missing
# (Linux, or macOS with Homebrew) and needs Python 3. Re-running is safe:
# secrets, data and catalog edits are kept.
set -euo pipefail
cd "$(dirname "$0")/.."

fail() { printf '\n✗ %s\n' "$1" >&2; exit 1; }
step() { printf '\n▸ %s\n' "$1"; }

confirm() {
  [[ "${LOOPFORGE_YES:-}" == 1 ]] && return 0
  [[ -t 0 ]] || return 1
  read -r -p "$1 [Y/n] " answer
  [[ -z "$answer" || "$answer" =~ ^[Yy] ]]
}
as_root() { if [[ $(id -u) -eq 0 ]]; then "$@"; else sudo "$@"; fi; }

install_docker() {
  case "$(uname -s)" in
    Linux)
      confirm 'Docker is not installed. Install it now with the official script (get.docker.com)?' \
        || fail 'Docker is required. Install it from https://docs.docker.com/engine/install/ or rerun with LOOPFORGE_YES=1.'
      command -v curl >/dev/null || fail 'curl is required to install Docker.'
      curl -fsSL https://get.docker.com | as_root sh
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
  for _ in $(seq 1 60); do docker info >/dev/null 2>&1 && return 0; sleep 2; done
  return 1
}

step 'Checking prerequisites'
command -v docker >/dev/null || install_docker
if ! docker info >/dev/null 2>&1; then
  case "$(uname -s)" in
    Linux) as_root systemctl start docker 2>/dev/null || as_root service docker start 2>/dev/null || true ;;
    Darwin) open -a Docker 2>/dev/null || true ;;
  esac
  wait_for_docker || fail 'Docker is installed but not running, or this user cannot access it (try: sudo usermod -aG docker "$USER", then log in again).'
fi
docker compose version >/dev/null 2>&1 || fail 'Docker Compose v2 is required (the "docker compose" command).'
command -v python3 >/dev/null || fail 'Python 3 is required to generate local secrets.'

bash scripts/configure.sh
set -a; source .env; set +a
mkdir -p "$LOOPFORGE_DATA_DIR/home" "$LOOPFORGE_DATA_DIR/workspaces"
port="${AGENTFLOW_HTTP_PORT:-8090}"
if ! docker compose ps --status running --services 2>/dev/null | grep -qx platform; then
  if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
    fail "Port $port is already in use. Set AGENTFLOW_HTTP_PORT in .env to a free port and run again."
  fi
fi

step 'Building and starting LoopForge (the first build takes a few minutes)'
docker compose --profile platform up -d --build --wait
# The schema exists once the API listens; seeds must not run before that.
python3 - "$port" <<'PY' || fail 'LoopForge did not become ready. Inspect: docker compose logs platform'
import sys,time,urllib.request
for _ in range(120):
    try:
        urllib.request.urlopen(f'http://127.0.0.1:{sys.argv[1]}/api/health',timeout=2); sys.exit(0)
    except Exception: time.sleep(1)
sys.exit(1)
PY

step 'Installing the schema extensions and the starter catalog'
bash scripts/install-observability-schema.sh
for seed in migrations/seed/*.sql; do
  docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge' < "$seed"
done

host="${LOOPFORGE_BIND:-127.0.0.1}"
[[ "$host" == 0.0.0.0 ]] && host=localhost
printf '\n✓ LoopForge is running at http://%s:%s\n' "$host" "$port"
users=$(docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -N -u loopforge loopforge -e "select count(*) from users"' 2>/dev/null || echo 0)
if [[ "${users//[^0-9]/}" == 0 ]]; then
  printf '  Open it in your browser and create the administrator account.\n'
else
  printf '  Open it in your browser and sign in.\n'
fi
printf '  AI SOPs run in Docker sandboxes; the sandbox image is building in the background (a few minutes on first install).\n'
printf '  Stop: docker compose --profile platform stop   ·   Logs: docker compose logs -f platform\n'
