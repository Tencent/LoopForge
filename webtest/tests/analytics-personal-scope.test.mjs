import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('personal analytics waits for the user identity and restores self after clearing a member', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.obsScope='user';state.obsUserFilter='';state.obsUserFilterReady=false;state.currentUser=null;ensureObsUserFilter()`);
  app.evaluate(`state.currentUser={id:42,isGuest:false};ensureObsUserFilter()`);
  assert.equal(app.evaluate('state.obsUserFilter'), '42');
  app.evaluate(`state.obsUserFilter='';ensureObsUserFilter()`);
  assert.equal(app.evaluate('state.obsUserFilter'), '42');
  assert.match(app.evaluate('analyticsQuery({overview:true})'), /userId=42/);
  assert.doesNotMatch(app.evaluate('obsFilterBar()'), />全部责任人<|项目内 · 全部成员/);
});

test('unassigned project analytics is explicit and does not change the topbar project', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.currentUser={id:42,isGuest:false};state.projectFilter='project-1';state.obsScope='user';state.obsUserFilter='42';state.obsProjectScope='unassigned'`);
  const query = new URLSearchParams(app.evaluate('analyticsQuery({overview:true})'));
  assert.equal(query.get('projectScope'), 'unassigned');
  assert.equal(query.get('userId'), '42');
  assert.equal(query.has('projectId'), false);
  assert.equal(app.evaluate('state.projectFilter'), 'project-1');
  assert.match(app.evaluate('obsFilterBar()'), /未归属项目/);
  assert.equal(app.evaluate('obsReportScope().project'), '未归属项目');
  const across = new URLSearchParams(app.evaluate('analyticsQuery({overview:true,crossProject:true})'));
  assert.equal(across.has('projectScope'), false);
  assert.equal(across.get('userId'), '42');
});

test('the unassigned project control changes the request and project mode restores project-wide analytics', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/analytics', call => ({
    summary: { totalCostCny: 0, costUnknownRuns: 0 },
    activity: { sessions: call.url.searchParams.get('projectScope') === 'unassigned' ? 2 : 9,
      sessionUsage: { tokens: 80, inputTokens: 40, outputTokens: 40, reportedReplies: 1 } },
    trend: [], runTraces: [],
  }));
  app.evaluate(`state.currentUser={id:42,isGuest:false};state.projectFilter='project-1';state.obsScope='user';state.obsUserFilter='42';obsEChartsFailed=true;switchView('observability')`);
  await settle(8);
  const control = app.document.querySelector('[data-obs-project-scope="unassigned"]');
  assert.ok(control, 'personal view needs an unassigned-project entry');
  control.click();
  await settle(8);
  assert.equal(app.evaluate('state.analytics.activity.sessions'), 2);
  assert.match(app.document.querySelector('#obsBody').textContent, /未归属项目/);
  app.document.querySelector('[data-obs-scope="project"]').click();
  await settle(8);
  const latest = app.router.calls.filter(call => call.path === '/api/analytics').at(-1);
  assert.equal(latest.url.searchParams.get('projectId'), 'project-1');
  assert.equal(latest.url.searchParams.has('projectScope'), false);
  assert.equal(latest.url.searchParams.has('userId'), false);
});

test('ordinary conversation cost exclusion is visible beside the cost metric and in exported reports', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.analytics={summary:{totalCostCny:0,costUnknownRuns:0},activity:{sessions:1,sessionUsage:{tokens:80,reportedReplies:1}},trend:[],runTraces:[]};obsEChartsFailed=true;renderObservability()`);
  assert.match(app.document.querySelector('#obsBody').textContent, /普通对话成本未计入/);
  assert.match(app.evaluate('buildObsReportHTML()'), /普通对话成本未计入/);
});
