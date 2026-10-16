import {test,expect} from '@playwright/test';
import fs from 'node:fs';
import {password as adminPassword} from './helpers.mjs';

// Credential UI must never enter a Playwright trace or screenshot artifact.
test.use({trace:'off',screenshot:'off'});

test('a user without model credentials can draft, configure credentials and return without losing text',async({page})=>{
  test.skip(!process.env.LOOPFORGE_UNCONFIGURED_USER,'Provision a password account without model credentials for this regression');
  const password=process.env.LOOPFORGE_UNCONFIGURED_PASSWORD || fs.readFileSync(process.env.LOOPFORGE_UNCONFIGURED_PASSWORD_FILE,'utf8').trim();
  await page.goto('/login.html');
  await page.locator('[name="login"]').fill(process.env.LOOPFORGE_UNCONFIGURED_USER);
  await page.locator('[name="password"]').fill(password);
  await page.locator('button[type="submit"]').click();
  await expect(page.locator('#projectSwitcher')).toBeVisible();
  await page.goto('/#/collaboration');
  const current=await page.request.get('/api/auth/current').then(r=>r.json());
  const path=`/api/users/${current.user.id}/credentials`;
  // This is an inert value for a UI state transition. No model request is sent.
  const marker='e2e-placeholder-credential-not-for-model-use';
  const draft='保留这份尚未发送的需求草稿';
  const input=page.locator('#aiAssistant .ai-compose-input');
  try {
    await expect(input).toBeEnabled();
    await input.fill(draft);
    await expect(page.locator('[data-configure-credentials]')).toBeVisible();
    await expect(page.locator('#aiAssistant button[type="submit"]')).toBeDisabled();
    await page.locator('[data-configure-credentials]').click();
    await expect(page.locator('#userCredentialForm [name="codeBuddyApiKey"]')).toBeFocused();
    await page.locator('#userCredentialForm [name="codeBuddyApiKey"]').fill(marker);
    await page.locator('#userCredentialForm button[type="submit"]').click();
    await expect(page.locator('#userCredentialForm [name="codeBuddyApiKey"]')).toHaveValue('');
    await page.locator('.brand[data-view="collaboration"]').click();
    await expect(input).toHaveValue(draft);
    await expect(input).toBeEnabled();
    await expect(page.locator('#aiAssistant button[type="submit"]')).toBeEnabled();
    await expect(page.locator('[data-configure-credentials]')).toBeHidden();
  } finally {
    const cleared=await page.request.put(path,{data:{codeBuddyApiKeyClear:true}});
    expect(cleared.ok()).toBe(true);
  }
});

test('the configured home composer sends a real model reply and reloads its saved conversation',async({page})=>{
  test.skip(process.env.AGENTFLOW_E2E_MODEL!=='1','Real model calls require AGENTFLOW_E2E_MODEL=1');
  test.setTimeout(300_000);
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.goto('/login.html');
  await page.locator('[name="login"]').fill(process.env.LOOPFORGE_TEST_USER||'admin');
  await page.locator('[name="password"]').fill(adminPassword());
  await page.locator('button[type="submit"]').click();
  await expect(page.locator('#projectSwitcher')).toBeVisible();
  await page.locator('#projectSwitcher').selectOption('welcome');
  await page.locator('.brand[data-view="collaboration"]').click();
  const input=page.locator('#aiAssistant .ai-compose-input');
  await expect(input).toBeEnabled();
  if(process.env.LOOPFORGE_TEST_MODEL)await expect(page.locator('#aiAssistant [data-model-label]')).toContainText(process.env.LOOPFORGE_TEST_MODEL);
  const marker=`BROWSER-LIVE-${Date.now().toString(36)}`;
  await input.fill(`这是只读的自动化测试。请通过回复工具提交一句话，包含 ${marker}。不要创建、修改或删除任何平台对象或文件。`);
  await page.locator('#aiAssistant button[type="submit"]').click();
  const reply=page.locator('#aiAssistant .ai-role-assistant').last();
  let sessionId='';
  try {
    await expect(reply).toContainText(marker,{timeout:240_000});
    await expect(reply.locator('.ai-message-actions.is-complete')).toBeVisible({timeout:30_000});
    sessionId=await page.evaluate(()=>window.AgentFlowAssistant.sessionId());
    expect(sessionId).not.toBe('');
    await page.reload();
    await expect(page.locator('#aiAssistant .ai-role-assistant').last()).toContainText(marker,{timeout:30_000});
    const saved=await page.request.get(`/api/assistant/sessions/${sessionId}`);
    expect(saved.ok()).toBe(true);
    expect((await saved.json()).messages.some(m=>m.role==='assistant'&&m.content.includes(marker))).toBe(true);
    expect(errors).toEqual([]);
  } finally {
    sessionId=sessionId||await page.evaluate(()=>window.AgentFlowAssistant.sessionId());
    if(sessionId)await page.request.delete(`/api/assistant/sessions/${sessionId}`);
  }
});
