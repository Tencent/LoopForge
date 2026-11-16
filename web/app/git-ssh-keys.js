let gitSSHKeyCache = null;

function loadGitSSHKeys(refresh = false) {
  const userId = state.currentUser?.id;
  if (!userId || state.currentUser.isGuest) return Promise.resolve([]);
  if (!gitSSHKeyCache || gitSSHKeyCache.userId !== userId) gitSSHKeyCache = {userId, keys:null, pending:null};
  const cache = gitSSHKeyCache;
  if (cache.pending) return cache.pending;
  if (!refresh && cache.keys) return Promise.resolve(cache.keys);
  cache.pending = api(`/api/users/${encodeURIComponent(userId)}/git-ssh-keys`, {silent:true})
    .then(keys => cache.keys = Array.isArray(keys) ? keys : [])
    .finally(() => { cache.pending = null; });
  return cache.pending;
}

async function renderGitSSHMissingHint(element, remote) {
  if (!element) return;
  const version = element.sshHintVersion = (element.sshHintVersion || 0) + 1;
  const userId = state.currentUser?.id;
  element.hidden = true;
  element.textContent = '';
  if (remote?.provider !== 'ssh') return;
  try {
    const keys = await loadGitSSHKeys();
    if (element.sshHintVersion !== version || state.currentUser?.id !== userId) return;
    if (keys.some(key => globalThis.LoopForgeGitSSHHost(key.host) === remote.host)) return;
    element.textContent = window.lfT?.(
      `尚未配置 ${remote.host} 的 SSH 密钥，可稍后在「个人配置 → Git SSH 密钥」添加。`,
      `No SSH key configured for ${remote.host}. You can add one later in Personal settings → Git SSH keys.`,
    ) || `尚未配置 ${remote.host} 的 SSH 密钥，可稍后在「个人配置 → Git SSH 密钥」添加。`;
  } catch {
    if (element.sshHintVersion !== version || state.currentUser?.id !== userId) return;
    element.textContent = '无法读取 SSH 密钥配置，请稍后重试。';
  }
  element.hidden = false;
}

function refreshGitSSHWarnings() {
  if (repoDrawerOpen()) syncRepoDrawerDetect();
  if ($('#repositoryModal')?.classList.contains('open')) renderRepositoryCredentialHint();
}

window.addEventListener('focus', () => {
  gitSSHKeyCache = null;
  refreshGitSSHWarnings();
});

function openGitSSHKeys(remote = '') {
  const user = state.currentUser;
  if (!user?.id || user.isGuest) return;

  const previous = $('#gitSSHKeyDialog');
  if (previous?.open) return;
  const opener = document.activeElement;
  const repository = globalThis.LoopForgeGitSSH?.(remote);
  const endpoint = `/api/users/${encodeURIComponent(user.id)}/git-ssh-keys`;
  let keys = [];
  let editing = null;
  let deleting = '';
  let busy = false;

  const dialog = document.createElement('dialog');
  dialog.id = 'gitSSHKeyDialog';
  dialog.className = 'git-ssh-dialog';
  dialog.setAttribute('aria-labelledby', 'gitSSHKeyTitle');
  dialog.innerHTML = `
    <header class="git-ssh-dialog-head">
      <h2 id="gitSSHKeyTitle">Git SSH 密钥</h2>
      <button type="button" class="credential-btn ghost icon" data-ssh-key-close aria-label="关闭">${credentialGlyph('close', 16)}</button>
    </header>
    <p class="git-ssh-description">每个 SSH 主机配置一把个人密钥，同一主机的仓库自动共用。</p>
    <p class="git-ssh-status" role="status">正在读取密钥…</p>
    <div class="git-ssh-key-list"></div>
    <button type="button" class="credential-btn primary" data-ssh-key-add disabled>添加密钥</button>
    <form class="git-ssh-key-form" hidden>
      <h3></h3>
      ${credentialFieldHTML({label:'密钥名称', name:'name', type:'text', placeholder:'例如 GitLab 或 Gitea', hint:null})}
      ${credentialFieldHTML({label:'SSH 主机', name:'host', type:'text', placeholder:'gitlab.com 或 git.example.org:2222', hint:'不填写端口时使用 22。'})}
      ${credentialFieldHTML({label:'SSH 私钥', name:'privateKey', multiline:true, placeholder:'粘贴完整的 OpenSSH 或 PEM 私钥', hint:'把对应公钥添加到代码托管账号，并授予仓库访问权限。推送需要写权限。'})}
      <footer class="git-ssh-form-actions">
        <button type="button" class="credential-btn secondary" data-ssh-key-cancel>取消</button>
        <button type="submit" class="credential-btn primary">保存密钥</button>
      </footer>
    </form>`;

  const list = dialog.querySelector('.git-ssh-key-list');
  const status = dialog.querySelector('.git-ssh-status');
  const add = dialog.querySelector('[data-ssh-key-add]');
  const form = dialog.querySelector('form');
  const name = form.elements.namedItem('name');
  const host = form.elements.namedItem('host');
  const privateKey = form.elements.namedItem('privateKey');
  name.maxLength = 80;
  host.maxLength = 2048;
  name.required = host.required = true;

  function setStatus(message = '', error = false) {
    status.textContent = message;
    status.classList.toggle('is-error', error);
    status.hidden = !message;
  }

  function renderList() {
    list.innerHTML = keys.map(key => {
      const id = escapeHTML(key.id);
      return `<div class="git-ssh-key-row">
        <div class="git-ssh-key-summary"><b>${escapeHTML(key.name)}</b><small><code>${escapeHTML(key.host)}</code></small></div>
        ${deleting === key.id
          ? `<span>删除这把密钥？</span><button type="button" class="credential-btn ghost" data-ssh-key-keep>取消</button><button type="button" class="credential-btn danger-solid" data-ssh-key-remove="${id}">确认删除</button>`
          : `<button type="button" class="credential-btn ghost" data-ssh-key-edit="${id}">编辑</button><button type="button" class="credential-btn ghost" data-ssh-key-delete="${id}">删除</button>`}
      </div>`;
    }).join('');
  }

  function openForm(key, prefill) {
    editing = key;
    deleting = '';
    renderList();
    form.reset();
    form.hidden = false;
    add.hidden = true;
    form.querySelector('h3').textContent = key ? '编辑密钥' : '添加密钥';
    name.value = key?.name || prefill?.host || '';
    host.value = key?.host || prefill?.host || '';
    host.readOnly = !!key;
    privateKey.required = !key;
    privateKey.placeholder = key ? '留空保留现有私钥' : '粘贴完整的 OpenSSH 或 PEM 私钥';
    (prefill ? privateKey : name).focus();
  }

  function closeForm() {
    form.reset();
    form.hidden = true;
    add.hidden = false;
    editing = null;
    add.focus();
  }

  async function request(method, body, id) {
    if (busy) return;
    busy = true;
    dialog.setAttribute('aria-busy', 'true');
    const save = form.querySelector('button[type="submit"]');
    save.disabled = true;
    setStatus(method === 'PUT' ? '正在保存…' : '正在删除…');
    try {
      keys = await api(id ? `${endpoint}/${encodeURIComponent(id)}` : endpoint, {
        method, silent:true, ...(body ? {body:JSON.stringify(body)} : {}),
      });
      gitSSHKeyCache = {userId:user.id, keys, pending:null};
      storeCredentialUser({...credentialUser(), id:user.id, gitSshKeyConfigured:keys.length > 0});
      renderCredentialPanel();
      notifyCredentialsChanged();
      refreshGitSSHWarnings();
      deleting = '';
      renderList();
      if (method === 'PUT') closeForm();
      setStatus(method === 'PUT' ? '密钥已保存' : '密钥已删除');
    } catch (error) {
      setStatus(error.message, true);
    } finally {
      busy = false;
      save.disabled = false;
      dialog.removeAttribute('aria-busy');
    }
  }

  dialog.onclick = event => {
    const button = event.target.closest('button');
    if (!button || busy) return;
    const action = button.dataset;
    if ('sshKeyClose' in action) return dialog.close();
    if ('sshKeyCancel' in action) return closeForm();
    if ('sshKeyAdd' in action) return openForm(null, null);
    if (action.sshKeyEdit) return openForm(keys.find(key => key.id === action.sshKeyEdit), null);
    if (action.sshKeyRemove) return request('DELETE', null, action.sshKeyRemove);
    if (action.sshKeyDelete || 'sshKeyKeep' in action) {
      deleting = action.sshKeyDelete || '';
      renderList();
      const confirmation = list.querySelector('[data-ssh-key-remove]');
      (confirmation || add).focus();
    }
  };
  form.onsubmit = event => {
    event.preventDefault();
    return request('PUT', {id:editing?.id, name:name.value, host:host.value, privateKey:privateKey.value});
  };
  dialog.addEventListener('keydown', event => {
    if (event.key === 'Escape' || event.metaKey || event.ctrlKey) event.stopPropagation();
  });
  dialog.addEventListener('cancel', event => { if (busy) event.preventDefault(); });
  dialog.addEventListener('close', () => {
    form.reset();
    dialog.remove();
    if (opener?.isConnected) opener.focus({preventScroll:true});
    else $('#credentialPanel [data-credential-row="gitSshKey"] [data-credential-act="setup"]')?.focus({preventScroll:true});
  });
  document.body.append(dialog);
  dialog.showModal();
  hydrateCredentialIcons(dialog);
  loadGitSSHKeys(true).then(items => {
    if (!dialog.open) return;
    keys = items;
    add.disabled = false;
    setStatus(keys.length ? '' : '还没有配置 Git SSH 密钥');
    renderList();
    if (repository) {
      const existing = keys.find(key => globalThis.LoopForgeGitSSHHost(key.host) === repository.host);
      openForm(existing || null, repository);
    }
  }).catch(error => setStatus(error.message, true));
}
