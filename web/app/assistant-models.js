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
    codebuddy:'/images/credentials/codebuddy.svg',
    cursor:'/images/credentials/cursor.svg',
    gpt:'/images/credentials/codex.svg',
    glm:'/images/credentials/glm.svg',
    gemini:'/images/credentials/gemini.svg',
    grok:'/images/credentials/xai.svg',
    muse:'/images/credentials/meta.svg',
    kimi:'/images/credentials/kimi.svg',
    moonshot:'/images/credentials/moonshot.svg',
    qwen:'/images/credentials/qwen.svg',
    doubao:'/images/credentials/doubao.svg',
    qianfan:'/images/credentials/qianfan.svg',
    deepseek:'/images/credentials/deepseek.svg',
    minimax:'/images/credentials/minimax.svg',
    hunyuan:'/images/credentials/hunyuan.svg',
    mimo:'/images/credentials/mimo.svg'
  };
  const CLI_ICON = {
    codebuddy:'codebuddy.svg', codex:'codex.svg', 'claude-code':'claude.svg',
    opencode:'opencode.svg', cursor:'cursor.svg', agy:'antigravity.svg',
    kimi:'kimi-code.png', pi:'pi.svg', dsh:'deepseek.svg', zcode:'glm.svg'
  };
  let catalog = {
    default: DEFAULT_MODEL,
    models: []
  };
  let loading = null;

  function familyOf(id) {
    const raw = String(id || '').toLowerCase();
    const v = raw.replace(/^(?:codebuddy|codex|claude-code|opencode|cursor|agy|kimi|pi|dsh|zcode):/, '').split('/').at(-1);
    if (/^(?:claude|anthropic|opus|sonnet|haiku|fable)(?:$|[-_.\s:\d])/.test(v)) return 'claude';
    if (/^composer(?:$|[-_.\s:\d])/.test(v) || (raw.startsWith('cursor:') && /^auto(?:-smart)?$/.test(v))) return 'cursor';
    if (v.startsWith('gpt') || v.includes('codex')) return 'gpt';
    if (v.startsWith('glm')) return 'glm';
    if (v.startsWith('gemini')) return 'gemini';
    if (/^(?:cursor-)?grok(?:$|[-_.\s:\d])/.test(v)) return 'grok';
    if (/^muse(?:$|[-_.\s:\d])/.test(v)) return 'muse';
    if (/^(?:kimi|k[23])(?:$|[-_.\s:])/.test(v)) return 'kimi';
    if (/^moonshot(?:ai)?(?:$|[-_.\s:\d])/.test(v)) return 'moonshot';
    if (/^(?:qwen|qvq|alibaba)(?:$|[-_.\s:\d])/.test(v)) return 'qwen';
    if (/^(?:doubao|seed|seedream|bytedance)(?:$|[-_.\s:\d])/.test(v)) return 'doubao';
    if (/^(?:ernie|wenxin|baidu|qianfan)(?:$|[-_.\s:\d])/.test(v)) return 'qianfan';
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
    return Object.hasOwn(CLI_ICON, prefix) ? prefix : 'codebuddy';
  }
  function modelMeta(id) {
    const value = id || DEFAULT_MODEL;
    const listed = catalog.models.find(m => m.id === value || ('codebuddy:' + m.id === value && !m.engine));
    if (listed) return {...listed, id: value};
    return {id: value, family: familyOf(value), name: value === DEFAULT_MODEL ? '默认' : value};
  }
  function concreteModelId(id) {
    const value = String(id || '').trim();
    const separator = value.indexOf(':');
    const prefix = value.slice(0, Math.max(0, separator));
    const qualified = Object.hasOwn(CLI_ICON, prefix);
    const engine = qualified ? prefix : 'codebuddy';
    const model = qualified ? value.slice(separator + 1) : value;
    return model === DEFAULT_MODEL || (engine === 'codebuddy' && model === 'default-model') || !model ? '' : value;
  }
  function fillModelIcon(el, family, id) {
    if (!el) return;
    if (!family || family === 'other') family = familyOf(id);
    el.dataset.family = family || 'other';
    const src = FAMILY_ICON[family];
    if (src) {
      el.innerHTML = `<img src="${src}" alt="">` + (family === 'kimi' ? '<img src="/images/credentials/kimi-dark.svg" alt="">' : '');
      return;
    }
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
  function paintFeature(toggle, selectedId) {
    const selected = concreteModelId(selectedId);
    paintItem(toggle, selected ? modelMeta(selected) : null);
  }
  // renderOptions renders any {id,name,family,credits,icon} list; icon:false
  // skips the icon slot (effort levels, custom model entry).
  function renderOptions(menu, items, selectedId, onPick) {
    if (!menu) return;
    menu.replaceChildren();
    for (const item of items || []) {
      const opt = document.createElement('button');
      opt.type = 'button';
      opt.setAttribute('role', 'option');
      opt.setAttribute('aria-selected', String(item.id === selectedId));
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
    }
  }
  function renderList(menu, selectedId, onPick) {
    renderOptions(menu, catalog.models || [], selectedId, onPick);
  }
  function renderFeatureList(menu, selectedId, onPick) {
    const selected = concreteModelId(selectedId);
    const items = [];
    for (const model of catalog.models) {
      const engine = catalog.engines?.find(item => item.id === engineOf(model.id));
      if (engine && !(engine.personal ?? engine.configured)) continue;
      const id = !model.engine && selected === 'codebuddy:' + model.id ? selected : model.id;
      items.push({...model, id, name: engine ? model.name + ' · ' + engine.name : model.name});
    }
    if (selected && !items.some(item => item.id === selected)) {
      items.push(modelMeta(selected));
    }
    renderOptions(menu, items, selected, onPick);
    if (!items.length) {
      const empty = document.createElement('div');
      empty.className = 'ai-model-empty';
      empty.textContent = '暂无可选模型';
      menu.append(empty);
    }
  }
  async function fetchCatalog() {
    const resp = await fetch('/api/assistant/models', {headers: {Accept: 'application/json'}, credentials: 'same-origin'});
    if (!resp.ok) throw new Error(`模型列表加载失败（${resp.status}）`);
    return resp.json();
  }
  // Share the in-flight request and retain the last catalog on a refresh failure.
  function load() {
    if (!loading) {
      loading = fetchCatalog().then(data => {
        if (Array.isArray(data?.models)) {
          const models = data.models.filter(m => concreteModelId(m.id));
          catalog = {default: data.default || DEFAULT_MODEL, models, engines: data.engines || []};
        }
        return catalog;
      }).catch(() => catalog).finally(() => { loading = null; });
    }
    return loading;
  }
  window.AgentFlowAssistantModels = {
    DEFAULT: DEFAULT_MODEL,
    catalog: () => catalog,
    cliIcon: id => Object.hasOwn(CLI_ICON, id) ? '/images/credentials/' + CLI_ICON[id] : '',
    concreteModelId,
    engineOf,
    load,
    paint,
    paintFeature,
    paintItem,
    renderFeatureList,
    renderList,
    renderOptions
  };
})();
