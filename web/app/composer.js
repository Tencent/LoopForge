let chatHistory=[];
let chatClarifySessionId='';
let chatClarifyJobId='';
let chatClarifyStopping=false;
let chatAbortController=null;
function reqCreateDirect(){
  return state.reqCreateMode==='direct';
}
function composerCreateReady(){
  return !!state.reqUserAccepted||!!state.chatClarifyResult?.ready;
}
function destLabelHTML(target){
  if(reqCreateDirect()){
    if(target==='tapd')return '<b>TAPD 需求</b><span>跳过分析，只建单</span>';
    return '<b>平台需求</b><span>跳过分析，只建单</span>';
  }
  if(composerCreateReady()){
    if(target==='tapd')return '<b>TAPD 需求</b><span>同步 TAPD 并进入 SOP</span>';
    return '<b>平台需求</b><span>建单并进入研发 SOP</span>';
  }
  if(target==='tapd')return '<b>TAPD 需求</b><span>先建单，详情里继续澄清</span>';
  return '<b>平台需求</b><span>先建单，详情里继续澄清</span>';
}
function composerSendLabel(result){
  const r=result||state.chatClarifyResult;
  if(state.composerClarifyRetry){
    const reply=collectChatReply(),last=(chatHistory||[]).filter(h=>h.role==='user').pop();
    if(!reply||reply===last?.content)return '重新分析';
  }
  if(!r)return '开始分析';
  return r.ready?'补充说明':'提交回答并继续分析';
}
function setComposerRetryMode(on){
  state.composerClarifyRetry=!!on;
  syncComposerReplyState();
}
function composerClarifyQuestions(aiResult){
  const questions=normalizeClarifyParsed(aiResult).questions;
  if(questions.length||!aiResult||aiResult.ready)return questions;
  // Older responses may only contain missing information. Preserve it as
  // free-text questions; business choices must come from the analyst.
  return composerAssessmentView(aiResult).missingNames.map((question,i)=>({id:`missing${i+1}`,question:String(question),options:[],freeText:true}));
}
function syncComposerReplyState(){
  const send=$('#chatSend'),input=$('#chatInput'),hint=$('#reqComposerHint');
  if(!send)return;
  const reply=collectChatReply(),result=state.chatClarifyResult;
  const retry=state.composerClarifyRetry&&(chatHistory||[]).some(h=>h.role==='user');
  send.disabled=requirementComposerBusy()||(!reply&&!retry);
  send.textContent=composerSendLabel();
  if(requirementComposerBusy())return;
  if(state.composerClarifyRetry){
    if(hint)hint.textContent='本轮未完成，可重新分析，也可补充说明后提交。';
    return;
  }
  if(!result)return;
  if(result.ready||state.reqUserAccepted){
    if(input)input.placeholder='还有要补充的，可以继续写';
    if(hint)hint.textContent=reply?'提交补充内容后会重新分析。':'信息已确认，可以建单；如需继续分析，请先补充说明。';
    return;
  }
  const questions=composerClarifyQuestions(result),hasOptions=questions.some(q=>clarifyQuestionOptions(q).length);
  if(input)input.placeholder=hasOptions?'选择上方选项，或在这里补充说明':(questions.length?'请回答上方问题，也可以在这里补充说明':'请根据上方分析补充说明');
  if(hint)hint.textContent=reply?'回答已填写，可以提交并继续分析。':(hasOptions?'请先选择答案或填写补充说明，再提交分析。':(questions.length?'请先填写问题的回答，再提交分析。':'请先补充说明，或点击上方「重新生成问题」。'));
}
function composerClarifyRound(){
  return (chatHistory||[]).filter(h=>h.role==='assistant').length;
}
function composerAssessmentView(aiResult){
  const policy=clarificationPolicy(currentProject());
  const dims=policy.dimensions||[];
  const rating=aiResult?.assessment||{};
  const ratings=rating.dimensions&&typeof rating.dimensions==='object'?rating.dimensions:{};
  const missing=Array.isArray(rating.missing)?rating.missing:[];
  const missingNames=missing.map(m=>{
    const dim=dims.find(d=>d.id===m);
    return dim?dim.name:m;
  }).filter(Boolean);
  let weighted=0,weight=0;
  dims.forEach(d=>{
    if(ratings[d.id]===undefined)return;
    const w=Number(d.weight)||0;
    weighted+=(Number(ratings[d.id])||0)*w;
    weight+=w;
  });
  return {dims,ratings,missing,missingNames,score:weight?Math.round(weighted/weight):0,threshold:policy.threshold||80,ready:!!aiResult?.ready};
}
function composerProgressHTML(aiResult){
  if(!aiResult)return '';
  const round=composerClarifyRound()||1;
  const view=composerAssessmentView(aiResult);
  const scoreBit=Object.keys(view.ratings).length?`完整度约 ${view.score} 分，项目准入线 ${view.threshold} 分。`:'没有固定轮数，分析师会按项目准入线判断。';
  if(aiResult.ready){
    return `<div class="req-progress is-ready"><div class="req-progress-badge">可以建单</div><div class="req-progress-copy"><b>分析师认为信息已经够用</b><span>第 ${round} 轮。${scoreBit}${projectTAPDReady()?'点下方「平台需求」或「TAPD 需求」':'点下方「平台需求」'}，建单后会按所选 SOP 启动。还要补充就写在输入框，再点「补充说明」。</span></div></div>`;
  }
  if(state.reqUserAccepted){
    return `<div class="req-progress is-ready"><div class="req-progress-badge">可以建单</div><div class="req-progress-copy"><b>你已确认可以建单</b><span>第 ${round} 轮。${scoreBit}不必再等分析师达标。${projectTAPDReady()?'点下方「平台需求」或「TAPD 需求」':'点下方「平台需求」'}，建单后会按所选 SOP 启动。</span></div></div>`;
  }
  const leftover=view.missingNames.length?view.missingNames:(aiResult.questions||[]).map(q=>q.question).filter(Boolean);
  const leftoverHTML=leftover.length?`<div class="req-progress-missing">${leftover.slice(0,4).map(n=>`<span>${escapeHTML(n)}</span>`).join('')}</div>`:'';
  return `<div class="req-progress"><div class="req-progress-badge warn">第 ${round} 轮</div><div class="req-progress-copy"><b>还在澄清，没有固定轮数</b><span>${scoreBit}请先回答问题或补充说明，再提交分析。也可以点「按当前结论建单」，确认后建单并启动 SOP。</span>${leftoverHTML}</div></div>`;
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
    if(market==='70'||market==='tapd'||market==='tapd_taihu')return true;
    return /tapd/i.test(`${s.id||''} ${s.name||''} ${s.url||''}`);
  });
}
function issueCanPushTAPD(issue){
  if(!issue||trackerRefOf(issue)||(issue.tapdId||'').trim())return false;
  return projectTAPDReady((state.data.projects||[]).find(p=>p.id===issue.projectId)||currentProject());
}
function fillReqRepository(){
  const host=$('#reqRepository');
  if(!host)return;
  const project=currentProject();
  const repos=(state.data.repositories||[]).filter(r=>r.projectId===project?.id);
  // 默认全选项目下所有仓库
  host.dataset.ids=JSON.stringify(repos.map(r=>r.id));
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
}
function selectedReqWorkflowId(){
  return $('#reqWorkflow')?.value.trim()||defaultSOPForIssue({projectId:state.projectFilter,kind:'requirement'});
}
function selectedReqPlaybookId(){
  const wf=$('#reqWorkflow')?.value.trim()||'';
  const books=projectPlaybooks(currentProject());
  const hit=books.find(b=>(b.bindings||[]).some(x=>x.workflowId===wf));
  return hit?.id||'';
}
function setReqRepositoryIds(ids,preserveAnalysis=false){
  const host=$('#reqRepository');
  if(!host)return;
  const project=currentProject();
  const repos=(state.data.repositories||[]).filter(r=>r.projectId===project?.id);
  const known=new Set(repos.map(r=>r.id));
  const next=[...new Set((ids||[]).filter(id=>known.has(id)))];
  const changed=JSON.stringify([...selectedReqRepositoryIds()].sort())!==JSON.stringify([...next].sort());
  host.dataset.ids=JSON.stringify(next);
  renderReqRepositoryPicker();
  if(changed&&!preserveAnalysis)invalidateRequirementAnalysis();
  persistRequirementComposer();
}
function renderReqRepositoryPicker(){
  const host=$('#reqRepository');
  if(!host)return;
  const project=currentProject();
  const repos=(state.data.repositories||[]).filter(r=>r.projectId===project?.id);
  let selected=[];
  try{selected=JSON.parse(host.dataset.ids||'[]')}catch{selected=[]}
  const chosen=new Set(selected);
  const leftover=repos.filter(r=>!chosen.has(r.id));
  const tags=selected.map(id=>{
    const r=repos.find(item=>item.id===id);
    const name=r?.name||id;
    const isDef=id===project?.defaultRepositoryId;
    return `<button type="button" class="repo-picker-tag" data-remove-req-repo="${escapeHTML(id)}" title="点击移除">${escapeHTML(name)}${isDef?'（默认）':''}<span aria-hidden="true">×</span></button>`;
  }).join('');
  const options=leftover.map(r=>`<option value="${escapeHTML(r.id)}">${escapeHTML(r.name)}</option>`).join('');
  const placeholder=!repos.length?'暂无可选仓库':(!leftover.length?'已全部选择':'＋ 添加仓库…');
  host.innerHTML=`<div class="repo-picker-field">${tags}<select class="repo-picker-select"${leftover.length?'':' disabled'}><option value="">${placeholder}</option>${options}</select></div>`;
  host.querySelectorAll('[data-remove-req-repo]').forEach(b=>b.onclick=()=>{
    const next=selected.filter(id=>id!==b.dataset.removeReqRepo);
    setReqRepositoryIds(next);
  });
  const sel=host.querySelector('select');
  if(sel)sel.onchange=()=>{
    if(!sel.value)return;
    setReqRepositoryIds([...selected,sel.value]);
  };
  updateIssueBaseBranchInput($('#reqBaseBranch'),$('#reqBaseBranchHint'),selectedReqRepositoryIds());
}
function selectedReqBaseBranch(){
  return selectedReqRepositoryIds().length===1?($('#reqBaseBranch')?.value.trim()||''):'';
}
function invalidateRequirementAnalysis(){
  if(!chatHistory.length&&!state.chatClarifyResult&&!chatClarifySessionId)return;
  const draft=collectRequirementDraft();
  chatHistory=[];chatClarifySessionId='';chatClarifyJobId='';
  state.chatClarifyResult=null;state.reqUserAccepted=false;state.reqCreateMode=null;state.reqClarifyStartedAt='';state.composerClarifyRetry=false;
  const input=$('#chatInput');if(input){input.value=draft.description;input.placeholder='写清楚要做什么，点击「开始分析」或「直接创建」。'}
  const panel=$('#reqClarifyPanel');if(panel){panel.hidden=true;panel.innerHTML=''}
  clarifyThinkingReset();
  syncReqComposerLayout();
  setChatCreateEnabled(false);
  syncComposerReplyState();
  const hint=$('#reqComposerHint');if(hint)hint.textContent='仓库或继续开发分支已变更，请重新分析，也可以直接创建。';
}
function renderReqContext(){
  const line=$('#reqContextLine');
  const tapdReady=projectTAPDReady();
  if(line) line.textContent=tapdReady?'写清楚要做什么，选好仓库和要启动的 SOP。先分析再创建，也可以直接创建。':'写清楚要做什么，选好仓库和要启动的 SOP。';
  const project=currentProject();
  const repos=(state.data.repositories||[]).filter(r=>r.projectId===project?.id);
  const row=$('#reqRepoRow'),sel=$('#reqRepository');
  if(row&&sel){
    if(repos.length){
      row.hidden=false;
      fillReqRepository();
    }else{
      sel.dataset.ids='[]';
      sel.innerHTML='';
      row.hidden=true;
    }
  }
  fillReqWorkflow();
  const hint=$('#reqComposerHint');
  if(hint) hint.textContent=repos.length?'':'当前项目还没绑定仓库，会按描述澄清。';
}
function collectChatReply(){
  const typed=$('#chatInput')?.value.trim()||'';
  const parts=[];
  $$('#reqClarifyPanel .chat-question').forEach(question=>{
    const text=question.querySelector('.chat-question-text')?.textContent?.replace(/^\d+\.\s*/,'')||'';
    const selected=[...question.querySelectorAll('.chat-option.selected')].map(b=>b.textContent.trim()).filter(Boolean);
    const other=question.querySelector('.chat-other-input')?.value.trim()||'';
    const answer=[...selected,other].filter(Boolean).join('、');
    if(answer)parts.push(`${text}：${answer}`);
  });
  if(typed) parts.push(typed);
  return parts.join('\n');
}
function usableClarifySummary(text){
  const t=String(text||'').trim();
  if(!t) return '';
  if(/没有返回摘要|no summary/i.test(t)) return '';
  return t;
}
function bindClarifyOptions(){
  $$('#reqClarifyPanel .chat-option').forEach(btn=>{
    btn.onclick=()=>{
      const qIdx=btn.dataset.q;
      const wasSelected=btn.classList.contains('selected');
      if(btn.dataset.multi==='true'){
        btn.classList.toggle('selected');
      }else{
        $$(`#reqClarifyPanel .chat-option[data-q="${qIdx}"]`).forEach(b=>b.classList.remove('selected'));
        if(!wasSelected)btn.classList.add('selected');
      }
      if(btn.classList.contains('selected')){
        const other=btn.closest('.chat-question')?.querySelector('.chat-other-input');
        if(other)other.value='';
      }
      $$('#reqClarifyPanel .chat-option').forEach(b=>b.setAttribute('aria-pressed',String(b.classList.contains('selected'))));
      syncComposerReplyState();
      persistRequirementComposer();
    };
  });
  $$('#reqClarifyPanel .chat-other-input').forEach(inp=>{
    inp.oninput=()=>{
      const qIdx=inp.dataset.q;
      $$(`#reqClarifyPanel .chat-option[data-q="${qIdx}"]`).forEach(b=>{b.classList.remove('selected');b.setAttribute('aria-pressed','false')});
      syncComposerReplyState();
      persistRequirementComposer();
    };
  });
}
function syncReqComposerLayout(){
  const modal=$('.req-composer');
  const panel=$('#reqClarifyPanel');
  if(!modal)return;
  modal.classList.toggle('has-clarify',!!(panel&&!panel.hidden&&panel.innerHTML.trim()));
}
function renderClarifyPanel(aiResult, err){
  const panel=$('#reqClarifyPanel');
  if(!panel)return;
  let html='';
  if(err){
    panel.hidden=false;
    panel.innerHTML=html+`<p class="req-error">${escapeHTML(err)}</p><button type="button" class="button primary req-retry" id="chatClarifyRetry">重新分析</button>`;
    setComposerRetryMode(true);
    $('#chatClarifyRetry')?.addEventListener('click',()=>sendChatClarify(true));
    syncReqComposerLayout();
    return;
  }
  const repoNotice=clarifyRepoNoticeHTML(aiResult?.repository);
  if(repoNotice) html+=repoNotice;
  const summary=usableClarifySummary(aiResult?.summary);
  const questions=composerClarifyQuestions(aiResult);
  if(aiResult) html+=composerProgressHTML(aiResult);
  if(summary) html+=`<p class="req-summary">${escapeHTML(summary)}</p>`;
  if(aiResult&&!aiResult.ready&&!normalizeClarifyParsed(aiResult).questions.length){
    html+='<p class="req-error">本轮未生成具体问题。你可以填写待确认信息或补充说明，也可以重新生成问题。</p><button type="button" class="button ghost req-retry" id="chatClarifyRetry">重新生成问题</button>';
  }
  if(questions.length){
    html+='<div class="chat-questions">';
    questions.forEach((q,i)=>{
      const options=clarifyQuestionOptions(q);
      html+=`<div class="chat-question"><div class="chat-question-text" id="reqQuestionText-${i}">${i+1}. ${escapeHTML(q.question)}</div><div class="chat-question-options">`;
      options.forEach(opt=>{html+=`<button type="button" class="chip chat-option" data-q="${i}" data-multi="${q.multiSelect||false}" aria-pressed="false">${escapeHTML(opt)}</button>`;});
      html+=`</div><input type="text" class="chat-other-input" data-q="${i}" aria-labelledby="reqQuestionText-${i}" placeholder="${options.length?'其他：自己填写':'请填写回答'}" /></div>`;
    });
    html+='</div>';
  }
  panel.hidden=!html;
  panel.innerHTML=html;
  bindClarifyOptions();
  $('#chatClarifyRetry')?.addEventListener('click',()=>sendChatClarify(true));
  syncReqComposerLayout();
}
// clarifyRepoNoticeHTML 在澄清结果顶部渲染"未拉到代码"的醒目告警，
// 确保 analyzed=false 时用户明确知道结论未基于真实代码，不被误导。
function clarifyRepoNoticeHTML(repo){
  if(!repo||repo.analyzed) return '';
  const name=repo.name||repo.id||'目标仓库';
  const reason=repo.reason||'未能拉取代码仓库';
  return `<div class="req-alert req-alert-warn" role="alert">`+
    `<span class="req-alert-icon">⚠️</span>`+
    `<div class="req-alert-body">`+
      `<div class="req-alert-title">未能读取代码仓库「${escapeHTML(String(name))}」</div>`+
      `<div class="req-alert-desc">原因：${escapeHTML(stripClarifyReason(reason))}。以下澄清<strong>未基于真实代码</strong>，仅供参考，请先解决仓库拉取问题后再建单。</div>`+
    `</div></div>`;
}
// clarifyRepoNoticeStream 在流式思考阶段就给出早期提示（仅拉取失败时）。
function clarifyRepoNoticeStream(repo){
  if(!repo||repo.analyzed) return;
  const box=$('#reqThinkingStream');
  if(!box) return;
  box.hidden=false;
  const name=repo.name||repo.id||'目标仓库';
  const row=document.createElement('div');
  row.className='think-row think-warn';
  row.innerHTML=`<span class="think-icon">⚠️</span><span class="think-text">未拉取到代码仓库「${escapeHTML(String(name))}」，本次分析不基于真实代码</span>`;
  box.insertBefore(row,box.firstChild);
}
function stripClarifyReason(reason){
  return String(reason||'').replace(/\u001b\[[0-9;]*m/g,'').replace(/\[([0-9]{1,3}(;[0-9]{1,3})*)m/g,'').replace(/\s+/g,' ').trim();
}
function requirementComposerOpen(){
  return !!$('#requirementModal')?.classList.contains('open');
}
function requirementComposerBusy(){
  return !!chatAbortController||!!state.chatSyncTarget;
}
function requirementComposerDirty(){
  if(requirementComposerBusy())return true;
  if((chatHistory||[]).length)return true;
  if(state.chatClarifyResult)return true;
  if(($('#chatInput')?.value||'').trim())return true;
  return false;
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
    history:chatHistory||[],
    sessionId:chatClarifySessionId||'',
    clarificationStartedAt:state.reqClarifyStartedAt||'',
    jobId:chatClarifyJobId||'',
    clarifyResult:state.chatClarifyResult||null,
    reqCreateMode:state.reqCreateMode||null,
    reqUserAccepted:!!state.reqUserAccepted,
    destOpen:!$('#reqDestinations')?.hidden,
    repoIds:selectedReqRepositoryIds(),
    baseBranch:selectedReqBaseBranch(),
    workflowId:selectedReqWorkflowId(),
    playbookId:selectedReqPlaybookId(),
    sendText:$('#chatSend')?.textContent||'开始分析',
    hint:$('#reqComposerHint')?.textContent||'',
    interrupted:requirementComposerBusy()
  };
  try{sessionStorage.setItem('agentflow.requirementComposer',JSON.stringify(draft))}catch{}
}
function clearRequirementComposer(){
  try{sessionStorage.removeItem('agentflow.requirementComposer')}catch{}
}
function paintRequirementComposerChrome(){
  const picker=$('#reqClarifyModelPicker');
  if(picker){
    bindClarifyModelPicker(picker);
    const api=window.AgentFlowAssistantModels,toggle=picker.querySelector('[data-model-toggle]');
    if(api&&toggle)api.paint(toggle, selectedClarifyModel());
  }
}
function resetRequirementComposer(){
  state.requirementIssueId=null;state.chatClarifyResult=null;state.chatSyncTarget=null;state.reqCreateMode=null;state.reqUserAccepted=false;state.composerClarifyRetry=false;state.reqClarifyStartedAt='';chatHistory=[];chatClarifySessionId='';
  if(chatAbortController)abortChatClarify();
  chatClarifyJobId='';
  chatClarifyStopping=false;
  const baseBranch=$('#reqBaseBranch');if(baseBranch)baseBranch.value='';
  renderReqContext();
  const panel=$('#reqClarifyPanel');if(panel){panel.hidden=true;panel.innerHTML=''}
  syncReqComposerLayout();
  clarifyThinkingReset();
  const input=$('#chatInput');
  if(input){
    input.value='';
    input.placeholder='例如：个人主页增加徽章，需要兼容现有成就体系。\n（Enter 换行；「开始分析」问清再建单并启动 SOP，「直接创建」跳过分析）';
  }
  setChatCreateEnabled(false);
  const send=$('#chatSend');if(send)send.textContent=composerSendLabel();
  const hint=$('#reqComposerHint');if(hint&&!hint.textContent) hint.textContent=selectedReqRepositoryIds().length?'':'当前项目还没绑定仓库，会按描述澄清。';
  syncComposerReplyState();
  paintRequirementComposerChrome();
}
function readRequirementComposerDraft(){
  try{
    const raw=JSON.parse(sessionStorage.getItem('agentflow.requirementComposer')||'');
    return raw&&typeof raw==='object'?raw:null;
  }catch{return null}
}
function requirementComposerDraftUseful(raw){
  if(!raw)return false;
  return !!(raw.open||(raw.input||'').trim()||(raw.history||[]).length||raw.clarifyResult);
}
function applyRequirementComposerDraft(raw){
  chatHistory=Array.isArray(raw.history)?raw.history:[];
  chatClarifySessionId=raw.sessionId||'';
  chatClarifyJobId=raw.jobId||'';
  state.reqClarifyStartedAt=raw.clarificationStartedAt||'';
  state.chatClarifyResult=raw.clarifyResult||null;
  state.reqCreateMode=raw.reqCreateMode||null;
  state.reqUserAccepted=!!raw.reqUserAccepted;
  if((raw.clarifyResult?.ready||raw.reqUserAccepted)&&state.reqCreateMode==='direct') state.reqCreateMode='analyzed';
  renderReqContext();
  if(Array.isArray(raw.repoIds))setReqRepositoryIds(raw.repoIds,true);
  const baseBranch=$('#reqBaseBranch');if(baseBranch)baseBranch.value=baseBranch.disabled?'':(raw.baseBranch||'');
  fillReqWorkflow(raw.workflowId||raw.playbookId);
  const input=$('#chatInput');
  if(input){
    input.value=raw.input||'';
    input.placeholder='例如：个人主页增加徽章，需要兼容现有成就体系。\n（Enter 换行；「开始分析」问清再建单并启动 SOP，「直接创建」跳过分析）';
  }
  const reconnect=!!(raw.interrupted&&raw.jobId);
  const interruptHint=reconnect?'正在接上分析…':'刷新后分析已中断，可继续。';
  const lastAssistant=(chatHistory||[]).filter(h=>h.role==='assistant').pop();
  if(raw.clarifyResult){
    renderClarifyPanel(raw.clarifyResult);
  }else if(lastAssistant){
    try{renderClarifyPanel(JSON.parse(lastAssistant.content))}catch{renderClarifyPanel(null, interruptHint)}
  }else if((chatHistory||[]).some(h=>h.role==='user')||reconnect){
    renderClarifyPanel(null, interruptHint);
  }
  setChatCreateEnabled(!!raw.destOpen||state.reqCreateMode==='direct'||!!raw.clarifyResult?.ready||!!state.reqUserAccepted);
  const send=$('#chatSend');if(send)send.textContent=raw.sendText||'开始分析';
  const hint=$('#reqComposerHint');
  if(hint) hint.textContent=reconnect?'正在接上分析…':(raw.interrupted?'刷新后分析已中断，可继续提问或直接创建。':(raw.hint||hint.textContent||''));
  if(!reconnect)syncComposerReplyState();
  paintRequirementComposerChrome();
}
function restoreRequirementComposer(){
  const raw=readRequirementComposerDraft();
  if(!raw?.open||!requirementComposerDraftUseful(raw))return false;
  applyRequirementComposerDraft(raw);
  $('#requirementModal').classList.add('open');
  if(raw.interrupted&&raw.jobId){
    resumeChatClarify(raw.jobId);
    toast('新建需求已恢复，正在接上分析');
  }else if(raw.interrupted){
    toast('新建需求已恢复。进行中的分析已中断，可继续。');
  }
  persistRequirementComposer();
  return true;
}
async function closeRequirementComposer(){
  if(requirementComposerBusy()){
    if(!await afConfirm({title:'分析仍在进行',message:'关闭会停止当前分析，并清空这次填写。下次打开「新建需求」会从空白开始。',confirmText:'关闭'}))return;
    abortChatClarify();
  }
  $('#requirementModal').classList.remove('open');
  resetRequirementComposer();
  clearRequirementComposer();
}
function openRequirementWizard(){
  if(!(state.data.projects||[]).length){switchView('projects');return toast('请先创建项目')}
  if(requirementComposerOpen()){
    paintRequirementComposerChrome();
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
    return {...q,question:String(q?.question||q?.prompt||''),options:clarifyQuestionOptions(q||{})};
  }).filter(q=>q.question||(q.options||[]).length);
  return {...src, questions};
}
function clarifyThinkingReset(){
  const box=$('#reqThinkingStream');
  if(box){box.hidden=true;box.innerHTML='';box.className='req-thinking-now';}
}
function clarifyToolLabel(tool){
  const name=String(tool||'').replace(/^mcp__agentflow_session__/,'');
  return ({Read:'读取',Grep:'搜索',Glob:'查找',MCP:'MCP',submit_clarification:'提交结论',submit_assistant_reply:'提交回复'})[name]||name||'操作';
}
function clarifyThinkingPush(ev){
  if(ev.kind!=='tool'&&ev.kind!=='status'&&clarifyThoughtShouldHide(ev&&ev.text))return;
  const box=$('#reqThinkingStream');
  if(!box)return;
  let text='正在分析…', icon='⏳', cls='think-status';
  if(ev.kind==='tool'){
    icon='🔍'; cls='think-tool';
    text=`${clarifyToolLabel(ev.tool)}${ev.detail?' · '+ev.detail:''}`;
  }else if(ev.kind==='status'){
    text=ev.text||'正在分析…';
  }else{
    icon='💭'; cls='think-idea';
    text=ev.text||'正在分析…';
  }
  box.hidden=false;
  box.className='req-thinking-now '+cls;
  box.innerHTML=`<span class="think-icon">${icon}</span><span class="think-text">${escapeHTML(text)}</span>`;
}
async function sendChatClarify(retry){
  if(requirementComposerBusy())return;
  const input=$('#chatInput');
  const reply=collectChatReply();
  const last=(chatHistory||[]).filter(h=>h.role==='user').pop();
  let msg='';
  if(retry&&(!reply||reply===last?.content)){
    if(!last)return toast('没有可重试的需求');
    msg=last.content;
  }else{
    msg=reply;
    if(!msg){
      syncComposerReplyState();
      (document.querySelector('#reqClarifyPanel .chat-other-input')||input)?.focus();
      return;
    }
    const followup=(chatHistory||[]).some(h=>h.role==='user');
    chatHistory.push({role:'user',content:msg});
    if(input && followup) input.value='';
  }
  state.composerClarifyRetry=false;
  const hint=$('#reqComposerHint');
  if(input)input.disabled=true;
  setChatSendAnalyzing(true);
  if(hint)hint.textContent='正在分析…';
  if(!state.chatClarifyResult){
    const panel=$('#reqClarifyPanel');
    if(panel){panel.hidden=true;panel.innerHTML='';}
    syncReqComposerLayout();
  }
  clarifyThinkingReset();
  clarifyThinkingPush({kind:'status',text:'正在准备分析…'});
  chatClarifyStopping=false;
  chatClarifyJobId=newComposerJobId();
  chatAbortController=new AbortController();
  persistRequirementComposer();
  try{
    const project=currentProject();
    const resp=await fetch('/api/chat-clarify',{method:'POST',headers:{'Content-Type':'application/json','Accept':'text/event-stream'},body:JSON.stringify({
      projectId:project.id,
      repositoryId:selectedReqRepositoryId(),
      repositoryIds:selectedReqRepositoryIds(),
      baseBranch:selectedReqBaseBranch(),
      title:chatHistory[0]?.content?.slice(0,80)||msg.slice(0,80),
      description:chatHistory.filter(h=>h.role==='user').map(h=>h.content).join('\n\n'),
      message:msg,
      model:selectedClarifyModel(),
      sessionId:chatClarifySessionId,
      jobId:chatClarifyJobId,
      history:chatHistory
    }),signal:chatAbortController.signal});
    if(!resp.ok){
      let em=`HTTP ${resp.status}`;try{const d=await resp.json();em=d.error||em;}catch{}
      throw new Error(em);
    }
    const data=await readChatClarifyResponse(resp,hint);
    applyChatClarifyOutcome(data,hint);
    chatClarifyJobId='';
  }catch(err){
    if(err.name==='AbortError'){
      if(chatClarifyStopping){
        if(!retry&&chatHistory.length&&chatHistory[chatHistory.length-1].role==='user')chatHistory.pop();
        if(hint)hint.textContent='已停止分析';
        renderClarifyPanel(null,'已停止，可修改需求后重新分析。');
      }
    }else{
      chatClarifyJobId='';
      renderClarifyPanel(null, `分析失败：${err.message}`);
      if(hint)hint.textContent='可点击重新分析';
    }
  }finally{
    finishComposerClarifyStream(input);
  }
}
function newComposerJobId(){
  return 'c'+Date.now().toString(36)+Math.random().toString(36).slice(2,8);
}
function resumeChatClarify(jobId){
  if(!jobId||chatAbortController)return;
  chatClarifyJobId=jobId;
  chatClarifyStopping=false;
  chatAbortController=new AbortController();
  const input=$('#chatInput');
  if(input)input.disabled=true;
  setChatSendAnalyzing(true);
  const hint=$('#reqComposerHint');
  if(hint)hint.textContent='正在接上分析…';
  attachComposerClarifyStream(jobId);
}
async function attachComposerClarifyStream(jobId){
  const hint=$('#reqComposerHint');
  const input=$('#chatInput');
  try{
    const resp=await fetch('/api/chat-clarify?jobId='+encodeURIComponent(jobId),{headers:{Accept:'text/event-stream'},signal:chatAbortController.signal});
    if(resp.status===404){
      chatClarifyJobId='';
      renderClarifyPanel(null,'刷新后分析已结束，可继续提问或直接创建。');
      if(hint)hint.textContent='分析已结束，可继续提问或直接创建。';
      return;
    }
    if(!resp.ok){
      let em=`HTTP ${resp.status}`;try{const d=await resp.json();em=d.error||em;}catch{}
      throw new Error(em);
    }
    const data=await consumeClarifyStream(resp,hint);
    applyChatClarifyOutcome(data,hint);
    chatClarifyJobId='';
  }catch(err){
    if(err.name==='AbortError'){
      if(chatClarifyStopping){
        if(hint)hint.textContent='已停止分析';
        renderClarifyPanel(null,'已停止，可修改需求后重新分析。');
      }
    }else{
      chatClarifyJobId='';
      renderClarifyPanel(null, `分析失败：${err.message}`);
      if(hint)hint.textContent='可点击重新分析';
    }
  }finally{
    finishComposerClarifyStream(input);
  }
}
async function readChatClarifyResponse(resp,hint){
  const isStream=(resp.headers.get('Content-Type')||'').includes('text/event-stream');
  if(isStream)return consumeClarifyStream(resp,hint);
  return resp.json();
}
function applyChatClarifyOutcome(data,hint){
  const input=$('#chatInput');
  if(data&&data.error) throw new Error(data.error);
  if(data?.startedAt&&!state.reqClarifyStartedAt)state.reqClarifyStartedAt=data.startedAt;
  const aiResult=parseClarifyPayload(data);
  aiResult.repository=(data&&data.repository)||null;
  chatClarifySessionId=data?.sessionId||data?.repository?.sessionId||chatClarifySessionId;
  if(!aiResult.ready && (!aiResult.questions || aiResult.questions.length===0) && !usableClarifySummary(aiResult.summary)){
    throw new Error('分析未返回有效问题，请再试一次');
  }
  chatHistory.push({role:'assistant',content:JSON.stringify(aiResult)});
  state.chatClarifyResult=aiResult;
  clarifyThinkingReset();
  renderClarifyPanel(aiResult);
  if(input)input.value='';
  if(aiResult.ready||state.reqUserAccepted){
    state.reqCreateMode='analyzed';
    if(hint)hint.textContent=aiResult.ready?(aiResult.suggestedTitle||'信息已经够用。点下方按钮建单并启动 SOP。'):(projectTAPDReady()?'你已确认可以建单。点下方按钮建到平台或 TAPD 并启动 SOP。':'你已确认可以建单。点下方按钮建单并启动 SOP。');
    if(input)input.placeholder='还有要补充的，可以继续写';
    setChatCreateEnabled(true);
  }else{
    state.reqCreateMode='partial';
    setChatCreateEnabled(false);
  }
  syncComposerReplyState();
}
function finishComposerClarifyStream(input){
  chatAbortController=null;
  chatClarifyStopping=false;
  clarifyThinkingReset();
  setChatSendAnalyzing(false);
  if(input){input.disabled=false;input.focus();}
  persistRequirementComposer();
}
// consumeClarifyStream reads a text/event-stream response, rendering "thought"
// events live and resolving with the payload of the final "done"/"error" event.
async function consumeClarifyStream(resp,hint){
  const reader=resp.body.getReader();
  const decoder=new TextDecoder();
  let buffer='',done=null,repository=null;
  const dispatch=(event,dataStr)=>{
    let payload={};try{payload=JSON.parse(dataStr);}catch{}
    if(event==='open'){
      // 最早可知的仓库状态：拉不到代码时先在思考流给出提示，避免误导
      if(payload&&payload.repository){repository=payload.repository;clarifyRepoNoticeStream(payload.repository);}
    }else if(event==='job'){
      if(payload&&payload.jobId){chatClarifyJobId=payload.jobId;if(payload.startedAt&&!state.reqClarifyStartedAt)state.reqClarifyStartedAt=payload.startedAt;persistRequirementComposer();}
    }else if(event==='thought'){
      clarifyThinkingPush(payload);
      if(hint&&payload.kind==='tool')hint.textContent=`${clarifyToolLabel(payload.tool)} ${payload.detail||''}`.trim();
      else if(hint&&payload.text)hint.textContent=payload.text;
      else if(hint)hint.textContent='分析师正在思考…';
    }else if(event==='done'){
      done=payload;
    }else if(event==='error'){
      done={error:payload.error||'分析失败'};
    }
  };
  for(;;){
    const {value,done:streamDone}=await reader.read();
    if(streamDone)break;
    buffer+=decoder.decode(value,{stream:true});
    let idx;
    while((idx=buffer.indexOf('\n\n'))>=0){
      const chunk=buffer.slice(0,idx);buffer=buffer.slice(idx+2);
      let event='message',dataStr='';
      chunk.split('\n').forEach(line=>{
        if(line.startsWith('event:'))event=line.slice(6).trim();
        else if(line.startsWith('data:'))dataStr+=line.slice(5).trim();
      });
      if(dataStr)dispatch(event,dataStr);
    }
  }
  if(done&&!done.repository&&repository)done.repository=repository;
  return done||{error:'分析未返回结果'};
}
function abortChatClarify(){
  chatClarifyStopping=true;
  const jobId=chatClarifyJobId;
  chatClarifyJobId='';
  if(chatAbortController){
    chatAbortController.abort();
    chatAbortController=null;
  }
  fetch('/api/chat-clarify/cancel',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({jobId:jobId||''})}).catch(()=>{});
  persistRequirementComposer();
}
function setChatSendAnalyzing(analyzing){
  const send=$('#chatSend'),stop=$('#chatStop'),direct=$('#chatCreateDirect');
  if(send){
    if(analyzing){
      send.disabled=true;
      $$('#reqClarifyPanel .chat-option').forEach(b=>b.disabled=true);
      $$('#reqClarifyPanel .chat-other-input').forEach(inp=>inp.disabled=true);
    }else{
      send.disabled=false;
      send.textContent=composerSendLabel();
      $$('#reqClarifyPanel .chat-option').forEach(b=>b.disabled=false);
      $$('#reqClarifyPanel .chat-other-input').forEach(inp=>inp.disabled=false);
    }
  }
  if(stop)stop.hidden=!analyzing;
  if(direct){
    direct.disabled=!!analyzing;
    direct.hidden=!!analyzing;
  }
  const baseBranch=$('#reqBaseBranch');if(baseBranch)baseBranch.disabled=!!analyzing||selectedReqRepositoryIds().length!==1;
  $('#reqRepository')?.querySelectorAll('button,select').forEach(el=>{el.disabled=!!analyzing||(el.tagName==='SELECT'&&el.options.length<2)});
  const retry=$('#chatClarifyRetry');if(retry)retry.disabled=!!analyzing;
  if(!analyzing)syncComposerReplyState();
}
function collectRequirementDraft(){
  const reply=collectChatReply();
  const history=(chatHistory||[]).filter(h=>h.role==='user').map(h=>String(h.content||'').trim()).filter(Boolean);
  const parts=history.slice();
  if(reply&&!parts.includes(reply)) parts.push(reply);
  const description=parts.join('\n\n').trim();
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
function reqDestButton(target){
  return target==='tapd'?$('#chatCreateTAPD'):$('#chatCreateLocal');
}
async function createIssueDirect(){
  if(state.chatSyncTarget)return toast('正在建单，请勿重复点击');
  if(state.chatClarifyResult){
    if(!composerCreateReady()){
      const round=composerClarifyRound()||1;
      const view=composerAssessmentView(state.chatClarifyResult);
      const score=Object.keys(view.ratings).length?`当前完整度约 ${view.score} 分，准入线 ${view.threshold} 分。`:'分析师还没有给出完整度分数。';
      if(!await afConfirm({title:'分析还没完成，仍要建单？',message:`这是第 ${round} 轮，分析师还没标成可以建单。${score}确认后会带上当前结论${projectTAPDReady()?'，建到平台或 TAPD':'建单'}，并启动 SOP。`,confirmText:'按当前结论建单'}))return;
      state.reqUserAccepted=true;
      renderClarifyPanel(state.chatClarifyResult);
    }
    state.reqCreateMode='analyzed';
    setChatCreateEnabled(true);
    const hint=$('#reqComposerHint');
    if(hint) hint.textContent=(state.chatClarifyResult.ready?'信息已经够用。':'你已确认可以建单。')+(projectTAPDReady()?'选择平台或 TAPD，建单后会启动 SOP。':'建单后会启动 SOP。');
    $('#reqDestinations')?.scrollIntoView({block:'nearest'});
    return;
  }
  const draft=collectRequirementDraft();
  if(!draft.description)return toast('先写一句需求');
  if(!await afConfirm({title:'跳过分析直接建单？',message:'不会读取代码仓库，也不会自动启动 SOP。建单后如需问清楚，再到详情页点「开始澄清」。',confirmText:'跳过分析并建单'}))return;
  state.reqUserAccepted=false;
  state.reqCreateMode='direct';
  setChatCreateEnabled(true);
  const hint=$('#reqComposerHint');
  if(hint) hint.textContent='已跳过分析，选择建到哪里。不会立刻启动 SOP';
}
async function createIssueFromChat(target){
  if(target!=='local'&&target!=='tapd')return toast(projectTAPDReady()?'请选择平台或 TAPD':'请选择建单方式');
  const accepted=composerCreateReady();
  const direct=reqCreateDirect()||!state.chatClarifyResult;
  if(!direct&&!state.chatClarifyResult)return;
  if(direct){
    const draft=collectRequirementDraft();
    if(!draft.description)return toast('先写一句需求');
  }
  if(target==='tapd'&&!projectTAPDReady())return toast('当前项目未配置 TAPD');
  if(state.chatSyncTarget){return toast('正在建单，请勿重复点击')}
  state.chatSyncTarget=target;
  const r=state.chatClarifyResult;
  const project=currentProject();
  const btn=reqDestButton(target);
  try{
    ['chatCreateLocal','chatCreateTAPD'].forEach(id=>{const el=$('#'+id);if(el)el.disabled=true});
    if(btn){btn.classList.add('busy');btn.innerHTML='<b>正在创建…</b><span>请稍候</span>'}
    const draft=collectRequirementDraft();
    const playbookId=selectedReqPlaybookId();
    const workflowId=selectedReqWorkflowId();
    if(!workflowId)return toast('当前项目还没有可跑的流程，先到流程设计里配 SOP');
    let payload;
    if(direct){
      payload={
        projectId:project?.id,
        title:(r&&r.suggestedTitle)||draft.title||draft.description.slice(0,80)||'新工作项',
        description:draft.description,
        priority:(r&&r.suggestedPriority)||'medium',
        status:'backlog',
        kind:'requirement',
        workflowId,
        playbookId,
        repositoryId:selectedReqRepositoryId(),
        repositoryIds:selectedReqRepositoryIds(),
        baseBranch:selectedReqBaseBranch(),
        assigneeUserId:Number(state.currentUser?.id)||0,
        clarification:state.reqClarifyStartedAt?{startedAt:state.reqClarifyStartedAt}:undefined
      };
    }else{
      const rating=r.assessment||{};
      const summary=usableClarifySummary(r.summary);
      payload={
        projectId:project?.id,
        title:r.suggestedTitle||chatHistory[0]?.content?.slice(0,80)||'新工作项',
        description:summary||chatHistory.filter(h=>h.role==='user').map(h=>h.content).join('\n\n'),
        priority:r.suggestedPriority||'medium',
        status:'backlog',
        kind:'requirement',
        workflowId,
        playbookId,
        repositoryId:selectedReqRepositoryId(),
        repositoryIds:selectedReqRepositoryIds(),
        baseBranch:selectedReqBaseBranch(),
        assigneeUserId:Number(state.currentUser?.id)||0,
        clarification:{summary:summary||'',dimensions:rating.dimensions,missing:rating.missing||[],questions:r.questions||[],status:accepted?'ready':'in_progress',passed:accepted,gated:accepted,startedAt:state.reqClarifyStartedAt||undefined}
      };
    }
    let issue=await api('/api/issues',{method:'POST',body:JSON.stringify(payload)});
    rememberIssue(issue);
    if(target==='tapd'){
      try{
        issue=await pushExistingIssueToTAPD(issue);
      }catch(err){
        clearRequirementComposer();
        $('#requirementModal').classList.remove('open');
        renderIssues();
        toast('已在平台建单，TAPD 同步失败：'+err.message,'err');
        openIssueDetail(issue.id);
        return;
      }
    }
    clearRequirementComposer();
    $('#requirementModal').classList.remove('open');
    renderIssues();
    if(direct){
      toast(target==='tapd'?`已创建需求并同步 TAPD ${issue.externalRef?.externalId||''}`:'已在平台创建需求。已跳过分析，不会立刻启动 SOP','ok');
      openIssueDetail(issue.id);
      return;
    }
    if(!accepted||requirementNeedsClarification(issue)){
      state.autoStartClarify=!issue.clarification?.questions?.length;
      toast(target==='tapd'?'已在 TAPD 创建需求。分析尚未完成，请在澄清页继续，完成后再触发 SOP':'已在平台创建需求。分析尚未完成，请在澄清页继续，完成后再触发 SOP','ok');
      openIssueDetail(issue.id,'clarify');
      return;
    }
    const task=await api(`/api/issues/${issue.id}/dispatch`,{method:'POST',body:JSON.stringify({workflowId})});
    issue.latestTaskId=task.id;issue.status='in_progress';
    rememberIssue(issue);
    state.data.tasks.unshift(task);
    toast(target==='tapd'?`已在 TAPD 创建需求并进入研发 SOP`:'已在平台创建需求并进入研发 SOP','ok');
    if(task.runId)openRunFromId(task.runId);
  }catch(err){toast(err.message,'err')}
  finally{state.chatSyncTarget=null;setChatCreateEnabled(true)}
}
function setChatCreateEnabled(on){
  const box=$('#reqDestinations');
  if(box)box.hidden=!on;
  const tapdReady=projectTAPDReady();
  const hint=$('#reqDestHint');
  if(hint){
    if(reqCreateDirect()) hint.textContent=tapdReady?'按当前描述建单并绑定上方 SOP，不会立刻启动。请选择平台或 TAPD。':'按当前描述建单并绑定上方 SOP，不会立刻启动。';
    else if(composerCreateReady()) hint.textContent=tapdReady?'选择平台或 TAPD，建单后按上方所选 SOP 启动。':'建单后按上方所选 SOP 启动。';
    else hint.textContent=tapdReady?'分析还没完成。现在建单会保存当前结论，不会立刻启动 SOP，之后可在详情里继续澄清。':'分析还没完成。现在建单会保存当前结论，不会立刻启动 SOP。';
  }
  const localBtn=$('#chatCreateLocal');
  if(localBtn){
    localBtn.disabled=!on;
    localBtn.classList.remove('busy');
    localBtn.innerHTML=destLabelHTML('local');
  }
  const tapdBtn=$('#chatCreateTAPD');
  if(tapdBtn){
    tapdBtn.hidden=!tapdReady;
    tapdBtn.classList.remove('busy');
    if(!tapdReady){
      tapdBtn.disabled=true;
      tapdBtn.innerHTML='<b>TAPD 需求</b><span>未配置</span>';
    }else{
      tapdBtn.disabled=!on;
      tapdBtn.innerHTML=destLabelHTML('tapd');
    }
  }
  syncComposerCreateButton();
  persistRequirementComposer();
}
function syncComposerCreateButton(){
  const direct=$('#chatCreateDirect');
  if(!direct)return;
  if(!state.chatClarifyResult){
    direct.hidden=reqCreateDirect();
    direct.textContent='直接创建';
    direct.title='跳过分析：不读代码、不评分，也不会自动启动 SOP';
    return;
  }
  if(composerCreateReady()){
    direct.hidden=true;
    return;
  }
  direct.hidden=false;
  direct.textContent='按当前结论建单';
  direct.title=projectTAPDReady()?'分析师还没标达标。确认后才按当前结论建到平台或 TAPD，并启动 SOP':'分析师还没标达标。确认后才按当前结论建单，并启动 SOP';
}
