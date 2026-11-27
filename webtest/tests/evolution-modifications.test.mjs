import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const now = new Date().toISOString();
const finding = { id: 'finding-linked', projectId: 'project-1', assetKind: 'agent', assetId: 'developer', assetName: '后端开发角色', status: 'proposed', proposalId: 'change-1', summary: '缺少空值校验', triage: { verdict: 'fix', score: 2 }, clusters: [{ id: 'null-input', label: '空输入返回 500', count: 4, runs: 3, lastSeenAt: now }] };
const proposal = { id: 'change-1', projectId: 'project-1', findingId: finding.id, assetKind: 'agent', assetId: 'developer', assetName: finding.assetName, title: '增加必填参数校验', status: 'pending', baseHash: 'base', createdAt: now, updatedAt: now, summary: { total: 3, finished: 3, passed: true, fixed: 1, changed: 1, unchanged: 1, regressed: 0, blocked: 0, verdict: '验证通过' } };
const detail = { ...proposal, baseContent: '实现接口\n运行测试', candidateContent: '实现接口\n校验必填参数 <img src=x onerror=alert(1)>\n运行测试', rationale: '异常输入缺少明确约定', replays: [] };
const live = { liveHash: 'base', currentVersion: 2, drifted: false, versions: [], liveContent: detail.baseContent };
const overview = { projectId: 'project-1', findings: [], pending: [proposal], validating: [], trial: [], recent: [], health: [], policies: [] };
const routes = [
  { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: overview },
  { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [finding] },
  { method: 'GET', path: '/api/evolution/proposals/change-1', body: detail },
  { method: 'GET', path: '/api/evolution/assets/agent/developer/versions', body: live },
  { method: 'GET', path: '/api/evolution/findings/finding-linked/runs?cluster=null-input', body: { complete: true, total: 1, items: [{ runId: 'run-1', status: 'failed', detail: 'expected 400, got 500', createdAt: now }] } },
];
async function open(app) { await app.evaluate(`state.projectFilter='project-1';switchView('evolution');renderEvolutionView()`); await settle(4); }
function change(app, el) { el.dispatchEvent(new app.window.Event('change', { bubbles: true })); }

test('linked issue shows real modification, escaped side-by-side diff and confirmed canary controls', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  assert.match($('.evo-home-row').textContent, /空输入返回 500.*后端开发角色.*增加必填参数校验/s);
  assert.equal($('#evoDetailPage'), null);
  assert.equal($('#evoPageHead .kb-head-desc'), null);
  assert.equal(app.router.calls.filter(c => c.path === '/api/evolution/proposals/change-1').length, 0, 'list does not fetch full bodies');
  $('[data-evo-asset-group]').click(); await settle(5);
  assert.equal($('#evoDetailPage [data-evo-propose]'), null, 'do not offer another proposal when linked');
  assert.match($('#evoLinkedChange').textContent, /指令修改.*完整 Diff/s);
  assert.equal($('#evoDetailPage img'), null);
  assert.match($('#evoLiveVersion').getAttribute('data-tooltip')||$('#evoLiveVersion').textContent, /当前正文与修改前快照一致/);
  assert.equal(app.document.querySelectorAll('.evo-detail-track li').length,6);
  assert.match($('#evoDetailActions').textContent, /小范围试用/);
  assert.equal($('#evoEvidenceDialog').open, false);
  assert.match($('#evoReviewDiff').textContent, /校验必填参数 <img src=x onerror=alert\(1\)>/);
  assert.equal($('#evoReviewDiff img'), null);
  assert.ok($('#evoReviewDiff .evo-diff-preview-line.add'));
  assert.ok($('#evoEvidenceBody'));
  assert.equal($('#evoPageHead').hidden, true);
  assert.doesNotMatch($('#evoLinkedChange').textContent, /问题未再出现/);
  assert.equal($('#evoDetailCanary').open, false);
  assert.equal($('#evoDetailActions [data-evo-approve]').disabled, false);
  $('#evoDetailActions [data-evo-canary]').click();
  assert.equal($('#evoDetailCanary').open, true);
  assert.equal(app.document.activeElement.id, 'evoCanaryPercent');
  assert.equal(app.router.calls.some(c => c.method === 'POST'), false);
  $('#evoCanaryPercent').value = '25'; $('#evoCanaryMinRuns').value = '10';
  app.router.post('/api/evolution/proposals/change-1/approve', { ...detail, status: 'canary' });
  $('#evoDetailCanary [data-evo-canary]').click(); await settle(2);
  assert.match($('#confirmModal').textContent, /25%.*10 次/s);
  assert.equal(app.router.calls.some(c => c.method === 'POST'), false, 'confirmation is required');
  $('#confirmOkBtn').click(); await settle(5);
  assert.deepEqual(app.router.calls.find(c => c.method === 'POST').body, { mode: 'canary', percent: 25, minRuns: 10, note: '' });
});

test('modification records distinguish pending, partial trial, release and rollback with filtering and pagination', async t => {
  const released = { ...proposal, id: 'published', findingId: '', assetId: 'released-asset', status: 'released', releasedVersion: 6, watch: { status: 'degraded' } };
  const trial = { ...proposal, id: 'trial', findingId: '', assetId: 'trial-asset', status: 'canary', canary: { status: 'passed', percent: 25 } };
  const rolled = { ...proposal, id: 'rolled', findingId: '', assetId: 'rolled-asset', status: 'rolled_back', releasedVersion: 4 };
  const others = Array.from({ length: 10 }, (_, i) => ({ ...proposal, id: `old-${i}`, findingId: '', assetId: `old-asset-${i}`, status: 'rejected' }));
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: { ...overview, trial: [trial, released], recent: [released, rolled, ...others] } }] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  assert.equal(app.document.querySelectorAll('.evo-home-row').length,10);
  $('[data-evo-unified-page="1"]').click();
  assert.equal(app.document.querySelectorAll('.evo-home-row').length,4);
  const status=$('[data-evo-task-filter="status"]');
  status.value='active'; change(app,status);
  assert.match($('#evoUnifiedList').textContent,/试用通过 · 待全量/);
  $('[data-evo-list-section="released"]').click();
  assert.ok($('[data-evo-proposal="published"]'));
  $('[data-evo-list-section="rolled_back"]').click();
  assert.ok($('[data-evo-proposal="rolled"]'));
  assert.doesNotMatch($('#evoUnifiedList').textContent,/当前仍生效/);
});

test('live version checks distinguish historical release, drift and unknown status', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  app.router.get('/api/evolution/proposals/change-1', { ...detail, status: 'released', releasedVersion: 3 });
  app.router.get('/api/evolution/assets/agent/developer/versions', { ...live, currentVersion: 4, liveHash: 'newer' });
  await app.evaluate(`evoOpenProposal('change-1')`); await settle(5);
  assert.match($('#evoLiveVersion').getAttribute('data-tooltip')||$('#evoLiveVersion').textContent, /已被替换/);
  assert.doesNotMatch($('#evoLiveVersion').getAttribute('data-tooltip')||$('#evoLiveVersion').textContent, /当前仍生效/);
  app.router.get('/api/evolution/assets/agent/developer/versions', { ...live, currentVersion: 3 });
  $('[data-evo-detail-refresh]').click(); await settle(4);
  assert.match($('#evoLiveVersion').getAttribute('data-tooltip')||$('#evoLiveVersion').textContent, /当前仍生效/);
  app.router.get('/api/evolution/proposals/change-1', detail);
  app.router.get('/api/evolution/assets/agent/developer/versions', { ...live, liveHash: 'changed' });
  $('[data-evo-detail-refresh]').click(); await settle(4);
  assert.match($('#evoLiveVersion').getAttribute('data-tooltip')||$('#evoLiveVersion').textContent, /不能直接发布这份旧修改稿/);
  assert.equal($('#evoDetailActions [data-evo-approve]').disabled, true);
  assert.equal($('#evoDetailActions [data-evo-canary]').disabled, true);
  app.router.get('/api/evolution/assets/agent/developer/versions', { status: 503, body: { error: 'unavailable' } });
  $('[data-evo-detail-refresh]').click(); await settle(4);
  assert.match($('#evoLiveVersion').textContent, /版本核对失败/);
  assert.doesNotMatch($('#evoLiveVersion').getAttribute('data-tooltip')||$('#evoLiveVersion').textContent, /当前仍生效/);
});

test('proposal errors retry and missing candidate cannot be approved or silently presented as a diff', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  app.router.get('/api/evolution/proposals/change-1', { status: 500, body: { error: '<script>bad()</script>' } });
  $('[data-evo-asset-group]').click(); await settle(4);
  assert.match($('#evoLinkedChange').textContent, /读取修改失败/);
  assert.equal($('#evoLinkedChange script'), null);
  assert.equal($('#evoDetailActions [data-evo-approve]'), null);
  app.router.get('/api/evolution/proposals/change-1', { ...detail, candidateContent: '' });
  $('[data-evo-detail-refresh]').click(); await settle(4);
  assert.match($('#evoLinkedChange').textContent, /暂无正文修改/);
  assert.equal($('.evo-before-after'), null);
  assert.equal($('#evoDetailActions [data-evo-approve]').disabled, true);
  assert.equal($('#evoDetailActions [data-evo-canary]').disabled, true);
});

test('reopened finding retains historical diff and can generate a new suggestion', async t => {
  const failed = { ...detail, status: 'failed', error: '验证失败' };
  const reopened = { ...finding, status: 'open', proposalId: '' };
  const app = await loadApp({ routes: [...routes,
    { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: { ...overview, pending: [], recent: [failed], health: [{ assetKind: 'agent', assetId: 'developer', level: 2 }] } },
    { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [reopened] },
    { method: 'GET', path: '/api/evolution/proposals/change-1', body: failed },
  ] }); t.after(app.close);
  await open(app);
  app.document.querySelector('[data-evo-asset-group]').click(); await settle(4);
  const button = app.document.querySelector('#evoDetailActions [data-evo-propose]');
  assert.equal(button.dataset.evoPropose, finding.id);
  assert.equal(button.disabled, false);
  assert.ok(app.document.querySelector('.evo-diff-preview-line'));
  assert.match(button.textContent, /重新生成修改建议/);
});

test('detail deep links survive re-entry and browser history without trapping focus', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  $('[data-evo-asset-group]').click(); await settle(4);
  const hash = app.window.location.hash;
  assert.equal(hash, `#/evolution/proposals/${proposal.id}`);
  assert.equal(app.document.body.style.overflow, '');
  assert.equal(app.document.activeElement.id, 'evoFindingTitle');
  app.window.history.back(); await settle(5);
  assert.ok($('#evoUnifiedList')); assert.equal($('#evoDetailPage'), null);
  app.window.history.forward(); await settle(5);
  assert.ok($('#evoDetailPage')); assert.equal(app.window.location.hash, hash);
  await app.evaluate(`renderEvolutionView()`); await settle(4);
  assert.ok($('#evoDetailPage')); assert.equal(app.window.location.hash, hash);
  await app.evaluate(`switchView('issues')`); await settle(3);
  assert.equal($('#evoDetailPage'), null);
  app.window.location.hash = hash; await settle(5);
  assert.ok($('#evoDetailPage')); assert.equal(app.window.location.hash, hash);
  $('[data-evo-asset]').click(); await settle(4);
  assert.ok($('#evoDetailPage')); assert.ok($('#kbDrawer.open'));
  app.evaluate(`evoDrawerClose()`);
  assert.ok($('#evoDetailPage')); assert.equal(app.window.location.hash, hash);
});

test('no percentages are fabricated for empty arms and version recheck blocks publish until it succeeds', async t => {
  const emptyTrial = { ...proposal, status: 'canary', canary: { percent: 50, minRuns: 5, control: { runs: 0, clean: 0 }, canary: { runs: 0, clean: 0 } }, summary: {} };
  const app = await loadApp({ routes }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  assert.equal(app.evaluate(`evoListEffect(${JSON.stringify(emptyTrial)})`).includes('0%'), false);
  $('[data-evo-asset-group]').click(); await settle(4);
  let resolve;
  app.router.get('/api/evolution/assets/agent/developer/versions', () => new Promise(r => { resolve = r; }));
  $('[data-evo-detail-refresh]').click(); await settle(3);
  assert.equal($('[data-evo-approve]').disabled, true);
  assert.equal($('[data-evo-canary]').disabled, true);
  resolve(live); await settle(4);
  assert.equal($('[data-evo-approve]').disabled, false);
  app.router.get('/api/evolution/assets/agent/developer/versions', () => { throw new Error('version offline'); });
  $('[data-evo-detail-refresh]').click(); await settle(4);
  assert.equal($('[data-evo-approve]').disabled, true);
  assert.match($('#evoLiveVersion').textContent, /版本核对失败/);
});

test('late reject response does not reopen detail after returning to the list', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  $('[data-evo-asset-group]').click(); await settle(4);
  let resolve;
  app.router.post('/api/evolution/proposals/change-1/reject', () => new Promise(r => { resolve = r; }));
  app.evaluate(`evoReject('change-1',document.querySelector('[data-evo-reject]'))`); await settle(2);
  app.document.querySelector('#confirmOkBtn').click(); await settle(2);
  $('[data-evo-list-back]').click(); await settle(4);
  resolve({ ...detail, status: 'rejected' }); await settle(5);
  assert.ok($('#evoUnifiedList')); assert.equal($('#evoDetailPage'), null);
});

test('late proposal and version responses do not replace a newer detail or cross a project switch', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  let resolveProposal;
  app.router.get('/api/evolution/proposals/late', () => new Promise(resolve => { resolveProposal = resolve; }));
  app.evaluate(`evoOpenProposal('late')`); await settle(2);
  app.evaluate(`evoOpenProposal('change-1')`); await settle(4);
  resolveProposal({ ...detail, id: 'late', title: '过期修改' }); await settle(3);
  assert.equal($('#evoFindingTitle').textContent, '空输入返回 500');
  let resolveVersion;
  app.router.get('/api/evolution/assets/agent/developer/versions', () => new Promise(resolve => { resolveVersion = resolve; }));
  $('[data-evo-detail-refresh]').click(); await settle(3);
  app.router.get('/api/evolution/overview?projectId=project-2', { projectId: 'project-2', findings: [], pending: [], health: [] });
  app.router.get('/api/evolution/findings?projectId=project-2', []);
  await app.evaluate(`state.projectFilter='project-2';renderEvolutionView()`);
  resolveVersion(live); await settle(3);
  assert.equal($('#evoDetailPage'), null);
  assert.doesNotMatch($('#evolutionBody').textContent, /后端开发角色/);
});

test('trial details show server totals for both arms without mixing historical replay totals', async t => {
  const trial = { ...detail, status: 'canary', canary: { status: 'running', percent: 25, minRuns: 5, canary: { runs: 5, clean: 3, reworked: 1, failed: 1 }, control: { runs: 4, clean: 2, reworked: 1, failed: 1 } } };
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/proposals/change-1', body: trial }] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app); $('[data-evo-asset-group]').click(); await settle(5);
  $('[data-evo-progress-stage="canary"]').click(); await settle(4);
  const rows=[...app.document.querySelectorAll('#evoStagePanel .evo-review-trial tbody tr')];
  assert.deepEqual(rows.map(row=>Array.from(row.cells,cell=>cell.textContent)),[['原版','4 / 5','2/4'],['试用版','5 / 5','3/5']]);
  assert.match($('.evo-traffic-legend').textContent,/试用版 25%.*原版 75%/s);
  assert.equal($('#evoValidationResults .evo-story-empty').textContent,'暂无');
  assert.equal($('#evoDetailActions [data-evo-approve]').disabled, true);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('zero-sample trial arms have no inferred success counts', async t => {
  const trial = { ...detail, status: 'canary', canary: { status: 'running', percent: 50, minRuns: 5, canary: { runs: 0, clean: 0 }, control: { runs: 0, clean: 0 } } };
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/proposals/change-1', body: trial }] }); t.after(app.close);
  await open(app); app.document.querySelector('[data-evo-asset-group]').click(); await settle(5);
  app.document.querySelector('[data-evo-progress-stage="canary"]').click(); await settle(4);
  const rows=[...app.document.querySelectorAll('#evoStagePanel .evo-review-trial tbody tr')];
  assert.deepEqual(rows.map(row=>row.cells[2].textContent),['—','—']);
  assert.equal(app.document.querySelector('[data-evo-approve]').disabled, true);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('post-release board counts only new-version observations and never claims improvement without a baseline', async t => {
  const released = { ...detail, status: 'released', releasedVersion: 3, summary: { total: 17, finished: 17, fixed: 17, passed: true }, canary: { status: 'passed', minRuns: 5, percent: 50, control: { runs: 13, clean: 13 }, canary: { runs: 11, clean: 11 } }, watch: { status: 'watching', version: 3, prevVersion: 2, minRuns: 5, baselineReady: false, baseline: { runs: 9, clean: 9 }, current: { runs: 2, clean: 1, reworked: 1, failed: 0 } } };
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/proposals/change-1', body: released }] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app); $('[data-evo-asset-group]').click(); await settle(5);
  $('[data-evo-progress-stage="watch"]').click(); await settle(4);
  const board=$('#evoStagePanel'), arms=[...board.querySelectorAll('.evo-arm:not(.evo-arm-head)')];
  assert.equal(arms.length,2);
  assert.match(arms[1].textContent,/发布后 v3.*2.*1.*1.*0/s);
  assert.doesNotMatch(board.textContent,/17 条|13 次|11 次|质量提升|已彻底解决/);
  assert.equal($('#evoValidationResults .evo-story-empty').textContent,'暂无');
  assert.equal($('#evoDetailActions [data-evo-rollback]').dataset.evoRollback,'2');
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('unfinished trial cannot publish through either detail or the full process drawer', async t => {
  const trial = { ...detail, status: 'canary', canary: { status: 'running', percent: 25, minRuns: 5, control: { runs: 2, clean: 2 }, canary: { runs: 2, clean: 2 } } };
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/proposals/change-1', body: trial }] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app); $('[data-evo-asset-group]').click(); await settle(5);
  const publish = $('#evoDetailActions [data-evo-approve]');
  assert.equal(publish.disabled, true); publish.click(); await settle(2);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
  $('[data-evo-process="change-1"]').click(); await settle(4);
  const drawerPublish = $('#kbDrawer [data-evo-approve]');
  assert.equal(drawerPublish.disabled, true, 'full process must not bypass the trial gate');
  drawerPublish.click(); await settle(2);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('passed trial publish and stop actions require explicit confirmation', async t => {
  const trial = { ...detail, status: 'canary', canary: { status: 'passed', percent: 25, minRuns: 5, control: { runs: 5, clean: 4 }, canary: { runs: 5, clean: 5 } } };
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/proposals/change-1', body: trial }] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app); $('[data-evo-asset-group]').click(); await settle(5);
  assert.equal($('#evoDetailActions [data-evo-approve]').disabled, false);
  $('#evoDetailActions [data-evo-reject]').click(); await settle(2);
  assert.match($('#confirmModal').textContent, /终止试用.*新运行不再使用修改稿/s);
  $('#confirmCancelBtn').click(); await settle(2);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
  $('#evoDetailActions [data-evo-approve]').click(); await settle(2);
  assert.match($('#confirmModal').textContent, /全量发布.*进入发布后观察/s);
  $('#confirmCancelBtn').click(); await settle(2);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
  app.router.post('/api/evolution/proposals/change-1/approve', { ...trial, status: 'released', releasedVersion: 3 });
  $('#evoDetailActions [data-evo-approve]').click(); await settle(2);
  $('#confirmOkBtn').click(); await settle(5);
  const writes = app.router.calls.filter(c => c.method !== 'GET');
  assert.equal(writes.length, 1);
  assert.equal(writes[0].path, '/api/evolution/proposals/change-1/approve');
  assert.deepEqual(writes[0].body, { note: '', force: false });
});

const queued = { id: 'queue-1', projectId: 'project-1', findingId: 'finding-next', assetKind: 'agent', assetId: 'developer', assetName: '后端开发角色', status: 'queued', title: '下一项 <img src=x onerror=alert(1)>' };

test('queue cannot start while the asset is occupied, and cancel is confirmed without auto-starting another item', async t => {
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: { ...overview, queue: [queued, { ...queued, id: 'queue-2' }] } }] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  assert.equal(app.document.querySelectorAll('[data-evo-asset-group]').length, 1);
  $('[data-evo-asset-group]').click(); await settle(5);
  assert.match($('[data-evo-queue]').textContent, /后续建议 2/);
  $('[data-evo-queue]').click(); await settle(3);
  assert.equal($('#kbDrawer img'), null);
  assert.equal($('#kbDrawer [data-evo-queue-start="queue-1"]').disabled, true);
  $('#kbDrawer [data-evo-queue-start="queue-1"]').click(); await settle(2);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
  $('#kbDrawer [data-evo-queue-reject="queue-1"]').click(); await settle(2);
  assert.match($('#confirmModal').textContent, /尚未生成修改稿.*退出队列/s);
  $('#confirmCancelBtn').click(); await settle(2);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
  app.router.post('/api/evolution/queue/queue-1/reject', () => { app.router.get('/api/evolution/overview?projectId=project-1', { ...overview, queue: [{ ...queued, id: 'queue-2' }] }); return { ...queued, status: 'rejected' }; });
  $('#kbDrawer [data-evo-queue-reject="queue-1"]').click(); await settle(2);
  $('#confirmOkBtn').click(); await settle(5);
  const writes = app.router.calls.filter(c => c.method !== 'GET');
  assert.equal(writes.length, 1);
  assert.equal(writes[0].path, '/api/evolution/queue/queue-1/reject');
  assert.deepEqual(writes[0].body, {});
  $('[data-evo-queue]').click(); await settle(3);
  assert.equal($('#kbDrawer [data-evo-queue-reject="queue-1"]'), null);
  assert.ok($('#kbDrawer [data-evo-queue-reject="queue-2"]'));
});

test('queue start waits for confirmation and opens a newly generated proposal using latest content', async t => {
  const archived = { ...detail, status: 'rejected' };
  const next = { ...detail, id: 'change-next', findingId: queued.findingId, status: 'validating', title: '按最新正文重新验证', baseHash: 'latest', baseContent: '线上已更新的正文', candidateContent: '', summary: { total: 2, finished: 0, passed: false } };
  const queueOverview = { ...overview, pending: [], recent: [archived], queue: [queued] };
  const app = await loadApp({ routes: [...routes,
    { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: queueOverview },
    { method: 'GET', path: '/api/evolution/proposals/change-1', body: archived },
    { method: 'GET', path: '/api/evolution/proposals/change-next', body: next },
  ] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app); $('[data-evo-asset-group]').click(); await settle(5);
  $('[data-evo-queue]').click(); await settle(3);
  const start = $('#kbDrawer [data-evo-queue-start="queue-1"]');
  assert.equal(start.disabled, false); start.click(); await settle(2);
  assert.match($('#confirmModal').textContent, /当前最新正文.*重新生成修改稿.*不直接使用旧修改稿/s);
  $('#confirmCancelBtn').click(); await settle(2);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
  app.router.post('/api/evolution/queue/queue-1/start', () => { app.router.get('/api/evolution/overview?projectId=project-1', { ...queueOverview, queue: [], validating: [next] }); return next; }, 202);
  start.click(); await settle(2); $('#confirmOkBtn').click(); await settle(6);
  const writes = app.router.calls.filter(c => c.method !== 'GET');
  assert.equal(writes.length, 1);
  assert.equal(writes[0].path, '/api/evolution/queue/queue-1/start');
  assert.deepEqual(writes[0].body, {});
  assert.equal(app.window.location.hash, '#/evolution/proposals/change-next');
  assert.equal($('#evoFindingTitle').textContent, next.title);
  assert.match($('#evoReviewDiff').textContent, /暂无正文修改/s);
  assert.equal($('#evoDetailActions [data-evo-approve]'), null);
  assert.equal($('#evoDetailActions [data-evo-canary]'), null);
});

for (const reply of [{ queued: true, queueId: queued.id }, queued, { status: 'queued', queueId: queued.id, queue: queued }]) {
  test(`busy proposal request queues without fetching a nonexistent proposal (${reply.queue ? 'queue envelope' : reply.queued ? 'queued flag' : 'queued object'})`, async t => {
    const later = { ...finding, id: 'finding-next', status: 'open', proposalId: '', clusters: [{ id: 'next', label: '后续问题', runs: 2 }] };
    const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [finding, later] }] }); t.after(app.close);
    const $ = s => app.document.querySelector(s);
    await open(app); $('[data-evo-asset-group]').click(); await settle(5);
    $('[data-evo-queue]').click(); await settle(3);
    const add = $('#kbDrawer [data-evo-propose="finding-next"]');
    assert.match(add.textContent, /加入后续改进/);
    app.router.post('/api/evolution/proposals', () => { app.router.get('/api/evolution/overview?projectId=project-1', { ...overview, queue: [queued] }); return reply; }, 202);
    add.click(); await settle(6);
    const writes = app.router.calls.filter(c => c.method !== 'GET');
    assert.equal(writes.length, 1);
    assert.deepEqual(writes[0].body, { projectId: 'project-1', findingId: 'finding-next' });
    assert.equal(app.router.calls.some(c => /\/proposals\/(undefined|queue-1)$/.test(c.path)), false);
    assert.ok($('#kbDrawer [data-evo-queue-reject="queue-1"]'));
    assert.equal($('#kbDrawer [data-evo-queue-start="queue-1"]').disabled, true);
    assert.equal($('#evoFindingTitle').textContent, '空输入返回 500');
  });
}

test('modification summary contains only real removed and added lines and treats identical content as no change', async t => {
  const changed = { ...detail, baseContent: '保留内容\n旧要求', candidateContent: '保留内容\n新要求 <img src=x onerror=alert(1)>', rationale: '不能当作真实修改的模型理由' };
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/proposals/change-1', body: changed }] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app); $('[data-evo-asset-group]').click(); await settle(5);
  assert.equal($('#evoReviewDiff .evo-diff-preview-line.del code').textContent, '旧要求');
  assert.equal($('#evoReviewDiff .evo-diff-preview-line.add code').textContent, '新要求 <img src=x onerror=alert(1)>');
  assert.doesNotMatch($('#evoReviewDiff').textContent, /保留内容|模型理由/);
  assert.equal($('#evoReviewDiff img'), null);
  app.router.get('/api/evolution/proposals/change-1', { ...changed, candidateContent: changed.baseContent });
  $('[data-evo-detail-refresh]').click(); await settle(5);
  assert.equal($('#evoReviewDiff .evo-diff-preview-line.add, #evoReviewDiff .evo-diff-preview-line.del'),null);
  assert.doesNotMatch($('#evoReviewDiff').textContent, /仅补充要求|仅移除部分要求/);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('queue confirmation abandoned by a project switch cannot issue a write', async t => {
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: { ...overview, queue: [queued] } }] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app); $('[data-evo-asset-group]').click(); await settle(5);
  $('[data-evo-queue]').click(); await settle(3);
  $('#kbDrawer [data-evo-queue-reject]').click(); await settle(2);
  app.router.get('/api/evolution/overview?projectId=project-2', { projectId: 'project-2', pending: [], recent: [], validating: [], trial: [], queue: [], health: [] });
  app.router.get('/api/evolution/findings?projectId=project-2', []);
  await app.evaluate("state.projectFilter='project-2';renderEvolutionView()"); await settle(3);
  $('#confirmOkBtn').click(); await settle(4);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
  assert.equal($('#evoDetailPage'), null);
  assert.equal($('#kbDrawer').classList.contains('open'), false);
});

const trafficTrial = { ...detail, status: 'canary', canary: { status: 'running', percent: 25, minRuns: 5, canary: { runs: 5, clean: 3, reworked: 1, failed: 1 }, control: { runs: 4, clean: 2, reworked: 1, failed: 1 } } };

test('traffic adjustment requires confirmation, no-ops at the current percent and preserves both server arm statistics', async t => {
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/proposals/change-1', body: trafficTrial }] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app); $('[data-evo-asset-group]').click(); await settle(5);
  $('[data-evo-progress-stage="canary"]').click(); await settle(4);
  const apply = $('[data-evo-traffic-apply]');
  assert.equal(apply.disabled, false);
  assert.equal($('#evoTrafficPercent').value, '25');
  apply.click(); await settle(2);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
  $('#evoTrafficPercent').value = '50'; apply.click(); await settle(2);
  assert.match($('#confirmModal').textContent, /从 25% 调整为 50%.*统计保留.*之后启动的新运行/s);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
  $('#confirmCancelBtn').click(); await settle(2);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
  app.router.post('/api/evolution/proposals/change-1/canary/traffic', () => {
    const updated = { ...trafficTrial, canary: { ...trafficTrial.canary, percent: 50 } };
    app.router.get('/api/evolution/proposals/change-1', updated);
    return updated;
  });
  apply.click(); await settle(2); $('#confirmOkBtn').click(); await settle(6);
  const writes = app.router.calls.filter(c => c.method !== 'GET');
  assert.equal(writes.length, 1);
  assert.equal(writes[0].path, '/api/evolution/proposals/change-1/canary/traffic');
  assert.deepEqual(writes[0].body, { percent: 50 });
  assert.equal($('#evoTrafficPercent').value, '50');
  assert.match($('.evo-traffic-legend').textContent, /试用版 50%.*原版 50%/s);
  const arms = [...app.document.querySelectorAll('#evoStagePanel .evo-review-trial tbody tr')];
  assert.deepEqual(arms.map(arm=>arm.cells[2].textContent),['2/4','3/5']);
  assert.equal($('#evoDetailActions [data-evo-approve]').disabled, true);
});

test('traffic controls stay disabled until live-version verification succeeds and hide when the canary base has drifted', async t => {
  let resolve;
  const app = await loadApp({ routes: [...routes,
    { method: 'GET', path: '/api/evolution/proposals/change-1', body: trafficTrial },
    { method: 'GET', path: '/api/evolution/assets/agent/developer/versions', body: () => new Promise(r => { resolve = r; }) },
  ] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app); $('[data-evo-asset-group]').click(); await settle(4);
  $('[data-evo-progress-stage="canary"]').click(); await settle(4);
  assert.equal($('[data-evo-traffic-apply]').disabled, true);
  $('[data-evo-traffic-apply]').click(); await settle(2);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
  resolve(live); await settle(4);
  assert.equal($('[data-evo-traffic-apply]').disabled, false);
  assert.equal($('#evoTrafficBlock').hidden, false);
  app.router.get('/api/evolution/assets/agent/developer/versions', { ...live, liveHash: 'changed', drifted: true });
  $('[data-evo-detail-refresh]').click(); await settle(5);
  assert.equal($('[data-evo-traffic-apply]').disabled, true);
  assert.equal($('#evoTrafficBlock').hidden, true);
  assert.match($('#evoLiveVersion').getAttribute('data-tooltip'), /正文已变化.*不能直接发布/);
  app.router.get('/api/evolution/assets/agent/developer/versions', { status: 503, body: { error: 'version unavailable' } });
  $('[data-evo-detail-refresh]').click(); await settle(5);
  assert.equal($('[data-evo-traffic-apply]').disabled, true);
  assert.match($('#evoLiveVersion').textContent, /版本核对失败/);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('released traffic is hidden until the exact live release is verified and stays hidden for replaced or unknown versions', async t => {
  const released = { ...detail, status: 'released', releasedVersion: 3, watch: { status: 'watching', minRuns: 5, current: { runs: 1, clean: 1 } } };
  let resolve;
  const app = await loadApp({ routes: [...routes,
    { method: 'GET', path: '/api/evolution/proposals/change-1', body: released },
    { method: 'GET', path: '/api/evolution/assets/agent/developer/versions', body: () => new Promise(r => { resolve = r; }) },
  ] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app); $('[data-evo-asset-group]').click(); await settle(4);
  $('[data-evo-progress-stage="watch"]').click(); await settle(4);
  assert.equal($('#evoTrafficBlock').hidden, true, 'historical release must not imply current traffic while verification is pending');
  resolve({ ...live, currentVersion: 3, liveHash: 'published-hash' }); await settle(4);
  assert.equal($('#evoTrafficBlock').hidden, false);
  assert.match($('#evoTrafficBlock').textContent, /新版 100%/);
  assert.match($('#evoLiveVersion').getAttribute('data-tooltip'), /当前仍生效/);
  for (const version of [{ ...live, currentVersion: 4 }, { ...live, currentVersion: 3, drifted: true }, { ...live, currentVersion: 3, liveHash: '' }, { ...live, currentVersion: 0 }]) {
    app.router.get('/api/evolution/assets/agent/developer/versions', version);
    $('[data-evo-detail-refresh]').click(); await settle(5);
    assert.equal($('#evoTrafficBlock').hidden, true, 'unmatched, drifted, or unknown release cannot claim 100% current traffic');
  }
  app.router.get('/api/evolution/assets/agent/developer/versions', { status: 503, body: { error: 'version unavailable' } });
  $('[data-evo-detail-refresh]').click(); await settle(5);
  assert.equal($('#evoTrafficBlock').hidden, true);
  assert.match($('#evoLiveVersion').textContent, /版本核对失败/);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('traffic adjustment confirmation cannot mutate an asset after leaving its project', async t => {
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/proposals/change-1', body: trafficTrial }] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app); $('[data-evo-asset-group]').click(); await settle(5);
  $('[data-evo-progress-stage="canary"]').click(); await settle(4);
  $('#evoTrafficPercent').value = '50'; $('[data-evo-traffic-apply]').click(); await settle(2);
  app.router.get('/api/evolution/overview?projectId=project-2', { projectId: 'project-2', pending: [], recent: [], validating: [], trial: [], queue: [], health: [] });
  app.router.get('/api/evolution/findings?projectId=project-2', []);
  await app.evaluate("state.projectFilter='project-2';renderEvolutionView()"); await settle(3);
  $('#confirmOkBtn').click(); await settle(4);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
  assert.equal($('#evoDetailPage'), null);
});

test('queue-only asset shows one waiting improvement and opens its queue without reading a nonexistent proposal or writing', async t => {
  const app = await loadApp({ routes: [...routes,
    { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: { ...overview, pending: [], recent: [], queue: [queued] } },
    { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [] },
  ] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  assert.equal(app.document.querySelectorAll('[data-evo-asset-group]').length, 1);
  assert.match($('#evoUnifiedList').textContent, /等待处理的改进.*等待开始下一项/s);
  $('[data-evo-asset-group]').click(); await settle(4);
  assert.equal($('#evoDetailPage'), null);
  assert.equal($('#kbDrawer').classList.contains('open'), true);
  assert.equal($('#kbDrawer [data-evo-queue-start]').disabled, false);
  assert.equal(app.router.calls.some(c => c.path.startsWith('/api/evolution/proposals/') || c.method !== 'GET'), false);
});

test('polling preserves an unsubmitted traffic selection and focus only while the server applied percent is unchanged', async t => {
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/proposals/change-1', body: trafficTrial }] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app); $('[data-evo-asset-group]').click(); await settle(5);
  $('[data-evo-progress-stage="canary"]').click(); await settle(4);
  $('#evoTrafficPercent').value = '50'; $('#evoTrafficPercent').focus();
  const hash = app.window.location.hash;
  await app.evaluate('evoRefresh().then(() => evoRender())'); await settle(6);
  assert.equal($('#evoTrafficPercent').value, '50', 'an unchanged server allocation must not reset an unsubmitted selection');
  assert.equal(app.document.activeElement.id, 'evoTrafficPercent');
  assert.match($('.evo-traffic-legend').textContent, /试用版 25%.*原版 75%/s);
  assert.equal(app.window.location.hash, hash);
  const updated = { ...trafficTrial, canary: { ...trafficTrial.canary, percent: 10 } };
  app.router.get('/api/evolution/proposals/change-1', updated);
  await app.evaluate('evoRefresh().then(() => evoRender())'); await settle(6);
  assert.equal($('#evoTrafficPercent').value, '10', 'server allocation changes must replace the stale unsubmitted selection');
  assert.match($('.evo-traffic-legend').textContent, /试用版 10%.*原版 90%/s);
  const arms = [...app.document.querySelectorAll('#evoStagePanel .evo-review-trial tbody tr')];
  assert.deepEqual(arms.map(arm=>arm.cells[2].textContent),['2/4','3/5']);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});
