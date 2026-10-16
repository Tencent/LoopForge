#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
python3 - <<'PY'
import os,secrets,pathlib
p=pathlib.Path('.env')
data_dir=os.path.abspath(os.environ.get('LOOPFORGE_DATA_DIR','.data'))
if p.exists():
 text=p.read_text()
 if 'LOOPFORGE_DATA_DIR=' not in text:
  with p.open('a') as f: f.write(f'LOOPFORGE_DATA_DIR="{data_dir}"\n')
  print('Added LOOPFORGE_DATA_DIR to existing .env')
 else:
  print('Keeping existing .env')
else:
 password=secrets.token_hex(24)
 http_port=os.environ.get('AGENTFLOW_HTTP_PORT','8090');mysql_port=os.environ.get('LOOPFORGE_MYSQL_PORT','33315')
 data={'LOOPFORGE_DB_PASSWORD':password,'LOOPFORGE_DB_ROOT_PASSWORD':secrets.token_hex(24),'LOOPFORGE_MYSQL_PORT':mysql_port,'COMPOSE_PROJECT_NAME':os.environ.get('COMPOSE_PROJECT_NAME','loopforge'),'AGENTFLOW_HTTP_PORT':http_port,'AGENTFLOW_MYSQL_DSN':f'loopforge:{password}@tcp(127.0.0.1:{mysql_port})/loopforge','AGENTFLOW_SESSION_SECRET':secrets.token_hex(32),'AGENTFLOW_AUTH_REQUIRED':'true','LOOPFORGE_DATA_DIR':data_dir}
 fd=os.open(str(p),os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
 with os.fdopen(fd,'w') as f: f.write(''.join(f'{k}="{v}"\n' for k,v in data.items()))
 print('Created .env with unique local secrets')
PY
