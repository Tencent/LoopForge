// Regression: the clarification model picker (and every other classic-SPA
// picker) needs window.AgentFlowAssistantModels. A refactor once deleted the
// only definition, bindClarifyModelPicker() returned early at
// `if(!api||!toggle||!menu)return;`, and the 澄清 tab's 「选择模型」 button
// silently stopped opening. This test boots the shared module plus the real
// picker helpers from the bundle and clicks through a selection.
'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const bundle = fs.readFileSync(process.argv[2], 'utf8');
const moduleSource = fs.readFileSync(process.argv[3], 'utf8');

function matches(node, selector) {
  if (selector.startsWith('.')) return String(node.className || '').split(/\s+/).includes(selector.slice(1));
  const attr = /^\[([^\]=]+)(?:="?([^\]"]*)"?)?\]$/.exec(selector);
  if (attr) {
    const name = attr[1];
    const own = name in node ? node[name] : node.attrs?.[name];
    if (own === undefined) return false;
    return attr[2] === undefined ? true : String(own) === attr[2];
  }
  return String(node.tagName || '').toLowerCase() === selector;
}
function descendants(node, out = []) {
  for (const child of node.children || []) { out.push(child); descendants(child, out); }
  return out;
}
function createElement(tag) {
  const node = {
    tagName: String(tag).toUpperCase(),
    children: [], parentElement: null, attrs: {}, dataset: {},
    className: '', textContent: '', title: '', hidden: false, disabled: false, type: '',
    setAttribute(name, value) { this.attrs[name] = String(value); },
    getAttribute(name) { return this.attrs[name] ?? null; },
    append(...nodes) { for (const item of nodes) { item.parentElement = this; this.children.push(item); } },
    replaceChildren(...nodes) { this.children = []; this.append(...nodes); },
    querySelector(selector) { return descendants(this).find(child => matches(child, selector)) || null; },
    querySelectorAll(selector) { return descendants(this).filter(child => matches(child, selector)); },
  };
  Object.defineProperty(node, 'innerHTML', { get() { return this._html || ''; }, set(value) { this._html = String(value); this.children = []; } });
  return node;
}

// host > button.ai-model-toggle([data-model-toggle], .ai-model-icon, [data-model-label])
//        + input[data-clarify-model-value] + .ai-model-menu
const host = createElement('div');
const toggle = createElement('button');
toggle.className = 'ai-model-toggle';
toggle.setAttribute('data-model-toggle', '');
const icon = createElement('span'); icon.className = 'ai-model-icon';
const label = createElement('span'); label.setAttribute('data-model-label', ''); label.textContent = '选择模型';
const caret = createElement('span'); caret.className = 'ai-model-caret';
toggle.append(icon, label, caret);
const input = createElement('input'); input.setAttribute('data-clarify-model-value', '');
const menu = createElement('div'); menu.className = 'ai-model-menu'; menu.hidden = true;
host.append(toggle, input, menu);

const store = new Map([['agentflow.clarifyModel', 'model-a']]);
const ctx = {
  console, setTimeout, clearTimeout, Promise,
  document: { addEventListener() {}, querySelectorAll: () => [], createElement },
  localStorage: {
    getItem: key => (store.has(key) ? store.get(key) : null),
    setItem: (key, value) => store.set(key, String(value)),
    removeItem: key => store.delete(key),
  },
  fetch: async () => ({
    ok: true,
    json: async () => ({
      default: 'model-b',
      models: [
        { id: 'model-a', name: 'Model A', family: 'claude', credits: 'x1.00' },
        { id: 'model-b', name: 'Model B', family: 'glm', credits: 'x0.06' },
      ],
    }),
  }),
};
ctx.window = ctx;
// bindClarifyModelPicker uses $$ for the outside-click closer and setClarifyModel.
ctx.$$ = selector => (selector === '[data-clarify-model]' ? [host] : [input]);
vm.createContext(ctx);
vm.runInContext(moduleSource, ctx);

(async () => {
  const api = ctx.window.AgentFlowAssistantModels;
  assert.ok(api, 'window.AgentFlowAssistantModels must be defined for classic-SPA pickers');
  for (const name of ['catalog', 'load', 'paint', 'paintItem', 'renderList', 'renderOptions']) {
    assert.equal(typeof api[name], 'function', `AgentFlowAssistantModels.${name}`);
  }
  assert.equal(api.DEFAULT, 'default');
  // Pickers keep painting before the catalog lands, so the built-in fallback
  // must already render a usable label.
  api.paint(toggle, api.DEFAULT);
  assert.equal(label.textContent, 'CLI 默认模型');
  assert.equal(icon.dataset.family, 'other');

  await api.load();
  assert.equal(api.catalog().default, 'model-b');
  assert.equal(api.catalog().models.length, 2, 'catalog adopts /api/assistant/models');

  // Pull the real picker helpers out of the bundle and bind them to the stub host.
  for (const name of ['selectedClarifyModel', 'setClarifyModel', 'bindClarifyModelPicker']) {
    const start = bundle.search(new RegExp('^function ' + name + '\\(', 'm'));
    assert.ok(start >= 0, `${name} is missing from the app bundle`);
    const rest = bundle.slice(start);
    const next = /\n(?:async )?function \w+\(/.exec(rest);
    vm.runInContext(next ? rest.slice(0, next.index) : rest, ctx);
  }
  ctx.bindClarifyModelPicker(host);
  assert.equal(host.dataset.bound, '1', 'clarify picker must bind once the model API exists');

  toggle.onclick({ stopPropagation() {} });
  assert.equal(menu.hidden, false, 'clicking 选择模型 must open the menu');
  assert.equal(menu.children.length, 2, 'menu lists the catalog models');
  assert.ok(menu.children[0].className.includes('is-active'), 'stored model is marked active');

  menu.children[1].onclick();
  assert.equal(store.get('agentflow.clarifyModel'), 'model-b');
  assert.equal(input.value, 'model-b');
  assert.equal(menu.hidden, true);
  assert.equal(label.textContent, 'Model B');
  console.log('assistant models ok');
})().catch(err => { console.error(err); process.exit(1); });
