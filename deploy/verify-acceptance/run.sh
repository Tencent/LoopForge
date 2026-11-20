#!/bin/sh
# Usage: run.sh <spec-dir> <output-json>
# Compiles every *.java under the spec dir with the JUnit API and runs them
# through the console launcher. Exit 0 only when tests ran and none failed.
set -eu
spec="${1:?spec dir}"
report="${2:-/tmp/acceptance.log}"
junit_jar="/opt/junit/junit-platform-console-standalone-1.10.2.jar"
classes="$(mktemp -d)"
trap 'rm -rf "$classes"' EXIT

sources="$(find "$spec" -name '*.java' -type f)"
if [ -z "$sources" ]; then
  echo "acceptance: no Java sources under $spec" >&2
  exit 2
fi
# shellcheck disable=SC2086
javac -encoding UTF-8 -d "$classes" -cp "$junit_jar" $sources
set +e
base_url=""
if [ -n "${LOOPFORGE_SUBJECT_URL:-}" ]; then
  base_url="-Daegis.baseUrl=${LOOPFORGE_SUBJECT_URL}"
fi
java $base_url -jar "$junit_jar" execute \
  --class-path "$classes" \
  --scan-classpath "$classes" \
  --details=summary \
  --disable-banner --disable-ansi-colors > "$report" 2>&1
status=$?
set -e
if [ "$report" != "/dev/stdout" ]; then cat "$report"; fi
if [ "$status" -ne 0 ]; then
  exit "$status"
fi
if grep -qE 'tests found: 0|0 tests found' "$report"; then
  echo "acceptance: no tests ran" >&2
  exit 2
fi
exit 0
