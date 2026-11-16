// 进化中心：看板（问题 / 验证中 / 待确认 / 试用与观察 / 最近）、资产与策略、回归样本三个标签。
// 复用 knowledge.js 的 kb-* 面板、行、徽标与右侧抽屉（#kbDrawer），只在 evolution.css 补看板与 diff。
// 依赖 app.js 的全局 state、api、$、escapeHTML、toast、afConfirm、afPrompt、relativeTime、switchView、showRunDetail、isPlatformAdmin。
const evo={tab:'board',projectId:null,overview:null,samples:null,error:'',seq:0,poll:0,busy:new Set(),drawerProposal:null,stage:''};

const EVO_LEVELS=[[0,'L0 关闭','不诊断、不生成提案'],[1,'L1 只报告','只诊断出问题，不生成候选'],[2,'L2 提案回放','生成候选并回放验证；试用、发布、回滚都由人确认'],[3,'L3 自动','回放通过后自动试用，试用通过自动发布，发布后变差自动回滚']];
const EVO_PROPOSAL_STATUS={drafting:['生成中','warn'],validating:['回放中','warn'],pending:['待确认','ok'],canary:['试用中','warn'],released:['已发布','ok'],rejected:['已驳回',''],failed:['未通过','err'],rolled_back:['已回滚','']};
const EVO_CANARY_STATUS={running:['试用中','warn'],passed:['试用通过','ok'],failed:['试用未通过','err'],aborted:['已终止',''],promoted:['已全量','ok']};
const EVO_WATCH_STATUS={watching:['观察中','warn'],healthy:['表现正常','ok'],degraded:['退化','err'],rolled_back:['已回滚',''],ended:['观察结束','']};
const EVO_CANARY_PERCENTS=[10,25,50];
const EVO_CANARY_MIN_RUNS=[5,10,20];
const EVO_GROUPS={diagnosis:['诊断样本','改进器看过的失败运行'],holdout:['留出样本','改进器没看过的失败运行'],regression:['回归样本','近期一次成功的运行'],pinned:['钉选样本','人工钉住的运行']};
const EVO_OUTCOMES={fixed:['修好','ok'],regressed:['退化','err'],unchanged:['无变化',''],changed:['结论改变','info'],blocked:['阻塞','warn']};
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
// 行内高亮：一段删除行紧跟一段新增行时，按词（中文按字）再做一次 LCS，
// 只标出真正改动的片段；相似度太低（基本是重写）时不做，保持整行着色。
function evoTokens(s){return String(s).match(/[\u3400-\u9fff\uf900-\ufaff]|[A-Za-z0-9_.\-/]+|\s+|[^\sA-Za-z0-9_\u3400-\u9fff\uf900-\ufaff]/gu)||[]}
function evoWordDiff(delLines,addLines){
  const NL='\u0000';
  const join=lines=>lines.flatMap((l,i)=>i?[NL,...evoTokens(l)]:evoTokens(l));
  const a=join(delLines),b=join(addLines),n=a.length,m=b.length;
  if(!n||!m||n*m>1500000)return null;
  const dp=Array.from({length:n+1},()=>new Uint32Array(m+1));
  for(let i=n-1;i>=0;i--)for(let j=m-1;j>=0;j--)dp[i][j]=a[i]===b[j]?dp[i+1][j+1]+1:Math.max(dp[i+1][j],dp[i][j+1]);
  const ops=[];let i=0,j=0;
  while(i<n&&j<m){if(a[i]===b[j]){ops.push(['eq',a[i]]);i++;j++}else if(dp[i+1][j]>=dp[i][j+1])ops.push(['del',a[i++]]);else ops.push(['add',b[j++]])}
  while(i<n)ops.push(['del',a[i++]]);while(j<m)ops.push(['add',b[j++]]);
  const len=k=>ops.filter(([o,t])=>o===k&&t!==NL&&t.trim()).reduce((s,[,t])=>s+t.length,0);
  const same=len('eq');
  if(same<0.3*Math.max(same+len('del'),same+len('add')))return null;
  const del=[''],add=[''];
  for(const [o,t] of ops){
    if(t===NL){if(o!=='add')del.push('');if(o!=='del')add.push('');continue}
    const h=escapeHTML(t);
    if(o==='eq'){del[del.length-1]+=h;add[add.length-1]+=h}
    else if(o==='del')del[del.length-1]+=`<del class="evo-w">${h}</del>`;
    else add[add.length-1]+=`<ins class="evo-w">${h}</ins>`;
  }
  return del.length===delLines.length&&add.length===addLines.length?{del,add}:null;
}
function evoDiffStats(oldText,newText){
  const lines=evoLineDiff(oldText,newText);
  return {add:lines.filter(([k,l])=>k==='add'&&l.trim()).length,del:lines.filter(([k,l])=>k==='del'&&l.trim()).length};
}
function evoDiffHTML(oldText,newText){
  const lines=evoLineDiff(oldText,newText);
  if(!lines.some(([k])=>k!=='ctx'))return '<div class="kb-line-note">与当前正文没有差异</div>';
  const html=lines.map(([,l])=>escapeHTML(l)),inline=new Set();
  for(let i=0;i<lines.length;){
    if(lines[i][0]!=='del'){i++;continue}
    let d=i;while(d<lines.length&&lines[d][0]==='del')d++;
    let e=d;while(e<lines.length&&lines[e][0]==='add')e++;
    if(e>d){
      const w=evoWordDiff(lines.slice(i,d).map(x=>x[1]),lines.slice(d,e).map(x=>x[1]));
      if(w){w.del.forEach((h,k)=>{html[i+k]=h;inline.add(i+k)});w.add.forEach((h,k)=>{html[d+k]=h;inline.add(d+k)})}
    }
    i=e;
  }
  const keep=new Set();
  lines.forEach(([k],i)=>{if(k!=='ctx')for(let d=-2;d<=2;d++)keep.add(i+d)});
  const out=[];let skipped=0;
  const flush=()=>{if(skipped){out.push(`<div class="evo-diff-skip">… ${skipped} 行未改动</div>`);skipped=0}};
  lines.forEach(([k],i)=>{
    if(k==='ctx'&&!keep.has(i)){skipped++;return}
    flush();
    out.push(`<div class="evo-diff-line ${k}${inline.has(i)?' w':''}"><span aria-hidden="true">${k==='add'?'+':k==='del'?'-':' '}</span><code>${html[i]||' '}</code></div>`);
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
  if(!evoActive()||!((evo.overview?.validating||[]).length||(evo.overview?.trial||[]).length))return;
  evo.poll=setTimeout(async()=>{
    if(!evoActive()||document.hidden)return evoSchedulePoll();
    await evoRefresh();evoRender();
  },EVO_POLL_MS);
}

function evoRenderMetrics(){
  const host=$('#evoMetrics');if(!host)return;
  const o=evo.overview;
  if(!o){host.innerHTML=['待处理问题','回放中','待确认','试用与观察'].map(l=>kbMetric(l,'—','',{empty:true})).join('');return}
  const high=(o.findings||[]).filter(f=>f.severity==='high').length;
  const trial=o.trial||[],canaries=trial.filter(p=>p.status==='canary').length;
  host.innerHTML=
    kbMetric('待处理问题',String((o.findings||[]).length),high?`${high} 个高严重度`:`近 ${o.windowDays||14} 天信号`)+
    kbMetric('回放中',String((o.validating||[]).length),'候选正文正在回放历史运行')+
    kbMetric('待确认',String((o.pending||[]).length),'回放通过，等待试用或发布')+
    kbMetric('试用与观察',String(trial.length),`试用 ${canaries} · 发布后观察 ${trial.length-canaries}${o.generatedAt?` · ${relativeTime(o.generatedAt)}更新`:''}`);
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
function evoPct(n){return `${Math.round((n||0)*100)}%`}
function evoProgressBar(pct,label){return `<div class="evo-progress" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${pct}" aria-label="${escapeHTML(label)}"><i style="width:${pct}%"></i></div>`}
// 试用 / 观察卡片的进度：试用取两组中较少的一组，观察取发布后运行数。
function evoTrialProgress(p){
  const c=p.canary,w=p.watch;
  if(p.status==='canary'&&c){
    const need=c.minRuns||1,have=Math.min(c.canary?.runs||0,c.control?.runs||0);
    return {pct:Math.min(100,Math.round(have/need*100)),note:`试用 ${c.percent}% · 候选 ${c.canary?.runs||0}/${need} · 线上 ${c.control?.runs||0}/${need}`,badge:evoStatusBadge(EVO_CANARY_STATUS,c.status)};
  }
  if(w){
    const need=w.minRuns||1,have=w.current?.runs||0;
    return {pct:Math.min(100,Math.round(have/need*100)),note:`观察 v${w.prevVersion||'?'} → v${w.version} · ${have}/${need} 次运行`,badge:evoStatusBadge(EVO_WATCH_STATUS,w.status)};
  }
  return null;
}
function evoProposalCard(p){
  const s=p.summary||{};
  const key=`proposal:${p.id}`;
  let progress='',badge=evoStatusBadge(EVO_PROPOSAL_STATUS,p.status);
  const trial=(p.status==='canary'||p.watch?.status==='watching'||p.watch?.status==='degraded')?evoTrialProgress(p):null;
  if(p.status==='validating'||p.status==='drafting'){
    const pct=s.total?Math.round((s.finished||0)/s.total*100):0;
    progress=`${evoProgressBar(pct,'回放进度')}<div class="evo-card-note">${p.status==='drafting'?'改进器正在生成候选…':`回放 ${s.finished||0}/${s.total||0}`}</div>`;
  }else if(trial){
    const verdict=(p.status==='canary'?p.canary?.verdict:p.watch?.verdict)||'';
    const bad=p.watch?.status==='degraded';
    progress=`${evoProgressBar(trial.pct,p.status==='canary'?'试用进度':'观察进度')}<div class="evo-card-note">${escapeHTML(trial.note)}</div>${verdict?`<div class="evo-card-note${bad?' kb-err':''}">${escapeHTML(verdict)}</div>`:''}`;
    badge=trial.badge;
  }else if(s.verdict||p.error){
    progress=`<div class="evo-card-note${p.status==='failed'?' kb-err':''}">${escapeHTML(p.error||s.verdict)}</div>`;
  }
  const desc=`${evoKindLabel(p.assetKind)} · ${escapeHTML(p.assetName||p.assetId)}`;
  const auto=p.canary?.auto?kbBadge('L3 自动','info','由 L3 自治级别自动发起'):'';
  return `<div class="kb-row kb-go evo-card" role="button" tabindex="0" data-evo-proposal="${escapeHTML(p.id)}" data-evo-key="${escapeHTML(key)}"><div class="kb-row-main">${kbText(escapeHTML(p.title||'改进提案'),desc)}${evoMiniSteps(p)}${progress}<div class="evo-card-foot">${badge}${auto}${p.releasedVersion?`<span class="mono">v${p.releasedVersion}</span>`:''}<span>${relativeTime(p.decidedAt||p.updatedAt||p.createdAt)}</span></div></div></div>`;
}
function evoBoardHTML(){
  const o=evo.overview;
  const cols=[
    evoColumn('问题','归因到角色与规则',(o.findings||[]).map(evoFindingCard),'近期没有需要处理的问题'),
    evoColumn('验证中','候选正文回放历史运行',(o.validating||[]).map(evoProposalCard),'没有正在回放的提案'),
    evoColumn('待确认','回放通过，试用或直接发布',(o.pending||[]).map(evoProposalCard),'没有等待确认的提案'),
    evoColumn('试用与观察','真实运行上对比新旧正文',(o.trial||[]).map(evoProposalCard),'没有试用或观察中的版本'),
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
  const note=kbCaption('L0 关闭不诊断；L1 只报告问题；L2（默认）生成候选并回放验证，试用、发布、回滚都由人确认；L3 回放通过后自动小范围试用，试用不差于线上则自动全量，发布后变差自动回滚。点资产名查看版本时间线与回滚。');
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

// ---------- 流程步骤：提案在「发现问题 → … → 发布后观察」里走到哪一步 ----------
const EVO_STAGES=[['diagnose','发现问题'],['draft','改写正文'],['replay','回放验证'],['canary','小范围试用'],['release','发布'],['watch','发布后观察']];
const EVO_STEP_ICON={done:'<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3.5 8.5l3 3 6-7" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>',fail:'!',stop:'×',skip:'–'};
const EVO_STEP_STATE={done:'已完成',active:'进行中',wait:'等你决定',fail:'未通过',stop:'已停止',skip:'跳过',todo:'未开始'};
function evoStages(p){
  const s=p.summary||{},c=p.canary,w=p.watch;
  const st={diagnose:'done',draft:'todo',replay:'todo',canary:'todo',release:'todo',watch:'todo'};
  const note={diagnose:p.findingId?'来自看板问题':'手动发起',draft:'',replay:s.total?`${s.finished||0}/${s.total} 条`:'',canary:'',release:'',watch:''};
  let panel='draft';
  const until=(k,state)=>{for(const [key] of EVO_STAGES){if(key===k){st[key]=state;break}st[key]='done'}};
  const canaryNote=()=>c?`${c.percent}% · ${Math.min(c.canary?.runs||0,c.control?.runs||0)}/${c.minRuns}`:'';
  const watchStep=()=>{
    if(!w)return;
    note.watch=`${w.current?.runs||0}/${w.minRuns} 次`;
    st.watch={watching:'active',degraded:'fail',healthy:'done',ended:'done',rolled_back:'fail'}[w.status]||'active';
    if(w.status==='degraded')note.watch='变差';
    if(w.status==='rolled_back'||p.status==='rolled_back')note.watch='已回滚';
    panel='watch';
  };
  switch(p.status){
    case 'drafting':until('draft','active');note.draft='改进器生成中';panel='draft';break;
    case 'validating':until('replay','active');panel='replay';break;
    case 'pending':until('canary','wait');note.replay='通过';note.canary='试用或直接发布';panel='replay';break;
    case 'canary':until('canary','active');note.replay='通过';note.canary=c?.status==='passed'?'通过 · 待全量':canaryNote();panel='canary';break;
    case 'released':case 'rolled_back':
      until('release','done');note.replay='通过';
      if(!c)st.canary='skip',note.canary='直接发布';else note.canary='通过';
      note.release=p.releasedVersion?`v${p.releasedVersion}`:'';panel='release';
      watchStep();
      if(p.status==='rolled_back'&&!w)st.watch='fail',note.watch='已回滚',panel='release';
      break;
    case 'failed':
      if(c){until('canary','fail');note.canary='试用变差';panel='canary'}
      else if(s.total){until('replay','fail');note.replay='未通过';panel='replay'}
      else{until('draft','fail');note.draft='生成失败';panel='draft'}
      break;
    case 'rejected':
      if(c){until('canary','stop');note.canary='已终止';panel='canary'}
      else if(s.total&&s.finished>=s.total){until('canary','stop');note.canary='已驳回';panel='replay'}
      else if(s.total){until('replay','stop');note.replay='已取消';panel='replay'}
      else{until('draft','stop');note.draft='已取消';panel='draft'}
      break;
  }
  return {steps:EVO_STAGES.map(([k,t])=>({k,t,state:st[k],note:note[k]})),panel};
}
function evoMiniSteps(p){
  const {steps}=evoStages(p);
  const cur=[...steps].reverse().find(x=>x.state!=='todo')||steps[0];
  return `<div class="evo-mini" title="${escapeHTML(steps.map(x=>`${x.t}：${EVO_STEP_STATE[x.state]}`).join(' → '))}">${steps.map(x=>`<i class="is-${x.state}"></i>`).join('')}<span>${escapeHTML(cur.t)} · ${escapeHTML(EVO_STEP_STATE[cur.state])}</span></div>`;
}
function evoStepperHTML(steps,selected){
  return `<ol class="evo-steps" role="tablist" aria-label="进化流程">${steps.map((x,i)=>{
    const icon=x.state==='active'||x.state==='wait'?'<i class="evo-step-pulse"></i>':EVO_STEP_ICON[x.state]||String(i+1);
    return `<li class="evo-step is-${x.state}${x.state!=='todo'?' is-reached':''}" style="--i:${i}"><button type="button" role="tab" data-evo-stage="${x.k}" aria-selected="${selected===x.k}"${x.state==='todo'?' disabled':''} title="${escapeHTML(`${x.t}：${EVO_STEP_STATE[x.state]}`)}"><span class="evo-step-dot">${icon}</span><span class="evo-step-name">${escapeHTML(x.t)}</span><span class="evo-step-note">${escapeHTML(x.note||EVO_STEP_STATE[x.state])}</span></button></li>`;
  }).join('')}</ol>`;
}
// 当前状态一句话：做到哪、结论是什么、下一步做什么。
function evoNowHTML(p){
  const s=p.summary||{},c=p.canary,w=p.watch;
  let tone='info',text='';
  switch(p.status){
    case 'drafting':text='改进器正在根据问题改写正文，完成后会自动开始回放。';break;
    case 'validating':text=`正在用新正文重跑 ${s.total||0} 条历史运行（已完成 ${s.finished||0}），全部跑完后给出结论。`;break;
    case 'pending':tone='ok';text=`${s.verdict||'回放通过'}。下一步：先小范围试用，或直接发布。`;break;
    case 'canary':tone=c?.status==='passed'?'ok':'warn';text=c?.verdict||'试用中';break;
    case 'released':tone=w?.status==='degraded'?'err':'ok';text=w?.verdict||`已发布为 v${p.releasedVersion||''}`;break;
    case 'rolled_back':tone='';text=`已回滚。${p.decisionNote||''}`;break;
    case 'failed':tone='err';text=p.error||s.verdict||'未通过';break;
    case 'rejected':tone='';text=`已驳回${p.decisionNote?`：${p.decisionNote}`:''}`;break;
  }
  return text?`<div class="evo-now ${tone}">${escapeHTML(text)}</div>`:'';
}

// ---------- 回放：每条一张卡，说清楚跑了什么、结果怎样、怎么看 ----------
const EVO_OUTCOME_HINTS={
  fixed:'原来失败或返工，用新正文后变好了。',
  regressed:'用新正文后变差了，这条会让提案不通过。',
  unchanged:'结果和原运行一样，说明新正文在这条样本上没有副作用。',
  changed:'新正文让受影响的节点改变了判断（比如开始打回、给出阻断结论）。反馈类问题里这正是想要的行为变化，请打开回放运行确认产出符合预期。',
  blocked:'回放没能跑完（超时、停在人工节点或没启动），不计入判定。',
};
function evoScoreTone(score){return score===2?'ok':score===1?'warn':score===0?'err':''}
function evoReplayCard(r,i,pid){
  const [group,groupHint]=EVO_GROUPS[r.group]||[r.group,''];
  const running=!r.outcome;
  const after=running?(r.status==='waiting'?'停在人工节点':'回放中'):r.outcome==='changed'&&!(r.score>0)?'给出阻断结论':evoScoreLabel(r.score);
  const pill=(label,id,value,tone)=>`<${id?'a href="#"':'span'} class="evo-pill ${tone}"${id?` data-kb-run="${escapeHTML(id)}" title="打开运行 ${escapeHTML(id)}"`:''}><small>${label}</small><b>${escapeHTML(value)}</b></${id?'a':'span'}>`;
  const reused=(r.reusedNodes||[]).length;
  const facts=[
    r.error?`<p><b>发生了什么</b>${escapeHTML(r.error)}</p>`:'',
    reused?`<p><b>怎么跑的</b>沿用原运行的 ${reused} 个上游节点（${escapeHTML(r.reusedNodes.join('、'))}），只用新正文重跑之后的节点。</p>`:`<p><b>怎么跑的</b>用新正文把整条流程重新跑一遍。</p>`,
    r.outcome&&EVO_OUTCOME_HINTS[r.outcome]?`<p><b>怎么看</b>${escapeHTML(EVO_OUTCOME_HINTS[r.outcome])}</p>`:'',
  ].join('');
  return `<article class="evo-replay" style="--i:${i}">
    <header><span class="evo-replay-no">${i+1}</span><b>${escapeHTML(group)}</b><span class="evo-replay-hint">${escapeHTML(groupHint)}</span>${r.outcome?evoStatusBadge(EVO_OUTCOMES,r.outcome):kbBadge('进行中','warn')}</header>
    <div class="evo-flow-row">${pill('原运行',r.sourceRunId,evoScoreLabel(r.sourceScore),evoScoreTone(r.sourceScore))}<span class="evo-flow${running?' is-running':''}" aria-hidden="true"><em>换成新正文重跑</em><i></i></span>${pill('回放',r.runId,r.runId?after:'未启动',running?'run':evoScoreTone(r.outcome==='changed'&&!(r.score>0)?0:r.score))}</div>
    <div class="evo-replay-facts">${facts}</div>
    ${r.runId?`<button type="button" class="kb-btn small evo-nodes-toggle" data-evo-replay-nodes="${escapeHTML(r.id)}" data-pid="${escapeHTML(pid)}" aria-expanded="false" aria-controls="evoNodes-${escapeHTML(r.id)}">逐节点对比</button><div class="evo-nodes" id="evoNodes-${escapeHTML(r.id)}" hidden></div>`:''}
  </article>`;
}
const EVO_NODE_STATUS={succeeded:['成功','ok'],failed:['失败','err'],waiting_approval:['等待确认','warn'],waiting:['等待中','warn'],running:['运行中','run'],skipped:['跳过',''],cancelled:['已取消','']};
const EVO_DECISIONS={pass:'通过',rework:'打回',fail:'不通过',human:'转人工'};
function evoNodeSideHTML(x){
  if(!x)return '<span class="evo-st">没跑到</span>';
  const [label,tone]=EVO_NODE_STATUS[x.status]||[x.status,''];
  const extra=[x.rework?`返工 ${x.rework} 次`:'',x.decision?`判定${EVO_DECISIONS[x.decision]||x.decision}`:''].filter(Boolean).join(' · ');
  return `<span class="evo-st ${tone}">${escapeHTML(label)}</span>${extra?`<small>${escapeHTML(extra)}</small>`:''}`;
}
function evoNodesHTML(d){
  const rows=(d.nodes||[]).map((n,i)=>{
    const tags=`${n.shaped?'<span class="evo-tag accent">受新正文影响</span>':''}${n.reused?'<span class="evo-tag">沿用原结果</span>':''}${n.changed?'<span class="evo-tag warn">有变化</span>':''}`;
    const reasons=[];
    if(n.changed&&n.source?.reason)reasons.push(`原：${n.source.reason}`);
    if((n.changed||n.shaped)&&n.replay?.reason)reasons.push(`新：${n.replay.reason}`);
    return `<div class="evo-node${n.shaped?' is-shaped':''}${n.changed?' is-changed':''}${n.reused?' is-reused':''}" style="--i:${i}"><div class="evo-node-name"><b>${escapeHTML(n.name)}</b>${tags}</div><div class="evo-node-side">${evoNodeSideHTML(n.source)}</div><span class="evo-node-arrow" aria-hidden="true">→</span><div class="evo-node-side">${n.reused?'<span class="evo-st">沿用</span>':evoNodeSideHTML(n.replay)}</div>${reasons.length?`<div class="evo-node-reason">${reasons.map(t=>`<p>${escapeHTML(t)}</p>`).join('')}</div>`:''}</div>`;
  }).join('');
  const changed=(d.nodes||[]).filter(n=>n.changed).length;
  return `${d.input?`<p class="evo-nodes-input"><b>这条样本的需求</b>${escapeHTML(d.input)}</p>`:''}<div class="evo-nodes-head"><span>节点</span><span>原运行</span><span></span><span>回放</span></div>${rows||'<div class="kb-line-note">没有节点记录</div>'}<p class="evo-nodes-sum">${changed?`${changed} 个节点结果有变化`:'各节点结果和原运行一致'}</p>`;
}
async function evoToggleReplayNodes(btn){
  const host=document.getElementById(`evoNodes-${btn.dataset.evoReplayNodes}`);if(!host)return;
  const open=btn.getAttribute('aria-expanded')!=='true';
  btn.setAttribute('aria-expanded',String(open));
  btn.textContent=open?'收起节点对比':'逐节点对比';
  host.hidden=!open;
  if(!open||host.dataset.loaded)return;
  host.innerHTML='<div class="kb-line-note">正在读取两次运行的节点…</div>';
  try{
    const d=await api(`/api/evolution/proposals/${encodeURIComponent(btn.dataset.pid)}/replays/${encodeURIComponent(btn.dataset.evoReplayNodes)}/compare`,{silent:true});
    host.innerHTML=evoNodesHTML(d);host.dataset.loaded='1';
  }catch(err){host.innerHTML=`<div class="kb-line-note kb-err">读取失败：${escapeHTML(err.message)}</div>`}
}

// 两组对比表：每组运行数、一次成功、返工后成功、失败、一次成功率。
function evoArmsHTML(rows,minRuns,label){
  const head=['分组','运行','一次成功','返工后成功','失败','一次成功率'];
  const line=([name,a])=>`<div class="evo-arm" role="row"><span role="cell">${name}</span><b role="cell">${a?.runs||0}${minRuns?`<small>/${minRuns}</small>`:''}</b><b role="cell" class="kb-ok">${a?.clean||0}</b><b role="cell" class="kb-warn">${a?.reworked||0}</b><b role="cell" class="kb-err">${a?.failed||0}</b><b role="cell">${a?.runs?evoPct(a.rate):'—'}</b></div>`;
  return `<div class="evo-arms" role="table" aria-label="${escapeHTML(label)}"><div class="evo-arm evo-arm-head" role="row">${head.map(h=>`<span role="columnheader">${h}</span>`).join('')}</div>${rows.map(line).join('')}</div>`;
}
function evoCanaryHTML(c){
  const ended=c.endedAt&&!String(c.endedAt).startsWith('0001');
  const meta=`${c.percent}% 新运行使用候选 · 每组至少 ${c.minRuns} 次${c.auto?' · L3 自动发起':''} · ${relativeTime(c.startedAt)}开始${ended?` · ${relativeTime(c.endedAt)}结束`:''}`;
  return `${kbCaption(`小范围试用 · ${meta}`)}<div class="evo-trial-head">${evoStatusBadge(EVO_CANARY_STATUS,c.status)}<span>候选比线上差的概率 <b class="${c.probWorse>=0.8?'kb-err':''}">${evoPct(c.probWorse)}</b></span></div>${evoArmsHTML([['候选正文',c.canary],['线上正文',c.control]],c.minRuns,'试用对比')}`;
}
function evoWatchHTML(w){
  const meta=`v${w.prevVersion||'?'} → v${w.version} · 至少 ${w.minRuns} 次运行 · ${relativeTime(w.startedAt)}开始`;
  return `${kbCaption(`发布后观察 · ${meta}`)}<div class="evo-trial-head">${evoStatusBadge(EVO_WATCH_STATUS,w.status)}<span>新版本比旧版本差的概率 <b class="${w.probWorse>=0.9?'kb-err':''}">${evoPct(w.probWorse)}</b></span></div>${evoArmsHTML([[`发布前 v${w.prevVersion||'?'}`,w.baseline],[`发布后 v${w.version}`,w.current]],0,'发布前后对比')}`;
}
function evoCanaryFormHTML(pid){
  const opts=(list,def,unit)=>list.map(v=>`<option value="${v}"${v===def?' selected':''}>${v}${unit}</option>`).join('');
  return `<p class="evo-panel-lead">先让一部分新业务运行用新正文，其余保持线上正文；两组都跑够次数后对比，不比线上差再全量发布。试用期间资产正文不变，随时可以终止。</p><div class="kb-field evo-canary-form"><label for="evoCanaryPercent">试用比例 / 每组最少运行数</label><div class="kb-field-pair"><select id="evoCanaryPercent" class="kb-input" aria-label="试用比例">${opts(EVO_CANARY_PERCENTS,50,'% 新运行')}</select><select id="evoCanaryMinRuns" class="kb-input" aria-label="每组最少运行数">${opts(EVO_CANARY_MIN_RUNS,5,' 次')}</select></div></div><button type="button" class="kb-btn primary evo-canary-go" data-evo-canary="${escapeHTML(pid)}">按此设置开始试用</button>`;
}

// 每一步的详情面板；抽屉里一次只显示一个。
function evoStagePanelHTML(p,k){
  const s=p.summary||{};
  switch(k){
    case 'diagnose':
      return `<p class="evo-panel-lead">改进器从近期运行里发现了下面的问题，并据此改写「${escapeHTML(p.assetName||p.assetId)}」的正文。</p>${p.rationale?`${kbCaption('为什么改')}<p class="evo-rationale">${escapeHTML(p.rationale)}</p>`:'<div class="kb-line-note">没有记录改进理由</div>'}<p class="evo-panel-meta">${p.model?`模型 ${escapeHTML(p.model)} · `:''}${relativeTime(p.createdAt)}发起</p>`;
    case 'draft':{
      if(!p.candidateContent)return `<div class="kb-line-note">${p.status==='drafting'?'改进器正在改写正文…':'改进器没有给出候选正文'}</div>`;
      const d=evoDiffStats(p.baseContent,p.candidateContent);
      return `<div class="evo-change-sum"><span class="kb-ok">+${d.add} 行</span><span class="kb-err">−${d.del} 行</span><span>绿色是新加的、红色删除线是去掉的；同一句里改动的字词单独加深标出</span></div>${evoDiffHTML(p.baseContent,p.candidateContent)}`;
    }
    case 'replay':{
      const replays=p.replays||[];
      if(!replays.length)return '<div class="kb-line-note">还没有开始回放</div>';
      const counts=[['修好',s.fixed,'ok'],['退化',s.regressed,'err'],['无变化',s.unchanged,''],['结论改变',s.changed,'info'],['阻塞',s.blocked,'warn']].filter(([,v])=>v);
      return `<p class="evo-panel-lead">挑了 ${s.total||replays.length} 条历史运行，把「${escapeHTML(p.assetName||p.assetId)}」换成新正文后重跑，和原来的结果逐条对比。</p><div class="evo-count-row">${counts.map(([l,v,t])=>kbBadge(`${l} ${v}`,t)).join('')}<span>完成 ${s.finished||0}/${s.total||0}</span></div>${replays.map((r,i)=>evoReplayCard(r,i,p.id)).join('')}`;
    }
    case 'canary':
      if(p.canary)return evoCanaryHTML(p.canary);
      if(p.status==='pending'&&s.passed&&p.candidateContent)return evoCanaryFormHTML(p.id);
      if(p.status==='released'||p.status==='rolled_back')return '<div class="kb-line-note">这次没有试用，回放通过后直接发布了。</div>';
      return '<div class="kb-line-note">没有进行试用。</div>';
    case 'release':
      return `<p class="evo-panel-lead">${p.releasedVersion?`新正文已写入资产，记为不可变版本 <b class="mono">v${p.releasedVersion}</b>，之后的运行都会使用它。`:'还没有发布。'}</p>${p.decidedAt?`<p class="evo-panel-meta">${relativeTime(p.decidedAt)}${p.decidedBy?'由人工':'由系统'}处理</p>`:''}${p.decisionNote?`${kbCaption('处理备注')}<p class="evo-rationale">${escapeHTML(p.decisionNote)}</p>`:''}`;
    case 'watch':
      return p.watch?evoWatchHTML(p.watch):'<div class="kb-line-note">没有发布后观察记录。</div>';
  }
  return '';
}
function evoSelectStage(k){
  const p=evo.drawerProposal;if(!p)return;
  evo.stage=k;
  $$('#kbDrawer .evo-steps [data-evo-stage]').forEach(b=>b.setAttribute('aria-selected',String(b.dataset.evoStage===k)));
  const host=$('#evoStagePanel');
  if(host){host.innerHTML=evoStagePanelHTML(p,k);host.classList.remove('is-in');void host.offsetWidth;host.classList.add('is-in')}
}

async function evoOpenProposal(id){
  const seq=evoDrawerOpen('改进提案','',`<div class="kb-empty">正在读取提案…</div>`,'',{openerKey:`proposal:${id}`,wide:true});
  try{
    const p=await api(`/api/evolution/proposals/${encodeURIComponent(id)}`,{silent:true});
    if(!evoDrawerLive(seq))return;
    const s=p.summary||{};
    const {steps,panel}=evoStages(p);
    evo.drawerProposal=p;evo.stage=panel;
    const body=`${evoStepperHTML(steps,panel)}${evoNowHTML(p)}<section id="evoStagePanel" class="evo-stage-panel is-in" role="tabpanel">${evoStagePanelHTML(p,panel)}</section>`;
    const canCanary=p.status==='pending'&&s.passed&&!!p.candidateContent;
    const asset=`<button type="button" class="kb-btn" data-evo-asset="${escapeHTML(p.assetKind)}" data-id="${escapeHTML(p.assetId)}">版本</button>`;
    const pid=escapeHTML(p.id);
    let foot=asset;
    if(p.status==='pending')foot=`<button type="button" class="kb-btn quiet" data-evo-reject="${pid}">驳回</button>${asset}<button type="button" class="kb-btn" data-evo-canary="${pid}"${canCanary?'':' disabled'}>开始试用</button><button type="button" class="kb-btn primary" data-evo-approve="${pid}"${s.passed?'':' disabled'}>直接发布</button>`;
    else if(p.status==='canary')foot=`<button type="button" class="kb-btn quiet" data-evo-reject="${pid}" data-trial="1">终止试用</button>${asset}<button type="button" class="kb-btn primary" data-evo-approve="${pid}" data-trial="${p.canary?.status==='passed'?'passed':'running'}">全量发布</button>`;
    else if(p.status==='validating'||p.status==='drafting')foot=`<button type="button" class="kb-btn quiet" data-evo-reject="${pid}">取消提案</button>${asset}`;
    else if(p.status==='released'&&(p.watch?.status==='watching'||p.watch?.status==='degraded')&&p.watch.prevVersion)foot=`${asset}<button type="button" class="kb-btn${p.watch.status==='degraded'?' primary':''}" data-evo-rollback="${p.watch.prevVersion}" data-kind="${escapeHTML(p.assetKind)}" data-id="${escapeHTML(p.assetId)}">回滚到 v${p.watch.prevVersion}</button>`;
    else if(p.status==='failed'&&p.candidateContent&&typeof isPlatformAdmin==='function'&&isPlatformAdmin())foot=`${asset}<button type="button" class="kb-btn" data-evo-approve="${pid}" data-force="1" title="仅平台管理员：跳过回放判定直接发布">强制发布</button>`;
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

async function evoApprove(id,force,btn,trial=''){
  let title=force?'强制发布':'确认发布';
  let message=force?'回放没有通过判定。强制发布会直接把候选正文写入资产并记一个新版本，确定继续？':'发布后候选正文会写入资产，并记为新的不可变版本，之后的运行都会使用它；发布后会持续对比新旧版本，可以随时回滚。';
  if(trial){
    title='全量发布';
    message=trial==='passed'?'试用通过：候选在真实运行上不差于线上正文。全量发布会把候选写入资产并记为新版本，之后的运行都会使用它，并进入发布后观察。':'试用还没有得出结论（运行数不足）。仍然全量发布会把候选写入资产并结束试用，确定继续？';
  }
  const note=await afPrompt({title,message:`${message}\n\n备注（可选）：`,defaultValue:''});
  if(note===null||note===undefined)return;
  if(btn)btn.disabled=true;
  try{
    const p=await api(`/api/evolution/proposals/${encodeURIComponent(id)}/approve`,{method:'POST',body:JSON.stringify({note,force:!!force})});
    toast(`已发布 v${p.releasedVersion||''}，进入发布后观察`,'ok');
    await evoAfterChange(p.id);
  }catch(err){toast(err.message,'err');if(btn)btn.disabled=false}
}

async function evoStartCanary(id,btn){
  if(!$('#evoCanaryPercent')){evoSelectStage('canary');$('#evoCanaryPercent')?.focus();return}
  const percent=Number($('#evoCanaryPercent')?.value)||50,minRuns=Number($('#evoCanaryMinRuns')?.value)||5;
  const note=await afPrompt({title:'开始试用',message:`${percent}% 的新业务运行会使用候选正文，其余保持线上正文；两组各满 ${minRuns} 次后对比。资产正文在全量发布前不会改动。\n\n备注（可选）：`,defaultValue:''});
  if(note===null||note===undefined)return;
  if(btn)btn.disabled=true;
  try{
    const p=await api(`/api/evolution/proposals/${encodeURIComponent(id)}/approve`,{method:'POST',body:JSON.stringify({mode:'canary',percent,minRuns,note})});
    toast('已开始试用','ok');
    await evoAfterChange(p.id);
  }catch(err){toast(err.message,'err');if(btn)btn.disabled=false}
}

async function evoReject(id,btn,trial=false){
  const note=await afPrompt(trial?{title:'终止试用',message:'终止后新运行不再使用候选正文（已开始的运行不受影响），提案记为驳回，问题重新回到看板。原因（可选）：',defaultValue:''}:{title:'驳回提案',message:'驳回后正在进行的回放会被取消，问题重新回到看板。原因（可选）：',defaultValue:''});
  if(note===null||note===undefined)return;
  if(btn)btn.disabled=true;
  try{await api(`/api/evolution/proposals/${encodeURIComponent(id)}/reject`,{method:'POST',body:JSON.stringify({note})});toast(trial?'已终止试用':'已驳回','ok');await evoAfterChange(id)}
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
  if((r.executionContext?.evolutionCanaryIds||[]).length)host.querySelector('.run-header-meta')?.insertAdjacentHTML('afterbegin','<span class="run-meta-chip evo-replay-chip" title="本次运行参与进化试用：受试资产使用候选正文，结果计入试用对比">进化试用</span>');
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
  const t=e.target.closest('[data-evo-tab],[data-evo-finding],[data-evo-proposal],[data-evo-asset],[data-evo-level],[data-evo-dismiss],[data-evo-propose],[data-evo-propose-asset],[data-evo-approve],[data-evo-canary],[data-evo-reject],[data-evo-rollback],[data-evo-version],[data-evo-unpin],[data-evo-stage],[data-evo-replay-nodes]');
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
  if(d.evoApprove)return evoApprove(d.evoApprove,d.force==='1',t,d.trial||'');
  if(d.evoCanary)return evoStartCanary(d.evoCanary,t);
  if(d.evoReject)return evoReject(d.evoReject,t,d.trial==='1');
  if(d.evoRollback)return evoRollback(d.kind,d.id,Number(d.evoRollback),t);
  if(d.evoVersion)return evoOpenAsset(d.kind,d.id,Number(d.evoVersion));
  if(d.evoUnpin)return evoUnpin(d.evoUnpin,t);
  if(d.evoStage){if(d.evoStage!==evo.stage)evoSelectStage(d.evoStage);return}
  if(d.evoReplayNodes)return evoToggleReplayNodes(t);
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
