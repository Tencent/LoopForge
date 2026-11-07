import {test,expect} from '@playwright/test';
import {signInUnconfigured, switchProject, chat, openHome, fitsFrame} from './helpers.mjs';

test.use({trace:'off',screenshot:'off'});

for(const viewport of [{width:1280,height:400},{width:390,height:600}]){
  test(`the executor and model menus stay usable at ${viewport.width}x${viewport.height}`,async({page})=>{
    const errors=[];page.on('pageerror',e=>errors.push(e.message));
    // appReady may reload a stalled first load; only the document under test counts.
    page.on('request',r=>{if(r.isNavigationRequest()&&r.frame()===page.mainFrame())errors.length=0});
    await page.setViewportSize(viewport);
    await signInUnconfigured(page);
    await openHome(page);
    const frame=chat(page);
    for(const [name,menu] of [['执行器','.permission-menu[role="menu"]'],['选择模型','.grasp-model-menu']]){
      await frame.getByRole('button',{name,exact:true}).click();
      const open=frame.locator(menu);
      await expect(open).toBeVisible();
      await expect.poll(()=>fitsFrame(open),{message:`${name} menu at ${viewport.width}x${viewport.height}`}).toBe(true);
      await page.keyboard.press('Escape');
      await expect(open).toBeHidden();
    }
    // Long catalogs are searched inside the menu.
    await frame.getByRole('button',{name:'选择模型'}).click();
    const modelMenu=frame.locator('.grasp-model-menu');
    const search=modelMenu.locator('input[aria-label="搜索模型"]');
    await expect(search).toBeVisible();
    const first=(await modelMenu.locator('[role^="menuitem"]').first().innerText()).split('\n')[0].trim();
    await search.fill('zz-no-such-model');
    await expect(modelMenu).not.toContainText(first);
    await search.fill(first);
    await expect(modelMenu).toContainText(first);
    await page.keyboard.press('Escape');
    expect(errors).toEqual([]);
  });
}

test('executor selection has independent models, credentials and a persistent choice',async({page})=>{
  await signInUnconfigured(page);
  const draft='Keep this draft while choosing an executor';
  const input=await openHome(page);
  const frame=chat(page);
  const executor=frame.getByRole('button',{name:'执行器',exact:true});
  const choose=async label=>{
    await executor.click();
    await frame.locator('.permission-menu [role="menuitemradio"]').filter({hasText:label}).click();
    await expect(executor).toContainText(label);
  };
  await executor.click();
  await expect(frame.locator('.permission-menu [role="menuitemradio"]')).toHaveCount(3);
  await page.keyboard.press('Escape');
  await input.fill(draft);
  for(const [id,label] of [['codex','Codex'],['claude','Claude Code'],['codebuddy','CodeBuddy']]){
    await choose(label);
    await expect(input).toHaveValue(draft);
    // Only CodeBuddy has the inline key reminder; every executor is still checked server-side.
    await expect(frame.locator('.credential-notice')).toHaveCount(id==='codebuddy'?1:0);
    const response=await page.request.post('/api/assistant/generations',{data:{executor:id,model:'default',projectId:'welcome',messages:[{role:'user',content:'No key: reject before executing.'}]}});
    expect(response.status()).toBe(400);expect((await response.json()).error).toContain(label);
    const models=await page.request.get('/api/assistant/models?executor='+id);
    expect(models.ok()).toBe(true);expect((await models.json()).models.length).toBeGreaterThan(0);
  }
  await choose('Codex');
  await page.reload();
  await expect(composer()).toBeEditable();
  await expect(chat(page).getByRole('button',{name:'执行器',exact:true})).toContainText('Codex');
  await choose('CodeBuddy');
  expect((await page.request.get('/api/assistant/models?executor=shell')).status()).toBe(400);
  function composer(){return chat(page).locator('.composer-column textarea')}
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
      expect((await page.request.put('/api/agents',{data:{id,projectId,name:id,executorId:executor,prompt:'Only perform an explicitly assigned task.'}})).ok()).toBe(true);
      expect((await page.request.put('/api/workflows',{data:{id,projectId,name:id,status:'draft',sandboxProfileId:'local',nodes:[{id:'work',name:'Work',type:'agent',agentId:id,executorId:'codebuddy',position:{x:100,y:100}}],edges:[]}})).ok()).toBe(true);
    }
    await page.reload();await switchProject(page,projectId);
    await page.locator('.nav[data-view="sops"]').click();
    const expectRows=async configured=>{
      for(const [i,label] of ['CodeBuddy','Codex','Claude Code'].entries()){
        const chips=page.locator(`[data-sop="${ids[i]}"] .sop-requirements .sop-badge`);
        const row=chips.filter({hasText:new RegExp(`(^|\\s)${label} API Key`)});
        await expect(row).toHaveCount(1);
        await expect(row).toHaveClass(new RegExp(configured&&i===1?'\\btone-success\\b':'\\btone-warn\\b'));
        // A role on one executor never asks for another executor's key.
        for(const other of ['CodeBuddy','Codex','Claude Code'].filter(x=>x!==label))await expect(chips.filter({hasText:new RegExp(`(^|\\s)${other} API Key`)})).toHaveCount(0);
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
