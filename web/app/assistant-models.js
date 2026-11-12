// Shared AI model catalog for the classic SPA. The SOP preview, role, Bot,
// scheduler, run-eval and requirement-clarification pickers all read
// window.AgentFlowAssistantModels, so this file must load before them.
// Only the picker chrome lives here; per-feature selections stay in the
// callers (localStorage keys, hidden inputs, form fields).
(() => {
  const DEFAULT_MODEL = 'default';
  const FAMILY_MARK = {claude:'',gpt:'',glm:'智',gemini:'G',kimi:'K',deepseek:'D',minimax:'M',hunyuan:'混',mimo:'Mi',echo:'E',other:'AI'};
  const FAMILY_ICON = {
    claude:'/images/credentials/claude.svg',
    gpt:'/images/credentials/codex.svg',
    glm:'/images/credentials/glm.svg',
    gemini:'/images/credentials/gemini.svg',
    kimi:'/images/credentials/kimi.svg',
    deepseek:'/images/credentials/deepseek.svg',
    minimax:'/images/credentials/minimax.svg',
    hunyuan:'/images/credentials/hunyuan.svg',
    mimo:'/images/credentials/mimo.svg'
  };
  let catalog = {
    default: DEFAULT_MODEL,
    models: [{id: DEFAULT_MODEL, name: 'CLI 默认模型', family: 'other'}]
  };
  let loading = null;

  function familyOf(id) {
    const v = String(id || '').toLowerCase();
    if (v.startsWith('claude')) return 'claude';
    if (v.startsWith('gpt') || v.includes('codex')) return 'gpt';
    if (v.startsWith('glm')) return 'glm';
    if (v.startsWith('gemini')) return 'gemini';
    if (v.startsWith('kimi')) return 'kimi';
    if (v.startsWith('deepseek')) return 'deepseek';
    if (v.startsWith('minimax')) return 'minimax';
    if (v.startsWith('hy') || v.includes('hunyuan')) return 'hunyuan';
    if (v.startsWith('mimo') || v.startsWith('xiaomi')) return 'mimo';
    if (v === 'echo') return 'echo';
    return 'other';
  }
  // engineOf names the CLI that runs a model: catalog entries carry it, and a
  // saved "codex:…" id that the viewer cannot list still keeps its prefix.
  function engineOf(id) {
    const listed = (catalog.models || []).find(m => m.id === id);
    if (listed) return listed.engine || 'codebuddy';
    const value = String(id || '');
    const prefix = value.slice(0, Math.max(0, value.indexOf(':')));
    return (catalog.engines || []).some(e => e.id === prefix) ? prefix : 'codebuddy';
  }
  function modelMeta(id) {
    return (catalog.models || []).find(m => m.id === id) || {id: id || DEFAULT_MODEL, family: familyOf(id), name: id || DEFAULT_MODEL};
  }
  function fillModelIcon(el, family, id) {
    if (!el) return;
    el.dataset.family = family || 'other';
    const src = FAMILY_ICON[family];
    if (src) { el.innerHTML = `<img src="${src}" alt="">`; return; }
    el.textContent = FAMILY_MARK[family] || String(id || 'AI').slice(0, 1).toUpperCase();
  }
  function creditLabel(item) {
    const credits = String(item?.credits || '').trim();
    if (!credits) return '';
    return credits.endsWith('credits') ? credits : credits + ' credits';
  }
  // paint draws a catalog entry into a <button class="ai-model-toggle">.
  function paint(toggle, selectedId) {
    if (!toggle) return;
    const meta = modelMeta(selectedId);
    const icon = toggle.querySelector('.ai-model-icon');
    if (icon) fillModelIcon(icon, meta.family || familyOf(meta.id), meta.id);
    const label = toggle.querySelector('[data-model-label]');
    if (label) label.textContent = meta.name || meta.id;
    const credits = creditLabel(meta);
    toggle.title = credits ? `${meta.name || meta.id} ${credits}` : (meta.id || '');
  }
  // paintItem draws an arbitrary {id,name,family,credits} item (agent efforts,
  // executor models) into the same toggle chrome.
  function paintItem(toggle, item) {
    if (!toggle) return;
    const id = item?.id || '';
    const icon = toggle.querySelector('.ai-model-icon');
    if (icon) fillModelIcon(icon, item?.family || familyOf(id), id);
    const label = toggle.querySelector('[data-model-label]');
    if (label) label.textContent = item?.name || id || '选择模型';
    const credits = creditLabel(item);
    toggle.title = credits ? `${item?.name || id} ${credits}` : (item?.name || id || '');
  }
  // appendModelOption draws one row of a model list and returns the element, so a
  // caller that must disable the row can do it without re-finding it.
  function appendModelOption(menu, item, selectedId, onPick) {
    const opt = document.createElement('button');
    opt.type = 'button';
    opt.setAttribute('role', 'option');
    opt.className = 'ai-model-option' + (item.id === selectedId ? ' is-active' : '');
    opt.dataset.id = item.id;
    if (item.icon !== false) {
      const icon = document.createElement('span');
      icon.className = 'ai-model-icon';
      fillModelIcon(icon, item.family || familyOf(item.id), item.id);
      opt.append(icon);
    }
    const label = document.createElement('span');
    label.className = 'ai-model-name';
    label.textContent = item.name || item.id;
    label.title = item.name ? `${item.name} (${item.id})` : item.id;
    const credits = document.createElement('span');
    credits.className = 'ai-model-credits';
    credits.textContent = creditLabel(item);
    opt.append(label, credits);
    opt.onclick = () => {
      try { onPick(item.id); } finally {
        menu.hidden = true;
        const toggle = menu.parentElement?.querySelector('[data-model-toggle]');
        if (toggle) toggle.setAttribute('aria-expanded', 'false');
      }
    };
    menu.append(opt);
    return opt;
  }
  // renderOptions renders any {id,name,family,credits,icon} list; icon:false
  // skips the icon slot (effort levels, custom model entry).
  function renderOptions(menu, items, selectedId, onPick) {
    if (!menu) return;
    menu.replaceChildren();
    for (const item of items || []) appendModelOption(menu, item, selectedId, onPick);
  }
  // renderGrouped draws the framework-grouped menu. A group whose framework cannot
  // run right now is disabled as a whole and states why in its header, so nothing
  // selectable is ever a promise the server cannot keep.
  function renderGrouped(menu, groups, selectedId, onPick) {
    if (!menu) return;
    menu.replaceChildren();
    for (const group of groups || []) {
      const reason = group.disabled ? String(group.reason || '') : '';
      const header = document.createElement('div');
      header.className = 'ai-model-group';
      header.textContent = reason ? `${group.name || group.id}｜${reason}` : (group.name || group.id);
      menu.append(header);
      for (const item of group.models || []) {
        const opt = appendModelOption(menu, item, group.disabled ? '' : selectedId, onPick);
        if (group.disabled) {
          opt.disabled = true;
          opt.title = reason;
          opt.onclick = () => {};
        }
      }
    }
  }
  function renderList(menu, selectedId, onPick) {
    renderOptions(menu, catalog.models || [], selectedId, onPick);
  }
  async function fetchCatalog() {
    const resp = await fetch('/api/assistant/models', {headers: {Accept: 'application/json'}, credentials: 'same-origin'});
    if (!resp.ok) throw new Error(`模型列表加载失败（${resp.status}）`);
    return resp.json();
  }
  // load keeps one in-flight request and keeps the built-in fallback when the
  // catalog cannot be read, so pickers never spin forever on a cold start.
  function load() {
    if (!loading) {
      loading = fetchCatalog().then(data => {
        if (data?.models?.length) catalog = {default: data.default || DEFAULT_MODEL, models: data.models, engines: data.engines || []};
        return catalog;
      }).catch(() => catalog).finally(() => { loading = null; });
    }
    return loading;
  }
  window.AgentFlowAssistantModels = {
    DEFAULT: DEFAULT_MODEL,
    catalog: () => catalog,
    engineOf,
    load,
    paint,
    paintItem,
    renderList,
    renderOptions,
    renderGrouped
  };
})();
