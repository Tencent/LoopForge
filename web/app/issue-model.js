function issueCanClose(issue){
  return issue?.closureAllowed===true&&!issueClarifying(issue.id);
}
function issueCanDelete(issue){
  return issue?.deletionAllowed===true&&!issueClarifying(issue.id);
}
const issueStatuses=[['backlog','Backlog'],['todo','Todo'],['in_progress','In progress'],['in_review','In review'],['done','Done'],['closed','Closed'],['blocked','Blocked'],['cancelled','Cancelled']];
function latestIssueTask(i){
  const tasks=(state.data.tasks||[]).filter(t=>t.issueId===i.id);
  if(tasks.length){
    tasks.sort((a,b)=>new Date(b.createdAt||0)-new Date(a.createdAt||0)||String(b.id).localeCompare(String(a.id)));
    return tasks[0];
  }
  return (state.data.tasks||[]).find(x=>x.id===i.latestTaskId)||null;
}
function latestIssueRun(i, task){
  const runs=state.data.runs||[];
  if(task){
    const hit=runs.find(x=>x.id===task.runId);
    if(hit)return hit;
  }
  // issueNumber is not unique: Gongfeng uses IID, TAPD uses per-project local numbers.
  // Matching on number steals another issue's succeeded run (e.g. gf #23 vs TAPD #23).
  const linked=runs.filter(r=>r.executionContext?.issueId===i.id&&!r.executionContext?.parentRunId);
  if(!linked.length)return null;
  linked.sort((a,b)=>new Date(b.createdAt||0)-new Date(a.createdAt||0));
  return linked[0];
}
function issueExecution(i){
  const p=(state.data.projects||[]).find(x=>x.id===i.projectId),task=latestIssueTask(i),run=latestIssueRun(i,task)||(state.issueDetailData?.run?.id&&state.issueDetailData.issue?.id===i.id?state.issueDetailData.run:null),workflowId=i.workflowId||defaultSOPForIssue(i),w=(state.data.workflows||[]).find(x=>x.id===workflowId);
  return {project:p,task,run,workflow:w}
}
const SOP_LANES=[{id:'product',label:'产品'},{id:'frontend',label:'前端'},{id:'backend',label:'后端'},{id:'design',label:'设计'},{id:'repair',label:'修复'}];
function normalizeSOPLane(raw){
  const v=String(raw||'').trim().toLowerCase();
  if(['product','产品','prd'].includes(v))return 'product';
  if(['frontend','front','前端'].includes(v))return 'frontend';
  if(['backend','back','后端'].includes(v))return 'backend';
  if(['design','设计','uiux'].includes(v))return 'design';
  if(['repair','fix','issue','bugfix','修复','缺陷'].includes(v))return 'repair';
  return 'other';
}
function inferSOPLane(wf){
  if(!wf)return 'other';
  if(wf.sopLane)return normalizeSOPLane(wf.sopLane);
  const hay=`${wf.id||''} ${wf.name||''} ${(wf.tags||[]).join(' ')}`.toLowerCase();
  if(/generic-product|产品交付|(product.+(delivery|prd))/.test(hay))return 'product';
  if(/generic-frontend|前端交付|(frontend.+delivery)/.test(hay))return 'frontend';
  if(/generic-design|设计交付/.test(hay))return 'design';
  if(/generic-backend|后端交付|requirement-development|loopforge|需求开发/.test(hay))return 'backend';
  if(/issue-fix|缺陷修复/.test(hay))return 'repair';
  return 'other';
}
function sopLaneLabel(lane){return ({product:'产品',frontend:'前端',backend:'后端',design:'设计',repair:'修复',other:'其他'})[lane]||'其他'}
function laneStatusLabel(status){return ({not_started:'未开始',active:'进行中',waiting:'待确认',attention:'需要介入',succeeded:'已完成',closed:'已闭环'})[status]||status||'未开始'}
function isRequirementWorkItem(issue){
  const kind=String(issue?.kind||'').toLowerCase();
  const source=String(issue?.source||'').toLowerCase();
  if(source==='gongfeng'||kind==='issue')return false;
  return kind==='requirement'||source==='tapd'||source==='local'||source==='';
}
function projectSOPBindings(project){
  const listed=(project?.sopBindings||[]).filter(b=>String(b.workflowId||'').trim());
  if(listed.length)return listed.slice().sort((a,b)=>(Number(a.order)||0)-(Number(b.order)||0));
  const wf=String(project?.workflowId||'').trim();
  return wf?[{id:'sop-default',workflowId:wf,order:1}]:[];
}
function normalizePlaybookList(books){
  const seen=new Set();
  const out=[];
  (books||[]).forEach((b,i)=>{
    const id=String(b.id||'').trim()||('pb-'+(i+1));
    if(seen.has(id))return;
    seen.add(id);
    const bindings=(b.bindings||[]).filter(x=>String(x.workflowId||'').trim()).map((x,j)=>({workflowId:x.workflowId,order:j+1,assigneeUserId:Number(x.assigneeUserId)||0,handoff:x.handoff||[],dependsOn:x.dependsOn||[]}));
    out.push({id,name:String(b.name||'').trim()||'未命名套餐',default:!!b.default,bindings});
  });
  if(out.length&&!out.some(b=>b.default))out[0].default=true;
  let found=false;
  return out.map(b=>{
    if(b.default&&!found){found=true;return b}
    return {...b,default:false};
  });
}
function projectPlaybooks(project){
  const named=normalizePlaybookList(project?.sopPlaybooks);
  if(named.length)return named;
  const legacy=projectSOPBindings(project);
  if(!legacy.length)return [];
  return [{id:'default',name:'默认流程',default:true,bindings:legacy}];
}
function projectPlaybook(project,id){
  const books=projectPlaybooks(project);
  const want=String(id||'').trim();
  return books.find(b=>b.id===want)||books.find(b=>b.default)||books[0]||null;
}
function playbookNames(project,id){
  const book=arguments.length>1?projectPlaybook(project,id):projectPlaybook(project,projectPlaybooks(project).find(b=>b.default)?.id);
  return (book?.bindings||[]).map(b=>{
    const wf=(state.data.workflows||[]).find(w=>w.id===b.workflowId);
    return wf?.name||b.workflowId;
  }).filter(Boolean);
}
function materializePlaybook(draft){
  const bindings=projectSOPBindings(draft).map((b,i)=>({workflowId:b.workflowId,order:i+1,assigneeUserId:Number(b.assigneeUserId)||0}));
  draft.sopBindings=bindings;
  if(bindings.length)draft.workflowId=bindings[0].workflowId;
  return bindings;
}
function effectiveIssueSOPBindings(issue){
  const listed=(issue?.sopBindings||[]).filter(b=>String(b.workflowId||'').trim());
  if(listed.length)return listed.slice().sort((a,b)=>(Number(a.order)||0)-(Number(b.order)||0));
  const wf=String(issue?.workflowId||'').trim();
  return wf?[{id:'sop-default',workflowId:wf,order:1,assigneeUserId:issue.assigneeUserId||0}]:[];
}
function clientIssueLanes(issue){
  if(state.issueDetailData?.requirement?.issue?.id===issue.id&&Array.isArray(state.issueDetailData.requirement.lanes)){
    return state.issueDetailData.requirement.lanes;
  }
  const bindings=effectiveIssueSOPBindings(issue);
  const tasks=(state.data.tasks||[]).filter(t=>t.issueId===issue.id);
  const runs=(state.data.runs||[]).filter(r=>r.executionContext?.issueId===issue.id&&!r.executionContext?.parentRunId);
  const by={};
  const ensure=(wfId,meta)=>{
    if(!by[wfId])by[wfId]={lane:meta.lane||'other',label:meta.label||wfId,workflowId:wfId,workflowName:meta.label||wfId,status:'not_started',started:false,runCount:0,tokens:0,assigneeUserId:meta.assigneeUserId||0,bindingId:meta.bindingId||'',handoff:meta.handoff||[],order:meta.order||0,dependsOn:meta.dependsOn||[]};
    return by[wfId];
  };
  bindings.forEach((b,i)=>{
    const wf=(state.data.workflows||[]).find(w=>w.id===b.workflowId)||{id:b.workflowId,name:b.workflowId};
    const lane=ensure(b.workflowId,{lane:inferSOPLane(wf),label:wf.name||b.workflowId,assigneeUserId:b.assigneeUserId,bindingId:b.id,handoff:b.handoff,order:b.order||i+1,dependsOn:b.dependsOn});
    if(!lane.stages?.length)lane.stages=plannedSOPNodes(null,wf);
  });
  runs.forEach(run=>{
    const wf=run.workflowSnapshot?.id?run.workflowSnapshot:(state.data.workflows||[]).find(w=>w.id===run.workflowId)||{id:run.workflowId,name:run.workflowId};
    const lane=ensure(run.workflowId,{lane:inferSOPLane(wf),label:wf.name||run.workflowId});
    lane.started=true;
    lane.runCount+=1;
    lane.tokens+=(run.usage?.totalTokens||0);
    lane.workflowId=run.workflowId;
    lane.workflowName=wf.name||run.workflowId;
    lane.latestRunId=run.id;
    const nodeFailed=Object.values(run.nodes||{}).some(n=>n?.status==='failed');
    const nodeWait=Object.values(run.nodes||{}).some(n=>n?.status==='waiting_approval');
    lane.status=run.status==='queued'?'active':nodeFailed||run.status==='failed'||run.status==='cancelled'?'attention':nodeWait||run.status==='waiting'?'waiting':run.status==='succeeded'?'succeeded':['running','queued'].includes(run.status)?'active':'active';
  });
  tasks.forEach(task=>{
    if(!task.workflowId)return;
    const wf=(state.data.workflows||[]).find(w=>w.id===task.workflowId)||{id:task.workflowId};
    const lane=ensure(task.workflowId,{lane:inferSOPLane(wf),label:wf.name||task.workflowId});
    if(!lane.workflowName)lane.workflowName=wf.name||task.workflowId;
    if(!lane.started&&['queued','dispatched','running'].includes(task.status)){lane.started=true;lane.status='active'}
  });
  const out=bindings.map(b=>by[b.workflowId]).filter(Boolean);
  Object.values(by).forEach(lane=>{
    if(out.some(x=>x.workflowId===lane.workflowId))return;
    if(lane.started){lane.orphan=true;out.push(lane)}
  });
  return collapseSingleSOPLane(bindings, by, runs, out);
}
function collapseSingleSOPLane(bindings, by, runs, out){
  if(bindings.length!==1)return out;
  const startedLanes=Object.values(by).filter(l=>l.started);
  if(startedLanes.length>1)return out;
  const started=runs.filter(r=>by[r.workflowId]?.started).slice().sort((a,b)=>String(b.createdAt||'').localeCompare(String(a.createdAt||''))||String(b.id||'').localeCompare(String(a.id||'')));
  const latest=started.length?by[started[0].workflowId]:Object.values(by).find(l=>l.started);
  if(!latest)return out;
  const run=started[0];
  if(run?.workflowSnapshot?.name){
    latest.workflowName=run.workflowSnapshot.name;
    latest.label=run.workflowSnapshot.name;
  }
  latest.orphan=false;
  latest.bindingId=latest.bindingId||bindings[0].id||'';
  latest.assigneeUserId=latest.assigneeUserId||bindings[0].assigneeUserId||0;
  latest.order=bindings[0].order||latest.order||1;
  if(run)latest.latestRunId=run.id;
  return [latest];
}
function issueSOPLabel(issue){
  const lanes=clientIssueLanes(issue).filter(l=>!l.orphan);
  if(lanes.length===1){
    const name=String(lanes[0].workflowName||lanes[0].label||'').trim();
    if(name&&name!==lanes[0].workflowId)return name;
  }
  const w=(state.data.workflows||[]).find(x=>x.id===issue?.workflowId);
  return w?.name||'未关联';
}
function suggestedWorkflowForLane(lane,issue){
  const projectId=issue?.projectId||state.projectFilter;
  const workflows=state.data.workflows||[];
  const match=workflows.find(w=>!w.generic&&inferSOPLane(w)===lane&&(!w.projectId||w.projectId===projectId))||workflows.find(w=>w.generic&&inferSOPLane(w)===lane);
  return match?.id||'';
}
function issueSOPQuickAdds(issue,boundIds){
  return SOP_LANES.map(lane=>{
    const workflowId=suggestedWorkflowForLane(lane.id,issue);
    if(!workflowId||boundIds.has(workflowId))return null;
    const wf=(state.data.workflows||[]).find(w=>w.id===workflowId);
    return {lane:lane.id,label:lane.label,workflowId,name:wf?.name||lane.label};
  }).filter(Boolean);
}
function playbookStepNames(book){
  return (book?.bindings||[]).map(b=>{
    const wf=(state.data.workflows||[]).find(w=>w.id===b.workflowId);
    return wf?.name||b.workflowId;
  }).filter(Boolean);
}
function issueLaneDots(issue){
  const lanes=clientIssueLanes(issue).filter(l=>!l.orphan);
  if(lanes.length<2)return '';
  return `<span class="issue-lane-dots">${lanes.map(l=>`<i class="lane-dot ${escapeHTML(l.status||'not_started')}" title="${escapeHTML(l.label||l.workflowName||l.lane)} · ${escapeHTML(laneStatusLabel(l.status))}"></i>`).join('')}</span>`;
}
function issueOwnerSummary(i){
  const primary=issueAssigneeName(i)||'未指定';
  const extra=[...new Set((i.sopBindings||[]).map(b=>Number(b.assigneeUserId)||0).filter(id=>id&&id!==Number(i.assigneeUserId)))];
  return extra.length?`${primary} · +${extra.length}`:primary;
}
function issueHasFailedNode(run){
  return Object.values(run?.nodes||{}).some(n=>n?.status==='failed');
}
function issueRunHealth(i){
  const {task,run}=issueExecution(i);
  if(issueExecutionBlocked(i))return'closed';
  if(issueTrackerClosed(i))return'closed';
  if(run&&['queued','running','waiting'].includes(run.status))return'active';
  if(isGhostRun(run))return'active';
  if(run?.status==='succeeded')return'closed';
  if(run?.status==='failed'||run?.status==='cancelled'||issueHasFailedNode(run))return'attention';
  if(issueClarifying(i.id))return'active';
  if(clarificationNeedsConfirm(i))return'attention';
  if(requirementNeedsClarification(i))return'attention';
  if(task&&['queued','dispatched','running'].includes(task.status))return'active';
  if(!task||!run)return'not_started';
  return'not_started';
}
function issueHealthRank(i){return ({attention:0,active:1,not_started:2,closed:3}[issueRunHealth(i)]??4)}
function currentRunNode(i){
  const {run,workflow}=issueExecution(i);if(!run||run.status==='queued')return null;
  const defs=run.workflowSnapshot?.nodes||workflow?.nodes||[];
  const entries=Object.entries(run.nodes||{});
  const hit=entries.find(([,n])=>n.status==='failed')||entries.find(([,n])=>n.status==='waiting_approval')||entries.find(([,n])=>n.status==='running');
  if(!hit)return null;
  const node=defs.find(n=>n.id===hit[0]);
  const agent=(state.data.agents||[]).find(x=>x.id===node?.agentId);
  return {id:hit[0], nodeRun:hit[1], def:node, agent};
}
function issueActor(i){
  if(issueExecution(i).run?.status==='queued')return{kind:'active',title:'排队中',sub:'获得项目名额后自动启动'};
  const live=currentRunNode(i);
  const bound=(state.data.agents||[]).find(x=>x.id===i.assigneeId);
  if(live?.nodeRun?.status==='waiting_approval')return{kind:'waiting',title:'待你审批',sub:live.def?.name||'人工门禁'};
  if(live?.nodeRun?.status==='failed')return{kind:'attention',title:'需要介入',sub:`${live.def?.name||'当前节点'} · 门禁失败`};
  if(live?.nodeRun?.status==='running')return{kind:'active',title:live.agent?.name||'Agent 执行中',sub:live.def?.name||'运行中'};
  if(issueClarifying(i.id))return{kind:'active',title:'澄清分析中',sub:'关闭网页也不会中断'};
  const task=latestIssueTask(i);
  if(task&&['queued','dispatched','running'].includes(task.status))return{kind:'active',title:'正在启动',sub:task.status==='queued'?'排队中':'调度中'};
  if(requirementNeedsClarification(i)){
    const score=i.clarification?.score||0;
    return{kind:'attention',title:'需要你澄清',sub:`${score} 分 · 未达准入线`};
  }
  if(issueRunHealth(i)==='attention')return{kind:'attention',title:'需要介入',sub:issueCurrentStage(i).hint||'运行失败或已中断'};
  if(bound)return{kind:'bound',title:bound.name,sub:'已绑定 Agent'};
  return{kind:'idle',title:'尚未执行',sub:'触发后由 SOP 节点 Agent 接手'};
}
function issueOutcome(i){
  const health=issueRunHealth(i),stage=issueCurrentStage(i);
  if(issueManuallyClosed(i))return{key:'closed',short:'已关闭',title:'需求已关闭',body:'该需求已在 AgentFlow 中手动关闭。如需继续修改或执行，请先重新打开。'};
  if(issueTrackerClosed(i)&&!issueMergedDelivery(i))return{key:'closed',short:'已关闭',title:gongfengIssueClosed(i)?'工蜂已关闭':'外部工作项已关闭',body:'请先在外部系统重新打开工作项，等待状态同步后再执行。'};
  if(deliveryLocked(i)){const label=deliveryLockedLabel(i);const short=label==='MR 已合入'?'已合入':label==='已合入基线'?'已合入基线':label==='关闭'?'关闭':'已交付';return{key:'closed',short,title:label,body:label==='MR 已合入'?'关联 MR 已合入默认分支。如需继续修改，请用重新触发再开一条 SOP。':label==='已合入基线'?'关联 MR 已合入集成分支，尚未进默认分支。如需继续修改，请用重新触发再开一条 SOP。':label==='关闭'?'需求已关闭。如需继续修改或执行，请用重新触发再开一条 SOP。':'SOP 已全部通过。如需继续修改，请用重新触发再开一条 SOP。'};}
  if((i.status||'')==='done')return{key:'closed',short:stage.label==='交付闭环'?'已交付':'已完成',title:stage.label==='交付闭环'?'交付闭环':'需求已完成',body:stage.label==='交付闭环'?'SOP 已全部通过。如需继续修改，请先重新打开后再触发新的 SOP。':'需求已完成。'};
  if(clarificationNeedsConfirm(i)){
    return{key:'confirm',short:'待确认',title:'请确认澄清结论并写回原单',body:'结论已追加到平台说明。确认写回原单（追加评论）后，即可开始触发需求执行 SOP。'};
  }
  if(requirementNeedsClarification(i)){
    const score=i.clarification?.score||0,threshold=clarificationPolicy((state.data.projects||[]).find(p=>p.id===i.projectId)).threshold||80;
    return{key:'attention',short:'待澄清',title:'需要你补充澄清',body:`导入评分 ${score} 分，未达准入线 ${threshold} 分。请先回答澄清问题后再触发 SOP。`};
  }
  if(latestIssueRun(i)?.status==='cancelled')return{key:'attention',short:'已取消',title:'运行已取消',body:'这次运行已结束且工作区已清理，不能接着原记录重试。可以重新触发整条 SOP。'};
  if(health==='attention')return{key:'attention',short:'失败',title:'运行失败',body:`停在「${stage.label}」。可从失败节点重试，或整条重新触发。`};
  if(stage.key==='waiting')return{key:'waiting',short:'待确认',title:'等待确认',body:`「${stage.label}」需要批准后才会继续。`};
  if(health==='active')return{key:'active',short:'进行中',title:'进行中',body:`当前在「${stage.label}」。`};
  if(health==='closed')return{key:'closed',short:'已完成',title:'已完成',body:'全部节点已通过。结果不对可以重新触发整条，或点某个节点从那里重跑。'};
  return{key:'idle',short:'未开始',title:'尚未执行',body:'触发后会按 SOP 执行。未绑定仓库时会先确认绑定。'};
}
function issueIdForRun(runId){
  const run=(state.data.runs||[]).find(r=>r.id===runId)||(state.activeRun?.id===runId?state.activeRun:null);
  return run?.executionContext?.issueId||(state.data.tasks||[]).find(t=>t.runId===runId)?.issueId||null
}
function gongfengIssueClosed(i){
  return String(i?.source||'').toLowerCase()==='gongfeng' && String(i?.externalRef?.externalState||'').toLowerCase()==='closed';
}
function deliveryLocked(i){return String(i?.deliveryState||'').toLowerCase()==='merged'}
function issueManuallyClosed(i){return String(i?.status||'').toLowerCase()==='closed'&&String(i?.closeSource||'').toLowerCase()!=='external'}
function issueExternalState(i){return String(i?.externalRef?.externalState||'').trim().toLowerCase()}
function issueTrackerClosed(i){
  if(String(i?.status||'').toLowerCase()==='closed'&&String(i?.closeSource||'').toLowerCase()==='external')return true;
  if(gongfengIssueClosed(i))return true;
  const s=issueExternalState(i);
  return s==='closed'||s==='已关闭';
}
function issueInPlanning(i){
  const s=issueExternalState(i);
  return s==='planned'||s==='规划中'||s==='planning';
}
function issueExternallyDone(i){
  return ['done','已完成','完成','合入','已合入'].includes(issueExternalState(i));
}
function deliveryMRLinked(i){return deliveryMRList(i).length>0}
// 多仓库交付每个仓库一个 MR；旧数据只有 deliveryMrUrl。
function deliveryMRList(i){
  const withRef=m=>{const hit=String(m.url).match(/git\.example\.com\/(.+?)(?:\/-)?\/merge_requests\/(\d+)/);return{...m,project:m.project||hit?.[1]||'',iid:m.iid||Number(hit?.[2])||0}};
  const list=(i?.deliveryMrs||[]).filter(m=>String(m?.url||'').trim());
  if(list.length)return list.map(withRef);
  const url=String(i?.deliveryMrUrl||'').trim();
  return url?[withRef({url,state:i?.deliveryMrState||''})]:[];
}
const deliveryMRStateText={merged:'已合入',integrated:'已合入基线',opened:'待合入',reopened:'待合入',closed:'已关闭'};
function deliveryMRButtons(issue,{cls='button ghost small'}={}){
  const list=deliveryMRList(issue);
  return list.map(mr=>{
    const iid=mr.iid||String(mr.url).match(/merge_requests\/(\d+)/)?.[1]||'';
    const repo=mr.repository||String(mr.project||'').split('/').pop();
    const label=list.length>1?`打开 MR · ${repo||'仓库'}${iid?` !${iid}`:''}`:'打开 MR';
    const st=deliveryMRStateText[String(mr.state||'').toLowerCase()];
    return `<a class="${cls}" href="${escapeHTML(safeURL(mr.url))}" target="_blank" rel="noopener noreferrer" title="${escapeHTML([mr.project,st].filter(Boolean).join(' · ')||mr.url)}">${escapeHTML(label)} ↗</a>`;
  }).join('');
}
function deliveryLockedLabel(i){
  if(issueManuallyClosed(i))return '关闭';
  const state=String(i?.deliveryMrState||'').toLowerCase();
  // 旧版手动关闭会写入 deliveryState=merged、deliveryMrState=closed，但没有 MR 链接。
  if(!deliveryMRLinked(i))return state==='closed'?'已关闭':'交付闭环';
  switch(state){
    case 'merged':return 'MR 已合入';
    case 'integrated':return '已合入基线';
    case 'closed':return 'MR 已关闭';
    default:return '交付闭环';
  }
}
// 工蜂合入 MR 后会把工单关掉。有真实合入结果时，展示以 MR 为准。
function issueMergedDelivery(i){
  if(!deliveryLocked(i))return false;
  const label=deliveryLockedLabel(i);
  return label==='MR 已合入'||label==='已合入基线';
}
function deliveryBlockReason(i){
  if(issueManuallyClosed(i))return'工作项已关闭，请先重新打开工作项再触发 SOP';
  if(!deliveryLocked(i))return'';
  const label=deliveryLockedLabel(i);
  if(label==='已关闭'||label==='关闭')return'工作项已关闭，请先重新打开工作项再触发 SOP';
  return label==='交付闭环'?'工作项已交付闭环，请先重新打开后再触发 SOP':`${label}，请先重新打开工作项再触发 SOP`;
}
function issueExecutionBlocked(i){
  if(issueTrackerClosed(i)&&!issueManuallyClosed(i))return'外部工作项已关闭，请先在外部系统重新打开';
  const blocked=deliveryBlockReason(i);
  if(blocked)return blocked;
  return'';
}
function closedTrackerHint(issue){const label=issue?.source==='gongfeng'?'工蜂':'外部工作项';return `<span class="muted" title="${label}已关闭，不能触发 SOP">${label}已关闭，不能触发</span>`}
function deliveredHint(issue){const label=deliveryLockedLabel(issue);return `<span class="muted" title="${escapeHTML(label)}，请在详情中重新打开后才可触发">${escapeHTML(label)}</span>`}
function reopenDeliveredButton(issue,{cls='button primary small'}={}){if(issueTrackerClosed(issue)||(!issueManuallyClosed(issue)&&!deliveryLocked(issue)))return'';return `<button type="button" class="${cls}" data-reopen-issue="${escapeHTML(issue.id)}">重新打开</button>`}
function canRetriggerIssue(issue,run){
  if(!issue||issueManuallyClosed(issue)||issueTrackerClosed(issue))return false;
  if(clarificationNeedsConfirm(issue)||requirementNeedsClarification(issue))return false;
  const current=run||latestIssueRun(issue);
  if(current&&['queued','running','waiting'].includes(current.status))return false;
  if(deliveryLocked(issue))return true;
  return ['succeeded','failed','cancelled'].includes(current?.status);
}
function retriggerButton(issue,run,{ghost=false,cls}={}){
  if(!canRetriggerIssue(issue,run))return'';
  const klass=cls||(ghost?'button ghost small':'button primary small');
  return `<button type="button" class="${klass}" data-retrigger-issue="${escapeHTML(issue.id)}" title="整条 SOP 从头再开一条新运行，旧记录保留">重新触发</button>`;
}
function triggerSOPButton(issue,{ghost=false,cls='button primary small',text='触发'}={}){
  if(issueExecutionBlocked(issue))return issueTrackerClosed(issue)&&!issueManuallyClosed(issue)?closedTrackerHint(issue):deliveredHint(issue);
  if(clarificationNeedsConfirm(issue))return `<button class="${ghost?'button ghost small':cls}" data-start-clarify title="请先确认澄清结论并写回原单">去确认结论</button>`;
  if(requirementNeedsClarification(issue))return `<button class="${ghost?'button ghost small':cls}" data-start-clarify title="需求未达准入线，请先补充澄清">去澄清</button>`;
  return `<button class="${ghost?'button ghost small':cls}" data-dispatch-issue="${escapeHTML(issue.id)}">${text}</button>`;
}
function issueActionButtons(i){
  const {task,run}=issueExecution(i),stage=issueCurrentStage(i),health=issueRunHealth(i);
  const more=`<button type="button" class="issue-act-more" data-issue-menu="${escapeHTML(i.id)}" title="更多操作">⋯</button>`;
  if(issueManuallyClosed(i)||deliveryLocked(i)||issueTrackerClosed(i))return `${run?`<button type="button" class="issue-act" data-view-run="${escapeHTML(run.id)}">查看</button>`:''}${more}`;
  if(issueClarifying(i.id))return `<button type="button" class="issue-act" data-open-issue="${escapeHTML(i.id)}" data-dtab="clarify">查看分析</button>${more}`;
  if(clarificationNeedsConfirm(i))return `<button type="button" class="issue-act primary" data-open-issue="${escapeHTML(i.id)}" data-dtab="clarify">确认结论</button>${more}`;
  if(requirementNeedsClarification(i))return `<button type="button" class="issue-act primary" data-open-issue="${escapeHTML(i.id)}" data-dtab="clarify">去澄清</button>${more}`;
  if(stage.key==='waiting')return `<button type="button" class="issue-act primary" data-open-issue="${escapeHTML(i.id)}" data-dtab="sop">${run?.pauseRequested?'继续流程':'去确认'}</button>${more}`;
  if(run?.status==='cancelled')return `${run?`<button type="button" class="issue-act" data-view-run="${escapeHTML(run.id)}">查看</button>`:''}${more}`;
  if(run?.status==='failed'){
    return `<button type="button" class="issue-act danger" data-open-issue="${escapeHTML(i.id)}" data-dtab="sop" title="查看失败原因并选择恢复方式">处理失败</button>${more}`;
  }
  if(health==='active'||health==='attention')return `<button type="button" class="issue-act" data-view-run="${escapeHTML(run?.id||'')}">查看</button>${more}`;
  if(health==='closed')return `${run?`<button type="button" class="issue-act" data-view-run="${escapeHTML(run.id)}">查看</button>`:''}${more}`;
  return `<button type="button" class="issue-act primary" data-trigger-issue="${escapeHTML(i.id)}" title="${i.workflowId?'整条 SOP 从头执行':'尚未绑定 SOP，触发时会先确认绑定'}">触发</button>${more}`;
}
function setIssueRunFilter(value,toggle=true){
  const next=toggle&&state.issueRunFilter===value?'':(value||'');
  state.issueRunFilter=next;
  resetListPage('issues');
  const sel=$('#issueRunFilter');if(sel)sel.value=next;
  if(next==='attention'&&state.issueView==='kanban')setIssueView('table',false);
  if(!$('#issuesView')?.classList.contains('active'))switchView('issues');
  else renderIssues();
}
let issueMenuPopover=null;
function ensureIssueMenuPopover(){
  if(issueMenuPopover)return issueMenuPopover;
  issueMenuPopover=document.createElement('div');
  issueMenuPopover.id='issueActionPopover';
  issueMenuPopover.className='issue-action-popover';
  issueMenuPopover.hidden=true;
  document.body.appendChild(issueMenuPopover);
  document.addEventListener('click',e=>{
    if(issueMenuPopover.hidden)return;
    if(issueMenuPopover.contains(e.target)||e.target.closest('[data-issue-menu]'))return;
    hideIssueActionMenu();
  });
  window.addEventListener('resize',hideIssueActionMenu,{passive:true});
  window.addEventListener('scroll',hideIssueActionMenu,{passive:true});
  document.addEventListener('keydown',e=>{
    if(e.key==='Escape')hideIssueActionMenu();
  });
  return issueMenuPopover;
}
function hideIssueActionMenu(){
  if(issueMenuPopover&&!issueMenuPopover.hidden){
    issueMenuPopover.hidden=true;
    issueMenuPopover.innerHTML='';
  }
}
function showIssueActionMenu(e,issueId){
  e.preventDefault();
  e.stopPropagation();
  const issue=(state.data?.issues||[]).find(i=>i.id===issueId);
  if(!issue)return;
  const menu=ensureIssueMenuPopover();
  const isClosed=issue.status==='done'||issueManuallyClosed(issue)||deliveryLocked(issue);
  menu.innerHTML=`
    <button type="button" class="issue-popover-item" data-action="edit"><span class="popover-icon">✎</span>编辑需求</button>
    ${issueTrackerClosed(issue)||(!isClosed&&!issueCanClose(issue))?'':!isClosed?`<button type="button" class="issue-popover-item danger" data-action="close"><span class="popover-icon">✕</span>关闭需求</button>`:`<button type="button" class="issue-popover-item" data-action="reopen"><span class="popover-icon">↺</span>重新打开</button>`}
    ${issueCanDelete(issue)?'<button type="button" class="issue-popover-item danger" data-action="delete"><span class="popover-icon">⌫</span>删除需求</button>':''}
  `;
  menu.querySelector('[data-action="edit"]').onclick=ev=>{
    ev.stopPropagation();
    hideIssueActionMenu();
    openIssue(issue);
  };
  const deleteBtn=menu.querySelector('[data-action="delete"]');
  if(deleteBtn)deleteBtn.onclick=ev=>{
    ev.stopPropagation();
    hideIssueActionMenu();
    deleteIssueById(issue.id);
  };
  const closeBtn=menu.querySelector('[data-action="close"]');
  if(closeBtn){
    closeBtn.onclick=ev=>{
      ev.stopPropagation();
      hideIssueActionMenu();
      closeIssueById(issue.id);
    };
  }
  const reopenBtn=menu.querySelector('[data-action="reopen"]');
  if(reopenBtn){
    reopenBtn.onclick=ev=>{
      ev.stopPropagation();
      hideIssueActionMenu();
      reopenDeliveredIssue(issue.id);
    };
  }
  menu.hidden=false;
  const btn=e.currentTarget;
  const rect=btn.getBoundingClientRect();
  const menuWidth=menu.offsetWidth||136;
  const menuHeight=menu.offsetHeight||72;
  let left=rect.right-menuWidth;
  if(left<8)left=8;
  let top=rect.bottom+4;
  if(top+menuHeight>window.innerHeight-8){
    top=rect.top-menuHeight-4;
  }
  menu.style.position='fixed';
  menu.style.left=`${Math.round(left)}px`;
  menu.style.top=`${Math.round(top)}px`;
}
function bindIssueRowActions(root=document){
  bindIssueTriggers(root);
  root.querySelectorAll('[data-issue-menu]').forEach(button=>button.onclick=e=>showIssueActionMenu(e,button.dataset.issueMenu));
  root.querySelectorAll('[data-edit-issue]').forEach(button=>button.onclick=e=>{e.stopPropagation();const id=button.dataset.editIssue;openIssue((state.data.issues||[]).find(i=>i.id===id)||(id?{id}:null))});
  root.querySelectorAll('[data-open-issue]').forEach(button=>button.onclick=e=>{e.stopPropagation();openIssueDetail(button.dataset.openIssue,button.dataset.dtab||'sop')});
  root.querySelectorAll('[data-open-logs]').forEach(button=>button.onclick=e=>{e.stopPropagation();openRunFromId(button.dataset.openLogs,{forceRuns:true})});
  root.querySelectorAll('[data-open-run]').forEach(button=>button.onclick=e=>{e.stopPropagation();openRunFromId(button.dataset.openRun)});
  root.querySelectorAll('[data-view-run]').forEach(button=>button.onclick=e=>{e.stopPropagation();if(button.dataset.viewRun){switchView('runs');showRunDetail(button.dataset.viewRun)}});
  root.querySelectorAll('[data-resume-run]').forEach(button=>button.onclick=e=>{e.stopPropagation();resumeRun(button.dataset.resumeRun,button)});
  root.querySelectorAll('[data-pass-node]').forEach(button=>button.onclick=e=>{e.stopPropagation();passFailedNode(button.dataset.passNode,button.dataset.node,button)});
  root.querySelectorAll('[data-reopen-issue]').forEach(button=>button.onclick=e=>{e.stopPropagation();reopenDeliveredIssue(button.dataset.reopenIssue)});
  root.querySelectorAll('[data-retrigger-issue]').forEach(button=>button.onclick=e=>{e.stopPropagation();retriggerIssue(button.dataset.retriggerIssue)});
  root.querySelectorAll('[data-rerun-from-here]').forEach(button=>button.onclick=e=>{e.stopPropagation();openRerunFromNode(state.issueDetailData?.run,button.dataset.rerunFromHere)});
  root.querySelectorAll('[data-close-issue]').forEach(button=>button.onclick=e=>{e.stopPropagation();closeIssueById(button.dataset.closeIssue)});
}
function issueLaneStage(i){
  const lanes=clientIssueLanes(i).filter(l=>l.started);
  if(lanes.length<2)return null;
  const attn=lanes.find(l=>l.status==='attention');
  if(attn)return{key:'attention',label:`失败 · ${attn.label}`,hint:`${attn.label}需要介入`};
  const wait=lanes.find(l=>l.status==='waiting');
  if(wait)return{key:'waiting',label:`${wait.label}待确认`,hint:`${wait.label}等待人工判断`};
  const active=lanes.find(l=>l.status==='active');
  if(active)return{key:'active',label:`${active.label}进行中`,hint:`${active.label}正在执行`};
  if(lanes.every(l=>l.status==='succeeded'||l.status==='closed'))return{key:'closed',label:'交付闭环',hint:'已跑过的泳道均已通过'};
  return{key:'active',label:lanes.map(l=>l.label).join(' / '),hint:'多条 SOP 并行'};
}
// Nested SOP approval must surface on the requirement banner too: the top run
// only shows its SOP node as "running", the waiting_approval lives in the
// child run, which the server snapshot already reports (overall.status).
function issueNestedWaitingStage(i){
  const snap=state.issueDetailData?.requirement;
  if(snap?.issue?.id!==i.id||snap.overall?.status!=='waiting')return null;
  const findWait=stages=>{
    for(const st of stages||[]){
      if(st.status==='waiting_approval')return st;
      const deep=findWait(st.children);
      if(deep)return deep;
    }
    return null;
  };
  for(const lane of snap.lanes||[]){
    if(!lane.started||lane.status!=='waiting')continue;
    const hit=findWait(lane.stages);
    if(hit)return{key:'waiting',label:hit.name||hit.nodeId,hint:'等待人工判断'};
  }
  return null;
}
function issueCurrentStage(i){
  const {task,run,workflow}=issueExecution(i);
  if(issueManuallyClosed(i))return{key:'closed',label:'已关闭',hint:'需求已手动关闭，重新打开后才可再次触发 SOP'};
  if(issueTrackerClosed(i)&&!issueMergedDelivery(i))return{key:'closed',label:gongfengIssueClosed(i)?'工蜂已关闭':'已关闭',hint:'外部工作项已关闭'};
  if(deliveryLocked(i))return{key:'closed',label:deliveryLockedLabel(i),hint:'重新打开后才可再次触发 SOP'};
  if(!run){
    if(issueClarifying(i.id))return{key:'active',label:'澄清分析中',hint:'关闭网页也不会中断，完成后可回来查看'};
    if(clarificationNeedsConfirm(i)){
      const score=i.clarification?.score||0;
      return{key:'confirm',label:score?`待确认结论 · ${score} 分`:'待确认结论',hint:'确认写回原单（追加评论）后即可开始触发需求'};
    }
    if(requirementNeedsClarification(i)){
      const score=i.clarification?.score||0,threshold=clarificationPolicy((state.data.projects||[]).find(p=>p.id===i.projectId)).threshold||80;
      return{key:'attention',label:`待澄清 · ${score} 分`,hint:`未达准入线 ${threshold} 分，需要你补充信息`};
    }
    if(task&&['queued','dispatched','running'].includes(task.status)){
      return{key:'active',label:task.status==='queued'?'排队中':'运行中',hint:'任务已调度，正在准备执行工作区与 Agent 环境'};
    }
    if((i.status||'')==='done')return{key:'closed',label:'已完成',hint:'需求已完成'};
    if(i.kind==='requirement'){
      const score=i.clarification?.score||0,ready=i.clarification?.status==='ready'||i.clarification?.passed;
      const multi=issueLaneStage(i);
      if(multi)return multi;
      return{key:'not_started',label:ready?(score?`澄清完成 · ${score} 分`:'澄清已通过'):'等待执行',hint:ready?'可以进入研发 SOP':'尚未创建运行'};
    }
    return{key:'not_started',label:'等待执行',hint:'尚未创建运行'};
  }
  if(run.status==='queued')return{key:'active',label:'排队中',hint:'获得项目名额后自动启动'};
  const multi=issueLaneStage(i);
  if(multi)return multi;
  if(run.status==='succeeded'){
    if(String(i.deliveryState||'').toLowerCase()==='reopened')return{key:'active',label:issueStatusLabel(i.status),hint:'已重新打开，当前状态以需求状态为准'};
    return{key:'closed',label:'交付闭环',hint:'全部节点已通过'};
  }
  const nestedWait=issueNestedWaitingStage(i);
  if(nestedWait)return nestedWait;
  const definitions=run.workflowSnapshot?.nodes||workflow?.nodes||[],byID=new Map(definitions.map(n=>[n.id,n])),entries=Object.entries(run.nodes||{});
  const ghost=isGhostRun(run);
  const current=entries.find(([,n])=>n.status==='failed')||entries.find(([,n])=>n.status==='waiting_approval')||entries.find(([,n])=>n.status==='running')||entries.find(([,n])=>n.status==='pending_input')||entries.find(([,n])=>n.status==='cancelled');
  if(current){
    const [id,node]=current,name=byID.get(id)?.name||id;
    if(node.status==='failed')return{key:'attention',label:`失败 · ${name}`,hint:String(node.error||'门禁失败').slice(0,120)};
    if(node.status==='waiting_approval')return{key:'waiting',label:name,hint:'等待人工判断'};
    if(node.status==='cancelled')return{key:'attention',label:`已取消 · ${name}`,hint:'可重新触发整条 SOP，不能接着这次运行重试'};
    if(node.status==='running'||node.status==='pending_input'){
      const hint=ghost?'运行状态已失效，但节点仍在执行，建议取消':String(node.activity||'Agent 正在执行');
      return{key:'active',label:name,hint};
    }
    return{key:'active',label:name,hint:'Agent 正在执行'};
  }
  if(run.status==='failed')return{key:'attention',label:'运行失败',hint:'打开查看失败原因'};
  if(run.status==='cancelled')return{key:'attention',label:'运行已取消',hint:'可重新触发整条 SOP，不能接着这次运行重试'};
  const last=[...entries].reverse().find(([,n])=>n.status==='succeeded');return{key:'not_started',label:last?(byID.get(last[0])?.name||last[0]):'等待调度',hint:last?'等待下一节点':'尚未开始'}
}
