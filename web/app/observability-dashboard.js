// Compact statistics panels using the existing Harness filters and card styles.
function obsSummaryStats(){
  const dashboard=state.analytics?.dashboard;
  if(!dashboard)return null;
  if(obsTrendGrain()==='all')return {...dashboard,delivered:dashboard.deliveredIssues||0};
  const point=(dashboard.trend||[]).find(p=>p.date===obsPeriodKey())||{};
  return {...point,delivered:Number(point.sopDelivered||0)+Number(point.vibeDelivered||0)};
}
function obsSummaryValues(items,klass=''){
  return `<div class="obs-summary-values ${klass}">${items.map(([key,label,value,tip])=>`<div class="obs-kpi" data-obs-stat="${key}"${tip?` title="${escapeHTML(tip)}" tabindex="0"`:''}><div class="obs-kpi-label">${escapeHTML(label)}</div><div class="obs-kpi-value"><b class="num" data-obs-count>${escapeHTML(value)}</b></div></div>`).join('')}</div>`;
}
function obsSummaryLegend(kind){
  return `<div class="obs-human-legend">${obsDashboardSeries(kind,[]).map((s,i)=>`<span><i class="${i===1?'blue':i===2?'amber':''}"></i>${escapeHTML(s.name)}</span>`).join('')}</div>`;
}
function obsSummaryCard(){
  const stats=obsSummaryStats(),attempts=Number(stats?.succeededRuns||0)+Number(stats?.failedRuns||0);
  const metrics=obsSummaryValues([
    ['delivered',lfT('已交付需求','Delivered issues'),stats?String(stats.delivered):'—'],
    ['sop-success',lfT('SOP 成功率','SOP success rate'),attempts?Math.round(Number(stats.succeededRuns||0)*100/attempts)+'%':'—'],
    ['human-rate',lfT('SOP 人工介入率','SOP intervention rate'),stats?.sopRuns?Math.round(Number(stats.humanRuns||0)*100/stats.sopRuns)+'%':'—'],
  ]);
  const points=obsTrendWindow(state.analytics?.dashboard?.trend||[]),spec=obsDashboardSpec('delivery',points);
  spec.empty=!points.some(p=>p.sopDelivered||p.vibeDelivered);spec.emptyText=lfT('暂无交付','No deliveries');
  return obsCard('summary',lfT('总览','Overview'),'', '',`<div class="obs-summary-overview">${metrics}<div>${obsChartSlot('dashboard-delivery',spec,156)}${obsSummaryLegend('delivery')}</div></div>`);
}
function obsVibeCard(){
  const spec=obsVibeSpec(),tabs=obsSeg('data-obs-vibe-view',[['activity',lfT('会话','Conversations')],['outcomes',lfT('产出','Outcomes')]],obsVibeView(),true);
  return obsCard('vibe','Vibe Coding','',tabs,`<div class="obs-vibe-values">${obsVibeValues()}</div>${obsChartSlot('dashboard-vibe',spec,192)}<div class="obs-vibe-legend"${obsVibeView()==='outcomes'?' hidden':''}>${obsSummaryLegend('vibe')}</div>`);
}
function obsVibeView(){return state.obsVibeView==='outcomes'?'outcomes':'activity'}
function obsCodingStats(){
  const stats=obsSummaryStats();
  if(stats?.coding)return stats.coding;
  if(obsTrendGrain()!=='all')return {issues:obsScopedView().conversationIssues,withMr:null,delivered:null};
  const summary=state.analytics?.summary||{};
  return {issues:summary.conversationIssues||0,withMr:summary.conversationIssuesWithMr,delivered:summary.conversationIssuesDelivered};
}
function obsVibeValues(){
  if(obsVibeView()==='outcomes'){
    const coding=obsCodingStats(),n=key=>coding[key]==null?'—':String(coding[key]);
    return obsSummaryValues([
      ['coding-issues',lfT('编码需求','Coding issues'),n('issues')],
      ['coding-pr',lfT('已提 PR 需求','Issues with PRs'),n('withMr')],
      ['coding-delivered',lfT('已交付需求','Delivered issues'),n('delivered')],
    ]);
  }
  const activity=obsActivityView(),view=obsScopedView();
  const n=key=>activity?String(activity[key]||0):'—';
  return obsSummaryValues([
    ['sessions',lfT('会话数','Sessions'),n('sessions')],
    ['collaborative',lfT('协作会话','Collaborative sessions'),n('collaborativeSessions')],
    ['session-tokens',lfT('会话 Token','Conversation tokens'),obsActivityUsageCard(activity,false).value],
    ['workspaces',lfT('编码工作区','Coding workspaces'),String(view.conversationWorkspaces||0)],
  ],'is-vibe');
}
function obsCodingRows(){const coding=obsCodingStats();return [
  {name:lfT('编码需求','Coding issues'),value:coding.issues||0},
  {name:lfT('已提 PR','With PRs'),value:coding.withMr||0},
  {name:lfT('已交付','Delivered'),value:coding.delivered||0},
]}
function obsCodingOption(rows,opts){
  const o=obsChartOpts(opts),t=o.theme;
  return obsChartBase(o,{
    grid:{left:6,right:48,top:10,bottom:10,containLabel:true},xAxis:obsValueAxis(t,String,{min:0,show:false}),
    yAxis:{type:'category',inverse:true,data:rows.map(r=>r.name),axisLine:{show:false},axisTick:{show:false},axisLabel:{color:t.muted,fontSize:12}},
    tooltip:{...obsTooltipBase(t),trigger:'item',formatter:p=>obsTooltipRows(rows[p.dataIndex]?.name||'',[[lfT('需求数','Issues'),String(rows[p.dataIndex]?.value||0)],[lfT('占编码需求','Share of coding issues'),(rows[0]?.value?(rows[p.dataIndex]?.value||0)*100/rows[0].value:0).toFixed(1)+'%']])},
    series:[{type:'bar',barMaxWidth:22,showBackground:true,backgroundStyle:{color:t.panel2},data:rows.map((r,i)=>({value:r.value,itemStyle:{color:i===2?t.c1:t.c2}})),label:{show:true,position:'right',color:t.text,fontSize:13},animationDurationUpdate:o.reduced?0:350}],
  });
}
function obsVibeSpec(){
  if(obsVibeView()==='outcomes'){
    const rows=obsCodingRows(),coding=obsCodingStats(),available=coding.withMr!=null&&coding.delivered!=null;
    return {empty:!rows[0].value||!available,emptyText:lfT('暂无编码产出统计','No coding outcomes'),option:(theme,reduced)=>obsCodingOption(rows,{theme:obsDashboardPalette(theme),reduced}),fallback:()=>`<div class="obs-token-table"><table class="obs-table"><thead><tr><th>${lfT('阶段','Stage')}</th><th>${lfT('需求数','Issues')}</th></tr></thead><tbody>${rows.map(r=>`<tr><td>${escapeHTML(r.name)}</td><td>${r.value}</td></tr>`).join('')}</tbody></table></div>`};
  }
  const points=obsTrendWindow(state.analytics?.activityTrend||[]),spec=obsDashboardSpec('vibe',points);
  spec.empty=!points.some(p=>p.sessions||p.collaborativeSessions);spec.emptyText=lfT('暂无会话','No sessions');
  return spec;
}
function obsSummaryCostCard(){
  const view=obsScopedView();
  const values=obsSummaryValues([
    ['tokens',lfT('Token 用量','Tokens'),formatTokens(view.tokens)],
    ['cost',lfT('费用（人民币）','Cost (CNY)'),view.costKnown?'¥'+Number(view.cost||0).toFixed(2):lfT('未识别','Unknown')],
  ]);
  const tabs=obsSeg('data-obs-token-view',[
    ['members',lfT('成员每日','Members by day')],['models',lfT('各模型','By model')],
  ],obsTokenView(),true);
  return obsCard('cost',lfT('用量与费用','Usage & cost'),'',values,`<div class="obs-token-tabs">${tabs}</div>${obsChartSlot('dashboard-tokens',obsTokenSpec(),240)}`,{klass:'obs-summary-cost',expand:true});
}
function obsDashboardSeries(kind,points,metric){
  const defs=kind==='delivery'?[[lfT('SOP 交付','SOP deliveries'),'sopDelivered'],[lfT('Vibe Coding 交付','Vibe Coding deliveries'),'vibeDelivered']]:kind==='vibe'?[[lfT('会话','Sessions'),'sessions'],[lfT('协作会话','Collaborative sessions'),'collaborativeSessions']]:[[lfT('审批 / 放行','Approved / passed'),'approvals'],[lfT('驳回','Rejected'),'rejections'],[lfT('重试 / 拒绝续跑','Retry / declined'),'retries']];
  return defs.map(([name,key])=>({name,key,values:points.map(p=>kind==='human'&&metric!=='count'?(p.sopRuns?Number(p[key]||0)*100/p.sopRuns:0):Number(p[key]||0))}));
}
function obsDashboardPalette(theme){
  const css=typeof getComputedStyle==='function'?getComputedStyle(document.documentElement):null;
  const read=(name,fallback)=>css?.getPropertyValue(name).trim()||fallback;
  return {...theme,c1:read('--hui-accent',theme.c1),c2:read('--hui-info',theme.c2),amber:read('--hui-warn',theme.amber),text:read('--hui-text',theme.text),muted:read('--hui-muted',theme.muted),muted2:read('--hui-muted',theme.muted2),line:read('--hui-border',theme.line),panel:read('--hui-surface',theme.panel),panel3:read('--hui-surface',theme.panel3)};
}
function obsDashboardOption(kind,points,opts={}){
  const o=obsChartOpts(opts),t=o.theme,metric=opts.metric||'normalized',series=obsDashboardSeries(kind,points,metric),line=kind==='vibe';
  const colors=[t.c1,t.c2,t.amber];
  const unit=kind==='delivery'?lfT('个','issues'):kind==='vibe'?lfT('个','sessions'):metric!=='count'?lfT('次 / 百次运行','per 100 runs'):lfT('次','events');
  return obsChartBase(o,{
    animationDuration:o.reduced?0:500,animationDurationUpdate:o.reduced?0:350,
    grid:{left:8,right:12,top:25,bottom:8,containLabel:true},
    xAxis:obsCategoryAxis(t,points.map(obsTrendLabel),{boundaryGap:!line,axisLabel:{color:t.muted,fontSize:11,hideOverlap:true}}),
    yAxis:obsValueAxis(t,v=>String(Math.round(v)),{min:0,minInterval:1,name:kind==='human'?'':unit,nameTextStyle:{color:t.muted,fontSize:11},splitLine:{lineStyle:{color:t.line}}}),
    tooltip:{...obsTooltipBase(t),trigger:'axis',axisPointer:{type:line?'line':'shadow'},formatter:params=>{
      const p=points[params?.[0]?.dataIndex];if(!p)return '';
      const i=params[0].dataIndex;
      return obsTooltipRows(p.date,series.map(s=>[s.name,(kind==='human'&&metric!=='count'?s.values[i].toFixed(1):s.values[i])+' '+unit]));
    }},
    series:series.map((s,i)=>({id:'dashboard-'+kind+'-'+s.key,name:s.name,type:line?'line':'bar',stack:line?undefined:'total',data:s.values,
      barMaxWidth:30,symbol:'circle',symbolSize:4.5,showSymbol:points.length<35,
      lineStyle:{color:colors[i],width:i?1.8:2.2,type:i?'dashed':'solid'},itemStyle:{color:colors[i]},
      emphasis:{focus:'series'},animationDelay:o.reduced?0:idx=>Math.min(idx*18,200)})),
  });
}
function obsDashboardFallback(kind,points){
  const slot=document.querySelector('[data-obs-chart="dashboard-'+kind+'"]'),w=slot?.clientWidth||480,h=220,l=34,r=12,t=20,b=28,pw=w-l-r,ph=h-t-b;
  const series=obsDashboardSeries(kind,points,state.obsDashboardMetric),line=kind==='vibe',colors=['var(--hui-accent)','var(--hui-info)','var(--hui-warn)'];
  const peak=Math.max(1,...points.map((_,i)=>line?Math.max(...series.map(s=>s.values[i])):series.reduce((n,s)=>n+s.values[i],0))),max=Math.ceil(peak/4)*4,x=i=>l+(i+.5)*pw/Math.max(1,points.length),y=v=>t+ph*(1-v/max);
  let html=`<svg width="${w}" height="${h}" role="img" aria-label="${obsEsc(kind==='delivery'?lfT('交付趋势','Delivery trend'):kind==='vibe'?'Vibe Coding':lfT('SOP 人工介入趋势','SOP intervention trend'))}">`;
  for(let i=0;i<=4;i++){const value=max*i/4;html+=`<line x1="${l}" y1="${y(value)}" x2="${w-r}" y2="${y(value)}" stroke="var(--hui-border)"/><text x="${l-7}" y="${y(value)+4}" text-anchor="end">${value}</text>`}
  points.forEach((p,i)=>{if(i===0||i===points.length-1||i===Math.floor(points.length/2))html+=`<text x="${x(i)}" y="${h-5}" text-anchor="middle">${obsEsc(obsTrendLabel(p))}</text>`});
  if(line)series.forEach((s,g)=>{html+=`<polyline points="${s.values.map((v,i)=>x(i)+','+y(v)).join(' ')}" fill="none" stroke="${colors[g]}" stroke-width="2" ${g?'stroke-dasharray="5 3"':''}/>`});
  else points.forEach((_,i)=>{let base=0;series.forEach((s,g)=>{const v=s.values[i],bw=Math.min(30,pw/points.length*.65);html+=`<rect x="${x(i)-bw/2}" y="${y(base+v)}" width="${bw}" height="${Math.max(0,y(base)-y(base+v))}" fill="${colors[g]}"/>`;base+=v})});
  return html+'</svg>';
}
function obsDashboardSpec(kind,points){return {option:(theme,reduced)=>obsDashboardOption(kind,points,{theme:obsDashboardPalette(theme),reduced,metric:state.obsDashboardMetric}),fallback:()=>obsDashboardFallback(kind,points),...(kind!=='human'?{on:{click:p=>{if(points[p.dataIndex])obsSelectTrendPeriod(points[p.dataIndex].date)}}}:{})}}

function obsHumanPoints(){
  const snapshot=state.analytics||{},d=snapshot.dashboard;
  if(!d)return [];
  const base=new Map((d.trend||[]).map(p=>[p.date,p]));
  for(const p of snapshot.trend||[])if(!base.has(p.date))base.set(p.date,{date:p.date,sopRuns:0});
  const counts=new Map();
  for(const p of d.humanTrend||[]){
    if(state.obsDashboardNode&&p.node!==state.obsDashboardNode)continue;
    const row=counts.get(p.date)||{approvals:0,rejections:0,retries:0};
    for(const key of ['approvals','rejections','retries'])row[key]+=Number(p[key]||0);
    counts.set(p.date,row);
  }
  return obsTrendWindow([...base.values()].sort((a,b)=>a.date.localeCompare(b.date))).map(p=>({...p,...counts.get(p.date)}));
}
function obsHumanTrendCard(){
  const d=state.analytics?.dashboard;
  const nodes=d?.humanNodes||[];
  if(!nodes.some(n=>n.key===state.obsDashboardNode))state.obsDashboardNode='';
  const node=`<select id="obsHumanNode" data-obs-select aria-label="${lfT('人工介入节点','Intervention node')}"><option value="">${lfT('全部节点','All nodes')}</option>${nodes.map(n=>`<option value="${escapeHTML(n.key)}"${n.key===state.obsDashboardNode?' selected':''}>${escapeHTML(n.name)}</option>`).join('')}</select>`;
  const metric=obsSeg('data-obs-human-metric',[
    ['normalized',lfT('每百次运行','Per 100 runs')],['count',lfT('介入次数','Decisions')],
  ],state.obsDashboardMetric||'normalized',true);
  const performance=obsSOPView()==='performance',tabs=obsSeg('data-obs-sop-view',[['human',lfT('人工介入','Interventions')],['performance',lfT('执行效率','Performance')]],obsSOPView(),true);
  return obsCard('human',performance?lfT('SOP 执行效率','SOP performance'):lfT('SOP 人工介入点趋势','SOP intervention trend'),'',tabs,`<div class="obs-human-controls"${performance?' hidden':''}>${node}${metric}</div><div class="obs-sop-values"${performance?'':' hidden'}>${performance?obsSOPValues():''}</div>${obsChartSlot('dashboard-human',obsSOPSpec(),196)}<div class="obs-sop-legend">${obsSOPLegend()}</div>`,{expand:true});
}
function obsSOPView(){return state.obsSOPView==='performance'?'performance':'human'}
function obsSOPValues(){
  const stats=obsSummaryStats(),p=stats?.performance||{},ok=Number(p.toolCallsSucceeded||0),bad=Number(p.toolCallsFailed||0);
  return obsSummaryValues([
    ['sop-runs',lfT('SOP 运行','SOP runs'),stats?String(stats.sopRuns||0):'—',stats?lfT(`成功 ${stats.succeededRuns||0} / 失败 ${stats.failedRuns||0}`,`${stats.succeededRuns||0} succeeded / ${stats.failedRuns||0} failed`):''],
    ['sop-duration',lfT('平均执行耗时','Mean execution time'),p.measuredRuns?duration(p.averageExecutionDurationMs):'—',p.measuredRuns?`P95 ${duration(p.p95ExecutionDurationMs)}`:''],
    ['tool-success',lfT('工具成功率','Tool success rate'),ok+bad?Math.round(ok*100/(ok+bad))+'%':'—',ok+bad?lfT(`成功 ${ok} / 失败 ${bad}`,`${ok} succeeded / ${bad} failed`):''],
  ]);
}
function obsSOPLegend(){
  return obsSOPView()==='human'?obsSummaryLegend('human'):`<div class="obs-human-legend"><span><i></i>${lfT('平均执行耗时','Mean execution time')}</span><span><i class="blue"></i>${lfT('P95 执行耗时','P95 execution time')}</span></div>`;
}
function obsPerformanceOption(points,opts){
  const o=obsChartOpts(opts),t=o.theme;
  return obsChartBase(o,{
    grid:{left:6,right:12,top:18,bottom:6,containLabel:true},xAxis:obsCategoryAxis(t,points.map(obsTrendLabel),{boundaryGap:false}),
    yAxis:obsValueAxis(t,v=>v?duration(v):'0',{min:0}),
    tooltip:{...obsTooltipBase(t),trigger:'axis',formatter:params=>{
      const point=points[params?.[0]?.dataIndex];if(!point)return '';
      const p=point.performance||{},ok=Number(p.toolCallsSucceeded||0),bad=Number(p.toolCallsFailed||0);
      return obsTooltipRows(point.date,[[lfT('平均执行耗时','Mean execution time'),p.measuredRuns?duration(p.averageExecutionDurationMs):'—'],['P95',p.measuredRuns?duration(p.p95ExecutionDurationMs):'—'],[lfT('有耗时记录的运行','Measured runs'),String(p.measuredRuns||0)],[lfT('工具成功率','Tool success rate'),ok+bad?(ok*100/(ok+bad)).toFixed(1)+'%':'—']]);
    }},
    series:[['averageExecutionDurationMs',lfT('平均执行耗时','Mean execution time'),t.c1],['p95ExecutionDurationMs','P95',t.c2]].map(([metric,name,color])=>({id:metric,name,type:'line',data:points.map(p=>p.performance?.measuredRuns?Number(p.performance[metric]||0):null),connectNulls:false,itemStyle:{color},lineStyle:{color,width:2,type:metric==='p95ExecutionDurationMs'?'dashed':'solid'},symbol:'circle',symbolSize:4,animationDurationUpdate:o.reduced?0:350})),
  });
}
function obsSOPSpec(){
  if(obsSOPView()==='performance'){
    const points=obsTrendWindow(state.analytics?.dashboard?.trend||[]);
    return {empty:!points.some(p=>p.performance?.measuredRuns),emptyText:lfT('暂无执行耗时记录','No execution timings'),option:(theme,reduced)=>obsPerformanceOption(points,{theme:obsDashboardPalette(theme),reduced}),fallback:()=>obsPerformanceFallback(points)};
  }
  const points=obsHumanPoints(),spec=obsDashboardSpec('human',points);spec.empty=!points.some(p=>p.sopRuns);spec.emptyText=lfT('暂无 SOP 运行','No SOP runs');return spec;
}
function obsPerformanceFallback(points){
  return `<div class="obs-token-table"><table class="obs-table"><thead><tr><th>${lfT('日期','Date')}</th><th>${lfT('平均执行','Mean')}</th><th>P95</th></tr></thead><tbody>${points.map(p=>`<tr><td>${escapeHTML(p.date)}</td><td>${p.performance?.measuredRuns?duration(p.performance.averageExecutionDurationMs):'—'}</td><td>${p.performance?.measuredRuns?duration(p.performance.p95ExecutionDurationMs):'—'}</td></tr>`).join('')}</tbody></table></div>`;
}
function obsUpdatePanelChart(card,key,spec,height){
  obsChartSpecs[key]=spec;
  const entry=obsChartEntry(key),el=card.querySelector(`[data-obs-chart="${key}"]`);
  if(entry&&entry.el===el&&!spec.empty){
    for(const name of Object.keys(entry.spec.on||{}))entry.chart.off(name);
    entry.spec=spec;entry.chart.setOption(spec.option(obsEChartsTheme(),obsReducedMotion()),{notMerge:true});obsBindChartEvents(entry);
  }
  else {
    obsDisposeCharts(el);const template=document.createElement('template');template.innerHTML=obsChartSlot(key,spec,height);el.replaceWith(template.content.firstElementChild);
  }
  obsMountCharts(card);
}
function obsUpdateHumanTrend(card){
  card.querySelectorAll('[data-obs-human-metric]').forEach(b=>{
    const active=b.dataset.obsHumanMetric===(state.obsDashboardMetric||'normalized');
    b.classList.toggle('active',active);b.setAttribute('aria-pressed',String(active));
  });
  obsUpdatePanelChart(card,'dashboard-human',obsSOPSpec(),196);
}
function obsBindHumanTrendActions(root){
  const cards=[...root.querySelectorAll('[data-obs-card="human"]')];
  if(root.matches?.('[data-obs-card="human"]'))cards.push(root);
  cards.forEach(card=>{
    card.querySelectorAll('[data-obs-sop-view]').forEach(button=>{button.setAttribute('aria-pressed',String(button.dataset.obsSopView===obsSOPView()));button.onclick=()=>{
      state.obsSOPView=button.dataset.obsSopView;const performance=obsSOPView()==='performance';
      card.querySelectorAll('[data-obs-sop-view]').forEach(b=>{const active=b.dataset.obsSopView===obsSOPView();b.classList.toggle('active',active);b.setAttribute('aria-pressed',String(active))});
      card.querySelector('h3').textContent=performance?lfT('SOP 执行效率','SOP performance'):lfT('SOP 人工介入点趋势','SOP intervention trend');
      card.querySelector('.obs-human-controls').hidden=performance;
      const values=card.querySelector('.obs-sop-values');values.hidden=!performance;values.innerHTML=performance?obsSOPValues():'';
      card.querySelector('.obs-sop-legend').innerHTML=obsSOPLegend();obsUpdateHumanTrend(card);
    }});
    const select=card.querySelector('#obsHumanNode');
    if(select)select.onchange=()=>{state.obsDashboardNode=select.value;obsUpdateHumanTrend(card)};
    card.querySelectorAll('[data-obs-human-metric]').forEach(b=>{
      b.setAttribute('aria-pressed',String(b.dataset.obsHumanMetric===(state.obsDashboardMetric||'normalized')));
      b.onclick=()=>{state.obsDashboardMetric=b.dataset.obsHumanMetric;obsUpdateHumanTrend(card)};
    });
  });
}

function obsBindVibeActions(root){
  root.querySelectorAll('[data-obs-vibe-view]').forEach(button=>{
    button.setAttribute('aria-pressed',String(button.dataset.obsVibeView===obsVibeView()));
    button.onclick=()=>{
      state.obsVibeView=button.dataset.obsVibeView;const card=button.closest('[data-obs-card="vibe"]');
      card.querySelectorAll('[data-obs-vibe-view]').forEach(b=>{const active=b.dataset.obsVibeView===obsVibeView();b.classList.toggle('active',active);b.setAttribute('aria-pressed',String(active))});
      card.querySelector('.obs-vibe-values').innerHTML=obsVibeValues();card.querySelector('.obs-vibe-legend').hidden=obsVibeView()==='outcomes';
      obsUpdatePanelChart(card,'dashboard-vibe',obsVibeSpec(),192);
    };
  });
}

function obsTokenView(){return state.obsTokenView==='models'?'models':'members'}
function obsTokenInPeriod(date){
  if(obsTrendGrain()==='all')return true;
  const range=obsPeriodRange(obsPeriodKey(),obsTrendGrain()),at=new Date(date+'T00:00:00Z');
  return !!range&&at>=range.from&&at<range.to;
}
function obsTokenRows(view=obsTokenView()){
  const snapshot=state.analytics||{},facts=snapshot.tokenUsage;
  const source=Array.isArray(facts)?facts.filter(r=>obsTokenInPeriod(r.date)):view==='members'
    ?(snapshot.userUsage||[]).filter(r=>obsTokenInPeriod(r.date))
    :obsTrendGrain()==='all'?(snapshot.models||[]).map(r=>({model:r.key||r.name||'',tokens:r.tokens})):[];
  if(!Array.isArray(facts)&&view==='models'&&obsTrendGrain()==='all'){
    const total=Number(snapshot.summary?.totalTokens||0),known=source.reduce((sum,r)=>sum+Number(r.tokens||0),0);
    if(known>total){source.splice(0,source.length,{model:'',tokens:total})}
    else if(total>known)source.push({model:'',tokens:total-known});
  }
  const rows=new Map();
  for(const r of source){
    const tokens=Number(r.tokens||0);if(tokens<=0)continue;
    const userId=String(r.userId||'unassigned'),model=String(r.model||'');
    const key=view==='members'?JSON.stringify([r.date,userId]):model;
    if(!rows.has(key))rows.set(key,view==='members'?{date:r.date,userId,tokens:0}:{model,name:model||lfT('未识别模型','Unattributed model'),tokens:0});
    rows.get(key).tokens+=tokens;
  }
  return [...rows.values()].sort((a,b)=>view==='members'?a.date.localeCompare(b.date)||a.userId.localeCompare(b.userId):b.tokens-a.tokens||a.name.localeCompare(b.name));
}
function obsTokenOption(view,rows,opts={}){
  const o=obsChartOpts(opts),t=o.theme,members=view==='members';
  if(!members){
    const total=rows.reduce((sum,r)=>sum+r.tokens,0),zoom=rows.length>6;
    return obsChartBase(o,{
      grid:{left:8,right:zoom?70:50,top:24,bottom:8,containLabel:true},
      xAxis:obsValueAxis(t,formatTokens,{min:0,name:'Token',nameTextStyle:{color:t.muted}}),
      yAxis:{type:'category',inverse:true,data:rows.map(r=>r.name),axisTick:{show:false},axisLine:{show:false},axisLabel:{color:t.muted,fontSize:11,width:150,overflow:'truncate'}},
      media:[{query:{maxWidth:480},option:{grid:{right:zoom?58:40},yAxis:{axisLabel:{width:80}}}},{query:{maxWidth:280},option:{yAxis:{axisLabel:{width:48}}}}],
      dataZoom:zoom?[{type:'slider',yAxisIndex:0,right:0,width:12,startValue:0,endValue:5,showDetail:false,brushSelect:false}]:[],
      tooltip:{...obsTooltipBase(t),trigger:'item',formatter:p=>obsTooltipRows(rows[p.dataIndex]?.name||'',[[lfT('Token 用量','Tokens'),formatTokens(rows[p.dataIndex]?.tokens||0)],[lfT('占比','Share'),(total?(rows[p.dataIndex]?.tokens||0)*100/total:0).toFixed(1)+'%']])},
      series:[{id:'token-models',name:'Token',type:'bar',barMaxWidth:18,data:rows.map(r=>({value:r.tokens,itemStyle:{color:r.model?t.c1:t.muted2}})),label:{show:true,position:'right',color:t.text,fontSize:11,formatter:p=>formatTokens(p.value)},animationDurationUpdate:o.reduced?0:350}],
    });
  }
  const dates=[...new Set(rows.map(r=>r.date))].sort(),totals=new Map();
  for(const r of rows)totals.set(r.userId,(totals.get(r.userId)||0)+r.tokens);
  const users=[...totals.keys()].sort((a,b)=>totals.get(b)-totals.get(a)||a.localeCompare(b));
  const cells=new Map(rows.map(r=>[JSON.stringify([r.date,r.userId]),r.tokens]));
  const palette=[t.c1,t.c2,t.amber,t.green,t.c3,t.c4],zoom=dates.length>14;
  const names=users.map(id=>obsUserName(id)),unique=names.map((name,i)=>names.indexOf(name)===i&&names.lastIndexOf(name)===i?name:`${name} (${users[i]})`);
  return obsChartBase(o,{
    color:palette,grid:{left:8,right:12,top:24,bottom:zoom?70:38,containLabel:true},
    xAxis:obsCategoryAxis(t,dates,{axisLabel:{color:t.muted,fontSize:11,hideOverlap:true,formatter:date=>date.slice(5).replace('-','/')}}),
    yAxis:obsValueAxis(t,formatTokens,{min:0,name:'Token',nameTextStyle:{color:t.muted}}),
    legend:{type:'scroll',bottom:0,itemWidth:8,itemHeight:8,textStyle:{color:t.muted,fontSize:11}},
    dataZoom:zoom?[{type:'slider',xAxisIndex:0,bottom:28,height:14,startValue:dates.length-14,endValue:dates.length-1,showDetail:false,brushSelect:false},{type:'inside',xAxisIndex:0}]:[],
    tooltip:{...obsTooltipBase(t),trigger:'axis',axisPointer:{type:'shadow'},formatter:params=>{
      const parts=(params||[]).filter(p=>Number(p.value)>0),total=parts.reduce((sum,p)=>sum+Number(p.value),0);
      return obsTooltipRows(dates[params?.[0]?.dataIndex]||'', [...parts.map(p=>[p.seriesName,formatTokens(p.value)]),[lfT('合计','Total'),formatTokens(total)]]);
    }},
    series:users.map((id,i)=>({id:'token-member-'+id,name:unique[i],type:'bar',stack:'tokens',barMaxWidth:30,data:dates.map(date=>cells.get(JSON.stringify([date,id]))||0),emphasis:{focus:'series'},animationDurationUpdate:o.reduced?0:350})),
  });
}
function obsTokenFallback(view,rows){
  const members=view==='members',headers=members?[lfT('日期','Date'),lfT('成员','Member'),'Token']:[lfT('模型','Model'),'Token'];
  return `<div class="obs-token-table"><table class="obs-table"><thead><tr>${headers.map(h=>`<th>${escapeHTML(h)}</th>`).join('')}</tr></thead><tbody>${rows.map(r=>`<tr>${(members?[r.date,obsUserName(r.userId),formatTokens(r.tokens)]:[r.name,formatTokens(r.tokens)]).map(v=>`<td>${escapeHTML(v)}</td>`).join('')}</tr>`).join('')}</tbody></table></div>`;
}
function obsTokenSpec(){
  const view=obsTokenView(),rows=obsTokenRows(view);
  return {empty:!rows.length,emptyText:lfT('暂无 Token 用量','No token usage'),option:(theme,reduced)=>obsTokenOption(view,rows,{theme:obsDashboardPalette(theme),reduced}),fallback:()=>obsTokenFallback(view,rows)};
}
function obsBindTokenActions(root){
  root.querySelectorAll('[data-obs-token-view]').forEach(button=>{
    button.setAttribute('aria-pressed',String(button.dataset.obsTokenView===obsTokenView()));
    button.onclick=()=>{
      state.obsTokenView=button.dataset.obsTokenView;
      const card=button.closest('[data-obs-card="cost"]'),spec=obsTokenSpec(),key='dashboard-tokens';obsChartSpecs[key]=spec;
      card.querySelectorAll('[data-obs-token-view]').forEach(b=>{const active=b.dataset.obsTokenView===obsTokenView();b.classList.toggle('active',active);b.setAttribute('aria-pressed',String(active))});
      const el=card.querySelector('[data-obs-chart="dashboard-tokens"]'),entry=obsChartEntry(key);
      if(entry&&entry.el===el&&!spec.empty){entry.spec=spec;entry.chart.setOption(spec.option(obsEChartsTheme(),obsReducedMotion()),{notMerge:true})}
      else {
        obsDisposeCharts(el);
        const template=document.createElement('template');template.innerHTML=obsChartSlot(key,spec,240);el.replaceWith(template.content.firstElementChild);
        obsMountCharts(card);
      }
    };
  });
}
