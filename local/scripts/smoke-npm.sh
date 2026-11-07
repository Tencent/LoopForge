#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

PACK_JSON="$(npm pack "$ROOT_DIR" --pack-destination "$TMP_DIR" --json)"
export PACK_JSON
PACKAGE_FILE="$(node -e 'process.stdout.write(JSON.parse(process.env.PACK_JSON)[0].filename)')"
PACKAGE_PATH="$TMP_DIR/$PACKAGE_FILE"

node <<'NODE'
const result = JSON.parse(process.env.PACK_JSON)[0]
if (result.name !== "loopforge-cli") {
  throw new Error(`unexpected npm package name ${result.name}`)
}
const files = new Set(result.files.map((item) => item.path))
const required = [
  "bin/loopforge.mjs",
  "src/devflow_cli/cli.py",
  "skills/devflow/SKILL.md",
  ".codebuddy/settings.json",
  ".codex/README.md",
  ".cursor/README.md",
  ".claude/README.md",
  "THIRD_PARTY_NOTICES.md",
  "README.md",
  "scripts/prepare-notices.py",
]
for (const path of required) {
  if (!files.has(path)) throw new Error(`npm package is missing ${path}`)
}
for (const item of files) {
  if (
    item.includes("__pycache__") ||
    item.endsWith(".pyc") ||
    item.endsWith(".pyo") ||
    item.startsWith("build/") ||
    /(^|\/)tests\//.test(item) ||
    /(^|\/)(upstream(?:-overrides)?\.json|\.env(?:\..*)?|install-state\.json)$/.test(item) ||
    (/\/agent-observability\/logs\//.test(item) && !item.endsWith("/.gitkeep"))
  ) {
    throw new Error(`npm package contains generated file ${item}`)
  }
}
NODE

npx --yes --package "$PACKAGE_PATH" loopforge --version

NPX_PROJECT="$TMP_DIR/npx project"
mkdir -p "$NPX_PROJECT"
npx --yes --package "$PACKAGE_PATH" loopforge install codex --project-root "$NPX_PROJECT"
npx --yes --package "$PACKAGE_PATH" loopforge status codex --project-root "$NPX_PROJECT"
npx --yes --package "$PACKAGE_PATH" loopforge uninstall codex --project-root "$NPX_PROJECT"
test ! -e "$NPX_PROJECT/.devflow/install-state.json"

NPM_PREFIX="$TMP_DIR/npm-prefix"
npm install --global --prefix "$NPM_PREFIX" "$PACKAGE_PATH" >/dev/null
NPM_BIN="$NPM_PREFIX/bin/loopforge"
test -x "$NPM_BIN"
"$NPM_BIN" --version

NPM_PROJECT="$TMP_DIR/npm-project"
mkdir -p "$NPM_PROJECT"
"$NPM_BIN" skills install claude --project-root "$NPM_PROJECT"
"$NPM_BIN" skills status claude --project-root "$NPM_PROJECT"
"$NPM_BIN" skills uninstall claude --project-root "$NPM_PROJECT"
test ! -e "$NPM_PROJECT/.devflow/install-state.json"

bash "$ROOT_DIR/scripts/build-release-archive.sh" "$TMP_DIR/release"
tar -xzf "$TMP_DIR/release/loopforge-workflow.tar.gz" -C "$TMP_DIR"
ARCHIVE_ROOT="$TMP_DIR/loopforge-workflow"
test -x "$ARCHIVE_ROOT/bin/loopforge.mjs"
test -f "$ARCHIVE_ROOT/README.md"
test -f "$ARCHIVE_ROOT/scripts/prepare-notices.py"
node "$ARCHIVE_ROOT/bin/loopforge.mjs" --version
(cd "$ARCHIVE_ROOT" && npm pack --dry-run --json >/dev/null)

echo "npm/npx and release archive smoke tests passed"
