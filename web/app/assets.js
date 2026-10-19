function assetScopeKey(type){return ({rules:'ruleScope',skills:'skillScope',dockerfiles:'dockerfileScope',environments:'environmentScope',sandboxes:'sandboxScope'})[type]||'ruleScope'}
function isProjectSharedAsset(type){return ['rules','skills','dockerfiles','environments','sandboxes'].includes(type)}
function renderAssets(){
  const dedicated=isProjectSharedAsset(state.assetType);
  const definitionScope=state[assetScopeKey(state.assetType)];
  const copies={
    rules:['能力资产','规则管理','维护当前项目的行为约束和执行规则。','＋ 新建规则'],
    skills:['能力资产','技能管理','维护当前项目可供角色使用的技能和附加文件。','＋ 新建技能'],
    dockerfiles:['运行环境','Dockerfile','维护当前项目的容器构建定义。','＋ 新建 Dockerfile'],
    environments:['运行环境','环境安装脚本','维护安装到隔离执行环境中的工具与依赖。','＋ 新建安装脚本'],
    sandboxes:['运行环境','沙箱管理','维护当前项目的运行隔离方式及环境依赖。','＋ 新建沙箱']
  };
  const pageCopy=[...(copies[state.assetType]||copies.dockerfiles)];if(definitionScope==='shared')pageCopy[2]=state.assetType==='rules'?'查看其他项目共享的规则；共享规则只读，可直接绑定到本项目角色。':state.assetType==='skills'?'查看其他项目共享的技能；共享技能只读，可直接绑定到本项目角色。':'查看其他项目共享的环境资产；共享资产只读，可直接用于本项目。';
  if($('#assetPageEyebrow'))$('#assetPageEyebrow').textContent=pageCopy[0];if($('#assetPageTitle'))$('#assetPageTitle').textContent=pageCopy[1];if($('#assetPageDescription'))$('#assetPageDescription').textContent=pageCopy[2];if($('#newAsset'))$('#newAsset').textContent=pageCopy[3];
  const groupName=['rules','skills'].includes(state.assetType)?'能力资产':'环境资产';if($('#assetScopeToolbar'))$('#assetScopeToolbar').hidden=!dedicated;if($('#assetScopeNote'))$('#assetScopeNote').textContent=definitionScope==='shared'?`查看其他项目共享的${groupName}`:`管理当前项目的${groupName}`;if($('#newAsset'))$('#newAsset').hidden=dedicated&&definitionScope==='shared';
  $$('[data-asset-scope]').forEach(btn=>btn.classList.toggle('active',btn.dataset.assetScope===definitionScope));
  const allItems=(state.data[state.assetType]||[]).filter(item=>definitionScope==='shared'?catalogShared(item.projectId,item.shared):catalogOwned(item.projectId));
  const items=allItems;
  const list=$('#assetList');
  const filter='';
  const cards=items.map(a=>{
    const blocked=catalogOwned(a.projectId)?assetDeleteBlocked(a):'';
    const deleteBtn=dedicated&&catalogOwned(a.projectId)?`<button type="button" class="scope-share delete-action" data-delete-asset="${escapeHTML(a.id)}" ${blocked?`disabled title="${escapeHTML(blocked)}"`:'title="删除"'}>删除</button>`:'';
    return `<div class="agent-card ${state.selectedAsset===a.id?'active':''}" data-asset-id="${a.id}"><div class="agent-card-top"><h3>${escapeHTML(a.name)}</h3><span class="pill">${assetKind()}</span></div><p>${escapeHTML(a.description||'暂无说明')}</p><div class="chips">${assetChips(a)}${dedicated&&catalogOwned(a.projectId)?`<button type="button" class="scope-share share-action ${a.shared?'is-shared':''}" data-share-asset="${escapeHTML(a.id)}" aria-label="${a.shared?'取消共享':'共享给其他项目'}">${a.shared?'⊘ 取消共享':'↗ 共享给其他项目'}</button>${deleteBtn}`:dedicated?`<span class="scope-tag">来自 ${escapeHTML((state.data.projects||[]).find(p=>p.id===a.projectId)?.name||a.projectId||'平台')}</span>`:''}</div></div>`;
  }).join('');
  const sharedEmpty=dedicated&&definitionScope==='shared';const shareHint=['rules','skills'].includes(state.assetType)?'请在资产所属项目中共享，或共享一个引用它的角色。':'请在资产所属项目中共享；共享 SOP 时也会自动共享其环境依赖。';const emptyState=!cards&&state.selectedAsset!=='__new__';list.closest('.agent-layout')?.classList.toggle('is-empty',emptyState);
  list.innerHTML=filter+(cards||(sharedEmpty?`<div class="asset-empty panel"><b>其他项目还没有共享${assetKindLabel()}</b><p>${shareHint}</p></div>`:`<div class="asset-empty panel"><b>当前项目还没有${/^[A-Za-z]/.test(assetKindLabel())?' ':''}${assetKindLabel()}</b><p>${assetEmptyHint()}</p><button type="button" class="button primary" data-empty-new>${escapeHTML(pageCopy[3])}</button></div>`));
  list.querySelector('[data-empty-new]')?.addEventListener('click',()=>$('#newAsset').click());
  list.querySelectorAll('[data-share-asset]').forEach(button=>button.onclick=e=>{e.stopPropagation();toggleAssetShare(button.dataset.shareAsset)});
  list.querySelectorAll('[data-delete-asset]').forEach(button=>button.onclick=e=>{e.stopPropagation();if(button.disabled)return;deleteAsset(button.dataset.deleteAsset)});
  list.querySelectorAll('[data-asset-id]').forEach(el=>el.onclick=()=>{state.selectedAsset=el.dataset.assetId;renderAssets()});
  let asset=items.find(x=>x.id===state.selectedAsset);if(!asset&&state.selectedAsset!=='__new__'&&items[0]){state.selectedAsset=items[0].id;asset=items[0]}
  if(state.selectedAsset==='__new__')asset={id:'',name:'',description:''};
  renderAssetForm(asset);
}
async function toggleAssetShare(id){const item=(state.data[state.assetType]||[]).find(x=>x.id===id);if(!item||!catalogOwned(item.projectId))return toast('只能共享本项目的资产');try{const saved={...item,shared:!item.shared};await api(`/api/${state.assetType}`,{method:'PUT',body:JSON.stringify(saved)});Object.assign(item,saved);renderAssets();const agent=state.data.agents.find(a=>a.id===state.selectedAgent)||state.agentDraft;if(agent)renderAgentAssetsPanel(agent);toast(saved.shared?'已共享，其他项目可绑定':'已取消共享')}catch(e){toast(e.message)}}
function assetEmptyHint(){return ({rules:'规则是写给 Agent 的行为约束，例如编码规范、禁止事项。建好后在「角色定义」里绑定到角色。',skills:'技能是 Agent 可按需加载的说明和附加文件，例如某个框架的操作手册。建好后在「角色定义」里绑定到角色。',dockerfiles:'Dockerfile 定义沙箱容器的镜像。建好后在「沙箱管理」里选用；也可以直接用「共享」里的现成配置。',environments:'安装脚本在沙箱初始化时把工具装进隔离环境，例如指定版本的 Go、PHP、Node。建好后在「沙箱管理」里选用。',sandboxes:'沙箱决定 SOP 节点在什么隔离环境里执行，由 Dockerfile 和安装脚本组合而成。建好后在流程设置里选用；也可以直接用「共享」里的现成配置。'})[state.assetType]||''}
function assetKindLabel(){return ({rules:'规则',skills:'技能',dockerfiles:'Dockerfile',environments:'环境安装脚本',sandboxes:'沙箱'})[state.assetType]||'资产'}
function assetRefKindLabel(){return ({rules:'角色',skills:'角色',dockerfiles:'沙箱',environments:'沙箱',sandboxes:'流程'})[state.assetType]||'引用'}
function assetReferenceNames(a){
  if(!a?.id)return [];
  if(state.assetType==='rules')return (state.data.agents||[]).filter(x=>(x.rules||[]).includes(a.id)).map(x=>x.name||x.id);
  if(state.assetType==='skills')return (state.data.agents||[]).filter(x=>(x.skills||[]).includes(a.id)).map(x=>x.name||x.id);
  if(state.assetType==='dockerfiles')return (state.data.sandboxes||[]).filter(x=>x.dockerfileId===a.id).map(x=>x.name||x.id);
  if(state.assetType==='environments')return (state.data.sandboxes||[]).filter(x=>x.environmentId===a.id).map(x=>x.name||x.id);
  if(state.assetType==='sandboxes')return (state.data.workflows||[]).filter(x=>!catalogGeneric(x)&&x.sandboxProfileId===a.id).map(x=>x.name||x.id);
  return [];
}
function assetDeleteBlocked(a){
  if(!a?.id)return '无法删除';
  if(state.assetType==='sandboxes'&&(a.id==='local'||a.id==='generic-docker'))return `内置沙箱 ${a.id} 不能删除`;
  if(state.assetType==='dockerfiles'&&a.id==='generic-backend')return '内置 Dockerfile generic-backend 不能删除';
  if(a.shared)return `共享状态的${assetKindLabel()}不能删除，请先取消共享`;
  const refs=assetReferenceNames(a);
  if(refs.length)return `${assetKindLabel()}仍被 ${refs.length} 个${assetRefKindLabel()}引用（${refs.join('、')}），请先解除引用`;
  return '';
}
async function deleteAsset(id){
  const item=(state.data[state.assetType]||[]).find(x=>x.id===id);
  if(!item)return toast('资产不存在或已删除');
  if(!catalogOwned(item.projectId))return toast('只能删除本项目的资产');
  const blocked=assetDeleteBlocked(item);
  if(blocked)return toast(blocked);
  const label=assetKindLabel();
  if(!await afConfirm({title:`删除${label}确认`,message:`确认删除${label}「${item.name}」？此操作不可恢复。`,confirmText:'确认删除',isDanger:true}))return;
  try{
    await api(`/api/${state.assetType}?id=${encodeURIComponent(id)}`,{method:'DELETE'});
    state.data[state.assetType]=(state.data[state.assetType]||[]).filter(x=>x.id!==id);
    if(state.selectedAsset===id)state.selectedAsset=null;
    renderAssets();
    toast(`${label}已删除`);
  }catch(e){toast(e.message||'删除失败')}
}
function assetKind(){return assetKindLabel()}
function assetChips(a){if(state.assetType==='rules'){const bound=(a.workflowIds||[]).length;return `<span class="chip">优先级 ${a.priority||0}</span>${a.global?`<span class="chip">全局${bound?` · ${bound} 个 SOP`:''}</span>`:''}`}if(state.assetType==='skills'){const count=Object.keys(a.files||{}).length+(a.fileRefs||[]).length;return `<span class="chip">${count} files</span>`}if(state.assetType==='dockerfiles')return `<span class="chip">${(a.content||'').split('\n').length} lines</span>`;if(state.assetType==='environments')return `<span class="chip">${Object.keys(a.variables||{}).length} vars</span>`;return `<span class="chip">${escapeHTML(a.driver||'local')}</span>`}
function ruleGlobalFields(a){const on=!!a.global;return `<div class="rule-global-block"><label class="rule-global-row"><span><b>作为全局规则</b><small>开启后选择要生效的 SOP。该 SOP 的每个节点执行时都会带上这条规则。</small></span><input name="global" type="checkbox" class="rule-global-input" role="switch" ${on?'checked':''}></label><div id="ruleSOPPickerWrap" class="rule-global-sops" ${on?'':'hidden'}><label>应用到 SOP</label><div id="ruleSOPPicker" class="repo-picker"></div><div class="json-help">从下拉框添加，点击标签移除。选中的 SOP 在运行时，其全部节点都会注入本规则。</div></div></div>`}
function ruleSOPChoices(){return runnableWorkflowOptions()}
function selectedRuleWorkflowIds(){try{const ids=JSON.parse($('#ruleSOPPicker')?.dataset.ids||'[]');return Array.isArray(ids)?ids:[]}catch{return []}}
function setRuleWorkflowIds(ids){const host=$('#ruleSOPPicker');if(!host)return;const known=new Set(ruleSOPChoices().map(w=>w.id));host.dataset.ids=JSON.stringify([...new Set((ids||[]).map(id=>String(id||'').trim()).filter(id=>id&&known.has(id)))]);renderRuleSOPPicker()}
function renderRuleSOPPicker(){const host=$('#ruleSOPPicker');if(!host)return;const sops=ruleSOPChoices();const selected=selectedRuleWorkflowIds();const chosen=new Set(selected);const leftover=sops.filter(w=>!chosen.has(w.id));const tags=selected.map(id=>{const sop=sops.find(w=>w.id===id);const name=sop?.name||id;const nodes=sop?.nodes?.length||0;const label=nodes?`${name} · ${nodes} 个节点`:name;return `<button type="button" class="repo-picker-tag" data-remove-rule-sop="${escapeHTML(id)}" title="点击移除">${escapeHTML(label)}<span aria-hidden="true">×</span></button>`}).join('');const options=leftover.map(w=>`<option value="${escapeHTML(w.id)}">${escapeHTML(w.name)}${w.nodes?.length?` · ${w.nodes.length} 个节点`:''}</option>`).join('');const placeholder=!sops.length?'暂无可选 SOP':(!leftover.length?'已全部选择':'＋ 添加 SOP…');host.innerHTML=`<div class="repo-picker-field">${tags}<select class="repo-picker-select"${leftover.length?'':' disabled'}><option value="">${placeholder}</option>${options}</select></div>`;host.querySelectorAll('[data-remove-rule-sop]').forEach(b=>b.onclick=()=>setRuleWorkflowIds(selected.filter(id=>id!==b.dataset.removeRuleSop)));const sel=host.querySelector('select');if(sel)sel.onchange=()=>{if(!sel.value)return;setRuleWorkflowIds([...selected,sel.value])}}
function bindRuleGlobalPicker(a){const host=$('#ruleSOPPicker');if(!host)return;const known=new Set(ruleSOPChoices().map(w=>w.id));host.dataset.ids=JSON.stringify((Array.isArray(a.workflowIds)?a.workflowIds:[]).filter(id=>known.has(id)));const toggle=document.querySelector('#assetForm [name="global"]');const wrap=$('#ruleSOPPickerWrap');const sync=()=>{if(wrap)wrap.hidden=!(toggle&&toggle.checked)};if(toggle)toggle.onchange=sync;sync();renderRuleSOPPicker()}
function dockerfileOptions(selected){const items=(state.data.dockerfiles||[]).filter(x=>catalogVisible(x.projectId,x.shared));if(!items.length)return `<option value="">请先创建 Dockerfile</option>`;return `<option value="">选择 Dockerfile</option>`+items.map(x=>`<option value="${escapeHTML(x.id)}" ${x.id===selected?'selected':''}>${escapeHTML(x.name||x.id)}${catalogForeign(x.projectId)?' · 共享':''}</option>`).join('')}
function environmentOptions(selected){const items=(state.data.environments||[]).filter(x=>catalogVisible(x.projectId,x.shared));return `<option value="">不选择（可用 workspace 的 agentflow-environment）</option>`+items.map(x=>`<option value="${escapeHTML(x.id)}" ${x.id===selected?'selected':''}>${escapeHTML(x.name||x.id)}${catalogForeign(x.projectId)?' · 共享':''}</option>`).join('')}
function syncSandboxDriverFields(select){const driver=select?.value||'local';select?.form?.querySelectorAll('[data-sandbox-drivers]').forEach(el=>{el.hidden=!el.dataset.sandboxDrivers.split(' ').includes(driver)})}
function resolveAssetSaveIdentity(items,selectedID,requestedID){if(selectedID!=='__new__'){const existing=(items||[]).find(item=>item.id===selectedID);return existing?{id:existing.id,existing,created:false}:{error:'选中的资产已不存在，请刷新后重试'}}if((items||[]).some(item=>item.id===requestedID))return {error:'该标识已存在，请从左侧选择后修改'};return {id:requestedID,existing:null,created:true}}
function renderAssetForm(a){const f=$('#assetForm');if(!a){f.innerHTML='<div class="empty-properties"><b>选择或新建资产</b></div>';return}const creating=state.selectedAsset==='__new__';const owned=creating||catalogOwned(a.projectId);const common=`<div class="panel-heading"><h2>${creating?'新建':owned?'编辑':'查看'}${/^[A-Za-z]/.test(assetKind())?' ':''}${assetKind()}</h2>${owned?'':'<span class="pill">只读</span>'}</div><label>标识</label><input name="id" value="${escapeHTML(a.id||'')}" required ${creating?'':'readonly aria-readonly="true"'}>${creating?'':'<div class="asset-field-hint">标识用于资产引用，创建后不可修改。</div>'}<label>名称</label><input name="name" value="${escapeHTML(a.name||'')}" required><label>描述</label><input name="description" value="${escapeHTML(a.description||'')}">`;let fields='';
  if(state.assetType==='rules')fields=`<label>优先级</label><input name="priority" type="number" value="${a.priority||100}"><label>规则内容</label><textarea name="content" rows="12" placeholder="写入必须遵守的行为和约束">${escapeHTML(a.content||'')}</textarea>${ruleGlobalFields(a)}`;
  if(state.assetType==='skills')fields=`${creating?`<div class="json-help">先创建技能，再上传文件夹或 zip 初始化文件。</div>`:''}
  <div class="skill-upload-block">
    <div class="skill-upload-head"><label>文件</label>${creating?'':skillFileSummary(a)}</div>
    <div class="skill-upload-actions">
      <input type="file" id="skillFolderPick" webkitdirectory multiple style="display:none">
      <input type="file" id="skillZipPick" accept=".zip,application/zip,application/x-zip-compressed" style="display:none">
      <input type="file" id="skillMdPick" accept=".md,.markdown,text/markdown" style="display:none">
      <button type="button" class="button skill-upload-btn" id="skillFolderBtn">选择文件夹</button>
      <button type="button" class="button skill-upload-btn" id="skillZipBtn">上传 .zip 压缩包</button>
      <button type="button" class="button skill-upload-btn skill-upload-btn-overwrite" id="skillMdBtn">上传 SKILL.md</button>
    </div>
    <div id="skillUploadStatus" class="skill-upload-status">${state.skillUploadStatus||''}</div>
    <div class="json-help">每次上传都是<b>完全替换</b>：文件夹 / zip 替换为整批文件；单独上传 SKILL.md 会先弹出确认，然后该技能<b>只保留这一份 SKILL.md</b>。单文件 ≤1MB，最多 500 个，单技能 ≤64MB。<b>zip 自动解包并保留可执行位</b>；markdown / 代码 / 图片可直接预览。</div>
    ${creating?'<div class="json-help">先保存技能，再上传文件。</div>':skillFileRowsHTML(a)}
  </div>`;
  if(state.assetType==='dockerfiles')fields=`<label>Dockerfile</label><textarea name="content" rows="16" placeholder="FROM node:22-bookworm-slim">${escapeHTML(a.content||'')}</textarea><label>构建上下文文件 JSON</label><textarea name="files" rows="5" placeholder='{"packages.txt":"git\\ncurl"}'>${escapeHTML(JSON.stringify(a.files||{},null,2))}</textarea><div class="json-help">正文和附加文件保存在这条 Dockerfile 资产里，写入数据库，和 <code>install.sh</code> 一样。不要指向仓库目录或现成镜像名。沙箱初始化时用库里这份 <code>docker build</code> 出一次性镜像。</div>`;
  if(state.assetType==='environments')fields=`<label>环境变量 JSON</label><textarea name="variables" rows="5" placeholder='{"PHP_VERSION":"7.4.33"}'>${escapeHTML(JSON.stringify(a.variables||{},null,2))}</textarea><label>PATH 条目 <small>相对 $PREFIX，每行一个，默认 bin</small></label><textarea name="pathEntries" rows="3" placeholder="bin">${escapeHTML((a.pathEntries||[]).join('\n'))}</textarea><label>install.sh</label><textarea name="installScript" rows="18" placeholder="#!/bin/sh&#10;set -e&#10;mkdir -p &quot;$PREFIX/bin&quot;&#10;curl -fsSL https://mirror.example/php.tgz | tar -xz -C &quot;$PREFIX&quot;">${escapeHTML(a.installScript||'')}</textarea><div class="json-help">脚本保存在环境资产里。Studio 保存即写库；下次沙箱初始化会按库里这份执行，把工具装进 <code>$PREFIX</code>。不要往宿主机写。脚本内容变化后会重新安装。</div>`;
  if(state.assetType==='sandboxes')fields=`<label>隔离方式</label><select name="driver" class="select"><option value="local" ${!['seatbelt','bwrap','docker'].includes(a.driver)?'selected':''}>local（本机直接执行）</option><option value="seatbelt" ${a.driver==='seatbelt'?'selected':''}>seatbelt（macOS 轻量隔离）</option><option value="bwrap" ${a.driver==='bwrap'?'selected':''}>bwrap（Linux 轻量隔离）</option><option value="docker" ${a.driver==='docker'?'selected':''}>docker（OCI 容器）</option></select><label>环境变量 JSON</label><textarea name="environment" rows="5" placeholder='{"NAME":"value"}'>${escapeHTML(JSON.stringify(a.environment||{},null,2))}</textarea><div class="json-help">叠加到沙箱基础环境并随资产持久化；不要在这里保存凭证。平台凭证、HOME/TMPDIR 和遥测变量拥有更高优先级。</div><div data-sandbox-drivers="local" class="json-help">直接在本机 workspace 执行，并继承受控的宿主进程环境。</div><div data-sandbox-drivers="seatbelt bwrap"><label>执行环境</label><select name="environmentId" class="select">${environmentOptions(a.environmentId)}</select><div class="json-help">选中后沙箱会执行该环境的 <code>install.sh</code>，把工具装进隔离的 <code>$PREFIX</code>，再用 PATH 切换。未选择时，若 workspace 存在 <code>agentflow-environment/</code> 也会自动启用。</div></div><div data-sandbox-drivers="docker"><label>Dockerfile</label><select name="dockerfileId" class="select">${dockerfileOptions(a.dockerfileId)}</select><div class="json-help">沙箱只引用这条目录资产。Dockerfile 正文在数据库里，和 <code>install.sh</code> 一样；初始化时用库里的内容构建临时镜像，结束后删除容器和镜像。</div><label class="toggle-row"><input name="readOnlyRootFS" type="checkbox" ${a.readOnlyRootFS?'checked':''}> 容器根文件系统只读（增强隔离）</label><div class="json-help">仅限制镜像自身的 /usr、/etc 等目录；/workspace 仍可写，/tmp 使用临时内存盘。部分需要写入镜像目录的工具可能不兼容，默认建议关闭。</div></div>`;
  f.innerHTML=common+fields+`<div class="form-footer">${!creating&&catalogOwned(a.projectId)?`<button type="button" class="button danger-outline asset-delete" id="deleteAssetBtn" ${assetDeleteBlocked(a)?`disabled title="${escapeHTML(assetDeleteBlocked(a))}"`:''}>删除</button>`:''}<button class="button dark" type="submit">${creating?'创建资产':'保存修改'}</button></div>`;
  if(state.assetType==='rules')bindRuleGlobalPicker(a);
  if(isProjectSharedAsset(state.assetType)&&catalogForeign(a.projectId)){f.querySelectorAll('input,textarea,select,button[type="submit"],.repo-picker-tag').forEach(el=>el.disabled=true);f.querySelector('.form-footer').innerHTML='<span class="scope-tag">共享资产只读，可在当前项目直接使用</span>'}
  const deleteBtn=$('#deleteAssetBtn');if(deleteBtn)deleteBtn.onclick=()=>deleteAsset(a.id);
  if(state.assetType==='sandboxes'){f.elements.driver.onchange=()=>syncSandboxDriverFields(f.elements.driver);syncSandboxDriverFields(f.elements.driver)}
  if(state.assetType==='skills'&&state.selectedAsset!=='__new__'){
    const readonly=catalogForeign(a.projectId);
    skillUploadButtons(readonly);
    const actions=f.querySelector('.skill-upload-actions');
    if(actions)actions.hidden=readonly;
  }
}
function fmtBytes(n){n=Number(n)||0;if(n<1024)return n+' B';if(n<1024*1024)return (n/1024).toFixed(1)+' KB';return (n/1024/1024).toFixed(1)+' MB'}
function skillFileSummary(a){
  const refs=a.fileRefs||[];
  const total=refs.reduce((s,r)=>s+(r.size||0),0);
  return `<span class="chip">${refs.length} 个文件 · ${fmtBytes(total)}</span>`;
}
function skillFileIcon(path){
  const ext=path.toLowerCase().split('.').pop()||'';
  if(['md','markdown'].includes(ext))return 'MD';
  if(['png','jpg','jpeg','gif','webp','svg'].includes(ext))return 'IMG';
  if(['json','yaml','yml','toml','xml'].includes(ext))return '{}';
  if(['sh','bash','py','js','ts','go','rs','css','html','sql','lua','rb','java','c','cpp','h'].includes(ext))return '<>';
  if(['zip','tar','gz','tgz','bz2','xz'].includes(ext))return 'ZIP';
  return '📄';
}
function skillFilePreviewable(path){
  const ext=path.toLowerCase().split('.').pop()||'';
  return ['md','markdown','txt','log','json','yaml','yml','xml','sh','bash','py','js','ts','go','rs','css','html','sql','lua','rb','java','c','cpp','h','png','jpg','jpeg','gif','webp','svg'].includes(ext);
}
function skillFileLang(path){
  const ext=path.toLowerCase().split('.').pop()||'';
  const m={sh:'bash',bash:'bash',py:'python',js:'javascript',ts:'typescript',go:'go',rs:'rust',rb:'ruby',lua:'lua',java:'java',c:'c',cpp:'cpp',h:'c',yaml:'yaml',yml:'yaml',toml:'toml',xml:'xml',json:'json',sql:'sql',md:'markdown',markdown:'markdown',html:'html',css:'css',svg:'xml'};
  return m[ext]||'TEXT';
}
function selectedSkill(){return (state.data.skills||[]).find(x=>x.id===state.selectedAsset)||null}
function skillWriteBlocked(){const skill=selectedSkill();return !skill||catalogForeign(skill.projectId)}
function refreshSkillFileTree(){
  const skill=selectedSkill();
  const host=$('#skillFileTree');
  if(!host||!skill)return;
  host.outerHTML=skillFileRowsHTML(skill);
}
function skillFileRowsHTML(a){
  const refs=a.fileRefs||[];
  if(!refs.length)return '<p class="empty-inline">还没有文件。选择文件夹或 zip 后自动上传。</p>';
  const base=`/api/skills/${encodeURIComponent(a.id)}/files`;
  const readonly=catalogForeign(a.projectId);
  if(!(state.skillTreeCollapsed instanceof Set))state.skillTreeCollapsed=new Set();
  // 构建 VS Code 风格文件树：目录可折叠，文件带预览/下载；本项目技能才显示删除
  const root={dirs:{},files:[]};
  refs.forEach(r=>{
    const segs=r.path.split('/');
    let node=root;
    for(let i=0;i<segs.length-1;i++){
      const seg=segs[i];
      if(!node.dirs[seg])node.dirs[seg]={dirs:{},files:[]};
      node=node.dirs[seg];
    }
    node.files.push(r);
  });
  const countFiles=node=>{
    let n=node.files.length;
    Object.values(node.dirs).forEach(d=>n+=countFiles(d));
    return n;
  };
  const html=[];
  (function render(node,depth,prefix){
    Object.keys(node.dirs).sort((x,y)=>x.localeCompare(y)).forEach(d=>{
      const child=node.dirs[d];
      const full=prefix+d;
      const collapsed=state.skillTreeCollapsed.has(full);
      html.push(`<div class="skill-tree-row" data-tree-toggle="${escapeHTML(full)}" style="padding-left:${depth*14+4}px"><span class="skill-tree-caret">${collapsed?'▸':'▾'}</span><span class="skill-file-icon">📁</span><code class="skill-tree-dirname">${escapeHTML(d)}/</code><span class="chip">${countFiles(child)}</span></div>`);
      if(!collapsed){
        html.push('<div class="skill-tree-children">');
        render(child,depth+1,full+'/');
        html.push('</div>');
      }
    });
    node.files.sort((x,y)=>x.path.localeCompare(y.path)).forEach(r=>{
      const name=r.path.split('/').pop();
      const url=`${base}/${r.path.split('/').map(encodeURIComponent).join('/')}`;
      const canPreview=skillFilePreviewable(r.path);
      html.push(`<div class="skill-file-row" style="padding-left:${depth*14+18}px" title="${escapeHTML(r.path)}"><span class="skill-file-icon">${skillFileIcon(r.path)}</span><code class="skill-file-path">${escapeHTML(name)}</code><span class="chip">${fmtBytes(r.size)}</span>${(r.mode&0o111)?'<span class="chip">可执行</span>':''}<button type="button" class="skill-file-preview" data-skill-file-preview="${escapeHTML(r.path)}" title="预览" ${canPreview?'':'disabled'}>预览</button><a href="${url}" download title="下载">下载</a>${readonly?'':`<button type="button" class="skill-file-del" data-skill-file-del="${escapeHTML(r.path)}" title="删除">×</button>`}</div>`);
    });
  })(root,0,'');
  return `<div id="skillFileTree" class="skill-file-list skill-file-tree">${html.join('')}</div>`;
}
function setSkillUploadStatus(html){
  state.skillUploadStatus=html||'';
  const el=$('#skillUploadStatus');
  if(el)el.innerHTML=state.skillUploadStatus;
}
function skillUploadButtons(disabled){
  ['skillFolderBtn','skillZipBtn','skillMdBtn','skillFolderPick','skillZipPick','skillMdPick'].forEach(id=>{
    const el=$('#'+id); if(el)el.disabled=!!disabled;
  });
}
let skillUploadLastStart=0;
async function uploadSkillFiles(input){
  const now=Date.now();
  if(now-skillUploadLastStart<800)return; // change + 轮询双触发去重
  skillUploadLastStart=now;
  const files=[...(input.files||[])];
  if(input)input.value='';
  if(!files.length)return toast('没有读取到任何文件，请重新选择');
  const id=state.selectedAsset;
  if(state.selectedAsset==='__new__')return toast('先保存技能，再上传文件');
  if(skillWriteBlocked())return toast('共享资产只读');
  const totalBytes=files.reduce((s,f)=>s+f.size,0);
  const totalLabel=fmtBytes(totalBytes);
  skillUploadButtons(true);

  // 取消控制：读文件阶段用标志位，fetch 阶段用 AbortController
  let cancelled=false;
  const ctrl=new AbortController();
  const cancelBtnHTML='<button type="button" class="skill-upload-cancel">取消</button>';
  const renderRow=(label,pct)=>`<div class="skill-upload-progress"><span>${label}</span><div class="skill-upload-bar ${pct<0?'indeterminate':''}"><i style="width:${Math.max(0,pct<0?30:pct)}%"></i></div><b class="skill-upload-pct">${pct<0?'···':`${pct}%`}</b>${cancelBtnHTML}</div>`;
  const bindCancel=()=>{const b=$('#skillUploadStatus .skill-upload-cancel');if(b)b.onclick=()=>{cancelled=true;ctrl.abort();toast('已取消上传')}};
  const setStatusRow=(label,pct)=>{setSkillUploadStatus(renderRow(label,pct));bindCancel()};

  const buildFd=parts=>{
    const fd=new FormData();
    // 相对路径走并行 paths 字段：multipart 的 filename 会被服务端只保留 basename。
    parts.forEach(p=>{fd.append('paths',p.path);fd.append('files',p.blob,p.path.split('/').pop())});
    return fd;
  };
  const finishOk=data=>{
    const i=(state.data.skills||[]).findIndex(x=>x.id===id);
    if(i>=0)state.data.skills[i]=data;else state.data.skills.push(data);
    setSkillUploadStatus(`<div class="skill-upload-ok">✓ 已替换为 ${files.length} 个文件 · ${totalLabel}（${new Date().toLocaleTimeString()}）</div>`);
    renderAssets();renderProperties();
    toast(`已替换为 ${files.length} 个文件`,'ok');
  };
  const showFail=err=>{
    setSkillUploadStatus(`<div class="skill-upload-err">✕ ${escapeHTML(err.message)}。现有文件未改动。</div>`);
    toast(err.message,'err',8000);
  };

  // ===== 方式 A：先逐文件读入内存（8 并发，单文件 3s 超时） =====
  const parts=new Array(files.length);
  let done=0, readBytes=0, readErr=null;
  const queue=files.map((_,idx)=>idx);
  setStatusRow(`正在读取 0/${files.length} 个文件`,-1);
  const readWithTimeout=async(f,ms)=>{
    let timer;
    const timeoutP=new Promise((_,rej)=>{timer=setTimeout(()=>rej(new Error('__read_timeout__')),ms)});
    const buf=await Promise.race([f.arrayBuffer(),timeoutP]).finally(()=>clearTimeout(timer));
    return buf;
  };
  const workers=Array.from({length:Math.min(8,files.length)},async()=>{
    while(queue.length&&!cancelled&&!readErr){
      const idx=queue.shift();
      const f=files[idx];
      const rel=f.webkitRelativePath||f.name;
      let buf;
      try{
                buf=await readWithTimeout(f,3000);
      }catch(err){
        if(err.message==='__read_timeout__'){
          readErr=new Error(`读取文件超时：${rel}。浏览器读取文件内容被系统/安全软件阻断，将尝试流式直传。`);
        }else{
          readErr=new Error(`读取文件失败：${rel}（${err.message}）`);
        }
        return;
      }
      parts[idx]={path:rel,blob:new Blob([buf])};
      done++;readBytes+=buf.byteLength;

      setStatusRow(`正在读取 ${done}/${files.length} 个文件 · 已读取 ${fmtBytes(readBytes)}`,-1);
    }
  });
  try{await Promise.all(workers)}
  catch(err){if(!readErr)readErr=new Error(`读取过程异常：${err.message}`)}
  if(cancelled){
    setSkillUploadStatus('<div class="skill-upload-err">已取消上传，现有文件未改动。</div>');
    skillUploadButtons(false);
    return;
  }

  let fetchP=null;
  if(!readErr){
    const fd=buildFd(parts);
    fetchP=fetch(`/api/skills/${encodeURIComponent(id)}/files`,{method:'POST',body:fd,signal:ctrl.signal});
  }else{
    // ===== 方式 B（降级）：流式直传原始 File，由浏览器网络进程读取 =====
    setStatusRow(`读取受阻，正在尝试流式直传 ${files.length} 个文件…`,-1);
    const fd=buildFd(files.map(f=>({path:f.webkitRelativePath||f.name,blob:f})));
    fetchP=fetch(`/api/skills/${encodeURIComponent(id)}/files`,{method:'POST',body:fd,signal:ctrl.signal});
  }

  // ===== 上传等待：模拟进度 + 看门狗 15s 必给结论 =====
  let fakePct=8;
  const ticker=setInterval(()=>{
    fakePct=Math.min(88,fakePct+Math.random()*10+4);
    setStatusRow(`上传中 ${Math.round(fakePct)}% · ${files.length} 个文件 · ${totalLabel}`,Math.round(fakePct));
  },220);
  const watchdog=setTimeout(()=>{
    ctrl.abort();
    setSkillUploadStatus('<div class="skill-upload-err">✕ 上传超过 15 秒未完成，已中止。浏览器读取/发送文件被环境阻断（常见于安全管控软件）。现有文件未改动。</div>');
  },15000);
  try{
    const res=await fetchP;
    clearTimeout(watchdog);clearInterval(ticker);

    const data=await res.json().catch(()=>({}));
    if(!res.ok)throw new Error(data.error||`HTTP ${res.status}`);
    finishOk(data);
  }catch(err){
    clearTimeout(watchdog);clearInterval(ticker);
    if(cancelled||err.name==='AbortError'){
      if(!cancelled)setSkillUploadStatus('<div class="skill-upload-err">✕ 上传超过 15 秒未完成，已中止。浏览器读取/发送文件被环境阻断（常见于安全管控软件）。现有文件未改动。</div>');
      else setSkillUploadStatus('<div class="skill-upload-err">已取消上传，现有文件未改动。</div>');
    }else{
      showFail(err);
    }
  }finally{
    skillUploadButtons(false);
  }
}
let skillMdUploadLastStart=0;
async function uploadSkillMd(input){
  const now=Date.now();
  if(now-skillMdUploadLastStart<800)return;
  skillMdUploadLastStart=now;
  const files=[...(input.files||[])];
  if(input)input.value='';
  if(!files.length)return toast('没有读取到任何文件，请重新选择');
  const file=files[0];
  const lower=(file.name||'').toLowerCase();
  if(!lower.endsWith('.md')&&!lower.endsWith('.markdown'))return toast('请选择 SKILL.md 或 .md 文件');
  if(file.size>1024*1024)return toast('SKILL.md 超过 1MB 限制','err');
  const id=state.selectedAsset;
  if(!id||id==='__new__')return toast('先保存技能，再上传文件');
  if(skillWriteBlocked())return toast('共享资产只读');
  const skill=(state.data.skills||[]).find(x=>x.id===id)||{};
  const extra=(skill.fileRefs||[]).length;
  const ok=await afConfirm({
    title:'覆盖性更改',
    message:extra
      ?`这是覆盖性更改：上传后该技能将只保留 SKILL.md，现有的 ${extra} 个文件（含 scripts、文档和其他资源）全部删除。此操作不可撤销。`
      :'这是覆盖性更改：上传后该技能将只保留这一份 SKILL.md。此操作不可撤销。',
    confirmText:'确认覆盖',
    cancelText:'取消',
    danger:true
  });
  if(!ok)return;
  skillUploadButtons(true);
  setSkillUploadStatus('<div class="skill-upload-progress"><span>正在覆盖为仅 SKILL.md…</span><div class="skill-upload-bar indeterminate"><i></i></div><b class="skill-upload-pct">···</b></div>');
  try{
    const fd=new FormData();
    fd.append('paths','SKILL.md');
    fd.append('files',file,'SKILL.md');
    const res=await fetch(`/api/skills/${encodeURIComponent(id)}/files`,{method:'POST',body:fd});
    const data=await res.json().catch(()=>({}));
    if(!res.ok)throw new Error(data.error||`HTTP ${res.status}`);
    const i=(state.data.skills||[]).findIndex(x=>x.id===id);
    if(i>=0)state.data.skills[i]=data;else state.data.skills.push(data);
    setSkillUploadStatus(`<div class="skill-upload-ok">✓ 已覆盖为仅 SKILL.md · ${fmtBytes(file.size)}（${new Date().toLocaleTimeString()}）</div>`);
    renderAssets();renderProperties();
    toast('已覆盖为仅 SKILL.md','ok');
  }catch(err){
    setSkillUploadStatus(`<div class="skill-upload-err">✕ ${escapeHTML(err.message)}。现有文件未改动。</div>`);
    toast(err.message,'err',8000);
  }finally{
    skillUploadButtons(false);
  }
}
async function deleteSkillFile(path){
  if(!path)return;
  if(!await afConfirm({title:'删除文件',message:`确定删除 ${path} 吗？此操作不可恢复。`,confirmText:'确认删除',isDanger:true}))return;
  const id=state.selectedAsset;
  try{
    const res=await fetch(`/api/skills/${encodeURIComponent(id)}/files/${path.split('/').map(encodeURIComponent).join('/')}`,{method:'DELETE'});
    const data=await res.json().catch(()=>({}));
    if(!res.ok)throw new Error(data.error||`HTTP ${res.status}`);
    const i=(state.data.skills||[]).findIndex(x=>x.id===id);
    if(i>=0)state.data.skills[i]=data;
    renderAssets();renderProperties();
    toast('文件已删除','ok');
  }catch(err){toast(err.message,'err')}
}
async function openSkillFilePreview(skill, path){
  if(!path)return toast('无法预览：缺少文件路径','err');
  if(!skill)return toast('无法预览：未选中技能','err');
  const ref=(skill.fileRefs||[]).find(r=>r.path===path)||{};
  const url=`/api/skills/${encodeURIComponent(skill.id)}/files/${path.split('/').map(encodeURIComponent).join('/')}`;
  const host=$('#artifactPreview');
  if(!host)return toast('预览窗口不可用','err');
  host.innerHTML='<div class="artifact-loading">正在加载文件…</div>';
  $('#artifactModal').classList.add('open');
  try{
    const res=await fetch(url);
    if(!res.ok)throw new Error(`HTTP ${res.status}`);
    const blob=await res.blob();
    const ext=(path.split('.').pop()||'').toLowerCase();
    const isImg=['png','jpg','jpeg','gif','webp','svg'].includes(ext);
    const isMd=['md','markdown'].includes(ext);
    let viewMode=isMd?'preview':'source';
    const objectURL=URL.createObjectURL(blob);
    const text=isImg?'':await blob.text();
    const lang=skillFileLang(path);
    function renderModal(){
      const body=isImg
        ? `<div class="artifact-body"><img src="${objectURL}" alt="${escapeHTML(path)}" style="max-width:100%"></div>`
        : viewMode==='preview'
          ? `<div class="artifact-body"><div class="markdown-body">${renderMarkdown(text)}</div></div>`
          : `<div class="artifact-body"><pre class="artifact-content">${highlightCode(text, lang)}</pre></div>`;
      host.innerHTML=`<div class="artifact-preview-head"><div class="artifact-preview-head-left"><span class="artifact-preview-icon">${skillFileIcon(path)}</span><div class="artifact-preview-titles"><h2>${escapeHTML(path.split('/').pop())}</h2><div class="artifact-preview-sub"><span>${escapeHTML(path)}</span></div></div></div><div class="artifact-preview-head-actions"><a class="button ghost small" href="${url}" download>下载</a></div></div><div class="artifact-preview-toolbar"><div class="artifact-toolbar-left">${isMd?`<div class="artifact-view-mode"><button type="button" class="artifact-mode-btn ${viewMode==='preview'?'active':''}" data-mode="preview">渲染预览</button><button type="button" class="artifact-mode-btn ${viewMode==='source'?'active':''}" data-mode="source">源码</button></div>`:''}<div class="artifact-evidence-badges"><span>SHA-256: <code>${escapeHTML((ref.sha256||'—').slice(0,16))}${(ref.sha256||'').length>16?'…':''}</code></span><span>${fmtBytes(ref.size||blob.size||0)}</span></div></div></div>${body}`;
      host.querySelectorAll('.artifact-mode-btn').forEach(btn=>btn.onclick=()=>{viewMode=btn.dataset.mode;renderModal()});
    }
    renderModal();
  }catch(err){
    host.innerHTML=`<div class="artifact-loading">预览失败：${escapeHTML(err.message)}</div>`;
    toast(err.message,'err');
  }
}
async function saveAsset(e){e.preventDefault();const f=e.currentTarget,items=state.data[state.assetType]||[],projectScoped=isProjectSharedAsset(state.assetType),identity=resolveAssetSaveIdentity(items,state.selectedAsset,f.elements.id.value.trim());if(identity.error)return toast(identity.error);const existing=identity.existing;if(existing&&projectScoped&&catalogForeign(existing.projectId))return toast('共享资产只读');let value={id:identity.id,name:f.elements.name.value.trim(),description:f.elements.description.value,projectId:existing?(existing.projectId||''):(state.projectFilter||''),shared:projectScoped&&!!existing?.shared};if(!value.id||!value.name)return toast('标识和名称不能为空');try{
  if(state.assetType==='rules'){const global=!!f.elements.global?.checked;Object.assign(value,{priority:Number(f.elements.priority.value),content:f.elements.content.value,global,workflowIds:global?selectedRuleWorkflowIds():[]})}
  if(state.assetType==='skills'){/* 文件经独立上传接口承载；此处只提交元数据。服务端会强制保留 Content/Files/FileRefs。 */}
  if(state.assetType==='dockerfiles')Object.assign(value,{content:f.elements.content.value,files:JSON.parse(f.elements.files.value||'{}')});
  if(state.assetType==='environments')Object.assign(value,{variables:JSON.parse(f.elements.variables.value||'{}'),pathEntries:lines(f.elements.pathEntries.value),installScript:f.elements.installScript.value});
  if(state.assetType==='sandboxes'){const driver=f.elements.driver.value,sandbox={driver,environment:JSON.parse(f.elements.environment.value||'{}')};if(driver==='docker')Object.assign(sandbox,{dockerfileId:f.elements.dockerfileId.value,readOnlyRootFS:f.elements.readOnlyRootFS.checked});if(driver==='seatbelt'||driver==='bwrap')Object.assign(sandbox,{environmentId:f.elements.environmentId.value});Object.assign(value,sandbox)}
  const endpoints={rules:'rules',skills:'skills',dockerfiles:'dockerfiles',environments:'environments',sandboxes:'sandboxes'};await api(`/api/${endpoints[state.assetType]}`,{method:'PUT',body:JSON.stringify(value)});const i=items.findIndex(x=>x.id===value.id);if(state.assetType==='skills'&&existing)value={...existing,...value,fileRefs:existing.fileRefs||[],content:existing.content||'',files:existing.files||{}};if(i>=0)items[i]=value;else items.push(value);state.selectedAsset=value.id;renderAssets();renderWorkflowMeta();renderProperties();toast(identity.created?'资产已创建':'资产修改已保存');
  }catch(err){toast(err.message)}
}
