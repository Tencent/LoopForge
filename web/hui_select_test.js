const assert = require('assert');
const fs = require('fs');
const vm = require('vm');

class FakeEvent {
  constructor(type, init = {}) {
    this.type = type;
    this.bubbles = !!init.bubbles;
    this.key = init.key;
    this.target = init.target || null;
    this.defaultPrevented = false;
    this.propagationStopped = false;
  }
  preventDefault() { this.defaultPrevented = true; }
  stopPropagation() { this.propagationStopped = true; }
}

class FakeNode {
  constructor(tag, doc) {
    this.tagName = String(tag).toUpperCase();
    this.ownerDocument = doc;
    this.parentNode = null;
    this.children = [];
    this.attributes = {};
    this.listeners = {};
    this.style = {};
    this.dataset = {};
    this.hidden = false;
    this.disabled = false;
    this.inert = false;
    this.tabIndex = 0;
    if (this.tagName === 'INPUT') this.value = '';
    this._text = '';
    this._classes = new Set();
    const classes = this._classes;
    this.classList = {
      add: (...names) => names.forEach(n => classes.add(n)),
      remove: (...names) => names.forEach(n => classes.delete(n)),
      contains: n => classes.has(n),
      toggle: (n, force) => {
        const on = force === undefined ? !classes.has(n) : !!force;
        if (on) classes.add(n); else classes.delete(n);
        return on;
      },
    };
  }
  get className() { return [...this._classes].join(' '); }
  set className(v) { this._classes.clear(); String(v).split(/\s+/).filter(Boolean).forEach(n => this._classes.add(n)); }
  get textContent() { return this._text + this.children.map(c => c.textContent).join(''); }
  set textContent(v) { this.children.forEach(c => { c.parentNode = null; }); this.children = []; this._text = String(v); }
  setAttribute(name, value) {
    if (name === 'class') this.className = value;
    else this.attributes[name] = String(value);
  }
  getAttribute(name) {
    if (name === 'class') return this.className;
    return Object.hasOwn(this.attributes, name) ? this.attributes[name] : null;
  }
  appendChild(child) {
    if (child.parentNode) child.parentNode.children.splice(child.parentNode.children.indexOf(child), 1);
    child.parentNode = this;
    this.children.push(child);
    return child;
  }
  replaceChildren(...nodes) {
    this.children.forEach(c => { c.parentNode = null; });
    this.children = [];
    this._text = '';
    nodes.forEach(n => this.appendChild(n));
  }
  after(node) {
    const parent = this.parentNode;
    if (node.parentNode) node.parentNode.children.splice(node.parentNode.children.indexOf(node), 1);
    parent.children.splice(parent.children.indexOf(this) + 1, 0, node);
    node.parentNode = parent;
  }
  contains(node) {
    for (let n = node; n; n = n.parentNode) if (n === this) return true;
    return false;
  }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  removeEventListener(type, fn) { this.listeners[type] = (this.listeners[type] || []).filter(f => f !== fn); }
  remove() { if (this.parentNode) this.parentNode.children.splice(this.parentNode.children.indexOf(this), 1); this.parentNode = null; }
  dispatchEvent(event) {
    if (!event.target) event.target = this;
    for (let n = this; n; n = event.bubbles ? n.parentNode : null) {
      for (const fn of n.listeners[event.type] || []) fn.call(n, event);
      if (event.propagationStopped) break;
    }
    return !event.defaultPrevented;
  }
  focus() { this.ownerDocument.activeElement = this; }
  click() { this.dispatchEvent(new FakeEvent('click', { bubbles: true })); }
  getBoundingClientRect() { return { left: 100, top: 100, right: 232, bottom: 134, width: 132, height: 34 }; }
  get scrollHeight() { return 200; }
}

class FakeSelect extends FakeNode {
  constructor(doc) {
    super('select', doc);
    this.options = [];
    this._index = -1;
  }
  setOptions(list, value = '') {
    this.options = list.map(([v, text, meta, search]) => ({ value: v, text, disabled: false, getAttribute: n => (n === 'data-meta' ? meta || null : n === 'data-search' ? search || null : null) }));
    this._index = this.options.findIndex(o => o.value === value);
    observers.filter(o => o.target === this).forEach(o => o.callback([]));
  }
}
Object.defineProperty(FakeSelect.prototype, 'value', {
  configurable: true,
  get() { return this.options[this._index]?.value ?? ''; },
  set(v) { this._index = this.options.findIndex(o => o.value === String(v)); },
});
Object.defineProperty(FakeSelect.prototype, 'selectedIndex', {
  configurable: true,
  get() { return this._index; },
  set(i) { this._index = i >= 0 && i < this.options.length ? i : -1; },
});

const observers = [];
class FakeMutationObserver {
  constructor(callback) { this.callback = callback; }
  observe(target) { this.target = target; observers.push(this); }
  disconnect() {}
}

const document = new FakeNode('#document', null);
document.ownerDocument = document;
document.createElement = tag => (tag === 'select' ? new FakeSelect(document) : new FakeNode(tag, document));
document.createElementNS = (_ns, tag) => new FakeNode(tag, document);
document.documentElement = new FakeNode('html', document);
document.body = new FakeNode('body', document);
document.appendChild(document.documentElement);
document.documentElement.appendChild(document.body);
document.activeElement = document.body;

const windowListeners = {};
const ctx = {
  document,
  MutationObserver: FakeMutationObserver,
  Event: FakeEvent,
  innerWidth: 1280,
  innerHeight: 800,
  addEventListener: (type, fn) => { (windowListeners[type] ||= []).push(fn); },
  removeEventListener: (type, fn) => { windowListeners[type] = (windowListeners[type] || []).filter(f => f !== fn); },
};
ctx.window = ctx;
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(process.argv[2], 'utf8'), ctx);
assert.strictEqual(typeof ctx.enhanceSelect, 'function');
assert.strictEqual(typeof ctx.HuiSelect.claim, 'function');

const toolbar = document.body.appendChild(new FakeNode('div', document));
function makeSelect(options, value = '') {
  const select = document.createElement('select');
  toolbar.appendChild(select);
  select.setOptions(options, value);
  return select;
}
const find = (node, cls) => (node.classList.contains(cls) ? node : node.children.map(c => find(c, cls)).find(Boolean) || null);
const optionButtons = api => find(api.panel, 'hui-select-options').children;
const triggerText = api => find(api.trigger, 'hui-select-value').textContent;

const status = makeSelect([['', '全部状态'], ['backlog', '待规划'], ['done', '已完成']]);
let changes = 0;
status.addEventListener('change', () => changes++);
const api = ctx.enhanceSelect(status);
assert.strictEqual(ctx.enhanceSelect(status), api, 'enhancing twice reuses the first instance');
assert.strictEqual(toolbar.children.filter(c => c.classList.contains('hui-select-trigger')).length, 1);
assert.strictEqual(toolbar.children[toolbar.children.indexOf(status) + 1], api.trigger, 'trigger sits right after the native select');
assert.ok(status.classList.contains('hui-select-native'));
assert.strictEqual(status.getAttribute('aria-hidden'), 'true');
assert.strictEqual(status.tabIndex, -1);
assert.strictEqual(api.trigger.type, 'button');
assert.strictEqual(api.trigger.getAttribute('aria-haspopup'), 'listbox');
assert.strictEqual(api.trigger.getAttribute('aria-controls'), api.panel.id);
assert.strictEqual(api.panel.getAttribute('role'), 'listbox');
assert.strictEqual(api.panel.parentNode, document.body, 'panel floats on body so the table card cannot clip it');
assert.strictEqual(triggerText(api), '全部状态');
assert.ok(!api.trigger.classList.contains('is-filtered'));
assert.ok(api.panel.inert);
assert.ok(!find(api.trigger, 'hui-select-lead'), 'selects without data-icon get no leading icon');
assert.deepStrictEqual(api.trigger.children.map(c => c.getAttribute('class')), ['hui-select-value', 'agent-icon harness-icon hui-select-chevron']);

api.trigger.click();
assert.ok(api.isOpen());
assert.ok(api.panel.classList.contains('is-open'));
assert.ok(!api.panel.inert);
assert.strictEqual(api.trigger.getAttribute('aria-expanded'), 'true');
assert.strictEqual(api.panel.style.width, '240px');
assert.strictEqual(api.panel.dataset.placement, 'bottom');
assert.ok(find(api.panel, 'hui-select-search').hidden, 'short option lists have no search box');
let buttons = optionButtons(api);
assert.deepStrictEqual(buttons.map(b => b.textContent), ['全部状态', '待规划', '已完成']);
assert.deepStrictEqual(buttons.map(b => b.getAttribute('role')), ['option', 'option', 'option']);
assert.strictEqual(buttons[0].getAttribute('aria-selected'), 'true');
assert.strictEqual(document.activeElement, buttons[0], 'opening focuses the selected option');

api.panel.dispatchEvent(new FakeEvent('keydown', { key: 'ArrowDown', bubbles: true }));
assert.strictEqual(document.activeElement, buttons[1]);
api.panel.dispatchEvent(new FakeEvent('keydown', { key: 'End', bubbles: true }));
assert.strictEqual(document.activeElement, buttons[2]);
api.panel.dispatchEvent(new FakeEvent('keydown', { key: 'ArrowDown', bubbles: true }));
assert.strictEqual(document.activeElement, buttons[0], 'arrow keys wrap around');

buttons[2].click();
assert.strictEqual(status.value, 'done');
assert.strictEqual(changes, 1, 'choosing an option fires one change event');
assert.strictEqual(triggerText(api), '已完成');
assert.ok(api.trigger.classList.contains('is-filtered'));
assert.ok(!api.isOpen());
assert.ok(api.panel.inert);
assert.strictEqual(api.trigger.getAttribute('aria-expanded'), 'false');
assert.strictEqual(document.activeElement, api.trigger, 'choosing returns focus to the trigger');

api.trigger.click();
buttons = optionButtons(api);
assert.strictEqual(buttons[2].getAttribute('aria-selected'), 'true');
assert.ok(buttons[2].classList.contains('is-selected'));
assert.ok(find(buttons[2], 'hui-select-check'), 'selected option shows a check');
buttons[2].click();
assert.strictEqual(changes, 1, 're-choosing the current value does not fire change');

status.value = '';
assert.strictEqual(triggerText(api), '全部状态', 'programmatic value writes resync the trigger');
assert.ok(!api.trigger.classList.contains('is-filtered'));
status.selectedIndex = 1;
assert.strictEqual(triggerText(api), '待规划');
assert.ok(api.trigger.classList.contains('is-filtered'));
assert.strictEqual(changes, 1, 'programmatic writes do not fire change');

api.trigger.dispatchEvent(new FakeEvent('keydown', { key: 'ArrowDown', bubbles: true }));
assert.ok(api.isOpen(), 'ArrowDown on the trigger opens the panel');
api.panel.dispatchEvent(new FakeEvent('keydown', { key: 'Escape', bubbles: true }));
assert.ok(!api.isOpen());
assert.strictEqual(document.activeElement, api.trigger, 'Escape returns focus to the trigger');

const people = [['', '全部责任人'], ['__unassigned', '未指定', '3 条'], ['u1', 'example-maintainer(示例用户)', '4 条'], ['u2', 'example-owner(示例甲)', '2 条'], ['u3', 'example-dev(示例乙)', '1 条'], ['u4', 'river-lead(示例丁)', '5 条']];
const assignee = makeSelect([['', '全部责任人']]);
const assigneeApi = ctx.enhanceSelect(assignee);
assignee.setOptions(people, 'u2');
assert.strictEqual(triggerText(assigneeApi), 'example-owner(示例甲)', 'rewritten options resync through the MutationObserver');
assert.ok(assigneeApi.trigger.classList.contains('is-filtered'));

api.trigger.click();
assigneeApi.trigger.click();
assert.ok(!api.isOpen(), 'opening one selector closes the other');
assert.ok(assigneeApi.isOpen());
const searchBar = find(assigneeApi.panel, 'hui-select-search');
assert.ok(!searchBar.hidden, 'long option lists show a search box');
const search = searchBar.children.find(c => c.tagName === 'INPUT');
assert.strictEqual(document.activeElement, search);
buttons = optionButtons(assigneeApi);
assert.strictEqual(find(buttons[2], 'hui-select-option-meta').textContent, '4 条', 'data-meta is shown on the right');
search.value = 'example-main';
search.dispatchEvent(new FakeEvent('input'));
assert.deepStrictEqual(buttons.filter(b => !b.hidden).map(b => find(b, 'hui-select-option-label').textContent), ['example-maintainer(示例用户)']);
search.value = 'nobody';
search.dispatchEvent(new FakeEvent('input'));
assert.ok(!find(assigneeApi.panel, 'hui-select-empty').hidden);
search.value = 'river';
search.dispatchEvent(new FakeEvent('input'));
let assigneeChanges = 0;
assignee.addEventListener('change', () => assigneeChanges++);
assigneeApi.panel.dispatchEvent(new FakeEvent('keydown', { key: 'Enter', bubbles: true }));
assert.strictEqual(assignee.value, 'u4', 'Enter in the search box picks the first match');
assert.strictEqual(assigneeChanges, 1);

assigneeApi.trigger.click();
document.dispatchEvent(new FakeEvent('pointerdown', { target: toolbar }));
assert.ok(!assigneeApi.isOpen(), 'pointerdown outside closes the panel');
assert.notStrictEqual(document.activeElement, assigneeApi.trigger, 'outside clicks do not steal focus');

assigneeApi.trigger.click();
windowListeners.resize.forEach(fn => fn());
assert.ok(!assigneeApi.isOpen(), 'resizing closes the panel');

let externalClosed = 0;
const external = () => { externalClosed++; };
ctx.HuiSelect.claim(external);
api.trigger.click();
assert.strictEqual(externalClosed, 1, 'opening a selector closes the heading menu');
ctx.HuiSelect.closeAll();
assert.ok(!api.isOpen());

const owner = makeSelect([['', '全部责任人'], ['1', 'example-owner(示例甲)', '我', 'example-owner 示例甲'], ['2', '王五', '', 'wangwu 王五'], ['3', '李四', '', 'lisi 李四'], ['4', '赵六', '', 'zhaoliu 赵六'], ['5', '示例乙', '', 'example-dev 示例乙']]);
owner.setAttribute('data-icon', 'user');
const ownerApi = ctx.enhanceSelect(owner);
const lead = ownerApi.trigger.children[0];
assert.ok(lead.classList.contains('hui-select-lead'), 'data-icon renders a leading icon first in the trigger');
assert.strictEqual(lead.getAttribute('data-icon'), 'user');
assert.strictEqual(lead.getAttribute('aria-hidden'), 'true');
assert.strictEqual(triggerText(ownerApi), '全部责任人');
ownerApi.trigger.click();
const ownerSearch = find(ownerApi.panel, 'hui-select-search').children.find(c => c.tagName === 'INPUT');
ownerSearch.value = 'zhaoliu';
ownerSearch.dispatchEvent(new FakeEvent('input'));
buttons = optionButtons(ownerApi);
assert.deepStrictEqual(buttons.filter(b => !b.hidden).map(b => find(b, 'hui-select-option-label').textContent), ['赵六'], 'search also matches option data-search');
ownerSearch.value = '示例甲';
ownerSearch.dispatchEvent(new FakeEvent('input'));
assert.deepStrictEqual(buttons.filter(b => !b.hidden).map(b => find(b, 'hui-select-option-label').textContent), ['example-owner(示例甲)']);

const listeners = () => [(document.listeners.pointerdown || []).length, (windowListeners.resize || []).length, (windowListeners.scroll || []).length];
const [pointerBefore, resizeBefore, scrollBefore] = listeners();
ownerApi.dispose();
assert.ok(!ownerApi.isOpen(), 'dispose closes an open panel');
assert.strictEqual(ownerApi.panel.parentNode, null, 'dispose removes the floating panel');
assert.deepStrictEqual(listeners(), [pointerBefore, resizeBefore, scrollBefore], 'selectors share the document and window listeners, so dispose leaves none behind');
assert.notStrictEqual(ctx.enhanceSelect(owner), ownerApi, 'a disposed select can be enhanced again');
assert.deepStrictEqual(listeners(), [pointerBefore, resizeBefore, scrollBefore], 're-enhancing adds no per-instance listeners');

console.log('hui select ok');
