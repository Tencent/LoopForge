#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

PROJECT="$TMP_DIR/example-project"
mkdir -p "$PROJECT"
cp -R "$ROOT_DIR/.codebuddy" "$PROJECT/.codebuddy"

test -f "$PROJECT/.codebuddy/README.md"
test -f "$PROJECT/.codebuddy/settings.json"
test -f "$PROJECT/.codebuddy/settings.local.json"
test -d "$PROJECT/.codebuddy/agents"
test -d "$PROJECT/.codebuddy/commands"
test -d "$PROJECT/.codebuddy/rules"
test -f "$PROJECT/.codebuddy/runtime/workflow-state-spec.md"
test -f "$ROOT_DIR/.codex/skills/devflow-codex/SKILL.md"
test -f "$ROOT_DIR/skills/devflow/SKILL.md"
test -f "$ROOT_DIR/skills/devflow-clarify-requirements/SKILL.md"

for adapter in cursor claude opencode; do
  ADAPTER_PROJECT="$TMP_DIR/${adapter}-project"
  mkdir -p "$ADAPTER_PROJECT"
  python3 "$ROOT_DIR/skills/devflow/scripts/install_adapter.py" \
    --adapter "$adapter" --project-root "$ADAPTER_PROJECT" --refresh-managed
  test -f "$ADAPTER_PROJECT/.$adapter/agents/devflow-stage-executor.md"
  test -f "$ADAPTER_PROJECT/.$adapter/agents/devflow-research-helper.md"
  test -L "$ADAPTER_PROJECT/.$adapter/skills/devflow"
  test -L "$ADAPTER_PROJECT/.$adapter/skills/devflow-clarify-requirements"
  test -f "$ADAPTER_PROJECT/.$adapter/.devflow-managed-skills.json"
done

COPY_PROJECT="$TMP_DIR/claude-copy-project"
mkdir -p "$COPY_PROJECT"
python3 "$ROOT_DIR/skills/devflow/scripts/install_adapter.py" \
  --adapter claude --project-root "$COPY_PROJECT" --copy-skills
printf '\nlocal drift\n' >> "$COPY_PROJECT/.claude/skills/devflow/SKILL.md"
python3 "$ROOT_DIR/skills/devflow/scripts/install_adapter.py" \
  --adapter claude --project-root "$COPY_PROJECT" --copy-skills --refresh-managed
cmp "$ROOT_DIR/skills/devflow/SKILL.md" "$COPY_PROJECT/.claude/skills/devflow/SKILL.md"

python3 "$ROOT_DIR/skills/devflow/scripts/validate_config.py"
python3 "$ROOT_DIR/scripts/build-classic-hosts.py" --check
python3 -m unittest discover -s "$ROOT_DIR/skills/devflow/tests" -v
PYTHONPATH="$ROOT_DIR/src" python3 -m unittest discover -s "$ROOT_DIR/tests" -v

WHEEL_DIR="$TMP_DIR/wheel"
VENV_DIR="$TMP_DIR/venv"
mkdir -p "$WHEEL_DIR"
python3 -m pip wheel "$ROOT_DIR" --no-deps -w "$WHEEL_DIR" >/dev/null
python3 -m venv "$VENV_DIR"
"$VENV_DIR/bin/python" -m pip install "$WHEEL_DIR"/*.whl >/dev/null
test -x "$VENV_DIR/bin/loopforge"
test ! -e "$VENV_DIR/bin/devflow"
test -f "$VENV_DIR/share/devflow/THIRD_PARTY_NOTICES.md"
test ! -e "$VENV_DIR/share/devflow/skills/devflow/tests"
CLI_PROJECT="$TMP_DIR/cli-project"
mkdir -p "$CLI_PROJECT"
"$VENV_DIR/bin/loopforge" doctor --project-root "$CLI_PROJECT"
"$VENV_DIR/bin/loopforge" install claude --project-root "$CLI_PROJECT"
"$VENV_DIR/bin/loopforge" status claude --project-root "$CLI_PROJECT"
"$VENV_DIR/bin/loopforge" uninstall claude --project-root "$CLI_PROJECT"

CODEBUDDY_PROJECT="$TMP_DIR/codebuddy-project"
DOWNSTREAM_PROJECT="$TMP_DIR/downstream-project"
mkdir -p "$CODEBUDDY_PROJECT" "$DOWNSTREAM_PROJECT"
"$VENV_DIR/bin/loopforge" skills install codebuddy --project-root "$CODEBUDDY_PROJECT"
test -f "$CODEBUDDY_PROJECT/.codebuddy/skills/manifest.json"

PI_PROJECT="$TMP_DIR/pi-project"
mkdir -p "$PI_PROJECT"
"$VENV_DIR/bin/loopforge" skills install pi --project-root "$PI_PROJECT"
test -f "$PI_PROJECT/.pi/skills/manifest.json"
test -f "$PI_PROJECT/.pi/skills/devflow/SKILL.md"

OPENCODE_CLI_PROJECT="$TMP_DIR/opencode-cli-project"
mkdir -p "$OPENCODE_CLI_PROJECT"
"$VENV_DIR/bin/loopforge" skills install opencode --project-root "$OPENCODE_CLI_PROJECT"
test -f "$OPENCODE_CLI_PROJECT/.opencode/skills/manifest.json"
test -f "$OPENCODE_CLI_PROJECT/.opencode/skills/devflow/SKILL.md"
test -f "$OPENCODE_CLI_PROJECT/.opencode/agents/devflow-stage-executor.md"
"$VENV_DIR/bin/python" \
  "$CODEBUDDY_PROJECT/.codebuddy/skills/devflow/scripts/install_adapter.py" \
  --adapter claude --project-root "$DOWNSTREAM_PROJECT" --copy-skills --refresh-managed
test -f "$DOWNSTREAM_PROJECT/.claude/skills/manifest.json"
test -f "$DOWNSTREAM_PROJECT/.claude/skills/devflow/SKILL.md"

python3 "$ROOT_DIR/scripts/test-permission-hook.py"

echo "Installation smoke test passed."
