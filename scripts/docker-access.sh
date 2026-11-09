#!/usr/bin/env bash

docker_access() {
  if [[ "${LOOPFORGE_DOCKER_USE_SUDO:-0}" == 1 ]]; then
    require_sudo
    command sudo -n docker "$@"
  else
    command docker "$@"
  fi
}

docker_access_fail() {
  printf '\n✗ %s\n' "$1" >&2
  exit 1
}

require_sudo() {
  command -v sudo >/dev/null || docker_access_fail 'Administrator access is required for Docker. Install sudo or ask an administrator to grant Docker access.'
  if ! sudo -n true 2>/dev/null; then
    [[ -t 0 ]] || docker_access_fail 'Docker requires sudo. Run sudo -v in a terminal first, then rerun the installer; or configure rootless Docker.'
    sudo -v || docker_access_fail 'Could not obtain administrator access for Docker.'
  fi
}

as_root() {
  if [[ $(id -u) -eq 0 ]]; then
    "$@"
  else
    require_sudo
    sudo -n "$@"
  fi
}

select_docker_access() {
  docker_access info >/dev/null 2>&1 && return 0
  if [[ $(uname -s) == Linux && $(id -u) -ne 0 ]]; then
    require_sudo
    if sudo -n docker info >/dev/null 2>&1; then
      export LOOPFORGE_DOCKER_USE_SUDO=1
      return 0
    fi
  fi
  return 1
}
