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
  const current=group.issues.filter(r=>!group.p||r.f.id===group.p.findingId||r.f.proposalId===group.p.id),runs=new Set();
  for(const r of current){for(const x of r.c.occurrences||[])if(x.runId)runs.add(x.runId);for(const x of r.f.sampleRunIds||[])runs.add(x)}
  const queued=evoFollowupCount(group);
  return `${runs.size?`${runs.size} 条问题运行`:current.length?`${current.length} 条问题线索`:'未关联问题样本'}${queued?` · 后续建议 ${queued}`:''}${group.proposals.length>1?` · 历史尝试 ${group.proposals.length-1}`:''}`;
}
function evoFollowupCount(group){
  if(!group)return 0;
  const current=group.p?.findingId||group.r?.f.id;
  const pending=new Set((group.issues||[]).filter(r=>r.f.id!==current&&r.f.status==='open'&&!r.f.proposalId&&!group.queue.some(q=>q.findingId===r.f.id)).map(r=>r.f.id));
  return group.queue.length+pending.size;
}
function evoOpenQueue(kind,id){
  const group=evoGroupForAsset({assetKind:kind,assetId:id}),items=evoAssetQueue(kind,id),busy=group?.proposals.some(evoOccupiesAsset),level=evoLevelOf(kind,id),key=evoAssetKey(kind,id);
  const pending=[...new Map((group?.issues||[]).filter(r=>r.f.status==='open'&&!r.f.proposalId&&!items.some(q=>q.findingId===r.f.id)).map(r=>[r.f.id,r])).values()];
  const body=`<p class="evo-panel-lead">同一角色或规则只处理一项当前改进。相关问题合并；后续改进等当前修改、验证、灰度与发布后观察结束。</p>${pending.map(r=>`<section class="evo-queue-item"><h3>${escapeHTML(r.title)}</h3><button type="button" class="kb-btn small" data-evo-propose="${escapeHTML(r.f.id)}"${level<2?' disabled':''}>${busy?'加入后续改进':'开始修改'}</button></section>`).join('')}${items.map((q,i)=>`<section class="evo-queue-item"><h3>${i+1}. ${escapeHTML(q.title||'后续改进')}</h3><p>尚未生成修改稿。开始时按最新正文重新生成并验证。</p><button type="button" class="kb-btn small" data-evo-queue-start="${escapeHTML(q.id)}"${busy||level<2?' disabled':''}>开始这项改进</button><button type="button" class="kb-btn quiet small" data-evo-queue-reject="${escapeHTML(q.id)}">取消排队</button></section>`).join('')||'<p class="evo-muted">暂无排队的改进。</p>'}${group?.proposals.length>1?`<h3>历史尝试与冲突修改稿</h3>${group.proposals.map(p=>`<button type="button" class="kb-btn small" data-evo-proposal="${escapeHTML(p.id)}">${escapeHTML(p.title||p.id)} · ${escapeHTML(evoChangeState(p).label)}</button>`).join('')}`:''}<p class="evo-muted">结束当前改进不会自动开始下一项。L3 仅自动推进当前修改稿的试用、发布与退化回滚。</p>`;
  evoDrawerOpen(group?.asset.assetName||id,'后续改进与处理规则',body,'',{openerKey:`asset:${key}`,wide:true});
}
async function evoQueueAction(id,start,btn){
  const context={pid:evo.projectId,detail:evo.modal},seq=kb.drawer.seq;
  if(!await afConfirm({title:start?'开始排队改进':'取消排队',message:start?'将读取当前最新正文，重新生成修改稿并执行历史验证，不直接使用旧修改稿。':'这项尚未生成修改稿的改进将退出队列。'}))return;
  if(context.pid!==evoProjectId()||!evoActive()||kb.drawer.seq!==seq)return;
  btn.disabled=true;
  try{const result=await api(`/api/evolution/queue/${encodeURIComponent(id)}/${start?'start':'reject'}`,{method:'POST',body:'{}'});toast(start?'已开始生成新的修改稿':'已取消排队','ok');if(kb.drawer.seq===seq)evoDrawerClose();await evoAfterChange(start?result.id:'',context)}catch(err){toast(err.message,'err');btn.disabled=false}
}
async function evoAdjustTraffic(id,btn){
  const context={pid:evo.projectId,detail:evo.modal},p=context.detail?.proposal,percent=Number($('#evoTrafficPercent')?.value);
  if(!p||p.id!==id||p.status!=='canary'||p.canary?.status!=='running'||!EVO_CANARY_PERCENTS.includes(percent))return;
  if(percent===p.canary.percent){toast('当前已使用该比例','ok');return}
  if(!await afConfirm({title:'调整灰度流量',message:`将试用流量从 ${p.canary.percent}% 调整为 ${percent}%。已发生的运行和统计保留；只影响之后启动的新运行。`}))return;
  if(evo.modal!==context.detail||context.pid!==evoProjectId())return;
  btn.disabled=true;
  try{await api(`/api/evolution/proposals/${encodeURIComponent(id)}/canary/traffic`,{method:'POST',body:JSON.stringify({percent})});toast('已调整灰度流量','ok');await evoAfterChange(id,context)}catch(err){toast(err.message,'err');if(btn.isConnected)evoSyncTraffic()}
}
document.addEventListener('click',event=>{
  const button=event.target.closest('[data-evo-queue],[data-evo-queue-start],[data-evo-queue-reject],[data-evo-related-finding],[data-evo-traffic-apply]');if(!button||button.disabled)return;
  if(button.hasAttribute('data-evo-traffic-apply'))return evoAdjustTraffic(button.dataset.evoTrafficApply,button);
  if(button.hasAttribute('data-evo-queue'))return evoOpenQueue(button.dataset.evoQueue,button.dataset.id);
  if(button.hasAttribute('data-evo-queue-start'))return evoQueueAction(button.dataset.evoQueueStart,true,button);
  if(button.hasAttribute('data-evo-queue-reject'))return evoQueueAction(button.dataset.evoQueueReject,false,button);
  if(button.hasAttribute('data-evo-related-finding')){evoOpenFinding(button.dataset.evoRelatedFinding,button.dataset.cluster);if(evo.modal)evoSetDetailTab('records')}
});
