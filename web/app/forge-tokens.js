function openForgeTokens({id: initialID = '', add: addInitially = false} = {}) {
  const user = state.currentUser;
  if (!user?.id || user.isGuest || $('#forgeTokenDialog')?.open) return;
  const endpoint = '/api/users/' + encodeURIComponent(user.id) + '/forge-tokens';
  const opener = document.activeElement;
  let instances = [];
  let tokens = [];
  let deleting = '';
  let busy = true;

  const dialog = document.createElement('dialog');
  dialog.id = 'forgeTokenDialog';
  dialog.className = 'git-ssh-dialog';
  dialog.setAttribute('aria-labelledby', 'forgeTokenTitle');
  dialog.innerHTML = `
    <header class="git-ssh-dialog-head">
      <h2 id="forgeTokenTitle">Forgejo 访问令牌</h2>
      <button type="button" class="credential-btn ghost icon" data-forge-close aria-label="关闭">${credentialGlyph('close', 16)}</button>
    </header>
    <p class="git-ssh-description">${lfT(
      '在 Forgejo「设置 → 应用」生成令牌。校验身份需要 read:user，创建和更新 PR 需要 write:repository。',
      'Create a token in Forgejo Settings → Applications. Use read:user for identity checks and write:repository to create and update PRs.',
    )}</p>
    <p class="git-ssh-status" role="status">正在读取凭据…</p>
    <div class="git-ssh-key-list"></div>
    <button type="button" class="credential-btn primary" data-forge-add disabled>添加令牌</button>
    <form class="git-ssh-key-form" hidden>
      <h3></h3>
      <label class="credential-field">
        <span>Forgejo 实例</span>
        <div class="credential-input is-plain"><select name="instance" aria-label="Forgejo 实例" required></select></div>
      </label>
      ${credentialFieldHTML({label: '个人访问令牌', name: 'token', placeholder: '粘贴个人访问令牌', hint: null})}
      <footer class="git-ssh-form-actions">
        <button type="button" class="credential-btn secondary" data-forge-cancel>取消</button>
        <button type="submit" class="credential-btn primary">校验并保存</button>
      </footer>
    </form>`;

  const status = dialog.querySelector('[role="status"]');
  const list = dialog.querySelector('.git-ssh-key-list');
  const add = dialog.querySelector('[data-forge-add]');
  const form = dialog.querySelector('form');
  const select = form.elements.instance;
  const token = form.elements.token;
  const save = form.querySelector('[type="submit"]');
  token.required = true;
  token.maxLength = 512;

  function setStatus(message, error = false) {
    status.textContent = message;
    status.hidden = !message;
    status.classList.toggle('is-error', error);
  }

  function availableInstances() {
    return instances.filter(instance => !tokens.some(saved => saved.instance === instance));
  }

  function renderList() {
    list.innerHTML = tokens.map(saved => {
      const recordID = escapeHTML(saved.id);
      const actions = deleting === saved.id
        ? `<span>删除这条令牌？</span>
          <button type="button" class="credential-btn ghost" data-forge-keep>取消</button>
          <button type="button" class="credential-btn danger-solid" data-forge-remove="${recordID}">确认删除</button>`
        : `<button type="button" class="credential-btn ghost" data-forge-edit="${recordID}"${instances.includes(saved.instance) ? '' : ' disabled'}>编辑</button>
          <button type="button" class="credential-btn ghost" data-forge-delete="${recordID}">删除</button>`;
      return `<div class="git-ssh-key-row"><div class="git-ssh-key-summary"><b>${escapeHTML(saved.instance)}</b></div>${actions}</div>`;
    }).join('');
    add.disabled = busy || availableInstances().length === 0;
  }

  function openForm(saved) {
    const choices = saved ? [saved.instance] : availableInstances();
    if (!choices.length) return;
    form.reset();
    if (token.type === 'text') toggleCredentialSecret(form.querySelector('[data-credential-eye]'));
    deleting = '';
    renderList();
    form.querySelector('h3').textContent = saved ? '编辑令牌' : '添加令牌';
    select.replaceChildren(...choices.map(instance => new Option(instance, instance)));
    if (!saved) {
      const placeholder = new Option(lfT('选择实例', 'Select an instance'), '', true, true);
      placeholder.disabled = true;
      select.prepend(placeholder);
    }
    select.disabled = Boolean(saved);
    form.hidden = false;
    add.hidden = true;
    (saved ? token : select).focus();
  }

  function closeForm() {
    form.reset();
    form.hidden = true;
    add.hidden = false;
    (add.disabled ? dialog.querySelector('[data-forge-close]') : add).focus();
  }

  async function request(method, body, tokenID = '') {
    if (busy) return;
    busy = true;
    add.disabled = save.disabled = true;
    dialog.setAttribute('aria-busy', 'true');
    setStatus(method === 'PUT' ? '正在保存…' : '正在删除…');
    try {
      const path = tokenID ? endpoint + '/' + encodeURIComponent(tokenID) : endpoint;
      const result = await api(path, {method, silent: true, ...(body ? {body: JSON.stringify(body)} : {})});
      if (state.currentUser?.id !== user.id) return;
      instances = result.instances;
      tokens = result.tokens;
      storeCredentialUser({...credentialUser(), id: user.id, forgeTokenConfigured: tokens.length > 0});
      renderCredentialGitGroup();
      notifyCredentialsChanged();
      if (!dialog.open) return;
      deleting = '';
      busy = false;
      renderList();
      closeForm();
      setStatus(method === 'PUT' ? '令牌已保存' : '令牌已删除');
    } catch (error) {
      setStatus(error.message, true);
    } finally {
      busy = false;
      save.disabled = false;
      dialog.removeAttribute('aria-busy');
      if (dialog.open) add.disabled = availableInstances().length === 0;
    }
  }

  form.onsubmit = event => {
    event.preventDefault();
    if (!select.value || !token.value.trim()) return;
    return request('PUT', {instance: select.value, token: token.value.trim()});
  };

  dialog.onclick = event => {
    const button = event.target.closest('button');
    if (!button) return;
    if (button.hasAttribute('data-forge-close')) return dialog.close();
    if (button.hasAttribute('data-credential-eye')) return toggleCredentialSecret(button);
    if (busy) return;
    if (button.hasAttribute('data-forge-add')) return openForm();
    if (button.hasAttribute('data-forge-cancel')) return closeForm();
    if (button.dataset.forgeEdit) return openForm(tokens.find(item => item.id === button.dataset.forgeEdit));
    if (button.dataset.forgeDelete) {
      deleting = button.dataset.forgeDelete;
      renderList();
      list.querySelector('[data-forge-keep]')?.focus();
    } else if (button.hasAttribute('data-forge-keep')) {
      const id = deleting;
      deleting = '';
      renderList();
      [...list.querySelectorAll('[data-forge-delete]')].find(item => item.dataset.forgeDelete === id)?.focus();
    } else if (button.dataset.forgeRemove && button.dataset.forgeRemove === deleting) {
      return request('DELETE', null, deleting);
    }
  };

  dialog.addEventListener('close', () => {
    token.value = '';
    dialog.remove();
    const fallback = $('#credentialPanel [data-credential-add-menu="platform"] + .hui-select-trigger');
    (opener?.isConnected && opener.getClientRects().length ? opener : fallback)?.focus();
  }, {once: true});
  document.body.append(dialog);
  hydrateCredentialIcons(dialog);
  dialog.showModal();

  return api(endpoint, {silent: true}).then(result => {
    if (!dialog.open || state.currentUser?.id !== user.id) return;
    instances = result.instances;
    tokens = result.tokens;
    busy = false;
    renderList();
    setStatus(instances.length ? '' : '请管理员先启用至少一个 Forgejo 实例。');
    if (initialID) {
      const saved = tokens.find(item => item.id === initialID);
      if (saved && instances.includes(saved.instance)) openForm(saved);
    } else if (addInitially) {
      openForm();
      if (instances.length && !availableInstances().length) {
        setStatus(lfT('已配置所有可用的 Forgejo 实例。', 'All available Forgejo instances are configured.'));
      }
    }
  }).catch(error => {
    if (dialog.open) setStatus(error.message, true);
  });
}
