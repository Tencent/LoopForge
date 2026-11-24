#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
python3 - <<'PY'
import os,secrets,pathlib,shlex,base64,datetime
p=pathlib.Path('.env')
data_dir=os.path.abspath(os.environ.get('LOOPFORGE_DATA_DIR','.data'))
def credential_keys():
 # AES-256 key sealing chat Bot secrets; keep it with the database backups.
 return f"k{datetime.date.today():%Y%m}:{base64.b64encode(secrets.token_bytes(32)).decode()}"
if p.exists():
 text=p.read_text()
 added=[]
 if 'LOOPFORGE_DATA_DIR=' not in text:
  with p.open('a') as f: f.write(f'\nLOOPFORGE_DATA_DIR={shlex.quote(data_dir)}\n')
  added.append('LOOPFORGE_DATA_DIR')
 if 'AGENTFLOW_CREDENTIAL_KEYS=' not in text:
  with p.open('a') as f: f.write(f'\nAGENTFLOW_CREDENTIAL_KEYS={shlex.quote(credential_keys())}\n')
  added.append('AGENTFLOW_CREDENTIAL_KEYS')
 print('Added '+', '.join(added)+' to existing .env' if added else 'Keeping existing .env')
else:
 password=secrets.token_hex(24)
 http_port=os.environ.get('AGENTFLOW_HTTP_PORT','8090');mysql_port=os.environ.get('LOOPFORGE_MYSQL_PORT','33315')
 data={'LOOPFORGE_DB_PASSWORD':password,'LOOPFORGE_DB_ROOT_PASSWORD':secrets.token_hex(24),'LOOPFORGE_MYSQL_PORT':mysql_port,'COMPOSE_PROJECT_NAME':os.environ.get('COMPOSE_PROJECT_NAME','loopforge'),'AGENTFLOW_HTTP_PORT':http_port,'AGENTFLOW_MYSQL_DSN':f'loopforge:{password}@tcp(127.0.0.1:{mysql_port})/loopforge','AGENTFLOW_SESSION_SECRET':secrets.token_hex(32),'AGENTFLOW_CREDENTIAL_KEYS':credential_keys(),'AGENTFLOW_AUTH_REQUIRED':'true','LOOPFORGE_DATA_DIR':data_dir,'LOOPFORGE_BIND':os.environ.get('LOOPFORGE_BIND','0.0.0.0'),'LOOPFORGE_PUBLIC_URL':os.environ.get('LOOPFORGE_PUBLIC_URL',''),'LOOPFORGE_IMAGE':os.environ.get('LOOPFORGE_IMAGE',''),'AGENTFLOW_GITHUB_CLIENT_ID':os.environ.get('AGENTFLOW_GITHUB_CLIENT_ID','')}
 fd=os.open(str(p),os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
 with os.fdopen(fd,'w') as f: f.write(''.join(f'{k}={shlex.quote(v)}\n' for k,v in data.items()))
 print('Created .env with unique local secrets')
PY
