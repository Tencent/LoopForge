#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
python3 - <<'PY'
import os,secrets,pathlib,shlex
p=pathlib.Path('.env')
data_dir=os.path.abspath(os.environ.get('LOOPFORGE_DATA_DIR','.data'))
if p.exists():
 text=p.read_text()
 if 'LOOPFORGE_DATA_DIR=' not in text:
  with p.open('a') as f: f.write(f'\nLOOPFORGE_DATA_DIR={shlex.quote(data_dir)}\n')
  print('Added LOOPFORGE_DATA_DIR to existing .env')
 else:
  print('Keeping existing .env')
else:
 password=secrets.token_hex(24)
 http_port=os.environ.get('AGENTFLOW_HTTP_PORT','8090');mysql_port=os.environ.get('LOOPFORGE_MYSQL_PORT','33315')
 data={'LOOPFORGE_DB_PASSWORD':password,'LOOPFORGE_DB_ROOT_PASSWORD':secrets.token_hex(24),'LOOPFORGE_MYSQL_PORT':mysql_port,'COMPOSE_PROJECT_NAME':os.environ.get('COMPOSE_PROJECT_NAME','loopforge'),'AGENTFLOW_HTTP_PORT':http_port,'AGENTFLOW_MYSQL_DSN':f'loopforge:{password}@tcp(127.0.0.1:{mysql_port})/loopforge','AGENTFLOW_SESSION_SECRET':secrets.token_hex(32),'AGENTFLOW_AUTH_REQUIRED':'true','LOOPFORGE_DATA_DIR':data_dir,'LOOPFORGE_BIND':os.environ.get('LOOPFORGE_BIND','0.0.0.0'),'LOOPFORGE_PUBLIC_URL':os.environ.get('LOOPFORGE_PUBLIC_URL',''),'LOOPFORGE_IMAGE':os.environ.get('LOOPFORGE_IMAGE',''),'AGENTFLOW_GITHUB_CLIENT_ID':os.environ.get('AGENTFLOW_GITHUB_CLIENT_ID','')}
 fd=os.open(str(p),os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
 with os.fdopen(fd,'w') as f: f.write(''.join(f'{k}={shlex.quote(v)}\n' for k,v in data.items()))
 print('Created .env with unique local secrets')
cur_text=p.read_text() if p.exists() else ''
with_wk=os.environ.get('LOOPFORGE_WITH_WEKNORA','').strip()
if not with_wk:
 with_wk='0' if 'LOOPFORGE_WITH_WEKNORA=0' in cur_text else '1'
if with_wk=='1':
 cur=p.read_text()
 if 'LOOPFORGE_WITH_WEKNORA=0' in cur:
  cur=cur.replace('LOOPFORGE_WITH_WEKNORA=0','LOOPFORGE_WITH_WEKNORA=1')
  p.write_text(cur)
 wk={'LOOPFORGE_WITH_WEKNORA':'1','WEKNORA_DB_USER':'weknora','WEKNORA_DB_NAME':'weknora','WEKNORA_DB_PASSWORD':secrets.token_hex(24),'WEKNORA_REDIS_PASSWORD':secrets.token_hex(24),'WEKNORA_SYSTEM_AES_KEY':secrets.token_hex(16),'WEKNORA_JWT_SECRET':secrets.token_hex(32),'LOOPFORGE_WEKNORA_ADMIN_PASSWORD':'Lf!'+secrets.token_hex(12)}
 added=[]
 for k,v in wk.items():
  if f'{k}=' not in cur:
   added.append(f'{k}={shlex.quote(v)}\n')
 if added:
  with p.open('a') as f: f.write('\n'+''.join(added))
  print('Added WeKnora secrets to .env')
else:
 cur=p.read_text()
 if 'LOOPFORGE_WITH_WEKNORA=1' in cur:
  p.write_text(cur.replace('LOOPFORGE_WITH_WEKNORA=1','LOOPFORGE_WITH_WEKNORA=0'))
 elif 'LOOPFORGE_WITH_WEKNORA=' not in cur:
  with p.open('a') as f: f.write('\nLOOPFORGE_WITH_WEKNORA=0\n')
PY
