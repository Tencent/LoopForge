const projectCredentialKinds = {
  'username-password': {
    label: '账号密码',
    icon: 'user',
    fields: [
      ['账号', 'SERVICE_USERNAME', '输入账号'],
      ['密码', 'SERVICE_PASSWORD', '输入密码'],
    ],
  },
  token: {
    label: 'API Token / API Key',
    icon: 'key',
    fields: [
      ['Token / Key', 'SERVICE_API_TOKEN', '粘贴 Token 或 API Key'],
    ],
  },
  aksk: {
    label: 'AK/SK',
    icon: 'lock',
    fields: [
      ['Access Key ID', 'ACCESS_KEY_ID', '输入 Access Key ID'],
      ['Secret Access Key', 'SECRET_ACCESS_KEY', '输入 Secret Access Key'],
      ['Session Token', 'SESSION_TOKEN', '输入临时 Session Token'],
    ],
  },
  custom: {
    label: '自定义',
    icon: 'variable',
    fields: [
      ['变量', 'API_KEY', '输入变量值'],
    ],
  },
};

let projectCredentialsRequest = 0;
let projectCredentialFieldUid = 0;

function projectCredentialsPath(projectID) {
  return '/api/projects/' + encodeURIComponent(projectID) + '/credentials';
}

function pcIcon(name, size = 14) {
  return `<svg class="agent-icon harness-icon" data-icon="${escapeHTML(name)}" width="${size}" height="${size}" aria-hidden="true" focusable="false"></svg>`;
}

function pcHydrate(root) {
  if (root) window.AgentFlowHarnessIcons?.hydrate?.(root);
}

function pcBadge(disabled) {
  if (disabled) {
    return '<span class="hui-badge" data-size="sm" data-tone="neutral"><span class="hui-badge-glyph" aria-hidden="true"><svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 12h12"/></svg></span><span class="hui-badge-label">已停用</span></span>';
  }
  return `<span class="hui-badge" data-size="sm" data-tone="success"><span class="hui-badge-glyph" aria-hidden="true">${pcIcon('check', 12)}</span><span class="hui-badge-label">已设置</span></span>`;
}

function pcFormatDate(value) {
  if (!value) return '';
  const ts = Date.parse(value);
  if (Number.isNaN(ts)) return '';
  const d = new Date(ts);
  const pad = n => String(n).padStart(2, '0');
  return `${d.getFullYear()}/${pad(d.getMonth() + 1)}/${pad(d.getDate())}`;
}

function pcPlaceholderExample(name) {
  const clean = String(name || '').trim() || 'API_KEY';
  return `例如 ${clean}`;
}

function pcNoticeHTML({ icon = 'key', title, body, steps = [], actionHTML = '', isError = false }) {
  return `<div class="project-credential-panel">
    <div class="project-credential-empty${isError ? ' is-error' : ''}">
      <div class="project-credential-empty-icon">${pcIcon(icon, 22)}</div>
      <h2>${escapeHTML(title)}</h2>
      <p>${escapeHTML(body)}</p>
      ${steps.length ? `<ol class="project-credential-guide-steps">${steps.map(([stepTitle, stepDesc]) => `<li><b>${escapeHTML(stepTitle)}</b><span>${escapeHTML(stepDesc)}</span></li>`).join('')}</ol>` : ''}
      ${actionHTML}
    </div>
  </div>`;
}

function closeProjectCredentialsPage() {
  projectCredentialsRequest++;
  $('#projectCredentialsContent')?.replaceChildren();
}

async function loadProjectCredentialsPage() {
  const host = $('#projectCredentialsContent');
  if (!host) return;
  const projectID = state.projectFilter;
  const stamp = ++projectCredentialsRequest;
  const project = (state.data?.projects || []).find(item => item.id === projectID);
  const projectBadge = $('#projectCredentialsProject');
  if (projectBadge) {
    projectBadge.textContent = project?.name || '';
    projectBadge.hidden = !project?.name;
  }
  const create = $('#newProjectCredential');
  create.hidden = true;
  if (!projectID) {
    host.innerHTML = pcNoticeHTML({
      icon: 'key',
      title: '请先选择一个项目',
      body: '在顶部栏选择项目后，即可管理该项目的凭据并查看可注入沙箱的环境变量。',
    });
    pcHydrate(host);
    return;
  }
  host.innerHTML = `<div class="project-credential-panel"><div class="project-credential-loading" role="status">${pcIcon('arc-spinner', 16)}<span>正在加载项目凭据…</span></div></div>`;
  pcHydrate(host);
  try {
    const { credentials, canManage } = await api(projectCredentialsPath(projectID), { silent: true });
    if (stamp !== projectCredentialsRequest || state.currentView !== 'projectCredentials') return;
    const redraw = () => loadProjectCredentialsPage();
    create.hidden = !canManage;
    create.onclick = () => editProjectCredential(host, projectID, null, redraw);
    if (!credentials.length) {
      host.innerHTML = pcNoticeHTML({
        icon: 'key',
        title: '当前项目还没有凭据',
        body: '可将第三方服务账号、API Token 或 AK/SK 加密保存在项目内；在 LOOP 中选用后，凭据会作为环境变量注入沙箱执行进程。',
        steps: [
          ['加密存储', '保存后仅可替换，不回显明文'],
          ['按需绑定', '在「流程设计 → 流程设置 → 项目凭据」中勾选选用'],
          ['沙箱注入', '自动转为环境变量并脱敏输出'],
        ],
        actionHTML: canManage
          ? `<button type="button" class="button hui-btn-soft" data-empty-new>${pcIcon('plus')}新增凭据</button>`
          : '<p class="project-credential-ro-note">仅项目负责人、维护者或平台管理员可新增或编辑凭据。</p>',
      });
      pcHydrate(host);
      host.querySelector('[data-empty-new]')?.addEventListener('click', () => editProjectCredential(host, projectID, null, redraw));
      return;
    }
    host.innerHTML = `<div class="project-credential-panel">
      <div class="project-credential-panel-head">
        <h2>项目凭据 <span class="project-credential-count">${credentials.length}</span></h2>
        <div class="project-credential-panel-meta">
          <span class="project-credential-panel-hint">${pcIcon('info', 14)}<span>作为环境变量注入已绑定 LOOP 的沙箱进程，日志自动脱敏</span></span>
          ${!canManage ? '<span class="hui-badge" data-size="sm" data-tone="neutral"><span class="hui-badge-label">只读查看</span></span>' : ''}
        </div>
      </div>
      <div class="project-credential-table-wrap">
        <div class="project-credential-row-head" aria-hidden="true">
          <span>凭据</span>
          <span>沙箱环境变量</span>
          <span>状态</span>
          <span class="is-end">操作</span>
        </div>
        <div class="project-credential-list">
          ${credentials.map(item => {
            const spec = projectCredentialKinds[item.kind];
            const kindLabel = spec?.label || item.kind;
            const kindIcon = spec?.icon || 'key';
            const updated = pcFormatDate(item.updatedAt);
            const sub = updated ? `${kindLabel} · 更新于 ${updated}` : kindLabel;
            return `<div class="project-credential-item${item.disabled ? ' is-disabled' : ''}">
              <div class="project-credential-main">
                <span class="project-credential-tile">${pcIcon(kindIcon, 16)}</span>
                <div class="project-credential-copy">
                  <strong data-i18n-skip>${escapeHTML(item.name)}</strong>
                  <span class="project-credential-kind">${escapeHTML(sub)}</span>
                </div>
              </div>
              <div class="project-credential-variables">${(item.variables || []).map(name => `<code data-i18n-skip>${escapeHTML(name)}</code>`).join('')}</div>
              <div class="project-credential-status">${pcBadge(item.disabled)}</div>
              <div class="project-credential-actions">
                ${canManage
                  ? `<button type="button" class="button small hui-btn-soft" data-edit-credential>${pcIcon('edit', 13)}编辑</button>`
                  : '<span class="project-credential-ro-hint">可用</span>'}
              </div>
            </div>`;
          }).join('')}
        </div>
      </div>
      <div class="project-credential-panel-foot">
        ${pcIcon('terminal', 14)}
        <span>使用方式：在「流程设计 → 流程设置 → 项目凭据」勾选后，运行时自动注入沙箱进程环境变量。</span>
      </div>
    </div>`;
    pcHydrate(host);
    if (canManage) {
      host.querySelectorAll('[data-edit-credential]').forEach((button, i) => {
        button.onclick = () => editProjectCredential(host, projectID, credentials[i], redraw);
      });
    }
  } catch (error) {
    if (stamp !== projectCredentialsRequest) return;
    host.innerHTML = pcNoticeHTML({
      icon: 'alert',
      title: '加载失败',
      body: error.message || '读取项目凭据失败',
      actionHTML: `<button type="button" class="button" data-retry-credentials>${pcIcon('refresh')}重试</button>`,
      isError: true,
    });
    pcHydrate(host);
    host.querySelector('[data-retry-credentials]')?.addEventListener('click', loadProjectCredentialsPage);
  }
}

function credentialVariableInput(label, placeholderName, valuePlaceholder, savedName, editingSaved, removable) {
  const uid = ++projectCredentialFieldUid;
  const envId = `pc-env-${uid}`;
  const valId = `pc-val-${uid}`;
  const hintName = placeholderName || 'API_KEY';
  const envValue = editingSaved ? (savedName || '') : '';
  const envPlaceholder = pcPlaceholderExample(hintName);
  const secretPlaceholder = editingSaved ? '已设置，留空保持不变' : (valuePlaceholder || '输入变量值');
  const envLabel = label === '变量' ? '环境变量名' : `${/[A-Za-z0-9)]$/.test(label) ? `${label} ` : label}环境变量名`;
  const valLabel = `${/[A-Za-z0-9)]$/.test(label) ? `${label} ` : label}值`;
  return `<div class="project-credential-field" data-credential-field data-hint="${escapeHTML(hintName)}" data-original="${escapeHTML(editingSaved ? (savedName || '') : '')}">
    ${removable ? `<div class="project-credential-field-head"><span>${escapeHTML(label)}</span><button type="button" class="button ghost small project-credential-remove" data-remove-variable>${pcIcon('trash', 13)}移除</button></div>` : ''}
    <div class="project-credential-field-grid">
      <div class="project-credential-field-group">
        <label class="project-credential-label" for="${envId}"><span>${escapeHTML(envLabel)}</span><span class="project-credential-req" aria-hidden="true">*</span></label>
        <input id="${envId}" class="input project-credential-mono" name="env" value="${escapeHTML(envValue)}" placeholder="${escapeHTML(envPlaceholder)}" required pattern="[A-Za-z_][A-Za-z0-9_]{0,127}" autocorrect="off" autocapitalize="none" spellcheck="false" aria-invalid="false">
      </div>
      <div class="project-credential-field-group">
        <label class="project-credential-label" for="${valId}"><span>${escapeHTML(valLabel)}</span>${editingSaved ? '' : '<span class="project-credential-req" aria-hidden="true">*</span>'}</label>
        <div class="project-credential-secret-wrap">
          <input id="${valId}" class="input" type="password" name="secretValue" autocomplete="new-password" placeholder="${escapeHTML(secretPlaceholder)}" ${editingSaved ? '' : 'required'} aria-invalid="false">
          <button type="button" class="hui-icon-btn project-credential-eye" data-toggle-secret aria-pressed="false" aria-label="显示值" title="显示值">${pcIcon('eye', 14)}</button>
        </div>
      </div>
    </div>
  </div>`;
}

function pcSyncFieldValidity(el) {
  if (!el?.matches?.('input, select')) return;
  let invalid = false;
  try {
    invalid = el.matches(':user-invalid');
  } catch {
    invalid = false;
  }
  el.setAttribute('aria-invalid', invalid ? 'true' : 'false');
}

function editProjectCredential(host, projectID, item, onClose) {
  $('#newProjectCredential').hidden = true;
  const uid = ++projectCredentialFieldUid;
  const nameId = `pc-name-${uid}`;
  const kindId = `pc-kind-${uid}`;
  const disabledId = `pc-disabled-${uid}`;
  host.innerHTML = `<div class="project-credential-layout">
    <div class="project-credential-panel project-credential-editor">
      <form class="project-credential-form" autocomplete="off">
        <div class="project-credential-editor-head">
          <nav class="project-credential-crumbs" aria-label="位置">
            <button type="button" class="project-credential-crumb-link" data-back-credential>${pcIcon('key', 14)}<span>凭据管理</span></button>
            ${pcIcon('chevron-right', 13)}
            <h2 class="project-credential-crumb-current">${item ? `<span>编辑</span> · <span data-i18n-skip>${escapeHTML(item.name)}</span>` : '<span>新增凭据</span>'}</h2>
          </nav>
        </div>
        <div class="project-credential-editor-body">
          <div class="project-credential-form-meta">
            <div class="project-credential-field-group">
              <label class="project-credential-label" for="${nameId}"><span>名称</span><span class="project-credential-req" aria-hidden="true">*</span></label>
              <input id="${nameId}" class="input" name="name" maxlength="80" required placeholder="例如 制品库访问令牌" value="${escapeHTML(item?.name || '')}" aria-invalid="false">
              <small class="project-credential-hint">用于在 LOOP 配置中识别此凭据（1–80 个字符）。</small>
            </div>
            <div class="project-credential-field-group">
              <label class="project-credential-label" for="${kindId}"><span>类型</span></label>
              <select id="${kindId}" class="select" name="kind" ${item ? 'disabled' : ''}>
                ${Object.entries(projectCredentialKinds).map(([kind, spec]) => `<option value="${kind}" ${item?.kind === kind ? 'selected' : ''}>${spec.label}</option>`).join('')}
              </select>
              <small class="project-credential-hint">${item ? '凭据创建后不可更改类型。' : '不同类型提供对应的常见环境变量提示。'}</small>
            </div>
          </div>
          <section class="project-credential-section" aria-label="环境变量与密钥值">
            <div class="project-credential-section-head">
              <h3>环境变量与密钥值</h3>
              <p>变量名将注入沙箱环境变量（字母、数字、下划线，不能以数字开头）；保存后密钥只写不回显。</p>
            </div>
            <div class="project-credential-fields" data-credential-fields></div>
            <button type="button" class="button ghost small project-credential-add-var" data-add-variable hidden></button>
          </section>
          <label class="project-credential-disabled" for="${disabledId}">
            <span class="project-credential-disabled-copy">
              <b>停用凭据</b>
              <small>停用后新启动的 LOOP 沙箱将不可选用此凭据，已加密保存的值仍保留。</small>
            </span>
            <input id="${disabledId}" type="checkbox" name="disabled" role="switch" ${item?.disabled ? 'checked' : ''}>
          </label>
          <p class="project-credential-error" data-credential-error role="alert" hidden></p>
        </div>
        <div class="project-credential-form-actions">
          ${item ? `<button type="button" class="button danger-outline" data-delete-credential>${pcIcon('trash')}删除</button>` : ''}
          <button type="button" class="button ghost" data-cancel-credential>取消</button>
          <button type="submit" class="button primary">保存</button>
        </div>
      </form>
    </div>
    <aside class="project-credential-aside" aria-label="沙箱环境变量说明">
      <div class="project-credential-aside-card">
        <div class="project-credential-aside-head">
          ${pcIcon('terminal', 15)}
          <h3>沙箱环境变量说明</h3>
        </div>
        <ul class="project-credential-aside-list">
          <li>
            <b>自动注入沙箱</b>
            <span>在「流程设计 → 流程设置 → 项目凭据」勾选后，以上变量会在沙箱进程启动时注入。</span>
          </li>
          <li>
            <b>只写存储与脱敏</b>
            <span>密钥加密存储且保存后不再回显，命令输出与对话流会自动脱敏为 <code>[redacted]</code></span>
          </li>
          <li>
            <b>变量名规范</b>
            <span>仅支持字母、数字和下划线且不能以数字开头；不可使用 <code>PATH</code> / <code>HOME</code> / <code>GITHUB_TOKEN</code> / <code>AGENTFLOW_*</code> 等沙箱保留名</span>
          </li>
        </ul>
      </div>
      <div class="project-credential-aside-card">
        <div class="project-credential-aside-head">
          <h4>环境变量预览</h4>
          <span class="project-credential-aside-status" data-preview-status></span>
        </div>
        <div class="project-credential-preview" data-env-preview></div>
        <p class="project-credential-preview-warn" data-preview-disabled-note hidden>已停用：保存后暂不会注入沙箱</p>
      </div>
    </aside>
  </div>`;

  const form = host.querySelector('form');
  const fields = form.querySelector('[data-credential-fields]');
  const add = form.querySelector('[data-add-variable]');
  const errorLabel = form.querySelector('[data-credential-error]');
  const previewBox = host.querySelector('[data-env-preview]');
  const previewStatus = host.querySelector('[data-preview-status]');
  const previewDisabledNote = host.querySelector('[data-preview-disabled-note]');
  const kind = () => form.elements.kind.value;
  const rows = () => Array.from(fields.querySelectorAll('[data-credential-field]'));

  function updateEnvPreview() {
    if (!previewBox) return;
    const isDisabled = !!form.elements.disabled?.checked;
    if (previewStatus) {
      previewStatus.textContent = isDisabled ? '已停用' : '生效中';
      previewStatus.classList.toggle('is-disabled', isDisabled);
    }
    if (previewDisabledNote) previewDisabledNote.hidden = !isDisabled;
    previewBox.innerHTML = rows().map(row => {
      const typed = row.querySelector('[name=env]')?.value.trim() || '';
      const hint = row.dataset.hint || 'API_KEY';
      if (typed) {
        return `<div class="project-credential-preview-row"><code data-i18n-skip>${escapeHTML(typed)}</code><span class="project-credential-preview-eq">=</span><span class="project-credential-preview-val">••••••••</span></div>`;
      }
      return `<div class="project-credential-preview-row is-pending"><code class="is-placeholder" data-i18n-skip>${escapeHTML(hint)}</code><span class="project-credential-preview-tag">待填写</span></div>`;
    }).join('');
  }

  function syncFieldActions() {
    const type = kind();
    const addLabel = type === 'aksk' ? '添加 Session Token' : '添加变量';
    add.innerHTML = `${pcIcon('plus', 13)}<span>${escapeHTML(addLabel)}</span>`;
    add.hidden = !(type === 'custom' && rows().length < 16 || type === 'aksk' && rows().length === 2);
    if (type === 'custom') {
      fields.querySelectorAll('[data-remove-variable]').forEach(button => { button.disabled = rows().length <= 1; });
    }
    pcHydrate(add);
    updateEnvPreview();
  }

  function showFields(saved) {
    const type = kind();
    const definition = projectCredentialKinds[type].fields;
    const initialCount = saved ? saved.length : (type === 'aksk' ? 2 : definition.length);
    const items = saved || Array.from({ length: initialCount }, (_, i) => '');
    fields.innerHTML = items.map((savedName, i) => {
      const spec = definition[i] || definition[0] || ['变量', 'API_KEY', '输入变量值'];
      return credentialVariableInput(
        spec[0],
        spec[1],
        spec[2],
        savedName,
        !!saved,
        type === 'custom' || i === 2 && type === 'aksk',
      );
    }).join('');
    pcHydrate(fields);
    syncFieldActions();
  }

  showFields(item?.variables);
  form.elements.kind.onchange = () => showFields();

  add.onclick = () => {
    const spec = kind() === 'aksk' ? projectCredentialKinds.aksk.fields[2] : projectCredentialKinds.custom.fields[0];
    fields.insertAdjacentHTML('beforeend', credentialVariableInput(spec[0], spec[1], spec[2], '', false, true));
    pcHydrate(fields);
    syncFieldActions();
    rows().at(-1)?.querySelector('[name=env]')?.focus();
  };

  fields.onclick = event => {
    const toggle = event.target.closest('[data-toggle-secret]');
    if (toggle) {
      const input = toggle.closest('.project-credential-secret-wrap')?.querySelector('[name=secretValue]');
      if (!input) return;
      const show = input.type === 'password';
      input.type = show ? 'text' : 'password';
      toggle.setAttribute('aria-pressed', String(show));
      const label = show ? '隐藏值' : '显示值';
      toggle.setAttribute('aria-label', label);
      if (toggle.hasAttribute('data-hui-title')) toggle.setAttribute('data-hui-title', label);
      else toggle.setAttribute('title', label);
      toggle.innerHTML = pcIcon(show ? 'eye-off' : 'eye', 14);
      pcHydrate(toggle);
      return;
    }
    const button = event.target.closest('[data-remove-variable]');
    if (!button) return;
    button.closest('[data-credential-field]').remove();
    syncFieldActions();
  };

  form.addEventListener('invalid', event => {
    const el = event.target;
    if (!el?.setCustomValidity) return;
    if (el.name === 'name' && el.validity.valueMissing) {
      el.setCustomValidity(window.lfT('请填写名称', 'Enter a credential name'));
    } else if (el.name === 'env') {
      if (el.validity.valueMissing) {
        el.setCustomValidity(window.lfT('请填写环境变量名', 'Enter an environment variable name'));
      } else if (el.validity.patternMismatch) {
        el.setCustomValidity(window.lfT('请输入有效的环境变量名（以字母或下划线开头，仅含字母、数字、下划线）', 'Enter a valid variable name (start with a letter or underscore; letters, digits, and underscores only)'));
      }
    } else if (el.name === 'secretValue' && el.validity.valueMissing) {
      el.setCustomValidity(window.lfT('请填写凭据值', 'Enter a secret value'));
    }
    el.setAttribute('aria-invalid', 'true');
  }, true);

  form.addEventListener('input', event => {
    const el = event.target;
    el?.setCustomValidity?.('');
    pcSyncFieldValidity(el);
    if (el?.name === 'env') updateEnvPreview();
  });

  form.addEventListener('change', event => {
    if (event.target?.name === 'disabled') updateEnvPreview();
  });

  form.addEventListener('blur', event => {
    pcSyncFieldValidity(event.target);
  }, true);

  const endpoint = projectCredentialsPath(projectID) + (item ? '/' + encodeURIComponent(item.id) : '');
  const submit = form.querySelector('[type=submit]');
  const showError = error => {
    errorLabel.hidden = false;
    errorLabel.textContent = error.message;
  };
  form.querySelector('[data-cancel-credential]').onclick = onClose;
  form.querySelector('[data-back-credential]').onclick = onClose;
  if (item) form.querySelector('[data-delete-credential]').onclick = async () => {
    const message = window.lfT(
      `已绑定「${item.name}」的执行在重新选择凭据后才能启动。`,
      `Runs that use “${item.name}” need a replacement credential before they can start.`,
    );
    const confirmed = await afConfirm({ title: '删除凭据？', message, danger: true, confirmText: '删除' });
    if (!confirmed || !form.isConnected) return;
    try {
      await api(endpoint, { method: 'DELETE', silent: true });
      if (form.isConnected) onClose();
    } catch (error) {
      showError(error);
    }
  };
  form.onsubmit = async event => {
    event.preventDefault();
    if (submit.disabled) return;
    submit.disabled = true;
    errorLabel.hidden = true;
    const input = {
      name: form.elements.name.value.trim(),
      kind: kind(),
      disabled: form.elements.disabled.checked,
      fields: rows().map(row => {
        const name = row.querySelector('[name=env]').value.trim();
        const value = row.querySelector('[name=secretValue]').value;
        const previousName = row.dataset.original;
        return { name, ...(previousName ? { previousName } : {}), ...(value || !previousName ? { value } : {}) };
      }),
    };
    try {
      await api(endpoint, { method: item ? 'PUT' : 'POST', body: JSON.stringify(input), silent: true });
      if (form.isConnected) onClose();
    } catch (error) {
      showError(error);
    } finally {
      submit.disabled = false;
    }
  };
  pcHydrate(host);
  form.elements.name.focus();
}

let workflowCredentialsRequest = 0;

async function renderWorkflowProjectCredentials(workflow) {
  const host = $('#workflowProjectCredentials');
  if (!host) return;
  const stamp = ++workflowCredentialsRequest;
  const visible = state.currentView === 'flow' && workflow?.projectId && !catalogGeneric(workflow) && !catalogForeign(workflow.projectId);
  host.hidden = !visible;
  if (!visible) return;
  const list = host.querySelector('[data-workflow-credential-list]');
  list.textContent = '正在加载项目凭据…';
  try {
    const { credentials } = await api(projectCredentialsPath(workflow.projectId), { silent: true });
    if (stamp !== workflowCredentialsRequest || state.workflow !== workflow) return;
    const selected = new Set(workflow.projectCredentialIds || []);
    const available = credentials.filter(item => !item.disabled || selected.has(item.id));
    const missing = [...selected].filter(id => !available.some(item => item.id === id));
    list.innerHTML = [...available, ...missing.map(id => ({ id, disabled: true }))].map(item => `
      <label class="project-credential-choice"><input type="checkbox" value="${escapeHTML(item.id)}" ${selected.has(item.id) ? 'checked' : ''} ${item.disabled && !selected.has(item.id) ? 'disabled' : ''}>
        <span>${item.name ? `<span data-i18n-skip>${escapeHTML(item.name)}</span>` : '凭据已不可用'}${item.disabled ? ' · 已停用或删除' : ''}</span>
      </label>`).join('') || '<p>当前项目没有可用的凭据。</p>';
    list.onchange = event => {
      const input = event.target;
      if (input.type !== 'checkbox' || state.workflow !== workflow) return;
      if (input.checked) selected.add(input.value);
      else selected.delete(input.value);
      workflow.projectCredentialIds = Array.from(selected);
      if (!input.checked && !credentials.some(item => item.id === input.value && !item.disabled)) input.disabled = true;
      dirty();
    };
  } catch (error) {
    if (stamp === workflowCredentialsRequest) list.textContent = error.message;
  }
}
