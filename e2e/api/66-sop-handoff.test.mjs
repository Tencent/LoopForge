// Latest upstream: durable, nonblocking group-to-SOP handoff on real MySQL
// and CodeBuddy. Crash/lease recovery is additionally covered by Go tests.
import {test,before,after} from 'node:test';
import assert from 'node:assert/strict';
import {get,post,api,cleanup,ensureProject,waitRun,waitFor} from '../lib/api.mjs';
import {cfg,uid} from '../lib/env.mjs';
import {createWorkflow,script,approval,startRun} from '../lib/fixtures.mjs';

before(ensureProject);
after(cleanup);

test('group-to-SOP: persist context, submit once, archive handoff and continue the selected node', {skip:!cfg.model,timeout:420_000},async()=>{
  const marker=uid('handoff-context');
  const wf=await createWorkflow('durable-handoff',[
    script('seed','printf seed > evidence.txt'),
    approval('gate','seed'),
    script('verify','test -f evidence.txt && echo HANDOFF_CONTINUED'),
  ]);
  const {id}=await startRun(wf);
  await waitRun(id,r=>r.nodes?.gate?.status==='waiting_approval');
  await post(`/api/runs/${id}/group/mode`,{});
  const session=await get(`/api/runs/${id}/conversation`);
  const started=await post('/api/assistant/generations',{
    sessionId:session.id,runId:id,projectId:cfg.projectId,model:cfg.modelID,approvalMode:'ask',
    messages:[...(session.messages||[]),{role:'user',content:`自动化验收，只回复已记录以下要求，不要调用任何工具或修改任何东西：后续交接请保留标记 ${marker}，并保留 evidence.txt。`}],
  });
  const generation=await waitFor('group context persisted',async()=>{
    const g=await get(`/api/assistant/sessions/${started.sessionId}/generation`);
    return g.status!=='running'?g:null;
  },{timeoutMs:180_000,intervalMs:1500});
  assert.equal(generation.status,'completed',generation.error);
  const accepted=await post(`/api/runs/${id}/group/sop`,{nodeId:'gate'},{raw:true,expect:202});
  const job=accepted.data.job;
  assert.ok(job.id);
  const duplicate=await post(`/api/runs/${id}/group/sop`,{nodeId:'gate'},{expect:202});
  assert.equal(duplicate.job.id,job.id,'an in-flight submission must be idempotent');
  const done=await waitFor('durable handoff completed',async()=>{
    const {job:j}=await get(`/api/runs/${id}/group/sop/handoff`);
    return j && ['failed','succeeded'].includes(j.status)?j:null;
  },{timeoutMs:180_000,intervalMs:1500});
  assert.equal(done.status,'succeeded',`${done.phase}: ${done.error}`);
  assert.equal(done.phase,'done');
  assert.ok(done.summaryArtifactId);
  const artifact=await api('GET',`/api/runs/${id}/artifacts/${encodeURIComponent(done.summaryArtifactId)}`,undefined,{raw:true});
  assert.match(artifact.text,new RegExp(marker),'handoff must carry the actual user constraint');
  await waitRun(id,r=>r.nodes?.gate?.status==='waiting_approval'&&!r.pauseRequested);
  await post(`/api/runs/${id}/approve`,{nodeId:'gate'});
  const run=await waitRun(id,r=>r.status==='succeeded');
  assert.equal(run.nodes.verify.status,'succeeded');
  assert.equal(run.nodes.gate.status,'succeeded');
  const {jobs}=await get(`/api/runs/group/sop/handoffs?projectId=${cfg.projectId}`);
  assert.ok(jobs.some(j=>j.id===job.id));
  assert.match((await api('GET',`/api/runs/${id}/logs.txt`,undefined,{raw:true})).text,/HANDOFF_CONTINUED/);
});
