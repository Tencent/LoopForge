#!/usr/bin/env bash
# Build and validate an isolated public installation, then remove only its data.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT_DIR="$PWD"
mkdir -p .local
TASK_DIR="$(mktemp -d "$ROOT_DIR/.local/container-e2e.XXXXXX")"
TASK_PROJECT="loopforge-e2e-$(date +%s)-$$"
TASK_PORT="${LOOPFORGE_CONTAINER_TEST_PORT:-8093}"
TASK_MYSQL_PORT="${LOOPFORGE_CONTAINER_MYSQL_PORT:-33317}"
export TASK_DIR TASK_PORT TASK_MYSQL_PORT TASK_PROJECT
python3 - <<'PY'
import os,secrets,pathlib
root=pathlib.Path(os.environ['TASK_DIR'])
values={'LOOPFORGE_DB_PASSWORD':secrets.token_hex(24),'LOOPFORGE_DB_ROOT_PASSWORD':secrets.token_hex(24),'AGENTFLOW_SESSION_SECRET':secrets.token_hex(32),'AGENTFLOW_HTTP_PORT':os.environ['TASK_PORT'],'LOOPFORGE_MYSQL_PORT':os.environ['TASK_MYSQL_PORT'],'COMPOSE_PROJECT_NAME':os.environ['TASK_PROJECT'],'LOOPFORGE_DATA_DIR':str(root/'data')}
(root/'data'/'home').mkdir(parents=True);(root/'data'/'workspaces').mkdir()
for name,data in [('install.env',''.join(f'{k}="{v}"\n' for k,v in values.items())),('admin-password',secrets.token_urlsafe(20)),('no-key-password',secrets.token_urlsafe(20))]:
    p=root/name;p.write_text(data);p.chmod(0o600)
PY
if [[ -n "${LOOPFORGE_CONTAINER_TEST_IMAGE:-}" ]]; then
  python3 - <<'PY_IMAGE'
import json,os,pathlib
p=pathlib.Path(os.environ['TASK_DIR'])/'image.json'
p.write_text(json.dumps({'services':{'platform':{'image':os.environ['LOOPFORGE_CONTAINER_TEST_IMAGE']}}}))
PY_IMAGE
fi
COMPOSE=(docker compose -f "$ROOT_DIR/compose.yaml")
if [[ -f "$TASK_DIR/image.json" ]]; then COMPOSE+=(-f "$TASK_DIR/image.json"); fi
COMPOSE+=( --env-file "$TASK_DIR/install.env" --project-name "$TASK_PROJECT")
cleanup() {
  docker ps -aq --filter "network=${TASK_PROJECT}_default" | xargs -r docker rm -f > /dev/null 2>&1 || true
  "${COMPOSE[@]}" --profile platform down --volumes --remove-orphans > /dev/null 2>&1 || true
  rm -rf "$TASK_DIR"
}
trap cleanup EXIT
if [[ "${LOOPFORGE_CONTAINER_SKIP_BUILD:-0}" != '1' ]]; then
  "${COMPOSE[@]}" --profile platform build platform
fi
"${COMPOSE[@]}" --profile platform up --no-build -d --wait
TASK_READY=0
for attempt in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:$TASK_PORT/api/health" > /dev/null 2>&1; then TASK_READY=1; break; fi
  sleep 1
done
if [[ "$TASK_READY" != '1' ]]; then echo 'Container did not become ready' >&2; exit 1; fi
# Fresh schema only; keep migrations explicit and outside application startup.
for sql in migrations/001_run_obs_generated_columns.sql migrations/seed/001_public_catalog.sql migrations/seed/002_welcome_project.sql; do
  "${COMPOSE[@]}" exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge' < "$sql"
done
export LOOPFORGE_TEST_URL="http://127.0.0.1:$TASK_PORT"
export LOOPFORGE_TEST_PASSWORD
LOOPFORGE_TEST_PASSWORD="$(cat "$TASK_DIR/admin-password")"
export LOOPFORGE_UNCONFIGURED_USER=e2e-no-key
export LOOPFORGE_UNCONFIGURED_PASSWORD_FILE="$TASK_DIR/no-key-password"
# Accounts are created through the browser exactly as a new user would.
LOOPFORGE_FRESH_INSTALL=1 npx --prefix e2e playwright test --config e2e/public/playwright.config.mjs 00-first-run
"${COMPOSE[@]}" exec -T platform test -s /app/LICENSE
"${COMPOSE[@]}" exec -T platform test -s /app/THIRD_PARTY_NOTICES.md
"${COMPOSE[@]}" exec -T platform test -s /app/third_party/go/THIRD_PARTY_NOTICES.txt
"${COMPOSE[@]}" exec -T platform sh -c 'codebuddy --version && codex --version && claude --version'
for task_os in linux darwin; do
  for task_arch in amd64 arm64; do
    "${COMPOSE[@]}" exec -T platform test -x "/app/bin/agentflow-runner-$task_os-$task_arch"
    "${COMPOSE[@]}" exec -T platform test -x "/app/bin/agentflow-gate-mcp-$task_os-$task_arch"
  done
done
node e2e/container-persistence.mjs prepare "$TASK_DIR/persistence.json"
"${COMPOSE[@]}" --profile platform up --no-build -d --force-recreate platform
TASK_READY=0
for attempt in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:$TASK_PORT/api/health" > /dev/null 2>&1; then TASK_READY=1; break; fi
  sleep 1
done
if [[ "$TASK_READY" != '1' ]]; then echo 'Recreated container did not become ready' >&2; exit 1; fi
node e2e/container-persistence.mjs verify "$TASK_DIR/persistence.json"
# This isolated installation tests packaging and script execution without model credentials.
AGENTFLOW_E2E_MODEL=0 npx --prefix e2e playwright test --config e2e/public/playwright.config.mjs
export AGENTFLOW_E2E_BASE="$LOOPFORGE_TEST_URL"
export AGENTFLOW_E2E_TOKEN_FILE="$TASK_DIR/api-token"
export AGENTFLOW_E2E_MODEL=0
export AGENTFLOW_E2E_DOCKER=0
node e2e/setup-auth.mjs
node --test --test-concurrency=1 --test-reporter=spec e2e/api/
npx --prefix e2e playwright test --config e2e/playwright.config.mjs
