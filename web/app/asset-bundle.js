(function () {
  const dialog = document.querySelector('#assetBundleDialog');
  const trigger = document.querySelector('#assetBundleButton');
  const list = dialog.querySelector('#assetBundleChoices');
  const upload = dialog.querySelector('#assetBundleFile');
  const exportButton = dialog.querySelector('#exportAssetBundle');
  const importButton = dialog.querySelector('#importAssetBundle');
  const notes = dialog.querySelector('#assetBundleNotes');
  const selection = [
    ['sop', 'workflows'],
    ['agent', 'agents'],
    ['skill', 'skills'],
    ['mcp', 'mcpServers'],
  ];
  const copy = (zh, en) => window.lfT?.(zh, en) ?? zh;
  let project = '';
  let bundle = null;
  let working = false;

  function endpoint(action) {
    return '/api/projects/' + encodeURIComponent(project) + '/asset-bundles/' + action;
  }

  function synchronize() {
    dialog.setAttribute('aria-busy', String(working));
    exportButton.disabled = working || !list.querySelector('input:checked');
    importButton.disabled = working || !bundle;
    upload.disabled = working;
  }

  function show(message) {
    notes.textContent = message;
  }

  function labelFor(kind) {
    return {
      sop: 'LOOP', agent: copy('角色', 'Role'), skill: 'Skill', mcp: 'MCP',
      rule: copy('规则', 'Rule'), sandbox: copy('沙箱', 'Sandbox'),
    }[kind] || kind;
  }

  const bindingAssetKinds = {
    'mcp-connection': 'mcp', 'role-integrations': 'agent', 'loop-integrations': 'sop',
    'sandbox-environment': 'sandbox', 'sandbox-privileged': 'sandbox',
  };

  // Name the asset a follow-up item points at. Preview bindings carry the
  // bundle ID and import bindings carry the new ID of the imported copy.
  function bindingTarget({kind, assetId}, assets) {
    const assetKind = bindingAssetKinds[kind];
    const asset = (assets || []).find(item => item.kind === assetKind &&
      (item.targetId ? item.targetId === assetId : item.id === assetId));
    if (!asset) return assetId;
    return asset.targetId ? asset.name + ' (' + asset.targetId + ')' : asset.name;
  }

  function exportedAssets(value) {
    return [['sop', 'workflows'], ['agent', 'agents'], ['mcp', 'mcpServers'], ['sandbox', 'sandboxes']]
      .flatMap(([kind, collection]) => (value[collection] || []).map(item => ({kind, id: item.id, name: item.name})));
  }

  function bindingText(binding, assets) {
    const {kind} = binding;
    const assetId = bindingTarget(binding, assets);
    const label = {
      'mcp-connection': copy('配置 MCP 连接及凭据', 'Configure the MCP connection and credentials'),
      'role-integrations': copy('核对角色的平台 MCP 和知识库绑定', 'Review platform MCP and knowledge bindings for the role'),
      'loop-integrations': copy('重新配置 LOOP 凭据、通知和知识库', 'Configure LOOP credentials, notifications and knowledge'),
      'sandbox-environment': copy('配置沙箱环境变量', 'Configure sandbox environment variables'),
      'sandbox-privileged': copy('确认 Docker 特权运行设置', 'Review privileged Docker execution'),
    }[kind];
    return label ? label + ' · ' + assetId : assetId;
  }

  function renderChoices() {
    list.replaceChildren();
    for (const [kind, collection] of selection) {
      const items = (state.data[collection] || []).filter(item =>
        item.projectId === project && !item.generic && item.scope !== 'personal'
      );
      if (!items.length) continue;
      const group = document.createElement('fieldset');
      const legend = document.createElement('legend');
      legend.textContent = labelFor(kind);
      group.append(legend);
      for (const item of items) {
        const label = document.createElement('label');
        const checkbox = document.createElement('input');
        checkbox.type = 'checkbox';
        checkbox.value = item.id;
        checkbox.dataset.kind = kind;
        const name = document.createElement('span');
        name.textContent = item.name || item.id;
        label.append(checkbox, name);
        group.append(label);
      }
      list.append(group);
    }
    if (!list.childElementCount) list.textContent = copy('本项目暂无可导出的资产。', 'This project has no exportable assets.');
  }

  function download(value) {
    const file = new Blob([JSON.stringify(value)], {type: 'application/json'});
    const url = URL.createObjectURL(file);
    const link = document.createElement('a');
    link.download = 'loopforge-assets.json';
    link.href = url;
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  async function work(action) {
    if (working || !project) return;
    working = true;
    synchronize();
    try {
      await action();
    } catch (error) {
      bundle = null;
      upload.value = '';
      show(error.message || String(error));
    } finally {
      working = false;
      synchronize();
    }
  }

  trigger.addEventListener('click', () => {
    const current = state.data?.projects?.find(item => item.id === state.projectFilter);
    if (!current) {
      toast(copy('请选择一个项目', 'Choose a project'));
      return;
    }
    const user = state.currentUser;
    const canManage = user && !user.isGuest && (user.isPlatformAdmin ||
      Number(current.ownerId) === Number(user.id) ||
      (current.members || []).some(member => Number(member.userId) === Number(user.id) &&
        member.role?.trim().toLowerCase() === 'maintainer'));
    if (!canManage) {
      toast(copy('资产包需要项目管理员权限', 'Asset bundles require project manager access'));
      return;
    }
    project = current.id;
    dialog.querySelector('#assetBundleProject').textContent = current.name || project;
    bundle = null;
    upload.value = '';
    show('');
    renderChoices();
    synchronize();
    dialog.showModal();
  });
  list.addEventListener('change', synchronize);
  exportButton.addEventListener('click', () => work(async () => {
    const assets = Array.from(list.querySelectorAll('input:checked'), input =>
      ({kind: input.dataset.kind, id: input.value})
    );
    const result = await api(endpoint('export'), {method: 'POST', body: JSON.stringify({assets})});
    download(result);
    const bindings = result.bindings || [];
    show([copy('已导出所选资产及项目内依赖。', 'Exported the selected assets and project dependencies.'), ...bindings.map(item => bindingText(item, exportedAssets(result)))].join('\n'));
  }));
  upload.addEventListener('change', () => work(async () => {
    bundle = null;
    const file = upload.files?.[0];
    if (!file) {
      show('');
      return;
    }
    if (file.size > 96 * 1024 * 1024) throw new Error(copy('资产包超过 96 MiB。', 'The asset bundle exceeds 96 MiB.'));
    const text = await file.text();
    const preview = await api(endpoint('preview'), {method: 'POST', body: text});
    const assets = preview.assets || [];
    const bindings = preview.bindings || [];
    show([
      copy('将导入 ' + assets.length + ' 项资产，LOOP 保存为项目私有草稿。',
        'Importing ' + assets.length + ' assets. LOOPs will be saved as project drafts.'),
      ...assets.map(item => labelFor(item.kind) + ' · ' + item.name),
      ...bindings.map(item => bindingText(item, assets)),
    ].join('\n'));
    bundle = text;
  }));
  importButton.addEventListener('click', () => work(async () => {
    const result = await api(endpoint('import'), {method: 'POST', body: bundle});
    bundle = null;
    upload.value = '';
    const assets = result.assets || [];
    const bindings = result.bindings || [];
    show([
      copy('已导入 ' + assets.length + ' 项私有资产（名称 → 新 ID）：', 'Imported ' + assets.length + ' project assets (name → new ID):'),
      ...assets.map(item => labelFor(item.kind) + ' · ' + item.name + ' → ' + item.targetId),
      ...(bindings.length ? [copy('导入后需配置：', 'Configure after import:')] : []),
      ...bindings.map(item => bindingText(item, assets)),
    ].join('\n'));
    try {
      const fresh = await api('/api/bootstrap', {silent: true});
      Object.assign(state.data, fresh);
      renderAll();
      renderChoices();
    } catch {
      toast(copy('导入已完成。刷新页面后可查看新资产。', 'Import finished. Refresh to see the new assets.'));
    }
  }));
  dialog.addEventListener('cancel', event => {
    if (working) event.preventDefault();
  });
  dialog.querySelector('#closeAssetBundle').addEventListener('click', () => {
    if (!working) dialog.close();
  });
})();
