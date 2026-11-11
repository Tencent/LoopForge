#!/usr/bin/env bash
# Install Chromium for Playwright in CI. Usage:
#   bash scripts/install-playwright-deps.sh npx playwright
#   bash scripts/install-playwright-deps.sh python3 -m playwright
#
# GitHub's Ubuntu runners resolve packages through an Azure apt mirror list.
# When that mirror stops resolving, every browser job fails on a few font
# packages, so retry the system dependencies against archive.ubuntu.com.
set -euo pipefail
(($#)) || { echo "usage: $0 <playwright command...>" >&2; exit 2; }

"$@" install chromium
"$@" install-deps chromium && exit 0

echo "Playwright system dependencies failed; retrying with archive.ubuntu.com" >&2
mapfile -t sources < <(sudo grep -rlE 'mirror\+file:|azure\.archive\.ubuntu\.com' /etc/apt/sources.list /etc/apt/sources.list.d 2>/dev/null || true)
((${#sources[@]})) || { echo "no Azure apt mirror configured; nothing to fall back from" >&2; exit 1; }
sudo sed -i -E \
  -e 's#mirror\+file:/etc/apt/apt-mirrors\.txt#http://archive.ubuntu.com/ubuntu/#g' \
  -e 's#https?://azure\.archive\.ubuntu\.com/ubuntu/?#http://archive.ubuntu.com/ubuntu/#g' \
  "${sources[@]}"
"$@" install-deps chromium
