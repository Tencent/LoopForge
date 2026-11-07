#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
if [[ ! -d .venv ]]; then
  python3 -m venv .venv
  .venv/bin/pip install -e '.[dev]'
fi
if [[ ! -d web/dist ]]; then
  (cd web && npm install && npm run build)
fi
exec .venv/bin/python -m app.main
