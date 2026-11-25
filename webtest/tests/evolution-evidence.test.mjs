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
async function detail(app) {
  await open(app); app.document.querySelector('[data-evo-asset-group]').click(); await settle(6);
}
const $ = (app, s) => app.document.querySelector(s);
function issueRecords(app) {
  for (const id of ['evoProblemEvidenceDetails','evoSourceDetails','evoRunDetails']) $(app, '#'+id).open=true;
}
function evidence(app,type,id) {
  if ($(app, '#evoEvidenceDialog').open) $(app, '[data-evo-evidence-close]').click();
  if (type==='run') issueRecords(app);
  $(app, `[data-evo-${type==='run'?'evidence-run':'story-replay'}="${id}"]`).click();
  $(app, '#evoEvidenceDetails').open=true;
}
function selectNode(app,id) {
  const select = $(app, '[data-evo-evidence-node]');
  select.value=id;
  select.dispatchEvent(new app.window.Event('change',{bubbles:true}));
}

test('task rows show the problem and change, with diagnosis and the diff on detail', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await open(app);
  assert.match($(app, '.evo-home-row').textContent, /上线前漏测.*评审角色.*补充 e2e 检查/s);
  assert.equal($(app, '#evoDetailPage'), null);
  assert.equal(app.router.calls.some(c => c.path.startsWith('/api/runs/')), false);
  $(app, '[data-evo-asset-group]').click(); await settle(6);
  assert.match($(app, '#evoProblemRecords').textContent, /96%/);
  assert.match($(app, '#evoLinkedChange .evo-diff-preview-line.add').textContent, /检查关键路径 e2e/);
  assert.equal(app.document.querySelectorAll('.evo-detail-track li').length, 6);
  assert.ok($(app, '[data-evo-review-regressions]'));
});

test('a release under observation appears in the task and release lists', async t => {
  const released = { ...proposal, status: 'released', releasedVersion: 2, watch: { status: 'watching', minRuns: 5, current: { runs: 0 } } };
  const app = await loadApp({ routes: [...routes,
    { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: { projectId: 'project-1', recent: [released], pending: [], trial: [], validating: [], health: [] } },
    { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [{...finding,status:'resolved'}] },
    { method: 'GET', path: '/api/evolution/proposals/p-evidence', body: released },
  ] }); t.after(app.close);
  await open(app);
  assert.match($(app, '.evo-home-next').textContent, /观察中/);
  $(app, '[data-evo-list-section="released"]').click();
  $(app, '[data-evo-proposal="p-evidence"]').click(); await settle(5);
  $(app, '[data-evo-progress-stage="watch"]').click(); await settle(4);
  assert.match($(app, '#evoStagePanel').textContent, /至少 5 次运行/);
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
  assert.equal(app.document.querySelectorAll('.evo-home-row').length, 1);
  assert.match($(app, '.evo-home-next').textContent, /试用/);
  assert.equal($(app, '[data-evo-proposal="old-failed"]'), null);
  $(app, '[data-evo-asset-group]').click(); await settle(5);
  $(app, '[data-evo-queue]').click(); await settle(3);
  assert.ok($(app, '#kbDrawer [data-evo-proposal="old-failed"]'));
  assert.ok($(app, '#kbDrawer [data-evo-proposal="old-rejected"]'));
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('a feedback title is escaped and a changed verdict is labeled separately', async t => {
  const feedback = '<img src=x onerror=alert(1)> review.md 缺少验收用例';
  const f = { ...finding, clusters: [{ id: 'feedback', label: feedback, kind: 'user_feedback', runs: 2 }] };
  const p = { ...proposal, replays: [changed], summary: { total: 1, finished: 1, changed: 1, fixed: 0 } };
  const app = await loadApp({ routes: [...routes,
    { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [f] },
    { method: 'GET', path: '/api/evolution/proposals/p-evidence', body: p },
  ] }); t.after(app.close);
  await detail(app);
  assert.match($(app, '#evoFindingTitle').textContent,/^<img/);
  assert.equal($(app, '#evoDetailPage img'), null);
  assert.equal($(app, '[data-evo-story-row="rp-changed"] [data-outcome]').textContent,'结论变化');
});

test('run samples open in the evidence pane and arbitrary run IDs do not issue a read', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  evidence(app, 'run', 'source-1'); await settle(4);
  assert.match($(app, '#evoEvidenceBody').textContent, /旧输出：缺少 e2e/);
  assert.equal($(app, '#evoEvidenceBody img'), null);
  assert.equal($(app, '#evoEvidenceBody script'), null);
  const hash = app.window.location.hash;
  evidence(app, 'run', 'other-run'); await settle(5);
  assert.match($(app, '#evoEvidenceBody').textContent, /第二条报错的原始证据/);
  assert.equal(app.window.location.hash, hash);
  const calls = app.router.calls.length;
  await app.evaluate(`evoSelectEvidence({type:'run',id:'not-authorized'})`);
  assert.equal(app.router.calls.length, calls);
});

test('comparison uses server outcomes, shows original and replay output, and marks reused nodes', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  assert.equal(app.document.querySelectorAll('[data-evo-story-row]').length, 4);
  $(app, '[data-evo-sample-filter="changed"]').click();
  assert.equal(app.document.querySelectorAll('[data-evo-story-row]').length, 1);
  assert.match($(app, '[data-evo-story-row]').textContent, /结论变化/);
  assert.doesNotMatch($(app, '[data-evo-story-row]').textContent, /已修复/);
  $(app, '[data-evo-sample-filter="blocked"]').click();
  assert.match($(app, '[data-evo-story-row]').textContent, /阻塞/);
  $(app, '[data-evo-sample-filter="fixed"]').click();
  evidence(app, 'replay', 'rp-fixed'); await settle(6);
  assert.match($(app, '.evo-output-pair').textContent, /旧输出：缺少 e2e.*新输出：完成端到端验收/s);
  assert.equal($(app, '.evo-output-pair img'), null);
  selectNode(app,'plan');
  assert.match($(app, '.evo-output-pair').textContent, /沿用的需求方案/);
  assert.equal(($(app, '.evo-output-pair').textContent.match(/沿用的需求方案/g) || []).length, 2);
  assert.match($(app, '#evoEvidenceBody').textContent, /回放时未重跑/);
  await app.evaluate(`evoLoadDetailProposal(evo.modal)`); await settle(6);
  assert.equal($(app, '[data-evo-sample-filter="fixed"]').getAttribute('aria-pressed'), 'true');
  assert.equal($(app, '[data-evo-story-replay="rp-fixed"]').getAttribute('aria-pressed'), 'true');
});

test('history judgment is preserved when the original run has been retried successfully', async t => {
  const retried = { ...source, status: 'succeeded', nodes: { plan: { status: 'succeeded', output: '重试后的新方案' }, check: { status: 'succeeded', output: '重试后已通过，不是历史失败输出' } } };
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/runs/source-1', body: retried }] }); t.after(app.close);
  await detail(app);
  evidence(app, 'replay', 'rp-fixed'); await settle(5);
  assert.match($(app, '#evoStoryBehavior').textContent, /原运行 失败.*回放 一次成功/s);
  assert.match($(app, '.evo-replay-judgment').textContent, /验证时整次运行：失败 → 一次成功/);
  assert.match($(app, '.evo-replay-judgment').textContent, /原运行当前状态已不同.*可能已重试/);
  assert.match($(app, '.evo-output-pair section:first-child').textContent, /原运行.*成功.*重试后已通过/s);
  assert.match($(app, '.evo-output-pair section:last-child').textContent, /修改稿回放.*新输出/s);
  assert.doesNotMatch($(app, '.evo-output-pair').textContent, /旧输出/);
  selectNode(app,'plan');
  assert.match($(app, '.evo-output-pair section:first-child').textContent, /重试后的新方案/);
  assert.match($(app, '.evo-output-pair section:last-child').textContent, /沿用的需求方案/);
  assert.doesNotMatch($(app, '.evo-output-pair section:last-child').textContent, /重试后的新方案/);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('missing reused output is never filled from the original current run', async t => {
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/runs/candidate-1', body: { ...candidate, nodes: { check: candidate.nodes.check } } }] }); t.after(app.close);
  await detail(app);
  evidence(app, 'replay', 'rp-fixed'); await settle(5);
  selectNode(app,'plan');
  assert.match($(app, '.evo-output-pair section:last-child').textContent, /未跑到此节点或无记录/);
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
  const summary = await app.evaluate(`evoValidationSummary({summary:{total:1,finished:1,fixed:1,verdict:'通过：修好 1 条'}})`);
  assert.match(summary, /已修复 1 条/);
  assert.doesNotMatch(summary, /修好/);
});

test('missing candidate output is not filled with original output and evidence errors can retry', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  app.router.get('/api/runs/candidate-1', { status: 404, body: { error: 'run deleted' } });
  evidence(app, 'replay', 'rp-fixed'); await settle(6);
  assert.match($(app, '.evo-output-pair section:last-child').textContent, /记录不可用.*run deleted/);
  assert.doesNotMatch($(app, '.evo-output-pair section:last-child').textContent, /旧输出/);
  app.router.get('/api/runs/other-run', { status: 403, body: { error: 'no access' } });
  evidence(app, 'run', 'other-run'); await settle(5);
  assert.match($(app, '#evoEvidenceBody').textContent, /读取证据失败.*no access/);
  app.router.get('/api/runs/other-run', { id: 'other-run', executionContext: { projectId: 'project-1' }, nodes: {} });
  $(app, '[data-evo-evidence-retry]').click(); await settle(4);
  assert.match($(app, '#evoEvidenceBody').textContent, /未跑到此节点或无记录/);
});

test('mismatched or inaccessible comparison stops before fetching its run outputs', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  app.router.get(path, { ...comparison, runId: 'foreign-run' });
  evidence(app, 'replay', 'rp-fixed'); await settle(4);
  assert.match($(app, '#evoEvidenceBody').textContent, /证据与当前样本不匹配/);
  assert.equal(app.router.calls.some(c => c.path === '/api/runs/candidate-1'), false);
  assert.equal(app.router.calls.some(c => c.path === '/api/runs/foreign-run'), false);
});

test('run evidence rejects foreign or unknown projects even when workflow snapshots or top-level fields match', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  for (const executionContext of [{ projectId: 'project-2' }, {}]) {
    app.router.get('/api/runs/other-run', { id: 'other-run', projectId: 'project-1', executionContext, workflowSnapshot: { projectId: 'project-1' }, nodes: { check: { output: '不应展示的其他项目输出' } } });
    evidence(app, 'run', 'other-run'); await settle(4);
    assert.match($(app, '#evoEvidenceBody').textContent, /运行不可用或不属于当前项目/);
    assert.doesNotMatch($(app, '#evoEvidenceBody').textContent, /不应展示/);
  }
});

test('replay sides use execution context rather than matching workflow snapshots for project access', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  app.router.get('/api/runs/candidate-1', { ...candidate, projectId: 'project-1', executionContext: { projectId: 'project-2' }, nodes: { check: { output: '其他项目候选内容' } } });
  evidence(app, 'replay', 'rp-fixed'); await settle(6);
  assert.match($(app, '.evo-output-pair section:first-child').textContent, /旧输出/);
  assert.match($(app, '.evo-output-pair section:last-child').textContent, /记录不可用.*不属于当前项目/);
  assert.doesNotMatch($(app, '.evo-output-pair').textContent, /其他项目候选内容/);
});

test('late comparison does not replace a newly selected sample or trigger output reads', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  let resolve;
  app.router.get(path, () => new Promise(r => { resolve = r; }));
  evidence(app, 'replay', 'rp-fixed'); await settle(2);
  evidence(app, 'run', 'other-run'); await settle(4);
  resolve(comparison); await settle(4);
  assert.match($(app, '#evoEvidenceBody').textContent, /第二条报错的原始证据/);
  assert.equal(app.router.calls.some(c => c.path === '/api/runs/candidate-1'), false);
});

test('late run output is discarded after changing project and cache is not shared', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  let resolve;
  app.router.get('/api/runs/other-run', () => new Promise(r => { resolve = r; }));
  evidence(app, 'run', 'other-run'); await settle(2);
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
  assert.equal($(app, '[data-evo-story-replay="rp-regressed"]').getAttribute('aria-pressed'), 'true');
  assert.match($(app, '#evoStoryBehavior').textContent, /退化/);
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
  assert.equal($(app, '#evoDetailCanary').open, true);
  assert.equal($(app, '#evoCanaryPercent').value, '25');
  assert.equal($(app, '#evoCanaryMinRuns').value, '20');
  assert.equal(app.document.activeElement.id, 'evoCanaryMinRuns');
  assert.equal($(app, '#evoDiagnosisDetails').open, true);
});

test('detail renders cases without loading run evidence until selected', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  assert.equal($(app, '#evoEvidenceDialog').open,false);
  assert.equal(app.document.querySelectorAll('[data-evo-story-row]').length,4);
  assert.equal(app.router.calls.some(c => c.path.startsWith('/api/runs/')||c.path.endsWith('/compare')),false);
  evidence(app,'replay','rp-regressed'); await settle(5);
  assert.equal($(app, '#evoEvidenceDialog').open,true);
  assert.equal($(app, '[data-evo-story-replay="rp-regressed"]').getAttribute('aria-pressed'),'true');
  assert.ok(app.router.calls.some(c=>c.path==='/api/runs/candidate-2'));
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('a single replay stays visible when problem evidence is opened and refreshed', async t => {
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/proposals/p-evidence', body: { ...proposal, replays:[replay] } }] }); t.after(app.close);
  await detail(app);
  assert.equal(app.document.querySelectorAll('[data-evo-story-row]').length,1);
  evidence(app,'replay','rp-fixed'); await settle(5);
  assert.match($(app, '.evo-output-pair section:last-child').textContent,/新输出/);
  evidence(app,'run','other-run'); await settle(4);
  await app.evaluate('evoLoadDetailProposal(evo.modal)'); await settle(5);
  assert.equal($(app, '#evoEvidenceDialog').open,true);
  assert.match($(app, '#evoEvidenceBody').textContent,/第二条报错/);
  assert.equal(app.document.querySelectorAll('[data-evo-story-row]').length,1);
});

test('detail shows the true diff, six steps and one primary action', async t => {
  const clean = { ...proposal, summary: { total: 1, finished: 1, fixed: 1, passed: true }, replays: [replay] };
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/proposals/p-evidence', body: clean }] }); t.after(app.close);
  await detail(app);
  assert.match($(app, '#evoDetailObject').textContent,/评审角色/);
  const title = $(app, '#evoFindingTitle');
  assert.match(title.textContent, /上线前漏测/);
  assert.ok(title.compareDocumentPosition($(app, '#evoStoryEffects')) & app.window.Node.DOCUMENT_POSITION_FOLLOWING);
  assert.equal($(app, '#evoProblemOverview'), null);
  assert.ok($(app, '#evoValidationResults').compareDocumentPosition($(app, '#evoProblemEvidenceDetails')) & app.window.Node.DOCUMENT_POSITION_FOLLOWING);
  const ids = [...app.document.querySelectorAll('#evoDetailPage [id]')].map(el => el.id);
  assert.equal(new Set(ids).size, ids.length);
  assert.match($(app, '#evoLinkedChange .evo-diff-preview-line.add').textContent,/检查关键路径 e2e/);
  assert.equal(app.document.querySelectorAll('.evo-detail-track li').length,6);
  assert.equal($(app, '#evoEvidenceDialog').open,false);
  assert.equal($(app, '#evoProblemEvidenceDetails').open,false);
  assert.equal(app.document.querySelectorAll('#evoDetailActions .kb-btn.primary').length,1);
  assert.equal($(app, '#evoDetailCanary').open,false);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('release advice shows the saved evaluation and uses the current rollout and baseline', async t => {
  const releaseEvaluation = {
    replayId: replay.id, sourceRunId: source.id, replayRunId: candidate.id,
    scores: { before: 64, after: 90, threshold: 75, model: 'recorded-model',
      dimensions: [{ name: '工具浪费', weight: 20, before: 60, after: 92 }] },
  };
  const canary = { ...proposal, status: 'canary', replays: [replay], summary: { total: 1, finished: 1, fixed: 1, passed: true },
    canary: { status: 'running', minRuns: 5 }, releaseEvaluation };
  const proposalPath = '/api/evolution/proposals/p-evidence';
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: proposalPath, body: canary }] });
  t.after(app.close);
  await detail(app);
  assert.match($(app, '#evoReleaseAdvice').textContent, /继续试用/);
  assert.match($(app, '.evo-release-score').textContent, /64.*90.*\+26/s);
  assert.match($(app, '#evoStoryRelease').textContent, /通过线 75 分/);
  assert.equal($(app, '#evoReleaseEvalDetails').open, false);
  $(app, '#evoReleaseEvalDetails').open = true;
  assert.match($(app, '#evoReleaseEvalDetails tbody tr').textContent, /工具浪费.*20%.*60.*92/s);
  assert.equal($(app, '#evoDetailActions [data-evo-approve]').disabled, true);
  assert.ok($(app, '.evo-story-layout .evo-story-main #evoValidationResults'));
  assert.ok($(app, '.evo-story-layout .evo-story-rail #evoStoryRelease'));
  assert.doesNotMatch($(app, '#evoDetailPage').textContent, /当前状态|试用摘要/);

  const passed = { ...canary, canary: { ...canary.canary, status: 'passed' } };
  app.router.get(proposalPath, passed);
  await app.evaluate('evoLoadDetailProposal(evo.modal)'); await settle(4);
  assert.match($(app, '#evoReleaseAdvice').textContent, /建议发布/);
  assert.equal($(app, '#evoDetailActions [data-evo-approve]').disabled, false);
  assert.equal($(app, '#evoReleaseEvalDetails').open, true);

  app.router.get(proposalPath, { ...passed, releaseEvaluation: { ...releaseEvaluation, scores: undefined } });
  await app.evaluate('evoLoadDetailProposal(evo.modal)'); await settle(4);
  assert.match($(app, '#evoStoryRelease').textContent, /暂无可对比评分/);
  assert.equal($(app, '.evo-release-score'), null);
  app.router.get('/api/evolution/assets/agent/reviewer/versions', { currentVersion: 2, liveHash: 'changed', drifted: true });
  await app.evaluate('evoLoadDetailProposal(evo.modal)'); await settle(4);
  assert.match($(app, '#evoReleaseAdvice').textContent, /暂不发布.*线上正文已变化/s);
  assert.equal($(app, '#evoDetailActions [data-evo-approve]').disabled, true);
  $(app, '#evoStoryRelease [data-evo-run-eval]').click(); await settle(5);
  assert.equal(app.window.location.hash, '#/runs/source-1');
  assert.equal($(app, '[data-run-pane="eval"]').hidden, false);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('incomplete validation, trial and release are labeled with their own progress', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  const cases = [
    [{ status:'validating', summary:{total:4,finished:2} }, 'replay', /回放验证/],
    [{ status:'drafting' }, 'draft', /生成修改/],
    [{ status:'canary',canary:{status:'passed'} }, 'release', /待确认发布/],
    [{ status:'released',releasedVersion:3,watch:{status:'degraded'} }, 'watch', /观察.*变差/s],
    [{ status:'rolled_back' }, 'watch', /已回滚/],
  ];
  for (const [state,key,expected] of cases) {
    const html = await app.evaluate(`evoDetailTrackHTML(${JSON.stringify({...proposal,...state})})`);
    const el = app.document.createElement('div'); el.innerHTML=html;
    assert.match(el.querySelector('[aria-current="step"]').textContent,expected,key);
  }
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('a replay button opens its dialog and returns focus without starting a mutation', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  $(app, '[data-evo-story-replay="rp-fixed"]').focus();
  app.document.activeElement.click(); await settle(5);
  assert.equal(app.document.activeElement, $(app, '[data-evo-evidence-close]'));
  app.document.activeElement.click();
  assert.equal(app.document.activeElement.dataset.evoStoryReplay,'rp-fixed');
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
  assert.equal($(app, '.evo-home-problem b').textContent, '报告结构检查未通过');
  $(app, '[data-evo-asset-group]').click(); await settle(6);
  evidence(app,'run','source-1'); await settle(4);
  assert.equal($(app, '#evoFindingTitle').textContent, '报告结构检查未通过');
  assert.doesNotMatch($(app, '#evoRunPreview').textContent, /python3|import json/);
  assert.equal($(app, '#evoStoryBehavior p').textContent,'报告结构检查未通过');
  assert.equal($(app, '.evo-evidence-signal p').textContent,raw);
  assert.match($(app, '.evo-review-output').textContent,/已生成报告/);
  assert.equal($(app, '#evoEvidenceBody img'), null);
  assert.equal($(app, '#evoEvidenceBody script'), null);
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});

test('sample index read errors are visible in the evidence pane and retry restores samples', async t => {
  const app = await loadApp({ routes: [...routes, { method: 'GET', path: '/api/evolution/findings/f-evidence/runs?cluster=e2e', status: 503, body: { error: 'index unavailable' } }] }); t.after(app.close);
  await detail(app);
  issueRecords(app);
  assert.match($(app, '#evoFindingRecords').textContent, /读取问题样本失败.*index unavailable/);
  app.router.get('/api/evolution/findings/f-evidence/runs?cluster=e2e', { complete: true, total: 1, items: [{ runId: source.id, nodeId: 'check', detail: '验收缺失', status: 'failed' }] });
  $(app, '#evoFindingRecords [data-evo-record-retry]').click(); await settle(6);
  evidence(app,'run','source-1'); await settle(4);
  assert.match($(app, '#evoEvidenceBody').textContent, /旧输出：缺少 e2e/);
});

test('refresh keeps the selected replay and raw record disclosure open', async t => {
  const app = await loadApp({ routes }); t.after(app.close);
  await detail(app);
  evidence(app,'replay','rp-fixed'); await settle(5);
  await app.evaluate('evoLoadDetailProposal(evo.modal)'); await settle(5);
  assert.equal($(app, '#evoEvidenceDialog').open,true);
  assert.equal($(app, '#evoEvidenceDetails').open,true);
  assert.match($(app, '#evoEvidenceBody').textContent,/新输出/);
  $(app, '[data-evo-evidence-close]').click();
  assert.equal(app.document.activeElement.dataset.evoStoryReplay,'rp-fixed');
  assert.equal(app.router.calls.some(c => c.method !== 'GET'), false);
});
