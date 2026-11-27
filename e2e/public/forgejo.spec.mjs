import fs from 'node:fs';
import {test,expect} from '@playwright/test';
import {appReady, openSettingsPage, signInUnconfigured} from './helpers.mjs';

const webURL=process.env.LOOPFORGE_TEST_FORGEJO_WEB_URL;
const sshURL=process.env.LOOPFORGE_TEST_FORGEJO_SSH_URL;
const tokenFile=process.env.LOOPFORGE_TEST_FORGEJO_TOKEN_FILE;
test.skip(!webURL||!sshURL||!tokenFile,'Provide a disposable Forgejo repository and an identity/PR token file');
test.use({trace:'off',screenshot:'off',actionTimeout:8_000});

test('binds a Forgejo repository and saves only personal token metadata',async({page})=>{
  await signInUnconfigured(page);
  const current=await page.request.get('/api/auth/current').then(r=>r.json());
  const endpoint=`/api/users/${current.user.id}/forge-tokens`;
  const metadata=await page.request.get(endpoint).then(r=>r.json());
  const instance=metadata.instances.find(root=>webURL.startsWith(root+'/'));
  expect(Boolean(instance)).toBe(true);
  expect(metadata.tokens.some(token=>token.instance===instance)).toBe(false);

  const projectId='e2e-forgejo';
  const existing=await page.request.get(`/api/projects/${projectId}`);
  if(!existing.ok()){
    const created=await page.request.post('/api/projects',{data:{id:projectId,key:'FORGE',name:'Forgejo acceptance'}});
    expect(created.ok()).toBe(true);
    await page.reload();
  }
  let repository;
  try{
    await openSettingsPage(page,'projects');
    await page.locator(`[data-project-open="${projectId}"]`).click();
    await page.locator('[data-pa-tab="repos"]').click();
    await page.locator('[data-repo-add]').first().click();
    const drawer=page.locator('#repoDrawer');
    await expect(drawer).toBeVisible();
    await drawer.locator('[name="provider"]').selectOption('forgejo');
    await drawer.locator('[name="repoUrl"]').fill(sshURL);
    await drawer.locator('[name="webUrl"]').fill(webURL);
    const saving=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/repositories'&&r.request().method()==='POST');
    await drawer.locator('#repoDrawerSave').click();
    const saved=await saving;
    expect(saved.ok()).toBe(true);
    repository=await saved.json();
    expect(repository.provider).toBe('forgejo');
    await expect(drawer).not.toHaveClass(/\bopen\b/);
    await expect(page.locator(`[data-repo-card="${repository.id}"] a`)).toHaveAttribute('href',webURL);

    await page.goto('/#/settings');
    await appReady(page);
    const add=page.locator('#credentialPanel').getByRole('button',{name:'新增 Git 配置',exact:true});
    await add.click();
    await page.getByRole('listbox',{name:'新增 Git 配置',exact:true}).getByRole('option',{name:'Forgejo',exact:true}).click();
    const dialog=page.locator('#forgeTokenDialog');
    await dialog.locator('[name="instance"]').selectOption(instance);
    const input=dialog.locator('[name="token"]');
    const credential=fs.readFileSync(tokenFile,'utf8').trim();
    const checking=page.waitForResponse(r=>new URL(r.url()).pathname===endpoint&&r.request().method()==='PUT');
    await input.fill(credential);
    await dialog.getByRole('button',{name:'校验并保存'}).click();
    const verified=await checking;
    await input.fill('');
    expect(verified.ok()).toBe(true);
    expect((await verified.text()).includes(credential)).toBe(false);
    await expect(dialog.locator('[data-forge-rotate]')).toHaveCount(1);
    await dialog.getByRole('button',{name:'关闭'}).click();
    await page.reload();
    await expect(page.locator('[data-credential-row="forgeToken"]')).toHaveClass(/is-configured/);
  }finally{
    const saved=await page.request.get(endpoint).then(r=>r.json());
    for(const token of saved.tokens||[]){
      if(token.instance===instance)await page.request.delete(`${endpoint}/${encodeURIComponent(token.id)}`);
    }
    if(repository)await page.request.delete(`/api/repositories?id=${encodeURIComponent(repository.id)}`);
  }
});
