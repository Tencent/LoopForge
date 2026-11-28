#!/usr/bin/env python3
"""Split a Postgres DSN into the libpq environment variables psql reads.

The DSN arrives on stdin, never as a command line argument: an argument
shows up in the process list of every local user. The script prints
`export NAME='value'` lines for the shell to evaluate; the values are
quoted with shlex.quote, so they are shell literals.

A connection parameter the DSN carries that has no libpq environment
variable is reported as an error instead of being dropped silently.
"""
import shlex
import sys
from urllib.parse import parse_qsl, unquote

# The libpq environment variable every connection parameter maps to.
QUERY_VARIABLES = {
    'host': 'PGHOST',
    'hostaddr': 'PGHOSTADDR',
    'port': 'PGPORT',
    'dbname': 'PGDATABASE',
    'user': 'PGUSER',
    'password': 'PGPASSWORD',
    'passfile': 'PGPASSFILE',
    'connect_timeout': 'PGCONNECT_TIMEOUT',
    'application_name': 'PGAPPNAME',
    'fallback_application_name': 'PGFALLBACKAPPLICATIONNAME',
    'client_encoding': 'PGCLIENTENCODING',
    'options': 'PGOPTIONS',
    'search_path': 'PGOPTIONS',
    'target_session_attrs': 'PGTARGETSESSIONATTRS',
    'sslmode': 'PGSSLMODE',
    'sslkey': 'PGSSLKEY',
    'sslcert': 'PGSSLCERT',
    'sslrootcert': 'PGSSLROOTCERT',
    'sslcrl': 'PGSSLCRL',
    'sslpassword': 'PGSSLASSWORD',
    'gssencmode': 'PGGSSENCMODE',
    'channel_binding': 'PGCHANNELBINDING',
    'sslsni': 'PGSSLSNI',
    'service': 'PGSERVICE',
}


def fail(message):
    print('pg-connection-env: ' + message, file=sys.stderr)
    sys.exit(2)


def split_hostport(hostport):
    # An IPv6 literal is bracketed, so the colons inside it do not end the host.
    if hostport.startswith('['):
        close = hostport.find(']')
        if close > 0:
            host = hostport[:close + 1]
            rest = hostport[close + 1:]
            return (host, rest[1:]) if rest.startswith(':') else (host, '')
    host, colon, port = hostport.partition(':')
    if not colon or not port.isdigit():
        return hostport, ''
    return host, port


def parse(dsn):
    """Split a Postgres URL into its pieces.

    The userinfo ends at the last '@', so a password that still carries a
    raw '@' does not cut the host in half. The query starts at the first '?'
    and the path at the first '/' behind the userinfo, so a raw '?' or '/'
    inside the password is not mistaken for a separator.
    """
    text = dsn.strip()
    scheme, separator, rest = text.partition('://')
    if not separator or scheme.lower() not in ('postgres', 'postgresql'):
        fail('the value is not a Postgres URL')
    userinfo, at, after = rest.rpartition('@')
    if not at:
        userinfo, after = '', rest
    authority, question, query = after.partition('?')
    hostport, slash, path = authority.partition('/')
    host, port = split_hostport(hostport)
    user, colon, password = userinfo.partition(':')
    if not colon:
        password = ''
    return {
        'host': host,
        'port': port,
        # The userinfo of a DSN written by scripts/configure.sh is
        # percent-encoded, so it is decoded here. A password that carries a
        # literal '%' would need to be written as '%25'.
        'user': unquote(user),
        'password': unquote(password),
        'database': unquote(path),
        'query': query if question else '',
    }


def main():
    parts = parse(sys.stdin.read())
    if not parts['database']:
        fail('the DSN names no database')
    values = {}
    if parts['host']:
        values['PGHOST'] = parts['host']
    values['PGPORT'] = parts['port'] or '5432'
    if parts['user']:
        values['PGUSER'] = parts['user']
    if parts['password']:
        values['PGPASSWORD'] = parts['password']
    values['PGDATABASE'] = parts['database']
    # A query parameter overrides the matching piece of the authority, the
    # way libpq itself reads a connection URI.
    overrides = {}
    for key, value in parse_qsl(parts['query'], keep_blank_values=True):
        name = QUERY_VARIABLES.get(key.lower())
        if name is None:
            fail("the DSN carries the connection parameter '%s', which has no libpq "
                 "environment variable; pass it to psql another way" % key)
        if name in overrides and overrides[name] != value:
            fail("the DSN sets '%s' twice, with different values" % key)
        overrides[name] = value
    values.update(overrides)
    for name in sorted(values):
        print('export %s=%s' % (name, shlex.quote(values[name])))


main()
