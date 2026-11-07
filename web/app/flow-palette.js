// Flow designer chrome: line icon set, node palette and the SOP switcher.
const flowPalette=(()=>{
  const COLLAPSE_KEY='agentflow.flowPaletteCollapsed',MIME='application/x-agentflow-node';
  const ICONS={
    search:'<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>',
    chev:'<path d="m6 9 6 6 6-6"/>',chevr:'<path d="m9 6 6 6-6 6"/>',
    workflow:'<rect x="3" y="3" width="7" height="7" rx="2"/><rect x="14" y="14" width="7" height="7" rx="2"/><path d="M6.5 10v3a2 2 0 0 0 2 2H14"/>',
    box:'<path d="M21 8 12 3 3 8v8l9 5 9-5z"/><path d="m3 8 9 5 9-5M12 13v8"/>',
    plus:'<path d="M12 5v14M5 12h14"/>',minus:'<path d="M5 12h14"/>',
    history:'<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5M12 7v5l3 2"/>',
    sliders:'<path d="M4 6h10M18 6h2M4 12h4M12 12h8M4 18h12"/><circle cx="16" cy="6" r="2"/><circle cx="10" cy="12" r="2"/><circle cx="18" cy="18" r="2"/>',
    check:'<path d="M20 6 9 17l-5-5"/>',upload:'<path d="M12 15V4M7 9l5-5 5 5M5 20h14"/>',
    panel:'<rect x="3" y="4" width="18" height="16" rx="2.5"/><path d="M9 4v16"/>',
    hand:'<path d="M18 11V6a2 2 0 0 0-4 0M14 10V4a2 2 0 0 0-4 0v2M10 10.5V6a2 2 0 0 0-4 0v8"/><path d="M18 8a2 2 0 1 1 4 0v6a8 8 0 0 1-8 8h-2c-2.8 0-4.5-.9-6-2.4l-3.6-3.6a2 2 0 0 1 2.8-2.8L7 15"/>',
    bot:'<rect x="4" y="8" width="16" height="12" rx="3"/><path d="M12 8V4M9 13v2M15 13v2M2 14h2M20 14h2"/>',
    terminal:'<path d="m5 7 5 5-5 5M12 17h7"/>',
    layers:'<path d="m12 3 9 5-9 5-9-5z"/><path d="m3 13 9 5 9-5"/>',
    eye:'<path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',
    shield:'<path d="M12 21s7.5-3.6 7.5-9.5V5.5L12 3 4.5 5.5v6C4.5 17.4 12 21 12 21z"/><path d="m9 12 2 2 4-4"/>',
    grip:'<circle cx="9" cy="6" r="1"/><circle cx="15" cy="6" r="1"/><circle cx="9" cy="12" r="1"/><circle cx="15" cy="12" r="1"/><circle cx="9" cy="18" r="1"/><circle cx="15" cy="18" r="1"/>',
    more:'<circle cx="5" cy="12" r="1.2"/><circle cx="12" cy="12" r="1.2"/><circle cx="19" cy="12" r="1.2"/>',
    undo:'<path d="M9 14 4 9l5-5"/><path d="M4 9h10.5a5.5 5.5 0 0 1 0 11H11"/>',
    redo:'<path d="m15 14 5-5-5-5"/><path d="M20 9H9.5a5.5 5.5 0 0 0 0 11H13"/>',
    fit:'<path d="M8 3H5a2 2 0 0 0-2 2v3M16 3h3a2 2 0 0 1 2 2v3M3 16v3a2 2 0 0 0 2 2h3M21 16v3a2 2 0 0 1-2 2h-3"/>',
    layout:'<rect x="3" y="4" width="6" height="5" rx="1.5"/><rect x="15" y="4" width="6" height="5" rx="1.5"/><rect x="9" y="15" width="6" height="5" rx="1.5"/><path d="M6 9v2a2 2 0 0 0 2 2h8a2 2 0 0 0 2-2V9M12 13v2"/>',
    map:'<path d="m3 6 6-2 6 2 6-2v14l-6 2-6-2-6 2z"/><path d="M9 4v14M15 6v14"/>',
    sparkles:'<path d="M12 3.5 13.8 9 19.5 11 13.8 13 12 18.5 10.2 13 4.5 11 10.2 9z"/><path d="M19 3v3M17.5 4.5h3"/>',
    trash:'<path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/>',
    x:'<path d="M18 6 6 18M6 6l12 12"/>',
    pencil:'<path d="M4 20h4L19 9a2.8 2.8 0 0 0-4-4L4 16z"/>',
    type:'<path d="M5 7V5h14v2M9 19h6M12 5v14"/>',
    copy:'<rect x="9" y="9" width="11" height="11" rx="2.5"/><path d="M5 15a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2"/>',
    stop:'<rect x="6" y="6" width="12" height="12" rx="2.5"/>',
    rotate:'<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5"/>',
    info:'<circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 8h0"/>',
    link:'<path d="M7 17 17 7M9 7h8v8"/>',
    file:'<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/>',
    code:'<path d="m8 8-4 4 4 4M16 8l4 4-4 4"/>',
    arrowUp:'<path d="M12 19V5M6 11l6-6 6 6"/>'
  };
  function icon(name,size=16,stroke=1.8){
    return `<svg class="fd-ic" width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="${stroke}" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICONS[name]||''}</svg>`;
  }
  function paint(root){
    (root||document).querySelectorAll('[data-fd-ic]').forEach(el=>{
      if(el.dataset.fdPainted===el.dataset.fdIc)return;
      el.querySelector(':scope>svg.fd-ic')?.remove();
      el.insertAdjacentHTML('afterbegin',icon(el.dataset.fdIc,Number(el.dataset.s)||16));
      el.dataset.fdPainted=el.dataset.fdIc;
    });
  }
  // Stable per-role tint so the same role keeps its colour across nodes.
  const ROLE_TINTS=['79 107 255','236 72 153','16 163 127','217 119 6','139 92 246','14 165 233','220 38 38','101 163 13'];
  function roleTint(id){let h=0;for(const ch of String(id||''))h=(h*31+ch.charCodeAt(0))>>>0;return ROLE_TINTS[h%ROLE_TINTS.length]}
  const initial=name=>(String(name||'?').trim()[0]||'?').toUpperCase();
  function roleDesc(a){
    const exec=(state.data?.executors||[]).find(x=>x.id===a.executorId);
    const model=a.model||exec?.model||exec?.name||'未选模型';
    if(catalogGeneric(state.workflow))return `${model} · 通用模板`;
    const projectId=state.workflow?.projectId||state.projectFilter||'';
    if(a.projectId===projectId)return `${model} · 本项目`;
    const owner=(state.data?.projects||[]).find(p=>p.id===a.projectId)?.name||a.projectId;
    return `${model} · 共享自 ${owner}`;
  }
  const matches=(q,...texts)=>!q||texts.join(' ').toLowerCase().includes(q);

  function render(){
    const host=document.getElementById('flowPaletteRoles');if(!host)return;
    const input=document.getElementById('flowPaletteQ'),raw=(input?.value||'').trim(),q=raw.toLowerCase();
    const roles=workflowAgents().map(a=>({id:a.id,name:a.name||a.id,desc:roleDesc(a)})).filter(r=>matches(q,r.name,r.desc));
    const all=workflowAgents().length;
    host.innerHTML=roles.map(r=>`<div class="fd-pal-item" role="button" tabindex="0" draggable="true" data-add="agent" data-agent-id="${escapeHTML(r.id)}" data-tip="${escapeHTML(r.name)}"><span class="fd-tile" style="--tint:${roleTint(r.id)}">${escapeHTML(initial(r.name))}</span><div class="fd-pal-text"><b>${escapeHTML(r.name)}</b><small>${escapeHTML(r.desc)}</small></div><span class="fd-grip">${icon('grip',14)}</span></div>`).join('')
      ||(!q&&!all?`<div class="fd-pal-none">${state.workflow?'这个项目还没有角色':'先选择一条 SOP'}${state.workflow&&!catalogGeneric(state.workflow)?'<button type="button" class="fd-link" data-pal-agents>去角色定义</button>':''}</div>`:'');
    const count=document.getElementById('flowPaletteRoleCount');if(count)count.textContent=all?String(roles.length):'';
    let shown=roles.length;
    document.querySelectorAll('#flowPaletteBody .fd-pal-group').forEach(group=>{
      if(group.dataset.palGroup==='role'){group.hidden=!!q&&!roles.length;return}
      let visibleItems=0;
      group.querySelectorAll('.fd-pal-item').forEach(item=>{const ok=matches(q,item.textContent);item.hidden=!ok;if(ok)visibleItems++});
      group.hidden=!visibleItems;shown+=visibleItems;
    });
    const empty=document.getElementById('flowPaletteEmpty');
    if(empty){empty.hidden=!!shown||!q;empty.textContent=`没有匹配「${raw}」的节点`}
    const pal=document.getElementById('flowPalette');
    pal?.classList.toggle('readonly',typeof flowReadOnly==='function'&&flowReadOnly());
  }
  function payload(el){return el.dataset.agentId?{type:'agent',agentId:el.dataset.agentId}:{type:el.dataset.add}}
  function setCollapsed(on){
    const pal=document.getElementById('flowPalette');if(!pal)return;
    pal.classList.toggle('collapsed',on);
    localStorage.setItem(COLLAPSE_KEY,on?'1':'0');
    const btn=document.getElementById('flowPaletteCollapse');if(btn)btn.dataset.tip=on?'展开节点库':'收起节点库';
    flowViewport.paletteToggled();
    setTimeout(()=>flowViewport.layoutTight(),220);
  }
  function bindDrop(){
    const c=document.getElementById('canvas');if(!c)return;
    const accepts=e=>[...(e.dataTransfer?.types||[])].includes(MIME)&&!flowReadOnly();
    c.addEventListener('dragover',e=>{if(!accepts(e))return;e.preventDefault();e.dataTransfer.dropEffect='copy';c.classList.add('dragover')});
    c.addEventListener('dragleave',e=>{if(!c.contains(e.relatedTarget))c.classList.remove('dragover')});
    c.addEventListener('drop',e=>{
      c.classList.remove('dragover');
      const raw=e.dataTransfer?.getData(MIME);if(!raw)return;
      e.preventDefault();
      let item;try{item=JSON.parse(raw)}catch{return}
      const w=flowViewport.toWorld(e.clientX,e.clientY);
      addNode(item.type,{agentId:item.agentId,at:{x:w.x-122,y:w.y-28}});
    });
  }
  function bind(){
    const body=document.getElementById('flowPaletteBody');if(!body||body.dataset.bound)return;
    body.dataset.bound='1';
    const pal=document.getElementById('flowPalette');
    if(localStorage.getItem(COLLAPSE_KEY)==='1')pal?.classList.add('collapsed');
    document.getElementById('flowPaletteQ')?.addEventListener('input',render);
    document.getElementById('flowPaletteCollapse')?.addEventListener('click',()=>setCollapsed(!pal.classList.contains('collapsed')));
    body.addEventListener('click',e=>{
      if(e.target.closest('[data-pal-agents]')){switchView('agents');return}
      const item=e.target.closest('.fd-pal-item');if(!item)return;
      const p=payload(item);addNode(p.type,{agentId:p.agentId});
    });
    body.addEventListener('keydown',e=>{
      if(e.key!=='Enter'&&e.key!==' ')return;
      const item=e.target.closest('.fd-pal-item');if(!item)return;
      e.preventDefault();item.click();
    });
    body.addEventListener('dragstart',e=>{
      const item=e.target.closest('.fd-pal-item');if(!item)return;
      if(flowReadOnly()){e.preventDefault();toast(flowReadOnlyReason());return}
      e.dataTransfer.setData(MIME,JSON.stringify(payload(item)));
      e.dataTransfer.effectAllowed='copy';
    });
    bindDrop();
    bindSwitcher();
    paint(document.getElementById('flowView'));
    render();
  }

  // ---- SOP switcher ----
  function workflowStatus(w){
    if(catalogGeneric(w))return {cls:'ro',label:'只读模板'};
    if(catalogForeign(w.projectId))return {cls:'ro',label:'共享 · 只读'};
    if(w.status==='published')return {cls:'ok',label:'已发布'};
    if(w.status==='archived')return {cls:'ro',label:'已归档'};
    return {cls:'draft',label:'草稿'};
  }
  function switcherGroups(){
    const wf=state.workflow,listed=catalogWorkflows(),generic=(state.data?.workflows||[]).filter(catalogGeneric);
    const own=listed.filter(w=>!catalogForeign(w.projectId)),shared=listed.filter(w=>catalogForeign(w.projectId));
    if(wf&&!listed.some(w=>w.id===wf.id)&&!generic.some(w=>w.id===wf.id))own.unshift(wf);
    return [{title:'本项目',items:own},{title:'共享给本项目',items:shared},{title:'通用模板 · 只读',items:generic}];
  }
  function renderSwitcher(){
    const host=document.getElementById('flowSwitchList');if(!host)return;
    const q=(document.getElementById('flowSwitchQ')?.value||'').trim().toLowerCase(),cur=state.workflow?.id;
    host.innerHTML=switcherGroups().map(g=>{
      const items=g.items.filter(w=>matches(q,w.name||'',w.id||''));
      if(!items.length)return '';
      return `<div class="fd-pop-gh">${escapeHTML(g.title)}</div>`+items.map(w=>{const s=workflowStatus(w),current=w.id===cur,name=w.name||'未命名 SOP';return `<button type="button" class="fd-pop-item ${current?'cur':''}" role="option" aria-selected="${current}" data-switch-workflow="${escapeHTML(w.id)}"><span class="fd-sd ${s.cls}"></span><span class="fd-pop-text"><span class="fd-pop-title"><b>${escapeHTML(name)}</b><span class="fd-pop-copy" role="button" tabindex="0" data-copy-sop-name="${escapeHTML(name)}" aria-label="复制 SOP 名称" title="复制 SOP 名称">${icon('copy',14,1.7)}</span></span><small>${s.label} · v${w.version||1} · ${(w.nodes||[]).length} 步</small></span>${current?`<span class="fd-ck">${icon('check',15,2.2)}</span>`:''}</button>`}).join('');
    }).join('')||`<div class="fd-pal-empty">没有匹配的 SOP</div>`;
  }
  function isSwitcherOpen(){const p=document.getElementById('flowSwitchPop');return !!p&&!p.hidden}
  function setSwitcher(open){
    const pop=document.getElementById('flowSwitchPop');if(!pop)return false;
    const was=!pop.hidden;pop.hidden=!open;
    document.getElementById('flowIdent')?.classList.toggle('open',open);
    document.getElementById('flowSwitch')?.setAttribute('aria-expanded',String(open));
    if(open){const q=document.getElementById('flowSwitchQ');if(q)q.value='';renderSwitcher();setTimeout(()=>q?.focus(),0)}
    return was!==open;
  }
  function closeSwitcher(){return isSwitcherOpen()&&setSwitcher(false)}
  function bindSwitcher(){
    const ident=document.getElementById('flowIdent');if(!ident)return;
    ident.addEventListener('click',e=>{if(e.target.closest('#sandboxChip,#copyWorkflowName'))return;setSwitcher(!isSwitcherOpen())});
    document.getElementById('copyWorkflowName')?.addEventListener('click',e=>{
      e.preventDefault();e.stopPropagation();
      const name=(state.workflow?.name||'').trim()||'未命名流程';
      if(!state.workflow||typeof copyText!=='function')return;
      copyText(name,'SOP 名称已复制');
    });
    const q=document.getElementById('flowSwitchQ');
    q?.addEventListener('input',renderSwitcher);
    q?.addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();document.querySelector('#flowSwitchList [data-switch-workflow]')?.click()}});
    document.getElementById('flowSwitchList')?.addEventListener('click',e=>{
      const copy=e.target.closest('[data-copy-sop-name]');
      if(copy){
        e.preventDefault();e.stopPropagation();
        const name=(copy.getAttribute('data-copy-sop-name')||'').trim()||'未命名流程';
        if(typeof copyText==='function')copyText(name,'SOP 名称已复制');
        return;
      }
      const item=e.target.closest('[data-switch-workflow]');if(!item)return;
      setSwitcher(false);
      if(item.dataset.switchWorkflow!==state.workflow?.id)switchFlowWorkflow(item.dataset.switchWorkflow);
    });
    document.getElementById('flowSwitchList')?.addEventListener('keydown',e=>{
      const copy=e.target.closest?.('[data-copy-sop-name]');
      if(!copy||(e.key!=='Enter'&&e.key!==' '))return;
      e.preventDefault();e.stopPropagation();
      copy.click();
    });
    document.getElementById('newSOPFromFlow')?.addEventListener('click',()=>{
      setSwitcher(false);openSOPWizard();
      const blank=document.querySelector('#sopTemplateChoices input[value="blank"]');
      if(blank){blank.checked=true;blank.dispatchEvent(new Event('change'))}
    });
    document.getElementById('flowFromTemplate')?.addEventListener('click',()=>{setSwitcher(false);openSOPWizard()});
    document.addEventListener('pointerdown',e=>{if(isSwitcherOpen()&&!e.target.closest('#flowSwitchPop,#flowIdent'))setSwitcher(false)});
  }
  return {icon,paint,render,bind,roleTint,initial,renderSwitcher,closeSwitcher,isSwitcherOpen,setSwitcher,switcherGroups,MIME};
})();
