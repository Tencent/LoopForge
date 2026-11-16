// Bind once during browser initialization; source-only tests can load these helpers.
function bindAvatarFallbacks(){document.addEventListener('error',event=>{
  const image=event.target;
  if(!(image instanceof HTMLImageElement)||!image.hasAttribute('data-avatar-fallback'))return;
  image.style.display='none';
  const fallback=image.nextElementSibling;
  if(fallback?.classList.contains('user-avatar-text'))fallback.style.display='';
},true)}

// ================= 工作台 =================
function jumpBind(host){if(!host)return;host.querySelectorAll('[data-jump],[data-issue-filter]').forEach(el=>el.onclick=()=>{
  if(el.dataset.issueFilter!=null){setIssueRunFilter(el.dataset.issueFilter,false);return}
  switchView(el.dataset.jump);
})}
function openRunFromId(id,opts={}){
  const issueId=issueIdForRun(id);
  if(!opts.forceRuns&&issueId){openIssueDetail(issueId,opts.tab||'sop');return}
  switchView('runs');
  showRunDetail(id);
}
function updateApprovalBadge(){
  const n=approvalItems().length;
  const badge=$('#approvalNavBadge');
  if(!badge)return;
  badge.hidden=!n;
  badge.textContent=n||'';
}

// ================= 个人凭证 =================
// One table drives the list rows, the inline editors and the add wizard.
const CREDENTIAL_GROUPS=[
  {id:'platform',name:'平台集成',sub:'代码仓库与统一认证',icon:'key'},
  {id:'model',name:'模型与执行',sub:'AI 对话与编码 CLI',icon:'sparkles'},
];
// A CodeBuddy key only works on the edition that issued it; the CLI treats unknown values as 国际站.
// Keep in sync with chatbot/src/credentials.js and domain.NormalizeCodeBuddyInternetEnvironment.
const CODEBUDDY_ENVIRONMENTS=[
  {value:'',label:'国际站',site:'codebuddy.ai',keyUrl:'https://www.codebuddy.ai/profile/keys'},
  {value:'internal',label:'国内站',site:'copilot.tencent.com',keyUrl:'https://copilot.tencent.com/profile/'},
  {value:'ioa',label:'iOA',site:'tencent.sso.copilot.tencent.com',keyUrl:'https://tencent.sso.copilot.tencent.com/profile/keys'},
];
const CODEBUDDY_ENVIRONMENT_ALIASES={public:'',intl:'',international:'','国际站':'',china:'internal',cn:'internal','国内站':'internal'};
// The CLI receives the stored text as-is, so only the exact values count; returns null for anything else.
function codeBuddyEnvironment(value){
  const key=String(value||'').trim();
  return CODEBUDDY_ENVIRONMENTS.find(env=>env.value===key)||null;
}
// What an old free-text value most likely meant (china → 国内站), used to preselect the fix.
function codeBuddyEnvironmentIntent(value){
  const key=String(value||'').trim().toLowerCase();
  const normalized=Object.prototype.hasOwnProperty.call(CODEBUDDY_ENVIRONMENT_ALIASES,key)?CODEBUDDY_ENVIRONMENT_ALIASES[key]:key;
  return CODEBUDDY_ENVIRONMENTS.find(env=>env.value===normalized)||null;
}
// A stored value the CLI does not take as written; it connects to 国际站 until it is saved again.
function codeBuddyEnvironmentLegacy(value){const raw=String(value||'').trim();return !!raw&&!codeBuddyEnvironment(raw)}
function codeBuddyKeyURL(value){return (codeBuddyEnvironmentIntent(value)||CODEBUDDY_ENVIRONMENTS[0]).keyUrl}
function codeBuddyKeyLinkLabel(value){return `前往${(codeBuddyEnvironmentIntent(value)||CODEBUDDY_ENVIRONMENTS[0]).label}获取 API Key`}
const CREDENTIAL_KINDS=[
  {key:'gitToken',group:'platform',name:'GitHub',short:'仓库授权',logo:'github',monochrome:true,
    use:'克隆私有仓库、创建任务分支、推送交付产物',
    description:'用于克隆私有仓库、创建任务分支及推送交付产物。',
    guide:[['打开 GitHub「Settings → Developer settings → Personal access tokens」','https://github.com/settings/tokens','前往 GitHub 申请个人访问令牌'],['新建令牌，为目标仓库授予 <code>Contents</code> 和 <code>Pull requests</code> 的读写权限'],['复制令牌粘贴到下方，保存前会自动校验权限']],
    placeholder:'请输入个人访问令牌（PAT）',hint:'保存前会先校验令牌有效性与权限',
    impact:'依赖私有仓库的 SOP 将无法克隆或推送仓库。'},
  {key:'gitSshKey',group:'platform',name:'Git SSH',short:'Git SSH 密钥',icon:'key',
    use:'按 SSH 主机管理个人密钥，仓库自动匹配'},
  {key:'codeBuddyApiKey',group:'model',name:'CodeBuddy',short:'CodeBuddy API Key',logo:'codebuddy',
    use:'AI 对话与 SOP 的 CodeBuddy 执行器',
    description:'AI 对话（含 Claude、GPT 等模型）和 SOP 的 CodeBuddy 执行器都使用它；网络环境按账号所在环境配置。',
    guide:[['先在下方选择 CodeBuddy 账号所在的网络环境：国际站、国内站或 iOA'],['登录该站点的 API Key 页面，新建 API Key',codeBuddyKeyURL,codeBuddyKeyLinkLabel],['复制以 <code>ck_</code> 开头的密钥粘贴到下方；DeepSeek、OpenAI 等第三方模型的 Key 不能填在这里']],
    placeholder:'请输入个人 ck_ key',
    extra:{name:'codeBuddyInternetEnvironment',label:'网络环境',options:CODEBUDDY_ENVIRONMENTS},
    impact:'AI 对话将只能使用默认模型，CodeBuddy 执行器节点会失败。'},
  {key:'codexApiKey',group:'model',name:'Codex',short:'Codex API Key',logo:'codex',monochrome:true,
    use:'AI 对话选择 Codex 执行器，以及以 Codex 运行的 SOP 节点',
    description:'AI 对话切换到 Codex 后可选用模型目录里的模型，SOP 在执行机器上以 Codex 运行的节点也使用它。目录为空时，未填 Base URL 使用 Codex 自带模型，填写后跟随服务发布的模型；也可以自己挑选、排序和命名。',
    guide:[['在模型服务商控制台创建 API Key'],['如走代理或私有网关，记下服务地址'],['粘贴 Key，按需填写 Base URL；保存后可在模型目录里挑选 AI 对话的模型']],
    placeholder:'请输入 Codex API Key',
    extra:{name:'codexBaseUrl',label:'Base URL',placeholder:'https://api.openai.com/v1',url:true,clearWithKey:true},
    models:{name:'codexModels',engine:'codex',endpoint:'/models'},
    impact:'以 Codex 运行的 SOP 节点将无法执行，Base URL 与模型目录一并清空。'},
  {key:'claudeCodeApiKey',group:'model',name:'Claude Code',short:'Claude Code API Key',logo:'claude',monochrome:true,
    use:'AI 对话选择 Claude 执行器，以及以 Claude Code 运行的 SOP 节点',
    description:'AI 对话切换到 Claude Code 后可选用模型目录里的模型，SOP 在执行机器上以 Claude Code 运行的节点也使用它。目录为空时，未填 Base URL 使用 Claude Code 自带模型，填写后跟随服务发布的模型；也可以自己挑选、排序和命名。',
    guide:[['在模型服务商控制台创建 API Key'],['如走代理或私有网关，记下服务地址'],['粘贴 Key，按需填写 Base URL；保存后可在模型目录里挑选 AI 对话的模型']],
    placeholder:'请输入 Claude Code API Key',
    extra:{name:'claudeCodeBaseUrl',label:'Base URL',placeholder:'https://api.anthropic.com',url:true,clearWithKey:true},
    models:{name:'claudeCodeModels',engine:'claude-code',endpoint:'/v1/models'},
    impact:'以 Claude Code 运行的 SOP 节点将无法执行，Base URL 与模型目录一并清空。'},
  agentCliCredentialKind('opencode',{name:'OpenCode',logo:'opencode',baseUrl:'https://api.example.com/v1',required:()=>true,
    note:'OpenCode 没有自己的模型服务，需要填写 Base URL。',protocols:[['chat','Chat Completions'],['responses','Responses'],['messages','Messages（Anthropic）']]}),
  agentCliCredentialKind('cursor',{name:'Cursor CLI',logo:'cursor',monochrome:true,
    note:'Cursor CLI 只连接 Cursor 官方服务，使用 Cursor 自带的模型。',
    guide:[['登录 Cursor 控制台，在「Integrations → User API Keys」新建 Key','https://cursor.com/dashboard?tab=integrations','前往 Cursor 创建 API Key'],['复制以 <code>crsr_</code> 开头的 Key'],['粘贴到下方保存']]}),
  agentCliCredentialKind('agy',{name:'Antigravity',logo:'gemini',baseUrl:'https://gateway.example.com/v1',required:protocol=>protocol==='chat',
    note:'默认连接 Gemini 官方服务；选择 OpenAI 兼容网关时需要填写 Base URL。',extraNote:'OpenAI 兼容网关必填',
    guide:[['在 Google AI Studio 创建 Gemini API Key，或准备 OpenAI 兼容网关的 Key','https://aistudio.google.com/apikey','前往 Google AI Studio'],['走网关时记下网关地址'],['粘贴 Key，选择协议；保存后可在模型目录里挑选 AI 对话的模型']],
    protocols:[['gemini','Gemini 官方 API'],['chat','OpenAI 兼容网关（Chat Completions）']]}),
  agentCliCredentialKind('kimi',{name:'Kimi Code',logo:'kimi',baseUrl:'https://api.moonshot.cn/v1',
    note:'未填写 Base URL 时连接 Moonshot 官方服务。',protocols:[['chat','Chat Completions'],['responses','Responses'],['messages','Messages（Anthropic）']]}),
  agentCliCredentialKind('pi',{name:'Pi',logo:'pi',monochrome:true,baseUrl:'https://api.example.com/v1',required:()=>true,
    note:'Pi 没有自己的模型服务，需要填写 Base URL。',protocols:[['chat','Chat Completions'],['responses','Responses'],['messages','Messages（Anthropic）']]}),
  agentCliCredentialKind('dsh',{name:'DeepSeek Harness',logo:'deepseek',baseUrl:'https://api.deepseek.com',
    note:'未填写 Base URL 时连接 DeepSeek 官方服务。',protocols:[['chat','Chat Completions'],['responses','Responses'],['messages','Messages（Anthropic）']]}),
  agentCliCredentialKind('zcode',{name:'ZCode',logo:'glm',baseUrl:'https://open.bigmodel.cn/api/anthropic',
    note:'未填写 Base URL 时连接智谱 BigModel 的 Anthropic 兼容服务（Messages）。',protocols:[['messages','Messages（Anthropic）'],['chat','Chat Completions'],['responses','Responses']]}),
  agentCliCredentialKind('qoder',{name:'Qoder CLI',icon:'terminal',short:'Qoder CLI 个人访问令牌（PAT）',placeholder:'请输入 Qoder CLI 个人访问令牌（PAT）',
    note:'使用 Qoder 个人访问令牌（PAT）连接官方服务。',
    guide:[['在 Qoder 账号的 Integrations 页面创建个人访问令牌','https://qoder.com/account/integrations','前往 Qoder 创建令牌'],['复制令牌并粘贴到下方保存']]}),
];
function agentCliCredentialKind(cli,o){
  const field=`agentCli.${cli}.`,configurable=!!o.protocols;
  return {key:`agentCli.${cli}`,agentCli:cli,group:'model',name:o.name,short:o.short||`${o.name} API Key`,logo:o.logo,icon:o.icon,monochrome:!!o.monochrome,
    use:`AI 对话的 ${o.name} 模型与以 ${o.name} 运行的 SOP 节点`,
    description:`AI 对话切换到 ${o.name} 后可选用它的模型，SOP 在执行机器上以 ${o.name} 运行的节点也使用它。${o.note||''}`,
    guide:o.guide||[['在模型服务商控制台创建 API Key'],['记下服务地址和它提供的接口协议'],['粘贴 Key，按需填写 Base URL 并选择协议；保存后可在模型目录里挑选 AI 对话的模型']],
    placeholder:o.placeholder||`请输入 ${o.name} API Key`,
    ...(configurable?{
      extra:{name:field+'baseUrl',label:'Base URL',placeholder:o.baseUrl,url:true,clearWithKey:true,required:o.required,note:o.extraNote||(o.required?'必填':'')},
      protocol:{name:field+'protocol',options:o.protocols},
      models:{name:field+'models',engine:cli},
    }:{}),
    impact:`AI 对话将不能再切换到 ${o.name}，以 ${o.name} 运行的 SOP 节点将无法执行${configurable?'，Base URL、协议与模型目录一并清空':''}。`};
}
function credentialValue(user,name){
  const [scope,cli,field]=String(name).split('.');
  return scope==='agentCli'&&field?user?.agentCliCredentials?.[cli]?.[field]:user?.[name];
}
function credentialProtocol(kind,user){
  const options=kind.protocol?.options||[],value=credentialValue(user,kind.protocol?.name||'');
  return options.some(([id])=>id===value)?value:(options[0]?.[0]||'');
}
function credentialProtocolHTML(kind,value){
  return `<label class="credential-field"><span>接口协议<em>Base URL 提供的 API</em></span><div class="credential-input is-plain is-select"><select name="protocol" aria-label="接口协议">${kind.protocol.options.map(([id,label])=>`<option value="${id}"${id===value?' selected':''}>${escapeHTML(label)}</option>`).join('')}</select></div></label>`;
}
// The model list behind an Anthropic-style Base URL sits under /v1.
function credentialModelsEndpoint(kind,protocol){return kind.models.endpoint||(protocol==='messages'?'/v1/models':'/models')}
// ---- 模型目录（Codex / Claude Code / 可配置 Agent CLI） ----
// A saved catalog is the chat list on the official endpoint and behind a Base URL.
// An empty catalog follows the CLI's built-in models when there is no Base URL
// (or the protocol is Gemini), and the Base URL's published models otherwise.
// Edits stay in the DOM until 保存.
const CREDENTIAL_CATALOG_MAX=100;
function credentialCatalogModels(value){
  return (Array.isArray(value)?value:[]).map(model=>{const id=String(model?.id||'').trim(),name=String(model?.name||'').trim();return {id,name:name===id?'':name}}).filter(model=>model.id||model.name);
}
function credentialCatalogStored(models){return credentialCatalogModels(models).map(model=>model.name?model:{id:model.id})}
function credentialCatalogKey(models){return JSON.stringify(credentialCatalogModels(models).map(model=>[model.id,model.name]))}
function credentialCatalogError(models){
  const seen=new Set();
  if(models.length>CREDENTIAL_CATALOG_MAX)return {index:CREDENTIAL_CATALOG_MAX,error:`模型目录最多 ${CREDENTIAL_CATALOG_MAX} 个`};
  for(const [index,{id='',name=''}] of models.entries()){
    const model={id,name};
    if(!model.id)return {index,field:'id',error:'模型 ID 不能为空'};
    if(model.id.length>128)return {index,field:'id',error:'模型 ID 不能超过 128 个字符'};
    if(/[\s"'`\\\u0000-\u001f\u007f]/.test(model.id))return {index,field:'id',error:`模型 ID「${model.id}」不能包含空格、引号或反斜杠`};
    if(seen.has(model.id))return {index,field:'id',error:`模型 ID「${model.id}」重复`};
    if([...model.name].length>64)return {index,field:'name',error:'显示名称不能超过 64 个字符'};
    seen.add(model.id);
  }
  return null;
}
function credentialCatalogRowHTML(model={}){
  return `<li class="credential-catalog-row" data-catalog-row><button type="button" class="credential-catalog-grip" data-catalog-grip aria-label="拖动排序，也可以按 ↑ ↓ 移动">${credentialGlyph('grip',14)}</button><span class="credential-input is-plain"><input class="credential-mono" data-catalog-field="id" value="${escapeHTML(model.id||'')}" placeholder="模型 ID，如 gpt-5.5" aria-label="模型 ID" autocomplete="off" spellcheck="false"></span><span class="credential-input is-plain"><input data-catalog-field="name" value="${escapeHTML(model.name||'')}" placeholder="显示名称（可选）" aria-label="显示名称" autocomplete="off"></span><button type="button" class="credential-btn ghost icon danger credential-ink" data-catalog-act="remove" aria-label="移除模型">${credentialGlyph('trash',14)}</button></li>`;
}
function credentialCatalogCopy(official,kind){
  const name=kind?.name||'该 CLI';
  return official
    ?{title:`使用 ${name} 自带的模型`,detail:`目录为空时，AI 对话列出 ${name} 支持的模型。获取可用模型可以改成自己的目录，也可以手动添加。`,reset:'清空目录，改回使用自带的模型',restored:'已恢复默认，保存后使用自带的模型'}
    :{title:'跟随服务发布的模型',reset:'清空目录，改回跟随服务发布的模型',restored:'已恢复默认，保存后跟随服务发布的模型'};
}
function credentialCatalogDetailHTML(official,kind,protocol){
  const copy=credentialCatalogCopy(official,kind);
  return official?escapeHTML(copy.detail):`目录为空时，AI 对话列出 Base URL 的 <code data-catalog-endpoint>${escapeHTML(credentialModelsEndpoint(kind,protocol))}</code> 返回的模型。`;
}
function credentialCatalogHTML(kind,user){
  const baseUrl=credentialValue(user,kind.extra.name),protocol=credentialProtocol(kind,user);
  const models=credentialCatalogModels(credentialValue(user,kind.models.name)),empty=!models.length,official=!String(baseUrl||'').trim()||protocol==='gemini';
  const copy=credentialCatalogCopy(official,kind);
  return `<section class="credential-catalog" data-credential-catalog data-catalog-mode="${official?'official':'remote'}" data-original-key="${escapeHTML(credentialCatalogKey(models))}" data-catalog-source="${escapeHTML(credentialCatalogSource(baseUrl))}" aria-label="模型目录">
    <div class="credential-catalog-body" data-catalog-body>
    <div class="credential-catalog-head"><div><b>模型目录</b><small data-catalog-count>${credentialCatalogCountText(models.length)}</small></div><div><button type="button" class="credential-btn secondary credential-ink" data-catalog-act="probe">${credentialGlyph('download',14)}<span>获取可用模型</span></button><button type="button" class="credential-btn secondary credential-ink" data-catalog-act="add"><span class="credential-turn">${credentialGlyph('plus',14)}</span><span>添加模型</span></button></div></div>
    <div class="credential-catalog-probe" data-catalog-probe hidden></div>
    <ol class="credential-catalog-list" data-catalog-list${empty?' hidden':''}>${models.map(credentialCatalogRowHTML).join('')}</ol>
    <div class="credential-catalog-empty" data-catalog-empty${empty?'':' hidden'}>${credentialGlyph('layers',16)}<div><b data-catalog-empty-title>${escapeHTML(copy.title)}</b><small data-catalog-empty-detail>${credentialCatalogDetailHTML(official,kind,protocol)}</small></div></div>
    <div class="credential-catalog-foot" data-catalog-foot${empty?' hidden':''}><button type="button" class="credential-btn link credential-ink" data-catalog-act="reset">${credentialGlyph('undo',13)}<span>恢复默认</span></button><small data-catalog-reset-note>${escapeHTML(copy.reset)}</small></div>
    </div>
    <span class="credential-sr" data-catalog-live role="status" aria-live="polite"></span>
  </section>`;
}
function credentialCatalogSource(baseUrl){return String(baseUrl||'').trim().replace(/\/+$/,'')}
function credentialCatalogCountText(count){return count?`${count} 个模型 · AI 对话按此顺序列出`:'未自定义'}
function credentialCatalogRows(catalog){return [...catalog.querySelectorAll('[data-catalog-row]')]}
function credentialCatalogRowModel(row){return {id:row.querySelector('[data-catalog-field="id"]').value,name:row.querySelector('[data-catalog-field="name"]').value}}
function credentialCatalogFilledRows(catalog){return credentialCatalogRows(catalog).filter(row=>credentialCatalogModels([credentialCatalogRowModel(row)]).length)}
function readCredentialCatalog(catalog){return credentialCatalogModels(credentialCatalogRows(catalog).map(credentialCatalogRowModel))}
function credentialCatalogOfficial(catalog){
  const form=catalog?.closest('.credential-editor-form'),extra=form?.querySelector('input[name="extra"]');
  return (!!extra&&!extra.value.trim())||form?.querySelector('select[name="protocol"]')?.value==='gemini';
}
function credentialCatalogDirty(catalog){return !!catalog&&credentialCatalogKey(readCredentialCatalog(catalog))!==catalog.dataset.originalKey}
function credentialCatalogPayload(catalog){
  if(!catalog)return undefined;
  return credentialCatalogDirty(catalog)?readCredentialCatalog(catalog):undefined;
}
function syncCredentialCatalog(catalog){
  const official=credentialCatalogOfficial(catalog),kind=credentialKind(catalog.closest('[data-credential-row]')?.dataset.credentialRow),protocol=catalog.closest('.credential-editor-form')?.querySelector('select[name="protocol"]')?.value;
  const copy=credentialCatalogCopy(official,kind),mode=official?'official':'remote';
  const title=catalog.querySelector('[data-catalog-empty-title]'),detail=catalog.querySelector('[data-catalog-empty-detail]'),note=catalog.querySelector('[data-catalog-reset-note]');
  if(title)title.textContent=copy.title;
  if(detail)detail.innerHTML=credentialCatalogDetailHTML(official,kind,protocol);
  if(note)note.textContent=copy.reset;
  if(catalog.dataset.catalogMode&&catalog.dataset.catalogMode!==mode&&!catalog.querySelector('[data-catalog-probe]').hidden)closeCredentialCatalogProbe(catalog);
  catalog.dataset.catalogMode=mode;
  const rows=credentialCatalogRows(catalog).length,empty=!rows;
  catalog.querySelector('[data-catalog-count]').textContent=credentialCatalogCountText(rows);
  catalog.querySelector('[data-catalog-list]').hidden=empty;
  catalog.querySelector('[data-catalog-empty]').hidden=!empty;
  catalog.querySelector('[data-catalog-foot]').hidden=empty;
  catalog.querySelector('[data-catalog-act="add"]').disabled=rows>=CREDENTIAL_CATALOG_MAX;
}
function announceCredentialCatalog(catalog,text){const live=catalog.querySelector('[data-catalog-live]');if(live)live.textContent=text}
function credentialCatalogChanged(catalog){
  syncCredentialCatalog(catalog);
  const form=catalog.closest('.credential-editor-form');if(form)updateCredentialEditorDirty(form);
}
function resetCredentialCatalog(row){
  const catalog=row.querySelector('[data-credential-catalog]'),kind=credentialKind(row.dataset.credentialRow);if(!catalog||!kind)return;
  const holder=document.createElement('div');
  holder.innerHTML=credentialCatalogHTML(kind,credentialUser());
  catalog.replaceWith(holder.firstElementChild);
  const fresh=row.querySelector('[data-credential-catalog]');
  syncCredentialCatalog(fresh);
  hydrateCredentialIcons(fresh);
}
function addCredentialCatalogRow(catalog,model={}){
  const list=catalog.querySelector('[data-catalog-list]');
  list.insertAdjacentHTML('beforeend',credentialCatalogRowHTML(model));
  const row=list.lastElementChild;
  hydrateCredentialIcons(row);
  credentialCatalogChanged(catalog);
  return row;
}
function removeCredentialCatalogRow(catalog,row){
  const next=row.nextElementSibling||row.previousElementSibling;
  row.remove();
  credentialCatalogChanged(catalog);
  announceCredentialCatalog(catalog,'已移除模型，保存后生效');
  (next?.querySelector('[data-catalog-grip]')||catalog.querySelector('[data-catalog-act="add"]'))?.focus({preventScroll:true});
}
function moveCredentialCatalogRow(catalog,row,step){
  const target=step<0?row.previousElementSibling:row.nextElementSibling;if(!target)return;
  if(step<0)target.before(row);else target.after(row);
  row.querySelector('[data-catalog-grip]')?.focus({preventScroll:true});
  credentialCatalogChanged(catalog);
  announceCredentialCatalog(catalog,`已移到第 ${credentialCatalogRows(catalog).indexOf(row)+1} 位`);
}
function startCredentialCatalogDrag(event,grip){
  if(event.button!==0)return;
  const row=grip.closest('[data-catalog-row]'),catalog=grip.closest('[data-credential-catalog]'),list=row?.parentElement;if(!row||!catalog)return;
  event.preventDefault();
  row.classList.add('is-dragging');list.classList.add('is-sorting');
  const move=moveEvent=>{
    if(moveEvent.pointerId!==event.pointerId)return;
    const y=moveEvent.clientY;
    let prev=row.previousElementSibling,next=row.nextElementSibling;
    while(prev&&y<prev.getBoundingClientRect().top+prev.getBoundingClientRect().height/2){prev.before(row);prev=row.previousElementSibling}
    while(next&&y>next.getBoundingClientRect().top+next.getBoundingClientRect().height/2){next.after(row);next=row.nextElementSibling}
  };
  const end=endEvent=>{
    if(endEvent.pointerId!==event.pointerId)return;
    window.removeEventListener('pointermove',move);window.removeEventListener('pointerup',end);window.removeEventListener('pointercancel',end);
    row.classList.remove('is-dragging');list.classList.remove('is-sorting');
    grip.focus({preventScroll:true});
    credentialCatalogChanged(catalog);
  };
  window.addEventListener('pointermove',move);window.addEventListener('pointerup',end);window.addEventListener('pointercancel',end);
}
function credentialCatalogProbeHTML(catalog,kind){
  const probe=catalog.probe,current=new Set(readCredentialCatalog(catalog).map(model=>model.id)),followAll=!current.size;
  const items=probe.models.map(model=>{
    const listed=current.has(model.id),checked=followAll||listed;
    return `<label class="credential-catalog-pick" data-pick="${escapeHTML((model.id+' '+(model.name||'')).toLowerCase())}"><input type="checkbox" value="${escapeHTML(model.id)}"${checked?' checked':''}><code>${escapeHTML(model.id)}</code>${model.name&&model.name!==model.id?`<small>${escapeHTML(model.name)}</small>`:''}${listed?'<em>已在目录</em>':''}</label>`;
  }).join('');
  const protocol=catalog.closest('.credential-editor-form')?.querySelector('select[name="protocol"]')?.value;
  const source=probe.source==='builtin'?'以下是内置模型':`来自 Base URL 的 ${escapeHTML(credentialModelsEndpoint(kind,protocol))}`;
  return `<div class="credential-catalog-probe-top"><span class="credential-input is-plain is-search">${credentialGlyph('search',14)}<input data-catalog-search placeholder="搜索模型" aria-label="搜索模型" autocomplete="off" spellcheck="false"></span><span class="credential-catalog-picked" data-catalog-picked></span><button type="button" class="credential-btn link credential-ink" data-catalog-act="toggle-all">全选</button></div>
    <div class="credential-catalog-probe-list" role="group" aria-label="可用模型">${items}</div>
    <div class="credential-catalog-probe-foot"><small>${source} · 勾选的模型会加入目录，取消勾选会移出</small><div><button type="button" class="credential-btn ghost credential-ink" data-catalog-act="probe-close">取消</button><button type="button" class="credential-btn primary credential-ink" data-catalog-act="probe-apply">更新列表</button></div></div>`;
}
function syncCredentialCatalogProbe(catalog){
  const panel=catalog.querySelector('[data-catalog-probe]'),query=String(panel.querySelector('[data-catalog-search]')?.value||'').trim().toLowerCase();
  const picks=[...panel.querySelectorAll('.credential-catalog-pick')];
  picks.forEach(pick=>{pick.hidden=!!query&&!pick.dataset.pick.includes(query)});
  const visible=picks.filter(pick=>!pick.hidden),checked=picks.filter(pick=>pick.querySelector('input').checked).length;
  const counter=panel.querySelector('[data-catalog-picked]');if(counter)counter.textContent=`已选 ${checked} / ${picks.length}`;
  const toggle=panel.querySelector('[data-catalog-act="toggle-all"]');
  if(toggle){toggle.textContent=visible.length&&visible.every(pick=>pick.querySelector('input').checked)?'全不选':'全选';toggle.disabled=!visible.length}
}
function closeCredentialCatalogProbe(catalog){
  const panel=catalog.querySelector('[data-catalog-probe]');
  catalog.probeToken=(catalog.probeToken||0)+1;catalog.probe=null;
  panel.hidden=true;panel.innerHTML='';
}
async function probeCredentialCatalog(row,catalog,button){
  const kind=credentialKind(row.dataset.credentialRow),form=row.querySelector('.credential-editor-form'),panel=catalog.querySelector('[data-catalog-probe]');
  const extra=form.querySelector('input[name="extra"]'),secret=form.querySelector('[name="secret"]')?.value.trim()||'',protocol=form.querySelector('select[name="protocol"]')?.value;
  const extraError=extra?credentialExtraError(kind,extra.value,protocol):'';
  if(extraError){toast(extraError,'err');extra.focus();return}
  const token=(catalog.probeToken||0)+1;catalog.probeToken=token;catalog.probe=null;
  panel.hidden=false;
  panel.innerHTML=`<p class="credential-vline is-info" role="status">${credentialSpinner(13)}正在获取可用模型…</p>`;
  hydrateCredentialIcons(panel);
  setCredentialButtonBusy(button,'获取中');
  let result;
  try{result=await api(`/api/users/${state.currentUser.id}/cli-models/probe`,{method:'POST',body:JSON.stringify({engine:kind.models.engine,apiKey:secret,...(extra?{baseUrl:extra.value.trim()}:{}),...(protocol?{protocol}:{})}),silent:true})}
  catch(err){result={error:err.message}}
  finally{setCredentialButtonBusy(button,'')}
  if(catalog.probeToken!==token||!catalog.isConnected)return;
  if(result?.error||!result?.models?.length){
    panel.innerHTML=`<div class="credential-catalog-probe-fail"><p class="credential-vline is-err" role="alert">${credentialGlyph('alert',13)}${escapeHTML(result?.error||'服务没有返回可用模型')}</p><div><button type="button" class="credential-btn ghost credential-ink" data-catalog-act="probe-close">关闭</button><button type="button" class="credential-btn secondary credential-ink" data-catalog-act="probe">${credentialGlyph('refresh',13)}<span>重试</span></button></div></div>`;
    hydrateCredentialIcons(panel);
    return;
  }
  const source=credentialCatalogSource(extra?.value);
  if(source!==catalog.dataset.catalogSource){
    const cleared=credentialCatalogRows(catalog);
    cleared.forEach(item=>item.remove());
    catalog.dataset.catalogSource=source;
    credentialCatalogChanged(catalog);
    if(cleared.length)announceCredentialCatalog(catalog,'Base URL 已更换，原模型目录已清空，保存后生效');
  }
  catalog.probe=result;
  panel.innerHTML=credentialCatalogProbeHTML(catalog,kind);
  hydrateCredentialIcons(panel);
  syncCredentialCatalogProbe(catalog);
  panel.querySelector('[data-catalog-search]')?.focus({preventScroll:true});
}
function toggleCredentialCatalogProbe(catalog){
  const panel=catalog.querySelector('[data-catalog-probe]'),visible=[...panel.querySelectorAll('.credential-catalog-pick:not([hidden]) input')];
  const on=!visible.every(input=>input.checked);
  visible.forEach(input=>{input.checked=on});
  syncCredentialCatalogProbe(catalog);
}
function applyCredentialCatalogProbe(catalog){
  const probe=catalog.probe;if(!probe)return;
  const picked=new Set([...catalog.querySelectorAll('[data-catalog-probe] input[type="checkbox"]:checked')].map(input=>input.value));
  const offered=new Set(probe.models.map(model=>model.id)),kept=new Set();
  let removed=0;
  credentialCatalogRows(catalog).forEach(row=>{
    const id=row.querySelector('[data-catalog-field="id"]').value.trim();
    if(offered.has(id)&&!picked.has(id)){row.remove();removed++}else kept.add(id);
  });
  const added=probe.models.filter(model=>picked.has(model.id)&&!kept.has(model.id));
  const room=Math.max(0,CREDENTIAL_CATALOG_MAX-kept.size),list=catalog.querySelector('[data-catalog-list]');
  list.insertAdjacentHTML('beforeend',added.slice(0,room).map(model=>credentialCatalogRowHTML({id:model.id,name:model.name&&model.name!==model.id?model.name:''})).join(''));
  hydrateCredentialIcons(list);
  closeCredentialCatalogProbe(catalog);
  credentialCatalogChanged(catalog);
  const skipped=added.length-Math.min(added.length,room);
  const message=`模型目录已更新：新增 ${Math.min(added.length,room)} 个，移除 ${removed} 个${skipped?`，超出上限未加入 ${skipped} 个`:''}，保存后生效`;
  announceCredentialCatalog(catalog,message);
  toast(message,skipped?'err':'ok');
  catalog.querySelector('[data-catalog-act="probe"]')?.focus({preventScroll:true});
}
function credentialCatalogAction(action,row,catalog,button){
  if(action==='add'){addCredentialCatalogRow(catalog).querySelector('[data-catalog-field="id"]').focus({preventScroll:true});return}
  if(action==='remove')return removeCredentialCatalogRow(catalog,button.closest('[data-catalog-row]'));
  if(action==='reset'){
    credentialCatalogRows(catalog).forEach(item=>item.remove());
    credentialCatalogChanged(catalog);
    announceCredentialCatalog(catalog,credentialCatalogCopy(credentialCatalogOfficial(catalog),credentialKind(row?.dataset.credentialRow)).restored);
    catalog.querySelector('[data-catalog-act="add"]')?.focus({preventScroll:true});
    return;
  }
  if(action==='probe')return probeCredentialCatalog(row,catalog,catalog.querySelector('[data-catalog-act="probe"]'));
  if(action==='probe-close'){closeCredentialCatalogProbe(catalog);catalog.querySelector('[data-catalog-act="probe"]')?.focus({preventScroll:true});return}
  if(action==='toggle-all')return toggleCredentialCatalogProbe(catalog);
  if(action==='probe-apply')return applyCredentialCatalogProbe(catalog);
}
// Platform features depend on these, in the order people should configure them.
const CREDENTIAL_RECOMMENDED=['gitToken','codeBuddyApiKey'];
const CREDENTIAL_PAT_WARNING='请填写 GitHub <b>个人访问令牌（PAT）</b>，为目标仓库授予 <b>Contents</b> 和 <b>Pull requests</b> 的读写权限；少勾权限会导致克隆 / 推送仓库失败。';
function credentialKind(key){return CREDENTIAL_KINDS.find(kind=>kind.key===key)||null}
function credentialConfigured(kind,user){return !!credentialValue(user,kind.agentCli?kind.key+'.apiKeyConfigured':kind.key+'Configured')}
function credentialUser(){return userById(state.currentUser?.id)||{}}
function credentialUI(){return state.credentialUI||(state.credentialUI={open:null,filter:'all',closed:{}})}
function credentialGlyph(name,size=16,cls=''){return `<svg class="agent-icon harness-icon${cls?' '+cls:''}" data-icon="${name}" width="${size}" height="${size}" aria-hidden="true" focusable="false"></svg>`}
function credentialSpinner(size=14){return credentialGlyph('arc-spinner',size,'credential-spin')}
function credentialLogo(kind,cls=''){return `<span class="credential-logo${kind.monochrome?' monochrome':''}${cls?' '+cls:''}">${kind.icon?credentialGlyph(kind.icon,18):`<img src="/images/credentials/${kind.logo}.svg" alt="">`}</span>`}
function hydrateCredentialIcons(root){if(root)window.AgentFlowHarnessIcons?.hydrate?.(root)}
function credentialNextKind(user){return CREDENTIAL_RECOMMENDED.map(credentialKind).find(kind=>!credentialConfigured(kind,user))||null}
function credentialSecretError(kind,value){
  return '';
}
// Advisory only: a third-party model key is accepted here but CodeBuddy answers it with 401.
function credentialSecretWarning(kind,value){
  const text=String(value||'').trim();
  if(kind?.key!=='codeBuddyApiKey'||!text||text.startsWith('ck_'))return '';
  return /^sk-/i.test(text)
    ?'这像是 DeepSeek、OpenAI 等第三方模型的 Key，CodeBuddy 会拒绝；第三方 Key 请改填到 Claude Code 或 Codex 凭证并设置 Base URL'
    :'CodeBuddy API Key 通常以 ck_ 开头，请确认复制的是 CodeBuddy 的 Key';
}
// The value an extra field starts from. An old network environment preselects what it meant (china → 国内站),
// and one that cannot be guessed shows as 国际站, which is what the CLI uses.
function credentialExtraValue(kind,value){
  if(kind?.extra?.options)return codeBuddyEnvironmentIntent(value)?.value??'';
  return value||kind?.extra?.fallback||'';
}
function credentialExtraLabel(kind,value){
  if(kind.extra.options){const env=codeBuddyEnvironment(value)||CODEBUDDY_ENVIRONMENTS[0];return `${env.label} · ${env.site}`}
  return String(value||'').trim()||(kind.extra.url?'官方地址':kind.extra.fallback||'');
}
function codeBuddyEnvironmentHintHTML(selected,stored=''){
  const raw=String(stored||'').trim(),legacy=codeBuddyEnvironmentLegacy(raw)?`原来填写的「${raw}」无法识别，CodeBuddy 实际按国际站连接，点保存改为上面选择的环境。`:'';
  return `${escapeHTML(legacy)}Key 只能在创建它的站点使用 · <a href="${escapeHTML(codeBuddyKeyURL(selected))}" target="_blank" rel="noopener noreferrer" data-credential-key-link>${escapeHTML(codeBuddyKeyLinkLabel(selected))} ↗</a>`;
}
function credentialSelectFieldHTML(kind,stored,note=''){
  const selected=credentialExtraValue(kind,stored);
  const options=kind.extra.options.map(option=>`<option value="${escapeHTML(option.value)}"${option.value===selected?' selected':''}>${escapeHTML(option.label)} · ${escapeHTML(option.site)}</option>`).join('');
  return `<label class="credential-field"><span>${escapeHTML(kind.extra.label)}${note?`<em>${escapeHTML(note)}</em>`:''}</span><div class="credential-input is-plain is-select"><select name="extra">${options}</select></div><small class="credential-env-hint" data-credential-env-hint>${codeBuddyEnvironmentHintHTML(selected,stored)}</small></label>`;
}
// Keeps the 获取 API Key links pointed at the edition that is selected now.
function refreshCredentialEnvironmentLinks(scope,value){
  const hint=scope?.querySelector('[data-credential-env-hint]');if(hint)hint.innerHTML=codeBuddyEnvironmentHintHTML(value);
  scope?.querySelectorAll('[data-wizard-link][data-environment-link]').forEach(link=>{link.href=codeBuddyKeyURL(value);link.textContent=`${codeBuddyKeyLinkLabel(value)} ↗`});
}
function credentialExtraError(kind,value,protocol){
  const text=String(value||'').trim();
  if(!text&&kind.extra?.required?.(protocol))return '请填写 Base URL';
  if(!kind.extra?.url||!text)return '';
  try{const url=new URL(text);if(url.protocol==='http:'||url.protocol==='https:')return ''}catch{}
  return '请填写以 http:// 或 https:// 开头的地址';
}
// Only the fields of one credential are sent; the server leaves the rest alone.
function credentialSavePayload(kind,values={}){
  if(kind.agentCli){
    const entry={},secret=String(values.secret||'').trim();
    if(secret)entry.apiKey=secret;
    if(kind.extra&&values.extra!==undefined)entry.baseUrl=String(values.extra||'').trim();
    if(kind.protocol&&values.protocol!==undefined)entry.protocol=values.protocol;
    if(kind.models&&values.models!==undefined)entry.models=credentialCatalogStored(values.models);
    return {agentCliCredentials:{[kind.agentCli]:entry}};
  }
  const payload={[kind.key]:String(values.secret||'').trim()};
  if(kind.extra&&values.extra!==undefined)payload[kind.extra.name]=String(values.extra||'').trim()||(kind.extra.fallback||'');
  if(kind.models&&values.models!==undefined)payload[kind.models.name]=credentialCatalogStored(values.models);
  return payload;
}
function credentialClearPayload(kind){
  if(kind.agentCli){
    const entry={apiKeyClear:true,baseUrl:'',protocol:''};
    if(kind.models)entry.models=[];
    return {agentCliCredentials:{[kind.agentCli]:entry}};
  }
  const payload={[kind.key+'Clear']:true};
  if(kind.extra?.clearWithKey)payload[kind.extra.name]='';
  if(kind.models)payload[kind.models.name]=[];
  return payload;
}
function gitTokenVerifyFor(user){return state.gitTokenVerify&&Number(state.gitTokenVerify.userId)===Number(user?.id)?state.gitTokenVerify:null}
function gitTokenFailure(result){
  if(!result)return '';
  if(result.error)return result.error;
  if(!result.valid)return `令牌有效但缺少权限：${(result.missing||[]).join('、')}，请在代码托管平台补充授权`;
  return '';
}
function credentialStatus(kind,user){
  if(!credentialConfigured(kind,user))return 'empty';
  if(kind.key!=='gitToken')return 'ok';
  const verify=gitTokenVerifyFor({id:state.currentUser?.id});
  if(verify?.busy)return 'busy';
  return gitTokenFailure(verify?.result)?'err':'ok';
}
function credentialBadgeHTML(status){
  const lamp={empty:'<span class="hui-lamp is-hollow" aria-hidden="true"><i></i></span>',ok:'<span class="hui-lamp" aria-hidden="true"><i></i></span>',busy:credentialSpinner(12),err:credentialGlyph('alert',12)}[status];
  const label={empty:'未配置',ok:'已配置',busy:'校验中',err:'有错误'}[status];
  const tone={empty:'muted',ok:'success',busy:'info',err:'error'}[status];
  return `<span class="hui-badge credential-status" data-size="sm" data-tone="${tone}" data-status="${status}" role="status">${lamp}<span class="hui-badge-label">${label}</span></span>`;
}
function credentialSubline(kind,user){
  const dot='<i class="credential-dot"></i>',use=`<span>${escapeHTML(kind.use)}</span>`;
  if(!credentialConfigured(kind,user))return use;
  if(kind.key==='gitToken'){
    const verify=gitTokenVerifyFor({id:state.currentUser?.id}),result=verify?.result,source=`<span>${user.gitTokenOAuth?'工蜂授权':'个人访问令牌'}</span>`;
    if(verify?.busy)return source+dot+'<span class="is-info">正在校验…</span>';
    if(!result)return source+dot+use;
    if(result.error)return source+dot+`<span class="is-err">${escapeHTML(result.error)}</span>`;
    if(!result.valid)return source+dot+`<span class="is-err">缺少权限 ${escapeHTML((result.missing||[]).join('、'))}</span>`;
    return source+dot+'<span class="is-ok">令牌有效</span>'+(result.scopes?.length?dot+`<span class="credential-mono">${escapeHTML(result.scopes.join(' · '))}</span>`:'');
  }
  if(kind.key==='codeBuddyApiKey'){
    const raw=String(user.codeBuddyInternetEnvironment||'').trim(),env=codeBuddyEnvironment(raw);
    return `<span>网络环境</span>${env?`<span class="credential-mono">${escapeHTML(env.label)}</span>`:`<span class="is-err">「${escapeHTML(raw)}」无法识别，按国际站连接</span>`}${dot}${use}`;
  }
  if(kind.extra){
    const models=kind.models&&credentialCatalogModels(credentialValue(user,kind.models.name)).length,baseUrl=credentialValue(user,kind.extra.name);
    const protocol=kind.protocol?kind.protocol.options.find(([id])=>id===credentialProtocol(kind,user))?.[1]:'';
    return (baseUrl?`<span class="credential-mono">${escapeHTML(baseUrl)}</span>`:'<span>官方地址</span>')+(protocol?dot+`<span>${escapeHTML(protocol)}</span>`:'')+(models?dot+`<span>${models} 个自定义模型</span>`:'')+dot+use;
  }
  return use;
}
function credentialFieldHTML({label,note='',name,type='password',value='',placeholder='',hint='',hintTone='',id='',multiline=false}){
  if(multiline)return `<label class="credential-field"><span>${escapeHTML(label)}${note?`<em>${escapeHTML(note)}</em>`:''}</span><div class="credential-input"><textarea${id?` id="${id}"`:''} name="${name}" rows="5" maxlength="16384" spellcheck="false" autocomplete="off" placeholder="${escapeHTML(placeholder)}"></textarea></div><small data-credential-hint="${name}" class="${hintTone?'is-'+hintTone:''}">${escapeHTML(hint)}</small></label>`;
  const secret=type==='password';
  // Secrets are never serialized into markup; callers assign input.value after rendering.
  return `<label class="credential-field"><span>${escapeHTML(label)}${note?`<em>${escapeHTML(note)}</em>`:''}</span><div class="credential-input${secret?'':' is-plain'}${hintTone?' is-'+(hintTone==='err'?'bad':hintTone==='ok'?'good':hintTone):''}"><input${id?` id="${id}"`:''} name="${name}" type="${type}" ${secret?'autocomplete="new-password"':'autocomplete="off" spellcheck="false"'}${secret?'':` value="${escapeHTML(value)}"`} placeholder="${escapeHTML(placeholder)}">${secret?`<button type="button" class="credential-eye" data-credential-eye aria-label="显示">${credentialGlyph('eye',15)}</button>`:''}</div>${hint!==null?`<small data-credential-hint="${name}"${hintTone?` class="is-${hintTone}"`:''}>${escapeHTML(hint)}</small>`:''}</label>`;
}
function credentialOAuthHTML(label,note){
  return (typeof githubConnectHTML==='function'?githubConnectHTML():'')+`<div class="credential-oauth" data-gongfeng-oauth${state.gongfengOAuthEnabled?'':' hidden'}><a class="credential-btn secondary credential-ink" href="/api/auth/gongfeng/start" target="_blank" rel="noopener noreferrer">${credentialLogo(credentialKind('gitToken'),'is-mini')}<span>${label}</span></a><small>${note}</small></div>`;
}
function credentialPatWarningHTML(show){return `<div class="credential-note is-warn git-token-pat-warning"${show?'':' hidden'}>${credentialGlyph('alert',14)}<span>${CREDENTIAL_PAT_WARNING}</span></div>`}
const GIT_TOKEN_REPLACE_HINT='保存后替换当前的令牌，对下一次任务生效';
function credentialAgo(at){
  const seconds=Math.max(0,Math.round((Date.now()-at)/1000));
  if(seconds<60)return '刚刚';
  if(seconds<3600)return `${Math.floor(seconds/60)} 分钟前`;
  if(seconds<86400)return `${Math.floor(seconds/3600)} 小时前`;
  return `${Math.floor(seconds/86400)} 天前`;
}
function gitTokenDetailHTML(kind,user){
  const verify=gitTokenVerifyFor({id:state.currentUser?.id}),result=verify?.result,missing=result?.missing||[];
  const scopes=(result?.scopes||[]).filter(scope=>!missing.includes(scope)).map(scope=>`<code class="credential-scope">${escapeHTML(scope)}</code>`)
    .concat(missing.map(scope=>`<code class="credential-scope is-miss" title="缺少此权限">${escapeHTML(scope)}</code>`)).join('');
  const ago=verify?.checkedAt?` · ${credentialAgo(verify.checkedAt)}`:'';
  const checked=verify?.busy?`<span class="is-info">${credentialSpinner(13)}校验中…</span>`
    :!result?'<span class="is-muted">尚未校验</span>'
    :result.error?`<span class="is-err">${escapeHTML(result.error)}</span>${ago}`
    :!result.valid?`<span class="is-err">令牌有效但缺少权限 ${escapeHTML(missing.join('、'))}</span>${ago}`
    :`<span class="is-ok">令牌有效</span>${ago}`;
  return `<dl class="credential-kv" data-git-detail><dt>来源</dt><dd>${user.gitTokenOAuth?'工蜂授权 · 到期自动续期':'GitHub 授权 / 个人访问令牌'}</dd><dt>权限</dt><dd>${scopes||'<span class="is-muted">校验后显示</span>'}</dd><dt>最近校验</dt><dd>${checked}</dd><dt>用于</dt><dd>${escapeHTML(kind.description)}</dd></dl>`;
}
function gitTokenEditorHTML(kind,user){
  const failure=gitTokenFailure(gitTokenVerifyFor({id:state.currentUser?.id})?.result);
  return `<div class="credential-editor-form is-detail">
    ${credentialPatWarningHTML(!!failure)}
    ${gitTokenDetailHTML(kind,user)}
    <div class="credential-swap">
      <div class="credential-bar" data-credential-pane="bar">${credentialOAuthHTML('重新授权','')}<button type="button" class="credential-btn ${failure?'primary':'secondary'} credential-ink" data-credential-act="replace">${credentialGlyph('key',14)}<span>手动更换令牌</span></button><button type="button" class="credential-btn ghost danger credential-ink" data-credential-act="delete">${credentialGlyph('trash',14)}<span>删除凭证</span></button></div>
      <div class="credential-replace is-off" data-credential-pane="form" inert>
        ${credentialFieldHTML({label:'新的个人访问令牌',note:'保存前会校验有效性与权限',name:'secret',placeholder:'粘贴 GitHub 个人访问令牌（PAT）',hint:GIT_TOKEN_REPLACE_HINT})}
        <div class="credential-replace-acts"><button type="button" class="credential-btn ghost credential-ink" data-credential-act="cancel">取消</button><button type="button" class="credential-btn primary credential-ink" data-credential-act="save" disabled>校验并保存</button></div>
        <p class="credential-vline git-token-verify-status" data-git-verify-line role="status"></p>
      </div>
      <div class="credential-confirm is-off" data-credential-pane="confirm" role="alert" inert><div>删除 <b>${escapeHTML(kind.short)}</b>？${escapeHTML(kind.impact)}</div><button type="button" class="credential-btn ghost credential-ink" data-credential-act="undelete">取消</button><button type="button" class="credential-btn danger-solid credential-ink" data-credential-act="confirm-delete">删除</button></div>
    </div>
  </div>`;
}
function credentialEditorHTML(kind,user){
  if(kind.key==='gitToken')return gitTokenEditorHTML(kind,user);
  const extraStored=kind.extra?credentialValue(user,kind.extra.name):'';
  const extraValue=kind.extra?credentialExtraValue(kind,extraStored):'';
  const extraField=!kind.extra?''
    :kind.extra.options?credentialSelectFieldHTML(kind,extraStored,'与 Key 所在站点一致')
    :credentialFieldHTML({label:kind.extra.label,note:kind.extra.note||(kind.extra.url?'留空使用官方地址':''),name:'extra',type:kind.extra.url?'url':'text',value:extraValue,placeholder:kind.extra.placeholder,hint:null});
  const protocolValue=kind.protocol?credentialProtocol(kind,user):'';
  const protocolField=kind.protocol?credentialProtocolHTML(kind,protocolValue):'';
  const legacy=!!(kind.extra?.options&&codeBuddyEnvironmentLegacy(user[kind.extra.name]));
  return `<div class="credential-editor-form${kind.models?' has-catalog':''}"${kind.extra?` data-original="${escapeHTML(extraValue)}"`:''}${kind.protocol?` data-original-protocol="${escapeHTML(protocolValue)}"`:''}${legacy?' data-legacy="true"':''}>
    ${credentialFieldHTML({label:kind.short,note:'已配置 · 留空则不改',name:'secret',placeholder:'输入新值以替换',hint:kind.hint||''})}
    ${extraField}
    ${protocolField}
    ${kind.models?credentialCatalogHTML(kind,user):''}
    <div class="credential-swap">
      <div class="credential-editor-foot" data-credential-pane="foot"><button type="button" class="credential-btn ghost danger credential-ink" data-credential-act="delete">${credentialGlyph('trash',14)}<span>删除</span></button><div><button type="button" class="credential-btn ghost credential-ink" data-credential-act="cancel">取消</button><button type="button" class="credential-btn primary credential-ink" data-credential-act="save"${legacy?'':' disabled'}>保存</button></div></div>
      <div class="credential-confirm is-off" data-credential-pane="confirm" role="alert"><div>删除 <b>${escapeHTML(kind.short)}</b>？${escapeHTML(kind.impact)}</div><button type="button" class="credential-btn ghost credential-ink" data-credential-act="undelete">取消</button><button type="button" class="credential-btn danger-solid credential-ink" data-credential-act="confirm-delete">删除</button></div>
    </div>
  </div>`;
}
function credentialRowMainHTML(kind,user){
  const ssh=kind.key==='gitSshKey',on=credentialConfigured(kind,user),open=on&&credentialUI().open===kind.key,status=credentialStatus(kind,user);
  const end=on&&!ssh
    ?`${credentialBadgeHTML(status)}${kind.key==='gitToken'?`<span class="credential-tip" data-tip="重新校验"><button type="button" class="credential-btn ghost icon credential-ink git-token-verify" data-credential-act="verify" aria-label="重新校验"${status==='busy'?' disabled':''}>${credentialGlyph('refresh',15)}</button></span>`:''}<span class="credential-chevron">${credentialGlyph('chevron-down',16)}</span>`
    :`${credentialBadgeHTML(status)}<button type="button" class="credential-btn secondary credential-ink" data-credential-act="setup"${ssh?' aria-haspopup="dialog"':''}>${ssh?'管理密钥':'去配置'}<span class="credential-nudge">${credentialGlyph('chevron-right',13)}</span></button>`;
  const interaction=ssh?'':` role="button" tabindex="0"${on?` aria-expanded="${open}"`:''}`;
  return `<div class="credential-row-main credential-ink"${interaction}>${credentialLogo(kind,on?'':'is-dim')}<div class="credential-row-who"><b>${escapeHTML(kind.name)}<span>${escapeHTML(kind.short)}</span></b><small>${credentialSubline(kind,user)}</small></div><div class="credential-row-end">${end}</div></div>`;
}
function credentialRowHTML(kind,user){
  const on=credentialConfigured(kind,user),open=on&&credentialUI().open===kind.key;
  const editor=on&&kind.key!=='gitSshKey'?credentialEditorHTML(kind,user):'';
  return `<div class="credential-row${on?' is-configured':''}${open?' is-open':''}" data-credential-row="${kind.key}">${credentialRowMainHTML(kind,user)}<div class="credential-editor"><div><div class="credential-editor-inner">${editor}</div></div></div></div>`;
}
function credentialGroupHTML(group,user){
  const kinds=CREDENTIAL_KINDS.filter(kind=>kind.group===group.id),done=kinds.filter(kind=>credentialConfigured(kind,user)).length,closed=!!credentialUI().closed[group.id];
  return `<section class="credential-group" data-credential-group="${group.id}"><button type="button" class="credential-group-head credential-ink" aria-expanded="${!closed}">${credentialGlyph(group.icon,16)}<strong>${escapeHTML(group.name)}</strong><span class="credential-group-sub">${escapeHTML(group.sub)}</span><span class="credential-group-meta">${done} / ${kinds.length}</span><span class="credential-group-chevron">${credentialGlyph('chevron-down',15)}</span></button><div class="credential-fold${closed?' is-closed':''}"><div><div class="credential-rows">${kinds.map(kind=>credentialRowHTML(kind,user)).join('')}<p class="credential-empty" hidden></p></div></div></div></section>`;
}
function credentialProgressHTML(user){
  const done=CREDENTIAL_KINDS.filter(kind=>credentialConfigured(kind,user)).length,next=credentialNextKind(user);
  return `<div class="credential-segs" aria-hidden="true">${CREDENTIAL_KINDS.map((_,index)=>`<i${index<done?' class="is-on"':''}></i>`).join('')}</div><span class="credential-count"><b>${done}</b> / ${CREDENTIAL_KINDS.length} 已配置</span>`
    +(next?`<span class="credential-next">下一步<button type="button" class="credential-btn link credential-ink" data-credential-setup="${next.key}">配置 ${escapeHTML(next.short)}<span class="credential-nudge">${credentialGlyph('arrow-right',13)}</span></button></span>`:`<span class="credential-ready">${credentialGlyph('check',15)}推荐凭证已全部就绪</span>`);
}
function credentialFilterHTML(user){
  const done=CREDENTIAL_KINDS.filter(kind=>credentialConfigured(kind,user)).length,filter=credentialUI().filter;
  return [['all','全部',CREDENTIAL_KINDS.length],['configured','已配置',done],['missing','未配置',CREDENTIAL_KINDS.length-done]].map(([id,label,count])=>`<button type="button" role="tab" data-credential-filter="${id}" class="${filter===id?'active':''}" aria-selected="${filter===id}">${label}<span>${count}</span></button>`).join('');
}
function credentialPanelHTML(user){
  return `<header class="credential-section-head"><div><h2>个人凭证</h2><p>SOP 使用需求责任人（未指定时为项目 Owner）的个人凭证，保存后对下一次任务生效。</p></div><button type="button" class="credential-btn primary credential-ink" id="credentialAddButton" data-credential-add><span class="credential-turn">${credentialGlyph('plus',15)}</span><span>添加凭证</span></button></header>
    <div class="credential-progress">${credentialProgressHTML(user)}</div>
    <div class="credential-toolbar"><div class="credential-filter" role="tablist" aria-label="筛选凭证">${credentialFilterHTML(user)}</div></div>
    <div class="credential-list">${CREDENTIAL_GROUPS.map(group=>credentialGroupHTML(group,user)).join('')}</div>`;
}
function applyCredentialFilter(panel,user){
  const filter=credentialUI().filter;
  panel.querySelectorAll('[data-credential-row]').forEach(row=>{
    const on=credentialConfigured(credentialKind(row.dataset.credentialRow),user);
    row.classList.toggle('is-filtered',(filter==='configured'&&!on)||(filter==='missing'&&on));
  });
  panel.querySelectorAll('[data-credential-group]').forEach(group=>{
    const empty=group.querySelector('.credential-empty'),visible=group.querySelectorAll('[data-credential-row]:not(.is-filtered)').length;
    empty.hidden=!!visible;
    empty.textContent=filter==='configured'?'这一组还没有配置凭证':'这一组已全部配置';
  });
}
function renderCredentialPanel(){
  const panel=$('#credentialPanel');if(!panel)return;
  const user=credentialUser();
  panel.innerHTML=credentialPanelHTML(user);
  if(typeof hydrateGitHubConnections==='function')hydrateGitHubConnections(panel);
  applyCredentialFilter(panel,user);
  hydrateCredentialIcons(panel);
}
function repaintCredentialRow(key){
  const row=$(`#credentialPanel [data-credential-row="${key}"]`),kind=credentialKind(key);if(!row||!kind)return;
  const user=credentialUser(),holder=document.createElement('div');
  holder.innerHTML=credentialRowMainHTML(kind,user);
  row.querySelector('.credential-row-main').replaceWith(holder.firstElementChild);
  if(key==='gitToken'){
    // Only the read-only parts are replaced: an open replace form keeps what was typed.
    const failure=gitTokenFailure(gitTokenVerifyFor({id:state.currentUser?.id})?.result),detail=row.querySelector('[data-git-detail]'),warning=row.querySelector('.git-token-pat-warning'),replace=row.querySelector('[data-credential-act="replace"]');
    if(detail){holder.innerHTML=gitTokenDetailHTML(kind,user);detail.replaceWith(holder.firstElementChild)}
    if(warning)warning.hidden=!failure;
    if(replace){replace.classList.toggle('primary',!!failure);replace.classList.toggle('secondary',!failure)}
  }
  row.querySelector('.credential-status')?.classList.add('is-swap');
  hydrateCredentialIcons(row);
}
function flashCredentialRow(key){
  const row=$(`#credentialPanel [data-credential-row="${key}"]`);if(!row)return;
  row.classList.remove('is-flash');void row.offsetWidth;row.classList.add('is-flash');
  row.querySelector('.credential-status')?.classList.add('is-swap');
}
function setCredentialRowOpen(key,open){
  const row=$(`#credentialPanel [data-credential-row="${key}"]`);if(!row)return;
  row.classList.toggle('is-open',open);
  row.querySelector('.credential-row-main')?.setAttribute('aria-expanded',String(open));
  if(!open){
    resetCredentialEditor(row);
    showCredentialPane(row,'bar');
  }
}
function resetCredentialEditor(row){
  row.querySelectorAll('[name="secret"]').forEach(input=>{input.value='';if(input.tagName==='INPUT')input.type='password'});
  const form=row.querySelector('.credential-editor-form');if(!form)return;
  const extra=form.querySelector('[name="extra"]');if(extra)extra.value=form.dataset.original||'';
  if(extra?.tagName==='SELECT')refreshCredentialEnvironmentLinks(form,extra.value);
  const protocol=form.querySelector('select[name="protocol"]');if(protocol)protocol.value=form.dataset.originalProtocol||'';
  resetCredentialCatalog(row);
  if(row.dataset.credentialRow==='gitToken'){
    const line=form.querySelector('[data-git-verify-line]');if(line){line.classList.remove('is-err');line.innerHTML=''}
    setCredentialHint(form,'secret',GIT_TOKEN_REPLACE_HINT);
  }else setCredentialHint(form,'secret',credentialKind(row.dataset.credentialRow)?.hint||'');
  updateCredentialEditorDirty(form);
}
function toggleCredentialRow(key,open){
  const ui=credentialUI(),next=open??ui.open!==key;
  if(ui.open&&ui.open!==key)setCredentialRowOpen(ui.open,false);
  ui.open=next?key:null;
  setCredentialRowOpen(key,next);
  // 代码仓库 opens on its read-only detail; the token input only appears after 手动更换令牌.
  if(next&&key!=='gitToken')setTimeout(()=>$(`#credentialPanel [data-credential-row="${key}"] [name="secret"]`)?.focus({preventScroll:true}),200);
}
const CREDENTIAL_PANE_FOCUS={form:'[name="secret"]',confirm:'[data-credential-act="undelete"]'};
// 代码仓库 swaps bar / form / confirm; the other kinds swap foot / confirm. Unknown names fall back to the first pane.
function showCredentialPane(row,name,focus=CREDENTIAL_PANE_FOCUS[name]){
  const panes=['bar','foot','form','confirm'].map(pane=>row.querySelector(`[data-credential-pane="${pane}"]`)).filter(Boolean);if(!panes.length)return;
  const target=panes.find(pane=>pane===row.querySelector(`[data-credential-pane="${name}"]`))||panes[0];
  panes.forEach(pane=>{const on=pane===target;pane.classList.toggle('is-off',!on);pane.inert=!on});
  if(focus)setTimeout(()=>target.querySelector(focus)?.focus({preventScroll:true}),60);
}
function updateCredentialEditorDirty(form){
  const secret=form.querySelector('[name="secret"]')?.value.trim()||'',extra=form.querySelector('[name="extra"]'),protocol=form.querySelector('select[name="protocol"]'),save=form.querySelector('[data-credential-act="save"]');
  if(save&&!save.dataset.busy)save.disabled=!secret&&!form.dataset.legacy&&!(extra&&extra.value.trim()!==(form.dataset.original||''))&&!(protocol&&protocol.value!==(form.dataset.originalProtocol||''))&&!credentialCatalogDirty(form.querySelector('[data-credential-catalog]'));
}
function setCredentialHint(scope,name,text,tone=''){
  const hint=scope.querySelector(`[data-credential-hint="${name}"]`),box=scope.querySelector(`[name="${name}"]`)?.closest('.credential-input');
  if(hint){hint.textContent=text;hint.className=tone?'is-'+tone:''}
  if(box){box.classList.toggle('is-bad',tone==='err');box.classList.toggle('is-good',tone==='ok');box.classList.toggle('is-warn',tone==='warn');if(tone==='err'){box.classList.remove('is-shake');void box.offsetWidth;box.classList.add('is-shake')}}
}
function setCredentialButtonBusy(button,label){
  if(!button)return;
  if(label){button.dataset.busy='true';button.dataset.label=button.innerHTML;button.disabled=true;button.innerHTML=`${credentialSpinner(14)}<span>${escapeHTML(label)}</span>`;hydrateCredentialIcons(button);return}
  delete button.dataset.busy;button.disabled=false;if(button.dataset.label!=null)button.innerHTML=button.dataset.label;
}
function storeCredentialUser(saved){
  const users=state.data.users||(state.data.users=[]),index=users.findIndex(u=>Number(u.id)===Number(saved.id));
  if(index>=0)users[index]=saved;else users.push(saved);
}
function notifyCredentialsChanged(){$('#chatNextFrame')?.contentWindow?.postMessage({type:'agentflow-credentials-changed'},location.origin)}
async function saveCredential(key,values={},options={}){
  const user=state.currentUser,kind=credentialKind(key);
  const saved=await api(`/api/users/${user.id}/credentials`,{method:'PUT',body:JSON.stringify(credentialSavePayload(kind,values))});
  if(key==='gitToken'&&!options.verified)state.gitTokenVerify=null;
  storeCredentialUser(saved);
  notifyCredentialsChanged();
  return saved;
}
async function deleteCredential(key){
  const user=state.currentUser,kind=credentialKind(key);
  const saved=await api(`/api/users/${user.id}/credentials`,{method:'PUT',body:JSON.stringify(credentialClearPayload(kind))});
  if(key==='gitToken')state.gitTokenVerify=null;
  storeCredentialUser(saved);
  notifyCredentialsChanged();
  return saved;
}
// Git 令牌必须先校验通过才保存，校验失败时什么都不写入。
async function verifyAndSaveGitToken(token){
  const user=state.currentUser;
  const result=await api(`/api/users/${user.id}/git-token/verify`,{method:'POST',body:JSON.stringify({token}),silent:true});
  if(!result?.valid)return {saved:false,result};
  const saved=await saveCredential('gitToken',{secret:token},{verified:true});
  state.gitTokenVerify={userId:user.id,result,checkedAt:Date.now()};
  return {saved:true,result,user:saved};
}
async function refreshGitTokenStatus(options={}){
  const user=state.currentUser;if(!user||user.isGuest)return null;
  // The previous answer stays alongside busy so the detail keeps its scopes while re-checking.
  const previous=gitTokenVerifyFor(user);
  state.gitTokenVerify={userId:user.id,busy:true,result:previous?.result,checkedAt:previous?.checkedAt};
  repaintCredentialRow('gitToken');
  let result;
  try{result=await api(`/api/users/${user.id}/git-token/verify`,{silent:true})}
  catch(err){result={valid:false,error:err.message}}
  if(Number(state.currentUser?.id)!==Number(user.id))return result;
  state.gitTokenVerify={userId:user.id,result,checkedAt:Date.now()};
  repaintCredentialRow('gitToken');
  const announce=options.announce||state.gitTokenAnnounceVerify;
  state.gitTokenAnnounceVerify=false;
  if(announce){const failure=gitTokenFailure(result);toast(failure||'Git 令牌校验通过',failure?'err':'ok')}
  return result;
}
function credentialSaved(key,message){
  credentialUI().open=null;
  renderCredentialPanel();
  flashCredentialRow(key);
  toast(message,'ok');
}
async function submitCredentialEditor(row,button){
  const key=row.dataset.credentialRow,kind=credentialKind(key),form=row.querySelector('.credential-editor-form');
  const secret=form.querySelector('[name="secret"]').value.trim(),extra=form.querySelector('[name="extra"]'),protocol=form.querySelector('[name="protocol"]');
  const secretError=credentialSecretError(kind,secret),extraError=extra?credentialExtraError(kind,extra.value,protocol?.value):'';
  if(secretError){setCredentialHint(form,'secret',secretError,'err');return}
  if(extraError){toast(extraError,'err');extra.focus();return}
  const catalog=form.querySelector('[data-credential-catalog]'),models=credentialCatalogPayload(catalog);
  const catalogError=models&&credentialCatalogError(models);
  if(catalogError){
    toast(catalogError.error,'err');
    const input=credentialCatalogFilledRows(catalog)[catalogError.index]?.querySelector(`[data-catalog-field="${catalogError.field||'id'}"]`);
    if(input){const box=input.closest('.credential-input');box.classList.add('is-bad');box.classList.remove('is-shake');void box.offsetWidth;box.classList.add('is-shake');input.focus()}
    return;
  }
  setCredentialButtonBusy(button,key==='gitToken'?'校验中':'保存中');
  try{
    if(key==='gitToken'){
      const outcome=await verifyAndSaveGitToken(secret);
      if(!outcome.saved){
        setCredentialButtonBusy(button,'');
        const line=row.querySelector('[data-git-verify-line]'),warning=row.querySelector('.git-token-pat-warning');
        if(line){line.classList.add('is-err');line.innerHTML=credentialGlyph('alert',13)+escapeHTML(`${gitTokenFailure(outcome.result)||'Git Token 校验未通过'}（未保存）`);hydrateCredentialIcons(line)}
        if(warning)warning.hidden=false;
        return;
      }
      credentialSaved(key,'Git Token 校验通过，已保存');
      return;
    }
    await saveCredential(key,{secret,...(extra?{extra:extra.value}:{}),...(protocol?{protocol:protocol.value}:{}),...(models?{models}:{})});
    credentialSaved(key,`${kind.short} 已保存`);
  }catch(err){toast(err.message,'err');setCredentialButtonBusy(button,'')}
}
async function confirmDeleteCredential(row,button){
  const key=row.dataset.credentialRow,kind=credentialKind(key);
  setCredentialButtonBusy(button,'删除中');
  try{await deleteCredential(key);credentialSaved(key,`${kind.short} 已删除`)}
  catch(err){toast(err.message,'err');setCredentialButtonBusy(button,'')}
}
function toggleCredentialSecret(button){
  const input=button.previousElementSibling,show=input.type==='password';
  input.type=show?'text':'password';
  button.setAttribute('aria-label',show?'隐藏':'显示');
  button.innerHTML=credentialGlyph(show?'eye-off':'eye',15);
  hydrateCredentialIcons(button);
}
function credentialRowAction(action,row,button){
  const key=row.dataset.credentialRow;
  if(action==='setup')return openCredentialWizard(key);
  if(action==='verify')return refreshGitTokenStatus({announce:true});
  const gongfeng=key==='gitToken';
  if(action==='replace')return showCredentialPane(row,'form');
  if(action==='cancel'){
    if(!gongfeng)return toggleCredentialRow(key,false);
    resetCredentialEditor(row);
    return showCredentialPane(row,'bar','[data-credential-act="replace"]');
  }
  if(action==='delete')return showCredentialPane(row,'confirm');
  if(action==='undelete')return showCredentialPane(row,'bar',gongfeng?'[data-credential-act="delete"]':'');
  if(action==='confirm-delete')return confirmDeleteCredential(row,button);
  if(action==='save')return submitCredentialEditor(row,button);
}
function setCredentialFilter(panel,filter){
  credentialUI().filter=filter;
  panel.querySelectorAll('[data-credential-filter]').forEach(tab=>{const on=tab.dataset.credentialFilter===filter;tab.classList.toggle('active',on);tab.setAttribute('aria-selected',String(on))});
  applyCredentialFilter(panel,credentialUser());
}
function toggleCredentialGroup(head){
  const id=head.closest('[data-credential-group]').dataset.credentialGroup,ui=credentialUI();
  ui.closed[id]=!ui.closed[id];
  head.setAttribute('aria-expanded',String(!ui.closed[id]));
  head.nextElementSibling.classList.toggle('is-closed',!!ui.closed[id]);
}
function activateCredentialRow(row){
  const key=row.dataset.credentialRow;
  if(key==='gitSshKey')return openGitSSHKeys();
  return row.classList.contains('is-configured')?toggleCredentialRow(key):openCredentialWizard(key);
}
function bindCredentialPanel(panel){
  panel.onclick=event=>{
    const target=event.target;
    if(target.closest('[data-credential-add]'))return openCredentialWizard();
    const setup=target.closest('[data-credential-setup]');if(setup)return openCredentialWizard(setup.dataset.credentialSetup);
    const filter=target.closest('[data-credential-filter]');if(filter)return setCredentialFilter(panel,filter.dataset.credentialFilter);
    const head=target.closest('.credential-group-head');if(head)return toggleCredentialGroup(head);
    const eye=target.closest('[data-credential-eye]');if(eye)return toggleCredentialSecret(eye);
    const row=target.closest('[data-credential-row]');if(!row)return;
    const catalogAction=target.closest('[data-catalog-act]');
    if(catalogAction){event.stopPropagation();if(!catalogAction.disabled)credentialCatalogAction(catalogAction.dataset.catalogAct,row,catalogAction.closest('[data-credential-catalog]'),catalogAction);return}
    const action=target.closest('[data-credential-act]');
    if(action){event.stopPropagation();if(!action.disabled)credentialRowAction(action.dataset.credentialAct,row,action);return}
    if(target.closest('.credential-editor'))return;
    if(target.closest('.credential-row-main'))activateCredentialRow(row);
  };
  panel.onkeydown=event=>{
    const main=event.target.closest?.('.credential-row-main[role="button"]');
    if(main&&event.target===main&&(event.key==='Enter'||event.key===' ')){event.preventDefault();activateCredentialRow(main.closest('[data-credential-row]'))}
    const grip=event.target.closest?.('[data-catalog-grip]');
    if(grip&&(event.key==='ArrowUp'||event.key==='ArrowDown')){event.preventDefault();moveCredentialCatalogRow(grip.closest('[data-credential-catalog]'),grip.closest('[data-catalog-row]'),event.key==='ArrowUp'?-1:1);return}
    const probe=event.target.closest?.('[data-catalog-probe]');
    if(probe&&event.key==='Escape'){event.preventDefault();event.stopPropagation();credentialCatalogAction('probe-close',null,probe.closest('[data-credential-catalog]'));return}
    const input=event.target.closest?.('.credential-editor-form input, .credential-editor-form select');
    if(input&&event.key==='Enter'&&!probe){event.preventDefault();const save=input.closest('.credential-editor-form').querySelector('[data-credential-act="save"]');if(!save.disabled)save.click()}
  };
  panel.onpointerdown=event=>{
    const grip=event.target.closest?.('[data-catalog-grip]');if(grip)startCredentialCatalogDrag(event,grip);
  };
  panel.oninput=event=>{
    const form=event.target.closest?.('.credential-editor-form');if(!form)return;
    const catalog=event.target.closest('[data-credential-catalog]');
    if(catalog){
      if(event.target.closest('[data-catalog-probe]'))return syncCredentialCatalogProbe(catalog);
      event.target.closest('.credential-input')?.classList.remove('is-bad');
    }
    if(event.target.name==='extra'||event.target.name==='protocol'){const catalogBox=form.querySelector('[data-credential-catalog]');if(catalogBox)syncCredentialCatalog(catalogBox)}
    if(event.target.name==='secret'){
      const kind=credentialKind(form.closest('[data-credential-row]').dataset.credentialRow),error=credentialSecretError(kind,event.target.value),warning=credentialSecretWarning(kind,event.target.value);
      setCredentialHint(form,'secret',error||warning||(kind.key==='gitToken'?GIT_TOKEN_REPLACE_HINT:kind.hint||''),error?'err':warning?'warn':'');
    }
    if(event.target.name==='extra'&&event.target.tagName==='SELECT')refreshCredentialEnvironmentLinks(form,event.target.value);
    updateCredentialEditorDirty(form);
  };
}
// gotoCredential lands here for configured credentials.
function revealCredentialRow(key){
  const panel=$('#credentialPanel'),row=panel?.querySelector(`[data-credential-row="${key}"]`);if(!row)return;
  if(row.classList.contains('is-filtered'))setCredentialFilter(panel,'all');
  const group=row.closest('[data-credential-group]'),head=group?.querySelector('.credential-group-head');
  if(head&&credentialUI().closed[group.dataset.credentialGroup])toggleCredentialGroup(head);
  if(credentialUI().open!==key)toggleCredentialRow(key,true);
  else if(key!=='gitToken')setTimeout(()=>row.querySelector('[name="secret"]')?.focus({preventScroll:true}),200);
  row.scrollIntoView({block:'center',behavior:'smooth'});
  row.classList.add('credential-highlight');
  setTimeout(()=>row.classList.remove('credential-highlight'),2400);
}

// ---- 添加凭证向导 ----
let credentialWizard=null;
function credentialWizardDefaults(kind,user){return kind?.extra?credentialExtraValue(kind,credentialValue(user,kind.extra.name)):''}
function openCredentialWizard(key){
  const user=credentialUser();
  if(!state.currentUser||state.currentUser.isGuest||!$('#credentialPanel'))return;
  if(key==='gitSshKey')return openGitSSHKeys();
  const preset=key?credentialKind(key):null;
  if(preset&&credentialConfigured(preset,user))return revealCredentialRow(key);
  const kind=preset||CREDENTIAL_RECOMMENDED.map(credentialKind).find(k=>!credentialConfigured(k,user))||CREDENTIAL_KINDS.find(k=>!credentialConfigured(k,user))||null;
  if(credentialWizard)closeCredentialWizard(true);
  credentialUI().open&&toggleCredentialRow(credentialUI().open,false);
  credentialWizard={step:preset?2:1,kind:kind?.key||null,direct:!!preset,secret:'',extra:credentialWizardDefaults(kind,user),protocol:credentialProtocol(kind||{},user),saving:false,error:'',done:false,guide:{},returnFocus:document.activeElement};
  let scrim=$('#credentialWizard');
  if(scrim?.closeTimer){clearTimeout(scrim.closeTimer);scrim.closeTimer=0}
  if(!scrim){
    scrim=document.createElement('div');
    scrim.id='credentialWizard';
    scrim.className='credential-scrim';
    scrim.innerHTML=`<section class="credential-dialog" role="dialog" aria-modal="true" aria-label="添加凭证" tabindex="-1"><div class="credential-dialog-top"><div class="credential-dots" aria-hidden="true"><span></span><span></span><span></span></div><div><span data-wizard-step role="status"></span><button type="button" class="credential-btn ghost icon credential-ink" data-wizard-close aria-label="关闭">${credentialGlyph('close',15)}</button></div></div><div class="credential-dialog-body"></div><div class="credential-dialog-foot"></div></section>`;
    document.body.append(scrim);
    scrim.addEventListener('pointerdown',event=>{if(event.target===scrim)closeCredentialWizard()});
    scrim.addEventListener('click',credentialWizardClick);
    scrim.addEventListener('input',credentialWizardInput);
  }
  document.removeEventListener('keydown',credentialWizardKeydown,true);
  document.addEventListener('keydown',credentialWizardKeydown,true);
  $('#personalSettingsContent')?.classList.add('credential-wizard-open');
  paintCredentialWizard(0);
  scrim.querySelector('.credential-dialog').focus({preventScroll:true});
  requestAnimationFrame(()=>scrim.classList.add('is-open'));
  setTimeout(focusCredentialWizard,260);
}
function closeCredentialWizard(immediate=false){
  const wizard=credentialWizard,scrim=$('#credentialWizard');
  if(!wizard||(wizard.saving&&!immediate))return;
  credentialWizard=null;
  document.removeEventListener?.('keydown',credentialWizardKeydown,true);
  $('#personalSettingsContent')?.classList.remove('credential-wizard-open');
  if(!scrim)return;
  const remove=()=>{scrim.closeTimer=0;scrim.remove()};
  scrim.classList.remove('is-open');
  // The secret input is removed with the dialog so nothing typed stays in the DOM.
  scrim.querySelectorAll('input').forEach(input=>{input.value=''});
  if(immediate)remove();else scrim.closeTimer=setTimeout(remove,260);
  if(wizard.done)flashCredentialRow(wizard.kind);
  if(wizard.returnFocus?.isConnected)wizard.returnFocus.focus({preventScroll:true});
}
function credentialWizardValid(wizard=credentialWizard){
  const kind=credentialKind(wizard.kind);if(!kind)return {ok:false};
  const secret=String(wizard.secret||'').trim(),error=credentialSecretError(kind,secret)||(kind.extra?credentialExtraError(kind,wizard.extra,wizard.protocol):'');
  return {ok:!!secret&&!error,error};
}
function credentialWizardSecretHint(kind,wizard,validity=credentialWizardValid(wizard)){
  const error=credentialSecretError(kind,wizard.secret);if(error)return {text:error,tone:'err'};
  const warning=credentialSecretWarning(kind,wizard.secret);if(warning)return {text:warning,tone:'warn'};
  return validity.ok?{text:'格式正确',tone:'ok'}:{text:kind.hint||'只在你负责的任务运行时注入，保存后不再明文展示',tone:''};
}
function credentialWizardStepOneHTML(user){
  const wizard=credentialWizard,all=CREDENTIAL_KINDS.every(kind=>credentialConfigured(kind,user));
  const groups=CREDENTIAL_GROUPS.map(group=>`<p class="credential-opt-label">${escapeHTML(group.name)}</p><div class="credential-opts" role="radiogroup" aria-label="${escapeHTML(group.name)}">${CREDENTIAL_KINDS.filter(kind=>kind.group===group.id).map((kind,index)=>{
    const on=credentialConfigured(kind,user),selected=wizard.kind===kind.key;
    return `<button type="button" class="credential-opt${selected?' is-selected':''}" style="--i:${index}" role="radio" aria-checked="${selected}" data-wizard-kind="${kind.key}"${on&&kind.key!=='gitSshKey'?' disabled':''}><span class="credential-radio">${credentialGlyph('check',10)}</span>${credentialLogo(kind)}<span class="credential-opt-text"><b>${escapeHTML(kind.name)}</b><small>${escapeHTML(kind.short)}</small></span>${on?'<span class="credential-pill is-ok">已配置</span>':CREDENTIAL_RECOMMENDED.includes(kind.key)?'<span class="credential-pill">推荐</span>':''}</button>`;
  }).join('')}</div>`).join('');
  return `<div class="credential-question">${credentialGlyph('key',16)}<span>要添加哪一项凭证？</span><small>选择一项 · 已配置的可在列表中编辑</small></div>${groups}${all?`<div class="credential-note">${credentialGlyph('check',14)}<span>已全部配置，可在列表中编辑。</span></div>`:''}`;
}
function credentialWizardStepTwoHTML(kind){
  const wizard=credentialWizard;
  // A guide link given as functions follows the selected network environment.
  const guide=kind.guide.map(([text,href,label],index)=>{
    const dynamic=typeof href==='function',url=dynamic?href(wizard.extra):href,name=typeof label==='function'?label(wizard.extra):label;
    return `<li class="${wizard.guide[index]?'is-done':''}" data-wizard-guide="${index}"><span class="credential-guide-no">${wizard.guide[index]?credentialGlyph('check-draw',10):index+1}</span><span>${text}${url?`<a href="${escapeHTML(url)}" target="_blank" rel="noopener noreferrer" data-wizard-link="${index}"${dynamic?' data-environment-link':''}>${escapeHTML(name)} ↗</a>`:''}</span></li>`;
  }).join('');
  const github=kind.key==='gitToken';
  const manual=wizard.githubMethod==='manual';
  const methods=github?`<div class="github-methods" role="tablist" aria-label="GitHub 连接方式"><button type="button" role="tab" data-github-method="oauth" aria-selected="${!manual}">GitHub 授权</button><button type="button" role="tab" data-github-method="manual" aria-selected="${manual}">访问令牌</button></div>`:'';
  if(github&&!manual)return `<div class="credential-question">${credentialLogo(kind,'is-mini')}<span>连接 GitHub</span><small class="credential-description">连接后可选择仓库，用于克隆代码、创建分支和推送交付产物。</small></div>${methods}${credentialOAuthHTML('用工蜂授权','')}`;
  const validity=credentialWizardValid();
  const {text:secretHint,tone:secretTone}=credentialWizardSecretHint(kind,wizard,validity);
  const secretField=credentialFieldHTML({label:github?'个人访问令牌（PAT）':kind.short,name:'secret',id:'credentialWizardSecret',placeholder:kind.placeholder,hint:secretHint,hintTone:secretTone});
  const extraField=!kind.extra?''
    :kind.extra.options?credentialSelectFieldHTML(kind,wizard.extra)
    :credentialFieldHTML({label:kind.extra.label,note:kind.extra.note||(kind.extra.url?'可选 · 留空使用官方地址':''),name:'extra',type:kind.extra.url?'url':'text',value:wizard.extra,placeholder:kind.extra.placeholder,hint:''});
  const protocolField=kind.protocol?credentialProtocolHTML(kind,wizard.protocol||credentialProtocol(kind,{})):'';
  // The network environment decides where the key comes from, so it is chosen first.
  return `<div class="credential-question">${credentialLogo(kind,'is-mini')}<span>${github?'使用访问令牌':'配置 '+escapeHTML(kind.short)}</span><small class="credential-description">${escapeHTML(kind.description)}</small></div>${methods}<ol class="credential-guide">${guide}</ol><div class="credential-fields">
    ${kind.extra?.options?extraField+secretField:secretField+extraField+protocolField}
  </div>`;
}
function credentialWizardStepThreeHTML(kind){
  const wizard=credentialWizard;
  if(wizard.done)return `<div class="credential-saved"><span class="credential-check-big"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 12.5l4.2 4.2L19 7"/></svg></span><h4>${escapeHTML(kind.short)} 已保存</h4><p>下一次运行你负责的任务时生效，可随时在列表中展开编辑。</p></div>`;
  const extra=kind.extra?`<dt>${escapeHTML(kind.extra.label)}</dt><dd${kind.extra.url?' class="credential-mono"':''}>${escapeHTML(credentialExtraLabel(kind,wizard.extra))}</dd>`:'';
  const warning=credentialSecretWarning(kind,wizard.secret);
  const line=wizard.error?`<p class="credential-vline is-err" role="alert">${credentialGlyph('alert',13)}${escapeHTML(wizard.error)}</p>`
    :wizard.saving&&kind.key==='gitToken'?`<p class="credential-vline is-info" role="status">${credentialSpinner(13)}正在校验令牌有效性与权限…</p>`
    :kind.key==='gitToken'?`<p class="credential-vline git-token-verify-status">${credentialGlyph('lock',13)}保存前会先校验令牌有效性与权限</p>`
    :warning?`<p class="credential-vline is-warn" role="status">${credentialGlyph('alert',13)}${escapeHTML(warning)}</p>`:'';
  return `<div class="credential-question">${credentialGlyph('check',16)}<span>确认并保存</span><small>密钥只在任务运行时注入，不写入 Agent 资产</small></div><div class="credential-summary"><div>${credentialGlyph('key',14)}${escapeHTML(kind.name)}</div><dl><dt>凭证</dt><dd>${escapeHTML(kind.short)}</dd><dt>密钥</dt><dd class="credential-mask">已填写 ••••••••</dd>${extra}<dt>生效</dt><dd>下一次任务</dd></dl></div>${line}`;
}
function credentialWizardFootHTML(){
  const wizard=credentialWizard;
  if(wizard.step===2&&wizard.kind==='gitToken'&&wizard.githubMethod!=='manual')return '<span class="credential-kbd"><kbd>Esc</kbd> 关闭</span><span class="github-private-note">仅为当前账号授权</span>';
  if(wizard.done)return `<span></span><button type="button" class="credential-btn primary credential-ink" data-wizard-done>完成</button>`;
  const back=wizard.step===1||(wizard.direct&&wizard.step===2)
    ?'<span class="credential-kbd"><kbd>Esc</kbd> 关闭</span>'
    :`<button type="button" class="credential-btn ghost credential-ink" data-wizard-back${wizard.saving?' disabled':''}><span class="credential-nudge is-left">${credentialGlyph('arrow-left',13)}</span>上一步</button>`;
  if(wizard.step<3){
    const ok=wizard.step===1?!!wizard.kind&&(wizard.kind==='gitSshKey'||!credentialConfigured(credentialKind(wizard.kind),credentialUser())):credentialWizardValid().ok;
    return `${back}<button type="button" class="credential-btn secondary credential-ink" data-wizard-next${ok?'':' disabled'}>下一步<span class="credential-nudge">${credentialGlyph('chevron-right',13)}</span></button>`;
  }
  const gongfeng=wizard.kind==='gitToken';
  return `${back}<button type="button" class="credential-btn primary credential-ink" data-wizard-next${wizard.saving?' disabled data-busy="true"':''}>${wizard.saving?credentialSpinner(14)+`<span>${gongfeng?'校验中':'保存中'}</span>`:credentialGlyph('check',14)+`<span>${gongfeng?'校验并保存':'保存凭证'}</span>`}</button>`;
}
function credentialWizardPaneHTML(){
  const wizard=credentialWizard,kind=credentialKind(wizard.kind);
  if(wizard.step===1)return credentialWizardStepOneHTML(credentialUser());
  if(wizard.step===2)return credentialWizardStepTwoHTML(kind);
  return credentialWizardStepThreeHTML(kind);
}
function paintCredentialWizard(direction){
  const scrim=$('#credentialWizard'),wizard=credentialWizard;if(!scrim||!wizard)return;
  const body=scrim.querySelector('.credential-dialog-body'),before=body.offsetHeight,old=body.querySelector('.credential-pane:not(.is-leaving)');
  const pane=document.createElement('div');
  pane.className='credential-pane'+(direction>0?' is-forward':direction<0?' is-back':'');
  pane.innerHTML=credentialWizardPaneHTML();
  if(old&&direction){old.className=`credential-pane is-leaving ${direction>0?'is-forward':'is-back'}`;old.querySelectorAll('input,textarea').forEach(input=>{input.value=''});setTimeout(()=>old.remove(),180)}
  else old?.remove();
  body.append(pane);
  const secretInput=pane.querySelector('[name="secret"]');if(secretInput)secretInput.value=wizard.secret||'';
  if(before&&direction){body.style.height=before+'px';requestAnimationFrame(()=>{body.style.height=pane.offsetHeight+'px';setTimeout(()=>{body.style.height=''},300)})}
  scrim.querySelectorAll('.credential-dots span').forEach((dot,index)=>{dot.className=wizard.done||index+1<wizard.step?'is-done':index+1===wizard.step?'is-current':''});
  scrim.querySelector('[data-wizard-step]').textContent=wizard.done?'已完成':`${wizard.step} / 3`;
  scrim.querySelector('[data-wizard-close]').disabled=wizard.saving;
  scrim.querySelector('.credential-dialog-foot').innerHTML=credentialWizardFootHTML();
  hydrateCredentialIcons(scrim);
  if(typeof hydrateGitHubConnections==='function')hydrateGitHubConnections(pane);
  pane.querySelectorAll('[data-github-method]').forEach(button=>{button.onclick=()=>{wizard.githubMethod=button.dataset.githubMethod;paintCredentialWizard(0);focusCredentialWizard()}});
}
function refreshCredentialWizardFoot(){
  const scrim=$('#credentialWizard');if(!scrim||!credentialWizard)return;
  scrim.querySelector('.credential-dialog-foot').innerHTML=credentialWizardFootHTML();
  hydrateCredentialIcons(scrim.querySelector('.credential-dialog-foot'));
}
function focusCredentialWizard(){
  const pane=$('#credentialWizard .credential-pane:not(.is-leaving)');
  // Deferred calls must not pull focus back once the user is already in this pane, or typing lands in the wrong field.
  if(pane?.contains(document.activeElement))return;
  (pane?.querySelector('input, textarea, .credential-opt.is-selected:not(:disabled), .credential-opt:not(:disabled)')
    ||$('#credentialWizard [data-wizard-next]:not(:disabled), #credentialWizard [data-wizard-done]')
    ||$('#credentialWizard [data-wizard-close]:not(:disabled)')
    ||$('#credentialWizard .credential-dialog'))?.focus({preventScroll:true});
}
function credentialWizardFocusables(){
  const dialog=$('#credentialWizard .credential-dialog');if(!dialog)return [];
  return [...dialog.querySelectorAll('button, a[href], input, select')].filter(el=>!el.disabled&&!el.closest('.credential-pane.is-leaving, [hidden]')&&el.getClientRects().length);
}
function markCredentialGuide(index){
  const wizard=credentialWizard;if(!wizard||wizard.guide[index])return;
  wizard.guide[index]=true;
  const item=$(`#credentialWizard .credential-pane:not(.is-leaving) [data-wizard-guide="${index}"]`);if(!item)return;
  item.classList.add('is-done');
  item.querySelector('.credential-guide-no').innerHTML=credentialGlyph('check-draw',10);
  hydrateCredentialIcons(item);
}
function credentialWizardInput(event){
  const wizard=credentialWizard,input=event.target;if(!wizard||!input.name)return;
  const kind=credentialKind(wizard.kind),pane=input.closest('.credential-pane');
  if(input.name==='extra')wizard.extra=input.value;
  if(input.name==='protocol')wizard.protocol=input.value;
  if(input.name==='secret')wizard.secret=input.value;
  const valid=credentialWizardValid(),hint=credentialWizardSecretHint(kind,wizard,valid);
  setCredentialHint(pane,'secret',hint.text,hint.tone);
  if(kind.extra?.options){if(input.name==='extra')refreshCredentialEnvironmentLinks(pane,wizard.extra)}
  else if(kind.extra){const extraError=credentialExtraError(kind,wizard.extra,wizard.protocol);setCredentialHint(pane,'extra',extraError,extraError?'err':'')}
  if(valid.ok)kind.guide.forEach((_,index)=>markCredentialGuide(index));
  const next=$('#credentialWizard [data-wizard-next]');if(next)next.disabled=!valid.ok;
}
function credentialWizardKeydown(event){
  if(!credentialWizard)return;
  if(event.key==='Escape'){event.preventDefault();event.stopPropagation();closeCredentialWizard();return}
  if(event.key==='Tab'){
    const items=credentialWizardFocusables(),dialog=$('#credentialWizard .credential-dialog');if(!dialog)return;
    if(!items.length){event.preventDefault();dialog.focus({preventScroll:true});return}
    const index=items.indexOf(document.activeElement);
    if(index<0||(event.shiftKey&&index===0)||(!event.shiftKey&&index===items.length-1)){event.preventDefault();items[event.shiftKey?items.length-1:0].focus({preventScroll:true})}
    return;
  }
  if(event.key==='Enter'&&event.target.matches?.('#credentialWizard .credential-pane input')){event.preventDefault();advanceCredentialWizard()}
}
function selectCredentialWizardKind(key){
  const wizard=credentialWizard;if(!wizard||wizard.kind===key)return;
  // A secret typed for one kind must never be submitted as another kind's credential.
  wizard.kind=key;wizard.secret='';wizard.guide={};wizard.error='';
  wizard.extra=credentialWizardDefaults(credentialKind(key),credentialUser());
  wizard.protocol=credentialProtocol(credentialKind(key),credentialUser());
}
function credentialWizardClick(event){
  const wizard=credentialWizard,target=event.target;if(!wizard)return;
  if(target.closest('[data-wizard-close]'))return closeCredentialWizard();
  if(target.closest('[data-wizard-done]'))return closeCredentialWizard();
  const eye=target.closest('[data-credential-eye]');if(eye)return toggleCredentialSecret(eye);
  const link=target.closest('[data-wizard-link]');if(link)return markCredentialGuide(Number(link.dataset.wizardLink));
  const option=target.closest('[data-wizard-kind]');
  if(option&&!option.disabled){
    selectCredentialWizardKind(option.dataset.wizardKind);
    option.parentElement.closest('.credential-pane').querySelectorAll('[data-wizard-kind]').forEach(item=>{const on=item===option;item.classList.toggle('is-selected',on);item.setAttribute('aria-checked',String(on))});
    refreshCredentialWizardFoot();
    return;
  }
  const back=target.closest('[data-wizard-back]');
  if(back&&!back.disabled){wizard.step--;wizard.error='';paintCredentialWizard(-1);setTimeout(focusCredentialWizard,220);return}
  const next=target.closest('[data-wizard-next]');if(next&&!next.disabled)advanceCredentialWizard();
}
async function advanceCredentialWizard(){
  const wizard=credentialWizard;if(!wizard||wizard.saving||wizard.done)return;
  if(wizard.step===1){
    if(!wizard.kind||(wizard.kind!=='gitSshKey'&&credentialConfigured(credentialKind(wizard.kind),credentialUser())))return;
    if(wizard.kind==='gitSshKey'){
      closeCredentialWizard(true);
      return openGitSSHKeys();
    }
    wizard.step=2;paintCredentialWizard(1);setTimeout(focusCredentialWizard,220);return;
  }
  if(wizard.step===2){
    if(!credentialWizardValid().ok){const pane=$('#credentialWizard .credential-pane:not(.is-leaving)');if(pane)setCredentialHint(pane,'secret',credentialWizardValid().error||'请先填写密钥','err');return}
    wizard.step=3;wizard.error='';paintCredentialWizard(1);setTimeout(focusCredentialWizard,220);return;
  }
  const kind=credentialKind(wizard.kind),secret=String(wizard.secret).trim();
  wizard.saving=true;wizard.error='';paintCredentialWizard(0);focusCredentialWizard();
  try{
    if(kind.key==='gitToken'){
      const outcome=await verifyAndSaveGitToken(secret);
      if(!outcome.saved){wizard.error=`${gitTokenFailure(outcome.result)||'Git Token 校验未通过'}（未保存）`;return}
    }else await saveCredential(kind.key,{secret,...(kind.extra?{extra:wizard.extra}:{}),...(kind.protocol?{protocol:wizard.protocol}:{})});
    wizard.done=true;wizard.secret='';
    renderCredentialPanel();
    toast(kind.key==='gitToken'?'Git Token 校验通过，已保存':`${kind.short} 已保存`,'ok');
  }catch(err){wizard.error=err.message}
  finally{
    if(credentialWizard===wizard){wizard.saving=false;paintCredentialWizard(0);focusCredentialWizard()}
  }
}

// Gongfeng OAuth completes in its own tab, which broadcasts from gongfeng-oauth-done.js.
// Called once at startup; other views ignore the signal and pick up the grant on their next render.
function listenForGongfengOAuth(){
  if(typeof BroadcastChannel!=='function')return null;
  const channel=new BroadcastChannel('agentflow-gongfeng-oauth');
  channel.onmessage=event=>{
    if(event.data?.type!=='gongfeng-oauth-completed'||activeViewName()!=='settings')return;
    refreshGongfengGrant().catch(()=>{});
  };
  return channel;
}
// Patch only the Gongfeng row: re-rendering the panel would drop inputs typed into other rows.
async function refreshGongfengGrant(){
  const user=state.currentUser;if(!user||user.isGuest||!$('#credentialPanel'))return;
  const users=await api('/api/users',{silent:true});
  const fresh=(Array.isArray(users)?users:[]).find(u=>Number(u.id)===Number(user.id));
  const panel=$('#credentialPanel'),row=panel?.querySelector('[data-credential-row="gitToken"]');
  if(!fresh||!row||Number(state.currentUser?.id)!==Number(user.id))return;
  storeCredentialUser(fresh);
  state.gitTokenVerify=null;
  if(credentialWizard?.kind==='gitToken'&&fresh.gitTokenConfigured)closeCredentialWizard(true);
  if(row.classList.contains('is-configured')!==!!fresh.gitTokenConfigured){
    const holder=document.createElement('div');
    holder.innerHTML=credentialRowHTML(credentialKind('gitToken'),fresh);
    row.replaceWith(holder.firstElementChild);
    panel.querySelector('.credential-progress').innerHTML=credentialProgressHTML(fresh);
    panel.querySelector('.credential-filter').innerHTML=credentialFilterHTML(fresh);
    applyCredentialFilter(panel,fresh);
    hydrateCredentialIcons(panel);
  }
  if(!fresh.gitTokenConfigured){repaintCredentialRow('gitToken');return}
  state.gitTokenAnnounceVerify=true;
  toast('工蜂授权已保存，正在校验授权有效性…');
  await refreshGitTokenStatus();
}

async function renderPersonalSettings(){
  const host=$('#personalSettingsContent');if(!host)return;
  const user=state.currentUser;
  if(!user||user.isGuest){
    host.innerHTML='<section class="personal-settings-card"><h2>登录账号</h2><p>登录后即可管理自己的 Git Token 和 API Key。</p><button class="button primary" id="settingsLogin">账号登录</button><button class="button ghost" id="localAcceptanceLogin" hidden>本地验收登录</button></section>';
    host.querySelector('#settingsLogin').onclick=()=>{window.location.href=state.loginUrl||'/login.html'};
    try{const capability=await api('/api/auth/local',{silent:true}),button=host.querySelector('#localAcceptanceLogin');if(button&&capability.enabled){button.hidden=false;button.onclick=async()=>{try{const result=await api('/api/auth/local',{method:'POST',body:'{}'});state.currentUser=result.user;state.data.users=await api('/api/users');window.AgentFlowExecutorModels=null;window.AgentFlowAssistantModels?.load?.();renderUserBadge();renderPersonalSettings();toast('已登录本地验收账号')}catch(err){toast(err.message)}}}}catch{}
    return;
  }
  const storedUser=userById(user.id)||{};
  const gongfengInURL=new URLSearchParams(location.search).get('gongfeng')==='ok';
  if(state.gongfengReturned||gongfengInURL){
    state.gongfengReturned=false;
    if(gongfengInURL)history.replaceState(null,'',location.pathname+location.hash);
    state.gitTokenVerify=null;
    state.gitTokenAnnounceVerify=true;
    toast('工蜂授权已保存，正在校验授权有效性…');
  }
  if(credentialWizard)closeCredentialWizard(true);
  host.innerHTML=`<section class="credential-account credential-enter" style="--i:0"><span class="credential-avatar">${userAvatarHTML(user, 150)}</span><div><b>${escapeHTML(userDisplay(user))}</b><small>凭证按用户隔离，仅在你负责的任务运行时注入。</small></div><span class="credential-account-note">${credentialGlyph('lock',14)}任务级注入，不写入 Agent 资产</span></section><section class="credential-panel credential-enter is-entering" id="credentialPanel" style="--i:1"></section>`;
  const panel=host.querySelector('#credentialPanel');
  bindCredentialPanel(panel);
  renderCredentialPanel();
  hydrateCredentialIcons(host);
  setTimeout(()=>panel.classList.remove('is-entering'),900);
  // Offer the OAuth shortcut only when the server has a Gongfeng application.
  api('/api/auth/gongfeng',{silent:true}).then(capability=>{state.gongfengOAuthEnabled=!!capability?.enabled;document.querySelectorAll('[data-gongfeng-oauth]').forEach(el=>{el.hidden=!state.gongfengOAuthEnabled})}).catch(()=>{});
  if(storedUser.gitTokenConfigured){
    const cached=gitTokenVerifyFor(user);
    if(!cached||(cached.busy&&!state.gitTokenVerifyInFlight)){state.gitTokenVerifyInFlight=true;refreshGitTokenStatus().finally(()=>{state.gitTokenVerifyInFlight=false})}
  }
}
// 谁能改通用模板是平台级权限，不属于任何一个项目，也不属于个人凭据；
// 只有平台管理员能看到「平台管理」入口。
function renderPlatformNav(){
  const group=$('#platformNavGroup');if(!group)return;
  group.hidden=!isPlatformAdmin();
}
// 平台管理分成三个 tab：成员账号、平台管理员、共享 CodeBuddy Key。
const PLATFORM_TABS=[
  {id:'accounts',zh:'成员账号',en:'Accounts'},
  {id:'admins',zh:'平台管理员',en:'Admins'},
  {id:'sharedKey',zh:'共享 CodeBuddy Key',en:'Shared CodeBuddy Key'},
];
function platformTabsHTML(active){
  return `<div class="pa-tabs-wrap platform-tabs-wrap"><div class="pa-tabs platform-tabs" id="platformTabs" role="tablist" aria-label="平台管理分区">${PLATFORM_TABS.map(t=>{
    const on=t.id===active;
    const extra=t.id==='sharedKey'?'<span class="platform-tab-state" data-platform-tab-state hidden></span>':'<span class="pa-tab-n" data-platform-tab-count hidden></span>';
    return `<button type="button" role="tab" class="pa-tab${on?' on':''}" id="platformTab-${t.id}" data-platform-tab="${t.id}" aria-controls="platformPanel-${t.id}" aria-selected="${on}" tabindex="${on?0:-1}">${lfT(t.zh,t.en)}${extra}</button>`;
  }).join('')}</div></div>`;
}
function platformTabPanel(id,active,body){
  return `<section class="platform-tab-panel" id="platformPanel-${id}" role="tabpanel" aria-labelledby="platformTab-${id}" data-platform-panel="${id}"${id===active?'':' hidden'}>${body}</section>`;
}
function setPlatformTab(host,tab){
  if(!PLATFORM_TABS.some(t=>t.id===tab))tab=PLATFORM_TABS[0].id;
  state.platformTab=tab;
  host.querySelectorAll('[data-platform-tab]').forEach(b=>{const on=b.dataset.platformTab===tab;b.classList.toggle('on',on);b.setAttribute('aria-selected',String(on));b.tabIndex=on?0:-1});
  host.querySelectorAll('[data-platform-panel]').forEach(p=>{p.hidden=p.dataset.platformPanel!==tab});
}
function bindPlatformTabs(host){
  const bar=host.querySelector('#platformTabs');if(!bar)return;
  bar.querySelectorAll('[data-platform-tab]').forEach(b=>b.onclick=()=>setPlatformTab(host,b.dataset.platformTab));
  bar.onkeydown=e=>{
    if(!['ArrowLeft','ArrowRight'].includes(e.key))return;
    const ids=PLATFORM_TABS.map(t=>t.id),i=ids.indexOf(state.platformTab),next=ids[(i+(e.key==='ArrowRight'?1:ids.length-1))%ids.length];
    setPlatformTab(host,next);bar.querySelector(`[data-platform-tab="${next}"]`)?.focus();
  };
}
function setPlatformTabCount(host,tab,n){
  const el=host.querySelector(`[data-platform-tab="${tab}"] [data-platform-tab-count]`);
  if(el){el.textContent=String(n);el.hidden=false}
}
function renderPlatformAdminPanel(){
  const host=$('#platformAdminPanel');if(!host)return;
  if(!isPlatformAdmin()){host.innerHTML='';return}
  const tab=PLATFORM_TABS.some(t=>t.id===state.platformTab)?state.platformTab:PLATFORM_TABS[0].id;
  state.platformTab=tab;
  host.innerHTML=`
    ${platformTabsHTML(tab)}
    ${platformTabPanel('accounts',tab,platformAccountsHTML())}
    ${platformTabPanel('admins',tab,`
    <div class="platform-metric-grid">
      <div class="platform-metric-card">
        <div class="platform-metric-icon blue">
          <svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2"><path d="M16 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="8.5" cy="7" r="4"/><polyline points="17 11 19 13 23 9"/></svg>
        </div>
        <div class="platform-metric-info">
          <span class="platform-metric-label">平台管理员</span>
          <b class="platform-metric-num" id="platformMetricTotal">--</b>
          <small>具有跨项目通用模板与角色编辑权</small>
        </div>
      </div>
      <div class="platform-metric-card">
        <div class="platform-metric-icon amber">
          <svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="11" width="18" height="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>
        </div>
        <div class="platform-metric-info">
          <span class="platform-metric-label">系统内置</span>
          <b class="platform-metric-num" id="platformMetricLocked">--</b>
          <small>系统初始预设名单，不可撤销</small>
        </div>
      </div>
      <div class="platform-metric-card">
        <div class="platform-metric-icon purple">
          <svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg>
        </div>
        <div class="platform-metric-info">
          <span class="platform-metric-label">界面授权</span>
          <b class="platform-metric-num" id="platformMetricGranted">--</b>
          <small>控制台动态授予，可随时撤销</small>
        </div>
      </div>
      <div class="platform-metric-card">
        <div class="platform-metric-icon green">
          <svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/></svg>
        </div>
        <div class="platform-metric-info">
          <span class="platform-metric-label">生效范围</span>
          <b class="platform-metric-num">全平台通用</b>
          <small>通用模板 · 共享角色 · 门禁规范</small>
        </div>
      </div>
    </div>

    <div class="platform-admin-layout">
      <div class="platform-admin-main-card">
        <div class="platform-admin-head">
          <div class="platform-admin-head-meta">
            <h2>谁可以改通用模板</h2>
            <p>平台管理员能直接编辑通用模板和它打包的角色，改完所有项目下次运行生效。系统内置名单不能在这里撤销。</p>
          </div>
          <span class="platform-admin-count-pill" id="platformAdminCountBadge">-- 位管理员</span>
        </div>

        <div class="platform-add-box">
          <form id="platformAdminForm" class="platform-admin-form">
            <div class="platform-input-wrap">
              <span class="platform-input-icon">
                <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>
              </span>
              <input name="loginName" placeholder="输入用户名，如 alice" autocomplete="off">
            </div>
            <button class="button primary platform-add-btn" type="submit">添加管理员</button>
          </form>
          <div class="platform-add-hint">
            <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
            <span>授权后用户将获得全平台通用 SOP 模板及公共角色的修改与发布权限，请谨慎授予。</span>
          </div>
        </div>

        <div class="platform-list-toolbar">
          <div class="platform-search-wrap">
            <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></svg>
            <input id="platformAdminSearch" placeholder="搜索姓名或用户名..." autocomplete="off">
          </div>
          <div class="platform-filter-tabs" id="platformFilterTabs">
            <button type="button" class="platform-filter-btn active" data-source-filter="all">全部</button>
            <button type="button" class="platform-filter-btn" data-source-filter="builtin">内置</button>
            <button type="button" class="platform-filter-btn" data-source-filter="granted">已授权</button>
          </div>
        </div>

        <div id="platformAdminList" class="platform-admin-list"></div>
      </div>

      <div class="platform-admin-side">
        <div class="platform-side-card">
          <div class="platform-side-head">
            <span class="platform-side-icon blue">
              <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/></svg>
            </span>
            <h3>权限与安全规则</h3>
          </div>
          <ul class="platform-side-list">
            <li>
              <b>通用 SOP 模板全局编辑</b>
              <p>通用模板面向全平台所有项目共享，由平台管理员统一定义并维护，修改后各项目下次运行即刻生效。</p>
            </li>
            <li>
              <b>公共角色与通用门禁下发</b>
              <p>通用模板中引用的 Agent 角色、执行门禁由平台统一调优升级，项目内默认只读，保障基准质量。</p>
            </li>
            <li>
              <b>系统固化名单安全保护</b>
              <p>系统内置名单具备最高防锁死安全防护，无法通过页面撤销，确保生产管理连续性。</p>
            </li>
          </ul>
        </div>

        <div class="platform-side-card">
          <div class="platform-side-head">
            <span class="platform-side-icon green">
              <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><polygon points="12 2 2 7 12 12 22 7 12 2"/><polyline points="2 17 12 22 22 17"/><polyline points="2 12 12 17 22 12"/></svg>
            </span>
            <h3>通用模板与资产管理</h3>
          </div>
          <p class="platform-side-desc">平台管理员在流程设计或角色定义中维护通用模板后，所有关联项目将在下次运行自动应用最新版本。</p>
          <div class="platform-side-quick-links">
            <button type="button" class="button ghost small platform-quick-link-btn" data-platform-nav="sops">查看 SOP 流程 ↗</button>
            <button type="button" class="button ghost small platform-quick-link-btn" data-platform-nav="agents">查看角色定义 ↗</button>
          </div>
          <div class="platform-audit-note">
            <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/></svg>
            <span>管理员授予与撤销操作均实时生效，变更将记录平台审计。</span>
          </div>
        </div>
      </div>
    </div>
    `)}
    ${platformTabPanel('sharedKey',tab,platformSharedKeyHTML())}
  `;
  bindPlatformTabs(host);
  loadPlatformAccounts(host);
  loadPlatformSharedKey(host);
  loadPlatformAdmins(host);
}

// 平台管理 → 成员账号 / 平台共享 CodeBuddy Key.
const PLATFORM_ACCOUNT_PASSWORD_MIN=6;
function platformRandomPassword(){
  const alphabet='ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789';
  const bytes=new Uint32Array(14);window.crypto.getRandomValues(bytes);
  return Array.from(bytes,b=>alphabet[b%alphabet.length]).join('');
}
function sharedModelLabel(id){return id==='deepseek-v4.1-flash'?'Deepseek-V4.1-Flash':String(id||'')}
// The models users without their own CodeBuddy key may use, as announced by bootstrap.
function sharedModelSummary(){
  const models=Array.isArray(state.data?.sponsoredModels)&&state.data.sponsoredModels.length?state.data.sponsoredModels:(state.data?.sponsoredModel?[state.data.sponsoredModel]:[]);
  return models.map(sharedModelLabel).join('、');
}
const PLATFORM_ICON_USER='<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>';
const PLATFORM_ICON_LOCK='<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="11" width="18" height="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>';
const PLATFORM_ICON_SEARCH='<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></svg>';
function platformAccountsHTML(){
  return `
    <section class="platform-admin-main-card" id="platformAccountsCard">
      <div class="platform-admin-head">
        <div class="platform-admin-head-meta">
          <h2>给同事开账号</h2>
          <p data-account-intro>填好用户名和初始密码，点创建，再把登录信息发给同事。同事忘记密码时可以在下面重置。</p>
        </div>
        <span class="platform-admin-count-pill" id="platformAccountCountBadge">${lfT('-- 个账号','-- accounts')}</span>
      </div>
      <div class="platform-account-create">
        <form id="platformAccountForm" class="platform-account-form" autocomplete="off">
          <div class="platform-input-wrap"><span class="platform-input-icon">${PLATFORM_ICON_USER}</span><input name="loginName" placeholder="用户名，如 alice" aria-label="用户名" maxlength="64" autocomplete="off" spellcheck="false"></div>
          <div class="platform-input-wrap"><span class="platform-input-icon">${PLATFORM_ICON_LOCK}</span><input name="password" placeholder="初始密码，至少 6 位" aria-label="初始密码" maxlength="72" autocomplete="new-password" spellcheck="false"><button type="button" class="button ghost small" data-account-generate>随机生成</button></div>
          <label class="platform-check"><input type="checkbox" name="admin"><span>同时设为平台管理员</span></label>
          <button class="button primary platform-add-btn" type="submit">创建账号</button>
        </form>
        <div class="platform-account-created" data-account-created hidden></div>
      </div>
      <div class="platform-list-toolbar">
        <div class="platform-search-wrap platform-account-search">${PLATFORM_ICON_SEARCH}<input data-account-search placeholder="搜索用户名..." autocomplete="off"></div>
      </div>
      <div id="platformAccountList" class="platform-admin-list platform-account-list"></div>
    </section>`;
}
function platformSharedKeyHTML(){
  const envOptions=CODEBUDDY_ENVIRONMENTS.map(env=>`<option value="${escapeHTML(env.value)}">${escapeHTML(env.label)} · ${escapeHTML(env.site)}</option>`).join('');
  return `
    <section class="platform-admin-main-card" id="platformSharedKeyCard">
      <div class="platform-admin-head">
        <div class="platform-admin-head-meta">
          <h2>给没配 Key 的成员用</h2>
          <p>成员没配自己的 CodeBuddy API Key 时，对话和 SOP 会用这里的 Key，并且只能用下面勾选的模型。成员配了自己的 Key 就用自己的，不受限制。</p>
        </div>
        <span class="platform-admin-count-pill platform-status-pill" data-shared-key-badge>${lfT('加载中','Loading')}</span>
      </div>
      <form id="platformSharedKeyForm" class="platform-shared-key-form" autocomplete="off">
        <div class="platform-shared-key-grid">
          <div class="platform-shared-key-col">
            <label class="platform-field"><span>网络环境</span><select name="internetEnvironment">${envOptions}</select></label>
            <div class="platform-field-group">
              <label class="platform-field"><span>API Key</span><input name="apiKey" type="password" autocomplete="new-password" spellcheck="false" placeholder="ck_ 开头的 CodeBuddy API Key"></label>
              <a class="platform-shared-key-link" data-shared-key-link target="_blank" rel="noopener noreferrer"></a>
            </div>
            <div class="platform-note is-warn" data-shared-key-warning hidden></div>
            <label class="platform-field"><span>默认模型</span><select name="defaultModel"></select><small class="platform-field-hint">没指定模型时用它；SOP 角色的模型不在允许范围内时也改用它。</small></label>
          </div>
          <div class="platform-shared-key-col">
            <div class="platform-field">
              <div class="platform-field-head"><span>允许使用的模型</span><small data-shared-models-count></small></div>
              <div class="platform-model-list" data-shared-models><p class="prop-hint">正在读取可用模型…</p></div>
            </div>
            <label class="platform-field"><span>列表里没有的模型</span><input name="extraModels" placeholder="模型 ID，多个用逗号分隔" autocomplete="off" spellcheck="false"></label>
          </div>
        </div>
        <div class="platform-shared-key-footer">
          <p class="platform-add-hint" data-shared-key-meta></p>
          <div class="platform-shared-key-actions">
            <button class="button ghost danger" type="button" data-shared-key-clear hidden>停用共享 Key</button>
            <button class="button primary" type="submit">保存</button>
          </div>
        </div>
      </form>
    </section>`;
}
function platformAccountCreatedHTML(action,login,password){
  const who=`<b data-i18n-skip>${escapeHTML(login)}</b>`,secret=`<code>${escapeHTML(password)}</code>`;
  const text=action==='reset'
    ?lfT(`已重置账号 ${who}，新密码 ${secret}。密码只显示这一次，请现在发给对方。`,`Password of ${who} reset to ${secret}. It is shown only once; send it now.`)
    :lfT(`已创建账号 ${who}，初始密码 ${secret}。密码只显示这一次，请现在发给对方。`,`Created ${who} with the initial password ${secret}. It is shown only once; send it now.`);
  return `<div class="platform-note is-ok"><span>${text}</span><button type="button" class="button ghost small" data-copy-login>复制登录信息</button></div>`;
}
const platformPasswordTooShort=()=>lfT(`密码至少 ${PLATFORM_ACCOUNT_PASSWORD_MIN} 位`,`The password needs at least ${PLATFORM_ACCOUNT_PASSWORD_MIN} characters`);
async function loadPlatformAccounts(host){
  const card=host.querySelector('#platformAccountsCard');if(!card)return;
  const list=card.querySelector('#platformAccountList'),form=card.querySelector('#platformAccountForm'),created=card.querySelector('[data-account-created]'),search=card.querySelector('[data-account-search]');
  let accounts=[],query='',resetting=null;
  const showCreated=(action,login,password)=>{
    created.innerHTML=platformAccountCreatedHTML(action,login,password);created.hidden=false;
    created.querySelector('[data-copy-login]').onclick=()=>copyText(lfT(`登录地址：${location.origin}\n用户名：${login}\n密码：${password}`,`Sign in at: ${location.origin}\nLogin: ${login}\nPassword: ${password}`),'已复制登录信息');
  };
  const render=()=>{
    const n=accounts.length,badge=card.querySelector('#platformAccountCountBadge');if(badge)badge.textContent=lfT(`共 ${n} 个账号`,`${n} account${n===1?'':'s'}`);
    setPlatformTabCount(host,'accounts',n);
    const q=query.toLowerCase();
    const rows=accounts.filter(a=>!q||String(a.loginName||'').toLowerCase().includes(q)||String(a.chineseName||'').toLowerCase().includes(q));
    if(!rows.length){list.innerHTML=`<div class="platform-empty-state"><b>${accounts.length?'没有匹配的账号':'还没有账号'}</b></div>`;return}
    list.innerHTML=rows.map(a=>{
      const name=a.chineseName&&a.chineseName.toLowerCase()!==String(a.loginName).toLowerCase()?`${a.loginName}(${a.chineseName})`:a.loginName;
      const badges=[a.admin?'<span class="platform-source-badge source-builtin">平台管理员</span>':'',a.password?'<span class="platform-source-badge source-default">用户名密码</span>':'<span class="platform-source-badge source-default">GitHub / 网关登录</span>',a.codeBuddyApiKeyConfigured?'<span class="platform-source-badge source-env">已配 CodeBuddy Key</span>':'<span class="platform-source-badge source-default">未配 CodeBuddy Key</span>'].join('');
      const editing=resetting===a.userId;
      return `<div class="platform-admin-row platform-account-row" data-account-row="${Number(a.userId)}">
        <div class="platform-admin-user">
          <div class="platform-admin-avatar">${userAvatarHTML(a.loginName,100)}</div>
          <div class="platform-admin-info"><span class="platform-admin-name" data-i18n-skip>${escapeHTML(name)}</span><div class="platform-admin-meta">${badges}</div></div>
        </div>
        <div class="platform-admin-actions">${a.password?(editing?`<form class="platform-account-reset" data-reset-form="${Number(a.userId)}"><input name="password" placeholder="新密码，至少 6 位" aria-label="新密码" maxlength="72" autocomplete="new-password" spellcheck="false"><button type="button" class="button ghost small" data-reset-generate>随机</button><button type="submit" class="button primary small">确认重置</button><button type="button" class="button ghost small" data-reset-cancel>取消</button></form>`:`<button type="button" class="button ghost small" data-reset-account="${Number(a.userId)}">重置密码</button>`):'<span class="platform-locked-tag" title="通过 GitHub 或网关登录的账号没有平台密码">无平台密码</span>'}</div>
      </div>`;
    }).join('');
    list.querySelectorAll('[data-reset-account]').forEach(btn=>btn.onclick=()=>{resetting=Number(btn.dataset.resetAccount);render();list.querySelector('[data-reset-form] input')?.focus()});
    const resetForm=list.querySelector('[data-reset-form]');
    if(resetForm){
      resetForm.querySelector('[data-reset-cancel]').onclick=()=>{resetting=null;render()};
      resetForm.querySelector('[data-reset-generate]').onclick=()=>{resetForm.elements.password.value=platformRandomPassword()};
      resetForm.onsubmit=async e=>{
        e.preventDefault();
        const password=resetForm.elements.password.value;
        if(password.length<PLATFORM_ACCOUNT_PASSWORD_MIN)return toast(platformPasswordTooShort(),'err');
        const account=accounts.find(a=>a.userId===resetting);
        resetForm.querySelectorAll('button').forEach(b=>b.disabled=true);
        try{
          await api(`/api/platform/accounts/${encodeURIComponent(resetting)}/password`,{method:'PUT',body:JSON.stringify({password})});
          resetting=null;render();showCreated('reset',account?.loginName||'',password);toast('密码已重置','ok');
        }catch(err){toast(err.message,'err');resetForm.querySelectorAll('button').forEach(b=>b.disabled=false)}
      };
    }
  };
  const load=async()=>{
    try{
      const result=await api('/api/platform/accounts');
      accounts=Array.isArray(result?.accounts)?result.accounts:[];
      const intro=card.querySelector('[data-account-intro]');
      if(intro)intro.textContent=result?.signupEnabled
        ?'登录页目前允许自助注册。也可以在这里直接创建账号，再把登录信息发给同事；同事忘记密码时可以在下面重置。'
        :'登录页的自助注册已关闭，同事的账号要在这里创建：填好用户名和初始密码，点创建，再把登录信息发给对方。忘记密码时可以在下面重置。';
      render();
    }catch(e){list.innerHTML=`<p class="prop-hint">${escapeHTML(e.message||'加载账号失败')}</p>`}
  };
  if(search)search.oninput=()=>{query=search.value.trim();render()};
  form.querySelector('[data-account-generate]').onclick=()=>{form.elements.password.value=platformRandomPassword()};
  form.onsubmit=async e=>{
    e.preventDefault();
    const loginName=form.elements.loginName.value.trim(),password=form.elements.password.value,admin=form.elements.admin.checked;
    if(!/^[A-Za-z0-9][A-Za-z0-9_.-]{1,63}$/.test(loginName))return toast('用户名需 2–64 位，以字母或数字开头，只能包含字母、数字、_ . -','err');
    if(password.length<PLATFORM_ACCOUNT_PASSWORD_MIN)return toast(platformPasswordTooShort(),'err');
    const submit=form.querySelector('button[type="submit"]');if(submit)submit.disabled=true;
    try{
      const account=await api('/api/platform/accounts',{method:'POST',body:JSON.stringify({loginName,password,admin})});
      form.reset();
      accounts=[...accounts.filter(a=>a.userId!==account?.userId),account].filter(Boolean);
      render();showCreated('create',loginName,password);toast('账号已创建','ok');
      if(admin)loadPlatformAdmins(host);
    }catch(err){toast(err.message,'err')}
    finally{if(submit)submit.disabled=false}
  };
  await load();
}
async function loadPlatformSharedKey(host){
  const card=host.querySelector('#platformSharedKeyCard');if(!card)return;
  const form=card.querySelector('#platformSharedKeyForm');
  const env=form.elements.internetEnvironment,key=form.elements.apiKey,extra=form.elements.extraModels,defaultSelect=form.elements.defaultModel;
  const modelsHost=card.querySelector('[data-shared-models]'),badge=card.querySelector('[data-shared-key-badge]'),meta=card.querySelector('[data-shared-key-meta]');
  const link=card.querySelector('[data-shared-key-link]'),warning=card.querySelector('[data-shared-key-warning]'),clear=card.querySelector('[data-shared-key-clear]');
  let status=null,catalog=[],selected=[];
  const extraIDs=()=>String(extra.value||'').split(/[,，\s]+/).map(s=>s.trim()).filter(Boolean);
  const chosen=()=>{const all=[...selected];for(const id of extraIDs())if(!all.includes(id))all.push(id);return all};
  const modelCount=card.querySelector('[data-shared-models-count]'),tabState=host.querySelector('[data-platform-tab-state]');
  const renderDefault=()=>{
    const models=chosen(),current=defaultSelect.value;
    if(modelCount)modelCount.textContent=models.length?lfT(`已选 ${models.length} 个`,`${models.length} selected`):'';
    defaultSelect.innerHTML=models.length?models.map(id=>`<option value="${escapeHTML(id)}">${escapeHTML(sharedModelLabel(id))}</option>`).join(''):'<option value="">请先勾选模型</option>';
    defaultSelect.value=models.includes(current)?current:(models[0]||'');
  };
  const renderModels=()=>{
    const listed=catalog.filter(m=>m&&m.id&&m.id!=='default');
    const ids=new Set(listed.map(m=>m.id));
    for(const id of selected)if(!ids.has(id)){listed.push({id,name:id});ids.add(id)}
    modelsHost.innerHTML=listed.length?listed.map(m=>`<label class="platform-check platform-model-option"><input type="checkbox" value="${escapeHTML(m.id)}"${selected.includes(m.id)?' checked':''}><span>${escapeHTML(m.name&&m.name!==m.id?`${m.name}（${m.id}）`:sharedModelLabel(m.id))}</span>${m.credits?`<small>${escapeHTML(m.credits)}</small>`:''}</label>`).join(''):'<p class="prop-hint">这个网络环境暂时没有读到模型列表，可以在下面直接填模型 ID。</p>';
    modelsHost.querySelectorAll('input[type="checkbox"]').forEach(box=>box.onchange=()=>{
      selected=box.checked?[...selected.filter(id=>id!==box.value),box.value]:selected.filter(id=>id!==box.value);
      renderDefault();
    });
    renderDefault();
  };
  const renderLink=()=>{link.href=codeBuddyKeyURL(env.value);link.textContent=`${codeBuddyKeyLinkLabel(env.value)} ↗`};
  const renderStatus=()=>{
    const on=!!status?.configured,envInfo=CODEBUDDY_ENVIRONMENTS.find(e=>e.value===(status?.internetEnvironment||''))||CODEBUDDY_ENVIRONMENTS[0];
    const tone=on?'on':status?.legacySponsor?'legacy':'off';
    badge.textContent=on?'已启用':status?.legacySponsor?'使用旧配置':'未启用';
    badge.classList.remove('is-on','is-legacy','is-off');badge.classList.add(`is-${tone}`);
    if(tabState){tabState.className=`platform-tab-state is-${tone}`;tabState.title=badge.textContent;tabState.hidden=false}
    key.placeholder=on?'已配置，留空保持不变':'ck_ 开头的 CodeBuddy API Key';
    clear.hidden=!on;
    const parts=[];
    if(on){
      const n=(status.models||[]).length,first=sharedModelLabel(status.models?.[0]),by=status.updatedBy||'';
      parts.push(lfT(`当前：${envInfo.label}，${n} 个模型，默认 ${first}${by?`，${by} 最后修改`:''}。`,`Current: ${envInfo.label}, ${n} model${n===1?'':'s'}, default ${first}${by?`, last changed by ${by}`:''}.`));
    }
    else parts.push(lfT('还没有配置共享 Key，没配 Key 的成员需要先配自己的 Key 才能对话和跑 SOP。','No shared key yet; members without their own key must add one before chatting or running SOPs.'));
    const sponsor=status?.legacySponsor;
    if(sponsor)parts.push(on
      ?lfT(`配置后不再使用环境变量 AGENTFLOW_ASSISTANT_SPONSOR_LOGIN 指定的账号 ${sponsor} 的 Key。`,`The key of ${sponsor} (AGENTFLOW_ASSISTANT_SPONSOR_LOGIN) is no longer used.`)
      :lfT(`现在沿用环境变量 AGENTFLOW_ASSISTANT_SPONSOR_LOGIN 指定的账号 ${sponsor} 的 Key，只能用 ${sharedModelLabel(status.legacySponsorModel)}。`,`For now the key of ${sponsor} (AGENTFLOW_ASSISTANT_SPONSOR_LOGIN) is shared, limited to ${sharedModelLabel(status.legacySponsorModel)}.`));
    meta.textContent=parts.join(lfT('',' '));
  };
  const syncBootstrap=()=>{
    if(!state.data)return;
    const models=status?.configured?(status.models||[]):(status?.legacySponsor?[status.legacySponsorModel]:[]);
    state.data.sponsoredModels=models;state.data.sponsoredModel=models[0]||'';
  };
  const fetchStatus=async environment=>{
    const query=environment===undefined?'':`?environment=${encodeURIComponent(environment)}`;
    const result=await api('/api/platform/codebuddy-key'+query);
    catalog=Array.isArray(result?.catalog)?result.catalog:[];
    return result;
  };
  try{
    status=await fetchStatus();
    env.value=status?.internetEnvironment||'';
    selected=status?.configured?[...(status.models||[])]:(status?.legacySponsorModel?[status.legacySponsorModel]:[]);
    renderStatus();renderLink();renderModels();
  }catch(e){
    badge.textContent='加载失败';modelsHost.innerHTML=`<p class="prop-hint">${escapeHTML(e.message||'加载共享 Key 失败')}</p>`;
    return;
  }
  env.onchange=async()=>{
    renderLink();
    modelsHost.innerHTML='<p class="prop-hint">正在读取可用模型…</p>';
    try{await fetchStatus(env.value)}catch(e){catalog=[]}
    renderModels();
  };
  extra.oninput=renderDefault;
  key.oninput=()=>{const text=credentialSecretWarning(credentialKind('codeBuddyApiKey'),key.value);warning.textContent=text;warning.hidden=!text};
  form.onsubmit=async e=>{
    e.preventDefault();
    const models=chosen();
    if(!models.length)return toast('请至少勾选一个允许使用的模型','err');
    if(!status?.configured&&!key.value.trim())return toast('请填写 CodeBuddy API Key','err');
    const first=defaultSelect.value&&models.includes(defaultSelect.value)?defaultSelect.value:models[0];
    const ordered=[first,...models.filter(id=>id!==first)];
    const submit=form.querySelector('button[type="submit"]');if(submit)submit.disabled=true;
    try{
      status=await api('/api/platform/codebuddy-key',{method:'PUT',body:JSON.stringify({apiKey:key.value.trim(),internetEnvironment:env.value,models:ordered})});
      key.value='';warning.hidden=true;extra.value='';selected=[...(status.models||ordered)];
      renderStatus();renderModels();syncBootstrap();toast('共享 Key 已保存','ok');
    }catch(err){toast(err.message,'err')}
    finally{if(submit)submit.disabled=false}
  };
  clear.onclick=async()=>{
    if(!await afConfirm({title:'停用共享 Key',message:'停用后，没配自己 Key 的成员将不能再用共享 Key 对话和跑 SOP。确定停用？',danger:true,confirmText:'停用'}))return;
    try{status=await api('/api/platform/codebuddy-key',{method:'DELETE'});renderStatus();syncBootstrap();toast('共享 Key 已停用','ok')}
    catch(err){toast(err.message,'err')}
  };
}
async function loadPlatformAdmins(host){
  const list=host.querySelector('#platformAdminList');
  const form=host.querySelector('#platformAdminForm');
  const searchInput=host.querySelector('#platformAdminSearch');
  const filterTabs=host.querySelectorAll('#platformFilterTabs button');
  if(!list||!form)return;

  host.querySelectorAll('[data-platform-nav]').forEach(btn=>{
    btn.onclick=()=>{
      const target=btn.dataset.platformNav;
      if(target&&typeof switchView==='function'){
        switchView(target);
      }
    };
  });

  const sourceMeta={
    builtin:{label:'内置',className:'source-builtin',icon:'<svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/></svg>'},
    granted:{label:'已授权',className:'source-granted',icon:'<svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2"><path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>'}
  };

  let allRows=[];
  let activeFilter='all';
  let searchQuery='';

  const updateMetrics=rows=>{
    const listArr=rows||[];
    const total=listArr.length;
    const locked=listArr.filter(r=>!r.revocable).length;
    const granted=listArr.filter(r=>r.revocable).length;
    const elTotal=host.querySelector('#platformMetricTotal');
    const elLocked=host.querySelector('#platformMetricLocked');
    const elGranted=host.querySelector('#platformMetricGranted');
    const elBadge=host.querySelector('#platformAdminCountBadge');
    if(elTotal)elTotal.textContent=total;
    if(elLocked)elLocked.textContent=locked;
    if(elGranted)elGranted.textContent=granted;
    if(elBadge)elBadge.textContent=`共 ${total} 位管理员`;
    setPlatformTabCount(host,'admins',total);
  };

  const render=()=>{
    let filtered=allRows||[];
    if(activeFilter==='builtin'){
      filtered=filtered.filter(r=>!r.revocable||r.source==='builtin'||r.source==='env');
    }else if(activeFilter==='granted'){
      filtered=filtered.filter(r=>r.revocable||r.source==='granted');
    }
    if(searchQuery){
      const q=searchQuery.toLowerCase();
      const knownChineseNames = {};
      filtered=filtered.filter(r=>
        (r.chineseName&&String(r.chineseName).toLowerCase().includes(q))||
        (r.loginName&&String(r.loginName).toLowerCase().includes(q))||
        (knownChineseNames[String(r.loginName).toLowerCase()]&&knownChineseNames[String(r.loginName).toLowerCase()].toLowerCase().includes(q))
      );
    }

    if(!filtered.length){
      list.innerHTML=`
        <div class="platform-empty-state">
          <div class="platform-empty-icon">🔍</div>
          <b>未找到符合条件的管理员</b>
          <p>${(allRows&&allRows.length)?'请尝试更换搜索关键字或重置筛选条件。':'还没有配置平台管理员。'}</p>
        </div>
      `;
      return;
    }

    list.innerHTML=filtered.map(row=>{
      const isBuiltin=!row.revocable||row.source==='builtin'||row.source==='env';
      const meta=isBuiltin?sourceMeta.builtin:sourceMeta.granted;
      const userObj=(state.data?.users||[]).find(u=>u.loginName===row.loginName||(row.userId&&Number(u.id)===Number(row.userId)));
      const knownChineseNames = {};
      const lName=row.loginName||'';
      let cName=row.chineseName||userObj?.chineseName||'';
      if(!cName||cName.toLowerCase()===lName.toLowerCase()){
        if(knownChineseNames[lName.toLowerCase()]){
          cName=knownChineseNames[lName.toLowerCase()];
        }
      }
      const displayName=(cName&&cName.toLowerCase()!==lName.toLowerCase())
        ? `${lName}(${cName})`
        : lName;
      return `
        <div class="platform-admin-row ${row.revocable?'is-granted':'is-locked'}">
          <div class="platform-admin-user">
            <div class="platform-admin-avatar">
              ${userAvatarHTML(row.loginName, 100)}
            </div>
            <div class="platform-admin-info">
              <div class="platform-admin-name-row">
                <span class="platform-admin-name">${escapeHTML(displayName)}</span>
              </div>
              <div class="platform-admin-meta">
                <span class="platform-source-badge ${meta.className}">
                  ${meta.icon}<span>${escapeHTML(meta.label)}</span>
                </span>
              </div>
            </div>
          </div>
          <div class="platform-admin-actions">
            ${row.revocable?`
              <button type="button" class="button ghost small platform-revoke-btn" data-revoke-admin="${escapeHTML(row.loginName)}" title="撤销该账号的平台管理员权限">
                <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
                <span>撤销</span>
              </button>
            `:`
              <span class="platform-locked-tag" title="系统内置名单无法通过界面撤销，保障平台管理连续性">
                <svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="11" width="18" height="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>
                <span>不可撤销</span>
              </span>
            `}
          </div>
        </div>
      `;
    }).join('');

    list.querySelectorAll('[data-revoke-admin]').forEach(btn=>btn.onclick=async()=>{
      const target=btn.dataset.revokeAdmin;
      if(!await afConfirm({
        title:'撤销平台管理员',
        message:`确定撤销 ${target} 的平台管理员权限？撤销后该账号将不再具备通用模板与角色编辑能力。`,
        danger:true,
        confirmText:'撤销权限'
      }))return;
      try{
        const rows=await api('/api/platform/admins',{method:'PUT',body:JSON.stringify({loginName:target,role:''})});
        allRows=rows||[];
        updateMetrics(allRows);
        render();
        toast('已撤销管理员权限','ok');
      }catch(e){toast(e.message,'err')}
    });
  };

  if(searchInput){
    searchInput.oninput=()=>{
      searchQuery=searchInput.value.trim();
      render();
    };
  }

  filterTabs.forEach(btn=>{
    btn.onclick=()=>{
      filterTabs.forEach(b=>b.classList.remove('active'));
      btn.classList.add('active');
      activeFilter=btn.dataset.sourceFilter||'all';
      render();
    };
  });

  try{
    allRows=await api('/api/platform/admins')||[];
    updateMetrics(allRows);
    render();
  }catch(e){
    list.innerHTML=`<p class="prop-hint">${escapeHTML(e.message||'加载管理员列表失败')}</p>`;
  }

  form.onsubmit=async e=>{
    e.preventDefault();
    const login=form.elements.loginName.value.trim();
    if(!login)return toast('请填写用户名');
    const submitBtn=form.querySelector('button[type="submit"]');
    if(submitBtn)submitBtn.disabled=true;
    try{
      const rows=await api('/api/platform/admins',{method:'PUT',body:JSON.stringify({loginName:login,role:'admin'})});
      form.reset();
      allRows=rows||[];
      updateMetrics(allRows);
      render();
      toast('已成功添加平台管理员','ok');
    }catch(err){
      toast(err.message,'err');
    }finally{
      if(submitBtn)submitBtn.disabled=false;
    }
  };
}


function renderUserProfilePopover() {
  const popover = $('#userProfilePopover');
  if (!popover) return;
  const user = state.currentUser;
  const isGuest = !user || user.isGuest;
  const displayName = user?.chineseName || user?.loginName || '访客';
  const initial = (displayName[0] || '访').toUpperCase();
  let authErr = null;
  try { authErr = sessionStorage.getItem('agentflow_auth_err'); } catch (_) {}

  if (isGuest) {
    popover.innerHTML = `
      <div class="upp-header">
        <div class="upp-avatar-lg is-guest">${escapeHTML(initial)}</div>
        <div class="upp-user-meta">
          <div class="upp-name-row">
            <span class="upp-name">访客身份</span>
            <span class="upp-badge-tag is-guest">未认证</span>
          </div>
          <div class="upp-login-sub">Guest Session</div>
        </div>
      </div>
      <div class="upp-divider"></div>
      <div class="upp-guest-body">
        ${authErr ? `
          <div class="upp-alert-warn">
            <b>⚠️ 认证换票未通过</b><br>此前统一认证票据已失效，请点击下方按钮重新登录。
          </div>
        ` : `
          <p class="upp-guest-desc">当前以访客身份访问平台。登录后可在左侧「个人配置」配置 GitHub Git Token 和 CodeBuddy API Key。</p>
        `}
      </div>
      <div class="upp-divider"></div>
      ${themeSegmentHTML()}
      <div class="upp-footer">
        <button id="uppLoginBtn" class="button small primary upp-full-btn" type="button">通过账号登录</button>
      </div>
    `;
    const loginBtn = popover.querySelector('#uppLoginBtn');
    if (loginBtn) {
      loginBtn.onclick = () => {
        try { sessionStorage.removeItem('agentflow_auth_err'); } catch (_) {}
        const target = state.loginUrl || '/login.html';
        window.location.href = target;
      };
    }
  } else {
    const storedUser=(state.data?.users||[]).find(u=>Number(u.id)===Number(user.id))||{};
    popover.innerHTML = `
      <div class="upp-header">
        <div class="upp-avatar-lg">${userAvatarHTML(user, 150)}</div>
        <div class="upp-user-meta">
          <div class="upp-name-row">
            <span class="upp-name">${escapeHTML(displayName)}</span>
            <span class="upp-badge-tag">已登录</span>
          </div>
          <div class="upp-login-sub">${escapeHTML(user.loginName || '')}</div>
        </div>
      </div>
      <div class="upp-divider"></div>
      <div class="upp-info-list">
        <div class="upp-info-item">
          <span class="upp-label">显示名称</span>
          <span class="upp-value">${escapeHTML(user.chineseName || displayName)}</span>
        </div>
        <div class="upp-info-item">
          <span class="upp-label">登录账号</span>
          <span class="upp-value">${escapeHTML(user.loginName || '-')}</span>
        </div>
        <div class="upp-info-item">
          <span class="upp-label">用户 ID</span>
          <span class="upp-value">${escapeHTML(user.id ? String(user.id) : '-')}</span>
        </div>
        <div class="upp-info-item">
          <span class="upp-label">所属部门</span>
          <span class="upp-value" title="${escapeHTML(user.deptName || '-')}">${escapeHTML(user.deptName || '-')}</span>
        </div>
        <div class="upp-info-item">
          <span class="upp-label">认证方式</span>
          <span class="upp-value">LoopForge 本地账号</span>
        </div>
      </div>
      <div class="upp-divider"></div>
      ${themeSegmentHTML()}
      <div class="upp-footer">
        <button id="uppLogoutBtn" class="button small ghost upp-btn-danger upp-full-btn" type="button" title="退出当前登录">退出登录</button>
      </div>
    `;
    const logoutBtn = popover.querySelector('#uppLogoutBtn');
    if (logoutBtn) {
      logoutBtn.onclick = async () => {
        try {
          await api('/api/auth/logout', {method: 'POST'});
          location.replace('/login.html');
        } catch (err) {
          toast('退出登录失败: ' + err.message, 'err');
        }
      };
    }
  }
  bindThemeSegment(popover);
}

const THEME_SEGMENT_OPTIONS = [['light', 'sun', '浅色'], ['dark', 'moon', '深色'], ['system', 'monitor', '跟随系统']];
function themeSegmentHTML() {
  return `
      <div class="upp-theme">
        <span class="upp-label" id="uppThemeLabel">外观</span>
        <div class="upp-theme-seg" role="radiogroup" aria-labelledby="uppThemeLabel">
          ${THEME_SEGMENT_OPTIONS.map(([value, icon, label]) => `<button type="button" role="radio" data-theme-pref="${value}" aria-checked="false" tabindex="-1"><svg class="agent-icon harness-icon" data-icon="${icon}" width="14" height="14" aria-hidden="true" focusable="false"></svg>${label}</button>`).join('')}
        </div>
      </div>`;
}
function bindThemeSegment(popover) {
  window.AgentFlowHarnessIcons?.hydrate(popover);
  const buttons = [...popover.querySelectorAll('[data-theme-pref]')];
  buttons.forEach((button, index) => {
    button.onclick = () => { if (typeof setThemePreference === 'function') setThemePreference(button.dataset.themePref); };
    button.onkeydown = event => {
      const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[event.key];
      if (!step) return;
      event.preventDefault();
      const next = buttons[(index + step + buttons.length) % buttons.length];
      next.click();
      next.focus();
    };
  });
  if (typeof renderThemeSegment === 'function') renderThemeSegment();
}

if (typeof window !== 'undefined' && typeof window.addEventListener === 'function' && !window.__avatarErrBound) {
  window.__avatarErrBound = true;
  window.addEventListener('error', e => {
    if (e.target && e.target.tagName === 'IMG' && e.target.classList?.contains('user-avatar-img')) {
      e.target.remove();
    }
  }, true);
}

function userAvatarHTML(userOrId, spec=100, extraClass=''){
  let user = null;
  if(userOrId && typeof userOrId === 'object'){
    user = userOrId;
  } else if(userOrId != null && userOrId !== '') {
    if(typeof userById === 'function'){
      user = userById(userOrId);
    }
    if(!user){
      user = {loginName: String(userOrId)};
    }
  }
  const isGuest = !user || user.isGuest || user.loginName === 'guest' || !user.loginName;
  const initial = (user?.chineseName || user?.loginName || '用')[0]?.toUpperCase() || '访';
  if(isGuest){
    return `<span class="user-avatar-text">${escapeHTML(initial)}</span>`;
  }
  const specNum = Number(spec) || 100;
  const photoUrl = `/api/auth/photo/${specNum}/${encodeURIComponent(user.loginName)}.png`;
  return `<img class="user-avatar-img ${extraClass}" src="${photoUrl}" alt="" loading="lazy"><span class="user-avatar-text">${escapeHTML(initial)}</span>`;
}

function renderUserBadge(){
  window.AgentFlowPageFeedback?.sync();
  const badge=$('#userBadge'), nameEl=$('#userName'), avatarEl=$('#userAvatarText');
  if(!badge) return;
  const user=state.currentUser;
  const isGuest = !user || user.isGuest;
  const displayName=user?.chineseName||user?.loginName||'访客';
  if(nameEl) nameEl.textContent=displayName;
  if(avatarEl) avatarEl.innerHTML=userAvatarHTML(user, 100);
  let authErr = null;
  try { authErr = sessionStorage.getItem('agentflow_auth_err'); } catch (_) {}
  if(isGuest){
    badge.classList.add('is-guest');
  }else{
    badge.classList.remove('is-guest');
  }
  const tipText = isGuest
    ? (authErr ? '认证换票未通过 · 点击查看个人信息' : '当前为访客身份 · 点击查看个人信息')
    : `${displayName} (${user.loginName||user.staffId||''}) · 点击查看个人信息`;
  badge.title = tipText;
  if(typeof badge.setAttribute === 'function'){
    badge.setAttribute('data-tooltip', tipText);
    badge.setAttribute('aria-label', tipText);
  }
  renderUserProfilePopover();
}
function renderDashboard(){
  const issues=state.data.issues||[];
  const projectIssues=issues.filter(i=>!state.projectFilter||i.projectId===state.projectFilter);
  const attention=projectIssues.filter(i=>issueRunHealth(i)==='attention');
  const waiting=projectIssues.filter(i=>issueCurrentStage(i).key==='waiting');
  const confirm=projectIssues.filter(i=>issueCurrentStage(i).key==='confirm');
  const active=projectIssues.filter(i=>issueRunHealth(i)==='active'&&issueCurrentStage(i).key!=='waiting');
  const closed=projectIssues.filter(i=>issueRunHealth(i)==='closed');
  updateApprovalBadge();
  const needMe=[...waiting,...confirm,...attention.filter(i=>!waiting.includes(i)&&!confirm.includes(i))];
  const statCards=[
    {label:'需要介入',value:attention.length,foot:'待确认结论、失败或已中断',color:'#c63c4b',filter:'attention'},
    {label:'待我确认',value:approvalItems().length,foot:'批准后才会继续跑',color:'#fbbf24',to:'approvals'},
    {label:'正在推进',value:active.length,foot:'Agent 正在执行',color:'#5b8cff',filter:'active'},
    {label:'已经完成',value:closed.length,foot:'本项目已闭环',color:'#34d399',filter:'closed'},
  ];
  const statsHost=$('#dashStats');
  statsHost.innerHTML=statCards.map(c=>`<div class="stat-card" ${c.filter!=null?`data-issue-filter="${c.filter}"`:`data-jump="${c.to}"`} style="--accent:${c.color}"><div class="label">${c.label}</div><div class="value">${c.value}</div><div class="foot">${c.foot}</div></div>`).join('');
  jumpBind(statsHost);jumpBind($('#dashboardView'));
  const row=(i)=>{
    const p=(state.data.projects||[]).find(x=>x.id===i.projectId),out=issueOutcome(i),stage=issueCurrentStage(i);
    return `<div class="row-item ${out.key}" data-issue="${escapeHTML(i.id)}"><span class="dot ${out.key}"></span><div style="flex:1;min-width:0"><div class="strong ellipsis">${escapeHTML(i.title)}</div><div class="muted">${escapeHTML(issueKeyLabel(i))} · ${escapeHTML(stage.label)}</div></div><span class="status ${out.key}">${escapeHTML(out.short)}</span></div>`;
  };
  const bindIssueRows=host=>{host.querySelectorAll('[data-issue]').forEach(el=>el.onclick=()=>openIssueDetail(el.dataset.issue))};
  $('#dashAttention').innerHTML=needMe.length?needMe.slice(0,8).map(row).join(''):'<div class="empty-box">没有需要你处理的需求</div>';
  $('#dashActive').innerHTML=active.length?active.slice(0,8).map(row).join(''):'<div class="empty-box">当前没有进行中的需求</div>';
  const results=closed.filter(i=>!issueManuallyClosed(i)&&!issueTrackerClosed(i)).sort((a,b)=>new Date(b.updatedAt||0)-new Date(a.updatedAt||0)).slice(0,8);
  $('#dashResults').innerHTML=results.length?results.map(row).join(''):'<div class="empty-box">还没有跑完的需求</div>';
  bindIssueRows($('#dashAttention'));bindIssueRows($('#dashActive'));bindIssueRows($('#dashResults'));
  const cfg=state.data.syncConfig||{},logs=state.data.syncLogs||[];
  const synced=knownTime(cfg.lastSyncAt),syncResult=logs[0]?.status==='success'?'成功':logs[0]?.status==='failed'?'失败':'';
  $('#dashFeedHint').textContent=synced?`最近同步 ${synced.toLocaleString()}${syncResult?` · ${syncResult}`:''}`:'从未同步';
  const feed=logs.slice(0,8).map(l=>`<div class="tl-item"><div><b>${l.direction==='pull'?'拉取':'推送'} · ${l.source==='github'?'GitHub':l.source==='gongfeng'?'工蜂':l.source==='tapd'?'TAPD':escapeHTML(l.source||'')}</b> <span class="muted">${escapeHTML(l.message||l.action)}</span></div><div class="muted" style="font-size:10.5px;margin-top:2px">${new Date(l.startedAt).toLocaleString()} · ${l.status==='success'?'成功':`失败 ${escapeHTML(l.message||'')}`}</div></div>`).join('');
  $('#dashFeed').innerHTML=feed||'<div class="tl-item"><div class="muted">尚未同步。绑定 GitHub 仓库后，可在「需求列表」里点击「同步 GitHub」。</div></div>';
}
