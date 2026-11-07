#!/usr/bin/env bash
# Build helpers with the same toolchain as the server. Installed agents and
# execution machines must not depend on a different Go found on service PATH.
#   build-binaries.sh [--helpers|--platform] [output-dir]
# --helpers builds everything that does not embed the web UI, so Docker can
# compile it while the chatbot bundle is still building; --platform builds the
# server, which embeds web/native-chat. Without a flag both are built.
set -euo pipefail
cd "$(dirname "$0")/.."
mode=all
case "${1:-}" in
  --helpers|--platform) mode="${1#--}"; shift ;;
esac
output="${1:-$PWD/.local/bin}"
mkdir -p "$output"
output="$(cd "$output" && pwd)"
if [[ $mode != helpers && ! -f web/native-chat/index.html ]]; then
  npm --prefix chatbot ci --include=dev --no-audit --no-fund
  npm --prefix chatbot run build
fi

# One compile job per CPU, but at most one per GiB of memory so small hosts do
# not run out of memory (a cold build peaks near 1.3 GiB total with 4 jobs).
# LOOPFORGE_BUILD_JOBS overrides the estimate.
build_jobs() {
  local cpus memory_kib limit
  cpus=$(nproc 2>/dev/null || echo 2)
  memory_kib=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null || true)
  limit=$(cat /sys/fs/cgroup/memory.max /sys/fs/cgroup/memory/memory.limit_in_bytes 2>/dev/null | head -1 || true)
  if [[ $limit =~ ^[0-9]+$ ]] && { [[ -z $memory_kib ]] || (( limit / 1024 < memory_kib )); }; then
    memory_kib=$((limit / 1024))
  fi
  local by_memory=$(( ${memory_kib:-2097152} / 1048576 ))
  local jobs=$(( cpus < by_memory ? cpus : by_memory ))
  echo $(( jobs < 1 ? 1 : jobs ))
}
jobs="${LOOPFORGE_BUILD_JOBS:-$(build_jobs)}"
[[ $jobs =~ ^[1-9][0-9]*$ ]] || { echo "LOOPFORGE_BUILD_JOBS must be a positive integer" >&2; exit 1; }
export CGO_ENABLED=0
flags=(-p="$jobs" -trimpath -ldflags=-s\ -w)

# build TARGET_SUFFIX PACKAGE... builds every package in one go invocation so
# shared dependencies compile and the links run in parallel.
build() {
  local suffix="$1"; shift
  local staging
  staging="$(mktemp -d "$output/.build.XXXXXX")"
  go build "${flags[@]}" -o "$staging/" "$@" || { rm -rf "$staging"; return 1; }
  local package
  for package in "$@"; do
    mv "$staging/${package##*/}" "$output/${package##*/}$suffix"
  done
  rmdir "$staging"
}

printf 'Building with %s parallel jobs\n' "$jobs"
if [[ $mode != platform ]]; then
  printf '\nBuilding agentflow-mcp and loopforge-account for the platform…\n'
  build '' ./cmd/agentflow-mcp ./cmd/loopforge-account
  for target_os in linux darwin; do
    for target_arch in amd64 arm64; do
      printf '\nBuilding agentflow-runner and agentflow-gate-mcp (%s/%s)…\n' "$target_os" "$target_arch"
      GOOS="$target_os" GOARCH="$target_arch" build "-$target_os-$target_arch" ./cmd/agentflow-runner ./cmd/agentflow-gate-mcp
    done
  done
fi
if [[ $mode != helpers ]]; then
  printf '\nBuilding agentflow for the platform…\n'
  build '' ./cmd/agentflow
fi
