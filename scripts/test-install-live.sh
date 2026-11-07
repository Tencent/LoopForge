#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
root="$PWD"
image="${LOOPFORGE_INSTALL_TEST_IMAGE:?Set LOOPFORGE_INSTALL_TEST_IMAGE to the locally built platform image}"
task_dir=$(mktemp -d /tmp/loopforge-install-live.XXXXXX)
chmod 755 "$task_dir"
docker_socket="${LOOPFORGE_DOCKER_SOCKET:-/var/run/docker.sock}"
cleanup() {
  for fixture in "$task_dir"/*; do
    [[ -f "$fixture/.env" ]] || continue
    docker compose -f "$root/compose.yaml" --env-file "$fixture/.env" --profile platform down --volumes --remove-orphans >/dev/null 2>&1 || true
  done
  rm -rf "$task_dir"
}
trap cleanup EXIT

actors=(root nonroot)
[[ -z "${LOOPFORGE_INSTALL_SUDO_TEST_IMAGE:-}" ]] || actors+=(sudo)
for actor in "${actors[@]}"; do
  fixture="$task_dir/$actor"
  mkdir -p "$fixture/scripts" "$fixture/migrations/seed" "$fixture/user-home"
  cp scripts/{install.sh,configure.sh,docker-access.sh,install-address.py,install-observability-schema.sh} "$fixture/scripts/"
  cp migrations/001_run_obs_generated_columns.sql "$fixture/migrations/"
  cp migrations/seed/*.sql "$fixture/migrations/seed/"
  cp compose.yaml "$fixture/"
  read -r http_port mysql_port < <(python3 - <<'PY'
import socket
with socket.socket() as http, socket.socket() as mysql:
    http.bind(('0.0.0.0',0)); mysql.bind(('127.0.0.1',0))
    print(http.getsockname()[1],mysql.getsockname()[1])
PY
)
  prefix=()
  if [[ "$actor" == nonroot ]]; then
    [[ $(id -u) == 0 ]] || { echo 'Non-root live check requires root to change only the fixture ownership and drop privileges.' >&2; exit 1; }
    chown -R 65534:65534 "$fixture"
    prefix=(setpriv --reuid=65534 --regid="$(stat -c %g "$docker_socket")" --clear-groups)
  elif [[ "$actor" == sudo ]]; then
    chown -R 1000:1000 "$fixture"
    prefix=(docker run --rm --network host --user 1000:1000 -v "$fixture:$fixture" -v "$docker_socket:$docker_socket" -v "${LOOPFORGE_TEST_COMPOSE_PLUGIN:-/usr/libexec/docker/cli-plugins/docker-compose}:/usr/local/lib/docker/cli-plugins/docker-compose:ro" -w "$fixture" --entrypoint /usr/bin/env "$LOOPFORGE_INSTALL_SUDO_TEST_IMAGE")
    "${prefix[@]}" bash -c 'if docker info >/dev/null 2>&1; then exit 1; fi; sudo -n docker info >/dev/null'
  fi
  started=$SECONDS
  for attempt in first repeat; do
    "${prefix[@]}" env HOME="$fixture/user-home" PATH=/usr/local/bin:/usr/bin:/bin COMPOSE_PROJECT_NAME="lf-install-${actor}-$$" LOOPFORGE_IMAGE="$image" LOOPFORGE_DATA_DIR="$fixture/data" LOOPFORGE_BIND=0.0.0.0 LOOPFORGE_PUBLIC_URL="http://192.0.2.10:$http_port" LOOPFORGE_DOCKER_SOCKET="$docker_socket" AGENTFLOW_HTTP_PORT="$http_port" LOOPFORGE_MYSQL_PORT="$mysql_port" bash "$fixture/scripts/install.sh" > "$fixture/$attempt.log" 2>&1 || { cat "$fixture/$attempt.log"; exit 1; }
    grep -q "Access: http://192.0.2.10:$http_port" "$fixture/$attempt.log"
    curl -fsS "http://127.0.0.1:$http_port/api/health" >/dev/null
    if [[ "$attempt" == first ]]; then
      cp "$fixture/.env" "$fixture/initial.env"
    else
      cmp "$fixture/initial.env" "$fixture/.env"
    fi
  done
  docker compose -f "$fixture/compose.yaml" --env-file "$fixture/.env" --profile platform port platform 8080 | grep -q "0.0.0.0:$http_port"
  python3 - "$http_port" <<'PY'
import json,secrets,socket,sys,urllib.request
base='http://127.0.0.1:'+sys.argv[1]
setup=json.load(urllib.request.urlopen(base+'/api/auth/setup'))
assert setup['needsSetup'],setup
assert urllib.request.urlopen(base+'/app/github-connect.js').status==200
with socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as probe:
    probe.connect(('192.0.2.1',80))
    address=probe.getsockname()[0]
direct=urllib.request.build_opener(urllib.request.ProxyHandler({}))
assert direct.open('http://'+address+':'+sys.argv[1]+'/api/health').status==200
request=urllib.request.Request(base+'/api/auth/register',data=json.dumps({'login':'install-test-admin','password':secrets.token_urlsafe(24)}).encode(),headers={'Content-Type':'application/json','Origin':base})
result=json.load(urllib.request.urlopen(request))
assert result['admin'],result
print('Verified non-loopback HTTP access, first-account creation and GitHub connection UI')
PY
  printf '%s install + repeat: %ss (UID %s)\n' "$actor" "$((SECONDS-started))" "$("${prefix[@]}" id -u)"
done
