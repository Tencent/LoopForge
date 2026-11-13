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
    id: 'eval-1', status: 'completed', score: 88, summary: 'Strong result',
    recommendations: [{ id: 'rec-1', title: 'Add regression test', type: 'workflow', applied: false }],
  });

  app.evaluate(`state.data.runs=[${JSON.stringify(run)}];state.selectedRun='run-1';state.activeRun=state.data.runs[0]`);
  app.evaluate(`switchView('runs');showRunDetail('run-1')`);
  await settle(8);
  await app.evaluate(`loadRunLogs('run-1','node-1')`);
  await app.evaluate(`loadRunTelemetry('run-1','node-1')`);
  await app.evaluate(`loadRunPreview('run-1',{})`);
  await app.evaluate(`loadRunWorkspace('run-1',true)`);
  await app.evaluate(`loadRunEval('run-1',false)`);
  app.evaluate(`renderRunDetail(state.activeRun);renderRunOverview();refreshChildRuns(state.activeRun)`);
  for (const tab of app.document.querySelectorAll('#runsView [role="tab"], #runsView .run-tab')) {
    tab.click();
    await settle(2);
  }
  assert.match(app.document.querySelector('#runsView').textContent, /Implemented|Strong result|Report|2400|2\.4k/i);
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

test('conversation workspaces stay out of SOP run lists and counts but keep their requirement', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const out = JSON.parse(app.evaluate(`
    const pid=state.projectFilter||state.data.projects[0].id;
    state.data.issues=[...(state.data.issues||[]),{id:'iss-conv',projectId:pid,number:42,title:'修复登录页文案',workflowId:state.data.workflows[0].id}];
    state.data.runs=[
      {id:'sop-1',workflowId:'wf-a',status:'succeeded',input:'SOP run',createdAt:new Date(Date.now()-60000).toISOString(),executionContext:{projectId:pid},nodes:{a:{status:'succeeded'}},workflowSnapshot:{name:'SOP A'}},
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
  // The requirement list is unchanged: the linked workspace still shows as its SOP.
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

test('a parent node only animates while its child SOP is executing', async (t) => {
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
