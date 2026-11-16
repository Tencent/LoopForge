function reqIcon(name,size=13){
  return `<svg class="agent-icon harness-icon" data-icon="${name}" width="${size}" height="${size}" aria-hidden="true"></svg>`;
}
function hydrateReqIcons(root){
  if(root)window.AgentFlowHarnessIcons?.hydrate?.(root);
}
function reqDescription(){
  return ($('#chatInput')?.value||'').trim();
}
function projectTAPDReady(project){
  project=project||currentProject();
  if(!project)return false;
  if(!(project.tapdWorkspaceId||'').trim()||!(project.tapdIterationId||'').trim())return false;
  const cfg=state.data.syncConfig||{};
  if((cfg.tapdMcpUrl||'').trim())return true;
  return (state.data.mcpServers||[]).some(s=>{
    if(s.scope==='project'&&s.projectId&&s.projectId!==project.id)return false;
    const market=String(s.marketId||'');
    if(market==='70'||market==='tapd')return true;
    return /tapd/i.test(`${s.id||''} ${s.name||''} ${s.url||''}`);
  });
}
function issueCanPushTAPD(issue){
  if(!issue||trackerRefOf(issue)||(issue.tapdId||'').trim())return false;
  return projectTAPDReady((state.data.projects||[]).find(p=>p.id===issue.projectId)||currentProject());
}
function reqProjectRepositories(){
  const project=currentProject();
  return (state.data.repositories||[]).filter(r=>r.projectId===project?.id);
}
function fillReqRepository(){
  const host=$('#reqRepository');
  if(!host)return;
  // 默认全选项目下所有仓库
  host.dataset.ids=JSON.stringify(reqProjectRepositories().map(r=>r.id));
  renderReqRepositoryPicker();
}
function selectedReqRepositoryIds(){
  const host=$('#reqRepository');
  if(host){
    try{
      const ids=JSON.parse(host.dataset.ids||'[]');
      if(Array.isArray(ids))return ids;
    }catch{}
  }
  const def=currentProject()?.defaultRepositoryId;
  return def?[def]:[];
}
function selectedReqRepositoryId(){
  return selectedReqRepositoryIds()[0]||'';
}
function fillReqWorkflow(preferred){
  const select=$('#reqWorkflow');
  if(!select)return;
  const opts=runnableWorkflowOptions();
  const preferredId=String(preferred||select.value||'').trim();
  const fallback=defaultSOPForIssue({projectId:state.projectFilter,kind:'requirement'});
  const selected=opts.some(w=>w.id===preferredId)?preferredId:(opts.some(w=>w.id===fallback)?fallback:(opts[0]?.id||''));
  select.innerHTML=opts.map(w=>`<option value="${escapeHTML(w.id)}" ${w.id===selected?'selected':''}>${escapeHTML(w.name)}</option>`).join('')||'<option value="">暂无可选 SOP</option>';
  if(selected)select.value=selected;
  window.enhanceSelect?.(select)?.refresh?.();
}
function reqUsesSOP(){
  return state.reqDelivery!=='none'&&runnableWorkflowOptions().length>0;
}
function renderReqDelivery(){
  const hasSOP=runnableWorkflowOptions().length>0;
  const delivery=reqUsesSOP()?'sop':'none';
  $$('#reqDelivery [data-req-delivery]').forEach(btn=>{
    const on=btn.dataset.reqDelivery===delivery;
    btn.classList.toggle('is-on',on);
    btn.setAttribute('aria-checked',String(on));
    const ind=btn.querySelector('.req-choice-ind');
    if(ind)ind.innerHTML=on?reqIcon('check',11):'';
    if(btn.dataset.reqDelivery==='sop'){
      btn.disabled=!hasSOP;
      btn.classList.toggle('is-unavailable',!hasSOP);
    }
  });
  const hint=$('#reqDeliverySOPHint');
  if(hint)hint.textContent=hasSOP?'按选定的 SOP 流程跑，可手动或定时触发':'当前项目还没有可运行的 SOP';
  const field=$('#reqWorkflowField');
  if(field)field.hidden=delivery!=='sop';
  hydrateReqIcons($('#reqDelivery'));
}
function setReqDelivery(delivery){
  if(delivery==='sop'&&!runnableWorkflowOptions().length)return;
  state.reqDelivery=delivery==='none'?'none':'sop';
  renderReqDelivery();
  renderReqFooter();
  persistRequirementComposer();
}
function selectedReqWorkflowId(){
  return reqUsesSOP()?($('#reqWorkflow')?.value.trim()||''):'';
}
function selectedReqPlaybookId(){
  const wf=selectedReqWorkflowId();
  const books=projectPlaybooks(currentProject());
  const hit=books.find(b=>(b.bindings||[]).some(x=>x.workflowId===wf));
  return hit?.id||'';
}
function setReqRepositoryIds(ids){
  const host=$('#reqRepository');
  if(!host)return;
  const known=new Set(reqProjectRepositories().map(r=>r.id));
  host.dataset.ids=JSON.stringify([...new Set((ids||[]).filter(id=>known.has(id)))]);
  renderReqRepositoryPicker();
  persistRequirementComposer();
}
function renderReqRepositoryPicker(){
  const host=$('#reqRepository');
  if(!host)return;
  const project=currentProject();
  const repos=reqProjectRepositories();
  let selected=[];
  try{selected=JSON.parse(host.dataset.ids||'[]')}catch{selected=[]}
  const chosen=new Set(selected);
  const leftover=repos.filter(r=>!chosen.has(r.id));
  const removable=selected.length>1;
  const tags=selected.map(id=>{
    const name=escapeHTML(repos.find(item=>item.id===id)?.name||id);
    const def=id===project?.defaultRepositoryId?'<span class="req-chip-def">默认</span>':'';
    if(!removable)return `<span class="repo-picker-tag is-single">${name}${def}</span>`;
    return `<span class="repo-picker-tag">${name}${def}<button type="button" data-remove-req-repo="${escapeHTML(id)}" aria-label="移除 ${name}">×</button></span>`;
  }).join('');
  const empty=!repos.length?'<span class="req-help">未绑定仓库</span>':(!selected.length?'<span class="req-help">未选择仓库</span>':'');
  const add=leftover.length?`<select class="repo-picker-select" aria-label="添加仓库"><option value="">＋ 添加仓库</option>${leftover.map(r=>`<option value="${escapeHTML(r.id)}">${escapeHTML(r.name)}</option>`).join('')}</select>`:'';
  host.innerHTML=`<div class="repo-picker-field">${tags}${empty}${add}</div>`;
  host.querySelectorAll('[data-remove-req-repo]').forEach(b=>b.onclick=e=>{
    e.stopPropagation?.();
    setReqRepositoryIds(selected.filter(id=>id!==b.dataset.removeReqRepo));
  });
  const sel=host.querySelector('select');
  if(sel)sel.onchange=()=>{
    if(!sel.value)return;
    setReqRepositoryIds([...selected,sel.value]);
  };
  syncReqBaseBranch();
  renderReqRepoChip();
}
// 分支规则沿用 updateIssueBaseBranchInput；弹窗里把长说明收进悬浮提示，只在多仓时留一行短提示。
function syncReqBaseBranch(){
  const input=$('#reqBaseBranch'),hint=$('#reqBaseBranchHint');
  updateIssueBaseBranchInput(input,hint,selectedReqRepositoryIds());
  if(!input||!hint)return;
  input.title=hint.textContent||'';
  hint.textContent=input.disabled?'只选一个仓库时可指定分支，否则从各自主分支新建':'';
  hint.hidden=!hint.textContent;
}
function selectedReqBaseBranch(){
  return selectedReqRepositoryIds().length===1?($('#reqBaseBranch')?.value.trim()||''):'';
}
function reqRepoNames(ids){
  const repos=reqProjectRepositories();
  const names=(ids||[]).map(id=>repos.find(r=>r.id===id)?.name||id);
  if(!names.length)return '未选择';
  return names.length===1?names[0]:`${names[0]} 等 ${names.length} 个`;
}
function renderReqRepoChip(){
  const chip=$('#reqRepoChip');
  if(!chip)return;
  chip.hidden=!reqProjectRepositories().length;
  const label=$('#reqRepoChipLabel');
  if(!label)return;
  const branch=selectedReqBaseBranch();
  label.innerHTML=`仓库 <b>${escapeHTML(reqRepoNames(selectedReqRepositoryIds()))}</b>${branch?` · ${escapeHTML(branch)}`:''}`;
}
function toggleReqRepoPopover(open){
  const pop=$('#reqRepoPopover');
  if(!pop)return;
  const next=open===undefined?pop.hidden:!!open;
  pop.hidden=!next;
  $('#reqRepoChip')?.classList.toggle('is-open',next);
  $('#reqRepoChipToggle')?.setAttribute('aria-expanded',String(next));
}
function renderReqContext(){
  const host=$('#reqRepository');
  if(host){
    if(reqProjectRepositories().length)fillReqRepository();
    else{host.dataset.ids='[]';renderReqRepositoryPicker()}
  }
  renderReqRepoChip();
  fillReqWorkflow();
  renderReqDelivery();
}
function reqFooterNote(){
  return reqUsesSOP()?'建单后可在详情里触发 SOP，也可以先去对话里把需求聊清楚':'不绑定 SOP，定时任务也不会自动认领；以后想跑 SOP 可在详情里选择';
}
function renderReqFooter(){
  const creating=!!state.reqCreating;
  const reason=reqDescription()?'':'先写一句需求';
  const create=$('#reqCreate'),chat=$('#reqCreateChat');
  if(create){
    create.disabled=creating||!!reason;
    create.title=reason;
    create.innerHTML=creating==='issue'?'<span class="req-spin" aria-hidden="true"></span>正在创建…':'创建需求';
  }
  if(chat){
    chat.disabled=creating||!!reason;
    chat.title=reason||'建单后打开 AI 工作台，新对话里已 @ 这个需求';
    chat.innerHTML=creating==='chat'?'<span class="req-spin" aria-hidden="true"></span>正在创建…':`${reqIcon('chat')}创建并去对话澄清`;
  }
  const note=$('#reqComposerHint');
  if(note)note.textContent=reqFooterNote();
  hydrateReqIcons($('#reqFooter'));
}
function renderReqTarget(){
  const tapdReady=projectTAPDReady();
  if(state.reqTarget==='tapd'&&!tapdReady)state.reqTarget='local';
  const target=state.reqTarget||'local';
  $$('#reqTarget [data-req-target]').forEach(btn=>{
    const on=btn.dataset.reqTarget===target;
    btn.classList.toggle('is-on',on);
    btn.setAttribute('aria-checked',String(on));
    const ind=btn.querySelector('.req-choice-ind');
    if(ind)ind.innerHTML=on?reqIcon('check',11):'';
    if(btn.dataset.reqTarget==='tapd'){
      btn.disabled=!tapdReady;
      btn.hidden=!tapdReady;
      btn.classList.toggle('is-unavailable',!tapdReady);
    }
  });
  const field=$('#reqTarget')?.closest('.req-field');
  if(field)field.hidden=!tapdReady;
  const hint=$('#reqTargetTAPDHint');
  if(hint)hint.textContent=tapdReady?'先在平台建单，再同步到 TAPD':'未配置';
  hydrateReqIcons($('#reqTarget'));
}
function setReqTarget(target){
  if(target==='tapd'&&!projectTAPDReady())return;
  state.reqTarget=target==='tapd'?'tapd':'local';
  renderReqTarget();
  persistRequirementComposer();
}
function updateReqCharCount(){
  const count=$('#reqCharCount'),len=($('#chatInput')?.value||'').length;
  if(count)count.textContent=len?`${len} 字`:'';
}
function onReqDescriptionInput(){
  updateReqCharCount();
  renderReqFooter();
  persistRequirementComposer();
}
function onReqBaseBranchInput(){
  renderReqRepoChip();
  persistRequirementComposer();
}
function requirementComposerOpen(){
  return !!$('#requirementModal')?.classList.contains('open');
}
function requirementComposerBusy(){
  return !!state.reqCreating;
}
function requirementComposerDirty(){
  return requirementComposerBusy()||!!reqDescription();
}
function persistRequirementComposer(){
  const modal=$('#requirementModal');
  if(!modal)return;
  if(!requirementComposerOpen()&&!requirementComposerDirty()){
    try{sessionStorage.removeItem('agentflow.requirementComposer')}catch{}
    return;
  }
  const draft={
    open:requirementComposerOpen(),
    projectId:currentProject()?.id||'',
    input:$('#chatInput')?.value||'',
    target:state.reqTarget||'local',
    delivery:reqUsesSOP()?'sop':'none',
    repoIds:selectedReqRepositoryIds(),
    baseBranch:selectedReqBaseBranch(),
    workflowId:selectedReqWorkflowId(),
    playbookId:selectedReqPlaybookId(),
  };
  try{sessionStorage.setItem('agentflow.requirementComposer',JSON.stringify(draft))}catch{}
}
function clearRequirementComposer(){
  try{sessionStorage.removeItem('agentflow.requirementComposer')}catch{}
}
function resetRequirementComposer(){
  Object.assign(state,{reqCreating:'',reqTarget:'local',reqDelivery:'sop'});
  ['#reqBaseBranch','#chatInput'].forEach(id=>{const el=$(id);if(el)el.value=''});
  renderReqContext();
  renderReqTarget();
  updateReqCharCount();
  renderReqFooter();
}
function readRequirementComposerDraft(){
  try{
    const raw=JSON.parse(sessionStorage.getItem('agentflow.requirementComposer')||'');
    return raw&&typeof raw==='object'?raw:null;
  }catch{return null}
}
function applyRequirementComposerDraft(raw){
  state.reqTarget=raw.target==='tapd'?'tapd':'local';
  state.reqDelivery=raw.delivery==='none'?'none':'sop';
  renderReqContext();
  if(Array.isArray(raw.repoIds))setReqRepositoryIds(raw.repoIds);
  const baseBranch=$('#reqBaseBranch');if(baseBranch)baseBranch.value=baseBranch.disabled?'':(raw.baseBranch||'');
  renderReqRepoChip();
  fillReqWorkflow(raw.workflowId||raw.playbookId);
  renderReqDelivery();
  const input=$('#chatInput');if(input)input.value=raw.input||'';
  renderReqTarget();
  updateReqCharCount();
  renderReqFooter();
}
function restoreRequirementComposer(){
  const raw=readRequirementComposerDraft();
  if(!raw?.open)return false;
  applyRequirementComposerDraft(raw);
  $('#requirementModal').classList.add('open');
  persistRequirementComposer();
  return true;
}
async function closeRequirementComposer(){
  if(requirementComposerBusy())return toast('正在建单，请稍候');
  $('#requirementModal').classList.remove('open');
  resetRequirementComposer();
  clearRequirementComposer();
}
function openRequirementWizard(){
  if(!(state.data.projects||[]).length){switchView('projects');return toast('请先创建项目')}
  if(requirementComposerOpen()){
    setTimeout(()=>$('#chatInput')?.focus(),0);
    return;
  }
  resetRequirementComposer();
  clearRequirementComposer();
  $('#requirementModal').classList.add('open');
  persistRequirementComposer();
  setTimeout(()=>$('#chatInput').focus(),0);
}
function parseClarifyPayload(data){
  if(data?.parsed && typeof data.parsed==='object') return normalizeClarifyParsed(data.parsed);
  const raw=typeof data?.result==='string'?data.result:(typeof data==='string'?data:JSON.stringify(data||{}));
  try{return normalizeClarifyParsed(JSON.parse(raw))}catch{}
  const start=raw.indexOf('{'), end=raw.lastIndexOf('}');
  if(start>=0&&end>start){try{return normalizeClarifyParsed(JSON.parse(raw.slice(start,end+1)))}catch{}}
  return {summary:'',questions:[],ready:false};
}
function normalizeClarifyParsed(parsed){
  const src=parsed&&typeof parsed==='object'?parsed:{};
  const questions=(Array.isArray(src.questions)?src.questions:[]).map((q,i)=>{
    if(typeof q==='string')return {id:`q${i+1}`,question:q,options:[]};
    const raw=Array.isArray(q?.options)?q.options:[];
    const recommended=raw.filter(o=>o&&typeof o==='object'&&o.recommended).map(clarifyOptionLabel).filter(Boolean);
    return {...q,question:String(q?.question||q?.prompt||''),options:clarifyQuestionOptions(q||{}),recommendedOptions:recommended.length?recommended:(q?.recommendedOptions||[])};
  }).filter(q=>q.question||(q.options||[]).length);
  return {...src, questions};
}
function clarifyToolLabel(tool){
  const name=String(tool||'').replace(/^mcp__agentflow_session__/,'');
  return ({Read:'读取',Grep:'搜索',Glob:'查找',MCP:'MCP',submit_clarification:'提交结论',submit_assistant_reply:'提交回复'})[name]||name||'操作';
}
function collectRequirementDraft(){
  const description=reqDescription();
  const first=(description.split('\n').map(l=>l.trim()).find(Boolean)||'');
  return {title:first.slice(0,80),description};
}
function rememberIssue(saved, prevId){
  if(!saved||!saved.id)return saved;
  if(!state.data.issues)state.data.issues=[];
  const prev=String(prevId||'').trim();
  if(prev&&saved.id!==prev){
    const oldIdx=state.data.issues.findIndex(i=>i.id===prev);
    if(oldIdx>=0)state.data.issues.splice(oldIdx,1);
  }
  const idx=state.data.issues.findIndex(i=>i.id===saved.id);
  if(idx>=0)state.data.issues[idx]=saved;else state.data.issues.unshift(saved);
  return saved;
}
async function pushExistingIssueToTAPD(issue){
  const updated=await api('/api/sync/create',{method:'POST',body:JSON.stringify({issueId:issue.id,target:'tapd'})});
  return rememberIssue(updated, issue.id);
}
async function pushIssueToTAPDFromDetail(issueId){
  if(!issueId)return;
  try{
    const current=(state.data.issues||[]).find(i=>i.id===issueId)||state.issueDetailData?.issue;
    if(!current)return toast('未找到工作项');
    if(!projectTAPDReady((state.data.projects||[]).find(p=>p.id===current.projectId)))return toast('当前项目未配置 TAPD，无法同步');
    const updated=await pushExistingIssueToTAPD(current);
    renderIssues();
    toast(`已在 TAPD 创建需求 ${updated.externalRef?.externalId||''}`,'ok');
    if(state.drawerOpen)openIssueDetail(updated.id);
  }catch(e){toast(e.message,'err')}
}
// createIssueFromChat 直接建单，不再在弹窗里澄清。mode 为 'chat' 时建单后打开
// AI 工作台的新对话，并 @ 这个需求，在对话里把需求聊清楚。
async function createIssueFromChat(target,mode){
  target=target||state.reqTarget||'local';
  const toChat=mode==='chat';
  if(target!=='local'&&target!=='tapd')return toast('请选择平台或 TAPD');
  const draft=collectRequirementDraft();
  if(!draft.description){$('#chatInput')?.focus();return toast('先写一句需求')}
  if(target==='tapd'&&!projectTAPDReady())return toast('当前项目未配置 TAPD');
  if(state.reqCreating)return toast('正在建单，请勿重复点击');
  const useSOP=reqUsesSOP();
  const workflowId=selectedReqWorkflowId();
  if(useSOP&&!workflowId)return toast('请选择 SOP，或改为不使用 SOP');
  state.reqCreating=toChat?'chat':'issue';
  renderReqFooter();
  const project=currentProject();
  try{
    let issue=await api('/api/issues',{method:'POST',body:JSON.stringify({
      projectId:project?.id,
      status:'backlog',
      kind:'requirement',
      sopMode:useSOP?'':'none',
      workflowId,
      playbookId:selectedReqPlaybookId(),
      repositoryId:selectedReqRepositoryId(),
      repositoryIds:selectedReqRepositoryIds(),
      baseBranch:selectedReqBaseBranch(),
      assigneeUserId:Number(state.currentUser?.id)||0,
      title:draft.title||'新工作项',
      description:draft.description,
      priority:'medium',
    })});
    rememberIssue(issue);
    let syncError='';
    if(target==='tapd'){
      try{issue=await pushExistingIssueToTAPD(issue)}catch(err){syncError=err.message}
    }
    state.reqCreating='';
    clearRequirementComposer();
    $('#requirementModal').classList.remove('open');
    resetRequirementComposer();
    renderIssues();
    if(syncError)toast('已在平台建单，TAPD 同步失败：'+syncError,'err');
    else if(target==='tapd')toast(`已创建需求并同步 TAPD ${issue.externalRef?.externalId||''}`,'ok');
    else toast(toChat?'已创建需求，在对话里继续澄清':(useSOP?'已在平台创建需求，可在详情里触发 SOP':'已创建需求（不使用 SOP）'),'ok');
    if(toChat)openIssueInChat(issue);
    else openIssueDetail(issue.id);
  }catch(err){toast(err.message,'err')}
  finally{state.reqCreating='';renderReqFooter()}
}
