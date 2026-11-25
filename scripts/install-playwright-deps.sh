#!/usr/bin/env bash
# Install Chromium for Playwright in CI. Usage:
#   bash scripts/install-playwright-deps.sh npx playwright
#   bash scripts/install-playwright-deps.sh python3 -m playwright
#
# GitHub's Ubuntu runners already ship Chromium's shared libraries; apt only
# adds fonts. When the runners cannot resolve the Ubuntu archives, continue
# without those fonts as long as Chromium still has every library it links.
set -euo pipefail
(($#)) || { echo "usage: $0 <playwright command...>" >&2; exit 2; }

"$@" install chromium

if getent hosts archive.ubuntu.com >/dev/null || getent hosts azure.archive.ubuntu.com >/dev/null; then
  "$@" install-deps chromium && exit 0
  echo "::warning::Playwright system dependencies failed to install" >&2
else
  echo "::warning::Ubuntu archives do not resolve; skipping Playwright system dependencies" >&2
fi

shopt -s nullglob
browsers=("${PLAYWRIGHT_BROWSERS_PATH:-$HOME/.cache/ms-playwright}"/chromium*/chrome-*/{chrome,chrome-headless-shell,headless_shell})
((${#browsers[@]})) || { echo "Chromium was not installed" >&2; exit 1; }
missing="$(ldd "${browsers[@]}" | grep 'not found' | sort -u || true)"
if [[ -n "$missing" ]]; then
  echo "Chromium is missing shared libraries:" >&2
  echo "$missing" >&2
  exit 1
fi
echo "Chromium links all of its libraries; continuing without the optional apt fonts"
