import {test,expect} from '@playwright/test';
import {signInUnconfigured, appReady, switchProject, chat, openHome, fitsFrame} from './helpers.mjs';

// Default journeys use an account with no model key, joined to the welcome
// project. New self-registered accounts join it automatically. The real-model
// question requires explicit AGENTFLOW_E2E_MODEL=1.
// Do not record the authentication request or settings form in trace artifacts.
test.use({trace:'off',screenshot:'off'});

for(const variant of [
  {name:'desktop',width:1600,height:1000,lang:'zh',theme:'light'},
  {name:'laptop',width:1280,height:720,lang:'zh',theme:'light'},
  {name:'mobile-English-dark',width:390,height:844,lang:'en',theme:'dark'},
  {name:'small-mobile',width:320,height:700,lang:'zh',theme:'light'},
]){
  test(`home explains the missing key, offers starters and keeps the composer reachable: ${variant.name}`,async({page},info)=>{
    const errors=[];page.on('pageerror',e=>errors.push(e.message));
    await page.setViewportSize({width:variant.width,height:variant.height});
    await page.addInitScript(({lang,theme})=>{
      localStorage.setItem('loopforge.lang',lang);
      localStorage.setItem('agentflow.theme.v3',theme);
    },variant);
    await signInUnconfigured(page);
    const input=await openHome(page);
    const frame=chat(page);
    await expect(page.locator('html')).toHaveAttribute('data-theme',variant.theme);
    await expect(frame.locator('html')).toHaveAttribute('data-theme',variant.theme);
    const notice=frame.locator('.credential-notice');
    await expect(notice).toBeVisible();
    await expect(notice.getByRole('button')).toBeVisible();
    const box=frame.locator('.composer-column');
    await box.scrollIntoViewIfNeeded();
    await expect.poll(()=>fitsFrame(box)).toBe(true);
    await expect.poll(()=>fitsFrame(notice)).toBe(true);
    // Every composer control, the send button last, stays on screen at phone widths.
    for(const control of await frame.locator('.composer-toolbar > :not(input):not(.toolbar-spacer)').all())await expect.poll(()=>fitsFrame(control)).toBe(true);
    // A first conversation is personal until a project is picked.
    await expect(frame.locator('.project-picker')).toHaveClass(/is-unset/);
    const starters=frame.locator('.home-starter');
    await expect(starters).toHaveCount(4);
    for(let i=0;i<4;i++){await starters.nth(i).scrollIntoViewIfNeeded();await expect(starters.nth(i)).toBeInViewport()}
    expect(await frame.locator('html').evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    await page.screenshot({path:info.outputPath(`home-${variant.name}.png`)});
    await input.fill('Draft a small tool');
    await page.reload();
    await expect(composer()).toHaveValue('Draft a small tool');
    await composer().fill('');
    expect(errors).toEqual([]);
    function composer(){return chat(page).locator('.composer-column textarea')}
  });
}

test('the welcome SOP needs no key, runs a real script and waits for approval in the run view',async({page})=>{
  test.setTimeout(120_000);
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await signInUnconfigured(page);
  await openHome(page);
  await expect(chat(page).locator('.session-list [data-session-id^="local-preview-"]')).toHaveCount(0);
  let runId='';
  try{
    const pending=page.waitForResponse(response=>response.request().method()==='POST'&&new URL(response.url()).pathname==='/api/runs');
    await chat(page).getByRole('button',{name:/一键体验/}).click();
    const created=await pending;
    expect(created.ok()).toBe(true);runId=(await created.json()).id;
    const read=async()=>{const r=await page.request.get(`/api/runs/${runId}`);expect(r.ok()).toBe(true);return r.json()};
    await expect.poll(async()=>(await read()).status,{timeout:30_000}).toBe('waiting');
    const run=await read();
    expect(run.nodes.prepare.status).toBe('succeeded');
    expect(run.nodes.approve.status).not.toBe('succeeded');
    expect(run.artifacts.some(a=>a.name==='welcome')).toBe(true);
    await page.goto(`/#/runs/${encodeURIComponent(runId)}`);
    await page.locator('[data-run-node="approve"]').click();
    const approve=page.locator('[data-approve="approve"]').first();
    await expect(approve).toBeVisible({timeout:15_000});
    await approve.click();
    await expect.poll(async()=>(await read()).status,{timeout:30_000}).toBe('succeeded');
    expect(errors).toEqual([]);
  }finally{
    if(runId){
      await page.request.post(`/api/runs/${runId}/cancel`,{data:{}}).catch(()=>{});
      await page.request.delete(`/api/runs/${runId}`).catch(()=>{});
    }
  }
});

test('home starters prepare a question or open quick access without calling a model',async({page})=>{
  let generations=0;
  page.on('request',r=>{if(r.method()==='POST'&&new URL(r.url()).pathname==='/api/assistant/generations')generations++});
  await signInUnconfigured(page);
  const input=await openHome(page);
  const frame=chat(page);
  await frame.locator('.home-starter').filter({hasText:'选用 SOP 与角色'}).click();
  await expect(input).toHaveValue(/SOP/);
  await expect(input).toBeFocused();
  await input.fill('');
  await frame.locator('.home-starter').filter({hasText:'快速接入'}).click();
  const wizard=frame.getByRole('dialog',{name:'快速接入'});
  await expect(wizard).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(wizard).toBeHidden();
  expect(generations).toBe(0);
});

test('sending without a model key is refused before any request, with a way to configure it',async({page})=>{
  let writes=0;
  page.on('request',r=>{if(r.method()!=='GET'&&new URL(r.url()).pathname.startsWith('/api/assistant/'))writes++});
  await signInUnconfigured(page);
  const input=await openHome(page);
  const frame=chat(page);
  const text='No key yet: this must be refused before any model runs.';
  await input.fill(text);
  await frame.locator('.send-button').click();
  const configure=frame.getByRole('button',{name:'去配置 CodeBuddy API Key'});
  await expect(configure).toBeVisible();
  await expect(input).toHaveValue(text);
  expect(writes).toBe(0);
  await configure.click();
  await expect(page).toHaveURL(/#\/settings/);
  await expect(page.locator('#credentialWizard .credential-pane:not(.is-leaving) input[name="secret"]')).toBeFocused();
  await page.keyboard.press('Escape');
});

test('the home composer asks a real model with one click',async({page})=>{
  test.skip(process.env.AGENTFLOW_E2E_MODEL!=='1','Requires a configured model key and AGENTFLOW_E2E_MODEL=1');
  test.setTimeout(300_000);
  const {password}=await import('./helpers.mjs');
  await page.goto('/login.html');
  await page.locator('[name="login"]').fill(process.env.LOOPFORGE_TEST_USER||'admin');
  await page.locator('[name="password"]').fill(password());
  await page.locator('#loginSubmit').click();
  await appReady(page);
  await switchProject(page,'welcome');
  const input=await openHome(page);
  const frame=chat(page);
  const sessionId=()=>new URL(page.frames().find(f=>f.url().includes('/chatbot/'))?.url()||'http://x/').searchParams.get('session')||'';
  try{
    await frame.locator('.home-starter').filter({hasText:'选用 SOP 与角色'}).click();
    await input.press('End');await input.pressSequentially('只介绍，不要创建或修改任何数据。');
    await frame.locator('.send-button').click();
    await expect(frame.locator('.message.user').last()).toContainText('SOP');
    const reply=frame.locator('.message.assistant').last();
    await expect.poll(async()=>(await reply.innerText()).length,{timeout:240_000}).toBeGreaterThan(30);
    await expect.poll(sessionId).not.toBe('');
  }finally{
    if(sessionId())await page.request.delete(`/api/assistant/sessions/${sessionId()}`);
  }
});

for(const variant of [
  {name:'desktop',width:1440,height:1100,lang:'zh',theme:'light'},
  {name:'mobile-English-dark',width:390,height:844,lang:'en',theme:'dark'},
]){
  test(`repository setup explains the missing token and aligns its checkbox: ${variant.name}`,async({page},info)=>{
    await page.setViewportSize({width:variant.width,height:variant.height});
    await page.addInitScript(({lang,theme})=>{
      localStorage.setItem('loopforge.lang',lang);localStorage.setItem('agentflow.theme.v3',theme);
    },variant);
    await signInUnconfigured(page);
    const identity=(await page.request.get('/api/auth/current').then(r=>r.json())).user;
    // Projects have no delete endpoint. Reuse one dedicated acceptance fixture;
    // every repository created below is removed and its settings are restored.
    const projectId='e2e-onboarding';
    let original=await page.request.get(`/api/projects/${projectId}`);
    let project;
    if(original.status()===404){
      const created=await page.request.post('/api/projects',{data:{id:projectId,key:'GUIDE',name:'E2E onboarding',ownerId:identity.id}});
      expect(created.ok()).toBe(true);project=await created.json();
    }else{expect(original.ok()).toBe(true);project=await original.json()}
    expect(project.ownerId).toBe(identity.id);
    let repositoryId='';
    try{
      await page.reload();
      await switchProject(page,projectId);
      await page.locator('.nav[data-view="issues"]').click();
      await page.evaluate(()=>openRepositoryModal());
      const modal=page.locator('#repositoryModal');
      await expect(modal).toHaveClass(/open/);
      const checkbox=modal.locator('[name="makeDefault"]');
      const label=modal.locator('label.toggle-row span');
      const cb=await checkbox.boundingBox(),text=await label.boundingBox();
      expect(cb.width).toBeLessThanOrEqual(20);
      expect(Math.abs(cb.y+cb.height/2-text.y-text.height/2)).toBeLessThan(3);
      expect(text.x-cb.x-cb.width).toBeLessThanOrEqual(12);
      await checkbox.uncheck();await label.click();await expect(checkbox).toBeChecked();
      await expect(modal.locator('#repositoryCredentialHint')).toHaveAttribute('data-configured','false');
      if(variant.lang==='en')await expect(modal.locator('#repositoryCredentialHint')).not.toContainText(/[\u3400-\u9fff]/);
      await page.screenshot({path:info.outputPath(`repository-${variant.name}.png`)});
      await modal.locator('[name="repoUrl"]').fill('https://github.com/example/loopforge-e2e-fixture');
      const saved=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname==='/api/repositories');
      await modal.locator('button[type="submit"]').click();
      const response=await saved;expect(response.ok()).toBe(true);repositoryId=(await response.json()).id;
      await expect.poll(async()=>(await page.request.get(`/api/projects/${projectId}`).then(r=>r.json())).defaultRepositoryId).toBe(repositoryId);
      await expect(modal.locator('#repositoryCredentialHint')).toHaveAttribute('data-configured','false');
      // The token link leaves the modal for the GitHub token step of the credential wizard.
      await modal.locator('[data-repository-configure-token]').click();
      await expect(modal).not.toHaveClass(/open/);
      await expect(page).toHaveURL(/#\/settings/);
      const wizard=page.locator('#credentialWizard');
      await expect(wizard.getByRole('tab',{name:'GitHub 授权'})).toHaveAttribute('aria-selected','true');
      await expect(wizard.locator('input[name="secret"]')).toHaveCount(0);
      await wizard.getByRole('tab',{name:'访问令牌'}).click();
      await expect(wizard.locator('.credential-pane:not(.is-leaving) input[name="secret"]')).toBeFocused();
      await expect(wizard).toContainText('GitHub');
      await page.keyboard.press('Escape');
    }finally{
      const restore=await page.request.put('/api/projects',{data:project});expect(restore.ok()).toBe(true);
      if(repositoryId){const deleted=await page.request.delete(`/api/repositories?id=${encodeURIComponent(repositoryId)}`);expect(deleted.ok()).toBe(true)}
    }
  });
}
