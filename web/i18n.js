// Runtime UI translation. The app renders Chinese copy directly; in English
// mode this layer rewrites text nodes and a few attributes as they appear,
// using the zh→en dictionary in i18n-en.js. Chinese mode is a no-op.
(() => {
  const root = document.documentElement;
  const lang = root.dataset.lang === 'en' ? 'en' : 'zh';
  window.lfLang = () => lang;
  window.lfSetLang = next => {
    const value = next === 'en' ? 'en' : 'zh';
    const current = root.dataset.lang === 'en' ? 'en' : 'zh';
    if (value === current || root.classList.contains('lang-leaving')) return;
    try { localStorage.setItem('loopforge.lang', value); } catch { /* A blocked store keeps the previous language after reload. */ }
    const groups = [...document.querySelectorAll('.lang-switch')];
    for (const group of groups) {
      group.querySelectorAll('[data-lang-value]').forEach(btn => {
        const on = btn.dataset.langValue === value;
        btn.classList.toggle('active', on);
        btn.setAttribute('aria-checked', String(on));
      });
    }
    const reduce = matchMedia('(prefers-reduced-motion: reduce)').matches;
    if (reduce) { location.reload(); return; }
    try { sessionStorage.setItem('loopforge.lang.motion', 'in'); } catch { /* Fade-in is skipped when the flag cannot be stored. */ }
    root.classList.add('lang-leaving');
    let done = false;
    const finish = () => { if (!done) { done = true; location.reload(); } };
    const indicator = groups[0]?.querySelector(':scope > [data-sliding-indicator]');
    if (indicator) {
      indicator.addEventListener('transitionend', event => {
        if (event.target === indicator && (event.propertyName === 'transform' || event.propertyName === 'width')) finish();
      });
    }
    setTimeout(finish, 560);
  };
  window.lfT = (zh, en) => (lang === 'en' ? en : zh);

  function bindLangSwitch() {
    const current = root.dataset.lang === 'en' ? 'en' : 'zh';
    document.querySelectorAll('.lang-switch').forEach(group => {
      group.querySelectorAll('[data-lang-value]').forEach(btn => {
        const on = btn.dataset.langValue === current;
        btn.classList.toggle('active', on);
        btn.setAttribute('aria-checked', String(on));
        btn.tabIndex = on ? 0 : -1;
        if (btn.dataset.langBound) return;
        btn.dataset.langBound = '1';
        btn.addEventListener('click', () => window.lfSetLang(btn.dataset.langValue));
      });
      if (!group.dataset.langKeys) {
        group.dataset.langKeys = '1';
        group.addEventListener('keydown', event => {
          if (event.altKey || event.ctrlKey || event.metaKey || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
          const buttons = [...group.querySelectorAll('[data-lang-value]')];
          if (!buttons.length) return;
          event.preventDefault();
          const index = Math.max(0, buttons.findIndex(btn => btn.classList.contains('active')));
          const nextIndex = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + buttons.length) % buttons.length;
          buttons[nextIndex].focus();
          buttons[nextIndex].click();
        });
      }
      window.AgentFlowSlidingTabs?.mount(group, { activeSelector: '.active', auto: true, compact: true, cacheKey: group.id || 'lang-switch' });
    });
  }
  function finishLangEnter() {
    if (!root.classList.contains('lang-enter')) return;
    try { sessionStorage.removeItem('loopforge.lang.motion'); } catch { /* The flag only skips the splash on the next load. */ }
    const shell = document.querySelector('.app-shell, .login-card');
    const clear = () => root.classList.remove('lang-enter', 'lang-shown');
    requestAnimationFrame(() => requestAnimationFrame(() => {
      root.classList.add('lang-shown');
      if (!shell) { clear(); return; }
      shell.addEventListener('transitionend', event => {
        if (event.target === shell && event.propertyName === 'opacity') clear();
      });
      setTimeout(clear, 700);
    }));
  }
  function startLangMotion() {
    bindLangSwitch();
    finishLangEnter();
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', startLangMotion, { once: true });
  else startLangMotion();

  if (lang !== 'en') return;

  const dict = window.LF_I18N_EN || {};
  const HAN = /[\u3400-\u9fff\u300c\u300d\uff0c\u3002\uff1a\uff08\uff09\u3001\uff1b\uff01\uff1f]/;
  const SKIP = new Set(['SCRIPT', 'STYLE', 'TEXTAREA', 'CODE', 'PRE', 'NOSCRIPT']);
  const SKIP_SEL = '[data-i18n-skip],.monaco-editor,.xterm,.log-line,.run-log,.markdown-body,[contenteditable="true"]';
  // User-authored text only gets whole-string dictionary hits, never fragment
  // rewrites, so a sentence typed by a person is not turned into mixed prose.
  const DATA_SEL = '[data-i18n-data],.ai-user-bubble,.ai-think-body,.collab-process-body,.collab-list-item,.issue-title-cell b,#issueDetailTitle,.detail-desc,.comment-row p';
  const ATTRS = ['placeholder', 'title', 'aria-label', 'data-tip', 'data-tooltip'];
  const escape = s => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const fragments = Object.keys(dict).filter(k => k.length > 1).sort((a, b) => b.length - a.length);
  const fragmentRE = fragments.length ? new RegExp('(?<![\\u3400-\\u9fff])(?:' + fragments.map(escape).join('|') + ')(?![\\u3400-\\u9fff])', 'g') : null;
  const sentences = fragments.filter(k => k.length >= 6 && HAN.test(k));
  const sentenceRE = sentences.length ? new RegExp(sentences.map(escape).join('|'), 'g') : null;
  const punct = [[/，/g, ', '], [/。/g, '. '], [/：/g, ': '], [/；/g, '; '], [/、/g, ', '], [/！/g, '! '], [/？/g, '? '], [/（/g, ' ('], [/）/g, ') '], [/「|」/g, '"'], [/『|』/g, '"']];
  const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  const UNITS = {次: ['time', 'times'], 步: ['step', 'steps'], 个: ['', ''], 台: ['', ''], 条: ['', ''], 篇: ['doc', 'docs']};
  const patterns = [
    [/第\s*(\d+)\s*(?:阶段|Stage)/g, 'Stage $1'],
    [/第\s*(\d+)\s*(?:轮|Round)/g, 'Round $1'],
    [/ · (高|中|低) · /g, (_, p) => ` · ${{高: 'High', 中: 'Medium', 低: 'Low'}[p]} · `],
    [/入\s*(\d+)\s*\/\s*出\s*(\d+)/g, 'In $1 / Out $2'],
    [/(\d{4})年(\d{1,2})月/g, (_, y, m) => `${MONTHS[m - 1] || m} ${y}`],
    [/(^|[^\d])(\d{1,2})月(?![\u3400-\u9fff])/g, (_, pre, m) => pre + (MONTHS[m - 1] || m)],
    [/共\s*(\d+)\s*(?=[A-Za-z])/g, '$1 '],
    [/共\s*(\d+)\s*(条|个|项)/g, (_, n) => `${n} ${Number(n) === 1 ? 'item' : 'items'}`],
    [/(\d{4}-\d{2}-\d{2})\s*至\s*(\d{4}-\d{2}-\d{2})/g, '$1 – $2'],
    [/(\d+(?:\.\d+)?)\s*(次|步|个|台|条|篇)(?![\u3400-\u9fff])/g, (_, n, u) => {
      const word = UNITS[u][Number(n) === 1 ? 0 : 1];
      return word ? `${n} ${word}` : n;
    }],
  ];
  const cache = new Map();

  function translateExact(text) {
    const trimmed = text && text.trim();
    return trimmed && Object.prototype.hasOwnProperty.call(dict, trimmed) ? text.replace(trimmed, dict[trimmed]) : text;
  }

  function translate(text) {
    if (!text || !HAN.test(text)) return text;
    const hit = cache.get(text);
    if (hit !== undefined) return hit;
    const trimmed = text.trim();
    let out;
    if (Object.prototype.hasOwnProperty.call(dict, trimmed)) {
      out = text.replace(trimmed, dict[trimmed]);
    } else {
      out = fragmentRE ? text.replace(fragmentRE, m => dict[m]) : text;
      if (sentenceRE && HAN.test(out)) out = out.replace(sentenceRE, m => dict[m]);
      for (const [re, rep] of patterns) out = out.replace(re, rep);
    }
    if (out !== text) {
      for (const [re, rep] of punct) out = out.replace(re, rep);
      out = out.replace(/:\s*:/g, ':').replace(/ {2,}/g, ' ').replace(/ ([,.:;!?)])/g, '$1').replace(/\( /g, '(');
      if (/^\s/.test(text) === false) out = out.replace(/^\s+/, '');
      if (/\s$/.test(text) === false) out = out.replace(/\s+$/, '');
    }
    if (cache.size > 20000) cache.clear();
    cache.set(text, out);
    return out;
  }
  window.lfTranslate = translate;

  function skipped(el) {
    return !el || SKIP.has(el.tagName) || (el.closest && el.closest(SKIP_SEL));
  }
  function translateText(node) {
    const parent = node.parentElement;
    if (skipped(parent)) return;
    const next = parent.closest(DATA_SEL) ? translateExact(node.nodeValue) : translate(node.nodeValue);
    if (next !== node.nodeValue) node.nodeValue = next;
  }
  function translateAttrs(el) {
    if (el.closest && el.closest(SKIP_SEL)) return;
    for (const name of ATTRS) {
      const value = el.getAttribute && el.getAttribute(name);
      if (value && HAN.test(value)) {
        const next = el.closest(DATA_SEL) ? translateExact(value) : translate(value);
        if (next !== value) el.setAttribute(name, next);
      }
    }
    if (el.tagName === 'INPUT' && /^(button|submit|reset)$/i.test(el.type) && HAN.test(el.value)) el.value = translate(el.value);
  }
  // Copy such as "<strong>平台所有项目共用执行名额</strong>，默认 10 条…" arrives
  // split over several text nodes by inline markup, so a dictionary key for the
  // whole sentence can never match a single node. Collect runs of text nodes and
  // plain inline wrappers inside one block and replace the run only when one
  // dictionary entry covers the whole sentence.
  const INLINE = new Set(['A', 'B', 'BR', 'CITE', 'CODE', 'EM', 'I', 'MARK', 'SMALL', 'SPAN', 'STRONG', 'TIME', 'U']);
  const KEEP_ATTRS = new Set(['class', 'style', 'title', 'aria-label', 'aria-hidden', 'role', 'lang', 'dir', 'data-i18n-skip']);
  function inlineOnly(el) {
    if (!INLINE.has(el.tagName) || !el.childNodes.length) return false;
    for (const attr of el.attributes) if (!KEEP_ATTRS.has(attr.name) && !attr.name.startsWith('data-i18n')) return false;
    for (const child of el.childNodes) {
      if (child.nodeType === 3) continue;
      if (child.nodeType !== 1 || !INLINE.has(child.tagName) || !inlineOnly(child)) return false;
    }
    return !el.closest(SKIP_SEL) && !el.closest(DATA_SEL);
  }
  function flatten(nodes) {
    return nodes.map(node => {
      if (node.nodeType === 3) return node.nodeValue;
      return node.childNodes.length ? flatten([...node.childNodes]) : (node.textContent || '');
    }).join('');
  }
  function matchRun(text, nodes) {
    // Only a single dictionary entry that covers the whole run may replace it.
    // Stitching several entries together would treat unrelated siblings (grid
    // cells, label + badge, metric label + value) as one sentence and flatten
    // them into a single text node, dropping their classes and layout.
    const trimmed = text.trim();
    const key = Object.prototype.hasOwnProperty.call(dict, trimmed)
      ? trimmed
      : (Object.prototype.hasOwnProperty.call(dict, trimmed.replace(/\s+/g, ' ')) ? trimmed.replace(/\s+/g, ' ') : null);
    if (!key) return null;
    // The entry has to cross a node boundary; if one node already holds the
    // whole sentence, translateText handles it and the markup must stay.
    const local = nodes.map(node => (node.nodeType === 3 ? node.nodeValue : node.textContent) || '');
    return local.some(value => value.includes(key)) ? null : dict[key];
  }
  function mergeRuns(el) {
    // Never rewrite rendered controls or observable subtrees: app code binds to
    // their ids and the run replacement would drop those elements. Only text
    // nodes and plain inline wrappers are safe to merge.
    if (el.closest(SKIP_SEL) || el.closest(DATA_SEL) || el.querySelector('[id],input,select,textarea,button,img,svg,iframe,[onclick],[onchange],[oninput]')) return;
    const runs = [];
    let run = null;
    for (const child of el.childNodes) {
      if (child.nodeType === 3 || (child.nodeType === 1 && inlineOnly(child))) {
        if (!run) { run = []; runs.push(run); }
        run.push(child);
      } else {
        run = null;
      }
    }
    for (const nodes of runs) {
      if (nodes.length < 2) continue;
      const text = flatten(nodes);
      if (!HAN.test(text)) continue;
      const hit = matchRun(text, nodes);
      if (!hit) continue;
      let anchor = nodes[0];
      if (anchor.nodeType === 1) {
        anchor = document.createTextNode('');
        nodes[0].replaceWith(anchor);
        nodes[0] = anchor;
      }
      anchor.nodeValue = hit;
      for (const node of nodes.slice(1)) node.remove();
    }
  }
  function walk(node) {
    if (node.nodeType === 3) return translateText(node);
    if (node.nodeType !== 1) return;
    if (skipped(node)) { if (node.tagName === 'TEXTAREA') translateAttrs(node); return; }
    translateAttrs(node);
    mergeRuns(node);
    for (const child of [...node.childNodes]) walk(child);
  }
  function translateTitle() {
    const next = translate(document.title);
    if (next !== document.title) document.title = next;
  }

  const observer = new MutationObserver(records => {
    for (const r of records) {
      if (r.type === 'childList') r.addedNodes.forEach(walk);
      else if (r.type === 'characterData') translateText(r.target);
      else if (r.type === 'attributes') translateAttrs(r.target);
    }
  });
  function start() {
    walk(document.body);
    translateTitle();
    observer.observe(document.body, {subtree: true, childList: true, characterData: true, attributes: true, attributeFilter: ATTRS});
    const title = document.querySelector('title');
    if (title) new MutationObserver(translateTitle).observe(title, {childList: true, characterData: true, subtree: true});
  }
  if (document.body) start();
  else document.addEventListener('DOMContentLoaded', start, {once: true});

  const nativeConfirm = window.confirm.bind(window);
  const nativeAlert = window.alert.bind(window);
  const nativePrompt = window.prompt.bind(window);
  window.confirm = message => nativeConfirm(translate(String(message ?? '')));
  window.alert = message => nativeAlert(translate(String(message ?? '')));
  window.prompt = (message, value) => nativePrompt(translate(String(message ?? '')), value);
})();
