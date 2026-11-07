function setIssueSelectionMode(enabled){
  if(state.issueBulkDeleting)return;
  state.issueSelectionMode=!!enabled;
  state.issueSelection?.clear();
  state.issueBulkDeleteResult=null;
  renderIssues();
}

function issueSelectionFilterKey(){
  return JSON.stringify([state.projectFilter||'',state.sourceFilter||'',state.issueStatusFilter||'',state.issueAssigneeFilter||'',state.issueRunFilter||'',state.issueQuery||'']);
}

function syncIssueSelection(issues){
  if(!state.issueSelection)state.issueSelection=new Set();
  if(!state.issueSelectionMode)state.issueSelection.clear();
  const scope=issueSelectionFilterKey();
  if(state.issueSelectionScope!==scope){
    state.issueSelection.clear();
    state.issueSelectionScope=scope;
    if(!state.issueBulkDeleting)state.issueBulkDeleteResult=null;
  }
  const allowed=new Set(issues.filter(issueCanDelete).map(i=>i.id));
  for(const id of state.issueSelection){
    if(!allowed.has(id))state.issueSelection.delete(id);
  }
}

function issueSelectAllCheckbox(label,showLabel=false){
  if(!state.issueSelectionMode)return '';
  return `<label class="issue-select-label${showLabel?' issue-bulk-select':''}" title="${escapeHTML(label)}"><input type="checkbox" class="issue-select-checkbox" data-issue-select-page aria-label="${escapeHTML(label)}">${showLabel?`<span>${escapeHTML(label)}</span>`:''}</label>`;
}

function issueSelectionCheckbox(issue){
  if(!state.issueSelectionMode)return '';
  const allowed=issueCanDelete(issue),disabled=state.issueBulkDeleting||!allowed;
  const hint=allowed?'选择此需求':'澄清或执行中、已有成功记录的需求不能删除';
  return `<label class="issue-select-label" title="${hint}" draggable="false"><input type="checkbox" class="issue-select-checkbox" data-issue-select="${escapeHTML(issue.id)}" aria-label="选择需求 ${escapeHTML(issue.title||issue.id)}"${state.issueSelection?.has(issue.id)?' checked':''}${disabled?' disabled':''}></label>`;
}

function renderIssueBulkActions(visibleIssues){
  const entry=$('#startIssueSelection');
  if(entry){entry.hidden=!!state.issueSelectionMode;entry.onclick=()=>setIssueSelectionMode(true)}
  const host=$('#issueBulkActions');if(!host)return;
  host.hidden=!state.issueSelectionMode;
  if(!state.issueSelectionMode){host.innerHTML='';return}
  const selected=state.issueSelection.size,busy=state.issueBulkDeleting,progress=state.issueBulkDeleteProgress,result=state.issueBulkDeleteResult;
  const scope=state.issueView==='kanban'?'全选当前可删除需求':'全选本页可删除需求';
  const buttonText=busy?(progress?`正在删除 ${progress.completed}/${progress.total}…`:'等待确认…'):'删除所选';
  host.innerHTML=`<div class="issue-bulk-toolbar">
    <div class="issue-bulk-summary">${issueSelectAllCheckbox(scope,true)}<span aria-live="polite">已选择 <b>${selected}</b> 项</span><small>可跨页选择；切换筛选条件会清空选择</small></div>
    <div class="issue-bulk-buttons"><button type="button" class="button ghost small" data-issue-cancel-selection${busy?' disabled':''}>取消</button><button type="button" class="button ghost small" data-issue-clear-selection${!selected||busy?' disabled':''}>清空选择</button><button type="button" class="button danger-outline small" data-issue-bulk-delete${!selected||busy?' disabled':''}>${buttonText}</button></div>
  </div>${result?.failures.length?`<details class="issue-bulk-result" open><summary>已删除 ${result.deleted} 条，${result.failures.length} 条未删除</summary><ul>${result.failures.map(f=>`<li><b>${escapeHTML(f.title||f.id)}</b>：${escapeHTML(f.message)}</li>`).join('')}</ul></details>`:''}`;
  host.querySelector('[data-issue-cancel-selection]').onclick=()=>setIssueSelectionMode(false);
  host.querySelector('[data-issue-clear-selection]').onclick=()=>{state.issueSelection.clear();renderIssueBulkActions(visibleIssues);bindIssueSelection(visibleIssues)};
  host.querySelector('[data-issue-bulk-delete]').onclick=deleteSelectedIssues;
}

function bindIssueSelection(visibleIssues){
  if(!state.issueSelectionMode)return;
  const root=$('#issuesView');if(!root)return;
  const eligible=visibleIssues.filter(issueCanDelete),selected=state.issueSelection;
  const byID=new Map((state.data.issues||[]).map(i=>[i.id,i]));
  root.querySelectorAll('.issue-select-label,.issue-bulk-select').forEach(label=>{
    label.onclick=e=>e.stopPropagation();
    label.ondragstart=e=>{e.preventDefault();e.stopPropagation()};
  });
  root.querySelectorAll('[data-issue-select]').forEach(input=>{
    const issue=byID.get(input.dataset.issueSelect);
    input.checked=selected.has(input.dataset.issueSelect);
    input.disabled=state.issueBulkDeleting||!issueCanDelete(issue);
    input.closest('.issue-table-row,.kanban-card')?.classList.toggle('is-selected',input.checked);
    input.onchange=()=>{
      if(input.disabled)return;
      if(input.checked)selected.add(input.dataset.issueSelect);else selected.delete(input.dataset.issueSelect);
      renderIssueBulkActions(visibleIssues);
      bindIssueSelection(visibleIssues);
    };
  });
  root.querySelectorAll('[data-issue-select-page]').forEach(input=>{
    const count=eligible.filter(i=>selected.has(i.id)).length;
    input.checked=eligible.length>0&&count===eligible.length;
    input.indeterminate=count>0&&count<eligible.length;
    input.disabled=state.issueBulkDeleting||!eligible.length;
    input.onchange=()=>{
      if(input.disabled)return;
      for(const issue of eligible){if(input.checked)selected.add(issue.id);else selected.delete(issue.id)}
      renderIssueBulkActions(visibleIssues);
      bindIssueSelection(visibleIssues);
    };
  });
}

async function deleteSelectedIssues(){
  if(!state.issueSelectionMode||state.issueBulkDeleting)return;
  const filtered=filteredIssues();
  syncIssueSelection(filtered);
  const issues=filtered.filter(i=>state.issueSelection.has(i.id));
  if(!issues.length)return toast('请先选择可删除的需求');
  const scope=issueSelectionFilterKey(),external=issues.some(i=>i.source==='tapd'||i.source==='gongfeng');
  const names=issues.slice(0,5).map(i=>`「${i.title||i.id}」`).join('、')+(issues.length>5?'等':'');
  state.issueBulkDeleting=true;
  renderIssues();
  try{
    if(!await afConfirm({title:'批量删除需求',message:`确定删除选中的 ${issues.length} 条需求（${names}）？需求、评论、澄清记录以及失败／取消的历史任务、运行记录、日志、产物和执行工作区将一并清理，且不可恢复。澄清或执行中、已有成功运行的需求不允许删除。${external?'此操作仅删除本地记录，不会删除 TAPD／工蜂原单，后续同步可能重新导入。':''}`,danger:true,confirmText:`确认删除 ${issues.length} 条需求`}))return;
    if(scope!==issueSelectionFilterKey())return toast('筛选条件已变化，请重新选择需求','info');
    const result={deleted:0,failures:[]};
    state.issueBulkDeleteResult=result;
    state.issueBulkDeleteProgress={completed:0,total:issues.length};
    // Reuse the single-delete endpoint so each issue gets authoritative history
    // checks and full engine cleanup. One conflict must not abort the batch.
    for(const issue of issues){
      try{
        await api(`/api/issues/${encodeURIComponent(issue.id)}`,{method:'DELETE'});
        result.deleted++;
        state.issueSelection.delete(issue.id);
        state.data.issues=(state.data.issues||[]).filter(i=>i.id!==issue.id);
        state.data.tasks=(state.data.tasks||[]).filter(t=>t.issueId!==issue.id);
        if(state.issueDetailData?.issue?.id===issue.id){closeIssueDrawer();state.issueDetailData=null;}
        if(state.selectedIssue===issue.id)state.selectedIssue=null;
      }catch(err){result.failures.push({id:issue.id,title:issue.title,message:err.message||String(err)})}
      state.issueBulkDeleteProgress.completed++;
      renderIssues();
    }
    await refreshRuns();
    if(!result.failures.length)state.issueSelectionMode=false;
    toast(result.failures.length?`已删除 ${result.deleted} 条需求，${result.failures.length} 条未删除，请查看失败原因`:`已删除 ${result.deleted} 条需求`,result.failures.length?'warn':'ok');
  }catch(err){toast(err.message,'err')}
  finally{
    state.issueBulkDeleting=false;
    state.issueBulkDeleteProgress=null;
    renderIssues();
  }
}
