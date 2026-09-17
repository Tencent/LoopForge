#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "[1/6] Checking forbidden public content"
if rg --hidden -n \
  -e 'Tencent/multi-agents-dev-workflow-open-source' \
  -g '!.git/**' \
  -g '!scripts/validate.sh' \
  "$ROOT_DIR"; then
  echo "Legacy public repository name found; use Tencent/LoopForge." >&2
  exit 1
fi
if rg --hidden -n -i \
  -e '-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----' \
  -e '(ghp_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|xox[baprs]-[A-Za-z0-9-]{20,})' \
  -g '!.git/**' \
  -g '!scripts/validate.sh' \
  "$ROOT_DIR"; then
  echo "Credential-like content found." >&2
  exit 1
fi
if rg --hidden -n -i \
  -e 'woa\.com' \
  -e '(^|[^[:alnum:]])oa\.com' \
  -e 'git\.woa' \
  -e 'iwiki' \
  -e 'tapd' \
  -e 'internal\.tencentcloudapi' \
  -e 'cls\.internal\.tencentcloudapi\.com' \
  -e 'AKID[A-Za-z0-9]{16,}' \
  -g '!.git/**' \
  -g '!.venv/**' \
  -g '!node_modules/**' \
  "$ROOT_DIR/tools/tcmcp"; then
  echo "Internal or credential-like content found under tools/tcmcp." >&2
  exit 1
fi

FIND_PRUNE=(
  -not -path '*/.git/*'
  -not -path '*/node_modules/*'
  -not -path '*/.venv/*'
  -not -path '*/web/dist/*'
)

echo "[2/6] Checking shell syntax"
while IFS= read -r -d '' file; do bash -n "$file"; done \
  < <(find "$ROOT_DIR" -type f -name '*.sh' "${FIND_PRUNE[@]}" -print0)

echo "[3/6] Checking JavaScript and Python syntax"
while IFS= read -r -d '' file; do node --check "$file" >/dev/null; done \
  < <(find "$ROOT_DIR" -type f \( -name '*.js' -o -name '*.cjs' -o -name '*.mjs' \) "${FIND_PRUNE[@]}" -print0)

PY_FIND=( "${FIND_PRUNE[@]}" )
python_minor="$(python3 -c 'import sys; print(sys.version_info.minor)')"
if (( python_minor < 10 )); then
  PY_FIND+=( -not -path '*/tools/tcmcp/*' )
fi
while IFS= read -r -d '' file; do python3 -m py_compile "$file"; done \
  < <(find "$ROOT_DIR" -type f -name '*.py' "${PY_FIND[@]}" -print0)

echo "[4/6] Checking JSON, YAML, Markdown links, and Skill metadata"
python3 - "$ROOT_DIR" <<'PY'
import json
import pathlib
import re
import sys

import yaml

root = pathlib.Path(sys.argv[1])
sys.path.insert(0, str(root / "src"))
from devflow_cli import __version__
from devflow_cli.editions import DEFAULT_EDITION, EDITION_SPECS, HOSTS, HOST_PRIORITY, source_for

SKIP_PARTS = {".git", "node_modules", ".venv", "dist"}

def skip(path: pathlib.Path) -> bool:
    return bool(SKIP_PARTS.intersection(path.parts))

for path in root.rglob("*.json"):
    if skip(path):
        continue
    json.loads(path.read_text(encoding="utf-8"))
for pattern in ("*.yaml", "*.yml"):
    for path in root.rglob(pattern):
        if skip(path):
            continue
        yaml.safe_load(path.read_text(encoding="utf-8"))

missing = []
for path in root.rglob("*.md"):
    if skip(path):
        continue
    for target in re.findall(r"\[[^]]*\]\(([^)]+)\)", path.read_text(encoding="utf-8")):
        target = target.split("#", 1)[0]
        if not target or "://" in target or target.startswith(("mailto:", "#")):
            continue
        if not (path.parent / target).exists():
            missing.append(f"{path.relative_to(root)} -> {target}")
if missing:
    raise SystemExit("Broken Markdown links:\n" + "\n".join(missing))

skill_paths = sorted((root / "skills").glob("*/SKILL.md"))
for path in skill_paths:
    text = path.read_text(encoding="utf-8")
    match = re.match(r"\A---\s*\n(.*?)\n---\s*\n", text, re.DOTALL)
    if not match:
        raise SystemExit(f"Invalid Skill frontmatter: {path.relative_to(root)}")
    metadata = yaml.safe_load(match.group(1))
    if not isinstance(metadata, dict) or set(metadata) != {"name", "description"}:
        raise SystemExit(f"Skill frontmatter must contain only name/description: {path.relative_to(root)}")
    if metadata["name"] != path.parent.name or not str(metadata["description"]).strip():
        raise SystemExit(f"Skill name/description invalid: {path.relative_to(root)}")

skills_manifest = json.loads((root / "skills/manifest.json").read_text(encoding="utf-8"))
portable = skills_manifest.get("skill_sets", {}).get("portable", [])
discovered = [path.parent.name for path in skill_paths]
if skills_manifest.get("version") != "1.0" or len(portable) != len(set(portable)):
    raise SystemExit("Invalid skills/manifest.json version or duplicate Skill")
if set(portable) != set(discovered):
    raise SystemExit(
        "skills/manifest.json drift: portable=" + repr(sorted(portable))
        + " discovered=" + repr(sorted(discovered))
    )

if DEFAULT_EDITION != "classic" or tuple(EDITION_SPECS) != ("portable", "classic"):
    raise SystemExit("Edition registry must keep Classic as the explicit default")
if set(HOST_PRIORITY) != set(HOSTS) or len(HOST_PRIORITY) != len(HOSTS):
    raise SystemExit("Host priority drift: HOST_PRIORITY must list every host exactly once")
for edition, metadata in EDITION_SPECS.items():
    if set(metadata.get("entrypoints", {})) != set(HOSTS):
        raise SystemExit(f"Edition entrypoint drift: {edition}")
    for source in metadata.get("source_roots", ()):
        if not (root / source).is_dir():
            raise SystemExit(f"Edition source does not exist: {edition}: {source}")
    for host in HOSTS:
        source = source_for(edition, host)
        if not (root / source).is_dir():
            raise SystemExit(f"Edition host source does not exist: {edition}/{host}: {source}")

npm_version = json.loads((root / "package.json").read_text(encoding="utf-8"))["version"]
if npm_version != __version__:
    raise SystemExit(
        f"Package version drift: npm={npm_version} python={__version__}"
    )

npm_bin = json.loads((root / "package.json").read_text(encoding="utf-8"))["bin"]
if npm_bin.get("lf") != npm_bin.get("loopforge"):
    raise SystemExit("Short alias drift: package.json bin.lf must point at bin.loopforge")

codex_readme = (root / ".codex/README.md").read_text(encoding="utf-8")
if "SOLO -> leader" in codex_readme:
    raise SystemExit("Codex README drift: small workflow must not route through leader")

codebuddy_state = json.loads(
    (root / ".codebuddy/assets/workflow-state-template.json").read_text(encoding="utf-8")
)
test_description = codebuddy_state["stages"]["TASK-04"]["description"]
if "E2E" in test_description:
    raise SystemExit("Classic state drift: TASK-04 must describe project-appropriate testing")
PY

echo "[5/6] Checking generated Classic host bundles and portable contracts"
python3 "$ROOT_DIR/scripts/build-classic-hosts.py" --check
python3 "$ROOT_DIR/skills/devflow/scripts/validate_config.py"

echo "[6/6] Checking public structure"
for path in README.md LICENSE SECURITY.md CONTRIBUTING.md THIRD_PARTY_NOTICES.md \
  AGENTS.md CLAUDE.md EDITIONS.md pyproject.toml package.json bin/loopforge.mjs install.sh \
  .github/workflows/validate.yml .github/workflows/release.yml \
  .codebuddy/README.md .codebuddy/settings.json .codebuddy/settings.local.json \
  .codex/README.md .cursor/README.md .claude/README.md \
  .codex/skills/devflow-codex/SKILL.md skills/README.md skills/devflow/SKILL.md \
  skills/devflow-clarify-requirements/SKILL.md \
  scripts/scan-secrets.sh scripts/smoke-install.sh scripts/smoke-npm.sh \
  scripts/smoke-curl-install.sh scripts/build-release-archive.sh \
  scripts/build-classic-hosts.py \
  src/devflow_cli/cli.py src/devflow_cli/core.py \
  tools/tcmcp/README.md tools/tcmcp/README.zh-CN.md \
  tools/tcmcp/pyproject.toml tools/tcmcp/app/main.py tools/tcmcp/Dockerfile; do
  test -f "$ROOT_DIR/$path"
done

for path in .agents/skills/devflow-codex .agents/skills/knowledge-distillation \
  .agents/skills/superpowers-brainstorming; do
  test -L "$ROOT_DIR/$path"
  test -e "$ROOT_DIR/$path/SKILL.md"
done

echo "Validation passed."
