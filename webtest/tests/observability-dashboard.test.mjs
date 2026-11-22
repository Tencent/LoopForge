import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const snapshot = {
  dashboard: {
    deliveredIssues: 3, sopDeliveredIssues: 2, vibeDeliveredIssues: 1,
    sopRuns: 4, succeededRuns: 2, failedRuns: 1, humanRuns: 1,
    p95ExecutionDurationMs: 180000, activeMembers: 2,
    trend: [{ date: '2026-11', sopRuns: 4, sopDelivered: 2, vibeDelivered: 1 }],
    humanNodes: [{ key: 'flow/review', name: 'Review' }, { key: 'flow/test', name: 'Test' }],
    humanTrend: [{ date: '2026-11', node: 'flow/review', approvals: 3, rejections: 1, retries: 1 }, { date: '2026-11', node: 'flow/test', approvals: 1, retries: 1 }],
  },
  summary: { totalTokens: 1000, totalCostCny: 7, conversationWorkspaces: 2, costUnknownRuns: 1 },
  activity: { sessions: 5, collaborativeSessions: 2 },
  activityTrend: [{ date: '2026-11', sessions: 5, collaborativeSessions: 2 }],
};

test('unified dashboard shows Vibe Coding and SOP charts without explanatory footnotes', async t => {
  const app = await loadApp(); t.after(app.close);
  app.evaluate(`state.obsDashboardDays='all';state.obsDashboardSnapshot=${JSON.stringify(snapshot)};renderObservability()`);
  await settle(4);
  const root = app.document.querySelector('.obs-dashboard');
  const value = key => root.querySelector(`[data-od-number="${key}"]`).getAttribute('aria-label');
  assert.equal(app.document.querySelectorAll('[data-obs-tab]').length, 0);
  assert.equal(root.querySelectorAll('[data-obs-chart]').length, 3);
  assert.equal(root.querySelectorAll('.od-kpis .od-kpi').length, 3);
  assert.equal(value('delivered'), '3'); assert.equal(value('success'), '66.7%'); assert.equal(value('humanRate'), '25.0%');
  assert.equal(value('sessions'), '5'); assert.equal(value('collaboration'), '2');
  assert.equal(value('members'), '2'); assert.equal(value('workspaces'), '2');
  assert.equal(value('tokens'), '1.0k'); assert.equal(value('cost'), '¥7.00');
  assert.doesNotMatch(root.textContent, /口径说明|仅统计|拟新增|暂停\/恢复|上报优先|NaN|Infinity/);
  const before = app.evaluate(`obsDashboardPoints(state.obsDashboardSnapshot)[0].approvals`);
  const select = root.querySelector('#obsDashboardNode');select.value = 'flow/review';select.dispatchEvent(new app.window.Event('change'));
  assert.equal(before, 4); assert.equal(app.evaluate(`obsDashboardPoints(state.obsDashboardSnapshot)[0].approvals`), 3);
  assert.equal(app.document.querySelector('.obs-dashboard'), root, 'node changes keep the chart containers for transition updates');
  const result = app.evaluate(`(() => {
    const p=obsDashboardPoints(state.obsDashboardSnapshot),theme=OBS_THEME_PRESETS.light;
    const normalized=obsDashboardOption('human',p,{theme,reduced:true});
    const raw=obsDashboardOption('human',p,{theme,reduced:false,metric:'count'});
    const vibe=obsDashboardOption('vibe',p,{theme,reduced:true});
    return {rate:normalized.series[0].data[0],count:raw.series[0].data[0],animated:raw.animation,still:normalized.animation,duration:normalized.animationDuration,lines:vibe.series.map(x=>x.data[0]),tip:normalized.tooltip.formatter([{dataIndex:0}])};
  })()`);
  assert.equal(result.rate, 75); assert.equal(result.count, 3);
  assert.equal(result.animated, true); assert.equal(result.still, false); assert.equal(result.duration, 0);
  assert.deepEqual([...result.lines], [5, 2]); assert.match(result.tip, /75\.0.*百次运行/);
});

test('dashboard range filters reach the API and late responses cannot replace a newer selection', async t => {
  const app = await loadApp(); t.after(app.close);
  let finishOld;
  app.router.get(/^\/api\/analytics\?/, call => {
    if (call.url.searchParams.get('grain') === 'month') return new Promise(resolve => { finishOld = resolve; });
    return { ...snapshot, dashboard: { ...snapshot.dashboard, deliveredIssues: 9 } };
  });
  app.evaluate(`state.obsDashboardDays='all';obsLoadDashboard()`);
  await settle(2); assert.ok(finishOld);
  await app.evaluate(`state.obsDashboardDays='7';obsLoadDashboard()`);
  finishOld(snapshot); await settle(4);
  assert.equal(app.evaluate(`state.obsDashboardSnapshot.dashboard.deliveredIssues`), 9);
  const calls = app.router.calls.filter(c => c.path === '/api/analytics');
  assert.equal(calls.at(-1).url.searchParams.get('scope'), 'dashboard');
  assert.ok(calls.at(-1).url.searchParams.get('from'));assert.ok(calls.at(-1).url.searchParams.get('to'));
  assert.equal(app.evaluate(`obsDashboardDates(state.obsDashboardSnapshot).length`), 7);
  assert.equal(app.evaluate(`state.obsDashboardLoading`), false);
  assert.equal(app.document.querySelector('.obs-dashboard').getAttribute('aria-busy'), 'false');
  app.router.get(/^\/api\/analytics\?/, { status: 503, body: { error: '统计趋势读取失败' } });
  await app.evaluate(`state.obsDashboardDays='30';obsLoadDashboard()`); await settle(2);
  const error = app.document.querySelector('.od-error');assert.equal(error.hidden, false);assert.match(error.textContent, /统计趋势读取失败/);
  assert.equal(app.document.querySelector('#exportObsReport').disabled, true);
});
