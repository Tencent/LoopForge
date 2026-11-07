// Shared definitions contain no project credentials, notification bindings or run state.
let sharedScheduleCatalog=[];
const sharedScheduleVersionCache=new Map();
let aiScheduleTab='project';
let sharedScheduleQuery='';
let sharedScheduleCatalogRequest=0;
let sharedScheduleCatalogLoading=false;
let sharedScheduleCatalogError='';
let sharedScheduleVersionRequest=0;
let sharedScheduleEditorBaseVersion=0;
let sharedScheduleEditorContext=null;
let sharedSchedulePublishRequest=0;
function rememberSharedScheduleVersions(versions){
  for(const v of versions||[])sharedScheduleVersionCache.set(`${v.id}:${v.version}`,v);
}
function sharedScheduleDefinition(id,version){return sharedScheduleVersionCache.get(`${id}:${version}`)}
async function refreshSharedScheduleCatalog(){
  const request=++sharedScheduleCatalogRequest;
  sharedScheduleCatalogLoading=true;sharedScheduleCatalogError='';renderAISchedulePanels();
  try{
    const items=await api('/api/shared-schedule-tasks',{silent:true});
    if(request!==sharedScheduleCatalogRequest)return sharedScheduleCatalog;
    sharedScheduleCatalog=Array.isArray(items)?items:[];
    rememberSharedScheduleVersions(sharedScheduleCatalog);
    return sharedScheduleCatalog;
  }catch(err){if(request===sharedScheduleCatalogRequest)sharedScheduleCatalogError=err.message;throw err}
  finally{if(request===sharedScheduleCatalogRequest){sharedScheduleCatalogLoading=false;renderAISchedulePanels()}}
}
async function selectAIScheduleTab(tab){
  aiScheduleTab=tab==='market'?'market':'project';
  renderAISchedulePanels();
  if(aiScheduleTab==='market')try{await refreshSharedScheduleCatalog()}catch{}
}
function renderAISchedulePanels(){
  const market=aiScheduleTab==='market';
  $$('[data-ai-schedule-tab]').forEach(b=>{const active=(b.dataset.aiScheduleTab==='market')===market;b.classList.toggle('active',active);b.setAttribute('aria-selected',String(active))});
  $('#aiScheduleProjectPanel').hidden=market;$('#aiScheduleMarketPanel').hidden=!market;
  $('#sharedScheduleSearchField').hidden=!market;
  $('#aiScheduleProjectCount').textContent=scheduleJobsInView(state.data.scheduleJobs,state.projectFilter).filter(j=>j.skipDispatch&&state.projectFilter).length;
  $('#sharedScheduleMarketCount').textContent=sharedScheduleCatalog.length;
  if(market)renderSharedScheduleMarket();
}
function renderSharedScheduleMarket(){
  const host=$('#sharedScheduleLibrary'),projectId=state.projectFilter;
  const focused=document.activeElement;
  const action=host.contains(focused)?['subscribeShared','readShared'].find(key=>focused.dataset[key]):null;
  const focusId=action?focused.dataset[action]:null;
  const items=sharedScheduleCatalog.filter(v=>[v.name,v.description,v.sourceProjectName,v.sourceProjectId,...(v.parameters||[]).map(p=>p.label)].join(' ').toLowerCase().includes(sharedScheduleQuery));
  const project=currentProject();
  $('#sharedScheduleMarketHint').textContent=projectId?`订阅将添加到「${project?.name||projectId}」，参数、调度和通知由本项目独立配置。新版本发布后可手动升级。`:'共享任务可跨项目浏览；请先选择项目，再添加订阅。';
  const status=$('#sharedScheduleMarketStatus');
  status.innerHTML=sharedScheduleCatalogError?`<div class="shared-schedule-market-error">读取共享任务失败：${escapeHTML(sharedScheduleCatalogError)} <button type="button" class="button ghost small" data-retry-shared>重试</button></div>`:'';
  status.querySelector('[data-retry-shared]')?.addEventListener('click',()=>refreshSharedScheduleCatalog().catch(()=>{}));
  host.setAttribute('aria-busy',String(sharedScheduleCatalogLoading));
  if(!sharedScheduleCatalog.length){host.innerHTML=`<div class="shared-schedule-market-empty">${sharedScheduleCatalogLoading?'正在读取共享任务…':sharedScheduleCatalogError?'共享任务暂时无法加载，请重试。':'还没有共享任务。在本项目的独立 AI 定时任务旁点击「发布共享」，即可供其他项目订阅。'}</div>`;return}
  host.innerHTML=items.map(v=>{
    const count=(state.data.scheduleJobs||[]).filter(j=>projectId&&j.projectId===projectId&&j.sharedTaskId===v.id).length;
    return `<article class="mcp-market-card"><div class="mcp-card-head"><span class="shared-schedule-market-icon"><svg class="agent-icon harness-icon" data-icon="clock" width="22" height="22" aria-hidden="true" focusable="false"></svg></span><div><h3>${escapeHTML(v.name)}</h3><span class="shared-schedule-market-source">${escapeHTML(v.sourceProjectName||v.sourceProjectId)}</span></div><span class="hui-badge" data-tone="neutral" data-size="sm">v${v.version}</span></div><p>${escapeHTML(v.description||'暂无用途说明，查看详情了解提示词与业务参数。')}</p><div class="shared-schedule-market-meta"><span>推荐 ${escapeHTML(scheduleJobTimingLabel(v))}</span><span>${(v.parameters||[]).length} 个参数</span>${count?`<span class="shared-schedule-subscribed">本项目已订阅 ${count} 个</span>`:''}</div><div class="shared-schedule-market-actions"><button type="button" class="button ghost small" data-read-shared="${escapeHTML(v.id)}">查看详情</button><button type="button" class="button primary small" data-subscribe-shared="${escapeHTML(v.id)}" ${projectId?'':'disabled'}>${count?'继续添加':'添加订阅'}</button></div></article>`;
  }).join('')||'<div class="shared-schedule-market-empty">没有匹配的共享任务，试试其他关键词。</div>';
  host.querySelectorAll('[data-read-shared]').forEach(b=>b.onclick=()=>openSharedScheduleVersions(b.dataset.readShared,false));
  host.querySelectorAll('[data-subscribe-shared]').forEach(b=>b.onclick=()=>{
    if(state.projectFilter!==projectId||!projectId)return toast('请先选择项目');
    const v=items.find(v=>v.id===b.dataset.subscribeShared);if(!v)return;
    openScheduleJobModal('assistant',{name:v.name,skipDispatch:true,sharedTaskId:v.id,sharedTaskVersion:v.version,parameterValues:{},assistantModel:v.model,cron:v.cron,intervalSec:v.intervalSec});
  });
  window.AgentFlowHarnessIcons?.hydrate(host);
  if(focusId)[...host.querySelectorAll('[data-read-shared],[data-subscribe-shared]')].find(el=>el.dataset[action]===focusId)?.focus();
}
function sharedScheduleRowInfo(job){
  if(!job.sharedTaskId)return '';
  const v=sharedScheduleDefinition(job.sharedTaskId,job.sharedTaskVersion);
  return `<span class="shared-schedule-origin">订阅 · v${Number(job.sharedTaskVersion)}${v?` · ${escapeHTML(v.sourceProjectName||v.sourceProjectId)}`:''}</span>`;
}
function sharedScheduleRowAction(job){
  if(!job.skipDispatch)return '';
  if(!job.sharedTaskId)return `<button type="button" class="button ghost small" data-publish-schedule="${escapeHTML(job.id)}">发布共享</button>`;
  const latest=sharedScheduleCatalog.find(v=>v.id===job.sharedTaskId);
  const newer=latest?.version>job.sharedTaskVersion;
  return `<button type="button" class="button ${newer?'hui-btn-soft':'ghost'} small" data-shared-versions="${escapeHTML(job.id)}">${newer?`有新版 v${latest.version}`:'查看版本'}</button>`;
}
function bindSharedScheduleRowActions(host){
  host.querySelectorAll('[data-publish-schedule]').forEach(b=>b.onclick=()=>openSharedSchedulePublish(b.dataset.publishSchedule));
  host.querySelectorAll('[data-shared-versions]').forEach(b=>b.onclick=async()=>{
    const job=(state.data.scheduleJobs||[]).find(j=>j.id===b.dataset.sharedVersions);
    if(!job)return;
    const opening=openScheduleJobModal('assistant',job),editorVersion=scheduleModalVersion;
    await opening;
    if(editorVersion===scheduleModalVersion&&$('#scheduleModal').classList.contains('open')&&$('#scheduleJobForm').elements.id.value===job.id)openSharedScheduleVersions(job.sharedTaskId,true);
  });
}
function sharedScheduleFormPayload(f){
  const id=f.elements.sharedTaskId?.value;
  if(!id)return {};
  const parameterValues=Object.fromEntries([...f.querySelectorAll('[data-shared-param]')].map(el=>[el.dataset.sharedParam,el.value]));
  return {sharedTaskId:id,sharedTaskVersion:Number(f.elements.sharedTaskVersion.value),parameterValues,assistantPrompt:''};
}
function sharedSchedulePreview(v,values){
  return String(v.prompt||'').replace(/\{\{\s*params\.([^{}]+?)\s*\}\}/g,(_,name)=>values[name.trim()]??'');
}
function fillSharedScheduleForm(job){
  const f=$('#scheduleJobForm'),section=$('#sharedScheduleBinding');
  const bound=!!job.sharedTaskId&&f.elements.jobKind.value==='assistant';
  f.elements.sharedTaskId.value=bound?job.sharedTaskId:'';
  f.elements.sharedTaskVersion.value=bound?job.sharedTaskVersion:'';
  f.querySelector('[data-schedule-prompt]').hidden=bound||f.elements.jobKind.value!=='assistant';
  f.elements.assistantPrompt.required=!bound&&f.elements.jobKind.value==='assistant';
  section.hidden=!bound;
  f.querySelector('[type="submit"]').disabled=false;
  if(!bound){section.replaceChildren();return}
  const v=sharedScheduleDefinition(job.sharedTaskId,job.sharedTaskVersion);
  if(!v){
    section.innerHTML='<p class="schedule-field-hint" role="status">正在读取订阅版本…</p>';
    f.querySelector('[type="submit"]').disabled=true;
    return;
  }
  const values=Object.fromEntries((v.parameters||[]).map(p=>[p.name,Object.hasOwn(job.parameterValues||{},p.name)?job.parameterValues[p.name]:(p.default||'')]));
  section.innerHTML=`<div class="shared-schedule-section-head"><div><b>${escapeHTML(v.name)}</b><p class="schedule-field-hint">来源：${escapeHTML(v.sourceProjectName||v.sourceProjectId)} · ${sharedScheduleEditorContext&&v.version!==sharedScheduleEditorBaseVersion?'待保存':'固定'} v${v.version}</p></div><button type="button" class="button ghost small" data-choose-shared-version aria-expanded="false">查看 / 切换版本</button></div><p class="schedule-field-hint">${escapeHTML(v.description||'提示词由共享任务维护；调度、模型、参数和通知保存在当前项目。')}</p><div class="shared-schedule-values">${(v.parameters||[]).map(p=>`<label>${escapeHTML(p.label)}${p.required?' *':''}<span class="schedule-field-hint">${escapeHTML(p.name)} · ${escapeHTML(p.type)}</span>${p.type==='boolean'?`<select class="select" data-shared-param="${escapeHTML(p.name)}" aria-label="${escapeHTML(p.label)}" ${p.required?'required':''}><option value="">请选择</option><option value="true" ${values[p.name]==='true'?'selected':''}>是</option><option value="false" ${values[p.name]==='false'?'selected':''}>否</option></select>`:`<input class="input" data-shared-param="${escapeHTML(p.name)}" aria-label="${escapeHTML(p.label)}" value="${escapeHTML(values[p.name])}" ${p.required?'required':''} maxlength="2000" ${['integer','number'].includes(p.type)?`type="number" step="${p.type==='integer'?'1':'any'}"`:'type="text"'}>`}</label>`).join('')||'<p class="schedule-field-hint">此版本没有业务参数。</p>'}</div><details class="shared-schedule-preview"><summary>执行提示词预览</summary><pre></pre></details>`;
  const preview=()=>{section.querySelector('pre').textContent=sharedSchedulePreview(v,sharedScheduleFormPayload(f).parameterValues)};
  section.querySelectorAll('[data-shared-param]').forEach(el=>el.addEventListener('input',preview));
  section.querySelector('[data-choose-shared-version]').onclick=()=>openSharedScheduleVersions(v.id,true);
  preview();
  paintSharedScheduleSaveLabel();
}
async function loadSharedScheduleEditor(job,version,projectId){
  if(!job.sharedTaskId)return;
  const active=()=>scheduleModalVersion===version&&state.projectFilter===projectId&&$('#scheduleModal').classList.contains('open');
  try{
    if(!sharedScheduleDefinition(job.sharedTaskId,job.sharedTaskVersion))rememberSharedScheduleVersions(await api(`/api/shared-schedule-tasks/${encodeURIComponent(job.sharedTaskId)}/versions`,{silent:true}));
    if(!active())return;
    if(!sharedScheduleDefinition(job.sharedTaskId,job.sharedTaskVersion))throw new Error('订阅版本不存在');
    fillSharedScheduleForm(job);
  }catch(err){
    if(!active())return;
    $('#sharedScheduleBinding').innerHTML=`<p role="status">读取订阅失败：${escapeHTML(err.message)}。请关闭后重试。</p>`;
    $('#scheduleJobForm').querySelector('[type="submit"]').disabled=true;
  }
}
function sharedScheduleParameterRow(p={}){
  const row=document.createElement('div');row.className='shared-schedule-parameter-row';
  row.innerHTML=`<label>参数名<input class="input" data-param-field="name" aria-label="参数名" required pattern="[a-zA-Z][a-zA-Z0-9_]{0,31}" value="${escapeHTML(p.name||'')}" placeholder="days"></label><label>显示名称<input class="input" data-param-field="label" aria-label="显示名称" required maxlength="120" value="${escapeHTML(p.label||'')}" placeholder="停滞天数"></label><label>类型<select class="select" data-param-field="type" aria-label="参数类型">${[['string','文本'],['integer','整数'],['number','数字'],['boolean','布尔值']].map(([value,label])=>`<option value="${value}" ${p.type===value?'selected':''}>${label}</option>`).join('')}</select></label><label>默认值<input class="input" data-param-field="default" aria-label="默认值" maxlength="2000" value="${escapeHTML(p.default||'')}"></label><label class="schedule-check"><input type="checkbox" data-param-field="required" ${p.required?'checked':''}>必填</label><button type="button" class="button ghost small" data-remove-parameter aria-label="删除参数">删除</button>`;
  row.querySelector('[data-remove-parameter]').onclick=()=>row.remove();
  return row;
}
async function openSharedSchedulePublish(jobId){
  const job=(state.data.scheduleJobs||[]).find(j=>j.id===jobId);
  if(!job||job.sharedTaskId||!job.skipDispatch)return;
  const request=++sharedSchedulePublishRequest,projectId=state.projectFilter,f=$('#sharedSchedulePublishForm');
  $('#sharedSchedulePublishModal').classList.add('open');f.inert=true;f.querySelector('[type="submit"]').disabled=false;
  $('#sharedSchedulePublishError').textContent='正在读取发布版本…';
  try{
    await refreshSharedScheduleCatalog();
    if(request!==sharedSchedulePublishRequest||!$('#sharedSchedulePublishModal').classList.contains('open'))return;
    if(state.projectFilter!==projectId)throw new Error('项目已切换，请关闭后重新发布');
    const prior=sharedScheduleCatalog.find(v=>v.sourceJobId===job.id&&v.sourceProjectId===job.projectId);
    f.elements.sourceJobId.value=job.id;f.elements.sourceProjectId.value=job.projectId;
    f.elements.baseVersion.value=prior?.version||0;
    f.elements.name.value=prior?.name||job.name;f.elements.description.value=prior?.description||'';
    f.elements.prompt.value=prior?.prompt||job.assistantPrompt;f.elements.changeNote.value='';
    $('#sharedScheduleParameterRows').replaceChildren(...(prior?.parameters||[]).map(sharedScheduleParameterRow));
    f.querySelector('[type="submit"]').textContent=`发布 v${(prior?.version||0)+1}`;
    $('#sharedSchedulePublishError').textContent='';f.inert=false;f.elements.name.focus();
  }catch(err){if(request===sharedSchedulePublishRequest)$('#sharedSchedulePublishError').textContent=err.message}
}
async function publishSharedSchedule(e){
  e.preventDefault();const f=e.currentTarget,projectId=state.projectFilter,request=sharedSchedulePublishRequest;
  if(f.elements.sourceProjectId.value!==projectId)return toast('项目已切换，请重新发布');
  const parameters=[...f.querySelectorAll('.shared-schedule-parameter-row')].map(row=>Object.fromEntries([...row.querySelectorAll('[data-param-field]')].map(el=>[el.dataset.paramField,el.type==='checkbox'?el.checked:el.value.trim()])));
  const payload={name:f.elements.name.value.trim(),description:f.elements.description.value.trim(),prompt:f.elements.prompt.value.trim(),parameters,changeNote:f.elements.changeNote.value.trim(),baseVersion:Number(f.elements.baseVersion.value)};
  const button=f.querySelector('[type="submit"]');button.disabled=true;
  try{
    const v=await api(`/api/schedule-jobs/${encodeURIComponent(f.elements.sourceJobId.value)}/publish`,{method:'POST',body:JSON.stringify(payload)});
    rememberSharedScheduleVersions([v]);
    sharedScheduleCatalog=sharedScheduleCatalog.filter(item=>item.id!==v.id).concat(v);
    renderScheduleJobs();
    if(request===sharedSchedulePublishRequest){$('#sharedSchedulePublishModal').classList.remove('open');$('#newAISchedule').focus()}
    toast(`已发布 ${v.name} v${v.version}，其他项目可从「共享任务市场」订阅`);
  }catch(err){if(request===sharedSchedulePublishRequest)$('#sharedSchedulePublishError').textContent=err.message}
  finally{if(request===sharedSchedulePublishRequest)button.disabled=false}
}
function resetSharedScheduleVersionEditor(job){
  sharedScheduleEditorBaseVersion=Number(job.sharedTaskVersion)||0;
  sharedScheduleEditorContext=null;
  const host=$('#sharedScheduleVersionEditor');host.hidden=true;host.replaceChildren();
}
function paintSharedScheduleSaveLabel(){
  const f=$('#scheduleJobForm'),selected=Number(f.elements.sharedTaskVersion.value);
  if(!sharedScheduleEditorContext)return;
  const expanded=!$('#sharedScheduleVersionEditor').hidden,choose=f.querySelector('[data-choose-shared-version]');
  if(choose){choose.textContent=expanded?'收起版本对比':'查看 / 切换版本';choose.setAttribute('aria-expanded',String(expanded))}
  const changed=selected!==sharedScheduleEditorBaseVersion;
  f.querySelector('[type="submit"]').textContent=changed?`保存并${selected>sharedScheduleEditorBaseVersion?'升级':'切换'}到 v${selected}`:'保存 AI 定时任务';
  $('#scheduleModalTitle').textContent=changed?'更新共享任务订阅':'共享任务版本';
  $('#scheduleModalHint').textContent='在这里对比版本、调整参数并保存。保存后，后续执行使用所选版本。';
}
async function openSharedScheduleVersionEditor(id){
  const f=$('#scheduleJobForm'),host=$('#sharedScheduleVersionEditor');
  if(!$('#scheduleModal').classList.contains('open')||f.elements.sharedTaskId.value!==id)return;
  const context={id,editorVersion:scheduleModalVersion,projectId:state.projectFilter,jobId:f.elements.id.value,versions:[],valuesByVersion:new Map()};
  // Reopening the comparison keeps in-progress parameter edits.
  if(sharedScheduleEditorContext?.id===id&&sharedScheduleEditorContext.editorVersion===scheduleModalVersion){
    host.hidden=!host.hidden;paintSharedScheduleSaveLabel();
    if(!host.hidden)host.focus({preventScroll:true});
    return;
  }
  sharedScheduleEditorContext=context;
  const active=()=>sharedScheduleEditorContext===context&&context.editorVersion===scheduleModalVersion&&context.projectId===state.projectFilter&&context.jobId===f.elements.id.value&&f.elements.sharedTaskId.value===id&&$('#scheduleModal').classList.contains('open');
  host.hidden=false;host.textContent='正在读取版本…';
  f.querySelector('[type="submit"]').disabled=true;
  try{
    const versions=await api(`/api/shared-schedule-tasks/${encodeURIComponent(id)}/versions`,{silent:true});
    if(!active())return;
    if(!Array.isArray(versions)||!versions.length)throw new Error('共享任务不存在');
    if(!versions.some(v=>v.version===sharedScheduleEditorBaseVersion))throw new Error('当前订阅版本不存在');
    context.versions=versions;rememberSharedScheduleVersions(versions);
    const selectVersion=selected=>{
      if(!active())return;
      const old=Number(f.elements.sharedTaskVersion.value),values=sharedScheduleFormPayload(f).parameterValues;
      context.valuesByVersion.set(old,values);
      const v=versions.find(v=>v.version===selected);if(!v)return;
      const retained=context.valuesByVersion.get(selected)||values;
      const parameterValues=Object.fromEntries((v.parameters||[]).filter(p=>Object.hasOwn(retained,p.name)).map(p=>[p.name,retained[p.name]]));
      fillSharedScheduleForm({sharedTaskId:id,sharedTaskVersion:v.version,parameterValues});
      const current=versions.find(v=>v.version===sharedScheduleEditorBaseVersion);
      host.innerHTML=`<div class="shared-schedule-section-head"><label class="shared-schedule-version-select">目标版本<select class="select" aria-label="目标订阅版本">${versions.map(item=>`<option value="${item.version}" ${item.version===v.version?'selected':''}>v${item.version}${item===versions[0]?' · 最新':''}${item.version===current.version?' · 当前':''}</option>`).join('')}</select></label><span class="shared-schedule-version-current">当前 v${current.version}${v.version!==current.version?` → v${v.version}`:''}</span></div><p class="schedule-field-hint">${escapeHTML(v.changeNote||'暂无版本说明')}</p><details class="shared-schedule-comparison" open><summary>提示词和参数对比</summary><div class="shared-schedule-diff"><section><b>当前 v${current.version}</b><pre>${escapeHTML(current.prompt)}</pre><small>${escapeHTML(sharedScheduleParameterSummary(current))}</small></section><section><b>目标 v${v.version}</b><pre>${escapeHTML(v.prompt)}</pre><small>${escapeHTML(sharedScheduleParameterSummary(v))}</small></section></div></details>`;
      host.querySelector('select').onchange=e=>selectVersion(Number(e.target.value));
      paintSharedScheduleSaveLabel();
    };
    selectVersion(versions[0].version);
    host.focus({preventScroll:true});host.scrollIntoView?.({block:'nearest'});
  }catch(err){
    if(!active())return;
    host.innerHTML=`<p role="status">读取版本失败：${escapeHTML(err.message)}</p><button type="button" class="button ghost small" data-retry-shared-versions>重试</button>`;
    host.querySelector('button').onclick=()=>{sharedScheduleEditorContext=null;openSharedScheduleVersionEditor(id)};
  }finally{if(active())f.querySelector('[type="submit"]').disabled=false}
}
async function openSharedScheduleVersions(id,editing){
  if(editing)return openSharedScheduleVersionEditor(id);
  const request=++sharedScheduleVersionRequest,host=$('#sharedScheduleVersions');
  $('#sharedScheduleVersionTitle').textContent='共享任务详情';
  $('#sharedScheduleVersionModal').classList.add('open');$('#sharedScheduleVersionModal .modal-close').focus();host.textContent='正在读取版本…';
  try{
    const versions=await api(`/api/shared-schedule-tasks/${encodeURIComponent(id)}/versions`,{silent:true});
    if(request!==sharedScheduleVersionRequest||!$('#sharedScheduleVersionModal').classList.contains('open'))return;
    if(!Array.isArray(versions)||!versions.length)throw new Error('共享任务不存在');
    rememberSharedScheduleVersions(versions);
    const paint=selected=>{
      const v=versions.find(v=>v.version===selected)||versions[0];
      host.innerHTML=`<p class="modal-hint">${escapeHTML(v.sourceProjectName||v.sourceProjectId)}</p><label class="shared-schedule-version-select">发布版本<select class="select" aria-label="发布版本">${versions.map(item=>`<option value="${item.version}" ${item.version===v.version?'selected':''}>v${item.version}${item===versions[0]?' · 最新':''}</option>`).join('')}</select></label><p>${escapeHTML(v.changeNote||'暂无版本说明')}</p><div class="shared-schedule-diff"><section><b>${escapeHTML(v.name)} · v${v.version}</b><pre>${escapeHTML(v.prompt)}</pre><small>${escapeHTML(sharedScheduleParameterSummary(v))}</small></section></div>`;
      host.querySelector('select').onchange=e=>paint(Number(e.target.value));
    };
    paint(versions[0].version);
  }catch(err){if(request===sharedScheduleVersionRequest)host.textContent=`读取失败：${err.message}`}
}
function sharedScheduleParameterSummary(v){return (v.parameters||[]).map(p=>`${p.label} (${p.name}, ${p.type}${p.required?', 必填':''}${p.default?`, 默认 ${p.default}`:''})`).join('；')||'无业务参数'}
function bindSharedScheduleUI(){
  $$('[data-ai-schedule-tab]').forEach(b=>b.onclick=()=>selectAIScheduleTab(b.dataset.aiScheduleTab));
  $('#sharedScheduleSearch').oninput=e=>{sharedScheduleQuery=e.target.value.trim().toLowerCase();renderSharedScheduleMarket()};
  ['sharedSchedulePublishModal','sharedScheduleVersionModal'].forEach(id=>{
    $(`#${id} .modal-close`).onclick=()=>{
      $(`#${id}`).classList.remove('open');
      const target=$('#scheduleModal').classList.contains('open')?$('#scheduleJobForm [type="submit"]'):$('#aiScheduleMarketTab');
      target.focus();
    };
  });
  $('#sharedScheduleAddParameter').onclick=()=>{
    const host=$('#sharedScheduleParameterRows');
    if(host.children.length>=20)return toast('最多支持 20 个参数');
    host.append(sharedScheduleParameterRow());host.lastElementChild.querySelector('input').focus();
  };
  $('#sharedSchedulePublishForm').onsubmit=publishSharedSchedule;
}
