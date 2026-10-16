#!/bin/sh
set -eu

PROGRAM="loopforge installer"
DEFAULT_REPOSITORY="Tencent/LoopForge"
ASSET_NAME="loopforge-workflow.tar.gz"

fail() {
  printf '%s: %s\n' "$PROGRAM" "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Install a LoopForge workflow directly into a project (no global CLI install).

Usage:
  curl -fsSL <install.sh URL> | sh -s -- <host> [options]

Hosts:
  codebuddy | codex | cursor | claude

Options:
  --edition <classic|portable>  Workflow edition (default: classic)
  --project-root <path>         Target project (default: current directory)
  --version <version>           Install a specific GitHub Release tag
  --repository <owner/repo>     GitHub repository containing release assets
  --release-base-url <url>      Override the release asset base URL
  --force                       Replace conflicting managed files
  -h, --help                    Show this help

Environment equivalents:
  LOOPFORGE_REPOSITORY, LOOPFORGE_VERSION, LOOPFORGE_RELEASE_BASE_URL
EOF
}

HOST=""
EDITION="classic"
PROJECT_ROOT="$(pwd)"
VERSION="${LOOPFORGE_VERSION:-}"
REPOSITORY="${LOOPFORGE_REPOSITORY:-$DEFAULT_REPOSITORY}"
RELEASE_BASE_URL="${LOOPFORGE_RELEASE_BASE_URL:-}"
GITHUB_URL="${LOOPFORGE_GITHUB_URL:-https://github.com}"
GITHUB_API_URL="${LOOPFORGE_GITHUB_API_URL:-https://api.github.com}"
FORCE=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    codebuddy|codex|cursor|claude)
      [ -z "$HOST" ] || fail "host was provided more than once"
      HOST="$1"
      shift
      ;;
    --edition)
      [ "$#" -ge 2 ] || fail "--edition requires a value"
      EDITION="$2"
      shift 2
      ;;
    --project-root)
      [ "$#" -ge 2 ] || fail "--project-root requires a value"
      PROJECT_ROOT="$2"
      shift 2
      ;;
    --version)
      [ "$#" -ge 2 ] || fail "--version requires a value"
      VERSION="$2"
      shift 2
      ;;
    --repository)
      [ "$#" -ge 2 ] || fail "--repository requires a value"
      REPOSITORY="$2"
      shift 2
      ;;
    --release-base-url)
      [ "$#" -ge 2 ] || fail "--release-base-url requires a value"
      RELEASE_BASE_URL="$2"
      shift 2
      ;;
    --force)
      FORCE=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

[ -n "$HOST" ] || {
  usage >&2
  fail "a host is required"
}
case "$EDITION" in
  classic|portable) ;;
  *) fail "unsupported edition: $EDITION" ;;
esac
[ -d "$PROJECT_ROOT" ] || fail "project directory does not exist: $PROJECT_ROOT"

command -v python3 >/dev/null 2>&1 || fail "Python 3.8 or newer is required"
python3 -c 'import sys; raise SystemExit(0 if sys.version_info >= (3, 8) else 1)' \
  || fail "Python 3.8 or newer is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"

download() {
  source_url="$1"
  destination="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fL --retry 3 --connect-timeout 15 --output "$destination" "$source_url"
  elif command -v wget >/dev/null 2>&1; then
    wget -q --tries=3 --timeout=15 --output-document="$destination" "$source_url"
  else
    fail "curl or wget is required"
  fi
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    fail "sha256sum or shasum is required"
  fi
}

TMP_DIR=$(mktemp -d 2>/dev/null || mktemp -d -t loopforge)
trap 'rm -rf "$TMP_DIR"' EXIT HUP INT TERM

if [ -z "$RELEASE_BASE_URL" ]; then
  printf '%s' "$REPOSITORY" | grep -Eq '^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$' \
    || fail "invalid GitHub repository: $REPOSITORY"
  if [ -n "$VERSION" ]; then
    VERSION=${VERSION#v}
    printf '%s' "$VERSION" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9._-]*$' \
      || fail "invalid version: $VERSION"
    RELEASE_BASE_URL="${GITHUB_URL%/}/$REPOSITORY/releases/download/v$VERSION"
  else
    RELEASES_JSON="$TMP_DIR/releases.json"
    download "${GITHUB_API_URL%/}/repos/$REPOSITORY/releases?per_page=1" "$RELEASES_JSON"
    RELEASE_TAG=$(python3 - "$RELEASES_JSON" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as file:
    releases = json.load(file)
if not isinstance(releases, list) or not releases:
    raise SystemExit("no published GitHub Release was found")
tag = releases[0].get("tag_name")
if not isinstance(tag, str) or not tag:
    raise SystemExit("latest GitHub Release has no tag")
print(tag)
PY
    ) || fail "could not resolve the latest GitHub Release"
    printf '%s' "$RELEASE_TAG" | grep -Eq '^v?[A-Za-z0-9][A-Za-z0-9._-]*$' \
      || fail "latest GitHub Release has an invalid tag: $RELEASE_TAG"
    RELEASE_BASE_URL="${GITHUB_URL%/}/$REPOSITORY/releases/download/$RELEASE_TAG"
  fi
fi

case "$RELEASE_BASE_URL" in
  https://*) ;;
  *)
    [ "${LOOPFORGE_ALLOW_INSECURE:-0}" = "1" ] \
      || fail "release URL must use HTTPS"
    ;;
esac
RELEASE_BASE_URL=${RELEASE_BASE_URL%/}
ARCHIVE_URL="$RELEASE_BASE_URL/$ASSET_NAME"
CHECKSUM_URL="$ARCHIVE_URL.sha256"

ARCHIVE="$TMP_DIR/$ASSET_NAME"
CHECKSUM="$ARCHIVE.sha256"

printf 'Downloading LoopForge workflow from %s\n' "$ARCHIVE_URL"
download "$ARCHIVE_URL" "$ARCHIVE"
download "$CHECKSUM_URL" "$CHECKSUM"

EXPECTED_SHA256=$(awk 'NR == 1 {print $1}' "$CHECKSUM")
printf '%s\n' "$EXPECTED_SHA256" | grep -Eq '^[A-Fa-f0-9]{64}$' \
  || fail "invalid SHA-256 checksum file"
ACTUAL_SHA256=$(sha256_file "$ARCHIVE")
[ "$ACTUAL_SHA256" = "$EXPECTED_SHA256" ] || fail "SHA-256 checksum mismatch"

tar -tzf "$ARCHIVE" | awk '
  $0 !~ /^loopforge-workflow(\/|$)/ { exit 1 }
  $0 ~ /(^|\/)\.\.($|\/)/ { exit 1 }
' || fail "release archive contains an unsafe path"
tar -xzf "$ARCHIVE" -C "$TMP_DIR"

ASSET_ROOT="$TMP_DIR/loopforge-workflow"
[ -f "$ASSET_ROOT/src/devflow_cli/cli.py" ] || fail "release archive is missing the CLI core"
[ -f "$ASSET_ROOT/skills/devflow/SKILL.md" ] || fail "release archive is missing workflow assets"

if [ -n "${PYTHONPATH:-}" ]; then
  PYTHONPATH="$ASSET_ROOT/src:$PYTHONPATH"
else
  PYTHONPATH="$ASSET_ROOT/src"
fi
export PYTHONPATH
export DEVFLOW_ASSET_ROOT="$ASSET_ROOT"

set -- install "$HOST" --edition "$EDITION" --project-root "$PROJECT_ROOT"
if [ "$FORCE" -eq 1 ]; then
  set -- "$@" --force
fi
python3 -m devflow_cli.cli "$@"
