#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
version=8.30.1
# Official GitHub release asset for gitleaks_8.30.1_linux_x64.tar.gz.
asset_id=378332058
archive_sha256=551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb
scan_dir="$(mktemp -d)"
trap 'rm -rf "$scan_dir"' EXIT
# GitHub release downloads can return transient 5xx responses.
if ! curl -fsSL --retry 4 --retry-delay 2 --retry-max-time 120 \
  --connect-timeout 15 --max-time 60 \
  "https://github.com/gitleaks/gitleaks/releases/download/v${version}/gitleaks_${version}_linux_x64.tar.gz" -o "$scan_dir/gitleaks.tar.gz"; then
  # Authenticate the API request when available to avoid the anonymous rate limit.
  {
    printf '%s\n' 'Accept: application/octet-stream'
    if [[ -n "${LOOPFORGE_GITLEAKS_TOKEN:-}" ]]; then
      printf 'Authorization: Bearer %s\n' "$LOOPFORGE_GITLEAKS_TOKEN"
    fi
  } | curl -fsSL --retry 4 --retry-delay 2 --retry-max-time 120 \
    --connect-timeout 15 --max-time 60 --header @- \
    "https://api.github.com/repos/gitleaks/gitleaks/releases/assets/${asset_id}" -o "$scan_dir/gitleaks.tar.gz"
fi
printf '%s  %s\n' "$archive_sha256" "$scan_dir/gitleaks.tar.gz" | sha256sum -c -
tar -xzf "$scan_dir/gitleaks.tar.gz" -C "$scan_dir" gitleaks
# Build an export of the candidate tracked/untracked files, respecting ignores.
# Scanning the working directory directly would also read personal .env/state.
python3 - "$scan_dir/source" <<'PY'
import pathlib,subprocess,shutil,sys
dest=pathlib.Path(sys.argv[1]);dest.mkdir()
paths=subprocess.check_output(['git','ls-files','--cached','--others','--exclude-standard','-z']).decode().split('\0')
for name in set(paths):
 p=pathlib.Path(name)
 if not name or not p.is_file() or p.is_symlink():continue
 target=dest/name;target.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(p,target)
PY
# Gitleaks replaces its report path. Use a disposable regular file, never a
# device such as /dev/null (running as root could replace that device).
"$scan_dir/gitleaks" dir "$scan_dir/source" --config "$PWD/.gitleaks.toml" --no-banner --redact --report-format json --report-path "${LOOPFORGE_SCAN_REPORT:-$scan_dir/report.json}" --exit-code 1
