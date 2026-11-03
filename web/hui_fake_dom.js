// Small DOM stand-in for the Harness control tests (hui_*_test.js). It covers
// what the hui-* scripts touch: element tree + attributes, a simple selector
// engine (compound selectors, descendant combinator, comma lists, :not,
// :nth-of-type),
// capture/bubble events, microtask MutationObserver records, label/control
// association, a minimal innerHTML parser and manually advanced timers.
'use strict';
const fs = require('fs');
const vm = require('vm');

const VOID = new Set(['area', 'br', 'col', 'embed', 'hr', 'img', 'input', 'link', 'meta', 'source', 'wbr']);
const REFLECT = { id: 'id', type: 'type', name: 'name', min: 'min', max: 'max', accept: 'accept', placeholder: 'placeholder', role: 'role', htmlFor: 'for' };
const BOOLEAN = { hidden: 'hidden', disabled: 'disabled', multiple: 'multiple', required: 'required', readOnly: 'readonly', webkitdirectory: 'webkitdirectory' };

function createDOM() {
  const observers = [];
  let pending = [];
  let flushQueued = false;
  function queue(record) {
    for (const obs of observers) {
      const o = obs.options;
      for (let n = record.target; n; n = n.parentNode) {
        if (obs.targets.has(n) && (n === record.target || o.subtree)) {
          if (record.type === 'childList' && !o.childList) break;
          if (record.type === 'attributes' && (!o.attributes || (o.attributeFilter && !o.attributeFilter.includes(record.attributeName)))) break;
          obs.records.push(record);
          if (!pending.includes(obs)) pending.push(obs);
          break;
        }
      }
    }
    if (pending.length && !flushQueued) {
      flushQueued = true;
      queueMicrotask(() => {
        flushQueued = false;
        const list = pending;
        pending = [];
        list.forEach(obs => { const records = obs.records; obs.records = []; if (records.length && obs.active) obs.callback(records, obs); });
      });
    }
  }

  class Event {
    constructor(type, init = {}) {
      Object.assign(this, init);
      this.type = type;
      this.bubbles = !!init.bubbles;
      this.target = null;
      this.currentTarget = null;
      this.defaultPrevented = false;
      this._stop = false;
    }
    preventDefault() { this.defaultPrevented = true; }
    stopPropagation() { this._stop = true; }
    stopImmediatePropagation() { this._stop = true; }
  }

  class Node {
    constructor(doc) {
      this.ownerDocument = doc;
      this.parentNode = null;
      this.childNodes = [];
      this.listeners = [];
    }
    get parentElement() { return this.parentNode && this.parentNode.nodeType === 1 ? this.parentNode : null; }
    get isConnected() {
      let n = this;
      while (n.parentNode) n = n.parentNode;
      return n === this.ownerDocument;
    }
    get firstChild() { return this.childNodes[0] || null; }
    get children() { return this.childNodes.filter(n => n.nodeType === 1); }
    get firstElementChild() { return this.children[0] || null; }
    get textContent() { return this.childNodes.map(n => n.textContent).join(''); }
    set textContent(v) { this.replaceChildren(); if (String(v ?? '') !== '') this.appendChild(this.ownerDocument.createTextNode(String(v))); }
    _detach(node) {
      const parent = node.parentNode;
      if (!parent) return;
      parent.childNodes.splice(parent.childNodes.indexOf(node), 1);
      node.parentNode = null;
      queue({ type: 'childList', target: parent, addedNodes: [], removedNodes: [node] });
    }
    insertBefore(node, ref) {
      if (node.nodeType === 11) { [...node.childNodes].forEach(c => this.insertBefore(c, ref)); return node; }
      this._detach(node);
      const at = ref ? this.childNodes.indexOf(ref) : -1;
      if (at < 0) this.childNodes.push(node); else this.childNodes.splice(at, 0, node);
      node.parentNode = this;
      queue({ type: 'childList', target: this, addedNodes: [node], removedNodes: [] });
      return node;
    }
    appendChild(node) { return this.insertBefore(node, null); }
    append(...nodes) { nodes.forEach(n => this.appendChild(typeof n === 'string' ? this.ownerDocument.createTextNode(n) : n)); }
    removeChild(node) { this._detach(node); return node; }
    remove() { this._detach(this); }
    after(node) {
      const parent = this.parentNode;
      const next = parent.childNodes[parent.childNodes.indexOf(this) + 1] || null;
      parent.insertBefore(node, next);
    }
    replaceChildren(...nodes) {
      [...this.childNodes].forEach(c => this._detach(c));
      nodes.forEach(n => this.appendChild(n));
    }
    contains(node) {
      for (let n = node; n; n = n.parentNode) if (n === this) return true;
      return false;
    }
    addEventListener(type, fn, opts) {
      if (!fn) return;
      const capture = opts === true || !!(opts && opts.capture);
      if (this.listeners.some(l => l.type === type && l.fn === fn && l.capture === capture)) return;
      this.listeners.push({ type, fn, capture, once: !!(opts && opts.once) });
    }
    removeEventListener(type, fn, opts) {
      const capture = opts === true || !!(opts && opts.capture);
      this.listeners = this.listeners.filter(l => !(l.type === type && l.fn === fn && l.capture === capture));
    }
    _fire(event, phase) {
      for (const l of [...this.listeners]) {
        if (l.type !== event.type) continue;
        if (phase === 'capture' && !l.capture) continue;
        if (phase === 'bubble' && l.capture) continue;
        if (l.once) this.removeEventListener(l.type, l.fn, l.capture);
        event.currentTarget = this;
        l.fn.call(this, event);
      }
    }
    dispatchEvent(event) {
      event.target = this;
      const path = [];
      for (let n = this.parentNode; n; n = n.parentNode) path.push(n);
      if (this.isConnected && this.ownerDocument.defaultView) path.push(this.ownerDocument.defaultView);
      for (let i = path.length - 1; i >= 0 && !event._stop; i--) path[i]._fire(event, 'capture');
      if (!event._stop) this._fire(event, 'target');
      for (let i = 0; i < path.length && event.bubbles && !event._stop; i++) path[i]._fire(event, 'bubble');
      if (!event.defaultPrevented && event.type === 'click') this._activate?.(event);
      return !event.defaultPrevented;
    }
  }

  class Text extends Node {
    constructor(doc, text) { super(doc); this.nodeType = 3; this.data = String(text); }
    get textContent() { return this.data; }
    set textContent(v) { this.data = String(v); }
  }
  class Fragment extends Node {
    constructor(doc) { super(doc); this.nodeType = 11; }
  }

  class Element extends Node {
    constructor(doc, tag) {
      super(doc);
      this.nodeType = 1;
      this.tagName = String(tag).toUpperCase();
      this.localName = String(tag).toLowerCase();
      this.attrs = new Map();
      this.style = {};
      this.dataset = new Proxy({}, {
        get: (_t, k) => this.getAttribute('data-' + kebab(k)) ?? undefined,
        set: (_t, k, v) => { this.setAttribute('data-' + kebab(k), v); return true; },
        deleteProperty: (_t, k) => { this.removeAttribute('data-' + kebab(k)); return true; },
        has: (_t, k) => this.hasAttribute('data-' + kebab(k)),
      });
      this.inert = false;
      this.tabIndex = ['BUTTON', 'INPUT', 'SELECT', 'TEXTAREA', 'A'].includes(this.tagName) ? 0 : -1;
      this.offsetWidth = 120;
      this.offsetHeight = 28;
      this.rect = { left: 100, top: 100, width: 132, height: 34 };
      const el = this;
      this.classList = {
        add: (...names) => el._setClasses([...new Set([...el._classes(), ...names])]),
        remove: (...names) => el._setClasses(el._classes().filter(c => !names.includes(c))),
        contains: name => el._classes().includes(name),
        toggle: (name, force) => {
          const on = force === undefined ? !el._classes().includes(name) : !!force;
          if (on) el.classList.add(name); else el.classList.remove(name);
          return on;
        },
      };
    }
    _classes() { return String(this.getAttribute('class') || '').split(/\s+/).filter(Boolean); }
    _setClasses(list) { this.setAttribute('class', list.join(' ')); }
    get className() { return this.getAttribute('class') || ''; }
    set className(v) { this.setAttribute('class', v); }
    setAttribute(name, value) {
      name = String(name).toLowerCase();
      const old = this.attrs.has(name) ? this.attrs.get(name) : null;
      this.attrs.set(name, String(value));
      queue({ type: 'attributes', target: this, attributeName: name, oldValue: old });
    }
    getAttribute(name) {
      name = String(name).toLowerCase();
      return this.attrs.has(name) ? this.attrs.get(name) : null;
    }
    hasAttribute(name) { return this.attrs.has(String(name).toLowerCase()); }
    removeAttribute(name) {
      name = String(name).toLowerCase();
      if (!this.attrs.has(name)) return;
      const old = this.attrs.get(name);
      this.attrs.delete(name);
      queue({ type: 'attributes', target: this, attributeName: name, oldValue: old });
    }
    matches(selector) { return matchList(this, parseList(selector)); }
    closest(selector) {
      const list = parseList(selector);
      for (let n = this; n && n.nodeType === 1; n = n.parentNode) if (matchList(n, list)) return n;
      return null;
    }
    querySelectorAll(selector) {
      const list = parseList(selector);
      const out = [];
      const walk = node => node.children.forEach(c => { if (matchList(c, list)) out.push(c); walk(c); });
      walk(this);
      return out;
    }
    querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
    getElementsByTagName(tag) { return this.querySelectorAll(tag); }
    get innerHTML() { return ''; }
    set innerHTML(html) { this.replaceChildren(...parseHTML(this.ownerDocument, html)); }
    focus() {
      const doc = this.ownerDocument;
      if (this.disabled || !this.isConnected || doc.activeElement === this) return;
      const prev = doc.activeElement;
      doc.activeElement = this;
      if (prev && prev !== doc.body) prev.dispatchEvent(new Event('focusout', { bubbles: true, relatedTarget: this }));
      this.dispatchEvent(new Event('focusin', { bubbles: true, relatedTarget: prev }));
    }
    blur() { if (this.ownerDocument.activeElement === this) this.ownerDocument.body.focus(); }
    click() { if (!this.disabled) this.dispatchEvent(new Event('click', { bubbles: true, button: 0 })); }
    select() {}
    scrollIntoView() {}
    getBoundingClientRect() {
      const r = this.rect;
      return { left: r.left, top: r.top, width: r.width, height: r.height, right: r.left + r.width, bottom: r.top + r.height };
    }
    getClientRects() { return this.isConnected && !this.hidden ? [this.getBoundingClientRect()] : []; }
    get scrollHeight() { return 200; }
    get labels() {
      const doc = this.ownerDocument;
      const out = [];
      const wrap = this.parentNode?.closest?.('label');
      if (wrap) out.push(wrap);
      if (this.id) doc.querySelectorAll('label').forEach(l => { if (l.getAttribute('for') === this.id && !out.includes(l)) out.push(l); });
      return out;
    }
    get control() {
      if (this.tagName !== 'LABEL') return undefined;
      const id = this.getAttribute('for');
      if (id) return this.ownerDocument.getElementById(id);
      return this.querySelector('input, select, textarea, button');
    }
    get form() { return this.closest('form'); }
    _activate() {
      if (this.tagName === 'LABEL') { const c = this.control; if (c && !c.contains(this)) c.focus(); }
      if (this.tagName === 'BUTTON' && this.type === 'reset') this.form?.reset();
    }
  }
  for (const [prop, attr] of Object.entries(REFLECT)) {
    Object.defineProperty(Element.prototype, prop, {
      configurable: true,
      get() { return this.getAttribute(attr) ?? ''; },
      set(v) { this.setAttribute(attr, v); },
    });
  }
  for (const [prop, attr] of Object.entries(BOOLEAN)) {
    Object.defineProperty(Element.prototype, prop, {
      configurable: true,
      get() { return this.hasAttribute(attr); },
      set(v) { if (v) this.setAttribute(attr, ''); else this.removeAttribute(attr); },
    });
  }

  class Input extends Element {
    constructor(doc, tag) { super(doc, tag); this._value = ''; this.files = []; }
  }
  Object.defineProperty(Input.prototype, 'value', {
    configurable: true,
    get() { return this._value; },
    set(v) { this._value = v == null ? '' : String(v); },
  });

  class Option extends Element {
    get text() { return this.textContent; }
    get value() { return this.hasAttribute('value') ? this.getAttribute('value') : this.textContent; }
    set value(v) { this.setAttribute('value', v); }
    get label() { return this.getAttribute('label') || this.text; }
  }
  class OptGroup extends Element {
    get label() { return this.getAttribute('label') || ''; }
  }
  class Select extends Element {
    constructor(doc, tag) { super(doc, tag); this._index = null; }
    get options() { return this.querySelectorAll('option'); }
    get size() { return Number(this.getAttribute('size') || 0); }
  }
  Object.defineProperty(Select.prototype, 'selectedIndex', {
    configurable: true,
    get() {
      const options = this.options;
      if (this._index != null && this._index < options.length) return this._index;
      const marked = options.findIndex(o => o.hasAttribute('selected'));
      return marked >= 0 ? marked : options.length ? 0 : -1;
    },
    set(i) { this._index = i >= 0 && i < this.options.length ? i : -1; },
  });
  Object.defineProperty(Select.prototype, 'value', {
    configurable: true,
    get() { return this.options[this.selectedIndex]?.value ?? ''; },
    set(v) { this._index = this.options.findIndex(o => o.value === String(v)); },
  });

  class Form extends Element {
    reset() {
      this.dispatchEvent(new Event('reset', { bubbles: true }));
      this.querySelectorAll('input, textarea').forEach(i => { i._value = ''; i.files = []; });
    }
  }

  const CLASSES = { input: Input, textarea: Input, select: Select, option: Option, optgroup: OptGroup, form: Form };

  class Document extends Node {
    constructor() {
      super(null);
      this.ownerDocument = this;
      this.nodeType = 9;
      this.readyState = 'complete';
      this.documentElement = this.createElement('html');
      this.head = this.createElement('head');
      this.body = this.createElement('body');
      this.appendChild(this.documentElement);
      this.documentElement.appendChild(this.head);
      this.documentElement.appendChild(this.body);
      this.activeElement = this.body;
    }
    get isConnected() { return true; }
    get children() { return [this.documentElement]; }
    createElement(tag) { const C = CLASSES[String(tag).toLowerCase()] || Element; return new C(this, tag); }
    createElementNS(_ns, tag) { return new Element(this, tag); }
    createTextNode(text) { return new Text(this, text); }
    createDocumentFragment() { return new Fragment(this); }
    getElementById(id) { return this.querySelector('#' + id); }
    querySelectorAll(selector) { return Element.prototype.querySelectorAll.call(this, selector); }
    querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  }

  class MutationObserver {
    constructor(callback) { this.callback = callback; this.targets = new Set(); this.records = []; this.options = {}; this.active = false; observers.push(this); }
    observe(target, options = {}) {
      this.targets.add(target);
      this.options = { ...options, attributes: options.attributes || !!options.attributeFilter };
      this.active = true;
    }
    disconnect() { this.targets.clear(); this.records = []; this.active = false; }
    takeRecords() { const r = this.records; this.records = []; return r; }
  }

  // ---- selectors ----
  function kebab(key) { return String(key).replace(/[A-Z]/g, c => '-' + c.toLowerCase()); }
  function splitTop(text, sep) {
    const out = [];
    let depth = 0, quote = '', start = 0;
    for (let i = 0; i < text.length; i++) {
      const c = text[i];
      if (quote) { if (c === quote) quote = ''; continue; }
      if (c === '"' || c === "'") quote = c;
      else if (c === '(' || c === '[') depth++;
      else if (c === ')' || c === ']') depth--;
      else if (depth === 0 && sep.test(c)) { out.push(text.slice(start, i)); start = i + 1; }
    }
    out.push(text.slice(start));
    return out.map(s => s.trim()).filter(Boolean);
  }
  const cache = new Map();
  function parseList(selector) {
    if (cache.has(selector)) return cache.get(selector);
    const list = splitTop(String(selector), /,/).map(part => splitTop(part.replace(/\s*>\s*/g, ' > '), /\s/).reduce((acc, token) => {
      if (token === '>') acc[acc.length - 1].child = true;
      else acc.push({ compound: parseCompound(token), child: false });
      return acc;
    }, []));
    cache.set(selector, list);
    return list;
  }
  function parseCompound(token) {
    const parts = [];
    const re = /(\*)|([a-zA-Z][\w-]*)|#([\w-]+)|\.([\w-]+)|\[([\w-]+)(?:([~^$*|]?=)\s*(?:"([^"]*)"|'([^']*)'|([^\]\s]+)))?\]|:not\(((?:[^()]|\([^()]*\))*)\)|:([\w-]+)(?:\((\d+)\))?/y;
    let at = 0;
    while (at < token.length) {
      re.lastIndex = at;
      const m = re.exec(token);
      if (!m) throw new SyntaxError('unsupported selector: ' + token);
      at = re.lastIndex;
      if (m[1]) continue;
      if (m[2]) parts.push(el => el.localName === m[2].toLowerCase());
      else if (m[3]) parts.push(el => el.getAttribute('id') === m[3]);
      else if (m[4]) parts.push(el => el._classes().includes(m[4]));
      else if (m[5]) {
        const name = m[5].toLowerCase(), op = m[6], val = m[7] ?? m[8] ?? m[9];
        parts.push(el => {
          const v = el.getAttribute(name);
          if (v == null) return false;
          if (!op) return true;
          if (op === '=') return v === val;
          if (op === '^=') return v.startsWith(val);
          if (op === '*=') return v.includes(val);
          if (op === '~=') return v.split(/\s+/).includes(val);
          return false;
        });
      } else if (m[10] != null) {
        const inner = parseList(m[10]);
        parts.push(el => !matchList(el, inner));
      } else if (m[11]) {
        const pseudo = m[11];
        if (pseudo === 'disabled') parts.push(el => !!el.disabled);
        else if (pseudo === 'checked') parts.push(el => !!el.checked);
        else if (pseudo === 'focus-visible') parts.push(el => el.ownerDocument.activeElement === el && el.ownerDocument.focusVisible !== false);
        else if (pseudo === 'nth-of-type' && m[12]) {
          const n = Number(m[12]);
          parts.push(el => !!el.parentNode && el.parentNode.children.filter(c => c.localName === el.localName).indexOf(el) + 1 === n);
        }
        else throw new SyntaxError('unsupported pseudo: ' + pseudo);
      }
    }
    return el => parts.every(p => p(el));
  }
  function matchChain(el, chain, i) {
    if (!chain[i].compound(el)) return false;
    if (i === 0) return true;
    const prev = chain[i - 1];
    if (prev.child) return !!el.parentElement && matchChain(el.parentElement, chain, i - 1);
    for (let p = el.parentElement; p; p = p.parentElement) if (matchChain(p, chain, i - 1)) return true;
    return false;
  }
  function matchList(el, list) {
    return el.nodeType === 1 && list.some(chain => matchChain(el, chain, chain.length - 1));
  }

  // ---- HTML ----
  function decode(text) {
    return text.replace(/&(amp|lt|gt|quot|#39);/g, (_m, e) => ({ amp: '&', lt: '<', gt: '>', quot: '"', '#39': "'" }[e]));
  }
  function parseHTML(doc, html) {
    const root = doc.createElement('template-root');
    const stack = [root];
    const re = /<!--[\s\S]*?-->|<\/([\w-]+)\s*>|<([\w-]+)((?:\s+[^\s=>/]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+))?)*)\s*(\/?)>|([^<]+)/g;
    let m;
    while ((m = re.exec(String(html)))) {
      const top = stack[stack.length - 1];
      if (m[1]) {
        const tag = m[1].toLowerCase();
        for (let i = stack.length - 1; i > 0; i--) if (stack[i].localName === tag) { stack.length = i; break; }
      } else if (m[2]) {
        const node = doc.createElement(m[2]);
        const attrRe = /([^\s=>/]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?/g;
        let a;
        while ((a = attrRe.exec(m[3] || ''))) node.setAttribute(a[1], decode(a[2] ?? a[3] ?? a[4] ?? ''));
        if (node.hasAttribute('value') && node instanceof Input) node._value = node.getAttribute('value');
        top.appendChild(node);
        if (!VOID.has(node.localName) && !m[4]) stack.push(node);
      } else if (m[5] && m[5].trim()) {
        top.appendChild(doc.createTextNode(decode(m[5])));
      }
    }
    const nodes = [...root.childNodes];
    nodes.forEach(n => root.removeChild(n));
    return nodes;
  }

  // ---- timers ----
  let now = 0;
  let timers = [];
  let timerId = 0;
  function setTimeout(fn, ms = 0) { const id = ++timerId; timers.push({ id, at: now + Number(ms || 0), fn }); return id; }
  function clearTimeout(id) { timers = timers.filter(t => t.id !== id); }
  function advance(ms) {
    const end = now + ms;
    for (;;) {
      timers.sort((a, b) => a.at - b.at || a.id - b.id);
      const next = timers[0];
      if (!next || next.at > end) break;
      timers.shift();
      now = next.at;
      next.fn();
    }
    now = end;
  }

  class DataTransfer {
    constructor() { const list = []; this.files = list; this.items = { add: file => list.push(file) }; }
  }

  const document = new Document();
  const windowObj = new Node(document);
  document.defaultView = windowObj;
  Object.assign(windowObj, {
    document,
    Event,
    CustomEvent: Event,
    MutationObserver,
    DataTransfer,
    setTimeout,
    clearTimeout,
    innerWidth: 1280,
    innerHeight: 800,
    getComputedStyle: el => ({ display: el.style.display || '' }),
    console,
    Promise,
    queueMicrotask,
  });
  windowObj.window = windowObj;
  windowObj.self = windowObj;
  const ctx = vm.createContext(windowObj);

  return {
    window: ctx,
    document,
    Event,
    advance,
    load(file) { vm.runInContext(fs.readFileSync(file, 'utf8'), ctx, { filename: file }); },
    run(code, filename) { return vm.runInContext(code, ctx, { filename }); },
    html(markup, parent = document.body) {
      const nodes = parseHTML(document, markup);
      nodes.forEach(n => parent.appendChild(n));
      return nodes.length === 1 ? nodes[0] : nodes;
    },
    event(type, init) { return new Event(type, init); },
    // Lets queued MutationObserver records and promise callbacks run.
    settle: () => new Promise(resolve => setImmediate(resolve)),
  };
}

module.exports = { createDOM };
