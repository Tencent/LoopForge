// 进化中心：看板（问题 / 验证中 / 待确认 / 最近）、资产与策略、回归样本三个标签。
// 复用 knowledge.js 的 kb-* 面板、行、徽标与右侧抽屉（#kbDrawer），只在 evolution.css 补看板与 diff。
// 依赖 app.js 的全局 state、api、$、escapeHTML、toast、afConfirm、afPrompt、relativeTime、switchView、showRunDetail、isPlatformAdmin。
const evo={tab:'board',projectId:null,overview:null,samples:null,error:'',seq:0,poll:0,busy:new Set()};

const EVO_LEVELS=[[0,'L0 关闭','不诊断、不生成提案'],[1,'L1 只报告','只诊断出问题，不生成候选'],[2,'L2 提案回放','生成候选并回放验证，人工确认后发布']];
const EVO_PROPOSAL_STATUS={drafting:['生成中','warn'],validating:['回放中','warn'],pending:['待确认','ok'],released:['已发布','ok'],rejected:['已驳回',''],failed:['未通过','err'],rolled_back:['已回滚','']};
const EVO_GROUPS={diagnosis:['诊断样本','改进器看过的失败运行'],holdout:['留出样本','改进器没看过的失败运行'],regression:['回归样本','近期一次成功的运行'],pinned:['钉选样本','人工钉住的运行']};
const EVO_OUTCOMES={fixed:['修好','ok'],regressed:['退化','err'],unchanged:['无变化',''],blocked:['阻塞','warn']};
const EVO_SEVERITY={high:['高','err'],medium:['中','warn'],low:['低','']};
const EVO_SIGNALS={node_failed:'节点失败',rework:'打回返工',eval_low:'评测低分',user_feedback:'用户反馈'};
const EVO_SOURCES={baseline:'基线',manual:'手动修改',evolution:'进化发布',rollback:'回滚'};
const EVO_KINDS={agent:'角色',rule:'规则'};
const EVO_SCORES=['失败','返工后成功','一次成功'];
const EVO_POLL_MS=15000;

function evoProjectId(){return state.projectFilter||''}
function evoActive(){return !!$('#evolutionView')?.classList.contains('active')}
function evoKindLabel(kind){return EVO_KINDS[kind]||kind||'-'}
function evoStatusBadge(map,key){const [label,tone]=map[key]||[key||'-',''];return kbBadge(escapeHTML(label),tone)}
function evoLevelOf(kind,id){
  const h=(evo.overview?.health||[]).find(x=>x.assetKind===kind&&x.assetId===id);
  if(h)return h.level;
  const p=(evo.overview?.policies||[]).find(x=>x.assetKind===kind&&x.assetId===id);
  return p?p.level:2;
}
function evoScoreLabel(score){return score>=0&&score<EVO_SCORES.length?EVO_SCORES[score]:'进行中'}
function evoRunLink(id,label){return id?`<a href="#" class="evo-run-link mono" data-kb-run="${escapeHTML(id)}" title="${escapeHTML(id)}">${escapeHTML(label||id.slice(-12))}</a>`:'—'}
function evoAssetKey(kind,id){return `${kind}:${id}`}

// ---------- 行级 diff：LCS，超大正文退化为集合差 ----------
function evoLineDiff(oldText,newText){
  const a=String(oldText||'').split('\n'),b=String(newText||'').split('\n');
  if(a.length*b.length>2500000){
    const as=new Set(a),bs=new Set(b);
    return [...a.filter(l=>!bs.has(l)).map(l=>['del',l]),...b.filter(l=>!as.has(l)).map(l=>['add',l])];
  }
  const n=a.length,m=b.length,dp=Array.from({length:n+1},()=>new Uint32Array(m+1));
  for(let i=n-1;i>=0;i--)for(let j=m-1;j>=0;j--)dp[i][j]=a[i]===b[j]?dp[i+1][j+1]+1:Math.max(dp[i+1][j],dp[i][j+1]);
  const out=[];let i=0,j=0;
  while(i<n&&j<m){if(a[i]===b[j]){out.push(['ctx',a[i]]);i++;j++}else if(dp[i+1][j]>=dp[i][j+1])out.push(['del',a[i++]]);else out.push(['add',b[j++]])}
  while(i<n)out.push(['del',a[i++]]);while(j<m)out.push(['add',b[j++]]);
  return out;
}
function evoDiffHTML(oldText,newText){
  const lines=evoLineDiff(oldText,newText);
  if(!lines.some(([k])=>k!=='ctx'))return '<div class="kb-line-note">与当前正文没有差异</div>';
  const keep=new Set();
  lines.forEach(([k],i)=>{if(k!=='ctx')for(let d=-2;d<=2;d++)keep.add(i+d)});
  const out=[];let skipped=0;
  const flush=()=>{if(skipped){out.push(`<div class="evo-diff-skip">… ${skipped} 行未改动</div>`);skipped=0}};
  lines.forEach(([k,l],i)=>{
    if(k==='ctx'&&!keep.has(i)){skipped++;return}
    flush();
    out.push(`<div class="evo-diff-line ${k}"><span aria-hidden="true">${k==='add'?'+':k==='del'?'-':' '}</span><code>${escapeHTML(l)||' '}</code></div>`);
  });
  flush();
  return `<div class="evo-diff" role="table" aria-label="正文差异">${out.join('')}</div>`;
}

// ---------- 入口与数据 ----------
async function renderEvolutionView(){
  const root=$('#evolutionBody');
  if(!root)return;
  if(evo.projectId!==null&&evo.projectId!==evoProjectId()){evoDrawerClose();evo.overview=null;evo.samples=null}
  evo.projectId=evoProjectId();
  if(!evo.projectId){evo.overview=null;evoRenderMetrics();root.innerHTML='<div class="kb-empty">请先在顶部选择一个项目，进化中心按项目诊断角色与规则</div>';return}
  if(!evo.overview)root.innerHTML='<div class="kb-empty">正在读取进化概览…</div>';
  await evoRefresh();
  evoRender();
}

async function evoRefresh(){
  const pid=evo.projectId,seq=++evo.seq,q=encodeURIComponent(pid);
  try{
    const [overview,samples]=await Promise.all([
      api(`/api/evolution/overview?projectId=${q}`,{silent:true}),
      api(`/api/evolution/samples?projectId=${q}`,{silent:true}).catch(()=>[]),
    ]);
    if(seq!==evo.seq||pid!==evoProjectId())return;
    evo.overview=overview;evo.samples=samples||[];evo.error='';
  }catch(err){if(seq===evo.seq){evo.error=err.message;evo.overview=null}}
}

function evoSchedulePoll(){
  clearTimeout(evo.poll);
  if(!evoActive()||!(evo.overview?.validating||[]).length)return;
  evo.poll=setTimeout(async()=>{
    if(!evoActive()||document.hidden)return evoSchedulePoll();
    await evoRefresh();evoRender();
  },EVO_POLL_MS);
}

function evoRenderMetrics(){
  const host=$('#evoMetrics');if(!host)return;
  const o=evo.overview;
  if(!o){host.innerHTML=['待处理问题','回放中','待确认','近期已发布'].map(l=>kbMetric(l,'—','',{empty:true})).join('');return}
  const released=(o.recent||[]).filter(p=>p.status==='released').length;
  const high=(o.findings||[]).filter(f=>f.severity==='high').length;
  host.innerHTML=
    kbMetric('待处理问题',String((o.findings||[]).length),high?`${high} 个高严重度`:`近 ${o.windowDays||14} 天信号`)+
    kbMetric('回放中',String((o.validating||[]).length),'候选正文正在回放历史运行')+
    kbMetric('待确认',String((o.pending||[]).length),'回放通过，等待人工发布')+
    kbMetric('近期已发布',String(released),o.generatedAt?`${relativeTime(o.generatedAt)}更新`:'');
}

function evoRender(){
  const root=$('#evolutionBody');if(!root)return;
  evoRenderMetrics();
  if(evo.error){root.innerHTML=`<div class="kb-note kb-err">读取失败：${escapeHTML(evo.error)}</div>`;return}
  if(!evo.overview)return;
  const tabs=[['board','看板'],['assets','资产与策略'],['samples','回归样本']];
  const counts={board:(evo.overview.findings||[]).length+(evo.overview.pending||[]).length,assets:(evo.overview.health||[]).length,samples:(evo.samples||[]).length};
  const focused=document.activeElement?.closest?.('#evolutionBody')?document.activeElement.dataset.evoKey:'';
  root.innerHTML=`<div class="kb-tabs" role="tablist" aria-label="进化中心">${tabs.map(([k,t])=>`<button type="button" role="tab" id="evoTab-${k}" data-evo-tab="${k}" aria-selected="${evo.tab===k}" class="${evo.tab===k?'active':''}">${t}<span class="kb-count">${counts[k]||0}</span></button>`).join('')}</div><div id="evoPane"></div>`;
  const pane=$('#evoPane');
  if(evo.tab==='assets')pane.innerHTML=evoAssetsHTML();
  else if(evo.tab==='samples')pane.innerHTML=evoSamplesHTML();
  else pane.innerHTML=evoBoardHTML();
  if(focused)root.querySelector(`[data-evo-key="${CSS.escape(focused)}"]`)?.focus({preventScroll:true});
  evoSchedulePoll();
}

// ---------- 看板 ----------
function evoColumn(title,meta,items,empty){
  return `<section class="kb-panel evo-col" aria-label="${escapeHTML(title)}"><div class="kb-panel-head">${kbPanelTitle(escapeHTML(title),`${items.length}`)}${meta?`<span class="kb-panel-meta evo-col-meta">${meta}</span>`:''}</div>${items.join('')||`<div class="kb-empty">${empty}</div>`}</section>`;
}
function evoFindingCard(f){
  const level=evoLevelOf(f.assetKind,f.assetId);
  const key=`finding:${f.id}`;
  const desc=`${evoKindLabel(f.assetKind)} · ${escapeHTML(f.assetName||f.assetId)} · ${(f.signals||[]).length} 条信号`;
  return `<div class="kb-row kb-go evo-card" role="button" tabindex="0" data-evo-finding="${escapeHTML(f.id)}" data-evo-key="${escapeHTML(key)}"><div class="kb-row-main">${kbText(escapeHTML(f.title),desc)}<div class="evo-card-foot">${evoStatusBadge(EVO_SEVERITY,f.severity)}${level<2?kbBadge('只报告','', '该资产的自治级别低于 L2，不会生成提案'):''}<span>${relativeTime(f.updatedAt||f.createdAt)}</span></div></div></div>`;
}
function evoProposalCard(p){
  const s=p.summary||{};
  const key=`proposal:${p.id}`;
  let progress='';
  if(p.status==='validating'||p.status==='drafting'){
    const pct=s.total?Math.round((s.finished||0)/s.total*100):0;
    progress=`<div class="evo-progress" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${pct}" aria-label="回放进度"><i style="width:${pct}%"></i></div><div class="evo-card-note">${p.status==='drafting'?'改进器正在生成候选…':`回放 ${s.finished||0}/${s.total||0}`}</div>`;
  }else if(s.verdict||p.error){
    progress=`<div class="evo-card-note${p.status==='failed'?' kb-err':''}">${escapeHTML(p.error||s.verdict)}</div>`;
  }
  const desc=`${evoKindLabel(p.assetKind)} · ${escapeHTML(p.assetName||p.assetId)}`;
  return `<div class="kb-row kb-go evo-card" role="button" tabindex="0" data-evo-proposal="${escapeHTML(p.id)}" data-evo-key="${escapeHTML(key)}"><div class="kb-row-main">${kbText(escapeHTML(p.title||'改进提案'),desc)}${progress}<div class="evo-card-foot">${evoStatusBadge(EVO_PROPOSAL_STATUS,p.status)}${p.releasedVersion?`<span class="mono">v${p.releasedVersion}</span>`:''}<span>${relativeTime(p.decidedAt||p.updatedAt||p.createdAt)}</span></div></div></div>`;
}
function evoBoardHTML(){
  const o=evo.overview;
  const cols=[
    evoColumn('问题','归因到角色与规则',(o.findings||[]).map(evoFindingCard),'近期没有需要处理的问题'),
    evoColumn('验证中','候选正文回放历史运行',(o.validating||[]).map(evoProposalCard),'没有正在回放的提案'),
    evoColumn('待确认','回放通过，人工确认后发布',(o.pending||[]).map(evoProposalCard),'没有等待确认的提案'),
    evoColumn('最近','已发布、驳回或未通过',(o.recent||[]).map(evoProposalCard),'还没有处理过的提案'),
  ];
  return `<div class="evo-board">${cols.join('')}</div>`;
}

// ---------- 资产与策略 ----------
function evoAssetsHTML(){
  const items=evo.overview.health||[];
  const levelSeg=h=>`<div class="kb-seg evo-level" aria-label="${escapeHTML((h.assetName||h.assetId)+' 自治级别')}">${EVO_LEVELS.map(([v,t,title])=>`<button type="button" data-evo-level="${v}" data-kind="${escapeHTML(h.assetKind)}" data-id="${escapeHTML(h.assetId)}" class="${h.level===v?'active':''}" title="${escapeHTML(title)}"${evo.busy.has(evoAssetKey(h.assetKind,h.assetId))?' disabled':''}>${t.split(' ')[0]}</button>`).join('')}</div>`;
  const rows=items.map(h=>{
    const rate=h.runs?`${Math.round((h.successRate||0)*100)}%`:'—';
    const desc=`${evoKindLabel(h.assetKind)} · ${h.runs||0} 次运行 · 失败 ${h.failed||0} · 返工 ${h.reworked||0}${h.latestVersion?` · v${h.latestVersion}`:''}`;
    const key=`asset:${evoAssetKey(h.assetKind,h.assetId)}`;
    return `<div class="kb-row evo-asset-row"><div class="kb-row-main evo-asset-main kb-go" role="button" tabindex="0" data-evo-asset="${escapeHTML(h.assetKind)}" data-id="${escapeHTML(h.assetId)}" data-evo-key="${escapeHTML(key)}">${kbText(escapeHTML(h.assetName||h.assetId),desc)}</div>${h.openFindings?kbBadge(`${h.openFindings} 个问题`,'warn'):''}${kbValue(rate,'evo-rate','近期成功率')}${levelSeg(h)}</div>`;
  });
  const note=kbCaption('L0 关闭不诊断；L1 只报告问题；L2（默认）生成候选并回放验证，人工确认后才会发布。点资产名查看版本时间线与回滚。');
  return kbPanel(kbPanelTitle('资产健康度',`近 ${evo.overview.windowDays||14} 天`),note+(rows.join('')||'<div class="kb-empty">近期没有运行用到可进化的角色或规则</div>'),{label:'资产健康度'});
}

// ---------- 回归样本 ----------
function evoSamplesHTML(){
  const rows=(evo.samples||[]).map(s=>kbRow(kbText(evoRunLink(s.runId,s.runId),escapeHTML(s.note||'无备注')+(s.workflowId?` · ${escapeHTML(s.workflowId)}`:'')),`${kbValue(relativeTime(s.createdAt))}<button type="button" class="kb-btn small" data-evo-unpin="${escapeHTML(s.runId)}">移除</button>`));
  const note=kbCaption('钉选样本每次提案都会回放，用来守住已知的好结果。在运行详情页点「钉为回归样本」添加。');
  return kbPanel(kbPanelTitle('回归样本',`${rows.length} 个`),note+(rows.join('')||'<div class="kb-empty">还没有钉选样本</div>'),{label:'回归样本'});
}

// ---------- 抽屉 ----------
function evoDrawerOpen(title,sub,body,foot,opt={}){
  const seq=kbDrawerOpen(title,sub,body,foot,opt);
  const drawer=$('#kbDrawer');
  drawer?.classList.add('evo-owned');
  drawer?.classList.toggle('evo-wide',!!opt.wide);
  return seq;
}
function evoDrawerOwned(){return !!$('#kbDrawer')?.classList.contains('evo-owned')}
function evoDrawerClose(){if(kbDrawerIsOpen())kbDrawerClose();$('#kbDrawer')?.classList.remove('evo-owned','evo-wide')}
function evoDrawerLive(seq){return seq===kb.drawer.seq&&kbDrawerIsOpen()}

function evoOpenFinding(id){
  const f=(evo.overview?.findings||[]).find(x=>x.id===id);if(!f)return;
  const level=evoLevelOf(f.assetKind,f.assetId);
  const signals=(f.signals||[]).map(s=>kbRow(kbText(escapeHTML(EVO_SIGNALS[s.kind]||s.kind)+(s.nodeId?` · <span class="mono">${escapeHTML(s.nodeId)}</span>`:''),escapeHTML(s.detail||'')),kbValue(evoRunLink(s.runId))));
  const body=`${f.summary?`<p class="kb-lead">${escapeHTML(f.summary)}</p>`:''}${kbCaption(`信号 · ${signals.length}`)}${signals.join('')||'<div class="kb-line-note">没有信号明细</div>'}${kbCaption('样本运行')}<div class="evo-chips">${(f.sampleRunIds||[]).map(r=>evoRunLink(r)).join('')||'—'}</div>`;
  const foot=`<button type="button" class="kb-btn quiet" data-evo-dismiss="${escapeHTML(f.id)}">忽略</button><button type="button" class="kb-btn" data-evo-asset="${escapeHTML(f.assetKind)}" data-id="${escapeHTML(f.assetId)}">版本</button><button type="button" class="kb-btn primary" data-evo-propose="${escapeHTML(f.id)}"${level<2?' disabled title="自治级别低于 L2，先在「资产与策略」调到 L2"':''}>生成提案</button>`;
  evoDrawerOpen(f.title,`${evoKindLabel(f.assetKind)} · ${escapeHTML(f.assetName||f.assetId)} · 严重度 ${escapeHTML((EVO_SEVERITY[f.severity]||[f.severity])[0])}`,body,foot,{openerKey:`finding:${f.id}`});
}

function evoReplayRow(r){
  const [group,groupHint]=EVO_GROUPS[r.group]||[r.group,''];
  const reused=(r.reusedNodes||[]).length;
  const reuseNote=reused?` · <span title="${escapeHTML(r.reusedNodes.join('、'))}">沿用原运行 ${reused} 个上游节点</span>`:'';
  const main=kbText(`<span title="${escapeHTML(groupHint)}">${escapeHTML(group)}</span> · 原运行 ${evoRunLink(r.sourceRunId)}${r.runId?` → 回放 ${evoRunLink(r.runId)}`:''}`,`${escapeHTML(evoScoreLabel(r.sourceScore))} → ${escapeHTML(r.status==='running'||!r.outcome?'回放中':evoScoreLabel(r.score))}${reuseNote}${r.error?` · ${escapeHTML(r.error)}`:''}`);
  return kbRow(main,r.outcome?evoStatusBadge(EVO_OUTCOMES,r.outcome):kbBadge('进行中','warn'));
}

async function evoOpenProposal(id){
  const seq=evoDrawerOpen('改进提案','',`<div class="kb-empty">正在读取提案…</div>`,'',{openerKey:`proposal:${id}`,wide:true});
  try{
    const p=await api(`/api/evolution/proposals/${encodeURIComponent(id)}`,{silent:true});
    if(!evoDrawerLive(seq))return;
    const s=p.summary||{};
    const verdict=s.verdict||p.error||'';
    const stats=`<div class="evo-stats">${[['修好',s.fixed,'ok'],['退化',s.regressed,'err'],['无变化',s.unchanged,''],['阻塞',s.blocked,'warn']].map(([l,v,t])=>`<div><span>${l}</span><b class="${t?`kb-${t}`:''}">${v||0}</b></div>`).join('')}<div><span>完成</span><b>${s.finished||0}/${s.total||0}</b></div></div>`;
    const replays=(p.replays||[]).map(evoReplayRow).join('');
    const diff=p.candidateContent?evoDiffHTML(p.baseContent,p.candidateContent):'<div class="kb-line-note">改进器还没有给出候选正文</div>';
    const body=`${verdict?`<div class="${p.status==='failed'?'kb-alert':'kb-note evo-verdict'}">${escapeHTML(verdict)}</div>`:''}${p.rationale?`${kbCaption('改进理由')}<p class="evo-rationale">${escapeHTML(p.rationale)}</p>`:''}${kbCaption('回放结果')}${stats}${replays||'<div class="kb-line-note">还没有回放</div>'}${kbCaption('正文差异（当前 → 候选）')}${diff}${p.decisionNote?`${kbCaption('处理备注')}<p class="evo-rationale">${escapeHTML(p.decisionNote)}</p>`:''}`;
    const asset=`<button type="button" class="kb-btn" data-evo-asset="${escapeHTML(p.assetKind)}" data-id="${escapeHTML(p.assetId)}">版本</button>`;
    let foot=asset;
    if(p.status==='pending')foot=`<button type="button" class="kb-btn quiet" data-evo-reject="${escapeHTML(p.id)}">驳回</button>${asset}<button type="button" class="kb-btn primary" data-evo-approve="${escapeHTML(p.id)}"${s.passed?'':' disabled'}>确认发布</button>`;
    else if(p.status==='validating'||p.status==='drafting')foot=`<button type="button" class="kb-btn quiet" data-evo-reject="${escapeHTML(p.id)}">取消提案</button>${asset}`;
    else if(p.status==='failed'&&p.candidateContent&&typeof isPlatformAdmin==='function'&&isPlatformAdmin())foot=`${asset}<button type="button" class="kb-btn" data-evo-approve="${escapeHTML(p.id)}" data-force="1" title="仅平台管理员：跳过回放判定直接发布">强制发布</button>`;
    const sub=`${evoKindLabel(p.assetKind)} · ${escapeHTML(p.assetName||p.assetId)} · ${evoStatusBadge(EVO_PROPOSAL_STATUS,p.status)}${p.model?` · ${escapeHTML(p.model)}`:''}`;
    evoDrawerOpen(p.title||'改进提案',sub,body,foot,{wide:true});
  }catch(err){if(evoDrawerLive(seq))$('#kbDrawerBody').innerHTML=`<div class="kb-empty">读取失败：${escapeHTML(err.message)}</div>`}
}

async function evoOpenAsset(kind,id,selected=0){
  const seq=evoDrawerOpen('版本时间线','',`<div class="kb-empty">正在读取版本…</div>`,'',{openerKey:`asset:${evoAssetKey(kind,id)}`,wide:true});
  try{
    const v=await api(`/api/evolution/assets/${encodeURIComponent(kind)}/${encodeURIComponent(id)}/versions`,{silent:true});
    if(!evoDrawerLive(seq))return;
    const versions=v.versions||[];
    const pick=versions.find(x=>x.version===selected)||null;
    const rows=versions.map(x=>{
      const desc=`${escapeHTML(EVO_SOURCES[x.source]||x.source)}${x.note?` · ${escapeHTML(x.note)}`:''} · ${relativeTime(x.createdAt)}`;
      const side=`${x.current?kbBadge('当前','ok'):''}${x.source==='evolution'&&x.proposalId?`<button type="button" class="kb-btn small" data-evo-proposal="${escapeHTML(x.proposalId)}">提案</button>`:''}<button type="button" class="kb-btn small" data-evo-version="${x.version}" data-kind="${escapeHTML(kind)}" data-id="${escapeHTML(id)}" aria-pressed="${pick?.version===x.version}">对比</button>${x.current?'':`<button type="button" class="kb-btn small" data-evo-rollback="${x.version}" data-kind="${escapeHTML(kind)}" data-id="${escapeHTML(id)}">回滚到此版</button>`}`;
      return kbRow(kbText(`<span class="mono">v${x.version}</span> <span class="mono evo-hash">${escapeHTML((x.hash||'').slice(0,8))}</span>`,desc),side,{cls:`evo-version${pick?.version===x.version?' is-selected':''}`});
    }).join('');
    const drift=v.drifted?'<div class="kb-note">当前正文不在版本记录里（在进化流程外被修改过）；下次发布或回滚前会先补记为「手动修改」版本。</div>':'';
    const compare=pick?`${kbCaption(`v${pick.version} → 当前正文`)}${evoDiffHTML(pick.content,v.liveContent)}`:`${kbCaption('当前正文')}<pre class="kb-pre evo-content">${escapeHTML(v.liveContent||'')}</pre>`;
    const level=(EVO_LEVELS.find(([l])=>l===v.level)||[])[1]||'';
    const body=`${drift}${kbCaption(`版本 · ${versions.length}${level?` · 自治级别 ${escapeHTML(level)}`:''}`)}${rows||'<div class="kb-line-note">还没有版本记录；首次发布或回滚时会把当前正文记为基线 v1。</div>'}${compare}`;
    const canPropose=v.level>=2;
    const foot=`<button type="button" class="kb-btn primary" data-evo-propose-asset="${escapeHTML(kind)}" data-id="${escapeHTML(id)}"${canPropose?'':' disabled title="自治级别低于 L2"'}>生成提案</button>`;
    evoDrawerOpen(v.assetName||id,`${evoKindLabel(kind)} · 当前 ${v.currentVersion?`v${v.currentVersion}`:'未登记'}`,body,foot,{wide:true});
  }catch(err){if(evoDrawerLive(seq))$('#kbDrawerBody').innerHTML=`<div class="kb-empty">读取失败：${escapeHTML(err.message)}</div>`}
}

// ---------- 操作 ----------
async function evoAfterChange(openProposal=''){
  await evoRefresh();evoRender();
  if(openProposal)evoOpenProposal(openProposal);
}

async function evoDiagnose(btn){
  if(!evo.projectId)return toast('请先选择项目','warn');
  if(btn)btn.disabled=true;
  try{
    const res=await api('/api/evolution/diagnose',{method:'POST',body:JSON.stringify({projectId:evo.projectId})});
    toast(`诊断完成：${res.runs||0} 次运行，${(res.findings||[]).length} 个问题`,'ok');
    await evoAfterChange();
  }catch(err){toast(err.message,'err')}
  finally{if(btn)btn.disabled=false}
}

async function evoPropose(payload,btn){
  if(btn)btn.disabled=true;
  try{
    const p=await api('/api/evolution/proposals',{method:'POST',body:JSON.stringify({projectId:evo.projectId,...payload})});
    toast('已开始生成候选正文，完成后自动回放','ok');
    await evoAfterChange(p.id);
  }catch(err){toast(err.message,'err');if(btn)btn.disabled=false}
}

async function evoDismiss(id){
  if(!await afConfirm({title:'忽略问题',message:'忽略后该问题不再出现在看板；之后出现新的信号会重新诊断。',confirmText:'忽略'}))return;
  try{await api(`/api/evolution/findings/${encodeURIComponent(id)}/dismiss`,{method:'POST',body:'{}'});evoDrawerClose();toast('已忽略','ok');await evoAfterChange()}
  catch(err){toast(err.message,'err')}
}

async function evoApprove(id,force,btn){
  const message=force?'回放没有通过判定。强制发布会直接把候选正文写入资产并记一个新版本，确定继续？':'发布后候选正文会写入资产，并记为新的不可变版本，之后的运行都会使用它；可以随时回滚。';
  const note=await afPrompt({title:force?'强制发布':'确认发布',message:`${message}\n\n备注（可选）：`,defaultValue:''});
  if(note===null||note===undefined)return;
  if(btn)btn.disabled=true;
  try{
    const p=await api(`/api/evolution/proposals/${encodeURIComponent(id)}/approve`,{method:'POST',body:JSON.stringify({note,force:!!force})});
    toast(`已发布 v${p.releasedVersion||''}`,'ok');
    await evoAfterChange(p.id);
  }catch(err){toast(err.message,'err');if(btn)btn.disabled=false}
}

async function evoReject(id,btn){
  const note=await afPrompt({title:'驳回提案',message:'驳回后正在进行的回放会被取消，问题重新回到看板。原因（可选）：',defaultValue:''});
  if(note===null||note===undefined)return;
  if(btn)btn.disabled=true;
  try{await api(`/api/evolution/proposals/${encodeURIComponent(id)}/reject`,{method:'POST',body:JSON.stringify({note})});toast('已驳回','ok');await evoAfterChange(id)}
  catch(err){toast(err.message,'err');if(btn)btn.disabled=false}
}

async function evoRollback(kind,id,version,btn){
  const note=await afPrompt({title:`回滚到 v${version}`,message:'回滚会把该版本的正文写回资产，并追加一个「回滚」版本；已经跑完的运行不受影响。备注（可选）：',defaultValue:''});
  if(note===null||note===undefined)return;
  if(btn)btn.disabled=true;
  try{
    const v=await api(`/api/evolution/assets/${encodeURIComponent(kind)}/${encodeURIComponent(id)}/rollback`,{method:'POST',body:JSON.stringify({version,note})});
    toast(`已回滚，当前为 v${v.version}`,'ok');
    await evoRefresh();evoRender();evoOpenAsset(kind,id);
  }catch(err){toast(err.message,'err');if(btn)btn.disabled=false}
}

async function evoSetLevel(kind,id,level){
  const key=evoAssetKey(kind,id);if(evo.busy.has(key))return;
  evo.busy.add(key);evoRender();
  try{await api(`/api/evolution/policies/${encodeURIComponent(kind)}/${encodeURIComponent(id)}`,{method:'PUT',body:JSON.stringify({level})});toast('自治级别已更新','ok')}
  catch(err){toast(err.message,'err')}
  finally{evo.busy.delete(key);await evoRefresh();evoRender()}
}

async function evoUnpin(runId,btn){
  if(btn)btn.disabled=true;
  try{await api(`/api/runs/${encodeURIComponent(runId)}/pin-sample`,{method:'DELETE'});toast('已移除样本','ok');await evoAfterChange()}
  catch(err){toast(err.message,'err');if(btn)btn.disabled=false}
}

// ---------- 运行详情：反馈、钉为样本、回放标记 ----------
function evoDecorateRunDetail(host,r){
  if(!host||!r)return;
  if(r.executionContext?.purpose==='evolution_replay'){
    host.querySelector('.run-header-meta')?.insertAdjacentHTML('afterbegin','<span class="run-meta-chip evo-replay-chip" title="候选正文的回放运行：不交付需求、不发通知、不计入统计">进化回放</span>');
    return;
  }
  if(!['succeeded','failed','cancelled'].includes(r.status)||!r.executionContext?.projectId)return;
  const actions=host.querySelector('.run-header-actions');if(!actions)return;
  actions.insertAdjacentHTML('afterbegin',`<button class="button ghost small" data-evo-feedback="${escapeHTML(r.id)}" title="把问题反馈给进化中心，参与角色与规则的诊断">反馈问题</button><button class="button ghost small" data-evo-pin="${escapeHTML(r.id)}" title="每次进化提案都会回放这次运行">钉为回归样本</button>`);
  actions.querySelector('[data-evo-feedback]')?.addEventListener('click',async ev=>{
    const btn=ev.currentTarget;
    const detail=await afPrompt({title:'反馈问题',message:'描述这次运行哪里不好（会作为用户反馈信号参与诊断）：',defaultValue:'',multiline:true});
    if(!detail||!String(detail).trim())return;
    btn.disabled=true;
    try{await api(`/api/runs/${encodeURIComponent(r.id)}/feedback`,{method:'POST',body:JSON.stringify({detail:String(detail).trim(),nodeId:state.selectedRunNode||''})});toast('反馈已记录','ok')}
    catch(err){toast(err.message,'err')}
    finally{btn.disabled=false}
  });
  actions.querySelector('[data-evo-pin]')?.addEventListener('click',async ev=>{
    const btn=ev.currentTarget;
    const note=await afPrompt({title:'钉为回归样本',message:'备注（可选），说明这次运行为什么值得守住：',defaultValue:''});
    if(note===null||note===undefined)return;
    btn.disabled=true;
    try{await api(`/api/runs/${encodeURIComponent(r.id)}/pin-sample`,{method:'POST',body:JSON.stringify({note})});toast('已钉为回归样本','ok')}
    catch(err){toast(err.message,'err')}
    finally{btn.disabled=false}
  });
}

// ---------- 全局交互 ----------
document.addEventListener('click',e=>{
  if(e.target.closest('#evoDiagnose'))return evoDiagnose(e.target.closest('#evoDiagnose'));
  const scope=e.target.closest('#evolutionView, #kbDrawer');if(!scope)return;
  if(scope.id==='kbDrawer'&&!evoDrawerOwned())return;
  const t=e.target.closest('[data-evo-tab],[data-evo-finding],[data-evo-proposal],[data-evo-asset],[data-evo-level],[data-evo-dismiss],[data-evo-propose],[data-evo-propose-asset],[data-evo-approve],[data-evo-reject],[data-evo-rollback],[data-evo-version],[data-evo-unpin]');
  if(!t||t.disabled)return;
  const d=t.dataset;
  if(d.evoTab){if(evo.tab!==d.evoTab){evo.tab=d.evoTab;evoRender()}$(`#evoTab-${d.evoTab}`)?.focus({preventScroll:true});return}
  if(d.evoFinding)return evoOpenFinding(d.evoFinding);
  if(d.evoProposal)return evoOpenProposal(d.evoProposal);
  if(d.evoAsset)return evoOpenAsset(d.evoAsset,d.id);
  if(d.evoLevel!==undefined){if(!t.classList.contains('active'))evoSetLevel(d.kind,d.id,Number(d.evoLevel));return}
  if(d.evoDismiss)return evoDismiss(d.evoDismiss);
  if(d.evoPropose)return evoPropose({findingId:d.evoPropose},t);
  if(d.evoProposeAsset)return evoPropose({assetKind:d.evoProposeAsset,assetId:d.id},t);
  if(d.evoApprove)return evoApprove(d.evoApprove,d.force==='1',t);
  if(d.evoReject)return evoReject(d.evoReject,t);
  if(d.evoRollback)return evoRollback(d.kind,d.id,Number(d.evoRollback),t);
  if(d.evoVersion)return evoOpenAsset(d.kind,d.id,Number(d.evoVersion));
  if(d.evoUnpin)return evoUnpin(d.evoUnpin,t);
});
document.addEventListener('click',e=>{
  const run=e.target.closest('#evolutionView [data-kb-run]');
  if(run){e.preventDefault();switchView('runs');showRunDetail(run.dataset.kbRun)}
});
(()=>{
  const view=document.getElementById('evolutionView');
  if(view)new MutationObserver(()=>{if(!view.classList.contains('active')){clearTimeout(evo.poll);if(evoDrawerOwned())evoDrawerClose()}}).observe(view,{attributes:true,attributeFilter:['class']});
  const drawer=document.getElementById('kbDrawer');
  if(drawer)new MutationObserver(()=>{if(!drawer.classList.contains('open')&&drawer.classList.contains('evo-owned'))drawer.classList.remove('evo-owned','evo-wide')}).observe(drawer,{attributes:true,attributeFilter:['class']});
})();
