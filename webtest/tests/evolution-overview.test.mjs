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
  assert.match($('[data-evo-asset-group="asset:agent:a"]').textContent, /12 类关联问题/);
  assert.equal($('#evoMetrics').textContent, '');
  assert.match($('[data-evo-list-status="all"]').textContent, /全部记录13/);
  assert.equal(app.document.querySelectorAll('.evo-list-row').length, 10);
  assert.equal($('#evolutionBody input'), null);
  assert.equal($('#evolutionBody select'), null);
  assert.equal($('#evolutionBody [role="tab"]'), null);
  const trend = app.evaluate('evoOverviewTrend(evoIssueRows())');
  assert.equal(trend.points.reduce((sum, p) => sum + p.value, 0), 1);
  $('[data-evo-unified-page="1"]').click();
  assert.equal(app.document.querySelectorAll('.evo-list-row').length, 3);
  $('[data-evo-asset-group="asset:agent:a-11"]').click(); await settle(4);
  assert.match(app.window.location.hash, /findings\/f-11\/c-11/);
  assert.ok($('#evoDetailPage'));
  $('[data-evo-list-back]').click(); await settle(5);
  assert.equal(app.window.location.hash, '#/evolution');
  assert.equal(app.document.querySelectorAll('.evo-list-row').length, 3);
  assert.equal(app.document.activeElement.dataset.evoAssetGroup, 'asset:agent:a-11');
  $('[data-evo-list-status="doing"]').click();
  assert.match($('#evoUnifiedList').textContent, /此筛选下暂无记录/);
  assert.equal(app.document.activeElement.dataset.evoListStatus, 'doing');
  $('[data-evo-list-status="all"]').click();
  assert.equal(app.document.querySelectorAll('.evo-list-row').length, 10);
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
  $('[data-evo-inspect="records"]').click(); await settle(2);
  assert.match($('#evoFindingTitle').textContent, /Old failure/);
  assert.ok($('#evoDetailPage [data-evo-rediagnose]'));
});

test('read errors expose retry without disguising failed requests as empty results', async t => {
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/overview?projectId=project-1', status: 503, body: { error: 'temporary failure' } }] }); t.after(app.close);
  await open(app);
  assert.match(app.document.querySelector('#evolutionBody').textContent, /读取失败.*temporary failure/);
  app.router.get('/api/evolution/overview?projectId=project-1', overview);
  app.document.querySelector('[data-evo-reload]').click(); await settle(5);
  assert.ok(app.document.querySelector('.evo-list-row'));
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
  assert.match($('[data-evo-asset-group="asset:agent:a"]').textContent, /14 类关联问题.*1 项后续改进排队.*1 次历史尝试.*当前修改/s);
  assert.match($('[data-evo-asset-group="asset:rule:a"]').textContent, /同名规则.*1 类关联问题/);
  assert.equal(app.router.calls.some(c => c.path.startsWith('/api/runs/') || c.path === '/api/evolution/proposals/p-grouped'), false);
  $('[data-evo-asset-group="asset:agent:a"]').click(); await settle(5);
  $('[data-evo-inspect="records"]').click(); await settle(2);
  assert.equal(app.document.querySelectorAll('[data-evo-related-finding]').length, 14);
  assert.equal($('[data-evo-related-finding="f-rule"]'), null);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});
