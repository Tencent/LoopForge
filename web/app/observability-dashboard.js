// Compact analytics dashboard. Detailed report builders remain in observability.js.
function obsDashboardDays(){return ['7','14','30','all'].includes(String(state.obsDashboardDays))?String(state.obsDashboardDays):'14'}
function obsDashboardWindow(now=new Date()){
  if(obsDashboardDays()==='all')return {from:null,to:null};
  const to=new Date(now),from=new Date(now);
  to.setHours(23,59,59,999);from.setHours(0,0,0,0);from.setDate(from.getDate()-Number(obsDashboardDays())+1);
  return {from,to};
}
function obsDashboardQuery(window){
  const p=new URLSearchParams(projectQuery().replace(/^\?/,''));
  p.set('scope','dashboard');p.set('grain',obsDashboardDays()==='all'?'month':'day');p.set('tz',obsTimeZone());
  if(window.from)p.set('from',window.from.toISOString());
  if(window.to)p.set('to',window.to.toISOString());
  return '?'+p.toString();
}
let obsDashboardRequest=0;
async function obsLoadDashboard(){
  const ticket=++obsDashboardRequest,window=obsDashboardWindow(),query=obsDashboardQuery(window);
  state.obsDashboardLoading=true;state.obsDashboardError='';
  if(state.obsDashboardQuery!==query)state.obsDashboardSnapshot=null;
  renderObservability();
  try{
    const snapshot=await api('/api/analytics'+query,{silent:true});
    if(ticket!==obsDashboardRequest||query!==obsDashboardQuery(window))return;
    if(!snapshot.dashboard)throw new Error(lfT('统计数据暂不可用','Analytics are unavailable'));
    state.analytics=snapshot;state.obsDashboardSnapshot=snapshot;state.obsDashboardQuery=query;state.obsDashboardWindow=window;
  }catch(e){if(ticket===obsDashboardRequest)state.obsDashboardError=e.message||lfT('统计加载失败','Unable to load analytics')}
  finally{if(ticket===obsDashboardRequest){state.obsDashboardLoading=false;renderObservability()}}
}
function obsDashboardDates(snapshot){
  const win=state.obsDashboardWindow||obsDashboardWindow();
  if(obsDashboardDays()==='all')return [...new Set([...(snapshot.dashboard?.trend||[]),...(snapshot.activityTrend||[])].map(p=>p.date))].sort();
  const dates=[];const end=win.to||new Date();
  for(let at=new Date(win.from||end);at<=end;at.setDate(at.getDate()+1))dates.push(at.getFullYear()+'-'+String(at.getMonth()+1).padStart(2,'0')+'-'+String(at.getDate()).padStart(2,'0'));
  return dates;
}
function obsDashboardPoints(snapshot){
  const trend=new Map((snapshot.dashboard?.trend||[]).map(p=>[p.date,p]));
  const activity=new Map((snapshot.activityTrend||[]).map(p=>[p.date,p]));
  const humans=new Map();
  for(const p of snapshot.dashboard?.humanTrend||[]){
    if(state.obsDashboardNode&&p.node!==state.obsDashboardNode)continue;
    const row=humans.get(p.date)||{approvals:0,rejections:0,retries:0};
    for(const key of ['approvals','rejections','retries'])row[key]+=Number(p[key]||0);
    humans.set(p.date,row);
  }
  return obsDashboardDates(snapshot).map(date=>({date,...trend.get(date),sessions:Number(activity.get(date)?.sessions||0),collaborativeSessions:Number(activity.get(date)?.collaborativeSessions||0),...humans.get(date)}));
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
  const colors=line?[t.c2,t.c1]:[t.c1,t.c2,t.amber];
  const unit=kind==='delivery'?lfT('个','issues'):kind==='human'&&metric!=='count'?lfT('次 / 百次运行','per 100 runs'):lfT('次','events');
  return obsChartBase(o,{
    animationDuration:o.reduced?0:500,animationDurationUpdate:o.reduced?0:350,
    grid:{left:8,right:12,top:25,bottom:8,containLabel:true},
    xAxis:obsCategoryAxis(t,points.map(p=>p.date.length===7?p.date.slice(2).replace('-','/'):p.date.slice(5).replace('-','/')),{boundaryGap:!line,axisLabel:{color:t.muted,fontSize:11,hideOverlap:true}}),
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
  const series=obsDashboardSeries(kind,points,state.obsDashboardMetric),line=kind==='vibe',colors=line?['var(--hui-info)','var(--hui-accent)']:['var(--hui-accent)','var(--hui-info)','var(--hui-warn)'];
  const peak=Math.max(1,...points.map((_,i)=>line?Math.max(...series.map(s=>s.values[i])):series.reduce((n,s)=>n+s.values[i],0))),max=Math.ceil(peak/4)*4,x=i=>l+(i+.5)*pw/Math.max(1,points.length),y=v=>t+ph*(1-v/max);
  let html=`<svg width="${w}" height="${h}" role="img" aria-label="${obsEsc(kind==='delivery'?lfT('交付趋势','Delivery trend'):kind==='vibe'?'Vibe Coding':lfT('SOP 人工介入趋势','SOP intervention trend'))}">`;
  for(let i=0;i<=4;i++){const value=max*i/4;html+=`<line x1="${l}" y1="${y(value)}" x2="${w-r}" y2="${y(value)}" stroke="var(--hui-border)"/><text x="${l-7}" y="${y(value)+4}" text-anchor="end">${value}</text>`}
  points.forEach((p,i)=>{if(i===0||i===points.length-1||i===Math.floor(points.length/2))html+=`<text x="${x(i)}" y="${h-5}" text-anchor="middle">${obsEsc(p.date.slice(5).replace('-','/'))}</text>`});
  if(line)series.forEach((s,g)=>{html+=`<polyline points="${s.values.map((v,i)=>x(i)+','+y(v)).join(' ')}" fill="none" stroke="${colors[g]}" stroke-width="2" ${g?'stroke-dasharray="5 3"':''}/>`});
  else points.forEach((_,i)=>{let base=0;series.forEach((s,g)=>{const v=s.values[i],bw=Math.min(30,pw/points.length*.65);html+=`<rect x="${x(i)-bw/2}" y="${y(base+v)}" width="${bw}" height="${Math.max(0,y(base)-y(base+v))}" fill="${colors[g]}"/>`;base+=v})});
  return html+'</svg>';
}
function obsDashboardSpec(kind,points){return {option:(theme,reduced)=>obsDashboardOption(kind,points,{theme:obsDashboardPalette(theme),reduced,metric:state.obsDashboardMetric}),fallback:()=>obsDashboardFallback(kind,points)}}
function obsDashboardSkeleton(){
  const tr=(zh,en)=>lfT(zh,en),metric=(key,label)=>`<div class="od-kpi"><span>${label}</span><b class="num" data-od-number="${key}">—</b></div>`;
  const legend=(color,label,key)=>`<span><i class="od-dot ${color}"></i>${label}${key?` <b class="num" data-od-number="${key}">—</b>`:''}</span>`;
  return `<div class="obs-dashboard">
    <div class="od-toolbar"><select id="obsDashboardProject" data-obs-select aria-label="${tr('项目','Project')}"></select><select id="obsDashboardRange" data-obs-select aria-label="${tr('时间范围','Date range')}">${[['7',tr('近 7 天','Last 7 days')],['14',tr('近 14 天','Last 14 days')],['30',tr('近 30 天','Last 30 days')],['all',tr('全部时间','All time')]].map(([key,label])=>`<option value="${key}">${label}</option>`).join('')}</select>${obsCanManageSources()?`<button type="button" class="obs-btn ghost od-sources" id="obsManageSources" aria-label="${tr('外部数据源','External sources')}" title="${tr('外部数据源','External sources')}">${obsIcon('integration')}</button>`:''}</div>
    <div class="od-error" role="alert" hidden></div>
    <div class="od-kpis">${metric('delivered',tr('已交付需求','Delivered issues'))}${metric('success',tr('SOP 成功率','SOP success rate'))}${metric('humanRate',tr('SOP 人工介入率','SOP intervention rate'))}</div>
    <div class="od-grid">
      <section class="od-panel"><header class="od-head"><h2>${tr('交付趋势','Delivery trend')}</h2></header><div class="od-legend">${legend('', 'SOP','sopDelivered')}${legend('blue','Vibe Coding','vibeDelivered')}</div>${obsChartSlot('dashboard-delivery',{},245)}</section>
      <section class="od-panel"><header class="od-head"><h2>Vibe Coding</h2></header><div class="od-vibe">${metric('sessions',tr('会话','Sessions'))}${metric('collaboration',tr('协作会话','Collaborative sessions'))}${metric('members',tr('活跃成员','Active members'))}${metric('workspaces',tr('编码工作区','Coding workspaces'))}</div><div class="od-legend">${legend('blue',tr('会话','Sessions'))}${legend('dash',tr('协作会话','Collaborative sessions'))}</div>${obsChartSlot('dashboard-vibe',{},180)}</section>
    </div>
    <div class="od-grid od-bottom">
      <section class="od-panel"><header class="od-head"><h2>${tr('SOP 人工介入趋势','SOP intervention trend')}</h2><select id="obsDashboardNode" data-obs-select aria-label="${tr('人工介入节点','Intervention node')}"></select></header><div class="od-sop"><span>${tr('SOP 运行','SOP runs')} <b class="num" data-od-number="runs">—</b></span><span>${tr('P95 执行耗时','P95 execution time')} <b class="num" data-od-number="p95">—</b></span></div><div class="od-metrics" role="group" aria-label="${tr('人工介入口径','Intervention measure')}"><button type="button" data-od-metric="normalized">${tr('每百次运行','Per 100 runs')}</button><button type="button" data-od-metric="count">${tr('介入次数','Decisions')}</button></div>${obsChartSlot('dashboard-human',{},210)}<div class="od-legend od-human-legend">${legend('',tr('审批 / 放行','Approved / passed'))}${legend('blue',tr('驳回','Rejected'))}${legend('amber',tr('重试 / 拒绝续跑','Retry / declined'))}</div></section>
      <section class="od-panel od-cost"><header class="od-head"><h2>${tr('费用与用量','Cost & usage')}</h2></header><span class="od-label" data-od-cost-label>${tr('已识别费用','Identified cost')}</span><b class="od-cost-value num" data-od-number="cost">—</b><div class="od-token"><span>${tr('执行 Token','Execution tokens')}</span><b class="num" data-od-number="tokens">—</b></div></section>
    </div>
  </div>`;
}
function obsDashboardNumber(el,value,format=String){
  if(!el)return;
  const text=value==null?'—':format(value);
  if(el.dataset.odFinal===text)return;
  el.dataset.odFinal=text;
  if(el._odFrame)cancelAnimationFrame(el._odFrame);
  const previous=Number(el.dataset.odValue)||0;el.dataset.odValue=String(value??0);el.setAttribute('aria-label',text);
  if(value==null||!Number.isFinite(value)||obsReducedMotion()||Math.abs(value-previous)<.001){el.textContent=text;return}
  const start=performance.now();
  const tick=now=>{if(!el.isConnected)return;const progress=Math.min(1,(now-start)/350);el.textContent=progress===1?text:format(previous+(value-previous)*(1-(1-progress)**3));if(progress<1)el._odFrame=requestAnimationFrame(tick)};
  el._odFrame=requestAnimationFrame(tick);
}
function obsRenderDashboard(){
  const host=$('#obsBody');if(!host)return;
  const snapshot=state.obsDashboardSnapshot,d=snapshot?.dashboard,a=snapshot?.activity||{},summary=snapshot?.summary||{};
  if(!host.querySelector('.obs-dashboard')){obsDisposeCharts(host);obsDisposeSelects();host.innerHTML=obsDashboardSkeleton();obsHydrateIcons(host)}
  const root=host.querySelector('.obs-dashboard');root.setAttribute('aria-busy',String(!!state.obsDashboardLoading));
  const refresh=$('#refreshObs'),exportButton=$('#exportObsReport');
  if(refresh)refresh.disabled=!!state.obsDashboardLoading;
  if(exportButton)exportButton.disabled=!d||!!state.obsDashboardLoading;
  const projects=state.data.projects||[],project=root.querySelector('#obsDashboardProject');
  const optionHTML=projects.map(p=>`<option value="${escapeHTML(p.id)}">${escapeHTML(p.name||p.id)}</option>`).join('');
  if(project.innerHTML!==optionHTML)project.innerHTML=optionHTML;
  project.value=state.projectFilter||'';project.onchange=()=>{const picker=$('#projectSwitcher');picker.value=project.value;picker.dispatchEvent(new Event('change',{bubbles:true}))};
  const range=root.querySelector('#obsDashboardRange');range.value=obsDashboardDays();range.onchange=()=>{state.obsDashboardDays=range.value;state.obsDashboardWindow=null;obsLoadDashboard()};
  const error=root.querySelector('.od-error');error.hidden=!state.obsDashboardError;error.textContent=state.obsDashboardError||'';
  const metrics={delivered:d?.deliveredIssues,success:d&&d.succeededRuns+d.failedRuns?d.succeededRuns*100/(d.succeededRuns+d.failedRuns):null,humanRate:d?.sopRuns?d.humanRuns*100/d.sopRuns:null,sopDelivered:d?.sopDeliveredIssues,vibeDelivered:d?.vibeDeliveredIssues,sessions:d?a.sessions||0:null,collaboration:d?a.collaborativeSessions||0:null,members:d?.activeMembers,workspaces:d?summary.conversationWorkspaces||0:null,runs:d?.sopRuns,p95:d?.p95ExecutionDurationMs||null,cost:d?summary.totalCostCny||0:null,tokens:d?summary.totalTokens||0:null};
  for(const [key,value] of Object.entries(metrics)){
    const format=key==='success'||key==='humanRate'?n=>n.toFixed(1)+'%':key==='p95'?duration:key==='cost'?n=>'¥'+n.toFixed(2):key==='tokens'?formatTokens:n=>Math.round(n).toLocaleString();
    obsDashboardNumber(root.querySelector(`[data-od-number="${key}"]`),value,format);
  }
  const costLabel=root.querySelector('[data-od-cost-label]');costLabel.textContent=d&&obsCostUnknown(summary)&&!(summary.totalCostCny>0)?lfT('费用未识别','Cost unknown'):lfT('已识别费用','Identified cost');
  if(d&&obsCostUnknown(summary)&&!(summary.totalCostCny>0))obsDashboardNumber(root.querySelector('[data-od-number="cost"]'),null);
  const node=root.querySelector('#obsDashboardNode'),nodes=d?.humanNodes||[];
  if(!nodes.some(n=>n.key===state.obsDashboardNode))state.obsDashboardNode='';
  const nodeHTML=`<option value="">${lfT('全部节点','All nodes')}</option>`+nodes.map(n=>`<option value="${escapeHTML(n.key)}">${escapeHTML(n.name)}</option>`).join('');
  if(node.innerHTML!==nodeHTML)node.innerHTML=nodeHTML;node.value=state.obsDashboardNode||'';
  node.onchange=()=>{state.obsDashboardNode=node.value;obsRenderDashboard()};
  root.querySelectorAll('[data-od-metric]').forEach(b=>{b.setAttribute('aria-pressed',String((state.obsDashboardMetric||'normalized')===b.dataset.odMetric));b.onclick=()=>{state.obsDashboardMetric=b.dataset.odMetric;obsRenderDashboard()}});
  const sources=root.querySelector('#obsManageSources');if(sources)sources.onclick=openObsSources;
  const points=snapshot?obsDashboardPoints(snapshot):[];
  for(const kind of ['delivery','vibe','human']){
    const key='dashboard-'+kind,spec=obsDashboardSpec(kind,points),el=root.querySelector(`[data-obs-chart="${key}"]`);
    obsChartSpecs[key]=spec;
    const entry=obsChartEntry(key);
    if(entry&&entry.el===el){entry.spec=spec;entry.chart.setOption(spec.option(obsEChartsTheme(),obsReducedMotion()),{replaceMerge:['series']})}
    else if(el.classList.contains('is-fallback'))obsChartFallback(el,spec);
  }
  obsMountCharts(root);
}

async function exportObsDashboardReport(){
  const source=document.querySelector('#obsBody .obs-dashboard');
  if(!source||!state.obsDashboardSnapshot?.dashboard)return;
  try{
    const clone=source.cloneNode(true),points=obsDashboardPoints(state.obsDashboardSnapshot),metric=state.obsDashboardMetric||'normalized',theme=obsDashboardPalette(obsEChartsTheme());
    const title=lfT('研发效能','Engineering analytics')+' · '+(currentProject()?.name||'LoopForge');
    clone.querySelectorAll('[data-od-number]').forEach(el=>{el.textContent=el.dataset.odFinal||'—'});
    for(const select of clone.querySelectorAll('select')){
      const value=document.createElement('span');value.textContent=source.querySelector('#'+select.id)?.selectedOptions?.[0]?.textContent||'';select.replaceWith(value);
    }
    const ec=await obsEnsureECharts();
    for(const slot of clone.querySelectorAll('[data-obs-chart]')){
      const key=slot.dataset.obsChart,spec=obsChartSpecs[key];
      if(ec&&spec){
        const chart=ec.init(null,null,{renderer:'svg',ssr:true,width:580,height:235});
        try{chart.setOption(obsDashboardOption(key.replace('dashboard-',''),points,{theme,reduced:true,metric}));slot.innerHTML=chart.renderToSVGString();slot.querySelector('svg')?.setAttribute('width','100%')}
        finally{chart.dispose()}
      }else slot.innerHTML=spec?.fallback?.()||'';
    }
    clone.querySelectorAll('button').forEach(b=>b.remove());
    clone.querySelector('.od-error')?.remove();
    clone.removeAttribute('aria-busy');
    const css=[...document.styleSheets].find(s=>s.href?.includes('/app/observability-dashboard.css'));
    const rules=css?[...css.cssRules].map(r=>r.cssText).join('\n'):'';
    const computed=getComputedStyle(document.documentElement);
    const tokens=['surface','text','muted','border','border-strong','accent','accent-soft','info','warn','soft','sans','ease-flow'].map(k=>'--hui-'+k+':'+computed.getPropertyValue('--hui-'+k)+';').join('');
    const html=`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>${escapeHTML(title)}</title><style>:root{${tokens}}*{box-sizing:border-box}body{margin:0;padding:24px;font:14px/1.5 var(--hui-sans);background:var(--hui-surface);color:var(--hui-text)}#observabilityView{max-width:1100px;margin:auto}.obs-chart svg{max-width:100%}${rules}#observabilityView .obs-dashboard *{animation:none!important;transition:none!important}</style></head><body><section id="observabilityView"><h1>${escapeHTML(title)}</h1>${clone.outerHTML}</section></body></html>`;
    const link=document.createElement('a'),url=URL.createObjectURL(new Blob([html],{type:'text/html;charset=utf-8'}));
    link.href=url;link.download='LoopForge-analytics-'+new Date().toISOString().slice(0,10)+'.html';document.body.appendChild(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(url),1500);
  }catch(e){toast(e.message||lfT('导出失败','Export failed'),'err')}
}
