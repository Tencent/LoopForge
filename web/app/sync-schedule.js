let scheduleNotificationBindings=[];
let scheduleEditorKind='dispatch';
let scheduleModalVersion=0;
let scheduleModalOpener=null;
// --- Sync Panel (embedded in Issues view) ---
function sourceBadge(issue){if(!issue.source||issue.source==='local')return '<span class="sync-badge local">本地</span>';if(issue.source==='github')return '<span class="sync-badge local">GitHub</span>';if(issue.source==='gongfeng')return '<span class="sync-badge gongfeng">工蜂</span>';if(issue.source==='tapd')return '<span class="sync-badge tapd">TAPD</span>';return ''}
function externalLink(issue){if(!issue.externalRef)return '';return `<a href="${escapeHTML(safeURL(issue.externalRef.externalUrl))}" target="_blank" rel="noopener noreferrer" class="ext-link" title="${escapeHTML(issue.externalRef.externalUrl)}">↗</a>`}
function renderSync(){
  const cfg=state.data.syncConfig||{};const logs=state.data.syncLogs||[];
  const syncing=state.data.syncing;
  const projectIssues=(state.data.issues||[]).filter(i=>!state.projectFilter||i.projectId===state.projectFilter);
  const gfCount=projectIssues.filter(i=>i.source==='gongfeng').length;
  const tapdCount=projectIssues.filter(i=>i.source==='tapd').length;
  const githubCount=projectIssues.filter(i=>i.source==='github').length;
  const lastSync=knownTime(cfg.lastSyncAt)?.toLocaleString()||'从未同步';
  // Scope logs to the current project (legacy entries without projectId stay
  // visible everywhere); diagnostics only reflect what the user can act on.
  const scopedLogs=logs.filter(l=>!state.projectFilter||!l.projectId||l.projectId===state.projectFilter);
  // Only the most recent sync round counts for status: stale failures from
  // older rounds (or legacy unscoped entries) must not keep the badge red.
  const recentLogs=scopedLogs.filter(l=>!knownTime(cfg.lastSyncAt)||new Date(l.startedAt).getTime()>=new Date(cfg.lastSyncAt).getTime()-15*60*1000);
  const recentBySource=['github','gongfeng','tapd'].map(source=>recentLogs.find(l=>l.source===source)).filter(Boolean);
  const scopedFails=recentBySource.filter(l=>l.status==='failed');
  const badge=$('#syncBadge');
  if(syncing){badge.className='sync-badge live';badge.textContent='同步中'}
  else if(scopedFails.length){badge.className='sync-badge failed';badge.textContent='同步失败'}
  else if(scopedLogs.some(l=>l.status==='success')||cfg.lastSyncStatus==='success'){badge.className='sync-badge success';badge.textContent='已同步'}
  else{badge.className='sync-badge live';badge.textContent='未配置'}
  const more=$('#issueHeadingMore');
  if(more){const failed=!syncing&&scopedFails.length>0;more.classList.toggle('has-alert',failed);more.title=failed?'外部同步失败':''}
  const latestBySource=['github','gongfeng','tapd'].map(source=>scopedLogs.find(l=>l.source===source)).filter(Boolean);
  const diag=[...new Set(scopedFails.flatMap(l=>String(l.message||'').split(/[；;]/)).map(s=>s.trim()).filter(Boolean))];
  const diagText=diag.join('；');
  $('#syncPanelSummary').innerHTML=`<span class="sync-stat"><b>${githubCount}</b>GitHub</span>${gfCount?`<span class="sync-stat"><b>${gfCount}</b>工蜂</span>`:''}${tapdCount?`<span class="sync-stat"><b>${tapdCount}</b>TAPD</span>`:''}<span class="sync-stat">最近：${lastSync}</span>${latestBySource.map(l=>`<span class="sync-stat ${l.status==='failed'?'danger':'success'}"><b>${l.status==='failed'?'!':'✓'}</b>${l.source==='github'?'GitHub':l.source==='gongfeng'?'工蜂':'TAPD'} ${l.status==='failed'?'异常':'正常'}</span>`).join('')}${diagText?`<span class="sync-stat danger sync-error-detail" title="${escapeHTML(diagText)}"><b>诊断</b>${escapeHTML(diagText)}</span>`:''}`;
  const project=currentProject();
  const githubButton=document.querySelector("#importGitHubIssues");if(githubButton)githubButton.disabled=!!syncing;
  const repos=(state.data.repositories||[]).filter(r=>r.projectId===state.projectFilter&&r.provider==='github');
  const repoHost=$('#syncGongfengRepos');
  if(repoHost){
    repoHost.innerHTML=repos.length
      ? `<div class="sync-repo-head">当前项目 GitHub 仓库（${escapeHTML(project?.name||state.projectFilter||'—')}）</div>`+repos.map(r=>`<div class="sync-repo-item"><b>${escapeHTML(r.name)}</b><code>${escapeHTML(r.gongfengProjectId||'—')}</code>${project?.defaultRepositoryId===r.id?'<em>默认</em>':''}</div>`).join('')
      : `<div class="sync-empty">当前项目还没有绑定 GitHub 仓库，去「项目配置」添加。</div>`;
  }
  const configPanel=$('#syncPanelConfig'),editing=configPanel&&configPanel.contains(document.activeElement);
  if(!editing){
    $('#syncGongfengURL').value=cfg.gongfengMcpUrl||'';
    $('#syncTAPDWorkspace').value=project?.tapdWorkspaceId||'';
    $('#syncTAPDIteration').value=project?.tapdIterationId||'';
    $('#syncTAPDURL').value=cfg.tapdMcpUrl||'';
    $('#syncInterval').value=cfg.autoSyncIntervalSec||300;
    $('#syncAutoEnabled').checked=cfg.autoSyncEnabled||false;
  }
  $('#syncAutoEnabledInline').checked=cfg.autoSyncEnabled||false;
  const repoNameOf=id=>{const r=(state.data.repositories||[]).find(x=>x.id===id);return r?r.name:''};
  $('#syncPushIssue').innerHTML=projectIssues.filter(i=>!i.externalRef||i.source==='local').map(i=>{const rn=repoNameOf(i.repositoryId||(i.repositoryIds||[])[0]);return `<option value="${i.id}">${rn?escapeHTML(rn)+' · ':''}${escapeHTML((i.title||'').slice(0,40))}</option>`}).join('')||'<option value="">当前项目暂无可推送项</option>';
  if(syncing){$('#syncNowBtn').textContent='⟳ 同步中…';$('#syncNowBtn').disabled=true}else{$('#syncNowBtn').textContent='⟳ 立即同步';$('#syncNowBtn').disabled=false}
  $('#syncLogList').innerHTML=scopedLogs.length?scopedLogs.slice(0,20).map(l=>`<div class="sync-log-item"><span class="sync-log-direction ${l.direction}">${l.direction==='pull'?'拉取':'推送'}</span><span class="sync-log-source">${l.source}</span><span class="sync-log-message">${escapeHTML(l.message||l.action)}</span><span class="sync-log-status ${l.status}">${l.status==='success'?'成功':'失败'}</span><span class="sync-log-time">${new Date(l.startedAt).toLocaleTimeString()}</span></div>`).join(''):'<div class="sync-empty">暂无同步记录</div>';
  renderScheduleJobs();
}
function scheduleJobRunning(job){return job?.lastStatus==='running'}
function scheduleJobStatusText(job){
  if(scheduleJobRunning(job))return '正在运行… 关闭页面不会中断';
  if(job?.lastStatus==='waiting')return `待对齐基线：${job.lastError||''}`;
  if(job?.lastStatus==='failed')return `失败：${job.lastError||''}`;
  return '';
}
function scheduleJobsInView(jobs, projectId){
  return (jobs||[]).filter(j=>!projectId||j.projectId===projectId);
}
function scheduleRunningCount(jobs, projectId){
  return scheduleJobsInView(jobs, projectId).filter(scheduleJobRunning).length;
}
function scheduleIntervalLabel(sec){
  const n=Number(sec)||0;
  if(n>=86400&&n%86400===0)return n===86400?'每天':`每 ${n/86400} 天`;
  if(n>=3600&&n%3600===0)return n===3600?'每小时':`每 ${n/3600} 小时`;
  if(n>=60&&n%60===0)return `每 ${n/60} 分钟`;
  return n?`每 ${n} 秒`:'';
}
function scheduleJobKind(job){
  return job?.skipDispatch?'assistant':'dispatch';
}
function scheduleJobTimingLabel(job){
  if(job?.cron)return job.cron;
  return scheduleIntervalLabel(job?.intervalSec||3600);
}
function scheduleSourceLabel(job){
  const source=String(job?.source||'').toLowerCase();
  if(source==='github')return '只同步 GitHub';
  if(source==='gongfeng')return '只同步工蜂';
  if(source==='tapd')return '只同步 TAPD';
  return '所有已配置来源';
}
function scheduleIssueStatusValues(job){
  const raw=(Array.isArray(job?.issueStatuses)?job.issueStatuses:[]).map(v=>String(v||'').trim()).filter(Boolean);
  if(raw.length)return raw;
  if(job?.id&&!job.skipDispatch)return ['backlog','todo','in_progress','in_review','blocked'];
  return ['backlog','todo'];
}
function scheduleModelAPI(){return window.AgentFlowAssistantModels}
function selectedClarifyModel(){
  const api=window.AgentFlowAssistantModels;
  const stored=localStorage.getItem('agentflow.clarifyModel')||'';
  return stored||(api&&api.DEFAULT)||'default';
}
function setClarifyModel(id){
  if(!id)return;
  localStorage.setItem('agentflow.clarifyModel',id);
  $$('[data-clarify-model-value]').forEach(el=>{el.value=id});
}
function clarifyModelPickerHTML(disabled){
  const id=escapeHTML(selectedClarifyModel());
  return `<div class="clarify-model-field" data-clarify-model><input type="hidden" data-clarify-model-value value="${id}"><button type="button" class="ai-model-toggle" data-model-toggle aria-haspopup="listbox" aria-expanded="false"${disabled?' disabled':''}><span class="ai-model-icon" data-family="glm">智</span><span data-model-label>选择模型</span><span class="ai-model-caret" aria-hidden="true"></span></button><div class="ai-model-menu" hidden role="listbox"></div></div>`;
}
function bindClarifyModelPicker(host){
  if(!host)return;
  const api=window.AgentFlowAssistantModels,toggle=host.querySelector('[data-model-toggle]'),menu=host.querySelector('.ai-model-menu');
  if(!api||!toggle||!menu)return;
  const paint=()=>{const keep=selectedClarifyModel();const hidden=host.querySelector('[data-clarify-model-value]');if(hidden)hidden.value=keep;api.paint(toggle,keep)};
  api.load?.().then(paint).catch(paint);
  if(!bindClarifyModelPicker.docBound){
    bindClarifyModelPicker.docBound=true;
    document.addEventListener('click',e=>{
      $$('[data-clarify-model]').forEach(el=>{
        if(el.contains(e.target))return;
        const m=el.querySelector('.ai-model-menu'),t=el.querySelector('[data-model-toggle]');
        if(m)m.hidden=true;
        if(t)t.setAttribute('aria-expanded','false');
      });
    });
  }
  if(host.dataset.bound)return;
  host.dataset.bound='1';
  toggle.onclick=e=>{
    e.stopPropagation();
    if(toggle.disabled)return;
    const open=menu.hidden;
    menu.hidden=!open;
    toggle.setAttribute('aria-expanded',open?'true':'false');
    if(open){
      api.renderList(menu, selectedClarifyModel(), id=>{
        setClarifyModel(id);
        api.paint(toggle, id);
        menu.hidden=true;
        toggle.setAttribute('aria-expanded','false');
      });
    }
  };
}
function bindScheduleModelPicker(){
  const host=$('#scheduleModelPicker');
  if(!host||host.dataset.bound)return;
  const toggle=host.querySelector('[data-model-toggle]');
  const menu=host.querySelector('.ai-model-menu');
  if(!toggle||!menu)return;
  host.dataset.bound='1';
  toggle.onclick=e=>{
    e.stopPropagation();
    const api=scheduleModelAPI();
    if(!api)return;
    const open=menu.hidden;
    menu.hidden=!open;
    toggle.setAttribute('aria-expanded',open?'true':'false');
    if(open){
      const selected=$('#scheduleJobForm').elements.assistantModel.value||api.DEFAULT;
      api.renderList(menu, selected, id=>{
        $('#scheduleJobForm').elements.assistantModel.value=id;
        api.paint(toggle, id);
        menu.hidden=true;
        toggle.setAttribute('aria-expanded','false');
      });
    }
  };
  document.addEventListener('click',e=>{
    if(host.contains(e.target))return;
    menu.hidden=true;
    toggle.setAttribute('aria-expanded','false');
  });
}
async function loadScheduleModels(){
  const api=scheduleModelAPI();
  if(!api)return;
  try{await api.load()}catch{}
  paintScheduleModel($('#scheduleJobForm')?.elements.assistantModel.value||api.DEFAULT);
}
function paintScheduleModel(selected){
  const api=scheduleModelAPI();
  const host=$('#scheduleModelPicker');
  const f=$('#scheduleJobForm');
  if(!api||!host||!f)return;
  const catalog=api.catalog?.()||{};
  const keep=selected||f.elements.assistantModel.value||catalog.default||api.DEFAULT;
  f.elements.assistantModel.value=keep;
  api.paint(host.querySelector('[data-model-toggle]'), keep);
}
function syncScheduleJobTiming(){
  const f=$('#scheduleJobForm');if(!f)return;
  const cron=f.elements.scheduleMode.value==='cron';
  f.elements.intervalSec.hidden=cron;
  f.elements.cron.hidden=!cron;
  f.querySelectorAll('[data-schedule-tab]').forEach(b=>{
    const on=b.dataset.scheduleTab===(cron?'cron':'interval');
    b.classList.toggle('is-active',on);
    b.setAttribute('aria-selected',on?'true':'false');
  });
  const hint=f.querySelector('[data-schedule-hint]');
  if(hint)hint.textContent=cron?'东八区 5 段：分 时 日 月 周。例 0 * * * * 每小时，0 9 * * 1-5 工作日 9:00。日和周都填时满足任一即执行，如 0 9 1 * 1 为每月 1 号和每周一。':'按所选间隔循环执行。';
}
function syncScheduleJobKind(){
  const f=$('#scheduleJobForm');if(!f)return;
  const kind=f.elements.jobKind.value==='assistant'?'assistant':'dispatch';
  f.elements.jobKind.value=kind;
  f.querySelectorAll('[data-schedule-kind]').forEach(el=>{el.hidden=el.dataset.scheduleKind!==kind});
  f.elements.workflowId.required=kind==='dispatch';
  f.elements.maxDispatch.disabled=kind==='assistant';
  f.elements.assistantPrompt.required=kind==='assistant'&&!f.elements.sharedTaskId?.value;
  f.elements.name.required=kind==='assistant';
  const assistant=kind==='assistant';
  $('#scheduleModalTitle').textContent=assistant?'AI 定时任务':'触发配置';
  $('#scheduleModalHint').textContent=assistant?'按计划用项目 Owner 的 CodeBuddy 执行提示词，使用与主页 AI 对话相同的完全权限，可调用 SOP、平台工具、工作区及浏览器。':'按计划扫描本项目选定状态、尚未跑过 SOP 的需求，通过基线门禁后派发 SOP。基线冲突会先由对齐 Agent 解决并合入，期间暂停派发。';
  $('#scheduleModal').querySelector('.schedule-head').hidden=assistant;
  $('#scheduleJobList').hidden=assistant||!projectTriggerJob();
  f.querySelector('[type="submit"]').textContent=assistant?'保存 AI 定时任务':'保存触发配置';
  if(assistant)paintScheduleModel(f.elements.assistantModel.value);
}
function fillScheduleJobForm(job={}){
 resetSharedScheduleVersionEditor(job);
 scheduleNotificationBindings=structuredClone(job.notifications||[]);
 mountNotificationEditor($('#scheduleNotificationEditor'),scheduleNotificationBindings,{sourceType:'schedule_job',projectId:job.projectId||state.projectFilter});
  const f=$('#scheduleJobForm');
  f.elements.id.value=job.id||'';
  f.elements.name.value=job.name||((job.id?scheduleJobKind(job):scheduleEditorKind)==='assistant'?'':'触发配置');
  const cron=String(job.cron||'').trim();
  f.elements.scheduleMode.value=cron?'cron':'interval';
  f.elements.cron.value=cron;
  const interval=String(job.intervalSec||3600);
  if(!cron&&![...f.elements.intervalSec.options].some(o=>o.value===interval)){
    const opt=document.createElement('option');opt.value=interval;opt.textContent=scheduleIntervalLabel(interval);f.elements.intervalSec.append(opt);
  }
  if(!cron)f.elements.intervalSec.value=interval;
  f.elements.jobKind.value=job.id?scheduleJobKind(job):scheduleEditorKind;
  f.elements.assistantPrompt.value=job.assistantPrompt||'';
  f.elements.assistantModel.value=job.assistantModel||scheduleModelAPI()?.DEFAULT||'default';
  f.elements.useIssueWorkflow.checked=job.id?!!job.useIssueWorkflow:true;
  f.elements.maxDispatch.value=job.maxDispatch||'';
  f.elements.enabled.checked=job.id?!!job.enabled:true;
  const source=String(job.source||'').toLowerCase();
  if(f.elements.source)f.elements.source.value=(source==='github'||source==='gongfeng'||source==='tapd')?source:'';
  const selected=new Set(scheduleIssueStatusValues(job.id?job:{}));
  f.querySelectorAll('[name="issueStatus"]').forEach(cb=>{cb.checked=selected.has(cb.value)});
  const sops=runnableWorkflowOptions();
  const preferred=job.workflowId||defaultSOPForIssue({source:'tapd',projectId:state.projectFilter})||'';
  const chosen=sops.some(w=>w.id===preferred)?preferred:(sops[0]&&sops[0].id)||'';
  f.elements.workflowId.innerHTML=(sops.length?'':`<option value="">请选择默认 SOP</option>`)+sops.map(w=>`<option value="${escapeHTML(w.id)}"${w.id===chosen?' selected':''}>${escapeHTML(w.name)}</option>`).join('');
  f.elements.workflowId.value=chosen;
  syncScheduleJobTiming();
  syncScheduleJobKind();
  fillSharedScheduleForm(job);
}
function projectTriggerJob(){return scheduleJobsInView(state.data.scheduleJobs,state.projectFilter).find(job=>!job.skipDispatch)}
function resetScheduleJobForm(){fillScheduleJobForm(scheduleEditorKind==='dispatch'?(projectTriggerJob()||{}):{})}
function closeScheduleJobModal(){
  scheduleModalVersion++;
  const modal=$('#scheduleModal');
  if(!modal.classList.contains('open'))return;
  modal.classList.remove('open');
  const editId=scheduleModalOpener?.dataset?.editSchedule;
  const replacement=editId?[...$$('#aiScheduleJobList [data-edit-schedule]')].find(el=>el.dataset.editSchedule===editId):null;
  const versionId=scheduleModalOpener?.dataset?.sharedVersions;
  const versionButton=versionId?[...$$('[data-shared-versions]')].find(el=>el.dataset.sharedVersions===versionId):null;
  const sharedId=scheduleModalOpener?.dataset?.subscribeShared;
  const marketButton=sharedId?[...$$('[data-subscribe-shared]')].find(el=>el.dataset.subscribeShared===sharedId):null;
  (replacement||versionButton||marketButton||(scheduleModalOpener?.isConnected?scheduleModalOpener:null)||$('#newAISchedule'))?.focus();
}
async function openScheduleJobModal(kind='dispatch',job){
  if(!state.projectFilter)return toast('请先选择项目');
  const version=++scheduleModalVersion,projectId=state.projectFilter;
  scheduleEditorKind=kind==='assistant'?'assistant':'dispatch';
  scheduleModalOpener=document.activeElement;
  if(scheduleModalOpener?.closest('#issueHeadingMenu'))scheduleModalOpener=$('#issueHeadingMore');
  const f=$('#scheduleJobForm');
  f.inert=false;
  f.querySelector('[type="submit"]').disabled=false;
  if(scheduleEditorKind==='assistant')fillScheduleJobForm(job||{});
  else resetScheduleJobForm();
  $('#scheduleModal').classList.add('open');
  if(scheduleEditorKind==='assistant'){
    f.elements.name.focus();
    await Promise.all([refreshAISchedules(),loadScheduleModels(),loadSharedScheduleEditor(job||{},version,projectId)]);
  }else{
    // Load the saved project config before allowing edits or a first save.
    f.inert=true;
    f.querySelector('[type="submit"]').disabled=true;
    try{
      const jobs=await api('/api/schedule-jobs');
      if(version!==scheduleModalVersion||projectId!==state.projectFilter)return;
      state.data.scheduleJobs=jobs||[];
      resetScheduleJobForm();
      renderScheduleJobs();
    }catch(err){
      if(version===scheduleModalVersion){toast(err.message,'err');closeScheduleJobModal()}
    }finally{
      if(version===scheduleModalVersion){
        f.inert=false;f.querySelector('[type="submit"]').disabled=false;
        (f.elements.scheduleMode.value==='cron'?f.elements.cron:f.elements.intervalSec).focus();
      }
    }
  }
}
async function refreshAISchedules(){
  try{
    const jobs=await api('/api/schedule-jobs',{silent:true});
    state.data.scheduleJobs=jobs||[];
    await refreshSharedScheduleCatalog().catch(()=>{});
    renderScheduleJobs();
  }catch(err){
    $('#aiScheduleRuntimeHint').textContent=`读取 AI 定时任务失败：${err.message}`;
    toast(err.message,'err');
  }
}
function findAlignRun(runId){return (state.data.runs||[]).find(r=>r.id===runId)}
function findAlignBlocks(runId){for(const j of state.data.scheduleJobs||[]){for(const b of j.blockers||[]){if(b.alignRunId===runId&&(b.conflictBlocks||[]).length)return b.conflictBlocks}}return []}
function conflictBlockHTML(b,idx){
  const ours=(b.ours||'').trim(),theirs=(b.theirs||'').trim();
  return `<div class="align-conflict-block"><div class="align-conflict-file">冲突块 ${idx+1} · <code>${escapeHTML(b.file)}</code></div><div class="align-conflict-sides"><div class="align-conflict-side ours"><b>集成分支侧（ours）</b><pre>${escapeHTML(ours||'（空）')}</pre></div><div class="align-conflict-side theirs"><b>默认分支侧（theirs）</b><pre>${escapeHTML(theirs||'（空）')}</pre></div></div></div>`;
}
function alignConflictPanelHTML(r){
  const blocks=findAlignBlocks(r.id);
  const resolveOut=r.nodes?.resolve?.output;
  const action=r.status==='waiting'?`<button class="button primary small" data-realign-run="${escapeHTML(r.id)}">让 Agent 重新对齐</button>`:r.status==='running'?'<span class="align-hint">Agent 执行中…</span>':'';
  if(!blocks.length&&!resolveOut)return '';
  return `<section class="align-conflicts"><div class="section-heading"><div><h3>基线冲突详情</h3><span>守卫提取的冲突块（左右对照）。Agent 解完并校验通过后会自动合入基线，不用人工确认</span></div>${action}</div>${blocks.map(conflictBlockHTML).join('')||''}${resolveOut?`<details class="align-resolve-out"><summary>Agent 的冲突处理说明（resolve 节点输出）</summary><pre>${escapeHTML(resolveOut)}</pre></details>`:''}</section>`;
}
async function realignBaseline(runId){
  if(!await afConfirm({title:'基线对齐确认',message:'让 Agent 重新执行基线对齐？会重新合并默认分支并解决全部冲突（verify 校验不通过会自动打回重做），通过后自动合入基线。',confirmText:'开始对齐'}))return;
  try{
    const run=findAlignRun(runId)||{};
    const waitingConfirm=run.nodes?.confirm?.status==='waiting_approval';
    if(waitingConfirm){
      await api(`/api/runs/${encodeURIComponent(runId)}/reject`,{method:'POST',body:JSON.stringify({nodeId:'confirm',targetId:'resolve',reason:'人工触发重新对齐',context:'请直接执行 git merge origin/master 并逐块解决全部冲突后提交；verify 节点会校验 HEAD 必须是合并提交，只输出计划会被再次打回'})});
    }else{
      await api(`/api/runs/${encodeURIComponent(runId)}/retry`,{method:'POST',body:JSON.stringify({nodeId:'resolve',reason:'人工触发重新对齐',context:'请直接执行 git merge origin/master 并逐块解决全部冲突后提交；verify 节点会校验 HEAD 必须是合并提交，只输出计划会被再次打回'})});
    }
    toast('已触发重新对齐，Agent 开始执行','ok');
    startLiveRun(runId);
    await refreshRuns();
  }catch(e){toast(e.message,'err')}
}
function scheduleBlockerHTML(j){
  const blockers=Array.isArray(j.blockers)?j.blockers:[];
  if(j.lastStatus!=='waiting'||!blockers.length)return '';
  const rows=blockers.map(b=>{
    const since=b.since?` · 已暂停 ${relativeTime(b.since)}`:'';
    const files=Array.isArray(b.conflictFiles)?b.conflictFiles:[];
    const branches=`${escapeHTML(b.defaultBranch||'默认分支')} → ${escapeHTML(b.integrateBranch||'集成分支')}`;
    const blocks=Array.isArray(b.conflictBlocks)?b.conflictBlocks:[];
    const run=blockers&&b.alignRunId?findAlignRun(b.alignRunId):null;
    const detail=blocks.length?`<details class="schedule-blocker-detail"><summary>展开 ${blocks.length} 个冲突块</summary>${blocks.map(conflictBlockHTML).join('')}</details>`:(files.length?`<small>冲突文件：${escapeHTML(files.slice(0,5).join('、'))}${files.length>5?' 等':''}</small>`:'');
    let action='';
    if(b.alignRunId){
      if(run&&run.status==='waiting')action=`<button type="button" class="button primary small" data-realign-run="${escapeHTML(b.alignRunId)}">一键重新对齐</button>`;
      else if(run&&run.status==='running')action='<span class="align-hint">Agent 执行中…</span>';
    }
    return `<div class="schedule-blocker-row"><div class="schedule-blocker-text"><b>自动派发已暂停</b><span>${escapeHTML(b.repositoryName||b.repositoryId||'仓库')}（${branches}）${since}</span>${detail}</div><div class="schedule-blocker-actions">${action}${b.alignRunId?`<button type="button" class="button ghost small" data-open-align-run="${escapeHTML(b.alignRunId)}">查看对齐 Run</button>`:''}</div></div>`;
  }).join('');
  return `<div class="schedule-blocker-card">${rows}</div>`;
}
function renderScheduleJobs(){
  const scoped=scheduleJobsInView(state.data.scheduleJobs,state.projectFilter);
  const dispatch=scoped.filter(j=>!j.skipDispatch),assistant=state.projectFilter?scoped.filter(j=>j.skipDispatch):[];
  const canRun=state.data.schedulerEnabled!==false,count=$('#scheduleJobCount');
  if(count){count.textContent=!dispatch.length?'未配置':scheduleJobRunning(dispatch[0])?'运行中':dispatch[0].enabled?'已启用':'未启用';count.classList.toggle('is-running',dispatch.some(scheduleJobRunning))}
  const disabled='本实例不执行定时任务；任务可在主实例自动运行。';
  $('#scheduleRuntimeHint').textContent=canRun?'每个项目使用一份触发配置，修改后保存即可生效。立即运行在服务端执行，关闭页面不会中断。':disabled;
  $('#aiScheduleRuntimeHint').textContent=!state.projectFilter?'请先在顶栏选择项目，再管理 AI 定时任务。':canRun?'使用项目 Owner 的 CodeBuddy，以完全权限执行，与主页 AI 对话相同。可调用 SOP、工作区及浏览器工具；关闭页面不会中断执行。':disabled;
  $('#aiScheduleProjectName').textContent=currentProject()?.name||'当前项目';
  $('#aiScheduleCount').textContent=`${assistant.length} 个任务`;
  $('#newAISchedule').disabled=!state.projectFilter;
  $('#scheduleJobList').hidden=$('#scheduleJobForm').elements.jobKind.value==='assistant'||!dispatch.length;
  renderScheduleJobList($('#scheduleJobList'),dispatch,'dispatch',canRun);
  renderScheduleJobList($('#aiScheduleJobList'),assistant,'assistant',canRun);
  renderAISchedulePanels();
  startScheduleJobPoll();
}
function renderScheduleJobList(list,jobs,kind,canRun){
  if(!list)return;
  const focused=document.activeElement;
  const focusAction=['editSchedule','runSchedule','deleteSchedule'].find(key=>focused?.dataset?.[key]);
  const focusId=list.contains(focused)&&focusAction?focused.dataset[focusAction]:null;
  if(kind==='dispatch'){
    const job=jobs[0];
    list.innerHTML=job?`<div class="schedule-config-runtime"><div><b>${job.enabled?'已启用':'未启用'} · ${escapeHTML(scheduleJobTimingLabel(job))}</b><span>${escapeHTML(scheduleSourceLabel(job))} · ${scheduleIssueStatusValues(job).map(issueStatusLabel).join('、')}</span><small>${knownTime(job.lastRunAt)?`最近：${knownTime(job.lastRunAt).toLocaleString()} · 候选 ${job.lastPulled||0}，启动 ${job.lastDispatched||0}`:'尚未运行'}${scheduleJobStatusText(job)?` · ${escapeHTML(scheduleJobStatusText(job))}`:''}</small></div><button type="button" class="button ghost small" data-run-schedule="${escapeHTML(job.id)}" ${scheduleJobRunning(job)||!canRun?'disabled':''}>${scheduleJobRunning(job)?'运行中…':'立即运行'}</button></div>${scheduleBlockerHTML(job)}`:'';
  }else list.innerHTML=jobs.map(j=>{
    const assistant=!!j.skipDispatch;
    const kind=assistant?'AI':'触发';
    const running=scheduleJobRunning(j);
    const extra=running?'':assistant?(j.lastAssistantMessage?`，${escapeHTML(String(j.lastAssistantMessage).slice(0,80))}`:''):`，${scheduleSourceLabel(j)}，${scheduleIssueStatusValues(j).map(issueStatusLabel).join('、')}，候选 ${j.lastPulled||0}，启动 ${j.lastDispatched||0}`;
    const statusText=scheduleJobStatusText(j);
    const status=statusText?`，${escapeHTML(statusText)}`:'';
    const runDisabled=running||!canRun;
    const runTitle=running?'任务正在服务端运行，关闭页面不会中断':(canRun?'':'本实例未启用 scheduler');
    return `<div class="schedule-job-item${running?' is-running':''}"><div class="schedule-job-row"><button type="button" class="schedule-job-main" data-edit-schedule="${escapeHTML(j.id)}" title="${assistant?'编辑 AI 定时任务':'编辑触发配置'}"><b><span class="schedule-kind-tag ${assistant?'is-assistant':'is-dispatch'}">${kind}</span>${escapeHTML(j.name)}</b>${sharedScheduleRowInfo(j)}<span>${escapeHTML(scheduleJobTimingLabel(j))} · ${j.enabled?'已启用':'已停用'}${assistant?'':` · 单次最多 ${escapeHTML(j.maxDispatch||10)} 个需求`}${running?' · 正在运行':''}</span><small class="${running?'schedule-run-live':''}">${(j.lastRunAt&&!String(j.lastRunAt).startsWith('0001-'))||running?`最近：${new Date(j.lastRunAt||Date.now()).toLocaleString()}${extra}${status}`:'尚未运行'}</small></button>${sharedScheduleRowAction(j)}<button type="button" class="button ghost small" data-edit-schedule="${escapeHTML(j.id)}">编辑</button><button type="button" class="button ghost small" data-run-schedule="${escapeHTML(j.id)}" ${runDisabled?'disabled':''} ${runTitle?`title="${escapeHTML(runTitle)}"`:''}>${running?'运行中…':'立即运行'}</button><button type="button" class="button ghost small danger" data-delete-schedule="${escapeHTML(j.id)}">删除</button></div>${assistant?'':scheduleBlockerHTML(j)}</div>`;
  }).join('')||'<div class="sync-empty">当前项目还没有 AI 定时任务，点击右上角「新建 AI 定时任务」。</div>';
  list.querySelectorAll('[data-edit-schedule]').forEach(b=>b.onclick=()=>{
    const job=(state.data.scheduleJobs||[]).find(j=>j.id===b.dataset.editSchedule);
    if(!job)return;
    if(job.skipDispatch)openScheduleJobModal('assistant',job);
    else fillScheduleJobForm(job);
  });
  bindSharedScheduleRowActions(list);
  list.querySelectorAll('[data-run-schedule]').forEach(b=>b.onclick=()=>runScheduleJob(b.dataset.runSchedule));
  list.querySelectorAll('[data-delete-schedule]').forEach(b=>b.onclick=()=>deleteScheduleJob(b.dataset.deleteSchedule));
  list.querySelectorAll('[data-open-align-run]').forEach(b=>b.onclick=()=>openRunFromId(b.dataset.openAlignRun,{forceRuns:true}));
  list.querySelectorAll('[data-realign-run]').forEach(b=>b.onclick=()=>realignBaseline(b.dataset.realignRun));
  if(focusId){const next=[...list.querySelectorAll('[data-edit-schedule],[data-run-schedule],[data-delete-schedule]')].find(el=>el.dataset[focusAction]===focusId);next?.focus()}

}
async function saveScheduleJob(e){
  e.preventDefault();
  const f=e.currentTarget,cron=f.elements.scheduleMode.value==='cron',assistant=f.elements.jobKind.value==='assistant';
  const payload={
    id:f.elements.id.value,
    name:f.elements.name.value.trim()||(assistant?'':'触发配置'),
    projectId:state.projectFilter||'platform',
    skipDispatch:assistant,
    notifications:assistant?notificationPayload(scheduleNotificationBindings):[],
    workflowId:assistant?'':f.elements.workflowId.value,
    useIssueWorkflow:assistant?false:!!f.elements.useIssueWorkflow.checked,
    source:assistant?'':(f.elements.source?f.elements.source.value:''),
    issueStatuses:assistant?[]:[...f.querySelectorAll('[name="issueStatus"]:checked')].map(cb=>cb.value),
    maxDispatch:assistant||f.elements.maxDispatch.value.trim()===''?undefined:Number(f.elements.maxDispatch.value),
    intervalSec:cron?0:Number(f.elements.intervalSec.value)||3600,
    cron:cron?f.elements.cron.value.trim():'',
    assistantPrompt:assistant?f.elements.assistantPrompt.value.trim():'',
    assistantModel:assistant?f.elements.assistantModel.value.trim():'',
    enabled:f.elements.enabled.checked
  };
  if(assistant&&typeof sharedScheduleFormPayload==='function')Object.assign(payload,sharedScheduleFormPayload(f));
  if(!payload.name)return toast('请填写任务名称');
  if(assistant&&!payload.assistantPrompt&&!payload.sharedTaskId)return toast('请填写 AI 定时任务的提示词');
  if(!assistant&&!payload.workflowId)return toast('请选择默认 SOP');
  if(!assistant&&!payload.issueStatuses.length)return toast('请至少选择一个触发状态');
  if(!assistant&&payload.maxDispatch!==undefined&&(!Number.isInteger(payload.maxDispatch)||payload.maxDispatch<1||payload.maxDispatch>10))return toast('单次最多触发需求数必须为 1–10，或留空使用默认值 10');
  if(cron&&!payload.cron)return toast('请填写 Cron 表达式');
  const version=scheduleModalVersion,projectId=state.projectFilter,button=f.querySelector?.('[type="submit"]'),label=button?.textContent;
  if(button?.disabled)return;
  if(button){button.disabled=true;button.textContent='保存中…'}
  try{
    const saved=await api('/api/schedule-jobs',{method:payload.id?'PUT':'POST',body:JSON.stringify(payload)}),jobs=state.data.scheduleJobs||(state.data.scheduleJobs=[]),idx=jobs.findIndex(j=>j.id===saved.id);
    if(idx>=0)jobs[idx]=saved;else jobs.push(saved);
    renderScheduleJobs();
    if(version===scheduleModalVersion&&projectId===state.projectFilter){fillScheduleJobForm(saved);closeScheduleJobModal()}
    toast(assistant?'AI 定时任务已保存':'触发配置已保存');
  }catch(err){toast(err.message,'err')}
  finally{if(button&&version===scheduleModalVersion){button.disabled=false;button.textContent=label}}
}
function scheduleJobOutcomeToast(job){
  if(!job)return;
  if(job.skipDispatch){
    toast(job.lastStatus==='failed'?`失败：${job.lastError||''}`:'AI 定时任务已执行', job.lastStatus==='failed'?'err':'ok', 4200);
    return;
  }
  if(job.lastStatus==='waiting'){
    toast(`待对齐基线：${job.lastError||'请先把默认分支合入集成分支'}`, 'info', 4200);
    return;
  }
  if(job.lastStatus==='failed'){
    toast(`失败：${job.lastError||''}`, 'err', 4200);
    return;
  }
  toast(`完成：候选 ${job.lastPulled||0}，启动 ${job.lastDispatched||0}`, 'ok', 4200);
}
function noteScheduleJobOutcomes(prev, next){
  const prevMap=Object.fromEntries((prev||[]).map(j=>[j.id,j]));
  let finished=false;
  for(const job of next||[]){
    const was=prevMap[job.id];
    if(was&&scheduleJobRunning(was)&&!scheduleJobRunning(job)){
      scheduleJobOutcomeToast(job);
      finished=true;
    }
  }
  if(finished)refreshIssues();
}
let scheduleJobPollTimer=null;
function scheduleJobPollSignature(jobs, projectId){
  return scheduleJobsInView(jobs, projectId).map(j=>[j.id,j.lastStatus,j.lastError||'',j.lastAssistantMessage||'',j.lastPulled||0,j.lastDispatched||0].join(':')).join('|');
}
function startScheduleJobPoll(){
  const running=scheduleRunningCount(state.data.scheduleJobs, state.projectFilter)>0;
  if(!running){
    if(scheduleJobPollTimer){clearInterval(scheduleJobPollTimer);scheduleJobPollTimer=null}
    return;
  }
  if(scheduleJobPollTimer)return;
  scheduleJobPollTimer=setInterval(async()=>{
    if(startScheduleJobPoll.checking)return;
    startScheduleJobPoll.checking=true;
    try{
      const prev=state.data.scheduleJobs||[];
      const jobs=await api('/api/schedule-jobs',{silent:true});
      const next=jobs||[];
      state.data.scheduleJobs=next;
      noteScheduleJobOutcomes(prev, next);
      if(scheduleRunningCount(next, state.projectFilter)===0){
        if(scheduleJobPollTimer){clearInterval(scheduleJobPollTimer);scheduleJobPollTimer=null}
      }
      if(scheduleJobPollSignature(prev, state.projectFilter)!==scheduleJobPollSignature(next, state.projectFilter)){
        renderScheduleJobs();
      }
    }catch(e){}
    finally{startScheduleJobPoll.checking=false}
  },2000);
}
async function runScheduleJob(id){
  const jobs=state.data.scheduleJobs||[];
  const idx=jobs.findIndex(j=>j.id===id);
  if(idx>=0){
    jobs[idx]={...jobs[idx], lastStatus:'running', lastError:'', lastRunAt:new Date().toISOString()};
    renderScheduleJobs();
  }
  startScheduleJobPoll();
  try{
    toast('已在服务端启动，关闭页面不会中断');
    const saved=await api(`/api/schedule-jobs/${encodeURIComponent(id)}/run`,{method:'POST',body:'{}'});
    const at=(state.data.scheduleJobs||[]).findIndex(j=>j.id===saved.id);
    if(at>=0)state.data.scheduleJobs[at]=saved;else (state.data.scheduleJobs||[]).push(saved);
    renderScheduleJobs();
    startScheduleJobPoll();
  }catch(err){
    toast(err.message,'err');
    await refreshSyncStatus();
  }
}
async function deleteScheduleJob(id){
  const job=(state.data.scheduleJobs||[]).find(j=>j.id===id);
  const label=job?.skipDispatch?'AI 定时任务':'触发配置';
  if(!await afConfirm({title:`删除${label}`,message:`确定删除这个${label}吗？此操作不可恢复。`,confirmText:'确认删除',isDanger:true}))return;
  try{
    await api(`/api/schedule-jobs/${encodeURIComponent(id)}`,{method:'DELETE'});
    state.data.scheduleJobs=(state.data.scheduleJobs||[]).filter(j=>j.id!==id);
    resetScheduleJobForm();renderScheduleJobs();toast(`${label}已删除`);
  }catch(err){toast(err.message)}
}
async function refreshSyncStatus(){try{const wasSyncing=!!state.data.syncing,[status,jobs]=await Promise.all([api('/api/sync/status',{silent:true}),api('/api/schedule-jobs',{silent:true})]);state.data.syncLogs=status.logs||[];state.data.syncing=status.syncing||false;state.data.syncConfig=status.config||state.data.syncConfig;state.data.scheduleJobs=jobs||[];renderSync();if(wasSyncing&&!state.data.syncing)await refreshIssues()}catch(e){if(!state.data.syncing)$('#syncBadge').textContent='连接异常'}}
async function syncNow(){try{$('#syncNowBtn').textContent='⟳ 同步中…';$('#syncNowBtn').disabled=true;state.data.syncing=true;renderSync();await api('/api/sync/now',{method:'POST',body:'{}'});toast('同步已启动…');let attempts=0,checking=false;const poll=setInterval(async()=>{if(checking)return;checking=true;attempts++;try{const status=await api('/api/sync/status');state.data.syncLogs=status.logs||[];state.data.syncing=status.syncing||false;state.data.syncConfig=status.config||state.data.syncConfig;if(!state.data.syncing){clearInterval(poll);const issues=await api('/api/issues');state.data.issues=issues;renderIssues();renderSync();const scoped=state.data.syncLogs.filter(l=>!state.projectFilter||!l.projectId||l.projectId===state.projectFilter);const failed=['github','gongfeng','tapd'].map(source=>scoped.find(l=>l.source===source)).filter(l=>l&&l.status==='failed');toast(failed.length?`同步未完全成功：${failed.map(l=>l.source==='github'?'GitHub':l.source==='gongfeng'?'工蜂':'TAPD').join('、')}，详见诊断`:`同步完成：已刷新外部工作项`)}else if(attempts>=80){clearInterval(poll);renderSync();toast('同步仍在后台执行，可稍后查看结果')}}catch(e){if(attempts>=80){clearInterval(poll);state.data.syncing=false;renderSync();toast(`同步状态检查失败：${e.message}`)}}finally{checking=false}},1500)}catch(e){toast(e.message);state.data.syncing=false;renderSync()}}
async function saveSyncConfig(){
  const project=currentProject();
  const tapdWs=$('#syncTAPDWorkspace').value.trim(),tapdIt=$('#syncTAPDIteration').value.trim();
  const cfg={
    gongfengProjectId:state.data.syncConfig?.gongfengProjectId||'',
    gongfengMcpUrl:$('#syncGongfengURL').value.trim(),
    tapdMcpUrl:$('#syncTAPDURL').value.trim(),
    autoSyncIntervalSec:Number($('#syncInterval').value)||300,
    autoSyncEnabled:$('#syncAutoEnabled').checked
  };
  try{
    const saved=await api('/api/sync/config',{method:'PUT',body:JSON.stringify(cfg)});
    state.data.syncConfig=saved;
    $('#syncAutoEnabledInline').checked=saved.autoSyncEnabled;
    if(project){
      const updated=await api('/api/projects',{method:'PUT',body:JSON.stringify({...project,tapdWorkspaceId:tapdWs,tapdIterationId:tapdIt})});
      const idx=(state.data.projects||[]).findIndex(p=>p.id===updated.id);
      if(idx>=0)state.data.projects[idx]=updated;
    }
    renderSync();
    toast('同步配置已保存');
  }catch(e){toast(e.message)}
}
async function pushIssue(target){const issueId=$('#syncPushIssue').value;if(!issueId)return toast('请先选择一个本地工作项');try{const updated=await api('/api/sync/create',{method:'POST',body:JSON.stringify({issueId,target})});const idx=state.data.issues.findIndex(i=>i.id===issueId);if(idx>=0)state.data.issues[idx]=updated;renderIssues();toast(target==='gongfeng'?`已在工蜂创建 Issue #${updated.externalRef?.externalIid}`:`已在 TAPD 创建需求 ${updated.externalRef?.externalId}`)}catch(e){toast(e.message)}}
