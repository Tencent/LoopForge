// 首页按问题组织当前的改进；复用真实数据和原有操作。
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
  if(p?.status==='pending')return p.summary?.passed?'查看指令差异':'查看验证结果';
  if(p?.status==='canary')return p.canary?.status==='passed'?'查看并确认发布':'查看试用结果';
  if(p?.status==='released')return p.watch?.status==='degraded'?'检查并回滚':'查看发布结果';
  if(['drafting','validating'].includes(p?.status))return '查看进度';
  if(row.queue?.length&&!evoOccupiesAsset(p))return '查看后续建议';
  if(p)return ['failed','rejected','rolled_back'].includes(p.status)?'查看记录':'查看指令差异';
  return evoIssueState(row.r?.f||{})==='done'?'查看记录':'查看问题';
}
function evoHomeOutcome(p){
  if(!p)return '';
  const trial=p.status==='canary'?p.canary:null,watch=p.status==='released'?p.watch:null;
  if(trial||watch){
    const arms=trial?[[trial.control,'原版'],[trial.canary,'试用版']]:[[watch.baseline,'发布前'],[watch.current,'发布后']];
    return [trial?'试用一次成功':'发布前后一次成功',...arms.map(([a,label])=>`${label} ${a?.runs>0&&Number.isFinite(a.clean)?a.clean+'/'+a.runs:'暂无结果'}`)].join(' · ');
  }
  const s=p.summary;
  if(!s?.total)return '';
  if((s.finished||0)<s.total)return `历史回放 ${s.finished||0}/${s.total} 已完成`;
  const facts=[];
  if(s.fixed)facts.push(`改善 ${s.fixed}`);
  if(s.regressed)facts.push(`退化 ${s.regressed}`);
  if(s.changed)facts.push(`结论变化 ${s.changed}`);
  if(s.blocked)facts.push(`阻塞 ${s.blocked}`);
  if(!facts.length&&s.unchanged)facts.push(`结果保持 ${s.unchanged}`);
  return ['历史回放',...facts,s.passed&&!s.fixed?'待核对产物':''].filter(Boolean).join(' · ');
}
function evoHomeRowHTML(row){
  const {p,r,asset,key}=row,state=evoListProgress(row);
  const problem=p?.review?.problem||r?.title||p?.title||'改进问题';
  const change=p?.review?.changes?.[0]?.title||(p?.title!==problem?p?.title:'')||'';
  const summary=[evoStoryBrief(change,64),evoHomeOutcome(p)].filter(Boolean).join(' · ');
  const glyph=r?.c?.kind==='user_feedback'?'user':'alert';
  return `
    <button type="button" class="evo-home-row" data-evo-asset-group="${escapeHTML(key)}" data-evo-key="${escapeHTML(key)}">
      <span class="evo-home-mark">${evoReviewIcon(glyph)}</span>
      <span class="evo-home-problem">
        <b data-tooltip="${escapeHTML(problem)}">${escapeHTML(evoStoryBrief(problem,60))}</b>
        <span class="evo-home-asset">${escapeHTML(asset.assetName||asset.assetId||'未知对象')} · ${escapeHTML(evoGroupFacts(row))}</span>
        ${summary?`<span class="evo-home-summary">${escapeHTML(summary)}</span>`:''}
      </span>
      <span class="evo-home-next">
        ${kbBadge(escapeHTML(row.conflict?'存在并行冲突':state.label),row.conflict?'err':state.tone)}
        <span>${escapeHTML(evoHomeNext(row))}${evoReviewIcon('arrow-right')}</span>
      </span>
    </button>`;
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
  return `<div class="evo-home">${error}${checkNote}${toolbar}<section id="evoUnifiedList" class="evo-home-list" aria-label="问题、修改与验证">${rows||empty}</section>${pager}</div>`;
}
document.addEventListener('click',event=>{
  const button=event.target.closest?.('[data-evo-home-check],[data-evo-home-runs]');
  if(!button||button.disabled||!evoActive())return;
  if(button.hasAttribute('data-evo-home-check'))return evoDiagnose(button);
  switchView('runs');
});
