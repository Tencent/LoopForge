// 访问密钥：访问密钥 的创建、撤销，以及创建后复制 MCP 接入提示词。
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
  return `按照这个流程安装、初始化并验证好 LoopForge MCP：\n\n1. 使用 curl 下载 安装流程文件, 禁止使用 fetch：${base}/mcp-setup.txt\n2. 两个占位符替换为：\n   API_TOKEN = ${token}\n   ENDPOINT = ${base}/mcp`;
}
// 对外展示的固定接入地址：优先使用部署下发的 APP_DOMAIN，避免硬编码固定域名。
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
function accessViewActive(){return !!$('#guideView')?.classList.contains('active')}
let credentialFocusRequest=0;
function gotoCredential(name){
  const request=++credentialFocusRequest;
  if(!CREDENTIAL_FIELDS[name]){switchView('settings');return}
  switchView('settings');
  const requestedRoute=settingsRouteTransition;
  const focusAfter=performance.now()+150;
  const focusCredential=()=>{
    const transition=settingsRouteTransition;
    if(request!==credentialFocusRequest||(transition&&transition!==requestedRoute))return;
    if(transition&&transition.target!=='settings')return;
    if(activeViewName()!=='settings'){
      // The outgoing workspace drawer closes before settings is mounted.
      // A different route cancels this request instead of stealing focus later.
      if(transition===requestedRoute&&transition?.phase==='closing')requestAnimationFrame(focusCredential);
      return;
    }
    if(performance.now()<focusAfter){requestAnimationFrame(focusCredential);return}
    const panel=document.querySelector('#personalSettingsContent #credentialPanel'),row=panel?.querySelector(`[data-credential-row="${name}"]`);
    if(!row||!panel.getClientRects().length)return;
    // Missing credentials open the wizard on its fill-in step; configured ones expand in place.
    if(row.classList.contains('is-configured'))revealCredentialRow(name);
    else openCredentialWizard(name);
  };
  requestAnimationFrame(focusCredential);
}
// Personal credential fields and the errors they cause. Keep the rules in
// sync with chatbot/src/credentials.js.
const CREDENTIAL_FIELDS={codeBuddyApiKey:'CodeBuddy API Key',codexApiKey:'Codex API Key',claudeCodeApiKey:'Claude Code API Key',gitToken:'GitHub Git Token'};
const CREDENTIAL_RULES=[
  ['codexApiKey',/Codex[^。\n]{0,16}API Key/i],
  ['claudeCodeApiKey',/Claude Code[^。\n]{0,16}API Key/i],
  ['codeBuddyApiKey',/CodeBuddy[^。\n]{0,20}(API Key|Key|凭据)|拒绝了你的 API Key/i],
  ['gitToken',/Git Token|工蜂[^。\n]{0,12}令牌|could not read Username|Authentication failed|HTTP Basic: Access denied|terminal prompts disabled|requested URL returned error: 40[13]/i],
];
function credentialHint(text){
  const message=String(text||'');
  if(!message)return null;
  const rule=CREDENTIAL_RULES.find(([,re])=>re.test(message));
  return rule?{field:rule[0],label:CREDENTIAL_FIELDS[rule[0]],owner:/责任人|Owner|该账号/i.test(message)}:null;
}
function credentialActionHTML(text){
  const hint=credentialHint(text);
  if(!hint)return '';
  return `<div class="credential-action"><button type="button" class="button ghost small" data-goto-credential="${hint.field}">去配置 ${escapeHTML(hint.label)} →</button>${hint.owner?'<small>需由责任人本人在个人配置中配置</small>':''}</div>`;
}
document.addEventListener('click',e=>{
  const button=e.target.closest?.('[data-goto-credential]');
  if(button)gotoCredential(button.dataset.gotoCredential);
});
function renderGuide(openCreate=false){if($('#accessBody'))renderAccessTokens(openCreate)}
// loadAccessTokens always settles accessTokensStatus, and only repaints when the
// user is still on 访问密钥, so a slow or failing request can never leave the
// loading placeholder on top of another page.
async function loadAccessTokens(){
  state.accessTokensStatus='loading';state.accessTokensError='';
  try{
    const list=await api('/api/auth/tokens',{silent:true});
    state.accessTokens=Array.isArray(list)?list:[];
    state.accessTokensStatus='ready';
  }catch(e){
    state.accessTokens=[];state.accessTokensStatus='error';state.accessTokensError=e.message||'加载失败';
  }
  if(accessViewActive())renderAccessTokens();
}
function renderAccessTokens(openCreate=false){
  const body=$('#accessBody'),user=state.currentUser,isGuest=!user||user.isGuest,tokens=Array.isArray(state.accessTokens)?state.accessTokens:[];
  if(isGuest){
    body.innerHTML=`<div class="access-card access-tokens"><div class="token-empty"><b>请先登录</b><p>访问密钥必须绑定到登录用户。</p><button type="button" class="button primary" data-access-login>登录后申请</button></div></div>`;
    body.querySelector('[data-access-login]').onclick=()=>{if(state.loginUrl)location.href=state.loginUrl};
    return;
  }
  if(state.accessTokensStatus==='idle'){loadAccessTokens()}
  if(state.accessTokensStatus==='loading'){body.innerHTML='<div class="access-card access-tokens"><div class="token-empty">正在加载访问密钥…</div></div>';return}
  if(state.accessTokensStatus==='error'){
    body.innerHTML=`<div class="access-card access-tokens"><div class="token-empty"><b>访问密钥列表加载失败</b><p>${escapeHTML(state.accessTokensError)}</p><div style="display:flex;gap:8px;justify-content:center"><button type="button" class="button primary small" data-retry-tokens>重试</button><button type="button" class="button ghost small" data-access-login>重新登录</button></div></div></div>`;
    body.querySelector('[data-retry-tokens]').onclick=()=>{state.accessTokensStatus='idle';renderAccessTokens()};
    body.querySelector('[data-access-login]').onclick=()=>{if(state.loginUrl)location.href=state.loginUrl};
    return;
  }
  const base=getPublicBaseURL();
  const revealedSpec=state.revealedToken?accessMcpClientSpec(state.accessClient,base,state.revealedToken):null;
  const revealedPrompt=revealedSpec?accessMcpAgentPrompt(revealedSpec,base,state.revealedToken):'';
  const revealClients=state.revealedToken?[['cursor','Cursor'],['claude','Claude Code'],['codebuddy','CodeBuddy'],['codex','Codex']].map(([id,label])=>`<button type="button" class="${state.accessClient===id?'active':''}" data-revealed-client="${id}">${label}</button>`).join(''):'';
  const reveal=state.revealedToken?`<div class="token-reveal"><b>访问密钥已创建，可直接复制完整配置提示词</b><p>先选择客户端；复制内容会自动带上本次访问密钥和 Endpoint，无需再次替换占位符。平台只保存哈希，关闭后无法再次查看明文。</p><div class="access-clients token-client-picker">${revealClients}</div><div class="token-reveal-line"><code>${escapeHTML(state.revealedToken)}</code><button type="button" class="button primary small" data-copy-revealed-prompt="${encodeURIComponent(revealedPrompt)}">⧉ 复制 ${escapeHTML(revealedSpec.label)} 配置提示词</button><button type="button" class="button ghost small" data-copy-revealed>仅复制密钥</button><button type="button" class="button ghost small" data-close-revealed>已保存</button></div></div>`:'';
  const rows=tokens.length?tokens.map(t=>{const revoked=!!t.revokedAt,expired=t.expiresAt&&new Date(t.expiresAt)<new Date();return `<div class="token-row ${revoked||expired?'revoked':''}"><div class="token-row-main"><b>${escapeHTML(t.name)}</b><code>${escapeHTML(t.prefix)}••••••••</code></div><div class="token-meta">创建 ${relativeTime(t.createdAt)} · ${t.lastUsedAt?`最近使用 ${relativeTime(t.lastUsedAt)}`:'尚未使用'} · ${revoked?'已撤销':expired?'已过期':`有效至 ${new Date(t.expiresAt).toLocaleDateString()}`}</div>${!revoked&&!expired?`<button type="button" class="button danger-outline small" data-revoke-token="${escapeHTML(t.id)}">撤销</button>`:''}</div>`}).join(''):'<div class="token-empty">还没有访问密钥，创建后即可接入 REST API 和 MCP。</div>';
  const activeCount=tokens.filter(t=>!t.revokedAt&&(!t.expiresAt||new Date(t.expiresAt)>new Date())).length;
  body.innerHTML=`<div class="access-card access-tokens">${reveal}<form id="accessTokenForm" class="token-create-form"><label>密钥名称<input class="input" name="name" required maxlength="128" placeholder="例如：我的 Cursor"></label><label>有效期<select class="select" name="expiresInDays"><option value="30">30 天</option><option value="90" selected>90 天</option><option value="180">180 天</option><option value="365">365 天</option></select></label><button type="submit" class="button primary">创建访问密钥</button></form><div class="access-card-title"><i>K</i>我的访问密钥 <span style="margin-left:auto;color:var(--muted-2);font-size:10.5px">${activeCount} 个可用</span></div><div class="token-list">${rows}</div></div>`;
  body.querySelector('[data-copy-revealed]')?.addEventListener('click',()=>copyText(state.revealedToken,'访问密钥已复制'));
  body.querySelector('[data-copy-revealed-prompt]')?.addEventListener('click',b=>copyText(decodeURIComponent(b.currentTarget.dataset.copyRevealedPrompt),`${revealedSpec.label} 配置提示词已复制`));
  body.querySelectorAll('[data-revealed-client]').forEach(b=>b.onclick=()=>{state.accessClient=b.dataset.revealedClient;renderAccessTokens()});
  body.querySelector('[data-close-revealed]')?.addEventListener('click',()=>{state.revealedToken='';renderAccessTokens()});
  body.querySelector('#accessTokenForm')?.addEventListener('submit',createPersonalToken);
  body.querySelectorAll('[data-revoke-token]').forEach(b=>b.onclick=()=>revokePersonalToken(b.dataset.revokeToken));
}
async function createPersonalToken(e){
  e.preventDefault();const form=e.currentTarget;
  try{const result=await api('/api/auth/tokens',{method:'POST',body:JSON.stringify({name:form.elements.name.value.trim(),expiresInDays:Number(form.elements.expiresInDays.value)})});state.revealedToken=result.token;await loadAccessTokens();toast('访问密钥已创建，请立即复制','ok')}catch(err){toast(err.message,'err')}
}
async function revokePersonalToken(id){
  if(!(await afConfirm({title:'撤销访问密钥',message:'撤销后，正在使用这个访问密钥的 API 和 MCP 客户端会立即失效。确定撤销？',danger:true,confirmText:'确定撤销'})))return;
  try{await api(`/api/auth/tokens/${encodeURIComponent(id)}`,{method:'DELETE'});await loadAccessTokens();toast('访问密钥已撤销','ok')}catch(err){toast(err.message,'err')}
}


let commandMatches=[],commandSelectedIndex=0;
function commandCatalog(){
  const pages=[['collaboration','AI 工作台','和 AI 对话，派发与跟进任务'],['issues','需求列表','查看进度，触发或重试'],['approvals','待我确认','批准或退回卡住的需求'],['observability','统计','用量分析 · Token 与质量'],['settings','个人配置','账号、模型与执行凭据'],['projects','项目配置','项目、成员和仓库'],['bots','通道管理','企业微信单聊或群聊通知通道'],['sops','流程模板','Issue 修复与需求开发'],['runs','运行记录','查看节点日志和产物'],['aiSchedules','定时任务','提示词任务、SOP 触发与共享任务'],['agents','角色定义','管理角色和能力资产'],['mcp','能力资产 · MCP','添加项目级或个人 MCP'],['rules','能力资产 · 规则','当前项目的行为与执行约束'],['skills','能力资产 · 技能','当前项目的技能和附加文件'],['runners','执行机器','用自己的电脑或 CVM 跑任务'],['dockerfiles','环境资产 · Dockerfile','管理项目容器构建定义'],['environments','环境资产 · 安装脚本','管理隔离环境的工具安装脚本'],['sandboxes','环境资产 · 沙箱','管理项目运行隔离策略'],['guide','访问密钥','创建和撤销访问密钥']].map(([view,title,meta])=>({kind:'页面',title,meta,action:()=>switchView(view)}));
  const issues=(state.data.issues||[]).map(i=>({kind:'需求',title:i.title,meta:`${i.priority||'—'} · ${issueStatusLabel(i.status)}`,action:()=>openIssueDetail(i.id)}));
  const runs=sopRuns(state.data.runs).map(r=>({kind:'运行',title:runIssueTitle(r),meta:`${runDisplayStatus(r)} · ${r.workflowSnapshot?.name||r.workflowId}`,action:()=>openRunFromId(r.id)}));
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
