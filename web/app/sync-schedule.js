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
function clarifyModelPickerHTML(disabled){
  return `<div class="feature-model-picker clarify-model-picker" data-clarify-model ${disabled?'data-picker-readonly':''}>${scheduleModelAPI()?.featurePickerHTML()||''}</div>`;
}
let clarifyModelChoice;
function bindClarifyModelPicker(host, issue){
  const models=scheduleModelAPI();
  return models?.bindFeaturePicker(host,{
    model:clarifyModelChoice?.issueId===issue.id?clarifyModelChoice.model:'',
    legacyModel:localStorage.getItem('agentflow.clarifyModel'),
    readOnly:host.hasAttribute('data-picker-readonly'),
    onChange:model=>{clarifyModelChoice={issueId:issue.id,model}},
    loadCatalog:()=>models.fetchCatalog(`/api/issues/${encodeURIComponent(issue.id)}/clarify-models`)
  });
}
// The AI 定时任务 model picker: a CLI select narrows the model list to that
// CLI, like the chat composer's CLI switch. The menu is fixed-positioned so the
// modal's scroll area cannot clip it, and opens toward the side with room.
let scheduleCatalog=null;
function scheduleModelCatalog(){
  return scheduleCatalog||scheduleModelAPI()?.catalog?.()||{};
}
function scheduleEngineModels(engine){
  return (scheduleModelCatalog().models||[]).filter(m=>(m.engine||'codebuddy')===engine);
}
function scheduleDefaultModel(engine){
  const models=scheduleEngineModels(engine);
  const prefs=scheduleModelCatalog().preferences;
  const saved=prefs?.executor===engine&&models.find(item=>item.id===prefs.model||item.id===engine+':'+prefs.model);
  const preferred=engine==='codebuddy'&&models.find(item=>item.id===scheduleModelCatalog().default);
  return saved?.id||preferred?.id||models[0]?.id||'';
}
function scheduleModelEngine(id){
  return scheduleModelAPI()?.engineOf?.(id,scheduleModelCatalog())||'codebuddy';
}
function paintScheduleEngines(selected){
  const select=$('#scheduleEngine');
  if(!select)return;
  const current=scheduleModelEngine(selected);
  const engines=scheduleModelCatalog().engines||[];
  const list=engines.filter(engine=>engine.configured||scheduleEngineModels(engine.id).length);
  if(!list.some(e=>e.id===current)&&(scheduleModelAPI()?.concreteModelId(selected)||$('#scheduleJobForm')?.elements.id.value)){
    list.push(engines.find(e=>e.id===current)||{id:current,name:current});
  }
  // CodeBuddy can run on its native default with no concrete model in the
  // list. A saved task keeps its CLI selectable during edits.
  select.replaceChildren(...list.map(e=>{
    const usable=e.id===current||(e.id==='codebuddy'&&e.configured)||scheduleEngineModels(e.id).length>0;
    const option=new Option(usable?e.name:`${e.name}（缺少凭据）`,e.id);
    option.disabled=!usable;
    return option;
  }));
  if(!list.length)select.add(new Option('暂无可用执行器',''));
  select.value=list.some(item=>item.id===current)?current:list[0]?.id||'';
  select.disabled=!list.length;
}
function closeScheduleModelMenu(){
  const host=$('#scheduleModelPicker');
  if(!host)return;
  host.querySelector('.ai-model-menu').hidden=true;
  host.querySelector('[data-model-toggle]').setAttribute('aria-expanded','false');
}
// Call with the menu shown: a short list must sit against its toggle, so the
// placement uses the rendered height, not the maximum.
function placeScheduleModelMenu(toggle,menu){
  const rect=toggle.getBoundingClientRect();
  const width=Math.min(Math.max(rect.width,320),window.innerWidth-24);
  menu.style.width=width+'px';
  menu.style.left=Math.max(12,Math.min(rect.left,window.innerWidth-width-12))+'px';
  menu.style.maxHeight='none';
  const need=Math.min(320,menu.scrollHeight,window.innerHeight-24);
  const below=window.innerHeight-rect.bottom-12,above=rect.top-12;
  const down=below>=need||below>=above;
  const room=Math.min(need,down?below:above);
  menu.style.maxHeight=room+'px';
  menu.style.top=(down?rect.bottom+6:Math.max(12,rect.top-room-6))+'px';
}
function bindScheduleModelPicker(){
  const host=$('#scheduleModelPicker');
  if(!host||host.dataset.bound)return;
  const toggle=host.querySelector('[data-model-toggle]');
  const menu=host.querySelector('.ai-model-menu');
  if(!toggle||!menu)return;
  host.dataset.bound='1';
  const form=$('#scheduleJobForm');
  toggle.onclick=e=>{
    e.stopPropagation();
    const api=scheduleModelAPI();
    if(!api)return;
    const open=menu.hidden;
    closeScheduleModelMenu();
    if(!open)return;
    const selected=form.elements.assistantModel.value||api.DEFAULT;
    api.renderOptions(menu, scheduleEngineModels(scheduleModelEngine(selected)), selected, id=>{
      paintScheduleModel(id);
      closeScheduleModelMenu();
    });
    menu.hidden=false;
    placeScheduleModelMenu(toggle,menu);
    toggle.setAttribute('aria-expanded','true');
  };
  const engine=$('#scheduleEngine');
  if(engine)engine.onchange=()=>{
    const api=scheduleModelAPI();
    const next=scheduleDefaultModel(engine.value)||(engine.value==='codebuddy'?(scheduleModelCatalog().default||api?.DEFAULT):'');
    if(next)paintScheduleModel(next);
  };
  document.addEventListener('click',e=>{if(!host.contains(e.target))closeScheduleModelMenu()});
  document.addEventListener('keydown',e=>{if(e.key==='Escape')closeScheduleModelMenu()});
  // A fixed menu would drift from its toggle; close it when anything else scrolls.
  document.addEventListener('scroll',e=>{if(!menu.contains(e.target))closeScheduleModelMenu()},true);
  window.addEventListener('resize',closeScheduleModelMenu);
}
async function loadScheduleModels(){
  const picker=scheduleModelAPI();
  if(!picker)return;
  const version=scheduleModalVersion;
  let catalog;
  try{
    catalog=await api('/api/assistant/models?schedule=1',{silent:true});
  }catch{
    catalog=await picker.load();
  }
  if(version!==scheduleModalVersion)return;
  scheduleCatalog=catalog;
  const form=$('#scheduleJobForm');
  const selected=form.elements.assistantModel.value;
  const configured=(catalog?.engines||[]).filter(engine=>engine.configured);
  const preferred=configured.find(engine=>engine.id===catalog.preferences?.executor);
  const fallback=!form.elements.id.value&&!picker.concreteModelId(selected)
    ?(preferred&&scheduleDefaultModel(preferred.id))||configured.map(engine=>scheduleDefaultModel(engine.id)).find(Boolean)
    :'';
  paintScheduleModel(fallback||selected||picker.DEFAULT);
}
function paintScheduleModel(selected){
  const api=scheduleModelAPI();
  const host=$('#scheduleModelPicker');
  const f=$('#scheduleJobForm');
  if(!api||!host||!f)return;
  const catalog=scheduleModelCatalog();
  const keep=selected||f.elements.assistantModel.value||catalog.default||api.DEFAULT;
  f.elements.assistantModel.value=keep;
  const toggle=host.querySelector('[data-model-toggle]');
  const item=catalog.models?.find(model=>model.id===keep);
  if(item)api.paintItem(toggle,item);
  else api.paint(toggle,keep);
  paintScheduleEngines(keep);
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
  f.elements.timeoutMinutes.disabled=!assistant;
  $('#scheduleModalTitle').textContent=assistant?'AI 定时任务':'触发配置';
  $('#scheduleModalHint').textContent=assistant?'按计划以保存人的身份和完全权限执行提示词，可调用 SOP、平台工具、工作区及浏览器。':'按计划扫描本项目选定状态、尚未跑过 SOP 的需求，通过基线门禁后派发 SOP。基线冲突会先由对齐 Agent 解决并合入，期间暂停派发。';
  $('#scheduleModal').querySelector('.schedule-head').hidden=assistant;
  $('#scheduleJobList').hidden=assistant||!projectTriggerJob();
  f.querySelector('[type="submit"]').textContent=assistant?'保存 AI 定时任务':'保存触发配置';
  const enabledField=f.elements.enabled?.closest('label');if(enabledField)enabledField.hidden=assistant;
  if(assistant)paintScheduleModel(f.elements.assistantModel.value);
}
// The pinned conversation is chosen by the server on the first fixed run.
// Only jobs that open a conversation per execution choose how many to keep.
const SCHEDULE_SESSION_KEEP_CHOICES=['10','50','100','-1'];
function fillScheduleSessionMode(f,job){
  const select=f.elements.sessionMode;if(!select)return;
  select.value=job.sessionMode==='fixed'?'fixed':'';
  const keep=f.elements.sessionKeep;
  if(keep){const v=String(job.sessionKeep||50);keep.value=SCHEDULE_SESSION_KEEP_CHOICES.includes(v)?v:'50'}
  scheduleSessionModeHint(job);
  select.onchange=()=>scheduleSessionModeHint(job);
}
function scheduleSessionModeHint(job){
  const f=$('#scheduleJobForm'),hint=$('#scheduleSessionHint');if(!f||!hint)return;
  const fixed=f.elements.sessionMode.value==='fixed';
  const keepField=$('#scheduleSessionKeepField');if(keepField)keepField.hidden=fixed;
  const pinned=fixed&&job.sessionMode==='fixed'&&job.fixedSessionId&&job.projectId===state.projectFilter?job.fixedSessionId:'';
  if(!fixed){hint.textContent='每次执行各开一个会话，可在执行记录里打开。';return}
  hint.innerHTML=pinned
    ?`每次执行都接着同一个会话，带上之前的上下文。<a href="${escapeHTML(scheduleExecConversationURL({projectId:job.projectId,sessionId:pinned}))}" target="_blank" rel="noopener" data-open-fixed-session>打开当前会话 ↗</a>`
    :'每次执行都接着同一个会话，带上之前的上下文。首次执行时创建这个会话；会话被删除后会重新创建。';
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
  f.elements.timeoutMinutes.value=job.timeoutMinutes||'';
  fillScheduleSessionMode(f,job);
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
  f.modelPickerReady=null;
  f.inert=false;
  f.querySelector('[type="submit"]').disabled=false;
  if(scheduleEditorKind==='assistant')fillScheduleJobForm(job||{});
  else resetScheduleJobForm();
  $('#scheduleModal').classList.add('open');
  if(scheduleEditorKind==='assistant'){
    f.elements.name.focus();
    f.modelPickerReady=loadScheduleModels();
    await Promise.all([refreshAISchedules(),f.modelPickerReady,loadSharedScheduleEditor(job||{},version,projectId)]);
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
    refreshScheduleExecutions().catch(()=>{});
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
  const triggerHint=$('#aiScheduleTriggerHint');if(triggerHint)triggerHint.textContent=canRun?'按计划扫描本项目选定状态、尚未跑过 SOP 的需求，通过基线门禁后派发 SOP。每个项目使用一份触发配置。':disabled;
  $('#aiScheduleRuntimeHint').textContent=!state.projectFilter?'请先在顶栏选择项目，再管理 AI 定时任务。':canRun?'任务以最后保存人的身份和完全权限执行。关闭页面后，任务会继续运行。':disabled;
  $('#aiScheduleProjectName').textContent=currentProject()?.name||'当前项目';
  $('#aiScheduleCount').textContent=`${assistant.length} 个任务`;
  $('#newAISchedule').disabled=!state.projectFilter;
  const editTrigger=$('#editScheduleTrigger');if(editTrigger){editTrigger.disabled=!state.projectFilter;editTrigger.lastChild.textContent=dispatch.length?'编辑触发配置':'配置触发'}
  const triggerList=$('#aiScheduleTriggerList');
  $('#scheduleJobList').hidden=$('#scheduleJobForm').elements.jobKind.value==='assistant'||!dispatch.length;
  renderScheduleJobList($('#scheduleJobList'),dispatch,'dispatch',canRun);
  renderScheduleJobList(triggerList,dispatch,'dispatch',canRun);
  if(triggerList&&!dispatch.length)triggerList.innerHTML=`<div class="sync-empty">${state.projectFilter?'当前项目还没有触发配置，点击「配置触发」设置自动派发。':'请先在顶栏选择项目，再配置 SOP 触发。'}</div>`;
  renderScheduleJobList($('#aiScheduleJobList'),assistant,'assistant',canRun);
  renderAISchedulePanels();
  startScheduleJobPoll();
}
// The schedule is started and stopped on the row, not in the editor.
function scheduleEnabledSwitchHTML(j){
  const on=!!j.enabled,label=on?'已启用':'已停用';
  return `<label class="schedule-enabled" title="${on?'点击停用：不再按计划执行，正在运行的这次不受影响':'点击启用：按计划执行'}"><input type="checkbox" class="hui-switch" role="switch" data-toggle-schedule="${escapeHTML(j.id)}" aria-label="启用 ${escapeHTML(j.name)}" ${on?'checked':''}><span data-schedule-enabled-label>${label}</span></label>`;
}
async function toggleScheduleJobEnabled(input){
  const id=input.dataset.toggleSchedule,enabled=input.checked;
  const jobs=state.data.scheduleJobs||[],idx=jobs.findIndex(j=>j.id===id);
  if(idx<0)return;
  const before=jobs[idx].enabled;
  input.disabled=true;
  try{
    const saved=await api(`/api/schedule-jobs/${encodeURIComponent(id)}/enabled`,{method:'POST',body:JSON.stringify({enabled})});
    const at=(state.data.scheduleJobs||[]).findIndex(j=>j.id===id);
    if(at>=0)state.data.scheduleJobs[at]=saved;
    toast(enabled?`已启用「${saved.name||jobs[idx].name}」`:`已停用「${saved.name||jobs[idx].name}」，正在运行的这次不受影响`);
  }catch(err){
    const at=(state.data.scheduleJobs||[]).findIndex(j=>j.id===id);
    if(at>=0)state.data.scheduleJobs[at]={...state.data.scheduleJobs[at],enabled:before};
    toast(err.message,'err');
  }
  renderScheduleJobs();
}
// AI job rows keep 立即运行 on the row; the rest sits in a ⋯ menu. Items keep
// the data-* hooks the row buttons used, so the list binds them as before.
function scheduleRowMenuHTML(j){
  const id=escapeHTML(j.id);
  const item=(attr,label,icon,cls='')=>`<button type="button" role="menuitem" tabindex="-1" class="schedule-row-menu-item${cls}" ${attr}="${id}"><svg class="agent-icon harness-icon" data-icon="${icon}" width="16" height="16" aria-hidden="true" focusable="false"></svg><span>${label}</span></button>`;
  return `<span class="schedule-row-more"><button type="button" class="button ghost small schedule-row-more-btn" data-schedule-menu="${id}" aria-label="更多操作：${escapeHTML(j.name)}" aria-haspopup="menu" aria-expanded="false"><svg class="agent-icon harness-icon" data-icon="more-horizontal" width="16" height="16" aria-hidden="true" focusable="false"></svg></button><span class="schedule-row-menu" role="menu" data-menu-for="${id}" hidden>${item('data-edit-schedule','编辑','edit')}${item('data-schedule-history','执行记录','history')}${sharedScheduleRowMenuItem(j,item)}<span class="schedule-row-menu-sep" role="separator"></span>${item('data-delete-schedule','删除','trash',' is-danger')}</span></span>`;
}
function closeScheduleRowMenus({restore=false}={}){
  document.querySelectorAll('.schedule-row-menu:not([hidden])').forEach(menu=>{
    menu.hidden=true;
    const trigger=menu.previousElementSibling;
    trigger?.setAttribute('aria-expanded','false');
    if(restore&&menu.contains(document.activeElement))trigger?.focus();
  });
}
function openScheduleRowMenu(trigger,{focus=true}={}){
  const menu=trigger?.nextElementSibling;if(!menu)return;
  closeScheduleRowMenus();
  menu.hidden=false;trigger.setAttribute('aria-expanded','true');
  window.AgentFlowHarnessIcons?.hydrate(menu);
  const r=trigger.getBoundingClientRect(),h=menu.offsetHeight,w=menu.offsetWidth;
  let top=r.bottom+6;if(top+h>window.innerHeight-8)top=r.top-h-6;
  menu.style.top=`${Math.max(8,top)}px`;
  menu.style.left=`${Math.max(8,Math.min(r.right-w,window.innerWidth-w-8))}px`;
  if(focus)menu.querySelector('[role="menuitem"]:not([disabled])')?.focus({preventScroll:true});
}
function bindScheduleRowMenus(list){
  list.querySelectorAll('[data-schedule-menu]').forEach(b=>b.onclick=e=>{
    e.stopPropagation();
    if(b.getAttribute('aria-expanded')==='true')closeScheduleRowMenus();
    else openScheduleRowMenu(b);
  });
  list.querySelectorAll('.schedule-row-menu').forEach(menu=>{
    menu.addEventListener('click',e=>{if(e.target.closest('[role="menuitem"]'))closeScheduleRowMenus()});
    menu.addEventListener('keydown',e=>{
      const items=[...menu.querySelectorAll('[role="menuitem"]:not([disabled])')];
      const i=items.indexOf(document.activeElement);
      const next={ArrowDown:i+1,ArrowUp:i-1,Home:0,End:items.length-1}[e.key];
      if(next===undefined||!items.length)return;
      e.preventDefault();items[(next+items.length)%items.length].focus();
    });
  });
  if(document.body.dataset.scheduleMenuBound)return;
  document.body.dataset.scheduleMenuBound='1';
  document.addEventListener('click',e=>{if(!e.target.closest?.('.schedule-row-more'))closeScheduleRowMenus()});
  document.addEventListener('keydown',e=>{if(e.key==='Escape'||e.key==='Tab')closeScheduleRowMenus({restore:e.key==='Escape'})});
  document.addEventListener('scroll',e=>{if(!e.target.closest?.('.schedule-row-menu'))closeScheduleRowMenus()},true);
  window.addEventListener('resize',()=>closeScheduleRowMenus());
}
function renderScheduleJobList(list,jobs,kind,canRun){
  if(!list)return;
  const focused=document.activeElement;
  const focusAction=['editSchedule','runSchedule','deleteSchedule','scheduleMenu','toggleSchedule'].find(key=>focused?.dataset?.[key]);
  const focusId=list.contains(focused)&&focusAction&&!focused.closest('.schedule-row-menu')?focused.dataset[focusAction]:null;
  // A poll re-render keeps an open ⋯ menu open (and focused) on its job.
  const openMenu=list.querySelector('.schedule-row-menu:not([hidden])');
  const reopenId=openMenu?.dataset.menuFor||'',reopenFocus=!!openMenu?.contains(focused);
  if(kind==='dispatch'){
    const job=jobs[0];
    list.innerHTML=job?`<div class="schedule-config-runtime"><div><b>${job.enabled?'已启用':'未启用'} · ${escapeHTML(scheduleJobTimingLabel(job))}</b><span>${escapeHTML(scheduleSourceLabel(job))} · ${scheduleIssueStatusValues(job).map(issueStatusLabel).join('、')}</span><small>${knownTime(job.lastRunAt)?`最近：${knownTime(job.lastRunAt).toLocaleString()} · 候选 ${job.lastPulled||0}，启动 ${job.lastDispatched||0}`:'尚未运行'}${scheduleJobStatusText(job)?` · ${escapeHTML(scheduleJobStatusText(job))}`:''}</small></div>${scheduleJobTraceActionsHTML(job)}<button type="button" class="button ghost small" data-run-schedule="${escapeHTML(job.id)}" ${scheduleJobRunning(job)||!canRun?'disabled':''}>${scheduleJobRunning(job)?'运行中…':'立即运行'}</button></div>${scheduleBlockerHTML(job)}`:'';
  }else list.innerHTML=jobs.map(j=>{
    const assistant=!!j.skipDispatch;
    const kind=assistant?'AI':'触发';
    const running=scheduleJobRunning(j);
    const extra=running?'':assistant?(j.lastAssistantMessage?`，${escapeHTML(String(j.lastAssistantMessage).slice(0,80))}`:''):`，${scheduleSourceLabel(j)}，${scheduleIssueStatusValues(j).map(issueStatusLabel).join('、')}，候选 ${j.lastPulled||0}，启动 ${j.lastDispatched||0}`;
    const statusText=scheduleJobStatusText(j);
    const status=statusText?`，${escapeHTML(statusText)}`:'';
    const runDisabled=running||!canRun;
    const runTitle=running?'任务正在服务端运行，关闭页面不会中断':(canRun?'':'本实例未启用 scheduler');
    const session=assistant&&j.sessionMode==='fixed'?' · 同一会话':'';
    return `<div class="schedule-job-item${running?' is-running':''}${assistant&&!j.enabled?' is-paused':''}"><div class="schedule-job-row"><button type="button" class="schedule-job-main" data-edit-schedule="${escapeHTML(j.id)}" title="${assistant?'编辑 AI 定时任务':'编辑触发配置'}"><b><span class="schedule-kind-tag ${assistant?'is-assistant':'is-dispatch'}">${kind}</span>${escapeHTML(j.name)}</b>${sharedScheduleRowInfo(j)}<span>${escapeHTML(scheduleJobTimingLabel(j))}${assistant?'':` · ${j.enabled?'已启用':'已停用'}`}${assistant?session:` · 单次最多 ${escapeHTML(j.maxDispatch||10)} 个需求`}${running?' · 正在运行':''}</span><small class="${running?'schedule-run-live':''}">${(j.lastRunAt&&!String(j.lastRunAt).startsWith('0001-'))||running?`最近：${new Date(j.lastRunAt||Date.now()).toLocaleString()}${extra}${status}`:'尚未运行'}</small></button>${sharedScheduleRowAction(j)}${assistant?scheduleEnabledSwitchHTML(j):''}<button type="button" class="button ghost small" data-run-schedule="${escapeHTML(j.id)}" ${runDisabled?'disabled':''} ${runTitle?`title="${escapeHTML(runTitle)}"`:''}>${running?'运行中…':'立即运行'}</button>${scheduleRowMenuHTML(j)}</div>${assistant?'':scheduleBlockerHTML(j)}</div>`;
  }).join('')||'<div class="sync-empty">当前项目还没有 AI 定时任务，点击右上角「新建 AI 定时任务」。</div>';
  list.querySelectorAll('[data-edit-schedule]').forEach(b=>b.onclick=()=>{
    const job=(state.data.scheduleJobs||[]).find(j=>j.id===b.dataset.editSchedule);
    if(!job)return;
    if(job.skipDispatch)openScheduleJobModal('assistant',job);
    else fillScheduleJobForm(job);
  });
  bindSharedScheduleRowActions(list);
  bindScheduleJobTraceActions(list);
  list.querySelectorAll('[data-run-schedule]').forEach(b=>b.onclick=()=>runScheduleJob(b.dataset.runSchedule));
  list.querySelectorAll('[data-toggle-schedule]').forEach(b=>b.onchange=()=>toggleScheduleJobEnabled(b));
  list.querySelectorAll('[data-delete-schedule]').forEach(b=>b.onclick=()=>deleteScheduleJob(b.dataset.deleteSchedule));
  list.querySelectorAll('[data-open-align-run]').forEach(b=>b.onclick=()=>openRunFromId(b.dataset.openAlignRun,{forceRuns:true}));
  list.querySelectorAll('[data-realign-run]').forEach(b=>b.onclick=()=>realignBaseline(b.dataset.realignRun));
  bindScheduleRowMenus(list);
  if(reopenId){const trigger=[...list.querySelectorAll('[data-schedule-menu]')].find(b=>b.dataset.scheduleMenu===reopenId);if(trigger)openScheduleRowMenu(trigger,{focus:reopenFocus})}
  if(focusId){const next=[...list.querySelectorAll('[data-edit-schedule],[data-run-schedule],[data-delete-schedule],[data-schedule-menu],[data-toggle-schedule]')].find(el=>el.dataset[focusAction]===focusId&&!el.closest('.schedule-row-menu'));next?.focus()}

}
async function saveScheduleJob(e){
  e.preventDefault();
  const f=e.currentTarget,cron=f.elements.scheduleMode.value==='cron',assistant=f.elements.jobKind.value==='assistant';
  const version=scheduleModalVersion;
  if(assistant&&f.modelPickerReady){
    await f.modelPickerReady;
    if(version!==scheduleModalVersion)return;
  }
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
    timeoutMinutes:assistant&&f.elements.timeoutMinutes.value!==''?Number(f.elements.timeoutMinutes.value):undefined,
    sessionMode:assistant&&f.elements.sessionMode?f.elements.sessionMode.value:'',
    sessionKeep:assistant&&f.elements.sessionKeep?Number(f.elements.sessionKeep.value)||50:0,
    enabled:assistant?scheduleSavedEnabled(f.elements.id.value):f.elements.enabled.checked
  };
  if(assistant&&typeof sharedScheduleFormPayload==='function')Object.assign(payload,sharedScheduleFormPayload(f));
  if(!payload.name)return toast('请填写任务名称');
  if(assistant&&!payload.assistantPrompt&&!payload.sharedTaskId)return toast('请填写 AI 定时任务的提示词');
  if(assistant&&!f.elements.timeoutMinutes.validity.valid)return toast('执行超时必须为 1–240 分钟');
  if(!assistant&&!payload.workflowId)return toast('请选择默认 SOP');
  if(!assistant&&!payload.issueStatuses.length)return toast('请至少选择一个触发状态');
  if(!assistant&&payload.maxDispatch!==undefined&&(!Number.isInteger(payload.maxDispatch)||payload.maxDispatch<1||payload.maxDispatch>10))return toast('单次最多触发需求数必须为 1–10，或留空使用默认值 10');
  if(cron&&!payload.cron)return toast('请填写 Cron 表达式');
  const projectId=state.projectFilter,button=f.querySelector?.('[type="submit"]'),label=button?.textContent;
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
// Saving an AI job keeps whatever the row switch last set; new jobs start enabled.
function scheduleSavedEnabled(id){
  const job=id?(state.data.scheduleJobs||[]).find(j=>j.id===id):null;
  return job?!!job.enabled:true;
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
  if(finished){refreshIssues();refreshScheduleExecutions().catch(()=>{})}
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
    refreshScheduleExecutions().catch(()=>{});
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
