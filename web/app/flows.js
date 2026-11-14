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
  {id:'gpt-6-sol',name:'GPT-6-Sol'},
  {id:'claude-sonnet-4.6',name:'Claude-Sonnet-4.6'},
  {id:'kimi-k3-ioa',name:'Kimi-K3'},
  {id:'deepseek-v4.1-flash',name:'DeepSeek-V4.1-Flash'}
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
  // The preview build node runs on CodeBuddy (executorId codebuddy).
  const models=api?.codeBuddyModels?.()||api?.catalog?.()?.models;
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
function flowReadOnly(wf=state.workflow){return !wf||catalogForeign(wf.projectId)||(catalogGeneric(wf)&&!isPlatformAdmin())}
function flowReadOnlyReason(wf=state.workflow){
  if(!wf)return '先选择或新建一条 SOP';
  return catalogGeneric(wf)?'通用模板为只读，请先「复制到本项目」再修改':'这是其他项目的共享 SOP，请先复制到本项目再改';
}
function flowStatusPill(wf){
  if(catalogGeneric(wf))return isPlatformAdmin()?{cls:'draft',label:'通用模板 · 可编辑'}:{cls:'ro',label:'只读'};
  if(catalogForeign(wf.projectId))return {cls:'ro',label:'只读'};
  if(wf.status==='published')return {cls:'ok',label:'已发布'};
  if(wf.status==='archived')return {cls:'ro',label:'已归档'};
  return {cls:'draft',label:'草稿'};
}
let workflowPublishing=false;
function workflowNeedsPublish(wf=state.workflow){
  return !!wf&&(state.workflowDirty||wf.status!=='published'||(wf.publishedRevision||wf.revision)!==wf.revision);
}
function renderFlowHeader(){
  const wf=state.workflow;
  const title=$('#flowCommandTitle');
  if(title){title.textContent=wf?(wf.name||'未命名流程'):'选择或新建 SOP';title.title=wf?.name||''}
  const copyName=$('#copyWorkflowName');if(copyName)copyName.hidden=!wf
  const meta=$('#workflowMeta');
  if(meta){
    if(!wf)meta.innerHTML='<span>还没有打开流程</span>';
    else{const p=flowStatusPill(wf);meta.innerHTML=`<span class="fd-pill ${p.cls}"><span class="dot"></span>${p.label}</span><span>v${escapeHTML(wf.version||1)}</span><span class="fd-dotsep"></span><span class="v-step">${(wf.nodes||[]).length} 步</span><span class="fd-dotsep v-step-sep"></span>`}
  }
  const chip=$('#sandboxChip');
  if(chip){
    chip.hidden=!wf;
    const sb=(state.data?.sandboxes||[]).find(s=>s.id===wf?.sandboxProfileId),label=$('#sandboxChipLabel');
    if(label)label.textContent=sb?.name||wf?.sandboxProfileId||'未设置沙箱';
  }
  const ro=flowReadOnly(wf),pending=!!(wf&&state.workflowDirty&&!ro);
  const save=$('#saveWorkflow');
  if(save){
    save.disabled=!pending;save.classList.toggle('dirty',pending);
    save.dataset.tip=!wf?'先选择一条 SOP':ro?flowReadOnlyReason(wf):pending?'有未保存的修改':'所有修改已保存';
  }
  const label=$('#saveStateLabel');if(label)label.textContent=pending?'保存':'已保存';
  const top=$('#saveState');if(top){top.textContent=state.workflowDirty?'有未保存更改':'已同步';top.style.color=state.workflowDirty?'#e59a24':''}
  const publish=$('#publishWorkflow');
  if(publish){publish.disabled=ro||workflowPublishing||!workflowNeedsPublish(wf);publish.dataset.tip=workflowPublishing?'正在发布':!workflowNeedsPublish(wf)?'当前内容已发布，修改后可发布新版本':'发布为新版本';publish.setAttribute('aria-busy',String(workflowPublishing))}
  const quickSave=$('#flowSettingsQuickSave');if(quickSave)quickSave.disabled=ro;
  const discard=$('#discardWorkflow');if(discard)discard.hidden=!(wf&&state.workflowDirty);
  $('#flowView')?.classList.toggle('fd-readonly',!!wf&&ro);
  if(typeof flowPalette!=='undefined'&&flowPalette.isSwitcherOpen())flowPalette.renderSwitcher();
}
function renderWorkflowMeta(){
  const workflows=catalogWorkflows();
  const wf=state.workflow;
  // Generic templates and an unsaved AI draft are not in the project SOP list.
  // Keep the open workflow selected while it is being previewed.
  const options=wf&&!workflows.some(w=>w.id===wf.id)?[wf,...workflows]:workflows;
  $('#workflowSelect').innerHTML=options.map(w=>`<option value="${escapeHTML(w.id)}" ${wf&&w.id===wf.id?'selected':''}>${escapeHTML(w.name)}</option>`).join('');
  if(!wf){
    $('#workflowName').value='';$('#workflowDescription').value='';$('#workflowOwner').value='';$('#workflowSandbox').innerHTML='';
    $('#workflowStatus').value='draft';$('#workflowTags').value='';$('#workflowMetricCategory').value='';$('#workflowSOPLane').value='';
    const pill=$('#flowSettingsPill');if(pill)pill.textContent='未选择流程';
    renderFlowHeader();
    if(typeof flowPalette!=='undefined')flowPalette.render();
    return;
  }
  normalizeWorkflowNotifications(wf);
 const notificationHost=$('#workflowNotificationEditor');if(notificationHost)mountNotificationEditor(notificationHost,wf.notifications,{sourceType:'run',kind:'sop',projectId:wf.projectId||state.projectFilter},()=>dirty());
 $('#workflowName').value=wf.name;$('#workflowDescription').value=wf.description||'';
  $('#workflowOwner').value=wf.owner||'';$('#workflowStatus').value=wf.status||'draft';$('#workflowTags').value=(wf.tags||[]).join(', ');$('#workflowMetricCategory').value=wf.metricCategory||'';$('#workflowSOPLane').value=wf.sopLane||'';
  const previewOn=!!wf.preview?.enabled, hasPreviewNode=(wf.nodes||[]).some(n=>isPreviewBuildNode(n.id));
  if(previewOn!==hasPreviewNode)syncPreviewBuildGraph();
  $('#workflowSandbox').innerHTML=(state.data.sandboxes||[]).filter(s=>catalogVisible(s.projectId,s.shared)).map(s=>`<option value="${escapeHTML(s.id)}" ${s.id===wf.sandboxProfileId?'selected':''}>${escapeHTML(s.name)}${catalogForeign(s.projectId)?' · 共享':''}</option>`).join('');
  const pill=$('#flowSettingsPill');if(pill)pill.textContent=catalogGeneric(wf)?(isPlatformAdmin()?'通用模板 · 可编辑':'通用模板'):`${wf.status==='published'?'已发布':'草稿'} · v${wf.version} · ${wf.nodes.length} 步`;
  renderFlowHeader();
  if(typeof flowPalette!=='undefined')flowPalette.render();
}
async function switchFlowWorkflow(id){
  if(!id||id===state.workflow?.id)return false;
  const next=(state.data?.workflows||[]).find(w=>w.id===id);
  if(!next){toast('SOP 不存在或已删除');return false}
  if(state.workflowDirty&&!(await afConfirm({title:'放弃修改并切换流程？',message:'当前流程有未保存修改。切换后将丢弃这些修改。',danger:true,confirmText:'放弃修改并切换',cancelText:'继续编辑'}))){renderWorkflowMeta();return false}
  state.workflow=clone(next);state.selectedNode=null;state.connectFrom=null;state.connectCursor=null;state.renamingNode=null;
  markWorkflowClean();flowHistory.clear();rememberOpenWorkflow();renderAll();refreshGenericWorkflowRoles();
  return true;
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
function addNode(type,opts={}){
  if(!state.workflow)return toast('流程尚未加载');
  if(flowReadOnly())return toast(flowReadOnlyReason());
  const at=opts.at?{x:Math.round(opts.at.x/10)*10,y:Math.round(opts.at.y/10)*10}:flowViewport.freeSpot();
  if(type==='preview'){
    const existing=state.workflow.nodes.find(n=>isPreviewBuildNode(n.id));
    if(existing){state.selectedNode=existing.id;renderCanvas();renderProperties();return toast('预览节点已存在，可拖拽连线调整它的位置')}
    flowHistory.record();
    ensurePreviewContract();
    state.workflow.nodes.push({id:PREVIEW_BUILD_NODE_ID,name:'构建静态站点',type:'agent',agentId:PREVIEW_BUILD_AGENT_ID,executorId:'codebuddy',executionScope:'run',onFailure:'fail',retryStrategy:'targeted_fix',maxAttempts:2,maxReworks:1,position:at,artifactOutputs:[{name:'frontend-preview',path:'dist/index.html',required:true}]});
    state.selectedNode=PREVIEW_BUILD_NODE_ID;
    dirty();renderCanvas();renderProperties();
    return;
  }
  flowHistory.record();
  const labels={agent:'新 Agent 节点',script:'脚本节点',approval:'人工门禁',sop:'SOP 节点'};
  const firstSOP=type==='sop'?sopWorkflowChoices(state.workflow.id)[0]:null;
  const agentId=type==='agent'?(opts.agentId||workflowAgents()[0]?.id||''):'';
  const agent=agentId?workflowAgent(agentId):null;
  const node={id:uid(type),name:firstSOP?.name||(opts.agentId&&agent?.name)||labels[type]||'新节点',type,agentId,sopWorkflowId:firstSOP?.id||'',scriptLanguage:type==='script'?'shell':'',script:type==='script'?"printf 'passed\\n'":'',executionScope:'run',artifactOutputs:firstSOP?sopDeclaredArtifacts(firstSOP):[],rejectTo:'',onFailure:'fail',retryStrategy:'targeted_fix',carryOver:['failed_checks','last_error','upstream_artifacts'],maxAttempts:1,maxReworks:3,position:at};
  state.workflow.nodes.push(node);state.selectedNode=node.id;
  dirty();renderCanvas();renderProperties();
}
// Removes a node with its edges and any reject routes pointing at it.
function removeNode(id){
  const wf=state.workflow,n=wf?.nodes.find(x=>x.id===id);
  if(!n)return false;
  if(flowReadOnly()){toast(flowReadOnlyReason());return false}
  flowHistory.record();
  wf.nodes=wf.nodes.filter(x=>x.id!==id);
  wf.edges=wf.edges.filter(e=>e.source!==id&&e.target!==id);
  wf.nodes.forEach(x=>{if(x.rejectTo===id){x.rejectTo='';x.onFailure='fail'}});
  if(isPreviewBuildNode(id)&&wf.preview)delete wf.preview;
  if(state.selectedNode===id)state.selectedNode=null;
  if(state.renamingNode===id)state.renamingNode=null;
  dirty();renderCanvas();renderProperties();
  flowToast(`已删除「${n.name}」`,{undo:true});
  return true;
}
function deleteSelected(){if(state.selectedNode)removeNode(state.selectedNode)}
function duplicateNode(id){
  const wf=state.workflow,n=wf?.nodes.find(x=>x.id===id);
  if(!n||isPreviewBuildNode(id))return null;
  if(flowReadOnly()){toast(flowReadOnlyReason());return null}
  flowHistory.record();
  const copy=clone(n);
  Object.assign(copy,{id:uid(n.type||'node'),name:`${n.name} 副本`,position:{x:(n.position?.x||0)+40,y:(n.position?.y||0)+40},rejectTo:''});
  if(copy.onFailure==='reject')copy.onFailure='fail';
  wf.nodes.push(copy);state.selectedNode=copy.id;
  dirty();renderCanvas();renderProperties();
  flowToast('已复制（不含连线）',{undo:true});
  return copy;
}
function arrange(){
  if(!state.workflow)return;
  if(flowReadOnly())return toast(flowReadOnlyReason());
  flowHistory.record();
  const stage=$('#canvasStage');stage?.classList.add('anim','arranging');
  workflowStages(state.workflow).forEach((column,x)=>column.forEach((node,y)=>{
    node.position={x:80+x*320,y:80+y*190};
    const el=flowNodeEl(node.id);if(el){el.style.left=node.position.x+'px';el.style.top=node.position.y+'px'}
  }));
  dirty();
  setTimeout(()=>{stage?.classList.remove('arranging');renderCanvas();flowViewport.fit()},380);
  flowToast('已按依赖层级整理画布',{undo:true});
}
// Layouts saved for the narrower legacy node, or templates on a 240px column
// pitch, overlap the 244px node: the overlap hides the ports, so no reject
// route can be drawn. Rows/columns that are that crowded are widened
// proportionally in memory; a later save keeps it. Diagonal or far-off
// overlaps are left to the user (and "整理").
const FLOW_SPREAD={w:244,h:132,pitchW:280,pitchH:160,maxFactor:1.6};
function spreadCrowdedNodes(wf){
  const nodes=(wf?.nodes||[]).filter(n=>n.position&&Number.isFinite(n.position.x)&&Number.isFinite(n.position.y));
  let minDx=Infinity,minDy=Infinity;
  for(let i=0;i<nodes.length;i++)for(let j=i+1;j<nodes.length;j++){
    const dx=Math.abs(nodes[i].position.x-nodes[j].position.x),dy=Math.abs(nodes[i].position.y-nodes[j].position.y);
    if(dy<40&&dx>=1&&dx<FLOW_SPREAD.w+16)minDx=Math.min(minDx,dx);
    else if(dx<40&&dy>=1&&dy<FLOW_SPREAD.h+8)minDy=Math.min(minDy,dy);
  }
  const fx=minDx<Infinity?FLOW_SPREAD.pitchW/minDx:1,fy=minDy<Infinity?FLOW_SPREAD.pitchH/minDy:1;
  const sx=fx>1&&fx<=FLOW_SPREAD.maxFactor,sy=fy>1&&fy<=FLOW_SPREAD.maxFactor;
  if(!sx&&!sy)return false;
  const x0=Math.min(...nodes.map(n=>n.position.x)),y0=Math.min(...nodes.map(n=>n.position.y));
  nodes.forEach(n=>{n.position={x:sx?x0+Math.ceil((n.position.x-x0)*fx/10)*10:n.position.x,y:sy?y0+Math.ceil((n.position.y-y0)*fy/10)*10:n.position.y}});
  return true;
}

const FLOW_NODE_TYPES={agent:{icon:'bot',label:'Agent 步骤'},script:{icon:'terminal',label:'脚本步骤'},sop:{icon:'layers',label:'子 SOP'},preview:{icon:'eye',label:'预览构建'},approval:{icon:'shield',label:'人工门禁'}};
function flowNodeKind(n){return isPreviewBuildNode(n.id)?'preview':(FLOW_NODE_TYPES[n.type]?n.type:'approval')}
function flowNodeTint(n){
  const kind=flowNodeKind(n);
  if(kind==='agent'&&n.agentId)return flowPalette.roleTint(n.agentId);
  return {agent:'79 107 255',script:'139 92 246',sop:'14 165 233',preview:'217 119 6',approval:'16 163 127'}[kind];
}
function flowNodeEl(id){return document.querySelector(`#nodes .flow-node[data-id="${CSS.escape(id)}"]`)}
function nodeMeta(n){
  if(isPreviewBuildNode(n.id))return {kind:'preview',subtitle:'CodeBuddy 静态站点构建',tags:[{text:workflowPreviewModel(state.workflow)}]};
  if(n.type==='agent'){
    const a=workflowAgent(n.agentId),generic=catalogGeneric(state.workflow);
    const e=(state.data.executors||[]).find(x=>x.id===(a?.executorId||n.executorId));
    const subtitle=a?.name||(n.agentId?(generic?n.agentId:'角色不可用'):'未绑定角色');
    const tags=[];
    if(!n.agentId)tags.push({text:'未绑定角色',warn:true});
    else if(!a)tags.push({text:generic?'通用模板角色':'角色不可用',warn:!generic});
    else{
      const model=a.model||e?.model||e?.name||'';
      tags.push(model?{text:model}:{text:'未选模型',warn:!generic});
      tags.push({text:`${effectiveNodeArtifacts(n,a).length} 产物`});
      tags.push({text:a.loop?.enabled?`${a.loop.maxIterations||1} 轮 Loop`:'单轮'});
    }
    return {kind:'agent',subtitle,tags};
  }
  if(n.type==='script'){
    const line=(n.script||'').trim().split('\n')[0];
    return {kind:'script',subtitle:n.scriptLanguage==='python'?'Python 脚本':'Shell 脚本',tags:[line?{text:line,mono:true}:{text:'未配置脚本',warn:true}]};
  }
  if(n.type==='sop'){
    const wf=sopWorkflowChoices().find(w=>w.id===n.sopWorkflowId)||(state.data.workflows||[]).find(w=>w.id===n.sopWorkflowId);
    const arts=(n.artifactOutputs||[]).map(a=>a.name).filter(Boolean);
    const tags=!n.sopWorkflowId?[{text:'未选择流程',warn:true}]:!wf?[{text:'流程不可用',warn:true}]:[{text:arts.length?`交给下一步：${arts.join('、')}`:'产物交给下一步'}];
    return {kind:'sop',subtitle:wf?.name||(n.sopWorkflowId?'流程不可用':'未选择流程'),tags};
  }
  return {kind:'approval',subtitle:'人工门禁',tags:[{text:'等待人工确认'}]};
}
function flowNodeHTML(n,run,ro){
  const m=nodeMeta(n),kind=m.kind,status=run?.nodes?.[n.id]?.status||'';
  const agent=kind==='agent'?workflowAgent(n.agentId):null;
  const tile=agent?escapeHTML(flowPalette.initial(agent.name)):flowPalette.icon(FLOW_NODE_TYPES[kind].icon,16);
  const title=state.renamingNode===n.id?`<input class="fd-rename" value="${escapeHTML(n.name)}" maxlength="120" aria-label="重命名步骤">`:escapeHTML(n.name);
  const cls=['flow-node','fd-node','t-'+kind,state.selectedNode===n.id?'sel':'',state.connectFrom===n.id?'connecting':'',status?'s-'+status:'',kind==='preview'?'preview-build':''].filter(Boolean).join(' ');
  const tags=m.tags.map(t=>`<span class="fd-tag${t.warn?' warn':''}${t.mono?' mono':''}" title="${escapeHTML(t.text)}">${escapeHTML(t.text)}</span>`).join('');
  const outs=`<div class="fd-out ok">成功<span class="fd-port out" data-outlet="success" title="拖到下一步"></span></div>`+(kind==='preview'?'':`<div class="fd-out rj">失败打回<span class="fd-port out" data-outlet="reject" title="拖到失败时要打回的步骤"></span></div>`);
  return `<div class="${cls}" data-id="${escapeHTML(n.id)}" style="left:${n.position.x}px;top:${n.position.y}px;--tint:${flowNodeTint(n)}"><span class="fd-port in" title="连到这里"></span><div class="fd-nh"><span class="fd-tile">${tile}</span><div class="fd-nt"><div class="node-title">${title}</div><div class="node-subtitle">${escapeHTML(m.subtitle)}</div></div>${status?`<span class="fd-status s-${escapeHTML(status)}">${statusText(status)||escapeHTML(status)}</span>`:''}${ro?'':`<button type="button" class="fd-more" data-node-menu aria-label="更多操作">${flowPalette.icon('more')}</button>`}</div><div class="fd-nbody">${tags}</div><div class="fd-outs">${outs}</div></div>`;
}
function renderCanvas(){
  const nodes=$('#nodes');if(!nodes)return;
  const wf=state.workflow,empty=$('#canvasEmpty');
  flowNodeMetrics.cache?.clear();
  if(!wf){nodes.innerHTML='';if(empty)empty.hidden=false;renderEdges();flowViewport.afterRender();return}
  if(empty)empty.hidden=(wf.nodes||[]).length>0;
  spreadCrowdedNodes(wf);
  const ro=flowReadOnly();
  nodes.innerHTML=wf.nodes.map(n=>flowNodeHTML(n,state.activeRun,ro)).join('');
  nodes.querySelectorAll('.flow-node').forEach(bindFlowNode);
  bindNodeRename(nodes.querySelector('.fd-rename'));
  renderEdges();
  flowViewport.afterRender();
}
function bindFlowNode(el){
  const n=state.workflow.nodes.find(x=>x.id===el.dataset.id);if(!n)return;
  el.addEventListener('pointerdown',e=>{
    if(e.button!==0||e.target.closest('.fd-port,.fd-more,input'))return;
    e.stopPropagation();closeNodeMenu();
    if(state.connectFrom&&state.connectFrom!==n.id){completeFlowConnect(n);return}
    const sx=e.clientX,sy=e.clientY,start={x:n.position.x,y:n.position.y},ro=flowReadOnly();
    let moved=false;
    el.setPointerCapture(e.pointerId);
    const move=ev=>{
      if(ro||(!moved&&Math.hypot(ev.clientX-sx,ev.clientY-sy)<4))return;
      if(!moved){flowHistory.record();moved=true;el.classList.add('dragging')}
      const z=flowViewport.zoom;
      n.position={x:Math.round((start.x+(ev.clientX-sx)/z)/10)*10,y:Math.round((start.y+(ev.clientY-sy)/z)/10)*10};
      el.style.left=n.position.x+'px';el.style.top=n.position.y+'px';
      scheduleFlowEdges();
    };
    const up=()=>{
      el.removeEventListener('pointermove',move);el.removeEventListener('pointerup',up);el.removeEventListener('pointercancel',up);
      el.classList.remove('dragging');
      if(moved){renderEdges();dirty()}else openNodeDrawer(n.id);
    };
    el.addEventListener('pointermove',move);el.addEventListener('pointerup',up);el.addEventListener('pointercancel',up);
  });
  el.addEventListener('dblclick',e=>{if(!e.target.closest('input'))openNodeDrawer(n.id)});
  el.querySelector('[data-node-menu]')?.addEventListener('click',e=>{e.stopPropagation();openNodeMenu(n.id,e.currentTarget)});
  el.querySelectorAll('.fd-port.out').forEach(port=>port.addEventListener('pointerdown',e=>startFlowConnect(e,n,port.dataset.outlet)));
  el.querySelector('.fd-port.in')?.addEventListener('pointerdown',e=>{if(state.connectFrom&&state.connectFrom!==n.id){e.stopPropagation();completeFlowConnect(n)}});
}
function startNodeRename(id){
  if(flowReadOnly()||!state.workflow?.nodes.some(n=>n.id===id))return;
  state.renamingNode=id;renderCanvas();
}
function bindNodeRename(input){
  if(!input)return;
  const id=state.renamingNode;
  input.focus();input.select();
  const done=commit=>{
    if(state.renamingNode!==id)return;
    state.renamingNode=null;
    const n=state.workflow?.nodes.find(x=>x.id===id),name=input.value.trim();
    if(commit&&n&&name&&name!==n.name){flowHistory.record();n.name=name;dirty()}
    renderCanvas();if(state.selectedNode===id)renderProperties();
  };
  input.addEventListener('keydown',e=>{e.stopPropagation();if(e.key==='Enter'){e.preventDefault();done(true)}else if(e.key==='Escape'){e.preventDefault();done(false)}});
  input.addEventListener('blur',()=>done(true));
  input.addEventListener('pointerdown',e=>e.stopPropagation());
}

function flowDiagnostics(){
  if(!state.workflow)return [];
  const nodes=state.workflow.nodes||[],edges=state.workflow.edges||[],incoming=new Map(nodes.map(n=>[n.id,0])),items=[];edges.forEach(e=>incoming.has(e.target)&&incoming.set(e.target,incoming.get(e.target)+1));
  const starts=nodes.filter(n=>incoming.get(n.id)===0);items.push({level:!nodes.length?'error':starts.length===1?'ok':'warn',title:'流程入口',detail:!nodes.length?'至少添加一个节点':starts.length===1?`从「${starts[0].name}」开始`:`检测到 ${starts.length} 个入口；这些节点会并行启动`});
  items.push({level:state.data.sandboxes?.some(s=>s.id===state.workflow.sandboxProfileId)?'ok':'error',title:'运行沙箱',detail:state.workflow.sandboxProfileId?`整条流程使用 ${state.workflow.sandboxProfileId}`:'尚未选择 Workflow 运行沙箱'});
  nodes.forEach(n=>{if(isPreviewBuildNode(n.id)){const inDeg=edges.filter(e=>e.target===n.id).length;items.push({level:inDeg?'ok':'warn',title:n.name,detail:inDeg?`插在流程中间：CodeBuddy ${workflowPreviewModel(state.workflow)} 构建静态站点，运行成功后归档预览`:'还没有前序依赖，会作为流程入口直接运行；请从上游节点连线到此'});return}if(n.type==='agent'){const missing=[];const agent=workflowAgent(n.agentId);const generic=catalogGeneric(state.workflow);if(!n.agentId)missing.push('角色');else if(!agent)missing.push(generic?'通用模板角色未加载':'角色不属于当前项目或未共享');else if(!generic&&!(agent.executorId||n.executorId))missing.push('角色未选模型');if(!(n.artifactOutputs||[]).length&&!(agent?.artifactOutputs||[]).length)missing.push('角色产物契约');items.push({level:missing.length?'warn':'ok',title:n.name,detail:missing.length?`尚未配置：${missing.join('、')}`:generic?'已绑定通用模板角色；任务书与门禁在角色定义中':'已绑定角色；任务书与门禁在角色定义中'})}if(n.type==='sop'){const missing=[];const wf=sopWorkflowChoices(state.workflow.id).find(w=>w.id===n.sopWorkflowId)||(state.data.workflows||[]).find(w=>w.id===n.sopWorkflowId);if(!n.sopWorkflowId)missing.push('要跑的 SOP');else if(!wf)missing.push('引用的 SOP 不存在');if(n.sopWorkflowId===state.workflow.id)missing.push('不能引用自己');items.push({level:missing.length?'error':'ok',title:n.name,detail:missing.length?`尚未配置：${missing.join('、')}`:`引用「${wf.name}」，产物交给下一步`})}if(n.rejectTo&&!nodes.some(x=>x.id===n.rejectTo))items.push({level:'error',title:n.name,detail:'失败打回目标不存在'})});
  if(nodes.length>1&&edges.length===0)items.push({level:'warn',title:'成功路径',detail:'多个节点之间没有依赖关系，将同时启动'});if(!items.some(x=>x.level==='error'))items.push({level:'ok',title:'结构校验',detail:'未发现阻止保存或试运行的结构错误'});return items;
}
function setFlowView(mode){$$('.canvas-view-toggle button').forEach(b=>b.classList.toggle('active',b.dataset.flowView===mode));$('#canvas').hidden=mode!=='canvas';$('#flowCheckPanel').hidden=mode!=='check';$('#flowStageWrap')?.classList.toggle('check-mode',mode==='check');if(mode==='check')$('#flowCheckPanel').innerHTML=`<div class="page-heading" style="margin-bottom:14px"><div><h1 style="font-size:17px">配置检查</h1><p>运行前检查入口、CLI、产物契约和返工路径。</p></div></div><div class="flow-check-grid">${flowDiagnostics().map(x=>`<div class="flow-check-item ${x.level}"><b>${escapeHTML(x.title)}</b><p>${escapeHTML(x.detail)}</p></div>`).join('')}</div>`;else flowViewport.renderMinimap()}
function setThemePreference(preference){if(!['light','dark','system'].includes(preference))return;try{localStorage.setItem(THEME_KEY,preference)}catch{}applyTheme(preference)}
// The profile card is visibility:hidden while closed, so callers re-run this with snap when it opens.
function renderThemeSegment({snap=false}={}){const preference=typeof activeThemePreference==='string'?activeThemePreference:readThemePreference();document.querySelectorAll('.upp-theme-seg').forEach(seg=>{seg.querySelectorAll('[data-theme-pref]').forEach(button=>{const on=button.dataset.themePref===preference;button.classList.toggle('active',on);if(button.getAttribute('aria-checked')!==String(on))button.setAttribute('aria-checked',String(on));button.tabIndex=on?0:-1});const tabs=window.AgentFlowSlidingTabs;if(!tabs)return;const indicator=seg.querySelector(':scope > [data-sliding-indicator]');if(snap&&indicator)indicator.style.transition='none';tabs.mount(seg,{activeSelector:'.active',auto:true,compact:true,cacheKey:'upp-theme-seg'}).refresh();if(snap&&indicator){indicator.getBoundingClientRect();requestAnimationFrame(()=>{indicator.style.transition=''})}})}
function renderThemeToggle(){renderThemeSegment();const light=document.documentElement.dataset.theme==='light',button=$('#themeToggle');if(!button)return;window.AgentFlowHarnessIcons?.render(button.querySelector('span'),light?'moon':'sun');button.querySelector('b').textContent=light?'深色':'亮色';button.title=light?'切换到深色主题':'切换到亮色主题'}
function toggleTheme(){const theme=document.documentElement.dataset.theme==='light'?'dark':'light';try{localStorage.setItem(THEME_KEY,theme)}catch{}applyTheme(theme)}
function statusText(s){return ({pending:'待执行',running:'运行中',succeeded:'已完成',failed:'失败',waiting_approval:'待审批',cancelled:'已取消'}[s]||'')}
function canvasPoint(e){return flowViewport.toWorld(e.clientX,e.clientY)}
function flowReaches(from,to){
  const seen=new Set([from]),queue=[from];
  while(queue.length){const cur=queue.shift();if(cur===to)return true;for(const e of state.workflow.edges)if(e.source===cur&&!seen.has(e.target)){seen.add(e.target);queue.push(e.target)}}
  return false;
}
function flowConnectCheck(src,tgt,outlet){
  if(!tgt||tgt.id===src.id)return {ok:false,why:outlet==='reject'?'不能打回到自己':'不能连到自己'};
  if(outlet==='reject')return isPreviewBuildNode(src.id)?{ok:false,why:'预览构建节点没有失败出口'}:{ok:true};
  if(state.workflow.edges.some(e=>e.source===src.id&&e.target===tgt.id))return {ok:false,why:'依赖已经存在'};
  if(flowReaches(tgt.id,src.id))return {ok:false,why:'会形成循环依赖；返工请用「失败打回」'};
  return {ok:true};
}
function startFlowConnect(e,src,outlet){
  e.stopPropagation();e.preventDefault();closeNodeMenu();
  if(flowReadOnly()){toast(flowReadOnlyReason());return}
  const port=e.currentTarget,sx=e.clientX,sy=e.clientY;
  port.setPointerCapture(e.pointerId);
  state.connectFrom=src.id;state.connectOutlet=outlet;state.connectCursor=canvasPoint(e);
  port.closest('.flow-node')?.classList.add('connecting');
  let moved=false,hover=null;
  const clearMarks=()=>$$('#nodes .drop-ok,#nodes .drop-bad').forEach(x=>x.classList.remove('drop-ok','drop-bad'));
  const move=ev=>{
    if(Math.hypot(ev.clientX-sx,ev.clientY-sy)>4)moved=true;
    state.connectCursor=canvasPoint(ev);
    const hit=document.elementFromPoint(ev.clientX,ev.clientY)?.closest('#nodes .flow-node');
    clearMarks();hover=null;
    if(hit&&hit.dataset.id!==src.id){
      hover=state.workflow.nodes.find(x=>x.id===hit.dataset.id)||null;
      if(hover){hit.classList.add(flowConnectCheck(src,hover,outlet).ok?'drop-ok':'drop-bad');state.connectCursor=flowPortPoint(hover,'in')}
    }
    scheduleFlowEdges();
  };
  const up=()=>{
    port.removeEventListener('pointermove',move);port.removeEventListener('pointerup',up);port.removeEventListener('pointercancel',up);
    clearMarks();
    if(!moved){state.connectCursor=null;renderEdges();flowToast(outlet==='reject'?'点一下失败时要打回的步骤，Esc 取消':'点一下下一步，Esc 取消');return}
    if(hover)completeFlowConnect(hover);else cancelFlowConnect();
  };
  port.addEventListener('pointermove',move);port.addEventListener('pointerup',up);port.addEventListener('pointercancel',up);
}
function completeFlowConnect(target){
  const src=state.workflow?.nodes.find(x=>x.id===state.connectFrom),outlet=state.connectOutlet||'success';
  state.connectFrom=null;state.connectCursor=null;state.connectOutlet=null;
  if(!src){renderCanvas();return false}
  const check=flowConnectCheck(src,target,outlet);
  if(!check.ok){toast(check.why);renderCanvas();return false}
  if(outlet!=='reject')return connect(src.id,target.id);
  const prev=state.workflow.nodes.find(x=>x.id===src.rejectTo);
  if(!setRejectTo(src.id,target.id)){renderCanvas();return false}
  flowToast(prev&&prev.id!==target.id?`「${src.name}」的打回目标已从「${prev.name}」改为「${target.name}」`:`「${src.name}」失败时打回到「${target.name}」`,{undo:true});
  return true;
}
function cancelFlowConnect(){
  if(!state.connectFrom&&!state.connectCursor)return false;
  state.connectFrom=null;state.connectCursor=null;state.connectOutlet=null;
  renderCanvas();return true;
}
function connect(source,target){
  if(source===target)return false;
  if(state.workflow.edges.some(e=>e.source===source&&e.target===target)){toast('依赖已经存在');renderCanvas();return false}
  if(flowReaches(target,source)){toast('不能形成循环依赖；返工请使用「失败打回」');renderCanvas();return false}
  flowHistory.record();
  state.workflow.edges.push({id:uid('edge'),source,target,when:'success'});
  dirty();renderCanvas();renderProperties();
  const a=state.workflow.nodes.find(n=>n.id===source),b=state.workflow.nodes.find(n=>n.id===target);
  flowToast(`已连接「${a?.name||source}」→「${b?.name||target}」`,{undo:true});
  return true;
}
// Sets the single reject route of a node; an empty target clears it.
function setRejectTo(nodeId,targetId){
  const wf=state.workflow,n=wf?.nodes.find(x=>x.id===nodeId);
  if(!n)return false;
  targetId=targetId||'';
  if(targetId&&isPreviewBuildNode(nodeId)){toast('预览构建节点没有失败出口');return false}
  if(targetId===nodeId){toast('不能打回到自己');return false}
  if(targetId&&!wf.nodes.some(x=>x.id===targetId))return false;
  const onFailure=targetId?'reject':'fail';
  if((n.rejectTo||'')===targetId&&n.onFailure===onFailure)return true;
  flowHistory.record();
  n.rejectTo=targetId;n.onFailure=onFailure;
  dirty();renderCanvas();renderProperties();
  return true;
}
// Node size and outlet offsets, measured once per renderCanvas so dragging
// and panning never force a layout per pointermove.
function flowNodeMetrics(id){
  const cache=flowNodeMetrics.cache||(flowNodeMetrics.cache=new Map());
  let m=cache.get(id);if(m)return m;
  const el=flowNodeEl(id);if(!el||!el.offsetWidth)return null;
  const row=sel=>{const r=el.querySelector(sel);return r?r.offsetTop+r.offsetHeight/2:el.offsetHeight/2};
  m={w:el.offsetWidth,h:el.offsetHeight,ok:row('.fd-out.ok'),rj:row('.fd-out.rj')};
  cache.set(id,m);return m;
}
function nodeBox(n){
  const m=flowNodeMetrics(n.id),w=m?.w||244,h=m?.h||132;
  return {x:n.position.x,y:n.position.y,w,h,cx:n.position.x+w/2,cy:n.position.y+h/2,right:n.position.x+w,left:n.position.x,top:n.position.y,bottom:n.position.y+h};
}
function flowPortPoint(n,kind){
  const m=flowNodeMetrics(n.id),p=n.position;
  if(!m||kind==='in')return {x:p.x,y:p.y+27};
  return {x:p.x+m.w,y:p.y+(kind==='reject'?m.rj:m.ok)};
}
function scheduleFlowEdges(){
  if(scheduleFlowEdges.raf)return;
  scheduleFlowEdges.raf=requestAnimationFrame(()=>{scheduleFlowEdges.raf=0;renderEdges()});
}
function flowCurve(a,b){const dx=Math.max(50,Math.abs(b.x-a.x)*.45);return `M ${a.x} ${a.y} C ${a.x+dx} ${a.y}, ${b.x-dx} ${b.y}, ${b.x} ${b.y}`}
// Reject routes usually point back upstream; loop them under both nodes.
function flowRejectPath(src,tgt){
  const a=flowPortPoint(src,'reject'),b=flowPortPoint(tgt,'in');
  if(b.x>a.x+60)return {d:flowCurve(a,b),mx:(a.x+b.x)/2,my:(a.y+b.y)/2};
  const y=Math.max(nodeBox(src).bottom,nodeBox(tgt).bottom)+46;
  return {d:`M ${a.x} ${a.y} C ${a.x+44} ${a.y}, ${a.x+44} ${y}, ${a.x} ${y} L ${b.x} ${y} C ${b.x-44} ${y}, ${b.x-44} ${b.y}, ${b.x} ${b.y}`,mx:(a.x+b.x)/2,my:y};
}
async function deleteEdgeById(id){
  const edge=state.workflow?.edges.find(x=>x.id===id);
  if(!edge)return;
  if(flowReadOnly())return toast(flowReadOnlyReason());
  flowHistory.record();
  state.workflow.edges=state.workflow.edges.filter(x=>x.id!==id);
  dirty();renderCanvas();renderProperties();flowToast('已删除连线',{undo:true});
}
async function deleteRejectEdgeByNodeId(nodeId){
  const node=state.workflow?.nodes.find(n=>n.id===nodeId);
  if(!node||!node.rejectTo)return;
  if(flowReadOnly())return toast(flowReadOnlyReason());
  if(setRejectTo(nodeId,''))flowToast('已取消失败打回',{undo:true});
}
function renderEdges(){
  const svg=$('#edges');if(!svg)return;
  const wf=state.workflow,del=(x,y)=>`<g class="fd-e-del" transform="translate(${x} ${y})"><circle r="9"/><path d="M -3 -3 L 3 3 M 3 -3 L -3 3"/></g>`;
  let html='<defs><marker id="fdArrow" viewBox="0 0 10 10" refX="8.5" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path class="fd-arrow" d="M1 1.5 8.5 5 1 8.5"/></marker><marker id="fdArrowHover" viewBox="0 0 10 10" refX="8.5" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path class="fd-arrow hover" d="M1 1.5 8.5 5 1 8.5"/></marker><marker id="fdArrowReject" viewBox="0 0 10 10" refX="8.5" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path class="fd-arrow rj" d="M1 1.5 8.5 5 1 8.5"/></marker></defs>';
  if(wf){
    const byId=new Map(wf.nodes.map(n=>[n.id,n]));
    wf.edges.forEach(e=>{
      const a=byId.get(e.source),b=byId.get(e.target);if(!a||!b)return;
      const p=flowPortPoint(a,'success'),q=flowPortPoint(b,'in'),d=flowCurve(p,q);
      html+=`<g class="fd-e" data-edge="${escapeHTML(e.id)}"><path class="fd-e-hit" d="${d}"/><path class="edge fd-e-path" d="${d}"/>${del((p.x+q.x)/2,(p.y+q.y)/2)}<title>${escapeHTML(a.name)} → ${escapeHTML(b.name)} · 点击删除</title></g>`;
    });
    wf.nodes.forEach(n=>{
      const t=n.rejectTo&&byId.get(n.rejectTo);if(!t)return;
      const {d,mx,my}=flowRejectPath(n,t);
      html+=`<g class="fd-e rj" data-reject-from="${escapeHTML(n.id)}"><path class="fd-e-hit" d="${d}"/><path class="edge fd-e-path reject-edge" d="${d}"/><g class="fd-e-label" transform="translate(${mx} ${my})"><rect x="-30" y="-10" width="60" height="20" rx="10"/><text text-anchor="middle" y="3.5">失败打回</text></g>${del(mx+42,my)}<title>${escapeHTML(n.name)} 失败打回到 ${escapeHTML(t.name)} · 点击取消</title></g>`;
    });
    const src=state.connectFrom&&byId.get(state.connectFrom);
    if(src&&state.connectCursor){
      const reject=state.connectOutlet==='reject';
      html+=`<path class="fd-e-temp${reject?' rj':''}" d="${flowCurve(flowPortPoint(src,reject?'reject':'success'),state.connectCursor)}"/>`;
    }
  }
  svg.innerHTML=html;
  if(svg.dataset.bound)return;
  svg.dataset.bound='1';
  const edgeAt=e=>state.workflow&&!flowReadOnly()?e.target.closest?.('.fd-e'):null;
  svg.addEventListener('pointerdown',e=>{if(edgeAt(e))e.stopPropagation()});
  svg.addEventListener('click',e=>{
    const g=edgeAt(e);if(!g)return;
    e.stopPropagation();
    if(state.connectFrom)return;
    if(g.dataset.edge)deleteEdgeById(g.dataset.edge);else deleteRejectEdgeByNodeId(g.dataset.rejectFrom);
  });
}

function openNodeMenu(id,anchor){
  closeNodeMenu();
  const wrap=$('#flowStageWrap');if(!wrap||flowReadOnly())return;
  const menu=document.createElement('div'),ic=name=>flowPalette.icon(name,15);
  menu.className='fd-menu';menu.id='flowNodeMenu';menu.setAttribute('role','menu');menu.dataset.node=id;
  menu.innerHTML=`<button type="button" role="menuitem" data-a="edit">${ic('pencil')}编辑属性</button><button type="button" role="menuitem" data-a="rename">${ic('type')}重命名<kbd>F2</kbd></button><button type="button" role="menuitem" data-a="dup" ${isPreviewBuildNode(id)?'disabled':''}>${ic('copy')}复制<kbd>⌘D</kbd></button><hr><button type="button" role="menuitem" data-a="del" class="danger">${ic('trash')}删除<kbd>Del</kbd></button>`;
  const r=anchor.getBoundingClientRect(),wr=wrap.getBoundingClientRect();
  menu.style.left=Math.max(8,r.right-wr.left-180)+'px';menu.style.top=(r.bottom-wr.top+6)+'px';
  wrap.appendChild(menu);
  menu.addEventListener('pointerdown',e=>e.stopPropagation());
  menu.addEventListener('click',e=>{
    const b=e.target.closest('button[data-a]');if(!b||b.disabled)return;
    closeNodeMenu();
    ({edit:()=>openNodeDrawer(id),rename:()=>startNodeRename(id),dup:()=>duplicateNode(id),del:()=>removeNode(id)})[b.dataset.a]();
  });
}
function closeNodeMenu(){const menu=$('#flowNodeMenu');if(!menu)return false;menu.remove();return true}
function flowToast(message,opts={}){
  const box=$('#flowToast');
  if(!box||!$('#flowView')?.classList.contains('active')){toast(message);return}
  $('#flowToastMsg').textContent=message;
  const withUndo=!!opts.undo&&flowHistory.canUndo(),at=flowHistory.current();
  box.classList.toggle('noact',!withUndo);
  $('#flowToastAction').onclick=()=>{box.classList.remove('on');if(flowHistory.current()!==at)return flowToast('之后又有修改，请用工具条或 ⌘Z 撤销');if(flowHistory.undo())flowToast('已撤销')};
  box.classList.add('on');
  clearTimeout(flowToast.timer);flowToast.timer=setTimeout(()=>box.classList.remove('on'),withUndo?4200:2400);
}
function openNodeDrawer(id){
  if(!state.workflow?.nodes.some(n=>n.id===id))return;
  if(state.selectedNode!==id)state.flowWantReject=null;
  state.selectedNode=id;renderCanvas();renderProperties();
}
function closeNodeDrawer(){
  if(!state.selectedNode)return false;
  state.selectedNode=null;state.flowWantReject=null;renderCanvas();renderProperties();
  return true;
}
// Keeps the stage wrapper's drawer flag in sync with the inspector and the AI panel.
function syncFlowDrawerLayout(){
  const wrap=$('#flowStageWrap');if(!wrap)return;
  const ai=!$('#aiOrchestrator')?.hidden,props=$('#properties')?.classList.contains('open');
  const had=wrap.classList.contains('has-drawer'),has=!!(ai||props);
  wrap.classList.toggle('has-drawer',has);wrap.classList.toggle('ai-open',ai);
  if(had!==has){flowViewport.layoutTight();flowViewport.renderMinimap()}
}
function setFlowDrawer(open,nodeId){
  const host=$('#properties');if(!host)return;
  const was=host.classList.contains('open');
  host.classList.toggle('open',open);
  syncFlowDrawerLayout();
  if(open&&(!was||host.dataset.node!==nodeId))setTimeout(()=>flowViewport.reveal(nodeId),260);
  host.dataset.node=open?nodeId:'';
}
function bindFlowDesigner(){
  if(bindFlowDesigner.done)return;bindFlowDesigner.done=true;
  flowViewport.bind({onBlankClick:()=>{closeNodeMenu();if(!cancelFlowConnect())closeNodeDrawer()}});
  flowPalette.bind();
  $('#canvasUndo')?.addEventListener('click',()=>{if(flowHistory.undo())flowToast('已撤销')});
  $('#canvasRedo')?.addEventListener('click',()=>{if(flowHistory.redo())flowToast('已重做')});
  $('#canvasArrange')?.addEventListener('click',arrange);
  document.addEventListener('pointerdown',e=>{if(!e.target.closest('#flowNodeMenu,[data-node-menu]'))closeNodeMenu()});
  document.addEventListener('keydown',e=>{
    if(!$('#flowView')?.classList.contains('active')||e.defaultPrevented)return;
    const mod=e.metaKey||e.ctrlKey,key=(e.key||'').toLowerCase(),blocked=!!$('.modal.open')||$('#commandPalette')?.hidden===false;
    if(mod&&key==='s'){e.preventDefault();if(!blocked&&!$('#saveWorkflow')?.disabled)saveWorkflow().catch(()=>{});return}
    if(blocked||e.target.closest?.('input,textarea,select,[contenteditable="true"],[contenteditable=""]'))return;
    if(mod&&key==='z'){e.preventDefault();if(e.shiftKey?flowHistory.redo():flowHistory.undo())flowToast(e.shiftKey?'已重做':'已撤销');return}
    if(mod&&key==='y'){e.preventDefault();if(flowHistory.redo())flowToast('已重做');return}
    if(mod&&(key==='='||key==='+')){e.preventDefault();flowViewport.zoomBy(1.2);return}
    if(mod&&key==='-'){e.preventDefault();flowViewport.zoomBy(1/1.2);return}
    const id=state.selectedNode;
    if(!id||flowReadOnly())return;
    if(mod&&key==='d'){e.preventDefault();duplicateNode(id)}
    else if(!mod&&(e.key==='Delete'||(e.key==='Backspace'&&!e.target.closest?.('#properties,#aiOrchestrator,#flowSwitchPop,#flowSettingsPanel')))){e.preventDefault();removeNode(id)}
    else if(e.key==='F2'){e.preventDefault();startNodeRename(id)}
  });
}
// Escape order inside the designer: switcher > connecting > node menu > drawer.
function flowDesignerEscape(e){
  if(flowPalette.closeSwitcher())return true;
  if(cancelFlowConnect())return true;
  if(closeNodeMenu())return true;
  if(!$('#flowView')?.classList.contains('active'))return false;
  if(e?.target?.closest?.('#properties input,#properties textarea,#properties select')){e.target.blur();return true}
  return false;
}
function ensureNodeDefaults(n){
  n.onFailure=n.onFailure||(n.rejectTo?'reject':'fail');
  n.retryStrategy=n.retryStrategy||'targeted_fix';
  n.carryOver=n.carryOver||['failed_checks','last_error','upstream_artifacts'];
}
function artifactRowsHTML(n){
  return (n.artifactOutputs||[]).map((a,i)=>`<article class="artifact-card" data-artifact-row="${i}">
      <header><input data-ak="name" value="${escapeHTML(a.name||'')}" placeholder="产物名称，如 checkers-report"><button type="button" data-remove-artifact="${i}" title="删除产物" aria-label="删除产物">${flowPalette.icon('x',12)}</button></header>
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
  state.agentDraft.prompt=state.agentDraft.prompt||'';
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
  const isNew=state.agentModalMode==='create';
  if(!isNew){host.innerHTML='';host.dataset.ready='1';return}
  if(host.dataset.ready)return;
  host.dataset.ready='1';
  const current=state.agentCreatePreset||'blank';
  const card=(id,icon,name,desc)=>`<button type="button" class="as-preset ${id===current?'active':''}" data-agent-preset="${id}" aria-pressed="${id===current?'true':'false'}"><span class="as-preset-icon">${icon}</span><b>${name}</b><small>${desc}</small></button>`;
  const presetIcon=p=>({'developer':'码','reviewer':'审','tester':'测','coordinator':'交'})[p.id]||'角';
  host.innerHTML=`<div class="as-presets agent-start-grid">${card('blank','空','空白','只填名称和模型，其余创建后再补')}${agentPresets.map(p=>card(p.id,presetIcon(p),p.name,p.desc)).join('')}</div>`;
  host.querySelectorAll('[data-agent-preset]').forEach(button=>button.onclick=()=>applyAgentPreset(button.dataset.agentPreset));
}
function insertAgentPlaceholder(host, token){
  const el=host.querySelector('[data-tp="prompt"]');
  if(!el)return;
  // Assigning el.value moves the caret to the end and scrolls the textarea to
  // the bottom; setRangeText edits in place, and the scroll offset is kept.
  const top=el.scrollTop;
  const start=el.selectionStart??el.value.length, end=el.selectionEnd??start;
  el.focus({preventScroll:true});
  el.setRangeText(token,start,end,'end');
  el.scrollTop=top;
  el.dispatchEvent(new Event('input',{bubbles:true}));
}
// Placeholders the engine replaces when it renders a node's task brief
// (internal/engine/prompt.go applyPromptPlaceholders, node_execute.go).
const AGENT_PROMPT_PLACEHOLDERS=[
  {t:'{{input}}',l:'任务输入',d:'本次运行的输入；不写时自动附在末尾'},
  {t:'{{artifacts}}',l:'上游产物',d:'上游节点交付的产物清单；不写时自动附在末尾'},
  {t:'{{upstream}}',l:'上游结论',d:'直接上游节点的输出；不写时自动附在末尾'},
  {t:'{{feedback}}',l:'打回反馈',d:'本节点上一轮被打回或门禁失败的原因，首轮为空'},
  {t:'{{rejectTo}}',l:'打回节点',d:'失败时打回的节点，未配置时为「上游实现节点」'},
  {t:'{{repository}}',l:'仓库',d:'仓库名、工蜂项目、基线与本次工作分支'},
  {t:'{{existingMR}}',l:'已有 MR',d:'本轮是否沿用已有分支与 MR 的说明'},
  {t:'{{pipelineUrl}}',l:'流水线',d:'仓库绑定的流水线地址'},
];
function renderAgentTaskBook(a){
  const host=$('#agentTaskBookPanel');if(!host)return;
  const d=ensureAgentDraft(a);
  host.innerHTML=`<div class="as-prompt-box">
      <div class="as-ph-bar" role="toolbar" aria-label="插入运行时变量"><span>插入</span>${AGENT_PROMPT_PLACEHOLDERS.map(x=>`<button type="button" data-insert-ph="${x.t}" data-tip="${escapeHTML(`${x.t} · ${x.d}`)}">${x.l}</button>`).join('')}</div>
      <textarea data-tp="prompt" aria-label="角色 prompt" placeholder="写清它是谁、做什么、按什么顺序、哪些事不能做。&#10;&#10;例如：你是 Issue 根因分析师，只分析不修改。读 Issue 和相关源码，把根因定位到具体文件和函数，写进 fix-plan.md。先复现，再缩小范围，最后给出修复步骤。不要改业务代码，也不要跑测试。">${escapeHTML(d.prompt||'')}</textarea>
    </div>
    <p class="as-prompt-note" id="agentPromptNote"></p>`;
  const el=host.querySelector('[data-tp="prompt"]');
  el.oninput=()=>{d.prompt=el.value;renderAgentSummary();dirty()};
  host.querySelectorAll('[data-insert-ph]').forEach(btn=>{
    // Keep focus (and the caret) in the textarea while clicking a chip.
    btn.onmousedown=e=>e.preventDefault();
    btn.onclick=()=>insertAgentPlaceholder(host,btn.dataset.insertPh);
  });
  renderAgentSummary();
}
function renderAgentGovernance(a){
  const host=$('#agentGovPanel');if(!host)return;
  const d=ensureAgentDraft(a);
  const loop=d.loop||{};
  const after=(d.interventions||[]).some(x=>x.stage==='after_execution');
  const publish=(d.interventions||[]).some(x=>x.stage==='before_publish');
  host.innerHTML=`<div class="as-section-stack">
    <section class="as-section-block as-loop-card"><div class="as-section-head"><b>做不对应再试一次</b><small>同一沙箱里重跑，适合测试失败后只修失败项</small></div><label class="as-switch-row"><span><b>启用自动重试</b><small>门禁失败后进入下一轮，直到通过或达到次数上限</small></span><input name="loopEnabled" type="checkbox" ${loop.enabled?'checked':''} role="switch"></label><div class="as-grid as-grid-tight"><label class="as-field"><span>最多试几轮</span><input name="loopMaxIterations" type="number" min="1" max="12" value="${loop.maxIterations||3}"></label><label class="as-field"><span>试完仍失败</span><select name="loopOnExhausted" class="select"><option value="fail" ${loop.onExhausted!=='human'?'selected':''}>标记失败并停下</option><option value="human" ${loop.onExhausted==='human'?'selected':''}>交给人工处理</option></select></label><label class="as-field wide"><span>下一轮怎么提醒它</span><textarea name="loopFeedbackPrompt" rows="2" placeholder="读 {{feedback}}，只修失败项，不要重做已经通过的部分">${escapeHTML(loop.feedbackPrompt||'')}</textarea></label></div></section>
    <section class="as-section-block"><div class="as-section-head"><b>要交的文件</b><small>写相对工作区的路径，例如 <code>fix-plan.md</code></small></div><div id="agentArtifactPanel"></div></section>
    <section class="as-section-block"><div class="as-section-head"><b>产物通过后的门禁命令</b><small>必需产物先校验；通过后才按顺序执行这些命令</small></div><div id="agentGatePanel"></div></section>
    <section class="as-section-block"><div class="as-section-head"><b>要不要人看一眼</b><small>打开后，运行会停在「待我确认」</small></div><div class="as-toggle-grid"><label class="as-switch-row"><span><b>做完后先复核</b><small>Agent 写完结论，人批准再往下走</small></span><input name="interventionAfter" type="checkbox" ${after?'checked':''} role="switch"></label><label class="as-switch-row"><span><b>交文件前先确认</b><small>磁盘产物落盘前停一下</small></span><input name="interventionPublish" type="checkbox" ${publish?'checked':''} role="switch"></label></div></section>
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
function gateCoverage(g){
  return g.coverage||{};
}
function gateCoverageFields(g){
  if(g.mode!=='diff_coverage')return '';
  const c=gateCoverage(g),fmt=['go_coverprofile','lcov','cobertura'].includes(c.reportFormat)?c.reportFormat:'go_coverprofile';
  const threshold=c.thresholdPct??80,minLines=c.minChangedLines??5;
  return `<div class="gate-coverage"><label>覆盖率报告路径（相对仓库）</label><input data-gk="coverage.reportPath" value="${escapeHTML(c.reportPath||'')}" placeholder="例如 coverage.out、lcov.info、coverage.xml"><label>报告格式</label><select data-gk="coverage.reportFormat"><option value="go_coverprofile" ${fmt==='go_coverprofile'?'selected':''}>go_coverprofile（go test -coverprofile）</option><option value="lcov" ${fmt==='lcov'?'selected':''}>lcov（lcov.info / tracefile）</option><option value="cobertura" ${fmt==='cobertura'?'selected':''}>cobertura（coverage.xml，Python/Java 常用）</option></select><label>改动行覆盖率阈值（%）</label><input type="number" min="0" max="100" step="1" data-gk="coverage.thresholdPct" value="${escapeHTML(String(threshold))}"><label>改动少于多少行时不判定</label><input type="number" min="0" step="1" data-gk="coverage.minChangedLines" value="${escapeHTML(String(minLines))}"><p class="prop-hint">只统计本次改动新增/修改的可执行行，存量低覆盖率不阻断。未达标时会把未覆盖的文件和行号交回 Agent 补测试。命令必须生成上面这个报告文件（例如 <code>go test -coverprofile=coverage.out ./...</code>）。</p></div>`;
}
function gateFailureSelect(g){
  const v=['loop','reject','fail','human'].includes(g.onFailure)?g.onFailure:'loop',scope=g.executionScope==='repositories'?'repositories':'run';
  const mode=g.mode==='diff_coverage'?'diff_coverage':'command';
  return `<label>判定方式</label><select data-gk="mode"><option value="command" ${mode==='command'?'selected':''}>命令退出码</option><option value="diff_coverage" ${mode==='diff_coverage'?'selected':''}>改动行覆盖率</option></select>${gateCoverageFields(g)}<label>检查范围</label><select data-gk="executionScope"><option value="run" ${scope==='run'?'selected':''}>Run工作区（校验产物等场景）</option><option value="repositories" ${scope==='repositories'?'selected':''}>每个代码仓库</option></select><label>失败后怎么走</label><select data-gk="onFailure"><option value="loop" ${v==='loop'?'selected':''}>本节点自修正</option><option value="reject" ${v==='reject'?'selected':''}>打回上游</option><option value="fail" ${v==='fail'?'selected':''}>本节点失败</option><option value="human" ${v==='human'?'selected':''}>转人工确认</option></select><p class="prop-hint">范围必须显式选择。Run 工作区适合报告和产物；每个代码仓库适合测试和 Git 检查（改动行覆盖率只能选每个代码仓库）。保存后按所选范围执行，不会再根据命令是否以 git 开头自动切换目录。</p>`;
}
function applyGateField(gate,el){
  const key=el.dataset.gk;
  if(key==='name')gate.name=el.value;
  else if(key==='onFailure')gate.onFailure=el.value;
  else if(key==='executionScope')gate.executionScope=el.value;
  else if(key==='mode'){
    gate.mode=el.value==='diff_coverage'?'diff_coverage':'command';
    if(gate.mode==='diff_coverage'){
      gate.executionScope='repositories';
      gate.coverage=gate.coverage||{reportPath:'',reportFormat:'go_coverprofile'};
    }else{delete gate.coverage}
  }
  else if(key.startsWith('coverage.')){
    const field=key.slice('coverage.'.length);
    gate.coverage=gate.coverage||{};
    if(field==='thresholdPct'){const raw=String(el.value||'').trim();const n=raw===''?NaN:Number(raw);gate.coverage.thresholdPct=Number.isFinite(n)?Math.min(100,Math.max(0,n)):undefined;if(gate.coverage.thresholdPct===undefined)delete gate.coverage.thresholdPct;}
    else if(field==='minChangedLines'){const n=parseInt(el.value,10);gate.coverage.minChangedLines=Number.isFinite(n)&&n>=0?n:undefined;if(gate.coverage.minChangedLines===undefined)delete gate.coverage.minChangedLines;}
    else gate.coverage[field]=el.value;
  }
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
  host.querySelectorAll('[data-gate-row]').forEach(row=>row.querySelectorAll('[data-gk]').forEach(el=>el.oninput=el.onchange=()=>{const gate=d.verification[Number(row.dataset.gateRow)];const isMode=el.dataset.gk==='mode';applyGateField(gate,el);if(isMode)renderAgentGateEditors(d);touch();dirty()}));
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
  const step=(i,title,sub,body)=>`<li class="fd-tl-step"><span class="fd-tl-n">${i}</span><div class="fd-tl-c"><div class="fd-tl-h"><b>${title}</b><small>${sub}</small></div>${body}</div></li>`;
  const artifactRows=artifacts.length?artifacts.map(a=>`<div class="fd-tl-row"><span class="fd-tl-ic">${flowPalette.icon('file',13)}</span><span class="fd-tl-main"><b>${escapeHTML(a.name||a.path||'未命名产物')}</b><code>${escapeHTML(a.path||'未设置路径')}</code></span><span class="fd-tag${a.required?'':' soft'}">${a.required?'必须交':'可选'}</span></div>`).join(''):'<div class="fd-tl-none">这个节点没有声明需要校验的产物。</div>';
  const gateRows=gates.length?gates.map(g=>{const cmds=(g.commands||[]).filter(Boolean);const cov=g.mode==='diff_coverage'?g.coverage||{}:null;const summary=cov?`改动行覆盖率 ≥ ${escapeHTML(String(cov.thresholdPct??80))}%（${escapeHTML(cov.reportPath||'未配置报告')}）`:escapeHTML(cmds.join(' && ')||'人工确认');return `<div class="fd-tl-row"><span class="fd-tl-ic">${flowPalette.icon('terminal',13)}</span><span class="fd-tl-main"><b>${escapeHTML(g.name||g.id||'未命名门禁')}</b><code>${summary}</code><small>${g.executionScope==='repositories'?'每个代码仓库':'Run 工作区'} · 失败后 ${escapeHTML({loop:'本节点自修正',reject:'打回上游',human:'转人工确认',fail:'本节点失败'}[g.onFailure]||'按节点策略')}</small></span></div>`}).join(''):'<div class="fd-tl-none">这个节点没有额外门禁命令。</div>';
  const terminal=n.type==='agent'?step(1,'先提交结构化终态','平台内建',`<div class="fd-tl-row"><span class="fd-tl-ic">${flowPalette.icon('check',13)}</span><span class="fd-tl-main"><code>pass | rework | fail | human</code><small>Agent 完成任务后必须先调用 submit_gate_result 提交终态；缺失时自动恢复同一会话重试。</small></span></div>`):'';
  const base=n.type==='agent'?1:0;
  return `<p class="prop-hint">执行时 Agent 节点先提交结构化终态，随后校验产物，最后执行可选门禁命令。</p><ol class="fd-tl">${terminal}${step(base+1,'再校验产物',`${artifacts.length} 项`,artifactRows)}${step(base+2,'最后触发额外门禁',`${gates.length} 项`,gateRows)}</ol>${agent?'<button type="button" class="fd-btn ghost sm" id="openBoundAgentValidation">去角色定义编辑校验清单</button>':''}`;
}

function renderAgentAssetsPanel(a){
  const host=$('#agentAssetsPanel');if(!host)return;
  const d=ensureAgentDraft(a);
  const generic=catalogGeneric(state.workflow);
  const col=(title,sub,name,items,selected)=>{const vis=name==='mcpServers'?(generic?(items||[]):visibleMCPAssets(items)):(name==='rules'||name==='skills'?(items||[]).filter(item=>catalogVisible(item.projectId,item.shared)):visibleAssets(items));const picked=vis.filter(item=>name==='mcpServers'&&generic?genericMCPChecked(item.id,selected):(selected||[]).includes(item.id)).length;const empty=name==='mcpServers'?(generic?'通用模板绑定市场别名（如 github），运行时落到当前项目已安装的 MCP':'这个项目或你的个人范围还没有 MCP，先到「MCP 管理」里添加'):name==='rules'?'这个项目还没有可用规则，先到「规则管理」里添加或共享':name==='skills'?'这个项目还没有可用技能，先到「技能管理」里添加或共享':'这个项目还没有此类配置，先到「环境资产」里添加';return `<div class="as-asset-col"><div class="as-asset-col-head"><b>${title}</b><span>${sub}${picked?` · 已选 ${picked}`:''}</span></div><div class="as-asset-search"><span>⌕</span><input type="search" data-asset-filter="${name}" placeholder="搜索名称…"></div><div class="as-asset-list" data-asset-list="${name}">${vis.map(x=>`<label class="as-asset-item"><input type="checkbox" name="${name}" value="${escapeHTML(x.id)}" ${(name==='mcpServers'&&generic?genericMCPChecked(x.id,selected):(selected||[]).includes(x.id))?'checked':''}><div><b>${escapeHTML(x.name)}</b><small>${escapeHTML(x.description||x.id)}${x.shared&&x.projectId!==state.projectFilter?' · 共享':''}</small></div></label>`).join('')||`<div class="as-empty">${empty}</div>`}</div></div>`};
  host.innerHTML=`<section class="as-section-block as-platform-mcp-section"><div class="as-section-head"><b>平台能力</b><small>独立开关，不占用 MCP 列表</small></div><label class="as-switch-row"><span><b>接入平台 MCP（LoopForge）</b><small>开启后，这个角色运行时通过平台 MCP（agentflow-platform）直接调用平台侧功能：需求、任务、运行、项目等平台操作。</small></span><input type="checkbox" name="platformMcp" ${d.platformMcp?'checked':''} role="switch"></label><label class="as-switch-row"><span><b>接入团队知识库</b><small>开启后，这个角色运行时可只读检索团队知识库（agentflow-knowledge），并自动附带「知识库使用规范」规则；产出的 knowledge.md 会进入知识沉淀审核队列。</small></span><input type="checkbox" name="knowledgeBase" ${d.knowledgeBase?'checked':''} role="switch"></label></section><div class="as-assets">${col('必须遵守','规则','rules',state.data.rules,d.rules)}${col('怎么干活','技能','skills',state.data.skills,d.skills)}${col('外部工具','MCP','mcpServers',generic?genericTemplateMCPOptions(d.mcpServers):state.data.mcpServers,d.mcpServers)}</div><section class="as-section-block as-hook-section"><div class="as-section-head"><b>运行时 Hook</b><small>在工具调用前后插一段脚本，比如拦危险命令。一般可以先不配。</small></div><div id="agentHookPanel"></div></section>`;
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
  const f=$('#agentForm'),d=state.agentDraft||{};
  const exec=(state.data.executors||[]).find(x=>x.id===(f?.elements?.executorId?.value||d.executorId));
  const chosen=agentEffectiveModel(f),model=chosen||(exec?.model?`${exec.model}（默认）`:'默认模型');
  const effort=(f?.elements?.effort?.value||'').trim();
  const meta=$('#agentRuntimeMeta');
  if(meta)meta.textContent=[exec?.name,effort?`${model} · ${effort}`:model].filter(Boolean).join(' · ');
  const arts=(d.artifactOutputs||[]).filter(x=>x.name||x.path).length;
  const gates=(d.verification||[]).filter(x=>x.name||(x.commands||[]).length).length;
  const loopOn=f?.elements?.loopEnabled?.checked??d.loop?.enabled;
  const note=$('#agentPromptNote');
  if(note){
    const parts=['角色名'];
    if(arts)parts.push(`${arts} 个产物`);
    if(gates)parts.push(`${gates} 条门禁`);
    parts.push(loopOn?`最多 ${f?.elements?.loopMaxIterations?.value||d.loop?.maxIterations||3} 轮重试`:'重试策略');
    note.textContent=`运行时自动附上：${parts.join('、')}`;
  }
  const desc=f?.elements?.description,count=$('#agentDescCount');
  if(desc&&count)count.textContent=`${desc.value.length}/${desc.maxLength>0?desc.maxLength:160}`;
  const picked=name=>f?f.querySelectorAll(`input[name="${name}"]:checked`).length:0;
  const setCount=(sel,n)=>{const el=$(sel);if(el&&el.textContent!==String(n)){el.textContent=String(n);el.hidden=!n;syncTabIndicator(el.closest('.agent-tabs'))}else if(el)el.hidden=!n};
  setCount('#agentGovCount',arts+gates);
  setCount('#agentAssetsCount',picked('rules')+picked('skills')+picked('mcpServers')+(d.hooks||[]).length);
}
function renderAgentPreview(a){
  renderAgentSummary();
}
function readAgentDraft(f){
  const d=state.agentDraft||{};
  return {prompt:String(d.prompt||'').trim()?d.prompt:'',artifactOutputs:d.artifactOutputs||[],verification:d.verification||[],hooks:d.hooks||[]};
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
  const hint=generic?'这些角色打包在通用模板里，和当前项目目录无关。改任务书只影响平台模板，所有项目下次运行生效。':`本项目 ${owned.length} 个 · 共享 ${shared.length} 个；其他项目私有角色已隐藏。`;
  if(!agent)return `<div class="fd-field"><label>这个步骤用哪个角色</label><select class="select" data-p="agentId">${opts}</select><p class="prop-hint">${hint}</p><p class="prop-hint">先选一个已定义的角色。画布只编排顺序和打回，不改编排角色内部的任务。</p></div>`;
  const arts=effectiveNodeArtifacts(n,agent),gates=roleGates(agent);
  const duty=String(agent.description||'').trim(),taskHint=duty?(duty.length>48?`${duty.slice(0,48)}…`:duty):'还没写职责，可在角色定义里补';
  const model=agent.model||exec?.model||exec?.name||'未选模型';
  return `<div class="fd-field"><label>这个步骤用哪个角色</label><select class="select" data-p="agentId">${opts}</select><p class="prop-hint">${hint}</p></div>
    <div class="fd-role"><div class="fd-role-h"><span class="fd-tile" style="--tint:${flowPalette.roleTint(agent.id)}">${escapeHTML(flowPalette.initial(agent.name))}</span><span class="fd-role-t"><b>${escapeHTML(agent.name)}</b><small>${escapeHTML(taskHint)}</small></span><button type="button" class="fd-btn ghost sm" id="openBoundAgent">编辑角色 prompt</button></div><div class="fd-stats"><div title="${escapeHTML(model)}"><b class="txt">${escapeHTML(model)}</b><small>模型</small></div><div><b>${arts.length}</b><small>产物</small></div><div><b>${gates.length}</b><small>门禁</small></div><div><b>${agent.loop?.enabled?`${agent.loop.maxIterations||1} 轮`:'单轮'}</b><small>Loop</small></div></div></div>`;
}
function flowDrawerHead(n,kind,label){
  const agent=kind==='agent'?workflowAgent(n.agentId):null;
  const tile=agent?escapeHTML(flowPalette.initial(agent.name)):flowPalette.icon(FLOW_NODE_TYPES[kind].icon,16);
  const title=kind==='preview'?`<h3>${escapeHTML(n.name)}</h3>`:`<input class="fd-dh-name" data-p="name" value="${escapeHTML(n.name)}" maxlength="120" aria-label="步骤名称" spellcheck="false">`;
  return `<div class="fd-dh"><span class="fd-tile" style="--tint:${flowNodeTint(n)}">${tile}</span><div class="fd-dh-t">${title}<small title="${escapeHTML(n.id)}">${label} · ${escapeHTML(n.id)}</small></div><button type="button" class="fd-icon-btn danger" id="deletePropertyNode" aria-label="删除步骤" data-tip="${kind==='preview'?'移除预览节点':'删除 Del'}">${flowPalette.icon('trash')}</button><button type="button" class="fd-icon-btn" id="closeNodeDrawer" aria-label="关闭" data-tip="关闭 Esc">${flowPalette.icon('x')}</button></div>`;
}
function flowFold(id,title,sub,body,defaultOpen){
  const folds=state.flowFolds||(state.flowFolds={});
  const open=folds[id]===undefined?defaultOpen:folds[id];
  return `<details class="fd-sec fd-fold" data-fold="${id}" ${open?'open':''}><summary><span class="fd-sec-t">${title}</span>${sub?`<small>${sub}</small>`:''}<span class="fd-chev">${flowPalette.icon('chev',14)}</span></summary><div class="fd-sec-b">${body}</div></details>`;
}
function renderPreviewBuildProperties(host,n){
  const model=workflowPreviewModel(state.workflow);
  const spa=state.workflow.preview?.spaFallback!==false;
  host.innerHTML=`${flowDrawerHead(n,'preview','系统步骤 · 前端预览')}
    <div class="fd-db">
      <section class="fd-sec"><div class="fd-sec-h"><span class="fd-sec-t">主配置</span></div><div class="fd-sec-b">
        <p class="prop-hint">运行到该节点时用所选模型构建静态站点，不必再手写脚本节点；Run 成功后平台归档预览。它只有成功出口。</p>
        <div class="fd-field"><label>CodeBuddy 模型</label><select class="select" id="previewNodeModel"></select></div>
        <label class="fd-check"><input type="checkbox" id="previewNodeSpa" ${spa?'checked':''}><span>单页应用回退（找不到路径时返回入口 HTML）</span></label>
      </div></section>
    </div>
    <div class="fd-df"><span class="fd-df-hint">从左侧「成功」出口连到下一步；预览节点没有失败出口</span></div>`;
  fillPreviewModelSelect($('#previewNodeModel'), model);
  const sel=$('#previewNodeModel');
  if(sel)sel.onchange=()=>{flowHistory.record();updatePreviewContract({model:sel.value});dirty();renderCanvas();};
  const spaBox=$('#previewNodeSpa');
  if(spaBox)spaBox.onchange=()=>{flowHistory.record();updatePreviewContract({spaFallback:spaBox.checked});dirty();};
}
function renderProperties(){
  const host=$('#properties');if(!host)return;
  const n=state.workflow?.nodes.find(node=>node.id===state.selectedNode);
  if(!n){setFlowDrawer(false);return}
  const kind=flowNodeKind(n),ro=flowReadOnly();
  const same=host.dataset.node===n.id,scroll=same?host.querySelector('.fd-db')?.scrollTop||0:0;
  const active=same&&host.contains(document.activeElement)?document.activeElement:null;
  const focusKey=active?.dataset?.p||'',caret=focusKey&&typeof active.selectionStart==='number'?[active.selectionStart,active.selectionEnd]:null;
  if(kind==='preview')renderPreviewBuildProperties(host,n);
  else{
    ensureNodeDefaults(n);
    const sopChoices=sopWorkflowChoices(state.workflow.id);
    const sopSelect=n.type==='sop'?`<div class="fd-field"><label>要跑的 SOP</label><select class="select" data-p="sopWorkflowId"><option value="">选择一条已有流程</option>${sopChoices.map(w=>`<option value="${escapeHTML(w.id)}" ${w.id===n.sopWorkflowId?'selected':''}>${escapeHTML(w.name)}</option>`).join('')}</select><p class="prop-hint">${escapeHTML((sopChoices.find(w=>w.id===n.sopWorkflowId)||{}).description||'这条 SOP 会作为子流程跑完，声明的产物交给下一步。')}</p></div><div class="fd-field"><label>交给下一步的产物</label><p class="prop-hint">子 SOP 成功后，下列文件会出现在本节点，供下一步读取。留空则收下子流程交出的全部产物。</p><div id="artifactContractRows">${artifactRowsHTML(n)}</div><button type="button" class="fd-btn ghost sm" id="addSOPHandoff">${flowPalette.icon('plus',13)}<span>增加下游产物</span></button></div>`:'';
    const scriptLines=(n.script||'').trim()?n.script.trim().split('\n').length:0;
    const scriptCountText=scriptLines?`已编写 ${scriptLines} 行 (${n.scriptLanguage==='python'?'Python':'Shell'})`:'未配置 (点击编写)';
    const scriptPreview=scriptLines?`<div class="script-snippet-preview" id="previewScriptNodeEditor" title="点击在代码编辑器中修改"><code>${escapeHTML(n.script.slice(0, 120))}${n.script.length>120?'…':''}</code></div>`:'';
    const scriptPane=n.type==='script'?`<div class="fd-row2"><div class="fd-field"><label>脚本语言</label><select class="select" data-p="scriptLanguage"><option value="shell" ${n.scriptLanguage!=='python'?'selected':''}>Shell</option><option value="python" ${n.scriptLanguage==='python'?'selected':''}>Python</option></select></div><div class="fd-field"><label>执行范围</label><select class="select" data-p="executionScope"><option value="run" ${n.executionScope!=='repositories'?'selected':''}>Run 工作区</option><option value="repositories" ${n.executionScope==='repositories'?'selected':''}>每个代码仓库</option></select></div></div><div class="fd-field"><label>脚本内容</label><button type="button" class="script-editor-launch-btn btn-open-code-editor" id="openScriptNodeEditor"><span>${flowPalette.icon('code',14)} 打开代码编辑器编写脚本</span><span class="script-btn-badge">${scriptCountText}</span></button>${scriptPreview}<p class="prop-hint">逐仓库执行时会自动进入每个仓库根目录，无需手写 cd。</p></div>`:'';
    const approvalPane=n.type==='approval'?`<div class="fd-field"><label>需要确认什么（可选）</label><textarea class="input" rows="4" data-p="taskPrompt" placeholder="例如：检查 MR 的改动范围和测试结果，确认可以上线">${escapeHTML(n.taskPrompt||'')}</textarea><p class="prop-hint">确认步骤会停在「待我确认」，批准后才继续；这段说明会和上下游一起展示给确认人。</p></div>`:'';
    const main=`<section class="fd-sec"><div class="fd-sec-h"><span class="fd-sec-t">主配置</span></div><div class="fd-sec-b">${n.type==='agent'?agentBindCard(n):''}${sopSelect}${scriptPane}${approvalPane}</div></section>`;
    const gates=n.type==='agent'?flowFold('gates','校验顺序',`${effectiveNodeArtifacts(n,workflowAgent(n.agentId)).length} 产物 · ${roleGates(workflowAgent(n.agentId)).length} 门禁`,nodeGatePane(n),false):'';
    const wantReject=state.flowWantReject??(n.onFailure==='reject');
    const targets=state.workflow.nodes.filter(x=>x.id!==n.id).map(x=>`<option value="${escapeHTML(x.id)}" ${x.id===n.rejectTo?'selected':''}>${escapeHTML(x.name)}</option>`).join('');
    const fail=`<section class="fd-sec"><div class="fd-sec-h"><span class="fd-sec-t">失败后</span></div><div class="fd-sec-b"><div class="fd-seg fd-seg-full" role="radiogroup" aria-label="失败后"><button type="button" role="radio" data-fail="fail" class="${wantReject?'':'active'}" aria-checked="${!wantReject}">${flowPalette.icon('stop',13)}<span>停下来</span></button><button type="button" role="radio" data-fail="reject" class="${wantReject?'active':''}" aria-checked="${wantReject}">${flowPalette.icon('rotate',13)}<span>打回重做</span></button></div>${wantReject?`<div class="fd-field"><label>打回到</label><select id="rejectTarget" class="select"><option value="">请选择步骤</option>${targets}</select><p class="prop-hint">重置目标步骤及其下游后重跑。也可以从节点的「失败打回」出口直接拖线。</p></div>`:'<p class="prop-hint">重试用尽后标记失败；运行页可以人工通过该节点继续下游。</p>'}</div></section>`;
    const metric=n.type==='sop'?'<p class="prop-hint">子 SOP 容器不重复计入阶段耗时；请在子流程的实际执行节点上标注统计阶段。</p>':`<div class="fd-field"><label>效能统计阶段</label><select class="select" data-p="metricStage"><option value="" ${!n.metricStage?'selected':''}>未标注（旧流程兼容推断）</option><option value="clarification" ${n.metricStage==='clarification'?'selected':''}>需求澄清</option><option value="development" ${n.metricStage==='development'?'selected':''}>研发</option><option value="testing" ${n.metricStage==='testing'?'selected':''}>测试</option><option value="none" ${n.metricStage==='none'?'selected':''}>不计入阶段耗时</option></select><p class="prop-hint">保存后的新 Run 会冻结此标记；历史 Run 不会被新配置改写。</p></div>`;
    const adv=flowFold('adv','高级','重试 · 返工 · 统计',`<div class="fd-row2"><div class="fd-field"><label>单轮最多试几次</label><input class="input" type="number" min="1" max="9" data-p="maxAttempts" value="${n.maxAttempts||1}"></div><div class="fd-field"><label>最多返工几轮</label><input class="input" type="number" min="1" max="20" data-p="maxReworks" value="${n.maxReworks||3}"></div></div><div class="fd-field"><label>返工策略</label><select class="select" data-p="retryStrategy"><option value="targeted_fix" ${n.retryStrategy!=='full_redo'?'selected':''}>只修失败项</option><option value="full_redo" ${n.retryStrategy==='full_redo'?'selected':''}>整步重做</option></select></div>${n.type==='script'?`<label class="fd-check"><input type="checkbox" data-p="runOnWorkspaceRebuild" ${n.runOnWorkspaceRebuild?'checked':''}><span>工作区重建时先执行（脚本需可重复执行）</span></label>`:''}${metric}`,false);
    host.innerHTML=`${flowDrawerHead(n,kind,FLOW_NODE_TYPES[kind].label)}<div class="fd-db">${main}${gates}${fail}${adv}</div><div class="fd-df"><span class="fd-df-hint"><kbd>F2</kbd> 重命名 · <kbd>⌘D</kbd> 复制 · <kbd>Del</kbd> 删除 · <kbd>⌘Z</kbd> 撤销</span></div>`;
    bindNodeProperties(host,n);
  }
  host.querySelector('#closeNodeDrawer')?.addEventListener('click',closeNodeDrawer);
  const del=host.querySelector('#deletePropertyNode');if(del)del.onclick=()=>removeNode(n.id);
  host.querySelectorAll('[data-fold]').forEach(d=>d.addEventListener('toggle',()=>{(state.flowFolds||(state.flowFolds={}))[d.dataset.fold]=d.open}));
  if(ro)host.querySelectorAll('.fd-db input,.fd-db select,.fd-db textarea,.fd-db button:not(#openBoundAgent):not(#openBoundAgentValidation),.fd-dh-name,#deletePropertyNode').forEach(el=>el.disabled=true);
  flowPalette.paint(host);
  const body=host.querySelector('.fd-db');if(body&&scroll)body.scrollTop=scroll;
  const refocus=focusKey&&host.querySelector(`[data-p="${focusKey}"]`);
  if(refocus){refocus.focus({preventScroll:true});if(caret)try{refocus.setSelectionRange(caret[0],caret[1])}catch{}}
  setFlowDrawer(true,n.id);
}
function bindNodeProperties(host,n){
  host.querySelectorAll('[data-p]').forEach(el=>el.oninput=()=>{
    const key=el.dataset.p;
    if(key!=='sopWorkflowId')flowHistory.record({merge:`p:${n.id}:${key}`});
    n[key]=el.type==='checkbox'?el.checked:el.type==='number'?Number(el.value):el.value;
    dirty();
    if(key==='agentId'){renderProperties();renderCanvas();return}
    renderCanvas();
  });
  const sopSel=host.querySelector('[data-p="sopWorkflowId"]');
  const renderedSOPId=n.sopWorkflowId;
  if(sopSel)sopSel.onchange=()=>{
    flowHistory.record();
    const choices=sopWorkflowChoices(state.workflow.id);
    const prev=choices.find(w=>w.id===renderedSOPId);
    const artifactKey=list=>JSON.stringify((list||[]).map(a=>[a.name||'',a.path||'']));
    n.sopWorkflowId=sopSel.value;
    const wf=choices.find(w=>w.id===n.sopWorkflowId);
    if(wf&&(!(n.artifactOutputs||[]).length||(prev&&artifactKey(n.artifactOutputs)===artifactKey(sopDeclaredArtifacts(prev)))))n.artifactOutputs=sopDeclaredArtifacts(wf);
    if(wf&&(!n.name||n.name==='SOP 节点'||n.name===prev?.name))n.name=wf.name;
    dirty();renderProperties();renderCanvas();
  };
  host.querySelector('#addSOPHandoff')?.addEventListener('click',()=>{flowHistory.record();n.artifactOutputs=n.artifactOutputs||[];n.artifactOutputs.push({name:'',path:'',mediaType:'text/markdown',required:true});renderProperties();dirty()});
  if(n.type==='sop')bindArtifactRows(host,n);
  const openBound=$('#openBoundAgent');if(openBound)openBound.onclick=()=>{const agent=workflowAgent(n.agentId);if(!agent)return;if(!catalogGeneric(state.workflow))switchView('agents');openAgentWizard(agent,2)};
  const openBoundValidation=$('#openBoundAgentValidation');if(openBoundValidation)openBoundValidation.onclick=()=>{const agent=workflowAgent(n.agentId);if(!agent)return;if(!catalogGeneric(state.workflow))switchView('agents');openAgentWizard(agent,3)};
  host.querySelectorAll('[data-fail]').forEach(b=>b.onclick=()=>{
    if(b.dataset.fail==='fail'){state.flowWantReject=false;if(!setRejectTo(n.id,'')&&n.onFailure==='reject'){n.onFailure='fail';dirty()}renderProperties();return}
    state.flowWantReject=true;renderProperties();host.querySelector('#rejectTarget')?.focus();
  });
  const reject=$('#rejectTarget');if(reject)reject.onchange=e=>{state.flowWantReject=null;setRejectTo(n.id,e.target.value)};
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
        flowHistory.record();
        n.script=newVal;
        if(newLang==='python'||newLang==='shell')n.scriptLanguage=newLang;
        renderProperties();renderCanvas();
        dirty();
      }
    });
  };
  if(openScriptBtn) openScriptBtn.onclick=triggerScriptEditor;
  if(previewScriptBtn) previewScriptBtn.onclick=triggerScriptEditor;
}
function dirty(){state.workflowDirty=true;renderFlowHeader();flowViewport.renderMinimap()}
function markWorkflowClean(){state.workflowDirty=false;state.workflowSaved=clone(state.workflow);renderFlowHeader();rememberOpenWorkflow()}
async function discardWorkflow(){
  if(!state.workflowDirty||!state.workflowSaved)return;
  if(!(await afConfirm({title:'放弃修改',message:'放弃未保存的修改，恢复到上次保存？',danger:true,confirmText:'放弃修改'})))return;
  state.workflow=clone(state.workflowSaved);state.selectedNode=null;state.connectFrom=null;state.connectCursor=null;state.renamingNode=null;markWorkflowClean();flowHistory.clear();renderAll();toast('已取消修改');
}
async function saveWorkflow(){
  if(catalogGeneric(state.workflow)&&!isPlatformAdmin()){toast('通用模板为只读，请先「复制到本项目」再修改');throw new Error('generic sop is read-only')}
  if(catalogForeign(state.workflow?.projectId)){toast('这是其他项目的共享 SOP，请先复制到本项目再改');throw new Error('shared sop is read-only')}
  try{const saved=await api('/api/workflows',{method:'PUT',body:JSON.stringify(state.workflow)});state.workflow=clone(saved);const i=state.data.workflows.findIndex(w=>w.id===saved.id);if(i>=0)state.data.workflows[i]=clone(saved);else state.data.workflows.unshift(clone(saved));markWorkflowClean();renderWorkflowMeta();renderSOPCatalog();renderCanvas();renderProperties();toast('SOP 草稿已保存');return saved}catch(e){if(e.message.includes('revision conflict')){toast('保存冲突：其他人已更新该 SOP，请重新选择后合并')}else if(e.message!=='shared sop is read-only'&&e.message!=='generic sop is read-only')toast(e.message);throw e}}
function sopSandboxChoices(){
  return (state.data.sandboxes||[]).filter(s=>s.id&&catalogVisible(s.projectId,s.shared));
}
function defaultSOPSandbox(choices=sopSandboxChoices()){
  return choices.find(s=>s.id==='local')||choices.find(s=>catalogOwned(s.projectId)&&s.driver==='docker')||choices.find(s=>s.driver==='docker')||choices[0];
}
function syncSOPSandboxSelection(){
  const select=$('#sopCreateSandbox'),template=(state.data.workflows||[]).find(w=>w.id===state.sopTemplate);
  const choices=sopSandboxChoices(),sameSource=select.dataset.source===state.sopTemplate&&select.dataset.project===state.projectFilter;
  const preferred=sameSource?select.value:(template?.sandboxProfileId||defaultSOPSandbox(choices)?.id||'');
  // Generic adoption already selects a project/shared fallback on the server.
  const selected=catalogGeneric(template)
    ?choices.find(s=>s.id===template.sandboxProfileId)||choices.find(s=>catalogOwned(s.projectId))||choices[0]
    :choices.find(s=>s.id===preferred);
  select.innerHTML=`<option value="">${escapeHTML(lfT('请选择运行沙箱','Select a sandbox'))}</option>`+choices.map(s=>`<option value="${escapeHTML(s.id)}">${escapeHTML(s.name||s.id)}${catalogForeign(s.projectId)?lfT(' · 共享',' · Shared'):''}</option>`).join('');
  select.value=selected?.id||'';
  select.dataset.source=state.sopTemplate;select.dataset.project=state.projectFilter;
  select.disabled=catalogGeneric(template)||!choices.length;
  select.onchange=()=>syncSOPSandboxSelection();
  $('#sopFinish').disabled=!selected;
  $('#sopSandboxHint').textContent=!choices.length
    ?lfT('当前项目没有可用的运行沙箱，请先配置或请管理员共享一个沙箱，再点击「刷新沙箱」。','No sandbox is available for this project. Configure one or ask an administrator to share one, then refresh sandboxes.')
    :!selected?lfT('模板原有沙箱不可用，请重新选择运行沙箱。','The template sandbox is unavailable. Select a replacement sandbox.')
    :catalogGeneric(template)?lfT('通用模板优先使用原有沙箱，不可用时自动选择本项目可用沙箱。复制后可在流程设置中修改。','Starter templates prefer their original sandbox and otherwise use an available project sandbox. You can change it in SOP settings after copying.')
    :lfT('选择此 SOP 使用的运行环境，创建后仍可在流程设置中修改。','Choose the execution environment for this SOP. You can change it in SOP settings later.');
}
async function refreshSOPSandboxes(){
  const button=$('#sopRefreshSandboxes');button.disabled=true;
  try{
    state.data.sandboxes=await api('/api/sandboxes');
    // Empty installations can acquire a first sandbox without losing form input.
    const select=$('#sopCreateSandbox');
    if(!select.value&&!state.data.workflows.some(w=>w.id===state.sopTemplate))delete select.dataset.source;
    syncSOPSandboxSelection();
  }catch(err){toast(err.message)}finally{button.disabled=false}
}
function newWorkflow(){
  const sandbox=defaultSOPSandbox();
  if(!sandbox)return toast(lfT('当前项目没有可用的运行沙箱，请先到「设置 → 沙箱」配置。','No sandbox is available for this project. Configure one in Settings → Sandboxes first.'));
  state.workflow={id:uid('sop'),name:'未命名 SOP',version:1,revision:0,status:'draft',projectId:state.projectFilter||'',shared:false,owner:'',tags:[],description:'',sandboxProfileId:sandbox.id,nodes:[],edges:[]};state.selectedNode=null;rememberOpenWorkflow();renderAll();dirty();
}
function openSOPWizard(){
  const f=$('#sopCreateForm');f.reset();delete $('#sopCreateSandbox').dataset.source;
  $('#sopRefreshSandboxes').onclick=refreshSOPSandboxes;
  const generic=(state.data.workflows||[]).filter(w=>catalogGeneric(w));
  const recommended=(state.data.workflows||[]).filter(w=>!catalogGeneric(w)&&['issue-fix-production','requirement-development-production'].includes(w.id));
  const fallback=(state.data.workflows||[]).find(w=>!catalogGeneric(w)&&w.status==='published');
  const choices=[...generic,...recommended,...(fallback&&!recommended.some(w=>w.id===fallback.id)?[fallback]:[])];
  state.sopTemplate=choices[0]?.id||'blank';
  $('#sopTemplateChoices').innerHTML=choices.map((w,index)=>`<label class="template-choice ${index===0?'active':''}"><input type="radio" name="template" value="${escapeHTML(w.id)}" ${index===0?'checked':''}><span class="template-icon">${catalogGeneric(w)?'通':w.id.includes('issue')?'修':w.id.includes('requirement')?'需':'▤'}</span><div><b>${escapeHTML(w.name)}</b><p>${escapeHTML(w.description||'复制生产模板')}</p><small>${catalogGeneric(w)?'通用 · 所有项目可用 · ':''}推荐 · ${w.nodes?.length||0} 个节点</small></div></label>`).join('')+`<label class="template-choice ${choices.length?'':'active'}"><input type="radio" name="template" value="blank" ${choices.length?'':'checked'}><span class="template-icon blank">＋</span><div><b>从空白开始</b><p>仅适合需要完全自定义的流程</p><small>高级方式 · 0 个节点</small></div></label>`;
  $('#sopTemplateChoices').querySelectorAll('input').forEach(input=>input.onchange=()=>{$$('#sopTemplateChoices .template-choice').forEach(x=>x.classList.toggle('active',x.contains(input)));state.sopTemplate=input.value;syncSOPSandboxSelection()});
  setSOPWizardStep(1);$('#sopCreateModal').classList.add('open');setTimeout(()=>f.elements.name.focus(),0)}
function setSOPWizardStep(step){const f=$('#sopCreateForm');syncSOPSandboxSelection();if(step>1&&state.sopWizardStep===1&&!f.elements.name.value.trim())return toast('先填写一个便于团队识别的流程名称');state.sopWizardStep=Math.max(1,Math.min(3,step));f.querySelectorAll('.wizard-pane').forEach(p=>p.classList.toggle('active',Number(p.dataset.step)===state.sopWizardStep));f.querySelectorAll('[data-step-target]').forEach(b=>b.classList.toggle('active',Number(b.dataset.stepTarget)<=state.sopWizardStep));$('#sopPrev').disabled=state.sopWizardStep===1;$('#sopNext').hidden=state.sopWizardStep===3;$('#sopNext').textContent=state.sopWizardStep===1?'选择流程模板':'查看创建摘要';$('#sopFinish').hidden=state.sopWizardStep===1;$('#sopFinish').textContent=state.sopWizardStep===2?'使用此模板创建':'创建并进入设计器';$('#sopStepHint').textContent=state.sopWizardStep===1?'填写基本信息即可开始':state.sopWizardStep===2?'选择最接近的模板，后续仍可修改':'确认后进入可视化设计器';if(state.sopWizardStep===3){const template=state.data.workflows.find(w=>w.id===state.sopTemplate);$('#sopCreateSummary').innerHTML=`<div><span>流程名称</span><b>${escapeHTML(f.elements.name.value)}</b></div><div><span>负责人</span><b>${escapeHTML(f.elements.owner.value||'暂未指定')}</b></div><div><span>创建方式</span><b>${template?`基于「${escapeHTML(template.name)}」`:'空白流程'}</b></div><div><span>${escapeHTML(lfT('运行沙箱','Run sandbox'))}</span><b>${escapeHTML(sopSandboxChoices().find(s=>s.id===f.elements.sandboxProfileId.value)?.name||lfT('未选择','Not selected'))}</b></div><div><span>初始结构</span><b>${template?.nodes?.length||0} 个节点 · ${template?.edges?.length||0} 条依赖</b></div>`}}
async function createSOPFromWizard(e){
  e.preventDefault();
  const f=e.currentTarget,template=(state.data.workflows||[]).find(w=>w.id===state.sopTemplate);
  const name=f.elements.name.value.trim(),description=f.elements.description.value.trim(),owner=f.elements.owner.value.trim(),tags=f.elements.tags.value.split(',').map(x=>x.trim()).filter(Boolean);
  const sandboxId=f.elements.sandboxProfileId.value;
  if(!sopSandboxChoices().some(s=>s.id===sandboxId)){
    syncSOPSandboxSelection();
    return toast($('#sopSandboxHint').textContent);
  }
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
      workflow.sandboxProfileId=sandboxId;
      delete workflow.createdAt;delete workflow.updatedAt;
      saved=await api('/api/workflows',{method:'PUT',body:JSON.stringify(workflow)});
    }
    state.data.workflows.unshift(saved);state.workflow=clone(saved);state.selectedNode=null;markWorkflowClean();
    $('#sopCreateModal').classList.remove('open');renderAll();switchView('flow');
    const missing=catalogGeneric(template)?genericRequirementRows(saved).filter(r=>!r.ok):[];
    toast(missing.length?`SOP 草稿已创建；${missing.length} 项运行依赖未配置（${missing.map(r=>r.label).join('、')}）`:'SOP 草稿已创建，请继续设计节点',missing.length?'':'ok')
  }catch(err){
    toast(/sandbox profile.*not found/i.test(err.message)
      ?lfT('所选运行沙箱已不可用，请点击「刷新沙箱」后重新选择。','The selected sandbox is no longer available. Refresh sandboxes and select another one.')
      :err.message);
  }}
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
  if(workflowPublishing||!workflowNeedsPublish())return;
  if(catalogGeneric(state.workflow)&&!isPlatformAdmin())return toast('通用模板为只读，请先「复制到本项目」再发布');
  workflowPublishing=true;renderFlowHeader();
  try{
    const id=state.workflow.id;
    if(state.workflowDirty||!state.workflow.revision)await saveWorkflow();
    const published=await api(`/api/workflows/${encodeURIComponent(id)}/publish`,{method:'POST',body:'{}'});
    const i=state.data.workflows.findIndex(w=>w.id===published.id);if(i>=0)state.data.workflows[i]=clone(published);
    if(state.workflow?.id===id){state.workflow=clone(published);markWorkflowClean();renderWorkflowMeta()}
    renderSOPCatalog();toast(`已发布 v${published.version}`);
  }catch(e){if(!e.message.includes('revision conflict'))toast(e.message)}
  finally{workflowPublishing=false;renderFlowHeader()}
}
