// ================= 可观测与分析 =================
let refreshObsPending=null;
function showDirectRunButton(onFlow){
  const btn=$('#runButton');
  if(!btn)return;
  btn.style.display=onFlow&&isPreviewHost()?'':'none';
}
function isPreviewHost(){
  const host=String((typeof location!=='undefined'&&location.hostname)||'').toLowerCase();
  const domain=String((typeof state!=='undefined'&&state.appDomain)||(typeof window!=='undefined'&&window.__AGENTFLOW_APP_DOMAIN__)||'').toLowerCase();
  return host==='loopforge-test.example.com'||domain==='loopforge-test.example.com'||host.includes('-test.')||isLoopbackHost(host);
}
function renderObsTabs(){state.obsTab=obsNormalizeTab(state.obsTab)}
function obsNormalizeTab(tab){return tab==='delivery'?'delivery':'overview'}
function obsScope(){return state.obsScope==='project'?'project':'user'}
function obsTrendGrain(){return ['day','week','month','all'].includes(state.obsTrendGrain)?state.obsTrendGrain:'all'}
function obsApiGrain(){return obsTrendGrain()==='all'?'month':obsTrendGrain()}
function obsTrendMetric(){return ['comp','runs','issues','cost','rate'].includes(state.obsTrendMetric)?state.obsTrendMetric:'tokens'}
function obsTrendWindow(points){
  const grain=obsTrendGrain();
  if(grain==='all'||grain==='month')return points||[];
  return (points||[]).slice(grain==='week'?-12:-30);
}
function obsPeriodIndex(key,grain){
  key=String(key||'');
  if(grain==='month'){const m=key.match(/^(\d{4})-(\d{2})$/);return m?Number(m[1])*12+Number(m[2])-1:null}
  if(grain==='week'){const monday=obsWeekMonday(key);return monday?Math.round(monday.getTime()/604800000):null}
  if(!/^\d{4}-\d{2}-\d{2}$/.test(key))return null;
  return Math.round(new Date(key+'T00:00:00Z').getTime()/86400000);
}
// trend 只含有数据的周期；窗口跨 span 个周期时，上一周期同位置 = 日期回退 span 个周期。
// 早于 trend 首个周期记为 null（无从比较），区间内缺失的周期记为 0。
function obsTrendPrevPoints(trend,list,grain){
  grain=grain||obsTrendGrain();
  trend=trend||[];
  if(grain==='all'||!list||!list.length||!trend.length)return null;
  const first=obsPeriodIndex(list[0].date,grain),last=obsPeriodIndex(list[list.length-1].date,grain);
  const start=obsPeriodIndex(trend[0].date,grain);
  if(first==null||last==null||start==null)return null;
  const span=last-first+1;
  const byDate=new Map(trend.map(p=>[p.date,p]));
  const out=list.map(p=>{
    const idx=obsPeriodIndex(p.date,grain);
    if(idx==null||idx-span<start)return null;
    const key=obsStepPeriodKey(p.date,grain,-span);
    return byDate.get(key)||{date:key,tokens:0,costCny:0,runs:0,issues:0,succeeded:0,failed:0};
  });
  return out.some(Boolean)?out:null;
}
function obsWeekMonday(key){
  const m=String(key||'').match(/^(\d{4})-W(\d{2})$/);
  if(!m)return null;
  const year=Number(m[1]),week=Number(m[2]);
  const jan4=new Date(Date.UTC(year,0,4));
  const day=jan4.getUTCDay()||7;
  const monday=new Date(jan4);
  monday.setUTCDate(jan4.getUTCDate()-(day-1)+(week-1)*7);
  return monday;
}
function obsDateToWeekKey(at){
  const d=new Date(Date.UTC(at.getUTCFullYear(),at.getUTCMonth(),at.getUTCDate()));
  const day=d.getUTCDay()||7;
  d.setUTCDate(d.getUTCDate()+4-day);
  const year=d.getUTCFullYear();
  const jan4=new Date(Date.UTC(year,0,4));
  const week=1+Math.round((d-jan4)/86400000/7);
  return `${year}-W${String(week).padStart(2,'0')}`;
}
function obsWeekLabel(key){
  const monday=obsWeekMonday(key);
  if(!monday)return String(key||'');
  const sunday=new Date(monday);
  sunday.setUTCDate(monday.getUTCDate()+6);
  const fmt=x=>`${x.getUTCMonth()+1}/${x.getUTCDate()}`;
  return `${fmt(monday)}–${fmt(sunday)}`;
}
function obsDefaultPeriodKey(grain){
  grain=grain||obsTrendGrain();
  const now=new Date();
  if(grain==='month')return `${now.getUTCFullYear()}-${String(now.getUTCMonth()+1).padStart(2,'0')}`;
  if(grain==='week')return obsDateToWeekKey(now);
  return now.toISOString().slice(0,10);
}
function obsPeriodKey(){
  if(obsTrendGrain()==='all')return '';
  const key=String(state.obsPeriodKey||'').trim();
  if(key)return key;
  const trend=obsTrendWindow(state.analytics?.trend||[]);
  return trend.length?trend[trend.length-1].date:obsDefaultPeriodKey();
}
function obsPeriodRange(key,grain){
  grain=grain||obsTrendGrain();
  key=String(key||'').trim();
  if(!key||grain==='all')return null;
  if(grain==='month'&&/^\d{4}-\d{2}$/.test(key)){
    const y=Number(key.slice(0,4)),m=Number(key.slice(5,7));
    return {from:new Date(Date.UTC(y,m-1,1)),to:new Date(Date.UTC(y,m,1)),label:`${y}年${m}月`,shortLabel:`${m}月`};
  }
  if(grain==='week'){
    const monday=obsWeekMonday(key);
    if(!monday)return null;
    const to=new Date(monday);
    to.setUTCDate(monday.getUTCDate()+7);
    return {from:monday,to,label:obsWeekLabel(key),shortLabel:obsWeekLabel(key)};
  }
  if(/^\d{4}-\d{2}-\d{2}$/.test(key)){
    const from=new Date(key+'T00:00:00Z');
    const to=new Date(from);
    to.setUTCDate(from.getUTCDate()+1);
    return {from,to,label:key,shortLabel:`${Number(key.slice(5,7))}/${Number(key.slice(8,10))}`};
  }
  return null;
}
function obsStepPeriodKey(key,grain,delta){
  grain=grain||obsTrendGrain();
  key=String(key||obsDefaultPeriodKey(grain));
  if(grain==='month'){
    const y=Number(key.slice(0,4)),m=Number(key.slice(5,7));
    const next=new Date(Date.UTC(y,m-1+delta,1));
    return `${next.getUTCFullYear()}-${String(next.getUTCMonth()+1).padStart(2,'0')}`;
  }
  if(grain==='week'){
    const monday=obsWeekMonday(key)||new Date();
    monday.setUTCDate(monday.getUTCDate()+delta*7);
    return obsDateToWeekKey(monday);
  }
  const day=new Date((/^\d{4}-\d{2}-\d{2}$/.test(key)?key:obsDefaultPeriodKey('day'))+'T00:00:00Z');
  day.setUTCDate(day.getUTCDate()+delta);
  return day.toISOString().slice(0,10);
}
function obsTraceEventAt(t){
  const raw=t?.finishedAt||t?.createdAt;
  const at=raw?new Date(raw):null;
  return at&&!Number.isNaN(at.getTime())?at:null;
}
function obsTraceInRange(t,range){
  if(!range)return true;
  const at=obsTraceEventAt(t);
  return !!at&&at>=range.from&&at<range.to;
}
function obsPercentile(values,pct){
  const list=(values||[]).filter(n=>n>0).slice().sort((a,b)=>a-b);
  if(!list.length)return 0;
  return list[Math.min(list.length-1,Math.max(0,Math.ceil(list.length*pct/100)-1))];
}
function obsTrendLabel(p){
  if(p&&p.label)return p.label;
  const d=p&&p.date||'';
  if(/^\d{4}-W\d{2}$/.test(d))return obsWeekLabel(d);
  if(/^\d{4}-\d{2}$/.test(d))return `${Number(d.slice(5))}月`;
  if(d.length>=10)return d.slice(5).replace('-','/');
  return d;
}
function obsTrendValue(p,metric){
  if(metric==='runs')return Number(p?.runs||0);
  if(metric==='issues')return Number(p?.issues||0);
  if(metric==='cost')return Number(p?.costCny||0);
  if(metric==='rate'){const attempts=Number(p?.succeeded||0)+Number(p?.failed||0);return attempts?Number(p?.succeeded||0)*100/attempts:0}
  return Number(p?.tokens||0);
}
function obsFormatTrendValue(v,metric){
  if(metric==='tokens')return formatTokens(v);
  if(metric==='cost')return '¥'+Number(v||0).toFixed(2);
  if(metric==='rate')return Math.round(Number(v||0))+'%';
  return String(Math.round(Number(v||0)));
}
function obsPeriodDelta(points,metric){
  const list=obsTrendWindow(points);
  if(list.length<2)return null;
  let idx=list.length-1;
  const selected=obsPeriodKey();
  if(selected){
    const found=list.findIndex(p=>p.date===selected);
    if(found>=0)idx=found;
  }
  if(idx<1)return null;
  const cur=list[idx],prev=list[idx-1];
  const current=obsTrendValue(cur,metric),previous=obsTrendValue(prev,metric);
  const delta=current-previous;
  const pct=previous?delta*100/previous:(current?100:0);
  return {current,previous,delta,pct,currentLabel:obsTrendLabel(cur),previousLabel:obsTrendLabel(prev)};
}
function obsDeltaChip(delta,invert){
  if(!delta)return '';
  const down=delta.delta<0,up=delta.delta>0;
  const tone=down?(invert?'good':'bad'):up?(invert?'bad':'good'):'flat';
  const arrow=down?'↓':up?'↑':'–';
  const pct=Math.abs(delta.pct);
  return `<em class="obs-delta ${tone}">${arrow} ${pct>=10?pct.toFixed(0):pct.toFixed(1)}% <span>较${escapeHTML(delta.previousLabel)}</span></em>`;
}
function obsGrainCaption(){return (obsTrendGrain()==='all'||obsTrendGrain()==='month')?'月':obsTrendGrain()==='week'?'周':'日'}
function obsRangeCaption(){
  if(obsTrendGrain()==='all')return '累计';
  const range=obsPeriodRange(obsPeriodKey(),obsTrendGrain());
  return range?range.shortLabel:('本'+obsGrainCaption());
}
function setObsTrendGrain(grain){
  grain=['day','week','month','all'].includes(grain)?grain:'all';
  if(obsTrendGrain()===grain)return;
  state.obsTrendGrain=grain;
  state.obsPeriodKey='';
  localStorage.setItem('agentflow.obsTrendGrain',grain);
  applyObsFilter();
}
function setObsPeriod(key,grain){
  key=String(key||'').trim();
  if(grain&&['day','week','month'].includes(grain)&&grain!==obsTrendGrain()){
    state.obsTrendGrain=grain;
    localStorage.setItem('agentflow.obsTrendGrain',grain);
    state.obsPeriodKey=key;
    applyObsFilter();
    return;
  }
  if(obsPeriodKey()===key)return;
  state.obsPeriodKey=key;
  renderObservability();
}
function obsShiftPeriod(step){
  const grain=obsTrendGrain();
  if(grain==='all')return;
  const next=obsStepPeriodKey(obsPeriodKey()||obsDefaultPeriodKey(grain),grain,step);
  if(step>0&&next>obsDefaultPeriodKey(grain))return;
  setObsPeriod(next);
}
function obsPeriodChoices(){
  const keys=new Set((state.analytics?.trend||[]).map(t=>t.date).filter(Boolean));
  keys.add(obsPeriodKey()||obsDefaultPeriodKey());
  keys.add(obsDefaultPeriodKey());
  return [...keys].sort();
}
function obsPeriodPicker(){
  const grain=obsTrendGrain();
  if(grain==='all')return '';
  const key=obsPeriodKey()||obsDefaultPeriodKey(grain);
  let control='';
  if(grain==='day')control=`<input type="date" id="obsPeriodInput" class="obs-period-input" value="${escapeHTML(key)}" aria-label="选择日期">`;
  else if(grain==='month')control=`<input type="month" id="obsPeriodInput" class="obs-period-input" value="${escapeHTML(key)}" aria-label="选择月份">`;
  else control=`<select id="obsPeriodInput" class="obs-period-input" data-obs-select data-icon="calendar" aria-label="选择周">${obsPeriodChoices().map(w=>`<option value="${escapeHTML(w)}" ${w===key?'selected':''}>${escapeHTML(obsWeekLabel(w))}</option>`).join('')}</select>`;
  return `<div class="obs-period-picker" role="group" aria-label="选择时段"><button type="button" class="obs-icon-btn" data-obs-period-step="-1" title="上一段" aria-label="上一段">${obsIcon('chevron-left')}</button>${control}<button type="button" class="obs-icon-btn" data-obs-period-step="1" title="下一段" aria-label="下一段">${obsIcon('chevron-right')}</button></div>`;
}
function setObsTrendMetric(metric){
  metric=['comp','runs','issues','cost','rate'].includes(metric)?metric:'tokens';
  if(obsTrendMetric()===metric)return;
  state.obsTrendMetric=metric;
  localStorage.setItem('agentflow.obsTrendMetric',metric);
  if(obsCardBuilders.trend&&document.querySelector('[data-obs-card="trend"]'))obsRefreshCard('trend');
  else renderObservability();
}
function obsTrendChart(points,metric){
  const list=obsTrendWindow(points);
  if(!list.length)return `<div class="detail-empty">暂无趋势</div>`;
  const w=760,h=210,l=52,r=18,t=16,b=36;
  const values=list.map(p=>obsTrendValue(p,metric));
  const max=Math.max(1,...values);
  const span=Math.max(1,list.length-1);
  const xs=list.map((_,i)=>list.length===1?l+(w-l-r)/2:l+i*(w-l-r)/span);
  const ys=values.map(v=>t+(h-t-b)*(1-v/max));
  const line=xs.map((x,i)=>(i?'L':'M')+x.toFixed(1)+','+ys[i].toFixed(1)).join(' ');
  const area=`${line} L${xs[xs.length-1].toFixed(1)},${h-b} L${xs[0].toFixed(1)},${h-b} Z`;
  const step=Math.max(1,Math.ceil(list.length/7));
  const labels=list.map((p,i)=>{
    if(i!==0&&i!==list.length-1&&i%step)return '';
    return `<text x="${xs[i].toFixed(1)}" y="${h-12}" text-anchor="middle">${escapeHTML(obsTrendLabel(p))}</text>`;
  }).join('');
  const yTicks=[0,0.5,1].map(p=>{
    const y=t+(h-t-b)*(1-p),val=max*p;
    return `<line x1="${l}" y1="${y.toFixed(1)}" x2="${w-r}" y2="${y.toFixed(1)}"/><text class="obs-chart-y" x="${l-8}" y="${(y+4).toFixed(1)}" text-anchor="end">${escapeHTML(obsFormatTrendValue(val,metric))}</text>`;
  }).join('');
  const selected=obsPeriodKey();
  const dots=xs.map((x,i)=>{
    const active=selected&&list[i].date===selected;
    return `<circle class="${active?'obs-chart-dot-active':''}" data-obs-period="${escapeHTML(list[i].date||'')}" cx="${x.toFixed(1)}" cy="${ys[i].toFixed(1)}" r="${active?5:list.length>16?2.4:3.4}"><title>${escapeHTML(obsTrendLabel(list[i]))}：${obsFormatTrendValue(values[i],metric)}（点击查看该时段）</title></circle>`;
  }).join('');
  return `<svg class="obs-trend-svg" viewBox="0 0 ${w} ${h}" role="img" aria-label="用量趋势">${yTicks}<path class="obs-chart-area" d="${area}"></path><path class="obs-chart-line" d="${line}"></path>${dots}${labels}</svg>`;
}
function obsScopedView(){
  const summary=state.analytics?.summary||{};
  const demo=state.analytics?.demo||{};
  const all=state.analytics?.runTraces||[];
  if(obsTrendGrain()==='all'){
    return {
      period:false,traces:all,total:summary.totalRuns||0,succeeded:summary.succeededRuns||0,failed:summary.failedRuns||0,cancelled:summary.cancelledRuns||0,
      reworked:summary.reworkedRuns||0,uniqueIssues:summary.uniqueIssues||0,tokens:summary.totalTokens||0,sopTokens:Math.max(0,(summary.totalTokens||0)-(summary.conversationTokens||0)),conversationTokens:summary.conversationTokens||0,conversationCost:summary.conversationCostCny||0,
      conversationIssues:summary.conversationIssues||0,conversationWorkspaces:summary.conversationWorkspaces||0,conversationLinked:summary.conversationLinkedWorkspaces,inputTokens:summary.inputTokens||0,outputTokens:summary.outputTokens||0,
      cost:summary.totalCostCny||0,costKnown:summary.costUnknownRuns===0,active:summary.activeRuns||0,p95:summary.executionP95DurationMs||0,
      avgExec:summary.executionAverageDurationMs||0,wait:summary.waitAverageDurationMs||0,runsPerIssue:summary.runsPerIssue,firstPass:summary.qualityRuns?summary.firstPassRate:null,
      successRate:summary.successRate,demo:demo||{}
    };
  }
  const range=obsPeriodRange(obsPeriodKey(),obsTrendGrain());
  const traces=all.filter(t=>obsTraceInRange(t,range));
  let succeeded=0,failed=0,cancelled=0,tokens=0,cost=0,human=0,toolOk=0,toolBad=0,auto=0;
  const issues=new Set(),durations=[];
  let costKnown=true,active=0;
  for(const t of traces){
    if(t.status==='succeeded')succeeded++;
    else if(t.status==='failed')failed++;
    else if(t.status==='cancelled')cancelled++;
    if(t.status==='running'||t.status==='waiting')active++;
    tokens+=Number(t.tokens||0);
    cost+=Number(t.costCny||0);
    if(t.costKnown===false)costKnown=false;
    human+=Number(t.humanInterventions||0);
    toolOk+=Number(t.toolCallsSucceeded||0);
    toolBad+=Number(t.toolCallsFailed||0);
    if(t.origin!=='external'&&t.status==='succeeded'&&!Number(t.humanInterventions||0))auto++;
    if(t.issueId)issues.add(t.issueId);
    if(t.origin==='external'){if(t.executionDurationMs!=null)durations.push(Number(t.executionDurationMs))}
    else if(t.durationMs)durations.push(Number(t.durationMs));
  }
  const covered=traces.filter(t=>t.origin!=='external'),coveredSucceeded=covered.filter(t=>t.status==='succeeded').length;
  const total=traces.length,attempts=succeeded+failed,observed=toolOk+toolBad;
  const point=(state.analytics?.trend||[]).find(p=>p.date===obsPeriodKey())||{};
  const conversationTokens=Number(point.conversationTokens||0),conversationCost=Number(point.conversationCostCny||0);
  return {
    period:true,traces,total,succeeded,failed,cancelled,reworked:null,uniqueIssues:issues.size,tokens:tokens+conversationTokens,sopTokens:tokens,conversationTokens,conversationCost,
    conversationIssues:Number(point.conversationIssues||0),conversationWorkspaces:Number(point.conversationWorkspaces||0),conversationLinked:null,inputTokens:null,outputTokens:null,
    cost:cost+conversationCost,costKnown,active,p95:obsPercentile(durations,95),avgExec:durations.length?Math.round(durations.reduce((a,b)=>a+b,0)/durations.length):0,
    wait:0,runsPerIssue:issues.size?traces.filter(t=>t.issueId).length/issues.size:null,firstPass:null,successRate:attempts?succeeded*100/attempts:0,
    demo:{
      coveredRuns:covered.length,coveredSucceededRuns:coveredSucceeded,averageTokensPerRun:total?tokens/total:0,toolCallsObserved:observed,toolCallsSucceeded:toolOk,toolCallsFailed:toolBad,
      toolSuccessRate:observed?toolOk*100/observed:0,humanInterventions:human,averageHumanInterventions:covered.length?human/covered.length:0,
      automatedSucceededRuns:auto,automationRate:coveredSucceeded?auto*100/coveredSucceeded:0
    }
  };
}
function ensureObsUserFilter(){
  if(state.obsUserFilterReady)return;
  state.obsUserFilterReady=true;
  if(state.obsScope==='project')return;
  const user=state.currentUser;
  if(user&&!user.isGuest&&user.id)state.obsUserFilter=String(user.id);
}
function setObsScope(scope){
  scope=scope==='project'?'project':'user';
  if(obsScope()===scope)return;
  if(scope==='project')state.obsUserSaved=state.obsUserFilter||'';
  state.obsScope=scope;
  localStorage.setItem('agentflow.obsScope',scope);
  if(scope==='user'&&!state.obsUserFilter){
    state.obsUserFilter=state.obsUserSaved||(state.currentUser&&!state.currentUser.isGuest&&state.currentUser.id?String(state.currentUser.id):'');
  }
  resetListPages('obs-');
  applyObsFilter();
}
async function switchObsTab(tab){
  tab=obsNormalizeTab(tab);
  state.obsTab=tab;
  $$('#obsTabs .obs-tab').forEach(x=>x.classList.toggle('active',x.dataset.obsTab===tab));
  if(tab==='delivery'&&!state.obsAnalyticsFull){
    const host=$('#obsBody');
    if(host)host.innerHTML=`<div class="obs-pane active"><div class="detail-empty">正在加载交付数据…</div></div>`;
    try{
      state.analytics=await api('/api/analytics'+analyticsQuery({overview:false}),{silent:true});
      state.obsAnalyticsFull=true;
    }catch(e){toast(e.message)}
  }
  renderObservability();
}
async function refreshObs(){
  if(refreshObsPending)return refreshObsPending;
  refreshObsPending=(async()=>{
    ensureObsUserFilter();
    const host=$('#obsBody');
    if(host)host.innerHTML=`<div class="obs-pane active"><div class="detail-empty">正在加载用量数据…</div></div>`;
    state.obsAnalyticsFull=false;
    try{
      const opt={silent:true};
      const overview=state.obsTab!=='delivery';
      const analytics=await api('/api/analytics'+analyticsQuery({overview}),opt);
      state.analytics=analytics;
      if(!overview)state.obsAnalyticsFull=true;
    }catch(e){toast(e.message)}
    finally{refreshObsPending=null}
    renderObservability();
    refreshObsCrossProject().then(()=>{if(activeViewName()==='observability')renderObservability()});
  })();
  return refreshObsPending;
}
function runCost(r){return Number((state.analytics?.runTraces||[]).find(t=>t.runId===r.id)?.costCny||0)}
function runTokens(r){return Number(r.usage?.totalTokens||0)}
function runArtifactCount(r){return Number(r?.artifactCount??r?.artifacts?.length??0)}
function obsKpiRow(items){return `<div class="obs-kpi-grid">${items.map(c=>`<div class="obs-metric" style="--accent:${c.color}"><span>${c.label}</span><b>${c.value}</b><small>${c.foot||''}</small></div>`).join('')}</div>`}
function analyticsQuery(opts){
  opts=opts||{};
  const parts=[];
  if(!opts.crossProject){
    const base=projectQuery().replace(/^\?/,'');
    if(base)parts.push(base);
  }
  if(opts.overview)parts.push('scope=overview');
  if(obsScope()==='user'&&state.obsUserFilter)parts.push('userId='+encodeURIComponent(state.obsUserFilter));
  if(state.obsOriginFilter)parts.push('origin='+encodeURIComponent(state.obsOriginFilter));
  if(state.obsSourceFilter)parts.push('issueSource='+encodeURIComponent(state.obsSourceFilter));
  if(['week','month'].includes(obsApiGrain()))parts.push('grain='+obsApiGrain());
  const tz=obsTimeZone();
  if(tz)parts.push('tz='+encodeURIComponent(tz));
  return parts.length?'?'+parts.join('&'):'';
}
async function refreshObsCrossProject(){
  if(obsScope()!=='user'||!state.obsUserFilter){state.obsCrossProject=null;return}
  try{
    state.obsCrossProject=await api('/api/analytics'+analyticsQuery({overview:true,crossProject:true}),{silent:true});
  }catch(e){state.obsCrossProject=null}
}
async function applyObsFilter(){
  try{
    const overview=state.obsTab!=='delivery';
    const [analytics]=await Promise.all([
      api('/api/analytics'+analyticsQuery({overview}),{silent:true}),
    ]);
    state.analytics=analytics;
    state.obsAnalyticsFull=!overview;
  }catch(e){toast(e.message)}
  renderObservability();
  refreshObsCrossProject().then(()=>{if(activeViewName()==='observability')renderObservability()});
}
function obsUserName(id){
  if(!id||id==='unassigned')return '未分配';
  const user=userById(id);
  return user?userDisplay(user):String(id);
}
function obsLinkedIssue(run){
  const task=(state.data.tasks||[]).find(t=>t.runId===run?.id);
  const issueId=task?.issueId||run?.executionContext?.issueId;
  return issueId&&(state.data.issues||[]).find(i=>i.id===issueId);
}
function obsRunMatchesUser(run,uid){
  if(!uid)return true;
  const want=String(uid);
  if(String(run?.executionContext?.responsibleUserId||'')===want)return true;
  return String(obsLinkedIssue(run)?.assigneeUserId||'')===want;
}
function obsUserChoices(){
  const me=Number(state.currentUser?.id||0);
  const project=currentProject();
  const memberIds=new Set((project?.members||[]).map(m=>Number(m.userId)));
  if(project?.ownerId)memberIds.add(Number(project.ownerId));
  return [...(state.data.users||[])].sort((a,b)=>{
    const aid=Number(a.id),bid=Number(b.id);
    if(aid===me)return -1;
    if(bid===me)return 1;
    const am=memberIds.has(aid),bm=memberIds.has(bid);
    if(am!==bm)return am?-1:1;
    return userDisplay(a).localeCompare(userDisplay(b),'zh');
  });
}
function obsFilterBar(){
  ensureObsUserFilter();
  const project=currentProject();
  const projectName=project?.name||project?.id||'当前项目';
  const scope=obsScope();
  const selected=scope==='user'?(state.obsUserFilter||''):'';
  const current=userById(selected);
  const label=current?userDisplay(current):(selected?obsUserName(selected):'全部成员');
  const me=String(state.currentUser?.id||'');
  const people=obsUserChoices();
  const userOptions=[`<option value=""${!selected?' selected':''} data-search="all 全部成员">全部责任人</option>`,
    ...people.map(u=>{const id=String(u.id);return `<option value="${escapeHTML(id)}"${id===String(selected)?' selected':''} data-search="${escapeHTML(`${u.loginName||''} ${u.chineseName||''}`)}"${id===me?' data-meta="我"':''}>${escapeHTML(userDisplay(u))}</option>`})];
  if(selected&&!people.some(u=>String(u.id)===String(selected)))userOptions.push(`<option value="${escapeHTML(String(selected))}" selected>${escapeHTML(label)}</option>`);
  const originChoices=[['','全部执行'],['internal','平台内'],['external','外部工作流']];
  const sourceChoices=[['','全部来源'],['local','本地'],['github','GitHub']];
  const sources=sourceChoices.map(([v,l])=>`<option value="${v}" ${(state.obsSourceFilter||'')===v?'selected':''}>${l}</option>`).join('');
  const grain=obsTrendGrain();
  const hint=scope==='project'
    ?`项目指标 = 顶栏当前项目「${projectName}」全部成员的运行、Token 和交付，不是全平台汇总。${grain==='all'?'当前看全部历史，趋势按月聚合，点一个月可下钻。':'上方数字和 Loop 指标只含所选时段；点趋势图或用两侧箭头换一天 / 一周 / 一月。'}`
    :`个人指标 = 责任人在当前项目「${projectName}」里的执行。一人多项目时，下方「各项目」看他在其他项目的量，不会加进上面的 KPI。${grain==='all'?'当前看全部历史，趋势按月聚合，点一个月可下钻。':'上方数字和 Loop 指标只含所选时段；点趋势图或用两侧箭头换一天 / 一周 / 一月。'}`;
  const userFilter=scope==='user'?`<select id="obsUserFilter" data-obs-select data-icon="user" aria-label="责任人">${userOptions.join('')}</select>`:'';
  const originFilter=`<select id="obsOriginFilter" data-obs-select data-icon="run-live" aria-label="执行">${originChoices.map(([v,l])=>`<option value="${v}" ${(state.obsOriginFilter||'')===v?'selected':''}>${l}</option>`).join('')}</select>`;
  const sourceFilter=`<select id="obsSourceFilter" data-obs-select data-icon="connector" aria-label="需求来源">${sources}</select>`;
  const clear=(kind,title)=>`<button type="button" data-obs-clear-filter="${kind}" title="${title}" aria-label="${title}">${obsIcon('close',11)}</button>`;
  const chips=[];
  if(scope==='project')chips.push(`<span class="obs-chip">项目「${escapeHTML(projectName)}」全部成员</span>`);
  else if(selected)chips.push(`<span class="obs-chip">「${escapeHTML(projectName)}」项目内 · ${escapeHTML(label)} 负责的执行${clear('user','清除责任人')}</span>`);
  else chips.push(`<span class="obs-chip">「${escapeHTML(projectName)}」项目内 · 全部成员</span>`);
  if(state.obsOriginFilter)chips.push(`<span class="obs-chip">${escapeHTML((originChoices.find(([v])=>v===state.obsOriginFilter)||[,state.obsOriginFilter])[1])}${clear('origin','清除执行来源')}</span>`);
  if(state.obsSourceFilter)chips.push(`<span class="obs-chip">${escapeHTML(obsSourceLabel(state.obsSourceFilter))}需求${clear('source','清除需求来源')}</span>`);
  if(grain!=='all'){
    const range=obsPeriodRange(obsPeriodKey(),grain);
    chips.push(`<span class="obs-chip">${escapeHTML(range?range.label:obsRangeCaption())}${clear('period','回到全部')}</span>`);
  }
  const manage=obsCanManageSources()?`<button class="obs-btn ghost" id="obsManageSources" type="button" title="外部数据源" aria-label="外部数据源">${obsIcon('integration')}<span>外部数据源</span></button>`:'';
  return `<div class="obs-fbar"><div class="obs-fbar-row">${obsSeg('data-obs-scope',[['user','个人',0,'','user'],['project','项目',0,'','users']],scope)}${userFilter}${originFilter}${sourceFilter}<span class="obs-sp"></span>${obsSeg('data-obs-grain',[['all','全部'],['day','日'],['week','周'],['month','月']],grain)}${obsPeriodPicker()}${manage}</div><div class="obs-scopeline"><span>统计范围</span>${chips.join('')}<span class="obs-info" tabindex="0">${obsIcon('info',13)}口径说明<span class="obs-tip" role="tooltip">${escapeHTML(hint)}</span></span></div></div>`;
}
function obsFilteredRuns(){
  let runs=scopedRuns(state.data.runs||[]);
  if(obsScope()==='user'&&state.obsUserFilter)runs=runs.filter(r=>obsRunMatchesUser(r,state.obsUserFilter));
  if(state.obsSourceFilter)runs=runs.filter(r=>String(obsLinkedIssue(r)?.source||'local').toLowerCase()===state.obsSourceFilter);
  return runs;
}
function obsProjectName(id){
  if(!id||id==='unassigned')return '未归属项目';
  const project=(state.data.projects||[]).find(p=>p.id===id);
  return project?.name||project?.key||id;
}
function obsUserTotals(){
  const bag={};
  for(const u of state.analytics?.userUsage||[]){
    const id=u.userId||'unassigned';
    const row=bag[id]||(bag[id]={userId:id,runs:0,success:0,failed:0,tokens:0,inputTokens:0,outputTokens:0,costCny:0,costKnown:true,executionDurationMs:0});
    row.runs+=u.runs||0;
    row.success+=u.success||0;
    row.failed+=u.failed||0;
    row.tokens+=u.tokens||0;
    row.inputTokens+=u.inputTokens||0;
    row.outputTokens+=u.outputTokens||0;
    row.costCny+=u.costCny||0;
    if(u.costKnown===false)row.costKnown=false;
    row.executionDurationMs+=u.executionDurationMs||0;
  }
  return Object.values(bag).sort((a,b)=>(b.tokens-a.tokens)||(b.runs-a.runs));
}
function obsTableCard(title,sub,headers,rows,colspan,pageKey){
  const list=Array.isArray(rows)?rows:(rows?[rows]:[]);
  const paged=pageKey?paginateList(pageKey,list):{items:list,total:list.length,size:list.length||1,page:1,pages:1,start:0,end:list.length};
  const body=paged.items.join('')||`<tr><td colspan="${colspan||headers.length}" class="detail-empty">暂无数据</td></tr>`;
  return `<div class="obs-card" style="margin-bottom:14px"><div class="obs-card-title">${title}${sub?`<span class="sub">${sub}</span>`:''}</div><table class="obs-table"><thead><tr>${headers.map(h=>`<th>${h}</th>`).join('')}</tr></thead><tbody>${body}</tbody></table>${pageKey?pagerHTML(pageKey,paged):''}</div>`;
}
// costLabel 区分"成本为 0"与"成本未识别"，未识别时展示占位而不是 ¥0。
function costLabel(value,known){
  const amount=Number(value||0);
  if(known===false&&amount<=0)return '<span class="muted">未识别</span>';
  return '¥'+(amount>0&&amount<.01?amount.toPrecision(3):amount.toFixed(2))+(known===false?'（部分未识别）':'');
}
function obsIcon(name,size){
  size=size||14;
  return `<svg class="agent-icon harness-icon" data-icon="${name}" width="${size}" height="${size}" aria-hidden="true" focusable="false"></svg>`;
}
function obsHydrateIcons(root){window.AgentFlowHarnessIcons?.hydrate?.(root||document)}
const OBS_EXPAND_ICON=obsIcon('expand');
const OBS_COLLAPSE_ICON=obsIcon('close');
// Harness Selector panels float on <body> (or inside the sources dialog) and
// outlive the innerHTML re-render that dropped their native select.
function obsDisposeSelects(){
  (state.obsSelects||[]).forEach(api=>api.dispose?.());
  state.obsSelects=[];
}
function obsEnhanceSelects(root,panelHost){
  const list=[];
  root.querySelectorAll('select[data-obs-select]').forEach(sel=>{
    const api=window.enhanceSelect?.(sel);
    if(!api)return;
    if(panelHost)panelHost.appendChild(api.panel);
    list.push(api);
  });
  return list;
}
let obsCardBuilders={};
let obsExpandState=null;
function obsChartFormatters(theme,reduced){return {theme,reduced,fmtTokens:formatTokens,fmtCost:v=>'¥'+Number(v||0).toFixed(2),label:obsTrendLabel}}
function renderObservability(){
  ensureObsUserFilter();
  renderObsTabs();
  const host=$('#obsBody');if(!host)return;
  state.obsTab=obsNormalizeTab(state.obsTab);
  $$('#obsTabs .obs-tab').forEach(b=>b.classList.toggle('active',b.dataset.obsTab===state.obsTab));
  const summary=state.analytics?.summary||{};
  $('#obsLiveState').classList.toggle('active',((summary.activeRuns||0)+(summary.waitingRuns||0))>0);
  const badge=$('#obsDeliveryCount');
  if(badge){const n=state.analytics?.delivery?.issues||0;badge.hidden=!n;badge.textContent=n?`${n} 单`:''}
  const reopen=obsExpandState?.key||'';
  if(reopen)obsCollapse({silent:true});
  obsDisposeCharts(host);
  obsDisposeSelects();
  obsCardBuilders={};
  const tab=state.obsTab;
  {
    const html=tab==='delivery'?obsDelivery():obsOverview();
    host.innerHTML=`<div class="obs-pane active">${obsFilterBar()}${html}</div>`;
    state.obsSelects=obsEnhanceSelects(host);
    host.querySelectorAll('[data-obs-scope]').forEach(b=>b.onclick=()=>setObsScope(b.dataset.obsScope));
    host.querySelectorAll('[data-obs-grain]').forEach(b=>b.onclick=()=>setObsTrendGrain(b.dataset.obsGrain));
    host.querySelectorAll('[data-obs-period-step]').forEach(b=>b.onclick=()=>obsShiftPeriod(Number(b.dataset.obsPeriodStep)));
    const periodInput=host.querySelector('#obsPeriodInput');
    if(periodInput)periodInput.onchange=()=>setObsPeriod(periodInput.value);
    const userSelect=host.querySelector('#obsUserFilter');
    if(userSelect)userSelect.onchange=()=>{state.obsUserFilter=userSelect.value||'';resetListPages('obs-');applyObsFilter()};
    const originSelect=host.querySelector('#obsOriginFilter');if(originSelect)originSelect.onchange=()=>{state.obsOriginFilter=originSelect.value;resetListPages('obs-');applyObsFilter()};
    const sourceButton=host.querySelector('#obsManageSources');if(sourceButton)sourceButton.onclick=openObsSources;
    const obsSourceSel=host.querySelector('#obsSourceFilter');if(obsSourceSel)obsSourceSel.onchange=()=>{state.obsSourceFilter=obsSourceSel.value;resetListPages('obs-');applyObsFilter()};
  }
  obsBindActions(host);
  bindPager(host);
  obsHydrateIcons(host);
  obsMountCharts(host);
  if(reopen){const card=host.querySelector(`[data-obs-card="${reopen}"]`);if(card)obsExpand(card,{silent:true})}
}
// obsBindActions 绑定卡片内的交互；卡片局部重渲染后会对新节点重新调用。
function obsBindActions(root){
  root.querySelectorAll('[data-obs-metric]').forEach(b=>b.onclick=()=>setObsTrendMetric(b.dataset.obsMetric));
  root.querySelectorAll('[data-obs-period]').forEach(el=>el.onclick=e=>{e.stopPropagation();const key=el.dataset.obsPeriod;if(key)obsSelectTrendPeriod(key)});
  root.querySelectorAll('[data-open-run]').forEach(b=>b.onclick=e=>{if(e.target.closest&&e.target.closest('a'))return;obsCollapse();openRunFromId(b.dataset.openRun)});
  root.querySelectorAll('[data-jump]').forEach(b=>b.onclick=e=>{e.preventDefault();obsCollapse();switchView(b.dataset.jump)});
  root.querySelectorAll('[data-edit-issue]').forEach(b=>b.onclick=e=>{if(e.target.closest&&e.target.closest('a'))return;obsCollapse();const issue=(state.data.issues||[]).find(i=>i.id===b.dataset.editIssue);if(issue)openIssue(issue);else openIssueDetail(b.dataset.editIssue)});
  root.querySelectorAll('[data-obs-delivery]').forEach(b=>b.onclick=()=>obsSetDeliveryFilter(b.dataset.obsDelivery));
  root.querySelectorAll('[data-obs-funnel]').forEach(b=>b.onclick=e=>{e.preventDefault();obsSetDeliveryFilter(b.dataset.obsFunnel,true)});
  root.querySelectorAll('[data-obs-jump]').forEach(b=>b.onclick=e=>{e.preventDefault();obsJumpDetail(b.dataset.obsJump,b.dataset.obsHl||'')});
  root.querySelectorAll('[data-obs-bd-dim]').forEach(b=>b.onclick=()=>{if(b.disabled)return;state.obsBdDim=b.dataset.obsBdDim;obsRefreshCard('breakdown')});
  root.querySelectorAll('[data-obs-bd-metric]').forEach(b=>b.onclick=()=>{if(b.disabled)return;state.obsBdMetric=b.dataset.obsBdMetric;obsRefreshCard('breakdown')});
  root.querySelectorAll('[data-obs-bd-row]').forEach(row=>{
    const i=Number(row.dataset.obsBdRow);
    row.onmouseenter=()=>obsBreakdownHover(i);
    row.onmouseleave=()=>obsBreakdownHover(null);
    row.onclick=()=>obsBreakdownJump(i);
  });
  root.querySelectorAll('[data-obs-heat-metric]').forEach(b=>b.onclick=()=>{state.obsHeatMetric=b.dataset.obsHeatMetric;obsRefreshCard('heat')});
  root.querySelectorAll('[data-obs-detail]').forEach(b=>b.onclick=()=>{state.obsDetailKeys=state.obsDetailKeys||{};state.obsDetailKeys[obsDetailScope()]=b.dataset.obsDetail;state.obsDetailQuery='';state.obsDetailHighlight='';obsRefreshCard('detail')});
  root.querySelectorAll('[data-obs-detail-search]').forEach(input=>input.oninput=()=>{state.obsDetailQuery=input.value;(obsDetailCurrent()?.tables||[]).forEach(t=>{if(t.pageKey)resetListPage(t.pageKey)});obsRefreshCard('detail');const next=document.querySelector('[data-obs-card="detail"] [data-obs-detail-search]');if(next){next.focus();next.setSelectionRange(next.value.length,next.value.length)}});
  root.querySelectorAll('[data-obs-export-csv]').forEach(b=>b.onclick=()=>obsExportCsv(b.dataset.obsExportCsv));
  root.querySelectorAll('[data-obs-clear-filter]').forEach(b=>b.onclick=()=>obsClearFilter(b.dataset.obsClearFilter));
  root.querySelectorAll('[data-obs-expand]').forEach(b=>b.onclick=e=>{e.stopPropagation();const card=b.closest('[data-obs-card]');if(obsExpandState&&obsExpandState.card===card)obsCollapse();else obsExpand(card)});
}
function obsRefreshCard(key){
  const builder=obsCardBuilders[key];
  const old=document.querySelector(`[data-obs-card="${key}"]`);
  if(!builder||!old){renderObservability();return}
  obsDisposeCharts(old);
  const tpl=document.createElement('template');
  tpl.innerHTML=builder().trim();
  const next=tpl.content.firstElementChild;
  if(!next){old.remove();return}
  if(old.classList.contains('is-expanded'))next.classList.add('is-expanded');
  old.replaceWith(next);
  if(obsExpandState&&obsExpandState.card===old){
    obsExpandState.card=next;
    const btn=next.querySelector('[data-obs-expand]');
    if(btn){btn.innerHTML=OBS_COLLAPSE_ICON;btn.title='关闭';btn.setAttribute('aria-label','关闭放大窗口')}
    obsRenderExpandExtra();
  }
  obsBindActions(next);
  bindPager(next);
  obsHydrateIcons(next);
  obsMountCharts(next);
  return next;
}
function obsRefreshPaged(key){
  const btn=document.querySelector(`[data-page-key="${key}"]`);
  const card=btn?.closest('[data-obs-card]');
  if(card&&obsCardBuilders[card.dataset.obsCard])obsRefreshCard(card.dataset.obsCard);
  else renderObservability();
}
function obsClearFilter(kind){
  if(kind==='period'){setObsTrendGrain('all');return}
  if(kind==='user')state.obsUserFilter='';
  else if(kind==='origin')state.obsOriginFilter='';
  else if(kind==='source')state.obsSourceFilter='';
  else return;
  resetListPages('obs-');
  applyObsFilter();
}
function obsSelectTrendPeriod(key){
  if(obsTrendGrain()==='all')setObsPeriod(key,'month');
  else setObsPeriod(key);
}

// ---------- 公共组件 ----------
function obsSeg(attr,items,active,small){
  return `<div class="obs-seg${small?' sm':''}" role="group">${items.map(([id,label,disabled,title,icon])=>`<button type="button" ${attr}="${escapeHTML(id)}" class="${id===active?'active':''}"${disabled?' disabled':''}${title?` title="${escapeHTML(title)}"`:''}>${icon?obsIcon(icon):''}${label}</button>`).join('')}</div>`;
}
function obsCard(key,title,sub,tools,body,opts){
  opts=opts||{};
  const expand=opts.expand?`<button type="button" class="obs-x-btn" data-obs-expand="${escapeHTML(key)}" title="放大" aria-label="放大查看">${OBS_EXPAND_ICON}</button>`:'';
  return `<section class="obs-card2 ${opts.klass||''}" data-obs-card="${escapeHTML(key)}"><header class="obs-card-head"><div class="obs-card-heading"><h3>${title}</h3>${sub?`<p>${sub}</p>`:''}</div><div class="obs-card-tools">${tools||''}${expand}</div></header>${body}</section>`;
}
function obsSpark(values){
  const list=(values||[]).map(v=>Number(v)).filter(v=>Number.isFinite(v));
  if(list.length<2)return '<div class="obs-spark is-empty"></div>';
  const w=120,h=24,max=Math.max(...list),min=Math.min(...list);
  const d=list.map((v,i)=>(i?'L':'M')+(i*w/(list.length-1)).toFixed(1)+','+(h-2-(h-4)*(v-min)/((max-min)||1)).toFixed(1)).join('');
  return `<svg class="obs-spark" viewBox="0 0 ${w} ${h}" preserveAspectRatio="none" aria-hidden="true"><path d="${d}" fill="none" stroke="currentColor" stroke-width="1.3" vector-effect="non-scaling-stroke"/></svg>`;
}
function obsMeter(pct){
  const v=Math.max(0,Math.min(100,Number(pct)||0));
  return `<div class="obs-meter" aria-hidden="true"><i style="width:${v.toFixed(1)}%"></i></div>`;
}
// obsDelta 文字环比：invert 表示下降为向好（Token、成本、耗时）。
function obsDelta(cur,prev,invert,unit){
  cur=Number(cur);prev=Number(prev);
  if(!Number.isFinite(cur)||!Number.isFinite(prev))return '';
  if(unit==='pt'){
    const diff=cur-prev,up=diff>0.05,down=diff<-0.05;
    const tone=!up&&!down?'flat':((invert?down:up)?'good':'bad');
    return `<span class="obs-dl ${tone}">${up?'↑':down?'↓':'–'}${Math.abs(diff).toFixed(Math.abs(diff)>=10?0:1)} pt</span>`;
  }
  if(!prev)return '';
  const pct=(cur-prev)*100/prev,up=pct>0.05,down=pct<-0.05;
  const tone=!up&&!down?'flat':((invert?down:up)?'good':'bad');
  return `<span class="obs-dl ${tone}">${up?'↑':down?'↓':'–'}${Math.abs(pct).toFixed(Math.abs(pct)>=10?0:1)}%</span>`;
}
function obsKpi(o){
  const tip=o.tip&&o.tip.length?`<div class="obs-kpi-tip" role="tooltip">${o.tip.map(r=>Array.isArray(r)?`<div><span>${r[0]}</span><b>${r[1]}</b></div>`:`<p>${r}</p>`).join('')}</div>`:'';
  const viz=o.meter!=null?obsMeter(o.meter):(o.spark||'<div class="obs-spark is-empty"></div>');
  return `<div class="obs-kpi${tip?' has-tip':''}${o.missing?' is-missing':''}"${tip?' tabindex="0"':''}><div class="obs-kpi-label">${o.label}</div><div class="obs-kpi-value"><b class="num" data-obs-count>${o.value}</b>${o.unit?`<u>${o.unit}</u>`:''}${o.delta||''}</div>${viz}<div class="obs-kpi-foot">${o.foot||''}</div>${tip}</div>`;
}
function obsKpiGroup(title,items,aside){
  return `<div class="obs-kg"><h4><span>${title}</span>${aside?`<span>${aside}</span>`:''}</h4><div class="obs-ks">${items.map(obsKpi).join('')}</div></div>`;
}
function obsKpiGroups(groups,klass){
  return `<div class="obs-kgroups ${klass||''}">${groups.join('')}</div>`;
}
function obsInsightList(items){
  items=(items||[]).filter(Boolean).slice(0,3);
  if(!items.length)return '';
  return `<div class="obs-insights cols-${items.length}">${items.map(i=>`<div class="obs-ins"><p>${i.text}</p>${i.action?`<a href="#" ${i.action}>${escapeHTML(i.label||'查看')}</a>`:''}</div>`).join('')}</div>`;
}
function obsSignedTokens(v){return (v>0?'+':v<0?'−':'')+formatTokens(Math.abs(v))}
function obsSignedPct(p){return (p>0?'+':p<0?'−':'')+Math.abs(p).toFixed(Math.abs(p)>=10?0:1)+'%'}
function obsTraceSourceLabel(t){return t.origin==='external'?('外部 · '+(t.workflowName||'工作流')):(t.workflowName||obsSourceLabel(t.issueSource||'local'))}
function obsFailureGroups(){
  const groups=new Map();
  for(const f of state.analytics?.failures||[]){
    const code=String(f.code||'').trim()||'未知错误';
    const g=groups.get(code)||{code,count:0,nodes:new Set()};
    g.count+=Number(f.count||0);
    if(f.nodeId)g.nodes.add(f.nodeId);
    groups.set(code,g);
  }
  return [...groups.values()].sort((a,b)=>b.count-a.count);
}
function obsFailureLatest(code,traces){
  let latest=null;
  for(const t of traces||[]){
    if(t.status!=='failed'||!t.errorMessage)continue;
    const msg=String(t.errorMessage);
    if(!(msg.includes(code)||code.includes(msg.slice(0,60))))continue;
    const at=obsTraceEventAt(t);
    if(at&&(!latest||at>latest))latest=at;
  }
  return latest;
}
// obsInsights 只用确定性比较生成洞察；条件不满足时不出条。
function obsInsights(view,kind){
  const a=state.analytics||{};
  const items=[];
  if(kind==='delivery'){
    const d=a.delivery||{},summary=a.summary||{};
    const funnel=obsFunnelStats(obsDeliveryStages());
    if(d.issues){
      const worst=funnel.worst>=0?funnel.stages[funnel.worst]:null;
      const prevName=worst?funnel.stages[funnel.worst-1]?.name:'';
      items.push({text:`<b>${d.delivered||0} / ${d.issues}</b> 单已交付${worst?`，最大流失在「${escapeHTML(prevName)} → ${escapeHTML(worst.name)}」（${worst.lost} 单）`:''}`,action:`data-obs-funnel="${worst?.key||'all'}"`,label:'查看'});
    }
    // 端到端按需求平均、执行按运行平均，分母不同，只能并列，不能写成「其中」
    if(d.averageDurationMs&&d.averageExecutionDurationMs){
      items.push({text:`需求平均端到端 <b>${duration(d.averageDurationMs)}</b>，单次执行平均 ${duration(d.averageExecutionDurationMs)}`});
    }
    if(summary.runsPerIssue!=null&&d.issues){
      items.push({text:`每需求执行 <b>${Number(summary.runsPerIssue).toFixed(2)}</b> 次${summary.failuresPerIssue!=null?`，失败 ${Number(summary.failuresPerIssue).toFixed(2)} 次`:''}`});
    }
    return obsInsightList(items);
  }
  const trend=a.trend||[];
  const tokenDelta=obsTrendGrain()==='all'?null:obsPeriodDelta(trend,'tokens');
  if(tokenDelta&&Math.abs(tokenDelta.pct)>=1){
    const range=obsPeriodRange(obsPeriodKey(),obsTrendGrain());
    const prevRange=range?obsPeriodRange(obsStepPeriodKey(obsPeriodKey(),obsTrendGrain(),-1),obsTrendGrain()):null;
    const bag=new Map();
    for(const t of a.runTraces||[]){
      const cur=obsTraceInRange(t,range),prev=prevRange&&obsTraceInRange(t,prevRange);
      if(!cur&&!prev)continue;
      const name=obsTraceSourceLabel(t);
      bag.set(name,(bag.get(name)||0)+(cur?1:-1)*Number(t.tokens||0));
    }
    const top=[...bag.entries()].sort((x,y)=>Math.abs(y[1])-Math.abs(x[1]))[0];
    const sameDir=top&&Math.sign(top[1])===Math.sign(tokenDelta.delta);
    items.push({text:`Token 环比 <b>${obsSignedPct(tokenDelta.pct)}</b>${sameDir?`，主要来自 ${escapeHTML(top[0])}（${obsSignedTokens(top[1])}）`:''}`,action:'data-obs-jump="sops"',label:'SOP 明细'});
  }else{
    const flows=[...(a.workflows||[])].filter(w=>w.tokens>0).sort((x,y)=>y.tokens-x.tokens);
    const total=flows.reduce((n,w)=>n+w.tokens,0);
    if(flows.length>=2&&total)items.push({text:`<b>${escapeHTML(flows[0].name||flows[0].key)}</b> 占 Token ${(flows[0].tokens*100/total).toFixed(0)}%，共 ${flows.length} 个 SOP`,action:'data-obs-jump="sops"',label:'SOP 明细'});
  }
  const cache=obsCacheStats(view);
  const models=[...(a.models||[])].filter(m=>m.tokens>0).sort((x,y)=>y.tokens-x.tokens);
  const modelTotal=models.reduce((n,m)=>n+m.tokens,0);
  if(cache.rate!=null||models.length){
    const parts=[];
    if(cache.rate!=null)parts.push(`缓存命中 <b>${cache.rate.toFixed(0)}%</b>${cache.prevRate!=null?`，环比 ${(cache.rate-cache.prevRate)>=0?'+':'−'}${Math.abs(cache.rate-cache.prevRate).toFixed(0)} pt`:''}`);
    if(models.length&&modelTotal)parts.push(`${escapeHTML(models[0].name||models[0].key)} 占 ${(models[0].tokens*100/modelTotal).toFixed(0)}%`);
    items.push({text:parts.join('；'),action:'data-obs-jump="models"',label:'模型明细'});
  }
  const failed=view.failed||0;
  const groups=obsFailureGroups();
  if(failed>0&&groups.length){
    const all=groups.reduce((n,g)=>n+g.count,0);
    const top=groups[0];
    const credential=/api[ _-]?key|凭据|credential|unauthori[sz]ed|\b401\b|invalid[ _-]?token|token (?:invalid|expired)/i.test(top.code);
    const reason=view.period
      ?`，累计失败原因最多的是 ${escapeHTML(top.code)}（${top.count} 次）`
      :(groups.length===1||top.count===all?`，原因均为 ${escapeHTML(top.code)}`:`，最多的是 ${escapeHTML(top.code)}（${top.count} 次）`);
    items.push({text:`<b>${failed} 次失败</b>${reason}`,action:credential?'data-jump="settings"':'data-obs-jump="fails"',label:credential?'配置凭据':'失败明细'});
  }else if(failed>0){
    items.push({text:`<b>${failed} 次失败</b>`,action:'data-obs-jump="fails"',label:'失败明细'});
  }
  return obsInsightList(items);
}
function obsCacheStats(view){
  const summary=state.analytics?.summary||{};
  const list=obsTrendWindow(state.analytics?.trend||[]);
  if(!view.period){
    const base=Number(summary.inputTokens||0)+Number(summary.cacheReadTokens||0);
    return {rate:base?Number(summary.cacheHitRate||0):null,read:summary.cacheReadTokens||0,write:summary.cacheWriteTokens||0,prevRate:null,series:list.map(obsTrendCacheHit).filter(v=>v!=null),known:true};
  }
  const sel=obsPeriodKey();
  const idx=list.findIndex(p=>p.date===sel);
  const point=idx>=0?list[idx]:null;
  const prev=idx>0?list[idx-1]:null;
  return {rate:point?obsTrendCacheHit(point):null,read:point?.cacheReadTokens,write:point?.cacheWriteTokens,prevRate:prev?obsTrendCacheHit(prev):null,series:list.map(obsTrendCacheHit).filter(v=>v!=null),known:!!point};
}

// ---------- 明细面板 ----------
function obsRow(cells,attrs,cls){return {cells,attrs:attrs||'',cls:cls||''}}
function obsHtmlRows(list){
  return (list||[]).map(html=>{
    const m=String(html).match(/^<tr([^>]*)>([\s\S]*)<\/tr>$/);
    if(!m)return obsRow([html]);
    let attrs=m[1],cls='';
    attrs=attrs.replace(/\sclass="([^"]*)"/,(_,c)=>{cls=c;return ''});
    const cells=[...m[2].matchAll(/<td[^>]*>([\s\S]*?)<\/td>/g)].map(x=>x[1]);
    return obsRow(cells,attrs.trim(),cls);
  });
}
function obsDetailScope(){return 'overview'}
function obsDetailGroups(){return obsUsageDetailGroups(obsScopedView())}
function obsDetailItems(groups){return (groups||obsDetailGroups()).flatMap(g=>g.items).filter(Boolean)}
function obsDetailCurrent(groups){
  const items=obsDetailItems(groups);
  const want=(state.obsDetailKeys||{})[obsDetailScope()];
  return items.find(i=>i.key===want)||items[0]||null;
}
function obsDetailCount(item){
  if(item.count!=null)return item.count;
  return (item.tables||[]).reduce((n,t)=>n+(t.rows||[]).length,0);
}
function obsRowText(row){return row.cells.map(c=>obsCsvText(c)).join(' ').toLowerCase()}
function obsDetailTable(tbl,query,highlight){
  const q=String(query||'').trim().toLowerCase();
  const rows=(tbl.rows||[]).filter(r=>!q||obsRowText(r).includes(q));
  const hl=String(highlight||'').trim().toLowerCase();
  const paged=tbl.pageKey?paginateList(tbl.pageKey,rows):{items:rows,total:rows.length,size:rows.length||1,page:1,pages:1,start:0,end:rows.length};
  const body=paged.items.map(r=>{
    const cls=[r.cls,hl&&obsRowText(r).includes(hl)?'obs-row-hl':''].filter(Boolean).join(' ');
    return `<tr${cls?` class="${cls}"`:''}${r.attrs?' '+r.attrs:''}>${r.cells.map(c=>`<td>${c}</td>`).join('')}</tr>`;
  }).join('')||`<tr><td colspan="${tbl.headers.length}" class="detail-empty">${q?'没有匹配的行':escapeHTML(tbl.empty||'暂无数据')}</td></tr>`;
  return `${tbl.title?`<h4 class="obs-dtable-title">${tbl.title}${tbl.sub?`<span>${tbl.sub}</span>`:''}</h4>`:''}${tbl.lead||''}<div class="obs-tw"><table class="obs-table obs-dtable"><thead><tr>${tbl.headers.map(h=>`<th>${h}</th>`).join('')}</tr></thead><tbody>${body}</tbody></table></div>${tbl.pageKey?pagerHTML(tbl.pageKey,paged):''}`;
}
function obsDetailPanel(groups,active){
  groups=(groups||[]).map(g=>({...g,items:g.items.filter(Boolean)})).filter(g=>g.items.length);
  const items=groups.flatMap(g=>g.items);
  const item=items.find(i=>i.key===active)||items[0];
  if(!item)return '';
  const nav=groups.map(g=>`<h6>${g.group}</h6>`+g.items.map(it=>`<button type="button" data-obs-detail="${escapeHTML(it.key)}" class="${it.key===item.key?'active':''}">${it.title}<i>${obsDetailCount(it)}</i></button>`).join('')).join('');
  const query=state.obsDetailQuery||'';
  const tables=item.tables?.length;
  const tools=tables?`<input type="search" class="obs-dsearch" data-obs-detail-search placeholder="筛选当前表…" value="${escapeHTML(query)}" aria-label="筛选当前表"><button type="button" class="obs-btn" data-obs-export-csv="${escapeHTML(item.key)}">${obsIcon('download')}CSV</button>`:'';
  const body=tables?item.tables.map(t=>obsDetailTable(t,query,state.obsDetailHighlight)).join(''):(item.html||'');
  return `<section class="obs-dx" data-obs-card="detail" id="obsDetail"><nav class="obs-dnav" aria-label="明细分组">${nav}</nav><div class="obs-dmain"><div class="obs-dtool"><div class="obs-dtool-title"><h3>${item.title}</h3>${item.sub?`<p>${item.sub}</p>`:''}</div>${tools}</div>${body}</div></section>`;
}
function obsDetailCard(){return obsDetailPanel(obsDetailGroups(),(state.obsDetailKeys||{})[obsDetailScope()])}
function obsPageToHighlight(item,highlight){
  const hl=String(highlight||'').trim().toLowerCase();
  if(!item||!hl)return;
  for(const tbl of item.tables||[]){
    if(!tbl.pageKey)continue;
    const idx=(tbl.rows||[]).findIndex(r=>obsRowText(r).includes(hl));
    if(idx>=0){state.listPages=state.listPages||{};state.listPages[tbl.pageKey]=Math.floor(idx/listPageSize(tbl.pageKey))+1}
  }
}
function obsJumpDetail(key,highlight){
  obsCollapse();
  if(state.obsTab==='delivery'){obsSetDeliveryFilter(key||'all',true);return}
  state.obsDetailKeys=state.obsDetailKeys||{};
  state.obsDetailKeys[obsDetailScope()]=key;
  state.obsDetailQuery='';
  state.obsDetailHighlight=highlight||'';
  obsPageToHighlight(obsDetailCurrent(),state.obsDetailHighlight);
  const card=obsRefreshCard('detail');
  if(!card)return;
  card.scrollIntoView({behavior:obsReducedMotion()?'auto':'smooth',block:'start'});
  const row=card.querySelector('.obs-row-hl');
  const wrap=row?.closest('.obs-tw');
  if(row&&wrap)wrap.scrollTop=Math.max(0,row.offsetTop-48);
}

// ---------- CSV ----------
function obsCsvText(v){
  return obsPlain(v).replace(/&lt;/g,'<').replace(/&gt;/g,'>').replace(/&quot;/g,'"').replace(/&#39;/g,"'").replace(/&nbsp;/g,' ').replace(/&amp;/g,'&').replace(/\s+/g,' ').trim();
}
function obsCsvCell(v){
  let s=obsCsvText(v);
  if(/^[=+\-@\t\r]/.test(s))s="'"+s;
  return /[",\r\n]/.test(s)?`"${s.replace(/"/g,'""')}"`:s;
}
function obsCsvContent(tables){
  const lines=[];
  (tables||[]).forEach((t,i)=>{
    if(tables.length>1){if(i)lines.push('');lines.push(obsCsvCell(t.title||''))}
    lines.push(t.headers.map(obsCsvCell).join(','));
    for(const r of t.rows||[])lines.push(r.cells.map(obsCsvCell).join(','));
  });
  return '\ufeff'+lines.join('\r\n')+'\r\n';
}
function obsCsvFileName(title){
  const project=currentProject();
  const clean=s=>String(s||'').replace(/<[^>]+>/g,'').replace(/[\\/:*?"<>|\s]+/g,'_').replace(/^_+|_+$/g,'');
  return `${clean(title)||'明细'}-${clean(project?.name||project?.id||'全部项目')}-${new Date().toISOString().slice(0,10)}.csv`;
}
function obsExportCsv(key){
  let item=null;
  if(key==='deliveries')item=obsDeliveryTableDef(true);
  else item=obsDetailItems().find(i=>i.key===key);
  if(!item||!item.tables?.length){toast('当前表没有可导出的数据');return}
  const blob=new Blob([obsCsvContent(item.tables)],{type:'text/csv;charset=utf-8'});
  const a=document.createElement('a');
  a.href=URL.createObjectURL(blob);
  a.download=obsCsvFileName(item.title);
  document.body.appendChild(a);a.click();a.remove();
  setTimeout(()=>URL.revokeObjectURL(a.href),1500);
}

// ---------- 放大窗口 ----------
// 每个页面在自己的视图里放一个 .obs-expand，隐藏视图里的弹窗无法显示。
function obsExpandModal(card){
  const modal=card?.closest?.('.view')?.querySelector?.('.obs-expand')||document.getElementById('obsExpand');
  if(modal&&!modal.dataset.bound){
    modal.dataset.bound='1';
    modal.addEventListener('click',e=>{if(e.target===modal)obsCollapse()});
    document.addEventListener('keydown',e=>{if(e.key==='Escape'&&obsExpandState)obsCollapse()});
    modal.addEventListener('keydown',e=>{if(e.key==='Tab'&&obsExpandState)obsTrapFocus(modal,e)});
  }
  return modal;
}
const OBS_FOCUSABLE='a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';
function obsTrapFocus(modal,e){
  const box=modal.querySelector('.obs-expand-box');
  const list=[...(box?.querySelectorAll(OBS_FOCUSABLE)||[])].filter(el=>!el.hidden&&el.offsetParent!==null);
  if(!list.length){e.preventDefault();box?.focus({preventScroll:true});return}
  const first=list[0],last=list[list.length-1],active=document.activeElement;
  const inside=box.contains(active);
  if(e.shiftKey&&(active===first||!inside||active===box)){e.preventDefault();last.focus()}
  else if(!e.shiftKey&&(active===last||!inside)){e.preventDefault();first.focus()}
}
function obsExpandExtra(key){
  if(key!=='trend')return '';
  const list=obsTrendWindow(state.analytics?.trend||[]);
  if(!list.length)return '';
  const sel=obsPeriodKey();
  const rows=list.slice().reverse().map(p=>{const rate=obsTrendModeValue(p,'rate'),hit=obsTrendCacheHit(p);return `<tr class="run-row${p.date===sel?' obs-row-hl':''}" data-obs-period="${escapeHTML(p.date||'')}"><td>${escapeHTML(obsTrendLabel(p))}</td><td>${formatTokens(p.tokens)}</td><td>${costLabel(p.costCny,!p.costUnknownRuns)}</td><td>${p.runs||0}</td><td>${p.issues||0}</td><td>${rate==null?'—':rate.toFixed(0)+'%'}</td><td>${hit==null?'—':hit.toFixed(0)+'%'}</td></tr>`}).join('');
  return `<h4>逐${obsGrainCaption()}数据<span>点一行等同点图</span></h4><div class="obs-tw"><table class="obs-table"><thead><tr><th>${obsGrainCaption()}</th><th>Token</th><th>成本</th><th>运行</th><th>需求</th><th>成功率</th><th>缓存命中</th></tr></thead><tbody>${rows}</tbody></table></div>`;
}
function obsRenderExpandExtra(){
  const box=(obsExpandState?.modal||document.getElementById('obsExpand'))?.querySelector('.obs-expand-box');
  if(!box||!obsExpandState)return;
  box.querySelector('.obs-expand-extra')?.remove();
  const extra=obsExpandExtra(obsExpandState.key);
  if(!extra)return;
  box.insertAdjacentHTML('beforeend',`<div class="obs-expand-extra">${extra}</div>`);
  obsBindActions(box.querySelector('.obs-expand-extra'));
}
function obsExpand(card,opts){
  opts=opts||{};
  const modal=obsExpandModal(card);
  const box=modal?.querySelector('.obs-expand-box');
  if(!card||!box)return;
  if(obsExpandState)obsCollapse({silent:true});
  const holder=document.createComment('obs-expand');
  card.before(holder);
  box.innerHTML='';
  box.appendChild(card);
  card.classList.add('is-expanded');
  const trigger=card.querySelector('[data-obs-expand]');
  if(trigger){trigger.innerHTML=OBS_COLLAPSE_ICON;trigger.title='关闭';trigger.setAttribute('aria-label','关闭放大窗口');obsHydrateIcons(trigger)}
  obsExpandState={card,holder,key:card.dataset.obsCard,trigger,modal};
  obsRenderExpandExtra();
  modal.hidden=false;
  document.body.classList.add('obs-expand-open');
  box.focus({preventScroll:true});
  requestAnimationFrame(()=>{
    for(const el of card.querySelectorAll('[data-obs-chart]')){
      const entry=obsChartEntry(el.dataset.obsChart);
      if(entry&&entry.el===el){entry.chart.resize();if(!opts.silent)obsReplayChart(entry.key)}
    }
    obsMountCharts(card);
  });
}
function obsCollapse(opts){
  opts=opts||{};
  const st=obsExpandState;
  if(!st)return;
  obsExpandState=null;
  const modal=st.modal||document.getElementById('obsExpand');
  const box=modal?.querySelector('.obs-expand-box');
  const card=st.card;
  card.classList.remove('is-expanded');
  const btn=card.querySelector('[data-obs-expand]');
  if(btn){btn.innerHTML=OBS_EXPAND_ICON;btn.title='放大';btn.setAttribute('aria-label','放大查看');obsHydrateIcons(btn)}
  if(st.holder.isConnected)st.holder.replaceWith(card);
  else{obsDisposeCharts(card);card.remove()}
  if(box)box.innerHTML='';
  if(modal)modal.hidden=true;
  document.body.classList.remove('obs-expand-open');
  if(card.isConnected){
    for(const el of card.querySelectorAll('[data-obs-chart]')){const entry=obsChartEntry(el.dataset.obsChart);if(entry&&entry.el===el)entry.chart.resize()}
    if(!opts.silent)btn?.focus({preventScroll:true});
  }
}

// ---------- 数字滚动 ----------
function obsCountUp(el){
  if(!el||el.dataset.obsCounted)return;
  el.dataset.obsCounted='1';
  if(obsReducedMotion()||el.children.length)return;
  const text=el.textContent;
  const m=text.match(/^([^\d]*?)(\d[\d,]*(?:\.\d+)?)([\s\S]*)$/);
  if(!m||/\d/.test(m[3]))return;
  const raw=m[2].replace(/,/g,''),to=parseFloat(raw);
  if(!Number.isFinite(to)||to===0)return;
  const dec=(raw.split('.')[1]||'').length,comma=m[2].includes(',');
  const fmt=v=>{const s=comma?v.toLocaleString('en-US',{minimumFractionDigits:dec,maximumFractionDigits:dec}):v.toFixed(dec);return m[1]+s+m[3]};
  const start=performance.now(),dur=700;
  el.textContent=fmt(0);
  const step=now=>{
    if(!el.isConnected){el.textContent=text;return}
    const k=Math.min(1,Math.max(0,(now-start)/dur));
    if(k>=1){el.textContent=text;return}
    el.textContent=fmt(to*(1-Math.pow(1-k,3)));
    requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
}
function obsSourceLabel(s){return s==='gongfeng'?'工蜂':s==='tapd'?'TAPD':s==='local'?'本地':s==='github'?'GitHub':s||'—'}
function obsCategoryLabel(k){return ({requirement:'需求开发',bugfix:'Bug 修复',refactor:'技术优化',other:'其他'}[k])||k||'未分类'}
function obsTraceLabel(t){return [t.origin==='external'?'外部 · '+t.workflowName:obsSourceLabel(t.issueSource||'local'),t.externalId||t.issueId||t.runId].filter(Boolean).join(' ')}
function obsOverview(){
  const view=obsScopedView();
  obsCardBuilders={trend:obsTrendCard,breakdown:obsBreakdownCard,quality:obsQualityCard,heat:obsHeatCard,detail:obsDetailCard};
  const heat=obsHeatCard();
  return `${obsInsights(view,'usage')}
  ${obsUsageKpis(view)}
  ${obsTrendCard()}
  <div class="obs-grid2">${obsBreakdownCard()}${obsQualityCard()}</div>
  ${heat}
  ${obsDetailCard()}`;
}
function obsUsageKpis(view){
  const summary=state.analytics?.summary||{};
  const demo=view.demo||{};
  const trend=state.analytics?.trend||[];
  const list=obsTrendWindow(trend);
  const all=obsTrendGrain()==='all';
  const rangeName=obsRangeCaption();
  const total=view.total||0,succeeded=view.succeeded||0,failed=view.failed||0,reworked=view.reworked;
  const totalTokens=view.tokens||0,totalCost=view.cost||0;
  const d=metric=>all?null:obsPeriodDelta(trend,metric);
  const dl=(delta,invert)=>delta?obsDelta(delta.current,delta.previous,invert):'';
  const prevLabel=d('tokens')?.previousLabel||'';
  const cache=obsCacheStats(view);
  const spark=metric=>obsSpark(list.map(p=>metric==='rate'?obsTrendModeValue(p,'rate'):obsTrendValue(p,metric)).filter(v=>v!=null));
  const cum=(text)=>view.period?text:'';
  const tokenFoot=view.inputTokens!=null?`入 ${formatTokens(view.inputTokens)} / 出 ${formatTokens(view.outputTokens)}`:(view.period?`该${obsGrainCaption()} ${formatTokens(totalTokens)}`:`累计 ${formatTokens(totalTokens)}`);
  const successFoot=`${succeeded} 成功 / ${failed} 失败${view.firstPass!=null?` · 一次通过 ${total?percent(view.firstPass):'—'}`:''}`;
  const costFoot='人民币 · Hy 上报 / 其他公价';
  const deltaTip=prevLabel?[`环比对象：${escapeHTML(prevLabel)}`]:[];
  const convTokens=view.conversationTokens||0,convCost=view.conversationCost||0;
  const convTip=convTokens?[['其中对话改代码',formatTokens(convTokens)]]:[];
  const usage=obsKpiGroup('投入 · SOP + 对话改代码',[
    {label:`${rangeName} Token`,value:formatTokens(totalTokens),delta:dl(d('tokens'),true),spark:spark('tokens'),foot:tokenFoot,tip:[['输入',view.inputTokens!=null?formatTokens(view.inputTokens):'—'],['输出',view.outputTokens!=null?formatTokens(view.outputTokens):'—'],['缓存读',formatTokens(cache.read||0)],['缓存写',formatTokens(cache.write||0)],...convTip,...(view.period?[['累计',formatTokens(summary.totalTokens)]]:[]),...deltaTip]},
    {label:`${rangeName}成本`,value:costLabel(totalCost,view.costKnown),delta:dl(d('cost'),true),spark:spark('cost'),foot:costFoot+cum(` · 累计 ${costLabel(summary.totalCostCny,summary.costUnknownRuns===0)}`),tip:[...(convCost?[['其中对话改代码',costLabel(convCost,true)]]:[]),'人民币统计：Hy 保留上报金额；其他模型按公开标准单价测算。优先人民币定价，美元按 1 USD = 7 CNY 折算。缺少模型或 Token 分项时标为未识别；不代表实际结算账单。',...deltaTip]},
    {label:'缓存命中',value:cache.rate==null?'—':cache.rate.toFixed(0),unit:cache.rate==null?'':'%',delta:cache.rate!=null&&cache.prevRate!=null?obsDelta(cache.rate,cache.prevRate,false,'pt'):'',meter:cache.rate==null?0:cache.rate,foot:`缓存读 ${formatTokens(cache.read||0)}`,tip:['缓存命中 = 缓存读 ÷（输入 + 缓存读）。越高越省。']},
  ]);
  const output=obsKpiGroup('SOP 产出与质量',[
    {label:`${rangeName}运行`,value:String(total),delta:dl(d('runs')),spark:spark('runs'),foot:`${view.active||0} 进行中${cum(` · 累计 ${summary.totalRuns||0}`)}`,tip:['只统计 SOP 运行，对话工作区不算运行。',['成功',String(succeeded)],['失败',String(failed)],['取消',String(view.cancelled||0)],...deltaTip]},
    {label:`${rangeName}需求`,value:String(view.uniqueIssues||0),delta:dl(d('issues')),spark:spark('issues'),foot:`每单 ${view.runsPerIssue!=null?Number(view.runsPerIssue).toFixed(1):'—'} 次${cum(` · 累计 ${summary.uniqueIssues||0}`)}`,tip:['有 SOP 运行的需求，按关联的 Issue / TAPD 单去重；对话改代码的需求见下方「对话改代码」。',...deltaTip]},
    {label:'成功率',value:total?percent(view.successRate):'—',meter:total?view.successRate:0,foot:successFoot,tip:['成功率 = 成功 ÷（成功 + 失败），不含取消与进行中。']},
    {label:'P95 执行',value:view.p95?duration(view.p95):'—',foot:`平均 ${view.avgExec?duration(view.avgExec):'—'}${view.wait?` · 等待 ${duration(view.wait)}`:''}`,tip:['P95：95% 的执行在这个时间内完成。外部工作流按其上报的执行耗时统计。']},
  ],escapeHTML(rangeName));
  const conv=obsConversationKpis(view,rangeName,totalTokens,totalCost);
  const loop=obsKpiGroup('Loop 自动化',[
    {label:'平均 Token / Run',value:total?formatTokens(demo.averageTokensPerRun):'—',foot:`SOP ${formatTokens(view.sopTokens??totalTokens)} ÷ ${total} 次运行`},
    {label:'工具调用成功率',value:demo.toolCallsObserved?percent(demo.toolSuccessRate):'—',meter:demo.toolCallsObserved?demo.toolSuccessRate:0,foot:`${demo.toolCallsSucceeded||0} 成功 / ${demo.toolCallsFailed||0} 失败 · 仅统计完成事件`},
    {label:'平均人工干预',value:demo.coveredRuns?Number(demo.averageHumanInterventions||0).toFixed(2):'—',foot:`${demo.humanInterventions||0} 次人工决策 ÷ ${demo.coveredRuns||0} 平台内 runs · 不含暂停/恢复`},
    {label:'自动完成率',value:demo.coveredSucceededRuns?percent(demo.automationRate):'—',meter:demo.coveredSucceededRuns?demo.automationRate:0,foot:`${demo.automatedSucceededRuns||0} 个全自动成功 / ${demo.coveredSucceededRuns||0} 个平台内成功`},
  ],`<span class="obs-info" tabindex="0">口径<span class="obs-tip" role="tooltip">${view.period?`仅统计 ${escapeHTML(rangeName)} 内的 Run，可点趋势换时段。`:'所有数字均可下钻到 workflow_task_id、run_id、trace_id 和原始事件。'}只统计 SOP 运行，不含对话改代码。自动完成率仅统计有平台事件的执行；外部汇总不推断人工干预。</span></span>`);
  return obsKpiGroups([output,usage])+(conv?obsKpiGroups([conv],'is-loop'):'')+obsKpiGroups([loop],'is-loop');
}
// obsConversationKpis groups everything done by changing code directly in a
// conversation, without an SOP. Empty when there is none.
function obsConversationKpis(view,rangeName,totalTokens,totalCost){
  const summary=state.analytics?.summary||{};
  if(!(summary.conversationWorkspaces||summary.conversationIssues||summary.conversationTokens))return '';
  const tokens=view.conversationTokens||0,cost=view.conversationCost||0,spaces=view.conversationWorkspaces||0;
  const share=(part,whole)=>whole>0?Math.min(100,part*100/whole):0;
  const pct=v=>v>0&&v<1?'不到 1%':Math.round(v)+'%';
  const tokenShare=share(tokens,totalTokens),costShare=share(cost,totalCost);
  return obsKpiGroup('对话改代码 · 不走 SOP',[
    {label:`${rangeName}需求`,value:String(view.conversationIssues||0),foot:`${view.period?`累计 ${summary.conversationIssues||0} 个，`:'其中 '}${summary.conversationIssuesWithMr||0} 个已提 PR · ${summary.conversationIssuesDelivered||0} 个已交付`,tip:['在对话里识别出、关联到对话工作区的需求，按需求去重。','只在对话里完成的需求，在「交付」页标为「对话直接交付」，PR 合入默认分支即算已交付。']},
    {label:`${rangeName} Token`,value:formatTokens(tokens),meter:tokenShare,foot:`占全部 Token ${pct(tokenShare)}`,tip:['已算进上方「投入」的 Token 合计。']},
    {label:`${rangeName}成本`,value:costLabel(cost,true),meter:costShare,foot:`占全部成本 ${pct(costShare)}`,tip:['已算进上方「投入」的成本合计。']},
    {label:`${rangeName}工作区`,value:String(spaces),foot:view.conversationLinked!=null?`${view.conversationLinked} 个已关联需求 · ${Math.max(0,spaces-view.conversationLinked)} 个未关联`:'按创建时间统计',tip:['对话里每打开一个代码工作区计 1 个。工作区不是 SOP 运行，不计入运行数、成功率和耗时。']},
  ],`<span class="obs-info" tabindex="0">口径<span class="obs-tip" role="tooltip">在对话里直接改代码、不走 SOP 的部分。Token 和成本已算进「投入」合计；工作区和需求不计入「SOP 产出与质量」和「Loop 自动化」。</span></span>`);
}

// ---------- 趋势 ----------
function obsTrendCard(){
  const trend=state.analytics?.trend||[];
  const list=obsTrendWindow(trend);
  const grainName=obsGrainCaption();
  const metric=obsTrendMetric();
  const tokenDelta=obsPeriodDelta(trend,'tokens'),issueDelta=obsPeriodDelta(trend,'issues');
  const last=list[list.length-1]||{};
  const seg=obsSeg('data-obs-metric',[['tokens','Token'],['comp','构成'],['cost','成本'],['runs','运行'],['issues','需求'],['rate','成功率']],metric,true);
  const compare=`<div class="obs-trend-compare"><span>最近一${grainName}（含未结束周期）Token <b>${obsFormatTrendValue(tokenDelta?tokenDelta.current:obsTrendValue(last,'tokens'),'tokens')}</b></span>${obsDeltaChip(tokenDelta,true)||'<em class="obs-delta flat">样本不足，暂无环比</em>'}<span>需求 <b>${obsFormatTrendValue(issueDelta?issueDelta.current:obsTrendValue(last,'issues'),'issues')}</b></span>${obsDeltaChip(issueDelta)||''}</div>`;
  const sel=obsPeriodKey();
  const prev=obsTrendPrevPoints(trend,list);
  const hasPrev=metric!=='comp'&&!!prev&&prev.some(p=>p&&obsTrendModeValue(p,metric)!=null);
  const legend=metric==='comp'?'<div class="obs-legend"><span><i style="background:var(--c1)"></i>输入</span><span><i style="background:var(--c2)"></i>输出</span><span><i style="background:var(--c3)"></i>缓存读</span><span><i style="background:var(--c4)"></i>缓存写</span></div>':`<div class="obs-legend"><span><i style="background:var(--c1)"></i>本期</span>${hasPrev?'<span><i class="dash"></i>上一周期</span>':''}</div>`;
  const chart=obsChartSlot('trend',{
    empty:!list.length,emptyText:'暂无趋势',
    option:(theme,reduced)=>obsTrendOption(list,metric,sel,prev,obsChartFormatters(theme,reduced)),
    fallback:()=>obsTrendChart(trend,metric==='comp'?'tokens':metric),
    axisClick:idx=>{const p=list[idx];if(p&&p.date)obsSelectTrendPeriod(p.date)}
  },240);
  return obsCard('trend','趋势变化',`按${grainName}对比；点击某${grainName}查看该时段指标`,seg,`${compare}${chart}${legend}`,{expand:true,klass:'obs-trend-card'});
}

// ---------- Token 去向 ----------
const OBS_BREAKDOWN_DIMS=[['sop','SOP'],['model','模型'],['node','节点'],['src','来源'],['origin','执行'],['cat','类型']];
function obsBreakdownRowsFor(dim){
  const a=state.analytics||{};
  if(dim==='model')return (a.models||[]).map(m=>({key:m.key,name:m.name||m.key,tokens:m.tokens,costCny:m.costCny}));
  if(dim==='node')return (a.nodes||[]).map(n=>({key:n.key,name:n.name||n.key,tokens:n.tokens,costCny:n.costCny||0}));
  if(dim==='src')return (a.issueSources||[]).map(s=>({key:s.key,name:obsSourceLabel(s.key),tokens:s.tokens,costCny:s.costCny}));
  if(dim==='cat')return (a.categories||[]).map(c=>({key:c.key,name:obsCategoryLabel(c.key),tokens:c.tokens,costCny:c.costCny}));
  if(dim==='origin'){
    const bag={internal:{key:'internal',name:'平台内',tokens:0,costCny:0},external:{key:'external',name:'外部工作流',tokens:0,costCny:0}};
    for(const t of a.runTraces||[]){const b=bag[t.origin==='external'?'external':'internal'];b.tokens+=Number(t.tokens||0);b.costCny+=Number(t.costCny||0)}
    return Object.values(bag).filter(r=>r.tokens||r.costCny);
  }
  return (a.workflows||[]).map(w=>({key:w.key,name:w.name||w.key,tokens:w.tokens,costCny:w.costCny}));
}
// Conversation workspaces are not SOP runs, so the SOP-based dimensions miss
// them; one slice keeps every ring equal to the Token / cost totals. Sources
// already carry the usage of conversations that are linked to a requirement.
function obsBreakdownConversationRow(dim){
  const s=state.analytics?.summary||{};
  if(dim==='src'||!(s.conversationTokens||s.conversationCostCny))return null;
  return {key:'__conversation',name:'对话改代码',tokens:Number(s.conversationTokens||0),costCny:Number(s.conversationCostCny||0),conversation:true};
}
function obsBreakdownRows(dim,metric){
  const conv=obsBreakdownConversationRow(dim);
  const pinned=conv&&obsBreakdownValue(conv,metric)>0?[conv]:[];
  const rows=obsBreakdownRowsFor(dim).filter(r=>obsBreakdownValue(r,metric)>0).sort((x,y)=>obsBreakdownValue(y,metric)-obsBreakdownValue(x,metric));
  const limit=6-pinned.length;
  if(rows.length<=limit)return [...rows,...pinned];
  // Conversation usage is often small: keep it visible instead of folding it.
  const rest=rows.slice(limit-1);
  return [...rows.slice(0,limit-1),{key:'__other',name:`其他 ${rest.length} 项`,tokens:rest.reduce((n,r)=>n+Number(r.tokens||0),0),costCny:rest.reduce((n,r)=>n+Number(r.costCny||0),0),other:true},...pinned];
}
function obsBreakdownState(){
  let dim=OBS_BREAKDOWN_DIMS.some(([k])=>k===state.obsBdDim)?state.obsBdDim:'sop';
  const metric=state.obsBdMetric==='cost'?'cost':'tokens';
  if(obsBreakdownRows(dim,metric).length<2){
    const alt=OBS_BREAKDOWN_DIMS.find(([k])=>obsBreakdownRows(k,metric).length>=2);
    if(alt&&!state.obsBdDim)dim=alt[0];
  }
  return {dim,metric};
}
function obsBreakdownTarget(dim){return ({sop:'sops',model:'models',node:'nodes',src:'dist',cat:'dist',origin:'traces'})[dim]||'sops'}
function obsBreakdownCard(){
  const {dim,metric}=obsBreakdownState();
  const rows=obsBreakdownRows(dim,metric);
  const total=obsBreakdownTotal(rows,metric);
  const fmt=metric==='cost'?(v=>'¥'+Number(v||0).toFixed(2)):formatTokens;
  const dims=obsSeg('data-obs-bd-dim',OBS_BREAKDOWN_DIMS.map(([k,l])=>{const n=obsBreakdownRows(k,metric).length;return [k,l,n<2,n<2?'该维度只有一项或没有数据':'']}),dim,true);
  const metrics=obsSeg('data-obs-bd-metric',[['tokens','Token'],['cost','成本']],metric,true);
  const legend=rows.map((r,i)=>`<button type="button" class="obs-bd-row" data-obs-bd-row="${i}" title="查看明细"><i style="background:${r.conversation?OBS_CONVERSATION_COLOR:['var(--c1)','var(--c2)','var(--c3)','var(--muted-2)','var(--c4)','var(--panel-3)'][Math.min(i,5)]}"></i><span>${escapeHTML(r.name)}</span><b>${fmt(obsBreakdownValue(r,metric))}</b><em>${total?(obsBreakdownValue(r,metric)*100/total).toFixed(0):0}%</em></button>`).join('');
  const chart=obsChartSlot('breakdown',{
    empty:!rows.length,emptyText:'暂无用量',
    option:(theme,reduced)=>obsBreakdownOption(rows,metric,obsChartFormatters(theme,reduced)),
    fallback:()=>`<div class="obs-bd-bars">${rows.map(r=>`<div><span>${escapeHTML(r.name)}</span>${obsMeter(total?obsBreakdownValue(r,metric)*100/total:0)}</div>`).join('')}</div>`,
    on:{click:p=>obsBreakdownJump(p.dataIndex),mouseover:p=>obsBreakdownMark(p.dataIndex),mouseout:()=>obsBreakdownMark(null)}
  },200);
  const cumulative=obsTrendGrain()!=='all';
  const center=rows.length?`<div class="obs-bd-center"><b>${fmt(total)}</b><span>${cumulative?'累计':''}${metric==='cost'?'成本':'Token'}</span></div>`:'';
  return obsCard('breakdown',metric==='cost'?'成本去向':'Token 去向',cumulative?'累计口径，不随所选时段变化；点一项跳到对应明细':'按维度拆分；点一项跳到对应明细',`${metrics}`,`<div class="obs-bd-dims">${dims}</div><div class="obs-bd"><div class="obs-bd-ring">${chart}${center}</div><div class="obs-bd-legend">${legend}</div></div>`,{expand:true});
}
function obsBreakdownMark(i){
  document.querySelectorAll('[data-obs-bd-row]').forEach(r=>r.classList.toggle('is-hover',i!=null&&Number(r.dataset.obsBdRow)===i));
}
function obsBreakdownHover(i){
  obsBreakdownMark(i);
  const entry=obsChartEntry('breakdown');
  if(!entry)return;
  entry.chart.dispatchAction({type:'downplay',seriesIndex:0});
  if(i!=null)entry.chart.dispatchAction({type:'highlight',seriesIndex:0,dataIndex:i});
}
function obsBreakdownJump(i){
  const {dim,metric}=obsBreakdownState();
  const row=obsBreakdownRows(dim,metric)[i];
  if(!row)return;
  if(row.conversation){state.obsDeliveryFilter='direct';switchObsTab('delivery');return}
  obsJumpDetail(obsBreakdownTarget(dim),row.other?'':(dim==='origin'?(row.key==='external'?'外部':''):row.name));
}

// ---------- 质量 ----------
function obsQualityCard(){
  const view=obsScopedView();
  const failed=view.failed||0,reworked=view.reworked,cancelled=view.cancelled||0,total=view.total||0;
  const groups=obsFailureGroups().slice(0,4);
  const max=Math.max(1,...groups.map(g=>g.count));
  const reasons=groups.map(g=>{
    const latest=obsFailureLatest(g.code,view.traces);
    return `<button type="button" class="obs-fail-row" data-obs-jump="fails" data-obs-hl="${escapeHTML(g.code.slice(0,60))}"><span title="${escapeHTML(g.code)}">${escapeHTML(g.code.slice(0,80))}</span><b>${g.count}</b><div class="obs-fail-meta">${obsMeter(g.count*100/max)}<em>${g.nodes.size?escapeHTML([...g.nodes].slice(0,2).join('、')):'Run'}${latest?' · '+relativeTime(latest.toISOString()):''}</em></div></button>`;
  }).join('');
  const body=`<div class="obs-signal-row"><div><b data-obs-count>${failed}</b><span>失败</span></div><div><b data-obs-count>${reworked==null?'—':reworked}</b><span>返工</span></div><div><b data-obs-count>${cancelled}</b><span>取消</span></div><div><b>${total?percent(view.successRate):'—'}</b><span>成功率</span></div></div>${reasons?`<h4 class="obs-sub-title">${view.period?'失败原因（累计）':'失败原因'}<span>${view.period?'不随所选时段变化；':''}点一条查看失败明细</span></h4><div class="obs-fails">${reasons}</div>`:'<div class="obs-ok">没有失败运行</div>'}`;
  return obsCard('quality','质量信号','失败优先处理',`<a href="#" class="obs-link" data-obs-jump="nodes">节点明细</a>`,body);
}

// ---------- 时段热力 ----------
function obsTraceWeekHour(traces){
  const bag=new Map();
  for(const t of traces||[]){
    const at=obsTraceEventAt(t);
    if(!at)continue;
    const weekday=(at.getDay()+6)%7,hour=at.getHours();
    const k=weekday*24+hour;
    const c=bag.get(k)||{weekday,hour,runs:0,tokens:0,costCny:0};
    c.runs++;c.tokens+=Number(t.tokens||0);c.costCny+=Number(t.costCny||0);
    bag.set(k,c);
  }
  return [...bag.values()];
}
function obsHeatCells(view){
  if(view.period)return obsTraceWeekHour(view.traces);
  return state.analytics?.weekHour||null;
}
function obsHeatCard(){
  const view=obsScopedView();
  const cells=obsHeatCells(view);
  if(cells==null&&!(view.traces||[]).length)return '';
  const list=cells||obsTraceWeekHour(view.traces);
  const metric=state.obsHeatMetric==='runs'?'runs':'tokens';
  const peak=obsHeatPeak(list,metric);
  const peakText=peak?`高峰 ${OBS_WEEKDAYS[peak.weekday]} ${String(peak.hour).padStart(2,'0')}:00 · ${metric==='runs'?peak.value+' 次':formatTokens(peak.value)}`:'';
  const chart=obsChartSlot('heat',{
    empty:!list.length,emptyText:'暂无执行时段数据',
    option:(theme,reduced)=>obsHeatOption(list,metric,obsChartFormatters(theme,reduced)),
    fallback:()=>{const grid=obsHeatMatrix(list);const max=Math.max(1,...grid.flat().map(c=>metric==='runs'?c.runs:c.tokens));return `<div class="obs-heat-fallback">${grid.map((row,d)=>`<div><span>${OBS_WEEKDAYS[d]}</span>${row.map((c,h)=>{const v=metric==='runs'?c.runs:c.tokens;return `<i title="${OBS_WEEKDAYS[d]} ${h}:00 · ${metric==='runs'?v+' 次':formatTokens(v)}" style="opacity:${v?(.15+.85*v/max).toFixed(2):.06}"></i>`}).join('')}</div>`).join('')}</div>`}
  },220);
  const tz=obsTimeZone();
  return obsCard('heat','执行时段',`星期 × 小时（${escapeHTML(tz)}）${peakText?' · '+peakText:''}`,obsSeg('data-obs-heat-metric',[['tokens','Token'],['runs','运行']],metric,true),chart,{expand:true});
}
function obsTimeZone(){
  try{return Intl.DateTimeFormat().resolvedOptions().timeZone||''}catch(e){return ''}
}

// ---------- 明细定义 ----------
function obsTraceRows(traces){
  return traces.map(t=>{
    const source=t.origin==='external'?'外部 · '+(t.workflowName||''):obsSourceLabel(t.issueSource||'local');
    const issueRef=t.externalId||t.issueId||'—';
    const issue=t.externalUrl?`<a href="${escapeHTML(safeURL(t.externalUrl))}" target="_blank" rel="noopener noreferrer">${escapeHTML(source+' '+issueRef)}</a>`:`${escapeHTML(source+' '+issueRef)}`;
    const task=t.workflowTaskId?`<code title="${escapeHTML(t.workflowTaskId)}">${escapeHTML(t.workflowTaskId.slice(-12))}</code>`:'—';
    const runId=String(t.runId||''),traceId=String(t.traceId||t.runId||'');
    const relation=t.origin==='external'?`<code title="${escapeHTML(t.externalId||'')}">外部执行 ${escapeHTML(t.externalId||'')}</code>`:`<div class="obs-trace-ids"><code title="${escapeHTML(runId)}">Run ${escapeHTML(runId.slice(-12))}</code><code title="${escapeHTML(traceId)}">Trace ${escapeHTML(traceId.slice(-12))}</code></div>`;
    const observed=(t.toolCallsSucceeded||0)+(t.toolCallsFailed||0);
    const tools=observed?`${t.toolCallsSucceeded||0}/${observed} 成功`:(t.toolCalls?`${t.toolCalls} 次 · 结果未上报`:'—');
    const error=t.errorMessage?`<span class="obs-trace-error" title="${escapeHTML(t.errorMessage)}">${escapeHTML(t.errorNodeId||'Run')} · ${escapeHTML(t.errorMessage)}</span>`:'—';
    return obsRow([issue,task,relation,`<span class="status ${escapeHTML(t.status||'')}">${runStatus(t.status)||escapeHTML(t.status||'—')}</span>`,t.durationMs?duration(t.durationMs):'—',formatTokens(t.tokens),costLabel(t.costCny,t.costKnown),tools,t.origin==='external'?'未上报':String(t.humanInterventions||0),error],t.origin==='external'?'':`data-open-run="${escapeHTML(runId)}"`,'run-row');
  });
}
function obsUsageDetailGroups(view){
  const a=state.analytics||{};
  const traces=view.traces||[];
  const stats=(a.workflows||[]).map(w=>({...w,name:w.name||w.key}));
  const nodeStats=a.nodes||[];
  const trend=obsTrendWindow(a.trend||[]);
  const grainName=obsGrainCaption();
  const selectedPeriod=obsPeriodKey();
  const maxTrend=Math.max(1,...trend.map(t=>t.tokens||0));
  const traceTable={headers:['TAPD / 需求','workflow_task_id','Run / Trace','状态','耗时','Tokens','成本','工具','人工','错误位置'],rows:obsTraceRows(traces),pageKey:'obs-traces',empty:'暂无运行'};
  const recent={headers:['需求 / 流程','状态','耗时','Tokens','成本'],rows:traces.slice(0,8).map(t=>obsRow([escapeHTML(obsTraceLabel(t)),`<span class="status ${escapeHTML(t.status||'')}">${runStatus(t.status)}</span>`,t.durationMs?duration(t.durationMs):'—',formatTokens(t.tokens),costLabel(t.costCny,t.costKnown)],t.origin==='external'?'':`data-open-run="${escapeHTML(t.runId||'')}"`,'run-row')),empty:'暂无运行'};
  const trendTable={headers:[grainName,'运行','需求','成功','失败','Tokens','输入 / 输出','成本','执行耗时',''],rows:trend.slice().reverse().map(t=>obsRow([escapeHTML(obsTrendLabel(t)),String(t.runs),String(t.issues||0),String(t.succeeded),String(t.failed||0),formatTokens(t.tokens),`${formatTokens(t.inputTokens)} / ${formatTokens(t.outputTokens)}`,costLabel(t.costCny,!t.costUnknownRuns),t.executionDurationMs?duration(t.executionDurationMs):'—',`<span class="obs-mini-bar"><i style="width:${Math.round((t.tokens||0)*100/maxTrend)}%"></i></span>`],`data-obs-period="${escapeHTML(t.date||'')}"`,`run-row${t.date===selectedPeriod?' obs-period-active':''}`)),empty:'暂无趋势'};
  const sopTable={headers:['SOP','运行','成功率','返工率','取消率','Tokens','输入 / 输出','Token 占比','成本','成本占比','均执行'],rows:stats.map(w=>obsRow([`<b>${escapeHTML(w.name)}</b>`,String(w.runs),percent(w.successRate),w.qualityUnknownRuns===w.runs?'—':percent(w.reworkRate),percent(w.cancelRate),formatTokens(w.tokens),`${formatTokens(w.inputTokens)} / ${formatTokens(w.outputTokens)}`,w.tokenSharePct!=null?w.tokenSharePct.toFixed(1)+'%':'—',costLabel(w.costCny,!w.costUnknownRuns),w.costSharePct!=null?w.costSharePct.toFixed(1)+'%':'—',w.averageExecutionDurationMs?duration(w.averageExecutionDurationMs):'—']))};
  const modelTable={headers:['模型','调用','Tokens','输入 / 输出','Token 占比','成本','成本占比','成功率','均执行'],rows:(a.models||[]).map(m=>obsRow([`<span class="mono">${escapeHTML(m.name||m.key)}</span>`,String(m.runs),formatTokens(m.tokens),`${formatTokens(m.inputTokens)} / ${formatTokens(m.outputTokens)}`,m.tokenSharePct!=null?m.tokenSharePct.toFixed(1)+'%':'—',costLabel(m.costCny,!m.costUnknownRuns),m.costSharePct!=null?m.costSharePct.toFixed(1)+'%':'—',m.qualityUnknownRuns===m.runs?'—':percent(m.successRate),m.averageExecutionDurationMs?duration(m.averageExecutionDurationMs):'—']))};
  const memberTable={title:'成员汇总',sub:'当前项目全部责任人，按 Token 排序',headers:['成员','运行','成功','失败','Tokens','输入 / 输出','成本','执行耗时'],rows:obsUserTotals().map(u=>obsRow([`<b>${escapeHTML(obsUserName(u.userId))}</b>`,String(u.runs),String(u.success||0),String(u.failed||0),formatTokens(u.tokens),`${formatTokens(u.inputTokens)} / ${formatTokens(u.outputTokens)}`,costLabel(u.costCny,u.costKnown),u.executionDurationMs?duration(u.executionDurationMs):'—']))};
  const userTable={title:obsScope()==='project'?'每人每日用量':'',headers:['日期','成员','运行','Tokens','输入 / 输出','成本','执行耗时'],rows:(a.userUsage||[]).map(u=>obsRow([escapeHTML(u.date),`<b>${escapeHTML(obsUserName(u.userId))}</b>`,String(u.runs),formatTokens(u.tokens),`${formatTokens(u.inputTokens)} / ${formatTokens(u.outputTokens)}`,costLabel(u.costCny,u.costKnown),u.executionDurationMs?duration(u.executionDurationMs):'—'])),pageKey:'obs-users'};
  const srcTable={title:'需求类型分布',sub:'按需求来源聚合，已完成数按需求去重',headers:['来源','关联执行','SOP 成功','已交付需求','Tokens','成本','平均执行'],rows:(a.issueSources||[]).map(s=>obsRow([`<b>${obsSourceLabel(s.key)}</b>`,String(s.runs),String(s.succeeded||0),String(s.completedIssues||0),formatTokens(s.tokens),costLabel(s.costCny,!s.costUnknownRuns),s.averageExecutionDurationMs?duration(s.averageExecutionDurationMs):'—']))};
  const catTable={title:'编排类型分布',sub:'按 SOP 统计标签归类，完成需求数已按需求去重',headers:['统计标签','执行','成功','完成需求','Tokens','成本'],rows:(a.categories||[]).map(c=>obsRow([`<b>${escapeHTML(obsCategoryLabel(c.key))}</b>`,String(c.runs),String(c.succeeded||0),String(c.completedIssues||0),formatTokens(c.tokens),costLabel(c.costCny,!c.costUnknownRuns)]))};
  const versionTable={title:'SOP 版本对比',sub:'同一 SOP 不同版本的执行效果；样本 < 5 标注样本不足',headers:['SOP','版本','运行','成功率','均执行耗时','Tokens','成本','提示'],rows:(a.workflowVersions||[]).map(v=>obsRow([`<b>${escapeHTML(v.name||v.workflowId)}</b>`,`<span class="mono">v${v.version}</span>`,String(v.runs),percent(v.successRate),v.averageExecutionDurationMs?duration(v.averageExecutionDurationMs):'—',formatTokens(v.tokens),costLabel(v.costCny,!v.costUnknownRuns),v.insufficientSample?'<span class="muted">样本不足</span>':'—']))};
  const profileRows=stats.filter(w=>w.nodeCount>0).map(w=>obsRow([`<b>${escapeHTML(w.name)}</b>`,String(w.nodeCount),Object.entries(w.nodeTypes||{}).map(([k,v])=>`${escapeHTML(k)}×${v}`).join('，')||'—',escapeHTML(obsCategoryLabel(w.category)),String(w.runs)]));
  const profileTable={title:'SOP 编排画像',sub:'节点数、节点类型构成与受控统计标签',headers:['SOP','节点数','节点类型构成','统计标签','使用频次'],rows:profileRows};
  const nodeTable={title:'节点质量明细',sub:'按失败节点数排序；重试率 = 重试节点 ÷ 节点执行次数',headers:['节点','执行','成功','失败','返工','重试率','均执行耗时','Tokens','工具调用'],rows:[...nodeStats].sort((x,y)=>(y.failedNodes||0)-(x.failedNodes||0)).map(n=>obsRow([escapeHTML(n.name||n.key),String(n.runs),String(n.succeeded||0),String(n.failedNodes||0),String(n.reworkNodes||0),n.retryRate!=null?percent(n.retryRate):'—',n.averageExecutionDurationMs?duration(n.averageExecutionDurationMs):'—',formatTokens(n.tokens),String(n.toolCalls||0)]))};
  const slowTable={title:'慢节点',sub:'按平均执行耗时排序',headers:['节点','执行','均耗时','P95','成功率','Tokens'],rows:[...nodeStats].filter(n=>n.averageExecutionDurationMs>0).sort((x,y)=>y.averageExecutionDurationMs-x.averageExecutionDurationMs).slice(0,10).map(n=>obsRow([escapeHTML(n.name||n.key),String(n.runs),duration(n.averageExecutionDurationMs),n.p95ExecutionDurationMs?duration(n.p95ExecutionDurationMs):'—',percent(n.successRate),formatTokens(n.tokens)]))};
  const failReasonTable={title:'失败原因',sub:'节点错误归一化后的高频原因',headers:['节点','错误','次数'],rows:(a.failures||[]).map(f=>obsRow([escapeHTML(f.nodeId||'—'),`<span title="${escapeHTML(f.code||'')}">${escapeHTML((f.code||'').slice(0,80))}</span>`,String(f.count)])),empty:'没有失败记录'};
  const failRunTable={title:'最近失败',headers:['最近失败','状态','错误','时间'],rows:traces.filter(t=>t.status==='failed').slice(0,20).map(t=>obsRow([escapeHTML(obsTraceLabel(t)),`<span class="status ${escapeHTML(t.status||'')}">${runStatus(t.status)}</span>`,t.errorMessage?`<span class="obs-trace-error" title="${escapeHTML(t.errorMessage)}">${escapeHTML(t.errorMessage.slice(0,80))}</span>`:'—',relativeTime(t.createdAt)],t.origin==='external'?'':`data-open-run="${escapeHTML(t.runId||'')}"`,'run-row')),empty:'没有失败运行'};
  const cross=obsCrossProjectTable();
  return [
    {group:'运行追溯',items:[
      {key:'traces',title:'关联与追溯',sub:'TAPD / 需求 → workflow_task_id → run_id / trace_id；点击一行查看事件明细',tables:[traceTable]},
      {key:'recent',title:'最近运行',sub:'点击打开对应需求',tables:[recent]},
    ]},
    {group:'用量拆解',items:[
      {key:'trend',title:'趋势明细',sub:`按${grainName}；点一行查看该时段`,tables:[trendTable]},
      {key:'sops',title:'SOP 一览',sub:'成功率、返工、取消和各流程占了多少 Token / 成本',tables:[sopTable]},
      {key:'models',title:'各模型用量',sub:'每个模型花了多少 Token 和钱',tables:[modelTable]},
      {key:'users',title:obsScope()==='project'?'成员':'每人每日用量',sub:'责任人 × 自然日',tables:obsScope()==='project'?[memberTable,userTable]:[userTable],count:(a.userUsage||[]).length},
      {key:'dist',title:'类型分布',sub:'需求来源与 SOP 统计标签',tables:[srcTable,catTable]},
    ]},
    {group:'质量',items:[
      {key:'nodes',title:'节点',sub:'节点质量与慢节点',tables:[nodeTable,slowTable],count:nodeStats.length},
      {key:'fails',title:'失败',sub:'失败原因与最近失败的运行',tables:[failReasonTable,failRunTable],count:(a.failures||[]).length},
      {key:'versions',title:'SOP 版本',sub:'版本对比与编排画像',tables:profileRows.length?[versionTable,profileTable]:[versionTable],count:(a.workflowVersions||[]).length},
    ]},
    cross?{group:'人与项目',items:[cross]}:null,
  ].filter(Boolean);
}
function obsCrossProjectTable(){
  if(obsScope()!=='user'||!state.obsUserFilter)return null;
  const rows=state.obsCrossProject?.projectUsage||[];
  if(!rows.length)return null;
  const current=state.projectFilter;
  const other=rows.filter(r=>r.projectId&&r.projectId!==current);
  const totalTokens=rows.reduce((n,r)=>n+(r.tokens||0),0);
  const here=rows.find(r=>r.projectId===current);
  const note=other.length
    ?`该责任人在 ${rows.length} 个项目有执行。上面 KPI 只含「${escapeHTML(obsProjectName(current))}」（${formatTokens(here?.tokens||0)}），跨项目合计 ${formatTokens(totalTokens)}。`
    :`该责任人目前只在当前项目有执行记录，个人与项目切换不会把其他项目加进来。`;
  return {key:'projects',title:'该责任人在各项目',sub:note,tables:[{headers:['项目','运行','需求','Tokens','成本'],rows:rows.map(r=>obsRow([`<b>${escapeHTML(obsProjectName(r.projectId))}</b>${r.projectId===current?'<span class="obs-pill">当前</span>':''}`,String(r.runs||0),String(r.uniqueIssues||0),formatTokens(r.tokens),costLabel(r.costCny,r.costKnown)],'',r.projectId===current?'obs-current-project':''))}]};
}
const OBS_DELIVERY_FILTERS=[['all','全部'],['delivered','已交付'],['sop','SOP 成功'],['direct','对话改代码'],['withmr','有 PR'],['mr','PR 已合入'],['baseline','已合入基线'],['github','GitHub'],['local','本地']];
function obsDeliveryMRs(it){
  const list=it.mrs?.length?it.mrs:it.mrUrl?[{url:it.mrUrl,project:it.mrProject,iid:it.mrIid,state:it.mrState}]:[];
  const seen=new Map();
  for(const mr of list){const key=mr.project&&mr.iid?`${mr.project.toLowerCase()}#${mr.iid}`:String(mr.url||'').replace(/^http:/,'https:').replace('/-/merge_requests/','/merge_requests/');if(key&&!seen.has(key))seen.set(key,mr)}
  return [...seen.values()];
}
function obsDeliveryMRHTML(it){return obsDeliveryMRs(it).map(mr=>`<a href="${escapeHTML(safeURL(mr.url))}" target="_blank" rel="noopener noreferrer">${escapeHTML(`${mr.project?mr.project+' ':''}!${mr.iid||''}`)}</a> · ${obsMrLabel(mr.state)}`).join('<br>')||'—'}
function obsDeliveryMatches(it,filter){
  if(filter==='delivered')return it.delivered;
  if(filter==='sop')return it.sopSucceeded;
  if(filter==='direct')return !!it.directDelivery;
  if(filter==='done')return it.sopSucceeded||(!!it.directDelivery&&obsDeliveryMRs(it).length>0);
  if(filter==='withmr')return obsDeliveryMRs(it).length>0;
  if(filter==='mr')return obsDeliveryMRs(it).some(mr=>mr.state==='merged');
  if(filter==='baseline')return obsDeliveryMRs(it).some(mr=>mr.state==='integrated');
  if(filter==='github'||filter==='local'||filter==='gongfeng'||filter==='tapd')return (it.source||'local')===filter;
  if(filter&&filter.startsWith('user:'))return String(it.userId||'unassigned')===filter.slice(5);
  return true;
}
function obsDeliveryStages(){
  const d=state.analytics?.delivery||{};
  return [{key:'all',name:'有执行的单',value:d.issues||0},{key:'done',name:d.directIssues?'SOP 成功 / 对话已提 PR':'SOP 成功',value:(d.sopSucceeded||0)+(d.directWithMr||0)},{key:'withmr',name:'有 PR 的单',value:d.withMr||0},{key:'mr',name:'有 PR 合入的单',value:d.issuesWithMergedMr??d.mrMerged??0},{key:'delivered',name:'已交付',value:d.delivered||0}];
}
function obsSetDeliveryFilter(filter,scroll){
  state.obsDeliveryFilter=filter||'all';
  resetListPage('obs-deliveries');
  obsCollapse({silent:!scroll});
  if(!obsRefreshCard('deliveries'))return;
  obsRefreshCard('funnel');
  obsRefreshCard('owners');
  if(scroll)document.querySelector('[data-obs-card="deliveries"]')?.scrollIntoView({behavior:obsReducedMotion()?'auto':'smooth',block:'start'});
}
function obsDelivery(){
  obsCardBuilders={funnel:obsFunnelCard,owners:obsOwnersCard,deliveries:obsDeliveriesCard};
  const d=state.analytics?.delivery||{};
  const summary=state.analytics?.summary||{};
  const kpis=obsKpiGroups([
    obsKpiGroup('交付结果',[
      {label:'已交付',value:String(d.delivered||0),meter:d.issues?d.delivered*100/d.issues:0,foot:`SOP 成功或对话直接交付，且已在默认分支 · ${d.issues||0} 个有执行的单${d.directIssues?`（含 ${d.directIssues} 个只在对话里改）`:''}`,tip:['「已合入 / 已交付」只统计默认分支。','对话直接改代码的需求没有 SOP，PR 合入默认分支即算交付。']},
      {label:'完成需求 / Bug',value:`${d.requirementCount||0} / ${d.bugFixCount||0}`,foot:'按 SOP 统计标签归类，需求去重'},
      {label:'PR 已合入',value:String(d.mrMerged||0),meter:(d.totalMrs??d.withMr)?d.mrMerged*100/(d.totalMrs??d.withMr):0,foot:`按 PR 计数 · 默认分支；${d.mrIntegrated||0} 个已合入基线`},
      {label:'人均周吞吐',value:summary.weeklyActiveContributors?Number(summary.weeklyThroughputPerUser||0).toFixed(2):'—',foot:`近 7 天 ${summary.weeklyDeliveredIssues||0} 个交付 / ${summary.weeklyActiveContributors||0} 人`},
    ]),
    obsKpiGroup('耗时与返工',[
      {label:'平均执行耗时',value:d.averageExecutionDurationMs?duration(d.averageExecutionDurationMs):'—',foot:`P50 ${d.p50ExecutionDurationMs?duration(d.p50ExecutionDurationMs):'—'} · P95 ${d.p95ExecutionDurationMs?duration(d.p95ExecutionDurationMs):'—'}`,tip:['执行耗时 = 该需求全部执行按 SOP 节点求和。']},
      {label:'平均端到端耗时',value:d.averageDurationMs?duration(d.averageDurationMs):'—',foot:`累计 ${formatTokens(d.totalTokens||0)} tokens`,tip:['端到端 = 该需求最近一次执行的开始到结束，含等待。']},
      {label:'每需求平均执行次数',value:summary.runsPerIssue!=null?Number(summary.runsPerIssue).toFixed(2):'—',foot:`失败 ${summary.failuresPerIssue!=null?Number(summary.failuresPerIssue).toFixed(2):'—'} 次/需求 · 进行中 ${summary.inProgressIssues||0} 个`},
    ]),
  ]);
  return `${obsInsights(obsScopedView(),'delivery')}${kpis}<div class="obs-grid2">${obsFunnelCard()}${obsOwnersCard()}</div>${obsDeliveriesCard()}`;
}
function obsFunnelCard(){
  const stages=obsDeliveryStages();
  const filter=state.obsDeliveryFilter||'all';
  const active=stages.findIndex(s=>s.key===filter);
  const empty=!stages[0].value;
  const chart=obsChartSlot('funnel',{
    empty,emptyText:'还没有带需求上下文的运行',
    option:(theme,reduced)=>obsFunnelOption(stages,{...obsChartFormatters(theme,reduced),active}),
    fallback:()=>{const st=obsFunnelStats(stages);const max=Math.max(1,...st.stages.map(s=>s.value));return `<div class="obs-funnel-fallback">${st.stages.map(s=>`<button type="button" data-obs-funnel="${s.key}"><span>${escapeHTML(s.name)}</span>${obsMeter(s.value*100/max)}<b>${s.value}</b><em class="${s.worst?'bad':''}">${s.conv==null?'':Math.round(s.conv)+'%'}</em></button>`).join('')}</div>`},
    on:{click:p=>obsSetDeliveryFilter(stages[p.dataIndex]?.key||'all',true)}
  },210);
  return obsCard('funnel','交付漏斗','点一层筛选下方明细；红字为最大流失',filter!=='all'?`<button type="button" class="obs-btn ghost" data-obs-delivery="all">清除筛选</button>`:'',chart,{expand:true});
}
function obsOwnerRows(){
  const bag=new Map();
  for(const it of state.analytics?.deliveries||[]){
    const id=String(it.userId||'unassigned');
    const r=bag.get(id)||{id,name:obsUserName(it.userId),delivered:0,active:0,undelivered:0,total:0,durationMs:0,durations:0,tokens:0,costCny:0,costKnown:true};
    r.total++;
    if(it.delivered)r.delivered++;
    else if(!it.directDelivery&&['running','waiting','queued','pending'].includes(it.runStatus))r.active++;
    else if(it.directDelivery&&obsDeliveryMRs(it).some(mr=>mr.state==='opened'||mr.state==='reopened'))r.active++;
    else r.undelivered++;
    if(it.durationMs){r.durationMs+=Number(it.durationMs);r.durations++}
    r.tokens+=Number(it.tokens||0);
    r.costCny+=Number(it.costCny||0);
    if(it.costKnown===false)r.costKnown=false;
    bag.set(id,r);
  }
  return [...bag.values()].map(r=>({...r,avgMs:r.durations?Math.round(r.durationMs/r.durations):0,durationLabel:r.durations?duration(Math.round(r.durationMs/r.durations)):'—'})).sort((a,b)=>(b.delivered-a.delivered)||(b.total-a.total));
}
function obsOwnersCard(){
  const rows=obsOwnerRows();
  const top=rows.slice(0,8);
  const filter=state.obsDeliveryFilter||'all';
  const chart=obsChartSlot('owners',{
    empty:!rows.length,emptyText:'暂无负责人数据',
    option:(theme,reduced)=>obsOwnersOption(top,obsChartFormatters(theme,reduced)),
    fallback:()=>`<div class="obs-bd-bars">${top.map(r=>`<div><span>${escapeHTML(r.name)}</span>${obsMeter(r.total?r.delivered*100/r.total:0)}<b>${r.delivered}/${r.total}</b></div>`).join('')}</div>`,
    axisClick:idx=>{const r=top[idx];if(r)obsSetDeliveryFilter('user:'+r.id,true)}
  },Math.max(120,top.length*30+16));
  const table=rows.length?`<div class="obs-tw"><table class="obs-table"><thead><tr><th>负责人</th><th>已交付</th><th>进行中</th><th>未交付</th><th>平均端到端</th><th>Tokens</th><th>成本</th></tr></thead><tbody>${rows.map(r=>`<tr class="run-row${filter==='user:'+r.id?' obs-row-hl':''}" data-obs-delivery="user:${escapeHTML(r.id)}"><td><b>${escapeHTML(r.name)}</b></td><td>${r.delivered}</td><td>${r.active}</td><td>${r.undelivered}</td><td>${r.durationLabel}</td><td>${formatTokens(r.tokens)}</td><td>${costLabel(r.costCny,r.costKnown)}</td></tr>`).join('')}</tbody></table></div>`:'';
  return obsCard('owners','按负责人','已交付 / 进行中 / 未交付；点一人筛选明细','',`<div class="obs-legend"><span><i style="background:var(--c1)"></i>已交付</span><span><i style="background:var(--c3)"></i>进行中</span><span><i style="background:var(--c4)"></i>未交付</span></div>${chart}${table}`,{expand:true});
}
function obsMrLabel(s){return s==='merged'?'<span class="obs-mr merged">已合入</span>':s==='integrated'?'<span class="obs-mr integrated">已合入基线</span>':({opened:'待合入',reopened:'重开',closed:'已关闭',none:'无 PR',unknown:'待查合入'}[s]||s||'—')}
// A conversation requirement has no SOP status: its state is its MRs'.
function obsDirectDeliveryText(it){
  if(it.delivered)return '对话直接交付';
  const states=obsDeliveryMRs(it).map(mr=>mr.state);
  if(!states.length)return '对话改代码中';
  if(states.some(s=>s==='merged'||s==='integrated'))return '对话 · PR 已合入';
  if(states.every(s=>s==='closed'))return '对话 · PR 已关闭';
  return '对话 · PR 待合入';
}
function obsDeliveryStatusText(it){return it.directDelivery?obsDirectDeliveryText(it):it.sopSucceeded?'SOP 成功':(runStatus(it.runStatus)||it.runStatus||'—')}
function obsDeliveryStatusHTML(it){return `<span class="status ${escapeHTML(it.directDelivery?'':(it.runStatus||''))}">${escapeHTML(obsDeliveryStatusText(it))}</span>`}
function obsDeliveryTableDef(all){
  const items=state.analytics?.deliveries||[];
  const filter=state.obsDeliveryFilter||'all';
  const visible=items.filter(it=>obsDeliveryMatches(it,filter));
  const rows=visible.map(it=>{
    const issue=it.externalIid?`#${it.externalIid}`:(it.externalId||it.issueId);
    const mr=obsDeliveryMRHTML(it);
    const startedLanes=(it.lanes||[]).filter(l=>l.started);
    const sop=startedLanes.length?startedLanes.map(l=>`${l.label}${l.status==='succeeded'?'✓':''}`).join(' / '):(it.workflowName||it.workflowId);
    return obsRow([`${obsSourceLabel(it.source)} ${escapeHTML(issue)}`,`<b>${escapeHTML(it.title||it.issueId)}</b>`,escapeHTML(sop),obsDeliveryStatusHTML(it),duration(it.durationMs),it.executionDurationMs?duration(it.executionDurationMs):'—',it.cycleDurationMs?duration(it.cycleDurationMs):'—',it.directDelivery?'对话':`${it.runCount||0} 次`,escapeHTML(obsUserName(it.userId)||'—'),formatTokens(it.tokens),costLabel(it.costCny,it.costKnown),mr,it.delivered?'已交付':'未交付'],`data-edit-issue="${escapeHTML(it.issueId)}"`,'run-row');
  });
  return {key:'deliveries',title:'需求交付明细',tables:[{headers:['单号','标题','SOP','流程','端到端','执行耗时','周期','执行次数','负责人','Tokens','成本','PR','交付'],rows,pageKey:all?'':'obs-deliveries',empty:'还没有带需求上下文的运行'}],items,filter};
}
function obsDeliveriesCard(){
  const def=obsDeliveryTableDef();
  const {items,filter}=def;
  const userFilter=filter.startsWith('user:')?filter:'';
  const chips=OBS_DELIVERY_FILTERS.map(([id,label])=>`<button type="button" data-obs-delivery="${id}" class="${filter===id?'active':''}">${label}<i>${items.filter(it=>obsDeliveryMatches(it,id)).length} 单</i></button>`).join('')+(userFilter?`<button type="button" class="active" data-obs-delivery="all" title="清除负责人筛选">${escapeHTML(obsUserName(userFilter.slice(5)))} ×</button>`:'');
  const body=`<div class="obs-chips">${chips}</div>${obsDetailTable(def.tables[0],'','')}`;
  return obsCard('deliveries','需求交付明细','耗时为该需求最近一次执行的端到端时间；执行耗时 = 该需求全部执行按 SOP 节点求和；周期为首次执行到最后一次执行。「已合入 / 已交付」只统计默认分支。',`<button type="button" class="obs-btn" data-obs-export-csv="deliveries">${obsIcon('download')}CSV</button>`,body,{klass:'obs-deliveries'});
}
function obsPlain(v){return String(v==null?'':v).replace(/<[^>]+>/g,'')}
function obsReportScope(){
  const project=currentProject();
  const dim=obsScope()==='project'?'项目':'个人';
  const who=obsScope()==='project'?(project?.name||project?.id||'当前项目'):(state.obsUserFilter?obsUserName(state.obsUserFilter):'全部成员');
  const source=(state.obsSourceFilter?obsSourceLabel(state.obsSourceFilter):'全部需求来源')+' · '+({internal:'平台内执行',external:'外部工作流'}[state.obsOriginFilter]||'全部执行');
  return {project:project?.name||project?.id||'全部项目', dim, who, source, at:new Date().toLocaleString()};
}
function obsReportTable(title,headers,rows){
  if(!rows)return `<section class="block"><h2>${title}</h2><p class="empty">暂无数据</p></section>`;
  return `<section class="block"><h2>${title}</h2><table><thead><tr>${headers.map(h=>`<th>${h}</th>`).join('')}</tr></thead><tbody>${rows}</tbody></table></section>`;
}
async function exportObsReport(){
  ensureObsUserFilter();
  toast('正在生成报告…');
  try{
    if(!state.obsAnalyticsFull){
      state.analytics=await api('/api/analytics'+analyticsQuery({overview:false}),{silent:true});
      state.obsAnalyticsFull=true;
    }
    const html=buildObsReportHTML();
    const blob=new Blob([html],{type:'text/html;charset=utf-8'});
    const a=document.createElement('a');
    const scope=obsReportScope();
    const stamp=new Date().toISOString().slice(0,10);
    a.href=URL.createObjectURL(blob);
    a.download=`AgentFlow用量报告-${scope.project}-${scope.who}-${stamp}.html`;
    document.body.appendChild(a);a.click();a.remove();
    setTimeout(()=>URL.revokeObjectURL(a.href),1500);
    toast('已导出 HTML 报告','ok');
  }catch(e){toast(e.message||'导出失败','err')}
}
function buildObsReportHTML(){
  const scope=obsReportScope(),summary=state.analytics?.summary||{},d=state.analytics?.delivery||{};
  const workflows=state.data.workflows||[];
  const stats=(state.analytics?.workflows||[]).map(w=>({...w,name:w.name||workflows.find(x=>x.id===w.key)?.name||w.key}));
  const wfRows=stats.map(w=>`<tr><td>${escapeHTML(w.name)}</td><td>${w.runs}</td><td>${percent(w.successRate)}</td><td>${w.qualityUnknownRuns===w.runs?'—':percent(w.reworkRate)}</td><td>${formatTokens(w.tokens)}</td><td>${obsPlain(costLabel(w.costCny,!w.costUnknownRuns))}</td><td>${w.averageExecutionDurationMs?duration(w.averageExecutionDurationMs):'—'}</td></tr>`).join('');
  const modelRows=(state.analytics?.models||[]).map(m=>`<tr><td>${escapeHTML(m.name||m.key)}</td><td>${m.runs}</td><td>${formatTokens(m.tokens)}</td><td>${formatTokens(m.inputTokens)} / ${formatTokens(m.outputTokens)}</td><td>${m.tokenSharePct!=null?m.tokenSharePct.toFixed(1)+'%':'—'}</td><td>${obsPlain(costLabel(m.costCny,!m.costUnknownRuns))}</td></tr>`).join('');
  const userRows=(state.analytics?.userUsage||[]).slice(0,40).map(u=>`<tr><td>${escapeHTML(u.date)}</td><td>${escapeHTML(obsUserName(u.userId))}</td><td>${u.runs}</td><td>${formatTokens(u.tokens)}</td><td>${formatTokens(u.inputTokens)} / ${formatTokens(u.outputTokens)}</td><td>${obsPlain(costLabel(u.costCny,u.costKnown))}</td></tr>`).join('');
  const memberRows=obsUserTotals().map(u=>`<tr><td>${escapeHTML(obsUserName(u.userId))}</td><td>${u.runs}</td><td>${u.success||0}</td><td>${u.failed||0}</td><td>${formatTokens(u.tokens)}</td><td>${formatTokens(u.inputTokens)} / ${formatTokens(u.outputTokens)}</td><td>${obsPlain(costLabel(u.costCny,u.costKnown))}</td></tr>`).join('');
  const srcRows=(state.analytics?.issueSources||[]).map(s=>`<tr><td>${obsSourceLabel(s.key)}</td><td>${s.runs}</td><td>${s.succeeded||0}</td><td>${s.completedIssues||0}</td><td>${formatTokens(s.tokens)}</td><td>${obsPlain(costLabel(s.costCny,!s.costUnknownRuns))}</td></tr>`).join('');
  const versionRows=(state.analytics?.workflowVersions||[]).map(v=>`<tr><td>${escapeHTML(v.name||v.workflowId)}</td><td>v${v.version}</td><td>${v.runs}</td><td>${percent(v.successRate)}</td><td>${formatTokens(v.tokens)}</td><td>${obsPlain(costLabel(v.costCny,!v.costUnknownRuns))}</td></tr>`).join('');
  const trendRows=obsTrendWindow(state.analytics?.trend||[]).slice().reverse().map(t=>`<tr><td>${escapeHTML(obsTrendLabel(t))}</td><td>${t.runs}</td><td>${t.issues||0}</td><td>${t.succeeded}</td><td>${t.failed||0}</td><td>${formatTokens(t.tokens)}</td><td>${obsPlain(costLabel(t.costCny,!t.costUnknownRuns))}</td><td>${t.executionDurationMs?duration(t.executionDurationMs):'—'}</td></tr>`).join('');
  const projectRows=(state.obsCrossProject?.projectUsage||state.analytics?.projectUsage||[]).map(r=>`<tr><td>${escapeHTML(obsProjectName(r.projectId))}</td><td>${r.runs||0}</td><td>${r.uniqueIssues||0}</td><td>${formatTokens(r.tokens)}</td><td>${obsPlain(costLabel(r.costCny,r.costKnown))}</td></tr>`).join('');
  const deliveryRows=(state.analytics?.deliveries||[]).slice(0,80).map(it=>`<tr><td>${obsSourceLabel(it.source)} ${escapeHTML(String(it.externalIid?('#'+it.externalIid):(it.externalId||it.issueId)))}</td><td>${escapeHTML(it.title||it.issueId)}</td><td>${escapeHTML(it.workflowName||it.workflowId)}</td><td>${escapeHTML(obsDeliveryStatusText(it))}</td><td>${escapeHTML(obsUserName(it.userId))}</td><td>${formatTokens(it.tokens)}</td><td>${obsPlain(costLabel(it.costCny,it.costKnown))}</td><td>${obsDeliveryMRHTML(it)}</td><td>${it.delivered?'已交付':'未交付'}</td></tr>`).join('');
  const kpis=[
    ['运行',summary.totalRuns||0,`${summary.activeRuns||0} 进行中`],
    ['成功率',summary.totalRuns?percent(summary.successRate):'—',`${summary.succeededRuns||0} 成功 / ${summary.failedRuns||0} 失败`],
    ['一次通过',summary.totalRuns?percent(summary.firstPassRate):'—',`${summary.reworkedRuns||0} 次返工`],
    ['Token',formatTokens(summary.totalTokens),`入 ${formatTokens(summary.inputTokens)} / 出 ${formatTokens(summary.outputTokens)}`],
    ['成本',obsPlain(costLabel(summary.totalCostCny,summary.costUnknownRuns===0)),summary.costUnknownRuns?`${summary.costUnknownRuns} 次未识别`:'已识别'],
    ['缓存命中',(Number(summary.inputTokens||0)+Number(summary.cacheReadTokens||0))?Number(summary.cacheHitRate||0).toFixed(0)+'%':'—',`缓存读 ${formatTokens(summary.cacheReadTokens||0)}`],
    ['P95 执行',summary.executionP95DurationMs?duration(summary.executionP95DurationMs):'—',summary.executionAverageDurationMs?`平均 ${duration(summary.executionAverageDurationMs)}`:'—'],
    ['对话改代码',`${summary.conversationIssues||0} 个需求`,`${summary.conversationWorkspaces||0} 个工作区 · ${formatTokens(summary.conversationTokens||0)} · ${obsPlain(costLabel(summary.conversationCostCny||0,true))}`],
    ['已交付',d.delivered||0,`${d.issues||0} 个有执行的单`],
    ['PR 已合入',d.mrMerged||0,`${d.mrIntegrated||0} 已合入基线`],
  ].map(([l,v,f])=>`<article><span>${l}</span><b>${v}</b><small>${f}</small></article>`).join('');
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>${escapeHTML(scope.project)} 用量报告</title>
<style>
:root{--text:#152033;--muted:#5b6b82;--line:#e6ebf3;--bg:#f5f7fb;--card:#fff;--blue:#3b6cff}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:14px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Noto Sans SC",sans-serif}
.wrap{max-width:1080px;margin:0 auto;padding:36px 28px 64px}header{margin-bottom:28px}
.eyebrow{margin:0;color:var(--blue);font-size:12px;font-weight:700;letter-spacing:.08em;text-transform:uppercase}
h1{margin:6px 0 8px;font-size:28px}header p{margin:0;color:var(--muted)}
.meta{display:flex;flex-wrap:wrap;gap:8px;margin-top:14px}
.meta span{padding:5px 10px;border:1px solid var(--line);border-radius:999px;background:var(--card);font-size:12px;color:var(--muted)}
.kpis{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:12px;margin:22px 0}
.kpis article{padding:16px 16px 14px;border:1px solid var(--line);border-radius:14px;background:var(--card)}
.kpis span,.kpis small{display:block;color:var(--muted);font-size:12px}.kpis b{display:block;margin:6px 0 4px;font-size:24px;letter-spacing:-.03em}
.block{margin:22px 0;padding:18px 18px 8px;border:1px solid var(--line);border-radius:14px;background:var(--card)}
.block h2{margin:0 0 12px;font-size:16px}table{width:100%;border-collapse:collapse;font-size:13px}
th{text-align:left;color:var(--muted);font-size:11px;padding:8px;border-bottom:1px solid var(--line)}td{padding:9px 8px;border-bottom:1px solid var(--line);vertical-align:top}
.empty{color:var(--muted);padding:12px 0}footer{margin-top:28px;color:var(--muted);font-size:12px}
@media print{body{background:#fff}.wrap{padding:0}.kpis article,.block{break-inside:avoid}}
@media(max-width:800px){.kpis{grid-template-columns:repeat(2,minmax(0,1fr))}}
</style></head><body><div class="wrap">
<header><p class="eyebrow">AgentFlow</p><h1>用量分析报告</h1><p>效果看成功率和一次通过，投入看 Token、成本和执行耗时。交付看 SOP 是否跑完、PR 是否合入。</p>
<div class="meta"><span>项目 ${escapeHTML(scope.project)}</span><span>维度 ${escapeHTML(scope.dim)}</span><span>${obsScope()==='project'?'范围':'责任人'} ${escapeHTML(scope.who)}</span><span>来源 ${escapeHTML(scope.source)}</span><span>生成于 ${escapeHTML(scope.at)}</span></div></header>
<section class="kpis">${kpis}</section>
${obsReportTable('SOP 一览',['SOP','运行','成功率','返工率','Tokens','成本','均执行'],wfRows)}
${obsReportTable('各模型用量',['模型','调用','Tokens','输入 / 输出','Token 占比','成本'],modelRows)}
${obsScope()==='project'?obsReportTable('成员汇总',['成员','运行','成功','失败','Tokens','输入 / 输出','成本'],memberRows):''}
${obsReportTable('每人每日用量',['日期','成员','运行','Tokens','输入 / 输出','成本'],userRows)}
${obsReportTable('需求类型分布',['来源','关联执行','SOP 成功','已交付需求','Tokens','成本'],srcRows)}
${obsReportTable('SOP 版本对比',['SOP','版本','运行','成功率','Tokens','成本'],versionRows)}
${obsReportTable(obsGrainCaption()+'趋势',['周期','运行','需求','成功','失败','Tokens','成本','执行耗时'],trendRows)}
${projectRows?obsReportTable('各项目用量',['项目','运行','需求','Tokens','成本'],projectRows):''}
${obsReportTable('交付明细',['单号','标题','SOP','流程','责任人','Tokens','成本','PR','交付'],deliveryRows)}
<footer>由 AgentFlow 用量分析导出。日志与 Trace 请在运行记录中查看。</footer>
</div></body></html>`;
}


function obsCanManageSources(){
  const p=currentProject(),u=state.currentUser;
  return !!(p&&u&&!u.isGuest&&(u.isPlatformAdmin||Number(p.ownerId)===Number(u.id)||(p.members||[]).some(m=>Number(m.userId)===Number(u.id)&&m.role==='maintainer')));
}
async function openObsSources(){
  const project=currentProject();if(!project)return toast('请先选择项目');
  const dialog=document.createElement('dialog');dialog.className='obs-source-dialog';
  const memberIds=new Set([project.ownerId,...(project.members||[]).map(m=>m.userId)].map(Number));
  const users=(state.data.users||[]).filter(u=>memberIds.has(Number(u.id)));
  const options=users.map(u=>`<option value="${Number(u.id)}">${escapeHTML(userDisplay(u))}</option>`).join('');
  dialog.setAttribute('aria-label',`${project.name} · 外部数据源`);
  dialog.innerHTML=`<header class="obs-source-head">${obsIcon('integration',16)}<h3>${escapeHTML(project.name)} · 外部数据源</h3><button class="obs-icon-btn" data-close type="button" title="关闭" aria-label="关闭">${obsIcon('close')}</button></header><div class="obs-source-body"><p>为外部工作流创建专用上报凭据。Token 仅在创建时显示，可随时撤销。</p><div data-sources>正在加载…</div><form data-create><label class="field">数据源名称<input name="name" required maxlength="100" placeholder="例如：团队代码评审流水线"></label><label class="field">有效期（天）<input name="days" type="number" min="1" max="365" value="90" required></label><p>责任人映射（可选）：外部账号对应本项目成员；不填责任人的记录归入“未分配”。</p><div data-mappings></div><div class="obs-source-actions"><button type="button" class="button ghost" data-add>${obsIcon('plus')}添加责任人映射</button><button class="button primary" type="submit">${obsIcon('plus')}创建数据源</button></div></form><div data-created hidden><p>请保存 Token，关闭后无法再次查看。</p><textarea data-token readonly aria-label="上报 Token"></textarea><button type="button" class="button ghost" data-copy>${obsIcon('copy')}复制 Token</button><p>上报地址</p><code>${escapeHTML(location.origin+'/api/insights/v1/ingest')}</code><p>完成或失败时上报一次执行汇总；相同 ID 重试不会重复计量。协议及脚本见仓库 docs/external-workflow-usage.md。</p></div></div>`;
  const selects=new Set();
  document.body.appendChild(dialog);
  dialog.onclose=()=>{selects.forEach(picker=>picker.dispose());selects.clear();dialog.remove()};
  dialog.querySelector('[data-close]').onclick=()=>dialog.close();
  obsHydrateIcons(dialog);
  dialog.showModal();
  async function refresh(){
    try{
      const items=await api('/api/insights/v1/sources?projectId='+encodeURIComponent(project.id),{silent:true});
      if(!dialog.isConnected)return;
      dialog.querySelector('[data-sources]').innerHTML=items.length?items.map(s=>`<div class="obs-source-row obs-source-item">${obsIcon('key')}<span><b>${escapeHTML(s.name)}</b><code>${escapeHTML(s.tokenPrefix)}</code><em>${s.revokedAt?'已撤销':new Date(s.expiresAt)<=new Date()?'已过期':'有效至 '+new Date(s.expiresAt).toLocaleDateString()}</em></span>${s.revokedAt?'':`<button type="button" class="obs-icon-btn" data-revoke="${escapeHTML(s.id)}" title="撤销" aria-label="撤销 ${escapeHTML(s.name)}">${obsIcon('trash')}</button>`}</div>`).join(''):'<p class="muted">尚无外部数据源</p>';
      obsHydrateIcons(dialog.querySelector('[data-sources]'));
      dialog.querySelectorAll('[data-revoke]').forEach(b=>b.onclick=async()=>{b.disabled=true;try{await api('/api/insights/v1/sources/'+encodeURIComponent(b.dataset.revoke),{method:'DELETE'});await refresh()}catch(e){toast(e.message);b.disabled=false}});
    }catch(e){dialog.querySelector('[data-sources]').textContent=e.message}
  }
  dialog.querySelector('[data-add]').onclick=()=>{
    const row=document.createElement('div');row.className='obs-source-row';row.dataset.mapping='';
    row.innerHTML=`<input required maxlength="128" placeholder="外部账号" aria-label="外部账号"><span class="obs-source-pick"><select required data-obs-select data-icon="user" aria-label="平台责任人"><option value="">选择责任人</option>${options}</select></span><button type="button" class="obs-icon-btn" title="移除" aria-label="移除责任人映射">${obsIcon('trash')}</button>`;
    dialog.querySelector('[data-mappings]').appendChild(row);
    // 原生 <dialog> 位于 top layer，挂在 body 上的面板会被它盖住。
    const [picker]=obsEnhanceSelects(row,dialog);
    if(picker){
      selects.add(picker);
      const select=picker.select;
      select.addEventListener('invalid',()=>picker.trigger.classList.add('is-invalid'));
      select.addEventListener('change',()=>picker.trigger.classList.toggle('is-invalid',!select.value));
    }
    row.querySelector('.obs-icon-btn').onclick=()=>{if(picker){picker.dispose();selects.delete(picker)}row.remove()};
    obsHydrateIcons(row);
  };
  dialog.querySelector('[data-copy]').onclick=async()=>{try{await navigator.clipboard.writeText(dialog.querySelector('[data-token]').value);toast('Token 已复制')}catch(e){toast('请手动复制 Token')}};
  dialog.querySelector('[data-create]').onsubmit=async e=>{
    e.preventDefault();const form=e.currentTarget,button=form.querySelector('[type=submit]'),userMappings=Object.create(null);
    for(const row of form.querySelectorAll('[data-mapping]')){const key=row.querySelector('input').value.trim();if(!key||Object.hasOwn(userMappings,key))return toast('外部账号不能为空或重复');userMappings[key]=Number(row.querySelector('select').value)}
    button.disabled=true;
    try{
      const result=await api('/api/insights/v1/sources',{method:'POST',body:JSON.stringify({projectId:project.id,name:form.elements.name.value,expiresInDays:Number(form.elements.days.value),userMappings})});
      dialog.querySelector('[data-created]').hidden=false;dialog.querySelector('[data-token]').value=result.token;form.reset();selects.forEach(picker=>picker.refresh());await refresh();
    }catch(e){toast(e.message)}finally{button.disabled=false}
  };
  await refresh();
}
