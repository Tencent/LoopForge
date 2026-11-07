#!/usr/bin/env bash
# Go statement coverage with agentflow/cmd/ and agentflow/deploy/ excluded.
#
#   scripts/coverage-go.sh [--min PCT] [--profile FILE] [--out FILE] [-p N] [--timeout D] [--coverpkg PATTERN]
#
# Without --profile it runs `go test ./... -coverprofile` first, instrumenting
# every internal package in every test binary (-coverpkg), so a statement run by
# any package's tests counts. --coverpkg '' measures each package by its own
# tests only. The per-package table and the total are computed from the merged
# profile; a block covered by any test binary counts as covered once.
set -euo pipefail

min=""
profile=""
out="${COVERAGE_OUT:-coverage/go.out}"
par="${GO_TEST_P:-4}"
timeout="${GO_TEST_TIMEOUT:-4m}"
coverpkg="${GO_COVERPKG-agentflow/internal/...}"
while [ $# -gt 0 ]; do
  case "$1" in
    --min) min="$2"; shift 2 ;;
    --profile) profile="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    -p) par="$2"; shift 2 ;;
    --timeout) timeout="$2"; shift 2 ;;
    --coverpkg) coverpkg="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

if [ -z "$profile" ]; then
  mkdir -p "$(dirname "$out")"
  profile="$out"
  args=(-p="$par" -count=1 -timeout="$timeout" -covermode=set -coverprofile="$profile")
  [ -z "$coverpkg" ] || args+=(-coverpkg="$coverpkg")
  status=0
  go test "${args[@]}" ./... || status=$?
  if [ "$status" -ne 0 ]; then
    echo "go test failed (exit $status)" >&2
    exit "$status"
  fi
fi

test -s "$profile" || { echo "empty coverage profile: $profile" >&2; exit 1; }

filtered="${profile%.out}.filtered.out"
{
  echo "mode: set"
  grep -v '^mode:' "$profile" | grep -Ev '^agentflow/(cmd|deploy)/' || true
} >"$filtered"

awk -v min="$min" '
  NR == 1 { next }
  {
    split($1, loc, ":")
    file = loc[1]
    key = $1
    stmts[key] = $2
    if ($3 > 0) hit[key] = 1
    n = split(file, parts, "/")
    pkg = parts[1]
    for (i = 2; i < n; i++) pkg = pkg "/" parts[i]
    pkgOf[key] = pkg
  }
  END {
    for (k in stmts) {
      p = pkgOf[k]
      total[p] += stmts[k]
      all += stmts[k]
      if (k in hit) { cov[p] += stmts[k]; allCov += stmts[k] }
    }
    m = 0
    for (p in total) names[++m] = p
    for (i = 1; i <= m; i++) for (j = i + 1; j <= m; j++) if (names[j] < names[i]) { t = names[i]; names[i] = names[j]; names[j] = t }
    printf "%-44s %8s %8s %8s %7s\n", "package", "stmts", "covered", "missed", "cover"
    for (i = 1; i <= m; i++) {
      p = names[i]
      printf "%-44s %8d %8d %8d %6.1f%%\n", p, total[p], cov[p], total[p] - cov[p], 100 * cov[p] / total[p]
    }
    pct = all ? 100 * allCov / all : 0
    printf "%-44s %8d %8d %8d %6.1f%%\n", "total", all, allCov, all - allCov, pct
    if (min != "" && pct + 1e-9 < min + 0) {
      printf "FAIL: Go coverage %.1f%% is below the %s%% threshold\n", pct, min > "/dev/stderr"
      exit 1
    }
    if (min != "") printf "OK: Go coverage %.1f%% >= %s%%\n", pct, min
  }
' "$filtered"
