import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const now = '2026-11-13T08:00:00Z';
const triage = {
  verdict: 'fix', reason: '比项目平均差的概率 98%，每周约多 2.3 次问题运行', runs: 42, staleRuns: 2, problemRuns: 9,
  kinds: { node_failed: 6, rework: 4 }, baselineRate: 0.08, rate: 0.19, rateLow: 0.1, rateHigh: 0.28,
  probWorse: 0.98, weeklyRuns: 21, weeklyExcess: 2.3, score: 2.25,
};
const finding = {
  id: 'evf-1', projectId: 'project-1', assetKind: 'agent', assetId: 'agent-1', assetName: 'Developer',
  title: '「Developer」近期 2 次节点失败', summary: '编译失败；缺少测试', severity: 'high', status: 'open',
  signals: [{ runId: 'run-1', nodeId: 'node-1', kind: 'node_failed', detail: '编译失败', createdAt: now }],
  sampleRunIds: ['run-1'], triage,
  clusters: [{ kind: 'node_failed', nodeId: 'node-1', label: '编译失败 第 # 行', count: 4, runs: 3, runId: 'run-1' }],
  trend: [0, 0, 1, 0, 2, 0, 0, 1, 0, 0, 2, 1, 0, 2], firstSeenAt: now, lastSeenAt: now, snoozedUntil: '0001-01-01T00:00:00Z',
  createdAt: now, updatedAt: now,
};
const noisy = {
  ...finding, id: 'evf-2', assetKind: 'rule', assetId: 'rule-1', assetName: 'Commit 规范', severity: 'low', clusters: [],
  triage: { ...triage, verdict: 'noise', reason: '问题率与项目平均接近，可能是偶发', probWorse: 0.4, rate: 0.07, weeklyExcess: 0, score: 0 },
};
const watching = {
  ...finding, id: 'evf-3', assetId: 'agent-2', assetName: 'Reviewer', severity: 'medium', clusters: [],
  triage: { ...triage, verdict: 'watch', reason: '证据还不够，按当前问题率再跑约 6 次可判定', probWorse: 0.75, runsToDecide: 6, weeklyExcess: 0.6, score: 0.45 },
};
const snoozed = { ...watching, id: 'evf-4', assetId: 'agent-3', assetName: 'Planner', snoozedUntil: '2999-01-01T00:00:00Z' };
const summary = { total: 3, finished: 3, fixed: 2, regressed: 0, unchanged: 1, blocked: 0, passed: true, verdict: '通过：修好 2 个，退化 0 个，无变化 1 个' };
const pending = {
  id: 'evp-1', projectId: 'project-1', findingId: 'evf-5', assetKind: 'agent', assetId: 'agent-1', assetName: 'Developer',
  title: '补充自测要求', status: 'pending', summary, baseHash: 'b', createdAt: now, updatedAt: now,
};
const validating = { ...pending, id: 'evp-2', title: '规则补充', status: 'validating', summary: { total: 4, finished: 1, passed: false, verdict: '回放进行中 1/4' } };
const detail = {
  ...pending, rationale: '失败都缺少自测',
  baseContent: 'Build safely\nKeep it small', candidateContent: 'Build safely\nKeep it small\n<b>Run unit tests</b>',
  replays: [
    { id: 'evr-1', group: 'diagnosis', sourceRunId: 'run-1', sourceScore: 0, runId: 'run-r1', status: 'succeeded', score: 2, outcome: 'fixed' },
    { id: 'evr-2', group: 'regression', sourceRunId: 'run-2', sourceScore: 2, runId: 'run-r2', status: 'succeeded', score: 2, outcome: 'unchanged' },
    { id: 'evr-3', group: 'diagnosis', sourceRunId: 'run-3', sourceScore: 2, runId: 'run-r3', status: 'failed', score: 0, outcome: 'changed', reusedNodes: ['clarify', 'design'], error: '「独立评审」按新正文给出阻断结论：缺少 e2e 用例' },
  ],
  summary: { ...summary, total: 3, unchanged: 1, changed: 1 },
};
const overview = {
  projectId: 'project-1', findings: [noisy, watching, finding, snoozed], validating: [validating], pending: [pending],
  recent: [{ ...pending, id: 'evp-0', status: 'released', releasedVersion: 2, decidedAt: now }],
  health: [{ assetKind: 'agent', assetId: 'agent-1', assetName: 'Developer', runs: 5, succeeded: 3, failed: 2, reworked: 1, successRate: 0.6, level: 2, latestVersion: 2, openFindings: 1 }],
  policies: [], windowDays: 14, generatedAt: now,
};
const versions = {
  assetKind: 'agent', assetId: 'agent-1', assetName: 'Developer', liveContent: 'Build safely\nRun unit tests', liveHash: 'b', currentVersion: 2, drifted: false, level: 2,
  versions: [
    { assetKind: 'agent', assetId: 'agent-1', version: 2, content: 'Build safely\nRun unit tests', hash: 'bbbbbbbbbbbb', source: 'evolution', proposalId: 'evp-0', note: '补充自测要求', createdAt: now, current: true },
    { assetKind: 'agent', assetId: 'agent-1', version: 1, content: 'Build safely', hash: 'aaaaaaaaaaaa', source: 'baseline', createdAt: now },
  ],
};

function confirmPrompt(app, value = '') {
  const input = app.document.querySelector('#confirmPromptInput');
  if (input) input.value = value;
  app.document.querySelector('#confirmOkBtn').click();
}

test('evolution center renders asset list, inspection, proposals, versions, policies and samples', async (t) => {
  const app = await loadApp({ routes: [
    { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: { ...overview, pending: [], validating: [], recent: [] } },
    { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: overview.findings },
    { method: 'GET', path: '/api/evolution/findings/evf-1/runs?cluster=legacy-0', body: { complete: false, total: 3, items: [{ runId: 'run-1', createdAt: now, status: 'failed', issueTitle: '登录页改版', workflowName: '需求交付', detail: '编译失败 第 12 行' }] } },
    { method: 'GET', path: '/api/evolution/samples?projectId=project-1', body: [{ projectId: 'project-1', runId: 'run-9', note: '典型成功', createdAt: now }] },
    { method: 'GET', path: '/api/evolution/proposals/evp-1', body: detail },
    { method: 'POST', path: '/api/evolution/proposals/evp-1/approve', body: { ...detail, status: 'released', releasedVersion: 3 } },
    { method: 'POST', path: '/api/evolution/proposals', status: 202, body: { ...validating, id: 'evp-3' } },
    { method: 'GET', path: '/api/evolution/proposals/evp-3', body: { ...validating, id: 'evp-3', replays: [] } },
    { method: 'POST', path: '/api/evolution/diagnose', body: { findings: [finding], runs: 5, archived: 1 } },
    { method: 'POST', path: '/api/evolution/findings/evf-1/snooze', body: { ...finding, snoozedUntil: '2999-01-01T00:00:00Z' } },
    { method: 'POST', path: '/api/evolution/findings/evf-1/dismiss', body: { ...finding, status: 'dismissed' } },
    { method: 'GET', path: '/api/evolution/assets/agent/agent-1/versions', body: versions },
    { method: 'POST', path: '/api/evolution/assets/agent/agent-1/rollback', body: { version: 3, source: 'rollback' } },
    { method: 'PUT', path: '/api/evolution/policies/agent/agent-1', body: { assetKind: 'agent', assetId: 'agent-1', level: 1 } },
    { method: 'DELETE', path: '/api/runs/run-9/pin-sample', body: { ok: true } },
  ] });
  t.after(app.close);
  const $ = (sel) => app.document.querySelector(sel);
  const call = (method, path) => app.router.calls.find((c) => c.method === method && c.path === path);

  await app.evaluate(`state.projectFilter='project-1';switchView('evolution');renderEvolutionView()`);
  await settle(4);
  const body = () => $('#evolutionBody').textContent;
  assert.equal(app.document.querySelectorAll('#evoMetrics .evo-workspace-stat').length,3);
  assert.equal(app.document.querySelectorAll('.evo-home-row').length,4);
  const fixRow = $('[data-evo-asset-group="asset:agent:agent-1"]');
  assert.match(fixRow.textContent, /编译失败 第 # 行/);
  assert.equal(fixRow.querySelector('.evo-ci'), null);
  assert.equal($('#evoDetailPage'), null);
  assert.ok($('[data-evo-asset-group="asset:agent:agent-2"]'));
  assert.ok($('[data-evo-asset-group="asset:rule:rule-1"]'));
  const status = $('[data-evo-task-filter="status"]');
  function filter(value) {
    status.value=value;
    status.dispatchEvent(new app.window.Event('change',{bubbles:true}));
  }
  filter('doing');
  assert.equal(app.document.querySelectorAll('.evo-home-row').length,0);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
  const all = $('[data-evo-task-filter="status"]');
  all.value='all'; all.dispatchEvent(new app.window.Event('change',{bubbles:true}));

  $('[data-evo-asset-group="asset:agent:agent-1"]').click();
  await settle(4);
  const fd = $('#evoDetailPage');
  assert.equal(fd.querySelector('[role="dialog"]'), null);
  assert.equal($('#evoPageHead').hidden, true);
  assert.equal(fd.querySelectorAll('.evo-detail-track li').length,6);
  assert.equal(fd.querySelector('#evoEvidenceDialog').open,false);
  $('#evoProblemEvidenceDetails').open=true;
  $('#evoSourceDetails').open=true;
  assert.match(fd.textContent, /编译失败/);
  assert.match(fd.textContent, /98%/);
  assert.match(fd.textContent, /另有 2 次运行用的是旧版本/);
  assert.match(fd.textContent, /登录页改版/);
  assert.ok(!$('#kbDrawer').classList.contains('open'));
  assert.ok($('#evoDetailPage [data-evo-propose="evf-1"]').classList.contains('primary'));
  $('[data-evo-list-back]').click(); await settle(4);
  assert.equal($('#evoDetailPage'), null);
  assert.equal(app.document.activeElement.dataset.evoAssetGroup, 'asset:agent:agent-1');
  $('[data-evo-asset-group="asset:agent:agent-1"]').click();
  await settle(2);
  $('#evoProblemEvidenceDetails').open=true;
  $('#evoSourceDetails').open=true;
  $('#evoDiagnosisDetails').open = true;
  $('[data-evo-snooze="evf-1"]').click();
  await settle(6);
  assert.deepEqual(call('POST', '/api/evolution/findings/evf-1/snooze').body, { days: 7 });
  $('#evoProblemEvidenceDetails').open=true;
  $('#evoSourceDetails').open=true;
  $('#evoDiagnosisDetails').open = true;
  $('[data-evo-dismiss="evf-1"]').click();
  await settle(2);
  confirmPrompt(app, '外部依赖故障');
  await settle(6);
  assert.deepEqual(call('POST', '/api/evolution/findings/evf-1/dismiss').body, { reason: '外部依赖故障' });

  $('[data-evo-asset-group="asset:agent:agent-1"]').click();
  $('[data-evo-propose="evf-1"]').click();
  await settle(6);
  assert.deepEqual(call('POST', '/api/evolution/proposals').body, { projectId: 'project-1', findingId: 'evf-1' });
  app.evaluate(`evoCloseFinding();evoDrawerClose()`);

  app.router.get('/api/evolution/overview?projectId=project-1', { ...overview, validating: [{ ...validating, assetId: 'agent-4' }] });
  await app.evaluate('evoReload(document.querySelector("#evoRefresh"))'); await settle(5);
  assert.match(body(), /补充自测要求/);
  assert.match(body(), /验证中/);
  assert.equal(app.document.querySelectorAll('#evoUnifiedList .evo-home-row').length, 5);

  $('[data-evo-asset-group="asset:agent:agent-1"]').click();
  await settle(4);
  assert.match($('#evoValidationResults').textContent, /验证通过/);
  assert.match($('#evoStoryEffects').textContent, /条可对比/);
  assert.match($('#evoDetailActions').textContent, /小范围试用/);
  assert.equal(app.document.querySelectorAll('.evo-detail-track li').length,6);
  assert.ok($('#evoDetailPage .evo-diff-preview-line.add'));
  assert.ok(!$('#evoDetailPage').innerHTML.includes('<b>Run unit tests</b>'));
  $('[data-evo-process="evp-1"]').click();
  await settle(4);
  const drawer = $('#kbDrawerBody');
  assert.ok($('#kbDrawer').classList.contains('evo-wide'));
  assert.equal(drawer.querySelectorAll('[data-evo-stage]').length, 6);
  $('[data-evo-stage="diagnose"]').click();
  assert.match(drawer.textContent, /失败都缺少自测/);
  $('[data-evo-stage="replay"]').click();
  assert.match(drawer.textContent, /诊断样本/);
  const changedReplay = drawer.querySelectorAll('.evo-replay')[2];
  assert.match(changedReplay.textContent, /给出阻断结论/);
  assert.match(changedReplay.textContent, /沿用原运行的 2 个上游节点/);
  assert.match(drawer.querySelector('.evo-count-row').textContent, /结论改变 1/);
  $('[data-evo-stage="draft"]').click();
  assert.ok(drawer.querySelector('.evo-diff-line.add'), 'candidate lines are highlighted');
  assert.ok(!drawer.innerHTML.includes('<b>Run unit tests</b>'), 'candidate text is escaped');
  $('[data-evo-approve="evp-1"]').click();
  await settle(2);
  confirmPrompt(app, '看过了');
  await settle(6);
  assert.deepEqual(call('POST', '/api/evolution/proposals/evp-1/approve').body, { note: '看过了', force: false });

  app.evaluate(`evoCloseFinding();evoDrawerClose()`);
  $('#evoDiagnose').click();
  await settle(6);
  assert.ok(call('POST', '/api/evolution/diagnose'));

  $('[data-evo-tab="assets"]').click();
  assert.match($('#evolutionBody').textContent, /角色与规则的进化方式/);
  assert.match($('#evolutionBody').textContent, /60%/);
  $('[data-evo-level="1"][data-id="agent-1"]').click();
  await settle(6);
  const policy = app.router.calls.find((c) => c.method === 'PUT' && c.path === '/api/evolution/policies/agent/agent-1');
  assert.deepEqual(policy.body, { level: 1 });

  $('[data-evo-asset="agent"][data-id="agent-1"]').click();
  await settle(4);
  assert.match($('#kbDrawerBody').textContent, /进化发布/);
  assert.match($('#kbDrawerBody').textContent, /基线/);
  $('[data-evo-version="1"]').click();
  await settle(4);
  assert.ok($('#kbDrawerBody .evo-diff-line.add'));
  $('[data-evo-rollback="1"]').click();
  await settle(2);
  confirmPrompt(app, '');
  await settle(6);
  const rollback = app.router.calls.find((c) => c.method === 'POST' && c.path === '/api/evolution/assets/agent/agent-1/rollback');
  assert.deepEqual(rollback.body, { version: 1, note: '' });

  app.evaluate(`evoCloseFinding();evoDrawerClose()`);
  $('[data-evo-tab="assets"]').click();
  $('[data-evo-tab="samples"]').click();
  assert.match($('#evolutionBody').textContent, /典型成功/);
  $('[data-evo-unpin="run-9"]').click();
  await settle(6);
  assert.ok(app.router.calls.some((c) => c.method === 'DELETE' && c.path === '/api/runs/run-9/pin-sample'));
});

test('issue modal isolates clusters, paginates, escapes text and rejects stale responses', async (t) => {
  const f = { ...finding, clusters: [
    { id: 'first', kind: 'node_failed', nodeId: 'code', label: 'Compile <img src=x onerror=alert(1)>', count: 13, runs: 12 },
    { id: 'second', kind: 'rework', nodeId: 'review', label: 'Missing e2e tests', count: 2, runs: 1 },
  ] };
  const records = Array.from({ length: 12 }, (_, i) => ({ runId: `run-${i}`, createdAt: now, status: i === 11 ? 'succeeded' : 'failed', issueTitle: '<script>bad()</script>', detail: 'Compile failed', nodeId: 'code' }));
  const app = await loadApp({ routes: [
    { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: { ...overview, validating: [], pending: [], recent: [] } },
    { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [f] },
    { method: 'GET', path: '/api/evolution/findings/evf-1/runs?cluster=first', body: { items: records, complete: true, total: 12 } },
    { method: 'GET', path: '/api/evolution/findings/evf-1/runs?cluster=second', body: { items: [{ runId: 'other-run', status: 'reworked', detail: 'Missing e2e' }], complete: true, total: 1 } },
  ] });
  t.after(app.close);
  const $ = s => app.document.querySelector(s);
  const event = type => new app.window.Event(type, { bubbles: true });
  await app.evaluate(`state.projectFilter='project-1';switchView('evolution');renderEvolutionView()`);
  await settle(4);
  assert.equal(app.document.querySelectorAll('[data-evo-asset-group="asset:agent:agent-1"]').length, 1);
  assert.match($('#evoUnifiedList').textContent, /Compile/);
  assert.equal($('#evoUnifiedList img'), null);
  $('[data-evo-asset-group="asset:agent:agent-1"]').click();
  await settle(4);
  $('#evoProblemEvidenceDetails').open=true;
  $('#evoSourceDetails').open=true;
  $('#evoRunDetails').open = true;
  assert.equal($('#evoDetailPage img'), null);
  assert.equal($('#evoFindingRecords script'), null);
  assert.equal(app.document.querySelectorAll('#evoFindingRecords tbody tr').length, 10);
  $('[data-evo-record-page="1"]').click();
  assert.equal(app.document.querySelectorAll('#evoFindingRecords tbody tr').length, 2);
  const status = $('[data-evo-record-status]');
  status.value = 'succeeded'; status.dispatchEvent(event('change'));
  assert.equal(app.document.querySelectorAll('#evoFindingRecords tbody tr').length, 1);
  assert.ok($('[data-evo-record-run="run-11"]'));
  status.value = 'unavailable'; status.dispatchEvent(event('change'));
  assert.match($('#evoFindingRecords').textContent, /没有符合条件/);
  assert.equal(app.document.body.style.overflow, '');
  $('[data-evo-list-back]').click(); await settle(4);
  assert.equal($('#evoDetailPage'), null);
  assert.equal(app.document.activeElement.dataset.evoAssetGroup, 'asset:agent:agent-1');

  let resolveFirst;
  app.router.get('/api/evolution/findings/evf-1/runs?cluster=first', () => new Promise(resolve => { resolveFirst = resolve; }));
  $('[data-evo-asset-group="asset:agent:agent-1"]').click();
  await settle(2);
  $('#evoProblemEvidenceDetails').open=true;
  $('#evoSourceDetails').open=true;
  $('[data-evo-related-finding="evf-1"][data-cluster="second"]').click();
  await settle(3);
  resolveFirst({ items: records, complete: true, total: 12 });
  await settle(3);
  assert.match($('#evoFindingTitle').textContent, /Missing e2e/);
  assert.ok($('[data-evo-record-run="other-run"]'));
  assert.equal($('[data-evo-record-run="run-0"]'), null, 'late response must not overwrite another cluster');
  app.evaluate(`showRunDetail=(id)=>{window.__openedRun=id}`);
  $('[data-evo-record-run="other-run"]').click();
  assert.equal(app.window.__openedRun, 'other-run');
  assert.equal($('#evoDetailPage'), null);
  assert.notEqual(app.document.body.style.overflow, 'hidden');

  app.evaluate(`switchView('evolution');evoOpenFinding('evf-1','second')`);
  await settle(3);
  $('#evoProblemEvidenceDetails').open=true;
  $('#evoSourceDetails').open=true;
  app.router.get('/api/evolution/findings/evf-1/runs?cluster=second', { status: 500, body: { error: 'temporary failure' } });
  app.evaluate(`evoLoadFindingRuns(evo.modal)`);
  await settle(3);
  assert.match($('#evoFindingRecords').textContent, /temporary failure/);
  assert.ok($('[data-evo-record-retry]'));
  app.router.get('/api/evolution/findings/evf-1/runs?cluster=second', { items: [], complete: true, total: 0 });
  $('[data-evo-record-retry]').click();
  await settle(3);
  assert.match($('#evoFindingRecords').textContent, /没有符合条件/);
  app.router.get('/api/evolution/overview?projectId=project-2', { projectId: 'project-2', findings: [], health: [] });
  app.router.get('/api/evolution/findings?projectId=project-2', []);
  await app.evaluate(`state.projectFilter='project-2';renderEvolutionView()`);
  assert.equal($('#evoDetailPage'), null, 'project change closes modal');
});

test('evolution center asks for a project and decorates run details', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  await app.evaluate(`state.projectFilter='';renderEvolutionView()`);
  assert.match(app.document.querySelector('#evolutionBody').textContent, /请先在顶部选择项目/);

  const host = app.document.createElement('div');
  host.innerHTML = '<div class="run-header-actions"></div><div class="run-header-meta"></div>';
  app.window.__evoHost = host;
  app.evaluate(`evoDecorateRunDetail(window.__evoHost,{id:'run-1',status:'failed',executionContext:{projectId:'project-1'}})`);
  assert.ok(host.querySelector('[data-evo-feedback="run-1"]'));
  assert.ok(host.querySelector('[data-evo-pin="run-1"]'));
  host.querySelector('[data-evo-pin="run-1"]').click();
  await settle(2);
  confirmPrompt(app, '守住');
  await settle(6);
  const pin = app.router.calls.find((c) => c.method === 'POST' && c.path === '/api/runs/run-1/pin-sample');
  assert.deepEqual(pin.body, { note: '守住' });

  const replay = app.document.createElement('div');
  replay.innerHTML = '<div class="run-header-actions"></div><div class="run-header-meta"></div>';
  app.window.__evoReplay = replay;
  app.evaluate(`evoDecorateRunDetail(window.__evoReplay,{id:'run-2',status:'succeeded',executionContext:{projectId:'project-1',purpose:'evolution_replay'}})`);
  assert.ok(replay.querySelector('.evo-replay-chip'));
  assert.equal(replay.querySelector('[data-evo-pin]'), null);
  assert.equal(app.evaluate(`evoLineDiff('a\\nb','a\\nc').map(x=>x[0]).join(',')`), 'ctx,del,add');
});


test('evolution settings keep shared assets report-only and L3 disabled until deployment opt-in', async (t) => {
  const app = await loadApp({ routes: [
    { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: { ...overview, autoEnabled: false, health: [
      { assetKind: 'agent', assetId: 'private', assetName: 'Private', level: 3 },
      { assetKind: 'rule', assetId: 'shared', assetName: 'Shared', level: 2, readOnlyReason: '共享规则仅供诊断' },
    ] } },
    { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [] },
    { method: 'GET', path: '/api/evolution/samples?projectId=project-1', body: [] },
  ] });
  t.after(app.close);
  await app.evaluate(`(async()=>{state.projectFilter='project-1';switchView('evolution');await renderEvolutionView();evo.tab='assets';evoRender()})()`);
  await settle(4);
  const button = (id, level) => app.document.querySelector(`[data-evo-level="${level}"][data-id="${id}"]`);
  assert.equal(button('private', 3).disabled, true);
  assert.equal(button('private', 2).disabled, false);
  assert.equal(button('private', 2).classList.contains('active'), true);
  assert.equal(button('shared', 2).disabled, true);
  assert.match(button('shared', 2).title, /仅供诊断/);
  await app.evaluate(`evo.overview.autoEnabled=true;evoRender()`);
  assert.equal(button('private', 3).disabled, false);
  assert.equal(button('shared', 3).disabled, true);
});
