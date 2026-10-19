import {test,expect} from '@playwright/test';
import fs from 'node:fs';
import {signInUnconfigured} from './helpers.mjs';

// Acceptance uses a dedicated account without Git credentials. The optional
// model case reads a protected local key file; never capture request bodies.
test.use({trace:'off',screenshot:'off'});

async function setup(page){
  await signInUnconfigured(page);
  const identity=(await page.request.get('/api/auth/current').then(r=>r.json())).user;
  const users=await page.request.get('/api/users').then(r=>r.json());
  const user=users.find(u=>u.id===identity.id);
  expect(!!user.gitTokenConfigured).toBe(false);
  expect(!!user.codeBuddyApiKeyConfigured).toBe(false);
  const projectId='e2e-onboarding';
  let response=await page.request.get(`/api/projects/${projectId}`);
  if(response.status()===404)response=await page.request.post('/api/projects',{data:{id:projectId,key:'GUIDE',name:'E2E onboarding',ownerId:user.id}});
  expect(response.ok()).toBe(true);
  const project=await response.json();expect(project.ownerId).toBe(user.id);
  expect(project.defaultRepositoryId||'').toBe('');
  const repositories=await page.request.get('/api/repositories').then(r=>r.json());
  expect(repositories.filter(r=>r.projectId===projectId)).toEqual([]);
  return {projectId,user};
}

async function readRun(page,id){
  const response=await page.request.get(`/api/runs/${id}`);expect(response.ok()).toBe(true);
  return response.json();
}
async function removeRun(page,id){
  if(!id)return;
  const run=await readRun(page,id);
  if(!['succeeded','failed','cancelled','rejected'].includes(run.status)){
    await page.request.post(`/api/runs/${id}/cancel`,{data:{}});
    await expect.poll(async()=>(await readRun(page,id)).status,{timeout:15_000}).toMatch(/^(succeeded|failed|cancelled|rejected)$/);
  }
  // Terminal workspace cleanup may overlap the delete request; wait for
  // the real deletion to succeed rather than failing on that short race.
  await expect.poll(async()=>{
    const removed=await page.request.delete(`/api/runs/${id}`);
    if(removed.ok())return true;
    const detail=await removed.text();
    if(removed.status()===400&&/no such file|directory not empty|工作区正在使用或保存/.test(detail))return false;
    throw new Error(`Run cleanup failed: HTTP ${removed.status()} ${detail}`);
  },{timeout:10_000}).toBe(true);
}

test('a script and human approval run without a model key, GitHub repository or Git token',async({page})=>{
  const {projectId}=await setup(page);let runId='';
  try{
    const created=await page.request.post('/api/runs',{data:{projectId,workflowId:'welcome-workflow',input:'Acceptance without a repository or credentials'}});
    expect(created.ok()).toBe(true);runId=(await created.json()).id;
    await expect.poll(async()=>(await readRun(page,runId)).status,{timeout:45_000}).toBe('waiting');
    const run=await readRun(page,runId);expect(run.repositories||[]).toEqual([]);
    expect(run.nodes.prepare.status).toBe('succeeded');
    expect(run.artifacts.some(a=>a.name==='welcome')).toBe(true);
    const approved=await page.request.post(`/api/runs/${runId}/approve`,{data:{nodeId:'approve'}});expect(approved.ok()).toBe(true);
    await expect.poll(async()=>(await readRun(page,runId)).status,{timeout:30_000}).toBe('succeeded');

  }finally{await removeRun(page,runId)}
});

test('all generic SOPs can start without a GitHub repository or token',async({page})=>{
  const {projectId}=await setup(page);
  const workflowId=`e2e-product-documents-${Date.now().toString(36)}`;
  let runId='',copied=false;
  try{
    await page.reload();
    await page.locator('#projectSwitcher').selectOption(projectId);
    await page.locator('.nav[data-view="sops"]').click();
    await page.locator('[data-scope-for="sop"] [data-scope="generic"]').click();
    for(const id of ['generic-product-delivery','generic-backend-delivery','generic-frontend-delivery','issue-fix-production']){
      const card=page.locator(`article.sop-card[data-adopt-generic="${id}"]`);
      await expect(card).toBeVisible();
      await expect(card.locator('.req-chip').filter({hasText:'GitHub'})).toHaveCount(0);
    }
    const clone=await page.request.post('/api/workflows/generic-product-delivery/clone',{data:{id:workflowId,name:'E2E product documents',projectId}});
    expect(clone.ok()).toBe(true);copied=true;
    // Missing model credentials may fail execution, but must never block
    // admission with repository_required or attach a Git repository.
    for(const id of ['generic-product-delivery','generic-backend-delivery','generic-frontend-delivery','issue-fix-production',workflowId]){
      const created=await page.request.post('/api/runs',{data:{projectId,workflowId:id,input:'Check repository-free admission only'}});
      expect(created.status()).toBe(201);
      const run=await created.json();runId=run.id;expect(run.repositories||[]).toEqual([]);
      await removeRun(page,runId);runId='';
    }
  }finally{
    await removeRun(page,runId);
    if(copied)expect((await page.request.delete(`/api/workflows/${workflowId}`)).ok()).toBe(true);
  }
});

test('a real Agent generates a document with a model key and no GitHub repository or token',async({page})=>{
  test.skip(process.env.AGENTFLOW_E2E_MODEL!=='1'||!process.env.LOOPFORGE_MODEL_KEY_FILE,'Requires AGENTFLOW_E2E_MODEL=1 and an explicit protected LOOPFORGE_MODEL_KEY_FILE');
  test.setTimeout(300_000);
  const {projectId,user}=await setup(page);
  const credentialPath=`/api/users/${user.id}/credentials`;
  const suffix=Date.now().toString(36),agentId=`e2e-no-git-writer-${suffix}`,workflowId=`e2e-no-git-document-${suffix}`,marker=`NO-GITHUB-${suffix}`;
  let runId='',agentSaved=false,workflowSaved=false;
  try{
    const saved=await page.request.put(credentialPath,{data:{codeBuddyApiKey:fs.readFileSync(process.env.LOOPFORGE_MODEL_KEY_FILE,'utf8').trim(),codeBuddyInternetEnvironment:process.env.LOOPFORGE_MODEL_KEY_ENVIRONMENT||''}});
    expect(saved.ok()).toBe(true);
    const agent=await page.request.put('/api/agents',{data:{id:agentId,name:'E2E document writer without GitHub',projectId,executorId:'codebuddy',model:process.env.AGENTFLOW_E2E_MODEL_ID||process.env.LOOPFORGE_TEST_MODEL||'default',instructions:`Write report.md in the current workspace with a short three-item to-do plan and the literal marker ${marker}. Do not use Git, access the network, install dependencies or modify other files. Reply when done.`}});
    expect(agent.ok()).toBe(true);agentSaved=true;
    const workflow=await page.request.put('/api/workflows',{data:{id:workflowId,name:'E2E document without GitHub',projectId,status:'draft',revision:0,sandboxProfileId:'local',nodes:[
      {id:'write',name:'Write document',type:'agent',agentId,executorId:'codebuddy',maxAttempts:1,maxReworks:0,position:{x:100,y:100},artifactOutputs:[{name:'report',path:'report.md',mediaType:'text/markdown',required:true}]},
      {id:'verify',name:'Verify document',type:'script',scriptLanguage:'shell',script:`grep -q '${marker}' report.md`,maxAttempts:1,maxReworks:0,position:{x:350,y:100}},
    ],edges:[{id:'write-verify',source:'write',target:'verify',when:'success'}]}});
    expect(workflow.ok()).toBe(true);workflowSaved=true;
    const published=await page.request.post(`/api/workflows/${workflowId}/publish`,{data:{}});expect(published.ok()).toBe(true);
    const created=await page.request.post('/api/runs',{data:{projectId,workflowId,input:'Produce a document without connecting GitHub'}});
    expect(created.ok()).toBe(true);runId=(await created.json()).id;
    await expect.poll(async()=>(await readRun(page,runId)).status,{timeout:240_000,intervals:[1000,2000,5000]}).toMatch(/^(succeeded|failed|cancelled)$/);
    const run=await readRun(page,runId);expect(run.status).toBe('succeeded');expect(run.repositories||[]).toEqual([]);
    expect(run.nodes.write.status).toBe('succeeded');expect(run.nodes.verify.status).toBe('succeeded');
    const artifact=run.artifacts.find(a=>a.name==='report');expect(artifact).toBeTruthy();
    const content=await page.request.get(`/api/runs/${runId}/artifacts/${encodeURIComponent(artifact.id)}`);
    expect(content.ok()).toBe(true);expect(await content.text()).toContain(marker);
    const currentUsers=await page.request.get('/api/users').then(r=>r.json());
    expect(!!currentUsers.find(u=>u.id===user.id).gitTokenConfigured).toBe(false);
  }finally{
    try{
      await removeRun(page,runId);
      if(workflowSaved)expect((await page.request.delete(`/api/workflows/${workflowId}`)).ok()).toBe(true);
      if(agentSaved)expect((await page.request.delete(`/api/agents/${agentId}`)).ok()).toBe(true);
    }finally{
      const cleared=await page.request.put(credentialPath,{data:{codeBuddyApiKeyClear:true,codeBuddyInternetEnvironment:user.codeBuddyInternetEnvironment||''}});expect(cleared.ok()).toBe(true);
    }
  }
});
