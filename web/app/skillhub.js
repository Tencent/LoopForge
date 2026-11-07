/* 技能管理页「SkillHub 市场」scope：列表、详情抽屉、安装 / 更新弹窗。
 * 浏览器只调用平台的 /api/skillhub/*；安装与更新由服务端下载所选版本快照入库，
 * 这里不接触 SkillHub 的 Key 和带签名的下载地址。视觉复用 assets-harness.css 与 Harness 组件类。
 */
const SKILLHUB_PAGE_SIZE=24;
const SKILLHUB_SORTS=[['downloads','按下载量'],['score','按综合评分'],['updated_at','最近更新']];
const SKILLHUB_TRACE=[['trust','可信度','Trust'],['reliability','可靠性','Reliability'],['adaptability','适应性','Adaptability'],['convention','规范性','Convention'],['effectiveness','有效性','Effectiveness']];
const SKILLHUB_TRACE_ITEMS={scan:'安全扫描',domestic:'国内可用',stability:'稳定性',func:'功能',errorHandling:'错误处理',boundary:'边界',trigger:'触发',progressive:'渐进披露',structure:'结构',docQuality:'文档质量',antiPatternFaq:'反模式 / FAQ',accuracy:'准确性',completeness:'完整性',usability:'易用性',creativity:'创造性'};
const SKILLHUB_ID_PATTERN=/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
const SKILLHUB_FILE_PREVIEW_MAX=1024*1024;
const SKILLHUB_PACKAGE_MAX=64*1024*1024;
const SKILLHUB_FILE_COUNT_MAX=500;

function skillhubState(){
  if(!state.skillhub)state.skillhub={keyword:'',category:'',sortBy:'downloads',page:1,seq:0,loading:false,loaded:false,error:'',items:[],total:0,categories:null,categoriesLoading:false,latest:{}};
  return state.skillhub;
}

// ---------- Pure helpers (covered by web/skillhub_market_test.js) ----------
function skillhubScoreValue(v){
  if(v===null||v===undefined||v==='')return null;
  const n=Number(v);return Number.isFinite(n)?n:null;
}
// Mean of the non-null item scores; null when the dimension has none.
function skillhubDimensionScore(dim){
  const scores=Object.values(dim?.items||{}).map(x=>skillhubScoreValue(x?.score)).filter(v=>v!==null);
  return scores.length?scores.reduce((a,b)=>a+b,0)/scores.length:null;
}
// TRACE total: average of the dimension means that have scores, one decimal.
function skillhubTraceScore(dimensions){
  const scores=SKILLHUB_TRACE.map(([key])=>skillhubDimensionScore(dimensions?.[key])).filter(v=>v!==null);
  if(!scores.length)return null;
  return Math.round(scores.reduce((a,b)=>a+b,0)/scores.length*10)/10;
}
function skillhubSourceOf(skill){const s=skill?.source;return s&&s.provider==='skillhub'?s:null}
function skillhubKey(namespace,slug){return `${namespace||''}/${slug||''}`}
// Only skills owned by the current project count as installed; an empty
// namespace on either side matches, the same rule the server enforces.
function skillhubInstalledSkill(item,skills=state.data?.skills){
  if(!item?.slug)return null;
  return (skills||[]).find(s=>{const src=skillhubSourceOf(s);return !!src&&catalogOwned(s.projectId)&&src.slug===item.slug&&(!src.namespace||!item.namespace||src.namespace===item.namespace)})||null;
}
function skillhubCardState(item,skills){
  const skill=skillhubInstalledSkill(item,skills);
  if(!skill)return {state:'none',skill:null,version:''};
  const version=skillhubSourceOf(skill).version||'';
  return {state:item.version&&version!==item.version?'update':'installed',skill,version};
}
function skillhubCount(n){
  n=Number(n)||0;
  if(n>=1e8)return `${+(n/1e8).toFixed(1)} 亿`;
  if(n>=1e4)return `${+(n/1e4).toFixed(1)} 万`;
  return String(n);
}
function skillhubDate(ms){
  let n=Number(ms)||0;if(!n)return '';
  if(n<1e12)n*=1000;
  const d=new Date(n);if(Number.isNaN(d.getTime()))return '';
  return `${d.getFullYear()}-${String(d.getMonth()+1).padStart(2,'0')}-${String(d.getDate()).padStart(2,'0')}`;
}
function skillhubSafeURL(raw){
  try{const u=new URL(String(raw||''));return u.protocol==='https:'||u.protocol==='http:'?u.href:''}catch{return ''}
}
function skillhubLabelBadges(labels){
  const on=v=>String(v??'').toLowerCase()==='true';const out=[];
  if(on(labels?.requires_api_key))out.push(['muted','需要 API Key']);
  if(on(labels?.paid)||on(labels?.is_paid)||String(labels?.pricing||'').toLowerCase()==='paid')out.push(['warn','付费']);
  return out;
}
function skillhubBadge(tone,label,glyph=''){return `<span class="hui-badge" data-tone="${tone}" data-size="sm">${glyph}<span class="hui-badge-label">${escapeHTML(label)}</span></span>`}
function skillhubLamp(){return '<span class="hui-lamp"><i></i></span>'}
function skillhubTile(name){const ch=Array.from(String(name||'?').trim())[0]||'?';return escapeHTML(ch.toUpperCase())}
function skillhubCategoryName(key){return (skillhubState().categories||[]).find(c=>c.key===key)?.name||key||''}
function skillhubSubCategoryName(sub){return sub&&typeof sub==='object'?String(sub.name||sub.key||''):String(sub??'')}
function skillhubProjectName(id){return (state.data?.projects||[]).find(p=>p.id===id)?.name||id||'其他项目'}

function skillhubCardHTML(item,index){
  const st=skillhubCardState(item);
  const labels=skillhubLabelBadges(item.labels).map(([tone,label])=>skillhubBadge(tone,label)).join('');
  const status=st.state==='installed'?skillhubBadge('success',`已安装 v${st.version}`,huiIcon('check'))
    :st.state==='update'?skillhubBadge('warn',`有新版本 v${item.version}`,skillhubLamp())
    :`<span class="asset-status">${escapeHTML(item.ownerName||item.namespace||'')}</span>`;
  const noProject=!state.projectFilter;
  const action=st.state==='update'?`<button type="button" class="button small hui-btn-soft" data-skillhub-update="${index}">${huiIcon('refresh')}更新</button>`
    :st.state==='installed'?`<span class="asset-card-go">详情${huiIcon('chevron-right')}</span>`
    :`<button type="button" class="button small hui-btn-soft" data-skillhub-install="${index}"${noProject?' disabled title="请先选择项目"':''}>${huiIcon('plus')}安装</button>`;
  return `<article class="asset-card skillhub-card" role="link" tabindex="0" data-skillhub-open="${index}" aria-label="查看「${escapeHTML(item.name||item.slug)}」"><div class="asset-card-top"><span class="asset-tile skillhub-tile">${skillhubTile(item.name||item.slug)}</span><div class="asset-card-name"><h3>${escapeHTML(item.name||item.slug)}</h3><code class="asset-mono">${escapeHTML(item.slug)}${item.version?` · v${escapeHTML(item.version)}`:''}</code></div></div><p class="asset-card-desc">${escapeHTML(item.description||'暂无说明')}</p><div class="asset-meta"><span>${huiIcon('download')}${escapeHTML(skillhubCount(item.downloads))}</span><span>${huiIcon('star')}${escapeHTML(skillhubCount(item.stars))}</span>${item.category?`<span>${huiIcon('layers')}${escapeHTML(skillhubCategoryName(item.category))}</span>`:''}${labels}</div><div class="asset-card-foot">${status}${action}</div></article>`;
}
function skillhubSecurityRows(reports){
  return Object.entries(reports||{}).map(([vendor,r])=>({vendor,status:r?.status||'',text:r?.statusText||r?.status||'未知',url:skillhubSafeURL(r?.reportUrl),safe:!!r?.safe})).sort((a,b)=>a.vendor.localeCompare(b.vendor));
}
function skillhubSecurityTone(row){if(row.safe)return 'success';return /malicious|danger|high|unsafe|risk/i.test(row.status)?'error':'warn'}
function skillhubSecurityHTML(reports){
  const rows=skillhubSecurityRows(reports);
  if(!rows.length)return '<p class="skillhub-none">暂无安全认证信息</p>';
  return `<div class="skillhub-panel skillhub-sec-list">${rows.map(r=>`<div class="skillhub-sec-row"><span class="skillhub-sec-who">${escapeHTML(r.vendor)}</span>${skillhubBadge(skillhubSecurityTone(r),r.text,huiIcon(r.safe?'check':'alert'))}${r.url?`<a href="${escapeHTML(r.url)}" target="_blank" rel="noopener noreferrer">查看报告${huiIcon('globe')}</a>`:''}</div>`).join('')}</div>`;
}
function skillhubSecuritySummary(reports){
  const rows=skillhubSecurityRows(reports);if(!rows.length)return skillhubBadge('muted','暂无');
  const bad=rows.filter(r=>!r.safe).length;
  return bad?skillhubBadge('warn',`${bad} 家提示风险`,huiIcon('alert')):skillhubBadge('success',`${rows.length} 家通过`,huiIcon('gate'));
}
function skillhubTraceHTML(evaluation,openKey){
  if(!evaluation?.available)return '<p class="skillhub-none">暂无质量评测</p>';
  const total=skillhubTraceScore(evaluation.dimensions);
  const dims=SKILLHUB_TRACE.map(([key,label,en])=>{
    const dim=evaluation.dimensions?.[key];if(!dim)return '';
    const score=skillhubDimensionScore(dim),open=key===openKey;
    const items=Object.entries(dim.items||{}).map(([k,x])=>{const v=skillhubScoreValue(x?.score);return `<li><span>${escapeHTML(SKILLHUB_TRACE_ITEMS[k]||k)} <small>${escapeHTML(k)}</small></span><b>${v===null?'—':v.toFixed(1)}</b><span class="skillhub-bar is-thin"><i style="width:${v===null?0:Math.max(0,Math.min(100,v/5*100))}%"></i></span>${x?.userReason?`<p>${escapeHTML(x.userReason)}</p>`:''}</li>`}).join('');
    return `<div class="skillhub-tl-sec${open?' is-open':''}" data-trace-dim="${key}"><button type="button" class="skillhub-tl-toggle" aria-expanded="${open}">${huiIcon(score!==null&&score>=4?'check':'alert')}<strong>${label}</strong><span class="skillhub-tl-en">${en}</span><span class="skillhub-bar"><i style="width:${score===null?0:Math.max(0,Math.min(100,score/5*100))}%"></i></span><span class="skillhub-tl-val">${score===null?'—':score.toFixed(1)}</span>${huiIcon('chevron-down')}</button><div class="skillhub-tl-fold"><div>${dim.userReason?`<p class="skillhub-tl-reason">${escapeHTML(dim.userReason)}</p>`:''}${items?`<ul class="skillhub-tl-items">${items}</ul>`:''}</div></div></div>`;
  }).join('');
  return `<div class="skillhub-panel skillhub-tl"><header class="skillhub-tl-head"><span>${huiIcon('flask')}TRACE 质量评测</span><span class="skillhub-score"><b>${total===null?'—':total.toFixed(1)}</b><small>/ 5${evaluation.updatedAt?` · ${escapeHTML(skillhubDate(evaluation.updatedAt))}`:''}</small></span></header>${evaluation.userSummary?`<p class="skillhub-tl-summary">${escapeHTML(evaluation.userSummary)}</p>`:''}${dims}</div>`;
}
function skillhubSplitFrontMatter(text){
  const m=/^---\r?\n([\s\S]*?)\r?\n---\r?\n?/.exec(String(text||''));
  return m?{front:m[1],body:String(text).slice(m[0].length)}:{front:'',body:String(text||'')};
}
function skillhubFileWarnings(files){
  const list=files||[],out=[];
  const big=list.filter(f=>(Number(f.size)||0)>SKILLHUB_FILE_PREVIEW_MAX);
  if(big.length)out.push(`${big.map(f=>f.path).join('、')} 超过平台单文件 1MB 限额，安装会失败并提示原因。`);
  if(list.length>SKILLHUB_FILE_COUNT_MAX)out.push(`技能包含 ${list.length} 个文件，超过单个技能最多 ${SKILLHUB_FILE_COUNT_MAX} 个文件的限额。`);
  const total=list.reduce((s,f)=>s+(Number(f.size)||0),0);
  if(total>SKILLHUB_PACKAGE_MAX)out.push(`技能包共 ${fmtBytes(total)}，超过单个技能 64MB 的限额。`);
  return out;
}
// Client-side hint only; the server re-checks ids across every project.
function skillhubIdProblem(id,{skills=state.data?.skills}={}){
  const v=String(id||'').trim();
  if(!v)return '请填写技能标识';
  if(!SKILLHUB_ID_PATTERN.test(v))return '标识只能包含字母、数字、点、下划线和短横线，且以字母或数字开头';
  const hit=(skills||[]).find(s=>s.id===v);
  if(hit)return `技能标识「${v}」已被${catalogOwned(hit.projectId)?'本项目':`项目「${skillhubProjectName(hit.projectId)}」`}使用，请换一个`;
  return '';
}

// ---------- Market list ----------
function skillhubSyncChrome(){
  const skills=state.assetType==='skills';
  const scope=state[assetScopeKey(state.assetType)];
  const market=skills&&scope==='skillhub'&&!state.selectedAsset;
  $$('[data-asset-scope="skillhub"]').forEach(b=>{b.hidden=!skills});
  const pane=$('#skillhubMarket');if(pane)pane.hidden=!market;
  for(const id of ['#assetCount','#assetList'])if($(id))$(id).hidden=market;
  const search=$('#assetSearchBox');if(search)search.hidden=market;
  if(!market)closeSkillhubDrawer({restoreFocus:false});
  return market;
}
function renderSkillHubMarket(){
  const s=skillhubState();
  bindSkillhubOnce();
  ensureSkillhubCategories();
  const kw=$('#skillhubKeyword');if(kw&&document.activeElement!==kw&&kw.value!==s.keyword)kw.value=s.keyword;
  const sort=$('#skillhubSort');if(sort&&sort.value!==s.sortBy)sort.value=s.sortBy;
  ['#skillhubCategory','#skillhubSort'].forEach(id=>{const el=$(id);if(el)window.enhanceSelect?.(el)});
  if(!s.loaded&&!s.loading&&!s.error)return loadSkillhubMarket();
  renderSkillhubGrid();
}
function renderSkillhubGrid(){
  const s=skillhubState(),grid=$('#skillhubGrid');if(!grid)return;
  const count=$('#skillhubCount');
  const sortName=(SKILLHUB_SORTS.find(x=>x[0]===s.sortBy)||SKILLHUB_SORTS[0])[1];
  const scopeName=s.keyword?`搜索「${s.keyword}」`:(s.category?skillhubCategoryName(s.category):'全部分类');
  if(count)count.innerHTML=s.loading&&!s.loaded?'正在加载 SkillHub 技能…':`${escapeHTML(scopeName)} · ${escapeHTML(sortName)}<b>${escapeHTML((Number(s.total)||0).toLocaleString('zh-CN'))}</b>`;
  grid.setAttribute('aria-busy',String(!!s.loading));
  if(s.loading)grid.innerHTML=Array.from({length:6},()=>'<div class="skillhub-skel" aria-hidden="true"></div>').join('');
  else if(s.error)grid.innerHTML=`<div class="asset-empty"><b>SkillHub 暂时无法访问</b><p>${escapeHTML(s.error)}</p><button type="button" class="button" data-skillhub-retry>${huiIcon('refresh')}重试</button></div>`;
  else if(!s.items.length)grid.innerHTML=`<div class="asset-empty"><b>${s.keyword?`没有找到与「${escapeHTML(s.keyword)}」相关的技能`:'该分类下暂无技能'}</b><p>换个关键词或分类试试。</p></div>`;
  else grid.innerHTML=s.items.map((item,i)=>skillhubCardHTML(item,i)).join('');
  renderSkillhubPager();
}
function skillhubPageList(page,pages){
  const set=new Set([1,pages,page-1,page,page+1].filter(p=>p>=1&&p<=pages));
  const sorted=[...set].sort((a,b)=>a-b),out=[];
  sorted.forEach((p,i)=>{if(i&&p-sorted[i-1]>1)out.push('…');out.push(p)});
  return out;
}
function renderSkillhubPager(){
  const s=skillhubState(),host=$('#skillhubPager');if(!host)return;
  const pages=Math.max(1,Math.ceil((Number(s.total)||0)/SKILLHUB_PAGE_SIZE));
  host.hidden=s.loading||!!s.error||!s.items.length||pages<=1;
  if(host.hidden){host.innerHTML='';return}
  const p=Math.min(s.page,pages);
  host.innerHTML=`<span>第 ${p} / ${pages.toLocaleString('zh-CN')} 页</span><div class="skillhub-pages"><button type="button" data-skillhub-page="${p-1}" aria-label="上一页"${p<=1?' disabled':''}>${huiIcon('chevron-left')}</button>${skillhubPageList(p,pages).map(x=>x==='…'?'<button type="button" disabled>…</button>':`<button type="button" data-skillhub-page="${x}"${x===p?' class="active" aria-current="page"':''}>${x}</button>`).join('')}<button type="button" data-skillhub-page="${p+1}" aria-label="下一页"${p>=pages?' disabled':''}>${huiIcon('chevron-right')}</button></div>`;
}
async function loadSkillhubMarket(){
  const s=skillhubState(),seq=++s.seq;
  s.loading=true;s.error='';renderSkillhubGrid();
  const q=new URLSearchParams({sortBy:s.sortBy,page:String(s.page),pageSize:String(SKILLHUB_PAGE_SIZE)});
  if(s.keyword)q.set('keyword',s.keyword);
  if(s.category)q.set('category',s.category);
  try{
    const body=await api(`/api/skillhub/skills?${q}`,{silent:true});
    if(seq!==s.seq)return;
    s.items=Array.isArray(body.items)?body.items:[];s.total=Number(body.total)||0;s.loaded=true;
    s.items.forEach(item=>{if(item.version)s.latest[skillhubKey(item.namespace,item.slug)]=item.version});
  }catch(err){
    if(seq!==s.seq)return;
    s.error=err.message||'请求失败';s.items=[];s.total=0;
  }
  s.loading=false;renderSkillhubGrid();
}
function skillhubQueryChanged(patch){
  const s=skillhubState();Object.assign(s,patch,{page:1});loadSkillhubMarket();
}
async function ensureSkillhubCategories(){
  const s=skillhubState();if(s.categories||s.categoriesLoading)return;
  s.categoriesLoading=true;
  try{
    const body=await api('/api/skillhub/categories',{silent:true});
    s.categories=(body.items||[]).filter(c=>c&&c.key);
  }catch{s.categories=[]}
  s.categoriesLoading=false;
  const sel=$('#skillhubCategory');
  if(sel)sel.innerHTML=`<option value="">全部分类</option>${s.categories.map(c=>`<option value="${escapeHTML(c.key)}"${c.key===s.category?' selected':''}>${escapeHTML(c.name||c.key)}</option>`).join('')}`;
  if(s.items.length)renderSkillhubGrid();
}
let skillhubBound=false,skillhubKeywordTimer=null;
function bindSkillhubOnce(){
  if(skillhubBound)return;skillhubBound=true;
  const kw=$('#skillhubKeyword');
  if(kw){
    kw.addEventListener('input',()=>{clearTimeout(skillhubKeywordTimer);skillhubKeywordTimer=setTimeout(()=>{const v=kw.value.trim().slice(0,100);if(v!==skillhubState().keyword)skillhubQueryChanged({keyword:v})},300)});
    kw.addEventListener('keydown',e=>{if(e.key!=='Enter')return;e.preventDefault();clearTimeout(skillhubKeywordTimer);skillhubQueryChanged({keyword:kw.value.trim().slice(0,100)})});
  }
  $('#skillhubCategory')?.addEventListener('change',e=>skillhubQueryChanged({category:e.target.value}));
  $('#skillhubSort')?.addEventListener('change',e=>skillhubQueryChanged({sortBy:e.target.value}));
  const market=$('#skillhubMarket');
  if(market){
    market.addEventListener('click',e=>{
      const t=e.target instanceof Element?e.target:null;if(!t)return;
      if(t.closest('[data-skillhub-retry]'))return loadSkillhubMarket();
      const page=t.closest('[data-skillhub-page]');if(page){if(page.disabled)return;skillhubState().page=Number(page.dataset.skillhubPage)||1;loadSkillhubMarket();$('#skillhubMarket')?.scrollIntoView?.({block:'start',behavior:'smooth'});return}
      const items=skillhubState().items;
      const ins=t.closest('[data-skillhub-install]');if(ins){e.stopPropagation();if(!ins.disabled)openSkillhubInstall(items[Number(ins.dataset.skillhubInstall)],ins);return}
      const up=t.closest('[data-skillhub-update]');if(up){e.stopPropagation();const item=items[Number(up.dataset.skillhubUpdate)];const skill=skillhubInstalledSkill(item);if(skill)openSkillhubUpdate(skill,{latest:item.version,opener:up});return}
      const card=t.closest('[data-skillhub-open]');if(card)openSkillhubDrawer(items[Number(card.dataset.skillhubOpen)],card);
    });
    market.addEventListener('keydown',e=>{const card=e.target instanceof Element?e.target.closest('[data-skillhub-open]'):null;if(card&&e.key==='Enter'&&e.target===card){e.preventDefault();openSkillhubDrawer(skillhubState().items[Number(card.dataset.skillhubOpen)],card)}});
  }
  $('#skillhubScrim')?.addEventListener('click',()=>closeSkillhubDrawer());
  $('#skillhubDrawer')?.addEventListener('click',onSkillhubDrawerClick);
  const modal=$('#skillhubModal');
  if(modal){
    modal.addEventListener('pointerdown',e=>{if(e.target===modal)closeSkillhubModal()});
    modal.addEventListener('click',onSkillhubModalClick);
    modal.addEventListener('input',e=>{if(e.target?.id==='skillhubModalId')validateSkillhubModal()});
    modal.addEventListener('submit',e=>{e.preventDefault();submitSkillhubModal()});
  }
  document.addEventListener('keydown',e=>{
    if(e.key!=='Escape'||e.defaultPrevented)return;
    if(state.skillhubModal){e.preventDefault();closeSkillhubModal();return}
    if(state.skillhubDrawer){e.preventDefault();closeSkillhubDrawer()}
  });
}

// ---------- Detail drawer ----------
async function openSkillhubDrawer(item,opener){
  if(!item?.slug)return;
  bindSkillhubOnce();
  const d=state.skillhubDrawer={item,slug:item.slug,namespace:item.namespace||'',version:item.version||'',tab:'overview',detail:null,versions:null,evaluation:null,files:null,filesFor:'',filePath:'',fileData:null,fileMode:'preview',skillMd:null,traceOpen:'trust',errors:{},seq:0,opener:opener||document.activeElement};
  const drawer=$('#skillhubDrawer'),scrim=$('#skillhubScrim');
  if(drawer){drawer.inert=false;drawer.classList.add('is-open');drawer.setAttribute('aria-hidden','false')}
  if(scrim)scrim.classList.add('is-open');
  renderSkillhubDrawer();
  drawer?.querySelector('[data-skillhub-close]')?.focus();
  const scope=`?namespace=${encodeURIComponent(d.namespace)}`;
  const base=`/api/skillhub/skills/${encodeURIComponent(d.slug)}`;
  const load=async(key,path)=>{try{const body=await api(base+path,{silent:true});if(state.skillhubDrawer!==d)return;d[key]=body}catch(err){if(state.skillhubDrawer!==d)return;d.errors[key]=err.message||'加载失败'}renderSkillhubDrawer()};
  await Promise.all([
    load('detail',scope).then(()=>{const v=d.detail?.latestVersion?.version;if(v){skillhubState().latest[skillhubKey(d.namespace||d.detail?.skill?.namespace,d.slug)]=v;if(!d.version){d.version=v;loadSkillhubFiles(d)}}}),
    load('versions',`/versions${scope}`),
    load('evaluation',`/evaluation${scope}`),
    d.version?loadSkillhubFiles(d):Promise.resolve(),
  ]);
}
async function loadSkillhubFiles(d){
  const version=d.version;if(!version||d.filesFor===version)return;
  d.filesFor=version;d.files=null;d.filePath='';d.fileData=null;d.skillMd=null;delete d.errors.files;
  renderSkillhubDrawer();
  try{
    const body=await api(`/api/skillhub/skills/${encodeURIComponent(d.slug)}/files?namespace=${encodeURIComponent(d.namespace)}&version=${encodeURIComponent(version)}`,{silent:true});
    if(state.skillhubDrawer!==d||d.filesFor!==version)return;
    d.files=(body.files||[]).slice().sort((a,b)=>a.path.localeCompare(b.path));
  }catch(err){if(state.skillhubDrawer!==d||d.filesFor!==version)return;d.errors.files=err.message||'加载失败';d.files=[]}
  renderSkillhubDrawer();
  const md=(d.files||[]).find(f=>f.path==='SKILL.md')||(d.files||[]).find(f=>/(^|\/)SKILL\.md$/i.test(f.path));
  if(md)await openSkillhubFile(d,md.path);
}
async function openSkillhubFile(d,path){
  const ref=(d.files||[]).find(f=>f.path===path);if(!ref)return;
  const version=d.version;
  d.filePath=path;d.fileMode=/\.(md|markdown)$/i.test(path)?'preview':'source';
  if((Number(ref.size)||0)>SKILLHUB_FILE_PREVIEW_MAX){d.fileData={path,previewable:false,reason:'文件超过 1MB，不支持预览'};renderSkillhubDrawer();return}
  d.fileData={path,loading:true};renderSkillhubDrawer();
  let data;
  try{data=await api(`/api/skillhub/skills/${encodeURIComponent(d.slug)}/file?namespace=${encodeURIComponent(d.namespace)}&version=${encodeURIComponent(version)}&path=${encodeURIComponent(path)}`,{silent:true})}
  catch(err){data={path,previewable:false,reason:err.message||'加载失败'}}
  if(state.skillhubDrawer!==d||d.version!==version)return;
  if(/(^|\/)SKILL\.md$/i.test(path)&&data.previewable)d.skillMd=data;
  if(d.filePath===path)d.fileData=data;
  renderSkillhubDrawer();
}
function closeSkillhubDrawer({restoreFocus=true}={}){
  const d=state.skillhubDrawer;if(!d)return;
  state.skillhubDrawer=null;
  const drawer=$('#skillhubDrawer');
  if(drawer){drawer.classList.remove('is-open');drawer.inert=true;drawer.setAttribute('aria-hidden','true')}
  $('#skillhubScrim')?.classList.remove('is-open');
  if(restoreFocus&&d.opener?.isConnected)d.opener.focus?.();
}
function skillhubDrawerView(d){
  const detail=d.detail?.skill||{},item=d.item;
  const ns=d.namespace||detail.namespace||'';
  const latest=d.detail?.latestVersion?.version||item.version||'';
  return {
    name:detail.name||item.name||d.slug,slug:d.slug,namespace:ns,latest,
    summary:detail.summary||item.description||'',owner:d.detail?.owner?.displayName||item.ownerName||ns,
    category:detail.category||item.category||'',homepage:skillhubSafeURL(detail.homepage||item.homepage),
    downloads:detail.downloads??item.downloads,stars:detail.stars??item.stars,labels:{...(item.labels||{}),...(detail.labels||{})},subCategories:detail.subCategories||[],
    versions:d.versions?.versions||(d.detail?.latestVersion?[d.detail.latestVersion]:[]),
  };
}
function skillhubDrawerActionHTML(view,st){
  if(st.state==='none'){
    const noProject=!state.projectFilter;
    return `<button type="button" class="button primary" data-skillhub-drawer-install${noProject?' disabled title="请先选择项目"':''}>${huiIcon('plus')}安装到本项目</button>`;
  }
  const go=`<button type="button" class="button" data-skillhub-goto="${escapeHTML(st.skill.id)}">查看技能${huiIcon('chevron-right')}</button>`;
  if(view.latest&&st.version!==view.latest)return `${skillhubBadge('warn',`有新版本 v${view.latest}`,skillhubLamp())}${go}<button type="button" class="button primary" data-skillhub-drawer-update>${huiIcon('refresh')}更新</button>`;
  return `${skillhubBadge('success','已是最新版本',huiIcon('check'))}${go}`;
}
function skillhubOverviewHTML(d,view){
  const trace=d.evaluation?.available?skillhubTraceScore(d.evaluation.dimensions):null;
  const versionRow=view.versions.find(v=>v.version===d.version);
  const reports=versionRow?.securityReports||(d.version===view.latest?d.detail?.securityReports:null);
  const md=d.skillMd?.content;
  let mdHTML;
  if(md){const {body}=skillhubSplitFrontMatter(md);mdHTML=`<div class="skillhub-panel skillhub-md markdown-body">${renderMarkdown(body)}</div>`}
  else if(d.files&&!d.files.some(f=>/(^|\/)SKILL\.md$/i.test(f.path)))mdHTML='<p class="skillhub-none">该版本没有 SKILL.md</p>';
  else mdHTML='<div class="skillhub-loading">正在加载 SKILL.md…</div>';
  const mdRef=(d.files||[]).find(f=>f.path===d.skillMd?.path);
  const tags=[view.category?skillhubCategoryName(view.category):'',...view.subCategories.map(skillhubSubCategoryName),...skillhubLabelBadges(view.labels).map(x=>x[1])].filter(Boolean);
  return `<div class="skillhub-sec"><p class="skillhub-lead">${escapeHTML(view.summary||'暂无简介')}</p></div>
<div class="skillhub-sec skillhub-panel skillhub-stats"><div><small>TRACE 总分</small><b class="is-accent">${trace===null?'—':trace.toFixed(1)}<em>/ 5</em></b></div><div><small>安全认证</small><b>${d.versions||d.detail?skillhubSecuritySummary(reports):'—'}</b></div><div><small>下载</small><b>${escapeHTML(skillhubCount(view.downloads))}</b></div><div><small>收藏</small><b>${escapeHTML(skillhubCount(view.stars))}</b></div></div>
<div class="skillhub-sec"><div class="skillhub-sec-title">${huiIcon('doc')}SKILL.md<span>${d.version?`v${escapeHTML(d.version)}`:''}${mdRef?` · ${escapeHTML(fmtBytes(mdRef.size))}`:''}</span></div>${mdHTML}</div>
${tags.length?`<div class="skillhub-sec"><div class="skillhub-sec-title">${huiIcon('tag')}标签</div><div class="skillhub-tags">${tags.map(t=>skillhubBadge('muted',t)).join('')}</div></div>`:''}`;
}
function skillhubFileTreeHTML(files,active){
  const root={dirs:{},files:[]};
  (files||[]).forEach(f=>{const segs=f.path.split('/');let node=root;segs.slice(0,-1).forEach(seg=>{node=node.dirs[seg]||(node.dirs[seg]={dirs:{},files:[]})});node.files.push(f)});
  const out=[];
  (function walk(node,depth){
    node.files.forEach(f=>{const big=(Number(f.size)||0)>SKILLHUB_FILE_PREVIEW_MAX;out.push(`<button type="button" class="skillhub-frow${f.path===active?' is-active':''}${big?' is-off':''}" style="padding-left:${8+depth*16}px" data-skillhub-file="${escapeHTML(f.path)}" title="${escapeHTML(big?'超过 1MB，不支持预览':f.path)}">${huiIcon('doc')}<span>${escapeHTML(f.path.split('/').pop())}</span><small>${escapeHTML(fmtBytes(f.size))}</small></button>`)});
    Object.keys(node.dirs).sort((a,b)=>a.localeCompare(b)).forEach(name=>{out.push(`<div class="skillhub-frow is-dir" style="padding-left:${8+depth*16}px">${huiIcon('folder')}<span>${escapeHTML(name)}</span></div>`);walk(node.dirs[name],depth+1)});
  })(root,0);
  return out.join('');
}
function skillhubFilesHTML(d){
  if(d.errors.files)return `<p class="skillhub-none">文件列表加载失败：${escapeHTML(d.errors.files)}</p>`;
  if(!d.files)return '<div class="skillhub-loading">正在加载文件…</div>';
  if(!d.files.length)return '<p class="skillhub-none">该版本没有文件</p>';
  const f=d.fileData;let view;
  if(!f)view='<p class="skillhub-none">选择左侧文件查看内容</p>';
  else if(f.loading)view='<div class="skillhub-loading">正在加载文件…</div>';
  else if(!f.previewable)view=`<p class="skillhub-none">${escapeHTML(f.reason||'不支持预览')}</p>`;
  else{
    const isMd=/\.(md|markdown)$/i.test(f.path);
    if(isMd&&d.fileMode==='preview'){const {front,body}=skillhubSplitFrontMatter(f.content);view=`<div class="skillhub-md markdown-body">${front?`<pre class="skillhub-front">${escapeHTML(front)}</pre>`:''}${renderMarkdown(body)}</div>`}
    else view=`<pre class="skillhub-code">${highlightCode(f.content||'',skillFileLang(f.path))}</pre>`;
  }
  const isMd=/\.(md|markdown)$/i.test(d.filePath||'');
  const warn=skillhubFileWarnings(d.files).map(w=>`<div class="skillhub-callout">${huiIcon('alert')}${escapeHTML(w)}</div>`).join('');
  return `<div class="skillhub-panel skillhub-files"><div class="skillhub-ftree">${skillhubFileTreeHTML(d.files,d.filePath)}</div><div class="skillhub-fview"><div class="skillhub-fbar">${huiIcon('doc')}<code>${escapeHTML(d.filePath||'')}</code><span class="skillhub-sp"></span>${isMd&&f?.previewable?`<div class="skillhub-seg"><button type="button" data-skillhub-mode="preview" class="${d.fileMode==='preview'?'is-on':''}">预览</button><button type="button" data-skillhub-mode="source" class="${d.fileMode==='source'?'is-on':''}">源码</button></div>`:''}</div><div class="skillhub-fbody">${view}</div></div></div>${warn}`;
}
function skillhubQualityHTML(d,view){
  const versionRow=view.versions.find(v=>v.version===d.version);
  const reports=versionRow?.securityReports||(d.version===view.latest?d.detail?.securityReports:null);
  const trace=d.errors.evaluation?`<p class="skillhub-none">质量评测加载失败：${escapeHTML(d.errors.evaluation)}</p>`:d.evaluation?skillhubTraceHTML(d.evaluation,d.traceOpen):'<div class="skillhub-loading">正在加载质量评测…</div>';
  const security=d.errors.versions&&!d.detail?`<p class="skillhub-none">安全认证加载失败：${escapeHTML(d.errors.versions)}</p>`:d.versions||d.detail?skillhubSecurityHTML(reports):'<div class="skillhub-loading">正在加载安全认证…</div>';
  return `<div class="skillhub-sec">${trace}</div><div class="skillhub-sec"><div class="skillhub-sec-title">${huiIcon('gate')}安全认证<span>${d.version?`v${escapeHTML(d.version)}`:''}</span></div>${security}</div>`;
}
function skillhubVersionsHTML(d,view,installedVersion){
  if(d.errors.versions)return `<p class="skillhub-none">版本列表加载失败：${escapeHTML(d.errors.versions)}</p>`;
  if(!d.versions)return '<div class="skillhub-loading">正在加载版本…</div>';
  if(!view.versions.length)return '<p class="skillhub-none">暂无版本信息</p>';
  return `<div class="skillhub-panel skillhub-versions">${view.versions.map(v=>{const latest=v.version===view.latest;const marks=(v.version===installedVersion?skillhubBadge('muted','本项目已安装'):'')+(latest?skillhubBadge('accent','最新'):'');return `<button type="button" class="skillhub-vrow${latest?' is-latest':''}${v.version===d.version?' is-current':''}" data-skillhub-version="${escapeHTML(v.version)}"><span class="skillhub-dot"></span><span class="skillhub-v">${escapeHTML(v.version)}</span><span class="skillhub-c">${escapeHTML(v.changelog||'无更新说明')}<small>${escapeHTML(skillhubDate(v.createdAt))}</small></span><span class="skillhub-marks">${marks}</span></button>`}).join('')}</div>`;
}
function renderSkillhubDrawer(){
  const d=state.skillhubDrawer,host=$('#skillhubDrawer');if(!d||!host)return;
  const view=skillhubDrawerView(d);
  const st=skillhubCardState({slug:d.slug,namespace:view.namespace,version:view.latest});
  if(d.errors.detail&&!d.detail&&!d.item.name){host.innerHTML=`<div class="skillhub-dhead"><div class="skillhub-drow"><div class="skillhub-dtitle"><h2 id="skillhubDrawerTitle">${escapeHTML(d.slug)}</h2></div><button type="button" class="hui-icon-btn" data-skillhub-close aria-label="关闭">${huiIcon('close')}</button></div></div><div class="skillhub-dbody"><p class="skillhub-none">${escapeHTML(d.errors.detail)}</p></div>`;return}
  const body=host.querySelector('.skillhub-dbody'),scroll=body?body.scrollTop:0;
  const versionOpts=(view.versions.length?view.versions:[{version:d.version}]).filter(v=>v.version).map(v=>`<option value="${escapeHTML(v.version)}"${v.version===d.version?' selected':''}>v${escapeHTML(v.version)}${v.version===view.latest?' · 最新':''}</option>`).join('');
  const tabs=[['overview','概览',''],['files','文件',d.files?String(d.files.length):''],['quality','质量与安全',''],['versions','版本',d.versions?String(view.versions.length):'']];
  const installedBadge=st.state==='none'?'':st.state==='installed'?skillhubBadge('success','本项目已安装',huiIcon('check')):skillhubBadge('accent',`本项目 v${st.version}`);
  const panel=d.tab==='files'?skillhubFilesHTML(d):d.tab==='quality'?skillhubQualityHTML(d,view):d.tab==='versions'?skillhubVersionsHTML(d,view,st.version):skillhubOverviewHTML(d,view);
  host.innerHTML=`<div class="skillhub-dhead"><div class="skillhub-drow"><span class="asset-tile skillhub-tile is-large">${skillhubTile(view.name)}</span><div class="skillhub-dtitle"><h2 id="skillhubDrawerTitle"><span>${escapeHTML(view.name)}</span>${installedBadge}</h2><div class="skillhub-dmeta"><code>${escapeHTML(view.namespace?`${view.namespace}/${view.slug}`:view.slug)}</code>${view.owner?`<span>作者 <b>${escapeHTML(view.owner)}</b></span>`:''}${view.category?`<span>${escapeHTML(skillhubCategoryName(view.category))}</span>`:''}${view.homepage?`<a href="${escapeHTML(view.homepage)}" target="_blank" rel="noopener noreferrer">在 SkillHub 打开${huiIcon('globe')}</a>`:''}</div></div><button type="button" class="hui-icon-btn" data-skillhub-close aria-label="关闭">${huiIcon('close')}</button></div><div class="skillhub-dacts"><div class="skillhub-ghost-select"><select id="skillhubDrawerVersion" data-icon="history" aria-label="查看的版本">${versionOpts}</select></div><span class="skillhub-sp"></span>${skillhubDrawerActionHTML(view,st)}</div></div><div class="asset-pane-tabs skillhub-dtabs" role="tablist" aria-label="技能详情">${tabs.map(([k,label,n])=>`<button type="button" role="tab" data-skillhub-tab="${k}" class="${d.tab===k?'active':''}" aria-selected="${d.tab===k}">${label}${n?`<span class="sliding-tab-count">${n}</span>`:''}</button>`).join('')}</div><div class="skillhub-dbody" role="tabpanel">${panel}</div>`;
  const sel=host.querySelector('#skillhubDrawerVersion');
  if(sel){window.enhanceSelect?.(sel);sel.addEventListener('change',()=>{d.version=sel.value;loadSkillhubFiles(d);renderSkillhubDrawer()})}
  const nextBody=host.querySelector('.skillhub-dbody');if(nextBody&&scroll)nextBody.scrollTop=scroll;
}
function onSkillhubDrawerClick(e){
  const d=state.skillhubDrawer,t=e.target instanceof Element?e.target:null;if(!d||!t)return;
  if(t.closest('[data-skillhub-close]'))return closeSkillhubDrawer();
  const tab=t.closest('[data-skillhub-tab]');if(tab){d.tab=tab.dataset.skillhubTab;const body=$('#skillhubDrawer .skillhub-dbody');if(body)body.scrollTop=0;renderSkillhubDrawer();return}
  const file=t.closest('[data-skillhub-file]');if(file){openSkillhubFile(d,file.dataset.skillhubFile);return}
  const mode=t.closest('[data-skillhub-mode]');if(mode){d.fileMode=mode.dataset.skillhubMode;renderSkillhubDrawer();return}
  const dim=t.closest('.skillhub-tl-toggle');if(dim){const key=dim.closest('[data-trace-dim]')?.dataset.traceDim;d.traceOpen=d.traceOpen===key?'':key;const sec=dim.closest('.skillhub-tl-sec');$$('#skillhubDrawer .skillhub-tl-sec').forEach(s=>{const on=s===sec&&!!d.traceOpen;s.classList.toggle('is-open',on);s.querySelector('.skillhub-tl-toggle')?.setAttribute('aria-expanded',String(on))});return}
  const ver=t.closest('[data-skillhub-version]');if(ver){d.version=ver.dataset.skillhubVersion;loadSkillhubFiles(d);renderSkillhubDrawer();return}
  const view=skillhubDrawerView(d);
  if(t.closest('[data-skillhub-drawer-install]'))return openSkillhubInstall({...d.item,name:view.name,namespace:view.namespace,version:view.latest},t.closest('button'),{version:d.version,versions:view.versions});
  if(t.closest('[data-skillhub-drawer-update]')){const skill=skillhubInstalledSkill({slug:d.slug,namespace:view.namespace});if(skill)openSkillhubUpdate(skill,{latest:view.latest,versions:view.versions,version:d.version,opener:t.closest('button')});return}
  const go=t.closest('[data-skillhub-goto]');if(go){const id=go.dataset.skillhubGoto;closeSkillhubDrawer({restoreFocus:false});state.skillScope='project';state.assetQuery='';openAssetDetail(id)}
}

// ---------- Install / update dialog ----------
async function skillhubFetchVersions(slug,namespace){
  const body=await api(`/api/skillhub/skills/${encodeURIComponent(slug)}/versions?namespace=${encodeURIComponent(namespace||'')}`,{silent:true});
  return body.versions||[];
}
function openSkillhubInstall(item,opener,{version='',versions=null}={}){
  if(!item?.slug)return;
  if(!state.projectFilter)return toast('请先选择项目','warn');
  bindSkillhubOnce();
  const m=state.skillhubModal={mode:'install',item,slug:item.slug,namespace:item.namespace||'',latest:item.version||'',version:version||item.version||'',versions,id:item.slug,name:item.name||item.slug,busy:false,error:'',opener:opener||document.activeElement};
  renderSkillhubModal();
  if(!versions)skillhubFetchVersions(m.slug,m.namespace).then(list=>{if(state.skillhubModal!==m)return;m.versions=list;if(!m.version)m.version=list[0]?.version||'';renderSkillhubModal()}).catch(err=>{if(state.skillhubModal!==m)return;m.versions=[];m.versionsError=err.message;renderSkillhubModal()});
}
function openSkillhubUpdate(skill,{latest='',versions=null,version='',opener=null}={}){
  const src=skillhubSourceOf(skill);if(!src)return;
  if(!catalogOwned(skill.projectId))return toast('只能更新本项目的技能','warn');
  bindSkillhubOnce();
  const m=state.skillhubModal={mode:'update',skill,slug:src.slug,namespace:src.namespace||'',installed:src.version,latest,version:version&&version!==src.version?version:latest,versions,busy:false,error:'',opener:opener||document.activeElement};
  renderSkillhubModal();
  if(!versions)skillhubFetchVersions(m.slug,m.namespace).then(list=>{if(state.skillhubModal!==m)return;m.versions=list;if(!m.latest)m.latest=list[0]?.version||'';if(!m.version)m.version=m.latest;renderSkillhubModal()}).catch(err=>{if(state.skillhubModal!==m)return;m.versions=[];m.versionsError=err.message;renderSkillhubModal()});
}
function renderSkillhubModal(){
  const m=state.skillhubModal,host=$('#skillhubModal');if(!m||!host)return;
  const list=m.versions||[];
  const versions=list.length?list:(m.version?[{version:m.version}]:[]);
  const latest=m.latest||list[0]?.version||'';
  const opts=versions.map(v=>`<option value="${escapeHTML(v.version)}"${v.version===m.version?' selected':''}>v${escapeHTML(v.version)}${v.version===latest?' · 最新':''}${m.mode==='update'&&v.version===m.installed?' · 当前':''}</option>`).join('');
  const project=skillhubProjectName(state.projectFilter);
  const name=m.mode==='install'?(m.item.name||m.slug):(m.skill.name||m.skill.id);
  const changelog=list.find(v=>v.version===m.version)?.changelog;
  const fields=m.mode==='install'
    ?`<div class="skillhub-field"><label for="skillhubModalId">技能标识</label><input id="skillhubModalId" class="asset-input is-mono" value="${escapeHTML(m.id)}" autocomplete="off" spellcheck="false" required><div class="skillhub-field-err" data-skillhub-id-err hidden>${huiIcon('alert')}<span></span></div><div class="skillhub-field-help" data-skillhub-id-help>运行时目录名，全平台唯一。</div></div><div class="skillhub-field"><label for="skillhubModalName">名称</label><input id="skillhubModalName" class="asset-input" value="${escapeHTML(m.name)}" maxlength="100" autocomplete="off"></div>`
    :`<div class="skillhub-field"><label>更新内容</label><div class="skillhub-changelog">${escapeHTML(changelog||(m.versions?'无更新说明':'正在加载…'))}<small>更新会整批替换文件；标识、名称、角色绑定和共享状态不变。</small></div></div>`;
  const ok=m.mode==='install'?`${huiIcon('plus')}安装`:`${huiIcon('refresh')}更新`;
  host.innerHTML=`<form class="skillhub-mcard" novalidate><h3 id="skillhubModalTitle">${m.mode==='install'?`安装「${escapeHTML(name)}」到本项目`:`更新「${escapeHTML(name)}」`}</h3><p class="skillhub-msub">${m.mode==='install'?`保存所选版本的快照到「${escapeHTML(project)}」，安装后可在「角色定义」里绑定。`:`当前已安装 v${escapeHTML(m.installed||'?')}，选择要更新到的版本。`}</p><div class="skillhub-field"><label for="skillhubModalVersion">版本</label><div class="skillhub-full-select"><select id="skillhubModalVersion" aria-label="版本"${versions.length?'':' disabled'}>${opts||'<option value="">正在加载版本…</option>'}</select></div>${m.versionsError?`<div class="skillhub-field-help">版本列表加载失败，将安装最新版本：${escapeHTML(m.versionsError)}</div>`:''}</div>${fields}${m.error?`<div class="skillhub-callout is-error" role="alert">${huiIcon('alert')}${escapeHTML(m.error)}</div>`:''}<div class="skillhub-mact"><button type="button" class="button" data-skillhub-modal-cancel>取消</button><button type="submit" class="button primary" data-skillhub-modal-ok>${m.busy?(m.mode==='install'?'安装中…':'更新中…'):ok}</button></div></form>`;
  host.hidden=false;host.classList.add('is-open');
  const sel=host.querySelector('#skillhubModalVersion');
  if(sel){window.enhanceSelect?.(sel);sel.addEventListener('change',()=>{m.version=sel.value;if(m.mode==='install'){m.id=$('#skillhubModalId')?.value??m.id;m.name=$('#skillhubModalName')?.value??m.name}renderSkillhubModal()})}
  validateSkillhubModal();
  if(!m.focused){m.focused=true;setTimeout(()=>(m.mode==='install'?$('#skillhubModalId'):host.querySelector('[data-skillhub-modal-ok]'))?.focus(),40)}
}
function validateSkillhubModal(){
  const m=state.skillhubModal,host=$('#skillhubModal');if(!m||!host)return false;
  const okBtn=host.querySelector('[data-skillhub-modal-ok]');
  let problem='';
  if(m.mode==='install'){
    const input=$('#skillhubModalId');m.id=input?.value??m.id;
    problem=skillhubIdProblem(m.id);
    const touched=String(m.id||'').trim()!=='';
    input?.classList.toggle('is-bad',!!problem&&touched);
    const err=host.querySelector('[data-skillhub-id-err]'),help=host.querySelector('[data-skillhub-id-help]');
    if(err){err.hidden=!problem||!touched;const span=err.querySelector('span');if(span)span.textContent=problem}
    if(help)help.hidden=!!problem&&touched;
  }else if(m.version&&m.version===m.installed)problem='已是当前版本';
  if(okBtn)okBtn.disabled=m.busy||!!problem||(m.mode==='update'&&!m.version);
  return !problem;
}
function closeSkillhubModal(){
  const m=state.skillhubModal;if(!m||m.busy)return;
  state.skillhubModal=null;
  const host=$('#skillhubModal');if(host){host.classList.remove('is-open');host.hidden=true;host.innerHTML=''}
  if(m.opener?.isConnected)m.opener.focus?.();
}
function onSkillhubModalClick(e){
  const t=e.target instanceof Element?e.target:null;if(!t)return;
  if(t.closest('[data-skillhub-modal-cancel]'))closeSkillhubModal();
}
function skillhubStoreSkill(skill){
  if(!skill?.id)return;
  const list=state.data.skills||(state.data.skills=[]);
  const i=list.findIndex(x=>x.id===skill.id);
  if(i>=0)list[i]=skill;else list.push(skill);
}
async function submitSkillhubModal(){
  const m=state.skillhubModal;if(!m||m.busy||!validateSkillhubModal())return;
  if(m.mode==='install')m.name=$('#skillhubModalName')?.value??m.name;
  m.busy=true;m.error='';renderSkillhubModal();
  try{
    let saved;
    if(m.mode==='install'){
      saved=await api('/api/skillhub/install',{method:'POST',body:JSON.stringify({slug:m.slug,namespace:m.namespace,version:m.version,projectId:state.projectFilter,id:String(m.id).trim(),name:String(m.name||'').trim()})});
    }else{
      saved=await api('/api/skillhub/update',{method:'POST',body:JSON.stringify({skillId:m.skill.id,version:m.version})});
    }
    skillhubStoreSkill(saved);
    const src=skillhubSourceOf(saved);
    if(src&&m.latest)skillhubState().latest[skillhubKey(src.namespace,src.slug)]=m.latest;
    m.busy=false;closeSkillhubModal();
    if(m.mode==='install')toast(`已安装「${saved.name||saved.id}」，可在「本项目」中查看并绑定到角色`,'ok',4200);
    else toast(`已更新到 v${src?.version||m.version}，角色绑定与共享状态不变`,'ok',4200);
    renderAssets();
    renderSkillhubDrawer();
    if(typeof renderProperties==='function')renderProperties();
  }catch(err){
    m.busy=false;m.error=err.message||'操作失败';
    if(state.skillhubModal===m)renderSkillhubModal();
    toast(m.error,'err',6000);
  }
}

// ---------- Project skill detail: source card + update check ----------
async function checkSkillhubUpdate(skill,btn){
  const src=skillhubSourceOf(skill);if(!src)return;
  if(btn)btn.disabled=true;
  try{
    const body=await api(`/api/skillhub/skills/${encodeURIComponent(src.slug)}?namespace=${encodeURIComponent(src.namespace||'')}`,{silent:true});
    const latest=body.latestVersion?.version||'';
    if(latest)skillhubState().latest[skillhubKey(src.namespace,src.slug)]=latest;
    if(latest&&latest!==src.version)openSkillhubUpdate(skill,{latest,opener:btn});
    else toast(`已是最新版本 v${src.version}`,'ok');
    if(typeof refreshAssetAside==='function'&&state.selectedAsset===skill.id)refreshAssetAside(skill);
  }catch(err){toast(err.message||'检查更新失败','err')}
  finally{if(btn?.isConnected)btn.disabled=false}
}
function skillhubLatestFor(src){return src?skillhubState().latest[skillhubKey(src.namespace,src.slug)]||'':''}
