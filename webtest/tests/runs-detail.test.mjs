import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('run detail loads logs, telemetry, preview, workspace, evaluation, and child runs', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const run = {
    ...app.evaluate('structuredClone(state.data.runs[0])'), status: 'succeeded',
    finishedAt: '2026-10-21T10:05:00Z', artifactCount: 1,
    usage: { totalTokens: 2400, inputTokens: 1600, outputTokens: 800, costUsd: 0.24 },
    workflowSnapshot: app.evaluate('structuredClone(state.data.workflows[0])'),
    nodes: {
      'node-1': { status: 'succeeded', attempt: 2, output: 'Implemented', startedAt: '2026-10-21T10:00:00Z',
        finishedAt: '2026-10-21T10:03:00Z', usage: { totalTokens: 1800 } },
      'node-2': { status: 'succeeded', attempt: 1, output: 'Approved', startedAt: '2026-10-21T10:03:00Z',
        finishedAt: '2026-10-21T10:05:00Z', usage: { totalTokens: 600 } },
    },
    artifacts: [{ id: 'artifact-1', name: 'Report', path: 'report.md', mediaType: 'text/markdown' }],
    childRuns: [{ id: 'run-child', status: 'succeeded', workflowName: 'Child flow' }],
  };
  app.router.get('/api/runs/run-1', run);
  app.router.get(/^\/api\/runs\/run-1\/logs\?/, {
    logs: [
      { sequence: 1, nodeId: 'node-1', level: 'info', message: 'Started', timestamp: '2026-10-21T10:00:00Z' },
      { sequence: 2, nodeId: 'node-1', level: 'error', message: 'Recovered', timestamp: '2026-10-21T10:01:00Z' },
    ], hasMore: false,
  });
  app.router.get(/^\/api\/runs\/run-1\/telemetry\?/, {
    events: [
      { sequence: 1, nodeId: 'node-1', category: 'reasoning', message: 'Thinking' },
      { sequence: 2, nodeId: 'node-1', category: 'tool', phase: 'finish', message: 'Tests' },
    ], hasMore: false,
  });
  app.router.get('/api/runs/run-1/preview', {
    available: true, url: 'http://127.0.0.1/preview/run-1/', backendUrl: 'http://127.0.0.1:8080',
    files: [{ path: 'index.html', size: 1200 }],
  });
  app.router.post('/api/runs/run-1/preview/session', { id: 'preview-1', url: 'http://127.0.0.1/preview/run-1/' });
  app.router.get('/api/runs/run-1/workspace', {
    available: true, path: '/workspace/atlas', repository: 'atlas-web',
    changes: [{ path: 'web/app.js', index: 'M', worktree: 'M' }],
  });
  app.router.get('/api/runs/run-1/eval', {
    id: 'eval-1', status: 'succeeded', score: 88, passed: true,
    result: { summary: 'Strong result', recommendations: [{ id: 'rec-1', title: 'Add regression test' }] },
  });

  app.evaluate(`state.data.runs=[${JSON.stringify(run)}];state.selectedRun='run-1';state.activeRun=state.data.runs[0]`);
  app.evaluate(`switchView('runs');showRunDetail('run-1')`);
  await settle(8);
  await app.evaluate(`loadRunLogs('run-1','node-1')`);
  await app.evaluate(`loadRunTelemetry('run-1','node-1')`);
  await app.evaluate(`loadRunPreview('run-1',{})`);
  await app.evaluate(`loadRunWorkspace('run-1',{refresh:true})`);
  await app.evaluate(`loadRunEval('run-1',false)`);
  app.evaluate(`renderRunDetail(state.activeRun);renderRunOverview();refreshChildRuns(state.activeRun)`);
  for (const tab of app.document.querySelectorAll('#runsView [role="tab"], #runsView .run-tab')) {
    tab.click();
    await settle(2);
  }
  assert.match(app.document.querySelector('#runsView').textContent, /Implemented|Strong result|Report|2400|2\.4k/i);
});

for (const entry of ['showRunDetail', 'applyRunPayload']) {
  test(`${entry} shows the failure reason for an unbound auto-assigned run`, async (t) => {
    const app = await loadApp();
    t.after(app.close);
    const workflow = app.evaluate('structuredClone(state.data.workflows[0])');
    const reason = '兼容的项目执行机器已离线超过 2 分钟，请启动执行机器后重试';
    const run = {
      id: 'run-1', workflowId: workflow.id, workflowSnapshot: workflow,
      status: 'failed', admissionState: 'unstarted', revision: 2,
      createdAt: '2026-11-25T10:00:00Z', queueEnteredAt: '2026-11-25T10:00:00Z', updatedAt: '2026-11-25T10:03:01Z',
      // The API omits executionMode when the run is unbound.
      executionContext: { projectId: workflow.projectId, executionPool: 'project-auto' },
      nodes: Object.fromEntries(workflow.nodes.map(({ id }) => [id, { nodeId: id, status: 'pending', attempt: 0 }])),
      events: [{ id: 'adm-2', runId: 'run-1', type: 'run.failed', message: reason, timestamp: '2026-11-25T10:03:01Z' }],
    };
    const live = entry === 'applyRunPayload';
    const initial = live ? { ...run, status: 'queued', admissionState: 'queued', revision: 1, events: null } : run;
    const summary = { ...initial, workflowSnapshot: { id: workflow.id, name: workflow.name, nodes: null }, events: null };
    app.router.get('/api/runs/run-1', initial);
    app.evaluate(`state.data.runs=[${JSON.stringify(summary)}];switchView('runs');showRunDetail('run-1')`);
    await settle(8);

    if (live) {
      const stream = app.evaluate('state.runStream');
      const update = { ...run, executionContext: { ...run.executionContext, executionMode: '' } };
      stream.emit({ run: update, telemetryDelta: [], logDelta: [] }, 'run');
    }
    const callout = app.document.querySelector('#runDetail .run-detail-header + .approval-callout');
    assert.equal(callout?.querySelector('b')?.textContent, '自动分配失败');
    assert.equal(callout.querySelector('p').textContent, reason);
    assert.equal(callout.closest('[hidden]'), null);
  });
}

test('live run redraws keep the current IDE document until the workspace is recycled', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const run = {
    ...app.evaluate('structuredClone(state.data.runs[0])'),
    status: 'running',
    workflowSnapshot: {
      ...app.evaluate('structuredClone(state.data.workflows[0])'),
      autoSelectSandbox: true, sandboxProfileId: 'project-toolchain',
    },
    nodes: {
      'node-1': { status: 'succeeded', gateResult: { decision: 'pass' } },
      'node-2': { status: 'running' },
    },
  };
  const workspacePath = '/api/runs/run-1/workspace';
  app.router.get('/api/runs/run-1', run);
  app.router.get(workspacePath, { available: true, readOnly: false });
  app.evaluate('state.data.runs=[' + JSON.stringify(run) + "];switchView('runs');showRunDetail('run-1')");
  await settle(8);
  const detail = app.document.querySelector('#runDetail');
  detail.querySelector('[data-run-node="node-1"]').click();
  detail.querySelector('[data-run-tab="workspace"]').click();
  await settle(8);

  const header = detail.querySelector('.run-detail-header');
  const frame = detail.querySelector('[data-vscode-frame]');
  const frameWindow = frame.contentWindow;
  assert.ok(frameWindow);
  const stream = app.evaluate('state.runStream');
  const sandbox = header.querySelector('[data-run-sandbox]');
  assert.equal(sandbox.title, 'project-toolchain');
  const events = [{ type: 'sandbox.selected', message: 'Agent 选择沙箱 project-toolchain' }];
  stream.emit({ run: { ...run, revision: 2, events } }, 'run');
  assert.equal(detail.querySelector('.run-detail-header'), header, 'a passed gate needs no prompt');
  assert.equal(sandbox.title, events[0].message);

  const withOutput = {
    ...run,
    revision: 3,
    nodes: {
      ...run.nodes,
      'node-1': { ...run.nodes['node-1'], output: 'New result' },
    },
  };
  stream.emit({ run: withOutput }, 'run');
  assert.notEqual(detail.querySelector('.run-detail-header'), header, 'new output redraws details');
  assert.equal(detail.querySelector('.node-output pre').textContent, 'New result');
  assert.equal(detail.querySelector('[data-vscode-frame]'), frame);
  assert.ok(frame.contentWindow === frameWindow, 'detail redraw keeps the IDE browsing context');

  app.evaluate('renderWorkspacePane(state.activeRun)');
  assert.ok(frame.contentWindow === frameWindow, 'workspace render keeps the IDE browsing context');

  app.router.get(workspacePath, { available: false, reason: '工作区已回收' });
  stream.emit({ run: { ...withOutput, revision: 4, workspaceCleanedAt: '2026-11-23T08:00:00Z' } }, 'run');
  await settle(8);
  assert.equal(detail.querySelector('[data-vscode-frame]'), null);
  assert.match(detail.querySelector('[data-run-pane="workspace"]').textContent, /工作区已回收/);
});

test('reopening a completed child workspace checks the owner and keeps a healthy IDE', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const run = {
    ...app.evaluate('structuredClone(state.data.runs[0])'),
    status: 'succeeded',
    executionContext: { projectId: 'p-1', parentRunId: 'parent', workspaceRunId: 'parent' },
  };
  const workspacePath = '/api/runs/run-1/workspace';
  const restorePath = workspacePath + '/restore';
  const active = { available: true, readOnly: true };
  let info = active;
  app.router.get('/api/runs/run-1', run);
  app.router.get(workspacePath, () => info);
  app.evaluate('state.data.runs=[' + JSON.stringify(run) + "];switchView('runs');showRunDetail('run-1')");
  await settle(8);
  const detail = app.document.querySelector('#runDetail');
  const overview = detail.querySelector('[data-run-tab="overview"]');
  const workspace = detail.querySelector('[data-run-tab="workspace"]');
  const frame = () => detail.querySelector('[data-vscode-frame]');
  workspace.click();
  await settle();
  const initialFrame = frame();
  assert.ok(initialFrame);
  const initialWindow = initialFrame.contentWindow;

  overview.click();
  let finishRefresh;
  info = new Promise((resolve) => { finishRefresh = resolve; });
  workspace.click();
  assert.equal(app.router.calls.filter((call) => call.path === workspacePath).length, 2);
  assert.equal(frame().contentWindow, initialWindow);
  finishRefresh(active);
  await settle();
  assert.equal(frame(), initialFrame);
  assert.equal(frame().contentWindow, initialWindow);

  overview.click();
  info = { available: false, restorable: true, reason: '工作区已回收' };
  let finishRestore;
  app.router.post(restorePath, () => new Promise((resolve) => { finishRestore = resolve; }));
  workspace.click();
  await settle();
  assert.equal(frame(), null);
  assert.match(detail.querySelector('[data-run-pane="workspace"]').textContent, /正在恢复工作区/);
  assert.equal(app.router.calls.filter((call) => call.path === restorePath).length, 1);
  info = active;
  finishRestore({ restored: true });
  await settle(8);
  assert.ok(frame());

  overview.click();
  info = { available: false, restorable: false, reason: '远程执行机器当前离线或工作区通道不可用' };
  workspace.click();
  await settle();
  assert.equal(frame(), null);
  assert.match(detail.querySelector('[data-run-pane="workspace"]').textContent, /远程执行机器当前离线/);
  assert.equal(detail.querySelector('[data-workspace-restore]').hidden, true);
  assert.equal(app.router.calls.filter((call) => call.path === restorePath).length, 1);
});

test('run detail node topology renders an icon glyph instead of undefined', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const workflow = app.evaluate('structuredClone(state.data.workflows[0])');
  const nodes = Object.fromEntries((workflow.nodes || []).map((node) => [node.id, { status: 'succeeded', attempt: 1 }]));
  const run = {
    ...app.evaluate('structuredClone(state.data.runs[0])'), status: 'succeeded', finishedAt: '2026-10-21T10:05:00Z',
    workflowSnapshot: workflow, nodes,
  };
  app.router.get('/api/runs/run-1', run);
  app.evaluate(`state.data.runs=[${JSON.stringify(run)}];state.selectedRun='run-1';state.activeRun=state.data.runs[0]`);
  app.evaluate(`switchView('runs');showRunDetail('run-1')`);
  await settle(6);
  const graph = app.document.querySelector('#runsView .run-graph');
  assert.ok(graph, 'run detail should render the node topology');
  const icons = [...graph.querySelectorAll('.graph-node .node-icon')];
  assert.equal(icons.length, (workflow.nodes || []).length, 'each topology node should have one icon slot');
  for (const icon of icons) {
    assert.equal(icon.textContent.trim(), '', 'node icon must not render stray text');
    assert.ok(icon.querySelector('svg'), 'node icon should render an svg glyph');
  }
  assert.doesNotMatch(graph.textContent, /undefined/);
});

test('run filtering, paging, status actions, and list/detail transitions remain interactive', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.data.runs=Array.from({length:28},(_,i)=>({...state.data.runs[0],id:'run-'+(i+1),
      status:['running','failed','succeeded','queued'][i%4],createdAt:new Date(Date.now()-i*60000).toISOString()}));
    state.runFilter='all';state.runQuery='';renderRuns();
  `);
  app.document.querySelector('[data-page-key="runs"][data-page="2"]')?.click();
  for (const button of app.document.querySelectorAll('#runsView [data-run-filter], #runsView .run-filter')) button.click();
  app.evaluate(`showRunList();showRunDetail('run-2');showRunList();renderRunFilterCounts(state.data.runs)`);
  assert.match(app.document.querySelector('#runsView').textContent, /共 28 条|运行记录/);
});

test('conversation workspaces stay out of LOOP run lists and counts but keep their requirement', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const out = JSON.parse(app.evaluate(`
    const pid=state.projectFilter||state.data.projects[0].id;
    state.data.issues=[...(state.data.issues||[]),{id:'iss-conv',projectId:pid,number:42,title:'修复登录页文案',workflowId:state.data.workflows[0].id}];
    state.data.runs=[
      {id:'sop-1',workflowId:'wf-a',status:'succeeded',input:'LOOP run',createdAt:new Date(Date.now()-60000).toISOString(),executionContext:{projectId:pid},nodes:{a:{status:'succeeded'}},workflowSnapshot:{name:'LOOP A'}},
      {id:'ws-linked',kind:'workspace',workflowId:'conversation-workspace',status:'waiting',input:'对话改代码：改登录文案',createdAt:new Date().toISOString(),executionContext:{projectId:pid,issueId:'iss-conv'},usage:{totalTokens:999},workflowSnapshot:{id:'conversation-workspace',name:'对话工作区'}},
      {id:'ws-review',kind:'workspace',workflowId:'conversation-workspace',status:'waiting',input:'对话改代码：只读评审 MR 546',createdAt:new Date().toISOString(),workspaceCleanedAt:new Date().toISOString(),executionContext:{projectId:pid},workflowSnapshot:{name:'对话工作区'}}
    ];
    state.runFilter='all';state.runQuery='';state.runWorkflowFilter='';renderRuns();
    const ids=[...document.querySelectorAll('#runList .run-card')].map(el=>el.dataset.id);
    const total=document.querySelector('#runPager .run-total').textContent;
    const overview=document.querySelector('#runOverview').textContent;
    state.runQuery='对话改代码';renderRuns();
    const searched=document.querySelectorAll('#runList .run-card').length;
    state.runQuery='';
    const issue=state.data.issues.find(i=>i.id==='iss-conv');
    const box=document.createElement('div');box.innerHTML=issueRow(issue);
    const row=[...box.querySelectorAll('.issue-table-row > *')].map(c=>c.textContent.trim());
    JSON.stringify({ids,total,overview,searched,live:liveRuns().map(r=>r.id),latest:latestIssueRun(issue,null)?.id||null,sop:issueSOPLabel(issue),row,palette:commandCatalog().filter(x=>x.kind==='运行').length})
  `));
  assert.deepEqual(out.ids, ['sop-1']);
  assert.match(out.total, /共 1 条/);
  assert.doesNotMatch(out.overview, /999/);
  assert.equal(out.searched, 0);
  assert.deepEqual(out.live, []);
  // The requirement list is unchanged: the linked workspace still shows as its LOOP.
  assert.equal(out.latest, 'ws-linked');
  assert.equal(out.sop, '对话工作区');
  assert.ok(out.row.includes('对话工作区'), JSON.stringify(out.row));
  assert.equal(out.palette, 1);
  // A direct link from the conversation still opens the workspace detail.
  const detail = app.evaluate(`showRunDetail('ws-review');document.querySelector('#runDetail')?.textContent||''`);
  assert.match(detail, /对话工作区/);
});

test('run detail topology shows each stage time window beside its stage label', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const workflow = app.evaluate('structuredClone(state.data.workflows[0])');
  const nodes = Object.fromEntries((workflow.nodes || []).map((node, i) => [node.id, {
    status: 'succeeded', attempt: 1,
    startedAt: `2026-10-21T10:0${i}:00Z`, finishedAt: `2026-10-21T10:0${i + 3}:00Z`,
  }]));
  const run = {
    ...app.evaluate('structuredClone(state.data.runs[0])'), status: 'succeeded',
    finishedAt: '2026-10-21T10:09:00Z', workflowSnapshot: workflow, nodes,
  };
  app.router.get('/api/runs/run-1', run);
  app.evaluate(`state.data.runs=[${JSON.stringify(run)}];state.selectedRun='run-1';state.activeRun=state.data.runs[0]`);
  app.evaluate(`switchView('runs');showRunDetail('run-1')`);
  await settle(6);

  const labels = [...app.document.querySelectorAll('#runsView .graph-stage-label')];
  const stageIds = app.evaluate(`workflowStages(${JSON.stringify(workflow)}).map(stage=>stage.map(node=>node.id))`);
  assert.equal(labels.length, stageIds.length, 'every stage should keep its label');
  stageIds.forEach((ids, index) => {
    const label = labels[index];
    assert.match(label.querySelector('.graph-stage-name').textContent, /^\u9636\u6bb5 \d/);
    const starts = ids.map((id) => new Date(nodes[id].startedAt).getTime());
    const ends = ids.map((id) => new Date(nodes[id].finishedAt).getTime());
    const expected = `${new Date(Math.min(...starts)).toLocaleTimeString()} \u2192 ${new Date(Math.max(...ends)).toLocaleTimeString()}`;
    assert.equal(label.querySelector('.graph-stage-time').textContent, expected,
      'stage label should show the stage start and finish time');
  });

  const runtime = app.document.querySelector('#runsView .node-inspector .runtime-details');
  assert.ok(runtime, 'node inspector should keep the runtime details row');
  assert.doesNotMatch(runtime.textContent, /\u2192/, 'time window is no longer repeated under the node output');
});

test('a stage that has not started or finished hides the zero default time', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const workflow = app.evaluate('structuredClone(state.data.workflows[0])');
  const [first, second] = workflow.nodes;
  const nodes = {
    [first.id]: { status: 'pending', startedAt: '0001-01-01T00:00:00Z', finishedAt: '0001-01-01T00:00:00Z' },
    [second.id]: { status: 'running', startedAt: '2026-10-21T10:04:00Z', finishedAt: '0001-01-01T00:00:00Z' },
  };
  const run = {
    ...app.evaluate('structuredClone(state.data.runs[0])'), status: 'running',
    workflowSnapshot: workflow, nodes,
  };
  app.router.get('/api/runs/run-1', run);
  app.evaluate(`state.data.runs=[${JSON.stringify(run)}];state.selectedRun='run-1';state.activeRun=state.data.runs[0]`);
  app.evaluate(`switchView('runs');showRunDetail('run-1')`);
  await settle(6);
  const labels = [...app.document.querySelectorAll('#runsView .graph-stage-label')];
  const times = labels.map((label) => label.querySelector('.graph-stage-time')?.textContent || '');
  assert.ok(times.some((text) => text === ''), 'an unstarted stage has no duration');
  assert.ok(times.some((text) => text.includes('现在') && !text.includes('0001')), `an open stage shows now, not year 1: ${times.join(' | ')}`);
  assert.ok(times.every((text) => !text.includes('0001')), times.join(' | '));
});

/** 渲染正在执行的节点，保留真实详情页与实时更新入口。 */
function renderActivityFixture(app) {
  app.evaluate(`
    state.activeRun={...state.data.runs[0],status:'running',pauseRequested:false,cancelRequested:false,
      workflowSnapshot:structuredClone(state.data.workflows[0]),artifacts:[],
      nodes:{'node-1':{status:'running',attempt:1,activity:'正在分析需求'},'node-2':{status:'pending'}}};
    state.selectedRun=state.activeRun.id;state.selectedRunNode='node-1';
    renderRunDetail(state.activeRun);
  `);
}

/** 同时检查活动文字与节点状态的动态反馈。 */
function assertInspectorMotion(app, active, message) {
  for (const selector of ['.node-activity-inline', '.node-inspector-status']) {
    const element = app.document.querySelector(`#runDetail ${selector}`);
    assert.ok(element, selector);
    assert.equal(element.classList.contains('is-running'), active, `${message}: ${selector}`);
  }
}

test('running node activity keeps its DOM and motion across streamed text updates', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  renderActivityFixture(app);
  assertInspectorMotion(app, true, 'initial running node');
  const activity = app.document.querySelector('#runDetail .node-activity-inline');
  const text = activity.querySelector('b');
  const status = app.document.querySelector('#runDetail .node-inspector-status');
  assert.equal(activity.hidden, false);
  app.evaluate(`startLiveRun(state.activeRun.id)`);
  const stream = app.evaluate('state.runStream');
  const next = '正在读取 <component> & 接口规范';
  stream.emit({ run: { id: 'run-1', status: 'running', nodes: { 'node-1': { activity: next } } } }, 'run');
  assert.equal(app.document.querySelector('#runDetail .node-activity-inline'), activity);
  assert.equal(activity.querySelector('b'), text, 'streamed text should not replace the animated element');
  assert.equal(app.document.querySelector('#runDetail .node-inspector-status'), status);
  assert.equal(text.textContent, next);
  assert.equal(text.children.length, 0, 'activity stays escaped text');
  assertInspectorMotion(app, true, 'streamed activity');
  stream.emit({ run: { id: 'run-1', status: 'running', nodes: {
    'node-1': { status: 'succeeded' }, 'node-2': { status: 'running' },
  } } }, 'run');
  assertInspectorMotion(app, false, 'selected completed node while another node runs');
  assert.equal(activity.querySelector('b').textContent, next, 'completed activity remains readable');
});

test('terminal, human-waiting, paused and cancelling runs stop inspector motion', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  for (const [label, runPatch, nodeStatus] of [
    ['completed', { status: 'succeeded' }, 'succeeded'],
    ['failed', { status: 'failed' }, 'failed'],
    ['cancelled', { status: 'cancelled' }, 'cancelled'],
    ['human approval', { status: 'waiting' }, 'waiting_approval'],
    ['paused', { status: 'paused' }, 'running'],
    ['pause requested', { pauseRequested: true }, 'running'],
    ['cancel requested', { cancelRequested: true }, 'running'],
  ]) {
    renderActivityFixture(app);
    app.evaluate(`Object.assign(state.activeRun,${JSON.stringify(runPatch)});
      state.activeRun.nodes['node-1'].status=${JSON.stringify(nodeStatus)};patchRunLive(state.activeRun)`);
    assertInspectorMotion(app, false, label);
    assert.equal(app.document.querySelector('#runDetail .node-activity-inline b').textContent, '正在分析需求', label);
  }
});

test('a parent node only animates while its child LOOP is executing', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  renderActivityFixture(app);
  app.evaluate(`
    state.activeRun.nodes['node-1'].childRunId='child-activity';
    state.childRuns={'child-activity':{at:Date.now(),run:{id:'child-activity',status:'waiting',
      workflowSnapshot:{nodes:[{id:'review',name:'Review'}]},nodes:{review:{status:'waiting_approval'}}}}};
    renderRunDetail(state.activeRun);
  `);
  assertInspectorMotion(app, false, 'child waits for human approval on first render');
  app.evaluate(`state.childRuns['child-activity'].run.status='running';
    state.childRuns['child-activity'].run.nodes.review.status='running';patchRunLive(state.activeRun)`);
  assertInspectorMotion(app, true, 'child resumes execution');
  app.evaluate(`state.childRuns['child-activity'].run.nodes.review.status='waiting_approval';patchRunLive(state.activeRun)`);
  assertInspectorMotion(app, false, 'child approval node overrides its stale running status');
});

test('child pause and cancellation polling refreshes motion without parent stream events', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  renderActivityFixture(app);
  const child = { id: 'child-activity', status: 'running', pauseRequested: false, cancelRequested: false,
    workflowSnapshot: { nodes: [{ id: 'work', name: 'Work' }] }, nodes: { work: { status: 'running' } } };
  app.evaluate(`state.activeRun.nodes['node-1'].childRunId='child-activity';
    state.childRuns={'child-activity':{at:Date.now(),run:${JSON.stringify(child)}}};renderRunDetail(state.activeRun)`);
  assertInspectorMotion(app, true, 'initial child execution');
  for (const flag of ['pauseRequested', 'cancelRequested']) {
    for (const requested of [true, false]) {
      app.router.get('/api/runs/child-activity', { ...child, [flag]: requested });
      app.evaluate(`state.childRuns['child-activity'].at=0;refreshChildRuns(state.activeRun)`);
      await settle(3);
      assert.equal(app.evaluate(`state.childRuns['child-activity'].run.${flag}`), requested);
      assertInspectorMotion(app, !requested, `${flag}=${requested} from child polling`);
    }
  }
});

test('run detail and artifact preview display strict two-layer knowledge citations', async (t) => {
  const app = await loadApp();
  t.after(app.close);

  const workflow = {
    ...app.evaluate('structuredClone(state.data.workflows[0])'),
    nodes: [{ id: 'node-1', name: '方案设计', type: 'agent' }],
  };
  const knowledgeReads = [
    {
      id: 'kr-cat',
      serverId: 'agentflow-knowledge',
      tool: 'list_documents',
      status: '命中',
      attempt: 1,
      evidences: [{ id: 'ev_cat_1', documentId: 'doc-cat', title: '目录文档不应混入.md' }],
    },
    {
      id: 'kr-search',
      serverId: 'agentflow-knowledge',
      tool: 'search_knowledge',
      query: '网关端口',
      status: '命中',
      attempt: 1,
      cited: true,
      evidences: [
        {
          id: 'ev_aaa_1',
          sourceId: 'wk::kb1',
          sourceName: '研发规范库',
          documentId: 'wk::doc1',
          title: '部署规范.md',
          locator: '#chunk-2',
          cited: true,
        },
        {
          id: 'ev_aaa_2',
          sourceId: 'wk::kb1',
          sourceName: '研发规范库',
          documentId: 'wk::doc2',
          title: '排障手册.md',
          locator: '#chunk-0',
          cited: false,
        },
      ],
    },
    {
      id: 'kr-read',
      serverId: 'agentflow-knowledge',
      tool: 'read_document',
      query: 'wk::doc3',
      status: '命中',
      attempt: 1,
      cited: false,
      evidences: [
        {
          id: 'ev_bbb_1',
          sourceId: 'wk::kb1',
          sourceName: '研发规范库',
          documentId: 'wk::doc3',
          title: '发布流程.md',
          locator: '#chunk-0..#chunk-2',
          cited: false,
        },
      ],
    },
  ];
  const run = {
    ...app.evaluate('structuredClone(state.data.runs[0])'),
    id: 'run-kb-1',
    status: 'succeeded',
    finishedAt: '2026-10-21T10:05:00Z',
    workflowSnapshot: workflow,
    nodes: {
      'node-1': {
        status: 'succeeded',
        attempt: 1,
        output: '采用默认端口 8080 [[knowledge:ev_aaa_1]]。',
        knowledgeReads,
      },
    },
    artifacts: [
      {
        id: 'art-kb-1',
        nodeId: 'node-1',
        attempt: 1,
        name: 'report.md',
        path: 'report.md',
        mediaType: 'text/markdown',
        size: 512,
        sha256: '1234567890abcdef',
        content: '# 总结\n\n端口见规范 [[knowledge:ev_aaa_1]]，示例代码 `[[knowledge:ev_aaa_1]]`。\n',
      },
    ],
  };

  app.router.get('/api/runs/run-kb-1', run);
  app.router.get('/api/runs/run-kb-1/artifacts/art-kb-1', run.artifacts[0].content);
  app.evaluate(`state.data.runs=[${JSON.stringify(run)}];state.selectedRun='run-kb-1';state.activeRun=state.data.runs[0]`);
  app.evaluate(`switchView('runs');showRunDetail('run-kb-1')`);
  await settle(6);

  const kbCard = app.document.querySelector('#runDetail .run-kb-card');
  assert.ok(kbCard, 'node inspector should render .run-kb-card');
  assert.match(kbCard.textContent, /实际使用 1 篇 · 检索读取 3 篇/);
  assert.match(kbCard.textContent, /部署规范\.md/);
  assert.match(kbCard.textContent, /发布流程\.md/);
  assert.match(kbCard.textContent, /排障手册\.md/);
  assert.match(kbCard.textContent, /\[1\] 已用于回答/);
  assert.match(kbCard.textContent, /已读正文 · 未引用/);
  assert.match(kbCard.textContent, /仅检索未采用/);
  assert.doesNotMatch(kbCard.textContent, /目录文档不应混入\.md/);
  assert.match(kbCard.querySelector('.run-kb-trace').textContent, /list_documents/);

  const outputPre = app.document.querySelector('#runDetail .node-output pre');
  assert.ok(outputPre, 'node output should render');
  assert.doesNotMatch(outputPre.textContent, /\[\[knowledge:/);
  const outCite = outputPre.querySelector('[data-kb-cite="1"]');
  assert.ok(outCite, 'node output should render numbered superscript [1]');
  assert.equal(outCite.textContent, '1');
  assert.equal(app.evaluate(`inspectorNeedsRefresh(document.querySelector('#runDetail'), state.activeRun)`), false);

  outCite.click();
  assert.ok(kbCard.querySelector('[data-kb-doc-index="1"]').classList.contains('is-highlighted'));

  await app.evaluate(`previewArtifact(state.activeRun, 'art-kb-1')`);
  await settle(3);
  const modal = app.document.querySelector('#artifactPreview');
  const mdBody = modal.querySelector('.markdown-body');
  assert.equal(mdBody.querySelector('code').textContent, '[[knowledge:ev_aaa_1]]');
  assert.doesNotMatch(mdBody.querySelector('p').textContent.replace(mdBody.querySelector('code').textContent, ''), /\[\[knowledge:/);
  assert.ok(mdBody.querySelector('[data-kb-cite="1"]'), 'artifact preview should render numbered superscript [1]');
  const artFooter = modal.querySelector('.run-kb-artifact-cites');
  assert.ok(artFooter, 'artifact preview should render citation footer');
  assert.match(artFooter.textContent, /实际使用 1 篇 · 检索读取 3 篇/);
  assert.match(artFooter.textContent, /部署规范\.md/);
});

test('run detail knowledge citations handle stripped artifacts, attempt isolation, copy cleanup, links, and English i18n', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const { readFile } = await import('node:fs/promises');
  const path = await import('node:path');
  const vm = await import('node:vm');
  const { WEB_ROOT } = await import('../lib/load-app.mjs');

  const workflow = app.evaluate('structuredClone(state.data.workflows[0])');
  const firstNodeId = workflow.nodes[0].id;
  const run = {
    ...app.evaluate('structuredClone(state.data.runs[0])'),
    id: 'run-kb-edges',
    status: 'succeeded',
    workflowSnapshot: workflow,
    nodes: {
      [firstNodeId]: {
        status: 'succeeded',
        attempt: 1,
        output: '结论 [链接](https://example.com/spec?x=1) 见规范 [[knowledge:ev_aaa_1]]，示例代码 `[[knowledge:ev_aaa_1]]`。',
        knowledgeReads: [
          {
            id: 'kr-1',
            attempt: 1,
            serverId: 'agentflow-knowledge',
            tool: 'search_knowledge',
            status: '命中',
            cited: true,
            evidences: [
              {
                id: 'ev_aaa_1',
                sourceId: 'wk::kb1',
                sourceName: '研发规范库',
                documentId: 'wk::doc1',
                title: '部署规范.md',
                locator: '#chunk-2',
                cited: true,
                citedIn: ['节点输出', 'artifacts/report.md'],
              },
            ],
          },
        ],
      },
      [workflow.nodes[1]?.id || 'node-2']: {
        status: 'succeeded',
        attempt: 2,
        output: '重试无检索',
        knowledgeReads: [
          { id: 'kr-stale', attempt: 1, serverId: 'agentflow-knowledge', tool: 'search_knowledge', status: '命中', evidences: [{ id: 'ev_stale', title: '旧重试.md' }] },
        ],
      },
    },
    artifacts: [
      {
        id: 'art-stripped',
        nodeId: firstNodeId,
        attempt: 1,
        name: 'report.md',
        path: 'artifacts/report.md',
        mediaType: 'text/markdown',
        size: 512,
        sha256: '1234567890abcdef',
      },
    ],
  };

  let copied = [];
  Object.defineProperty(app.window.navigator, 'clipboard', {
    configurable: true,
    value: {
      writeText: async (txt) => {
        copied.push(txt);
      },
    },
  });
  Object.defineProperty(app.window, 'isSecureContext', { configurable: true, value: true });

  app.router.get('/api/runs/run-kb-edges', run);
  app.router.get(
    '/api/runs/run-kb-edges/artifacts/art-stripped',
    '# 交付\n\n[链接 [[knowledge:ev_aaa_1]]](https://example.com) 与正文 [[knowledge:ev_aaa_1]]。\n\n```md\n[[knowledge:ev_aaa_1]]\n```',
  );
  app.evaluate(`state.data.runs=[${JSON.stringify(run)}];state.selectedRun='run-kb-edges';state.activeRun=state.data.runs[0]`);
  app.evaluate(`switchView('runs');showRunDetail('run-kb-edges')`);
  await settle(6);

  const kbCard = app.document.querySelector('#runDetail .run-kb-card');
  assert.ok(kbCard, 'node 1 should render .run-kb-card');
  assert.match(kbCard.textContent, /引用位置：/);
  assert.match(kbCard.textContent, /artifacts\/report\.md/);

  // Clicking [data-copy-output] copies formatted citation [1] + References block cleanly.
  app.document.querySelector('#runDetail [data-copy-output]')?.click();
  await settle(2);
  assert.ok(copied.at(-1)?.includes('见规范 [1]'), 'copied node output should convert citation marker to [1]');
  assert.ok(copied.at(-1)?.includes('参考来源:'), 'copied node output should include References');
  assert.ok(copied.at(-1)?.includes('`[[knowledge:ev_aaa_1]]`'), 'copied node output should preserve code marker');

  // Previewing artifact: no button inside <a>, preview copy converts [1], source copy keeps raw marker.
  await app.evaluate(`previewArtifact(state.activeRun, 'art-stripped')`);
  await settle(3);
  const modal = app.document.querySelector('#artifactPreview');
  assert.equal(modal.querySelectorAll('.markdown-body a button').length, 0, 'must not nest <button> inside <a>');
  modal.querySelector('[data-copy-artifact]')?.click();
  await settle(2);
  assert.ok(copied.at(-1)?.includes('与正文 [1]。'));
  modal.querySelector('[data-mode="source"]')?.click();
  await settle(2);
  modal.querySelector('[data-copy-artifact]')?.click();
  await settle(2);
  assert.ok(copied.at(-1)?.includes('与正文 [[knowledge:ev_aaa_1]]。'));

  // Attempt 2 node with zero attempt-2 reads must not leak attempt 1 reads.
  if (workflow.nodes[1]?.id) {
    app.evaluate(`selectRunNode(${JSON.stringify(workflow.nodes[1].id)})`);
    await settle(3);
    assert.equal(app.document.querySelector('#runDetail .run-kb-card'), null);
  }

  // Verify English i18n renders KB chrome cleanly while preserving data-i18n-data doc titles.
  app.evaluate(`selectRunNode(${JSON.stringify(firstNodeId)});renderRunDetail(state.activeRun)`);
  await settle(4);
  app.document.documentElement.dataset.lang = 'en';
  for (const file of ['i18n-en.js', 'i18n.js']) {
    new vm.Script(await readFile(path.join(WEB_ROOT, file), 'utf8'), { filename: file }).runInContext(
      app.dom.getInternalVMContext(),
    );
  }
  const enCard = app.document.querySelector('#runDetail .run-kb-card');
  assert.match(enCard.textContent, /Knowledge base sources/);
  assert.match(enCard.textContent, /1 doc/);
  assert.match(enCard.textContent, /部署规范\.md/, 'document title in data-i18n-data must stay intact');
  assert.match(enCard.textContent, /Used in answer/);
});
