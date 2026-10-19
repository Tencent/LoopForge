// ================= Issue 详情抽屉 =================
const issueClarifyJobs=new Map();
let renderedIssueClarifyKey='';
let submittedClarifyDraft=null;
function issueClarifying(id){
  if(!!id&&issueClarifyJobs.has(id))return true;
  const issue=(state.data.issues||[]).find(i=>i.id===id)||state.issueDetailData?.issue;
  return issue?.id===id&&issue?.clarification?.status==='analyzing';
}
function closeIssueDrawer(){
  const id=state.issueDetailData?.issue?.id;
  const running=issueClarifying(id);
  state.drawerOpen=false;
  const d=$('#issueDetailDrawer');if(d){d.classList.remove('open');d.setAttribute('aria-hidden','true')}
  if(running)toast('澄清继续在后台进行');
}
async function openIssueDetail(id,tab){
  if(!id)return;
  const issueId=String(id);
  state.drawerOpen=true;state.selectedIssue=issueId;state.issueDetailData=null;
  const drawer=$('#issueDetailDrawer');
  if(drawer&&drawer.parentElement!==document.body)document.body.appendChild(drawer);
  drawer.classList.add('open');
  drawer.setAttribute('aria-hidden','false');
  $('#issueDetailBody').innerHTML='<div class="detail-empty">正在加载需求详情…</div>';
  try{
    const detail=await api(`/api/issues/${encodeURIComponent(issueId)}`);const issue=detail.issue;const comments=detail.comments||[];
    const localIssue=(state.data.issues||[]).find(i=>i.id===issueId);if(localIssue)Object.assign(issue,localIssue);
    const task=latestIssueTask(issue);
    const runId=task?.runId||latestIssueRun(issue, task)?.id;
    let requirement=null;
    try{requirement=await api(`/api/issues/${encodeURIComponent(issueId)}/requirement`,{silent:true})}catch(e){}
    let run=null;
    if(runId){
      try{run=await api(`/api/runs/${runId}`)}catch(e){}
      if(run?.id){
        if(!state.data.runs)state.data.runs=[];
        const rIdx=state.data.runs.findIndex(r=>r.id===run.id);
        if(rIdx>=0)state.data.runs[rIdx]={...state.data.runs[rIdx],...run};
        else state.data.runs=sortRunsDescending([run,...state.data.runs]);
        if(['queued','running','waiting'].includes(run.status))startLiveRun(run.id);
      }
    }
    state.issueDetailData={issue,comments,run,task,requirement};
    const health=issueRunHealth(issue),stage=issueCurrentStage(issue),needsClarify=requirementNeedsClarification(issue),needsConfirm=clarificationNeedsConfirm(issue);
    state.issueDetailTab=tab||(issueClarifying(issueId)||needsClarify||needsConfirm?'clarify':((health==='attention'||health==='active'||stage.key==='waiting')?'sop':'overview'));
    $$('#issueDetailTabs .drawer-tab').forEach(b=>b.classList.toggle('active',b.dataset.dtab===state.issueDetailTab));
    renderIssueDetailTab();
    if(issueClarifying(issueId)){
      restoreIssueClarifyThinking(issueId);
      attachIssueClarifyStream(issueId);
    }
    if(state.autoStartClarify&&(needsClarify||issueClarifying(issueId))){
      state.autoStartClarify=false;
      if(!issueClarifying(issueId))openIssueClarification(true);
      else{setIssueClarifyTab();renderIssueDetailTab();attachIssueClarifyStream(issueId)}
    }else{state.autoStartClarify=false}
  }catch(e){state.autoStartClarify=false;$('#issueDetailBody').innerHTML=`<div class="detail-empty">加载失败：${escapeHTML(e.message)}</div>`}
}
function issueClarifyViewKey(issue,project){
  const c=issue?.clarification||{};
  const policy=clarificationPolicy(project);
  return JSON.stringify({
    analyzing:!!issueClarifying(issue?.id),
    status:c.status||'',
    score:c.score||0,
    passed:!!c.passed,
    gated:!!c.gated,
    lastError:c.lastError||'',
    summary:c.summary||'',
    writeBack:c.writeBackStatus||'',
    writeBackError:c.writeBackError||'',
    questions:(c.questions||[]).map(q=>({question:q.question||'',multi:!!q.multiSelect,options:clarifyQuestionOptions(q)})),
    missing:c.missing||[],
    dimensions:c.dimensions||{},
    history:(c.history||[]).map(h=>({role:h.role||'',content:h.content||''})),
    threshold:policy?.threshold||0,
    source:issue?.source||'',
    external:issue?.externalRef?.externalUrl||'',
    needs:!!requirementNeedsClarification(issue),
    confirm:!!clarificationNeedsConfirm(issue),
    blocked:issueExecutionBlocked(issue)||''
  });
}
function clarifyQuestionKey(questions){
  return (questions||[]).map(q=>`${q.question||''}\n${clarifyQuestionOptions(q).join('\n')}`).join('\n---\n');
}
function rememberSubmittedClarifyDraft(issue,msg){
  const questions=issue?.clarification?.questions||[];
  const captured=captureIssueClarifyDraft($('#issueDetailBody'));
  const reply=String(msg||'').trim();
  submittedClarifyDraft={
    issueId:issue?.id||'',
    questionKey:clarifyQuestionKey(questions),
    questions:captured?.questions||[],
    extra:captured?.extra||'',
    reply:CLARIFY_AUTO_PROMPTS.includes(reply)?'':reply
  };
}
function clarifyAnswerDraft(issue){
  const saved=submittedClarifyDraft;
  if(!saved||saved.issueId!==issue?.id)return null;
  const status=issue?.clarification?.status||'';
  if(status==='ready'){submittedClarifyDraft=null;return null}
  if(!issueClarifying(issue.id)&&saved.questionKey!==clarifyQuestionKey(issue?.clarification?.questions)){
    submittedClarifyDraft=null;
    return null;
  }
  return saved;
}
function clarifySubmittedHTML(reply){
  const text=String(reply||'').trim();
  if(!text||CLARIFY_AUTO_PROMPTS.includes(text))return '';
  return `<div class="clarify-submitted"><b>本轮回答已提交</b><p>${escapeHTML(text)}</p></div>`;
}
function clarifyQuestionCardsHTML(questions,saved){
  return (questions||[]).map((q,i)=>{
    const picked=saved?.questions?.[i];
    const options=clarifyQuestionOptions(q).map((opt,j)=>`<button type="button" class="issue-clarify-option${picked?.selected?.[j]?' selected':''}" data-multi="${!!q.multiSelect}">${escapeHTML(opt)}</button>`).join('');
    return `<div class="issue-clarify-question"><b>${i+1}. ${escapeHTML(q.question)}</b><div class="issue-clarify-options">${options}</div><input class="issue-clarify-other" data-question="${i}" placeholder="其他：自己填写" value="${escapeHTML(picked?.other||'')}"></div>`;
  }).join('');
}
function captureIssueClarifyDraft(host){
  if(!host?.querySelectorAll)return null;
  const active=document.activeElement;
  const others=[...host.querySelectorAll('.issue-clarify-other')];
  const questions=[...host.querySelectorAll('.issue-clarify-question')].map(row=>({
    text:row.querySelector('b')?.textContent||'',
    selected:[...row.querySelectorAll('.issue-clarify-option')].map(b=>b.classList.contains('selected')),
    other:row.querySelector('.issue-clarify-other')?.value||''
  }));
  const extra=host.querySelector('#issueClarifyInput');
  if(!questions.length&&!extra)return null;
  let focus=null;
  if(active&&host.contains?.(active)){
    const sel=typeof active.selectionStart==='number'?{start:active.selectionStart,end:active.selectionEnd}:null;
    if(active.id==='issueClarifyInput')focus={kind:'extra',sel};
    else if(active.classList?.contains('issue-clarify-other'))focus={kind:'other',index:others.indexOf(active),sel};
  }
  return {questions,extra:extra?.value||'',focus,scroll:host.scrollTop||0};
}
function restoreIssueClarifySelection(el,sel){
  if(!el||!sel||typeof el.setSelectionRange!=='function')return;
  try{el.setSelectionRange(sel.start,sel.end)}catch{}
}
function restoreIssueClarifyDraft(host,draft){
  if(!host||!draft)return;
  const rows=[...host.querySelectorAll('.issue-clarify-question')];
  const same=rows.length===draft.questions.length&&rows.every((row,i)=>(row.querySelector('b')?.textContent||'')===draft.questions[i].text);
  if(!same)return;
  rows.forEach((row,i)=>{
    const saved=draft.questions[i];
    [...row.querySelectorAll('.issue-clarify-option')].forEach((b,j)=>b.classList.toggle('selected',!!saved.selected[j]));
    const other=row.querySelector('.issue-clarify-other');
    if(other)other.value=saved.other||'';
  });
  const extra=host.querySelector('#issueClarifyInput');
  if(extra)extra.value=draft.extra||'';
  if(host.scrollTop!=null)host.scrollTop=draft.scroll||0;
  if(draft.focus?.kind==='extra'&&extra){
    extra.focus?.();
    restoreIssueClarifySelection(extra,draft.focus.sel);
  }else if(draft.focus?.kind==='other'){
    const input=host.querySelectorAll('.issue-clarify-other')[draft.focus.index];
    if(input){input.focus?.();restoreIssueClarifySelection(input,draft.focus.sel)}
  }
}
function renderIssueDetailTab(){
  const data=state.issueDetailData;const host=$('#issueDetailBody');if(!data||!host)return;
  const issue=data.issue;if(!issue){host.innerHTML='<div class="detail-empty">未找到该需求</div>';renderedIssueClarifyKey='';return}
  const project=(state.data.projects||[]).find(p=>p.id===issue.projectId);
  $('#issueDetailKey').textContent=issueKeyLabel(issue);
  $('#issueDetailTitle').textContent=issue.title||'';
  $('#issueDetailSubtitle').textContent=`${project?.name||'—'} · ${issuePriorityLabel(issue.priority)} · ${issueStatusText(issue)} · 更新于 ${new Date(issue.updatedAt).toLocaleString()}`;
  // 答案只存在输入框里。问题没变时跳过重绘，避免轮询和运行推送把正在填的内容清掉。
  const clarifyKey=state.issueDetailTab==='clarify'?issueClarifyViewKey(issue,project):'';
  if(clarifyKey&&clarifyKey===renderedIssueClarifyKey&&host.querySelector?.('.clarify-workspace'))return;
  const clarifyDraft=clarifyKey?captureIssueClarifyDraft(host):null;
  const panes={overview:overviewPane(issue,data),operations:operationsPane(issue),clarify:clarifyPane(issue,project),sop:sopPane(issue,data),artifacts:artifactsPane(data),delivery:deliveryPane(issue,data),acceptance:acceptancePane(issue,data),activity:activityPane(issue,data)};
  host.innerHTML=panes[state.issueDetailTab]||'<div class="detail-empty">暂无内容</div>';
  renderedIssueClarifyKey=clarifyKey;
  host.querySelectorAll('[data-edit-issue]').forEach(b=>b.onclick=()=>{closeIssueDrawer();openIssue(issue)});
  host.querySelectorAll('[data-dispatch-issue]').forEach(b=>b.onclick=()=>triggerIssue(b.dataset.dispatchIssue,b.dataset.workflow));
  host.querySelectorAll('[data-retrigger-issue]').forEach(b=>b.onclick=()=>retriggerIssue(b.dataset.retriggerIssue,b.dataset.workflow));
  host.querySelectorAll('[data-rerun-from-here]').forEach(b=>b.onclick=()=>openRerunFromNode((b.dataset.run&&(state.data.runs||[]).find(r=>r.id===b.dataset.run))||data.run,b.dataset.rerunFromHere));
  host.querySelectorAll('[data-toggle-sop-row]').forEach(el=>el.onclick=e=>{
    if(e.target.closest('button,a,select,input,.user-combobox'))return;
    const id=el.dataset.toggleSopRow;
    state.issueSOPOpen=state.issueSOPOpen===id?'':id;
    renderIssueDetailTab();
  });
  bindUserSearches(host);
  host.querySelectorAll('[data-sop-assignee] input[type="hidden"]').forEach(input=>input.onchange=()=>setIssueSOPOwner(issue.id, input.closest('[data-sop-assignee]').dataset.sopAssignee, Number(input.value)||0));
  host.querySelectorAll('[data-push-tapd]').forEach(b=>b.onclick=()=>pushIssueToTAPDFromDetail(b.dataset.pushTapd));
  host.querySelectorAll('[data-resync-tapd]').forEach(b=>b.onclick=()=>resyncTAPDIssueById(b.dataset.resyncTapd));
  host.querySelectorAll('[data-reopen-issue]').forEach(b=>b.onclick=()=>reopenDeliveredIssue(b.dataset.reopenIssue));
  host.querySelectorAll('[data-delete-issue]').forEach(b=>b.onclick=()=>deleteIssueById(b.dataset.deleteIssue||issue.id));
  host.querySelectorAll('[data-close-issue]').forEach(b=>b.onclick=()=>closeIssueById(b.dataset.closeIssue||issue.id));
  host.querySelectorAll('[data-resume-run]').forEach(b=>b.onclick=()=>resumeRun(b.dataset.resumeRun,b));
  host.querySelectorAll('[data-pause-run]').forEach(b=>b.onclick=()=>pauseRun(b.dataset.pauseRun));
  host.querySelectorAll('[data-open-run]').forEach(b=>b.onclick=e=>{e.stopPropagation();openRunFromId(b.dataset.openRun,{forceRuns:true})});
  host.querySelectorAll('[data-open-logs]').forEach(b=>b.onclick=()=>openRunFromId(b.dataset.openLogs,{forceRuns:true}));
  host.querySelectorAll('[data-rerun-from-here]').forEach(b=>b.onclick=()=>openRerunFromNode(cachedRun(b.dataset.run),b.dataset.rerunFromHere));
  host.querySelectorAll('[data-export-logs]').forEach(b=>b.onclick=()=>downloadRunLogs(b.dataset.exportLogs));
  host.querySelectorAll('[data-artifact]').forEach(b=>b.onclick=()=>previewArtifact(data.run,b.dataset.artifact));
  host.querySelectorAll('[data-start-clarify]').forEach(b=>b.onclick=()=>openIssueClarification(true));
  host.querySelectorAll('[data-continue-clarify]').forEach(b=>b.onclick=()=>{
    const current=state.issueDetailData?.issue;
    if(current?.clarification?.lastError){retryIssueClarifyTurn();return;}
    sendIssueClarification();
  });
  host.querySelectorAll('[data-stop-clarify]').forEach(b=>b.onclick=()=>stopIssueClarification(issue.id));
  host.querySelectorAll('[data-retry-clarify]').forEach(b=>b.onclick=()=>retryIssueClarificationWriteBack());
  host.querySelectorAll('[data-retry-clarify-turn]').forEach(b=>b.onclick=()=>retryIssueClarifyTurn());
  host.querySelectorAll('[data-reclarify]').forEach(b=>b.onclick=()=>openIssueClarification(false,true));
  host.querySelectorAll('.issue-clarify-option').forEach(b=>b.onclick=()=>{
    if(b.dataset.multi==='true'){
      b.classList.toggle('selected');
    }else{
      const wasSelected=b.classList.contains('selected');
      b.parentElement.querySelectorAll('.issue-clarify-option').forEach(x=>x.classList.remove('selected'));
      if(!wasSelected)b.classList.add('selected');
    }
  });
  host.querySelectorAll('[data-clarify-model]').forEach(bindClarifyModelPicker);
  bindWaitingActions(host);
  if(state.issueDetailTab==='clarify'&&issueClarifying(issue.id))restoreIssueClarifyThinking(issue.id);
  if(clarifyDraft)restoreIssueClarifyDraft(host,clarifyDraft);
}
function drawerFact(label,value,title){return `<div class="detail-fact"><span>${label}</span><b title="${escapeHTML(title||value||'')}">${value===''||value==null?'—':value}</b></div>`}
function clarificationOverviewCard(issue){
  const c=issue.clarification||{},source=issue.source==='gongfeng'?'工蜂':issue.source==='tapd'?'TAPD':'AgentFlow';
  const score=c.score||0;
  if(issueClarifying(issue.id)||c.status==='analyzing'){
    return `<div class="clarify-overview"><span>…</span><div><b>澄清正在后台分析</b><small>关闭网页也不会中断，点查看可回到分析过程</small></div><button class="button ghost small" data-start-clarify>查看</button></div>`;
  }
  if(requirementNeedsClarification(issue)&&c.status!=='in_progress'&&c.status!=='ready'){
    return `<div class="clarify-overview needs-user"><span>!</span><div><b>导入评分 ${score} 分，需要你处理</b><small>未达项目准入线，点这里开始澄清后再触发 SOP</small></div><button class="button primary small" data-start-clarify>去澄清</button></div>`;
  }
  if(c.status==='ready'&&c.summary){
    const failed=c.writeBackStatus==='failed';
    const pending=c.writeBackStatus==='pending';
    const scoreLabel=score?` · ${score} 分`:'';
    const cls=failed||pending?'failed':'ready';
    const title=failed?`澄清已完成${scoreLabel}，写回失败`:pending?`澄清已完成${scoreLabel}，待确认写回`:`已澄清${scoreLabel} · ${c.writeBackStatus==='succeeded'?`已写回 ${source}`:'结论已保存'}`;
    return `<div class="clarify-overview ${cls}"><span>${failed||pending?'!':'✓'}</span><div><b>${title}</b><small>${escapeHTML((c.summary||'').slice(0,120))}${c.summary&&c.summary.length>120?'…':''}</small></div><button class="button ghost small" data-start-clarify>查看</button></div>`;
  }
  if(c.status==='in_progress'){
    return `<div class="clarify-overview"><span>…</span><div><b>澄清尚未完成</b><small>继续回答关键问题，进度已经保存</small></div><button class="button ghost small" data-start-clarify>继续</button></div>`;
  }
  return `<div class="clarify-overview"><span>?</span><div><b>描述不够清楚？</b><small>结合原单和代码问清关键细节</small></div><button class="button ghost small" data-start-clarify>帮我问清楚</button></div>`;
}
function setIssueClarifyTab(){
  state.issueDetailTab='clarify';
  $$('#issueDetailTabs .drawer-tab').forEach(b=>b.classList.toggle('active',b.dataset.dtab==='clarify'));
}
const CLARIFY_AUTO_PROMPTS=['请先检查这张工作项的描述和相关代码，告诉我还缺哪些关键信息。','请基于已有澄清结论继续检查，还有哪些重要信息需要补充？'];
function clarifyKickoffPending(issue){
  const c=issue?.clarification||{};
  if(c.status==='ready'||c.status==='in_progress')return false;
  return !(c.questions||[]).length;
}
function openIssueClarification(autoStart=false,restart=false){
  const issue=state.issueDetailData?.issue;if(!issue)return;
  setIssueClarifyTab();
  if(issueClarifying(issue.id)&&!restart){
    renderIssueDetailTab();
    restoreIssueClarifyThinking(issue.id);
    attachIssueClarifyStream(issue.id);
    return;
  }
  issue.clarification=issue.clarification||{};
  if(restart||!issue.clarification.status||issue.clarification.status==='needs_user'||issue.clarification.status==='analyzing'){
    if(restart)issue.clarification.questions=[];
  }
  renderIssueDetailTab();
  if(autoStart&&issue.clarification.status==='analyzing')attachIssueClarifyStream(issue.id);
  else if(autoStart&&clarifyKickoffPending(issue))setTimeout(()=>sendIssueClarification(CLARIFY_AUTO_PROMPTS[0]),0);
  else if(restart)setTimeout(()=>sendIssueClarification(CLARIFY_AUTO_PROMPTS[1]),0);
}
function collectIssueClarifyReply(){
  const parts=[];
  $$('.issue-clarify-question').forEach(row=>{
    const question=row.querySelector('b')?.textContent||'';
    const selected=[...row.querySelectorAll('.issue-clarify-option.selected')].map(x=>x.textContent.trim());
    const other=row.querySelector('.issue-clarify-other')?.value.trim();
    const answers=[...selected,...(other?[other]:[])];
    if(answers.length)parts.push(`${question} ${answers.join('、')}`);
  });
  const extra=$('#issueClarifyInput')?.value.trim();
  if(extra)parts.push(extra);
  return parts.join('\n');
}
function retryIssueClarifyTurn(){
  const issue=state.issueDetailData?.issue;if(!issue)return;
  if(issue.clarification)issue.clarification.lastError='';
  const last=(issue.clarification?.history||[]).filter(h=>h.role==='user').pop();
  sendIssueClarification(last?.content||CLARIFY_AUTO_PROMPTS[0]);
}
function issueClarifyThinking(ev,issueId){
  const job=issueId?issueClarifyJobs.get(issueId):null;
  if(job)job.thinking.push(ev);
  const box=$('#issueClarifyThinking');if(!box)return;
  if(job&&state.issueDetailData?.issue?.id!==issueId)return;
  appendIssueClarifyThought(box,ev);
}
function appendIssueClarifyThought(box,ev){
  if(ev.kind!=='tool'&&clarifyThoughtShouldHide(ev&&ev.text))return;
  box.hidden=false;
  const row=document.createElement('div');
  row.className=`think-row ${ev.kind==='tool'?'think-tool':(ev.kind==='status'?'think-status':'think-idea')}`;
  row.innerHTML=`<span class="think-icon">${ev.kind==='tool'?'🔍':(ev.kind==='status'?'⏳':'💭')}</span><span class="think-text">${escapeHTML(ev.kind==='tool'?`${clarifyToolLabel(ev.tool)}${ev.detail?' · '+ev.detail:''}`:(ev.text||''))}</span>`;
  box.appendChild(row);box.scrollTop=box.scrollHeight;
}
function restoreIssueClarifyThinking(issueId){
  const job=issueClarifyJobs.get(issueId),box=$('#issueClarifyThinking');
  if(!box)return;
  if(job){
    box.innerHTML='';
    job.thinking.forEach(ev=>appendIssueClarifyThought(box,ev));
  }
  const button=$('[data-continue-clarify]'),stop=$('[data-stop-clarify]'),hint=$('#issueClarifyHint');
  if(button){button.disabled=true;button.textContent='分析中…'}
  if(stop)stop.hidden=false;
  if(hint)hint.textContent='正在后台分析，关闭网页也不会中断';
}
function stopIssueClarification(issueId){
  const job=issueClarifyJobs.get(issueId);
  if(job)job.stopping=true;
  api(`/api/issues/${encodeURIComponent(issueId)}/chat-clarify/cancel`,{method:'POST',body:'{}'}).catch(()=>{});
  if(job?.abort)job.abort.abort();
}
async function attachIssueClarifyStream(issueId){
  if(!issueId)return;
  const existing=issueClarifyJobs.get(issueId);
  if(existing?.abort&&!existing.abort.signal.aborted&&existing.attached)return;
  const abort=new AbortController();
  const job={abort,thinking:existing?.thinking||[],attached:true};
  issueClarifyJobs.set(issueId,{...job});
  renderIssues();
  try{
    const resp=await fetch(`/api/issues/${encodeURIComponent(issueId)}/chat-clarify`,{headers:{Accept:'text/event-stream'},signal:abort.signal});
    if(resp.status===404){
      issueClarifyJobs.delete(issueId);
      return;
    }
    if(!resp.ok){let em=`HTTP ${resp.status}`;try{const d=await resp.json();em=d.error||em}catch{}throw new Error(em)}
    const data=await consumeIssueClarifyStream(resp,issueId);
    await applyIssueClarifyDone(issueId,data);
  }catch(err){
    if(err.name==='AbortError'){
      const current=issueClarifyJobs.get(issueId);
      if(current?.stopping)toast('已停止本轮澄清');
      return;
    }
    toast(`澄清失败：${err.message}`,'err');
    setIssueClarifyLastError(issueId,err.message);
  }finally{
    finishIssueClarifyJob(issueId,abort);
  }
}
function finishIssueClarifyJob(issueId,abort){
  const current=issueClarifyJobs.get(issueId);
  if(current?.abort===abort)issueClarifyJobs.delete(issueId);
  if(state.drawerOpen&&state.issueDetailData?.issue?.id===issueId)renderIssueDetailTab();
  renderIssues();
}
function setIssueClarifyLastError(issueId,message){
  const issue=state.issueDetailData?.issue;
  if(issue?.id!==issueId)return;
  issue.clarification=issue.clarification||{};
  issue.clarification.lastError=message||'';
}
async function applyIssueClarifyDone(issueId,data){
  if(!data||data.error)throw new Error(data?.error||'分析未返回结果');
  let updated=data.issue;
  if(!updated){
    try{const detail=await api(`/api/issues/${encodeURIComponent(issueId)}`);updated=detail.issue}catch{}
  }
  if(updated){
    const idx=(state.data.issues||[]).findIndex(x=>x.id===updated.id);if(idx>=0)state.data.issues[idx]=updated;
    if(state.issueDetailData?.issue?.id===issueId)state.issueDetailData.issue=updated;
  }
  if(state.drawerOpen&&state.issueDetailData?.issue?.id===issueId)renderIssueDetailTab();
  else{
    renderIssues();
    if($('#dashboardView')?.classList.contains('active'))renderDashboard();
  }
  const result=parseClarifyPayload(data);
  if(result.ready){
    const wb=updated?.clarification?.writeBackStatus;
    if(wb==='pending')toast('澄清完成，请确认结论后再写回原单','ok');
    else if(wb==='succeeded')toast('澄清完成，已写回原单','ok');
    else toast('澄清完成，结论已保存','ok');
  }
  else toast('澄清已更新，可以回来继续补充','ok');
}
async function consumeIssueClarifyStream(resp,issueId){
  const reader=resp.body.getReader(),decoder=new TextDecoder();let buffer='',done=null;
  const dispatch=(event,dataStr)=>{let payload={};try{payload=JSON.parse(dataStr)}catch{}if(event==='thought')issueClarifyThinking(payload,issueId);else if(event==='done')done=payload;else if(event==='error')done={error:payload.error||'分析失败'}};
  for(;;){
    const next=await reader.read();if(next.done)break;
    buffer+=decoder.decode(next.value,{stream:true});let idx;
    while((idx=buffer.indexOf('\n\n'))>=0){const chunk=buffer.slice(0,idx);buffer=buffer.slice(idx+2);let event='message',data='';chunk.split('\n').forEach(line=>{if(line.startsWith('event:'))event=line.slice(6).trim();else if(line.startsWith('data:'))data+=line.slice(5).trim()});if(data)dispatch(event,data)}
  }
  return done||{error:'分析未返回结果'};
}
async function sendIssueClarification(initialMessage=''){
  const issue=state.issueDetailData?.issue;if(!issue)return;
  const issueId=issue.id;
  if(issueClarifyJobs.has(issueId)&&issueClarifyJobs.get(issueId)?.attached)return toast('澄清正在后台进行');
  if(issue.clarification?.status==='analyzing'&&!initialMessage){
    attachIssueClarifyStream(issueId);
    return;
  }
  const msg=initialMessage||collectIssueClarifyReply();
  if(!msg)return toast('请先选择或填写答案');
  const c=issue.clarification||(issue.clarification={});
  rememberSubmittedClarifyDraft(issue,msg);
  c.lastError='';
  const prev=c.history||[];
  const last=prev[prev.length-1];
  const history=(last&&last.role==='user'&&last.content===msg)?prev.slice():[...prev,{role:'user',content:msg}];
  c.status='analyzing';
  c.history=history;
  const idxSeed=(state.data.issues||[]).findIndex(x=>x.id===issueId);if(idxSeed>=0)state.data.issues[idxSeed].clarification=c;
  const abort=new AbortController();
  issueClarifyJobs.set(issueId,{abort,thinking:[],attached:true});
  renderIssueDetailTab();
  renderIssues();
  try{
    const resp=await fetch(`/api/issues/${encodeURIComponent(issueId)}/chat-clarify`,{method:'POST',headers:{'Content-Type':'application/json','Accept':'text/event-stream'},body:JSON.stringify({message:msg,history,model:selectedClarifyModel()}),signal:abort.signal});
    if(!resp.ok){let em=`HTTP ${resp.status}`;try{const d=await resp.json();em=d.error||em}catch{}throw new Error(em)}
    const data=(resp.headers.get('Content-Type')||'').includes('text/event-stream')?await consumeIssueClarifyStream(resp,issueId):await resp.json();
    await applyIssueClarifyDone(issueId,data);
  }catch(err){
    if(c.status==='analyzing')c.status=(c.questions||[]).length?'in_progress':'needs_user';
    if(err.name==='AbortError'){
      const current=issueClarifyJobs.get(issueId);
      if(current?.stopping)toast('已停止本轮澄清');
      return;
    }
    toast(`澄清失败：${err.message}`,'err');
    setIssueClarifyLastError(issueId,err.message);
  }finally{
    finishIssueClarifyJob(issueId,abort);
  }
}
async function retryIssueClarificationWriteBack(){
  const issue=state.issueDetailData?.issue;if(!issue)return;
  try{
    const updated=await api(`/api/issues/${encodeURIComponent(issue.id)}/clarification`,{method:'POST',body:JSON.stringify({ready:true,retry:true})});
    state.issueDetailData.issue=updated;
    const idx=(state.data.issues||[]).findIndex(x=>x.id===updated.id);
    if(idx>=0)state.data.issues[idx]=updated;
    renderIssueDetailTab();
    renderIssues();
    toast(updated.clarification?.writeBackStatus==='succeeded'?'已写回原单评论，现在可以触发需求了！':updated.clarification?.writeBackStatus==='pending'?'结论已保存，尚未写回原单':`写回仍未成功：${updated.clarification?.writeBackError||'未知错误'}`,updated.clarification?.writeBackStatus==='succeeded'?'ok':'err');
  }catch(e){toast(e.message,'err')}
}
async function closeIssueById(issueId){
  const issue=(state.data.issues||[]).find(i=>i.id===issueId);
  if(!issue)return toast('未找到需求单');
  if(issueManuallyClosed(issue)||issueTrackerClosed(issue))return toast('该需求单已处于关闭状态');
  if(!issueCanClose(issue))return toast('需求正在澄清或执行 SOP，请先停止后再关闭','err');
  const ok=await afConfirm({
    title:'手动关闭需求',
    message:`确定手动关闭需求「${issue.title||issue.id}」？关闭后可在状态里筛「已关闭」。`,
    confirmText:'确认关闭'
  });
  if(!ok)return;
  try{
    const updated=await api(`/api/issues/${encodeURIComponent(issueId)}/close`,{method:'POST'});
    const idx=(state.data.issues||[]).findIndex(i=>i.id===issueId);
    if(idx>=0)state.data.issues[idx]=updated;
    if(state.issueDetailData?.issue?.id===issueId){
      state.issueDetailData.issue=updated;
      renderIssueDetailTab();
    }
    renderIssues();
    toast('需求已关闭','ok');
  }catch(err){
    toast(err.message,'err');
  }
}
async function resyncTAPDIssueById(issueId){
  const issue=(state.data.issues||[]).find(i=>i.id===issueId)||state.issueDetailData?.issue;
  if(!issue)return toast('未找到需求单');
  if(issue.source!=='tapd'||!issue.externalRef?.externalId)return toast('仅 TAPD 来源的需求支持重新同步');
  try{
    const updated=await api(`/api/issues/${encodeURIComponent(issueId)}/resync-tapd`,{method:'POST'});
    const idx=(state.data.issues||[]).findIndex(i=>i.id===issueId);
    if(idx>=0)state.data.issues[idx]=updated;
    if(state.issueDetailData?.issue?.id===issueId){
      state.issueDetailData.issue=updated;
      renderIssueDetailTab();
    }
    renderIssues();
    toast('已从 TAPD 同步最新内容','ok');
  }catch(err){
    toast(err.message,'err');
  }
}
function issueProgressActionsHTML(issue,run,{includeIssueActions=false}={}){
  const workflowActions=[],logActions=[],issueActions=[];
  if(run){
    const runId=escapeHTML(run.id);
    if(!gongfengIssueClosed(issue)&&!run.pauseRequested)workflowActions.push(refreshRunSOPButtonHTML(run));
    if(!issueExecutionBlocked(issue)){
      if(canManuallyPassFailedNode(run))workflowActions.push(`<button class="button ghost small" data-pass-node="${runId}" data-node="${escapeHTML(failedNodeId(run))}">手动通过失败脚本</button>`);
      if(run.status==='failed')workflowActions.push(triggerSOPButton(issue,{ghost:true,text:'整条 SOP 从头触发'}));
    }
    logActions.push(`<button class="button ghost small" data-open-logs="${runId}">查看运行与日志</button>`);
    logActions.push(`<button class="button ghost small" data-export-logs="${runId}">导出全部 Worker 日志</button>`);
  }
  if(includeIssueActions){
    issueActions.push('<button class="button ghost small" data-edit-issue>编辑需求</button>');
    if(issueCanPushTAPD(issue))issueActions.push(`<button class="button ghost small" data-push-tapd="${escapeHTML(issue.id)}">同步到 TAPD</button>`);
    if(issueCanClose(issue)&&issue.status!=='done'&&!issueManuallyClosed(issue)&&!issueTrackerClosed(issue)&&!deliveryLocked(issue))issueActions.push(`<button class="button ghost small" data-close-issue="${escapeHTML(issue.id)}">关闭需求</button>`);
    if(issueCanDelete(issue))issueActions.push(`<button class="button ghost small danger" data-delete-issue="${escapeHTML(issue.id)}">删除需求</button>`);
    if(issue.externalRef?.externalUrl)issueActions.push(`<a class="button ghost small" href="${escapeHTML(safeURL(issue.externalRef.externalUrl))}" target="_blank" rel="noopener noreferrer">打开外部链接 ↗</a>`);
    if(issue.source==='tapd'&&issue.externalRef?.externalId)issueActions.push(`<button class="button ghost small" data-resync-tapd="${escapeHTML(issue.id)}">从 TAPD 同步</button>`);
  }
  const group=(title,items)=>items.some(Boolean)?`<div class="issue-action-group"><span>${title}</span><div class="issue-action-group-buttons">${items.filter(Boolean).join('')}</div></div>`:'';
  const groups=[group('流程处理',workflowActions),group('排查记录',logActions),group('需求管理',issueActions)].filter(Boolean);
  return groups.length?`<div class="issue-action-groups">${groups.join('')}</div>`:'';
}
function overviewPane(issue,data){
  const project=(state.data.projects||[]).find(p=>p.id===issue.projectId),workflow=(state.data.workflows||[]).find(w=>w.id===(issue.workflowId||project?.workflowId)),repo=(state.data.repositories||[]).find(r=>r.id===(issue.repositoryId||project?.defaultRepositoryId));
  const run=data.run,out=issueOutcome(issue),stage=issueCurrentStage(issue),task=data.task;
  const waitingId=run&&waitingNodeId(run);
  const isRunning=(run&&['queued','running','waiting'].includes(run.status))||(task&&['queued','dispatched','running'].includes(task.status));
  let actions='';
  if(issueManuallyClosed(issue)||deliveryLocked(issue))actions=reopenDeliveredButton(issue)+deliveryMRButtons(issue);
  else if(out.key==='waiting'&&run&&waitingId&&!gongfengIssueClosed(issue))actions=run.pauseRequested?pausedRunPanelHTML(run):humanDecisionPanelHTML(run.id,waitingId,run.nodes?.[waitingId],run.workflowSnapshot?.nodes?.find(n=>n.id===waitingId),run.workflowSnapshot?.nodes||[]);
  else if(run?.status==='failed')actions=issueExecutionBlocked(issue)?triggerSOPButton(issue):failedResumePanelHTML(run);
  else if(run?.status==='cancelled')actions=triggerSOPButton(issue);
  else if(canRetriggerIssue(issue,run))actions=retriggerButton(issue,run);
  else if(!isRunning&&out.key!=='active'&&out.key!=='closed')actions=triggerSOPButton(issue);
  if(run?.status==='running')actions+=`<button class="button ghost small" data-pause-run="${escapeHTML(run.id)}">暂停运行</button>`;
  actions+=issueProgressActionsHTML(issue,run,{includeIssueActions:true});

  const banner=`<div class="issue-outcome ${out.key}"><b>${escapeHTML(out.title)}</b><p>${escapeHTML(out.body)}</p><div class="issue-outcome-actions">${actions}</div></div>`;
  const facts=`<div class="detail-grid">${drawerFact('责任人',issueAssigneeName(issue)||'未指定')}${drawerFact('优先级',issuePriorityLabel(issue.priority))}${drawerFact('来源',issue.source==='gongfeng'?'工蜂':issue.source==='tapd'?'TAPD':'平台')}${drawerFact('触发方式',escapeHTML(taskTriggerInfo(data.task).label))}${drawerFact('关联 SOP',workflow?.name||'未关联')}${drawerFact('代码仓库',repo?.name||'未绑定')}${drawerFact('继续开发分支',escapeHTML(storedIssueWorkBranch(issue)||'自动新建'))}</div>`;
  return `<div class="detail-section">${banner}${facts}${issue.description?`<div class="detail-desc" style="margin-top:12px">${escapeHTML(issue.description)}</div>`:''}${clarificationOverviewCard(issue)}</div>`;
}
// Bug-style items are rated on reproduction quality; requirements use the
// project's configurable policy. Mirrors clarificationRatingDimensions in Go.
function issueClarifyDimensions(issue,project){
  if(issue.source==='gongfeng'||issue.kind==='issue')return[
    {id:'phenomenon',name:'现象与复现'},{id:'impact',name:'影响范围'},{id:'expected',name:'期望行为'},
    {id:'acceptance',name:'验收标准'},{id:'constraints',name:'约束与依赖'}];
  return clarificationPolicy(project).dimensions||[];
}
function clarifyScoreLevel(score){return score>=80?'good':score>=60?'fair':'weak'}
function clarifyScoreHTML(issue,project){
  const c=issue.clarification||{},ratings=c.dimensions||{};
  if(!Object.keys(ratings).length)return'';
  const dims=issueClarifyDimensions(issue,project),threshold=clarificationPolicy(project).threshold||0;
  const bars=dims.filter(d=>ratings[d.id]!==undefined).map(d=>{
    const v=ratings[d.id]||0;
    return `<div class="clarify-rate"><span>${escapeHTML(d.name||d.id)}</span><i><b class="${clarifyScoreLevel(v)}" style="width:${v}%"></b></i><em>${v}</em></div>`;
  }).join('');
  const missingNames=(c.missing||[]).map(m=>{
    const dim=dims.find(d=>d.id===m);
    return dim?dim.name:m;
  });
  const missing=missingNames.map(name=>`<span class="clarify-missing-chip"><i></i>${escapeHTML(name)}</span>`).join('');
  const missingBlock=missing?`<div class="clarify-missing"><div class="clarify-missing-head"><svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 9v4"/><path d="M12 17h.01"/><path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0Z"/></svg><b>仍未确认 · ${missingNames.length} 项</b></div><div class="clarify-missing-list">${missing}</div></div>`:'';
  return `<div class="clarify-score">
    <div class="clarify-score-total ${clarifyScoreLevel(c.score||0)}"><b>${c.score||0}</b><span>信息完整度</span></div>
    <div class="clarify-score-body">${bars}${threshold?`<small>${c.status==='ready'||c.passed?`信息完整度 ${c.score||0} 分，澄清完成后可触发 SOP。`:`项目准入线 ${threshold} 分；未达标需先完成澄清。`}</small>`:''}</div>
  </div>${missingBlock}`;
}
function clarifyHistoryHTML(c){
  const rows=[];
  (c.history||[]).forEach(msg=>{
    const content=(msg.content||'').trim();
    if(!content)return;
    if(msg.role==='assistant'){
      let parsed={};try{parsed=JSON.parse(content)}catch{}
      const questions=(parsed.questions||[]).map(q=>`<li>${escapeHTML(q.question||'')}</li>`).join('');
      if(questions)rows.push(`<div class="clarify-turn ai"><span>问</span><ul>${questions}</ul></div>`);
    }else if(!CLARIFY_AUTO_PROMPTS.includes(content)){
      rows.push(`<div class="clarify-turn me"><span>答</span><p>${escapeHTML(content)}</p></div>`);
    }
  });
  if(!rows.length)return'';
  return `<details class="clarify-history"><summary>澄清过程（${rows.length} 条）</summary><div class="clarify-turns">${rows.join('')}</div></details>`;
}
function clarifyPane(issue,project){
  const c=recoverClarifyView(issue.clarification||{}),source=issue.source==='gongfeng'?'工蜂 Issue':issue.source==='tapd'?'TAPD 需求':'工作项';
  if(c.status==='ready'&&submittedClarifyDraft?.issueId===issue.id)submittedClarifyDraft=null;
  const writeBack=c.writeBackStatus==='succeeded'?`已写回 ${source}`:c.writeBackStatus==='failed'?'写回失败':c.writeBackStatus==='skipped'?'仅保存到 AgentFlow':'';
  if(c.status==='ready'&&c.summary){
    const pending=c.writeBackStatus==='pending'&&(issue.source==='tapd'||issue.source==='gongfeng')&&issue.externalRef;
    const retry=c.writeBackStatus==='failed'?`<button class="button primary small" data-retry-clarify>重试写回原单</button>`:'';
    const confirm=pending?`<button class="button primary small" data-retry-clarify title="确认写回原单（追加评论）之后，即可开始触发需求">确认写回原单（追加评论）</button>`:'';
    const external=issue.externalRef?.externalUrl?`<a class="button ghost small" href="${escapeHTML(safeURL(issue.externalRef.externalUrl))}" target="_blank" rel="noopener noreferrer">打开原单 ↗</a>`:'';
    const trigger=!pending&&!issueExecutionBlocked(issue)?`<button class="button primary small" data-dispatch-issue="${escapeHTML(issue.id)}" title="需求已具备进入研发准入条件，点击直接触发 SOP">触发需求</button>`:'';
    const blockedHint=!pending&&issueExecutionBlocked(issue)?(issueTrackerClosed(issue)&&!issueManuallyClosed(issue)?closedTrackerHint(issue):deliveredHint(issue)):'';
    const stepGuide=pending?`<div class="clarify-next-step-callout"><div class="clarify-step-badge active"><span class="step-num">1</span><span>确认写回原单（追加评论）</span></div><span class="clarify-step-arrow">➔</span><div class="clarify-step-badge"><span class="step-num">2</span><span>触发需求执行 SOP</span></div></div>`:'';
    const head=pending
      ?`<div class="clarify-ready-head"><span class="clarify-check warn">!</span><div><b>请确认澄清结论并写回原单</b><small>结论已追加到平台说明。确认写回原单（追加评论）后，即可开始触发需求执行 SOP（不会修改原单描述）</small></div></div>${stepGuide}`
      :`<div class="clarify-ready-head"><span class="clarify-check">✓</span><div><b>澄清已完成${c.writeBackStatus==='succeeded'?` · 已写回 ${source} 评论`:''}</b><small class="${c.writeBackStatus==='failed'?'danger':''}">${escapeHTML(writeBack||'结论已保存到平台说明')}，已满足准入条件，可直接触发需求执行 SOP</small></div></div>`;
    const actionTip=pending
      ?`<div class="clarify-action-hint">确认写回原单（追加评论）后，即可开始触发需求</div>`
      :(trigger?`<div class="clarify-action-hint ok">已写回原单并满足准入条件，可直接点击「触发需求」开始执行</div>`:'');
    return `<div class="clarify-workspace">${head}${clarifyVisibleSummary(c.summary)?`<div class="clarify-summary">${escapeHTML(clarifyVisibleSummary(c.summary))}</div>`:''}${clarifyScoreHTML(issue,project)}${c.writeBackError?`<div class="clarify-error">${escapeHTML(c.writeBackError)}</div>`:''}${clarifyHistoryHTML(c)}<div class="clarify-actions">${confirm}${trigger}${blockedHint}${retry}${external}<button class="button ghost small" data-reclarify>再问一轮</button>${actionTip}</div></div>`;
  }
  const questions=c.questions||[];
  const analyzing=issueClarifying(issue.id);
  const saved=clarifyAnswerDraft(issue);
  const questionHTML=analyzing?'':clarifyQuestionCardsHTML(questions,saved);
  const submittedHTML=analyzing?clarifySubmittedHTML(saved?.reply):'';
  if(c.status!=='in_progress'&&!analyzing){
    const scored=c.score||Object.keys(c.dimensions||{}).length;
    const need=requirementNeedsClarification(issue);
    const intro=need
      ?`<div class="clarify-orb">!</div><h3>导入评分 ${c.score||0} 分，需要你补充</h3><p>未达项目准入线。分析师会结合这张单和本项目代码提出关键问题，补齐后再触发 SOP。</p>`
      :`<div class="clarify-orb">?</div><h3>描述不够清楚？一起问明白</h3><p>分析师会结合这张单和本项目代码提出关键问题。确认信息够用后，这里会出现「澄清已完成」，才能触发需求。</p>`;
    const legacy=scored&&!need?`<div class="clarify-summary compact">已有结构化澄清：${c.score||0}/100 · ${c.passed?'已通过':'未通过'}。</div>`:'';
    return `<div class="clarify-workspace clarify-empty">${legacy}${intro}${need?clarifyScoreHTML(issue,project):''}<div class="clarify-start-row">${clarifyModelPickerHTML(false)}<button class="button primary" data-start-clarify>${need?'开始澄清':'开始澄清'}</button></div></div>`;
  }
  const lastError=(!analyzing&&c.lastError)?`<div class="clarify-error req-clarify-retry"><span>${escapeHTML(c.lastError)}</span><button type="button" class="button primary small" data-retry-clarify-turn>重新分析</button></div>`:'';
  const rounds=(c.history||[]).filter(m=>m.role==='assistant').length;
  const lastParsed=latestClarifyParsed(c);
  const progressIssue={...issue,clarification:{...c,...((!c.score&&lastParsed?.assessment)?{dimensions:lastParsed.assessment.dimensions||{},missing:lastParsed.assessment.missing||[]}:{})}};
  const scoreBlock=clarifyScoreHTML(progressIssue,project);
  const nextHint=analyzing?'正在后台分析，关闭详情不会中断':(c.lastError?'上次分析未完成，可重新分析':`回答后点「继续澄清」。分析师确认够用后会出现「澄清已完成」，才能触发需求。已澄清 ${rounds||0} 轮。`);
  const composerInput=analyzing?'':`<textarea id="issueClarifyInput" rows="3" placeholder="${questions.length?'点选上面的答案，或补充说明':'补充你认为重要的信息'}">${escapeHTML(saved?.extra||'')}</textarea>`;
  return `<div class="clarify-workspace"><div class="clarify-intro"><span>AI</span><div><b>${analyzing?'正在分析这张':'继续完善这张'}${issue.source==='gongfeng'?' Issue':'需求'}</b><small>${analyzing?'关闭详情不会中断，再打开可继续看过程':'每轮只问最关键的 1–3 个问题；确认够用前不会出现「触发需求」'}</small></div>${clarifyModelPickerHTML(analyzing)}</div>${clarifyVisibleSummary(c.summary)?`<div class="clarify-summary compact">${escapeHTML(clarifyVisibleSummary(c.summary))}</div>`:''}${scoreBlock}${clarifyHistoryHTML(c)}${submittedHTML}${questionHTML}${lastError}<div id="issueClarifyThinking" class="req-thinking-stream" hidden></div><div class="issue-clarify-composer">${composerInput}<div><span id="issueClarifyHint">${nextHint}</span><span><button class="button ghost small" data-stop-clarify ${analyzing?'':'hidden'}>停止</button> <button class="button primary small" data-continue-clarify${analyzing?' disabled':''}>${analyzing?'分析中…':(c.lastError?'重新分析':'继续澄清')}</button></span></div></div></div>`;
}
function latestClarifyParsed(c){
  const rows=[...(c?.history||[])].reverse();
  for(const msg of rows){
    if(msg.role!=='assistant')continue;
    try{
      const parsed=JSON.parse(msg.content||'');
      if(parsed&&typeof parsed==='object'&&(parsed.questions||parsed.ready||parsed.assessment))return parsed;
    }catch{}
  }
  return null;
}
function clarifyVisibleSummary(s){
  const t=String(s||'').trim();
  if(!t)return '';
  const proto=clarifyExtractProtocol(t);
  if(proto&&( 'mcpCalls' in proto||'suggestedTitle' in proto||('summary' in proto&&'questions' in proto))){
    const head=t.charAt(0)==='{'||t.charAt(0)==='['?'':t.slice(0,t.indexOf('{')).trim();
    if(head&&!clarifyThoughtShouldHide(head))return head;
    const inner=String(proto.summary||'').trim();
    return inner&&!clarifyThoughtShouldHide(inner)?inner:'';
  }
  if(clarifyThoughtShouldHide(t))return '';
  return t;
}
function clarifyExtractProtocol(s){
  let t=String(s||'').trim();
  if(!t)return null;
  t=t.replace(/^```json\s*/i,'').replace(/^```\s*/,'').replace(/\s*```$/,'');
  const start=t.indexOf('{'),end=t.lastIndexOf('}');
  if(start<0||end<=start)return null;
  try{
    const m=JSON.parse(t.slice(start,end+1));
    return m&&typeof m==='object'?m:null;
  }catch{return null}
}
function recoverClarifyView(src){
  const c={...(src||{})};
  const proto=clarifyExtractProtocol(c.summary);
  if(proto){
    if(!(c.questions||[]).length&&Array.isArray(proto.questions)){
      c.questions=normalizeClarifyParsed(proto).questions;
    }
    c.summary=clarifyVisibleSummary(c.summary);
  }
  return c;
}
function clarifyThoughtShouldHide(s){
  let t=String(s||'').trim();
  if(!t)return false;
  t=t.replace(/^```json\s*/i,'').replace(/^```\s*/,'').replace(/\s*```$/,'');
  if(t.charAt(0)==='{'||t.charAt(0)==='[')return true;
  if(/"questions"\s*:/.test(t)&&(/"summary"\s*:/.test(t)||/"mcpCalls"\s*:/.test(t)))return true;
  const start=t.indexOf('{'),end=t.lastIndexOf('}');
  if(start<0||end<=start)return false;
  try{
    const m=JSON.parse(t.slice(start,end+1));
    if(!m||typeof m!=='object')return false;
    return 'mcpCalls' in m||'suggestedTitle' in m||('summary' in m&&'questions' in m);
  }catch{return false}
}
function plannedSOPNodes(lane,wf){
  if(lane?.stages?.length)return lane.stages;
  return (wf?.nodes||[]).map(n=>({nodeId:n.id,name:n.name||n.id,status:'pending'}));
}
function sopPane(issue,data){
  const snap=data.requirement||{};
  const lanes=snap.lanes||clientIssueLanes(issue);
  const overall=snap.overall||{};
  const out=issueOutcome(issue);
  const run=data.run;
  const workflow=(state.data.workflows||[]).find(w=>w.id===(issue.workflowId||defaultSOPForIssue(issue)));
  const waitingId=run&&waitingNodeId(run);
  const nodes=run?.workflowSnapshot?.nodes||workflow?.nodes||[];
  let actions='';
  if(issueManuallyClosed(issue)||deliveryLocked(issue))actions=reopenDeliveredButton(issue)+deliveryMRButtons(issue);
  else if(out.key==='waiting'&&waitingId&&!gongfengIssueClosed(issue))actions=run.pauseRequested?pausedRunPanelHTML(run):humanDecisionPanelHTML(run.id,waitingId,run.nodes?.[waitingId],nodes.find(n=>n.id===waitingId),nodes);
  else if(run?.status==='failed')actions=issueExecutionBlocked(issue)?triggerSOPButton(issue):failedResumePanelHTML(run);
  else if(run?.status==='cancelled')actions=triggerSOPButton(issue);
  if(run?.status==='running')actions+=`<button class="button ghost small" data-pause-run="${escapeHTML(run.id)}">暂停运行</button>`;
  actions+=issueProgressActionsHTML(issue,run);
  const overallFacts=`<div class="req-overall ${out.key}"><b>${escapeHTML(out.title)}</b><p>整体 ${overall.runCount||0} 次执行 · ${formatTokens(overall.tokens||0)} tokens · 周期 ${overall.cycleDurationMs?duration(overall.cycleDurationMs):'—'}</p>${actions?`<div class="issue-outcome-actions">${actions}</div>`:''}</div>`;
  const bound=lanes.filter(l=>!l.orphan);
  const orphans=lanes.filter(l=>l.orphan);
  const busy=bound.find(l=>['active','waiting','attention'].includes(l.status));
  const openId=state.issueSOPOpen||busy?.workflowId||bound[0]?.workflowId||issue.workflowId||'';
  const cards=bound.map((lane,idx)=>{
    const wf=(state.data.workflows||[]).find(w=>w.id===lane.workflowId);
    const laneRun=lane.latestRunId?(state.data.runs||[]).find(r=>r.id===lane.latestRunId):null;
    const open=openId===lane.workflowId;
    const stageRows=(stages,depth)=>(stages||[]).map(n=>`<div class="node-progress-row${depth?' is-child-stage':''}"${depth?` style="padding-left:${depth*20}px"`:''}><span class="node-name">${depth?'└ ':''}${escapeHTML(n.name||n.nodeId)}</span><div class="node-progress-track"><i style="width:${n.status==='succeeded'||n.status==='failed'?100:n.status==='running'||n.status==='waiting_approval'?55:0}%;background:${n.status==='succeeded'?'#34d399':n.status==='failed'?'#f87171':n.status==='waiting_approval'?'#fbbf24':n.status==='running'?'#5b8cff':'#64748b'}"></i></div><span class="status ${n.status}" style="flex-shrink:0">${statusText(n.status)||'未开始'}</span>${n.childRunId?`<button type="button" class="button ghost small" data-open-run="${escapeHTML(n.childRunId)}" title="查看这个子 SOP 的运行">查看</button>`:''}${!depth&&!issueManuallyClosed(issue)&&!issueTrackerClosed(issue)&&canRerunFromHere(laneRun,n.nodeId)?`<button type="button" class="button ghost small" data-rerun-from-here="${escapeHTML(n.nodeId)}" data-run="${escapeHTML(lane.latestRunId)}">从这里重跑</button>`:''}</div>${stageRows(n.children,depth+1)}`).join('');
    const nodeRows=stageRows(plannedSOPNodes(lane,wf),0);
    const singleSOP=effectiveIssueSOPBindings(issue).length<=1;
    const actionWorkflow=singleSOP&&String(issue.workflowId||'').trim()?issue.workflowId:lane.workflowId;
    const canStart=actionWorkflow&&!issueExecutionBlocked(issue)&&!clarificationNeedsConfirm(issue)&&!requirementNeedsClarification(issue)&&!lane.started;
    const canRetriggerLane=actionWorkflow&&lane.started&&!issueManuallyClosed(issue)&&!issueTrackerClosed(issue)&&!clarificationNeedsConfirm(issue)&&!requirementNeedsClarification(issue);
    const trigger=canStart?`<button class="button primary small" data-dispatch-issue="${escapeHTML(issue.id)}" data-workflow="${escapeHTML(actionWorkflow)}">触发</button>`:(canRetriggerLane?`<button class="button ghost small" data-retrigger-issue="${escapeHTML(issue.id)}" data-workflow="${escapeHTML(actionWorkflow)}">重新触发</button>`:'');
    const ownerName=userDisplay(userById(lane.assigneeUserId||issue.assigneeUserId))||'需求跟进人';
    const hint=lane.started?`${lane.runCount||0} 次 · ${formatTokens(lane.tokens||0)}`:'展开可看节点';
    return `<article class="req-lane-card req-sop-row ${escapeHTML(lane.status||'')} ${open?'is-open':''}" data-toggle-sop-row="${escapeHTML(lane.workflowId||'')}">
      <div class="req-sop-main">
        <span class="req-sop-idx">${idx+1}</span>
        <div class="req-sop-meta"><b>${escapeHTML(lane.label||lane.workflowName||wf?.name||'SOP')}</b><small>${escapeHTML(sopLaneLabel(lane.lane))} · ${escapeHTML(laneStatusLabel(lane.status))} · ${escapeHTML(ownerName)} · ${escapeHTML(hint)}</small></div>
        <div class="req-sop-acts">${trigger}${lane.latestRunId?`<button class="button ghost small" data-open-run="${escapeHTML(lane.latestRunId)}">查看</button>`:''}</div>
      </div>
      ${open?`<div class="req-sop-detail">${nodeRows||`<div class="detail-empty">${lane.started?'暂无节点':'这条 SOP 还没有节点'}</div>`}</div>`:''}
    </article>`;
  }).join('');
  const orphanHtml=orphans.length?`<div class="req-orphan">${orphans.map(lane=>`<div class="req-orphan-row"><span>还跑过 ${escapeHTML(lane.label||lane.workflowName||lane.workflowId)} · ${escapeHTML(laneStatusLabel(lane.status))}</span>${lane.latestRunId?`<button class="button ghost small" data-open-run="${escapeHTML(lane.latestRunId)}">查看</button>`:''}</div>`).join('')}</div>`:'';
  if(!bound.length&&!orphans.length&&!workflow){
    return `<div class="detail-section"><div class="req-sop-empty"><p>这条需求还没有可跑的流程。</p></div></div>`;
  }
  return `<div class="detail-section">${overallFacts}<div class="req-sop-list">${cards||''}</div>${orphanHtml}</div>`;
}
function artifactsPane(data){
  const run=data.run;
  if(!run||!run.artifacts?.length)return '<div class="detail-empty">该需求尚未产生交付物。执行 SOP 后，节点按产物契约冻结不可变快照。</div>';
  const grouped={};run.artifacts.forEach(a=>{(grouped[a.nodeId]=grouped[a.nodeId]||[]).push(a)});
  const wf=run.workflowSnapshot;
  const html=Object.entries(grouped).map(([nodeId,list])=>`<div class="detail-section"><h3>${escapeHTML(wf?.nodes?.find(n=>n.id===nodeId)?.name||nodeId)}<span class="sub">${list.length} 份快照</span></h3><div class="artifact-grid" style="padding:0">${list.map(a=>`<div class="artifact-card" data-artifact="${escapeHTML(a.id)}"><b>${escapeHTML(a.name)}</b><span>${escapeHTML(a.path)}</span><small>attempt ${a.attempt} · ${Math.max(1,Math.ceil((a.size||0)/1024))} KB</small><code>${escapeHTML((a.sha256||'').slice(0,10))}…</code></div>`).join('')}</div></div>`).join('');
  return html+`<div class="detail-section"><h3>运行信息</h3><div class="delivery-run" data-open-run="${escapeHTML(run.id)}"><span class="dot ${run.status}"></span><div style="flex:1;min-width:0"><div class="strong ellipsis">${escapeHTML(run.workflowSnapshot?.name||run.workflowId)}</div><div class="muted" style="font-size:10.5px">${run.id} · ${runDisplayStatus(run)} · ${duration(run.durationMs)}</div></div><button class="button ghost small">查看运行</button></div></div>`;
}
// 多仓库需求按仓库逐组展示；优先用 Run 冻结的仓库上下文（含工作分支），再补需求绑定的仓库。
function deliveryRepositories(issue,run){
  const catalog=state.data.repositories||[],project=(state.data.projects||[]).find(p=>p.id===issue.projectId);
  const frozen=(run?.repositories?.length?run.repositories:(run?.repository?.repositoryId?[run.repository]:[]));
  const ids=[...new Set([...(issue.repositoryIds||[]),issue.repositoryId,...frozen.map(r=>r.repositoryId)].filter(Boolean))];
  if(!ids.length&&project?.defaultRepositoryId)ids.push(project.defaultRepositoryId);
  return ids.map(id=>{const repo=catalog.find(r=>r.id===id)||{},rc=frozen.find(r=>r.repositoryId===id)||{};return{id,name:repo.name||rc.name||id,project:repo.gongfengProjectId||rc.gongfengProjectId||'',defaultBranch:repo.defaultBranch||rc.defaultBranch||'',baseBranch:rc.baseBranch||repo.defaultBranch||rc.defaultBranch||'',workBranch:rc.workBranch||(ids.length===1?storedIssueWorkBranch(issue):'')||'',webUrl:repo.webUrl||rc.webUrl||''}});
}
function deliveryRepoFacts(issue,repos){
  if(!repos.length)return `<div class="detail-grid">${drawerFact('仓库','未绑定')}</div>`;
  const mrs=deliveryMRList(issue),multi=repos.length>1;
  return repos.map(r=>{
    const mr=mrs.find(m=>(m.repositoryId&&m.repositoryId===r.id)||(r.project&&String(m.project||'').toLowerCase()===r.project.toLowerCase()));
    const iid=mr&&(mr.iid||String(mr.url).match(/merge_requests\/(\d+)/)?.[1]);
    const link=(label,url)=>`<div class="detail-fact"><span>${label}</span><b><a href="${escapeHTML(safeURL(url))}" target="_blank" rel="noopener noreferrer">${escapeHTML(url===mr?.url&&iid?`!${iid}`:'打开')} ↗</a></b></div>`;
    const grid=`<div class="detail-grid">${multi?'':drawerFact('仓库',escapeHTML(r.name))}${drawerFact('仓库路径',escapeHTML(r.project||'—'))}${drawerFact('配置主分支',escapeHTML(r.defaultBranch||'—'))}${drawerFact('目标主分支',escapeHTML(r.baseBranch||'—'))}${drawerFact('工作分支',escapeHTML(r.workBranch||'待新建'))}${mr?link('交付 PR',mr.url):''}${r.webUrl?link('仓库链接',r.webUrl):''}</div>`;
    return multi?`<div class="delivery-repo"><div class="delivery-repo-name">${escapeHTML(r.name)}</div>${grid}</div>`:grid;
  }).join('');
}
function deliveryPane(issue,data){
  const run=data.run,repos=deliveryRepositories(issue,run),frozen=run?.repositories?.length?run.repositories:(run?.repository?.name?[run.repository]:[]);
  const repoFacts=deliveryRepoFacts(issue,repos);
  const frozenBranches=[...new Set(frozen.map(r=>r.baseBranch).filter(Boolean))];
  const ctx=run?.executionContext;
  const runFacts=`<div class="detail-grid">${drawerFact('Run ID',run?run.id.slice(-14):'—')}${drawerFact('Task ID',data.task?data.task.id.slice(-10):'—')}${drawerFact('触发方式',escapeHTML(taskTriggerInfo(data.task).label))}${drawerFact('状态',run?`<span class="status ${runDisplayStatusClass(run)}">${runDisplayStatus(run)}</span>`:'—')}${drawerFact('执行仓库',escapeHTML(frozen.map(r=>r.name).filter(Boolean).join('、')||'—'))}${drawerFact('冻结目标主分支',escapeHTML(frozenBranches.join('、')||'—'))}${drawerFact('Trace ID',ctx?.traceId?ctx.traceId.slice(-14):'—')}</div>`;
  return `<div class="detail-section"><h3>代码仓库与分支${repos.length>1?`<span class="sub">${repos.length} 个仓库</span>`:''}</h3>${repoFacts}</div><div class="detail-section"><h3>交付运行上下文<span class="sub">发起时冻结的仓库与 Trace 关联</span></h3>${run?runFacts+'<div style="margin-top:10px"><button class="button ghost small" data-open-run="'+escapeHTML(run.id)+'">查看运行详情与产物 →</button></div>':'<div class="detail-empty">尚未发起交付运行</div>'}</div>${run?.input?`<div class="detail-section"><h3>任务输入</h3><div class="detail-desc">${escapeHTML(run.input)}</div></div>`:''}`;
}
function acceptancePane(issue,data){
  const run=data.run;
  if(!run)return '<div class="detail-empty">尚未执行，暂无验收信息。</div>';
  const wf=run.workflowSnapshot||(state.data.workflows||[]).find(w=>w.id===run.workflowId);
  const nodes=wf?.nodes||[];
  const gates=nodes.map(n=>{const nr=run.nodes?.[n.id]||{},agent=(state.data.agents||[]).find(a=>a.id===n.agentId),verif=roleGates(agent);const isApproval=n.type==='approval';const manual=verif.some(g=>g.mode==='manual');const commands=verif.filter(g=>g.mode==='command');let verdict='—';
    if(nr.status==='succeeded')verdict=isApproval?'已批准':(commands.length||manual)?'门禁通过':'完成';
    if(nr.status==='waiting_approval')verdict=turnsExhausted(nr)?'等待确认是否重试':(isApproval?'等待人工判断':'等待人工验收');
    if(nr.status==='failed')verdict='门禁失败';
    if(nr.status==='running')verdict='执行中';
    return {name:n.name||n.id,status:nr.status||'pending',verdict,commands:commands.length,manual,rejectTo:n.rejectTo,nodeId:n.id,nr,node:n};
  });
  const rows=gates.map(g=>`<div class="node-progress-row"><span class="node-name">${escapeHTML(g.name)}</span><span class="pill">${g.commands} 项命令 · ${g.manual?'需人工':''}${g.rejectTo?' · 可打回':''}</span><div style="flex:1"></div><span class="status ${g.status}">${escapeHTML(g.verdict)}</span></div>${g.status==='waiting_approval'?humanDecisionPanelHTML(run.id,g.nodeId,g.nr,g.node,nodes):''}`).join('');
  return `<div class="detail-section"><h3>验收门禁<span class="sub">节点自动验证与人工门禁，失败可打回指定节点</span></h3>${rows||'<div class="detail-empty">该流程没有配置验证门禁</div>'}</div>`;
}
function formatActivityTime(ts){
  const d=new Date(ts);
  if(Number.isNaN(d.getTime()))return '—';
  const p=n=>String(n).padStart(2,'0');
  return `${p(d.getMonth()+1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}
function activityEventLabel(type){
  return ({
    'run.started':'开始执行','run.failed':'执行失败','run.succeeded':'执行完成','run.cancelled':'已取消',
    'node.started':'节点开始','node.failed':'节点失败','node.retry':'准备重试','node.succeeded':'节点完成','node.passed':'人工通过节点',
    'task.created':'创建任务'
  }[type]||String(type||'').replace(/^node\./,'节点 · ').replace(/^run\./,'运行 · '));
}
function issueOperationLabel(action){
  return {create:'创建需求',update:'编辑需求',close:'关闭需求',reopen:'重新打开',dispatch:'触发 SOP'}[action]||action||'操作';
}
function issueOperationEntries(issue){
  const stored=(issue?.operations||[]).map(op=>({
    t:new Date(op.at).getTime(),
    action:op.action||'',
    actor:op.actor||'',
    label:issueOperationLabel(op.action),
    detail:op.detail||''
  })).filter(op=>!Number.isNaN(op.t));
  const inferred=[];
  if(issue?.createdAt&&!stored.some(op=>op.action==='create')){
    inferred.push({t:new Date(issue.createdAt).getTime(),action:'create',actor:'',label:'创建需求',detail:'按创建时间补记'});
  }
  if(issueManuallyClosed(issue)&&issue.deliveryUpdatedAt&&!stored.some(op=>op.action==='close')){
    inferred.push({t:new Date(issue.deliveryUpdatedAt).getTime(),action:'close',actor:'',label:'关闭需求',detail:'手动关闭'});
  }
  return stored.concat(inferred).filter(op=>!Number.isNaN(op.t)).sort((a,b)=>b.t-a.t);
}
function operationsPane(issue){
  const rows=issueOperationEntries(issue).map(e=>{
    const detail=[e.actor,e.detail].filter(Boolean).join(' · ');
    return `<div class="activity-row"><time>${formatActivityTime(e.t)}</time><b>${escapeHTML(e.label)}</b><p>${escapeHTML(detail||'—')}</p></div>`;
  }).join('');
  return `<div class="detail-section"><h3>操作日志<span class="sub">关闭、重新打开、编辑和触发都会记在这里</span></h3>${rows?`<div class="activity-list">${rows}</div>`:'<div class="detail-empty compact">还没有操作记录</div>'}</div>`;
}
function activityPane(issue,data){
  const comments=data.comments||[],task=data.task,run=data.run;
  const commentHtml=comments.slice().reverse().map(c=>`<div class="comment-row"><div class="muted">${c.authorType==='agent'?'Agent':c.authorId||'成员'} · ${new Date(c.createdAt).toLocaleString()}</div><p>${escapeHTML(c.body)}</p></div>`).join('')||'<div class="detail-empty compact">暂无讨论</div>';
  const items=[];
  if(task)items.push({t:new Date(task.createdAt).getTime(),type:'task.created',label:'创建任务',detail:`${taskStatusLabel(task.status)}${task.agentId?' · '+task.agentId:''}`});
  for(const e of run?.events||[])items.push({t:new Date(e.timestamp).getTime(),type:e.type||'',label:activityEventLabel(e.type),detail:e.message||''});
  items.sort((a,b)=>b.t-a.t);
  const rows=items.map(e=>{
    const fail=/fail|error/i.test(e.type);
    const detail=e.detail.length>180?e.detail.slice(0,180)+'…':e.detail;
    return `<div class="activity-row${fail?' fail':''}"><time>${formatActivityTime(e.t)}</time><b>${escapeHTML(e.label)}</b><p title="${escapeHTML(e.detail)}">${escapeHTML(detail)}</p></div>`;
  }).join('');
  return `<div class="detail-section"><h3>讨论与评论</h3>${commentHtml}</div><div class="detail-section"><h3>任务与运行轨迹</h3>${rows?`<div class="activity-list">${rows}</div>`:'<div class="detail-empty compact">尚未创建执行任务</div>'}</div>`;
}


async function deleteIssueById(issueId){
  const issue=(state.data.issues||[]).find(i=>i.id===issueId)||state.issueDetailData?.issue;
  if(!issue||issue.id!==issueId)return toast('未找到需求单');
  if(issueClarifying(issueId))return toast('需求正在澄清，请先停止后再删除','err');
  const external=issue.source==='tapd'||issue.source==='gongfeng';
  if(!await afConfirm({title:'删除需求',message:`确定删除需求「${issue.title||issue.id}」？需求、评论、澄清记录以及失败／取消的历史任务、运行记录、日志、产物和执行工作区将一并清理，且不可恢复。已有成功运行的需求不允许删除。${external?'此操作仅删除本地记录，不会删除 TAPD／工蜂原单，后续同步可能重新导入。':''}`,danger:true,confirmText:'确认删除'}))return;
  try{
    await api(`/api/issues/${encodeURIComponent(issueId)}`,{method:'DELETE'});
    state.data.issues=(state.data.issues||[]).filter(i=>i.id!==issueId);
    if(state.issueDetailData?.issue?.id===issueId){closeIssueDrawer();state.issueDetailData=null;state.selectedIssue=null;}
    state.data.tasks=(state.data.tasks||[]).filter(t=>t.issueId!==issueId);
    await refreshRuns();
    renderIssues();
    toast('需求已删除','ok');
  }catch(err){toast(err.message,'err')}
}
