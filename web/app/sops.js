function renderCatalogScopes(){
  const workflows=state.data?.workflows||[],agents=state.data?.agents||[];
  const sopProject=workflows.filter(w=>!catalogGeneric(w)&&catalogOwned(w.projectId)).length;
  const sopShared=workflows.filter(w=>!catalogGeneric(w)&&catalogShared(w.projectId,w.shared)).length;
  const sopGeneric=workflows.filter(w=>catalogGeneric(w)).length;
  const agentProject=agents.filter(a=>catalogOwned(a.projectId)).length;
  const agentShared=agents.filter(a=>catalogShared(a.projectId,a.shared)).length;
  const set=(id,n)=>{const el=document.querySelector(`[data-scope-count="${id}"]`);if(el)el.textContent=n};
  set('sop-project',sopProject);set('sop-shared',sopShared);set('sop-generic',sopGeneric);set('agent-project',agentProject);set('agent-shared',agentShared);
  document.querySelectorAll('[data-scope-for]').forEach(bar=>{
    const current=bar.dataset.scopeFor==='agent'?state.agentScope:state.sopScope;
    bar.querySelectorAll('[data-scope]').forEach(btn=>btn.classList.toggle('on',btn.dataset.scope===current));
  });
}
async function catalogItemFull(kind,id){
  return api(kind==='workflow'?`/api/workflows/${encodeURIComponent(id)}`:`/api/agents/${encodeURIComponent(id)}`);
}
function replaceCatalogItem(kind,saved){
  const list=kind==='workflow'?state.data.workflows:state.data.agents;
  const i=list.findIndex(x=>x.id===saved.id);
  if(i>=0)list[i]=saved;else list.unshift(saved);
  if(kind==='workflow'&&state.workflow?.id===saved.id)state.workflow=clone(saved);
  if(kind==='agent'&&state.selectedAgent===saved.id)state.selectedAgent=saved.id;
}
async function toggleShare(kind,id){
  const list=kind==='workflow'?state.data.workflows:state.data.agents;
  const item=list.find(x=>x.id===id);
  if(!item||!catalogOwned(item.projectId))return toast('只能共享本项目的内容');
  try{
    const full=await catalogItemFull(kind,id);
    if(!catalogOwned(full.projectId))return toast('只能共享本项目的内容');
    const saved=await api(kind==='workflow'?'/api/workflows':'/api/agents',{method:'PUT',body:JSON.stringify({...full,shared:!full.shared})});
    replaceCatalogItem(kind,saved);
    if(kind==='agent'&&saved.shared){(state.data.rules||[]).forEach(item=>{if((saved.rules||[]).includes(item.id)&&item.projectId===saved.projectId)item.shared=true});(state.data.skills||[]).forEach(item=>{if((saved.skills||[]).includes(item.id)&&item.projectId===saved.projectId)item.shared=true})}
    kind==='workflow'?renderSOPCatalog():renderAgents();
    toast(saved.shared?'已共享，其他项目可在共享页看到':'已取消共享');
  }catch(e){toast(e.message)}
}
// --- 通用模板：依赖预检与复制 -------------------------------------------------
// Generic templates are platform blueprints. Their 发起 MR node needs a 工蜂 Git
// Token to push and a 工蜂 MCP to create the merge
// request, so the catalog surfaces that dependency before adoption.
const GENERIC_REQUIREMENT_CHECKS={
  'gongfeng-git-token':{label:'工蜂 Git Token',hint:'推送工作分支到工蜂。',fix:'settings',fixLabel:'去个人配置配置',check:()=>!!(userById(state.currentUser?.id)||{}).gitTokenConfigured},
  'gongfeng-mcp':{label:'工蜂 MCP',hint:'调用工蜂接口创建 MR；未安装时复制出的 SOP 需安装后为「交付工程师」角色绑定。',fix:'mcp',fixLabel:'去能力资产添加',check:()=>gongfengMCPInstalled()},
  'tapd-mcp':{label:'TAPD MCP',hint:'把验收结论回写 TAPD；未安装时只产出待人工粘贴的说明。',fix:'mcp',fixLabel:'去能力资产添加',check:()=>tapdMCPInstalled()}
};
function gongfengMarketItem(){
  return (state.mcpMarket||[]).find(item=>(item.aliases||[]).includes('gongfeng')||/gongfeng/i.test(item.url||''))||null;
}
function gongfengMCPInstalled(){
  const marketID=gongfengMarketItem()?.id||'';
  return (state.data?.mcpServers||[]).some(item=>{
    if(item.scope!=='project'||item.projectId!==state.projectFilter)return false;
    if(marketID&&item.marketId)return item.marketId===marketID;
    return /gongfeng|工蜂/i.test(`${item.name||''} ${item.url||''} ${item.marketId||''}`);
  });
}
function tapdMarketItem(){
  return (state.mcpMarket||[]).find(item=>(item.aliases||[]).includes('tapd')||/tapd/i.test(item.url||''))||null;
}
function tapdMCPInstalled(){
  const marketID=tapdMarketItem()?.id||'';
  return (state.data?.mcpServers||[]).some(item=>{
    if(item.scope!=='project'||item.projectId!==state.projectFilter)return false;
    if(marketID&&item.marketId)return item.marketId===marketID;
    return /tapd/i.test(`${item.name||''} ${item.url||''} ${item.marketId||''}`);
  });
}
function genericRequirementRows(w){
  return (w?.requirements||[]).map(req=>{
    const meta=GENERIC_REQUIREMENT_CHECKS[req.key]||{};
    let ok=false;
    try{ok=typeof meta.check==='function'?!!meta.check():false}catch(e){ok=false}
    return {key:req.key,label:req.label||meta.label||req.key,hint:req.hint||meta.hint||'',nodeId:req.nodeId||'',ok,fix:meta.fix||'',fixLabel:meta.fixLabel||'去处理'};
  });
}
function workflowNodeName(w,id){return (w?.nodes||[]).find(n=>n.id===id)?.name||id}
function genericRequirementChips(w){
  const rows=genericRequirementRows(w);
  if(!rows.length)return '';
  return `<div class="sop-requirements">${rows.map(r=>sopBadge(r.label,r.ok?'success':'warn',r.ok?'check':'alert',r.hint)).join('')}</div>`;
}
// Agent nodes run with the executor credentials of the requirement owner; a
// bound repository only matters for templates that check out project code.
function sopRunRequirements(w){
  const nodes=w?.nodes||[],user=userById(state.currentUser?.id)||{};
  const project=(state.data?.projects||[]).find(p=>p.id===(w?.projectId||state.projectFilter))||(state.data?.projects||[]).find(p=>p.id===state.projectFilter);
  const driver=((state.data?.sandboxes||[]).find(sb=>sb.id===w?.sandboxProfileId)||{}).driver||(/docker/i.test(w?.sandboxProfileId||'')?'docker':'');
  const usesRepo=(catalogGeneric(w)||(w?.tags||[]).some(t=>String(t).startsWith('generic-source:')))&&!!project?.defaultRepositoryId;
  const rows=[];
  const providers=new Set();
  for(const node of nodes.filter(n=>n.type==='agent')){
    const role=(state.data?.agents||[]).find(a=>a.id===node.agentId);
    const id=role?.executorId||node.executorId||'codebuddy';
    const kind=(state.data?.executors||[]).find(e=>e.id===id)?.kind||id;
    if(['codebuddy','codex','claude','claude-code'].includes(kind))providers.add(kind==='claude'?'claude-code':kind);
  }
  for(const kind of providers){
    const [label,field,flag]={codebuddy:['CodeBuddy','codeBuddyApiKey','codeBuddyApiKeyConfigured'],codex:['Codex','codexApiKey','codexApiKeyConfigured'],'claude-code':['Claude Code','claudeCodeApiKey','claudeCodeApiKeyConfigured']}[kind];
    rows.push({key:'run-key-'+kind,field,fix:'settings',fixLabel:'去个人配置配置',label:label+' API Key',ok:!!user[flag],hint:lfT('Agent 节点使用 '+label+'，请在个人配置里配置对应 API Key。','Agent nodes use '+label+'. Configure its API key in Settings.')});
  }
  if(usesRepo)rows.push({key:'run-repo',fix:'',label:'GitHub 仓库（已绑定）',ok:true,hint:'使用项目已绑定的仓库。访问私有仓库或推送代码时需要在个人配置中配置 GitHub Token；未绑定仓库时可在任务工作区直接运行。'});
  if(driver==='docker')rows.push({key:'run-docker',label:'Docker',ok:state.data?.capabilities?.docker!==false,hint:state.data?.capabilities?.docker===false?'平台连不上 Docker，这个 SOP 现在跑不了。重新运行 scripts/install.sh 会自动安装并挂载 Docker。':'在隔离的 Docker 沙箱里运行，首次运行会构建镜像，需要几分钟。'});
  return rows;
}
function sopRunRequirementChips(w){
  const rows=sopRunRequirements(w);
  if(!rows.length)return `<div class="sop-requirements">${sopBadge('无需 Key 或仓库','success','check','只包含脚本和人工审批节点')}</div>`;
  return `<div class="sop-requirements" aria-label="运行条件">${rows.map(r=>sopBadge((r.ok?'':'需要 ')+r.label,r.ok?'success':'warn',r.ok?'check':'alert',r.hint)).join('')}</div>`;
}
// --- 流程模板卡片 -------------------------------------------------------------
// Each card exposes one primary action; the rest live in the ⋯ popover.
const SOP_HEALTH_TONES={'运行中':{tone:'info',icon:'lamp'},'待审批':{tone:'warn',icon:'user-hold'},'已闭环':{tone:'success',icon:'check'},'有阻塞':{tone:'error',icon:'alert'},'未闭环':{tone:'error',icon:'alert'},'从未运行':{tone:'neutral',icon:'circle'}};
function sopHealthTone(health){return SOP_HEALTH_TONES[health?.label]||{tone:'neutral',icon:'circle'}}
function sopBadge(label,tone,icon,title){
  const mark=icon==='lamp'?'<span class="sop-lamp" aria-hidden="true"><i></i></span>':icon?huiIcon(icon):'';
  return `<span class="sop-badge tone-${tone}"${title?` title="${escapeHTML(title)}"`:''}>${mark}<span class="sop-badge-label">${escapeHTML(label)}</span></span>`;
}
function sopHealthBadge(health){const {tone,icon}=sopHealthTone(health);return sopBadge(health?.label||'',tone,icon)}
function sopStatusBadge(w){
  if(catalogGeneric(w))return sopBadge('通用','accent','layers');
  if(w?.status==='published')return sopBadge('已发布','success','check');
  if(w?.status==='archived')return sopBadge('已归档','neutral','circle');
  return sopBadge('草稿','neutral','edit');
}
function sopCardHead(left,hint,right){return `<div class="sop-card-head"><div>${left}<span class="sop-card-hint">${escapeHTML(hint||'')}</span></div>${right}</div>`}
function sopCardTitle(w){return `<div class="sop-card-title"><span class="sop-card-tile">${huiIcon('workflow')}</span><h3 title="${escapeHTML(w.name)}">${escapeHTML(w.name)}</h3></div>`}
function sopCardPrimary(attr,id,label,icon='',title=''){return `<button type="button" class="sop-card-primary" ${attr}="${escapeHTML(id)}" data-hover-ink-control${title?` title="${escapeHTML(title)}"`:''}>${icon?huiIcon(icon):''}${escapeHTML(label)}</button>`}
function sopCardMoreButton(w){return `<button type="button" class="sop-card-more" data-sop-menu="${escapeHTML(w.id)}" data-hover-ink-control aria-haspopup="menu" aria-expanded="false" aria-label="更多操作" title="更多操作">${huiIcon('more-horizontal')}</button>`}
function sopRunKpis(m,liveCount){return `<div class="sop-kpis"><span><b>${m.totalRuns||0}</b>运行</span><span><b>${m.succeededRuns||0}</b>成功</span><span${m.failedRuns>0?' class="is-bad"':''}><b>${m.failedRuns||0}</b>失败</span><span><b>${liveCount}</b>进行中</span></div>`}
function sopRunCardBody(w,m,liveCount){
  const health=metricHealth(m,liveCount);
  return `${sopCardHead(sopHealthBadge(health),m.totalRuns?`${percent(m.successRate)} 成功率`:health.hint,sopStatusBadge(w))}${sopCardTitle(w)}<p>${escapeHTML(w.description||'暂无流程说明')}</p>${sopRunRequirementChips(w)}${sopRunKpis(m,liveCount)}`;
}
function projectSOPCard(w,m={},liveCount=0){
  const recent=formatTimestamp(m.lastRunAt);
  const meta=`${w.owner||'未分配负责人'} · v${w.version||1} · ${recent?`最近 ${recent}`:'尚未运行'}`;
  return `<article class="sop-card" data-sop="${escapeHTML(w.id)}" tabindex="0" role="button" aria-label="打开流程设计器：${escapeHTML(w.name)}">${sopRunCardBody(w,m,liveCount)}<div class="sop-card-foot"><span class="sop-card-meta" title="${escapeHTML(meta)}">${escapeHTML(meta)}</span>${sopCardPrimary('data-open-sop',w.id,'打开')}${sopCardMoreButton(w)}</div></article>`;
}
function sharedSOPCard(w,m={},liveCount=0){
  const from=!w.projectId?'平台':`来自 ${(state.data.projects||[]).find(p=>p.id===w.projectId)?.name||w.projectId}`;
  const meta=`${w.owner?`${w.owner} · `:''}v${w.version||1}`;
  return `<article class="sop-card" data-sop="${escapeHTML(w.id)}" tabindex="0" role="button" aria-label="打开流程设计器：${escapeHTML(w.name)}">${sopRunCardBody(w,m,liveCount)}<div class="sop-card-foot"><span class="sop-card-meta" title="${escapeHTML(`${from} · ${meta}`)}"><span class="sop-card-tag">${escapeHTML(from)}</span>${escapeHTML(meta)}</span>${sopCardPrimary('data-clone-workflow',w.id,'复制到本项目','copy')}</div></article>`;
}
function genericProjectCopy(w){return (state.data?.workflows||[]).find(x=>!catalogGeneric(x)&&x.projectId===state.projectFilter&&(x.tags||[]).includes(`generic-source:${w.id}`))}
function genericSOPCard(w){
  const nodes=w.nodes||[],agentCount=nodes.filter(n=>n.type==='agent').length,gates=nodes.filter(n=>n.type==='approval').length;
  const copy=genericProjectCopy(w);
  const primary=copy
    ?sopCardPrimary('data-open-sop',copy.id,'查看项目副本','eye')
    :sopCardPrimary('data-adopt-generic',w.id,'复制到本项目','copy','复制后归本项目所有、可自由修改，不再跟随平台更新');
  const tags=(w.tags||[]).filter(t=>!t.startsWith('generic-source:')).join(' · ')||'通用模板';
  return `<article class="sop-card generic" data-generic-id="${escapeHTML(w.id)}" tabindex="0" role="button" aria-label="使用通用模板：${escapeHTML(w.name)}">${sopCardHead(sopStatusBadge(w),'平台维护 · 与项目无关',sopBadge(`v${w.version||1}`,'neutral',''))}${sopCardTitle(w)}<p>${escapeHTML(w.description||'平台提供的通用流程模板')}</p>${sopRunRequirementChips(w)}${genericRequirementChips(w)}<div class="sop-kpis"><span><b>${nodes.length}</b>节点</span><span><b>${agentCount}</b>角色节点</span><span><b>${gates}</b>人工门禁</span><span><b>v${w.version||1}</b>版本</span></div><div class="sop-card-foot"><span class="sop-card-meta" title="${escapeHTML(`${w.owner||'平台'} · ${tags}`)}"><span class="sop-card-tag">${escapeHTML(w.owner||'平台')}</span>${escapeHTML(tags)}</span>${primary}${sopCardMoreButton(w)}</div></article>`;
}
function sopCatalogCard(w,metrics,live){
  if(catalogGeneric(w))return genericSOPCard(w);
  const m=metrics.get(w.id)||{},liveCount=live.filter(r=>r.workflowId===w.id).length;
  return catalogOwned(w.projectId)?projectSOPCard(w,m,liveCount):sharedSOPCard(w,m,liveCount);
}
// --- 流程模板卡片 · ⋯ 菜单 -----------------------------------------------------
const SOP_MENU_ACTIONS={
  share:{hook:'data-share-workflow',run:id=>toggleShare('workflow',id)},
  delete:{hook:'data-delete-workflow',run:id=>deleteWorkflow(id)},
  run:{hook:'data-run-generic',run:id=>openRunModal(id)},
  edit:{hook:'data-edit-generic',run:id=>openGenericTemplateEditor(id)}
};
function sopCardMenuItems(w){
  if(!w)return [];
  if(catalogGeneric(w))return [{action:'run',label:'直接运行',icon:'play'},...(isPlatformAdmin()?[{action:'edit',label:'编辑模板',icon:'edit'}]:[])];
  if(!catalogOwned(w.projectId))return [];
  return [{action:'share',label:w.shared?'取消共享':'共享给其他项目',icon:w.shared?'close':'arrow-up'},'-',{action:'delete',label:'删除',icon:'trash',danger:true}];
}
function sopCardMenuHTML(w){
  return sopCardMenuItems(w).map(item=>item==='-'?'<span class="sop-popover-sep" role="separator"></span>':`<button type="button" role="menuitem" tabindex="-1" class="issue-popover-item${item.danger?' danger':''}" data-sop-action="${item.action}" ${SOP_MENU_ACTIONS[item.action].hook}="${escapeHTML(w.id)}">${huiIcon(item.icon,'popover-icon')}${escapeHTML(item.label)}</button>`).join('');
}
let sopMenuPopover=null,sopMenuTrigger=null;
function ensureSOPMenuPopover(){
  if(sopMenuPopover)return sopMenuPopover;
  const menu=document.createElement('div');
  menu.id='sopActionPopover';
  menu.className='issue-action-popover sop-action-popover';
  menu.setAttribute('role','menu');
  menu.setAttribute('aria-label','SOP 更多操作');
  menu.hidden=true;
  document.body.appendChild(menu);
  menu.addEventListener('click',e=>{
    const item=e.target.closest('[data-sop-action]');if(!item)return;
    e.stopPropagation();
    const action=SOP_MENU_ACTIONS[item.dataset.sopAction],id=item.getAttribute(action?.hook||'');
    hideSOPCardMenu(true);
    if(action&&id)action.run(id);
  });
  menu.addEventListener('keydown',e=>{
    const items=[...menu.querySelectorAll('[role="menuitem"]')],i=items.indexOf(document.activeElement);
    if(e.key==='ArrowDown'||e.key==='ArrowUp'){
      e.preventDefault();
      const n=items.length,down=e.key==='ArrowDown';if(!n)return;
      items[i<0?(down?0:n-1):(i+(down?1:n-1))%n].focus();
    }
    else if(e.key==='Home'||e.key==='End'){e.preventDefault();items[e.key==='Home'?0:items.length-1]?.focus()}
    else if(e.key==='Escape'||e.key==='Tab'){e.preventDefault();e.stopPropagation();hideSOPCardMenu(true)}
  });
  document.addEventListener('click',e=>{
    if(menu.hidden||menu.contains(e.target)||e.target.closest?.('[data-sop-menu]'))return;
    hideSOPCardMenu();
  });
  document.addEventListener('keydown',e=>{if(e.key==='Escape'&&!menu.hidden)hideSOPCardMenu(true)});
  window.addEventListener('resize',()=>hideSOPCardMenu(),{passive:true});
  window.addEventListener('scroll',e=>{if(!menu.contains(e.target))hideSOPCardMenu()},{passive:true,capture:true});
  sopMenuPopover=menu;
  return menu;
}
function hideSOPCardMenu(restoreFocus=false){
  if(!sopMenuPopover||sopMenuPopover.hidden)return;
  const trigger=sopMenuTrigger;
  sopMenuPopover.hidden=true;
  sopMenuPopover.innerHTML='';
  sopMenuTrigger=null;
  trigger?.setAttribute('aria-expanded','false');
  if(restoreFocus&&trigger?.isConnected)trigger.focus({preventScroll:true});
}
function positionSOPCardMenu(){
  const menu=sopMenuPopover,trigger=sopMenuTrigger;if(!menu||!trigger)return;
  const r=trigger.getBoundingClientRect(),w=menu.offsetWidth||188,h=menu.offsetHeight||88;
  const left=Math.max(8,Math.min(r.right-w,window.innerWidth-w-8));
  let top=r.bottom+6;
  if(top+h>window.innerHeight-8)top=Math.max(8,r.top-h-6);
  menu.style.left=`${Math.round(left)}px`;
  menu.style.top=`${Math.round(top)}px`;
}
function showSOPCardMenu(trigger){
  const menu=ensureSOPMenuPopover();
  if(!menu.hidden&&sopMenuTrigger===trigger)return hideSOPCardMenu(true);
  hideSOPCardMenu();
  if(typeof hideIssueActionMenu==='function')hideIssueActionMenu();
  const w=(state.data?.workflows||[]).find(x=>x.id===trigger.dataset.sopMenu);
  const html=w?sopCardMenuHTML(w):'';
  if(!html)return;
  menu.innerHTML=html;
  window.AgentFlowHarnessIcons?.hydrate?.(menu);
  menu.hidden=false;
  sopMenuTrigger=trigger;
  trigger.setAttribute('aria-expanded','true');
  positionSOPCardMenu();
  menu.querySelector('[role="menuitem"]')?.focus({preventScroll:true});
}
// A metrics refresh replaces the card markup; keep an open menu attached to the new ⋯.
function syncSOPCardMenu(root){
  if(!sopMenuPopover||sopMenuPopover.hidden||sopMenuTrigger?.isConnected)return;
  const id=sopMenuTrigger?.dataset.sopMenu;
  const next=[...root.querySelectorAll('.sop-card:not(.is-leaving) [data-sop-menu]')].find(b=>b.dataset.sopMenu===id);
  if(!next)return hideSOPCardMenu();
  sopMenuTrigger=next;
  next.setAttribute('aria-expanded','true');
  positionSOPCardMenu();
}
// --- 流程模板卡片 · 动效 -------------------------------------------------------
// Cards enter once per reveal context (view activation + scope + filter + sort);
// content refreshes inside the same context only move cards whose slot changed.
const sopCatalogMotion=(()=>{
  const seen=new Set(),entering=new Map();
  const reduced=typeof window!=='undefined'&&typeof window.matchMedia==='function'?window.matchMedia('(prefers-reduced-motion: reduce)'):{matches:false};
  const now=()=>typeof performance!=='undefined'?performance.now():Date.now();
  const enterMs=550,stepMs=30,maxIndex=10;
  let activation=0,played=-1,context=null;
  const keyOf=el=>el.dataset.sop?`sop:${el.dataset.sop}`:el.dataset.genericId?`generic:${el.dataset.genericId}`:'';
  const liveCards=root=>[...root.querySelectorAll('.sop-card:not(.is-leaving)')];
  const visible=root=>root.getClientRects().length>0;
  function snapshot(root){
    const cards=new Map();
    if(!visible(root))return {cards,visible:false};
    liveCards(root).forEach(el=>{const key=keyOf(el);if(key)cards.set(key,{el,rect:el.getBoundingClientRect()})});
    return {cards,host:root.getBoundingClientRect(),visible:true};
  }
  function enter(el,key,index,elapsed=0){
    el.style.setProperty('--card-index',String(index));
    if(elapsed)el.style.animationDelay=`${Math.min(index,maxIndex)*stepMs-elapsed}ms`;
    el.classList.add('is-entering');
    el.addEventListener('animationend',function done(e){if(e.target!==el)return;el.classList.remove('is-entering');el.style.animationDelay='';entering.delete(key);el.removeEventListener('animationend',done)});
  }
  function flip(el,dx,dy){
    el.style.transition='none';
    el.style.transform=`translate(${dx}px, ${dy}px)`;
    requestAnimationFrame(()=>requestAnimationFrame(()=>{
      if(!el.isConnected)return;
      el.style.transition='transform .58s var(--hui-ease-flow), box-shadow .4s var(--hui-ease-flow), border-color .3s var(--hui-ease-flow)';
      el.style.transform='';
      setTimeout(()=>{if(!el.style.transform)el.style.transition=''},640);
    }));
  }
  function leave(root,before,keep){
    before.cards.forEach((prev,key)=>{
      if(keep.has(key))return;
      const ghost=prev.el;
      ghost.classList.remove('is-entering');
      ghost.classList.add('is-leaving');
      ghost.setAttribute('aria-hidden','true');
      ghost.setAttribute('inert','');
      ghost.removeAttribute('tabindex');
      Object.assign(ghost.style,{left:`${prev.rect.left-before.host.left}px`,top:`${prev.rect.top-before.host.top}px`,width:`${prev.rect.width}px`,height:`${prev.rect.height}px`,transform:'',transition:'',animationDelay:''});
      root.appendChild(ghost);
      const remove=()=>ghost.remove();
      ghost.addEventListener('animationend',remove,{once:true});
      setTimeout(remove,260);
    });
  }
  function play(root,before,revealKey){
    const ctx=`${activation}|${revealKey}`,replay=ctx!==context;
    context=ctx;
    if(replay){seen.clear();entering.clear()}
    // Cards left over from before the view was re-entered are not on screen to move from.
    if(played!==activation)before={cards:new Map(),visible:false};
    played=activation;
    const cards=liveCards(root);
    if(reduced.matches||!visible(root)){
      if(visible(root))cards.forEach(el=>seen.add(keyOf(el)));
      return;
    }
    const t=now(),keep=new Set(cards.map(keyOf));
    let index=0;
    cards.forEach(el=>{
      const key=keyOf(el),prev=before.visible?before.cards.get(key):null,running=entering.get(key);
      if(prev&&running&&t-running.start<enterMs+Math.min(running.index,maxIndex)*stepMs){
        enter(el,key,running.index,t-running.start);
      }else if(prev){
        entering.delete(key);
        const r=el.getBoundingClientRect(),dx=prev.rect.left-r.left,dy=prev.rect.top-r.top;
        if(Math.abs(dx)>.5||Math.abs(dy)>.5)flip(el,dx,dy);
      }else if(!seen.has(key)){
        entering.set(key,{start:t,index});
        enter(el,key,index++);
      }
      seen.add(key);
    });
    if(before.visible)leave(root,before,keep);
  }
  return {snapshot,play,reset(){activation++;seen.clear();entering.clear()},get reduced(){return reduced.matches}};
})();
function sopCatalogFocus(root){
  const el=document.activeElement;
  if(!el||!root.contains(el))return null;
  const card=el.closest('.sop-card');if(!card)return null;
  const part=el===card?'':el.matches('[data-sop-menu]')?'[data-sop-menu]':el.matches('.sop-card-primary')?'.sop-card-primary':'';
  return {sop:card.dataset.sop||'',generic:card.dataset.genericId||'',part};
}
function restoreSOPCatalogFocus(root,focus){
  if(!focus)return;
  const card=liveSOPCards(root).find(c=>(focus.sop&&c.dataset.sop===focus.sop)||(focus.generic&&c.dataset.genericId===focus.generic));
  (card&&focus.part?card.querySelector(focus.part):card)?.focus({preventScroll:true});
}
function liveSOPCards(root){return [...root.querySelectorAll('.sop-card:not(.is-leaving)')]}
function openSOPDesigner(id,missing='SOP 不存在或已删除'){
  const wf=(state.data.workflows||[]).find(x=>x.id===id);
  if(!wf)return toast(missing);
  state.workflow=clone(wf);state.selectedNode=null;renderWorkflowMeta();renderCanvas();renderProperties();switchView('flow');
}
function activateSOPCard(card){
  if(card.dataset.genericId)openGenericTemplateModal(card.dataset.genericId);
  else if(card.dataset.sop)openSOPDesigner(card.dataset.sop);
}
function bindSOPCatalogEvents(root){
  if(root.dataset.sopEvents)return;
  root.dataset.sopEvents='1';
  root.addEventListener('click',e=>{
    const card=e.target.closest('.sop-card');
    if(!card||card.classList.contains('is-leaving'))return;
    const btn=e.target.closest('button');
    if(btn){
      e.stopPropagation();
      if(btn.dataset.sopMenu)return showSOPCardMenu(btn);
      hideSOPCardMenu();
      if(btn.dataset.openSop)openSOPDesigner(btn.dataset.openSop,'项目副本不存在或已删除');
      else if(btn.dataset.cloneWorkflow)cloneWorkflow(btn.dataset.cloneWorkflow);
      else if(btn.dataset.adoptGeneric)openGenericTemplateModal(btn.dataset.adoptGeneric);
      return;
    }
    hideSOPCardMenu();
    activateSOPCard(card);
  });
  root.addEventListener('keydown',e=>{
    if((e.key!=='Enter'&&e.key!==' ')||!e.target.matches?.('.sop-card:not(.is-leaving)'))return;
    e.preventDefault();
    hideSOPCardMenu();
    activateSOPCard(e.target);
  });
}
function renderGenericRequirementRows(w){
  const host=$('#genericTemplateRequirements');if(!host)return;
  const rows=[...sopRunRequirements(w).map(r=>({...r,fix:r.ok?'':r.fix||''})),...genericRequirementRows(w)];
  host.innerHTML=rows.map(r=>`<div class="req-row ${r.ok?'ok':'missing'}"><span class="req-mark">${r.ok?'✓':'!'}</span><div><b>${escapeHTML(r.label)}</b><p>${escapeHTML(r.hint)}${r.nodeId?` · 节点「${escapeHTML(workflowNodeName(w,r.nodeId))}」`:''}</p></div><span class="req-state">${r.ok?'已配置':'未配置'}</span>${r.fix?`<button type="button" class="button ghost small" data-fix-requirement="${escapeHTML(r.key)}">${escapeHTML(r.fixLabel)}</button>`:''}</div>`).join('')||'<div class="req-row ok"><span class="req-mark">✓</span><div><b>无额外依赖</b><p>复制到本项目后即可直接运行。</p></div></div>';
  host.querySelectorAll('[data-fix-requirement]').forEach(btn=>btn.onclick=()=>openGenericRequirementTarget(btn.dataset.fixRequirement));
}
async function openGenericTemplateEditor(id){
  const w=(state.data?.workflows||[]).find(x=>x.id===id&&catalogGeneric(x));
  if(!w)return toast('通用模板不存在或已下架');
  if(!isPlatformAdmin())return toast('只有平台管理员可以编辑通用模板');
  state.workflow=clone(w);state.selectedNode=null;markWorkflowClean();
  clearGenericTemplateAgents();
  renderWorkflowMeta();renderCanvas();renderProperties();switchView('flow');
  await refreshGenericWorkflowRoles();
}
function openGenericTemplateModal(id){
  const w=(state.data?.workflows||[]).find(x=>x.id===id&&catalogGeneric(x));
  if(!w)return toast('通用模板不存在或已下架');
  state.genericTemplateId=id;
  $('#genericTemplateTitle').textContent=`使用「${w.name}」`;
  $('#genericTemplateIntro').textContent=w.description||'平台提供的通用流程模板，复制后归本项目所有。';
  const nodes=w.nodes||[],agentCount=nodes.filter(n=>n.type==='agent').length,gates=nodes.filter(n=>n.type==='approval').length;
  $('#genericTemplateStructure').innerHTML=`<span>${nodes.length} 个节点</span><span>${agentCount} 个角色节点</span><span>${gates} 个人工门禁</span>${nodes.some(n=>n.id==='mr')?'<span class="highlight">含「发起 PR」节点</span>':''}<span>复制=归本项目、可自由修改、不再跟随平台更新</span><span>直接运行=始终使用平台最新版</span>`;
  renderGenericRequirementRows(w);
  $('#genericTemplateModal').classList.add('open');
}
function openGenericRequirementTarget(key){
  if(key==='run-key'||key.startsWith('run-key-')||key==='run-repo'){
    const w=(state.data?.workflows||[]).find(x=>x.id===state.genericTemplateId);
    const row=sopRunRequirements(w).find(r=>r.key===key);
    $('#genericTemplateModal').classList.remove('open');
    if(row?.fix==='repository')return openRepositoryModal();
    gotoCredential(row?.field||(key==='run-key'?'codeBuddyApiKey':'gitToken'));
    return;
  }
  const meta=GENERIC_REQUIREMENT_CHECKS[key];if(!meta)return;
  $('#genericTemplateModal').classList.remove('open');
  if(meta.fix==='mcp'){
    const query=key==='tapd-mcp'?'tapd':'gongfeng';
    state.mcpScope='project';state.mcpQuery=query;
    const input=$('#mcpMarketSearch');if(input)input.value=query;
    switchView('mcp');
    toast(query==='tapd'?'在「能力资产 › MCP」添加 TAPD MCP，添加后回到通用模板查看状态':'在「能力资产 › MCP」添加 Gongfeng MCP，添加后回到通用模板查看状态');
    return;
  }
  switchView('settings');
  toast('在「个人凭证」中配置后，重新打开通用模板即可看到最新状态');
}
async function adoptGenericTemplate(){
  const src=(state.data?.workflows||[]).find(w=>w.id===state.genericTemplateId&&catalogGeneric(w));
  if(!src)return toast('通用模板不存在或已下架');
  if(!state.projectFilter)return toast('请先在顶部选择一个项目');
  const button=$('#confirmGenericTemplate');button.disabled=true;button.textContent='复制中…';
  try{
    const copied=await api(`/api/workflows/${encodeURIComponent(src.id)}/clone`,{method:'POST',body:JSON.stringify({id:uid('sop'),name:src.name.replace(/^通用/,''),owner:src.owner||'',projectId:state.projectFilter})});
    state.data.workflows.unshift(copied);
    state.workflow=clone(copied);state.selectedNode=null;state.sopScope='project';markWorkflowClean();
    $('#genericTemplateModal').classList.remove('open');
    renderAll();switchView('flow');
    const missing=genericRequirementRows(copied).filter(r=>!r.ok);
    toast(missing.length?`已复制到本项目；${missing.length} 项运行依赖未配置（${missing.map(r=>r.label).join('、')}）`:'已复制到本项目，可直接修改和运行',missing.length?'':'ok');
  }catch(e){toast(e.message||'复制通用模板失败')}
  finally{button.disabled=false;button.textContent='复制到本项目'}
}
function renderSOPCatalog(){
  renderCatalogScopes();
  let workflows=scopedWorkflows();
  const metrics=new Map((state.data.workflowMetrics||[]).map(m=>[m.workflowId,m]));
  const live=liveRuns();
  const all=[...metrics.values()],totalRuns=all.reduce((n,m)=>n+m.totalRuns,0),succeeded=all.reduce((n,m)=>n+m.succeededRuns,0),failed=all.reduce((n,m)=>n+m.failedRuns,0),active=live.length,tokens=all.reduce((n,m)=>n+m.totalTokens,0);
  const scopeLabel=state.sopScope==='generic'?'通用':state.sopScope==='shared'?'共享':'项目';
  $('#sopOverview').innerHTML=`<div class="overview-metric primary"><span>本项目运行</span><b>${totalRuns}</b><small>${workflows.length} 条${scopeLabel} SOP</small></div><div class="overview-metric success"><span>已闭环</span><b>${succeeded}</b><small>${totalRuns?percent(succeeded*100/totalRuns):'暂无运行'}</small></div><div class="overview-metric live"><span>进行中 / 待审批</span><b>${active}</b><small>实时更新 · 不含中断残留</small></div><div class="overview-metric danger${failed>0?' is-bad':''}"><span>失败运行</span><b>${failed}</b><small>${totalRuns?percent(failed*100/totalRuns):'0%'}</small></div><div class="overview-metric"><span>Token 消耗</span><b>${formatTokens(tokens)}</b><small>当前项目累计</small></div>`;
  const filter=$('#sopHealthFilter')?.value||'all',sort=$('#sopSort')?.value||'recent';
  if(filter!=='all')workflows=workflows.filter(w=>metricHealth(metrics.get(w.id), live.filter(r=>r.workflowId===w.id).length).key===filter);
  workflows.sort((a,b)=>{const x=metrics.get(a.id)||{},y=metrics.get(b.id)||{};if(sort==='runs')return (y.totalRuns||0)-(x.totalRuns||0);if(sort==='closure')return (y.closureRate||0)-(x.closureRate||0);if(sort==='failures')return (y.failedRuns||0)-(x.failedRuns||0);return new Date(y.lastRunAt||0)-new Date(x.lastRunAt||0)});
  $('#sopStats').innerHTML=`<span class="issue-stat">${workflows.length} SOPs</span><span class="issue-stat">${workflows.filter(w=>w.status==='published').length} 已发布</span><span class="issue-stat">${new Set(workflows.map(w=>w.owner).filter(Boolean)).size} 负责人</span>`;
  const empty=state.sopScope==='generic'?'通用模板暂时不可用。它们由平台维护、与项目无关，所有项目都能复制使用。':state.sopScope==='shared'?'还没有共享给当前项目的 SOP。把本项目模板设为共享后，其他项目会在这里看到。':'当前项目还没有 SOP。可以新建，或从共享模板 / 通用模板复制一份。';
  const emptyText=filter!=='all'&&scopedWorkflows().length?'当前筛选下没有 SOP。可以切换健康状态查看全部。':empty;
  const sopCatalog=$('#sopCatalog');
  bindSOPCatalogEvents(sopCatalog);
  const before=sopCatalogMotion.snapshot(sopCatalog),focus=sopCatalogFocus(sopCatalog);
  sopCatalog.innerHTML=workflows.map(w=>sopCatalogCard(w,metrics,live)).join('')||`<div class="empty-properties sop-empty">${huiIcon('workflow-empty')}<b>${emptyText}</b></div>`;
  window.AgentFlowHarnessIcons?.hydrate?.(sopCatalog);
  sopCatalogMotion.play(sopCatalog,before,[state.projectFilter,state.sopScope,filter,sort].join('|'));
  restoreSOPCatalogFocus(sopCatalog,focus);
  syncSOPCardMenu(sopCatalog);
}
async function refreshWorkflowMetrics(){try{const opt={silent:true};const [metrics,runs]=await Promise.all([api('/api/workflow-metrics'+projectQuery(),opt),api('/api/runs',opt)]);state.data.workflowMetrics=metrics;state.data.runs=runs;updateApprovalBadge();renderSOPCatalog()}catch(e){toast(e.message)}}

