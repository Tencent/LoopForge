async function startRun(){const button=$('#confirmRun');try{const workflowId=$('#runWorkflowChoice').value||state.workflow?.id,projectId=currentProject()?.id||'';if(!workflowId)return toast('暂无可运行 SOP');button.disabled=true;button.textContent='正在启动…';if($('#flowView').classList.contains('active')&&workflowId===state.workflow.id&&!catalogForeign(state.workflow.projectId)&&(!catalogGeneric(state.workflow)||isPlatformAdmin()))await saveWorkflow();const run=await api('/api/runs',{method:'POST',body:JSON.stringify({workflowId,input:$('#runInput').value.trim(),projectId})});state.activeRun=run;state.selectedRun=run.id;state.selectedRunNode=null;if(run?.id){if(!state.data.runs)state.data.runs=[];const existing=state.data.runs.findIndex(x=>x.id===run.id);if(existing>=0)state.data.runs[existing]={...state.data.runs[existing],...run};else state.data.runs=sortRunsDescending([run,...state.data.runs]);}$('#runModal').classList.remove('open');$('#runInput').value='';switchView('runs');showRunDetail(run.id);toast(run.status==='queued'?'已进入项目排队，获得名额后自动启动':'流水线已开始，实时事件流已连接')}catch(e){toast(e.message)}finally{button.disabled=false;button.textContent='开始运行'}}
function stopLiveRun(){if(state.runStream){state.runStream.close();state.runStream=null}clearInterval(state.poll);state.poll=null}
function mergeSeqItems(prev, incoming, seqOf){
  const bySeq=new Map();
  for(const item of prev||[]){const seq=Number(seqOf(item)||0);if(seq)bySeq.set(seq,item)}
  for(const item of incoming||[]){const seq=Number(seqOf(item)||0);if(seq)bySeq.set(seq,item);else bySeq.set(Symbol(),item)}
  return [...bySeq.values()].sort((a,b)=>Number(seqOf(a)||0)-Number(seqOf(b)||0));
}
function mergeLiveRun(id,incoming,payload){
  const old=state.activeRun?.id===id?state.activeRun:null,byId=new Map((old?.telemetry||[]).map(e=>[e.id,e]));
  for(const e of payload?.telemetryDelta||[])byId.set(e.id,e);
  if(state.runLogs?.runId===id){
    const nodeId=state.runLogs.nodeId;
    const extra=(payload?.logDelta||[]).filter(l=>l&&(!nodeId||l.context?.nodeId===nodeId));
    if(extra.length){
      state.runLogs.items=mergeSeqItems(state.runLogs.items, extra, l=>l.sequence);
      state.runLogs.maxSeq=Math.max(state.runLogs.maxSeq||0,...extra.map(l=>Number(l.sequence||0)));
      state.runLogs.loaded=true;
    }
  }
  if(state.runTelemetry?.runId===id){
    const nodeId=state.runTelemetry.nodeId;
    const extra=(payload?.telemetryDelta||[]).filter(e=>e&&(!nodeId||e.nodeId===nodeId)&&matchTelemetryAdapter(e,state.runTelemetryAdapter||'live'));
    if(extra.length){
      state.runTelemetry.items=mergeSeqItems(state.runTelemetry.items, extra, e=>e.sequence);
      state.runTelemetry.maxSeq=Math.max(state.runTelemetry.maxSeq||0,...extra.map(e=>Number(e.sequence||0)));
      state.runTelemetry.loaded=true;
    }
  }
  const telemetry=[...byId.values()].sort((a,b)=>a.sequence-b.sequence);
  const nodes={...(old?.nodes||{})};
  for(const [k,n] of Object.entries(incoming.nodes||{})){
    const prev=nodes[k]||{};
    nodes[k]={...prev,...n,output:n.output||prev.output,error:n.error||prev.error,feedback:n.feedback||prev.feedback};
  }
  return {...(old||{}),...incoming,pauseRequested:Boolean(incoming.pauseRequested),cancelRequested:Boolean(incoming.cancelRequested),workflowSnapshot:(old?.workflowSnapshot?.id?old.workflowSnapshot:incoming.workflowSnapshot),telemetry,nodes};
}
function applyRunPayload(run,{list=true}={}){
  if(state.selectedRun&&run?.id!==state.selectedRun)return;
  state.activeRun=run;state.selectedRun=run.id;
  const i=(state.data.runs||[]).findIndex(x=>x.id===run.id);
  if(i>=0)state.data.runs[i]={...state.data.runs[i],...run,telemetry:undefined,logs:undefined};
  else if(run?.id){
    if(!state.data.runs)state.data.runs=[];
    state.data.runs=sortRunsDescending([{...run,telemetry:undefined,logs:undefined},...state.data.runs]);
  }
  if(list)renderRuns();
  if(shouldPatchRunLive(run))patchRunLive(run);else renderRunDetail(run);
}
function startLiveRun(id){
  stopLiveRun();
  const previous=state.activeRun?.id===id?state.activeRun:null;
  const after=previous?.telemetrySequence||state.runTelemetry?.maxSeq||0;
  const logsAfter=state.runLogs?.runId===id?(state.runLogs.maxSeq||0):0;
  const stream=new EventSource(`/api/runs/${encodeURIComponent(id)}/stream?after=${after}&logsAfter=${logsAfter}`);
  state.runStream=stream;
  stream.addEventListener('run',event=>{
    if(state.runStream!==stream)return;
    const payload=JSON.parse(event.data),run=mergeLiveRun(id,payload.run,payload);
    patchCachedRun(id,run);
    if(state.selectedRun===id){
      applyRunPayload(run,{list:false});
      const list=$('#runList')?.querySelector(`[data-id="${CSS.escape(id)}"]`);
      if(list){
        const st=list.querySelector('.status');
        if(st){st.className=`status ${runDisplayStatusClass(run)}`;st.textContent=runDisplayStatus(run)}
        const p=runProgress(run),bar=list.querySelector('.run-progress i');
        if(bar)bar.style.width=p.pct+'%';
      }
    }
    if(state.drawerOpen&&state.issueDetailData?.run?.id===id){
      state.issueDetailData.run=run;
      if(state.issueDetailTab!=='clarify'||!issueClarifying(state.issueDetailData.issue?.id))renderIssueDetailTab();
    }
    if(['succeeded','failed','cancelled'].includes(run.status)){stopLiveRun();refreshWorkflowMetrics();renderRuns()}
  });
  stream.onerror=()=>{stream.close();if(state.runStream!==stream)return;state.runStream=null;startPollingFallback(id)}
}
async function refreshRunPagesDelta(id){
  const pg=state.activeRun?.id===id?runPageTarget(state.activeRun,state.selectedRunNode):{runId:id,nodeId:state.selectedRunNode||''};
  id=pg.runId;
  const nodeId=pg.nodeId,jobs=[];
  if(state.runLogs?.runId===id&&state.runLogs.nodeId===nodeId&&state.runLogs.loaded){
    const params=new URLSearchParams({limit:'100',after:String(state.runLogs.maxSeq||0)});if(nodeId)params.set('nodeId',nodeId);
    jobs.push(api(`/api/runs/${encodeURIComponent(id)}/logs?${params}`,{silent:true}).then(data=>{const incoming=data.logs||[];if(!incoming.length)return;state.runLogs.items=mergeSeqItems(state.runLogs.items,incoming,l=>l.sequence);state.runLogs.maxSeq=Math.max(state.runLogs.maxSeq||0,...incoming.map(l=>Number(l.sequence||0)))}));
  }
  const adapter=state.runTelemetryAdapter||'live';
  if(state.runTelemetry?.runId===id&&state.runTelemetry.nodeId===nodeId&&state.runTelemetry.adapter===adapter&&state.runTelemetry.loaded){
    const params=new URLSearchParams({limit:'100',adapter,after:String(state.runTelemetry.maxSeq||0)});if(nodeId)params.set('nodeId',nodeId);
    jobs.push(api(`/api/runs/${encodeURIComponent(id)}/telemetry?${params}`,{silent:true}).then(data=>{const incoming=data.events||[];if(!incoming.length)return;state.runTelemetry.items=mergeSeqItems(state.runTelemetry.items,incoming,e=>e.sequence);state.runTelemetry.maxSeq=Math.max(state.runTelemetry.maxSeq||0,...incoming.map(e=>Number(e.sequence||0)))}));
  }
  if(jobs.length){await Promise.all(jobs);if(state.activeRun&&runPageTarget(state.activeRun,state.selectedRunNode).runId===id)patchRunLive(state.activeRun)}
}
function startPollingFallback(id){
  clearInterval(state.poll);
  let busy=false;
  const relevant=()=>state.selectedRun===id||(state.drawerOpen&&state.issueDetailData?.run?.id===id);
  const stop=()=>{clearInterval(timer);if(state.poll===timer)state.poll=null};
  const tick=async()=>{
    if(state.poll!==timer||busy)return;
    if(!relevant()){stop();return}
    busy=true;
    try{
      const run=await api(`/api/runs/${encodeURIComponent(id)}`,{silent:true});
      if(state.poll!==timer||!relevant())return;
      patchCachedRun(id,run);
      if(state.selectedRun===id){applyRunPayload(run,{list:false});await refreshRunPagesDelta(id)}
      if(state.drawerOpen&&state.issueDetailData?.run?.id===id)renderIssueDetailTab();
      if(['succeeded','failed','cancelled'].includes(run.status)){stop();renderRuns()}
    }catch{}finally{busy=false}
  };
  const timer=setInterval(tick,4000);state.poll=timer;tick();
}
function sortRunsDescending(runs){
  if(!Array.isArray(runs))return [];
  return [...runs].sort((a,b)=>{
    const tb=new Date(b?.createdAt||0).getTime();
    const ta=new Date(a?.createdAt||0).getTime();
    if(tb!==ta)return tb-ta;
    return String(b?.id||'').localeCompare(String(a?.id||''));
  });
}
let queuedRunRefreshTimer=null;
function queuedRunListVisible(){return !document.hidden&&activeViewName()==='runs'&&$('#runsListSection')?.hidden===false}
function queuedRunRefreshNeeded(){return queuedRunListVisible()&&scopedRuns(state.data.runs||[]).some(r=>r.status==='queued')}
function stopQueuedRunRefresh(){clearInterval(queuedRunRefreshTimer);queuedRunRefreshTimer=null}
function syncQueuedRunRefresh(){
  if(!queuedRunRefreshNeeded()){stopQueuedRunRefresh();return}
  if(queuedRunRefreshTimer!==null)return;
  queuedRunRefreshTimer=setInterval(()=>{if(!queuedRunRefreshNeeded()){stopQueuedRunRefresh();return}refreshRuns(true,true)},4000);
}
document.addEventListener('visibilitychange',()=>{syncQueuedRunRefresh();if(queuedRunRefreshNeeded())refreshRuns(true,true)});
let refreshRunsPending=null;
async function refreshRuns(background=false,runsView=false){if(refreshRunsPending)return refreshRunsPending;refreshRunsPending=(async()=>{try{const opt=background?{silent:true}:{};const runs=await api('/api/runs',opt);const list=sortRunsDescending(runs);state.data.runs=list;renderRuns();if(runsView){const [tasks,issues]=await Promise.all([api('/api/tasks',{silent:true}),api('/api/issues',{silent:true})]);state.data.tasks=tasks;state.data.issues=issues;if(activeViewName()==='runs')renderRuns()}else if(!background){const [tasks,issues]=await Promise.all([api('/api/tasks',opt),api('/api/issues',opt)]);state.data.tasks=tasks;state.data.issues=issues;renderRuns()}if(!state.selectedRun)return;const summary=list.find(x=>x.id===state.selectedRun);if(!summary){if($('#runsDetailSection')?.hidden===false)return;state.selectedRun=null;state.activeRun=null;renderRunDetail(null);return}if(background)return;if($('#runsDetailSection')?.hidden===false)return;selectRun(state.selectedRun,true)}catch(e){if(!background)toast(e.message)}finally{refreshRunsPending=null}})();return refreshRunsPending}
function runProgress(r){const nodes=Object.values(r.nodes||{}).filter(Boolean),done=nodes.filter(n=>['succeeded','failed','cancelled'].includes(n.status)).length,total=Math.max(nodes.length,done);return {done:Math.min(done,total),total,pct:total?Math.round(Math.min(done,total)*100/total):0}}
function workflowStages(workflow){const nodes=workflow?.nodes||[],edges=workflow?.edges||[],byId=new Map(nodes.map(n=>[n.id,n])),indegree=new Map(nodes.map(n=>[n.id,0]));for(const edge of edges)if(indegree.has(edge.target)&&byId.has(edge.source))indegree.set(edge.target,indegree.get(edge.target)+1);let ready=nodes.filter(n=>indegree.get(n.id)===0).map(n=>n.id),seen=new Set(),stages=[];while(ready.length){const ids=ready,stage=ids.map(id=>byId.get(id)).filter(Boolean);stages.push(stage);ready=[];for(const id of ids){seen.add(id);for(const edge of edges.filter(e=>e.source===id)){if(!indegree.has(edge.target))continue;indegree.set(edge.target,indegree.get(edge.target)-1);if(indegree.get(edge.target)===0)ready.push(edge.target)}}}const remaining=nodes.filter(n=>!seen.has(n.id));if(remaining.length)stages.push(remaining);return stages}
function runFinishedAt(r){
  let end=0;
  for(const n of Object.values(r.nodes||{})){
    const t=new Date(n?.finishedAt||0).getTime();
    if(t>end)end=t;
  }
  if(['succeeded','failed','cancelled'].includes(r.status)) return end||new Date(r.updatedAt||0).getTime();
  return Date.now();
}
function runTiming(r){
  const t=r?.timing;
  if(!t){const wall=Math.max(0,runFinishedAt(r)-new Date(r.createdAt));return {execution:wall,wall,waiting:0}}
  const drift=Math.max(0,Date.now()-new Date(t.asOf||Date.now()).getTime());
  const open=!['succeeded','failed','cancelled'].includes(r.status);
  const execution=Number(t.executionMs||0)+(t.executing?drift:0);
  const wall=Math.max(execution,Number(t.wallMs||0)+(open?drift:0));
  return {execution,wall,waiting:wall-execution};
}
function runElapsed(r){return duration(runTiming(r).execution)}
function runTimingTitle(r){const t=runTiming(r);return `执行 ${duration(t.execution)}：节点实际运行时间，含每次重试\n端到端 ${duration(t.wall)}：从创建到${['succeeded','failed','cancelled'].includes(r.status)?'结束':'现在'}\n等待 ${duration(t.waiting)}：失败待处理、暂停、人工确认、排队等`}
function runWallNote(r){const t=runTiming(r);return t.waiting>=60000?` · 端到端 ${duration(t.wall)}（等待 ${duration(t.waiting)}）`:''}
function relativeTime(value){const seconds=Math.max(0,Math.floor((Date.now()-new Date(value))/1000));if(seconds<60)return '刚刚';if(seconds<3600)return `${Math.floor(seconds/60)} 分钟前`;if(seconds<86400)return `${Math.floor(seconds/3600)} 小时前`;return `${Math.floor(seconds/86400)} 天前`}
function runContext(r){
  const issueId=r?.executionContext?.issueId||(state.data.tasks||[]).find(t=>t.runId===r?.id)?.issueId;
  const task=(state.data.tasks||[]).find(t=>t.runId===r?.id)||(issueId?(state.data.tasks||[]).find(t=>t.issueId===issueId):null)||null;
  const issue=issueId&&(state.data.issues||[]).find(i=>i.id===issueId);
  const project=issue&&(state.data.projects||[]).find(p=>p.id===issue.projectId);
  return {task,issue,project}
}
function taskTriggerInfo(task){
  if(!task)return{label:'直接运行',kind:'manual',title:'从流程设计器或 API 直接启动，未关联需求'};
  if(task.dispatchMode==='scheduled'||task.scheduleJobId){
    const job=(state.data.scheduleJobs||[]).find(j=>j.id===task.scheduleJobId),name=job?.name||task.scheduleJobId||'定时任务';
    const action=task.triggerOrigin==='manual_run'?'立即运行':task.triggerOrigin==='timer'?'定时触发':'定时任务';
    return{label:`${action} · ${name}`,kind:'scheduled'};
  }
  if(task.dispatchMode==='rerun')return{label:'手动重跑',kind:'manual'};
  return{label:'手动触发',kind:'manual'};
}
function taskTriggerBadge(task){const info=taskTriggerInfo(task);return `<span class="run-trigger ${info.kind}" title="${escapeHTML(info.title||'触发来源')}">${escapeHTML(info.label)}</span>`}
function renderRunOverview(){
  const live=liveRuns(),runs=scopedRuns(state.data.runs||[]),active=live.filter(r=>r.status==='running'&&!r.pauseRequested).length,paused=runs.filter(r=>r.pauseRequested||r.status==='paused').length,waiting=live.filter(r=>r.status==='waiting'&&!r.pauseRequested).length,queued=runs.filter(r=>r.status==='queued').length,failed=runs.filter(r=>r.status==='failed').length,cancelled=runs.filter(r=>r.status==='cancelled').length,finished=runs.filter(r=>r.status==='succeeded'),successBase=finished.length+failed,rate=successBase?Math.round(finished.length*100/successBase):0,totalTokens=runs.reduce((n,r)=>n+Number(r.usage?.totalTokens||0),0);
  $('#runOverview').innerHTML=`<div class="run-kpi"><span>进行中</span><b>${active}</b></div><div class="run-kpi"><span>排队中</span><b>${queued}</b></div><div class="run-kpi ${paused?'warning':''}"><span>已暂停</span><b>${paused}</b></div><div class="run-kpi"><span>待确认</span><b>${waiting}</b></div><div class="run-kpi"><span>已完成</span><b>${finished.length}</b></div><div class="run-kpi ${failed?'danger':''}"><span>失败</span><b>${failed}</b></div><div class="run-kpi"><span>已取消</span><b>${cancelled}</b></div><div class="run-kpi"><span>成功率</span><b>${rate}%</b><small>${successBase} 次已结束</small></div><div class="run-kpi"><span>累计 Token</span><b>${formatTokens(totalTokens)}</b><small>${runs.length} 次</small></div>`;
  $('#runsLiveState').classList.toggle('active',active+waiting+queued>0)
}
function renderRuns(){
  renderRunOverview();const workflows=catalogWorkflows();$('#runWorkflowFilter').innerHTML='<option value="">全部 SOP</option>'+workflows.map(w=>`<option value="${escapeHTML(w.id)}" ${state.runWorkflowFilter===w.id?'selected':''}>${escapeHTML(w.name)}</option>`).join('');
  let runs=sortRunsDescending(scopedRuns(state.data.runs||[]));
  if(state.runFilter==='active')runs=runs.filter(r=>r.status==='running'&&!r.pauseRequested);
  else if(state.runFilter==='paused')runs=runs.filter(r=>r.pauseRequested||r.status==='paused');
  else if(state.runFilter==='waiting')runs=runs.filter(r=>r.status==='waiting'&&!r.pauseRequested);
  else if(state.runFilter==='succeeded')runs=runs.filter(r=>r.status==='succeeded');
  else if(state.runFilter==='failed')runs=runs.filter(r=>r.status==='failed');
  else if(state.runFilter==='cancelled')runs=runs.filter(r=>r.status==='cancelled');
  else if(state.runFilter!=='all')runs=runs.filter(r=>r.status===state.runFilter);
  if(state.runWorkflowFilter)runs=runs.filter(r=>r.workflowId===state.runWorkflowFilter);
  if(state.runQuery)runs=runs.filter(r=>{const ctx=runContext(r);return `${r.id} ${r.input} ${r.workflowSnapshot?.name||''} ${ctx.issue?.title||''}`.toLowerCase().includes(state.runQuery)});
  $('#runListCount')&&($('#runListCount').textContent=`${runs.length} 条`);const list=$('#runList');
  const pagedRuns=paginateList('runs', runs);
  list.innerHTML=pagedRuns.items.map(r=>{
    const p=runProgress(r),ctx=runContext(r),sop=r.workflowSnapshot?.name||workflows.find(w=>w.id===r.workflowId)?.name||r.workflowId,title=ctx.issue?.title||sop;
    const isPaused=Boolean(r.pauseRequested||r.status==='paused');
    const isDeletable=r.status!=='queued'&&(!['running','waiting'].includes(r.status)||isPaused);
    return `<div class="run-card ${state.selectedRun===r.id?'active':''}" data-id="${escapeHTML(r.id)}"><div class="run-card-top"><span class="run-workflow-icon">${escapeHTML((ctx.project?.key||sop)).slice(0,1)}</span><div class="run-card-title"><h3>${escapeHTML(title)}${r.workflowSnapshot?.generic?' <span class="sop-status generic">通用</span>':''}</h3><small>${ctx.issue?`${escapeHTML(ctx.project?.key||'ISS')}-${ctx.issue.number} · `:''}${escapeHTML(sop)} · ${relativeTime(r.createdAt)} ${taskTriggerBadge(ctx.task)}</small></div><div class="run-card-right"><span class="run-meta-step">${p.done}/${p.total} 步</span><span class="run-meta-time" title="${escapeHTML(runTimingTitle(r))}">${runElapsed(r)}</span><span class="status ${runDisplayStatusClass(r)}">${runDisplayStatus(r)}</span><button class="button ghost run-view-btn" data-view-run="${escapeHTML(r.id)}">查看</button>${isDeletable?`<button class="button ghost run-del-btn" data-delete-run="${escapeHTML(r.id)}" title="删除该运行记录">删除</button>`:'<span class="run-del-btn run-del-placeholder" aria-hidden="true">删除</span>'}</div></div><div class="run-progress"><i style="width:${p.pct}%"></i></div></div>`
  }).join('')||'<div class="run-list-empty"><span>⌕</span><b>没有匹配的运行</b><p>可到需求列表查看对应需求的执行结果。</p></div>';
  list.insertAdjacentHTML('beforeend', pagerHTML('runs', pagedRuns));
  list.querySelectorAll('.run-card').forEach(el=>el.onclick=(e)=>{if(e.target.closest('.run-view-btn,.run-del-btn,.pager'))return;showRunDetail(el.dataset.id)});
  list.querySelectorAll('.run-view-btn').forEach(btn=>btn.onclick=(e)=>{e.stopPropagation();showRunDetail(btn.dataset.viewRun)});
  list.querySelectorAll('.run-del-btn').forEach(btn=>btn.onclick=(e)=>{e.stopPropagation();deleteRun(btn.dataset.deleteRun)});
  bindPager(list);
  syncQueuedRunRefresh();
}
function showRunDetail(id){
  if(!id)return;
  stopQueuedRunRefresh();
  if(state.selectedRun!==id){stopLiveRun();stopPreviewPoll();state.runWorkspace=null}
  state.selectedRun=id;
  syncRunHash(id);
  $('#runsView')?.classList.add('detail-open');
  document.querySelector('#runsView .run-toolbar')?.setAttribute('hidden','');
  $('#runsListSection').hidden=true;
  $('#runsDetailSection').hidden=false;
  const summary=(state.data.runs||[]).find(x=>x.id===id);
  if(summary){
    state.activeRun=summary;
    renderRunDetail(summary);
  }
  api(`/api/runs/${encodeURIComponent(id)}`,{silent:!!summary}).then(r=>{
    if(state.selectedRun!==id)return;
    state.activeRun=r;
    if(shouldPatchRunLive(r))patchRunLive(r);else renderRunDetail(r);
    if(['queued','running','waiting'].includes(r.status))startLiveRun(r.id);else stopLiveRun();
  }).catch(e=>toast(e.message,'err'));
}
function showRunList(){
  stopLiveRun();
  stopPreviewPoll();
  state.selectedRun=null;
  syncRunHash('');
  $('#runsView')?.classList.remove('detail-open');
  document.querySelector('#runsView .run-toolbar')?.removeAttribute('hidden');
  $('#runsListSection').hidden=false;
  $('#runsDetailSection').hidden=true;
  renderRuns();
}
function selectRun(id,keepPages=false){
  if(!id)return;
  if(state.selectedRun!==id)stopPreviewPoll();
  if(!keepPages&&state.selectedRun!==id){state.runLogs=null;state.runTelemetry=null;state.runWorkspace=null;state.selectedRunNode=null}
  state.selectedRun=id;
  const summary=(state.data.runs||[]).find(x=>x.id===id);
  if(summary){
    state.activeRun=summary;
    renderRuns();
    renderRunDetail(summary);
    if(['queued','running','waiting'].includes(summary.status))startLiveRun(id);else stopLiveRun();
  }
  api(`/api/runs/${encodeURIComponent(id)}`,{silent:!!summary}).then(r=>{
    if(state.selectedRun!==id)return;
    state.activeRun=r;
    renderRuns();
    if(shouldPatchRunLive(r))patchRunLive(r);else renderRunDetail(r);
    if(['queued','running','waiting'].includes(r.status))startLiveRun(r.id);else stopLiveRun();
  }).catch(e=>toast(e.message,'err'));
}
function runStatus(s){return ({queued:'排队中',running:'运行中',waiting:'待确认',paused:'已暂停',succeeded:'已完成',failed:'失败',cancelled:'已取消'}[s]||s)}
function runControlSignature(r){return `${r?.status||''}:${Boolean(r?.pauseRequested)}:${Boolean(r?.cancelRequested)}:${r?.admissionState||''}`}
function runControlToast(run,message){return run?.status==='queued'?'已进入项目排队，获得名额后自动启动':run?.status==='running'?message:`当前状态：${runDisplayStatus(run)}`}
async function syncRunControlState(runId){
  const run=await api(`/api/runs/${encodeURIComponent(runId)}`,{silent:true});
  patchCachedRun(runId,run);
  if(!(state.data.runs||[]).some(r=>r.id===runId))state.data.runs=sortRunsDescending([run,...(state.data.runs||[])]);
  const relevant=state.selectedRun===runId||(state.drawerOpen&&state.issueDetailData?.run?.id===runId);
  if(relevant){if(['queued','running','waiting'].includes(run.status))startLiveRun(runId);else stopLiveRun()}
  paintRunControl(runId);
  return run;
}
function runDisplayStatus(r){return (r?.pauseRequested||r?.status==='paused')?'已暂停':runStatus(r?.status)}
function runDisplayStatusClass(r){return (r?.pauseRequested||r?.status==='paused')?'paused':(r?.status||'')}
function formatTokens(n){n=Number(n||0);if(n>=1000000)return `${(n/1000000).toFixed(n<10000000?1:0)}M`;return n>999?`${(n/1000).toFixed(n>9999?0:1)}k`:String(n)}
function compactTelemetry(events){const out=[];for(const e of events||[]){const last=out[out.length-1];if(last&&last.nodeId===e.nodeId&&last.category===e.category&&last.name===e.name&&['reasoning','output'].includes(e.category)){last.message=(last.message||'')+(e.message||'');last.sequence=e.sequence;last.timestamp=e.timestamp}else out.push({...e})}return out}
function toolInputHint(input){
  if(input==null)return '';
  if(typeof input==='string')return input.replace(/\s+/g,' ').slice(0,160);
  if(typeof input!=='object')return '';
  const v=input.command||input.cmd||input.path||input.file_path||input.filePath||input.pattern||input.glob||input.query||input.url||input.target||'';
  return String(v).replace(/\s+/g,' ').slice(0,160);
}
function formatLogBody(l){
  let body=String(l.body||'').replace(/\r\n/g,'\n').trim();
  if(!body)return '';
  if((l.level||'')!=='error'&&isToolCompletionNoise(body)){
    const hint=toolInputHint(l.attributes?.input);
    const name=(body.split(/\s+/)[0]||'').trim();
    return hint&&/^[A-Za-z][\w./-]{0,48}$/.test(name)?`${name} ${hint}`.slice(0,180):'';
  }
  const hint=toolInputHint(l.attributes?.input);
  const first=(body.split('\n').find(s=>s.trim())||'').trim();
  const looksCode=body.split('\n').length>2||/t\.Fatalf|func |package | := |import \(/.test(body);
  if(/^[A-Za-z][\w./-]{0,48}$/.test(first)&&hint)return `${first} ${hint}`.slice(0,180);
  if(looksCode){
    if(hint)return `${(first.split(/\s+/)[0]||'tool')} ${hint}`.slice(0,180);
    if(/^(Bash|Grep|Read|Write|Edit|Glob|Shell)\b/i.test(first))return first.replace(/\s*结果.*$/,'').slice(0,180);
    return '';
  }
  return first.replace(/\s+/g,' ').slice(0,180);
}
function isToolCompletionNoise(body){
  const s=String(body||'').replace(/\s+/g,' ').trim();
  if(!s)return false;
  if(/^(工具完成|tool result|工具返回结果|工具结果)(\s|$)/i.test(s))return true;
  return /^[A-Za-z][\w./-]{0,48} (完成([:：].*)?|结果.*)$/.test(s);
}
function visibleWorkerLogs(items){return (items||[]).filter(l=>formatLogBody(l))}
function logLineHTML(l){
  const body=formatLogBody(l);
  if(!body)return '';
  const repository=l.attributes?.repositoryName||l.attributes?.repositoryDir||l.attributes?.repositoryId||'';
  return `<div class="worker-log-line ${escapeHTML(l.level||'info')}"><time>${new Date(l.timestamp).toLocaleTimeString()}</time><span>${escapeHTML(l.stream||'worker')}</span><b>${escapeHTML(l.context?.nodeId||'run')}${repository?` · ${escapeHTML(repository)}`:''}</b><pre>${escapeHTML(body)}</pre></div>`;
}
function isHookAdapter(adapter){return adapter==='agent-hooks-v1'||adapter==='codebuddy-hooks-v1'}
function hookTraceDetails(e){const a=e.attributes||{},facts=[["ID",a.hookId],["事件",a.hook],["仓库",a.repositoryName||a.repositoryDir||a.repositoryId],["阶段",a.executionPhase],["语言",a.language],["Matcher",a.matcher],["耗时",a.durationMs!=null?`${a.durationMs} ms`:""],["退出码",a.exitCode!=null?String(a.exitCode):""]].filter(x=>x[1]!==''&&x[1]!=null);return `${facts.length?`<dl class="hook-trace-facts">${facts.map(([k,v])=>`<div><dt>${k}</dt><dd>${escapeHTML(v)}</dd></div>`).join('')}</dl>`:''}${a.stdout?`<details class="hook-trace-output"><summary>stdout</summary><pre>${escapeHTML(a.stdout)}</pre></details>`:''}${a.stderr?`<details class="hook-trace-output error"><summary>stderr</summary><pre>${escapeHTML(a.stderr)}</pre></details>`:''}`}
function telemetryItemHTML(e){const hook=isHookAdapter(e.source?.adapter),tag=hook?'Hook':e.source?.adapter==='codebuddy-otel-v1'?'OTEL':'',title=hook?(e.attributes?.hookName||e.attributes?.hookId||e.name):e.name,repository=e.attributes?.repositoryName||e.attributes?.repositoryDir||e.attributes?.repositoryId||'';return `<article class="telemetry-event ${e.category}"><time>${new Date(e.timestamp).toLocaleTimeString()}</time><span class="telemetry-category">${telemetryLabel(e)}${tag?`<em class="adapter-tag">${tag}</em>`:''}</span><div><b>${escapeHTML(title)}</b><p>${escapeHTML(e.message||'')}</p>${hook?hookTraceDetails(e):''}<small>${escapeHTML(e.nodeId||'run')}${repository?` · ${escapeHTML(repository)}`:''}${hook&&e.source?.provider?` · ${escapeHTML(e.source.provider)}`:''}${e.model?` · ${escapeHTML(e.model)}`:''}${e.toolCallId?` · ${escapeHTML(e.toolCallId)}`:''}</small></div></article>`}
async function loadRunLogs(runId,nodeId,more=false){
  if(!more&&state.runLogs?.runId===runId&&state.runLogs?.nodeId===nodeId&&state.runLogs.loading)return;
  const params=new URLSearchParams({limit:'100'});
  if(nodeId)params.set('nodeId',nodeId);
  if(more&&state.runLogs?.runId===runId&&state.runLogs.nodeId===nodeId&&state.runLogs.nextBefore)params.set('before',String(state.runLogs.nextBefore));
  const prev=(!more||state.runLogs?.runId!==runId||state.runLogs?.nodeId!==nodeId)?(state.runLogs?.runId===runId&&state.runLogs?.nodeId===nodeId?state.runLogs.items:[]):state.runLogs.items;
  if(!more){state.runLogs={runId,nodeId,items:prev||[],loaded:!!(prev&&prev.length),loading:true,hasMore:!!state.runLogs?.hasMore,nextBefore:state.runLogs?.nextBefore||0,maxSeq:state.runLogs?.maxSeq||0}}
  try{
    const data=await api(`/api/runs/${encodeURIComponent(runId)}/logs?${params}`,{silent:true});
    const incoming=data.logs||[];
    const items=more?mergeSeqItems(incoming, prev||[], l=>l.sequence):mergeSeqItems(prev||[], incoming, l=>l.sequence);
    const seqs=items.map(l=>Number(l.sequence||0));
    state.runLogs={runId,nodeId,items,loaded:true,loading:false,hasMore:!!data.hasMore||(!more&&incoming.length>=100),nextBefore:data.nextBefore||(seqs.length?Math.min(...seqs.filter(Boolean)):0),maxSeq:seqs.length?Math.max(...seqs):0};
    if(state.activeRun&&runPageTarget(state.activeRun,state.selectedRunNode).runId===runId){const host=$('#runDetail');if(host?.dataset.runId===state.activeRun.id)patchRunLive(state.activeRun);else renderRunDetail(state.activeRun)}
  }catch(e){if(state.runLogs?.runId===runId)state.runLogs.loading=false;toast(e.message,'err')}
}
async function loadRunTelemetry(runId,nodeId,more=false){
  const adapter=state.runTelemetryAdapter||'live';
  if(!more&&state.runTelemetry?.runId===runId&&state.runTelemetry?.nodeId===nodeId&&state.runTelemetry?.adapter===adapter&&state.runTelemetry.loading)return;
  const params=new URLSearchParams({limit:'100',adapter});
  if(nodeId)params.set('nodeId',nodeId);
  if(more&&state.runTelemetry?.runId===runId&&state.runTelemetry.nodeId===nodeId&&state.runTelemetry.adapter===adapter&&state.runTelemetry.nextBefore)params.set('before',String(state.runTelemetry.nextBefore));
  const same=state.runTelemetry?.runId===runId&&state.runTelemetry?.nodeId===nodeId&&state.runTelemetry?.adapter===adapter;
  const prev=(!more||!same)?(same?state.runTelemetry.items:[]):state.runTelemetry.items;
  if(!more){state.runTelemetry={runId,nodeId,adapter,items:prev||[],loaded:!!(prev&&prev.length),loading:true,hasMore:!!state.runTelemetry?.hasMore,nextBefore:state.runTelemetry?.nextBefore||0,maxSeq:state.runTelemetry?.maxSeq||0}}
  try{
    const data=await api(`/api/runs/${encodeURIComponent(runId)}/telemetry?${params}`,{silent:true});
    const incoming=data.events||[];
    const items=more?mergeSeqItems(incoming, prev||[], e=>e.sequence):mergeSeqItems(prev||[], incoming, e=>e.sequence);
    const seqs=items.map(e=>Number(e.sequence||0));
    state.runTelemetry={runId,nodeId,adapter,items,loaded:true,loading:false,hasMore:!!data.hasMore||(!more&&incoming.length>=100),nextBefore:data.nextBefore||(seqs.length?Math.min(...seqs.filter(Boolean)):0),maxSeq:seqs.length?Math.max(...seqs):0};
    if(state.activeRun&&runPageTarget(state.activeRun,state.selectedRunNode).runId===runId){const host=$('#runDetail');if(host?.dataset.runId===state.activeRun.id)patchRunLive(state.activeRun);else renderRunDetail(state.activeRun)}
  }catch(e){if(state.runTelemetry?.runId===runId)state.runTelemetry.loading=false;toast(e.message,'err')}
}
function ensureRunPages(runId,nodeId){
  if(!runId)return;
  if(!(state.runLogs?.runId===runId&&state.runLogs.nodeId===nodeId&&(state.runLogs.loaded||state.runLogs.loading)))loadRunLogs(runId,nodeId,false);
  const adapter=state.runTelemetryAdapter||'live';
  if(!(state.runTelemetry?.runId===runId&&state.runTelemetry.nodeId===nodeId&&state.runTelemetry.adapter===adapter&&(state.runTelemetry.loaded||state.runTelemetry.loading)))loadRunTelemetry(runId,nodeId,false);
}
function telemetryLabel(e){return ({lifecycle:'生命周期',hook:'Hook 执行',script:'脚本',model:'模型',reasoning:'内部推理',output:'输出',tool:'工具调用',usage:'Token 用量',process:'进程',verification:'验证',sandbox:'沙箱',artifact:'产物'}[e.category]||e.category)}
function matchTelemetryAdapter(e,view){
  const a=e?.source?.adapter||'';
  if(!view||view==='all')return true;
  if(view==='hooks')return isHookAdapter(a);
  if(view==='otel')return a==='codebuddy-otel-v1';
  if(view==='live')return !isHookAdapter(a)&&a!=='codebuddy-otel-v1';
  return a===view;
}
function telemetrySectionTitle(adapter){return ({live:'节点实时轨迹',hooks:'Hook 轨迹',otel:'OTEL 轨迹'})[adapter]||'节点轨迹'}
function telemetryEmptyHint(adapter){return ({live:'该节点没有实时轨迹',hooks:'还没有 Hook 事件。角色级 Hook 在 Claude Code、Codex 或 CodeBuddy 触发后会写到这里。',otel:'还没有 OTEL span。需要 CodeBuddy 打开原生 OTLP 导出。'})[adapter]||'该节点没有轨迹'}
function telemetrySourceTabs(adapter,attr){
  return `<div class="trace-source-tabs">${[['live','实时'],['hooks','Hook'],['otel','OTEL']].map(([id,label])=>`<button type="button" class="${adapter===id?'active':''}" ${attr}="${id}">${label}</button>`).join('')}</div>`;
}
function captureScrollBox(el){if(!el)return null;return {top:el.scrollTop,left:el.scrollLeft,stick:(el.scrollHeight-el.clientHeight-el.scrollTop)<72}}
function restoreScrollBox(el,saved,fallbackBottom){if(!el)return;if(saved){el.scrollTop=saved.stick?el.scrollHeight:saved.top;if(saved.left!=null)el.scrollLeft=saved.left;return}if(fallbackBottom)el.scrollTop=el.scrollHeight}
function resetRunScroll(runId){state.uiScroll={runId,nodeId:'',graphLeft:0,feedTop:0,telTop:0,mainTop:0,feedStick:true,telStick:true}}
function bindRunScrollLocks(host,runId,nodeId){
  if(!state.uiScroll||state.uiScroll.runId!==runId)resetRunScroll(runId);
  if(state.uiScroll.nodeId!==nodeId){state.uiScroll.nodeId=nodeId;state.uiScroll.feedTop=0;state.uiScroll.telTop=0;state.uiScroll.feedStick=true;state.uiScroll.telStick=true}
  const graph=host.querySelector('.run-graph'),feed=host.querySelector('.worker-log-feed'),tel=host.querySelector('.telemetry-feed'),main=host.querySelector('.run-detail-main');
  if(graph){graph.scrollLeft=state.uiScroll.graphLeft||0;graph.onscroll=()=>{state.uiScroll.graphLeft=graph.scrollLeft}}
  if(main){main.scrollTop=state.uiScroll.mainTop||0;main.onscroll=()=>{state.uiScroll.mainTop=main.scrollTop}}
  const bindBottom=(el,key,stickKey)=>{if(!el)return;el.scrollTop=state.uiScroll[stickKey]?el.scrollHeight:state.uiScroll[key];el.onscroll=()=>{state.uiScroll[key]=el.scrollTop;state.uiScroll[stickKey]=(el.scrollHeight-el.clientHeight-el.scrollTop)<72}};
  bindBottom(feed,'feedTop','feedStick');
  // Telemetry is rendered newest-first, so its live edge is the top rather
  // than the bottom used by chronological Worker logs.
  if(tel){tel.scrollTop=state.uiScroll.telStick?0:state.uiScroll.telTop;tel.onscroll=()=>{state.uiScroll.telTop=tel.scrollTop;state.uiScroll.telStick=tel.scrollTop<72}}
}
function liveUsageHTML(r){
  const usage=r.usage||{};
  return `<span>资源消耗</span><b>${formatTokens(usage.totalTokens)} tokens · ${r.toolCalls||0} tools</b><small>${formatTokens(usage.inputTokens)} 输入 / ${formatTokens(usage.outputTokens)} 输出 · cache ${formatTokens(usage.cacheReadTokens)} 命中 / ${formatTokens(usage.cacheWriteTokens)} 写入 · $${Number(usage.costUsd||0).toFixed(4)}</small><small class="usage-source">实时 stream-json · Hook / OTEL 只进轨迹</small>`;
}
function gateResultCardHTML(nr){
  const gate=nr?.gateResult;
  if(gate&&nr.status==='waiting_approval'){
    const findings=(gate.findings||[]).map(f=>`<li><b>${escapeHTML(f.title||'待确认项')}</b>${f.evidence?`<p>${escapeHTML(f.evidence)}</p>`:''}</li>`).join('');
    return `<div class="node-decision human"><b>等待人工确认</b><div><strong>需要确认</strong><p>${escapeHTML(gate.actionRequired||'请结合节点产物和执行证据作出判断。')}</p></div><div><strong>Agent 判断依据</strong><p>${escapeHTML(gate.reason||'未提供')}</p></div>${gate.summary?`<div><strong>摘要</strong><p>${escapeHTML(gate.summary)}</p></div>`:''}${findings?`<ul>${findings}</ul>`:''}</div>`;
  }
  if(gate&&nr.status==='failed'){
    const findings=(gate.findings||[]).map(f=>`<li><b>${escapeHTML(f.title||'失败项')}</b>${f.evidence?`<p>${escapeHTML(f.evidence)}</p>`:''}</li>`).join('');
    // Only fail/rework decisions explain the failure in the agent's own words.
    // When the node failed with a pass/human decision (artifact contract or
    // command gate failed afterwards), nr.error is the real failure reason and
    // gate.reason ("任务已完成") must not mask it.
    const agentOwnsFailure=gate.decision==='fail'||gate.decision==='rework';
    const reason=agentOwnsFailure?(gate.reason||nr.error||'未提供'):(nr.error||'未提供');
    const terminalNote=agentOwnsFailure?'':`<strong>Agent 终态</strong><p>${gate.decision==='pass'?'pass：Agent 认为任务已完成，但节点客观校验未通过':'Agent 提交了 '+escapeHTML(gate.decision||'未知终态')+'，节点随后失败'}</p>`;
    return `<div class="node-error"><b>执行失败</b><div class="node-error-detail"><strong>失败原因</strong><p>${escapeHTML(reason)}</p>${terminalNote}${gate.summary?`<strong>摘要</strong><p>${escapeHTML(gate.summary)}</p>`:''}${findings?`<ul>${findings}</ul>`:''}</div></div>`;
  }
  return nr?.error&&nr.status==='failed'?`<div class="node-error"><b>执行失败</b><pre>${escapeHTML(nr.error)}</pre></div>`:'';
}
function unescapeJSONString(body){
  const attempt=value=>{try{return JSON.parse('"'+value+'"')}catch{return null}};
  let text=attempt(body);
  if(text==null){
    const cut=body.replace(/\\+$/,slash=>slash.length%2?slash.slice(0,-1):slash);
    text=attempt(cut);
  }
  return text==null?body.replace(/\\n/g,'\n').replace(/\\t/g,'\t').replace(/\\"/g,'"').replace(/\\\\/g,'\\'):text;
}
function prettyIfJSON(value){
  const text=String(value??'');
  const trimmed=text.trim();
  if(!trimmed.startsWith('{')&&!trimmed.startsWith('['))return text;
  try{return JSON.stringify(JSON.parse(trimmed),null,2)}catch{return text}
}
function assistantMessageText(message){
  if(!message||typeof message!=='object')return '';
  const content=message.content;
  if(typeof content==='string')return content;
  if(!Array.isArray(content))return '';
  return content.map(block=>{
    if(!block)return '';
    if(typeof block==='string')return block;
    if(block.type==='text'||block.type==='output_text')return block.text||'';
    return '';
  }).filter(Boolean).join('\n');
}
function looseProtocolText(raw){
  const trimmed=String(raw||'').trim();
  if(!trimmed.startsWith('{'))return '';
  if(!/"role"\s*:\s*"assistant"|"type"\s*:\s*"(?:assistant|result|agent_message)"/.test(trimmed))return '';
  const result=raw.match(/"result"\s*:\s*"((?:\\.|[^"\\])*)/);
  const text=raw.match(/"text"\s*:\s*"((?:\\.|[^"\\])*)/);
  const body=(result&&result[1])||(text&&text[1])||'';
  return body?unescapeJSONString(body):'';
}
function displayNodeOutput(raw){
  const text=String(raw??'');
  const trimmed=text.trim();
  if(!trimmed)return '';
  let resultText='',lastAssistant='',sawProtocol=false;
  for(const line of trimmed.split(/\r?\n/)){
    const item=line.trim();
    if(!item.startsWith('{'))continue;
    let obj;
    try{obj=JSON.parse(item)}catch{continue}
    if(!obj||typeof obj!=='object')continue;
    if(obj.type==='result'&&typeof obj.result==='string'){
      sawProtocol=true;
      if(obj.result.trim())resultText=obj.result;
      continue;
    }
    const message=obj.message&&typeof obj.message==='object'?obj.message:obj;
    if(obj.type==='assistant'||message.role==='assistant'){
      const extracted=assistantMessageText(message);
      if(extracted){sawProtocol=true;lastAssistant=extracted}
      continue;
    }
    const itemBody=obj.item&&typeof obj.item==='object'?obj.item:obj;
    if((obj.type==='agent_message'||itemBody.type==='agent_message')&&typeof itemBody.text==='string'&&itemBody.text.trim()){
      sawProtocol=true;
      lastAssistant=itemBody.text;
    }
  }
  const chosen=resultText||(sawProtocol?lastAssistant:'')||looseProtocolText(trimmed);
  return prettyIfJSON(chosen||text);
}
function inspectorNeedsRefresh(host,r){
  const selected=state.selectedRunNode,nr=r.nodes?.[selected]||{};
  if(Boolean(r.pauseRequested)!==Boolean(host.querySelector('.paused-run-callout')))return true;
  if(r.pauseRequested){
    const hasContextField=Boolean(host.querySelector('.paused-run-callout [data-human-context]'));
    if(hasContextField!==isRunPausedForAgent(r))return true;
  }
  const output=host.querySelector('.node-output');
  if(nr.output&&(!output||output.classList.contains('empty')))return true;
  if(nr.output){const pre=output.querySelector('pre');if(pre&&pre.textContent!==displayNodeOutput(nr.output))return true}
  if(!nr.output&&output&&!output.classList.contains('empty'))return true;
  const arts=(r.artifacts||[]).filter(a=>a.nodeId===selected);
  if(arts.length!==host.querySelectorAll('.node-artifact-card').length)return true;
  const hasCallout=Boolean(host.querySelector('.approval-callout,.node-decision,.node-error'));
  if(nr.gateResult&&!hasCallout)return true;
  if(nr.error&&nr.status==='failed'&&!hasCallout)return true;
  if(nr.status==='waiting_approval'&&!hasCallout)return true;
  return false;
}
function selectRunNode(nodeId){
  if(!nodeId)return;
  const same=state.selectedRunNode===nodeId;
  state.selectedRunNode=nodeId;
  if(!same){state.runLogs=null;state.runTelemetry=null}
  const run=state.activeRun;if(!run)return;
  state._scrollToDrill=true;
  renderRunDetail(run);
  if(!same){const pg=runPageTarget(run,nodeId);ensureRunPages(pg.runId,pg.nodeId)}
}
function patchRunLive(r){
  const host=$('#runDetail');if(!host||host.dataset.runId!==r.id||host.dataset.controlSignature!==runControlSignature(r)){renderRunDetail(r);return}
  const usageHost=host.querySelector('[data-run-usage]');
  if(usageHost){const html=liveUsageHTML(r);if(usageHost.innerHTML!==html)usageHost.innerHTML=html}
  if(inspectorNeedsRefresh(host,r)){renderRunDetail(r);return}
  const headerStatus=host.querySelector('.run-header-main .status');if(headerStatus){headerStatus.className=`status ${runDisplayStatusClass(r)}`;headerStatus.textContent=runDisplayStatus(r)}
  const progress=runProgress(r),bar=host.querySelector('.run-progress.large i'),pct=host.querySelector('.run-progress-pct'),count=host.querySelector('.run-progress-cell>b');
  if(bar)bar.style.width=progress.pct+'%';if(pct)pct.textContent=progress.pct+'% 已完成';if(count)count.textContent=`${progress.done} / ${progress.total} 节点已结束`;
  host.querySelectorAll('[data-run-node]').forEach(btn=>{
    const n=r.nodes?.[btn.dataset.runNode]||{},small=btn.querySelector('small');
    const gv=graphNodeView(r,btn.dataset.runNode);
    btn.className=`graph-node ${gv.cls} ${state.selectedRunNode===btn.dataset.runNode?'selected':''}`;
    if(small){const extra=(small.textContent.split(' · 上游')[1]);small.textContent=`${gv.text}${extra?` · 上游 ${extra}`:''}`}
    const count=(r.artifacts||[]).filter(a=>a.nodeId===btn.dataset.runNode).length;
    let em=btn.querySelector('em');
    if(count){if(!em){em=document.createElement('em');btn.appendChild(em)}em.textContent=`${count} 产物`}else if(em)em.remove();
  });
  const selected=state.selectedRunNode,nr=r.nodes?.[selected]||{};
  const inspectorStatus=host.querySelector('.node-inspector .status');if(inspectorStatus){inspectorStatus.className=`status ${nr.status||'pending'}`;inspectorStatus.textContent=statusText(nr.status)||'待执行'}
  const facts=host.querySelectorAll('.node-facts b');
  if(facts[0])facts[0].textContent=String(nr.attempt||0);
  if(facts[1])facts[1].textContent=String(nr.reworkCount||0);
  if(facts[2])facts[2].textContent=formatTokens(nr.usage?.totalTokens);
  if(facts[3])facts[3].textContent=String(nr.toolCalls||0);
  const artifactTotal=host.querySelector('.run-context-grid > div:last-child b');
  if(artifactTotal)artifactTotal.textContent=`${(r.artifacts||[]).length} 个不可变快照`;
  const activity=host.querySelector('.node-activity-inline');if(activity){if(nr.activity){activity.hidden=false;const b=activity.querySelector('b');if(b)b.textContent=nr.activity}else{activity.hidden=true}}
  const feed=host.querySelector('.worker-log-feed'),tel=host.querySelector('.telemetry-feed');
  const pg=runPageTarget(r,selected);
  const logPage=state.runLogs?.runId===pg.runId&&state.runLogs.nodeId===pg.nodeId&&state.runLogs.loaded?state.runLogs:null;
  const telAdapter=state.runTelemetryAdapter||'live';
  const telPage=state.runTelemetry?.runId===pg.runId&&state.runTelemetry.nodeId===pg.nodeId&&state.runTelemetry.adapter===telAdapter&&state.runTelemetry.loaded?state.runTelemetry:null;
  if(feed&&logPage){const html=visibleWorkerLogs(logPage.items).map(logLineHTML).join('')||'<div class="empty-inline"><p>该节点没有日志</p></div>';if(feed.innerHTML!==html){feed.innerHTML=html;if(state.uiScroll.feedStick)feed.scrollTop=feed.scrollHeight;else feed.scrollTop=state.uiScroll.feedTop}}
  if(tel&&telPage){const telemetry=compactTelemetry(telPage.items);const html=[...telemetry].reverse().map(telemetryItemHTML).join('')||`<div class="empty-inline"><p>${telemetryEmptyHint(telAdapter)}</p></div>`;if(tel.innerHTML!==html){tel.innerHTML=html;if(state.uiScroll.telStick)tel.scrollTop=0;else tel.scrollTop=state.uiScroll.telTop}}
  const heading=host.querySelector('.trace-section h3');if(heading)heading.textContent=telemetrySectionTitle(telAdapter);
  host.querySelectorAll('[data-tel-adapter]').forEach(b=>b.classList.toggle('active',b.dataset.telAdapter===telAdapter));
  syncHistoryButton(host.querySelector('.worker-log-section'),'data-logs-more','加载更早的 100 条',!!logPage?.hasMore,()=>loadRunLogs(pg.runId,pg.nodeId,true),logPage?`${visibleWorkerLogs(logPage.items).length} 条`:'');
  syncHistoryButton(host.querySelector('.trace-section'),'data-tel-more','加载更早的 100 条',!!telPage?.hasMore,()=>loadRunTelemetry(pg.runId,pg.nodeId,true),telPage?`${compactTelemetry(telPage.items).length} 条已加载`:'');
}
function syncHistoryButton(section,attr,label,hasMore,onClick,countText){
  if(!section)return;
  const heading=section.querySelector('.section-heading');
  if(!heading)return;
  const count=heading.querySelector('div span');
  if(count&&countText)count.textContent=countText;
  let actions=heading.querySelector(':scope > div:last-child');
  if(!actions){actions=document.createElement('div');heading.appendChild(actions)}
  let btn=section.querySelector(`[${attr}]`);
  if(hasMore){
    if(!btn){btn=document.createElement('button');btn.className='button ghost small';btn.setAttribute(attr,'1');btn.textContent=label;actions.appendChild(btn)}
    btn.onclick=onClick;
  }else if(btn)btn.remove();
}

function vscodeWorkbenchURL(runId,options={}){
  const params=new URLSearchParams();
  if(runId)params.set('run',runId);
  if(options.embed)params.set('embed','1');
  return `/vscode.html?${params.toString()}`;
}
function vscodeWindowName(runId){return 'agentflow-vscode-'+String(runId||'').replace(/[^A-Za-z0-9_-]/g,'')}
function openVSCodeWindow(runId){
  const win=window.open(vscodeWorkbenchURL(runId),vscodeWindowName(runId),'popup=yes,width=1440,height=900');
  if(!win)return false;
  try{win.focus()}catch(e){}
  return true;
}
function workspaceUnavailableHTML(reason){
  return `<div class="workspace-unavailable"><span>◇</span><b>当前无法浏览工作区</b><p>${escapeHTML(reason||'工作区不可用')}</p><small>运行日志和不可变交付物仍可继续查看。</small></div>`;
}
function runWorkspaceWasCleaned(run){
  const raw=run?.workspaceCleanedAt;
  if(!raw)return false;
  const ts=Date.parse(raw);
  if(!Number.isFinite(ts))return false;
  return new Date(ts).getUTCFullYear()>=1971;
}
function workspaceIsAvailable(run,ws){
  if(runWorkspaceWasCleaned(run))return false;
  return !!(ws&&ws.runId===run?.id&&ws.info?.available);
}
function workspacePaneHTML(run){
  const ws=state.runWorkspace;
  if(runWorkspaceWasCleaned(run))return workspaceUnavailableHTML('运行已完成，工作区已按清理策略删除');
  if(!ws||ws.runId!==run.id||ws.loading)return '<div class="workspace-loading"><span class="workspace-spinner"></span><b>正在读取工作区</b><p>确认工作区是否仍可浏览…</p></div>';
  if(ws.error)return `<div class="workspace-unavailable"><span>!</span><b>工作区加载失败</b><p>${escapeHTML(ws.error)}</p><button class="button ghost small" data-workspace-refresh>重试</button></div>`;
  if(!ws.info?.available)return workspaceUnavailableHTML(ws.info?.reason);
  const maximized=!!ws.maximized;
  return `<section class="run-workspace vscode-embed${maximized?' maximized':''}"><iframe data-vscode-frame data-run-id="${escapeHTML(run.id)}" title="VS Code" src="${escapeHTML(vscodeWorkbenchURL(run.id,{embed:true}))}" allow="clipboard-read; clipboard-write"></iframe></section>`;
}
function runPreviewDeclared(run){
  return !!(run?.workflowSnapshot?.preview&&run.workflowSnapshot.preview.enabled);
}
function shouldPatchRunLive(run){
  const host=$('#runDetail');
  if(!host||host.dataset.runId!==run?.id)return false;
  if(host.dataset.controlSignature!==runControlSignature(run))return false;
  // 列表摘要没有节点。先画了空拓扑之后，完整运行到达时必须整页重画，不能只补状态。
  const want=(run?.workflowSnapshot?.nodes||[]).length;
  if(want>0&&host.querySelectorAll('[data-run-node]').length!==want)return false;
  if(runPreviewDeclared(run)&&!host.querySelector('[data-run-tab="preview"]'))return false;
  return true;
}
function previewCandidateHttpsURL(rawUrl){
  if(!rawUrl)return '';
  try{
    const u=new URL(rawUrl,window.location.href);
    if(u.protocol==='http:'){
      u.protocol='https:';
      if(u.port==='80')u.port='';
    }
    return u.href;
  }catch(e){
    return rawUrl;
  }
}
async function resolvePreviewSessionURL(rawUrl){
  if(!rawUrl)return '';
  try{
    const parsed=new URL(rawUrl,window.location.href);
    if(parsed.protocol==='https:')return parsed.href;
    const httpsCandidate=previewCandidateHttpsURL(rawUrl);
    if(httpsCandidate===rawUrl)return rawUrl;

    const controller=typeof AbortController!=='undefined'?new AbortController():null;
    const timer=controller?setTimeout(()=>controller.abort(),2500):null;
    try{
      const resp=await fetch(httpsCandidate,{
        method:'HEAD',
        signal:controller?.signal,
        credentials:'omit'
      });
      if(timer)clearTimeout(timer);
      if(resp.ok||resp.status<500){
        return httpsCandidate;
      }
    }catch(err){
      if(timer)clearTimeout(timer);
      if(window.location.protocol!=='https:'){
        return rawUrl;
      }
    }
    return httpsCandidate;
  }catch(e){
    return rawUrl;
  }
}
function previewPaneHTML(run){
  const pv=state.runPreview;
  if(!runPreviewDeclared(run)&&!(pv&&pv.runId===run.id&&pv.info)){
    return `<div class="workspace-unavailable"><span>◇</span><b>该 SOP 未开启前端预览</b><p>在流程编辑页左侧「流程步骤」面板添加「预览」节点并连线到需要构建的位置，运行时自动构建静态站点并归档。</p></div>`;
  }
  if(!pv||pv.runId!==run.id||pv.loading)return '<div class="workspace-loading"><span class="workspace-spinner"></span><b>正在读取前端预览</b><p>确认交付物是否已经归档…</p></div>';
  if(pv.error)return `<div class="workspace-unavailable"><span>!</span><b>预览加载失败</b><p>${escapeHTML(pv.error)}</p><button class="button ghost small" data-preview-refresh>重试</button></div>`;
  const info=pv.info||{};
  if(info.status==='pending'||info.status==='archiving'){
    return `<div class="workspace-loading"><span class="workspace-spinner"></span><b>正在生成前端预览</b><p>来源 ${escapeHTML(info.sourcePath||run.workflowSnapshot?.preview?.sourcePath||'')}，打包完成后即可打开。</p><button class="button ghost small" data-preview-refresh>刷新</button></div>`;
  }
  if(info.status==='failed'){
    return `<div class="workspace-unavailable"><span>!</span><b>预览生成失败</b><p>${escapeHTML(info.error||'归档未能完成')}</p><small>业务交付结果不受影响，工作区仍会按成功 Run 策略清理。</small></div>`;
  }
  if(info.status==='skipped'){
    return `<div class="workspace-unavailable"><span>◇</span><b>已跳过前端预览</b><p>${escapeHTML(info.error||'该仓库没有可构建的前端工程。')}</p><small>业务交付结果不受影响，主流程已成功结束。</small></div>`;
  }
  if(info.status==='expired'||info.status==='deleted'){
    return `<div class="workspace-unavailable"><span>◇</span><b>预览已过期</b><p>该交付物已超过 TTL，静态包已被回收。</p></div>`;
  }
  if(info.status!=='ready'){
    return `<div class="workspace-unavailable"><span>◇</span><b>预览尚未就绪</b><p>当前状态：${escapeHTML(info.status||'未知')}</p></div>`;
  }
  const session=pv.session;
  if(!session?.url)return '<div class="workspace-loading"><span class="workspace-spinner"></span><b>正在签发预览会话</b></div>';
  const backend=String(session.backendUrl||pv.backendUrl||pv.info?.backendUrl||'').trim();
  const backendLabel=backend?`<code title="${escapeHTML(backend)}">${escapeHTML(backend)}</code>`:'<em>未设置</em>';
  const backendAction=backend?'更改':'设置';
  const frameUrl=window.location.protocol==='https:'?previewCandidateHttpsURL(session.url):session.url;
  return `<section class="run-preview-pane"><div class="run-preview-toolbar"><span>预览 · ${escapeHTML(info.entryFile||'index.html')} · ${info.fileCount||0} 个文件</span><div class="run-preview-backend"><small>当前后端</small>${backendLabel}<button type="button" class="button ghost small" data-preview-backend-edit>${backendAction}</button></div><div><button class="button ghost small" data-preview-refresh>刷新</button><button class="button ghost small" data-preview-copy>复制地址</button><button class="button ghost small" data-preview-open>独立窗口</button></div></div><iframe data-preview-frame data-run-id="${escapeHTML(run.id)}" title="前端预览" src="${escapeHTML(frameUrl)}" sandbox="allow-scripts allow-forms allow-modals allow-downloads allow-same-origin" referrerpolicy="no-referrer"></iframe></section>`;
}
function stopPreviewPoll(){
  if(state.previewPoll){clearTimeout(state.previewPoll);state.previewPoll=null}
  state.previewPollRunId=null;
  state.previewPollAttempt=0;
}
function schedulePreviewPoll(runId,status){
  if(status!=='pending'&&status!=='archiving'){stopPreviewPoll();return}
  if(state.selectedRun!==runId||(state.runDetailTab||'overview')!=='preview'){stopPreviewPoll();return}
  if(state.previewPollRunId!==runId){state.previewPollAttempt=0;state.previewPollRunId=runId}
  if(state.previewPoll){clearTimeout(state.previewPoll);state.previewPoll=null}
  const attempt=state.previewPollAttempt||0;
  if(attempt>=40){stopPreviewPoll();return}
  const delay=Math.min(8000,Math.round(1500*Math.pow(1.5,attempt)));
  state.previewPollAttempt=attempt+1;
  state.previewPoll=setTimeout(()=>{state.previewPoll=null;loadRunPreview(runId,{silent:true})},delay);
}
function previewBackendStorageKey(runId){return 'agentflow.preview.backend.'+runId}
function readPreviewBackend(runId){
  try{return (localStorage.getItem(previewBackendStorageKey(runId))||'').trim()}catch(e){return ''}
}
function persistPreviewBackend(runId, value){
  try{
    const key=previewBackendStorageKey(runId);
    value=String(value||'').trim();
    if(value)localStorage.setItem(key,value);else localStorage.removeItem(key);
  }catch(e){}
}
async function loadRunPreview(runId,opts){
  const run=state.activeRun;if(!run||run.id!==runId)return;
  const silent=!!opts?.silent;
  if(state.runPreview?.runId===runId&&state.runPreview.loading)return;
  const prior=state.runPreview?.runId===runId?state.runPreview:null;
  let backend=opts?.backend!=null?String(opts.backend).trim():(prior?.backendUrl||readPreviewBackend(runId));
  if(!silent){
    state.runPreview={runId,loading:true,error:'',info:prior?.info||null,session:prior?.session||null,backendUrl:backend};
    renderPreviewPane(run);
  }
  try{
    const info=await api(`/api/runs/${encodeURIComponent(runId)}/preview`,silent?{silent:true}:undefined);
    if(opts?.backend==null && info?.backendUrl!=null){
      backend=String(info.backendUrl).trim();
    }
    let session=prior?.session||null;
    const needRenew=!!(opts?.renewSession||!session||session.previewId!==info.id||String(session.backendUrl||'').trim()!==backend);
    if(info.status==='ready'&&needRenew){
      session=await api(`/api/runs/${encodeURIComponent(runId)}/preview/session`,{method:'POST',body:JSON.stringify({backendUrl:backend}),silent:!!silent});
    }
    if(session?.url){
      session.url=await resolvePreviewSessionURL(session.url);
    }
    if(state.activeRun?.id!==runId)return;
    persistPreviewBackend(runId, session?.backendUrl||backend);
    state.runPreview={runId,loading:false,error:'',info,session,backendUrl:session?.backendUrl||backend};
    schedulePreviewPoll(runId,info.status);
    renderPreviewPane(run);
  }catch(err){
    if(state.activeRun?.id!==runId)return;
    const msg=String(err?.message||err||'');
    const missing=msg.includes('preview not found')||msg.includes('HTTP 404')||msg.includes('not enabled');
    if(missing&&runPreviewDeclared(run)){
      state.runPreview={runId,loading:false,error:'',info:{status:'pending',sourcePath:run.workflowSnapshot?.preview?.sourcePath||''},session:null,backendUrl:backend};
      schedulePreviewPoll(runId,'pending');
    }else if(missing){
      stopPreviewPoll();
      state.runPreview={runId,loading:false,error:'',info:null,session:null,backendUrl:backend};
    }else{
      stopPreviewPoll();
      state.runPreview={runId,loading:false,error:msg,info:null,session:null,backendUrl:backend};
    }
  }
  renderPreviewPane(state.activeRun||run);
}
function renderPreviewPane(run){
  const pane=$('#runDetail')?.querySelector('[data-run-pane="preview"]');
  if(!pane||!run||pane.closest('#runDetail')?.dataset.runId!==run.id)return;
  const existing=pane.querySelector('iframe[data-preview-frame]');
  const keep=!!(existing&&existing.getAttribute('data-run-id')===run.id&&state.runPreview?.session?.url&&existing.getAttribute('src')===state.runPreview.session.url);
  pane.innerHTML=previewPaneHTML(run);
  if(keep){const fresh=pane.querySelector('iframe[data-preview-frame]');if(fresh)fresh.replaceWith(existing)}
  bindPreviewPane(run,pane);
}
function bindPreviewPane(run,pane){
  if(!pane)return;
  pane.querySelector('[data-preview-refresh]')?.addEventListener('click',()=>{stopPreviewPoll();state.runPreview=null;loadRunPreview(run.id)});
  pane.querySelector('[data-preview-copy]')?.addEventListener('click',()=>{const url=state.runPreview?.session?.url;if(url)copyText(url,'预览地址已复制')});
  pane.querySelector('[data-preview-open]')?.addEventListener('click',()=>{const url=state.runPreview?.session?.url;if(url)window.open(url,'_blank','noopener')});
  pane.querySelector('[data-preview-backend-edit]')?.addEventListener('click',()=>editPreviewBackend(run));
}
async function editPreviewBackend(run){
  const current=String(state.runPreview?.session?.backendUrl||state.runPreview?.backendUrl||state.runPreview?.info?.backendUrl||readPreviewBackend(run.id)||'').trim();
  const value=await afPrompt({
    title:'自定义预览后端',
    message:'预览页会拦截接口请求（fetch / XHR / axios / sendBeacon / WebSocket）：其它站点或站点根路径如 /api 会原样转到这个地址，静态资源仍走预览。留空则不允许访问任何后端。',
    label:'后端地址',
    placeholder:'https://api.example.com',
    defaultValue:current,
    confirmText:'应用'
  });
  if(value===null||value===undefined)return;
  const next=String(value).trim();
  if(state.runPreview?.session?.url && current===next){
    toast(next?'已是当前后端':'当前未设置后端','ok');
    return;
  }
  try{
    const res=await api(`/api/runs/${encodeURIComponent(run.id)}/preview/backend`,{
      method:'PUT',
      body:JSON.stringify({backendUrl:next})
    });
    persistPreviewBackend(run.id, next);
    let session=res?.session||null;
    if(session?.url){
      session.url=await resolvePreviewSessionURL(session.url);
    }
    const info=res?.preview||state.runPreview?.info||null;
    state.runPreview={
      runId:run.id,
      loading:false,
      error:'',
      info,
      session,
      backendUrl:session?.backendUrl||next
    };
    renderPreviewPane(state.activeRun||run);
    toast(next?'已应用到预览页':'已清除预览后端','ok');
  }catch(err){
    toast(err?.message||'设置后端地址失败','err');
  }
}
function renderWorkspacePane(run){
  const pane=$('#runDetail')?.querySelector('[data-run-pane="workspace"]');
  if(!pane||!run||pane.closest('#runDetail')?.dataset.runId!==run.id)return;
  const existingFrame=pane.querySelector('iframe[data-vscode-frame]');
  const keepFrame=!!(existingFrame&&existingFrame.getAttribute('data-run-id')===run.id&&workspaceIsAvailable(run,state.runWorkspace)&&!state.runWorkspace.loading);
  pane.innerHTML=workspacePaneHTML(run);
  if(keepFrame){const fresh=pane.querySelector('iframe[data-vscode-frame]');if(fresh)fresh.replaceWith(existingFrame)}
  bindWorkspacePane(run,pane);
}
function bindWorkspacePane(run,pane){
  if(!pane)return;
  if(!window.__agentflowVscodeBound){
    window.addEventListener('message',onWorkspaceVscodeMessage);
    window.__agentflowVscodeBound=true;
  }
  pane.querySelectorAll('[data-workspace-refresh]').forEach(button=>button.onclick=()=>loadRunWorkspace(run.id,true));
  applyWorkspaceEmbedState(run,pane);
}
async function loadRunWorkspace(runId,refresh=false){
  const run=state.activeRun?.id===runId?state.activeRun:null;
  if(runWorkspaceWasCleaned(run)){
    state.runWorkspace={runId,loading:false,error:'',info:{available:false,reason:'运行已完成，工作区已按清理策略删除'},maximized:false};
    renderWorkspacePane(run);
    return;
  }
  const cached=state.runWorkspace?.runId===runId?state.runWorkspace:null;
  if(!refresh&&cached&&(cached.loading||cached.info||cached.error))return;
  const prior=cached||{};
  state.runWorkspace={runId,loading:true,error:'',info:null,maximized:!!prior.maximized&&prior.runId===runId};
  renderWorkspacePane(state.activeRun);
  try{
    const info=await api(`/api/runs/${encodeURIComponent(runId)}/workspace`);
    if(state.runWorkspace?.runId!==runId)return;
    state.runWorkspace={...state.runWorkspace,loading:false,info,error:''};
  }catch(e){
    if(state.runWorkspace?.runId===runId)state.runWorkspace={...state.runWorkspace,loading:false,error:e.message};
  }
  renderWorkspacePane(state.activeRun);
}
function applyWorkspaceEmbedState(run,pane){
  const section=pane?.querySelector('.run-workspace')||pane;
  section?.classList.toggle('maximized',!!state.runWorkspace?.maximized&&state.runWorkspace?.runId===run.id);
}
function onWorkspaceVscodeMessage(ev){
  if(ev.origin!==location.origin||ev.data?.type!=='agentflow-vscode')return;
  const run=state.activeRun;
  if(!run||ev.data.runId!==run.id)return;
  if(ev.data.action==='maximize'){
    const maximized=ev.data.maximized!=null?!!ev.data.maximized:!(state.runWorkspace?.maximized);
    state.runWorkspace={...(state.runWorkspace||{}),runId:run.id,maximized};
    applyWorkspaceEmbedState(run,$('#runDetail')?.querySelector('[data-run-pane="workspace"]'));
    const frame=$('#runDetail')?.querySelector('iframe[data-vscode-frame]');
    try{frame?.contentWindow?.postMessage({type:'agentflow-vscode',action:'maximized',maximized,runId:run.id},location.origin)}catch(e){}
  }
  if(ev.data.action==='popout')openVSCodeWindow(run.id);
}
function runPageTarget(r,nodeId){
  const child=r?.nodes?.[nodeId]?.childRunId||'';
  return child?{runId:child,nodeId:'',child:true}:{runId:r?.id||'',nodeId:nodeId||'',child:false};
}
function childRunState(id){return state.childRuns?.[id]?.run||null}
function childRunSignature(run){return `${run?.status}|${Object.entries(run?.nodes||{}).map(([k,v])=>`${k}:${v?.status}`).sort().join(',')}`}
// Where the child SOP needs attention: a pending human decision first, then a failure, then the running node.
function childRunFocus(child){
  if(!child)return null;
  const nodes=child.workflowSnapshot?.nodes||[];
  const pick=status=>nodes.find(n=>child.nodes?.[n.id]?.status===status);
  for(const status of ['waiting_approval','failed','running']){const node=pick(status);if(node)return{status,node}}
  return{status:child.status,node:null};
}
function graphNodeView(r,nodeId){
  const x=r.nodes?.[nodeId]||{};
  const view={cls:x.status||'pending',text:`${statusText(x.status)||'待执行'} · attempt ${x.attempt||0}`};
  const focus=x.childRunId&&x.status==='running'?childRunFocus(childRunState(x.childRunId)):null;
  if(focus?.node){
    if(focus.status!=='running')view.cls=focus.status;
    view.text=`子 SOP ${statusText(focus.status)} · ${focus.node.name||focus.node.id}`;
  }
  return view;
}
function refreshChildRuns(r){
  state.childRuns=state.childRuns||{};
  const ids=[...new Set(Object.values(r?.nodes||{}).map(n=>n?.childRunId).filter(Boolean))];
  for(const id of ids){
    const cached=state.childRuns[id];
    const live=!cached?.run||['running','waiting','queued'].includes(cached.run.status);
    if(cached?.loading||(cached&&(!live||Date.now()-cached.at<4000)))continue;
    const before=cached?.run?childRunSignature(cached.run):'';
    state.childRuns[id]={...(cached||{}),loading:true,at:Date.now()};
    api(`/api/runs/${encodeURIComponent(id)}`,{silent:true}).then(run=>{
      state.childRuns[id]={run,at:Date.now(),loading:false};
      if(childRunSignature(run)!==before&&state.activeRun?.id===r.id&&state.selectedRun===r.id)renderRunDetail(state.activeRun);
    }).catch(()=>{state.childRuns[id]={...(cached||{}),loading:false,at:Date.now()}});
  }
  return ids;
}
// A parent run's own stream stays quiet while its child waits or runs, so poll the child here.
function watchChildRuns(r){
  const ids=refreshChildRuns(r);
  if(state.childRunTimer&&state.childRunWatch===r.id)return;
  clearInterval(state.childRunTimer);state.childRunTimer=null;
  if(!ids.length||!['queued','running','waiting'].includes(r.status))return;
  state.childRunWatch=r.id;
  state.childRunTimer=setInterval(()=>{
    const run=state.activeRun;
    if(activeViewName()!=='runs'||$('#runsDetailSection')?.hidden)return;
    if(!run||run.id!==r.id||state.selectedRun!==r.id||!['running','waiting'].includes(run.status)){clearInterval(state.childRunTimer);state.childRunTimer=null;return}
    refreshChildRuns(run);
    if(runPageTarget(run,state.selectedRunNode).child)refreshRunPagesDelta(run.id);
  },4000);
}
function childSOPPanelHTML(r,nodeId){
  const x=r?.nodes?.[nodeId];
  if(!x?.childRunId)return '';
  const child=childRunState(x.childRunId);
  const open=`<button type="button" class="button ghost small" data-open-child-run="${escapeHTML(x.childRunId)}">打开子 SOP 运行</button>`;
  if(!child)return `<div class="child-sop-panel"><div class="child-sop-head"><b>子 SOP 运行</b>${open}</div><p class="muted">正在加载子 SOP 状态…</p></div>`;
  const rows=(child.workflowSnapshot?.nodes||[]).map(n=>{const c=child.nodes?.[n.id]||{};return `<div class="child-sop-row"><span>${escapeHTML(n.name||n.id)}</span><span class="status ${escapeHTML(c.status||'pending')}">${statusText(c.status)||'待执行'}</span>${c.status==='failed'&&c.error?`<small>${escapeHTML(String(c.error).slice(0,200))}</small>`:''}</div>`}).join('');
  return `<div class="child-sop-panel"><div class="child-sop-head"><b>子 SOP：${escapeHTML(child.workflowSnapshot?.name||child.workflowId||'')}</b><span class="status ${runDisplayStatusClass(child)}">${runDisplayStatus(child)}</span>${open}</div>${rows}</div>`;
}
function childSOPCalloutHTML(r){
  const out=[];
  for(const [nodeId,x] of Object.entries(r?.nodes||{})){
    if(!x?.childRunId)continue;
    if(x.status!=='running'&&!(x.status==='failed'&&r.status==='failed'))continue;
    const child=childRunState(x.childRunId);
    if(!child){
      if(x.status==='failed'){const parentName=(r.workflowSnapshot?.nodes||[]).find(n=>n.id===nodeId)?.name||nodeId;out.push(`<div class="child-sop-callout failed"><div><b>「${escapeHTML(parentName)}」里的子 SOP 失败了</b><p>在子 SOP 运行里重试或手动通过后，父流程会继续。</p></div><button type="button" class="button primary small" data-open-child-run="${escapeHTML(x.childRunId)}">查看子 SOP 运行</button></div>`);}
      continue;
    }
    const focus=childRunFocus(child);
    if(!focus?.node||!['waiting_approval','failed'].includes(focus.status))continue;
    const parentName=(r.workflowSnapshot?.nodes||[]).find(n=>n.id===nodeId)?.name||nodeId,childName=child.workflowSnapshot?.name||child.workflowId||'';
    const nodeName=focus.node.name||focus.node.id,err=child.nodes?.[focus.node.id]?.error||'';
    const title=focus.status==='waiting_approval'?`「${parentName}」里的子 SOP 在等人工审批`:`「${parentName}」里的子 SOP 失败了`;
    const detail=focus.status==='waiting_approval'?`子 SOP「${childName}」的节点「${nodeName}」需要人工判断，审批在子 SOP 运行里处理，处理完父流程会自动继续。`:`子 SOP「${childName}」在节点「${nodeName}」失败${err?`：${String(err).slice(0,200)}`:''}。在子 SOP 运行里重试或手动通过后，父流程会继续。`;
    out.push(`<div class="child-sop-callout ${focus.status}"><div><b>${escapeHTML(title)}</b><p>${escapeHTML(detail)}</p></div><button type="button" class="button primary small" data-open-child-run="${escapeHTML(x.childRunId)}">${focus.status==='waiting_approval'?'去审批':'查看子 SOP 运行'}</button></div>`);
  }
  return out.join('');
}
function openRunDetailFresh(id){
  if(!id)return;
  state.selectedRunNode=null;state.runLogs=null;state.runTelemetry=null;
  showRunDetail(id);
}
function bindChildSOPActions(host,r,pg){
  host.querySelectorAll('[data-open-child-run]').forEach(b=>b.onclick=e=>{e.stopPropagation();openRunDetailFresh(b.dataset.openChildRun)});
  host.querySelector('[data-open-parent-run]')?.addEventListener('click',e=>{e.stopPropagation();openRunDetailFresh(e.currentTarget.dataset.openParentRun)});
  watchChildRuns(r);
}

function runIsGroupMode(r){return r?.workflowSnapshot?.collaborationMode==='group'}
function runFrozenSopNodes(r,wf){
  const frozen=wf?.sopCheckpoint?.workflow||wf;
  return (frozen.nodes||[]).filter(n=>!n.requestId);
}
function runProgressCellHTML(r,wf,progress,graphNodes){
  if(!runIsGroupMode(r)){
    return `<div class="run-progress-cell"><span>执行进度</span><b>${progress.done} / ${progress.total} 节点已结束</b><div class="run-progress large"><i style="width:${progress.pct}%"></i></div><small class="run-progress-pct">${progress.pct}% 已完成</small></div>`;
  }
  const live=(wf.nodes||[]).filter(n=>n.requestId).length;
  return `<div class="run-progress-cell"><span>协作模式</span><b>自由协作中</b><small class="run-progress-pct">SOP 主干已冷冻 · ${graphNodes.length} 步可展开 · 群内任务 ${live} 个</small></div>`;
}
function runModeChipHTML(r,wf){
  if(runIsGroupMode(r))return `<span class="run-meta-chip">自由协作</span><span class="run-meta-chip">冷冻 SOP v${wf.sopCheckpoint?.workflow?.version||wf.version||1}</span>`;
  return `<span class="run-meta-chip">SOP v${wf.version||1}</span>`;
}

function renderRunDetail(r){
  const host=$('#runDetail');if(!r){host.innerHTML='<div class="empty-properties"><div>◷</div><b>选择一次运行</b><p>节点状态、产物和执行轨迹会显示在这里</p></div>';delete host.dataset.runId;delete host.dataset.nodeId;return}
  if(host.dataset.runId&&host.dataset.runId!==r.id){
    resetRunScroll(r.id);
    state.runPreview=null;
  }
  const existingFrame=host.querySelector('iframe[data-vscode-frame]');
  const existingPreviewFrame=host.querySelector('iframe[data-preview-frame]');
  const keepFrame=!!(existingFrame&&host.dataset.runId===r.id&&existingFrame.getAttribute('data-run-id')===r.id&&workspaceIsAvailable(r,state.runWorkspace));
  const keepPreview=!!(existingPreviewFrame&&host.dataset.runId===r.id&&existingPreviewFrame.getAttribute('data-run-id')===r.id&&state.runPreview?.session?.url&&existingPreviewFrame.getAttribute('src')===state.runPreview.session.url);
  const wf=r.workflowSnapshot?.id?r.workflowSnapshot:((state.data.workflows||[]).find(w=>w.id===r.workflowId)||state.workflow||{id:r.workflowId||'',name:r.workflowId||'流程',nodes:[],edges:[],version:1}),nodes=wf.nodes||[],graphNodes=runIsGroupMode(r)?runFrozenSopNodes(r,wf):nodes,progress=runProgress(r),ctx=runContext(r),usage=r.usage||{};
  const issueId=ctx.issue?.id||r.executionContext?.issueId||'';
  const pickNodes=runIsGroupMode(r)?(graphNodes.length?graphNodes:nodes):nodes;if(!state.selectedRunNode||!pickNodes.some(n=>n.id===state.selectedRunNode)){state.selectedRunNode=pickNodes.find(n=>['running','waiting_approval','failed','cancelled'].includes(r.nodes?.[n.id]?.status))?.id||pickNodes.find(n=>r.nodes?.[n.id]?.status==='succeeded')?.id||pickNodes[0]?.id}const pg=runPageTarget(r,state.selectedRunNode);const selected=pickNodes.find(n=>n.id===state.selectedRunNode)||nodes.find(n=>n.id===state.selectedRunNode),nr=r.nodes?.[selected?.id]||{},nodeArtifacts=(r.artifacts||[]).filter(a=>a.nodeId===selected?.id),logPage=state.runLogs?.runId===pg.runId&&state.runLogs.nodeId===pg.nodeId&&state.runLogs.loaded?state.runLogs:null,telAdapter=state.runTelemetryAdapter||'live',telPage=state.runTelemetry?.runId===pg.runId&&state.runTelemetry.nodeId===pg.nodeId&&state.runTelemetry.adapter===telAdapter&&state.runTelemetry.loaded?state.runTelemetry:null,telemetry=compactTelemetry(telPage?.items||[]);
  const issueRef=ctx.issue?`${escapeHTML(ctx.project?.key||'TASK')}-${ctx.issue.number}`:'独立运行';
  const alignPanelHTML=r.workflowId==='scheduled-baseline-align'?alignConflictPanelHTML(r):'';
  const logBusy=state.runLogs?.runId===pg.runId&&state.runLogs.nodeId===pg.nodeId&&state.runLogs.loading&&!logPage;
  const previewEnabled=runPreviewDeclared(r);
  const allowedTabs=['overview','trace','logs','workspace','eval'];
  if(previewEnabled)allowedTabs.splice(4,0,'preview');
  const runDetailTab=allowedTabs.includes(state.runDetailTab)?state.runDetailTab:'overview';
  const runTabs=['overview:概览','trace:实时轨迹','logs:Worker日志','workspace:工作区'];
  if(previewEnabled)runTabs.push('preview:前端预览');
  runTabs.push('eval:智能评测');
  host.innerHTML=`<div class="run-detail-main"><header class="run-detail-header"><div class="run-detail-topbar"><button class="button ghost small run-back-inline" data-run-back>← 返回运行列表</button><div class="run-breadcrumb"><span>${issueRef}</span><i>›</i><span>${escapeHTML(wf.name)}</span></div>${r.executionContext?.parentRunId?`<button class="button ghost small" data-open-parent-run="${escapeHTML(r.executionContext.parentRunId)}">↑ 返回父 SOP 运行</button>`:''}<div class="run-header-actions">${issueId?`<button class="button primary small" data-open-issue="${escapeHTML(issueId)}">打开需求</button>`:''}${!r.pauseRequested?refreshRunSOPButtonHTML(r):''}<button class="button ghost small" data-open-flow>查看流程定义</button>${['queued','running','waiting'].includes(r.status)?'<button class="button danger-outline small" data-cancel-run>取消运行</button>':'<button class="button danger-outline small" data-delete-run>删除记录</button>'}</div></div><div class="run-header-main"><div><h2>${escapeHTML(ctx.issue?.title||wf.name)}</h2><p>${escapeHTML(r.input||'无运行输入')}</p></div><div class="run-status-wrap"><span class="status ${runDisplayStatusClass(r)}">${runDisplayStatus(r)}</span>${r.status==='failed'&&!gongfengIssueClosed(ctx.issue)?'<button class="button primary small" data-resume-run>↻ 重试运行</button>':''}${canManuallyPassFailedNode(r)&&!gongfengIssueClosed(ctx.issue)?`<button class="button ghost small" data-pass-node="${escapeHTML(r.id)}" data-node="${escapeHTML(failedNodeId(r))}">手动通过</button>`:''}</div></div><div class="run-header-meta"><span class="run-meta-chip run-id-chip" title="${escapeHTML(r.id)}">Run <code>${escapeHTML(r.id.slice(-12))}</code><button type="button" class="run-id-copy" data-copy-run title="复制完整 Run ID" aria-label="复制 Run ID"><svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><rect x="5.5" y="5.5" width="8" height="8" rx="1.5"/><path d="M10.5 5.5V3.5A1.5 1.5 0 0 0 9 2H3.5A1.5 1.5 0 0 0 2 3.5V9A1.5 1.5 0 0 0 3.5 10.5H5.5"/></svg></button></span>${runModeChipHTML(r,wf)}${runExecutionChipHTML(r)}<span class="run-meta-chip"><svg viewBox="0 0 24 24" width="11" height="11" fill="none" stroke="currentColor" stroke-width="2" style="vertical-align:-1px;margin-right:4px;"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg>启动于 ${new Date(r.createdAt).toLocaleString()}</span><span class="run-meta-chip" title="${escapeHTML(runTimingTitle(r))}"><svg viewBox="0 0 24 24" width="11" height="11" fill="none" stroke="currentColor" stroke-width="2" style="vertical-align:-1px;margin-right:4px;"><polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"/></svg>执行 ${runElapsed(r)}${runWallNote(r)}</span></div></header><div class="run-section-label"><b>执行详情</b><div class="run-section-label-actions"><button class="button ghost small" data-export-logs title="下载本次运行全部节点的 Worker 日志">导出全部日志</button><div class="run-tabs" role="tablist">${runTabs.map(t=>{const[k,label]=t.split(':');return `<button class="run-tab ${runDetailTab===k?'active':''}" data-run-tab="${k}">${label}</button>`}).join('')}</div></div></div><div class="run-pane" data-run-pane="overview" ${runDetailTab==='overview'?'':'hidden'}><section class="run-context-grid">${runProgressCellHTML(r,wf,progress,graphNodes)}<div data-run-usage>${liveUsageHTML(r)}</div><div><span>交付物</span><b>${(r.artifacts||[]).length} 个不可变快照</b><small>按节点和尝试次数保留版本</small></div></section></section>${alignPanelHTML}${runIsGroupMode(r)?`<details class="run-sop-snapshot"><summary><div><b>已冷冻 SOP 快照</b><span>自由协作中不按主干推进 · ${graphNodes.length} 个节点可展开审计 / 切回</span></div></summary>`:''}<section class="run-graph-section"><div class="section-heading"><div><h3>节点执行拓扑</h3><span>点击节点查看该节点的输出、产物和执行事件</span></div><div class="graph-legend"><span><i class="succeeded"></i>完成</span><span><i class="running"></i>运行中</span><span><i class="waiting_approval"></i>待审批</span><span><i></i>未开始</span></div></div><div class="run-graph">${graphNodes.map((n,index)=>{const gv=graphNodeView(r,n.id),meta=nodeMeta(n),arts=(r.artifacts||[]).filter(a=>a.nodeId===n.id).length;return `${index?'<span class="graph-arrow">→</span>':''}<button class="graph-node ${gv.cls} ${state.selectedRunNode===n.id?'selected':''}" data-run-node="${escapeHTML(n.id)}"><span class="node-icon ${meta.kind}">${meta.icon}</span><div><b>${escapeHTML(n.name)}</b><small>${escapeHTML(gv.text)}</small></div>${arts?`<em>${arts} 产物</em>`:''}</button>`}).join('')}</div></section><div class="node-drilldown"><section class="node-inspector"><div class="section-heading"><div class="node-inspector-title"><h3>${escapeHTML(selected?.name||'节点详情')}</h3><span class="node-activity-inline" ${nr.activity?'':'hidden'}>当前活动：<b>${escapeHTML(nr.activity||'')}</b></span><span class="node-inspector-model">${escapeHTML(nr.provider||'未分配')}${nr.model?` · ${escapeHTML(nr.model)}`:''}</span></div><span class="status ${nr.status||'pending'}">${statusText(nr.status)||'待执行'}</span></div><div class="node-facts"><div><span>执行次数</span><b>${nr.attempt||0}</b></div><div><span>返工</span><b>${nr.reworkCount||0}</b></div><div><span>Token</span><b>${formatTokens(nr.usage?.totalTokens)}</b></div><div><span>工具调用</span><b>${nr.toolCalls||0}</b></div></div>${childSOPPanelHTML(r,selected?.id)}${gateResultCardHTML(nr)}${canRerunFromHere(r,selected?.id)?`<div class="node-rerun-row"><button type="button" class="button ghost small" data-rerun-from-here="${escapeHTML(selected.id)}">从这里重跑</button><small>保留上游结果，从本节点及下游重跑</small></div>`:''}${nr.output?`<div class="node-output"><div><b>节点输出</b><button data-copy-output>复制</button></div><pre>${escapeHTML(displayNodeOutput(nr.output))}</pre></div>`:'<div class="node-output empty"><b>节点输出</b><p>节点完成后，结构化输出会显示在这里。</p></div>'}<div class="runtime-details"><span>Sandbox <code>${escapeHTML(nr.sandboxId||'—')}</code></span><span>Session <code>${escapeHTML(nr.sessionId||'—')}</code></span><span>${nr.startedAt?`${new Date(nr.startedAt).toLocaleTimeString()} → ${nr.finishedAt?new Date(nr.finishedAt).toLocaleTimeString():'现在'}`:'尚未调度'}</span></div></section><section class="node-artifacts"><div class="section-heading"><div><h3>节点交付物</h3><span>${nodeArtifacts.length} 个版本</span></div></div><div class="node-artifact-list">${nodeArtifacts.map(a=>`<button class="node-artifact-card" data-artifact="${escapeHTML(a.id)}"><span class="artifact-file-icon">${a.mediaType==='application/json'?'{}':'MD'}</span><div><b>${escapeHTML(a.name)}</b><span>${escapeHTML(a.path)}</span><small>attempt ${a.attempt} · ${Math.max(1,Math.ceil((a.size||0)/1024))} KB · ${(a.sha256||'').slice(0,10)}</small></div><i>预览 ›</i></button>`).join('')||'<div class="empty-inline"><span>◇</span><p>此节点还没有登记交付物</p></div>'}</div></section></div>${runIsGroupMode(r)?'</details>':''}</div><div class="run-pane" data-run-pane="trace" ${runDetailTab==='trace'?'':'hidden'}><section class="trace-section"><div class="section-heading"><div><h3>${telemetrySectionTitle(telAdapter)}</h3><span>${telPage?`${telemetry.length} 条已加载`:'加载最近 100 条'}</span></div><div class="trace-heading-actions">${telemetrySourceTabs(telAdapter,'data-tel-adapter')}${telPage?.hasMore?`<button class="button ghost small" data-tel-more>加载更早的 100 条</button>`:''}</div></div><div class="telemetry-feed">${telPage?([...telemetry].reverse().map(telemetryItemHTML).join('')||`<div class="empty-inline"><p>${telemetryEmptyHint(telAdapter)}</p></div>`):'<div class="empty-inline"><p>正在加载轨迹…</p></div>'}</div></section></div><div class="run-pane" data-run-pane="logs" ${runDetailTab==='logs'?'':'hidden'}><section class="worker-log-section"><div class="section-heading"><div><h3>Worker 日志</h3><span>${logPage?`${visibleWorkerLogs(logPage.items).length} 条调用`:(logBusy?'正在接收…':'实时加载最近调用')}</span></div><div class="worker-log-heading-actions">${logPage?.hasMore?`<button class="button ghost small" data-logs-more>加载更早的 100 条</button>`:''}</div></div><div class="worker-log-feed">${logPage?(visibleWorkerLogs(logPage.items).map(logLineHTML).join('')||'<div class="empty-inline"><p>该节点没有日志</p></div>'):'<div class="empty-inline"><p>正在加载 Worker 实时日志…</p></div>'}</div></section></div></div>`;  host.querySelector('.run-detail-main')?.insertAdjacentHTML('beforeend',`<div class="run-pane" data-run-pane="workspace" ${runDetailTab==='workspace'?'':'hidden'}>${workspacePaneHTML(r)}</div>${previewEnabled?`<div class="run-pane" data-run-pane="preview" ${runDetailTab==='preview'?'':'hidden'}>${previewPaneHTML(r)}</div>`:''}<div class="run-pane" data-run-pane="eval" ${runDetailTab==='eval'?'':'hidden'}>${runEvalPaneHTML(r)}</div>`);
  if(keepFrame){const fresh=host.querySelector('iframe[data-vscode-frame]');if(fresh)fresh.replaceWith(existingFrame);}
  if(keepPreview){const fresh=host.querySelector('iframe[data-preview-frame]');if(fresh)fresh.replaceWith(existingPreviewFrame);}
  const headerActions=host.querySelector('.run-header-actions');if(r.status==='running'&&headerActions)headerActions.querySelector('[data-cancel-run]')?.insertAdjacentHTML('beforebegin','<button class="button ghost small" data-pause-run>暂停运行</button>');
  const waitId=waitingNodeId(r);
  const failId=failedNodeId(r);
  const focusNode=waitId?nodes.find(n=>n.id===waitId):(r.status==='failed'&&failId?nodes.find(n=>n.id===failId):selected);
  const focusNr=focusNode?(r.nodes?.[focusNode.id]||{}):nr;
  if(r.status==='queued'){
    host.querySelector('[data-cancel-run]')?.replaceChildren(document.createTextNode('取消排队'));
    host.querySelector('.run-detail-header')?.insertAdjacentHTML('afterend','<div class="approval-callout queued-run-callout"><b>排队中，获得项目名额后自动启动</b><p>每个项目最多同时占用 10 条顶层 SOP，子 SOP 共享父级名额；未获准入和成功运行不占名额；一旦获准入即占用，失败、待确认、暂停及获准入后取消仍占用名额。可取消排队后删除记录。</p></div>');
  }else if(r.pauseRequested){
    const header=host.querySelector('.run-detail-header');header?.insertAdjacentHTML('afterend',pausedRunPanelHTML(r));
    host.querySelector('.node-inspector .approval-callout')?.remove();
    const status=host.querySelector('.run-header-main .status');if(status){status.textContent='已暂停';status.className='status paused'}
  }else if(focusNode&&focusNr&&(focusNr.status==='waiting_approval'||(r.status==='failed'&&focusNr.status==='failed'&&focusNode.type!=='sop'))){
    const header=host.querySelector('.run-detail-header');
    const callout=focusNr.status==='waiting_approval'?approvalCalloutHTML(focusNode,focusNr,nodes):failedNodeRecoveryCalloutHTML(r.id,focusNode,focusNr);
    header?.insertAdjacentHTML('afterend',callout);
    host.querySelector('.node-inspector .approval-callout')?.remove();
  }
  const childCallout=r.status==='queued'?'':childSOPCalloutHTML(r);if(childCallout)host.querySelector('.run-detail-header')?.insertAdjacentHTML('afterend',childCallout);
  if(pg.child){const note=`<p class="child-sop-trace-note">这是 SOP 节点，下面显示的是子 SOP 运行 <code>${escapeHTML(pg.runId.slice(-12))}</code> 全部节点的内容。</p>`;host.querySelectorAll('.trace-section>.section-heading,.worker-log-section>.section-heading').forEach(h=>h.insertAdjacentHTML('afterend',note))}
  const runMeta=host.querySelector('.run-header-meta');if(runMeta)runMeta.insertAdjacentHTML('beforeend',`${taskTriggerBadge(ctx.task)}<span class="run-meta-chip">Sandbox <code>${escapeHTML(r.sandboxId||'—')}</code></span>`);
  const nodeSandbox=host.querySelector('.runtime-details span:first-child');if(nodeSandbox)nodeSandbox.remove();
  const graph=host.querySelector('.run-graph');if(graph){graph.querySelectorAll('.graph-arrow').forEach(arrow=>arrow.remove());for(const node of workflowStages(runIsGroupMode(r)?{...wf,nodes:graphNodes}:wf).flat()){const button=graph.querySelector(`[data-run-node="${CSS.escape(node.id)}"]`),parents=(wf.edges||[]).filter(edge=>edge.target===node.id).map(edge=>nodes.find(item=>item.id===edge.source)?.name).filter(Boolean);if(!button)continue;if(parents.length)button.querySelector('small').append(` · 上游 ${parents.join('、')}`);graph.append(button)}}
  host.querySelector('[data-run-back]')?.addEventListener('click',()=>showRunList());
  host.querySelectorAll('[data-run-tab]').forEach(b=>b.onclick=()=>{const key=b.dataset.runTab;if((state.runDetailTab||'overview')===key)return;state.runDetailTab=key;host.querySelectorAll('[data-run-tab]').forEach(x=>x.classList.toggle('active',x.dataset.runTab===key));host.querySelectorAll('[data-run-pane]').forEach(p=>p.hidden=p.dataset.runPane!==key);if(key==='workspace')loadRunWorkspace(r.id);if(key==='preview')loadRunPreview(r.id);else stopPreviewPoll();if(key==='eval')loadRunEval(r.id)});
  host.querySelectorAll('[data-run-node]').forEach(b=>b.onclick=()=>selectRunNode(b.dataset.runNode));host.querySelectorAll('[data-tel-adapter]').forEach(b=>b.onclick=()=>{if((state.runTelemetryAdapter||'live')===b.dataset.telAdapter)return;state.runTelemetryAdapter=b.dataset.telAdapter;state.runTelemetry=null;renderRunDetail(state.activeRun||r)});host.querySelector('[data-logs-load]')?.addEventListener('click',()=>loadRunLogs(pg.runId,pg.nodeId,false));host.querySelector('[data-logs-more]')?.addEventListener('click',()=>loadRunLogs(pg.runId,pg.nodeId,true));host.querySelector('[data-tel-load]')?.addEventListener('click',()=>loadRunTelemetry(pg.runId,pg.nodeId,false));host.querySelector('[data-tel-more]')?.addEventListener('click',()=>loadRunTelemetry(pg.runId,pg.nodeId,true));host.querySelector('[data-open-issue]')?.addEventListener('click',ev=>{ev.preventDefault();ev.stopPropagation();openIssueDetail(ev.currentTarget.dataset.openIssue||issueId,'sop')});host.querySelectorAll('[data-rerun-from-here]').forEach(b=>b.onclick=()=>openRerunFromNode(state.activeRun||r,b.dataset.rerunFromHere));host.querySelector('[data-copy-run]')?.addEventListener('click',()=>copyText(r.id,'Run ID 已复制'));host.querySelector('[data-export-logs]')?.addEventListener('click',()=>downloadRunLogs(r.id));host.querySelector('[data-copy-output]')?.addEventListener('click',()=>copyText(displayNodeOutput(nr.output),'节点输出已复制'));host.querySelector('[data-open-flow]')?.addEventListener('click',()=>{state.workflow=clone(state.data.workflows.find(w=>w.id===r.workflowId)||wf);state.selectedNode=selected?.id||null;switchView('flow');renderAll()});host.querySelector('[data-cancel-run]')?.addEventListener('click',async ()=>{if(await afConfirm({title:'取消运行确认',message:r.status==='queued'?'确定取消排队？此运行将不会启动，之后可删除记录。':'确定取消这次运行？正在执行的节点会被终止。',confirmText:r.status==='queued'?'取消排队':'确认取消',isDanger:true}))runAction(r.id,'cancel',{})});host.querySelector('[data-delete-run]')?.addEventListener('click',()=>deleteRun(r.id));host.querySelectorAll('[data-resume-run]').forEach(b=>b.addEventListener('click',ev=>resumeRun(b.dataset.resumeRun||r.id,ev.currentTarget)));host.querySelectorAll('[data-pass-node]').forEach(b=>b.onclick=()=>passFailedNode(b.dataset.passNode,b.dataset.node,b));host.querySelectorAll('[data-artifact]').forEach(b=>b.onclick=()=>previewArtifact(state.activeRun||r,b.dataset.artifact));host.querySelectorAll('[data-approve]').forEach(b=>b.onclick=()=>runAction(r.id,'approve',withHumanContext({nodeId:b.dataset.approve,context:readHumanContext(b)})));host.querySelectorAll('[data-reject]').forEach(b=>b.onclick=async ()=>{const reason=await afPrompt({title:'打回返工',message:'请输入打回原因：',defaultValue:'验证未通过，请修复后重新提交'});if(reason!==null&&reason!==undefined)runAction(r.id,'reject',withHumanContext({nodeId:b.dataset.reject,targetId:b.dataset.target,reason,context:readHumanContext(b)}))});host.querySelectorAll('[data-retry-node]').forEach(b=>b.onclick=()=>retryWaitingNode(r.id,b.dataset.retryNode,b));host.querySelectorAll('[data-decline-retry]').forEach(b=>b.onclick=()=>declineRetry(r.id,b.dataset.declineRetry));host.querySelectorAll('[data-realign-run]').forEach(b=>b.onclick=()=>realignBaseline(b.dataset.realignRun));
  host.querySelector('[data-pause-run]')?.addEventListener('click',()=>pauseRun(r.id));bindPausedResumeActions(host);host.querySelectorAll('[data-refresh-run-sop]').forEach(b=>b.onclick=()=>refreshRunSOP(b.dataset.refreshRunSop));
  host.dataset.runId=r.id;host.dataset.nodeId=state.selectedRunNode||'';host.dataset.controlSignature=runControlSignature(r);
  bindWorkspacePane(r,host.querySelector('[data-run-pane="workspace"]'));
  if(runDetailTab==='workspace')loadRunWorkspace(r.id);
  if(previewEnabled){
    bindPreviewPane(r,host.querySelector('[data-run-pane="preview"]'));
    if(runDetailTab==='preview')loadRunPreview(r.id);
  }
  bindRunScrollLocks(host,r.id,state.selectedRunNode||'');
  if(state._scrollToDrill){state._scrollToDrill=false;requestAnimationFrame(()=>{const main=host.querySelector('.run-detail-main'),drill=host.querySelector('.node-drilldown');if(main&&drill){const top=drill.getBoundingClientRect().top-main.getBoundingClientRect().top+main.scrollTop-12;main.scrollTo({top:Math.max(0,top),behavior:'smooth'});state.uiScroll&&(state.uiScroll.mainTop=Math.max(0,top))}})}
  ensureRunPages(pg.runId,pg.nodeId);
  bindChildSOPActions(host,r,pg);
  bindRunEvalPane(r,host);
  if(runDetailTab==='eval')loadRunEval(r.id);
}
function fallbackCopyText(value){const ta=document.createElement('textarea');ta.value=value;ta.setAttribute('readonly','');ta.style.cssText='position:fixed;top:-9999px;left:-9999px';document.body.appendChild(ta);ta.select();let ok=false;try{ok=document.execCommand('copy')}catch(e){ok=false}document.body.removeChild(ta);return ok}
async function copyText(value,message){if(navigator.clipboard&&window.isSecureContext){try{await navigator.clipboard.writeText(value);toast(message);return}catch(e){}}toast(fallbackCopyText(value)?message:'复制失败，请手动复制')}
async function downloadRunLogs(runId){
  try{
    const res=await fetch(`/api/runs/${encodeURIComponent(runId)}/logs.txt`);
    if(!res.ok)throw new Error(`HTTP ${res.status}`);
    const blob=await res.blob();
    const url=URL.createObjectURL(blob);
    const a=document.createElement('a');
    a.href=url;
    a.download=`${runId}-worker-logs.txt`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
    toast('已导出全部 Worker 日志，可发给 AI 复盘');
  }catch(e){toast(e.message,'err')}
}
function defaultRunEval(){return{enabled:true,version:1,threshold:70,dimensions:[
  {id:'control',name:'控制流健康',question:'节点是否多次循环、返工或打回？',weight:25},
  {id:'latency',name:'耗时与等待',question:'单节点是否占了大部分执行时间？',weight:20},
  {id:'tools',name:'工具浪费',question:'是否有同工具连续失败或空转调用？',weight:20},
  {id:'tokens',name:'Token 效率',question:'Token 是否集中在单一节点？',weight:15},
  {id:'recovery',name:'错误恢复',question:'相同错误是否反复出现？',weight:10},
  {id:'delivery',name:'交付完整',question:'终态、产物和验证信号是否齐全？',weight:10}
]}}
function runEvalModel(){return state.runEvalModel||localStorage.getItem('agentflow.runEvalModel')||(window.AgentFlowAssistantModels&&window.AgentFlowAssistantModels.DEFAULT)||'default'}
function setRunEvalModel(id){state.runEvalModel=id;localStorage.setItem('agentflow.runEvalModel',id)}
function runEvalHasKey(){return !!(userById(state.currentUser?.id)||state.currentUser||{}).codeBuddyApiKeyConfigured}
function runEvalApplyable(rec){if(!rec||rec.status==='applied')return false;const action=((rec.target||'')+'.'+(rec.action||'')).replace(/^\./,'').toLowerCase();const full=rec.action&&rec.action.includes('.')?rec.action.toLowerCase():action;return ['workflow.update_node','agent.upsert_gate','agent.remove_gate','agent.append_instructions','agent.append_task_prompt'].includes(full)}
function runEvalPaneHTML(run){
  const rec=state.runEval?.runId===run.id?state.runEval:null,busy=!!state.runEvalBusy,thoughts=state.runEvalThoughts||[];
  const dims=rec?.policy?.dimensions||defaultRunEval().dimensions;
  const isTerminal=['succeeded','failed','cancelled'].includes(run.status);

  const heroCard=`<div class="run-eval-hero">
    <div class="run-eval-hero-info">
      <div class="run-eval-badge"><svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2.2"><path d="M12 2l2.4 7.2L21.6 12l-7.2 2.4L12 21.6l-2.4-7.2L2.4 12l7.2-2.4L12 2z"/></svg> AI 智能评测与架构巡检</div>
      <h2>SOP 执行效果与准入质量评估</h2>
      <p>基于大模型深度审视全链路 Trace，交叉评估目标达成度、产物契约与门禁防线，自动生成可确认落地的规则补丁与 SOP 优化建议。</p>
      <div class="run-eval-hero-meta">
        <span><i class="dot"></i>标准评测规则集 v${rec?.policy?.version||1}</span>
        <span><i class="dot"></i>基线准入分：${rec?.policy?.threshold||70} 分</span>
        <span><i class="dot"></i>支持一键回写 SOP 补丁</span>
      </div>
    </div>
    <div class="run-eval-hero-action-card">
      <div class="eval-control-label">评测大模型</div>
      <div class="run-eval-toolbar-row">
        <div class="schedule-model-field run-eval-model" id="runEvalModelPicker">
          <input type="hidden" name="runEvalModel" value="${escapeHTML(runEvalModel())}">
          <button type="button" class="ai-model-toggle" data-model-toggle aria-haspopup="listbox" aria-expanded="false">
            <span class="ai-model-icon" data-family="glm">智</span>
            <span data-model-label>选择模型</span>
            <span class="ai-model-caret" aria-hidden="true"></span>
          </button>
          <div class="ai-model-menu" hidden role="listbox"></div>
        </div>
        <button type="button" class="button primary run-eval-launch-btn" data-start-run-eval ${isTerminal?'':'disabled'}>
          <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.2"><path d="M12 2l2.4 7.2L21.6 12l-7.2 2.4L12 21.6l-2.4-7.2L2.4 12l7.2-2.4L12 2z"/></svg>
          <span>${rec?.status==='succeeded'?'重新评测':'启动智能评测'}</span>
        </button>
      </div>
      <div class="eval-control-hint">消耗约 1.5k~3.5k Tokens · 耗时约 5~12 秒</div>
    </div>
  </div>`;

  if(!isTerminal){
    return `<section class="run-eval-section">${heroCard}<div class="run-eval-empty-card"><div class="empty-icon"><svg viewBox="0 0 24 24" width="30" height="30" fill="none" stroke="currentColor" stroke-width="1.8"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg></div><h3>运行执行中，请等待结束后评测</h3><p>SOP 执行全部节点完成后，将汇聚完整的链路 Trace、Token 消耗分布与交付物契约，届时可在此一键启动质量与准入评测。</p></div></section>`;
  }

  if(busy||rec?.status==='running'){
    return `<section class="run-eval-section">${heroCard}<div class="run-eval-evaluating-card"><div class="evaluating-pulse-head"><span class="evaluating-spinner"></span><div><h4>大模型全链路 Trace 深度评测中...</h4><p>正在分析节点流转拓扑、工具调用频次、异常返工日志与产物交付契约</p></div></div><div class="run-eval-thoughts">${thoughts.map(t=>`<div class="thought-item"><span class="thought-dot"></span><span>${escapeHTML(t)}</span></div>`).join('')||'<div class="thought-item"><span class="thought-dot"></span><span>正在整理执行链路 Trace 并组织评测 Prompt...</span></div>'}</div></div></section>`;
  }

  if(rec?.status==='failed'){
    return `<section class="run-eval-section">${heroCard}<div class="run-eval-error-card"><div class="error-head"><svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/></svg><b>评测执行失败</b></div><pre>${escapeHTML(rec.error||'模型未返回结果')}</pre><p class="error-tip">请检查个人设置中的 CodeBuddy API Key 配置或网络状态后重新评测。</p></div></section>`;
  }

  if(!rec||!rec.status){
    return `<section class="run-eval-section">${heroCard}<div class="run-eval-pre-dashboard"><div class="eval-pre-title-row"><div class="eval-pre-title"><span class="eval-pre-icon"><svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><path d="M9 5H7a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7a2 2 0 0 0-2-2h-2"/><rect x="9" y="3" width="6" height="4" rx="1"/><path d="M9 12h6M9 16h4"/></svg></span><div><h3>评测指标体系与准入标准</h3><p>系统将基于当前 SOP 规则策略自动对本次运行的以下 6 个核心维度进行打分与归因诊断：</p></div></div><div class="eval-policy-tag">基线通过线：<b>${rec?.policy?.threshold||70} 分</b></div></div><div class="run-eval-dim-grid">${dims.map(d=>`<div class="eval-dim-card"><div class="eval-dim-head"><span class="eval-dim-name">${escapeHTML(d.name)}</span><span class="eval-dim-weight">权重 ${d.weight}%</span></div><p class="eval-dim-q">${escapeHTML(d.question)}</p></div>`).join('')}</div><div class="eval-pre-footer-tip"><span class="tip-sparkle"><svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg></span><p>评测完成后，不仅将生成各维度量化得分和缺陷根因证据链，还将提供针对任务书（Prompt）、门禁（Gate）以及重试策略的<b>「一键合并优化建议」</b>，帮助持续沉淀企业级优质 SOP。</p></div></div></section>`;
  }

  const scores=rec.result?.dimensions||{};
  const bars=dims.map(d=>{
    const val=scores[d.id]??'—';
    const num=typeof val==='number'?val:0;
    const barClass=num>=80?'high':(num>=60?'med':'low');
    return `<div class="clarify-bar"><span>${escapeHTML(d.name)}</span><i><b class="${barClass}" style="width:${Math.max(0,Math.min(100,num))}%"></b></i><em>${val}</em></div>`;
  }).join('');
  const findings=(rec.result?.findings||[]).map(f=>`<li class="finding-item ${escapeHTML(f.severity||'info')}"><div class="finding-head"><span class="finding-badge ${escapeHTML(f.severity||'info')}">${escapeHTML(f.severity==='error'?'严重缺陷':f.severity==='warn'?'改进建议':'信息备忘')}</span><b class="finding-dim">${escapeHTML(f.dimension||'')}</b></div><p class="finding-msg">${escapeHTML(f.message||'')}</p>${f.nodeId?`<small class="finding-evidence">节点：<b>${escapeHTML(f.nodeId)}</b>${f.evidence?' · 证据：'+escapeHTML(f.evidence):''}</small>`:''}</li>`).join('');
  const recs=(rec.result?.recommendations||[]).map(item=>runEvalRecHTML(item)).join('')||'<div class="empty-inline"><p>本次评测未发现阻止落地的缺陷，无需额外规则补丁。</p></div>';

  return `<section class="run-eval-section">${heroCard}<div class="run-eval-result-grid"><div class="run-eval-scorecard"><div class="eval-score-ring-wrap ${rec.passed?'good':(rec.score>=60?'fair':'weak')}"><div class="eval-score-num">${rec.score||0}</div><div class="eval-score-label">${rec.passed?'达标通过':'未达标'}</div></div><div class="eval-score-details"><div class="eval-score-meta"><span><b>模型：</b>${escapeHTML(rec.model||'—')}</span><span><b>准入基线：</b>${rec.policy?.threshold||70} 分</span><span><b>规则：</b>v${rec.policy?.version||1}</span></div><div class="eval-dims-bars">${bars}</div></div></div>${rec.result?.summary?`<div class="run-eval-summary-card"><div class="summary-head"><span class="summary-icon"><svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="11" width="18" height="10" rx="2"/><circle cx="12" cy="5" r="2"/><path d="M12 7v4M8 16h.01M16 16h.01"/></svg></span><b>AI 架构师评估总述</b></div><p>${escapeHTML(rec.result.summary)}</p></div>`:''}</div>${findings?`<div class="run-eval-findings-box"><div class="findings-box-head"><h4>关键诊断发现 <span>(${(rec.result?.findings||[]).length})</span></h4></div><ul class="run-eval-findings">${findings}</ul></div>`:''}<div class="run-eval-recs-box"><div class="recs-head"><h4>可落地改进建议 <span>(${(rec.result?.recommendations||[]).length})</span></h4><p>点击「应用此修改」可直接将治理规则回写到 SOP 流程或角色定义中</p></div><div class="recs-list">${recs}</div></div></section>`;
}
function runEvalRecHTML(item){
  const applied=item.status==='applied',can=runEvalApplyable(item);
  const patch=item.gate||item.set||(item.text?{text:item.text}:null);
  return `<article class="run-eval-rec ${applied?'is-applied':''}"><div><b>${escapeHTML(item.title||item.action||'建议')}</b><p>${escapeHTML(item.rationale||'')}</p><small>${escapeHTML(item.target||'')} ${escapeHTML(item.action||'')} ${item.nodeId?('· '+escapeHTML(item.nodeId)):''}</small>${patch?`<details><summary>预览补丁</summary><pre>${escapeHTML(JSON.stringify(patch,null,2))}</pre></details>`:''}${item.applyError?`<small class="danger">${escapeHTML(item.applyError)}</small>`:''}</div>${applied?`<button type="button" class="button ghost small" data-open-eval-target="${escapeHTML(item.target||'')}" data-open-eval-id="${escapeHTML(item.appliedTargetId||item.targetId||'')}">打开</button>`:(can?`<button type="button" class="button primary small" data-apply-eval="${escapeHTML(item.id)}">应用此修改</button>`:'<small>仅建议</small>')}</article>`;
}
function bindRunEvalPane(run,host){
  bindRunEvalModelPicker(host.querySelector('#runEvalModelPicker'));
  host.querySelectorAll('[data-run-eval]').forEach(b=>b.onclick=()=>{
    state.runDetailTab='eval';
    host.querySelectorAll('[data-run-tab]').forEach(x=>x.classList.toggle('active',x.dataset.runTab==='eval'));
    host.querySelectorAll('[data-run-pane]').forEach(p=>p.hidden=p.dataset.runPane!=='eval');
    startRunEval(run);
  });
  host.querySelectorAll('[data-start-run-eval]').forEach(b=>b.onclick=()=>startRunEval(run));
  host.querySelectorAll('[data-apply-eval]').forEach(b=>b.onclick=()=>applyRunEvalRec(run,b.dataset.applyEval));
  host.querySelectorAll('[data-open-eval-target]').forEach(b=>b.onclick=()=>openRunEvalTarget(b.dataset.openEvalTarget,b.dataset.openEvalId));
}
function bindRunEvalModelPicker(host){
  if(!host)return;
  const api=window.AgentFlowAssistantModels,toggle=host.querySelector('[data-model-toggle]'),menu=host.querySelector('.ai-model-menu');
  if(!api||!toggle||!menu)return;
  const paint=()=>{const keep=runEvalModel();const inp=host.querySelector('[name=runEvalModel]');if(inp)inp.value=keep;api.paint(toggle,keep)};
  paint();
  api.load?.().then(paint).catch(paint);
  if(host.dataset.bound)return;
  host.dataset.bound='1';
  toggle.onclick=e=>{
    e.stopPropagation();
    const open=menu.hidden;
    menu.hidden=!open;
    toggle.setAttribute('aria-expanded',open?'true':'false');
    if(open)api.renderList(menu,runEvalModel(),id=>{setRunEvalModel(id);paint();menu.hidden=true;toggle.setAttribute('aria-expanded','false')});
  };
  document.addEventListener('click',e=>{if(host.contains(e.target))return;menu.hidden=true;toggle.setAttribute('aria-expanded','false')});
}
async function loadRunEval(runId,background){
  try{
    const rec=await api(`/api/runs/${encodeURIComponent(runId)}/eval`,{silent:true});
    if((state.activeRun?.id||state.selectedRun)!==runId&&state.activeRun?.id!==runId)return;
    state.runEval={...rec,runId};
    if(rec?.model)setRunEvalModel(rec.model);
    if(rec?.status==='running'&&!state.runEvalStream)attachRunEvalStream(runId);
    if(!background){
      const host=$('#runDetail');
      const pane=host?.querySelector('[data-run-pane="eval"]');
      if(pane){pane.innerHTML=runEvalPaneHTML(state.activeRun||{id:runId,status:state.activeRun?.status});bindRunEvalPane(state.activeRun||{id:runId,status:state.activeRun?.status},host)}
    }
  }catch(e){if(!background)toast(e.message,'err')}
}
async function startRunEval(run){
  if(!['succeeded','failed','cancelled'].includes(run.status))return toast('请等 SOP 跑完后再评测');
  if(!runEvalHasKey()){toast('请先在个人设置中配置自己的 CodeBuddy API Key','err');switchView('settings');return}
  state.runEvalBusy=true;state.runEvalThoughts=['正在提交评测…'];
  const host=$('#runDetail'),pane=host?.querySelector('[data-run-pane="eval"]');
  if(pane){pane.innerHTML=runEvalPaneHTML(run);bindRunEvalPane(run,host)}
  try{
    const rec=await api(`/api/runs/${encodeURIComponent(run.id)}/eval`,{method:'POST',body:JSON.stringify({model:runEvalModel()})});
    state.runEval={...rec,runId:run.id};
    attachRunEvalStream(run.id);
  }catch(e){state.runEvalBusy=false;toast(e.message,'err');if(pane){pane.innerHTML=runEvalPaneHTML(run);bindRunEvalPane(run,host)}}
}
function attachRunEvalStream(runId){
  if(state.runEvalStream){state.runEvalStream.close();state.runEvalStream=null}
  const es=new EventSource(`/api/runs/${encodeURIComponent(runId)}/eval/stream`);
  state.runEvalStream=es;
  const onThought=ev=>{try{const p=JSON.parse(ev.data||'{}');if(p.text){state.runEvalThoughts=[...(state.runEvalThoughts||[]),p.text].slice(-12);const pane=$('#runDetail')?.querySelector('[data-run-pane="eval"] .run-eval-thoughts');if(pane)pane.innerHTML=state.runEvalThoughts.map(t=>`<p>${escapeHTML(t)}</p>`).join('')}}catch(e){}};
  es.addEventListener('thought',onThought);
  const finish=async()=>{es.close();if(state.runEvalStream===es)state.runEvalStream=null;state.runEvalBusy=false;await loadRunEval(runId)};
  es.addEventListener('done',finish);
  es.addEventListener('error',async ev=>{if(ev.data){try{const p=JSON.parse(ev.data);if(p.error)toast(p.error,'err')}catch(e){}}await finish()});
}
async function applyRunEvalRec(run,recId){
  if(!await afConfirm({title:'应用评测建议',message:'确认把这条建议写进当前 SOP 或角色？不会改这次运行的历史快照。',confirmText:'确认应用'}))return;
  try{
    const rec=await api(`/api/runs/${encodeURIComponent(run.id)}/eval/apply`,{method:'POST',body:JSON.stringify({evalId:state.runEval?.id,recommendationIds:[recId]})});
    state.runEval={...rec,runId:run.id};
    const [workflows,agents]=await Promise.all([api('/api/workflows',{silent:true}),api('/api/agents',{silent:true})]);
    state.data.workflows=workflows;state.data.agents=agents;
    toast('已写回当前定义','ok');
    const host=$('#runDetail'),pane=host?.querySelector('[data-run-pane="eval"]');
    if(pane){pane.innerHTML=runEvalPaneHTML(run);bindRunEvalPane(run,host)}
  }catch(e){toast(e.message,'err')}
}
function openRunEvalTarget(target,id){
  if(target==='agent'){state.selectedAgent=id;switchView('agents');const agent=(state.data.agents||[]).find(a=>a.id===id);if(agent&&typeof openAgentWizard==='function')openAgentWizard(agent);return}
  const wf=(state.data.workflows||[]).find(w=>w.id===id);
  if(wf){state.workflow=clone(wf);state.selectedNode=null;switchView('flow');renderAll()}
}
async function previewArtifact(run,artifactId){
  const artifact=(run.artifacts||[]).find(a=>a.id===artifactId);
  if(!artifact)return;
  const host=$('#artifactPreview');
  host.innerHTML='<div class="artifact-loading">正在加载交付物…</div>';
  $('#artifactModal').classList.add('open');
  try{
    const res=await fetch(`/api/runs/${encodeURIComponent(run.id)}/artifacts/${encodeURIComponent(artifactId)}`);
    if(!res.ok)throw new Error(`HTTP ${res.status}`);
    const content=await res.text();
    const isMd=isMarkdownArtifact(artifact, content);
    let viewMode=isMd?'preview':'source';

    function renderModal(){
      host.innerHTML=`
        <div class="artifact-preview-head">
          <div class="artifact-preview-head-left">
            <span class="artifact-preview-icon">${isMd?'MD':'{}'}</span>
            <div class="artifact-preview-titles">
              <h2>${escapeHTML(artifact.name)}</h2>
              <div class="artifact-preview-sub">
                <span>${escapeHTML(artifact.path)}</span>
                <span>·</span>
                <span>attempt ${artifact.attempt}</span>
              </div>
            </div>
          </div>
          <div class="artifact-preview-head-actions">
            <a class="button ghost small" href="/api/runs/${encodeURIComponent(run.id)}/artifacts/${encodeURIComponent(artifactId)}" target="_blank" rel="noopener noreferrer">在新窗口打开 ↗</a>
          </div>
        </div>
        <div class="artifact-preview-toolbar">
          <div class="artifact-toolbar-left">
            ${isMd ? `
              <div class="artifact-view-mode">
                <button type="button" class="artifact-mode-btn ${viewMode==='preview'?'active':''}" data-mode="preview">📖 渲染预览</button>
                <button type="button" class="artifact-mode-btn ${viewMode==='source'?'active':''}" data-mode="source">📄 源码</button>
              </div>
            ` : ''}
            <div class="artifact-evidence-badges">
              <span>SHA-256: <code>${escapeHTML((artifact.sha256||'—').slice(0, 16))}…</code></span>
              <span>${Math.max(1, Math.ceil((artifact.size||0)/1024))} KB</span>
            </div>
          </div>
          <div class="artifact-toolbar-right">
            <button type="button" class="button ghost small" data-copy-artifact>📋 复制内容</button>
            <button type="button" class="button ghost small" data-download-artifact>⬇ 下载文件</button>
          </div>
        </div>
        <div class="artifact-body">
          ${viewMode==='preview' ? `<div class="markdown-body">${renderMarkdown(content)}</div>` : `<pre class="artifact-content">${escapeHTML(content)}</pre>`}
        </div>
      `;

      host.querySelectorAll('.artifact-mode-btn').forEach(btn=>{
        btn.onclick=()=>{
          viewMode=btn.dataset.mode;
          renderModal();
        };
      });

      host.querySelector('[data-copy-artifact]').onclick=()=>copyText(content, '产物内容已复制到剪贴板');

      host.querySelector('[data-download-artifact]').onclick=()=>{
        const blob=new Blob([content], {type: artifact.mediaType || 'text/plain'});
        const url=URL.createObjectURL(blob);
        const a=document.createElement('a');
        a.href=url;
        a.download=artifact.name || 'artifact.txt';
        a.click();
        URL.revokeObjectURL(url);
      };

      host.querySelectorAll('.md-code-copy').forEach(b=>{
        b.onclick=()=>{
          const codeText=b.closest('.md-code-block')?.querySelector('pre code')?.textContent||'';
          copyText(codeText, '代码片段已复制');
        };
      });
    }

    renderModal();
  }catch(e){
    host.innerHTML=`<div class="empty-properties"><b>交付物加载失败</b><p>${escapeHTML(e.message)}</p></div>`;
  }
}
async function runAction(id,action,body){
  if(action!=='cancel'&&queuedRunActionBlocked(id))return;
  try{
    await api(`/api/runs/${encodeURIComponent(id)}/${action}`,{method:'POST',body:JSON.stringify(body)});
    const run=await syncRunControlState(id);
    await refreshRuns(true);
    toast(action==='cancel'&&run.status==='cancelled'?(run.admissionState==='unstarted'?'已取消排队':'运行已取消'):runControlToast(run,action==='approve'?'已批准，流程继续':action==='reject'?'已打回，开始返工':action==='retry'?'已从中断处继续':'已选择不重试'));
  }catch(e){toast(e.message)}
}
function runExecutionChipHTML(r){
  const ec=r.executionContext||{};
  if(ec.executionMode!=='runner')return '';
  const id=ec.executionRunnerId||'';
  const mine=(state.runners||[]).find(m=>m.id===id);
  const personal=r.concurrencyExempt===true||Number(mine?.ownerUserId)>0;
  const machineLimit=Number(mine?.runLimit)||0;
  const label=personal?`个人机器 ${mine?.name||id.slice(-8)} · 不受项目 10 条 SOP 限额${machineLimit>0?`，本机最多 ${machineLimit} 条`:''}`:ec.executionPool==='cloud'?(id?'云端执行池':'等待云端执行机器'):`执行机器 ${mine?.name||id.slice(-8)}`;
  return `<span class="run-meta-chip" title="${escapeHTML(id||'尚未分配')}">${escapeHTML(label)}</span>`;
}
async function deleteRun(id){
  if(!id)return;
  if(queuedRunActionBlocked(id))return;
  if(!(await afConfirm({title:'删除运行记录',message:'确定删除这条运行记录？其节点状态、日志、产物以及对应执行工作区和沙箱会一并清理，且不可恢复。',danger:true,confirmText:'确定删除'})))return;
  try{
    await api(`/api/runs/${encodeURIComponent(id)}`,{method:'DELETE'});
    toast('运行记录已删除');
    if(state.selectedRun===id){state.selectedRun=null;state.activeRun=null;showRunList()}
    // Deleting a Run also changes the execution state of its Task/Issue.  Pull
    // all three collections so the issue list and an open SOP pane cannot keep
    // rendering the deleted run as “正在启动中”.
    await Promise.all([refreshRuns(),refreshIssues()]);
  }catch(e){toast(e.message,'err')}
}
