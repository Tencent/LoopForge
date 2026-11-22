// 访问密钥：访问密钥 的创建、撤销，以及创建后复制 MCP 接入提示词。
function accessMcpClientSpec(client,base,token){
  const json=JSON.stringify({mcpServers:{agentflow:{url:`${base}/mcp`,headers:{Authorization:`Bearer ${token}`}}}},null,2);
  const specs={
    cursor:{label:'Cursor',location:'~/.cursor/mcp.json',codeTitle:'mcp.json',code:json,verify:'在 Cursor 设置的 MCP 列表中确认 agentflow 已连接'},
    claude:{label:'Claude Code',location:'Claude CLI（用户级配置写入 ~/.claude.json）',codeTitle:'Shell',code:`claude mcp add --transport http --scope user agentflow ${base}/mcp \\\n  --header "Authorization: Bearer ${token}"`,verify:'claude mcp get agentflow && claude mcp list'},
    codebuddy:{label:'CodeBuddy',location:'CodeBuddy 用户级 MCP 配置',codeTitle:'mcp.json',code:json,verify:'在 CodeBuddy 的 MCP 管理页确认 agentflow 已连接'},
    codex:{label:'Codex',location:'~/.codex/config.toml',codeTitle:'config.toml',code:`[mcp_servers.agentflow]\nurl = "${base}/mcp"\nhttp_headers = { Authorization = "Bearer ${token}" }`,verify:'codex mcp list'}
  };
  return specs[client]||specs.cursor;
}
function accessMcpAgentPrompt(spec,base,token){
  if(!token||token==='<YOUR_PERSONAL_TOKEN>')return '';
  const guide = `${base}/mcp-setup.txt`;
  const instructions = window.lfT(
    `请为 ${spec.label} 接入 LoopForge 平台 MCP。使用 curl 下载 ${guide}（禁止使用 fetch），按指南中 ${spec.label} 的步骤配置，最后用 tools/list 验证连接。`,
    `Connect ${spec.label} to the LoopForge platform MCP server. Download ${guide} with curl, follow the steps for ${spec.label}, then verify the connection with tools/list.`
  );
  return `${instructions}\n\nAPI_TOKEN = ${token}\nENDPOINT = ${base}/mcp`;
}
// 对外展示的固定接入地址：优先使用部署下发的 APP_DOMAIN，避免硬编码固定域名。
function getPublicBaseURL(){
  const configured = (typeof window !== 'undefined' && (window.__AGENTFLOW_APP_DOMAIN__ || window.__AGENTFLOW_PUBLIC_BASE_URL__))
    || (typeof state !== 'undefined' && state?.appDomain ? state.appDomain : '');
  return accessPublicBaseURL(configured, typeof window !== 'undefined' ? window.location : null);
}
// 接入片段会携带 Bearer Token：配置里显式的协议优先，否则跟随当前页面；
// HTTPS 页面生成的地址不降级为 http://。只有配置显式写了 http:// 和非 443 端口时
// 保留 http，因为把只提供 HTTP 的端口改成 https:// 会指向一个没有 TLS 的端口。
function accessPublicBaseURL(configured, page){
  const secure = page?.protocol === 'https:';
  const fallback = (secure ? 'https:' : 'http:') + '//' + (page?.host || 'loopforge.example.com');
  const raw = String(configured || '').trim();
  if(!raw) return fallback;
  const explicitScheme = /^[a-z][a-z0-9+.-]*:\/\//i.test(raw);
  let url;
  try{ url = new URL(explicitScheme ? raw : (secure ? 'https://' : 'http://') + raw) }catch(_){ return fallback }
  if(!['http:','https:'].includes(url.protocol) || !url.host) return fallback;
  const explicitHTTPPort = explicitScheme && url.protocol === 'http:' && url.port && url.port !== '443';
  if(secure && !explicitHTTPPort) url.protocol = 'https:';
  return url.protocol + '//' + url.host + url.pathname.replace(/\/+$/, '');
}
function accessViewActive(){return !!$('#guideView')?.classList.contains('active')}
let credentialFocusRequest=0;
function cancelCredentialFocus(){credentialFocusRequest++}
function gotoCredential(name,sshRemote){
  const request=++credentialFocusRequest;
  if(name!=='executors'&&!Object.hasOwn(CREDENTIAL_FIELDS,name)){switchView('settings');return}
  if(name==='gitSshKey')return openGitSSHKeys(sshRemote);
  if(name==='gitHttpCredentials')return openGitHTTPCredentials();
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
    if(!panel?.getClientRects().length)return;
    if(name==='executors'){
      const group=panel.querySelector('[data-credential-group="model"]');
      if(matchMedia('(max-width:1000px)').matches)setSettingsSidebarExpanded(false);
      group?.scrollIntoView({block:'nearest',behavior:'instant'});
      const select=group?.querySelector('[data-credential-add-menu]');
      if(select)window.HuiSelect?.enhance(select)?.open();
      return;
    }
    // Missing credentials open the wizard on its fill-in step; configured ones expand in place.
    if(row?.classList.contains('is-configured'))revealCredentialRow(name);
    else openCredentialWizard(name);
  };
  requestAnimationFrame(focusCredential);
}
// Personal credential fields and the errors they cause. Keep the rules in
// sync with chatbot/src/credentials.js.
const CREDENTIAL_FIELDS={codeBuddyApiKey:'CodeBuddy API Key',codexApiKey:'Codex API Key',claudeCodeApiKey:'Claude Code API Key',gitToken:'GitHub Git Token',gitSshKey:'Git SSH 密钥',gitHttpCredentials:'Git 访问令牌'};
const CREDENTIAL_RULES=[
  ['codexApiKey',/Codex[^。\n]{0,16}API Key/i],
  ['claudeCodeApiKey',/Claude Code[^。\n]{0,16}API Key/i],
  ['codeBuddyApiKey',/CodeBuddy[^。\n]{0,20}(API Key|Key|凭据)|拒绝了你的 API Key/i],
  ['gitSshKey',/Git SSH (?:密钥|私钥)/i],
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
// renderAll() repaints every page, so only the active 访问密钥 page may fetch
// the token list; Bearer sessions get 401 there and would poison the state.
function renderGuide(openCreate=false){if($('#accessBody')&&accessViewActive())renderAccessTokens(openCreate)}
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
function accessTokenSetupHTML(token){
  const clients = [['cursor','Cursor'], ['claude','Claude Code'], ['codebuddy','CodeBuddy'], ['codex','Codex']];
  return `<section class="token-reveal" aria-labelledby="token-setup-title">
    <div class="token-setup-layout">
      <div class="token-setup-intro">
        <div class="token-created">${huiIcon('check')}访问密钥已创建</div>
        <h2 id="token-setup-title" tabindex="-1">连接 LoopForge 平台 MCP</h2>
        <p>接入后，可以在 AI 对话里查询项目、需求和运行进度。</p>
        <fieldset class="token-client-picker">
          <legend>选择 AI 客户端</legend>
          <div class="token-client-options">
            ${clients.map(([id,label]) => `<label class="token-client-choice">
              <input type="radio" name="token-client" data-native data-revealed-client="${id}" value="${id}">
              <span class="token-client-tile">
                <img class="token-client-icon" data-client="${id}" src="/images/credentials/${id}.svg" width="20" height="20" alt="">
                <span>${label}</span>
                <span class="token-client-indicator" aria-hidden="true"></span>
              </span>
            </label>`).join('')}
          </div>
        </fieldset>
      </div>
      <div class="token-setup-action">
        <div class="token-connection" aria-hidden="true">
          <span class="token-connection-mark"><img src="/images/loopforge-logo.png" width="28" height="28" alt=""></span>
          <span class="token-connection-link">${huiIcon('connector')}</span>
          <span class="token-connection-mark"><img id="token-connection-client" class="token-client-icon" width="23" height="23" alt=""></span>
        </div>
        <div class="token-paste" aria-live="polite">
          <h3 id="token-paste-title"></h3>
          <p id="token-paste-hint"></p>
        </div>
        <button type="button" class="button primary" data-copy-revealed-prompt>${huiIcon('copy')}复制 MCP 接入提示词</button>
        <p class="token-copy-note">${huiIcon('lock')}已包含接入地址和访问密钥</p>
      </div>
    </div>
    <div class="token-reveal-footer">
      <details class="token-key-info">
        <summary>
          ${huiIcon('key')}<span>查看或复制访问密钥</span>${huiIcon('chevron-down','token-key-chevron')}
          <small>仅显示一次</small>
        </summary>
        <div class="token-key-controls">
          <code>${escapeHTML(token)}</code>
          <button type="button" class="button ghost small" data-copy-revealed>仅复制密钥</button>
          <p>手动配置 MCP 或调用 REST API 时可使用此密钥。</p>
        </div>
      </details>
      <button type="button" class="button ghost small" data-close-revealed>完成</button>
    </div>
  </section>`;
}
function bindAccessTokenSetup(panel,token){
  const base = getPublicBaseURL();
  const clients = [...panel.querySelectorAll('[data-revealed-client]')];
  const copyPrompt = panel.querySelector('[data-copy-revealed-prompt]');
  const clientIcon = panel.querySelector('#token-connection-client');
  const title = panel.querySelector('#token-paste-title');
  const hint = panel.querySelector('#token-paste-hint');
  function selectClient(choice){
    const id = choice.value;
    choice.checked = true;
    state.accessClient = id;
    const spec = accessMcpClientSpec(id,base,token);
    clientIcon.src = choice.closest('label').querySelector('img').src;
    clientIcon.dataset.client = id;
    title.textContent = window.lfT(`在 ${spec.label} 中配置`, `Set up in ${spec.label}`);
    hint.textContent = window.lfT(
      `将提示词粘贴到你自己的 ${spec.label} 对话中并发送，助手会配置并验证连接。`,
      `Paste the prompt into your own ${spec.label} chat and send it. Your assistant will set up and verify the connection.`
    );
    copyPrompt.onclick = () => copyText(accessMcpAgentPrompt(spec,base,token),window.lfT(
      `已复制，请粘贴到 ${spec.label} 对话中并发送`,
      `Prompt copied. Paste it into your ${spec.label} chat and send it.`
    ));
  }
  clients.forEach(choice => choice.onchange = () => selectClient(choice));
  selectClient(clients.find(choice => choice.value === state.accessClient) || clients[0]);
  panel.querySelector('[data-copy-revealed]').onclick = () => copyText(token,'访问密钥已复制');
  panel.querySelector('[data-close-revealed]').onclick = () => {
    state.revealedToken = '';
    renderAccessTokens();
    $('#accessTokenForm input')?.focus();
  };
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
  const reveal = state.revealedToken ? accessTokenSetupHTML(state.revealedToken) : '';
  const now = Date.now();
  const rows = tokens.map(t => {
    const revoked = !!t.revokedAt;
    const expired = t.expiresAt && Date.parse(t.expiresAt) <= now;
    const expiry = t.expiresAt ? `有效至 ${new Date(t.expiresAt).toLocaleDateString()}` : '长期有效';
    return `<div class="token-row${revoked || expired ? ' revoked' : ''}">
      <span class="token-row-icon">${huiIcon('key')}</span>
      <div class="token-row-main"><b>${escapeHTML(t.name)}</b><code>${escapeHTML(t.prefix)}••••••••</code></div>
      <div class="token-meta">创建 ${relativeTime(t.createdAt)} · ${t.lastUsedAt ? `最近使用 ${relativeTime(t.lastUsedAt)}` : '尚未使用'} · ${revoked ? '已撤销' : expired ? '已过期' : expiry}</div>
      ${revoked || expired ? '' : `<button type="button" class="button danger-outline small" data-revoke-token="${escapeHTML(t.id)}">撤销</button>`}
    </div>`;
  }).join('');
  const activeCount = tokens.filter(t => !t.revokedAt && (!t.expiresAt || Date.parse(t.expiresAt) > now)).length;
  const form = `<form id="accessTokenForm" class="token-create-form">
    <label>密钥名称<input class="input" name="name" required maxlength="128" placeholder="例如：我的 Cursor"></label>
    <label>有效期<select class="select" name="expiresInDays">
      <option value="30">30 天</option><option value="90" selected>90 天</option>
      <option value="180">180 天</option><option value="365">365 天</option>
    </select></label>
    <button type="submit" class="button ${reveal ? 'ghost' : 'primary'}">创建访问密钥</button>
  </form>`;
  body.innerHTML = `${reveal}
    <section class="token-manager" aria-labelledby="token-list-title">
      ${reveal ? '' : form}
      <div class="token-list-heading">
        <h2 id="token-list-title">我的访问密钥</h2>
        <span>${activeCount} 个可用</span>
      </div>
      <div class="token-list">${rows || '<div class="token-empty">还没有访问密钥，创建后即可接入 REST API 和 MCP。</div>'}</div>
      ${reveal ? `<details class="token-create-more"><summary class="button ghost small">${huiIcon('plus')}创建新密钥</summary>${form}</details>` : ''}
    </section>`;
  if(reveal) bindAccessTokenSetup(body.querySelector('.token-reveal'),state.revealedToken);
  body.querySelector('#accessTokenForm')?.addEventListener('submit',createPersonalToken);
  body.querySelectorAll('[data-revoke-token]').forEach(b=>b.onclick=()=>revokePersonalToken(b.dataset.revokeToken));
  window.AgentFlowHarnessIcons?.hydrate(body);
}
async function createPersonalToken(e){
  e.preventDefault();const form=e.currentTarget;
  try{
    const result = await api('/api/auth/tokens',{method:'POST',body:JSON.stringify({name:form.elements.name.value.trim(),expiresInDays:Number(form.elements.expiresInDays.value)})});
    state.revealedToken = result.token;
    await loadAccessTokens();
    if(accessViewActive()) $('#token-setup-title')?.focus();
    toast('访问密钥已创建','ok');
  }catch(err){toast(err.message,'err')}
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
