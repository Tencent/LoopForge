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
  {id:'platform',name:'Tích hợp nền tảng',sub:'Repository code và đăng nhập thống nhất',icon:'key'},
  {id:'model',name:'Model và thực thi',sub:'AI trò chuyện và coding CLI',icon:'sparkles'},
];
// A CodeBuddy key only works on the edition that issued it; the CLI treats unknown values as Trang quốc tế.
// Keep in sync with chatbot/src/credentials.js and domain.NormalizeCodeBuddyInternetEnvironment.
const CODEBUDDY_ENVIRONMENTS=[
  {value:'',label:'Trang quốc tế',site:'codebuddy.ai',keyUrl:'https://www.codebuddy.ai/profile/keys'},
  {value:'internal',label:'Trang nội địa',site:'copilot.tencent.com',keyUrl:'https://copilot.tencent.com/profile/'},
  {value:'ioa',label:'iOA',site:'tencent.sso.copilot.tencent.com',keyUrl:'https://tencent.sso.copilot.tencent.com/profile/keys'},
];
const CODEBUDDY_ENVIRONMENT_ALIASES={public:'',intl:'',international:'','国际站':'',china:'internal',cn:'internal','国内站':'internal'};
// The CLI receives the stored text as-is, so only the exact values count; returns null for anything else.
function codeBuddyEnvironment(value){
  const key=String(value||'').trim();
  return CODEBUDDY_ENVIRONMENTS.find(env=>env.value===key)||null;
}
// What an old free-text value most likely meant (china → Trang nội địa), used to preselect the fix.
function codeBuddyEnvironmentIntent(value){
  const key=String(value||'').trim().toLowerCase();
  const normalized=Object.prototype.hasOwnProperty.call(CODEBUDDY_ENVIRONMENT_ALIASES,key)?CODEBUDDY_ENVIRONMENT_ALIASES[key]:key;
  return CODEBUDDY_ENVIRONMENTS.find(env=>env.value===normalized)||null;
}
// A stored value the CLI does not take as written; it connects to Trang quốc tế until it is saved again.
function codeBuddyEnvironmentLegacy(value){const raw=String(value||'').trim();return !!raw&&!codeBuddyEnvironment(raw)}
function codeBuddyKeyURL(value){return (codeBuddyEnvironmentIntent(value)||CODEBUDDY_ENVIRONMENTS[0]).keyUrl}
function codeBuddyKeyLinkLabel(value){return `Mở ${(codeBuddyEnvironmentIntent(value)||CODEBUDDY_ENVIRONMENTS[0]).label} để lấy API Key`}
const CREDENTIAL_KINDS=[
  {key:'gitToken',group:'platform',name:'GitHub',short:'Ủy quyền repository',logo:'github',monochrome:true,
    use:'Clone repository riêng, tạo nhánh tác vụ và đẩy sản phẩm bàn giao',
    description:'Dùng để clone repository riêng, tạo nhánh tác vụ và đẩy sản phẩm bàn giao.',
    guide:[['Mở GitHub「Settings → Developer settings → Personal access tokens」','https://github.com/settings/tokens','Mở GitHub để tạo personal access token'],['Tạo token và cấp quyền đọc/ghi <code>Contents</code> và <code>Pull requests</code> cho repository đích'],['Sao chép token và dán vào bên dưới; hệ thống sẽ kiểm tra quyền trước khi lưu']],
    placeholder:'Nhập personal access token (PAT)',hint:'Trước khi lưu, hệ thống sẽ kiểm tra token còn hiệu lực và đủ quyền',
    impact:'Các SOP phụ thuộc repository riêng sẽ không clone hoặc đẩy được mã.'},
  {key:'gitSshKey',group:'platform',name:'Git SSH',short:'Khóa Git SSH',icon:'key',
    use:'Khóa SSH dùng để clone và push; tạo PR/MR cần access token',dialog:()=>openGitSSHKeys(),action:'Quản lý khóa'},
  {key:'gitHttpCredentials',group:'platform',name:'Git HTTPS',short:'Token truy cập Git',icon:'lock',
    use:'Quản lý token truy cập HTTPS theo host; git, gh và glab trong workspace tự xác thực',dialog:()=>openGitHTTPCredentials(),action:'Quản lý token'},
  {key:'codeBuddyApiKey',group:'model',name:'CodeBuddy',short:'CodeBuddy API Key',logo:'codebuddy',
    use:'Hội thoại AI và trình thực thi CodeBuddy của SOP',
    description:'Hội thoại AI (gồm các model như Claude, GPT) và trình thực thi CodeBuddy của SOP đều dùng credential này; môi trường mạng cần khớp với tài khoản.',
    guide:[['Trước hết hãy chọn môi trường mạng của tài khoản CodeBuddy: Trang quốc tế, Trang nội địa hoặc iOA'],['Đăng nhập trang API Key của site đó và tạo API Key mới',codeBuddyKeyURL,codeBuddyKeyLinkLabel],['Sao chép key bắt đầu bằng <code>ck_</code> và dán vào bên dưới; không điền Key model bên thứ ba như DeepSeek, OpenAI vào đây']],
    placeholder:'Nhập ck_ key cá nhân',
    extra:{name:'codeBuddyInternetEnvironment',label:'Môi trường mạng',options:CODEBUDDY_ENVIRONMENTS},
    models:{name:'codeBuddyModels',engine:'codebuddy'},
    impact:'Hội thoại AI sẽ chỉ dùng được model mặc định, các bước trình thực thi CodeBuddy sẽ thất bại.'},
  {key:'codexApiKey',group:'model',name:'Codex',short:'Codex API Key',logo:'codex',monochrome:true,
    use:'Chọn trình thực thi Codex trong hội thoại AI, và các bước SOP chạy bằng Codex',
    description:'Sau khi chuyển hội thoại AI sang Codex, bạn có thể chọn model trong danh mục; các bước SOP chạy Codex trên máy thực thi cũng dùng credential này. Khi danh mục trống, nếu chưa điền Base URL sẽ dùng model có sẵn của Codex, đã điền thì theo model do dịch vụ công bố; bạn cũng có thể tự chọn, sắp xếp và đặt tên.',
    guide:[['Tạo API Key trong bảng điều khiển của nhà cung cấp model'],['Nếu đi qua proxy hoặc cổng riêng, hãy ghi lại địa chỉ dịch vụ'],['Dán Key, điền Base URL nếu cần; sau khi lưu có thể chọn model hội thoại AI trong danh mục']],
    placeholder:'Nhập Codex API Key',
    extra:{name:'codexBaseUrl',label:'Base URL',placeholder:'https://api.openai.com/v1',url:true,clearWithKey:true},
    models:{name:'codexModels',engine:'codex',endpoint:'/models'},
    impact:'Các bước SOP chạy bằng Codex sẽ không thực thi được; Base URL và danh mục model cũng sẽ bị xóa.'},
  {key:'claudeCodeApiKey',group:'model',name:'Claude Code',short:'Claude Code API Key',logo:'claude',monochrome:true,
    use:'Chọn trình thực thi Claude trong hội thoại AI, và các bước SOP chạy bằng Claude Code',
    description:'Sau khi chuyển hội thoại AI sang Claude Code, bạn có thể chọn model trong danh mục; các bước SOP chạy Claude Code trên máy thực thi cũng dùng credential này. Khi danh mục trống, nếu chưa điền Base URL sẽ dùng model có sẵn của Claude Code, đã điền thì theo model do dịch vụ công bố; bạn cũng có thể tự chọn, sắp xếp và đặt tên.',
    guide:[['Tạo API Key trong bảng điều khiển của nhà cung cấp model'],['Nếu đi qua proxy hoặc cổng riêng, hãy ghi lại địa chỉ dịch vụ'],['Dán Key, điền Base URL nếu cần; sau khi lưu có thể chọn model hội thoại AI trong danh mục']],
    placeholder:'Nhập Claude Code API Key',
    extra:{name:'claudeCodeBaseUrl',label:'Base URL',placeholder:'https://api.anthropic.com',url:true,clearWithKey:true},
    models:{name:'claudeCodeModels',engine:'claude-code',endpoint:'/v1/models'},
    impact:'Các bước SOP chạy bằng Claude Code sẽ không thực thi được; Base URL và danh mục model cũng sẽ bị xóa.'},
  agentCliCredentialKind('opencode',{name:'OpenCode',logo:'opencode',baseUrl:'https://api.example.com/v1',required:()=>true,
    note:'OpenCode không có dịch vụ model riêng, bạn cần điền Base URL.',protocols:[['chat','Chat Completions'],['responses','Responses'],['messages','Messages（Anthropic）']]}),
  agentCliCredentialKind('cursor',{name:'Cursor CLI',logo:'cursor',monochrome:true,
    note:'Cursor CLI kết nối dịch vụ chính thức của Cursor và có thể lấy model theo tài khoản.',
    guide:[['Đăng nhập bảng điều khiển Cursor, tạo Key trong「Integrations → User API Keys」','https://cursor.com/dashboard?tab=integrations','Mở Cursor để tạo API Key'],['Sao chép Key bắt đầu bằng <code>crsr_</code>'],['Dán vào bên dưới rồi lưu']]}),
  agentCliCredentialKind('agy',{name:'Antigravity',logo:'antigravity',baseUrl:'https://gateway.example.com/v1',required:protocol=>protocol==='chat',
    note:'Mặc định kết nối dịch vụ chính thức của Gemini; khi chọn cổng tương thích OpenAI cần điền Base URL.',extraNote:'Bắt buộc với cổng tương thích OpenAI',
    guide:[['Tạo Gemini API Key trên Google AI Studio, hoặc chuẩn bị Key của cổng tương thích OpenAI','https://aistudio.google.com/apikey','Mở Google AI Studio'],['Nếu đi qua cổng, hãy ghi lại địa chỉ cổng'],['Dán Key và chọn giao thức; sau khi lưu có thể chọn model hội thoại AI trong danh mục']],
    protocols:[['gemini','Gemini official API'],['chat','Cổng tương thích OpenAI (Chat Completions)']]}),
  agentCliCredentialKind('kimi',{name:'Kimi Code',logo:'kimi-code.png',baseUrl:'https://api.kimi.com/coding/v1',
    note:'Nếu chưa điền Base URL sẽ kết nối dịch vụ chính thức của Kimi Code.',protocols:[['chat','Chat Completions'],['responses','Responses'],['messages','Messages（Anthropic）']]}),
  agentCliCredentialKind('pi',{name:'Pi',logo:'pi',baseUrl:'https://api.example.com/v1',required:()=>true,
    note:'Pi không có dịch vụ model riêng, bạn cần điền Base URL.',protocols:[['chat','Chat Completions'],['responses','Responses'],['messages','Messages（Anthropic）']]}),
  agentCliCredentialKind('dsh',{name:'DeepSeek Harness',logo:'deepseek',baseUrl:'https://api.deepseek.com',
    note:'Nếu chưa điền Base URL sẽ kết nối dịch vụ chính thức của DeepSeek.',protocols:[['chat','Chat Completions'],['responses','Responses'],['messages','Messages（Anthropic）']]}),
  agentCliCredentialKind('zcode',{name:'ZCode',logo:'glm',baseUrl:'https://open.bigmodel.cn/api/anthropic',
    note:'Nếu chưa điền Base URL sẽ kết nối dịch vụ tương thích Anthropic (Messages) của Zhipu BigModel.',protocols:[['messages','Messages（Anthropic）'],['chat','Chat Completions'],['responses','Responses']]}),
];
function agentCliCredentialKind(cli,o){
  const field=`agentCli.${cli}.`,configurable=!!o.protocols;
  return {key:`agentCli.${cli}`,agentCli:cli,group:'model',name:o.name,short:`${o.name} API Key`,logo:o.logo,monochrome:!!o.monochrome,
    use:`Model ${o.name} cho hội thoại AI và các bước SOP chạy bằng ${o.name}`,
    description:`Sau khi chuyển hội thoại AI sang ${o.name}, bạn có thể chọn model của nó; các bước SOP chạy ${o.name} trên máy thực thi cũng dùng credential này.${o.note||''}`,
    guide:o.guide||[['Tạo API Key trong bảng điều khiển của nhà cung cấp model'],['Ghi lại địa chỉ dịch vụ và giao thức API mà dịch vụ cung cấp'],['Dán Key, điền Base URL nếu cần và chọn giao thức; sau khi lưu có thể chọn model hội thoại AI trong danh mục']],
    placeholder:`Nhập API Key ${o.name}`,
    ...(configurable?{
      extra:{name:field+'baseUrl',label:'Base URL',placeholder:o.baseUrl,url:true,clearWithKey:true,required:o.required,note:o.extraNote||(o.required?'Bắt buộc':'')},
      protocol:{name:field+'protocol',options:o.protocols},
    }:{}),
    models:{name:field+'models',engine:cli},
    impact:`Hội thoại AI sẽ không thể chuyển sang ${o.name}, các bước SOP chạy bằng ${o.name} sẽ không thực thi được${configurable?'; Base URL, giao thức và danh mục model cũng sẽ bị xóa':''}。`};
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
  return `<label class="credential-field"><span>Giao thức API<em>API mà Base URL cung cấp</em></span><div class="credential-input is-plain is-select"><select name="protocol" aria-label="Giao thức API">${kind.protocol.options.map(([id,label])=>`<option value="${id}"${id===value?' selected':''}>${escapeHTML(label)}</option>`).join('')}</select></div></label>`;
}
// The model list behind an Anthropic-style Base URL sits under /v1.
function credentialModelsEndpoint(kind,protocol){
  return kind.models.endpoint||({messages:'/v1/models',gemini:'/v1beta/models'}[protocol]||'/models');
}
// ---- Danh mục model（Codex / Claude Code / 可配置 Agent CLI） ----
// A saved catalog is the chat list on the official endpoint and behind a Base URL.
// An empty catalog follows the CLI's built-in models when there is no Base URL
// and the Base URL's published models otherwise.
// Edits stay in the DOM until 保存.
const CREDENTIAL_CATALOG_MAX=100;
let credentialEffortCatalogs,credentialEffortCatalogRequest;
async function loadCredentialEffortCatalogs(){
  if(credentialEffortCatalogs)return;
  if(!credentialEffortCatalogRequest)credentialEffortCatalogRequest=api('/api/executor-models?builtin=1',{silent:true}).then(data=>{credentialEffortCatalogs=data}).catch(()=>{}).finally(()=>{credentialEffortCatalogRequest=null});
  await credentialEffortCatalogRequest;
}
function credentialCatalogModels(value){
  return (Array.isArray(value)?value:[]).map(model=>{
    const id=String(model?.id||'').trim(),name=String(model?.name||'').trim();
    const metadata={
      ...(model?.efforts?{efforts:model.efforts,defaultEffort:model.defaultEffort}:{}),
      ...(Array.isArray(model?.reasoningEfforts)?{reasoningEfforts:model.reasoningEfforts}:{}),
      ...(Array.isArray(model?.discoveredReasoningEfforts)?{discoveredReasoningEfforts:model.discoveredReasoningEfforts}:{}),
    };
    return {id,name:name===id?'':name,...metadata};
  }).filter(model=>model.id||model.name);
}
function credentialCatalogStored(models){return credentialCatalogModels(models).map(({name,...model})=>name?{...model,name}:model)}
function credentialCatalogKey(models){
  return JSON.stringify(credentialCatalogModels(models).map(model=>[
    model.id,model.name,
    model.reasoningEfforts?.slice().sort()??null,
    model.discoveredReasoningEfforts?.slice().sort()??null,
    Object.entries(model.efforts||{}).sort(([a],[b])=>a.localeCompare(b)),model.defaultEffort||'',
  ]));
}
function credentialCatalogError(models){
  const seen=new Set();
  if(models.length>CREDENTIAL_CATALOG_MAX)return {index:CREDENTIAL_CATALOG_MAX,error:`Danh mục model tối đa ${CREDENTIAL_CATALOG_MAX} mục`};
  for(const [index,{id='',name=''}] of models.entries()){
    const model={id,name};
    if(!model.id)return {index,field:'id',error:'Model ID không được để trống'};
    if(model.id.length>128)return {index,field:'id',error:'Model ID không được dài quá 128 ký tự'};
    if(/[\s"'`\\\u0000-\u001f\u007f]/.test(model.id))return {index,field:'id',error:`Model ID「${model.id}」không được chứa khoảng trắng, dấu ngoặc kép hoặc dấu gạch chéo ngược`};
    if(seen.has(model.id))return {index,field:'id',error:`Model ID「${model.id}」bị trùng`};
    if([...model.name].length>64)return {index,field:'name',error:'Tên hiển thị không được dài quá 64 ký tự'};
    seen.add(model.id);
  }
  return null;
}
function credentialCatalogEffortSummary(model){
  if(model.reasoningEfforts)return model.reasoningEfforts.join(' / ')||'Chỉ dùng mặc định';
  if(!model.discoveredReasoningEfforts)return 'Tự động';
  return 'Tự động · '+(model.discoveredReasoningEfforts.join(' / ')||'Chỉ dùng mặc định');
}
function credentialCatalogEffortHTML(model,engine){
  const available=credentialEffortCatalogs?.[engine==='claude-code'?'claude':engine]?.efforts||[];
  const knownLevels=new Set([...(model.reasoningEfforts||[]),...(model.discoveredReasoningEfforts||[])]);
  const additional=[...knownLevels].filter(value=>!available.some(option=>option.value===value));
  const options=available.concat(additional.map(value=>({value,label:value})));
  if(!options.length&&!model.discoveredReasoningEfforts)return '';
  const custom=Array.isArray(model.reasoningEfforts),selected=model.reasoningEfforts??model.discoveredReasoningEfforts??[];
  return `<details class="credential-catalog-effort"><summary>Mức suy luận<span data-catalog-effort-summary>${escapeHTML(credentialCatalogEffortSummary(model))}</span></summary><label class="credential-catalog-effort-mode">Mức tùy chọn<select data-catalog-effort-mode aria-label="Cấu hình mức suy luận"><option value="auto"${custom?'':' selected'}>Tự động</option><option value="custom"${custom?' selected':''}>Tùy chỉnh</option></select></label><div class="credential-catalog-effort-levels" data-catalog-effort-levels${custom?'':' hidden'}>${options.map(({value,label})=>`<label><input type="checkbox" data-catalog-effort="${escapeHTML(value)}"${selected.includes(value)?' checked':''}>${escapeHTML(label)}</label>`).join('')}<small>Chọn các mức mà model này hỗ trợ; nếu bỏ hết sẽ chỉ dùng mức mặc định của model.</small></div></details>`;
}
function syncCredentialCatalogEfforts(row){
  if(!row)return;
  const mode=row.querySelector('[data-catalog-effort-mode]');if(!mode)return;
  row.querySelector('[data-catalog-effort-levels]').hidden=mode.value!=='custom';
  row.querySelector('[data-catalog-effort-summary]').textContent=credentialCatalogEffortSummary(credentialCatalogRowModel(row));
}
function refreshCredentialCatalogEfforts(catalog){
  if(!catalog?.isConnected)return;
  credentialCatalogRows(catalog).forEach(row=>{
    const html=credentialCatalogEffortHTML(credentialCatalogRowModel(row),catalog.dataset.catalogEngine),levels=row.querySelector('[data-catalog-effort-levels]');
    if(!levels){row.insertAdjacentHTML('beforeend',html);return}
    // Capabilities may arrive after a row was expanded. Add missing choices
    // without replacing focused controls or the user's unsaved selections.
    const holder=document.createElement('div');holder.innerHTML=html;
    const options=[...holder.querySelectorAll('[data-catalog-effort]')];
    const labels=new Map([...levels.querySelectorAll('[data-catalog-effort]')].map(input=>[input.dataset.catalogEffort,input.closest('label')]));
    options.forEach((input,index)=>{
      if(labels.has(input.dataset.catalogEffort))return;
      const next=options.slice(index+1).map(option=>labels.get(option.dataset.catalogEffort)).find(Boolean)||levels.querySelector('small');
      const label=input.closest('label');levels.insertBefore(label,next);labels.set(input.dataset.catalogEffort,label);
    });
  });
}
function credentialCatalogRowHTML(model={},engine=''){
  const metadata=` data-catalog-metadata="${escapeHTML(JSON.stringify(credentialCatalogModels([model])[0]||{}))}"`;
  return `<li class="credential-catalog-row" data-catalog-row${metadata}><button type="button" class="credential-catalog-grip" data-catalog-grip aria-label="Kéo để sắp xếp, hoặc dùng ↑ ↓">${credentialGlyph('grip',14)}</button><span class="credential-input is-plain"><input class="credential-mono" data-catalog-field="id" value="${escapeHTML(model.id||'')}" placeholder="Model ID, ví dụ gpt-5.5" aria-label="Model ID" autocomplete="off" spellcheck="false"></span><span class="credential-input is-plain"><input data-catalog-field="name" value="${escapeHTML(model.name||'')}" placeholder="Tên hiển thị (tùy chọn)" aria-label="Tên hiển thị" autocomplete="off"></span><button type="button" class="credential-btn ghost icon danger credential-ink" data-catalog-act="remove" aria-label="Gỡ model">${credentialGlyph('trash',14)}</button>${credentialCatalogEffortHTML(model,engine)}</li>`;
}
function credentialCatalogCopy(mode,kind){
  const name=kind?.name||'CLI này';
  if(mode==='runner')return {title:'Dùng model theo tài khoản trên máy thực thi',detail:'Lấy model theo tài khoản trên máy này, hoặc thêm thủ công.',reset:'Xóa danh sách tùy chỉnh',restored:'Đã xóa danh mục, có hiệu lực sau khi lưu'};
  return mode==='official'
    ?{title:`Dùng model có sẵn của ${name}`,detail:`Khi danh mục trống, hội thoại AI liệt kê các model mà ${name} hỗ trợ. Bạn có thể lấy danh sách model khả dụng để tự tạo danh mục, hoặc thêm thủ công.`,reset:'Xóa danh mục và quay lại dùng model có sẵn',restored:'Đã khôi phục mặc định; sau khi lưu sẽ dùng model có sẵn'}
    :{title:'Theo model do dịch vụ công bố',reset:'Xóa danh mục và quay lại theo model do dịch vụ công bố',restored:'Đã khôi phục mặc định; sau khi lưu sẽ theo model do dịch vụ công bố'};
}
function credentialCatalogDetailHTML(mode,kind,protocol){
  const copy=credentialCatalogCopy(mode,kind);
  return mode!=='remote'?escapeHTML(copy.detail):`Khi danh mục trống, hội thoại AI liệt kê các model do Base URL <code data-catalog-endpoint>${escapeHTML(credentialModelsEndpoint(kind,protocol))}</code> trả về.`;
}
function credentialCatalogHTML(kind,user,{runnerLogin=false,label='Danh mục model'}={}){
  const baseUrl=kind.extra?.url?credentialValue(user,kind.extra.name):'',protocol=credentialProtocol(kind,user);
  const cursorAutomatic=kind.agentCli==='cursor'&&!user?.agentCliCredentials?.cursor?.modelsCustomized;
  const models=credentialCatalogModels(cursorAutomatic?[]:credentialValue(user,kind.models.name)),empty=!models.length;
  const mode=runnerLogin?'runner':!String(baseUrl||'').trim()?'official':'remote';
  const copy=credentialCatalogCopy(mode,kind);
  return `<section class="credential-catalog" data-credential-catalog data-catalog-engine="${escapeHTML(kind.models.engine)}" data-catalog-mode="${mode}" data-catalog-protocol="${escapeHTML(protocol)}" data-original-key="${escapeHTML(credentialCatalogKey(models))}" data-catalog-source="${escapeHTML(credentialCatalogSource(baseUrl))}" aria-label="Danh mục model">
    <div class="credential-catalog-body" data-catalog-body>
    <div class="credential-catalog-head"><div>${label?`<b>${escapeHTML(label)}</b>`:''}<small data-catalog-count>${credentialCatalogCountText(models.length)}</small></div><div><button type="button" class="credential-btn secondary credential-ink" data-catalog-act="probe">${credentialGlyph('download',14)}<span>Lấy model khả dụng</span></button><button type="button" class="credential-btn secondary credential-ink" data-catalog-act="add"><span class="credential-turn">${credentialGlyph('plus',14)}</span><span>Thêm model</span></button></div></div>
    <div class="credential-catalog-probe" data-catalog-probe hidden></div>
    <ol class="credential-catalog-list" data-catalog-list${empty?' hidden':''}>${models.map(model=>credentialCatalogRowHTML(model,kind.models.engine)).join('')}</ol>
    <div class="credential-catalog-empty" data-catalog-empty${empty?'':' hidden'}>${credentialGlyph('layers',16)}<div><b data-catalog-empty-title>${escapeHTML(copy.title)}</b><small data-catalog-empty-detail>${credentialCatalogDetailHTML(mode,kind,protocol)}</small></div></div>
    <div class="credential-catalog-foot" data-catalog-foot${empty?' hidden':''}><button type="button" class="credential-btn link credential-ink" data-catalog-act="reset">${credentialGlyph('undo',13)}<span>Khôi phục mặc định</span></button><small data-catalog-reset-note>${escapeHTML(copy.reset)}</small></div>
    </div>
    <span class="credential-sr" data-catalog-live role="status" aria-live="polite"></span>
  </section>`;
}
function credentialCatalogSource(baseUrl){return String(baseUrl||'').trim().replace(/\/+$/,'')}
function credentialCatalogCountText(count){return count?`${count} model`:'Chưa tùy chỉnh'}
function credentialCatalogRows(catalog){return [...catalog.querySelectorAll('[data-catalog-row]')]}
function credentialCatalogRowModel(row){
  const id=row.querySelector('[data-catalog-field="id"]').value,name=row.querySelector('[data-catalog-field="name"]').value;
  const metadata=row.dataset.catalogMetadata?JSON.parse(row.dataset.catalogMetadata):null;
  const model=metadata?.id===id.trim()?{...metadata,id,name}:{id,name},mode=row.querySelector('[data-catalog-effort-mode]');
  if(mode){
    delete model.reasoningEfforts;
    if(mode.value==='custom')model.reasoningEfforts=[...row.querySelectorAll('[data-catalog-effort]:checked')].map(input=>input.dataset.catalogEffort);
  }
  return model;
}
function credentialCatalogFilledRows(catalog){return credentialCatalogRows(catalog).filter(row=>credentialCatalogModels([credentialCatalogRowModel(row)]).length)}
function readCredentialCatalog(catalog){return credentialCatalogModels(credentialCatalogRows(catalog).map(credentialCatalogRowModel))}
function credentialCatalogMode(catalog){
  const form=catalog?.closest('.credential-editor-form'),extra=form?.querySelector('input[name="extra"]');
  if(!extra||catalog.dataset.catalogMode==='runner')return catalog.dataset.catalogMode;
  return extra.value.trim()?'remote':'official';
}
function credentialCatalogDirty(catalog){return !!catalog&&credentialCatalogKey(readCredentialCatalog(catalog))!==catalog.dataset.originalKey}
function credentialCatalogPayload(catalog){
  if(!catalog)return undefined;
  return credentialCatalogDirty(catalog)?readCredentialCatalog(catalog):undefined;
}
function syncCredentialCatalog(catalog){
  const mode=credentialCatalogMode(catalog),kind=credentialKind(catalog.closest('[data-credential-row]')?.dataset.credentialRow),protocol=catalog.closest('.credential-editor-form')?.querySelector('select[name="protocol"]')?.value||catalog.dataset.catalogProtocol;
  const copy=credentialCatalogCopy(mode,kind);
  const title=catalog.querySelector('[data-catalog-empty-title]'),detail=catalog.querySelector('[data-catalog-empty-detail]'),note=catalog.querySelector('[data-catalog-reset-note]');
  if(title)title.textContent=copy.title;
  if(detail)detail.innerHTML=credentialCatalogDetailHTML(mode,kind,protocol);
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
  catalog.onCatalogChange?.();
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
  list.insertAdjacentHTML('beforeend',credentialCatalogRowHTML(model,catalog.dataset.catalogEngine));
  const row=list.lastElementChild;
  hydrateCredentialIcons(row);
  credentialCatalogChanged(catalog);
  return row;
}
function removeCredentialCatalogRow(catalog,row){
  const next=row.nextElementSibling||row.previousElementSibling;
  row.remove();
  credentialCatalogChanged(catalog);
  announceCredentialCatalog(catalog,'Đã gỡ model, có hiệu lực sau khi lưu');
  (next?.querySelector('[data-catalog-grip]')||catalog.querySelector('[data-catalog-act="add"]'))?.focus({preventScroll:true});
}
function moveCredentialCatalogRow(catalog,row,step){
  const target=step<0?row.previousElementSibling:row.nextElementSibling;if(!target)return;
  if(step<0)target.before(row);else target.after(row);
  row.querySelector('[data-catalog-grip]')?.focus({preventScroll:true});
  credentialCatalogChanged(catalog);
  announceCredentialCatalog(catalog,`Đã chuyển tới vị trí ${credentialCatalogRows(catalog).indexOf(row)+1}`);
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
    return `<label class="credential-catalog-pick" data-pick="${escapeHTML((model.id+' '+(model.name||'')).toLowerCase())}"><input type="checkbox" value="${escapeHTML(model.id)}"${checked?' checked':''}><code>${escapeHTML(model.id)}</code>${model.name&&model.name!==model.id?`<small>${escapeHTML(model.name)}</small>`:''}${listed?'<em>Đã có trong danh mục</em>':''}</label>`;
  }).join('');
  const protocol=catalog.closest('.credential-editor-form')?.querySelector('select[name="protocol"]')?.value||catalog.dataset.catalogProtocol;
  const sources={builtin:'Model có sẵn của CLI',official:'Từ dịch vụ chính thức',runner:'Từ tài khoản đã đăng nhập trên máy này'};
  const source=sources[probe.source]||`Từ Base URL ${escapeHTML(credentialModelsEndpoint(kind,protocol))}`;
  return `<div class="credential-catalog-probe-top"><span class="credential-input is-plain is-search">${credentialGlyph('search',14)}<input data-catalog-search placeholder="Tìm model" aria-label="Tìm model" autocomplete="off" spellcheck="false"></span><span class="credential-catalog-picked" data-catalog-picked></span><button type="button" class="credential-btn link credential-ink" data-catalog-act="toggle-all">Chọn tất cả</button></div>
    <div class="credential-catalog-probe-list" role="group" aria-label="Model khả dụng">${items}</div>
    <div class="credential-catalog-probe-foot"><small>${source} · Model được chọn sẽ vào danh mục, bỏ chọn sẽ bị gỡ</small><div><button type="button" class="credential-btn ghost credential-ink" data-catalog-act="probe-close">Hủy</button><button type="button" class="credential-btn primary credential-ink" data-catalog-act="probe-apply">Cập nhật danh sách</button></div></div>`;
}
function syncCredentialCatalogProbe(catalog){
  const panel=catalog.querySelector('[data-catalog-probe]'),query=String(panel.querySelector('[data-catalog-search]')?.value||'').trim().toLowerCase();
  const picks=[...panel.querySelectorAll('.credential-catalog-pick')];
  picks.forEach(pick=>{pick.hidden=!!query&&!pick.dataset.pick.includes(query)});
  const visible=picks.filter(pick=>!pick.hidden),checked=picks.filter(pick=>pick.querySelector('input').checked).length;
  const counter=panel.querySelector('[data-catalog-picked]');if(counter)counter.textContent=`Đã chọn ${checked} / ${picks.length}`;
  const toggle=panel.querySelector('[data-catalog-act="toggle-all"]');
  if(toggle){toggle.textContent=visible.length&&visible.every(pick=>pick.querySelector('input').checked)?'Bỏ chọn tất cả':'Chọn tất cả';toggle.disabled=!visible.length}
}
function closeCredentialCatalogProbe(catalog){
  const panel=catalog.querySelector('[data-catalog-probe]');
  catalog.probeToken=(catalog.probeToken||0)+1;catalog.probe=null;
  panel.hidden=true;panel.innerHTML='';
}
async function probeCredentialCatalog(row,catalog,button){
  const kind=credentialKind(row.dataset.credentialRow),form=row.querySelector('.credential-editor-form'),panel=catalog.querySelector('[data-catalog-probe]');
  const extra=form?.querySelector('input[name="extra"]'),secret=form?.querySelector('[name="secret"]')?.value.trim()||'',protocol=form?.querySelector('select[name="protocol"]')?.value,environment=form?.querySelector('select[name="extra"]')?.value;
  const extraError=extra?credentialExtraError(kind,extra.value,protocol):'';
  if(extraError){toast(extraError,'err');extra.focus();return}
  const token=(catalog.probeToken||0)+1;catalog.probeToken=token;catalog.probe=null;
  panel.hidden=false;
  panel.innerHTML=`<p class="credential-vline is-info" role="status">${credentialSpinner(13)}Đang lấy model khả dụng…</p>`;
  hydrateCredentialIcons(panel);
  setCredentialButtonBusy(button,'Đang lấy');
  let result;
  try{
    result=catalog.probeModels?await catalog.probeModels():await api(`/api/users/${state.currentUser.id}/cli-models/probe`,{method:'POST',body:JSON.stringify({
      engine:kind.models.engine,apiKey:secret,baseUrl:extra?.value.trim(),protocol,environment
    }),silent:true});
  }
  catch(err){result={error:err.message}}
  finally{setCredentialButtonBusy(button,'')}
  if(catalog.probeToken!==token||!catalog.isConnected)return;
  if(result?.error||!result?.models?.length){
    panel.innerHTML=`<div class="credential-catalog-probe-fail"><p class="credential-vline is-err" role="alert">${credentialGlyph('alert',13)}${escapeHTML(result?.error||'Dịch vụ không trả về model nào')}</p><div><button type="button" class="credential-btn ghost credential-ink" data-catalog-act="probe-close">Đóng</button><button type="button" class="credential-btn secondary credential-ink" data-catalog-act="probe">${credentialGlyph('refresh',13)}<span>Thử lại</span></button></div></div>`;
    hydrateCredentialIcons(panel);
    return;
  }
  const source=credentialCatalogSource(extra?.value);
  if(extra&&source!==catalog.dataset.catalogSource){
    const cleared=credentialCatalogRows(catalog);
    cleared.forEach(item=>item.remove());
    catalog.dataset.catalogSource=source;
    credentialCatalogChanged(catalog);
    if(cleared.length)announceCredentialCatalog(catalog,'Base URL đã đổi, danh mục model cũ đã được xóa, có hiệu lực sau khi lưu');
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
  const discovered=new Map(probe.models.map(model=>[model.id,model]));
  let removed=0;
  credentialCatalogRows(catalog).forEach(row=>{
    const id=row.querySelector('[data-catalog-field="id"]').value.trim();
    if(offered.has(id)&&!picked.has(id)){row.remove();removed++}else{
      kept.add(id);
      if(picked.has(id)&&discovered.has(id)){
        const current=credentialCatalogRowModel(row),fresh=discovered.get(id);
        row.outerHTML=credentialCatalogRowHTML({...current,...fresh,name:current.name,reasoningEfforts:current.reasoningEfforts},catalog.dataset.catalogEngine);
      }
    }
  });
  const added=probe.models.filter(model=>picked.has(model.id)&&!kept.has(model.id));
  const room=Math.max(0,CREDENTIAL_CATALOG_MAX-kept.size),list=catalog.querySelector('[data-catalog-list]');
  list.insertAdjacentHTML('beforeend',credentialCatalogModels(added.slice(0,room)).map(model=>credentialCatalogRowHTML(model,catalog.dataset.catalogEngine)).join(''));
  hydrateCredentialIcons(list);
  closeCredentialCatalogProbe(catalog);
  credentialCatalogChanged(catalog);
  const skipped=added.length-Math.min(added.length,room);
  const message=`Đã cập nhật danh mục model: thêm ${Math.min(added.length,room)}, gỡ ${removed}${skipped?`, ${skipped} mục không thêm vì vượt giới hạn`:''}, có hiệu lực sau khi lưu`;
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
    announceCredentialCatalog(catalog,credentialCatalogCopy(credentialCatalogMode(catalog),credentialKind(row?.dataset.credentialRow)).restored);
    catalog.querySelector('[data-catalog-act="add"]')?.focus({preventScroll:true});
    return;
  }
  if(action==='probe')return probeCredentialCatalog(row,catalog,catalog.querySelector('[data-catalog-act="probe"]'));
  if(action==='probe-close'){closeCredentialCatalogProbe(catalog);catalog.querySelector('[data-catalog-act="probe"]')?.focus({preventScroll:true});return}
  if(action==='toggle-all')return toggleCredentialCatalogProbe(catalog);
  if(action==='probe-apply')return applyCredentialCatalogProbe(catalog);
}
function credentialCatalogKeydown(event){
  const grip=event.target.closest?.('[data-catalog-grip]');
  if(!grip||!['ArrowUp','ArrowDown'].includes(event.key))return;
  event.preventDefault();
  moveCredentialCatalogRow(grip.closest('[data-credential-catalog]'),grip.closest('[data-catalog-row]'),event.key==='ArrowUp'?-1:1);
}
function validateCredentialCatalog(catalog,models){
  const error=models&&credentialCatalogError(models);
  if(!error)return true;
  toast(error.error,'err');
  const input=credentialCatalogFilledRows(catalog)[error.index]?.querySelector(`[data-catalog-field="${error.field||'id'}"]`);
  if(input){
    const box=input.closest('.credential-input');
    box.classList.add('is-bad');
    box.classList.remove('is-shake');
    void box.offsetWidth;
    box.classList.add('is-shake');
    input.focus();
  }
  return false;
}
// Platform features depend on these, in the order people should configure them.
const CREDENTIAL_RECOMMENDED=['gitToken','codeBuddyApiKey'];
const CREDENTIAL_PAT_WARNING='Hãy điền GitHub <b>personal access token (PAT)</b> và cấp quyền đọc/ghi <b>Contents</b> cùng <b>Pull requests</b> cho repository đích; thiếu quyền sẽ khiến việc clone / đẩy mã thất bại.';
function credentialKind(key){return CREDENTIAL_KINDS.find(kind=>kind.key===key)||null}
// Kinds with a dialog manage several entries there instead of the inline editor.
function credentialDialog(key){return credentialKind(key)?.dialog||null}
function credentialConfigured(kind,user){if(kind.key==='gitToken')return !!user.gitHubCredentialConfigured;return !!credentialValue(user,kind.agentCli?kind.key+'.apiKeyConfigured':kind.key+'Configured')}
function credentialUser(){return userById(state.currentUser?.id)||{}}
function credentialUI(){return state.credentialUI||(state.credentialUI={open:null,filter:'all',closed:{}})}
function credentialGlyph(name,size=16,cls=''){return `<svg class="agent-icon harness-icon${cls?' '+cls:''}" data-icon="${name}" width="${size}" height="${size}" aria-hidden="true" focusable="false"></svg>`}
function credentialSpinner(size=14){return credentialGlyph('arc-spinner',size,'credential-spin')}
function credentialLogo(kind,cls=''){
  const file=kind.logo?.endsWith('.png')?kind.logo:kind.logo+'.svg';
  return `<span class="credential-logo${kind.monochrome?' monochrome':''}${cls?' '+cls:''}">${kind.icon?credentialGlyph(kind.icon,18):`<img src="/images/credentials/${file}" alt="">`}</span>`;
}
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
    ?'Key này giống Key model bên thứ ba như DeepSeek, OpenAI, CodeBuddy sẽ từ chối; hãy điền Key bên thứ ba vào thông tin xác thực Claude Code hoặc Codex và đặt Base URL'
    :'CodeBuddy API Key thường bắt đầu bằng ck_; hãy chắc bạn đã sao chép đúng Key của CodeBuddy';
}
// The value an extra field starts from. An old network environment preselects what it meant (china → Trang nội địa),
// and one that cannot be guessed shows as Trang quốc tế, which is what the CLI uses.
function credentialExtraValue(kind,value){
  if(kind?.extra?.options)return codeBuddyEnvironmentIntent(value)?.value??'';
  return value||kind?.extra?.fallback||'';
}
function credentialExtraLabel(kind,value){
  if(kind.extra.options){const env=codeBuddyEnvironment(value)||CODEBUDDY_ENVIRONMENTS[0];return `${env.label} · ${env.site}`}
  return String(value||'').trim()||(kind.extra.url?'Địa chỉ chính thức':kind.extra.fallback||'');
}
function codeBuddyEnvironmentHintHTML(selected,stored=''){
  const raw=String(stored||'').trim(),legacy=codeBuddyEnvironmentLegacy(raw)?`Giá trị đã điền trước đây「${raw}」không nhận diện được; CodeBuddy thực tế kết nối theo Trang quốc tế. Bấm lưu để đổi sang môi trường đã chọn ở trên.`:'';
  return `${escapeHTML(legacy)}Key chỉ dùng được trên site đã tạo ra nó · <a href="${escapeHTML(codeBuddyKeyURL(selected))}" target="_blank" rel="noopener noreferrer" data-credential-key-link>${escapeHTML(codeBuddyKeyLinkLabel(selected))} ↗</a>`;
}
function credentialSelectFieldHTML(kind,stored,note=''){
  const selected=credentialExtraValue(kind,stored);
  const options=kind.extra.options.map(option=>`<option value="${escapeHTML(option.value)}"${option.value===selected?' selected':''}>${escapeHTML(option.label)} · ${escapeHTML(option.site)}</option>`).join('');
  return `<label class="credential-field"><span>${escapeHTML(kind.extra.label)}${note?`<em>${escapeHTML(note)}</em>`:''}</span><div class="credential-input is-plain is-select"><select name="extra">${options}</select></div><small class="credential-env-hint" data-credential-env-hint>${codeBuddyEnvironmentHintHTML(selected,stored)}</small></label>`;
}
// Keeps the  để lấy API Key links pointed at the edition that is selected now.
function refreshCredentialEnvironmentLinks(scope,value){
  const hint=scope?.querySelector('[data-credential-env-hint]');if(hint)hint.innerHTML=codeBuddyEnvironmentHintHTML(value);
  scope?.querySelectorAll('[data-wizard-link][data-environment-link]').forEach(link=>{link.href=codeBuddyKeyURL(value);link.textContent=`${codeBuddyKeyLinkLabel(value)} ↗`});
}
function credentialExtraError(kind,value,protocol){
  const text=String(value||'').trim();
  if(!text&&kind.extra?.required?.(protocol))return 'Hãy điền Base URL';
  if(!kind.extra?.url||!text)return '';
  try{const url=new URL(text);if(url.protocol==='http:'||url.protocol==='https:')return ''}catch{}
  return 'Hãy điền địa chỉ bắt đầu bằng http:// hoặc https://';
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
  if(!result.valid)return `Token còn hiệu lực nhưng thiếu quyền: ${(result.missing||[]).join(', ')}; hãy bổ sung ủy quyền trên nền tảng lưu mã`;
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
  const label={empty:'Chưa cấu hình',ok:'Đã cấu hình',busy:'Đang kiểm tra',err:'Có lỗi'}[status];
  const tone={empty:'muted',ok:'success',busy:'info',err:'error'}[status];
  return `<span class="hui-badge credential-status" data-size="sm" data-tone="${tone}" data-status="${status}" role="status">${lamp}<span class="hui-badge-label">${label}</span></span>`;
}
function credentialSubline(kind,user){
  const dot='<i class="credential-dot"></i>',use=`<span>${escapeHTML(kind.use)}</span>`;
  if(!credentialConfigured(kind,user))return use;
  if(kind.key==='gitToken'){
    const verify=gitTokenVerifyFor({id:state.currentUser?.id}),result=verify?.result,source=`<span>${user.gitTokenOAuth?'Ủy quyền Gongfeng':'Personal access token'}</span>`;
    if(verify?.busy)return source+dot+'<span class="is-info">Đang kiểm tra…</span>';
    if(!result)return source+dot+use;
    if(result.error)return source+dot+`<span class="is-err">${escapeHTML(result.error)}</span>`;
    if(!result.valid)return source+dot+`<span class="is-err">Thiếu quyền ${escapeHTML((result.missing||[]).join(', '))}</span>`;
    return source+dot+'<span class="is-ok">Token còn hiệu lực</span>'+(result.scopes?.length?dot+`<span class="credential-mono">${escapeHTML(result.scopes.join(' · '))}</span>`:'');
  }
  if(kind.key==='codeBuddyApiKey'){
    const raw=String(user.codeBuddyInternetEnvironment||'').trim(),env=codeBuddyEnvironment(raw);
    return `<span>Môi trường mạng</span>${env?`<span class="credential-mono">${escapeHTML(env.label)}</span>`:`<span class="is-err">「${escapeHTML(raw)}」không nhận diện được, kết nối theo Trang quốc tế</span>`}${dot}${use}`;
  }
  if(kind.extra){
    const models=kind.models&&credentialCatalogModels(credentialValue(user,kind.models.name)).length,baseUrl=credentialValue(user,kind.extra.name);
    const protocol=kind.protocol?kind.protocol.options.find(([id])=>id===credentialProtocol(kind,user))?.[1]:'';
    return (baseUrl?`<span class="credential-mono">${escapeHTML(baseUrl)}</span>`:'<span>Địa chỉ chính thức</span>')+(protocol?dot+`<span>${escapeHTML(protocol)}</span>`:'')+(models?dot+`<span>${models} model tùy chỉnh</span>`:'')+dot+use;
  }
  return use;
}
function credentialFieldHTML({label,note='',name,type='password',value='',placeholder='',hint='',hintTone='',id='',multiline=false}){
  if(multiline)return `<label class="credential-field"><span>${escapeHTML(label)}${note?`<em>${escapeHTML(note)}</em>`:''}</span><div class="credential-input"><textarea${id?` id="${id}"`:''} name="${name}" rows="5" maxlength="16384" spellcheck="false" autocomplete="off" placeholder="${escapeHTML(placeholder)}"></textarea></div><small data-credential-hint="${name}" class="${hintTone?'is-'+hintTone:''}">${escapeHTML(hint)}</small></label>`;
  const secret=type==='password';
  // Secrets are never serialized into markup; callers assign input.value after rendering.
  return `<label class="credential-field"><span>${escapeHTML(label)}${note?`<em>${escapeHTML(note)}</em>`:''}</span><div class="credential-input${secret?'':' is-plain'}${hintTone?' is-'+(hintTone==='err'?'bad':hintTone==='ok'?'good':hintTone):''}"><input${id?` id="${id}"`:''} name="${name}" type="${type}" ${secret?'autocomplete="new-password"':'autocomplete="off" spellcheck="false"'}${secret?'':` value="${escapeHTML(value)}"`} placeholder="${escapeHTML(placeholder)}">${secret?`<button type="button" class="credential-eye" data-credential-eye aria-label="Hiện">${credentialGlyph('eye',15)}</button>`:''}</div>${hint!==null?`<small data-credential-hint="${name}"${hintTone?` class="is-${hintTone}"`:''}>${escapeHTML(hint)}</small>`:''}</label>`;
}
function credentialOAuthHTML(label,note){
  return (typeof githubConnectHTML==='function'?githubConnectHTML():'')+`<div class="credential-oauth" data-gongfeng-oauth${state.gongfengOAuthEnabled?'':' hidden'}><a class="credential-btn secondary credential-ink" href="/api/auth/gongfeng/start" target="_blank" rel="noopener noreferrer">${credentialLogo(credentialKind('gitToken'),'is-mini')}<span>${label}</span></a><small>${note}</small></div>`;
}
function credentialPatWarningHTML(show){return `<div class="credential-note is-warn git-token-pat-warning"${show?'':' hidden'}>${credentialGlyph('alert',14)}<span>${CREDENTIAL_PAT_WARNING}</span></div>`}
const GIT_TOKEN_REPLACE_HINT='Sau khi lưu sẽ thay token hiện tại, có hiệu lực từ tác vụ tiếp theo';
function credentialAgo(at){
  const seconds=Math.max(0,Math.round((Date.now()-at)/1000));
  if(seconds<60)return 'Vừa xong';
  if(seconds<3600)return `${Math.floor(seconds/60)} phút trước`;
  if(seconds<86400)return `${Math.floor(seconds/3600)} giờ trước`;
  return `${Math.floor(seconds/86400)} ngày trước`;
}
function gitTokenDetailHTML(kind,user){
  const verify=gitTokenVerifyFor({id:state.currentUser?.id}),result=verify?.result,missing=result?.missing||[];
  const scopes=(result?.scopes||[]).filter(scope=>!missing.includes(scope)).map(scope=>`<code class="credential-scope">${escapeHTML(scope)}</code>`)
    .concat(missing.map(scope=>`<code class="credential-scope is-miss" title="Thiếu quyền này">${escapeHTML(scope)}</code>`)).join('');
  const ago=verify?.checkedAt?` · ${credentialAgo(verify.checkedAt)}`:'';
  const checked=verify?.busy?`<span class="is-info">${credentialSpinner(13)}Đang kiểm tra…</span>`
    :!result?'<span class="is-muted">Chưa kiểm tra</span>'
    :result.error?`<span class="is-err">${escapeHTML(result.error)}</span>${ago}`
    :!result.valid?`<span class="is-err">Token còn hiệu lực nhưng thiếu quyền ${escapeHTML(missing.join(', '))}</span>${ago}`
    :`<span class="is-ok">Token còn hiệu lực</span>${ago}`;
  return `<dl class="credential-kv" data-git-detail><dt>Nguồn</dt><dd>${user.gitTokenOAuth?'Ủy quyền Gongfeng · tự gia hạn khi hết hạn':'Ủy quyền GitHub / personal access token'}</dd><dt>Quyền</dt><dd>${scopes||'<span class="is-muted">Hiện sau khi kiểm tra</span>'}</dd><dt>Kiểm tra gần nhất</dt><dd>${checked}</dd><dt>Dùng cho</dt><dd>${escapeHTML(kind.description)}</dd></dl>`;
}
function gitTokenEditorHTML(kind,user){
  const failure=gitTokenFailure(gitTokenVerifyFor({id:state.currentUser?.id})?.result);
  return `<div class="credential-editor-form is-detail">
    ${credentialPatWarningHTML(!!failure)}
    ${gitTokenDetailHTML(kind,user)}
    <div class="credential-swap">
      <div class="credential-bar" data-credential-pane="bar">${credentialOAuthHTML('Ủy quyền lại','')}<button type="button" class="credential-btn ${failure?'primary':'secondary'} credential-ink" data-credential-act="replace">${credentialGlyph('key',14)}<span>Đổi token thủ công</span></button><button type="button" class="credential-btn ghost danger credential-ink" data-credential-act="delete">${credentialGlyph('trash',14)}<span>Xóa thông tin xác thực</span></button></div>
      <div class="credential-replace is-off" data-credential-pane="form" inert>
        ${credentialFieldHTML({label:'Personal access token mới',note:'Trước khi lưu sẽ kiểm tra hiệu lực và quyền',name:'secret',placeholder:'Dán GitHub personal access token (PAT)',hint:GIT_TOKEN_REPLACE_HINT})}
        <div class="credential-replace-acts"><button type="button" class="credential-btn ghost credential-ink" data-credential-act="cancel">Hủy</button><button type="button" class="credential-btn primary credential-ink" data-credential-act="save" disabled>Kiểm tra và lưu</button></div>
        <p class="credential-vline git-token-verify-status" data-git-verify-line role="status"></p>
      </div>
      <div class="credential-confirm is-off" data-credential-pane="confirm" role="alert" inert><div>Xóa <b>${escapeHTML(kind.short)}</b>？${escapeHTML(kind.impact)}</div><button type="button" class="credential-btn ghost credential-ink" data-credential-act="undelete">Hủy</button><button type="button" class="credential-btn danger-solid credential-ink" data-credential-act="confirm-delete">Xóa</button></div>
    </div>
  </div>`;
}
function credentialEditorHTML(kind,user){
  if(kind.key==='gitToken')return gitTokenEditorHTML(kind,user);
  const extraStored=kind.extra?credentialValue(user,kind.extra.name):'';
  const extraValue=kind.extra?credentialExtraValue(kind,extraStored):'';
  const extraField=!kind.extra?''
    :kind.extra.options?credentialSelectFieldHTML(kind,extraStored,'Khớp với site đã cấp Key')
    :credentialFieldHTML({label:kind.extra.label,note:kind.extra.note||(kind.extra.url?'Để trống để dùng địa chỉ chính thức':''),name:'extra',type:kind.extra.url?'url':'text',value:extraValue,placeholder:kind.extra.placeholder,hint:null});
  const protocolValue=kind.protocol?credentialProtocol(kind,user):'';
  const protocolField=kind.protocol?credentialProtocolHTML(kind,protocolValue):'';
  const legacy=!!(kind.extra?.options&&codeBuddyEnvironmentLegacy(user[kind.extra.name]));
  return `<div class="credential-editor-form${kind.models?' has-catalog':''}"${kind.extra?` data-original="${escapeHTML(extraValue)}"`:''}${kind.protocol?` data-original-protocol="${escapeHTML(protocolValue)}"`:''}${legacy?' data-legacy="true"':''}>
    ${credentialFieldHTML({label:kind.short,note:'Đã cấu hình · để trống nếu không đổi',name:'secret',placeholder:'Nhập giá trị mới để thay',hint:kind.hint||''})}
    ${extraField}
    ${protocolField}
    ${kind.models?credentialCatalogHTML(kind,user):''}
    <div class="credential-swap">
      <div class="credential-editor-foot" data-credential-pane="foot"><button type="button" class="credential-btn ghost danger credential-ink" data-credential-act="delete">${credentialGlyph('trash',14)}<span>Xóa</span></button><div><button type="button" class="credential-btn ghost credential-ink" data-credential-act="cancel">Hủy</button><button type="button" class="credential-btn primary credential-ink" data-credential-act="save"${legacy?'':' disabled'}>Lưu</button></div></div>
      <div class="credential-confirm is-off" data-credential-pane="confirm" role="alert"><div>Xóa <b>${escapeHTML(kind.short)}</b>？${escapeHTML(kind.impact)}</div><button type="button" class="credential-btn ghost credential-ink" data-credential-act="undelete">Hủy</button><button type="button" class="credential-btn danger-solid credential-ink" data-credential-act="confirm-delete">Xóa</button></div>
    </div>
  </div>`;
}
function credentialRowMainHTML(kind,user){
  const managed=!!kind.dialog,on=credentialConfigured(kind,user),open=on&&credentialUI().open===kind.key,status=credentialStatus(kind,user);
  const end=on&&!managed
    ?`${credentialBadgeHTML(status)}${kind.key==='gitToken'?`<span class="credential-tip" data-tip="Kiểm tra lại"><button type="button" class="credential-btn ghost icon credential-ink git-token-verify" data-credential-act="verify" aria-label="Kiểm tra lại"${status==='busy'?' disabled':''}>${credentialGlyph('refresh',15)}</button></span>`:''}<span class="credential-chevron">${credentialGlyph('chevron-down',16)}</span>`
    :`${credentialBadgeHTML(status)}<button type="button" class="credential-btn secondary credential-ink" data-credential-act="setup"${managed?' aria-haspopup="dialog"':''}>${managed?kind.action:'Đi cấu hình'}<span class="credential-nudge">${credentialGlyph('chevron-right',13)}</span></button>`;
  const interaction=managed?'':` role="button" tabindex="0"${on?` aria-expanded="${open}"`:''}`;
  return `<div class="credential-row-main credential-ink"${interaction}>${credentialLogo(kind,on?'':'is-dim')}<div class="credential-row-who"><b>${escapeHTML(kind.name)}<span>${escapeHTML(kind.short)}</span></b><small>${credentialSubline(kind,user)}</small></div><div class="credential-row-end">${end}</div></div>`;
}
function credentialRowHTML(kind,user){
  const on=credentialConfigured(kind,user),open=on&&credentialUI().open===kind.key;
  const editor=on&&!kind.dialog?credentialEditorHTML(kind,user):'';
  return `<div class="credential-row${on?' is-configured':''}${open?' is-open':''}" data-credential-row="${kind.key}">${credentialRowMainHTML(kind,user)}<div class="credential-editor"><div><div class="credential-editor-inner">${editor}</div></div></div></div>`;
}
function credentialGroupHTML(group,user){
  const kinds=CREDENTIAL_KINDS.filter(kind=>kind.group===group.id),done=kinds.filter(kind=>credentialConfigured(kind,user)).length,closed=!!credentialUI().closed[group.id];
  return `<section class="credential-group" data-credential-group="${group.id}"><button type="button" class="credential-group-head credential-ink" aria-expanded="${!closed}">${credentialGlyph(group.icon,16)}<strong>${escapeHTML(group.name)}</strong><span class="credential-group-sub">${escapeHTML(group.sub)}</span><span class="credential-group-meta">${done} / ${kinds.length}</span><span class="credential-group-chevron">${credentialGlyph('chevron-down',15)}</span></button><div class="credential-fold${closed?' is-closed':''}"><div><div class="credential-rows">${kinds.map(kind=>credentialRowHTML(kind,user)).join('')}<p class="credential-empty" hidden></p></div></div></div></section>`;
}
function credentialProgressHTML(user){
  const done=CREDENTIAL_KINDS.filter(kind=>credentialConfigured(kind,user)).length,next=credentialNextKind(user);
  return `<div class="credential-segs" aria-hidden="true">${CREDENTIAL_KINDS.map((_,index)=>`<i${index<done?' class="is-on"':''}></i>`).join('')}</div><span class="credential-count"><b>${done}</b> / ${CREDENTIAL_KINDS.length} đã cấu hình</span>`
    +(next?`<span class="credential-next">Tiếp theo<button type="button" class="credential-btn link credential-ink" data-credential-setup="${next.key}">Cấu hình ${escapeHTML(next.short)}<span class="credential-nudge">${credentialGlyph('arrow-right',13)}</span></button></span>`:`<span class="credential-ready">${credentialGlyph('check',15)}Các thông tin xác thực gợi ý đã sẵn sàng hết</span>`);
}
function credentialFilterHTML(user){
  const done=CREDENTIAL_KINDS.filter(kind=>credentialConfigured(kind,user)).length,filter=credentialUI().filter;
  return [['all','Tất cả',CREDENTIAL_KINDS.length],['configured','Đã cấu hình',done],['missing','Chưa cấu hình',CREDENTIAL_KINDS.length-done]].map(([id,label,count])=>`<button type="button" role="tab" data-credential-filter="${id}" class="${filter===id?'active':''}" aria-selected="${filter===id}">${label}<span>${count}</span></button>`).join('');
}
function credentialPanelHTML(user){
  return `<header class="credential-section-head"><div><h2>Thông tin xác thực cá nhân</h2><p>SOP dùng thông tin xác thực của người phụ trách yêu cầu (hoặc Owner dự án nếu chưa chỉ định). Lưu xong sẽ áp dụng cho lần chạy sau.</p></div><button type="button" class="credential-btn primary credential-ink" id="credentialAddButton" data-credential-add><span class="credential-turn">${credentialGlyph('plus',15)}</span><span>Thêm thông tin xác thực</span></button></header>
    <div class="credential-note credential-subscription-note">
      ${credentialGlyph('monitor',16)}
      <div><strong>Dùng gói đăng ký sẵn có</strong>
        <p>Gắn máy đã đăng nhập Claude Code, Codex hoặc Antigravity, chọn「Đăng nhập tài khoản」là có thể dùng hạn mức gói đăng ký để chạy hội thoại và SOP, không cần API Key.</p></div>
      <a class="button ghost small" href="#/runners"><span>Cấu hình máy thực thi</span>${credentialGlyph('arrow-right',14)}</a>
    </div>
    <div class="credential-progress">${credentialProgressHTML(user)}</div>
    <div class="credential-toolbar"><div class="credential-filter" role="tablist" aria-label="Lọc thông tin xác thực">${credentialFilterHTML(user)}</div></div>
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
    empty.textContent=filter==='configured'?'Nhóm này chưa có thông tin xác thực':'Nhóm này đã cấu hình hết';
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
  let saved;
  if(key==='gitToken'){
    const endpoint=`/api/users/${user.id}/git-http-credentials`,credentials=await api(endpoint),existing=credentials.find(credential=>credential.host==='github.com');
    const listed=await api(endpoint,{method:'PUT',body:JSON.stringify({...(existing?{id:existing.id}:{}),name:'GitHub',platform:'github',host:'github.com',username:'x-access-token',token:values.secret})});
    saved={...credentialUser(),id:user.id,gitHubCredentialConfigured:listed.some(item=>item.host==='github.com'),gitHttpCredentialsConfigured:listed.length>0};
  }else saved=await api(`/api/users/${user.id}/credentials`,{method:'PUT',body:JSON.stringify(credentialSavePayload(kind,values))});
  if(key==='gitToken'&&!options.verified)state.gitTokenVerify=null;
  storeCredentialUser(saved);
  notifyCredentialsChanged();
  return saved;
}
async function deleteCredential(key){
  const user=state.currentUser,kind=credentialKind(key);
  let saved;
  if(key==='gitToken'){
    const endpoint=`/api/users/${user.id}/git-http-credentials`,credentials=await api(endpoint),existing=credentials.find(credential=>credential.host==='github.com');
    const listed=existing?await api(`${endpoint}/${encodeURIComponent(existing.id)}`,{method:'DELETE'}):credentials;
    saved={...credentialUser(),id:user.id,gitHubCredentialConfigured:listed.some(item=>item.host==='github.com'),gitHttpCredentialsConfigured:listed.length>0};
  }else saved=await api(`/api/users/${user.id}/credentials`,{method:'PUT',body:JSON.stringify(credentialClearPayload(kind))});
  if(key==='gitToken')state.gitTokenVerify=null;
  storeCredentialUser(saved);
  notifyCredentialsChanged();
  return saved;
}
// Git 令牌必须先校验通过才保存，校验Thất bại时什么都不写入。
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
  if(announce){const failure=gitTokenFailure(result);toast(failure||'Git token đã được kiểm tra',failure?'err':'ok')}
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
  if(!validateCredentialCatalog(catalog,models))return;
  setCredentialButtonBusy(button,key==='gitToken'?'Đang kiểm tra':'Đang lưu');
  try{
    if(key==='gitToken'){
      const outcome=await verifyAndSaveGitToken(secret);
      if(!outcome.saved){
        setCredentialButtonBusy(button,'');
        const line=row.querySelector('[data-git-verify-line]'),warning=row.querySelector('.git-token-pat-warning');
        if(line){line.classList.add('is-err');line.innerHTML=credentialGlyph('alert',13)+escapeHTML(`${gitTokenFailure(outcome.result)||'Git Token không vượt qua kiểm tra'} (chưa lưu)`);hydrateCredentialIcons(line)}
        if(warning)warning.hidden=false;
        return;
      }
      credentialSaved(key,'Git Token hợp lệ và đã được lưu');
      return;
    }
    await saveCredential(key,{secret,...(extra?{extra:extra.value}:{}),...(protocol?{protocol:protocol.value}:{}),...(models?{models}:{})});
    credentialSaved(key,`${kind.short} đã được lưu`);
  }catch(err){toast(err.message,'err');setCredentialButtonBusy(button,'')}
}
async function confirmDeleteCredential(row,button){
  const key=row.dataset.credentialRow,kind=credentialKind(key);
  setCredentialButtonBusy(button,'Đang xóa');
  try{await deleteCredential(key);credentialSaved(key,`${kind.short} đã được xóa`)}
  catch(err){toast(err.message,'err');setCredentialButtonBusy(button,'')}
}
function toggleCredentialSecret(button){
  const input=button.previousElementSibling,show=input.type==='password';
  input.type=show?'text':'password';
  button.setAttribute('aria-label',show?'Ẩn':'Hiện');
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
  const dialog=credentialDialog(key);if(dialog)return dialog();
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
    credentialCatalogKeydown(event);
    if(event.defaultPrevented)return;
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
      syncCredentialCatalogEfforts(event.target.closest('[data-catalog-row]'));
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

// ---- Thêm thông tin xác thực向导 ----
let credentialWizard=null;
function credentialWizardDefaults(kind,user){return kind?.extra?credentialExtraValue(kind,credentialValue(user,kind.extra.name)):''}
function openCredentialWizard(key){
  const user=credentialUser();
  if(!state.currentUser||state.currentUser.isGuest||!$('#credentialPanel'))return;
  const dialog=credentialDialog(key);if(dialog)return dialog();
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
    scrim.innerHTML=`<section class="credential-dialog" role="dialog" aria-modal="true" aria-label="Thêm thông tin xác thực" tabindex="-1"><div class="credential-dialog-top"><div class="credential-dots" aria-hidden="true"><span></span><span></span><span></span></div><div><span data-wizard-step role="status"></span><button type="button" class="credential-btn ghost icon credential-ink" data-wizard-close aria-label="Đóng">${credentialGlyph('close',15)}</button></div></div><div class="credential-dialog-body"></div><div class="credential-dialog-foot"></div></section>`;
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
  return validity.ok?{text:'Định dạng hợp lệ',tone:'ok'}:{text:kind.hint||'Chỉ được gắn vào khi tác vụ bạn phụ trách chạy; sau khi lưu sẽ không còn hiện dạng rõ.',tone:''};
}
function credentialWizardStepOneHTML(user){
  const wizard=credentialWizard,all=CREDENTIAL_KINDS.every(kind=>credentialConfigured(kind,user));
  const groups=CREDENTIAL_GROUPS.map(group=>`<p class="credential-opt-label">${escapeHTML(group.name)}</p><div class="credential-opts" role="radiogroup" aria-label="${escapeHTML(group.name)}">${CREDENTIAL_KINDS.filter(kind=>kind.group===group.id).map((kind,index)=>{
    const on=credentialConfigured(kind,user),selected=wizard.kind===kind.key;
    return `<button type="button" class="credential-opt${selected?' is-selected':''}" style="--i:${index}" role="radio" aria-checked="${selected}" data-wizard-kind="${kind.key}"${on&&!kind.dialog?' disabled':''}><span class="credential-radio">${credentialGlyph('check',10)}</span>${credentialLogo(kind)}<span class="credential-opt-text"><b>${escapeHTML(kind.name)}</b><small>${escapeHTML(kind.short)}</small></span>${on?'<span class="credential-pill is-ok">Đã cấu hình</span>':CREDENTIAL_RECOMMENDED.includes(kind.key)?'<span class="credential-pill">Gợi ý</span>':''}</button>`;
  }).join('')}</div>`).join('');
  return `<div class="credential-question">${credentialGlyph('key',16)}<span>Bạn muốn thêm mục nào?</span><small>Chọn một mục · mục đã có thể sửa trong danh sách</small></div>${groups}${all?`<div class="credential-note">${credentialGlyph('check',14)}<span>Đã cấu hình hết, có thể sửa trong danh sách.</span></div>`:''}`;
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
  const methods=github?`<div class="github-methods" role="tablist" aria-label="Cách kết nối GitHub"><button type="button" role="tab" data-github-method="oauth" aria-selected="${!manual}">Ủy quyền GitHub</button><button type="button" role="tab" data-github-method="manual" aria-selected="${manual}">Access token</button></div>`:'';
  if(github&&!manual)return `<div class="credential-question">${credentialLogo(kind,'is-mini')}<span>Kết nối GitHub</span><small class="credential-description">Sau khi kết nối, bạn có thể chọn repository để clone mã, tạo nhánh và đẩy sản phẩm bàn giao.</small></div>${methods}${credentialOAuthHTML('Ủy quyền bằng Gongfeng','')}`;
  const validity=credentialWizardValid();
  const {text:secretHint,tone:secretTone}=credentialWizardSecretHint(kind,wizard,validity);
  const secretField=credentialFieldHTML({label:github?'Personal access token (PAT)':kind.short,name:'secret',id:'credentialWizardSecret',placeholder:kind.placeholder,hint:secretHint,hintTone:secretTone});
  const extraField=!kind.extra?''
    :kind.extra.options?credentialSelectFieldHTML(kind,wizard.extra)
    :credentialFieldHTML({label:kind.extra.label,note:kind.extra.note||(kind.extra.url?'Tùy chọn · để trống để dùng địa chỉ chính thức':''),name:'extra',type:kind.extra.url?'url':'text',value:wizard.extra,placeholder:kind.extra.placeholder,hint:''});
  const protocolField=kind.protocol?credentialProtocolHTML(kind,wizard.protocol||credentialProtocol(kind,{})):'';
  // The network environment decides where the key comes from, so it is chosen first.
  return `<div class="credential-question">${credentialLogo(kind,'is-mini')}<span>${github?'Dùng access token':'Cấu hình '+escapeHTML(kind.short)}</span><small class="credential-description">${escapeHTML(kind.description)}</small></div>${methods}<ol class="credential-guide">${guide}</ol><div class="credential-fields">
    ${kind.extra?.options?extraField+secretField:secretField+extraField+protocolField}
  </div>`;
}
function credentialWizardStepThreeHTML(kind){
  const wizard=credentialWizard;
  if(wizard.done)return `<div class="credential-saved"><span class="credential-check-big"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 12.5l4.2 4.2L19 7"/></svg></span><h4>${escapeHTML(kind.short)} Đã lưu</h4><p>Có hiệu lực khi tác vụ bạn phụ trách chạy lần sau; bạn có thể mở lại trong danh sách để sửa bất cứ lúc nào.</p></div>`;
  const extra=kind.extra?`<dt>${escapeHTML(kind.extra.label)}</dt><dd${kind.extra.url?' class="credential-mono"':''}>${escapeHTML(credentialExtraLabel(kind,wizard.extra))}</dd>`:'';
  const warning=credentialSecretWarning(kind,wizard.secret);
  const line=wizard.error?`<p class="credential-vline is-err" role="alert">${credentialGlyph('alert',13)}${escapeHTML(wizard.error)}</p>`
    :wizard.saving&&kind.key==='gitToken'?`<p class="credential-vline is-info" role="status">${credentialSpinner(13)}Đang kiểm tra hiệu lực và quyền của token…</p>`
    :kind.key==='gitToken'?`<p class="credential-vline git-token-verify-status">${credentialGlyph('lock',13)}Trước khi lưu, hệ thống sẽ kiểm tra token còn hiệu lực và đủ quyền</p>`
    :warning?`<p class="credential-vline is-warn" role="status">${credentialGlyph('alert',13)}${escapeHTML(warning)}</p>`:'';
  return `<div class="credential-question">${credentialGlyph('check',16)}<span>Xác nhận và lưu</span><small>Key chỉ được gắn khi tác vụ chạy, không ghi vào tài sản Agent</small></div><div class="credential-summary"><div>${credentialGlyph('key',14)}${escapeHTML(kind.name)}</div><dl><dt>Thông tin xác thực</dt><dd>${escapeHTML(kind.short)}</dd><dt>Key</dt><dd class="credential-mask">Đã điền ••••••••</dd>${extra}<dt>Có hiệu lực</dt><dd>Tác vụ tiếp theo</dd></dl></div>${line}`;
}
function credentialWizardFootHTML(){
  const wizard=credentialWizard;
  if(wizard.step===2&&wizard.kind==='gitToken'&&wizard.githubMethod!=='manual')return '<span class="credential-kbd"><kbd>Esc</kbd> đóng</span><span class="github-private-note">Chỉ ủy quyền cho tài khoản hiện tại</span>';
  if(wizard.done)return `<span></span><button type="button" class="credential-btn primary credential-ink" data-wizard-done>Hoàn tất</button>`;
  const back=wizard.step===1||(wizard.direct&&wizard.step===2)
    ?'<span class="credential-kbd"><kbd>Esc</kbd> đóng</span>'
    :`<button type="button" class="credential-btn ghost credential-ink" data-wizard-back${wizard.saving?' disabled':''}><span class="credential-nudge is-left">${credentialGlyph('arrow-left',13)}</span>Bước trước</button>`;
  if(wizard.step<3){
    const ok=wizard.step===1?!!wizard.kind&&(!!credentialDialog(wizard.kind)||!credentialConfigured(credentialKind(wizard.kind),credentialUser())):credentialWizardValid().ok;
    return `${back}<button type="button" class="credential-btn secondary credential-ink" data-wizard-next${ok?'':' disabled'}>Bước tiếp<span class="credential-nudge">${credentialGlyph('chevron-right',13)}</span></button>`;
  }
  const gongfeng=wizard.kind==='gitToken';
  return `${back}<button type="button" class="credential-btn primary credential-ink" data-wizard-next${wizard.saving?' disabled data-busy="true"':''}>${wizard.saving?credentialSpinner(14)+`<span>${gongfeng?'Đang kiểm tra':'Đang lưu'}</span>`:credentialGlyph('check',14)+`<span>${gongfeng?'Kiểm tra và lưu':'Lưu thông tin xác thực'}</span>`}</button>`;
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
  scrim.querySelector('[data-wizard-step]').textContent=wizard.done?'Đã xong':`${wizard.step} / 3`;
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
    const dialog=credentialDialog(wizard.kind);
    if(!wizard.kind||(!dialog&&credentialConfigured(credentialKind(wizard.kind),credentialUser())))return;
    if(dialog){
      closeCredentialWizard(true);
      return dialog();
    }
    wizard.step=2;paintCredentialWizard(1);setTimeout(focusCredentialWizard,220);return;
  }
  if(wizard.step===2){
    if(!credentialWizardValid().ok){const pane=$('#credentialWizard .credential-pane:not(.is-leaving)');if(pane)setCredentialHint(pane,'secret',credentialWizardValid().error||'Hãy điền key trước','err');return}
    wizard.step=3;wizard.error='';paintCredentialWizard(1);setTimeout(focusCredentialWizard,220);return;
  }
  const kind=credentialKind(wizard.kind),secret=String(wizard.secret).trim();
  wizard.saving=true;wizard.error='';paintCredentialWizard(0);focusCredentialWizard();
  try{
    if(kind.key==='gitToken'){
      const outcome=await verifyAndSaveGitToken(secret);
      if(!outcome.saved){wizard.error=`${gitTokenFailure(outcome.result)||'Git Token không vượt qua kiểm tra'} (chưa lưu)`;return}
    }else await saveCredential(kind.key,{secret,...(kind.extra?{extra:wizard.extra}:{}),...(kind.protocol?{protocol:wizard.protocol}:{})});
    wizard.done=true;wizard.secret='';
    renderCredentialPanel();
    toast(kind.key==='gitToken'?'Git Token hợp lệ và đã được lưu':`${kind.short} đã được lưu`,'ok');
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
  if(credentialWizard?.kind==='gitToken'&&fresh.gitHubCredentialConfigured)closeCredentialWizard(true);
  if(row.classList.contains('is-configured')!==!!fresh.gitHubCredentialConfigured){
    const holder=document.createElement('div');
    holder.innerHTML=credentialRowHTML(credentialKind('gitToken'),fresh);
    row.replaceWith(holder.firstElementChild);
    panel.querySelector('.credential-progress').innerHTML=credentialProgressHTML(fresh);
    panel.querySelector('.credential-filter').innerHTML=credentialFilterHTML(fresh);
    applyCredentialFilter(panel,fresh);
    hydrateCredentialIcons(panel);
  }
  if(!fresh.gitHubCredentialConfigured){repaintCredentialRow('gitToken');return}
  state.gitTokenAnnounceVerify=true;
  toast('Đã lưu ủy quyền Gongfeng, đang kiểm tra hiệu lực…');
  await refreshGitTokenStatus();
}

async function renderPersonalSettings(){
  const host=$('#personalSettingsContent');if(!host)return;
  const user=state.currentUser;
  if(!user||user.isGuest){
    host.innerHTML='<section class="personal-settings-card"><h2>Đăng nhập tài khoản</h2><p>Sau khi đăng nhập, bạn có thể quản lý Git Token và API Key của mình.</p><button class="button primary" id="settingsLogin">Đăng nhập</button><button class="button ghost" id="localAcceptanceLogin" hidden>Đăng nhập nghiệm thu cục bộ</button></section>';
    host.querySelector('#settingsLogin').onclick=()=>{window.location.href=state.loginUrl||'/login.html'};
    try{const capability=await api('/api/auth/local',{silent:true}),button=host.querySelector('#localAcceptanceLogin');if(button&&capability.enabled){button.hidden=false;button.onclick=async()=>{try{const result=await api('/api/auth/local',{method:'POST',body:'{}'});state.currentUser=result.user;state.data.users=await api('/api/users');window.AgentFlowExecutorModels=null;window.AgentFlowAssistantModels?.load?.();renderUserBadge();renderPersonalSettings();toast('Đã đăng nhập tài khoản nghiệm thu cục bộ')}catch(err){toast(err.message)}}}}catch{}
    return;
  }
  const storedUser=userById(user.id)||{};
  const gongfengInURL=new URLSearchParams(location.search).get('gongfeng')==='ok';
  if(state.gongfengReturned||gongfengInURL){
    state.gongfengReturned=false;
    if(gongfengInURL)history.replaceState(null,'',location.pathname+location.hash);
    state.gitTokenVerify=null;
    state.gitTokenAnnounceVerify=true;
    toast('Đã lưu ủy quyền Gongfeng, đang kiểm tra hiệu lực…');
  }
  if(credentialWizard)closeCredentialWizard(true);
  host.innerHTML=`<section class="credential-account credential-enter" style="--i:0"><span class="credential-avatar">${userAvatarHTML(user, 150)}</span><div><b>${escapeHTML(userDisplay(user))}</b><small>Thông tin xác thực được tách theo người dùng, chỉ gắn vào khi tác vụ bạn phụ trách chạy.</small></div><span class="credential-account-note">${credentialGlyph('lock',14)}Gắn theo tác vụ, không ghi vào tài sản Agent</span></section><section class="credential-panel credential-enter is-entering" id="credentialPanel" style="--i:1"></section>`;
  const panel=host.querySelector('#credentialPanel');
  bindCredentialPanel(panel);
  renderCredentialPanel();
  loadCredentialEffortCatalogs().then(()=>panel.querySelectorAll('[data-credential-catalog]').forEach(refreshCredentialCatalogEfforts));
  hydrateCredentialIcons(host);
  setTimeout(()=>panel.classList.remove('is-entering'),900);
  // Offer the OAuth shortcut only when the server has a Gongfeng application.
  api('/api/auth/gongfeng',{silent:true}).then(capability=>{state.gongfengOAuthEnabled=!!capability?.enabled;document.querySelectorAll('[data-gongfeng-oauth]').forEach(el=>{el.hidden=!state.gongfengOAuthEnabled})}).catch(()=>{});
  if(storedUser.gitHubCredentialConfigured){
    const cached=gitTokenVerifyFor(user);
    if(!cached||(cached.busy&&!state.gitTokenVerifyInFlight)){state.gitTokenVerifyInFlight=true;refreshGitTokenStatus().finally(()=>{state.gitTokenVerifyInFlight=false})}
  }
}
// 谁能改通用模板是Nền tảng级权限，不属于任何一个项目，也不属于个人凭据；
// 只有Quản trị viên nền tảng能看到「Nền tảng管理」入口。
function renderPlatformNav(){
  const group=$('#platformNavGroup');if(!group)return;
  group.hidden=!isPlatformAdmin();
}
const PLATFORM_TABS=[
  {id:'accounts',zh:'Tài khoản thành viên',en:'Accounts'},
  {id:'admins',zh:'Quản trị viên nền tảng',en:'Admins'},
  {id:'sharedKey',zh:'CodeBuddy Key dùng chung',en:'Shared CodeBuddy Key'},
  {id:'sshTrust',zh:'Tin cậy máy chủ Git SSH',en:'Git SSH Host Trust'},
];
function platformTabsHTML(active){
  return `<div class="pa-tabs-wrap platform-tabs-wrap"><div class="pa-tabs platform-tabs" id="platformTabs" role="tablist" aria-label="Khu vực quản trị nền tảng">${PLATFORM_TABS.map(t=>{
    const on=t.id===active;
    const extra=t.id==='sharedKey'?'<span class="platform-tab-state" data-platform-tab-state hidden></span>':'<span class="pa-tab-n" data-platform-tab-count hidden></span>';
    return `<button type="button" role="tab" class="pa-tab${on?' on':''}" id="platformTab-${t.id}" data-platform-tab="${t.id}" aria-controls="platformPanel-${t.id}" aria-selected="${on}" tabindex="${on?0:-1}">${lfT(t.zh,t.en,t.zh)}${extra}</button>`;
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
          <span class="platform-metric-label">Quản trị viên nền tảng</span>
          <b class="platform-metric-num" id="platformMetricTotal">--</b>
          <small>Có quyền sửa mẫu dùng chung và vai trò, áp dụng xuyên suốt các dự án</small>
        </div>
      </div>
      <div class="platform-metric-card">
        <div class="platform-metric-icon amber">
          <svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="11" width="18" height="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>
        </div>
        <div class="platform-metric-info">
          <span class="platform-metric-label">Có sẵn của hệ thống</span>
          <b class="platform-metric-num" id="platformMetricLocked">--</b>
          <small>Danh sách đặt sẵn ban đầu của hệ thống, không thể thu hồi</small>
        </div>
      </div>
      <div class="platform-metric-card">
        <div class="platform-metric-icon purple">
          <svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg>
        </div>
        <div class="platform-metric-info">
          <span class="platform-metric-label">Ủy quyền trên giao diện</span>
          <b class="platform-metric-num" id="platformMetricGranted">--</b>
          <small>Cấp trên bảng điều khiển, có thể thu hồi bất cứ lúc nào</small>
        </div>
      </div>
      <div class="platform-metric-card">
        <div class="platform-metric-icon green">
          <svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/></svg>
        </div>
        <div class="platform-metric-info">
          <span class="platform-metric-label">Phạm vi áp dụng</span>
          <b class="platform-metric-num">Toàn nền tảng</b>
          <small>Mẫu dùng chung · Vai trò dùng chung · Quy tắc cửa kiểm</small>
        </div>
      </div>
    </div>

    <div class="platform-admin-layout">
      <div class="platform-admin-main-card">
        <div class="platform-admin-head">
          <div class="platform-admin-head-meta">
            <h2>Ai được sửa mẫu dùng chung</h2>
            <p>Quản trị viên nền tảng có thể sửa trực tiếp mẫu dùng chung và các vai trò đi kèm; sau khi sửa, mọi dự án sẽ dùng bản mới ở lần chạy tiếp theo. Danh sách có sẵn của hệ thống không thể thu hồi tại đây.</p>
          </div>
          <span class="platform-admin-count-pill" id="platformAdminCountBadge">-- quản trị viên</span>
        </div>

        <div class="platform-add-box">
          <form id="platformAdminForm" class="platform-admin-form">
            <div class="platform-input-wrap">
              <span class="platform-input-icon">
                <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>
              </span>
              <input name="loginName" placeholder="Nhập tên đăng nhập, ví dụ alice" autocomplete="off">
            </div>
            <button class="button primary platform-add-btn" type="submit">Thêm quản trị viên</button>
          </form>
          <div class="platform-add-hint">
            <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
            <span>Sau khi ủy quyền, người dùng sẽ được sửa và xuất bản mẫu SOP dùng chung toàn nền tảng cùng vai trò công cộng; hãy cấp quyền thận trọng.</span>
          </div>
        </div>

        <div class="platform-list-toolbar">
          <div class="platform-search-wrap">
            <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></svg>
            <input id="platformAdminSearch" placeholder="Tìm theo tên hoặc tên đăng nhập..." autocomplete="off">
          </div>
          <div class="platform-filter-tabs" id="platformFilterTabs">
            <button type="button" class="platform-filter-btn active" data-source-filter="all">Tất cả</button>
            <button type="button" class="platform-filter-btn" data-source-filter="builtin">Có sẵn</button>
            <button type="button" class="platform-filter-btn" data-source-filter="granted">Đã ủy quyền</button>
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
            <h3>Quy tắc quyền và bảo mật</h3>
          </div>
          <ul class="platform-side-list">
            <li>
              <b>Sửa mẫu SOP dùng chung toàn cục</b>
              <p>Mẫu dùng chung được chia sẻ cho mọi dự án trên nền tảng, do quản trị viên nền tảng định nghĩa và duy trì; sau khi sửa, các dự án sẽ áp dụng ngay ở lần chạy tiếp theo.</p>
            </li>
            <li>
              <b>Phát hành vai trò công cộng và cửa kiểm dùng chung</b>
              <p>Vai trò Agent và cửa kiểm thực thi được mẫu dùng chung tham chiếu do nền tảng tinh chỉnh và nâng cấp thống nhất; trong dự án mặc định chỉ đọc để giữ chất lượng chuẩn.</p>
            </li>
            <li>
              <b>Bảo vệ danh sách cố định của hệ thống</b>
              <p>Danh sách có sẵn của hệ thống có lớp bảo vệ chống khóa chết mức cao nhất, không thể thu hồi trên trang, để việc quản trị sản xuất luôn liên tục.</p>
            </li>
          </ul>
        </div>

        <div class="platform-side-card">
          <div class="platform-side-head">
            <span class="platform-side-icon green">
              <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><polygon points="12 2 2 7 12 12 22 7 12 2"/><polyline points="2 17 12 22 22 17"/><polyline points="2 12 12 17 22 12"/></svg>
            </span>
            <h3>Quản lý mẫu dùng chung và tài sản</h3>
          </div>
          <p class="platform-side-desc">Sau khi quản trị viên nền tảng duy trì mẫu dùng chung trong thiết kế quy trình hoặc Định nghĩa vai trò, mọi dự án liên kết sẽ tự áp dụng phiên bản mới nhất ở lần chạy tiếp theo.</p>
          <div class="platform-side-quick-links">
            <button type="button" class="button ghost small platform-quick-link-btn" data-platform-nav="sops">Xem quy trình SOP ↗</button>
            <button type="button" class="button ghost small platform-quick-link-btn" data-platform-nav="agents">Xem định nghĩa vai trò ↗</button>
          </div>
          <div class="platform-audit-note">
            <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/></svg>
            <span>Việc cấp và thu hồi quyền quản trị có hiệu lực ngay; thay đổi sẽ được ghi vào nhật ký kiểm tra của nền tảng.</span>
          </div>
        </div>
      </div>
    </div>
    `)}
    ${platformTabPanel('sharedKey',tab,platformSharedKeyHTML())}
    ${platformTabPanel('sshTrust',tab,platformSSHHostTrustHTML())}
  `;
  bindPlatformTabs(host);
  loadPlatformAccounts(host);
  loadPlatformSharedKey(host);
  loadPlatformSSHHosts(host);
  loadPlatformAdmins(host);
}

// Nền tảng管理 → Tài khoản thành viên / Nền tảngCodeBuddy Key dùng chung.
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
          <h2>Mở tài khoản cho đồng nghiệp</h2>
          <p data-account-intro>Điền tên đăng nhập và mật khẩu ban đầu, bấm tạo, rồi gửi thông tin đăng nhập cho đồng nghiệp. Nếu họ quên mật khẩu, hãy đặt lại bên dưới.</p>
        </div>
        <span class="platform-admin-count-pill" id="platformAccountCountBadge">${lfT('-- tài khoản','-- accounts','-- tài khoản')}</span>
      </div>
      <div class="platform-account-create">
        <form id="platformAccountForm" class="platform-account-form" autocomplete="off">
          <div class="platform-input-wrap"><span class="platform-input-icon">${PLATFORM_ICON_USER}</span><input name="loginName" placeholder="Tên đăng nhập, ví dụ alice" aria-label="Tên đăng nhập" maxlength="64" autocomplete="off" spellcheck="false"></div>
          <div class="platform-input-wrap"><span class="platform-input-icon">${PLATFORM_ICON_LOCK}</span><input name="password" placeholder="Mật khẩu ban đầu, ít nhất 6 ký tự" aria-label="Mật khẩu ban đầu" maxlength="72" autocomplete="new-password" spellcheck="false"><button type="button" class="button ghost small" data-account-generate>Tạo ngẫu nhiên</button></div>
          <label class="platform-check"><input type="checkbox" name="admin"><span>Đồng thời đặt làm quản trị viên nền tảng</span></label>
          <button class="button primary platform-add-btn" type="submit">Tạo tài khoản</button>
        </form>
        <div class="platform-account-created" data-account-created hidden></div>
      </div>
      <div class="platform-list-toolbar">
        <div class="platform-search-wrap platform-account-search">${PLATFORM_ICON_SEARCH}<input data-account-search placeholder="Tìm tên đăng nhập..." autocomplete="off"></div>
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
          <h2>Dùng cho thành viên chưa có Key</h2>
          <p>Khi thành viên chưa có CodeBuddy API Key riêng, hội thoại và SOP sẽ dùng Key ở đây và chỉ được dùng các model đã chọn bên dưới. Thành viên đã có Key riêng sẽ dùng Key của mình, không bị giới hạn.</p>
        </div>
        <span class="platform-admin-count-pill platform-status-pill" data-shared-key-badge>${lfT('Đang tải','Loading','Đang tải')}</span>
      </div>
      <form id="platformSharedKeyForm" class="platform-shared-key-form" autocomplete="off">
        <div class="platform-shared-key-grid">
          <div class="platform-shared-key-col">
            <label class="platform-field"><span>Môi trường mạng</span><select name="internetEnvironment">${envOptions}</select></label>
            <div class="platform-field-group">
              <label class="platform-field"><span>API Key</span><input name="apiKey" type="password" autocomplete="new-password" spellcheck="false" placeholder="CodeBuddy API Key bắt đầu bằng ck_"></label>
              <a class="platform-shared-key-link" data-shared-key-link target="_blank" rel="noopener noreferrer"></a>
            </div>
            <div class="platform-note is-warn" data-shared-key-warning hidden></div>
            <label class="platform-field"><span>Model mặc định</span><select name="defaultModel"></select><small class="platform-field-hint">Dùng model này khi chưa chỉ định model; nếu model của vai trò SOP không nằm trong danh sách cho phép cũng chuyển sang model này.</small></label>
          </div>
          <div class="platform-shared-key-col">
            <div class="platform-field">
              <div class="platform-field-head"><span>Model được phép dùng</span><small data-shared-models-count></small></div>
              <div class="platform-model-list" data-shared-models><p class="prop-hint">Đang đọc danh sách model khả dụng…</p></div>
            </div>
            <label class="platform-field"><span>Model không có trong danh sách</span><input name="extraModels" placeholder="Model ID, nhiều ID thì cách nhau bằng dấu phẩy" autocomplete="off" spellcheck="false"></label>
          </div>
        </div>
        <div class="platform-shared-key-footer">
          <p class="platform-add-hint" data-shared-key-meta></p>
          <div class="platform-shared-key-actions">
            <button class="button ghost danger" type="button" data-shared-key-clear hidden>Tắt Key dùng chung</button>
            <button class="button primary" type="submit">Lưu</button>
          </div>
        </div>
      </form>
    </section>`;
}
function platformAccountCreatedHTML(action,login,password){
  const who=`<b data-i18n-skip>${escapeHTML(login)}</b>`,secret=`<code>${escapeHTML(password)}</code>`;
  const text=action==='reset'
    ?lfT(`Đã đặt lại tài khoản ${who}, mật khẩu mới ${secret}. Mật khẩu chỉ hiện lần này, hãy gửi ngay cho đối phương.`,`Password of ${who} reset to ${secret}. It is shown only once; send it now.`,`Đã đặt lại tài khoản ${who}, mật khẩu mới ${secret}. Mật khẩu chỉ hiện lần này, hãy gửi ngay cho đối phương.`)
    :lfT(`Đã tạo tài khoản ${who}, mật khẩu ban đầu ${secret}. Mật khẩu chỉ hiện lần này, hãy gửi ngay cho đối phương.`,`Created ${who} with the initial password ${secret}. It is shown only once; send it now.`,`Đã tạo tài khoản ${who}, mật khẩu ban đầu ${secret}. Mật khẩu chỉ hiện lần này, hãy gửi ngay cho đối phương.`);
  return `<div class="platform-note is-ok"><span>${text}</span><button type="button" class="button ghost small" data-copy-login>Sao chép thông tin đăng nhập</button></div>`;
}
const platformPasswordTooShort=()=>lfT(`Mật khẩu cần ít nhất ${PLATFORM_ACCOUNT_PASSWORD_MIN} ký tự`,`The password needs at least ${PLATFORM_ACCOUNT_PASSWORD_MIN} characters`,`Mật khẩu cần ít nhất ${PLATFORM_ACCOUNT_PASSWORD_MIN} ký tự`);
async function loadPlatformAccounts(host){
  const card=host.querySelector('#platformAccountsCard');if(!card)return;
  const list=card.querySelector('#platformAccountList'),form=card.querySelector('#platformAccountForm'),created=card.querySelector('[data-account-created]'),search=card.querySelector('[data-account-search]');
  let accounts=[],query='',resetting=null;
  const showCreated=(action,login,password)=>{
    created.innerHTML=platformAccountCreatedHTML(action,login,password);created.hidden=false;
    created.querySelector('[data-copy-login]').onclick=()=>copyText(lfT(`Địa chỉ đăng nhập: ${location.origin}\nTên đăng nhập: ${login}\nMật khẩu: ${password}`,`Sign in at: ${location.origin}\nLogin: ${login}\nPassword: ${password}`,`Địa chỉ đăng nhập: ${location.origin}\nTên đăng nhập: ${login}\nMật khẩu: ${password}`),'Đã sao chép thông tin đăng nhập');
  };
  const render=()=>{
    const n=accounts.length,badge=card.querySelector('#platformAccountCountBadge');if(badge)badge.textContent=lfT(`${n} tài khoản`,`${n} account${n===1?'':'s'}`,`${n} tài khoản`);
    setPlatformTabCount(host,'accounts',n);
    const q=query.toLowerCase();
    const rows=accounts.filter(a=>!q||String(a.loginName||'').toLowerCase().includes(q)||String(a.chineseName||'').toLowerCase().includes(q));
    if(!rows.length){list.innerHTML=`<div class="platform-empty-state"><b>${accounts.length?'Không có tài khoản khớp':'Chưa có tài khoản nào'}</b></div>`;return}
    list.innerHTML=rows.map(a=>{
      const name=a.chineseName&&a.chineseName.toLowerCase()!==String(a.loginName).toLowerCase()?`${a.loginName}(${a.chineseName})`:a.loginName;
      const badges=[a.admin?'<span class="platform-source-badge source-builtin">Quản trị viên nền tảng</span>':'',a.password?'<span class="platform-source-badge source-default">Tên đăng nhập và mật khẩu</span>':'<span class="platform-source-badge source-default">Đăng nhập GitHub / cổng</span>',a.codeBuddyApiKeyConfigured?'<span class="platform-source-badge source-env">Đã có CodeBuddy Key</span>':'<span class="platform-source-badge source-default">Chưa có CodeBuddy Key</span>'].join('');
      const editing=resetting===a.userId;
      return `<div class="platform-admin-row platform-account-row" data-account-row="${Number(a.userId)}">
        <div class="platform-admin-user">
          <div class="platform-admin-avatar">${userAvatarHTML(a.loginName,100)}</div>
          <div class="platform-admin-info"><span class="platform-admin-name" data-i18n-skip>${escapeHTML(name)}</span><div class="platform-admin-meta">${badges}</div></div>
        </div>
        <div class="platform-admin-actions">${a.password?(editing?`<form class="platform-account-reset" data-reset-form="${Number(a.userId)}"><input name="password" placeholder="Mật khẩu mới, ít nhất 6 ký tự" aria-label="Mật khẩu mới" maxlength="72" autocomplete="new-password" spellcheck="false"><button type="button" class="button ghost small" data-reset-generate>Ngẫu nhiên</button><button type="submit" class="button primary small">Xác nhận đặt lại</button><button type="button" class="button ghost small" data-reset-cancel>Hủy</button></form>`:`<button type="button" class="button ghost small" data-reset-account="${Number(a.userId)}">Đặt lại mật khẩu</button>`):'<span class="platform-locked-tag" title="Tài khoản đăng nhập bằng GitHub hoặc cổng không có mật khẩu nền tảng">Không có mật khẩu nền tảng</span>'}</div>
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
          resetting=null;render();showCreated('reset',account?.loginName||'',password);toast('Đã đặt lại mật khẩu','ok');
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
        ?'Trang đăng nhập hiện cho phép tự đăng ký. Bạn cũng có thể tạo tài khoản tại đây rồi gửi thông tin đăng nhập cho đồng nghiệp; nếu họ quên mật khẩu, hãy đặt lại bên dưới.'
        :'Tự đăng ký trên trang đăng nhập đã tắt, tài khoản của đồng nghiệp cần được tạo tại đây: điền tên đăng nhập và mật khẩu ban đầu, bấm tạo, rồi gửi thông tin đăng nhập cho họ. Nếu quên mật khẩu, hãy đặt lại bên dưới.';
      render();
    }catch(e){list.innerHTML=`<p class="prop-hint">${escapeHTML(e.message||'Không tải được danh sách tài khoản')}</p>`}
  };
  if(search)search.oninput=()=>{query=search.value.trim();render()};
  form.querySelector('[data-account-generate]').onclick=()=>{form.elements.password.value=platformRandomPassword()};
  form.onsubmit=async e=>{
    e.preventDefault();
    const loginName=form.elements.loginName.value.trim(),password=form.elements.password.value,admin=form.elements.admin.checked;
    if(!/^[A-Za-z0-9][A-Za-z0-9_.-]{1,63}$/.test(loginName))return toast('Tên đăng nhập dài 2–64 ký tự, bắt đầu bằng chữ hoặc số, chỉ gồm chữ, số, _ . -','err');
    if(password.length<PLATFORM_ACCOUNT_PASSWORD_MIN)return toast(platformPasswordTooShort(),'err');
    const submit=form.querySelector('button[type="submit"]');if(submit)submit.disabled=true;
    try{
      const account=await api('/api/platform/accounts',{method:'POST',body:JSON.stringify({loginName,password,admin})});
      form.reset();
      accounts=[...accounts.filter(a=>a.userId!==account?.userId),account].filter(Boolean);
      render();showCreated('create',loginName,password);toast('Đã tạo tài khoản','ok');
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
    if(modelCount)modelCount.textContent=models.length?lfT(`Đã chọn ${models.length}`,`${models.length} selected`,`Đã chọn ${models.length}`):'';
    defaultSelect.innerHTML=models.length?models.map(id=>`<option value="${escapeHTML(id)}">${escapeHTML(sharedModelLabel(id))}</option>`).join(''):'<option value="">Hãy chọn model trước</option>';
    defaultSelect.value=models.includes(current)?current:(models[0]||'');
  };
  const renderModels=()=>{
    const listed=catalog.filter(m=>m&&m.id&&m.id!=='default');
    const ids=new Set(listed.map(m=>m.id));
    for(const id of selected)if(!ids.has(id)){listed.push({id,name:id});ids.add(id)}
    modelsHost.innerHTML=listed.length?listed.map(m=>`<label class="platform-check platform-model-option"><input type="checkbox" value="${escapeHTML(m.id)}"${selected.includes(m.id)?' checked':''}><span>${escapeHTML(m.name&&m.name!==m.id?`${m.name}（${m.id}）`:sharedModelLabel(m.id))}</span>${m.credits?`<small>${escapeHTML(m.credits)}</small>`:''}</label>`).join(''):'<p class="prop-hint">Môi trường mạng này tạm thời chưa đọc được danh sách model, bạn có thể điền Model ID bên dưới.</p>';
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
    badge.textContent=on?'Đã bật':status?.legacySponsor?'Dùng cấu hình cũ':'Chưa bật';
    badge.classList.remove('is-on','is-legacy','is-off');badge.classList.add(`is-${tone}`);
    if(tabState){tabState.className=`platform-tab-state is-${tone}`;tabState.title=badge.textContent;tabState.hidden=false}
    key.placeholder=on?'Đã cấu hình, để trống nếu giữ nguyên':'CodeBuddy API Key bắt đầu bằng ck_';
    clear.hidden=!on;
    const parts=[];
    if(on){
      const n=(status.models||[]).length,first=sharedModelLabel(status.models?.[0]),by=status.updatedBy||'';
      parts.push(lfT(`Hiện tại: ${envInfo.label}, ${n} model, mặc định ${first}${by?`, ${by} sửa lần cuối`:''}.`,`Current: ${envInfo.label}, ${n} model${n===1?'':'s'}, default ${first}${by?`, last changed by ${by}`:''}.`));
    }
    else parts.push(lfT('Chưa cấu hình Key dùng chung; thành viên chưa có Key riêng cần tự cấu hình trước khi trò chuyện và chạy SOP.','No shared key yet; members without their own key must add one before chatting or running SOPs.','Chưa cấu hình Key dùng chung; thành viên chưa có Key riêng cần tự cấu hình trước khi trò chuyện và chạy SOP.'));
    const sponsor=status?.legacySponsor;
    if(sponsor)parts.push(on
      ?lfT(`Sau khi cấu hình, sẽ không còn dùng Key của tài khoản ${sponsor} được chỉ định bởi biến môi trường AGENTFLOW_ASSISTANT_SPONSOR_LOGIN.`,`The key of ${sponsor} (AGENTFLOW_ASSISTANT_SPONSOR_LOGIN) is no longer used.`,`Sau khi cấu hình, sẽ không còn dùng Key của tài khoản ${sponsor} được chỉ định bởi biến môi trường AGENTFLOW_ASSISTANT_SPONSOR_LOGIN.`)
      :lfT(`Hiện vẫn dùng Key của tài khoản ${sponsor} được chỉ định bởi biến môi trường AGENTFLOW_ASSISTANT_SPONSOR_LOGIN, chỉ được dùng ${sharedModelLabel(status.legacySponsorModel)}.`,`For now the key of ${sponsor} (AGENTFLOW_ASSISTANT_SPONSOR_LOGIN) is shared, limited to ${sharedModelLabel(status.legacySponsorModel)}.`,`Hiện vẫn dùng Key của tài khoản ${sponsor} được chỉ định bởi biến môi trường AGENTFLOW_ASSISTANT_SPONSOR_LOGIN, chỉ được dùng ${sharedModelLabel(status.legacySponsorModel)}.`));
    meta.textContent=parts.join(' ');
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
    badge.textContent='Không tải được';modelsHost.innerHTML=`<p class="prop-hint">${escapeHTML(e.message||'Không tải được Key dùng chung')}</p>`;
    return;
  }
  env.onchange=async()=>{
    renderLink();
    modelsHost.innerHTML='<p class="prop-hint">Đang đọc danh sách model khả dụng…</p>';
    try{await fetchStatus(env.value)}catch(e){catalog=[]}
    renderModels();
  };
  extra.oninput=renderDefault;
  key.oninput=()=>{const text=credentialSecretWarning(credentialKind('codeBuddyApiKey'),key.value);warning.textContent=text;warning.hidden=!text};
  form.onsubmit=async e=>{
    e.preventDefault();
    const models=chosen();
    if(!models.length)return toast('Hãy chọn ít nhất một model được phép dùng','err');
    if(!status?.configured&&!key.value.trim())return toast('Hãy điền CodeBuddy API Key','err');
    const first=defaultSelect.value&&models.includes(defaultSelect.value)?defaultSelect.value:models[0];
    const ordered=[first,...models.filter(id=>id!==first)];
    const submit=form.querySelector('button[type="submit"]');if(submit)submit.disabled=true;
    try{
      status=await api('/api/platform/codebuddy-key',{method:'PUT',body:JSON.stringify({apiKey:key.value.trim(),internetEnvironment:env.value,models:ordered})});
      key.value='';warning.hidden=true;extra.value='';selected=[...(status.models||ordered)];
      renderStatus();renderModels();syncBootstrap();toast('Đã lưu Key dùng chung','ok');
    }catch(err){toast(err.message,'err')}
    finally{if(submit)submit.disabled=false}
  };
  clear.onclick=async()=>{
    if(!await afConfirm({title:'Tắt Key dùng chung',message:'Sau khi tắt, thành viên chưa có Key riêng sẽ không dùng được Key dùng chung để trò chuyện và chạy SOP. Bạn chắc chắn muốn tắt?',danger:true,confirmText:'Tắt'}))return;
    try{status=await api('/api/platform/codebuddy-key',{method:'DELETE'});renderStatus();syncBootstrap();toast('Đã tắt Key dùng chung','ok')}
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
    builtin:{label:'Có sẵn',className:'source-builtin',icon:'<svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/></svg>'},
    granted:{label:'Đã ủy quyền',className:'source-granted',icon:'<svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2"><path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>'}
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
    if(elBadge)elBadge.textContent=`${total} quản trị viên`;
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
          <b>Không tìm thấy quản trị viên phù hợp</b>
          <p>${(allRows&&allRows.length)?'Hãy thử đổi từ khóa tìm kiếm hoặc đặt lại bộ lọc.':'Chưa cấu hình quản trị viên nền tảng.'}</p>
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
              <button type="button" class="button ghost small platform-revoke-btn" data-revoke-admin="${escapeHTML(row.loginName)}" title="Thu hồi quyền quản trị viên nền tảng của tài khoản này">
                <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
                <span>Thu hồi</span>
              </button>
            `:`
              <span class="platform-locked-tag" title="Danh sách có sẵn của hệ thống không thể thu hồi trên giao diện, để đảm bảo quản trị nền tảng luôn liên tục">
                <svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="11" width="18" height="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>
                <span>Không thể thu hồi</span>
              </span>
            `}
          </div>
        </div>
      `;
    }).join('');

    list.querySelectorAll('[data-revoke-admin]').forEach(btn=>btn.onclick=async()=>{
      const target=btn.dataset.revokeAdmin;
      if(!await afConfirm({
        title:'Thu hồi quản trị viên nền tảng',
        message:`Thu hồi quyền quản trị nền tảng của ${target}? Sau khi thu hồi, tài khoản này sẽ không còn chỉnh được mẫu dùng chung và vai trò.`,
        danger:true,
        confirmText:'Thu hồi quyền'
      }))return;
      try{
        const rows=await api('/api/platform/admins',{method:'PUT',body:JSON.stringify({loginName:target,role:''})});
        allRows=rows||[];
        updateMetrics(allRows);
        render();
        toast('Đã thu hồi quyền quản trị','ok');
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
    list.innerHTML=`<p class="prop-hint">${escapeHTML(e.message||'Không tải được danh sách quản trị viên')}</p>`;
  }

  form.onsubmit=async e=>{
    e.preventDefault();
    const login=form.elements.loginName.value.trim();
    if(!login)return toast('Hãy điền tên đăng nhập');
    const submitBtn=form.querySelector('button[type="submit"]');
    if(submitBtn)submitBtn.disabled=true;
    try{
      const rows=await api('/api/platform/admins',{method:'PUT',body:JSON.stringify({loginName:login,role:'admin'})});
      form.reset();
      allRows=rows||[];
      updateMetrics(allRows);
      render();
      toast('Đã thêm quản trị viên nền tảng','ok');
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
  const displayName = user?.chineseName || user?.loginName || 'Khách';
  const initial = (displayName[0] || 'K').toUpperCase();
  let authErr = null;
  try { authErr = sessionStorage.getItem('agentflow_auth_err'); } catch (_) {}

  if (isGuest) {
    popover.innerHTML = `
      <div class="upp-header">
        <div class="upp-avatar-lg is-guest">${escapeHTML(initial)}</div>
        <div class="upp-user-meta">
          <div class="upp-name-row">
            <span class="upp-name">Khách</span>
            <span class="upp-badge-tag is-guest">Chưa xác thực</span>
          </div>
          <div class="upp-login-sub">Guest Session</div>
        </div>
      </div>
      <div class="upp-divider"></div>
      <div class="upp-guest-body">
        ${authErr ? `
          <div class="upp-alert-warn">
            <b>⚠️ Đổi vé xác thực chưa thành công</b><br>Vé xác thực thống nhất trước đó đã hết hiệu lực, hãy bấm nút bên dưới để đăng nhập lại.
          </div>
        ` : `
          <p class="upp-guest-desc">Bạn đang truy cập nền tảng với tư cách khách. Sau khi đăng nhập, có thể cấu hình GitHub Git Token và CodeBuddy API Key trong「Cài đặt cá nhân」ở bên trái.</p>
        `}
      </div>
      <div class="upp-divider"></div>
      ${themeSegmentHTML()}
      <div class="upp-footer">
        <button id="uppLoginBtn" class="button small primary upp-full-btn" type="button">Đăng nhập bằng tài khoản</button>
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
            <span class="upp-badge-tag">Đã đăng nhập</span>
          </div>
          <div class="upp-login-sub">${escapeHTML(user.loginName || '')}</div>
        </div>
      </div>
      <div class="upp-divider"></div>
      <div class="upp-info-list">
        <div class="upp-info-item">
          <span class="upp-label">Tên hiển thị</span>
          <span class="upp-value">${escapeHTML(user.chineseName || displayName)}</span>
        </div>
        <div class="upp-info-item">
          <span class="upp-label">Tài khoản đăng nhập</span>
          <span class="upp-value">${escapeHTML(user.loginName || '-')}</span>
        </div>
        <div class="upp-info-item">
          <span class="upp-label">ID người dùng</span>
          <span class="upp-value">${escapeHTML(user.id ? String(user.id) : '-')}</span>
        </div>
        <div class="upp-info-item">
          <span class="upp-label">Phòng ban</span>
          <span class="upp-value" title="${escapeHTML(user.deptName || '-')}">${escapeHTML(user.deptName || '-')}</span>
        </div>
        <div class="upp-info-item">
          <span class="upp-label">Cách xác thực</span>
          <span class="upp-value">Tài khoản cục bộ LoopForge</span>
        </div>
      </div>
      <div class="upp-divider"></div>
      ${themeSegmentHTML()}
      <div class="upp-footer">
        <button id="uppLogoutBtn" class="button small ghost upp-btn-danger upp-full-btn" type="button" title="Đăng xuất phiên hiện tại">Đăng xuất</button>
      </div>
    `;
    const logoutBtn = popover.querySelector('#uppLogoutBtn');
    if (logoutBtn) {
      logoutBtn.onclick = async () => {
        try {
          await api('/api/auth/logout', {method: 'POST'});
          location.replace('/login.html');
        } catch (err) {
          toast('Đăng xuất thất bại: ' + err.message, 'err');
        }
      };
    }
  }
  bindThemeSegment(popover);
}

const THEME_SEGMENT_OPTIONS = [['light', 'sun', 'Sáng'], ['dark', 'moon', 'Tối'], ['system', 'monitor', 'Theo hệ thống']];
function themeSegmentHTML() {
  return `
      <div class="upp-theme">
        <span class="upp-label" id="uppThemeLabel">Giao diện</span>
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
  const initial = (user?.chineseName || user?.loginName || 'U')[0]?.toUpperCase() || 'K';
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
  const displayName=user?.chineseName||user?.loginName||'Khách';
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
    ? (authErr ? 'Đổi vé xác thực chưa thành công · bấm để xem hồ sơ' : 'Hiện đang là khách · bấm để xem hồ sơ')
    : `${displayName} (${user.loginName||user.staffId||''}) · bấm để xem hồ sơ`;
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
    {label:'Cần can thiệp',value:attention.length,foot:'Chờ xác nhận kết luận, thất bại hoặc đã dừng',color:'#c63c4b',filter:'attention'},
    {label:'Chờ tôi xác nhận',value:approvalItems().length,foot:'Chỉ tiếp tục sau khi được phê duyệt',color:'#fbbf24',to:'approvals'},
    {label:'Đang triển khai',value:active.length,foot:'Agent đang chạy',color:'#5b8cff',filter:'active'},
    {label:'Đã hoàn thành',value:closed.length,foot:'Dự án này đã khép lại',color:'#34d399',filter:'closed'},
  ];
  const statsHost=$('#dashStats');
  statsHost.innerHTML=statCards.map(c=>`<div class="stat-card" ${c.filter!=null?`data-issue-filter="${c.filter}"`:`data-jump="${c.to}"`} style="--accent:${c.color}"><div class="label">${c.label}</div><div class="value">${c.value}</div><div class="foot">${c.foot}</div></div>`).join('');
  jumpBind(statsHost);jumpBind($('#dashboardView'));
  const row=(i)=>{
    const p=(state.data.projects||[]).find(x=>x.id===i.projectId),out=issueOutcome(i),stage=issueCurrentStage(i);
    return `<div class="row-item ${out.key}" data-issue="${escapeHTML(i.id)}"><span class="dot ${out.key}"></span><div style="flex:1;min-width:0"><div class="strong ellipsis">${escapeHTML(i.title)}</div><div class="muted">${escapeHTML(issueKeyLabel(i))} · ${escapeHTML(stage.label)}</div></div><span class="status ${out.key}">${escapeHTML(out.short)}</span></div>`;
  };
  const bindIssueRows=host=>{host.querySelectorAll('[data-issue]').forEach(el=>el.onclick=()=>openIssueDetail(el.dataset.issue))};
  $('#dashAttention').innerHTML=needMe.length?needMe.slice(0,8).map(row).join(''):'<div class="empty-box">Không có yêu cầu nào cần bạn xử lý</div>';
  $('#dashActive').innerHTML=active.length?active.slice(0,8).map(row).join(''):'<div class="empty-box">Hiện không có yêu cầu đang triển khai</div>';
  const results=closed.filter(i=>!issueManuallyClosed(i)&&!issueTrackerClosed(i)).sort((a,b)=>new Date(b.updatedAt||0)-new Date(a.updatedAt||0)).slice(0,8);
  $('#dashResults').innerHTML=results.length?results.map(row).join(''):'<div class="empty-box">Chưa có yêu cầu nào chạy xong</div>';
  bindIssueRows($('#dashAttention'));bindIssueRows($('#dashActive'));bindIssueRows($('#dashResults'));
  const cfg=state.data.syncConfig||{},logs=state.data.syncLogs||[];
  const synced=knownTime(cfg.lastSyncAt),syncResult=logs[0]?.status==='success'?'Thành công':logs[0]?.status==='failed'?'Thất bại':'';
  $('#dashFeedHint').textContent=synced?`Đồng bộ lần cuối ${synced.toLocaleString()}${syncResult?` · ${syncResult}`:''}`:'Chưa từng đồng bộ';
  const feed=logs.slice(0,8).map(l=>`<div class="tl-item"><div><b>${l.direction==='pull'?'Kéo':'Đẩy'} · ${l.source==='github'?'GitHub':l.source==='gongfeng'?'Gongfeng':l.source==='tapd'?'TAPD':escapeHTML(l.source||'')}</b> <span class="muted">${escapeHTML(l.message||l.action)}</span></div><div class="muted" style="font-size:10.5px;margin-top:2px">${new Date(l.startedAt).toLocaleString()} · ${l.status==='success'?'Thành công':`Thất bại ${escapeHTML(l.message||'')}`}</div></div>`).join('');
  $('#dashFeed').innerHTML=feed||'<div class="tl-item"><div class="muted">Chưa đồng bộ. Sau khi gắn repository GitHub, hãy bấm「Đồng bộ GitHub」trong「Danh sách yêu cầu」.</div></div>';
}
