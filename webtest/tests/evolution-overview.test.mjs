import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

function confirmPrompt(app) { app.document.querySelector('#confirmOkBtn').click(); }

const now = new Date().toISOString();
const old = new Date(Date.now() - 20 * 86400000).toISOString();
const clusters = Array.from({ length: 12 }, (_, i) => ({
  id: `c-${i}`, label: `Error ${i}`, kind: 'node_failed', count: 20 - i, runs: 1,
  firstSeenAt: old, lastSeenAt: i < 2 ? now : old,
  occurrences: [{ runId: 'same-run', createdAt: now }],
}));
const finding = { id: 'f', projectId: 'project-1', assetKind: 'agent', assetId: 'a', assetName: 'Reviewer', status: 'open', clusters, triage: { verdict: 'fix', score: 3 } };
const overview = { projectId: 'project-1', findings: [finding], health: [], pending: [{ id: 'proposal-1', status: 'pending' }], validating: [], trial: [], recent: [] };
const routes = [
  { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: overview },
  { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [finding] },
  { method: 'POST', path: '/api/evolution/findings/f/snooze', body: finding },
];

test('evolution overview paginates, filters dates, selects per page and snoozes unique assets', async t => {
  const app = await loadApp({ routes });
  t.after(app.close);
  const $ = s => app.document.querySelector(s);
  const $$ = s => [...app.document.querySelectorAll(s)];
  const change = el => el.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  await app.evaluate(`state.projectFilter='project-1';switchView('evolution');renderEvolutionView()`);
  await settle(4);
  assert.equal($$('.evo-metric').length, 0);
  assert.match($('#evoMetrics').textContent, /1 项修改需要你处理/);
  assert.match($('#evoTab-issues').textContent, /发现的问题12/);
  assert.equal($$('[data-evo-finding]').length, 10);
  const trend = await app.evaluate('evoOverviewTrend(evoIssueRows())');
  assert.equal(trend.recorded, true);
  assert.equal(trend.points.reduce((sum, p) => sum + p.value, 0), 1, 'same run is deduplicated across clusters');
  const all = $('[data-evo-select-page]'); all.checked = true; change(all);
  assert.equal($$('[data-evo-select]:checked').length, 10);
  assert.equal($('#evoFindingModal'), null, 'selecting must not open the record modal');
  $('[data-evo-list-page="1"]').click();
  assert.equal($$('[data-evo-finding]').length, 2);
  assert.equal($$('[data-evo-select]:checked').length, 0, 'select all affects this page, not all pages');
  assert.equal($('[data-evo-select-page]').checked, false);
  $('[data-evo-batch-snooze]').click();
  await settle(2);
  assert.match($('#confirmModal').textContent, /1 个资产/);
  confirmPrompt(app);
  await settle(5);
  assert.equal(app.router.calls.filter(c => c.method === 'POST' && c.path.endsWith('/snooze')).length, 1, 'one request per asset despite multiple selected classes');

  const days = $('[data-evo-days]'); days.value = '7'; change(days);
  assert.equal($$('[data-evo-finding]').length, 2);
  assert.equal($('.evo-list-footer [aria-current="page"]').textContent, '1', 'filter resets the page');
  assert.equal($('[data-evo-batch-snooze]'), null);
  const sort = $('[data-evo-sort]'); sort.value = 'recent'; change(sort);
  assert.equal($('[data-evo-finding]').dataset.evoCluster, 'c-0');
  const search = $('[data-evo-search]'); search.focus(); search.value = 'Error 11';
  search.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.match($('#evoIssueList').textContent, /没有匹配的问题/);
  assert.equal(app.document.activeElement, search);
  days.value = ''; change(days);
  assert.equal($$('[data-evo-finding]').length, 1);
  assert.equal($('[data-evo-finding]').dataset.evoCluster, 'c-11');

  $('#evoTab-issues').focus();
  $('#evoTab-issues').dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
  assert.equal($('#evoTab-proposals').getAttribute('aria-selected'), 'true');
  assert.equal(app.document.activeElement, $('#evoTab-proposals'));
  assert.equal($('#evoPane').getAttribute('aria-labelledby'), 'evoTab-proposals');
  $('[data-evo-change-decisions]').click();
  assert.equal($('[data-evo-change-status]').value, 'decide');
  $('#evoTab-issues').click();
  assert.equal($('#evoTab-issues').getAttribute('aria-selected'), 'true');
});

test('refresh does not diagnose and repeated diagnosis is guarded; legacy trend remains honest', async t => {
  const legacy = { ...finding, triage: null, clusters: [{ id: 'old', label: 'Old failure', count: 2, runs: 2 }] };
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [legacy] }] });
  t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await app.evaluate(`state.projectFilter='project-1';switchView('evolution');renderEvolutionView()`);
  await settle(4);
  assert.equal($('.evo-overview-trend'), null, 'statistics stay out of the main page');
  $('[data-evo-tab="assets"]').click();
  assert.match($('.evo-overview-trend').textContent, /暂无趋势数据/);
  assert.equal($('.evo-overview-trend > svg'), null);
  $('[data-evo-tab="issues"]').click();
  $('#evoRefresh').click(); await settle(4);
  assert.equal(app.router.calls.filter(c => c.method === 'POST' && c.path.endsWith('/diagnose')).length, 0);
  let resolve;
  app.router.post('/api/evolution/diagnose', () => new Promise(r => { resolve = r; }));
  $('[data-evo-rediagnose]').click(); await settle(2);
  $('#evoDiagnose').click();
  assert.equal(app.router.calls.filter(c => c.method === 'POST' && c.path.endsWith('/diagnose')).length, 1);
  assert.equal($('#evoDiagnose').disabled, true);
  resolve({ findings: [], runs: 3 }); await settle(5);
  assert.equal($('#evoDiagnose').disabled, false);
  $('.evo-row-menu summary').click();
  $('.evo-row-menu [data-evo-open]').click(); await settle(3);
  assert.match($('#evoFindingTitle').textContent, /Old failure/);
});
