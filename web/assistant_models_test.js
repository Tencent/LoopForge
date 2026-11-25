// Shared catalog rendering and the linked executor/model selectors.
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

const toggle = createElement('button');
const icon = createElement('span'); icon.className = 'ai-model-icon';
const label = createElement('span'); label.setAttribute('data-model-label', '');
toggle.append(icon, label);

function pickerFixture() {
  const field = () => ({
    options: [], value: '', disabled: true,
    replaceChildren(...options) { this.options = options; },
    append(option) { this.options.push(option); },
    prepend(option) { this.options.unshift(option); },
  });
  const executor = field(), model = field();
  const host = {
    isConnected: true,
    querySelector(selector) { return selector === '[data-feature-executor]' ? executor : model; },
  };
  return {host, executor, model};
}

const ctx = {
  console, setTimeout, clearTimeout, Promise,
  document: { addEventListener() {}, querySelectorAll: () => [], createElement },
  Option: class Option { constructor(text, value) { this.text = text; this.value = value; } },
  fetch: async () => ({
    ok: true,
    json: async () => ({
      default: 'model-b',
      models: [
        { id: 'default', name: 'CLI 默认模型', family: 'other' },
        { id: 'default-model', name: '旧版默认', family: 'other' },
        { id: 'codex:default', name: 'Codex 默认', engine: 'codex' },
        { id: 'model-a', name: 'Model A', family: 'claude', credits: 'x1.00' },
        { id: 'model-b', name: 'Model B', family: 'glm', credits: 'x0.06' },
        { id: 'codex:gpt-5.5', name: 'GPT-5.5', family: 'gpt', engine: 'codex' },
      ],
      engines: [{ id: 'codebuddy', name: 'CodeBuddy', configured: true, personal: true }, { id: 'codex', name: 'Codex', configured: true, personal: true }],
    }),
  }),
};
ctx.window = ctx;
vm.createContext(ctx);
vm.runInContext(moduleSource, ctx);

(async () => {
  const api = ctx.window.AgentFlowAssistantModels;
  assert.ok(api, 'window.AgentFlowAssistantModels must be defined for classic-SPA pickers');
  for (const name of ['catalog', 'load', 'paint', 'paintItem', 'renderList', 'renderOptions']) {
    assert.equal(typeof api[name], 'function', `AgentFlowAssistantModels.${name}`);
  }
  assert.equal(api.DEFAULT, 'default');
  assert.equal(api.cliIcon('agy'), '/images/credentials/antigravity.svg');
  assert.equal(api.cliIcon('kimi'), '/images/credentials/kimi-code.png');
  assert.equal(api.cliIcon('pi'), '/images/credentials/pi.svg');
  api.paintItem(toggle, {id: 'kimi:k3.1-pro', family: 'other'});
  assert.match(icon.innerHTML, /\/images\/credentials\/kimi\.svg/);
  api.paintItem(toggle, {id: 'agy:gemini-4-neon-high'});
  assert.match(icon.innerHTML, /\/images\/credentials\/gemini\.svg/);
  api.paintItem(toggle, {id: 'claude-code:opus'});
  assert.match(icon.innerHTML, /\/images\/credentials\/claude\.svg/);
  api.paintItem(toggle, {id: 'cursor:composer-2.5'});
  assert.match(icon.innerHTML, /\/images\/credentials\/cursor\.svg/);
  api.paintItem(toggle, {id: 'cursor:cursor-grok-4.6', family: 'other'});
  assert.equal(icon.dataset.family, 'grok');
  assert.match(icon.innerHTML, /\/images\/credentials\/xai\.svg/);
  api.paintItem(toggle, {id: 'cursor:muse-wonder-1.0'});
  assert.equal(icon.dataset.family, 'muse');
  assert.match(icon.innerHTML, /\/images\/credentials\/meta\.svg/);
  api.paintItem(toggle, {id: 'auto', family: 'cursor'});
  assert.match(icon.innerHTML, /\/images\/credentials\/cursor\.svg/);
  api.paintItem(toggle, {id: 'primary-model', family: 'codebuddy'});
  assert.match(icon.innerHTML, /\/images\/credentials\/codebuddy\.svg/);
  for (const [id, family] of [
    ['opencode:alibaba/qwen3.7-plus', 'qwen'],
    ['opencode:doubao-seed-2-0-lite-260428', 'doubao'],
    ['pi:ernie-5.1', 'qianfan'],
    ['pi:moonshot-v1-8k', 'moonshot'],
  ]) {
    api.paintItem(toggle, {id, family: 'other'});
    assert.equal(icon.dataset.family, family);
    assert.ok(icon.innerHTML.includes(`/images/credentials/${family}.svg`));
  }
  assert.equal(api.engineOf('codex:gpt-5.5'), 'codex');
  assert.equal(api.concreteModelId('codebuddy:default-model'), '');
  await api.load();
  assert.equal(api.catalog().default, 'model-b');
  assert.equal(api.catalog().models.length, 3, 'catalog adopts concrete models');
  assert.match(bundle, /bindClarifyModelPicker/);

  const pair = pickerFixture();
  const changes = [];
  await api.bindFeaturePicker(pair.host, {model: 'default', onChange: id => changes.push(id)});
  assert.equal(pair.executor.value, 'codebuddy');
  assert.equal(pair.model.value, 'codebuddy:model-b');
  assert.deepEqual(pair.model.options.map(option => option.value), ['codebuddy:model-a', 'codebuddy:model-b']);
  pair.model.value = 'codebuddy:model-a';
  pair.model.onchange();
  pair.executor.value = 'codex';
  pair.executor.onchange();
  assert.equal(pair.model.value, 'codex:gpt-5.5');
  assert.deepEqual(pair.model.options.map(option => option.text), ['GPT-5.5']);
  pair.executor.value = 'codebuddy';
  pair.executor.onchange();
  assert.equal(pair.model.value, 'codebuddy:model-a');
  assert.deepEqual(changes, ['codebuddy:model-a', 'codex:gpt-5.5', 'codebuddy:model-a']);

  const codexOnly = pickerFixture();
  await api.bindFeaturePicker(codexOnly.host, {
    model: 'model-b',
    loadCatalog: async () => ({
      ...api.catalog(),
      engines: [
        {id: 'codebuddy', name: 'CodeBuddy', configured: true, personal: false},
        {id: 'codex', name: 'Codex', configured: true, personal: true},
      ],
    }),
  });
  assert.equal(codexOnly.executor.value, 'codex');
  assert.deepEqual(codexOnly.executor.options.map(item => item.value), ['codex']);
  assert.equal(codexOnly.model.value, 'codex:gpt-5.5');

  const saved = pickerFixture();
  await api.bindFeaturePicker(saved.host, {model: 'claude-code:old-model', keepSaved: true, loadCatalog: async () => ({
    engines: [{id: 'claude-code', name: 'Claude Code', configured: false}], models: [],
  })});
  assert.equal(saved.executor.value, 'claude-code');
  assert.equal(saved.model.value, 'claude-code:old-model');
  console.log('assistant models ok');
})().catch(err => { console.error(err); process.exit(1); });
