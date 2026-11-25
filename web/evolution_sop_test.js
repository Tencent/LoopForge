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
  w.kbDrawerIsOpen = () => false;
  w.kbDrawerClose = () => {};
  w.afConfirm = async () => true;
  w.afPrompt = async () => 'ok';
  w.history = { state: null, pushState() {}, replaceState() {}, back() {} };
  w.location = { hash: '#/evolution' };

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

  const api = dom.run('({evo, evoSOPState, evoHomeHTML, evoSOPDetailHTML, evoOpenSOP, evoSOPSavePolicy, evoSOPStartGray, evoSOPPublishGray})');
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
        model: 'claude-sonnet-4',
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

  // 1. Home view renders SOP table with 3 clean stages and NO round-progress column.
  const homeHTML = api.evoHomeHTML();
  assert.match(homeHTML, /evo-sop-home/);
  assert.match(homeHTML, /SOP 自动检测已开启/);
  assert.match(homeHTML, /Bug 自动修复/);
  assert.match(homeHTML, /需求研发交付/);
  assert.match(homeHTML, /省 Token · 省工具/);
  assert.match(homeHTML, /每 15 次/);
  assert.match(homeHTML, /监测中/);
  assert.match(homeHTML, /优化中/);
  assert.match(homeHTML, /灰度中/);
  assert.doesNotMatch(homeHTML, /本轮进度/, 'home table must not show current round progress column');

  // 2. Detail view renders 5 core metrics with source notes, lifecycle callout, version & gray tabs, and diff tab.
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

  const trendHTML = api.evoSOPDetailHTML({ workflowId: 'wf-bug', tab: 'trend', scope: 'versions', metric: 'score', fileIndex: 0, detail: detailPayload });
  assert.match(trendHTML, /v3 生效中/);
  assert.match(trendHTML, /SOP 评分/);
  assert.match(trendHTML, /平均 Token/);
  assert.match(trendHTML, /平均工具调用/);
  assert.match(trendHTML, /平均执行耗时/);
  assert.match(trendHTML, /一次成功率/);
  assert.match(trendHTML, /近 10 次运行评测均分/);
  assert.match(trendHTML, /发布 v4/);
  assert.match(trendHTML, /历史正式版本/);
  assert.match(trendHTML, /近几次灰度数据/);

  const grayHTML = api.evoSOPDetailHTML({ workflowId: 'wf-bug', tab: 'trend', scope: 'gray', metric: 'score', fileIndex: 0, expandedGray: 'evp-gray', detail: detailPayload });
  assert.match(grayHTML, /v3 → v4/);
  assert.match(grayHTML, /run-gray-1/);
  assert.match(grayHTML, /候选版/);

  const changeHTML = api.evoSOPDetailHTML({ workflowId: 'wf-bug', tab: 'change', scope: 'versions', metric: 'score', fileIndex: 0, detail: detailPayload });
  assert.match(changeHTML, /工具反复调用 grep/);
  assert.match(changeHTML, /roles\/coder\.md/);
  assert.match(changeHTML, /优先查索引/);

  // 3. Optimize stage prompts whether to start grayscale.
  const optDetail = {
    ...evo.overview.sops[1],
    versions: [],
    grayRounds: [],
    activeProposal: { id: 'evp-opt', status: 'pending', title: '优化提示词', candidateContent: 'new', summary: { passed: true, fixed: 2, total: 3 } },
  };
  const optHTML = api.evoSOPDetailHTML({ workflowId: 'wf-feat', tab: 'trend', scope: 'versions', metric: 'score', fileIndex: 0, detail: optDetail });
  assert.match(optHTML, /优化完成，是否开始灰度/);
  assert.match(optHTML, /data-evo-sop-start-gray="evp-opt"/);

  // 4. Policy save hits PUT /api/evolution/sops/:workflowId/policy with preferences & batchSize.
  const calls = [];
  w.api = async (url, options = {}) => {
    calls.push({ url, method: options.method || 'GET', body: options.body ? JSON.parse(options.body) : null });
    if (url.includes('/policy')) return { workflowId: 'wf-bug', projectId: 'p', enabled: false, preferences: ['tokens', 'duration'], batchSize: 20, model: '' };
    return detailPayload;
  };
  dom.html('<section id="evolutionView" class="active"><div id="evoPageHead"></div><div id="evoMetrics"></div><div id="evolutionBody"></div></section>');
  const saved = await api.evoSOPSavePolicy('wf-bug', { enabled: false, preferences: ['tokens', 'duration'], batchSize: 20 });
  assert.equal(saved.batchSize, 20);
  assert.equal(calls[0].method, 'PUT');
  assert.match(calls[0].url, /\/api\/evolution\/sops\/wf-bug\/policy$/);
  assert.deepEqual(calls[0].body.preferences, ['tokens', 'duration']);
  assert.equal(calls[0].body.batchSize, 20);

  console.log('evolution sop: SOP-centric home, detail metrics, version/gray trends, diff tab, and policy save passed');
})().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
