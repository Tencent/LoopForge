function renderAgentsLegacy(){
  const list=$('#agentList');list.innerHTML=state.data.agents.map(a=>`<div class="agent-card ${state.selectedAgent===a.id?'active':''}" data-id="${a.id}"><div class="agent-card-top"><h3>${escapeHTML(a.name)}</h3><span class="pill">v${a.version}</span></div><p>${escapeHTML(a.description||'暂无说明')}</p><div class="chips"><span class="chip">${a.rules?.length||0} rules</span><span class="chip">${a.skills?.length||0} skills</span><span class="chip">${a.mcpServers?.length||0} MCP</span></div></div>`).join('');
  list.querySelectorAll('.agent-card').forEach(el=>el.onclick=()=>selectAgent(state.data.agents.find(a=>a.id===el.dataset.id)));
  if(!state.selectedAgent&&state.data.agents[0])selectAgent(state.data.agents[0],false);
}
function renderAgents(){
  const list=$('#agentList');if(!list)return;renderCatalogScopes();
  const query=($('#agentSearch')?.value||'').trim().toLowerCase(),agents=scopedAgents().filter(a=>!query||`${a.name} ${a.id} ${a.description} ${(a.rules||[]).join(' ')} ${(a.skills||[]).join(' ')}`.toLowerCase().includes(query));
  const used=new Map();(state.data.workflows||[]).forEach(w=>(w.nodes||[]).forEach(n=>{if(n.agentId)used.set(n.agentId,(used.get(n.agentId)||0)+1)}));
  $('#agentCatalogStats').innerHTML=`<span><b>${agents.length}</b> 个${state.agentScope==='shared'?'共享':'项目'}角色</span><span><b>${agents.reduce((n,a)=>n+(a.skills?.length||0),0)}</b> 项技能绑定</span><span><b>${agents.reduce((n,a)=>n+(a.mcpServers?.length||0),0)}</b> 个 MCP 连接</span>`;
  const empty=state.agentScope==='shared'?'还没有共享给当前项目的角色。':'当前项目还没有角色。可以新建，或从共享角色复制后改。';
  closeAgentRowMenu();
  state.agentUsage=used;
  const rows=agents.map(a=>{const exec=(state.data.executors||[]).find(x=>x.id===a.executorId);const model=a.model||exec?.model||exec?.name||'';const uses=used.get(a.id)||0;return`<div class="agent-row" role="button" tabindex="0" data-agent-id="${escapeHTML(a.id)}" title="${escapeHTML(`${a.id} · v${a.version||1}`)}"><div class="agent-row-who"><div><b>${escapeHTML(a.name)}</b>${agentRowTag(a)}<small>${escapeHTML(a.description||'未填写职责')}</small></div></div><div class="agent-row-model${model?'':' none'}">${escapeHTML(model||'未选模型')}</div><div class="agent-row-config agent-col-config">${agentConfigSummary(a)}</div><div class="agent-row-uses agent-col-uses${uses?'':' zero'}">${uses?`${uses} 个节点`:'未使用'}</div><div class="agent-row-more"><button type="button" class="agent-more-btn" data-agent-more="${escapeHTML(a.id)}" aria-label="更多操作" aria-haspopup="menu" aria-expanded="false"><svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><circle cx="5" cy="12" r="1.6"/><circle cx="12" cy="12" r="1.6"/><circle cx="19" cy="12" r="1.6"/></svg></button></div></div>`}).join('');
  const head='<div class="agent-row agent-row-head" aria-hidden="true"><div>角色</div><div>模型</div><div class="agent-col-config">配置</div><div class="agent-col-uses">引用</div><div></div></div>';
  list.innerHTML=rows?head+rows:(query&&scopedAgents().length?'<div class="catalog-empty"><span>AI</span><b>没有匹配的角色。</b><p>调整搜索，或新建一个角色。</p></div>':`<div class="catalog-empty"><span>AI</span><b>${empty}</b><p>调整搜索，或新建一个角色。</p></div>`);
  list.querySelectorAll('[data-agent-more]').forEach(btn=>btn.onclick=e=>{e.stopPropagation();openAgentRowMenu(btn,state.data.agents.find(a=>a.id===btn.dataset.agentMore))});
  list.querySelectorAll('.agent-row[data-agent-id]').forEach(row=>{
    const open=()=>openAgentWizard(state.data.agents.find(a=>a.id===row.dataset.agentId));
    row.onclick=open;
    row.onkeydown=e=>{if(e.target===row&&(e.key==='Enter'||e.key===' ')){e.preventDefault();open()}};
  });
  syncTabIndicator($('#agentsView [data-scope-for="agent"]'));
}
function agentRowTag(a){
  if(catalogOwned(a.projectId))return a.shared?'<span class="agent-row-tag shared">已共享</span>':'';
  return `<span class="agent-row-tag">${!a.projectId?'平台':`来自 ${escapeHTML((state.data.projects||[]).find(p=>p.id===a.projectId)?.name||a.projectId)}`}</span>`;
}
function agentConfigSummary(a){
  const parts=[];
  if(a?.loop?.enabled)parts.push(`<span class="loop">${a.loop.maxIterations||1} 轮 Loop</span>`);
  [[a?.artifactOutputs?.length,'产物'],[a?.verification?.length,'门禁'],[a?.rules?.length,'规则'],[a?.skills?.length,'技能'],[a?.mcpServers?.length,'MCP'],[a?.hooks?.length,'Hook']].forEach(([n,label])=>{if(n)parts.push(`${n} ${label}`)});
  return parts.length?parts.join(' · '):'<span class="empty">未配置</span>';
}
function agentDeleteBlockReason(a){
  if(a.shared)return '共享状态的角色不能删除，请先取消共享';
  if(state.agentUsage?.get(a.id))return '角色仍被流程引用，请先解除引用';
  return '';
}
function openAgentRowMenu(btn,a){
  const menu=$('#agentRowMenu');if(!menu||!a)return;
  const wasOpen=state.agentMenuFor===a.id&&!menu.hidden;
  closeAgentRowMenu();
  if(wasOpen)return;
  const id=escapeHTML(a.id);
  if(catalogOwned(a.projectId)){
    const why=agentDeleteBlockReason(a);
    menu.innerHTML=`<button type="button" role="menuitem" data-agent-act="edit">编辑</button><button type="button" role="menuitem" data-agent-act="clone">复制</button><button type="button" role="menuitem" data-agent-act="share">${a.shared?'取消共享':'共享给其他项目'}</button><hr><button type="button" role="menuitem" class="danger" data-agent-act="delete" ${why?`disabled title="${why}"`:''}>删除</button>${why?`<small>${why}</small>`:''}`;
  }else{
    menu.innerHTML=`<button type="button" role="menuitem" data-agent-act="edit">查看</button><button type="button" role="menuitem" data-agent-act="clone">复制到本项目</button>`;
  }
  menu.dataset.agentId=a.id;state.agentMenuFor=a.id;
  menu.hidden=false;btn.setAttribute('aria-expanded','true');btn.closest('.agent-row')?.classList.add('menu-open');
  const r=btn.getBoundingClientRect(),h=menu.offsetHeight,w=menu.offsetWidth;
  let top=r.bottom+4;if(top+h>window.innerHeight-8)top=r.top-h-4;
  menu.style.top=`${Math.max(8,top)}px`;menu.style.left=`${Math.max(8,Math.min(r.right-w,window.innerWidth-w-8))}px`;
  menu.querySelectorAll('[data-agent-act]').forEach(item=>item.onclick=e=>{
    e.stopPropagation();if(item.disabled)return;
    const agent=state.data.agents.find(x=>x.id===menu.dataset.agentId);closeAgentRowMenu();if(!agent)return;
    const act=item.dataset.agentAct;
    if(act==='edit')openAgentWizard(agent);
    else if(act==='clone')cloneAgent(agent.id);
    else if(act==='share')toggleShare('agent',agent.id);
    else if(act==='delete')deleteAgent(agent.id);
  });
  if(!state.agentMenuBound){
    state.agentMenuBound=true;
    document.addEventListener('click',e=>{const m=$('#agentRowMenu');if(m&&!m.hidden&&!m.contains(e.target))closeAgentRowMenu()});
    document.addEventListener('keydown',e=>{if(e.key==='Escape')closeAgentRowMenu()});
    window.addEventListener('scroll',e=>{const m=$('#agentRowMenu');if(m&&!m.hidden&&!m.contains(e.target))closeAgentRowMenu()},true);
    window.addEventListener('resize',()=>closeAgentRowMenu());
  }
  menu.querySelector('button:not([disabled])')?.focus({preventScroll:true});
}
function closeAgentRowMenu(){
  const menu=$('#agentRowMenu');if(!menu)return;
  menu.hidden=true;state.agentMenuFor='';
  $$('#agentList [data-agent-more][aria-expanded="true"]').forEach(b=>{b.setAttribute('aria-expanded','false');b.closest('.agent-row')?.classList.remove('menu-open')});
}
const AGENT_TAB_INSET=10;
function syncTabIndicator(container){
  if(!container)return;
  let ind=container.querySelector(':scope > .agent-tab-ind');
  if(!ind){ind=document.createElement('span');ind.className='agent-tab-ind';ind.setAttribute('aria-hidden','true');container.appendChild(ind)}
  const tabs=[...container.querySelectorAll(':scope > .agent-tab')],active=tabs.find(t=>t.classList.contains('on'));
  tabs.forEach(t=>{t.setAttribute('aria-selected',t===active?'true':'false');t.tabIndex=t===active?0:-1});
  if(typeof ResizeObserver==='function'){
    state.agentTabObserver=state.agentTabObserver||new ResizeObserver(entries=>{const seen=new Set();entries.forEach(en=>{const c=en.target.closest('.agent-tabs');if(c&&!seen.has(c)){seen.add(c);syncTabIndicator(c)}})});
    [container,...tabs].forEach(el=>{if(!el._agentTabObserved){el._agentTabObserved=true;state.agentTabObserver.observe(el)}});
  }
  if(!active||!active.offsetWidth){ind.style.opacity='0';return}
  ind.style.opacity='1';
  ind.style.width=`${Math.max(0,active.offsetWidth-AGENT_TAB_INSET*2)}px`;
  ind.style.transform=`translateX(${active.offsetLeft+AGENT_TAB_INSET}px)`;
}
function bindAgentTabKeys(container){
  if(!container||container._agentTabKeys)return;
  container._agentTabKeys=true;
  container.addEventListener('keydown',e=>{
    if(!['ArrowLeft','ArrowRight','Home','End'].includes(e.key))return;
    const tabs=[...container.querySelectorAll(':scope > .agent-tab')].filter(t=>!t.disabled&&t.offsetWidth);
    const i=tabs.indexOf(document.activeElement);if(i<0||!tabs.length)return;
    e.preventDefault();
    const next=e.key==='Home'?tabs[0]:e.key==='End'?tabs[tabs.length-1]:tabs[(i+(e.key==='ArrowRight'?1:-1)+tabs.length)%tabs.length];
    next.click();next.focus();
  });
}
function bindAgentModalInputs(){
  const f=$('#agentForm');if(!f)return;
  f.elements.name?.addEventListener('input',()=>{if(f.elements.name.value.trim())$('#agentNameError').hidden=true;syncAgentSlug()});
  f.elements.id?.addEventListener('input',()=>{state.agentIdAuto=false;$('#agentIdError').hidden=true;syncAgentSlug()});
  $('#agentIdEdit')?.addEventListener('click',()=>{showAgentIdField('');f.elements.id.focus()});
  f.addEventListener('invalid',e=>{if(state.agentModalMode==='edit'&&e.target.closest('[data-agent-pane="identity"]')&&state.agentTab!=='identity'){setAgentTab('identity');setTimeout(()=>e.target.reportValidity(),0)}},true);
  $$('#agentsView .agent-tabs, #agentModal .agent-tabs').forEach(bindAgentTabKeys);
}
function syncAgentTabIndicators(){$$('#agentsView .agent-tabs, #agentModal .agent-tabs').forEach(syncTabIndicator)}
if(typeof document!=='undefined'&&document.fonts?.ready)document.fonts.ready.then(()=>syncAgentTabIndicators()).catch(()=>{});
function pickerHTML(items,selected,prefix){return `<input class="input picker-filter" data-picker-filter="${prefix}" placeholder="筛选名称或说明…">${(items||[]).map(x=>`<label class="asset-check"><input type="checkbox" name="${prefix}" value="${x.id}" ${(selected||[]).includes(x.id)?'checked':''}><span>${escapeHTML(x.name)}<small>${escapeHTML(x.description||x.id)}</small></span></label>`).join('')||'<span class="type-label">暂无可用资产，请先到对应的资产管理页面添加</span>'}`}
function bindPickerFilters(root){(root||document).querySelectorAll('[data-picker-filter]').forEach(input=>input.oninput=()=>{const q=input.value.trim().toLowerCase();input.parentElement.querySelectorAll('.asset-check').forEach(row=>row.hidden=!!q&&!row.textContent.toLowerCase().includes(q))})}
function executorOptions(selected){const allowed=new Set(['codebuddy','codex','claude']),items=(state.data.executors||[]).filter(x=>allowed.has(x.id)),legacy=selected&&!allowed.has(selected)?`<option value="${escapeHTML(selected)}" selected disabled>历史执行器 ${escapeHTML(selected)}（请重新选择 CLI）</option>`:'';return legacy+items.map(x=>`<option value="${escapeHTML(x.id)}" ${x.id===selected?'selected':''}>${escapeHTML(x.name||x.id)}</option>`).join('')}
const AGENT_CUSTOM_MODEL='__custom__';
function executorModelCatalogs(){return window.AgentFlowExecutorModels||{}}
async function loadExecutorModelCatalogs(){
  // 未登录时接口 401，失败结果不缓存：下次打开向导会重试。
  const cached=window.AgentFlowExecutorModels;
  if(cached&&Object.keys(cached).length)return cached;
  try{
    const data=await api('/api/executor-models',{silent:true});
    if(data&&data.codebuddy&&data.codex&&data.claude)window.AgentFlowExecutorModels=data;
  }catch{}
  return executorModelCatalogs();
}
function agentModelAPI(){return window.AgentFlowAssistantModels}
function currentAgentExecutorId(){return $('#agentForm')?.elements?.executorId?.value||'codebuddy'}
function agentModelItems(executorId,current){
  const cat=executorModelCatalogs()[executorId]||{},items=(cat.models||[]).slice();
  if(current&&current!==AGENT_CUSTOM_MODEL&&!items.some(m=>m.id===current))items.push({id:current,name:`${current}（历史模型）`,family:'other'});
  if(cat.customModel)items.push({id:AGENT_CUSTOM_MODEL,name:'手动输入模型 ID…',icon:false});
  return items;
}
function agentEffortItems(executorId){
  const cat=executorModelCatalogs()[executorId]||{};
  return [{id:'',name:'默认',icon:false}].concat((cat.efforts||[]).map(e=>({id:e.value,name:e.label,icon:false})));
}
function paintAgentPicker(pickerId,item){
  const picker=$(pickerId);if(!picker)return;
  const toggle=picker.querySelector('[data-model-toggle]');
  agentModelAPI()?.paintItem?.(toggle,item);
  toggle?.querySelector('.ai-model-icon')?.toggleAttribute('hidden',!item||item.icon===false);
  // Only a clipped label needs the hover hint.
  const label=toggle?.querySelector('[data-model-label]');
  if(toggle&&label&&label.scrollWidth<=label.clientWidth){toggle.removeAttribute('title');toggle.removeAttribute('data-hui-title')}
}
// The empty choice runs the CLI's own default model; name it instead of
// showing an unset 「选择模型」.
function agentDefaultModelItem(executorId){
  const exec=(state.data?.executors||[]).find(x=>x.id===executorId);
  return {id:'',name:exec?.model?`默认 · ${exec.model}`:'默认模型',icon:false};
}
function renderAgentModelPicker(){
  const picker=$('#agentModelPicker');if(!picker)return;
  const current=picker.querySelector('[name="modelChoice"]').value;
  const items=[agentDefaultModelItem(currentAgentExecutorId())].concat(agentModelItems(currentAgentExecutorId(),current));
  const menu=picker.querySelector('.ai-model-menu');
  agentModelAPI()?.renderOptions?.(menu,items,current,id=>{
    picker.querySelector('[name="modelChoice"]').value=id;
    syncAgentModelCustomField();
    paintAgentPicker('#agentModelPicker',items.find(x=>x.id===id));
    menu.hidden=true;picker.querySelector('[data-model-toggle]').setAttribute('aria-expanded','false');
  });
  syncAgentModelCustomField();
  paintAgentPicker('#agentModelPicker',items.find(x=>x.id===current)||{id:current,name:current});
}
function syncAgentModelCustomField(){
  const picker=$('#agentModelPicker'),field=$('#agentModelCustomField');
  if(!picker||!field)return;
  field.hidden=picker.querySelector('[name="modelChoice"]').value!==AGENT_CUSTOM_MODEL;
}
function renderAgentEffortPicker(){
  const picker=$('#agentEffortPicker');if(!picker)return;
  const items=agentEffortItems(currentAgentExecutorId());
  const current=picker.querySelector('[name="effort"]').value;
  const menu=picker.querySelector('.ai-model-menu');
  agentModelAPI()?.renderOptions?.(menu,items,current,id=>{
    picker.querySelector('[name="effort"]').value=id;
    paintAgentPicker('#agentEffortPicker',items.find(x=>x.id===id));
    menu.hidden=true;picker.querySelector('[data-model-toggle]').setAttribute('aria-expanded','false');
  });
  paintAgentPicker('#agentEffortPicker',items.find(x=>x.id===current)||items[0]);
}
function applyAgentModelUI(executorId,model,effort){
  const picker=$('#agentModelPicker');if(!picker||!$('#agentEffortPicker'))return;
  const cat=executorModelCatalogs()[executorId]||{};
  let choice=model||'';
  if(choice&&!(cat.models||[]).some(m=>m.id===choice)&&cat.customModel)choice=AGENT_CUSTOM_MODEL;
  picker.querySelector('[name="modelChoice"]').value=choice;
  const customInput=$('#agentModelCustomField')?.querySelector('input[name="model"]');
  if(customInput&&choice===AGENT_CUSTOM_MODEL)customInput.value=model||'';
  renderAgentModelPicker();
  const efforts=cat.efforts||[];
  $('#agentEffortPicker').querySelector('[name="effort"]').value=efforts.some(e=>e.value===effort)?(effort||''):'';
  renderAgentEffortPicker();
}
function bindAgentPicker(pickerId,render){
  const picker=$(pickerId);if(!picker||picker.dataset.bound)return;
  picker.dataset.bound='1';
  picker.querySelector('[data-model-toggle]').onclick=e=>{
    e.stopPropagation();
    const menu=picker.querySelector('.ai-model-menu'),toggle=picker.querySelector('[data-model-toggle]');
    const open=menu.hidden;menu.hidden=!open;
    toggle.setAttribute('aria-expanded',open?'true':'false');
    if(open){render();placeAgentPickerMenu(toggle,menu);toggle.removeAttribute('title');toggle.removeAttribute('data-hui-title')}
  };
}
// The wizard body (.as-main) scrolls and clips its children, so the menu opens
// toward whichever side of the trigger has more room and never grows past it.
function placeAgentPickerMenu(toggle,menu){
  const box=toggle.closest('.as-main')?.getBoundingClientRect(),at=toggle.getBoundingClientRect();
  if(!box)return;
  const gap=6,margin=8;
  const above=at.top-box.top-gap-margin,below=box.bottom-at.bottom-gap-margin;
  const down=below>=above;
  menu.style.setProperty('top',down?`calc(100% + ${gap}px)`:'auto','important');
  menu.style.setProperty('bottom',down?'auto':`calc(100% + ${gap}px)`,'important');
  menu.style.setProperty('max-height',`${Math.max(120,Math.min(320,Math.floor(down?below:above)))}px`,'important');
}
function agentEffectiveModel(f){
  const choice=f?.elements?.modelChoice?.value;
  if(choice==null)return (f?.elements?.model?.value||'').trim();
  return choice===AGENT_CUSTOM_MODEL?(f?.elements?.model?.value||'').trim():choice;
}
function selectAgent(a,rerender=true){
  state.selectedAgent=a.id;state.agentDraft=null;
  const f=$('#agentForm');
  ['id','name','description'].forEach(k=>{if(f.elements[k])f.elements[k].value=a[k]||''});
  if(f.elements.shared)f.elements.shared.checked=!!a.shared;
  if(f.elements.executorId)f.elements.executorId.innerHTML=executorOptions(a.executorId||'codebuddy');
  const paintAgentModel=()=>applyAgentModelUI(a.executorId||f.elements.executorId?.value||'codebuddy',a.model||'',a.effort||'');
  paintAgentModel();loadExecutorModelCatalogs().then(paintAgentModel).catch(()=>{});
  if(f.elements.id)f.elements.id.readOnly=!!a.id;
  renderAgentPresets();renderAgentTaskBook(a);renderAgentGovernance(a);renderAgentAssetsPanel(a);renderAgentSummary();
  $('#agentVersion').textContent=`v${a.version||1}`;
  const idShow=$('#agentIdShow');if(idShow)idShow.value=a.id||'';
  $('#agentWizardTitle').textContent=agentWizardForeign(a)?`查看 ${a.name}`:(a.id?`编辑 ${a.name}`:'新建角色');
  syncAgentOwnershipUI(a);
  if(rerender)renderAgents();
}
function syncAgentOwnershipUI(a){
  const generic=catalogGeneric(state.workflow);
  const foreign=agentWizardForeign(a);
  const save=$('#agentSave'),cloneBtn=$('#agentClone');
  if(save)save.hidden=foreign;
  if(cloneBtn)cloneBtn.hidden=generic||!foreign;
  const share=$('#agentForm')?.elements?.shared;
  if(share)share.disabled=foreign;
}
// 「选起点」模板：项目里直接创建，不需要平台先建共享角色再复制。
const agentPresets=[{id:'developer',name:'开发实现',title:'研发工程师',desc:'根据任务书完成代码变更、验证和产物交付',prompt:'你是生产研发工程师。先读取规则、任务输入、上游冻结产物和相关源码；实施最小且完整的变更；运行构建、静态检查和测试；按节点产物契约提交真实证据。不得用自述代替磁盘产物和命令退码。'},{id:'reviewer',name:'独立评审',title:'独立代码审查员',desc:'独立检查正确性、安全性、兼容性和回归风险',prompt:'你是独立审查员，不是实现者。基于代码差异、上游产物和真实验证证据进行审查，明确阻断项、风险和结论；发现问题必须给出具体文件、原因与可执行修复建议。'},{id:'tester',name:'独立测试',title:'独立测试工程师',desc:'覆盖正常、边界、错误和回归路径',prompt:'你是独立测试工程师。根据需求与验收标准设计正常、边界、错误和回归用例，运行真实测试并记录命令、结果、覆盖率与数据清理证据。'},{id:'coordinator',name:'交付协调',title:'交付协调员',desc:'核对 PR、Checkers、Pipeline、验收与关闭证据',prompt:'你是交付协调员。只根据可核验的外部状态和冻结产物推进交付，确保 PR、Checkers、Pipeline、验收和关闭证据完整；任何关键事实缺失时停止并报告。'}];
const AGENT_TABS=['identity','task','gov','assets'];
function setAgentModalMode(mode){
  state.agentModalMode=mode==='create'?'create':'edit';
  const f=$('#agentForm');if(f)f.dataset.agentMode=state.agentModalMode;
}
function openAgentWizard(agent=null,initialStep=1){
  const open=a=>{
    const presetHost=$('#agentPresetHost');if(presetHost)presetHost.dataset.ready='';
    setAgentModalMode(a?.id?'edit':'create');
    if(state.agentModalMode==='create'){state.agentCreatePreset='blank';state.agentIdAuto=true;state.agentIdSuffix=agentSlugSuffix()}
    selectAgent(a||{id:'',name:'',version:1,projectId:state.projectFilter||'',shared:false,description:'',prompt:'',rules:[],skills:[],mcpServers:[],hooks:[],loop:{enabled:false,maxIterations:3,onExhausted:'fail'},interventions:[],artifactOutputs:[],verification:[]},false);
    resetAgentIdentityErrors();
    if(state.agentModalMode==='create'){syncAgentSlug();setAgentCreateStep(1)}
    else setAgentTab(AGENT_TABS[Math.max(1,Math.min(4,Number(initialStep)||1))-1]);
    $('#agentModal').classList.add('open');
    syncAgentTabIndicators();requestAnimationFrame(()=>syncAgentTabIndicators());
    if(catalogGeneric(state.workflow))loadMCPMarket().then(()=>{const current=workflowAgent(state.selectedAgent)||state.agentDraft||a;if(current){renderAgentAssetsPanel(current);renderAgentSummary()}});
  };
  if(agent?.id && !agent.prompt){
    api(`/api/agents/${encodeURIComponent(agent.id)}`).then(full=>{const i=state.data.agents.findIndex(x=>x.id===full.id);if(i>=0)state.data.agents[i]=full;open(full)}).catch(e=>{toast(e.message,'err');open(agent)});
    return;
  }
  open(agent);
}
function agentSlugSuffix(){return Math.random().toString(36).slice(2,6).padEnd(4,'0')}
function agentSlugFromName(name,suffix){
  const base=String(name||'').toLowerCase().replace(/[^a-z0-9]+/g,'-').replace(/^-+|-+$/g,'').slice(0,32).replace(/-+$/,'')||'role';
  return `${base}-${suffix||agentSlugSuffix()}`;
}
function agentIdTaken(id){
  const key=String(id||'').trim();
  return !!key&&[...(state.data?.agents||[]),...(state.genericTemplateAgents||[])].some(a=>a.id===key);
}
function syncAgentSlug(){
  const f=$('#agentForm');if(!f||state.agentModalMode!=='create')return;
  if(state.agentIdAuto){
    let id=agentSlugFromName(f.elements.name.value,state.agentIdSuffix);
    for(let i=0;i<20&&agentIdTaken(id);i++){state.agentIdSuffix=agentSlugSuffix();id=agentSlugFromName(f.elements.name.value,state.agentIdSuffix)}
    f.elements.id.value=id;
  }
  const preview=$('#agentIdPreview');if(preview)preview.textContent=f.elements.id.value.trim()||'—';
}
function showAgentIdField(message){
  const line=$('#agentIdLine'),field=$('#agentIdField'),err=$('#agentIdError');
  if(line)line.hidden=true;
  if(field)field.hidden=false;
  if(err){err.textContent=message||'';err.hidden=!message}
}
function resetAgentIdentityErrors(){
  const create=state.agentModalMode==='create';
  const line=$('#agentIdLine'),field=$('#agentIdField');
  if(line)line.hidden=!create;
  if(field)field.hidden=true;
  ['#agentNameError','#agentIdError'].forEach(sel=>{const el=$(sel);if(el)el.hidden=true});
}
function applyAgentPreset(id){
  const f=$('#agentForm');if(!f)return;
  const prev=agentPresets.find(x=>x.id===state.agentCreatePreset),p=agentPresets.find(x=>x.id===id);
  if(id!=='blank'&&!p)return;
  const fill=(key,next,old)=>{const el=f.elements[key];if(el&&(p||!el.value||el.value===old))el.value=next};
  fill('name',p?.title||'',prev?.title);fill('description',p?.desc||'',prev?.desc);const draft=state.agentDraft;if(draft&&(p||!draft.prompt||draft.prompt===prev?.prompt)){draft.prompt=p?.prompt||'';renderAgentTaskBook(draft)}
  state.agentCreatePreset=id;
  $$('[data-agent-preset]').forEach(b=>{const on=b.dataset.agentPreset===id;b.classList.toggle('active',on);b.setAttribute('aria-pressed',on?'true':'false')});
  if(f.elements.name.value.trim())$('#agentNameError')&&($('#agentNameError').hidden=true);
  syncAgentSlug();renderAgentSummary();
  if(p)toast(`已套用「${p.name}」模板，可直接保存或继续调整`);
}
function showAgentPane(name){
  $('#agentForm')?.querySelectorAll('.wizard-pane').forEach(p=>p.classList.toggle('active',p.dataset.agentPane===name));
  const main=$('#agentForm .as-main');if(main)main.scrollTop=0;
}
function setAgentTab(tab){
  if(!AGENT_TABS.includes(tab))tab='identity';
  state.agentTab=tab;
  state.agentWizardStep=AGENT_TABS.indexOf(tab)+1;
  showAgentPane(tab);
  $$('[data-wizard-steps="agent"] [data-agent-tab]').forEach(b=>b.classList.toggle('on',b.dataset.agentTab===tab));
  if(tab==='gov'&&!$('#agentGovPanel')?.children.length&&state.selectedAgent)renderAgentGovernance(agentWizardSubject());
  syncAgentModalChrome();
  syncTabIndicator($('#agentForm [data-wizard-steps="agent"]'));
}
function validateAgentCreateIdentity(){
  const f=$('#agentForm'),name=f.elements.name.value.trim(),id=f.elements.id.value.trim();
  if(!name){const err=$('#agentNameError');if(err)err.hidden=false;f.elements.name.focus();return false}
  if(!id){showAgentIdField('标识不能为空');f.elements.id.focus();return false}
  if(agentIdTaken(id)){showAgentIdField('标识已存在，请换一个');f.elements.id.focus();return false}
  return true;
}
function setAgentCreateStep(step){
  let n=Math.max(1,Math.min(3,Number(step)||1));
  if(n===3&&!validateAgentCreateIdentity())n=2;
  state.agentCreateStep=n;
  showAgentPane(n===1?'create-template':n===2?'identity':'create-review');
  $$('[data-wizard-steps="agent-create"] [data-create-step]').forEach(b=>{
    const k=Number(b.dataset.createStep);
    b.classList.toggle('on',k===n);b.classList.toggle('done',k<n);b.disabled=k>n+1;
    const num=b.querySelector('.agent-tab-num');if(num)num.textContent=k<n?'✓':String(k);
  });
  if(n===3)renderAgentCreateReview();
  syncAgentModalChrome();
  syncTabIndicator($('#agentForm [data-wizard-steps="agent-create"]'));
}
function setAgentWizardStep(step){
  if(state.agentModalMode==='create')return setAgentCreateStep(step);
  setAgentTab(AGENT_TABS[Math.max(1,Math.min(4,Number(step)||1))-1]);
}
function renderAgentCreateReview(){
  const host=$('#agentCreateReview'),f=$('#agentForm');if(!host||!f)return;
  const preset=agentPresets.find(x=>x.id===state.agentCreatePreset);
  const cli=f.elements.executorId?.selectedOptions?.[0]?.textContent||f.elements.executorId?.value||'';
  const effort=(f.elements.effort?.value||'').trim();
  const rows=[['名称',f.elements.name.value.trim()],['标识',f.elements.id.value.trim()],['起点',preset?.name||'空白'],['CLI · 模型',`${cli} · ${agentEffectiveModel(f)||'未选模型'}${effort?` · ${effort}`:''}`],['职责',f.elements.description.value.trim()||'未填写职责']];
  host.innerHTML=rows.map(([k,v])=>`<div><dt>${k}</dt><dd>${escapeHTML(v)}</dd></div>`).join('');
}
function syncAgentModalChrome(){
  const create=state.agentModalMode==='create',step=state.agentCreateStep||1;
  const save=$('#agentSave'),next=$('#agentNext'),prev=$('#agentPrev');
  const foreign=agentWizardForeign(agentWizardSubject());
  if(save){save.hidden=create||foreign;save.textContent='保存'}
  const cloneBtn=$('#agentClone');
  if(cloneBtn)cloneBtn.hidden=catalogGeneric(state.workflow)||!foreign;
  if(next){next.hidden=!create;next.textContent=step===3?'创建':'下一步'}
  if(prev){prev.hidden=!create||step===1;prev.disabled=false}
  const hint=$('#agentStepHint');if(hint)hint.textContent=create?`第 ${step} 步，共 3 步`:'';
  renderAgentSummary();
}
function agentWizardNext(){
  if(state.agentModalMode!=='create')return;
  if(state.agentCreateStep<3)return setAgentCreateStep(state.agentCreateStep+1);
  if(!validateAgentCreateIdentity())return setAgentCreateStep(2);
  persistAgent($('#agentForm'));
}
function agentWizardPrev(){if(state.agentModalMode==='create')setAgentCreateStep(state.agentCreateStep-1)}
function showCreatedAgent(a){
  setAgentModalMode('edit');
  const presetHost=$('#agentPresetHost');if(presetHost)presetHost.dataset.ready='';
  selectAgent(a,false);
  resetAgentIdentityErrors();
  setAgentTab('task');
  syncAgentTabIndicators();
}
function saveAgent(e){
  e.preventDefault();
  if(state.agentModalMode==='create')return agentWizardNext();
  return persistAgent(e.currentTarget);
}
async function persistAgent(f){
  const creating=state.agentModalMode==='create';
  if(state.agentSaving)return null;
  state.agentSaving=true;
  try{return await persistAgentRequest(f,creating)}finally{state.agentSaving=false}
}
async function persistAgentRequest(f,creating){const existing=state.data.agents.find(a=>a.id===f.elements.id.value);if(!catalogGeneric(state.workflow)&&existing&&catalogForeign(existing.projectId))return toast('这是其他项目的共享角色，请先复制到本项目再改');const checked=name=>[...f.querySelectorAll(`input[name="${name}"]:checked`)].map(x=>x.value);const draft=readAgentDraft(f);const artifacts=draft.artifactOutputs.map((x,i)=>({name:x.name||`artifact-${i+1}`,path:x.path||'result.md',mediaType:x.path?.endsWith('.json')?'application/json':(x.mediaType||'text/markdown'),required:!!x.required})),verification=draft.verification.map((gate,i)=>({id:gate.id||`gate-${i+1}`,name:gate.name||`门禁 ${i+1}`,mode:'command',stage:'after_execution',commands:(gate.commands||[]).map(c=>String(c||'').trim()).filter(Boolean),required:true,onFailure:['loop','reject','fail','human'].includes(gate.onFailure)?gate.onFailure:'loop',executionScope:gate.executionScope==='repositories'?'repositories':'run'})).filter(g=>g.commands.length),hooks=(draft.hooks||[]).map((h,i)=>({id:String(h.id||`hook-${i+1}`).trim(),name:String(h.name||`Hook ${i+1}`).trim(),event:h.event||'PreToolUse',matcher:String(h.matcher||'').trim(),language:h.language==='python'?'python':'shell',script:String(h.script||'').trim(),timeoutSec:Number(h.timeoutSec)||10,executionScope:h.executionScope==='repositories'?'repositories':'run'})).filter(h=>h.script),interventions=[];if(f.elements.interventionAfter?.checked)interventions.push({id:'human-review',name:'人工结果复核',stage:'after_execution',required:true});if(f.elements.interventionPublish?.checked)interventions.push({id:'publish-approval',name:'产物发布确认',stage:'before_publish',required:true});const a={id:f.elements.id.value.trim(),name:f.elements.name.value.trim(),version:(existing?.version||0)+1,projectId:existing?(existing.projectId||state.projectFilter||''):(state.projectFilter||''),shared:!!f.elements.shared?.checked,description:f.elements.description.value,prompt:draft.prompt,executorId:f.elements.executorId?.value||'',model:agentEffectiveModel(f),effort:f.elements.effort?.value||'',rules:checked('rules'),skills:checked('skills'),mcpServers:checked('mcpServers'),platformMcp:!!f.elements.platformMcp?.checked,knowledgeBase:!!f.elements.knowledgeBase?.checked,hooks,loop:{enabled:!!f.elements.loopEnabled?.checked,maxIterations:Number(f.elements.loopMaxIterations?.value)||1,exitCondition:'all_gates_pass',onExhausted:f.elements.loopOnExhausted?.value||'fail',feedbackPrompt:f.elements.loopFeedbackPrompt?.value||''},interventions,artifactOutputs:artifacts,verification};if(!a.id||!a.name)return toast('标识和名称不能为空');if(catalogGeneric(state.workflow)){if(!isPlatformAdmin())return toast('只有平台管理员可以编辑通用模板角色');const bundled=workflowAgent(a.id);if(!bundled)return toast('该角色不属于当前通用模板');a.projectId='';a.shared=false;a.rules=bundled.rules||[];a.skills=bundled.skills||[];a.mcpServers=f.querySelectorAll('input[name="mcpServers"]').length?checked('mcpServers'):(bundled.mcpServers||[]);a.hooks=Array.isArray(bundled.hooks)?bundled.hooks:a.hooks;a.version=bundled.version||0;try{const saved=await api(`/api/workflows/${encodeURIComponent(state.workflow.id)}/agents`,{method:'PUT',body:JSON.stringify(a)});const list=state.genericTemplateAgents||[];const gi=list.findIndex(x=>x.id===saved.id);if(gi>=0)list[gi]=saved;else list.push(saved);state.genericTemplateAgents=list;state.selectedAgent=saved.id;state.agentDraft=null;$('#agentModal').classList.remove('open');renderCanvas();renderProperties();toast('通用模板角色已保存，所有项目下次运行生效');}catch(err){toast(err.message)}return;}try{await api('/api/agents',{method:'PUT',body:JSON.stringify(a)});const i=state.data.agents.findIndex(x=>x.id===a.id);if(i>=0)state.data.agents[i]=a;else state.data.agents.push(a);if(a.shared){(state.data.rules||[]).forEach(item=>{if(a.rules.includes(item.id)&&item.projectId===a.projectId)item.shared=true});(state.data.skills||[]).forEach(item=>{if(a.skills.includes(item.id)&&item.projectId===a.projectId)item.shared=true})}state.selectedAgent=a.id;state.agentDraft=null;if(creating){state.agentScope='project';renderAgents();renderCanvas();toast('Agent 新版本已保存');showCreatedAgent(a);return a}$('#agentModal').classList.remove('open');renderAgents();renderCanvas();toast('Agent 新版本已保存');return a}catch(err){toast(err.message)}return null}
async function deleteAgent(id){
  const a=(state.data.agents||[]).find(x=>x.id===id);
  if(!a)return toast('角色不存在或已删除');
  if(!await afConfirm({title:'删除角色确认',message:`确认删除角色「${a.name}」？此操作不可恢复。`,confirmText:'确认删除',isDanger:true}))return;
  try{
    await api(`/api/agents/${encodeURIComponent(id)}`,{method:'DELETE'});
    state.data.agents=state.data.agents.filter(x=>x.id!==id);
    if(state.selectedAgent===id)state.selectedAgent=null;
    if(state.agentDraft&&state.agentDraft.id===id)state.agentDraft=null;
    renderAgents();renderCanvas();
    toast('角色已删除')
  }catch(e){toast(e.message||'删除失败')}
}
async function cloneAgent(id){
  const src=id?(state.data.agents||[]).find(a=>a.id===id):state.data.agents.find(a=>a.id===state.selectedAgent);
  if(!src?.id)return toast('先选择一个角色');
  const name=await afPrompt({title:'复制角色',message:'请输入复制后的角色名称：',defaultValue:`${src.name} - 副本`});
  if(!name)return;
  try{
    const copied=await api(`/api/agents/${encodeURIComponent(src.id)}/clone`,{method:'POST',body:JSON.stringify({id:uid('agent'),name,projectId:state.projectFilter||''})});
    const [mcps,rules,skills]=await Promise.all([api('/api/mcp-servers',{silent:true}),api('/api/rules',{silent:true}),api('/api/skills',{silent:true})]);state.data.mcpServers=mcps;state.data.rules=rules;state.data.skills=skills;
    const i=state.data.agents.findIndex(a=>a.id===copied.id);
    if(i>=0)state.data.agents[i]=copied;else state.data.agents.unshift(copied);
    state.agentScope='project';
    renderAgents();
    openAgentWizard(copied);
    toast('已复制到本项目，可继续改');
  }catch(e){toast(e.message)}
}
