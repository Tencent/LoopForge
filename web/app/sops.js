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
function shareBadge(item,kind){
  const owned=catalogOwned(item.projectId);
  if(owned)return `<button type="button" class="scope-share share-action ${item.shared?'is-shared':''}" data-share-${kind}="${escapeHTML(item.id)}" aria-label="${item.shared?'取消共享':'共享给其他项目'}">${item.shared?'⊘ 取消共享':'↗ 共享给其他项目'}</button>`;
  const tag=!item.projectId?'<span class="scope-tag">平台</span>':`<span class="scope-tag">来自 ${escapeHTML((state.data.projects||[]).find(p=>p.id===item.projectId)?.name||item.projectId)}</span>`;
  return `${tag}<button type="button" class="scope-share" data-clone-${kind}="${escapeHTML(item.id)}">复制到本项目</button>`;
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
// Token to push and a 工蜂 MCP (+ 太湖 personal token) to create the merge
// request, so the catalog surfaces that dependency before adoption.
const GENERIC_REQUIREMENT_CHECKS={
  'gongfeng-git-token':{label:'工蜂 Git Token',hint:'推送工作分支到工蜂。',fix:'settings',fixLabel:'去个人设置配置',check:()=>!!(userById(state.currentUser?.id)||{}).gitTokenConfigured},
  'taihu-pat':{label:'太湖个人令牌',hint:'工蜂 MCP 走太湖统一认证。',fix:'settings',fixLabel:'去个人设置配置',check:()=>!!(userById(state.currentUser?.id)||{}).taihuPatConfigured},
  'gongfeng-mcp':{label:'工蜂 MCP',hint:'调用工蜂接口创建 MR；未安装时复制出的 SOP 需安装后为「交付工程师」角色绑定。',fix:'mcp',fixLabel:'去 MCP 市场安装',check:()=>gongfengMCPInstalled()},
  'tapd-mcp':{label:'TAPD MCP',hint:'把验收结论回写 TAPD；未安装时只产出待人工粘贴的说明。',fix:'mcp',fixLabel:'去 MCP 市场安装',check:()=>tapdMCPInstalled()}
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
  return `<div class="sop-requirements">${rows.map(r=>`<span class="req-chip ${r.ok?'ok':'missing'}" title="${escapeHTML(r.hint)}">${r.ok?'✓':'!'} ${escapeHTML(r.label)}</span>`).join('')}</div>`;
}
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
    rows.push({key:'run-key-'+kind,field,fix:'settings',fixLabel:'去个人设置配置',label:label+' API Key',ok:!!user[flag],hint:lfT('Agent 节点使用 '+label+'，请在个人设置里配置对应 API Key。','Agent nodes use '+label+'. Configure its API key in Settings.')});
  }
  if(usesRepo)rows.push({key:'run-repo',fix:'',label:'GitHub 仓库（已绑定）',ok:true,hint:'使用项目已绑定的仓库。访问私有仓库或推送代码时需要在个人设置中配置 GitHub Token；未绑定仓库时可在任务工作区直接运行。'});
  if(driver==='docker')rows.push({key:'run-docker',label:'Docker',ok:state.data?.capabilities?.docker!==false,hint:state.data?.capabilities?.docker===false?'平台连不上 Docker，这个 SOP 现在跑不了。重新运行 scripts/install.sh 会自动安装并挂载 Docker。':'在隔离的 Docker 沙箱里运行，首次运行会构建镜像，需要几分钟。'});
  return rows;
}
function sopRunRequirementChips(w){
  const rows=sopRunRequirements(w);
  if(!rows.length)return '<div class="sop-requirements"><span class="req-chip ok" title="只包含脚本和人工审批节点">✓ 无需 Key 或仓库</span></div>';
  return `<div class="sop-requirements" aria-label="运行条件">${rows.map(r=>`<span class="req-chip ${r.ok?'ok':'missing'}" title="${escapeHTML(r.hint)}">${r.ok?'✓':'!'} ${r.ok?'':'需要 '}${escapeHTML(r.label)}</span>`).join('')}</div>`;
}
function genericSOPCard(w){
  const nodes=w.nodes||[],agentCount=nodes.filter(n=>n.type==='agent').length,gates=nodes.filter(n=>n.type==='approval').length;
  const copy=(state.data?.workflows||[]).find(x=>!catalogGeneric(x)&&x.projectId===state.projectFilter&&(x.tags||[]).includes(`generic-source:${w.id}`));
  const copyBtn=copy
    ?`<button type="button" class="scope-share adopt-action" data-open-sop="${escapeHTML(copy.id)}">查看项目副本</button>`
    :`<button type="button" class="scope-share adopt-action" data-adopt-generic="${escapeHTML(w.id)}">复制到本项目</button>`;
  const editBtn=isPlatformAdmin()?`<button type="button" class="button ghost small" data-edit-generic="${escapeHTML(w.id)}">编辑模板</button>`:'';
  const headHint=isPlatformAdmin()?'管理员可直接改模板，所有项目下次运行即生效 · 复制后归项目所有':'直接跑=始终跟随平台最新版 · 复制=可自由修改且不再跟随更新';
  return `<article class="sop-card generic" data-adopt-generic="${escapeHTML(w.id)}"><div class="sop-card-head"><div><span class="health-dot-card never"></span><b>平台维护</b><small>${headHint}</small></div><span class="sop-status generic">通用</span></div><h3>${escapeHTML(w.name)}</h3><p>${escapeHTML(w.description||'平台提供的通用流程模板')}</p>${sopRunRequirementChips(w)}${genericRequirementChips(w)}<div class="sop-kpis"><span><b>${nodes.length}</b>节点</span><span><b>${agentCount}</b>角色节点</span><span><b>${gates}</b>人工门禁</span><span><b>v${w.version||1}</b>版本</span></div><div class="sop-card-foot"><span>${escapeHTML(w.owner||'平台')} · 与项目无关</span>${editBtn}<button type="button" class="button ghost small" data-run-generic="${escapeHTML(w.id)}">直接运行</button>${copyBtn}<time>${escapeHTML((w.tags||[]).join(' · ')||'通用模板')}</time></div></article>`;
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
    switchView('settings');
    const field=document.querySelector(`#userCredentialForm [name="${row?.field||(key==='run-key'?'codeBuddyApiKey':'gitToken')}"]`);
    field?.focus();field?.scrollIntoView({block:'center'});
    return;
  }
  const meta=GENERIC_REQUIREMENT_CHECKS[key];if(!meta)return;
  $('#genericTemplateModal').classList.remove('open');
  if(meta.fix==='mcp'){
    const query=key==='tapd-mcp'?'tapd':'gongfeng';
    state.mcpScope='project';state.mcpTab='market';state.mcpQuery=query;
    const input=$('#mcpMarketSearch');if(input)input.value=query;
    switchView('mcp');
    toast(query==='tapd'?'在 MCP 市场安装 TAPD MCP（太湖认证），安装后回到通用模板查看状态':'在 MCP 市场安装 Gongfeng MCP（太湖认证），安装后回到通用模板查看状态');
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
  $('#sopOverview').innerHTML=`<div class="overview-metric primary"><span>本项目运行</span><b>${totalRuns}</b><small>${workflows.length} 条${scopeLabel} SOP</small></div><div class="overview-metric success"><span>已闭环</span><b>${succeeded}</b><small>${totalRuns?percent(succeeded*100/totalRuns):'暂无运行'}</small></div><div class="overview-metric live"><span>进行中 / 待审批</span><b>${active}</b><small>实时更新 · 不含中断残留</small></div><div class="overview-metric danger"><span>失败运行</span><b>${failed}</b><small>${totalRuns?percent(failed*100/totalRuns):'0%'}</small></div><div class="overview-metric"><span>Token 消耗</span><b>${formatTokens(tokens)}</b><small>当前项目累计</small></div>`;
  const filter=$('#sopHealthFilter')?.value||'all',sort=$('#sopSort')?.value||'recent';
  if(filter!=='all')workflows=workflows.filter(w=>metricHealth(metrics.get(w.id), live.filter(r=>r.workflowId===w.id).length).key===filter);
  workflows.sort((a,b)=>{const x=metrics.get(a.id)||{},y=metrics.get(b.id)||{};if(sort==='runs')return (y.totalRuns||0)-(x.totalRuns||0);if(sort==='closure')return (y.closureRate||0)-(x.closureRate||0);if(sort==='failures')return (y.failedRuns||0)-(x.failedRuns||0);return new Date(y.lastRunAt||0)-new Date(x.lastRunAt||0)});
  $('#sopStats').innerHTML=`<span class="issue-stat">${workflows.length} SOPs</span><span class="issue-stat">${workflows.filter(w=>w.status==='published').length} 已发布</span><span class="issue-stat">${new Set(workflows.map(w=>w.owner).filter(Boolean)).size} 负责人</span>`;
  const empty=state.sopScope==='generic'?'内置 SOP 暂时不可用。它们由平台维护、与项目无关，所有项目都能复制使用。':state.sopScope==='shared'?'还没有共享给当前项目的 SOP。把本项目的 SOP 设为共享后，其他项目会在这里看到。':'当前项目还没有 SOP。可以新建，或从「团队共享」/「内置」复制一份。';
  const sopCatalog=$('#sopCatalog');
  sopCatalog.innerHTML=workflows.map(w=>{if(catalogGeneric(w))return genericSOPCard(w);const m=metrics.get(w.id)||{},liveCount=live.filter(r=>r.workflowId===w.id).length,health=metricHealth(m,liveCount),recent=formatTimestamp(m.lastRunAt);return `<article class="sop-card health-${health.key}" data-sop="${escapeHTML(w.id)}"><div class="sop-card-head"><div><span class="health-dot-card ${health.key}"></span><b>${health.label}</b><small>${m.totalRuns?`${percent(m.successRate)} 成功率` : health.hint}</small></div><span class="sop-status ${w.status||'draft'}">${w.status==='published'?'已发布':w.status==='archived'?'已归档':'草稿'}</span></div><h3>${escapeHTML(w.name)}</h3><p>${escapeHTML(w.description||'暂无流程说明')}</p>${sopRunRequirementChips(w)}<div class="sop-kpis"><span><b>${m.totalRuns||0}</b>运行</span><span><b>${m.succeededRuns||0}</b>成功</span><span><b>${m.failedRuns||0}</b>失败</span><span><b>${liveCount}</b>进行中</span></div><div class="sop-card-foot"><span>${escapeHTML(w.owner||'未分配负责人')} · v${w.version||1}</span>${shareBadge(w,'workflow')}${catalogOwned(w.projectId)?`<button type="button" class="scope-share danger" data-delete-workflow="${escapeHTML(w.id)}" title="删除此 SOP（不可恢复）">删除</button>`:''}<time>${recent?`最近 ${recent}`:'尚未运行'}</time></div></article>`}).join('')||`<div class="empty-properties"><b>${empty}</b></div>`;
  sopCatalog.querySelectorAll('[data-share-workflow]').forEach(btn=>btn.onclick=e=>{e.stopPropagation();toggleShare('workflow',btn.dataset.shareWorkflow)});
  sopCatalog.querySelectorAll('[data-clone-workflow]').forEach(btn=>btn.onclick=e=>{e.stopPropagation();cloneWorkflow(btn.dataset.cloneWorkflow)});
  sopCatalog.querySelectorAll('[data-adopt-generic]').forEach(btn=>btn.onclick=e=>{e.stopPropagation();openGenericTemplateModal(btn.dataset.adoptGeneric)});
  sopCatalog.querySelectorAll('[data-edit-generic]').forEach(btn=>btn.onclick=e=>{e.stopPropagation();openGenericTemplateEditor(btn.dataset.editGeneric)});
  sopCatalog.querySelectorAll('[data-run-generic]').forEach(btn=>btn.onclick=e=>{e.stopPropagation();openRunModal(btn.dataset.runGeneric)});
  sopCatalog.querySelectorAll('[data-open-sop]').forEach(btn=>btn.onclick=e=>{e.stopPropagation();const wf=(state.data.workflows||[]).find(x=>x.id===btn.dataset.openSop);if(!wf)return toast('项目副本不存在或已删除');state.workflow=clone(wf);state.selectedNode=null;renderWorkflowMeta();renderCanvas();renderProperties();switchView('flow')});
  sopCatalog.querySelectorAll('[data-sop]').forEach(card=>card.onclick=()=>{state.workflow=clone((state.data.workflows||[]).find(w=>w.id===card.dataset.sop));state.selectedNode=null;renderWorkflowMeta();renderCanvas();renderProperties();switchView('flow')})
  sopCatalog.querySelectorAll('[data-delete-workflow]').forEach(btn=>btn.onclick=e=>{e.stopPropagation();deleteWorkflow(btn.dataset.deleteWorkflow)})}
async function refreshWorkflowMetrics(){try{const opt={silent:true};const [metrics,runs]=await Promise.all([api('/api/workflow-metrics'+projectQuery(),opt),api('/api/runs',opt)]);state.data.workflowMetrics=metrics;state.data.runs=runs;renderSOPCatalog()}catch(e){toast(e.message)}}
