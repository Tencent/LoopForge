// 知识中心：连接 / 角色 / 审核 / 检索四个标签，详情与编辑统一进右侧抽屉。
// 依赖 app.js 的全局 state、api、$、$$、escapeHTML、toast、relativeTime、switchView、showRunDetail。
const kb={tab:'setup',projectId:null,settings:null,sources:null,sourcesError:'',usage:null,candidates:[],submissions:[],subStatus:'todo',draft:null,togglingAgents:new Set(),search:{query:'',sources:[],result:null,loading:false,error:'',seq:0},drawer:{opener:null,openerKey:'',seq:0}};

const KB_TOOLS=[
  ['kb_search','按问题或关键词混合检索，结果带 source / commit / 行号'],
  ['kb_fetch','按 source + path + 行号读取 Markdown 原文'],
  ['kb_list_sources','列出当前项目可用的知识库来源'],
  ['kb_get_source','查看来源状态与文档清单'],
];
const KB_SUB_STATUS={pending:['待审核','warn'],published:['已写回','ok'],rejected:['已驳回','']};
const KB_SOURCE_STATUS={indexing:['索引中','warn'],pending:['等待索引','warn'],error:['异常','err'],failed:['异常','err']};
const KB_SEARCH_ICON='<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true" focusable="false"><circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/></svg>';

function kbProjectId(){return state.projectFilter||''}
function kbProjectName(){const p=(state.data?.projects||[]).find(x=>x.id===kbProjectId());return p?.name||'全部项目'}
function kbConsoleURL(){const u=kb.settings?.url||'';return u.replace(/\/mcp\/?$/,'/')}
function kbAllowed(){const s=kb.settings||{};const own=s.projectSources?.[kbProjectId()];return own!==undefined?own:(s.defaultSources||[])}
function kbHasOwnScope(){return kb.settings?.projectSources?.[kbProjectId()]!==undefined}
function kbAgentName(id){return (state.data?.agents||[]).find(a=>a.id===id)?.name||id||'-'}
function kbAgentWorkflows(id){const names=[];for(const w of state.data?.workflows||[])if((w.nodes||[]).some(n=>n.agentId===id)&&!names.includes(w.name))names.push(w.name);return names}
function kbSourceCount(){return (kb.sources||[]).filter(x=>x.allowed).length}
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
  const root=$('#knowledgeBody');
  if(!root)return;
  if(kb.projectId!==null&&kb.projectId!==kbProjectId()){kbDrawerClose();Object.assign(kb.search,{result:null,error:'',sources:[]})}
  kb.projectId=kbProjectId();
  if(!kb.settings){root.innerHTML='<div class="kb-empty">正在加载知识库配置…</div>';kbRenderStatus()}
  await kbRefresh();
  kbRender();
}

async function kbRefresh(){
  const pid=encodeURIComponent(kbProjectId());
  const [settings,usage,subs,cands]=await Promise.all([
    api('/api/knowledge/settings',{silent:true}).catch(err=>({error:err.message})),
    api(`/api/knowledge/usage?projectId=${pid}`,{silent:true}).catch(()=>null),
    api(`/api/knowledge/submissions?projectId=${pid}`,{silent:true}).catch(()=>[]),
    api(`/api/knowledge/candidates?projectId=${pid}`,{silent:true}).catch(()=>[]),
  ]);
  kb.settings=settings;kb.usage=usage;kb.submissions=subs||[];kb.candidates=cands||[];
  if(settings?.ready){
    try{kb.sources=await api(`/api/knowledge/sources?projectId=${pid}`,{silent:true});kb.sourcesError=''}
    catch(err){kb.sources=null;kb.sourcesError=err.message}
  }else{kb.sources=null;kb.sourcesError=''}
}

// 返回 [色调, 指标卡短标签, 完整说明]。
function kbConnState(){
  const s=kb.settings;
  if(!s)return ['','读取中','正在读取知识库状态…'];
  if(s.error)return ['err','读取失败',`无法读取知识库配置：${s.error}`];
  if(s.ready)return kb.sourcesError?['err','连接异常',`连接异常 · ${kb.sourcesError}`]:['ok','已接入',`已接入团队知识库 · ${kbProjectName()} 可用 ${kbSourceCount()} 个来源`];
  if(s.tokenConfigured||s.url)return ['warn','未启用','未启用团队知识库'];
  return ['warn','未接入','未接入团队知识库'];
}

function kbSourcesLive(){return !!(kb.settings?.ready&&!kb.sourcesError&&kb.sources)}
function kbMetric(label,value,foot,opt={}){
  return `<div class="kb-metric"><span class="kb-metric-label">${label}</span><div class="kb-metric-value${opt.empty?' is-empty':''}"${opt.attrs?` ${opt.attrs}`:''}>${value}</div><span class="kb-metric-foot"${foot?` title="${escapeHTML(foot)}"`:''}>${escapeHTML(foot)}</span></div>`;
}

function kbMetricsHTML(){
  const s=kb.settings;
  const [tone,label,detail]=kbConnState();
  const updated=s?.updatedAt&&s?.updatedBy?`${s.updatedBy} · ${relativeTime(s.updatedAt)}更新`:'';
  const connFoot=!s?'':tone==='err'?(s.error||kb.sourcesError):updated||(s.ready?kbProjectName():'在「连接」中完成配置');
  const conn=kbMetric('连接状态',`<span class="kb-dot${tone?` ${tone}`:''}" aria-hidden="true"></span><span class="kb-status-text">${escapeHTML(label)}</span>`,connFoot,{attrs:`id="kbStatus" title="${escapeHTML(detail)}"`});
  const live=kbSourcesLive();
  const offFoot=!s?'':s.error?'配置读取失败':s.ready?'来源暂不可达':'知识库未启用';
  const sources=live
    ?kbMetric('可用来源',String(kbSourceCount()),kbHasOwnScope()?'本项目自定义':'跟随平台默认')
    :kbMetric('可用来源','—',offFoot,{empty:true});
  let docs;
  if(live){
    const ready=(kb.sources||[]).filter(x=>x.allowed&&x.status==='ready');
    const total=ready.reduce((n,x)=>n+(Number(x.doc_count)||0),0);
    const top=ready.reduce((a,x)=>(Number(x.doc_count)||0)>(Number(a?.doc_count)||0)?x:a,null);
    docs=kbMetric('文档总数',total.toLocaleString('en-US'),total&&top?`${top.id} 占 ${Math.round((Number(top.doc_count)||0)/total*100)}%`:'暂无已索引文档');
  }else docs=kbMetric('文档总数','—',offFoot,{empty:true});
  const roles=kb.usage?kb.usage.agents?.length||0:null;
  const todo=kbTodoItems().length;
  const recent=kb.usage?.activity?.length||0;
  const pair=`<button type="button" class="kb-metric-link" data-kb-go="usage" aria-label="已开启角色 ${roles??'—'}，查看角色">${roles??'—'}</button><span class="kb-metric-sep" aria-hidden="true">/</span><button type="button" class="kb-metric-link" data-kb-go="distill" aria-label="待审核 ${todo}，查看审核">${todo}</button>`;
  const agents=kbMetric('已开启角色 / 待审核',pair,!s?'':recent?`Agent 最近检索 ${recent} 次`:'暂无 Agent 检索');
  return conn+sources+docs+agents;
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
  const link=$('#kbConsoleLink');
  if(link){link.hidden=!kb.settings?.url;link.href=kbConsoleURL()||'#'}
}

function kbRender(){
  const root=$('#knowledgeBody');
  if(!root)return;
  kbRenderStatus();
  const tabFocused=!!document.activeElement?.closest?.('#kbTabs');
  const roles=kb.usage?.agents?.length||0;
  const todo=kbTodoItems().length;
  const tabs=[['setup','连接',0],['usage','角色',roles],['distill','审核',todo],['search','检索',0]];
  if(!tabs.some(([k])=>k===kb.tab))kb.tab='setup';
  root.innerHTML=`
    <div class="kb-tabs" id="kbTabs" role="tablist" aria-label="知识中心">${tabs.map(([k,t,n])=>`<button type="button" role="tab" id="kbTab-${k}" data-kb-tab="${k}" aria-controls="kbTabBody" aria-selected="${kb.tab===k}" class="${kb.tab===k?'active':''}">${t}${n?`<span class="kb-count">${n}</span>`:''}</button>`).join('')}</div>
    <div id="kbTabBody" role="tabpanel" aria-labelledby="kbTab-${kb.tab}"></div>`;
  root.querySelectorAll('[data-kb-tab]').forEach(b=>b.onclick=()=>{
    if(kb.tab===b.dataset.kbTab)return;
    kb.tab=b.dataset.kbTab;kbRender();
    $(`#kbTab-${kb.tab}`)?.focus({preventScroll:true});
  });
  ({setup:kbRenderSetup,usage:kbRenderUsage,distill:kbRenderDistill,search:kbRenderSearch}[kb.tab])($('#kbTabBody'));
  if(tabFocused)$(`#kbTab-${kb.tab}`)?.focus({preventScroll:true});
}

// ---------- 连接 ----------
function kbRenderSetup(body){
  const s=kb.settings||{};
  const ro=!s.canManage;
  const dis=ro?' disabled':'';
  const updated=s.updatedAt&&s.updatedBy?`${escapeHTML(s.updatedBy)} · ${relativeTime(s.updatedAt)}`:'';
  const formHTML=`
    <form id="kbSettingsForm">
      ${kbRow(kbText('MCP 地址'),`<input class="kb-input mono" type="url" name="url" aria-label="MCP 地址" placeholder="http://kb.example.com:8080/mcp" value="${escapeHTML(s.url||'')}"${dis}>`,{cls:'kb-stack'})}
      ${kbRow(kbText('Token',s.tokenConfigured?'已配置，留空不变':'未配置'),`<input class="kb-input" type="password" name="token" autocomplete="new-password" aria-label="Token" placeholder="${s.tokenConfigured?'••••••••':'mcp_xxx（由知识库管理员申请）'}"${dis}>`,{cls:'kb-stack'})}
      ${kbRow(kbText('启用知识库'),kbSwitch(s.enabled,'启用知识库',{disabled:ro,attrs:'data-kb-field="enabled"'}))}
      ${kbRow(kbText('审核后写回','审核通过的条目写入知识库仓库'),kbSwitch(s.writeEnabled,'审核后写回',{disabled:ro,attrs:'data-kb-field="writeEnabled"'}))}
      <div class="kb-foot"><span class="kb-hint" id="kbCheckResult"${updated?' title="上次更新"':''}>${updated}</span>${ro?'':'<button type="button" class="kb-btn" id="kbCheckBtn">检查连接</button><button type="submit" class="kb-btn primary">保存</button>'}</div>
    </form>`;
  body.innerHTML=`
    ${s.error?`<div class="kb-note kb-err">读取配置失败：${escapeHTML(s.error)}</div>`:''}
    <div class="kb-grid">
      ${kbPanel(kbPanelTitle('连接配置',ro?'仅平台管理员可修改':''),formHTML,{label:'连接配置'})}
      ${kbPanel(`${kbPanelTitle('本项目可用来源')}<span id="kbScopeSeg"></span>`,'<div id="kbScopeBody"></div>',{label:'本项目可用来源'})}
    </div>`;
  kbRenderScope($('#kbScopeSeg'),$('#kbScopeBody'));
  const form=$('#kbSettingsForm');
  const field=name=>form.querySelector(`[data-kb-field="${name}"]`).getAttribute('aria-checked')==='true';
  if(!ro)form.querySelectorAll('[data-kb-field]').forEach(sw=>sw.onclick=()=>sw.setAttribute('aria-checked',String(sw.getAttribute('aria-checked')!=='true')));
  form.onsubmit=async e=>{
    e.preventDefault();
    if(ro)return;
    const f=form.elements;
    const submit=form.querySelector('[type="submit"]');submit.disabled=true;
    try{
      const saved=await api('/api/knowledge/settings',{method:'PUT',body:JSON.stringify({...kbSettingsPayload(),enabled:field('enabled'),url:f.url.value.trim(),token:f.token.value.trim(),writeEnabled:field('writeEnabled')})});
      kb.settings=saved;toast('知识库配置已保存','ok');await renderKnowledgeView();
    }catch(err){submit.disabled=false;toast(err.message,'err')}
  };
  const check=$('#kbCheckBtn');
  if(check)check.onclick=async()=>{
    const out=$('#kbCheckResult');out.className='kb-hint';out.removeAttribute('title');out.textContent='检查中…';check.disabled=true;
    try{
      const r=await api('/api/knowledge/check',{method:'POST',body:JSON.stringify({url:form.elements.url.value.trim(),token:form.elements.token.value.trim()})});
      if(r.ok){out.className='kb-hint kb-ok';out.textContent=`连接正常 · ${r.latencyMs}ms · ${(r.sources||[]).length} 个来源`;if(r.tools?.length)out.title=`工具：${r.tools.join(', ')}`}
      else{out.className='kb-hint kb-err';out.textContent=r.error||'连接失败';out.title=out.textContent}
    }catch(err){out.className='kb-hint kb-err';out.textContent=err.message;out.title=err.message}
    check.disabled=false;
  };
}

function kbSettingsPayload(){
  const s=kb.settings||{};
  return {enabled:!!s.enabled,url:s.url||'',writeEnabled:!!s.writeEnabled,defaultSources:s.defaultSources||[],projectSources:s.projectSources||{}};
}

function kbRenderScope(seg,host){
  const s=kb.settings||{};
  const note=text=>{seg.innerHTML='';host.innerHTML=`<div class="kb-line-note">${escapeHTML(text)}</div>`};
  if(!s.ready)return note('启用并保存连接后，这里会列出知识库来源');
  if(kb.sourcesError)return note(`知识库暂时不可达：${kb.sourcesError}`);
  const list=kb.sources||[];
  if(!list.length)return note('知识库没有返回任何来源');
  const own=kbHasOwnScope();
  const pid=kbProjectId();
  const ro=!s.canManage||!pid;
  const allowed=new Set(kbAllowed());
  const defaults=(s.defaultSources||[]).length;
  seg.innerHTML=kbSeg([['all','跟随平台',defaults?`平台默认 ${defaults} 个来源`:'平台默认：全部来源'],['custom','自定义','为本项目指定来源']],own?'custom':'all','kb-scope',{disabled:ro,label:'来源范围'});
  const max=Math.max(0,...list.filter(x=>x.status==='ready').map(x=>Number(x.doc_count)||0));
  host.innerHTML=`${pid?'':'<div class="kb-line-note">切换到具体项目后，可以为本项目指定来源</div>'}${list.map(src=>{
    const on=allowed.size===0||allowed.has(src.id);
    const ready=src.status==='ready';
    const n=Number(src.doc_count)||0;
    const [label,tone]=KB_SOURCE_STATUS[src.status]||[src.status||'-','warn'];
    const share=ready?`<span class="kb-share" aria-hidden="true"><i style="width:${max?(n/max*100).toFixed(1):0}%"></i></span>`:kbBadge(escapeHTML(label),tone);
    return kbRow(kbText(`<span class="mono" title="${escapeHTML(src.name||src.id)}">${escapeHTML(src.id)}</span>`),share+kbValue(ready?`${n} 篇`:'—','kb-source-num')+kbSwitch(on,src.id,{disabled:!own||ro,attrs:`data-kb-source="${escapeHTML(src.id)}"`}));
  }).join('')}`;
  const persist=async next=>{
    host.querySelectorAll('.kb-switch').forEach(x=>x.disabled=true);
    seg.querySelectorAll('button').forEach(x=>x.disabled=true);
    const payload=kbSettingsPayload();
    payload.projectSources={...payload.projectSources};
    if(next===null)delete payload.projectSources[pid];else payload.projectSources[pid]=next;
    try{kb.settings=await api('/api/knowledge/settings',{method:'PUT',body:JSON.stringify(payload)});await kbRefresh();kbRender();toast('项目授权范围已更新','ok')}
    catch(err){toast(err.message,'err');kbRender()}
  };
  if(ro)return;
  seg.querySelectorAll('[data-kb-scope]').forEach(b=>b.onclick=()=>{
    if(b.dataset.kbScope==='all'&&own)persist(null);
    if(b.dataset.kbScope==='custom'&&!own)persist(list.filter(x=>x.status==='ready').map(x=>x.id));
  });
  if(own)host.querySelectorAll('[data-kb-source]').forEach(sw=>sw.onclick=()=>{
    const id=sw.dataset.kbSource;const cur=new Set(kbAllowed());
    if(cur.has(id))cur.delete(id);else cur.add(id);
    if(!cur.size)return toast('至少保留一个来源；不限制请选「跟随平台」');
    sw.setAttribute('aria-checked',String(cur.has(id)));
    persist([...cur]);
  });
}

// ---------- 角色 ----------
function kbRenderUsage(body){
  const s=kb.settings||{};
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
  body.innerHTML=kbPanel(kbPanelTitle('角色',s.ready?'开启后，该角色运行时可只读检索知识库':'知识库未启用，开关可先打开，启用后生效'),list,{label:'角色'});
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
  const rule=kb.settings?.usageRule||'';
  const body=`
    ${kbCaption('MCP')}
    ${kbRow(kbText('团队知识库','<span class="mono">agentflow-knowledge</span> · 经平台代理访问，限定本项目授权来源'))}
    ${kbCaption('工具')}
    ${KB_TOOLS.map(([n,d])=>kbRow(kbText(`<span class="mono">${n}</span>`,escapeHTML(d)))).join('')}
    ${kbRow(kbText('<span class="mono kb-off">kb_write_document</span>','不对 Agent 开放，写回只能经「审核」'))}
    ${kbCaption('自动附带的规则「知识库使用规范」')}
    <pre class="kb-pre">${escapeHTML(rule||'未返回规则内容')}</pre>`;
  kbDrawerOpen('运行时注入的工具与规则','节点启动时平台签发 48 小时有效、限定本项目的只读令牌',body);
}

// ---------- 审核 ----------
function kbRenderDistill(body){
  const items=kb.subStatus==='todo'?kbTodoItems():kb.submissions.filter(x=>x.status===kb.subStatus).map(x=>({kind:'sub',x}));
  const showState=kb.subStatus==='todo';
  const list=items.length?items.map(it=>{
    if(it.kind==='cand'){
      const a=it.c.artifact||{};
      return kbRow(kbText(escapeHTML(a.path||a.name||'knowledge.md'),escapeHTML(`运行 ${a.runId||'-'} 产出 · ${relativeTime(a.createdAt)}`)),kbBadge('候选'),{go:true,attrs:`data-kb-key="cand:${escapeHTML(a.id)}" data-kb-cand="${escapeHTML(a.id)}"`});
    }
    const x=it.x;const [label,tone]=KB_SUB_STATUS[x.status]||[x.status||'-',''];
    return kbRow(kbText(escapeHTML(x.title||'未命名'),escapeHTML(`${x.source||'未选来源'} / ${x.path||'未填路径'} · ${relativeTime(x.updatedAt)}`)),showState?kbBadge(escapeHTML(label),tone):'',{go:true,attrs:`data-kb-key="sub:${escapeHTML(x.id)}" data-kb-sub="${escapeHTML(x.id)}"`});
  }).join(''):`<div class="kb-empty">${showState?'暂无待处理。开启知识库的角色写出 knowledge.md 后会出现在这里':'没有条目'}</div>`;
  body.innerHTML=kbPanel(`${kbSeg([['todo','待处理'],['published','已写回'],['rejected','已驳回']],kb.subStatus,'kb-status',{label:'审核筛选'})}<button type="button" class="kb-btn small" id="kbNewSub" data-kb-key="new">新建</button>`,list,{label:'审核'});
  body.querySelectorAll('[data-kb-status]').forEach(b=>b.onclick=()=>{if(kb.subStatus!==b.dataset.kbStatus){kb.subStatus=b.dataset.kbStatus;kbRender()}});
  body.querySelectorAll('[data-kb-cand]').forEach(el=>el.onclick=()=>{const c=kb.candidates.find(x=>x.artifact?.id===el.dataset.kbCand);if(c)kbOpenCandidate(c)});
  body.querySelectorAll('[data-kb-sub]').forEach(el=>el.onclick=()=>kbOpenSubmission(el.dataset.kbSub));
  $('#kbNewSub').onclick=()=>{kb.draft={id:'',title:'',source:(kbAllowed()[0]||''),path:'',content:'',status:'new'};kbRenderEditor()};
}

function kbRunLink(runId){return `<a href="#/runs/${encodeURIComponent(runId)}" data-kb-run="${escapeHTML(runId)}">运行 ${escapeHTML(runId)}</a>`}

function kbOpenCandidate(c){
  const a=c.artifact||{};
  const sub=[a.runId?`来自${kbRunLink(a.runId)}`:'',a.nodeId?`节点 ${escapeHTML(a.nodeId)}`:'',relativeTime(a.createdAt)].filter(Boolean).join(' · ');
  kbDrawerOpen(a.path||a.name||'knowledge.md',sub,'<p class="kb-lead">这是运行产出的候选。加入审核后可以编辑标题、来源和路径。</p>','<button type="button" class="kb-btn primary" id="kbQueueBtn">加入审核</button>');
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
  const all=(kb.sources||[]).filter(x=>x.allowed).map(x=>x.id);
  if(selected&&!all.includes(selected))all.unshift(selected);
  return `<option value="">选择来源</option>${all.map(id=>`<option value="${escapeHTML(id)}" ${id===selected?'selected':''}>${escapeHTML(id)}</option>`).join('')}`;
}

function kbRenderEditor(opt={}){
  const d=kb.draft;
  if(!d)return;
  const s=kb.settings||{};
  const locked=d.status==='published';
  const isNew=d.status==='new';
  const dis=locked?' disabled':'';
  const sub=isNew?'手动添加一条待审核知识':[d.runId?`来自${kbRunLink(d.runId)}`:'手动创建',d.createdBy?`创建人 ${escapeHTML(d.createdBy)}`:'',d.reviewedBy?`审核人 ${escapeHTML(d.reviewedBy)}`:'',escapeHTML(KB_SUB_STATUS[d.status]?.[0]||'')].filter(Boolean).join(' · ');
  const body=`
    ${d.error?`<div class="kb-alert">上次写回失败：${escapeHTML(d.error)}</div>`:''}
    <form id="kbEditorForm" autocomplete="off">
      <div class="kb-field"><label for="kbEdTitle">标题</label><input id="kbEdTitle" class="kb-input" type="text" name="title" value="${escapeHTML(d.title||'')}" placeholder="一句话说明这条知识"${dis}></div>
      <div class="kb-field"><label for="kbEdSource">写入位置</label><div class="kb-field-pair"><select id="kbEdSource" class="kb-input" name="source"${dis}>${kbSourceOptions(d.source)}</select><input class="kb-input mono" type="text" name="path" aria-label="文档路径（.md）" value="${escapeHTML(d.path||'')}" placeholder="guides/xxx.md"${dis}></div></div>
      <div class="kb-field"><label for="kbEdContent">正文</label><textarea id="kbEdContent" class="kb-input" name="content"${dis}>${escapeHTML(d.content||'')}</textarea></div>
      ${locked&&!d.reviewNote?'':`<div class="kb-field"><label for="kbEdNote">备注</label><input id="kbEdNote" class="kb-input" type="text" name="reviewNote" value="${escapeHTML(d.reviewNote||'')}" placeholder="可选：驳回原因或修改说明"${dis}></div>`}
    </form>`;
  const foot=locked
    ?`<span class="kb-foot-note kb-ok">已写回${d.commit?` · commit <code>${escapeHTML(d.commit.slice(0,10))}</code>`:''}</span>`
    :isNew?'<button type="button" class="kb-btn primary" data-kb-act="create">创建</button>'
    :`<button type="button" class="kb-btn quiet" data-kb-act="delete">删除</button><button type="button" class="kb-btn" data-kb-act="reject">驳回</button><button type="button" class="kb-btn" data-kb-act="save">保存</button><button type="button" class="kb-btn primary" data-kb-act="publish"${s.writeEnabled?'':' disabled title="先在「连接」中开启审核后写回"'}>写回知识库</button>`;
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
    if(!(await afConfirm({title:'确认写回知识库？',message:`来源：${v.source}\n路径：${v.path}\n\n将提交到知识库 Git 仓库并触发重建索引。`,confirmText:'写回',info:true})))return;
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
    if(act==='publish'){
      await save();
      const item=await api(`/api/knowledge/submissions/${id}/publish`,{method:'POST'});
      toast(`已写回知识库 · commit ${String(item.commit||'').slice(0,8)}`,'ok');
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

// ---------- 检索 ----------
function kbRenderSearch(body){
  const s=kb.settings||{};
  const q=kb.search;
  const allowed=(kb.sources||[]).filter(x=>x.allowed);
  if(q.sources.length&&!(q.sources.length===1&&allowed.some(x=>x.id===q.sources[0])))q.sources=[];
  const selected=q.sources[0]||'';
  const dis=s.ready?'':' disabled';
  body.innerHTML=kbPanel(`<form class="kb-search" id="kbSearchForm" role="search">${KB_SEARCH_ICON}<input type="search" name="q" aria-label="检索知识库" placeholder="问一个问题，结果与 Agent 看到的一致" autocomplete="off" value="${escapeHTML(q.query)}"${dis}><select name="source" aria-label="限定来源"${dis}><option value="">全部来源</option>${allowed.map(x=>`<option value="${escapeHTML(x.id)}"${x.id===selected?' selected':''}>${escapeHTML(x.id)}</option>`).join('')}</select></form>`,`<div id="kbHits">${kbHitsHTML()}</div>`,{label:'检索',headCls:'kb-panel-search'});
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
  if(!kb.settings?.ready)return '<div class="kb-empty">知识库未启用，请先在「连接」中完成配置</div>';
  if(q.loading)return '<div class="kb-empty">检索中…</div>';
  if(q.error)return `<div class="kb-empty">检索失败：${escapeHTML(q.error)}</div>`;
  if(!q.result)return kbRecentHTML();
  const r=q.result.result||{};
  const hits=r.results||[];
  const head=kbCaption(`${hits.length} 条结果 · ${Number(q.result.latencyMs)||0}ms${r.degraded?` · <span class="kb-warn" title="${escapeHTML(r.degrade_reason||'')}">语义检索已降级为词法检索</span>`:''}`);
  if(!hits.length)return head+'<div class="kb-empty">没有命中，换个关键词试试</div>';
  return head+hits.map((h,i)=>kbRow(`<div class="kb-row-title">${escapeHTML(h.title||h.path)}${h.heading?` <span class="kb-count">· ${escapeHTML(h.heading)}</span>`:''}</div><div class="kb-row-desc">${escapeHTML(h.snippet||'')}</div><cite>${escapeHTML(h.citation||`${h.source}:${h.path}#L${h.start_line}`)}</cite>`,'',{go:true,cls:'kb-hit',attrs:`data-kb-key="hit:${i}" data-kb-hit="${i}"`})).join('');
}

function kbUpdateHits(){const hits=$('#kbHits');if(hits){hits.innerHTML=kbHitsHTML();kbBindHits()}}

function kbBindHits(){
  $$('#kbHits [data-kb-hit]').forEach(el=>el.onclick=()=>kbOpenDocument(kb.search.result?.result?.results?.[Number(el.dataset.kbHit)]));
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
    const result=await api('/api/knowledge/search',{method:'POST',body:JSON.stringify({query:q.query,projectId:kbProjectId(),sources:q.sources})});
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
  const seq=kbDrawerOpen(hit.title||hit.path,`<span class="mono">${escapeHTML(hit.citation||`${hit.source}:${hit.path}`)}</span>`,'<div class="kb-empty">正在读取原文…</div>');
  try{
    const doc=await api(`/api/knowledge/document?source=${encodeURIComponent(hit.source)}&path=${encodeURIComponent(hit.path)}&projectId=${encodeURIComponent(kbProjectId())}`);
    if(seq!==kb.drawer.seq)return;
    const host=$('#kbDrawerBody');
    host.innerHTML=`<div class="kb-doc">${(doc.lines||[]).map(l=>`<div class="${l.number>=hit.start_line&&l.number<=hit.end_line?'hl':''}"${l.number===hit.start_line?' id="kbDocAnchor"':''}><span>${l.number}</span><code>${escapeHTML(l.text)}</code></div>`).join('')}</div>`;
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
