function renderProjectSwitcher(){
  const projects=state.data.projects||[];
  const switcher=$('#projectSwitcher');if(!switcher)return;
  switcher.innerHTML=projects.map(p=>`<option value="${escapeHTML(p.id)}" ${p.id===state.projectFilter?'selected':''}>${escapeHTML(p.key||'PROJECT')} · ${escapeHTML(p.name)}</option>`).join('')||'<option value="">暂无项目</option>';
  window.enhanceSelect?.(switcher);
  if(!projects.some(p=>p.id===state.projectFilter))state.projectFilter=defaultProjectId(projects);
}
function renderProjectContext(projectIssues){
  const projects=state.data.projects||[],project=projects.find(p=>p.id===state.projectFilter);renderProjectSwitcher();
  $('#projectContextMark').textContent=(project?.key||project?.name||'P').slice(0,2).toUpperCase();$('#projectContextMark').style.background=safeColor(project?.color);
  $('#projectContextName').textContent=project?.name||'暂无项目';$('#projectContextDescription').textContent=project?.description||'工作台数据会按项目整体切换';
  const active=projectIssues.filter(i=>issueRunHealth(i)==='active').length,attention=projectIssues.filter(i=>issueRunHealth(i)==='attention').length,pendingMRs=projectIssues.reduce((n,i)=>n+pendingDeliveryMRCount(i),0),repo=(state.data.repositories||[]).find(r=>r.id===project?.defaultRepositoryId);
  const repoCount=(state.data.repositories||[]).filter(r=>r.projectId===project?.id).length;
  $('#projectContextMetrics').innerHTML=`<button type="button" data-issue-filter="" class="${!state.issueRunFilter?'on':''}"><span>工作项</span><b>${projectIssues.length}</b></button><button type="button" data-issue-filter="active" class="${state.issueRunFilter==='active'?'on':''}"><span>正在推进</span><b>${active}</b></button><button type="button" data-issue-filter="attention" class="${attention?'danger':''} ${state.issueRunFilter==='attention'?'on':''}" title="点此只看待确认澄清结论、门禁失败、运行失败或已中断的工作项"><span>需要介入</span><b>${attention}</b></button><button type="button" data-issue-filter="mr" class="${state.issueRunFilter==='mr'?'on':''}" title="已提 MR 但还没合入的 MR 数（按 MR 计，点击只看涉及的需求）"><span>待处理 MR</span><b>${pendingMRs}</b></button><div class="project-default-sop"><span>代码仓库（${repoCount}）</span><b title="${escapeHTML(repo?.gongfengProjectId||'未绑定')}">默认：${escapeHTML(repo?.name||'未绑定')}</b></div>`;
  $('#projectContextMetrics').querySelectorAll('[data-issue-filter]').forEach(btn=>btn.onclick=()=>setIssueRunFilter(btn.dataset.issueFilter));
}
function clarificationNeedsConfirm(i){
  const c=i?.clarification||{};
  if(c.status!=='ready')return false;
  if(c.writeBackStatus!=='pending')return false;
  return (i.source==='tapd'||i.source==='gongfeng')&&!!i.externalRef;
}
function userCombobox(name,selected,emptyLabel='请选择用户',attributes=''){
  const current=userById(selected),label=current?userDisplay(current):emptyLabel;
  const options=(state.data.users||[]).map(u=>`<button type="button" class="user-combobox-option${Number(u.id)===Number(selected)?' selected':''}" data-user-option="${u.id}" data-search="${escapeHTML(`${u.loginName||''} ${u.chineseName||''}`.toLowerCase())}"><span>${escapeHTML(userDisplay(u))}</span>${Number(u.id)===Number(selected)?'<i>✓</i>':''}</button>`).join('');
  return `<div class="user-combobox" data-user-combobox><input type="hidden" name="${escapeHTML(name)}" value="${selected?escapeHTML(String(selected)):''}" ${attributes}><button type="button" class="user-combobox-trigger" data-user-combobox-trigger aria-haspopup="listbox" aria-expanded="false"><span>${escapeHTML(label)}</span><i>⌄</i></button><div class="user-combobox-panel" data-user-combobox-panel hidden><input type="search" data-user-combobox-search placeholder="搜索中文名 / 英文名" autocomplete="off"><div class="user-combobox-options" role="listbox">${options}</div><p class="user-combobox-empty" hidden>没有匹配的用户</p></div></div>`;
}
function bindUserSearches(root=document){
  if(!document.body.dataset.userComboboxBound){document.body.dataset.userComboboxBound='true';document.addEventListener('click',()=>document.querySelectorAll('[data-user-combobox].is-open').forEach(box=>{box.classList.remove('is-open');const panel=box.querySelector('[data-user-combobox-panel]'),trigger=box.querySelector('[data-user-combobox-trigger]');if(panel)panel.hidden=true;if(trigger)trigger.setAttribute('aria-expanded','false')}))}
  root.querySelectorAll('[data-user-combobox]').forEach(box=>{
    const value=box.querySelector('input[type="hidden"]'),trigger=box.querySelector('[data-user-combobox-trigger]'),panel=box.querySelector('[data-user-combobox-panel]'),search=box.querySelector('[data-user-combobox-search]'),empty=box.querySelector('.user-combobox-empty');
    const close=()=>{panel.hidden=true;box.classList.remove('is-open');trigger.setAttribute('aria-expanded','false')};
    trigger.onclick=e=>{e.stopPropagation();document.querySelectorAll('[data-user-combobox].is-open').forEach(other=>{if(other!==box){other.classList.remove('is-open');const p=other.querySelector('[data-user-combobox-panel]');if(p)p.hidden=true;other.querySelector('[data-user-combobox-trigger]')?.setAttribute('aria-expanded','false')}});const opening=panel.hidden;panel.hidden=!opening;box.classList.toggle('is-open',opening);trigger.setAttribute('aria-expanded',String(opening));if(opening){search.value='';box.querySelectorAll('[data-user-option]').forEach(o=>o.hidden=false);empty.hidden=true;setTimeout(()=>search.focus(),0)}};
    trigger.addEventListener('click',()=>{if(panel.hidden)return;const rect=trigger.getBoundingClientRect(),below=window.innerHeight-rect.bottom-14,above=rect.top-14,up=below<240&&above>below,height=Math.min(250,Math.max(100,up?above:below));Object.assign(panel.style,{position:'fixed',left:rect.left+'px',right:'auto',width:rect.width+'px',top:up?'auto':(rect.bottom+5)+'px',bottom:up?(window.innerHeight-rect.top+5)+'px':'auto'});box.querySelector('.user-combobox-options').style.maxHeight=Math.max(50,height-55)+'px'});
    panel.onclick=e=>e.stopPropagation();search.onclick=e=>e.stopPropagation();
    search.oninput=()=>{const query=search.value.trim().toLowerCase();let visible=0;box.querySelectorAll('[data-user-option]').forEach(option=>{option.hidden=Boolean(query&&!String(option.dataset.search||'').includes(query));if(!option.hidden)visible++});empty.hidden=visible>0};
    box.querySelectorAll('[data-user-option]').forEach(option=>option.onclick=e=>{e.stopPropagation();value.value=option.dataset.userOption;trigger.querySelector('span').textContent=option.querySelector('span').textContent;box.querySelectorAll('[data-user-option]').forEach(o=>{const selected=o===option;o.classList.toggle('selected',selected);o.setAttribute('aria-selected',String(selected));o.querySelector('i')?.remove();if(selected){const mark=document.createElement('i');mark.textContent='✓';o.append(mark)}});const avatar=box.closest('.member-row')?.querySelector('.member-avatar'),user=userById(value.value);if(avatar)avatar.innerHTML=userAvatarHTML(user,48);value.dispatchEvent(new Event('change',{bubbles:true}));close()});
    box.addEventListener('keydown',e=>{if(e.key==='Escape')close()});
  });
}
// Project admin has two levels: #/projects lists cards, #/projects/<id>[/<tab>] edits one project.
const PROJECT_ADMIN_TABS=[{id:'basic',label:'基本信息'},{id:'repos',label:'代码仓库'},{id:'members',label:'项目成员'},{id:'tapd',label:'高级设置'}];
// TAPD is a legacy tracker: its settings appear only once the platform has a TAPD integration or the project already uses one.
function tapdIntegrationAvailable(project){
  if(project&&(String(project.tapdWorkspaceId||'').trim()||String(project.tapdIterationId||'').trim()))return true;
  if(String(state.data?.syncConfig?.tapdMcpUrl||'').trim())return true;
  return (state.data?.mcpServers||[]).some(s=>['70','tapd'].includes(String(s.marketId||'')));
}
const PROJECT_SWATCHES=['#536dfe','#5b8cff','#7c3aed','#0ea5e9','#10b981','#f59e0b','#ef4444','#ec4899'];
const PA_ICON_PATHS={
  plus:'<path d="M12 5v14M5 12h14"/>',
  arrow:'<path d="m9 6 6 6-6 6"/>',
  search:'<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>',
  repo:'<circle cx="6" cy="6" r="2.3"/><circle cx="6" cy="18" r="2.3"/><circle cx="18" cy="8" r="2.3"/><path d="M6 8.3v7.4M18 10.3c0 3.4-4.5 3.6-9.8 6"/>',
  flow:'<rect x="3" y="4" width="6" height="6" rx="1.5"/><rect x="15" y="14" width="6" height="6" rx="1.5"/><path d="M9 7h4a2 2 0 0 1 2 2v5"/>',
  alert:'<path d="M12 9v4M12 17h.01"/><path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"/>',
  user:'<circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/>',
  tapd:'<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 9h18M8 4v5"/>',
  edit:'<path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/>',
  trash:'<path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6"/>',
  branch:'<circle cx="6" cy="5" r="2"/><circle cx="6" cy="19" r="2"/><circle cx="18" cy="7" r="2"/><path d="M6 7v10M18 9c0 4-6 4-11 8"/>',
  merge:'<circle cx="6" cy="5" r="2"/><circle cx="18" cy="19" r="2"/><path d="M6 7v12M6 11c0 4 4 8 10 8"/>',
  ext:'<path d="M14 4h6v6M20 4l-9 9M19 14v6H4V5h6"/>',
  x:'<path d="M18 6 6 18M6 6l12 12"/>'
};
function paIcon(name,size=14){return `<svg width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${PA_ICON_PATHS[name]||''}</svg>`}

function projectIdentityWords(name){return String(name||'').replace(/([a-z0-9])([A-Z])/g,'$1 $2').replace(/([A-Z]+)([A-Z][a-z])/g,'$1 $2').match(/[A-Za-z0-9]+/g)||[]}
// Without a pinyin library a name with no latin letters falls back to proj-<seed> / PRJ.
function generateProjectIdentity(name,projects=[],seed=''){
  const words=projectIdentityWords(name),alpha=words.map(w=>w.replace(/[^A-Za-z]/g,'')).filter(Boolean);
  const taken=new Set(['new',...(projects||[]).map(p=>String(p?.id||'').toLowerCase())]);
  const keys=new Set((projects||[]).map(p=>String(p?.key||'').toUpperCase()));
  let id,key;
  if(alpha.length){
    id=words.join('-').toLowerCase();
    key=(alpha.length>1?alpha.map(w=>w[0]).join(''):alpha[0].length<=6?alpha[0]:alpha[0].slice(0,3)).toUpperCase().slice(0,6);
  }else{
    const s=String(seed||'').toLowerCase().replace(/[^a-z0-9]/g,'').slice(0,4)||Math.random().toString(36).slice(2,6);
    id='proj-'+s;key='PRJ';
  }
  const baseId=id;for(let n=2;taken.has(id);n++)id=`${baseId}-${n}`;
  const letter=i=>String.fromCharCode(65+i),baseKey=key.slice(0,5),longKey=key.slice(0,4);
  for(let i=0;keys.has(key)&&i<26;i++)key=baseKey+letter(i);
  for(let i=0;keys.has(key)&&i<676;i++)key=longKey+letter(Math.floor(i/26))+letter(i%26);
  return {id,key};
}
function filterAdminProjects(projects,repos,query,filter,userId){
  const q=String(query||'').trim().toLowerCase(),uid=Number(userId)||0;
  return (projects||[]).filter(p=>{
    const own=(repos||[]).filter(r=>r.projectId===p.id);
    if(filter==='setup'&&own.length)return false;
    if(filter==='mine'&&!(uid&&(Number(p.ownerId)===uid||(p.members||[]).some(m=>Number(m.userId)===uid))))return false;
    if(!q)return true;
    return [p.name,p.id,p.key,...own.flatMap(r=>[r.name,r.gongfengProjectId,r.webUrl])].some(v=>String(v||'').toLowerCase().includes(q));
  });
}
function parseProjectAdminRoute(hash){
  const parts=String(hash||'').replace(/^#\/?/,'').split('?')[0].split('/').filter(Boolean);
  if(parts[0]!=='projects')return null;
  let id=parts[1]||'';try{id=decodeURIComponent(id)}catch{}
  const tab=id!=='new'&&PROJECT_ADMIN_TABS.some(t=>t.id===parts[2])?parts[2]:'basic';
  return {id,tab};
}
function projectAdminHash(id,tab){return id?`#/projects/${encodeURIComponent(id)}${id!=='new'&&tab&&tab!=='basic'?'/'+tab:''}`:'#/projects'}
function projectPeopleIds(p){return [...new Set([p?.ownerId,...(p?.members||[]).map(m=>m.userId)].map(Number).filter(Boolean))]}
function blankProjectDraft(){return {id:'',key:'',name:'',description:'',color:PROJECT_SWATCHES[0],ownerId:Number(state.currentUser?.id)||'',workflowId:'generic-backend-delivery',defaultRepositoryId:'',businessDomainId:'',members:[],runEval:defaultRunEval(),tapdWorkspaceId:'',tapdIterationId:'',tapdClear:false}}
function projectDraftFrom(project){const d=clone(project)||{};d.members=(d.members||[]).filter(m=>Number(m.userId)!==Number(d.ownerId)).map(m=>({userId:Number(m.userId)||0,role:'member'}));d.tapdClear=false;return d}
function projectDraftSignature(d){
  d=d||{};
  return JSON.stringify({id:String(d.id||''),key:String(d.key||'').trim().toUpperCase(),name:String(d.name||'').trim(),description:String(d.description||''),color:String(d.color||'').toLowerCase(),ownerId:Number(d.ownerId)||0,workflowId:String(d.workflowId||''),defaultRepositoryId:String(d.defaultRepositoryId||''),businessDomainId:String(d.businessDomainId||''),tapdWorkspaceId:String(d.tapdWorkspaceId||'').trim(),tapdIterationId:String(d.tapdIterationId||'').trim(),tapdClear:!!d.tapdClear,members:(d.members||[]).map(m=>Number(m.userId)||0)});
}
function projectDraftDirty(draft,base){return !!draft&&!!base&&projectDraftSignature(draft)!==projectDraftSignature(base)}
function projectAdminDirty(){return !!state.projectAdminDetailId&&state.projectDraftFor===state.projectAdminDetailId&&projectDraftDirty(state.projectDraft,state.projectDraftBase)}
function projectWorkflowName(id){const w=(state.data?.workflows||[]).find(x=>x.id===id);return w?`${w.generic?'通用 · ':''}${w.name}`:''}

function projectCardHTML(p,ctx={}){
  const repos=(ctx.repos||[]).filter(r=>r.projectId===p.id),issues=(ctx.issues||[]).filter(i=>i.projectId===p.id);
  const active=issues.filter(i=>issueRunHealth(i)==='active').length,people=projectPeopleIds(p),def=repos.find(r=>r.id===p.defaultRepositoryId);
  const key=String(p.key||p.name||'P');
  const repoFact=repos.length
    ?`<span class="pa-fact" title="${def?'默认仓库':'已绑定仓库，未设默认'}">${paIcon('repo',12)}<span>${escapeHTML((def||repos[0]).name||'仓库')}${repos.length>1?` +${repos.length-1}`:''}</span></span>`
    :`<span class="pa-fact warn">${paIcon('alert',12)}<span>未绑定代码仓库</span></span>`;
  const sop=ctx.workflowName?ctx.workflowName(p.workflowId):'';
  const avatars=people.slice(0,3).map(id=>{const u=userById(id);return `<span class="pa-av" title="${escapeHTML(u?userDisplay(u):String(id))}">${userAvatarHTML(u||id,48)}</span>`}).join('')+(people.length>3?`<span class="pa-av more">+${people.length-3}</span>`:'');
  return `<article class="pa-card" tabindex="0" role="link" data-project-open="${escapeHTML(p.id)}" aria-label="打开项目 ${escapeHTML(p.name||p.id)}">
    <div class="pa-card-top"><span class="pa-mark" style="background:${safeColor(p.color)}">${escapeHTML(key.slice(0,2).toUpperCase())}</span><div class="pa-card-m"><h3><span title="${escapeHTML(p.name||'')}">${escapeHTML(p.name||p.id)}</span>${p.id===ctx.currentId?'<span class="pa-chip cur">当前</span>':''}</h3><div class="pa-card-sub" title="${escapeHTML(p.id)} · ${escapeHTML(p.key||'')}">${escapeHTML(p.id)} · ${escapeHTML(p.key||'—')}</div></div><span class="pa-go">${paIcon('arrow',16)}</span></div>
    <p class="pa-card-desc${p.description?'':' none'}">${escapeHTML(p.description||'暂无项目说明')}</p>
    <div class="pa-facts">${repoFact}${sop?`<span class="pa-fact" title="默认 SOP">${paIcon('flow',12)}<span>${escapeHTML(sop)}</span></span>`:''}${p.tapdWorkspaceId?`<span class="pa-fact" title="TAPD Workspace ${escapeHTML(p.tapdWorkspaceId)}">${paIcon('tapd',12)}<span>TAPD</span></span>`:''}</div>
    <div class="pa-card-foot"><div class="pa-avs">${avatars}</div><div class="pa-stats"><span><b>${issues.length}</b> 需求</span>${active?`<span class="pa-live"><i></i><b>${active}</b> 推进中</span>`:''}</div></div>
  </article>`;
}
function projectAdminEmptyHTML(icon,title,text,action){return `<div class="pa-empty"><div class="pa-empty-ic">${paIcon(icon,22)}</div><b>${title}</b><p>${text}</p>${action||''}</div>`}
function renderProjectList(){
  const host=$('#projectAdminList');if(!host)return;
  const projects=state.data.projects||[],repos=state.data.repositories||[];
  const filter=state.projectAdminFilter||'all',query=state.projectAdminQuery||'';
  $('#projectAdminCount').textContent=projects.length;
  $$('#projectAdminFilters [data-project-filter]').forEach(b=>{const on=b.dataset.projectFilter===filter;b.classList.toggle('on',on);b.setAttribute('aria-selected',String(on))});
  const search=$('#projectAdminSearch');if(search&&document.activeElement!==search&&search.value!==query)search.value=query;
  const list=filterAdminProjects(projects,repos,query,filter,state.currentUser?.id);
  if(!projects.length)host.innerHTML=projectAdminEmptyHTML('repo','还没有项目','项目是需求、仓库与团队的统一边界，先创建第一个项目。','<button type="button" class="button primary" data-project-new>＋ 新建项目</button>');
  else if(!list.length)host.innerHTML=projectAdminEmptyHTML('search','没有匹配的项目','换个关键词试试，或者新建一个项目。','<button type="button" class="button ghost" data-project-clear>清除筛选</button>');
  else host.innerHTML=`<div class="pa-grid">${list.map(p=>projectCardHTML(p,{repos,issues:state.data.issues||[],currentId:state.projectFilter,workflowName:projectWorkflowName})).join('')}</div>`;
  host.querySelectorAll('[data-project-open]').forEach(card=>{card.onclick=()=>openProjectAdminDetail(card.dataset.projectOpen);card.onkeydown=e=>{if(e.key==='Enter'||e.key===' '){e.preventDefault();openProjectAdminDetail(card.dataset.projectOpen)}}});
  host.querySelectorAll('[data-project-new]').forEach(b=>b.onclick=()=>openProjectWizard());
  host.querySelectorAll('[data-project-clear]').forEach(b=>b.onclick=()=>{state.projectAdminQuery='';state.projectAdminFilter='all';if(search)search.value='';renderProjectList()});
}

function paRow(label,desc,control,opt={}){return `<div class="pa-row${opt.stack?' stack':''}"${opt.attrs||''}><div class="pa-row-copy"><div class="pa-row-lbl">${label}${opt.required?'<em>*</em>':''}</div>${desc?`<div class="pa-row-desc"${opt.descAttr||''}>${desc}</div>`:''}</div><div class="pa-row-ctl">${control}</div></div>`}
function paSection(title,desc,body,action=''){return `<div class="pa-sec"><div class="pa-sec-h"><div><h3>${title}</h3>${desc?`<p>${desc}</p>`:''}</div>${action}</div>${body}</div>`}
function projectIdentDesc(creating,custom){return creating?(custom?'标识用于接口与链接，创建后不可修改；Key 是需求编号前缀与头像文字，最多 6 位。':'根据项目名称自动生成，一般无需修改。'):'标识用于接口与链接，创建后不可修改。'}
function projectIdentHTML(d,creating,custom){
  return `<div class="pa-ident" data-pa-ident-view${custom?' hidden':''}><code data-pa-ident-id class="${d.id?'':'ph'}" title="${escapeHTML(d.id||'')}">${escapeHTML(d.id||'输入名称后生成')}</code><span class="pa-ident-sep">·</span><code data-pa-ident-key class="${d.key?'':'ph'}">${escapeHTML(String(d.key||'—').toUpperCase())}</code><button type="button" class="pa-link" data-pa-ident-edit>${creating?'自定义':'修改 Key'}</button></div>
    <div class="pa-ident-edit" data-pa-ident-form${custom?'':' hidden'}><label><span>标识</span><input class="pa-input mono" name="id" value="${escapeHTML(d.id||'')}" required ${creating?'':'readonly'} spellcheck="false" placeholder="web-console"></label><label><span>Key</span><input class="pa-input mono" name="key" value="${escapeHTML(d.key||'')}" required maxlength="6" spellcheck="false" placeholder="DC"></label><button type="button" class="pa-link" ${creating?'data-pa-ident-auto>恢复自动':'data-pa-ident-collapse>收起'}</button></div>`;
}
function projectColorHTML(color){
  const cur=safeColor(color),preset=PROJECT_SWATCHES.includes(String(cur).toLowerCase());
  return `<div class="pa-swatches">${PROJECT_SWATCHES.map(c=>`<button type="button" class="pa-sw${c===String(cur).toLowerCase()?' on':''}" data-pa-color="${c}" style="background:${c};color:${c}" aria-label="品牌色 ${c}" aria-pressed="${c===String(cur).toLowerCase()}"></button>`).join('')}<label class="pa-sw pa-sw-custom${preset?'':' on'}" title="自定义颜色" style="color:${cur}${preset?'':`;background:${cur}`}"><input type="color" data-pa-color-custom value="${cur.length===7?cur:'#536dfe'}" aria-label="自定义品牌色"></label><code class="pa-sw-hex" data-pa-color-hex>${escapeHTML(String(cur).toUpperCase())}</code><input type="hidden" name="color" value="${escapeHTML(cur)}"></div>`;
}
function repoCardHTML(r,isDefault){
  const info=parseGongfengRepoURL(r.provider==='ssh'?r.cloneUrl:r.webUrl||r.cloneUrl||r.gongfengProjectId||''),path=info?.project||r.gongfengProjectId||'',web=r.provider==='ssh'?r.webUrl||'':info?.webUrl||'',name=r.name||info?.name||path||r.id;
  const updated=typeof knownTime==='function'?knownTime(r.updatedAt):null;
  return `<article class="pa-rc" data-repo-card="${escapeHTML(r.id)}">
    <div class="pa-rc-top"><span class="pa-rc-ic">${paIcon('repo',15)}</span><div class="pa-rc-m"><h4 title="${escapeHTML(name)}">${escapeHTML(name)}</h4><code title="${escapeHTML(repoDisplayPath(info,path))}">${escapeHTML(repoDisplayPath(info,path))}</code></div><div class="pa-rc-acts"><button type="button" class="pa-icon-btn" data-repo-edit="${escapeHTML(r.id)}" title="编辑" aria-label="编辑仓库 ${escapeHTML(name)}">${paIcon('edit')}</button><button type="button" class="pa-icon-btn danger" data-repo-delete="${escapeHTML(r.id)}" title="删除" aria-label="删除仓库 ${escapeHTML(name)}">${paIcon('trash')}</button></div></div>
    <div class="pa-tags"><span class="pa-tag" title="主分支">${paIcon('branch',11)}${escapeHTML(r.defaultBranch||'main')}</span>${r.integrateBranch?`<span class="pa-tag" title="集成分支">${paIcon('merge',11)}${escapeHTML(r.integrateBranch)}</span>`:'<span class="pa-tag muted">未配置集成分支</span>'}${isDefault?'<span class="pa-tag def">默认</span>':''}</div>
    <div class="pa-rc-foot"><small>${updated?`更新于 ${relativeTime(updated)}`:'&nbsp;'}</small>${web?`<a class="pa-link outline" href="${escapeHTML(web)}" target="_blank" rel="noopener noreferrer">${repoProviderLabel(info)} ${paIcon('ext',11)}</a>`:''}</div>
  </article>`;
}
function projectRepoListHTML(repos,defaultId){
  if(!repos.length)return projectAdminEmptyHTML('repo','还没有绑定代码仓库','绑定后，Agent 执行 SOP 时会自动克隆仓库并在工作分支上提交。第一个仓库会自动设为默认。',`<ol class="pa-steps"><li><span>1</span>粘贴 GitHub 链接或 Git SSH 地址</li><li><span>2</span>确认分支</li><li><span>3</span>保存即生效</li></ol><button type="button" class="button primary" data-repo-add>＋ 添加第一个仓库</button>`);
  return `<div class="pa-rgrid">${repos.map(r=>repoCardHTML(r,r.id===defaultId)).join('')}<button type="button" class="pa-rc-add" data-repo-add><i>${paIcon('plus',14)}</i><b>添加仓库</b></button></div>`;
}
function projectAdminFormHTML(d,ctx){
  const {creating,custom,repos,saved}=ctx;
  const defaultRepo=repos.some(r=>r.id===d.defaultRepositoryId)?d.defaultRepositoryId:'';
  const repoOptions=repos.map(r=>`<option value="${escapeHTML(r.id)}" ${r.id===defaultRepo?'selected':''}>${escapeHTML(r.name||r.gongfengProjectId||r.id)}</option>`).join('');
  const repoDesc=creating?'保存项目后，在「代码仓库」中添加。':repos.length?'只能从「代码仓库」中已绑定的仓库里选。':'还没有仓库，可在「代码仓库」中添加。';
  const basic=paSection('项目身份','项目在工作台、需求和通知里的显示方式。',`<div class="pa-rows">
      ${paRow('项目名称','显示在项目列表与工作台顶部。',`<input class="pa-input" name="name" value="${escapeHTML(d.name||'')}" required maxlength="80" placeholder="例如 Web Console">`,{required:true})}
      ${paRow('标识与 Key',projectIdentDesc(creating,custom),projectIdentHTML(d,creating,custom),{descAttr:' data-pa-ident-desc'})}
      ${paRow('品牌色','用于项目头像和标签。',projectColorHTML(d.color))}
      ${paRow('项目说明','可选，简要说明项目范围，Agent 会把它作为上下文。',`<textarea class="pa-input" name="description" rows="3" placeholder="这个项目负责什么？">${escapeHTML(d.description||'')}</textarea>`,{stack:true})}
    </div>`)+paSection('默认设置','新建需求时自动带上的流程与仓库。',`<div class="pa-rows">
      ${creating&&!Number(state.currentUser?.id)?paRow('项目 Owner','项目的默认责任人，创建后可在「项目成员」中调整。',userCombobox('ownerId',d.ownerId,'请选择项目 Owner'),{required:true}):''}
      ${paRow('默认 SOP','用于新需求的默认流程；已关联 SOP 的需求保留原绑定。',projectDefaultSOPField(d),{required:true})}
      ${paRow('默认代码仓库',repoDesc,`<select name="defaultRepositoryId" class="select" ${repos.length?'':'disabled'}><option value="">暂未绑定</option>${repoOptions}</select>`)}
    </div>`);
  const owner=userById(d.ownerId);
  const members=(d.members||[]).map((m,i)=>{const u=userById(m.userId);return `<div class="member-row pa-member-row" data-member-row="${i}"><span class="member-avatar">${userAvatarHTML(u,48)}</span><span class="member-kind">成员</span>${userCombobox(`member-${i}`,m.userId,'请选择项目成员','data-m="userId"')}<button type="button" data-remove-member="${i}" title="移除成员" aria-label="移除成员">×</button></div>`}).join('');
  const memberPanel=paSection('负责人','Owner 是项目的默认责任人，接收需要介入的通知。',`<div class="pa-rows"><div class="member-row owner-row pa-member-row"><span class="member-avatar owner">${userAvatarHTML(owner,48)}</span><span class="member-kind owner">Owner</span>${userCombobox('ownerId',d.ownerId,'请选择项目 Owner')}<span></span></div></div>`)
    +paSection('成员','其他成员无需设置角色，可查看和推进项目内所有需求。',`<div class="pa-rows" id="memberRows">${members||'<div class="pa-rows-empty">还没有其他成员</div>'}</div>`,'<button id="addMember" type="button" class="button ghost small">＋ 添加成员</button>');
  const configured=!!(saved?.tapdWorkspaceId||saved?.tapdIterationId);
  const tapdPanel=(tapdIntegrationAvailable(saved)?paSection('TAPD 需求池','配置后可在需求页从 TAPD 导入需求，澄清结论会回写到 TAPD 单。',`<div class="pa-rows">
      ${paRow('Workspace ID','TAPD 项目 URL 中 /tapd_fe/ 后的数字。',`<input class="pa-input mono" name="tapdWorkspaceId" value="${escapeHTML(d.tapdWorkspaceId||'')}" placeholder="例如 20375341" spellcheck="false">`)}
      ${paRow('Iteration ID','可选，只导入该迭代内的需求。',`<input class="pa-input mono" name="tapdIterationId" value="${escapeHTML(d.tapdIterationId||'')}" placeholder="留空导入全部" spellcheck="false">`)}
    </div>`):'')+paSection('危险操作','',`<div class="pa-rows">${projectDeleteRow(saved)}${configured?paRow(`清除已保存的 TAPD 配置${configured?' <span class="pa-chip">当前已配置</span>':''}`,'保存项目设置时同时清空 Workspace 与 Iteration，已导入的需求不受影响。',`<label class="pa-switch-wrap"><input type="checkbox" name="tapdClear" role="switch" ${d.tapdClear?'checked':''} aria-label="清除已保存的 TAPD 配置"><span class="pa-switch danger"></span></label>`,{attrs:' data-pa-row-inline'}):''}</div>`);
  const panels={basic,repos:paSection('代码仓库','Agent 执行 SOP 时会克隆这些仓库。仓库保存后立即生效，无需点「保存项目设置」。',`<div data-pa-repo-box>${projectRepoListHTML(repos,saved?.defaultRepositoryId||'')}</div>`,repos.length?'<button type="button" class="button ghost small" data-repo-add>＋ 添加仓库</button>':''),members:memberPanel,tapd:tapdPanel};
  const tab=state.projectAdminTab||'basic';
  return PROJECT_ADMIN_TABS.map(t=>`<section class="pa-panel" data-pa-panel="${t.id}" id="paPanel-${t.id}" role="tabpanel" aria-labelledby="paTab-${t.id}"${t.id===tab?'':' hidden'}>${creating&&t.id!=='basic'?'':panels[t.id]}</section>`).join('')
    +`<div class="pa-savebar-dock"><div class="pa-savebar" data-pa-savebar inert><span class="pa-savebar-tx"><i></i><span data-pa-savebar-text>有未保存的修改</span></span><button type="button" class="button ghost" data-pa-discard>放弃修改</button><button type="submit" class="button primary" data-pa-submit>保存项目设置</button></div></div>`;
}
function renderProjectAdminHead(){
  const d=state.projectDraft||{},creating=state.projectAdminDetailId==='new',saved=(state.data.projects||[]).find(p=>p.id===state.projectAdminDetailId);
  const name=String(d.name||'').trim()||(creating?'新项目':saved?.name||''),key=String(d.key||'').trim().toUpperCase();
  const crumb=$('#projectAdminCrumb');if(crumb){crumb.textContent=creating?'新建项目':name;crumb.title=crumb.textContent}
  const head=$('#projectAdminHead');if(!head)return;
  const issues=(state.data.issues||[]).filter(i=>!creating&&i.projectId===state.projectAdminDetailId),active=issues.filter(i=>issueRunHealth(i)==='active').length,sop=projectWorkflowName(d.workflowId);
  head.innerHTML=`<span class="pa-mark lg" style="background:${safeColor(d.color)}">${escapeHTML((key||(creating?'NEW':name)||'P').slice(0,2).toUpperCase())}</span><div class="pa-dhd-m"><h1><span title="${escapeHTML(name)}">${escapeHTML(name)}</span>${key?`<span class="pa-chip">${escapeHTML(key)}</span>`:''}${!creating&&saved?.id===state.projectFilter?'<span class="pa-chip cur">当前工作台项目</span>':''}</h1><p>${escapeHTML(creating?'填写项目名称并创建，之后即可绑定代码仓库、邀请成员。':(d.description||''))}</p>${creating?'':`<div class="pa-dmeta"><span>${paIcon('user',12)}<b>${projectPeopleIds(d).length}</b> 位成员</span><span><b>${issues.length}</b> 个需求</span><span><b>${active}</b> 个推进中</span>${sop?`<span>${paIcon('flow',12)}${escapeHTML(sop)}</span>`:''}</div>`}</div>`;
}
function syncProjectAdminTabs(){
  const bar=$('#projectAdminTabs');if(!bar)return;
  const creating=state.projectAdminDetailId==='new',d=state.projectDraft||{},tab=state.projectAdminTab||'basic',errors=state.projectAdminErrors||{};
  const repoCount=creating?0:(state.data.repositories||[]).filter(r=>r.projectId===state.projectAdminDetailId).length;
  if(!bar.querySelector('.pa-tab-ind')){
    bar.innerHTML=PROJECT_ADMIN_TABS.map(t=>`<button type="button" role="tab" class="pa-tab" id="paTab-${t.id}" data-pa-tab="${t.id}" aria-controls="paPanel-${t.id}">${t.label}<span class="pa-tab-n" hidden></span><span class="pa-tab-dot" hidden></span></button>`).join('')+'<span class="pa-tab-ind" aria-hidden="true"></span>';
    bar.querySelectorAll('[data-pa-tab]').forEach(b=>b.onclick=()=>setProjectAdminTab(b.dataset.paTab));
    bar.onkeydown=e=>{if(!['ArrowLeft','ArrowRight'].includes(e.key))return;const ids=PROJECT_ADMIN_TABS.map(t=>t.id),i=ids.indexOf(state.projectAdminTab||'basic'),next=ids[(i+(e.key==='ArrowRight'?1:ids.length-1))%ids.length];if(setProjectAdminTab(next)!==false)bar.querySelector(`[data-pa-tab="${next}"]`)?.focus()};
  }
  bar.querySelectorAll('[data-pa-tab]').forEach(b=>{
    const id=b.dataset.paTab,on=id===tab,locked=creating&&id!=='basic',n=b.querySelector('.pa-tab-n'),dot=b.querySelector('.pa-tab-dot');
    b.classList.toggle('on',on);b.classList.toggle('locked',locked);b.setAttribute('aria-selected',String(on));b.tabIndex=on?0:-1;
    if(locked){b.setAttribute('aria-disabled','true');b.title='保存项目后可用'}else{b.removeAttribute('aria-disabled');b.removeAttribute('title')}
    const count=id==='repos'?repoCount:id==='members'?projectPeopleIds(d).length:null;
    n.hidden=creating||count==null;n.textContent=count==null?'':String(count);dot.hidden=!errors[id];
  });
  moveProjectAdminTabIndicator();
}
function moveProjectAdminTabIndicator(){
  const bar=$('#projectAdminTabs'),a=bar?.querySelector('.pa-tab.on'),ind=bar?.querySelector('.pa-tab-ind');
  if(!a||!ind||!a.offsetWidth)return;
  ind.style.width=Math.max(0,a.offsetWidth-16)+'px';ind.style.transform=`translateX(${a.offsetLeft+8}px)`;
}
function setProjectAdminTab(tab,opt={}){
  if(!PROJECT_ADMIN_TABS.some(t=>t.id===tab))tab='basic';
  if(state.projectAdminDetailId==='new'&&tab!=='basic'){if(!opt.silent)toast('保存项目后可用','warn');return false}
  state.projectAdminTab=tab;
  $$('#projectAdminForm [data-pa-panel]').forEach(p=>p.hidden=p.dataset.paPanel!==tab);
  syncProjectAdminTabs();
  if(!opt.silent&&activeViewName()==='projects'){const next=projectAdminHash(state.projectAdminDetailId,tab);if(location.hash!==next)history.replaceState(history.state,'',next)}
  return true;
}
function syncProjectAdminSaveBar(){
  const bar=$('#projectAdminForm [data-pa-savebar]');if(!bar)return;
  const creating=state.projectAdminDetailId==='new',show=creating||projectAdminDirty();
  bar.classList.toggle('show',show);bar.inert=!show;
  bar.querySelector('[data-pa-savebar-text]').textContent=creating?'新项目尚未保存':'有未保存的修改';
  bar.querySelector('[data-pa-discard]').textContent=creating?'取消':'放弃修改';
  bar.querySelector('[data-pa-submit]').textContent=creating?'创建项目':'保存项目设置';
}
function syncProjectIdentity(){
  const form=$('#projectAdminForm'),d=state.projectDraft;if(!form||!d)return;
  const creating=state.projectAdminDetailId==='new',custom=!!state.projectIdentCustom;
  if(creating&&!custom){
    const g=String(d.name||'').trim()?generateProjectIdentity(d.name,state.data.projects||[],state.projectIdentSeed):{id:'',key:''};
    d.id=g.id;d.key=g.key;
    if(form.elements.id)form.elements.id.value=g.id;
    if(form.elements.key)form.elements.key.value=g.key;
  }
  const idChip=form.querySelector('[data-pa-ident-id]'),keyChip=form.querySelector('[data-pa-ident-key]');
  if(idChip){idChip.textContent=d.id||'输入名称后生成';idChip.title=d.id||'';idChip.classList.toggle('ph',!d.id)}
  if(keyChip){keyChip.textContent=String(d.key||'—').toUpperCase();keyChip.classList.toggle('ph',!d.key)}
  const view=form.querySelector('[data-pa-ident-view]'),edit=form.querySelector('[data-pa-ident-form]'),desc=form.querySelector('[data-pa-ident-desc]');
  if(view)view.hidden=custom;if(edit)edit.hidden=!custom;if(desc)desc.textContent=projectIdentDesc(creating,custom);
}
function captureProjectAdminForm(){
  const form=$('#projectAdminForm'),d=state.projectDraft;if(!form||!d||form.dataset.projectFor!==state.projectDraftFor)return;
  const el=form.elements;
  ['name','description','color','workflowId','defaultRepositoryId','tapdWorkspaceId','tapdIterationId','key'].forEach(n=>{if(el[n])d[n]=el[n].value});
  if(state.projectAdminDetailId==='new'&&el.id)d.id=el.id.value.trim();
  if(el.tapdClear)d.tapdClear=!!el.tapdClear.checked;
  if(el.ownerId){
    d.ownerId=Number(el.ownerId.value)||0;
    d.members=[...form.querySelectorAll('[data-member-row]')].map(row=>({userId:Number(row.querySelector('input[type="hidden"]')?.value)||0,role:'member'}));
  }
}
let projectAdminInvalidPending=false;
function projectAdminRevealField(el){
  const panel=el?.closest?.('[data-pa-panel]');if(!panel)return;
  const tab=panel.dataset.paPanel;
  state.projectAdminErrors={...(state.projectAdminErrors||{}),[tab]:true};
  const target=el.type==='hidden'?el.closest('[data-user-combobox]')?.querySelector('[data-user-combobox-trigger]')||el:el;
  target.classList?.add('bad');
  if(projectAdminInvalidPending){syncProjectAdminTabs();return}
  projectAdminInvalidPending=true;setTimeout(()=>{projectAdminInvalidPending=false},0);
  if(el.closest('[data-pa-ident-form]')&&!state.projectIdentCustom){state.projectIdentCustom=true;syncProjectIdentity()}
  if(state.projectAdminTab!==tab)setProjectAdminTab(tab);else syncProjectAdminTabs();
  if(target!==el)setTimeout(()=>target.focus?.(),0);
}
// Called by saveProjectAdmin for checks the browser cannot express with required.
function projectAdminFail(form,name,message){
  const el=form?.elements?.[name]||(name==='members'?form?.querySelector?.('#memberRows [data-user-combobox-trigger]'):null);
  if(el){projectAdminRevealField(el);setTimeout(()=>(el.type==='hidden'?null:el)?.focus?.(),0)}
  toast(message,'err');return false;
}
function onProjectAdminInput(e){
  const t=e.target,form=$('#projectAdminForm');if(!form||!t?.name&&!t?.dataset)return;
  if(t.matches?.('[data-pa-color-custom]')){setProjectAdminColor(t.value);return}
  t.classList?.remove('bad');t.closest?.('[data-user-combobox]')?.querySelector('[data-user-combobox-trigger]')?.classList.remove('bad');
  captureProjectAdminForm();
  if(t.name==='name')syncProjectIdentity();
  if(t.name==='key'||t.name==='id')syncProjectIdentity();
  const panel=t.closest?.('[data-pa-panel]');
  if(panel&&state.projectAdminErrors?.[panel.dataset.paPanel]&&!panel.querySelector('.bad')){const next={...state.projectAdminErrors};delete next[panel.dataset.paPanel];state.projectAdminErrors=next}
  renderProjectAdminHead();syncProjectAdminTabs();syncProjectAdminSaveBar();
}
function setProjectAdminColor(color){
  const form=$('#projectAdminForm');if(!form||!/^#[0-9a-f]{6}$/i.test(color))return;
  form.elements.color.value=color;
  const lower=color.toLowerCase(),preset=PROJECT_SWATCHES.includes(lower);
  form.querySelectorAll('[data-pa-color]').forEach(b=>{const on=b.dataset.paColor===lower;b.classList.toggle('on',on);b.setAttribute('aria-pressed',String(on))});
  const custom=form.querySelector('.pa-sw-custom');if(custom){custom.classList.toggle('on',!preset);custom.style.background=preset?'':color;custom.style.color=color}
  const hex=form.querySelector('[data-pa-color-hex]');if(hex)hex.textContent=color.toUpperCase();
  captureProjectAdminForm();renderProjectAdminHead();syncProjectAdminSaveBar();
}
function bindProjectAdminForm(form){
  if(form.dataset.paBound)return;form.dataset.paBound='true';
  form.addEventListener('input',onProjectAdminInput);
  form.addEventListener('change',onProjectAdminInput);
  form.addEventListener('invalid',e=>projectAdminRevealField(e.target),true);
  form.addEventListener('click',e=>{
    const t=e.target instanceof Element?e.target:null;if(!t)return;
    const sw=t.closest('[data-pa-color]');if(sw){setProjectAdminColor(sw.dataset.paColor);return}
    if(t.closest('[data-pa-ident-edit]')){state.projectIdentCustom=true;syncProjectIdentity();(state.projectAdminDetailId==='new'?form.elements.id:form.elements.key)?.focus();return}
    if(t.closest('[data-pa-ident-auto]')){state.projectIdentCustom=false;form.elements.id?.classList.remove('bad');form.elements.key?.classList.remove('bad');syncProjectIdentity();renderProjectAdminHead();syncProjectAdminSaveBar();return}
    if(t.closest('[data-pa-ident-collapse]')){state.projectIdentCustom=false;syncProjectIdentity();return}
    if(t.closest('[data-pa-discard]')){discardProjectAdminDraft();return}
    if(t.closest('[data-repo-add]')){if(typeof openRepoDrawer==='function')openRepoDrawer('');return}
    const edit=t.closest('[data-repo-edit]');if(edit){if(typeof openRepoDrawer==='function')openRepoDrawer(edit.dataset.repoEdit);return}
    const del=t.closest('[data-repo-delete]');if(del){deleteProjectRepository(del.dataset.repoDelete);return}
    const pdel=t.closest('[data-project-delete]');if(pdel){deleteProjectAdmin(pdel.dataset.projectDelete);return}
    const rm=t.closest('[data-remove-member]');if(rm){captureProjectAdminForm();state.projectDraft.members.splice(+rm.dataset.removeMember,1);renderProjectDetail({force:true});return}
    if(t.closest('#addMember')){captureProjectAdminForm();state.projectDraft.members.push({userId:0,role:'member'});renderProjectDetail({force:true});setTimeout(()=>form.querySelector('#memberRows [data-member-row]:last-child [data-user-combobox-trigger]')?.click(),0)}
  });
}
function renderProjectDetail(opt={}){
  const form=$('#projectAdminForm');if(!form)return;
  const id=state.projectAdminDetailId,creating=id==='new',projects=state.data.projects||[],saved=creating?null:projects.find(p=>p.id===id);
  if(!creating&&!saved){
    toast('项目不存在或无权访问，已返回项目列表','warn');
    resetProjectAdminDetail();
    if(activeViewName()==='projects'&&parseProjectAdminRoute(location.hash)?.id)history.replaceState(null,'',projectAdminHash(''));
    return renderProjectAdmin();
  }
  if(!state.projectDraft||state.projectDraftFor!==id){
    state.projectDraft=creating?blankProjectDraft():projectDraftFrom(saved);
    state.projectDraftFor=id;state.projectDraftBase=clone(state.projectDraft);
    state.projectIdentCustom=false;state.projectIdentSeed=Math.random().toString(36).slice(2,6);state.projectAdminErrors={};
    opt.force=true;
  }
  const mode=creating?'new':'edit';
  const typing=form.dataset.projectFor===id&&form.dataset.mode===mode&&form.contains(document.activeElement)&&document.activeElement!==form;
  if(opt.force||!typing){
    const repos=creating?[]:(state.data.repositories||[]).filter(r=>r.projectId===id);
    form.innerHTML=projectAdminFormHTML(state.projectDraft,{creating,custom:!!state.projectIdentCustom,repos,saved});
    form.dataset.projectFor=id;form.dataset.mode=mode;
    bindProjectAdminForm(form);bindUserSearches(form);
  }
  syncProjectIdentity();renderProjectAdminHead();
  if(!PROJECT_ADMIN_TABS.some(t=>t.id===state.projectAdminTab)||creating&&state.projectAdminTab!=='basic')state.projectAdminTab='basic';
  form.querySelectorAll('[data-pa-panel]').forEach(p=>p.hidden=p.dataset.paPanel!==state.projectAdminTab);
  syncProjectAdminTabs();syncProjectAdminSaveBar();
  requestAnimationFrame?.(moveProjectAdminTabIndicator);
}
function renderProjectAdmin(opt={}){
  const listView=$('#projectListView'),detailView=$('#projectDetailView');if(!listView||!detailView||!state.data)return;
  const detail=!!state.projectAdminDetailId;
  listView.hidden=detail;detailView.hidden=!detail;
  if(detail)renderProjectDetail(opt);else renderProjectList();
}
function resetProjectAdminDetail(){
  state.projectAdminDetailId=null;state.projectAdminTab='basic';state.projectDraft=null;state.projectDraftFor='';state.projectDraftBase=null;state.projectIdentCustom=false;state.projectAdminErrors={};
  if(typeof closeRepoDrawer==='function')closeRepoDrawer();
}
function syncProjectAdminRouteState(){
  const route=parseProjectAdminRoute(location.hash);
  if(!route||!route.id){if(state.projectAdminDetailId)resetProjectAdminDetail();return}
  if(route.id==='new'){if(state.projectAdminDetailId)resetProjectAdminDetail();openProjectWizard();return}
  if(route.id!==state.projectAdminDetailId){resetProjectAdminDetail();state.projectAdminDetailId=route.id}
  state.projectAdminTab=route.tab;
}
async function confirmDiscardProjectDraft(){
  if(!projectAdminDirty())return true;
  const name=String(state.projectDraft?.name||'').trim()||'新项目';
  return afConfirm({title:'放弃未保存的修改？',message:`「${name}」有未保存的修改，离开后这些修改会丢失。`,danger:true,confirmText:'放弃修改',cancelText:'继续编辑'});
}
function openProjectAdminDetail(id,tab='basic'){
  if(id==='new')return openProjectWizard();
  resetProjectAdminDetail();
  state.projectAdminDetailId=id;state.projectAdminTab=id==='new'?'basic':tab;
  const next=projectAdminHash(id,state.projectAdminTab);
  if(location.hash!==next)history.pushState({paDetail:true},'',next);
  renderProjectAdmin({force:true});
  $('#projectsView')?.scrollTo?.(0,0);
  if(id==='new')setTimeout(()=>$('#projectAdminForm')?.elements.name?.focus(),60);
}
async function leaveProjectAdminDetail(){
  if(!state.projectAdminDetailId)return true;
  if(!(await confirmDiscardProjectDraft()))return false;
  resetProjectAdminDetail();
  if(history.state?.paDetail)history.back();
  else if(location.hash!==projectAdminHash(''))history.pushState(null,'',projectAdminHash(''));
  renderProjectAdmin();
  return true;
}
// hashchange inside the projects view: back/forward between list and details.
async function applyProjectAdminRoute(){
  const route=parseProjectAdminRoute(location.hash)||{id:'',tab:'basic'},cur=state.projectAdminDetailId||'';
  if(route.id===cur){if(cur&&route.tab!==state.projectAdminTab)setProjectAdminTab(route.tab,{silent:true});return}
  if(cur&&projectAdminDirty()&&!(await confirmDiscardProjectDraft())){history.pushState({paDetail:true},'',projectAdminHash(cur,state.projectAdminTab));return}
  if(route.id==='new'){if(cur){resetProjectAdminDetail();renderProjectAdmin({force:true})}openProjectWizard();return}
  resetProjectAdminDetail();
  state.projectAdminDetailId=route.id||null;state.projectAdminTab=route.tab;
  renderProjectAdmin({force:true});
}
function discardProjectAdminDraft(){
  if(state.projectAdminDetailId==='new'){resetProjectAdminDetail();if(history.state?.paDetail)history.back();else history.pushState(null,'',projectAdminHash(''));renderProjectAdmin();return}
  state.projectDraft=clone(state.projectDraftBase);state.projectIdentCustom=false;state.projectAdminErrors={};
  renderProjectDetail({force:true});toast('已放弃未保存的修改');
}
// Repository writes are immediate; keep the open draft and its baseline in step so a later project save cannot restore a stale default.
function syncProjectDraftDefaultRepo(projectId,repoId,onlyIf){
  if(state.projectDraftFor!==projectId)return;
  for(const d of [state.projectDraft,state.projectDraftBase]){if(d&&(onlyIf===undefined||d.defaultRepositoryId===onlyIf))d.defaultRepositoryId=repoId}
}
// Focus usually sits on a card button inside the form after a repo write, which the typing guard in renderProjectDetail would treat as editing.
function refreshProjectAdminRepos(focusRepoId){
  const id=state.projectAdminDetailId;if(!id||id==='new'||state.projectDraftFor!==id)return;
  renderProjectDetail({force:true});
  const form=$('#projectAdminForm');if(!form)return;
  const btn=(focusRepoId&&[...form.querySelectorAll('[data-repo-edit]')].find(b=>b.dataset.repoEdit===focusRepoId))||form.querySelector('[data-repo-add]');
  btn?.focus?.();
}
function canDeleteProject(project){
  if(!project?.id)return false;
  if(!state.authRequired||isPlatformAdmin())return true;
  return Number(project.ownerId)>0&&Number(project.ownerId)===Number(state.currentUser?.id);
}
function projectDeleteRow(project){
  if(!canDeleteProject(project))return '';
  return paRow('删除项目','项目从列表中移除，需求、运行记录和设置保留，平台管理员可以恢复。有未结束的运行或启用中的定时任务时不能删除。',`<button type="button" class="button danger-outline small" data-project-delete="${escapeHTML(project.id)}">删除项目</button>`,{attrs:' data-pa-row-inline'});
}
async function deleteProjectAdmin(projectId){
  const project=(state.data.projects||[]).find(p=>p.id===projectId);if(!project)return;
  const ok=await afConfirm({title:'删除项目',message:`确定删除项目「${project.name||project.id}」吗？\n\n项目会从列表中移除，需求、运行记录和设置保留，可以恢复；标识 ${project.id} 仍被占用。`,danger:true,confirmText:'确定删除'});
  if(!ok)return;
  try{
    await api('/api/projects/'+encodeURIComponent(project.id),{method:'DELETE'});
    state.data.projects=(state.data.projects||[]).filter(p=>p.id!==project.id);
    if(state.projectFilter===project.id){
      state.projectFilter=defaultProjectId(state.data.projects)||'';state.selectedIssue=null;
      localStorage.setItem('agentflow.activeProject',state.projectFilter);
      $('#chatNextFrame')?.contentWindow?.postMessage({type:'agentflow-project-context',projectId:state.projectFilter},location.origin);
    }
    resetProjectAdminDetail();
    history.replaceState(null,'',projectAdminHash(''));
    renderAll();renderProjectAdmin();toast(`已删除项目「${project.name||project.id}」`,'ok');
  }catch(err){toast(err.message,'err')}
}
async function deleteProjectRepository(repoId){
  const repo=(state.data.repositories||[]).find(r=>r.id===repoId);if(!repo)return;
  const project=(state.data.projects||[]).find(p=>p.id===repo.projectId),isDefault=project?.defaultRepositoryId===repo.id;
  const ok=await afConfirm({title:'删除关联仓库',message:`确定删除仓库「${repo.name||repo.gongfengProjectId||repo.id}」吗？已在执行中的 SOP 不受影响，该操作不可恢复。${isDefault?'\n\n这是当前的默认仓库，删除后默认仓库将变为「暂未绑定」。':''}`,danger:true,confirmText:'确定删除'});
  if(!ok)return;
  try{
    captureProjectAdminForm();
    await api('/api/repositories?id='+encodeURIComponent(repo.id),{method:'DELETE'});
    state.data.repositories=(state.data.repositories||[]).filter(r=>r.id!==repo.id);
    if(isDefault)project.defaultRepositoryId='';
    syncProjectDraftDefaultRepo(repo.projectId,'',repo.id);
    renderAll();refreshProjectAdminRepos();toast(`已删除仓库「${repo.name||repo.id}」`,'ok');
  }catch(err){toast(err.message,'err')}
}
function bindProjectAdmin(){
  $('#newProject').onclick=()=>openProjectWizard();
  $('#projectAdminBack').onclick=()=>leaveProjectAdminDetail();
  const search=$('#projectAdminSearch');if(search)search.oninput=()=>{state.projectAdminQuery=search.value;renderProjectList()};
  $$('#projectAdminFilters [data-project-filter]').forEach(b=>b.onclick=()=>{state.projectAdminFilter=b.dataset.projectFilter;renderProjectList()});
  $('#projectAdminForm').onsubmit=saveProjectAdmin;
  window.addEventListener('resize',moveProjectAdminTabIndicator);
}
function projectAdminAfterSave(projectId,creating){
  state.projectDraft=null;state.projectDraftFor='';state.projectDraftBase=null;state.projectAdminErrors={};
  if(!creating)return;
  state.projectAdminDetailId=projectId;state.projectAdminTab='repos';
  if(activeViewName()==='projects')history.replaceState(history.state,'',projectAdminHash(projectId,'repos'));
}
function projectDefaultSOPField(project){
  const current=String(project.workflowId||(!project.id?'generic-backend-delivery':'')).trim();
  const choices=(state.data.workflows||[]).filter(w=>w.generic||!w.projectId||w.projectId===project.id||w.shared);
  const missing=current&&!choices.some(w=>w.id===current);
  const options=choices.map(w=>`<option value="${escapeHTML(w.id)}" ${w.id===current?'selected':''}>${w.generic?'通用 · ':''}${escapeHTML(w.name)}</option>`).join('');
  return `<select name="workflowId" class="select" required><option value="">请选择默认 SOP</option>${missing?`<option value="${escapeHTML(current)}" selected disabled>已失效：${escapeHTML(current)}（请重新选择）</option>`:''}${options}</select>`;
}
function applyProjectDefaultSOP(project,workflowId){
  project.workflowId=workflowId;
  if(Array.isArray(project.sopPlaybooks)&&project.sopPlaybooks.length){
    const book=project.sopPlaybooks.find(b=>b.default)||project.sopPlaybooks[0];
    const binding=(book.bindings||[]).slice().sort((a,b)=>(Number(a.order)||0)-(Number(b.order)||0))[0];
    if(binding)binding.workflowId=workflowId;
  }else if(Array.isArray(project.sopBindings)&&project.sopBindings.length){
    const binding=project.sopBindings.slice().sort((a,b)=>(Number(a.order)||0)-(Number(b.order)||0))[0];
    binding.workflowId=workflowId;
  }
}
// Shared by the edit page and the new-project wizard. Fields left undefined fall back to base.
function buildProjectPayload(base,fields={}){
  base=base||{};
  const ownerId=Number(fields.ownerId??base.ownerId)||0,id=String(fields.id??base.id??'').trim(),rawDefault=String(fields.defaultRepositoryId??base.defaultRepositoryId??'');
  const value={...base,id,key:String(fields.key??base.key??'').trim().toUpperCase(),name:String(fields.name??base.name??'').trim(),description:fields.description??base.description??'',color:fields.color||base.color||PROJECT_SWATCHES[0],ownerId,workflowId:fields.workflowId??base.workflowId??'',defaultRepositoryId:rawDefault&&(state.data.repositories||[]).some(r=>r.id===rawDefault&&r.projectId===id)?rawDefault:'',members:(fields.members??base.members??[]).map(m=>({userId:Number(m.userId)||0,role:'member'})).filter(m=>m.userId!==ownerId),runEval:base.runEval,tapdWorkspaceId:String(fields.tapdWorkspaceId??base.tapdWorkspaceId??'').trim(),tapdIterationId:String(fields.tapdIterationId??base.tapdIterationId??'').trim()};
  applyProjectDefaultSOP(value,value.workflowId);
  for(const k of ['businessDomainId','tapdClear','repoRows','repoDrafts','gitToken','codeBuddyApiKey','codeBuddyInternetEnvironment'])delete value[k];
  if(fields.tapdClear)value.tapdClear=true;
  return value;
}
// Returns {field,message} for the first problem, or null. field is '' when no input is to blame.
function validateProjectPayload(value,{creating,projects=[],workflows=[],saved}={}){
  if(!value.name)return {field:'name',message:'项目名称不能为空'};
  if(!value.id)return {field:'id',message:'项目标识不能为空'};
  if(!value.key)return {field:'key',message:'项目 Key 不能为空'};
  if(creating&&(value.id.toLowerCase()==='new'||projects.some(p=>String(p.id).toLowerCase()===value.id.toLowerCase())))return {field:'id',message:`项目标识「${value.id}」已被占用，请换一个`};
  if(!creating&&!saved)return {field:'',message:'项目不存在或无权访问'};
  if(!value.workflowId||!workflows.some(w=>w.id===value.workflowId))return {field:'workflowId',message:'请选择有效的默认 SOP'};
  if(!value.ownerId)return {field:'ownerId',message:'请选择项目 Owner'};
  if(value.members.some(m=>!m.userId))return {field:'members',message:'请选择有效的项目成员'};
  if(new Set(value.members.map(m=>m.userId)).size!==value.members.length)return {field:'members',message:'项目成员不能重复'};
  return null;
}
function storeSavedProject(result,value){
  const projects=state.data.projects||(state.data.projects=[]),stored=result&&result.id?result:value;
  const i=projects.findIndex(p=>p.id===stored.id);if(i>=0)projects[i]=stored;else projects.push(stored);
  return stored;
}
async function saveProjectAdmin(e){
  e.preventDefault();
  const f=e.currentTarget,el=f.elements,projects=state.data.projects||[],creating=state.projectAdminDetailId==='new';
  const projectId=String(el.id?.value||'').trim(),saved=projects.find(p=>p.id===projectId);
  const base=clone(state.projectDraft||saved||{});
  const value=buildProjectPayload(base,{id:projectId,key:el.key?.value||'',name:el.name?.value||'',description:el.description?.value,color:el.color?.value,ownerId:el.ownerId?el.ownerId.value:undefined,workflowId:el.workflowId?.value,defaultRepositoryId:el.defaultRepositoryId?.value||'',tapdWorkspaceId:el.tapdWorkspaceId?.value,tapdIterationId:el.tapdIterationId?.value,tapdClear:!!el.tapdClear?.checked});
  const invalid=validateProjectPayload(value,{creating,projects,workflows:state.data.workflows||[],saved});
  if(invalid)return invalid.field?projectAdminFail(f,invalid.field,invalid.message):toast(invalid.message,'err');
  const submit=f.querySelector?.('[data-pa-submit]');if(submit)submit.disabled=true;
  try{
    const result=await api('/api/projects',{method:creating?'POST':'PUT',body:JSON.stringify(value)});
    const savedProjectId=storeSavedProject(result,value).id||value.id;
    if(!state.projectFilter){state.projectFilter=savedProjectId;localStorage.setItem('agentflow.activeProject',savedProjectId)}
    projectAdminAfterSave(savedProjectId,creating);
    renderAll();
    toast(creating?'项目已创建，下一步绑定代码仓库':'项目设置已保存','ok');
  }catch(err){toast(err.message,'err')}
  finally{if(submit)submit.disabled=false}
}
