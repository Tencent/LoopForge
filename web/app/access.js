function accessGroup(op){
  const name=op.name||'';
  if(name.startsWith('issues_')||name.startsWith('tasks_')||name.startsWith('clarification_'))return '需求与任务';
  if(name.startsWith('runs_'))return '运行与审批';
  if(name.startsWith('workflows_')||name.startsWith('workflow_')||name.startsWith('agents_'))return 'SOP 与角色';
  if(name.startsWith('sync_')||name.startsWith('schedule_'))return '外部同步与定时';
  if(name.startsWith('analytics_')||name.startsWith('observability_')||name.startsWith('deployment_'))return '分析与观测';
  if(name.startsWith('projects_')||name.startsWith('repositories_'))return '项目与仓库';
  return '平台与环境资产';
}
function accessCode(title,text){
  return `<div class="access-code"><div class="access-code-head"><b>${escapeHTML(title)}</b><button type="button" class="access-copy" data-copy-access="${encodeURIComponent(text)}">⧉ 复制</button></div><pre>${escapeHTML(text)}</pre></div>`;
}
function accessMcpClientSpec(client,base,token){
  const json=JSON.stringify({mcpServers:{agentflow:{type:'streamable-http',url:`${base}/mcp`,headers:{Authorization:`Bearer ${token}`}}}},null,2);
  const specs={
    cursor:{label:'Cursor',location:'~/.cursor/mcp.json',codeTitle:'mcp.json',code:json,verify:'在 Cursor 设置的 MCP 列表中确认 agentflow 已连接'},
    claude:{label:'Claude Code',location:'Claude CLI（用户级配置写入 ~/.claude.json）',codeTitle:'Shell',code:`claude mcp add --transport http --scope user agentflow ${base}/mcp \\\n  --header "Authorization: Bearer ${token}"`,verify:'claude mcp get agentflow && claude mcp list'},
    codebuddy:{label:'CodeBuddy',location:'~/.codebuddy/mcp.json',codeTitle:'mcp.json',code:json,verify:'在 CodeBuddy 的 MCP 管理页确认 agentflow 已连接'},
    codex:{label:'Codex',location:'~/.codex/config.toml',codeTitle:'config.toml',code:`[mcp_servers.agentflow]\nurl = "${base}/mcp"\nhttp_headers = { Authorization = "Bearer ${token}" }`,verify:'codex mcp list'}
  };
  return specs[client]||specs.cursor;
}
function accessMcpAgentPrompt(spec,base,token){
  if(!token||token==='<YOUR_PERSONAL_TOKEN>')return '';
  return `按照这个流程安装、初始化并验证好 AgentFlow MCP：\n\n1. 使用 curl 下载 安装流程文件, 禁止使用 fetch：${base}/mcp-setup.txt\n2. 两个占位符替换为：\n   API_TOKEN = ${token}\n   ENDPOINT = ${base}/mcp`;
}
function accessAgentSetup(prompt){
  const attr=prompt?`data-copy-agent-prompt="${encodeURIComponent(prompt)}"`:'data-create-token-first';
  return `<div class="access-agent-setup"><div><span class="access-recommended">推荐</span><b>${prompt?'提示词已就绪':'一键生成接入提示词'}</b><p>${prompt?'已包含本次创建的真实 Token，可直接粘贴给 Agent。':'自动创建专用 Token，并把真实 Token 和地址写进提示词。'}</p></div><button type="button" class="button dark" ${attr}>⧉ ${prompt?'复制提示词':'生成并复制'}</button></div>`;
}
async function createQuickAccessPrompt(button){
  const user=state.currentUser;
  if(!user||user.isGuest){if(state.loginUrl)location.href=state.loginUrl;else toast('请先登录后接入','err');return}
  const base=getPublicBaseURL();
  const spec=accessMcpClientSpec(state.accessClient,base,'<YOUR_PERSONAL_TOKEN>');
  const original=button.textContent;button.disabled=true;button.textContent='正在创建专用 Token…';
  try{
    const result=await api('/api/auth/tokens',{method:'POST',body:JSON.stringify({name:`MCP 快速接入 · ${spec.label}`,expiresInDays:90})});
    if(!result?.token)throw new Error('Token 创建成功但未返回明文，请到「我的 Token」重新创建');
    state.revealedToken=result.token;
    const prompt=accessMcpAgentPrompt(spec,base,result.token);
    await copyText(prompt,'接入提示词已复制，直接粘贴给 Agent 即可');
    loadAccessTokens();
    renderGuide();
  }catch(err){button.disabled=false;button.textContent=original;toast(err.message||'创建 Token 失败','err')}
}
// 对外展示的固定接入地址：devcloud 等环境经 https 访问不稳定（网关 443 链路问题），
// 接入配置统一展示并使用 http 域名，优先使用部署下发的 APP_DOMAIN，避免硬编码固定域名。
function getPublicBaseURL(){
  const domain = (typeof window !== 'undefined' && (window.__AGENTFLOW_APP_DOMAIN__ || window.__AGENTFLOW_PUBLIC_BASE_URL__))
    || (typeof state !== 'undefined' && state?.appDomain ? state.appDomain : '')
    || (typeof window !== 'undefined' && window.location?.host ? window.location.host : '')
    || 'loopforge.example.com';
  return 'http://' + domain;
}
const PUBLIC_BASE_URL = typeof window !== 'undefined' && (window.__AGENTFLOW_APP_DOMAIN__ || window.location?.host)
  ? ('http://' + (window.__AGENTFLOW_APP_DOMAIN__ || window.location.host))
  : 'http://loopforge.example.com';
function accessSummary(){
  const base=getPublicBaseURL(),user=state.currentUser,isGuest=!user||user.isGuest,count=state.accessCapabilities?.operations?.length||'—';
  return `<div class="access-summary"><div class="access-instance"><span>API</span><div><small>当前实例</small><b>${escapeHTML(base)}</b><p>REST API 与 MCP 共用当前地址，使用同一个个人 Token</p></div></div><div class="access-facts"><div class="access-fact"><span>当前身份</span><b>${escapeHTML(isGuest?'未登录':(user.chineseName||user.loginName))}</b></div><div class="access-fact"><span>可用操作</span><b>${count} 个</b></div><div class="access-fact"><span>鉴权</span><b>${state.authRequired?'Bearer / 登录 Cookie':'兼容开放模式'}</b></div><div class="access-fact good"><span>服务状态</span><b>● 正常</b></div></div></div>`;
}
const ACCESS_TABS=['setup','mcp','tokens','capabilities','api'];
function setAccessTab(tab,openCreate=false){
  state.accessTab=ACCESS_TABS.includes(tab)?tab:'setup';
  try{localStorage.setItem('agentflow.accessTab',state.accessTab)}catch(e){}
  renderGuide(openCreate);
}
function accessViewActive(){return !!$('#guideView')?.classList.contains('active')}
function loadAccessCapabilities(){
  if(state.accessCapabilitiesStatus==='loading')return;
  state.accessCapabilitiesStatus='loading';state.accessCapabilitiesError='';
  api('/api/capabilities',{silent:true})
    .then(v=>{state.accessCapabilities=v;state.accessCapabilitiesStatus='ready'})
    .catch(e=>{state.accessCapabilities=null;state.accessCapabilitiesStatus='error';state.accessCapabilitiesError=e.message||'能力清单加载失败'})
    .finally(()=>{if(accessViewActive())renderGuide()});
}
function renderAccessGuide(){
  const body=$('#accessBody'),base=getPublicBaseURL(),token=state.revealedToken||'<YOUR_PERSONAL_TOKEN>';
  const mainCode=`export AGENTFLOW_URL=${base}\nexport AGENTFLOW_TOKEN='${token}'\n\ncurl -sS -H \"Authorization: Bearer $AGENTFLOW_TOKEN\" \\\n  \"$AGENTFLOW_URL/api/issues\"`;
  const checkCode=`curl -sS -H \"Authorization: Bearer $AGENTFLOW_TOKEN\" \\\n  \"$AGENTFLOW_URL/api/health\"`;
  body.innerHTML=`<div class=\"access-grid\"><div class=\"access-card\"><div class=\"access-card-title\"><i>API</i>通过 REST API 调用 AgentFlow</div><div class=\"access-step\"><span>1</span><b>设置地址和个人 Token</b></div>${accessCode('Shell',mainCode)}<div class=\"access-step\"><span>2</span><b>验证连通</b><small>返回 JSON 即接入成功</small></div>${accessCode('连通性自检',checkCode)}<div class=\"access-note\"><b>Token 不要提交到代码仓库。</b>建议通过 CI Secret 或环境变量保存。</div></div><div class=\"access-card\"><div class=\"access-card-title\"><i>↗</i>接口契约</div><div class=\"access-note\" style=\"margin-top:0\">REST API 与 MCP 使用同一个个人 Token。完整请求结构以 OpenAPI 契约为准。</div><a class=\"button ghost small\" href=\"/api/openapi.yaml\" target=\"_blank\" rel=\"noopener\">打开 OpenAPI 契约 →</a></div></div>`;
  body.querySelectorAll('[data-copy-access]').forEach(b=>b.onclick=()=>copyText(decodeURIComponent(b.dataset.copyAccess),'配置已复制'));
}
// 最小接入配置：引导配好 CodeBuddy API Key 与工蜂 Git Token，配好即可运行。
function renderGuideSetup(){
  const body=$('#accessBody');if(!body)return;
  const user=state.currentUser,isGuest=!user||user.isGuest;
  // 仅当用户列表缺失时才拉取并重渲染一次；bootstrap 已带用户列表时绝不重复触发，
  // 否则 resolve().then(重渲染) 会形成无限微任务循环，冻住整个页面。
  if(!isGuest&&!(Array.isArray(state.data.users)&&state.data.users.length)){
    api('/api/users',{silent:true}).then(users=>{state.data.users=Array.isArray(users)?users:[];if(state.accessTab==='setup'&&activeViewName()==='guide')renderGuideSetup()}).catch(()=>{});
  }
  renderGuideSetupBody(body,user,isGuest);
}
function gotoCredential(name){
  switchView('settings');
  setTimeout(()=>{
    const el=document.querySelector(`#personalSettingsContent input[name="${name}"]`);
    if(!el)return;
    el.scrollIntoView({block:'center',behavior:'smooth'});
    const card=el.closest('fieldset');if(!card)return;
    card.classList.add('credential-highlight');
    setTimeout(()=>card.classList.remove('credential-highlight'),2400);
    el.focus({preventScroll:true});
  },150);
}
function renderGuideSetupBody(body,user,isGuest){
  if(isGuest){
    body.innerHTML=`<div class="quick-wrap"><div class="access-card access-tokens"><div class="token-empty"><b>请先登录</b><p>最小接入配置需要绑定到你的账号：执行器 API Key 和 GitHub Git Token 保存在个人设置里；也可以绑定本机 CLI。</p><button type="button" class="button primary" id="setupLogin">登录后配置</button></div></div></div>`;
    body.querySelector('#setupLogin').onclick=()=>{if(state.loginUrl)location.href=state.loginUrl};
    return;
  }
  const stored=userById(user.id)||{};
  const cbReady=!!stored.codeBuddyApiKeyConfigured,gfReady=!!stored.gitTokenConfigured,thReady=!!stored.taihuPatConfigured;
  const modelReady=cbReady||!!stored.codexApiKeyConfigured||!!stored.claudeCodeApiKeyConfigured||!!state.defaultRunnerId;
  const credReady=modelReady&&gfReady;
  // 第 2 步：就绪度按「用户所属项目」计算（负责人或成员包含自己）。
  // 只看平台项目列表会让未加入任何项目的新用户误显示"项目已就绪"。
  const projects=state.data.projects||[];
  const myProjects=projects.filter(p=>Number(p.ownerId)===Number(user.id)||(p.members||[]).some(m=>Number(m.userId)===Number(user.id)));
  const projReady=myProjects.length>0;
  // 第 3 步：顶栏选中的项目不属于自己时，SOP 就绪度按自己名下项目的 SOP 计算
  const selMine=myProjects.find(p=>p.id===state.projectFilter);
  const sopScope=selMine?[selMine]:myProjects;
  const mySops=(state.data.workflows||[]).filter(w=>!catalogGeneric(w)&&sopScope.some(p=>w.projectId===p.id));
  const sopReady=mySops.length>0;
  const allReady=credReady&&projReady&&sopReady;
  const credDone=[modelReady,gfReady].filter(Boolean).length;
  const badge=ready=>`<span class="setup-status ${ready?'ready':'missing'}">${ready?'✓ 已就绪':'未完成'}</span>`;
  const subItem=(name,label,ready,applyUrl,hint)=>`<div class="setup-sub ${ready?'is-ready':''}"><div class="setup-sub-info"><b>${label}</b>${hint?`<small>${hint}</small>`:''}</div>${ready?'':`<a class="setup-mini" href="${applyUrl}" target="_blank" rel="noopener noreferrer">申请 ↗</a>`}<button type="button" class="setup-mini" data-setup-cred="${name}">${ready?'查看':'去填写'} →</button></div>`;
  // 第 1 步：三张个人凭据一次性配齐
  const credCard=`<div class="setup-card ${credReady?'is-ready':''}"><div class="setup-card-head"><b>① 配个人凭据</b><small>个人设置 · 一次配齐</small>${badge(credReady)}</div>
    <p>${lfT('在<b>个人设置</b>中配置 CodeBuddy API Key 与 GitHub Token，或绑定本机 CLI。Main Bot 和内置 SOP 默认都使用 CodeBuddy。','Configure your CodeBuddy API key and GitHub token in <b>Settings</b>, or connect a local CLI. Main Bot and the built-in SOPs use CodeBuddy by default.')}</p>
    <div class="setup-steps">
      ${subItem('codeBuddyApiKey','CodeBuddy API Key',cbReady,'https://www.codebuddy.ai/','Agent 调模型执行任务')}
      ${subItem('codexApiKey','Codex API Key',!!stored.codexApiKeyConfigured,'https://platform.openai.com/api-keys','公共 SOP 默认执行器')}
      ${subItem('claudeCodeApiKey','Claude Code API Key',!!stored.claudeCodeApiKeyConfigured,'https://console.anthropic.com/','可按角色选择的执行器')}
      ${subItem('gitToken','GitHub Git Token',gfReady,'https://github.com/settings/tokens','目标仓库需要 Contents 和 Pull requests 读写权限')}
    </div>
    <button type="button" class="button primary small" data-setup-cred="codeBuddyApiKey" style="margin-top:12px">打开个人设置 →</button></div>`;
  // 第 2 步：加入已有项目或新建项目
  const projDesc=!projects.length
    ?'平台还没有项目：点「新建项目」，填标识 / Key / 名称，粘贴 GitHub 仓库地址，默认 SOP 保持「通用后端交付 SOP」，保存。'
    :!projReady
      ?`平台已有 <b>${projects.length}</b> 个项目，但你还不是任何项目的成员：让项目负责人在这一页把你加成成员，加入后回到本页会自动亮起。`
      :`已加入 <b>${myProjects.length}</b> 个项目；需求、角色和运行都归属到顶栏选中的项目。`;
  const projCard=`<div class="setup-card ${projReady?'is-ready':''}"><div class="setup-card-head"><b>② 加入或新建项目</b><small>项目配置</small>${badge(projReady)}</div>
    <p>${projDesc}</p>
    <div class="setup-steps"><button type="button" class="setup-step" data-goto-view="projects"><i>→</i><span>${projReady?`当前项目：${escapeHTML((selMine||myProjects[0]).name)}（点此切换或管理成员）`:'前往项目配置页新建 / 加入项目'}</span><em>→</em></button></div></div>`;
  // 第 3 步：复制通用 SOP 到本项目
  const sopCard=`<div class="setup-card ${sopReady?'is-ready':''}"><div class="setup-card-head"><b>③ 复制通用 SOP</b><small>SOP 流程</small>${badge(sopReady)}</div>
    <p>打开 SOP 流程，切到「<b>内置</b>」标签，选「<b>通用后端交付 SOP</b>」→「复制到本项目」。${sopReady?'你的项目已有 '+mySops.length+' 条 SOP，可跳过。':'内置 SOP 是平台维护的只读蓝图，复制后才能派发和修改。'}</p>
    <div class="setup-steps"><button type="button" class="setup-step" data-goto-view="sops"><i>→</i><span>前往 SOP 流程 · 内置</span><em>→</em></button></div></div>`;
  // 第 4 步：派发需求跑起来
  const issueCard=`<div class="setup-card"><div class="setup-card-head"><b>④ 跑一条需求</b><small>需求列表</small></div>
    <p>同步或新建一条需求，选刚复制的 SOP，点<b>派发</b>。之后到「运行记录」看进度，人工确认在「待我确认」处理。</p>
    <div class="setup-steps"><button type="button" class="setup-step" data-goto-view="issues"><i>→</i><span>${allReady?'配置已就绪，去派发第一个需求':'先完成上面三步，再来派发'}</span><em>→</em></button></div></div>`;
  const doneBanner=allReady
    ?`<div class="setup-done"><div><b>✓ 最小配置完成</b><p>凭据、项目、SOP 都已就绪，可以派发需求运行 SOP 了。</p></div><button type="button" class="button primary" data-goto-view="issues">去派发第一个需求 →</button></div>`
    :`<div class="setup-done pending"><div><b>就绪度 ${credDone}/2 凭据 · ${projReady?'✓':'—'} 项目 · ${sopReady?'✓':'—'} SOP</b><p>按上面 4 步依次完成，全部就绪后即可直接派发需求运行 SOP。</p></div></div>`;
  body.innerHTML=`<div class="quick-wrap"><div class="quick-hero"><div class="quick-hero-text"><h2>最小接入配置</h2><p>按 4 步配好，平台即可完整运行需求：凭据就位、项目就位、流程就位，然后派发第一条需求。状态实时检测，完成即亮。</p></div><div class="quick-hero-facts"><div><b>${credDone}/2</b><span>个人凭据</span></div><div><b>${projReady?'✓':'—'}</b><span>项目</span></div><div><b>${sopReady?'✓':'—'}</b><span>SOP</span></div><div><b>${allReady?'就绪':'进行中'}</b><span>整体状态</span></div></div></div><div class="setup-grid">${credCard}${projCard}${sopCard}${issueCard}</div>${doneBanner}</div>`;
  body.querySelectorAll('[data-setup-cred]').forEach(b=>b.onclick=()=>gotoCredential(b.dataset.setupCred));
  body.querySelectorAll('[data-goto-view]').forEach(b=>b.onclick=()=>switchView(b.dataset.gotoView));
}
function renderGuide(openCreate=false){
  const body=$('#accessBody'),summary=$('#accessSummary');if(!body||!summary)return;
  if(!ACCESS_TABS.includes(state.accessTab))state.accessTab='setup';
  summary.hidden=state.accessTab==='mcp'||state.accessTab==='setup';
  summary.innerHTML=summary.hidden?'':accessSummary();
  $$('#accessTabs [data-access-tab]').forEach(b=>b.classList.toggle('active',b.dataset.accessTab===state.accessTab));
  if(state.accessTab==='setup'){renderGuideSetup();return}
  if(state.accessTab==='tokens'){renderAccessTokens(openCreate);return}
  if(state.accessTab==='capabilities'){if(state.accessCapabilitiesStatus==='idle')loadAccessCapabilities();renderAccessCapabilities();return}
  if(state.accessTab==='api'){renderAccessGuide();return}
  const base=getPublicBaseURL(),token=state.revealedToken||'<YOUR_PERSONAL_TOKEN>';
  if(state.accessCapabilitiesStatus==='idle'){
    loadAccessCapabilities();
  }
  const mcpSpec=accessMcpClientSpec(state.accessClient,base,token);
  const mcpPrompt=accessMcpAgentPrompt(mcpSpec,base,token);
  const checkCode=`curl -sS -H 'Authorization: Bearer ${token}' \\\n  -H 'Content-Type: application/json' \\\n  -d '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}' \\\n  ${base}/mcp`;
  const count=state.accessCapabilities?.operations?.length||82;
  const clients=[['cursor','Cursor'],['claude','Claude Code'],['codebuddy','CodeBuddy'],['codex','Codex']].map(([id,label])=>`<button type="button" class="${state.accessClient===id?'active':''}" data-access-client="${id}">${label}</button>`).join('');
  const tools=[['projects_save','管理项目与仓库'],['agents_save','定义 Agent 角色'],['workflows_publish','发布 SOP 流程'],['issues_dispatch','派发需求并启动'],['runs_get','查看运行与产物'],['runs_approve','处理人工确认']].map(([name,desc])=>`<div class="quick-tool"><code>${name}</code><span>${desc}</span></div>`).join('');
  body.innerHTML=`<div class="quick-wrap"><section class="quick-onboard"><div class="quick-onboard-main"><div class="quick-onboard-heading"><span>MCP 快速接入</span><h2>复制给 Agent，自动完成配置</h2><p>选择你正在使用的客户端。首次点击会自动创建专用 Token，并生成可直接使用的接入提示词。</p></div><label class="quick-onboard-label">选择客户端</label><div class="access-clients">${clients}</div>${accessAgentSetup(mcpPrompt)}<div class="quick-safe-note">Token 有效期 90 天，仅在本次创建后展示，可随时在「我的 Token」撤销。</div><details class="access-manual"><summary>手工配置 · ${escapeHTML(mcpSpec.location)}</summary>${accessCode(mcpSpec.codeTitle,mcpSpec.code)}</details></div><aside class="quick-onboard-next"><span class="quick-next-label">复制后</span><b>粘贴给 Agent 即可</b><ol><li><i>1</i><span>curl 下载安装流程</span></li><li><i>2</i><span>合并现有 MCP 配置</span></li><li><i>3</i><span>自检并报告连接结果</span></li></ol><div class="quick-success">✓ 工具列表出现即接入成功</div></aside></section><section class="quick-after"><div class="access-card"><div class="access-card-title"><i>✓</i>接入后能做什么</div><div class="quick-tool-grid">${tools}</div><button type="button" class="button ghost small" data-guide-caps>查看全部 ${count} 个能力 →</button></div><div class="access-card quick-help"><div><b>长任务</b><p><code>issues_dispatch</code> 获取 Run ID，再用 <code>runs_get</code> 查询结果。</p></div><div><b>Token 安全</b><p>不要提交到代码仓库；怀疑泄露时立即撤销。</p></div><button type="button" class="button ghost small" data-guide-api>REST API 接入 →</button></div></section></div>`;
  $('#guideCreateToken').onclick=()=>setAccessTab('tokens',true);
  body.querySelectorAll('[data-copy-access]').forEach(b=>b.onclick=()=>copyText(decodeURIComponent(b.dataset.copyAccess),'配置已复制'));
  body.querySelectorAll('[data-copy-agent-prompt]').forEach(b=>b.onclick=()=>copyText(decodeURIComponent(b.dataset.copyAgentPrompt),'配置提示词已复制，可直接发给 Agent'));
  body.querySelector('[data-create-token-first]')?.addEventListener('click',e=>createQuickAccessPrompt(e.currentTarget));
  body.querySelectorAll('[data-access-client]').forEach(b=>b.onclick=()=>{state.accessClient=b.dataset.accessClient;renderGuide()});
  body.querySelectorAll('[data-goto-view]').forEach(b=>b.onclick=()=>switchView(b.dataset.gotoView));
  body.querySelector('[data-guide-caps]')?.addEventListener('click',()=>setAccessTab('capabilities'));
  body.querySelector('[data-guide-api]')?.addEventListener('click',()=>setAccessTab('api'));
}
function renderAccessCapabilities(){
  const body=$('#accessBody'),ops=state.accessCapabilities?.operations||[];
  if(state.accessCapabilitiesStatus==='loading'){
    body.innerHTML='<div class="access-card access-tokens"><div class="token-empty">正在加载能力清单…</div></div>';
    return;
  }
  if(state.accessCapabilitiesStatus==='error'){
    body.innerHTML=`<div class="access-card access-tokens"><div class="token-empty"><b>能力清单加载失败</b><p>${escapeHTML(state.accessCapabilitiesError||'请检查登录状态或网络连接')}</p><button type="button" class="button primary small" data-retry-capabilities>重试</button></div></div>`;
    body.querySelector('[data-retry-capabilities]').onclick=()=>loadAccessCapabilities();
    return;
  }
  body.innerHTML=`<div class="access-toolbar"><input id="accessCapabilitySearch" class="input" type="search" placeholder="搜索工具名、路径或说明"><span class="issue-stat">共 <b>${ops.length}</b> 个操作</span><button type="button" class="button ghost small" data-copy-capabilities>⧉ 复制 JSON</button></div><div class="access-table-wrap"><table class="access-table"><thead><tr><th>MCP 工具</th><th>HTTP 接口</th><th>分组</th><th>说明</th><th>读写</th></tr></thead><tbody></tbody></table></div>`;
  const renderRows=()=>{
    const query=($('#accessCapabilitySearch')?.value||'').trim().toLowerCase();
    const visible=ops.filter(op=>`${op.name} ${op.method} ${op.path} ${op.description}`.toLowerCase().includes(query));
    body.querySelector('tbody').innerHTML=visible.map(op=>`<tr><td><code>${escapeHTML(op.name)}</code></td><td><span class="access-method">${escapeHTML(op.method)}</span> <code>${escapeHTML(op.path)}</code></td><td>${escapeHTML(accessGroup(op))}</td><td>${escapeHTML(op.description)}</td><td>${op.readOnly?'只读':'写入'}</td></tr>`).join('');
  };
  renderRows();$('#accessCapabilitySearch').oninput=renderRows;
  body.querySelector('[data-copy-capabilities]').onclick=()=>copyText(JSON.stringify(state.accessCapabilities,null,2),'能力清单已复制');
}
// loadAccessTokens always settles accessTokensStatus, and only repaints when the
// user is still looking at the token tab, so a slow or failing request can never
// leave the loading placeholder on top of another tab.
async function loadAccessTokens(){
  state.accessTokensStatus='loading';state.accessTokensError='';
  try{
    const list=await api('/api/auth/tokens',{silent:true});
    state.accessTokens=Array.isArray(list)?list:[];
    state.accessTokensStatus='ready';
  }catch(e){
    state.accessTokens=[];state.accessTokensStatus='error';state.accessTokensError=e.message||'加载失败';
  }
  if(state.accessTab==='tokens'&&accessViewActive())renderAccessTokens();
}
function renderAccessTokens(openCreate=false){
  const body=$('#accessBody'),user=state.currentUser,isGuest=!user||user.isGuest,tokens=Array.isArray(state.accessTokens)?state.accessTokens:[];
  if(isGuest){
    body.innerHTML=`<div class="access-card access-tokens"><div class="token-empty"><b>请先登录</b><p>个人 Token 必须绑定到登录用户。</p><button type="button" class="button primary" data-access-login>登录后申请</button></div></div>`;
    body.querySelector('[data-access-login]').onclick=()=>{if(state.loginUrl)location.href=state.loginUrl};
    return;
  }
  if(state.accessTokensStatus==='idle'){loadAccessTokens()}
  if(state.accessTokensStatus==='loading'){body.innerHTML='<div class="access-card access-tokens"><div class="token-empty">正在加载我的 Token…</div></div>';return}
  if(state.accessTokensStatus==='error'){
    body.innerHTML=`<div class="access-card access-tokens"><div class="token-empty"><b>Token 列表加载失败</b><p>${escapeHTML(state.accessTokensError)}</p><div style="display:flex;gap:8px;justify-content:center"><button type="button" class="button primary small" data-retry-tokens>重试</button><button type="button" class="button ghost small" data-access-login>重新登录</button></div></div></div>`;
    body.querySelector('[data-retry-tokens]').onclick=()=>{state.accessTokensStatus='idle';renderAccessTokens()};
    body.querySelector('[data-access-login]').onclick=()=>{if(state.loginUrl)location.href=state.loginUrl};
    return;
  }
  const base=getPublicBaseURL();
  const revealedSpec=state.revealedToken?accessMcpClientSpec(state.accessClient,base,state.revealedToken):null;
  const revealedPrompt=revealedSpec?accessMcpAgentPrompt(revealedSpec,base,state.revealedToken):'';
  const revealClients=state.revealedToken?[['cursor','Cursor'],['claude','Claude Code'],['codebuddy','CodeBuddy'],['codex','Codex']].map(([id,label])=>`<button type="button" class="${state.accessClient===id?'active':''}" data-revealed-client="${id}">${label}</button>`).join(''):'';
  const reveal=state.revealedToken?`<div class="token-reveal"><b>Token 已创建，可直接复制完整配置提示词</b><p>先选择客户端；复制内容会自动带上本次 Token 和 Endpoint，无需再次替换占位符。平台只保存哈希，关闭后无法再次查看明文。</p><div class="access-clients token-client-picker">${revealClients}</div><div class="token-reveal-line"><code>${escapeHTML(state.revealedToken)}</code><button type="button" class="button primary small" data-copy-revealed-prompt="${encodeURIComponent(revealedPrompt)}">⧉ 复制 ${escapeHTML(revealedSpec.label)} 配置提示词</button><button type="button" class="button ghost small" data-copy-revealed>仅复制 Token</button><button type="button" class="button ghost small" data-close-revealed>已保存</button></div></div>`:'';
  const rows=tokens.length?tokens.map(t=>{const revoked=!!t.revokedAt,expired=t.expiresAt&&new Date(t.expiresAt)<new Date();return `<div class="token-row ${revoked||expired?'revoked':''}"><div class="token-row-main"><b>${escapeHTML(t.name)}</b><code>${escapeHTML(t.prefix)}••••••••</code></div><div class="token-meta">创建 ${relativeTime(t.createdAt)} · ${t.lastUsedAt?`最近使用 ${relativeTime(t.lastUsedAt)}`:'尚未使用'} · ${revoked?'已撤销':expired?'已过期':`有效至 ${new Date(t.expiresAt).toLocaleDateString()}`}</div>${!revoked&&!expired?`<button type="button" class="button danger-outline small" data-revoke-token="${escapeHTML(t.id)}">撤销</button>`:''}</div>`}).join(''):'<div class="token-empty">还没有个人 Token，创建后即可接入 REST API 和 MCP。</div>';
  const activeCount=tokens.filter(t=>!t.revokedAt&&(!t.expiresAt||new Date(t.expiresAt)>new Date())).length;
  body.innerHTML=`<div class="access-card access-tokens">${reveal}<form id="accessTokenForm" class="token-create-form"><label>Token 名称<input class="input" name="name" required maxlength="128" placeholder="例如：我的 Cursor"></label><label>有效期<select class="select" name="expiresInDays"><option value="30">30 天</option><option value="90" selected>90 天</option><option value="180">180 天</option><option value="365">365 天</option></select></label><button type="submit" class="button primary">创建 Token</button></form><div class="access-card-title"><i>K</i>我的 Token <span style="margin-left:auto;color:var(--muted-2);font-size:10.5px">${activeCount} 个可用</span></div><div class="token-list">${rows}</div></div>`;
  body.querySelector('[data-copy-revealed]')?.addEventListener('click',()=>copyText(state.revealedToken,'Token 已复制'));
  body.querySelector('[data-copy-revealed-prompt]')?.addEventListener('click',b=>copyText(decodeURIComponent(b.currentTarget.dataset.copyRevealedPrompt),`${revealedSpec.label} 配置提示词已复制`));
  body.querySelectorAll('[data-revealed-client]').forEach(b=>b.onclick=()=>{state.accessClient=b.dataset.revealedClient;renderAccessTokens()});
  body.querySelector('[data-close-revealed]')?.addEventListener('click',()=>{state.revealedToken='';renderAccessTokens()});
  body.querySelector('#accessTokenForm')?.addEventListener('submit',createPersonalToken);
  body.querySelectorAll('[data-revoke-token]').forEach(b=>b.onclick=()=>revokePersonalToken(b.dataset.revokeToken));
}
async function createPersonalToken(e){
  e.preventDefault();const form=e.currentTarget;
  try{const result=await api('/api/auth/tokens',{method:'POST',body:JSON.stringify({name:form.elements.name.value.trim(),expiresInDays:Number(form.elements.expiresInDays.value)})});state.revealedToken=result.token;await loadAccessTokens();toast('Token 已创建，请立即复制','ok')}catch(err){toast(err.message,'err')}
}
async function revokePersonalToken(id){
  if(!(await afConfirm({title:'撤销 Token',message:'撤销后，正在使用这个 Token 的 API 和 MCP 客户端会立即失效。确定撤销？',danger:true,confirmText:'确定撤销'})))return;
  try{await api(`/api/auth/tokens/${encodeURIComponent(id)}`,{method:'DELETE'});await loadAccessTokens();toast('Token 已撤销','ok')}catch(err){toast(err.message,'err')}
}


let commandMatches=[],commandSelectedIndex=0;
function commandCatalog(){
  const pages=[['dashboard','工作总览','看失败、待确认和进行中的需求'],['issues','需求列表','查看进度，触发或重试'],['approvals','待我确认','批准或退回卡住的需求'],['sops','SOP 流程','可复用的 AI 协作流程'],['runs','运行记录','查看节点日志和产物'],['metrics','研发效能','产能、效率、质量、成本与自动化看板'],['deployments','部署耗时','按分支查看测试环境流水线耗时'],['agents','角色定义','管理角色和能力资产'],['mcp','MCP 管理','添加项目级或个人自定义 MCP'],['rules','规则管理','当前项目的行为与执行约束'],['skills','技能管理','当前项目的技能和附加文件'],['dockerfiles','Dockerfile','管理项目容器构建定义'],['environments','环境安装脚本','管理隔离环境的工具安装脚本'],['sandboxes','沙箱管理','管理项目运行隔离策略'],['observability','用量分析','Token 与质量'],['businessDomains','业务领域','分类管理与跨项目汇报'],['projects','项目配置','项目、成员和仓库'],['guide','接入指南','最小接入配置、MCP 或 REST API 接入']].filter(([view])=>view!=='deployments'||canShowSkillhubEfficiency()).map(([view,title,meta])=>({kind:'页面',title,meta,action:()=>switchView(view)}));
  const issues=(state.data.issues||[]).map(i=>({kind:'需求',title:i.title,meta:`${i.priority||'—'} · ${issueStatusLabel(i.status)}`,action:()=>openIssueDetail(i.id)}));
  const runs=(state.data.runs||[]).map(r=>({kind:'运行',title:runIssueTitle(r),meta:`${runDisplayStatus(r)} · ${r.workflowSnapshot?.name||r.workflowId}`,action:()=>openRunFromId(r.id)}));
  const sops=catalogWorkflows().map(w=>({kind:'SOP',title:w.name,meta:`${w.status==='published'?'已发布':'草稿'} · ${w.nodes?.length||0} 个节点`,action:()=>{state.workflow=clone(w);state.selectedNode=null;switchView('flow')}}));
  const agents=(state.data.agents||[]).map(a=>({kind:'Agent',title:a.name,meta:a.description||a.id,action:()=>{switchView('agents');openAgentWizard(a)}}));
  const actions=[{kind:'操作',title:'新建 SOP',meta:'创建流程草稿',action:openSOPWizard}];
  return [...actions,...pages,...issues,...runs,...sops,...agents];
}
function openCommandPalette(){const host=$('#commandPalette');host.hidden=false;$('#commandSearch').value='';renderCommandResults();requestAnimationFrame(()=>$('#commandSearch').focus())}
function closeCommandPalette(){$('#commandPalette').hidden=true}
function moveCommandSelection(delta){
  if(!commandMatches.length)return;
  commandSelectedIndex=(commandSelectedIndex+delta+commandMatches.length)%commandMatches.length;
  highlightCommandSelection();
}
function highlightCommandSelection(){
  $$('#commandResults [data-command-index]').forEach((el,i)=>{
    el.classList.toggle('selected',i===commandSelectedIndex);
    if(i===commandSelectedIndex)el.scrollIntoView({block:'nearest'});
  });
}
function renderCommandResults(){
  const q=$('#commandSearch').value.trim().toLowerCase();
  commandMatches=commandCatalog().filter(x=>!q||`${x.kind} ${x.title} ${x.meta}`.toLowerCase().includes(q)).slice(0,12);
  commandSelectedIndex=0;
  $('#commandResults').innerHTML=commandMatches.length
    ?commandMatches.map((x,i)=>`<button data-command-index="${i}" class="${i===0?'selected':''}"><span class="command-kind">${escapeHTML(x.kind)}</span><span><b>${escapeHTML(x.title)}</b><small>${escapeHTML(x.meta||'')}</small></span><em>↵</em></button>`).join('')
    :`<div class="command-empty"><b>没有匹配结果</b><span>换一个标题、编号或页面名称试试</span></div>`;
}
function executeCommand(index){const item=commandMatches[index];if(!item)return;closeCommandPalette();item.action()}
