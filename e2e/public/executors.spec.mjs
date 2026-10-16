import {test,expect} from '@playwright/test';
import {signInUnconfigured} from './helpers.mjs';

test.use({trace:'off',screenshot:'off'});

test('the model menu remains usable in a short window and on mobile after dismissing setup',async({page})=>{
  await signInUnconfigured(page);
  await page.locator('#projectSwitcher').selectOption('welcome');
  await page.evaluate(()=>localStorage.setItem('loopforge.onboarding.dismissed','1'));
  await page.reload();
  await page.locator('[data-executor-select]').selectOption('codex');
  for(const viewport of [{width:1280,height:400},{width:390,height:600}]){
    await page.setViewportSize(viewport);
    await page.locator('#aiAssistant [data-model-toggle]').click();
    const menu=page.locator('#aiAssistant .ai-compose-model .ai-model-menu');
    await expect(menu).toBeVisible();
    const visible=await menu.evaluate(el=>{const r=el.getBoundingClientRect(),header=document.querySelector('.topbar').getBoundingClientRect();return r.top>=header.bottom&&r.bottom<=innerHeight&&r.left>=0&&r.right<=innerWidth});
    expect(visible).toBe(true);
    const custom=menu.getByRole('textbox',{name:'自定义模型 ID'});
    await custom.scrollIntoViewIfNeeded();await custom.fill('viewport-test-model');
    await menu.locator('.ai-custom-model button').click();
    await expect(page.locator('#aiAssistant [data-model-label]')).toHaveText('viewport-test-model');
  }
});

test('executor selection has independent models, credentials and a persistent choice',async({page})=>{
  await signInUnconfigured(page);
  await page.locator('#projectSwitcher').selectOption('welcome');
  const input=page.locator('#aiAssistant .ai-compose-input');
  const selector=page.locator('[data-executor-select]');
  await expect(selector.locator('option')).toHaveCount(3);
  await input.fill('Keep this draft while choosing an executor');
  for(const [executor,field,label] of [['codex','codexApiKey','Codex'],['claude','claudeCodeApiKey','Claude Code'],['codebuddy','codeBuddyApiKey','CodeBuddy']]){
    await selector.selectOption(executor);
    await expect(input).toHaveValue('Keep this draft while choosing an executor');
    await expect(page.locator('[data-compose-hint]')).toBeVisible();
    await expect(page.locator('#aiAssistant button[type="submit"]')).toBeDisabled();
    const response=await page.request.post('/api/assistant/generations',{data:{executor,model:'default',projectId:'welcome',messages:[{role:'user',content:'No key: reject before executing.'}]}});
    expect(response.status()).toBe(400);expect((await response.json()).error).toContain(label);
    const catalog=await page.request.get('/api/assistant/models?executor='+executor);
    expect(catalog.ok()).toBe(true);expect((await catalog.json()).models.length).toBeGreaterThan(0);
    await page.locator('[data-configure-credentials]').click();
    await expect(page.locator(`#userCredentialForm [name="${field}"]`)).toBeFocused();
    await page.locator('.brand[data-view="collaboration"]').click();
    await expect(input).toHaveValue('Keep this draft while choosing an executor');
  }
  await selector.selectOption('codex');
  await page.reload();await expect(selector).toHaveValue('codex');
  await page.locator('#aiAssistant [data-model-toggle]').click();
  await page.getByRole('textbox',{name:'自定义模型 ID'}).fill('my-custom-gateway-model');
  await page.locator('.ai-custom-model button').click();
  await expect(page.locator('#aiAssistant [data-model-label]')).toHaveText('my-custom-gateway-model');
  await selector.selectOption('codebuddy');
  expect((await page.request.get('/api/assistant/models?executor=shell')).status()).toBe(400);
});

test('SOP readiness checks each role executor rather than always requiring CodeBuddy',async({page})=>{
  await signInUnconfigured(page);
  const {user}=await page.request.get('/api/auth/current').then(r=>r.json());
  expect(!!user.codexApiKeyConfigured).toBe(false);
  const projectId='e2e-executors',suffix=Date.now().toString(36),ids=[];
  const project=await page.request.get('/api/projects/'+projectId);
  if(project.status()===404)expect((await page.request.post('/api/projects',{data:{id:projectId,key:'E2EX',name:'E2E executor setup',ownerId:user.id}})).ok()).toBe(true);
  else expect((await project.json()).ownerId).toBe(user.id);
  try{
    for(const executor of ['codebuddy','codex','claude']){
      const id=`e2e-provider-${executor}-${suffix}`;ids.push(id);
      expect((await page.request.put('/api/agents',{data:{id,projectId,name:id,executorId:executor,instructions:'Only perform an explicitly assigned task.'}})).ok()).toBe(true);
      expect((await page.request.put('/api/workflows',{data:{id,projectId,name:id,status:'draft',sandboxProfileId:'local',nodes:[{id:'work',name:'Work',type:'agent',agentId:id,executorId:'codebuddy',position:{x:100,y:100}}],edges:[]}})).ok()).toBe(true);
    }
    await page.reload();await page.locator('#projectSwitcher').selectOption(projectId);
    await page.locator('.nav[data-view="sops"]').click();
    const expectRows=async configured=>{
      for(const [i,label] of ['CodeBuddy','Codex','Claude Code'].entries()){
        const row=page.locator(`[data-sop="${ids[i]}"] .sop-requirements .req-chip`).filter({hasText:label+' API Key'});
        await expect(row).toHaveCount(1);
        await expect(row).toHaveClass(new RegExp(configured&&i===1?'\\bok\\b':'\\bmissing\\b'));
      }
    };
    await expectRows(false);
    expect((await page.request.put(`/api/users/${user.id}/credentials`,{data:{codexApiKey:'e2e-inert-key-for-readiness-only'}})).ok()).toBe(true);
    await page.reload();await expectRows(true);
  }finally{
    expect((await page.request.put(`/api/users/${user.id}/credentials`,{data:{codexApiKeyClear:true}})).ok()).toBe(true);
    for(const id of ids){await page.request.delete('/api/workflows/'+id);await page.request.delete('/api/agents/'+id)}
  }
});
