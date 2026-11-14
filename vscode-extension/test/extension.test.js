'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const Module = require('node:module');

function harness() {
  const commands = new Map(), errors = [], documents = [], requests = [], updates = [], messages = [], secretOps = [];
  let provider, visibilityListener, configurationListener, input = ['Implement this', 'looks good'], picks = [], run;
  const folder = { name: 'project', uri: { fsPath: '/project', scheme: 'file' } };
  const values = { serverUrl: 'https://example.test', projectId: 'project-1', workflowId: 'workflow-1', pollInterval: 300 };
  const vscode = {
    TreeItem: class { constructor(label, collapsibleState) { this.label = label; this.collapsibleState = collapsibleState; } },
    TreeItemCollapsibleState: { Collapsed: 1, None: 0 }, ThemeIcon: class { constructor(id) { this.id = id; } },
    EventEmitter: class { constructor() { this.event = () => {}; } fire() {} dispose() {} },
    ConfigurationTarget: { Global: 1, Workspace: 2 },
    workspace: {
      workspaceFolders: [folder], getWorkspaceFolder: () => folder, asRelativePath: () => 'src/file.js',
      getConfiguration: () => ({ get: (key, fallback) => values[key] ?? fallback, update: async (...args) => { updates.push(args); values[args[0]] = args[1]; } }),
      onDidChangeConfiguration: fn => { configurationListener = fn; return { dispose() {} }; },
      openTextDocument: async options => { documents.push(options); return { uri: `document-${documents.length}` }; }
    },
    window: {
      activeTextEditor: { document: { uri: { fsPath: '/project/src/file.js' }, getText: () => 'first line\nsecond line' }, selection: { isEmpty: false } },
      createOutputChannel: () => ({ appendLine() {}, clear() {}, show() {}, dispose() {} }),
      createTreeView: (_id, options) => { provider = options.treeDataProvider; return { visible: false, onDidChangeVisibility: fn => { visibilityListener = fn; return { dispose() {} }; }, dispose() {} }; },
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
  const context = { subscriptions: [], secrets: { get: async () => '', store: async (key, value) => { secretOps.push(['store', key, value]); }, delete: async key => { secretOps.push(['delete', key]); } } };
  extension.activate(context);
  return { commands, errors, documents, requests, updates, messages, values, secretOps, vscode, get provider() { return provider; }, setVisible: visible => visibilityListener({ visible }), changeConfig: name => configurationListener({ affectsConfiguration: key => key === name }), setInput: value => { input = value; }, setPicks: value => { picks = value; }, advanceRun() { run = { ...run, nodes: { gate: { ...run.nodes.gate, attempt: 2, reworkCount: 1 } } }; }, dispose() { context.subscriptions.forEach(item => item.dispose()); globalThis.fetch = oldFetch; } };
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
    assert.doesNotMatch(request.body.input, /\/project/);
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

test('polls only for visible non-terminal runs and responds to configuration changes', async () => {
  const h = harness();
  try {
    const state = h.provider.state;
    assert.equal(h.requests.length, 0);
    assert.equal(state.pollTimer, undefined);
    await h.commands.get('loopforge.startWorkflow')();
    assert.equal(state.pollTimer, undefined);
    h.setVisible(true);
    await new Promise(resolve => setImmediate(resolve));
    assert.ok(state.pollTimer);
    assert.equal(state.pollTimer._idleTimeout, 300000);
    h.values.pollInterval = 12; h.changeConfig('loopforge.pollInterval');
    assert.equal(state.pollTimer._idleTimeout, 12000);
    h.setVisible(false); assert.equal(state.pollTimer, undefined);
    h.setVisible(true); await new Promise(resolve => setImmediate(resolve));
    await h.commands.get('loopforge.approve')();
    assert.equal(state.pollTimer, undefined);
  } finally { h.dispose(); }
});

test('selecting a project without an open folder uses user settings', async () => {
  const h = harness();
  try {
    h.vscode.workspace.workspaceFolders = [];
    await h.commands.get('loopforge.selectProject')();
    assert.deepEqual(h.updates, [['workflowId', '', 1], ['projectId', 'project-2', 1]]);
  } finally { h.dispose(); }
});

test('manual refresh runs again after an in-flight refresh', async () => {
  const h = harness();
  const originalFetch = globalThis.fetch;
  try {
    let release;
    globalThis.fetch = async (...args) => { await new Promise(resolve => { release = resolve; }); return originalFetch(...args); };
    const initial = h.provider.state.refresh({ quiet: true });
    await new Promise(resolve => setImmediate(resolve));
    const manual = h.commands.get('loopforge.refresh')();
    globalThis.fetch = originalFetch;
    release();
    await Promise.all([initial, manual]);
    assert.equal(h.requests.filter(r => r.route === '/api/runs').length, 2);
  } finally { h.dispose(); }
});

test('backs off polling on failures and resumes at the configured interval', async () => {
  const h = harness();
  const originalFetch = globalThis.fetch;
  try {
    h.values.pollInterval = 10;
    await h.commands.get('loopforge.startWorkflow')();
    h.setVisible(true);
    await new Promise(resolve => setImmediate(resolve));
    globalThis.fetch = async () => { throw new Error('offline'); };
    await h.provider.state.refresh({ quiet: true });
    assert.equal(h.provider.state.pollTimer._idleTimeout, 20000);
    await h.provider.state.refresh({ quiet: true });
    assert.equal(h.provider.state.pollTimer._idleTimeout, 40000);
    globalThis.fetch = originalFetch;
    await h.provider.state.refresh({ quiet: true });
    assert.equal(h.provider.state.pollTimer._idleTimeout, 10000);
  } finally { h.dispose(); }
});

test('refreshes a visible view on server changes and stays idle while hidden', async () => {
  const h = harness();
  try {
    h.changeConfig('loopforge.serverUrl');
    assert.equal(h.requests.length, 0);
    h.setVisible(true);
    await new Promise(resolve => setImmediate(resolve));
    const before = h.requests.length;
    h.changeConfig('loopforge.serverUrl');
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(h.requests.length, before + 1);
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

// Serves the run-workspace endpoints on top of the harness server so the
// diff/file commands can be exercised; full URLs are kept to check queries.
function withWorkspace(h, workspace, { diff = {}, files = {} } = {}) {
  const base = globalThis.fetch, urls = [];
  globalThis.fetch = async (url, options) => {
    const parsed = new URL(url);
    urls.push(parsed.pathname + parsed.search);
    const json = body => new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } });
    if (parsed.pathname === '/api/runs/run-1/workspace') return json(workspace);
    if (parsed.pathname === '/api/runs/run-1/workspace/diff') return json(diff[parsed.searchParams.get('path') || ''] || { diff: '' });
    if (parsed.pathname === '/api/runs/run-1/workspace/file') return json(files[parsed.searchParams.get('path')] || {});
    return base(url, options);
  };
  return urls;
}

test('openDiff shows a side-by-side diff for a single changed file', async () => {
  const h = harness();
  try {
    await h.commands.get('loopforge.startWorkflow')();
    const urls = withWorkspace(h, { available: true, changes: [{ path: 'repo/src/a.go', repository: 'repo' }] }, { diff: { 'repo/src/a.go': { original: 'old', modified: 'new' } } });
    await h.commands.get('loopforge.openDiff')();
    assert.ok(urls.includes('/api/runs/run-1/workspace/diff?path=repo%2Fsrc%2Fa.go'));
    assert.deepEqual(h.documents.slice(-2), [{ language: 'go', content: 'old' }, { language: 'go', content: 'new' }]);
    const diffCommand = h.requests.find(item => item.command?.[0] === 'vscode.diff');
    assert.equal(diffCommand.command[3], 'LoopForge: repo/src/a.go');
    assert.deepEqual(h.errors, []);
  } finally { h.dispose(); }
});

test('openDiff asks which change to show and stops when the pick is cancelled', async () => {
  const h = harness();
  try {
    await h.commands.get('loopforge.startWorkflow')();
    const changes = [{ path: 'a/x.md', repository: 'a' }, { path: 'b/y.json', repository: 'b' }];
    let urls = withWorkspace(h, { available: true, changes }, { diff: { 'b/y.json': { original: '{}', modified: '{"a":1}' } } });
    h.setPicks([{ change: changes[1] }]);
    await h.commands.get('loopforge.openDiff')();
    assert.ok(urls.includes('/api/runs/run-1/workspace/diff?path=b%2Fy.json'));
    assert.equal(h.documents.at(-1).language, 'json');

    const before = h.documents.length;
    urls = withWorkspace(h, { available: true, changes });
    // showQuickPick resolves undefined when the user dismisses the picker.
    const pick = h.vscode.window.showQuickPick;
    h.vscode.window.showQuickPick = async () => undefined;
    try { await h.commands.get('loopforge.openDiff')(); } finally { h.vscode.window.showQuickPick = pick; }
    assert.ok(!urls.some(url => url.includes('/workspace/diff')));
    assert.equal(h.documents.length, before);
  } finally { h.dispose(); }
});

test('openDiff falls back to the whole-workspace diff when no file is listed', async () => {
  const h = harness();
  try {
    await h.commands.get('loopforge.startWorkflow')();
    const urls = withWorkspace(h, { available: true, changes: [] }, { diff: { '': { diff: 'diff --git a b' } } });
    const before = h.documents.length;
    await h.commands.get('loopforge.openDiff')();
    assert.ok(urls.includes('/api/runs/run-1/workspace/diff'));
    assert.equal(h.documents.length, before);
    assert.deepEqual(h.errors, []);
  } finally { h.dispose(); }
});

for (const command of ['openDiff', 'openFile']) {
  test(`${command} reports an unavailable workspace with the server reason`, async () => {
    const h = harness();
    try {
      await h.commands.get('loopforge.startWorkflow')();
      withWorkspace(h, { available: false, reason: '工作区已清理' });
      await h.commands.get(`loopforge.${command}`)();
      assert.deepEqual(h.errors, ['工作区已清理']);
      withWorkspace(h, { available: false });
      await h.commands.get(`loopforge.${command}`)();
      assert.equal(h.errors[1], 'Run workspace is unavailable.');
    } finally { h.dispose(); }
  });
}

test('openFile opens the only changed file and rejects runs without changes', async () => {
  const h = harness();
  try {
    await h.commands.get('loopforge.startWorkflow')();
    let urls = withWorkspace(h, { available: true, changes: [{ path: 'repo/main.py' }] }, { files: { 'repo/main.py': { content: 'print(1)' } } });
    await h.commands.get('loopforge.openFile')();
    assert.ok(urls.includes('/api/runs/run-1/workspace/file?path=repo%2Fmain.py'));
    assert.ok(h.documents.some(doc => doc.language === 'python' && doc.content === 'print(1)'));

    urls = withWorkspace(h, { available: true, changes: [] });
    await h.commands.get('loopforge.openFile')();
    assert.deepEqual(h.errors, ['This run has no readable changed files.']);
    assert.ok(!urls.some(url => url.includes('/workspace/file')));
  } finally { h.dispose(); }
});

test('openFile lets the user choose among several changed files', async () => {
  const h = harness();
  try {
    await h.commands.get('loopforge.startWorkflow')();
    const changes = [{ path: 'a/one.sh' }, { path: 'b/two.yml' }];
    const urls = withWorkspace(h, { available: true, changes }, { files: { 'b/two.yml': { content: 'k: v' } } });
    h.setPicks([{ change: changes[1] }]);
    await h.commands.get('loopforge.openFile')();
    assert.ok(urls.includes('/api/runs/run-1/workspace/file?path=b%2Ftwo.yml'));
    assert.ok(h.documents.some(doc => doc.language === 'yaml' && doc.content === 'k: v'));
  } finally { h.dispose(); }
});

test('configureToken stores a trimmed token, clears on empty input and ignores cancel', async () => {
  const h = harness();
  try {
    h.setInput(['  lf_token  ', '   ', undefined]);
    await h.commands.get('loopforge.configureToken')();
    await h.commands.get('loopforge.configureToken')();
    await h.commands.get('loopforge.configureToken')();
    assert.deepEqual(h.secretOps, [['store', 'loopforge.apiToken', 'lf_token'], ['delete', 'loopforge.apiToken']]);
    assert.deepEqual(h.messages, ['LoopForge API Token 已安全保存。', 'LoopForge API Token 已清除。']);
  } finally { h.dispose(); }
});
