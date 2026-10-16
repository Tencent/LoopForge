#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT_DIR="${1:-$ROOT_DIR/dist/release}"
ASSET_NAME="loopforge-workflow.tar.gz"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

PAYLOAD_DIR="$TMP_DIR/loopforge-workflow"
mkdir -p "$PAYLOAD_DIR" "$OUTPUT_DIR"

for path in src skills .codebuddy .codex .cursor .claude; do
  cp -R "$ROOT_DIR/$path" "$PAYLOAD_DIR/$path"
done
python3 "$ROOT_DIR/scripts/prepare-notices.py"
cp "$ROOT_DIR/package.json" "$ROOT_DIR/LICENSE" \
  "$ROOT_DIR/THIRD_PARTY_NOTICES.md" "$PAYLOAD_DIR/"
find "$PAYLOAD_DIR/skills" -type d -name tests -prune -exec rm -rf {} +
find "$PAYLOAD_DIR" -type d -name __pycache__ -prune -exec rm -rf {} +
find "$PAYLOAD_DIR" -type f \( -name '*.pyc' -o -name '*.pyo' \) -delete
# agent-observability run output (metrics, state, debug logs) is local-only.
find "$PAYLOAD_DIR" -path '*/agent-observability/logs/*' -type f ! -name .gitkeep -delete

tar -czf "$OUTPUT_DIR/$ASSET_NAME" -C "$TMP_DIR" loopforge-workflow
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$OUTPUT_DIR" && sha256sum "$ASSET_NAME" > "$ASSET_NAME.sha256")
else
  (cd "$OUTPUT_DIR" && shasum -a 256 "$ASSET_NAME" > "$ASSET_NAME.sha256")
fi

printf 'Created %s\n' "$OUTPUT_DIR/$ASSET_NAME"
printf 'Created %s\n' "$OUTPUT_DIR/$ASSET_NAME.sha256"
