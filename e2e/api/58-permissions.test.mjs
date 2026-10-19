import {test,before,after} from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import {get,put,post,del,track,cleanup,ensureProject,waitRun,tokenOwner} from '../lib/api.mjs';
import {cfg,uid} from '../lib/env.mjs';
import {createWorkflow,script,approval,startRun} from '../lib/fixtures.mjs';

before(ensureProject);
after(cleanup);

test('separate account cannot access private conversations, edit workspaces or impersonate credentials',{skip:!process.env.LOOPFORGE_UNCONFIGURED_USER},async()=>{
  const login=await fetch(cfg.base+'/api/auth/password',{method:'POST',headers:{Origin:cfg.base,'Content-Type':'application/json'},body:JSON.stringify({login:process.env.LOOPFORGE_UNCONFIGURED_USER,password:process.env.LOOPFORGE_UNCONFIGURED_PASSWORD||fs.readFileSync(process.env.LOOPFORGE_UNCONFIGURED_PASSWORD_FILE,'utf8').trim()})});
  assert.equal(login.status,200,'second account login');
  const identity=(await login.json()).user;
  const cookie=login.headers.get('set-cookie').split(';')[0];
  const second=async(method,path,body,expected)=>{
    const r=await fetch(cfg.base+path,{method,headers:{Cookie:cookie,Origin:cfg.base,'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});
    assert.ok([].concat(expected).includes(r.status),`${method} ${path}: HTTP ${r.status}`);
    return r;
  };
  const owner=await tokenOwner();
  assert.notEqual(owner.id,identity.id,'test needs two distinct accounts');
  const initial=await get(`/api/projects/${cfg.projectId}`);
  const restore={...initial,members:initial.members||[]};
  track('restore original project membership',()=>put('/api/projects',restore));
  // A prior interrupted run may have left this account as a viewer. Restore
  // the exact original membership afterwards, including an explicit [] (an
  // omitted members field intentionally means 'keep existing' in the API).
  await put('/api/projects',{...initial,members:(initial.members||[]).filter(m=>m.userId!==identity.id)});
  const id=uid('private-conversation');
  await put(`/api/assistant/sessions/${id}`,{title:'E2E private',projectId:cfg.projectId,messages:[{role:'user',content:'Private acceptance fixture'}]});
  track('private session',()=>del(`/api/assistant/sessions/${id}`));
  await second('GET',`/api/assistant/sessions/${id}`,undefined,[403,404]);
  await second('PUT',`/api/users/${owner.id}/credentials`,{gitTokenClear:true},403);
  await second('PUT','/api/platform/admins',{loginName:identity.loginName,role:'admin'},403);
  await second('GET',`/api/assistant/references?projectId=${cfg.projectId}`,undefined,403);
  const wf=await createWorkflow('permissions',[script('seed','echo isolated > note.txt'),approval('gate','seed')]);
  const run=await startRun(wf);
  await waitRun(run.id,r=>r.nodes?.gate?.status==='waiting_approval');
  await second('POST',`/api/runs/${run.id}/workspace/takeover`,{},403);
  await second('PUT',`/api/runs/${run.id}/workspace/file`,{path:'note.txt',content:'wrong actor'},403);
  await second('GET',`/api/runs/${run.id}/conversation`,undefined,403);
  await second('POST',`/api/runs/${run.id}/group/mode`,{},403);
  assert.equal((await get(`/api/runs/${run.id}/workspace/file?path=note.txt`)).content,'isolated\n');
  const snapshot=await get(`/api/projects/${cfg.projectId}`);
  const original={...snapshot,members:snapshot.members||[]};
  track('restore project membership',()=>put('/api/projects',original));
  await put('/api/projects',{...original,members:[...(original.members||[]),{userId:identity.id,role:'viewer'}]});
  try {
    // Read-only membership must not become agent execution permission.
    await second('GET',`/api/runs/${run.id}/conversation`,undefined,200);
    await second('POST','/api/task-groups',{projectId:cfg.projectId,workflowId:wf},403);
    for(const path of ['mode','members','tasks','sop','sop/handoff/retry']){
      await second('POST',`/api/runs/${run.id}/group/${path}`,{},403);
    }
    await second('PUT',`/api/runs/${run.id}/workspace/file`,{path:'note.txt',content:'viewer write'},403);
    assert.equal((await get(`/api/runs/${run.id}/workspace/file?path=note.txt`)).content,'isolated\n');
  } finally {await put('/api/projects',original)}
});
