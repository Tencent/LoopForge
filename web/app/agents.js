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
  list.innerHTML=agents.map((a,index)=>{const initials=(a.name||'AI').replace(/[^A-Za-z\u4e00-\u9fa5]/g,'').slice(0,2),tone=['blue','purple','green','amber'][index%4];const exec=(state.data.executors||[]).find(x=>x.id===a.executorId);return`<article class="definition-card" data-agent-id="${escapeHTML(a.id)}"><header><span class="definition-avatar ${tone}">${escapeHTML(initials)}</span><div><h3>${escapeHTML(a.name)}</h3><code>${escapeHTML(a.id)}</code></div><span class="definition-version">v${a.version||1}</span></header><p>${escapeHTML(a.description||'尚未填写职责说明')}</p><div class="agent-runtime-summary"><span>${escapeHTML(a.model||exec?.model||exec?.name||'未选模型')}</span><span class="${a.loop?.enabled?'active':''}">${a.loop?.enabled?`${a.loop.maxIterations||1} 轮 Loop`:'单轮'}</span><span>${a.artifactOutputs?.length||0} 产物</span><span>${a.verification?.length||0} 门禁</span><span>${a.hooks?.length||0} Hook</span></div><div class="definition-assets"><span><i>R</i>${a.rules?.length||0} 规则</span><span><i>S</i>${a.skills?.length||0} 技能</span><span><i>M</i>${a.mcpServers?.length||0} MCP</span></div><footer><span>用于 <b>${used.get(a.id)||0}</b> 个流程节点</span>${shareBadge(a,'agent')}${catalogOwned(a.projectId)?`<button type="button" class="scope-share delete-action" data-delete-agent="${escapeHTML(a.id)}" ${a.shared?'disabled title="共享状态的角色不能删除，请先取消共享"':(used.get(a.id)?'disabled title="角色仍被流程引用，请先解除引用"':'')}>删除</button>`:''}<button type="button">编辑 <i>›</i></button></footer></article>`}).join('')||`<div class="catalog-empty"><span>AI</span><b>${empty}</b><p>调整搜索，或新建一个角色。</p></div>`;
  list.querySelectorAll('[data-share-agent]').forEach(btn=>btn.onclick=e=>{e.stopPropagation();toggleShare('agent',btn.dataset.shareAgent)});
  list.querySelectorAll('[data-clone-agent]').forEach(btn=>btn.onclick=e=>{e.stopPropagation();cloneAgent(btn.dataset.cloneAgent)});
  list.querySelectorAll('[data-delete-agent]').forEach(btn=>btn.onclick=e=>{e.stopPropagation();if(btn.disabled)return;deleteAgent(btn.dataset.deleteAgent)});
  list.querySelectorAll('[data-agent-id]').forEach(card=>card.onclick=()=>openAgentWizard(state.data.agents.find(a=>a.id===card.dataset.agentId)));
}
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
  agentModelAPI()?.paintItem?.(picker.querySelector('[data-model-toggle]'),item);
}
function renderAgentModelPicker(){
  const picker=$('#agentModelPicker');if(!picker)return;
  const items=agentModelItems(currentAgentExecutorId(),picker.querySelector('[name="modelChoice"]').value);
  const current=picker.querySelector('[name="modelChoice"]').value;
  const menu=picker.querySelector('.ai-model-menu');
  agentModelAPI()?.renderOptions?.(menu,items,current,id=>{
    picker.querySelector('[name="modelChoice"]').value=id;
    syncAgentModelCustomField();
    paintAgentPicker('#agentModelPicker',items.find(x=>x.id===id));
    menu.hidden=true;picker.querySelector('[data-model-toggle]').setAttribute('aria-expanded','false');
  });
  syncAgentModelCustomField();
  paintAgentPicker('#agentModelPicker',items.find(x=>x.id===current)||(current?{id:current,name:current}:null));
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
    if(open)render();
  };
}
function agentEffectiveModel(f){
  const choice=f?.elements?.modelChoice?.value;
  if(choice==null)return (f?.elements?.model?.value||'').trim();
  return choice===AGENT_CUSTOM_MODEL?(f?.elements?.model?.value||'').trim():choice;
}
function selectAgent(a,rerender=true){
  state.selectedAgent=a.id;state.agentDraft=null;
  const f=$('#agentForm');
  ['id','name','description','instructions'].forEach(k=>{if(f.elements[k])f.elements[k].value=a[k]||''});
  if(f.elements.shared)f.elements.shared.checked=!!a.shared;
  if(f.elements.executorId)f.elements.executorId.innerHTML=executorOptions(a.executorId||'codebuddy');
  const paintAgentModel=()=>applyAgentModelUI(a.executorId||f.elements.executorId?.value||'codebuddy',a.model||'',a.effort||'');
  paintAgentModel();loadExecutorModelCatalogs().then(paintAgentModel).catch(()=>{});
  if(f.elements.id)f.elements.id.readOnly=!!a.id;
  renderAgentPresets();renderAgentTaskBook(a);renderAgentGovernance(a);renderAgentAssetsPanel(a);renderAgentSummary();
  $('#agentVersion').textContent=`v${a.version||1}`;
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
const agentPresets=[{id:'developer',name:'开发实现',title:'研发工程师',desc:'根据任务书完成代码变更、验证和产物交付',instructions:'你是生产研发工程师。先读取规则、任务输入、上游冻结产物和相关源码；实施最小且完整的变更；运行构建、静态检查和测试；按节点产物契约提交真实证据。不得用自述代替磁盘产物和命令退码。'},{id:'reviewer',name:'独立评审',title:'独立代码审查员',desc:'独立检查正确性、安全性、兼容性和回归风险',instructions:'你是独立审查员，不是实现者。基于代码差异、上游产物和真实验证证据进行审查，明确阻断项、风险和结论；发现问题必须给出具体文件、原因与可执行修复建议。'},{id:'tester',name:'独立测试',title:'独立测试工程师',desc:'覆盖正常、边界、错误和回归路径',instructions:'你是独立测试工程师。根据需求与验收标准设计正常、边界、错误和回归用例，运行真实测试并记录命令、结果、覆盖率与数据清理证据。'},{id:'coordinator',name:'交付协调',title:'交付协调员',desc:'核对 PR、Checkers、Pipeline、验收与关闭证据',instructions:'你是交付协调员。只根据可核验的外部状态和冻结产物推进交付，确保 PR、Checkers、Pipeline、验收和关闭证据完整；任何关键事实缺失时停止并报告。'}];
function openAgentWizard(agent=null,initialStep=1){
  const open=a=>{
    const presetHost=$('#agentPresetHost');if(presetHost)presetHost.dataset.ready='';
    selectAgent(a||{id:'',name:'',version:1,projectId:state.projectFilter||'',shared:false,description:'',instructions:'',taskPrompt:{},rules:[],skills:[],mcpServers:[],hooks:[],loop:{enabled:false,maxIterations:3,onExhausted:'fail'},interventions:[],artifactOutputs:[],verification:[]},false);
    setAgentWizardStep(initialStep);$('#agentModal').classList.add('open');
    if(catalogGeneric(state.workflow))loadMCPMarket().then(()=>{const current=workflowAgent(state.selectedAgent)||state.agentDraft||a;if(current)renderAgentAssetsPanel(current)});
  };
  if(agent?.id && !agent.instructions){
    api(`/api/agents/${encodeURIComponent(agent.id)}`).then(full=>{const i=state.data.agents.findIndex(x=>x.id===full.id);if(i>=0)state.data.agents[i]=full;open(full)}).catch(e=>{toast(e.message,'err');open(agent)});
    return;
  }
  open(agent);
}
function applyAgentPreset(id){const p=agentPresets.find(x=>x.id===id),f=$('#agentForm');if(!p)return;f.elements.id.value=f.elements.id.value||`${id}-${Math.random().toString(36).slice(2,6)}`;f.elements.name.value=p.title;f.elements.description.value=p.desc;f.elements.instructions.value=p.instructions;$$('[data-agent-preset]').forEach(b=>b.classList.toggle('active',b.dataset.agentPreset===id));renderAgentSummary();toast(`已套用「${p.name}」模板，可直接保存或继续调整`)}
function setAgentWizardStep(step){
  const f=$('#agentForm');
  if(step>1&&state.agentWizardStep===1&&(!f.elements.id.value.trim()||!f.elements.name.value.trim()))return toast('选择模板或填写标识和名称');
  state.agentWizardStep=Math.max(1,Math.min(4,step));
  f.querySelectorAll('.wizard-pane').forEach(p=>p.classList.toggle('active',Number(p.dataset.step)===state.agentWizardStep));
  f.querySelectorAll('[data-wizard-steps="agent"] [data-step-target]').forEach(b=>{
    const n=Number(b.dataset.stepTarget);
    b.classList.toggle('active',n===state.agentWizardStep);
    b.classList.toggle('done',n<state.agentWizardStep);
  });
  $('#agentPrev').disabled=state.agentWizardStep===1;
  $('#agentNext').hidden=state.agentWizardStep===4;
  $('#agentNext').textContent=state.agentWizardStep===1?'下一步：任务':state.agentWizardStep===2?'下一步：门禁':'下一步：技能';
  const save=$('#agentSave');
  const foreign=agentWizardForeign(agentWizardSubject());
  save.hidden=foreign;
  save.textContent='保存';
  save.classList.toggle('primary',state.agentWizardStep===4);
  save.classList.toggle('ghost',state.agentWizardStep!==4);
  const cloneBtn=$('#agentClone');
  if(cloneBtn)cloneBtn.hidden=catalogGeneric(state.workflow)||!foreign;
  $('#agentStepHint').textContent=`${state.agentWizardStep} / 4`;
  if(state.agentWizardStep===3&&state.selectedAgent){const a=agentWizardSubject();renderAgentGovernance(a)}
  renderAgentSummary();
}
async function saveAgent(e){e.preventDefault();const f=e.currentTarget;const existing=state.data.agents.find(a=>a.id===f.elements.id.value);if(!catalogGeneric(state.workflow)&&existing&&catalogForeign(existing.projectId))return toast('这是其他项目的共享角色，请先复制到本项目再改');const checked=name=>[...f.querySelectorAll(`input[name="${name}"]:checked`)].map(x=>x.value);const draft=readAgentDraft(f);const artifacts=draft.artifactOutputs.map((x,i)=>({name:x.name||`artifact-${i+1}`,path:x.path||'result.md',mediaType:x.path?.endsWith('.json')?'application/json':(x.mediaType||'text/markdown'),required:!!x.required})),verification=draft.verification.map((gate,i)=>({id:gate.id||`gate-${i+1}`,name:gate.name||`门禁 ${i+1}`,mode:'command',stage:'after_execution',commands:(gate.commands||[]).map(c=>String(c||'').trim()).filter(Boolean),required:true,onFailure:['loop','reject','fail','human'].includes(gate.onFailure)?gate.onFailure:'loop',executionScope:gate.executionScope==='repositories'?'repositories':'run'})).filter(g=>g.commands.length),hooks=(draft.hooks||[]).map((h,i)=>({id:String(h.id||`hook-${i+1}`).trim(),name:String(h.name||`Hook ${i+1}`).trim(),event:h.event||'PreToolUse',matcher:String(h.matcher||'').trim(),language:h.language==='python'?'python':'shell',script:String(h.script||'').trim(),timeoutSec:Number(h.timeoutSec)||10,executionScope:h.executionScope==='repositories'?'repositories':'run'})).filter(h=>h.script),interventions=[];if(f.elements.interventionAfter?.checked)interventions.push({id:'human-review',name:'人工结果复核',stage:'after_execution',required:true});if(f.elements.interventionPublish?.checked)interventions.push({id:'publish-approval',name:'产物发布确认',stage:'before_publish',required:true});const a={id:f.elements.id.value.trim(),name:f.elements.name.value.trim(),version:(existing?.version||0)+1,projectId:existing?(existing.projectId||state.projectFilter||''):(state.projectFilter||''),shared:!!f.elements.shared?.checked,description:f.elements.description.value,instructions:f.elements.instructions.value,taskPrompt:draft.taskPrompt,executorId:f.elements.executorId?.value||'',model:agentEffectiveModel(f),effort:f.elements.effort?.value||'',rules:checked('rules'),skills:checked('skills'),mcpServers:checked('mcpServers'),platformMcp:!!f.elements.platformMcp?.checked,knowledgeBase:!!f.elements.knowledgeBase?.checked,hooks,loop:{enabled:!!f.elements.loopEnabled?.checked,maxIterations:Number(f.elements.loopMaxIterations?.value)||1,exitCondition:'all_gates_pass',onExhausted:f.elements.loopOnExhausted?.value||'fail',feedbackPrompt:f.elements.loopFeedbackPrompt?.value||''},interventions,artifactOutputs:artifacts,verification};if(!a.id||!a.name)return toast('标识和名称不能为空');if(catalogGeneric(state.workflow)){if(!isPlatformAdmin())return toast('只有平台管理员可以编辑通用模板角色');const bundled=workflowAgent(a.id);if(!bundled)return toast('该角色不属于当前通用模板');a.projectId='';a.shared=false;a.rules=bundled.rules||[];a.skills=bundled.skills||[];a.mcpServers=f.querySelectorAll('input[name="mcpServers"]').length?checked('mcpServers'):(bundled.mcpServers||[]);a.hooks=Array.isArray(bundled.hooks)?bundled.hooks:a.hooks;a.version=bundled.version||0;try{const saved=await api(`/api/workflows/${encodeURIComponent(state.workflow.id)}/agents`,{method:'PUT',body:JSON.stringify(a)});const list=state.genericTemplateAgents||[];const gi=list.findIndex(x=>x.id===saved.id);if(gi>=0)list[gi]=saved;else list.push(saved);state.genericTemplateAgents=list;state.selectedAgent=saved.id;state.agentDraft=null;$('#agentModal').classList.remove('open');renderCanvas();renderProperties();toast('通用模板角色已保存，所有项目下次运行生效');}catch(err){toast(err.message)}return;}try{await api('/api/agents',{method:'PUT',body:JSON.stringify(a)});const i=state.data.agents.findIndex(x=>x.id===a.id);if(i>=0)state.data.agents[i]=a;else state.data.agents.push(a);if(a.shared){(state.data.rules||[]).forEach(item=>{if(a.rules.includes(item.id)&&item.projectId===a.projectId)item.shared=true});(state.data.skills||[]).forEach(item=>{if(a.skills.includes(item.id)&&item.projectId===a.projectId)item.shared=true})}state.selectedAgent=a.id;state.agentDraft=null;$('#agentModal').classList.remove('open');renderAgents();renderCanvas();toast('Agent 新版本已保存')  }catch(err){toast(err.message)}}
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
