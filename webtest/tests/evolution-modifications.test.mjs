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
  assert.match($('.evo-issue-row').textContent, /空输入返回 500.*后端开发角色.*增加必填参数校验.*待你确认/s);
  assert.equal(app.router.calls.filter(c => c.path === '/api/evolution/proposals/change-1').length, 0, 'list does not fetch full bodies');
  $('[data-evo-finding]').click(); await settle(5);
  assert.equal($('#evoFindingModal [data-evo-propose]'), null, 'do not offer another proposal when linked');
  assert.match($('#evoLinkedChange').textContent, /生成建议时的正文.*候选正文/s);
  assert.equal($('#evoFindingModal img'), null);
  assert.match($('#evoLiveVersion').textContent, /当前正文与修改前快照一致/);
  assert.match($('#evoLinkedChange').textContent, /结论改变 1/);
  assert.doesNotMatch($('#evoLinkedChange').textContent, /问题未再出现/);
  assert.equal($('#evoDetailCanary').hidden, true);
  assert.equal($('#evoDetailActions [data-evo-approve]').disabled, false);
  $('#evoDetailActions [data-evo-canary]').click();
  assert.equal($('#evoDetailCanary').hidden, false);
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
  const released = { ...proposal, id: 'published', status: 'released', releasedVersion: 6, watch: { status: 'degraded' } };
  const trial = { ...proposal, id: 'trial', status: 'canary', canary: { status: 'passed', percent: 25 } };
  const rolled = { ...proposal, id: 'rolled', status: 'rolled_back', releasedVersion: 4 };
  const others = Array.from({ length: 10 }, (_, i) => ({ ...proposal, id: `old-${i}`, status: 'rejected' }));
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: { ...overview, trial: [trial, released], recent: [released, rolled, ...others] } }] }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  assert.match($('#evoMetrics').textContent, /1 项候选尚未发布；1 项试用待全量；1 项发布后变差/);
  $('#evoTab-proposals').click();
  assert.match($('#evoChangeList').textContent, /当前载入 14 项修改/);
  assert.equal(app.document.querySelectorAll('.evo-change-row').length, 10);
  $('[data-evo-change-page="1"]').click();
  assert.equal(app.document.querySelectorAll('.evo-change-row').length, 4);
  const status = $('[data-evo-change-status]'); status.value = 'rolled_back'; change(app, status);
  assert.equal(app.document.querySelectorAll('.evo-change-row').length, 1);
  assert.match($('#evoChangeList').textContent, /此修改已撤回/);
  assert.doesNotMatch($('#evoChangeList').textContent, /当前仍生效/);
  $('[data-evo-change-decisions]').click();
  assert.equal($('[data-evo-change-status]').value, 'decide');
  assert.equal(app.document.querySelectorAll('.evo-change-row').length, 3);
  assert.match($('#evoChangeList').textContent, /25% 新运行使用候选，尚未全量/);
  const input = $('[data-evo-change-search]'); input.focus(); input.value = '没有这个修改'; input.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal(app.document.activeElement, input);
  assert.match($('#evoChangeList').textContent, /没有匹配的修改记录/);
});

test('live version checks distinguish historical release, drift and unknown status', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  app.router.get('/api/evolution/proposals/change-1', { ...detail, status: 'released', releasedVersion: 3 });
  app.router.get('/api/evolution/assets/agent/developer/versions', { ...live, currentVersion: 4, liveHash: 'newer' });
  await app.evaluate(`evoOpenProposal('change-1')`); await settle(5);
  assert.match($('#evoLiveVersion').textContent, /已被替换/);
  assert.doesNotMatch($('#evoLiveVersion').textContent, /当前仍生效/);
  app.router.get('/api/evolution/assets/agent/developer/versions', { ...live, currentVersion: 3 });
  $('[data-evo-detail-refresh]').click(); await settle(4);
  assert.match($('#evoLiveVersion').textContent, /当前仍生效/);
  app.router.get('/api/evolution/proposals/change-1', detail);
  app.router.get('/api/evolution/assets/agent/developer/versions', { ...live, liveHash: 'changed' });
  $('[data-evo-detail-refresh]').click(); await settle(4);
  assert.match($('#evoLiveVersion').textContent, /不能直接发布这份旧候选/);
  assert.equal($('#evoDetailActions [data-evo-approve]').disabled, true);
  assert.equal($('#evoDetailActions [data-evo-canary]').disabled, true);
  app.router.get('/api/evolution/assets/agent/developer/versions', { status: 503, body: { error: 'unavailable' } });
  $('[data-evo-detail-refresh]').click(); await settle(4);
  assert.match($('#evoLiveVersion').textContent, /当前版本核对失败/);
  assert.doesNotMatch($('#evoLiveVersion').textContent, /当前仍生效/);
});

test('proposal errors retry and missing candidate cannot be approved or silently presented as a diff', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  app.router.get('/api/evolution/proposals/change-1', { status: 500, body: { error: '<script>bad()</script>' } });
  $('[data-evo-finding]').click(); await settle(4);
  assert.match($('#evoLinkedChange').textContent, /读取修改失败/);
  assert.equal($('#evoLinkedChange script'), null);
  assert.equal($('#evoDetailActions [data-evo-approve]'), null);
  app.router.get('/api/evolution/proposals/change-1', { ...detail, candidateContent: '' });
  $('[data-evo-detail-refresh]').click(); await settle(4);
  assert.match($('#evoLinkedChange').textContent, /没有候选正文/);
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
  app.document.querySelector('[data-evo-finding]').click(); await settle(4);
  const button = app.document.querySelector('#evoDetailActions [data-evo-propose]');
  assert.equal(button.dataset.evoPropose, finding.id);
  assert.equal(button.disabled, false);
  assert.ok(app.document.querySelector('.evo-before-after'));
  assert.match(button.textContent, /重新生成修改建议/);
});

test('late proposal and version responses do not replace a newer modal or cross a project switch', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  const $ = s => app.document.querySelector(s);
  await open(app);
  let resolveProposal;
  app.router.get('/api/evolution/proposals/late', () => new Promise(resolve => { resolveProposal = resolve; }));
  app.evaluate(`evoOpenProposal('late')`); await settle(2);
  app.evaluate(`evoOpenProposal('change-1')`); await settle(4);
  resolveProposal({ ...detail, id: 'late', title: '过期修改' }); await settle(3);
  assert.equal($('#evoFindingTitle').textContent, proposal.title);
  let resolveVersion;
  app.router.get('/api/evolution/assets/agent/developer/versions', () => new Promise(resolve => { resolveVersion = resolve; }));
  $('[data-evo-detail-refresh]').click(); await settle(3);
  app.router.get('/api/evolution/overview?projectId=project-2', { projectId: 'project-2', findings: [], pending: [], health: [] });
  app.router.get('/api/evolution/findings?projectId=project-2', []);
  await app.evaluate(`state.projectFilter='project-2';renderEvolutionView()`);
  resolveVersion(live); await settle(3);
  assert.equal($('#evoFindingModal'), null);
  assert.doesNotMatch($('#evolutionBody').textContent, /后端开发角色/);
});
