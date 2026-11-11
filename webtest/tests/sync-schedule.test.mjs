import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle, chooseOption } from '../lib/load-app.mjs';

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
  assert.match(text, /每小时|只同步工蜂|待对齐基线/);
  assert.match(text, /package\.json|集成分支侧|默认分支侧|一键重新对齐/);
  assert.doesNotMatch(text, /Daily summary/);
  assert.match(app.document.querySelector('#aiScheduleJobList').textContent, /Daily summary.*运行中/s);
  assert.doesNotMatch(app.document.querySelector('#aiScheduleJobList').textContent, /Hourly dispatch|Hidden/);
  assert.doesNotMatch(text, /Hidden/);
  assert.equal(app.document.querySelector('#scheduleJobCount').textContent, '已启用');
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
  assert.match(app.document.querySelector('#scheduleJobList').textContent, /每小时/);
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


test('AI schedule navigation separates existing tasks and preserves editor kind and project scope', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const tasks = [
    { id: 'ai-old', projectId: 'project-1', name: 'Existing AI job', skipDispatch: true, assistantPrompt: 'Review issues', intervalSec: 3600, enabled: true },
    { id: 'trigger-old', projectId: 'project-1', name: 'Existing trigger', workflowId: 'flow-1', intervalSec: 3600, enabled: true },
    { id: 'ai-other', projectId: 'other-project', name: 'Other AI job', skipDispatch: true, assistantPrompt: 'Other project', intervalSec: 3600 }
  ];
  app.router.get('/api/schedule-jobs', tasks);
  assert.equal(app.document.querySelector('.rail-btn[data-view="aiSchedules"]'), null, '定时任务 lives in the settings sidebar, not the rail');
  app.document.querySelector('#configurationNav .nav[data-view="aiSchedules"]').click();
  await settle(4);
  assert.equal(app.evaluate('state.currentView'), 'aiSchedules');
  assert.equal(app.window.location.hash, '#/aiSchedules');
  assert.equal(app.document.body.classList.contains('no-sidebar'), false);
  assert.equal(app.document.body.classList.contains('management-mode'), true);
  assert.match(app.document.querySelector('#aiScheduleJobList').textContent, /Existing AI job/);
  assert.doesNotMatch(app.document.querySelector('#aiScheduleJobList').textContent, /Existing trigger|Other AI job/);
  assert.equal(app.document.querySelector('#scheduleJobCount').textContent, '已启用');
  assert.equal(app.document.querySelector('#issueHeadingMenu #newScheduleJob'), null, 'SOP 触发 moved out of the issue list menu');
  app.document.querySelector('#aiScheduleTriggerTab').click();
  await settle(2);
  assert.equal(app.document.querySelector('#aiScheduleTriggerPanel').hidden, false);
  assert.equal(app.document.querySelector('#aiScheduleProjectPanel').hidden, true);
  assert.match(app.document.querySelector('#aiScheduleTriggerList').textContent, /已启用/);
  assert.ok(app.document.querySelector('#aiScheduleTriggerList [data-run-schedule="trigger-old"]'));
  assert.equal(app.document.querySelector('#aiScheduleTriggerList [data-run-schedule="ai-old"]'), null);
  app.document.querySelector('#aiScheduleProjectTab').click();
  await settle(2);

  app.document.querySelector('#aiScheduleJobList [data-edit-schedule="ai-old"]').focus();
  app.document.querySelector('#aiScheduleJobList [data-edit-schedule="ai-old"]').click();
  await settle(4);
  const form = app.document.querySelector('#scheduleJobForm');
  assert.equal(form.elements.id.value, 'ai-old');
  assert.equal(form.elements.assistantPrompt.value, 'Review issues');
  assert.equal(form.elements.jobKind.value, 'assistant');
  assert.equal(app.document.querySelector('#scheduleModalTitle').textContent, 'AI 定时任务');
  assert.match(app.document.querySelector('#scheduleModalHint').textContent, /完全权限/);
  assert.doesNotMatch(app.document.querySelector('#aiScheduleRuntimeHint').textContent, /不直接运行|不会派发/);
  assert.equal(app.document.querySelector('#scheduleJobList').hidden, true);
  app.evaluate('resetScheduleJobForm()');
  assert.equal(form.elements.id.value, '');
  assert.equal(form.elements.jobKind.value, 'assistant');

  app.evaluate('renderScheduleJobs()');
  app.window.document.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  assert.equal(app.document.activeElement.dataset.editSchedule, 'ai-old');
  assert.equal(app.document.querySelector('#scheduleModal').classList.contains('open'), false);
  await app.evaluate("openScheduleJobModal('dispatch')");
  assert.equal(form.elements.jobKind.value, 'dispatch');
  assert.equal(form.elements.workflowId.required, true);
  assert.equal(app.document.querySelector('#scheduleJobList').hidden, false);
  assert.equal(app.document.querySelector('#scheduleModalTitle').textContent, '触发配置');
  assert.equal(app.document.querySelector('#scheduleKindTabs'), null);
  assert.ok(app.document.querySelector('#issueHeadingMenu #importTAPDIssue[data-legacy-tracker][hidden]'));
  assert.equal(app.document.querySelectorAll('#importTAPDIssue').length, 1);

  app.evaluate("state.data.projects.push({id:'other-project',name:'Other',key:'OTHER'});renderProjectSwitcher()");
  chooseOption(app.document.querySelector('#projectSwitcher'), 'other-project');
  await settle(6);
  assert.equal(app.document.querySelector('#scheduleModal').classList.contains('open'), false);
  assert.match(app.document.querySelector('#aiScheduleJobList').textContent, /Other AI job/);
  assert.doesNotMatch(app.document.querySelector('#aiScheduleJobList').textContent, /Existing AI job/);
  assert.equal(app.document.querySelector('#scheduleJobCount').textContent, '未配置');
});

test('AI schedule actions retain task ids, notifications, scheduler availability and failed status', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const job = { id:'ai-1', projectId:'project-1', name:'Daily review', skipDispatch:true, assistantPrompt:'Review', intervalSec:3600, enabled:true, notifications:[] };
  app.router.get('/api/schedule-jobs', [job]);
  app.router.put('/api/schedule-jobs', call => ({ ...call.body }));
  app.router.post('/api/schedule-jobs/ai-1/run', { ...job, lastStatus:'failed', lastError:'model unavailable', lastRunAt:'2026-11-04T03:00:00Z' });
  app.router.delete('/api/schedule-jobs/ai-1', { ok:true });
  await app.evaluate('refreshAISchedules()');
  app.evaluate('state.data.schedulerEnabled=false;renderScheduleJobs()');
  assert.equal(app.document.querySelector('#aiScheduleJobList [data-run-schedule]').disabled, true);
  assert.match(app.document.querySelector('#aiScheduleRuntimeHint').textContent, /本实例不执行/);
  app.evaluate('state.data.schedulerEnabled=true;renderScheduleJobs()');
  app.document.querySelector('#aiScheduleJobList [data-edit-schedule]').click();
  await settle(4);
  const form=app.document.querySelector('#scheduleJobForm');
  form.elements.name.value='Updated review';
  form.dispatchEvent(new app.window.Event('submit', {bubbles:true,cancelable:true}));
  await settle(4);
  const saved=app.router.calls.find(c=>c.method==='PUT'&&c.path==='/api/schedule-jobs');
  assert.equal(saved.body.id, 'ai-1');
  assert.equal(saved.body.skipDispatch, true);
  assert.equal(saved.body.workflowId, '');
  assert.deepEqual(saved.body.notifications, []);
  app.evaluate('closeScheduleJobModal()');
  app.document.querySelector('#aiScheduleJobList [data-run-schedule]').click();
  await settle(4);
  assert.match(app.document.querySelector('#aiScheduleJobList').textContent, /model unavailable/);
  app.document.querySelector('#aiScheduleJobList [data-delete-schedule]').click();
  app.document.querySelector('#confirmOkBtn').click();
  await settle(4);
  assert.match(app.document.querySelector('#aiScheduleJobList').textContent, /还没有 AI 定时任务/);
  assert.equal(app.document.querySelector('#aiScheduleCount').textContent, '0 个任务');
});

test('project trigger config loads and updates the one saved configuration without a new action', async (t) => {
  const app=await loadApp();
  t.after(app.close);
  const saved={id:'trigger-existing',projectId:'project-1',name:'Saved trigger',workflowId:'flow-1',cron:'0 9 * * 1-5',enabled:false,maxDispatch:3};
  app.router.get('/api/schedule-jobs',[saved]);
  app.router.put('/api/schedule-jobs',call=>call.body);
  await app.evaluate("openScheduleJobModal('dispatch')");
  const f=app.document.querySelector('#scheduleJobForm');
  assert.equal(app.document.querySelector('#scheduleJobReset'),null);
  assert.doesNotMatch(app.document.querySelector('#scheduleModal').textContent,/新建配置|点击「新建/);
  assert.equal(f.elements.id.value,'trigger-existing');
  assert.equal(f.elements.cron.value,'0 9 * * 1-5');
  assert.equal(f.elements.enabled.checked,false);
  assert.equal(f.elements.name.closest('[data-schedule-kind]').hidden,true);
  assert.equal(f.elements.assistantPrompt.required,false);
  assert.equal(f.hidden,false);
  assert.equal(app.document.querySelector('#scheduleJobList [data-edit-schedule]'),null);
  assert.equal(app.document.querySelector('#scheduleJobList [data-delete-schedule]'),null);
  f.elements.maxDispatch.value='5';
  f.dispatchEvent(new app.window.Event('submit',{bubbles:true,cancelable:true}));
  await settle(4);
  const write=app.router.calls.find(c=>c.method==='PUT'&&c.path==='/api/schedule-jobs');
  assert.equal(write.body.id,'trigger-existing');
  assert.equal(write.body.maxDispatch,5);
  assert.equal(app.router.calls.filter(c=>c.method==='POST'&&c.path==='/api/schedule-jobs').length,0);
});

test('first trigger save reuses its id on reopen while AI schedules retain independent creation', async(t)=>{
  const app=await loadApp();
  t.after(app.close);
  let jobs=[];
  app.router.get('/api/schedule-jobs',()=>jobs);
  app.router.post('/api/schedule-jobs',call=>{const job={...call.body,id:'single-trigger'};jobs.push(job);return job});
  await app.evaluate("openScheduleJobModal('dispatch')");
  const f=app.document.querySelector('#scheduleJobForm');
  assert.equal(app.document.querySelector('#scheduleJobList').innerHTML,'');
  assert.equal(f.elements.name.value,'触发配置');
  f.dispatchEvent(new app.window.Event('submit',{bubbles:true,cancelable:true}));
  await settle(4);
  assert.equal(f.elements.id.value,'single-trigger');
  app.evaluate('closeScheduleJobModal()');
  await app.evaluate("openScheduleJobModal('dispatch')");
  assert.equal(f.elements.id.value,'single-trigger');
  app.evaluate('closeScheduleJobModal()');
  await app.evaluate("openScheduleJobModal('assistant')");
  assert.equal(f.elements.id.value,'');
  assert.equal(f.elements.name.value,'');
  assert.equal(f.elements.name.closest('[data-schedule-kind]').hidden,false);
});

test('save closes its own editor after success, retains input after failure and prevents duplicate submissions',async t=>{
 const app=await loadApp();t.after(app.close);
 let resolveSave;app.router.post('/api/schedule-jobs',call=>new Promise(resolve=>{resolveSave=()=>resolve({...call.body,id:'saved-local'})}));
 await app.evaluate("openScheduleJobModal('assistant')");
 const form=app.document.querySelector('#scheduleJobForm');form.elements.name.value='检查需求';form.elements.assistantPrompt.value='inspect';
 form.dispatchEvent(new app.window.Event('submit',{bubbles:true,cancelable:true}));await settle();
 assert.equal(form.querySelector('[type=submit]').disabled,true);assert.equal(app.document.querySelector('#scheduleModal').classList.contains('open'),true);
 form.dispatchEvent(new app.window.Event('submit',{bubbles:true,cancelable:true}));await settle();
 assert.equal(app.router.calls.filter(c=>c.path==='/api/schedule-jobs'&&c.method==='POST').length,1);
 resolveSave();await settle(4);
 assert.equal(app.document.querySelector('#scheduleModal').classList.contains('open'),false);
 assert.match(app.document.querySelector('#aiScheduleJobList').textContent,/检查需求/);
 app.router.post('/api/schedule-jobs',{status:400,body:{error:'save failed'}});
 await app.evaluate("openScheduleJobModal('assistant')");form.elements.name.value='保留输入';form.elements.assistantPrompt.value='keep me';
 form.dispatchEvent(new app.window.Event('submit',{bubbles:true,cancelable:true}));await settle(4);
 assert.equal(app.document.querySelector('#scheduleModal').classList.contains('open'),true);
 assert.equal(form.elements.assistantPrompt.value,'keep me');assert.equal(form.querySelector('[type=submit]').disabled,false);
 assert.equal(form.querySelector('[type=submit]').textContent,'保存 AI 定时任务');
});

test('late save responses cannot close a newly opened editor',async t=>{
 const app=await loadApp();t.after(app.close);
 let resolveSave;app.router.post('/api/schedule-jobs',call=>new Promise(resolve=>{resolveSave=()=>resolve({...call.body,id:'old-save'})}));
 await app.evaluate("openScheduleJobModal('assistant')");
 const form=app.document.querySelector('#scheduleJobForm');form.elements.name.value='旧任务';form.elements.assistantPrompt.value='old';
 form.dispatchEvent(new app.window.Event('submit',{bubbles:true,cancelable:true}));await settle();
 app.evaluate('closeScheduleJobModal()');await app.evaluate("openScheduleJobModal('assistant')");
 form.elements.name.value='正在编辑的新任务';form.elements.assistantPrompt.value='new';resolveSave();await settle(4);
 assert.equal(app.document.querySelector('#scheduleModal').classList.contains('open'),true);
 assert.equal(form.elements.name.value,'正在编辑的新任务');assert.equal(form.elements.assistantPrompt.value,'new');
 assert.equal(form.querySelector('[type=submit]').disabled,false);
});
