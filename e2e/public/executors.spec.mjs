import {test,expect} from '@playwright/test';
import {signInUnconfigured, switchProject, chat, openHome, fitsFrame, openSettingsPage} from './helpers.mjs';

test.use({trace:'off',screenshot:'off'});

// The published image labels the picker 执行器. The source build labels it CLI.
const executorButton = (frame) => frame.getByRole('button', { name: /^(执行器|CLI)$/ });
const testKey='e2e-inert-executor-key-for-menu-tests';
const putCredentials=async(page,path,data)=>expect((await page.request.put(path,{data})).ok()).toBe(true);

async function emptyCredentialsPath(page){
  const {user}=await page.request.get('/api/auth/current').then(r=>r.json());
  expect(!!user.codeBuddyApiKeyConfigured).toBe(false);
  expect(!!user.codexApiKeyConfigured).toBe(false);
  expect(!!user.claudeCodeApiKeyConfigured).toBe(false);
  return `/api/users/${user.id}/credentials`;
}

for(const viewport of [{width:1280,height:400},{width:390,height:600}]){
  test(`the executor and model menus stay usable at ${viewport.width}x${viewport.height}`,async({page})=>{
    const errors=[];page.on('pageerror',e=>errors.push(e.message));
    // appReady may reload a stalled first load; only the document under test counts.
    page.on('request',r=>{if(r.isNavigationRequest()&&r.frame()===page.mainFrame())errors.length=0});
    await page.setViewportSize(viewport);
    await signInUnconfigured(page);
    const path=await emptyCredentialsPath(page);
    await putCredentials(page,path,{codexApiKey:testKey});
    try{
      await page.reload();await openHome(page);
      const frame=chat(page);
      const cli=executorButton(frame),cliMenu=frame.locator('.permission-menu[role="menu"]');
      const fits=async menu=>{
        await expect(menu).toBeVisible();
        await expect.poll(()=>fitsFrame(menu)).toBe(true);
      };
      await cli.click();
      await fits(cliMenu);
      await page.keyboard.press('Escape');
      await expect(cliMenu).toBeHidden();
      await cli.click();
      await cliMenu.getByRole('menuitemradio',{name:'Codex',exact:true}).click();
      const model=frame.getByRole('button',{name:'选择模型',exact:true}),modelMenu=frame.locator('.grasp-model-menu');
      await model.click();
      await fits(modelMenu);
      await page.keyboard.press('Escape');
      await expect(modelMenu).toBeHidden();
      await model.click();
      const search=modelMenu.locator('input[aria-label="搜索模型"]');
      await expect(search).toBeVisible();
      const first=(await modelMenu.locator('[role^="menuitem"]').first().innerText()).split('\n')[0].trim();
      await search.fill('zz-no-such-model');
      await expect(modelMenu).not.toContainText(first);
      await search.fill(first);
      await expect(modelMenu).toContainText(first);
      await page.keyboard.press('Escape');
      expect(errors).toEqual([]);
    }finally{
      await putCredentials(page,path,{codexApiKeyClear:true});
    }
  });
}

test('executor selection has independent models, credentials and a persistent choice',async({page})=>{
  await signInUnconfigured(page);
  const path=await emptyCredentialsPath(page);
  const draft='Keep this draft while choosing an executor';
  for(const [id,label] of [['codex','Codex'],['claude','Claude Code'],['codebuddy','CodeBuddy']]){
    const response=await page.request.post('/api/assistant/generations',{data:{executor:id,model:'default',messages:[{role:'user',content:'No key: reject before executing.'}]}});
    expect(response.status()).toBe(400);expect((await response.json()).error).toContain(label);
    const models=await page.request.get('/api/assistant/models?executor='+id);
    expect(models.ok()).toBe(true);
    const catalog=await models.json();
    expect(Array.isArray(catalog.models)).toBe(true);
    if(id==='codebuddy')expect(catalog.default).toBeTruthy();
    else expect(catalog.models.length).toBeGreaterThan(0);
  }
  await putCredentials(page,path,{codexApiKey:testKey});
  try{
    const catalog=await page.request.get('/api/assistant/models').then(r=>r.json());
    await page.reload();
    const initial=await openHome(page);
    const frame=chat(page),executor=executorButton(frame);
    const options=frame.locator('.permission-menu [role="menuitemradio"]');
    if(catalog.preferences){
      expect(catalog.preferences.executor).toBe('codex');
      await executor.click();
      await expect(options).toHaveCount(1);
      await expect(options.first()).toContainText('Codex');
      await page.keyboard.press('Escape');
    }
    await initial.fill(draft);
    await putCredentials(page,path,{codeBuddyApiKey:testKey,claudeCodeApiKey:testKey});
    await page.reload();
    const input=await openHome(page);
    const choose=async label=>{
      await executor.click();
      await options.filter({hasText:label}).click();
      await expect(executor).toContainText(label);
    };
    const {engines}=await page.request.get('/api/assistant/models').then(r=>r.json());
    for(const [id,label] of [['codex','Codex'],['claude-code','Claude Code'],['codebuddy','CodeBuddy']]){
      await choose(label);
      await expect(input).toHaveValue(draft);
      await expect(frame.locator('.credential-notice')).toHaveCount(0);
      const missing=engines.find(engine=>engine.id===id)?.serverMissing;
      await expect(frame.locator('.runtime-cli-notice')).toHaveCount(missing?1:0);
      if(missing)await expect(frame.locator('.send-button')).toBeDisabled();
    }
    await choose('Codex');
    await page.reload();
    await expect(executor).toContainText('Codex');
    expect((await page.request.get('/api/assistant/models?executor=shell')).status()).toBe(400);
  }finally{
    await putCredentials(page,path,{codeBuddyApiKeyClear:true,codexApiKeyClear:true,claudeCodeApiKeyClear:true});
  }
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
      expect((await page.request.put('/api/workflows',{data:{id,projectId,name:id,status:'draft',sandboxProfileId:'generic-docker',nodes:[{id:'work',name:'Work',type:'agent',agentId:id,executorId:'codebuddy',position:{x:100,y:100}}],edges:[]}})).ok()).toBe(true);
    }
    await page.reload();await switchProject(page,projectId);
    await openSettingsPage(page,'sops');
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
