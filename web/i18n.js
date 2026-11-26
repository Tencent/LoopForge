// Runtime UI translation. The app renders Chinese copy directly; in English
// or Vietnamese mode this layer rewrites text nodes and a few attributes as
// they appear, using the zh→locale dictionary in i18n-en.js / i18n-vi.js.
// Chinese mode is a no-op.
(() => {
  const root = document.documentElement;
  const raw = root.dataset.lang;
  const lang = raw === 'en' ? 'en' : 'vi';
  window.lfLang = () => lang;
  window.lfSetLang = next => {
    localStorage.setItem('loopforge.lang', next === 'en' ? 'en' : 'vi');
    location.reload();
  };
  window.lfCycleLang = () => {
    window.lfSetLang(lang === 'en' ? 'vi' : 'en');
  };
  window.lfLangLabel = () => lang === 'en'
    ? {text: 'EN / VI', title: 'English / Tiếng Việt'}
    : {text: 'VI / EN', title: 'Tiếng Việt / English'};
  // Chinese is no longer a display language. Missing keys fall through
  // the dictionary / pattern pass instead of being left in 中文.

  const dict = (lang === 'vi' ? window.LF_I18N_VI : window.LF_I18N_EN) || {};
  window.lfT = (zh, en, vi) => {
    if (lang === 'vi') return vi || dict[zh] || en || zh;
    return en || dict[zh] || zh;
  };

  const HAN = /[\u3400-\u9fff\u300c\u300d\uff0c\u3002\uff1a\uff08\uff09\u3001\uff1b\uff01\uff1f]/;
  const SKIP = new Set(['SCRIPT', 'STYLE', 'TEXTAREA', 'CODE', 'PRE', 'NOSCRIPT']);
  const SKIP_SEL = '[data-i18n-skip],.monaco-editor,.xterm,.log-line,.run-log,.markdown-body,[contenteditable="true"]';
  // User-authored text only gets whole-string dictionary hits, never fragment
  // rewrites, so a sentence typed by a person is not turned into mixed prose.
  const DATA_SEL = '[data-i18n-data],.ai-user-bubble,.ai-think-body,.collab-process-body,.collab-list-item,.issue-title-cell b,#issueDetailTitle,.detail-desc,.comment-row p';
  const ATTRS = ['placeholder', 'title', 'alt', 'aria-label', 'aria-placeholder', 'aria-description', 'aria-roledescription', 'data-tip', 'data-tooltip', 'data-title'];
  const escape = s => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  // Short labels (外观, 保存) are applied only as whole-string matches.
  // Fragment replace of 1–2 character keys produced mixed Chinese/Vietnamese.
  const fragments = Object.keys(dict).filter(k => {
    if (((k.match(/[\u3400-\u9fff]/g) || []).length < 3)) return false;
    const val = dict[k];
    return typeof val === 'string' && val.length && !HAN.test(val);
  }).sort((a, b) => b.length - a.length);
  const fragmentREs = [];
  const CHUNK = 200;
  for (let i = 0; i < fragments.length; i += CHUNK) {
    const chunk = fragments.slice(i, i + CHUNK);
    fragmentREs.push(new RegExp('(?<![\\u3400-\\u9fff])(?:' + chunk.map(escape).join('|') + ')(?![\\u3400-\\u9fff])', 'g'));
  }
  function applyFragments(text) {
    const wordy = /[A-Za-zÀ-ỹ0-9]/;
    for (const re of fragmentREs) {
      text = text.replace(re, (m, offset, full) => {
        let rep = dict[m];
        if (typeof rep !== 'string') return m;
        const before = full[offset - 1] || '';
        const after = full[offset + m.length] || '';
        if (wordy.test(before) && wordy.test(rep[0] || '')) rep = ' ' + rep;
        if (wordy.test(rep[rep.length - 1] || '') && wordy.test(after)) rep = rep + ' ';
        return rep;
      });
    }
    return text;
  }
  const punct = [[/，/g, ', '], [/。/g, '. '], [/：/g, ': '], [/；/g, '; '], [/、/g, ', '], [/！/g, '! '], [/？/g, '? '], [/（/g, ' ('], [/）/g, ') '], [/「|」/g, '"'], [/『|』/g, '"']];
  const MONTHS_EN = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  const MONTHS_VI = ['Thg 1', 'Thg 2', 'Thg 3', 'Thg 4', 'Thg 5', 'Thg 6', 'Thg 7', 'Thg 8', 'Thg 9', 'Thg 10', 'Thg 11', 'Thg 12'];
  const MONTHS = lang === 'vi' ? MONTHS_VI : MONTHS_EN;
  const UNITS = lang === 'vi'
    ? {次: ['lần', 'lần'], 步: ['bước', 'bước'], 个: ['', ''], 台: ['', ''], 条: ['', '']}
    : {次: ['time', 'times'], 步: ['step', 'steps'], 个: ['', ''], 台: ['', ''], 条: ['', '']};
  const level = lang === 'vi' ? {高: 'Cao', 中: 'Trung bình', 低: 'Thấp'} : {高: 'High', 中: 'Medium', 低: 'Low'};
  const stage = lang === 'vi' ? 'Giai đoạn $1' : 'Stage $1';
  const round = lang === 'vi' ? 'Vòng $1' : 'Round $1';
  const inout = lang === 'vi' ? 'Vào $1 / Ra $2' : 'In $1 / Out $2';
  const stepOf = lang === 'vi' ? 'Bước $1/$2' : 'Step $1 of $2';
  const stepOnly = lang === 'vi' ? 'Bước $1' : 'Step $1';
  const patterns = [
    [/第\s*(\d+)\s*步[，,]\s*共\s*(\d+)\s*步/g, stepOf],
    [/第\s*(\d+)\s*步(?![\u3400-\u9fff])/g, stepOnly],
    [/第\s*(\d+)\s*(?:阶段|Stage)/g, stage],
    [/第\s*(\d+)\s*(?:轮|Round)/g, round],
    [/ · (高|中|低) · /g, (_, p) => ` · ${level[p]} · `],
    [/入\s*(\d+)\s*\/\s*出\s*(\d+)/g, inout],
    [/(\d{4})年(\d{1,2})月/g, (_, y, m) => lang === 'vi' ? `${MONTHS[m - 1] || m} ${y}` : `${MONTHS[m - 1] || m} ${y}`],
    [/(^|[^\d])(\d{1,2})月(?![\u3400-\u9fff])/g, (_, pre, m) => pre + (MONTHS[m - 1] || m)],
    [/共\s*(\d+)/g, lang === 'vi' ? 'Tổng $1' : '$1'],
    [/更新于/g, lang === 'vi' ? 'cập nhật lúc' : 'updated'],
    [/近\s*(\d+)\s*天/g, lang === 'vi' ? 'Gần $1 ngày' : 'Last $1 days'],
    [/最近\s*(\d+)\s*天/g, lang === 'vi' ? '$1 ngày gần đây' : 'Last $1 days'],
    [/成功率/g, lang === 'vi' ? 'Tỷ lệ thành công' : 'Success rate'],
    [/累计/g, lang === 'vi' ? 'Tổng' : 'Total'],
    [/(\d+)\s*人(?![\u3400-\u9fff])/g, lang === 'vi' ? '$1 người' : '$1 people'],
    [/(\d+)\s*单(?![\u3400-\u9fff])/g, lang === 'vi' ? '$1 yêu cầu' : '$1 issues'],
    [/(^|[\s·])成功\s+(\d+)/g, lang === 'vi' ? '$1Thành công $2' : '$1Succeeded $2'],
    [/(^|[\s·])失败\s+(\d+)/g, lang === 'vi' ? '$1Thất bại $2' : '$1Failed $2'],
    [/于\s+(\d{1,2}:\d{2})/g, lang === 'vi' ? 'lúc $1' : 'at $1'],
    [/默认[:：]\s*/g, lang === 'vi' ? 'Mặc định: ' : 'Default: '],
    [/创建\s+/g, lang === 'vi' ? 'Tạo ' : 'Created '],
    [/LoopForge 云端/g, 'LoopForge Cloud'],
    [/云端/g, 'Cloud'],
    [/停下(?=[\u3400-\u9fffA-Za-zÀ-ỹ])/g, lang === 'vi' ? 'Dừng ' : 'Pause '],
    [/^停下$/g, lang === 'vi' ? 'Dừng' : 'Pause'],
    [/并归档/g, lang === 'vi' ? ' và lưu trữ' : ' and archive'],
    [/按\s*(?=SOP|PR|MR)/g, lang === 'vi' ? 'Theo ' : 'By '],
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
    if (Object.prototype.hasOwnProperty.call(dict, trimmed) && dict[trimmed] && !HAN.test(dict[trimmed])) {
      out = text.replace(trimmed, dict[trimmed]);
    } else {
      out = text;
    }
    if (HAN.test(out)) {
      out = applyFragments(out);
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
      if (r.type === 'childList') {
        r.addedNodes.forEach(node => {
          walk(node);
          if (node.nodeType === 1) {
            if (node.tagName === 'IFRAME') bindFrame(node);
            else if (node.querySelectorAll) node.querySelectorAll('iframe').forEach(bindFrame);
          }
        });
      } else if (r.type === 'characterData') translateText(r.target);
      else if (r.type === 'attributes') translateAttrs(r.target);
    }
  });
  function applyLangButtons() {
    const label = window.lfLangLabel();
    for (const id of ['langToggle', 'loginLang']) {
      const btn = document.getElementById(id);
      if (!btn) continue;
      btn.textContent = label.text;
      btn.title = label.title;
    }
  }
  function bindFrame(iframe) {
    if (!iframe || iframe.tagName !== 'IFRAME' || iframe.dataset.lfI18nBound === '1') return;
    const run = () => {
      try {
        const doc = iframe.contentDocument;
        if (!doc || !doc.defaultView) return;
        // Chatbot and other same-origin frames that already load /i18n.js
        // keep their own observer. Still walk once in case their boot lagged.
        if (doc.body) walk(doc.body);
        if (doc.title && HAN.test(doc.title)) doc.title = translate(doc.title);
        if (!doc.defaultView.lfTranslate && doc.body) {
          observer.observe(doc.body, {subtree: true, childList: true, characterData: true, attributes: true, attributeFilter: ATTRS});
        }
        iframe.dataset.lfI18nBound = '1';
      } catch (_) { /* cross-origin preview frames stay untouched */ }
    };
    iframe.addEventListener('load', run);
    try {
      const doc = iframe.contentDocument;
      if (doc && (doc.readyState === 'complete' || doc.body)) run();
    } catch (_) { /* ignore */ }
  }
  function start() {
    applyLangButtons();
    walk(document.body);
    translateTitle();
    observer.observe(document.body, {subtree: true, childList: true, characterData: true, attributes: true, attributeFilter: ATTRS});
    document.querySelectorAll('iframe').forEach(bindFrame);
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
