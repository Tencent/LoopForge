import test from 'node:test';
import assert from 'node:assert/strict';
import { chooseOption, loadApp, settle } from '../lib/load-app.mjs';

function snapshot(user='') {
  const count=user==='7'?1:user==='8'?2:3;
  return {
    dashboard:{sopRuns:count,succeededRuns:count,failedRuns:0,humanRuns:1,deliveredIssues:count,
      trend:[{date:'2026-11',sopRuns:count,succeededRuns:count,humanRuns:1,sopDelivered:count}],humanNodes:[{key:'flow/review',name:'Review'},{key:'flow/test',name:'Test'}],
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
const activityText=app=>app.document.querySelector('[data-obs-card="vibe"]')?.textContent||'';
const stat=(app,key)=>app.document.querySelector(`[data-obs-stat="${key}"] .num`)?.textContent;

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
  assert.deepEqual([...app.document.querySelectorAll('#obsBody [data-obs-card]')].map(el=>el.dataset.obsCard),['summary','vibe','human','cost']);
  assert.equal(app.document.querySelectorAll('#obsBody .obs-kpi').length,9);
  assert.equal(app.document.querySelectorAll('#obsBody .obs-kpi-foot, #obsBody .obs-info, #obsBody .obs-card-heading p').length,0);
  assert.equal(stat(app,'delivered'),'1');assert.equal(stat(app,'sop-success'),'100%');
  const project=app.document.querySelector('[data-obs-scope="project"]');project.click();await settle(8);
  assert.equal(app.evaluate('state.analytics.summary.totalCostCny'),21);
  assert.equal(app.evaluate('state.analytics.activity.sessions'),3);
  assert.equal(stat(app,'delivered'),'3');assert.equal(stat(app,'human-rate'),'33%');
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
  assert.equal(stat(app,'sessions'),'2');assert.equal(stat(app,'cost'),'¥14.00');
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

test('overview ratios use the selected period cohort and remain independent of the selected human node', async t => {
  const app=await loadApp();t.after(app.close);prepare(app);
  const data=snapshot('7');
  data.dashboard={...data.dashboard,sopRuns:10,succeededRuns:7,failedRuns:3,humanRuns:6,deliveredIssues:8,
    trend:[{date:'2026-10',sopRuns:8,succeededRuns:7,failedRuns:1,humanRuns:6,sopDelivered:7},
      {date:'2026-11',sopRuns:2,succeededRuns:0,failedRuns:2,humanRuns:0,vibeDelivered:1}]};
  app.evaluate(`state.analytics=${JSON.stringify(data)};state.obsTrendGrain='month';state.obsPeriodKey='2026-11';renderObservability()`);
  assert.equal(stat(app,'delivered'),'1');assert.equal(stat(app,'sop-success'),'0%');assert.equal(stat(app,'human-rate'),'0%');
  chooseOption(app.document.querySelector('#obsHumanNode'),'flow/review');
  assert.equal(stat(app,'human-rate'),'0%');
  app.evaluate(`state.obsPeriodKey='2026-10';renderObservability()`);
  assert.equal(stat(app,'delivered'),'7');assert.equal(stat(app,'sop-success'),'88%');assert.equal(stat(app,'human-rate'),'75%');
  app.evaluate(`state.obsPeriodKey='2026-12';renderObservability()`);
  assert.equal(stat(app,'delivered'),'0');assert.equal(stat(app,'sop-success'),'—');assert.equal(stat(app,'human-rate'),'—');
});

test('partial query failures are surfaced instead of showing zero metrics, and real empty data stays compact', async t => {
  const app=await loadApp();t.after(app.close);prepare(app);
  app.router.get(/^\/api\/analytics\?/,()=>({...snapshot(),dataErrors:['delivery read failed']}));
  await app.evaluate('refreshObs()');
  assert.match(app.document.querySelector('#obsBody [role="alert"]').textContent,/加载失败/);
  assert.match(app.document.querySelector('#obsLiveState').textContent,/加载失败/);
  assert.equal(app.document.querySelector('#obsBody [data-obs-stat]'),null);
  assert.equal(app.document.querySelector('#exportObsReport').disabled,true);
  const empty={dashboard:{sopRuns:0,deliveredIssues:0,trend:[],humanNodes:[],humanTrend:[]},summary:{},activity:{},trend:[],activityTrend:[],runTraces:[]};
  app.router.get(/^\/api\/analytics\?/,empty);await app.evaluate('refreshObs()');
  assert.equal(app.document.querySelector('#obsBody [role="alert"]'),null);
  assert.equal(stat(app,'sessions'),'0');assert.equal(stat(app,'sop-success'),'—');assert.equal(stat(app,'human-rate'),'—');
  assert.equal(app.document.querySelectorAll('#obsBody [data-obs-chart][data-empty="1"]').length,3);
  assert.doesNotMatch(app.document.querySelector('#obsBody').textContent,/NaN|Infinity|输入 0 \/ 输出 0/);
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
