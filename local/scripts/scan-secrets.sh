#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="8.30.1"
# Official GitHub release asset for gitleaks_8.30.1_linux_x64.tar.gz.
ASSET_ID="378332058"
ARCHIVE_SHA256="551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb"

if [[ "$(uname -s)" != "Linux" || "$(uname -m)" != "x86_64" ]]; then
  echo "This helper currently supports Linux x86_64 only." >&2
  exit 2
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

ARCHIVE="$TMP_DIR/gitleaks.tar.gz"
# GitHub release downloads can return transient 5xx responses.
if ! curl -fsSL --retry 4 --retry-delay 2 --retry-max-time 120 \
  --connect-timeout 15 --max-time 60 \
  "https://github.com/gitleaks/gitleaks/releases/download/v${VERSION}/gitleaks_${VERSION}_linux_x64.tar.gz" \
  -o "$ARCHIVE"; then
  # The API redirects to the same pinned asset when the release edge is down.
  curl -fsSL --retry 4 --retry-delay 2 --retry-max-time 120 \
    --connect-timeout 15 --max-time 60 -H 'Accept: application/octet-stream' \
    "https://api.github.com/repos/gitleaks/gitleaks/releases/assets/${ASSET_ID}" -o "$ARCHIVE"
fi
printf '%s  %s\n' "$ARCHIVE_SHA256" "$ARCHIVE" | sha256sum -c -
tar -xzf "$ARCHIVE" -C "$TMP_DIR" gitleaks

"$TMP_DIR/gitleaks" dir "$ROOT_DIR" --no-banner --redact --exit-code 1

if [[ "$(git -C "$ROOT_DIR" rev-parse --show-toplevel 2>/dev/null || true)" == "$ROOT_DIR" ]]; then
  "$TMP_DIR/gitleaks" git "$ROOT_DIR" --no-banner --redact --exit-code 1
fi
