// AI 定时任务「执行记录」看板：每次执行一条记录，失败可直接复制 Trace ID 去日志里检索。
let scheduleExecutions=[];
let scheduleExecProject=null;
let scheduleExecLoading=false;
let scheduleExecError='';
let scheduleExecRequest=0;
let scheduleExecStatusFilter='';
let scheduleExecJobFilter='';
const SCHEDULE_EXEC_STATUS={
  running:{label:'进行中',tone:'info',icon:''},
  success:{label:'成功',tone:'success',icon:'check'},
  failed:{label:'失败',tone:'error',icon:'alert'},
  waiting:{label:'待对齐基线',tone:'warn',icon:'clock'}
};
function scheduleExecStatusInfo(status){return SCHEDULE_EXEC_STATUS[status]||{label:status||'未知',tone:'neutral',icon:'circle'}}
function scheduleExecBadgeHTML(e){
  const info=scheduleExecStatusInfo(e?.status);
  const mark=info.icon?`<span class="hui-badge-glyph" aria-hidden="true"><svg class="agent-icon harness-icon" data-icon="${info.icon}" width="12" height="12" aria-hidden="true" focusable="false"></svg></span>`:'<span class="hui-lamp" aria-hidden="true"><i></i></span>';
  return `<span class="hui-badge" data-size="sm" data-tone="${info.tone}" data-status="${escapeHTML(e?.status||'')}">${mark}<span class="hui-badge-label">${escapeHTML(info.label)}</span></span>`;
}
function scheduleExecTriggerLabel(origin){return origin==='manual_run'?'手动运行':origin==='timer'?'定时触发':(origin||'定时触发')}
function scheduleExecDurationLabel(ms){
  const n=Number(ms)||0;
  if(n<=0)return '—';
  if(n<1000)return '<1 秒';
  const s=Math.round(n/1000);
  if(s<60)return `${s} 秒`;
  if(s<3600)return `${Math.floor(s/60)} 分 ${s%60} 秒`;
  return `${Math.floor(s/3600)} 小时 ${Math.floor(s%3600/60)} 分`;
}
function scheduleExecShortTrace(trace){return String(trace||'').slice(-8)}
function scheduleExecSummary(list){
  const done=list.filter(e=>e.status!=='running');
  const success=list.filter(e=>e.status==='success').length,failed=list.filter(e=>e.status==='failed').length;
  const timed=done.filter(e=>Number(e.durationMs)>0);
  return {
    total:list.length,success,failed,
    running:list.filter(e=>e.status==='running').length,
    rate:done.length?Math.round(success/done.length*100):null,
    avgMs:timed.length?timed.reduce((sum,e)=>sum+Number(e.durationMs),0)/timed.length:0
  };
}
function scheduleExecMatches(e,{status=scheduleExecStatusFilter,job=scheduleExecJobFilter}={}){
  return (!status||e.status===status)&&(!job||e.jobId===job);
}
async function refreshScheduleExecutions(){
  const projectId=state.projectFilter||'';
  const request=++scheduleExecRequest;
  scheduleExecProject=projectId;
  if(!projectId){scheduleExecutions=[];scheduleExecError='';scheduleExecLoading=false;renderScheduleExecutions();return scheduleExecutions}
  scheduleExecLoading=true;scheduleExecError='';
  renderScheduleExecutions();
  try{
    const list=await api(`/api/schedule-job-executions?projectId=${encodeURIComponent(projectId)}&limit=200`,{silent:true});
    if(request!==scheduleExecRequest)return scheduleExecutions;
    scheduleExecutions=Array.isArray(list)?list:[];
  }catch(err){
    if(request===scheduleExecRequest)scheduleExecError=err.message;
  }finally{
    if(request===scheduleExecRequest){scheduleExecLoading=false;renderScheduleExecutions()}
  }
  return scheduleExecutions;
}
function scheduleExecMetricsHTML(list){
  const s=scheduleExecSummary(list);
  const cell=(label,value,foot,cls='')=>`<div class="sched-exec-metric${cls}"><span>${label}</span><b>${value}</b><small>${foot}</small></div>`;
  return cell('执行次数',s.total,s.running?`${s.running} 次进行中`:`最近 ${s.total} 次`)
    +cell('成功率',s.rate===null?'—':`${s.rate}%`,`${s.success} 次成功`)
    +cell('失败',s.failed,s.failed?'可复制 Trace ID 排查':'近期无失败',s.failed?' is-danger':'')
    +cell('平均耗时',s.avgMs?scheduleExecDurationLabel(s.avgMs):'—','已结束的执行');
}
function scheduleExecRowHTML(e){
  const assistant=e.jobKind==='assistant';
  const started=knownTime(e.startedAt);
  const meta=[scheduleExecTriggerLabel(e.triggerOrigin),started?started.toLocaleString():'',started?relativeTime(e.startedAt):''].filter(Boolean).map(escapeHTML).join(' · ');
  const error=String(e.error||'').trim();
  const detail=error
    ?`<p class="sched-exec-error${e.status==='waiting'?' is-warn':''}" title="${escapeHTML(error)}">${escapeHTML(error)}</p>`
    :(assistant&&e.assistantMessage?`<p class="sched-exec-message" title="${escapeHTML(e.assistantMessage)}">${escapeHTML(e.assistantMessage)}</p>`:'');
  const runs=(e.runs||[]).filter(r=>r.runId);
  const runLinks=runs.length?`<div class="sched-exec-runs">${runs.map(r=>`<button type="button" class="sched-exec-run" data-open-exec-run="${escapeHTML(r.runId)}" title="打开运行 ${escapeHTML(r.runId)}"><svg class="agent-icon harness-icon" data-icon="workflow" width="12" height="12" aria-hidden="true" focusable="false"></svg>${escapeHTML(r.issueTitle||r.issueId||r.runId)}</button>`).join('')}</div>`:'';
  const stats=assistant?'':`<span class="sched-exec-stat">候选 ${Number(e.pulled)||0} · 启动 ${Number(e.dispatched)||0}</span>`;
  const trace=e.traceId?`<button type="button" class="button ghost small sched-exec-copy" data-copy-trace="${escapeHTML(e.traceId)}" aria-label="复制 Trace ID 与排障信息：${escapeHTML(e.traceId)}" title="复制 Trace ID 与排障信息（${escapeHTML(e.traceId)}），可直接发给 AI 助手排查"><svg class="agent-icon harness-icon" data-icon="copy" width="14" height="14" aria-hidden="true" focusable="false"></svg><span>Trace</span><code>${escapeHTML(scheduleExecShortTrace(e.traceId))}</code></button>`:'';
  return `<div class="sched-exec-row" data-status="${escapeHTML(e.status||'')}" data-exec-id="${escapeHTML(e.id)}"><div class="sched-exec-badge">${scheduleExecBadgeHTML(e)}</div><div class="sched-exec-main"><b><span class="schedule-kind-tag ${assistant?'is-assistant':'is-dispatch'}">${assistant?'AI':'触发'}</span>${escapeHTML(e.jobName||e.jobId)}</b><span class="sched-exec-meta">${meta}${stats?` · ${stats}`:''}</span>${detail}${runLinks}</div><span class="sched-exec-duration">${e.status==='running'?'—':escapeHTML(scheduleExecDurationLabel(e.durationMs))}</span><div class="sched-exec-actions">${trace}</div></div>`;
}
function scheduleExecJobOptions(){
  const jobs=new Map();
  for(const j of scheduleJobsInView(state.data.scheduleJobs,state.projectFilter))jobs.set(j.id,j.name||j.id);
  for(const e of scheduleExecutions)if(!jobs.has(e.jobId))jobs.set(e.jobId,e.jobName||e.jobId);
  if(scheduleExecJobFilter&&!jobs.has(scheduleExecJobFilter))scheduleExecJobFilter='';
  return [...jobs].sort((a,b)=>a[1].localeCompare(b[1],'zh'));
}
function renderScheduleExecutionCount(){
  const count=$('#aiScheduleExecCount');
  if(!count)return;
  const failed=scheduleExecProject===(state.projectFilter||'')?scheduleExecutions.filter(e=>e.status==='failed').length:0;
  count.textContent=failed?String(failed):'';
  count.hidden=!failed;
  count.classList.toggle('is-danger',failed>0);
  count.title=failed?`最近有 ${failed} 次执行失败`:'';
}
function renderScheduleExecutions(){
  renderScheduleExecutionCount();
  const panel=$('#aiScheduleExecPanel');
  if(!panel||panel.hidden)return;
  if(scheduleExecProject!==(state.projectFilter||'')&&!scheduleExecLoading){refreshScheduleExecutions();return}
  const jobSelect=$('#scheduleExecJob'),statusSelect=$('#scheduleExecStatus');
  const options=scheduleExecJobOptions();
  if(jobSelect){
    jobSelect.replaceChildren(new Option('全部任务',''),...options.map(([id,name])=>new Option(name,id)));
    jobSelect.value=scheduleExecJobFilter;
  }
  if(statusSelect)statusSelect.value=scheduleExecStatusFilter;
  const scoped=scheduleExecutions.filter(e=>scheduleExecMatches(e,{status:''}));
  $('#scheduleExecMetrics').innerHTML=scheduleExecMetricsHTML(scoped);
  const list=$('#scheduleExecList');
  const focused=document.activeElement;
  const focusTrace=list.contains(focused)?focused.dataset?.copyTrace:null;
  const rows=scheduleExecutions.filter(e=>scheduleExecMatches(e));
  list.setAttribute('aria-busy',String(scheduleExecLoading));
  let empty='';
  if(!state.projectFilter)empty='请先在顶栏选择项目，再查看执行记录。';
  else if(scheduleExecError)empty=`读取执行记录失败：${escapeHTML(scheduleExecError)} <button type="button" class="button ghost small" data-retry-exec>重试</button>`;
  else if(scheduleExecLoading&&!scheduleExecutions.length)empty='正在读取执行记录…';
  else if(!scheduleExecutions.length)empty='还没有执行记录。任务每次运行后，这里会记录结果、耗时和 Trace ID。';
  else if(!rows.length)empty='没有符合筛选条件的执行记录。';
  list.innerHTML=empty?`<div class="sync-empty">${empty}</div>`:rows.map(scheduleExecRowHTML).join('');
  list.querySelector('[data-retry-exec]')?.addEventListener('click',()=>refreshScheduleExecutions());
  bindScheduleTraceButtons(list);
  list.querySelectorAll('[data-open-exec-run]').forEach(b=>b.onclick=()=>openRunFromId(b.dataset.openExecRun,{forceRuns:true}));
  $('#scheduleExecFootnote').textContent=rows.length?`共 ${rows.length} 条 · 每个任务保留最近 200 次执行`:'';
  window.AgentFlowHarnessIcons?.hydrate(panel);
  if(focusTrace)[...list.querySelectorAll('[data-copy-trace]')].find(el=>el.dataset.copyTrace===focusTrace)?.focus();
}
// The copied notes start with the Trace ID on its own line (greppable as
// trace=<id> in scheduler logs) and carry enough context to paste into a chat.
function scheduleTraceSource(traceId){
  const exec=scheduleExecutions.find(e=>e.traceId===traceId);
  if(exec)return exec;
  const job=(state.data.scheduleJobs||[]).find(j=>j.lastTraceId===traceId);
  if(!job)return {traceId};
  return {traceId,jobId:job.id,jobName:job.name,jobKind:job.skipDispatch?'assistant':'trigger',projectId:job.projectId,status:job.lastStatus,error:job.lastError,assistantMessage:job.lastAssistantMessage,pulled:job.lastPulled,dispatched:job.lastDispatched,startedAt:job.lastRunAt};
}
function scheduleTraceDiagnostics(traceId){
  const e=scheduleTraceSource(traceId);
  const project=(state.data.projects||[]).find(p=>p.id===e.projectId);
  const started=knownTime(e.startedAt);
  const oneLine=v=>String(v??'').replace(/\s+/g,' ').trim();
  const rows=[
    ['Trace ID',traceId],
    ['任务',e.jobName||e.jobId?`${oneLine(e.jobName||e.jobId)}${e.jobId&&e.jobName?`（${e.jobId}）`:''}`:''],
    ['类型',e.jobKind==='assistant'?'AI 提示词任务':e.jobKind==='trigger'?'SOP 触发':''],
    ['项目',e.projectId?`${oneLine(project?.name||e.projectId)}${project?.name&&project.name!==e.projectId?`（${e.projectId}）`:''}`:''],
    ['触发方式',e.triggerOrigin?scheduleExecTriggerLabel(e.triggerOrigin):''],
    ['状态',e.status?scheduleExecStatusInfo(e.status).label:''],
    ['开始时间',started?`${started.toLocaleString()}（${started.toISOString()}）`:''],
    ['耗时',Number(e.durationMs)>0?scheduleExecDurationLabel(e.durationMs):''],
    ['候选 / 启动',e.jobKind==='trigger'?`${Number(e.pulled)||0} / ${Number(e.dispatched)||0}`:''],
    ['错误',oneLine(e.error).slice(0,1000)],
    ['AI 回复摘要',e.error?'':oneLine(e.assistantMessage).slice(0,300)],
    ['派发运行',(e.runs||[]).filter(r=>r.runId).map(r=>`${r.runId}${r.issueTitle||r.issueId?`（${oneLine(r.issueTitle||r.issueId)}）`:''}`).join('；')],
    ['页面',location.href],
    ['复制时间',new Date().toISOString()]
  ].filter(([,v])=>String(v||'').trim());
  return ['定时任务执行排障信息',...rows.map(([k,v])=>`${k}: ${v}`)].join('\n');
}
function copyScheduleTrace(traceId){
  if(!traceId)return;
  copyText(scheduleTraceDiagnostics(traceId),'已复制 Trace ID 与排障信息');
}
function bindScheduleTraceButtons(host){
  host?.querySelectorAll('[data-copy-trace]').forEach(b=>b.onclick=e=>{e.stopPropagation();copyScheduleTrace(b.dataset.copyTrace)});
}
// Job rows: a failed run gets a direct copy button; every row links to its history.
function scheduleJobTraceActionsHTML(job){
  const copy=job?.lastStatus==='failed'&&job.lastTraceId?`<button type="button" class="button ghost small sched-exec-copy" data-copy-trace="${escapeHTML(job.lastTraceId)}" aria-label="复制最近一次失败的 Trace ID 与排障信息" title="复制 Trace ID 与排障信息（${escapeHTML(job.lastTraceId)}），可直接发给 AI 助手排查"><svg class="agent-icon harness-icon" data-icon="copy" width="14" height="14" aria-hidden="true" focusable="false"></svg><span>Trace</span></button>`:'';
  return `${copy}<button type="button" class="button ghost small" data-schedule-history="${escapeHTML(job.id)}">执行记录</button>`;
}
function bindScheduleJobTraceActions(list){
  bindScheduleTraceButtons(list);
  list?.querySelectorAll('[data-schedule-history]').forEach(b=>b.onclick=()=>openScheduleExecutions(b.dataset.scheduleHistory));
  window.AgentFlowHarnessIcons?.hydrate(list);
}
function openScheduleExecutions(jobId=''){
  scheduleExecJobFilter=jobId||'';
  scheduleExecStatusFilter='';
  if($('#scheduleModal')?.classList.contains('open'))closeScheduleJobModal();
  if(state.currentView!=='aiSchedules'&&typeof switchView==='function')switchView('aiSchedules');
  selectAIScheduleTab('executions');
  $('#aiScheduleExecTab')?.focus();
}
function bindScheduleExecutionUI(){
  const status=$('#scheduleExecStatus'),job=$('#scheduleExecJob');
  if(status)status.onchange=()=>{scheduleExecStatusFilter=status.value;renderScheduleExecutions()};
  if(job)job.onchange=()=>{scheduleExecJobFilter=job.value;renderScheduleExecutions()};
}
