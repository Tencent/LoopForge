import {test,before,after} from 'node:test';
import assert from 'node:assert/strict';
import {execFile} from 'node:child_process';
import {promisify} from 'node:util';
import {fileURLToPath} from 'node:url';
import {api,get,put,post,track,cleanup,ensureProject,waitRun} from '../lib/api.mjs';
import {cfg,uid} from '../lib/env.mjs';

before(ensureProject);
after(cleanup);

test('a scoped cleanup removes its run and preserves another waiting workflow', {timeout:90_000},async()=>{
  const workflows=[];
  for(const label of ['cleanup-own','cleanup-other']){
    const id=uid(label);workflows.push(id);
    await put('/api/workflows',{id,projectId:cfg.projectId,name:id,sandboxProfileId:cfg.sandbox,status:'draft',nodes:[{id:'gate',name:'Human decision',type:'approval',maxAttempts:1}],edges:[]});
    track(id,()=>api('DELETE',`/api/workflows/${id}`,undefined,{expect:[200,204,404]}));
    await post(`/api/workflows/${id}/publish`,{});
  }
  const runs=[];
  for(const workflowId of workflows){
    const run=await post('/api/runs',{workflowId,projectId:cfg.projectId,input:'Cleanup isolation fixture'});runs.push(run.id);
    track(run.id,async()=>{const existing=await api('GET',`/api/runs/${run.id}`,undefined,{expect:[200,404]});if(!existing.id)return;await api('POST',`/api/runs/${run.id}/cancel`,{},{expect:[200,202,400,404,409]});await api('DELETE',`/api/runs/${run.id}`,undefined,{expect:[200,204,404]})});
    await waitRun(run.id,r=>r.nodes?.gate?.status==='waiting_approval');
  }
  await promisify(execFile)(process.execPath,[fileURLToPath(new URL('../cleanup.mjs',import.meta.url))],{
    env:{...process.env,AGENTFLOW_E2E_BASE:cfg.base,AGENTFLOW_E2E_RUN_TAG:workflows[0]},timeout:60_000,
  });
  await api('GET',`/api/runs/${runs[0]}`,undefined,{expect:404});
  await api('GET',`/api/workflows/${workflows[0]}`,undefined,{expect:404});
  assert.equal((await get(`/api/runs/${runs[1]}`)).nodes.gate.status,'waiting_approval');
  assert.equal((await get(`/api/workflows/${workflows[1]}`)).id,workflows[1]);
});
