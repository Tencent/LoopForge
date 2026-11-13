#!/usr/bin/env bash
set -euo pipefail
image="${1:?Usage: bash scripts/test-runtime-image.sh IMAGE}"
docker run --rm -i --network none --read-only --tmpfs /tmp:rw,exec,nosuid,size=128m --entrypoint sh "$image" -eu <<'CHECK'
export HOME=/tmp/home
mkdir -p "$HOME"
cd /tmp
for command in node npm git bash curl python3 rg gcc g++ make pkg-config ssh unzip; do
  command -v "$command" >/dev/null || { printf 'Missing sandbox tool: %s\n' "$command" >&2; exit 1; }
done
printf 'int main(void) { return 0; }\n' > check.c
printf 'int main() { return 0; }\n' > check.cpp
gcc check.c -o check-c
g++ check.cpp -o check-cpp
./check-c
./check-cpp
printf 'check:\n\t./check-c\n\t./check-cpp\n' > Makefile
make check
node -e 'if (2 + 2 !== 4) process.exit(1)'
python3 -c 'assert 2 + 2 == 4'
git --version
ssh -V
unzip -v >/dev/null
codex --version
if command -v claude >/dev/null; then printf 'Claude Code must not be bundled; it installs on first use.\n' >&2; exit 1; fi
codebuddy --version
dsh --version
printf 'Runtime image compatibility checks passed.\n'
CHECK
