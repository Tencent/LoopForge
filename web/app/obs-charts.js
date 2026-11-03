// ================= 统计图表层（ECharts） =================
// option 构造函数均为纯函数：只读入参，不访问 DOM 或 echarts 全局，便于 node 测试。
const OBS_ECHARTS_VERSION='6.1.0';
const OBS_ECHARTS_SRC='/vendor/echarts/echarts.min.js?v='+OBS_ECHARTS_VERSION;
const OBS_ECHARTS_TIMEOUT_MS=8000;
const OBS_THEME_PRESETS={
  dark:{c1:'#5b8cff',c2:'#3d5fae',c3:'#2a3d66',c4:'#4b5568',text:'#e8edf6',muted:'#94a3bd',muted2:'#5b6b85',line:'rgba(148,163,184,.13)',lineStrong:'rgba(148,163,184,.25)',panel:'#131a29',panel2:'#19223a',panel3:'#222d48',red:'#f87171',green:'#34d399',amber:'#fbbf24'},
  light:{c1:'#4f7cf0',c2:'#8fb0f7',c3:'#c8d7fb',c4:'#c4cad6',text:'#0f172a',muted:'#475569',muted2:'#94a3b8',line:'rgba(30,41,59,.10)',lineStrong:'rgba(30,41,59,.18)',panel:'#ffffff',panel2:'#f3f5fa',panel3:'#e8ecf4',red:'#c63c4b',green:'#059669',amber:'#b45309'},
};
const OBS_THEME_VARS={c1:'--c1',c2:'--c2',c3:'--c3',c4:'--c4',text:'--text',muted:'--muted',muted2:'--muted-2',line:'--line',lineStrong:'--line-strong',panel:'--panel',panel2:'--panel-2',panel3:'--panel-3',red:'--red',green:'--green',amber:'--amber'};
const OBS_WEEKDAYS=['周一','周二','周三','周四','周五','周六','周日'];
let obsEChartsPromise=null;
// 加载失败只在下一次整页挂载（重渲染/再次进入统计页）时重试，同一次渲染内的多张图共用失败结果。
let obsEChartsFailed=false;
const obsCharts=new Map();
let obsChartSpecs={};
let obsChartIO=null,obsChartRO=null,obsThemeMO=null;
let obsVisibleCallbacks=new WeakMap();
const obsChartWaiting=new Set();

function obsEsc(v){return String(v??'').replace(/[&<>'"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]))}
function obsReducedMotion(){return typeof matchMedia==='function'&&matchMedia('(prefers-reduced-motion: reduce)').matches}
function obsEnsureECharts(){
  if(typeof window!=='undefined'&&window.echarts&&typeof window.echarts.init==='function')return Promise.resolve(window.echarts);
  if(obsEChartsPromise)return obsEChartsPromise;
  obsEChartsPromise=new Promise(resolve=>{
    if(typeof document==='undefined'){resolve(null);return}
    let done=false,timer=0;
    const finish=value=>{if(done)return;done=true;clearTimeout(timer);resolve(value)};
    const script=document.createElement('script');
    script.src=OBS_ECHARTS_SRC;
    script.async=true;
    script.dataset.obsEcharts=OBS_ECHARTS_VERSION;
    script.onload=()=>{const ec=window.echarts&&typeof window.echarts.init==='function'?window.echarts:null;if(!ec)obsEChartsFailed=true;finish(ec)};
    script.onerror=()=>{obsEChartsFailed=true;script.remove();finish(null)};
    timer=setTimeout(()=>{obsEChartsFailed=true;finish(null)},OBS_ECHARTS_TIMEOUT_MS);
    document.head.appendChild(script);
  });
  return obsEChartsPromise;
}
function obsEChartsTheme(mode){
  const doc=typeof document!=='undefined'?document.documentElement:null;
  mode=mode||(doc&&doc.dataset&&doc.dataset.theme==='dark'?'dark':'light');
  const theme={...(OBS_THEME_PRESETS[mode]||OBS_THEME_PRESETS.light),mode};
  if(doc&&typeof getComputedStyle==='function'){
    const css=getComputedStyle(doc);
    for(const [key,name] of Object.entries(OBS_THEME_VARS)){
      const value=String(css.getPropertyValue(name)||'').trim();
      if(value)theme[key]=value;
    }
  }
  return theme;
}

// ---------- option 公共片段 ----------
function obsChartOpts(opts){
  opts=opts||{};
  return {theme:opts.theme||OBS_THEME_PRESETS.light,reduced:!!opts.reduced,fmtTokens:opts.fmtTokens||(v=>String(Math.round(Number(v)||0))),fmtCost:opts.fmtCost||(v=>'¥'+Number(v||0).toFixed(2)),fmtDuration:opts.fmtDuration||(v=>Math.round((Number(v)||0)/1000)+'s'),label:opts.label||(p=>String(p?.label||p?.date||''))};
}
function obsChartBase(o,extra){
  const t=o.theme;
  return Object.assign({
    backgroundColor:'transparent',
    textStyle:{color:t.muted,fontSize:11},
    aria:{enabled:true},
    animation:!o.reduced,
    animationDuration:o.reduced?0:800,
    animationEasing:'cubicOut',
    animationDurationUpdate:o.reduced?0:500,
    animationEasingUpdate:'cubicOut',
    tooltip:obsTooltipBase(t),
  },extra||{});
}
function obsTooltipBase(t){
  return {confine:true,backgroundColor:t.panel3,borderColor:t.lineStrong,borderWidth:1,padding:[8,10],textStyle:{color:t.text,fontSize:11.5},extraCssText:'box-shadow:none;border-radius:9px;'};
}
function obsCategoryAxis(t,labels,extra){
  return Object.assign({type:'category',data:labels,axisLine:{lineStyle:{color:t.line}},axisTick:{show:false},axisLabel:{color:t.muted2,fontSize:10.5,hideOverlap:true}},extra||{});
}
function obsValueAxis(t,formatter,extra){
  return Object.assign({type:'value',splitNumber:4,axisLine:{show:false},axisTick:{show:false},axisLabel:{color:t.muted2,fontSize:10.5,formatter},splitLine:{lineStyle:{color:t.line,type:[3,4]}}},extra||{});
}
function obsBarDelay(o,step){return o.reduced?0:(idx=>idx*(step||35))}
function obsTooltipRows(title,rows,note){
  return `<div class="obs-tt"><b>${obsEsc(title)}</b>${rows.map(([k,v])=>`<div><span>${obsEsc(k)}</span><em>${obsEsc(v)}</em></div>`).join('')}${note?`<small>${obsEsc(note)}</small>`:''}</div>`;
}

// ---------- 趋势 ----------
const OBS_TREND_MODES=['tokens','comp','cost','runs','issues','rate'];
function obsTrendModeValue(p,mode){
  if(mode==='cost')return Number(p?.costCny||0);
  if(mode==='runs')return Number(p?.runs||0);
  if(mode==='issues')return Number(p?.issues||0);
  if(mode==='rate'){
    const attempts=Number(p?.succeeded||0)+Number(p?.failed||0);
    return attempts?Math.round(Number(p?.succeeded||0)*1000/attempts)/10:null;
  }
  return Number(p?.tokens||0);
}
function obsTrendCacheHit(p){
  const base=Number(p?.inputTokens||0)+Number(p?.cacheReadTokens||0);
  return base?Number(p?.cacheReadTokens||0)*100/base:null;
}
function obsTrendFormat(mode,o){
  if(mode==='tokens'||mode==='comp')return v=>o.fmtTokens(v);
  if(mode==='cost')return v=>o.fmtCost(v);
  if(mode==='rate')return v=>v==null?'—':Math.round(v)+'%';
  return v=>String(Math.round(Number(v)||0));
}
// prev 与 points 按位置对齐：prev[i] 为 points[i] 上一周期同位置的数据点，缺失为 null；全为空时不画虚线。
function obsTrendPrevValues(points,prev,mode){
  if(mode==='comp'||!Array.isArray(prev))return null;
  const values=points.map((_,i)=>prev[i]?obsTrendModeValue(prev[i],mode):null);
  return values.some(v=>v!=null)?values:null;
}
function obsTrendOption(points,mode,sel,prev,opts){
  const o=obsChartOpts(opts),t=o.theme;
  points=Array.isArray(points)?points:[];
  mode=OBS_TREND_MODES.includes(mode)?mode:'tokens';
  const labels=points.map(p=>o.label(p));
  const selIdx=points.findIndex(p=>p&&p.date===sel);
  const fmt=obsTrendFormat(mode,o);
  const prevValues=obsTrendPrevValues(points,prev,mode);
  const tooltip={...obsTooltipBase(t),trigger:'axis',axisPointer:{type:'line',lineStyle:{color:t.lineStrong}},formatter:params=>{
    const list=Array.isArray(params)?params:[params];
    const idx=list[0]?list[0].dataIndex:-1;
    const p=points[idx];
    if(!p)return '';
    const rate=obsTrendModeValue(p,'rate'),hit=obsTrendCacheHit(p);
    const rows=[['Token',o.fmtTokens(p.tokens)],['成本',o.fmtCost(p.costCny)],['运行',String(p.runs||0)],['需求',String(p.issues||0)],['成功率',rate==null?'—':rate.toFixed(0)+'%'],['缓存命中',hit==null?'—':hit.toFixed(0)+'%']];
    if(mode==='comp')rows.splice(1,0,['输入',o.fmtTokens(p.inputTokens)],['输出',o.fmtTokens(p.outputTokens)],['缓存读',o.fmtTokens(p.cacheReadTokens)],['缓存写',o.fmtTokens(p.cacheWriteTokens)]);
    const pp=mode!=='comp'&&prevValues?prev[idx]:null;
    const pv=pp?obsTrendModeValue(pp,mode):null;
    if(pv!=null)rows.push([`上一周期 ${o.label(pp)}`,fmt(pv)]);
    return obsTooltipRows(labels[idx],rows,'点击查看该时段');
  }};
  const selArea=selIdx>=0?{silent:true,itemStyle:{color:t.text,opacity:.05},data:[[{xAxis:labels[selIdx]},{xAxis:labels[selIdx]}]]}:undefined;
  let series;
  if(mode==='comp'){
    const parts=[['输入','inputTokens',t.c1],['输出','outputTokens',t.c2],['缓存读','cacheReadTokens',t.c3],['缓存写','cacheWriteTokens',t.c4]];
    series=parts.map(([name,field,color],i)=>({name,type:'line',stack:'tokens',symbol:'none',smooth:false,lineStyle:{width:0},areaStyle:{color,opacity:1},itemStyle:{color},data:points.map(p=>Number(p?.[field]||0)),animationDelay:o.reduced?0:i*90,markArea:i===0?selArea:undefined}));
  }else{
    const values=points.map(p=>obsTrendModeValue(p,mode));
    series=[
      ...(prevValues?[{name:'上一周期',type:'line',symbol:'none',silent:true,z:1,lineStyle:{color:t.muted2,width:1.5,type:'dashed'},itemStyle:{color:t.muted2},data:prevValues,connectNulls:false,animationDelay:o.reduced?0:300}]:[]),
      {name:'本期',type:'line',symbol:'circle',symbolSize:points.length>16?0:5,showSymbol:points.length<=16,z:2,lineStyle:{color:t.c1,width:1.8},itemStyle:{color:t.c1,borderColor:t.panel,borderWidth:1.5},areaStyle:{color:t.c1,opacity:.08},data:values,connectNulls:true,animationDuration:o.reduced?0:900,
        markArea:selArea,
        markPoint:selIdx>=0&&values[selIdx]!=null?{symbol:'circle',symbolSize:10,silent:true,label:{show:false},itemStyle:{color:t.c1,borderColor:t.panel,borderWidth:2},data:[{coord:[selIdx,values[selIdx]]}],animation:!o.reduced}:undefined},
    ];
  }
  return obsChartBase(o,{
    grid:{left:6,right:12,top:14,bottom:4,containLabel:true},
    tooltip,
    xAxis:obsCategoryAxis(t,labels,{boundaryGap:mode!=='comp',axisLabel:{color:t.muted2,fontSize:10.5,hideOverlap:true,formatter:(v,i)=>i===selIdx?`{s|${v}}`:v,rich:{s:{color:t.text,fontWeight:600,fontSize:10.5}}}}),
    yAxis:obsValueAxis(t,v=>fmt(v),mode==='rate'?{max:100,min:0}:{}),
    series,
  });
}

// ---------- Token 去向环图 ----------
function obsBreakdownPalette(t){return [t.c1,t.c2,t.c3,t.muted2,t.c4,t.panel3]}
function obsBreakdownValue(row,metric){return Number(metric==='cost'?row?.costCny:row?.tokens)||0}
function obsBreakdownTotal(rows,metric){return (rows||[]).reduce((n,r)=>n+obsBreakdownValue(r,metric),0)}
// Conversation code changes stand apart from SOPs, and are often a sliver.
const OBS_CONVERSATION_COLOR='#f59e0b';
function obsBreakdownOption(rows,metric,opts){
  const o=obsChartOpts(opts),t=o.theme,pal=obsBreakdownPalette(t);
  rows=Array.isArray(rows)?rows:[];
  metric=metric==='cost'?'cost':'tokens';
  const total=obsBreakdownTotal(rows,metric);
  const fmt=metric==='cost'?o.fmtCost:o.fmtTokens;
  return obsChartBase(o,{
    tooltip:{...obsTooltipBase(t),trigger:'item',formatter:p=>obsTooltipRows(p.name,[[metric==='cost'?'成本':'Token',fmt(p.value)],['占比',total?(p.value*100/total).toFixed(1)+'%':'—']],'点击查看明细')},
    series:[{type:'pie',radius:['66%','80%'],center:['50%','50%'],avoidLabelOverlap:true,minAngle:2,padAngle:1,label:{show:false},labelLine:{show:false},itemStyle:{borderColor:t.panel,borderWidth:2},emphasis:{scale:true,scaleSize:4,label:{show:false}},
      animationType:'expansion',animationDuration:o.reduced?0:700,
      data:rows.map((r,i)=>({name:String(r.name||r.key||'—'),value:obsBreakdownValue(r,metric),itemStyle:{color:r.conversation?OBS_CONVERSATION_COLOR:pal[Math.min(i,pal.length-1)]}}))}],
  });
}

// ---------- 星期 × 小时热力图 ----------
function obsHeatMatrix(cells,metric){
  const grid=Array.from({length:7},()=>Array.from({length:24},()=>({runs:0,tokens:0,costCny:0})));
  for(const c of cells||[]){
    const d=Number(c?.weekday),h=Number(c?.hour);
    if(!(d>=0&&d<7&&h>=0&&h<24))continue;
    const cell=grid[d][h];
    cell.runs+=Number(c.runs||0);cell.tokens+=Number(c.tokens||0);cell.costCny+=Number(c.costCny||0);
  }
  return grid;
}
function obsHeatPeak(cells,metric){
  const grid=obsHeatMatrix(cells);
  let best=null;
  grid.forEach((row,d)=>row.forEach((cell,h)=>{const v=metric==='runs'?cell.runs:cell.tokens;if(v>0&&(!best||v>best.value))best={weekday:d,hour:h,value:v}}));
  return best;
}
function obsHeatOption(cells,metric,opts){
  const o=obsChartOpts(opts),t=o.theme;
  metric=metric==='runs'?'runs':'tokens';
  const grid=obsHeatMatrix(cells);
  const data=[];
  let max=0;
  grid.forEach((row,d)=>row.forEach((cell,h)=>{const v=metric==='runs'?cell.runs:cell.tokens;max=Math.max(max,v);data.push({value:[h,d,v],runs:cell.runs,tokens:cell.tokens})}));
  const hours=Array.from({length:24},(_,h)=>String(h));
  return obsChartBase(o,{
    grid:{left:6,right:6,top:4,bottom:4,containLabel:true},
    tooltip:{...obsTooltipBase(t),trigger:'item',formatter:p=>{const [h,d]=p.value;return obsTooltipRows(`${OBS_WEEKDAYS[d]} ${String(h).padStart(2,'0')}:00`,[['运行',String(p.data.runs)+' 次'],['Token',o.fmtTokens(p.data.tokens)]])}},
    xAxis:{type:'category',data:hours,splitArea:{show:false},axisLine:{show:false},axisTick:{show:false},axisLabel:{color:t.muted2,fontSize:10,interval:5}},
    yAxis:{type:'category',data:OBS_WEEKDAYS,inverse:true,axisLine:{show:false},axisTick:{show:false},axisLabel:{color:t.muted2,fontSize:10}},
    visualMap:{show:false,min:0,max:Math.max(1,max),inRange:{color:[t.panel2,t.c1]}},
    series:[{type:'heatmap',data,itemStyle:{borderColor:t.panel,borderWidth:2,borderRadius:3},emphasis:{itemStyle:{borderColor:t.text,borderWidth:1}},
      animationDuration:o.reduced?0:500,animationDelay:o.reduced?0:(idx=>(idx%24)*8+Math.floor(idx/24)*24)}],
  });
}

// ---------- 交付漏斗 / 按负责人 ----------
function obsFunnelStats(stages){
  const list=(stages||[]).map(s=>({name:String(s.name||''),value:Number(s.value)||0,key:s.key}));
  let worst=-1,worstRatio=Infinity;
  list.forEach((s,i)=>{
    if(!i){s.conv=null;return}
    const prev=list[i-1].value;
    s.conv=prev?s.value*100/prev:null;
    if(prev&&s.value/prev<worstRatio&&s.value<prev){worstRatio=s.value/prev;worst=i}
  });
  list.forEach((s,i)=>{s.worst=i===worst;s.lost=i?Math.max(0,list[i-1].value-s.value):0});
  return {stages:list,worst};
}
function obsFunnelOption(stages,opts){
  const o=obsChartOpts(opts),t=o.theme;
  const stats=obsFunnelStats(stages);
  const list=stats.stages;
  const active=opts&&opts.active!=null?opts.active:-1;
  const max=Math.max(1,...list.map(s=>s.value));
  return obsChartBase(o,{
    grid:{left:6,right:118,top:4,bottom:4,containLabel:true},
    tooltip:{...obsTooltipBase(t),trigger:'item',formatter:p=>{const s=list[p.dataIndex];return obsTooltipRows(s.name,[['数量',String(s.value)],['转化率',s.conv==null?'—':Math.round(s.conv)+'%']],s.worst?'最大流失':'点击筛选明细')}},
    xAxis:{type:'value',max,show:false},
    yAxis:{type:'category',inverse:true,data:list.map(s=>s.name),axisLine:{show:false},axisTick:{show:false},axisLabel:{color:t.muted,fontSize:12,formatter:(v,i)=>i===active?`{a|${v}}`:v,rich:{a:{color:t.text,fontWeight:600,fontSize:12}}}},
    series:[{type:'bar',barWidth:18,showBackground:true,backgroundStyle:{color:t.panel2,borderRadius:4},itemStyle:{borderRadius:4},
      label:{show:true,position:'right',distance:8,formatter:p=>{const s=list[p.dataIndex];const conv=s.conv==null?'':`  {${s.worst?'w':'c'}|↓ ${Math.round(s.conv)}%${s.worst?' · 最大流失':''}}`;return `{v|${s.value}}${conv}`},rich:{v:{color:t.text,fontWeight:600,fontSize:14},c:{color:t.muted2,fontSize:10.5},w:{color:t.red,fontSize:10.5,fontWeight:500}}},
      data:list.map((s,i)=>({value:s.value,itemStyle:{color:i===list.length-1||i===active?t.c1:t.c2}})),
      animationDelay:obsBarDelay(o,90)}],
  });
}
function obsOwnersOption(rows,opts){
  const o=obsChartOpts(opts),t=o.theme;
  rows=Array.isArray(rows)?rows:[];
  const names=rows.map(r=>String(r.name||'—'));
  const parts=[['已交付','delivered',t.c1],['进行中','active',t.c3],['未交付','undelivered',t.c4]];
  return obsChartBase(o,{
    grid:{left:6,right:12,top:4,bottom:4,containLabel:true},
    tooltip:{...obsTooltipBase(t),trigger:'axis',axisPointer:{type:'shadow',shadowStyle:{color:t.text,opacity:.04}},formatter:params=>{const r=rows[params[0]?.dataIndex];if(!r)return '';return obsTooltipRows(r.name,[['已交付',String(r.delivered||0)],['进行中',String(r.active||0)],['未交付',String(r.undelivered||0)],['端到端',String(r.durationLabel||'—')],['Tokens',o.fmtTokens(r.tokens)]])}},
    xAxis:{type:'value',show:false},
    yAxis:{type:'category',inverse:true,data:names,axisLine:{show:false},axisTick:{show:false},axisLabel:{color:t.muted,fontSize:12,width:72,overflow:'truncate'}},
    series:parts.map(([name,field,color])=>({name,type:'bar',stack:'owners',barWidth:10,itemStyle:{color},data:rows.map(r=>Number(r[field]||0)),animationDelay:obsBarDelay(o,60)})),
  });
}

// ---------- SkillHub 提效 ----------
function obsStackBarOption(labels,stacks,opts,extra){
  const o=obsChartOpts(opts),t=o.theme;
  extra=extra||{};
  const totals=labels.map((_,i)=>stacks.reduce((n,s)=>n+(Number(s.values[i])||0),0));
  const series=stacks.map((s,si)=>({name:s.name,type:'bar',stack:'total',barMaxWidth:extra.barMaxWidth||36,itemStyle:{color:s.color,borderRadius:si===stacks.length-1?[2,2,0,0]:0},data:labels.map((_,i)=>Number(s.values[i])||0),animationDelay:obsBarDelay(o,extra.delayStep||35),
    label:extra.showTotal&&si===stacks.length-1?{show:true,position:'top',color:t.text,fontWeight:600,fontSize:11,formatter:p=>totals[p.dataIndex]||''}:undefined}));
  if(extra.markFrom!=null&&extra.markFrom>=0&&labels.length){
    series[0].markArea={silent:true,itemStyle:{color:t.text,opacity:.05},data:[[{xAxis:labels[Math.min(extra.markFrom,labels.length-1)]},{xAxis:labels[labels.length-1]}]]};
  }
  return obsChartBase(o,{
    grid:{left:6,right:8,top:extra.showTotal?22:12,bottom:4,containLabel:true},
    tooltip:{...obsTooltipBase(t),trigger:'axis',axisPointer:{type:'shadow',shadowStyle:{color:t.text,opacity:.04}},formatter:params=>{const idx=params[0]?.dataIndex;return obsTooltipRows(labels[idx],[...stacks.map(s=>[s.name,String(Number(s.values[idx])||0)]),['合计',String(totals[idx]||0)]])}},
    xAxis:obsCategoryAxis(t,labels,extra.xInterval!=null?{axisLabel:{color:t.muted2,fontSize:10.5,interval:extra.xInterval}}:{}),
    yAxis:obsValueAxis(t,v=>String(v)),
    series,
  });
}
function obsSkillhubMonthlyOption(d,opts){
  const o=obsChartOpts(opts),t=o.theme,gf=d?.gongfeng||{};
  const months=Object.keys(gf.mergedMonths||{}).sort();
  return obsStackBarOption(months.map(m=>Number(m.slice(5))+'月'),[{name:'老库',color:t.c4,values:months.map(m=>gf.monthlyLegacy?.[m]||0)},{name:'大仓',color:t.c1,values:months.map(m=>gf.monthlyMonorepo?.[m]||0)}],opts,{showTotal:true});
}
function obsSkillhubWeeklyOption(d,opts){
  const o=obsChartOpts(opts),t=o.theme,gf=d?.gongfeng||{};
  const keys=Object.keys(gf.weeklyMerged||{}).sort().slice(-12);
  return obsStackBarOption(keys.map(k=>'W'+k.slice(-2)),[{name:'老库',color:t.c4,values:keys.map(k=>gf.weeklyLegacy?.[k]||0)},{name:'大仓',color:t.c1,values:keys.map(k=>gf.weeklyMonorepo?.[k]||0)}],opts,{markFrom:Math.max(0,keys.length-4),barMaxWidth:28});
}
function obsSkillhubDeployOption(snap,opts){
  const o=obsChartOpts(opts),t=o.theme,daily=snap?.dailySample||{};
  const labels=daily.labels||[];
  return obsStackBarOption(labels,[{name:'后端',color:t.c1,values:daily.backend||[]},{name:'前端',color:t.c2,values:daily.frontend||[]},{name:'E2E',color:t.c3,values:daily.e2e||[]}],opts,{xInterval:4,delayStep:12,barMaxWidth:18});
}

// ---------- 研发效能 ----------
const OBS_PHASES=[['clarification','澄清','c1'],['development','研发','c2'],['testing','测试','c3']];
const OBS_PHASE_TOP_LABEL_MAX=45;
// 柱高 = 当日三阶段均值之和；三段全空的日期为 null（不画柱）。point.total 是逐条求和后再平均，口径不同，只在悬停中单列。
function obsPhaseSums(points){
  return (points||[]).map(p=>OBS_PHASES.some(([k])=>p?.[k]!=null)?OBS_PHASES.reduce((n,[k])=>n+(Number(p[k])||0),0):null);
}
const OBS_DURATION_STEPS=[1,2,5,10,15,30,60,120,240,360,720,1440,2880,7200].map(m=>m*60000);
function obsDurationAxis(max){
  if(!(max>0))return {};
  const step=OBS_DURATION_STEPS.find(s=>max/s<=5)||Math.ceil(max/5/OBS_DURATION_STEPS.at(-1))*OBS_DURATION_STEPS.at(-1);
  return {interval:step,max:Math.ceil(max/step)*step};
}
function obsPhaseStackOption(points,opts){
  const o=obsChartOpts(opts),t=o.theme;
  points=Array.isArray(points)?points:[];
  const labels=points.map(p=>o.label(p));
  const sums=obsPhaseSums(points);
  const fd=v=>o.fmtDuration(v);
  const delay=obsBarDelay(o,points.length>60?4:30);
  const series=OBS_PHASES.map(([key,name,color],i)=>({name,type:'bar',stack:'phase',barMaxWidth:36,itemStyle:{color:t[color],borderRadius:i===OBS_PHASES.length-1?[2,2,0,0]:0},
    data:points.map((p,j)=>sums[j]==null?null:(p[key]==null?0:Number(p[key]))),animationDelay:delay}));
  series.push({name:'合计',type:'bar',stack:'phase',silent:true,barMaxWidth:36,itemStyle:{color:'transparent'},data:sums.map(v=>v==null?null:0),animationDelay:delay,
    label:{show:points.length<=OBS_PHASE_TOP_LABEL_MAX,position:'top',color:t.text,fontWeight:600,fontSize:11,formatter:p=>sums[p.dataIndex]==null?'':fd(sums[p.dataIndex])}});
  return obsChartBase(o,{
    grid:{left:6,right:8,top:34,bottom:4,containLabel:true},
    legend:{data:OBS_PHASES.map(([,name])=>name),top:0,right:0,icon:'roundRect',itemWidth:9,itemHeight:9,itemGap:14,textStyle:{color:t.muted2,fontSize:11},inactiveColor:t.lineStrong},
    tooltip:{...obsTooltipBase(t),trigger:'axis',axisPointer:{type:'shadow',shadowStyle:{color:t.text,opacity:.04}},formatter:params=>{
      const list=Array.isArray(params)?params:[params];
      const idx=list[0]?list[0].dataIndex:-1;
      const p=points[idx];
      if(!p)return '';
      if(sums[idx]==null)return obsTooltipRows(labels[idx],[],'当日没有完成项样本');
      const samples=p.samples||{};
      const rows=OBS_PHASES.map(([key,name])=>[name,p[key]==null?'待采集':fd(p[key])+(samples[key]!=null?`（${samples[key]} 条）`:'')]);
      rows.push(['三阶段均值之和',fd(sums[idx])],['已采集耗时（按条平均）',p.total==null?'待采集':fd(p.total)],['完成项样本',`${Number(p.deliveries)||0} 条`]);
      return obsTooltipRows(labels[idx],rows);
    }},
    xAxis:obsCategoryAxis(t,labels),
    yAxis:obsValueAxis(t,v=>v?fd(v):'0',obsDurationAxis(Math.max(0,...sums.filter(v=>v!=null)))),
    series,
  });
}
// spec: {name, get(point)->number|null, format(v), kind:'bar'|'line', percent, integer, rows(point)->[[k,v]]}
function obsMetricTrendOption(points,spec,opts){
  const o=obsChartOpts(opts),t=o.theme;
  points=Array.isArray(points)?points:[];
  spec=spec||{};
  const get=typeof spec.get==='function'?spec.get:(()=>null);
  const fmt=typeof spec.format==='function'?spec.format:(v=>String(v));
  const name=spec.name||'';
  const bar=spec.kind==='bar';
  const labels=points.map(p=>o.label(p));
  const values=points.map(p=>{const v=get(p);return v==null||!Number.isFinite(Number(v))?null:Number(v)});
  const known=values.filter(v=>v!=null).length;
  const tooltip={...obsTooltipBase(t),trigger:'axis',axisPointer:bar?{type:'shadow',shadowStyle:{color:t.text,opacity:.04}}:{type:'line',lineStyle:{color:t.lineStrong}},formatter:params=>{
    const list=Array.isArray(params)?params:[params];
    const idx=list[0]?list[0].dataIndex:-1;
    const p=points[idx];
    if(!p)return '';
    const rows=[[name,values[idx]==null?(spec.emptyLabel||'—'):fmt(values[idx])]];
    if(typeof spec.rows==='function')rows.push(...spec.rows(p));
    return obsTooltipRows(labels[idx],rows);
  }};
  const series=bar
    ?[{name,type:'bar',barMaxWidth:28,itemStyle:{color:t.c1,borderRadius:[2,2,0,0]},data:values,animationDelay:obsBarDelay(o,points.length>60?4:30),
      label:{show:points.length<=31,position:'top',color:t.text,fontWeight:600,fontSize:11,formatter:p=>p.value?fmt(p.value):''}}]
    :[{name,type:'line',symbol:'circle',symbolSize:5,showSymbol:points.length<=46||known<=1,lineStyle:{color:t.c1,width:1.8},itemStyle:{color:t.c1,borderColor:t.panel,borderWidth:1.5},areaStyle:{color:t.c1,opacity:.08},data:values,connectNulls:false,animationDuration:o.reduced?0:900}];
  return obsChartBase(o,{
    grid:{left:6,right:12,top:bar?22:14,bottom:4,containLabel:true},
    tooltip,
    xAxis:obsCategoryAxis(t,labels,bar?{}:{boundaryGap:false}),
    yAxis:obsValueAxis(t,v=>fmt(v),spec.percent?{min:0,max:100,interval:25}:(spec.integer?{minInterval:1}:{})),
    series,
  });
}
// slices: [{label,value}]；opts.center = {value, unit} 显示在环心。颜色按原始下标取，0 值分片不画但保留图例对应关系。
function obsSliceDonutOption(slices,opts){
  const o=obsChartOpts(opts),t=o.theme,pal=obsBreakdownPalette(t);
  const list=(Array.isArray(slices)?slices:[]).map((s,i)=>({name:String(s?.label??s?.name??'—'),value:Math.max(0,Number(s?.value)||0),itemStyle:{color:pal[Math.min(i,pal.length-1)]}}));
  const total=list.reduce((n,s)=>n+s.value,0);
  const center=opts&&opts.center;
  const extra={
    tooltip:{...obsTooltipBase(t),trigger:'item',formatter:p=>obsTooltipRows(p.name,[['数量',String(p.value)],['占比',total?(p.value*100/total).toFixed(1)+'%':'—']])},
    series:[{type:'pie',radius:['68%','82%'],center:['50%','50%'],avoidLabelOverlap:true,minAngle:2,padAngle:1,label:{show:false},labelLine:{show:false},itemStyle:{borderColor:t.panel,borderWidth:2},emphasis:{scale:true,scaleSize:4,label:{show:false}},
      animationType:'expansion',animationDuration:o.reduced?0:700,data:list.filter(s=>s.value>0)}],
  };
  if(center)extra.title={text:String(center.value??''),subtext:String(center.unit||''),left:'center',top:'center',itemGap:2,textStyle:{color:t.text,fontSize:20,fontWeight:650},subtextStyle:{color:t.muted2,fontSize:11}};
  return obsChartBase(o,extra);
}

// ---------- 挂载、释放与重播 ----------
// spec: {option(theme,reduced), fallback()->html, empty, on:{event:fn}, axisClick(idx), mounted(entry)}
function obsChartSlot(key,spec,height){
  obsChartSpecs[key]=spec||{};
  const empty=!spec||spec.empty;
  return `<div class="obs-chart" data-obs-chart="${obsEsc(key)}" style="height:${Number(height)||240}px"${empty?' data-empty="1"':''}>${empty?`<div class="obs-chart-empty">${obsEsc(spec?.emptyText||'暂无数据')}</div>`:'<div class="obs-chart-skeleton" aria-hidden="true"></div>'}</div>`;
}
function obsChartEntry(key){return obsCharts.get(key)||null}
function obsChartCount(){return obsCharts.size}
function obsObserveVisible(el,fn){
  if(typeof IntersectionObserver!=='function'){fn();return}
  if(!obsChartIO)obsChartIO=new IntersectionObserver(entries=>{
    for(const e of entries){
      if(!e.isIntersecting)continue;
      const cb=obsVisibleCallbacks.get(e.target);
      obsVisibleCallbacks.delete(e.target);
      obsChartIO.unobserve(e.target);
      if(cb)cb();
    }
  },{threshold:.15});
  obsVisibleCallbacks.set(el,fn);
  obsChartIO.observe(el);
}
function obsResizeObserver(){
  if(obsChartRO||typeof ResizeObserver!=='function')return obsChartRO;
  obsChartRO=new ResizeObserver(entries=>{
    for(const e of entries){
      const el=e.target;
      if(obsChartWaiting.has(el)){
        if(el.clientWidth>0){obsChartWaiting.delete(el);obsChartRO.unobserve(el);obsInitChart(el)}
        continue;
      }
      for(const entry of obsCharts.values())if(entry.el===el&&!entry.chart.isDisposed?.())entry.chart.resize();
    }
  });
  return obsChartRO;
}
function obsWatchTheme(){
  if(obsThemeMO||typeof MutationObserver!=='function'||typeof document==='undefined')return;
  obsThemeMO=new MutationObserver(()=>obsRebuildCharts());
  obsThemeMO.observe(document.documentElement,{attributes:true,attributeFilter:['data-theme']});
}
function obsMountCharts(root){
  if(!root||typeof root.querySelectorAll!=='function')return;
  if(obsEChartsFailed){obsEChartsFailed=false;obsEChartsPromise=null}
  obsWatchTheme();
  for(const [key,entry] of obsCharts)if(!entry.el.isConnected)obsDisposeChart(key);
  root.querySelectorAll('[data-obs-chart]').forEach(el=>{
    const key=el.dataset.obsChart;
    if(el.dataset.empty||el.dataset.obsChartPending)return;
    const entry=obsCharts.get(key);
    if(entry&&entry.el===el)return;
    if(entry)obsDisposeChart(key);
    el.dataset.obsChartPending='1';
    obsObserveVisible(el,()=>obsInitChart(el));
  });
  if(typeof obsCountUp==='function')root.querySelectorAll('[data-obs-count]').forEach(el=>{if(!el.dataset.obsCounted)obsObserveVisible(el,()=>obsCountUp(el))});
}
async function obsInitChart(el){
  const key=el.dataset.obsChart;
  if(!el.isConnected||!obsChartSpecs[key])return;
  if(!el.clientWidth){
    const ro=obsResizeObserver();
    if(ro){obsChartWaiting.add(el);ro.observe(el);return}
  }
  const ec=await obsEnsureECharts();
  const spec=obsChartSpecs[key];
  if(!el.isConnected||el.dataset.obsChart!==key||!spec)return;
  delete el.dataset.obsChartPending;
  if(!ec){obsChartFallback(el,spec);return}
  const existing=obsCharts.get(key);
  if(existing){if(existing.el===el)return;obsDisposeChart(key)}
  el.querySelector('.obs-chart-skeleton')?.remove();
  const chart=ec.init(el,null,{renderer:'svg'});
  const entry={key,el,chart,spec};
  obsCharts.set(key,entry);
  el.classList.add('is-ready');
  chart.setOption(spec.option(obsEChartsTheme(),obsReducedMotion()),true);
  obsBindChartEvents(entry);
  obsResizeObserver()?.observe(el);
  if(typeof spec.mounted==='function')spec.mounted(entry);
}
function obsBindChartEvents(entry){
  const {chart,spec}=entry;
  for(const [name,fn] of Object.entries(spec.on||{}))chart.on(name,params=>fn(params,entry));
  if(typeof spec.axisClick==='function'){
    chart.getZr().on('click',e=>{
      const point=[e.offsetX,e.offsetY];
      if(!chart.containPixel({gridIndex:0},point))return;
      const pos=chart.convertFromPixel({gridIndex:0},point);
      const idx=Math.round(Array.isArray(pos)?pos[0]:pos);
      if(Number.isFinite(idx))spec.axisClick(idx,entry);
    });
  }
}
function obsChartFallback(el,spec){
  let html='';
  try{html=typeof spec.fallback==='function'?spec.fallback():''}catch(e){html=''}
  el.innerHTML=html||'<div class="obs-chart-empty">图表暂不可用</div>';
  el.classList.add('is-fallback');
  el.style.height='auto';
  const head=el.closest('[data-obs-card]')?.querySelector('.obs-card-head');
  if(head&&!head.querySelector('.obs-fallback-badge'))head.querySelector('.obs-card-tools')?.insertAdjacentHTML('afterbegin','<span class="obs-fallback-badge" title="图表库未加载，显示简版图表">简版图表</span>');
}
function obsDisposeChart(key){
  const entry=obsCharts.get(key);
  if(!entry)return;
  obsCharts.delete(key);
  obsChartRO?.unobserve(entry.el);
  try{entry.chart.dispose()}catch(e){}
}
function obsDisposeCharts(root){
  for(const [key,entry] of [...obsCharts]){
    if(!root||!entry.el.isConnected||root.contains(entry.el))obsDisposeChart(key);
  }
  const scope=root&&typeof root.querySelectorAll==='function'?root:(typeof document!=='undefined'?document:null);
  scope?.querySelectorAll?.('[data-obs-chart],[data-obs-count]').forEach(el=>{
    obsVisibleCallbacks.delete(el);
    obsChartIO?.unobserve(el);
    obsChartWaiting.delete(el);
    delete el.dataset.obsChartPending;
  });
}
function obsReplayChart(key){
  const entry=obsCharts.get(key);
  if(!entry)return false;
  entry.chart.clear();
  entry.chart.setOption(entry.spec.option(obsEChartsTheme(),obsReducedMotion()),true);
  return true;
}
function obsRebuildCharts(){
  for(const [key,entry] of [...obsCharts]){
    const el=entry.el;
    obsDisposeChart(key);
    if(el.isConnected)obsInitChart(el);
  }
}
