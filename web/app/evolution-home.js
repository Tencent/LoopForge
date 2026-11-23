// 首页按「问题 → 修改 → 验证 → 下一步」呈现；复用真实数据和原有操作。
// 视觉令牌与图标沿用 Harness UI（MIT），见 harness-ui-LICENSE.txt。
function evoHomeCheck(){return evo.lastCheck?.projectId===evoProjectId()?evo.lastCheck:null}
function evoHomeButton(label,attr,primary=false){
  return `<button type="button" class="kb-btn${primary?' primary':''}" ${attr}>${label}</button>`;
}
function evoHomeSyncChecking(){
  $$('#evoDiagnose,[data-evo-rediagnose],[data-evo-home-check]').forEach(b=>b.disabled=evo.diagnosing);
  $$('#evoDiagnose [data-evo-check-label],[data-evo-home-check] [data-evo-check-label]').forEach(el=>el.textContent=evo.diagnosing?'检查中…':'检查最近运行');
}
function evoHomeEmptyHTML(){
  const check=evoHomeCheck(),hasRuns=check?check.runs>0:!!evo.overview?.health?.some(h=>h.runs>0);
  const title=check?check.findings?'检查已完成':hasRuns?'暂无待处理问题':'当前项目没有可分析的运行':'暂无改进记录';
  const note=check?check.findings?'刷新列表查看处理进度。':hasRuns?'产物不符合预期时，在运行详情反馈具体问题。':'先完成一条使用角色或规则的任务，再回来检查。':hasRuns?'点击右上角检查最近运行，或在运行详情反馈问题。':'先完成一条任务，再从运行结果开始改进。';
  const action=check?.findings?evoHomeButton('刷新列表','data-evo-reload'):evoHomeButton(`${evoReviewIcon('play')}查看运行`,'data-evo-home-runs');
  return `<section class="evo-home-empty" aria-labelledby="evoHomeEmptyTitle"><div class="evo-home-empty-mark">${evoReviewIcon(check&&hasRuns?'check':'workflow')}</div><h2 id="evoHomeEmptyTitle">${title}</h2><p>${escapeHTML(note)}</p><div class="evo-home-empty-actions">${action}</div></section>`;
}
function evoHomeNext(row){
  const p=row.p;
  if(row.conflict)return '检查并行修改';
  if(p?.summary?.regressed>0)return '查看退化样例';
  if(p?.status==='pending')return p.summary?.passed?'查看修改并试用':'查看验证结果';
  if(p?.status==='canary')return p.canary?.status==='passed'?'查看并确认发布':'查看试用结果';
  if(p?.status==='released')return p.watch?.status==='degraded'?'检查并回滚':'查看发布结果';
  if(['drafting','validating'].includes(p?.status))return '查看进度';
  if(row.queue?.length&&!evoOccupiesAsset(p))return '查看后续建议';
  if(p)return ['failed','rejected','rolled_back'].includes(p.status)?'查看记录':'查看修改';
  return evoIssueState(row.r?.f||{})==='done'?'查看记录':'查看问题';
}
function evoHomeValidationHTML(p){
  if(!p)return `<span class="evo-home-unverified">${evoReviewIcon('flask')}尚未验证</span>`;
  const trial=p.status==='canary'?p.canary:null,watch=p.status==='released'?p.watch:null;
  if(trial||watch){
    const arms=trial?[[trial.control,'原版'],[trial.canary,'试用版']]:[[watch.baseline,'发布前'],[watch.current,'发布后']];
    return `<span class="evo-home-eval-label">${trial?'真实运行试用':'发布后观察'}</span>${arms.map(([a,label])=>`<span class="evo-home-arm">${label}<b>${a?.runs>0&&Number.isFinite(a.clean)?`${a.clean}/${a.runs}`:'—'}</b><small>一次成功</small></span>`).join('')}`;
  }
  const s=p.summary||{};
  if(!s.total)return `<span class="evo-home-unverified">${evoReviewIcon(p.status==='drafting'?'edit':'flask')}${p.status==='drafting'?'生成后自动验证':'暂无验证结果'}</span>`;
  return `<span class="evo-home-eval-label">历史验证 · 完成 ${s.finished||0}/${s.total}</span><span class="evo-home-outcomes"><span class="evo-good">${evoReviewIcon('check')}改善 ${s.fixed||0}</span><span class="${s.regressed?'evo-bad':'evo-muted'}">${evoReviewIcon('undo')}退化 ${s.regressed||0}</span></span>${s.blocked?`<small class="evo-home-blocked">阻塞 ${s.blocked}</small>`:''}`;
}
function evoHomeRowHTML(row){
  const {p,r,asset,key}=row,state=evoListProgress(row);
  const problem=p?.review?.problem||r?.title||'查看问题来源';
  const points=p?.review?.changes||[],point=points[0],change=points.map(x=>x.title).filter(Boolean).join(' · ')||p?.title||'尚未生成修改';
  const reason=point?.reason||p?.review?.reason||p?.rationale||'';
  const excerpt=(value,empty)=>escapeHTML(value?evoStoryBrief(value,27):empty);
  const pair=point?`<span class="evo-home-change-pair"><span>${excerpt(point.before,'新增要求')}</span>${evoReviewIcon('arrow-right')}<span>${excerpt(point.after,'移除此要求')}</span></span>`:'';
  return `<button type="button" class="evo-home-row" data-evo-asset-group="${escapeHTML(key)}" data-evo-key="${escapeHTML(key)}"><span class="evo-home-problem"><b data-tooltip="${escapeHTML(problem)}">${escapeHTML(evoStoryBrief(problem,45))}</b><span class="evo-home-asset">${evoReviewIcon(asset.assetKind==='rule'?'doc':'robot')}${escapeHTML(asset.assetName||asset.assetId||'未知对象')}</span><small>${escapeHTML(evoGroupFacts(row))}</small></span><span class="evo-home-change"><b data-tooltip="${escapeHTML(change)}">${escapeHTML(evoStoryBrief(change,38))}</b>${pair}${reason?`<small data-tooltip="${escapeHTML(reason)}">${escapeHTML(evoStoryBrief(reason,58))}</small>`:''}</span><span class="evo-home-validation">${evoHomeValidationHTML(p)}</span><span class="evo-home-next">${kbBadge(escapeHTML(row.conflict?'存在并行冲突':state.label),row.conflict?'err':state.tone)}<span>${escapeHTML(evoHomeNext(row))}${evoReviewIcon('arrow-right')}</span></span></button>`;
}
function evoHomeHTML(){
  const all=evoUnifiedRows(),check=evoHomeCheck();
  const checkNote=check?`<p class="evo-home-check-note" role="status">${evoReviewIcon('check')}上次检查 ${check.runs} 次运行 · ${check.findings} 个问题<span>${escapeHTML(relativeTime(check.at))}</span></p>`:'';
  const error=evo.checkError?`<div class="evo-home-check-error" role="alert">检查失败：${escapeHTML(evo.checkError)}${evoHomeButton('重试','data-evo-home-check')}</div>`:'';
  if(!all.length)return `<div class="evo-home">${error}${checkNote}${evoHomeEmptyHTML()}</div>`;
  if(!['active','all'].includes(evo.list.status))evo.list={status:'active',page:0};
  const active=all.filter(r=>evoListCategory(r)!=='done'),list=evo.list.status==='all'?all:[...active];
  const priority=r=>r.p?.watch?.status==='degraded'||r.p?.summary?.regressed>0?0:evoListCategory(r)==='todo'?1:2;
  list.sort((a,b)=>priority(a)-priority(b));
  const pages=Math.max(1,Math.ceil(list.length/10));evo.list.page=Math.max(0,Math.min(evo.list.page,pages-1));
  const rows=list.slice(evo.list.page*10,(evo.list.page+1)*10).map(evoHomeRowHTML).join('');
  const toolbar=`<div class="evo-home-toolbar"><div class="evo-home-tabs" role="group" aria-label="改进记录筛选">${[['active','待跟进',active.length],['all','全部记录',all.length]].map(([key,label,n])=>`<button type="button" data-evo-list-status="${key}" data-evo-key="filter:${key}" aria-pressed="${evo.list.status===key}">${label}<span>${n}</span></button>`).join('')}</div><span class="evo-home-scope">当前载入 ${all.length} 项</span></div>`;
  const empty=`<div class="evo-home-filter-empty">${evoReviewIcon('check')}当前没有待跟进的改进${evoHomeButton(`查看全部 ${all.length} 条`,'data-evo-list-status="all"')}</div>`;
  const pager=pages>1?`<footer class="evo-list-footer"><span>当前载入 ${list.length} 项</span><nav aria-label="改进分页"><button type="button" data-evo-unified-page="${evo.list.page-1}"${evo.list.page?'':' disabled'}>上一页</button><span>${evo.list.page+1}/${pages}</span><button type="button" data-evo-unified-page="${evo.list.page+1}"${evo.list.page+1<pages?'':' disabled'}>下一页</button></nav></footer>`:'';
  return `<div class="evo-home">${error}${checkNote}${toolbar}<section id="evoUnifiedList" class="evo-home-list" aria-label="问题、修改与验证">${rows?`<div class="evo-home-columns" aria-hidden="true"><span>问题 / 对象</span><span>修改</span><span>验证结果</span><span>下一步</span></div>${rows}`:empty}</section>${pager}</div>`;
}
document.addEventListener('click',event=>{
  const button=event.target.closest?.('[data-evo-home-check],[data-evo-home-runs]');
  if(!button||button.disabled||!evoActive())return;
  if(button.hasAttribute('data-evo-home-check'))return evoDiagnose(button);
  switchView('runs');
});
