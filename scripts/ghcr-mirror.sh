#!/usr/bin/env bash
# Optional pull path for a slow ghcr.io or Docker Hub link. Sourced by start.sh
# and install.sh. MIRROR is unset: callers keep their existing pull.
# MIRROR=<host> replaces the ghcr.io host, for example MIRROR=ghcr.nju.edu.cn,
# then tags the image back so compose.yaml and the default pull policy stay
# unchanged. A ghcr.<domain> host also pulls MySQL from docker.<domain>;
# DOCKERHUB_MIRROR=<host> names another Docker Hub mirror. A mirror that cannot
# serve an image falls back to the original registry.

registry_host() {
  local host
  host=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')
  host="${host#"${host%%[![:space:]]*}"}"
  host="${host%"${host##*[![:space:]]}"}"
  printf '%s' "${host%/}"
}

valid_registry_host() {
  [[ "$1" =~ ^(localhost|[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+)(:[0-9]{1,5})?$ ]]
}

# pull_mirrored <mirror image> <compose image> <fallback name>
# Returns 1 when the caller should pull the compose image from its own registry.
pull_mirrored() {
  printf '\n▸ Pulling %s and tagging it %s\n' "$1" "$2"
  # Called from `if`, so a failing docker command does not exit on its own.
  if ! docker_access pull "$1"; then
    printf '\n! Could not pull %s. Falling back to %s.\n' "$1" "$3" >&2
    return 1
  fi
  if ! docker_access tag "$1" "$2"; then
    printf '\n✗ Could not tag %s as %s. Stopped without starting.\n' "$1" "$2" >&2
    exit 1
  fi
}

mirror_published_ghcr() {
  local policy="${1:-missing}"
  [[ -n "${MIRROR:-}" ]] || return 1
  local mirror_host
  mirror_host=$(registry_host "$MIRROR")
  # The upstream host is the normal pull. Any other registry host replaces ghcr.io.
  if [[ "$mirror_host" == ghcr.io || "$mirror_host" == ghcr.io:443 ]]; then
    return 1
  fi
  if ! valid_registry_host "$mirror_host"; then
    printf '\n✗ MIRROR must be the registry host that replaces ghcr.io, for example ghcr.nju.edu.cn.\n' >&2
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
        matches = re.findall(r'(?m)^%s=(.*)$' % re.escape(name), path.read_text())
        if matches:
            return quoted(matches[-1])
    return ''

if stored('COMPOSE_FILE') == 'compose.yaml:compose.dev.yaml':
    print('skip')
    raise SystemExit(0)

image = stored('LOOPFORGE_IMAGE')
if not image:
    compose = pathlib.Path('compose.yaml')
    if not compose.is_file():
        sys.stderr.write('MIRROR needs compose.yaml to find the pinned image.\n')
        raise SystemExit(1)
    match = re.search(r'(?m)^[ \t]*image:[ \t]*\$\{LOOPFORGE_IMAGE:-([^}]+)\}[ \t]*$', compose.read_text())
    if not match:
        sys.stderr.write('MIRROR could not find the pinned image in compose.yaml.\n')
        raise SystemExit(1)
    image = match.group(1).strip()

if not image.startswith('ghcr.io/') or '@' in image:
    print('skip')
    raise SystemExit(0)
if not re.fullmatch(r'ghcr\.io/[A-Za-z0-9._/-]+:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}', image):
    sys.stderr.write('MIRROR cannot rewrite this image reference: %s\n' % image)
    raise SystemExit(1)
print('mirror ' + image)
PY
  ); then
    exit 1
  fi
  [[ "$decision" == skip ]] && return 1
  [[ "$decision" == mirror\ * ]] || {
    printf '\n✗ Could not resolve the image for MIRROR=%s.\n' "$mirror_host" >&2
    exit 1
  }

  local image="${decision#mirror }"
  local mirror_image="${mirror_host}/${image#ghcr.io/}"
  if [[ "$policy" != always ]] && docker_access image inspect "$image" >/dev/null 2>&1; then
    printf '\n▸ Using local image %s\n' "$image"
    return 0
  fi
  pull_mirrored "$mirror_image" "$image" ghcr.io
}

mirror_dockerhub_mysql() {
  local policy="${1:-missing}"
  local hub
  if [[ -n "${DOCKERHUB_MIRROR:-}" ]]; then
    hub=$(registry_host "$DOCKERHUB_MIRROR")
  elif [[ -n "${MIRROR:-}" ]]; then
    hub=$(registry_host "$MIRROR")
    [[ "$hub" == ghcr.* && "$hub" != ghcr.io && "$hub" != ghcr.io:* ]] || return 1
    hub="docker.${hub#ghcr.}"
  else
    return 1
  fi
  [[ "$hub" != docker.io && "$hub" != registry-1.docker.io ]] || return 1
  if ! valid_registry_host "$hub"; then
    printf '\n✗ DOCKERHUB_MIRROR must be a Docker Hub mirror host, for example docker.nju.edu.cn.\n' >&2
    exit 1
  fi

  local image
  [[ -f compose.yaml ]] || return 1
  image=$(sed -nE 's/^[[:space:]]+image:[[:space:]]*(mysql:[A-Za-z0-9_][A-Za-z0-9_.-]*)[[:space:]]*$/\1/p' compose.yaml | head -n 1)
  [[ -n "$image" ]] || return 1
  if [[ "$policy" != always ]] && docker_access image inspect "$image" >/dev/null 2>&1; then
    return 0
  fi
  pull_mirrored "${hub}/library/${image}" "$image" 'Docker Hub'
}
