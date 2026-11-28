#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
python3 - "$@" <<'PY'
import os,secrets,pathlib,shlex,base64,datetime,re,stat,sys,tempfile
p=pathlib.Path('.env')
data_dir=os.path.abspath(os.environ.get('LOOPFORGE_DATA_DIR','.data'))
project_key_name='AGENTFLOW_PROJECT_CREDENTIAL_KEY'
project_key_only=sys.argv[1:] == ['--project-credential-key']
# Postgres (this fork's deployment target) and the upstream MySQL default.
pg_host=os.environ.get('LOOPFORGE_PG_HOST','127.0.0.1')
pg_port=os.environ.get('LOOPFORGE_PG_PORT','5432')
pg_user=os.environ.get('LOOPFORGE_PG_USER','postgres.cesar')
pg_database=os.environ.get('LOOPFORGE_PG_DATABASE','loopforge')
def random_credential_key():
 return base64.b64encode(secrets.token_bytes(32)).decode()
def credential_keys():
 # AES-256 key sealing chat Bot secrets; keep it with the database backups.
 return f"k{datetime.date.today():%Y%m}:{random_credential_key()}"
def project_credential_key():
 return os.environ.get(project_key_name) or random_credential_key()

def split_comment(raw):
 # Cut a trailing comment off an .env value. A # inside the value is part of it.
 quote=None
 escaped=False
 for index,char in enumerate(raw):
  if escaped:
   escaped=False
   continue
  if char=='\\' and quote!="'":
   escaped=True
   continue
  if quote:
   if char==quote:
    quote=None
   continue
  if char in "'\"":
   quote=char
   continue
  if char=='#' and (index==0 or raw[index-1] in ' \t'):
   return raw[:index],raw[index:]
 return raw,''

def env_lookup(text,name):
 # The value of name in an .env text, unquoted; empty when it is absent or empty.
 matches=list(re.finditer(rf'(?m)^[ \t]*(?:export[ \t]+)?{re.escape(name)}[ \t]*=(.*)$',text))
 if not matches:
  return ''
 value,_=split_comment(matches[-1].group(1).strip())
 if len(value)>=2 and value[0]==value[-1] and value[0] in "'\"":
  try:
   parts=shlex.split(value,comments=False)
  except ValueError:
   return value[1:-1]
  return parts[0] if parts else ''
 return value

def set_env(text,name,value):
 # Set name in an .env text. shlex.quote keeps the value literal, so a
 # password with a shell metacharacter (an & for example) cannot break
 # `set -a; . ./.env`.
 quoted=shlex.quote(value)
 matches=list(re.finditer(rf'(?m)^[ \t]*(?:export[ \t]+)?{re.escape(name)}[ \t]*=([^\r\n]*)',text))
 if not matches:
  return text+f'\n{name}={quoted}\n'
 match=matches[-1]
 _,comment=split_comment(match.group(1))
 suffix=(' '+comment) if comment.strip() else ''
 return text[:match.start(1)]+quoted+suffix+text[match.end(1):]

def pg_password(text=''):
 # LOOPFORGE_PG_PASSWORD is the platform's own name; POSTGRES_PASSWORD is the
 # name the Supabase .env already uses and is accepted as a fallback.
 for name in ('LOOPFORGE_PG_PASSWORD','POSTGRES_PASSWORD'):
  value=os.environ.get(name,'').strip()
  if value:
   return value
  value=env_lookup(text,name).strip()
  if value:
   return value
 return ''

def pg_dsn(password):
 return f'postgres://{pg_user}:{password}@{pg_host}:{pg_port}/{pg_database}?sslmode=disable'

def split_hostport(hostport):
 # An IPv6 literal is bracketed, so the colons inside it do not end the host.
 if hostport.startswith('['):
  close=hostport.find(']')
  if close>0:
   host=hostport[:close+1]
   rest=hostport[close+1:]
   return (host,rest[1:]) if rest.startswith(':') else (host,'')
 host,colon,port=hostport.partition(':')
 if not colon or not port.isdigit():
  return hostport,''
 return host,port

def dsn_parts(dsn):
 # Split a DSN by hand: scheme, userinfo, host, port, path and query.
 # A URL parser would misread a password that contains '#' (the fragment
 # separator) or '/' (the path separator), so the pieces are located by
 # hand: the last '@' ends the userinfo, and the first '/' and '?' after it
 # end the host and the path.
 scheme,sep,rest=dsn.partition('://')
 if not sep:
  return ('','','','','','')
 userinfo,at,after=rest.rpartition('@')
 if not at:
  userinfo,after='',rest
 # The query starts at the first '?' and the path ends at the first '/'; the
 # database name may be absent. The userinfo is already split off, so a '?'
 # inside the password is not mistaken for the query separator.
 authority,question,query=after.partition('?')
 hostport,slash,path=authority.partition('/')
 host,port=split_hostport(hostport)
 return (scheme,userinfo,host,port,path,query if question else '')

def compose_dsn(dsn,host):
 # The same database reached from a Compose container: the host publishes
 # host.docker.internal, the native process connects to 127.0.0.1. The result
 # is the native DSN with the host replaced, byte for byte.
 scheme,userinfo,_,port,path,query=dsn_parts(dsn)
 netloc=f'{userinfo}@{host}' if userinfo else host
 if port:
  netloc+=f':{port}'
 result=f'{scheme}://{netloc}'
 if path:
  result+=f'/{path}'
 if query:
  result+=f'?{query}'
 return result

def compose_host(dsn):
 # A database on this machine is reached through the Docker host gateway; a
 # database elsewhere keeps the host of the native DSN.
 override=os.environ.get('LOOPFORGE_PG_COMPOSE_HOST','').strip()
 if override:
  return override
 host=dsn_parts(dsn)[2]
 bare=host[1:-1] if host.startswith('[') and host.endswith(']') else host
 return 'host.docker.internal' if bare in ('','127.0.0.1','localhost','::1') else host

def configure_project_credential(text):
 matches = list(re.finditer(rf'(?m)^[ \t]*(?:export[ \t]+)?{project_key_name}[ \t]*=([^\r\n]*)', text))
 if not matches:
  return text + f'\n{project_key_name}={shlex.quote(project_credential_key())}\n'
 match = matches[-1]
 empty = re.fullmatch(r"""[ \t]*(?:""|'')?(?:[ \t]+(#.*))?[ \t]*""", match[1])
 if empty is None:
  return text
 value = shlex.quote(project_credential_key())
 if empty[1]:
  value += ' ' + empty[1]
 return text[:match.start(1)] + value + text[match.end(1):]

def save_existing_env(text):
 target = p.resolve()
 mode = stat.S_IMODE(target.stat().st_mode)
 fd, temporary = tempfile.mkstemp(prefix=f'{target.name}.', dir=target.parent)
 try:
  with os.fdopen(fd, 'w', newline='') as env:
   env.write(text)
   os.fchmod(env.fileno(), mode)
  os.replace(temporary, target)
 finally:
  pathlib.Path(temporary).unlink(missing_ok=True)

if p.exists():
 with p.open(newline='') as env:
  text = env.read()
 added=[]
 if not project_key_only:
  if 'LOOPFORGE_DATA_DIR=' not in text:
   text += f'\nLOOPFORGE_DATA_DIR={shlex.quote(data_dir)}\n'
   added.append('LOOPFORGE_DATA_DIR')
  if 'AGENTFLOW_CREDENTIAL_KEYS=' not in text:
   text += f'\nAGENTFLOW_CREDENTIAL_KEYS={shlex.quote(credential_keys())}\n'
   added.append('AGENTFLOW_CREDENTIAL_KEYS')
  # The deployment scripts read the HTTP port from the environment under
  # `set -u`, so an .env without it stops the platform before it starts.
  if not env_lookup(text,'AGENTFLOW_HTTP_PORT'):
   text=set_env(text,'AGENTFLOW_HTTP_PORT',os.environ.get('AGENTFLOW_HTTP_PORT','8090'))
   added.append('AGENTFLOW_HTTP_PORT')
  # An installation made before the Postgres port has no DSN yet. Fill it in
  # instead of leaving the platform without a database.
  native=env_lookup(text,'AGENTFLOW_DATABASE_URL')
  if not native:
   password=pg_password(text)
   if not password:
    print('AGENTFLOW_DATABASE_URL is missing and no Postgres password was found.\n'
          '  Set LOOPFORGE_PG_PASSWORD (or POSTGRES_PASSWORD) in .env or in the environment,\n'
          '  then run this script again to write the Postgres DSN.',file=sys.stderr)
   else:
    native=pg_dsn(password)
    text=set_env(text,'AGENTFLOW_DATABASE_URL',native)
    added.append('AGENTFLOW_DATABASE_URL')
  if native:
   compose=env_lookup(text,'LOOPFORGE_COMPOSE_DATABASE_URL')
   # The Compose DSN is the Postgres one with a different host. A MySQL DSN
   # has no Compose counterpart, and deriving one from it would write a
   # meaningless connection string into .env.
   if not compose and native.startswith(('postgres://','postgresql://')):
    text=set_env(text,'LOOPFORGE_COMPOSE_DATABASE_URL',compose_dsn(native,compose_host(native)))
    added.append('LOOPFORGE_COMPOSE_DATABASE_URL')
 configured = configure_project_credential(text)
 if configured != text:
  added.append(project_key_name)
 if added:
  save_existing_env(configured)
 print('Configured '+', '.join(added)+' in existing .env' if added else 'Keeping existing .env')
elif project_key_only:
 sys.exit('Not installed yet. Run ./start.sh first.')
else:
 http_port=os.environ.get('AGENTFLOW_HTTP_PORT','8090')
 password=pg_password()
 if password:
  dsn=pg_dsn(password)
  data={'AGENTFLOW_DATABASE_URL':dsn,'LOOPFORGE_COMPOSE_DATABASE_URL':compose_dsn(dsn,compose_host(dsn))}
  note='Created .env with unique local secrets'
 else:
  # No Postgres password: keep the upstream MySQL default path working. The
  # operator can switch to Postgres later with scripts/configure.sh.
  mysql_password=secrets.token_hex(24)
  mysql_port=os.environ.get('LOOPFORGE_MYSQL_PORT','33315')
  data={'LOOPFORGE_DB_PASSWORD':mysql_password,'LOOPFORGE_DB_ROOT_PASSWORD':secrets.token_hex(24),'LOOPFORGE_MYSQL_PORT':mysql_port,'AGENTFLOW_MYSQL_DSN':f'loopforge:{mysql_password}@tcp(127.0.0.1:{mysql_port})/loopforge'}
  note=('Created .env with unique local secrets. No Postgres password was found, so the '
        'upstream MySQL variables were written instead. Set LOOPFORGE_PG_PASSWORD (or '
        'POSTGRES_PASSWORD) and rerun scripts/configure.sh to switch to Postgres.')
 data.update({'COMPOSE_PROJECT_NAME':os.environ.get('COMPOSE_PROJECT_NAME','loopforge'),'AGENTFLOW_HTTP_PORT':http_port,'AGENTFLOW_SESSION_SECRET':secrets.token_hex(32),'AGENTFLOW_CREDENTIAL_KEYS':credential_keys(),'AGENTFLOW_AUTH_REQUIRED':'true','LOOPFORGE_DATA_DIR':data_dir,'LOOPFORGE_BIND':os.environ.get('LOOPFORGE_BIND','0.0.0.0'),'LOOPFORGE_PUBLIC_URL':os.environ.get('LOOPFORGE_PUBLIC_URL',''),'LOOPFORGE_IMAGE':os.environ.get('LOOPFORGE_IMAGE',''),'AGENTFLOW_GITHUB_CLIENT_ID':os.environ.get('AGENTFLOW_GITHUB_CLIENT_ID','')})
 data[project_key_name]=project_credential_key()
 fd=os.open(str(p),os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
 with os.fdopen(fd,'w') as f: f.write(''.join(f'{k}={shlex.quote(v)}\n' for k,v in data.items()))
 print(note)
PY
