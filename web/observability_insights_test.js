'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const ctx = {
  state: {obsScope:'project',obsTrendGrain:'all',obsOriginFilter:'external',data:{users:[]}},
  projectQuery:()=>'?projectId=p',currentProject:()=>({id:'p',ownerId:7,members:[{userId:8,role:'maintainer'}]}),
};
vm.createContext(ctx);
vm.runInContext(fs.readFileSync('app/observability.js','utf8'),ctx);
assert.match(ctx.analyticsQuery({overview:true}),/origin=external/);
assert.match(ctx.analyticsQuery({overview:true}),/projectId=p/);
assert.doesNotMatch(ctx.analyticsQuery({crossProject:true}),/projectId=/);
ctx.state.currentUser={id:7};assert.equal(ctx.obsCanManageSources(),true);
ctx.state.currentUser={id:8};assert.equal(ctx.obsCanManageSources(),true);
ctx.state.currentUser={id:9};assert.equal(ctx.obsCanManageSources(),false);
ctx.state.analytics={userUsage:[
  {userId:'7',runs:1,tokens:100,costKnown:true,costCny:0.5},
  {userId:'7',runs:1,tokens:20,costKnown:false,costCny:0},
]};
assert.equal(ctx.obsUserTotals()[0].costKnown,false);
assert.equal(ctx.obsUserTotals()[0].tokens,120);
assert.match(ctx.costLabel(0,false),/未识别/);
assert.equal(ctx.costLabel(0,true),'¥0.00');
assert.equal(ctx.costLabel(.007,true),'¥0.00700');
assert.equal(ctx.obsFormatTrendValue(12.5,'cost'),'¥12.50');
assert.match(ctx.costLabel(0.5,false),/部分未识别/);
ctx.state.obsTrendGrain='day';ctx.state.obsPeriodKey='2026-10-08';
ctx.state.analytics={runTraces:[
  {origin:'external',status:'succeeded',tokens:100,durationMs:5000,costKnown:false,finishedAt:'2026-10-08T01:00:00Z'},
  {status:'succeeded',tokens:20,durationMs:1000,humanInterventions:1,finishedAt:'2026-10-08T01:00:00Z'},
]};
const scoped=ctx.obsScopedView();
assert.equal(scoped.total,2);assert.equal(scoped.tokens,120);assert.equal(scoped.succeeded,2);
assert.equal(scoped.demo.coveredRuns,1);assert.equal(scoped.demo.coveredSucceededRuns,1);
assert.equal(scoped.demo.automatedSucceededRuns,0);assert.equal(scoped.demo.averageHumanInterventions,1);
assert.equal(scoped.p95,1000,'unknown external active duration must not become wall-clock duration');
ctx.state.analytics.runTraces[0].executionDurationMs=2000;
assert.equal(ctx.obsScopedView().p95,2000,'reported active duration should participate in period statistics');
console.log('external usage frontend checks passed');
// Conversation metrics have their own creation-time buckets, including periods
// with no Run/token usage, and remain available on the lightweight overview.
ctx.escapeHTML=x=>String(x);
ctx.state.obsTrendGrain='all';
ctx.state.analytics={activity:{sessions:3,collaborativeSessions:2,requirements:4,sopRuns:5},activityTrend:[{date:'2026-10-08',sessions:2,collaborativeSessions:1,requirements:1,sopRuns:0}]};
assert.equal(ctx.obsActivityView().sessions,3);
assert.match(ctx.obsActivityKpis(),/66\.7%/);
assert.match(ctx.obsActivityKpis(),/需求 SOP/);
ctx.state.obsTrendGrain='day';ctx.state.obsPeriodKey='2026-10-08';
assert.equal(ctx.obsActivityView().sessions,2);
assert.match(ctx.obsActivityKpis(),/50\.0%/);
ctx.state.obsPeriodKey='2026-10-09';
assert.equal(ctx.obsActivityView().sessions,0);
assert.match(ctx.obsActivityKpis(),/暂无会话/);
assert.doesNotMatch(ctx.obsActivityKpis(),/NaN|Infinity/);
ctx.state.obsTrendGrain='month';ctx.state.obsPeriodKey='2026-10';
ctx.state.analytics.activityTrend=[{date:'2026-10',sessions:3,collaborativeSessions:2,requirements:4,sopRuns:5}];
assert.equal(ctx.obsActivityView().sopRuns,5);
ctx.state.analytics={};
assert.match(ctx.obsActivityKpis(),/暂无统计数据/);
ctx.state.obsTrendGrain='day';ctx.state.obsPeriodKey='';
ctx.state.analytics={activity:{sessions:1},trend:[],activityTrend:[{date:'2026-10-08',sessions:1}]};
assert.equal(ctx.obsPeriodKey(),'2026-10-08');
assert.ok(ctx.obsPeriodChoices().includes('2026-10-08'));
ctx.formatTokens=x=>String(x);
const usage={sessionUsage:{tokens:123,reportedReplies:1,missingReplies:2},collaborativeUsage:{tokens:50,reportedReplies:1}};
assert.equal(ctx.obsActivityUsageCard(usage,false).value,'123');
assert.equal(ctx.obsActivityUsageCard(usage,true).value,'50');
assert.match(ctx.obsActivityUsageCard(usage,false).foot,/2 条回复未采集/);
assert.equal(ctx.obsActivityUsageCard({sessionUsage:{missingReplies:1}},false).value,'未采集');
assert.equal(ctx.obsActivityUsageCard({sessionUsage:{}},false).value,'0');
assert.equal(ctx.obsActivityUsageCard(null,false).value,'—');
