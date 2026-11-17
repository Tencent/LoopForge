// Shared AI model catalog for the classic SPA. The SOP preview, role, Bot,
// scheduler, run-eval and requirement-clarification pickers all read
// window.AgentFlowAssistantModels, so this file must load before them.
// Pickers for entries that only run on CodeBuddy use renderCodeBuddyList.
// Only the picker chrome lives here; per-feature selections stay in the
// callers (localStorage keys, hidden inputs, form fields).
//
// bindPicker is the classic-SPA twin of the chat composer's ModelPicker
// (chatbot/src/ModelPicker.jsx): the same body-level menu, search, brand
// icons, "family · credits" line, fixed check slot, keyboard handling and
// placement, so choosing a CLI or model looks the same in every entry.
(() => {
  const DEFAULT_MODEL = 'default';
  const FAMILY_ICON = {
    claude:'/images/credentials/claude.svg',
    gpt:'/images/credentials/codex.svg',
    glm:'/images/credentials/glm.svg',
    gemini:'/images/credentials/gemini.svg',
    kimi:'/images/credentials/kimi.svg',
    deepseek:'/images/credentials/deepseek.svg',
    minimax:'/images/credentials/minimax.svg',
    hunyuan:'/images/credentials/hunyuan.svg',
    mimo:'/images/credentials/mimo.svg',
    codebuddy:'/images/credentials/codebuddy.svg',
    cursor:'/images/credentials/cursor.svg',
    opencode:'/images/credentials/opencode.svg',
    pi:'/images/credentials/pi.svg'
  };
  // The patterns of modelProviderFor in chatbot/src/ModelProviderIcon.jsx,
  // mapped onto the logos under /images/credentials. Brands are a visual
  // label only: never route a request by an inferred brand.
  const MODEL_BRANDS = [
    [/^(?:claude|anthropic)(?:$|[-_.\s:\d])/i, 'claude'],
    [/^(?:gpt|chatgpt|codex|openai)(?:$|[-_.\s:\d])|^o[1-9]\d?(?:$|[-_.\s:])/i, 'gpt'],
    [/^(?:gemini|google)(?:$|[-_.\s:\d])/i, 'gemini'],
    [/^deepseek(?:$|[-_.\s:\d])/i, 'deepseek'],
    [/^(?:chatglm|glm|zhipu)(?:$|[-_.\s:\d])/i, 'glm'],
    [/^(?:kimi|moonshot(?:ai)?)(?:$|[-_.\s:\d])/i, 'kimi'],
    [/^(?:hunyuan|tencent)(?:$|[-_.\s:\d])|^hy(?:\d|[-_.\s:])/i, 'hunyuan'],
    [/^minimax(?:$|[-_.\s:\d])/i, 'minimax'],
    [/^(?:mimo|xiaomi)(?:$|[-_.\s:\d])/i, 'mimo'],
  ];
  const FALLBACK_BRANDS = new Set(['codebuddy', 'cursor', 'opencode', 'pi']);
  // One logo per CLI, as engineBrand in chatbot/src/engines.js. `deepseek` is
  // the DeepSeek executor that runs through Codex.
  const ENGINE_BRANDS = {
    codebuddy:'codebuddy', codex:'gpt', claude:'claude', 'claude-code':'claude', opencode:'opencode',
    cursor:'cursor', agy:'gemini', kimi:'kimi', pi:'pi', dsh:'deepseek', deepseek:'deepseek', zcode:'glm'
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
  function engineOf(id, source = catalog) {
    const listed = (source.models || []).find(m => m.id === id);
    if (listed) return listed.engine || 'codebuddy';
    const value = String(id || '');
    const prefix = value.slice(0, Math.max(0, value.indexOf(':')));
    return (source.engines || []).some(e => e.id === prefix) ? prefix : 'codebuddy';
  }
  function modelMeta(id) {
    return (catalog.models || []).find(m => m.id === id) || {id: id || DEFAULT_MODEL, family: familyOf(id), name: id || DEFAULT_MODEL};
  }
  // brandOf picks the logo of a model or CLI entry: an explicit `brand` wins,
  // then the family, id and name are matched the way the chat composer does,
  // so "GPT-6 Sol" in family "other" still shows the GPT mark.
  function brandOf(item) {
    const entry = typeof item === 'string' ? {id: item} : (item || {});
    if (entry.brand) return FAMILY_ICON[entry.brand] ? entry.brand : '';
    const values = [entry.family, entry.id, entry.name];
    for (const value of values) {
      for (const part of String(value || '').trim().split('/').reverse()) {
        const hit = MODEL_BRANDS.find(([pattern]) => pattern.test(part.trim()));
        if (hit) return hit[1];
      }
    }
    return values.map(v => String(v || '').trim().toLowerCase()).find(v => FALLBACK_BRANDS.has(v)) || '';
  }
  function engineBrand(id) {
    return ENGINE_BRANDS[id] || '';
  }
  function harnessIcon(name, size) {
    return `<svg class="agent-icon harness-icon" data-icon="${name}" width="${size}" height="${size}" aria-hidden="true" focusable="false"></svg>`;
  }
  function hydrateIcons(root) {
    window.AgentFlowHarnessIcons?.hydrate?.(root);
  }
  // fillModelIcon draws the brand logo, or the neutral Harness brain glyph the
  // chat composer uses for an unknown brand (never a lettered badge).
  function fillModelIcon(el, item) {
    if (!el) return;
    const entry = typeof item === 'string' ? {id: item} : (item || {});
    const brand = brandOf(entry);
    el.dataset.family = brand || entry.family || familyOf(entry.id);
    el.dataset.brand = brand || 'unknown';
    if (brand) { el.innerHTML = `<img src="${FAMILY_ICON[brand]}" alt="">`; return; }
    el.innerHTML = harnessIcon('brain', 14);
    hydrateIcons(el);
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
    if (icon) fillModelIcon(icon, meta);
    const label = toggle.querySelector('[data-model-label]');
    if (label) label.textContent = meta.name || meta.id;
    const credits = creditLabel(meta);
    toggle.title = credits ? `${meta.name || meta.id} ${credits}` : (meta.id || '');
  }
  // paintItem draws an arbitrary {id,name,family,credits,brand} item (agent
  // efforts, executor models, CLIs) into the same toggle chrome.
  function paintItem(toggle, item) {
    if (!toggle) return;
    const id = item?.id || '';
    const icon = toggle.querySelector('.ai-model-icon');
    if (icon) fillModelIcon(icon, item || {id});
    const label = toggle.querySelector('[data-model-label]');
    if (label) label.textContent = item?.name || id || '选择模型';
    const credits = creditLabel(item);
    toggle.title = credits ? `${item?.name || id} ${credits}` : (item?.name || id || '');
  }
  // renderOptions renders any {id,name,family,credits,icon} list into an
  // inline .ai-model-menu; icon:false skips the icon slot. Pickers that have
  // moved to bindPicker no longer use it.
  function renderOptions(menu, items, selectedId, onPick) {
    if (!menu) return;
    menu.replaceChildren();
    for (const item of items || []) {
      const opt = document.createElement('button');
      opt.type = 'button';
      opt.setAttribute('role', 'option');
      opt.className = 'ai-model-option' + (item.id === selectedId ? ' is-active' : '');
      opt.dataset.id = item.id;
      if (item.icon !== false) {
        const icon = document.createElement('span');
        icon.className = 'ai-model-icon';
        fillModelIcon(icon, item);
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
    }
  }
  function renderList(menu, selectedId, onPick) {
    renderOptions(menu, catalog.models || [], selectedId, onPick);
  }
  // Requirement clarification, Bot replies, run evals and the SOP preview
  // build run on CodeBuddy only, so their pickers list CodeBuddy models and
  // never offer a "codex:…" id the server would have to refuse.
  function codeBuddyModels() {
    return (catalog.models || []).filter(m => (m.engine || 'codebuddy') === 'codebuddy');
  }
  function renderCodeBuddyList(menu, selectedId, onPick) {
    renderOptions(menu, codeBuddyModels(), selectedId, onPick);
  }
  // codeBuddyModel keeps a CodeBuddy id and turns an empty id, or a saved id
  // of another CLI, into the CLI default the server resolves itself.
  function codeBuddyModel(id) {
    const value = String(id || '').trim();
    // The catalog may not have landed yet; the built-in CLI prefixes still count.
    if (!value || /^(codex|claude-code):/.test(value) || engineOf(value) !== 'codebuddy') return DEFAULT_MODEL;
    return value;
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

  // ---- bindPicker: one menu for every CLI / model / effort choice ----
  const MENU_WIDTH = 240;
  const MENU_MAX_WIDTH = 360;
  const MENU_MAX_HEIGHT = 420;
  const CLOSE_MS = 180;
  const pickers = new WeakMap();
  let serial = 0;
  let current = null;

  function reducedMotion() {
    try { return !!window.matchMedia?.('(prefers-reduced-motion: reduce)').matches; } catch { return false; }
  }
  function nextFrame(fn) {
    return (window.requestAnimationFrame || (cb => setTimeout(cb, 16)))(fn);
  }
  function clamp(value, min, max) {
    return Math.max(min, Math.min(value, max));
  }
  function present(value) {
    return value !== undefined && value !== null && String(value).trim() !== '';
  }
  // The second line under a name: an explicit `detail`, else "family · credits"
  // exactly as the chat composer prints it.
  function optionDetail(item) {
    if (item.detail !== undefined) return String(item.detail || '');
    return [item.family, item.credits].filter(present).join(' · ');
  }
  function usable(item) {
    return !!item && !item.disabled && !item.unavailable;
  }

  // bindPicker(toggle, options) turns a <button data-model-toggle> into the
  // trigger of the shared menu. Options:
  //   items()   -> [{id, name, family, credits, detail, brand, icon, disabled}]
  //   value()   -> the selected id
  //   onPick(id, item)   called after the menu closes
  //   label            menu name for assistive technology
  //   search           true (default), false, or 'auto' (more than 5 items)
  //   searchPlaceholder / emptyText
  //   onOpen()         runs before the menu renders (refresh items, hide hints)
  // Binding the same toggle again only replaces the options.
  function bindPicker(toggle, options) {
    if (!toggle) return null;
    const known = pickers.get(toggle);
    if (known) { known.configure(options); return known; }
    const doc = toggle.ownerDocument || document;
    const identity = 'lfPicker' + (++serial);
    let opts = options || {};
    let panel = null;
    let list = null;
    let search = null;
    let query = '';
    let closeTimer = 0;
    let detach = null;

    const items = () => (typeof opts.items === 'function' ? opts.items() : opts.items) || [];
    const selected = () => (typeof opts.value === 'function' ? opts.value() : opts.value);
    const isOpen = () => !!panel && panel.dataset.phase !== 'closing';
    const inside = target => !!target && (toggle.contains(target) || !!panel?.contains(target));
    const searchable = all => opts.search === 'auto' ? all.length > 5 : opts.search !== false;

    function filtered() {
      const needle = query.trim().toLocaleLowerCase();
      const all = items();
      if (!needle) return all;
      return all.filter(item => [item.name, item.id, item.family].filter(Boolean).join(' ').toLocaleLowerCase().includes(needle));
    }
    function renderList() {
      const value = selected();
      const all = filtered();
      // Rows keep one column grid: when any entry has a logo, the others get an
      // empty slot so names stay aligned.
      const icons = items().some(item => item.icon !== false);
      list.replaceChildren();
      for (const item of all) {
        const option = doc.createElement('button');
        option.type = 'button';
        option.className = 'lf-picker-option';
        option.setAttribute('role', 'menuitemradio');
        option.setAttribute('aria-checked', String(item.id === value));
        option.dataset.id = item.id;
        option.disabled = !usable(item);
        if (icons) {
          const icon = doc.createElement('span');
          icon.className = 'ai-model-icon' + (item.icon === false ? ' is-empty' : '');
          icon.setAttribute('aria-hidden', 'true');
          if (item.icon !== false) fillModelIcon(icon, item);
          option.append(icon);
        }
        const copy = doc.createElement('span');
        copy.className = 'lf-picker-copy';
        const name = doc.createElement('strong');
        name.textContent = item.name || item.id;
        copy.append(name);
        const detail = optionDetail(item);
        if (detail) {
          const small = doc.createElement('small');
          small.textContent = detail;
          copy.append(small);
        }
        const check = doc.createElement('span');
        check.className = 'lf-picker-check';
        if (item.id === value) check.innerHTML = harnessIcon('check', 14);
        option.append(copy, check);
        option.addEventListener('click', () => choose(item));
        list.append(option);
      }
      if (!all.length) {
        const empty = doc.createElement('p');
        empty.className = 'lf-picker-empty';
        empty.setAttribute('role', 'status');
        empty.textContent = opts.emptyText || '没有匹配的选项';
        list.append(empty);
      }
      hydrateIcons(list);
    }
    function build() {
      panel = doc.createElement('div');
      panel.id = identity;
      panel.className = 'lf-picker-menu';
      panel.setAttribute('role', 'menu');
      panel.setAttribute('aria-label', opts.label || '选择');
      panel.dataset.phase = reducedMotion() ? 'open' : 'entering';
      search = null;
      if (searchable(items())) {
        const bar = doc.createElement('label');
        bar.className = 'lf-picker-search';
        bar.innerHTML = harnessIcon('search', 16);
        search = doc.createElement('input');
        search.type = 'text';
        search.autocomplete = 'off';
        search.spellcheck = false;
        search.placeholder = opts.searchPlaceholder || '搜索';
        search.setAttribute('aria-label', opts.searchPlaceholder || '搜索');
        const clear = doc.createElement('button');
        clear.type = 'button';
        clear.className = 'lf-picker-search-clear';
        clear.setAttribute('aria-label', '清空搜索');
        clear.innerHTML = harnessIcon('close', 13);
        clear.hidden = true;
        search.addEventListener('input', () => {
          query = search.value;
          clear.hidden = !query;
          renderList();
          place();
        });
        clear.addEventListener('click', () => {
          query = '';
          search.value = '';
          clear.hidden = true;
          renderList();
          place();
          search.focus({preventScroll: true});
        });
        bar.append(search, clear);
        panel.append(bar);
      }
      list = doc.createElement('div');
      list.className = 'lf-picker-options';
      panel.append(list);
      panel.addEventListener('keydown', keyDown);
      doc.body.append(panel);
      renderList();
      hydrateIcons(panel);
    }
    // Same rule as the chat composer: open below unless that side is cramped
    // and the top has more room; stay inside the visual viewport.
    function place() {
      if (!panel) return;
      if (toggle.isConnected === false) { close(false); return; }
      const vv = window.visualViewport;
      const minX = (vv?.offsetLeft || 0) + 8;
      const minY = (vv?.offsetTop || 0) + 8;
      const maxX = minX + (vv?.width || window.innerWidth) - 16;
      const maxY = minY + (vv?.height || window.innerHeight) - 16;
      const anchor = toggle.getBoundingClientRect();
      const anchorTop = clamp(anchor.top, minY, maxY);
      const anchorBottom = clamp(anchor.bottom, minY, maxY);
      const width = Math.min(clamp(anchor.width, MENU_WIDTH, MENU_MAX_WIDTH), maxX - minX);
      panel.style.width = width + 'px';
      panel.style.maxHeight = 'none';
      const desired = Math.min(MENU_MAX_HEIGHT, panel.scrollHeight || 0);
      const above = Math.max(0, anchorTop - minY - 6);
      const below = Math.max(0, maxY - anchorBottom - 6);
      const placement = below < Math.min(280, desired) && above > below ? 'top' : 'bottom';
      const maxHeight = Math.min(MENU_MAX_HEIGHT, placement === 'top' ? above : below);
      const height = Math.min(desired, maxHeight);
      panel.style.maxHeight = maxHeight + 'px';
      panel.style.left = Math.round(clamp(anchor.left, minX, maxX - width)) + 'px';
      panel.style.top = Math.round(clamp(placement === 'top' ? anchorTop - 6 - height : anchorBottom + 6, minY, maxY - height)) + 'px';
      panel.dataset.placement = placement;
    }
    function listen() {
      const outside = event => { if (!inside(event.target)) close(false); };
      const scroll = event => { if (!panel?.contains(event.target)) place(); };
      doc.addEventListener('pointerdown', outside, true);
      doc.addEventListener('focusin', outside, true);
      doc.addEventListener('scroll', scroll, true);
      window.addEventListener('resize', place);
      window.visualViewport?.addEventListener('resize', place);
      window.visualViewport?.addEventListener('scroll', place);
      return () => {
        doc.removeEventListener('pointerdown', outside, true);
        doc.removeEventListener('focusin', outside, true);
        doc.removeEventListener('scroll', scroll, true);
        window.removeEventListener('resize', place);
        window.visualViewport?.removeEventListener('resize', place);
        window.visualViewport?.removeEventListener('scroll', place);
      };
    }
    function open() {
      if (toggle.disabled || isOpen()) return;
      if (panel) { clearTimeout(closeTimer); panel.remove(); panel = null; }
      // Only one Harness popover at a time: a native selector or date panel
      // that opens next closes this menu, and this menu closes them.
      if (current && current !== api) current.close(false);
      window.HuiPopover?.claim?.(close, null);
      current = api;
      opts.onOpen?.();
      query = '';
      build();
      place();
      toggle.setAttribute('aria-haspopup', 'menu');
      toggle.setAttribute('aria-expanded', 'true');
      toggle.setAttribute('aria-controls', identity);
      detach = listen();
      const opened = panel;
      if (opened.dataset.phase === 'entering') nextFrame(() => { if (opened.dataset.phase === 'entering') opened.dataset.phase = 'open'; });
      const options = [...list.querySelectorAll('.lf-picker-option')].filter(option => !option.disabled);
      (search || options.find(option => option.getAttribute('aria-checked') === 'true') || options[0])?.focus({preventScroll: true});
    }
    function close(returnFocus) {
      if (!isOpen()) return;
      window.HuiPopover?.release?.(close);
      if (current === api) current = null;
      detach?.();
      detach = null;
      toggle.setAttribute('aria-expanded', 'false');
      toggle.removeAttribute('aria-controls');
      const closing = panel;
      closing.inert = true;
      closing.setAttribute('aria-hidden', 'true');
      if (returnFocus) toggle.focus({preventScroll: true});
      if (reducedMotion()) {
        closing.remove();
        panel = null;
      } else {
        closing.dataset.phase = 'closing';
        closeTimer = setTimeout(() => {
          closing.remove();
          if (panel === closing) panel = null;
        }, CLOSE_MS);
      }
      opts.onClose?.();
    }
    function choose(item) {
      if (!usable(item)) return;
      close(true);
      opts.onPick?.(item.id, item);
    }
    function keyDown(event) {
      if (event.isComposing) return;
      if (event.key === 'Escape') {
        // Keep the surrounding dialog open; only the menu closes.
        event.preventDefault();
        event.stopPropagation();
        close(true);
        return;
      }
      if (event.key === 'Tab') {
        // Resume the form's tab order from the trigger, not from the body-level menu.
        toggle.focus({preventScroll: true});
        close(false);
        return;
      }
      const inSearch = event.target === search;
      if (inSearch && event.key === 'Enter') {
        event.preventDefault();
        choose(filtered().find(usable));
        return;
      }
      if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key) || (inSearch && ['Home', 'End'].includes(event.key))) return;
      const options = [...list.querySelectorAll('.lf-picker-option')].filter(option => !option.disabled);
      if (!options.length) return;
      event.preventDefault();
      const index = options.indexOf(doc.activeElement);
      const next = event.key === 'Home' ? 0 : event.key === 'End' ? options.length - 1 :
        index < 0 ? (event.key === 'ArrowDown' ? 0 : options.length - 1) :
        (index + (event.key === 'ArrowDown' ? 1 : -1) + options.length) % options.length;
      options[next].focus({preventScroll: true});
      options[next].scrollIntoView?.({block: 'nearest'});
    }

    const api = {
      open,
      close,
      isOpen,
      // Re-render an open menu after its items change (catalog landed, CLI switched).
      refresh() {
        if (!isOpen()) return;
        const focused = list.contains(doc.activeElement) ? doc.activeElement.closest('.lf-picker-option') : null;
        const focusedId = focused?.dataset.id;
        const scrollTop = list.scrollTop;
        renderList();
        if (focused) {
          const options = [...list.querySelectorAll('.lf-picker-option')].filter(option => !option.disabled);
          // Catalog refreshes keep keyboard navigation on the same model;
          // if it disappeared, return to search (or the first usable row).
          (options.find(option => option.dataset.id === focusedId) || search || options[0])?.focus({preventScroll: true});
        }
        list.scrollTop = scrollTop;
        place();
      },
      configure(next) { if (next) opts = next; },
    };
    toggle.setAttribute('aria-haspopup', 'menu');
    toggle.setAttribute('aria-expanded', 'false');
    toggle.addEventListener('click', event => {
      event.stopPropagation();
      if (isOpen()) close(false); else open();
    });
    toggle.addEventListener('keydown', event => {
      if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
        event.preventDefault();
        open();
      }
    });
    pickers.set(toggle, api);
    return api;
  }

  window.AgentFlowAssistantModels = {
    DEFAULT: DEFAULT_MODEL,
    bindPicker,
    brandOf,
    catalog: () => catalog,
    codeBuddyModel,
    codeBuddyModels,
    engineBrand,
    engineOf,
    load,
    paint,
    paintItem,
    renderCodeBuddyList,
    renderList,
    renderOptions
  };
})();
