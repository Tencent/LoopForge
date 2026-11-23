import test from 'node:test';
import assert from 'node:assert/strict';
import { chooseOption, loadApp, settle } from '../lib/load-app.mjs';

function snapshot(user='') {
  const count=user==='7'?1:user==='8'?2:3;
  return {
    dashboard:{sopRuns:count,succeededRuns:count,failedRuns:0,humanRuns:1,deliveredIssues:count,
      trend:[{date:'2026-11',sopRuns:count}],humanNodes:[{key:'flow/review',name:'Review'},{key:'flow/test',name:'Test'}],
      humanTrend:[{date:'2026-11',node:'flow/review',approvals:count,rejections:0,retries:1},{date:'2026-11',node:'flow/test',approvals:1,rejections:1,retries:0}]},
    summary:{totalRuns:count,succeededRuns:count,failedRuns:0,qualityRuns:count,successRate:100,totalTokens:count*1000,totalCostCny:count*7,conversationWorkspaces:count,conversationTokens:count*100},
    activity:{sessions:count,collaborativeSessions:1,requirements:count,sopRuns:count,sessionUsage:{tokens:count*200,reportedReplies:count},collaborativeUsage:{tokens:100,reportedReplies:1}},
    activityTrend:[{date:'2026-11',sessions:count,collaborativeSessions:1}],
    demo:{coveredRuns:count,humanInterventions:count,averageHumanInterventions:1},
    trend:[{date:'2026-11',runs:count,tokens:count*1000,costCny:count*7}],
    runTraces:[{runId:'run-'+(user||'all'),createdAt:'2026-11-01T00:00:00Z',status:'succeeded',tokens:count*1000,costCny:count*7}],
    delivery:{issues:count,delivered:count},deliveries:[],models:[],workflows:[],nodes:[],failures:[],userUsage:[],projectUsage:[],weekHour:[],
  };
}
function prepare(app) {
  app.evaluate(`
    state.currentUser={id:7,loginName:'alice',isGuest:false,isPlatformAdmin:true};
    state.data.users=[state.currentUser,{id:8,loginName:'bob'}];
    state.data.projects[0].ownerId=7;
    state.obsScope='user';state.obsUserFilter='';state.obsUserFilterReady=false;
    state.obsTrendGrain='all';state.obsTab='overview';
  `);
}
const activityText=app=>[...app.document.querySelectorAll('.obs-kg')].find(el=>el.querySelector('h4')?.textContent.includes('Vibe Coding'))?.textContent||'';

test('grouped design keeps personal/project and owner filters, with all metrics scoped together', async t => {
  const app=await loadApp();t.after(app.close);prepare(app);
  app.router.get(/^\/api\/analytics\?/,call=>snapshot(call.url.searchParams.get('userId')||''));
  await app.evaluate('refreshObs()');await settle(3);
  assert.deepEqual([...app.document.querySelectorAll('[data-obs-tab]')].map(b=>b.dataset.obsTab),['overview','delivery']);
  assert.ok(app.document.querySelector('.obs-fbar'));assert.ok(app.document.querySelector('[data-obs-scope="user"].active'));
  assert.equal(app.document.querySelector('#obsUserFilter').value,'7');
  assert.equal(app.evaluate('state.analytics.summary.totalCostCny'),7);
  assert.match(activityText(app),/Vibe Coding|会话数/);
  assert.ok(app.document.querySelector('[data-obs-card="human"]'));
  const project=app.document.querySelector('[data-obs-scope="project"]');project.click();await settle(8);
  assert.equal(app.evaluate('state.analytics.summary.totalCostCny'),21);
  assert.equal(app.evaluate('state.analytics.activity.sessions'),3);
  assert.equal(app.document.querySelector('#obsUserFilter'),null);
  assert.ok(app.document.querySelector('[data-obs-scope="project"].active'));
  assert.match(app.document.querySelector('.obs-scopeline').textContent,/全部成员/);
  let call=app.router.calls.filter(c=>c.path==='/api/analytics'&&c.url.searchParams.has('projectId')).at(-1);
  assert.equal(call.url.searchParams.has('userId'),false);
  assert.equal(call.url.searchParams.get('dashboard'),'1');assert.equal(call.url.searchParams.get('scope'),'overview');
  await app.evaluate(`setObsScope('user')`);await settle(3);
  const owner=app.document.querySelector('#obsUserFilter');assert.equal(owner.value,'7');
  chooseOption(owner,'8');await settle(8);
  assert.equal(app.evaluate('state.analytics.summary.totalCostCny'),14);
  assert.equal(app.evaluate('state.analytics.activity.sessions'),2);
  call=app.router.calls.filter(c=>c.path==='/api/analytics'&&c.url.searchParams.has('projectId')).at(-1);
  assert.equal(call.url.searchParams.get('userId'),'8');
  assert.match(app.document.querySelector('.obs-scopeline').textContent,/bob/);
  await app.evaluate(`setObsScope('project')`);await app.evaluate(`setObsScope('user')`);
  assert.equal(app.document.querySelector('#obsUserFilter').value,'8','restore the selected owner when returning to personal scope');
  const card=app.document.querySelector('[data-obs-card="human"]'),node=card.querySelector('#obsHumanNode');
  chooseOption(node,'flow/review');
  assert.equal(app.document.querySelector('[data-obs-card="human"]'),card,'node filtering preserves the chart container');
  assert.equal(app.evaluate('obsHumanPoints()[0].approvals'),2);
  card.querySelector('[data-obs-human-metric="count"]').click();
  assert.equal(app.evaluate('state.obsDashboardMetric'),'count');
  assert.equal(card.querySelector('[data-obs-human-metric="count"]').getAttribute('aria-pressed'),'true');
  const options=app.evaluate(`(() => {const points=obsHumanPoints(),theme=OBS_THEME_PRESETS.light;const raw=obsDashboardOption('human',points,{theme,metric:'count',reduced:true}),rate=obsDashboardOption('human',points,{theme});return {raw:raw.series[0].data[0],rate:rate.series[0].data[0],still:raw.animation}})()`);
  assert.equal(options.raw,2);assert.equal(options.rate,100);assert.equal(options.still,false);
  assert.doesNotMatch(app.document.querySelector('#obsBody').textContent,/NaN|Infinity/);
});

test('late personal results cannot overwrite project metrics or a newly selected owner', async t => {
  const app=await loadApp();t.after(app.close);prepare(app);
  let finishOld;
  app.router.get(/^\/api\/analytics\?/,call=>{
    if(!call.url.searchParams.has('projectId'))return snapshot('7');
    if(call.url.searchParams.get('userId')==='7')return new Promise(resolve=>{finishOld=resolve});
    return snapshot(call.url.searchParams.get('userId')||'');
  });
  app.evaluate('refreshObs()');await settle(3);assert.ok(finishOld);
  await app.evaluate(`setObsScope('project')`);finishOld(snapshot('7'));await settle(5);
  assert.equal(app.evaluate('state.analytics.summary.totalCostCny'),21);
  assert.equal(app.evaluate('state.obsScope'),'project');
  assert.equal(app.document.querySelector('#obsBody').getAttribute('aria-busy'),'false');
  app.router.get(/^\/api\/analytics\?/,call=>{
    if(!call.url.searchParams.has('projectId'))return snapshot('8');
    if(call.url.searchParams.get('userId')==='7')return new Promise(resolve=>{finishOld=resolve});
    return snapshot(call.url.searchParams.get('userId')||'');
  });
  app.evaluate(`setObsScope('user')`);await settle(2);
  app.evaluate(`state.obsUserFilter='8';applyObsFilter()`);await settle(5);
  finishOld(snapshot('7'));await settle(4);
  assert.equal(app.evaluate('state.analytics.summary.totalCostCny'),14);
  assert.equal(app.document.querySelector('#obsUserFilter').value,'8');
  assert.match(app.evaluate('buildObsReportHTML()'),/bob/);
});

test('opening delivery while a scope change loads requests the full data for the new scope', async t => {
  const app=await loadApp();t.after(app.close);prepare(app);
  app.router.get(/^\/api\/analytics\?/,call=>snapshot(call.url.searchParams.get('userId')||''));
  await app.evaluate(`switchObsTab('delivery')`);
  assert.equal(app.evaluate('state.obsAnalyticsFull'),true);
  await app.evaluate(`switchObsTab('overview')`);
  let finishOverview;
  app.router.get(/^\/api\/analytics\?/,call=>{
    if(call.url.searchParams.has('projectId')&&!call.url.searchParams.has('userId')&&call.url.searchParams.get('scope')==='overview'){
      return new Promise(resolve=>{finishOverview=resolve});
    }
    return snapshot(call.url.searchParams.get('userId')||'');
  });
  app.evaluate(`setObsScope('project')`);await settle(3);assert.ok(finishOverview);
  await app.evaluate(`switchObsTab('delivery')`);finishOverview(snapshot());await settle(4);
  assert.equal(app.evaluate('state.obsAnalyticsFull'),true);
  assert.equal(app.evaluate('state.analytics.delivery.issues'),3);
  assert.ok(app.document.querySelector('[data-obs-card="deliveries"]'),'delivery details render for the new project scope');
});
