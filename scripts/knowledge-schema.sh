#!/usr/bin/env bash
# Apply or verify the knowledge-engine schema.
#
#   bash scripts/knowledge-schema.sh apply [--compose|--native]
#   bash scripts/knowledge-schema.sh check [--compose|--native]
#
# `apply` executes migrations/026_*.sql onward in numeric order. A script can
# carry `-- @plane control` / `-- @plane content` markers; each segment runs
# against the control database (AGENTFLOW_MYSQL_DSN) or the content database
# (AGENTFLOW_KNOWLEDGE_MYSQL_DSN, falling back to the control DSN). Scripts
# without markers are control-plane only.
#
# `check` fails with an explicit repair command when the ngram full-text index
# required by the MySQL keyword adapter is absent. Neither command runs from
# the Go process: install/upgrade scripts call this before starting the API.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/docker-access.sh

fail() { printf '\n✗ %s\n' "$1" >&2; exit 1; }
step() { printf '\n▸ %s\n' "$1"; }

command_name=""
mode="compose"
for arg in "$@"; do
  case "$arg" in
    apply|check) command_name="$arg" ;;
    --compose) mode="compose" ;;
    --native) mode="native" ;;
    -h|--help) printf 'usage: %s {apply|check} [--compose|--native]\n' "$0"; exit 0 ;;
    *) fail "unknown argument: $arg" ;;
  esac
done
[[ -n "$command_name" ]] || fail 'usage: scripts/knowledge-schema.sh {apply|check} [--compose|--native]'

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi

if [[ "$mode" == "compose" ]]; then
  select_docker_access || docker_access_fail 'Docker is not running, or this user cannot access it.'
fi

main_dsn="${AGENTFLOW_MYSQL_DSN:-}"
content_dsn="${AGENTFLOW_KNOWLEDGE_MYSQL_DSN:-}"
[[ -n "$main_dsn" ]] || fail 'AGENTFLOW_MYSQL_DSN is not configured.'

# Parse a go-sql-driver DSN into shell-safe values. Only tcp/unix networks are
# accepted; the database name is restricted to characters that are safe in an
# SQL identifier because 036 quotes it in cross-schema migration SQL.
parse_dsn() {
  local prefix=$1 dsn=$2 parsed
  parsed=$(python3 - "$prefix" "$dsn" <<'PY'
import re, shlex, sys
prefix, dsn = sys.argv[1], sys.argv[2]
slash = dsn.find('/')
if slash < 0:
    raise SystemExit('invalid MySQL DSN (missing /database): ' + dsn)
left, right = dsn[:slash], dsn[slash + 1:]
database, _, _ = right.partition('?')
at = left.rfind('@')
if at >= 0:
    userinfo, network_part = left[:at], left[at + 1:]
else:
    userinfo, network_part = '', left
user, _, password = userinfo.partition(':')
if '(' in network_part:
    network, _, address = network_part.partition('(')
    address = address.rstrip(')')
elif network_part:
    network, address = network_part, ''
else:
    network, address = 'tcp', ''
network = network.strip() or 'tcp'
address = address.strip()
if network == 'unix':
    host, port = address, ''
elif network == 'tcp':
    host, port = address, '3306'
    if address.startswith('['):
        host, _, tail = address.partition(']')
        host = host + ']'
        if tail.startswith(':'):
            port = tail[1:]
    elif ':' in address:
        host, port = address.rsplit(':', 1)
    else:
        host = address
else:
    raise SystemExit(f'unsupported MySQL network {network!r}')
host = host or '127.0.0.1'
port = port or '3306'
if not re.fullmatch(r'[A-Za-z0-9_$.-]+', database):
    raise SystemExit(f'unsafe or empty database name in DSN: {database!r}')
values = {
    'USER': user or 'root',
    'PASSWORD': password,
    'HOST': host,
    'PORT': port,
    'NETWORK': network,
    'DB': database,
}
for key, value in values.items():
    print(f'{prefix}_{key}={shlex.quote(value)}')
PY
) || fail "invalid $prefix DSN; expected user:password@tcp(host:port)/database"
  eval "$parsed"
}

parse_dsn MAIN "$main_dsn"
if [[ -z "$content_dsn" ]]; then
  content_dsn="$main_dsn"
fi
parse_dsn CONTENT "$content_dsn"

loopback() {
  case "$1" in
    127.0.0.1|localhost|::1|'[::1]') return 0 ;;
  esac
  return 1
}

# compose_local reports whether a DSN resolves to the MySQL service started by
# this Compose project, where the mysql client connects through the container's
# local socket. Anything else is dialed by host/port from the mysql container
# (compose mode) or from the host (native mode).
compose_local() {
  local host=$1 port=$2 expected_port=${LOOPFORGE_MYSQL_PORT:-33315}
  [[ "$host" == "mysql" ]] && return 0
  loopback "$host" && [[ "$port" == "$expected_port" ]]
}

mysql_exec() {
  local plane=$1
  local user pass host port db network
  if [[ "$plane" == "control" ]]; then
    user=$MAIN_USER; pass=$MAIN_PASSWORD; host=$MAIN_HOST; port=$MAIN_PORT; db=$MAIN_DB; network=$MAIN_NETWORK
  else
    user=$CONTENT_USER; pass=$CONTENT_PASSWORD; host=$CONTENT_HOST; port=$CONTENT_PORT; db=$CONTENT_DB; network=$CONTENT_NETWORK
  fi
  if [[ "$network" == "unix" ]]; then
    if [[ "$mode" == "compose" ]]; then
      docker_access compose exec -T -e MYSQL_PWD="$pass" mysql mysql --batch --skip-column-names --default-character-set=utf8mb4 -u "$user" "$db"
    else
      MYSQL_PWD="$pass" mysql --batch --skip-column-names --default-character-set=utf8mb4 -S "$host" -u "$user" "$db"
    fi
    return
  fi
  if compose_local "$host" "$port"; then
    if [[ "$mode" == "compose" || "$mode" == "native" ]]; then
      docker_access compose exec -T -e MYSQL_PWD="$pass" mysql mysql --batch --skip-column-names --default-character-set=utf8mb4 -u "$user" "$db"
      return
    fi
  fi
  if [[ "$mode" == "compose" ]]; then
    docker_access compose exec -T -e MYSQL_PWD="$pass" mysql mysql --batch --skip-column-names --default-character-set=utf8mb4 \
      -h "$host" -P "$port" -u "$user" "$db"
  else
    command -v mysql >/dev/null || fail "mysql client is required to reach $host:$port; install it or set the DSN to this Compose project's MySQL."
    MYSQL_PWD="$pass" mysql --batch --skip-column-names --default-character-set=utf8mb4 -h "$host" -P "$port" -u "$user" "$db"
  fi
}

split_segments() {
  local file=$1 outdir=$2
  python3 - "$file" "$outdir" <<'PY'
import pathlib, re, sys
source, outdir = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
plane = 'control'
segment = []
index = 0

def flush():
    global index
    meaningful = [line for line in segment if line.strip() and not line.lstrip().startswith('--')]
    if not meaningful:
        segment.clear()
        return
    base = outdir / f'{index:03d}'
    base.with_suffix('.sql').write_text(''.join(segment))
    base.with_suffix('.plane').write_text(plane + '\n')
    index += 1
    segment.clear()

for line in source.read_text().splitlines(keepends=True):
    match = re.match(r'^\s*--\s*@plane\s+(control|content)\s*$', line.rstrip('\n'))
    if match:
        flush()
        plane = match.group(1)
        continue
    segment.append(line)
flush()
PY
}

apply_migrations() {
  local control_schema
  control_schema=$(python3 - "$MAIN_DB" <<'PY'
import sys
print(sys.argv[1].replace("'", "''"))
PY
)
  local files=() file base num
  shopt -s nullglob
  files=(migrations/[0-9][0-9][0-9]_*.sql)
  shopt -u nullglob
  [[ ${#files[@]} -gt 0 ]] || fail 'no migrations/*.sql files found.'
  for file in "${files[@]}"; do
    base=$(basename "$file")
    num=${base%%_*}
    if ((10#$num < 26)); then
      continue
    fi
    local outdir
    outdir=$(mktemp -d)
    split_segments "$file" "$outdir"
    local segment plane
    for segment in "$outdir"/*.sql; do
      plane=$(cat "${segment%.sql}.plane")
      step "Applying $base ($plane)"
      if [[ "$plane" == "content" ]]; then
        {
          printf "SET @knowledge_control_schema = '%s';\n" "$control_schema"
          cat "$segment"
        } | mysql_exec "$plane" || fail "failed to apply $base to the $plane database; check the DSN and rerun bash scripts/knowledge-schema.sh apply"
      else
        mysql_exec "$plane" < "$segment" || fail "failed to apply $base to the $plane database; check the DSN and rerun bash scripts/knowledge-schema.sh apply"
      fi
    done
    rm -rf "$outdir"
  done
}

check_ngram() {
  local index_type ddl
  index_type=$(printf "%s\n" \
    "SELECT INDEX_TYPE FROM information_schema.STATISTICS" \
    "WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'knowledge_chunks'" \
    "AND INDEX_NAME = 'ft_knowledge_chunks_text' LIMIT 1;" | mysql_exec content | tr -d '\r')
  if [[ "$index_type" != "FULLTEXT" ]]; then
    fail "content database '$CONTENT_DB' is missing the knowledge full-text index ft_knowledge_chunks_text. Retrieval fails with \"Can't find FULLTEXT index matching the column list\". Repair: bash scripts/knowledge-schema.sh apply (or run migrations/026_builtin_knowledge.sql on the content database)."
  fi
  ddl=$(printf '%s\n' 'SHOW CREATE TABLE knowledge_chunks;' | mysql_exec content | tr -d '\r')
  if ! grep -qi 'ngram' <<<"$ddl"; then
    fail "content database '$CONTENT_DB' has ft_knowledge_chunks_text but it does not use the ngram parser. Repair: bash scripts/knowledge-schema.sh apply (or run migrations/026_builtin_knowledge.sql on the content database)."
  fi
  printf 'Knowledge full-text index ready on content database %s.\n' "$CONTENT_DB"
}

case "$command_name" in
  apply) apply_migrations ;;
  check) check_ngram ;;
esac
