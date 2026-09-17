#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="8.30.1"
ARCHIVE_SHA256="551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb"

if command -v gitleaks >/dev/null 2>&1; then
  gitleaks dir "$ROOT_DIR" --no-banner --redact --exit-code 1
  if [[ "$(git -C "$ROOT_DIR" rev-parse --show-toplevel 2>/dev/null || true)" == "$ROOT_DIR" ]]; then
    gitleaks git "$ROOT_DIR" --no-banner --redact --exit-code 1
  fi
  exit 0
fi

if [[ "$(uname -s)" != "Linux" || "$(uname -m)" != "x86_64" ]]; then
  echo "This helper currently supports Linux x86_64 only." >&2
  exit 2
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

ARCHIVE="$TMP_DIR/gitleaks.tar.gz"
curl -fsSL \
  "https://github.com/gitleaks/gitleaks/releases/download/v${VERSION}/gitleaks_${VERSION}_linux_x64.tar.gz" \
  -o "$ARCHIVE"
printf '%s  %s\n' "$ARCHIVE_SHA256" "$ARCHIVE" | sha256sum -c -
tar -xzf "$ARCHIVE" -C "$TMP_DIR" gitleaks

"$TMP_DIR/gitleaks" dir "$ROOT_DIR" --no-banner --redact --exit-code 1

if [[ "$(git -C "$ROOT_DIR" rev-parse --show-toplevel 2>/dev/null || true)" == "$ROOT_DIR" ]]; then
  "$TMP_DIR/gitleaks" git "$ROOT_DIR" --no-banner --redact --exit-code 1
fi
