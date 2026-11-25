const assert = require('node:assert/strict');
const path = require('node:path');
const { createDOM } = require('./hui_fake_dom');

(async () => {
  const dom = createDOM();
  const w = dom.window;
  w.$ = s => dom.document.querySelector(s);
  w.$$ = s => [...dom.document.querySelectorAll(s)];
  w.escapeHTML = v => String(v ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  w.state = { projectFilter: 'p' };
  w.relativeTime = () => '刚刚';
  w.toast = () => {};
  w.kbBadge = (text, tone) => `<span class="hui-badge" data-tone="${tone || ''}">${text}</span>`;
  w.kbCaption = text => `<p class="kb-caption">${text}</p>`;
  w.kbPanel = (head, body) => `<section class="kb-panel">${head}${body}</section>`;
  w.kbPanelTitle = (title, meta) => `<h3>${title}${meta ? ` <span>${meta}</span>` : ''}</h3>`;
  w.kbText = (t, d) => `<div><strong>${t}</strong><p>${d || ''}</p></div>`;
  w.kbValue = v => `<span>${v}</span>`;
  w.kbRow = (l, r) => `<div class="kb-row">${l}${r || ''}</div>`;
  w.kbDrawerIsOpen = () => false;
  w.kbDrawerClose = () => {};
  w.afConfirm = async () => true;
  w.afPrompt = async () => 'ok';
  w.history = { state: null, pushState() {}, replaceState() {}, back() {} };
  w.location = { hash: '#/evolution' };

  let boundModel = null;
  w.AgentFlowAssistantModels = {
    featurePickerHTML: name => `<select data-feature-executor></select><select data-feature-model name="${name || ''}"><option value="claude-code:sonnet-4" selected>Sonnet 4</option></select>`,
    bindFeaturePicker: (host, opts) => { boundModel = opts; return Promise.resolve(); },
  };

  for (const file of [
    'evolution.js',
    'app/evolution-simple.js',
    'app/evolution-review.js',
    'app/evolution-story.js',
    'app/evolution-home.js',
    'app/evolution-sop.js',
  ]) {
    dom.load(path.join(__dirname, file));
  }

  const api = dom.run('({evo, evoSOPState, evoHomeHTML, evoSOPDetailHTML, evoSOPOpenPrefDialog, evoSOPPrefMove, evoSOPPrefToggle, evoSOPPrefApplyPreset, evoSOPSyncPrefModelUI, evoSOPSavePolicy})');
  const { evo } = api;
  evo.projectId = 'p';
  evo.overview = {
    health: [],
    sopDetection: { enabled: true, model: 'claude-sonnet-4' },
    sops: [
      {
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
        latestMetrics: { runs: 10, totalTokens: 480000, avgTokens: 48000, totalTools: 110, avgTools: 11, avgDuration: 72, cleanCount: 9, cleanRate: 0.9, score: 88, scoredRuns: 10, scoreSource: 'eval' },
        activeProposalId: 'evp-gray',
      },
      {
        workflowId: 'wf-feat',
        workflowName: '需求研发交付',
        currentVersion: 'v2',
        enabled: true,
        preferences: ['tokens', 'clean'],
        batchSize: 15,
        stage: 'optimize',
        pendingRuns: 15,
        lastRunNote: '最近 1 次 · SOP 轨迹效率异常',
        latestMetrics: { runs: 15, totalTokens: 1200000, avgTokens: 80000, totalTools: 300, avgTools: 20, avgDuration: 140, cleanCount: 11, cleanRate: 0.7333, score: 74, scoredRuns: 15, scoreSource: 'heuristic' },
        activeProposalId: 'evp-opt',
      },
      {
        workflowId: 'wf-doc',
        workflowName: '发布说明整理',
        currentVersion: 'v1',
        enabled: false,
        preferences: ['duration'],
        batchSize: 5,
        stage: 'monitor',
        pendingRuns: 2,
        lastRunNote: '最近 1 次 · 一次成功，未发现异常',
        latestMetrics: { runs: 5, totalTokens: 150000, avgTokens: 30000, totalTools: 30, avgTools: 6, avgDuration: 35, cleanCount: 5, cleanRate: 1, score: 94, scoredRuns: 5, scoreSource: 'heuristic' },
      },
    ],
  };

  // 1. Home view has NO top auto-detection bar and NO round-progress column.
  const homeHTML = api.evoHomeHTML();
  assert.match(homeHTML, /evo-sop-home/);
  assert.doesNotMatch(homeHTML, /evo-sop-detect-bar/, 'home must not show redundant auto-detection banner');
  assert.match(homeHTML, /Bug 自动修复/);
  assert.match(homeHTML, /需求研发交付/);
  assert.match(homeHTML, /省 Token › 省工具/);
  assert.match(homeHTML, /每 15 次/);
  assert.match(homeHTML, /监测中/);
  assert.match(homeHTML, /优化中/);
  assert.match(homeHTML, /灰度中/);
  assert.doesNotMatch(homeHTML, /本轮进度/, 'home table must not show current round progress column');
  assert.match(homeHTML, /evo-sop-dialog-close/, 'preference dialog uses dedicated neutral close icon button');

  // 2. Detail view has NO config bar, renders 5 core metrics, lifecycle callout, version & gray tabs, and side-by-side split diff.
  const detailPayload = {
    ...evo.overview.sops[0],
    versions: [
      { version: 'v2', current: false, metrics: { runs: 10, totalTokens: 760000, avgTokens: 76000, totalTools: 210, avgTools: 21, avgDuration: 110, cleanCount: 7, cleanRate: 0.7, score: 72, scoredRuns: 10, scoreSource: 'eval' } },
      { version: 'v3', current: true, metrics: { runs: 10, totalTokens: 480000, avgTokens: 48000, totalTools: 110, avgTools: 11, avgDuration: 72, cleanCount: 9, cleanRate: 0.9, score: 88, scoredRuns: 10, scoreSource: 'eval' } },
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
        recentRuns: [{ runId: 'run-gray-1', group: 'candidate', tokens: 34000, tools: 7, duration: 52, clean: true, score: 94 }],
      },
    ],
    activeProposal: {
      id: 'evp-gray',
      status: 'canary',
      title: '压缩上下文减少重复检索',
      canary: { percent: 50, minRuns: 5, status: 'passed', control: { runs: 5 }, canary: { runs: 5 } },
    },
    batchMetrics: { runs: 10, totalTokens: 480000, avgTokens: 48000, totalTools: 110, avgTools: 11, avgDuration: 72, cleanCount: 9, cleanRate: 0.9, score: 88 },
    problems: [{ key: 'tools', title: '工具反复调用 grep', detail: '单节点连续调用 7 次', fix: '先读索引再精准检索', hits: 7, runs: 4 }],
    changeFiles: [{ path: 'roles/coder.md', assetName: '开发工程师', lines: [['ctx', '步骤 1'], ['del', '全局 grep'], ['add', '优先查索引']] }],
  };

  const trendHTML = api.evoSOPDetailHTML({ workflowId: 'wf-bug', tab: 'trend', scope: 'versions', metric: 'score', fileIndex: 0, diffMode: 'split', detail: detailPayload });
  assert.doesNotMatch(trendHTML, /evo-sop-config-bar/, 'detail view must not show redundant config bar');
  assert.match(trendHTML, /v3 生效中/);
  assert.match(trendHTML, /SOP 评分/);
  assert.match(trendHTML, /平均 Token/);
  assert.match(trendHTML, /平均工具调用/);
  assert.match(trendHTML, /平均执行耗时/);
  assert.match(trendHTML, /一次成功率/);
  assert.match(trendHTML, /近 10 次运行评测均分/);
  assert.match(trendHTML, /P1 主目标/, 'detail view must badge the P1 preference KPI card');
  assert.match(trendHTML, /发布 v4/);

  const grayHTML = api.evoSOPDetailHTML({ workflowId: 'wf-bug', tab: 'trend', scope: 'gray', metric: 'score', fileIndex: 0, expandedGray: 'evp-gray', detail: detailPayload });
  assert.match(grayHTML, /v3 → v4/);
  assert.match(grayHTML, /run-gray-1/);
  assert.match(grayHTML, /候选版/);

  const changeHTML = api.evoSOPDetailHTML({ workflowId: 'wf-bug', tab: 'change', scope: 'versions', metric: 'score', fileIndex: 0, diffMode: 'split', detail: detailPayload });
  assert.match(changeHTML, /工具反复调用 grep/);
  assert.match(changeHTML, /roles\/coder\.md/);
  assert.match(changeHTML, /evo-sop-split-diff/, 'change tab defaults to side-by-side split diff');
  assert.match(changeHTML, /修改前（原版）/);
  assert.match(changeHTML, /修改后（候选版）/);
  assert.match(changeHTML, /全局 grep/);
  assert.match(changeHTML, /优先查索引/);

  // 2b. External Chatbot edit renders badge on version table and split diff when no RSI proposal is active.
  const chatbotDetail = {
    ...detailPayload,
    stage: 'monitor',
    activeProposal: null,
    lastProposal: null,
    changeFiles: [],
    versions: [
      { version: 'v3', current: false, assetKind: 'agent', assetId: 'coder', assetName: '开发工程师', rollbackVersion: 1, metrics: { runs: 5, avgTokens: 70000, avgTools: 18, avgDuration: 95, cleanRate: 0.6, score: 68 } },
      { version: 'v3 · Chatbot', current: true, source: 'chatbot', changeNote: 'Chatbot 修改「开发工程师」', metrics: { runs: 3, avgTokens: 42000, avgTools: 9, avgDuration: 55, cleanRate: 1, score: 92 } },
    ],
    assets: [{ kind: 'agent', id: 'coder', name: '开发工程师', currentVersion: 2, versionCount: 2 }],
    recentEdits: [{
      id: 'agent:coder:v2',
      assetKind: 'agent',
      assetId: 'coder',
      assetName: '开发工程师',
      version: 2,
      source: 'chatbot',
      createdAt: new Date().toISOString(),
      diff: { path: 'roles/coder.md', lines: [['del', '旧指令'], ['add', 'Chatbot 修复后的新指令']] },
    }],
  };
  const chatTrendHTML = api.evoSOPDetailHTML({ workflowId: 'wf-bug', tab: 'trend', scope: 'versions', metric: 'score', detail: chatbotDetail });
  assert.match(chatTrendHTML, /v3 · Chatbot/);
  assert.match(chatTrendHTML, /Chatbot 修改/);
  assert.match(chatTrendHTML, /版本与回滚 · v2/, 'detail header must expose version & rollback entry');
  assert.match(chatTrendHTML, /回滚「开发工程师」到 v1/, 'historical version table must label rollback with asset name');

  // Read-only asset must NOT expose direct rollback button.
  const readOnlyDetail = { ...chatbotDetail, assets: [{ kind: 'agent', id: 'coder', name: '开发工程师', currentVersion: 2, versionCount: 2, readOnly: true }] };
  const roTrendHTML = api.evoSOPDetailHTML({ workflowId: 'wf-bug', tab: 'trend', scope: 'versions', metric: 'score', detail: readOnlyDetail });
  assert.doesNotMatch(roTrendHTML, /data-evo-rollback=/, 'read-only asset must not render direct rollback button');

  // Unpassed gray round must disable publish button (P2-5).
  const runningGrayDetail = {
    ...detailPayload,
    activeProposal: { ...detailPayload.activeProposal, canary: { percent: 50, minRuns: 5, status: 'running', control: { runs: 2 }, canary: { runs: 2 } } },
  };
  const runningGrayHTML = api.evoSOPDetailHTML({ workflowId: 'wf-bug', tab: 'trend', scope: 'versions', metric: 'score', detail: runningGrayDetail });
  assert.match(runningGrayHTML, /data-evo-sop-publish="evp-gray"[^>]*disabled/, 'unpassed gray round must disable publish button');

  const chatChangeHTML = api.evoSOPDetailHTML({ workflowId: 'wf-bug', tab: 'change', scope: 'versions', metric: 'score', diffMode: 'split', detail: chatbotDetail });
  assert.match(chatChangeHTML, /Chatbot 修复后的新指令/);
  assert.match(chatChangeHTML, /evo-sop-split-diff/);

  // 2c. Failed last proposal surfaces error reason & retry action on detail callout instead of silent monitor state.
  const failedDetail = {
    ...detailPayload,
    stage: 'monitor',
    activeProposal: null,
    lastProposal: { id: 'evp-fail-1', status: 'failed', error: '改进器调用失败：模型未配置' },
  };
  const failHTML = api.evoSOPDetailHTML({ workflowId: 'wf-bug', tab: 'trend', scope: 'versions', metric: 'score', detail: failedDetail });
  assert.match(failHTML, /上次优化未通过/);
  assert.match(failHTML, /改进器调用失败：模型未配置/);
  assert.match(failHTML, /重试优化/);

  // 3. Preference dialog renders P1/P2 priority badges, supports up/down reorder & presets, and binds real model picker.
  dom.html(`<section id="evolutionView" class="active"><header id="evoPageHead"><button data-evo-tab="assets">进化设置</button><button id="evoRefresh">刷新</button><button id="evoDiagnose">发起诊断</button></header><div id="evoMetrics"></div><div id="evolutionBody">${homeHTML}</div></section>`);
  api.evoSOPOpenPrefDialog('wf-bug');
  assert.equal(boundModel?.model, 'codex:gpt-5', 'custom model must initialize feature picker');
  assert.equal(w.$('#evoSopModelPickerHost').hidden, false);
  assert.deepEqual([...api.evoSOPState.prefTarget.preferences], ['tokens', 'tools']);
  assert.match(w.$('#evoSopPrefOptions').textContent, /P1 主目标/);
  assert.equal(w.$('#evoSopPrefOptions [data-pref-key]').dataset.prefKey, 'tokens');
  api.evoSOPPrefMove('tools', -1);
  assert.deepEqual([...api.evoSOPState.prefTarget.preferences], ['tools', 'tokens'], 'moving tools up must promote it to P1');
  assert.equal(w.$('#evoSopPrefOptions [data-pref-key]').dataset.prefKey, 'tools', 'reordered P1 option must render first');
  api.evoSOPPrefToggle('duration', true);
  assert.deepEqual([...api.evoSOPState.prefTarget.preferences], ['tools', 'tokens', 'duration'], 'checking new preference appends as P3');
  api.evoSOPPrefApplyPreset('quality');
  assert.deepEqual([...api.evoSOPState.prefTarget.preferences], ['clean', 'tokens'], 'preset must set ordered priority chain');

  // 4. Policy save hits PUT /api/evolution/sops/:workflowId/policy with preferences, batchSize, and standalone model.
  const calls = [];
  w.api = async (url, options = {}) => {
    calls.push({ url, method: options.method || 'GET', body: options.body ? JSON.parse(options.body) : null });
    if (url.includes('/policy')) return { workflowId: 'wf-bug', projectId: 'p', enabled: false, preferences: ['tokens', 'duration'], batchSize: 20, model: 'claude-code:sonnet-4' };
    return detailPayload;
  };
  const saved = await api.evoSOPSavePolicy('wf-bug', { enabled: false, preferences: ['tokens', 'duration'], batchSize: 20, model: 'claude-code:sonnet-4' });
  assert.equal(saved.batchSize, 20);
  assert.equal(saved.model, 'claude-code:sonnet-4');
  assert.equal(calls[0].method, 'PUT');
  assert.match(calls[0].url, /\/api\/evolution\/sops\/wf-bug\/policy$/);
  assert.deepEqual(calls[0].body.preferences, ['tokens', 'duration']);
  assert.equal(calls[0].body.model, 'claude-code:sonnet-4');
  assert.equal(w.$('[data-evo-sop-pref-open="wf-bug"]').disabled, false, 'preference button must re-enable after policy save');
  assert.equal(w.$('[data-evo-sop-toggle="wf-bug"]').disabled, false, 'switch must re-enable after policy save');

  // 5. SOP workbench hides diagnose button, keeps settings accessible for L0-L3/samples, and shows legacy detection only if already enabled (P2-8).
  evo.tab = 'assets';
  evo.overview.sopDetection = { enabled: false, model: '' };
  dom.run('evoRender()');
  assert.equal(w.$('#evoPageHead [data-evo-tab="assets"]').hidden, false, 'settings button must stay accessible for L0-L3 and samples');
  assert.equal(w.$('#evoDiagnose').hidden, true, 'diagnose button must be hidden in SOP workbench');
  assert.equal(w.$('#evoSOPDetection'), null, 'must hide legacy global SOP detection card when disabled');
  assert.ok(w.$('.evo-settings-title'), 'settings page must remain accessible');

  evo.overview.sopDetection = { enabled: true, model: 'claude-sonnet-4' };
  dom.run('evoRender()');
  assert.ok(w.$('#evoSOPDetection'), 'must allow turning off already-enabled legacy project SOP detection');

  console.log('evolution sop: clean home/detail, fixed switch scope, side-by-side diff, close icon, and standalone model picker passed');
})().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
