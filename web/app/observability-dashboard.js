// Compact statistics panels using the existing Harness filters and card styles.
function obsSummaryStats(){
  const dashboard=state.analytics?.dashboard;
  if(!dashboard)return null;
  if(obsTrendGrain()==='all')return {...dashboard,delivered:dashboard.deliveredIssues||0};
  const point=(dashboard.trend||[]).find(p=>p.date===obsPeriodKey())||{};
  return {...point,delivered:Number(point.sopDelivered||0)+Number(point.vibeDelivered||0)};
}
function obsSummaryValues(items,klass=''){
  return `<div class="obs-summary-values ${klass}">${items.map(([key,label,value])=>`<div class="obs-kpi" data-obs-stat="${key}"><div class="obs-kpi-label">${escapeHTML(label)}</div><div class="obs-kpi-value"><b class="num" data-obs-count>${escapeHTML(value)}</b></div></div>`).join('')}</div>`;
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
  const activity=obsActivityView(),view=obsScopedView();
  const n=key=>activity?String(activity[key]||0):'—';
  const metrics=obsSummaryValues([
    ['sessions',lfT('会话数','Sessions'),n('sessions')],
    ['collaborative',lfT('协作会话','Collaborative sessions'),n('collaborativeSessions')],
    ['requirements',lfT('创建需求','Requirements created'),n('requirements')],
    ['workspaces',lfT('编码工作区','Coding workspaces'),String(view.conversationWorkspaces||0)],
  ],'is-vibe');
  const points=obsTrendWindow(state.analytics?.activityTrend||[]),spec=obsDashboardSpec('vibe',points);
  spec.empty=!points.some(p=>p.sessions||p.collaborativeSessions);spec.emptyText=lfT('暂无会话','No sessions');
  return obsCard('vibe','Vibe Coding','','',metrics+obsChartSlot('dashboard-vibe',spec,192)+obsSummaryLegend('vibe'));
}
function obsSummaryCostCard(){
  const view=obsScopedView();
  return `<section class="obs-card2 obs-summary-cost" data-obs-card="cost"><h3>${lfT('用量与费用','Usage & cost')}</h3>${obsSummaryValues([
    ['tokens',lfT('Token 用量','Tokens'),formatTokens(view.tokens)],
    ['cost',lfT('费用（人民币）','Cost (CNY)'),view.costKnown?'¥'+Number(view.cost||0).toFixed(2):lfT('未识别','Unknown')],
  ])}</section>`;
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
  const points=obsHumanPoints(),spec=obsDashboardSpec('human',points);spec.empty=!points.some(p=>p.sopRuns);spec.emptyText=lfT('暂无 SOP 运行','No SOP runs');
  return obsCard('human',lfT('SOP 人工介入点趋势','SOP intervention trend'),'',`<div class="obs-human-controls">${node}${metric}</div>`,obsChartSlot('dashboard-human',spec,244)+obsSummaryLegend('human'),{expand:true});
}
function obsUpdateHumanTrend(card){
  const points=obsHumanPoints(),key='dashboard-human',spec=obsDashboardSpec('human',points);
  spec.empty=!points.some(p=>p.sopRuns);spec.emptyText=lfT('暂无 SOP 运行','No SOP runs');obsChartSpecs[key]=spec;
  const entry=obsChartEntry(key),el=card.querySelector('[data-obs-chart="dashboard-human"]');
  card.querySelectorAll('[data-obs-human-metric]').forEach(b=>{
    const active=b.dataset.obsHumanMetric===(state.obsDashboardMetric||'normalized');
    b.classList.toggle('active',active);b.setAttribute('aria-pressed',String(active));
  });
  if(entry&&entry.el===el){entry.spec=spec;entry.chart.setOption(spec.option(obsEChartsTheme(),obsReducedMotion()),{replaceMerge:['series']})}
  else if(el?.classList.contains('is-fallback'))obsChartFallback(el,spec);
  else obsMountCharts(card);
}
function obsBindHumanTrendActions(root){
  const cards=[...root.querySelectorAll('[data-obs-card="human"]')];
  if(root.matches?.('[data-obs-card="human"]'))cards.push(root);
  cards.forEach(card=>{
    const select=card.querySelector('#obsHumanNode');
    if(select)select.onchange=()=>{state.obsDashboardNode=select.value;obsUpdateHumanTrend(card)};
    card.querySelectorAll('[data-obs-human-metric]').forEach(b=>{
      b.setAttribute('aria-pressed',String(b.dataset.obsHumanMetric===(state.obsDashboardMetric||'normalized')));
      b.onclick=()=>{state.obsDashboardMetric=b.dataset.obsHumanMetric;obsUpdateHumanTrend(card)};
    });
  });
}
