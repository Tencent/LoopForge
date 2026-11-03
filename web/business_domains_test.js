'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const ctx = {
  state:{data:{businessDomains:[]}},
  escapeHTML:value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])),
  formatTokens:value=>String(value||0),duration:value=>`${value}ms`,
};
vm.createContext(ctx);
const path = require('node:path');
vm.runInContext(fs.readFileSync(path.join(__dirname,'app/obs-charts.js'),'utf8'),ctx);
vm.runInContext(fs.readFileSync(path.join(__dirname,'app/business-domains.js'),'utf8'),ctx);
const run = code=>vm.runInContext(code,ctx);
assert.equal(ctx.businessCost(0,false),'未识别');
assert.equal(ctx.businessCost(0,true),'¥0.00');
assert.match(ctx.businessCost(1,false),/部分未识别/);
assert.equal(ctx.businessCycleLabel({cycleSamples:0,averageCycleDurationMs:100}),'待采集');
assert.equal(ctx.businessCycleLabel({cycleSamples:1,averageCycleDurationMs:0}),'0s');
run("businessState.from='2026-10-01';businessState.to='2026-10-03';businessState.report={grain:'day'}");
const points=ctx.businessChartPoints([{date:'2026-10-01',completedIssues:1,cycleSamples:1,averageCycleDurationMs:100,executionSamples:1,averageExecutionDurationMs:20},{date:'2026-10-03',completedIssues:2}]);
assert.equal(points.length,3);
assert.equal(points[1].completedIssues,0,'missing throughput days must be zero filled');
assert.equal(points[1].cycleSamples,0,'missing cycle samples must remain unknown');
const chart=ctx.businessDeliveryChart(points);
assert.match(chart,/需求完成与 MR 合入趋势/);
assert.doesNotMatch(chart,/NaN|Infinity/);
assert.match(ctx.businessDeliveryChart([],true),/暂无可计算/);
const durationFallback=ctx.businessDeliveryChart(points,true);
assert.equal((durationFallback.match(/class="business-cycle-line"/g)||[]).length,2,'static export retains both duration series');
assert.match(durationFallback,/不含等待 20ms/);
assert.match(durationFallback,/含等待 100ms/);
assert.doesNotMatch(durationFallback,/NaN|Infinity/);
run("businessState.from='2025-12-29';businessState.to='2026-01-11';businessState.report={grain:'week'}");
const weeks=ctx.businessChartPoints([{date:'2026-W01',completedIssues:2},{date:'2026-W02',mergedMrs:3}]);
assert.equal(weeks.length,2,'ISO week years must align with the selected dates');
assert.equal(weeks[0].completedIssues,2);
assert.equal(weeks[1].mergedMrs,3);
assert.doesNotMatch(ctx.businessDeliveryChart(weeks),/NaN|Infinity/);
run("businessState.from='';businessState.to='';businessState.report={grain:'month'}");
assert.equal(ctx.businessGrain(),'month');
assert.equal(ctx.businessChartPoints([{date:'2025-12',mergedMrs:1},{date:'2026-02',mergedMrs:1}]).length,3);
ctx.fixture={
  summary:{uniqueIssues:10,totalRuns:12,totalTokens:300,succeededRuns:9,failedRuns:1,successRate:90,costCoveragePct:0},
  delivery:{delivered:999},
  deliveryMetrics:{completedIssues:4,mergedMrs:7,cycleSamples:3,averageCycleDurationMs:100,executionSamples:2,averageExecutionDurationMs:40,averageTokensPerCompletedIssue:50,estimatedCompletionIssues:1,completionTimeMissingIssues:2,mergedMrsMissingTime:3,completionToMergeSamples:2,averageCompletionToMergeMs:4200000,completionToMergeMissingMrs:1,mergedBeforeCompletionMrs:1},
  projects:[{projectId:'p1',name:'<script>alert(1)</script>',tokens:100,deliveryMetrics:{completedIssues:3,cycleSamples:0}},{projectId:'p2',name:'B',tokens:200,deliveryMetrics:{completedIssues:1,cycleSamples:1,averageCycleDurationMs:0}}],
  businessDomains:[{id:'a',name:'A',active:true,projectCount:2,uniqueIssues:10,tokens:300,deliveryMetrics:{completedIssues:4}}],
  deliveryTrend:[{date:'2026-10-01',completedIssues:4,mergedMrs:7,cycleSamples:3,averageCycleDurationMs:100}],
  trend:[],grain:'day',timeZone:'Asia/Singapore',generatedAt:'2026-10-30T00:00:00Z',
};
run("businessState.from='2026-10-01';businessState.to='2026-10-03';businessState.report=fixture;businessState.scope='all'");
const html=ctx.businessReportHTML();
assert.match(html,/MR 合入数量/);
assert.match(html,/最近完成时间/);
assert.match(html,/平均完成耗时/);
assert.match(html,/平均完成耗时（含等待）/);
assert.match(html,/平均完成后合入耗时/);
assert.match(html,/2 个有效 MR 样本/);
assert.match(html,/1 个 MR 缺少可配对的完成记录/);
assert.match(html,/1 个 MR 在流程完成前已合入/);
assert.match(html,/一个需求有多个 MR 时分别计算/);
assert.match(html,/成本（人民币）/);
assert.doesNotMatch(html,/\$\d/);
assert.equal(ctx.businessCost(.007,true),'¥0.00700');
assert.equal(ctx.businessCycleLabel({completionToMergeSamples:1,averageCompletionToMergeMs:0},'averageCompletionToMergeMs'),'0s');
assert.equal(ctx.businessCycleLabel({completionToMergeSamples:0,averageCompletionToMergeMs:120},'averageCompletionToMergeMs'),'待采集');
assert.doesNotMatch(html,/P95|平均完成耗时（不含等待）|平均耗时（不含等待）/);
assert.equal(ctx.businessCycleLabel({executionSamples:0,averageExecutionDurationMs:100},'averageExecutionDurationMs'),'待采集');
assert.equal(ctx.businessCycleLabel({executionSamples:1,averageExecutionDurationMs:0},'averageExecutionDurationMs'),'0s');
assert.doesNotMatch(html,/未完成需求|<th[^>]*>未完成/);
assert.match(html,/data-business-scope="a"/);
assert.match(html,/历史完成项缺少完成记录/);
assert.match(html,/3 个已合入 MR 缺少合入时间/);
assert.doesNotMatch(html,/<script>|999|NaN|Infinity/);
assert.match(html,/&lt;script&gt;/);
assert.equal(ctx.businessSortItems(ctx.fixture.projects)[0].projectId,'p1');
run("businessState.sort='tokens';businessState.scope='a'");
assert.equal(ctx.businessSortItems(ctx.fixture.projects)[0].projectId,'p2');
const single=ctx.businessReportHTML();
assert.doesNotMatch(single,/业务交付对比|<th scope="col">主业务域/);
assert.match(single,/项目用量分布/);

// ECharts options: pure, finite, cycle gaps stay null, keys never collide with the statistics page.
const echarts = require('./vendor/echarts/echarts.min.js');
const finite = values=>values.every(v=>v===null||Number.isFinite(v));
const ssr = option=>{const chart=echarts.init(null,null,{renderer:'svg',ssr:true,width:480,height:220});try{chart.setOption({...option,animation:false});return chart.renderToSVGString()}finally{chart.dispose()}};
const tooltipAt = (option,i)=>option.tooltip.formatter([{dataIndex:i}]);
for (const set of [[],[points[0]],points,weeks]) {
  for (const build of ['businessDeliveryOption','businessCycleOption','businessRunsOption']) {
    const option=ctx[build](set);
    assert.equal(option.xAxis.data.length,set.length,`${build} labels follow the points`);
    option.series.forEach(s=>{assert.equal(s.data.length,set.length);assert.ok(finite(s.data),`${build} series must not contain NaN`)});
    assert.doesNotMatch(ssr(option),/NaN|Infinity/,`${build} renders to static SVG`);
  }
}
const delivery=ctx.businessDeliveryOption(points);
assert.deepEqual([...delivery.series.map(s=>s.name)],['完成需求','合入 MR']);
assert.deepEqual([...delivery.series[0].data],[1,0,2]);
const cycle=ctx.businessCycleOption(points);
assert.deepEqual([...cycle.series.map(s=>s.name)],['不含等待','含等待']);
assert.deepEqual([...cycle.series[0].data],[20,null,null],'missing active timing is unknown, not zero');
assert.deepEqual([...cycle.series[1].data],[100,null,null],'days without cycle samples must break the line');
assert.deepEqual([...ctx.businessCycleOption([{executionSamples:1,averageExecutionDurationMs:0,cycleSamples:0}]).series[0].data],[0]);
assert.equal(cycle.series[0].connectNulls,false);
assert.match(tooltipAt(cycle,1),/—[\s\S]*0 个样本/);
const differentSamples=tooltipAt(ctx.businessCycleOption([{date:'2026-10-01',executionSamples:2,averageExecutionDurationMs:20,cycleSamples:3,averageCycleDurationMs:100}]),0);
assert.match(differentSamples,/不含等待[\s\S]*2 个样本[\s\S]*含等待[\s\S]*3 个样本/,'duration averages show their own sample coverage');
assert.match(ctx.businessCycleOption(points,{fmtDuration:v=>`${v} 毫秒`}).tooltip.formatter([{dataIndex:0}]),/100 毫秒/);
assert.equal(ctx.businessDurationAxis(2*86400000),'2.0d');
assert.equal(ctx.businessDurationAxis(0),'0');
const hostile=[{date:'<img src=x>',label:'<img src=x>',completedIssues:1,mergedMrs:0,runs:1,cycleSamples:0}];
for (const build of ['businessDeliveryOption','businessCycleOption','businessRunsOption']) {
  const tip=tooltipAt(ctx[build](hostile),0);
  assert.doesNotMatch(tip,/<img/);assert.match(tip,/&lt;img src=x&gt;/);
}
assert.equal(ctx.businessRunsOption(points,{reduced:true}).animation,false,'reduced motion turns chart animation off');

// Report template: grouped KPI card, compact empty charts, biz- chart keys.
run("businessState.scope='all';businessState.sort='completed'");
const grouped=ctx.businessReportHTML();
assert.match(grouped,/class="obs-kg biz-kg"/);
assert.match(grouped,/class="biz-kg-sub"[\s\S]*关联需求<b>10<\/b>/);
assert.equal((grouped.match(/class="obs-kpi"/g)||[]).length,6);
assert.doesNotMatch(grouped,/eyebrow|business-kpis|business-report-meta/);
const chartKeys=[...grouped.matchAll(/data-obs-chart="([^"]+)"/g)].map(m=>m[1]);
assert.deepEqual(chartKeys,['biz-delivery','biz-cycle','biz-runs']);
assert.match(grouped,/data-obs-chart="biz-runs"[^>]*data-empty="1"[^>]*>[\s\S]*?所选时间段暂无运行数据/);
assert.match(grouped,/data-obs-chart="biz-runs" style="height:110px"/,'empty charts collapse to a compact state');
assert.match(grouped,/data-obs-chart="biz-delivery" style="height:220px"/);
const deliverySpec=run("obsChartSpecs['biz-delivery']");
assert.equal(typeof deliverySpec.fallback,'function');
assert.match(deliverySpec.fallback(),/business-trend-svg/);
assert.doesNotMatch(deliverySpec.option(run('OBS_THEME_PRESETS.dark'),false).series[0].data.join(','),/NaN/);
assert.match(grouped,/biz-notice[\s\S]*历史完成项缺少完成记录/);
assert.match(grouped,/<details class="biz-fold" data-biz-fold="resources">/);
assert.match(grouped,/data-icon="chevron-right"/);

ctx.pending={...ctx.fixture,deliveryMetrics:{completedIssues:0,mergedMrs:0,cycleSamples:0},deliveryTrend:[],trend:[]};
run("businessState.report=pending");
const pending=ctx.businessReportHTML();
assert.equal((pending.match(/<b class="num is-na">—<\/b>/g)||[]).length,4,'cycles, merge and Token per issue show — without samples');
assert.equal((pending.match(/待采集 · 0 个样本/g)||[]).length,3);
assert.doesNotMatch(pending,/<b class="num[^"]*">待采集/);
assert.match(pending,/<td>待采集<small>0 个样本<\/small><\/td>/,'tables keep the 待采集 label');
assert.match(pending,/data-obs-chart="biz-delivery" style="height:110px" data-empty="1"><div class="obs-chart-empty">所选时间段暂无需求完成或 MR 合入/);
assert.match(pending,/暂无可计算完成耗时的样本/);

const index=fs.readFileSync(path.join(__dirname,'index.html'),'utf8');
const heading=/<section id="businessDomainsView"[\s\S]*?<div id="businessDomainsBody">/.exec(index)[0];
assert.doesNotMatch(heading,/eyebrow|button primary/);
assert.match(heading,/id="businessExport" class="button ghost"[^>]*disabled><svg[^>]*data-icon="download"/);
assert.match(heading,/id="businessRefresh" class="button ghost"[^>]*><svg[^>]*data-icon="refresh"/);
const sheets=[...index.matchAll(/<link rel="stylesheet" href="([^"?]+)/g)].map(m=>m[1]);
assert.ok(sheets.indexOf('/app/business-harness.css')>sheets.indexOf('/app/observability-harness.css'),'business-harness.css loads after the statistics sheet');
const harnessCSS=fs.readFileSync(path.join(__dirname,'app/business-harness.css'),'utf8').replace(/\/\*[\s\S]*?\*\//g,'');
for (const rule of harnessCSS.split('}')) {
  const selector=rule.split('{').slice(-2,-1)[0]?.trim();
  if(!selector||selector.startsWith('@')||/^(from|to|\d+%)/.test(selector))continue;
  let depth=0;const parts=[''];
  for (const c of selector) {if(c==='('||c==='[')depth++;if(c===')'||c===']')depth--;if(c===','&&!depth)parts.push('');else parts[parts.length-1]+=c}
  for (const part of parts) assert.match(part,/#businessDomainsView/,`business-harness.css rule ${part.trim()} escapes the report scope`);
}
assert.match(fs.readFileSync(path.join(__dirname,'app/harness-ui.css'),'utf8'),/html\[data-theme="dark"\] \{/);
console.log('business delivery frontend checks passed');
