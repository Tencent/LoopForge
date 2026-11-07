import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('observability chart options expose meaningful series, labels, tooltips, and statistics', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = app.evaluate(`(() => {
    const theme=OBS_THEME_PRESETS.light;
    const opts={theme,reduced:true,fmtTokens:v=>v+' tok',fmtCost:v=>'¥'+Number(v).toFixed(2),label:p=>p.date};
    const points=[
      {date:'2026-09',tokens:100,inputTokens:60,outputTokens:20,cacheReadTokens:20,cacheWriteTokens:5,costUsd:1,costCny:7,runs:2,issues:1,succeeded:1,failed:1},
      {date:'2026-10',tokens:240,inputTokens:120,outputTokens:60,cacheReadTokens:60,cacheWriteTokens:10,costUsd:2.5,costCny:17.5,runs:4,issues:3,succeeded:3,failed:1}
    ];
    const prev=[{date:'2026-07',tokens:50,costUsd:.5,costCny:3.5,runs:1,issues:1,succeeded:1,failed:0},{date:'2026-08',tokens:80,costUsd:.8,costCny:5.6,runs:2,issues:1,succeeded:1,failed:1}];
    const trend=OBS_TREND_MODES.map(mode=>obsTrendOption(points,mode,'2026-10',prev,opts));
    const breakdown=obsBreakdownOption([{name:'GPT',tokens:75,costUsd:3,costCny:21},{name:'GLM',tokens:25,costUsd:1,costCny:7}],'cost',opts);
    const heat=obsHeatOption([{weekday:1,hour:9,runs:2,tokens:90},{weekday:1,hour:9,runs:1,tokens:10},{weekday:9,hour:30,runs:8}], 'runs', opts);
    const funnel=obsFunnelOption([{name:'需求',value:10},{name:'运行',value:8},{name:'交付',value:3}],{...opts,active:2});
    const owners=obsOwnersOption([{name:'Alice',delivered:3,active:2,undelivered:1,durationLabel:'2h',tokens:400}],opts);
    return {
      trendSeries:trend.map(x=>x.series.length),
      trendTip:trend[0].tooltip.formatter([{dataIndex:1}]),
      costSeries:trend[2].series.map(s=>s.data),
      costTip:trend[2].tooltip.formatter([{dataIndex:1}]),
      costAxis:obsTrendOption(points,'cost','',null).yAxis.axisLabel.formatter(17.5),
      breakdownValues:breakdown.series[0].data.map(p=>p.value),
      breakdownTip:breakdown.tooltip.formatter({name:'GPT',value:21}),
      heatCells:heat.series[0].data.length,heatTip:heat.tooltip.formatter({value:[9,1,3],data:{runs:3,tokens:100}}),
      peak:obsHeatPeak([{weekday:2,hour:11,runs:4,tokens:50},{weekday:3,hour:8,runs:1,tokens:90}],'tokens'),
      funnel:obsFunnelStats([{name:'需求',value:10},{name:'运行',value:8},{name:'交付',value:3}]),
      funnelLabel:funnel.series[0].label.formatter({dataIndex:2}),
      ownerTip:owners.tooltip.formatter([{dataIndex:0}]),
      escaped:obsEsc('<x>'),
      theme:obsEChartsTheme('light').mode
    };
  })()`);
  assert.equal(JSON.stringify(result.trendSeries), JSON.stringify([2, 4, 2, 2, 2, 2]));
  assert.match(result.trendTip, /240 tok|成功率/);
  assert.equal(JSON.stringify(result.costSeries), JSON.stringify([[3.5,5.6],[7,17.5]]));
  assert.match(result.costTip, /¥17\.50/);
  assert.equal(result.costAxis, '¥17.50');
  assert.equal(JSON.stringify(result.breakdownValues), JSON.stringify([21,7]));
  assert.match(result.breakdownTip, /¥21\.00/);
  assert.match(result.breakdownTip, /75\.0%/);
  assert.equal(result.heatCells, 168);
  assert.match(result.heatTip, /周二 09:00|100 tok/);
  assert.equal(JSON.stringify(result.peak), JSON.stringify({ weekday: 3, hour: 8, value: 90 }));
  assert.equal(result.funnel.worst, 2);
  assert.match(result.funnelLabel, /最大流失/);
  assert.match(result.ownerTip, /Alice|400 tok/);
  assert.equal(result.escaped, '&lt;x&gt;');
  assert.equal(result.theme, 'light');
});

test('chart lifecycle mounts a concrete chart, routes events, replays, rebuilds, and disposes it', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    window.__chartLog=[];
    window.IntersectionObserver=undefined;
    const chart={
      disposed:false,
      handlers:{},zr:{handlers:{},on(n,f){this.handlers[n]=f}},
      setOption(o){window.__chartLog.push(['option',o.series[0].data[0]])},
      on(n,f){this.handlers[n]=f},getZr(){return this.zr},
      containPixel(){return true},convertFromPixel(){return [2,0]},
      resize(){window.__chartLog.push(['resize'])},clear(){window.__chartLog.push(['clear'])},
      dispose(){this.disposed=true;window.__chartLog.push(['dispose'])},isDisposed(){return this.disposed}
    };
    window.echarts={init:(el,theme,opt)=>{window.__chartLog.push(['init',opt.renderer]);return chart}};
    const host=document.createElement('div');host.id='chart-host';document.body.append(host);
    host.innerHTML=obsChartSlot('unit',{option:()=>({series:[{data:[7]}]}),on:{click:p=>window.__chartLog.push(['click',p.name])},axisClick:i=>window.__chartLog.push(['axis',i]),mounted:()=>window.__chartLog.push(['mounted'])},180);
    Object.defineProperty(host.firstElementChild,'clientWidth',{value:400,configurable:true});
    obsMountCharts(host);
  `);
  await settle(5);
  app.evaluate(`
    const e=obsChartEntry('unit');e.chart.handlers.click({name:'point'});
    e.chart.zr.handlers.click({offsetX:4,offsetY:5});
    obsReplayChart('unit');obsRebuildCharts();
  `);
  await settle(5);
  assert.equal(app.evaluate(`obsChartCount()`), 1);
  const log = app.evaluate(`window.__chartLog`);
  assert.ok(log.some(x => x[0] === 'init' && x[1] === 'svg'));
  assert.ok(log.some(x => x[0] === 'click' && x[1] === 'point'));
  assert.ok(log.some(x => x[0] === 'axis' && x[1] === 2));
  assert.ok(log.some(x => x[0] === 'clear'));
  app.evaluate(`obsDisposeCharts(document.querySelector('#chart-host'))`);
  assert.equal(app.evaluate(`obsChartCount()`), 0);
});
