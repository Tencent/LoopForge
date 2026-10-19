function workflowPreviewContract(wf){
  return wf?.preview && typeof wf.preview==='object' ? wf.preview : null;
}
const PREVIEW_BUILD_NODE_ID='af-preview-build';
const PREVIEW_BUILD_AGENT_ID='af-preview-builder';
const PREVIEW_MODEL_FALLBACK=[
  {id:'default',name:'GLM-5.3-Flash'},
  {id:'glm-5.3-ioa',name:'GLM-5.3'},
  {id:'gpt-5.6-terra',name:'GPT-5.6-Terra'},
  {id:'gpt-5.6-luna',name:'GPT-5.6-Luna'},
  {id:'gpt-5.4',name:'GPT-5.4'},
  {id:'claude-sonnet-4.6',name:'Claude-Sonnet-4.6'},
  {id:'kimi-k2.7-ioa',name:'Kimi-K2.7-Code'},
  {id:'deepseek-v4.1-flash',name:'Deepseek-V4.1-Flash'}
];
function isPreviewBuildNode(id){return id===PREVIEW_BUILD_NODE_ID}
function workflowPreviewModel(wf){
  const api=window.AgentFlowAssistantModels;
  const fallback=(api&&api.DEFAULT)||'default';
  const model=(workflowPreviewContract(wf)||{}).model;
  return (model&&String(model).trim())||fallback;
}
function previewModelChoices(){
  const api=window.AgentFlowAssistantModels;
  const models=api?.catalog?.()?.models;
  if(models&&models.length)return models.map(m=>({id:m.id,name:m.name||m.id}));
  return PREVIEW_MODEL_FALLBACK.slice();
}
function fillPreviewModelSelect(sel, selected){
  if(!sel)return;
  const items=previewModelChoices();
  const keep=selected||items[0]?.id||'default';
  if(keep&&!items.some(m=>m.id===keep))items.unshift({id:keep,name:keep});
  sel.innerHTML=items.map(m=>`<option value="${escapeHTML(m.id)}" ${m.id===keep?'selected':''}>${escapeHTML(m.name||m.id)}</option>`).join('');
}
function ensurePreviewContract(){
  const wf=state.workflow;if(!wf)return null;
  const prev=workflowPreviewContract(wf)||{};
  const api=window.AgentFlowAssistantModels;
  const model=(prev.model&&String(prev.model).trim())||(api&&api.DEFAULT)||'default';
  wf.preview={
    ...prev,
    enabled:true,
    kind:'static',
    executorId:'codebuddy',
    model,
    sourcePath:prev.sourcePath||'dist',
    indexFile:prev.indexFile||'index.html',
    spaFallback:prev.spaFallback!==false,
    retentionSec:30*24*3600
  };
  return wf.preview;
}
function updatePreviewContract(patch){
  const wf=state.workflow;if(!wf)return;
  if(!wf.preview)ensurePreviewContract();
  Object.assign(wf.preview,patch);
}
function stripPreviewBuildNode(wf){
  if(!wf)return;
  wf.nodes=(wf.nodes||[]).filter(n=>!isPreviewBuildNode(n.id));
  wf.edges=(wf.edges||[]).filter(e=>!isPreviewBuildNode(e.source)&&!isPreviewBuildNode(e.target));
}
function previewBuildPosition(nodes){
  if(!nodes||!nodes.length)return {x:110,y:120};
  let maxX=nodes[0].position?.x||0,maxY=nodes[0].position?.y||0;
  nodes.forEach(n=>{if((n.position?.x||0)>maxX)maxX=n.position.x;if((n.position?.y||0)>maxY)maxY=n.position.y});
  return {x:maxX+280,y:maxY};
}
// 兼容旧数据：契约开启但节点缺失时，按旧规则补挂在成功路径末尾。
function syncPreviewBuildGraph(){
  const wf=state.workflow;if(!wf)return;
  if(!(wf.preview?.enabled))return;
  if((wf.nodes||[]).some(n=>isPreviewBuildNode(n.id)))return;
  const sinks=(wf.nodes||[]).filter(n=>!(wf.edges||[]).some(e=>e.source===n.id));
  wf.nodes=wf.nodes||[];
  wf.edges=wf.edges||[];
  wf.nodes.push({
    id:PREVIEW_BUILD_NODE_ID,
    name:'构建静态站点',
    type:'agent',
    agentId:PREVIEW_BUILD_AGENT_ID,
    executorId:'codebuddy',
    executionScope:'run',
    onFailure:'fail',
    retryStrategy:'targeted_fix',
    maxAttempts:2,
    maxReworks:1,
    position:previewBuildPosition(wf.nodes),
    artifactOutputs:[{name:'frontend-preview',path:'dist/index.html',required:true}]
  });
  sinks.forEach((n,i)=>wf.edges.push({id:'af-preview-build-edge-'+(i+1),source:n.id,target:PREVIEW_BUILD_NODE_ID,when:'success'}));
}
function renderWorkflowMeta(){
  const workflows=catalogWorkflows();
  const wf=state.workflow;
  // Generic templates and an unsaved AI draft are not in the project SOP list.
  // Keep the open workflow selected while it is being previewed.
  const options=wf&&!workflows.some(w=>w.id===wf.id)?[wf,...workflows]:workflows;
  $('#workflowSelect').innerHTML=options.map(w=>`<option value="${escapeHTML(w.id)}" ${wf&&w.id===wf.id?'selected':''}>${catalogGeneric(w)?'通用 ':(w.status==='published'?'●':'○')} ${escapeHTML(w.name)}</option>`).join('');
  if(!wf){
    $('#workflowName').value='';$('#workflowDescription').value='';$('#workflowOwner').value='';$('#workflowSandbox').innerHTML='';
    $('#workflowStatus').value='draft';$('#workflowTags').value='';$('#workflowMetricCategory').value='';$('#workflowSOPLane').value='';
    const meta=$('#workflowMeta');if(meta)meta.textContent='';
    const title=$('#flowCommandTitle');if(title)title.textContent='未选择流程';
    const pill=$('#flowSettingsPill');if(pill)pill.textContent='未选择流程';
    const chip=$('#sandboxChip');if(chip)chip.hidden=true;
    ['saveWorkflow','publishWorkflow','flowSettingsQuickSave'].forEach(id=>{const btn=$('#'+id);if(btn)btn.disabled=true});
    return;
  }
  normalizeWorkflowNotifications(wf);
 const notificationHost=$('#workflowNotificationEditor');if(notificationHost)mountNotificationEditor(notificationHost,wf.notifications,{sourceType:'run',kind:'sop',projectId:wf.projectId||state.projectFilter},()=>dirty());
 $('#workflowName').value=wf.name;$('#workflowDescription').value=wf.description||'';
  $('#workflowOwner').value=wf.owner||'';$('#workflowStatus').value=wf.status||'draft';$('#workflowTags').value=(wf.tags||[]).join(', ');$('#workflowMetricCategory').value=wf.metricCategory||'';$('#workflowSOPLane').value=wf.sopLane||'';
  const previewOn=!!wf.preview?.enabled, hasPreviewNode=(wf.nodes||[]).some(n=>isPreviewBuildNode(n.id));
  if(previewOn!==hasPreviewNode)syncPreviewBuildGraph();
  $('#workflowSandbox').innerHTML=(state.data.sandboxes||[]).filter(s=>catalogVisible(s.projectId,s.shared)).map(s=>`<option value="${escapeHTML(s.id)}" ${s.id===wf.sandboxProfileId?'selected':''}>${escapeHTML(s.name)}${catalogForeign(s.projectId)?' · 共享':''}</option>`).join('');
  $('#workflowMeta').textContent=catalogGeneric(wf)?(isPlatformAdmin()?`通用模板 · 平台管理员可编辑 · ${wf.nodes.length} 步 · 保存后所有项目下次运行生效`:`通用模板 · 只读 · ${wf.nodes.length} 步 · 复制到本项目后可编辑`): `${wf.status||'draft'} · v${wf.version} · ${wf.nodes.length} 步`;
  const pill=$('#flowSettingsPill');if(pill)pill.textContent=catalogGeneric(wf)?(isPlatformAdmin()?'通用模板 · 可编辑':'通用模板'):`${wf.status==='published'?'已发布':'草稿'} · v${wf.version} · ${wf.nodes.length} 步`;
  $('#flowCommandTitle').textContent=wf.name||'未命名流程';
  const chip=$('#sandboxChip');
  if(chip){
    const sb=(state.data.sandboxes||[]).find(s=>s.id===wf.sandboxProfileId);
    chip.hidden=false;
    chip.textContent=`沙箱 · ${sb?.name||wf.sandboxProfileId||'未设置'}`;
  }
  const foreign=catalogForeign(wf.projectId);
  ['saveWorkflow','publishWorkflow','flowSettingsQuickSave'].forEach(id=>{const btn=$('#'+id);if(btn)btn.disabled=foreign||(catalogGeneric(wf)&&!isPlatformAdmin())});
}

function sopWorkflowChoices(exceptId){
  return catalogWorkflows().filter(w=>w.id&&w.id!==exceptId);
}
function sopDeclaredArtifacts(wf){
  const seen=new Set(),out=[];
  (wf?.nodes||[]).forEach(n=>(n.artifactOutputs||[]).forEach(a=>{
    const name=String(a.name||'').trim();
    if(!name||seen.has(name))return;
    seen.add(name);
    out.push({name,path:a.path||(name+'.md'),mediaType:a.mediaType||'text/markdown',required:!!a.required});
  }));
  return out;
}
function addNode(type){
  if(!state.workflow)return toast('流程尚未加载');
  if(type==='preview'){
    const existing=state.workflow.nodes.find(n=>isPreviewBuildNode(n.id));
    if(existing){state.selectedNode=existing.id;renderCanvas();renderProperties();return toast('预览节点已存在，可拖拽连线调整它的位置')}
    ensurePreviewContract();
    const count=state.workflow.nodes.length;
    state.workflow.nodes.push({id:PREVIEW_BUILD_NODE_ID,name:'构建静态站点',type:'agent',agentId:PREVIEW_BUILD_AGENT_ID,executorId:'codebuddy',executionScope:'run',onFailure:'fail',retryStrategy:'targeted_fix',maxAttempts:2,maxReworks:1,position:{x:110+(count%3)*280,y:120+Math.floor(count/3)*190},artifactOutputs:[{name:'frontend-preview',path:'dist/index.html',required:true}]});
    state.selectedNode=PREVIEW_BUILD_NODE_ID;
    dirty();renderCanvas();renderProperties();
    return;
  }
  const count=state.workflow.nodes.length;
  const labels={agent:'新 Agent 节点',script:'脚本节点',approval:'人工门禁',sop:'SOP 节点'};
  const firstSOP=type==='sop'?sopWorkflowChoices(state.workflow.id)[0]:null;
  const node={id:uid(type),name:firstSOP?.name||labels[type]||'新节点',type,agentId:type==='agent'?(workflowAgents()[0]?.id||''):'',sopWorkflowId:firstSOP?.id||'',scriptLanguage:type==='script'?'shell':'',script:type==='script'?"printf 'passed\\n'":'',executionScope:'run',artifactOutputs:firstSOP?sopDeclaredArtifacts(firstSOP):[],rejectTo:'',onFailure:'fail',retryStrategy:'targeted_fix',carryOver:['failed_checks','last_error','upstream_artifacts'],maxAttempts:1,maxReworks:3,position:{x:110+(count%3)*280,y:120+Math.floor(count/3)*190}};
  state.workflow.nodes.push(node);state.selectedNode=node.id;
  dirty();renderCanvas();renderProperties();
}
function deleteSelected(){
  if(!state.selectedNode||!state.workflow)return;
  const removingPreview=isPreviewBuildNode(state.selectedNode);
  state.workflow.nodes=state.workflow.nodes.filter(n=>n.id!==state.selectedNode);
  state.workflow.edges=state.workflow.edges.filter(e=>e.source!==state.selectedNode&&e.target!==state.selectedNode);
  state.selectedNode=null;
  if(removingPreview&&state.workflow.preview)delete state.workflow.preview;
  dirty();renderCanvas();renderProperties();
}
function arrange(){workflowStages(state.workflow).forEach((stage,column)=>stage.forEach((node,row)=>{node.position={x:80+column*280,y:80+row*140}}));dirty();renderCanvas();toast('已按依赖层级整理画布')}

function nodeMeta(n){
  if(isPreviewBuildNode(n.id)){
    return {icon:'预',kind:'preview',subtitle:'CodeBuddy 静态站点构建',detail:workflowPreviewModel(state.workflow)};
  }
  if(n.type==='agent'){
    const a=workflowAgent(n.agentId);
    const generic=catalogGeneric(state.workflow);
    const e=state.data.executors.find(x=>x.id===(a?.executorId||n.executorId));
    const subtitle=a?.name||(n.agentId?(generic?n.agentId:'角色不可用'):'未绑定角色');
    const detail=a?(a.model||e?.model||e?.name||(generic?'通用模板角色':'模型在角色定义里选')):(n.agentId?(generic?'通用模板角色':'不属于当前项目且未共享'):'模型在角色定义里选');
    return {icon:'AI',kind:'agent',subtitle,detail};
  }
  if(n.type==='script')return {icon:'⌘',kind:'script',subtitle:n.scriptLanguage==='python'?'Python Script':'Shell Script',detail:n.script||'未配置脚本'};
  if(n.type==='sop'){
    const wf=sopWorkflowChoices().find(w=>w.id===n.sopWorkflowId)||(state.data.workflows||[]).find(w=>w.id===n.sopWorkflowId);
    const arts=(n.artifactOutputs||[]).map(a=>a.name).filter(Boolean);
    return {icon:'SOP',kind:'sop',subtitle:wf?.name||(n.sopWorkflowId?'流程不可用':'未选择流程'),detail:arts.length?`交给下一步：${arts.join('、')}`:(wf?.description||'跑完这条 SOP，产物交给下一步')};
  }
  return {icon:'✓',kind:'approval',subtitle:'Human in the loop',detail:'等待人工判断'};
}
function renderCanvas(){
  const run=state.activeRun; const nodes=$('#nodes');if(!nodes)return;nodes.innerHTML='';
  if(!state.workflow){renderFlowOutline();return}
  state.workflow.nodes.forEach(n=>{
    const m=nodeMeta(n), nr=run?.nodes?.[n.id]; const el=document.createElement('div');el.className=`flow-node ${isPreviewBuildNode(n.id)?'preview-build':''} ${state.selectedNode===n.id?'selected':''} ${state.connectFrom===n.id?'connecting':''} ${nr?.status||''}`;el.style.left=`${n.position.x}px`;el.style.top=`${n.position.y}px`;el.dataset.id=n.id;
    const rejectName=state.workflow.nodes.find(x=>x.id===n.rejectTo)?.name;
    el.innerHTML=`<button type="button" class="node-port in" title="连到这里"></button><div class="node-top"><span class="node-icon ${m.kind}">${m.icon}</span><div><div class="node-title">${escapeHTML(n.name)}</div><div class="node-subtitle">${escapeHTML(m.subtitle)}</div></div></div><div class="node-meta" title="${escapeHTML(m.detail)}">${escapeHTML(m.detail)}</div>${rejectName?`<div class="node-reject">失败打回 ${escapeHTML(rejectName)}</div>`:''}<button type="button" class="node-port out connector" title="拖到下一步，或点一下再点目标"></button>`;
    el.onclick=e=>{
      if(e.target.classList.contains('node-port'))return;
      if(state.connectFrom&&state.connectFrom!==n.id){connect(state.connectFrom,n.id);state.connectFrom=null;state.connectCursor=null;renderCanvas();renderProperties();return}
      state.selectedNode=n.id;renderCanvas();renderProperties();
    };
    bindNodePort(el,n);makeDraggable(el,n);nodes.appendChild(el);
  });
  requestAnimationFrame(renderEdges);renderFlowOutline();
}

function renderFlowOutline(){
  const host=$('#flowOutline');if(!host||!state.workflow)return;
  const run=state.activeRun,stages=workflowStages(state.workflow),ordered=stages.flatMap((stage,stageIndex)=>stage.map(node=>({node,stageIndex,parallel:stage.length>1})));
  host.innerHTML=ordered.map(({node,stageIndex,parallel},index)=>{const meta=nodeMeta(node),status=run?.nodes?.[node.id]?.status||'',kind=isPreviewBuildNode(node.id)?'预览构建':(node.type==='agent'?'Agent':node.type==='script'?'脚本':node.type==='sop'?'SOP':'人工');return `<div class="flow-step ${state.selectedNode===node.id?'active':''}" data-flow-node="${escapeHTML(node.id)}"><span class="flow-step-index">${index+1}</span><div><b>${escapeHTML(node.name)}</b><small>第 ${stageIndex+1} 阶段${parallel?' · 并行':' · '+kind}</small></div><span class="flow-step-state">${statusText(status)||meta.icon}</span></div>`}).join('')||'<div class="catalog-empty" style="padding:30px 8px"><b>还没有步骤</b><p>从上方选择一种节点开始</p></div>';
  host.querySelectorAll('[data-flow-node]').forEach(el=>el.onclick=()=>{state.selectedNode=el.dataset.flowNode;renderCanvas();renderProperties()});
}

function flowDiagnostics(){
  if(!state.workflow)return [];
  const nodes=state.workflow.nodes||[],edges=state.workflow.edges||[],incoming=new Map(nodes.map(n=>[n.id,0])),items=[];edges.forEach(e=>incoming.has(e.target)&&incoming.set(e.target,incoming.get(e.target)+1));
  const starts=nodes.filter(n=>incoming.get(n.id)===0);items.push({level:!nodes.length?'error':starts.length===1?'ok':'warn',title:'流程入口',detail:!nodes.length?'至少添加一个节点':starts.length===1?`从「${starts[0].name}」开始`:`检测到 ${starts.length} 个入口；这些节点会并行启动`});
  items.push({level:state.data.sandboxes?.some(s=>s.id===state.workflow.sandboxProfileId)?'ok':'error',title:'运行沙箱',detail:state.workflow.sandboxProfileId?`整条流程使用 ${state.workflow.sandboxProfileId}`:'尚未选择 Workflow 运行沙箱'});
  nodes.forEach(n=>{if(isPreviewBuildNode(n.id)){const inDeg=edges.filter(e=>e.target===n.id).length;items.push({level:inDeg?'ok':'warn',title:n.name,detail:inDeg?`插在流程中间：CodeBuddy ${workflowPreviewModel(state.workflow)} 构建静态站点，运行成功后归档预览`:'还没有前序依赖，会作为流程入口直接运行；请从上游节点连线到此'});return}if(n.type==='agent'){const missing=[];const agent=workflowAgent(n.agentId);const generic=catalogGeneric(state.workflow);if(!n.agentId)missing.push('角色');else if(!agent)missing.push(generic?'通用模板角色未加载':'角色不属于当前项目或未共享');else if(!generic&&!(agent.executorId||n.executorId))missing.push('角色未选模型');if(!(n.artifactOutputs||[]).length&&!(agent?.artifactOutputs||[]).length)missing.push('角色产物契约');items.push({level:missing.length?'warn':'ok',title:n.name,detail:missing.length?`尚未配置：${missing.join('、')}`:generic?'已绑定通用模板角色；任务书与门禁在角色定义中':'已绑定角色；任务书与门禁在角色定义中'})}if(n.type==='sop'){const missing=[];const wf=sopWorkflowChoices(state.workflow.id).find(w=>w.id===n.sopWorkflowId)||(state.data.workflows||[]).find(w=>w.id===n.sopWorkflowId);if(!n.sopWorkflowId)missing.push('要跑的 SOP');else if(!wf)missing.push('引用的 SOP 不存在');if(n.sopWorkflowId===state.workflow.id)missing.push('不能引用自己');items.push({level:missing.length?'error':'ok',title:n.name,detail:missing.length?`尚未配置：${missing.join('、')}`:`引用「${wf.name}」，产物交给下一步`})}if(n.rejectTo&&!nodes.some(x=>x.id===n.rejectTo))items.push({level:'error',title:n.name,detail:'失败打回目标不存在'})});
  if(nodes.length>1&&edges.length===0)items.push({level:'warn',title:'成功路径',detail:'多个节点之间没有依赖关系，将同时启动'});if(!items.some(x=>x.level==='error'))items.push({level:'ok',title:'结构校验',detail:'未发现阻止保存或试运行的结构错误'});return items;
}
function setFlowView(mode){$$('.canvas-view-toggle button').forEach(b=>b.classList.toggle('active',b.dataset.flowView===mode));$('#canvas').hidden=mode!=='canvas';$('#flowCheckPanel').hidden=mode!=='check';if(mode==='check')$('#flowCheckPanel').innerHTML=`<div class="page-heading" style="margin-bottom:14px"><div><h1 style="font-size:17px">配置检查</h1><p>运行前检查入口、CLI、产物契约和返工路径。</p></div></div><div class="flow-check-grid">${flowDiagnostics().map(x=>`<div class="flow-check-item ${x.level}"><b>${escapeHTML(x.title)}</b><p>${escapeHTML(x.detail)}</p></div>`).join('')}</div>`}
function renderThemeToggle(){const light=document.documentElement.dataset.theme==='light',button=$('#themeToggle');if(!button)return;button.querySelector('span').textContent=light?'☾':'☀';button.querySelector('b').textContent=light?'深色':'亮色';button.title=light?'切换到深色主题':'切换到亮色主题'}
function toggleTheme(){const theme=document.documentElement.dataset.theme==='light'?'dark':'light';document.documentElement.dataset.theme=theme;localStorage.setItem('loopforge.theme.v3',theme);renderThemeToggle();toast(theme==='light'?'已切换亮色主题':'已切换深色主题')}
function statusText(s){return ({pending:'待执行',running:'运行中',succeeded:'已完成',failed:'失败',waiting_approval:'待审批',cancelled:'已取消'}[s]||'')}
function canvasPoint(e){
  const canvas=$('#canvas');if(!canvas)return {x:0,y:0};
  const rect=canvas.getBoundingClientRect();
  return {x:e.clientX-rect.left+canvas.scrollLeft,y:e.clientY-rect.top+canvas.scrollTop};
}
function bindNodePort(el,node){
  const out=el.querySelector('.node-port.out');
  const inn=el.querySelector('.node-port.in');
  if(inn)inn.onclick=e=>{
    e.stopPropagation();
    if(state.connectFrom&&state.connectFrom!==node.id){connect(state.connectFrom,node.id);state.connectFrom=null;state.connectCursor=null;renderCanvas();renderProperties()}
  };
  if(!out)return;
  out.onpointerdown=e=>{
    e.stopPropagation();e.preventDefault();
    state.connectFrom=node.id;state.selectedNode=node.id;state.connectCursor=canvasPoint(e);
    out.setPointerCapture(e.pointerId);
    el.classList.add('connecting');
    renderEdges();
  };
  out.onpointermove=e=>{
    if(state.connectFrom!==node.id)return;
    state.connectCursor=canvasPoint(e);
    renderEdges();
  };
  out.onpointerup=e=>{
    const moved=state.connectCursor;
    const start=node.position;
    const dragged=moved&&(Math.abs(moved.x-(start.x+240))>8||Math.abs(moved.y-(start.y+48))>8);
    const hit=document.elementFromPoint(e.clientX,e.clientY)?.closest('.flow-node');
    if(hit&&hit.dataset.id&&hit.dataset.id!==node.id){
      connect(node.id,hit.dataset.id);state.connectFrom=null;state.connectCursor=null;
    }else if(!dragged){
      state.connectCursor=null;
    }else{
      state.connectFrom=null;state.connectCursor=null;
    }
    renderCanvas();renderProperties();
  };
}
function makeDraggable(el,node){let start=null;el.onpointerdown=e=>{if(e.target.classList.contains('node-port')||e.target.classList.contains('connector')||['INPUT','TEXTAREA','SELECT','BUTTON'].includes(e.target.tagName))return;start={x:e.clientX,y:e.clientY,nx:node.position.x,ny:node.position.y};el.setPointerCapture(e.pointerId)};el.onpointermove=e=>{if(!start)return;node.position.x=Math.max(20,start.nx+e.clientX-start.x);node.position.y=Math.max(20,start.ny+e.clientY-start.y);el.style.left=node.position.x+'px';el.style.top=node.position.y+'px';renderEdges()};el.onpointerup=()=>{if(start){start=null;dirty()}}}
function connect(source,target){if(source===target)return;if(state.workflow.edges.some(e=>e.source===source&&e.target===target)){toast('依赖已经存在');return}const reachable=new Set([target]),queue=[target];while(queue.length){const current=queue.shift();for(const edge of state.workflow.edges.filter(e=>e.source===current)){if(edge.target===source){toast('不能形成循环依赖；返工请使用"失败打回到"');return}if(!reachable.has(edge.target)){reachable.add(edge.target);queue.push(edge.target)}}}state.workflow.edges.push({id:uid('edge'),source,target,when:'success'});dirty();renderCanvas();renderProperties()}
function nodeBox(n){
  const el=document.querySelector(`.flow-node[data-id="${CSS.escape?CSS.escape(n.id):n.id}"]`);
  const w=el?.offsetWidth||228,h=el?.offsetHeight||96;
  return {x:n.position.x,y:n.position.y,w,h,cx:n.position.x+w/2,cy:n.position.y+h/2,right:n.position.x+w,left:n.position.x,top:n.position.y,bottom:n.position.y+h};
}
async function deleteEdgeById(id){
  if(!id)return;
  const edge=state.workflow.edges.find(x=>x.id===id);
  if(!edge)return;
  const a=state.workflow.nodes.find(n=>n.id===edge.source);
  const b=state.workflow.nodes.find(n=>n.id===edge.target);
  const desc=(a&&b)?`确定删除从「${a.name}」到「${b.name}」的依赖连线？`:'确定删除这条连线？';
  if(!await afConfirm({
    title:'删除连线',
    message:desc,
    confirmText:'确认删除',
    danger:true
  }))return;
  state.workflow.edges=state.workflow.edges.filter(x=>x.id!==id);
  dirty();renderCanvas();renderProperties();toast('已删除连线');
}

async function deleteRejectEdgeByNodeId(nodeId){
  if(!nodeId)return;
  const node=state.workflow.nodes.find(n=>n.id===nodeId);
  if(!node||!node.rejectTo)return;
  const target=state.workflow.nodes.find(n=>n.id===node.rejectTo);
  const desc=target?`确定取消「${node.name}」失败打回到「${target.name}」的返工连线？`:`确定取消「${node.name}」的失败打回关系？`;
  if(!await afConfirm({
    title:'取消失败打回',
    message:desc,
    confirmText:'确认取消',
    danger:true
  }))return;
  node.rejectTo='';
  node.onFailure='fail';
  dirty();renderCanvas();renderProperties();toast('已取消失败打回');
}

function renderEdges(){
  const svg=$('#edges');if(!svg||!state.workflow)return;
  const existingDefs=svg.querySelector('defs');
  const defs=existingDefs?existingDefs.outerHTML:`<defs>
    <marker id="arrow" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="#98a2b3"></path></marker>
    <marker id="arrowHover" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="#ef4444"></path></marker>
    <marker id="rejectArrow" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="#e26d6d"></path></marker>
    <marker id="rejectArrowHover" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="#dc2626"></path></marker>
  </defs>`;
  let html=defs;
  const size=state.workflow.nodes.reduce((acc,n)=>({w:Math.max(acc.w,n.position.x+320),h:Math.max(acc.h,n.position.y+220)}),{w:1200,h:800});
  svg.setAttribute('width',size.w);svg.setAttribute('height',size.h);svg.style.width=size.w+'px';svg.style.height=size.h+'px';
  state.workflow.edges.forEach(e=>{
    const a=state.workflow.nodes.find(n=>n.id===e.source),b=state.workflow.nodes.find(n=>n.id===e.target);if(!a||!b)return;
    const A=nodeBox(a),B=nodeBox(b);
    const x1=A.right,y1=A.cy,x2=B.left,y2=B.cy,dx=Math.max(56,Math.abs(x2-x1)*0.4);
    const mx=Math.round((x1+x2)/2), my=Math.round((y1+y2)/2);
    const d=`M ${x1} ${y1} C ${x1+dx} ${y1}, ${x2-dx} ${y2}, ${x2} ${y2}`;
    html+=`<g class="edge-group" data-edge="${escapeHTML(e.id)}">
      <path class="edge-hitbox" d="${d}"/>
      <path class="edge" d="${d}"/>
      <g class="edge-delete-btn" transform="translate(${mx}, ${my})" data-edge-del="${escapeHTML(e.id)}">
        <circle cx="0" cy="0" r="11"/>
        <path d="M -4 -4 L 4 4 M 4 -4 L -4 4"/>
      </g>
      <title>点击连线或 × 删除依赖（${escapeHTML(a.name)} → ${escapeHTML(b.name)}）</title>
    </g>`;
  });
  state.workflow.nodes.filter(n=>n.rejectTo).forEach(n=>{
    const target=state.workflow.nodes.find(x=>x.id===n.rejectTo);if(!target)return;
    const A=nodeBox(n),B=nodeBox(target);
    const x1=A.cx,y1=A.bottom,x2=B.cx,y2=B.bottom,dip=Math.max(48,Math.abs(x2-x1)*0.18)+36;
    const mx=Math.round((x1+x2)/2), my=Math.round((y1+y2)/2 + dip*0.75);
    const d=`M ${x1} ${y1} C ${x1} ${y1+dip}, ${x2} ${y2+dip}, ${x2} ${y2}`;
    html+=`<g class="edge-group reject-group" data-reject-from="${escapeHTML(n.id)}">
      <path class="edge-hitbox reject-hitbox" d="${d}"/>
      <path class="edge reject-edge" d="${d}"/>
      <g class="edge-delete-btn reject-del-btn" transform="translate(${mx}, ${my})" data-reject-del="${escapeHTML(n.id)}">
        <circle cx="0" cy="0" r="11"/>
        <path d="M -4 -4 L 4 4 M 4 -4 L -4 4"/>
      </g>
      <title>点击连线或 × 取消失败打回（${escapeHTML(n.name)} ⇠ ${escapeHTML(target.name)}）</title>
    </g>`;
  });
  if(state.connectFrom&&state.connectCursor){
    const src=state.workflow.nodes.find(n=>n.id===state.connectFrom);
    if(src){
      const A=nodeBox(src),x1=A.right,y1=A.cy,x2=state.connectCursor.x,y2=state.connectCursor.y,dx=Math.max(40,Math.abs(x2-x1)*0.35);
      html+=`<path class="edge preview-edge" d="M ${x1} ${y1} C ${x1+dx} ${y1}, ${x2-dx} ${y2}, ${x2} ${y2}"/>`;
    }
  }
  svg.innerHTML=html;
  svg.querySelectorAll('.edge-group[data-edge]').forEach(grp=>{
    grp.onpointerenter=()=>{grp.classList.add('hovered')};
    grp.onpointerleave=()=>{grp.classList.remove('hovered')};
    grp.onclick=async ev=>{
      ev.stopPropagation();
      if(state.connectFrom)return;
      const id=grp.dataset.edge;
      await deleteEdgeById(id);
    };
    const delBtn=grp.querySelector('.edge-delete-btn');
    if(delBtn){
      delBtn.onclick=async ev=>{
        ev.stopPropagation();
        if(state.connectFrom)return;
        await deleteEdgeById(grp.dataset.edge);
      };
    }
  });
  svg.querySelectorAll('.edge-group[data-reject-from]').forEach(grp=>{
    grp.onpointerenter=()=>{grp.classList.add('hovered')};
    grp.onpointerleave=()=>{grp.classList.remove('hovered')};
    grp.onclick=async ev=>{
      ev.stopPropagation();
      if(state.connectFrom)return;
      const nodeId=grp.dataset.rejectFrom;
      await deleteRejectEdgeByNodeId(nodeId);
    };
    const delBtn=grp.querySelector('.reject-del-btn');
    if(delBtn){
      delBtn.onclick=async ev=>{
        ev.stopPropagation();
        if(state.connectFrom)return;
        await deleteRejectEdgeByNodeId(grp.dataset.rejectFrom);
      };
    }
  });
}

function ensureNodeDefaults(n){
  n.onFailure=n.onFailure||(n.rejectTo?'reject':'fail');
  n.retryStrategy=n.retryStrategy||'targeted_fix';
  n.carryOver=n.carryOver||['failed_checks','last_error','upstream_artifacts'];
}
function artifactRowsHTML(n){
  return (n.artifactOutputs||[]).map((a,i)=>`<article class="artifact-card" data-artifact-row="${i}">
      <header><input data-ak="name" value="${escapeHTML(a.name||'')}" placeholder="产物名称，如 checkers-report"><button type="button" data-remove-artifact="${i}" title="删除产物">×</button></header>
      <label>写入路径</label>
      <input data-ak="path" value="${escapeHTML(a.path||'')}" placeholder="相对路径，如 checkers-report.md">
      <footer><select data-ak="mediaType"><option value="application/json" ${a.mediaType==='application/json'?'selected':''}>JSON</option><option value="text/markdown" ${a.mediaType!=='application/json'?'selected':''}>Markdown</option></select>
        <label class="toggle-mini"><input type="checkbox" data-ak="required" ${a.required?'checked':''}> 必需</label></footer>
    </article>`).join('')||'<p class="empty-inline">还没有产物。点下面添加一项即可。</p>';
}
function bindArtifactRows(host,n){
  const box=host.querySelector('#artifactContractRows');if(!box)return;
  box.querySelectorAll('[data-artifact-row]').forEach(row=>row.querySelectorAll('[data-ak]').forEach(el=>el.oninput=()=>{const value=el.dataset.ak==='required'?el.checked:el.value;n.artifactOutputs[Number(row.dataset.artifactRow)][el.dataset.ak]=value;dirty()}));
  box.querySelectorAll('[data-remove-artifact]').forEach(el=>el.onclick=()=>{n.artifactOutputs.splice(Number(el.dataset.removeArtifact),1);box.innerHTML=artifactRowsHTML(n);bindArtifactRows(host,n);dirty()});
}
function listEditorHTML(items, placeholder){
  const rows=(items&&items.length?items:['']).map((v,i)=>`<div class="list-edit-row" data-i="${i}"><textarea rows="2" placeholder="${escapeHTML(placeholder)}">${escapeHTML(v||'')}</textarea><button type="button" data-list-del="${i}" title="删除">×</button></div>`).join('');
  return `<div class="list-edit">${rows}</div>`;
}
function bindListEditor(host, selector, getItems, setItems, placeholder){
  const root=host.querySelector(selector);if(!root)return;
  const box=root.querySelector('.list-edit');if(!box)return;
  const read=()=>[...box.querySelectorAll('textarea')].map(el=>el.value);
  const persist=()=>{setItems(read().map(v=>v.trim()).filter(Boolean));dirty()};
  const remount=items=>{root.querySelector('.list-edit').outerHTML=listEditorHTML(items,placeholder);bindListEditor(host,selector,getItems,setItems,placeholder)};
  box.querySelectorAll('textarea').forEach(el=>el.oninput=persist);
  box.querySelectorAll('[data-list-del]').forEach(btn=>btn.onclick=()=>{const next=read();next.splice(Number(btn.dataset.listDel),1);setItems(next.map(v=>v.trim()).filter(Boolean));dirty();remount(next.length?next:[''])});
  const add=root.querySelector('[data-list-add]');
  if(add)add.onclick=()=>remount(read().concat(['']));
}
function ensureAgentDraft(a){
  if(!state.agentDraft||state.agentDraft.id!==a.id)state.agentDraft=JSON.parse(JSON.stringify(a||{}));
  state.agentDraft.taskPrompt=state.agentDraft.taskPrompt||{};
  state.agentDraft.artifactOutputs=state.agentDraft.artifactOutputs||[];
  state.agentDraft.verification=(state.agentDraft.verification||[]).filter(x=>x.mode!=='manual'&&x.mode!=='submit');
  state.agentDraft.loop=state.agentDraft.loop||{enabled:false,maxIterations:3,onExhausted:'fail'};
  state.agentDraft.interventions=state.agentDraft.interventions||[];
  state.agentDraft.hooks=state.agentDraft.hooks||[];
  return state.agentDraft;
}
function agentListEditorHTML(items,placeholder){
  const rows=(items&&items.length?items:['']).map((v,i)=>`<div class="as-list-row" data-i="${i}"><span class="as-list-idx">${i+1}</span><textarea rows="2" placeholder="${escapeHTML(placeholder)}">${escapeHTML(v||'')}</textarea><button type="button" data-list-del="${i}" title="删除">×</button></div>`).join('');
  return `<div class="as-list">${rows}</div>`;
}
function bindAgentListEditor(host, selector, getItems, setItems, placeholder, onChange){
  const root=host.querySelector(selector);if(!root)return;
  const box=root.querySelector('.as-list');if(!box)return;
  const read=()=>[...box.querySelectorAll('textarea')].map(el=>el.value);
  const persist=()=>{setItems(read().map(v=>v.trim()).filter(Boolean));onChange?.();dirty()};
  const remount=items=>{root.querySelector('.as-list').outerHTML=agentListEditorHTML(items,placeholder);bindAgentListEditor(host,selector,getItems,setItems,placeholder,onChange)};
  box.querySelectorAll('textarea').forEach(el=>el.oninput=persist);
  box.querySelectorAll('[data-list-del]').forEach(btn=>btn.onclick=()=>{const next=read();next.splice(Number(btn.dataset.listDel),1);setItems(next.map(v=>v.trim()).filter(Boolean));dirty();remount(next.length?next:['']);onChange?.()});
  const add=root.querySelector('[data-list-add]');
  if(add)add.onclick=()=>remount(read().concat(['']));
}
function renderAgentPresets(){
  const host=$('#agentPresetHost');if(!host)return;
  const isNew=!($('#agentForm')?.elements?.id?.value||'').trim();
  if(!isNew){host.innerHTML='';host.dataset.ready='1';return}
  if(host.dataset.ready)return;
  host.dataset.ready='1';
  host.innerHTML=`<p class="as-lead-line">可先点一个常见角色，名称和职责会自动填好，再按你们项目改。</p><div class="as-presets">${agentPresets.map(p=>`<button type="button" class="as-preset" data-agent-preset="${p.id}"><span class="as-preset-icon">${p.id==='developer'?'码':p.id==='reviewer'?'审':p.id==='tester'?'测':'交'}</span><b>${p.name}</b><small>${p.desc}</small></button>`).join('')}</div>`;
  host.querySelectorAll('[data-agent-preset]').forEach(button=>button.onclick=()=>applyAgentPreset(button.dataset.agentPreset));
}
function insertAgentPlaceholder(host, token){
  const el=host._tpFocus||host.querySelector('[data-tp="objective"]')||host.querySelector('[data-tp]');
  if(!el||el.tagName!=='TEXTAREA'&&el.tagName!=='INPUT')return;
  const start=el.selectionStart??el.value.length, end=el.selectionEnd??start;
  el.value=el.value.slice(0,start)+token+el.value.slice(end);
  el.focus();
  const caret=start+token.length;
  try{el.setSelectionRange(caret,caret)}catch(_){}
  el.dispatchEvent(new Event('input',{bubbles:true}));
}
function renderAgentTaskBook(a){
  const host=$('#agentTaskBookPanel');if(!host)return;
  const d=ensureAgentDraft(a),tp=d.taskPrompt;
  const block=(key,title,hint,example,items)=>`<section class="as-task-card" data-tp-list="${key}"><div class="as-task-card-head"><div><b>${title}</b><small>${hint}</small></div><em>${(items||[]).filter(Boolean).length}</em></div>${agentListEditorHTML(items,example)}<button type="button" class="as-add" data-list-add>+ 再加一条</button></section>`;
  const ph=[{t:'{{input}}',l:'任务输入'},{t:'{{artifacts}}',l:'上游产物'},{t:'{{feedback}}',l:'失败原因'},{t:'{{repository}}',l:'仓库'},{t:'{{upstream}}',l:'上游结论'}];
  host.innerHTML=`<p class="as-lead-line">用几句话写清「做成什么样」。下面的步骤和禁区会编进 Agent 的任务书，不用写 prompt 语法。</p>
    <label class="as-field as-hero"><span>做成什么样</span><textarea data-tp="objective" rows="4" placeholder="例如：读 Issue 和相关源码，把根因写到具体文件和函数。不要改业务代码，也不要跑测试。">${escapeHTML(tp.objective||'')}</textarea></label>
    <div class="as-ph-bar"><span>点一下插入到光标处</span>${ph.map(x=>`<button type="button" data-insert-ph="${x.t}" title="${x.l}">${x.l}</button>`).join('')}</div>
    <label class="as-field as-role-field"><span>对 Agent 怎么称呼</span><input data-tp="role" value="${escapeHTML(tp.role||a.name||'')}" placeholder="例如：Issue 根因分析师"></label>
    <div class="as-task-grid">
      ${block('actions','按这个顺序做','每条一句，运行时会按顺序写进任务书','读 Issue，再搜索相关源码，定位到文件与行',tp.actions)}
      ${block('constraints','这些事不要做','安全边界和职责外的事','不得改业务源码、不得跑测试、不得提交密钥',tp.constraints)}
    </div>
    <div class="as-task-grid">
      ${block('deliverables','要交什么文件','和「产物与门禁」里的路径对应起来','fix-plan.md：根因、影响面和修复步骤',tp.deliverables)}
      ${block('acceptanceCriteria','怎样算做完','这里写人话；真正拦运行的命令在下一步','fix-plan.md 写清根因，并点到具体文件',tp.acceptanceCriteria)}
    </div>
    <details class="as-fold as-fold-muted"><summary>更多可选说明<em>输入变量、工作区、失败后怎么改</em></summary><div class="as-fold-body as-task-more">
      <div data-tp-list="inputs"><p class="as-fold-hint">运行时会替换的变量，可点上方按钮插入</p>${agentListEditorHTML(tp.inputs,'任务输入：{{input}}')}<button type="button" class="as-add" data-list-add>+ 添加</button></div>
      <label class="as-field"><span>工作区里有什么</span><textarea data-tp="context" rows="3" placeholder="当前目录已经是检出好的 git 工作区，直接改相对路径。">${escapeHTML(tp.context||'')}</textarea></label>
      <label class="as-field"><span>失败或被打回时</span><textarea data-tp="retryInstructions" rows="3" placeholder="先读 failed_checks，只修失败项，不要重做已经通过的部分。">${escapeHTML(tp.retryInstructions||'')}</textarea></label>
    </div></details>`;
  const touch=()=>renderAgentSummary();
  host.onfocusin=e=>{const t=e.target;if(t&&(t.tagName==='TEXTAREA'||(t.tagName==='INPUT'&&t.type!=='checkbox'&&t.type!=='search')))host._tpFocus=t};
  host.querySelectorAll('[data-tp]').forEach(el=>{
    el.oninput=()=>{tp[el.dataset.tp]=el.value;touch();dirty()};
  });
  host.querySelectorAll('[data-insert-ph]').forEach(btn=>btn.onclick=()=>insertAgentPlaceholder(host,btn.dataset.insertPh));
  ['inputs','actions','deliverables','acceptanceCriteria','constraints'].forEach(key=>bindAgentListEditor(host,`[data-tp-list="${key}"]`,()=>tp[key]||[],items=>{tp[key]=items},'',touch));
}
function renderAgentGovernance(a){
  const host=$('#agentGovPanel');if(!host)return;
  const d=ensureAgentDraft(a);
  const loop=d.loop||{};
  const after=(d.interventions||[]).some(x=>x.stage==='after_execution');
  const publish=(d.interventions||[]).some(x=>x.stage==='before_publish');
  host.innerHTML=`<div class="as-section-stack">
    <p class="as-lead-line">运行时会先校验必需产物是否存在且符合契约，通过后才触发门禁命令。任一步失败都不会继续交付。</p>
    <section class="as-section-block as-loop-card"><div class="as-section-head"><b>做不对应再试一次</b><small>同一沙箱里重跑，适合测试失败后只修失败项</small></div><label class="as-toggle"><input name="loopEnabled" type="checkbox" ${loop.enabled?'checked':''}><div><b>启用自动重试</b><small>门禁失败后进入下一轮，直到通过或达到次数上限</small></div></label><div class="as-grid as-grid-tight"><label class="as-field"><span>最多试几轮</span><input name="loopMaxIterations" type="number" min="1" max="12" value="${loop.maxIterations||3}"></label><label class="as-field"><span>试完仍失败</span><select name="loopOnExhausted" class="select"><option value="fail" ${loop.onExhausted!=='human'?'selected':''}>标记失败并停下</option><option value="human" ${loop.onExhausted==='human'?'selected':''}>交给人工处理</option></select></label><label class="as-field wide"><span>下一轮怎么提醒它</span><textarea name="loopFeedbackPrompt" rows="2" placeholder="读 {{feedback}}，只修失败项，不要重做已经通过的部分">${escapeHTML(loop.feedbackPrompt||'')}</textarea></label></div></section>
    <section class="as-section-block"><div class="as-section-head"><b>要交的文件</b><small>写相对工作区的路径，例如 <code>fix-plan.md</code></small></div><div id="agentArtifactPanel"></div></section>
    <section class="as-section-block"><div class="as-section-head"><b>产物通过后的门禁命令</b><small>必需产物先校验；通过后才按顺序执行这些命令</small></div><div id="agentGatePanel"></div></section>
    <section class="as-section-block"><div class="as-section-head"><b>要不要人看一眼</b><small>勾选后，运行会停在「待我确认」</small></div><div class="as-toggle-grid"><label class="as-toggle"><input name="interventionAfter" type="checkbox" ${after?'checked':''}><div><b>做完后先复核</b><small>Agent 写完结论，人批准再往下走</small></div></label><label class="as-toggle"><input name="interventionPublish" type="checkbox" ${publish?'checked':''}><div><b>交文件前先确认</b><small>磁盘产物落盘前停一下</small></div></label></div></section>
  </div>`;
  renderAgentArtifactEditors(d);
  renderAgentGateEditors(d);
  host.querySelectorAll('input,select,textarea').forEach(el=>{
    const prevInput=el.oninput, prevChange=el.onchange;
    el.oninput=e=>{if(typeof prevInput==='function')prevInput.call(el,e);renderAgentSummary()};
    el.onchange=e=>{if(typeof prevChange==='function')prevChange.call(el,e);renderAgentSummary()};
  });
}
function renderAgentArtifactEditors(d){
  const host=$('#agentArtifactPanel');if(!host)return;
  const arts=d.artifactOutputs||[];
  host.innerHTML=arts.length?arts.map((a,i)=>`<article class="as-artifact" data-artifact-row="${i}"><div class="as-artifact-head"><span class="as-file-icon">📄</span><input data-ak="name" value="${escapeHTML(a.name||'')}" placeholder="给文件起个名字，例如 根因报告"><button type="button" data-remove-artifact="${i}" title="删除">×</button></div><label>写到哪个文件</label><input data-ak="path" type="text" value="${escapeHTML(a.path||'')}" placeholder="fix-plan.md"><div class="as-artifact-foot"><select data-ak="mediaType"><option value="application/json" ${a.mediaType==='application/json'?'selected':''}>JSON</option><option value="text/markdown" ${a.mediaType!=='application/json'?'selected':''}>Markdown</option></select><label><input type="checkbox" data-ak="required" ${a.required?'checked':''}> 必须交</label></div></article>`).join(''):'<div class="as-empty">还没有要交的文件。一般至少加一份结论，比如 <code>fix-plan.md</code>。</div>';
  host.insertAdjacentHTML('beforeend','<button type="button" class="as-add" id="agentAddArtifact">+ 添加要交的文件</button>');
  const touch=()=>renderAgentSummary();
  host.querySelectorAll('[data-artifact-row]').forEach(row=>row.querySelectorAll('[data-ak]').forEach(el=>el.oninput=el.onchange=()=>{const art=d.artifactOutputs[Number(row.dataset.artifactRow)];art[el.dataset.ak]=el.dataset.ak==='required'?el.checked:el.value;touch();dirty()}));
  host.querySelectorAll('[data-remove-artifact]').forEach(el=>el.onclick=()=>{d.artifactOutputs.splice(Number(el.dataset.removeArtifact),1);renderAgentGovernance(d);touch();dirty()});
  host.querySelector('#agentAddArtifact')?.addEventListener('click',()=>{d.artifactOutputs.push({name:'',path:'',mediaType:'text/markdown',required:true});renderAgentGovernance(d);touch();dirty()});
}
function gateFailureSelect(g){
  const v=['loop','reject','fail','human'].includes(g.onFailure)?g.onFailure:'loop',scope=g.executionScope==='repositories'?'repositories':'run';
  return `<label>检查范围</label><select data-gk="executionScope"><option value="run" ${scope==='run'?'selected':''}>Run工作区（校验产物等场景）</option><option value="repositories" ${scope==='repositories'?'selected':''}>每个代码仓库</option></select><label>失败后怎么走</label><select data-gk="onFailure"><option value="loop" ${v==='loop'?'selected':''}>本节点自修正</option><option value="reject" ${v==='reject'?'selected':''}>打回上游</option><option value="fail" ${v==='fail'?'selected':''}>本节点失败</option><option value="human" ${v==='human'?'selected':''}>转人工确认</option></select><p class="prop-hint">范围必须显式选择。Run 工作区适合报告和产物；每个代码仓库适合测试和 Git 检查。保存后按所选范围执行，不会再根据命令是否以 git 开头自动切换目录。</p>`;
}
function applyGateField(gate,el){
  if(el.dataset.gk==='name')gate.name=el.value;
  else if(el.dataset.gk==='onFailure')gate.onFailure=el.value;
  else if(el.dataset.gk==='executionScope')gate.executionScope=el.value;
  else gate.commands=el.value.split('\n').map(s=>s.trim()).filter(Boolean);
}
function renderAgentGateEditors(d){
  const host=$('#agentGatePanel');if(!host)return;
  const gates=d.verification||[];
  host.innerHTML=gates.length?gates.map((g,i)=>{
    const cmds=(g.commands||[]).filter(Boolean);
    const countText=cmds.length?`已配置 ${cmds.length} 条验收命令`:'未配置 (点击编写)';
    const preview=cmds.length?`<div class="script-snippet-preview" data-editor-agent-gate="${i}" title="点击在代码编辑器中修改"><code>${escapeHTML(cmds.join(' && '))}</code></div>`:'';
    return `<article class="as-gate" data-gate-row="${i}"><div class="as-gate-head"><span class="as-file-icon">⌘</span><input data-gk="name" value="${escapeHTML(g.name||'')}" placeholder="例如：文件必须存在"><button type="button" data-remove-gate="${i}" title="删除">×</button></div><div class="gate-script-trigger-wrap"><label style="font-size:11px;color:var(--muted);font-weight:600">验收命令</label><button type="button" class="script-editor-launch-btn btn-open-code-editor" data-editor-agent-gate="${i}"><span>⛶ 打开代码编辑器编写命令</span><span class="script-btn-badge">${countText}</span></button>${preview}</div>${gateFailureSelect(g)}</article>`;
  }).join(''):'<div class="as-empty">还没有验收命令。可以先加一条检查文件在不在，例如 <code>test -f fix-plan.md</code>。</div>';
  host.insertAdjacentHTML('beforeend',`<button type="button" class="as-add" id="agentAddGate">+ 添加验收命令</button>`);
  const touch=()=>renderAgentSummary();
  host.querySelectorAll('[data-gate-row]').forEach(row=>row.querySelectorAll('[data-gk]').forEach(el=>el.oninput=el.onchange=()=>{applyGateField(d.verification[Number(row.dataset.gateRow)],el);touch();dirty()}));
  host.querySelectorAll('[data-remove-gate]').forEach(el=>el.onclick=()=>{d.verification.splice(Number(el.dataset.removeGate),1);renderAgentGovernance(d);touch();dirty()});
  host.querySelector('#agentAddGate')?.addEventListener('click',()=>{d.verification.push({id:`gate-${d.verification.length+1}`,name:'',mode:'command',stage:'after_execution',commands:[''],required:true,onFailure:'loop',executionScope:'run'});renderAgentGovernance(d);touch();dirty()});
  host.querySelectorAll('[data-editor-agent-gate]').forEach(btn=>{
    btn.onclick=()=>{
      const idx=Number(btn.dataset.editorAgentGate);
      const gate=d.verification[idx];
      if(!gate)return;
      openCodeEditorModal({
        title:`验收命令 · ${gate.name||'门禁 '+(idx+1)}`,
        subtitle:'Shell 验证脚本，支持多行控制流（if/for/函数等），整体作为一个脚本执行',
        language:'shell',
        value:(gate.commands||[]).join('\n'),
        onSave:(newVal)=>{
          gate.commands=newVal.split('\n').map(s=>s.trim()).filter(Boolean);
          renderAgentGateEditors(d);
          renderAgentSummary();
          dirty();
        }
      });
    };
  });
}
function effectiveNodeArtifacts(node,agent){
  const merged=[],index=new Map();
  [...(agent?.artifactOutputs||[]),...(node?.artifactOutputs||[])].forEach(item=>{
    const key=String(item.name||item.path||'').trim();
    if(index.has(key))merged[index.get(key)]=item;
    else{index.set(key,merged.length);merged.push(item)}
  });
  return merged;
}
function roleGates(agent){
  return [...(agent?.verification||[])].filter(g=>g.mode!=='submit');
}
function nodeGatePane(n){
  const agent=workflowAgent(n.agentId);
  const artifacts=effectiveNodeArtifacts(n,agent);
  const gates=roleGates(agent);
  const artifactRows=artifacts.length?artifacts.map(a=>`<article class="as-artifact"><div class="as-artifact-head"><span class="as-file-icon">📄</span><b>${escapeHTML(a.name||a.path||'未命名产物')}</b></div><label>${escapeHTML(a.path||'未设置路径')}</label><div class="as-artifact-foot"><span>${escapeHTML(a.mediaType||'text/markdown')}</span><span>${a.required?'必须交':'可选'}</span></div></article>`).join(''):'<div class="as-empty">这个节点没有声明需要校验的产物。</div>';
  const gateRows=gates.length?gates.map(g=>{const cmds=(g.commands||[]).filter(Boolean);return `<article class="as-gate"><div class="as-gate-head"><span class="as-file-icon">⌘</span><b>${escapeHTML(g.name||g.id||'未命名门禁')}</b></div><div class="script-snippet-preview"><code>${escapeHTML(cmds.join(' && ')||'人工确认')}</code></div><p class="prop-hint">${g.executionScope==='repositories'?'每个代码仓库':'Run 工作区'} · 失败后 ${escapeHTML({loop:'本节点自修正',reject:'打回上游',human:'转人工确认',fail:'本节点失败'}[g.onFailure]||'按节点策略')}</p></article>`}).join(''):'<div class="as-empty">这个节点没有额外门禁命令。</div>';
  const terminal=n.type==='agent'?`<section class="as-section-block"><div class="as-section-head"><b>1. 先提交结构化终态</b><small>平台内建，无需配置</small></div><article class="as-gate"><div class="as-gate-head"><span class="as-file-icon">✓</span><b>结构化节点终态</b></div><div class="script-snippet-preview"><code>pass | rework | fail | human</code></div><p class="prop-hint">Agent 完成任务后必须先调用 submit_gate_result 提交终态；缺失时自动恢复同一会话重试。</p></article></section>`:'';
  return `<p class="prop-hint">执行时 Agent 节点先提交结构化终态，随后校验产物，最后执行可选门禁命令。</p>${terminal}<section class="as-section-block"><div class="as-section-head"><b>2. 再校验产物</b><small>${artifacts.length} 项</small></div>${artifactRows}</section><section class="as-section-block"><div class="as-section-head"><b>3. 最后触发额外门禁</b><small>${gates.length} 项</small></div>${gateRows}</section>${agent?'<button type="button" class="button ghost small" id="openBoundAgentValidation">去角色定义编辑校验清单</button>':''}`;
}
function renderAgentAssetsPanel(a){
  const host=$('#agentAssetsPanel');if(!host)return;
  const d=ensureAgentDraft(a);
  const generic=catalogGeneric(state.workflow);
  const col=(title,sub,name,items,selected)=>{const vis=name==='mcpServers'?(generic?(items||[]):visibleMCPAssets(items)):(name==='rules'||name==='skills'?(items||[]).filter(item=>catalogVisible(item.projectId,item.shared)):visibleAssets(items));const picked=vis.filter(item=>name==='mcpServers'&&generic?genericMCPChecked(item.id,selected):(selected||[]).includes(item.id)).length;const empty=name==='mcpServers'?(generic?'通用模板绑定市场别名（如 github），运行时落到当前项目已安装的 MCP':'这个项目或你的个人范围还没有 MCP，先到「MCP 管理」里添加'):name==='rules'?'这个项目还没有可用规则，先到「规则管理」里添加或共享':name==='skills'?'这个项目还没有可用技能，先到「技能管理」里添加或共享':'这个项目还没有此类配置，先到「环境资产」里添加';return `<div class="as-asset-col"><div class="as-asset-col-head"><b>${title}</b><span>${sub}${picked?` · 已选 ${picked}`:''}</span></div><div class="as-asset-search"><span>⌕</span><input type="search" data-asset-filter="${name}" placeholder="搜索名称…"></div><div class="as-asset-list" data-asset-list="${name}">${vis.map(x=>`<label class="as-asset-item"><input type="checkbox" name="${name}" value="${escapeHTML(x.id)}" ${(name==='mcpServers'&&generic?genericMCPChecked(x.id,selected):(selected||[]).includes(x.id))?'checked':''}><div><b>${escapeHTML(x.name)}</b><small>${escapeHTML(x.description||x.id)}${x.shared&&x.projectId!==state.projectFilter?' · 共享':''}</small></div></label>`).join('')||`<div class="as-empty">${empty}</div>`}</div></div>`};
  host.innerHTML=`<p class="as-lead-line">勾选这个角色能用的规则、技能和外部工具。Hook 只在这个角色跑的时候生效。</p><section class="as-section-block as-platform-mcp-section"><div class="as-section-head"><b>平台能力</b><small>独立开关，不占用 MCP 列表</small></div><label class="as-toggle"><input type="checkbox" name="platformMcp" ${d.platformMcp?'checked':''}><div><b>接入平台 MCP（AgentFlow）</b><small>开启后，这个角色运行时通过平台 MCP（agentflow-platform）直接调用平台侧功能：需求、任务、运行、项目等平台操作。</small></div></label><label class="as-toggle"><input type="checkbox" name="knowledgeBase" ${d.knowledgeBase?'checked':''}><div><b>接入团队知识库</b><small>开启后，这个角色运行时可只读检索团队知识库（agentflow-knowledge），并自动附带「知识库使用规范」规则；产出的 knowledge.md 会进入知识沉淀审核队列。</small></div></label></section><div class="as-assets">${col('必须遵守','规则','rules',state.data.rules,d.rules)}${col('怎么干活','技能','skills',state.data.skills,d.skills)}${col('外部工具','MCP','mcpServers',generic?genericTemplateMCPOptions(d.mcpServers):state.data.mcpServers,d.mcpServers)}</div><section class="as-section-block as-hook-section"><div class="as-section-head"><b>运行时 Hook</b><small>在工具调用前后插一段脚本，比如拦危险命令。一般可以先不配。</small></div><div id="agentHookPanel"></div></section>`;
  host.querySelectorAll('[data-asset-filter]').forEach(input=>input.oninput=()=>{const q=input.value.trim().toLowerCase();input.closest('.as-asset-col').querySelectorAll('.as-asset-item').forEach(row=>row.hidden=!!q&&!row.textContent.toLowerCase().includes(q))});
  host.querySelectorAll('input[type=checkbox]').forEach(el=>el.onchange=()=>{renderAgentSummary();dirty()});
  const platformToggle=host.querySelector('input[name="platformMcp"]');
  if(platformToggle)platformToggle.onchange=()=>{d.platformMcp=platformToggle.checked;renderAgentSummary();dirty()};
  const knowledgeToggle=host.querySelector('input[name="knowledgeBase"]');
  if(knowledgeToggle)knowledgeToggle.onchange=()=>{d.knowledgeBase=knowledgeToggle.checked;renderAgentSummary();dirty()};
  renderAgentHookEditors(d);
}
function renderAgentHookEditors(d){
  const host=$('#agentHookPanel');if(!host)return;
  const hooks=d.hooks||[];
  const events=['SessionStart','UserPromptSubmit','PreToolUse','PostToolUse','Stop'];
  host.innerHTML=hooks.length?hooks.map((h,i)=>{
    const sc=(h.script||'').trim();
    const lineCount=sc?sc.split('\n').length:0;
    const countText=lineCount?`已编写 ${lineCount} 行 (${h.language==='python'?'Python':'Shell'})`:'未配置 (点击编写)';
    const preview=lineCount?`<div class="script-snippet-preview" data-editor-hook="${i}" title="点击在代码编辑器中修改"><code>${escapeHTML(sc.slice(0, 120))}${sc.length>120?'…':''}</code></div>`:'';
    return `<article class="as-hook" data-hook-row="${i}"><div class="as-hook-head"><input data-hk="name" value="${escapeHTML(h.name||'')}" placeholder="Hook 名称"><input data-hk="id" value="${escapeHTML(h.id||'')}" placeholder="hook-id"><button type="button" data-remove-hook="${i}" title="删除">×</button></div><div class="as-hook-grid"><label>触发事件<select data-hk="event">${events.map(v=>`<option value="${v}" ${h.event===v?'selected':''}>${v}</option>`).join('')}</select></label><label>执行范围<select data-hk="executionScope"><option value="run" ${h.executionScope!=='repositories'?'selected':''}>Run工作区（校验产物等场景）</option><option value="repositories" ${h.executionScope==='repositories'?'selected':''}>每个代码仓库</option></select></label><label>语言<select data-hk="language"><option value="shell" ${h.language!=='python'?'selected':''}>Shell</option><option value="python" ${h.language==='python'?'selected':''}>Python</option></select></label><label>超时（秒）<input type="number" min="1" max="600" data-hk="timeoutSec" value="${Number(h.timeoutSec)||10}"></label><label>Matcher（可选）<input data-hk="matcher" value="${escapeHTML(h.matcher||'')}" placeholder="Bash|Edit"></label></div><div class="gate-script-trigger-wrap"><label style="font-size:11px;color:var(--muted);font-weight:600">Hook 脚本</label><button type="button" class="script-editor-launch-btn btn-open-code-editor" data-editor-hook="${i}"><span>⛶ 打开代码编辑器编写脚本</span><span class="script-btn-badge">${countText}</span></button>${preview}</div></article>`;
  }).join(''):'<div class="as-empty">没有自定义 Hook。系统观测 Hook 不受这里影响。</div>';
  host.insertAdjacentHTML('beforeend','<button type="button" class="as-add" id="agentAddHook">+ 添加 Hook</button>');
  host.querySelectorAll('[data-hook-row]').forEach(row=>row.querySelectorAll('[data-hk]').forEach(el=>el.oninput=el.onchange=()=>{const hook=d.hooks[Number(row.dataset.hookRow)];hook[el.dataset.hk]=el.type==='number'?Number(el.value):el.value;renderAgentSummary()}));
  host.querySelectorAll('[data-remove-hook]').forEach(el=>el.onclick=()=>{d.hooks.splice(Number(el.dataset.removeHook),1);renderAgentAssetsPanel(d);renderAgentSummary()});
  host.querySelector('#agentAddHook')?.addEventListener('click',()=>{const index=d.hooks.length+1;d.hooks.push({id:`hook-${index}`,name:`Hook ${index}`,event:'PreToolUse',matcher:'',language:'shell',script:'',timeoutSec:10,executionScope:'run'});renderAgentAssetsPanel(d);renderAgentSummary()});
  host.querySelectorAll('[data-editor-hook]').forEach(btn=>{
    btn.onclick=()=>{
      const idx=Number(btn.dataset.editorHook);
      const hook=d.hooks[idx];
      if(!hook)return;
      const lang=hook.language==='python'?'python':'shell';
      openCodeEditorModal({
        title:`Hook 脚本 · ${hook.name||'Hook '+(idx+1)}`,
        subtitle:`触发事件: ${hook.event} · 语言: ${lang}`,
        language:lang,
        value:hook.script||'',
        onSave:(newVal, newLang)=>{
          hook.script=newVal;
          if(newLang==='python'||newLang==='shell'){
            hook.language=newLang;
          }
          renderAgentHookEditors(d);
          renderAgentSummary();
          dirty();
        }
      });
    };
  });
}
function renderAgentSummary(){
  const host=$('#agentSummaryBar');if(!host)return;
  const f=$('#agentForm'),d=state.agentDraft||{};
  const exec=(state.data.executors||[]).find(x=>x.id===(f?.elements?.executorId?.value||d.executorId));
  const model=agentEffectiveModel(f)||d.model||exec?.model||exec?.name||'未选模型';
  const effort=(f?.elements?.effort?.value||'').trim();
  const arts=(d.artifactOutputs||[]).filter(x=>x.name||x.path).length;
  const gates=(d.verification||[]).filter(x=>x.name||(x.commands||[]).length).length;
  const loopOn=f?.elements?.loopEnabled?.checked??d.loop?.enabled;
  host.innerHTML=`<span class="as-chip">${escapeHTML(effort?`${model} · ${effort}`:model)}</span>${d.platformMcp?'<span class="as-chip on">平台 MCP</span>':''}${d.knowledgeBase?'<span class="as-chip on">知识库</span>':''}<span class="as-chip ${loopOn?'on':''}">${loopOn?`${f?.elements?.loopMaxIterations?.value||d.loop?.maxIterations||3} 轮 Loop`:'单轮'}</span><span class="as-chip">${arts} 产物</span><span class="as-chip">${gates} 门禁</span><span class="as-chip">${(d.hooks||[]).length} Hook</span>`;
}
function renderAgentPreview(a){
  renderAgentSummary();
}
function readAgentDraft(f){
  const d=state.agentDraft||{};
  const tp=d.taskPrompt||{};
  const hasTaskBook=[tp.role,tp.objective,tp.context,tp.retryInstructions,...(tp.inputs||[]),...(tp.actions||[]),...(tp.deliverables||[]),...(tp.acceptanceCriteria||[]),...(tp.constraints||[])].some(v=>String(v||'').trim());
  return {taskPrompt:hasTaskBook?tp:null,artifactOutputs:d.artifactOutputs||[],verification:d.verification||[],hooks:d.hooks||[]};
}
function sectionHead(title,hint,count,unit){
  const label=count==null?'':`${count} ${unit||'条'}`;
  return `<div class="prop-section-head"><div><b>${title}</b>${hint?`<small>${hint}</small>`:''}</div>${count!=null?`<em>${label}</em>`:''}</div>`;
}
function agentBindCard(n){
  const agent=workflowAgent(n.agentId);
  const exec=(state.data.executors||[]).find(x=>x.id===(agent?.executorId||n.executorId));
  const generic=catalogGeneric(state.workflow);
  const projectId=state.workflow?.projectId||state.projectFilter||'',projects=state.data.projects||[];
  const invalid=n.agentId&&!agent?'<option value="" selected disabled>当前绑定角色不可用，请重新选择</option>':'';
  const available=workflowAgents(),owned=available.filter(x=>!generic&&x.projectId===projectId),shared=available.filter(x=>!generic&&x.projectId!==projectId);
  const options=items=>items.map(x=>{const owner=projects.find(p=>p.id===x.projectId)?.name||x.projectId;return `<option value="${escapeHTML(x.id)}" ${x.id===n.agentId?'selected':''}>${escapeHTML(x.name)}${!generic&&x.projectId!==projectId?` · 来自 ${escapeHTML(owner)}`:''}</option>`}).join('');
  const grouped=generic
    ?(available.length?`<optgroup label="通用模板角色 · ${available.length} 个">${options(available)}</optgroup>`:'')
    :(owned.length?`<optgroup label="本项目角色 · ${owned.length} 个">${options(owned)}</optgroup>`:'')+(shared.length?`<optgroup label="共享角色 · ${shared.length} 个">${options(shared)}</optgroup>`:'');
  const opts=(invalid+grouped)||(generic?'<option value="">该模板还没有打包角色</option>':'<option value="">当前项目暂无可用角色</option>');
  const arts=effectiveNodeArtifacts(n,agent).map(a=>`${a.path||a.name}${a.required?' (必需)':''}`).filter(Boolean).join('、')||'未声明';
  const gates=roleGates(agent).map(g=>g.name||g.id).filter(Boolean).join('、')||'未声明';
  const taskHint=agent?.taskPrompt?.objective?`任务：${agent.taskPrompt.objective.slice(0,48)}…`:'任务书在角色定义第 2 步';
  const hint=generic?'这些角色打包在通用模板里，和当前项目目录无关。改任务书只影响平台模板，所有项目下次运行生效。':`本项目 ${owned.length} 个 · 共享 ${shared.length} 个；其他项目私有角色已隐藏。`;
  return `<div class="agent-bind-card"><label>这个步骤用哪个角色</label><select class="select" data-p="agentId">${opts}</select><p class="prop-hint">${hint}</p>${agent?`<p class="agent-bind-meta"><b>${escapeHTML(agent.name)}</b> · ${escapeHTML(agent.model||exec?.model||exec?.name||'未选模型')}<br><small>${escapeHTML(taskHint)}</small></p><dl class="agent-contract-preview"><div><dt>会校验的产物</dt><dd>${escapeHTML(arts)}</dd></div><div><dt>统一门禁</dt><dd>${escapeHTML(gates)}</dd></div><div><dt>执行顺序</dt><dd>先交终态，再校验产物，后触发门禁</dd></div><div><dt>Loop</dt><dd>${agent.loop?.enabled?`${agent.loop.maxIterations||1} 轮自修正`:'单轮'}</dd></div></dl><button type="button" class="button ghost small" id="openBoundAgent">去角色定义编辑任务书</button>`:'<p class="prop-hint">先选一个已定义的角色。画布只编排顺序和打回，不改编排角色内部的任务。</p>'}</div>`;
}
function renderPreviewBuildProperties(host,n){
  const model=workflowPreviewModel(state.workflow);
  const inEdges=state.workflow.edges.filter(e=>e.target===n.id);
  const inChips=inEdges.length?inEdges.map(e=>{
    const src=state.workflow.nodes.find(x=>x.id===e.source);
    const name=src?src.name:'未知步骤';
    return `<span class="prop-edge-chip" title="前序步骤：${escapeHTML(name)}"><span class="prop-edge-dir">←</span><span class="prop-edge-name">${escapeHTML(name)}</span></span>`;
  }).join(''):'<span class="prop-edge-none">无前序依赖（从上游节点连线到此）</span>';
  const spa=state.workflow.preview?.spaFallback!==false;
  host.innerHTML=`<div class="prop-header sticky"><div><h3>${escapeHTML(n.name)}</h3><small>系统步骤 · 前端预览</small></div></div>
    <div class="prop-body"><div class="prop-pane prop-compact">
      <p class="prop-hint">从左侧「流程步骤」面板添加，可插入流程任意位置。运行到该节点时用所选模型构建静态站点，不必再手写脚本节点；Run 成功后平台归档预览。</p>
      <label>CodeBuddy 模型</label>
      <select class="select" id="previewNodeModel"></select>
      <label class="flow-check-row" style="margin-top:10px"><input type="checkbox" id="previewNodeSpa" ${spa?'checked':''}><span>单页应用回退（找不到路径时返回入口 HTML）</span></label>
      <div class="prop-edge-manager" style="margin-top:12px"><div class="prop-edge-group"><div class="prop-edge-title">前序依赖（入边 ${inEdges.length}）</div><div class="prop-edge-chips">${inChips}</div></div></div>
    </div>
    <div class="prop-footer"><button id="deletePropertyNode" class="button ghost danger">移除预览节点</button></div></div>`;
  fillPreviewModelSelect($('#previewNodeModel'), model);
  const sel=$('#previewNodeModel');
  if(sel)sel.onchange=()=>{updatePreviewContract({model:sel.value});dirty();renderCanvas();};
  const spaBox=$('#previewNodeSpa');
  if(spaBox)spaBox.onchange=()=>{updatePreviewContract({spaFallback:spaBox.checked});dirty();};
  const del=$('#deletePropertyNode');
  if(del)del.onclick=async()=>{
    if(!(await afConfirm({title:'移除前端预览',message:'将移除「构建静态站点」节点，并不再归档预览。',danger:true,confirmText:'移除节点'})))return;
    state.selectedNode=n.id;
    deleteSelected();
  };
}
function renderProperties(){
  const host=$('#properties');if(!host)return;
  if(!state.workflow){host.innerHTML='<div class="empty-properties"><div>◇</div><b>流程还没加载完</b><p>稍候刷新，或从「SOP 目录」打开一条已发布流程。</p></div>';return}
  const n=state.workflow.nodes.find(node=>node.id===state.selectedNode);
  if(!n){host.innerHTML='<div class="empty-properties"><div>◇</div><b>点左侧步骤或画布节点</b><p>Agent 步骤绑定角色、连线、统一校验清单和失败打回。节点会同步展示角色定义的产物与门禁。</p></div>';return}
  if(isPreviewBuildNode(n.id)){renderPreviewBuildProperties(host,n);return}
  ensureNodeDefaults(n);
  const targets=state.workflow.nodes.map(x=>`<option value="${x.id}" ${x.id===n.rejectTo?'selected':''}>${escapeHTML(x.name)}${x.id===n.id?'（当前节点）':''}</option>`).join('');
  const typeLabel=n.type==='agent'?'Agent 步骤':n.type==='script'?'脚本步骤':n.type==='sop'?'SOP 步骤':'人工门禁';
  const tabs=n.type==='agent'?[{id:'basic',label:'角色'},{id:'gates',label:'校验'},{id:'fail',label:'失败返工'}]:n.type==='script'?[{id:'basic',label:'基本'},{id:'task',label:'脚本'},{id:'fail',label:'失败返工'}]:n.type==='sop'?[{id:'basic',label:'基本'},{id:'handoff',label:'下游产物'},{id:'fail',label:'失败返工'}]:[{id:'basic',label:'基本'},{id:'fail',label:'失败返工'}];
  let tab=state.propTab||'basic';if(!tabs.some(t=>t.id===tab))tab='basic';
  const inEdges=state.workflow.edges.filter(e=>e.target===n.id);
  const outEdges=state.workflow.edges.filter(e=>e.source===n.id);
  const inChips=inEdges.length?inEdges.map(e=>{
    const src=state.workflow.nodes.find(x=>x.id===e.source);
    const name=src?src.name:'未知步骤';
    return `<span class="prop-edge-chip" title="前序步骤：${escapeHTML(name)}"><span class="prop-edge-dir">←</span><span class="prop-edge-name">${escapeHTML(name)}</span><button type="button" class="prop-edge-del" data-del-edge="${escapeHTML(e.id)}" title="删除此连线">×</button></span>`;
  }).join(''):'<span class="prop-edge-none">无（流程起点）</span>';

  const outChips=outEdges.length?outEdges.map(e=>{
    const tgt=state.workflow.nodes.find(x=>x.id===e.target);
    const name=tgt?tgt.name:'未知步骤';
    return `<span class="prop-edge-chip" title="后续步骤：${escapeHTML(name)}"><span class="prop-edge-dir">→</span><span class="prop-edge-name">${escapeHTML(name)}</span><button type="button" class="prop-edge-del" data-del-edge="${escapeHTML(e.id)}" title="删除此连线">×</button></span>`;
  }).join(''):'<span class="prop-edge-none">无（流程末端）</span>';

  const rejectTarget=n.rejectTo?state.workflow.nodes.find(x=>x.id===n.rejectTo):null;
  const rejectChip=rejectTarget?`<div class="prop-edge-group reject"><div class="prop-edge-title"><span class="reject-icon">⇠</span> 失败打回到</div><div class="prop-edge-chips"><span class="prop-edge-chip reject" title="失败打回目标：${escapeHTML(rejectTarget.name)}"><span class="prop-edge-name">${escapeHTML(rejectTarget.name)}</span><button type="button" class="prop-edge-del" data-del-reject="${escapeHTML(n.id)}" title="取消打回">×</button></span></div></div>`:'';

  const edgesBlock=`<div class="prop-edge-manager">
    <div class="prop-edge-group"><div class="prop-edge-title">前序依赖（入边 ${inEdges.length}）</div><div class="prop-edge-chips">${inChips}</div></div>
    <div class="prop-edge-group"><div class="prop-edge-title">后续步骤（出边 ${outEdges.length}）</div><div class="prop-edge-chips">${outChips}</div></div>
    ${rejectChip}
    <div class="prop-edge-hint">💡 悬停画布连线会出现 <b>×</b> 快捷删除；也可点上方标签的 × 移除依赖。</div>
  </div>`;

  const sopChoices=sopWorkflowChoices(state.workflow.id);
  const sopSelect=n.type==='sop'?`<label>要跑的 SOP</label><select class="select" data-p="sopWorkflowId"><option value="">选择一条已有流程</option>${sopChoices.map(w=>`<option value="${escapeHTML(w.id)}" ${w.id===n.sopWorkflowId?'selected':''}>${escapeHTML(w.name)}</option>`).join('')}</select><p class="prop-hint">${escapeHTML((sopChoices.find(w=>w.id===n.sopWorkflowId)||{}).description||'这条 SOP 会作为子流程跑完，声明的产物交给下一步。')}</p>`:'';
  const basic=`<div class="prop-compact">
      <label>步骤名称</label><input class="input" data-p="name" value="${escapeHTML(n.name)}">
      ${n.type==='sop'?'<p class="prop-hint">子 SOP 容器不重复计入阶段耗时；请在子流程的实际执行节点上标注统计阶段。</p>':`<label>效能统计阶段</label><select class="select" data-p="metricStage"><option value="" ${!n.metricStage?'selected':''}>未标注（旧流程兼容推断）</option><option value="clarification" ${n.metricStage==='clarification'?'selected':''}>需求澄清</option><option value="development" ${n.metricStage==='development'?'selected':''}>研发</option><option value="testing" ${n.metricStage==='testing'?'selected':''}>测试</option><option value="none" ${n.metricStage==='none'?'selected':''}>不计入阶段耗时</option></select><p class="prop-hint">保存后的新 Run 会冻结此标记；历史 Run 不会被新配置改写。</p>`}
      <div class="property-grid">${n.type==='agent'?`<div class="wide">${agentBindCard(n)}</div>`:''}</div>
      ${sopSelect}
      ${n.type==='approval'?'<p class="prop-hint">确认步骤会停在「待我确认」，批准后才继续。</p>':''}
      ${edgesBlock}
    </div>`;
  const scriptLines=(n.script||'').trim()?n.script.trim().split('\n').length:0;
  const scriptCountText=scriptLines?`已编写 ${scriptLines} 行 (${n.scriptLanguage==='python'?'Python':'Shell'})`:'未配置 (点击编写)';
  const scriptPreview=scriptLines?`<div class="script-snippet-preview" id="previewScriptNodeEditor" title="点击在代码编辑器中修改"><code>${escapeHTML(n.script.slice(0, 120))}${n.script.length>120?'…':''}</code></div>`:'';
  const taskPane=n.type==='script'
    ? `<label>执行范围</label><select class="select" data-p="executionScope"><option value="run" ${n.executionScope!=='repositories'?'selected':''}>Run工作区（校验产物等场景）</option><option value="repositories" ${n.executionScope==='repositories'?'selected':''}>每个代码仓库</option></select><label class="toggle-row"><input type="checkbox" data-p="runOnWorkspaceRebuild" ${n.runOnWorkspaceRebuild?'checked':''}>工作区重建时先执行</label><p class="prop-hint">正常运行按流程执行；重建工作区时，先补跑已完成且开启此项的脚本。请确保脚本可重复执行并保留已有修改。</p><label>脚本语言</label><select class="select" data-p="scriptLanguage"><option value="shell" ${n.scriptLanguage!=='python'?'selected':''}>Shell</option><option value="python" ${n.scriptLanguage==='python'?'selected':''}>Python</option></select><div class="gate-script-trigger-wrap"><label style="font-size:11px;color:var(--muted);font-weight:600">脚本内容</label><button type="button" class="script-editor-launch-btn btn-open-code-editor" id="openScriptNodeEditor"><span>⛶ 打开代码编辑器编写脚本</span><span class="script-btn-badge">${scriptCountText}</span></button>${scriptPreview}</div><p class="prop-hint">逐仓库执行时会自动进入每个仓库根目录，无需手写 cd。</p>`
    : '<p class="prop-hint">人工门禁没有任务书。</p>';
  const fail=`<div class="failure-choice"><label class="${n.onFailure!=='reject'?'active':''}"><input type="radio" name="onFailure" value="fail" ${n.onFailure!=='reject'?'checked':''}><span><b>停下来</b><small>重试用尽后标记失败</small></span></label><label class="${n.onFailure==='reject'?'active':''}"><input type="radio" name="onFailure" value="reject" ${n.onFailure==='reject'?'checked':''}><span><b>打回重做</b><small>重置目标步骤及其下游</small></span></label></div>
    <div class="failure-route ${n.onFailure==='reject'?'':'disabled'}"><label>打回到</label><select id="rejectTarget" class="select" ${n.onFailure==='reject'?'':'disabled'}><option value="">请选择步骤</option>${targets}</select></div>
    <div class="property-grid"><div><label>单轮最多试几次</label><input class="input" type="number" min="1" max="9" data-p="maxAttempts" value="${n.maxAttempts||1}"></div><div><label>最多返工几轮</label><input class="input" type="number" min="1" max="20" data-p="maxReworks" value="${n.maxReworks||3}"></div></div>
    <label>返工策略</label><select class="select" data-p="retryStrategy"><option value="targeted_fix" ${n.retryStrategy!=='full_redo'?'selected':''}>只修失败项</option><option value="full_redo" ${n.retryStrategy==='full_redo'?'selected':''}>整步重做</option></select>
    <p class="prop-hint">${n.type==='agent'?'这里管节点本身失败后怎么走。产物和门禁使用「校验」页里的统一清单。':'停下来或打回轮次用尽后，运行页可以人工通过该节点继续下游，避免硬脚本一直卡住。'}</p>`;
  const handoffPane=n.type==='sop'
    ? `<p class="prop-hint">子 SOP 成功后，下列文件会出现在本节点，供下一步读取。留空则收下子流程交出的全部产物。</p><div id="artifactContractRows">${artifactRowsHTML(n)}</div><button type="button" class="button ghost small" id="addSOPHandoff">＋ 增加下游产物</button>`
    : '';
  const panes={basic,task:taskPane,gates:nodeGatePane(n),fail,handoff:handoffPane};
  host.innerHTML=`<div class="prop-header sticky"><div><h3>${escapeHTML(n.name)}</h3><small>${typeLabel}</small></div></div>
    <div class="prop-tabs">${tabs.map(t=>`<button type="button" data-prop-tab="${t.id}" class="${tab===t.id?'active':''}">${t.label}</button>`).join('')}</div>
    <div class="prop-body"><div class="prop-pane">${panes[tab]||basic}</div>
    <div class="prop-footer">${state.connectFrom===n.id?'<div class="connection-mode">正在连线：再点目标步骤，或按 Esc 取消</div>':'<button id="connectNode" class="button ghost">连接到下一步</button>'}<button id="deletePropertyNode" class="button ghost danger">删除步骤</button></div></div>`;
  host.querySelectorAll('[data-prop-tab]').forEach(b=>b.onclick=()=>{state.propTab=b.dataset.propTab;renderProperties()});
  host.querySelectorAll('[data-del-edge]').forEach(b=>{b.onclick=async ev=>{ev.stopPropagation();await deleteEdgeById(b.dataset.delEdge)}});
  host.querySelectorAll('[data-del-reject]').forEach(b=>{b.onclick=async ev=>{ev.stopPropagation();await deleteRejectEdgeByNodeId(b.dataset.delReject)}});
  host.querySelectorAll('[data-p]').forEach(el=>el.oninput=()=>{n[el.dataset.p]=el.type==='checkbox'?el.checked:el.type==='number'?Number(el.value):el.value;const title=host.querySelector('.prop-header h3');if(title&&el.dataset.p==='name')title.textContent=el.value;dirty();if(el.dataset.p==='agentId'){renderProperties();renderCanvas();return}renderCanvas()});
  const sopSel=host.querySelector('[data-p="sopWorkflowId"]');
  const renderedSOPId=n.sopWorkflowId;
  if(sopSel)sopSel.onchange=()=>{
    const choices=sopWorkflowChoices(state.workflow.id);
    const prev=choices.find(w=>w.id===renderedSOPId);
    const artifactKey=list=>JSON.stringify((list||[]).map(a=>[a.name||'',a.path||'']));
    n.sopWorkflowId=sopSel.value;
    const wf=choices.find(w=>w.id===n.sopWorkflowId);
    if(wf&&(!(n.artifactOutputs||[]).length||(prev&&artifactKey(n.artifactOutputs)===artifactKey(sopDeclaredArtifacts(prev)))))n.artifactOutputs=sopDeclaredArtifacts(wf);
    if(wf&&(!n.name||n.name==='SOP 节点'||n.name===prev?.name))n.name=wf.name;
    dirty();renderProperties();renderCanvas();
  };
  host.querySelector('#addSOPHandoff')?.addEventListener('click',()=>{n.artifactOutputs=n.artifactOutputs||[];n.artifactOutputs.push({name:'',path:'',mediaType:'text/markdown',required:true});renderProperties();dirty()});
  if(tab==='handoff')bindArtifactRows(host,n);
  const openBound=$('#openBoundAgent');if(openBound)openBound.onclick=()=>{const agent=workflowAgent(n.agentId);if(!agent)return;if(!catalogGeneric(state.workflow))switchView('agents');openAgentWizard(agent)};
  const openBoundValidation=$('#openBoundAgentValidation');if(openBoundValidation)openBoundValidation.onclick=()=>{const agent=workflowAgent(n.agentId);if(!agent)return;if(!catalogGeneric(state.workflow))switchView('agents');openAgentWizard(agent,3)};
  host.querySelectorAll('input[name="onFailure"]').forEach(el=>el.onchange=()=>{n.onFailure=el.value;if(el.value==='fail')n.rejectTo='';host.querySelectorAll('.failure-choice label').forEach(l=>l.classList.toggle('active',l.querySelector('input').value===el.value));const route=host.querySelector('.failure-route'),sel=$('#rejectTarget');if(route)route.classList.toggle('disabled',el.value!=='reject');if(sel)sel.disabled=el.value!=='reject';dirty();renderCanvas()});
  const reject=$('#rejectTarget');if(reject)reject.onchange=e=>{n.rejectTo=e.target.value;n.onFailure=n.rejectTo?'reject':'fail';dirty();renderCanvas()};
  const connectButton=$('#connectNode');if(connectButton)connectButton.onclick=()=>{state.connectFrom=n.id;renderCanvas();renderProperties()};
  const del=$('#deletePropertyNode');if(del)del.onclick=async()=>{if(!(await afConfirm({title:'删除步骤',message:`确定删除步骤「${n.name}」？关联连线将被一并移除。`,danger:true,confirmText:'删除步骤'})))return;state.workflow.nodes=state.workflow.nodes.filter(x=>x.id!==n.id);state.workflow.edges=state.workflow.edges.filter(e=>e.source!==n.id&&e.target!==n.id);state.workflow.nodes.forEach(x=>{if(x.rejectTo===n.id){x.rejectTo='';x.onFailure='fail'}});state.selectedNode=null;dirty();renderCanvas();renderProperties()};
  const openScriptBtn=$('#openScriptNodeEditor');
  const previewScriptBtn=$('#previewScriptNodeEditor');
  const triggerScriptEditor=()=>{
    const lang=n.scriptLanguage==='python'?'python':'shell';
    openCodeEditorModal({
      title:`步骤脚本 · ${n.name||'脚本节点'}`,
      subtitle:`节点执行脚本 (${lang==='python'?'Python 3':'Shell'})`,
      language:lang,
      value:n.script||'',
      onSave:(newVal, newLang)=>{
        n.script=newVal;
        if(newLang==='python'||newLang==='shell'){
          n.scriptLanguage=newLang;
          const langSel=host.querySelector('[data-p="scriptLanguage"]');
          if(langSel) langSel.value=newLang;
        }
        renderProperties();
        dirty();
      }
    });
  };
  if(openScriptBtn) openScriptBtn.onclick=triggerScriptEditor;
  if(previewScriptBtn) previewScriptBtn.onclick=triggerScriptEditor;
}

function dirty(){state.workflowDirty=true;$('#saveState').textContent='有未保存更改';$('#saveState').style.color='#e59a24';const btn=$('#discardWorkflow');if(btn)btn.hidden=false}
function markWorkflowClean(){state.workflowDirty=false;state.workflowSaved=clone(state.workflow);$('#saveState').textContent='已同步';$('#saveState').style.color='';const btn=$('#discardWorkflow');if(btn)btn.hidden=true;rememberOpenWorkflow()}
async function discardWorkflow(){
  if(!state.workflowDirty||!state.workflowSaved)return;
  if(!(await afConfirm({title:'放弃修改',message:'放弃未保存的修改，恢复到上次保存？',danger:true,confirmText:'放弃修改'})))return;
  state.workflow=clone(state.workflowSaved);state.selectedNode=null;state.connectFrom=null;state.connectCursor=null;markWorkflowClean();renderAll();toast('已取消修改');
}
async function saveWorkflow(){
  if(catalogGeneric(state.workflow)&&!isPlatformAdmin()){toast('通用模板为只读，请先「复制到本项目」再修改');throw new Error('generic sop is read-only')}
  if(catalogForeign(state.workflow?.projectId)){toast('这是其他项目的共享 SOP，请先复制到本项目再改');throw new Error('shared sop is read-only')}
  try{const saved=await api('/api/workflows',{method:'PUT',body:JSON.stringify(state.workflow)});state.workflow=clone(saved);const i=state.data.workflows.findIndex(w=>w.id===saved.id);if(i>=0)state.data.workflows[i]=clone(saved);else state.data.workflows.unshift(clone(saved));markWorkflowClean();renderWorkflowMeta();renderSOPCatalog();toast('SOP 草稿已保存');return saved}catch(e){if(e.message.includes('revision conflict')){toast('保存冲突：其他人已更新该 SOP，请重新选择后合并')}else if(e.message!=='shared sop is read-only'&&e.message!=='generic sop is read-only')toast(e.message);throw e}}
function bindFlowResize(){
  const workbench=$('.flow-workbench');if(!workbench)return;
  const ow=localStorage.getItem('agentflow.flowOutlineW'),pw=localStorage.getItem('agentflow.flowPropsW');
  if(ow)workbench.style.setProperty('--flow-outline-w',ow);
  if(pw)workbench.style.setProperty('--flow-props-w',pw);
  $$('.flow-resize').forEach(handle=>{
    handle.onpointerdown=e=>{
      const key=handle.dataset.resize,startX=e.clientX;
      const startOutline=parseFloat(getComputedStyle(workbench).getPropertyValue('--flow-outline-w'))||240;
      const startProps=parseFloat(getComputedStyle(workbench).getPropertyValue('--flow-props-w'))||360;
      handle.setPointerCapture(e.pointerId);handle.classList.add('dragging');
      handle.onpointermove=ev=>{
        const dx=ev.clientX-startX;
        if(key==='outline')workbench.style.setProperty('--flow-outline-w',Math.min(420,Math.max(180,startOutline+dx))+'px');
        else workbench.style.setProperty('--flow-props-w',Math.min(560,Math.max(280,startProps-dx))+'px');
      };
      handle.onpointerup=()=>{
        handle.classList.remove('dragging');handle.onpointermove=null;
        localStorage.setItem('agentflow.flowOutlineW',getComputedStyle(workbench).getPropertyValue('--flow-outline-w').trim());
        localStorage.setItem('agentflow.flowPropsW',getComputedStyle(workbench).getPropertyValue('--flow-props-w').trim());
      };
    };
  });
}
function newWorkflow(){state.workflow={id:uid('sop'),name:'未命名 SOP',version:1,revision:0,status:'draft',projectId:state.projectFilter||'',shared:false,owner:'',tags:[],description:'',sandboxProfileId:'local',nodes:[],edges:[]};state.selectedNode=null;rememberOpenWorkflow();renderAll();dirty()}
function openSOPWizard(){
  const f=$('#sopCreateForm');f.reset();
  const generic=(state.data.workflows||[]).filter(w=>catalogGeneric(w));
  const recommended=(state.data.workflows||[]).filter(w=>!catalogGeneric(w)&&['issue-fix-production','requirement-development-production'].includes(w.id));
  const fallback=(state.data.workflows||[]).find(w=>!catalogGeneric(w)&&w.status==='published');
  const choices=[...generic,...recommended,...(fallback&&!recommended.some(w=>w.id===fallback.id)?[fallback]:[])];
  state.sopTemplate=choices[0]?.id||'blank';
  $('#sopTemplateChoices').innerHTML=choices.map((w,index)=>`<label class="template-choice ${index===0?'active':''}"><input type="radio" name="template" value="${escapeHTML(w.id)}" ${index===0?'checked':''}><span class="template-icon">${catalogGeneric(w)?'通':w.id.includes('issue')?'修':w.id.includes('requirement')?'需':'▤'}</span><div><b>${escapeHTML(w.name)}</b><p>${escapeHTML(w.description||'复制生产模板')}</p><small>${catalogGeneric(w)?'通用 · 所有项目可用 · ':''}推荐 · ${w.nodes?.length||0} 个节点</small></div></label>`).join('')+`<label class="template-choice ${choices.length?'':'active'}"><input type="radio" name="template" value="blank" ${choices.length?'':'checked'}><span class="template-icon blank">＋</span><div><b>从空白开始</b><p>仅适合需要完全自定义的流程</p><small>高级方式 · 0 个节点</small></div></label>`;
  $('#sopTemplateChoices').querySelectorAll('input').forEach(input=>input.onchange=()=>{$$('#sopTemplateChoices .template-choice').forEach(x=>x.classList.toggle('active',x.contains(input)));state.sopTemplate=input.value});
  setSOPWizardStep(1);$('#sopCreateModal').classList.add('open');setTimeout(()=>f.elements.name.focus(),0)}
function setSOPWizardStep(step){const f=$('#sopCreateForm');if(step>1&&state.sopWizardStep===1&&!f.elements.name.value.trim())return toast('先填写一个便于团队识别的流程名称');state.sopWizardStep=Math.max(1,Math.min(3,step));f.querySelectorAll('.wizard-pane').forEach(p=>p.classList.toggle('active',Number(p.dataset.step)===state.sopWizardStep));f.querySelectorAll('[data-step-target]').forEach(b=>b.classList.toggle('active',Number(b.dataset.stepTarget)<=state.sopWizardStep));$('#sopPrev').disabled=state.sopWizardStep===1;$('#sopNext').hidden=state.sopWizardStep===3;$('#sopNext').textContent=state.sopWizardStep===1?'选择流程模板':'查看创建摘要';$('#sopFinish').hidden=state.sopWizardStep===1;$('#sopFinish').textContent=state.sopWizardStep===2?'使用此模板创建':'创建并进入设计器';$('#sopStepHint').textContent=state.sopWizardStep===1?'填写基本信息即可开始':state.sopWizardStep===2?'选择最接近的模板，后续仍可修改':'确认后进入可视化设计器';if(state.sopWizardStep===3){const template=state.data.workflows.find(w=>w.id===state.sopTemplate);$('#sopCreateSummary').innerHTML=`<div><span>流程名称</span><b>${escapeHTML(f.elements.name.value)}</b></div><div><span>负责人</span><b>${escapeHTML(f.elements.owner.value||'暂未指定')}</b></div><div><span>创建方式</span><b>${template?`基于「${escapeHTML(template.name)}」`:'空白流程'}</b></div><div><span>初始结构</span><b>${template?.nodes?.length||0} 个节点 · ${template?.edges?.length||0} 条依赖</b></div>`}}
async function createSOPFromWizard(e){
  e.preventDefault();
  const f=e.currentTarget,template=(state.data.workflows||[]).find(w=>w.id===state.sopTemplate);
  const name=f.elements.name.value.trim(),description=f.elements.description.value.trim(),owner=f.elements.owner.value.trim(),tags=f.elements.tags.value.split(',').map(x=>x.trim()).filter(Boolean);
  try{
    let saved;
    if(catalogGeneric(template)){
      // A generic template brings its own roles, so it must be adopted through
      // the clone endpoint instead of being written as a bare workflow.
      if(!state.projectFilter)return toast('请先在顶部选择一个项目');
      saved=await api(`/api/workflows/${encodeURIComponent(template.id)}/clone`,{method:'POST',body:JSON.stringify({id:uid('sop'),name,owner,projectId:state.projectFilter})});
      if(description||owner||tags.length)saved=await api('/api/workflows',{method:'PUT',body:JSON.stringify({...saved,name,description:description||saved.description,owner:owner||saved.owner,tags:tags.length?tags:saved.tags})});
    }else{
      const workflow=template?clone(template):{nodes:[],edges:[]};
      Object.assign(workflow,{id:uid('sop'),name,description,owner,tags,status:'draft',version:1,revision:0,projectId:state.projectFilter||'',shared:false});
      workflow.sandboxProfileId=workflow.sandboxProfileId||'local';
      delete workflow.createdAt;delete workflow.updatedAt;
      saved=await api('/api/workflows',{method:'PUT',body:JSON.stringify(workflow)});
    }
    state.data.workflows.unshift(saved);state.workflow=clone(saved);state.selectedNode=null;markWorkflowClean();
    $('#sopCreateModal').classList.remove('open');renderAll();switchView('flow');
    const missing=catalogGeneric(template)?genericRequirementRows(saved).filter(r=>!r.ok):[];
    toast(missing.length?`SOP 草稿已创建；${missing.length} 项运行依赖未配置（${missing.map(r=>r.label).join('、')}）`:'SOP 草稿已创建，请继续设计节点',missing.length?'':'ok')
  }catch(err){toast(err.message)}}
async function cloneWorkflow(id){const src=id?(state.data.workflows||[]).find(w=>w.id===id):state.workflow;if(!src?.id)return toast('先选择一条 SOP');const name=await afPrompt({title:'复制 SOP',message:'请输入复制后的 SOP 名称：',defaultValue:`${src.name} - 副本`});if(!name)return;const owner=await afPrompt({title:'SOP 负责人',message:'请输入负责人（可留空）：',defaultValue:src.owner||''})??'';try{const copied=await api(`/api/workflows/${encodeURIComponent(src.id)}/clone`,{method:'POST',body:JSON.stringify({id:uid('sop'),name,owner,projectId:state.projectFilter||''})});state.data.workflows.unshift(copied);state.workflow=clone(copied);state.selectedNode=null;state.sopScope='project';markWorkflowClean();renderAll();toast('已复制到本项目')}catch(e){toast(e.message)}}

async function deleteWorkflow(id){
  const w=(state.data.workflows||[]).find(x=>x.id===id);
  if(!w)return toast('SOP 不存在或已删除');
  if(!await afConfirm({title:'删除 SOP 确认',message:`确认删除 SOP「${w.name}」？此操作不可恢复，关联的运行记录会保留但流程定义将移除。`,confirmText:'确认删除',isDanger:true}))return;
  try{
    await api(`/api/workflows/${encodeURIComponent(id)}`,{method:'DELETE'});
    state.data.workflows=state.data.workflows.filter(x=>x.id!==id);
    if(state.workflow?.id===id){state.workflow=null;state.selectedNode=null;markWorkflowClean()}
    renderSOPCatalog();renderWorkflowMeta();
    toast('SOP 已删除')
  }catch(e){toast(e.message||'删除失败')}}
async function publishWorkflow(){
  if(catalogGeneric(state.workflow)&&!isPlatformAdmin())return toast('通用模板为只读，请先「复制到本项目」再发布');
  try{await saveWorkflow();const published=await api(`/api/workflows/${state.workflow.id}/publish`,{method:'POST',body:'{}'});state.workflow=clone(published);const i=state.data.workflows.findIndex(w=>w.id===published.id);if(i>=0)state.data.workflows[i]=clone(published);markWorkflowClean();renderWorkflowMeta();toast(`已发布 v${published.version}`)}catch(e){if(!e.message.includes('revision conflict'))toast(e.message)}}
