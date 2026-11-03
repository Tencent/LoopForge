import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('deployment dashboard renders SkillHub KPIs, trend, statuses, filtering, and API query', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    const repo=state.data.repositories[0];
    repo.name='skillhub';repo.gongfengProjectId='example/team-project';
    state.deploymentData={
      queriedAt:'2026-10-30T12:00:00Z',pipelineUrl:'https://qci.example/pipeline',
      summary:{total:3,succeeded:1,failed:1,running:1,deployments7d:2,average7dDurationMs:90000,averageDurationMs:120000,p50DurationMs:100000,p90DurationMs:180000,successRate:50},
      records:[
        {id:'101',runNumber:101,status:2,statusLabel:'成功',durationMs:90000,branch:'main',startedAt:'2026-10-30T08:00:00Z',starter:'alice',revision:'abcdef123456',comment:'release',url:'https://qci.example/101'},
        {id:'102',runNumber:102,status:3,statusLabel:'失败',durationMs:180000,branch:'feature',startedAt:'2026-10-29T08:00:00Z',starter:'bob',revision:'fedcba654321',comment:'test'},
        {id:'103',runNumber:103,status:1,statusLabel:'运行中',durationMs:0,branch:'main',startedAt:'2026-10-31T02:00:00Z',starter:'ci',revision:'123456789012',comment:'deploy'}
      ]
    };
    renderDeploymentDashboard();
  `);
  const body = app.document.querySelector('#deploymentBody');
  assert.match(body.textContent, /近 7 天平均耗时|1\.5m|正在部署|近 30 天成功率|50\.0%/);
  assert.match(body.textContent, /#101|#102|#103|release|运行中/);
  assert.ok(body.querySelector('svg[aria-label="部署耗时趋势"]'));
  assert.equal(app.evaluate(`deploymentStatusClass(2)`), 'succeeded');
  assert.equal(app.evaluate(`deploymentStatusClass(8)`), 'running');
  assert.equal(app.evaluate(`deploymentStatusClass(13)`), 'failed');
  assert.equal(app.evaluate(`duration(3600000)`), '1.0h');

  app.router.get(/^\/api\/deployment-metrics\?/, call => ({ queriedAt: '2026-10-31T03:00:00Z', branch: new URL(call.url).searchParams.get('branch'), summary: {}, records: [] }));
  const form = body.querySelector('#deploymentQuery');
  form.elements.branch.value = 'release/v2';
  form.elements.branch.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(5);
  const call = app.router.calls.find(c => c.path === '/api/deployment-metrics');
  assert.match(call.search, /projectId=project-1|repositoryId=repo-1|branch=release%2Fv2/);
  assert.match(body.textContent, /分支「release\/v2」最近 30 天没有流水线记录/);
});

test('metrics helpers calculate windows, phases, source identity, buckets, and SVG summaries', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = app.evaluate(`(() => {
    state.metricsRange='custom';state.metricsDateFrom='2026-10-10';state.metricsDateTo='2026-10-01';
    const win=metricsWindow();
    const phases=metricsDeliveryPhases({clarificationDurationMs:60000,developmentDurationMs:120000,testDurationMs:30000,phaseSamples:{clarification:1,development:1,testing:1,inferred:0}});
    const unknown=metricsDeliveryPhases({lanes:[]});
    const tapd={id:'i1',projectId:'p',source:'tapd',externalRef:{source:'tapd',externalId:'T-7'},status:'done'};
    const local={id:'i2',source:'local',tapdId:'T-8',kind:'requirement'};
    const buckets=metricsBuckets({from:new Date('2026-10-01T00:00:00'),to:new Date('2026-10-03T23:59:59'),grain:'day'});
    const trend=[{label:'10/1',deliveries:2,tokens:100,firstPass:50},{label:'10/2',deliveries:4,tokens:200,firstPass:75}];
    return {
      range:[metricsDateValue(win.from),metricsDateValue(win.to),win.grain],
      phases,unknown,
      identity:[metricsRequirementTAPDID(tapd),metricsRequirementKey(tapd),metricsRequirementTAPDID(local),metricsRequirementIssue(local),metricsIssueDone(tapd)],
      week:metricsISOWeekKey('2026-10-31'),bucket:metricsBucketKey('2026-10-31','month'),
      buckets:buckets.map(x=>x.key),
      stage:metricsNodeStageSummary([{name:'Frontend build',nodeRuns:2,averageExecutionDurationMs:100},{name:'前端修复',runs:1,averageExecutionDurationMs:400},{name:'API',runs:9,averageExecutionDurationMs:999}],'frontend'),
      trend:metricsTrendSVG(trend,'deliveries'),
      phaseSvg:metricsPhaseTrendSVG([{label:'10/1',clarification:60,development:120,testing:null,total:180}]),
      empty:metricsTrendSVG([],'deliveries')
    };
  })()`);
  assert.equal(JSON.stringify(result.range), JSON.stringify(['2026-10-01', '2026-10-10', 'day']));
  assert.equal(result.phases.total, 210000);
  assert.equal(result.unknown.total, null);
  assert.equal(JSON.stringify(result.identity), JSON.stringify(['T-7', 'tapd:p:T-7', 'T-8', true, true]));
  assert.equal(result.week, '2026-W44');
  assert.equal(result.bucket, '2026-10');
  assert.equal(JSON.stringify(result.buckets), JSON.stringify(['2026-10-01', '2026-10-02', '2026-10-03']));
  assert.equal(result.stage.averageMs, 200);
  assert.equal(result.stage.samples, 3);
  assert.match(result.trend, /交付需求趋势|10\/2/);
  assert.match(result.phaseSvg, /澄清|研发|已采集耗时/);
  assert.match(result.empty, /暂无趋势数据/);
});

test('metrics demo snapshot drives the full dashboard with observable project KPIs', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.metricsRange='30d';
    const demo=metricsDemoSnapshot(metricsWindow());
    state.metricsData=demo.analytics;state.metricsIssues=demo.issues;
    state.metricsDataKey=metricsQueryKey();state.metricsLoading=false;state.metricsError='';
    renderMetricsDashboard();
  `);
  const text = app.document.querySelector('#metricsBody').textContent;
  assert.match(text, /交付数（含 Issue）|MR 合入数量|需求 \/ Issue 阶段耗时/);
  assert.match(text, /交付趋势|一次通过率趋势|自动完成率趋势/);
  assert.match(text, /效能指标接入|TAPD/);
  assert.ok(app.document.querySelectorAll('#metricsBody .obs-kpi').length >= 5);
});
