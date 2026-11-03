import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('schedule helpers and job list describe timing, status, blockers, and project scope', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.data.runs=[{id:'align-1',status:'waiting',nodes:{resolve:{output:'resolved package.json'}}}];
    state.data.scheduleJobs=[
      {id:'job-dispatch',projectId:'project-1',name:'Hourly dispatch',intervalSec:3600,enabled:true,maxDispatch:4,source:'gongfeng',issueStatuses:['todo','blocked'],lastStatus:'waiting',lastError:'基线冲突',blockers:[{repositoryName:'atlas-web',defaultBranch:'main',integrateBranch:'develop',since:'2026-10-30T08:00:00Z',alignRunId:'align-1',conflictFiles:['package.json'],conflictBlocks:[{file:'package.json',ours:'"a": 1',theirs:'"a": 2'}]}]},
      {id:'job-ai',projectId:'project-1',name:'Daily summary',cron:'0 9 * * 1-5',skipDispatch:true,assistantPrompt:'summarize',enabled:true,lastStatus:'running',lastRunAt:'2026-10-30T08:00:00Z'},
      {id:'other',projectId:'other-project',name:'Hidden',intervalSec:60,enabled:true}
    ];
    state.data.schedulerEnabled=true;
    renderScheduleJobs();
  `);
  const text = app.document.querySelector('#scheduleJobList').textContent;
  assert.match(text, /Hourly dispatch|每小时|只同步工蜂|待对齐基线/);
  assert.match(text, /package\.json|集成分支侧|默认分支侧|一键重新对齐/);
  assert.match(text, /Daily summary|运行中/);
  assert.doesNotMatch(text, /Hidden/);
  assert.equal(app.document.querySelector('#scheduleJobCount').textContent, '2');
  assert.equal(app.evaluate(`scheduleIntervalLabel(172800)`), '每 2 天');
  assert.equal(app.evaluate(`scheduleIntervalLabel(120)`), '每 2 分钟');
  assert.equal(app.evaluate(`scheduleJobTimingLabel({cron:'0 8 * * *'})`), '0 8 * * *');
  assert.equal(app.evaluate(`scheduleSourceLabel({source:'tapd'})`), '只同步 TAPD');
  assert.equal(JSON.stringify(app.evaluate(`scheduleIssueStatusValues({id:'old'})`)), JSON.stringify(['backlog', 'todo', 'in_progress', 'in_review', 'blocked']));
  assert.match(app.evaluate(`alignConflictPanelHTML(state.data.runs[0])`), /resolved package\.json|让 Agent 重新对齐/);
});

test('schedule form switches job kind and timing and sends validated assistant and dispatch payloads', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.post('/api/schedule-jobs', call => ({ ...call.body, id: call.body.skipDispatch ? 'new-ai' : 'new-dispatch', lastStatus: '' }));
  app.evaluate(`(() => {
    state.data.scheduleJobs=[];
    fillScheduleJobForm({});
    const f=$('#scheduleJobForm');
    f.elements.jobKind.value='assistant';syncScheduleJobKind();
    f.elements.scheduleMode.value='cron';syncScheduleJobTiming();
  })()`);
  const form = app.document.querySelector('#scheduleJobForm');
  assert.equal(form.elements.workflowId.required, false);
  assert.equal(form.elements.assistantPrompt.required, true);
  assert.equal(form.elements.intervalSec.hidden, true);
  assert.equal(form.elements.cron.hidden, false);
  assert.match(form.querySelector('[data-schedule-hint]').textContent, /东八区 5 段/);

  form.elements.name.value = 'Morning summary';
  form.elements.assistantPrompt.value = 'Summarize failures';
  form.elements.assistantModel.value = 'glm-test';
  form.elements.cron.value = '0 9 * * 1-5';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(4);
  const aiCall = app.router.calls.find(c => c.method === 'POST' && c.path === '/api/schedule-jobs');
  assert.equal(aiCall.body.skipDispatch, true);
  assert.equal(aiCall.body.assistantPrompt, 'Summarize failures');
  assert.equal(aiCall.body.cron, '0 9 * * 1-5');
  assert.equal(aiCall.body.workflowId, '');

  app.evaluate(`(() => {
    fillScheduleJobForm({});
    const f=$('#scheduleJobForm');
    f.elements.name.value='Every hour';
    f.elements.jobKind.value='dispatch';syncScheduleJobKind();
    f.elements.scheduleMode.value='interval';syncScheduleJobTiming();
    f.elements.intervalSec.value='3600';
    f.elements.workflowId.value='flow-1';
    f.elements.maxDispatch.value='3';
    [...f.querySelectorAll('[name="issueStatus"]')].forEach(x=>x.checked=x.value==='todo');
  })()`);
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(4);
  const calls = app.router.calls.filter(c => c.method === 'POST' && c.path === '/api/schedule-jobs');
  assert.equal(calls.length, 2);
  assert.equal(calls[1].body.skipDispatch, false);
  assert.equal(calls[1].body.intervalSec, 3600);
  assert.deepEqual(calls[1].body.issueStatuses, ['todo']);
  assert.equal(calls[1].body.maxDispatch, 3);
  assert.match(app.document.querySelector('#scheduleJobList').textContent, /Morning summary|Every hour/);
});

test('running a schedule job updates the UI immediately and persists the server outcome', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.data.scheduleJobs=[{id:'job-1',projectId:'project-1',name:'Manual job',intervalSec:3600,enabled:true,lastStatus:''}];renderScheduleJobs()`);
  app.router.post('/api/schedule-jobs/job-1/run', { id: 'job-1', projectId: 'project-1', name: 'Manual job', intervalSec: 3600, enabled: true, lastStatus: 'succeeded', lastRunAt: '2026-10-31T03:00:00Z', lastPulled: 4, lastDispatched: 2 });
  await app.evaluate(`runScheduleJob('job-1')`);
  const call = app.router.calls.find(c => c.method === 'POST' && c.path === '/api/schedule-jobs/job-1/run');
  assert.deepEqual(call.body, {});
  assert.equal(app.evaluate(`state.data.scheduleJobs[0].lastStatus`), 'succeeded');
  assert.match(app.document.querySelector('#scheduleJobList').textContent, /候选 4|启动 2/);
  app.evaluate(`
    scheduleJobOutcomeToast({skipDispatch:true,lastStatus:'failed',lastError:'model unavailable'});
    scheduleJobOutcomeToast({lastStatus:'waiting',lastError:'merge main'});
    scheduleJobOutcomeToast({lastStatus:'failed',lastError:'sync failed'});
    scheduleJobOutcomeToast({lastStatus:'succeeded',lastPulled:5,lastDispatched:3});
  `);
  const messages = [...app.document.querySelectorAll('.toast-item-msg')].map(x => x.textContent).join('|');
  assert.match(messages, /model unavailable|待对齐基线|sync failed|候选 5，启动 3/);
  assert.match(app.evaluate(`scheduleJobPollSignature([{id:'a',lastStatus:'running',lastPulled:2}], '')`), /a:running/);
});

test('realign and delete schedule actions send the confirmed mutation requests', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.post('/api/runs/align-1/reject', { ok: true });
  app.router.delete('/api/schedule-jobs/job-delete', { ok: true });
  app.evaluate(`
    state.data.runs=[{id:'align-1',status:'waiting',nodes:{confirm:{status:'waiting_approval'}}}];
    state.data.scheduleJobs=[{id:'job-delete',projectId:'project-1',name:'Delete me',intervalSec:3600,enabled:true}];
  `);
  const realign = app.evaluate(`realignBaseline('align-1')`);
  app.document.querySelector('#confirmOkBtn').click();
  await realign;
  const reject = app.router.calls.find(c => c.method === 'POST' && c.path === '/api/runs/align-1/reject');
  assert.equal(reject.body.nodeId, 'confirm');
  assert.equal(reject.body.targetId, 'resolve');
  assert.match(reject.body.context, /git merge origin\/master/);

  const deletion = app.evaluate(`deleteScheduleJob('job-delete')`);
  app.document.querySelector('#confirmOkBtn').click();
  await deletion;
  assert.ok(app.router.calls.some(c => c.method === 'DELETE' && c.path === '/api/schedule-jobs/job-delete'));
  assert.equal(app.evaluate(`state.data.scheduleJobs.length`), 0);
});

test('sync configuration and issue push persist project-scoped integration settings', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.put('/api/sync/config', call => ({ ...call.body, autoSyncEnabled: call.body.autoSyncEnabled }));
  app.router.put('/api/projects', call => call.body);
  app.router.post('/api/sync/create', call => ({ ...app.evaluate(`state.data.issues[0]`), source: call.body.target, externalRef: { externalIid: 42, externalId: 'T-42' } }));
  app.evaluate(`
    state.data.syncConfig={gongfengProjectId:'1001'};
    $('#syncGongfengURL').value='https://mcp.example/gongfeng';
    $('#syncTAPDURL').value='https://mcp.example/tapd';
    $('#syncTAPDWorkspace').value='ws-9';
    $('#syncTAPDIteration').value='iter-3';
    $('#syncInterval').value='600';
    $('#syncAutoEnabled').checked=true;
  `);
  await app.evaluate(`saveSyncConfig()`);
  const config = app.router.calls.find(c => c.method === 'PUT' && c.path === '/api/sync/config');
  assert.equal(config.body.gongfengMcpUrl, 'https://mcp.example/gongfeng');
  assert.equal(config.body.autoSyncIntervalSec, 600);
  assert.equal(config.body.autoSyncEnabled, true);
  const project = app.router.calls.find(c => c.method === 'PUT' && c.path === '/api/projects');
  assert.equal(project.body.tapdWorkspaceId, 'ws-9');
  assert.equal(project.body.tapdIterationId, 'iter-3');

  app.evaluate(`renderSync();$('#syncPushIssue').value='issue-1'`);
  await app.evaluate(`pushIssue('gongfeng')`);
  const push = app.router.calls.find(c => c.method === 'POST' && c.path === '/api/sync/create');
  assert.equal(push.body.issueId, 'issue-1');
  assert.equal(push.body.target, 'gongfeng');
  assert.equal(app.evaluate(`state.data.issues[0].source`), 'gongfeng');
});
