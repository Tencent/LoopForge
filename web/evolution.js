// RSI工作台：具体报错列表与记录弹窗；提案、资产、样本沿用 kb-* 面板与抽屉。
// 依赖 app.js 与 knowledge.js 的公共状态、API 和组件。
const evo={tab:'issues',projectId:null,overview:null,findings:null,samples:null,error:'',seq:0,poll:0,busy:new Set(),drawerProposal:null,stage:'',filter:{kind:'',q:'',status:'open',sort:'value',days:''},changes:{q:'',status:'all',page:0},list:evoInitialTaskList(),groups:{},modal:null,page:0,selected:new Set(),diagnosing:false,composing:false,lastCheck:null,checkError:''};

function evoInitialTaskList(){return {section:'tasks',status:'all',sop:'',q:'',sort:'impact',page:0}}

const EVO_LEVELS=[[0,'L0 关闭','不诊断、不生成提案'],[1,'L1 只报告','只诊断出问题，不生成修改稿'],[2,'L2 提案回放','生成修改稿并回放验证；试用、发布、回滚都由人确认'],[3,'L3 自动','回放通过后自动试用，试用通过自动发布，发布后变差自动回滚']];
const EVO_PROPOSAL_STATUS={drafting:['生成中','warn'],validating:['回放中','warn'],pending:['待确认','ok'],canary:['试用中','warn'],released:['已发布','ok'],rejected:['已驳回',''],failed:['未通过','err'],rolled_back:['已回滚','']};
const EVO_CANARY_STATUS={running:['试用中','warn'],passed:['试用通过','ok'],failed:['试用未通过','err'],aborted:['已终止',''],promoted:['已全量','ok']};
const EVO_WATCH_STATUS={watching:['观察中','warn'],healthy:['表现正常','ok'],degraded:['退化','err'],rolled_back:['已回滚',''],ended:['观察结束','']};
const EVO_CANARY_PERCENTS=[10,25,50];
const EVO_CANARY_MIN_RUNS=[5,10,20];
const EVO_GROUPS={diagnosis:['诊断样本','改进器看过的失败运行'],holdout:['留出样本','改进器没看过的失败运行'],regression:['回归样本','近期一次成功的运行'],pinned:['钉选样本','人工钉住的运行']};
const EVO_OUTCOMES={fixed:['已修复','ok'],regressed:['退化','err'],unchanged:['无变化',''],changed:['结论改变','info'],blocked:['阻塞','warn']};
const EVO_SIGNALS={node_failed:'节点失败',rework:'打回返工',eval_low:'评测低分',sop_detection:'SOP 自动检测',user_feedback:'用户反馈'};
const EVO_VERDICTS={fix:['需修复','err'],watch:['观察中','warn'],noise:['可能偶发','']};
const EVO_SNOOZE_DAYS=7;
const EVO_SOURCES={baseline:'基线',manual:'手动修改',chatbot:'Chatbot 修改',evolution:'进化发布',rollback:'回滚'};
const EVO_KINDS={agent:'角色',rule:'规则'};
const EVO_SCORES=['失败','返工后成功','一次成功'];
const EVO_POLL_MS=15000;

function evoProjectId(){return state.projectFilter||''}
function evoActive(){return !!$('#evolutionView')?.classList.contains('active')}
function evoKindLabel(kind){return EVO_KINDS[kind]||kind||'-'}
function evoStatusBadge(map,key){const [label,tone]=map[key]||[key||'-',''];return kbBadge(escapeHTML(label),tone)}
function evoLevelOf(kind,id){
  const h=(evo.overview?.health||[]).find(x=>x.assetKind===kind&&x.assetId===id);
  if(h)return Math.min(h.level,h.readOnlyReason?1:evo.overview?.autoEnabled?3:2);
  const p=(evo.overview?.policies||[]).find(x=>x.assetKind===kind&&x.assetId===id);
  return Math.min(p?p.level:2,evo.overview?.autoEnabled?3:2);
}
function evoScoreLabel(score){return score>=0&&score<EVO_SCORES.length?EVO_SCORES[score]:'进行中'}
function evoResultText(text){return String(text||'').replaceAll('修好','已修复')}
function evoRunLink(id,label){return id?`<a href="#" class="evo-run-link mono" data-kb-run="${escapeHTML(id)}" title="${escapeHTML(id)}">${escapeHTML(label||id.slice(-12))}</a>`:'—'}
function evoAssetKey(kind,id){return `${kind}:${id}`}
function evoTime(t){return t&&!String(t).startsWith('0001')?t:''}
function evoVerdict(f){return f.triage?.verdict||'watch'}
function evoSnoozed(f){const t=evoTime(f.snoozedUntil);return !!t&&new Date(t).getTime()>Date.now()}
function evoNum(n,d=1){const v=Number(n)||0;return Number.isInteger(v)?String(v):v.toFixed(d)}
// 等你决策：回放通过待确认、试用通过待全量、发布后退化待回滚。
function evoProposalBuckets(){
  const o=evo.overview||{},trial=o.trial||[];
  const decide=[...(o.pending||[]),...trial.filter(p=>(p.status==='canary'&&p.canary?.status==='passed')||p.watch?.status==='degraded')];
  const ids=new Set(decide.map(p=>p.id));
  return {decide,running:[...(o.validating||[]),...trial.filter(p=>!ids.has(p.id))],recent:o.recent||[]};
}
function evoGroupOpen(key,fallback){return Object.prototype.hasOwnProperty.call(evo.groups,key)?evo.groups[key]:fallback}

// 区间条：后验 90% 区间、估计值与项目平均画在同一条刻度上。
function evoRateBar(t,big=false){
  const scale=Math.min(1,Math.max(0.2,(t.rateHigh||0)*1.25,(t.baselineRate||0)*2));
  const at=v=>Math.max(0,Math.min(100,(Number(v)||0)/scale*100));
  const lo=at(t.rateLow),hi=at(t.rateHigh);
  const label=`估计问题率 ${evoPct(t.rate)}，90% 区间 ${evoPct(t.rateLow)}–${evoPct(t.rateHigh)}，项目平均 ${evoPct(t.baselineRate)}`;
  return `<div class="evo-ci v-${escapeHTML(t.verdict||'watch')}${big?' big':''}" role="img" aria-label="${escapeHTML(label)}" title="${escapeHTML(label)}"><span class="evo-ci-range" style="left:${lo.toFixed(1)}%;width:${Math.max(1,hi-lo).toFixed(1)}%"></span><span class="evo-ci-base" style="left:${at(t.baselineRate).toFixed(1)}%"></span><span class="evo-ci-dot" style="left:${at(t.rate).toFixed(1)}%"></span>${big?`<span class="evo-ci-tick start" style="left:0">0</span><span class="evo-ci-tick end" style="left:100%">${evoPct(scale)}</span>`:''}</div>`;
}
// 趋势：每天的问题运行数，最右是今天。
function evoSpark(trend,big=false){
  const t=(trend||[]).map(Number);if(!t.length)return '';
  const max=Math.max(1,...t),w=big?10:4,g=big?3:2,h=big?40:20,W=t.length*(w+g)-g;
  const bars=t.map((v,i)=>{const bh=v>0?Math.max(2,v/max*h):1;return `<rect x="${i*(w+g)}" y="${(h-bh).toFixed(1)}" width="${w}" height="${bh.toFixed(1)}" rx="1"${v>0?' class="on"':''}/>`}).join('');
  return `<svg class="evo-spark${big?' big':''}" viewBox="0 0 ${W} ${h}" width="${W}" height="${h}" role="img" aria-label="${escapeHTML(`近 ${t.length} 天每天的问题运行数：${t.map(v=>evoNum(v)).join('、')}`)}">${bars}</svg>`;
}

// ---------- 行级 diff：LCS，超大正文退化为集合差 ----------
function evoLineDiff(oldText,newText){
  const a=oldText?String(oldText).split('\n'):[],b=newText?String(newText).split('\n'):[];
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
function evoDiffHTML(oldText,newText,fullContext=false,compact=false,changesOnly=false,lineLabels=null){
  const lines=evoLineDiff(oldText,newText);
  if(!lines.some(([k])=>k!=='ctx')){
    const message=String(oldText||'')===String(newText||'')?'与当前正文没有差异':'仅行顺序或重复次数有变化，可切换并排对比查看完整正文';
    return `<div class="kb-line-note">${message}</div>`;
  }
  const approximate=lineLabels&&String(oldText||'').split('\n').length*String(newText||'').split('\n').length>2500000;
  const numbered=lineLabels&&!approximate;
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
  const out=[];let skipped=0,hiddenChanges=0,blockKind='',blockCount=0,oldLine=0,newLine=0;
  const flush=()=>{if(skipped){if(!changesOnly)out.push(`<div class="evo-diff-skip">… ${skipped} 行未改动</div>`);skipped=0}};
  const flushChanges=()=>{if(hiddenChanges){out.push(`<div class="evo-diff-skip">… ${hiddenChanges} 行改动</div>`);hiddenChanges=0}};
  lines.forEach(([k],i)=>{
    const before=k==='add'?'':++oldLine,after=k==='del'?'':++newLine;
    if(k!==blockKind){flushChanges();blockKind=k;blockCount=0}
    if(compact&&k!=='ctx'&&++blockCount>3){hiddenChanges++;return}
    if(!fullContext&&k==='ctx'&&(changesOnly||!keep.has(i))){skipped++;return}
    flush();
    const numbers=numbered?`<span class="evo-diff-number">${before}</span><span class="evo-diff-number">${after}</span>`:'';
    const sign=k==='add'?'+':k==='del'?'−':' ';
    out.push(`<div class="evo-diff-line ${k}${inline.has(i)?' w':''}">${numbers}<span class="evo-diff-sign" aria-hidden="true">${sign}</span><code>${html[i]||' '}</code></div>`);
  });
  flushChanges();flush();
  const header=numbered?`<div class="evo-diff-line evo-diff-heading"><span class="evo-diff-number">${escapeHTML(lineLabels[0])}</span><span class="evo-diff-number">${escapeHTML(lineLabels[1])}</span><span class="evo-diff-heading-label">正文</span></div>`:'';
  const note=lineLabels&&approximate?'<p class="kb-line-note">正文较长，仅显示两版独有的行，可切换并排对比查看全文。</p>':'';
  return `${note}<div class="evo-diff${numbered?' evo-diff-numbered':''}" role="group" aria-label="正文差异">${header}${out.join('')}</div>`;
}

// ---------- 入口与数据 ----------
async function renderEvolutionView(){
  const root=$('#evolutionBody');
  if(!root)return;
  if(evo.projectId!==null&&evo.projectId!==evoProjectId()){evoDispose();evo.overview=null;evo.findings=null;evo.samples=null;evo.list=evoInitialTaskList();evo.lastCheck=null;evo.checkError='';evo.tab='issues';if(evoActive())history.replaceState(null,'','#/evolution')}
  evo.projectId=evoProjectId();
  if(!evo.projectId){evo.overview=null;evoRenderOverviewHeader();root.innerHTML='<div class="kb-empty">请先在顶部选择项目</div>';return}
  if(!evo.overview&&!evo.modal)root.innerHTML='<div class="kb-empty">正在读取进化概览…</div>';
  await evoRefresh();
  if(evoActive()){evoRender();evoApplyRoute()}
}

async function evoRefresh(){
  const pid=evo.projectId,seq=++evo.seq,q=encodeURIComponent(pid);
  try{
    const [overview,samples,findings]=await Promise.all([
      api(`/api/evolution/overview?projectId=${q}`,{silent:true}),
      api(`/api/evolution/samples?projectId=${q}`,{silent:true}).catch(()=>[]),
      api(`/api/evolution/findings?projectId=${q}`,{silent:true}),
    ]);
    if(seq!==evo.seq||pid!==evoProjectId())return;
    evo.overview=overview;evo.findings=findings||[];evo.samples=samples||[];evo.error='';
  }catch(err){if(seq===evo.seq){evo.error=err.message;evo.overview=null;evo.findings=null}}
}

function evoPollInteractionPaused(){
  if(evo.composing||$('#evoSopPrefDialog')?.open)return true;
  const active=document.activeElement;
  return !!active?.matches?.('#evolutionView input[type="search"], #evolutionView [data-evo-sop-search], #evolutionView [data-evo-search], #evolutionView [data-evo-task-search], #evolutionView [data-evo-change-search]');
}
function evoSchedulePoll(){
  clearTimeout(evo.poll);
  const detect=(evo.overview?.sopDetection?.enabled||(evo.overview?.sops||[]).some(s=>s.enabled))&&!evo.modal&&!['assets','samples'].includes(evo.tab);
  if(!evoActive()||!((evo.overview?.validating||[]).length||(evo.overview?.trial||[]).length||detect))return;
  evo.poll=setTimeout(async()=>{
    if(!evoActive()||document.hidden||evoPollInteractionPaused())return evoSchedulePoll();
    await evoRefresh();
    if(!evoActive()||evoPollInteractionPaused())return evoSchedulePoll();
    if(typeof evoSOPState!=='undefined'&&evoSOPState.view){
      await evoSOPLoadDetail(evoSOPState.view);
      evoSchedulePoll();
      return;
    }
    if(typeof evoSOPIsEnabledView==='function'&&evoSOPIsEnabledView()&&$('#evoSopListHost')&&!evo.modal&&!['assets','samples'].includes(evo.tab)){
      evoSOPUpdateList();
      evoSchedulePoll();
      return;
    }
    evoRender();
  },EVO_POLL_MS);
}

function evoIcon(name){
  const paths={issue:'<rect x="5" y="3" width="14" height="18" rx="2"/><path d="M9 8h6M9 12h6M9 16h4"/>',proposal:'<path d="m12 3 10 18H2zM12 9v5M12 17v1"/>',check:'<circle cx="12" cy="12" r="9"/><path d="m7 12 3 3 7-7"/>',spark:'<path d="m12 3 2.4 6.6L21 12l-6.6 2.4L12 21l-2.4-6.6L3 12l6.6-2.4z"/>',layers:'<path d="m12 3 9 5-9 5-9-5zM3 12l9 5 9-5M3 16l9 5 9-5"/>',chart:'<path d="M4 20v-6h3v6zM10 20V9h3v11zM16 20V4h3v16z"/>',user:'<circle cx="12" cy="7" r="4"/><path d="M4 21a8 8 0 0 1 16 0z"/>',play:'<path d="m7 4 14 8-14 8z"/>',calendar:'<rect x="3" y="5" width="18" height="16" rx="2"/><path d="M7 3v4M17 3v4M3 11h18"/>',chevron:'<path d="m9 5 7 7-7 7"/>'};
  return `<svg class="evo-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${paths[name]||paths.issue}</svg>`;
}
function evoOverviewTrend(rows,now=new Date()){
  const today=new Date(Date.UTC(now.getUTCFullYear(),now.getUTCMonth(),now.getUTCDate())),runs=Array.from({length:7},()=>new Set());
  let recorded=false;
  for(const {c} of rows)for(const o of c.occurrences||[]){
    const t=Date.parse(evoTime(o.createdAt));if(!Number.isFinite(t)||!o.runId)continue;
    recorded=true;
    const at=new Date(t),day=Date.UTC(at.getUTCFullYear(),at.getUTCMonth(),at.getUTCDate()),ago=Math.round((today-day)/86400000);
    if(ago>=0&&ago<7)runs[6-ago].add(o.runId);
  }
  const points=runs.map((r,i)=>({label:new Date(+today-(6-i)*86400000).toISOString().slice(5,10),value:r.size}));
  return {recorded,points};
}
function evoTrendHTML(rows){
  const {recorded,points}=evoOverviewTrend(rows),max=Math.max(1,...points.map(p=>p.value));
  const coords=points.map((p,i)=>[12+i*42,54-p.value/max*40]);
  const line=coords.map(([x,y],i)=>`${i?'L':'M'}${x},${y}`).join(' ');
  return `<section class="evo-overview-trend" aria-label="近 7 天问题趋势"><h3>近 7 天问题趋势 <span title="按 UTC 日期，对已载入诊断的运行去重；不是诊断执行次数">ⓘ</span></h3>${recorded?`<svg viewBox="0 0 276 66" role="img" aria-label="${escapeHTML(points.map(p=>`${p.label}：${p.value} 次运行`).join('，'))}"><defs><linearGradient id="evoTrendFill" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="currentColor" stop-opacity=".18"/><stop offset="100%" stop-color="currentColor" stop-opacity="0"/></linearGradient></defs><path d="${line} L264,62 L12,62 Z" fill="url(#evoTrendFill)"/><path d="${line}" fill="none" stroke="currentColor" stroke-width="2"/>${coords.map(([x,y],i)=>`<circle cx="${x}" cy="${y}" r="2.4"><title>${points[i].label}：${points[i].value} 次运行</title></circle>`).join('')}</svg>`:'<div class="evo-trend-empty">暂无趋势数据 · 重新诊断后生成</div>'}<div class="evo-trend-dates">${points.map(p=>`<span>${p.label}</span>`).join('')}</div></section>`;
}
function evoAllProposals(){
  const {decide,running,recent}=evoProposalBuckets();
  return [...new Map([...recent,...running,...decide].filter(p=>p.id).map(p=>[p.id,p])).values()].sort((a,b)=>(Date.parse(b.updatedAt||b.createdAt)||0)-(Date.parse(a.updatedAt||a.createdAt)||0));
}
function evoFindingProposal(f){return evoAllProposals().find(p=>f.proposalId?p.id===f.proposalId:p.findingId===f.id)}
function evoChangeState(p){
  if(!p)return {label:'修改状态待读取',tone:'',note:'打开详情查看关联修改'};
  if(p.status==='rolled_back'||p.watch?.status==='rolled_back')return {label:'已回滚',tone:'',note:'此修改已撤回，不代表当前正文'};
  if(p.status==='released')return {label:p.releasedVersion?`已发布 v${p.releasedVersion}`:'已发布',tone:p.watch?.status==='degraded'?'err':'ok',note:p.watch?.status==='degraded'?'发布后表现变差，建议检查':p.watch?.status==='watching'?'正在观察后续运行':'当前是否仍生效请看版本'};
  if(p.status==='canary')return {label:p.canary?.status==='passed'?'试用通过 · 待全量':'小范围试用中',tone:'warn',note:`${p.canary?.percent||'?'}% 新运行使用修改稿，尚未全量`};
  if(p.status==='pending')return {label:'待你确认',tone:'warn',note:'修改稿未发布；先看修改与验证结果'};
  if(p.status==='validating')return {label:'验证中',tone:'info',note:`历史运行 ${p.summary?.finished||0}/${p.summary?.total||0} · 尚未发布`};
  if(p.status==='drafting')return {label:'正在生成修改',tone:'info',note:'正在改写正文，尚未发布'};
  if(p.status==='failed')return {label:'未通过',tone:'err',note:p.error||p.summary?.verdict||'修改未发布，请检查原因'};
  if(p.status==='rejected')return {label:'未采用',tone:'',note:p.canary?'试用已终止，未全量发布':'修改已取消或驳回，未发布'};
  return {label:'状态待确认',tone:'',note:'打开详情查看处理记录'};
}
function evoChangeBadge(p){const s=evoChangeState(p);return kbBadge(s.label,s.tone)}
function evoRenderOverviewHeader(){
  const inSopDetail=typeof evoSOPState!=='undefined'&&!!evoSOPState.view;
  const show=!evo.modal&&!inSopDetail;
  const head=$('#evoPageHead');
  if(head){
    head.hidden=!show;
    const isSopWorkbench=Array.isArray(evo.overview?.sops);
    const settingsBtn=head.querySelector('[data-evo-tab="assets"]');
    const diagnoseBtn=head.querySelector('#evoDiagnose');
    if(settingsBtn)settingsBtn.hidden=evo.overview==null;
    if(diagnoseBtn)diagnoseBtn.hidden=isSopWorkbench||evo.overview==null;
  }
  const host=$('#evoMetrics');if(!host)return;
  const sopHome=typeof evoSOPIsEnabledView==='function'&&evoSOPIsEnabledView();
  host.hidden=!show||sopHome;
  host.innerHTML=show&&!sopHome&&evo.overview&&!['assets','samples'].includes(evo.tab)?evoHomeSummaryHTML():'';
  window.AgentFlowHarnessIcons?.hydrate(host);
}
function evoRecentChangeHTML(){
  const p=evoAllProposals().filter(p=>p.status==='released').sort((a,b)=>(Date.parse(b.decidedAt)||0)-(Date.parse(a.decidedAt)||0))[0];
  if(!p)return '';
  return `<section class="evo-recent-change">${evoIcon('check')}<div><strong>最近发布</strong> · ${escapeHTML(p.assetName||p.assetId)} ${p.releasedVersion?`<span class="mono">v${p.releasedVersion}</span>`:''}<p>${escapeHTML(p.title||'正文修改')} · ${escapeHTML(evoChangeState(p).note)}</p></div><button type="button" class="kb-btn quiet" data-evo-proposal="${escapeHTML(p.id)}">看改了什么 ${evoIcon('chevron')}</button></section>`;
}

function evoRender(){
  const root=$('#evolutionBody');if(!root)return;
  evoRenderOverviewHeader();
  evoSchedulePoll();
  if(typeof evoSOPState!=='undefined'&&evoSOPState.view){
    evoSOPRenderDetail();
    return;
  }
  if(evo.modal){
    const m=evo.modal;
    $('#evoDetailStatus').innerHTML=evoReadErrorHTML();
    if(m.row)m.row=evoIssueRows().find(r=>evoIssueKey(r)===evoIssueKey(m.row))||m.row;
    if(!m.proposalId&&m.row)m.proposalId=m.row.f.proposalId||evoFindingProposal(m.row.f)?.id||'';
    if(m.proposalId)return evoLoadDetailProposal(m);
    if(m.row){evoDetailLayout(m);if(m.data)evoRenderFindingRuns()}
    return;
  }
  if(evo.error){root.innerHTML=evoReadErrorHTML();return}
  if(!evo.overview)return;
  const focused=document.activeElement?.closest?.('#evolutionBody')?document.activeElement.dataset.evoKey:'';
  if(['assets','samples'].includes(evo.tab)){
    const tabs=[['assets','角色与规则'],['samples','固定验证样本']];
    const showLegacyDetect=!Array.isArray(evo.overview?.sops)||!!evo.overview?.sopDetection?.enabled;
    root.innerHTML=`<button type="button" class="evo-back" data-evo-list-back>‹ RSI工作台</button><h2 class="evo-settings-title">进化设置</h2>${showLegacyDetect?evoSOPDetectionHTML():''}<div class="evo-tabs" role="tablist" aria-label="进化设置">${tabs.map(([k,t])=>`<button type="button" role="tab" id="evoTab-${k}" data-evo-tab="${k}" data-evo-key="tab:${k}" aria-selected="${evo.tab===k}" tabindex="${evo.tab===k?0:-1}" aria-controls="evoPane" class="${evo.tab===k?'active':''}">${t}</button>`).join('')}</div><div id="evoPane" role="tabpanel" aria-labelledby="evoTab-${evo.tab}">${evo.tab==='assets'?evoAssetsHTML():evoSamplesHTML()}</div>`;
    if(showLegacyDetect)evoBindSOPDetection(root.querySelector('#evoSOPDetection'));
  }else{
    root.innerHTML=evoUnifiedListHTML();
  }
  evoHomeSyncChecking();
  window.AgentFlowHarnessIcons?.hydrate(root);
  if(focused)root.querySelector(`[data-evo-key="${CSS.escape(focused)}"]`)?.focus({preventScroll:true});
}
function evoUnifiedRows(){return evoAssetGroups()}
function evoListCategory({r,p,history,issues,queue,conflict}){
  if(conflict)return 'todo';
  if(queue?.length&&!evoOccupiesAsset(p))return 'todo';
  if(issues?.some(issue=>issue.f.status==='open'&&!issue.f.proposalId)&&!evoOccupiesAsset(p))return 'todo';
  if(history)return 'done';
  if(r&&evoIssueState(r.f)==='done'&&!p)return 'done';
  if(p?.status==='released')return p.watch?.status==='degraded'?'todo':p.watch?.status==='watching'?'doing':'done';
  if(p&&['drafting','validating'].includes(p.status))return 'doing';
  if(p?.status==='canary')return p.canary?.status==='passed'?'todo':'doing';
  if(p&&['rejected','rolled_back'].includes(p.status)&&!(r?.f.status==='open'&&!r.f.proposalId))return 'done';
  return 'todo';
}
function evoListState({r,p,queue}){
  if(p){
    const s=evoChangeState(p);
    if(p.status==='canary'&&p.canary?.status!=='passed'){
      const c=p.canary,have=Math.min(c?.canary?.runs||0,c?.control?.runs||0);
      return {...s,label:c?.minRuns?`试用中 ${have}/${c.minRuns}`:'试用中'};
    }
    return s;
  }
  if(queue?.length&&!evoOccupiesAsset(p))return {label:'等待开始下一项',tone:'warn'};
  const f=r?.f;if(!f)return {label:'等待处理',tone:''};
  if(evoIssueState(f)!=='done'&&!evoSnoozed(f)&&evoHasUserFeedback(f))return {label:'待处理反馈',tone:'warn'};
  return {label:evoIssueState(f)==='done'?'已归档':evoSnoozed(f)?'已暂缓':!f.triage?'待重新检查':evoVerdict(f)==='fix'?'待开始修改':'观察中',tone:evoVerdict(f)==='fix'?'warn':''};
}
function evoHasUserFeedback(f){return [...(f?.signals||[]),...(f?.clusters||[])].some(s=>s.kind==='user_feedback')}
function evoListEffect(p){
  const w=p?.watch,c=p?.canary;
  const comparison=w?[w.baseline,w.current,'发布前后一次成功率']:c?[c.control,c.canary,'试用一次成功率']:null;
  if(comparison&&comparison[0]?.runs>0&&comparison[1]?.runs>0){
    const [before,after,label]=comparison,a=before.clean/before.runs,b=after.clean/after.runs;
    if(Number.isFinite(a)&&Number.isFinite(b))return `<span title="${label}">${evoPct(a)} → <b class="${b>a?'evo-good':b<a?'evo-bad':''}">${evoPct(b)}</b><small>${w?'发布前 → 发布后':'当前 → 修改稿'} · 一次成功</small></span>`;
  }
  const s=p?.summary;if(!s?.finished)return '<span class="evo-muted">—</span>';
  return `<span title="该修改的历史验证结果，不代表本类问题已全部修复">${s.fixed?`<b class="evo-good">${s.fixed}</b> 已修复`:''}${s.regressed?`${s.fixed?' · ':''}<b class="evo-bad">${s.regressed}</b> 退化`:''}${!s.fixed&&!s.regressed?(s.changed?`${s.changed} 结论改变`:`${s.unchanged||0} 无变化`):''}</span>`;
}
function evoListProgress(row){
  const {r,p}=row,s=evoListState(row),w=p?.watch,c=p?.canary;
  let label=s.label,note=s.note||'';
  if(row.queue?.length&&!evoOccupiesAsset(p))return {label:'等待开始下一项',note:`${row.queue.length} 项排队 · 开始时按最新版本生成`,tone:'warn'};
  if(p?.status==='released'&&w){
    label=`已发布${p.releasedVersion?` v${p.releasedVersion}`:''} · ${EVO_WATCH_STATUS[w.status]?.[0]||'状态待确认'}`;
    note=`新版本 ${w.current?.runs||0}/${w.minRuns||5} 次${w.status==='watching'&&!(w.current?.runs>0)?' · 等待新建运行':''}`;
  }else if(p?.status==='canary'&&c){
    label=c.status==='passed'?'试用通过 · 待全量':EVO_CANARY_STATUS[c.status]?.[0]||'试用状态待确认';
    note=`试用版 ${c.canary?.runs||0}/${c.minRuns||5} · 当前版 ${c.control?.runs||0}/${c.minRuns||5} 次`;
  }else if(p?.status==='pending'){
    const v=p.summary;
    note=v?.regressed>0?`历史验证有 ${v.regressed} 条退化 · 先检查样本`:v?.passed&&v.finished>0?`历史验证 ${v.finished} 条 · ${v.changed>0?`${v.changed} 条结论改变，待核对`:v.fixed>0?`已修复 ${v.fixed} 条`:'未见退化，不代表已修复'}`:'等待有效验证结果';
  }else if(!p&&r){
    const f=r.f;
    note=evoIssueState(f)==='done'?'历史问题记录，不代表已验证修复':evoSnoozed(f)?'已暂缓处理':evoHasUserFeedback(f)?'人工反馈，核对预期与实际产物':!f.triage?'需重新检查运行':evoVerdict(f)==='fix'?'查看证据后生成修改':evoVerdict(f)==='noise'?'可能偶发，暂不建议修改':'证据不足，需更多新运行';
  }
  return {label,note,tone:s.tone};
}
function evoListProblem(row){
  const {r,p,title}=row;
  if(!r)return {title:p.title||'历史修改',kind:'修改记录'};
  const raw=r.c.label||title,verification=String(raw).match(/^verification\s+(.+?)\s+command(?:\s|$)/i);
  const kind=r.c.kind||r.f.signals?.[0]?.kind;
  return {title:evoProblemText(raw),kind:verification?'验收检查未通过':EVO_SIGNALS[kind]||(r.f.clusters?.length?'问题线索':'反馈 / 问题线索')};
}
function evoUnifiedListHTML(){return evoHomeHTML()}

// ---------- 问题队列：按判定分组，组内按修复价值排序 ----------
function evoGroupHTML(key,title,hint,count,open,body,opt={}){
  return `<section class="kb-panel evo-group g-${key}" aria-label="${escapeHTML(title)}"><button type="button" class="evo-group-head" data-evo-group="${key}" data-evo-key="group:${key}" aria-expanded="${open}"><span class="evo-group-caret" aria-hidden="true"></span><h3>${escapeHTML(title)}</h3><span class="kb-count">${count}</span><span class="evo-group-hint">${escapeHTML(hint)}</span>${opt.side||''}</button>${open?`<div class="evo-group-body">${body}</div>`:''}</section>`;
}
function evoProblemText(text){
  return String(text||'').split('\n').map(line=>{
    const match=line.match(/^(\s*(?:[·•-]\s*)?)verification\s+(.+?)\s+command(?:\s|$)/i);
    return match?`${match[1]}${match[2]}未通过`:line;
  }).join('\n');
}
function evoIssueRows(){
  return (evo.findings||evo.overview?.findings||[]).flatMap(f=>{
    const clusters=f.clusters?.length?f.clusters:[{label:f.signals?.[0]?.detail||f.summary||f.title,count:f.signals?.length||0,runs:f.sampleRunIds?.length||0}];
    return clusters.map((c,i)=>({f,c,key:c.id||(f.clusters?.length?`legacy-${i}`:''),title:evoProblemText(c.label||f.title)}));
  });
}
function evoIssueState(f){
  if(f.status==='resolved'||f.status==='dismissed')return 'done';
  if(evoSnoozed(f))return 'snoozed';
  return evoVerdict(f)==='noise'?'noise':'open';
}
function evoFindingWorkflows(finding){
  const ids=new Set(finding?.workflowIds||[]);
  return (state.data?.workflows||[]).filter(w=>w.name&&ids.has(w.id));
}
function evoIssueKey(r){return `finding:${r.f.id}:${r.key}`}
function evoIssueDate(t){return evoTime(t)?relativeTime(t):'—'}
function evoIssueChange(f){
  const p=evoFindingProposal(f);
  if(p||f.proposalId)return {title:p?.title||'已有相关修改，查看正文对比',...evoChangeState(p)};
  if(evoIssueState(f)==='done')return {title:f.status==='dismissed'?'已忽略这组问题':'问题已归档，不代表经过验证修复',label:'无关联修改记录',tone:'',note:f.closeNote||'可查看历史运行'};
  if(!f.triage)return {title:'先重新检查运行，补全诊断依据',label:'尚未生成修改',tone:'',note:'旧诊断数据，不推断修改内容'};
  if(evoSnoozed(f))return {title:'已暂缓处理，尚未生成修改建议',label:'已暂缓',tone:'',note:`暂缓至 ${new Date(f.snoozedUntil).toLocaleDateString()}`};
  return {title:evoVerdict(f)==='fix'?'建议生成修改，再对比正文与验证结果':'先观察更多运行，再判断是否需要改指令',label:'还没有修改',tone:'',note:evoVerdict(f)==='noise'?'可能偶发，不必立即修改':'问题线索不等于确定根因'};
}
function evoAssetLabel(p){return `<span class="evo-object-name">${evoIcon(p.assetKind==='rule'?'issue':'user')}${escapeHTML(p.assetName||p.assetId||'未知对象')}</span><small>${escapeHTML(evoKindLabel(p.assetKind))} · ${p.assetKind==='rule'?'规则正文':'角色指令'}</small>`}
function evoIssueRow(r){
  const {f,c}=r,verdict=evoVerdict(f),key=evoIssueKey(r),change=evoIssueChange(f),p=evoFindingProposal(f);
  const priority=f.triage?({fix:'建议优先处理',watch:'继续观察',noise:'可能偶发'}[verdict]||'待判断'):'需要重新检查';
  const open=`data-evo-open="${escapeHTML(f.id)}" data-cluster="${escapeHTML(r.key)}"`;
  return `<div class="evo-issue-row${evo.selected.has(key)?' selected':''}"><input type="checkbox" class="evo-row-check" data-evo-select="${escapeHTML(key)}" aria-label="选择问题：${escapeHTML(r.title)}"${evo.selected.has(key)?' checked':''}><button type="button" class="evo-issue v-${escapeHTML(verdict)}" data-evo-finding="${escapeHTML(f.id)}" data-evo-cluster="${escapeHTML(r.key)}" data-evo-key="${escapeHTML(key)}" aria-haspopup="dialog"><b>${escapeHTML(r.title)}</b><span class="evo-issue-desc"><span>${priority}</span><span>出现 ${c.count||0} 次 · 涉及 ${c.runs||0} 次运行</span><span>最近 ${escapeHTML(evoIssueDate(c.lastSeenAt||f.lastSeenAt))}</span></span></button><div class="evo-object">${evoAssetLabel(f)}</div><div class="evo-change-copy"><p>${escapeHTML(change.title)}</p><div>${kbBadge(change.label,change.tone)}<small>${escapeHTML(change.note)}</small></div></div><div class="evo-row-actions"><button type="button" class="evo-row-primary" ${open}>${p||f.proposalId?'看问题与修改':'查看问题'}</button><details class="evo-row-menu"><summary aria-label="更多操作：${escapeHTML(r.title)}">···</summary><div class="evo-row-menu-items"><button type="button" ${open}>问题详情与处理</button><button type="button" data-evo-asset="${escapeHTML(f.assetKind)}" data-id="${escapeHTML(f.assetId)}">查看对象版本</button>${!f.triage?`<button type="button" data-evo-rediagnose${evo.diagnosing?' disabled':''}>重新检查运行</button>`:''}</div></details></div></div>`;
}
function evoIssueTime(r){return Date.parse(evoTime(r.c.lastSeenAt)||evoTime(r.f.lastSeenAt))||0}
function evoFilteredIssues(){
  const {kind,q,status,sort,days}=evo.filter,s=q.trim().toLowerCase(),since=days?Date.now()-Number(days)*86400000:0;
  const list=evoIssueRows().filter(r=>evoIssueState(r.f)===status&&(!kind||r.f.assetKind===kind)&&(!days||evoIssueTime(r)>=since)&&(!s||[r.title,r.f.assetName,r.f.assetId,r.c.nodeId].some(v=>String(v||'').toLowerCase().includes(s))));
  list.sort((a,b)=>{
    const delta=sort==='recent'?evoIssueTime(b)-evoIssueTime(a):sort==='count'?(b.c.count||0)-(a.c.count||0):(b.f.triage?.score||0)-(a.f.triage?.score||0);
    return delta||(b.c.runs||0)-(a.c.runs||0)||evoIssueKey(a).localeCompare(evoIssueKey(b));
  });
  return list;
}
function evoIssueListHTML(){
  const list=evoFilteredIssues(),pages=Math.max(1,Math.ceil(list.length/10));
  evo.page=Math.max(0,Math.min(evo.page,pages-1));
  const valid=new Set(list.map(evoIssueKey));evo.selected=new Set([...evo.selected].filter(k=>valid.has(k)));
  const page=list.slice(evo.page*10,(evo.page+1)*10),checked=page.filter(r=>evo.selected.has(evoIssueKey(r))).length;
  const batch=evo.selected.size?`<div class="evo-batch"><span>已选择 ${evo.selected.size} 类问题</span><button type="button" class="kb-btn" data-evo-batch-snooze>暂缓所属资产 7 天</button><button type="button" class="kb-btn quiet" data-evo-clear-selection>清除选择</button></div>`:'';
  const nearby=[...new Set([0,evo.page-1,evo.page,evo.page+1,pages-1].filter(p=>p>=0&&p<pages))].sort((a,b)=>a-b);
  const pager=nearby.map((p,i)=>`${i&&p-nearby[i-1]>1?'<span>…</span>':''}<button type="button" data-evo-list-page="${p}" aria-label="第 ${p+1} 页"${p===evo.page?' aria-current="page"':''}>${p+1}</button>`).join('');
  return `${batch}<div class="evo-issue-columns"><input type="checkbox" aria-label="选择本页问题" data-evo-select-page${checked===page.length&&page.length?' checked':''}${page.length?'':' disabled'} data-partial="${checked>0&&checked<page.length}"><span>发生了什么问题</span><span>涉及哪个对象</span><span>准备怎么改 / 当前进展</span><span>操作</span></div>${page.map(evoIssueRow).join('')||'<div class="kb-empty">没有匹配的问题。可调整筛选，或点「检查最近运行」更新问题。</div>'}<footer class="evo-list-footer"><span>共 ${list.length} 类问题</span><nav aria-label="问题分页"><button type="button" data-evo-list-page="${evo.page-1}" aria-label="上一页"${evo.page===0?' disabled':''}>‹</button>${pager}<button type="button" data-evo-list-page="${evo.page+1}" aria-label="下一页"${evo.page===pages-1?' disabled':''}>›</button></nav></footer>`;
}
function evoUpdateIssueList(reset=false){
  if(reset){evo.page=0;evo.selected.clear()}
  const host=$('#evoIssueList');if(!host)return;
  host.innerHTML=evoIssueListHTML();
  const checkbox=host.querySelector('[data-evo-select-page]');if(checkbox)checkbox.indeterminate=checkbox.dataset.partial==='true';
}
function evoIssuesHTML(){
  const rows=evoIssueRows(),tabs=[['open','待处理'],['noise','可能是偶发'],['snoozed','已暂缓'],['done','已归档 / 忽略']];
  const statuses=`<div class="evo-status-tabs" role="group" aria-label="问题状态">${tabs.map(([key,label])=>`<button type="button" data-evo-status="${key}" aria-pressed="${evo.filter.status===key}" class="${evo.filter.status===key?'active':''}">${label}<span class="kb-count">${rows.filter(r=>evoIssueState(r.f)===key).length}</span></button>`).join('')}</div>`;
  const options=(items,current)=>items.map(([v,label])=>`<option value="${v}"${current===v?' selected':''}>${label}</option>`).join('');
  const toolbar=`<div class="evo-toolbar"><label class="kb-search evo-search">${typeof KB_SEARCH_ICON==='string'?KB_SEARCH_ICON:''}<input type="search" data-evo-search data-evo-key="search" aria-label="搜索问题" placeholder="搜索报错、角色、规则或关键词…" autocomplete="off" value="${escapeHTML(evo.filter.q)}"></label><label class="evo-select">${evoIcon('layers')}<select class="kb-input" data-evo-kind-select aria-label="资产类型">${options([['','全部资产'],['agent','角色'],['rule','规则']],evo.filter.kind)}</select></label><label class="evo-select">${evoIcon('issue')}<select class="kb-input" data-evo-sort aria-label="问题排序">${options([['value','按资产修复价值'],['recent','按最近出现'],['count','按出现次数']],evo.filter.sort)}</select></label><label class="evo-select">${evoIcon('calendar')}<select class="kb-input" data-evo-days aria-label="最近出现时间">${options([['','全部时间'],['7','近 7 天'],['14','近 14 天'],['30','近 30 天']],evo.filter.days)}</select></label></div>`;
  return `<section class="kb-panel evo-issues" aria-label="问题列表">${statuses}${toolbar}<div id="evoIssueList">${evoIssueListHTML()}</div><div class="evo-list-note">一行是一类报错，点击查看对应运行。判定与修复价值按所属资产计算；最多展示最近 200 条资产诊断。</div></section>`;
}
function evoPct(n){return `${Math.round((n||0)*100)}%`}
function evoProgressBar(pct,label){return `<div class="evo-progress" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${pct}" aria-label="${escapeHTML(label)}"><i style="width:${pct}%"></i></div>`}
// 试用 / 观察卡片的进度：试用取两组中较少的一组，观察取发布后运行数。
function evoTrialProgress(p){
  const c=p.canary,w=p.watch;
  if(p.status==='canary'&&c){
    const need=c.minRuns||1,have=Math.min(c.canary?.runs||0,c.control?.runs||0);
    return {pct:Math.min(100,Math.round(have/need*100)),note:`试用 ${c.percent}% · 修改稿 ${c.canary?.runs||0}/${need} · 线上 ${c.control?.runs||0}/${need}`,badge:evoStatusBadge(EVO_CANARY_STATUS,c.status)};
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
    progress=`${evoProgressBar(pct,'回放进度')}<div class="evo-card-note">${p.status==='drafting'?'改进器正在生成修改稿…':`回放 ${s.finished||0}/${s.total||0}`}</div>`;
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
// ---------- 提案：等你决策置顶，进行中带进度，最近结束默认收起 ----------
function evoChangeListHTML(){
  const q=evo.changes.q.trim().toLowerCase(),decisions=new Set(evoProposalBuckets().decide.map(p=>p.id));
  const list=evoAllProposals().filter(p=>(evo.changes.status==='all'||(evo.changes.status==='decide'?decisions.has(p.id):p.status===evo.changes.status))&&(!q||[p.title,p.assetName,p.assetId,p.rationale].some(s=>String(s||'').toLowerCase().includes(q))));
  const pages=Math.max(1,Math.ceil(list.length/10));evo.changes.page=Math.min(Math.max(0,evo.changes.page),pages-1);
  const rows=list.slice(evo.changes.page*10,(evo.changes.page+1)*10).map(p=>{
    const f=(evo.findings||[]).find(f=>f.id===p.findingId),s=evoChangeState(p);
    const reason=f?.summary||f?.title||p.rationale||(p.findingId?'关联问题未载入，打开详情查看':'从角色或规则直接发起');
    return `<div class="evo-change-row"><div><b>${escapeHTML(reason)}</b><small>${escapeHTML(evoIssueDate(p.decidedAt||p.updatedAt||p.createdAt))}</small></div><div class="evo-object">${evoAssetLabel(p)}${p.releasedVersion?`<small>曾发布版本 v${p.releasedVersion}</small>`:''}</div><div class="evo-change-copy"><p>${escapeHTML(p.title||'查看正文修改')}</p><div>${evoChangeBadge(p)}${p.canary?.auto?kbBadge('自动执行','info'):''}<small>${escapeHTML(s.note)}</small></div></div><button type="button" class="evo-row-primary" data-evo-proposal="${escapeHTML(p.id)}" data-evo-key="proposal:${escapeHTML(p.id)}" aria-haspopup="dialog">查看修改对比</button></div>`;
  }).join('');
  return `<div class="evo-change-columns"><span>为什么修改</span><span>修改了哪个对象</span><span>具体改动 / 是否发布</span><span>操作</span></div>${rows||'<div class="kb-empty">没有匹配的修改记录。可从问题详情生成修改建议。</div>'}<footer class="evo-list-footer"><span>当前载入 ${list.length} 项修改 · 不含全部历史</span><nav aria-label="修改记录分页"><button type="button" data-evo-change-page="${evo.changes.page-1}"${evo.changes.page===0?' disabled':''}>上一页</button><span>${evo.changes.page+1} / ${pages}</span><button type="button" data-evo-change-page="${evo.changes.page+1}"${evo.changes.page+1===pages?' disabled':''}>下一页</button></nav></footer>`;
}
function evoUpdateChanges(reset=false){if(reset)evo.changes.page=0;const host=$('#evoChangeList');if(host)host.innerHTML=evoChangeListHTML()}
function evoProposalsHTML(){
  const filters=[['all','全部修改'],['decide','需要我处理'],['released','已发布'],['rolled_back','已回滚'],['rejected','未采用'],['failed','未通过']];
  return `<section class="evo-changes" aria-label="修改记录"><div class="evo-toolbar"><label class="kb-search evo-search">${typeof KB_SEARCH_ICON==='string'?KB_SEARCH_ICON:''}<input type="search" data-evo-change-search data-evo-key="change-search" aria-label="搜索修改记录" placeholder="搜索问题、角色、规则或改动…" value="${escapeHTML(evo.changes.q)}"></label><select class="kb-input" data-evo-change-status aria-label="修改状态">${filters.map(([v,t])=>`<option value="${v}"${evo.changes.status===v?' selected':''}>${t}</option>`).join('')}</select><span class="evo-toolbar-hint">待确认 ≠ 已发布；试用仅影响部分新运行。</span></div><div id="evoChangeList" class="evo-change-table">${evoChangeListHTML()}</div></section>`;
}

// ---------- 资产与策略 ----------
function evoAssetsHTML(){
  const items=evo.overview.health||[];
  const levelSeg=h=>`<div class="kb-seg evo-level" aria-label="${escapeHTML((h.assetName||h.assetId)+' 自治级别')}">${EVO_LEVELS.map(([v,t,title])=>`<button type="button" data-evo-level="${v}" data-kind="${escapeHTML(h.assetKind)}" data-id="${escapeHTML(h.assetId)}" class="${evoLevelOf(h.assetKind,h.assetId)===v?'active':''}" title="${escapeHTML(h.readOnlyReason&&v>=2?h.readOnlyReason:v===3&&!evo.overview.autoEnabled?'自动进化尚未启用':title)}"${evo.busy.has(evoAssetKey(h.assetKind,h.assetId))||(v>=2&&h.readOnlyReason)||(v===3&&!evo.overview.autoEnabled)?' disabled':''}>${t.split(' ')[0]}</button>`).join('')}</div>`;
  const rows=items.map(h=>{
    const rate=h.runs?`${Math.round((h.successRate||0)*100)}%`:'—';
    const desc=`${evoKindLabel(h.assetKind)} · ${h.runs||0} 次运行 · 失败 ${h.failed||0} · 返工 ${h.reworked||0}${h.latestVersion?` · v${h.latestVersion}`:''}`;
    const key=`asset:${evoAssetKey(h.assetKind,h.assetId)}`;
    return `<div class="kb-row evo-asset-row"><div class="kb-row-main evo-asset-main kb-go" role="button" tabindex="0" data-evo-asset="${escapeHTML(h.assetKind)}" data-id="${escapeHTML(h.assetId)}" data-evo-key="${escapeHTML(key)}">${kbText(escapeHTML(h.assetName||h.assetId),desc+(h.readOnlyReason?` · ${h.readOnlyReason}`:''))}</div>${h.openFindings?kbBadge(`${h.openFindings} 个问题`,'warn'):''}${kbValue(rate,'evo-rate','近期成功率')}${levelSeg(h)}</div>`;
  });
  const note=kbCaption('L0 关闭不诊断；L1 只报告问题；L2（默认）生成修改稿并回放验证，试用、发布、回滚都由人确认；L3 回放通过后自动小范围试用，试用不差于线上则自动全量，发布后变差自动回滚。自动进化需要管理员启用；共享与平台资产仅供诊断。点资产名查看版本时间线与回滚。');
  return kbPanel(kbPanelTitle('角色与规则的进化方式',`近 ${evo.overview.windowDays||14} 天`),note+(rows.join('')||'<div class="kb-empty">近期没有运行用到可进化的角色或规则</div>'),{label:'角色与规则的进化方式'})+`<details class="evo-detail-more"><summary>查看诊断趋势与统计口径</summary>${evoTrendHTML(evoIssueRows())}<p>趋势按 UTC 日期与运行 ID 去重，仅统计已载入诊断中的运行；不是执行检查的次数。</p></details>`;
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

function evoCloseFinding(restore=true){
  const m=evo.modal;evo.modal=null;
  $('#evoDetailPage')?.remove();
  if(restore&&evoActive()){
    evo.tab='issues';history.replaceState(null,'','#/evolution');evoRender();
    evoFocusTask(m);
  }
}
function evoDispose(){clearTimeout(evo.poll);++evo.seq;evoCloseFinding(false);if(typeof evoCloseSOP==='function')evoCloseSOP(false);if(evoDrawerOwned())evoDrawerClose()}
function evoRoute(){
  try{
    const parts=location.hash.replace(/^#\/?/,'').split('/').map(decodeURIComponent);
    if(parts[0]!=='evolution')return null;
    if(parts[1]==='sops'&&parts[2])return {type:'sop',id:parts[2],tab:parts[3]||'trend',scope:parts[4]||'versions'};
    if(parts[1]==='proposals'&&parts[2])return {type:'proposal',id:parts[2]};
    if(parts[1]==='findings'&&parts[2])return {type:'finding',id:parts[2],key:parts[3]||''};
    if(parts[1]==='settings')return {type:'settings',tab:parts[2]==='samples'?'samples':'assets'};
    return {type:'list'};
  }catch{return {type:'list'}}
}
function evoNavigate(hash){
  if(location.hash!==hash)history.pushState({evolutionFrom:location.hash},'',hash);
  evoApplyRoute();
}
function evoBack(){
  const opener=evo.modal;
  evoCloseFinding(false);evo.tab='issues';
  if(history.state?.evolutionFrom==='#/evolution'){history.back()}else history.replaceState(null,'','#/evolution');
  evoRender();
  evoFocusTask(opener);
}
function evoFocusTask(opener){
  const tasks=$('#evoWorkspaceTasks');
  const find=key=>key&&tasks?.querySelector(`[data-evo-key="${CSS.escape(key)}"]`);
  (find(opener?.routeKey)||find(opener?.openerKey)||tasks?.querySelector('button[aria-pressed="true"]')||$('#evoDiagnose'))?.focus();
}
function evoApplyRoute(){
  if(!evoActive())return;
  const r=evoRoute();if(!r||!evo.overview&&r.type!=='list')return;
  if(r.type==='sop'&&typeof evoOpenSOP==='function'){
    if(evoSOPState.view?.workflowId===r.id&&evoSOPState.view?.tab===(r.tab==='change'?'change':'trend'))return;
    return evoOpenSOP(r.id,r.tab,r.scope,true);
  }
  if(typeof evoCloseSOP==='function')evoCloseSOP(false);
  const identity=r.type==='finding'?`finding:${r.id}:${r.key}`:r.type==='proposal'?`proposal:${r.id}`:'';
  if(identity&&evo.modal?.routeKey===identity)return;
  if(r.type==='finding')return evoOpenFinding(r.id,r.key,true);
  if(r.type==='proposal')return evoOpenProposal(r.id,true);
  const opener=evo.modal;
  evoCloseFinding(false);if(evoDrawerOwned())evoDrawerClose();
  evo.tab=r.type==='settings'?r.tab:'issues';
  evoRender();
  if(r.type==='list'&&opener)evoFocusTask(opener);
}
function evoFindingStats(r){
  const {f,c}=r,t=f.triage;
  const stats=`<div><b>${c.count||0}</b><small>本类出现次数</small></div><div><b>${c.runs||0}</b><small>本类受影响运行</small></div>`;
  if(!t)return `<div class="evo-finding-stats">${stats}<p>旧版诊断，请重新诊断以补全记录与统计。</p></div>`;
  return `<div class="evo-finding-stats">${stats}<div><b>${evoPct(t.probWorse)}</b><small>所属资产比平均差的概率</small></div><details class="evo-how"><summary>资产问题率 ${evoPct(t.rate)} · 项目平均 ${evoPct(t.baselineRate)} · 怎么算的</summary><p>这是该资产所有问题的综合判定，不是当前报错的独立概率。${escapeHTML(t.reason||'')}。Beta-Binomial 先验强度为 10 次运行，旧运行每 7 天权重减半，返工按半次计。</p><p>当前版本 ${t.runs} 次运行；90% 区间 ${evoPct(t.rateLow)}–${evoPct(t.rateHigh)}。${t.staleRuns?`另有 ${t.staleRuns} 次运行用的是旧版本，没有计入。`:''}</p></details></div>`;
}
function evoMountDetail(title,openerKey,row=null){
  evoCloseFinding(false);if(evoDrawerOwned())evoDrawerClose();
  const asset=row?.f||evoAllProposals().find(p=>openerKey===`proposal:${p.id}`);
  const m={row,openerKey:asset?`asset:${evoAssetKey(asset.assetKind,asset.assetId)}`:openerKey,routeKey:openerKey,projectId:evo.projectId,status:'',page:0,data:null,evidence:null,evidenceCache:new Map(),compareFilter:'all',viewOpen:[]};
  evo.modal=m;evo.tab='issues';
  $('#evolutionBody').innerHTML=`
    <section id="evoDetailPage" aria-labelledby="evoFindingTitle">
      <button type="button" class="evo-back" data-evo-list-back>‹ 返回改进任务</button>
      <div id="evoDetailStatus">${evoReadErrorHTML()}</div>
      <header class="evo-detail-head">
        <div class="evo-workspace-heading">
          <div class="evo-review-identity"><span id="evoDetailGlyph" class="evo-review-glyph"></span><div>
            <h1 id="evoFindingTitle" tabindex="-1">${escapeHTML(title)}</h1>
            <div id="evoDetailObject" class="evo-detail-object"></div>
          </div></div>
        </div>
        <div id="evoDetailActions" class="evo-detail-actions"></div>
      </header>
      <div id="evoDetailProgress"></div>
      <div id="evoDetailBody" class="evo-detail-grid"><div class="kb-empty">正在读取…</div></div>
      ${evoStoryEvidenceDialogHTML()}
    </section>`;
  $('#evoEvidenceDialog').addEventListener('close',()=>evoCloseStoryEvidence(m));
  evoRenderOverviewHeader();
  $('#evoFindingTitle').focus({preventScroll:true});
  $('#evoDetailPage').scrollIntoView?.({block:'start'});
  return m;
}
function evoDetailTrackHTML(p,finding){
  const steps=p?evoStages(p).steps:EVO_STAGES.map(([k,t],i)=>({k,t,state:i?'todo':'active',note:''}));
  const [problem,draft,replay,trial,release,watch]=steps;
  problem.t='诊断';
  draft.t='生成修改';
  watch.t='观察';
  if(!p){
    problem.note='待开始修改';
    if(finding&&evoSnoozed(finding)){problem.state='stop';problem.note='已暂缓'}
    else if(finding&&evoIssueState(finding)==='done'){problem.state='stop';problem.note='已归档'}
  }else if(p.status==='pending'&&!p.summary?.passed){
    replay.state='wait';replay.note='待确认结果';
    trial.state='todo';trial.note='';
  }else if(p.status==='pending'){
    trial.note='待确认试用';
  }else if(p.status==='canary'&&p.canary?.status==='passed'){
    trial.state='done';trial.note='通过';
    release.state='wait';release.note='待确认发布';
  }else if(p.status==='rolled_back'){
    watch.state='stop';watch.note='已回滚';
  }
  const current=steps.findIndex(step=>['active','wait','fail','stop'].includes(step.state));
  const targets=['evoProblemEvidenceDetails','evoFullChange','evoValidationResults'];
  return `<ol class="evo-detail-track" aria-label="进化进度">${steps.map((step,i)=>{
    const active=i===current;
    const note=step.note||EVO_STEP_STATE[step.state];
    return `<li class="is-${step.state}"${active?' aria-current="step"':''}>
      <button type="button" ${targets[i]?`data-evo-progress-target="${targets[i]}"`:p?`data-evo-progress-stage="${step.k}"`: 'disabled'}>
        <span aria-hidden="true">${EVO_STEP_ICON[step.state]||i+1}</span>
        <b>${escapeHTML(step.t)}</b>
        ${active||step.state==='skip'?`<small>${escapeHTML(note)}</small>`:`<small class="evo-review-sr">${escapeHTML(EVO_STEP_STATE[step.state])}</small>`}
      </button>
    </li>`;
  }).join('')}</ol>`;
}
function evoDetailLayout(m,p=null){
  const r=m.row,asset=p||r?.f;
  m.viewOpen=[...document.querySelectorAll('#evoDetailPage details[open][id]')].map(d=>d.id);
  const problem=evoStoryProblemText(m,p);
  if(asset)$('#evoFindingTitle').textContent=evoStoryBrief(problem,72)||asset.assetName||asset.assetId;
  $('#evoDetailGlyph').innerHTML=evoFindingPriorityHTML(r?.f)||evoReviewIcon(asset?.assetKind==='rule'?'doc':'robot');
  $('#evoDetailObject').innerHTML=evoReviewVersionHTML(asset,p,r?.f);
  $('#evoDetailProgress').innerHTML=p||!m.proposalId?evoDetailTrackHTML(p,r?.f):'';
  $('#evoDetailBody').innerHTML=evoReviewDetailHTML(m,p);
  if(!p)$('#evoProblemBox').textContent=m.proposalId?'正在读取修改…':'';
  if(!p&&r)$('#evoDetailActions').innerHTML=evoFindingActions(r.f);
  m.viewOpen.forEach(id=>{const d=document.getElementById(id);if(d)d.open=true});
  evoSetDetailTab(m.detailTab||(p||m.proposalId?'change':'records'),false);
  if(p)evoRenderComparisonList(m);
  if($('#evoEvidenceDialog').open&&m.evidence)evoSelectEvidence(m.evidence,false);
  window.AgentFlowHarnessIcons?.hydrate($('#evoDetailPage'));
}
function evoSetDetailTab(key,remember=true){
  const m=evo.modal;if(!m||!['validation','change','records'].includes(key))return;
  m.detailTab=key;
  if($('#evoReviewWorkbench')?.classList.contains('evo-story-workbench')){
    return;
  }
  $$('[data-evo-detail-tab]').forEach(tab=>{const active=tab.dataset.evoDetailTab===key;tab.setAttribute('aria-selected',String(active));tab.tabIndex=active?0:-1});
  ['validation','change','records'].forEach(id=>{$(`#evoDetailPanel-${id}`).hidden=id!==key});
  const workbench=$('#evoReviewWorkbench');if(workbench)workbench.dataset.view=key;
  if(remember){
    m.evidenceTouched=true;
    if(key==='records'){
      const record=(m.data?.items||[]).find(r=>r.runId===m.lastRun&&!r.unavailable)||(m.data?.items||[]).find(r=>!r.unavailable);
      if(record)evoSelectEvidence({type:'run',id:record.runId});
      else evoEmptyDetailEvidence(m,'records');
    }else if(key==='validation'){
      const records=m.proposal?.replays||[],replay=records.find(r=>r.id===m.lastReplay)||records.find(r=>r.outcome==='regressed')||records.find(r=>r.outcome==='fixed')||records[0];
      if(replay)evoSelectEvidence({type:'replay',id:replay.id});
      else evoEmptyDetailEvidence(m,'validation');
    }
    $(`#evoDetailTab-${key}`)?.focus({preventScroll:true});
  }
}
function evoEmptyDetailEvidence(m,key){
  m.evidence=null;m.evidenceRequest=null;
  $('#evoEvidenceTitle').textContent=key==='records'?'原问题 · 运行证据':'验证证据 · 新旧对比';
  $('#evoEvidenceCount').textContent='';
  $('#evoEvidenceBody').innerHTML=key==='records'&&m.dataError?`<div class="evo-evidence-empty">读取问题样本失败：${escapeHTML(m.dataError)} <button type="button" class="kb-btn small" data-evo-record-retry>重试</button></div>`:`<div class="evo-evidence-empty">${key==='records'?(m.data?'暂无可访问的问题样本':'正在读取问题样本…'):'尚无验证样本'}</div>`;
}
function evoFindingActions(f){
  const level=evoLevelOf(f.assetKind,f.assetId),blocked=level<2||evoIssueState(f)==='done'||evoSnoozed(f);
  return `<button type="button" class="kb-btn primary" data-evo-propose="${escapeHTML(f.id)}"${blocked?' disabled':''} data-tooltip="修改角色指令或规则正文">${evoReviewIcon('edit')}开始修改</button>`;
}
function evoRecommendation(r,p){
  const f=r?.f,level=f?evoLevelOf(f.assetKind,f.assetId):2;
  let title='先观察更多运行',note='现有证据不足以确定改正文能解决问题。';
  if(f&&!f.triage){title='重新检查最近运行';note='旧诊断缺少当前版本的依据。'}
  else if(f&&evoSnoozed(f)){title='已暂缓处理';note='取消暂缓后再开始修改。'}
  else if(f&&evoIssueState(f)==='done'){title='已归档';note=f.closeNote||'归档不等于已经验证修复。'}
  else if(f&&level<2){title='当前只报告问题';note='如需生成修改，在进化设置中调整该对象的策略。'}
  else if(f&&evoVerdict(f)==='fix'){title='生成修改并验证';note='先改角色指令或规则，不直接改业务代码。'}
  if(p){
    const map={drafting:['正在生成修改','生成完成后自动进行历史验证。'],validating:['正在验证修改稿','等待回放结果，当前正文还没有改变。'],pending:p.summary?.passed?['先小范围试用','历史验证已通过；建议用真实新运行继续检查。']:['暂不发布','验证尚未通过或证据不足，请先检查结果。'],canary:p.canary?.status==='passed'?['可确认全量发布','试用未达到退化阈值，不代表已经证明质量提升。']:['继续等待试用结果','当前只分流部分运行，未全量发布。'],released:p.watch?.status==='degraded'?['建议回滚','发布后出现退化，请先检查受影响运行。']:['继续观察新版本','曾发布不等于当前仍在生效，以版本核对为准。'],failed:['检查失败原因后重新生成','不要把失败修改稿直接当作有效修复。'],rejected:['此修改未采用','可查看历史依据。'],rolled_back:['此修改已回滚','后续修改需要重新发起。']};
    [title,note]=map[p.status]||[title,note];
    if(p.status==='pending'&&p.summary?.regressed>0){title=`先检查 ${p.summary.regressed} 条退化`;note='即使达到验证通过条件，也应先确认改坏的样本能否接受。'}
    if(f?.status==='open'&&!f.proposalId&&['failed','rejected','rolled_back'].includes(p.status)){title='重新生成修改';note='原问题已重开，保留这次修改作为历史证据。'}
  }
  return {title,note};
}
function evoDecisionHTML(r,p,proposalId=''){
  let {title:label,note}=evoRecommendation(r,p),tone='';
  if(p){
    const s=p.summary||{},state=evoChangeState(p);label=state.label;tone=state.tone;
    if(p.status==='pending'){
      if(s.regressed>0){label=`历史验证含 ${s.regressed} 条退化`;note='先检查退化样本，再决定是否试用。';tone='err'}
      else if(s.passed&&s.finished>0){label='历史验证通过';note=`${s.finished} 条样本 · 已修复 ${evoNum(s.fixed)} 条 · 尚未发布`;tone='ok'}
      else{label='等待有效验证结果';note='当前证据不足，暂不发布。';tone='warn'}
    }else if(p.status==='validating'){label='历史验证中';note=`已完成 ${s.finished||0}/${s.total||0} 条 · 当前正文未改变`}
    else if(p.status==='drafting'){note='完成后自动进行历史验证 · 当前正文未改变'}
  }else if(proposalId){label='正在读取修改';note='等待修改稿和验证结果。'}
  else if(r?.f?.triage&&evoLevelOf(r.f.assetKind,r.f.assetId)>=2&&!evoSnoozed(r.f)&&evoIssueState(r.f)!=='done'){
    label={fix:'建议处理',watch:'继续观察',noise:'可能偶发'}[r.f.triage.verdict]||'依据不足';
  }
  return `<strong class="${tone?`evo-decision-${tone}`:''}">${escapeHTML(label)}</strong><span>${escapeHTML(note)}</span>`;
}
function evoEffectSummaryHTML(p){
  const s=p.summary||{},count=(label,n,tone='')=>`<div><small>${label}</small><b class="${tone}">${evoNum(n)}</b></div>`;
  const counts=s.finished?`<div class="evo-effect-numbers">${count('历史验证 · 已修复',s.fixed,'evo-good')}${count('退化',s.regressed,s.regressed?'evo-bad':'')}${count('无变化',s.unchanged)}${s.changed?count('结论改变',s.changed):''}${s.blocked?count('阻塞',s.blocked):''}</div>`:'<p class="evo-muted">暂无验证结果</p>';
  const w=p.watch,c=p.canary,arms=w?[[w.baseline,'发布前'],[w.current,'发布后']]:c?[[c.control,'试用 · 当前'],[c.canary,'试用 · 修改稿']]:[];
  const comparison=arms.length?`<div class="evo-effect-numbers evo-effect-arms">${arms.map(([a,t])=>`<div><small>${t} · 一次成功</small><b>${a?.runs>0&&Number.isFinite(a.clean)?`${a.clean}/${a.runs}`:'—'}</b></div>`).join('')}</div>`:'';
  const progress=p.status==='canary'&&c?`<p class="evo-muted">${c.percent}% 试用 · 两组各需 ${c.minRuns} 次</p>`:'';
  return `${counts}${comparison}${progress}${p.error?`<p class="evo-bad">${escapeHTML(p.error)}</p>`:''}<details class="evo-detail-more"><summary>验证明细</summary>${evoValidationSummary(p)}<button type="button" class="kb-btn small" data-evo-process="${escapeHTML(p.id)}">查看运行与判定</button></details>`;
}
function evoDetailSection(n,title,body){return `<section class="evo-detail-section"><h3><span>${n}</span>${title}</h3>${body}</section>`}
function evoProblemHTML(r,p=null){
  const f=r?.f,c=r?.c;
  const records=r?`<div id="evoRunPreview" class="evo-run-preview"></div><details class="evo-detail-more" id="evoRunDetails"><summary>全部运行记录</summary><select class="kb-input" aria-label="运行结果" data-evo-record-status><option value="">全部结果</option><option value="failed">失败</option><option value="succeeded">成功</option><option value="reworked">返工后成功</option><option value="unavailable">记录不可用</option></select><div id="evoFindingRecords" aria-live="polite"></div></details><details class="evo-detail-more" id="evoDiagnosisDetails"><summary>诊断与处理</summary>${evoFindingStats(r)}<p>处理范围：${escapeHTML(f.assetName||f.assetId)} 的全部问题。</p><div class="evo-finding-actions">${evoIssueState(f)==='done'?`<span>${escapeHTML(f.closeNote||'已归档')}</span>`:`<button type="button" class="kb-btn small" data-evo-snooze="${escapeHTML(f.id)}" data-days="${evoSnoozed(f)?0:EVO_SNOOZE_DAYS}"${f.status!=='open'?' disabled':''}>${evoSnoozed(f)?'取消暂缓':'暂缓 7 天'}</button><button type="button" class="kb-btn quiet small" data-evo-dismiss="${escapeHTML(f.id)}">忽略</button>${!f.triage?'<button type="button" class="kb-btn small" data-evo-rediagnose>重新检查</button>':''}`}</div></details>`:'';
  const t=f?.triage,judgment=t?({fix:'建议处理',watch:'继续观察',noise:'可能偶发'}[t.verdict]||'待判断'):'依据不足';
  const estimate=t&&Number.isFinite(t.score)?`预计每周额外 ${evoNum(t.score)} 次问题运行`:'暂无影响估计';
  return `<h2>问题判断${r?`<span class="evo-muted">${c.runs||0} 次运行受影响</span>`:''}</h2><div class="evo-judgment-summary"><strong class="v-${escapeHTML(t?.verdict||'unknown')}">${escapeHTML(judgment)}</strong><span>${estimate}</span></div><p class="evo-judgment-scope">原问题判定针对整个${escapeHTML(evoKindLabel(f?.assetKind))}，不是本类问题的独立评分。</p>${p?.rationale?`<p class="evo-problem-lead">${escapeHTML(evoProblemText(c?.label||f?.title||p.title))}</p><details class="evo-detail-more evo-cause" id="evoRationaleDetails"><summary>修改理由 · 模型建议，待验证</summary><p id="evoFindingEvidence" class="evo-problem-lead">${escapeHTML(p.rationale)}</p></details>`:`<p id="evoFindingEvidence" class="evo-problem-lead">${escapeHTML(evoProblemText(f?.summary||c?.label||p?.title||'关联问题未载入'))}</p>`}${records}`;
}
function evoOpenFinding(id,key,fromRoute=false){
  if(!fromRoute)return evoNavigate(`#/evolution/findings/${encodeURIComponent(id)}/${encodeURIComponent(key||'')}`);
  const r=evoIssueRows().find(x=>x.f.id===id&&(key===undefined||x.key===key));
  if(!r){evoMountDetail('问题不可用',`finding:${id}:${key||''}`);$('#evoDetailBody').innerHTML='<div class="kb-empty">问题已删除或不在当前项目中</div>';return}
  const m=evoMountDetail(r.title,evoIssueKey(r),r),f=r.f,p=evoFindingProposal(f);
  m.proposalId=f.proposalId||p?.id||'';evoDetailLayout(m);
  if(m.proposalId)evoLoadDetailProposal(m);
  else $('#evoDetailActions').innerHTML=evoFindingActions(f);
  evoLoadFindingRuns(m);
}
function evoBeforeAfterHTML(p,fullContext=false,changesOnly=false,compact=false){
  if(!p.candidateContent)return `<div class="kb-line-note">${p.status==='drafting'?'正在生成修改…':'暂无指令修改'}</div>`;
  const old=p.baseContent||'',next=p.candidateContent,diff=evoLineDiff(old,next),keep=new Set();
  const numbered=old.split('\n').length*next.split('\n').length<=2500000;
  diff.forEach(([k],i)=>{if(k!=='ctx')for(let j=Math.max(0,i-2);j<=Math.min(diff.length-1,i+2);j++)keep.add(j)});
  const column=side=>{
    const out=[];let skipped=0,lineNo=0,changes=0;
    const flush=()=>{if(skipped){if(!changesOnly)out.push(`<div class="evo-diff-skip">… ${skipped} 行未改动</div>`);skipped=0}};
    diff.forEach(([k,line],i)=>{
      if(k!=='ctx'&&k!==side)return;
      lineNo++;
      if(!fullContext&&k==='ctx'&&(changesOnly||!keep.has(i))){skipped++;return}
      if(compact&&k===side&&++changes>3)return;
      flush();
      out.push(`<div class="evo-diff-line ${k}">${numbered?`<span class="evo-split-number">${lineNo}</span>`:''}<span>${k==='add'?'+':k==='del'?'−':' '}</span><code>${escapeHTML(line)}</code></div>`);
    });
    flush();
    if(compact&&changes>3)out.push(`<div class="evo-diff-skip">… ${changes-3} 行改动</div>`);
    return out.join('')||'<div class="kb-line-note">该侧没有改动行</div>';
  };
  const label=(version,title)=>`${version?`<b>v${version}</b>`:''}${title}`;
  return `<div class="evo-before-after">
    <section><h4>${label(p.baseVersion,'原指令')}</h4><div>${column('del')}</div></section>
    <span class="evo-split-direction">${evoReviewIcon('arrow-right')}</span>
    <section><h4>${label(p.releasedVersion,p.releasedVersion?'已发布指令':'AI 修改稿')}</h4><div>${column('add')}</div></section>
  </div>`;
}
function evoValidationSummary(p){
  const s=p.summary||{},hasResult=(s.total||0)>0;
  const counts=[['已修复',s.fixed,'ok'],['退化',s.regressed,'err'],['无变化',s.unchanged,''],['结论改变',s.changed,'info'],['阻塞',s.blocked,'warn']].filter(([,n])=>Number(n)>0);
  return `<div class="evo-validation-summary"><h4>用历史运行验证</h4><p>${hasResult?`换成修改稿正文重跑 ${s.total} 条历史运行，完成 ${s.finished||0} 条。`:'尚无历史运行验证结果。'}</p>${hasResult?`<div class="evo-count-row">${counts.map(([label,n,tone])=>kbBadge(`${label} ${n}`,tone)).join('')}</div>`:''}${s.verdict?`<p>${escapeHTML(evoResultText(s.verdict))}</p>`:''}${p.error?`<p class="kb-err">${escapeHTML(p.error)}</p>`:''}<small>“无变化”只说明结果相同；历史验证通过不保证未来没有问题。</small></div>${p.canary?`<details class="evo-detail-more"${p.status==='canary'?' open':''}><summary>真实运行的小范围试用结果</summary>${evoCanaryHTML(p.canary)}</details>`:''}${p.watch?`<details class="evo-detail-more" open><summary>发布后的运行表现</summary>${evoWatchHTML(p.watch)}<p>这是运行成功、返工与失败的对比，不等于原问题未复发或已彻底解决。</p></details>`:''}`;
}
function evoProposalActions(p){
  const reopened=(evo.findings||[]).find(f=>f.id===p.findingId&&f.status==='open'&&!f.proposalId);
  const retry=reopened&&['failed','rejected','rolled_back'].includes(p.status)?`<button type="button" class="kb-btn primary" data-evo-propose="${escapeHTML(reopened.id)}"${evoLevelOf(reopened.assetKind,reopened.assetId)<2?' disabled':''}>重新生成修改建议</button>`:'';
  const pid=escapeHTML(p.id),asset=retry;
  const ready=p.summary?.passed&&!!p.candidateContent;
  if(p.status==='pending'){
    const review=(p.replays||[]).some(r=>r.outcome==='regressed');
    return `${review?`<button type="button" class="kb-btn primary" data-evo-review-regressions>${evoReviewIcon('alert')}查看退化</button>`:''}<button type="button" class="kb-btn${review?'':' primary'}" data-evo-canary="${pid}"${ready?'':' disabled'}>${evoReviewIcon('play')}小范围试用</button><details class="evo-secondary-actions"><summary aria-label="更多操作" data-tooltip="更多操作">···</summary><div><button type="button" class="kb-btn" data-evo-approve="${pid}"${ready?'':' disabled'}>${evoReviewIcon('arrow-up')}直接发布</button><button type="button" class="kb-btn quiet" data-evo-reject="${pid}">${evoReviewIcon('close')}不采用</button>${asset}</div></details>`;
  }
  if(p.status==='canary')return `<button type="button" class="kb-btn quiet" data-evo-reject="${pid}" data-trial="1">${evoReviewIcon('stop')}停止试用</button>${asset}<button type="button" class="kb-btn primary" data-evo-approve="${pid}" data-trial="${p.canary?.status==='passed'?'passed':'running'}"${p.canary?.status==='passed'?'':' disabled'}>${evoReviewIcon('arrow-up')}发布</button>`;
  if(['drafting','validating'].includes(p.status))return `<button type="button" class="kb-btn quiet" data-evo-reject="${pid}">取消修改</button>${asset}`;
  if(p.status==='released'&&['watching','degraded'].includes(p.watch?.status)&&p.watch.prevVersion)return `${asset}<button type="button" class="kb-btn${p.watch.status==='degraded'?' primary':''}" data-evo-rollback="${p.watch.prevVersion}" data-kind="${escapeHTML(p.assetKind)}" data-id="${escapeHTML(p.assetId)}">${evoReviewIcon('undo')}回滚 v${p.watch.prevVersion}</button>`;
  if(p.status==='failed'&&p.candidateContent&&typeof isPlatformAdmin==='function'&&isPlatformAdmin())return `${asset}<button type="button" class="kb-btn" data-evo-approve="${pid}" data-force="1">强制发布</button>`;
  return asset;
}
function evoOpenProposal(id,fromRoute=false){
  if(!fromRoute)return evoNavigate(`#/evolution/proposals/${encodeURIComponent(id)}`);
  const m=evoMountDetail('正文修改',`proposal:${id}`);m.proposalId=id;
  evoDetailLayout(m);evoLoadDetailProposal(m);
}
async function evoLoadDetailProposal(m){
  if(evo.modal!==m)return;
  const request={};m.proposalRequest=request;m.liveVersion=null;m.liveVersionError=false;
  if(!m.proposal){$('#evoDetailActions').innerHTML='';}
  else $$('[data-evo-approve],[data-evo-canary],[data-evo-traffic-apply]').forEach(b=>b.disabled=true);
  try{
    const p=await api(`/api/evolution/proposals/${encodeURIComponent(m.proposalId)}`,{silent:true});
    if(evo.modal!==m||m.proposalRequest!==request||m.projectId!==evoProjectId())return;
    const currentForm=$('#evoDetailCanary'),formState=currentForm?.open?{percent:$('#evoCanaryPercent')?.value,min:$('#evoCanaryMinRuns')?.value,focus:currentForm.contains(document.activeElement)?document.activeElement.id:''}:null;
    for(const [key,data] of m.evidenceCache)if(data.replay){const current=(p.replays||[]).find(r=>r.id===data.replay.id);if(!current||!evoTime(current.finishedAt)||JSON.stringify(current)!==JSON.stringify(data.replay))m.evidenceCache.delete(key)}
    if(m.evidence?.type==='replay'&&!(p.replays||[]).some(r=>r.id===m.evidence.id))$('#evoEvidenceDialog').close();
    m.proposal=p;
    evoRefreshProcessTrial(p);
    if(m.openerKey.startsWith('proposal:'))m.openerKey=`asset:${evoAssetKey(p.assetKind,p.assetId)}`;
    if(!m.row)m.row=evoIssueRows().find(r=>r.f.id===p.findingId||r.f.proposalId===p.id)||null;
    evoDetailLayout(m,p);
    $('#evoTrialHost').innerHTML=p.status==='pending'&&p.summary?.passed&&p.candidateContent?`<dialog id="evoDetailCanary" class="evo-review-trial-dialog" aria-labelledby="evoTrialTitle"><header><h2 id="evoTrialTitle">${evoReviewIcon('flask')}试用</h2><button type="button" class="kb-btn quiet small" data-evo-trial-close aria-label="关闭试用设置">${evoReviewIcon('close')}</button></header>${evoCanaryFormHTML(p.id)}</dialog>`:'';
    if(formState&&$('#evoDetailCanary')){$('#evoCanaryPercent').value=formState.percent;$('#evoCanaryMinRuns').value=formState.min;$('#evoDetailCanary').showModal();if(formState.focus)document.getElementById(formState.focus)?.focus({preventScroll:true})}
    m.viewOpen.forEach(id=>{const d=document.getElementById(id);if(d)d.open=true});
    $('#evoDetailActions').innerHTML=evoProposalActions(p);
    $$('#evoDetailActions [data-evo-approve], #evoDetailActions [data-evo-canary], #evoDetailCanary [data-evo-canary]').forEach(b=>b.disabled=true);
    window.AgentFlowHarnessIcons?.hydrate($('#evoDetailPage'));
    if(m.row){if(m.data)evoRenderFindingRuns();else evoLoadFindingRuns(m)}
    evoLoadLiveVersion(m,p,request);
  }catch(err){
    if(evo.modal!==m||m.proposalRequest!==request||m.projectId!==evoProjectId())return;
    $('#evoDetailActions').innerHTML='';
    evoSyncTraffic();
    $('#evoProblemBox').innerHTML='<strong>修改暂不可用</strong><span>读取失败，请重试。</span>';
    const host=$('#evoLinkedChange')||$('#evoDetailBody');
    host.innerHTML=`<div class="kb-empty">读取修改失败：${escapeHTML(err.message)} <button type="button" class="kb-btn" data-evo-detail-refresh>重试</button></div>`;
    evoSetDetailTab('change',false);
  }
}
async function evoLoadLiveVersion(m,p,request){
  try{
    const v=await api(`/api/evolution/assets/${encodeURIComponent(p.assetKind)}/${encodeURIComponent(p.assetId)}/versions`,{silent:true});
    if(evo.modal!==m||m.proposalRequest!==request||m.projectId!==evoProjectId())return;
    let note='当前正文未匹配已登记版本，可在历史版本中核对。';
    const changed=!!(v.liveHash&&p.baseHash&&v.liveHash!==p.baseHash),draft=['pending','validating','drafting','canary','failed'].includes(p.status);
    if(v.currentVersion)note=`当前登记版本为 v${v.currentVersion}。`;
    if(p.status==='rolled_back')note+=' 此修改已回滚。';
    else if(p.releasedVersion&&v.currentVersion)note+=v.currentVersion===p.releasedVersion&&!v.drifted?' 此次发布的正文当前仍生效。':' 此次发布的正文已被替换或在版本外修改。';
    else if(draft&&changed)note+=' 当前正文已变化，不能直接发布这份旧修改稿，请重新生成建议。';
    else if(draft&&v.liveHash&&p.baseHash)note+=' 当前正文与修改前快照一致，修改稿尚未全量发布。';
    m.liveVersion=v;
    $('#evoLiveVersion').textContent=v.drifted?'正文存在版本外修改':draft&&changed?'正文已变化 · 需重新生成':v.currentVersion?`当前生效 v${v.currentVersion}`:'当前版本未登记';
    $('#evoLiveVersion').setAttribute('data-tooltip',note);
    evoSyncTraffic();
    evoRefreshReleaseAdvice(m,p);
    if(document.activeElement===document.body&&m.trafficFocus?.isConnected&&!m.trafficFocus.closest('[hidden]'))m.trafficFocus.focus({preventScroll:true});
    m.trafficFocus=null;
    $$('#evoDetailActions [data-evo-approve], #evoDetailActions [data-evo-canary], #evoDetailCanary [data-evo-canary]').forEach(b=>{
      const ready=(p.status==='canary'&&p.canary?.status==='passed')||(p.status==='pending'&&p.summary?.passed&&!!p.candidateContent)||(b.dataset.force==='1'&&!!p.candidateContent);
      b.disabled=!ready||evoGroupForAsset(p)?.conflict||(draft&&(changed||!v.liveHash||!p.baseHash));
    });
  }catch(err){
    if(evo.modal!==m||m.proposalRequest!==request||m.projectId!==evoProjectId())return;
    m.liveVersionError=true;
    $('#evoLiveVersion').textContent='版本核对失败 · 请刷新';
    $('#evoLiveVersion').setAttribute('data-tooltip',err.message);
    evoRefreshReleaseAdvice(m,p);
    evoSyncTraffic();
  }
}
function evoRefreshReleaseAdvice(m,p){
  const host=$('#evoReleaseAdvice');
  if(!host)return;
  const html=evoStoryReleaseAdviceHTML(m,p);
  if(host.innerHTML===html)return;
  host.innerHTML=html;
  window.AgentFlowHarnessIcons?.hydrate(host);
}
async function evoLoadFindingRuns(m){
  const host=$('#evoFindingRecords');if(!host||evo.modal!==m)return;
  host.innerHTML='<div class="kb-empty" role="status">正在读取对应运行…</div>';
  const request={};m.request=request;
  try{
    const data=await api(`/api/evolution/findings/${encodeURIComponent(m.row.f.id)}/runs?cluster=${encodeURIComponent(m.row.key)}`,{silent:true});
    if(evo.modal!==m||m.request!==request||m.projectId!==evoProjectId())return;
    m.data=data;m.dataError=null;
    evoRenderFindingRuns();
  }catch(err){
    if(evo.modal!==m||m.request!==request||m.projectId!==evoProjectId())return;
    m.dataError=err.message;
    const error=`<div class="evo-evidence-empty">读取问题样本失败：${escapeHTML(err.message)} <button type="button" class="kb-btn small" data-evo-record-retry>重试</button></div>`;
    const current=$('#evoFindingRecords');if(current)current.innerHTML=error;
  }
}
function evoRenderFindingRuns(){
  const m=evo.modal,host=$('#evoFindingRecords');if(!m?.data||!host)return;
  const list=(m.data.items||[]).filter(r=>!m.status||r.status===m.status),size=10,pages=Math.max(1,Math.ceil(list.length/size));
  m.page=Math.min(m.page,pages-1);
  const labels={failed:['失败','err'],succeeded:['成功','ok'],reworked:['返工后成功','warn'],cancelled:['已取消',''],unavailable:['记录不可用','']};
  const preview=$('#evoRunPreview');
  if(preview)preview.innerHTML=`<h3>问题样本</h3>${!m.data.complete?'<small class="evo-muted">仅保留部分样例</small>':''}${(m.data.items||[]).slice(0,3).map(r=>`<button type="button" data-evo-evidence-run="${escapeHTML(r.runId)}"${r.unavailable?' disabled':''} aria-pressed="${m.evidence?.type==='run'&&m.evidence.id===r.runId}"><span><b>${escapeHTML(r.issueTitle||r.runId)}</b><small>${escapeHTML(evoProblemText(r.detail||'无报错摘要'))}</small></span>${evoStatusBadge(labels,r.status)}</button>`).join('')||'<small class="evo-muted">暂无运行记录</small>'}`;
  const select=$('[data-evo-record-status]');if(select)select.value=m.status;
  const rows=list.slice(m.page*size,(m.page+1)*size).map(r=>`<tr><td>${escapeHTML(evoIssueDate(r.createdAt))}</td><td>${r.unavailable?escapeHTML(r.runId):`<a href="#" class="evo-run-link" data-evo-record-run="${escapeHTML(r.runId)}">${escapeHTML(r.runId)}</a>`}${r.nodeId?`<small>节点 ${escapeHTML(r.nodeId)}</small>`:''}</td><td>${escapeHTML(r.issueTitle||'—')}<small>${escapeHTML(r.workflowName||'')}</small></td><td>${evoStatusBadge(labels,r.status)}</td><td>${escapeHTML(r.unavailable?'运行已删除或当前项目不可访问':r.detail||'无报错详情')}</td></tr>`).join('');
  host.innerHTML=`${m.data.complete?'':'<div class="evo-record-note">旧版诊断只保留了部分样例，请重新诊断以获得当前窗口完整记录。</div>'}<div class="evo-record-table"><table><caption class="evo-record-caption">本类报错涉及 ${m.data.total||0} 次运行，当前筛选 ${list.length} 条；点击运行 ID 查看详情。</caption><thead><tr><th>出现时间</th><th>运行 / 节点</th><th>需求 / 工作流</th><th>运行结果</th><th>报错摘要</th></tr></thead><tbody>${rows||'<tr><td colspan="5" class="kb-empty">没有符合条件的运行记录</td></tr>'}</tbody></table></div><div class="evo-record-pages"><button type="button" class="kb-btn" data-evo-record-page="-1"${m.page===0?' disabled':''}>上一页</button><span>${m.page+1} / ${pages}</span><button type="button" class="kb-btn" data-evo-record-page="1"${m.page+1===pages?' disabled':''}>下一页</button></div>`;
  if(m.proposal)evoRenderComparisonList(m);
}

function evoRenderComparisonList(m){
  const host=$('#evoComparisonList');if(!host||!m.proposal)return;
  if($('#evoReviewWorkbench')?.classList.contains('evo-story-workbench')){
    const focused=document.activeElement;
    const replay=host.contains(focused)?focused.dataset.evoStoryReplay:'';
    host.innerHTML=evoStoryReplayTable(m);evoStoryUpdateSelection(m);
    $$('#evoValidationResults [data-evo-sample-filter]').forEach(button=>button.setAttribute('aria-pressed',String(button.dataset.evoSampleFilter===m.compareFilter)));
    window.AgentFlowHarnessIcons?.hydrate($('#evoReviewWorkbench'));
    if(replay)host.querySelector(`[data-evo-story-replay="${CSS.escape(replay)}"]`)?.focus({preventScroll:true});
    return;
  }
  if($('#evoReviewWorkbench')){
    host.innerHTML=evoReviewReplayTable(m);
    const full=$('#evoValidationList');if(full)full.innerHTML=evoReviewReplayTable(m,true);
    window.AgentFlowHarnessIcons?.hydrate($('#evoReviewWorkbench'));
    return;
  }
  const records=m.proposal.replays||[],tabs=[['all','全部'],['regressed','退化'],['fixed','已修复'],['changed','结论改变'],['blocked','阻塞'],['unchanged','无变化']];
  if(!records.length){host.innerHTML='<p class="evo-muted">暂无逐条回放记录</p>';return}
  const visible=records.filter(r=>m.compareFilter==='all'||r.outcome===m.compareFilter);
  host.innerHTML=`<div class="evo-sample-filters" role="group" aria-label="验证样本筛选">${tabs.filter(([k])=>k==='all'||records.some(r=>r.outcome===k)).map(([k,t])=>`<button type="button" data-evo-sample-filter="${k}" aria-pressed="${m.compareFilter===k}">${t}<span>${k==='all'?records.length:records.filter(r=>r.outcome===k).length}</span></button>`).join('')}</div><div class="evo-compare-columns" aria-hidden="true"><span>历史样本</span><span>验证时评分 → 回放</span><span>判定</span></div>${visible.map(r=>`<button type="button" class="evo-compare-row" data-evo-evidence-replay="${escapeHTML(r.id)}" aria-pressed="${m.evidence?.type==='replay'&&m.evidence.id===r.id}"><span><b>${escapeHTML(r.sourceRunId||'原运行不可用')}</b><small>${escapeHTML(EVO_GROUPS[r.group]?.[0]||r.group||'验证样本')}</small></span><span class="evo-result-pair">${escapeHTML(evoHistoricalScore(r.sourceScore))} <i>→</i> ${escapeHTML(r.finishedAt&&Number.isFinite(r.score)?evoScoreLabel(r.score):EVO_NODE_STATUS[r.status]?.[0]||r.status||'等待结果')}</span><span>${r.outcome?evoStatusBadge(EVO_OUTCOMES,r.outcome):'<span class="evo-muted">待判定</span>'}</span></button>`).join('')||'<p class="evo-muted">暂无此类结果</p>'}<p class="evo-compare-scope">同一运行输入重放；可能沿用上游产出，环境不保证完全一致。“已修复”仅指本样本的历史运行评分改善，不是语义质量评分，也不代表彻底解决或已发布。</p>`;
}
function evoEvidenceValid(m,request){return evo.modal===m&&m.evidenceRequest===request&&m.projectId===evoProjectId()&&evoActive()&&$('#evoEvidenceDialog')?.open!==false}
async function evoSelectEvidence(selection,refresh=true,reveal=false){
  const m=evo.modal,host=$('#evoEvidenceBody');if(!m||!host)return;
  const replay=selection.type==='replay'?(m.proposal?.replays||[]).find(r=>r.id===selection.id):null;
  const record=selection.type==='run'?(m.data?.items||[]).find(r=>r.runId===selection.id&&!r.unavailable):null;
  if(!replay&&!record)return;
  if(reveal){m.evidenceTouched=true;if(!replay||m.detailTab!=='change'){m.detailTab=replay?'validation':'records';evoSetDetailTab(m.detailTab,false)}}
  if(replay)m.lastReplay=selection.id;else m.lastRun=selection.id;
  $('#evoEvidenceCount').textContent=replay?(EVO_GROUPS[replay.group]?.[0]||'历史样例'):'问题运行';
  if(m.evidence?.type!==selection.type||m.evidence?.id!==selection.id){m.evidenceData=null;m.evidenceNode=''}
  m.evidence={type:selection.type,id:selection.id};const key=`${selection.type}:${selection.id}`,request={};m.evidenceRequest=request;
  evoStoryUpdateSelection(m);
  if(reveal&&!$('#evoEvidenceDialog')&&innerWidth<=850)host.closest('.evo-evidence-panel')?.scrollIntoView?.({block:'start',behavior:matchMedia('(prefers-reduced-motion: reduce)').matches?'instant':'smooth'});
  if(refresh){evoRenderComparisonList(m);$$('[data-evo-evidence-run]').forEach(b=>b.setAttribute('aria-pressed',selection.type==='run'&&b.dataset.evoEvidenceRun===selection.id));if(replay)$(`${m.detailTab==='validation'?'#evoValidationList':'#evoComparisonList'} [data-evo-evidence-replay="${CSS.escape(replay.id)}"]`)?.focus({preventScroll:true})}
  if(m.evidenceCache.has(key)){evoRenderEvidence(m,m.evidenceCache.get(key));return}
  host.innerHTML='<div class="evo-evidence-empty" role="status">正在读取运行证据…</div>';
  if($('#evoStoryBehavior'))$('#evoStoryBehavior').innerHTML=`${evoStorySampleResultHTML(replay)}<p class="evo-story-empty" role="status">正在读取样例…</p>`;
  try{
    let data;
    if(record){
      const run=await api(`/api/runs/${encodeURIComponent(record.runId)}`,{silent:true});
      if(!run||run.id!==record.runId||(m.projectId&&runProjectId(run)!==m.projectId))throw new Error('运行不可用或不属于当前项目');
      data={record,run};
    }else{
      const comparison=await api(`/api/evolution/proposals/${encodeURIComponent(m.proposal.id)}/replays/${encodeURIComponent(replay.id)}/compare`,{silent:true});
      if(!evoEvidenceValid(m,request))return;
      if(comparison.sourceRunId!==replay.sourceRunId||comparison.runId!==replay.runId||!Array.isArray(comparison.nodes))throw new Error('回放证据与当前样本不匹配');
      const ids=[replay.sourceRunId,replay.runId];
      const runs=await Promise.allSettled(ids.map(id=>id?api(`/api/runs/${encodeURIComponent(id)}`,{silent:true}):Promise.reject(new Error('没有产生运行'))));
      const side=(result,id)=>result.status==='fulfilled'&&result.value?.id===id&&(!m.projectId||runProjectId(result.value)===m.projectId)?{run:result.value}:{error:result.status==='rejected'?result.reason.message:'运行不可用或不属于当前项目'};
      data={replay,comparison,source:side(runs[0],ids[0]),candidate:side(runs[1],ids[1])};
    }
    if(!evoEvidenceValid(m,request))return;
    m.evidenceCache.set(key,data);evoRenderEvidence(m,data);
  }catch(err){
    if(evoEvidenceValid(m,request)){
      host.innerHTML=`<div class="evo-evidence-empty">读取证据失败：${escapeHTML(err.message)}<button type="button" class="kb-btn small" data-evo-evidence-retry>重试</button></div>`;
      if($('#evoStoryBehavior'))$('#evoStoryBehavior').innerHTML=`${evoStorySampleResultHTML(replay)}<p>读取样例失败 <button type="button" class="kb-btn small" data-evo-evidence-retry>重试</button></p>`;
    }
  }
}
function evoEvidenceRunLink(id,label){return id?`<a href="#" data-evo-record-run="${escapeHTML(id)}">${escapeHTML(label)}</a>`:''}
function evoNodeExcerpt(node){
  if(!node)return '<p class="evo-muted">未跑到此节点或记录不可用</p>';
  const text=node.output||node.error||node.gateResult?.summary||node.feedback;
  const error=node.error&&node.error!==text?`<details class="evo-detail-more"><summary>节点错误</summary><pre class="evo-output-excerpt">${escapeHTML(node.error)}</pre></details>`:'';
  return `${evoNodeSideHTML({status:node.status,rework:node.reworkCount,decision:node.gateResult?.decision})}${text?`<pre class="evo-output-excerpt">${escapeHTML(text)}</pre><small class="evo-muted">API 返回的记录摘要，可能已截断</small>`:'<p class="evo-muted">未记录输出摘要</p>'}${error}`;
}
function evoHistoricalScore(score){return Number.isInteger(score)&&score>=0&&score<EVO_SCORES.length?EVO_SCORES[score]:'未记录'}
function evoReplaySourceChanged(replay,source){
  return !!(source.run&&(replay.sourceStatus&&source.run.status!==replay.sourceStatus||replay.sourceScore===0&&source.run.status==='succeeded'));
}
function evoReplayJudgmentHTML(replay,source){
  const before=evoHistoricalScore(replay.sourceScore),after=replay.outcome&&replay.outcome!=='blocked'?evoHistoricalScore(replay.score):'尚无有效结果';
  const changed=evoReplaySourceChanged(replay,source);
  const hint={fixed:'仅表示这条历史样本的运行评分改善，不代表业务问题已彻底解决，也不代表已发布。',regressed:'这条历史样本的运行评分下降，应先检查退化原因。',unchanged:'这条样本的运行评分未改变，不代表产出内容完全相同。',changed:'修改稿改变了节点结论，需要人工确认是否符合反馈；不等于已修复。',blocked:'未得到有效验证结果，不能认定已修复。'}[replay.outcome]||'历史验证尚未完成。';
  return `<div class="evo-replay-judgment"><strong>验证时整次运行：${escapeHTML(before)} → ${escapeHTML(after)}</strong><p>${escapeHTML(hint)}</p>${changed?'<p class="evo-bad">原运行当前状态已不同于验证时的基线，可能已重试；下方两边成功不改变历史判定。</p>':''}<details class="evo-detail-more"><summary>判定与下方记录有什么区别？</summary><p>判定使用创建验证时保存的整次运行评分：失败为 0，返工后成功为 1，无返工成功为 2；评分提高标为“已修复”。下方展示当前可读取的节点状态与输出，不是验证时的原节点快照，原运行或回放运行重试后可能变化。</p></details></div>`;
}
function evoRenderEvidence(m,data){
  const host=$('#evoEvidenceBody');if(!host||evo.modal!==m)return;
  m.evidenceData=data;
  if($('#evoReviewWorkbench')){
    host.innerHTML=evoReviewEvidenceHTML(m,data);
    if($('#evoStoryBehavior')){evoStoryRenderBehavior(m,data);evoStoryLoadArtifacts(m,data)}
    window.AgentFlowHarnessIcons?.hydrate(host);return;
  }
  if(data.record){
    const {record,run}=data,nodes=run.nodes&&!Array.isArray(run.nodes)?run.nodes:{},ids=Object.keys(nodes);
    const chosen=ids.includes(m.evidenceNode)?m.evidenceNode:record.nodeId&&ids.includes(record.nodeId)?record.nodeId:ids.find(id=>nodes[id].status==='failed')||ids[0]||'';
    m.evidenceNode=chosen;
    host.innerHTML=`<div class="evo-evidence-context"><h3>${escapeHTML(record.issueTitle||record.runId)}</h3><small>${escapeHTML(record.workflowName||run.workflowId||'')} · ${escapeHTML(evoIssueDate(record.createdAt))}</small>${evoEvidenceRunLink(record.runId,'打开完整运行 ↗')}</div><div class="evo-evidence-signal"><small>问题信号</small><p>${escapeHTML(evoProblemText(record.detail||'无信号摘要'))}</p>${record.detail&&evoProblemText(record.detail)!==record.detail?`<details class="evo-detail-more"><summary>原始报错摘要</summary><pre class="evo-output-excerpt">${escapeHTML(record.detail)}</pre><small class="evo-muted">诊断保存的摘要，可能已截断；完整日志请打开运行查看。</small></details>`:''}</div><details class="evo-detail-more"><summary>运行输入</summary><pre class="evo-output-excerpt">${escapeHTML(run.input||'未记录输入')}</pre></details><label class="evo-evidence-node-label">节点<select class="kb-input" data-evo-evidence-node>${ids.map(id=>`<option value="${escapeHTML(id)}"${id===chosen?' selected':''}>${escapeHTML(id)}</option>`).join('')}</select></label><section class="evo-evidence-single">${evoNodeExcerpt(nodes[chosen])}</section>`;
  }else{
    const {replay,comparison,source,candidate}=data,nodes=comparison.nodes;
    const chosen=nodes.find(n=>n.id===m.evidenceNode)||nodes.find(n=>n.changed)||nodes.find(n=>n.shaped)||nodes[0];
    m.evidenceNode=chosen?.id||'';
    const reused=chosen?.reused;
    const before=source.run?.nodes?.[chosen?.id],after=candidate.run?.nodes?.[chosen?.id];
    host.innerHTML=`<div class="evo-evidence-context"><h3>历史验证判定与运行记录</h3><div>${evoStatusBadge(EVO_OUTCOMES,replay.outcome)}<span>${escapeHTML(EVO_GROUPS[replay.group]?.[0]||'历史样本')} · 仅限本样本</span></div></div>${evoReplayJudgmentHTML(replay,source)}<div class="evo-evidence-links">${evoEvidenceRunLink(replay.sourceRunId,'原运行最新记录 ↗')}${evoEvidenceRunLink(replay.runId,'修改稿回放记录 ↗')}</div>${comparison.input?`<details class="evo-detail-more"><summary>验证使用的输入 · 摘要</summary><pre class="evo-output-excerpt">${escapeHTML(comparison.input)}</pre></details>`:''}${replay.error?`<p class="evo-bad">${escapeHTML(replay.error)}</p>`:''}<label class="evo-evidence-node-label">查看节点记录<select class="kb-input" data-evo-evidence-node>${nodes.map(n=>`<option value="${escapeHTML(n.id)}"${n.id===chosen?.id?' selected':''}>${escapeHTML(n.name||n.id)}${n.reused?' · 沿用':n.shaped?' · 受修改稿影响':''}</option>`).join('')}</select></label><div class="evo-output-pair"><section><h4>原运行最新记录</h4><small class="evo-muted">不是验证时的历史快照</small>${source.error?`<p class="evo-muted">原输出不可用：${escapeHTML(source.error)}</p>`:evoNodeExcerpt(before)}</section><section><h4>${reused?'回放保存的沿用产出':'修改稿回放记录'}</h4>${reused?'<small class="evo-muted">此节点未重跑，产出从原运行复制</small>':''}${candidate.error?`<p class="evo-muted">回放输出不可用：${escapeHTML(candidate.error)}</p>`:reused&&!after?'<p class="evo-muted">回放未保存该节点的沿用产出，不以原运行最新输出补齐。</p>':evoNodeExcerpt(after)}</section></div>${chosen?.source?.reason||chosen?.replay?.reason?`<details class="evo-detail-more"><summary>当前记录中的节点判定理由</summary>${chosen.source?.reason?`<p>原运行当前记录：${escapeHTML(chosen.source.reason)}</p>`:''}${chosen.replay?.reason?`<p>回放当前记录：${escapeHTML(chosen.replay.reason)}</p>`:''}</details>`:''}`;
  }
}

// ---------- 流程步骤：提案在「发现问题 → … → 发布后观察」里走到哪一步 ----------
const EVO_STAGES=[['diagnose','发现问题'],['draft','改写正文'],['replay','回放验证'],['canary','小范围试用'],['release','发布'],['watch','发布后观察']];
const EVO_STEP_ICON={done:'<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3.5 8.5l3 3 6-7" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>',fail:'!',stop:'×',skip:'–'};
const EVO_STEP_STATE={done:'已完成',active:'进行中',wait:'等你决定',fail:'未通过',stop:'已停止',skip:'跳过',todo:'未开始'};
function evoStages(p){
  const s=p.summary||{},c=p.canary,w=p.watch;
  const st={diagnose:'done',draft:'todo',replay:'todo',canary:'todo',release:'todo',watch:'todo'};
  const note={diagnose:p.findingId?'来自问题队列':'手动发起',draft:'',replay:s.total?`${s.finished||0}/${s.total} 条`:'',canary:'',release:'',watch:''};
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
  const meta=`${c.percent}% 新运行使用修改稿 · 每组至少 ${c.minRuns} 次${c.auto?' · L3 自动发起':''} · ${relativeTime(c.startedAt)}开始${ended?` · ${relativeTime(c.endedAt)}结束`:''}`;
  return `${kbCaption(`小范围试用 · ${meta}`)}<div class="evo-trial-head">${evoStatusBadge(EVO_CANARY_STATUS,c.status)}<span>修改稿比线上差的概率 <b class="${c.probWorse>=0.8?'kb-err':''}">${evoPct(c.probWorse)}</b></span></div>${evoArmsHTML([['修改稿正文',c.canary],['线上正文',c.control]],c.minRuns,'试用对比')}`;
}
function evoWatchHTML(w){
  const meta=`v${w.prevVersion||'?'} → v${w.version} · 至少 ${w.minRuns} 次运行 · ${relativeTime(w.startedAt)}开始`;
  return `${kbCaption(`发布后观察 · ${meta}`)}<div class="evo-trial-head">${evoStatusBadge(EVO_WATCH_STATUS,w.status)}<span>新版本比旧版本差的概率 <b class="${w.probWorse>=0.9?'kb-err':''}">${evoPct(w.probWorse)}</b></span></div>${evoArmsHTML([[`发布前 v${w.prevVersion||'?'}`,w.baseline],[`发布后 v${w.version}`,w.current]],0,'发布前后对比')}`;
}
function evoCanaryFormHTML(pid){
  const opts=(list,def,unit)=>list.map(v=>`<option value="${v}"${v===def?' selected':''}>${v}${unit}</option>`).join('');
  return `<p class="evo-review-trial-hint">仅影响之后启动的新运行。</p><div class="kb-field evo-canary-form"><label for="evoCanaryPercent">试用流量 / 每组样本</label><div class="kb-field-pair"><select id="evoCanaryPercent" class="kb-input" aria-label="试用比例">${opts(EVO_CANARY_PERCENTS,50,'% 新运行')}</select><select id="evoCanaryMinRuns" class="kb-input" aria-label="每组最少运行数">${opts(EVO_CANARY_MIN_RUNS,5,' 次')}</select></div></div><button type="button" class="kb-btn primary evo-canary-go" data-evo-canary="${escapeHTML(pid)}">${evoReviewIcon('play')}开始试用</button>`;
}

// 每一步的详情面板；抽屉里一次只显示一个。
function evoStagePanelHTML(p,k){
  const s=p.summary||{};
  switch(k){
    case 'diagnose':
      return `<p class="evo-panel-lead">改进器从近期运行里发现了下面的问题，并据此改写「${escapeHTML(p.assetName||p.assetId)}」的正文。</p>${p.rationale?`${kbCaption('为什么改')}<p class="evo-rationale">${escapeHTML(p.rationale)}</p>`:'<div class="kb-line-note">没有记录改进理由</div>'}<p class="evo-panel-meta">${p.model?`模型 ${escapeHTML(p.model)} · `:''}${relativeTime(p.createdAt)}发起</p>`;
    case 'draft':{
      if(!p.candidateContent)return `<div class="kb-line-note">${p.status==='drafting'?'改进器正在改写正文…':'改进器没有给出修改稿正文'}</div>`;
      const d=evoDiffStats(p.baseContent,p.candidateContent);
      return `<div class="evo-change-sum"><span class="kb-ok">+${d.add} 行</span><span class="kb-err">−${d.del} 行</span><span>绿色是新加的、红色删除线是去掉的；同一句里改动的字词单独加深标出</span></div>${evoDiffHTML(p.baseContent,p.candidateContent)}`;
    }
    case 'replay':{
      const replays=p.replays||[];
      if(!replays.length)return '<div class="kb-line-note">还没有开始回放</div>';
      const counts=[['已修复',s.fixed,'ok'],['退化',s.regressed,'err'],['无变化',s.unchanged,''],['结论改变',s.changed,'info'],['阻塞',s.blocked,'warn']].filter(([,v])=>v);
      return `<p class="evo-panel-lead">挑了 ${s.total||replays.length} 条历史运行，把「${escapeHTML(p.assetName||p.assetId)}」换成新正文后重跑，和原来的结果逐条对比。</p><div class="evo-count-row">${counts.map(([l,v,t])=>kbBadge(`${l} ${v}`,t)).join('')}<span>完成 ${s.finished||0}/${s.total||0}</span></div>${replays.map((r,i)=>evoReplayCard(r,i,p.id)).join('')}`;
    }
    case 'canary':
      if(p.canary)return p.status==='canary'&&evo.modal?.proposalId===p.id?evoReviewTrialHTML(p):evoCanaryHTML(p.canary);
      if(p.status==='pending'&&s.passed&&p.candidateContent)return evo.modal?.proposalId===p.id?'<p class="evo-panel-lead">回放已通过，可以开始小范围试用。</p>':evoCanaryFormHTML(p.id);
      if(p.status==='released'||p.status==='rolled_back')return '<div class="kb-line-note">这次没有试用，回放通过后直接发布了。</div>';
      return '<div class="kb-line-note">没有进行试用。</div>';
    case 'release':
      return `<p class="evo-panel-lead">${p.releasedVersion?`新正文已写入资产，记为不可变版本 <b class="mono">v${p.releasedVersion}</b>，之后的运行都会使用它。`:'还没有发布。'}</p>${p.decidedAt?`<p class="evo-panel-meta">${relativeTime(p.decidedAt)}${p.decidedBy?'由人工':'由系统'}处理</p>`:''}${p.decisionNote?`${kbCaption('处理备注')}<p class="evo-rationale">${escapeHTML(p.decisionNote)}</p>`:''}`;
    case 'watch':
      return p.watch?evoWatchHTML(p.watch)+(p.status==='released'&&evo.modal?.proposalId===p.id?evoReviewTrafficHTML(p):''):'<div class="kb-line-note">没有发布后观察记录。</div>';
  }
  return '';
}
function evoSyncTraffic(){
  const m=evo.modal,p=m?.proposal,block=$('#evoStagePanel #evoTrafficBlock');
  if(!block)return;
  const v=m?.liveVersion,verified=!!v?.liveHash&&p?.id===evo.drawerProposal?.id;
  const trial=verified&&p.status==='canary'&&p.baseHash===v.liveHash;
  const release=verified&&p.status==='released'&&!!p.releasedVersion&&p.releasedVersion===v.currentVersion&&!v.drifted;
  block.hidden=!(trial||release);
  const button=block.querySelector('[data-evo-traffic-apply]');
  if(button)button.disabled=!trial||p.canary?.status!=='running'||!!evoGroupForAsset(p)?.conflict;
}
function evoRefreshProcessTrial(p){
  const prev=evo.drawerProposal,stage=evo.stage;
  if(!kbDrawerIsOpen()||!$('#evoStagePanel')||prev?.id!==p.id)return;
  evo.drawerProposal=p;
  if(!['canary','watch'].includes(stage)||
    (prev.status===p.status&&JSON.stringify(prev[stage])===JSON.stringify(p[stage])))return;
  const select=$('#evoTrafficPercent'),value=select?.value,focused=document.activeElement===select;
  evoSelectStage(stage);
  const next=$('#evoTrafficPercent');
  if(next&&value&&prev.canary?.percent===p.canary?.percent)next.value=value;
  if(next&&focused)evo.modal.trafficFocus=next;
}
function evoSelectStage(k){
  const p=evo.drawerProposal;if(!p)return;
  evo.stage=k;
  $$('#kbDrawer .evo-steps [data-evo-stage]').forEach(b=>b.setAttribute('aria-selected',String(b.dataset.evoStage===k)));
  const host=$('#evoStagePanel');
  if(host){host.innerHTML=evoStagePanelHTML(p,k);evoSyncTraffic();window.AgentFlowHarnessIcons?.hydrate(host);host.classList.remove('is-in');void host.offsetWidth;host.classList.add('is-in')}
}

async function evoOpenProposalProcess(id,stage){
  const seq=evoDrawerOpen('完整处理过程','',`<div class="kb-empty">正在读取提案…</div>`,'',{openerKey:`proposal:${id}`,wide:true});
  try{
    const p=await api(`/api/evolution/proposals/${encodeURIComponent(id)}`,{silent:true});
    if(!evoDrawerLive(seq))return;
    const s=p.summary||{};
    const {steps,panel:current}=evoStages(p),panel=stage||current;
    evo.drawerProposal=p;evo.stage=panel;
    const body=`${evoStepperHTML(steps,panel)}${evoNowHTML(p)}<section id="evoStagePanel" class="evo-stage-panel is-in" role="tabpanel">${evoStagePanelHTML(p,panel)}</section>`;
    const canCanary=p.status==='pending'&&s.passed&&!!p.candidateContent;
    const asset=`<button type="button" class="kb-btn" data-evo-asset="${escapeHTML(p.assetKind)}" data-id="${escapeHTML(p.assetId)}">版本</button>`;
    const pid=escapeHTML(p.id);
    let foot=asset;
    if(p.status==='pending')foot=`<button type="button" class="kb-btn quiet" data-evo-reject="${pid}">驳回</button>${asset}<button type="button" class="kb-btn" data-evo-canary="${pid}"${canCanary?'':' disabled'}>开始试用</button><button type="button" class="kb-btn primary" data-evo-approve="${pid}"${s.passed?'':' disabled'}>直接发布</button>`;
    else if(p.status==='canary')foot=`<button type="button" class="kb-btn quiet" data-evo-reject="${pid}" data-trial="1">终止试用</button>${asset}<button type="button" class="kb-btn primary" data-evo-approve="${pid}" data-trial="${p.canary?.status==='passed'?'passed':'running'}"${p.canary?.status==='passed'?'':' disabled'}>全量发布</button>`;
    else if(p.status==='validating'||p.status==='drafting')foot=`<button type="button" class="kb-btn quiet" data-evo-reject="${pid}">取消提案</button>${asset}`;
    else if(p.status==='released'&&(p.watch?.status==='watching'||p.watch?.status==='degraded')&&p.watch.prevVersion)foot=`${asset}<button type="button" class="kb-btn${p.watch.status==='degraded'?' primary':''}" data-evo-rollback="${p.watch.prevVersion}" data-kind="${escapeHTML(p.assetKind)}" data-id="${escapeHTML(p.assetId)}">回滚到 v${p.watch.prevVersion}</button>`;
    else if(p.status==='failed'&&p.candidateContent&&typeof isPlatformAdmin==='function'&&isPlatformAdmin())foot=`${asset}<button type="button" class="kb-btn" data-evo-approve="${pid}" data-force="1" title="仅平台管理员：跳过回放判定直接发布">强制发布</button>`;
    const sub=`${evoKindLabel(p.assetKind)} · ${escapeHTML(p.assetName||p.assetId)} · ${evoStatusBadge(EVO_PROPOSAL_STATUS,p.status)}${p.model?` · ${escapeHTML(p.model)}`:''}`;
    evoDrawerOpen(p.title||'改进提案',sub,body,foot,{wide:true});
    evoSyncTraffic();
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
    const sopAssets=typeof evoSOPState!=='undefined'&&evoSOPState.view?.detail?.assets?.length>1?evoSOPState.view.detail.assets:null;
    const switcher=sopAssets?`<div class="evo-sop-seg" role="tablist" aria-label="切换 SOP 关联角色或规则" style="margin-bottom:10px">${sopAssets.map(a=>`<button type="button" role="tab" data-evo-asset="${escapeHTML(a.kind)}" data-id="${escapeHTML(a.id)}" aria-pressed="${a.kind===kind&&a.id===id}">${escapeHTML(a.name||a.id)}${a.currentVersion?` v${a.currentVersion}`:''}</button>`).join('')}</div>`:'';
    const compare=pick?`${kbCaption(`v${pick.version} → 当前正文`)}${evoDiffHTML(pick.content,v.liveContent)}`:`${kbCaption('当前正文')}<pre class="kb-pre evo-content">${escapeHTML(v.liveContent||'')}</pre>`;
    const level=(EVO_LEVELS.find(([l])=>l===v.level)||[])[1]||'';
    const body=`${switcher}${drift}${kbCaption(`版本 · ${versions.length}${level?` · 自治级别 ${escapeHTML(level)}`:''}`)}${rows||'<div class="kb-line-note">还没有版本记录；首次发布或回滚时会把当前正文记为基线 v1。</div>'}${compare}`;
    const canPropose=v.level>=2;
    const inSop=typeof evoSOPState!=='undefined'&&!!evoSOPState.view;
    const foot=inSop?'':`<button type="button" class="kb-btn primary" data-evo-propose-asset="${escapeHTML(kind)}" data-id="${escapeHTML(id)}"${canPropose?'':' disabled title="自治级别低于 L2"'}>生成提案</button>`;
    evoDrawerOpen(v.assetName||id,`${evoKindLabel(kind)} · 当前 ${v.currentVersion?`v${v.currentVersion}`:'未登记'}`,body,foot,{wide:true});
  }catch(err){if(evoDrawerLive(seq))$('#kbDrawerBody').innerHTML=`<div class="kb-empty">读取失败：${escapeHTML(err.message)}</div>`}
}

// ---------- 操作 ----------
async function evoAfterChange(openProposal='',context={pid:evo.projectId,detail:evo.modal}){
  await evoRefresh();
  if(context.pid!==evoProjectId()||!evoActive())return;
  if(openProposal&&context.detail&&evo.modal===context.detail&&context.detail.proposalId!==openProposal){
    evoSchedulePoll();
    evoOpenProposal(openProposal);
  }else await evoRender();
}

async function evoDiagnose(btn){
  if(!evo.projectId)return toast('请先选择项目','warn');
  if(evo.diagnosing)return;
  const pid=evo.projectId;evo.diagnosing=true;
  evo.checkError='';evoHomeSyncChecking();
  if(btn)btn.disabled=true;
  try{
    const res=await api('/api/evolution/diagnose',{method:'POST',body:JSON.stringify({projectId:pid})});
    const fix=(res.findings||[]).filter(f=>f.triage?.verdict==='fix').length;
    toast(`诊断完成：${res.runs||0} 次运行，${(res.findings||[]).length} 个问题，其中 ${fix} 个值得修复${res.archived?`，自动归档 ${res.archived} 个`:''}`,'ok');
    if(pid===evoProjectId()){evo.lastCheck={projectId:pid,runs:res.runs||0,findings:(res.findings||[]).length,at:new Date().toISOString()};evo.selected.clear();await evoAfterChange()}
  }catch(err){if(pid===evoProjectId()){evo.checkError=err.message;if(!evo.modal)evoRender()}toast(err.message,'err')}
  finally{evo.diagnosing=false;evoHomeSyncChecking();if(btn?.isConnected)btn.disabled=false}
}
async function evoReload(btn){
  if(!evo.projectId)return toast('请先选择项目','warn');
  btn.disabled=true;
  try{await evoRefresh();evoRender()}finally{btn.disabled=false}
}
async function evoSnoozeSelected(btn){
  if(evo.busy.has('batch'))return;
  const pid=evo.projectId,selected=evoFilteredIssues().filter(r=>evo.selected.has(evoIssueKey(r))),findings=[...new Map(selected.map(r=>[r.f.id,r.f])).values()];
  if(!findings.length)return;
  if(findings.some(f=>f.status!=='open'))return toast('仅待处理资产可以暂缓，请取消选择已提案或已关闭的问题','warn');
  if(!await afConfirm({title:'暂缓所选资产',message:`将暂缓 ${findings.length} 个资产 7 天，包含这些资产的所有报错，不限于选中的类别。`,confirmText:'暂缓 7 天'}))return;
  if(pid!==evoProjectId())return;
  evo.busy.add('batch');btn.disabled=true;
  try{
    let failed=0;
    for(const f of findings){try{await api(`/api/evolution/findings/${encodeURIComponent(f.id)}/snooze`,{method:'POST',body:JSON.stringify({days:7})})}catch{failed++}}
    toast(failed?`${findings.length-failed} 个资产已暂缓，${failed} 个失败；刷新后可重试`:`已暂缓 ${findings.length} 个资产`,failed?'warn':'ok');
    if(pid===evoProjectId()){evo.selected.clear();await evoAfterChange()}
  }finally{evo.busy.delete('batch');btn.disabled=false}
}

async function evoPropose(payload,btn){
  if(btn)btn.disabled=true;
  const pid=evo.projectId,modal=evo.modal,drawerSeq=kb.drawer.seq;
  const live=()=>pid===evoProjectId()&&evoActive()&&evo.modal===modal&&kb.drawer.seq===drawerSeq;
  try{
    const p=await api('/api/evolution/proposals',{method:'POST',body:JSON.stringify({projectId:pid,...payload})});
    const queued=p.queued||p.status==='queued',item=p.queue||p;
    toast(queued?'已加入后续改进，当前修改不受影响':p.status&&p.status!=='drafting'?'已关联当前改进，不另开并行修改稿':'已开始生成修改稿正文，完成后自动回放','ok');
    if(pid!==evoProjectId())return;
    await evoRefresh();
    if(live()){if(queued){evoRender();evoOpenQueue(item.assetKind||payload.assetKind||modal?.row?.f.assetKind,item.assetId||payload.assetId||modal?.row?.f.assetId)}else evoOpenProposal(p.id)}
    else if(!evo.modal)evoRender();
  }catch(err){toast(err.message,'err');if(btn)btn.disabled=false}
}

async function evoDismiss(id){
  const modal=evo.modal,pid=evo.projectId,drawerSeq=kb.drawer.seq;
  const reason=await afPrompt({title:'忽略资产问题',message:'这会忽略该资产本次诊断的所有报错，而不只是当前一类。再次诊断仍可能重新出现。\n\n原因（可选）：',defaultValue:''});
  if(reason===null||reason===undefined)return;
  try{
    await api(`/api/evolution/findings/${encodeURIComponent(id)}/dismiss`,{method:'POST',body:JSON.stringify({reason:String(reason).trim()})});
    if(evo.modal===modal)evoCloseFinding();
    if(kb.drawer.seq===drawerSeq)evoDrawerClose();
    toast('已忽略','ok');
    if(pid===evoProjectId())await evoAfterChange();
  }catch(err){toast(err.message,'err')}
}

async function evoSnooze(id,days,btn){
  if(btn)btn.disabled=true;
  const modal=evo.modal;
  try{
    await api(`/api/evolution/findings/${encodeURIComponent(id)}/snooze`,{method:'POST',body:JSON.stringify({days})});
    toast(days?`已暂缓 ${days} 天`:'已取消暂缓','ok');
    await evoAfterChange();
    if(modal&&evo.modal===modal)evoOpenFinding(id,modal.row.key);
  }catch(err){toast(err.message,'err');if(btn)btn.disabled=false}
}

async function evoApprove(id,force,btn,trial=''){
  const context={pid:evo.projectId,detail:evo.modal};
  let title=force?'强制发布':'确认发布';
  let message=force?'回放没有通过判定。强制发布会直接把修改稿正文写入资产并记一个新版本，确定继续？':'发布后修改稿正文会写入资产，并记为新的不可变版本，之后的运行都会使用它；发布后会持续对比新旧版本，可以随时回滚。';
  if(trial){
    title='全量发布';
    message=trial==='passed'?'试用通过：修改稿在真实运行上不差于线上正文。全量发布会把修改稿写入资产并记为新版本，之后的运行都会使用它，并进入发布后观察。':'试用还没有得出结论（运行数不足）。仍然全量发布会把修改稿写入资产并结束试用，确定继续？';
  }
  const note=await afPrompt({title,message:`${message}\n\n备注（可选）：`,defaultValue:''});
  if(note===null||note===undefined)return;
  if(context.pid!==evoProjectId()||context.detail!==evo.modal||!evoActive())return;
  if(btn)btn.disabled=true;
  try{
    const p=await api(`/api/evolution/proposals/${encodeURIComponent(id)}/approve`,{method:'POST',body:JSON.stringify({note,force:!!force})});
    toast(`已发布 v${p.releasedVersion||''}，进入发布后观察`,'ok');
    await evoAfterChange(p.id,context);
  }catch(err){toast(err.message,'err');if(btn)btn.disabled=false}
}

async function evoStartCanary(id,btn){
  const context={pid:evo.projectId,detail:evo.modal};
  const form=$('#evoDetailCanary');
  if(form&&!form.open){if(!form.querySelector('[data-evo-canary]')?.disabled){form.showModal();$('#evoCanaryPercent')?.focus()}return}
  if(!$('#evoCanaryPercent')){evoSelectStage('canary');$('#evoCanaryPercent')?.focus();return}
  const percent=Number($('#evoCanaryPercent')?.value)||50,minRuns=Number($('#evoCanaryMinRuns')?.value)||5;
  form?.close();
  const note=await afPrompt({title:'开始试用',message:`${percent}% 的新业务运行会使用修改稿正文，其余保持线上正文；两组各满 ${minRuns} 次后对比。资产正文在全量发布前不会改动。\n\n备注（可选）：`,defaultValue:''});
  if(note===null||note===undefined)return;
  if(context.pid!==evoProjectId()||context.detail!==evo.modal||!evoActive())return;
  if(btn)btn.disabled=true;
  try{
    const p=await api(`/api/evolution/proposals/${encodeURIComponent(id)}/approve`,{method:'POST',body:JSON.stringify({mode:'canary',percent,minRuns,note})});
    toast('已开始试用','ok');
    await evoAfterChange(p.id,context);
  }catch(err){toast(err.message,'err');if(btn)btn.disabled=false}
}

async function evoReject(id,btn,trial=false){
  const context={pid:evo.projectId,detail:evo.modal};
  const note=await afPrompt(trial?{title:'终止试用',message:'终止后新运行不再使用修改稿正文（已开始的运行不受影响），提案记为驳回，问题重新回到队列。原因（可选）：',defaultValue:''}:{title:'驳回提案',message:'驳回后正在进行的回放会被取消，问题重新回到队列。原因（可选）：',defaultValue:''});
  if(note===null||note===undefined)return;
  if(btn)btn.disabled=true;
  try{await api(`/api/evolution/proposals/${encodeURIComponent(id)}/reject`,{method:'POST',body:JSON.stringify({note})});toast(trial?'已终止试用':'已驳回','ok');await evoAfterChange(id,context)}
  catch(err){toast(err.message,'err');if(btn)btn.disabled=false}
}

async function evoRollback(kind,id,version,btn){
  const note=await afPrompt({title:`回滚到 v${version}`,message:'回滚会把该版本的正文写回资产，并追加一个「回滚」版本；已经跑完的运行不受影响。备注（可选）：',defaultValue:''});
  if(note===null||note===undefined)return;
  if(btn)btn.disabled=true;
  try{
    const v=await api(`/api/evolution/assets/${encodeURIComponent(kind)}/${encodeURIComponent(id)}/rollback`,{method:'POST',body:JSON.stringify({version,note})});
    toast(`已回滚，当前为 v${v.version}`,'ok');
    await evoRefresh();
    if(typeof evoSOPState!=='undefined'&&evoSOPState.view&&typeof evoSOPLoadDetail==='function'){
      await evoSOPLoadDetail(evoSOPState.view);
    }else{
      evoRender();
    }
    evoOpenAsset(kind,id);
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
    host.querySelector('.run-header-meta')?.insertAdjacentHTML('afterbegin','<span class="run-meta-chip evo-replay-chip" title="修改稿正文的回放运行：不交付需求、不发通知、不计入统计">进化回放</span>');
    return;
  }
  if((r.executionContext?.evolutionCanaryIds||[]).length)host.querySelector('.run-header-meta')?.insertAdjacentHTML('afterbegin','<span class="run-meta-chip evo-replay-chip" title="本次运行参与进化试用：受试资产使用修改稿正文，结果计入试用对比">进化试用</span>');
  if(!['succeeded','failed','cancelled'].includes(r.status)||!r.executionContext?.projectId)return;
  const actions=host.querySelector('.run-header-actions');if(!actions)return;
  actions.insertAdjacentHTML('afterbegin',`<button class="button ghost small" data-evo-feedback="${escapeHTML(r.id)}" title="把问题反馈给RSI工作台，参与角色与规则的诊断">反馈问题</button><button class="button ghost small" data-evo-pin="${escapeHTML(r.id)}" title="每次进化提案都会回放这次运行">钉为回归样本</button>`);
  actions.querySelector('[data-evo-feedback]')?.addEventListener('click',async ev=>{
    const btn=ev.currentTarget,nodeId=state.selectedRunNode||'';
    const detail=await afPrompt({title:'反馈问题',message:'请写清预期结果、实际结果和需要改进的地方。提交后会整理为关联角色的问题。',defaultValue:'',multiline:true});
    if(!detail||!String(detail).trim())return;
    btn.disabled=true;
    try{
      const result=await api(`/api/runs/${encodeURIComponent(r.id)}/feedback`,{method:'POST',body:JSON.stringify({detail:String(detail).trim(),nodeId})});
      if(result.findingId&&evo.projectId===result.projectId)evo.lastCheck=null;
      if(btn.isConnected){
        host.querySelector('.evo-feedback-result')?.remove();
        const notice=document.createElement('div');notice.className='evo-feedback-result';notice.setAttribute('role','status');
        notice.innerHTML=`<span>${escapeHTML(result.message||'反馈已保存，请在RSI工作台检查最近运行后查看。')}</span>${result.findingId?'<button type="button">查看问题 ↗</button>':''}`;
        notice.querySelector('button')?.addEventListener('click',()=>evoOpenFeedbackFinding(result));host.prepend(notice);
      }
      toast(result.findingId?'反馈已加入RSI工作台':'反馈已保存，详见提示','ok');
    }catch(err){toast(err.message,'err')}
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

async function evoOpenFeedbackFinding(result){
  const pid=result.projectId;
  if(state.projectFilter!==pid){
    const picker=$('#projectSwitcher');
    if(!picker?.onchange||![...picker.options].some(o=>o.value===pid))return toast('请先选择该运行所属项目，再打开RSI工作台','warn');
    await picker.onchange({target:{value:pid}});
    if(state.projectFilter!==pid)return;
  }
  switchView('evolution');await renderEvolutionView();
  if(evoActive()&&evoProjectId()===pid&&!evo.error)evoOpenFinding(result.findingId,result.clusterId||'');
}

// ---------- 全局交互 ----------
document.addEventListener('click',e=>{
  if(e.target.closest('#evoDiagnose'))return evoDiagnose(e.target.closest('#evoDiagnose'));
  if(e.target.closest('#evoRefresh'))return evoReload(e.target.closest('#evoRefresh'));
  const menu=e.target.closest('.evo-row-menu');
  document.querySelectorAll('.evo-row-menu[open]').forEach(m=>{if(m!==menu||!e.target.closest('summary'))m.open=false});
  const scope=e.target.closest('#evolutionView, #kbDrawer');if(!scope)return;
  const progress=e.target.closest('[data-evo-progress-target]');
  if(progress){
    const target=document.getElementById(progress.dataset.evoProgressTarget)||$('#evoProblemEvidenceDetails');
    if(target?.tagName==='DETAILS')target.open=true;
    target?.scrollIntoView?.({block:'nearest'});
    return;
  }
  const stage=e.target.closest('[data-evo-progress-stage]');
  if(stage&&evo.modal?.proposalId)return evoOpenProposalProcess(evo.modal.proposalId,stage.dataset.evoProgressStage);
  const example=e.target.closest('[data-evo-source-example]');
  if(example&&evo.modal){
    const m=evo.modal,kind=example.dataset.evoSourceExample,host=$('#evoStorySourceExample');
    m.sourceKind=m.sourceKind===kind?'':kind;
    host.innerHTML=evoStorySourceExampleHTML(evoStoryCurrentIssues(m,m.proposal),m.sourceKind);
    host.hidden=!m.sourceKind;
    $$('[data-evo-source-example]').forEach(button=>button.setAttribute('aria-expanded',String(button.dataset.evoSourceExample===m.sourceKind)));
    return;
  }
  const openSources=e.target.closest('[data-evo-open-sources]');
  if(openSources){
    const sources=$('#evoSourceDetails');
    if(sources){sources.open=true;sources.querySelector('summary').focus()}
    return;
  }
  const detailTab=e.target.closest('[data-evo-detail-tab]');if(detailTab)return evoSetDetailTab(detailTab.dataset.evoDetailTab);
  if(e.target.closest('[data-evo-list-back]'))return evoBack();
  if(e.target.closest('[data-evo-reload]'))return renderEvolutionView();
  if(e.target.closest('[data-evo-review-regressions]')&&evo.modal){const m=evo.modal,r=m.proposal?.replays?.find(r=>r.outcome==='regressed');if(r){m.compareFilter='regressed';evoRenderComparisonList(m);return evoOpenStoryEvidence({type:'replay',id:r.id},e.target.closest('[data-evo-review-regressions]'))}}
  const evidenceRun=e.target.closest('[data-evo-evidence-run]');if(evidenceRun&&!evidenceRun.disabled)return evoOpenStoryEvidence({type:'run',id:evidenceRun.dataset.evoEvidenceRun},evidenceRun);
  const evidenceReplay=e.target.closest('[data-evo-evidence-replay]');if(evidenceReplay)return evoSelectEvidence({type:'replay',id:evidenceReplay.dataset.evoEvidenceReplay},true,true);
  if(e.target.closest('[data-evo-evidence-retry]')&&evo.modal?.evidence){const m=evo.modal;m.evidenceCache.delete(`${m.evidence.type}:${m.evidence.id}`);return evoSelectEvidence(m.evidence)}
  const sampleFilter=e.target.closest('[data-evo-sample-filter]');if(sampleFilter&&evo.modal){evo.modal.compareFilter=sampleFilter.dataset.evoSampleFilter;evoRenderComparisonList(evo.modal);$(`[data-evo-sample-filter="${evo.modal.compareFilter}"]`)?.focus({preventScroll:true});return}
  const section=e.target.closest('[data-evo-list-section]');
  if(section){
    evo.list.section=section.dataset.evoListSection;evo.list.page=0;evoUpdateWorkspaceTasks();
    $('[data-evo-list-section][aria-pressed="true"]')?.focus({preventScroll:true});return;
  }
  if(e.target.closest('[data-evo-list-reset]')){
    Object.assign(evo.list,{status:'all',sop:'',q:'',page:0});evoUpdateWorkspaceTasks();
    $('[data-evo-task-search]')?.focus();return;
  }
  const unifiedPage=e.target.closest('[data-evo-unified-page]');
  if(unifiedPage&&!unifiedPage.disabled){evo.list.page=Number(unifiedPage.dataset.evoUnifiedPage);evoUpdateHomeResults();$('#evoUnifiedList button')?.focus();return}
  const rediagnose=e.target.closest('[data-evo-rediagnose]');if(rediagnose&&!rediagnose.disabled)return evoDiagnose(rediagnose);
  const open=e.target.closest('[data-evo-open]');if(open)return evoOpenFinding(open.dataset.evoOpen,open.dataset.cluster);
  if(e.target.closest('[data-evo-change-decisions]')){evo.tab='proposals';evo.changes={q:'',status:'decide',page:0};evoRender();$('#evoTab-proposals')?.focus({preventScroll:true});return}
  const changePage=e.target.closest('[data-evo-change-page]');
  if(changePage&&!changePage.disabled){evo.changes.page=Number(changePage.dataset.evoChangePage);evoUpdateChanges();$('#evoChangeList [data-evo-change-page]:not(:disabled)')?.focus({preventScroll:true});return}
  const assetGroup=e.target.closest('[data-evo-asset-group]');if(assetGroup)return evoOpenAssetGroup(assetGroup.dataset.evoAssetGroup);
  const process=e.target.closest('[data-evo-process]');if(process){return evoOpenProposalProcess(process.dataset.evoProcess)}
  if(e.target.closest('[data-evo-detail-refresh]')){const m=evo.modal;if(m?.proposalId){m.evidenceCache.clear();return evoLoadDetailProposal(m)}return;}
  const listPage=e.target.closest('[data-evo-list-page]');
  if(listPage&&!listPage.disabled){evo.page=Number(listPage.dataset.evoListPage);evoUpdateIssueList();$('#evoIssueList [aria-current="page"]')?.focus({preventScroll:true});return}
  if(e.target.closest('[data-evo-clear-selection]')){evo.selected.clear();evoUpdateIssueList();$('#evoIssueList [data-evo-select-page]')?.focus({preventScroll:true});return}
  const batch=e.target.closest('[data-evo-batch-snooze]');if(batch&&!batch.disabled)return evoSnoozeSelected(batch);
  if(scope.id==='kbDrawer'&&!evoDrawerOwned())return;
  if(e.target.id==='evoFindingModal'||e.target.closest('[data-evo-close]'))return evoCloseFinding();
  const record=e.target.closest('[data-evo-record-run]');
  if(record){
    e.preventDefault();
    evoCloseFinding(false);switchView('runs');
    if(record.hasAttribute('data-evo-run-eval'))state.runDetailTab='eval';
    showRunDetail(record.dataset.evoRecordRun);
    return;
  }
  if(e.target.closest('[data-evo-record-retry]'))return evo.modal&&evoLoadFindingRuns(evo.modal);
  const page=e.target.closest('[data-evo-record-page]');
  if(page&&!page.disabled&&evo.modal){
    const direction=page.dataset.evoRecordPage;
    evo.modal.page+=Number(direction);evoRenderFindingRuns();
    ($(`#evoFindingRecords [data-evo-record-page="${direction}"]:not(:disabled)`)||$('#evoFindingRecords [data-evo-record-page]:not(:disabled)'))?.focus({preventScroll:true});
    return;
  }
  const t=e.target.closest('[data-evo-tab],[data-evo-group],[data-evo-status],[data-evo-finding],[data-evo-proposal],[data-evo-asset],[data-evo-level],[data-evo-dismiss],[data-evo-snooze],[data-evo-propose],[data-evo-propose-asset],[data-evo-approve],[data-evo-canary],[data-evo-reject],[data-evo-rollback],[data-evo-version],[data-evo-unpin],[data-evo-stage],[data-evo-replay-nodes]');
  if(!t||t.disabled)return;
  const d=t.dataset;
  if(d.evoTab){evoNavigate(['assets','samples'].includes(d.evoTab)?`#/evolution/settings/${d.evoTab}`:'#/evolution');$(`#evoTab-${d.evoTab}`)?.focus({preventScroll:true});return}
  if(d.evoGroup){evo.groups[d.evoGroup]=t.getAttribute('aria-expanded')!=='true';evoRender();return}
  if(d.evoStatus){evo.filter.status=d.evoStatus;evo.page=0;evo.selected.clear();evoRender();$(`[data-evo-status="${CSS.escape(d.evoStatus)}"]`)?.focus({preventScroll:true});return}
  if(d.evoFinding)return evoOpenFinding(d.evoFinding,d.evoCluster);
  if(d.evoProposal)return evoOpenProposal(d.evoProposal);
  if(d.evoAsset)return evoOpenAsset(d.evoAsset,d.id);
  if(d.evoLevel!==undefined){if(!t.classList.contains('active'))evoSetLevel(d.kind,d.id,Number(d.evoLevel));return}
  if(d.evoDismiss)return evoDismiss(d.evoDismiss);
  if(d.evoSnooze)return evoSnooze(d.evoSnooze,Number(d.days)||0,t);
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
// 搜索只重绘问题列表，保留输入框焦点与光标。
document.addEventListener('compositionstart',e=>{if(e.target.closest?.('#evolutionView'))evo.composing=true});
document.addEventListener('compositionend',e=>{if(e.target.closest?.('#evolutionView'))evo.composing=false});
document.addEventListener('input',e=>{
  if(e.target.matches('#evoWorkspaceTasks [data-evo-task-search]')){
    evo.list.q=e.target.value;evo.list.page=0;evoUpdateHomeResults();return;
  }
  const changes=e.target.closest?.('#evolutionView [data-evo-change-search]');
  if(changes){evo.changes.q=changes.value;evoUpdateChanges(true);return}
  const input=e.target.closest?.('#evolutionView [data-evo-search]');if(!input)return;
  evo.filter.q=input.value;evoUpdateIssueList(true);
});
document.addEventListener('change',e=>{
  if(e.target.matches('#evoWorkspaceTasks [data-evo-task-filter]')){
    evo.list[e.target.dataset.evoTaskFilter]=e.target.value;evo.list.page=0;evoUpdateHomeResults();return;
  }
  if(e.target.matches('#evoDetailPage [data-evo-evidence-node]')&&evo.modal?.evidenceData){evo.modal.evidenceNode=e.target.value;evoRenderEvidence(evo.modal,evo.modal.evidenceData);$('[data-evo-evidence-node]')?.focus({preventScroll:true});return}
  if(e.target.matches('#evolutionView [data-evo-change-status]')){evo.changes.status=e.target.value;evoUpdateChanges(true);return}
  if(e.target.matches('#evolutionView [data-evo-kind-select], #evolutionView [data-evo-sort], #evolutionView [data-evo-days]')){
    evo.filter[e.target.hasAttribute('data-evo-sort')?'sort':e.target.hasAttribute('data-evo-days')?'days':'kind']=e.target.value;
    evoUpdateIssueList(true);
  }
  if(e.target.matches('#evolutionView [data-evo-select]')){
    const key=e.target.dataset.evoSelect;
    if(e.target.checked)evo.selected.add(key);else evo.selected.delete(key);
    evoUpdateIssueList();$(`[data-evo-select="${CSS.escape(key)}"]`)?.focus({preventScroll:true});
  }
  if(e.target.matches('#evolutionView [data-evo-select-page]')){
    const checked=e.target.checked;
    evoFilteredIssues().slice(evo.page*10,(evo.page+1)*10).forEach(r=>{if(checked)evo.selected.add(evoIssueKey(r));else evo.selected.delete(evoIssueKey(r))});
    evoUpdateIssueList();$('#evoIssueList [data-evo-select-page]')?.focus({preventScroll:true});
  }
  if(e.target.matches('#evoDetailPage [data-evo-record-status]')&&evo.modal){evo.modal.status=e.target.value;evo.modal.page=0;evoRenderFindingRuns()}
});
document.addEventListener('keydown',e=>{
  if(e.key==='Escape'&&e.target.closest?.('.evo-row-menu[open]')){const menu=e.target.closest('.evo-row-menu');menu.open=false;menu.querySelector('summary').focus();e.preventDefault();return}
  const tab=e.target.closest?.('.evo-tabs [role="tab"]');
  if(tab&&['ArrowLeft','ArrowRight','Home','End'].includes(e.key)){
    const tabs=[...tab.parentElement.querySelectorAll('[role="tab"]')],i=tabs.indexOf(tab);
    const next=e.key==='Home'?0:e.key==='End'?tabs.length-1:(i+(e.key==='ArrowRight'?1:-1)+tabs.length)%tabs.length;
    e.preventDefault();tabs[next].click();return;
  }
},true);
(()=>{
  const view=document.getElementById('evolutionView');
  if(view)new MutationObserver(()=>{if(!view.classList.contains('active')){clearTimeout(evo.poll);evoCloseFinding(false);if(evoDrawerOwned())evoDrawerClose()}}).observe(view,{attributes:true,attributeFilter:['class']});
  const drawer=document.getElementById('kbDrawer');
  if(drawer)new MutationObserver(()=>{if(!drawer.classList.contains('open')&&drawer.classList.contains('evo-owned'))drawer.classList.remove('evo-owned','evo-wide')}).observe(drawer,{attributes:true,attributeFilter:['class']});
})();
