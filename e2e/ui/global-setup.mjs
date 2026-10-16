import fs from 'node:fs';
import {request} from '@playwright/test';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { ensureProject, tokenOwner } from '../lib/api.mjs';
import { cfg } from '../lib/env.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
export const identityFile = path.join(here, '..', '.auth', 'identity.json');

export default async function globalSetup() {
  await ensureProject();
  const user = await tokenOwner();
  fs.mkdirSync(path.dirname(identityFile), { recursive: true });
  fs.writeFileSync(identityFile, JSON.stringify({ user, projectId: cfg.projectId }, null, 2), {mode:0o600});
  const password = process.env.LOOPFORGE_TEST_PASSWORD || fs.readFileSync(process.env.LOOPFORGE_TEST_PASSWORD_FILE||path.resolve(here,'../../.local/admin-password'),'utf8').trim();
  const context=await request.newContext({baseURL:cfg.base});
  try {
    const login=await context.post('/api/auth/password',{headers:{Origin:cfg.base},data:{login:process.env.LOOPFORGE_TEST_USER||'admin',password}});
    if(!login.ok())throw new Error('Real browser-session login failed: HTTP '+login.status());
    const current=await context.get('/api/auth/current').then(r=>r.json());
    if(current.user.id!==user.id)throw new Error('Browser and API credentials must belong to the same account');
    await context.storageState({path:path.join(here,'../.auth/browser.json')});
    fs.chmodSync(path.join(here,'../.auth/browser.json'),0o600);
  } finally {await context.dispose()}
}
