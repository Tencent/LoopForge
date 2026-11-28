import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const now = '2026-11-26T00:30:00Z';

function makeSopSummary(overrides = {}) {
  return {
    workflowId: 'wf-bug',
    workflowName: 'Bug 自动修复',
    description: '定位并修复线上缺陷',
    currentVersion: 'v3',
    enabled: true,
    preferences: ['tokens', 'tools'],
    batchSize: 10,
    model: 'codex:gpt-5',
    stage: 'gray',
    pendingRuns: 4,
    lastRunNote: '最近 1 次 · 一次成功，未发现异常',
    latestMetrics: {
      runs: 10, totalTokens: 480000, avgTokens: 48000, totalTools: 110, avgTools: 11,
      avgDuration: 72, cleanCount: 9, cleanRate: 0.9, score: 88, scoredRuns: 10, scoreSource: 'eval',
    },
    activeProposalId: 'evp-gray',
    ...overrides,
  };
}

function makeDetail(overrides = {}) {
  return {
    ...makeSopSummary(),
    versions: [
      {
        version: 'v2', current: false, source: 'manual', changeNote: '手动修改「开发工程师」',
        assetKind: 'agent', assetId: 'coder', assetName: '开发工程师', rollbackVersion: 1,
        metrics: { runs: 10, totalTokens: 1200000, avgTokens: 1200000, totalTools: 210, avgTools: 21, avgDuration: 120, cleanCount: 7, cleanRate: 0.7, score: 72, scoredRuns: 10, scoreSource: 'eval' },
      },
      {
        version: 'v3 · Chatbot', current: false, source: 'chatbot', changeNote: 'Chatbot 修改「开发工程师」',
        assetKind: 'agent', assetId: 'coder', assetName: '开发工程师', rollbackVersion: 2,
        metrics: { runs: 8, totalTokens: 480000, avgTokens: 60000, totalTools: 120, avgTools: 15, avgDuration: 50, cleanCount: 6, cleanRate: 0.75, score: 80, scoredRuns: 8, scoreSource: 'heuristic' },
      },
      {
        version: 'v3 · 进化', current: true, source: 'evolution', changeNote: '自动进化发布「开发工程师」',
        assetKind: 'agent', assetId: 'coder', assetName: '开发工程师',
        metrics: { runs: 10, totalTokens: 480000, avgTokens: 48000, totalTools: 110, avgTools: 11, avgDuration: 72, cleanCount: 9, cleanRate: 0.9, score: 88, scoredRuns: 10, scoreSource: 'eval' },
      },
    ],
    assets: [
      { kind: 'agent', id: 'coder', name: '开发工程师', currentVersion: 3, versionCount: 3 },
      { kind: 'rule', id: 'rule-ro', name: '共享规范', currentVersion: 1, versionCount: 1, readOnly: true },
    ],
    grayRounds: [
      {
        proposalId: 'evp-gray',
        baseVersion: 'v3',
        target: 'v4',
        status: 'passed',
        percent: 50,
        minRuns: 5,
        control: { runs: 5, avgTokens: 48000, avgTools: 11, avgDuration: 72, cleanRate: 0.8, score: 86 },
        candidate: { runs: 5, avgTokens: 35000, avgTools: 8, avgDuration: 55, cleanRate: 1, score: 93 },
        recentRuns: [
          { runId: 'run-gray-ctrl-0001', group: 'control', tokens: 49000, tools: 12, duration: 75, clean: false, score: 80 },
          { runId: 'run-gray-cand-0002', group: 'candidate', tokens: 34000, tools: 7, duration: 52, clean: true, score: 94 },
        ],
      },
    ],
    activeProposal: {
      id: 'evp-gray',
      status: 'canary',
      title: '压缩上下文减少重复检索',
      canary: { percent: 50, minRuns: 5, status: 'passed', verdict: '候选版显著优于对照', control: { runs: 5 }, canary: { runs: 5 } },
    },
    batchMetrics: { runs: 10, totalTokens: 480000, avgTokens: 48000, totalTools: 110, avgTools: 11, avgDuration: 72, cleanCount: 9, cleanRate: 0.9, score: 88 },
    problems: [
      { key: 'tokens', title: '重复读取完整仓库', detail: '单节点重复读取 5 次', fix: '先检索目录摘要', hits: 5, runs: 4 },
      { key: 'tools', title: '工具反复调用 grep', detail: '单节点连续调用 7 次', fix: '先读索引再精准检索', hits: 7, runs: 4 },
    ],
    changeFiles: [
      { path: 'roles/coder.md', assetName: '开发工程师', lines: [['ctx', '步骤 1'], ['del', '全局 grep'], ['add', '优先查索引'], ['add', '仅读取命中片段']] },
      { path: 'rules/style.md', assetName: '编码规范', lines: [['ctx', '规则 A'], ['del', '输出全部日志'], ['add', '仅输出失败摘要']] },
    ],
    recentEdits: [
      {
        id: 'agent:coder:v2', assetKind: 'agent', assetId: 'coder', assetName: '开发工程师', version: 2,
        source: 'chatbot', createdAt: now,
        diff: { path: 'roles/coder.md', lines: [['del', '旧规则'], ['add', 'Chatbot 新规则']] },
      },
      {
        id: 'agent:coder:v3', assetKind: 'agent', assetId: 'coder', assetName: '开发工程师', version: 3,
        source: 'rollback', createdAt: now,
        diff: { path: 'roles/coder.md', lines: [['del', '临时规则'], ['add', '回滚后规则']] },
      },
    ],
    ...overrides,
  };
}

function mockCanvas2D(app) {
  const ops = [];
  const ctx = {
    setTransform() {},
    clearRect() { ops.push('clear'); },
    beginPath() {},
    closePath() {},
    moveTo() {},
    lineTo() {},
    arc() {},
    stroke() { ops.push('stroke'); },
    fill() { ops.push('fill'); },
    fillRect() {},
    fillText(t) { ops.push(`text:${t}`); },
    measureText: (t) => ({ width: String(t || '').length * 7 }),
    save() {},
    restore() {},
    setLineDash() {},
  };
  app.window.HTMLCanvasElement.prototype.getContext = () => ctx;
  return ops;
}

function confirmModal(app, promptValue = '') {
  const input = app.document.querySelector('#confirmPromptInput');
  if (input) input.value = promptValue;
  app.document.querySelector('#confirmOkBtn')?.click();
}

test('SOP workbench home: filters, search, preferences priority dialog, polling guard, and settings access', async (t) => {
  const sops = [
    makeSopSummary(),
    makeSopSummary({
      workflowId: 'wf-feat', workflowName: '需求研发交付', currentVersion: 'v2',
      enabled: true, preferences: ['tokens', 'clean', 'duration'], batchSize: 15, model: '',
      stage: 'optimize', pendingRuns: 15, lastRunNote: '最近 1 次 · SOP 轨迹效率异常',
      latestMetrics: { runs: 0 },
    }),
    makeSopSummary({
      workflowId: 'wf-doc', workflowName: '发布说明整理', currentVersion: 'v1',
      enabled: false, preferences: ['duration'], batchSize: 5, model: '',
      stage: 'monitor', pendingRuns: 2, lastRunNote: '最近 1 次 · 一次成功',
      latestMetrics: { runs: 5, totalTokens: 800, avgTokens: 800, totalTools: 10, avgTools: 2, avgDuration: 60, cleanCount: 5, cleanRate: 1, score: 94 },
    }),
  ];
  const overview = {
    projectId: 'project-1', findings: [], health: [], policies: [],
    sopDetection: { enabled: false, model: 'codebuddy:claude-sonnet-5' },
    sops,
  };

  const app = await loadApp({
    routes: [
      { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: overview },
      { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [] },
      { method: 'GET', path: '/api/evolution/samples?projectId=project-1', body: [] },
      { method: 'GET', path: '/api/assistant/models', body: { engines: [{ id: 'codex', name: 'Codex', personal: true }], models: [{ id: 'codex:gpt-5', name: 'GPT-5' }] } },
      { method: 'PUT', path: '/api/evolution/sops/wf-bug/policy', body: { workflowId: 'wf-bug', projectId: 'project-1', enabled: true, preferences: ['clean', 'tokens'], batchSize: 20, model: '' } },
      { method: 'PUT', path: '/api/evolution/sops/wf-doc/policy', body: { workflowId: 'wf-doc', projectId: 'project-1', enabled: true, preferences: ['duration'], batchSize: 5, model: '' } },
    ],
  });
  t.after(app.close);
  const $ = s => app.document.querySelector(s);

  await app.evaluate(`state.projectFilter='project-1';switchView('evolution');renderEvolutionView()`);
  await settle(5);

  // Formatters edge coverage
  assert.equal(app.evaluate('evoSOPNum("bad")'), '—');
  assert.equal(app.evaluate('evoSOPFormatTokens(0)'), '—');
  assert.equal(app.evaluate('evoSOPFormatTokens(500)'), '500');
  assert.equal(app.evaluate('evoSOPFormatTokens(1500)'), '1.5k');
  assert.equal(app.evaluate('evoSOPFormatTokens(2500000)'), '2.50M');
  assert.equal(app.evaluate('evoSOPFormatSec(0)'), '—');
  assert.equal(app.evaluate('evoSOPFormatSec(45)'), '45s');
  assert.equal(app.evaluate('evoSOPFormatSec(120)'), '2m');
  assert.equal(app.evaluate('evoSOPFormatSec(135)'), '2m 15s');
  assert.equal(app.evaluate('evoSOPMetricValue(null, "score")'), null);
  assert.equal(app.evaluate('evoSOPMetricValue({runs:1}, "unknown")'), null);
  assert.match(app.evaluate('evoSOPDeltaHTML(10, 10, true)'), /持平/);
  assert.match(app.evaluate('evoSOPPrefsText(["tokens","tools","duration"])'), /\+1/);

  // Home rows, filters & search
  assert.equal(app.document.querySelectorAll('.evo-sop-row').length, 3);
  $('[data-evo-sop-filter="gray"]').click();
  assert.equal(app.document.querySelectorAll('.evo-sop-row').length, 1);
  $('[data-evo-sop-filter="all"]').click();
  assert.equal(app.document.querySelectorAll('.evo-sop-row').length, 3);

  const search = $('[data-evo-sop-search]');
  search.focus();
  assert.equal(app.evaluate('evoPollInteractionPaused()'), true, 'focused search must pause polling redraws');
  search.value = '不存在的关键词';
  search.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.match($('#evoSopListHost').textContent, /暂无匹配的 SOP/);
  $('[data-evo-sop-reset]').click();
  assert.equal(app.document.querySelectorAll('.evo-sop-row').length, 3);

  // Composition pauses polling too (P2-1)
  search.dispatchEvent(new app.window.Event('compositionstart', { bubbles: true }));
  assert.equal(app.evaluate('evo.composing'), true);
  search.dispatchEvent(new app.window.Event('compositionend', { bubbles: true }));
  assert.equal(app.evaluate('evo.composing'), false);

  // Toggle switch saves policy
  const sw = $('[data-evo-sop-toggle="wf-doc"]');
  sw.checked = true;
  sw.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  await settle(5);
  assert.ok(app.router.calls.some(c => c.method === 'PUT' && c.path === '/api/evolution/sops/wf-doc/policy' && c.body.enabled === true));

  // Toggle switch error path restores UI cleanly
  app.router.put('/api/evolution/sops/wf-doc/policy', { status: 400, body: { error: '模型无效' } });
  sw.checked = false;
  sw.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  await settle(5);

  // Preference priority dialog: open, presets, move, toggle, drag reorder, batch, model, submit
  $('[data-evo-sop-pref-open="wf-bug"]').click();
  await settle(4);
  const dlg = $('#evoSopPrefDialog');
  assert.equal(dlg.open, true);
  assert.equal(app.evaluate('evoPollInteractionPaused()'), true, 'open preference dialog must pause polling');

  // Move tools up to P1
  $('[data-evo-sop-pref-move="up"][data-pref-key="tools"]').click();
  assert.deepEqual(Array.from(app.evaluate('evoSOPState.prefTarget.preferences')), ['tools', 'tokens']);

  // Drag reorder back (tokens before tools)
  const fromRow = $('#evoSopPrefOptions [data-pref-key="tokens"]');
  const toRow = $('#evoSopPrefOptions [data-pref-key="tools"]');
  fromRow.dispatchEvent(new app.window.Event('dragstart', { bubbles: true }));
  toRow.dispatchEvent(new app.window.Event('dragover', { bubbles: true, cancelable: true }));
  toRow.dispatchEvent(new app.window.Event('dragleave', { bubbles: true }));
  toRow.dispatchEvent(new app.window.Event('drop', { bubbles: true, cancelable: true }));
  fromRow.dispatchEvent(new app.window.Event('dragend', { bubbles: true }));
  assert.deepEqual(Array.from(app.evaluate('evoSOPState.prefTarget.preferences')), ['tokens', 'tools']);

  // Uncheck all -> submit blocked with warn toast
  for (let cb = $('#evoSopPrefOptions input[type="checkbox"]:checked'); cb; cb = $('#evoSopPrefOptions input[type="checkbox"]:checked')) {
    cb.checked = false;
    cb.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  }
  assert.match($('#evoSopPrefOptions').textContent, /请至少勾选一项/);
  $('#evoSopPrefForm').dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(2);

  // Apply preset, pick batch preset & custom input, toggle model mode to default, and save
  $('[data-evo-sop-pref-preset="quality"]').click();
  $('[data-evo-sop-preset="20"]').click();
  $('#evoSopBatchCustom').value = '20';
  $('#evoSopBatchCustom').dispatchEvent(new app.window.Event('input', { bubbles: true }));
  $('[data-evo-sop-model-mode="default"]').click();
  $('#evoSopPrefForm').dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(5);
  assert.equal(dlg.open, false);
  const savedPut = app.router.calls.find(c => c.method === 'PUT' && c.path === '/api/evolution/sops/wf-bug/policy');
  assert.deepEqual(savedPut?.body?.preferences, ['clean', 'tokens']);
  assert.equal(savedPut?.body?.batchSize, 20);
  assert.equal(savedPut?.body?.model, '');

  // Open & close via close button
  $('[data-evo-sop-pref-open="wf-bug"]').click();
  $('[data-evo-sop-pref-close]').click();
  assert.equal(dlg.open, false);

  // Settings access (P2-8): settings button stays visible, diagnose stays hidden;
  // legacy SOP detection card hidden when disabled, shown when already enabled.
  assert.equal($('#evoPageHead [data-evo-tab="assets"]').hidden, false);
  assert.equal($('#evoDiagnose').hidden, true);
  $('#evoPageHead [data-evo-tab="assets"]').click();
  await settle(4);
  assert.ok($('.evo-settings-title'));
  assert.equal($('#evoSOPDetection'), null);
  await app.evaluate(`evo.overview.sopDetection={enabled:true,model:'codex:gpt-5'};evoRender()`);
  assert.ok($('#evoSOPDetection'));
  $('[data-evo-list-back]').click();
  await settle(4);

  // Empty SOPs state
  app.router.get('/api/evolution/overview?projectId=project-1', { ...overview, sops: [] });
  await app.evaluate('evoRefresh().then(evoRender)');
  await settle(4);
  assert.match($('#evoSopListHost').textContent, /当前项目暂无 SOP/);
});

test('SOP workbench detail: canvas trend, versions rollback labels, gray publish guard, change split/inline diff, and actions', async (t) => {
  const detail = makeDetail();
  const overview = {
    projectId: 'project-1', findings: [], health: [], policies: [],
    sopDetection: { enabled: false, model: '' },
    sops: [makeSopSummary()],
  };

  const app = await loadApp({
    routes: [
      { method: 'GET', path: '/api/evolution/overview?projectId=project-1', body: overview },
      { method: 'GET', path: '/api/evolution/findings?projectId=project-1', body: [] },
      { method: 'GET', path: '/api/evolution/samples?projectId=project-1', body: [] },
      { method: 'GET', path: '/api/evolution/sops/wf-bug?projectId=project-1', body: detail },
      { method: 'POST', path: '/api/evolution/proposals/evp-gray/approve', body: { id: 'evp-gray', status: 'released', releasedVersion: 4 } },
      { method: 'POST', path: '/api/evolution/proposals/evp-gray/reject', body: { id: 'evp-gray', status: 'rejected' } },
      { method: 'POST', path: '/api/evolution/proposals/evp-opt/approve', body: { id: 'evp-opt', status: 'canary' } },
      { method: 'POST', path: '/api/evolution/sops/wf-bug/optimize', status: 202, body: { id: 'evp-new', status: 'drafting' } },
    ],
  });
  t.after(app.close);
  const ops = mockCanvas2D(app);
  const $ = s => app.document.querySelector(s);

  await app.evaluate(`state.projectFilter='project-1';switchView('evolution');renderEvolutionView()`);
  await settle(5);

  // Open detail view
  $('[data-evo-sop-open="wf-bug"]').click();
  await settle(6);
  assert.ok($('#evoSopDetailPage'));
  assert.ok(ops.includes('stroke'), 'versions trend canvas must draw');
  assert.match($('#evoSopDetailPage').textContent, /回滚「开发工程师」到 v1/);
  assert.match($('#evoSopDetailPage').textContent, /回滚「开发工程师」到 v2/);

  // Switch metrics across all 5 dimensions (score/tokens/tools/duration/clean)
  for (const m of ['tokens', 'tools', 'duration', 'clean', 'score']) {
    $(`[data-evo-sop-metric="${m}"]`).click();
  }

  // Switch to gray rounds tab, expand detail runs, then collapse
  $('[data-evo-sop-scope="gray"]').click();
  assert.match($('#evoSopDetailPage').textContent, /v3 → v4/);
  $('[data-evo-sop-gray-expand="evp-gray"]').click();
  assert.ok($('[data-kb-run="run-gray-cand-0002"]'));
  $('[data-evo-sop-gray-expand="evp-gray"]').click();

  // Switch to change tab: multi-file tabs, split vs inline diff, external edits tabs
  $('[data-evo-sop-tab="change"]').click();
  assert.ok($('.evo-sop-split-diff'));
  $('[data-evo-sop-file="1"]').click();
  assert.match($('#evoSopDetailPage').textContent, /仅输出失败摘要/);
  $('[data-evo-sop-edit="1"]').click();
  assert.match($('#evoSopDetailPage').textContent, /回滚后规则/);
  $('[data-evo-sop-diff-mode="inline"]').click();
  assert.ok($('.evo-sop-compact-diff'));

  // Passed gray round: publish & stop gray actions
  $('[data-evo-sop-tab="trend"]').click();
  $('[data-evo-sop-publish="evp-gray"]').click();
  await settle(2);
  confirmModal(app);
  await settle(5);
  assert.ok(app.router.calls.some(c => c.method === 'POST' && c.path === '/api/evolution/proposals/evp-gray/approve'));

  $('[data-evo-sop-stop-gray="evp-gray"]').click();
  await settle(2);
  confirmModal(app, '暂停验证');
  await settle(5);
  assert.ok(app.router.calls.some(c => c.method === 'POST' && c.path === '/api/evolution/proposals/evp-gray/reject'));

  // Verify unpassed gray round disables publish button (P2-5)
  app.router.get('/api/evolution/sops/wf-bug?projectId=project-1', makeDetail({
    activeProposal: { id: 'evp-gray', status: 'canary', canary: { percent: 50, minRuns: 5, status: 'running', control: { runs: 2 }, canary: { runs: 2 } } },
  }));
  await app.evaluate('evoSOPLoadDetail(evoSOPState.view)');
  await settle(4);
  assert.equal($('[data-evo-sop-publish="evp-gray"]').disabled, true);

  // Verify optimize pending (passed) -> start gray action
  app.router.get('/api/evolution/sops/wf-bug?projectId=project-1', makeDetail({
    stage: 'optimize',
    activeProposal: { id: 'evp-opt', status: 'pending', title: '精简上下文', candidateContent: 'new prompt', summary: { passed: true, fixed: 2, verdict: '修复 2 条' } },
  }));
  await app.evaluate('evoSOPLoadDetail(evoSOPState.view)');
  await settle(4);
  $('[data-evo-sop-start-gray="evp-opt"]').click();
  await settle(2);
  confirmModal(app);
  await settle(5);
  assert.ok(app.router.calls.some(c => c.method === 'POST' && c.path === '/api/evolution/proposals/evp-opt/approve' && c.body?.mode === 'canary'));

  // Verify drafting, validating, pending-unpassed, monitor-failed, monitor-ready, monitor-waiting, paused, and empty data callouts
  const variants = [
    { enabled: false, stage: 'monitor', activeProposal: null },
    { stage: 'optimize', activeProposal: { id: 'p-d', status: 'drafting', title: '生成中' } },
    { stage: 'optimize', activeProposal: { id: 'p-v', status: 'validating', title: '回放中', summary: { finished: 1, total: 3 } } },
    { stage: 'optimize', activeProposal: { id: 'p-u', status: 'pending', summary: { passed: false } } },
    { stage: 'monitor', activeProposal: null, lastProposal: { id: 'p-f', status: 'failed', error: '模型不可用' } },
    { stage: 'monitor', activeProposal: null, lastProposal: null, pendingRuns: 10, batchSize: 10 },
    { stage: 'monitor', activeProposal: null, lastProposal: null, pendingRuns: 2, batchSize: 10, latestMetrics: { runs: 12 }, versions: [], grayRounds: [], changeFiles: [], recentEdits: [], problems: [] },
  ];
  for (const v of variants) {
    app.router.get('/api/evolution/sops/wf-bug?projectId=project-1', makeDetail(v));
    await app.evaluate('evoSOPLoadDetail(evoSOPState.view)');
    await settle(3);
  }
  // Empty chart message & empty tables
  assert.equal($('#evoSopTrendCanvas').hidden, true);
  assert.equal($('#evoSopChartEmpty').hidden, false);
  $('[data-evo-sop-scope="gray"]').click();
  assert.match($('#evoSopDetailPage').textContent, /暂无灰度轮次数据/);
  $('[data-evo-sop-tab="change"]').click();
  assert.match($('#evoSopDetailPage').textContent, /本轮尚未生成修改文件/);

  // Manual trigger optimize button from monitor stage
  app.router.get('/api/evolution/sops/wf-bug?projectId=project-1', makeDetail({
    stage: 'monitor', activeProposal: null, lastProposal: null, pendingRuns: 10, batchSize: 10,
  }));
  await app.evaluate('evoSOPLoadDetail(evoSOPState.view)');
  await settle(3);
  $('[data-evo-sop-optimize="wf-bug"]').click();
  await settle(5);
  assert.ok(app.router.calls.some(c => c.method === 'POST' && c.path === '/api/evolution/sops/wf-bug/optimize'));

  // Error load + retry button
  app.router.get('/api/evolution/sops/wf-bug?projectId=project-1', { status: 500, body: { error: 'boom' } });
  await app.evaluate('evoSOPLoadDetail(evoSOPState.view)');
  await settle(3);
  assert.match($('#evoSopDetailPage').textContent, /boom/);
  app.router.get('/api/evolution/sops/wf-bug?projectId=project-1', detail);
  $('[data-evo-sop-reload]').click();
  await settle(4);
  assert.ok($('#evoSopDetailTitle'));

  // Back to list
  $('[data-evo-sop-back]').click();
  await settle(4);
  assert.equal($('#evoSopDetailPage'), null);
});
