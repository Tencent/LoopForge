import test from 'node:test';
import assert from 'node:assert/strict';
import { chooseOption, loadApp, settle } from '../lib/load-app.mjs';

const skillhub = {
  snapshotAt: '2026-10-30T08:00:00Z',
  gongfeng: {
    mergedMonths: { '2026-09': 4, '2026-10': 9 },
    monthlyLegacy: { '2026-09': 3, '2026-10': 2 },
    monthlyMonorepo: { '2026-09': 1, '2026-10': 7 },
    weeklyMerged: { '2026-W40': 2, '2026-W41': 3, '2026-W42': 4 },
    weeklyLegacy: { '2026-W40': 1 }, weeklyMonorepo: { '2026-W40': 1, '2026-W41': 3, '2026-W42': 4 },
    repos: [{ name: 'skillhub', path: 'ai/skillhub', created: 10, merged: 8, opened: 2, lastAt: '2026-10-29', url: 'https://git.example/skillhub' }],
    authors: [{ username: 'alice', mrs: 8 }],
  },
  qci: {
    projectId: 10001, countedPipelines: 3,
    backend: { runNum: 10, recent30d: 5 }, frontend: { runNum: 8, recent30d: 4 },
    e2e: { runNum: 6, url: 'https://qci.example/e2e' },
    deploy: { url: 'https://qci.example/deploy' },
    pipelines: [{ id: 10000002, name: 'Web', kind: '前端', era: '当前', runNum: 8, recent30d: 4, url: 'https://qci.example/web' }],
    dailySample: { labels: ['10/28', '10/29'], backend: [2, 3], frontend: [1, 2], e2e: [1, 1] },
  },
  localWorkflows: { backendUnique: 3, backendStageHits: { '01-requirement': 3, '02-design': 2, '03-code': 3, '04-e2e': 1 }, frontendWorkflows: 2, backendKnowledgeDocs: 4, backendE2eFiles: 1 },
  loop: { issues: 5, doneIssues: 4, totalRuns: 7 },
  evidence: [{ label: '验收证据', url: 'https://example.com/evidence' }],
  insights: [{ title: '吞吐提升', body: '近四周保持增长。' }],
};

test('SkillHub observability renders KPI, charts, deployment evidence, and detail tables', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.skillhubEfficiency=${JSON.stringify(skillhub)};
    state.skillhubDeployError='实时部署接口暂不可达';
    state.obsTab='skillhub';
    obsEChartsFailed=true;
    renderObservability();
  `);
  await settle(3);
  const text = app.document.querySelector('#obsBody').textContent;
  assert.equal(app.evaluate(`state.obsTab`), 'overview');
  assert.equal(app.document.querySelector('#obsSkillhubTab')?.hidden ?? true, true);
  assert.doesNotMatch(text, /测试环境部署|后端测试环境/);
  assert.match(app.evaluate(`obsSkillhubDeployCard()`), /后端测试环境|实时接口：实时部署接口暂不可达/);
  const groups = app.evaluate(`obsSkillhubDetailGroups().map(g=>({group:g.group,items:g.items.filter(Boolean).map(i=>i.title)}))`);
  assert.equal(groups.length, 4);
  assert.match(JSON.stringify(groups), /仓库|流水线|Loop 运行|数据来源|结论/);
  const legacy = app.evaluate(`obsSkillhubCharts(state.skillhubEfficiency)`);
  assert.match(legacy, /每月已合入|近 12 周|sh-bars/);
  assert.match(app.evaluate(`obsSkillhubDeployCharts(state.skillhubEfficiency.qci)`), /每天部署次数|后端|E2E/);
  assert.match(app.evaluate(`obsSkillhubInsights(state.skillhubEfficiency.insights)`), /吞吐提升|近四周保持增长/);
  assert.match(app.evaluate(`obsSkillhubLoopCard(state.skillhubEfficiency.loop)`), /平台需求|已闭环|SOP 运行/);
});

test('observability period, scope, query, user, and filtering helpers preserve their business semantics', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = app.evaluate(`(() => {
    state.analytics={trend:[
      {date:'2026-07',tokens:10},{date:'2026-08',tokens:20},
      {date:'2026-09',tokens:30},{date:'2026-10',tokens:40}
    ],runTraces:[]};
    state.obsTrendGrain='month';state.obsPeriodKey='2026-10';
    state.currentUser={id:1,isGuest:false};
    state.data.users=[{id:1,loginName:'alice',chineseName:'艾丽丝'},{id:2,loginName:'bob'}];
    state.data.projects[0].members=[{userId:2}];state.data.projects[0].ownerId=1;
    state.obsScope='user';state.obsUserFilter='1';state.obsOriginFilter='internal';state.obsSourceFilter='tapd';
    const list=state.analytics.trend.slice(2);
    const prev=obsTrendPrevPoints(state.analytics.trend,list,'month');
    const month=obsPeriodRange('2026-10','month'),week=obsPeriodRange('2026-W42','week'),day=obsPeriodRange('2026-10-30','day');
    return {
      prev:prev.map(x=>x&&x.date), month:month.label, week:week.label, day:day.shortLabel,
      nextMonth:obsStepPeriodKey('2026-12','month',1),
      nextWeek:obsStepPeriodKey('2026-W42','week',1),
      nextDay:obsStepPeriodKey('2026-10-31','day',1),
      query:analyticsQuery({overview:true}),
      names:obsUserChoices().map(x=>obsUserName(x.id)),
      metric:[obsTrendValue({succeeded:3,failed:1},'rate'),obsFormatTrendValue(75,'rate')],
      pct:obsPercentile([10,30,20],95),
      source:obsSourceLabel('gongfeng'),category:obsCategoryLabel('requirement-development')
    };
  })()`);
  assert.equal(result.month, '2026年10月');
  assert.match(result.week, /10\/12–10\/18/);
  assert.equal(result.day, '10/30');
  assert.equal(result.nextMonth, '2027-01');
  assert.equal(result.nextWeek, '2026-W43');
  assert.equal(result.nextDay, '2026-11-01');
  assert.match(result.query, /scope=overview|userId=1|origin=internal|issueSource=tapd|grain=month/);
  assert.match(result.names.join(','), /alice\(艾丽丝\)|bob/);
  assert.equal(result.metric[0], 75);
  assert.equal(result.metric[1], '75%');
  assert.equal(result.pct, 30);
  assert.match(result.source + result.category, /工蜂|需求开发/);
});

test('period-scoped analytics and external source dialog expose trace metrics and send source payloads', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/insights/v1/sources?projectId=project-1', [{ id: 'src-1', name: 'Review CI', tokenPrefix: 'af_x', expiresAt: '2027-01-01T00:00:00Z' }]);
  app.router.post('/api/insights/v1/sources', call => ({ ...call.body, id: 'src-2', token: 'secret-token' }));
  app.router.get(/^\/api\/analytics\?/, call => call.search.includes('skillhub') ? skillhub : { summary: {}, trend: [], runTraces: [] });
  app.router.get('/api/analytics/skillhub', skillhub);
  app.router.get(/^\/api\/deployment-metrics\?/, { records: [], summary: {} });
  app.evaluate(`
    state.currentUser={id:1,isGuest:false,isPlatformAdmin:true};
    state.data.users=[{id:1,loginName:'alice'}];
    state.data.projects[0].ownerId=1;
    state.obsTrendGrain='month';state.obsPeriodKey='2026-10';
    state.analytics={
      summary:{inputTokens:100,cacheReadTokens:50,cacheHitRate:33,cacheWriteTokens:10},
      trend:[
        {date:'2026-09',tokens:100,inputTokens:50,outputTokens:30,cacheReadTokens:20,cacheWriteTokens:4,costCny:1,runs:1,issues:1,succeeded:1,failed:0},
        {date:'2026-10',tokens:300,inputTokens:120,outputTokens:80,cacheReadTokens:60,cacheWriteTokens:8,costCny:3,runs:3,issues:2,succeeded:2,failed:1}
      ],
      runTraces:[
        {runId:'r1',issueId:'i1',status:'succeeded',createdAt:'2026-10-10T01:00:00Z',finishedAt:'2026-10-10T01:02:00Z',durationMs:120000,tokens:200,costCny:2,costKnown:true,toolCallsSucceeded:4,humanInterventions:0},
        {runId:'r2',issueId:'i2',status:'failed',createdAt:'2026-10-11T01:00:00Z',finishedAt:'2026-10-11T01:03:00Z',durationMs:180000,tokens:100,costCny:1,costKnown:true,toolCallsSucceeded:1,toolCallsFailed:2,humanInterventions:1,errorMessage:'failed'}
      ]
    };
  `);
  const scoped = app.evaluate(`obsScopedView()`);
  assert.equal(scoped.total, 2);
  assert.equal(scoped.succeeded, 1);
  assert.equal(scoped.failed, 1);
  assert.equal(scoped.tokens, 300);
  assert.equal(scoped.cost, 3);
  assert.equal(scoped.demo.toolSuccessRate, 500 / 7);
  assert.match(app.evaluate(`obsTrendChart(state.analytics.trend,'tokens')`), /obs-trend-svg|300/);
  assert.match(app.evaluate(`obsPeriodPicker()`), /type="month"|上一段|下一段/);
  assert.equal(app.evaluate(`obsCacheStats(obsScopedView()).rate`), 100 / 3);
  assert.equal(app.evaluate(`obsRunMatchesUser({executionContext:{responsibleUserId:1}},1)`), true);
  app.evaluate(`
    state.data.repositories[0].name='skillhub';
    state.data.repositories[0].gongfengProjectId='example/team-project';
    state.obsScope='user';state.obsUserFilter='1';state.obsOriginFilter='internal';state.obsSourceFilter='tapd';
    setObsScope('project');
    obsClearFilter('origin');obsClearFilter('source');obsClearFilter('user');
  `);
  await settle(5);
  assert.equal(app.evaluate(`state.obsScope`), 'project');
  assert.equal(app.evaluate(`state.obsOriginFilter`), '');
  await app.evaluate(`refreshSkillhubEfficiency()`);
  assert.equal(app.evaluate(`state.skillhubEfficiency`), null);
  assert.ok(!app.router.calls.some(c => c.path === '/api/analytics/skillhub'));

  await app.evaluate(`openObsSources()`);
  await settle(3);
  const dialog = app.document.querySelector('.obs-source-dialog');
  assert.match(dialog.textContent, /Review CI|af_x|外部数据源/);
  dialog.querySelector('[data-add]').click();
  const row = dialog.querySelector('[data-mapping]');
  row.querySelector('input').value = 'github:alice';
  chooseOption(row.querySelector('select'), '1');
  const form = dialog.querySelector('[data-create]');
  form.elements.name.value = 'GitHub Actions';
  form.elements.days.value = '30';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(4);
  const call = app.router.calls.find(c => c.method === 'POST' && c.path === '/api/insights/v1/sources');
  assert.equal(call.body.name, 'GitHub Actions');
  assert.equal(call.body.expiresInDays, 30);
  assert.equal(call.body.userMappings['github:alice'], 1);
  assert.equal(dialog.querySelector('[data-token]').value, 'secret-token');
});
