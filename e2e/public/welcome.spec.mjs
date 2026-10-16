import {test,expect} from '@playwright/test';
import {signInUnconfigured} from './helpers.mjs';

// Default journeys use an account with no model key, joined to the welcome
// project. New self-registered accounts join it automatically. The real-model
// capability question requires explicit AGENTFLOW_E2E_MODEL=1.
// Do not record the authentication request or settings form in trace artifacts.
test.use({trace:'off',screenshot:'off'});

async function signIn(page){
  await signInUnconfigured(page);
  await page.locator('#projectSwitcher').selectOption('welcome');
  await page.goto('/#/collaboration');
  await expect(page.locator('.ai-welcome h1')).toBeVisible();
}

for(const variant of [
  {name:'desktop',width:1600,height:1000,lang:'zh',theme:'light'},
  {name:'laptop',width:1280,height:720,lang:'zh',theme:'light'},
  {name:'mobile-English-dark',width:390,height:844,lang:'en',theme:'dark'},
  {name:'small-mobile',width:320,height:700,lang:'zh',theme:'light'},
]){
  test(`welcome shows all setup steps and keeps the draft reachable: ${variant.name}`,async({page},info)=>{
    const errors=[];page.on('pageerror',e=>errors.push(e.message));
    await page.setViewportSize({width:variant.width,height:variant.height});
    await page.addInitScript(({lang,theme})=>{
      localStorage.setItem('loopforge.lang',lang);
      localStorage.setItem('loopforge.theme.v3',theme);
    },variant);
    await signIn(page);
    const input=page.locator('#aiAssistant .ai-compose-input');
    const guide=page.locator('.ai-onboarding');
    await expect(input).toBeEnabled();
    await expect(page.locator('.ai-explainer')).toHaveCount(0);
    await expect(page.locator('[data-compose-hint]')).toBeVisible();
    await expect(guide.locator('details, summary')).toHaveCount(0);
    await expect(guide.locator('li')).toHaveCount(5);
    await expect(guide.locator('.ai-onboarding-count')).toContainText('/ 4');
    await expect(guide.locator('[data-step="repo"]')).toHaveAttribute('data-optional','true');
    // Expanded onboarding must not push the first interaction down the page.
    const heading=await page.locator('.ai-welcome h1').boundingBox();
    const topbar=await page.locator('.topbar').boundingBox();
    expect(heading.y-topbar.y-topbar.height).toBeLessThanOrEqual(48);
    if(variant.width>=1280){
      const composer=await page.locator('.ai-compose-box').boundingBox();
      expect(composer.y+composer.height).toBeLessThanOrEqual(variant.height);
    }
    for(const step of ['key','project','chat','run','repo']){
      const row=guide.locator(`[data-step="${step}"]`);
      await row.scrollIntoViewIfNeeded();await expect(row).toBeInViewport();
    }
    await page.locator('.ai-compose-box').scrollIntoViewIfNeeded();
    for(const selector of ['.ai-compose-box','[data-compose-hint]']){
      const box=await page.locator(selector).boundingBox();
      expect(box.y,selector).toBeGreaterThanOrEqual(56);
      expect(box.y+box.height,selector).toBeLessThanOrEqual(variant.height);
      expect(box.x,selector).toBeGreaterThanOrEqual(0);
      expect(box.x+box.width,selector).toBeLessThanOrEqual(variant.width);
    }
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    const guideBox=await guide.boundingBox(),composeBox=await page.locator('.ai-compose-box').boundingBox();
    expect(guideBox.y+guideBox.height).toBeLessThanOrEqual(composeBox.y);
    await expect(page.locator('.ai-compose-box [data-compose-hint]')).toBeVisible();
    const warning=await page.locator('[data-compose-hint]').evaluate(el=>{
      const style=getComputedStyle(el);return {color:style.color,background:style.backgroundColor};
    });
    expect(warning.background).not.toBe('rgba(0, 0, 0, 0)');
    expect(warning.color).not.toBe(await page.locator('.ai-welcome>p').evaluate(el=>getComputedStyle(el).color));
    if(variant.lang==='en'){
      await expect(guide.locator('header')).not.toContainText(/[\u3400-\u9fff]/);
      await expect(page.locator('[data-compose-hint]')).not.toContainText(/[\u3400-\u9fff]/);
    }
    await page.screenshot({path:info.outputPath(`welcome-${variant.name}.png`)});
    await input.fill('Draft a small tool');
    await expect(page.locator('#aiAssistant button[type="submit"]')).toBeDisabled();
    // The capability question remains available after a previous chat.
    await page.evaluate(()=>{localStorage.setItem('loopforge.onboarding.chatted','1');window.AgentFlowAssistant.sync()});
    const expanded=await guide.boundingBox(),composer=await page.locator('.ai-compose-box').boundingBox();
    expect(expanded.y+expanded.height).toBeLessThanOrEqual(composer.y);
    await expect(guide.locator('[data-onboarding-step="chat"]')).toBeVisible();
    await guide.locator('[data-onboarding-step="chat"]').click();
    await expect(input).toHaveValue('Draft a small tool');
    await expect(input).toBeFocused();
    await expect(guide.locator('details, summary')).toHaveCount(0);
    await page.locator('[data-onboarding-dismiss]').click();
    await expect(guide).toHaveCount(0);
    await page.reload();
    await expect(guide).toHaveCount(0);
    await expect(input).toBeEnabled();
    expect(errors).toEqual([]);
  });
}

test('the checklist no-key demo starts a real SOP and waits for human approval',async({page})=>{
  test.setTimeout(120_000);
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await signIn(page);
  let runId='',issueId='';
  try{
    const creation=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname==='/api/issues');
    await page.locator('[data-onboarding-step="run"]').click();
    const response=await creation;
    expect(response.ok()).toBe(true);
    issueId=(await response.json()).id;
    await expect.poll(async()=>{
      const r=await page.request.get(`/api/issues/${issueId}/runs`);
      expect(r.ok()).toBe(true);
      const data=await r.json(),runs=Array.isArray(data)?data:(data?.items||[]);
      runId=runs[0]?.id||runs[0]?.runId||'';
      return runId;
    },{timeout:60_000}).not.toBe('');
    const read=async()=>{
      const response=await page.request.get(`/api/runs/${runId}`);
      expect(response.ok()).toBe(true);
      return response.json();
    };
    await expect.poll(async()=>(await read()).status,{timeout:60_000}).toBe('waiting');
    const run=await read();issueId=run.issueId;
    expect(run.nodes.prepare.status).toBe('succeeded');
    expect(run.nodes.approve.status).not.toBe('succeeded');
    expect(run.artifacts.some(a=>a.name==='welcome')).toBe(true);
    await expect(page.locator('[data-approve="approve"]').first()).toBeVisible({timeout:15_000});
    await page.evaluate(()=>window.LoopForgeTour.closeRunTour());
    await page.locator('[data-approve="approve"]').first().click();
    await expect.poll(async()=>(await read()).status,{timeout:30_000}).toBe('succeeded');
    expect(errors).toEqual([]);
  }finally{
    if(runId){
      await page.request.post(`/api/runs/${runId}/cancel`,{data:{}}).catch(()=>{});
      await page.request.delete(`/api/runs/${runId}`).catch(()=>{});
    }
    if(issueId)await page.request.delete(`/api/issues/${issueId}`).catch(()=>{});
  }
});

test('asking about capabilities without a model key opens setup and preserves the prepared question',async({page})=>{
  let generations=0;
  page.on('request',r=>{if(r.method()==='POST'&&new URL(r.url()).pathname==='/api/assistant/generations')generations++});
  await signIn(page);
  await page.locator('[data-onboarding-step="chat"]').click();
  await expect(page.locator('#userCredentialForm [name="codeBuddyApiKey"]')).toBeFocused();
  await page.locator('.brand[data-view="collaboration"]').click();
  await expect(page.locator('#aiAssistant .ai-compose-input')).toHaveValue(/LoopForge/);
  await expect(page.locator('#aiAssistant button[type="submit"]')).toBeDisabled();
  expect(generations).toBe(0);
});

test('the checklist asks a real model about capabilities with one click',async({page})=>{
  test.skip(process.env.AGENTFLOW_E2E_MODEL!=='1','Requires a configured model key and AGENTFLOW_E2E_MODEL=1');
  test.setTimeout(300_000);
  const {password}=await import('./helpers.mjs');
  await page.goto('/login.html');
  await page.locator('[name="login"]').fill(process.env.LOOPFORGE_TEST_USER||'admin');
  await page.locator('[name="password"]').fill(password());
  await page.locator('#loginSubmit').click();
  await expect(page.locator('#projectSwitcher')).toBeVisible();
  await page.locator('#projectSwitcher').selectOption('welcome');
  await page.goto('/#/collaboration');
  let sessionId='';
  try{
    await page.locator('[data-onboarding-step="chat"]').click();
    await expect(page.locator('#aiAssistant .ai-role-user').last()).toContainText('LoopForge');
    const reply=page.locator('#aiAssistant .ai-role-assistant').last();
    await expect(reply.locator('.ai-message-actions.is-complete')).toBeVisible({timeout:240_000});
    expect((await reply.innerText()).length).toBeGreaterThan(30);
    sessionId=await page.evaluate(()=>window.AgentFlowAssistant.sessionId());
    expect(sessionId).not.toBe('');
  }finally{
    sessionId=sessionId||await page.evaluate(()=>window.AgentFlowAssistant.sessionId());
    if(sessionId)await page.request.delete(`/api/assistant/sessions/${sessionId}`);
  }
});

for(const variant of [
  {name:'desktop',width:1440,height:1100,lang:'zh',theme:'light'},
  {name:'mobile-English-dark',width:390,height:844,lang:'en',theme:'dark'},
]){
  test(`repository setup explains the missing token and aligns its checkbox: ${variant.name}`,async({page},info)=>{
    await page.setViewportSize({width:variant.width,height:variant.height});
    await page.addInitScript(({lang,theme})=>{
      localStorage.setItem('loopforge.lang',lang);localStorage.setItem('loopforge.theme.v3',theme);
    },variant);
    await signIn(page);
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
      await page.locator('#projectSwitcher').selectOption(projectId);
      await expect(page.locator('.ai-onboarding')).toBeVisible();
      await page.locator('[data-onboarding-step="repo"]').click();
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
      await modal.locator('[name="repoUrl"]').fill('https://github.com/Robert0622/agentflow-e2e-0930');
      const saved=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname==='/api/repositories');
      await modal.locator('button[type="submit"]').click();
      const response=await saved;expect(response.ok()).toBe(true);repositoryId=(await response.json()).id;
      await expect.poll(async()=>(await page.request.get(`/api/projects/${projectId}`).then(r=>r.json())).defaultRepositoryId).toBe(repositoryId);
      await expect(modal.locator('#repositoryCredentialHint')).toHaveAttribute('data-configured','false');
      await modal.locator('.repository-close').first().click();
      const step=page.locator('.ai-onboarding [data-step="repo"]');
      await expect(step).not.toHaveClass(/is-done/);
      await step.locator('[data-onboarding-step="repo"]').click();
      await expect(page.locator('#userCredentialForm [name="gitToken"]')).toBeFocused();
      // The same direct setup link also works inside the repository modal.
      await page.goto('/#/collaboration');
      await page.evaluate(()=>openRepositoryModal());
      await page.locator('[data-repository-configure-token]').click();
      await expect(page.locator('#repositoryModal')).not.toHaveClass(/open/);
      await expect(page.locator('#userCredentialForm [name="gitToken"]')).toBeFocused();
    }finally{
      const restore=await page.request.put('/api/projects',{data:project});expect(restore.ok()).toBe(true);
      if(repositoryId){const deleted=await page.request.delete(`/api/repositories?id=${encodeURIComponent(repositoryId)}`);expect(deleted.ok()).toBe(true)}
    }
  });
}
