import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const base = {
  summary: { totalRuns: 3, succeededRuns: 1, failedRuns: 1, qualityRuns: 2, successRate: 50,
    totalTokens: 9000, totalCostCny: 2.13, costUnknownRuns: 2 },
  demo: {}, trend: [], runTraces: [], workflows: [], models: [], nodes: [], failures: [],
  userUsage: [], projectUsage: [], weekHour: [], tokenUsage: [],
  activity: { sessions: 0, collaborativeSessions: 0, requirements: 0, sopRuns: 0 }, activityTrend: [],
  delivery: { issues: 4, delivered: 0 },
};

const deliveries = [
  { issueId: 'issue-1795853187153192960', projectId: 'project-1', issueNumber: 3, source: 'local',
    title: 'snake_case', workflowName: 'Fix', runStatus: 'running', runCount: 1, tokens: 500, costCny: .9, costKnown: false },
  { issueId: 'gh-repo-1', projectId: 'project-1', issueNumber: 1, externalIid: 1, source: 'github',
    title: 'median', workflowName: 'Fix', runStatus: 'running', runCount: 1, tokens: 500, costCny: 1, costKnown: false },
  { issueId: 'tapd-9', projectId: 'project-1', issueNumber: 9, externalId: '1020001', source: 'tapd',
    title: 'story', workflowName: 'Fix', runStatus: 'failed', runCount: 1, tokens: 100, costCny: 0, costKnown: true },
  { issueId: 'legacy-issue', source: 'local', title: 'old row', workflowName: 'Fix', runStatus: 'failed', runCount: 1 },
];

function setup(app, analytics) {
  app.evaluate(`
    state.currentUser={id:7,loginName:'alice',isGuest:false,isPlatformAdmin:true};
    state.data.users=[state.currentUser];
    state.data.projects[0].key='ATL';state.data.projects[0].ownerId=7;
    state.obsScope='project';state.obsTrendGrain='all';state.obsVibeView='activity';state.obsSOPView='human';
    state.analytics=${JSON.stringify(analytics)};state.obsAnalyticsFull=true;state.obsDeliveryFilter='all';
  `);
}
const firstCells = app => [...app.document.querySelectorAll('#obsBody tr[data-edit-issue]')].map(r => r.querySelector('td').textContent.trim());

test('delivery detail, CSV and HTML report show platform requirements by project key, not internal ID', async t => {
  const app = await loadApp(); t.after(app.close);
  setup(app, { ...base, deliveries });
  app.evaluate(`state.obsTab='delivery';renderObservability()`); await settle(3);
  const cells = firstCells(app);
  assert.equal(cells.length, 4);
  assert.match(cells[0], /ATL-3$/);
  assert.match(cells[1], /#1$/, 'GitHub / 工蜂 keep #iid');
  assert.match(cells[2], /1020001$/, 'TAPD keeps the TAPD ID');
  assert.match(cells[3], /legacy-issue$/, 'rows without a number fall back to the internal ID');
  assert.doesNotMatch(cells[0], /issue-1795853187153192960/);
  const csv = app.evaluate(`obsCsvContent(obsDeliveryTableDef(true).tables)`);
  assert.match(csv, /ATL-3/);
  assert.doesNotMatch(csv, /issue-1795853187153192960/);
  const report = app.evaluate(`buildObsReportHTML()`);
  assert.match(report, /ATL-3/);
  assert.doesNotMatch(report, /issue-1795853187153192960/);
});

test('overview cost card keeps the recognised amount when only some runs are unpriced', async t => {
  const app = await loadApp(); t.after(app.close);
  setup(app, { ...base, deliveries: [] });
  const cost = () => app.document.querySelector('[data-obs-stat="cost"] .num')?.textContent;
  app.evaluate(`state.obsTab='overview';renderObservability()`); await settle(3);
  assert.equal(cost(), '¥2.13（部分未识别）');
  assert.match(app.evaluate(`buildObsReportHTML()`), /¥2\.13（部分未识别）/, 'card and exported report agree');
  app.evaluate(`state.analytics.summary.totalCostCny=0;renderObservability()`); await settle(3);
  assert.equal(cost(), '未识别', 'nothing recognised is still shown as unknown');
  app.evaluate(`state.analytics.summary.totalCostCny=1.5;state.analytics.summary.costUnknownRuns=0;renderObservability()`); await settle(3);
  assert.equal(cost(), '¥1.50');
  assert.equal(app.evaluate(`window.lfT=(zh,en)=>en;obsDashboardCostText({cost:2.13,costKnown:false})`), '¥2.13 (partially unrecognized)');
  assert.equal(app.evaluate(`obsDashboardCostText({cost:0,costKnown:false})`), 'Unknown');
});

test('cost card formats sub-cent amounts exactly like costLabel and the exported report', async t => {
  const app = await loadApp(); t.after(app.close);
  setup(app, { ...base, summary: { ...base.summary, totalCostCny: .004, costUnknownRuns: 1 }, deliveries: [] });
  const cost = () => app.document.querySelector('[data-obs-stat="cost"] .num')?.textContent;
  app.evaluate(`state.obsTab='overview';renderObservability()`); await settle(3);
  assert.equal(cost(), '¥0.00400（部分未识别）', 'a partly priced ¥0.004 must not read as ¥0.00');
  assert.match(app.evaluate(`buildObsReportHTML()`), /¥0\.00400（部分未识别）/);
  app.evaluate(`state.analytics.summary.costUnknownRuns=0;renderObservability()`); await settle(3);
  assert.equal(cost(), '¥0.00400');
});

// Runs still running or waiting for approval carry finishedAt = Go zero time (0001-01-01).
// The day / week / month views must count them by createdAt, like the server's trend point,
// instead of dropping their usage and cost.
test('day / week / month views count in-progress runs whose finishedAt is the zero time', async t => {
  const app = await loadApp(); t.after(app.close);
  const zero = '0001-01-01T00:00:00Z';
  const runTraces = [
    { runId: 'r-wait', status: 'waiting', createdAt: '2026-11-28T09:11:08Z', finishedAt: zero, tokens: 4000, costCny: 3.18, costKnown: false },
    { runId: 'r-run', status: 'running', createdAt: '2026-11-28T09:13:08Z', finishedAt: zero, tokens: 4000, costCny: 3.19, costKnown: false },
    { runId: 'r-fail', status: 'failed', createdAt: '2026-11-28T09:12:23Z', finishedAt: '2026-11-28T09:12:26Z', tokens: 0, costCny: 0, costKnown: true },
  ];
  const trend = g => [{ date: g, runs: 3, tokens: 8000, costCny: 6.37, costUnknownRuns: 2 }];
  setup(app, { ...base, summary: { ...base.summary, totalRuns: 3, totalCostCny: 6.37, costUnknownRuns: 2 }, runTraces, deliveries: [] });
  const cost = () => app.document.querySelector('[data-obs-stat="cost"] .num')?.textContent;
  for (const [grain, key] of [['day', '2026-11-28'], ['week', '2026-W48'], ['month', '2026-11']]) {
    app.evaluate(`state.analytics.trend=${JSON.stringify(trend(key))};state.obsTrendGrain='${grain}';state.obsPeriodKey='${key}';state.obsTab='overview';renderObservability()`); await settle(3);
    const view = app.evaluate(`(()=>{const v=obsScopedView();return {n:v.traces.length,active:v.active,cost:Math.round(v.cost*100)/100}})()`);
    assert.deepEqual({ ...view }, { n: 3, active: 2, cost: 6.37 }, `${grain} view keeps the in-progress runs`);
    assert.equal(cost(), '¥6.37（部分未识别）', `${grain} card matches the all-time card`);
  }
  assert.equal(app.evaluate(`obsTraceEventAt({createdAt:'2026-11-28T09:11:08Z',finishedAt:'${zero}'}).toISOString()`), '2026-11-28T09:11:08.000Z');
  assert.equal(app.evaluate(`obsTraceEventAt({createdAt:'${zero}'})`), null);
});
