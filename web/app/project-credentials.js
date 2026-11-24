const projectCredentialKinds = {
  'username-password': {label:'账号密码', fields:[['账号','SERVICE_USERNAME'],['密码','SERVICE_PASSWORD']]},
  token: {label:'API Token / API Key', fields:[['Token / Key','SERVICE_API_TOKEN']]},
  aksk: {label:'AK/SK', fields:[['Access Key ID','ACCESS_KEY_ID'],['Secret Access Key','SECRET_ACCESS_KEY'],['Session Token','SESSION_TOKEN']]},
  custom: {label:'自定义', fields:[['变量','']]},
};

function projectCredentialsPath(projectID) {
  return '/api/projects/' + encodeURIComponent(projectID) + '/credentials';
}

let projectCredentialsRequest = 0;

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
  $('#projectCredentialsProject').textContent = project?.name || '';
  const create = $('#newProjectCredential');
  create.hidden = true;
  host.textContent = projectID ? '正在加载项目凭据…' : '请先选择项目。';
  if (!projectID) return;
  try {
    const {credentials, canManage} = await api(projectCredentialsPath(projectID), {silent:true});
    if (stamp !== projectCredentialsRequest || state.currentView !== 'projectCredentials') return;
    const redraw = () => loadProjectCredentialsPage();
    create.hidden = !canManage;
    create.onclick = () => editProjectCredential(host, projectID, null, redraw);
    if (!credentials.length) {
      host.textContent = '当前项目还没有凭据。';
      return;
    }
    host.innerHTML = credentials.map(item => `
      <div class="project-credential-item">
        <div>
          <strong data-i18n-skip>${escapeHTML(item.name)}</strong>
          <span class="project-credential-kind">${escapeHTML(projectCredentialKinds[item.kind]?.label || item.kind)} · ${item.disabled ? '已停用' : '已设置'}</span>
          <div class="project-credential-variables">${item.variables.map(name => `<code>${escapeHTML(name)}</code>`).join('')}</div>
        </div>
        ${canManage ? '<button type="button" class="button ghost small" data-edit-credential>编辑</button>' : ''}
      </div>`).join('');
    if (canManage) {
      host.querySelectorAll('[data-edit-credential]').forEach((button, i) => {
        button.onclick = () => editProjectCredential(host, projectID, credentials[i], redraw);
      });
    }
  } catch (error) {
    if (stamp === projectCredentialsRequest) host.textContent = error.message;
  }
}

function credentialVariableInput(label, name, editing, removable) {
  return `<div class="project-credential-field" data-credential-field data-original="${escapeHTML(editing ? name : '')}">
    <label><span>${escapeHTML(label)}环境变量名</span><input class="input" name="env" value="${escapeHTML(name)}" required pattern="[A-Za-z_][A-Za-z0-9_]{0,127}" spellcheck="false"></label>
    <label><span>${escapeHTML(label)}值</span><input class="input" type="password" name="secretValue" autocomplete="new-password" ${editing ? 'placeholder="已设置，填写新值可替换"' : 'required'}></label>
    ${removable ? '<button type="button" class="button ghost small" data-remove-variable>移除</button>' : ''}
  </div>`;
}

function editProjectCredential(host, projectID, item, onClose) {
  $('#newProjectCredential').hidden = true;
  host.innerHTML = `<form class="project-credential-form" autocomplete="off">
    <h2>${item ? '编辑凭据' : '新增凭据'}</h2>
    <div class="project-credential-form-meta">
      <label><span>名称</span><input class="input" name="name" maxlength="80" required value="${escapeHTML(item?.name || '')}"></label>
      <label><span>类型</span><select class="select" name="kind" ${item ? 'disabled' : ''}>
        ${Object.entries(projectCredentialKinds).map(([kind, spec]) => `<option value="${kind}" ${item?.kind === kind ? 'selected' : ''}>${spec.label}</option>`).join('')}
      </select></label>
    </div>
    <div class="project-credential-fields" data-credential-fields></div>
    <button type="button" class="button ghost small" data-add-variable hidden></button>
    <label class="project-credential-disabled"><input type="checkbox" name="disabled" role="switch" ${item?.disabled ? 'checked' : ''}>停用凭据</label>
    <div class="project-credential-form-actions">
      ${item ? '<button type="button" class="button danger-outline" data-delete-credential>删除</button>' : ''}
      <button type="button" class="button ghost" data-cancel-credential>取消</button>
      <button type="submit" class="button primary">保存</button>
    </div>
    <p data-credential-error role="alert" hidden></p>
  </form>`;

  const form = host.querySelector('form');
  const fields = form.querySelector('[data-credential-fields]');
  const add = form.querySelector('[data-add-variable]');
  const errorLabel = form.querySelector('[data-credential-error]');
  const kind = () => form.elements.kind.value;
  const rows = () => Array.from(fields.querySelectorAll('[data-credential-field]'));
  function syncFieldActions() {
    const type = kind();
    add.textContent = type === 'aksk' ? '添加 Session Token' : '添加变量';
    add.hidden = !(type === 'custom' && rows().length < 16 || type === 'aksk' && rows().length === 2);
    if (type === 'custom') {
      fields.querySelectorAll('[data-remove-variable]').forEach(button => { button.disabled = rows().length <= 1; });
    }
  }
  function showFields(saved) {
    const type = kind();
    const definition = projectCredentialKinds[type].fields;
    const names = saved || definition.slice(0, type === 'aksk' ? 2 : undefined).map(field => field[1]);
    fields.innerHTML = names.map((name, i) => credentialVariableInput(
      definition[i]?.[0] || '变量', name, !!saved, type === 'custom' || i === 2 && type === 'aksk',
    )).join('');
    syncFieldActions();
  }
  showFields(item?.variables);
  form.elements.kind.onchange = () => showFields();
  add.onclick = () => {
    const [label, name] = kind() === 'aksk' ? projectCredentialKinds.aksk.fields[2] : ['变量',''];
    fields.insertAdjacentHTML('beforeend', credentialVariableInput(label, name, false, true));
    syncFieldActions();
    rows().at(-1).querySelector('input').focus();
  };
  fields.onclick = event => {
    const button = event.target.closest('[data-remove-variable]');
    if (!button) return;
    button.closest('[data-credential-field]').remove();
    syncFieldActions();
  };
  const endpoint = projectCredentialsPath(projectID) + (item ? '/' + encodeURIComponent(item.id) : '');
  const submit = form.querySelector('[type=submit]');
  const showError = error => {
    errorLabel.hidden = false;
    errorLabel.textContent = error.message;
  };
  form.querySelector('[data-cancel-credential]').onclick = onClose;
  if (item) form.querySelector('[data-delete-credential]').onclick = async () => {
    const message = window.lfT(
      `已绑定「${item.name}」的执行在重新选择凭据后才能启动。`,
      `Runs that use “${item.name}” need a replacement credential before they can start.`,
    );
    const confirmed = await afConfirm({title:'删除凭据？', message, danger:true, confirmText:'删除'});
    if (!confirmed || !form.isConnected) return;
    try {
      await api(endpoint, {method:'DELETE', silent:true});
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
        return {name, ...(previousName ? {previousName} : {}), ...(value || !previousName ? {value} : {})};
      }),
    };
    try {
      await api(endpoint, {method:item ? 'PUT' : 'POST', body:JSON.stringify(input), silent:true});
      if (form.isConnected) onClose();
    } catch (error) {
      showError(error);
    } finally {
      submit.disabled = false;
    }
  };
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
    const {credentials} = await api(projectCredentialsPath(workflow.projectId), {silent:true});
    if (stamp !== workflowCredentialsRequest || state.workflow !== workflow) return;
    const selected = new Set(workflow.projectCredentialIds || []);
    const available = credentials.filter(item => !item.disabled || selected.has(item.id));
    const missing = [...selected].filter(id => !available.some(item => item.id === id));
    list.innerHTML = [...available, ...missing.map(id => ({id, disabled:true}))].map(item => `
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
