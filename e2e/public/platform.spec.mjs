import {test,expect} from '@playwright/test';
import fs from 'node:fs';
import {password} from './helpers.mjs';

async function signIn(page){
  await page.goto('/login.html');
  await page.locator('[name="login"]').fill(process.env.LOOPFORGE_TEST_USER || 'admin');
  await page.locator('[name="password"]').fill(password());
  await page.locator('button[type="submit"]').click();
  await expect(page.locator('#projectSwitcher')).toBeVisible();
  await page.locator('#projectSwitcher').selectOption('welcome');
}

test('password authentication loads the upstream workspace',async({page,request,baseURL})=>{
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  const anonymous=await request.get('/api/bootstrap');expect(anonymous.status()).toBe(401);
  const forged=await request.post('/api/auth/password',{headers:{Origin:'https://attacker.invalid'},data:{login:'admin',password:password()}});expect(forged.status()).toBe(403);
  await signIn(page);
  await expect(page.locator('.nav[data-view="issues"]')).toBeVisible();
  await expect(page.locator('.nav[data-view="sops"]')).toBeVisible();
  expect(errors).toEqual([]);
  const cookie=(await page.context().cookies(baseURL)).find(c=>c.name==='agentflow_user');
  expect(cookie.httpOnly).toBe(true);expect(cookie.sameSite).toBe('Lax');
  await page.request.post('/api/auth/logout');
  await page.goto('/');
  await expect(page).toHaveURL(/login\.html$/);
});

test('Chinese/English and light/dark preferences persist across reloads',async({page},info)=>{
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await signIn(page);
  await page.locator('.nav[data-view="issues"]').click();
  await expect(page.locator('html')).toHaveAttribute('lang','zh-CN');
  await page.screenshot({path:info.outputPath('light-zh.png'),fullPage:true});
  await page.locator('#themeToggle').click();
  await expect(page.locator('html')).toHaveAttribute('data-theme','dark');
  await page.mouse.move(500,500);
  await page.locator('#langToggle').click();
  await expect(page.locator('html')).toHaveAttribute('lang','en');
  await expect(page.locator('.nav[data-view="issues"]')).toContainText('Requirements');
  await page.screenshot({path:info.outputPath('dark-en.png'),fullPage:true});
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-theme','dark');
  await expect(page.locator('html')).toHaveAttribute('lang','en');
  // Translation must leave source code, editable content and user-marked text intact.
  await page.evaluate(()=>{
    for(const tag of ['pre','textarea']){const el=document.createElement(tag);el.id='test-'+tag;el.textContent='需求列表';document.body.append(el)}
    const el=document.createElement('div');el.id='test-user-text';el.dataset.i18nSkip='';el.textContent='需求列表';document.body.append(el);
  });
  await expect(page.locator('#test-pre')).toHaveText('需求列表');
  await expect(page.locator('#test-textarea')).toHaveValue('需求列表');
  await expect(page.locator('#test-user-text')).toHaveText('需求列表');
  expect(errors).toEqual([]);
});

test('real script artifacts survive a human approval and execution continues',async({page})=>{
  await signIn(page);
  const response=await page.request.post('/api/runs',{data:{workflowId:'welcome-workflow',projectId:'welcome',input:'Browser acceptance: artifacts, approval and continuation'}});
  expect(response.ok()).toBe(true);
  const run=await response.json();
  try {
  const read=async()=>{const r=await page.request.get(`/api/runs/${run.id}`);expect(r.ok()).toBe(true);return r.json()};
  await expect.poll(async()=>(await read()).status,{timeout:30_000}).toBe('waiting');
  let detail=await read();expect(detail.nodes.prepare.status).toBe('succeeded');
  // Fresh MySQL installations need the generated adapter column used by run details.
  const telemetry=await page.request.get(`/api/runs/${run.id}/telemetry?adapter=live`);
  expect(telemetry.ok(),await telemetry.text()).toBe(true);
  const artifact=detail.artifacts.find(a=>a.name==='welcome');expect(artifact).toBeTruthy();
  const content=await page.request.get(`/api/runs/${run.id}/artifacts/${encodeURIComponent(artifact.id)}`);
  expect(await content.text()).toContain('LoopForge');
  const approval=await page.request.post(`/api/runs/${run.id}/approve`,{data:{nodeId:'approve',context:'Verified the saved report'}});
  expect(approval.ok()).toBe(true);
  await expect.poll(async()=>(await read()).status,{timeout:30_000}).toBe('succeeded');
  detail=await read();expect(detail.nodes.finish.status).toBe('succeeded');
  expect(detail.artifacts.some(a=>a.name==='completed')).toBe(true);
  await page.locator('.nav[data-view="runs"]').click();
  await expect(page.locator('#runsView')).toBeVisible();
  } finally {
    if(!process.env.AGENTFLOW_E2E_KEEP){
      await page.request.post(`/api/runs/${run.id}/cancel`,{data:{}}).catch(()=>{});
      await page.request.delete(`/api/runs/${run.id}`).catch(()=>{});
    }
  }
});
