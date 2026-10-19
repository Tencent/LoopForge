// Explicit setup for a disposable acceptance environment. Never prints tokens.
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const root=path.dirname(fileURLToPath(import.meta.url));
const base=(process.env.AGENTFLOW_E2E_BASE||'http://127.0.0.1:8090').replace(/\/$/,'');
const password=process.env.LOOPFORGE_TEST_PASSWORD||fs.readFileSync(process.env.LOOPFORGE_TEST_PASSWORD_FILE||path.join(root,'../.local/admin-password'),'utf8').trim();
const login=await fetch(base+'/api/auth/password',{method:'POST',headers:{Origin:base,'Content-Type':'application/json'},body:JSON.stringify({login:process.env.LOOPFORGE_TEST_USER||'admin',password})});
if(!login.ok)throw new Error('Acceptance login: HTTP '+login.status);
const cookie=login.headers.get('set-cookie').split(';')[0];
const response=await fetch(base+'/api/auth/tokens',{method:'POST',headers:{Origin:base,Cookie:cookie,'Content-Type':'application/json'},body:JSON.stringify({name:'e2e-acceptance-'+Date.now()})});
if(!response.ok)throw new Error('Acceptance token creation: HTTP '+response.status);
const data=await response.json();
if(!data.token)throw new Error('Token response has no token');
const target=process.env.AGENTFLOW_E2E_TOKEN_FILE||path.join(root,'.auth/api-token');
fs.mkdirSync(path.dirname(target),{recursive:true,mode:0o700});
const fd=fs.openSync(target,'w',0o600);fs.writeFileSync(fd,data.token);fs.closeSync(fd);fs.chmodSync(target,0o600);
console.log('Acceptance API token saved to the protected file. Revoke it after testing a persistent deployment.');
