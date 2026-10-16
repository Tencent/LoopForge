#!/usr/bin/env bash
# Build helpers with the same toolchain as the server. Installed agents and
# execution machines must not depend on a different Go found on service PATH.
set -euo pipefail
cd "$(dirname "$0")/.."
output="${1:-$PWD/.local/bin}"
mkdir -p "$output"
output="$(cd "$output" && pwd)"
export CGO_ENABLED=0
export GOMAXPROCS="${GOMAXPROCS:-2}"
for command in agentflow agentflow-mcp loopforge-account; do
  go build -p=2 -o "$output/$command.next" "./cmd/$command"
  mv "$output/$command.next" "$output/$command"
done
for target_os in linux darwin; do
  for target_arch in amd64 arm64; do
    for command in agentflow-runner agentflow-gate-mcp; do
      target="$output/$command-$target_os-$target_arch"
      GOOS="$target_os" GOARCH="$target_arch" go build -p=2 -o "$target.next" "./cmd/$command"
      mv "$target.next" "$target"
    done
  done
done
