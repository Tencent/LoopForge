// Shared CLI / model picker for the classic SPA (issue #441).
//
// Regression: the clarification model picker (and every other classic-SPA
// picker) needs window.AgentFlowAssistantModels. A refactor once deleted the
// only definition and the 澄清 tab's 「选择模型」 button silently stopped
// opening. This test boots the shared module plus the real picker helpers
// from the bundle and drives the menus the way a user does.
//
// Issue #441: the chat composer, the role wizard and 新建需求 each drew a
// different picker. They now share AgentFlowAssistantModels.bindPicker, the
// classic-SPA twin of chatbot/src/ModelPicker.jsx: a body-level menu with
// search, brand icons, the "family · credits" line and a fixed check slot.
'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { createDOM } = require('./hui_fake_dom');

const bundle = fs.readFileSync(process.argv[2], 'utf8');
const modulePath = process.argv[3];
const indexHTML = fs.readFileSync(process.argv[4] || 'index.html', 'utf8');

const dom = createDOM();
const { window: win, document } = dom;
const store = new Map([['agentflow.clarifyModel', 'model-a']]);
Object.assign(win, {
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
        { id: 'codex:gpt-5.5', name: 'GPT-5.5', family: 'gpt', engine: 'codex' },
      ],
      engines: [{ id: 'codebuddy', name: 'CodeBuddy' }, { id: 'codex', name: 'Codex' }],
    }),
  }),
  $: selector => document.querySelector(selector),
  $$: selector => [...document.querySelectorAll(selector)],
  escapeHTML: value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]),
  state: { data: { executors: [
    { id: 'codebuddy', name: 'CodeBuddy', model: 'glm-5' },
    { id: 'codex', name: 'Codex' },
  ] } },
});
dom.load(modulePath);

// Pull real helpers out of the app bundle, as they ship.
function extract(name) {
  const start = bundle.search(new RegExp('^(?:function ' + name + '\\(|const ' + name + '=)', 'm'));
  assert.ok(start >= 0, `${name} is missing from the app bundle`);
  const rest = bundle.slice(start);
  const next = /\n(?:async )?function \w+\(|\nconst \w+=/.exec(rest.slice(1));
  dom.run(next ? rest.slice(0, next.index + 1) : rest, name);
}
const menus = () => [...document.querySelectorAll('.lf-picker-menu')].filter(menu => menu.getAttribute('aria-hidden') !== 'true');
const options = menu => [...menu.querySelectorAll('.lf-picker-option')];
const key = (target, name) => {
  const event = new win.Event('keydown', { bubbles: true, key: name });
  target.dispatchEvent(event);
  return event;
};

(async () => {
  const api = win.AgentFlowAssistantModels;
  assert.ok(api, 'window.AgentFlowAssistantModels must be defined for classic-SPA pickers');
  for (const name of ['bindPicker', 'brandOf', 'catalog', 'engineBrand', 'load', 'paint', 'paintItem', 'renderList', 'renderOptions']) {
    assert.equal(typeof api[name], 'function', `AgentFlowAssistantModels.${name}`);
  }
  assert.equal(api.DEFAULT, 'default');

  // Brands follow the chat composer: family, then id, then name. A model the
  // catalog files under "other" still gets its logo from the name.
  assert.equal(api.brandOf({ id: 'sol-6.1', name: 'GPT-6.1 Sol', family: 'other' }), 'gpt');
  assert.equal(api.brandOf({ id: 'vendor/claude-opus', family: 'other' }), 'claude');
  assert.equal(api.brandOf({ id: 'glm-5.3-flash-ioa' }), 'glm');
  assert.equal(api.brandOf({ id: 'mystery-1', name: 'Mystery' }), '');
  assert.equal(api.engineBrand('codex'), 'gpt');
  assert.equal(api.engineBrand('codebuddy'), 'codebuddy');
  assert.equal(api.engineBrand('deepseek'), 'deepseek', 'the DeepSeek executor keeps its logo');

  // Pickers keep painting before the catalog lands, so the built-in fallback
  // must already render a usable label, and an unknown brand shows the
  // neutral glyph instead of a lettered "AI" badge.
  assert.equal(api.codeBuddyModel('codex:gpt-5.5'), 'default', 'codex ids fall back before the catalog loads');
  const probe = dom.html('<button><span class="ai-model-icon"></span><span data-model-label></span></button>');
  api.paint(probe, api.DEFAULT);
  assert.equal(probe.querySelector('[data-model-label]').textContent, 'CLI 默认模型');
  const probeIcon = probe.querySelector('.ai-model-icon');
  assert.equal(probeIcon.dataset.family, 'other');
  assert.equal(probeIcon.dataset.brand, 'unknown');
  assert.ok(probeIcon.querySelector('svg[data-icon="brain"]'), 'unknown brands use the Harness brain glyph');
  assert.ok(!/AI/.test(probeIcon.textContent), 'no lettered badge');
  probe.remove();

  await api.load();
  assert.equal(api.catalog().default, 'model-b');
  assert.equal(api.catalog().models.length, 3, 'catalog adopts /api/assistant/models');
  // Issue #228: clarification runs on CodeBuddy only, so CodeBuddy-only
  // pickers hide other CLIs and a saved "codex:…" id falls back.
  assert.deepEqual(api.codeBuddyModels().map(m => m.id), ['model-a', 'model-b']);
  assert.equal(api.codeBuddyModel('codex:gpt-5.5'), 'default');
  assert.equal(api.codeBuddyModel(''), 'default');
  assert.equal(api.codeBuddyModel('model-a'), 'model-a');

  // ---- 新建需求 / 澄清: the clarification picker ----
  for (const name of ['selectedClarifyModel', 'setClarifyModel', 'clarifyModelPickerHTML', 'bindClarifyModelPicker']) extract(name);
  const host = dom.html(win.clarifyModelPickerHTML(false));
  const toggle = host.querySelector('[data-model-toggle]');
  const hidden = host.querySelector('[data-clarify-model-value]');
  assert.equal(host.querySelector('.ai-model-menu'), null, 'the old inline menu is gone');
  win.bindClarifyModelPicker(host);
  await dom.settle();
  assert.equal(host.dataset.bound, '1', 'clarify picker must bind once the model API exists');
  assert.equal(toggle.querySelector('[data-model-label]').textContent, 'Model A');
  assert.equal(toggle.querySelector('.ai-model-icon').dataset.brand, 'claude');
  assert.equal(toggle.getAttribute('aria-haspopup'), 'menu');

  toggle.click();
  let [menu] = menus();
  assert.ok(menu, 'clicking 选择模型 must open the shared menu');
  assert.equal(menu.parentNode, document.body, 'the menu hangs from <body> so dialogs cannot clip it');
  assert.equal(menu.getAttribute('role'), 'menu');
  assert.equal(toggle.getAttribute('aria-expanded'), 'true');
  assert.equal(toggle.getAttribute('aria-controls'), menu.id);
  const search = menu.querySelector('.lf-picker-search input');
  assert.ok(search, 'model menus are searchable');
  assert.equal(search.placeholder, '搜索模型');
  assert.equal(document.activeElement, search, 'opening focuses the search box');
  let rows = options(menu);
  assert.equal(rows.length, 2, 'menu lists only CodeBuddy models');
  assert.ok(!rows.some(row => row.dataset.id.startsWith('codex:')), 'clarify menu hides Codex models');
  assert.equal(rows[0].getAttribute('role'), 'menuitemradio');
  assert.equal(rows[0].getAttribute('aria-checked'), 'true', 'stored model is checked');
  assert.ok(rows[0].querySelector('.lf-picker-check svg[data-icon="check"]'), 'checked row draws the check');
  assert.equal(rows[1].querySelector('.lf-picker-check').children.length, 0, 'check slot stays reserved');
  assert.equal(rows[0].querySelector('.lf-picker-copy strong').textContent, 'Model A');
  assert.equal(rows[0].querySelector('.lf-picker-copy small').textContent, 'claude · x1.00', 'family · credits, as in the chat composer');
  assert.equal(rows[1].querySelector('.ai-model-icon').dataset.brand, 'glm');

  // Search by name, id or family; an empty result says so.
  search.value = 'glm';
  search.dispatchEvent(new win.Event('input', { bubbles: true }));
  rows = options(menu);
  assert.deepEqual(rows.map(row => row.dataset.id), ['model-b'], 'search matches the family');
  search.value = 'nothing-like-this';
  search.dispatchEvent(new win.Event('input', { bubbles: true }));
  assert.equal(options(menu).length, 0);
  assert.equal(menu.querySelector('.lf-picker-empty').textContent, '没有匹配的模型');
  menu.querySelector('.lf-picker-search-clear').click();
  assert.equal(options(menu).length, 2, 'clearing restores the full list');

  // Arrow keys move through the rows; Escape closes without closing the dialog.
  key(search, 'ArrowDown');
  assert.equal(document.activeElement, options(menu)[0]);
  key(document.activeElement, 'ArrowDown');
  assert.equal(document.activeElement, options(menu)[1]);
  let leaked = false;
  const dialogEscape = event => { if (event.key === 'Escape') leaked = true; };
  document.addEventListener('keydown', dialogEscape);
  const escape = key(document.activeElement, 'Escape');
  document.removeEventListener('keydown', dialogEscape);
  assert.ok(escape.defaultPrevented);
  assert.equal(leaked, false, 'Escape must not reach the surrounding dialog');
  assert.equal(menus().length, 0);
  assert.equal(toggle.getAttribute('aria-expanded'), 'false');
  assert.equal(document.activeElement, toggle, 'Escape returns focus to the trigger');
  dom.advance(200);
  assert.equal(document.querySelectorAll('.lf-picker-menu').length, 0, 'closed menus are removed after the exit motion');

  // Enter in the search box picks the first match.
  key(toggle, 'ArrowDown');
  [menu] = menus();
  assert.ok(menu, 'ArrowDown on the trigger opens the menu');
  const box = menu.querySelector('.lf-picker-search input');
  box.value = 'model b';
  box.dispatchEvent(new win.Event('input', { bubbles: true }));
  key(box, 'Enter');
  assert.equal(store.get('agentflow.clarifyModel'), 'model-b');
  assert.equal(hidden.value, 'model-b');
  assert.equal(toggle.querySelector('[data-model-label]').textContent, 'Model B');
  assert.equal(menus().length, 0);

  // A press outside closes it.
  toggle.click();
  assert.equal(menus().length, 1);
  document.body.dispatchEvent(new win.Event('pointerdown', { bubbles: true }));
  assert.equal(menus().length, 0, 'outside press closes the menu');
  dom.advance(200);
  host.remove();

  // A Codex id saved before #228 must not reach chat-clarify.
  store.set('agentflow.clarifyModel', 'codex:gpt-5.5');
  assert.equal(win.selectedClarifyModel(), 'default');

  // ---- 角色定义: CLI, model and effort in the role wizard ----
  const grid = /<div class="as-grid agent-runtime-grid">[\s\S]*?<label class="as-field" id="agentModelCustomField"[\s\S]*?<\/label>/.exec(indexHTML);
  assert.ok(grid, 'role wizard runtime fields are in index.html');
  assert.ok(!/class="select" id="agentExecutorSelect"/.test(grid[0]), 'the CLI is no longer a plain select');
  const form = dom.html(`<form id="agentForm">${grid[0]}</form>`);
  const select = form.querySelector('select[name="executorId"]');
  assert.ok(select.hasAttribute('hidden') && select.hasAttribute('data-native'), 'the select only keeps the value');
  form.elements = {
    executorId: select,
    modelChoice: form.querySelector('[name="modelChoice"]'),
    effort: form.querySelector('[name="effort"]'),
    model: form.querySelector('input[name="model"]'),
  };
  win.AgentFlowExecutorModels = {
    codebuddy: { models: [{ id: 'glm-5', name: 'GLM-5' }], efforts: [{ value: 'high', label: '高' }] },
    codex: { models: [{ id: 'gpt-5.5', name: 'GPT-5.5' }], efforts: [], customModel: true },
    claude: { models: [] },
  };
  for (const name of ['executorOptions', 'AGENT_CUSTOM_MODEL', 'executorModelCatalogs', 'agentModelAPI', 'currentAgentExecutorId',
    'agentModelItems', 'agentEffortItems', 'paintAgentPicker', 'agentDefaultModelItem', 'agentExecutorItems', 'agentModelChoices',
    'renderAgentExecutorPicker', 'renderAgentModelPicker', 'syncAgentModelCustomField', 'renderAgentEffortPicker',
    'applyAgentModelUI', 'bindAgentPickers']) extract(name);
  select.innerHTML = win.executorOptions('codebuddy');
  let changes = 0;
  select.addEventListener('change', event => { changes++; win.applyAgentModelUI(event.target.value, '', ''); });
  win.bindAgentPickers();
  win.applyAgentModelUI('codebuddy', 'glm-5', 'high');
  const cliToggle = form.querySelector('#agentExecutorPicker [data-model-toggle]');
  const modelToggle = form.querySelector('#agentModelPicker [data-model-toggle]');
  const effortToggle = form.querySelector('#agentEffortPicker [data-model-toggle]');
  const label = t => t.querySelector('[data-model-label]').textContent;
  assert.equal(label(cliToggle), 'CodeBuddy');
  assert.equal(cliToggle.querySelector('.ai-model-icon').dataset.brand, 'codebuddy', 'CLI trigger shows the CLI logo');
  assert.equal(label(modelToggle), 'GLM-5');
  assert.equal(modelToggle.querySelector('.ai-model-icon').dataset.brand, 'glm');
  assert.equal(label(effortToggle), '高');

  // CLI menu: same chrome, logo per CLI, no search for a short list.
  cliToggle.click();
  [menu] = menus();
  assert.equal(menu.getAttribute('aria-label'), 'CLI');
  assert.equal(menu.querySelector('.lf-picker-search'), null, 'two CLIs need no search box');
  rows = options(menu);
  assert.deepEqual(rows.map(row => row.dataset.id), ['codebuddy', 'codex']);
  assert.equal(document.activeElement, rows[0], 'without search the checked row takes focus');
  assert.equal(rows[1].querySelector('.ai-model-icon').dataset.brand, 'gpt');
  assert.equal(rows[0].querySelector('small').textContent, '1 个可用模型');

  // Only one menu at a time.
  modelToggle.click();
  assert.equal(menus().length, 1, 'opening another picker closes the first');
  assert.equal(cliToggle.getAttribute('aria-expanded'), 'false');
  assert.equal(menus()[0].getAttribute('aria-label'), '模型');
  key(document.activeElement, 'Escape');
  dom.advance(200);

  // Switching CLI fires change on the select, so the model list follows it.
  cliToggle.click();
  options(menus()[0])[1].click();
  assert.equal(select.value, 'codex');
  assert.equal(changes, 1, 'picking a CLI fires change exactly once');
  assert.equal(label(cliToggle), 'Codex');
  assert.equal(label(modelToggle), '默认模型');
  assert.equal(label(effortToggle), '默认');
  dom.advance(200);

  modelToggle.click();
  [menu] = menus();
  assert.ok(menu.querySelector('.lf-picker-search input'), 'model menu is searchable');
  rows = options(menu);
  assert.deepEqual(rows.map(row => row.dataset.id), ['', 'gpt-5.5', '__custom__']);
  assert.ok(rows[0].querySelector('.ai-model-icon.is-empty'), 'entries without a logo keep the column aligned');
  assert.equal(rows[1].querySelector('.ai-model-icon').dataset.brand, 'gpt');
  rows[2].click();
  assert.equal(form.elements.modelChoice.value, '__custom__');
  assert.equal(form.querySelector('#agentModelCustomField').hidden, false, 'manual model id field appears');
  dom.advance(200);

  // Disabled triggers stay shut.
  effortToggle.disabled = true;
  effortToggle.click();
  assert.equal(menus().length, 0);

  console.log('assistant models ok');
})().catch(err => { console.error(err); process.exit(1); });
