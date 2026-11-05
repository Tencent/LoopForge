import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const analytics = {
  summary: {
    totalRuns: 4, succeededRuns: 2, failedRuns: 1, cancelledRuns: 1, activeRuns: 1,
    reworkedRuns: 1, uniqueIssues: 3, totalTokens: 6400, inputTokens: 3200,
    outputTokens: 1800, cacheReadTokens: 1400, cacheWriteTokens: 300, cacheHitRate: 30,
    totalCostCny: 1.25, costUnknownRuns: 0, executionP95DurationMs: 95000,
    executionAverageDurationMs: 45000, waitAverageDurationMs: 3000, runsPerIssue: 1.33,
    firstPassRate: 66, successRate: 67, qualityRuns: 3, weeklyActiveContributors: 2,
    weeklyThroughputPerUser: 1.5, weeklyDeliveredIssues: 3, failuresPerIssue: .3,
  },
  demo: {
    averageTokensPerRun: 1600, toolCallsObserved: 12, toolCallsSucceeded: 10,
    toolCallsFailed: 2, toolSuccessRate: 83, coveredRuns: 3, humanInterventions: 1,
    averageHumanInterventions: .33, coveredSucceededRuns: 2, automatedSucceededRuns: 1,
    automationRate: 50,
  },
  trend: [
    { date: '2026-08', runs: 1, issues: 1, succeeded: 1, failed: 0, tokens: 1000,
      inputTokens: 500, outputTokens: 300, cacheReadTokens: 200, cacheWriteTokens: 30,
      costCny: .2, executionDurationMs: 30000 },
    { date: '2026-09', runs: 2, issues: 2, succeeded: 1, failed: 1, tokens: 2200,
      inputTokens: 1100, outputTokens: 600, cacheReadTokens: 500, cacheWriteTokens: 80,
      costCny: .45, executionDurationMs: 50000 },
    { date: '2026-10', runs: 4, issues: 3, succeeded: 2, failed: 1, tokens: 3200,
      inputTokens: 1600, outputTokens: 900, cacheReadTokens: 700, cacheWriteTokens: 140,
      costCny: .6, executionDurationMs: 60000 },
  ],
  runTraces: [
    { runId: 'run-1', traceId: 'trace-1', issueId: 'issue-1', workflowTaskId: 'task-1',
      workflowName: 'Delivery', status: 'succeeded', issueSource: 'local', origin: 'internal',
      createdAt: '2026-10-20T08:00:00Z', finishedAt: '2026-10-20T08:01:00Z',
      durationMs: 60000, tokens: 2400, costCny: .5, costKnown: true,
      toolCallsSucceeded: 5, toolCallsFailed: 0, humanInterventions: 0 },
    { runId: 'run-2', traceId: 'trace-2', externalId: 'GF-9', workflowTaskId: 'task-2',
      workflowName: 'Review', status: 'failed', issueSource: 'gongfeng', origin: 'external',
      createdAt: '2026-10-21T10:00:00Z', finishedAt: '2026-10-21T10:02:00Z',
      executionDurationMs: 120000, durationMs: 120000, tokens: 800, costCny: .1,
      costKnown: false, toolCallsSucceeded: 1, toolCallsFailed: 2,
      errorNodeId: 'review', errorMessage: 'credential invalid token' },
  ],
  workflows: [
    { key: 'flow-1', name: 'Delivery', runs: 3, successRate: 67, reworkRate: 20,
      cancelRate: 0, tokens: 5000, inputTokens: 2500, outputTokens: 1500, costCny: 1,
      tokenSharePct: 78, costSharePct: 80, averageExecutionDurationMs: 50000,
      qualityUnknownRuns: 0, costUnknownRuns: 0, nodeCount: 3, nodeTypes: { agent: 2, approval: 1 } },
    { key: 'flow-2', name: 'Review', runs: 1, successRate: 0, reworkRate: 0,
      cancelRate: 0, tokens: 1400, costCny: .25, tokenSharePct: 22, costSharePct: 20 },
  ],
  models: [
    { key: 'gpt', name: 'GPT', runs: 3, tokens: 5000, inputTokens: 2500, outputTokens: 1500,
      costCny: 1, tokenSharePct: 78, costSharePct: 80, successRate: 67, qualityUnknownRuns: 0 },
    { key: 'claude', name: 'Claude', runs: 1, tokens: 1400, costCny: .25, tokenSharePct: 22 },
  ],
  nodes: [
    { key: 'impl', name: 'Implement', runs: 3, succeeded: 2, failedNodes: 1,
      reworkNodes: 1, retryRate: 33, averageExecutionDurationMs: 40000,
      p95ExecutionDurationMs: 80000, successRate: 67, tokens: 4000, toolCalls: 8 },
    { key: 'review', name: 'Review', runs: 2, succeeded: 1, failedNodes: 1,
      averageExecutionDurationMs: 20000, successRate: 50, tokens: 2400, toolCalls: 4 },
  ],
  issueSources: [
    { key: 'local', runs: 3, succeeded: 2, completedIssues: 2, tokens: 5000, costCny: 1 },
    { key: 'gongfeng', runs: 1, succeeded: 0, completedIssues: 0, tokens: 1400, costCny: .25 },
  ],
  categories: [{ key: 'requirement', runs: 3, succeeded: 2, completedIssues: 2, tokens: 5000, costCny: 1 }],
  failures: [{ code: 'credential invalid token', nodeId: 'review', count: 1 }],
  workflowVersions: [{ workflowId: 'flow-1', name: 'Delivery', version: 2, runs: 3,
    successRate: 67, averageExecutionDurationMs: 50000, tokens: 5000, costCny: 1 }],
  userUsage: [{ date: '2026-10-20', userId: 1, runs: 2, success: 1, failed: 1,
    tokens: 3200, inputTokens: 1800, outputTokens: 900, costCny: .6, costKnown: true }],
  weekHour: [{ weekday: 1, hour: 10, runs: 2, tokens: 3200, costCny: .6 }],
};

test('observability renders rich usage, drills into details, filters, and expands cards', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.currentUser={...state.data.users[0],id:1,isGuest:false};
    state.data.users[0]=state.currentUser;
    state.analytics=${JSON.stringify(analytics)};
    state.obsTab='overview';state.obsScope='project';state.obsTrendGrain='all';
    renderObservability();
  `);
  const report = app.evaluate(`buildObsReportHTML()`);
  assert.match(report, /¥1\.25/);
  assert.doesNotMatch(report, /\$1\.25/);
  const body = app.document.querySelector('#obsBody');
  body.querySelector('[data-obs-metric="cost"]')?.click();
  body.querySelector('[data-obs-bd-dim="model"]')?.click();
  body.querySelector('[data-obs-bd-metric="cost"]')?.click();
  body.querySelector('[data-obs-heat-metric="runs"]')?.click();
  body.querySelector('[data-obs-jump="fails"]')?.click();
  const search = body.querySelector('[data-obs-detail-search]');
  if (search) {
    search.value = 'credential';
    search.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  }
  const expand = body.querySelector('[data-obs-card="trend"] [data-obs-expand]');
  expand?.click();
  app.document.querySelector('#obsExpand')?.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Tab', bubbles: true }));
  app.evaluate(`obsCollapse();setObsTrendGrain('month');setObsPeriod('2026-10');obsShiftPeriod(-1);obsShiftPeriod(1)`);
  await settle(5);
  assert.match(body.textContent, /Token|成本|credential|运行/);
  assert.match(app.evaluate(`obsCsvContent(obsDetailCurrent().tables)`), /credential|错误/);
  assert.match(report, /Delivery|GPT/);
});

test('delivery observability renders funnel, owners, filters, and CSV data', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const delivery = {
    issues: 2, sopSucceeded: 2, withMr: 2, totalMrs: 3, mrMerged: 2, mrIntegrated: 1, issuesWithMergedMr: 1,
    delivered: 1, requirementCount: 1, bugFixCount: 1, averageExecutionDurationMs: 50000,
    averageDurationMs: 120000, p50ExecutionDurationMs: 40000, p95ExecutionDurationMs: 90000,
    totalTokens: 6400,
  };
  const deliveries = [
    { issueId: 'issue-1', externalId: 'GF-1', externalIid: 1, title: 'Dashboard',
      source: 'gongfeng', workflowId: 'flow-1', workflowName: 'Delivery',
      runStatus: 'succeeded', sopSucceeded: true, delivered: true, mrState: 'merged',
      mrUrl: 'https://example.com/mr/1', mrIid: 1, runCount: 2, userId: 1,
      mrs: [
        {project:'team/frontend',iid:1,url:'https://git.example.com/team/frontend/-/merge_requests/1',state:'merged'},
        {project:'team/backend',iid:1,url:'https://git.example.com/team/backend/-/merge_requests/1',state:'merged'},
        {project:'team/frontend',iid:1,url:'http://git.example.com/team/frontend/merge_requests/1',state:'merged'},
      ],
      durationMs: 120000, executionDurationMs: 50000, cycleDurationMs: 180000,
      tokens: 5000, costCny: 1, costKnown: true, lanes: [{ label: 'Code', started: true, status: 'succeeded' }] },
    { issueId: 'issue-2', externalId: 'TAPD-2', title: 'Fix login', source: 'tapd',
      workflowId: 'flow-2', workflowName: 'Review', runStatus: 'failed',
      sopSucceeded: false, delivered: false, mrState: 'opened', runCount: 1, userId: 2,
      mrs:[{project:'team/worker',iid:2,url:'https://git.example.com/team/worker/-/merge_requests/2',state:'integrated'}],
      durationMs: 60000, tokens: 1400, costCny: .25, costKnown: false },
  ];
  app.evaluate(`
    state.analytics=${JSON.stringify({ ...analytics, delivery, deliveries })};
    state.obsTab='delivery';state.obsAnalyticsFull=true;state.obsDeliveryFilter='all';
    renderObservability();
  `);
  const body = app.document.querySelector('#obsBody');
  const row = body.querySelector('[data-edit-issue="issue-1"]');
  assert.equal(row.querySelectorAll('a[href*="merge_requests/1"]').length, 2);
  assert.match(row.textContent, /team\/frontend !1/);
  assert.match(row.textContent, /team\/backend !1/);
  assert.equal(app.evaluate(`obsDeliveryStages().find(s=>s.key==='mr').value`), 1, 'funnel counts requirements while KPI counts two MRs');
  assert.equal(app.evaluate(`obsDeliveryMatches(state.analytics.deliveries[1],'baseline')`), true, 'filters inspect all MR states');
  const exported = app.evaluate(`buildObsReportHTML()`);
  assert.match(exported, /team\/frontend !1/);
  assert.match(exported, /team\/backend !1/);
  body.querySelector('[data-obs-delivery="delivered"]')?.click();
  app.evaluate(`obsSetDeliveryFilter('user:1',false);obsSetDeliveryFilter('all',false)`);
  body.querySelector('[data-obs-funnel="mr"]')?.click();
  await settle(3);
  assert.match(body.textContent, /交付|Dashboard|负责人/);
  assert.match(app.evaluate(`obsCsvContent(obsDeliveryTableDef(true).tables)`), /Dashboard|工蜂/);
  assert.match(app.evaluate(`obsCsvContent(obsDeliveryTableDef(true).tables)`), /team\/backend !1/);
});

test('conversation code changes have their own group, close the Token ring, and flow through delivery', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const summary = { ...analytics.summary, totalTokens: 7400, totalCostCny: 1.55, conversationWorkspaces: 3, conversationLinkedWorkspaces: 2, conversationIssues: 1, conversationIssuesWithMr: 1, conversationIssuesDelivered: 1, conversationTokens: 1000, conversationCostCny: .3 };
  const trend = analytics.trend.map(p => p.date === '2026-10' ? { ...p, tokens: 4200, costCny: .9, conversationTokens: 1000, conversationCostCny: .3, conversationIssues: 1, conversationWorkspaces: 3 } : p);
  const delivery = { issues: 2, sopSucceeded: 1, directIssues: 1, directWithMr: 1, directDelivered: 1, withMr: 1, totalMrs: 1, mrMerged: 1, issuesWithMergedMr: 1, delivered: 1 };
  const deliveries = [
    { issueId: 'issue-chat', title: '对话里提的需求', source: 'local', workflowId: 'conversation-workspace',
      workflowName: '对话改代码', runStatus: 'idle', sopSucceeded: false, directDelivery: true, delivered: true, mrState: 'merged',
      runCount: 0, userId: 1, tokens: 1000, costCny: .3, costKnown: true,
      mrs: [{ project: 'team/app', iid: 7, url: 'https://git.example.com/team/app/-/merge_requests/7', state: 'merged' }] },
    { issueId: 'issue-sop', title: 'SOP 需求', source: 'local', workflowId: 'flow-1', workflowName: 'Delivery', runStatus: 'succeeded',
      sopSucceeded: true, delivered: false, runCount: 1, userId: 1, tokens: 2400, costCny: .5, costKnown: true },
  ];
  const full = { ...analytics, summary, trend, delivery, deliveries };
  app.evaluate(`
    state.currentUser={...state.data.users[0],id:1,isGuest:false};
    state.data.users[0]=state.currentUser;
    state.analytics=${JSON.stringify(full)};
    state.obsAnalyticsFull=true;
    state.obsTab='overview';state.obsScope='project';state.obsTrendGrain='all';
    renderObservability();
  `);
  const groups = [...app.document.querySelectorAll('#obsBody .obs-kg')].map(g => ({ title: g.querySelector('h4 span').textContent, text: g.textContent }));
  const titles = groups.map(g => g.title);
  assert.deepEqual(titles.slice(0, 5), ['Vibe Coding 与协作', 'SOP 产出与质量', '投入 · SOP + 对话改代码', '对话改代码 · 不走 SOP', 'Loop 自动化']);
  const groupText = title => groups.find(g => g.title === title).text;
  const conv = groupText('对话改代码 · 不走 SOP');
  assert.match(conv, /累计需求1其中 1 个已提 PR · 1 个已交付/);
  assert.match(conv, /累计 Token1\.0k占全部 Token 14%/);
  assert.match(conv, /累计工作区32 个已关联需求 · 1 个未关联/);
  assert.doesNotMatch(groupText('投入 · SOP + 对话改代码'), /工作区/, 'the 投入 group keeps three cards');
  assert.match(groupText('投入 · SOP + 对话改代码'), /其中对话改代码1\.0k/);
  assert.match(groupText('Loop 自动化'), /SOP 6\.4k ÷ 4 次运行/);
  // The Token ring adds up to the Token card.
  const rows = JSON.parse(app.evaluate(`JSON.stringify(obsBreakdownRows('sop','tokens'))`));
  assert.equal(rows.reduce((n, r) => n + r.tokens, 0), 7400);
  assert.equal(rows.find(r => r.conversation).name, '对话改代码');
  assert.equal(JSON.parse(app.evaluate(`JSON.stringify(obsBreakdownRows('src','tokens'))`)).some(r => r.conversation), false, 'sources already carry linked conversations');
  // A small conversation slice is never folded into 其他, even with many SOPs.
  const crowded = JSON.parse(app.evaluate(`
    (()=>{const saved=state.analytics.workflows;
      state.analytics.workflows=Array.from({length:9},(_,i)=>({key:'w'+i,name:'W'+i,tokens:100000-i,costCny:1}));
      const rows=obsBreakdownRows('sop','tokens');
      state.analytics.workflows=saved;
      return JSON.stringify(rows)})()`));
  assert.equal(crowded.length, 6);
  assert.equal(crowded[5].name, '对话改代码');
  assert.equal(crowded[4].other, true);
  assert.equal(crowded[4].name, '其他 5 项');
  assert.equal(crowded.reduce((n, r) => n + r.tokens, 0), 100000 * 9 - 36 + 1000);
  // A period shows that period's conversation numbers.
  const view = JSON.parse(app.evaluate(`state.obsTrendGrain='month';state.obsPeriodKey='2026-10';JSON.stringify(obsScopedView())`));
  assert.deepEqual([view.conversationTokens, view.sopTokens, view.tokens, view.conversationIssues, view.conversationWorkspaces], [1000, 3200, 4200, 1, 3]);
  // Clicking the conversation slice opens its deliveries.
  app.evaluate(`state.obsTrendGrain='all';state.obsPeriodKey='';state.obsBdDim='sop';state.obsBdMetric='tokens';renderObservability();obsBreakdownJump(obsBreakdownRows('sop','tokens').findIndex(r=>r.conversation))`);
  await settle(3);
  assert.equal(app.evaluate(`state.obsTab`), 'delivery');
  assert.equal(app.evaluate(`state.obsDeliveryFilter`), 'direct');
  const body = app.document.querySelector('#obsBody');
  assert.ok(body.querySelector('[data-edit-issue="issue-chat"]'));
  assert.equal(body.querySelector('[data-edit-issue="issue-sop"]'), null);
  const row = body.querySelector('[data-edit-issue="issue-chat"]');
  assert.match(row.textContent, /对话直接交付/);
  assert.doesNotMatch(row.textContent, /SOP 成功|idle/);
  // The label follows the MRs, and never reads as delivered when it is not.
  const label = (mrs, delivered) => app.evaluate(`obsDirectDeliveryText(${JSON.stringify({ directDelivery: true, delivered, mrs })})`);
  const url = 'https://git.example.com/team/app/-/merge_requests/';
  assert.equal(label([], false), '对话改代码中');
  assert.equal(label([{ project: 'team/app', iid: 1, url: url + 1, state: 'opened' }], false), '对话 · PR 待合入');
  assert.equal(label([{ project: 'team/app', iid: 2, url: url + 2, state: 'closed' }], false), '对话 · PR 已关闭');
  assert.equal(label([{ project: 'team/app', iid: 3, url: url + 3, state: 'merged' }], false), '对话 · PR 已合入');
  assert.equal(label([{ project: 'team/app', iid: 4, url: url + 4, state: 'closed' }, { project: 'team/app', iid: 5, url: url + 5, state: 'opened' }], false), '对话 · PR 待合入');
  assert.equal(label([{ project: 'team/app', iid: 6, url: url + 6, state: 'merged' }], true), '对话直接交付');
  // The funnel counts conversation work as executed once it has a PR.
  const stages = JSON.parse(app.evaluate(`JSON.stringify(obsDeliveryStages())`));
  assert.deepEqual(stages.slice(0, 2).map(s => [s.name, s.value]), [['有执行的单', 2], ['SOP 成功 / 对话已提 PR', 2]]);
  assert.equal(app.evaluate(`state.analytics.deliveries.filter(it=>obsDeliveryMatches(it,'done')).length`), 2);
  assert.equal(app.evaluate(`obsOwnerRows()[0].delivered`), 1);
  const report = app.evaluate(`buildObsReportHTML()`);
  assert.match(report, /对话直接交付/);
  assert.match(report, /1 个需求/);
  // Without conversation data the group is not shown.
  app.evaluate(`state.analytics=${JSON.stringify(analytics)};state.obsTab='overview';renderObservability()`);
  assert.doesNotMatch(app.document.querySelector('#obsBody').textContent, /不走 SOP/);
});

test('conversation activity cards show counts, collaboration share, periods without runs, and report data', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const activity = { sessions: 3, collaborativeSessions: 2, requirements: 4, sopRuns: 5,
    sessionUsage: { tokens: 6000, inputTokens: 5000, outputTokens: 1000, cacheReadTokens: 3000, cacheWriteTokens: 500, reportedReplies: 3, missingReplies: 1, undatedReplies: 1 },
    collaborativeUsage: { tokens: 4500, inputTokens: 4000, outputTokens: 500, reportedReplies: 2, missingReplies: 0 } };
  app.evaluate(`
    state.analytics={...${JSON.stringify(analytics)},activity:${JSON.stringify(activity)},trend:[],runTraces:[],
      activityTrend:[{date:'2026-10',sessions:2,collaborativeSessions:1,requirements:1,sopRuns:0,
        sessionUsage:{tokens:1000,reportedReplies:1},collaborativeUsage:{tokens:500,reportedReplies:1}}]};
    state.obsTab='overview';state.obsScope='project';state.obsTrendGrain='all';state.obsPeriodKey='';
    renderObservability();
  `);
  const group = () => [...app.document.querySelectorAll('#obsBody .obs-kg')]
    .find(g => g.querySelector('h4 span').textContent === 'Vibe Coding 与协作');
  assert.ok(group(), 'the activity group appears on the usage page');
  assert.deepEqual([...group().querySelectorAll('.obs-kpi-label')].map(el => el.textContent),
    ['会话数', '创建需求', '需求 SOP', '多人协作会话', '会话 Token', '协作会话 Token']);
  assert.match(group().textContent, /占会话 66\.7%/);
  assert.match(group().textContent, /1 条回复未采集 · 仅含已记录部分/);
  assert.match(group().textContent, /缺少时间，仅计入累计用量/);
  assert.match(app.evaluate('obsActivityUsageCard(state.analytics.activity,false).value'), /6\.0k/);
  assert.match(app.evaluate('obsActivityUsageCard(state.analytics.activity,true).value'), /4\.5k/);
  const report = app.evaluate('buildObsReportHTML()');
  assert.match(report, /会话数|创建需求|需求 SOP|多人协作会话/);
  assert.match(report, /66\.7%/);
  assert.match(report, /协作会话 Token/);
  assert.match(report, /4\.5k/);
  app.evaluate(`state.obsTrendGrain='month';state.obsPeriodKey='';renderObservability()`);
  assert.equal(app.evaluate('obsPeriodKey()'), '2026-10', 'activity-only periods can be selected without run data');
  assert.ok(app.evaluate(`obsPeriodChoices().includes('2026-10')`));
  assert.match(group().textContent, /占会话 50\.0%/);
  assert.equal(app.evaluate('obsActivityView().sopRuns'), 0);
  assert.equal(app.evaluate('obsActivityView().sessionUsage.tokens'), 1000);
  assert.equal(app.evaluate('obsActivityView().collaborativeUsage.tokens'), 500);
  app.evaluate(`state.obsPeriodKey='2026-11';renderObservability()`);
  assert.match(group().textContent, /暂无会话/);
  assert.equal(app.evaluate('obsActivityView().sessions'), 0);
  assert.equal(app.evaluate('obsActivityUsageCard(obsActivityView(),false).value'), '0');
  app.evaluate(`
    state.obsTrendGrain='week';state.obsPeriodKey='2026-W42';
    state.analytics.activityTrend=[{date:'2026-W42',sessions:1,collaborativeSessions:1,requirements:2,sopRuns:3}];
    renderObservability();
  `);
  assert.match(group().textContent, /占会话 100\.0%/);
  app.evaluate(`
    state.obsTrendGrain='day';state.obsPeriodKey='2026-10-08';
    state.analytics.activityTrend=[{date:'2026-10-08',sessions:1,collaborativeSessions:0,requirements:0,sopRuns:0}];
    renderObservability();
  `);
  assert.match(group().textContent, /占会话 0\.0%/);
  app.evaluate(`state.analytics.activityTrend[0].sessionUsage={missingReplies:2};renderObservability()`);
  assert.equal(app.evaluate('obsActivityUsageCard(obsActivityView(),false).value'), '未采集');
  assert.match(group().textContent, /2 条回复未采集/);
  app.evaluate(`delete state.analytics.activity;renderObservability()`);
  assert.match(group().textContent, /暂无统计数据/);
  assert.doesNotMatch(group().textContent, /NaN|Infinity/);
});
