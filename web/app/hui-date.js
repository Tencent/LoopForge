/* Harness date picker layered over a native <input type="date">. The input
 * keeps its name, value, min/max and input/change events; this file draws a
 * trigger button after it and a month calendar in the shared popover slot
 * (window.HuiPopover). Inputs inside [data-native] keep the browser control.
 */
(function () {
  'use strict';
  if (typeof window === 'undefined') return;
  const doc = typeof document !== 'undefined' ? document : null;
  const supported = !!(doc && typeof doc.createElement === 'function' && typeof MutationObserver === 'function');
  const instances = new WeakMap();
  const WEEKDAYS = ['一', '二', '三', '四', '五', '六', '日'];
  const PLACEHOLDER = '选择日期';
  let serial = 0;

  const pad = n => String(n).padStart(2, '0');
  function format(date) {
    return date.getFullYear() + '-' + pad(date.getMonth() + 1) + '-' + pad(date.getDate());
  }
  function parse(value) {
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(String(value || ''));
    if (!m) return null;
    const date = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
    return format(date) === m[0] ? date : null;
  }
  function addDays(date, days) {
    return new Date(date.getFullYear(), date.getMonth(), date.getDate() + days);
  }
  function addMonths(date, months) {
    const target = new Date(date.getFullYear(), date.getMonth() + months, 1);
    const last = new Date(target.getFullYear(), target.getMonth() + 1, 0).getDate();
    return new Date(target.getFullYear(), target.getMonth(), Math.min(date.getDate(), last));
  }
  function today() {
    const now = new Date();
    return new Date(now.getFullYear(), now.getMonth(), now.getDate());
  }
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
  function el(tag, className, text) {
    const node = doc.createElement(tag);
    if (className) node.className = className;
    if (text != null) node.textContent = text;
    return node;
  }
  function button(className, text, label) {
    const node = el('button', className, text);
    node.type = 'button';
    if (label) node.setAttribute('aria-label', label);
    return node;
  }
  function descriptor(target, name) {
    for (let proto = Object.getPrototypeOf(target); proto; proto = Object.getPrototypeOf(proto)) {
      const found = Object.getOwnPropertyDescriptor(proto, name);
      if (found) return found;
    }
    return null;
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
  function isDateInput(node) {
    return !!node && node.tagName === 'INPUT' && String(node.getAttribute?.('type') || node.type || '').toLowerCase() === 'date';
  }

  function enhance(input) {
    if (!supported || !isDateInput(input)) return null;
    if (instances.has(input)) return instances.get(input);
    if (input.closest?.('[data-native]')) return null;
    const popover = window.HuiPopover;
    const id = 'huiDate' + (++serial);

    const trigger = button('hui-date-trigger');
    trigger.id = id + 'Trigger';
    trigger.setAttribute('aria-haspopup', 'dialog');
    trigger.setAttribute('aria-expanded', 'false');
    trigger.setAttribute('aria-controls', id + 'Panel');
    const value = el('span', 'hui-date-value');
    trigger.appendChild(icon('calendar'));
    trigger.appendChild(value);
    if (input.style?.width) trigger.style.width = input.style.width;

    const panel = el('div', 'hui-date-panel');
    panel.id = id + 'Panel';
    panel.setAttribute('role', 'dialog');
    panel.setAttribute('aria-label', '选择日期');
    panel.inert = true;
    const head = el('div', 'hui-date-head');
    const prev = button('hui-date-nav', null, '上个月');
    prev.appendChild(icon('chevron-left'));
    const title = el('b', 'hui-date-title');
    title.setAttribute('aria-live', 'polite');
    const next = button('hui-date-nav', null, '下个月');
    next.appendChild(icon('chevron-right'));
    head.appendChild(prev);
    head.appendChild(title);
    head.appendChild(next);
    const grid = el('div', 'hui-date-grid');
    grid.setAttribute('role', 'grid');
    const foot = el('div', 'hui-date-foot');
    const clear = button('hui-date-action', '清除');
    const todayButton = button('hui-date-action', '今天');
    foot.appendChild(clear);
    foot.appendChild(todayButton);
    panel.appendChild(head);
    panel.appendChild(grid);
    panel.appendChild(foot);

    input.classList.add('hui-date-native');
    input.setAttribute('aria-hidden', 'true');
    const tabIndex = input.tabIndex;
    input.tabIndex = -1;
    input.after(trigger);

    const base = descriptor(input, 'value');
    const read = () => (base?.get ? base.get.call(input) : input.value);
    const write = next => { if (base?.set) base.set.call(input, next); else input.value = next; };
    let open = false;
    let view = today();
    let focusDate = today();
    let detachTimer = 0;

    function bounds() {
      let min = parse(input.min || input.getAttribute?.('min'));
      let max = parse(input.max || input.getAttribute?.('max'));
      if (min && max && min > max) { min = null; max = null; }
      return { min, max };
    }
    function allowed(date) {
      const { min, max } = bounds();
      return !(min && date < min) && !(max && date > max);
    }
    function clamp(date) {
      const { min, max } = bounds();
      if (min && date < min) return min;
      if (max && date > max) return max;
      return date;
    }
    function label() {
      const own = input.getAttribute('aria-label');
      if (own) return own;
      return ownText(input.labels?.[0]) || PLACEHOLDER;
    }
    function sync() {
      const current = read();
      value.textContent = current || PLACEHOLDER;
      value.classList.toggle('is-placeholder', !current);
      trigger.disabled = !!input.disabled || !!input.readOnly;
      trigger.hidden = !!input.hidden || input.style?.display === 'none';
      trigger.setAttribute('aria-label', label() + (current ? '：' + current : ''));
      if (open && trigger.disabled) close(false);
      else if (open) render();
    }
    function render() {
      const selected = parse(read());
      const now = today();
      const y = view.getFullYear();
      const m = view.getMonth();
      title.textContent = y + ' 年 ' + (m + 1) + ' 月';
      const { min, max } = bounds();
      prev.disabled = !!min && new Date(y, m, 0) < min;
      next.disabled = !!max && new Date(y, m + 1, 1) > max;
      const first = new Date(y, m, 1);
      const start = addDays(first, -((first.getDay() + 6) % 7));
      const cells = WEEKDAYS.map(name => {
        const cell = el('span', 'hui-date-weekday', name);
        cell.setAttribute('role', 'columnheader');
        return cell;
      });
      for (let i = 0; i < 42; i++) {
        const date = addDays(start, i);
        const key = format(date);
        const day = button('hui-date-day', String(date.getDate()), key);
        day.setAttribute('role', 'gridcell');
        day.dataset.date = key;
        day.tabIndex = key === format(focusDate) ? 0 : -1;
        day.classList.toggle('is-outside', date.getMonth() !== m);
        day.classList.toggle('is-today', key === format(now));
        const isSelected = !!selected && key === format(selected);
        day.classList.toggle('is-selected', isSelected);
        day.setAttribute('aria-selected', String(isSelected));
        if (key === format(now)) day.setAttribute('aria-current', 'date');
        day.disabled = !allowed(date);
        cells.push(day);
      }
      grid.replaceChildren(...cells);
      clear.disabled = !read() || !!input.required;
      todayButton.disabled = !allowed(now);
      window.AgentFlowHarnessIcons?.hydrate?.(panel);
    }
    function dayButton(date) {
      const key = format(date);
      return grid.children.find ? grid.children.find(node => node.dataset?.date === key) : grid.querySelector?.('[data-date="' + key + '"]');
    }
    function focusOn(date) {
      focusDate = clamp(date);
      if (focusDate.getMonth() !== view.getMonth() || focusDate.getFullYear() !== view.getFullYear()) {
        view = new Date(focusDate.getFullYear(), focusDate.getMonth(), 1);
      }
      render();
      dayButton(focusDate)?.focus();
    }
    function show() {
      if (open || trigger.disabled) return;
      popover?.claim(close, target => trigger.contains(target) || panel.contains(target));
      if (detachTimer) clearTimeout(detachTimer);
      detachTimer = 0;
      if (panel.parentNode !== doc.body) doc.body.appendChild(panel);
      open = true;
      focusDate = clamp(parse(read()) || today());
      view = new Date(focusDate.getFullYear(), focusDate.getMonth(), 1);
      render();
      panel.inert = false;
      panel.classList.add('is-open');
      trigger.setAttribute('aria-expanded', 'true');
      popover?.place(trigger, panel, { width: 264, maxHeight: 400 });
      dayButton(focusDate)?.focus();
    }
    function close(focusTrigger) {
      if (!open) return;
      open = false;
      popover?.release(close);
      panel.classList.remove('is-open');
      panel.inert = true;
      trigger.setAttribute('aria-expanded', 'false');
      if (focusTrigger) trigger.focus();
      if (typeof setTimeout === 'function') detachTimer = setTimeout(() => { if (!open) panel.remove(); }, 240);
      else panel.remove();
    }
    function choose(next) {
      if (next !== read()) {
        write(next);
        input.dispatchEvent(new Event('input', { bubbles: true }));
        input.dispatchEvent(new Event('change', { bubbles: true }));
      }
      sync();
      close(true);
    }

    trigger.addEventListener('click', event => {
      event.stopPropagation?.();
      if (open) close(true); else show();
    });
    trigger.addEventListener('keydown', event => {
      if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
        event.preventDefault();
        show();
      }
    });
    prev.addEventListener('click', () => { view = addMonths(view, -1); focusDate = clamp(addMonths(focusDate, -1)); render(); });
    next.addEventListener('click', () => { view = addMonths(view, 1); focusDate = clamp(addMonths(focusDate, 1)); render(); });
    grid.addEventListener('click', event => {
      const day = event.target?.closest?.('.hui-date-day') || (event.target?.classList?.contains('hui-date-day') ? event.target : null);
      if (!day || day.disabled) return;
      choose(day.dataset.date);
    });
    clear.addEventListener('click', () => { if (!clear.disabled) choose(''); });
    todayButton.addEventListener('click', () => { if (!todayButton.disabled) choose(format(today())); });
    panel.addEventListener('keydown', event => {
      if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation?.(); close(true); return; }
      if (event.key === 'Tab') {
        const focusable = [prev, next, dayButton(focusDate), clear, todayButton].filter(node => node && !node.disabled);
        const at = focusable.indexOf(doc.activeElement);
        event.preventDefault();
        const step = event.shiftKey ? -1 : 1;
        focusable[(at + step + focusable.length) % focusable.length]?.focus();
        return;
      }
      const onDay = doc.activeElement?.classList?.contains('hui-date-day');
      if (!onDay) return;
      const moves = {
        ArrowLeft: () => addDays(focusDate, -1),
        ArrowRight: () => addDays(focusDate, 1),
        ArrowUp: () => addDays(focusDate, -7),
        ArrowDown: () => addDays(focusDate, 7),
        PageUp: () => addMonths(focusDate, event.shiftKey ? -12 : -1),
        PageDown: () => addMonths(focusDate, event.shiftKey ? 12 : 1),
        Home: () => addDays(focusDate, -((focusDate.getDay() + 6) % 7)),
        End: () => addDays(focusDate, 6 - ((focusDate.getDay() + 6) % 7)),
      };
      if (moves[event.key]) {
        event.preventDefault();
        focusOn(moves[event.key]());
      }
    });

    // Existing code writes input.value directly (metrics range buttons).
    if (base?.set) {
      Object.defineProperty(input, 'value', {
        configurable: true,
        enumerable: base.enumerable,
        get() { return base.get.call(this); },
        set(next) { base.set.call(this, next); sync(); },
      });
    }
    const focus = input.focus;
    input.focus = function () { trigger.focus(); };
    const observer = new MutationObserver(sync);
    observer.observe(input, { attributes: true });
    input.addEventListener('change', sync);

    const api = {
      node: input,
      input,
      trigger,
      panel,
      refresh: sync,
      open: show,
      close,
      isOpen: () => open,
      destroy() {
        if (!instances.has(input)) return;
        close(false);
        if (detachTimer) clearTimeout(detachTimer);
        panel.remove();
        observer.disconnect();
        input.removeEventListener?.('change', sync);
        delete input.value;
        if (input.focus !== focus) delete input.focus;
        input.classList.remove('hui-date-native');
        input.removeAttribute?.('aria-hidden');
        input.tabIndex = tabIndex;
        trigger.remove?.();
        instances.delete(input);
        window.HuiAuto?.untrack(api);
      },
    };
    instances.set(input, api);
    window.HuiAuto?.track(api);
    sync();
    window.AgentFlowHarnessIcons?.hydrate?.(trigger);
    return api;
  }

  if (supported && typeof doc.addEventListener === 'function') {
    doc.addEventListener('click', event => {
      const label = event.target?.closest?.('label');
      const api = label?.control && instances.get(label.control);
      if (!api || api.trigger.contains(event.target) || api.trigger.disabled) return;
      event.preventDefault();
      api.trigger.focus();
    });
  }

  window.HuiDate = Object.freeze({
    enhance,
    destroy(input) { instances.get(input)?.destroy(); },
    format,
    parse,
  });
  window.HuiAuto?.auto('input[type="date"]', enhance);
})();
