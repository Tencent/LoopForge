'use strict';

const vscode = require('vscode');
const path = require('path');
const { LoopForgeClient, LoopForgeError } = require('./lib/client');

const CONFIG = 'loopforge';
const TOKEN_KEY = 'loopforge.apiToken';

function config() { return vscode.workspace.getConfiguration(CONFIG); }
function configured(name, fallback = '') { return config().get(name, fallback); }
function workspaceFolder() {
  const uri = vscode.window.activeTextEditor?.document.uri;
  return (uri && vscode.workspace.getWorkspaceFolder(uri)) || vscode.workspace.workspaceFolders?.[0];
}
function workspaceName() {
  const folder = workspaceFolder();
  return folder ? (folder.name || path.basename(folder.uri.fsPath)) : '当前项目';
}
function trimText(value, max = 4000) {
  const text = String(value || '').trim();
  return text.length > max ? `${text.slice(0, max)}\n…(已截断)` : text;
}
function selectedContext() {
  const editor = vscode.window.activeTextEditor;
  if (!editor) return { file: '', selection: '' };
  const file = vscode.workspace.asRelativePath(editor.document.uri, false);
  const selected = editor.selection && !editor.selection.isEmpty ? editor.document.getText(editor.selection) : '';
  const selection = selected.length > 4000 ? `${selected.slice(0, 4000)}\n…(已截断)` : selected;
  return { file, selection };
}
function languageFor(name) {
  const ext = path.extname(String(name || '')).toLowerCase();
  return { '.md': 'markdown', '.json': 'json', '.js': 'javascript', '.ts': 'typescript', '.py': 'python', '.go': 'go', '.css': 'css', '.html': 'html', '.yaml': 'yaml', '.yml': 'yaml', '.sh': 'shell' }[ext] || 'plaintext';
}
function runStatusLabel(run) {
  const status = run?.status || 'unknown';
  const nodes = Object.values(run?.nodes || {});
  const active = nodes.find(node => ['running', 'waiting_approval', 'waiting'].includes(node?.status));
  const stage = active ? `${active.nodeId || 'stage'} · ${active.status}` : '';
  return stage ? `${status} · ${stage}` : status;
}
function waitingApproval(run) {
  for (const node of Object.values(run?.nodes || {})) {
    if (node && node.status === 'waiting_approval') return node;
  }
  return null;
}

class RunTreeItem extends vscode.TreeItem {
  constructor(run, kind = 'run', parent) {
    const label = kind === 'run' ? `${run.id} · ${runStatusLabel(run)}` : run;
    super(label, kind === 'run' ? vscode.TreeItemCollapsibleState.Collapsed : vscode.TreeItemCollapsibleState.None);
    this.run = kind === 'run' ? run : parent?.run;
    this.kind = kind;
    this.parent = parent;
    this.contextValue = `loopforge.${kind}`;
    this.tooltip = kind === 'run' ? `${run.workflowId || run.workflowSnapshot?.name || 'workflow'}\n${run.input || ''}` : label;
    if (kind === 'run') {
      this.description = run.workflowSnapshot?.name || run.workflowId || '';
      this.iconPath = new vscode.ThemeIcon(run.status === 'succeeded' ? 'pass' : run.status === 'failed' ? 'error' : 'sync~spin');
      this.command = { command: 'loopforge.showStatus', title: 'Show Status', arguments: [this] };
    } else if (kind === 'approval') {
      this.description = '需要人工确认';
      this.iconPath = new vscode.ThemeIcon('question');
      this.command = { command: 'loopforge.approve', title: 'Approve', arguments: [this] };
    } else {
      const artifact = parent?.artifact;
      this.description = `${artifact?.mediaType || 'text'} · ${artifact?.size || 0} bytes`;
      this.iconPath = new vscode.ThemeIcon('file');
      this.command = { command: 'loopforge.openArtifact', title: 'Open Artifact', arguments: [this] };
    }
  }
}

class LoopForgeTreeProvider {
  constructor(state) { this.state = state; this.emitter = new vscode.EventEmitter(); this.onDidChangeTreeData = this.emitter.event; }
  refresh() { this.emitter.fire(); }
  getTreeItem(item) { return item; }
  async getChildren(item) {
    if (!item) {
      if (this.state.lastError) return [new vscode.TreeItem(`连接失败：${this.state.lastError}`)];
      const runs = this.state.runs || [];
      if (!runs.length) return [new vscode.TreeItem('没有最近任务。运行 “LoopForge: Start Workflow” 开始。')];
      return runs.map(run => new RunTreeItem(run));
    }
    if (item.kind !== 'run') return [];
    try {
      item.run = await this.state.detail(item, false);
    } catch (error) {
      this.state.error(error);
      return [new vscode.TreeItem(`读取任务详情失败：${error.message}`)];
    }
    const children = [];
    for (const approval of Object.values(item.run.nodes || {}).filter(node => node?.status === 'waiting_approval')) {
      const approvalItem = new RunTreeItem(`审批：${approval.nodeId || '当前阶段'}`, 'approval', item);
      // Keep the attempt displayed in the tree even if the run changes later.
      approvalItem.approval = { ...approval };
      children.push(approvalItem);
    }
    for (const artifact of item.run.artifacts || []) {
      const artifactItem = new RunTreeItem(artifact.name || artifact.id, 'artifact', item);
      artifactItem.artifact = artifact;
      artifactItem.description = `${artifact.mediaType || 'text'} · ${artifact.size || 0} bytes`;
      children.push(artifactItem);
    }
    if (!children.length) children.push(new vscode.TreeItem('暂无审批或产物'));
    return children;
  }
}

class ExtensionState {
  constructor(context) {
    this.context = context;
    this.runs = [];
    this.client = null;
    this.output = vscode.window.createOutputChannel('LoopForge');
    this.tree = new LoopForgeTreeProvider(this);
    this.refreshing = false;
    this.pollTimer = undefined;
    this.lastError = '';
  }
  async connect() {
    const token = await this.context.secrets.get(TOKEN_KEY) || '';
    this.client = new LoopForgeClient({ baseUrl: configured('serverUrl', 'http://127.0.0.1:8090'), token });
    return this.client;
  }
  async refresh({ quiet = false } = {}) {
    if (this.refreshing) return;
    this.refreshing = true;
    try {
      await this.connect();
      this.runs = await this.client.listRuns(configured('projectId', ''), Number(configured('maxRuns', 20)));
      this.lastError = '';
      this.tree.refresh();
      if (!quiet) vscode.window.setStatusBarMessage(`LoopForge: 已刷新 ${this.runs.length} 个任务`, 3000);
    } catch (error) {
      this.lastError = error.message;
      this.tree.refresh();
      if (!quiet) this.error(error);
      else this.output.appendLine(`Refresh failed: ${error.message}`);
    } finally { this.refreshing = false; }
  }
  error(error) {
    const message = error instanceof LoopForgeError ? error.message : `LoopForge error: ${error?.message || error}`;
    this.output.appendLine(message);
    vscode.window.showErrorMessage(message);
  }
  async detail(runOrItem, refreshTree = true) {
    await this.connect();
    const run = runOrItem?.run || runOrItem;
    if (!run?.id) throw new LoopForgeError('No LoopForge run is selected.');
    const detail = await this.client.getRun(run.id);
    const index = this.runs.findIndex(item => item.id === detail.id);
    if (index >= 0) this.runs[index] = detail;
    if (refreshTree) this.tree.refresh();
    return detail;
  }
  dispose() {
    if (this.pollTimer) clearInterval(this.pollTimer);
    this.output.dispose();
    this.tree.emitter.dispose();
  }
}

async function chooseProject(state, force = false) {
  const value = configured('projectId', '');
  if (value && !force) return value;
  await state.connect();
  const projects = await state.client.listProjects();
  const items = (Array.isArray(projects) ? projects : []).map(project => ({ label: project.name || project.id, description: project.id, project }));
  if (!items.length) throw new LoopForgeError('No projects found. Create a project in LoopForge first.');
  const selected = await vscode.window.showQuickPick(items, { placeHolder: '选择 LoopForge 项目' });
  return selected?.project?.id;
}
async function chooseWorkflow(state, projectId, force = false) {
  const value = configured('workflowId', '');
  if (value && !force) return value;
  await state.connect();
  const workflows = await state.client.listWorkflows();
  const items = (Array.isArray(workflows) ? workflows : []).filter(workflow => !projectId || !workflow.projectId || workflow.projectId === projectId).map(workflow => ({ label: workflow.name || workflow.id, description: workflow.id, workflow }));
  if (!items.length) throw new LoopForgeError('No workflows were returned by LoopForge. Configure loopforge.workflowId or publish a workflow first.');
  const selected = await vscode.window.showQuickPick(items, { placeHolder: '选择 LoopForge 工作流' });
  return selected?.workflow.id;
}
function workflowInput() {
  const context = selectedContext();
  const lines = [`在项目“${workspaceName()}”中执行 LoopForge 工作流。`];
  const folder = workspaceFolder();
  if (folder) lines.push(`项目路径：${folder.uri.fsPath}`);
  if (context.file) lines.push(`当前文件：${context.file}`);
  if (context.selection) lines.push(`选中代码：\n${context.selection}`);
  return lines.join('\n');
}

async function startWorkflow(state) {
  // Capture editor context before opening any UI, including in multi-root workspaces.
  const context = workflowInput();
  const projectId = await chooseProject(state);
  if (!projectId) return;
  const workflowId = await chooseWorkflow(state, projectId);
  if (!workflowId) return;
  const input = await vscode.window.showInputBox({ prompt: '描述要执行的 LoopForge 工作流（当前文件和选中代码将作为上下文附加）', ignoreFocusOut: true, validateInput: value => value.trim() ? undefined : '请输入工作流任务描述' });
  if (input === undefined) return;
  await state.connect();
  const run = await state.client.createRun({ workflowId, input: `${input.trim()}\n\n${context}`, projectId });
  state.output.appendLine(`Started run ${run.id} (${workflowId})`);
  await state.refresh({ quiet: true });
  vscode.window.showInformationMessage(`LoopForge 任务已启动：${run.id}`, '查看状态').then(action => { if (action === '查看状态') showStatus(state, { run }); });
}
async function showStatus(state, item) {
  try {
    const run = await state.detail(item || state.runs[0]);
    const lines = [`Run: ${run.id}`, `Workflow: ${run.workflowSnapshot?.name || run.workflowId}`, `Status: ${run.status}`, `Input: ${run.input || ''}`];
    for (const node of Object.values(run.nodes || {})) lines.push(`Stage ${node.nodeId}: ${node.status}${node.error ? ` — ${node.error}` : ''}`);
    if (run.status === 'waiting' || waitingApproval(run)) lines.push('Human action is required. Use the approval item in the LoopForge view.');
    state.output.clear(); state.output.appendLine(lines.join('\n')); state.output.show(true);
  } catch (error) { state.error(error); }
}
async function showResult(state, item) {
  try {
    const run = await state.detail(item || state.runs[0]);
    const logs = await state.client.getLogs(run.id).catch(error => ({ logs: [`Unable to load logs: ${error.message}`] }));
    const lines = [`Run ${run.id}`, `Status: ${run.status}`, '', 'Stages:'];
    for (const node of Object.values(run.nodes || {})) lines.push(`- ${node.nodeId}: ${node.status}${node.error ? `\n  Error: ${node.error}` : ''}${node.output ? `\n  ${trimText(node.output, 1000)}` : ''}`);
    if (Array.isArray(logs.logs)) lines.push('', 'Logs:', ...logs.logs.map(log => typeof log === 'string' ? log : `[${log.timestamp || ''}] ${log.body || log.message || ''}`));
    state.output.clear(); state.output.appendLine(lines.join('\n')); state.output.show(true);
  } catch (error) { state.error(error); }
}
async function approve(state, item) {
  try {
    const run = await state.detail(item || state.runs[0]);
    const node = item?.approval || waitingApproval(run);
    if (!node || node.status !== 'waiting_approval') throw new LoopForgeError('This run has no stage waiting for approval.');
    const context = await vscode.window.showInputBox({ prompt: `确认 ${node.nodeId}（第 ${node.attempt} 次执行，退回 ${node.reworkCount} 次），可附加说明`, value: '' });
    if (context === undefined) return;
    await state.client.approve(run.id, { nodeId: node.nodeId, context, expected: { status: node.status, attempt: node.attempt || 0, reworkCount: node.reworkCount || 0 } });
    vscode.window.showInformationMessage(`LoopForge 已确认 ${node.nodeId || '当前阶段'}`);
    await state.refresh({ quiet: true });
  } catch (error) { state.error(error); }
}
async function reject(state, item) {
  try {
    const run = await state.detail(item || state.runs[0]);
    const node = item?.approval || waitingApproval(run);
    if (!node || node.status !== 'waiting_approval') throw new LoopForgeError('This run has no stage waiting for a human decision.');
    const reason = await vscode.window.showInputBox({ prompt: `退回 ${node.nodeId}（第 ${node.attempt} 次执行，退回 ${node.reworkCount} 次）的原因`, validateInput: value => value.trim() ? undefined : '请输入原因' });
    if (reason === undefined) return;
    const workflowNode = (run.workflowSnapshot?.nodes || []).find(value => value.id === node.nodeId);
    await state.client.reject(run.id, { nodeId: node.nodeId, reason, targetId: workflowNode?.rejectTo || '', expected: { status: node.status, attempt: node.attempt || 0, reworkCount: node.reworkCount || 0 } });
    vscode.window.showInformationMessage(`LoopForge 已退回 ${node.nodeId || '当前阶段'}`);
    await state.refresh({ quiet: true });
  } catch (error) { state.error(error); }
}
async function openArtifact(state, item) {
  try {
    const run = await state.detail(item || state.runs[0]);
    let artifact = item?.artifact;
    if (!artifact) {
      if (!run.artifacts?.length) throw new LoopForgeError('This run has no artifacts.');
      const selected = await vscode.window.showQuickPick(run.artifacts.map(value => ({ label: value.name || value.id, artifact: value })), { placeHolder: '选择要打开的产物' });
      artifact = selected?.artifact;
    }
    if (!artifact) return;
    if (!artifact.id) throw new LoopForgeError('Artifact ID is missing from the server response.');
    const content = await state.client.getArtifact(run.id, artifact.id);
    const document = await vscode.workspace.openTextDocument({ language: languageFor(artifact.name), content: String(content || '') });
    await vscode.window.showTextDocument(document, { preview: false });
  } catch (error) { state.error(error); }
}
async function openDiff(state, item) {
  try {
    const run = await state.detail(item || state.runs[0]);
    const workspace = await state.client.getWorkspace(run.id);
    if (!workspace.available) throw new LoopForgeError(workspace.reason || 'Run workspace is unavailable.');
    const changes = workspace?.changes || [];
    let selectedPath = changes.length === 1 ? changes[0].path : undefined;
    if (!selectedPath && changes.length > 1) {
      const selected = await vscode.window.showQuickPick(changes.map(change => ({ label: change.path, description: change.repository || '', change })), { placeHolder: '选择要查看的变更' });
      if (!selected) return;
      selectedPath = selected?.change?.path;
    }
    if (!selectedPath) {
      const result = await state.client.getDiff(run.id, '');
      state.output.clear(); state.output.appendLine(result.diff || 'No diff available.'); state.output.show(true); return;
    }
    const diff = await state.client.getDiff(run.id, selectedPath);
    const original = await vscode.workspace.openTextDocument({ language: languageFor(selectedPath), content: String(diff.original || '') });
    const modified = await vscode.workspace.openTextDocument({ language: languageFor(selectedPath), content: String(diff.modified || '') });
    await vscode.commands.executeCommand('vscode.diff', original.uri, modified.uri, `LoopForge: ${selectedPath}`);
  } catch (error) { state.error(error); }
}
async function openFile(state, item) {
  try {
    const run = await state.detail(item || state.runs[0]);
    const workspace = await state.client.getWorkspace(run.id);
    if (!workspace.available) throw new LoopForgeError(workspace.reason || 'Run workspace is unavailable.');
    const changes = workspace?.changes || [];
    if (!changes.length) throw new LoopForgeError('This run has no readable changed files.');
    const selected = changes.length === 1
      ? { change: changes[0] }
      : await vscode.window.showQuickPick(changes.map(change => ({ label: change.path, description: change.repository || '', change })), { placeHolder: '选择要打开的运行文件' });
    if (!selected?.change?.path) return;
    const file = await state.client.getWorkspaceFile(run.id, selected.change.path);
    const document = await vscode.workspace.openTextDocument({ language: languageFor(selected.change.path), content: String(file.content || '') });
    await vscode.window.showTextDocument(document, { preview: false });
  } catch (error) { state.error(error); }
}
async function configureToken(state) {
  const token = await vscode.window.showInputBox({ prompt: '输入 LoopForge Personal API Token（保存在 VS Code SecretStorage）', password: true, ignoreFocusOut: true });
  if (token === undefined) return;
  if (!token.trim()) { await state.context.secrets.delete(TOKEN_KEY); vscode.window.showInformationMessage('LoopForge API Token 已清除。'); return; }
  await state.context.secrets.store(TOKEN_KEY, token.trim());
  vscode.window.showInformationMessage('LoopForge API Token 已安全保存。');
}
async function configureSetting(name, value) {
  await config().update(name, value, vscode.ConfigurationTarget.Workspace);
}

function activate(context) {
  const state = new ExtensionState(context);
  const view = vscode.window.createTreeView('loopforge.runs', { treeDataProvider: state.tree, showCollapseAll: true });
  const command = (name, fn) => context.subscriptions.push(vscode.commands.registerCommand(name, async (...args) => {
    try { return await fn(state, ...args); } catch (error) { state.error(error); }
  }));
  command('loopforge.startWorkflow', startWorkflow);
  command('loopforge.showStatus', showStatus);
  command('loopforge.showResult', showResult);
  command('loopforge.approve', approve);
  command('loopforge.reject', reject);
  command('loopforge.openArtifact', openArtifact);
  command('loopforge.openFile', openFile);
  command('loopforge.openDiff', openDiff);
  command('loopforge.refresh', state => state.refresh());
  command('loopforge.configureToken', configureToken);
  command('loopforge.selectProject', async state => {
    const project = await chooseProject(state, true);
    if (!project) return;
    if (project !== configured('projectId', '')) {
      // An explicit empty workspace value also masks any user-level default.
      await configureSetting('workflowId', '');
      await configureSetting('projectId', project);
      await state.refresh({ quiet: true });
      vscode.window.showInformationMessage('项目已切换，请重新选择工作流。');
    }
  });
  command('loopforge.selectWorkflow', async state => { const workflow = await chooseWorkflow(state, configured('projectId', ''), true); if (workflow) await configureSetting('workflowId', workflow); });
  context.subscriptions.push(view, { dispose: () => state.dispose() });
  state.refresh({ quiet: true });
  const interval = Math.max(3, Number(configured('pollInterval', 10))) * 1000;
  state.pollTimer = setInterval(() => state.refresh({ quiet: true }), interval);
}
function deactivate() {}

module.exports = { activate, deactivate, selectedContext, workflowInput, runStatusLabel, waitingApproval };
