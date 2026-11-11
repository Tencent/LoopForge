'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const Module = require('node:module');

function harness() {
  const commands = new Map(), errors = [], documents = [], requests = [], updates = [], messages = [];
  let provider, input = ['Implement this', 'looks good'], picks = [], run;
  const folder = { name: 'project', uri: { fsPath: '/project', scheme: 'file' } };
  const values = { serverUrl: 'https://example.test', projectId: 'project-1', workflowId: 'workflow-1', pollInterval: 300 };
  const vscode = {
    TreeItem: class { constructor(label, collapsibleState) { this.label = label; this.collapsibleState = collapsibleState; } },
    TreeItemCollapsibleState: { Collapsed: 1, None: 0 }, ThemeIcon: class { constructor(id) { this.id = id; } },
    EventEmitter: class { constructor() { this.event = () => {}; } fire() {} dispose() {} },
    ConfigurationTarget: { Workspace: 2 },
    workspace: {
      workspaceFolders: [folder], getWorkspaceFolder: () => folder, asRelativePath: () => 'src/file.js',
      getConfiguration: () => ({ get: (key, fallback) => values[key] ?? fallback, update: async (...args) => { updates.push(args); values[args[0]] = args[1]; } }),
      openTextDocument: async options => { documents.push(options); return { uri: `document-${documents.length}` }; }
    },
    window: {
      activeTextEditor: { document: { uri: { fsPath: '/project/src/file.js' }, getText: () => 'first line\nsecond line' }, selection: { isEmpty: false } },
      createOutputChannel: () => ({ appendLine() {}, clear() {}, show() {}, dispose() {} }),
      createTreeView: (_id, options) => { provider = options.treeDataProvider; return { dispose() {} }; },
      showInputBox: async () => input.shift(), showQuickPick: async items => picks.length ? picks.shift() : items[0],
      showErrorMessage: message => { errors.push(message); }, showInformationMessage: async message => { messages.push(message); },
      setStatusBarMessage() {}, showTextDocument: async doc => { documents.push(doc); }
    },
    commands: { registerCommand: (id, fn) => { commands.set(id, fn); return { dispose() {} }; }, executeCommand: async (...args) => requests.push({ command: args }) }
  };
  const originalLoad = Module._load;
  Module._load = function(request, ...args) { return request === 'vscode' ? vscode : originalLoad.call(this, request, ...args); };
  let extension;
  try { delete require.cache[require.resolve('../extension')]; extension = require('../extension'); } finally { Module._load = originalLoad; }
  const oldFetch = globalThis.fetch;
  globalThis.fetch = async (url, options) => {
    const route = new URL(url).pathname;
    requests.push({ route, method: options.method, body: options.body && JSON.parse(options.body) });
    let body;
    if (route === '/api/projects') body = [{ id: 'project-2', name: 'Other' }];
    else if (route === '/api/workflows') body = [{ id: 'workflow-2', name: 'Other', projectId: 'project-2' }];
    else if (route === '/api/runs' && options.method === 'POST') {
      run = { id: 'run-1', status: 'waiting', workflowId: 'workflow-1', nodes: { gate: { nodeId: 'gate', status: 'waiting_approval', attempt: 1, reworkCount: 0 } }, artifacts: [{ id: 'artifact-id', name: 'report.md', mediaType: 'text/markdown' }], workflowSnapshot: { nodes: [{ id: 'gate', rejectTo: 'work' }] } };
      body = run;
    } else if (route === '/api/runs') body = run ? [{ ...run, artifacts: undefined }] : [];
    else if (route === '/api/runs/run-1') body = run;
    else if (route === '/api/runs/run-1/approve' || route === '/api/runs/run-1/reject') {
      const expected = JSON.parse(options.body).expected;
      const current = run.nodes.gate;
      if (expected.status !== current.status || expected.attempt !== current.attempt || expected.reworkCount !== current.reworkCount) return new Response(JSON.stringify({ error: 'stale decision' }), { status: 409, headers: { 'content-type': 'application/json' } });
      run = { ...run, status: 'succeeded', nodes: { gate: { ...current, status: 'succeeded' } } }; body = { ok: true };
    }
    else if (route === '/api/runs/run-1/artifacts/artifact-id') return new Response('report content', { status: 200, headers: { 'content-type': 'text/plain' } });
    else if (route === '/api/runs/run-1/logs') body = { logs: [{ body: 'completed' }] };
    else return new Response(JSON.stringify({ error: 'not found' }), { status: 404, headers: { 'content-type': 'application/json' } });
    return new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } });
  };
  const context = { subscriptions: [], secrets: { get: async () => '', store: async () => {}, delete: async () => {} } };
  extension.activate(context);
  return { commands, errors, documents, requests, updates, messages, get provider() { return provider; }, setInput: value => { input = value; }, setPicks: value => { picks = value; }, advanceRun() { run = { ...run, nodes: { gate: { ...run.nodes.gate, attempt: 2, reworkCount: 1 } } }; }, dispose() { context.subscriptions.forEach(item => item.dispose()); globalThis.fetch = oldFetch; } };
}

test('starts a run with multiline selection, shows approval and opens artifact by ID', async () => {
  const h = harness();
  try {
    await h.commands.get('loopforge.startWorkflow')();
    const request = h.requests.find(item => item.route === '/api/runs' && item.method === 'POST');
    assert.equal(request.body.projectId, 'project-1');
    assert.match(request.body.input, /Implement this/);
    assert.match(request.body.input, /src\/file\.js/);
    assert.match(request.body.input, /first line\nsecond line/);
    const roots = await h.provider.getChildren();
    assert.equal(roots[0].contextValue, 'loopforge.run');
    const children = await h.provider.getChildren(roots[0]);
    assert.deepEqual(children.map(item => item.contextValue), ['loopforge.approval', 'loopforge.artifact']);
    await h.commands.get('loopforge.openArtifact')(children[1]);
    assert.ok(h.requests.some(item => item.route === '/api/runs/run-1/artifacts/artifact-id'));
    assert.ok(h.documents.some(doc => doc.content === 'report content'));
    await h.commands.get('loopforge.approve')(children[0]);
    assert.deepEqual(h.requests.find(item => item.route === '/api/runs/run-1/approve').body.expected, { status: 'waiting_approval', attempt: 1, reworkCount: 0 });
    await h.commands.get('loopforge.showResult')(roots[0]);
    assert.ok(h.requests.some(item => item.route === '/api/runs/run-1/logs'));
    assert.deepEqual(h.errors, []);
    assert.deepEqual(h.updates, []);
  } finally { h.dispose(); }
});

test('switching projects clears the old workflow and asks for a workflow in the new project', async () => {
  const h = harness();
  try {
    await h.commands.get('loopforge.selectProject')();
    assert.deepEqual(h.updates, [['workflowId', '', 2], ['projectId', 'project-2', 2]]);
    await h.commands.get('loopforge.startWorkflow')();
    const start = h.requests.find(item => item.route === '/api/runs' && item.method === 'POST');
    assert.equal(start.body.projectId, 'project-2');
    assert.equal(start.body.workflowId, 'workflow-2');
  } finally { h.dispose(); }
});

for (const action of ['approve', 'reject']) {
  test(`${action} uses the displayed attempt and reports a stale decision`, async () => {
    const h = harness();
    try {
      await h.commands.get('loopforge.startWorkflow')();
      const [root] = await h.provider.getChildren();
      const [approval] = await h.provider.getChildren(root);
      h.advanceRun();
      await h.commands.get(`loopforge.${action}`)(approval);
      const request = h.requests.find(item => item.route === `/api/runs/run-1/${action}`);
      assert.deepEqual(request.body.expected, { status: 'waiting_approval', attempt: 1, reworkCount: 0 });
      assert.ok(h.errors.some(error => error.includes('stale decision')));
      assert.ok(!h.messages.some(message => /已确认|已退回/.test(message)));
    } finally { h.dispose(); }
  });
}

test('start failures are reported to the user', async () => {
  const h = harness();
  try {
    globalThis.fetch = async () => { throw new Error('offline'); };
    await h.commands.get('loopforge.startWorkflow')();
    assert.match(h.errors[0], /Cannot connect to LoopForge/);
  } finally { h.dispose(); }
});
