import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const now = '2026-11-13T08:00:00Z';
const finding = {
  id: 'evf-1', projectId: 'project-1', assetKind: 'agent', assetId: 'agent-1', assetName: 'Developer',
  title: '「Developer」近期 2 次节点失败', summary: '编译失败；缺少测试', severity: 'medium', status: 'open',
  signals: [{ runId: 'run-1', nodeId: 'node-1', kind: 'node_failed', detail: '编译失败', createdAt: now }],
  sampleRunIds: ['run-1'], createdAt: now, updatedAt: now,
};
const summary = { total: 3, finished: 3, fixed: 2, regressed: 0, unchanged: 1, blocked: 0, passed: true, verdict: '通过：修好 2 个，退化 0 个，无变化 1 个' };
const pending = {
  id: 'evp-1', projectId: 'project-1', findingId: 'evf-1', assetKind: 'agent', assetId: 'agent-1', assetName: 'Developer',
  title: '补充自测要求', status: 'pending', summary, baseHash: 'h', createdAt: now, updatedAt: now,
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
  projectId: 'project-1', findings: [finding], validating: [validating], pending: [pending],
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

test('evolution center renders board, proposal drawer, versions, policies and samples', async (t) => {
  const app = await loadApp({ routes: [
    { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: overview },
    { method: 'GET', path: '/api/evolution/samples?projectId=project-1', body: [{ projectId: 'project-1', runId: 'run-9', note: '典型成功', createdAt: now }] },
    { method: 'GET', path: '/api/evolution/proposals/evp-1', body: detail },
    { method: 'POST', path: '/api/evolution/proposals/evp-1/approve', body: { ...detail, status: 'released', releasedVersion: 3 } },
    { method: 'POST', path: '/api/evolution/proposals', status: 202, body: { ...validating, id: 'evp-3' } },
    { method: 'GET', path: '/api/evolution/proposals/evp-3', body: { ...validating, id: 'evp-3', replays: [] } },
    { method: 'POST', path: '/api/evolution/diagnose', body: { findings: [finding], runs: 5 } },
    { method: 'GET', path: '/api/evolution/assets/agent/agent-1/versions', body: versions },
    { method: 'POST', path: '/api/evolution/assets/agent/agent-1/rollback', body: { version: 3, source: 'rollback' } },
    { method: 'PUT', path: '/api/evolution/policies/agent/agent-1', body: { assetKind: 'agent', assetId: 'agent-1', level: 1 } },
    { method: 'DELETE', path: '/api/runs/run-9/pin-sample', body: { ok: true } },
  ] });
  t.after(app.close);
  const $ = (sel) => app.document.querySelector(sel);

  await app.evaluate(`state.projectFilter='project-1';switchView('evolution');renderEvolutionView()`);
  await settle(4);
  const body = $('#evolutionBody').textContent;
  assert.match(body, /近期 2 次节点失败/);
  assert.match(body, /补充自测要求/);
  assert.match(body, /回放 1\/4/);
  assert.equal(app.document.querySelectorAll('.evo-col').length, 5);
  assert.match(body, /试用与观察/);
  assert.match($('#evoMetrics').textContent, /待处理问题1/);

  $('[data-evo-proposal="evp-1"]').click();
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
  const approve = app.router.calls.find((c) => c.method === 'POST' && c.path === '/api/evolution/proposals/evp-1/approve');
  assert.deepEqual(approve.body, { note: '看过了', force: false });

  app.evaluate(`evoDrawerClose()`);
  $('[data-evo-finding="evf-1"]').click();
  assert.match($('#kbDrawerBody').textContent, /编译失败/);
  $('[data-evo-propose="evf-1"]').click();
  await settle(6);
  const create = app.router.calls.find((c) => c.method === 'POST' && c.path === '/api/evolution/proposals');
  assert.deepEqual(create.body, { projectId: 'project-1', findingId: 'evf-1' });

  app.evaluate(`evoDrawerClose()`);
  $('#evoDiagnose').click();
  await settle(6);
  assert.ok(app.router.calls.some((c) => c.method === 'POST' && c.path === '/api/evolution/diagnose'));

  $('[data-evo-tab="assets"]').click();
  assert.match($('#evolutionBody').textContent, /资产健康度/);
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

  app.evaluate(`evoDrawerClose()`);
  $('[data-evo-tab="samples"]').click();
  assert.match($('#evolutionBody').textContent, /典型成功/);
  $('[data-evo-unpin="run-9"]').click();
  await settle(6);
  assert.ok(app.router.calls.some((c) => c.method === 'DELETE' && c.path === '/api/runs/run-9/pin-sample'));
});

test('evolution center asks for a project and decorates run details', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  await app.evaluate(`state.projectFilter='';renderEvolutionView()`);
  assert.match(app.document.querySelector('#evolutionBody').textContent, /请先在顶部选择一个项目/);

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
