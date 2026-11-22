const GIT_HTTP_PLATFORMS = [['github', 'GitHub / GitHub Enterprise'], ['gitlab', 'GitLab'], ['other', '其他平台']];

// Mirrors gitwork.HTTPSHostScope closely enough to prefill the form; the server
// remains the authority on what a host scope is.
function gitHTTPHostScope(raw) {
  let host = String(raw || '').trim().toLowerCase().replace(/^https:\/\//, '').replace(/\/$/, '');
  if (host.endsWith(':443')) host = host.slice(0, -4);
  return host;
}

function gitHTTPDefaultUsername(platform) {
  return platform === 'gitlab' ? 'oauth2' : 'x-access-token';
}

function openGitHTTPCredentials(options = {}) {
  const user = state.currentUser;
  if (!user?.id || user.isGuest) return;

  const previous = $('#gitHTTPCredentialDialog');
  if (previous?.open) return;
  const opener = document.activeElement;
  const endpoint = `/api/users/${encodeURIComponent(user.id)}/git-http-credentials`;
  let creds = [];
  let editing = null;
  let deleting = '';
  let busy = false;

  const dialog = document.createElement('dialog');
  dialog.id = 'gitHTTPCredentialDialog';
  dialog.className = 'git-ssh-dialog git-http-dialog';
  dialog.setAttribute('aria-labelledby', 'gitHTTPCredentialTitle');
  dialog.innerHTML = `
    <header class="git-ssh-dialog-head">
      <h2 id="gitHTTPCredentialTitle">Git 访问令牌（按主机）</h2>
      <button type="button" class="credential-btn ghost icon" data-http-cred-close aria-label="关闭">${credentialGlyph('close', 16)}</button>
    </header>
    <p class="git-ssh-description">每个 HTTPS 主机配置一个个人访问令牌。平台与工作区访问该主机时，使用本轮责任人或发言人的凭据。</p>
    <p class="git-ssh-status" role="status">正在读取凭据…</p>
    <div class="git-ssh-key-list"></div>
    <button type="button" class="credential-btn primary" data-http-cred-add disabled>添加令牌</button>
    <form class="git-ssh-key-form" hidden>
      <h3></h3>
      ${credentialFieldHTML({label:'HTTPS 主机', name:'host', type:'text', placeholder:'github.com 或 gitlab.example.org:8443', hint:'只填主机名，可附带端口；令牌只提供给这台主机。'})}
      <label class="credential-field">
        <span>平台</span>
        <div class="credential-input is-plain">
          <select name="platform" class="git-http-select" aria-label="平台" aria-describedby="gitHTTPCredentialPlatformHint">
            ${GIT_HTTP_PLATFORMS.map(([value, label]) => `<option value="${value}">${escapeHTML(label)}</option>`).join('')}
          </select>
        </div>
        <small id="gitHTTPCredentialPlatformHint">GitHub 和 GitLab 支持 gh / glab 认证和 PR/MR 合并状态同步。其他平台也可由 Agent 创建 PR/MR，合并状态请到托管平台查看。</small>
      </label>
      ${credentialFieldHTML({label:'名称', name:'name', type:'text', placeholder:'例如 公司 GitLab', hint:null})}
      ${credentialFieldHTML({label:'Git 认证用户名', name:'username', type:'text', placeholder:'x-access-token', hint:'按托管平台要求填写；这是 HTTPS 认证用户名，不是提交作者名，通常无法从 Token 推导。'})}
      ${credentialFieldHTML({label:'个人访问令牌', name:'token', placeholder:'粘贴个人访问令牌', hint:'保存后不再显示；编辑时留空保留现有令牌。'})}
      <footer class="git-ssh-form-actions">
        <button type="button" class="credential-btn secondary" data-http-cred-cancel>取消</button>
        <button type="submit" class="credential-btn primary">保存令牌</button>
      </footer>
    </form>`;

  const list = dialog.querySelector('.git-ssh-key-list');
  const status = dialog.querySelector('.git-ssh-status');
  const add = dialog.querySelector('[data-http-cred-add]');
  const form = dialog.querySelector('form');
  const field = key => form.elements.namedItem(key);
  const [host, platform, name, username, token] = ['host', 'platform', 'name', 'username', 'token'].map(field);
  host.maxLength = 255;
  name.maxLength = 80;
  username.maxLength = 128;
  token.maxLength = 4096;
  host.required = true;

  function setStatus(message = '', error = false) {
    status.textContent = message;
    status.classList.toggle('is-error', error);
    status.hidden = !message;
  }

  function credentialSummary(cred) {
    const platformLabel = (GIT_HTTP_PLATFORMS.find(([value]) => value === cred.platform) || [, cred.platform])[1];
    const tags = [platformLabel, cred.managed ? '平台托管' : ''].filter(Boolean);
    return `<div class="git-ssh-key-summary"><b>${escapeHTML(cred.name)}</b><small><code>${escapeHTML(cred.host)}</code> · ${tags.map(escapeHTML).join(' · ')}</small></div>`;
  }

  function renderList() {
    list.innerHTML = creds.map(cred => {
      const id = escapeHTML(cred.id);
      const verify = cred.platform !== 'other' || cred.managed ? `<button type="button" class="credential-btn ghost" data-http-cred-verify="${id}">校验令牌</button>` : '';
      const actions = cred.managed
        ? verify
        : deleting === cred.id
          ? `<span>删除这条令牌？</span><button type="button" class="credential-btn ghost" data-http-cred-keep>取消</button><button type="button" class="credential-btn danger-solid" data-http-cred-remove="${id}">确认删除</button>`
          : `${verify}<button type="button" class="credential-btn ghost" data-http-cred-edit="${id}">编辑</button><button type="button" class="credential-btn ghost" data-http-cred-delete="${id}">删除</button>`;
      return `<div class="git-ssh-key-row">${credentialSummary(cred)}${actions}</div>`;
    }).join('');
  }

  function openForm(cred) {
    editing = cred;
    deleting = '';
    renderList();
    form.reset();
    form.hidden = false;
    add.hidden = true;
    form.querySelector('h3').textContent = cred ? '编辑令牌' : '添加令牌';
    host.value = cred?.host || '';
    host.readOnly = !!cred;
    platform.value = cred?.platform || options.platform || 'github';
    name.value = cred?.name || '';
    name.placeholder = '例如 公司 GitLab';
    username.value = cred?.username || '';
    username.placeholder = gitHTTPDefaultUsername(platform.value);
    token.required = !cred;
    token.placeholder = cred ? '留空保留现有令牌' : '粘贴个人访问令牌';
    (cred ? name : host).focus();
  }

  function closeForm() {
    form.reset();
    form.hidden = true;
    add.hidden = false;
    editing = null;
    add.focus();
  }

  function setBusy(on) {
    busy = on;
    dialog.toggleAttribute('aria-busy', on);
    form.querySelector('button[type="submit"]').disabled = on;
  }

  async function request(method, body, id) {
    if (busy) return;
    setBusy(true);
    setStatus(method === 'PUT' ? '正在保存…' : '正在删除…');
    try {
      creds = await api(id ? `${endpoint}/${encodeURIComponent(id)}` : endpoint, {
        method, silent:true, ...(body ? {body:JSON.stringify(body)} : {}),
      });
      storeCredentialUser({...credentialUser(), id:user.id, gitHttpCredentialsConfigured:creds.length > 0, gitHubCredentialConfigured:creds.some(credential=>credential.host==='github.com')});
      renderCredentialGitGroup();
      notifyCredentialsChanged();
      deleting = '';
      renderList();
      if (method === 'PUT') closeForm();
      setStatus(method === 'PUT' ? '令牌已保存' : '令牌已删除');
    } catch (error) {
      setStatus(error.message, true);
    } finally {
      setBusy(false);
    }
  }

  async function verify(id) {
    if (busy) return;
    setBusy(true);
    setStatus('正在校验…');
    try {
      const result = await api(`${endpoint}/${encodeURIComponent(id)}/verify`, {method:'POST', silent:true});
      if (result?.valid) setStatus(result.login ? `令牌有效，账号 ${result.login}` : '令牌有效');
      else setStatus(result?.error || '令牌校验未通过', true);
    } catch (error) {
      setStatus(error.message, true);
    } finally {
      setBusy(false);
    }
  }

  dialog.onclick = event => {
    const button = event.target.closest('button');
    if (!button || busy) return;
    const action = button.dataset;
    if ('httpCredClose' in action) return dialog.close();
    if ('httpCredCancel' in action) return closeForm();
    if ('httpCredAdd' in action) return openForm(null);
    if (action.httpCredVerify) return verify(action.httpCredVerify);
    if (action.httpCredEdit) return openForm(creds.find(cred => cred.id === action.httpCredEdit));
    if (action.httpCredRemove) return request('DELETE', null, action.httpCredRemove);
    if (action.httpCredDelete || 'httpCredKeep' in action) {
      deleting = action.httpCredDelete || '';
      renderList();
      (list.querySelector('[data-http-cred-remove]') || add).focus();
    }
  };
  host.addEventListener('change', () => {
    if (editing) return;
    const scope = gitHTTPHostScope(host.value);
    if (scope === 'github.com') platform.value = 'github';
    else if (/(^|\.)gitlab\./.test(scope)) platform.value = 'gitlab';
    username.placeholder = gitHTTPDefaultUsername(platform.value);
    name.placeholder = scope || '例如 公司 GitLab';
  });
  platform.addEventListener('change', () => { username.placeholder = gitHTTPDefaultUsername(platform.value); });
  form.onsubmit = event => {
    event.preventDefault();
    const body = {
      host:host.value, platform:platform.value, name:name.value.trim() || gitHTTPHostScope(host.value),
      username:username.value.trim() || gitHTTPDefaultUsername(platform.value),
      token:token.value,
    };
    if (editing) body.id = editing.id;
    return request('PUT', body);
  };
  dialog.addEventListener('keydown', event => {
    if (event.key === 'Escape' || event.metaKey || event.ctrlKey) event.stopPropagation();
  });
  dialog.addEventListener('cancel', event => { if (busy) event.preventDefault(); });
  dialog.addEventListener('close', () => {
    form.reset();
    dialog.remove();
    (opener?.isConnected ? opener : $('#credentialPanel [data-credential-add-menu="platform"]'))?.focus({preventScroll:true});
  });
  document.body.append(dialog);
  dialog.showModal();
  hydrateCredentialIcons(dialog);
  api(endpoint, {silent:true}).then(items => {
    if (!dialog.open) return;
    creds = Array.isArray(items) ? items : [];
    add.disabled = false;
    setStatus(creds.length ? '' : '还没有配置 Git 访问令牌');
    renderList();
    const selected = creds.find(cred => cred.id === options.id);
    if (selected && !selected.managed) openForm(selected);
    else if (options.add) openForm(null);
  }).catch(error => setStatus(error.message, true));
}
