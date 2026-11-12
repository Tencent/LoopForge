#!/usr/bin/env bash
# Optional pull path for a slow ghcr.io link. Sourced by start.sh and install.sh.
# MIRROR is unset: callers keep their existing pull. MIRROR=NJU: pull the same
# ghcr.io name:tag from ghcr.nju.edu.cn and tag it back, so compose.yaml and
# the default pull policy stay unchanged.

mirror_published_ghcr() {
  local policy="${1:-missing}"
  [[ -n "${MIRROR:-}" ]] || return 1
  local mirror_name
  mirror_name=$(printf '%s' "$MIRROR" | tr '[:upper:]' '[:lower:]')
  if [[ "$mirror_name" != nju ]]; then
    printf '\n✗ Unknown MIRROR=%s. Use MIRROR=NJU to pull ghcr.io images from ghcr.nju.edu.cn and tag them back.\n' "$MIRROR" >&2
    exit 1
  fi

  local decision
  if ! decision=$(python3 - <<'PY'
import os, re, pathlib, sys

def quoted(value):
    value = value.strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "'\"":
        return value[1:-1]
    return value

def stored(name):
    current = os.environ.get(name)
    if current is not None and current.strip():
        return quoted(current)
    path = pathlib.Path('.env')
    if path.is_file():
        match = re.search(r'(?m)^%s=(.*)$' % re.escape(name), path.read_text())
        if match:
            return quoted(match.group(1))
    return ''

if stored('COMPOSE_FILE') == 'compose.yaml:compose.dev.yaml':
    print('skip')
    raise SystemExit(0)

image = stored('LOOPFORGE_IMAGE')
if not image:
    compose = pathlib.Path('compose.yaml')
    if not compose.is_file():
        sys.stderr.write('MIRROR=NJU needs compose.yaml to find the pinned image.\n')
        raise SystemExit(1)
    match = re.search(r'(?m)^[ \t]*image:[ \t]*\$\{LOOPFORGE_IMAGE:-([^}]+)\}[ \t]*$', compose.read_text())
    if not match:
        sys.stderr.write('MIRROR=NJU could not find the pinned image in compose.yaml.\n')
        raise SystemExit(1)
    image = match.group(1).strip()

if not image.startswith('ghcr.io/') or '@' in image:
    print('skip')
    raise SystemExit(0)
if not re.fullmatch(r'ghcr\.io/[A-Za-z0-9._/-]+:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}', image):
    sys.stderr.write('MIRROR=NJU cannot rewrite this image reference: %s\n' % image)
    raise SystemExit(1)
print('mirror ' + image)
PY
  ); then
    exit 1
  fi
  [[ "$decision" == skip ]] && return 1
  [[ "$decision" == mirror\ * ]] || {
    printf '\n✗ Could not resolve the image for MIRROR=NJU.\n' >&2
    exit 1
  }

  local image="${decision#mirror }"
  local mirror_image="ghcr.nju.edu.cn/${image#ghcr.io/}"
  if [[ "$policy" != always ]] && docker_access image inspect "$image" >/dev/null 2>&1; then
    printf '\n▸ Using local image %s\n' "$image"
    return 0
  fi
  printf '\n▸ Pulling %s and tagging it %s\n' "$mirror_image" "$image"
  docker_access pull "$mirror_image"
  docker_access tag "$mirror_image" "$image"
}
