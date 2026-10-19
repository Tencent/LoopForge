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
function isSkillHubProject(p){
  p=p||currentProject();
  const blob=[p?.id,p?.key,p?.name].map(x=>String(x||'').toLowerCase()).join(' ');
  return blob.includes('skillhub');
}
function canShowSkillhubEfficiency(){return false}
function renderObsTabs(){
  const show=canShowSkillhubEfficiency();
  const tab=$('#obsSkillhubTab');
  if(tab)tab.hidden=!show;
  const btn=$('.nav[data-view="deployments"]');
  if(btn)btn.hidden=!show;
  if(!show&&state.obsTab==='skillhub')state.obsTab='overview';
}
function obsNormalizeTab(tab){
  if(tab==='delivery')return 'delivery';
  if(tab==='skillhub'&&canShowSkillhubEfficiency())return 'skillhub';
  return 'overview';
}
function obsScope(){return state.obsScope==='project'?'project':'user'}
function obsTrendGrain(){return ['day','week','month','all'].includes(state.obsTrendGrain)?state.obsTrendGrain:'all'}
function obsApiGrain(){return obsTrendGrain()==='all'?'month':obsTrendGrain()}
function obsTrendMetric(){return ['runs','issues','cost'].includes(state.obsTrendMetric)?state.obsTrendMetric:'tokens'}
function obsTrendWindow(points){
  const grain=obsTrendGrain();
  if(grain==='all'||grain==='month')return points||[];
  return (points||[]).slice(grain==='week'?-12:-30);
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
  if(metric==='cost')return Number(p?.costUsd||0);
  return Number(p?.tokens||0);
}
function obsFormatTrendValue(v,metric){
  if(metric==='tokens')return formatTokens(v);
  if(metric==='cost')return '$'+Number(v||0).toFixed(2);
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
  if(grain==='day')control=`<input type="date" id="obsPeriodInput" class="select obs-period-input" value="${escapeHTML(key)}">`;
  else if(grain==='month')control=`<input type="month" id="obsPeriodInput" class="select obs-period-input" value="${escapeHTML(key)}">`;
  else control=`<select id="obsPeriodInput" class="select obs-period-input">${obsPeriodChoices().map(w=>`<option value="${escapeHTML(w)}" ${w===key?'selected':''}>${escapeHTML(obsWeekLabel(w))}</option>`).join('')}</select>`;
  return `<div class="obs-period-picker" aria-label="选择时段"><button type="button" data-obs-period-step="-1" title="上一段">‹</button>${control}<button type="button" data-obs-period-step="1" title="下一段">›</button></div>`;
}
function setObsTrendMetric(metric){
  metric=['runs','issues','cost'].includes(metric)?metric:'tokens';
  if(obsTrendMetric()===metric)return;
  state.obsTrendMetric=metric;
  localStorage.setItem('agentflow.obsTrendMetric',metric);
  renderObservability();
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
      reworked:summary.reworkedRuns||0,uniqueIssues:summary.uniqueIssues||0,tokens:summary.totalTokens||0,inputTokens:summary.inputTokens||0,outputTokens:summary.outputTokens||0,
      cost:summary.totalCostUsd||0,costKnown:summary.costUnknownRuns===0,active:summary.activeRuns||0,p95:summary.executionP95DurationMs||0,
      avgExec:summary.executionAverageDurationMs||0,wait:summary.waitAverageDurationMs||0,runsPerIssue:summary.runsPerIssue,firstPass:summary.firstPassRate,
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
    cost+=Number(t.costUsd||0);
    if(t.costKnown===false)costKnown=false;
    human+=Number(t.humanInterventions||0);
    toolOk+=Number(t.toolCallsSucceeded||0);
    toolBad+=Number(t.toolCallsFailed||0);
    if(t.status==='succeeded'&&!Number(t.humanInterventions||0))auto++;
    if(t.issueId)issues.add(t.issueId);
    if(t.durationMs)durations.push(Number(t.durationMs));
  }
  const total=traces.length,attempts=succeeded+failed,observed=toolOk+toolBad;
  return {
    period:true,traces,total,succeeded,failed,cancelled,reworked:null,uniqueIssues:issues.size,tokens,inputTokens:null,outputTokens:null,
    cost,costKnown,active,p95:obsPercentile(durations,95),avgExec:durations.length?Math.round(durations.reduce((a,b)=>a+b,0)/durations.length):0,
    wait:0,runsPerIssue:issues.size?total/issues.size:null,firstPass:null,successRate:attempts?succeeded*100/attempts:0,
    demo:{
      averageTokensPerRun:total?tokens/total:0,toolCallsObserved:observed,toolCallsSucceeded:toolOk,toolCallsFailed:toolBad,
      toolSuccessRate:observed?toolOk*100/observed:0,humanInterventions:human,averageHumanInterventions:total?human/total:0,
      automatedSucceededRuns:auto,automationRate:succeeded?auto*100/succeeded:0
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
  if(tab==='skillhub'){
    const host=$('#obsBody');
    if(host)host.innerHTML=`<div class="obs-pane active"><div class="detail-empty">正在加载 SkillHub 提效数据…</div></div>`;
    await refreshSkillhubEfficiency();
  }else if(tab==='delivery'&&!state.obsAnalyticsFull){
    const host=$('#obsBody');
    if(host)host.innerHTML=`<div class="obs-pane active"><div class="detail-empty">正在加载交付数据…</div></div>`;
    try{
      state.analytics=await api('/api/analytics'+analyticsQuery({overview:false}),{silent:true});
      state.obsAnalyticsFull=true;
    }catch(e){toast(e.message)}
  }
  renderObservability();
}
async function refreshSkillhubEfficiency(){
  if(!canShowSkillhubEfficiency()){state.skillhubEfficiency=null;state.skillhubDeploy=null;state.skillhubDeployError='';return}
  const repo=deploymentSelectedRepository();
  const project=currentProject();
  const q=repo?new URLSearchParams({projectId:project?.id||'',repositoryId:repo.id,pageSize:'100'}):null;
  const [board,deploy]=await Promise.allSettled([
    api('/api/analytics/skillhub',{silent:true}),
    q?api('/api/deployment-metrics?'+q.toString(),{silent:true}):Promise.reject(new Error('当前项目未绑定 SkillHub 仓库，快照里的 QCI 数仍可用')),
  ]);
  if(board.status==='fulfilled')state.skillhubEfficiency=board.value;
  else{state.skillhubEfficiency=null;toast(board.reason?.message||'提效数据加载失败')}
  if(deploy.status==='fulfilled'){state.skillhubDeploy=deploy.value;state.skillhubDeployError=''}
  else{state.skillhubDeploy=null;state.skillhubDeployError=deploy.reason?.message||'部署数据加载失败'}
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
      const [analytics]=await Promise.all([
        api('/api/analytics'+analyticsQuery({overview}),opt),
        state.obsTab==='skillhub'?refreshSkillhubEfficiency():Promise.resolve(),
      ]);
      state.analytics=analytics;
      if(!overview)state.obsAnalyticsFull=true;
    }catch(e){toast(e.message)}
    finally{refreshObsPending=null}
    renderObservability();
    refreshObsCrossProject().then(()=>{if(activeViewName()==='observability')renderObservability()});
  })();
  return refreshObsPending;
}
function runCost(r){return Number(r.usage?.costUsd||0)}
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
  if(state.obsSourceFilter)parts.push('issueSource='+encodeURIComponent(state.obsSourceFilter));
  if(['week','month'].includes(obsApiGrain()))parts.push('grain='+obsApiGrain());
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
  const allOpt=`<button type="button" class="user-combobox-option${!selected?' selected':''}" data-user-option="" data-search="全部成员 all"><span>全部成员</span>${!selected?'<i>✓</i>':''}</button>`;
  const options=allOpt+obsUserChoices().map(u=>`<button type="button" class="user-combobox-option${String(u.id)===String(selected)?' selected':''}" data-user-option="${escapeHTML(String(u.id))}" data-search="${escapeHTML(`${u.loginName||''} ${u.chineseName||''}`.toLowerCase())}"><span>${escapeHTML(userDisplay(u))}</span>${String(u.id)===String(selected)?'<i>✓</i>':''}</button>`).join('');
  const sources=[['','全部来源'],['local','本地'],['github','GitHub']].map(([v,l])=>`<option value="${v}" ${(state.obsSourceFilter||'')===v?'selected':''}>${l}</option>`).join('');
  const grain=obsTrendGrain();
  const grainBtns=[['all','全部'],['day','按日'],['week','按周'],['month','按月']].map(([id,l])=>`<button type="button" data-obs-grain="${id}" class="${grain===id?'active':''}">${l}</button>`).join('');
  const hint=scope==='project'
    ?`项目指标 = 顶栏当前项目「${projectName}」全部成员的运行、Token 和交付，不是全平台汇总。${grain==='all'?'当前看全部历史，趋势按月聚合，点一个月可下钻。':'上方数字和 Loop 指标只含所选时段；点趋势图或用两侧箭头换一天 / 一周 / 一月。'}`
    :`个人指标 = 责任人在当前项目「${projectName}」里的执行。一人多项目时，下方「各项目」看他在其他项目的量，不会加进上面的 KPI。${grain==='all'?'当前看全部历史，趋势按月聚合，点一个月可下钻。':'上方数字和 Loop 指标只含所选时段；点趋势图或用两侧箭头换一天 / 一周 / 一月。'}`;
  const userFilter=scope==='user'?`<label class="field obs-filter-user"><span>责任人</span><div class="user-combobox" data-user-combobox id="obsUserCombobox"><input type="hidden" name="obsUserFilter" value="${escapeHTML(selected)}"><button type="button" class="user-combobox-trigger" data-user-combobox-trigger aria-haspopup="listbox" aria-expanded="false"><span>${escapeHTML(label)}</span><i>⌄</i></button><div class="user-combobox-panel" data-user-combobox-panel hidden><input type="search" data-user-combobox-search placeholder="搜索中文名 / 英文名" autocomplete="off"><div class="user-combobox-options" role="listbox">${options}</div><p class="user-combobox-empty" hidden>没有匹配的用户</p></div></div></label>`:'';
  return `<div class="obs-toolbar"><div class="obs-toolbar-filters"><div class="mcp-scope-switch obs-scope-switch" aria-label="用量维度"><button type="button" data-obs-scope="user" class="${scope==='user'?'active':''}">个人</button><button type="button" data-obs-scope="project" class="${scope==='project'?'active':''}">项目</button></div>${userFilter}<label class="field obs-filter-source"><span>需求来源</span><select id="obsSourceFilter" class="select">${sources}</select></label><div class="obs-grain-switch" aria-label="趋势粒度">${grainBtns}</div>${obsPeriodPicker()}</div><p class="obs-toolbar-note">${escapeHTML(hint)}</p></div>`;
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
function obsCrossProjectCard(){
  if(obsScope()!=='user'||!state.obsUserFilter)return '';
  const rows=state.obsCrossProject?.projectUsage||[];
  if(!rows.length)return '';
  const current=state.projectFilter;
  const other=rows.filter(r=>r.projectId&&r.projectId!==current);
  const totalTokens=rows.reduce((n,r)=>n+(r.tokens||0),0);
  const here=rows.find(r=>r.projectId===current);
  const body=rows.map(r=>`<tr class="${r.projectId===current?'obs-current-project':''}"><td><b>${escapeHTML(obsProjectName(r.projectId))}</b>${r.projectId===current?'<span class="obs-pill">当前</span>':''}</td><td>${r.runs||0}</td><td>${r.uniqueIssues||0}</td><td>${formatTokens(r.tokens)}</td><td>${costLabel(r.costUsd,r.costKnown)}</td></tr>`).join('');
  const note=other.length
    ?`该责任人在 ${rows.length} 个项目有执行。上面 KPI 只含「${escapeHTML(obsProjectName(current))}」（${formatTokens(here?.tokens||0)}），跨项目合计 ${formatTokens(totalTokens)}。`
    :`该责任人目前只在当前项目有执行记录，个人与项目切换不会把其他项目加进来。`;
  return `<div class="obs-card obs-project-mix"><div class="obs-card-title">该责任人在各项目<span class="sub">${note}</span></div><table class="obs-table"><thead><tr><th>项目</th><th>运行</th><th>需求</th><th>Tokens</th><th>成本</th></tr></thead><tbody>${body}</tbody></table></div>`;
}
function obsUserTotals(){
  const bag={};
  for(const u of state.analytics?.userUsage||[]){
    const id=u.userId||'unassigned';
    const row=bag[id]||(bag[id]={userId:id,runs:0,success:0,failed:0,tokens:0,inputTokens:0,outputTokens:0,costUsd:0,costKnown:false,executionDurationMs:0});
    row.runs+=u.runs||0;
    row.success+=u.success||0;
    row.failed+=u.failed||0;
    row.tokens+=u.tokens||0;
    row.inputTokens+=u.inputTokens||0;
    row.outputTokens+=u.outputTokens||0;
    row.costUsd+=u.costUsd||0;
    if(u.costKnown)row.costKnown=true;
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
  return '$'+amount.toFixed(2);
}
function renderObservability(){
  ensureObsUserFilter();
  renderObsTabs();
  const host=$('#obsBody');if(!host)return;
  state.obsTab=obsNormalizeTab(state.obsTab);
  $$('#obsTabs .obs-tab').forEach(b=>b.classList.toggle('active',b.dataset.obsTab===state.obsTab));
  const summary=state.analytics?.summary||{};
  $('#obsLiveState').classList.toggle('active',((summary.activeRuns||0)+(summary.waitingRuns||0))>0);
  const tab=state.obsTab;
  if(tab==='skillhub'){
    host.innerHTML=`<div class="obs-pane active">${obsSkillhub()}</div>`;
    bindPager(host);
    return;
  }
  const html=tab==='delivery'?obsDelivery():obsOverview();
  host.innerHTML=`<div class="obs-pane active">${obsFilterBar()}${html}</div>`;
  bindUserSearches(host);
  host.querySelectorAll('[data-obs-scope]').forEach(b=>b.onclick=()=>setObsScope(b.dataset.obsScope));
  host.querySelectorAll('[data-obs-grain]').forEach(b=>b.onclick=()=>setObsTrendGrain(b.dataset.obsGrain));
  host.querySelectorAll('[data-obs-metric]').forEach(b=>b.onclick=()=>setObsTrendMetric(b.dataset.obsMetric));
  host.querySelectorAll('[data-obs-period]').forEach(el=>el.onclick=e=>{e.stopPropagation();const key=el.dataset.obsPeriod;if(!key)return;if(obsTrendGrain()==='all')setObsPeriod(key,'month');else setObsPeriod(key)});
  host.querySelectorAll('[data-obs-period-step]').forEach(b=>b.onclick=()=>obsShiftPeriod(Number(b.dataset.obsPeriodStep)));
  const periodInput=host.querySelector('#obsPeriodInput');
  if(periodInput)periodInput.onchange=()=>setObsPeriod(periodInput.value);
  const obsUserInput=host.querySelector('#obsUserCombobox input[name="obsUserFilter"]');
  if(obsUserInput)obsUserInput.onchange=()=>{state.obsUserFilter=obsUserInput.value||'';resetListPages('obs-');applyObsFilter()};
  const obsSourceSel=host.querySelector('#obsSourceFilter');if(obsSourceSel)obsSourceSel.onchange=()=>{state.obsSourceFilter=obsSourceSel.value;resetListPages('obs-');applyObsFilter()};
  host.querySelectorAll('[data-open-run]').forEach(b=>b.onclick=e=>{if(e.target.closest&&e.target.closest('a'))return;openRunFromId(b.dataset.openRun)});
  host.querySelectorAll('[data-jump]').forEach(b=>b.onclick=()=>switchView(b.dataset.jump));
  host.querySelectorAll('[data-edit-issue]').forEach(b=>b.onclick=e=>{if(e.target.closest&&e.target.closest('a'))return;const issue=(state.data.issues||[]).find(i=>i.id===b.dataset.editIssue);if(issue)openIssue(issue);else openIssueDetail(b.dataset.editIssue)});
  host.querySelectorAll('[data-obs-delivery]').forEach(b=>b.onclick=()=>{state.obsDeliveryFilter=b.dataset.obsDelivery;resetListPage('obs-deliveries');renderObservability()});
  bindPager(host);
}
function obsSourceLabel(s){return s==='gongfeng'?'工蜂':s==='tapd'?'TAPD':s==='local'?'本地':s==='github'?'GitHub':s||'—'}
function obsCategoryLabel(k){return ({requirement:'需求开发',bugfix:'Bug 修复',refactor:'技术优化',other:'其他'}[k])||k||'未分类'}
function obsOverview(){
  const view=obsScopedView();
  const summary=state.analytics?.summary||{};
  const demo=view.demo||{};
  const traces=view.traces||[];
  const succeeded=view.succeeded||0;
  const failed=view.failed||0;
  const reworked=view.reworked;
  const cancelled=view.cancelled||0;
  const total=view.total||0;
  const totalTokens=view.tokens||0;
  const totalCost=view.cost||0;
  const stats=(state.analytics?.workflows||[]).map(w=>({...w,name:w.name||w.key}));
  const wfRows=stats.map(w=>`<tr><td><b>${escapeHTML(w.name)}</b></td><td>${w.runs}</td><td>${percent(w.successRate)}</td><td>${percent(w.reworkRate)}</td><td>${percent(w.cancelRate)}</td><td>${formatTokens(w.tokens)}</td><td>${formatTokens(w.inputTokens)} / ${formatTokens(w.outputTokens)}</td><td>${w.tokenSharePct!=null?w.tokenSharePct.toFixed(1)+'%':'—'}</td><td>${costLabel(w.costUsd,true)}</td><td>${w.costSharePct!=null?w.costSharePct.toFixed(1)+'%':'—'}</td><td>${w.averageExecutionDurationMs?duration(w.averageExecutionDurationMs):'—'}</td></tr>`).join('');
  const modelRows=(state.analytics?.models||[]).map(m=>`<tr><td class="mono">${escapeHTML(m.name||m.key)}</td><td>${m.runs}</td><td>${formatTokens(m.tokens)}</td><td>${formatTokens(m.inputTokens)} / ${formatTokens(m.outputTokens)}</td><td>${m.tokenSharePct!=null?m.tokenSharePct.toFixed(1)+'%':'—'}</td><td>${costLabel(m.costUsd,true)}</td><td>${m.costSharePct!=null?m.costSharePct.toFixed(1)+'%':'—'}</td><td>${percent(m.successRate)}</td><td>${m.averageExecutionDurationMs?duration(m.averageExecutionDurationMs):'—'}</td></tr>`).join('');
  const userRows=(state.analytics?.userUsage||[]).map(u=>`<tr><td>${escapeHTML(u.date)}</td><td><b>${escapeHTML(obsUserName(u.userId))}</b></td><td>${u.runs}</td><td>${formatTokens(u.tokens)}</td><td>${formatTokens(u.inputTokens)} / ${formatTokens(u.outputTokens)}</td><td>${costLabel(u.costUsd,u.costKnown)}</td><td>${u.executionDurationMs?duration(u.executionDurationMs):'—'}</td></tr>`);
  const memberRows=obsUserTotals().map(u=>`<tr><td><b>${escapeHTML(obsUserName(u.userId))}</b></td><td>${u.runs}</td><td>${u.success||0}</td><td>${u.failed||0}</td><td>${formatTokens(u.tokens)}</td><td>${formatTokens(u.inputTokens)} / ${formatTokens(u.outputTokens)}</td><td>${costLabel(u.costUsd,u.costKnown)}</td><td>${u.executionDurationMs?duration(u.executionDurationMs):'—'}</td></tr>`).join('');
  const srcRows=(state.analytics?.issueSources||[]).map(s=>`<tr><td><b>${obsSourceLabel(s.key)}</b></td><td>${s.runs}</td><td>${s.succeeded||0}</td><td>${s.completedIssues||0}</td><td>${formatTokens(s.tokens)}</td><td>${costLabel(s.costUsd,true)}</td><td>${s.averageExecutionDurationMs?duration(s.averageExecutionDurationMs):'—'}</td></tr>`).join('');
  const catRows=(state.analytics?.categories||[]).map(c=>`<tr><td><b>${escapeHTML(obsCategoryLabel(c.key))}</b></td><td>${c.runs}</td><td>${c.succeeded||0}</td><td>${c.completedIssues||0}</td><td>${formatTokens(c.tokens)}</td><td>${costLabel(c.costUsd,true)}</td></tr>`).join('');
  const versionRows=(state.analytics?.workflowVersions||[]).map(v=>`<tr><td><b>${escapeHTML(v.name||v.workflowId)}</b></td><td class="mono">v${v.version}</td><td>${v.runs}</td><td>${percent(v.successRate)}</td><td>${v.averageExecutionDurationMs?duration(v.averageExecutionDurationMs):'—'}</td><td>${formatTokens(v.tokens)}</td><td>${costLabel(v.costUsd,true)}</td><td>${v.insufficientSample?'<span class="muted">样本不足</span>':'—'}</td></tr>`).join('');
  const profileRows=stats.filter(w=>w.nodeCount>0).map(w=>`<tr><td><b>${escapeHTML(w.name)}</b></td><td>${w.nodeCount}</td><td>${Object.entries(w.nodeTypes||{}).map(([k,v])=>`${escapeHTML(k)}×${v}`).join('，')||'—'}</td><td>${escapeHTML(obsCategoryLabel(w.category))}</td><td>${w.runs}</td></tr>`).join('');
  const nodeStats=state.analytics?.nodes||[];
  const failReasonRows=(state.analytics?.failures||[]).slice(0,12).map(f=>`<tr><td>${escapeHTML(f.nodeId||'—')}</td><td title="${escapeHTML(f.code||'')}">${escapeHTML((f.code||'').slice(0,80))}</td><td>${f.count}</td></tr>`).join('');
  const nodeQualityRows=[...nodeStats].sort((a,b)=>(b.failedNodes||0)-(a.failedNodes||0)).slice(0,16).map(n=>`<tr><td>${escapeHTML(n.name||n.key)}</td><td>${n.runs}</td><td>${n.succeeded||0}</td><td>${n.failedNodes||0}</td><td>${n.reworkNodes||0}</td><td>${n.retryRate!=null?percent(n.retryRate):'—'}</td><td>${n.averageExecutionDurationMs?duration(n.averageExecutionDurationMs):'—'}</td><td>${formatTokens(n.tokens)}</td><td>${n.toolCalls||0}</td></tr>`).join('');
  const slowRows=[...nodeStats].filter(n=>n.averageExecutionDurationMs>0).sort((a,b)=>b.averageExecutionDurationMs-a.averageExecutionDurationMs).slice(0,10).map(n=>`<tr><td>${escapeHTML(n.name||n.key)}</td><td>${n.runs}</td><td>${duration(n.averageExecutionDurationMs)}</td><td>${n.p95ExecutionDurationMs?duration(n.p95ExecutionDurationMs):'—'}</td><td>${percent(n.successRate)}</td><td>${formatTokens(n.tokens)}</td></tr>`).join('');
  const trend=state.analytics?.trend||[];
  const visibleTrend=obsTrendWindow(trend);
  const grainName=obsGrainCaption();
  const rangeName=obsRangeCaption();
  const metric=obsTrendMetric();
  const metricBtns=[['tokens','Token'],['runs','运行'],['issues','需求量'],['cost','成本']].map(([id,l])=>`<button type="button" data-obs-metric="${id}" class="${metric===id?'active':''}">${l}</button>`).join('');
  const tokenDelta=obsPeriodDelta(trend,'tokens'),runDelta=obsPeriodDelta(trend,'runs'),issueDelta=obsPeriodDelta(trend,'issues'),costDelta=obsPeriodDelta(trend,'cost');
  const maxTrend=Math.max(1,...visibleTrend.map(t=>t.tokens||0));
  const selectedPeriod=obsPeriodKey();
  const trendRows=visibleTrend.slice().reverse().map(t=>`<tr class="run-row${t.date===selectedPeriod?' obs-period-active':''}" data-obs-period="${escapeHTML(t.date||'')}"><td>${escapeHTML(obsTrendLabel(t))}</td><td>${t.runs}</td><td>${t.issues||0}</td><td>${t.succeeded}</td><td>${t.failed||0}</td><td>${formatTokens(t.tokens)}</td><td>${formatTokens(t.inputTokens)} / ${formatTokens(t.outputTokens)}</td><td>${costLabel(t.costUsd,true)}</td><td>${t.executionDurationMs?duration(t.executionDurationMs):'—'}</td><td><span class="obs-mini-bar"><i style="width:${Math.round((t.tokens||0)*100/maxTrend)}%"></i></span></td></tr>`).join('');
  const failRows=traces.filter(t=>t.status==='failed').slice(0,5).map(t=>{
    const label=[obsSourceLabel(t.issueSource||'local'),t.externalId||t.issueId||t.runId].filter(Boolean).join(' ');
    return `<tr class="run-row" data-open-run="${escapeHTML(t.runId||'')}"><td>${escapeHTML(label)}</td><td><span class="status ${escapeHTML(t.status||'')}">${runStatus(t.status)}</span></td><td>${relativeTime(t.createdAt)}</td></tr>`;
  }).join('');
  const recent=traces.slice(0,8).map(t=>{
    const label=[obsSourceLabel(t.issueSource||'local'),t.externalId||t.issueId||t.runId].filter(Boolean).join(' ');
    return `<tr class="run-row" data-open-run="${escapeHTML(t.runId||'')}"><td>${escapeHTML(label)}</td><td><span class="status ${escapeHTML(t.status||'')}">${runStatus(t.status)}</span></td><td>${t.durationMs?duration(t.durationMs):'—'}</td><td>${formatTokens(t.tokens)}</td><td>${costLabel(t.costUsd,t.costKnown)}</td></tr>`;
  }).join('');
  const traceRows=traces.map(t=>{
    const source=obsSourceLabel(t.issueSource||'local');
    const issueRef=t.externalId||t.issueId||'—';
    const issue=t.externalUrl?`<a href="${escapeHTML(safeURL(t.externalUrl))}" target="_blank" rel="noopener noreferrer">${escapeHTML(source+' '+issueRef)}</a>`:`${escapeHTML(source+' '+issueRef)}`;
    const task=t.workflowTaskId?`<code title="${escapeHTML(t.workflowTaskId)}">${escapeHTML(t.workflowTaskId.slice(-12))}</code>`:'—';
    const runId=String(t.runId||''),traceId=String(t.traceId||t.runId||'');
    const relation=`<div class="obs-trace-ids"><code title="${escapeHTML(runId)}">Run ${escapeHTML(runId.slice(-12))}</code><code title="${escapeHTML(traceId)}">Trace ${escapeHTML(traceId.slice(-12))}</code></div>`;
    const observed=(t.toolCallsSucceeded||0)+(t.toolCallsFailed||0);
    const tools=observed?`${t.toolCallsSucceeded||0}/${observed} 成功`:(t.toolCalls?`${t.toolCalls} 次 · 结果未上报`:'—');
    const error=t.errorMessage?`<span class="obs-trace-error" title="${escapeHTML(t.errorMessage)}">${escapeHTML(t.errorNodeId||'Run')} · ${escapeHTML(t.errorMessage)}</span>`:'—';
    return `<tr class="run-row" data-open-run="${escapeHTML(runId)}"><td>${issue}</td><td>${task}</td><td>${relation}</td><td><span class="status ${escapeHTML(t.status||'')}">${runStatus(t.status)||escapeHTML(t.status||'—')}</span></td><td>${t.durationMs?duration(t.durationMs):'—'}</td><td>${formatTokens(t.tokens)}</td><td>${costLabel(t.costUsd,t.costKnown)}</td><td>${tools}</td><td>${t.humanInterventions||0}</td><td>${error}</td></tr>`;
  });
  const uniqueIssues=view.uniqueIssues||0;
  const tokenFoot=view.inputTokens!=null?`入 ${formatTokens(view.inputTokens)} / 出 ${formatTokens(view.outputTokens)}`:(view.period?`该${obsGrainCaption()} ${formatTokens(totalTokens)}`:`累计 ${formatTokens(totalTokens)}`);
  const successFoot=`${succeeded} 成功 / ${failed} 失败${view.firstPass!=null?` · 一次通过 ${total?percent(view.firstPass):'—'}`:''}`;
  const costFoot=reworked==null?`该${obsGrainCaption()} ${costLabel(totalCost,view.costKnown)}`:`返工 ${reworked}${view.period?'':''}`;
  return `<div class="obs-hero">
    <article><span>${rangeName}运行</span><b>${total}</b><small>${view.active||0} 进行中${view.period?` · 累计 ${summary.totalRuns||0}`:''}</small>${obsTrendGrain()==='all'?'':obsDeltaChip(runDelta)}</article>
    <article><span>${rangeName}需求</span><b>${uniqueIssues}</b><small>每单 ${view.runsPerIssue!=null?Number(view.runsPerIssue).toFixed(1):'—'} 次${view.period?` · 累计 ${summary.uniqueIssues||0}`:''}</small>${obsTrendGrain()==='all'?'':obsDeltaChip(issueDelta)}</article>
    <article><span>${rangeName} Token</span><b>${formatTokens(totalTokens)}</b><small>${tokenFoot}${view.period?` · 累计 ${formatTokens(summary.totalTokens)}`:''}</small>${obsTrendGrain()==='all'?'':obsDeltaChip(tokenDelta,true)}</article>
    <article><span>成功率</span><b>${total?percent(view.successRate):'—'}</b><small>${successFoot}</small></article>
    <article><span>${rangeName}成本</span><b>${costLabel(totalCost,view.costKnown)}</b><small>${costFoot}${view.period?` · 累计 ${costLabel(summary.totalCostUsd,summary.costUnknownRuns===0)}`:''}</small>${obsTrendGrain()==='all'?'':obsDeltaChip(costDelta,true)}</article>
    <article><span>P95 执行</span><b>${view.p95?duration(view.p95):'—'}</b><small>平均 ${view.avgExec?duration(view.avgExec):'—'}${view.wait?` · 等待 ${duration(view.wait)}`:''}</small></article>
  </div>
  <div class="obs-card obs-demo-card">
    <div class="obs-card-title"><div>Loop 指标<small>${view.period?`仅统计 ${escapeHTML(rangeName)} 内的 Run，可点趋势换时段`:'所有数字均可下钻到 workflow_task_id、run_id、trace_id 和原始事件'}</small></div><span class="sub">自动完成率 = 无人工干预的成功 Run ÷ 成功 Run</span></div>
    ${obsKpiRow([
      {label:'平均 Token / Run',value:total?formatTokens(demo.averageTokensPerRun):'—',foot:`${formatTokens(totalTokens)} tokens ÷ ${total} runs`,color:'#5b8cff'},
      {label:'工具调用成功率',value:demo.toolCallsObserved?percent(demo.toolSuccessRate):'—',foot:`${demo.toolCallsSucceeded||0} 成功 / ${demo.toolCallsFailed||0} 失败 · 仅统计完成事件`,color:'#34d399'},
      {label:'平均人工干预',value:total?Number(demo.averageHumanInterventions||0).toFixed(2):'—',foot:`${demo.humanInterventions||0} 次人工决策 ÷ ${total} runs · 不含暂停/恢复`,color:'#fbbf24'},
      {label:'自动完成率',value:succeeded?percent(demo.automationRate):'—',foot:`${demo.automatedSucceededRuns||0} 个全自动成功 / ${succeeded} 个成功`,color:'#8b5cf6'},
    ])}
  </div>
  ${obsTableCard('关联与追溯','需求 → workflow_task_id → run_id / trace_id；点击一行查看事件明细',['需求','workflow_task_id','Run / Trace','状态','耗时','Tokens','成本','工具','人工','错误位置'],traceRows,10,'obs-traces')}
  <div class="obs-card obs-trend-card">
    <div class="obs-card-title">
      <div>趋势变化<small>按${grainName}对比 Token、需求量和运行量；点击某${grainName}查看该时段指标</small></div>
      <div class="obs-metric-switch">${metricBtns}</div>
    </div>
    <div class="obs-trend-compare">
      <span>最近一${grainName}（含未结束周期）Token ${obsFormatTrendValue(tokenDelta?tokenDelta.current:obsTrendValue(visibleTrend[visibleTrend.length-1]||{},'tokens'),'tokens')}</span>
      ${obsDeltaChip(tokenDelta,true)||'<em class="obs-delta flat">样本不足，暂无环比</em>'}
      <span>需求 ${obsFormatTrendValue(issueDelta?issueDelta.current:obsTrendValue(visibleTrend[visibleTrend.length-1]||{},'issues'),'issues')}</span>
      ${obsDeltaChip(issueDelta)||''}
    </div>
    ${obsTrendChart(trend,metric)}
    <table class="obs-table obs-trend-table"><thead><tr><th>${grainName}</th><th>运行</th><th>需求</th><th>成功</th><th>失败</th><th>Tokens</th><th>输入 / 输出</th><th>成本</th><th>执行耗时</th><th></th></tr></thead><tbody>${trendRows||'<tr><td colspan="10" class="detail-empty">暂无趋势</td></tr>'}</tbody></table>
  </div>
  ${obsCrossProjectCard()}
  <div class="obs-split">
    <div class="obs-card"><div class="obs-card-title">质量信号<span class="sub">失败优先处理</span></div><div class="obs-signal-row"><div><b>${failed}</b><span>失败</span></div><div><b>${reworked}</b><span>返工</span></div><div><b>${cancelled}</b><span>取消</span></div></div><table class="obs-table"><thead><tr><th>最近失败</th><th>状态</th><th>时间</th></tr></thead><tbody>${failRows||'<tr><td colspan="3" class="detail-empty">没有失败运行</td></tr>'}</tbody></table></div>
    <div class="obs-card"><div class="obs-card-title">最近运行<span class="sub">点击打开对应需求</span></div><table class="obs-table"><thead><tr><th>需求 / 流程</th><th>状态</th><th>耗时</th><th>Tokens</th><th>成本</th></tr></thead><tbody>${recent||'<tr><td colspan="5" class="detail-empty">暂无运行</td></tr>'}</tbody></table></div>
  </div>
  ${obsTableCard('SOP 一览','成功率、返工、取消和各流程占了多少 Token / 成本',['SOP','运行','成功率','返工率','取消率','Tokens','输入 / 输出','Token 占比','成本','成本占比','均执行'],wfRows,11)}
  ${obsTableCard('各模型用量','每个模型花了多少 Token 和钱',['模型','调用','Tokens','输入 / 输出','Token 占比','成本','成本占比','成功率','均执行'],modelRows,9)}
  ${obsScope()==='project'?obsTableCard('成员汇总','当前项目全部责任人，按 Token 排序',['成员','运行','成功','失败','Tokens','输入 / 输出','成本','执行耗时'],memberRows,8):''}
  ${obsTableCard('每人每日用量','责任人 × 自然日',['日期','成员','运行','Tokens','输入 / 输出','成本','执行耗时'],userRows,7,'obs-users')}
  <div class="obs-split">
    ${obsTableCard('需求类型分布','按需求来源聚合，已完成数按需求去重',['来源','关联执行','SOP 成功','已交付需求','Tokens','成本','平均执行'],srcRows,7)}
    ${obsTableCard('编排类型分布','按 SOP 统计标签归类，完成需求数已按需求去重',['统计标签','执行','成功','完成需求','Tokens','成本'],catRows,6)}
  </div>
  ${obsTableCard('SOP 版本对比','同一 SOP 不同版本的执行效果；样本 < 5 标注样本不足',['SOP','版本','运行','成功率','均执行耗时','Tokens','成本','提示'],versionRows,8)}
  ${profileRows?obsTableCard('SOP 编排画像','节点数、节点类型构成与受控统计标签',['SOP','节点数','节点类型构成','统计标签','使用频次'],profileRows,5):''}
  ${obsTableCard('节点质量明细','按失败节点数排序；重试率 = 重试节点 ÷ 节点执行次数',['节点','执行','成功','失败','返工','重试率','均执行耗时','Tokens','工具调用'],nodeQualityRows,9)}
  <div class="obs-split">
    ${obsTableCard('失败原因','节点错误归一化后的高频原因',['节点','错误','次数'],failReasonRows,3)}
    ${obsTableCard('慢节点','按平均执行耗时排序',['节点','执行','均耗时','P95','成功率','Tokens'],slowRows,6)}
  </div>`;
}
function obsSkillhub(){
  const d=state.skillhubEfficiency;
  if(!d)return `<div class="detail-empty">正在加载 SkillHub 提效数据…</div>`;
  const loop=d.loop||{};
  const gf=d.gongfeng||{};
  const local=d.localWorkflows||{};
  const tp=gf.throughput||{};
  const totals=gf.totals||{};
  const cycle=gf.cycle||{};
  const qci=d.qci||{};
  const deploy30=(qci.backend?.recent30d||0)+(qci.frontend?.recent30d||0);
  const hero=[
    {label:'已合入',value:'725',foot:'大仓 210 · 老库 515'},
    {label:'近 4 周日均',value:'6.45',foot:'129 个 / 20 个工作日'},
    {label:'相对起步',value:'7.82×',foot:'起步 8 周日均 0.83'},
    {label:'测试环境部署',value:'1589',foot:`近 30 天 ${deploy30||375} · E2E 482`},
  ].map(c=>`<div class="sh-kpi"><span>${escapeHTML(c.label)}</span><b>${escapeHTML(c.value)}</b><small>${escapeHTML(c.foot)}</small></div>`).join('');
  const repos=(gf.repos||[]).map(r=>{
    const url=safeURL(r.url||'');
    const name=url?`<a href="${escapeHTML(url)}" target="_blank" rel="noopener noreferrer">${escapeHTML(r.name)}</a>`:escapeHTML(r.name);
    return `<tr><td>${name}<div class="muted">${escapeHTML(r.path||'')}</div></td><td>${r.created||0}</td><td><b>${r.merged||0}</b></td><td>${r.opened||0}</td><td>${escapeHTML((r.lastAt||'').slice(0,10))}</td></tr>`;
  }).join('');
  const authorRows=(gf.authors||[]).slice(0,8).map(a=>`<tr><td>${escapeHTML(a.username)}</td><td>${a.mrs||0}</td></tr>`).join('');
  const stages=local.backendStageHits||{};
  const stageRows=[['01-requirement','需求'],['02-design','设计'],['03-code','编码'],['04-e2e','E2E']].map(([k,l])=>`<tr><td>${l}</td><td>${stages[k]||0} / ${local.backendUnique||0}</td></tr>`).join('');
  const links=(d.evidence||[]).map(e=>`<a class="sh-link" href="${escapeHTML(safeURL(e.url))}" target="_blank" rel="noopener noreferrer">${escapeHTML(e.label)}</a>`).join('');
  const snap=d.snapshotAt?new Date(d.snapshotAt).toLocaleString():'—';
  return `<div class="sh-page">
    <header class="sh-head">
      <div><h2>SkillHub 提效</h2><p>合入从起步每天不到 1 个，到近四周每个工作日 6.45 个。灰柱老库，绿柱大仓。</p></div>
      <span class="sh-head-meta">快照 ${escapeHTML(snap)}</span>
    </header>
    <div class="sh-kpis">${hero}</div>
    ${obsSkillhubCharts(d)}
    <div class="sh-vs">
      <article>
        <span>起步 · 前 8 周</span>
        <b>0.83</b>
        <em>每个工作日</em>
        <p>33 个已合入 · 40 个工作日</p>
      </article>
      <article class="is-now">
        <span>近 4 周</span>
        <b>6.45</b>
        <em>每个工作日</em>
        <p>129 个已合入 · 大仓 125 · 7.82 倍</p>
      </article>
    </div>
    <div class="obs-split">
      ${obsTableCard('仓库','',['仓库','创建','已合入','打开','最近合入'],repos,5)}
      ${obsTableCard('作者 TOP','三库合计',['作者','MR'],authorRows,2)}
    </div>
    ${obsSkillhubDeployCard()}
    ${obsSkillhubLoopCard(loop)}
    <div class="obs-split">
      ${obsTableCard('后端工作流',(local.backendUnique||0)+' 条',['阶段','覆盖'],stageRows,2)}
      <div class="obs-card">
        <div class="obs-card-title">本地产物</div>
        <div class="obs-signal-row">
          <div><b>${local.frontendWorkflows||0}</b><span>前端工作流</span></div>
          <div><b>${local.backendKnowledgeDocs||0}</b><span>知识文档</span></div>
          <div><b>${local.backendE2eFiles||0}</b><span>E2E 文档</span></div>
        </div>
      </div>
    </div>
    <div class="obs-card"><div class="obs-card-title">数据来源</div><div class="sh-links">${links}</div></div>
    ${obsSkillhubInsights(d.insights||[])}
  </div>`;
}
function obsSkillhubCharts(d){
  const gf=d.gongfeng||{};
  const months=Object.keys(gf.mergedMonths||{}).sort();
  const weekKeys=Object.keys(gf.weeklyMerged||{}).sort().slice(-12);
  const legend=`<div class="sh-chart-legend"><span><i class="legacy"></i>老库</span><span><i class="mono"></i>大仓</span></div>`;
  return `<div class="sh-charts">
    ${obsSkillhubChart({title:'每月已合入',klass:'sh-chart-hero',labels:months.map(m=>Number(m.slice(5))+'月'),stacks:[{color:'#9aa3b5',values:months.map(m=>gf.monthlyLegacy?.[m]||0)},{color:'#22c55e',values:months.map(m=>gf.monthlyMonorepo?.[m]||0)}],showValue:true,legend})}
    ${obsSkillhubChart({title:'近 12 周',labels:weekKeys.map(k=>'W'+k.slice(-2)),stacks:[{color:'#9aa3b5',values:weekKeys.map(k=>gf.weeklyLegacy?.[k]||0)},{color:'#22c55e',values:weekKeys.map(k=>gf.weeklyMonorepo?.[k]||0)}],markFrom:weekKeys.length-4,legend})}
  </div>`;
}
function obsSkillhubChart({title,klass,labels,stacks,showValue,markFrom,legend,xStep}){
  const cols=labels||[];
  if(!cols.length||!stacks?.length)return '';
  const w=640,h=248,l=42,r=10,t=22,b=30;
  const plotW=w-l-r,plotH=h-t-b;
  const totals=cols.map((_,i)=>stacks.reduce((s,st)=>s+(Number(st.values[i])||0),0));
  const max=Math.max(1,...totals);
  const gap=cols.length>8?6:16;
  const barW=Math.max(8,(plotW-gap*(cols.length+1))/cols.length);
  const step=xStep||(cols.length>10?Math.ceil(cols.length/6):1);
  const yTicks=[0,0.5,1].map(p=>{
    const y=t+plotH*(1-p),val=Math.round(max*p);
    return `<line x1="${l}" y1="${y.toFixed(1)}" x2="${w-r}" y2="${y.toFixed(1)}"/><text class="obs-chart-y" x="${l-8}" y="${(y+3).toFixed(1)}" text-anchor="end">${val}</text>`;
  }).join('');
  let mark='';
  if(markFrom!=null&&markFrom>=0){
    const x1=l+gap+markFrom*(barW+gap)-gap/2;
    const x2=l+gap+(cols.length-1)*(barW+gap)+barW+gap/2;
    mark=`<rect x="${x1.toFixed(1)}" y="${t}" width="${Math.max(0,x2-x1).toFixed(1)}" height="${plotH}" fill="rgba(91,140,255,.08)"></rect>`;
  }
  const bars=cols.map((lb,i)=>{
    const x=l+gap+i*(barW+gap);
    let y=t+plotH;
    const rects=stacks.map(st=>{
      const bh=plotH*(Number(st.values[i])||0)/max;
      y-=bh;
      return `<rect x="${x.toFixed(1)}" y="${y.toFixed(1)}" width="${barW.toFixed(1)}" height="${Math.max(0,bh).toFixed(1)}" fill="${st.color}"></rect>`;
    }).join('');
    const n=totals[i];
    const value=showValue&&n?`<text x="${(x+barW/2).toFixed(1)}" y="${Math.max(12,y-6).toFixed(1)}" text-anchor="middle" class="sh-bar-n">${n}</text>`:'';
    const tick=i===0||i===cols.length-1||i%step===0?`<text x="${(x+barW/2).toFixed(1)}" y="${h-10}" text-anchor="middle">${escapeHTML(lb)}</text>`:'';
    return `<g>${rects}${value}${tick}</g>`;
  }).join('');
  return `<div class="obs-card sh-chart-card ${klass||''}">
    <div class="sh-chart-head"><b>${escapeHTML(title)}</b>${legend||''}</div>
    <svg class="sh-bars" viewBox="0 0 ${w} ${h}" role="img" aria-label="${escapeHTML(title)}">${yTicks}${mark}${bars}</svg>
  </div>`;
}
function obsSkillhubLoopCard(loop){
  if(!loop||!(loop.issues||loop.totalRuns||loop.doneIssues))return '';
  return `<div class="obs-card" style="margin-bottom:14px">
    <div class="obs-card-title">Loop 运行</div>
    <div class="obs-signal-row">
      <div><b>${loop.issues||0}</b><span>平台需求</span></div>
      <div><b>${loop.doneIssues||0}</b><span>已闭环</span></div>
      <div><b>${loop.totalRuns||0}</b><span>SOP 运行</span></div>
    </div>
  </div>`;
}
function obsSkillhubInsights(items){
  if(!items.length)return '';
  const rows=items.map(it=>`<details class="sh-insight"><summary>${escapeHTML(it.title||'')}</summary><p>${escapeHTML(it.body||'')}</p></details>`).join('');
  return `<section class="sh-appendix"><div class="sh-chart-head"><b>结论</b><span class="sub">AI 分析 · 点开看</span></div><div class="sh-insights">${rows}</div></section>`;
}
function obsSkillhubDeployCard(){
  const snap=state.skillhubEfficiency?.qci||{};
  const deploy=snap.deploy||{},e2e=snap.e2e||{},backend=snap.backend||{},frontend=snap.frontend||{},totals=snap.totals||{};
  const err=state.skillhubDeployError;
  const backendURL=safeURL(deploy.url||snap.url||'');
  const frontendURL=safeURL((snap.pipelines||[]).find(p=>p.id===11867492)?.url||'');
  const e2eURL=safeURL(e2e.url||'');
  const liveNote=err?`实时接口：${err}`:'';
  const pipeRows=(snap.pipelines||[]).map(p=>{
    const url=safeURL(p.url||'');
    const name=url?`<a href="${escapeHTML(url)}" target="_blank" rel="noopener noreferrer">${escapeHTML(p.name||'')}</a>`:escapeHTML(p.name||'');
    return `<tr><td>${escapeHTML(p.kind||'')}</td><td>${name}<div class="muted">${escapeHTML(p.era||'')}</div></td><td><b>${p.runNum||0}</b></td><td>${p.recent30d||0}</td></tr>`;
  }).join('');
  return `<div class="obs-card" style="margin-bottom:14px"><div class="obs-card-title">测试环境部署<span class="sub">智研 ${snap.projectId||10001} · ${snap.countedPipelines||13} 条流水线</span></div>
    <div class="obs-signal-row">
      <div><b>${backend.runNum||0}</b><span>后端测试环境${backendURL?` · <a href="${escapeHTML(backendURL)}" target="_blank" rel="noopener noreferrer">打开</a>`:''}</span></div>
      <div><b>${frontend.runNum||0}</b><span>前端测试环境${frontendURL?` · <a href="${escapeHTML(frontendURL)}" target="_blank" rel="noopener noreferrer">打开</a>`:''}</span></div>
      <div><b>${e2e.runNum||0}</b><span>独立 E2E${e2eURL?` · <a href="${escapeHTML(e2eURL)}" target="_blank" rel="noopener noreferrer">打开</a>`:''}</span></div>
    </div>
    ${obsSkillhubDeployCharts(snap)}
    ${pipeRows?`<div class="obs-table-wrap" style="margin-top:12px"><table class="obs-table"><thead><tr><th>类型</th><th>流水线</th><th>累计</th><th>近 30 天</th></tr></thead><tbody>${pipeRows}</tbody></table></div>`:''}
    ${liveNote?`<p class="sh-note">${escapeHTML(liveNote)}</p>`:''}</div>`;
}
function obsSkillhubDeployCharts(snap){
  const daily=snap.dailySample||{};
  const labels=daily.labels||[];
  const countLegend=`<div class="sh-chart-legend"><span><i style="background:#5b8cff"></i>后端</span><span><i style="background:#38bdf8"></i>前端</span><span><i style="background:#8b5cf6"></i>E2E</span></div>`;
  return `<div class="sh-charts is-single" style="margin-top:12px">${obsSkillhubChart({
    title:'每天部署次数',
    klass:'sh-chart-hero',
    labels,
    stacks:[
      {color:'#5b8cff',values:daily.backend||[]},
      {color:'#38bdf8',values:daily.frontend||[]},
      {color:'#8b5cf6',values:daily.e2e||[]}
    ],
    legend:countLegend,
    xStep:5
  })}</div>`;
}
function obsDelivery(){
  const d=state.analytics?.delivery||{},items=state.analytics?.deliveries||[];
  const filter=state.obsDeliveryFilter||'all';
  const sourceLabel=s=>s==='github'?'GitHub':s==='gongfeng'?'工蜂':s==='tapd'?'TAPD':s==='local'?'本地':s||'—';
  const mrLabel=s=>s==='merged'?'<span style="color:#34d399;font-weight:600">已合入</span>':s==='integrated'?'<span style="color:#fbbf24;font-weight:600">已合入基线</span>':({opened:'待合入',reopened:'重开',closed:'已关闭',none:'无 PR',unknown:'待查合入'}[s]||s||'—');
  const visible=items.filter(it=>{
    if(filter==='delivered')return it.delivered;
    if(filter==='sop')return it.sopSucceeded;
    if(filter==='mr')return it.mrState==='merged';
    if(filter==='baseline')return it.mrState==='integrated';
    if(filter==='github'||filter==='local'||filter==='gongfeng'||filter==='tapd')return (it.source||'local')===filter;
    return true;
  });
  const chips=[['all','全部'],['delivered','已交付'],['sop','SOP 成功'],['mr','PR 已合入'],['baseline','已合入基线'],['github','GitHub'],['local','本地'],...(items.some(it=>it.source==='gongfeng')?[['gongfeng','工蜂']]:[]),...(items.some(it=>it.source==='tapd')?[['tapd','TAPD']]:[])].map(([id,label])=>`<button class="obs-tab ${filter===id?'active':''}" data-obs-delivery="${id}">${label}</button>`).join('');
  const deliveryRows=visible.map(it=>{
    const issue=it.externalIid?`#${it.externalIid}`:(it.externalId||it.issueId);
    const mr=it.mrUrl?`<a href="${escapeHTML(safeURL(it.mrUrl))}" target="_blank" rel="noopener noreferrer">!${it.mrIid||''}</a>`:'—';
    const startedLanes=(it.lanes||[]).filter(l=>l.started);
    const sop=startedLanes.length?startedLanes.map(l=>`${l.label}${l.status==='succeeded'?'✓':''}`).join(' / '):(it.workflowName||it.workflowId);
    return `<tr class="run-row" data-edit-issue="${escapeHTML(it.issueId)}"><td>${sourceLabel(it.source)} ${escapeHTML(issue)}</td><td><b>${escapeHTML(it.title||it.issueId)}</b></td><td>${escapeHTML(sop)}</td><td><span class="status ${it.runStatus||''}">${it.sopSucceeded?'SOP 成功':(runStatus(it.runStatus)||it.runStatus||'—')}</span></td><td>${duration(it.durationMs)}</td><td>${it.executionDurationMs?duration(it.executionDurationMs):'—'}</td><td>${it.cycleDurationMs?duration(it.cycleDurationMs):'—'}</td><td>${it.runCount||0} 次</td><td>${escapeHTML(obsUserName(it.userId)||'—')}</td><td>${formatTokens(it.tokens)}</td><td>${costLabel(it.costUsd,it.costKnown)}</td><td>${mr} · ${mrLabel(it.mrState)}</td><td>${it.delivered?'已交付':'未交付'}</td></tr>`;
  });
  const pagedDeliveries=paginateList('obs-deliveries', deliveryRows);
  const rows=pagedDeliveries.items.join('');
  const summary=state.analytics?.summary||{};
  return obsKpiRow([
    {label:'已交付',value:d.delivered||0,foot:`SOP 成功且已在默认分支 · ${d.issues||0} 个有运行的单`,color:'#34d399'},
    {label:'完成需求 / Bug',value:`${d.requirementCount||0} / ${d.bugFixCount||0}`,foot:`按 SOP 统计标签归类，需求去重`,color:'#5b8cff'},
    {label:'PR 已合入',value:d.mrMerged||0,foot:`仅默认分支；${d.mrIntegrated||0} 个已合入基线`,color:'#38bdf8'},
    {label:'平均执行耗时',value:d.averageExecutionDurationMs?duration(d.averageExecutionDurationMs):'—',foot:`P50 ${d.p50ExecutionDurationMs?duration(d.p50ExecutionDurationMs):'—'} · P95 ${d.p95ExecutionDurationMs?duration(d.p95ExecutionDurationMs):'—'}`,color:'#8b5cf6'},
    {label:'平均端到端耗时',value:d.averageDurationMs?duration(d.averageDurationMs):'—',foot:`累计 ${formatTokens(d.totalTokens||0)} tokens`,color:'#38bdf8'},
    {label:'人均周吞吐',value:summary.weeklyActiveContributors?Number(summary.weeklyThroughputPerUser||0).toFixed(2):'—',foot:`近 7 天 ${summary.weeklyDeliveredIssues||0} 个交付 / ${summary.weeklyActiveContributors||0} 人`,color:'#34d399'},
    {label:'每需求平均执行次数',value:summary.runsPerIssue!=null?Number(summary.runsPerIssue).toFixed(2):'—',foot:`失败 ${summary.failuresPerIssue!=null?Number(summary.failuresPerIssue).toFixed(2):'—'} 次/需求 · 进行中 ${summary.inProgressIssues||0} 个`,color:'#fbbf24'},
  ])+`<div class="obs-card" style="margin-bottom:14px"><div class="obs-card-title">需求交付明细<span class="sub">耗时为该需求最近一次执行的端到端时间；执行耗时 = 该需求全部执行按 SOP 节点求和；周期为首次执行到最后一次执行。「已合入 / 已交付」只统计默认分支。</span></div><div class="obs-tabs" style="margin:0 0 10px">${chips}</div><table class="obs-table"><thead><tr><th>单号</th><th>标题</th><th>SOP</th><th>流程</th><th>端到端</th><th>执行耗时</th><th>周期</th><th>执行次数</th><th>负责人</th><th>Tokens</th><th>成本</th><th>PR</th><th>交付</th></tr></thead><tbody>${rows||'<tr><td colspan="13" class="detail-empty">还没有带需求上下文的运行</td></tr>'}</tbody></table>${pagerHTML('obs-deliveries',pagedDeliveries)}</div>`;
}
function buildSkillhubReportHTML(){
  const d=state.skillhubEfficiency;
  if(!d||!canShowSkillhubEfficiency())return '';
  const gf=d.gongfeng||{},loop=d.loop||{},local=d.localWorkflows||{},tp=gf.throughput||{},totals=gf.totals||{};
  const repoRows=(gf.repos||[]).map(r=>`<tr><td>${escapeHTML(r.name)}</td><td>${r.created||0}</td><td>${r.merged||0}</td><td>${r.opened||0}</td><td>${r.closed||0}</td></tr>`).join('');
  const insightRows=(d.insights||[]).map(c=>`<tr><td>${escapeHTML(c.title)}</td><td>${escapeHTML(c.body||'')}</td></tr>`).join('');
  return `<section class="block"><h2>SkillHub 提效看板</h2>
  <p>已合入 PR ${totals.merged||0} · 近窗口日均 ${tp.currentDaily||'—'} · Loop 闭环 ${loop.doneIssues||0}/${loop.issues||0} · 本地工作流 ${local.backendUnique||0}+${local.frontendWorkflows||0}</p></section>
  ${obsReportTable('工蜂 MR',['仓库','创建','已合入','打开','关闭'],repoRows)}
  ${obsReportTable('AI 分析',['结论','依据'],insightRows)}`;
}
function obsPlain(v){return String(v==null?'':v).replace(/<[^>]+>/g,'')}
function obsReportScope(){
  const project=currentProject();
  const dim=obsScope()==='project'?'项目':'个人';
  const who=obsScope()==='project'?(project?.name||project?.id||'当前项目'):(state.obsUserFilter?obsUserName(state.obsUserFilter):'全部成员');
  const source=state.obsSourceFilter?obsSourceLabel(state.obsSourceFilter):'全部来源';
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
    if(canShowSkillhubEfficiency()&&!state.skillhubEfficiency)await refreshSkillhubEfficiency();
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
  const wfRows=stats.map(w=>`<tr><td>${escapeHTML(w.name)}</td><td>${w.runs}</td><td>${percent(w.successRate)}</td><td>${percent(w.reworkRate)}</td><td>${formatTokens(w.tokens)}</td><td>${obsPlain(costLabel(w.costUsd,true))}</td><td>${w.averageExecutionDurationMs?duration(w.averageExecutionDurationMs):'—'}</td></tr>`).join('');
  const modelRows=(state.analytics?.models||[]).map(m=>`<tr><td>${escapeHTML(m.name||m.key)}</td><td>${m.runs}</td><td>${formatTokens(m.tokens)}</td><td>${formatTokens(m.inputTokens)} / ${formatTokens(m.outputTokens)}</td><td>${m.tokenSharePct!=null?m.tokenSharePct.toFixed(1)+'%':'—'}</td><td>${obsPlain(costLabel(m.costUsd,true))}</td></tr>`).join('');
  const userRows=(state.analytics?.userUsage||[]).slice(0,40).map(u=>`<tr><td>${escapeHTML(u.date)}</td><td>${escapeHTML(obsUserName(u.userId))}</td><td>${u.runs}</td><td>${formatTokens(u.tokens)}</td><td>${formatTokens(u.inputTokens)} / ${formatTokens(u.outputTokens)}</td><td>${obsPlain(costLabel(u.costUsd,u.costKnown))}</td></tr>`).join('');
  const memberRows=obsUserTotals().map(u=>`<tr><td>${escapeHTML(obsUserName(u.userId))}</td><td>${u.runs}</td><td>${u.success||0}</td><td>${u.failed||0}</td><td>${formatTokens(u.tokens)}</td><td>${formatTokens(u.inputTokens)} / ${formatTokens(u.outputTokens)}</td><td>${obsPlain(costLabel(u.costUsd,u.costKnown))}</td></tr>`).join('');
  const srcRows=(state.analytics?.issueSources||[]).map(s=>`<tr><td>${obsSourceLabel(s.key)}</td><td>${s.runs}</td><td>${s.succeeded||0}</td><td>${s.completedIssues||0}</td><td>${formatTokens(s.tokens)}</td><td>${obsPlain(costLabel(s.costUsd,true))}</td></tr>`).join('');
  const versionRows=(state.analytics?.workflowVersions||[]).map(v=>`<tr><td>${escapeHTML(v.name||v.workflowId)}</td><td>v${v.version}</td><td>${v.runs}</td><td>${percent(v.successRate)}</td><td>${formatTokens(v.tokens)}</td><td>${obsPlain(costLabel(v.costUsd,true))}</td></tr>`).join('');
  const trendRows=obsTrendWindow(state.analytics?.trend||[]).slice().reverse().map(t=>`<tr><td>${escapeHTML(obsTrendLabel(t))}</td><td>${t.runs}</td><td>${t.issues||0}</td><td>${t.succeeded}</td><td>${t.failed||0}</td><td>${formatTokens(t.tokens)}</td><td>${obsPlain(costLabel(t.costUsd,true))}</td><td>${t.executionDurationMs?duration(t.executionDurationMs):'—'}</td></tr>`).join('');
  const projectRows=(state.obsCrossProject?.projectUsage||state.analytics?.projectUsage||[]).map(r=>`<tr><td>${escapeHTML(obsProjectName(r.projectId))}</td><td>${r.runs||0}</td><td>${r.uniqueIssues||0}</td><td>${formatTokens(r.tokens)}</td><td>${obsPlain(costLabel(r.costUsd,r.costKnown))}</td></tr>`).join('');
  const deliveryRows=(state.analytics?.deliveries||[]).slice(0,80).map(it=>`<tr><td>${obsSourceLabel(it.source)} ${escapeHTML(String(it.externalIid?('#'+it.externalIid):(it.externalId||it.issueId)))}</td><td>${escapeHTML(it.title||it.issueId)}</td><td>${escapeHTML(it.workflowName||it.workflowId)}</td><td>${it.sopSucceeded?'SOP 成功':(it.runStatus||'—')}</td><td>${escapeHTML(obsUserName(it.userId))}</td><td>${formatTokens(it.tokens)}</td><td>${obsPlain(costLabel(it.costUsd,it.costKnown))}</td><td>${it.delivered?'已交付':'未交付'}</td></tr>`).join('');
  const kpis=[
    ['运行',summary.totalRuns||0,`${summary.activeRuns||0} 进行中`],
    ['成功率',summary.totalRuns?percent(summary.successRate):'—',`${summary.succeededRuns||0} 成功 / ${summary.failedRuns||0} 失败`],
    ['一次通过',summary.totalRuns?percent(summary.firstPassRate):'—',`${summary.reworkedRuns||0} 次返工`],
    ['Token',formatTokens(summary.totalTokens),`入 ${formatTokens(summary.inputTokens)} / 出 ${formatTokens(summary.outputTokens)}`],
    ['成本',obsPlain(costLabel(summary.totalCostUsd,summary.costUnknownRuns===0)),summary.costUnknownRuns?`${summary.costUnknownRuns} 次未识别`:'已识别'],
    ['P95 执行',summary.executionP95DurationMs?duration(summary.executionP95DurationMs):'—',summary.executionAverageDurationMs?`平均 ${duration(summary.executionAverageDurationMs)}`:'—'],
    ['已交付',d.delivered||0,`${d.issues||0} 个有运行的单`],
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
${obsReportTable('交付明细',['单号','标题','SOP','流程','责任人','Tokens','成本','交付'],deliveryRows)}
${buildSkillhubReportHTML()}
<footer>由 AgentFlow 用量分析导出。日志与 Trace 请在运行记录中查看。</footer>
</div></body></html>`;
}
