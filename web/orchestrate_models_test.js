'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {createDOM} = require('./hui_fake_dom');

const dom = createDOM();
const html = fs.readFileSync('index.html', 'utf8');
dom.html(html.slice(html.indexOf('<aside id="aiOrchestrator"'), html.indexOf('</aside>', html.indexOf('<aside id="aiOrchestrator"')) + 8));
const store = new Map([['agentflow.assistantModel', 'cb-model']]);
const requests = [];
const transcripts = new Map();
const catalog = {
  default: 'cb-model',
  engines: [{id: 'codebuddy', name: 'CodeBuddy', configured: true}, {id: 'codex', name: 'Codex', configured: true}, {id: 'claude-code', name: 'Claude Code', configured: true}, {id: 'pi', name: 'Pi', configured: true}, {id: 'opencode', name: 'OpenCode', configured: false}],
  models: [
    {id: 'cb-model', name: 'CB model', family: 'glm'},
    {id: 'codex:gpt-test', name: 'GPT test', engine: 'codex', family: 'gpt'},
    {id: 'codex:gpt-other', name: 'GPT other', engine: 'codex', family: 'gpt'},
    {id: 'claude-code:sonnet-test', name: 'Sonnet test', engine: 'claude-code', family: 'claude'},
    {id: 'pi:custom-test', name: 'Custom test', engine: 'pi'},
  ],
};
Object.assign(dom.window, {
  $: selector => dom.document.querySelector(selector),
  localStorage: {getItem: key => store.get(key) || null, setItem: (key, value) => store.set(key, value)},
  state: {projectFilter: 'project-a', workflow: null},
  AbortController,
  api: async url => {
    assert.match(url, /^\/api\/assistant\/sessions\/session-\d+$/);
    return {messages: transcripts.get(url.split('/').pop())};
  },
  fetch: async (url, options) => {
    if (url === '/api/assistant/models') return {ok: true, json: async () => catalog};
    assert.equal(url, '/api/assistant/chat');
    const request = JSON.parse(options.body);
    requests.push(request);
    const id = 'session-' + requests.length;
    const reply = {sessionId: id, message: 'draft ready', drafts: []};
    transcripts.set(id, [
      ...request.messages.map(message => ({...message, authorId: 7})),
      {role: 'assistant', model: request.model, content: JSON.stringify(reply), createdAt: '2026-11-21T00:00:00Z'},
    ]);
    dom.document.querySelectorAll('[data-model-toggle]').forEach(toggle => assert.equal(toggle.disabled, true));
    return {ok: true, headers: {get: () => 'application/json'}, json: async () => reply};
  },
});
dom.load('app/hui-select.js');
dom.load('app/assistant-models.js');
dom.load('orchestrate.js');
// Keep the real picker, transport and history; isolate canvas animation and
// asset writes so this test cannot save or publish a workflow.
dom.run(`
  beginAIOrchestration = async () => (aiBuild = {token: aiBuildToken, mode: 'create', projectId: state.projectFilter});
  aiReferencedWorkflows = () => [];
  aiOrchestratePrompt = goal => goal;
  refreshWorkflowAgents = async () => {};
  renderWorkflowMeta = () => {};
  placeAIGhost = () => {};
  completeAIBuild = async () => { aiBuild = null; return true; };
  appendOrchestratorNote = () => {};
  finishAIBuildVisuals = () => {};
  bindAIOrchestratorModelPicker();
`);

(async () => {
  await dom.settle();
  function picker(id) {
    const host = dom.document.querySelector(id);
    return {host, toggle: host.querySelector('[data-model-toggle]'), menu: host.querySelector('.ai-model-menu'), search: host.querySelector('[data-model-search]'), list: host.querySelector('[role="listbox"]')};
  }
  const cli = picker('#aiOrchestratorCLIPicker');
  const model = picker('#aiOrchestratorModelPicker');
  assert.equal(cli.host.querySelector('[data-model-label]').textContent, 'CodeBuddy');
  assert.equal(model.host.querySelector('[data-model-label]').textContent, 'CB model');

  function open(picker) { if (picker.menu.hidden) picker.toggle.onclick(); assert.equal(picker.menu.hidden, false); }
  function choose(picker, id) {
    open(picker);
    const option = picker.list.querySelectorAll('[role="option"]').find(item => item.dataset.id === id);
    assert.ok(option, 'catalog contains ' + id);
    assert.equal(option.disabled, false);
    option.onclick();
    assert.equal(picker.menu.hidden, true);
  }
  function pick(id) {
    const engine = id.includes(':') ? id.split(':')[0] : 'codebuddy';
    if (cli.host.querySelector('[data-model-label]').textContent !== catalog.engines.find(item => item.id === engine).name) choose(cli, engine);
    choose(model, id);
  }
  open(model);
  assert.equal(model.list.children.length, 1, 'model picker only shows selected CLI models');
  open(cli);
  assert.equal(model.menu.hidden, true, 'CLI and model menus are mutually exclusive');
  assert.equal(cli.list.children.length, 5);
  assert.equal(cli.list.children[4].disabled, true, 'unconfigured CLIs are visible and disabled');
  cli.search.value = 'Codex'; cli.search.oninput();
  assert.equal(cli.list.children.length, 1, 'CLI search is independent of models');
  cli.search.dispatchEvent(dom.event('keydown', {key: 'Escape', bubbles: true}));
  assert.equal(cli.menu.hidden, true);
  assert.equal(dom.document.activeElement, cli.toggle);
  choose(cli, 'codex');
  assert.equal(store.get('agentflow.orchestratorModel'), 'codex:gpt-test', 'changing CLI selects its model');
  assert.equal(cli.host.querySelector('[data-model-label]').textContent, 'Codex');
  assert.equal(model.host.querySelector('[data-model-label]').textContent, 'GPT test');
  open(model);
  assert.equal(model.list.children.length, 2);
  model.search.value = 'gpt-other'; model.search.oninput();
  assert.equal(model.list.children.length, 1, 'model search matches ID');
  model.search.value = 'missing'; model.search.oninput();
  assert.equal(model.host.querySelector('[data-model-empty]').hidden, false);
  model.search.dispatchEvent(dom.event('keydown', {key: 'Escape', bubbles: true}));
  open(model);
  model.search.dispatchEvent(dom.event('keydown', {key: 'ArrowUp', bubbles: true}));
  assert.equal(dom.document.activeElement, model.list.children[1], 'up from search focuses last option');
  choose(model, 'codex:gpt-test');
  assert.equal(store.get('agentflow.assistantModel'), 'cb-model', 'orchestration has its own preference');

  async function send() {
    dom.document.querySelector('#aiOrchestratorInput').value = 'Build workflow';
    await dom.run('runAIOrchestrator()');
    dom.document.querySelectorAll('[data-model-toggle]').forEach(toggle => assert.equal(toggle.disabled, false));
    const request = requests.at(-1);
    assert.equal(request.approvalMode, 'confirm', 'orchestration keeps draft-only permissions');
    return request;
  }
  let request = await send();
  assert.equal(request.model, 'codex:gpt-test');
  assert.equal(request.sessionId, '');
  request = await send();
  assert.equal(request.sessionId, 'session-1', 'same CLI resumes the conversation');
  assert.deepEqual(request.messages.slice(0, -1), transcripts.get('session-1'), 'resuming preserves the stored transcript');
  pick('codex:gpt-other');
  request = await send();
  assert.equal(request.sessionId, 'session-2', 'same CLI model change keeps history');
  assert.equal(request.model, 'codex:gpt-other');
  choose(cli, 'codex');
  assert.equal(store.get('agentflow.orchestratorModel'), 'codex:gpt-other', 'reselecting CLI keeps its current model');
  pick('claude-code:sonnet-test');
  request = await send();
  assert.equal(request.sessionId, '', 'different CLI starts a fresh session');
  assert.equal(request.messages.length, 1);
  assert.equal(request.model, 'claude-code:sonnet-test');
  pick('pi:custom-test');
  assert.equal((await send()).model, 'pi:custom-test', 'additional CLIs use the same path');
  pick('cb-model');
  assert.equal((await send()).model, 'cb-model', 'CodeBuddy keeps its unprefixed model ID');

  choose(cli, 'codex');
  assert.equal(store.get('agentflow.orchestratorModel'), 'codex:gpt-other', 'switching back restores the last model for that CLI');
  choose(cli, 'codebuddy');

  // Another project's older session must also be isolated after a CLI change.
  dom.window.state.projectFilter = 'project-b';
  pick('codex:gpt-test');
  await send();
  dom.window.state.projectFilter = 'project-a';
  request = await send();
  assert.equal(request.sessionId, '');
  assert.equal(request.messages.length, 1);

  catalog.default = 'default';
  await dom.run('AgentFlowAssistantModels.load()');
  store.delete('agentflow.orchestratorModel.codebuddy');
  choose(cli, 'codebuddy');
  assert.equal((await send()).model, 'cb-model', 'switching to CodeBuddy picks the first offered model');
  open(model);
  assert.deepEqual([...model.list.querySelectorAll('[role="option"]')].map(option => option.dataset.id), ['cb-model']);
  console.log('orchestrator multi-CLI models ok');
})().catch(error => { console.error(error); process.exit(1); });
