// Task 7: 澄清选择器按框架分组。这里钉的是「不能用就整组灰掉并说清为什么」：
// 选中项要能看出来，禁用项点不动，责任人不是自己时界面要说明用的是谁的凭据。
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
function extract(name) {
  const pattern = new RegExp('^function ' + name + '\\(|^async function ' + name + '\\(', 'm');
  const start = bundle.search(pattern);
  assert.ok(start >= 0, `${name} is missing from the app bundle`);
  const rest = bundle.slice(start);
  const next = /\n(?:async )?function \w+\(/.exec(rest);
  return next ? rest.slice(0, next.index) : rest;
}

const engineCatalog = {
  responsibleUserId: 7,
  responsibleUserName: '责任人丁',
  default: 'codex:gpt-5.5',
  defaultStale: false,
  engines: [
    { id: 'codebuddy', name: 'CodeBuddy', configured: true, available: true, reason: '', credential: 'codeBuddyApiKey', models: [{ id: 'glm-a', name: 'GLM A', engine: '' }, { id: 'glm-b', name: 'GLM B', engine: '' }] },
    { id: 'codex', name: 'Codex', configured: true, available: true, reason: '', credential: 'codexApiKey', models: [{ id: 'codex:gpt-5.5', name: 'GPT 5.5', engine: 'codex' }] },
    { id: 'claude-code', name: 'Claude Code', configured: false, available: false, reason: '请先在个人配置中配置 Claude Code API Key 后再使用 Claude Code 模型', credential: 'claudeCodeApiKey', models: [] },
  ],
};

let fetches = [];
const store = new Map();
const ctx = {
  console, setTimeout, clearTimeout, Promise, assert,
  document: { addEventListener() {}, querySelectorAll: () => [], createElement },
  localStorage: {
    getItem: key => (store.has(key) ? store.get(key) : null),
    setItem: (key, value) => store.set(key, String(value)),
    removeItem: key => store.delete(key),
  },
  state: { currentUser: { id: 9 } },
  escapeHTML: value => String(value ?? ''),
  $$: () => [],
  fetch: async (url) => {
    fetches.push(url);
    if (String(url).startsWith('/api/clarify-engines')) {
      return { ok: true, json: async () => engineCatalog };
    }
    return {
      ok: true,
      json: async () => ({
        default: 'glm-a',
        models: engineCatalog.engines.flatMap(engine => engine.models),
        engines: engineCatalog.engines.map(engine => ({ id: engine.id, name: engine.name, configured: engine.configured, credential: engine.credential })),
      }),
    };
  },
};
ctx.window = ctx;
vm.createContext(ctx);
vm.runInContext(moduleSource, ctx);

(async () => {
  const api = ctx.window.AgentFlowAssistantModels;
  assert.equal(typeof api.renderGrouped, 'function', 'AgentFlowAssistantModels.renderGrouped must exist for the grouped clarify picker');

  // 1) renderGrouped：分组标题、禁用原因、选中态、点不动的禁用项。
  const menu = createElement('div');
  menu.className = 'ai-model-menu';
  const picked = [];
  const groups = [
    { id: 'codebuddy', name: 'CodeBuddy', reason: '', disabled: false, models: engineCatalog.engines[0].models },
    { id: 'claude-code', name: 'Claude Code', reason: '请先在个人配置中配置 Claude Code API Key 后再使用 Claude Code 模型', disabled: true, models: [{ id: 'claude-code:claude-opus-5', name: 'Claude Opus 5' }] },
  ];
  api.renderGrouped(menu, groups, 'glm-b', id => picked.push(id));
  const headers = menu.children.filter(child => String(child.className).includes('ai-model-group'));
  assert.equal(headers.length, 2, '每个框架一个分组标题');
  assert.ok(String(headers[1].textContent).includes('Claude Code'), '分组标题写框架名');
  assert.ok(String(headers[1].textContent).includes('请先在个人配置中配置'), '禁用分组要在标题里带上原因');
  const options = menu.children.filter(child => String(child.className).includes('ai-model-option'));
  assert.equal(options.length, 3, '两个分组共三项模型');
  const enabled = options.filter(option => !option.disabled);
  assert.deepEqual(enabled.map(option => option.dataset.id), ['glm-a', 'glm-b'], '禁用分组里的模型必须全部点不动');
  const active = enabled.filter(option => option.className.includes('is-active'));
  assert.equal(active.length, 1, '当前选中的模型要标出来');
  assert.equal(active[0].dataset.id, 'glm-b');
  active[0].onclick();
  assert.deepEqual(picked, ['glm-b'], '可用项点一下就选中');
  const blocked = options.filter(option => option.disabled);
  assert.deepEqual(blocked.map(option => option.dataset.id), ['claude-code:claude-opus-5'], '不可用框架的模型整组禁用');
  assert.ok(blocked[0].title.includes('请先在个人配置中配置'), '禁用项悬停给出原因');
  blocked.forEach(option => option.onclick());
  assert.deepEqual(picked, ['glm-b'], '禁用项不能触发选择');

  // 2) 真实的澄清选择器 helper：作用域查询、分组映射、生效值、责任人提示。
  for (const name of ['selectedClarifyModel', 'setClarifyModel', 'clarifyEngineCacheKey', 'clarifyEngineCatalogCached', 'clarifyEngineCatalog', 'resetClarifyEngineCatalogCache', 'clarifyPickerGroups', 'clarifyEffectiveModel', 'clarifyResponsibleHint']) {
    vm.runInContext(extract(name), ctx);
  }

  ctx.resetClarifyEngineCatalogCache();
  const first = await ctx.clarifyEngineCatalog({ projectId: 'p1' });
  assert.equal(first.responsibleUserId, 7, '可用性来自需求责任人');
  const again = await ctx.clarifyEngineCatalog({ projectId: 'p1' });
  assert.equal(again, first, '同一作用域只问一次');
  await ctx.clarifyEngineCatalog({ issueId: 'i1' });
  assert.equal(fetches.length, 2, '换作用域要重新问');

  const mapped = ctx.clarifyPickerGroups(first);
  assert.equal(mapped.length, 3, '每个框架一个分组');
  const disabledGroup = mapped.find(group => group.id === 'claude-code');
  assert.equal(disabledGroup.disabled, true, '不可用的框架整组禁用');
  assert.equal(disabledGroup.reason, engineCatalog.engines[2].reason, '原因要原样带出来');
  assert.equal(mapped.find(group => group.id === 'codex').disabled, false);

  store.set('agentflow.clarifyModel', 'glm-a');
  assert.equal(ctx.clarifyEffectiveModel(first, null), 'glm-a', '浏览器里的临时选择优先');
  assert.equal(ctx.clarifyEffectiveModel(first, { clarification: { modelId: 'codex:gpt-5.5' } }), 'codex:gpt-5.5', '这张单记住的框架优先于临时选择');
  store.clear();
  assert.equal(ctx.clarifyEffectiveModel(first, null), 'codex:gpt-5.5', '没有临时选择时用接口给的默认');

  const hint = ctx.clarifyResponsibleHint(first);
  assert.ok(hint.includes('澄清将使用这位责任人的凭据'), '责任人不是自己要说明用的是谁的凭据');
  assert.ok(hint.includes('责任人丁'), '提示里要写出责任人的名字');
  ctx.state.currentUser = { id: 7 };
  assert.equal(ctx.clarifyResponsibleHint(first), '', '就是自己时不多说一句');

  console.log('clarify engine picker ok');
})().catch(err => { console.error(err); process.exit(1); });
