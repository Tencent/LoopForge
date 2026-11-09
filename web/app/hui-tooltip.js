/* Harness tooltip that takes over native `title` hints. Document-level
 * delegation, so templates keep writing title="…": after ~400ms of hover (or
 * on keyboard focus) the title moves to data-hui-title while one shared
 * role="tooltip" bubble is shown, and it is restored on leave, blur, scroll,
 * Escape or removal. Touch pointers and [data-native] subtrees are skipped.
 * Shared by web/ (classic script) and chatbot/ (imported for side effects).
 */
(function () {
  'use strict';
  if (typeof window === 'undefined' || typeof document === 'undefined' || window.HuiTooltip) return;
  const doc = document;
  if (typeof doc.addEventListener !== 'function' || typeof window.addEventListener !== 'function') return;
  const SHOW_DELAY = 400;
  const TIP_ID = 'huiTooltip';
  const SKIP = '[data-native], [data-hui-tooltip="off"], .monaco-editor';
  let tip = null;
  let current = null;
  let pending = null;
  let timer = 0;
  let attrWatch = null;
  let treeWatch = null;

  function bubble() {
    if (!tip) {
      tip = doc.createElement('div');
      tip.className = 'hui-tooltip';
      tip.id = TIP_ID;
      tip.setAttribute('role', 'tooltip');
      tip.hidden = true;
    }
    if (tip.parentNode !== doc.body && doc.body) doc.body.appendChild(tip);
    return tip;
  }
  function titleOf(node) {
    if (node === current) return node.getAttribute('data-hui-title') || '';
    return node.getAttribute('title') || '';
  }
  // Nearest ancestor with a non-blank title (or the element already shown).
  function findTarget(node) {
    for (let at = node; at && at.nodeType === 1; at = at.parentElement) {
      const hit = at.closest?.('[title], [data-hui-title]');
      if (!hit) return null;
      if (hit.closest(SKIP)) return null;
      if (titleOf(hit).trim()) return hit;
      at = hit;
    }
    return null;
  }
  function cancel() {
    if (timer) clearTimeout(timer);
    timer = 0;
    pending = null;
  }
  function describe(el, on) {
    const ids = String(el.getAttribute('aria-describedby') || '').split(/\s+/).filter(id => id && id !== TIP_ID);
    if (on) ids.push(TIP_ID);
    if (ids.length) el.setAttribute('aria-describedby', ids.join(' '));
    else el.removeAttribute('aria-describedby');
  }
  function position(el) {
    const node = bubble();
    const rect = el.getBoundingClientRect();
    const vw = window.innerWidth || doc.documentElement.clientWidth || 0;
    const vh = window.innerHeight || doc.documentElement.clientHeight || 0;
    const width = node.offsetWidth;
    const height = node.offsetHeight;
    let top = rect.top - height - 8;
    let placement = 'top';
    if (top < 8) {
      top = rect.bottom + 8;
      placement = 'bottom';
      if (top + height > vh - 8) top = Math.max(8, vh - height - 8);
    }
    const left = Math.min(Math.max(8, rect.left + rect.width / 2 - width / 2), Math.max(8, vw - width - 8));
    node.style.transform = '';
    node.style.top = Math.round(top) + 'px';
    node.style.left = Math.round(left) + 'px';
    node.dataset.placement = placement;
  }
  function show(el) {
    cancel();
    if (!el.isConnected) return;
    const raw = el.getAttribute('title');
    if (raw == null || !raw.trim()) return;
    if (current && current !== el) hide();
    current = el;
    el.setAttribute('data-hui-title', raw);
    el.removeAttribute('title');
    describe(el, true);
    const node = bubble();
    node.textContent = raw.trim();
    node.hidden = false;
    node.classList.remove('is-visible');
    position(el);
    void node.offsetWidth;
    node.classList.add('is-visible');
    // Code may rewrite title while the bubble is up; keep the new text.
    if (typeof MutationObserver === 'function') {
      attrWatch = new MutationObserver(() => {
        if (current !== el || !el.hasAttribute('title')) return;
        const next = el.getAttribute('title');
        el.setAttribute('data-hui-title', next);
        el.removeAttribute('title');
        if (!next.trim()) { hide(); return; }
        node.textContent = next.trim();
        position(el);
      });
      attrWatch.observe(el, { attributes: true, attributeFilter: ['title'] });
      treeWatch = new MutationObserver(() => { if (current && !current.isConnected) hide(); });
      treeWatch.observe(doc.body, { childList: true, subtree: true });
    }
  }
  function hide() {
    cancel();
    attrWatch?.disconnect();
    treeWatch?.disconnect();
    attrWatch = null;
    treeWatch = null;
    const el = current;
    current = null;
    if (el) {
      const stored = el.getAttribute('data-hui-title');
      if (!el.hasAttribute('title') && stored != null) el.setAttribute('title', stored);
      el.removeAttribute('data-hui-title');
      describe(el, false);
    }
    if (tip) {
      tip.classList.remove('is-visible');
      tip.hidden = true;
    }
  }
  function arm(el, delay) {
    if (el === current || el === pending) return;
    hide();
    pending = el;
    timer = setTimeout(() => show(el), delay);
  }
  const noHover = () => typeof matchMedia === 'function' && matchMedia('(hover: none)').matches;

  doc.addEventListener('pointerover', event => {
    if (event.pointerType === 'touch' || event.pointerType === 'pen' && noHover()) return;
    const target = findTarget(event.target);
    if (target) arm(target, SHOW_DELAY);
    else if (current || pending) hide();
  }, true);
  doc.addEventListener('pointerout', event => {
    const to = event.relatedTarget;
    if (pending && !(to && pending.contains(to))) cancel();
    if (current && !(to && current.contains(to))) hide();
  }, true);
  doc.addEventListener('focusin', event => {
    const target = event.target;
    let keyboard = true;
    try { keyboard = target.matches(':focus-visible'); } catch {}
    if (!keyboard || findTarget(target) !== target) return;
    arm(target, 0);
  }, true);
  doc.addEventListener('focusout', event => {
    if (event.target === current || event.target === pending) hide();
  }, true);
  doc.addEventListener('pointerdown', () => { if (current || pending) hide(); }, true);
  doc.addEventListener('keydown', event => { if (event.key === 'Escape' && (current || pending)) hide(); }, true);
  window.addEventListener('scroll', () => { if (current || pending) hide(); }, { capture: true, passive: true });
  window.addEventListener('resize', () => { if (current) hide(); }, { passive: true });

  window.HuiTooltip = Object.freeze({ hide, show, findTarget, current: () => current });
})();
