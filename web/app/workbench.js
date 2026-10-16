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

function credentialClearAction(name,configured,label='API Key'){
  return `<div class="credential-actions"><button type="button" class="button small credential-clear" data-clear-credential="${name}" data-clear-label="${label}" data-pending-clear="false">清除 ${label}</button><span data-credential-status="${name}" data-configured="${configured?'true':'false'}" role="status">${configured?'当前已配置':'尚未配置'}</span></div>`;
}
function bindCredentialClearButtons(form){
  form.querySelectorAll('[data-clear-credential]').forEach(button=>{
    button.onclick=()=>{
      if(form.querySelector('button[type="submit"]').disabled)return;
      const name=button.dataset.clearCredential,input=form.elements[name],status=form.querySelector(`[data-credential-status="${name}"]`),pending=button.dataset.pendingClear!=='true';
      button.dataset.pendingClear=String(pending);
      button.textContent=pending?'撤销清除':`清除 ${button.dataset.clearLabel}`;
      input.disabled=pending;
      status.textContent=pending?'待清除，保存后生效':status.dataset.configured==='true'?'当前已配置':'尚未配置';
    };
  });
}
function credentialClearPending(form,name){return form.querySelector(`[data-clear-credential="${name}"]`).dataset.pendingClear==='true'}
function credentialBadge(configured){return `<span class="credential-badge ${configured?'is-ready':'is-empty'}">${configured?'已配置':'未配置'}</span>`}
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
  host.innerHTML=`<section class="personal-settings-card personal-account-card"><div class="personal-account-avatar">${userAvatarHTML(user, 150)}</div><div><span class="credential-eyebrow">当前账号</span><h2>${escapeHTML(userDisplay(user))}</h2><p>凭证按用户隔离，仅在你负责的任务运行时注入。</p></div></section><section class="personal-settings-card credential-settings-card"><form id="userCredentialForm" class="upp-credentials">
        <header class="credential-settings-head"><div><span class="credential-eyebrow">凭证</span><h2>个人凭证</h2><p>SOP 使用需求责任人（未指定时为项目 Owner）的个人凭证。</p></div><div class="credential-security-note"><span>⌁</span>任务级注入，不写入 Agent 资产</div></header>
        <div class="credential-grid">
        <fieldset class="provider-credentials"><legend><span class="credential-card-logo"><img src="/images/credentials/gongfeng.svg" alt=""></span><span>代码仓库<small>GitHub 访问</small></span>${credentialBadge(storedUser.gitTokenConfigured)}</legend>
          <div class="upp-alert-warn git-token-pat-warning"${storedUser.gitTokenConfigured?' hidden':''}>请填写 GitHub 个人访问令牌（PAT），为目标仓库授予 Contents 和 Pull requests 的读写权限。</div>
          <p class="credential-description">用于克隆私有仓库、创建任务分支及推送交付产物。</p>
          <label>GitHub Git Token<input name="gitToken" type="password" autocomplete="new-password" placeholder="${storedUser.gitTokenConfigured?'已配置，留空则不改':'请输入个人访问令牌（PAT）'}"></label>
          ${credentialClearAction('gitToken',storedUser.gitTokenConfigured,'Git Token')}
          <a class="credential-help-link" href="https://github.com/settings/tokens" target="_blank" rel="noopener noreferrer">前往GitHub 申请个人访问令牌 ↗</a>
          <div class="git-token-verify-row"><button type="button" class="button ghost small" id="verifyGitToken">校验 Token 有效性</button><span id="gitTokenVerifyStatus" class="git-token-verify-status"></span></div>
        </fieldset>
        <fieldset class="provider-credentials"><legend><span class="credential-card-logo"><img src="/images/credentials/codebuddy.svg" alt=""></span><span>CodeBuddy<small>模型与执行</small></span>${credentialBadge(storedUser.codeBuddyApiKeyConfigured)}</legend>
          <p class="credential-description">用于调用 CodeBuddy 执行 Agent 任务；网络环境按账号所在环境配置。</p>
          <label>CodeBuddy API Key<input name="codeBuddyApiKey" type="password" autocomplete="new-password" placeholder="${storedUser.codeBuddyApiKeyConfigured?'已配置，留空则不改':'请输入个人 ck_ key'}"></label>
          ${credentialClearAction('codeBuddyApiKey',storedUser.codeBuddyApiKeyConfigured)}
          <label>CodeBuddy 网络环境<input name="codeBuddyInternetEnvironment" value="${escapeHTML(storedUser.codeBuddyInternetEnvironment||'')}" placeholder="国际站留空，国内站 internal，iOA 填 ioa"></label>
          <a class="credential-help-link" href="https://www.codebuddy.ai/" target="_blank" rel="noopener noreferrer">前往 CodeBuddy 获取 API Key ↗</a>
        </fieldset>

        <fieldset class="provider-credentials"><legend><span class="credential-card-logo monochrome"><img src="/images/credentials/codex.svg" alt=""></span><span>Codex<small>Responses API</small></span>${credentialBadge(storedUser.codexApiKeyConfigured)}</legend>
          <p class="credential-description">用于调用 OpenAI Responses API 兼容服务；具体模型名称在角色定义中设置。</p>
          <label>Codex API Key<input name="codexApiKey" type="password" autocomplete="new-password" placeholder="${storedUser.codexApiKeyConfigured?'已配置，留空则不改':'请输入 Codex API Key'}"></label>
          ${credentialClearAction('codexApiKey',storedUser.codexApiKeyConfigured)}
          <label>Codex Base URL<input name="codexBaseUrl" type="url" value="${escapeHTML(storedUser.codexBaseUrl||'')}" placeholder="https://api.openai.com/v1"></label>
          <small>填写提供商地址；留空使用官方地址。</small>
        </fieldset>
        <fieldset class="provider-credentials"><legend><span class="credential-card-logo monochrome"><img src="/images/credentials/claude.svg" alt=""></span><span>Claude Code<small>Messages API</small></span>${credentialBadge(storedUser.claudeCodeApiKeyConfigured)}</legend>
          <p class="credential-description">用于调用 Anthropic Messages API 兼容服务，支持自定义服务地址。</p>
          <label>Claude Code API Key<input name="claudeCodeApiKey" type="password" autocomplete="new-password" placeholder="${storedUser.claudeCodeApiKeyConfigured?'已配置，留空则不改':'请输入 Claude Code API Key'}"></label>
          ${credentialClearAction('claudeCodeApiKey',storedUser.claudeCodeApiKeyConfigured)}
          <label>Claude Code Base URL<input name="claudeCodeBaseUrl" type="url" value="${escapeHTML(storedUser.claudeCodeBaseUrl||'')}" placeholder="https://api.anthropic.com"></label>
          <small>填写提供商地址；留空使用官方地址。</small>
        </fieldset>
        </div>
        <footer class="credential-savebar"><div><b>保存后对下一次任务生效</b><span>留空不会覆盖已经配置的密钥。</span></div><button class="button primary" type="submit">保存个人凭证</button></footer>
      </form></section><section class="personal-settings-card" data-personal-bot-bindings><h2>企业微信账号</h2><p>关联后，Bot 使用你的平台身份和个人凭据执行指令。请在企业微信向 Bot 发送“绑定账号”开始关联。</p><div data-wecom-bindings>正在读取绑定…</div></section>`;
  bindCredentialClearButtons(host.querySelector('#userCredentialForm'));
  const verifyBtn=host.querySelector('#verifyGitToken');
  // 状态文案节点每次重渲染后都会重建，统一现查现用，避免写入游离 DOM。
  const setStatus=text=>{const el=$('#gitTokenVerifyStatus');if(el)el.textContent=text};
  // The token-type warning only matters while no working token is stored.
  const showPatWarning=show=>{const el=$('.git-token-pat-warning');if(el)el.hidden=!show};
  const renderVerifyStatus=r=>{
    state.gitTokenVerify={userId:user.id,result:r};
    if(r.error){setStatus(`✗ ${r.error}`);showPatWarning(true);if(state.gitTokenAnnounceVerify){state.gitTokenAnnounceVerify=false;toast(r.error)}return}
    if(!r.valid){setStatus(`✗ 令牌有效但缺少权限：${(r.missing||[]).join('、')}，请在工蜂补充勾选`);showPatWarning(true);if(state.gitTokenAnnounceVerify){state.gitTokenAnnounceVerify=false;toast(r.error||'Git Token 校验未通过，请检查权限范围')}return}
    showPatWarning(false);
    setStatus(r.scopes&&r.scopes.length?`✓ 校验通过（${(r.scopes||[]).join(', ')}）`:'✓ 令牌有效');
    if(state.gitTokenAnnounceVerify){state.gitTokenAnnounceVerify=false}
  };
  async function verifyGitToken(){
    if(verifyBtn)verifyBtn.disabled=true;
    setStatus('正在校验…');
    try{renderVerifyStatus(await api(`/api/users/${user.id}/git-token/verify`,{silent:true}))}
    catch(err){renderVerifyStatus({error:err.message})}
    finally{if(verifyBtn)verifyBtn.disabled=false}
  }
  if(verifyBtn)verifyBtn.onclick=verifyGitToken;
  if(storedUser.gitTokenConfigured){
    if(state.gitTokenVerify?.userId===user.id)renderVerifyStatus(state.gitTokenVerify.result);
    else verifyGitToken();
  }
  host.querySelector('#userCredentialForm').onsubmit=async e=>{
    e.preventDefault();const f=e.currentTarget,button=f.querySelector('button[type="submit"]');button.disabled=true;
    const payload={gitToken:f.elements.gitToken.value.trim(),gitTokenClear:credentialClearPending(f,'gitToken'),codeBuddyApiKey:f.elements.codeBuddyApiKey.value.trim(),codeBuddyApiKeyClear:credentialClearPending(f,'codeBuddyApiKey'),codeBuddyInternetEnvironment:f.elements.codeBuddyInternetEnvironment.value.trim(),codexApiKey:f.elements.codexApiKey.value.trim(),codexApiKeyClear:credentialClearPending(f,'codexApiKey'),codexBaseUrl:f.elements.codexBaseUrl.value.trim(),claudeCodeApiKey:f.elements.claudeCodeApiKey.value.trim(),claudeCodeApiKeyClear:credentialClearPending(f,'claudeCodeApiKey'),claudeCodeBaseUrl:f.elements.claudeCodeBaseUrl.value.trim()};
    try{if(payload.gitToken||payload.gitTokenClear)state.gitTokenVerify=null;if(payload.gitToken)state.gitTokenAnnounceVerify=true;const saved=await api(`/api/users/${user.id}/credentials`,{method:'PUT',body:JSON.stringify(payload)}),index=state.data.users.findIndex(u=>Number(u.id)===Number(saved.id));if(index>=0)state.data.users[index]=saved;else state.data.users.push(saved);renderPersonalSettings();window.AgentFlowAssistant?.sync();toast('个人凭据已保存')}
    catch(err){toast(err.message);button.disabled=false}
  };
  const bindingSection=host.querySelector('[data-personal-bot-bindings]');if(bindingSection)loadPersonalBotBindings(bindingSection);
}
// 谁能改通用模板是平台级权限，不属于任何一个项目，也不属于个人凭据，
// 平台管理也容纳业务域管理；按各自授权展示入口与 tab。
function renderPlatformNav(){
  const group=$('#platformNavGroup');if(!group)return;
  group.hidden=!canAccessPlatformManagement();
  const platformItem=group.querySelector('[data-view="platform"]');
  if(platformItem)platformItem.hidden=!isPlatformAdmin();
}
function renderPlatformAdminPanel(){
  const host=$('#platformAdminPanel');if(!host)return;
  if(!isPlatformAdmin()){host.innerHTML='';return}
  host.innerHTML=`
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
            <span class="credential-eyebrow">平台权限</span>
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
  `;
  loadPlatformAdmins(host);
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
          <p class="upp-guest-desc">当前以访客身份访问平台。登录后可在左侧「个人设置」配置GitHub Git Token 和 CodeBuddy API Key。</p>
        `}
      </div>
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
      <button type="button" id="openPersonalSettings" class="button ghost">个人设置 · 执行凭据</button>
 <div class="upp-divider"></div>
      <div class="upp-footer">
        <button id="uppReAuthBtn" class="button small ghost" type="button" title="使用其他账号登录">切换账号</button>
        <button id="uppLogoutBtn" class="button small ghost upp-btn-danger" type="button" title="退出当前登录">退出登录</button>
      </div>
    `;
    popover.querySelector('#openPersonalSettings').onclick=()=>{ $('#userProfileWrap').classList.remove('is-open'); switchView('settings') };
    const reAuthBtn = popover.querySelector('#uppReAuthBtn');
    if (reAuthBtn) {
      reAuthBtn.onclick = () => {
        try { sessionStorage.removeItem('agentflow_auth_err'); } catch (_) {}
        const target = state.loginUrl || '/login.html';
        window.location.href = target;
      };
    }
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
  return `<img class="user-avatar-img ${extraClass}" src="${photoUrl}" alt="${escapeHTML(user.chineseName || user.loginName)}" data-avatar-fallback><span class="user-avatar-text" style="display:none">${escapeHTML(initial)}</span>`;
}

function renderUserBadge(){
  const badge=$('#userBadge'), nameEl=$('#userName'), avatarEl=$('#userAvatarText');
  if(!badge||!nameEl||!avatarEl) return;
  const user=state.currentUser;
  const isGuest = !user || user.isGuest;
  const displayName=user?.chineseName||user?.loginName||'访客';
  nameEl.textContent=displayName;
  avatarEl.innerHTML=userAvatarHTML(user, 100);
  if(isGuest){
    badge.classList.add('is-guest');
    let authErr = null;
    try { authErr = sessionStorage.getItem('agentflow_auth_err'); } catch (_) {}
    badge.title=authErr ? '认证换票未通过，点击或悬停查看个人信息' : '当前为访客身份，点击或悬停查看个人信息';
  }else{
    badge.classList.remove('is-guest');
    badge.title=`${displayName} (${user.loginName||user.staffId}) - 点击或悬停查看个人信息`;
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
    {label:'需要介入',value:attention.length,foot:'澄清未达标、失败或已中断',color:'#c63c4b',filter:'attention'},
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
  const feed=logs.slice(0,8).map(l=>`<div class="tl-item"><div><b>${l.direction==='pull'?'拉取':'推送'} · ${l.source==='gongfeng'?'工蜂':l.source==='tapd'?'TAPD':l.source}</b> <span class="muted">${escapeHTML(l.message||l.action)}</span></div><div class="muted" style="font-size:10.5px;margin-top:2px">${new Date(l.startedAt).toLocaleString()} · ${l.status==='success'?'成功':`失败 ${escapeHTML(l.message||'')}`}</div></div>`).join('');
  $('#dashFeed').innerHTML=feed||'<div class="tl-item"><div class="muted">尚未配置外部同步。可在「需求列表」里同步工蜂 / TAPD。</div></div>';
}


async function loadPersonalBotBindings(section){
 const list=section.querySelector('[data-wecom-bindings]');
 try{const rows=await api('/api/bot-bindings',{silent:true});if(!section.isConnected)return;list.innerHTML=rows.length?rows.map(row=>`<div class="bot-list-head"><div><strong>企业微信账号 ${escapeHTML(row.senderId)}</strong><p class="muted">Bot：${escapeHTML(row.botId)} · ${escapeHTML(new Date(row.updatedAt).toLocaleString())}</p></div><button class="button ghost small" data-unbind="${escapeHTML(row.id)}">解除绑定</button></div>`).join(''):'<p class="muted">尚未关联企业微信账号。</p>';list.querySelectorAll('[data-unbind]').forEach(button=>{button.onclick=async()=>{if(!confirm('解除后，该企微账号需要重新关联才能操作平台。确认解除？'))return;button.disabled=true;try{await api('/api/bot-bindings/'+encodeURIComponent(button.dataset.unbind),{method:'DELETE'});await loadPersonalBotBindings(section);toast('已解除绑定','ok')}catch(error){button.disabled=false;toast(error.message,'err')}}});}catch(error){if(section.isConnected)list.textContent=error.message;}
}
