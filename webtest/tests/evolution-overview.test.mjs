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

test('unified list paginates, preserves filter and page on return, and keeps diagnostics out of the home page', async t => {
  const assets = [finding, ...Array.from({ length: 11 }, (_, i) => ({ ...finding, id: `f-${i + 1}`, assetId: `a-${i + 1}`, clusters: [clusters[i + 1]] }))];
  const app = await loadApp({ routes: [...routes,
    { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: assets },
    { method: 'GET', path: '/api/evolution/findings/f-11/runs?cluster=c-11', body: { complete: true, total: 0, items: [] } },
  ] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  assert.match($('[data-evo-asset-group="asset:agent:a"]').textContent, /1 条问题运行/);
  assert.equal($('#evoMetrics').textContent, '');
  assert.match($('[data-evo-list-status="all"]').textContent, /全部记录13/);
  assert.equal(app.document.querySelectorAll('.evo-home-row').length, 10);
  assert.equal($('#evolutionBody input'), null);
  assert.equal($('#evolutionBody select'), null);
  assert.equal($('#evolutionBody [role="tab"]'), null);
  const trend = app.evaluate('evoOverviewTrend(evoIssueRows())');
  assert.equal(trend.points.reduce((sum, p) => sum + p.value, 0), 1);
  $('[data-evo-unified-page="1"]').click();
  assert.equal(app.document.querySelectorAll('.evo-home-row').length, 3);
  $('[data-evo-asset-group="asset:agent:a-11"]').click(); await settle(4);
  assert.match(app.window.location.hash, /findings\/f-11\/c-11/);
  assert.ok($('#evoDetailPage'));
  $('[data-evo-list-back]').click(); await settle(5);
  assert.equal(app.window.location.hash, '#/evolution');
  assert.equal(app.document.querySelectorAll('.evo-home-row').length, 3);
  assert.equal(app.document.activeElement.dataset.evoAssetGroup, 'asset:agent:a-11');
  assert.equal(app.document.querySelectorAll('.evo-home-tabs button').length, 2);
  assert.equal($('[data-evo-list-status="doing"]'), null);
  $('[data-evo-list-status="all"]').click();
  assert.equal(app.document.querySelectorAll('.evo-home-row').length, 10);
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
  assert.match($('#evoFindingTitle').textContent, /Reviewer/);
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
  const history = { ...p, id: 'p-history', status: 'rejected' };
  const q = { id: 'q-grouped', projectId: 'project-1', findingId: second.id, assetKind: 'agent', assetId: 'a', status: 'queued', title: '排队修改' };
  const app = await loadApp({ routes: [...routes,
    { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: { projectId: 'project-1', pending: [p], validating: [], trial: [p], recent: [p, history], queue: [q], health: [] } },
    { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [{ ...finding, proposalId: p.id }, second, rule] },
    { method: 'GET', path: '/api/evolution/proposals/p-grouped', body: p },
    { method: 'GET', path: '/api/evolution/assets/agent/a/versions', body: { liveHash: 'base', currentVersion: 1 } },
  ] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  assert.equal(app.document.querySelectorAll('[data-evo-asset-group]').length, 2);
  assert.match($('[data-evo-list-status="all"]').textContent, /全部记录2/);
  assert.match($('[data-evo-asset-group="asset:agent:a"]').textContent, /1 条问题运行.*后续建议 1.*历史尝试 1.*当前修改/s);
  assert.match($('[data-evo-asset-group="asset:rule:a"]').textContent, /同名规则.*1 条问题线索/);
  assert.equal(app.router.calls.some(c => c.path.startsWith('/api/runs/') || c.path === '/api/evolution/proposals/p-grouped'), false);
  $('[data-evo-asset-group="asset:agent:a"]').click(); await settle(5);
  assert.equal(app.document.querySelectorAll('#evoProblemRecords [data-evo-related-finding]').length, 12);
  assert.equal($('#evoProblemRecords [data-evo-related-finding="f-second"]'), null);
  assert.equal($('[data-evo-related-finding="f-rule"]'), null);
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
  assert.match(app.document.querySelector('.evo-home-row').textContent,/后续建议 1/);
  app.evaluate(`evoOpenQueue('agent','a')`);await settle(2);
  assert.match(app.document.querySelector('#kbDrawerBody').textContent,/产物缺少 reason 字段/);
  assert.equal(app.router.calls.some(c=>c.method!=='GET'),false);
});

test('clicking any history cell compares that sample and preserves the selected button', async t => {
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
  const cells = [...$('#evoFullChange .evo-diff-numbered .ctx:last-child').querySelectorAll('.evo-diff-number')];
  assert.deepEqual(cells.map(el => el.textContent), ['3', '4']);
  assert.match($('#evoStoryBehavior').textContent, /Test0/);
  $('[data-evo-story-row="regression"] td:last-child').click();
  await settle(5);
  assert.match($('#evoStoryBehavior').textContent, /Test1/);
  assert.doesNotMatch($('#evoStoryBehavior').textContent, /Test0/);
  assert.match($('#evoStorySelectedSample').textContent, /回归样本/);
  assert.equal($('[data-evo-story-replay="regression"]').getAttribute('aria-pressed'), 'true');
  assert.equal(app.document.activeElement.dataset.evoStoryReplay, 'regression');
  assert.equal(app.document.querySelectorAll('[data-evo-story-row]').length, 2);
  assert.equal(app.router.calls.some(call => call.method !== 'GET'), false);
});
