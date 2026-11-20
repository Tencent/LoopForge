// 简化进化视图，复用 evolution.js 的真实数据、证据与操作。
function evoOccupiesAsset(p){return ['drafting','validating','pending','canary'].includes(p?.status)||(p?.status==='released'&&['watching','degraded'].includes(p.watch?.status))}
function evoAssetQueue(kind,id){return (evo.overview?.queue||[]).filter(q=>q.assetKind===kind&&q.assetId===id&&q.status==='queued')}
function evoAssetGroups(){
  const groups=new Map();
  const get=asset=>{const key=asset.assetKind&&asset.assetId?evoAssetKey(asset.assetKind,asset.assetId):`proposal:${asset.id}`;if(!groups.has(key))groups.set(key,{key:`asset:${key}`,asset,issues:[],proposals:[],queue:[]});return groups.get(key)};
  for(const r of evoIssueRows())get(r.f).issues.push(r);
  for(const p of evoAllProposals())get(p).proposals.push(p);
  for(const q of evo.overview?.queue||[])if(q.status==='queued')get(q).queue.push(q);
  return [...groups.values()].map(g=>{
    const active=g.proposals.filter(evoOccupiesAsset),rank=p=>p.watch?.status==='degraded'?0:p.summary?.regressed>0?1:2;
    active.sort((a,b)=>rank(a)-rank(b)||(Date.parse(b.updatedAt||b.createdAt)||0)-(Date.parse(a.updatedAt||a.createdAt)||0));
    g.p=active[0]||g.proposals[0]||null;g.conflict=active.length>1;
    g.r=g.issues.find(r=>r.f.id===g.p?.findingId||r.f.proposalId===g.p?.id)||g.issues.find(r=>r.f.status==='open')||g.issues[0]||null;
    g.asset=g.p||g.r?.f||g.asset;g.title=g.p?.title||g.r?.title||'等待处理的改进';
    return g;
  });
}
function evoGroupForAsset(asset){return asset?evoAssetGroups().find(g=>g.asset.assetKind===asset.assetKind&&g.asset.assetId===asset.assetId):null}
function evoOpenAssetGroup(key){
  const group=evoAssetGroups().find(g=>g.key===key);if(!group)return;
  if(group.p)evoOpenProposal(group.p.id);else if(group.r)evoOpenFinding(group.r.f.id,group.r.key);else return evoOpenQueue(group.asset.assetKind,group.asset.assetId);
  if(evo.modal)evo.modal.openerKey=group.key;
}
function evoGroupFacts(group){
  const n=group.issues.length,queued=group.queue.length;
  return `${n?`${n} 类关联问题`:'未关联问题样本'}${queued?` · ${queued} 项后续改进排队`:''}${group.proposals.length>1?` · ${group.proposals.length-1} 次历史尝试`:''}`;
}
function evoSimpleTrack(p){
  const steps=p?evoStages(p).steps.slice(1,5):[['draft','改写正文'],['replay','历史验证'],['canary','灰度试用'],['release','全量发布']].map(([k,t])=>({k,t,state:'todo',note:'尚未开始'}));
  if(p){
    steps[2]={...steps[2],t:'灰度试用'};steps[3]={...steps[3],t:'全量发布'};
    if(p.status==='pending'&&!p.summary?.passed){steps[1]={...steps[1],state:'wait',note:'验证尚未通过'};steps[2]={...steps[2],state:'todo',note:''}}
    if(p.status==='rolled_back')steps[3]={...steps[3],state:'stop',note:'已回滚'};
  }
  return `<ol id="evoDetailTrack" class="evo-detail-track" aria-label="改进进度">${steps.map((s,i)=>`<li class="is-${s.state}"${['active','wait'].includes(s.state)?' aria-current="step"':''}><span>${EVO_STEP_ICON[s.state]||i+1}</span><b>${escapeHTML(s.t)}</b><small>${escapeHTML(s.note||EVO_STEP_STATE[s.state])}</small></li>`).join('')}</ol>`;
}
function evoSummaryHTML(p){
  if(!p?.candidateContent)return `<p class="evo-muted">${p?.status==='drafting'?'正在生成候选正文，暂时没有可对比的修改。':'还没有候选正文，不展示虚构的修改对比。'}</p>`;
  if(p.baseContent===p.candidateContent)return '<p class="evo-muted">候选与修改前正文一致，没有实际正文改动。</p>';
  const lines=evoLineDiff(p.baseContent,p.candidateContent),excerpt=kind=>{const text=lines.filter(([k,l])=>k===kind&&l.trim()).map(([,l])=>l.trim()).join('；');return text?`${text.slice(0,180)}${text.length>180?'…':''}`:kind==='del'?'保留原有正文，仅补充要求。':'没有新增正文，仅移除部分要求。'};
  return `<div class="evo-change-lines"><div><span>之前</span><p>${escapeHTML(excerpt('del'))}</p></div><div class="after"><span>${p.releasedVersion?'修改后':'候选'}</span><p>${escapeHTML(excerpt('add'))}</p></div></div><small class="evo-summary-scope">从真实正文差异提取的摘要；候选未发布时，线上仍使用原版。</small>`;
}
function evoSampleArm(label,a,goal){
  const count=v=>Math.max(0,Math.floor(Number(v)||0)),runs=count(a?.runs),clean=count(a?.clean),rework=count(a?.reworked),failed=count(a?.failed),missing=Math.max(0,runs-clean-rework-failed);
  const values=[['clean',clean,'一次成功','✓'],['rework',rework,'返工后成功','↻'],['failed',failed,'失败','×'],['unknown',missing,'未分类','?'],['waiting',Math.max(0,count(goal)-runs),'等待运行','']];
  const limit=60,dots=[];
  for(const [kind,n,name,glyph] of values)for(let i=0;i<n&&dots.length<limit;i++)dots.push(`<span class="evo-sample-dot ${kind}" aria-hidden="true" title="${escapeHTML(name)}">${glyph}</span>`);
  return `<section class="evo-sample-arm" aria-label="${escapeHTML(label)}"><header><strong>${escapeHTML(label)}</strong><span>${runs}${goal?` / ${count(goal)}`:''} 次运行</span></header><div class="evo-sample-dots" role="img" aria-label="${escapeHTML(`${label}：一次成功 ${clean}，返工后成功 ${rework}，失败 ${failed}，未分类 ${missing}，还需 ${Math.max(0,count(goal)-runs)} 次运行`)}">${dots.join('')||'<span class="evo-muted">还没有运行</span>'}</div>${Math.max(runs,count(goal))>limit?`<small>仅绘制前 ${limit} 个分类方块，统计包含全部运行。</small>`:''}<div class="evo-arm-rate"><b>${runs?evoPct(clean/runs):'—'}</b><span>一次成功 · ${clean}/${runs}</span></div></section>`;
}
function evoGrayBoardHTML(p){
  const c=p?.canary,w=p?.watch,released=p?.status==='released',ended=p?.status==='rolled_back'||p?.status==='rejected'||(p?.status==='failed'&&c),trial=p?.status==='canary'&&['running','passed'].includes(c?.status);
  const percent=released?100:trial?Number(c.percent)||0:0;
  const title=released||p?.status==='rolled_back'?'发布后观察':'灰度看板';
  const message=released?w?.verdict||'新版本已发布，等待发布后运行。':ended?'本次改进已结束，新运行不再分配给此候选。':trial?c.verdict||'等待两组运行达到最少次数。':p?.status==='pending'?'历史验证后，先小范围试用，再决定是否发布。':p?'候选完成历史验证后，才能进入灰度。':'开始修改并验证后，才会产生灰度运行。';
  const board=released&&w?evoSampleArm(`新版 v${w.version||p.releasedVersion||'?'}`,w.current,w.minRuns):c?`${evoSampleArm('试用版',c.canary,c.minRuns)}${evoSampleArm('原版',c.control,c.minRuns)}`:'';
  return `<section class="evo-detail-box evo-gray-board" aria-label="${title}"><header class="evo-simple-section-head"><h2>${title}</h2>${p?evoChangeBadge(p):kbBadge('尚未开始','')}</header>${trial||released?`<div id="evoTrafficBlock"${released?' hidden':''}><div class="evo-traffic-track" role="img" aria-label="${escapeHTML(`候选或新版流量 ${percent}%，原版 ${100-percent}%`)}"><span style="width:${Math.max(0,Math.min(100,percent))}%"></span></div><div class="evo-traffic-legend"><span>${released?'新版':'试用版'} · ${percent}% 流量</span><span>原版 · ${100-percent}%</span></div>${trial&&c.status==='running'?`<div class="evo-traffic-setting"><label for="evoTrafficPercent">试用流量</label><select id="evoTrafficPercent" class="kb-input" aria-label="灰度流量">${EVO_CANARY_PERCENTS.map(value=>`<option value="${value}"${value===percent?' selected':''}>${value}%</option>`).join('')}</select><button type="button" class="kb-btn quiet small" data-evo-traffic-apply="${escapeHTML(p.id)}" disabled>调整比例</button><small>已发生的运行保留在原分组</small></div>`:''}</div>`:''}${board?`<div class="evo-sample-board${released?' single':''}">${board}</div><div class="evo-dot-legend"><span><i class="clean"></i>一次成功</span><span><i class="rework"></i>返工后成功</span><span><i class="failed"></i>失败</span><span><i class="waiting"></i>等待运行</span></div><p class="evo-summary-scope">按结果分类的数量方块，非时间序列；评分针对受试角色或规则的节点，不是整次运行的语义质量。</p>`:''}<div class="evo-gray-decision"><div id="evoProblemBox" class="evo-decision-copy">${p?`<strong>${escapeHTML(message)}</strong>`:''}</div><div id="evoDetailActions" class="evo-detail-actions"></div></div><div id="evoTrialHost"></div><p class="evo-summary-scope">${released&&w&&!w.baselineReady?'发布前基线尚未就绪，不能宣称新版本改善。 ':''}${p&&evoLevelOf(p.assetKind,p.assetId)>=3?'L3：验证通过后自动灰度，灰度通过自动发布，退化自动回滚。':'试用、发布、停止与回滚需确认。'} 新运行按启动时的正文分组，历史回放不计入灰度。</p></section>`;
}
function evoQueueHTML(asset){
  const group=evoGroupForAsset(asset),queue=group?.queue||[];
  return `<section id="evoNextImprovements" class="evo-next-improvements"><div><h3>接下来${queue.length?` · ${queue.length} 项排队`:' · 暂无排队'}</h3><p>${queue.length?'等当前修改与发布后观察结束，再按最新版本生成和验证。':'同一角色或规则只处理一项当前改进，新问题不会另开并行候选。'}</p></div>${asset?`<button type="button" class="kb-btn quiet small" data-evo-queue="${escapeHTML(asset.assetKind)}" data-id="${escapeHTML(asset.assetId)}">${queue.length?'查看队列':'处理规则'}</button>`:''}</section>`;
}
function evoSimpleDetailHTML(m,p){
  const r=m.row,asset=p||r?.f,group=evoGroupForAsset(asset);
  const inspector=`<dialog id="evoInspection" class="evo-inspection" aria-labelledby="evoInspectionTitle"><header class="evo-inspection-head"><h2 id="evoInspectionTitle">修改与验证明细</h2><button type="button" class="kb-btn quiet small" data-evo-inspection-close aria-label="关闭明细">关闭</button></header><nav class="evo-detail-tabs evo-tabs" role="tablist" aria-label="改进明细">${[['validation','验证对比'],['change','完整修改'],['records','关联问题']].map(([key,label])=>`<button type="button" role="tab" id="evoDetailTab-${key}" data-evo-detail-tab="${key}" aria-controls="evoDetailPanel-${key}">${label}</button>`).join('')}</nav><section id="evoDetailPanel-validation" role="tabpanel" aria-labelledby="evoDetailTab-validation"><div class="evo-inspect-workspace"><section class="evo-detail-box" id="evoEffectBox"><h2>验证结果</h2><div id="evoEffectSummary">${p?evoEffectSummaryHTML(p):'<p class="evo-muted">还没有修改或验证结果</p>'}</div><div id="evoComparisonList"></div></section><div data-evo-evidence-slot="validation"></div></div></section><section id="evoDetailPanel-change" role="tabpanel" aria-labelledby="evoDetailTab-change"><section class="evo-detail-box" id="evoLinkedChange"><h2>完整修改</h2>${p?.candidateContent?evoDiffHTML(p.baseContent,p.candidateContent):'<p class="evo-muted">还没有候选正文</p>'}</section></section><section id="evoDetailPanel-records" role="tabpanel" aria-labelledby="evoDetailTab-records"><div class="evo-related-issues">${(group?.issues||[]).map(issue=>`<button type="button" class="kb-btn small" data-evo-related-finding="${escapeHTML(issue.f.id)}" data-cluster="${escapeHTML(issue.key)}" aria-pressed="${issue.f.id===r?.f.id&&issue.key===r?.key}">${escapeHTML(issue.title)}</button>`).join('')}</div><div class="evo-inspect-workspace"><section class="evo-detail-box" id="evoProblemRecords">${evoProblemHTML(r,p)}</section><div data-evo-evidence-slot="records"></div></div></section><aside class="evo-evidence-panel" aria-label="运行证据"><header><h2 id="evoEvidenceTitle">运行证据</h2><span id="evoEvidenceCount" class="evo-muted"></span></header><div id="evoEvidenceBody" aria-live="polite"><div class="evo-evidence-empty">选择一条样本查看结果</div></div></aside></dialog>`;
  return `<section class="evo-detail-box" id="evoChangeSummary" aria-label="修改摘要"><header class="evo-simple-section-head"><h2>改了什么</h2><button type="button" class="kb-btn quiet small" data-evo-inspect="change">查看完整修改</button></header>${evoSummaryHTML(p)}<div class="evo-summary-foot"><span>${p?.summary?.finished?`历史验证 ${p.summary.finished} 条${p.summary.passed?' · 通过':' · 未通过或待判定'}`:'尚无历史验证结果'}</span><button type="button" class="kb-btn quiet small" data-evo-inspect="records">关联问题 ${group?.issues.length||0} 类</button></div><p id="evoLiveVersionMain" class="evo-live-note">${p?'正在核对当前版本':'尚未生成候选，线上正文没有改变。'}</p></section>${evoSimpleTrack(p)}${evoGrayBoardHTML(p)}${evoQueueHTML(asset)}${group?.conflict?'<p class="evo-bad evo-conflict-note">检测到同对象存在多个进行中的修改；已禁止发布或灰度，请先在历史尝试中取消冲突候选。</p>':''}<div class="evo-secondary-links"><button type="button" class="kb-btn quiet small" data-evo-inspect="validation">验证依据与原始记录</button>${asset?`<button type="button" class="kb-btn quiet small" data-evo-asset="${escapeHTML(asset.assetKind)}" data-id="${escapeHTML(asset.assetId)}">历史版本</button>`:''}${p?`<button type="button" class="kb-btn quiet small" data-evo-process="${escapeHTML(p.id)}">完整处理过程</button><button type="button" class="kb-btn quiet small" data-evo-detail-refresh>刷新状态</button>`:''}</div>${inspector}`;
}
function evoOpenInspection(key,opener){
  const m=evo.modal,dialog=$('#evoInspection');if(!m||!dialog)return;
  m.inspection=key;m.detailTab=key;m.inspectionOpener=opener||m.inspectionOpener;
  window.HuiPopover?.closeAll();
  if(!dialog.open){if(dialog.showModal)dialog.showModal();else dialog.setAttribute('open','')}
  evoSetDetailTab(key,!!opener);$('#evoInspection [data-evo-inspection-close]')?.focus({preventScroll:true});
}
function evoCloseInspection(restore=true){
  const m=evo.modal,dialog=$('#evoInspection');if(!m)return;
  m.inspection='';if(dialog?.open){if(dialog.close)dialog.close();else dialog.removeAttribute('open')}
  if(restore){const opener=m.inspectionOpener?.isConnected?m.inspectionOpener:$(`[data-evo-inspect="${m.detailTab||'validation'}"]`);(opener||$('#evoFindingTitle'))?.focus({preventScroll:true})}
}
function evoBindInspection(m){
  const dialog=$('#evoInspection');if(!dialog)return;
  dialog.addEventListener('cancel',event=>{event.preventDefault();evoCloseInspection()});
  dialog.addEventListener('click',event=>{if(event.target!==dialog)return;const box=dialog.getBoundingClientRect();if(event.clientX<box.left||event.clientX>box.right||event.clientY<box.top||event.clientY>box.bottom)evoCloseInspection()});
  dialog.addEventListener('keydown',event=>{
    if(event.key==='Escape'){event.preventDefault();evoCloseInspection();return}
    if(event.key!=='Tab')return;
    const controls=[...dialog.querySelectorAll('button:not(:disabled),a[href],input,select,[tabindex="0"]')].filter(b=>!b.closest('[hidden]')&&b.getClientRects().length),first=controls[0],last=controls.at(-1);
    if(first&&(event.shiftKey&&document.activeElement===first||!event.shiftKey&&document.activeElement===last)){event.preventDefault();(event.shiftKey?last:first).focus()}
  });
  if(m.inspection)evoOpenInspection(m.inspection);
}
function evoOpenQueue(kind,id){
  evoCloseInspection(false);
  const group=evoGroupForAsset({assetKind:kind,assetId:id}),items=evoAssetQueue(kind,id),busy=group?.proposals.some(evoOccupiesAsset),level=evoLevelOf(kind,id),key=evoAssetKey(kind,id);
  const pending=[...new Map((group?.issues||[]).filter(r=>r.f.status==='open'&&!r.f.proposalId&&!items.some(q=>q.findingId===r.f.id)).map(r=>[r.f.id,r])).values()];
  const body=`<p class="evo-panel-lead">同一角色或规则只处理一项当前改进。相关问题合并；后续改进等当前修改、验证、灰度与发布后观察结束。</p>${pending.map(r=>`<section class="evo-queue-item"><h3>${escapeHTML(r.title)}</h3><button type="button" class="kb-btn small" data-evo-propose="${escapeHTML(r.f.id)}"${level<2?' disabled':''}>${busy?'加入后续改进':'开始修改'}</button></section>`).join('')}${items.map((q,i)=>`<section class="evo-queue-item"><h3>${i+1}. ${escapeHTML(q.title||'后续改进')}</h3><p>尚未生成候选。开始时按最新正文重新生成并验证。</p><button type="button" class="kb-btn small" data-evo-queue-start="${escapeHTML(q.id)}"${busy||level<2?' disabled':''}>开始这项改进</button><button type="button" class="kb-btn quiet small" data-evo-queue-reject="${escapeHTML(q.id)}">取消排队</button></section>`).join('')||'<p class="evo-muted">暂无排队的改进。</p>'}${group?.proposals.length>1?`<h3>历史尝试与冲突候选</h3>${group.proposals.map(p=>`<button type="button" class="kb-btn small" data-evo-proposal="${escapeHTML(p.id)}">${escapeHTML(p.title||p.id)} · ${escapeHTML(evoChangeState(p).label)}</button>`).join('')}`:''}<p class="evo-muted">结束当前改进不会自动开始下一项。L3 仅自动推进当前候选的试用、发布与退化回滚。</p>`;
  evoDrawerOpen(group?.asset.assetName||id,'后续改进与处理规则',body,'',{openerKey:`asset:${key}`,wide:true});
}
async function evoQueueAction(id,start,btn){
  const context={pid:evo.projectId,detail:evo.modal},seq=kb.drawer.seq;
  if(!await afConfirm({title:start?'开始排队改进':'取消排队',message:start?'将读取当前最新正文，重新生成候选并执行历史验证，不直接使用旧候选。':'这项尚未生成候选的改进将退出队列。'}))return;
  if(context.pid!==evoProjectId()||!evoActive()||kb.drawer.seq!==seq)return;
  btn.disabled=true;
  try{const result=await api(`/api/evolution/queue/${encodeURIComponent(id)}/${start?'start':'reject'}`,{method:'POST',body:'{}'});toast(start?'已开始生成新的候选':'已取消排队','ok');if(kb.drawer.seq===seq)evoDrawerClose();await evoAfterChange(start?result.id:'',context)}catch(err){toast(err.message,'err');btn.disabled=false}
}
async function evoAdjustTraffic(id,btn){
  const context={pid:evo.projectId,detail:evo.modal},p=context.detail?.proposal,percent=Number($('#evoTrafficPercent')?.value);
  if(!p||p.id!==id||p.status!=='canary'||p.canary?.status!=='running'||!EVO_CANARY_PERCENTS.includes(percent))return;
  if(percent===p.canary.percent){toast('当前已使用该比例','ok');return}
  if(!await afConfirm({title:'调整灰度流量',message:`将试用流量从 ${p.canary.percent}% 调整为 ${percent}%。已发生的运行和统计保留；只影响之后启动的新运行。`}))return;
  if(evo.modal!==context.detail||context.pid!==evoProjectId())return;
  btn.disabled=true;
  try{await api(`/api/evolution/proposals/${encodeURIComponent(id)}/canary/traffic`,{method:'POST',body:JSON.stringify({percent})});toast('已调整灰度流量','ok');await evoAfterChange(id,context)}catch(err){toast(err.message,'err');if(btn.isConnected)btn.disabled=false}
}
document.addEventListener('click',event=>{
  const button=event.target.closest('[data-evo-inspect],[data-evo-inspection-close],[data-evo-queue],[data-evo-queue-start],[data-evo-queue-reject],[data-evo-related-finding],[data-evo-traffic-apply]');if(!button||button.disabled)return;
  if(button.hasAttribute('data-evo-traffic-apply'))return evoAdjustTraffic(button.dataset.evoTrafficApply,button);
  if(button.hasAttribute('data-evo-inspect'))return evoOpenInspection(button.dataset.evoInspect,button);
  if(button.hasAttribute('data-evo-inspection-close'))return evoCloseInspection();
  if(button.hasAttribute('data-evo-queue'))return evoOpenQueue(button.dataset.evoQueue,button.dataset.id);
  if(button.hasAttribute('data-evo-queue-start'))return evoQueueAction(button.dataset.evoQueueStart,true,button);
  if(button.hasAttribute('data-evo-queue-reject'))return evoQueueAction(button.dataset.evoQueueReject,false,button);
  if(button.hasAttribute('data-evo-related-finding')){evoCloseInspection(false);evoOpenFinding(button.dataset.evoRelatedFinding,button.dataset.cluster);if(evo.modal)evoOpenInspection('records',button)}
});
