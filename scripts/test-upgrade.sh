#!/usr/bin/env bash
# Disposable real upgrade: clone, install the old release, retain user data,
# fetch/checkout the target, and run the same installer twice. No model key.
# Usage: bash scripts/test-upgrade.sh FROM_REF TO_REF EXPECTED_IMAGE
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
[[ $# == 3 ]] || { echo 'Usage: bash scripts/test-upgrade.sh FROM_REF TO_REF EXPECTED_IMAGE' >&2; exit 1; }
from_ref=$1
to_ref=$2
expected_image=$3
git -C "$root" rev-parse --verify "${from_ref}^{commit}" >/dev/null
git -C "$root" rev-parse --verify "${to_ref}^{commit}" >/dev/null
source "$root/scripts/docker-access.sh"
select_docker_access || docker_access_fail 'Docker is required for the upgrade test.'
node --input-type=module -e 'if (Number(process.versions.node.split(".")[0]) < 22) throw new Error("Node.js 22+ is required")'

umask 077
task_dir=$(mktemp -d /tmp/loopforge-upgrade.XXXXXX)
fixture="$task_dir/LoopForge"
compose() { (cd "$fixture" && docker_access compose --profile platform "$@"); }
cleanup() {
  status=$?
  trap - EXIT
  if [[ -f "$fixture/.env" ]]; then
    if [[ $status != 0 ]]; then compose logs --no-color --tail 150 platform mysql || true; fi
    # Only this test's bind mount: container-created workspaces may be root-owned.
    compose exec -T platform chmod -R a+rwX "$LOOPFORGE_DATA_DIR" >/dev/null 2>&1 || true
    # A failed waiting SOP can leave a sandbox outside Compose's labels. All
    # containers on this randomly named fixture network belong to this test.
    while IFS= read -r container; do
      [[ -n "$container" ]] || continue
      docker_access rm -f "$container" >/dev/null || status=1
    done < <(docker_access ps -aq --filter "network=${COMPOSE_PROJECT_NAME}_default")
    # Script views can share a sandbox's network namespace instead of joining
    # the Compose network. Match their bind mounts to this fixture as well.
    mapfile -t sandbox_candidates < <(docker_access ps -aq --filter name=agentflow-)
    if [[ ${#sandbox_candidates[@]} != 0 ]]; then
      while IFS= read -r container; do
        [[ -n "$container" ]] || continue
        docker_access rm -f "$container" >/dev/null || status=1
      done < <(docker_access inspect --format '{{.Id}} {{json .Mounts}}' "${sandbox_candidates[@]}" | python3 -c '
import json, sys
root = sys.argv[1]
for line in sys.stdin:
    container, mounts = line.split(" ", 1)
    if any(m.get("Type") == "bind" and (m["Source"] == root or m["Source"].startswith(root + "/")) for m in json.loads(mounts)):
        print(container)
' "$LOOPFORGE_DATA_DIR")
    fi
    compose down --volumes --remove-orphans || status=1
  fi
  rm -rf "$task_dir" || status=1
  exit "$status"
}
trap cleanup EXIT

# Never reuse the caller's installation, ports, image override or source build.
project_suffix=${task_dir##*.}
export COMPOSE_PROJECT_NAME="lf-upgrade-${project_suffix,,}"
export LOOPFORGE_DATA_DIR="$task_dir/data"
unset LOOPFORGE_WORKSPACE_HOST
export LOOPFORGE_IMAGE='' COMPOSE_FILE=compose.yaml LOOPFORGE_BUILD_FROM_SOURCE=0
export LOOPFORGE_BIND=127.0.0.1 LOOPFORGE_PUBLIC_URL='' LOOPFORGE_PULL_POLICY=missing LOOPFORGE_YES=1
export AGENTFLOW_ALLOW_SIGNUP=true AGENTFLOW_SECURE_COOKIES=false
read -r AGENTFLOW_HTTP_PORT LOOPFORGE_MYSQL_PORT < <(python3 - <<'PY'
import socket
with socket.socket() as http, socket.socket() as mysql:
    http.bind(('127.0.0.1', 0))
    mysql.bind(('127.0.0.1', 0))
    print(http.getsockname()[1], mysql.getsockname()[1])
PY
)
export AGENTFLOW_HTTP_PORT LOOPFORGE_MYSQL_PORT
export LOOPFORGE_TEST_URL="http://127.0.0.1:$AGENTFLOW_HTTP_PORT"

# A clone of the release checkout supplies the same public history without
# fetching the whole repository from GitHub a second time.
git clone --no-hardlinks --quiet "$root" "$fixture"
git -C "$fixture" checkout --quiet --detach "$from_ref"
printf '\nInstalling baseline %s with bash scripts/install.sh\n' "$from_ref"
(cd "$fixture" && bash scripts/install.sh)
node "$root/e2e/upgrade-persistence.mjs" prepare "$task_dir/state.json"
cp "$fixture/.env" "$task_dir/original.env"
before_container=$(compose ps -q platform)
before_image=$(docker_access inspect --format '{{.Config.Image}}' "$before_container")
before_hostname=$(docker_access inspect --format '{{.Config.Hostname}}' "$before_container")
database_volume() { docker_access inspect --format '{{range .Mounts}}{{if eq .Destination "/var/lib/mysql"}}{{.Name}}{{end}}{{end}}' "$(compose ps -q mysql)"; }
before_volume=$(database_volume)
[[ -n "$before_volume" ]] || { echo 'Baseline has no persistent MySQL volume' >&2; exit 1; }

git -C "$fixture" fetch --quiet origin --tags
git -C "$fixture" checkout --quiet --detach "$to_ref"
printf '\nUpgrading %s -> %s with bash scripts/install.sh\n' "$from_ref" "$to_ref"
(cd "$fixture" && bash scripts/install.sh)

# Health alone could pass while .env still pins the old image. Require the
# running image to match the digest actually built by this release job.
if ! docker_access image inspect "$expected_image" >/dev/null 2>&1; then
  docker_access pull "$expected_image" >/dev/null
fi
expected_id=$(docker_access image inspect --format '{{.Id}}' "$expected_image")
after_container=$(compose ps -q platform)
after_image=$(docker_access inspect --format '{{.Config.Image}}' "$after_container")
[[ "$after_image" != "$before_image" && "$after_container" != "$before_container" ]] || { echo 'The upgrade did not replace the old platform container/image' >&2; exit 1; }
verify() {
  [[ "$(docker_access inspect --format '{{.Image}}' "$(compose ps -q platform)")" == "$expected_id" ]] || { echo 'The running platform does not match the release image' >&2; exit 1; }
  [[ "$(database_volume)" == "$before_volume" ]] || { echo 'The upgrade replaced the database volume' >&2; exit 1; }
  [[ "$(docker_access inspect --format '{{.Config.Hostname}}' "$(compose ps -q platform)")" == "$before_hostname" ]] || { echo 'The upgrade changed the workspace hostname' >&2; exit 1; }
  python3 - "$task_dir/original.env" "$fixture/.env" <<'PY'
from pathlib import Path
import sys
# The legacy upgrade adds a stable workspace hostname. Every existing setting,
# including secrets, image overrides, ports and the data path must stay intact.
def settings(path):
    return [line for line in Path(path).read_text().splitlines() if line.strip() and not line.startswith('LOOPFORGE_WORKSPACE_HOST=')]
assert settings(sys.argv[1]) == settings(sys.argv[2]), 'Existing .env settings changed during upgrade'
PY
  node "$root/e2e/upgrade-persistence.mjs" verify "$task_dir/state.json"
}
verify

printf '\nRepeating installation to check upgrade idempotence\n'
(cd "$fixture" && bash scripts/install.sh)
verify
printf '\nUpgrade %s -> %s passed, including repeat installation.\n' "$from_ref" "$to_ref"
