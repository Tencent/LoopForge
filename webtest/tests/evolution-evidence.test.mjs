import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const now = new Date().toISOString();
const finding = { id: 'f-evidence', projectId: 'project-1', assetKind: 'agent', assetId: 'reviewer', assetName: '评审角色', status: 'proposed', proposalId: 'p-evidence', summary: '缺少端到端验证', triage: { verdict: 'fix', score: 2.6, probWorse: .96, rate: .4, baselineRate: .1 }, clusters: [{ id: 'e2e', label: '上线前漏测', kind: 'node_failed', count: 5, runs: 3, lastSeenAt: now }] };
const replay = { id: 'rp-fixed', sourceRunId: 'source-1', runId: 'candidate-1', sourceScore: 0, score: 2, group: 'holdout', status: 'succeeded', outcome: 'fixed', finishedAt: now };
const regressed = { ...replay, id: 'rp-regressed', sourceRunId: 'source-2', runId: 'candidate-2', sourceScore: 2, score: 0, status: 'failed', outcome: 'regressed' };
const changed = { ...replay, id: 'rp-changed', sourceRunId: 'source-3', runId: 'candidate-3', score: 0, status: 'failed', outcome: 'changed' };
const blocked = { ...replay, id: 'rp-blocked', sourceRunId: 'source-4', runId: '', score: -1, status: 'failed', outcome: 'blocked', error: '回放未能启动' };
const proposal = { id: 'p-evidence', projectId: 'project-1', findingId: finding.id, assetKind: 'agent', assetId: 'reviewer', assetName: finding.assetName, status: 'pending', baseHash: 'base', title: '补充 e2e 检查', rationale: '可能缺少验收要求 <img src=x onerror=alert(1)>', baseContent: '检查文档结构', candidateContent: '检查文档结构\n检查关键路径 e2e', summary: { total: 4, finished: 4, fixed: 1, regressed: 1, changed: 1, blocked: 1, passed: true }, replays: [replay, regressed, changed, blocked] };
const source = { id: 'source-1', executionContext: { projectId: 'project-1' }, workflowSnapshot: { projectId: 'project-1' }, input: '实现用户注册 <script>bad()</script>', nodes: { plan: { status: 'succeeded', output: '沿用的需求方案' }, check: { status: 'failed', output: '旧输出：缺少 e2e <img src=x onerror=alert(1)>' } } };
const candidate = { id: 'candidate-1', executionContext: { projectId: 'project-1' }, workflowSnapshot: { projectId: 'project-1' }, input: source.input, nodes: { plan: { status: 'succeeded', output: '沿用的需求方案' }, check: { status: 'succeeded', output: '新输出：完成端到端验收' } } };
const comparison = { replayId: replay.id, sourceRunId: replay.sourceRunId, runId: replay.runId, input: '实现用户注册', nodes: [{ id: 'plan', name: '需求澄清', reused: true, source: { status: 'succeeded' } }, { id: 'check', name: '独立评审', shaped: true, changed: true, source: { status: 'failed', reason: '缺少 e2e' }, replay: { status: 'succeeded', reason: '验收通过' } }] };
const path = `/api/evolution/proposals/${proposal.id}/replays/${replay.id}/compare`;
const routes = [
  { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: { projectId: 'project-1', pending: [proposal], health: [], trial: [], validating: [], recent: [] } },
  { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [finding] },
  { method: 'GET', path: `/api/evolution/proposals/${proposal.id}`, body: proposal },
  { method: 'GET', path: '/api/evolution/assets/agent/reviewer/versions', body: { currentVersion: 1, liveHash: 'base', drifted: false } },
  { method: 'GET', path: '/api/evolution/findings/f-evidence/runs?cluster=e2e', body: { complete: true, total: 2, items: [{ runId: source.id, nodeId: 'check', status: 'failed', detail: '缺少验收', issueTitle: '用户注册', createdAt: now }, { runId: 'other-run', status: 'failed', detail: '第二条报错', createdAt: now }] } },
  { method: 'GET', path: '/api/runs/source-1', body: source },
  { method: 'GET', path: '/api/runs/candidate-1', body: candidate },
  { method: 'GET', path: '/api/runs/other-run', body: { id: 'other-run', executionContext: { projectId: 'project-1' }, input: '其他需求', nodes: { check: { status: 'failed', error: '第二条报错的原始证据' } } } },
  { method: 'GET', path, body: comparison },
  { method: 'GET', path: `/api/evolution/proposals/${proposal.id}/replays/${regressed.id}/compare`, body: { ...comparison, replayId: regressed.id, sourceRunId: regressed.sourceRunId, runId: regressed.runId } },
  { method: 'GET', path: '/api/runs/source-2', body: { ...source, id: 'source-2' } },
  { method: 'GET', path: '/api/runs/candidate-2', body: { ...candidate, id: 'candidate-2' } },
];
async function open(app) { await app.evaluate(`state.projectFilter='project-1';switchView('evolution');renderEvolutionView()`); await settle(4); }
async function detail(app, inspection = 'validation') {
  await open(app); app.document.querySelector('[data-evo-asset-group]').click(); await settle(6);
  if (inspection) { app.document.querySelector(`[data-evo-inspect="${inspection}"]`).click(); await settle(5); }
}
const $ = (app, s) => app.document.querySelector(s);

test('problem-first list exposes real impact and object-level recommendation without technical stages', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await open(app);
  assert.match($(app, '.evo-list-row').textContent, /评审角色.*角色 · 1 类关联问题.*补充 e2e 检查.*待你确认.*1 条退化/s);
  assert.equal($(app, '.evo-list-steps'), null);
  assert.equal($(app, '.evo-list-effect'), null);
  assert.equal($(app, '.evo-list-judgment'), null);
  assert.equal($(app, '#evoInspection'), null);
  assert.equal($(app, '.evo-evidence-panel'), null);
  assert.equal(app.router.calls.some(c => c.path.startsWith('/api/runs/')), false);
  $(app, '[data-evo-asset-group]').click(); await settle(6);
  assert.match($(app, '#evoProblemRecords').textContent, /预计每周额外 2.6 次问题运行/);
  assert.match($(app, '#evoProblemRecords').textContent, /模型建议，待验证/);
  assert.equal($(app, '#evoInspection').open, false);
  assert.match($(app, '#evoChangeSummary').textContent, /从真实正文差异提取/);
  assert.equal($(app, '#evoDetailTrack').children.length, 4);
  assert.match($(app, '#evoDetailActions').textContent, /查看退化样本/);
});

test('home separates historical impact from post-release runs and keeps watching releases active', async t => {
  const released = { ...proposal, status: 'released', releasedVersion: 2, watch: { status: 'watching', minRuns: 5, current: { runs: 0 } } };
  const closed = { ...finding, status: 'resolved' };
  const app = await loadApp({ routes: [...routes,
    { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: { projectId: 'project-1', recent: [released], pending: [], trial: [], validating: [], health: [] } },
    { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [closed] },
  ] }); t.after(app.close);
  await open(app);
  assert.equal($(app, '[data-evo-list-status="active"]').getAttribute('aria-pressed'), 'true');
  assert.match($(app, '.evo-list-context').textContent, /1 类关联问题/);
  assert.match($(app, '.evo-list-progress').textContent, /已发布 v2 · 观察中.*新版本 0\/5 次.*等待新建运行/s);
  assert.doesNotMatch($(app, '.evo-list-progress').textContent, /继续观察|待开始修改/);
  $(app, '[data-evo-list-status="done"]').click();
  assert.equal($(app, '.evo-list-row'), null);
  $(app, '[data-evo-list-status="doing"]').click();
  assert.ok($(app, '[data-evo-asset-group]'));
  released.watch.current.runs = 2;
  app.router.get('/api/evolution/overview?projectId=project-1', { projectId: 'project-1', recent: [released], pending: [], trial: [], validating: [], health: [] });
  $(app, '#evoRefresh').click(); await settle(4);
  assert.match($(app, '.evo-list-context').textContent, /1 类关联问题/);
  assert.match($(app, '.evo-list-progress').textContent, /新版本 2\/5 次/);
  assert.doesNotMatch($(app, '.evo-list-progress').textContent, /等待新建运行/);
  assert.equal(app.router.calls.some(c => c.method !== 'GET' || c.path.startsWith('/api/runs/')), false);
});

test('home shows both trial arms and archives older failed suggestions without hiding them from all records', async t => {
  const trial = { ...proposal, status: 'canary', canary: { status: 'running', minRuns: 5, percent: 50, canary: { runs: 3 }, control: { runs: 1 } } };
  const old = { ...proposal, id: 'old-failed', title: '提案生成失败', status: 'failed' };
  const rejected = { ...proposal, id: 'old-rejected', status: 'rejected' };
  const app = await loadApp({ routes: [...routes,
    { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: { projectId: 'project-1', pending: [], trial: [trial], recent: [old,rejected], validating: [], health: [] } },
  ] }); t.after(app.close);
  await open(app);
  assert.equal(app.document.querySelectorAll('.evo-list-row').length, 1);
  assert.match($(app, '.evo-list-progress').textContent, /试用中.*试用版 3\/5 · 当前版 1\/5 次/s);
  assert.equal($(app, '[data-evo-proposal="old-failed"]'), null);
  $(app, '[data-evo-list-status="all"]').click();
  assert.equal(app.document.querySelectorAll('.evo-list-row').length, 1);
  assert.match($(app, '.evo-list-context').textContent, /2 次历史尝试/);
  $(app, '[data-evo-asset-group]').click(); await settle(5);
  $(app, '[data-evo-queue]').click(); await settle(3);
  assert.ok($(app, '#kbDrawer [data-evo-proposal="old-failed"]'));
  assert.ok($(app, '#kbDrawer [data-evo-proposal="old-rejected"]'));
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('home preserves actual feedback titles and does not mistake changed verdicts for fixed problems', async t => {
  const feedback = '<img src=x onerror=alert(1)> review.md 只检查文档结构，未检查关键路径的 e2e 验收用例';
  const f = { ...finding, clusters: [{ id: 'feedback', label: feedback, kind: 'user_feedback', runs: 2 }] };
  const p = { ...proposal, summary: { total: 2, finished: 2, passed: true, changed: 2, fixed: 0, regressed: 0 } };
  const app = await loadApp({ routes: [...routes,
    { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [f] },
    { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: { projectId: 'project-1', pending: [p], recent: [], trial: [], validating: [], health: [] } },
  ] }); t.after(app.close);
  await open(app);
  assert.equal($(app, '.evo-list-problem b').textContent, finding.assetName);
  assert.match($(app, '.evo-list-context').textContent, /角色 · 1 类关联问题/);
  assert.match($(app, '.evo-list-progress').textContent, /2 条结论改变，待核对/);
  assert.doesNotMatch($(app, '.evo-list-progress').textContent, /已修复/);
  assert.equal($(app, '#evoUnifiedList img'), null);
  assert.equal($(app, '#evoUnifiedList script'), null);
  $(app, '[data-evo-asset-group]').click(); await settle(5);
  $(app, '[data-evo-inspect="records"]').click(); await settle(3);
  assert.equal($(app, '[data-evo-related-finding]').textContent, feedback);
  assert.equal($(app, '#evoInspection img'), null);
});

test('run samples open in the evidence pane and arbitrary run IDs do not issue a read', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  $(app, '[data-evo-detail-tab="records"]').click(); await settle(4);
  assert.match($(app, '#evoEvidenceBody').textContent, /旧输出：缺少 e2e/);
  assert.equal($(app, '#evoEvidenceBody img'), null);
  assert.equal($(app, '#evoEvidenceBody script'), null);
  const hash = app.window.location.hash;
  $(app, '[data-evo-evidence-run="other-run"]').click(); await settle(5);
  assert.match($(app, '#evoEvidenceBody').textContent, /第二条报错的原始证据/);
  assert.equal(app.window.location.hash, hash);
  const calls = app.router.calls.length;
  await app.evaluate(`evoSelectEvidence({type:'run',id:'not-authorized'})`);
  assert.equal(app.router.calls.length, calls);
});

test('comparison uses server outcomes, shows original and replay output, and marks reused nodes', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  assert.equal(app.document.querySelectorAll('.evo-compare-row').length, 4);
  $(app, '[data-evo-sample-filter="changed"]').click();
  assert.equal(app.document.querySelectorAll('.evo-compare-row').length, 1);
  assert.match($(app, '.evo-compare-row').textContent, /结论改变/);
  assert.doesNotMatch($(app, '.evo-compare-row').textContent, /已修复/);
  $(app, '[data-evo-sample-filter="blocked"]').click();
  assert.match($(app, '.evo-compare-row').textContent, /阻塞/);
  $(app, '[data-evo-sample-filter="fixed"]').click();
  $(app, '[data-evo-evidence-replay="rp-fixed"]').click(); await settle(6);
  assert.match($(app, '.evo-output-pair').textContent, /旧输出：缺少 e2e.*新输出：完成端到端验收/s);
  assert.equal($(app, '.evo-output-pair img'), null);
  const select = $(app, '[data-evo-evidence-node]'); select.value = 'plan';
  select.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  assert.match($(app, '.evo-output-pair').textContent, /回放保存的沿用产出.*此节点未重跑/s);
  assert.equal(($(app, '.evo-output-pair').textContent.match(/沿用的需求方案/g) || []).length, 2);
  assert.match($(app, '#evoComparisonList').textContent, /环境不保证完全一致/);
  await app.evaluate(`evoLoadDetailProposal(evo.modal)`); await settle(6);
  assert.equal($(app, '[data-evo-sample-filter="fixed"]').getAttribute('aria-pressed'), 'true');
  assert.equal($(app, '[data-evo-evidence-replay="rp-fixed"]').getAttribute('aria-pressed'), 'true');
});

test('history judgment is preserved when the original run has been retried successfully', async t => {
  const retried = { ...source, status: 'succeeded', nodes: { plan: { status: 'succeeded', output: '重试后的新方案' }, check: { status: 'succeeded', output: '重试后已通过，不是历史失败输出' } } };
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/runs/source-1', body: retried }] }); t.after(app.close);
  await detail(app);
  $(app, '[data-evo-evidence-replay="rp-fixed"]').click(); await settle(5);
  assert.match($(app, '.evo-evidence-context').textContent, /已修复.*仅限本样本/);
  assert.match($(app, '.evo-replay-judgment').textContent, /验证时整次运行：失败 → 一次成功/);
  assert.match($(app, '.evo-replay-judgment').textContent, /原运行当前状态已不同.*可能已重试/);
  assert.match($(app, '.evo-output-pair section:first-child').textContent, /原运行最新记录.*不是验证时的历史快照.*成功.*重试后已通过/s);
  assert.match($(app, '.evo-output-pair section:last-child').textContent, /候选回放记录.*新输出/s);
  assert.doesNotMatch($(app, '.evo-output-pair').textContent, /旧输出/);
  const select = $(app, '[data-evo-evidence-node]'); select.value = 'plan';
  select.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  assert.match($(app, '.evo-output-pair section:first-child').textContent, /重试后的新方案/);
  assert.match($(app, '.evo-output-pair section:last-child').textContent, /沿用的需求方案/);
  assert.doesNotMatch($(app, '.evo-output-pair section:last-child').textContent, /重试后的新方案/);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('missing reused output is never filled from the original current run', async t => {
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/runs/candidate-1', body: { ...candidate, nodes: { check: candidate.nodes.check } } }] }); t.after(app.close);
  await detail(app);
  $(app, '[data-evo-evidence-replay="rp-fixed"]').click(); await settle(5);
  const select = $(app, '[data-evo-evidence-node]'); select.value = 'plan';
  select.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  assert.match($(app, '.evo-output-pair section:last-child').textContent, /未保存该节点的沿用产出/);
  assert.doesNotMatch($(app, '.evo-output-pair section:last-child').textContent, /沿用的需求方案/);
});

test('historical zero and reworked scores are preserved and missing baselines are not inferred', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  for (const [score,label] of [[0,'失败'],[1,'返工后成功'],[2,'一次成功'],[null,'未记录'],[undefined,'未记录']]) {
    const r = { ...replay, sourceScore: score };
    const html = await app.evaluate(`evoReplayJudgmentHTML(${JSON.stringify(r)}, {})`);
    const el = app.document.createElement('div'); el.innerHTML = html;
    assert.match(el.textContent, new RegExp('验证时整次运行：'+label+' → 一次成功'));
    assert.match(el.textContent, /仅表示这条历史样本的运行评分改善/);
  }
  assert.match($(app, '#evoLinkedChange').textContent, /可修改范围：角色提示词、规则正文/);
  assert.match($(app, '#evoLinkedChange').textContent, /不直接修改业务数据.*SOP.*技能.*知识库.*MCP.*代码/);
  const summary = await app.evaluate(`evoValidationSummary({summary:{total:1,finished:1,fixed:1,verdict:'通过：修好 1 条'}})`);
  assert.match(summary, /已修复 1 条/);
  assert.doesNotMatch(summary, /修好/);
});

test('missing candidate output is not filled with original output and evidence errors can retry', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  app.router.get('/api/runs/candidate-1', { status: 404, body: { error: 'run deleted' } });
  $(app, '[data-evo-evidence-replay="rp-fixed"]').click(); await settle(6);
  assert.match($(app, '.evo-output-pair section:last-child').textContent, /回放输出不可用.*run deleted/);
  assert.doesNotMatch($(app, '.evo-output-pair section:last-child').textContent, /旧输出/);
  app.router.get('/api/runs/other-run', { status: 403, body: { error: 'no access' } });
  $(app, '[data-evo-evidence-run="other-run"]').click(); await settle(5);
  assert.match($(app, '#evoEvidenceBody').textContent, /读取证据失败.*no access/);
  app.router.get('/api/runs/other-run', { id: 'other-run', executionContext: { projectId: 'project-1' }, nodes: {} });
  $(app, '[data-evo-evidence-retry]').click(); await settle(4);
  assert.match($(app, '#evoEvidenceBody').textContent, /未跑到此节点或记录不可用/);
});

test('mismatched or inaccessible comparison stops before fetching its run outputs', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  app.router.get(path, { ...comparison, runId: 'foreign-run' });
  $(app, '[data-evo-evidence-replay="rp-fixed"]').click(); await settle(4);
  assert.match($(app, '#evoEvidenceBody').textContent, /证据与当前样本不匹配/);
  assert.equal(app.router.calls.some(c => c.path === '/api/runs/candidate-1'), false);
  assert.equal(app.router.calls.some(c => c.path === '/api/runs/foreign-run'), false);
});

test('run evidence rejects foreign or unknown projects even when workflow snapshots or top-level fields match', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  for (const executionContext of [{ projectId: 'project-2' }, {}]) {
    app.router.get('/api/runs/other-run', { id: 'other-run', projectId: 'project-1', executionContext, workflowSnapshot: { projectId: 'project-1' }, nodes: { check: { output: '不应展示的其他项目输出' } } });
    $(app, '[data-evo-evidence-run="other-run"]').click(); await settle(4);
    assert.match($(app, '#evoEvidenceBody').textContent, /运行不可用或不属于当前项目/);
    assert.doesNotMatch($(app, '#evoEvidenceBody').textContent, /不应展示/);
  }
});

test('replay sides use execution context rather than matching workflow snapshots for project access', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  app.router.get('/api/runs/candidate-1', { ...candidate, projectId: 'project-1', executionContext: { projectId: 'project-2' }, nodes: { check: { output: '其他项目候选内容' } } });
  $(app, '[data-evo-evidence-replay="rp-fixed"]').click(); await settle(6);
  assert.match($(app, '.evo-output-pair section:first-child').textContent, /旧输出/);
  assert.match($(app, '.evo-output-pair section:last-child').textContent, /回放输出不可用.*不属于当前项目/);
  assert.doesNotMatch($(app, '.evo-output-pair').textContent, /其他项目候选内容/);
});

test('late comparison does not replace a newly selected sample or trigger output reads', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  let resolve;
  app.router.get(path, () => new Promise(r => { resolve = r; }));
  $(app, '[data-evo-evidence-replay="rp-fixed"]').click(); await settle(2);
  $(app, '[data-evo-evidence-run="other-run"]').click(); await settle(4);
  resolve(comparison); await settle(4);
  assert.match($(app, '#evoEvidenceBody').textContent, /第二条报错的原始证据/);
  assert.equal(app.router.calls.some(c => c.path === '/api/runs/candidate-1'), false);
});

test('late run output is discarded after changing project and cache is not shared', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  let resolve;
  app.router.get('/api/runs/other-run', () => new Promise(r => { resolve = r; }));
  $(app, '[data-evo-evidence-run="other-run"]').click(); await settle(2);
  app.router.get('/api/evolution/overview?projectId=project-2', { projectId: 'project-2', pending: [], health: [] });
  app.router.get('/api/evolution/findings?projectId=project-2', []);
  await app.evaluate(`state.projectFilter='project-2';renderEvolutionView()`);
  resolve({ id: 'other-run', executionContext: { projectId: 'project-1' }, nodes: { check: { output: '过期内容' } } }); await settle(5);
  assert.equal($(app, '#evoDetailPage'), null);
  assert.doesNotMatch($(app, '#evolutionBody').textContent, /过期内容/);
});

test('recommended review action opens a real regressed sample without publishing', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  app.router.get(`/api/evolution/proposals/${proposal.id}/replays/${regressed.id}/compare`, { ...comparison, replayId: regressed.id, sourceRunId: regressed.sourceRunId, runId: regressed.runId });
  app.router.get('/api/runs/source-2', { ...source, id: 'source-2' });
  app.router.get('/api/runs/candidate-2', { ...candidate, id: 'candidate-2' });
  $(app, '[data-evo-review-regressions]').click(); await settle(6);
  assert.equal($(app, '[data-evo-sample-filter="regressed"]').getAttribute('aria-pressed'), 'true');
  assert.equal($(app, '[data-evo-evidence-replay="rp-regressed"]').getAttribute('aria-pressed'), 'true');
  assert.match($(app, '#evoEvidenceBody').textContent, /退化/);
  assert.equal(app.router.calls.some(c => c.method === 'POST'), false);
});

test('refresh preserves unconfirmed trial inputs and expanded diagnostics', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  $(app, '#evoDiagnosisDetails').open = true;
  $(app, '#evoDetailActions [data-evo-canary]').click();
  $(app, '#evoCanaryPercent').value = '25';
  $(app, '#evoCanaryMinRuns').value = '20';
  $(app, '#evoCanaryMinRuns').focus();
  await app.evaluate(`evoLoadDetailProposal(evo.modal)`); await settle(5);
  assert.equal($(app, '#evoDetailCanary').hidden, false);
  assert.equal($(app, '#evoCanaryPercent').value, '25');
  assert.equal($(app, '#evoCanaryMinRuns').value, '20');
  assert.equal(app.document.activeElement.id, 'evoCanaryMinRuns');
  assert.equal($(app, '#evoDiagnosisDetails').open, true);
});

test('detail defaults to candidate comparison and moves one evidence pane between bounded workspaces', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  assert.equal($(app, '[data-evo-detail-tab="validation"]').getAttribute('aria-selected'), 'true');
  assert.equal($(app, '#evoDetailPanel-change').hidden, true);
  assert.equal($(app, '#evoDetailPanel-records').hidden, true);
  assert.equal($(app, '#evoRationaleDetails').open, false);
  assert.equal($(app, '#evoRunPreview').parentElement.id, 'evoProblemRecords');
  assert.equal($(app, '.evo-evidence-panel').parentElement.dataset.evoEvidenceSlot, 'validation');
  assert.equal($(app, '[data-evo-evidence-replay="rp-regressed"]').getAttribute('aria-pressed'), 'true');
  assert.match($(app, '#evoEvidenceTitle').textContent, /验证证据.*新旧对比/);
  assert.match($(app, '#evoLinkedChange').textContent, /单候选.*不会自动反馈再改.*Harness²/s);
  assert.equal($(app, '#evoDetailCanary').parentElement.id, 'evoTrialHost');
  $(app, '[data-evo-detail-tab="records"]').click(); await settle(4);
  assert.equal($(app, '#evoDetailPanel-validation').hidden, true);
  assert.equal($(app, '#evoDetailPanel-records').hidden, false);
  assert.equal($(app, '.evo-evidence-panel').parentElement.dataset.evoEvidenceSlot, 'records');
  assert.match($(app, '#evoEvidenceTitle').textContent, /原问题/);
  const reads = app.router.calls.length;
  $(app, '[data-evo-detail-tab="change"]').click(); await settle(2);
  assert.equal($(app, '#evoDetailPanel-change').hidden, false);
  assert.equal($(app, '#evoDetailPanel-records').hidden, true);
  assert.equal(app.router.calls.length, reads);
  await app.evaluate(`evoLoadDetailProposal(evo.modal)`); await settle(5);
  assert.equal($(app, '[data-evo-detail-tab="change"]').getAttribute('aria-selected'), 'true');
  assert.equal(app.document.querySelectorAll('.evo-evidence-panel').length, 1);
  $(app, '[data-evo-detail-tab="validation"]').click(); await settle(4);
  assert.equal($(app, '[data-evo-evidence-replay="rp-regressed"]').getAttribute('aria-pressed'), 'true');
  assert.match($(app, '#evoEvidenceTitle').textContent, /新旧对比/);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('a single fixed replay is the default and original issue samples do not replace it after loading', async t => {
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: `/api/evolution/proposals/${proposal.id}`, body: { ...proposal, replays: [replay] } }] }); t.after(app.close);
  await detail(app);
  assert.equal($(app, '[data-evo-evidence-replay="rp-fixed"]').getAttribute('aria-pressed'), 'true');
  assert.match($(app, '#evoEvidenceCount').textContent, /1 条验证样本/);
  assert.match($(app, '.evo-output-pair section:last-child').textContent, /新输出/);
  $(app, '[data-evo-detail-tab="records"]').click(); await settle(3);
  $(app, '[data-evo-evidence-run="other-run"]').click(); await settle(3);
  await app.evaluate(`evoLoadDetailProposal(evo.modal)`); await settle(5);
  assert.equal($(app, '[data-evo-detail-tab="records"]').getAttribute('aria-selected'), 'true');
  assert.match($(app, '#evoEvidenceBody').textContent, /第二条报错/);
});

test('simple detail contains a true diff summary, four steps, one primary action and hidden diagnostic records', async t => {
  const clean = { ...proposal, summary: { total: 1, finished: 1, fixed: 1, regressed: 0, unchanged: 0, passed: true }, replays: [replay] };
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: `/api/evolution/proposals/${proposal.id}`, body: clean }] }); t.after(app.close);
  await detail(app, '');
  const bar = $(app, '.evo-gray-decision');
  assert.match($(app, '#evoDetailObject').textContent, /评审角色.*1 类关联问题/);
  assert.match($(app, '#evoProblemRecords').textContent, /3 次运行受影响/);
  assert.equal($(app, '#evoFindingTitle').textContent, proposal.title);
  const ids = [...app.document.querySelectorAll('#evoDetailPage [id]')].map(el => el.id);
  assert.equal(new Set(ids).size, ids.length, 'detail and inspection must never duplicate IDs');
  assert.equal(bar.querySelectorAll('.evo-detail-box, h2, h3, #evoRationaleDetails').length, 0);
  assert.doesNotMatch(bar.textContent, /上线前漏测|问题判断|建议下一步|预计每周|模型建议|待你确认/);
  assert.match($(app, '#evoChangeSummary').textContent, /检查关键路径 e2e.*从真实正文差异提取.*历史验证 1 条 · 通过/s);
  assert.doesNotMatch($(app, '#evoChangeSummary').textContent, /可能缺少验收要求/);
  assert.equal($(app, '#evoDetailTrack').children.length, 4);
  assert.equal($(app, '#evoInspection').open, false);
  assert.equal($(app, '.evo-evidence-panel').closest('dialog').id, 'evoInspection');
  assert.equal([...bar.querySelectorAll('button')].filter(button => button.textContent === '先小范围试用').length, 1);
  assert.equal($(app, '#evoDetailActions').querySelectorAll('.kb-btn.primary').length, 1);
  assert.equal($(app, '#evoDetailCanary').hidden, true);
  assert.ok($(app, '#evoDetailPanel-records #evoRationaleDetails'));
  assert.equal($(app, '#evoDetailState'), null);
  $(app, '#evoRationaleDetails').open = true;
  await app.evaluate(`evoLoadDetailProposal(evo.modal)`); await settle(5);
  assert.equal($(app, '#evoRationaleDetails').open, true);
  assert.equal($(app, '#evoInspection').open, false);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('compact decision does not claim a fix for incomplete validation and distinguishes later states', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  const cases = [
    [{ status: 'pending', summary: { total: 1, finished: 0, passed: true } }, /等待有效验证结果.*暂不发布/, /历史验证通过/],
    [{ status: 'pending', summary: { total: 1, finished: 1, passed: false } }, /等待有效验证结果.*暂不发布/, /历史验证通过/],
    [{ status: 'pending', summary: { total: 2, finished: 2, passed: true, regressed: 1 } }, /历史验证含 1 条退化.*先检查退化样本/, /历史验证通过/],
    [{ status: 'validating', summary: { total: 4, finished: 2 } }, /历史验证中.*2\/4.*当前正文未改变/, /历史验证通过/],
    [{ status: 'drafting' }, /正在生成修改.*当前正文未改变/, /历史验证通过/],
    [{ status: 'canary', canary: { status: 'passed' } }, /试用通过.*不代表已经证明质量提升/, /历史验证通过/],
    [{ status: 'released', releasedVersion: 3, watch: { status: 'degraded' } }, /已发布 v3.*发布后出现退化/, /历史验证通过/],
    [{ status: 'failed' }, /未通过.*不要把失败候选/, /历史验证通过/],
    [{ status: 'rejected' }, /未采用/, /历史验证通过/],
    [{ status: 'rolled_back' }, /已回滚/, /历史验证通过/],
  ];
  for (const [p,expected,absent] of cases) {
    const html = await app.evaluate(`evoDecisionHTML(evo.modal.row, ${JSON.stringify({ ...proposal, ...p })})`);
    const el = app.document.createElement('div'); el.innerHTML = html;
    assert.match(el.textContent, expected);
    assert.doesNotMatch(el.textContent, absent);
  }
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('detail tabs support keyboard navigation without starting a mutation', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  $(app, '[data-evo-detail-tab="validation"]').focus();
  app.document.activeElement.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, cancelable: true }));
  await settle(2);
  assert.equal(app.document.activeElement.id, 'evoDetailTab-change');
  assert.equal($(app, '#evoDetailPanel-change').hidden, false);
  app.document.activeElement.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'End', bubbles: true, cancelable: true }));
  await settle(3);
  assert.equal(app.document.activeElement.id, 'evoDetailTab-records');
  assert.equal($(app, '#evoDetailPanel-records').hidden, false);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('verification failures have readable titles while raw evidence and node errors remain inspectable', async t => {
  const raw = `verification 报告结构检查 command "python3 -c 'import json; assert False'": exit status 1 <img src=x onerror=alert(1)>`;
  const gateFinding = { ...finding, status: 'open', proposalId: '', summary: `· ${raw}`, clusters: [{ ...finding.clusters[0], label: raw }] };
  const app = await loadApp({ routes: [...routes,
    { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: { projectId: 'project-1', pending: [], health: [], trial: [], validating: [], recent: [] } },
    { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [gateFinding] },
    { method: 'GET', path: '/api/evolution/findings/f-evidence/runs?cluster=e2e', body: { complete: true, total: 1, items: [{ runId: source.id, nodeId: 'check', status: 'failed', detail: raw, createdAt: now }] } },
    { method: 'GET', path: '/api/runs/source-1', body: { ...source, nodes: { check: { status: 'failed', output: '已生成报告', error: raw } } } },
  ] }); t.after(app.close);
  await open(app);
  assert.equal($(app, '.evo-list-problem b').textContent, finding.assetName);
  assert.equal($(app, '.evo-list-impact b').textContent, '报告结构检查未通过');
  $(app, '[data-evo-asset-group]').click(); await settle(6);
  $(app, '[data-evo-inspect="records"]').click(); await settle(4);
  assert.equal($(app, '#evoFindingTitle').textContent, '报告结构检查未通过');
  assert.doesNotMatch($(app, '#evoFindingEvidence').textContent, /python3|import json/);
  assert.doesNotMatch($(app, '#evoRunPreview').textContent, /python3|import json/);
  assert.equal($(app, '.evo-evidence-signal p').textContent, '报告结构检查未通过');
  assert.equal($(app, '.evo-evidence-signal details pre').textContent, raw);
  assert.match($(app, '.evo-evidence-single').textContent, /已生成报告.*节点错误.*import json/s);
  assert.equal($(app, '#evoEvidenceBody img'), null);
  assert.equal($(app, '#evoEvidenceBody script'), null);
  assert.equal($(app, '.evo-evidence-signal details').open, false);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('sample index read errors are visible in the evidence pane and retry restores samples', async t => {
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/findings/f-evidence/runs?cluster=e2e', status: 503, body: { error: 'index unavailable' } }] }); t.after(app.close);
  await detail(app);
  $(app, '[data-evo-detail-tab="records"]').click(); await settle(3);
  assert.match($(app, '#evoEvidenceBody').textContent, /读取问题样本失败.*index unavailable/);
  app.router.get('/api/evolution/findings/f-evidence/runs?cluster=e2e', { complete: true, total: 1, items: [{ runId: source.id, nodeId: 'check', detail: '验收缺失', status: 'failed' }] });
  $(app, '#evoEvidenceBody [data-evo-record-retry]').click(); await settle(6);
  assert.match($(app, '#evoEvidenceBody').textContent, /旧输出：缺少 e2e/);
});

test('inspection entry switches real evidence, retains its tab across refresh and restores focus on close', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app, '');
  assert.equal($(app, '#evoInspection').open, false);
  const records = $(app, '[data-evo-inspect="records"]'); records.click(); await settle(4);
  assert.equal($(app, '#evoInspection').open, true);
  assert.equal($(app, '[data-evo-detail-tab="records"]').getAttribute('aria-selected'), 'true');
  assert.match($(app, '#evoEvidenceTitle').textContent, /原问题/);
  assert.match($(app, '#evoEvidenceBody').textContent, /旧输出：缺少 e2e/);
  await app.evaluate('evoLoadDetailProposal(evo.modal)'); await settle(5);
  assert.equal($(app, '#evoInspection').open, true);
  assert.equal($(app, '[data-evo-detail-tab="records"]').getAttribute('aria-selected'), 'true');
  $(app, '[data-evo-inspection-close]').click();
  assert.equal($(app, '#evoInspection').open, false);
  assert.equal(app.document.activeElement.dataset.evoInspect, 'records');
  $(app, '[data-evo-inspect="validation"]').click(); await settle(4);
  assert.match($(app, '#evoEvidenceTitle').textContent, /新旧对比/);
  $(app, '#evoInspection').dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
  assert.equal($(app, '#evoInspection').open, false);
  assert.equal(app.document.activeElement.dataset.evoInspect, 'validation');
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});
