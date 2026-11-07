#!/usr/bin/env bash
# Reproducible frontend build; must finish before any Go embedding/build starts.
set -euo pipefail
chat_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$chat_root"
node -e 'if (+process.versions.node.split(".")[0] < 22) { console.error("Chatbot build requires Node >= 22"); process.exit(1) }'
npm --prefix chatbot ci --include=dev --no-audit --no-fund
# Tests import src; Vite only writes web/native-chat. Run them together.
status=0
npm --prefix chatbot test &
test_pid=$!
npm --prefix chatbot run build &
build_pid=$!
wait "$test_pid" || status=$?
wait "$build_pid" || status=$?
exit "$status"
