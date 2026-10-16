// ================= 待办验收 =================
function waitingNodeId(run){return run.status==='queued'?undefined:Object.keys(run.nodes||{}).find(id=>run.nodes[id]?.status==='waiting_approval')}
function turnsExhausted(nr){const t=`${nr?.error||''} ${nr?.activity||''}`;return /max turns|轮次用尽|是否重试/i.test(t)}
function humanContextFieldHTML(){
  return `<label class="human-context-field"><span class="human-context-label">补充上下文（可选）</span><textarea data-human-context rows="3" placeholder="例如：报告里缺少性能数据，补充后再继续；或告诉 Agent 下一步要注意什么"></textarea></label>`;
}
function readHumanContext(fromEl){
  const root=fromEl?.closest?.('[data-human-context-root], .approval-callout, .ap-card, .issue-outcome, .node-inspector, .human-decision');
  return String(root?.querySelector?.('[data-human-context]')?.value||'').trim();
}
function withHumanContext(payload){
  const context=String(payload?.context||'').trim();
  if(!context)return payload;
  const reason=String(payload.reason||'').trim();
  return {...payload, context, reason:reason?`${reason}\n\n人工补充上下文：\n${context}`:context};
}
function reworkTarget(node,nodes){
  const id=String(node?.rejectTo||'').trim();
  if(!id)return null;
  return {id,name:(nodes||[]).find(n=>n.id===id)?.name||id};
}
function queuedRunActionBlocked(runId){if(cachedRun(runId)?.status!=='queued')return false;toast('运行正在排队，请先取消排队','err');return true}
function waitingActionButtons(runId,nodeId,nr,node,nodes){
  if(cachedRun(runId)?.status==='queued')return '';
  if(turnsExhausted(nr))return `<button class="button primary small" data-retry-node="${escapeHTML(runId)}" data-node="${escapeHTML(nodeId)}">重试本节点</button><button class="button danger-outline small" data-decline-retry="${escapeHTML(runId)}" data-node="${escapeHTML(nodeId)}">不重试</button>`;
  const target=reworkTarget(node,nodes);
  const rework=target?`<button class="button ghost small" data-reject="${escapeHTML(runId)}" data-node="${escapeHTML(nodeId)}" data-target="${escapeHTML(target.id)}" data-target-name="${escapeHTML(target.name)}">${lfT('退回上游「'+escapeHTML(target.name)+'」并重跑后续','Send back to “'+escapeHTML(target.name)+'” and rerun downstream')}</button>`:'';
  return `<button class="button primary small" data-approve="${escapeHTML(runId)}" data-node="${escapeHTML(nodeId)}">确认通过，继续下游</button><button class="button ghost small" data-retry-node="${escapeHTML(runId)}" data-node="${escapeHTML(nodeId)}">补充信息，重跑当前节点</button>${rework}`;
}
function humanGateSectionsHTML(nr){
  if(turnsExhausted(nr)){
    return `<div class="callout-section"><strong class="callout-label">说明</strong><p class="callout-text">${escapeHTML(nr?.error||'请确认是否重试本节点。不重试则本节点失败。')}</p></div>`;
  }
  const gate=nr?.gateResult;
  const sections=[];
  if(gate){
    if(gate.actionRequired){
      sections.push(`<div class="callout-section"><strong class="callout-label">需要确认</strong><p class="callout-text">${escapeHTML(gate.actionRequired)}</p></div>`);
    }
    if(gate.reason){
      sections.push(`<div class="callout-section"><strong class="callout-label">Agent 判断依据</strong><p class="callout-text">${escapeHTML(gate.reason)}</p></div>`);
    }
    if(gate.summary){
      sections.push(`<div class="callout-section"><strong class="callout-label">摘要</strong><p class="callout-text">${escapeHTML(gate.summary)}</p></div>`);
    }
    if(gate.findings&&gate.findings.length){
      const items=gate.findings.map(f=>`<li><b>${escapeHTML(f.title||'待确认项')}</b>${f.evidence?`<p>${escapeHTML(f.evidence)}</p>`:''}</li>`).join('');
      sections.push(`<div class="callout-section"><strong class="callout-label">待确认项与证据</strong><ul class="callout-findings">${items}</ul></div>`);
    }
  }
  if(!sections.length){
    const err=nr?.error||nr?.feedback||'门禁未通过。请看产物和日志后决定。';
    sections.push(`<div class="callout-section"><strong class="callout-label">判断依据</strong><p class="callout-text">${escapeHTML(err)}</p></div>`);
  }
  return sections.join('');
}
function humanDecisionPanelHTML(runId,nodeId,nr,node,nodes){
  if(cachedRun(runId)?.status==='queued')return '';
  const field=isScriptWorkflowNode(node)?'':humanContextFieldHTML();
  return `<div class="human-decision" data-human-context-root><div class="human-gate-brief">${humanGateSectionsHTML(nr)}</div>${field}<div class="ap-buttons">${waitingActionButtons(runId,nodeId,nr,node,nodes)}</div></div>`;
}
function failedNodeId(run){
  const nodes=run?.workflowSnapshot?.nodes||[];
  for(const n of nodes){if(run?.nodes?.[n.id]?.status==='failed')return n.id}
  for(const [id,nr] of Object.entries(run?.nodes||{})){if(nr?.status==='failed')return id}
  return '';
}
function isScriptWorkflowNode(node){return node?.type==='script'||node?.type==='command'}
function cachedRun(runId){
  const id=String(runId||'');
  const candidates=[state.activeRun,state.issueDetailData?.run,(state.data.runs||[]).find(r=>r.id===id)].filter(r=>r?.id===id);
  return candidates.find(r=>r?.workflowSnapshot?.nodes?.length)||candidates[0]||null;
}
function failedNodeDef(run){
  const id=failedNodeId(run);
  if(!id)return null;
  return (run?.workflowSnapshot?.nodes||[]).find(n=>n.id===id)||(state.data.workflows||[]).find(w=>w.id===run?.workflowId)?.nodes?.find(n=>n.id===id)||null;
}
function runRetryNeedsContext(run){
  if(!run||run.status==='cancelled')return false;
  const node=failedNodeDef(run);
  if(node)return !isScriptWorkflowNode(node);
  const nodes=(run?.workflowSnapshot?.nodes||[]).length?(run.workflowSnapshot.nodes):((state.data.workflows||[]).find(w=>w.id===run?.workflowId)?.nodes||[]);
  if(nodes.length&&nodes.every(isScriptWorkflowNode))return false;
  return true;
}
function canManuallyPassFailedNode(run){return run?.status==='failed'&&isScriptWorkflowNode(failedNodeDef(run))}
function failedResumePanelHTML(run){
  const runId=run?.id||run;
  const node=run&&typeof run==='object'?failedNodeDef(run):null;
  const retry=isScriptWorkflowNode(node)?'重试':'补充信息并重试';
  const field=isScriptWorkflowNode(node)?'':humanContextFieldHTML();
  return `<div class="human-decision" data-human-context-root>${field}<div class="ap-buttons"><button type="button" class="button primary small" data-resume-run="${escapeHTML(runId)}">${retry}失败节点</button></div></div>`;
}
function failedNodePassCalloutHTML(runId,node,nr){
  return failedNodeRecoveryCalloutHTML(runId,node,nr);
}
function failedNodeRetryCalloutHTML(runId,node,nr){
  return failedNodeRecoveryCalloutHTML(runId,node,nr);
}
function failedNodeRecoveryCalloutHTML(runId,node,nr){
  const nodeTitle=escapeHTML(node?.name||node?.id||'当前节点');
  const gate=nr?.gateResult;
  let sections=[];
  if(gate){
    const agentOwnsFailure=gate.decision==='fail'||gate.decision==='rework';
    const reason=agentOwnsFailure?(gate.reason||nr?.error||'未提供'):(nr?.error||'未提供');
    sections.push(`<div class="callout-section"><strong class="callout-label">失败原因</strong><p class="callout-text">${escapeHTML(reason)}</p></div>`);
    if(!agentOwnsFailure){
      const terminalNote=gate.decision==='pass'?'pass：Agent 认为任务已完成，但节点客观校验未通过':('Agent 提交了 '+escapeHTML(gate.decision||'未知终态')+'，节点随后失败');
      sections.push(`<div class="callout-section"><strong class="callout-label">Agent 终态</strong><p class="callout-text">${terminalNote}</p></div>`);
    }
    if(gate.summary){
      sections.push(`<div class="callout-section"><strong class="callout-label">摘要</strong><p class="callout-text">${escapeHTML(gate.summary)}</p></div>`);
    }
    if(gate.findings&&gate.findings.length){
      const items=gate.findings.map(f=>`<li><b>${escapeHTML(f.title||'失败项')}</b>${f.evidence?`<p>${escapeHTML(f.evidence)}</p>`:''}</li>`).join('');
      sections.push(`<div class="callout-section"><strong class="callout-label">失败证据</strong><ul class="callout-findings">${items}</ul></div>`);
    }
  }else if(nr?.error){
    sections.push(`<div class="callout-section"><strong class="callout-label">失败信息</strong><pre class="callout-error-pre">${escapeHTML(nr.error)}</pre></div>`);
  }
  const isScript=isScriptWorkflowNode(node);
  const hint=isScript
    ? '重试会重新执行本步骤。如果硬脚本会一直卡住，也可以手动通过并继续下游。'
    : '补充信息后只会重跑当前节点及其下游；已成功的上游步骤不会重跑。';
  sections.push(`<p class="callout-hint">${hint}</p>`);
  const field=isScript?'':humanContextFieldHTML();
  const buttons=isScript
    ? `<button type="button" class="button primary" data-resume-run="${escapeHTML(runId)}">重试</button><button type="button" class="button ghost" data-pass-node="${escapeHTML(runId)}" data-node="${escapeHTML(node?.id||'')}">手动通过，继续下游</button>`
    : `<button type="button" class="button primary" data-resume-run="${escapeHTML(runId)}">补充信息，重跑当前节点</button>${canManuallyPassFailedNode(cachedRun(runId))?`<button type="button" class="button ghost" data-pass-node="${escapeHTML(runId)}" data-node="${escapeHTML(node?.id||'')}">手动通过，继续下游</button>`:''}`;

  return `<div class="approval-callout failed" data-human-context-root><div class="callout-head"><div class="callout-title-row"><span class="callout-badge failed">执行异常</span><b class="callout-title">节点已失败</b><span class="callout-node-name">步骤：<b>${nodeTitle}</b></span></div></div><div class="callout-content">${sections.join('')}</div>${field}<div class="ap-buttons">${buttons}</div></div>`;
}
async function passFailedNode(runId,nodeId,fromEl){
  if(!runId||!nodeId)return toast('没有可通过的失败节点','err');
  if(queuedRunActionBlocked(runId))return;
  const issue=(state.data.issues||[]).find(i=>i.id===issueIdForRun(runId))||state.issueDetailData?.issue;
  const blocked=issueExecutionBlocked(issue);if(blocked)return toast(blocked,'err');
  // /api/runs (list) responses are summaries with an empty workflowSnapshot
  // (see publicRun(run, summary=true) server-side), and failed runs never
  // start the live SSE stream that would otherwise backfill state.data.runs
  // with the full snapshot. So prefer whichever cached copy of this run
  // actually carries workflowSnapshot.nodes instead of trusting list order.
  const run=cachedRun(runId);
  const node=run?.workflowSnapshot?.nodes?.find(n=>n.id===nodeId);
  if(!isScriptWorkflowNode(node))return toast('只有失败的脚本节点可以手动通过，AI 节点请补充信息后重试','err');
  if(!await afConfirm({title:'手动通过确认',message:`确定将「${node?.name||nodeId}」视为通过并继续下游？该节点不会重新执行。`,confirmText:'确认通过'}))return;
  try{
    await api(`/api/runs/${encodeURIComponent(runId)}/pass`,{method:'POST',body:JSON.stringify(withHumanContext({nodeId,context:readHumanContext(fromEl)}))});
    toast('已手动通过，流程继续','ok');
    startLiveRun(runId);
    Promise.all([refreshIssues(),refreshRuns(true)]).catch(()=>{});
    renderApprovals(true);
    if(state.drawerOpen&&state.issueDetailData?.issue)openIssueDetail(state.issueDetailData.issue.id,'sop');
  }catch(e){toast(e.message,'err')}
}
function restoredSOPResumeNode(run){
  if(!run?.pauseRequested||run.workflowSnapshot?.collaborationMode==='group')return null;
  const event=[...(run.events||[])].reverse().find(e=>e.type==='group.sop_restored');
  const nr=run.nodes?.[event?.nodeId];
  if(nr?.status!=='pending'||nr.activity!=='切回 SOP，等待继续')return null;
  return run.workflowSnapshot?.nodes?.find(n=>n.id===event.nodeId)||null;
}
function pausedWorkflowNodes(run){
  const current=run?.workflowSnapshot?.nodes?.length?run.workflowSnapshot:((state.data.workflows||[]).find(w=>w.id===run?.workflowId)||state.workflow||run?.workflowSnapshot||{});
  const nodes=current.nodes||[];
  let paused=nodes.filter(node=>{const nr=run?.nodes?.[node.id];return nr?.status==='waiting_approval'&&String(nr.activity||'').includes('用户暂停')});
  const restored=restoredSOPResumeNode(run);
  if(!paused.length&&restored)paused=[restored];
  if(!paused.length&&run?.pauseRequested){
    paused=nodes.filter(node=>run?.nodes?.[node.id]?.status==='waiting_approval');
  }
  return paused;
}
function isRunPausedForAgent(run){
  return pausedWorkflowNodes(run).some(node=>!isScriptWorkflowNode(node));
}
function refreshRunSOPButtonHTML(run){
  if(!run||!['waiting','failed','cancelled','succeeded'].includes(run.status)||
    Object.values(run.nodes||{}).some(n=>['running','pending_input'].includes(n?.status)))return '';
  const latest=(state.data.workflows||[]).find(w=>w.id===run.workflowId);
  if(!latest)return '';
  const snapshot=run.workflowSnapshot?.id?run.workflowSnapshot:cachedRun(run.id)?.workflowSnapshot;
  const current=snapshot?.id?snapshot:null;
  if(!current)return '';
  const same=
    (Number(latest.version||0)===Number(current.version||0)&&Number(latest.revision||0)===Number(current.revision||0));
  if(same)return '';
  const version=`v${latest.version||1} #${latest.revision||0}`;
  return `<button class="button ghost small" data-refresh-run-sop="${escapeHTML(run.id)}">更新为最新 SOP（${escapeHTML(version)}）</button>`;
}
function pausedRunPanelHTML(run){
  const paused=pausedWorkflowNodes(run);
  const agentPaused=paused.some(node=>!isScriptWorkflowNode(node));
  const scriptPaused=paused.some(node=>isScriptWorkflowNode(node));
  const pausedScriptNode=paused.find(node=>isScriptWorkflowNode(node));
  const restored=restoredSOPResumeNode(run);
  const detail=restored
    ? `将从「${escapeHTML(restored.name||restored.id)}」重新启动 SOP，保留已完成的上游结果。已有文件和平台变更不会回退，后续节点将重新检查当前工作区。`
    : agentPaused
    ? `Agent 将优先接续原有 CLI 对话；若暂停前尚未建立会话，则在原工作区重新启动${scriptPaused?'；脚本会保留工作区并从头重新执行':''}。补充信息只会提供给 Agent。`
    : '脚本会保留当前工作区并从头重新执行；不会恢复已终止的进程，也不需要补充信息。如果脚本卡在重跑也解决不了的问题上，也可以手动通过并继续下游。';
  const passButton=pausedScriptNode&&!restored?`<button class="button ghost" data-pass-node="${escapeHTML(run.id)}" data-node="${escapeHTML(pausedScriptNode.id)}" title="将该脚本节点视为通过并继续下游，不重新执行">手动通过，跳过该脚本</button>`:'';
  return `<div class="approval-callout paused-run-callout" data-human-context-root><div class="callout-head"><div class="callout-title-row"><span class="callout-badge human">运行已暂停</span><b class="callout-title">流程已暂停执行</b></div></div><div class="callout-content"><p class="callout-text">${detail}</p></div>${agentPaused?humanContextFieldHTML():''}<div class="ap-buttons"><button class="button primary" data-resume-paused="${escapeHTML(run.id)}">直接继续</button>${passButton}${refreshRunSOPButtonHTML(run)}</div></div>`;
}
function approvalCalloutHTML(node,nr,nodes){
  nodes=nodes||state.activeRun?.workflowSnapshot?.nodes||[];
  const field=isScriptWorkflowNode(node)?'':humanContextFieldHTML();
  const nodeTitle=escapeHTML(node?.name||node?.id||'当前节点');
  const gate=nr?.gateResult;
  const target=reworkTarget(node,nodes);
  const rework=turnsExhausted(nr)?'':(target?`<button class="button ghost" data-reject="${node.id}" data-target="${escapeHTML(target.id)}" data-target-name="${escapeHTML(target.name)}">${lfT('退回上游「'+escapeHTML(target.name)+'」并重跑后续','Send back to “'+escapeHTML(target.name)+'” and rerun downstream')}</button>`:'');
  const buttons=turnsExhausted(nr)
    ? `<button class="button primary" data-retry-node="${node.id}">重试本节点</button><button class="button ghost" data-decline-retry="${node.id}">不重试</button>`
    : `<button class="button primary" data-approve="${node.id}">确认通过，继续下游</button><button class="button ghost" data-retry-node="${node.id}">补充信息，重跑当前节点</button>${rework}`;
  const title=turnsExhausted(nr)?'对话轮次已用尽':(gate?.decision==='human'?'等待人工确认':'需要人工判断');
  return `<div class="approval-callout human" data-human-context-root><div class="callout-head"><div class="callout-title-row"><span class="callout-badge human">待确认</span><b class="callout-title">${title}</b><span class="callout-node-name">步骤：<b>${nodeTitle}</b></span></div></div><div class="callout-content">${humanGateSectionsHTML(nr)}</div>${field}<div class="ap-buttons">${buttons}</div></div>`;
}
async function retryWaitingNode(runId,nodeId,fromEl){
  if(queuedRunActionBlocked(runId))return;
  const issue=(state.data.issues||[]).find(i=>i.id===issueIdForRun(runId))||state.issueDetailData?.issue;
  const blocked=issueExecutionBlocked(issue);if(blocked)return toast(blocked,'err');
  const nr=(state.data.runs||[]).find(r=>r.id===runId)?.nodes?.[nodeId]||state.issueDetailData?.run?.nodes?.[nodeId];
  const reason=turnsExhausted(nr)?'对话轮次用尽后人工确认重试':'人工确认重试本节点';
  const context=readHumanContext(fromEl);
  try{await api(`/api/runs/${runId}/retry`,{method:'POST',body:JSON.stringify(withHumanContext({nodeId,reason,context}))});const run=await syncRunControlState(runId);toast(runControlToast(run,'已确认重试本节点'),'ok');renderApprovals(true);if(state.drawerOpen&&state.issueDetailData?.issue)openIssueDetail(state.issueDetailData.issue.id,'sop')}catch(e){toast(e.message,'err')}
}
async function declineRetry(runId,nodeId){if(queuedRunActionBlocked(runId))return;if(!await afConfirm({title:'放弃重试确认',message:'确定不重试？本节点将失败，整条 SOP 停止。',confirmText:'确认不重试',isDanger:true}))return;try{await api(`/api/runs/${runId}/decline-retry`,{method:'POST',body:JSON.stringify({nodeId})});toast('已选择不重试，节点失败','ok');renderApprovals(true);if(state.drawerOpen&&state.issueDetailData?.issue)openIssueDetail(state.issueDetailData.issue.id,'sop')}catch(e){toast(e.message,'err')}}
async function pauseRun(runId){
  if(queuedRunActionBlocked(runId))return;
  if(!await afConfirm({title:'暂停运行确认',message:'确定暂停当前运行？正在执行的节点会安全中断，工作区会保留，稍后可补充信息后继续。',confirmText:'确认暂停'}))return;
  try{
    await api(`/api/runs/${encodeURIComponent(runId)}/pause`,{method:'POST'});
    patchCachedRun(runId,{pauseRequested:true});
    toast('已暂停','ok');
    startLiveRun(runId);
    paintRunControl(runId);
    Promise.all([refreshIssues(),refreshRuns(true)]).catch(()=>{});
  }catch(e){toast(e.message,'err')}
}
async function resumePausedRun(runId,fromEl){
  if(queuedRunActionBlocked(runId))return;
  const context=readHumanContext(fromEl);
  try{
    await api(`/api/runs/${encodeURIComponent(runId)}/resume`,{method:'POST',body:JSON.stringify({context})});
    const run=await syncRunControlState(runId);
    toast(runControlToast(run,'已继续运行'),'ok');
    Promise.all([refreshIssues(),refreshRuns(true)]).catch(()=>{});
    if(state.drawerOpen&&state.issueDetailData?.issue)openIssueDetail(state.issueDetailData.issue.id,'sop');
  }catch(e){toast(e.message,'err')}
}
function patchCachedRun(runId,patch){
  if(!runId||!patch)return;
  if(state.activeRun?.id===runId)state.activeRun={...state.activeRun,...patch};
  if(state.issueDetailData?.run?.id===runId)state.issueDetailData.run={...state.issueDetailData.run,...patch};
  const i=(state.data.runs||[]).findIndex(r=>r.id===runId);
  if(i>=0)state.data.runs[i]={...state.data.runs[i],...patch};
}
function paintRunControl(runId){
  const run=state.activeRun?.id===runId?state.activeRun:(state.data.runs||[]).find(r=>r.id===runId);
  if(run&&state.selectedRun===runId)renderRunDetail(run);
  renderRuns();
  if(state.drawerOpen&&state.issueDetailData?.run?.id===runId)renderIssueDetailTab();
}
function bindPausedResumeActions(root){
  root.querySelectorAll('[data-resume-paused]').forEach(button=>{
    const field=button.closest('[data-human-context-root]')?.querySelector('[data-human-context]');
    const update=()=>{button.textContent=String(field?.value||'').trim()?'补充信息并继续':'直接继续'};
    field?.addEventListener('input',update);update();
    button.onclick=()=>resumePausedRun(button.dataset.resumePaused,button);
  });
}
async function refreshRunSOP(runId){
  let current;
  try{current=await api(`/api/runs/${encodeURIComponent(runId)}`,{silent:true})}catch(e){return toast(e.message,'err')}
  const latest=(state.data.workflows||[]).find(w=>w.id===current?.workflowId);
  if(!latest)return toast('未找到最新 SOP','err');
  if(Number(latest.version||0)===Number(current.workflowSnapshot?.version||0)&&Number(latest.revision||0)===Number(current.workflowSnapshot?.revision||0))return toast('当前运行已使用最新 SOP');
  if(!await afConfirm({title:'更新 SOP 确认',message:'确定把当前运行更新为最新 SOP？已完成的同名节点和产物会保留，新节点会加入待执行；当前运行环境保持不变。',confirmText:'确认更新'}))return;
  try{
    await api(`/api/runs/${encodeURIComponent(runId)}/refresh-sop`,{method:'POST'});
    const [run]=await Promise.all([api(`/api/runs/${encodeURIComponent(runId)}`),refreshIssues(),refreshRuns(true)]);
    if(run?.id){
      const index=(state.data.runs||[]).findIndex(r=>r.id===run.id);
      if(index>=0)state.data.runs[index]=run;
      if(state.selectedRun===runId){state.activeRun=run;renderRunDetail(run)}
    }
    if($('#approvalsView')?.classList.contains('active'))await renderApprovals(true);
    if(state.drawerOpen&&state.issueDetailData?.issue)openIssueDetail(state.issueDetailData.issue.id,'sop');
    toast('已更新为最新 SOP','ok');
  }catch(e){toast(e.message,'err')}
}
function bindWaitingActions(root){
  if(!root)return;
  root.querySelectorAll('[data-approve]').forEach(b=>b.onclick=()=>approveRun(b.dataset.approve,b.dataset.node,b));
  root.querySelectorAll('[data-reject]').forEach(b=>b.onclick=()=>rejectRun(b.dataset.reject,b.dataset.node,b));
  root.querySelectorAll('[data-retry-node]').forEach(b=>b.onclick=()=>retryWaitingNode(b.dataset.retryNode,b.dataset.node,b));
  root.querySelectorAll('[data-pass-node]').forEach(b=>b.onclick=()=>passFailedNode(b.dataset.passNode,b.dataset.node,b));
  root.querySelectorAll('[data-decline-retry]').forEach(b=>b.onclick=()=>declineRetry(b.dataset.declineRetry,b.dataset.node));
  bindPausedResumeActions(root);
  root.querySelectorAll('[data-refresh-run-sop]').forEach(b=>b.onclick=()=>refreshRunSOP(b.dataset.refreshRunSop));
}
function approvalItems(){
  const waiting=scopedRuns((state.data.runs||[]).filter(r=>{
    if(r.status!=='waiting')return false;
    // 基线对齐不再等人确认：不进「待我确认」，由引擎自动合入。
    return r.workflowId!=='scheduled-baseline-align'&&r.workflowSnapshot?.id!=='scheduled-baseline-align';
  }));
  const uid=state.currentUser&&!state.currentUser.isGuest?Number(state.currentUser.id):0;
  // 登录用户只看分派给自己的流程；responsibleUserId 为 0 的历史/无主流程对所有人可见，
  // 避免升级后旧数据被藏没（可用新增的「取消流程」清理）。
  if(!uid)return waiting;
  return waiting.filter(r=>{const rid=Number(r.executionContext?.responsibleUserId||0);return rid===0||rid===uid});
}
async function cancelRunFlow(runId){
  if(!await afConfirm({title:'取消流程确认',message:'确定取消该流程？取消后将不再出现在待我确认列表，对应需求可重新派发。',confirmText:'确认取消',isDanger:true}))return;
  try{await api(`/api/runs/${encodeURIComponent(runId)}/cancel`,{method:'POST'});toast('流程已取消','ok');renderApprovals(true)}catch(e){toast(e.message,'err')}
}
async function approveRun(runId,nodeId,fromEl){if(queuedRunActionBlocked(runId))return;try{await api(`/api/runs/${runId}/approve`,{method:'POST',body:JSON.stringify(withHumanContext({nodeId,context:readHumanContext(fromEl)}))});toast('已批准，流程继续','ok');renderApprovals(true);if(state.drawerOpen&&state.issueDetailData?.issue)openIssueDetail(state.issueDetailData.issue.id,'sop')}catch(e){toast(e.message,'err')}}
async function rejectRun(runId,nodeId,fromEl){if(queuedRunActionBlocked(runId))return;const target=String(fromEl?.dataset?.target||'').trim(),targetName=String(fromEl?.dataset?.targetName||target).trim();if(!target)return toast('当前节点没有配置上游返工目标','err');const reason=await afPrompt({title:`退回上游「${targetName}」重做`,message:`将重置「${targetName}」及其全部下游节点。请输入返工原因：`,defaultValue:'验证未通过，请上游修复后重新提交'});if(reason===null||reason===undefined)return;try{await api(`/api/runs/${runId}/reject`,{method:'POST',body:JSON.stringify(withHumanContext({nodeId,targetId:target,reason,context:readHumanContext(fromEl)}))});toast(`已退回「${targetName}」，开始重跑后续流程`,'ok');renderApprovals(true);if(state.drawerOpen&&state.issueDetailData?.issue)openIssueDetail(state.issueDetailData.issue.id,'sop')}catch(e){toast(e.message,'err')}}
async function renderApprovals(background=false){
  if(!background){try{const [runs,tasks,issues]=await Promise.all([api('/api/runs'),api('/api/tasks'),api('/api/issues')]);state.data.runs=runs;state.data.tasks=tasks;state.data.issues=issues}catch(e){toast(e.message,'err')}}
  const items=(await Promise.all(approvalItems().map(async summary=>{
    const cached=cachedRun(summary.id);
    if(cached?.workflowSnapshot?.id)return {...cached,...summary,workflowSnapshot:cached.workflowSnapshot};
    try{
      const full=await api(`/api/runs/${encodeURIComponent(summary.id)}`,{silent:true});
      if(full?.id){
        const index=(state.data.runs||[]).findIndex(r=>r.id===full.id);
        if(index>=0)state.data.runs[index]=full;
        return full;
      }
    }catch(e){}
    return summary;
  }))).filter(r=>r.status==='waiting');
  $('#approvalLiveState').classList.toggle('active',items.length>0);
  const updated=$('#approvalLiveState [data-updated]');if(updated)updated.textContent=`更新于 ${new Date().toLocaleTimeString('zh-CN',{hour:'2-digit',minute:'2-digit'})}`;
  updateApprovalBadge();
  $('#approvalStats').innerHTML=`<span class="issue-stat">${items.length} 条待确认</span>`;
  const host=$('#approvalList');
  host.innerHTML=items.map(r=>{
    const nodeId=waitingNodeId(r),node=r.workflowSnapshot?.nodes?.find(n=>n.id===nodeId);
    const task=(state.data.tasks||[]).find(t=>t.runId===r.id),issue=task&&(state.data.issues||[]).find(i=>i.id===task.issueId);
    const alignRun=r.workflowId==='scheduled-baseline-align'||r.workflowSnapshot?.id==='scheduled-baseline-align';
    const repoName=r.repository?.name;
    const title=alignRun?(repoName?`${repoName} · 确认对齐基线`:'定时任务 · 确认对齐基线'):issue?.title||r.workflowSnapshot?.name||r.workflowId;
    const key=alignRun?'定时任务基线':issue?`${(state.data.projects||[]).find(p=>p.id===issue.projectId)?.key||'ISS'}-${issue.number}`:'未关联需求';
    const sideButton=issue?`<button class="button ghost small" data-open-issue="${escapeHTML(issue.id)}">打开需求</button>`:`<button class="button ghost small" data-open-logs="${escapeHTML(r.id)}">${alignRun?'查看对齐详情':'排查日志'}</button>`;
    const decision=r.pauseRequested?pausedRunPanelHTML(r):humanDecisionPanelHTML(r.id,nodeId,r.nodes?.[nodeId],node,r.workflowSnapshot?.nodes||[]);
    return `<div class="ap-card attention"><div class="ap-main"><div class="ap-head"><b>${escapeHTML(title)}</b><span class="status ${r.pauseRequested?'paused':'waiting'}">${r.pauseRequested?'已暂停':'待确认'}</span></div><div class="ap-meta">${escapeHTML(key)} ｜ ${r.pauseRequested?'暂停于':'卡在'}「${escapeHTML(node?.name||nodeId||'确认')}」｜ 已等待 ${relativeTime(r.createdAt)}</div></div><div class="ap-actions">${decision}${!r.pauseRequested?refreshRunSOPButtonHTML(r):''}${sideButton}<button class="button ghost small danger" data-cancel-run="${escapeHTML(r.id)}">取消流程</button></div></div>`;
  }).join('')||'<div class="approval-empty"><div class="empty-box">没有需要你拍板的需求</div></div>';
  host.querySelectorAll('[data-open-issue]').forEach(b=>b.onclick=()=>openIssueDetail(b.dataset.openIssue,'sop'));
  host.querySelectorAll('[data-open-logs]').forEach(b=>b.onclick=()=>openRunFromId(b.dataset.openLogs,{forceRuns:true}));
  host.querySelectorAll('[data-cancel-run]').forEach(b=>b.onclick=()=>cancelRunFlow(b.dataset.cancelRun));
  bindWaitingActions(host);
}
