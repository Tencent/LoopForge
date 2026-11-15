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
  if(nr?.waitReason==='model_credentials')return `<button class="button primary small" data-retry-node="${escapeHTML(runId)}" data-node="${escapeHTML(nodeId)}">处理完成，重新执行</button>`;
  if(turnsExhausted(nr))return `<button class="button primary small" data-retry-node="${escapeHTML(runId)}" data-node="${escapeHTML(nodeId)}">重试本节点</button><button class="button danger-outline small" data-decline-retry="${escapeHTML(runId)}" data-node="${escapeHTML(nodeId)}">不重试</button>`;
  const target=reworkTarget(node,nodes);
  const rework=target?`<button class="button ghost small" data-reject="${escapeHTML(runId)}" data-node="${escapeHTML(nodeId)}" data-target="${escapeHTML(target.id)}" data-target-name="${escapeHTML(target.name)}">${lfT('退回上游「'+escapeHTML(target.name)+'」并重跑后续','Send back to “'+escapeHTML(target.name)+'” and rerun downstream')}</button>`:'';
  return `<button class="button primary small" data-approve="${escapeHTML(runId)}" data-node="${escapeHTML(nodeId)}">确认通过，继续下游</button><button class="button ghost small" data-retry-node="${escapeHTML(runId)}" data-node="${escapeHTML(nodeId)}">补充信息，重跑当前节点</button>${rework}`;
}
// approvalWaitReason 说明人工门禁为什么停下：哪些上一步已完成待检查、确认后继续哪一步、
// 不通过打回到哪一步，以及节点配置的「确认要求」。与 internal/domain/approval_reason.go、
// chatbot/src/runs.js 同规则同文案。
function approvalWaitReason(run,nodeId){
  const wf=run?.workflowSnapshot||{},nodes=wf.nodes||[],edges=wf.edges||[];
  const index=nodes.findIndex(n=>n.id===nodeId);
  if(index<0)return '';
  const node=nodes[index],name=id=>`「${nodes.find(n=>n.id===id)?.name||id}」`;
  let up=edges.filter(e=>e.target===nodeId&&e.source).map(e=>name(e.source));
  let down=edges.filter(e=>e.source===nodeId&&e.target).map(e=>name(e.target));
  if(!edges.length){up=index>0?[name(nodes[index-1].id)]:[];down=index+1<nodes.length?[name(nodes[index+1].id)]:[]}
  const sop=String(wf.name||'').trim()?`SOP「${String(wf.name).trim()}」`:'该 SOP';
  let text=`${sop}在这一步设置了人工门禁`;
  text+=up.length?`：${up.join('、')}已完成，需要有权限的成员检查结果后确认`:'，需要有权限的成员确认';
  text+=down.length?`，确认后继续${down.join('、')}`:'，确认后运行结束';
  if(node.rejectTo)text+=`；不通过可打回到${name(node.rejectTo)}重做`;
  text+='。';
  const instructions=String(node.taskPrompt||'').trim();
  if(instructions)text+=`确认要求：${instructions}`;
  return text;
}
function humanGateSectionsHTML(nr,run,node){
  if(nr?.waitReason==='model_credentials')return `<div class="callout-section"><strong class="callout-label">需要处理</strong><p class="callout-text">${escapeHTML(nr.activity||'请更新模型凭据或额度后重新执行。')}</p></div>`;
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
  if(!sections.length&&node?.type==='approval'&&!nr?.error&&!nr?.feedback){
    const why=approvalWaitReason(run,node.id);
    if(why)sections.push(`<div class="callout-section"><strong class="callout-label">为什么需要人工确认</strong><p class="callout-text">${escapeHTML(why)}</p></div>`);
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
  return `<div class="human-decision" data-human-context-root><div class="human-gate-brief">${humanGateSectionsHTML(nr,cachedRun(runId),node)}</div>${field}<div class="ap-buttons">${waitingActionButtons(runId,nodeId,nr,node,nodes)}</div></div>`;
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
    sections.push(`<div class="callout-section"><strong class="callout-label">失败原因</strong><p class="callout-text">${escapeHTML(reason)}</p>${credentialActionHTML(reason)}</div>`);
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
    sections.push(`<div class="callout-section"><strong class="callout-label">失败信息</strong><pre class="callout-error-pre">${escapeHTML(nr.error)}</pre>${credentialActionHTML(nr.error)}</div>`);
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
  const idlePause=!paused.length&&!restored;
  const detail=restored
    ? `将从「${escapeHTML(restored.name||restored.id)}」重新启动 SOP，保留已完成的上游结果。已有文件和平台变更不会回退，后续节点将重新检查当前工作区。`
    : idlePause
    ? '当前没有被打断的步骤。继续后会接着执行尚未完成的节点；如果流程已经走完，运行会正常结束。'
    : agentPaused
    ? `Agent 将优先接续原有 CLI 对话；若暂停前尚未建立会话，则在原工作区重新启动${scriptPaused?'；脚本会保留工作区并从头重新执行':''}。补充信息只会提供给 Agent。`
    : '脚本会保留当前工作区并从头重新执行；不会恢复已终止的进程，也不需要补充信息。如果脚本卡在重跑也解决不了的问题上，也可以手动通过并继续下游。';
  const passButton=pausedScriptNode&&!restored?`<button class="button ghost" data-pass-node="${escapeHTML(run.id)}" data-node="${escapeHTML(pausedScriptNode.id)}" title="将该脚本节点视为通过并继续下游，不重新执行">手动通过，跳过该脚本</button>`:'';
  const resumeLabel='直接继续';
  return `<div class="approval-callout paused-run-callout" data-human-context-root><div class="callout-head"><div class="callout-title-row"><span class="callout-badge human">运行已暂停</span><b class="callout-title">流程已暂停执行</b></div></div><div class="callout-content"><p class="callout-text">${detail}</p></div>${agentPaused?humanContextFieldHTML():''}<div class="ap-buttons"><button class="button primary" data-resume-paused="${escapeHTML(run.id)}">${resumeLabel}</button>${passButton}${refreshRunSOPButtonHTML(run)}</div></div>`;
}
function approvalCalloutHTML(node,nr,nodes){
  nodes=nodes||state.activeRun?.workflowSnapshot?.nodes||[];
  const field=isScriptWorkflowNode(node)?'':humanContextFieldHTML();
  const nodeTitle=escapeHTML(node?.name||node?.id||'当前节点');
  if(nr?.waitReason==='model_credentials')return `<div class="approval-callout human"><div class="callout-head"><div class="callout-title-row"><span class="callout-badge human">待处理</span><b class="callout-title">模型服务需要处理</b><span class="callout-node-name">步骤：<b>${nodeTitle}</b></span></div></div><div class="callout-content">${humanGateSectionsHTML(nr)}</div><div class="ap-buttons"><button class="button primary" data-retry-node="${escapeHTML(node.id)}">处理完成，重新执行</button></div></div>`;
  const gate=nr?.gateResult;
  const target=reworkTarget(node,nodes);
  const rework=turnsExhausted(nr)?'':(target?`<button class="button ghost" data-reject="${node.id}" data-target="${escapeHTML(target.id)}" data-target-name="${escapeHTML(target.name)}">${lfT('退回上游「'+escapeHTML(target.name)+'」并重跑后续','Send back to “'+escapeHTML(target.name)+'” and rerun downstream')}</button>`:'');
  const buttons=turnsExhausted(nr)
    ? `<button class="button primary" data-retry-node="${node.id}">重试本节点</button><button class="button ghost" data-decline-retry="${node.id}">不重试</button>`
    : `<button class="button primary" data-approve="${node.id}">确认通过，继续下游</button><button class="button ghost" data-retry-node="${node.id}">补充信息，重跑当前节点</button>${rework}`;
  const title=turnsExhausted(nr)?'对话轮次已用尽':(gate?.decision==='human'?'等待人工确认':'需要人工判断');
  return `<div class="approval-callout human" data-human-context-root><div class="callout-head"><div class="callout-title-row"><span class="callout-badge human">待确认</span><b class="callout-title">${title}</b><span class="callout-node-name">步骤：<b>${nodeTitle}</b></span></div></div><div class="callout-content">${humanGateSectionsHTML(nr,typeof state!=='undefined'?state.activeRun:null,node)}</div>${field}<div class="ap-buttons">${buttons}</div></div>`;
}
async function retryWaitingNode(runId,nodeId,fromEl){
  if(queuedRunActionBlocked(runId))return;
  const issue=(state.data.issues||[]).find(i=>i.id===issueIdForRun(runId))||state.issueDetailData?.issue;
  const blocked=issueExecutionBlocked(issue);if(blocked)return toast(blocked,'err');
  const nr=(state.data.runs||[]).find(r=>r.id===runId)?.nodes?.[nodeId]||state.issueDetailData?.run?.nodes?.[nodeId];
  const reason=turnsExhausted(nr)?'对话轮次用尽后人工确认重试':'人工确认重试本节点';
  const context=readHumanContext(fromEl);
  try{await requestRunActivation(runId,'retry',withHumanContext({nodeId,reason,context}));const run=await syncRunControlState(runId);toast(runControlToast(run,'已确认重试本节点'),'ok');renderApprovals(true);if(state.drawerOpen&&state.issueDetailData?.issue)openIssueDetail(state.issueDetailData.issue.id,'sop')}catch(e){toast(e.message,'err')}
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
    await requestRunActivation(runId,'resume',{context});
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
    // Workspaces and retired groups never request SOP approval.
    if(r.kind==='workspace'||r.workflowSnapshot?.collaborationMode==='group')return false;
    // 基线对齐不再等人确认：不进「待我确认」，由引擎自动合入。
    return r.workflowId!=='scheduled-baseline-align'&&r.workflowSnapshot?.id!=='scheduled-baseline-align';
  }));
  const uid=state.currentUser&&!state.currentUser.isGuest?Number(state.currentUser.id):0;
  // 登录用户只看分派给自己的流程；responsibleUserId 为 0 的历史/无主流程对所有人可见，
  // 避免升级后旧数据被藏没（可用新增的「取消流程」清理）。
  if(!uid)return waiting;
  return waiting.filter(r=>{const rid=Number(r.executionContext?.responsibleUserId||0);return rid===0||rid===uid});
}
// /api/bootstrap 不带 runs，首屏单独拉一次，否则徽标要等进了「待我确认」才出现。
async function preloadApprovalBadge(){try{const runs=await api('/api/runs',{silent:true});if(Array.isArray(runs)){state.data.runs=runs;updateApprovalBadge()}}catch{}}
async function cancelRunFlow(runId){
  if(!await afConfirm({title:'取消流程确认',message:'确定取消该流程？取消后将不再出现在待我确认列表，对应需求可重新派发。',confirmText:'确认取消',isDanger:true}))return;
  try{await api(`/api/runs/${encodeURIComponent(runId)}/cancel`,{method:'POST'});toast('流程已取消','ok');renderApprovals(true)}catch(e){toast(e.message,'err')}
}
async function approveRun(runId,nodeId,fromEl){if(queuedRunActionBlocked(runId))return;try{await api(`/api/runs/${runId}/approve`,{method:'POST',body:JSON.stringify(withHumanContext({nodeId,context:readHumanContext(fromEl)}))});toast('已批准，流程继续','ok');await renderApprovals();if(state.drawerOpen&&state.issueDetailData?.issue)openIssueDetail(state.issueDetailData.issue.id,'sop')}catch(e){toast(e.message,'err')}}
async function rejectRun(runId,nodeId,fromEl){if(queuedRunActionBlocked(runId))return;const target=String(fromEl?.dataset?.target||'').trim(),targetName=String(fromEl?.dataset?.targetName||target).trim();if(!target)return toast('当前节点没有配置上游返工目标','err');const reason=await afPrompt({title:`退回上游「${targetName}」重做`,message:`将重置「${targetName}」及其全部下游节点。请输入返工原因：`,defaultValue:'验证未通过，请上游修复后重新提交'});if(reason===null||reason===undefined)return;try{await api(`/api/runs/${runId}/reject`,{method:'POST',body:JSON.stringify(withHumanContext({nodeId,targetId:target,reason,context:readHumanContext(fromEl)}))});toast(`已退回「${targetName}」，开始重跑后续流程`,'ok');renderApprovals(true);if(state.drawerOpen&&state.issueDetailData?.issue)openIssueDetail(state.issueDetailData.issue.id,'sop')}catch(e){toast(e.message,'err')}}
function approvalStatusBadgeHTML(paused){
  const [tone,icon,label]=paused?['neutral','pause','已暂停']:['warn','pending-review','待确认'];
  return `<span class="hui-badge" data-tone="${tone}" data-stage-icon="${icon}">${huiIcon(icon)}<span class="hui-badge-label">${label}</span></span>`;
}
// ---------- 批量处理 ----------
// 审批动作仍然是「每次运行 × 节点」一次判定；批量只是在页面上把同一组
// /approve、/reject 接口串行调用一遍，逐条收集结果，不合并、不跳过校验。
function approvalKey(runId,nodeId){return `${runId}::${nodeId||''}`}
function approvalSelection(){
  if(!(state.approvalSelection instanceof Set))state.approvalSelection=new Set();
  return state.approvalSelection;
}
function approvalNodeDef(r){
  const nodeId=waitingNodeId(r);
  if(!nodeId)return null;
  return (r?.workflowSnapshot?.nodes||[]).find(n=>n.id===nodeId)||null;
}
function approvalSelectable(r){
  if(!r||r.status!=='waiting'||r.pauseRequested)return false;
  const nodeId=waitingNodeId(r),nr=r.nodes?.[nodeId];
  return !!nodeId&&nr?.status==='waiting_approval'&&!turnsExhausted(nr);
}
function approvalSelectedRuns(items){
  const selection=approvalSelection();
  return (items||[]).filter(r=>approvalSelectable(r)&&selection.has(approvalKey(r.id,waitingNodeId(r))));
}
function pruneApprovalSelection(items){
  const selection=approvalSelection(),valid=new Set((items||[]).filter(approvalSelectable).map(r=>approvalKey(r.id,waitingNodeId(r))));
  for(const key of [...selection])if(!valid.has(key))selection.delete(key);
  if(valid.size<2)selection.clear();
  return selection;
}
function approvalBatchEnabled(){return (state.approvalItems||[]).filter(approvalSelectable).length>=2}
function approvalSelectBoxHTML(r){
  if(!approvalSelectable(r)||!approvalBatchEnabled())return '';
  const key=approvalKey(r.id,waitingNodeId(r)),checked=approvalSelection().has(key)?' checked':'';
  return `<label class="ap-batch-check" title="选中后可批量处理"><input type="checkbox" data-ap-select="${escapeHTML(r.id)}" data-ap-node="${escapeHTML(waitingNodeId(r))}"${checked} aria-label="选中这条待确认"></label>`;
}
function approvalBatchBarHTML(items){
  const selectable=(items||[]).filter(approvalSelectable);
  if(selectable.length<2)return '';
  const chosen=approvalSelectedRuns(items),all=chosen.length===selectable.length,disabled=chosen.length?'':' disabled';
  return `<label class="ap-batch-check ap-batch-all"><input type="checkbox" data-ap-select-all${all?' checked':''}>全选可审批的 ${selectable.length} 条</label><span class="ap-batch-count" data-ap-batch-count>已选 ${chosen.length} 条</span><span class="ap-batch-tip">逐条执行原有审批，结果会汇总</span><span class="ap-batch-actions"><button type="button" class="button ghost small" data-ap-batch-reject${disabled}>批量退回上游</button><button type="button" class="button primary small" data-ap-batch-approve${disabled}>批量通过</button></span>`;
}
function refreshApprovalBatchUI(){
  const items=state.approvalItems||[],chosen=approvalSelectedRuns(items),selectable=items.filter(approvalSelectable);
  document.querySelectorAll('#approvalList [data-ap-select]').forEach(box=>{
    const on=approvalSelection().has(approvalKey(box.dataset.apSelect,box.dataset.apNode));
    box.checked=on;box.closest('.ap-card')?.classList.toggle('is-selected',on);
  });
  const all=document.querySelector('#approvalBatch [data-ap-select-all]');
  if(all)all.checked=selectable.length>0&&chosen.length===selectable.length;
  const count=document.querySelector('#approvalBatch [data-ap-batch-count]');
  if(count)count.textContent=`已选 ${chosen.length} 条`;
  document.querySelectorAll('#approvalBatch [data-ap-batch-approve],#approvalBatch [data-ap-batch-reject]').forEach(b=>{b.disabled=!chosen.length});
}
async function batchApproveApprovals(){
  const items=approvalSelectedRuns(state.approvalItems||[]);
  if(!items.length)return toast('请先勾选要批量通过的条目','err');
  if(!await afConfirm({title:`批量通过 ${items.length} 条`,message:`将逐条批准这 ${items.length} 条运行当前的人工确认节点，批准后各自继续下游。批量通过不会携带卡片里填写的补充上下文。`,confirmText:`确认通过 ${items.length} 条`}))return;
  const passed=[],failed=[];
  for(const r of items){
    const nodeId=waitingNodeId(r);
    try{await api(`/api/runs/${encodeURIComponent(r.id)}/approve`,{method:'POST',body:JSON.stringify({nodeId})});passed.push(approvalKey(r.id,nodeId))}
    catch(e){failed.push(`${r.workflowSnapshot?.name||r.id}：${e.message}`)}
  }
  passed.forEach(key=>approvalSelection().delete(key));
  await renderApprovals(false);
  if(state.drawerOpen&&state.issueDetailData?.issue)openIssueDetail(state.issueDetailData.issue.id,'sop');
  if(!failed.length)return toast(`已批量通过 ${passed.length} 条`,'ok');
  toast(`已通过 ${passed.length} 条，${failed.length} 条失败：${failed.slice(0,3).join('；')}${failed.length>3?'…':''}`,'err');
}
async function batchRejectApprovals(){
  const items=approvalSelectedRuns(state.approvalItems||[]);
  if(!items.length)return toast('请先勾选要批量退回的条目','err');
  const nodesOf=r=>r.workflowSnapshot?.nodes||[];
  const withTarget=items.map(r=>({run:r,target:reworkTarget(approvalNodeDef(r),nodesOf(r))})).filter(x=>x.target);
  if(!withTarget.length)return toast('选中的运行都没有配置上游返工目标，只能逐条处理','err');
  const reason=await afPrompt({title:`批量退回 ${withTarget.length} 条`,message:`将把这些运行分别退回到各自配置的上游节点重做，并重置其下游；未配置返工目标的 ${items.length-withTarget.length} 条会跳过。请输入统一的返工原因：`,defaultValue:'验证未通过，请上游修复后重新提交',multiline:true,confirmText:'批量退回'});
  if(reason===null||reason===undefined||!String(reason).trim())return;
  const rejected=[],failed=[];
  for(const {run,target} of withTarget){
    const nodeId=waitingNodeId(run);
    try{await api(`/api/runs/${encodeURIComponent(run.id)}/reject`,{method:'POST',body:JSON.stringify({nodeId,targetId:target.id,reason:String(reason).trim()})});rejected.push(approvalKey(run.id,nodeId))}
    catch(e){failed.push(`${run.workflowSnapshot?.name||run.id}：${e.message}`)}
  }
  rejected.forEach(key=>approvalSelection().delete(key));
  await renderApprovals(false);
  if(state.drawerOpen&&state.issueDetailData?.issue)openIssueDetail(state.issueDetailData.issue.id,'sop');
  const skipped=items.length-withTarget.length;
  toast(`已退回 ${rejected.length} 条${skipped?`，跳过 ${skipped} 条（无返工目标）`:''}${failed.length?`，${failed.length} 条失败：${failed.slice(0,3).join('；')}`:''}`,failed.length?'err':'ok');
}
function bindApprovalBatch(){
  const host=$('#approvalBatch');if(!host)return;
  const selectAll=host.querySelector('[data-ap-select-all]');
  if(selectAll)selectAll.onchange=()=>{
    const selection=approvalSelection();
    (state.approvalItems||[]).filter(approvalSelectable).forEach(r=>{
      const key=approvalKey(r.id,waitingNodeId(r));
      if(selectAll.checked)selection.add(key);else selection.delete(key);
    });
    refreshApprovalBatchUI();
  };
  const approve=host.querySelector('[data-ap-batch-approve]');if(approve)approve.onclick=()=>batchApproveApprovals();
  const reject=host.querySelector('[data-ap-batch-reject]');if(reject)reject.onclick=()=>batchRejectApprovals();
}

// The decision / paused panels are shared with the issue drawer and run detail, so
// only the card shell changes here; #approvalsView CSS reorders their buttons.
function approvalCardHTML(r){
  const nodeId=waitingNodeId(r),node=r.workflowSnapshot?.nodes?.find(n=>n.id===nodeId);
  const task=(state.data.tasks||[]).find(t=>t.runId===r.id),issue=task&&(state.data.issues||[]).find(i=>i.id===task.issueId);
  const alignRun=r.workflowId==='scheduled-baseline-align'||r.workflowSnapshot?.id==='scheduled-baseline-align';
  const repoName=r.repository?.name;
  const title=alignRun?(repoName?`${repoName} · 确认对齐基线`:'定时任务 · 确认对齐基线'):issue?.title||r.workflowSnapshot?.name||r.workflowId;
  const key=alignRun?'定时任务基线':issue?`${(state.data.projects||[]).find(p=>p.id===issue.projectId)?.key||'ISS'}-${issue.number}`:'未关联需求';
  const keyHTML=issue&&!alignRun?`<code class="ap-key">${escapeHTML(key)}</code>`:`<span class="ap-key">${escapeHTML(key)}</span>`;
  const sep='<span class="ap-sep" aria-hidden="true">·</span>';
  const sideButton=issue?`<button class="button ghost small" data-open-issue="${escapeHTML(issue.id)}">打开需求</button>`:`<button class="button ghost small" data-open-logs="${escapeHTML(r.id)}">${alignRun?'查看对齐详情':'排查日志'}</button>`;
  const more=`<div class="ap-more"><button type="button" class="ap-more-toggle" data-ap-more title="更多操作" aria-label="更多操作" aria-haspopup="menu" aria-expanded="false">${huiIcon('more-horizontal')}</button><div class="ap-more-menu" role="menu" hidden><button type="button" class="ap-menu-item danger" role="menuitem" data-cancel-run="${escapeHTML(r.id)}">${huiIcon('close')}<span>取消流程</span></button></div></div>`;
  const decision=r.pauseRequested?pausedRunPanelHTML(r):humanDecisionPanelHTML(r.id,nodeId,r.nodes?.[nodeId],node,r.workflowSnapshot?.nodes||[]);
  return `<div class="ap-card${approvalSelectable(r)&&approvalSelection().has(approvalKey(r.id,waitingNodeId(r)))?' is-selected':''}"><div class="ap-card-head">${approvalSelectBoxHTML(r)}<div class="ap-main"><div class="ap-head"><b>${escapeHTML(title)}</b>${approvalStatusBadgeHTML(r.pauseRequested)}</div><div class="ap-meta">${keyHTML}${sep}${r.pauseRequested?'暂停于':'卡在'}「${escapeHTML(node?.name||nodeId||'确认')}」${sep}已等待 ${relativeTime(r.createdAt)}</div></div><div class="ap-side">${sideButton}${!r.pauseRequested?refreshRunSOPButtonHTML(r):''}${more}</div></div>${decision?`<div class="ap-body">${decision}</div>`:''}</div>`;
}
function closeApprovalMoreMenus(except){
  document.querySelectorAll('#approvalsView .ap-more.is-open').forEach(more=>{
    if(more===except)return;
    more.classList.remove('is-open');
    more.querySelector('.ap-more-menu')?.setAttribute('hidden','');
    more.querySelector('[data-ap-more]')?.setAttribute('aria-expanded','false');
  });
}
let approvalMoreMenusBound=false;
function bindApprovalMoreMenus(host){
  host.querySelectorAll('[data-ap-more]').forEach(toggle=>toggle.onclick=()=>{
    const more=toggle.closest('.ap-more'),open=!more.classList.contains('is-open');
    closeApprovalMoreMenus(more);
    more.classList.toggle('is-open',open);
    more.querySelector('.ap-more-menu').hidden=!open;
    toggle.setAttribute('aria-expanded',String(open));
  });
  if(approvalMoreMenusBound)return;
  approvalMoreMenusBound=true;
  document.addEventListener('pointerdown',e=>{if(!(e.target instanceof Element)||!e.target.closest('#approvalsView .ap-more'))closeApprovalMoreMenus()});
  document.addEventListener('keydown',e=>{
    if(e.key!=='Escape')return;
    const open=document.querySelector('#approvalsView .ap-more.is-open');
    if(!open)return;
    const hadFocus=open.contains(document.activeElement);
    closeApprovalMoreMenus();
    if(hadFocus)open.querySelector('[data-ap-more]')?.focus();
  });
  document.addEventListener('scroll',e=>{if(!(e.target instanceof Element&&e.target.closest('.ap-more-menu')))closeApprovalMoreMenus()},true);
}
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
  $('#approvalStats').innerHTML=`<span class="issue-stat"><b>${items.length}</b> 条待确认</span>`;
  state.approvalItems=items;pruneApprovalSelection(items);
  const batch=$('#approvalBatch');
  if(batch){batch.innerHTML=approvalBatchBarHTML(items);batch.hidden=!batch.innerHTML;bindApprovalBatch()}
  const host=$('#approvalList');
  host.innerHTML=items.map(approvalCardHTML).join('')||`<div class="approval-empty"><div class="empty-box"><span class="ap-empty-icon">${huiIcon('check')}</span><b>没有需要你拍板的需求</b><p>流程停在人工确认节点时会出现在这里。</p></div></div>`;
  host.querySelectorAll('[data-open-issue]').forEach(b=>b.onclick=()=>openIssueDetail(b.dataset.openIssue,'sop'));
  host.querySelectorAll('[data-open-logs]').forEach(b=>b.onclick=()=>openRunFromId(b.dataset.openLogs,{forceRuns:true}));
  host.querySelectorAll('[data-cancel-run]').forEach(b=>b.onclick=()=>{closeApprovalMoreMenus();cancelRunFlow(b.dataset.cancelRun)});
  bindApprovalMoreMenus(host);
  host.querySelectorAll('[data-ap-select]').forEach(box=>{box.onchange=()=>{const key=approvalKey(box.dataset.apSelect,box.dataset.apNode);if(box.checked)approvalSelection().add(key);else approvalSelection().delete(key);refreshApprovalBatchUI()}});
  bindWaitingActions(host);
}
