import {test,expect} from '@playwright/test';
import {password as adminPassword, signInUnconfigured, appReady, switchProject, chat, openHome} from './helpers.mjs';

// Credential UI must never enter a Playwright trace or screenshot artifact.
test.use({trace:'off',screenshot:'off'});

test('a user without model credentials can draft, configure credentials and return without losing text',async({page})=>{
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await signInUnconfigured(page);
  const current=await page.request.get('/api/auth/current').then(r=>r.json());
  const path=`/api/users/${current.user.id}/credentials`;
  // This is an inert value for a UI state transition. No model request is sent.
  const marker='e2e-placeholder-credential-not-for-model-use';
  const draft='保留这份尚未发送的需求草稿';
  try {
    let input=await openHome(page);
    await input.fill(draft);
    const notice=chat(page).locator('.credential-notice');
    await expect(notice).toBeVisible();
    await expect(notice).toContainText('CodeBuddy API Key');
    await notice.getByRole('button',{name:'去配置'}).click();
    await expect(page).toHaveURL(/#\/settings/);
    const wizard=page.locator('#credentialWizard');
    const pane=wizard.locator('.credential-pane:not(.is-leaving)');
    const next=wizard.locator('[data-wizard-next]');
    await expect(wizard.locator('[data-wizard-step]')).toHaveText('2 / 3');
    await expect(pane.locator('input[name="secret"]')).toBeFocused();
    await expect(next).toBeDisabled();
    await pane.locator('input[name="secret"]').fill(marker);
    await expect(next).toBeEnabled();
    await next.click();
    await expect(wizard.locator('[data-wizard-step]')).toHaveText('3 / 3');
    await expect(pane).not.toContainText(marker);
    await next.click();
    await expect(wizard.locator('[data-wizard-done]')).toBeVisible({timeout:20_000});
    const users=await page.request.get('/api/users').then(r=>r.json());
    const saved=users.find(u=>u.id===current.user.id);
    expect(saved.codeBuddyApiKeyConfigured).toBe(true);
    expect(JSON.stringify(saved)).not.toContain(marker);
    await wizard.locator('[data-wizard-done]').click();
    input=await openHome(page);
    await expect(input).toHaveValue(draft);
    await expect(chat(page).locator('.credential-notice')).toHaveCount(0);
    expect(errors).toEqual([]);
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
  await appReady(page);
  await switchProject(page,'welcome');
  const input=await openHome(page);
  const marker=`BROWSER-LIVE-${Date.now().toString(36)}`;
  await input.fill(`这是只读的自动化测试。请回复一句话，包含 ${marker}。不要创建、修改或删除任何平台对象或文件。`);
  await chat(page).locator('.send-button').click();
  const reply=chat(page).locator('.message.assistant').last();
  const sessionId=()=>new URL(page.frames().find(f=>f.url().includes('/chatbot/'))?.url()||'http://x/').searchParams.get('session')||'';
  try {
    await expect(reply).toContainText(marker,{timeout:240_000});
    await expect.poll(sessionId).not.toBe('');
    await page.reload();
    await expect(chat(page).locator('.message.assistant').last()).toContainText(marker,{timeout:30_000});
    const saved=await page.request.get(`/api/assistant/sessions/${sessionId()}`);
    expect(saved.ok()).toBe(true);
    expect((await saved.json()).messages.some(m=>m.role==='assistant'&&m.content.includes(marker))).toBe(true);
    expect(errors).toEqual([]);
  } finally {
    if(sessionId())await page.request.delete(`/api/assistant/sessions/${sessionId()}`);
  }
});
