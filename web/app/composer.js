let chatHistory=[];
let chatClarifySessionId='';
let chatClarifyJobId='';
let chatClarifyStopping=false;
let chatAbortController=null;
let reqAdvanceTimer=null;
function reqIcon(name,size=13){
  return `<svg class="agent-icon harness-icon" data-icon="${name}" width="${size}" height="${size}" aria-hidden="true"></svg>`;
}
function hydrateReqIcons(root){
  if(root)window.AgentFlowHarnessIcons?.hydrate?.(root);
}
function reqDescription(){
  return ($('#chatInput')?.value||'').trim();
}
function reqCreateDirect(){
  return !!state.reqClarifySkipped||!state.chatClarifyResult;
}
function reqCreateAccepted(){
  return !state.reqClarifySkipped&&!!state.chatClarifyResult?.ready;
}
function composerClarifyQuestions(aiResult){
  return normalizeClarifyParsed(aiResult).questions;
}
function reqAnswerOf(index){
  const answer=(state.reqAnswers||{})[index];
  return {selected:Array.isArray(answer?.selected)?answer.selected:[],other:String(answer?.other||'')};
}
function reqQuestionAnswered(index){
  const answer=reqAnswerOf(index);
  return !!(answer.selected.length||answer.other.trim());
}
function reqAnsweredCount(questions){
  return questions.filter((_,i)=>reqQuestionAnswered(i)).length;
}
// composerProgressHTML 只给出「还有 N 个问题 / 已问清」。
function composerProgressHTML(aiResult){
  if(!aiResult)return '';
  if(aiResult.ready)return '<span class="hui-badge" data-tone="success"><span class="hui-lamp is-still"><i></i></span>已问清，可以创建</span>';
  const questions=composerClarifyQuestions(aiResult);
  const remaining=questions.length-reqAnsweredCount(questions);
  const label=!questions.length?'还需要补充说明':(remaining?`还有 ${remaining} 个问题`:'已答完，提交后继续分析');
  return `<span class="hui-badge" data-tone="warn"><span class="hui-lamp is-still"><i></i></span>${label}</span>`;
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
function selectedReqWorkflowId(){
  return $('#reqWorkflow')?.value.trim()||defaultSOPForIssue({projectId:state.projectFilter,kind:'requirement'});
}
function selectedReqPlaybookId(){
  const wf=$('#reqWorkflow')?.value.trim()||'';
  const books=projectPlaybooks(currentProject());
  const hit=books.find(b=>(b.bindings||[]).some(x=>x.workflowId===wf));
  return hit?.id||'';
}
// 改仓库不清空澄清结论：确认页用 reqClarifyRepoSnapshot 比较后提示「重新澄清」。
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
  const empty=!repos.length?'<span class="req-help">未绑定仓库</span>':(!selected.length?'<span class="req-help">未选择仓库，会按描述澄清</span>':'');
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
  if(state.reqStep===3)renderReqRepoChangedAlert();
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
function reqCurrentRepoSnapshot(){
  return {ids:[...selectedReqRepositoryIds()].sort(),baseBranch:selectedReqBaseBranch()};
}
function reqRepoSnapshotChanged(){
  const snap=state.reqClarifyRepoSnapshot;
  if(!snap||!state.chatClarifyResult||state.reqClarifySkipped)return false;
  const saved={ids:[...(snap.ids||[])].sort(),baseBranch:snap.baseBranch||''};
  return JSON.stringify(reqCurrentRepoSnapshot())!==JSON.stringify(saved);
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
  label.innerHTML=`分析仓库 <b>${escapeHTML(reqRepoNames(selectedReqRepositoryIds()))}</b>${branch?` · ${escapeHTML(branch)}`:''}`;
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
}
function collectChatReply(){
  const parts=[];
  const result=state.chatClarifyResult;
  if(result&&!result.ready){
    composerClarifyQuestions(result).forEach((q,i)=>{
      const options=clarifyQuestionOptions(q),answer=reqAnswerOf(i);
      const text=[...answer.selected.slice().sort((a,b)=>a-b).map(n=>options[n]).filter(Boolean),answer.other.trim()].filter(Boolean).join('、');
      if(text)parts.push(`${q.question}：${text}`);
    });
  }
  const extra=($('#reqSupplement')?.value||'').trim();
  if(extra)parts.push(extra);
  return parts.join('\n');
}
function usableClarifySummary(text){
  const t=String(text||'').trim();
  if(!t) return '';
  if(/没有返回摘要|no summary/i.test(t)) return '';
  return t;
}
function reqSupplementHTML(label,placeholder,action){
  return `<div class="req-supplement"><div class="req-supplement-label">${label}</div><div class="req-chat-composer is-compact"><textarea id="reqSupplement" aria-label="${label}" rows="2" placeholder="${placeholder}"></textarea><div class="req-composer-toolbar"><span class="req-toolbar-sp"></span><button type="button" id="reqResubmit" class="button small">${action}</button></div></div></div>`;
}
function reqQuestionCardHTML(questions){
  const n=questions.length;
  const idx=Math.min(Math.max(0,Number(state.reqQuestionIndex)||0),n-1);
  state.reqQuestionIndex=idx;
  const q=questions[idx],options=clarifyQuestionOptions(q),answer=reqAnswerOf(idx),multi=!!q.multiSelect;
  const recommended=Array.isArray(q.recommendedOptions)?q.recommendedOptions:[];
  const busy=requirementComposerBusy()?' disabled':'';
  const dots=questions.map((_,i)=>`<span class="${i===idx?'is-current':(reqQuestionAnswered(i)?'is-answered':'')}"></span>`).join('');
  const kind=!options.length?'请填写回答':(multi?'可多选':'单选 · 选中后自动进入下一题');
  const optionHTML=options.map((label,i)=>{
    const on=answer.selected.includes(i);
    return `<button type="button" class="req-q-option${on?' is-on':''}" role="${multi?'checkbox':'radio'}" aria-checked="${on}" data-req-option="${i}"${busy}><span class="req-q-ind${multi?' is-multi':''}" aria-hidden="true">${on?reqIcon('check',11):''}</span><span class="req-q-label">${escapeHTML(label)}</span>${recommended.includes(label)?'<span class="req-rec">推荐</span>':''}</button>`;
  }).join('');
  const next=idx===n-1
    ?`<button type="button" id="reqSubmitAnswers" class="button small primary"${busy}>提交回答</button>`
    :`<button type="button" id="reqNextQuestion" class="button small">${reqQuestionAnswered(idx)?'下一题':'跳过此题'}${reqIcon('chevron-right')}</button>`;
  return `<div class="req-q-card">`+
    `<div class="req-q-progress"><div class="req-q-dots" aria-hidden="true">${dots}</div><span>第 ${idx+1} / ${n} 题</span></div>`+
    `<div class="req-q-head">${reqIcon('chat',16)}<span id="reqQuestionText">${escapeHTML(q.question)}</span><span class="req-q-kind">${kind}</span></div>`+
    (options.length?`<div class="req-q-options" role="${multi?'group':'radiogroup'}" aria-labelledby="reqQuestionText">${optionHTML}</div>`:'')+
    `<input type="text" id="reqQuestionOther" class="input req-q-other" aria-labelledby="reqQuestionText" value="${escapeHTML(answer.other)}" placeholder="${options.length?'都不是？写下你的想法':'请填写回答'}"${busy}>`+
    `<div class="req-q-foot"><button type="button" id="reqPrevQuestion" class="req-prev"${idx===0?' disabled':''}>${reqIcon('chevron-left')}上一题</button>${next}</div>`+
    (state.reqWarnNone?`<div class="req-hint-inline">${reqIcon('info')}至少回答一题，或点「直接去创建」</div>`:'')+
  `</div>`;
}
function renderClarifyPanel(aiResult, err){
  const panel=$('#reqClarifyPanel');
  if(!panel)return;
  clearTimeout(reqAdvanceTimer);
  if(err){
    state.reqClarifyError=err;
    panel.hidden=false;
    panel.innerHTML=`<div class="req-clarify-error"><p class="req-error">${escapeHTML(err)}</p><button type="button" class="button small" id="chatClarifyRetry">${reqIcon('refresh')}重新澄清</button></div>`;
    hydrateReqIcons(panel);
    bindReqClarifyPanel();
    renderReqFooter();
    return;
  }
  state.reqClarifyError='';
  if(!aiResult){
    panel.hidden=true;
    panel.innerHTML='';
    renderReqFooter();
    return;
  }
  const summary=usableClarifySummary(aiResult.summary);
  const questions=composerClarifyQuestions(aiResult);
  const recommend=!aiResult.ready&&questions.some(q=>(q.recommendedOptions||[]).length)?`<button type="button" id="reqUseRecommended" class="button small hui-btn-soft">${reqIcon('sparkles')}全部用推荐答案</button>`:'';
  let html=clarifyRepoNoticeHTML(aiResult.repository);
  html+=`<div class="req-cl-top">${composerProgressHTML(aiResult)}<span class="req-toolbar-sp"></span>${recommend}</div>`;
  if(aiResult.ready){
    html+=`<div class="req-ready-card"><div class="req-ready-head">${reqIcon('check',16)}需求结论</div><div class="req-ready-body">${escapeHTML(summary||'信息已经够用，可以创建。')}</div></div>`;
    html+=reqSupplementHTML('还有要补充的？写在这里会再分析一次','例如：徽章需要支持点击查看获得方式','补充并重新分析');
  }else{
    if(summary)html+=`<p class="req-understand"><b>分析师的理解</b>${escapeHTML(summary)}</p>`;
    if(!normalizeClarifyParsed(aiResult).questions.length){
      html+=`<div class="req-clarify-note"><span>分析师没有给出具体问题，可以补充说明，也可以重新生成问题。</span><button type="button" class="button small" id="chatClarifyRetry">${reqIcon('refresh')}重新生成问题</button></div>`;
    }
    html+=questions.length?reqQuestionCardHTML(questions):reqSupplementHTML('补充说明','补充背景、范围或验收标准','提交补充');
  }
  panel.hidden=false;
  panel.innerHTML=html;
  hydrateReqIcons(panel);
  bindReqClarifyPanel();
  renderReqFooter();
}
function bindReqClarifyPanel(){
  const panel=$('#reqClarifyPanel');
  if(!panel?.querySelector)return;
  const on=(selector,fn)=>{const el=panel.querySelector(selector);if(el)el.onclick=fn};
  on('#chatClarifyRetry',()=>sendChatClarify(true));
  on('#reqPrevQuestion',()=>moveReqQuestion(-1));
  on('#reqNextQuestion',()=>moveReqQuestion(1));
  on('#reqSubmitAnswers',()=>submitReqAnswers());
  on('#reqResubmit',()=>submitReqSupplement());
  on('#reqUseRecommended',()=>useRecommendedAnswers());
  panel.querySelectorAll('[data-req-option]').forEach(btn=>btn.onclick=()=>pickReqOption(Number(btn.dataset.reqOption)));
  const other=panel.querySelector('#reqQuestionOther');
  if(other)other.oninput=()=>setReqOtherAnswer(other.value);
  const supplement=panel.querySelector('#reqSupplement');
  if(supplement)supplement.oninput=()=>persistRequirementComposer();
}
function pickReqOption(option){
  if(requirementComposerBusy())return;
  const questions=composerClarifyQuestions(state.chatClarifyResult);
  const idx=Number(state.reqQuestionIndex)||0,q=questions[idx];
  if(!q)return;
  const current=reqAnswerOf(idx);
  let selected=current.selected.slice();
  if(q.multiSelect)selected=selected.includes(option)?selected.filter(x=>x!==option):[...selected,option];
  else selected=selected.includes(option)?[]:[option];
  state.reqAnswers={...(state.reqAnswers||{}),[idx]:{selected,other:selected.length?'':current.other}};
  state.reqWarnNone=false;
  renderClarifyPanel(state.chatClarifyResult);
  persistRequirementComposer();
  clearTimeout(reqAdvanceTimer);
  if(q.multiSelect||!selected.length)return;
  reqAdvanceTimer=setTimeout(()=>{
    if(state.reqQuestionIndex!==idx||requirementComposerBusy())return;
    if(idx<questions.length-1)moveReqQuestion(1);
    else submitReqAnswers();
  },260);
}
function setReqOtherAnswer(text){
  const idx=Number(state.reqQuestionIndex)||0,typed=String(text||'');
  const selected=typed.trim()?[]:reqAnswerOf(idx).selected;
  state.reqAnswers={...(state.reqAnswers||{}),[idx]:{selected,other:typed}};
  state.reqWarnNone=false;
  const panel=$('#reqClarifyPanel');
  if(typed.trim())panel?.querySelectorAll?.('[data-req-option]').forEach(b=>{
    b.classList.remove('is-on');
    b.setAttribute('aria-checked','false');
    const ind=b.querySelector('.req-q-ind');if(ind)ind.innerHTML='';
  });
  const next=panel?.querySelector?.('#reqNextQuestion');
  if(next?.firstChild)next.firstChild.textContent=reqQuestionAnswered(idx)?'下一题':'跳过此题';
  persistRequirementComposer();
}
function moveReqQuestion(delta){
  const n=composerClarifyQuestions(state.chatClarifyResult).length;
  clearTimeout(reqAdvanceTimer);
  state.reqQuestionIndex=Math.min(Math.max(0,(Number(state.reqQuestionIndex)||0)+delta),Math.max(0,n-1));
  renderClarifyPanel(state.chatClarifyResult);
  persistRequirementComposer();
}
function submitReqAnswers(){
  if(requirementComposerBusy())return;
  if(!collectChatReply()){
    state.reqWarnNone=true;
    renderClarifyPanel(state.chatClarifyResult);
    return;
  }
  state.reqWarnNone=false;
  return sendChatClarify(false);
}
function submitReqSupplement(){
  const box=$('#reqSupplement');
  if(!(box?.value||'').trim()){box?.focus?.();return}
  return sendChatClarify(false);
}
function useRecommendedAnswers(){
  const questions=composerClarifyQuestions(state.chatClarifyResult);
  const answers={};
  questions.forEach((q,i)=>{
    const options=clarifyQuestionOptions(q),picks=options.map((label,n)=>(q.recommendedOptions||[]).includes(label)?n:-1).filter(n=>n>=0);
    answers[i]={selected:q.multiSelect?picks:picks.slice(0,1),other:''};
  });
  state.reqAnswers=answers;
  state.reqQuestionIndex=Math.max(0,questions.length-1);
  renderClarifyPanel(state.chatClarifyResult);
  reqAdvanceTimer=setTimeout(()=>submitReqAnswers(),420);
}
function reqAnswerReceipts(){
  return (chatHistory||[]).filter(h=>h.role==='user').slice(1).map(h=>{
    const lines=String(h.content||'').split('\n').map(l=>l.trim()).filter(Boolean);
    if(lines.length>1&&/：$/.test(lines[0]))return [{q:lines[0].slice(0,-1),v:lines.slice(1).join(' ')}];
    return lines.map(line=>{
      const i=line.indexOf('：');
      return i>0?{q:line.slice(0,i),v:line.slice(i+1)}:{q:'补充说明',v:line};
    });
  }).filter(items=>items.length);
}
function renderReqAnswerHistory(){
  const host=$('#reqAnswerHistory');
  if(!host)return;
  const receipts=reqAnswerReceipts();
  host.hidden=!receipts.length;
  host.innerHTML=receipts.map(items=>`<div class="req-receipt"><div class="req-receipt-head">${reqIcon('check')}你的回答</div>${items.map(a=>`<div class="req-receipt-item"><div class="req-receipt-q">${escapeHTML(a.q)}</div><div class="req-receipt-v">${escapeHTML(a.v)}</div></div>`).join('')}</div>`).join('');
  hydrateReqIcons(host);
}
// clarifyRepoNoticeHTML 在澄清结果顶部渲染"未拉到代码"的醒目告警，
// 确保 analyzed=false 时用户明确知道结论未基于真实代码，不被误导。
function clarifyRepoNoticeHTML(repo){
  if(!repo||repo.analyzed) return '';
  const name=repo.name||repo.id||'目标仓库';
  const reason=repo.reason||'未能拉取代码仓库';
  return `<div class="req-alert req-alert-warn" role="alert">`+
    `<span class="req-alert-icon">${reqIcon('warning',16)}</span>`+
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
  const name=repo.name||repo.id||'目标仓库';
  const row=document.createElement('div');
  row.className='req-trace-row is-warn';
  row.innerHTML=`${reqIcon('warning')}<span>未拉取到代码仓库「${escapeHTML(String(name))}」，本次分析不基于真实代码</span>`;
  box.insertBefore(row,box.firstChild);
  hydrateReqIcons(row);
  toggleReqTrace(true);
}
function stripClarifyReason(reason){
  return String(reason||'').replace(/\u001b\[[0-9;]*m/g,'').replace(/\[([0-9]{1,3}(;[0-9]{1,3})*)m/g,'').replace(/\s+/g,' ').trim();
}
function toggleReqTrace(open){
  const toggle=$('#reqTraceToggle'),box=$('#reqThinkingStream');
  if(!toggle||!box)return;
  const next=open===undefined?toggle.getAttribute('aria-expanded')!=='true':!!open;
  toggle.hidden=false;
  toggle.setAttribute('aria-expanded',String(next));
  box.hidden=!next;
}
function goReqStep(step){
  const n=Math.min(3,Math.max(1,Number(step)||1));
  clearTimeout(reqAdvanceTimer);
  state.reqStep=n;
  state.reqReached=Math.max(Number(state.reqReached)||1,n);
  if(n===2)state.reqClarifySkipped=false;
  $$('#requirementModal [data-req-pane]').forEach(pane=>{pane.hidden=Number(pane.dataset.reqPane)!==n});
  const fields=$('#reqRepoFields'),slot=n===3?$('#reqConfirmRepoSlot'):$('#reqRepoPopover');
  if(fields&&slot&&fields.parentElement!==slot)slot.appendChild(fields);
  toggleReqRepoPopover(false);
  if(n===2)renderReqAnswerHistory();
  if(n===3)renderReqConfirm();
  renderReqSteps();
  renderReqFooter();
  persistRequirementComposer();
}
function renderReqSteps(){
  const step=state.reqStep||1,reached=state.reqReached||1,skipped=!!state.reqClarifySkipped;
  $$('#reqSteps [data-req-step]').forEach(btn=>{
    const n=Number(btn.dataset.reqStep),current=n===step,done=n<step&&!(n===2&&skipped);
    btn.setAttribute('aria-selected',String(current));
    btn.disabled=!current&&n>reached;
    const num=btn.querySelector('.req-step-n');
    if(num){
      num.classList.toggle('is-done',done);
      num.innerHTML=done?reqIcon('check',11):String(n);
    }
  });
  hydrateReqIcons($('#reqSteps'));
  const tag=$('#reqStepSkipTag');
  if(tag)tag.textContent=skipped&&step!==2?' · 已跳过':'';
}
function openReqStep(step){
  if(step===state.reqStep)return;
  if(step===2&&state.reqClarifySkipped&&!(chatHistory||[]).length)return startReqClarify();
  goReqStep(step);
}
function backReqStep(){
  if(state.reqStep===3&&!state.reqClarifySkipped)return goReqStep(2);
  goReqStep(1);
  setTimeout(()=>$('#chatInput')?.focus(),0);
}
function startReqClarify(){
  const desc=reqDescription();
  if(!desc){$('#chatInput')?.focus();return toast('先写一句需求')}
  const asked=(chatHistory||[]).some(h=>h.role==='user');
  goReqStep(2);
  if(requirementComposerBusy())return;
  if(!asked||desc!==state.reqLastDescription){
    state.reqLastDescription=desc;
    return sendChatClarify(false,asked?`需求描述已更新：\n${desc}`:desc);
  }
  if(!state.chatClarifyResult)return sendChatClarify(true);
}
function skipReqClarify(){
  if(!reqDescription()){$('#chatInput')?.focus();return toast('先写一句需求')}
  if(requirementComposerBusy())abortChatClarify();
  state.reqClarifySkipped=true;
  goReqStep(3);
}
function reclarifyRequirement(){
  goReqStep(2);
  return sendChatClarify(true);
}
function reqFooterNote(){
  const step=state.reqStep||1;
  if(step===1)return reqProjectRepositories().length?'澄清会读取所选仓库的代码，把需求问清楚后再建单':'当前项目还没绑定仓库，会按描述澄清';
  if(step===2){
    const r=state.chatClarifyResult;
    if(chatAbortController||!r||r.ready)return '';
    return composerClarifyQuestions(r).length?'最后一题选完会自动提交，分析师可能会继续追问':'';
  }
  return ($('#reqWorkflow')?.value||'').trim()?'':'当前项目还没有可运行的 SOP，先到流程设计里配置';
}
function renderReqFooter(){
  const step=state.reqStep||1,busy=!!chatAbortController,creating=!!state.chatSyncTarget;
  const show=(id,on)=>{const el=$('#'+id);if(el)el.hidden=!on;return el};
  show('reqBack',step>1);
  show('chatStop',step===2&&busy);
  const hasDesc=!!reqDescription();
  [show('reqSkip',step===1),show('reqNext',step===1)].forEach(btn=>{
    if(!btn)return;
    btn.disabled=!hasDesc;
    btn.title=hasDesc?'':'先写一句需求';
  });
  const go=show('reqGoCreate',step===2&&!busy);
  if(go){
    const ready=!!state.chatClarifyResult?.ready;
    go.className=ready?'button primary':'button';
    go.innerHTML=ready?`下一步：确认创建${reqIcon('chevron-right')}`:'直接去创建';
  }
  const create=show('reqCreate',step===3);
  if(create){
    const workflow=($('#reqWorkflow')?.value||'').trim(),title=($('#reqTitle')?.value||'').trim();
    create.disabled=creating||!workflow||!title;
    create.title=!workflow?'当前项目还没有可运行的 SOP，先到流程设计里配置':(!title?'先填写标题':'');
    create.innerHTML=creating?'<span class="req-spin" aria-hidden="true"></span>正在创建…':(reqCreateAccepted()?'创建并启动 SOP':'创建需求');
  }
  const note=$('#reqComposerHint');
  if(note)note.textContent=reqFooterNote();
  hydrateReqIcons($('#reqFooter'));
}
function reqDefaultTitle(){
  const r=state.reqClarifySkipped?null:state.chatClarifyResult;
  const draft=collectRequirementDraft();
  return (r&&r.suggestedTitle)||draft.title||'新工作项';
}
function reqCreateOutcome(){
  if(state.reqClarifySkipped)return {start:false,text:'跳过了澄清，创建后不会自动启动 SOP，可在详情里开始澄清。'};
  const r=state.chatClarifyResult;
  if(!r)return {start:false,text:'还没有澄清结论，创建后不会自动启动 SOP，可在详情里开始澄清。'};
  if(r.ready)return {start:true,text:'创建后按所选 SOP 自动启动，并打开运行详情。'};
  return {start:false,text:'澄清未完成，不会自动启动 SOP，可在详情里继续澄清。'};
}
function renderReqConfirm(){
  const title=$('#reqTitle');
  if(title&&(!state.reqTitleEdited||!title.value.trim()))title.value=reqDefaultTitle();
  const r=state.reqClarifySkipped?null:state.chatClarifyResult;
  const conclusion=r?usableClarifySummary(r.summary):'';
  const label=$('#reqSummaryLabel'),summary=$('#reqSummary');
  if(label)label.textContent=conclusion?'需求结论':'需求描述';
  if(summary)summary.textContent=conclusion||collectRequirementDraft().description||'（未填写）';
  renderReqTarget();
  renderReqRepoChangedAlert();
  const outcome=$('#reqOutcome');
  if(outcome){
    const o=reqCreateOutcome();
    outcome.classList.toggle('is-muted',!o.start);
    outcome.innerHTML=`${reqIcon(o.start?'play':'info')}<span>${escapeHTML(o.text)}</span>`;
    hydrateReqIcons(outcome);
  }
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
function renderReqRepoChangedAlert(){
  const box=$('#reqRepoChangedAlert');
  if(!box)return;
  const changed=reqRepoSnapshotChanged();
  box.hidden=!changed;
  if(!changed)return;
  const snap=state.reqClarifyRepoSnapshot||{};
  const text=$('#reqRepoChangedText');
  if(text)text.innerHTML=`澄清基于原仓库（<b>${escapeHTML(reqRepoNames(snap.ids||[]))}</b>${snap.baseBranch?` · 分支 ${escapeHTML(snap.baseBranch)}`:''}）`;
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
  if(state.reqStep===3)renderReqRepoChangedAlert();
  persistRequirementComposer();
}
function onReqTitleInput(){
  state.reqTitleEdited=true;
  renderReqFooter();
  persistRequirementComposer();
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
    step:state.reqStep||1,
    reached:state.reqReached||1,
    clarifySkipped:!!state.reqClarifySkipped,
    questionIndex:Number(state.reqQuestionIndex)||0,
    answers:state.reqAnswers||{},
    supplement:$('#reqSupplement')?.value||'',
    title:$('#reqTitle')?.value||'',
    titleEdited:!!state.reqTitleEdited,
    target:state.reqTarget||'local',
    repoSnapshot:state.reqClarifyRepoSnapshot||null,
    lastDescription:state.reqLastDescription||'',
    repoIds:selectedReqRepositoryIds(),
    baseBranch:selectedReqBaseBranch(),
    workflowId:selectedReqWorkflowId(),
    playbookId:selectedReqPlaybookId(),
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
    const scope=clarifyScopeForComposer();
    bindClarifyModelPicker(picker,scope);
    const api=window.AgentFlowAssistantModels,toggle=picker.querySelector('[data-model-toggle]');
    if(!api||!toggle)return;
    api.paint(toggle, clarifyEffectiveModel(clarifyEngineCatalogCached(scope),null));
    // 可用性到了才能把标签刷成真正会用的那个框架。
    if(scope)clarifyEngineCatalog(scope).then(catalog=>{if(catalog)api.paint(toggle, clarifyEffectiveModel(catalog,null))});
  }
}
function resetRequirementComposerState(){
  Object.assign(state,{
    requirementIssueId:null,chatClarifyResult:null,chatSyncTarget:null,reqClarifyStartedAt:'',
    reqStep:1,reqReached:1,reqClarifySkipped:false,reqQuestionIndex:0,reqAnswers:{},reqWarnNone:false,
    reqTarget:'local',reqTitleEdited:false,reqClarifyRepoSnapshot:null,reqLastDescription:'',reqClarifyError:'',
  });
}
function resetRequirementComposer(){
  resetRequirementComposerState();
  chatHistory=[];chatClarifySessionId='';
  if(chatAbortController)abortChatClarify();
  chatClarifyJobId='';
  chatClarifyStopping=false;
  ['#reqBaseBranch','#reqTitle','#chatInput'].forEach(id=>{const el=$(id);if(el)el.value=''});
  renderReqContext();
  renderClarifyPanel(null);
  renderReqAnswerHistory();
  clarifyThinkingReset();
  setChatSendAnalyzing(false);
  updateReqCharCount();
  goReqStep(1);
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
  state.reqClarifySkipped=!!raw.clarifySkipped;
  state.reqQuestionIndex=Number(raw.questionIndex)||0;
  state.reqAnswers=raw.answers&&typeof raw.answers==='object'?raw.answers:{};
  state.reqTarget=raw.target==='tapd'?'tapd':'local';
  state.reqTitleEdited=!!raw.titleEdited;
  state.reqClarifyRepoSnapshot=raw.repoSnapshot||null;
  state.reqLastDescription=raw.lastDescription||'';
  state.reqReached=Math.min(3,Math.max(1,Number(raw.reached)||Number(raw.step)||1));
  renderReqContext();
  if(Array.isArray(raw.repoIds))setReqRepositoryIds(raw.repoIds);
  const baseBranch=$('#reqBaseBranch');if(baseBranch)baseBranch.value=baseBranch.disabled?'':(raw.baseBranch||'');
  renderReqRepoChip();
  fillReqWorkflow(raw.workflowId||raw.playbookId);
  const input=$('#chatInput');if(input)input.value=raw.input||'';
  const title=$('#reqTitle');if(title)title.value=raw.title||'';
  const reconnect=!!(raw.interrupted&&raw.jobId);
  const interruptHint=reconnect?'正在接上分析…':'刷新后分析已中断，可以重新澄清。';
  const lastAssistant=(chatHistory||[]).filter(h=>h.role==='assistant').pop();
  if(raw.clarifyResult){
    renderClarifyPanel(raw.clarifyResult);
  }else if(lastAssistant){
    try{renderClarifyPanel(JSON.parse(lastAssistant.content))}catch{renderClarifyPanel(null, interruptHint)}
  }else if((chatHistory||[]).some(h=>h.role==='user')||reconnect){
    renderClarifyPanel(null, interruptHint);
  }
  const supplement=$('#reqSupplement');if(supplement&&raw.supplement)supplement.value=raw.supplement;
  renderReqAnswerHistory();
  updateReqCharCount();
  goReqStep(raw.step);
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
    const raw=Array.isArray(q?.options)?q.options:[];
    const recommended=raw.filter(o=>o&&typeof o==='object'&&o.recommended).map(clarifyOptionLabel).filter(Boolean);
    return {...q,question:String(q?.question||q?.prompt||''),options:clarifyQuestionOptions(q||{}),recommendedOptions:recommended.length?recommended:(q?.recommendedOptions||[])};
  }).filter(q=>q.question||(q.options||[]).length);
  return {...src, questions};
}
function clarifyThinkingReset(){
  const box=$('#reqThinkingStream');
  if(box){box.hidden=true;box.innerHTML=''}
  state.reqToolCount=0;
  const toggle=$('#reqTraceToggle');
  if(toggle){toggle.hidden=true;toggle.setAttribute('aria-expanded','false')}
  const label=$('#reqTraceLabel');if(label)label.textContent='已用 0 个工具';
}
function clarifyToolLabel(tool){
  const name=String(tool||'').replace(/^mcp__agentflow_session__/,'');
  return ({Read:'读取',Grep:'搜索',Glob:'查找',MCP:'MCP',submit_clarification:'提交结论',submit_assistant_reply:'提交回复'})[name]||name||'操作';
}
function clarifyThinkingPush(ev){
  if(ev.kind!=='tool'&&ev.kind!=='status'&&clarifyThoughtShouldHide(ev&&ev.text))return;
  if(ev.kind==='tool'){
    const box=$('#reqThinkingStream');
    if(box){
      const row=document.createElement('div');
      row.className='req-trace-row';
      row.innerHTML=`${reqIcon(/Grep|Glob/.test(String(ev.tool||''))?'search':'doc')}<span>${escapeHTML(clarifyToolLabel(ev.tool))}</span>${ev.detail?`<code>${escapeHTML(ev.detail)}</code>`:''}`;
      box.appendChild(row);
      hydrateReqIcons(row);
    }
    state.reqToolCount=(state.reqToolCount||0)+1;
    const toggle=$('#reqTraceToggle');if(toggle)toggle.hidden=false;
    const label=$('#reqTraceLabel');if(label)label.textContent=`已用 ${state.reqToolCount} 个工具`;
    return;
  }
  const label=$('#reqThinkingLabel');
  if(label)label.textContent=ev.text||'正在分析…';
}
// sendChatClarify 发起一轮澄清。retry 时重发上一条用户消息；explicit 用于第 1 步的需求描述。
async function sendChatClarify(retry, explicit){
  if(requirementComposerBusy())return;
  const last=(chatHistory||[]).filter(h=>h.role==='user').pop();
  let msg='',pushed=false;
  if(retry&&last){
    msg=last.content;
  }else{
    msg=String(explicit??(last?collectChatReply():reqDescription())).trim();
    if(!msg)return;
    chatHistory.push({role:'user',content:msg});
    pushed=true;
  }
  state.reqWarnNone=false;
  state.reqClarifyRepoSnapshot=reqCurrentRepoSnapshot();
  chatClarifyStopping=false;
  chatClarifyJobId=newComposerJobId();
  chatAbortController=new AbortController();
  clarifyThinkingReset();
  renderReqAnswerHistory();
  setChatSendAnalyzing(true);
  persistRequirementComposer();
  try{
    const project=currentProject();
    const clarifyModel=await clarifyModelForSend(clarifyScopeForComposer(),null);
    const resp=await fetch('/api/chat-clarify',{method:'POST',headers:{'Content-Type':'application/json','Accept':'text/event-stream'},body:JSON.stringify({
      projectId:project.id,
      repositoryId:selectedReqRepositoryId(),
      repositoryIds:selectedReqRepositoryIds(),
      baseBranch:selectedReqBaseBranch(),
      title:chatHistory[0]?.content?.slice(0,80)||msg.slice(0,80),
      description:chatHistory.filter(h=>h.role==='user').map(h=>h.content).join('\n\n'),
      message:msg,
      model:clarifyModel,
      sessionId:chatClarifySessionId,
      jobId:chatClarifyJobId,
      history:chatHistory
    }),signal:chatAbortController.signal});
    if(!resp.ok){
      let em=`HTTP ${resp.status}`;try{const d=await resp.json();em=d.error||em;}catch{}
      throw new Error(em);
    }
    const data=await readChatClarifyResponse(resp);
    applyChatClarifyOutcome(data);
    chatClarifyJobId='';
  }catch(err){
    if(err.name==='AbortError'){
      if(chatClarifyStopping){
        if(pushed&&chatHistory.length&&chatHistory[chatHistory.length-1].role==='user')chatHistory.pop();
        renderReqAnswerHistory();
        renderClarifyPanel(state.chatClarifyResult,state.chatClarifyResult?'':'已停止，可以重新澄清，也可以直接去创建。');
      }
    }else{
      chatClarifyJobId='';
      renderClarifyPanel(null, `分析失败：${err.message}`);
    }
  }finally{
    finishComposerClarifyStream();
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
  setChatSendAnalyzing(true);
  attachComposerClarifyStream(jobId);
}
async function attachComposerClarifyStream(jobId){
  try{
    const resp=await fetch('/api/chat-clarify?jobId='+encodeURIComponent(jobId),{headers:{Accept:'text/event-stream'},signal:chatAbortController.signal});
    if(resp.status===404){
      chatClarifyJobId='';
      renderClarifyPanel(state.chatClarifyResult,state.chatClarifyResult?'':'刷新后分析已结束，可以重新澄清，也可以直接去创建。');
      return;
    }
    if(!resp.ok){
      let em=`HTTP ${resp.status}`;try{const d=await resp.json();em=d.error||em;}catch{}
      throw new Error(em);
    }
    const data=await consumeClarifyStream(resp);
    applyChatClarifyOutcome(data);
    chatClarifyJobId='';
  }catch(err){
    if(err.name==='AbortError'){
      if(chatClarifyStopping)renderClarifyPanel(state.chatClarifyResult,state.chatClarifyResult?'':'已停止，可以重新澄清，也可以直接去创建。');
    }else{
      chatClarifyJobId='';
      renderClarifyPanel(null, `分析失败：${err.message}`);
    }
  }finally{
    finishComposerClarifyStream();
  }
}
async function readChatClarifyResponse(resp){
  const isStream=(resp.headers.get('Content-Type')||'').includes('text/event-stream');
  if(isStream)return consumeClarifyStream(resp);
  return resp.json();
}
function applyChatClarifyOutcome(data){
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
  state.reqAnswers={};
  state.reqQuestionIndex=0;
  state.reqWarnNone=false;
  clarifyThinkingReset();
  renderReqAnswerHistory();
  renderClarifyPanel(aiResult);
}
function finishComposerClarifyStream(){
  chatAbortController=null;
  chatClarifyStopping=false;
  clarifyThinkingReset();
  setChatSendAnalyzing(false);
  // 结果在请求仍占用时渲染，选项是禁用态；请求结束后重绘一次让它们可点。
  if(state.chatClarifyResult&&!state.reqClarifyError)renderClarifyPanel(state.chatClarifyResult);
  persistRequirementComposer();
}
// consumeClarifyStream reads a text/event-stream response, rendering "thought"
// events live and resolving with the payload of the final "done"/"error" event.
async function consumeClarifyStream(resp){
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
  clearTimeout(reqAdvanceTimer);
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
  const thinking=$('#reqThinking'),panel=$('#reqClarifyPanel'),label=$('#reqThinkingLabel');
  if(thinking)thinking.hidden=!analyzing;
  if(analyzing&&panel)panel.hidden=true;
  if(analyzing&&label)label.textContent=reqAnswerReceipts().length?'正在根据你的回答继续分析':'分析师正在读代码';
  renderReqFooter();
}
function collectRequirementDraft(){
  const typed=reqDescription();
  const history=(chatHistory||[]).filter(h=>h.role==='user').map(h=>String(h.content||'').trim()).filter(Boolean);
  const parts=state.reqClarifySkipped||!history.length?(typed?[typed]:[]):history.slice();
  const reply=state.reqClarifySkipped?'':collectChatReply();
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
// createIssueFromChat 由第 3 步「创建」一键建单：只有澄清 ready 才 dispatch SOP；
// 跳过澄清或未 ready 都只建单并打开详情。
async function createIssueFromChat(target){
  target=target||state.reqTarget||'local';
  if(target!=='local'&&target!=='tapd')return toast('请选择平台或 TAPD');
  const accepted=reqCreateAccepted();
  const direct=reqCreateDirect();
  const draft=collectRequirementDraft();
  if(direct&&!draft.description)return toast('先写一句需求');
  if(target==='tapd'&&!projectTAPDReady())return toast('当前项目未配置 TAPD');
  if(state.chatSyncTarget)return toast('正在建单，请勿重复点击');
  const workflowId=selectedReqWorkflowId();
  if(!workflowId)return toast('当前项目还没有可跑的流程，先到流程设计里配 SOP');
  state.chatSyncTarget=target;
  renderReqFooter();
  const r=direct?null:state.chatClarifyResult;
  const project=currentProject();
  const title=($('#reqTitle')?.value||'').trim();
  try{
    const playbookId=selectedReqPlaybookId();
    const base={
      projectId:project?.id,
      status:'backlog',
      kind:'requirement',
      workflowId,
      playbookId,
      repositoryId:selectedReqRepositoryId(),
      repositoryIds:selectedReqRepositoryIds(),
      baseBranch:selectedReqBaseBranch(),
      assigneeUserId:Number(state.currentUser?.id)||0,
    };
    let payload;
    if(direct){
      payload={
        ...base,
        title:title||draft.title||draft.description.slice(0,80)||'新工作项',
        description:draft.description,
        priority:'medium',
        clarification:state.reqClarifyStartedAt?{startedAt:state.reqClarifyStartedAt}:undefined
      };
    }else{
      const summary=usableClarifySummary(r.summary);
      payload={
        ...base,
        title:title||r.suggestedTitle||chatHistory[0]?.content?.slice(0,80)||'新工作项',
        description:summary||chatHistory.filter(h=>h.role==='user').map(h=>h.content).join('\n\n'),
        priority:r.suggestedPriority||'medium',
        clarification:{summary:summary||'',questions:r.questions||[],status:accepted?'ready':'in_progress',startedAt:state.reqClarifyStartedAt||undefined}
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
      toast(target==='tapd'?`已创建需求并同步 TAPD ${issue.externalRef?.externalId||''}`:'已在平台创建需求，可在详情里触发 SOP','ok');
      openIssueDetail(issue.id);
      return;
    }
    if(!accepted){
      toast(target==='tapd'?'已在 TAPD 创建需求。澄清尚未完成，可在澄清页继续，也可以直接触发 SOP':'已在平台创建需求。澄清尚未完成，可在澄清页继续，也可以直接触发 SOP','ok');
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
  finally{state.chatSyncTarget=null;renderReqFooter()}
}
