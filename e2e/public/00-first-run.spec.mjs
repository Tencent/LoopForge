import {test,expect} from '@playwright/test';
import fs from 'node:fs';
import {password, appReady, chat, composer, openHome} from './helpers.mjs';

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
  await appReady(page);
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
  await appReady(page);
  await expect(page.locator('#projectSwitcher option[value="welcome"]')).toHaveCount(1);
  const input=await openHome(page);
  const frame=chat(page);
  await expect(frame.locator('.home-starter')).toHaveCount(3);
  const notice=frame.locator('.credential-notice');
  await expect(notice).toContainText('CodeBuddy API Key');
  await input.fill('First question after installing');
  await notice.getByRole('button',{name:'去配置'}).click();
  await expect(page.locator('#credentialWizard .credential-pane:not(.is-leaving) input[name="secret"]')).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(await openHome(page)).toHaveValue('First question after installing');
  await composer(page).fill('');
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
  const welcome=await page.request.get('/api/projects/welcome').then(r=>r.json());
  expect(welcome.members.some(m=>m.userId===current.user.id)).toBe(true);
  await openHome(page);
  await expect(chat(page).locator('.credential-notice')).toBeVisible();
  const again=await request.post('/api/auth/register',{headers:{Origin:new URL(page.url()).origin},data:{login:member,password:memberPassword()}});
  expect(again.status()).toBe(409);
});
