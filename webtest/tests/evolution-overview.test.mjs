import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const now = new Date().toISOString();
const clusters = Array.from({ length: 12 }, (_, i) => ({
  id: `c-${i}`, label: `Error ${i}`, kind: 'node_failed', count: 20 - i, runs: 1,
  lastSeenAt: now, occurrences: [{ runId: 'same-run', createdAt: now }],
}));
const finding = { id: 'f', projectId: 'project-1', assetKind: 'agent', assetId: 'a', assetName: 'Reviewer', status: 'open', clusters, triage: { verdict: 'fix', score: 3 } };
const overview = { projectId: 'project-1', findings: [finding], health: [], pending: [{ id: 'proposal-1', status: 'pending' }], validating: [], trial: [], recent: [] };
const routes = [
  { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: overview },
  { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [finding] },
  { method: 'GET', path: '/api/evolution/findings/f/runs?cluster=c-11', body: { complete: true, total: 0, items: [] } },
];
async function open(app) { await app.evaluate(`state.projectFilter='project-1';switchView('evolution');renderEvolutionView()`); await settle(4); }
function filterTasks(app,key,value) {
  const filter=app.document.querySelector(`[data-evo-task-filter="${key}"]`);
  filter.value=value;
  filter.dispatchEvent(new app.window.Event('change',{bubbles:true}));
}

test('task details have a separate page and returning preserves pagination, filters and sorting', async t => {
  const assets = [finding, ...Array.from({ length: 11 }, (_, i) => ({
    ...finding, id: `f-${i + 1}`, assetId: `a-${i + 1}`, clusters: [clusters[i + 1]],
    ...(i===10?{ updatedAt:now, workflowIds:['flow-1'], triage:{...finding.triage,score:1} }:{}),
  }))];
  const app = await loadApp({ routes: [...routes,
    { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: assets },
    { method: 'GET', path: '/api/evolution/findings/f-11/runs?cluster=c-11', body: { complete: true, total: 0, items: [] } },
  ] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  assert.match($('[data-evo-asset-group="asset:agent:a"] .evo-home-problem').textContent, /Error 0.*Reviewer/s);
  assert.deepEqual([...app.document.querySelectorAll('.evo-workspace-stat h2')].map(el=>el.textContent), ['待处理问题','迭代中','已发布']);
  assert.equal($('[data-evo-list-section="tasks"]').getAttribute('aria-pressed'),'true');
  assert.equal(app.document.querySelectorAll('.evo-home-row').length, 10);
  assert.equal($('#evoDetailPage'), null);
  assert.ok($('[data-evo-task-search]'));
  assert.equal($('[data-evo-task-filter="sort"]').getAttribute('aria-label'), '改进任务排序');
  assert.equal($('#evolutionBody [role="tab"]'), null);
  const trend = app.evaluate('evoOverviewTrend(evoIssueRows())');
  assert.equal(trend.points.reduce((sum, p) => sum + p.value, 0), 1);
  $('[data-evo-unified-page="1"]').click();
  assert.equal(app.document.querySelectorAll('.evo-home-row').length, 3);
  $('[data-evo-asset-group="asset:agent:a-11"]').click(); await settle(4);
  assert.match(app.window.location.hash, /findings\/f-11\/c-11/);
  assert.ok($('#evolutionBody > #evoDetailPage'));
  assert.equal($('#evoValidationResults .evo-story-empty').textContent,'暂无');
  assert.equal($('#evoValidationResults table'),null);
  assert.equal($('#evoWorkspaceTasks'), null);
  assert.equal($('#evoPageHead').hidden, true);
  assert.equal($('#evoMetrics').hidden, true);
  $('[data-evo-list-back]').click(); await settle(5);
  assert.equal(app.window.location.hash, '#/evolution');
  assert.equal($('#evoDetailPage'), null);
  assert.equal($('#evoPageHead').hidden, false);
  assert.equal(app.document.querySelectorAll('.evo-home-row').length, 3);
  assert.equal(app.document.activeElement.dataset.evoAssetGroup, 'asset:agent:a-11');
  assert.equal(app.document.querySelectorAll('.evo-home-tabs button').length, 3);
  filterTasks(app,'sort','recent');
  assert.equal($('.evo-home-row').dataset.evoAssetGroup,'asset:agent:a-11');
  filterTasks(app,'status','active');
  filterTasks(app,'sop','flow-1');
  const search=$('[data-evo-task-search]');
  search.value='delivery flow';
  search.dispatchEvent(new app.window.Event('input',{bubbles:true}));
  assert.equal($('[data-evo-task-search]'),search);
  assert.equal(app.document.querySelectorAll('.evo-home-row').length,1);
  assert.match($('.evo-home-asset').textContent,/SOP · Delivery Flow/);
  $('.evo-home-row').click(); await settle(4);
  $('[data-evo-list-back]').click(); await settle(5);
  assert.equal($('[data-evo-task-filter="sort"]').value, 'recent');
  assert.equal($('[data-evo-task-filter="status"]').value, 'active');
  assert.equal($('[data-evo-task-filter="sop"]').value, 'flow-1');
  assert.equal($('[data-evo-task-search]').value,'delivery flow');
});

test('refresh does not diagnose and repeated diagnosis is guarded; legacy diagnosis is kept in details', async t => {
  const legacy = { ...finding, triage: null, clusters: [{ id: 'old', label: 'Old failure', count: 2, runs: 2 }] };
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [legacy] }] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  assert.equal($('.evo-overview-trend'), null);
  $('[data-evo-tab="assets"]').click();
  assert.match($('.evo-overview-trend').textContent, /暂无趋势数据/);
  assert.equal($('.evo-overview-trend > svg'), null);
  $('[data-evo-list-back]').click(); await settle(4);
  $('#evoRefresh').click(); await settle(4);
  assert.equal(app.router.calls.filter(c => c.method === 'POST' && c.path.endsWith('/diagnose')).length, 0);
  let resolve;
  app.router.post('/api/evolution/diagnose', () => new Promise(r => { resolve = r; }));
  $('#evoDiagnose').click(); await settle(2);
  $('#evoDiagnose').click();
  assert.equal(app.router.calls.filter(c => c.method === 'POST' && c.path.endsWith('/diagnose')).length, 1);
  assert.equal($('#evoDiagnose').disabled, true);
  resolve({ findings: [], runs: 3 }); await settle(5);
  assert.equal($('#evoDiagnose').disabled, false);
  $('[data-evo-asset-group="asset:agent:a"]').click(); await settle(3);
  assert.match($('#evoFindingTitle').textContent, /Old failure/);
  $('#evoSourceDetails').open = true;
  $('#evoDiagnosisDetails').open = true;
  assert.match($('#evoDiagnosisDetails').textContent, /旧版诊断/);
});

test('read errors expose retry without disguising failed requests as empty results', async t => {
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/overview?projectId=project-1', status: 503, body: { error: 'temporary failure' } }] }); t.after(app.close);
  await open(app);
  assert.match(app.document.querySelector('#evolutionBody').textContent, /读取失败.*temporary failure/);
  app.router.get('/api/evolution/overview?projectId=project-1', overview);
  app.document.querySelector('[data-evo-reload]').click(); await settle(5);
  assert.ok(app.document.querySelector('.evo-home-row'));
});

test('asset grouping deduplicates proposal appearances and merges findings without merging different asset kinds', async t => {
  const p = { id: 'p-grouped', projectId: 'project-1', findingId: finding.id, assetKind: 'agent', assetId: 'a', assetName: 'Reviewer', title: '当前修改', status: 'pending', summary: { total: 1, finished: 1, fixed: 1, passed: true }, baseHash: 'base', baseContent: '原文', candidateContent: '原文\n新增要求', replays: [] };
  const second = { ...finding, id: 'f-second', clusters: [{ id: 'new-1', label: '新问题一', runs: 1 }, { id: 'new-2', label: '新问题二', runs: 1 }] };
  const rule = { ...finding, id: 'f-rule', assetKind: 'rule', assetName: '同名规则', clusters: [{ id: 'rule-error', label: '规则问题', runs: 1 }] };
  const history = { ...p, id: 'p-history', status: 'released', releasedVersion: 2 };
  const rollback = { ...history, id: 'p-rollback', status: 'rolled_back' };
  const q = { id: 'q-grouped', projectId: 'project-1', findingId: second.id, assetKind: 'agent', assetId: 'a', status: 'queued', title: '排队修改' };
  const app = await loadApp({ routes: [...routes,
    { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: { projectId: 'project-1', pending: [p], validating: [], trial: [p], recent: [p, history, rollback], queue: [q], health: [] } },
    { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [{ ...finding, proposalId: p.id }, second, rule] },
    { method: 'GET', path: '/api/evolution/proposals/p-grouped', body: p },
    { method: 'GET', path: '/api/evolution/proposals/p-history', body: history },
    { method: 'GET', path: '/api/evolution/assets/agent/a/versions', body: { liveHash: 'base', currentVersion: 1 } },
  ] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  assert.equal(app.document.querySelectorAll('[data-evo-asset-group]').length, 2);
  assert.match($('[data-evo-asset-group="asset:agent:a"] .evo-home-summary').textContent, /当前修改/);
  assert.match($('[data-evo-asset-group="asset:rule:a"]').textContent, /规则问题.*同名规则/s);
  assert.equal(app.router.calls.some(c => c.path.startsWith('/api/runs/') || c.path === '/api/evolution/proposals/p-grouped'), false);
  $('[data-evo-asset-group="asset:agent:a"]').click(); await settle(5);
  $('#evoProblemEvidenceDetails > summary').click();
  assert.match($('.evo-story-signal-section').textContent, /失败案例/);
  $('[data-evo-source-example="node_failed"]').click();
  assert.equal($('#evoStorySourceExample').hidden,false);
  assert.match($('#evoStorySourceExample').textContent,/Error 0/);
  $('#evoSourceDetails > summary').click();
  assert.equal($('#evoSourceDetails').open, true);
  assert.equal(app.document.querySelectorAll('#evoProblemRecords [data-evo-related-finding]').length, 12);
  assert.equal($('#evoProblemRecords [data-evo-related-finding="f-second"]'), null);
  assert.equal($('[data-evo-related-finding="f-rule"]'), null);
  assert.equal($('#evoWorkspaceTasks'), null);
  $('[data-evo-list-back]').click(); await settle(4);
  filterTasks(app,'status','doing');
  assert.equal($('#evoDetailPage'), null);
  assert.equal(app.document.querySelectorAll('.evo-home-row').length, 0);
  $('[data-evo-list-section="released"]').click();
  $('[data-evo-proposal="p-history"]').click(); await settle(5);
  assert.equal(app.window.location.hash,'#/evolution/proposals/p-history');
  $('[data-evo-list-back]').click(); await settle(4);
  assert.equal(app.document.activeElement.dataset.evoProposal,'p-history');
  $('[data-evo-list-section="rolled_back"]').click();
  assert.equal($('.evo-home-row').dataset.evoProposal,'p-rollback');
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});


test('empty home explains explicit diagnosis results without a verification guide', async t => {
  const app = await loadApp({routes:[
    {method:'GET',path:'/api/evolution/overview?projectId=project-1',body:{health:[],queue:[]}},
    {method:'GET',path:'/api/evolution/findings?projectId=project-1',body:[]},
    {method:'POST',path:'/api/evolution/diagnose',body:{runs:0,findings:[]}},
  ]}); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  assert.match($('#evolutionBody').textContent,/暂无改进记录/);
  assert.equal($('.evo-home-columns'),null);
  assert.equal($('.evo-home-tabs'),null);
  assert.equal($('[data-evo-home-guide]'),null);
  assert.equal($('.evo-home-empty [data-evo-home-check]'),null);
  $('#evoDiagnose').click(); await settle(6);
  assert.match($('#evolutionBody').textContent,/当前项目没有可分析的运行/);
  assert.match($('.evo-home-check-note').textContent,/0 次运行/);
  app.router.post('/api/evolution/diagnose',{runs:4,findings:[]});
  $('#evoDiagnose').click(); await settle(6);
  assert.match($('#evolutionBody').textContent,/暂无待处理问题/);
  assert.match($('.evo-home-check-note').textContent,/4 次运行/);
  $('#evoRefresh').click(); await settle(4);
  assert.equal(app.router.calls.filter(c=>c.method==='POST'&&c.path.endsWith('/diagnose')).length,2,'refresh must not diagnose');
  $('[data-evo-home-runs]').click();await settle(3);
  assert.ok($('#runsView').classList.contains('active'));
  assert.equal(app.evaluate('state.projectFilter'),'project-1');
});


test('new unqueued feedback is counted as a follow-up without changing the current proposal',async t=>{
  const current={id:'active-p',findingId:'original',projectId:'project-1',assetKind:'agent',assetId:'a',assetName:'Reviewer',status:'pending',title:'当前修改',summary:{total:1,finished:1,passed:true}};
  const old={...finding,id:'original',status:'proposed',proposalId:current.id};
  const feedback={...finding,id:'new-feedback',clusters:[{id:'new-human',kind:'user_feedback',label:'产物缺少 reason 字段',runs:1}],sampleRunIds:['feedback-run']};
  const app=await loadApp({routes:[
    {method:'GET',path:'/api/evolution/overview?projectId=project-1',body:{pending:[current],health:[],queue:[]}},
    {method:'GET',path:'/api/evolution/findings?projectId=project-1',body:[old,feedback]},
  ]});t.after(app.close);
  await open(app);
  assert.equal(app.evaluate('evoFollowupCount(evoUnifiedRows()[0])'),1);
  app.evaluate(`evoOpenQueue('agent','a')`);await settle(2);
  assert.match(app.document.querySelector('#kbDrawerBody').textContent,/产物缺少 reason 字段/);
  assert.equal(app.router.calls.some(c=>c.method!=='GET'),false);
});

test('clicking a history cell opens a short summary and restores focus when closed', async t => {
  const replays = [
    { id: 'problem', sourceRunId: 'original-a', runId: 'replay-a', group: 'diagnosis', sourceScore: 0, score: 2, outcome: 'fixed', finishedAt: now },
    { id: 'regression', sourceRunId: 'original-b', runId: 'replay-b', group: 'regression', sourceScore: 2, score: 2, outcome: 'unchanged', finishedAt: now },
  ];
  const proposal = {
    id: 'p-linked', projectId: 'project-1', assetKind: 'agent', assetId: 'a', assetName: '日志定位',
    status: 'pending', baseVersion: 1, baseContent: '共用指令\n保留首条错误\n输出定位结果',
    candidateContent: '共用指令\n跳过已恢复的错误\n优先定位真实失败\n输出定位结果',
    summary: { total: 2, finished: 2, fixed: 1, unchanged: 1, passed: true }, replays,
  };
  const replayRoutes = replays.flatMap((replay, index) => {
    const lines = [`ERROR summary ${index}`, `--- FAIL: Test${index}`];
    const run = (id, position) => ({
      id, executionContext: { projectId: 'project-1' }, input: JSON.stringify({ lines }),
      nodes: { check: { output: JSON.stringify({ evidence: { line: position, text: lines[position - 1] } }) } },
    });
    return [
      { method: 'GET', path: `/api/evolution/proposals/${proposal.id}/replays/${replay.id}/compare`,
        body: { sourceRunId: replay.sourceRunId, runId: replay.runId, nodes: [{ id: 'check', shaped: true }] } },
      { method: 'GET', path: `/api/runs/${replay.sourceRunId}`, body: run(replay.sourceRunId, index + 1) },
      { method: 'GET', path: `/api/runs/${replay.runId}`, body: run(replay.runId, 2) },
    ];
  });
  const app = await loadApp({ routes: [...routes, ...replayRoutes,
    { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: { pending: [proposal] } },
    { method: 'GET', path: `/api/evolution/proposals/${proposal.id}`, body: proposal },
  ] });
  t.after(app.close);
  const $ = selector => app.document.querySelector(selector);
  await open(app);
  $('[data-evo-asset-group="asset:agent:a"]').click();
  await settle(7);
  assert.equal(app.document.querySelectorAll('.evo-detail-track li').length, 6);
  assert.match($('.evo-detail-track [aria-current="step"]').textContent, /小范围试用[\s\S]+待确认试用/);
  assert.match($('#evoStoryEffects').textContent,/2\/2 条可对比[\s\S]+50%[\s\S]+100%/);
  assert.doesNotMatch($('#evoValidationResults').textContent,/试用|流量/);
  assert.equal($('#evoRolloutDetails'),null);
  assert.doesNotMatch($('#evoDetailBody').textContent,/来源记录与诊断|试用、发布与观察/);
  $('.evo-detail-track [aria-current="step"] button').click();
  await settle(5);
  assert.equal($('#kbDrawer [data-evo-stage="canary"]').getAttribute('aria-selected'),'true');
  assert.match($('#evoStagePanel').textContent,/可以开始小范围试用/);
  app.evaluate('evoDrawerClose()');
  assert.equal($('#evoReviewDiff .evo-diff-preview-line.del span').textContent,'2');
  $('[data-evo-diff-expand]').click();
  $('[data-evo-diff-context]').click();
  const cells = [...$('#evoFullChange .evo-diff-numbered .ctx:last-child').querySelectorAll('.evo-diff-number')];
  assert.deepEqual(cells.map(el => el.textContent), ['3', '4']);
  assert.equal($('#evoEvidenceDialog').open,false);
  assert.equal(app.router.calls.some(call=>call.path.endsWith('/compare')),false);
  assert.equal($('#evoValidationDetails'),null);
  assert.equal(app.document.querySelectorAll('[data-evo-story-row]').length,2);
  $('[data-evo-sample-filter="fixed"]').click();
  assert.equal(app.document.querySelectorAll('[data-evo-story-row]').length,1);
  assert.equal($('[data-evo-sample-filter="fixed"]').getAttribute('aria-pressed'),'true');
  $('[data-evo-sample-filter="all"]').click();
  assert.equal(app.document.querySelectorAll('[data-evo-story-row]').length,2);
  $('[data-evo-story-row="problem"] td:last-child').click();
  await settle(5);
  assert.equal($('#evoEvidenceDialog').open,true);
  assert.equal($('#evoEvidenceDetails').open,false);
  assert.match($('#evoStoryBehavior').textContent,/日志定位从第 1 行移到第 2 行：--- FAIL: Test0/);
  assert.doesNotMatch($('#evoStoryBehavior').textContent,/ERROR summary/);
  $('#evoEvidenceDetails > summary').click();
  assert.equal($('#evoEvidenceDetails').open,true);
  assert.match($('#evoEvidenceBody').textContent,/ERROR summary 0/);
  $('[data-evo-evidence-close]').click();
  assert.equal($('#evoEvidenceDialog').open,false);
  assert.equal(app.document.activeElement.dataset.evoStoryReplay,'problem');
  $('[data-evo-story-replay="regression"]').click();
  await settle(5);
  assert.equal($('#evoEvidenceDetails').open,false);
  assert.match($('#evoStoryBehavior').textContent,/结构化输出保持一致/);
  assert.doesNotMatch($('#evoEvidenceBody').textContent,/Test0/);
  assert.match($('#evoEvidenceTitle').textContent,/回归样本/);
  assert.match($('.evo-detail-track [aria-current="step"]').textContent,/待确认试用/);
  assert.equal($('[data-evo-story-replay="regression"]').getAttribute('aria-pressed'),'true');
  assert.equal(app.document.activeElement.dataset.evoEvidenceClose,'');
  assert.equal(app.document.querySelectorAll('[data-evo-story-row]').length,2);
  assert.equal(app.router.calls.some(call => call.method !== 'GET'), false);
});

test('SOP detection settings save one project switch and the selected model', async t => {
  const app = await loadApp({ routes: [...routes,
    { method: 'GET', path: '/api/assistant/models', body: {
      engines: [{ id: 'codebuddy', name: 'CodeBuddy', personal: true }],
      models: [{ id: 'codebuddy:claude-sonnet-5', name: 'Sonnet' }, { id: 'codebuddy:deepseek-v4', name: 'DeepSeek' }]
    } },
    { method: 'PUT', path: '/api/evolution/sop-detection?projectId=project-1', body: { enabled: true, model: 'codebuddy:deepseek-v4' } },
  ] });
  t.after(app.close);
  await open(app);
  app.document.querySelector('#evoPageHead [data-evo-tab="assets"]').click();
  await settle(6);
  const form = app.document.querySelector('#evoSOPDetection');
  const toggle = form.querySelector('[role=switch]');
  const model = form.querySelector('[data-feature-model]');
  const save = form.querySelector('[type=submit]');
  assert.equal(form.querySelectorAll('[role=switch]').length, 1);
  assert.equal(toggle.checked, false);
  assert.equal(model.closest('fieldset').disabled, true);
  assert.equal(save.disabled, true);
  toggle.click();
  assert.equal(model.closest('fieldset').disabled, false);
  model.value = 'codebuddy:deepseek-v4';
  model.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  assert.equal(save.disabled, false);
  save.click();
  await settle(4);
  const call = app.router.calls.find(item => item.method === 'PUT' && item.path === '/api/evolution/sop-detection');
  assert.deepEqual(call?.body, { enabled: true, model: 'codebuddy:deepseek-v4' });
  assert.equal(form.querySelector('[role=status]').textContent, '已保存');
});
