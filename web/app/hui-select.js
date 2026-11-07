/* Harness UI Selector (MIT, see harness-ui-LICENSE.txt),
 * layered over a native <select>. The select keeps its id, value and change
 * event; this file only draws the option panel and, in trigger mode, the
 * trigger button.
 *
 * - Every single <select> outside [data-native] is handled in place: the
 *   select stays the visible trigger (hui-controls.css restyles it), and
 *   document-level listeners swap the browser popup for the Harness panel.
 *   The panel only exists while open, so re-rendered views leak nothing.
 * - enhanceSelect(select) switches a select to trigger mode (a Harness button
 *   after the hidden select) for toolbars styled around .hui-select-trigger.
 *   Instances are destroyed once their select leaves the document.
 */
(function () {
  'use strict';
  if (typeof window === 'undefined') return;
  const doc = typeof document !== 'undefined' ? document : null;
  const supported = !!(doc && typeof doc.createElement === 'function' && typeof MutationObserver === 'function');

  // Only one Harness popover (selector panel, date panel or heading menu) is
  // open at a time. Popovers that pass `contains` are also closed by outside
  // pointerdown, resize and outer scroll through the single set of listeners
  // below.
  let active = null;
  function claim(close, contains) {
    if (active && active.close !== close) active.close(false);
    active = { close, contains: typeof contains === 'function' ? contains : null };
  }
  function release(close) {
    if (active && active.close === close) active = null;
  }
  function closeAll() {
    active?.close(false);
  }
  function viewport() {
    const vv = window.visualViewport;
    return {
      width: vv?.width || window.innerWidth || doc.documentElement?.clientWidth || 0,
      height: vv?.height || window.innerHeight || doc.documentElement?.clientHeight || 0,
    };
  }
  // Fixed-position placement below the anchor, flipping above when there is
  // more room there, and clamped inside the viewport.
  function place(anchor, panel, options) {
    const opts = options || {};
    const gap = opts.gap ?? 6;
    const rect = anchor.getBoundingClientRect();
    const { width: viewportW, height: viewportH } = viewport();
    const room = Math.max(0, (viewportW || 0) - 16) || Infinity;
    const width = Math.min(opts.width || Math.max(rect.width, opts.minWidth ?? 240), room);
    const below = viewportH - rect.bottom - 12;
    const above = rect.top - 12;
    const height = Math.min(panel.scrollHeight || 320, opts.maxHeight ?? 360);
    const down = below >= height || below >= above;
    const left = Math.min(Math.max(8, rect.left), Math.max(8, viewportW - width - 8));
    panel.style.width = width + 'px';
    panel.style.left = Math.round(left) + 'px';
    panel.style.maxHeight = Math.max(120, Math.floor(down ? below : above)) + 'px';
    panel.style.top = down ? Math.round(rect.bottom + gap) + 'px' : 'auto';
    panel.style.bottom = down ? 'auto' : Math.round(viewportH - rect.top + gap) + 'px';
    panel.dataset.placement = down ? 'bottom' : 'top';
    return panel.dataset.placement;
  }
  window.HuiPopover = Object.freeze({ claim, release, closeAll, place });

  const instances = new WeakMap();
  const live = new Set();
  let serial = 0;

  function icon(name, extra) {
    const svg = doc.createElementNS ? doc.createElementNS('http://www.w3.org/2000/svg', 'svg') : doc.createElement('svg');
    svg.setAttribute('class', 'agent-icon harness-icon' + (extra ? ' ' + extra : ''));
    svg.setAttribute('data-icon', name);
    svg.setAttribute('width', '14');
    svg.setAttribute('height', '14');
    svg.setAttribute('aria-hidden', 'true');
    svg.setAttribute('focusable', 'false');
    return svg;
  }
  function hydrate(root) {
    window.AgentFlowHarnessIcons?.hydrate?.(root);
  }
  function el(tag, className, text) {
    const node = doc.createElement(tag);
    if (className) node.className = className;
    if (text != null) node.textContent = text;
    return node;
  }
  function descriptor(target, name) {
    for (let proto = Object.getPrototypeOf(target); proto; proto = Object.getPrototypeOf(proto)) {
      const found = Object.getOwnPropertyDescriptor(proto, name);
      if (found) return found;
    }
    return null;
  }
  function optionMeta(option) {
    return option.getAttribute?.('data-meta') || option.dataset?.meta || '';
  }
  function optionSearch(option) {
    return option.getAttribute?.('data-search') || option.dataset?.search || '';
  }
  function optionGroup(option) {
    const parent = option.parentNode;
    return parent && parent.tagName === 'OPTGROUP' ? parent : null;
  }
  function connected(node) {
    if (!node) return false;
    if (typeof node.isConnected === 'boolean') return node.isConnected;
    return !!doc.documentElement?.contains?.(node);
  }
  // Label text without nested controls (a wrapping <label> also contains the
  // select's options or the Harness trigger).
  function ownText(label) {
    const walk = node => {
      if (node.nodeType === 3) return node.textContent;
      if (node.nodeType !== 1 || /^(select|button|textarea|input|svg)$/i.test(node.tagName)) return '';
      return [...node.childNodes].map(walk).join('');
    };
    return label ? walk(label).replace(/\s+/g, ' ').trim() : '';
  }
  function labelText(select) {
    const own = select.getAttribute('aria-label');
    if (own) return own;
    return ownText(select.labels?.[0]) || select.options?.[0]?.text || '';
  }
  function nativeHidden(select) {
    return !!select.hidden || select.style?.display === 'none';
  }

  // Panel + keyboard behaviour shared by both modes. `anchor` is the element
  // the panel hangs from and that gets focus back (trigger or select).
  function createSelector(select, anchor, mode) {
    const id = 'huiSelect' + (++serial);
    const panel = el('div', 'hui-select-panel');
    panel.id = id + 'Panel';
    panel.setAttribute('role', 'listbox');
    panel.inert = true;
    const searchBar = el('label', 'hui-select-search');
    const search = el('input');
    search.type = 'search';
    search.placeholder = '搜索…';
    search.setAttribute('aria-label', '搜索选项');
    searchBar.appendChild(icon('search'));
    searchBar.appendChild(search);
    const list = el('div', 'hui-select-options');
    const empty = el('div', 'hui-select-empty', '没有匹配的选项');
    empty.hidden = true;
    panel.appendChild(searchBar);
    panel.appendChild(list);
    panel.appendChild(empty);

    let buttons = [];
    let headers = [];
    let open = false;
    let watcher = null;
    let detachTimer = 0;

    function selectedIndex() {
      return Math.max(0, select.selectedIndex);
    }
    function visibleButtons() {
      return buttons.filter(button => !button.hidden && !button.disabled);
    }
    function applyFilter() {
      const query = String(search.value || '').trim().toLocaleLowerCase();
      buttons.forEach(button => {
        const text = (button.textContent + ' ' + (button.searchText || '')).toLocaleLowerCase();
        button.hidden = !!query && !text.includes(query);
      });
      headers.forEach(header => { header.hidden = header.huiOptions.every(button => button.hidden); });
      empty.hidden = visibleButtons().length > 0;
    }
    function render() {
      const focused = buttons.indexOf(doc.activeElement);
      const index = selectedIndex();
      const nodes = [];
      let group = null;
      let header = null;
      buttons = [];
      headers = [];
      [...select.options].forEach((option, i) => {
        if (option.hidden) return;
        const owner = optionGroup(option);
        if (owner !== group) {
          group = owner;
          header = null;
          if (owner) {
            header = el('div', 'hui-select-group', owner.label || owner.getAttribute?.('label') || '');
            header.setAttribute('role', 'presentation');
            header.huiOptions = [];
            headers.push(header);
            nodes.push(header);
          }
        }
        const button = el('button', 'hui-select-option');
        button.type = 'button';
        button.tabIndex = -1;
        button.setAttribute('role', 'option');
        button.setAttribute('aria-selected', String(i === index));
        button.classList.toggle('is-selected', i === index);
        button.disabled = !!(option.disabled || group?.disabled);
        button.huiIndex = i;
        button.searchText = optionSearch(option);
        button.appendChild(el('span', 'hui-select-option-label', option.text));
        const meta = el('span', 'hui-select-option-meta', optionMeta(option));
        if (i === index) meta.appendChild(icon('check', 'hui-select-check'));
        button.appendChild(meta);
        button.addEventListener('click', event => { event.stopPropagation?.(); choose(i); });
        header?.huiOptions.push(button);
        buttons.push(button);
        nodes.push(button);
      });
      list.replaceChildren(...nodes);
      searchBar.hidden = buttons.length <= 5;
      applyFilter();
      hydrate(panel);
      if (!open) return;
      if (focused >= 0) { (buttons[focused] || buttons.find(b => b.huiIndex === index))?.focus(); return; }
      // Focus outside the panel (or on a search box that just got hidden)
      // would leave Escape and the arrow keys without a target.
      const current = doc.activeElement;
      if (!panel.contains(current) || (current === search && searchBar.hidden)) focusOption();
    }
    function focusOption() {
      (buttons.find(b => b.huiIndex === selectedIndex() && !b.disabled && !b.hidden) || visibleButtons()[0])?.focus();
    }
    function attach() {
      if (detachTimer) clearTimeout(detachTimer);
      detachTimer = 0;
      if (panel.parentNode !== doc.body) doc.body.appendChild(panel);
    }
    // `via` is 'touch' when opened by a tap: the search box is not focused
    // then, so the on-screen keyboard stays down.
    function show(via) {
      if (open || select.disabled) return;
      claim(close, target => anchor.contains(target) || panel.contains(target));
      attach();
      open = true;
      search.value = '';
      const label = labelText(select);
      if (label) panel.setAttribute('aria-label', label);
      render();
      panel.inert = false;
      panel.classList.add('is-open');
      anchor.setAttribute('aria-expanded', 'true');
      anchor.setAttribute('aria-controls', panel.id);
      place(anchor, panel);
      if (mode === 'inline') {
        watcher = new MutationObserver(() => {
          if (select.disabled || !connected(select)) close(false);
          else render();
        });
        watcher.observe(select, { childList: true, subtree: true, attributes: true, characterData: true });
      }
      if (!searchBar.hidden && via !== 'touch') search.focus();
      else focusOption();
    }
    function close(focusAnchor) {
      if (!open) return;
      open = false;
      release(close);
      watcher?.disconnect();
      watcher = null;
      panel.classList.remove('is-open');
      panel.inert = true;
      anchor.setAttribute('aria-expanded', 'false');
      if (focusAnchor) anchor.focus();
      if (mode === 'inline') {
        // Keep the panel for its exit transition, then drop it from <body>.
        detachTimer = setTimeout(() => { if (!open) panel.remove(); }, 240);
      }
    }
    function choose(index) {
      const option = select.options[index];
      if (!option || option.disabled || optionGroup(option)?.disabled) return;
      const changed = select.selectedIndex !== index;
      if (changed) {
        select.selectedIndex = index;
        select.dispatchEvent(new Event('input', { bubbles: true }));
        select.dispatchEvent(new Event('change', { bubbles: true }));
      }
      close(true);
    }
    function move(step, edge) {
      const items = visibleButtons();
      if (!items.length) return;
      const at = items.indexOf(doc.activeElement);
      let next;
      if (edge === 'start') next = 0;
      else if (edge === 'end') next = items.length - 1;
      else next = at < 0 ? (step > 0 ? 0 : items.length - 1) : (at + step + items.length) % items.length;
      items[next].focus();
      items[next].scrollIntoView?.({ block: 'nearest' });
    }

    search.addEventListener('input', applyFilter);
    panel.addEventListener('keydown', event => {
      if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation?.(); close(true); return; }
      if (event.key === 'Tab') { close(false); anchor.focus(); return; }
      if (event.key === 'ArrowDown') { event.preventDefault(); move(1); return; }
      if (event.key === 'ArrowUp') { event.preventDefault(); move(-1); return; }
      if (event.key === 'Home' && doc.activeElement !== search) { event.preventDefault(); move(0, 'start'); return; }
      if (event.key === 'End' && doc.activeElement !== search) { event.preventDefault(); move(0, 'end'); return; }
      if (event.key === 'Enter' && doc.activeElement === search) {
        event.preventDefault();
        const first = visibleButtons()[0];
        if (first) choose(first.huiIndex);
      }
    });

    return {
      id,
      select,
      panel,
      render,
      show,
      close,
      isOpen: () => open,
      dispose() {
        close(false);
        if (detachTimer) clearTimeout(detachTimer);
        panel.remove();
      },
    };
  }

  // ---------- Trigger mode ----------
  function enhanceSelect(select) {
    if (!supported || !select) return null;
    if (instances.has(select)) return instances.get(select);
    const trigger = el('button', 'hui-select-trigger');
    trigger.type = 'button';
    trigger.setAttribute('aria-haspopup', 'listbox');
    trigger.setAttribute('aria-expanded', 'false');
    const lead = select.getAttribute('data-icon');
    if (lead) trigger.appendChild(icon(lead, 'hui-select-lead'));
    const value = el('span', 'hui-select-value');
    trigger.appendChild(value);
    trigger.appendChild(icon('chevron-down', 'hui-select-chevron'));
    const selector = createSelector(select, trigger, 'trigger');
    const panel = selector.panel;
    trigger.id = selector.id + 'Trigger';
    trigger.setAttribute('aria-controls', panel.id);
    const label = labelText(select);
    if (label) {
      trigger.setAttribute('aria-label', label);
      panel.setAttribute('aria-label', label);
    }
    if (select.style?.width) trigger.style.width = select.style.width;

    select.classList.add('hui-select-native');
    select.setAttribute('aria-hidden', 'true');
    const tabIndex = select.tabIndex;
    select.tabIndex = -1;
    select.after(trigger);
    doc.body.appendChild(panel);

    function sync() {
      const current = select.options[Math.max(0, select.selectedIndex)];
      value.textContent = current ? current.text : '';
      trigger.classList.toggle('is-filtered', select.selectedIndex > 0);
      trigger.disabled = !!select.disabled;
      trigger.hidden = nativeHidden(select);
      if (trigger.disabled && selector.isOpen()) selector.close(false);
      if (selector.isOpen()) selector.render();
    }

    // Existing code assigns select.value / disabled directly (e.g. after
    // re-rendering the assignee options); mirror those writes, which do not
    // always produce mutations.
    const patched = [];
    for (const name of ['value', 'selectedIndex', 'disabled']) {
      const base = descriptor(select, name);
      if (!base || !base.set) continue;
      Object.defineProperty(select, name, {
        configurable: true,
        enumerable: base.enumerable,
        get() { return base.get.call(this); },
        set(next) { base.set.call(this, next); sync(); },
      });
      patched.push(name);
    }
    // The select is display:none; send programmatic focus to the trigger.
    select.focus = function () { trigger.focus(); };
    patched.push('focus');
    select.addEventListener('change', sync);
    const observer = new MutationObserver(sync);
    observer.observe(select, { childList: true, subtree: true, attributes: true, characterData: true });

    trigger.addEventListener('click', event => {
      event.stopPropagation?.();
      if (selector.isOpen()) selector.close(true); else selector.show();
    });
    trigger.addEventListener('keydown', event => {
      if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
        event.preventDefault();
        selector.show();
      }
    });

    const api = {
      node: select,
      select,
      trigger,
      panel,
      refresh: sync,
      open: selector.show,
      close: selector.close,
      isOpen: selector.isOpen,
      destroy() {
        if (!instances.has(select)) return;
        selector.dispose();
        observer.disconnect();
        select.removeEventListener?.('change', sync);
        patched.forEach(name => { delete select[name]; });
        select.classList.remove('hui-select-native');
        select.removeAttribute?.('aria-hidden');
        select.tabIndex = tabIndex;
        trigger.remove?.();
        instances.delete(select);
        untrack(api);
      },
      dispose() { api.destroy(); },
    };
    instances.set(select, api);
    track(api);
    sync();
    hydrate(trigger);
    return api;
  }

  // ---------- In-place mode ----------
  const inline = new WeakMap();
  let inlineOpen = null;
  function eligible(target) {
    if (!target || target.tagName !== 'SELECT') return false;
    if (target.multiple || Number(target.size) > 1 || target.disabled) return false;
    if (instances.has(target)) return false;
    return !target.closest?.('[data-native]');
  }
  function inlineSelector(select) {
    let selector = inline.get(select);
    if (!selector) {
      selector = createSelector(select, select, 'inline');
      inline.set(select, selector);
    }
    return selector;
  }
  function toggleInline(select, via) {
    const selector = inlineSelector(select);
    if (selector.isOpen()) { selector.close(true); return; }
    if (doc.activeElement !== select) select.focus?.({ preventScroll: true });
    selector.show(via);
    inlineOpen = selector.isOpen() ? selector : null;
    if (inlineOpen) watchRemovals();
  }
  const openKeys = new Set(['ArrowDown', 'ArrowUp', 'Enter', ' ', 'F4']);
  let touch = null;

  // ---------- Auto-enhance and removal cleanup ----------
  // One document observer serves every Harness control: registered selectors
  // are enhanced when inserted, tracked instances are destroyed once their
  // native element leaves the document. Batches are merged per microtask.
  const autos = [];
  let observer = null;
  let added = [];
  let removed = false;
  let scheduled = false;
  function enhanceWithin(nodes) {
    for (const node of nodes) {
      if (!node || node.nodeType !== 1 || !connected(node)) continue;
      for (const { selector, enhance } of autos) {
        if (node.matches?.(selector)) enhance(node);
        node.querySelectorAll?.(selector).forEach(enhance);
      }
    }
  }
  function flush() {
    scheduled = false;
    const nodes = added;
    added = [];
    if (nodes.length && autos.length) enhanceWithin(nodes);
    if (!removed) return;
    removed = false;
    live.forEach(api => { if (!connected(api.node)) api.destroy(); });
    if (inlineOpen && inlineOpen.isOpen() && !connected(inlineOpen.select)) inlineOpen.close(false);
    if (inlineOpen && !inlineOpen.isOpen()) inlineOpen = null;
  }
  function watchRemovals() {
    if (observer || !doc.documentElement) return;
    observer = new MutationObserver(records => {
      for (const record of records) {
        if (record.removedNodes?.length) removed = true;
        if (autos.length && record.addedNodes?.length) added.push(...record.addedNodes);
      }
      if (scheduled || (!removed && !added.length)) return;
      scheduled = true;
      Promise.resolve().then(flush);
    });
    observer.observe(doc.documentElement, { childList: true, subtree: true });
  }
  function track(api) {
    live.add(api);
    watchRemovals();
  }
  function untrack(api) {
    live.delete(api);
  }
  function whenReady(fn) {
    if (doc.readyState === 'loading') doc.addEventListener('DOMContentLoaded', fn, { once: true });
    else fn();
  }
  function auto(selector, enhance) {
    if (!supported) return;
    autos.push({ selector, enhance });
    watchRemovals();
    whenReady(() => enhanceWithin([doc.documentElement]));
  }
  window.HuiAuto = Object.freeze({ auto, track, untrack, flush });

  if (supported && typeof doc.addEventListener === 'function') {
    doc.addEventListener('mousedown', event => {
      const target = event.target;
      if (event.button !== 0 || !eligible(target)) return;
      event.preventDefault();
      toggleInline(target);
    }, true);
    doc.addEventListener('touchstart', event => {
      const point = event.touches?.[0];
      touch = eligible(event.target) && point ? { target: event.target, x: point.clientX, y: point.clientY } : null;
    }, { capture: true, passive: true });
    doc.addEventListener('touchend', event => {
      const start = touch;
      touch = null;
      const point = event.changedTouches?.[0];
      if (!start || start.target !== event.target || !point) return;
      if (Math.abs(point.clientX - start.x) > 8 || Math.abs(point.clientY - start.y) > 8) return;
      event.preventDefault();
      toggleInline(start.target, 'touch');
    }, true);
    doc.addEventListener('keydown', event => {
      const target = event.target;
      if (!eligible(target) || !openKeys.has(event.key) || event.altKey && event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
      if (inline.get(target)?.isOpen()) return;
      event.preventDefault();
      toggleInline(target);
    }, true);
    // A label click focuses its control; a hidden trigger-mode select cannot
    // take focus, so hand it to the trigger.
    doc.addEventListener('click', event => {
      const label = event.target?.closest?.('label');
      const api = label?.control && instances.get(label.control);
      if (!api || api.trigger.contains(event.target) || api.trigger.disabled) return;
      event.preventDefault();
      api.trigger.focus();
    });
    doc.addEventListener('pointerdown', event => {
      if (active?.contains && !active.contains(event.target)) active.close(false);
    });
    window.addEventListener?.('resize', () => { if (active?.contains) active.close(false); }, { passive: true });
    window.addEventListener?.('scroll', event => {
      if (active?.contains && !active.contains(event.target)) active.close(false);
    }, { passive: true, capture: true });
  }

  window.enhanceSelect = enhanceSelect;
  window.HuiSelect = Object.freeze({
    enhance: enhanceSelect,
    claim,
    release,
    closeAll,
    destroy(select) { instances.get(select)?.destroy(); },
    instanceCount: () => [...live].filter(api => instances.get(api.node) === api).length,
  });
})();
