// Both the SOP editor / run modal and the conversation composer use the same selection UI.
window.editKnowledgeRefs = async function editKnowledgeRefs(project, refs, save) {
  const getRefId = r => String(r?.connectorId || r?.mcpServerId || '').trim();
  const parseSourceDisplay = (rawName, rawId) => {
    const text = String(rawName || rawId || '').trim();
    const m = text.match(/^\[([^\]]+)\]\s*(.+)$/);
    if (m) return { name: m[2].trim(), space: m[1].trim() };
    return { name: text, space: '' };
  };

  const dialog = document.createElement('dialog');
  dialog.className = 'knowledge-ref-dialog';

  const head = document.createElement('div');
  head.className = 'kr-head';
  const title = document.createElement('h3');
  title.textContent = '关联我的知识库';
  const lead = document.createElement('p');
  lead.className = 'kr-lead';
  lead.textContent = '选择当前对话、节点或本次运行可只读检索的个人知识库连接器。';
  const sublead = document.createElement('p');
  sublead.className = 'kr-sublead';
  sublead.textContent = '仅展示你本人已启用的连接器；凭证加密托管在服务端，不会进入执行沙箱，且只放行只读工具。';
  head.append(title, lead, sublead);

  const list = document.createElement('div');
  list.className = 'kr-list';

  const status = document.createElement('p');
  status.className = 'kr-status';
  status.setAttribute('role', 'status');

  const footer = document.createElement('div');
  footer.className = 'knowledge-ref-footer';
  const cancel = document.createElement('button');
  cancel.type = 'button';
  cancel.className = 'button ghost';
  cancel.textContent = '取消';
  cancel.onclick = () => dialog.close();
  const apply = document.createElement('button');
  apply.type = 'button';
  apply.className = 'button primary';
  apply.textContent = '保存';
  apply.disabled = true;
  footer.append(cancel, apply);

  dialog.append(head, list, status, footer);
  dialog.onclose = () => dialog.remove();
  document.body.append(dialog);
  dialog.showModal();

  const readers = [];
  const load = async path => {
    const sep = project ? (path.includes('?') ? '&' : '?') + 'projectId=' + encodeURIComponent(project) : '';
    const response = await fetch(path + sep);
    if (!response.ok) throw new Error('知识库列表加载失败');
    return response.json();
  };

  const refreshApplyState = () => {
    let valid = true;
    for (const read of readers) {
      const state = read(true);
      if (state && state.error) {
        valid = false;
        break;
      }
    }
    apply.disabled = !valid;
  };

  try {
    status.textContent = '正在加载你的知识库连接器…';
    const rawOptions = await load('/api/knowledge/reference-options');
    const options = Array.isArray(rawOptions) ? [...rawOptions] : [];
    for (const ref of (refs || [])) {
      const rid = getRefId(ref);
      if (rid && !options.some(s => s.id === rid)) {
        options.push({ id: rid, name: rid + '（已失效或无权限）', missing: true });
      }
    }

    for (const s of options) {
      const card = document.createElement('div');
      card.className = 'kr-service';

      const srvHead = document.createElement('label');
      srvHead.className = 'kr-service-head';
      const srvTitle = document.createElement('span');
      srvTitle.className = 'kr-service-title';
      const enabled = document.createElement('input');
      enabled.type = 'checkbox';
      const previous = (refs || []).find(r => getRefId(r) === s.id);
      enabled.checked = !!previous;
      srvTitle.append(enabled, document.createTextNode(s.name || s.id));
      const srvId = document.createElement('span');
      srvId.className = 'kr-service-id';
      srvId.textContent = s.typeName ? `${s.typeName} · ${s.id}` : s.id;
      srvHead.append(srvTitle, srvId);
      card.append(srvHead);

      const body = document.createElement('div');
      body.className = 'kr-service-body';

      const scopeField = document.createElement('div');
      scopeField.className = 'kr-field';
      const scopeLabel = document.createElement('span');
      scopeLabel.className = 'kr-label';
      scopeLabel.textContent = '可检索范围';
      const mode = document.createElement('select');
      mode.className = 'kr-control';
      mode.add(new Option('该连接器可读的全部来源', 'all'));
      mode.add(new Option('仅指定子知识库', 'allowlist'));
      mode.value = (previous?.mode === 'allowlist' || previous?.sources?.length) ? 'allowlist' : 'all';
      scopeField.append(scopeLabel, mode);

      const allowField = document.createElement('div');
      allowField.className = 'kr-field';
      const allowLabelRow = document.createElement('div');
      allowLabelRow.className = 'kr-label-row';
      const allowLabel = document.createElement('span');
      allowLabel.className = 'kr-label';
      allowLabel.textContent = '选择子知识库';
      const allowCount = document.createElement('span');
      allowCount.className = 'kr-count';
      allowLabelRow.append(allowLabel, allowCount);

      const choices = document.createElement('div');
      choices.className = 'kr-sources';

      const adv = document.createElement('details');
      adv.className = 'kr-advanced';
      const advSum = document.createElement('summary');
      advSum.textContent = '高级：手动输入或查看来源 ID';
      const sourcesInput = document.createElement('input');
      sourcesInput.className = 'kr-control';
      sourcesInput.placeholder = '来源 ID，以逗号分隔';
      sourcesInput.setAttribute('aria-label', (s.name || s.id) + '允许的来源 ID');
      sourcesInput.value = (previous?.sources || []).join(', ');
      adv.append(advSum, sourcesInput);

      allowField.append(allowLabelRow, choices, adv);

      const canScope = s.scoped !== false || !!(previous?.sources?.length);
      if (canScope) body.append(scopeField, allowField);
      card.append(body);
      list.append(card);

      const selectedSet = () => new Set(
        sourcesInput.value.split(/[,，]/).map(x => x.trim()).filter(Boolean)
      );

      const syncChoiceUI = () => {
        const ids = selectedSet();
        choices.querySelectorAll('.kr-source-row').forEach(row => {
          const cb = row.querySelector('input[type="checkbox"]');
          const on = ids.has(row.dataset.sourceId);
          if (cb) cb.checked = on;
          row.classList.toggle('selected', on);
        });
        if (mode.value === 'allowlist') {
          if (ids.size === 0) {
            allowCount.textContent = '请选择至少一个子知识库';
            allowCount.className = 'kr-count warn';
          } else {
            allowCount.textContent = `已选 ${ids.size} 个子知识库`;
            allowCount.className = 'kr-count ok';
          }
        } else {
          allowCount.textContent = '';
          allowCount.className = 'kr-count';
        }
        refreshApplyState();
      };

      let loaded = false;
      const update = async () => {
        body.hidden = !enabled.checked || !canScope;
        mode.disabled = !enabled.checked;
        const showAllow = enabled.checked && canScope && mode.value === 'allowlist';
        allowField.hidden = !showAllow;

        if (showAllow && !loaded && !s.missing) {
          loaded = true;
          choices.innerHTML = '<div class="kr-source-row"><span class="kr-source-meta">正在加载子知识库列表…</span></div>';
          try {
            const response = await fetch(`/api/knowledge/connectors/${encodeURIComponent(s.id)}/sources`);
            if (!response.ok) throw new Error('子知识库列表加载失败');
            const available = await response.json();
            const items = Array.isArray(available) ? available : [];
            choices.innerHTML = '';
            if (!items.length) {
              choices.innerHTML = '<div class="kr-source-row"><span class="kr-source-meta">该连接器未返回可细分列表，可改选「全部来源」</span></div>';
            }
            for (const source of items) {
              const info = parseSourceDisplay(source.name, source.id);
              const row = document.createElement('label');
              row.className = 'kr-source-row';
              row.dataset.sourceId = source.id;
              const cb = document.createElement('input');
              cb.type = 'checkbox';
              const box = document.createElement('span');
              box.className = 'kr-source-info';
              const nm = document.createElement('span');
              nm.className = 'kr-source-name';
              nm.textContent = info.name;
              const meta = document.createElement('span');
              meta.className = 'kr-source-meta';
              meta.textContent = info.space ? `空间：${info.space}` : source.id;
              box.append(nm, meta);
              row.append(cb, box);

              cb.addEventListener('change', () => {
                const ids = selectedSet();
                if (cb.checked) ids.add(source.id);
                else ids.delete(source.id);
                sourcesInput.value = [...ids].join(', ');
                syncChoiceUI();
              });
              choices.append(row);
            }
          } catch (e) {
            loaded = false;
            choices.innerHTML = '';
            const errRow = document.createElement('div');
            errRow.className = 'kr-source-row';
            const errMeta = document.createElement('span');
            errMeta.className = 'kr-source-meta';
            errMeta.textContent = e?.message || '子知识库列表加载失败';
            errRow.append(errMeta);
            choices.append(errRow);
          }
        }
        syncChoiceUI();
      };

      sourcesInput.addEventListener('input', syncChoiceUI);
      enabled.addEventListener('change', update);
      mode.addEventListener('change', update);
      update();

      readers.push(silent => {
        if (!enabled.checked) return null;
        if (s.missing) {
          if (silent) return { error: '请移除已失效连接器' };
          throw new Error('请移除已失效连接器：' + s.name);
        }
        const selected = mode.value === 'allowlist' ? [...selectedSet()] : [];
        if (mode.value === 'allowlist' && !selected.length) {
          if (silent) return { error: '请选择至少一个子知识库' };
          throw new Error(`请为「${s.name || s.id}」选择至少一个子知识库`);
        }
        return { connectorId: s.id, mode: mode.value, sources: selected };
      });
    }

    if (options.length) {
      status.textContent = '';
    } else {
      status.textContent = '你还没有可用的个人知识库连接器。';
      const goBtn = document.createElement('button');
      goBtn.type = 'button';
      goBtn.className = 'button ghost small kr-goto-kb';
      goBtn.textContent = '去「我的知识库」添加连接器';
      goBtn.onclick = () => {
        dialog.close();
        if (typeof window.switchView === 'function') {
          window.switchView('knowledge');
        } else if (window.parent && window.parent !== window) {
          window.parent.postMessage({ type: 'agentflow-chat-open-view', view: 'knowledge' }, window.location.origin);
        } else {
          window.location.assign('/#knowledge');
        }
      };
      list.append(goBtn);
    }
    refreshApplyState();

    apply.onclick = async () => {
      apply.disabled = true;
      status.className = 'kr-status';
      status.textContent = '正在校验并保存…';
      try {
        const nextRefs = readers.map(read => read(false)).filter(Boolean);
        const sep = project ? '?projectId=' + encodeURIComponent(project) : '';
        const response = await fetch('/api/knowledge/reference-check' + sep, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ knowledgeRefs: nextRefs }),
        });
        if (!response.ok) {
          const errPayload = await response.json().catch(() => ({}));
          throw new Error(errPayload.error || '引用校验失败');
        }
        await save(nextRefs);
        dialog.close();
      } catch (e) {
        status.className = 'kr-status error';
        status.textContent = e.message;
        refreshApplyState();
      }
    };
  } catch (e) {
    status.className = 'kr-status error';
    status.textContent = e.message;
  }
};
