// 首页按问题组织当前的改进；复用真实数据和原有操作。
// 视觉令牌与图标沿用 Harness UI（MIT），见 harness-ui-LICENSE.txt。
function evoHomeCheck(){return evo.lastCheck?.projectId===evoProjectId()?evo.lastCheck:null}
function evoHomeButton(label,attr,primary=false){
  return `<button type="button" class="kb-btn${primary?' primary':''}" ${attr}>${label}</button>`;
}
function evoHomeSyncChecking(){
  $$('#evoDiagnose,[data-evo-rediagnose],[data-evo-home-check]').forEach(b=>b.disabled=evo.diagnosing);
  $$('#evoDiagnose [data-evo-check-label],[data-evo-home-check] [data-evo-check-label]').forEach(el=>el.textContent=evo.diagnosing?'诊断中…':'发起诊断');
}
function evoHomeEmptyHTML(){
  const check=evoHomeCheck(),hasRuns=check?check.runs>0:!!evo.overview?.health?.some(h=>h.runs>0);
  const title=check?check.findings?'检查已完成':hasRuns?'暂无待处理问题':'当前项目没有可分析的运行':'暂无改进记录';
  const note=check?check.findings?'刷新列表查看处理进度。':hasRuns?'产物不符合预期时，在运行详情反馈具体问题。':'先完成一条使用角色或规则的任务，再回来检查。':hasRuns?'点击右上角发起诊断，或在运行详情反馈问题。':'先完成一条任务，再从运行结果开始改进。';
  const action=check?.findings?evoHomeButton('刷新列表','data-evo-reload'):evoHomeButton(`${evoReviewIcon('play')}查看运行`,'data-evo-home-runs');
  return `<section class="evo-home-empty" aria-labelledby="evoHomeEmptyTitle"><div class="evo-home-empty-mark">${evoReviewIcon(check&&hasRuns?'check':'workflow')}</div><h2 id="evoHomeEmptyTitle">${title}</h2><p>${escapeHTML(note)}</p><div class="evo-home-empty-actions">${action}</div></section>`;
}
function evoHomeBrief(raw,limit=28){
  const first=String(raw||'').trim().split(/[。！？\r\n]/u,1)[0].trim();
  return evoStoryBrief(first,limit);
}
function evoHomeProblem(row){
  const {p,r}=row;
  if(p?.review?.problem)return evoHomeBrief(p.review.problem);
  const raw=r?.title||p?.title||row.title||'改进问题';
  // Execution wrappers repeat internal instructions. The signal type is a clearer list title.
  if(/^agent submitted terminal failure:/i.test(raw))return EVO_SIGNALS[r?.c?.kind]||'节点失败';
  return evoHomeBrief(raw)||'改进问题';
}
function evoHomeUpdated(row){
  return (row.history&&evoTime(row.p.decidedAt))||evoTime(row.p?.updatedAt)||
    evoTime(row.r?.f.updatedAt)||evoTime(row.r?.f.lastSeenAt)||evoTime(row.queue?.[0]?.updatedAt);
}
function evoHomeRowHTML(row){
  const {p,r,asset,key}=row,state=evoListProgress(row);
  const problem=evoHomeProblem(row),rawProblem=p?.review?.problem||r?.title||p?.title||problem;
  const change=p?.review?.changes?.[0]?.title||(p?.status!=='drafting'&&p?.title!==rawProblem?p?.title:'')||'';
  const workflows=evoFindingWorkflows(r?.f);
  const related=workflows.length===1?`SOP · ${workflows[0].name}`:workflows.length>1?`${workflows.length} 个 SOP`:asset.assetName||asset.assetId||'未知对象';
  const glyph=r?.c?.kind==='user_feedback'?'user':'alert';
  const updated=evoHomeUpdated(row),date=updated?new Date(updated):null;
  const time=date&&!Number.isNaN(+date)?`<time datetime="${escapeHTML(updated)}" data-tooltip="${escapeHTML(date.toLocaleString())}">${escapeHTML(evoIssueDate(updated))}</time>`:'—';
  const destination=row.history?`data-evo-proposal="${escapeHTML(p.id)}"`:`data-evo-asset-group="${escapeHTML(key)}"`;
  return `
    <button type="button" class="evo-home-row" ${destination} data-evo-key="${escapeHTML(key)}">
      <span class="evo-home-mark">${evoFindingPriorityHTML(r?.f)||evoReviewIcon(glyph)}</span>
      <span class="evo-home-problem">
        <b data-tooltip="${escapeHTML(rawProblem)}">${escapeHTML(problem)}</b>
        <span class="evo-home-asset" data-tooltip="${escapeHTML(workflows.map(w=>w.name).join('、')||related)}">${escapeHTML(related)}</span>
      </span>
      <span class="evo-home-summary"${change?` data-tooltip="${escapeHTML(change)}"`:''}>${escapeHTML(evoHomeBrief(change,30))}</span>
      <span class="evo-home-next">${kbBadge(escapeHTML(row.conflict?'存在并行冲突':state.label),row.conflict?'err':state.tone)}</span>
      <span class="evo-home-updated">${time}</span>
      <span class="evo-home-chevron">${evoIcon('chevron')}</span>
    </button>`;
}
function evoHomeStatusHTML(){
  const check=evoHomeCheck();
  const checkNote=check?`<p class="evo-home-check-note" role="status">${evoReviewIcon('check')}上次检查 ${check.runs} 次运行 · ${check.findings} 个问题<span>${escapeHTML(relativeTime(check.at))}</span></p>`:'';
  const error=evo.checkError?`<div class="evo-home-check-error" role="alert">检查失败：${escapeHTML(evo.checkError)}${evoHomeButton('重试','data-evo-home-check')}</div>`:'';
  return error+checkNote;
}
function evoReadErrorHTML(){
  return evo.error?`<div class="evo-home-check-error" role="alert">读取失败：${escapeHTML(evo.error)}${evoHomeButton('重试','data-evo-reload')}</div>`:'';
}
function evoHomeBuckets(all){
  return {
    all,
    active:all.filter(r=>evoListCategory(r)==='todo'),
    doing:all.filter(r=>evoListCategory(r)==='doing'),
    released:all.filter(r=>r.p?.status==='released'),
    rolled_back:all.filter(r=>r.p?.status==='rolled_back')
  };
}
function evoHomeSummaryHTML(){
  const groups=evoHomeBuckets(evoUnifiedRows());
  const stats=[
    ['alert','待处理问题',groups.active.length,'warn'],
    ['refresh','迭代中',groups.doing.length,'accent'],
    ['arrow-up','已发布',evoAllProposals().filter(p=>p.status==='released').length,'success']
  ];
  return `<div class="evo-workspace-metrics">${stats.map(([icon,label,value,tone])=>`
    <section class="evo-workspace-stat" data-tone="${tone}">
      <h2><span>${evoReviewIcon(icon)}</span>${label}</h2><b>${value}</b>
    </section>`).join('')}</div>`;
}
const EVO_TASK_SECTIONS=[['tasks','改进任务'],['released','近期发布'],['rolled_back','回滚记录']];
const EVO_TASK_STATUSES=[['all','全部状态'],['active','待处理'],['doing','迭代中'],['released','已发布'],['rolled_back','已回滚']];
function evoHomeSectionRows(all){
  return evo.list.section==='released'||evo.list.section==='rolled_back'?all.flatMap(group=>group.proposals
    .filter(p=>p.status===evo.list.section).map(p=>({
      key:`proposal:${p.id}`,history:true,p,asset:p,
      r:group.issues.find(r=>r.f.id===p.findingId||r.f.proposalId===p.id)
    }))):evoHomeBuckets(all)[evo.list.status]||all;
}
function evoHomeListHTML(all){
  const q=(evo.list.q||'').trim().toLocaleLowerCase();
  const list=evoHomeSectionRows(all).filter(row=>{
    const workflows=evoFindingWorkflows(row.r?.f);
    return (!evo.list.sop||workflows.some(w=>w.id===evo.list.sop))&&
      (!q||[evoHomeProblem(row),row.r?.title,row.asset.assetName,row.p?.title,row.p?.review?.changes?.[0]?.title,...workflows.map(w=>w.name)]
        .some(v=>String(v||'').toLocaleLowerCase().includes(q)));
  });
  const needsAttention=r=>r.p?.watch?.status==='degraded'||r.p?.summary?.regressed>0;
  const priority={todo:0,doing:1,done:2};
  const impact=r=>r.r?.f.triage?.score||0;
  const newest=(a,b)=>(Date.parse(evoHomeUpdated(b))||0)-(Date.parse(evoHomeUpdated(a))||0);
  list.sort(evo.list.sort==='recent'||evo.list.section&&evo.list.section!=='tasks'?
    newest:(a,b)=>Number(needsAttention(b))-Number(needsAttention(a))||
    impact(b)-impact(a)||priority[evoListCategory(a)]-priority[evoListCategory(b)]||newest(a,b));
  const pages=Math.max(1,Math.ceil(list.length/10));evo.list.page=Math.max(0,Math.min(evo.list.page,pages-1));
  const rows=list.slice(evo.list.page*10,(evo.list.page+1)*10).map(evoHomeRowHTML).join('');
  const filtered=q||evo.list.sop||(!evo.list.section||evo.list.section==='tasks')&&evo.list.status!=='all';
  const empty=`<div class="evo-home-filter-empty">${filtered?'暂无匹配任务':evo.list.section==='released'?'暂无发布记录':evo.list.section==='rolled_back'?'暂无回滚记录':'暂无改进任务'}
    ${filtered?evoHomeButton('清除筛选','data-evo-list-reset'):''}</div>`;
  const pager=pages>1?`<footer class="evo-list-footer"><nav aria-label="改进分页"><button type="button" data-evo-unified-page="${evo.list.page-1}"${evo.list.page?'':' disabled'}>上一页</button><span>${evo.list.page+1}/${pages}</span><button type="button" data-evo-unified-page="${evo.list.page+1}"${evo.list.page+1<pages?'':' disabled'}>下一页</button></nav></footer>`:'';
  const columns=rows?'<div class="evo-home-columns" aria-hidden="true"><span>问题 / 关联 SOP</span><span>改动摘要</span><span>当前阶段</span><span>更新</span></div>':'';
  return `<section id="evoUnifiedList" class="evo-home-list" aria-label="任务记录">${columns}${rows||empty}</section>${pager}`;
}
function evoHomeTasksHTML(all){
  if(!all.length)return evoHomeEmptyHTML();
  const section=evo.list.section||'tasks';
  const workflows=[...new Map(all.flatMap(group=>group.issues.flatMap(r=>evoFindingWorkflows(r.f))).map(w=>[w.id,w])).values()];
  return `<div class="evo-home-tabs" role="group" aria-label="改进记录">${EVO_TASK_SECTIONS.map(([key,label])=>
      `<button type="button" data-evo-list-section="${key}" data-evo-key="section:${key}" aria-pressed="${section===key}">${label}</button>`).join('')}</div>
    <div class="evo-home-toolbar">
      ${section==='tasks'?`<select class="kb-input" data-evo-task-filter="status" data-evo-key="task-status" aria-label="任务状态">${EVO_TASK_STATUSES.map(([key,label])=>
        `<option value="${key}"${evo.list.status===key?' selected':''}>${label}</option>`).join('')}</select>`:''}
      ${workflows.length?`<select class="kb-input" data-evo-task-filter="sop" data-evo-key="task-sop" aria-label="关联 SOP"><option value="">全部 SOP</option>${workflows.map(w=>
        `<option value="${escapeHTML(w.id)}"${evo.list.sop===w.id?' selected':''}>${escapeHTML(w.name)}</option>`).join('')}</select>`:''}
      <label class="evo-home-search">${evoReviewIcon('search')}<input class="kb-input" type="search" data-evo-task-search data-evo-key="task-search" aria-label="搜索问题、SOP 或任务" placeholder="搜索问题、SOP 或任务" value="${escapeHTML(evo.list.q||'')}"></label>
      ${section==='tasks'?`<select class="kb-input" data-evo-task-filter="sort" data-evo-key="task-sort" aria-label="改进任务排序">
        <option value="impact">按影响度</option><option value="recent"${evo.list.sort==='recent'?' selected':''}>最近更新</option></select>`:''}
    </div>
    <div id="evoHomeResults">${evoHomeListHTML(all)}</div>`;
}
function evoHomeHTML(){
  if(typeof evoSOPIsEnabledView==='function'&&evoSOPIsEnabledView())return evoSOPHomeHTML();
  const all=evoUnifiedRows();
  const backToSOP=Array.isArray(evo.overview?.sops)?'<div class="evo-sop-mode-banner"><button type="button" class="kb-btn quiet small" data-evo-sop-mode="sop">‹ 返回 SOP 进化列表</button></div>':'';
  return `<div class="evo-home">
    ${backToSOP}
    ${evoHomeStatusHTML()}
    <section id="evoWorkspaceTasks" class="evo-workspace-tasks" aria-label="改进任务">${evoHomeTasksHTML(all)}</section>
  </div>`;
}
function evoUpdateHomeResults(){
  const results=$('#evoHomeResults');
  if(!results)return;
  results.innerHTML=evoHomeListHTML(evoUnifiedRows());
  window.AgentFlowHarnessIcons?.hydrate(results);
}
function evoUpdateWorkspaceTasks(){
  const tasks=$('#evoWorkspaceTasks');
  if(!tasks)return;
  const key=tasks.contains(document.activeElement)?document.activeElement.dataset.evoKey:'';
  tasks.innerHTML=evoHomeTasksHTML(evoUnifiedRows());
  window.AgentFlowHarnessIcons?.hydrate(tasks);
  if(key)tasks.querySelector(`[data-evo-key="${CSS.escape(key)}"]`)?.focus({preventScroll:true});
}
document.addEventListener('click',event=>{
  const button=event.target.closest?.('[data-evo-home-check],[data-evo-home-runs]');
  if(!button||button.disabled||!evoActive())return;
  if(button.hasAttribute('data-evo-home-check'))return evoDiagnose(button);
  switchView('runs');
});

function evoSOPDetectionHTML(){
  const setting=evo.overview?.sopDetection;
  return `<form id="evoSOPDetection" class="evo-sop-detection kb-panel" aria-label="SOP 自动检测设置">
    <label class="evo-sop-setting">
      <span><b>SOP 自动检测</b><small>新运行结束后检查重复步骤、工具反复使用和调用失败。</small></span>
      <input type="checkbox" name="enabled" role="switch" aria-label="SOP 自动检测"${setting?.enabled?' checked':''}>
    </label>
    <div class="evo-sop-setting">
      <span><b>检测模型</b><small>使用保存设置的账号配置。</small></span>
      <fieldset class="evo-sop-model"${setting?.enabled?'':' disabled'}>${window.AgentFlowAssistantModels?.featurePickerHTML('model')||''}</fieldset>
    </div>
    <footer><span data-evo-detection-status role="status"></span><button type="submit" class="kb-btn small" disabled>保存</button></footer>
  </form>`;
}
function evoBindSOPDetection(form){
  if(!form)return;
  const pid=evo.projectId,setting=evo.overview?.sopDetection||{},enabled=form.elements?.enabled||form.querySelector('[name="enabled"]');
  if(!enabled)return;
  const model=form.querySelector('[data-feature-model]'),submit=form.querySelector('[type=submit]');
  const status=form.querySelector('[data-evo-detection-status]');
  const picker=form.querySelector('.evo-sop-model');
  let changed=false,pending=false;
  const refresh=()=>{
    picker.disabled=pending||!enabled.checked;
    submit.disabled=pending||!changed||(enabled.checked&&!model?.value);
  };
  enabled.onchange=()=>{changed=true;status.textContent='';refresh()};
  window.AgentFlowAssistantModels?.bindFeaturePicker(picker,{
    model:setting.model||'',keepSaved:true,onChange:()=>{changed=true;status.textContent='';refresh()}
  })?.then(refresh);
  form.onsubmit=async event=>{
    event.preventDefault();
    if(submit.disabled||pid!==evoProjectId())return;
    pending=true;refresh();enabled.disabled=true;
    try{
      const next=await api(`/api/evolution/sop-detection?projectId=${encodeURIComponent(pid)}`,{
        method:'PUT',body:JSON.stringify({enabled:enabled.checked,model:model?.value||''})
      });
      if(!form.isConnected||pid!==evoProjectId())return;
      evo.overview.sopDetection=next;
      changed=false;status.textContent='已保存';enabled.checked=next.enabled;
    }catch(err){
      if(form.isConnected)status.textContent=err.message;
    }finally{
      pending=false;enabled.disabled=false;refresh();
    }
  };
}
