// Business-domain permissions come from the server, independent of the project switcher.
const businessState={items:[],canAdmin:false,userId:0,scope:'all',from:'',to:'',report:null,error:'',loading:false,editorId:null,seq:0,rangeDays:30,sort:'completed',selects:[]};
function businessDate(d){return `${d.getFullYear()}-${String(d.getMonth()+1).padStart(2,'0')}-${String(d.getDate()).padStart(2,'0')}`}
function businessDomainName(id){return businessState.items.find(x=>x.domain.id===id)?.domain.name||(state.data?.businessDomains||[]).find(d=>d.id===id)?.name||id||'未分类'}
function businessUserName(id){const u=(state.data?.users||[]).find(u=>Number(u.id)===Number(id));return u?.chineseName||u?.loginName||String(id)}
function businessCost(value,known=true){const amount=Number(value||0);return !known&&!amount?'未识别':`¥${amount>0&&amount<.01?amount.toPrecision(3):amount.toFixed(2)}${known?'':'（部分未识别）'}`}
function businessUserOptions(ids){return(state.data?.users||[]).map(u=>`<option value="${Number(u.id)}" ${ids.includes(Number(u.id))?'selected':''}>${escapeHTML(u.chineseName||u.loginName)} · ${escapeHTML(u.loginName)}</option>`).join('')}
async function loadBusinessDomains(){
  if(!businessState.from&&!businessState.to&&!businessState.initialized){const to=new Date(),from=new Date(to);from.setDate(from.getDate()-29);businessState.from=businessDate(from);businessState.to=businessDate(to);businessState.initialized=true}
  businessState.loading=true;businessState.error='';renderBusinessDomains();
  // 报表接口不依赖权限/项目列表的返回，先并发发出，避免多一个串行往返。
  const pendingReport=refreshBusinessReport();
  try{
    const [access,projects]=await Promise.all([api('/api/business-domains',{silent:true}),api('/api/projects',{silent:true})]);
    businessState.items=access.items;state.data.businessDomains=access.items.map(x=>x.domain);businessState.canAdmin=access.canAdmin;businessState.userId=access.userId;state.data.projects=projects;renderProjectSwitcher();
    if(businessState.scope!=='all'&&businessState.scope!=='unclassified'&&!access.items.some(x=>x.domain.id===businessState.scope&&x.canView)){businessState.scope='all';await refreshBusinessReport();return}
    await pendingReport;
    // 报表可能先于业务域列表返回（命中缓存时尤其如此），已按旧列表渲染；
    // 这里补渲染一次，让业务域下拉、名称和管理权限用上刚拿到的列表。
    renderBusinessDomains();
  }catch(e){businessState.seq++;businessState.error=e.message;businessState.loading=false;businessState.report=null;renderBusinessDomains()}
}
async function refreshBusinessReport(){
  const seq=++businessState.seq;businessState.loading=true;businessState.error='';businessState.report=null;renderBusinessDomains();
  const q=new URLSearchParams({businessDomainId:businessState.scope,grain:businessGrain(),timeZone:Intl.DateTimeFormat().resolvedOptions().timeZone||'UTC'});
  try{if(businessState.from)q.set('from',new Date(`${businessState.from}T00:00:00`).toISOString());
  if(businessState.to)q.set('to',new Date(`${businessState.to}T23:59:59.999`).toISOString());
  const report=await api('/api/business-domain-report?'+q,{silent:true});if(seq!==businessState.seq)return;businessState.report=report}catch(e){if(seq===businessState.seq)businessState.error=e.message}
  finally{if(seq===businessState.seq){businessState.loading=false;renderBusinessDomains()}}
}
function businessScopeLabel(){return businessState.scope==='all'?'全部可查看业务':businessState.scope==='unclassified'?'未分类':businessDomainName(businessState.scope)}
function businessGrain(){const b=businessState,days=(Date.parse(b.to)-Date.parse(b.from))/86400000;return !b.from||!b.to||days>180?'month':days>60?'week':'day'}
function businessDurationSamples(metrics,key){return Number(metrics?.[{averageExecutionDurationMs:'executionSamples',averageCompletionToMergeMs:'completionToMergeSamples'}[key]||'cycleSamples']||0)}
function businessCycleLabel(metrics,key='averageCycleDurationMs'){const value=metrics?.[key];return businessDurationSamples(metrics,key)>0&&value!=null&&Number.isFinite(Number(value))&&Number(value)>=0?(Number(value)===0?'0s':duration(Number(value))):'待采集'}
function businessCompletedTime(value){if(!value)return '—';const at=new Date(value);return Number.isFinite(at.getTime())?at.toLocaleString([],{year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit'}):'—'}
function businessSortItems(items){
  const key={completed:'completedIssues',mrs:'mergedMrs',cycle:'averageCycleDurationMs',execution:'averageExecutionDurationMs',merge:'averageCompletionToMergeMs'}[businessState.sort];
  return [...items].sort((a,b)=>{const av=businessState.sort==='tokens'?a.tokens:a.deliveryMetrics?.[key],bv=businessState.sort==='tokens'?b.tokens:b.deliveryMetrics?.[key];return Number(bv||0)-Number(av||0)||a.name.localeCompare(b.name)});
}
function businessBucketStart(value,grain){
  const week=/^(\d{4})-W(\d{2})$/.exec(value||'');
  if(week){const day=new Date(Date.UTC(Number(week[1]),0,4));day.setUTCDate(day.getUTCDate()-((day.getUTCDay()+6)%7)+(Number(week[2])-1)*7);return day}
  const day=new Date(`${value?.length===7?value+'-01':value}T00:00:00Z`);
  if(grain==='month')day.setUTCDate(1);
  if(grain==='week')day.setUTCDate(day.getUTCDate()-((day.getUTCDay()+6)%7));
  return day;
}
function businessChartPoints(trend){
  const grain=businessState.report?.grain||businessGrain(),b=businessState;
  const sorted=[...trend].sort((a,b)=>a.date.localeCompare(b.date));
  const start=businessBucketStart(b.from||sorted[0]?.date,grain),end=businessBucketStart(b.to||sorted.at(-1)?.date,grain);
  if(!Number.isFinite(+start)||!Number.isFinite(+end)||start>end)return [];
  const byDate=new Map(sorted.map(point=>[+businessBucketStart(point.date,grain),point])),points=[];
  for(const day=new Date(start);day<=end;grain==='month'?day.setUTCMonth(day.getUTCMonth()+1):day.setUTCDate(day.getUTCDate()+(grain==='week'?7:1))){
    const point=byDate.get(+day),date=day.toISOString().slice(0,10);points.push({date,label:grain==='month'?date.slice(0,7):date.slice(5),completedIssues:0,mergedMrs:0,runs:0,cycleSamples:0,executionSamples:0,...point});
  }
  return points;
}
function businessDeliveryChart(trend,cycle=false){
  const points=businessChartPoints(trend),series=[['averageExecutionDurationMs','不含等待','executionSamples','var(--c1,#647cf1)'],['averageCycleDurationMs','含等待','cycleSamples','var(--c2,#38b5c8)']],values=series.map(([key])=>businessCycleValues(points,key));
  const hasData=cycle?values.some(list=>list.some(v=>v!=null)):points.some(p=>p.completedIssues||p.mergedMrs);
  if(!hasData)return `<div class="business-chart-empty">${cycle?'暂无可计算完成耗时的样本':'所选时间段暂无需求完成或 MR 合入'}</div>`;
  const w=720,h=220,left=80,right=32,top=20,bottom=34,plotW=w-left-right,plotH=h-top-bottom,step=plotW/points.length;
  const rawMax=Math.max(cycle?1:2,...(cycle?values.flat().map(v=>v??0):points.map(p=>Math.max(p.completedIssues,p.mergedMrs)))),max=cycle?rawMax:Math.ceil(rawMax/2)*2,format=v=>cycle?(v===0?'0':v>=86400000?`${(v/86400000).toFixed(1)}d`:duration(v)):String(Math.round(v));
  const grid=[0,.5,1].map(n=>{const y=top+plotH*(1-n);return `<line x1="${left}" x2="${w-right}" y1="${y}" y2="${y}"/><text x="${left-9}" y="${y+4}" text-anchor="end">${escapeHTML(format(max*n))}</text>`}).join('');
  const x=i=>left+step*(i+.5),y=v=>top+plotH*(1-Number(v)/max),tickEvery=Math.max(1,Math.ceil(points.length/6));
  const ticks=points.map((p,i)=>i%tickEvery===0||i===points.length-1?`<text x="${x(i)}" y="${h-8}" text-anchor="middle">${escapeHTML(p.label)}</text>`:'').join('');
  let marks='';
  if(cycle){marks=series.map(([,label,sampleKey,color],n)=>{let open=false;const list=values[n],path=list.map((value,i)=>{if(value==null){open=false;return ''}const segment=`${open?'L':'M'}${x(i)},${y(value)}`;open=true;return segment}).join(' ');return `<path class="business-cycle-line" style="stroke:${color}" d="${path}"/>`+points.map((p,i)=>list[i]!=null?`<circle class="business-cycle-dot" style="stroke:${color}" cx="${x(i)}" cy="${y(list[i])}" r="3.5"><title>${escapeHTML(p.date)} · ${label} ${escapeHTML(format(list[i]))} · ${p[sampleKey]} 个样本</title></circle>`:'').join('')}).join('')}
  else{const bw=Math.max(.5,Math.min(14,step*.3));marks=points.map((p,i)=>[['completedIssues','已完成需求',-1,'business-completion-bar'],['mergedMrs','合入 MR',0,'business-merge-bar']].map(([key,label,offset,cls])=>p[key]>0?`<rect class="${cls}" x="${x(i)+offset*bw}" y="${y(p[key])}" width="${bw}" height="${top+plotH-y(p[key])}" rx="2"><title>${escapeHTML(p.date)} · ${label} ${p[key]}</title></rect>`:'').join('')).join('')}
  return `<svg class="business-trend-svg" viewBox="0 0 ${w} ${h}" role="img" aria-label="${cycle?'平均完成耗时趋势，分别展示不含等待与含等待，缺少样本的日期留白':'需求完成与 MR 合入趋势'}">${grid}${marks}${ticks}</svg>`;
}
function businessTrendChart(trend){
  const points=businessChartPoints(trend);if(!points.some(p=>p.runs))return '<div class="business-chart-empty">所选时间段暂无运行数据</div>';
  const w=720,h=200,left=38,right=22,top=20,bottom=32,plotW=w-left-right,plotH=h-top-bottom,max=Math.max(2,...points.map(p=>Number(p.runs||0))),step=plotW/points.length,bw=Math.min(28,step*.6);
  const grid=[0,Math.ceil(max/2),max].map(value=>{const y=top+plotH*(1-value/max);return `<line x1="${left}" x2="${w-right}" y1="${y}" y2="${y}"/><text x="${left-8}" y="${y+4}" text-anchor="end">${value}</text>`}).join('');
  const marks=points.map((p,i)=>{const height=p.runs/max*plotH,x=left+step*(i+.5);return p.runs?`<rect x="${x-bw/2}" y="${top+plotH-height}" width="${bw}" height="${height}" rx="2"><title>${escapeHTML(p.date)} · ${p.runs} 次运行</title></rect>`:''}).join('');
  const ticks=points.map((p,i)=>i%Math.max(1,Math.ceil(points.length/6))===0||i===points.length-1?`<text x="${left+step*(i+.5)}" y="${h-8}" text-anchor="middle">${escapeHTML(p.label)}</text>`:'').join('');
  return `<svg class="business-trend-svg" viewBox="0 0 ${w} ${h}" role="img" aria-label="运行次数，按实际日期间隔展示">${grid}${marks}${ticks}</svg>`;
}
// Charts share obs-charts.js's global registry with the statistics page, so every key carries the biz- prefix.
function businessDurationAxis(value){const v=Number(value)||0;return v<=0?'0':v>=86400000?`${(v/86400000).toFixed(1)}d`:duration(v)}
function businessPointTitle(p){const date=String(p?.date||'');return p?.label&&p.label===date.slice(0,7)?p.label:date}
function businessChartGrid(){return {left:6,right:12,top:14,bottom:4,containLabel:true}}
function businessBarWidth(count){return count>45?6:count>20?10:16}
function businessAxisTooltip(t,points,rows){
  return {...obsTooltipBase(t),trigger:'axis',axisPointer:{type:'shadow',shadowStyle:{color:t.text,opacity:.04}},formatter:params=>{const i=(Array.isArray(params)?params[0]:params)?.dataIndex,p=points[i];return p?obsTooltipRows(businessPointTitle(p),rows(p,i)):''}};
}
function businessDeliveryOption(points,opts){
  const o=obsChartOpts(opts),t=o.theme;points=Array.isArray(points)?points:[];
  return obsChartBase(o,{
    grid:businessChartGrid(),
    tooltip:businessAxisTooltip(t,points,p=>[['完成需求',String(Number(p.completedIssues)||0)],['合入 MR',String(Number(p.mergedMrs)||0)]]),
    xAxis:obsCategoryAxis(t,points.map(p=>String(p.label||p.date||''))),
    yAxis:obsValueAxis(t,v=>String(Math.round(v)),{minInterval:1}),
    series:[['完成需求','completedIssues',t.c1],['合入 MR','mergedMrs',t.c2]].map(([name,field,color])=>({name,type:'bar',barMaxWidth:businessBarWidth(points.length),barGap:'20%',itemStyle:{color,borderRadius:[2,2,0,0]},data:points.map(p=>Number(p[field])||0),animationDelay:obsBarDelay(o,points.length>30?10:30)})),
  });
}
// Days without cycle samples stay null so the line breaks instead of dropping to zero.
function businessCycleValues(points,key='averageCycleDurationMs'){return (points||[]).map(p=>{const v=Number(p?.[key]);return businessDurationSamples(p,key)>0&&p[key]!=null&&Number.isFinite(v)&&v>=0?v:null})}
function businessCycleOption(points,opts){
  const o=obsChartOpts(opts),t=o.theme;points=Array.isArray(points)?points:[];
  const series=[['不含等待','averageExecutionDurationMs',t.c1],['含等待','averageCycleDurationMs',t.c2]],values=series.map(([,key])=>businessCycleValues(points,key)),fmt=opts?.fmtDuration||businessDurationAxis;
  return obsChartBase(o,{
    grid:businessChartGrid(),
    tooltip:{...businessAxisTooltip(t,points,(p,i)=>series.map(([label,key],n)=>[label,`${values[n][i]==null?'—':fmt(values[n][i])} · ${businessDurationSamples(p,key)} 个样本`])),axisPointer:{type:'line',lineStyle:{color:t.lineStrong}}},
    xAxis:obsCategoryAxis(t,points.map(p=>String(p.label||p.date||''))),
    yAxis:obsValueAxis(t,businessDurationAxis),
    series:series.map(([name,,color],n)=>({name,type:'line',symbol:'circle',symbolSize:6,showSymbol:true,connectNulls:false,z:2,lineStyle:{color,width:1.8},itemStyle:{color,borderColor:t.panel,borderWidth:1.5},data:values[n],animationDuration:o.reduced?0:900})),
  });
}
function businessRunsOption(points,opts){
  const o=obsChartOpts(opts),t=o.theme;points=Array.isArray(points)?points:[];
  return obsChartBase(o,{
    grid:businessChartGrid(),
    tooltip:businessAxisTooltip(t,points,p=>[['运行',`${Number(p.runs)||0} 次`]]),
    xAxis:obsCategoryAxis(t,points.map(p=>String(p.label||p.date||''))),
    yAxis:obsValueAxis(t,v=>String(Math.round(v)),{minInterval:1}),
    series:[{name:'运行',type:'bar',barMaxWidth:businessBarWidth(points.length)+6,itemStyle:{color:t.c1,borderRadius:[2,2,0,0]},data:points.map(p=>Number(p.runs)||0),animationDelay:obsBarDelay(o,points.length>30?10:30)}],
  });
}
function businessChart(key,points,build,fallback,emptyText,hasData,height){
  return obsChartSlot(key,{option:(theme,reduced)=>build(points,{theme,reduced,fmtDuration:v=>v===0?'0s':duration(v)}),fallback,empty:!hasData,emptyText},hasData?height:110);
}
function businessIcon(name,size=14){return `<svg class="agent-icon harness-icon" data-icon="${name}" width="${size}" height="${size}" aria-hidden="true" focusable="false"></svg>`}
function businessCard(key,title,sub,tools,body){return `<section class="obs-card2 biz-card" data-obs-card="${key}"><header class="obs-card-head"><div class="obs-card-heading"><h3>${title}</h3>${sub?`<p>${sub}</p>`:''}</div><div class="obs-card-tools">${tools||''}</div></header>${body}</section>`}
function businessPanel(title,sub,count,body){return `<section class="biz-panel"><header class="biz-panel-head"><div><h3>${title}</h3><p>${sub}</p></div><span class="biz-count">${count}</span></header>${body}</section>`}
// Tables keep businessCycleLabel's 待采集; the KPI strip shows — and moves the reason into the footnote.
function businessKpiCycle(metrics,key,foot){const label=businessCycleLabel(metrics,key);return label==='待采集'?{value:'—',na:true,foot:`待采集 · ${businessDurationSamples(metrics,key)} 个样本`}:{value:label,foot}}
function businessKpiHTML(k){return `<div class="obs-kpi"><div class="obs-kpi-label"><i class="biz-dot business-color-${k.tone}" aria-hidden="true"></i>${k.label}</div><div class="obs-kpi-value"><b class="num${k.na?' is-na':''}">${k.value}</b></div><div class="obs-kpi-foot">${k.foot}</div></div>`}
function businessReportHTML(){
  const r=businessState.report;if(!r)return '';
  const singleDomain=businessState.scope!=='all',s=r.summary,m=r.deliveryMetrics||{},groups=businessSortItems(r.businessDomains||[]),projects=businessSortItems(r.projects||[]),trend=r.deliveryTrend||[];
  const estimated=Number(m.estimatedCompletionIssues||0),missing=Number(m.completionTimeMissingIssues||0),grainLabel={day:'每日',week:'每周',month:'每月'}[r.grain]||'每日';
  const kpis=[
    {label:'已完成需求',value:m.completedIssues||0,foot:estimated?`含 ${estimated} 条历史时间推定`:'按需求完成时间统计',tone:0},
    {label:'MR 合入数量',value:m.mergedMrs||0,foot:'按合入时间去重 · 含集成分支',tone:1},
    {label:'平均完成耗时',...businessKpiCycle(m,'averageExecutionDurationMs',`${m.executionSamples||0} 条有执行记录的样本`),tone:2},
    {label:'平均完成耗时（含等待）',...businessKpiCycle(m,'averageCycleDurationMs',`${m.cycleSamples||0} 条有起止时间的样本`),tone:3},
    {label:'平均完成后合入耗时',...businessKpiCycle(m,'averageCompletionToMergeMs',`${m.completionToMergeSamples||0} 个有效 MR 样本`),tone:1},
    {label:'单需求 Token',value:m.completedIssues?formatTokens(m.averageTokensPerCompletedIssue):'—',na:!m.completedIssues,foot:'已完成需求的全部关联执行',tone:4}
  ];
  const resources=[['关联需求',s.uniqueIssues||0],['运行',s.totalRuns||0],['运行成功率',s.succeededRuns+s.failedRuns?Number(s.successRate||0).toFixed(1)+'%':'—'],['Token 用量',formatTokens(s.totalTokens)]].map(([label,value])=>`<span>${label}<b>${value}</b></span>`).join('')+`<span>已识别成本<b>${businessCost(s.costReportedCny,Number(s.costCoveragePct||0)===100)}</b><small>识别率 ${Number(s.costCoveragePct||0).toFixed(1)}%</small></span>`;
  const table=(head,rows)=>`<div class="business-table-wrap"><table class="business-table"><thead><tr>${head.map(h=>`<th scope="col">${h}</th>`).join('')}</tr></thead><tbody>${rows.length?rows.join(''):`<tr><td colspan="${head.length}" class="business-table-empty">所选范围暂无数据</td></tr>`}</tbody></table></div>`;
  const metricCells=item=>{const d=item.deliveryMetrics||{};return `<td class="business-emphasis">${d.completedIssues||0}</td><td>${d.mergedMrs||0}</td><td>${businessCycleLabel(d,'averageExecutionDurationMs')}<small>${d.executionSamples||0} 个样本</small></td><td>${businessCycleLabel(d)}<small>${d.cycleSamples||0} 个样本</small></td><td>${businessCycleLabel(d,'averageCompletionToMergeMs')}<small>${d.completionToMergeSamples||0} 个样本</small></td><td>${escapeHTML(businessCompletedTime(d.latestCompletedAt))}</td><td>${formatTokens(item.tokens)}</td><td>${businessCost(item.costCny,item.costKnown)}</td>`};
  const groupRows=groups.map(g=>`<tr><td><div class="biz-name"><span class="biz-av${g.id?'':' is-muted'}" aria-hidden="true">${escapeHTML(String(g.name||'').slice(0,1))}</span><div><button type="button" class="business-scope-link" data-business-scope="${escapeHTML(g.id||'unclassified')}">${escapeHTML(g.name)}${businessIcon('chevron-right',12)}</button>${g.active?'':' <span class="business-status">已停用</span>'}<small>${g.projectCount} 个项目 · ${g.uniqueIssues} 个关联需求</small></div></div></td>${metricCells(g)}</tr>`);
  const projectRows=projects.map(p=>`<tr><td><b>${escapeHTML(p.name)}</b><small>${escapeHTML(p.projectId)}</small></td>${singleDomain?'':`<td><span class="business-domain-tag">${escapeHTML(businessDomainName(p.businessDomainId))}</span></td>`}${metricCells(p)}</tr>`);
  const distributionItems=singleDomain?[...projects].sort((a,b)=>b.tokens-a.tokens||a.name.localeCompare(b.name)):groups;
  const distribution=distributionItems.map((g,i)=>{const share=s.totalTokens?100*g.tokens/s.totalTokens:0;return `<div class="business-mix-row"><div><span class="business-mix-dot business-color-${i%4}"></span><b>${escapeHTML(g.name)}</b><span>${formatTokens(g.tokens)} <small>${share.toFixed(1)}%</small></span></div><div class="business-mix-track"><i class="business-color-${i%4}" style="width:${share.toFixed(2)}%"></i></div><small>${g.runs||0} 次运行 · ${businessCost(g.costCny,g.costKnown)}</small></div>`}).join('')||'<div class="business-chart-empty">暂无业务用量</div>';
  const warnings=[estimated?`${estimated} 条历史完成项缺少完成记录，使用最后一次成功执行的结束时间推定。`:'',missing?`另有 ${missing} 条历史完成项缺少可用完成时间，未计入期间完成数。`:'',m.mergedMrsMissingTime?`${m.mergedMrsMissingTime} 个已合入 MR 缺少合入时间，未计入期间合入数。`:'' ,m.completionToMergeMissingMrs?`${m.completionToMergeMissingMrs} 个 MR 缺少可配对的完成记录或交付关联，未计入完成后合入耗时。`:'',m.mergedBeforeCompletionMrs?`${m.mergedBeforeCompletionMrs} 个 MR 在流程完成前已合入，未计入完成后合入耗时均值。`:''].filter(Boolean);
  const heads=['已完成','合入 MR','平均耗时','平均耗时（含等待）','完成后合入耗时','最近完成时间','Token 用量','成本（人民币）'];
  const delivery=businessChartPoints(trend),runs=businessChartPoints(r.trend||[]);
  const deliveryChart=businessChart('biz-delivery',delivery,businessDeliveryOption,()=>businessDeliveryChart(trend),'所选时间段暂无需求完成或 MR 合入',delivery.some(p=>p.completedIssues||p.mergedMrs),220);
  const cycleChart=businessChart('biz-cycle',delivery,businessCycleOption,()=>businessDeliveryChart(trend,true),'暂无可计算完成耗时的样本',delivery.some(p=>p.cycleSamples>0||p.executionSamples>0),220);
  const runsChart=businessChart('biz-runs',runs,businessRunsOption,()=>businessTrendChart(r.trend||[]),'所选时间段暂无运行数据',runs.some(p=>p.runs),180);
  const legend='<div class="biz-legend"><span><i class="business-color-0"></i>完成需求</span><span><i class="business-color-1"></i>合入 MR</span></div>';
  return `<div class="business-report biz-report">
    <section class="obs-kg biz-kg" aria-label="交付指标"><h4><span><b>${escapeHTML(businessScopeLabel())}</b> · ${escapeHTML(businessState.from||'不限开始日期')} 至 ${escapeHTML(businessState.to||'不限结束日期')}</span><span>${projects.length} 个项目 · 按项目当前归属统计</span></h4><div class="obs-ks">${kpis.map(businessKpiHTML).join('')}</div><div class="biz-kg-sub">${resources}</div></section>
    ${warnings.length?`<div class="biz-notice" role="note">${businessIcon('warning')}<div>${warnings.map(text=>`<p>${escapeHTML(text)}</p>`).join('')}</div></div>`:''}
    <div class="biz-grid2">${businessCard('biz-delivery','需求交付与 MR 合入',`${grainLabel}按各自完成、合入时间统计`,legend,deliveryChart)}${businessCard('biz-cycle','平均完成耗时趋势','按需求完成时间统计，缺失执行记录留空','<div class="biz-legend"><span><i class="business-color-0"></i>不含等待</span><span><i class="business-color-1"></i>含等待</span></div>',cycleChart)}</div>
    <div class="business-table-toolbar"><p>${singleDomain?'对比域内项目的交付结果与完成速度。':'对比业务与项目的交付结果，点击业务域查看域内项目。'}</p><span class="biz-sort"><span>排序</span><select id="businessReportSort" data-biz-select aria-label="项目与业务排序">${[['completed','完成需求最多'],['mrs','合入 MR 最多'],['execution','不含等待耗时最长'],['cycle','含等待耗时最长'],['merge','完成后合入耗时最长'],['tokens','Token 用量最多']].map(([key,label])=>`<option value="${key}" ${businessState.sort===key?'selected':''}>${label}</option>`).join('')}</select></span></div>
    ${singleDomain?'':businessPanel('业务交付对比','交付规模与完成速度',`${groups.length} 个业务分类`,table(['业务域',...heads],groupRows))}
    ${businessPanel('项目贡献',singleDomain?'所选业务域内各项目的交付结果':'按项目当前主业务域汇总交付结果',`${projects.length} 个项目`,table(['项目',...(singleDomain?[]:['主业务域']),...heads],projectRows))}
    <details class="biz-fold" data-biz-fold="resources"><summary>${businessIcon('chevron-right')}<b>运行与资源投入</b><span>${s.totalRuns||0} 次运行 · ${formatTokens(s.totalTokens)} Token</span></summary><div class="biz-fold-body biz-grid2 is-wide-left">${businessCard('biz-runs','运行趋势','按运行创建时间统计（UTC）','',runsChart)}${businessCard('biz-mix',singleDomain?'项目用量分布':'业务用量分布','占当前可查看范围的 Token 比例','',`<div class="business-mix-list">${distribution}</div>`)}</div></details>
    <details class="biz-fold biz-defs" data-biz-fold="definitions"><summary>${businessIcon('chevron-right')}<b>统计口径与数据覆盖</b></summary><div class="biz-fold-body"><p>完成需求包含需求与 Issue，按完成时间筛选。MR 按实际合入时间统计，包含集成分支，同一仓库的同一 MR 仅计一次。</p><p>含等待完成耗时从首次澄清或首次主流程创建至需求完成，包含排队、审批、暂停和跨日间隔；无有效起止时间的项不参与均值。不含等待完成耗时合并所有关联执行的实际运行时段，重试计入、并行时段和子流程不重复累计；任一次执行缺少计时记录时，该需求不参与此项均值。两项按各自有效样本求平均，样本数分别展示。</p><p>完成后合入耗时：每个 MR 对应执行的最新一次主流程成功完成至该 MR 实际合入，按仓库和 MR 编号去重。一个需求有多个 MR 时分别计算，各形成一个样本；按各自合入时间筛选，包含集成分支。完成前已合入、缺少完成事件或 MR 关联的项不参与均值，其他有效 MR 正常统计。</p><p>历史完成时间优先采用关闭 / 完成操作记录，否则按最后一次成功执行推定；普通编辑时间及 MR 检查时间不作为完成时间。需求按平台工作项去重。</p><p>单需求 Token 为期间完成项所有关联执行的累计用量除以完成项数；其他运行、用量、成本按期间创建的运行统计。成本统一以人民币统计：Hy 保留上报金额，其他模型按公开标准价测算；优先人民币价格，美元按 1 USD = 7 CNY 换算。缓存按分项计费，缺少模型或用量分项时为未识别；测算不代表实际结算账单。趋势时区：${escapeHTML(r.timeZone||'UTC')}。</p></div></details>
    <div class="business-report-foot">生成于 ${escapeHTML(new Date(r.generatedAt).toLocaleString())}</div>
  </div>`;
}
// Selector panels live on <body> and charts in the shared registry; both outlive an innerHTML re-render.
function businessDisposeView(host){
  host=host||$('#businessDomainsBody');
  if(host&&typeof obsDisposeCharts==='function')obsDisposeCharts(host);
  (businessState.selects||[]).forEach(api=>api.dispose?.());businessState.selects=[];
}
function businessMountCharts(root){
  if(!root||typeof obsMountCharts!=='function')return;
  obsMountCharts(root);
  root.querySelectorAll('[data-obs-chart]').forEach(el=>{const entry=obsChartEntry(el.dataset.obsChart);if(entry?.el===el&&!entry.chart.isDisposed?.())entry.chart.resize()});
}
function renderBusinessDomains(){
  const host=$('#businessDomainsBody');if(!host)return;const b=businessState;
  businessDisposeView(host);
  const report=b.loading?'<div class="biz-state is-loading" role="status"><span class="biz-spinner" aria-hidden="true"></span>正在查询业务数据…</div>':b.error?`<div class="biz-state is-error" role="alert">${businessIcon('alert')}<span>${escapeHTML(b.error)}</span></div>`:businessReportHTML();
  const scopes=`<option value="all">全部可查看业务</option><option value="unclassified" ${b.scope==='unclassified'?'selected':''}>未分类</option>${b.items.filter(x=>x.canView).map(x=>`<option value="${escapeHTML(x.domain.id)}" ${b.scope===x.domain.id?'selected':''}>${escapeHTML(x.domain.name)}${x.domain.active?'':'（已停用）'}</option>`).join('')}`;
  host.innerHTML=`<form id="businessReportFilter" class="business-filters biz-fbar"><div class="business-periods" role="group" aria-label="时间段">${[7,30,90].map(days=>`<button type="button" data-business-days="${days}" class="${b.rangeDays===days?'active':''}">${days} 天</button>`).join('')}<button id="businessAllTime" type="button" class="${!b.from&&!b.to?'active':''}">全部</button></div><span class="biz-field"><select name="scope" data-biz-select data-icon="layers" aria-label="业务范围">${scopes}</select></span><input type="date" name="from" value="${escapeHTML(b.from)}" aria-label="开始日期"><span class="business-filter-arrow" aria-hidden="true">→</span><input type="date" name="to" value="${escapeHTML(b.to)}" aria-label="结束日期"><button class="button ghost" type="submit">应用筛选</button></form>${report}`;
  b.selects=[...host.querySelectorAll('select[data-biz-select]')].map(sel=>window.enhanceSelect?.(sel)).filter(Boolean);
  window.AgentFlowSlidingTabs?.mount?.(host.querySelector('.business-periods'),{activeSelector:'.active',cacheKey:'business-periods'});
  $('#businessExport').disabled=!b.report||b.loading;$('#businessExport').onclick=exportBusinessReport;$('#businessRefresh').onclick=loadBusinessDomains;
  $('#businessReportFilter').onsubmit=e=>{e.preventDefault();const f=e.currentTarget;b.scope=f.elements.scope.value;b.from=f.elements.from.value;b.to=f.elements.to.value;b.rangeDays=null;refreshBusinessReport()};
  const sort=host.querySelector('#businessReportSort');if(sort)sort.onchange=()=>{b.sort=sort.value;renderBusinessDomains();b.selects.find(api=>api.select.id==='businessReportSort')?.trigger.focus()};
  host.querySelectorAll('[data-business-scope]').forEach(button=>button.onclick=()=>{b.scope=button.dataset.businessScope;refreshBusinessReport()});
  $('#businessAllTime').onclick=()=>{b.scope=$('#businessReportFilter').elements.scope.value;b.from='';b.to='';b.rangeDays=null;refreshBusinessReport()};
  host.querySelectorAll('[data-business-days]').forEach(button=>button.onclick=()=>{b.scope=$('#businessReportFilter').elements.scope.value;const to=new Date(),from=new Date(to);from.setDate(from.getDate()-Number(button.dataset.businessDays)+1);b.from=businessDate(from);b.to=businessDate(to);b.rangeDays=Number(button.dataset.businessDays);refreshBusinessReport()});
  host.querySelectorAll('details[data-biz-fold]').forEach(details=>details.addEventListener('toggle',()=>{if(details.open)businessMountCharts(details)}));
  window.AgentFlowHarnessIcons?.hydrate?.(host);
  businessMountCharts(host);
}
// The exported file is opened offline: tokens are inlined as computed values and charts as static SVG.
const BUSINESS_EXPORT_TOKENS=['--hui-accent','--hui-accent-hover','--hui-accent-soft','--hui-text','--hui-muted','--hui-secondary','--hui-tertiary','--hui-border','--hui-border-soft','--hui-border-strong','--hui-canvas','--hui-soft','--hui-surface','--hui-stage','--hui-panel-shadow','--hui-ind-shadow','--hui-info','--hui-success','--hui-error','--hui-warn','--hui-neutral','--hui-ease-flow','--hui-sans','--hui-mono','--c1','--c2','--c3','--c4'];
function businessExportTokens(view){
  if(!view||typeof getComputedStyle!=='function')return '';
  const css=getComputedStyle(view);
  return BUSINESS_EXPORT_TOKENS.map(name=>[name,String(css.getPropertyValue(name)||'').trim()]).filter(([,value])=>value&&!/[<>{};]/.test(value)).map(([name,value])=>`${name}:${value}`).join(';');
}
function businessStaticChart(ec,spec,width,height){
  const chart=ec.init(null,null,{renderer:'svg',ssr:true,width,height});
  try{chart.setOption({...spec.option(obsEChartsTheme(),true),animation:false},true);return chart.renderToSVGString()}finally{chart.dispose()}
}
async function businessExportCharts(root){
  const slots=[...root.querySelectorAll('[data-obs-chart]')];if(!slots.length)return;
  const ec=typeof obsEnsureECharts==='function'?await obsEnsureECharts():null;
  for(const el of slots){
    const key=el.dataset.obsChart,spec=obsChartSpecs[key],live=document.querySelector(`#businessDomainsBody [data-obs-chart="${key}"]`);
    el.removeAttribute('data-obs-chart');
    if(!spec||spec.empty)continue;
    const height=parseInt(el.style.height,10)||220,width=Math.round(live?.clientWidth||0)||640;
    let svg='';
    if(ec)try{svg=businessStaticChart(ec,spec,width,height)}catch(e){svg=''}
    if(svg)el.classList.add('is-static');
    else{try{svg=spec.fallback?.()||''}catch(e){svg=''}el.classList.add('is-fallback');el.style.height='auto'}
    el.innerHTML=svg||'<div class="obs-chart-empty">图表暂不可用</div>';
  }
}
async function exportBusinessReport(){
  if(!businessState.report||businessState.loading)return;
  const button=$('#businessExport');button.disabled=true;
  try{
    const [base,harness]=await Promise.all(['/business-domains.css','/app/business-harness.css'].map(url=>fetch(url).then(r=>{if(!r.ok)throw new Error('报告样式加载失败');return r.text()})));
    const view=$('#businessDomainsView'),tpl=document.createElement('template');
    tpl.innerHTML=businessReportHTML();
    const content=tpl.content,sort=content.querySelector('.biz-sort'),select=sort?.querySelector('select');
    if(sort)sort.textContent=`排序：${select?.options[select.selectedIndex]?.text||''}`;
    content.querySelectorAll('details').forEach(details=>details.setAttribute('open',''));
    window.AgentFlowHarnessIcons?.hydrate?.(content);
    await businessExportCharts(content);
    const canvas=view?String(getComputedStyle(view).getPropertyValue('--hui-canvas')||'').trim():'',title=`${escapeHTML(businessScopeLabel())} · 业务报告`;
    const html=`<!doctype html><html lang="zh-CN" data-theme="${document.documentElement.dataset.theme==='dark'?'dark':'light'}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>${title}</title><style>${base}\n${harness}\n#businessDomainsView{${businessExportTokens(view)}}\nhtml,body{margin:0;background:${canvas&&!/[<>{};]/.test(canvas)?canvas:'#fafafb'}}</style></head><body><div id="businessDomainsView" class="biz-export"><div class="page-heading"><div><h1>${title}</h1><p>跨项目查看交付规模、完成耗时与资源投入，按项目当前业务归属统计。</p></div></div>${tpl.innerHTML}</div></body></html>`;
    const url=URL.createObjectURL(new Blob([html],{type:'text/html;charset=utf-8'})),a=document.createElement('a');a.href=url;a.download=`业务报告-${businessDate(new Date())}.html`;a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);toast('报告已导出');
  }catch(e){toast(e.message)}
  finally{button.disabled=!businessState.report||businessState.loading}
}

function canAccessPlatformManagement(){
  const uid=Number(state.currentUser?.id)||0;
  return isPlatformAdmin()||!state.authRequired||uid>0&&(state.data?.businessDomains||[]).some(d=>(d.managerIds||[]).includes(uid));
}
async function loadBusinessDomainManagement(){
  const host=$('#platformBusinessDomains');if(!host)return;
  host.innerHTML='<div class="business-card">正在加载业务域…</div>';
  try{
    const access=await api('/api/business-domains',{silent:true});
    businessState.items=access.items;businessState.canAdmin=access.canAdmin;businessState.userId=access.userId;
    state.data.businessDomains=access.items.map(x=>x.domain);
    businessState.report=null;businessState.seq++;
    renderBusinessDomainManagement();renderPlatformNav();
  }catch(e){host.innerHTML=`<div class="business-card business-error">${escapeHTML(e.message)}</div>`}
}
const businessManagementDrafts=new Map();
function businessManagementUserChoices(name,ids,disabled=false){
  const label=name==='managerIds'?'业务域管理员':'汇报查看者';
  return `<div class="business-user-picker" data-user-picker="${name}" data-selected="${escapeHTML(JSON.stringify(ids))}" data-disabled="${disabled}"><div class="business-selected-users"></div>${disabled?'':`<input type="search" class="business-user-search" aria-label="搜索${label}" placeholder="输入姓名或账号搜索" autocomplete="off"><div class="business-user-results" aria-label="${label}搜索结果"></div>`}<small class="business-user-selection-count" aria-live="polite"></small></div>`;
}
function bindBusinessUserPickers(host){
  const pickers=[...host.querySelectorAll('[data-user-picker]')].map(el=>({el,name:el.dataset.userPicker,ids:new Set(JSON.parse(el.dataset.selected).map(Number)),disabled:el.dataset.disabled==='true'}));
  const users=(state.data?.users||[]).map(user=>({user,search:`${user.chineseName||''} ${user.loginName||''}`.normalize('NFKC').toLocaleLowerCase()}));
  function renderSelected(picker){
    const el=picker.el;
    el.querySelector('.business-selected-users').innerHTML=[...picker.ids].map(id=>`<span class="business-user-chip"><input type="hidden" name="${picker.name}" value="${id}"><span class="business-user-avatar business-chip-avatar">${userAvatarHTML(userById(id),48)}</span><span>${escapeHTML(businessUserName(id))}</span>${picker.disabled?'':`<button type="button" data-remove-user="${id}" aria-label="移除${escapeHTML(businessUserName(id))}">×</button>`}</span>`).join('')||'<span class="business-no-selection">尚未选择</span>';
    el.querySelector('.business-user-selection-count').textContent=`已选择 ${picker.ids.size} 人${picker.disabled?' · 由平台管理员维护':''}`;
    el.querySelectorAll('[data-remove-user]').forEach(button=>button.onclick=()=>{picker.ids.delete(Number(button.dataset.removeUser));renderSelected(picker);pickers.forEach(renderResults)});
  }
  function renderResults(picker){
    if(picker.disabled)return;
    const input=picker.el.querySelector('.business-user-search'),result=picker.el.querySelector('.business-user-results'),query=input.value.trim().normalize('NFKC').toLocaleLowerCase();
    if(!query){result.innerHTML='<small>输入姓名或账号查找，可连续添加多位用户。</small>';return}
    const matches=users.filter(x=>x.search.includes(query)&&!picker.ids.has(Number(x.user.id))),shown=matches.slice(0,20),other=pickers.find(x=>x!==picker);
    result.innerHTML=shown.map(({user:u})=>{const assigned=other?.ids.has(Number(u.id));return `<button type="button" class="business-user-result" data-add-user="${Number(u.id)}" ${assigned?'disabled':''}><span class="business-user-avatar">${userAvatarHTML(u,48)}</span><span><b>${escapeHTML(u.chineseName||u.loginName)}</b><small>${escapeHTML(u.loginName)}</small></span><span class="business-user-add">${assigned?'已在另一角色':'＋ 添加'}</span></button>`}).join('')+(matches.length>20?`<small>共 ${matches.length} 个匹配，显示前 20 个，请继续输入缩小范围。</small>`:matches.length?'':'<small>没有匹配的未选用户，试试其他姓名或账号。</small>');
    result.querySelectorAll('[data-add-user]').forEach(button=>button.onclick=()=>{picker.ids.add(Number(button.dataset.addUser));input.value='';renderSelected(picker);pickers.forEach(renderResults);input.focus()});
  }
  pickers.forEach(picker=>{
    renderSelected(picker);renderResults(picker);
    const input=picker.el.querySelector('.business-user-search');if(!input)return;
    input.oninput=()=>renderResults(picker);
    input.onkeydown=e=>{if(e.key==='Enter'){e.preventDefault();picker.el.querySelector('[data-add-user]:not(:disabled)')?.focus()}else if(e.key==='ArrowDown'){e.preventDefault();picker.el.querySelector('[data-add-user]:not(:disabled)')?.focus()}else if(e.key==='Escape'){input.value='';renderResults(picker)}};
  });
}
function captureBusinessManagementDraft(){
  const f=$('#businessDomainForm');if(!f)return;
  const b=businessState,base=businessManagementDrafts.get(b.editorId)||b.items.find(x=>x.domain.id===b.editorId)?.domain||{active:true,managerIds:[],viewerIds:[]};
  const selected=name=>[...f.querySelectorAll(`input[type="hidden"][name="${name}"]`)].map(x=>Number(x.value));
  const value={...base,id:f.elements.id.value.trim(),name:f.elements.name.value.trim(),description:f.elements.description.value,active:b.canAdmin?f.elements.active.checked:base.active,managerIds:b.canAdmin?selected('managerIds'):base.managerIds,viewerIds:selected('viewerIds')};
  businessManagementDrafts.set(b.editorId,value);return value;
}
function renderBusinessDomainManagement(){
  const host=$('#platformBusinessDomains');if(!host)return;
  const b=businessState,editable=b.items.filter(x=>x.canManage);
  if(!b.canAdmin&&!editable.length){host.innerHTML='<div class="business-card">没有可管理的业务域。</div>';return}
  if(b.editorId===null||b.editorId&&!editable.some(x=>x.domain.id===b.editorId))b.editorId=editable[0]?.domain.id??'';
  const existing=editable.find(x=>x.domain.id===b.editorId),draft=businessManagementDrafts.get(b.editorId)||existing?.domain||{id:'',name:'',description:'',active:true,managerIds:[],viewerIds:[]};
  host.innerHTML=`<div class="business-management-layout"><aside class="business-catalog"><div class="business-catalog-head"><div><b>业务域</b><small>${editable.length} 个可管理</small></div>${b.canAdmin?'<button id="businessNew" class="button ghost small" type="button">＋ 新建</button>':''}</div><div class="business-catalog-list">${editable.map(x=>`<button type="button" class="business-catalog-item ${b.editorId===x.domain.id?'selected':''}" data-business-domain="${escapeHTML(x.domain.id)}" aria-pressed="${b.editorId===x.domain.id}"><span class="business-domain-avatar">${escapeHTML(x.domain.name.slice(0,1))}</span><span class="business-domain-item-text"><b>${escapeHTML(x.domain.name)}</b><small>${escapeHTML(x.domain.id)}</small></span><span class="business-status ${x.domain.active?'enabled':''}">${x.domain.active?'启用':'停用'}</span></button>`).join('')||'<p class="business-catalog-empty">还没有业务域，点击新建开始。</p>'}</div></aside><section class="business-editor"><header class="business-editor-head"><div><small>${existing?'编辑业务域':'创建业务域'}</small><h2>${escapeHTML(existing?.domain.name||'新建业务域')}</h2></div>${existing?`<span class="business-status ${existing.domain.active?'enabled':''}">${existing.domain.active?'启用中':'已停用'}</span>`:''}</header><form id="businessDomainForm" class="business-form"><div class="business-form-section business-wide"><h3>业务信息</h3><div class="business-form-grid"><label>业务域名称<input name="name" value="${escapeHTML(draft.name)}" required maxlength="100" placeholder="例如：DNS 控制台"></label><label>业务域标识<input name="id" value="${escapeHTML(draft.id)}" ${existing?'readonly':''} required maxlength="64" pattern="[a-z0-9][a-z0-9_-]{0,63}" placeholder="例如：web-console"><small>${existing?'创建后不可修改':'使用小写字母、数字、下划线或短横线'}</small></label><label class="business-wide">业务说明<textarea name="description" maxlength="2000" placeholder="说明业务范围，便于项目选择归属">${escapeHTML(draft.description)}</textarea></label></div></div><div class="business-form-section business-wide"><h3>管理与查看权限</h3><p>管理员维护本域信息与查看者；查看者可查看本域汇总报告。</p><div class="business-form-grid"><div><div class="business-choice-heading">业务域管理员${b.canAdmin?'':'<small>由平台管理员配置</small>'}</div>${businessManagementUserChoices('managerIds',draft.managerIds||[],!b.canAdmin)}</div><div><div class="business-choice-heading">汇报查看者</div>${businessManagementUserChoices('viewerIds',draft.viewerIds||[])}</div></div></div><div class="business-form-section business-wide business-state-section"><div><h3>业务域状态</h3><p>停用后保留项目和统计，禁止新增归属。</p></div><label class="business-active"><input type="checkbox" name="active" ${draft.active?'checked':''} ${b.canAdmin?'':'disabled'}>启用业务域</label></div><footer class="business-editor-footer business-wide"><span id="businessSaveStatus" role="status" aria-live="polite">${existing?'修改后点击保存生效':'创建后，可在项目配置中设置归属'}</span><button class="button primary" type="submit">${existing?'保存修改':'创建业务域'}</button></footer></form></section></div>`;
  bindBusinessUserPickers(host);
  host.querySelectorAll('[data-business-domain]').forEach(button=>button.onclick=()=>{captureBusinessManagementDraft();b.editorId=button.dataset.businessDomain;renderBusinessDomainManagement()});
  const create=host.querySelector('#businessNew');if(create)create.onclick=()=>{captureBusinessManagementDraft();b.editorId='';renderBusinessDomainManagement();host.querySelector('[name="name"]').focus()};
  host.querySelector('#businessDomainForm').onsubmit=async e=>{
    e.preventDefault();const value=captureBusinessManagementDraft(),btn=e.currentTarget.querySelector('[type=submit]'),status=host.querySelector('#businessSaveStatus');
    if(!existing&&b.items.some(x=>x.domain.id===value.id)){status.textContent='业务域标识已存在，请使用新的标识';status.classList.add('business-error');return}
    btn.disabled=true;status.textContent='正在保存…';status.classList.remove('business-error');
    try{await api('/api/business-domains'+(existing?'':'?createOnly=true'),{method:'PUT',body:JSON.stringify(value)});businessManagementDrafts.delete(b.editorId);b.editorId=value.id;await loadBusinessDomainManagement();toast('业务域已保存')}
    catch(err){btn.disabled=false;status.textContent=err.message;status.classList.add('business-error')}
  };
}
