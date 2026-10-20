function setIssueView(view, rerender=true){
  state.issueView=view==='kanban'?'kanban':'table';
  localStorage.setItem('agentflow.issueView',state.issueView);
  $$('[data-issue-view]').forEach(b=>b.classList.toggle('active',b.dataset.issueView===state.issueView));
  if(rerender&&state.data)renderIssues();
}

function renderIssueAssigneeFilter(projectIssues=[]){
  const sel=$('#issueAssigneeFilter');if(!sel)return;
  const current=state.issueAssigneeFilter||'';
  const members=projectMembers(state.projectFilter);
  const userMap=new Map();
  members.forEach(m=>{
    const u=userById(m.userId);
    if(u)userMap.set(String(u.id), userDisplay(u));
  });
  projectIssues.forEach(i=>{
    if(i.assigneeUserId){
      const uid=String(i.assigneeUserId);
      if(!userMap.has(uid)){
        const u=userById(i.assigneeUserId);
        userMap.set(uid, u?userDisplay(u):`用户 ${uid}`);
      }
    }else if(i.assigneeId&&i.assigneeType==='agent'){
      const aid=String(i.assigneeId);
      if(!userMap.has(aid)){
        const a=(state.data.agents||[]).find(x=>x.id===aid);
        userMap.set(aid, a?.name?`Agent: ${a.name}`:`Agent: ${aid}`);
      }
    }
    (i.sopBindings||[]).forEach(b=>{
      const uid=Number(b.assigneeUserId)||0;
      if(!uid)return;
      const key=String(uid);
      if(userMap.has(key))return;
      const u=userById(uid);
      userMap.set(key, u?userDisplay(u):`用户 ${key}`);
    });
  });
  let html=`<option value="">全部责任人</option><option value="unassigned"${current==='unassigned'?' selected':''}>未指定责任人</option>`;
  for(const [id, label] of userMap.entries()){
    html+=`<option value="${escapeHTML(id)}"${current===id?' selected':''}>${escapeHTML(label)}</option>`;
  }
  sel.innerHTML=html;
  sel.value=current;
}

function normalizeIssueTitleSearch(value){
  return String(value||'').normalize('NFKC').toLocaleLowerCase().replace(/[\s\p{P}\p{S}]+/gu,'');
}

function issueMatchesQuery(issue,project,query){
  const raw=String(query||'').trim().toLocaleLowerCase();
  if(!raw)return true;
  const title=String(issue?.title||''),normalizedTitle=normalizeIssueTitleSearch(title),normalizedQuery=normalizeIssueTitleSearch(raw);
  const titleTokens=raw.split(/[\s\p{P}\p{S}]+/u).map(normalizeIssueTitleSearch).filter(Boolean);
  if(normalizedQuery&&normalizedTitle.includes(normalizedQuery))return true;
  if(titleTokens.length&&titleTokens.every(token=>normalizedTitle.includes(token)))return true;
  const key=`${project?.key||'ISS'}-${issue?.number||''}`,external=issue?.externalRef?.externalId||issue?.externalRef?.externalIid||'';
  return `${key} ${title} ${issue?.description||''} ${external}`.toLocaleLowerCase().includes(raw);
}

function filteredIssues(){
  const projects=state.data.projects||[];
  let issues=(state.data.issues||[]).filter(i=>!state.projectFilter||i.projectId===state.projectFilter);
  if(state.sourceFilter)issues=issues.filter(i=>(i.source||'local')===state.sourceFilter);
  if(state.issueStatusFilter)issues=issues.filter(i=>issueMatchesStatusFilter(i,state.issueStatusFilter));
  if(state.issueAssigneeFilter){
    if(state.issueAssigneeFilter==='unassigned'){
      issues=issues.filter(i=>!i.assigneeUserId&&!i.assigneeId&&!(i.sopBindings||[]).some(b=>b.assigneeUserId));
    }else{
      issues=issues.filter(i=>String(i.assigneeUserId)===state.issueAssigneeFilter||String(i.assigneeId)===state.issueAssigneeFilter||(i.sopBindings||[]).some(b=>String(b.assigneeUserId)===state.issueAssigneeFilter));
    }
  }
  if(state.issueRunFilter)issues=issues.filter(i=>issueRunHealth(i)===state.issueRunFilter);
  if(state.issueQuery){
    issues=issues.filter(i=>issueMatchesQuery(i,projects.find(x=>x.id===i.projectId),state.issueQuery));
  }
  const priority={urgent:0,high:1,medium:2,low:3};
  return [...issues].sort((a,b)=>issueHealthRank(a)-issueHealthRank(b)||(priority[a.priority]??4)-(priority[b.priority]??4)||(b.number||0)-(a.number||0));
}

function renderIssues(){
  const tapdImport=document.querySelector("#importTAPDIssue");if(tapdImport)tapdImport.hidden=!currentProject()?.tapdWorkspaceId;
  watchClarifyingIssues();
  const projectIssues=(state.data.issues||[]).filter(i=>!state.projectFilter||i.projectId===state.projectFilter);
  renderProjectContext(projectIssues);
  renderIssueAssigneeFilter(projectIssues);
  const statusSel=$('#issueStatusFilter');if(statusSel&&statusSel.value!==state.issueStatusFilter)statusSel.value=state.issueStatusFilter||'';
  const issues=filteredIssues();
  $$('[data-issue-view]').forEach(b=>b.classList.toggle('active',b.dataset.issueView===state.issueView));
  const board=$('#issueBoard');
  board.classList.toggle('kanban-mode',state.issueView==='kanban');
  if(state.issueView==='kanban'){
    board.innerHTML=renderIssueKanban(issues);
    bindIssueKanban();
    return;
  }
  const emptyTitle=state.issueRunFilter==='attention'?'没有需要介入的工作项':'当前项目没有匹配的工作项';
  const emptyHint=state.issueRunFilter==='attention'?'澄清未达标、门禁失败、运行失败或已中断的工作项会显示在这里。':'从 GitHub 同步，或在本平台新建需求。';
  const paged=paginateList('issues', issues);
  const keyWidth=issueKeyColumnWidth(paged.items);
  board.innerHTML=`<div class="issue-table" data-page-anchor="issues" style="--issue-col-key:${keyWidth}"><div class="issue-table-row issue-table-head" role="row"><span>编号</span><span>标题</span><span>责任人</span><span>仓库</span><span>优先级</span><span>关联 SOP</span><span>当前环节</span><span>进度</span><span>操作</span></div><div class="issue-table-body">${paged.items.map(issueRow).join('')||`<div class="issue-table-empty"><span>⌕</span><b>${emptyTitle}</b><p>${emptyHint}</p></div>`}</div></div>${pagerHTML('issues',paged)}`;
  $$('.issue-table-row[data-id]').forEach(el=>el.onclick=()=>openIssueDetail(el.dataset.id));
  bindIssueRowActions(board);
  bindPager(board);
}

let clarifyPoll=null;
function watchClarifyingIssues(){
  const busy=(state.data.issues||[]).some(i=>i.clarification?.status==='analyzing')||issueClarifyJobs.size>0;
  if(busy&&!clarifyPoll)clarifyPoll=setInterval(()=>{if(!document.hidden)refreshIssues()},4000);
  if(!busy&&clarifyPoll){clearInterval(clarifyPoll);clarifyPoll=null}
}

function issueKanbanCard(i){
  const {project:p,run,workflow:w}=issueExecution(i);
  const stage=issueCurrentStage(i);
  const progress=issueListProgress(i,run,w);
  const key=issueKeyLabel(i);
  const isFailed=Boolean(run&&(['failed','cancelled'].includes(run.status)||issueHasFailedNode(run)));
  const needsConfirm=clarificationNeedsConfirm(i);
  const cardTone=needsConfirm?' needs-confirm':(isFailed?' needs-attention':'');
  return `<article class="kanban-card${cardTone}" draggable="true" data-id="${escapeHTML(i.id)}">
    <div class="kanban-card-top"><b>${escapeHTML(key)}</b></div>
    <h4 title="${escapeHTML(i.title)}">${issueTitleMarkup(i.title)}</h4>
    <div class="kanban-card-meta">
      <span class="priority-label ${escapeHTML(i.priority||'')}"><i class="priority ${escapeHTML(i.priority||'')}"></i>${issuePriorityLabel(i.priority)}</span>
      <span class="kanban-stage ${stage.key}">${escapeHTML(stage.label)}</span>
    </div>
    <div class="kanban-card-progress"><span>${progress.done}/${progress.total||'—'} · ${progress.pct}%</span><div class="issue-progress-track"><i style="width:${progress.pct}%"></i></div></div>
    <div class="kanban-card-actions">${issueActionButtons(i)}</div>
  </article>`;
}

function renderIssueKanban(issues){
  return `<div class="kanban-board">${issueStatuses.map(([status])=>{
    const col=issues.filter(i=>(i.status||'backlog')===status);
    return `<section class="kanban-column" data-status="${status}">
      <header><b>${issueStatusLabel(status)}</b><span>${col.length}</span></header>
      <div class="kanban-column-body">${col.map(issueKanbanCard).join('')||'<div class="kanban-empty">暂无卡片</div>'}</div>
    </section>`;
  }).join('')}</div>`;
}

let kanbanDragMoved=false;
function bindIssueKanban(){
  $$('.kanban-card').forEach(card=>{
    card.ondragstart=e=>{
      kanbanDragMoved=false;
      e.dataTransfer.setData('text/plain',card.dataset.id);
      e.dataTransfer.effectAllowed='move';
      card.classList.add('dragging');
    };
    card.ondrag=()=>{kanbanDragMoved=true};
    card.ondragend=()=>card.classList.remove('dragging');
    card.onclick=()=>{if(kanbanDragMoved){kanbanDragMoved=false;return}openIssueDetail(card.dataset.id)};
  });
  bindIssueRowActions($('#issueBoard'));
  $$('.kanban-column').forEach(col=>{
    col.ondragover=e=>{e.preventDefault();e.dataTransfer.dropEffect='move';col.classList.add('drop-target')};
    col.ondragleave=e=>{if(!col.contains(e.relatedTarget))col.classList.remove('drop-target')};
    col.ondrop=async e=>{
      e.preventDefault();
      col.classList.remove('drop-target');
      const id=e.dataTransfer.getData('text/plain');
      const issue=(state.data.issues||[]).find(i=>i.id===id);
      const next=col.dataset.status;
      if(!issue||(issue.status||'backlog')===next)return;
      const prev=issue.status;
      issue.status=next;
      renderIssues();
      try{
        await persistIssue(issue);
        toast('状态已更新','ok');
      }catch(err){
        issue.status=prev;
        renderIssues();
        toast(err.message,'err');
      }
    };
  });
}
function issueStatusLabel(value){return ({backlog:'待规划',todo:'待处理',in_progress:'进行中',in_review:'评审中',done:'已完成',blocked:'已阻塞',cancelled:'已取消',closed:'已关闭'}[value]||value||'—')}
function issueStatusText(issue){
  return issueStatusLabel(issueStatusBucket(issue));
}
function issueWaitingToExecute(i){
  if(!i||issueManuallyClosed(i)||issueTrackerClosed(i)||deliveryLocked(i)||issueInPlanning(i)||issueFinished(i))return false;
  const status=i.status||'backlog';
  if(status==='done'||status==='cancelled')return false;
  if(status==='todo')return true;
  // “待执行”只补充尚未进入业务流转的 backlog；进行中、评审中和
  // 已阻塞必须留在各自的状态筛选中，即使它们暂时没有 Run。
  if(status!=='backlog')return false;
  const stage=issueCurrentStage(i);
  return stage.key==='not_started'||stage.label==='等待执行';
}
function issueFinished(i){
  if(!i)return false;
  if(issueMergedDelivery(i))return true;
  if(issueManuallyClosed(i)||issueTrackerClosed(i))return false;
  if(deliveryLocked(i))return deliveryLockedLabel(i)!=='关闭';
  if(issueExternallyDone(i))return true;
  const {run}=issueExecution(i);
  if(run?.status==='succeeded')return true;
  return !run&&(i.status||'')==='done';
}
function issueStatusBucket(issue){
  if(!issue)return 'backlog';
  if((issue.status==='closed'||issueTrackerClosed(issue))&&!issueMergedDelivery(issue))return 'closed';
  const stage=issueCurrentStage(issue);
  if(stage.key==='closed'){
    const label=stage.label||'';
    if(label==='已关闭'||label==='关闭'||label==='MR 已关闭'||label==='工蜂已关闭')return 'closed';
    if(label==='已完成'||label==='交付闭环'||label==='MR 已合入'||label==='已合入基线')return 'done';
  }
  return issue.status||'backlog';
}
function issueMatchesStatusFilter(issue,filter){
  return !filter||issueStatusBucket(issue)===filter;
}
function issuePriorityLabel(value){return ({urgent:'紧急',high:'高',medium:'中',low:'低'}[value]||value||'—')}
function taskStatusLabel(value){return ({queued:'排队中',dispatched:'已调度',running:'运行中',completed:'已完成',failed:'失败',cancelled:'已取消'}[value]||value||'—')}
function issueKeyLabel(i){
  if(i.source==='gongfeng'&&i.externalRef?.externalIid) return `#${i.externalRef.externalIid}`;
  if(i.source==='tapd'&&i.externalRef?.externalId) return String(i.externalRef.externalId);
  const p=(state.data.projects||[]).find(x=>x.id===i.projectId);
  return `${p?.key||'ISS'}-${i.number||''}`;
}
function issueKeyMarkup(i){
  const raw=String(issueKeyLabel(i)||'');
  const m=raw.match(/^(.*?)(\d+)$/);
  if(!m) return `<span class="issue-key-num">${escapeHTML(raw)}</span>`;
  return `<span class="issue-key-prefix">${escapeHTML(m[1])}</span><span class="issue-key-num">${escapeHTML(m[2])}</span>`;
}
function issueKeyColumnWidth(issues){
  let maxLen=0;
  for(const i of (issues||[])){
    const len=String(issueKeyLabel(i)||'').length;
    if(len>maxLen)maxLen=len;
  }
  if(maxLen>=16)return '168px';
  if(maxLen>=10)return '120px';
  if(maxLen>=7)return '96px';
  return '76px';
}
function issueTagTone(tag){
  const t=String(tag||'').toLowerCase();
  if(/bug|缺陷|故障/.test(t)) return 'danger';
  if(/巡检|监控|告警/.test(t)) return 'warn';
  if(/mr|审查|review/.test(t)) return 'accent';
  if(/前端|ui|交互/.test(t)) return 'cyan';
  return 'neutral';
}
function issueTitleMarkup(title){
  const tags=[];
  let rest=String(title||'');
  const re=/^\s*(?:\[([^[\]【】]{1,24})\]|【([^[\]【】]{1,24})】)\s*/;
  let m;
  while((m=rest.match(re))){tags.push(m[1]||m[2]);rest=rest.slice(m[0].length)}
  rest=rest.trim()||title||'';
  const pills=tags.map(t=>`<span class="issue-title-tag tone-${issueTagTone(t)}" title="${escapeHTML(t)}">${escapeHTML(t)}</span>`).join('');
  return `${pills}<b title="${escapeHTML(title||'')}">${escapeHTML(rest)}</b>`;
}
function issueListProgress(i,run,w){
  const raw=run?runProgress(run):{done:0,total:w?.nodes?.length||0,pct:0};
  const forgedClose=deliveryLocked(i)&&String(i?.deliveryMrState||'').toLowerCase()==='closed'&&!deliveryMRLinked(i);
  if((deliveryLocked(i)&&!forgedClose)||gongfengIssueClosed(i)||run?.status==='succeeded'){
    const total=raw.total||w?.nodes?.length||Object.keys(run?.nodes||{}).length||raw.done||0;
    return {done:total||raw.done,total:total||raw.done,pct:100};
  }
  return raw;
}
function issueRow(i){
  const {project:p,run,workflow:w}=issueExecution(i),boundRepos=(state.data.repositories||[]).filter(x=>(i.repositoryIds||[]).includes(x.id)||x.id===i.repositoryId||(!((i.repositoryIds||[]).length||i.repositoryId)&&x.id===p?.defaultRepositoryId)),issueRepos=[boundRepos.find(x=>x.id===i.repositoryId),...boundRepos.filter(x=>x.id!==i.repositoryId)].filter(Boolean),repo=issueRepos[0],repoLabel=issueRepos.length>1?`${repo?.name||'未绑定'} 等${issueRepos.length}个`:repo?.name||'未绑定',health=issueRunHealth(i),stage=issueCurrentStage(i),progress=issueListProgress(i,run,w);
  const sop=issueSOPLabel(i);
  const laneDots=issueLaneDots(i);
  const isFailed=Boolean(run&&(['failed','cancelled'].includes(run.status)||issueHasFailedNode(run)));
  const needsConfirm=clarificationNeedsConfirm(i);
  const needsClarify=requirementNeedsClarification(i);
  let progressText=`${progress.pct}%`;
  let progressCls='';
  let progressTitle=`${progress.done}/${progress.total||0} 节点`;
  let trackWidth=progress.pct;
  if(isFailed){
    progressText='失败';
    progressCls=' fail';
    trackWidth=0;
    progressTitle=`门禁或脚本执行失败（${progress.done}/${progress.total||0} 节点）`;
  }else if(needsConfirm){
    progressText='需要确认结论';
    progressCls=' confirm';
    trackWidth=Math.min(100,Math.max(20,i.clarification?.score||100));
    progressTitle=`澄清已完成（${i.clarification?.score||80} 分），需要确认结论后再触发 SOP`;
  }else if(needsClarify){
    progressText='待澄清';
    progressCls=' warn';
    trackWidth=0;
    progressTitle=`信息完整度 ${i.clarification?.score||0} 分，未达准入线，需先澄清`;
  }
  const rowTone=needsConfirm?' needs-confirm':(isFailed?' needs-attention':'');
  const owner=issueOwnerSummary(i);
  const pri=i.priority||'medium';
  return `<div class="issue-table-row${rowTone}" role="row" data-id="${escapeHTML(i.id)}"><div class="issue-key-cell" title="${escapeHTML(String(issueKeyLabel(i)||''))}">${issueKeyMarkup(i)}</div><div class="issue-title-cell">${issueTitleMarkup(i.title)}</div><div class="issue-assignee-cell" title="${escapeHTML(owner)}">${escapeHTML(owner)}</div><div class="issue-repository-cell" title="${escapeHTML(issueRepos.map(r=>r.name).join('、')||'未绑定')} · ${escapeHTML(i.baseBranch||repo?.defaultBranch||'')}">${escapeHTML(repoLabel)}</div><div class="issue-priority-cell"><span class="issue-priority ${escapeHTML(pri)}">${issuePriorityLabel(pri)}</span></div><div class="issue-workflow-cell" title="${escapeHTML(sop)}">${escapeHTML(sop)}${laneDots}</div><div class="issue-stage-cell ${stage.key}" title="${escapeHTML(stage.hint)}"><span class="issue-stage-pill">${escapeHTML(stage.label)}</span></div><div class="issue-progress-cell${progressCls}" title="${escapeHTML(progressTitle)}"><b>${escapeHTML(progressText)}</b><span class="issue-progress-track"><i style="width:${trackWidth}%"></i></span></div><div class="issue-actions-cell">${issueActionButtons(i)}</div></div>`;
}

let refreshIssuesPending=null;
async function refreshIssues(){if(refreshIssuesPending)return refreshIssuesPending;refreshIssuesPending=(async()=>{try{const opt={silent:true};const [issues,tasks,runs]=await Promise.all([api('/api/issues',opt),api('/api/tasks',opt),api('/api/runs',opt)]);state.data.issues=issues;state.data.tasks=tasks;state.data.runs=runs;renderIssues();if(state.drawerOpen&&state.issueDetailData?.issue?.id){const curId=state.issueDetailData.issue.id,updatedIssue=issues.find(i=>i.id===curId);if(updatedIssue){const curTask=latestIssueTask(updatedIssue),curRunId=curTask?.runId||latestIssueRun(updatedIssue,curTask)?.id,curRun=curRunId?(runs||[]).find(r=>r.id===curRunId):null;const keepClarifyStream=state.issueDetailTab==='clarify'&&(state.issueDetailData.issue.clarification?.status||'')==='analyzing'&&(updatedIssue.clarification?.status||'')==='analyzing';state.issueDetailData.issue=updatedIssue;state.issueDetailData.task=curTask;if(curRun){const previous=state.issueDetailData.run;state.issueDetailData.run=previous?.id===curRun.id&&previous.workflowSnapshot?.nodes?.length?{...previous,...curRun,workflowSnapshot:previous.workflowSnapshot}:curRun;}try{const snap=await api(`/api/issues/${encodeURIComponent(curId)}/requirement`,opt);if(snap)state.issueDetailData.requirement=snap}catch(e){}if(!keepClarifyStream)renderIssueDetailTab()}}}catch(e){toast(e.message)}finally{refreshIssuesPending=null}})();return refreshIssuesPending}
function fillIssueRepositories(projectId,selected){
  const f=$('#issueForm'),project=(state.data.projects||[]).find(p=>p.id===projectId),repos=(state.data.repositories||[]).filter(r=>r.projectId===projectId),box=$('#issueRepoChecks');
  if(!box)return;
  const def=project?.defaultRepositoryId;
  const sel=Array.isArray(selected)?selected:(selected?[selected]:repos.map(r=>r.id));
  box.innerHTML=repos.map(r=>{
    const checked=sel.includes(r.id)?' checked':'';
    return `<label class="repo-check"><input type="checkbox" value="${escapeHTML(r.id)}"${checked}><span>${escapeHTML(r.name)} · ${escapeHTML(r.defaultBranch)}${r.id===def?'（默认）':''}</span></label>`;
  }).join('');
}
function selectedIssueRepositoryIds(){
  return [...document.querySelectorAll('#issueRepoChecks input[type="checkbox"]:checked')].map(i=>i.value).filter(Boolean);
}
// Older records stored the configured main branch in baseBranch automatically.
// Apply compatibility only when reading saved issues, never to new form input.
function storedIssueWorkBranch(issue){
  const project=(state.data.projects||[]).find(p=>p.id===issue.projectId);
  const ids=[...new Set([...(issue.repositoryIds||[]),issue.repositoryId].filter(Boolean))];
  if(!ids.length&&project?.defaultRepositoryId)ids.push(project.defaultRepositoryId);
  if(ids.length!==1)return '';
  const repo=(state.data.repositories||[]).find(r=>r.id===ids[0]);
  const branch=String(issue.baseBranch||'').trim();
  return branch===String(repo?.defaultBranch||'').trim()?'':branch;
}
function updateIssueBaseBranchInput(input,hint,repositoryIds){
  if(!input)return;
  const repos=(state.data.repositories||[]).filter(r=>(repositoryIds||[]).includes(r.id));
  const ids=JSON.stringify(repos.map(r=>r.id).sort());
  if(repos.length!==1||(input.dataset.repositoryIds&&input.dataset.repositoryIds!==ids))input.value='';
  input.dataset.repositoryIds=ids;
  input.disabled=repos.length!==1;
  const mainBranch=repos.length===1?repos[0].defaultBranch:'';
  input.placeholder=mainBranch?`留空从主分支 ${mainBranch} 新建`:'留空从各仓库配置的主分支新建';
  if(hint)hint.textContent=repos.length!==1?'指定继续开发分支时请选择一个仓库；留空从各仓库配置的主分支分别新建工作分支。':'留空从配置主分支新建工作分支；填写时直接继续该已有分支，不新建。';
}
function refreshIssueBaseBranchInput(){
  updateIssueBaseBranchInput($('#issueForm')?.elements.baseBranch,$('#issueBaseBranchHint'),selectedIssueRepositoryIds());
}
function fillIssueAssignees(projectId,value=''){
  const f=$('#issueForm'),picker=$('#issueAssigneePicker');if(!f||!picker)return;
  const project=(state.data.projects||[]).find(p=>p.id===projectId),selected=Number(value)||Number(state.currentUser?.id)||Number(project?.ownerId)||0;
  picker.innerHTML=userCombobox('assigneeUserId',selected,'使用项目 Owner');
  bindUserSearches(f);
}
function openIssue(issue=null){
  state.selectedIssue=issue?.id||null;
  const f=$('#issueForm'),projects=state.data.projects||[];
  f.elements.projectId.innerHTML=projects.map(p=>`<option value="${p.id}">${escapeHTML(p.name)}</option>`).join('');
  f.elements.workflowId.innerHTML=`<option value="">使用来源默认流程</option>`+runnableWorkflowOptions().map(w=>`<option value="${w.id}">${escapeHTML(w.name)}</option>`).join('');
  const v=issue||{id:'',projectId:state.projectFilter||projects[0]?.id||'',title:'',description:'',priority:'medium',status:'backlog',workflowId:'',repositoryId:'',repositoryIds:[],baseBranch:''};
  ['id','projectId','title','description','priority','status','workflowId','baseBranch','tapdId'].forEach(k=>f.elements[k].value=v[k]||'');
  f.elements.baseBranch.value=storedIssueWorkBranch(v);
  f.elements.automationIgnored.checked=v.automationState==='ignored';
  fillIssueRepositories(v.projectId,(v.repositoryIds&&v.repositoryIds.length)?v.repositoryIds:(v.repositoryId?[v.repositoryId]:null));
  fillIssueAssignees(v.projectId,v.assigneeUserId||'');
  f.elements.workflowId.disabled=false;
  if(!f.elements.workflowId.value)f.elements.workflowId.value=defaultSOPForIssue(v);
  let workflowTouched=Boolean(v.workflowId);
  f.elements.workflowId.onchange=()=>{workflowTouched=true};
  delete f.elements.baseBranch.dataset.repositoryIds;
  refreshIssueBaseBranchInput();
  f.elements.projectId.onchange=()=>{fillIssueRepositories(f.elements.projectId.value);fillIssueAssignees(f.elements.projectId.value,'');f.elements.baseBranch.value='';refreshIssueBaseBranchInput()};
  $('#issueRepoChecks').onchange=refreshIssueBaseBranchInput;
  $('#issueModalTitle').textContent=issue?`${(projects.find(p=>p.id===v.projectId)?.key||'ISS')}-${v.number} ${v.title}`:'新建工作项';
  const task=(state.data.tasks||[]).find(t=>t.id===v.latestTaskId);
  let stateHtml=task?`最近任务 ${escapeHTML(task.id)} · ${taskStatusLabel(task.status)} · Run ${escapeHTML(task.runId)}`:'';
  if(v.automationState==='ignored')stateHtml+=`${stateHtml?'<br>':''}定时 SOP：<b>已忽略</b>`;
  if(v.deliveryState==='merged')stateHtml+=`${stateHtml?'<br>':''}交付状态：<b>${escapeHTML(deliveryLockedLabel(v))}，需要重新打开后才能触发</b>`;
  if(v.externalRef){stateHtml+=`<br>外部来源: <b>${escapeHTML(v.externalRef.source)}</b> · <a href="${escapeHTML(safeURL(v.externalRef.externalUrl))}" target="_blank" rel="noopener noreferrer">${escapeHTML(v.externalRef.externalId)}</a> · 状态: ${escapeHTML(v.externalRef.externalState||'—')}`}
  $('#issueTaskState').innerHTML=stateHtml;
  const reopenBtn=$('#reopenDeliveredIssue'),dispatchBtn=$('#dispatchIssue');
  if(reopenBtn){
    const locked=issue&&deliveryLocked(v);
    reopenBtn.hidden=!locked;
    reopenBtn.onclick=locked?()=>reopenDeliveredIssue(issue.id):null;
  }
  if(dispatchBtn)dispatchBtn.style.display=issue&&!issueExecutionBlocked(v)?'':'none';
  const isNew=!issue?.id,box=$('#issueSyncTargetBox'),note=$('#issueBoundNote');
  const manualBinding=$('#issueManualTapdBinding');
  const showManualBinding=()=>{if(manualBinding)manualBinding.hidden=true};
  if(box){
    box.hidden=true;
    box.querySelectorAll('input[name="syncTarget"]').forEach(el=>{
      el.checked=isNew&&el.value==='local';
      el.required=isNew;
      el.onchange=()=>{showManualBinding();if(!workflowTouched)f.elements.workflowId.value=defaultSOPForIssue({source:el.value,kind:el.value==='gongfeng'?'issue':'requirement'})};
    });
  }
  showManualBinding();
  if(note){note.hidden=isNew||!v.externalRef;const text=$('#issueBoundNoteText');if(text&&v.externalRef){const src=v.externalRef.source==='github'?'GitHub Issue':v.externalRef.source==='tapd'?'TAPD':v.externalRef.source==='gongfeng'?'工蜂 Issue':'外部';const id=v.externalRef.externalIid||v.externalRef.externalId||'';text.innerHTML=`已关联 ${src} <b>${escapeHTML(String(id))}</b>。保存只改本平台记录，不会回写外部单据。`}}
  const submit=document.querySelector('#issueForm button[type="submit"]');
  if(submit)submit.textContent=isNew?'创建需求':'保存';
  $('#issueModal').classList.add('open');
}
function issueFromForm(){
  const f=$('#issueForm'),id=f.elements.id.value,old=canonicalIssue(id);
  const assigneeUserId=Number(f.elements.assigneeUserId?.value)||0;
  const syncTarget=document.querySelector('#issueForm [name="syncTarget"]:checked')?.value||'';
  const source=old.source||(id?'local':syncTarget);
  const workflowId=f.elements.workflowId.value||defaultSOPForIssue({...old,source});
  const repositoryIds=selectedIssueRepositoryIds(),status=f.elements.status.value;
  const closeSource=status==='closed'?(old.status==='closed'?old.closeSource||'manual':'manual'):'';
  return {...old,id,projectId:f.elements.projectId.value,title:f.elements.title.value.trim(),description:f.elements.description.value,priority:f.elements.priority.value,status,closeSource,automationState:f.elements.automationIgnored.checked?'ignored':'eligible',source,workflowId,repositoryId:repositoryIds[0]||'',repositoryIds,baseBranch:f.elements.baseBranch.value.trim(),assigneeUserId,tapdId:source==='local'?f.elements.tapdId.value.trim():'',externalRef:trackerRefOf(old)||old.externalRef||undefined};
}

// ParseGongfengRepoURL 与后端 domain.ParseGongfengRepoURL 保持一致：把粘贴的
// 工蜂链接（web / ssh / scp / 纯项目路径）解析成统一的仓库信息，让"粘一个链接"
// 就能完成仓库配置。
function parseGongfengRepoURL(raw){
  let publicURL=String(raw||'').trim().replace(/^git clone /,'').replace(/^github:/,'https://github.com/').replace(/^git@github.com:/,'https://github.com/');
  if(publicURL.startsWith('github.com/'))publicURL='https://'+publicURL;
  try{const u=new URL(publicURL);if(u.hostname==='github.com'&&!u.username&&!u.password&&['http:','https:'].includes(u.protocol)){const parts=u.pathname.split('/').filter(Boolean),owner=parts[0],name=(parts[1]||'').replace(/\.git$/,'');if(parts.length>=2&&/^[A-Za-z0-9_.-]+$/.test(owner)&&/^[A-Za-z0-9_.-]+$/.test(name)&&!['.','..'].includes(owner)&&!['.','..'].includes(name))return {project:'github:'+owner+'/'+name,name,webUrl:'https://github.com/'+owner+'/'+name,cloneUrl:'https://github.com/'+owner+'/'+name+'.git',provider:'github'}}}catch(_){}

  let original=String(raw||'').trim().replace(/^['"]+|['"]+$/g,'');
  if(!original)return null;
  const strippedClone=/^git\s+clone\s+/i.test(original);
  let s=original.replace(/^git\s+clone\s+/i,'').trim().replace(/^['"]+|['"]+$/g,'');
  if(strippedClone&&/\s/.test(s))s=s.split(/\s+/)[0];
  if(!s||/[\s"'<>()\\]/.test(s))return null;
  const ssh=/^(git@|ssh:\/\/)/i.test(s);
  s=s.replace(/\/+$/,'');
  const hadScheme=s.includes('://'),hadAt=s.includes('@');
  let rest=s;
  if(hadScheme)rest=rest.slice(rest.indexOf('://')+3);
  if(rest.includes('@'))rest=rest.slice(rest.lastIndexOf('@')+1);
  let path='';
  if(!hadScheme&&!hadAt&&!rest.includes(':')){
    path=rest;
  }else if(rest.includes('/')){
    const cut=rest.search(/[:\/]/),host=rest.slice(0,cut);
    if(host.toLowerCase()!=='git.example.com')return null;
    path=rest.slice(cut+1);
    if(rest[cut]===':'){const j=path.indexOf('/');if(j>=0&&/^\d+$/.test(path.slice(0,j)))path=path.slice(j+1)}
  }else return null;
  path=path.replace(/^git\.example\.com\//i,'').replace(/\.git$/i,'');
  for(const marker of ['/-/','/tree/','/blob/','/commit/','/commits/','/merge_requests/','/issues/','/snippets/','/wikis/','/releases/','/tags/']){
    const i=path.indexOf(marker);if(i>=0)path=path.slice(0,i);
  }
  path=path.replace(/^\/+|\/+$/g,'');
  if(!path||path.includes('..')||path.split('/').some(seg=>!seg||/[\s\\?#]/.test(seg)))return null;
  const name=path.slice(path.lastIndexOf('/')+1);
  return {project:path,name,webUrl:`https://git.example.com/${path}`,cloneUrl:ssh?`git@git.example.com:${path}.git`:`https://git.example.com/${path}.git`};
}
// fillRepositoryFromLink 把解析结果填进空缺字段；显式填过的字段不覆盖。
function fillRepositoryFromLink(url,fill){
  const info=parseGongfengRepoURL(url);
  if(!info)return false;
  fill.gongfengProjectId=info.project;
  if(!String(fill.name||'').trim())fill.name=info.name;
  fill.webUrl=info.webUrl;
  fill.cloneUrl=info.cloneUrl;
  return true;
}
function renderRepositoryCredentialHint(){
  const host=$('#repositoryCredentialHint');if(!host)return;
  const configured=!!(userById(state.currentUser?.id)||{}).gitTokenConfigured;
  const title=configured?'已配置 GitHub Token':'仓库可以先保存，代码任务还需要 Token';
  const message=configured?'请确认 Token 已获得目标仓库的访问和写入权限。SOP 使用需求责任人的 Token，未指定时使用项目 Owner 的 Token。':'使用仓库进行需求分析、访问私有代码或提交 PR 前，请先配置 GitHub Token。SOP 使用需求责任人的 Token，未指定时使用项目 Owner 的 Token。';
  host.dataset.configured=String(configured);
  host.innerHTML=`<div><b>${escapeHTML(title)}</b><p>${escapeHTML(message)}</p></div><button type="button" class="button ghost small" data-repository-configure-token>${configured?'个人设置':'配置 GitHub Token'}</button>`;
  host.querySelector('[data-repository-configure-token]').onclick=()=>{
    $('#repositoryModal').classList.remove('open');switchView('settings');
    const field=document.querySelector('#userCredentialForm [name="gitToken"]');
    field?.focus();field?.scrollIntoView({block:'center'});
  };
}
function renderRepositoryList(){renderRepositoryCredentialHint();const project=(state.data.projects||[]).find(p=>p.id===state.projectFilter),repos=(state.data.repositories||[]).filter(r=>r.projectId===state.projectFilter);$('#repositoryProjectHint').textContent=`当前项目：${project?.name||'—'}。粘贴任意一种 GitHub 地址即可；集成分支用于检查多个 SOP 执行后是否存在合并冲突，可留空。`;$('#repositoryList').innerHTML=repos.map(r=>`<button type="button" data-edit-repo="${escapeHTML(r.id)}"><b>${escapeHTML(r.name)}</b><span>${escapeHTML(r.gongfengProjectId)} · 主分支 ${escapeHTML(r.defaultBranch||'master')} · 集成分支 ${escapeHTML(r.integrateBranch||'未配置')}</span><em>${project?.defaultRepositoryId===r.id?'默认仓库':'编辑'}</em></button>`).join('')||'<p>当前项目还没有绑定代码仓库。</p>';$('#repositoryList').querySelectorAll('[data-edit-repo]').forEach(b=>b.onclick=()=>fillRepositoryForm((state.data.repositories||[]).find(r=>r.id===b.dataset.editRepo)))}
function fillRepositoryForm(repo={}){const f=$('#repositoryForm');['id','name','gongfengProjectId','webUrl','cloneUrl','localPath'].forEach(k=>{if(f.elements[k])f.elements[k].value=repo[k]||''});if(f.elements.repoUrl)f.elements.repoUrl.value=repo.webUrl||repo.cloneUrl||repo.gongfengProjectId||'';
  const nameField=$('#repositoryNameField');if(nameField)nameField.hidden=!repo.id;f.elements.defaultBranch.value=repo.defaultBranch||'master';f.elements.integrateBranch.value=repo.integrateBranch||'';const project=(state.data.projects||[]).find(p=>p.id===state.projectFilter);f.elements.makeDefault.checked=!project?.defaultRepositoryId||project.defaultRepositoryId===repo.id;if(f.elements.repoUrl)f.elements.repoUrl.onchange=()=>{const form=f.elements,fill={name:form.name.value,gongfengProjectId:form.gongfengProjectId.value,webUrl:form.webUrl.value,cloneUrl:form.cloneUrl.value};if(fillRepositoryFromLink(form.repoUrl.value,fill)){form.name.value=fill.name;form.gongfengProjectId.value=fill.gongfengProjectId;form.webUrl.value=fill.webUrl;form.cloneUrl.value=fill.cloneUrl}}}
function openRepositoryModal(){if(!state.projectFilter)return toast('请先选择项目');fillRepositoryForm();renderRepositoryList();$('#repositoryModal').classList.add('open')}
async function saveRepository(e){e.preventDefault();const f=e.currentTarget,project=(state.data.projects||[]).find(p=>p.id===state.projectFilter);if(f.elements.repoUrl){const fill={name:f.elements.name.value,gongfengProjectId:f.elements.gongfengProjectId.value,webUrl:f.elements.webUrl.value,cloneUrl:f.elements.cloneUrl.value};if(fillRepositoryFromLink(f.elements.repoUrl.value,fill)){f.elements.name.value=fill.name;f.elements.gongfengProjectId.value=fill.gongfengProjectId;f.elements.webUrl.value=fill.webUrl;f.elements.cloneUrl.value=fill.cloneUrl}}const payload={id:f.elements.id.value,projectId:state.projectFilter,provider:(parseGongfengRepoURL(f.elements.repoUrl?.value)?.provider||'gongfeng'),name:f.elements.name.value.trim(),gongfengProjectId:f.elements.gongfengProjectId.value.trim(),webUrl:f.elements.webUrl.value.trim(),cloneUrl:f.elements.cloneUrl.value.trim(),defaultBranch:f.elements.defaultBranch.value.trim(),integrateBranch:f.elements.integrateBranch.value.trim(),localPath:f.elements.localPath.value.trim(),repoUrl:f.elements.repoUrl?f.elements.repoUrl.value.trim():''};try{const repo=await api('/api/repositories',{method:payload.id?'PUT':'POST',body:JSON.stringify(payload)}),idx=(state.data.repositories||[]).findIndex(r=>r.id===repo.id);if(idx>=0)state.data.repositories[idx]=repo;else(state.data.repositories||(state.data.repositories=[])).push(repo);if(f.elements.makeDefault.checked&&project){const saved=await api('/api/projects',{method:'PUT',body:JSON.stringify({...project,defaultRepositoryId:repo.id})});if(saved&&saved.id){const pi=(state.data.projects||[]).findIndex(p=>p.id===saved.id);if(pi>=0)state.data.projects[pi]=saved;project.defaultRepositoryId=repo.id}else project.defaultRepositoryId=repo.id}fillRepositoryForm(repo);renderRepositoryList();renderIssues();toast((userById(state.currentUser?.id)||{}).gitTokenConfigured?'Git 仓库绑定已保存':'仓库已保存；配置 GitHub Token 后才能进行代码交付。')}catch(err){toast(err.message)}}
function fillImportTAPDWorkflow(){
  const select=$('#importTAPDWorkflow');
  if(!select)return;
  const opts=runnableWorkflowOptions();
  const selected=defaultSOPForIssue({projectId:state.projectFilter,source:'tapd',kind:'requirement'});
  const chosen=opts.some(w=>w.id===selected)?selected:(opts[0]?.id||'');
  select.innerHTML=opts.map(w=>`<option value="${escapeHTML(w.id)}" ${w.id===chosen?'selected':''}>${escapeHTML(w.name)}</option>`).join('')||'<option value="">暂无可选 SOP</option>';
}
function fillImportTAPDRepository(){
  const host=$('#importTAPDRepository');
  if(!host)return;
  host.dataset.ids='[]';
  renderImportTAPDRepositoryPicker();
}
function selectedImportTAPDRepositoryIds(){
  try{return JSON.parse($('#importTAPDRepository')?.dataset.ids||'[]')}catch{return []}
}
function setImportTAPDRepositoryIds(ids){
  const host=$('#importTAPDRepository');
  if(!host)return;
  const repos=(state.data.repositories||[]).filter(r=>r.projectId===state.projectFilter);
  const known=new Set(repos.map(r=>r.id));
  host.dataset.ids=JSON.stringify([...new Set((ids||[]).filter(id=>known.has(id)))]);
  renderImportTAPDRepositoryPicker();
}
function renderImportTAPDRepositoryPicker(){
  const host=$('#importTAPDRepository');
  if(!host)return;
  const repos=(state.data.repositories||[]).filter(r=>r.projectId===state.projectFilter);
  const selected=selectedImportTAPDRepositoryIds();
  const chosen=new Set(selected);
  const leftover=repos.filter(r=>!chosen.has(r.id));
  const tags=selected.map(id=>{
    const name=repos.find(r=>r.id===id)?.name||id;
    return `<button type="button" class="repo-picker-tag" data-remove-repo="${escapeHTML(id)}">${escapeHTML(name)}<span aria-hidden="true">×</span></button>`;
  }).join('');
  const options=leftover.map(r=>`<option value="${escapeHTML(r.id)}">${escapeHTML(r.name)}</option>`).join('');
  const placeholder=!repos.length?'暂无可选仓库':(!leftover.length?'已全部选择':'选择仓库');
  host.innerHTML=`<div class="repo-picker-field">${tags}<select class="repo-picker-select"${leftover.length?'':' disabled'}><option value="">${placeholder}</option>${options}</select></div>`;
  host.querySelectorAll('[data-remove-repo]').forEach(b=>b.onclick=()=>setImportTAPDRepositoryIds(selected.filter(id=>id!==b.dataset.removeRepo)));
  const sel=host.querySelector('select');
  if(sel)sel.onchange=()=>{if(!sel.value)return;setImportTAPDRepositoryIds([...selected,sel.value])};
  updateIssueBaseBranchInput($('#importTAPDBaseBranch'),$('#importTAPDBaseBranchHint'),selected);
}
function openImportTAPDModal(){
  if(!state.projectFilter)return toast('请先在顶部选择一个项目');
  const input=$('#importTAPDURL');if(input)input.value='';
  const baseBranch=$('#importTAPDBaseBranch');if(baseBranch)baseBranch.value='';
  fillImportTAPDWorkflow();
  fillImportTAPDRepository();
  $('#importTAPDModal').classList.add('open');
  setTimeout(()=>input?.focus(),0);
}
async function importTAPDStory(e){
  e.preventDefault();
  const url=$('#importTAPDURL')?.value.trim();
  if(!url)return toast('请粘贴 TAPD 工作项链接或分享文本');
  if(!state.projectFilter)return toast('请先选择项目');
  const repositoryIds=selectedImportTAPDRepositoryIds();
  if(!repositoryIds.length)return toast('请至少选择一个代码仓库');
  const button=$('#confirmImportTAPD');
  try{
    if(button){button.disabled=true;button.textContent='正在导入…'}
    const workflowId=$('#importTAPDWorkflow')?.value.trim();
    const baseBranch=$('#importTAPDBaseBranch')?.value.trim()||'';
    const issue=await api('/api/issues/import-tapd',{method:'POST',body:JSON.stringify({projectId:state.projectFilter,url,workflowId:workflowId||undefined,repositoryIds,repositoryId:repositoryIds[0],baseBranch})});
    const idx=(state.data.issues||[]).findIndex(i=>i.id===issue.id);
    if(idx>=0)state.data.issues[idx]=issue;else (state.data.issues||(state.data.issues=[])).unshift(issue);
    $('#importTAPDModal').classList.remove('open');
    renderIssues();
    if(requirementNeedsClarification(issue)){
      state.autoStartClarify=true;
      toast(`已导入：${issue.title||issue.id}。评分 ${issue.clarification?.score||0} 分，需要你补充澄清`,'ok');
      openIssueDetail(issue.id,'clarify');
    }else{
      toast(`已导入 TAPD 工作项：${issue.title||issue.id}`,'ok');
      if(issue.id)openIssueDetail(issue.id);
    }
  }catch(err){toast(err.message)}
  finally{if(button){button.disabled=false;button.textContent='导入'}}
}
function canonicalIssue(id){
  id=String(id||'').trim();
  if(!id)return {};
  return (state.data.issues||[]).find(i=>i.id===id)||(state.issueDetailData?.issue?.id===id?state.issueDetailData.issue:{})||{};
}
function trackerRefOf(issue){
  const ref=issue?.externalRef;
  if(ref&&(ref.externalId||ref.externalIid))return ref;
  return undefined;
}
async function persistIssue(issue){
  const id=String(issue.id||'').trim();
  const prior=canonicalIssue(id);
  const payload={...prior,...issue,id};
  if(prior.source)payload.source=prior.source;
  const bound=trackerRefOf(prior)||trackerRefOf(payload);
  if(bound)payload.externalRef=bound;
  else if(payload.externalRef&&!(payload.externalRef.externalId||payload.externalRef.externalIid))delete payload.externalRef;
  delete payload.syncTarget;
  const isNew=!id;
  if(isNew){
    const target=issue.syncTarget||document.querySelector('#issueForm [name="syncTarget"]:checked')?.value;
    if(!target)throw new Error('新建必须选择创建方式');
    payload.syncTarget=target;
  }
  const saved=await api('/api/issues',{method:isNew?'POST':'PUT',body:JSON.stringify(payload)});
  rememberIssue(saved, id);
  return saved;
}
async function saveIssue(e){e.preventDefault();const button=e.currentTarget.querySelector('button[type="submit"]');try{button.disabled=true;button.textContent='正在保存…';const saved=await persistIssue(issueFromForm());state.selectedIssue=saved.id;$('#issueModal').classList.remove('open');renderIssues();toast('需求已保存')}catch(err){toast(err.message)}finally{button.disabled=false;button.textContent='保存'}}
async function dispatchIssue(){try{let issue=issueFromForm();const blocked=issueExecutionBlocked(issue);if(blocked)return toast(blocked);const current=latestIssueTask(issue),rerun=!!current;if(current&&!await afConfirm({title:'重新运行确认',message:['queued','dispatched','running'].includes(current.status)?'该需求已有进行中的任务，确定额外发起一次重新运行吗？':'该需求已经处理过，确定整条 SOP 重新运行吗？',confirmText:'确认重新运行'}))return;const button=$('#dispatchIssue');button.disabled=true;button.textContent='正在调度…';issue=await persistIssue(issue);const task=await api(`/api/issues/${issue.id}/dispatch`,{method:'POST',body:JSON.stringify({rerun,workflowId:issue.workflowId||''})});issue.latestTaskId=task.id;issue.status='in_progress';const idx=state.data.issues.findIndex(i=>i.id===issue.id);if(idx>=0)state.data.issues[idx]=issue;if(!state.data.tasks)state.data.tasks=[];const existingTaskIdx=state.data.tasks.findIndex(t=>t.id===task.id);if(existingTaskIdx>=0)state.data.tasks[existingTaskIdx]=task;else state.data.tasks.unshift(task);if(task.runId){startLiveRun(task.runId);try{const run=await api(`/api/runs/${encodeURIComponent(task.runId)}`,{silent:true});if(run?.id){if(!state.data.runs)state.data.runs=[];const rIdx=state.data.runs.findIndex(r=>r.id===run.id);if(rIdx>=0)state.data.runs[rIdx]={...state.data.runs[rIdx],...run};else state.data.runs=sortRunsDescending([run,...state.data.runs]);}}catch(e){}}$('#issueModal').classList.remove('open');renderIssues();await openIssueDetail(issue.id,'sop');toast(rerun?'已创建重新运行':'已创建执行任务并启动工作流','ok');Promise.all([refreshIssues(),refreshRuns(true)]).catch(()=>{});}catch(err){toast(err.message)}finally{const button=$('#dispatchIssue');button.disabled=false;button.textContent='启动 SOP'}}
async function dispatchIssueById(issueId,rerun=false,workflowId=''){try{const issue=(state.data.issues||[]).find(i=>i.id===issueId),blocked=issueExecutionBlocked(issue);if(blocked)return toast(blocked);if(clarificationNeedsConfirm(issue)){openIssueDetail(issueId,'clarify');return toast('请先确认澄清结论后再触发 SOP')}
    if(requirementNeedsClarification(issue)){state.autoStartClarify=true;openIssueDetail(issueId,'clarify');return toast('需求澄清未达标，请先补充关键信息')}const task=await api(`/api/issues/${issueId}/dispatch`,{method:'POST',body:JSON.stringify({rerun,workflowId})});if(issue){issue.latestTaskId=task.id;issue.status='in_progress'}if(!state.data.tasks)state.data.tasks=[];const existingTaskIdx=state.data.tasks.findIndex(t=>t.id===task.id);if(existingTaskIdx>=0)state.data.tasks[existingTaskIdx]=task;else state.data.tasks.unshift(task);if(task.runId){startLiveRun(task.runId);try{const run=await api(`/api/runs/${encodeURIComponent(task.runId)}`,{silent:true});if(run?.id){if(!state.data.runs)state.data.runs=[];const rIdx=state.data.runs.findIndex(r=>r.id===run.id);if(rIdx>=0)state.data.runs[rIdx]={...state.data.runs[rIdx],...run};else state.data.runs=sortRunsDescending([run,...state.data.runs]);}}catch(e){}}renderIssues();await openIssueDetail(issueId,'sop');toast(rerun?'已创建重新运行':'已启动 SOP','ok');Promise.all([refreshIssues(),refreshRuns(true)]).catch(()=>{});setTimeout(()=>refreshIssues(),2000);setTimeout(()=>refreshIssues(),10000);}catch(err){if(/澄清/.test(err.message||'')){state.issueDetailTab='clarify';openIssueDetail(issueId,'clarify')}toast(err.message,'err')}}
function defaultSOPForIssue(issue){
  const projectId=issue?.projectId||state.projectFilter;
  const project=(state.data.projects||[]).find(p=>p.id===projectId);
  const projectSOP=(project?.workflowId||'').trim();
  const runnable=runnableWorkflowOptions();
  const inRunnable=id=>id&&runnable.some(w=>w.id===id);
  const knownSOP=id=>id&&(state.data.workflows||[]).some(w=>w.id===id);
  const projectFix=projectId?`${projectId}-issue-fix`:'';
  // 项目默认 SOP 是平台通用模板（如新建项目自带的通用后端交付 SOP）时，
  // 工蜂缺陷也默认走它；绑定生产 SOP 的旧项目保持原默认不变。
  const projectSOPDef=(state.data.workflows||[]).find(w=>w.id===projectSOP);
  const genericSOP=!!projectSOPDef&&!!projectSOPDef.generic;
  if(issue?.source==='gongfeng'||issue?.kind==='issue'){
    if(projectFix&&knownSOP(projectFix))return projectFix;
    if(genericSOP)return projectSOP;
    return inRunnable('issue-fix-production')?'issue-fix-production':(runnable[0]?.id||'issue-fix-production');
  }
  const playbook=projectPlaybook(project);
  if(playbook?.bindings?.[0]&&(inRunnable(playbook.bindings[0].workflowId)||knownSOP(playbook.bindings[0].workflowId)))return playbook.bindings[0].workflowId;
  if(inRunnable(projectSOP))return projectSOP;
  if(projectSOP&&knownSOP(projectSOP))return projectSOP;
  return inRunnable('requirement-development-production')?'requirement-development-production':(runnable[0]?.id||'requirement-development-production');
}
function bindIssueTriggers(root=document){root.querySelectorAll('[data-trigger-issue]').forEach(button=>{button.onclick=e=>{e.stopPropagation();e.preventDefault();triggerIssue(button.dataset.triggerIssue)}})}
async function resumeRun(runId,fromEl){
  if(!runId)return toast('没有可继续的运行');
  const issue=(state.data.issues||[]).find(i=>i.id===issueIdForRun(runId));
  const blocked=issueExecutionBlocked(issue);if(blocked)return toast(blocked);
  const run=cachedRun(runId);
  if(queuedRunActionBlocked(runId))return;
  if(run?.status==='cancelled')return toast('已取消的运行不能重试');
  let context=readHumanContext(fromEl);
  if(!context && !fromEl?.closest?.('[data-human-context-root]') && runRetryNeedsContext(run)){
    const typed=await afPrompt({title:'补充执行上下文',message:'可补充上下文后再从失败节点继续（例如单测失败用例名）。留空则直接重试，取消则中止。',placeholder:'例如：修复某用例或调整依赖',defaultValue:''});
    if(typed===null)return;
    context=typed.trim();
  }
  try{
    await api(`/api/runs/${encodeURIComponent(runId)}/retry`,{method:'POST',body:JSON.stringify(withHumanContext({reason:'从中断处继续',context}))});
    const current=await syncRunControlState(runId);
    toast(runControlToast(current,'已从失败节点继续，已完成的步骤不会重跑'));
    Promise.all([refreshIssues(),refreshRuns(true)]).catch(()=>{});
    const issueId=issueIdForRun(runId);
    if(state.drawerOpen&&issueId)openIssueDetail(issueId,'sop');
  }catch(err){toast(err.message)}
}
async function triggerIssue(issueId,workflowId){
  const issue=(state.data.issues||[]).find(i=>i.id===issueId);
  if(!issue)return toast('未找到工作项');
  const blocked=issueExecutionBlocked(issue);if(blocked)return toast(blocked);
  if(requirementNeedsClarification(issue)){
    state.autoStartClarify=true;
    openIssueDetail(issue.id,'clarify');
    return toast('需求澄清未达标，请先补充关键信息');
  }
  const targetWorkflow=String(workflowId||'').trim()||issue.workflowId||defaultSOPForIssue(issue);
  const laneTask=(state.data.tasks||[]).find(t=>t.issueId===issue.id&&t.workflowId===targetWorkflow);
  const latestRun=laneTask?latestIssueRun(issue,laneTask):null;
  if(laneTask&&['queued','dispatched','running'].includes(laneTask.status)&&!await afConfirm({title:'发起新运行确认',message:'这条 SOP 已有进行中的任务，确定再次发起一个新运行吗？',confirmText:'确认发起'}))return;
  if(['failed','cancelled','succeeded'].includes(latestRun?.status)&&!await afConfirm({title:'触发整条 SOP 确认',message:'「触发」会把这条 SOP 从头再跑，已完成的步骤也会重做。要从失败节点继续请用「重试」。确定触发？',confirmText:'确认触发'}))return;
  try{
    await dispatchIssueById(issue.id,!!laneTask,targetWorkflow);
  }catch(err){toast(err.message)}
}
async function saveIssueSOPS(issueId,bindings){
  const issue=await api(`/api/issues/${encodeURIComponent(issueId)}/sops`,{method:'PUT',body:JSON.stringify({bindings})});
  const idx=(state.data.issues||[]).findIndex(i=>i.id===issue.id);
  if(idx>=0)state.data.issues[idx]=issue;
  if(state.issueDetailData?.issue?.id===issue.id){
    state.issueDetailData.issue=issue;
    try{const snap=await api(`/api/issues/${encodeURIComponent(issue.id)}/requirement`,{silent:true});if(snap)state.issueDetailData.requirement=snap}catch(e){}
    renderIssueDetailTab();
  }
  renderIssues();
  return issue;
}
function currentIssueSOPBindings(issueId){
  const issue=(state.data.issues||[]).find(i=>i.id===issueId)||state.issueDetailData?.issue;
  return effectiveIssueSOPBindings(issue).map((b,i)=>({id:b.id||('sop-'+b.workflowId),workflowId:b.workflowId,order:i+1,assigneeUserId:Number(b.assigneeUserId)||0,handoff:b.handoff||[],dependsOn:b.dependsOn||[]}));
}
async function addIssueSOP(issueId,workflowId){
  workflowId=String(workflowId||'').trim();
  if(!workflowId)return toast('请选择要绑定的 SOP');
  const bindings=currentIssueSOPBindings(issueId);
  if(bindings.some(b=>b.workflowId===workflowId))return toast('这条 SOP 已经绑定');
  bindings.push({workflowId,order:bindings.length+1,assigneeUserId:0,handoff:[]});
  try{await saveIssueSOPS(issueId,bindings);toast('已加上','ok')}catch(err){toast(err.message)}
}
async function removeIssueSOP(issueId,workflowId){
  const bindings=currentIssueSOPBindings(issueId).filter(b=>b.workflowId!==workflowId);
  try{await saveIssueSOPS(issueId,bindings)}catch(err){toast(err.message)}
}
async function moveIssueSOP(issueId,workflowId,dir){
  const bindings=currentIssueSOPBindings(issueId);
  const idx=bindings.findIndex(b=>b.workflowId===workflowId);
  const next=idx+dir;
  if(idx<0||next<0||next>=bindings.length)return;
  const swap=bindings[idx];bindings[idx]=bindings[next];bindings[next]=swap;
  try{await saveIssueSOPS(issueId,bindings)}catch(err){toast(err.message)}
}
async function setIssueSOPOwner(issueId,workflowId,userId){
  const bindings=currentIssueSOPBindings(issueId).map(b=>b.workflowId===workflowId?{...b,assigneeUserId:Number(userId)||0}:b);
  try{await saveIssueSOPS(issueId,bindings)}catch(err){toast(err.message)}
}
async function setIssueSOPHandoff(issueId,workflowId,raw){
  const handoff=String(raw||'').split(/[,，]/).map(s=>s.trim()).filter(Boolean);
  const bindings=currentIssueSOPBindings(issueId).map(b=>b.workflowId===workflowId?{...b,handoff}:b);
  try{await saveIssueSOPS(issueId,bindings)}catch(err){toast(err.message)}
}
async function retriggerIssue(issueId,workflowId){
  const issue=(state.data.issues||[]).find(i=>i.id===issueId)||state.issueDetailData?.issue;
  if(!issue)return toast('未找到工作项');
  if(issueTrackerClosed(issue))return toast('外部工作项已关闭，请先在外部系统重新打开');
  if(issueManuallyClosed(issue))return toast('请先重新打开该需求，再触发 SOP');
  if(requirementNeedsClarification(issue)||clarificationNeedsConfirm(issue)){
    openIssueDetail(issue.id,'clarify');
    return toast('请先完成澄清再重新触发');
  }
  if(!await afConfirm({title:'重新触发确认',message:'会整条 SOP 从头再开一条新运行，已完成步骤也会重做，旧记录还在。失败时若只想从失败点继续，请用「重试」。',confirmText:'确认重新触发'}))return;
  try{
    let target=issue;
    if(deliveryLocked(target))target=await reopenDeliveredIssueSilent(target.id);
    const wf=String(workflowId||'').trim()||target.workflowId||defaultSOPForIssue(target);
    if(!String(target.workflowId||'').trim())target=await persistIssue({...target,workflowId:wf});
    const ran=!!(state.data.tasks||[]).some(t=>t.issueId===target.id&&t.workflowId===wf);
    await dispatchIssueById(target.id,ran,wf);
  }catch(err){toast(err.message)}
}
async function reopenDeliveredIssueSilent(issueId){
  const issue=await api(`/api/issues/${encodeURIComponent(issueId)}/reopen`,{method:'POST',body:'{}'});
  const idx=(state.data.issues||[]).findIndex(i=>i.id===issue.id);
  if(idx>=0)state.data.issues[idx]=issue;
  return issue;
}
async function reopenDeliveredIssue(issueId){
  if(!await afConfirm({title:'重新打开确认',message:'重新打开后，该工作项可以再次触发 SOP。确定继续？',confirmText:'确认重新打开'}))return;
  try{
    const issue=await reopenDeliveredIssueSilent(issueId);
    renderIssues();
    $('#issueModal')?.classList.remove('open');
    if(state.drawerOpen&&state.selectedIssue===issue.id)openIssueDetail(issue.id);
    toast('工作项已重新打开，可以再次触发 SOP');
  }catch(err){toast(err.message)}
}
function canRerunFromNode(r){
  if(!r||gongfengIssueClosed(runContext(r).issue))return false;
  return ['waiting','failed','cancelled','succeeded'].includes(r.status)&&
    !Object.values(r.nodes||{}).some(n=>['running','pending_input'].includes(n?.status))&&
    runNodeChoices(r).some(n=>r.nodes?.[n.id]);
}
function canRerunFromHere(r,nodeId){
  return !!nodeId&&canRerunFromNode(r)&&runNodeChoices(r).some(node=>node.id===nodeId);
}
function runNodeChoices(r){
  const wf=r.workflowSnapshot?.id?r.workflowSnapshot:((state.data.workflows||[]).find(w=>w.id===r.workflowId)||{});
  const defs=wf.nodes||[];
  const seen=new Set();
  const out=[];
  const upstreamReady=id=>{
    const visited=new Set([id]);
    const queue=[id];
    while(queue.length){
      const target=queue.shift();
      for(const edge of wf.edges||[]){
        if(edge.target!==target||visited.has(edge.source))continue;
        if(r.nodes?.[edge.source]?.status!=='succeeded')return false;
        visited.add(edge.source);
        queue.push(edge.source);
      }
    }
    return true;
  };
  for(const n of defs){
    if(!r.nodes?.[n.id]||!upstreamReady(n.id))continue;
    seen.add(n.id);
    out.push({id:n.id,name:n.name||n.id,type:n.type||'',status:r.nodes?.[n.id]?.status||'pending'});
  }
  if(!defs.length){
    for(const [id,nr] of Object.entries(r.nodes||{})){
      if(seen.has(id))continue;
      out.push({id,name:id,type:'',status:nr?.status||'pending'});
    }
  }
  return out;
}
function runNodeAllowsContext(node){return !!node?.type&&!isScriptWorkflowNode(node)}
function afRerunFromNode(run,nodeId){
  return new Promise((resolve)=>{
    const modal=$('#confirmModal');
    const fields=$('#rerunFromNodeFields');
    const context=$('#rerunFromNodeContext');
    const contextFields=$('#rerunFromNodeContextFields');
    const hint=$('#rerunFromNodeHint');
    if(!modal||!fields||!context||!contextFields||!hint)return resolve(null);
    const node=runNodeChoices(run).find(n=>n.id===nodeId);
    if(!node){toast('这个节点现在不能从这里重跑');return resolve(null)}
    $('#confirmTitle').textContent='从这里重跑';
    $('#confirmMessage').textContent=`从「${node.name}」及其下游重跑，上游已完成的步骤会保留。${run.workspaceCleanedAt&&!String(run.workspaceCleanedAt).startsWith('0001-')?'工作区已回收：优先恢复已有快照；没有快照时重新准备仓库和已归档的上游产物，未保存的历史文件无法恢复。':''}`;
    $('#confirmPromptWrap').hidden=true;
    fields.hidden=false;
    context.value='';
    const allowsContext=runNodeAllowsContext(node);
    contextFields.hidden=!allowsContext;
    hint.textContent=allowsContext?'补充说明会写进这次执行的上下文。':'脚本节点不接收补充上下文。';
    const okBtn=$('#confirmOkBtn');
    const cancelBtn=$('#confirmCancelBtn');
    okBtn.textContent='开始重跑';
    okBtn.className='button primary';
    cancelBtn.textContent='取消';
    cancelBtn.hidden=false;
    $('#confirmIconWrap').className='confirm-icon-wrap info';
    $('#confirmIcon').textContent='↻';
    modal.classList.add('open');
    if(allowsContext)context.focus();else okBtn.focus();
    confirmPromiseResolve=(isOk)=>{
      const payload=isOk?{nodeId,context:allowsContext?context.value.trim():''}:null;
      modal.classList.remove('open');
      fields.hidden=true;
      confirmPromiseResolve=null;
      resolve(payload);
    };
  });
}
async function openRerunFromNode(run,nodeId){
  if(!run?.id)return;
  if(!canRerunFromHere(run,nodeId))return toast('这个节点现在不能从这里重跑');
  const choice=await afRerunFromNode(run,nodeId);
  if(!choice?.nodeId)return;
  const issueId=run.executionContext?.issueId||issueIdForRun(run.id);
  const issue=(state.data.issues||[]).find(i=>i.id===issueId);
  if(issueTrackerClosed(issue))return toast('外部工作项已关闭，请先在外部系统重新打开');
  if(issueManuallyClosed(issue))return toast('请先重新打开该需求，再从这里重跑');
  try{
    if(deliveryLocked(issue))await reopenDeliveredIssueSilent(issue.id);
    await api(`/api/runs/${encodeURIComponent(run.id)}/retry`,{method:'POST',body:JSON.stringify({nodeId:choice.nodeId,reason:'从指定节点重跑',context:choice.context||''})});
    const next=await syncRunControlState(run.id);
    toast(runControlToast(next,'已从该节点重跑，上游完成步骤会保留'));
    Promise.all([refreshIssues(),refreshRuns(true)]).catch(()=>{});

  }catch(err){toast(err.message,'err')}
}
