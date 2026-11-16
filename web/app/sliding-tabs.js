/* Adapted from Harness UI SlidingTabs.vue (MIT).
 * Copyright (c) 2026 Harness UI contributors. See harness-ui-LICENSE.txt.
 */
(function () {
  'use strict';
  if (typeof window === 'undefined' || typeof MutationObserver !== 'function' || window.AgentFlowSlidingTabs) return;

  const mounted = new WeakMap();
  const instances = new Set();
  const geometryCache = new Map();
  const defaults = '[aria-selected="true"]';
  const registrations = [
    ['.issue-view-toggle', '.active'],
    ['.run-filters', '.active'],
    ['.scope-switch[data-scope-for]', '.on'],
    ['.canvas-view-toggle', '.active'],
    ['.prop-tabs', '.active'],
    ['.run-tabs', '.active'],
    ['.trace-source-tabs', '.active'],
    ['.metrics-period', '.active'],
    ['.business-periods', '.active'],
    ['.obs-tabs', '.active'],
    ['.mcp-scope-switch', '.active'],
    ['.asset-pane-tabs', '.active'],
    ['.mcp-view-tabs', '.active'],
    ['.asset-type-tabs', '.active'],
    ['.obs-grain-switch', '.active'],
    ['.obs-metric-switch', '.active'],
    ['.obs-seg', '.active'],
    ['.access-tabs', '.active'],
    ['.access-clients', '.active'],
    ['.drawer-tabs', '.active'],
    ['.schedule-tabs', '.is-active'],
    ['.platform-filter-tabs', '.active'],
    ['.credential-filter', '.active'],
    ['.kb-tabs', '.active'],
    ['.kb-seg', '.active'],
    ['.artifact-view-mode', '.active'],
    ['.bot-tabs', '.active'],
    ['.bot-record-tabs', '.active'],
    ['.collab-dock-tabs:not(.collab-overview-head)', '.active', ':scope > [role="tab"]'],
    ['.collab-browser-tabs', '.active', ':scope > [role="tab"]'],
    ['.upp-theme-seg', '.active'],
    ['.req-steps', '[aria-selected="true"]'],
  ];
  const same = (a, b) => a && b && ['x', 'y', 'width', 'height', 'opacity'].every(key => a[key] === b[key]);
  const blocked = item => item.matches(':disabled, [disabled], [aria-disabled="true"], [hidden]') || !!item.closest('[inert]');
  const visible = item => !item.hidden && item.getClientRects().length && getComputedStyle(item).visibility !== 'hidden';

  function remember(key, geometry) {
    if (!key || !geometry || !geometry.width || !geometry.height) return;
    geometryCache.delete(key);
    geometryCache.set(key, { ...geometry });
    if (geometryCache.size > 240) geometryCache.delete(geometryCache.keys().next().value);
  }
  function writeGeometry(state, geometry) {
    if (same(state.geometry, geometry)) return;
    state.geometry = { ...geometry };
    const style = state.indicator.style;
    style.opacity = String(geometry.opacity);
    if (geometry.width && geometry.height) {
      style.transform = `translate3d(${geometry.x}px, ${geometry.y}px, 0)`;
      style.width = geometry.width + 'px';
      style.height = geometry.height + 'px';
    }
  }
  function capture(state) {
    if (!state.key || !state.root.isConnected || !state.geometry?.opacity) return;
    const style = getComputedStyle(state.indicator);
    const matrix = style.transform === 'none' ? null : new DOMMatrixReadOnly(style.transform);
    remember(state.key, {
      x: matrix?.m41 ?? state.geometry.x,
      y: matrix?.m42 ?? state.geometry.y,
      width: parseFloat(style.width) || state.geometry.width,
      height: parseFloat(style.height) || state.geometry.height,
      opacity: Number(style.opacity),
    });
  }
  function itemKey(item, index) {
    const attribute = [...item.attributes].find(attr => attr.name.startsWith('data-') && !/^data-(?:sliding|icon|hover|af)-/.test(attr.name));
    return attribute ? attribute.name + '=' + attribute.value : item.id || item.textContent.trim() || String(index);
  }
  function groupKey(root, selector) {
    if (root.id) return 'id:' + root.id;
    const owner = root.parentElement?.closest('[id]') || document.body;
    const peers = [...owner.querySelectorAll(selector)];
    return (owner.id || 'body') + ':' + selector + ':' + peers.indexOf(root);
  }

  function mount(root, options = {}) {
    if (!root) return { refresh() {}, dispose() {} };
    const existing = mounted.get(root);
    if (existing) {
      existing.api.refresh();
      return existing.api;
    }
    let indicator = root.querySelector(':scope > [data-sliding-indicator]');
    const ownedIndicator = !indicator;
    if (!indicator) {
      indicator = document.createElement('span');
      indicator.className = 'sliding-tabs-indicator';
      indicator.setAttribute('data-sliding-indicator', '');
      indicator.setAttribute('aria-hidden', 'true');
    }
    const wasClass = root.classList.contains('sliding-tabs');
    const wasAttribute = root.hasAttribute('data-sliding-tabs');
    const oldPosition = root.style.position;
    const inline = getComputedStyle(root).display.startsWith('inline');
    const state = {
      root, indicator, ownedIndicator, wasClass, wasAttribute, oldPosition,
      key: options.cacheKey || null,
      activeSelector: options.activeSelector || defaults,
      itemSelector: options.itemSelector || ':scope > button, :scope > [role="tab"]',
      auto: !!options.auto,
      oldRole: root.getAttribute('role'), oldOrientation: root.getAttribute('aria-orientation'), restoring: false,
      disposed: false, frame: 0, readyFrame: 0, items: [], observed: new Set(), original: new Map(), geometry: null,
    };
    mounted.set(root, state);
    instances.add(state);
    root.classList.add('sliding-tabs');
    root.setAttribute('data-sliding-tabs', '');
    if (inline) root.setAttribute('data-sliding-inline', '');
    if (options.compact) root.setAttribute('data-sliding-compact', '');
    if (getComputedStyle(root).position === 'static') root.style.position = 'relative';
    if (!root.hasAttribute('role') || (state.auto && root.getAttribute('role') === 'group')) root.setAttribute('role', 'tablist');
    if (root.getAttribute('role') === 'tablist') root.setAttribute('aria-orientation', 'horizontal');
    if (ownedIndicator) root.prepend(indicator);

    function schedule() {
      if (state.disposed || state.frame) return;
      state.frame = requestAnimationFrame(() => {
        state.frame = 0;
        refresh();
      });
    }
    function annotate(item) {
      if (!state.original.has(item)) state.original.set(item, {
        role: item.getAttribute('role'), tabIndex: item.getAttribute('tabindex'), selected: item.getAttribute('aria-selected'),
      });
      item.setAttribute('data-sliding-tab', '');
      if (root.getAttribute('role') === 'tablist' && !item.hasAttribute('role')) item.setAttribute('role', 'tab');
    }
    function refresh() {
      if (state.disposed) return;
      if (!root.contains(indicator)) root.prepend(indicator);
      const items = [...root.querySelectorAll(state.itemSelector)].filter(item => !item.hasAttribute('data-sliding-indicator'));
      const currentItems = new Set(items);
      for (const previous of state.original.keys()) if (!currentItems.has(previous)) state.original.delete(previous);
      // Business selection survives hidden routes, closing panes and inert DOM.
      // Visibility and disabled state only decide which tabs accept keyboard focus.
      const active = items.find(item => item.matches(state.activeSelector));
      const enabled = items.filter(item => !blocked(item) && visible(item));
      const focusTarget = enabled.includes(active) ? active : enabled[0];
      state.items = items;
      state.active = active;
      for (const item of items) {
        annotate(item);
        const selected = item === active;
        item.setAttribute('data-sliding-selected', String(selected));
        if (state.auto && root.getAttribute('role') === 'tablist') {
          if (item.getAttribute('aria-selected') !== String(selected)) item.setAttribute('aria-selected', String(selected));
        }
        const tabIndex = item === focusTarget ? '0' : '-1';
        if (item.getAttribute('tabindex') !== tabIndex) item.setAttribute('tabindex', tabIndex);
      }
      const targets = new Set([root, ...items]);
      for (const observed of state.observed) if (!targets.has(observed)) {
        state.resize?.unobserve(observed);
        state.observed.delete(observed);
      }
      for (const item of targets) if (!state.observed.has(item)) {
        state.resize?.observe(item);
        state.observed.add(item);
      }
      if (!active || !visible(active)) {
        writeGeometry(state, { ...(state.geometry || {}), opacity: 0 });
        return;
      }
      const bounds = root.getBoundingClientRect();
      const target = active.getBoundingClientRect();
      const scaleX = root.offsetWidth ? bounds.width / root.offsetWidth : 1;
      const scaleY = root.offsetHeight ? bounds.height / root.offsetHeight : 1;
      if (!target.width || !target.height || !scaleX || !scaleY) return;
      const next = {
        x: (target.left - bounds.left) / scaleX - root.clientLeft + root.scrollLeft,
        y: (target.top - bounds.top) / scaleY - root.clientTop + root.scrollTop,
        width: target.width / scaleX,
        height: target.height / scaleY,
        opacity: 1,
      };
      if (!state.restoring) {
        writeGeometry(state, next);
        remember(state.key, next);
      }
    }
    function keydown(event) {
      if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || event.isComposing ||
          !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
      const item = state.items.find(candidate => candidate === event.target || candidate.contains(event.target));
      if (!item || event.target.closest('input, textarea, select, [contenteditable="true"]')) return;
      const enabled = state.items.filter(candidate => !blocked(candidate) && visible(candidate));
      if (!enabled.length) return;
      event.preventDefault();
      const index = enabled.indexOf(item);
      const rtl = getComputedStyle(root).direction === 'rtl';
      const forward = event.key === (rtl ? 'ArrowLeft' : 'ArrowRight');
      const nextIndex = event.key === 'Home' ? 0 : event.key === 'End' ? enabled.length - 1 :
        index < 0 ? (forward ? 0 : enabled.length - 1) : (index + (forward ? 1 : -1) + enabled.length) % enabled.length;
      const next = enabled[nextIndex];
      const key = itemKey(next, nextIndex);
      capture(state);
      const button = next.matches('button') ? next : next.querySelector('button');
      if (next !== state.active) button?.click();
      const restoreFocus = () => {
        if (root.isConnected && next.isConnected) {
          next.focus({ preventScroll: true });
          next.scrollIntoView({ block: 'nearest', inline: 'nearest' });
          refresh();
        } else if (state.key) {
          for (const replacement of instances) if (replacement.key === state.key && replacement.root.isConnected) {
            const target = replacement.items.find((candidate, i) => itemKey(candidate, i) === key);
            target?.focus({ preventScroll: true });
            target?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
          }
        }
      };
      queueMicrotask(() => {
        if (root.isConnected) restoreFocus();
        else requestAnimationFrame(restoreFocus);
      });
    }
    const captureClick = () => capture(state);
    state.resize = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(schedule);
    state.mutation = new MutationObserver(records => {
      if (records.some(record => record.target !== indicator && !indicator.contains(record.target))) schedule();
    });
    state.mutation.observe(root, {
      subtree: true, childList: true, characterData: true, attributes: true,
      attributeFilter: ['class', 'style', 'hidden', 'disabled', 'aria-selected', 'aria-pressed', 'aria-checked'],
    });
    root.addEventListener('keydown', keydown);
    root.addEventListener('scroll', schedule, { passive: true });
    root.addEventListener('click', captureClick, true);
    window.addEventListener('resize', schedule);
    const cached = state.key && geometryCache.get(state.key);
    if (cached) {
      state.restoring = true;
      refresh();
      state.restoring = false;
      if (state.active && visible(state.active)) {
        writeGeometry(state, cached);
        indicator.getBoundingClientRect();
      }
      root.setAttribute('data-sliding-ready', '');
      schedule();
    } else {
      refresh();
      state.readyFrame = requestAnimationFrame(() => {
        state.readyFrame = 0;
        if (!state.disposed) root.setAttribute('data-sliding-ready', '');
      });
    }
    document.fonts?.ready.then(() => { if (!state.disposed) schedule(); });
    state.api = {
      refresh,
      dispose() {
        if (state.disposed) return;
        capture(state);
        state.disposed = true;
        cancelAnimationFrame(state.frame);
        cancelAnimationFrame(state.readyFrame);
        state.resize?.disconnect();
        state.mutation.disconnect();
        root.removeEventListener('keydown', keydown);
        root.removeEventListener('scroll', schedule);
        root.removeEventListener('click', captureClick, true);
        window.removeEventListener('resize', schedule);
        for (const [item, original] of state.original) {
          item.removeAttribute('data-sliding-tab');
          item.removeAttribute('data-sliding-selected');
          for (const [attribute, value] of [['role', original.role], ['tabindex', original.tabIndex], ['aria-selected', original.selected]]) {
            if (value === null) item.removeAttribute(attribute); else item.setAttribute(attribute, value);
          }
        }
        if (ownedIndicator) indicator.remove();
        if (!wasClass) root.classList.remove('sliding-tabs');
        if (!wasAttribute) root.removeAttribute('data-sliding-tabs');
        root.removeAttribute('data-sliding-ready');
        root.removeAttribute('data-sliding-inline');
        root.removeAttribute('data-sliding-compact');
        for (const [attribute, value] of [['role', state.oldRole], ['aria-orientation', state.oldOrientation]]) {
          if (value === null) root.removeAttribute(attribute); else root.setAttribute(attribute, value);
        }
        if (root.style.position === 'relative') root.style.position = oldPosition;
        mounted.delete(root);
        instances.delete(state);
      },
    };
    return state.api;
  }

  window.AgentFlowSlidingTabs = { mount };
  // The React iframe opts in through its wrapper. Only the native host scans.
  function scanNative() {
    for (const state of instances) if (state.auto && !state.root.isConnected) state.api.dispose();
    for (const [selector, activeSelector, itemSelector] of registrations) {
      for (const root of document.querySelectorAll(selector)) {
        if (!mounted.has(root)) mount(root, { activeSelector, itemSelector, auto: true, compact: true, cacheKey: groupKey(root, selector) });
      }
    }
  }
  function startNative() {
    if (!document.querySelector('.app-shell')) return;
    scanNative();
    let queued = false;
    const observer = new MutationObserver(() => {
      if (queued) return;
      queued = true;
      queueMicrotask(() => { queued = false; scanNative(); });
    });
    observer.observe(document.body, { subtree: true, childList: true });
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', startNative, { once: true });
  else startNative();
})();
