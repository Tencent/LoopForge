// Floating page feedback widget: a button in the bottom-right corner that
// opens a card for describing a problem on the current page, attaching images
// and an optional picked element. Rendered only for signed-in, non-guest
// users; renderUserBadge() calls sync() whenever the login state changes.
// Hiding the button (drag into the trash) lives in module memory only and
// never touches localStorage, sessionStorage or cookies.
(function () {
  'use strict';
  const root = typeof window !== 'undefined' ? window : globalThis;
  const doc = root.document;

  const MAX_IMAGES = 5;
  const MAX_IMAGE_BYTES = 5 * 1024 * 1024;
  const IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'];
  const MAX_TITLE = 120;
  const MAX_TEXT = 5000;
  const MAX_ELEMENT_TEXT = 200;
  const HOLD_MS = 320;
  const MOVE_PX = 6;
  const HOT_RADIUS = 72;
  const SUCCESS_MS = 1600;
  const TOAST_MS = 4000;
  const ERROR_MS = 2600;
  const ENDPOINT = '/api/page-feedback';

  // Keep in sync with feedbackSensitiveParam in internal/httpapi/page_feedback.go.
  const SENSITIVE_EXACT = ['code', 'key', 'sig', 'pwd', 'auth'];
  const SENSITIVE_FRAGMENTS = ['token', 'ticket', 'session', 'secret', 'password', 'passwd', 'credential', 'signature', 'apikey'];

  // ---------- pure helpers ----------
  function deriveTitle(text) {
    const first = String(text || '').trim().split(/\r?\n/)[0] || '';
    return Array.from(first.trim()).slice(0, MAX_TITLE).join('');
  }

  function sensitiveParam(name) {
    name = String(name || '').trim().toLowerCase();
    if (!name) return false;
    if (SENSITIVE_EXACT.some(k => name === k || name.endsWith('_' + k) || name.endsWith('-' + k))) return true;
    return SENSITIVE_FRAGMENTS.some(k => name.includes(k));
  }

  function stripSensitiveQuery(query) {
    return String(query || '').split('&').filter(part => {
      if (!part) return false;
      let name = part.split('=')[0];
      try { name = decodeURIComponent(name.replace(/\+/g, ' ')); } catch (_) {}
      return !sensitiveParam(name);
    }).join('&');
  }

  function sanitizeRoute(route) {
    route = String(route || '');
    const at = route.indexOf('?');
    if (at < 0) return route;
    const query = stripSensitiveQuery(route.slice(at + 1));
    return route.slice(0, at) + (query ? '?' + query : '');
  }

  // Drops credentials and sensitive query parameters (also inside a hash route).
  function sanitizeURL(href) {
    href = String(href || '').trim();
    const m = /^(https?:\/\/)(?:[^@/?#]*@)?([^/?#]*)([^?#]*)(?:\?([^#]*))?(?:#(.*))?$/i.exec(href);
    if (!m) return '';
    const query = stripSensitiveQuery(m[4] || '');
    const hash = m[5] != null ? sanitizeRoute(m[5]) : '';
    return m[1] + m[2] + m[3] + (query ? '?' + query : '') + (hash ? '#' + hash : '');
  }

  function cssIdent(value) {
    return /^-?[A-Za-z_][\w-]*$/.test(String(value || ''));
  }

  function attrValue(value) {
    return '"' + String(value).replace(/\\/g, '\\\\').replace(/"/g, '\\"') + '"';
  }

  function isUnique(selector, scope) {
    try { return scope.querySelectorAll(selector).length === 1; } catch (_) { return false; }
  }

  function dataAttribute(el) {
    const testid = el.getAttribute('data-testid');
    if (testid && testid.length <= 80) return '[data-testid=' + attrValue(testid) + ']';
    const names = el.attributes ? Array.from(el.attributes).map(a => a.name)
      : Array.from(el.attrs ? el.attrs.keys() : []);
    for (const name of names) {
      if (!/^data-[a-z][\w-]*$/.test(name) || /^data-(hui|hover|tooltip|icon|fb)/.test(name)) continue;
      const value = el.getAttribute(name);
      if (value != null && value.length <= 80) return '[' + name + '=' + attrValue(value) + ']';
    }
    return '';
  }

  // Builds a selector that matches exactly this element: id, then
  // data-testid / data-*, then a tag:nth-of-type path, stopping at the first
  // unique prefix.
  function cssSelector(el, scope) {
    scope = scope || el.ownerDocument;
    if (!el || el.nodeType !== 1) return '';
    if (el.id && cssIdent(el.id) && isUnique('#' + el.id, scope)) return '#' + el.id;
    const parts = [];
    for (let node = el; node && node.nodeType === 1; node = node.parentElement) {
      if (node.id && cssIdent(node.id) && isUnique('#' + node.id, scope)) {
        parts.unshift('#' + node.id);
        break;
      }
      const tag = node.localName || String(node.tagName || '').toLowerCase();
      if (tag === 'html') { parts.unshift('html'); break; }
      if (tag === 'body') { parts.unshift('body'); break; }
      let part = tag + dataAttribute(node);
      const parent = node.parentElement;
      if (parent) {
        const same = parent.children.filter ? parent.children.filter(c => c.localName === node.localName)
          : Array.from(parent.children).filter(c => c.localName === node.localName);
        if (same.length > 1 && !(part !== tag && same.filter(c => c.matches(part)).length === 1)) {
          part += ':nth-of-type(' + (same.indexOf(node) + 1) + ')';
        }
      }
      parts.unshift(part);
      if (isUnique(parts.join(' > '), scope)) break;
    }
    return parts.join(' > ');
  }

  function textSummary(el) {
    const raw = el ? (el.innerText != null ? el.innerText : el.textContent) : '';
    let text = String(raw || '').replace(/\s+/g, ' ').trim();
    if (!text && el && el.getAttribute) text = String(el.getAttribute('aria-label') || el.getAttribute('title') || '').trim();
    return Array.from(text).slice(0, MAX_ELEMENT_TEXT).join('');
  }

  const INTERACTIVE = 'button, a, [role="button"], [role="link"], [role="tab"], [role="menuitem"], [role="option"], label, summary';

  // Icon-only controls are hit on an inner <path>; lift such hits to the
  // nearest interactive ancestor of the outermost <svg>, else to its parent.
  function pickTarget(node) {
    if (!node || node.nodeType !== 1 || !node.closest) return node;
    let svg = node.closest('svg');
    if (!svg) return node;
    for (let up = svg.parentElement && svg.parentElement.closest('svg'); up; up = up.parentElement && up.parentElement.closest('svg')) svg = up;
    const host = svg.parentElement;
    if (!host) return svg;
    return host.closest(INTERACTIVE) || host;
  }

  function rectOf(r) {
    return { x: Math.round(r.left), y: Math.round(r.top), width: Math.round(r.width), height: Math.round(r.height) };
  }

  function mainScroller() {
    const candidates = [doc.querySelector('.page-view.active'), doc.querySelector('main'), doc.scrollingElement];
    for (const el of candidates) if (el && el.scrollTop > 0) return el;
    return null;
  }

  function workbenchFrame() {
    const frame = doc.getElementById('chatNextFrame');
    if (!frame || !frame.closest || !frame.closest('.page-view.active')) return null;
    return frame;
  }

  function frameLocation() {
    const frame = workbenchFrame();
    if (!frame) return '';
    try {
      const href = frame.contentWindow && frame.contentWindow.location && frame.contentWindow.location.href;
      return href && href !== 'about:blank' ? sanitizeURL(href) : '';
    } catch (_) {
      return '';
    }
  }

  function collectPageContext() {
    const loc = root.location || {};
    const scroller = mainScroller();
    const titleEl = doc.getElementById('pageTitle');
    const view = String((titleEl && titleEl.textContent) || doc.title || '').trim().slice(0, 128);
    const nav = root.navigator || {};
    return {
      url: sanitizeURL(loc.href || ''),
      route: sanitizeRoute(loc.hash || ''),
      view,
      frameUrl: frameLocation(),
      context: {
        scroll: {
          x: Math.round(scroller ? scroller.scrollLeft || 0 : root.scrollX || 0),
          y: Math.round(scroller ? scroller.scrollTop || 0 : root.scrollY || 0),
        },
        viewport: { width: root.innerWidth || 0, height: root.innerHeight || 0 },
        dpr: root.devicePixelRatio || 1,
        userAgent: nav.userAgent || '',
        language: nav.language || '',
        theme: doc.documentElement.getAttribute('data-theme') === 'dark' ? 'dark' : 'light',
        capturedAt: new Date().toISOString(),
      },
    };
  }

  // The payload never names the submitter: the server takes it from the session.
  function buildPayload(text, page, element) {
    const description = Array.from(String(text || '').trim()).slice(0, MAX_TEXT).join('');
    return {
      title: deriveTitle(description),
      description,
      url: page.url,
      route: page.route,
      view: page.view,
      frameUrl: page.frameUrl,
      context: page.context,
      element: element || null,
    };
  }

  function buildFormData(payload, files) {
    const form = new root.FormData();
    form.append('payload', JSON.stringify(payload));
    (files || []).forEach(file => form.append('images', file, file.name || 'image'));
    return form;
  }

  // Returns an error message, or '' when the file can be attached.
  function imageProblem(file, count) {
    if (count >= MAX_IMAGES) return '最多 5 张图片';
    if (!file || !IMAGE_TYPES.includes(String(file.type || '').toLowerCase())) return '仅支持 PNG / JPEG / GIF / WebP 图片';
    if (file.size > MAX_IMAGE_BYTES) return (file.name || '图片') + ' 超过 5MB';
    return '';
  }

  function escapeText(value) {
    return String(value == null ? '' : value).replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
  }

  function currentUser() {
    try {
      // eslint-disable-next-line no-undef
      if (typeof state !== 'undefined' && state) return state.currentUser || null;
    } catch (_) {}
    return root.state ? root.state.currentUser || null : null;
  }

  function eligible(user) {
    return !!(user && !user.isGuest);
  }

  // ---------- template ----------
  const X_ICON = '<svg class="fb-i fb-x" viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18"/></svg>';
  const TEMPLATE = `
<div class="fb-pickbar" role="status"><span>点击页面上要反馈的元素</span><kbd>Esc</kbd><button type="button" class="fb-pick-cancel">取消</button></div>
<div class="fb-hl" aria-hidden="true"><span></span></div>
<div class="fb-scrim" aria-hidden="true"></div>
<div class="fb-trash" aria-hidden="true"><span class="fb-trash-label">拖到这里隐藏</span><span class="fb-bin"><svg class="fb-i" viewBox="0 0 24 24"><g class="lid"><path d="M4 7h16"/><path d="M9.5 7V4.5h5V7"/></g><path d="M6 7l1 13h10l1-13"/><path d="M10 11v5.5M14 11v5.5"/></svg></span></div>
<div class="fb-toast" role="status"><span>反馈入口已隐藏，刷新页面后恢复</span><button type="button" class="fb-undo">撤销</button></div>
<section class="fb-panel" id="pageFeedbackPanel" role="dialog" aria-labelledby="pageFeedbackTitle" aria-hidden="true">
  <input type="file" class="fb-file" accept="image/png,image/jpeg,image/gif,image/webp" multiple hidden>
  <header class="fb-head">
    <span class="fb-mark" aria-hidden="true"><svg class="fb-i" viewBox="0 0 24 24"><path d="M20 12.5a7.5 7.5 0 0 1-10.6 6.8L4.5 20.5l1.2-4.6A7.5 7.5 0 0 1 12 5"/><g class="spark"><path d="M18 3v6M15 6h6"/></g></svg></span>
    <div class="fb-head-text"><h2 class="fb-title" id="pageFeedbackTitle">帮助我们做得更好</h2><small class="fb-subtitle"><i></i>AI Native团队将在24小时以内快速响应</small></div>
    <button type="button" class="fb-icon-btn fb-close" aria-label="关闭">${X_ICON}</button>
  </header>
  <div class="fb-body">
    <div class="fb-composer">
      <div class="fb-field"><textarea class="fb-text" rows="4" maxlength="5000" placeholder="您的不知所措也是我们的问题" aria-label="问题描述"></textarea></div>
      <div class="fb-atts"></div>
      <div class="fb-toolbar">
        <button type="button" class="fb-tool fb-add-image" title="添加图片（可直接粘贴或拖入，最多 5 张，每张 ≤5MB）"><svg class="fb-i" viewBox="0 0 24 24" aria-hidden="true"><rect x="3" y="3" width="18" height="18" rx="2"/><circle class="sun" cx="8.5" cy="8.5" r="1.5"/><path d="m21 15-5-5L5 21"/></svg>图片<span class="fb-count"></span></button>
        <button type="button" class="fb-tool fb-pick"><svg class="fb-i" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="7"/><g class="arms"><path d="M12 2v4M12 18v4M2 12h4M18 12h4"/></g></svg>点选元素</button>
        <button type="button" class="fb-submit" disabled><span class="fb-submit-label">提交</span><svg class="fb-i fb-arrow" viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12h14M13 6l6 6-6 6"/></svg><svg class="fb-i fb-spin" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3A9 9 0 1 1 3 12"/></svg></button>
      </div>
      <div class="fb-drop-hint">松开以添加图片</div>
    </div>
    <div class="fb-el"><div><div class="fb-el-chip"><svg class="fb-i" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 4l7 16 2.5-6.5L20 11z"/></svg><div class="fb-el-tx"><b class="fb-el-text"></b><code class="fb-el-sel"></code></div><button type="button" class="fb-icon-btn fb-repick" title="重新点选" aria-label="重新点选"><svg class="fb-i" viewBox="0 0 24 24" aria-hidden="true"><path d="M20 11a8 8 0 1 0-2.3 5.7M20 5v6h-6"/></svg></button><button type="button" class="fb-icon-btn fb-clear-el" title="清除标注" aria-label="清除标注">${X_ICON}</button></div></div></div>
    <div class="fb-err" role="alert"></div>
  </div>
  <footer class="fb-foot"><button type="button" class="fb-ctx-row" aria-expanded="false"><svg class="fb-i fb-pin" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 21s-6-5.3-6-11a6 6 0 0 1 12 0c0 5.7-6 11-6 11z"/><circle cx="12" cy="10" r="2"/></svg><span class="fb-ctx-sum"></span><svg class="fb-i fb-chev" viewBox="0 0 24 24" aria-hidden="true"><path d="m6 9 6 6 6-6"/></svg></button><div class="fb-ctx-more"><div><dl class="fb-ctx-list"></dl></div></div></footer>
  <div class="fb-done" aria-live="polite"><div class="fb-ring"><svg class="fb-i" viewBox="0 0 24 24" aria-hidden="true"><path pathLength="1" d="M7.6 12.4l3.6 3.6L18.2 7.4"/></svg></div><b>已收到，谢谢你</b><small>AI Native团队将在24小时以内快速响应</small></div>
</section>
<button type="button" class="fb-fab" aria-label="帮助我们做得更好" aria-expanded="false" aria-controls="pageFeedbackPanel">
  <span class="fb-g fb-g-chat" aria-hidden="true"><svg class="fb-i" viewBox="0 0 24 24" style="width:20px;height:20px"><g class="fb-bubble"><path d="M21 12a8 8 0 0 1-11.3 7.3L4 20l.7-5.7A8 8 0 1 1 21 12z"/><path d="M12 8.5v3.5M12 15v.01"/></g></svg></span>
  <span class="fb-g fb-g-close" aria-hidden="true"><svg class="fb-i" viewBox="0 0 24 24"><path d="M6 6l12 12M18 6L6 18"/></svg></span>
</button>`;

  // ---------- widget state (memory only) ----------
  let ui = null;
  let images = [];
  let element = null;
  let page = null;
  let picking = null;
  let press = null;
  let swallowClick = false;
  let trashed = false;
  let busy = false;
  let sent = false;
  const timers = {};

  function later(name, fn, ms) {
    root.clearTimeout(timers[name]);
    timers[name] = root.setTimeout(() => { timers[name] = 0; fn(); }, ms);
  }
  function cancel(name) {
    root.clearTimeout(timers[name]);
    timers[name] = 0;
  }

  function q(selector) { return ui.root.querySelector(selector); }

  function mount() {
    const host = doc.createElement('div');
    host.className = 'fb-root';
    host.innerHTML = TEMPLATE;
    doc.body.appendChild(host);
    ui = {
      root: host, fab: host.querySelector('.fb-fab'), panel: host.querySelector('.fb-panel'),
      text: host.querySelector('.fb-text'), submit: host.querySelector('.fb-submit'),
      submitLabel: host.querySelector('.fb-submit-label'), atts: host.querySelector('.fb-atts'),
      file: host.querySelector('.fb-file'), addImage: host.querySelector('.fb-add-image'),
      count: host.querySelector('.fb-count'), composer: host.querySelector('.fb-composer'),
      el: host.querySelector('.fb-el'), err: host.querySelector('.fb-err'), foot: host.querySelector('.fb-foot'),
      ctxRow: host.querySelector('.fb-ctx-row'), ctxSum: host.querySelector('.fb-ctx-sum'), ctxList: host.querySelector('.fb-ctx-list'),
      done: host.querySelector('.fb-done'), trash: host.querySelector('.fb-trash'), scrim: host.querySelector('.fb-scrim'),
      toast: host.querySelector('.fb-toast'), pickbar: host.querySelector('.fb-pickbar'), hl: host.querySelector('.fb-hl'),
    };
    ui.panel.inert = true;
    bindUI();
    doc.addEventListener('keydown', onDocKeydown, true);
    root.addEventListener('hashchange', onHashChange);
    if (trashed) ui.fab.classList.add('gone');
    refreshControls();
    placeFab();
  }

  function unmount() {
    if (!ui) return;
    stopPicking(false);
    if (press) { root.clearTimeout(press.timer); press = null; }
    Object.keys(timers).forEach(cancel);
    images.forEach(item => revoke(item.url));
    images = [];
    element = null;
    page = null;
    busy = false;
    sent = false;
    swallowClick = false;
    doc.removeEventListener('keydown', onDocKeydown, true);
    root.removeEventListener('hashchange', onHashChange);
    ui.root.remove();
    ui = null;
  }

  function sync() {
    if (!doc || !doc.body) return;
    const ok = eligible(currentUser());
    if (ok && !ui) mount();
    else if (!ok && ui) unmount();
    else if (ui) placeFab();
  }

  // Lift the button above the workbench "return" floating button when shown.
  function placeFab() {
    if (!ui) return;
    const other = doc.querySelector('.chat-next-return');
    const visible = !!(other && !other.hidden && other.getClientRects && other.getClientRects().length);
    ui.root.classList.toggle('fb-lift', visible);
  }

  function isOpen() { return !!ui && ui.panel.classList.contains('open'); }

  function setOpen(open, opts = {}) {
    if (!ui) return;
    ui.panel.classList.toggle('open', open);
    ui.panel.inert = !open;
    ui.panel.setAttribute('aria-hidden', open ? 'false' : 'true');
    ui.fab.setAttribute('aria-expanded', open ? 'true' : 'false');
    if (open) {
      placeFab();
      refreshContext();
      later('focus', () => { if (ui && isOpen()) ui.text.focus({ preventScroll: true }); }, 120);
    } else {
      cancel('focus');
      if (!opts.keepFocus && !trashed && !picking) ui.fab.focus({ preventScroll: true });
    }
  }

  function refreshControls() {
    if (!ui) return;
    ui.submit.disabled = busy || sent || !ui.text.value.trim();
    ui.count.textContent = images.length ? images.length + '/' + MAX_IMAGES : '';
    ui.addImage.hidden = images.length >= MAX_IMAGES;
    ui.atts.classList.toggle('has', images.length > 0);
  }

  function autosize() {
    ui.text.style.height = 'auto';
    ui.text.style.height = Math.min(ui.text.scrollHeight, 180) + 'px';
  }

  function showError(message, sticky) {
    if (!ui) return;
    ui.err.textContent = message || '';
    ui.err.classList.toggle('show', !!message);
    cancel('error');
    if (message && !sticky) later('error', () => ui && ui.err.classList.remove('show'), ERROR_MS);
  }

  // ---------- page context ----------
  function refreshContext() {
    if (!ui) return;
    page = collectPageContext();
    const size = page.context.viewport.width + '×' + page.context.viewport.height;
    ui.ctxSum.textContent = '已附上页面位置 · ' + [page.route || '#/', page.view, size].filter(Boolean).join(' · ');
    const rows = [
      ['地址', page.url],
      ['路由', page.route || '#/'],
      ['视图', page.view],
      ['工作台', page.frameUrl],
      ['滚动', page.context.scroll.x + ', ' + page.context.scroll.y],
      ['视口', size + ' @' + page.context.dpr + 'x'],
      ['主题', page.context.theme === 'dark' ? '深色' : '浅色'],
      ['语言', page.context.language],
      ['浏览器', page.context.userAgent],
      ['时间', page.context.capturedAt],
    ].filter(row => row[1]);
    ui.ctxList.innerHTML = rows.map(row => '<dt>' + escapeText(row[0]) + '</dt><dd>' + escapeText(row[1]) + '</dd>').join('');
  }

  function onHashChange() {
    placeFab();
    if (isOpen()) refreshContext();
  }

  // ---------- images ----------
  function revoke(url) {
    try { if (url && root.URL && root.URL.revokeObjectURL) root.URL.revokeObjectURL(url); } catch (_) {}
  }

  function addImages(list) {
    if (!ui) return;
    let problem = '';
    for (const file of Array.from(list || [])) {
      const issue = imageProblem(file, images.length);
      if (issue) {
        problem = issue;
        if (images.length >= MAX_IMAGES) break;
        continue;
      }
      const url = root.URL && root.URL.createObjectURL ? root.URL.createObjectURL(file) : '';
      const item = { file, url };
      const thumb = doc.createElement('div');
      thumb.className = 'fb-thumb';
      if (url) thumb.style.backgroundImage = 'url("' + url + '")';
      const remove = doc.createElement('button');
      remove.type = 'button';
      remove.setAttribute('aria-label', '移除图片');
      remove.innerHTML = '<svg class="fb-i" viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18"/></svg>';
      remove.addEventListener('click', () => removeImage(item));
      thumb.appendChild(remove);
      item.node = thumb;
      ui.atts.appendChild(thumb);
      images.push(item);
    }
    if (problem) showError(problem);
    refreshControls();
  }

  function removeImage(item) {
    if (!images.includes(item)) return;
    images = images.filter(i => i !== item);
    item.node.classList.add('leave');
    refreshControls();
    root.setTimeout(() => { item.node.remove(); revoke(item.url); }, 180);
  }

  function clearImages() {
    images.forEach(item => { item.node.remove(); revoke(item.url); });
    images = [];
  }

  function pastedFiles(event) {
    const items = event.clipboardData && event.clipboardData.items;
    return Array.from(items || []).filter(i => i.kind === 'file').map(i => i.getAsFile()).filter(Boolean);
  }

  function hasFiles(event) {
    const types = event.dataTransfer && event.dataTransfer.types;
    return !types || Array.from(types).includes('Files');
  }

  // ---------- element annotation ----------
  function renderElement() {
    if (!ui) return;
    ui.el.classList.toggle('has', !!element);
    ui.root.querySelector('.fb-el-text').textContent = element ? (element.text ? '“' + element.text + '”' : '<' + element.tag + '>') + (element.inFrame ? ' · 工作台内' : '') : '';
    ui.root.querySelector('.fb-el-sel').textContent = element ? element.selector : '';
  }

  function frameOffset(frame) {
    if (!frame) return { x: 0, y: 0 };
    const r = frame.getBoundingClientRect();
    return { x: r.left + (frame.clientLeft || 0), y: r.top + (frame.clientTop || 0) };
  }

  function pickTargets() {
    const out = [{ doc, frame: null }];
    Array.from(doc.querySelectorAll('iframe')).forEach(frame => {
      if (!frame.getClientRects || !frame.getClientRects().length) return;
      try {
        const inner = frame.contentDocument;
        if (inner && inner.documentElement) out.push({ doc: inner, frame });
      } catch (_) {}
    });
    return out;
  }

  const BLOCKED = ['click', 'dblclick', 'auxclick', 'mousedown', 'mouseup', 'pointerdown', 'pointerup', 'contextmenu', 'submit', 'pointerover', 'mouseover'];

  function startPicking() {
    if (!ui || picking) return;
    showError('');
    setOpen(false, { keepFocus: true });
    picking = { hover: null, targets: [] };
    ui.fab.classList.add('is-hidden');
    ui.pickbar.classList.add('show');
    doc.body.classList.add('fb-picking');
    for (const target of pickTargets()) {
      const handlers = {
        move: event => onPickMove(event, target.frame),
        block: event => onPickBlock(event, target.frame),
        key: event => { if (event.key === 'Escape') { event.preventDefault(); stopPicking(true); } },
      };
      target.doc.addEventListener('mousemove', handlers.move, true);
      BLOCKED.forEach(type => target.doc.addEventListener(type, handlers.block, true));
      if (target.frame) target.doc.addEventListener('keydown', handlers.key, true);
      picking.targets.push({ ...target, handlers });
    }
  }

  function stopPicking(reopen) {
    if (!picking) return;
    for (const target of picking.targets) {
      target.doc.removeEventListener('mousemove', target.handlers.move, true);
      BLOCKED.forEach(type => target.doc.removeEventListener(type, target.handlers.block, true));
      if (target.frame) target.doc.removeEventListener('keydown', target.handlers.key, true);
    }
    picking = null;
    if (!ui) return;
    doc.body.classList.remove('fb-picking');
    ui.hl.classList.remove('show');
    ui.pickbar.classList.remove('show');
    ui.fab.classList.remove('is-hidden');
    if (reopen) setOpen(true);
  }

  function ownWidget(node) {
    return !!(ui && node && node.nodeType === 1 && ui.root.contains(node));
  }

  function onPickMove(event, frame) {
    if (!picking || !ui) return;
    const node = pickTarget(event.target);
    if (!node || node.nodeType !== 1 || ownWidget(node) || (!frame && node.localName === 'iframe')) {
      picking.hover = null;
      ui.hl.classList.remove('show');
      return;
    }
    picking.hover = { node, frame };
    const r = node.getBoundingClientRect();
    const off = frameOffset(frame);
    Object.assign(ui.hl.style, {
      top: r.top + off.y - 3 + 'px', left: r.left + off.x - 3 + 'px',
      width: r.width + 6 + 'px', height: r.height + 6 + 'px',
    });
    ui.hl.querySelector('span').textContent = node.localName + '  ' + Math.round(r.width) + '×' + Math.round(r.height);
    ui.hl.classList.add('show');
  }

  function onPickBlock(event, frame) {
    if (!picking || ownWidget(event.target)) return;
    event.preventDefault();
    event.stopPropagation();
    if (event.stopImmediatePropagation) event.stopImmediatePropagation();
    if (event.type !== 'click') return;
    let node = pickTarget(event.target);
    if (picking.hover && picking.hover.frame === frame) node = picking.hover.node;
    if (!node || node.nodeType !== 1 || (!frame && node.localName === 'iframe')) return;
    element = describeElement(node, frame, event);
    renderElement();
    stopPicking(true);
  }

  function describeElement(node, frame, event) {
    const off = frameOffset(frame);
    const r = node.getBoundingClientRect();
    const view = node.ownerDocument.defaultView || root;
    const rect = rectOf({ left: r.left + off.x, top: r.top + off.y, width: r.width, height: r.height });
    return {
      selector: cssSelector(node, node.ownerDocument),
      tag: node.localName,
      text: textSummary(node),
      rect,
      pageRect: rectOf({ left: r.left + (view.scrollX || 0), top: r.top + (view.scrollY || 0), width: r.width, height: r.height }),
      point: { x: Math.round((event.clientX || 0) + off.x), y: Math.round((event.clientY || 0) + off.y) },
      inFrame: !!frame,
    };
  }

  // ---------- submit ----------
  async function submit() {
    if (!ui || busy || sent || !ui.text.value.trim()) return;
    if (!page) refreshContext();
    busy = true;
    ui.submit.classList.add('busy');
    ui.submitLabel.textContent = '提交中';
    showError('');
    refreshControls();
    let message = '';
    let ok = false;
    try {
      const payload = buildPayload(ui.text.value, page, element);
      const res = await root.fetch(ENDPOINT, {
        method: 'POST', credentials: 'same-origin',
        headers: { Accept: 'application/json' },
        body: buildFormData(payload, images.map(i => i.file)),
      });
      if (res.status === 401) message = '登录已失效，请重新登录后提交';
      else if (!res.ok) {
        let body = null;
        try { body = await res.json(); } catch (_) {}
        message = (body && body.error) || '提交失败（HTTP ' + res.status + '），请稍后重试';
      } else ok = true;
    } catch (_) {
      message = '网络异常，提交失败，请稍后重试';
    }
    if (!ui) return;
    busy = false;
    ui.submit.classList.remove('busy');
    ui.submitLabel.textContent = '提交';
    if (!ok) {
      showError(message, true);
      refreshControls();
      return;
    }
    sent = true;
    ui.done.classList.add('show');
    refreshControls();
    later('success', () => {
      setOpen(false, { keepFocus: !ui.root.contains(doc.activeElement) });
      later('reset', resetDraft, 300);
    }, SUCCESS_MS);
  }

  function resetDraft() {
    if (!ui) return;
    sent = false;
    ui.done.classList.remove('show');
    ui.text.value = '';
    ui.text.style.height = '';
    clearImages();
    element = null;
    renderElement();
    showError('');
    refreshControls();
  }

  // ---------- drag into the trash ----------
  function binCenter() {
    return { x: (root.innerWidth || 0) / 2, y: (root.innerHeight || 0) - 28 - 30 };
  }

  function onPointerDown(event) {
    if (event.button !== 0 || picking || trashed || busy) return;
    const r = ui.fab.getBoundingClientRect();
    press = {
      id: event.pointerId, sx: event.clientX, sy: event.clientY, x: event.clientX, y: event.clientY,
      ox: r.left + r.width / 2, oy: r.top + r.height / 2, on: false, hot: false, cancelled: false,
    };
    press.timer = root.setTimeout(lift, HOLD_MS);
    try { if (ui.fab.setPointerCapture && event.pointerId != null) ui.fab.setPointerCapture(event.pointerId); } catch (_) {}
  }

  function onPointerMove(event) {
    if (!press || press.cancelled) return;
    press.x = event.clientX;
    press.y = event.clientY;
    if (!press.on && Math.hypot(press.x - press.sx, press.y - press.sy) > MOVE_PX) lift();
    if (press && press.on) follow();
  }

  function lift() {
    if (!press || press.on || !ui) return;
    root.clearTimeout(press.timer);
    press.on = true;
    swallowClick = true;
    cancel('swallow');
    if (isOpen()) setOpen(false, { keepFocus: true });
    hideToast();
    ui.fab.classList.remove('returning', 'back');
    ui.fab.classList.add('dragging');
    ui.trash.classList.add('show');
    ui.scrim.classList.add('show');
    follow();
  }

  function follow() {
    const b = binCenter();
    const dx = press.x - press.sx, dy = press.y - press.sy;
    press.hot = Math.hypot(press.ox + dx - b.x, press.oy + dy - b.y) < HOT_RADIUS;
    ui.trash.classList.toggle('hot', press.hot);
    const tx = press.hot ? b.x - press.ox : dx, ty = press.hot ? b.y - press.oy : dy;
    ui.fab.style.transform = 'translate(' + tx + 'px,' + ty + 'px) scale(' + (press.hot ? 0.72 : 1.08) + ')';
  }

  function onPointerEnd(event) {
    if (!press) return;
    const p = press;
    press = null;
    root.clearTimeout(p.timer);
    if (!p.on) return;
    if (!p.cancelled) finishDrag(p, event.type === 'pointerup');
    // The click that follows this release belongs to the drag, even when Esc already ended it.
    later('swallow', () => { swallowClick = false; }, 400);
  }

  function cancelDrag() {
    if (!press || !press.on || press.cancelled) return false;
    press.cancelled = true;
    finishDrag(press, false);
    return true;
  }

  function finishDrag(p, drop) {
    ui.fab.classList.remove('dragging');
    if (drop && p.hot) {
      const b = binCenter();
      trashed = true;
      ui.fab.classList.add('trashed');
      ui.fab.style.transform = 'translate(' + (b.x - p.ox) + 'px,' + (b.y - p.oy) + 'px) scale(.2)';
      ui.trash.classList.add('eat');
      later('trash', () => {
        if (!ui) return;
        ui.fab.classList.add('gone');
        ui.fab.classList.remove('trashed');
        ui.fab.style.transform = '';
        ui.trash.classList.remove('show', 'hot', 'eat');
        ui.scrim.classList.remove('show');
        showToast();
      }, 360);
      return;
    }
    ui.trash.classList.remove('show', 'hot');
    ui.scrim.classList.remove('show');
    ui.fab.classList.add('returning');
    ui.fab.style.transform = '';
    later('return', () => ui && ui.fab.classList.remove('returning'), 560);
  }

  function showToast() {
    ui.toast.classList.add('show');
    later('toast', hideToast, TOAST_MS);
  }

  function hideToast() {
    cancel('toast');
    if (ui) ui.toast.classList.remove('show');
  }

  function undoHide() {
    hideToast();
    trashed = false;
    ui.fab.classList.remove('gone');
    ui.fab.classList.add('back');
    later('back', () => ui && ui.fab.classList.remove('back'), 520);
    ui.fab.focus({ preventScroll: true });
  }

  // ---------- events ----------
  function onFabClick(event) {
    if (swallowClick) {
      swallowClick = false;
      cancel('swallow');
      event.preventDefault();
      return;
    }
    setOpen(!isOpen());
  }

  function onDocKeydown(event) {
    if (!ui) return;
    if (event.key === 'Escape') {
      if (cancelDrag()) { event.preventDefault(); return; }
      if (picking) { event.preventDefault(); event.stopPropagation(); stopPicking(true); return; }
      if (isOpen() && (ui.root.contains(event.target) || event.target === doc.body)) {
        event.preventDefault();
        setOpen(false);
      }
    }
  }

  function bindUI() {
    const { fab, panel, text, file, composer } = ui;
    fab.addEventListener('click', onFabClick);
    fab.addEventListener('pointerdown', onPointerDown);
    fab.addEventListener('pointermove', onPointerMove);
    fab.addEventListener('pointerup', onPointerEnd);
    fab.addEventListener('pointercancel', onPointerEnd);
    fab.addEventListener('contextmenu', event => event.preventDefault());
    fab.addEventListener('dragstart', event => event.preventDefault());
    q('.fb-close').addEventListener('click', () => setOpen(false));
    text.addEventListener('input', () => { autosize(); refreshControls(); });
    text.addEventListener('keydown', event => {
      if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
        event.preventDefault();
        submit();
      }
    });
    ui.submit.addEventListener('click', submit);
    ui.addImage.addEventListener('click', () => file.click());
    file.addEventListener('change', () => { addImages(file.files); file.value = ''; });
    panel.addEventListener('paste', event => {
      const files = pastedFiles(event);
      if (!files.length) return;
      event.preventDefault();
      addImages(files);
    });
    let depth = 0;
    composer.addEventListener('dragenter', event => {
      if (!hasFiles(event)) return;
      event.preventDefault();
      depth++;
      composer.classList.add('drag');
    });
    composer.addEventListener('dragover', event => { if (hasFiles(event)) event.preventDefault(); });
    composer.addEventListener('dragleave', () => {
      depth = Math.max(0, depth - 1);
      if (!depth) composer.classList.remove('drag');
    });
    composer.addEventListener('drop', event => {
      event.preventDefault();
      depth = 0;
      composer.classList.remove('drag');
      addImages(event.dataTransfer && event.dataTransfer.files);
    });
    q('.fb-pick').addEventListener('click', startPicking);
    q('.fb-repick').addEventListener('click', startPicking);
    q('.fb-pick-cancel').addEventListener('click', () => stopPicking(true));
    q('.fb-clear-el').addEventListener('click', () => { element = null; renderElement(); });
    ui.ctxRow.addEventListener('click', () => {
      const open = !ui.foot.classList.contains('open');
      ui.foot.classList.toggle('open', open);
      ui.ctxRow.setAttribute('aria-expanded', open ? 'true' : 'false');
    });
    q('.fb-undo').addEventListener('click', undoHide);
  }

  root.AgentFlowPageFeedback = {
    sync,
    // Exposed for web/page_feedback_test.js.
    _test: {
      deriveTitle, sanitizeURL, sanitizeRoute, sensitiveParam, cssSelector, collectPageContext,
      buildPayload, buildFormData, imageProblem,
      state: () => ({ mounted: !!ui, open: isOpen(), trashed, picking: !!picking, images: images.length, element, swallowClick }),
    },
  };

  if (doc && doc.readyState === 'loading') doc.addEventListener('DOMContentLoaded', sync);
  else sync();
})();
