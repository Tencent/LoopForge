import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('late analytics responses cannot overwrite a newer project scope', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  let release;
  const delayed = new Promise(resolve => { release = resolve; });
  app.router.get('/api/analytics', async call => {
    if (call.url.searchParams.get('projectScope') === 'unassigned') await delayed;
    return {summary:{},activity:{sessions:call.url.searchParams.get('projectScope') === 'unassigned' ? 2 : 9},trend:[],runTraces:[]};
  });
  app.evaluate(`state.currentUser={id:42,isGuest:false};state.projectFilter='project-1';state.obsScope='user';state.obsUserFilter='42';obsEChartsFailed=true`);
  app.evaluate(`setObsProjectScope('unassigned')`);
  await settle(2);
  app.evaluate(`setObsProjectScope('')`);
  await settle(6);
  assert.equal(app.evaluate('state.analytics.activity.sessions'), 9);
  release();
  await settle(6);
  assert.equal(app.evaluate('state.obsProjectScope'), '');
  assert.equal(app.evaluate('state.analytics.activity.sessions'), 9);
});

test('late cross-project responses cannot overwrite a newer selected member', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  let release;
  const delayed = new Promise(resolve => { release = resolve; });
  app.router.get('/api/analytics', async call => {
    const id = call.url.searchParams.get('userId');
    if (id === '42') await delayed;
    return {projectUsage:[{projectId:id,tokens:Number(id)}]};
  });
  app.evaluate(`state.currentUser={id:42,isGuest:false};state.obsScope='user';state.obsUserFilter='42';refreshObsCrossProject()`);
  await settle(2);
  app.evaluate(`state.obsUserFilter='43';refreshObsCrossProject()`);
  await settle(4);
  assert.equal(app.evaluate('state.obsCrossProject.projectUsage[0].projectId'), '43');
  release();
  await settle(4);
  assert.equal(app.evaluate('state.obsCrossProject.projectUsage[0].projectId'), '43');
});

test('a delayed export cannot replace data after the project scope changes', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  let release;
  const delayed = new Promise(resolve => { release = resolve; });
  app.router.get('/api/analytics', async call => {
    if (call.url.searchParams.get('projectScope') === 'unassigned') await delayed;
    return {summary:{},activity:{sessions:call.url.searchParams.get('projectScope') === 'unassigned' ? 2 : 9},trend:[],runTraces:[]};
  });
  app.evaluate(`state.currentUser={id:42,isGuest:false};state.obsScope='user';state.obsUserFilter='42';state.obsProjectScope='unassigned';state.obsAnalyticsFull=false;obsEChartsFailed=true;exportObsReport()`);
  await settle(2);
  app.evaluate(`setObsProjectScope('')`);
  await settle(6);
  release();
  await settle(6);
  assert.equal(app.evaluate('state.analytics.activity.sessions'), 9);
  assert.equal(app.evaluate('state.obsAnalyticsFull'), false);
});

test('a failed delivery request restores the analytics controls', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/analytics', {status:503,body:{error:'temporarily unavailable'}});
  app.evaluate(`state.analytics={summary:{},trend:[],runTraces:[]};state.obsAnalyticsFull=false;obsEChartsFailed=true;switchObsTab('delivery')`);
  await settle(6);
  assert.doesNotMatch(app.document.querySelector('#obsBody').textContent, /正在加载交付数据/);
  assert.ok(app.document.querySelector('[data-obs-scope]'));
});

test('export still loads full analytics from the usage tab and downloads the current scope', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const downloads = [];
  app.document.addEventListener('click', event => {
    const anchor = event.target.closest('a[download]');
    if (anchor) { event.preventDefault(); downloads.push(anchor.download); }
  });
  app.router.get('/api/analytics', {summary:{},activity:{sessions:2},trend:[],runTraces:[]});
  app.evaluate(`state.currentUser={id:42,isGuest:false};state.obsScope='user';state.obsTab='usage';state.obsUserFilter='42';state.obsProjectScope='unassigned';state.obsAnalyticsFull=false;exportObsReport()`);
  await settle(6);
  assert.equal(app.evaluate('state.obsAnalyticsFull'), true);
  assert.equal(app.evaluate('state.analytics.activity.sessions'), 2);
  assert.equal(downloads.length, 1);
  assert.match(downloads[0], /未归属项目/);
});

test('delivery reloads a full cache when the selected project scope has changed', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  let release;
  const delayed = new Promise(resolve => { release = resolve; });
  app.router.get('/api/analytics', async call => {
    if (call.url.searchParams.get('scope') === 'overview') await delayed;
    return {summary:{},activity:{sessions:9},trend:[],runTraces:[]};
  });
  app.evaluate(`state.currentUser={id:42,isGuest:false};state.obsScope='user';state.obsUserFilter='42';state.obsProjectScope='unassigned';state.obsTab='usage';state.obsAnalyticsFull=true;state.obsAnalyticsQuery=analyticsQuery({overview:false});state.analytics={summary:{},activity:{sessions:2},trend:[],runTraces:[]};obsEChartsFailed=true;setObsProjectScope('')`);
  await settle(2);
  app.evaluate(`switchObsTab('delivery')`);
  await settle(6);
  assert.equal(app.evaluate('state.analytics.activity.sessions'), 9);
  assert.equal(app.evaluate('state.obsAnalyticsQuery'), app.evaluate('analyticsQuery({overview:false})'));
  release();
  await settle(4);
  assert.equal(app.evaluate('state.analytics.activity.sessions'), 9);
});
