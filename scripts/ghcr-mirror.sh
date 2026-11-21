#!/usr/bin/env bash
# Pull path for the published ghcr.io image. Sourced by start.sh and install.sh.
# MIRROR is unset: time ghcr.io and ghcr.nju.edu.cn, then pull the faster one.
# MIRROR=<host> replaces the ghcr.io host, for example MIRROR=ghcr.nju.edu.cn,
# and tags the image back so compose.yaml stays unchanged. MIRROR=ghcr.io keeps
# the direct pull. The choice is not written to .env.

mirror_published_ghcr() {
  local policy="${1:-missing}"
  local mirror_host=""
  local auto=0

  if [[ -n "${MIRROR:-}" ]]; then
    mirror_host=$(printf '%s' "$MIRROR" | tr '[:upper:]' '[:lower:]')
    mirror_host="${mirror_host#"${mirror_host%%[![:space:]]*}"}"
    mirror_host="${mirror_host%"${mirror_host##*[![:space:]]}"}"
    mirror_host=${mirror_host%/}
    # The upstream host is the normal pull. Any other registry host replaces ghcr.io.
    if [[ "$mirror_host" == ghcr.io || "$mirror_host" == ghcr.io:443 ]]; then
      return 1
    fi
    if [[ ! "$mirror_host" =~ ^(localhost|[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+)(:[0-9]{1,5})?$ ]]; then
      printf '\n✗ MIRROR must be the registry host that replaces ghcr.io, for example ghcr.nju.edu.cn.\n' >&2
      exit 1
    fi
  else
    auto=1
  fi

  local decision
  if ! decision=$(python3 - "$auto" <<'PY'
import os, re, pathlib, sys

auto = sys.argv[1] == '1'

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

def skip():
    print('skip')
    raise SystemExit(0)

if stored('COMPOSE_FILE') == 'compose.yaml:compose.dev.yaml':
    skip()

image = stored('LOOPFORGE_IMAGE')
if not image:
    compose = pathlib.Path('compose.yaml')
    if not compose.is_file():
        if auto:
            skip()
        sys.stderr.write('MIRROR needs compose.yaml to find the pinned image.\n')
        raise SystemExit(1)
    match = re.search(r'(?m)^[ \t]*image:[ \t]*\$\{LOOPFORGE_IMAGE:-([^}]+)\}[ \t]*$', compose.read_text())
    if not match:
        if auto:
            skip()
        sys.stderr.write('MIRROR could not find the pinned image in compose.yaml.\n')
        raise SystemExit(1)
    image = match.group(1).strip()

if not image.startswith('ghcr.io/') or '@' in image:
    skip()
if not re.fullmatch(r'ghcr\.io/[A-Za-z0-9._/-]+:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}', image):
    if auto:
        skip()
    sys.stderr.write('MIRROR cannot rewrite this image reference: %s\n' % image)
    raise SystemExit(1)
print('mirror ' + image)
PY
  ); then
    exit 1
  fi
  [[ "$decision" == skip ]] && return 1
  [[ "$decision" == mirror\ * ]] || {
    if [[ "$auto" == 1 ]]; then
      return 1
    fi
    printf '\n✗ Could not resolve the image for MIRROR=%s.\n' "$mirror_host" >&2
    exit 1
  }

  local image="${decision#mirror }"
  # A re-run that already has the pinned image does not need a speed test.
  # An explicit mirror still reports that it kept the local image.
  if [[ "$policy" != always ]] && docker_access image inspect "$image" >/dev/null 2>&1; then
    if [[ "$auto" == 1 ]]; then
      return 1
    fi
    printf '\n▸ Using local image %s\n' "$image"
    return 0
  fi

  local mirror_digest=""
  if [[ "$auto" == 1 ]]; then
    local probe_line=""
    if ! probe_line=$(python3 - "$image" <<'PY'
import json, re, shutil, subprocess, sys, tempfile, threading, time
from pathlib import Path
from urllib.parse import urlencode

image = sys.argv[1]
ref = image[len('ghcr.io/'):]
repo, tag = ref.rsplit(':', 1)
hosts = ('ghcr.io', 'ghcr.nju.edu.cn')
accept = ', '.join([
    'application/vnd.oci.image.index.v1+json',
    'application/vnd.docker.distribution.manifest.list.v2+json',
    'application/vnd.oci.image.manifest.v1+json',
    'application/vnd.docker.distribution.manifest.v2+json',
])
min_layer = 262144
min_sample = 65536
deadline = 6.0

class Result:
    def __init__(self):
        self.digest = ''
        self.speed_ok = False
        self.bps = 0.0
        self.timed_out = False

def curl(url, headers, timeout, body, header_file):
    cmd = [
        'curl', '-sS', '--connect-timeout', '2', '--max-time', f'{max(timeout, 0.1):.1f}',
        '-o', str(body), '-D', str(header_file),
        '-w', '%{http_code} %{size_download} %{time_total}',
    ]
    for header in headers:
        cmd.extend(['-H', header])
    cmd.append(url)
    # stdout/stderr/universal_newlines are the names available on Python 3.6.
    proc = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, universal_newlines=True)
    parts = proc.stdout.split()
    code = size = elapsed = None
    if len(parts) >= 3:
        try:
            code = int(float(parts[0]))
            size = int(float(parts[1]))
            elapsed = float(parts[2])
        except ValueError:
            code = size = elapsed = None
    return proc.returncode, code, size, elapsed, header_file.read_text(errors='replace') if header_file.is_file() else ''

def bearer(headers):
    match = re.search(r'(?im)^www-authenticate:\s*(.+)$', headers)
    if not match or not match.group(1).lower().startswith('bearer'):
        return {}
    return dict(re.findall(r'(\w+)="([^"]*)"', match.group(1)))

def header_value(headers, name):
    match = re.search(r'(?im)^%s:\s*(\S+)' % re.escape(name), headers)
    return match.group(1).strip() if match else ''

def host_of(url):
    match = re.match(r'https://([^/?#]+)', url or '')
    return match.group(1).lower() if match else ''

def trusted_digest(value):
    return re.fullmatch(r'sha256:[a-f0-9]{64}', value or '') is not None

def probe(host):
    result = Result()
    started = time.monotonic()
    work = Path(tempfile.mkdtemp(prefix='loopforge-mirror-'))
    token = ''
    try:
        return probe_host(host, result, started, work, token)
    finally:
        shutil.rmtree(work, ignore_errors=True)

def probe_host(host, result, started, work, token):

    def remaining():
        return deadline - (time.monotonic() - started)

    def request(url, headers, redirects=0):
        if remaining() < 0.3:
            result.timed_out = True
            return None
        body = work / 'body'
        headers_path = work / 'headers'
        status, code, size, elapsed, text = curl(url, headers, remaining(), body, headers_path)
        if status == 28:
            result.timed_out = True
            return None
        if status != 0 or code is None:
            return None
        # GHCR answers a layer range with 307 and an empty body, then the CDN
        # returns 206. Follow only HTTPS, and do not send the registry token to
        # another host. The sample is the final response.
        if code in (301, 302, 303, 307, 308):
            location = header_value(text, 'Location')
            if redirects >= 3 or not location.startswith('https://'):
                return None
            next_headers = [
                header for header in headers
                if host_of(location) == host_of(url) or not header.lower().startswith('authorization:')
            ]
            return request(location, next_headers, redirects + 1)
        payload = body.read_bytes() if body.is_file() else b''
        return code, size, elapsed, text, payload

    challenge = request(f'https://{host}/v2/', [])
    if challenge is None:
        return result
    code, _, _, text, _ = challenge
    auth = []
    if code == 401:
        params = bearer(text)
        realm = params.get('realm', '')
        service = params.get('service', '')
        if not realm:
            return result
        query = urlencode({'service': service, 'scope': f'repository:{repo}:pull'})
        join = '&' if '?' in realm else '?'
        token_response = request(realm + join + query, ['Accept: application/json'])
        if token_response is None or token_response[0] != 200:
            return result
        try:
            payload = json.loads(token_response[4].decode())
        except (ValueError, UnicodeError):
            return result
        token = payload.get('token') or payload.get('access_token') or ''
        if not token:
            return result
        auth = [f'Authorization: Bearer {token}']
    elif code != 200:
        return result

    def fetch_manifest(reference):
        response = request(
            f'https://{host}/v2/{repo}/manifests/{reference}',
            auth + [f'Accept: {accept}'],
        )
        if response is None or response[0] != 200:
            return None, None
        digest = header_value(response[3], 'Docker-Content-Digest')
        try:
            document = json.loads(response[4].decode())
        except (ValueError, UnicodeError):
            return digest, None
        return digest, document

    digest, document = fetch_manifest(tag)
    if digest:
        result.digest = digest
    if not isinstance(document, dict):
        return result
    if 'manifests' in document and 'layers' not in document:
        chosen = ''
        fallback = ''
        for item in document.get('manifests') or []:
            if not isinstance(item, dict) or not item.get('digest'):
                continue
            platform = item.get('platform') or {}
            if platform.get('os') == 'unknown' or platform.get('architecture') == 'unknown':
                continue
            if platform.get('os') == 'linux' and platform.get('architecture') in ('amd64', 'arm64'):
                chosen = item['digest']
                break
            fallback = fallback or item['digest']
        child = chosen or fallback
        if not child:
            return result
        _, document = fetch_manifest(child)
        if not isinstance(document, dict):
            return result

    layer = ''
    for item in document.get('layers') or []:
        if not isinstance(item, dict):
            continue
        size = item.get('size') or 0
        layer_digest = item.get('digest') or ''
        if isinstance(size, int) and size >= min_layer and layer_digest.startswith('sha256:'):
            layer = layer_digest
            break
    if not layer:
        return result
    blob = request(
        f'https://{host}/v2/{repo}/blobs/{layer}',
        auth + ['Range: bytes=0-262143'],
    )
    if blob is None or blob[0] not in (200, 206):
        return result
    _, size, elapsed, _, _ = blob
    if size is None or size < min_sample or elapsed is None or elapsed <= 0:
        return result
    result.speed_ok = True
    result.bps = size / elapsed
    return result

results = {}

def run(host):
    try:
        results[host] = probe(host)
    except Exception:
        results[host] = Result()

threads = [threading.Thread(target=run, args=(host,)) for host in hosts]
for thread in threads:
    thread.start()
for thread in threads:
    thread.join(deadline + 1)
for host in hosts:
    results.setdefault(host, Result())

ghcr = results['ghcr.io']
nju = results['ghcr.nju.edu.cn']

def rate(bps):
    if bps >= 1024 * 1024:
        return f'{bps / (1024 * 1024):.1f} MB/s'
    if bps >= 1024:
        return f'{bps / 1024:.0f} KB/s'
    return f'{bps:.0f} B/s'

def side(result):
    if result.speed_ok:
        return f'was {rate(result.bps)}'
    if result.timed_out:
        return 'timed out'
    return 'failed'

# The official manifest digest is the only trusted identity. Without it, or
# when the mirror disagrees, keep the ghcr.io pull. A faster mirror is pulled
# by that digest, not by the mutable tag.
official = ghcr.digest if trusted_digest(ghcr.digest) else ''

def emit(host, digest, message):
    print('%s\t%s\t%s' % (host, digest, message))

if not official:
    emit('ghcr.io', '-', 'Could not verify the ghcr.io digest; using ghcr.io')
    raise SystemExit(0)
if trusted_digest(nju.digest) and nju.digest != official:
    emit('ghcr.io', '-', 'Registry digests differ; using ghcr.io')
    raise SystemExit(0)
nju_verified = nju.speed_ok and nju.digest == official
if nju_verified and (not ghcr.speed_ok or nju.bps >= ghcr.bps * 1.5):
    emit('ghcr.nju.edu.cn', official, 'Fastest registry is ghcr.nju.edu.cn (%s); ghcr.io %s' % (rate(nju.bps), side(ghcr)))
    raise SystemExit(0)
if ghcr.speed_ok:
    emit('ghcr.io', '-', 'Fastest registry is ghcr.io (%s); ghcr.nju.edu.cn %s' % (rate(ghcr.bps), side(nju)))
    raise SystemExit(0)
if nju.speed_ok:
    emit('ghcr.io', '-', 'Could not verify the mirror digest; using ghcr.io')
    raise SystemExit(0)
emit('ghcr.io', '-', 'Could not time either registry; using ghcr.io')
PY
    ); then
      probe_line=$'ghcr.io\t-\tCould not time either registry; using ghcr.io'
    fi
    if [[ "$probe_line" != *$'\t'*$'\t'* ]]; then
      probe_line=$'ghcr.io\t-\tCould not time either registry; using ghcr.io'
    fi
    printf '\n▸ %s\n' "${probe_line##*$'\t'}"
    mirror_host=${probe_line%%$'\t'*}
    mirror_digest=${probe_line#*$'\t'}
    mirror_digest=${mirror_digest%%$'\t'*}
    if [[ "$mirror_host" != ghcr.nju.edu.cn ]]; then
      return 1
    fi
    if [[ ! "$mirror_digest" =~ ^sha256:[a-f0-9]{64}$ ]]; then
      printf '\n▸ Mirror digest is not the ghcr.io digest; using ghcr.io\n'
      return 1
    fi
  fi

  local mirror_image="${mirror_host}/${image#ghcr.io/}"
  if [[ -n "$mirror_digest" ]]; then
    local repo="${image#ghcr.io/}"
    repo="${repo%:*}"
    mirror_image="${mirror_host}/${repo}@${mirror_digest}"
  fi
  printf '\n▸ Pulling %s and tagging it %s\n' "$mirror_image" "$image"
  # docker_access returns the failure on Bash 3.2 and later.
  # An explicit mirror stops here. Automatic selection falls back to ghcr.io.
  if ! docker_access pull "$mirror_image"; then
    if [[ "$auto" == 1 ]]; then
      printf '\n▸ Could not pull %s; using ghcr.io\n' "$mirror_image"
      return 1
    fi
    printf '\n✗ Could not pull %s. Stopped without retagging the previous image.\n' "$mirror_image" >&2
    exit 1
  fi
  if ! docker_access tag "$mirror_image" "$image"; then
    printf '\n✗ Could not tag %s as %s. Stopped without starting.\n' "$mirror_image" "$image" >&2
    exit 1
  fi
}
