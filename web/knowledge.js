// 知识中心：本项目的托管知识库、来源、文档、试搜与读原文；角色与审核标签保留。
// 依赖 app.js 的全局 state、api、$、$$、escapeHTML、toast、relativeTime、switchView、showRunDetail、huiIcon、afConfirm。
const kb={tab:'bases',projectId:null,overview:null,overviewError:'',docs:{source:'',items:[],error:'',loading:false,loaded:false},usage:null,candidates:[],submissions:[],subStatus:'todo',draft:null,togglingAgents:new Set(),search:{query:'',sources:[],result:null,loading:false,error:'',seq:0},governance:null,governanceError:'',feedbackSent:new Set(),drawer:{opener:null,openerKey:'',seq:0}};

const KB_TOOLS=[
  ['kb_search','在项目已授权来源里做关键词检索，返回带出处和行号的结果'],
  ['kb_fetch','按 source + path + 行号读取 Markdown 原文'],
  ['kb_list_sources','列出当前项目可用的知识库来源'],
  ['kb_get_source','查看来源状态与文档清单'],
  ['kb_report_issue','标记文档问题；只记录反馈，不修改正文'],
];
const KB_SUB_STATUS={pending:['待审核','warn'],published:['已写回','ok'],rejected:['已驳回',''],retracted:['已撤回','']};
const KB_SEDIMENT_MODE={review:'人工审核',auto:'门禁全过自动发布'};
const KB_SIGNAL_LABEL={run_succeeded:'SOP 运行成功',mr_merged:'合并请求已合并',issue_completed:'需求完成',gate_approved:'人工关卡通过'};
const KB_WRITE_STATUS={pending:['写回中','warn'],branch_pushed:['已推分支','warn'],mr_open:['合并请求待合并','warn'],merged:['已合并','ok'],closed:['合并请求已关闭',''],failed:['写回失败','err']};
function kbWriteStatus(d){return KB_WRITE_STATUS[d?.mrStatus]||null}
function kbSafeHref(url){const u=String(url||'');return /^https?:\/\//i.test(u)?u:''}
function kbWriteInfoHTML(d){
  const parts=[];
  if(d.branch)parts.push(`分支 <code>${escapeHTML(d.branch)}</code>`);
  const href=kbSafeHref(d.mrUrl);
  if(href)parts.push(`<a href="${escapeHTML(href)}" target="_blank" rel="noopener">合并请求</a>`);
  const st=kbWriteStatus(d);
  const note=d.writeNote?`<div class="kb-write-note">${escapeHTML(d.writeNote)}</div>`:'';
  const err=d.writeError&&d.writeError!==d.error?`<div class="kb-write-note kb-err">${escapeHTML(d.writeError)}</div>`:'';
  if(!parts.length&&!note&&!err&&!st)return '';
  return `<div class="kb-write-info">${st?kbBadge(escapeHTML(st[0]),st[1]):''}${parts.join(' · ')}${note}${err}</div>`;
}
const KB_SOURCE_STATUS={ready:['就绪','ok'],pending:['待同步','warn'],syncing:['同步中','warn'],error:['异常','err'],failed:['异常','err'],'':['未同步','warn']};
const KB_DOC_STATUS={draft:['草稿',''],verified:['已验证','ok'],deprecated:['已废弃','warn'],archived:['已归档','']};
const KB_GOVERNANCE_TYPE={stale:['过期','warn'],duplicate:['重复','warn'],dead_link:['死链','err'],missing_owner:['缺负责人','warn']};
const KB_FEEDBACK_KIND={helpful:['有用','ok'],not_helpful:['没用','warn'],issue:['标记问题','warn']};
const KB_SEARCH_ICON='<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true" focusable="false"><circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/></svg>';

function kbProjectId(){return kb.sharedAdmin?'':(state.projectFilter||'')}
function kbAdminScope(){return !kbProjectId()}
function kbSourceWritable(src){const base=kbBases().find(b=>b.id===src?.baseId);return kbAdminScope()?base?.layer==='shared':base?.layer==='project'}
function kbLayerLabel(layer){return layer==='project'?'项目层':layer==='shared'?'公共层':layer||''}
function kbSourceLabel(src){const base=kbBases().find(b=>b.id===src.baseId);return [kbLayerLabel(base?.layer),base?.name,src.name||src.id].filter(Boolean).join(' · ')}
function kbProjectName(){const p=(state.data?.projects||[]).find(x=>x.id===kbProjectId());return p?.name||'全部项目'}
function kbBases(){return kb.overview?.bases||[]}
function kbAllSources(){const out=[];for(const b of kbBases())for(const s of b.sources||[])out.push(s);return out}
function kbSourceById(id){return kbAllSources().find(s=>s.id===id)||null}
function kbSourceName(src){return src?.name||src?.id||''}
function kbAgentName(id){return (state.data?.agents||[]).find(a=>a.id===id)?.name||id||'-'}
function kbAgentWorkflows(id){const names=[];for(const w of state.data?.workflows||[])if((w.nodes||[]).some(n=>n.agentId===id)&&!names.includes(w.name))names.push(w.name);return names}
function kbTodoItems(){
  const fresh=kb.candidates.filter(c=>!c.submissionId).map(c=>({kind:'cand',c}));
  const pending=kb.submissions.filter(x=>x.status==='pending').map(x=>({kind:'sub',x}));
  return [...fresh,...pending];
}

// ---------- 行级辅助：返回 HTML 字符串，调用方负责转义传入内容 ----------
function kbRow(main,side='',opt={}){
  const go=!!opt.go;
  return `<div class="kb-row${opt.cls?` ${opt.cls}`:''}${go?' kb-go':''}"${go?' role="button" tabindex="0"':''}${opt.attrs?` ${opt.attrs}`:''}>${opt.icon?`<span class="kb-row-icon" aria-hidden="true">${huiIcon(opt.icon)}</span>`:''}<div class="kb-row-main">${main}</div>${side}${go?'<span class="kb-chev" aria-hidden="true">›</span>':''}</div>`;
}
// head 为面板头内部 HTML；label 用于 aria-label。
function kbPanel(head,body,opt={}){return `<section class="kb-panel"${opt.label?` aria-label="${escapeHTML(opt.label)}"`:''}${opt.attrs?` ${opt.attrs}`:''}><div class="kb-panel-head${opt.headCls?` ${opt.headCls}`:''}">${head}</div>${body}</section>`}
function kbPanelTitle(title,meta=''){return `<h3>${title}</h3>${meta?`<span class="kb-panel-meta">${meta}</span>`:''}`}
// tone 取 ok|warn|err，空值为中性灰。
const KB_BADGE_TONES={ok:'success',warn:'warn',err:'error'};
function kbBadge(label,tone='',title=''){return `<span class="hui-badge" data-size="sm" data-tone="${KB_BADGE_TONES[tone]||'neutral'}"${title?` title="${escapeHTML(title)}"`:''}><span class="hui-badge-label">${label}</span></span>`}
function kbText(title,desc=''){return `<div class="kb-row-title">${title}</div>${desc?`<div class="kb-row-desc">${desc}</div>`:''}`}
function kbValue(html,cls='',title=''){return `<span class="kb-row-value${cls?` ${cls}`:''}"${title?` title="${escapeHTML(title)}"`:''}>${html}</span>`}
function kbSwitch(on,label,opt={}){return `<button type="button" class="kb-switch" role="switch" aria-checked="${on?'true':'false'}" aria-label="${escapeHTML(label)}"${opt.disabled?' disabled':''}${opt.attrs?` ${opt.attrs}`:''}></button>`}
function kbSeg(items,value,attr,opt={}){
  return `<div class="kb-seg" aria-label="${escapeHTML(opt.label||'')}">${items.map(([k,t,title])=>`<button type="button" data-${attr}="${k}" class="${k===value?'active':''}"${title?` title="${escapeHTML(title)}"`:''}${opt.disabled?' disabled':''}>${t}</button>`).join('')}</div>`;
}
function kbCaption(text,cls=''){return `<div class="kb-cap${cls?` ${cls}`:''}">${text}</div>`}
function kbShortVersion(version){const v=String(version||'');return /^[0-9a-f]{40}$/i.test(v)?v.slice(0,10):(v||'-')}
function kbGitFileURL(sourceId,version,path,start,end){
  const src=kbSourceById(sourceId);
  if(!src||src.kind!=='git'||!src.gitUrl||!path||!/^[0-9a-f]{40}$/i.test(String(version||'')))return '';
  let u;try{u=new URL(src.gitUrl)}catch{return ''}
  if(!/^https?:$/.test(u.protocol)||!u.hostname)return '';
  const repo=(u.pathname||'').replace(/^\/+|\/+$/g,'').replace(/\.git$/i,'');
  if(!repo)return '';
  const host=u.hostname.toLowerCase();
  const rel=String(path).split('/').map(encodeURIComponent).join('/');
  const lo=Number(start)>0?Number(start):0,hi=Number(end)>0?Number(end):lo;
  const ref=lo?`#L${lo}${hi>lo?`-L${hi}`:''}`:'';
  if(host==='github.com'||host==='gitee.com')return `${u.origin}/${repo}/blob/${version}/${rel}${ref}`;
  if(host==='codeberg.org')return `${u.origin}/${repo}/src/commit/${version}/${rel}${ref}`;
  if(host==='gitlab.com'||host.includes('gitlab'))return `${u.origin}/${repo}/-/blob/${version}/${rel}${ref}`;
  if(host==='bitbucket.org')return `${u.origin}/${repo}/src/${version}/${rel}${lo?`#lines-${lo}:${hi||lo}`:''}`;
  return '';
}
function kbCitationText(sourceId,commit,revision,path,start,end){
  const src=kbSourceById(sourceId);
  const version=src?.kind==='hosted'&&Number(revision)>0?String(Number(revision)):String(commit||'');
  const lo=Number(start)>0?Number(start):0,hi=Number(end)>0?Number(end):lo;
  const ref=lo?`#L${lo}${hi>lo?`-L${hi}`:''}`:'';
  return `${sourceId}@${version}:${path}${ref}`;
}
// 出处标识固定一行：超长时省略而不是折断；能拼出仓库网页时给可点链接。
function kbCitationHTML(text,url=''){
  const body=escapeHTML(text);
  const link=url?`<a href="${escapeHTML(url)}" target="_blank" rel="noopener noreferrer" data-kb-cite>${body}<span aria-hidden="true"> ↗</span></a>`:body;
  return `<span class="kb-cite" title="${escapeHTML(text)}">${link}</span>`;
}
function kbGitURLError(raw){
  const value=String(raw||'').trim();
  if(!value)return 'Git 地址必填';
  const normalized=/^[\w.-]+@[^/:]+:.+/.test(value)?`ssh://${value.replace(':','/')}`:value;
  let u;try{u=new URL(normalized)}catch{return 'Git 地址必须是 HTTPS 或 SSH 仓库地址'}
  if(!['https:','ssh:'].includes(u.protocol)||!u.hostname||!u.pathname||u.pathname==='/')return 'Git 地址必须是 HTTPS 或 SSH 仓库地址';
  if(u.password||(u.protocol==='https:'&&u.username)||u.search||u.hash)return 'Git 地址不能包含凭据或查询参数';
  return '';
}
function kbDocPathError(raw){
  const path=String(raw||'').trim();
  if(!path)return '文档路径必填';
  if(!/\.md$/i.test(path))return '路径必须以 .md 结尾';
  if(path.includes('\\'))return '文档路径不能使用反斜杠';
  if(path.startsWith('/')||/^[A-Za-z]:/.test(path))return '文档路径必须是仓库内的相对路径';
  if(path.split('/').some(seg=>!seg||seg==='.'||seg==='..'))return '文档路径不能包含 . 或 .. 路径段';
  return '';
}

// ---------- 通用右侧抽屉 ----------
function kbDrawerIsOpen(){return !!$('#kbDrawer')?.classList.contains('open')}

// sub、body、foot 为 HTML；返回本次打开的序号，异步填充前用它判断抽屉是否已被替换或关闭。
function kbDrawerOpen(title,sub,body,foot='',opt={}){
  const drawer=$('#kbDrawer');
  if(!drawer)return 0;
  const wasOpen=kbDrawerIsOpen();
  if(!wasOpen){
    kb.drawer.opener=opt.opener||document.activeElement;
    kb.drawer.openerKey=opt.openerKey||kb.drawer.opener?.dataset?.kbKey||'';
  }else if(opt.openerKey)kb.drawer.openerKey=opt.openerKey;
  $('#kbDrawerTitle').textContent=title||'';
  $('#kbDrawerSub').innerHTML=sub||'';
  const host=$('#kbDrawerBody');host.innerHTML=body||'';host.scrollTop=0;
  const footer=$('#kbDrawerFoot');footer.hidden=!foot;footer.innerHTML=foot||'';
  drawer.inert=false;drawer.removeAttribute('inert');
  drawer.classList.add('open');drawer.setAttribute('aria-hidden','false');
  $('#kbScrim')?.classList.add('open');
  if(!drawer.contains(document.activeElement))$('#kbDrawerClose').focus({preventScroll:true});
  return ++kb.drawer.seq;
}

function kbDrawerClose(){
  const drawer=$('#kbDrawer');
  if(!drawer||!kbDrawerIsOpen())return;
  kb.drawer.seq++;
  $('#kbCredentialForm')?.reset();
  drawer.classList.remove('open');drawer.setAttribute('aria-hidden','true');drawer.inert=true;drawer.setAttribute('inert','');
  $('#kbScrim')?.classList.remove('open');
  kb.draft=null;
  const {opener,openerKey}=kb.drawer;
  kb.drawer.opener=null;kb.drawer.openerKey='';
  if(!$('#knowledgeView')?.classList.contains('active'))return;
  const target=opener?.isConnected&&opener!==document.body?opener:(openerKey&&$(`#knowledgeBody [data-kb-key="${CSS.escape(openerKey)}"]`))||$('#kbTabs [aria-selected="true"]');
  target?.focus?.();
}

// ---------- 入口与数据 ----------
async function renderKnowledgeView(){
 if(kb.selectedProject!==state.projectFilter){kb.sharedAdmin=false;kb.selectedProject=state.projectFilter}
  const root=$('#knowledgeBody');
  if(!root)return;
  if(kb.projectId!==null&&kb.projectId!==kbProjectId()){
    kbDrawerClose();
    Object.assign(kb.search,{result:null,error:'',sources:[]});
    kb.governance=null;kb.governanceError='';kb.feedbackSent=new Set();
    kb.docs={source:'',items:[],error:'',loading:false,loaded:false};
  }
  kb.projectId=kbProjectId();
  if(!kb.overview&&!kb.overviewError){root.innerHTML='<div class="kb-empty">正在加载知识库…</div>';kbRenderStatus()}
  await kbRefresh();
  kbRender();
}

async function kbRefresh(){
  const pid=encodeURIComponent(kbProjectId());
  const [overview,usage,subs,cands,bindings,governance]=await Promise.all([
    api(`/api/knowledge/bases?projectId=${pid}`,{silent:true}).catch(err=>({error:err.message})),
    api(`/api/knowledge/usage?projectId=${pid}`,{silent:true}).catch(()=>null),
    api(`/api/knowledge/submissions?projectId=${pid}`,{silent:true}).catch(()=>[]),
    api(`/api/knowledge/candidates?projectId=${pid}`,{silent:true}).catch(()=>[]),
    kbProjectId()?api(`/api/knowledge/bases/bindings?projectId=${pid}`,{silent:true}).catch(err=>({error:err.message})):Promise.resolve([]),
    api(`/api/knowledge/governance?projectId=${pid}`,{silent:true}).catch(err=>({error:err.message})),
  ]);
  kb.bindings=Array.isArray(bindings)?bindings:[];kb.bindingsError=bindings?.error||'';
  kb.overview=overview;kb.overviewError=overview?.error||'';
  kb.usage=usage;kb.submissions=subs||[];kb.candidates=cands||[];
  kb.governanceError=governance?.error||'';if(!kb.governanceError)kb.governance=governance;
}

// 返回 [色调, 指标卡短标签, 完整说明]。
function kbConnState(){
  if(kb.overviewError)return ['err','读取失败',`无法读取知识库：${kb.overviewError}`];
  if(!kb.overview)return ['','读取中','正在读取知识库…'];
  return ['ok','已就绪','本项目的托管知识库已就绪，可直接写文档、试搜和读原文'];
}
function kbMetric(label,value,foot,opt={}){
  return `<div class="kb-metric"><span class="kb-metric-label">${label}</span><div class="kb-metric-value${opt.empty?' is-empty':''}"${opt.attrs?` ${opt.attrs}`:''}>${value}</div><span class="kb-metric-foot"${foot?` title="${escapeHTML(foot)}"`:''}>${escapeHTML(foot)}</span></div>`;
}
function kbMetricsHTML(){
  const [tone,label,detail]=kbConnState();
  const sources=kbAllSources();
  const hosted=sources.filter(s=>s.kind==='hosted').length;
  const git=sources.filter(s=>s.kind==='git').length;
  const docs=sources.reduce((n,s)=>n+(Number(s.docCount)||0),0);
  const pending=sources.filter(s=>s.status==='pending'||s.status==='syncing').length;
  const conn=kbMetric('托管库',`<span class="kb-dot${tone?` ${tone}`:''}" aria-hidden="true"></span><span class="kb-status-text">${escapeHTML(label)}</span>`,kb.overviewError?'读取失败':kbProjectName(),{attrs:`id="kbStatus" title="${escapeHTML(detail)}"`});
  const src=kb.overview?kbMetric('来源',String(sources.length),`${hosted} 个托管 · ${git} 个 Git`):kbMetric('来源','—','读取中',{empty:true});
  const doc=kb.overview?kbMetric('文档总数',docs.toLocaleString('en-US'),pending?`${pending} 个来源待同步`:'已索引文档'):kbMetric('文档总数','—','读取中',{empty:true});
  const roles=kb.usage?kb.usage.agents?.length||0:null;
  const todo=kbTodoItems().length;
  const recent=kb.usage?.activity?.length||0;
  const pair=`<button type="button" class="kb-metric-link" data-kb-go="usage" aria-label="已开启角色 ${roles??'—'}，查看角色">${roles??'—'}</button><span class="kb-metric-sep" aria-hidden="true">/</span><button type="button" class="kb-metric-link" data-kb-go="distill" aria-label="待审核 ${todo}，查看审核">${todo}</button>`;
  const agents=kbMetric('已开启角色 / 待审核',pair,recent?`Agent 最近检索 ${recent} 次`:'暂无 Agent 检索');
  return conn+src+doc+agents;
}

function kbGoTab(tab){
  if(kb.tab!==tab){kb.tab=tab;kbRender()}
  $(`#kbTab-${tab}`)?.focus({preventScroll:true});
}

function kbRenderStatus(){
  const host=$('#kbMetrics');
  if(host){
    const go=document.activeElement?.closest?.('#kbMetrics [data-kb-go]')?.dataset.kbGo;
    host.innerHTML=kbMetricsHTML();
    host.querySelectorAll('[data-kb-go]').forEach(b=>b.onclick=()=>kbGoTab(b.dataset.kbGo));
    if(go)host.querySelector(`[data-kb-go="${go}"]`)?.focus({preventScroll:true});
  }
}

function kbRender(){
  const root=$('#knowledgeBody');
  if(!root)return;
  kbRenderStatus();
  const tabFocused=!!document.activeElement?.closest?.('#kbTabs');
  const roles=kb.usage?.agents?.length||0;
  const todo=kbTodoItems().length;
  const governanceIssues=Number(kb.governance?.counts?.total)||0;
  const tabs=[['bases','知识库',0],['docs','文档',0],['search','试搜',0],['governance','治理',governanceIssues],['usage','角色',roles],['distill','审核',todo]];
  if(!tabs.some(([k])=>k===kb.tab))kb.tab='bases';
  root.innerHTML=`
    ${(!state.authRequired||isPlatformAdmin())?`<div class="kb-scope"><span>${kbAdminScope()?'公共知识库管理':escapeHTML(kbProjectName())+' · 项目知识库'}</span><button type="button" class="kb-btn small" id="kbSharedManage">${kb.sharedAdmin?'返回项目知识库':'公共知识库管理'}</button></div>`:''}
    <div class="kb-tabs" id="kbTabs" role="tablist" aria-label="知识中心">${tabs.map(([k,t,n])=>`<button type="button" role="tab" id="kbTab-${k}" data-kb-tab="${k}" aria-controls="kbTabBody" aria-selected="${kb.tab===k}" class="${kb.tab===k?'active':''}">${t}${n?`<span class="kb-count">${n}</span>`:''}</button>`).join('')}</div>
    <div id="kbTabBody" role="tabpanel" aria-labelledby="kbTab-${kb.tab}"></div>`;
  const manage=$('#kbSharedManage');if(manage)manage.onclick=async()=>{kb.sharedAdmin=!kb.sharedAdmin;kb.tab='bases';kb.overview=null;kb.docs.loaded=false;kbDrawerClose();await renderKnowledgeView()};
  root.querySelectorAll('[data-kb-tab]').forEach(b=>b.onclick=()=>{
    if(kb.tab===b.dataset.kbTab)return;
    kb.tab=b.dataset.kbTab;kbRender();
    $(`#kbTab-${kb.tab}`)?.focus({preventScroll:true});
  });
  ({bases:kbRenderBases,docs:kbRenderDocs,search:kbRenderSearch,governance:kbRenderGovernance,usage:kbRenderUsage,distill:kbRenderDistill}[kb.tab])($('#kbTabBody'));
  if(tabFocused)$(`#kbTab-${kb.tab}`)?.focus({preventScroll:true});
}

// ---------- 知识库与来源 ----------
function kbRenderBases(body){
  const bases=kbBases();
  const sources=kbAllSources();
  const canManage=kbCanManageSources();
  const writableBases=bases.filter(b=>kbAdminScope()?b.layer==='shared':b.layer==='project');
  const baseOptions=writableBases.map(b=>`<option value="${escapeHTML(b.id)}">${escapeHTML(b.name||b.id)}</option>`).join('');
  const left=`
    ${kbPanel(kbPanelTitle(kbAdminScope()?'公共知识库':'本项目与已加载的公共库',kbAdminScope()?'维护公共来源，供多个项目只读加载':'项目知识优先；公共知识只读'),bases.length?bases.map(b=>{
      const canConfig=canManage&&(kbAdminScope()?b.layer==='shared':b.layer==='project');
      const mode=KB_SEDIMENT_MODE[b.sedimentPublishMode]||KB_SEDIMENT_MODE.review;
      const action=canConfig?`<button type="button" class="kb-btn small" data-kb-sediment="${escapeHTML(b.id)}">沉淀配置</button>`:'';
      return kbRow(kbText(`<span class="mono">${escapeHTML(b.id)}</span>`,`${escapeHTML(b.name||'')} · ${escapeHTML(kbLayerLabel(b.layer))} · ${escapeHTML(mode)}`),`${action}${kbValue(`${(b.sources||[]).length} 个来源`)}`);
    }).join(''):'<div class="kb-empty">还没有知识库</div>',{label:'项目知识库'})}
    ${kbAdminScope()?kbSharedCreateHTML():kbSharedBindingsHTML()}
    ${canManage&&writableBases.length?kbPanel(kbPanelTitle('登记 Git 来源','HTTPS 或 SSH；私有仓库按来源配置服务级凭据'),`
      <form id="kbGitForm" class="kb-src-form">
        <div class="kb-field"><label for="kbGitName">名称</label><input id="kbGitName" class="kb-input" name="name" placeholder="公开样例"></div>
        <div class="kb-field"><label for="kbGitUrl">Git 地址</label><input id="kbGitUrl" class="kb-input mono" name="gitUrl" type="text" inputmode="url" autocomplete="off" spellcheck="false" placeholder="https://github.com/example/repo.git"></div>
        <div class="kb-src-grid">
          <div class="kb-field"><label for="kbGitBranch">分支</label><input id="kbGitBranch" class="kb-input" name="branch" placeholder="main"></div>
          <div class="kb-field"><label for="kbGitPrefix">路径前缀</label><input id="kbGitPrefix" class="kb-input mono" name="pathPrefix" placeholder="docs/"></div>
        </div>
        <div class="kb-field"><label for="kbGitBase">写入知识库</label><select id="kbGitBase" class="kb-input" name="baseId">${baseOptions}</select></div>
        ${kbCredentialFields()}
        <div class="kb-foot"><span class="kb-hint" id="kbGitResult">登记后由后台同步；点右侧「同步」可重新触发</span><button type="submit" class="kb-btn primary">登记来源</button></div>
      </form>`,{label:'登记 Git 来源'}):''}`;
  const right=kbPanel(kbPanelTitle('来源与同步状态','同步状态、篇数与最近错误'),sources.length?sources.map(s=>{
    const [label,tone]=KB_SOURCE_STATUS[s.status]||[s.status||'未同步','warn'];
    const synced=s.lastSyncedAt?` · 最近同步 ${escapeHTML(relativeTime(s.lastSyncedAt))}`:'';
    const meta=s.kind==='git'?`Git · ${escapeHTML(s.branch||'main')} · ${escapeHTML(s.pathPrefix||'仓库根目录')}${synced}`:`托管${synced}`;
    const err=s.lastError?`<div class="kb-row-desc kb-err" title="${escapeHTML(s.lastError)}">同步错误：${escapeHTML(s.lastError)}</div>`:'';
    const actions=s.kind==='git'&&canManage&&kbSourceWritable(s)
      ?`<span class="kb-row-actions"><button type="button" class="kb-btn small" data-kb-credential="${escapeHTML(s.id)}">${s.credentialConfigured?'替换凭据':'配置凭据'}</button><button type="button" class="kb-btn small" data-kb-sync="${escapeHTML(s.id)}">同步</button><button type="button" class="kb-btn quiet small" data-kb-del="${escapeHTML(s.id)}">删除</button></span>`
      :'';
    const name=kbSourceName(s);
    return kbRow(kbText(escapeHTML(name)+(s.credentialConfigured?'<span class="kb-hint"> · 凭据已配置</span>':''),`<span>${escapeHTML(kbSourceLabel(s))}</span> · ${meta} · ${Number(s.docCount)||0} 篇`+err),actions+kbBadge(escapeHTML(label),tone),{cls:'kb-source-row'});
  }).join(''):'<div class="kb-empty">暂无来源</div>',{label:'来源与同步状态'});
  body.innerHTML=`<div class="kb-grid"><div class="kb-source-column">${left}</div>${right}</div>`;
  kbBindSharedControls(body);
  const form=$('#kbGitForm');
  if(form)kbBindCredentialFields(form);
  if(form)form.onsubmit=async e=>{
    e.preventDefault();
    const f=form.elements;
    const out=$('#kbGitResult');
    const invalid=kbGitURLError(f.gitUrl.value);
    if(invalid){out.className='kb-hint kb-err';out.textContent=invalid;f.gitUrl.focus();return}
    const submit=form.querySelector('[type="submit"]');submit.disabled=true;
    out.className='kb-hint';out.textContent='登记中…';
    try{
      await api('/api/knowledge/bases/sources',{method:'POST',body:JSON.stringify({
        projectId:kbProjectId(),baseId:f.baseId.value,name:f.name.value.trim(),
        gitUrl:f.gitUrl.value.trim(),branch:f.branch.value.trim(),pathPrefix:f.pathPrefix.value.trim(),credential:kbCredentialPayload(form),
      })});
      toast('已登记 Git 来源','ok');
      await renderKnowledgeView();
    }catch(err){submit.disabled=false;out.className='kb-hint kb-err';out.textContent=err.message}
  };
  if(form)form.elements.gitUrl.oninput=()=>{const out=$('#kbGitResult');out.className='kb-hint';out.textContent='登记后由后台同步；点右侧「同步」可重新触发'};
  body.querySelectorAll('[data-kb-credential]').forEach(btn=>btn.onclick=()=>kbOpenCredentialEditor(btn.dataset.kbCredential));
  body.querySelectorAll('[data-kb-sync]').forEach(btn=>btn.onclick=async()=>{
    btn.disabled=true;
    try{
      await api('/api/knowledge/bases/sync',{method:'POST',body:JSON.stringify({projectId:kbProjectId(),sourceId:btn.dataset.kbSync})});
      toast('已置为待同步','ok');
      await renderKnowledgeView();
    }catch(err){btn.disabled=false;toast(err.message,'err')}
  });
  body.querySelectorAll('[data-kb-sediment]').forEach(btn=>btn.onclick=()=>kbOpenSedimentConfig(btn.dataset.kbSediment));
  body.querySelectorAll('[data-kb-del]').forEach(btn=>btn.onclick=async()=>{
    const src=kbSourceById(btn.dataset.kbDel);
    if(!src)return;
    if(!(await afConfirm({title:'删除来源',message:`删除来源「${kbSourceName(src)}」以及它已索引的全部文档？`,confirmText:'删除',danger:true})))return;
    btn.disabled=true;
    try{
      await api(`/api/knowledge/bases/sources?projectId=${encodeURIComponent(kbProjectId())}&sourceId=${encodeURIComponent(src.id)}`,{method:'DELETE'});
      toast('已删除来源','ok');
      kb.docs={source:'',items:[],error:'',loading:false,loaded:false};
      kb.search.sources=[];
      await renderKnowledgeView();
    }catch(err){btn.disabled=false;toast(err.message,'err')}
  });
}

function kbCanManageBindings(){
  if(!state.authRequired||isPlatformAdmin())return true;
  const p=(state.data?.projects||[]).find(p=>p.id===kbProjectId());
  const uid=Number(state.currentUser?.id);
  return !!uid&&!!p&&(Number(p.ownerId)===uid||(p.members||[]).some(m=>Number(m.userId)===uid&&String(m.role||'').toLowerCase()==='maintainer'));
}
function kbSharedBindingsHTML(){
  const can=kbCanManageBindings();
  const rows=(kb.bindings||[]).map(b=>kbRow(kbText(escapeHTML(b.name),'公共层 · 加载后对本项目只读'),`<input type="checkbox" aria-label="加载 ${escapeHTML(b.name)}" data-kb-binding="${escapeHTML(b.id)}"${b.loaded?' checked':''}${can?'':' disabled'}>`)).join('');
  return kbPanel(kbPanelTitle('加载公共知识库',can?'勾选后供本项目和 Agent 使用':'由项目 Owner 或维护者选择'),kb.bindingsError?`<div class="kb-empty">${escapeHTML(kb.bindingsError)}</div>`:rows||'<div class="kb-empty">平台还没有公共知识库</div>',{label:'加载公共知识库'});
}
function kbSharedCreateHTML(){
  return kbPanel(kbPanelTitle('新建公共知识库'),`<form id="kbSharedCreateForm" class="kb-src-form"><div class="kb-field"><label for="kbSharedName">名称</label><input id="kbSharedName" class="kb-input" name="name" required maxlength="120" placeholder="团队通用规范"></div><div class="kb-foot"><span class="kb-hint">创建后登记来源或添加托管文档</span><button type="submit" class="kb-btn primary">创建公共库</button></div></form>`,{label:'新建公共知识库'});
}
function kbBindSharedControls(body){
  body.querySelectorAll('[data-kb-binding]').forEach(input=>input.onchange=async()=>{
    const enabled=input.checked;input.disabled=true;
    try{
      await api('/api/knowledge/bases/bindings',{method:'PUT',body:JSON.stringify({projectId:kbProjectId(),baseId:input.dataset.kbBinding,enabled})});
      kb.docs.loaded=false;kb.search.result=null;kb.search.sources=[];
      await renderKnowledgeView();
      body.querySelector(`[data-kb-binding="${CSS.escape(input.dataset.kbBinding)}"]`)?.focus();
    }catch(err){input.checked=!enabled;input.disabled=false;toast(err.message,'err')}
  });
  const form=$('#kbSharedCreateForm');if(form)form.onsubmit=async e=>{
    e.preventDefault();const button=form.querySelector('button');button.disabled=true;
    try{await api('/api/knowledge/bases/shared',{method:'POST',body:JSON.stringify({name:form.elements.name.value.trim()})});toast('已创建公共知识库','ok');await renderKnowledgeView()}
    catch(err){button.disabled=false;toast(err.message,'err')}
  };
}

function kbCanManageSources(){
  if(kbAdminScope())return !state.authRequired||isPlatformAdmin();
  if(!state.authRequired||isPlatformAdmin())return true;
  const project=(state.data?.projects||[]).find(p=>p.id===kbProjectId());
  const userId=Number(state.currentUser?.id);
  return !!project&&!!userId&&(Number(project.ownerId)===userId||(project.members||[]).some(m=>Number(m.userId)===userId&&String(m.role||'').toLowerCase()!=='viewer'));
}
function kbCredentialFields(requiredKind='',prefix='kbCredential'){
  return `<div class="kb-field"><label for="${prefix}Kind">来源凭据</label><select id="${prefix}Kind" class="kb-input" name="credentialKind">${requiredKind?'':'<option value="">公开仓库，无凭据</option>'}<option value="https"${requiredKind==='https'?' selected':''}>HTTPS 用户名与令牌</option><option value="ssh"${requiredKind==='ssh'?' selected':''}>SSH 部署密钥</option></select></div>
    <div data-kb-auth="https" hidden>
      <div class="kb-field"><label for="${prefix}User">用户名</label><input id="${prefix}User" class="kb-input" name="credentialUsername" autocomplete="off"></div>
      <div class="kb-field"><label for="${prefix}Token">令牌</label><input id="${prefix}Token" class="kb-input" name="credentialToken" type="password" autocomplete="new-password"></div>
    </div>
    <div data-kb-auth="ssh" hidden>
      <div class="kb-field"><label for="${prefix}Key">SSH 私钥</label><textarea id="${prefix}Key" class="kb-input mono" name="credentialPrivateKey" rows="4" autocomplete="off" spellcheck="false" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"></textarea></div>
      <div class="kb-field"><label for="${prefix}Hosts">服务器公钥记录（known_hosts）</label><textarea id="${prefix}Hosts" class="kb-input mono" name="credentialKnownHosts" rows="2" spellcheck="false" placeholder="github.com ssh-ed25519 AAAA…"></textarea><span class="kb-hint">向仓库管理员取得并核对服务器公钥记录。</span></div>
    </div>
    <label class="kb-hint" data-kb-auth-grant hidden><input type="checkbox" name="credentialCanWrite"> 此凭据已授予写权限</label>`;
}
function kbBindCredentialFields(form){
  const update=()=>{
    const kind=form.elements.credentialKind.value;
    form.querySelectorAll('[data-kb-auth]').forEach(el=>{el.hidden=el.dataset.kbAuth!==kind;el.querySelectorAll('input,textarea').forEach(input=>input.disabled=el.hidden)});
    form.querySelector('[data-kb-auth-grant]').hidden=!kind;
  };
  form.elements.credentialKind.onchange=update;update();
}
function kbCredentialPayload(form){
  const f=form.elements;
  if(!f.credentialKind.value)return undefined;
  const grant={kind:f.credentialKind.value,canWrite:f.credentialCanWrite.checked};
  return grant.kind==='https'?{...grant,username:f.credentialUsername.value,token:f.credentialToken.value}:{...grant,privateKey:f.credentialPrivateKey.value,knownHosts:f.credentialKnownHosts.value};
}
function kbOpenCredentialEditor(sourceId){
  const source=kbSourceById(sourceId);if(!source)return;
  const kind=source.gitUrl.startsWith('https://')?'https':'ssh';
  kbDrawerOpen(source.credentialConfigured?'替换来源凭据':'配置来源凭据',escapeHTML(kbSourceName(source)),`<form id="kbCredentialForm" class="kb-src-form">${kbCredentialFields(kind,'kbReplaceCredential')}<span id="kbCredentialResult" class="kb-hint">${source.credentialConfigured?'凭据已配置，填写新凭据后替换。':'凭据仅供本来源的后台同步使用。'}</span></form>`,'<button type="button" class="kb-btn primary" id="kbCredentialSave">保存凭据</button>');
  const form=$('#kbCredentialForm');kbBindCredentialFields(form);
  $('#kbCredentialSave').onclick=async e=>{
    const button=e.currentTarget;button.disabled=true;
    try{
      await api('/api/knowledge/bases/sources/credential',{method:'PUT',body:JSON.stringify({projectId:kbProjectId(),sourceId,credential:kbCredentialPayload(form)})});
      kbDrawerClose();toast('凭据已配置，等待后台同步','ok');await renderKnowledgeView();
    }catch(err){button.disabled=false;const result=$('#kbCredentialResult');result.className='kb-hint kb-err';result.textContent=err.message}
  };
}

// ---------- 文档：按来源浏览托管与 Git 已索引文档 ----------
function kbCurrentDocSource(){
  const sources=kbAllSources();
  const fallback=sources.find(s=>s.kind==='hosted')?.id||sources[0]?.id||'';
  return (kb.docs.source&&sources.some(s=>s.id===kb.docs.source))?kb.docs.source:fallback;
}
function kbRenderDocs(body){
  const sources=kbAllSources();
  const cur=kbCurrentDocSource();
  if(kb.docs.source!==cur){kb.docs.source=cur;kb.docs.loaded=false}
  const src=kbSourceById(cur);
  const hosted=src?.kind==='hosted'&&kbSourceWritable(src);
  const options=sources.map(s=>`<option value="${escapeHTML(s.id)}"${s.id===cur?' selected':''}>${escapeHTML(kbSourceLabel(s))}</option>`).join('');
  const head=hosted
    ?`<button type="button" class="kb-btn primary" id="kbNewDoc">新建 / 覆盖文档</button>`
    :`<span class="kb-hint">只读来源</span>`;
  body.innerHTML=kbPanel(
    `${kbPanelTitle('文档','按来源浏览已索引文档；托管来源可新建或覆盖')}<div class="kb-docs-head"><select id="kbDocsSource" class="kb-input" aria-label="来源"${sources.length?'':' disabled'}>${options||'<option value="">暂无来源</option>'}</select>${head}</div>`,
    `<div id="kbDocsList">${kbDocsHTML()}</div>`,{label:'文档'});
  const sel=$('#kbDocsSource');
  if(sel)sel.onchange=()=>{kb.docs.source=sel.value;kb.docs.loaded=false;kbRenderDocs($('#kbTabBody'))};
  const btn=$('#kbNewDoc');
  if(btn)btn.onclick=()=>kbOpenDocEditor(cur);
  if(cur&&!kb.docs.loaded)kbLoadDocs(cur);
  kbBindDocs();
}

function kbDocsHTML(){
  const d=kb.docs;
  const src=kbSourceById(d.source);
  if(d.loading)return '<div class="kb-empty">读取中…</div>';
  if(d.error)return `<div class="kb-empty">读取失败：${escapeHTML(d.error)}</div>`;
  if(!d.items.length)return `<div class="kb-empty">${src?.kind==='git'?'这个来源还没有已索引文档；刚登记时等同步完成后刷新':'这个来源还没有文档，点「新建 / 覆盖文档」写一篇'}</div>`;
  return d.items.map((x,i)=>{
    const hosted=src?.kind==='hosted';
    const version=hosted?`修订 ${Number(x.revision)>0?Number(x.revision):(x.commit||'-')}`:kbShortVersion(x.commit);
    return kbRow(kbText(`<span class="mono">${escapeHTML(x.path)}</span>`,`${escapeHTML(x.title||'')} · ${Number(x.lineCount)||0} 行`),kbValue(version,'kb-source-num'),{go:true,cls:'kb-hit',attrs:`data-kb-key="doc:${i}" data-kb-doc="${i}"`});
  }).join('');
}

function kbUpdateDocs(){const host=$('#kbDocsList');if(host){host.innerHTML=kbDocsHTML();kbBindDocs()}}

function kbBindDocs(){
  $$('#kbDocsList [data-kb-doc]').forEach(el=>el.onclick=()=>{
    const x=kb.docs.items[Number(el.dataset.kbDoc)];
    if(!x)return;
    const start=1,end=Number(x.lineCount)||0;
    kbOpenDocument({source:kb.docs.source,commit:x.commit||'',revision:x.revision||0,path:x.path,title:x.title||x.path,start_line:start,end_line:end});
  });
}

async function kbLoadDocs(source){
  const sid=source||kb.docs.source;
  if(!sid)return;
  kb.docs.source=sid;kb.docs.loading=true;kb.docs.error='';kbUpdateDocs();
  try{
    kb.docs.items=await api(`/api/knowledge/bases/documents?projectId=${encodeURIComponent(kbProjectId())}&source=${encodeURIComponent(sid)}`,{silent:true});
    kb.docs.loaded=true;
  }catch(err){kb.docs.items=[];kb.docs.error=err.message}
  kb.docs.loading=false;
  kbUpdateDocs();
}

function kbOpenDocEditor(source){
  if(!source)return toast('请先选择一个托管来源');
  const body=`
    <form id="kbDocForm" autocomplete="off">
      <div class="kb-field"><label for="kbDocPath">文档路径</label><input id="kbDocPath" class="kb-input mono" name="path" placeholder="guides/xxx.md" value="guides/new.md"></div>
      <div class="kb-field-error" id="kbDocPathError" role="alert" hidden></div>
      <div class="kb-field"><label for="kbDocContent">正文（Markdown）</label><textarea id="kbDocContent" class="kb-input" name="content" placeholder="# 标题&#10;&#10;正文…"></textarea></div>
      <div class="kb-cap">同路径再次保存即覆盖：修订号按这篇文档递增，检索与读原文只看到新正文。不会自动补 frontmatter，你写的第一行就是第 1 行；自己写的 frontmatter 会照常校验。</div>
    </form>`;
  kbDrawerOpen('新建 / 覆盖托管文档',`<span class="mono">${escapeHTML(source)}</span>`,body,'<button type="button" class="kb-btn primary" id="kbDocSave">保存文档</button>');
  const form=$('#kbDocForm');
  form.onsubmit=e=>e.preventDefault();
  const showError=message=>{const el=$('#kbDocPathError');if(!el)return;el.hidden=!message;el.textContent=message||''};
  $('#kbDocSave').onclick=async()=>{
    const path=form.elements.path.value.trim();
    const content=form.elements.content.value;
    const invalid=kbDocPathError(path);
    if(invalid){showError(invalid);form.elements.path.focus();return}
    if(!content.trim()){showError('正文不能为空');form.elements.content.focus();return}
    showError('');
    const btn=$('#kbDocSave');btn.disabled=true;
    try{
      const out=await api('/api/knowledge/bases/documents',{method:'POST',body:JSON.stringify({projectId:kbProjectId(),sourceId:source,path,content})});
      toast(`已保存 · 修订 ${out.commit}`,'ok');
      kbDrawerClose();
      kb.docs.loaded=false;
      await renderKnowledgeView();
    }catch(err){btn.disabled=false;showError(err.message)}
  };
}

// ---------- 角色（保留） ----------
function kbRenderUsage(body){
  const enabledAgents=kb.usage?.agents||[];
  const enabled=new Set(enabledAgents.map(a=>a.id));
  const pid=kbProjectId();
  const candidates=(state.data?.agents||[]).filter(a=>!enabled.has(a.id)&&(!pid||a.projectId===pid)).slice(0,12);
  const rows=[
    ...enabledAgents.map(a=>({id:a.id,name:a.name,shared:a.shared,workflows:a.workflows||[],on:true})),
    ...candidates.map(a=>({id:a.id,name:a.name,shared:a.shared,workflows:kbAgentWorkflows(a.id),on:false})),
  ];
  const list=`
    ${rows.length?rows.map(r=>{
      const busy=kb.togglingAgents.has(r.id);
      const desc=[r.workflows.length?r.workflows.join('、'):'未用于任何流程',r.shared?'共享':''].filter(Boolean).join(' · ');
      return kbRow(kbText(escapeHTML(r.name||r.id),escapeHTML(desc)),kbSwitch(r.on,r.name||r.id,{disabled:busy,attrs:`data-kb-agent="${escapeHTML(r.id)}"${busy?' aria-busy="true"':''}`}),{icon:'robot'});
    }).join(''):'<div class="kb-line-note">当前项目还没有可开启的角色</div>'}
    ${kbRow(kbText('运行时注入的工具与规则'),kbValue(`${KB_TOOLS.length} 个只读工具`),{go:true,icon:'settings',attrs:'data-kb-key="inject" data-kb-inject'})}`;
  body.innerHTML=kbPanel(kbPanelTitle('角色','开启后可只读检索；运行产出的 knowledge.md 在闭环后自动入审核队列'),list,{label:'角色'});
  window.AgentFlowHarnessIcons?.hydrate(body);
  body.querySelectorAll('[data-kb-agent]').forEach(sw=>sw.onclick=()=>kbToggleAgent(sw.dataset.kbAgent,sw.getAttribute('aria-checked')!=='true',sw));
  body.querySelector('[data-kb-inject]').onclick=kbOpenInject;
}

async function kbToggleAgent(id,on,sw){
  if(kb.togglingAgents.has(id))return;
  kb.togglingAgents.add(id);
  const hadFocus=document.activeElement===sw;
  sw.setAttribute('aria-checked',String(on));sw.disabled=true;sw.setAttribute('aria-busy','true');
  const refocus=()=>{if(hadFocus)$(`#knowledgeBody [data-kb-agent="${CSS.escape(id)}"]`)?.focus()};
  try{
    const full=await api(`/api/agents/${encodeURIComponent(id)}`);
    const next={...full,knowledgeBase:on,version:(full.version||0)+1};
    await api('/api/agents',{method:'PUT',body:JSON.stringify(next)});
    const local=(state.data?.agents||[]).find(a=>a.id===id);
    if(local)local.knowledgeBase=on;
    kb.togglingAgents.delete(id);
    toast(on?`已为「${full.name}」开启知识库`:`已关闭「${full.name}」的知识库`,'ok');
    await kbRefresh();kbRender();refocus();
  }catch(err){
    kb.togglingAgents.delete(id);
    toast(err.message,'err');
    kbRender();refocus();
  }
}

function kbOpenInject(){
  const body=`
    ${kbCaption('MCP')}
    ${kbRow(kbText('项目知识库','<span class="mono">agentflow-knowledge</span> · 经平台代理只读访问，限定本项目授权来源'))}
    ${kbCaption('工具')}
    ${KB_TOOLS.map(([n,d])=>kbRow(kbText(`<span class="mono">${n}</span>`,escapeHTML(d)))).join('')}
    ${kbRow(kbText('<span class="mono kb-off">kb_write_document</span>','不对 Agent 开放，写入只能经知识中心的托管文档编辑'))}
    ${kbCaption('说明')}
    <div class="kb-cap">角色开启知识库开关后，运行时注入这 5 个知识库工具（4 个只读检索 + 1 个标记问题）与「知识库使用规范」；节点启动时平台签发限定本项目的令牌。Agent 对正文仍完全只读，标记问题只记录反馈、不改正文；运行到达闭环后，平台把该角色产出的 knowledge.md 自动加入审核队列，审核通过或门禁全过后才写回。</div>`;
  kbDrawerOpen('运行时注入的工具与规则','Agent 只读正文；标记问题只记录反馈',body);
}

// ---------- 沉淀配置 ----------
async function kbOpenSedimentConfig(baseId){
  let config;
  try{config=await api(`/api/knowledge/bases/sediment?projectId=${encodeURIComponent(kbProjectId())}&baseId=${encodeURIComponent(baseId)}`)}
  catch(err){toast(err.message,'err');return}
  const rules=(config.rules||[]).filter(r=>r.enabled!==false).map(r=>`${r.name}=${r.pattern}`).join('\n');
  const body=`
    <p class="kb-lead">闭环信号到达后，运行产出的 <code>knowledge.md</code> 会自动进入审核队列。这里决定通过后是否还要人工点写回，以及哪些内容按敏感信息拦截。</p>
    <form id="kbSedimentForm" autocomplete="off">
      <div class="kb-field"><label for="kbSedimentMode">发布模式</label><select id="kbSedimentMode" class="kb-input" name="mode">
        <option value="review"${config.mode!=='auto'?' selected':''}>人工审核（默认）：门禁通过后等待审核人写回</option>
        <option value="auto"${config.mode==='auto'?' selected':''}>门禁全过自动发布：直接走已有写回</option>
      </select></div>
      <div class="kb-field"><label for="kbSedimentRules">敏感规则</label><textarea id="kbSedimentRules" class="kb-input mono" name="rules" rows="8" placeholder="令牌=(?i)token\s*[:=]\s*\S+">${escapeHTML(rules)}</textarea>
        <div class="kb-cap">每行一条「名称=正则」，留空表示关闭敏感过滤。未配置时使用平台内置的通用规则（令牌、口令、私钥、邮箱、手机号、IPv4、提示词注入）。</div></div>
    </form>`;
  kbDrawerOpen('沉淀配置',escapeHTML(baseId),body,'<button type="button" class="kb-btn primary" id="kbSedimentSave">保存配置</button>');
  $('#kbSedimentSave').onclick=async e=>{
    const form=$('#kbSedimentForm');const btn=e.currentTarget;btn.disabled=true;
    const rules=[];
    for(const line of form.elements.rules.value.split('\n')){
      const text=line.trim();if(!text)continue;
      const cut=text.indexOf('=');if(cut<=0)return toast('敏感规则每行应为「名称=正则」','err');
      rules.push({name:text.slice(0,cut).trim(),pattern:text.slice(cut+1).trim(),enabled:true});
    }
    try{
      await api('/api/knowledge/bases/sediment',{method:'PUT',body:JSON.stringify({projectId:kbProjectId(),baseId, mode:form.elements.mode.value, rules})});
      toast('已保存沉淀配置','ok');kbDrawerClose();await renderKnowledgeView();
    }catch(err){btn.disabled=false;toast(err.message,'err')}
  };
}

// ---------- 审核 ----------
function kbRenderDistill(body){
  const items=kb.subStatus==='todo'?kbTodoItems():kb.submissions.filter(x=>x.status===kb.subStatus).map(x=>({kind:'sub',x}));
  const showState=kb.subStatus==='todo';
  const list=items.length?items.map(it=>{
    if(it.kind==='cand'){
      const a=it.c.artifact||{};
      return kbRow(kbText(escapeHTML(a.path||a.name||'运行产物'),escapeHTML(`运行 ${a.runId||'-'} 产出 · ${relativeTime(a.createdAt)}`)),kbBadge('候选'),{go:true,attrs:`data-kb-key="cand:${escapeHTML(a.id)}" data-kb-cand="${escapeHTML(a.id)}"`});
    }
    const x=it.x;const [label,tone]=KB_SUB_STATUS[x.status]||[x.status||'-',''];
    const write=kbWriteStatus(x);
    const side=showState?kbBadge(escapeHTML(label),tone):(write?kbBadge(escapeHTML(write[0]),write[1]):'');
    const action=x.action==='retract'?` ${kbBadge('下线','warn')}`:'';
    return kbRow(kbText(`${escapeHTML(x.title||'未命名')}${action}`,escapeHTML(`${x.source||'未选来源'} / ${x.path||'未填路径'}${x.branch?` · 分支 ${x.branch}`:''} · ${relativeTime(x.updatedAt)}`)),side,{go:true,attrs:`data-kb-key="sub:${escapeHTML(x.id)}" data-kb-sub="${escapeHTML(x.id)}"`});
  }).join(''):`<div class="kb-empty">${showState?'暂无待处理条目。运行产出 knowledge.md 并在闭环信号后自动入队；也可以在这里手动新建或从运行产物加入。':'没有条目'}</div>`;
  body.innerHTML=kbPanel(`${kbSeg([['todo','待处理'],['published','已写回'],['rejected','已驳回'],['retracted','已撤回']],kb.subStatus,'kb-status',{label:'审核筛选'})}<button type="button" class="kb-btn small" id="kbNewSub" data-kb-key="new">新建</button>`,list,{label:'审核'});
  body.querySelectorAll('[data-kb-status]').forEach(b=>b.onclick=()=>{if(kb.subStatus!==b.dataset.kbStatus){kb.subStatus=b.dataset.kbStatus;kbRender()}});
  body.querySelectorAll('[data-kb-cand]').forEach(el=>el.onclick=()=>{const c=kb.candidates.find(x=>x.artifact?.id===el.dataset.kbCand);if(c)kbOpenCandidate(c)});
  body.querySelectorAll('[data-kb-sub]').forEach(el=>el.onclick=()=>kbOpenSubmission(el.dataset.kbSub));
  $('#kbNewSub').onclick=()=>{kb.draft={id:'',title:'',source:(kbAllSources()[0]?.id||''),path:'',content:'',status:'new'};kbRenderEditor()};
}

function kbRunLink(runId){return `<a href="#/runs/${encodeURIComponent(runId)}" data-kb-run="${escapeHTML(runId)}">运行 ${escapeHTML(runId)}</a>`}

function kbOpenCandidate(c){
  const a=c.artifact||{};
  const sub=[a.runId?`来自${kbRunLink(a.runId)}`:'',a.nodeId?`节点 ${escapeHTML(a.nodeId)}`:'',relativeTime(a.createdAt)].filter(Boolean).join(' · ');
  kbDrawerOpen(a.path||a.name||'运行产物',sub,'<p class="kb-lead">这是运行产出的候选。加入审核后可以编辑标题、来源和路径。</p>','<button type="button" class="kb-btn primary" id="kbQueueBtn">加入审核</button>');
  $('#kbQueueBtn').onclick=async e=>{
    const btn=e.currentTarget;btn.disabled=true;
    try{
      const item=await api('/api/knowledge/submissions',{method:'POST',body:JSON.stringify({runId:a.runId,artifactId:a.id})});
      toast('已加入审核队列','ok');kb.subStatus='todo';
      await kbRefresh();kbRender();
      await kbOpenSubmission(item.id);
    }catch(err){btn.disabled=false;toast(err.message,'err')}
  };
}

async function kbOpenSubmission(id){
  const opener=document.activeElement;
  try{kb.draft=await api(`/api/knowledge/submissions/${encodeURIComponent(id)}`)}
  catch(err){toast(err.message,'err');return}
  kbRenderEditor({opener,openerKey:`sub:${id}`});
}

function kbSourceOptions(selected){
  const all=kbAllSources().filter(kbSourceWritable).map(x=>({id:x.id,name:kbSourceName(x)}));
  if(selected&&!all.some(x=>x.id===selected))all.unshift({id:selected,name:selected});
  return `<option value="">选择来源</option>${all.map(x=>`<option value="${escapeHTML(x.id)}" ${x.id===selected?'selected':''}>${escapeHTML(x.name)}</option>`).join('')}`;
}

function kbGateHTML(d){
  const checks=d.gateChecks||[];
  const signals=(d.sedimentSignals||[]).map(x=>KB_SIGNAL_LABEL[x]||x);
  if(!checks.length&&!signals.length)return '';
  const rows=checks.map(c=>`<div class="kb-gate ${c.ok?'ok':'err'}">${kbBadge(c.ok?'通过':'未通过',c.ok?'ok':'err')}<span>${escapeHTML(c.name||c.key)}</span><span class="kb-gate-detail">${escapeHTML(c.detail||'')}</span></div>`).join('');
  const signalLine=signals.length?`<div class="kb-cap">闭环事件：${escapeHTML(signals.join('、'))}${d.fingerprint?` · 指纹 <code>${escapeHTML(d.fingerprint.slice(0,12))}</code>`:''}</div>`:'';
  return `<div class="kb-gates"><div class="kb-gates-head">沉淀门禁</div>${rows}${signalLine}</div>`;
}

function kbRenderEditor(opt={}){
  const d=kb.draft;
  if(!d)return;
  const locked=d.status==='published';
  const isNew=d.status==='new';
  const retracted=d.status==='retracted';
  const offline=d.action==='retract'&&!isNew;
  const dis=locked||retracted||offline?' disabled':'';
  const sub=isNew?'手动添加一条待审核知识':[d.runId?`来自${kbRunLink(d.runId)}`:'手动创建',d.createdBy?`创建人 ${escapeHTML(d.createdBy)}`:'',d.reviewedBy?`审核人 ${escapeHTML(d.reviewedBy)}`:'',escapeHTML(KB_SUB_STATUS[d.status]?.[0]||'')].filter(Boolean).join(' · ');
  const writeInfo=kbWriteInfoHTML(d);
  const gateHTML=kbGateHTML(d);
  const body=`
    ${d.error?`<div class="kb-alert">上次写回失败：${escapeHTML(d.error)}</div>`:''}
    ${writeInfo}
    ${offline?`<div class="kb-alert">治理下线：审核通过后会删除托管正文与索引并写墓碑，不会直接修改知识内容。</div>`:''}
    ${gateHTML}
    ${locked?`<div class="kb-field"><label for="kbRetractReason">撤回原因</label><div class="kb-field-pair"><select id="kbRetractReason" class="kb-input" name="retractReason"><option>不再适用</option><option>内容错误</option><option>包含敏感信息</option><option>重复</option><option>其他</option></select><span class="kb-hint">撤回会写墓碑、删原文并去索引</span></div></div>`:''}
    ${retracted?`<div class="kb-alert">该条目已撤回，原文与索引已删除；同内容再次沉淀会被墓碑拦截。</div>`:''}
    <form id="kbEditorForm" autocomplete="off">
      <div class="kb-field"><label for="kbEdTitle">标题</label><input id="kbEdTitle" class="kb-input" type="text" name="title" value="${escapeHTML(d.title||'')}" placeholder="一句话说明这条知识"${dis}></div>
      <div class="kb-field"><label for="kbEdSource">写入位置</label><div class="kb-field-pair"><select id="kbEdSource" class="kb-input" name="source"${dis}>${kbSourceOptions(d.source)}</select><input class="kb-input mono" type="text" name="path" aria-label="文档路径（.md）" value="${escapeHTML(d.path||'')}" placeholder="guides/xxx.md"${dis}></div></div>
      <div class="kb-field"><label for="kbEdContent">正文</label><textarea id="kbEdContent" class="kb-input" name="content"${dis}>${escapeHTML(d.content||'')}</textarea></div>
      ${locked&&!d.reviewNote?'':`<div class="kb-field"><label for="kbEdNote">备注</label><input id="kbEdNote" class="kb-input" type="text" name="reviewNote" value="${escapeHTML(d.reviewNote||'')}" placeholder="可选：驳回原因或修改说明"${dis}></div>`}
    </form>`;
  const writeStatus=kbWriteStatus(d);
  const gitSource=locked&&kbSourceById(d.source)?.kind==='git';
  const foot=locked
    ?`${writeStatus?kbBadge(escapeHTML(writeStatus[0]),writeStatus[1]):''}<span class="kb-foot-note kb-ok">已写回${d.commit?` · 版本 <code>${escapeHTML(d.commit.slice(0,10))}</code>`:''}</span>${gitSource?'<span class="kb-foot-note">Git 来源请通过删除合并请求撤回</span>':'<button type="button" class="kb-btn quiet" data-kb-act="retract">撤回</button>'}`
    :retracted?`${kbBadge('已撤回','')}`
    :isNew?'<button type="button" class="kb-btn primary" data-kb-act="create">创建</button>'
    :offline?`<button type="button" class="kb-btn quiet" data-kb-act="delete">删除</button><button type="button" class="kb-btn" data-kb-act="reject">驳回</button><button type="button" class="kb-btn primary" data-kb-act="publish">确认下线</button>`
    :`<button type="button" class="kb-btn quiet" data-kb-act="delete">删除</button><button type="button" class="kb-btn" data-kb-act="reject">驳回</button><button type="button" class="kb-btn" data-kb-act="save">保存</button><button type="button" class="kb-btn primary" data-kb-act="publish">写回知识库</button>`;
  kbDrawerOpen(isNew?'新建条目':(d.title||'未命名'),sub,body,foot,opt);
  const form=$('#kbEditorForm');
  form.onsubmit=e=>e.preventDefault();
  const values=()=>({title:form.elements.title.value.trim(),source:form.elements.source.value,path:form.elements.path.value.trim(),content:form.elements.content.value,reviewNote:(form.elements.reviewNote?.value||'').trim()});
  $$('#kbDrawerFoot [data-kb-act]').forEach(b=>b.onclick=()=>kbEditorAction(b.dataset.kbAct,d,values));
}

async function kbEditorAction(act,d,values){
  const id=encodeURIComponent(d.id);
  const save=()=>api(`/api/knowledge/submissions/${id}`,{method:'PUT',body:JSON.stringify(values())});
  const buttons=$$('#kbDrawerFoot button');
  const states=buttons.map(b=>b.disabled);
  const busy=on=>buttons.forEach((b,i)=>b.disabled=on||states[i]);
  const done=async()=>{await kbRefresh();kbRender();kbDrawerClose()};
  if(act==='delete'&&!(await afConfirm({title:'删除沉淀条目',message:'删除这条沉淀条目？',confirmText:'删除',danger:true})))return;
  if(act==='publish'){
    const v=values();
    if(!v.source)return toast('请选择写入的知识库来源');
    if(!/\.md$/i.test(v.path))return toast('路径必须以 .md 结尾');
    if(d.action==='retract'){
      if(!(await afConfirm({title:'确认下线这篇文档？',message:`来源：${v.source}\n路径：${v.path}\n\n将删除托管正文与索引并写墓碑；不会直接修改知识内容，也不会改写 Git 仓库。`,confirmText:'确认下线',danger:true})))return;
    }else{
      const source=kbSourceById(v.source);
      const isGit=source?.kind==='git';
      const message=isGit
        ?`来源：${kbSourceName(source)}\n路径：${v.path}\n\n将提交到一个新分支并推送，再按托管平台创建合并请求；不支持的平台只推送分支，需在仓库网页手动创建。`
        :`来源：${v.source}\n路径：${v.path}\n\n将写入托管知识库并重建索引。`;
      if(!(await afConfirm({title:isGit?'确认写回 Git 来源？':'确认写回知识库？',message,confirmText:'写回',info:true})))return;
    }
  }
  busy(true);
  try{
    if(act==='create'){
      const item=await api('/api/knowledge/submissions',{method:'POST',body:JSON.stringify({...values(),projectId:kbProjectId()})});
      toast('已创建沉淀条目','ok');
      await kbRefresh();kbRender();
      kb.draft=await api(`/api/knowledge/submissions/${encodeURIComponent(item.id)}`);
      kbRenderEditor({openerKey:`sub:${item.id}`});
      return;
    }
    if(act==='save'){kb.draft=await save();toast('已保存','ok');await kbRefresh();kbRender();kbRenderEditor();return}
    if(act==='reject'){await save();await api(`/api/knowledge/submissions/${id}/reject`,{method:'POST',body:JSON.stringify({reviewNote:values().reviewNote})});toast('已驳回');await done();return}
    if(act==='delete'){await api(`/api/knowledge/submissions/${id}`,{method:'DELETE'});toast('已删除');await done();return}
    if(act==='retract'){
      const reason=$('#kbRetractReason')?.value||'不再适用';
      if(!(await afConfirm({title:'撤回这条知识？',message:`原因：${reason}\n\n撤回会写墓碑、删除原文并去索引；同内容不能再次沉淀。`,confirmText:'撤回',danger:true})))return;
      await api(`/api/knowledge/submissions/${id}/retract`,{method:'POST',body:JSON.stringify({reasonCategory:reason,reasonDetail:(values().reviewNote||'')})});
      toast('已撤回并写入墓碑');await done();return;
    }
    if(act==='publish'){
      await save();
      const item=await api(`/api/knowledge/submissions/${id}/publish`,{method:'POST'});
      if(d.action==='retract')toast('已下线并写入墓碑','ok');
      else if(item.mrUrl)toast('已推送分支并创建合并请求','ok');
      else if(item.branch)toast(`已推送写回分支 ${item.branch}`,'ok');
      else toast(`已写回知识库 · 版本 ${String(item.commit||'').slice(0,8)}`,'ok');
      await done();
    }
  }catch(err){
    toast(err.message,'err');
    await kbRefresh();kbRender();
    if(act==='publish'&&d.id&&kbDrawerIsOpen()){
      try{kb.draft=await api(`/api/knowledge/submissions/${id}`);kbRenderEditor();return}catch{}
    }
    busy(false);
  }
}

// ---------- 反馈与治理 ----------
function kbDocStatusBadge(status){
  const item=KB_DOC_STATUS[String(status||'').toLowerCase()];
  return item?` ${kbBadge(item[0],item[1])}`:'';
}

function kbFeedbackButtons(source,path,title,query=''){
  const attrs=` data-kb-fb-source="${escapeHTML(source||'')}" data-kb-fb-path="${escapeHTML(path||'')}" data-kb-fb-title="${escapeHTML(title||'')}" data-kb-fb-query="${escapeHTML(query||'')}"`;
  return `<span class="kb-fb-actions" data-kb-feedback-box><button type="button" class="kb-btn small" data-kb-feedback="helpful"${attrs}>有用</button><button type="button" class="kb-btn small" data-kb-feedback="not_helpful"${attrs}>没用</button></span>`;
}

async function kbSendFeedback(btn){
  const box=btn.closest('[data-kb-feedback-box]');
  if(!box||box.dataset.sent)return;
  const kind=btn.dataset.kbFeedback;
  if(!kind)return;
  box.querySelectorAll('button').forEach(b=>b.disabled=true);
  try{
    await api('/api/knowledge/feedback',{method:'POST',body:JSON.stringify({
      projectId:kbProjectId(),source:btn.dataset.kbFbSource||'',path:btn.dataset.kbFbPath||'',
      title:btn.dataset.kbFbTitle||'',query:btn.dataset.kbFbQuery||'',kind,surface:'knowledge_center',
    })});
    box.dataset.sent='1';
    box.innerHTML=`<span class="kb-fb-sent">${kind==='helpful'?'已记录：有用':'已记录：没用'}</span>`;
    toast(kind==='helpful'?'已记录：有用':'已记录：没用','ok');
    if(kb.governance)await kbRefresh();
  }catch(err){
    box.querySelectorAll('button').forEach(b=>b.disabled=false);
    toast(err.message,'err');
  }
}

function kbBindFeedback(root=document){
  root.querySelectorAll('[data-kb-feedback-box] [data-kb-feedback]').forEach(btn=>{
    btn.onclick=e=>{e.stopPropagation();kbSendFeedback(btn)};
  });
}

function kbGovernanceIssueHTML(issue,i){
  const meta=KB_GOVERNANCE_TYPE[issue.type]||[issue.type||'问题','warn'];
  const hosted=kbSourceById(issue.sourceId)?.kind==='hosted';
  const loc=[issue.sourceId,issue.path,issue.line?`L${issue.line}`:''].filter(Boolean).join(' · ');
  const actions=`<span class="kb-row-actions kb-gov-actions"><button type="button" class="kb-btn small" data-kb-gov-action="update" data-kb-gov-source="${escapeHTML(issue.sourceId||'')}" data-kb-gov-path="${escapeHTML(issue.path||'')}">发起更新</button>${hosted?`<button type="button" class="kb-btn quiet small" data-kb-gov-action="retract" data-kb-gov-source="${escapeHTML(issue.sourceId||'')}" data-kb-gov-path="${escapeHTML(issue.path||'')}">下线</button>`:''}</span>`;
  return kbRow(`<div class="kb-row-title">${escapeHTML(issue.title||issue.path||'文档')} ${kbBadge(meta[0],meta[1])}</div><div class="kb-row-desc">${escapeHTML(issue.summary||'')}</div><cite>${escapeHTML(loc)}</cite>`,actions,{cls:'kb-gov-row',attrs:`data-kb-key="gov:${i}"`});
}

function kbFeedbackRowHTML(item,i){
  const meta=KB_FEEDBACK_KIND[item.kind]||[item.kind||'反馈',''];
  const target=item.title||item.path||(item.surface==='main_bot'?'Main Bot 回答':'知识文档');
  const detail=[meta[0],item.actor,relativeTime(item.createdAt),item.surface==='agent'?'Agent 标记':item.surface==='main_bot'?'Main Bot':'知识中心'].filter(Boolean).join(' · ');
  return kbRow(kbText(`${escapeHTML(target)} ${kbBadge(meta[0],meta[1])}`,item.note?escapeHTML(item.note):escapeHTML(detail)),item.note?kbCaption(escapeHTML(detail)):'',{cls:'kb-feedback-row',attrs:`data-kb-key="fb:${i}"`});
}

function kbRenderGovernance(body){
  if(kb.governanceError){body.innerHTML=`<div class="kb-empty">治理面板读取失败：${escapeHTML(kb.governanceError)}</div>`;return}
  const g=kb.governance||{};
  const counts=g.counts||{};
  const issues=g.issues||[];
  const feedback=g.feedback||[];
  const gaps=g.gaps||[];
  const last=g.lastScanAt?relativeTime(g.lastScanAt):'尚未检查';
  const chips=Object.entries(KB_GOVERNANCE_TYPE).map(([k,meta])=>`<span class="kb-gov-chip">${meta[0]} <b>${Number(counts[k])||0}</b></span>`).join('');
  const health=kbPanel(`${kbPanelTitle('健康检查',`最近检查 ${escapeHTML(last)}`)}<button type="button" class="kb-btn small" id="kbGovScan">立即检查</button>`,
    `<div class="kb-gov-chips">${chips}<span class="kb-gov-chip">合计 <b>${Number(counts.total)||0}</b></span></div>${issues.length?issues.map(kbGovernanceIssueHTML).join(''):'<div class="kb-empty">还没有健康检查结果。点「立即检查」或等待每日定时检查。</div>'}`,
    {label:'健康检查'});
  const feedbackPanel=kbPanel(kbPanelTitle('反馈与问题标记',`${feedback.length} 条`),feedback.length?feedback.map(kbFeedbackRowHTML).join(''):'<div class="kb-empty">还没有反馈。试搜、读原文或 Main Bot 回答旁都可以反馈；Agent 可用 kb_report_issue 标记问题。</div>',{label:'反馈与问题标记'});
  const gapsPanel=kbPanel(kbPanelTitle('知识缺口',`${Number(g.gapsTotal)||0} 组搜不到结果的查询`),gaps.length?gaps.map((gap,i)=>kbRow(kbText(escapeHTML(gap.query||'-'),`出现 ${Number(gap.count)||1} 次 · 最近 ${escapeHTML(relativeTime(gap.lastSeen))}`),kbValue(String(Number(gap.count)||1),'kb-source-num'),{cls:'kb-gap-row',attrs:`data-kb-key="gap:${i}"`})).join(''):'<div class="kb-empty">还没有知识缺口记录。试搜没有命中时会自动记录在这里。</div>',{label:'知识缺口'});
  const placeholders=kbPanel(kbPanelTitle('引用次数与从未命中','依赖后续引用表，本阶段留位'),`
    <div class="kb-row kb-placeholder-row"><div class="kb-row-main"><div class="kb-row-title">常被引用的文档</div><div class="kb-row-desc">接入引用表后显示</div></div><span class="kb-row-value">—</span></div>
    <div class="kb-row kb-placeholder-row"><div class="kb-row-main"><div class="kb-row-title">从未命中的文档</div><div class="kb-row-desc">接入引用表后显示</div></div><span class="kb-row-value">—</span></div>
    <div class="kb-cap">平台不会用 0 冒充“从未命中”；这两项等引用记录持久化后再接。</div>`,{label:'引用与命中占位'});
  body.innerHTML=`<div class="kb-gov-layout"><div class="kb-gov-main">${health}${feedbackPanel}</div><div class="kb-gov-side">${gapsPanel}${placeholders}</div></div>`;
  const scan=$('#kbGovScan');
  if(scan)scan.onclick=async()=>{
    scan.disabled=true;scan.textContent='检查中…';
    try{
      await api('/api/knowledge/governance/scan',{method:'POST',body:JSON.stringify({projectId:kbProjectId()})});
      toast('健康检查完成','ok');
      await kbRefresh();kbRender();
    }catch(err){scan.disabled=false;scan.textContent='立即检查';toast(err.message,'err')}
  };
  body.querySelectorAll('[data-kb-gov-action]').forEach(btn=>btn.onclick=async e=>{
    e.stopPropagation();
    const action=btn.dataset.kbGovAction;
    btn.disabled=true;
    try{
      const item=await api('/api/knowledge/governance/dispose',{method:'POST',body:JSON.stringify({
        projectId:kbProjectId(),source:btn.dataset.kbGovSource,path:btn.dataset.kbGovPath,action,
      })});
      toast(action==='retract'?'已加入审核队列，确认后下线':'已加入审核队列，可继续编辑','ok');
      kb.tab='distill';kb.subStatus='todo';
      await kbRefresh();kbRender();
      await kbOpenSubmission(item.id);
    }catch(err){btn.disabled=false;toast(err.message,'err')}
  });
}

// ---------- 试搜与读原文 ----------
function kbRenderSearch(body){
  const q=kb.search;
  const sources=kbAllSources();
  if(q.sources.length&&!sources.some(x=>x.id===q.sources[0]))q.sources=[];
  const selected=q.sources[0]||'';
  body.innerHTML=kbPanel(`<form class="kb-search" id="kbSearchForm" role="search">${KB_SEARCH_ICON}<input type="search" name="q" aria-label="检索知识库" placeholder="问一个问题，或输入关键词，结果与 Agent 看到的一致" autocomplete="off" value="${escapeHTML(q.query)}"><select name="source" aria-label="限定来源"><option value="">全部来源</option>${sources.map(x=>`<option value="${escapeHTML(x.id)}"${x.id===selected?' selected':''}>${escapeHTML(x.name||x.id)}</option>`).join('')}</select></form>`,`<div class="kb-cap kb-search-note">少于两个字的查询可能没有结果，换更长或更具体的词再试。</div><div id="kbHits">${kbHitsHTML()}</div>`,{label:'试搜',headCls:'kb-panel-search'});
  const form=$('#kbSearchForm');
  form.onsubmit=async e=>{
    e.preventDefault();
    q.query=form.elements.q.value.trim();
    if(q.query)return kbDoSearch();
    q.seq++;q.result=null;q.error='';q.loading=false;kbUpdateHits();
  };
  form.elements.source.onchange=()=>{
    const v=form.elements.source.value;q.sources=v?[v]:[];
    if(q.query&&(q.result||q.error))kbDoSearch();
  };
  kbBindHits();
}

function kbRecentHTML(){
  const list=(kb.usage?.activity||[]).slice(0,20);
  const head=kbCaption('Agent 最近的检索');
  if(!list.length)return head+'<div class="kb-line-note">还没有 Agent 检索记录。开启角色并运行一次流程后会显示在这里</div>';
  return head+list.map((x,i)=>{
    const value=x.ok?kbValue(x.tool==='kb_search'?`${Number(x.hits)||0} 条`:'成功'):kbBadge('失败','err',x.error||'');
    const desc=[kbAgentName(x.agentId),x.tool&&x.tool!=='kb_search'?x.tool:'',relativeTime(x.at)].filter(Boolean).join(' · ');
    return kbRow(kbText(escapeHTML(x.query||x.tool||'-'),escapeHTML(desc)),value,x.query?{go:true,attrs:`data-kb-key="recent:${i}" data-kb-recent="${i}"`}:{});
  }).join('');
}

function kbHitsHTML(){
  const q=kb.search;
  if(q.loading)return '<div class="kb-empty">检索中…</div>';
  if(q.error)return `<div class="kb-empty">检索失败：${escapeHTML(q.error)}</div>`;
  if(!q.result)return kbRecentHTML();
  const r=q.result.result||{};
  const hits=r.results||[];
  const head=kbCaption(`${hits.length} 条结果 · ${Number(q.result.latencyMs)||0}ms`);
  if(!hits.length)return head+'<div class="kb-empty">没有命中，换个关键词试试</div>';
  return head+hits.map((h,i)=>{
    const citation=h.citation||kbCitationText(h.source,h.commit,h.revision,h.path,h.start_line,h.end_line);
    const url=kbGitFileURL(h.source,h.commit,h.path,h.start_line,h.end_line);
    const fb=kbFeedbackButtons(h.source,h.path,h.title||h.path,q.query);
    return kbRow(`<div class="kb-row-title">${escapeHTML(h.title||h.path)}${kbDocStatusBadge(h.doc_status)}${h.heading?` <span class="kb-count">· ${escapeHTML(h.heading)}</span>`:''}</div><div class="kb-row-desc">${escapeHTML(h.snippet||'')}</div><cite>${escapeHTML([kbLayerLabel(h.layer),h.base_name].filter(Boolean).join(' · '))} · ${kbCitationHTML(citation,url)}</cite>`,fb,{go:true,cls:'kb-hit',attrs:`data-kb-key="hit:${i}" data-kb-hit="${i}"`});
  }).join('');
}

function kbUpdateHits(){const hits=$('#kbHits');if(hits){hits.innerHTML=kbHitsHTML();kbBindHits()}}

function kbBindHits(){
  $$('#kbHits [data-kb-cite]').forEach(a=>a.onclick=e=>e.stopPropagation());
  $$('#kbHits [data-kb-hit]').forEach(el=>el.onclick=()=>kbOpenDocument(kb.search.result?.result?.results?.[Number(el.dataset.kbHit)]));
  kbBindFeedback($('#kbHits'));
  const recent=(kb.usage?.activity||[]).slice(0,20);
  $$('#kbHits [data-kb-recent]').forEach(el=>el.onclick=()=>{
    const query=recent[Number(el.dataset.kbRecent)]?.query;
    if(!query)return;
    kb.search.query=query;
    const input=$('#kbSearchForm')?.elements.q;if(input)input.value=query;
    kbDoSearch();
  });
}

async function kbDoSearch(){
  const q=kb.search;
  const seq=++q.seq;
  q.loading=true;q.error='';kbUpdateHits();
  try{
    const result=await api('/api/knowledge/bases/search',{method:'POST',body:JSON.stringify({query:q.query,projectId:kbProjectId(),sources:q.sources})});
    if(seq!==q.seq)return;
    q.result=result;
  }catch(err){
    if(seq!==q.seq)return;
    q.error=err.message;q.result=null;
  }
  q.loading=false;
  kbUpdateHits();
}

async function kbOpenDocument(hit){
  if(!hit)return;
  const source=hit.source||kb.docs.source;
  const path=hit.path||'';
  if(!source||!path)return;
  const start=Number(hit.start_line)||0;
  const end=Number(hit.end_line)||0;
  const citation=hit.citation||kbCitationText(source,hit.commit,hit.revision,path,start,end);
  const seq=kbDrawerOpen(hit.title||path,kbCitationHTML(citation,kbGitFileURL(source,hit.commit,path,start,end)),'<div class="kb-empty">正在读取原文…</div>');
  try{
    const doc=await api(`/api/knowledge/bases/document?projectId=${encodeURIComponent(kbProjectId())}&source=${encodeURIComponent(source)}&path=${encodeURIComponent(path)}&start=${start}&end=${end}`);
    if(seq!==kb.drawer.seq)return;
    const shownCitation=doc.citation||citation;
    $('#kbDrawerSub').innerHTML=escapeHTML([kbLayerLabel(doc.layer),doc.base_name].filter(Boolean).join(' · '))+' · '+kbCitationHTML(shownCitation,kbGitFileURL(source,doc.commit,path,start||doc.start_line,end||doc.end_line));
    const first=start||Number(doc.start_line)||0,last=end||Number(doc.end_line)||0;
    const host=$('#kbDrawerBody');
    host.innerHTML=`<div class="kb-doc">${(doc.lines||[]).map(l=>`<div class="${l.number>=first&&l.number<=last?'hl':''}"${l.number===first?' id="kbDocAnchor"':''}><span>${l.number}</span><code>${escapeHTML(l.text)}</code></div>`).join('')}</div>`;
    const foot=$('#kbDrawerFoot');
    if(foot){
      foot.hidden=false;
      foot.innerHTML=`<div class="kb-feedback-bar"><span>这篇知识对你有帮助吗？</span>${kbFeedbackButtons(source,path,hit.title||path,kb.search.query)}</div>`;
      kbBindFeedback(foot);
    }
    const anchor=$('#kbDocAnchor');
    if(anchor)host.scrollTop+=anchor.getBoundingClientRect().top-host.getBoundingClientRect().top-(host.clientHeight-anchor.offsetHeight)/2;
  }catch(err){if(seq===kb.drawer.seq)$('#kbDrawerBody').innerHTML=`<div class="kb-empty">读取失败：${escapeHTML(err.message)}</div>`}
}

// ---------- 全局交互 ----------
document.addEventListener('click',e=>{
  if(e.target.closest('#kbDrawerClose')||e.target.id==='kbScrim')return kbDrawerClose();
  const run=e.target.closest('#kbDrawer [data-kb-run]');
  if(run){e.preventDefault();const id=run.dataset.kbRun;kbDrawerClose();switchView('runs');showRunDetail(id)}
});
document.addEventListener('keydown',e=>{
  if(e.key==='Escape'&&kbDrawerIsOpen()){e.preventDefault();kbDrawerClose();return}
  if((e.key==='Enter'||e.key===' ')&&e.target.classList?.contains('kb-go')){e.preventDefault();e.target.click()}
});
(()=>{
  const view=document.getElementById('knowledgeView');
  if(view)new MutationObserver(()=>{if(!view.classList.contains('active'))kbDrawerClose()}).observe(view,{attributes:true,attributeFilter:['class']});
})();
