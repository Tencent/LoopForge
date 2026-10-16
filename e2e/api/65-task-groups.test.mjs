import {test,before,after} from 'node:test';
import assert from 'node:assert/strict';
import {api,get,post,cleanup,ensureProject,waitRun,waitFor} from '../lib/api.mjs';
import {cfg,uid} from '../lib/env.mjs';
import {createWorkflow,createAgent,script,approval,startRun,trackRun} from '../lib/fixtures.mjs';

before(ensureProject);
after(cleanup);

test('task group: direct creation, shared conversation retention and invalid invitations',async()=>{
  const role=await createAgent('group-member','Only respond when explicitly assigned.');
  const wf=await createWorkflow('group-direct',[script('seed','echo GROUP_SEED'),approval('gate','seed')],{groupMembers:[role]});
  const run=await post('/api/task-groups',{workflowId:wf,projectId:cfg.projectId,input:'E2E group'});
  trackRun(run.id);
  assert.equal(run.workflowSnapshot.collaborationMode,'group');
  const session=await get(`/api/runs/${run.id}/conversation`);
  assert.equal(session.shared,true);
  await api('DELETE',`/api/assistant/sessions/${session.id}`,undefined,{expect:403});
  await api('POST',`/api/assistant/sessions/${session.id}/archive`,{archived:true},{expect:403});
  await api('POST',`/api/runs/${run.id}/group/members`,{agentId:'e2e-missing-role'},{expect:409});
});

test('task group: switch from SOP, dispatch a real role idempotently, complete explicitly',{skip:!cfg.model,timeout:420_000},async()=>{
  const member=await createAgent('group-worker','你是自动化测试角色。只执行指派任务，禁止网络访问。');
  const peer=await createAgent('group-peer','Only respond when explicitly assigned.');
  const wf=await createWorkflow('group-switch',[script('seed','echo GROUP_START > seed.txt'),approval('gate','seed'),script('finish','echo SOP_FINISH')]);
  const {id}=await startRun(wf);
  await waitRun(id,r=>r.nodes?.gate?.status==='waiting_approval');
  await post(`/api/runs/${id}/group/mode`,{});
  for(const agentId of [member,peer])await post(`/api/runs/${id}/group/members`,{agentId});
  const request={agentId:member,requestId:uid('dispatch'),prompt:'自动化验收：确认 seed.txt 存在，然后创建 group-result.txt，内容为 GROUP_MODEL_OK。只修改这个文件，完成后停止。'};
  const node=await post(`/api/runs/${id}/group/tasks`,request);
  const duplicate=await post(`/api/runs/${id}/group/tasks`,request);
  assert.equal(duplicate.id,node.id,'same request ID must not create duplicate work');
  const done=await waitFor('group role completion',async()=>{
    const r=await get(`/api/runs/${id}`); const n=r.nodes?.[node.id];
    return n && ['succeeded','failed'].includes(n.status)?{r,n}:null;
  },{timeoutMs:360_000,intervalMs:2000});
  assert.equal(done.n.status,'succeeded',done.n.error);
  assert.ok(done.n.usage?.totalTokens>0,'group role must record real model usage');
  const result=await get(`/api/runs/${id}/workspace/file?path=group-result.txt`,{expect:[200,404]});
  assert.match(result.content||'',/GROUP_MODEL_OK/,`role did not deliver its file: ${done.n.output}`);
  await api('POST',`/api/runs/${id}/group/sop`,{complete:true,nodeId:'finish'},{expect:400});
  await post(`/api/runs/${id}/group/sop`,{complete:true});
  const completed=await get(`/api/runs/${id}`);
  assert.equal(completed.status,'succeeded');
  assert.equal(completed.nodes.finish.status,'skipped');
});
