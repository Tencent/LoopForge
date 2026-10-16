import {test,expect} from '@playwright/test';
import fs from 'node:fs';
import {password} from './helpers.mjs';

// A brand-new installation, seen by someone who has never used LoopForge:
// first visit creates the administrator, the home page explains what to do
// next, and a teammate can sign up on their own. Runs only against an empty
// database because it consumes the one-time administrator setup.
test.describe.configure({mode:'serial'});
test.skip(process.env.LOOPFORGE_FRESH_INSTALL!=='1','Set LOOPFORGE_FRESH_INSTALL=1 against an installation with no accounts');
test.use({trace:'off',screenshot:'off'});

const admin=process.env.LOOPFORGE_TEST_USER||'admin';
const member=process.env.LOOPFORGE_UNCONFIGURED_USER||'first-run-member';
const memberPassword=()=>process.env.LOOPFORGE_UNCONFIGURED_PASSWORD||(process.env.LOOPFORGE_UNCONFIGURED_PASSWORD_FILE?fs.readFileSync(process.env.LOOPFORGE_UNCONFIGURED_PASSWORD_FILE,'utf8').trim():'first-run-member-password');

async function createAccount(page,login,secret){
  await page.locator('[name="login"]').fill(login);
  await page.locator('[name="password"]').fill(secret);
  await page.locator('[name="confirm"]').fill(secret);
  await page.locator('#loginSubmit').click();
  await expect(page.locator('#projectSwitcher')).toBeVisible();
}

test('the first visitor creates the administrator from the browser',async({page,request},info)=>{
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  expect(await request.get('/api/auth/setup').then(r=>r.json())).toEqual({needsSetup:true,signupEnabled:true});
  await page.goto('/');
  await expect(page).toHaveURL(/login\.html/);
  await expect(page.locator('#setupNotice')).toBeVisible();
  await expect(page.locator('#loginSwitch')).toBeHidden();
  await page.locator('[name="login"]').fill(admin);
  await page.locator('[name="password"]').fill(password());
  await page.locator('[name="confirm"]').fill(password()+'x');
  await page.locator('#loginSubmit').click();
  await expect(page.locator('#loginError')).toHaveText('两次输入的密码不一致');
  await createAccount(page,admin,password());
  await page.screenshot({path:info.outputPath('first-run-home.png')});
  const current=await page.request.get('/api/auth/current').then(r=>r.json());
  expect(current.user.loginName).toBe(admin);
  expect(await page.request.get('/api/platform/admins').then(r=>r.status())).toBe(200);
  expect((await request.get('/api/auth/setup').then(r=>r.json())).needsSetup).toBe(false);
  expect(errors).toEqual([]);
});

test('the home page guides a new account and explains the missing model key',async({page})=>{
  const errors=[],failed=[];
  page.on('pageerror',e=>errors.push(e.message));
  page.on('response',r=>{if(r.status()>=500)failed.push(r.status()+' '+r.url())});
  await page.goto('/login.html');
  await page.locator('[name="login"]').fill(admin);
  await page.locator('[name="password"]').fill(password());
  await page.locator('#loginSubmit').click();
  await expect(page.locator('#projectSwitcher')).toBeVisible();
  await page.goto('/#/collaboration');
  await page.evaluate(()=>localStorage.removeItem('loopforge.onboarding.dismissed'));
  await page.reload();
  const guide=page.locator('.ai-onboarding');
  await expect(guide).toBeVisible();
  await expect(guide.locator('li[data-step="key"]')).toHaveClass(/is-next/);
  await expect(guide.locator('li[data-step="project"]')).toHaveClass(/is-done/);
  await expect(page.locator('#projectSwitcher option[value="welcome"]')).toHaveCount(1);
  await expect(page.locator('[data-compose-hint]')).toBeVisible();
  await expect(page.locator('#aiAssistant button[type="submit"]')).toBeDisabled();
  await guide.locator('[data-onboarding-step="chat"]').click();
  await expect(page.locator('#aiAssistant .ai-compose-input')).not.toHaveValue('');
  await expect(page.locator('#userCredentialForm [name="codeBuddyApiKey"]')).toBeFocused();
  await page.goto('/#/collaboration');
  await page.locator('[data-onboarding-dismiss]').click();
  await expect(guide).toHaveCount(0);
  await page.reload();
  await expect(page.locator('.ai-welcome')).toBeVisible();
  await expect(guide).toHaveCount(0);
  expect(errors).toEqual([]);
  expect(failed).toEqual([]);
});

test('a teammate signs up without administrator rights',async({page,request})=>{
  await page.goto('/login.html');
  await expect(page.locator('#loginSwitch')).toBeVisible();
  await page.locator('#switchMode').click();
  await createAccount(page,member,memberPassword());
  const current=await page.request.get('/api/auth/current').then(r=>r.json());
  expect(current.user.loginName).toBe(member);
  expect(await page.request.get('/api/platform/admins').then(r=>r.status())).toBe(403);
  await expect(page.locator('.ai-onboarding')).toBeVisible();
  await expect(page.locator('.ai-onboarding li[data-step="project"]')).toHaveClass(/is-done/);
  const again=await request.post('/api/auth/register',{headers:{Origin:new URL(page.url()).origin},data:{login:member,password:memberPassword()}});
  expect(again.status()).toBe(409);
});
