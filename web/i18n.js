// Runtime UI translation. The app renders Chinese copy directly; in English
// mode this layer rewrites text nodes and a few attributes as they appear,
// using the zh→en dictionary in i18n-en.js. Chinese mode is a no-op.
(() => {
  const root = document.documentElement;
  const lang = root.dataset.lang === 'en' ? 'en' : 'zh';
  window.lfLang = () => lang;
  window.lfSetLang = next => {
    localStorage.setItem('loopforge.lang', next === 'en' ? 'en' : 'zh');
    location.reload();
  };
  window.lfT = (zh, en) => (lang === 'en' ? en : zh);
  if (lang !== 'en') return;

  const dict = window.LF_I18N_EN || {};
  const HAN = /[\u3400-\u9fff\u300c\u300d\uff0c\u3002\uff1a\uff08\uff09\u3001\uff1b\uff01\uff1f]/;
  const SKIP = new Set(['SCRIPT', 'STYLE', 'TEXTAREA', 'CODE', 'PRE', 'NOSCRIPT']);
  const SKIP_SEL = '[data-i18n-skip],.monaco-editor,.xterm,.log-line,.run-log,.markdown-body,[contenteditable="true"]';
  // User-authored text only gets whole-string dictionary hits, never fragment
  // rewrites, so a sentence typed by a person is not turned into mixed prose.
  const DATA_SEL = '[data-i18n-data],.ai-user-bubble,.ai-think-body,.collab-process-body,.collab-list-item,.issue-title-cell b,#issueDetailTitle,.detail-desc,.comment-row p';
  const ATTRS = ['placeholder', 'title', 'aria-label', 'data-tip'];
  const escape = s => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const fragments = Object.keys(dict).filter(k => k.length > 1).sort((a, b) => b.length - a.length);
  const fragmentRE = fragments.length ? new RegExp('(?<![\\u3400-\\u9fff])(?:' + fragments.map(escape).join('|') + ')(?![\\u3400-\\u9fff])', 'g') : null;
  const sentences = fragments.filter(k => k.length >= 6 && HAN.test(k));
  const sentenceRE = sentences.length ? new RegExp(sentences.map(escape).join('|'), 'g') : null;
  const punct = [[/，/g, ', '], [/。/g, '. '], [/：/g, ': '], [/；/g, '; '], [/、/g, ', '], [/！/g, '! '], [/？/g, '? '], [/（/g, ' ('], [/）/g, ') '], [/「|」/g, '"'], [/『|』/g, '"']];
  const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  const UNITS = {次: ['time', 'times'], 步: ['step', 'steps'], 个: ['', ''], 台: ['', ''], 条: ['', '']};
  const patterns = [
    [/第\s*(\d+)\s*(?:阶段|Stage)/g, 'Stage $1'],
    [/第\s*(\d+)\s*(?:轮|Round)/g, 'Round $1'],
    [/ · (高|中|低) · /g, (_, p) => ` · ${{高: 'High', 中: 'Medium', 低: 'Low'}[p]} · `],
    [/入\s*(\d+)\s*\/\s*出\s*(\d+)/g, 'In $1 / Out $2'],
    [/(\d{4})年(\d{1,2})月/g, (_, y, m) => `${MONTHS[m - 1] || m} ${y}`],
    [/(^|[^\d])(\d{1,2})月(?![\u3400-\u9fff])/g, (_, pre, m) => pre + (MONTHS[m - 1] || m)],
    [/共\s*(\d+)\s*(?=[A-Za-z])/g, '$1 '],
    [/(\d{4}-\d{2}-\d{2})\s*至\s*(\d{4}-\d{2}-\d{2})/g, '$1 – $2'],
    [/(\d+(?:\.\d+)?)\s*(次|步|个|台|条)(?![\u3400-\u9fff])/g, (_, n, u) => {
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
  function walk(node) {
    if (node.nodeType === 3) return translateText(node);
    if (node.nodeType !== 1) return;
    if (skipped(node)) { if (node.tagName === 'TEXTAREA') translateAttrs(node); return; }
    translateAttrs(node);
    const tw = document.createTreeWalker(node, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT, {
      acceptNode: n => {
      if (n.nodeType !== 1 || !skipped(n)) return NodeFilter.FILTER_ACCEPT;
      if (n.tagName === 'TEXTAREA') translateAttrs(n);
      return NodeFilter.FILTER_REJECT;
    },
    });
    let n;
    while ((n = tw.nextNode())) {
      if (n.nodeType === 3) translateText(n);
      else translateAttrs(n);
    }
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
