#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
SERVER_PID=""

cleanup() {
  if [[ -n "$SERVER_PID" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

RELEASE_DIR="$TMP_DIR/release"
bash "$ROOT_DIR/scripts/build-release-archive.sh" "$RELEASE_DIR" >/dev/null

MOCK_ROOT="$TMP_DIR/http-root"
API_DIR="$MOCK_ROOT/api/repos/Tencent/LoopForge"
DOWNLOAD_DIR="$MOCK_ROOT/downloads/Tencent/LoopForge/releases/download/v0.1.0"
mkdir -p "$API_DIR" "$DOWNLOAD_DIR"
printf '[{"tag_name":"v0.1.0","prerelease":false}]\n' > "$API_DIR/releases"
cp "$RELEASE_DIR/loopforge-workflow.tar.gz"* "$DOWNLOAD_DIR/"

PORT="$(python3 - <<'PY'
import socket

with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)"
python3 -m http.server "$PORT" --bind 127.0.0.1 --directory "$MOCK_ROOT" \
  >"$TMP_DIR/http.log" 2>&1 &
SERVER_PID=$!
BASE_URL="http://127.0.0.1:$PORT"

for _ in {1..20}; do
  if curl -fsS "$BASE_URL/api/repos/Tencent/LoopForge/releases?per_page=1" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
curl -fsS "$BASE_URL/api/repos/Tencent/LoopForge/releases?per_page=1" >/dev/null

CLASSIC_PROJECT="$TMP_DIR/classic-project"
mkdir -p "$CLASSIC_PROJECT"
cat "$ROOT_DIR/install.sh" | LOOPFORGE_ALLOW_INSECURE=1 \
  LOOPFORGE_GITHUB_URL="$BASE_URL/downloads" \
  LOOPFORGE_GITHUB_API_URL="$BASE_URL/api" \
  sh -s -- codex --project-root "$CLASSIC_PROJECT"
test -f "$CLASSIC_PROJECT/.codex/README.md"
test -f "$CLASSIC_PROJECT/.agents/skills/devflow-codex/SKILL.md"
test -f "$CLASSIC_PROJECT/.devflow/install-state.json"

PORTABLE_PROJECT="$TMP_DIR/portable-project"
mkdir -p "$PORTABLE_PROJECT"
cat "$ROOT_DIR/install.sh" | LOOPFORGE_ALLOW_INSECURE=1 sh -s -- claude \
  --edition portable --project-root "$PORTABLE_PROJECT" \
  --release-base-url "$BASE_URL/downloads/Tencent/LoopForge/releases/download/v0.1.0"
test -f "$PORTABLE_PROJECT/.claude/skills/devflow/SKILL.md"
test -f "$PORTABLE_PROJECT/.devflow/install-state.json"

echo "curl installer smoke tests passed"
