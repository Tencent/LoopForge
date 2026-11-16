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
DOWNLOAD_DIR="$MOCK_ROOT/downloads/Tencent/LoopForge/releases/download/local-v0.2.3"
mkdir -p "$API_DIR" "$DOWNLOAD_DIR"
cat > "$API_DIR/releases-1" <<'JSON'
[
  {
    "tag_name": "v0.3.8",
    "prerelease": false,
    "assets": [{"name": "loopforge-platform.tar.gz"}]
  },
  {
    "tag_name": "local-v0.2.4-rc.1",
    "prerelease": true,
    "assets": [{"name": "loopforge-workflow.tar.gz"}, {"name": "loopforge-workflow.tar.gz.sha256"}]
  }
]
JSON
cat > "$API_DIR/releases-2" <<'JSON'
[
  {
    "tag_name": "local-v0.2.3",
    "prerelease": false,
    "assets": [{"name": "loopforge-workflow.tar.gz"}, {"name": "loopforge-workflow.tar.gz.sha256"}]
  }
]
JSON
cp "$RELEASE_DIR/loopforge-workflow.tar.gz"* "$DOWNLOAD_DIR/"

PORT="$(python3 - <<'PY'
import socket

with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)"
python3 - "$PORT" "$MOCK_ROOT" >"$TMP_DIR/http.log" 2>&1 <<'PY' &
import os
import sys
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit

os.chdir(sys.argv[2])

class ReleasesHandler(SimpleHTTPRequestHandler):
    def do_GET(self):
        request = urlsplit(self.path)
        if request.path == "/api/repos/Tencent/LoopForge/releases":
            page = parse_qs(request.query).get("page", ["1"])[0]
            self.path = request.path + "-" + page
        super().do_GET()

ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), ReleasesHandler).serve_forever()
PY
SERVER_PID=$!
BASE_URL="http://127.0.0.1:$PORT"

for _ in {1..20}; do
  if curl -fsS "$BASE_URL/api/repos/Tencent/LoopForge/releases?per_page=100&page=1" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
curl -fsS "$BASE_URL/api/repos/Tencent/LoopForge/releases?per_page=100&page=1" >/dev/null

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
  --release-base-url "$BASE_URL/downloads/Tencent/LoopForge/releases/download/local-v0.2.3"
test -f "$PORTABLE_PROJECT/.claude/skills/devflow/SKILL.md"
test -f "$PORTABLE_PROJECT/.devflow/install-state.json"

PINNED_PROJECT="$TMP_DIR/pinned-project"
mkdir -p "$PINNED_PROJECT"
cat "$ROOT_DIR/install.sh" | LOOPFORGE_ALLOW_INSECURE=1 \
  LOOPFORGE_GITHUB_URL="$BASE_URL/downloads" \
  sh -s -- codex --version local-v0.2.3 --project-root "$PINNED_PROJECT"
test -f "$PINNED_PROJECT/.codex/README.md"

echo "curl installer smoke tests passed"
