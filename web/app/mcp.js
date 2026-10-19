async function loadMCPMarket(){
  try{
    if(!state.mcpMarket.length){const result=await api('/api/mcp-market',{silent:true});state.mcpMarket=result.items||[]}
    state.data.mcpServers=await api('/api/mcp-servers',{silent:true});
    renderMCPManagement();
  }catch(e){toast(e.message)}
}
function scopedMCPInstallations(){
  const uid=state.currentUser&&!state.currentUser.isGuest?Number(state.currentUser.id):0;
  return (state.data?.mcpServers||[]).filter(item=>state.mcpScope==='personal'?item.scope==='personal'&&!item.marketId&&Number(item.ownerUserId)===uid:item.scope==='project'&&item.projectId===state.projectFilter);
}
function mcpSearchHaystack(item){
  return [item.id,item.name,item.description,item.category,item.url,item.marketId,...(item.tags||[]),...(item.aliases||[])].join(' ').toLowerCase();
}
function mcpMatchesQuery(item,query){
  if(!query)return true;
  const hay=mcpSearchHaystack(item);
  return query.split(/\s+/).every(part=>part&&hay.includes(part));
}
function mcpMarketAdded(item,installedIDs){
  if(installedIDs.has(item.id))return true;
  return (item.aliases||[]).some(alias=>installedIDs.has(alias));
}
function mcpMarketLogo(item){
  const src=item.marketId||item.official?safeURL(item.iconUrl||''):'#';
  const fallback=`<span class="mcp-logo-fallback">${escapeHTML((item.name||'M')[0])}</span>`;
  return src==='#'?fallback:`<img src="${escapeHTML(src)}" alt="">${fallback}`;
}
function bindMCPLogoFallbacks(root){
  root.querySelectorAll('.mcp-card-logo img').forEach(img=>{
    const fallback=()=>img.parentElement?.classList.add('logo-load-failed');
    img.addEventListener('error',fallback,{once:true});
    if(img.complete&&img.naturalWidth===0)fallback();
  });
}
function mcpImportPlainObject(value){return value&&typeof value==='object'&&!Array.isArray(value)}
function mcpImportStringMap(value,field){
  if(value==null)return {};
  if(!mcpImportPlainObject(value))throw new Error(`${field} 必须是 JSON 对象`);
  const out={};for(const [key,item] of Object.entries(value)){if(item==null)continue;if(typeof item==='object')throw new Error(`${field}.${key} 必须是字符串`);out[key]=String(item)}return out;
}
function mcpImportStringList(value,field){
  if(value==null)return [];
  if(!Array.isArray(value))throw new Error(`${field} 必须是 JSON 数组`);
  return value.map((item,index)=>{if(typeof item!=='string')throw new Error(`${field}[${index}] 必须是字符串`);return item});
}
function mcpImportedURL(value){
  const text=String(value||'').trim(),markdown=text.match(/^\[[^\]]+\]\((https?:\/\/[^)\s]+)\)$/i);return markdown?markdown[1]:text;
}
function mcpNormalizeImportedServer(raw,key=''){
  if(!mcpImportPlainObject(raw))throw new Error('MCP server 配置必须是 JSON 对象');
  const nested=mcpImportPlainObject(raw.stdio)?raw.stdio:(mcpImportPlainObject(raw.http)?raw.http:{}),value={...raw,...nested};
  let transport=String(value.transport||value.type||'').trim().toLowerCase();
  if(['streamable-http','streamable_http','sse'].includes(transport))transport='http';
  if(!transport)transport=value.url?'http':'stdio';
  if(!['http','stdio'].includes(transport))throw new Error(`不支持的 MCP transport：${transport}`);
  const id=String(value.id||key||'').trim(),name=String(value.name||id||'').trim();
  if(!id)throw new Error('mcp.json 中的 server 缺少标识');
  const args=value.args==null?[]:value.args;if(!Array.isArray(args))throw new Error(`${id}.args 必须是数组`);
  const timeout=Number(value.timeoutSec??value.toolTimeoutSec??60);
  if(!Number.isFinite(timeout)||timeout<0||timeout>600)throw new Error(`${id}.timeoutSec 需在 0–600 之间`);
  const server={id,name:name||id,description:String(value.description||''),transport,timeoutSec:timeout,url:mcpImportedURL(value.url),authType:String(value.authType||''),headers:mcpImportStringMap(value.headers,'headers'),command:String(value.command||''),args:args.map(item=>String(item)),installScript:String(value.installScript||''),workingDirectory:String(value.workingDirectory??value.cwd??''),pathEntries:mcpImportStringList(value.pathEntries,'pathEntries'),environment:mcpImportStringMap(value.environment??value.env,'env')};
  if(transport==='http'&&!server.url)throw new Error(`${id} 缺少 HTTP URL`);
  if(transport==='stdio'&&!server.command)throw new Error(`${id} 缺少 command`);
  return server;
}
function mcpImportCandidates(payload){
  if(!mcpImportPlainObject(payload)&&!Array.isArray(payload))throw new Error('mcp.json 顶层必须是对象或数组');
  const root=mcpImportPlainObject(payload)?(payload.mcpServers??payload.servers??payload):payload;
  if(Array.isArray(root)){if(!root.length)throw new Error('mcp.json 中没有 server');return root.map((item,index)=>mcpNormalizeImportedServer(item,item?.id||`server-${index+1}`))}
  if(!mcpImportPlainObject(root))throw new Error('mcpServers / servers 必须是对象或数组');
  if(root.command||root.url||root.stdio||root.http)return [mcpNormalizeImportedServer(root,root.id||'mcp-server')];
  const entries=Object.entries(root).filter(([,item])=>mcpImportPlainObject(item));
  if(!entries.length)throw new Error('mcp.json 中没有可识别的 server');
  return entries.map(([key,item])=>mcpNormalizeImportedServer(item,key));
}
function mcpParseJSONText(raw){
  let text=String(raw||'').replace(/\u00a0/g,' ').trim();if(!text)throw new Error('请先粘贴 mcp.json 配置');
  const fence=text.match(/^```(?:json)?\s*([\s\S]*?)\s*```$/i);if(fence)text=fence[1].trim();
  try{return JSON.parse(text)}catch{}
  const fragment=text.replace(/;\s*$/,'').replace(/,\s*$/,'');
  try{return JSON.parse(`{${fragment}}`)}catch{throw new Error('无法解析配置：请检查 JSON 引号、逗号和括号')}
}
function mcpApplyImportedServer(server){
  const form=$('#customMcpForm');if(!form)return;
  if(!state.editingMCPId)form.elements.id.value=server.id;
  form.elements.name.value=server.name;form.elements.description.value=server.description;form.elements.transport.value=server.transport;form.elements.timeoutSecHttp.value=server.timeoutSec;form.elements.timeoutSecStdio.value=server.timeoutSec;form.elements.url.value=server.url;form.elements.authType.value=server.authType;form.elements.command.value=server.command;form.elements.args.value=server.args.join('\n');form.elements.installScript.value=server.installScript;form.elements.workingDirectory.value=server.workingDirectory;form.elements.pathEntries.value=server.pathEntries.join('\n');
  mcpRenderKV('headers',server.headers);mcpRenderKV('environment',server.environment);mcpSetTransport(server.transport);
}
async function importMCPJSONText(text){
  const candidates=mcpImportCandidates(mcpParseJSONText(text));let selected=candidates[0];
  if(candidates.length>1){
    const listing=candidates.map((item,index)=>`${index+1}. ${item.id}（${item.transport}）`).join('；');
    const choice=await afPrompt({title:'选择要导入的 MCP',message:`粘贴内容包含 ${candidates.length} 个 server：${listing}`,label:'输入序号或标识',defaultValue:'1'});
    if(choice==null)return;
    const trimmed=choice.trim(),index=/^\d+$/.test(trimmed)?Number(trimmed)-1:-1;selected=index>=0?candidates[index]:candidates.find(item=>item.id===trimmed);
    if(!selected)throw new Error('没有找到指定的 MCP server');
  }
  mcpApplyImportedServer(selected);toast(`已从 mcp.json 填入 ${selected.name}`,'ok');
}
function openCustomMCPModal(item=null){
  const form=$('#customMcpForm'),editing=Boolean(item?.id);state.editingMCPId=editing?item.id:'';form.reset();
  const jsonImport=$('#mcpJsonImport');if(jsonImport)jsonImport.open=false;
  form.elements.scope.value=editing?item.scope:state.mcpScope;form.elements.id.value=editing?item.id:'';form.elements.name.value=editing?item.name||'':'';form.elements.transport.value=editing?item.transport||'http':'http';form.elements.description.value=editing?item.description||'':'';form.elements.authType.value=editing?item.authType||'':'';form.elements.timeoutSecHttp.value=editing?item.timeoutSec||60:60;form.elements.timeoutSecStdio.value=editing?item.timeoutSec||60:60;form.elements.url.value=editing?item.url||'':'';form.elements.command.value=editing?item.command||'':'';form.elements.args.value=editing?(item.args||[]).join('\n'):'';form.elements.installScript.value=editing?item.installScript||'':'';form.elements.workingDirectory.value=editing?item.workingDirectory||'':'';form.elements.pathEntries.value=editing?(item.pathEntries||[]).join('\n'):'';
  mcpRenderKV('environment',editing?item.environment||{}:{});mcpRenderKV('headers',editing?item.headers||{}:{});mcpSetTransport(form.elements.transport.value);mcpUpdateFileSummary(null,editing?item.fileRefs||[]:[]);
  form.elements.scope.disabled=editing;form.elements.id.disabled=editing;$('#customMcpTitle').textContent=editing?'修改自定义 MCP':'添加自定义 MCP';$('#customMcpSubmit').textContent=editing?'保存修改':'添加 MCP';$('#customMcpModal').classList.add('open');setTimeout(()=>form.elements.name.focus(),0)
}
function mcpSetTransport(value){
  $$('[data-mcp-http]').forEach(el=>el.hidden=value!=='http');$$('[data-mcp-stdio]').forEach(el=>el.hidden=value!=='stdio');
  const form=$('#customMcpForm');if(!form)return;form.elements.url.required=value==='http';form.elements.command.required=value==='stdio';
}
function mcpEditor(kind){return $(kind==='headers'?'#mcpHeadersEditor':'#mcpEnvironmentEditor')}
function prepareMCPFormHints(){
  const form=$('#customMcpForm'),args=form?.elements?.args,cwd=form?.elements?.workingDirectory,advanced=form?.querySelector('[data-mcp-stdio] details.mcp-advanced');if(!form||!args||!cwd||!advanced)return;
  const addHelp=(label,title)=>{if(label?.querySelector('.field-help'))return;const help=document.createElement('span');help.className='field-help';help.tabIndex=0;help.textContent='?';help.title=title;help.setAttribute('aria-label',title);label.append(' ',help)};
  args.placeholder='server.py\n--stdio';addHelp(args.previousElementSibling,'文件路径相对于上传内容根目录。例如上传根目录中有 server.py，这里填写 server.py。');
  const cwdLabel=cwd.previousElementSibling;cwd.placeholder='通常留空；例如 packages/my-mcp';addHelp(cwdLabel,'通常留空。上传 MCP 时相对于上传内容根目录，仅在入口程序依赖子目录作为当前目录时填写。');
  const envLabel=Array.from(advanced.children).find(el=>el.tagName==='LABEL'&&el.textContent.includes('Environment Variables'));if(!envLabel)return;
  advanced.insertBefore(cwdLabel,envLabel);advanced.insertBefore(cwd,envLabel);let hint=advanced.querySelector('[data-mcp-cwd-hint]');if(!hint){hint=document.createElement('div');hint.className='json-help';hint.dataset.mcpCwdHint='';hint.textContent='上传文件时默认以内容根目录启动；这里填写的目录也相对于该根目录。';advanced.insertBefore(hint,envLabel)}
  if(!form.elements.pathEntries){const label=document.createElement('label');label.textContent='额外 PATH 目录 ';const small=document.createElement('small');small.textContent='每行一个，相对 $PREFIX';label.appendChild(small);const input=document.createElement('textarea');input.name='pathEntries';input.rows=3;input.placeholder='通常留空，例如 tools/bin';const help=document.createElement('div');help.className='json-help';help.textContent='npm、pip、Go、Cargo、Composer、Ruby 等常见目录已由平台自动加入 PATH；只有自定义安装目录才需要填写。';advanced.insertBefore(label,envLabel);advanced.insertBefore(input,envLabel);advanced.insertBefore(help,envLabel)}
}
function mcpRenderKV(kind,values){const root=mcpEditor(kind);if(!root)return;root.innerHTML='';Object.entries(values||{}).forEach(([key,value])=>mcpAddKVRow(kind,key,value));}
function mcpAddKVRow(kind,key='',value=''){const root=mcpEditor(kind);if(!root)return;const row=document.createElement('div');row.className='kv-editor-row';row.innerHTML=`<input data-kv-key placeholder="KEY" value="${escapeHTML(key)}"><input data-kv-value placeholder="VALUE" value="${escapeHTML(value)}"><button type="button" class="button ghost small" data-mcp-kv-del>删除</button>`;root.appendChild(row);}
function mcpCollectKV(kind){const out={};mcpEditor(kind)?.querySelectorAll('.kv-editor-row').forEach(row=>{const key=row.querySelector('[data-kv-key]')?.value.trim()||'';if(key)out[key]=row.querySelector('[data-kv-value]')?.value||''});return out}
function mcpUpdateFileSummary(files,refs=[]){const count=files?.length||refs.length;const el=$('#mcpFileSummary');if(el)el.textContent=count?`${count} 个文件${files?.length?'待上传':'已保存'}`:'未上传文件';}
async function uploadMCPSelection(id){
  const folder=$('#mcpFolderPick'),zip=$('#mcpZipPick'),files=folder?.files?.length?folder.files:zip?.files;if(!files?.length)return null;
  const body=new FormData();for(const file of files){body.append('files',file,file.name);body.append('paths',file.webkitRelativePath||file.name)}
  return api(`/api/mcp-servers/${encodeURIComponent(id)}/files`,{method:'POST',body});
}
async function saveCustomMCP(e){
	e.preventDefault();const form=e.currentTarget,button=form.querySelector('button[type="submit"]'),wasEditing=Boolean(state.editingMCPId);button.disabled=true;button.textContent=wasEditing?'保存中…':'添加中…';
  try{
    const transport=form.elements.transport.value,server={id:form.elements.id.value.trim(),name:form.elements.name.value.trim(),description:form.elements.description.value.trim(),transport,timeoutSec:Number((transport==='http'?form.elements.timeoutSecHttp:form.elements.timeoutSecStdio).value||60)};
    if(transport==='http')Object.assign(server,{authType:form.elements.authType.value,url:form.elements.url.value.trim(),headers:mcpCollectKV('headers')});
    else Object.assign(server,{command:form.elements.command.value.trim(),args:lines(form.elements.args.value),installScript:form.elements.installScript.value,workingDirectory:form.elements.workingDirectory.value.trim(),pathEntries:lines(form.elements.pathEntries.value),environment:mcpCollectKV('environment')});
    const editing=state.editingMCPId&&state.data.mcpServers.find(item=>item.id===state.editingMCPId);
    let saved;
    if(editing){Object.assign(server,{id:editing.id,scope:editing.scope,projectId:editing.projectId||'',ownerUserId:editing.ownerUserId||0,marketId:'',iconUrl:''});saved=await api('/api/mcp-servers',{method:'PUT',body:JSON.stringify(server)});toast('自定义 MCP 已更新','ok')}
    else{const scope=form.elements.scope.value;saved=await api('/api/mcp-servers/custom',{method:'POST',body:JSON.stringify({server,scope,projectId:scope==='project'?state.projectFilter:''})});state.mcpScope=scope;state.mcpTab='installed';toast('自定义 MCP 已添加','ok')}
    if(transport==='stdio'){const uploaded=await uploadMCPSelection(saved.id);if(uploaded)saved=uploaded}
    const index=state.data.mcpServers.findIndex(item=>item.id===saved.id);if(index>=0)state.data.mcpServers[index]=saved;else state.data.mcpServers.push(saved);
    state.editingMCPId='';$('#customMcpModal').classList.remove('open');renderMCPManagement();renderAgents()
	}catch(err){toast(err.message,'err')}finally{button.disabled=false;button.textContent=wasEditing?'保存修改':'添加 MCP'}
}
function renderMCPManagement(){
  const grid=$('#mcpMarketGrid'),hint=$('#mcpScopeHint');if(!grid||!hint)return;
  if(state.mcpScope==='personal'&&state.mcpTab==='market')state.mcpTab='installed';
  $$('[data-mcp-tab]').forEach(btn=>btn.classList.toggle('active',btn.dataset.mcpTab===state.mcpTab));
  const viewTabs=$('.mcp-view-tabs');if(viewTabs)viewTabs.hidden=state.mcpScope==='personal';
  $$('[data-mcp-scope]').forEach(btn=>btn.classList.toggle('active',btn.dataset.mcpScope===state.mcpScope));
  const project=currentProject(),personal=state.mcpScope==='personal',installed=scopedMCPInstallations();
  hint.innerHTML=personal?`<b>个人自定义 MCP</b><span>仅 ${escapeHTML(userDisplay(state.currentUser)||'当前用户')} 可见；市场能力统一由项目安装。</span>`:`<b>项目 MCP</b><span>市场和自定义 MCP 均安装到「${escapeHTML(project?.name||'未选择项目')}」，不会出现在其他项目中。</span>`;
  if(personal&&(!state.currentUser||state.currentUser.isGuest)){grid.innerHTML='<div class="mcp-market-empty panel"><b>登录后才能管理个人 MCP</b><p>项目级 MCP 仍可在当前项目中管理。</p></div>';return}
  const query=state.mcpQuery;
  if(state.mcpTab==='market'&&!personal){
    const total=(state.mcpMarket||[]).length;
    const matched=(state.mcpMarket||[]).filter(item=>mcpMatchesQuery(item,query)).length;
    hint.innerHTML+=query?`<span>公共服务 ${total} 个，匹配 <b>${matched}</b></span>`:`<span>${total} 个公共 MCP 服务，按需安装。</span>`;
  }
  if(state.mcpTab==='installed'){
    const cards=installed.filter(item=>mcpMatchesQuery(item,query)).map(item=>`<article class="mcp-market-card is-installed"><div class="mcp-card-head"><span class="mcp-card-logo">${mcpMarketLogo(item)}</span><div><h3>${escapeHTML(item.name)}</h3><small>${personal?'个人专属':escapeHTML(project?.name||item.projectId)}</small></div><span class="mcp-installed-badge">${item.marketId?'市场':'自定义'}</span></div><p>${escapeHTML(item.description||'暂无说明')}</p><div class="mcp-card-meta">${item.authType==='taihu-pat'?'<span>平台令牌认证</span>':'<span>自定义认证</span>'}<span>${escapeHTML(item.transport||'http')}</span></div><div class="mcp-card-actions"><span></span><div class="mcp-action-buttons">${item.marketId?'':`<button class="button ghost small" type="button" data-mcp-edit="${escapeHTML(item.id)}">修改</button>`}<button class="button ghost small" type="button" data-mcp-remove="${escapeHTML(item.id)}">移除</button></div></div></article>`).join('');
    grid.innerHTML=cards||`<div class="mcp-market-empty panel"><b>当前范围还没有 MCP</b><p>${personal?'点击右上角「添加自定义 MCP」创建个人专属配置。':'切换到「MCP 市场」即可添加。'}</p></div>`;
  }else{
    const installedIDs=new Set(installed.map(item=>item.marketId));
    const pagedMCP=paginateList('mcp', (state.mcpMarket||[]).filter(item=>mcpMatchesQuery(item,query)));
    const cards=pagedMCP.items.map(item=>{const added=mcpMarketAdded(item,installedIDs);return `<article class="mcp-market-card ${added?'is-installed':''}"><div class="mcp-card-head"><span class="mcp-card-logo">${mcpMarketLogo(item)}</span><div><h3>${escapeHTML(item.name)}</h3><small>${escapeHTML(item.category||'效率工具')}</small></div>${item.official?'<span class="mcp-official-badge">官方服务</span>':''}</div><p>${escapeHTML(item.description)}</p><div class="mcp-card-tags">${(item.tags||[]).map(tag=>`<span>${escapeHTML(tag)}</span>`).join('')}</div><div class="mcp-card-actions"><a href="${escapeHTML(safeURL(item.detailUrl))}" target="_blank" rel="noopener noreferrer">查看详情 ↗</a><button class="button ${added?'ghost':'primary'} small" type="button" data-mcp-install="${escapeHTML(item.id)}" ${added?'disabled':''}>${added?'已添加':'添加'}</button></div></article>`}).join('');
    grid.innerHTML=cards||'<div class="mcp-market-empty panel"><b>没有匹配的 MCP</b><p>换个关键词试试。</p></div>';
    grid.insertAdjacentHTML('beforeend', pagerHTML('mcp', pagedMCP));
  }
  bindMCPLogoFallbacks(grid);
  grid.querySelectorAll('[data-mcp-install]').forEach(button=>button.onclick=()=>installMarketMCP(button.dataset.mcpInstall,button));
  grid.querySelectorAll('[data-mcp-edit]').forEach(button=>button.onclick=()=>openCustomMCPModal(state.data.mcpServers.find(item=>item.id===button.dataset.mcpEdit)));
  grid.querySelectorAll('[data-mcp-remove]').forEach(button=>button.onclick=()=>removeInstalledMCP(button.dataset.mcpRemove,button));
  bindPager(grid);
}
async function installMarketMCP(marketId,button){
  if(state.mcpScope==='project'&&!state.projectFilter)return toast('请先选择项目');
  button.disabled=true;button.textContent='添加中…';
  try{const saved=await api('/api/mcp-market/install',{method:'POST',body:JSON.stringify({marketId,scope:state.mcpScope,projectId:state.mcpScope==='project'?state.projectFilter:''})});state.data.mcpServers.push(saved);renderMCPManagement();renderAgents();toast(`已添加到${state.mcpScope==='personal'?'个人':'当前项目'}`,'ok')}catch(e){toast(e.message,'err');button.disabled=false;button.textContent='添加'}
}
async function removeInstalledMCP(id,button){
  if(!await afConfirm({title:'移除 MCP 确认',message:'移除后，引用这个 MCP 的角色将无法再调用它。确定移除？',confirmText:'确认移除',isDanger:true}))return;
  button.disabled=true;
  try{await api(`/api/mcp-servers?id=${encodeURIComponent(id)}`,{method:'DELETE'});state.data.mcpServers=state.data.mcpServers.filter(item=>item.id!==id);renderMCPManagement();renderAgents();toast('MCP 已移除','ok')}catch(e){toast(e.message,'err');button.disabled=false}
}
