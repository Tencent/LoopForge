// SOP designer's AI orchestrator. The conversation runs in the chatbot's
// sop-designer surface (/chatbot/?surface=sop-designer), embedded in the
// canvas side panel; this file owns the canvas: it hands the chat the graph
// to edit and replays the streamed draft as nodes and wires on the canvas.
let aiBuildToken = 0;
let aiBuild = null;

const AI_FRAME_PATH = '/chatbot/';
const AI_MESSAGES = {
  context: 'agentflow-sop-designer-context',
  begun: 'agentflow-sop-designer-begun',
  newConversation: 'agentflow-sop-designer-new',
  ready: 'agentflow-sop-designer-ready',
  begin: 'agentflow-sop-designer-begin',
  draft: 'agentflow-sop-designer-draft',
  reply: 'agentflow-sop-designer-reply',
  end: 'agentflow-sop-designer-end',
  place: 'agentflow-sop-designer-place',
  role: 'agentflow-sop-designer-role',
  reset: 'agentflow-sop-designer-reset'
};

function layoutAIGraph(nodes, edges) {
  const ids = nodes.map(node => node.id);
  const indegree = new Map(ids.map(id => [id, 0]));
  const next = new Map(ids.map(id => [id, []]));
  edges.forEach(edge => {
    if (!indegree.has(edge.source) || !indegree.has(edge.target)) return;
    indegree.set(edge.target, indegree.get(edge.target) + 1);
    next.get(edge.source).push(edge.target);
  });
  const depth = new Map(ids.map(id => [id, 0]));
  const queue = ids.filter(id => indegree.get(id) === 0);
  const pending = new Map(indegree);
  while (queue.length) {
    const id = queue.shift();
    for (const target of next.get(id) || []) {
      depth.set(target, Math.max(depth.get(target) || 0, (depth.get(id) || 0) + 1));
      pending.set(target, pending.get(target) - 1);
      if (pending.get(target) === 0) queue.push(target);
    }
  }
  const rows = new Map();
  nodes.forEach(node => {
    const column = depth.get(node.id) || 0;
    const row = rows.get(column) || 0;
    rows.set(column, row + 1);
    const positioned = node.position && Number.isFinite(node.position.x) && Number.isFinite(node.position.y);
    if (!positioned) node.position = {x: 80 + column * 280, y: 80 + row * 160};
  });
  return nodes;
}

function normalizeAIWorkflow(raw) {
  const sourceNodes = Array.isArray(raw.nodes) ? raw.nodes : [];
  const nodes = sourceNodes.map((node, index) => normalizeAINode(node, index));
  const known = new Set(nodes.map(node => node.id));
  const edges = (Array.isArray(raw.edges) ? raw.edges : []).filter(edge => known.has(edge.source) && known.has(edge.target) && edge.source !== edge.target).map(normalizeAIEdge);
  layoutAIGraph(nodes, edges);
  return {nodes, edges};
}

function normalizeAINode(node, index) {
  const pos = node.position && Number.isFinite(node.position.x) && Number.isFinite(node.position.y) ? {x: node.position.x, y: node.position.y} : undefined;
  return {...node, id: node.id || uid('node'), name: node.name || node.type || ('步骤 ' + (index + 1)), type: node.type || 'agent', position: pos};
}

function normalizeAIEdge(edge) {
  return {...edge, id: edge.id || uid('edge'), when: edge.when || 'success'};
}

// ---- 流式草稿解析：从 submit_assistant_reply 的参数增量里挑出已完整的节点/连线 ----

function aiDecodePartialString(text, start) {
  let out = '';
  for (let i = start; i < text.length; i++) {
    const ch = text[i];
    if (ch === '"') return out;
    if (ch !== '\\') { out += ch; continue; }
    const nx = text[i + 1];
    if (nx === undefined) return out;
    if (nx === 'u') {
      const hex = text.slice(i + 2, i + 6);
      if (hex.length < 4) return out;
      out += String.fromCharCode(parseInt(hex, 16));
      i += 5;
      continue;
    }
    out += {n: '\n', t: '\t', r: '\r', b: '\b', f: '\f'}[nx] ?? nx;
    i++;
  }
  return out;
}

// Body may arrive as an object or, from some models, as a JSON string.
function aiDraftBodyText(buffer) {
  const m = /"body"\s*:\s*"/.exec(buffer);
  if (m && !/"body"\s*:\s*\{/.test(buffer.slice(0, m.index + m[0].length + 1))) return aiDecodePartialString(buffer, m.index + m[0].length);
  return buffer;
}

function aiExtractArray(text, key) {
  const re = new RegExp('"' + key + '"\\s*:\\s*\\[', 'g');
  let m;
  while ((m = re.exec(text))) {
    if (text[m.index - 1] !== '\\') break;
  }
  const items = [];
  if (!m) return {items, closed: false};
  let depth = 0, start = -1, inStr = false, esc = false;
  for (let i = m.index + m[0].length; i < text.length; i++) {
    const ch = text[i];
    if (inStr) {
      if (esc) esc = false;
      else if (ch === '\\') esc = true;
      else if (ch === '"') inStr = false;
      continue;
    }
    if (ch === '"') { inStr = true; continue; }
    if (ch === '{' || ch === '[') {
      if (depth === 0 && ch === '{') start = i;
      depth++;
      continue;
    }
    if (ch === '}' || ch === ']') {
      if (depth === 0) return {items, closed: true};
      depth--;
      if (depth === 0 && start >= 0) {
        try { items.push(JSON.parse(text.slice(start, i + 1))); } catch {}
        start = -1;
      }
    }
  }
  return {items, closed: false};
}

function aiExtractName(text) {
  const nodesAt = text.search(/"nodes"\s*:/);
  const head = nodesAt >= 0 ? text.slice(0, nodesAt) : text;
  const all = [...head.matchAll(/"name"\s*:\s*"((?:[^"\\]|\\.)*)"/g)];
  if (!all.length) return '';
  try { return JSON.parse('"' + all[all.length - 1][1] + '"'); } catch { return ''; }
}

function parseStreamingWorkflow(buffer) {
  const text = aiDraftBodyText(buffer);
  return {name: aiExtractName(text), nodes: aiExtractArray(text, 'nodes').items, edges: aiExtractArray(text, 'edges').items};
}

// An edit streams operations ({"op":"add_node",…}); place each added node and
// wire as soon as its operation is complete.
function parseStreamingOperations(buffer) {
  const ops = aiExtractArray(aiDraftBodyText(buffer), 'operations').items;
  return {
    name: '',
    nodes: ops.filter(op => op?.op === 'add_node' && op.node?.id).map(op => op.node),
    edges: ops.filter(op => op?.op === 'add_edge' && op.source && op.target).map(op => ({source: op.source, target: op.target, when: op.when || 'success'}))
  };
}

// ---- 面板：嵌入对话，转发画布上下文 ----

function shellForAIWorkflow(raw) {
  const existing = (state.data?.workflows || []).find(item => item.id && item.id === raw.id);
  const shell = existing ? clone(existing) : {
    id: raw.id || uid('sop'),
    name: raw.name || 'AI 编排 SOP',
    version: 1,
    revision: 0,
    status: 'draft',
    projectId: raw.projectId || state.projectFilter || '',
    shared: false,
    owner: raw.owner || '',
    tags: Array.isArray(raw.tags) ? raw.tags : [],
    description: raw.description || '',
    sandboxProfileId: raw.sandboxProfileId || 'local',
    nodes: [],
    edges: []
  };
  shell.name = raw.name || shell.name;
  shell.description = raw.description || shell.description;
  if (!existing) shell.status = 'draft';
  shell.nodes = [];
  shell.edges = [];
  return shell;
}

function setAIBuildStatus(text) {
  const status = $('#aiBuildStatus');
  if (!status) return;
  status.hidden = !text;
  status.textContent = text || '';
}

function aiOrchestratorFrame() {
  return $('#aiOrchestratorFrame');
}

function aiOrchestratorContext() {
  return {projectId: state.projectFilter || state.workflow?.projectId || '', workflowId: state.workflow?.id || ''};
}

function postToAIOrchestrator(type, payload = {}) {
  const target = aiOrchestratorFrame()?.contentWindow;
  if (target) target.postMessage({type, ...payload}, location.origin);
}

// Load the chat on first open; afterwards only tell it which SOP is shown.
function syncAIOrchestratorFrame() {
  const frame = aiOrchestratorFrame();
  if (!frame || $('#aiOrchestrator')?.hidden !== false) return;
  const context = aiOrchestratorContext();
  if (!frame.dataset.loaded) {
    frame.dataset.loaded = '1';
    frame.src = AI_FRAME_PATH + '?surface=sop-designer&projectId=' + encodeURIComponent(context.projectId) + '&workflowId=' + encodeURIComponent(context.workflowId);
    frame.dataset.context = context.projectId + '/' + context.workflowId;
    return;
  }
  const key = context.projectId + '/' + context.workflowId;
  if (frame.dataset.context === key) return;
  frame.dataset.context = key;
  postToAIOrchestrator(AI_MESSAGES.context, context);
}

function setAIOrchestratorOpen(open) {
  const panel = $('#aiOrchestrator');
  if (!panel) return;
  panel.hidden = !open;
  if (typeof syncFlowDrawerLayout === 'function') syncFlowDrawerLayout();
  if (open) {
    syncAIOrchestratorFrame();
    aiOrchestratorFrame()?.focus();
  } else window.HuiPopover?.closeAll();
}

function openAIOrchestrator() {
  setAIOrchestratorOpen(true);
}

function abortAIBuild() {
  const build = aiBuild;
  const restore = isCurrentAIBuild(build);
  aiBuildToken++;
  aiBuild = null;
  if (restore) restoreAIBuildFailure(build);
  finishAIBuildVisuals();
  syncCanvasAIEntry();
  setAIBuildStatus('');
}

function resetAIOrchestrator() {
  abortAIBuild();
  postToAIOrchestrator(AI_MESSAGES.newConversation);
}

function syncCanvasAIEntry() {
  const entry = $('#canvasAIEntry');
  if (!entry) return;
  entry.hidden = !!aiBuild || !state.workflow || (state.workflow.nodes || []).length > 0;
}

// The chat asks before every turn: the canvas prepares the build and hands
// over the graph it shows, so the server edits exactly what the user sees.
async function beginAIOrchestratorTurn(requestId) {
  const reply = (payload) => postToAIOrchestrator(AI_MESSAGES.begun, {requestId, ...payload});
  if (!state.projectFilter) return reply({ok: false, error: '先在顶栏选择项目'});
  if (aiBuild) return reply({ok: false, error: '画布还在生成上一轮的流程，请稍候'});
  const context = aiOrchestratorContext();
  await refreshWorkflowAgents().catch(() => {});
  const current = aiOrchestratorContext();
  if (aiBuild || context.projectId !== current.projectId || context.workflowId !== current.workflowId) {
    return reply({ok: false, error: '画布已切换，请重试'});
  }
  const canvas = state.workflow ? aiClone(state.workflow) : null;
  const build = await beginAIOrchestration();
  if (!build) return reply({ok: false, error: '画布暂时不能编排'});
  reply({ok: true, projectId: state.projectFilter, workflowId: canvas?.id || state.workflow?.id || '', canvas});
}

function ingestAIDraftBuffer(buffer) {
  const build = aiBuild;
  if (!build || typeof buffer !== 'string') return;
  build.buffer = buffer;
  ingestStreamingWorkflow(build.mode === 'edit' ? parseStreamingOperations(buffer) : parseStreamingWorkflow(buffer));
}

async function handleAIOrchestratorMessage(event) {
  const frame = aiOrchestratorFrame();
  if (!frame || event.source !== frame.contentWindow || event.origin !== location.origin) return;
  const data = event.data || {};
  switch (data.type) {
    case AI_MESSAGES.ready:
      frame.dataset.context = '';
      syncAIOrchestratorFrame();
      return;
    case AI_MESSAGES.begin:
      return beginAIOrchestratorTurn(data.requestId);
    case AI_MESSAGES.draft:
      return ingestAIDraftBuffer(data.buffer);
    case AI_MESSAGES.reply:
      if (data.draft?.tool === 'agents_save') {
        abortAIBuild();
        return;
      }
      return finishAIOrchestration(data.draft?.body || null);
    case AI_MESSAGES.role:
      if (!aiBuild && data.projectId === state.projectFilter && data.workflowId === state.workflow?.id) {
        configureAIRoleDraft(data.role);
      }
      return;
    case AI_MESSAGES.end:
      abortAIBuild();
      if (data.error) setAIBuildStatus(data.error);
      return;
    case AI_MESSAGES.place:
      if (!aiBuild && data.draft) await playAIDraft(data.draft);
      return;
    case AI_MESSAGES.reset:
      if (aiBuild) abortAIBuild();
      return;
  }
}

function bindOrchestrator() {
  const open = $('#openAIOrchestrator');
  if (!open || open.dataset.bound) return;
  open.dataset.bound = '1';
  open.onclick = () => setAIOrchestratorOpen($('#aiOrchestrator')?.hidden !== false);
  $('#closeAIOrchestrator').onclick = () => setAIOrchestratorOpen(false);
  $('#aiOrchestratorReset').onclick = resetAIOrchestrator;
  $('#canvasAIEntry').onclick = openAIOrchestrator;
  window.addEventListener('message', event => { void handleAIOrchestratorMessage(event); });
  if (typeof renderCanvas === 'function' && !renderCanvas.aiWrapped) {
    const base = renderCanvas;
    renderCanvas = function () {
      const out = base.apply(this, arguments);
      syncCanvasAIEntry();
      placeAIGhost();
      syncAIOrchestratorFrame();
      return out;
    };
    renderCanvas.aiWrapped = true;
  }
  syncCanvasAIEntry();
}

// ---- 画布构建：队列逐个放节点、画连线，镜头跟随 ----

function aiSleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms));
}

function aiCanvasFollow(x, y) {
  flowViewport.centerOn(x + 122, y + 60, {smooth: true});
}

function aiProvisionalPosition(index) {
  return {x: 80 + index * 280, y: 120};
}

function setAIGhostText(text) {
  if (!aiBuild) return;
  aiBuild.ghostText = text;
  placeAIGhost();
}

function placeAIGhost() {
  const host = $('#nodes');
  if (!host) return;
  host.querySelector('.ai-ghost-node')?.remove();
  if (!aiBuild || aiBuild.ghostHidden) return;
  const pos = aiProvisionalPosition(aiBuild.placed.size);
  const ghost = document.createElement('div');
  ghost.className = 'ai-ghost-node';
  ghost.style.left = pos.x + 'px';
  ghost.style.top = pos.y + 'px';
  ghost.innerHTML = `<i></i><i></i><span>${escapeHTML(aiBuild.ghostText || 'AI 正在构思…')}</span>`;
  host.append(ghost);
}

function isCurrentAIBuild(build) {
  return !!build && aiBuild === build && build.token === aiBuildToken
    && state.projectFilter === build.projectId && state.workflow?.id === build.workflowId;
}

async function startAIBuild(raw, opts = {}) {
  const occupied = !!state.workflow && ((state.workflow.nodes || []).length > 0 || state.workflowDirty);
  if (occupied && !opts.confirmed) {
    const ok = await afConfirm({
      title: '换成一条新流程',
      message: '当前画布会被整条替换。只想改其中几步时，直接说改哪里，不用清空。',
      confirmText: '替换当前画布',
      danger: !!state.workflowDirty
    });
    if (!ok) return null;
  }
  const token = ++aiBuildToken;
  finishAIBuildVisuals();
  const previous = state.workflow ? aiClone(state.workflow) : null;
  const selectedNode = state.selectedNode;
  state.workflow = shellForAIWorkflow(raw || {});
  aiBuild = {
    token, projectId: state.projectFilter, workflowId: state.workflow.id, selectedNode,
    buffer: '', thought: '', placed: new Map(), wired: new Set(), pendingEdges: [], queue: [], draining: null,
    ghostText: opts.ghost || 'AI 正在理解你的需求…', ghostHidden: false, freePositions: true,
    mode: 'create', previous
  };
  state.selectedNode = null;
  if ((state.currentView || activeViewName()) !== 'flow') switchView('flow');
  renderWorkflowMeta();
  renderCanvas();
  renderProperties();
  openAIOrchestrator();
  $('#canvas')?.classList.add('ai-building');
  flowViewport.reset();
  return aiBuild;
}

function ingestStreamingWorkflow(graph) {
  const build = aiBuild;
  if (!isCurrentAIBuild(build)) return;
  if (graph.name && state.workflow && state.workflow.name !== graph.name) {
    state.workflow.name = graph.name;
    renderWorkflowMeta();
  }
  graph.nodes.forEach((node, index) => {
    const id = node?.id || '';
    if (!id || build.placed.has(id) || build.queue.some(item => item.kind === 'node' && item.data.id === id)) return;
    if (node.position && Number.isFinite(node.position.x)) build.freePositions = false;
    build.queue.push({kind: 'node', data: normalizeAINode(node, index)});
  });
  graph.edges.forEach(edge => {
    if (!edge.source || !edge.target || edge.source === edge.target) return;
    const key = edge.source + '→' + edge.target;
    if (build.wired.has(key) || build.pendingEdges.some(item => item.key === key) || build.queue.some(item => item.kind === 'edge' && item.key === key)) return;
    build.pendingEdges.push({key, data: normalizeAIEdge(edge)});
  });
  drainAIBuild();
}

function drainAIBuild() {
  const build = aiBuild;
  if (!build || build.draining) return build?.draining;
  build.draining = (async () => {
    // Yield first so the assignment above lands before the loop can finish and clear it.
    await Promise.resolve();
    try {
      while (isCurrentAIBuild(build)) {
        const ready = build.pendingEdges.findIndex(item => build.placed.has(item.data.source) && build.placed.has(item.data.target));
        if (ready >= 0) {
          const [item] = build.pendingEdges.splice(ready, 1);
          build.queue.push({kind: 'edge', key: item.key, data: item.data});
        }
        const item = build.queue.shift();
        if (!item) break;
        if (item.kind === 'node') await aiPlaceNode(build, item.data);
        else await aiWireEdge(build, item.data, item.key);
      }
    } finally {
      build.draining = null;
    }
  })();
  return build.draining;
}

async function aiPlaceNode(build, node) {
  if (!node.position) node.position = aiProvisionalPosition(build.placed.size);
  state.workflow.nodes.push(node);
  build.placed.set(node.id, node);
  if ($('#aiOrchestrator')?.hidden === false) state.selectedNode = node.id;
  renderCanvas();
  const el = document.querySelector('.flow-node[data-id="' + (CSS.escape ? CSS.escape(node.id) : node.id) + '"]');
  el?.classList.add('ai-just-built');
  aiCanvasFollow(node.position.x, node.position.y);
  setAIBuildStatus('已建 ' + build.placed.size + ' 个节点：' + node.name);
  setAIGhostText('AI 正在写下一个节点…');
  await aiSleep(420);
}

function aiDrawEdge(edgeId) {
  const path = document.querySelector('[data-edge="' + (CSS.escape ? CSS.escape(edgeId) : edgeId) + '"] .edge');
  if (!path || typeof path.getTotalLength !== 'function') return;
  const len = path.getTotalLength();
  path.classList.add('ai-just-wired');
  path.style.strokeDasharray = len + ' ' + len;
  path.style.strokeDashoffset = String(len);
  path.getBoundingClientRect();
  path.style.transition = 'stroke-dashoffset .5s ease-out';
  path.style.strokeDashoffset = '0';
  setTimeout(() => { path.style.strokeDasharray = ''; path.style.strokeDashoffset = ''; path.style.transition = ''; }, 700);
}

async function aiWireEdge(build, edge, key) {
  build.wired.add(key);
  state.workflow.edges.push(edge);
  renderEdges();
  aiDrawEdge(edge.id);
  const from = build.placed.get(edge.source);
  const to = build.placed.get(edge.target);
  setAIBuildStatus('连线 ' + (from?.name || edge.source) + ' → ' + (to?.name || edge.target));
  await aiSleep(280);
}

// Glide every node to its final layered position, then redraw the wires.
async function aiRelayout(build) {
  const nodes = state.workflow.nodes;
  const target = nodes.map(node => ({...node, position: build.freePositions ? undefined : node.position}));
  layoutAIGraph(target, state.workflow.edges);
  const moved = target.some((node, i) => node.position.x !== nodes[i].position.x || node.position.y !== nodes[i].position.y);
  if (!moved) return;
  const canvas = $('#canvas');
  canvas?.classList.add('ai-relayout');
  target.forEach((node, i) => {
    const el = document.querySelector('.flow-node[data-id="' + (CSS.escape ? CSS.escape(node.id) : node.id) + '"]');
    if (el) { el.style.left = node.position.x + 'px'; el.style.top = node.position.y + 'px'; }
    nodes[i].position = node.position;
  });
  await aiSleep(620);
  canvas?.classList.remove('ai-relayout');
  renderCanvas();
}

// Fit the whole flow into view once it has been built.
async function aiOverview() {
  if (!(state.workflow?.nodes || []).length) return;
  flowViewport.fit({smooth: true});
  await aiSleep(600);
}

function finishAIBuildVisuals() {
  const canvas = $('#canvas');
  canvas?.classList.remove('ai-building', 'ai-relayout');
  $('#nodes')?.querySelector('.ai-ghost-node')?.remove();
  document.querySelectorAll('.ai-live-thought.live').forEach(box => box.classList.remove('live'));
}

async function completeAIBuild(build, finalRaw) {
  if (build.mode === 'edit') return completeAIEdit(build, finalRaw);
  if (finalRaw && Array.isArray(finalRaw.nodes)) {
    ingestStreamingWorkflow({name: finalRaw.name || '', nodes: finalRaw.nodes, edges: finalRaw.edges || []});
  }
  await drainAIBuild();
  if (!isCurrentAIBuild(build)) return false;
  if (finalRaw && Array.isArray(finalRaw.nodes)) {
    const finalById = new Map(finalRaw.nodes.filter(n => n.id).map(n => [n.id, n]));
    state.workflow.nodes.forEach(node => {
      const fin = finalById.get(node.id);
      if (fin) Object.assign(node, {...fin, position: node.position});
    });
    if (finalRaw.description) state.workflow.description = finalRaw.description;
    if (finalRaw.preview) state.workflow.preview = aiClone(finalRaw.preview);
  }
  if (!await validateAIBuildRoles(build)) return false;
  build.ghostHidden = true;
  placeAIGhost();
  if (!state.workflow.nodes.length) {
    finishAIBuildVisuals();
    aiBuild = null;
    syncCanvasAIEntry();
    return false;
  }
  setAIBuildStatus('整理布局…');
  await aiRelayout(build);
  if (!isCurrentAIBuild(build)) return false;
  const canvas = $('#canvas');
  finishAIBuildVisuals();
  canvas?.classList.add('ai-done');
  setTimeout(() => canvas?.classList.remove('ai-done'), 1600);
  aiBuild = null;
  dirty();
  renderWorkflowMeta();
  renderCanvas();
  renderProperties();
  aiOverview();
  setAIBuildStatus(state.workflow.edges.length ? '节点和连线都已放好，确认后点保存' : '节点已放好，确认后点保存');
  return true;
}

async function validateAIBuildRoles(build) {
  const unavailable = () => state.workflow.nodes.filter(node => node.type === 'agent'
    && !(isPreviewBuildNode(node.id) && node.agentId === PREVIEW_BUILD_AGENT_ID)
    && !workflowAgent(node.agentId));
  if (!isCurrentAIBuild(build)) return false;
  if (!unavailable().length) return true;
  // A role may have been created or shared while the assistant was working.
  await refreshWorkflowAgents().catch(() => {});
  if (!isCurrentAIBuild(build)) return false;
  const missing = unavailable();
  if (!missing.length) return true;
  const names = missing.map(node => node.name + '（' + (node.agentId || '未绑定') + '）');
  abortAIBuild();
  setAIBuildStatus('草稿引用了当前流程不可用的角色：' + names.join('、') + '。请先创建角色，或选择已有角色。');
  return false;
}

function configureAIRoleDraft(role) {
  if (!role?.name || catalogGeneric(state.workflow)) return;
  const {id, name, description, prompt, executorId, model} = role;
  if (id && agentIdTaken(id)) return toast('这个角色标识已存在，请使用已保存的角色继续编排');
  openAgentWizard({id: '', name, description, prompt, executorId, model, projectId: state.projectFilter});
  if (id) {
    state.agentIdAuto = false;
    $('#agentForm').elements.id.value = id;
    syncAgentSlug();
  }
  setAgentCreateStep(2);
}

function aiClone(value) {
  return value == null ? value : JSON.parse(JSON.stringify(value));
}

// The server applies edit operations to the canvas it was given, so ids are
// stable: match by id and keep layout and scripts the model left out.
function mergeAIEdit(base, incoming) {
  const prevNodes = base?.nodes || [];
  const nodes = (Array.isArray(incoming?.nodes) ? incoming.nodes : []).map((raw, index) => {
    const normalized = normalizeAINode(raw, index);
    const prev = prevNodes.find(node => node.id === normalized.id);
    if (!prev) return normalized;
    const merged = {...prev, ...normalized};
    if (!raw.script && prev.script) merged.script = prev.script;
    if (!raw.position || !Number.isFinite(raw.position.x)) merged.position = prev.position;
    return merged;
  });
  const known = new Set(nodes.map(node => node.id));
  const edges = (Array.isArray(incoming?.edges) ? incoming.edges : []).map(normalizeAIEdge)
    .filter(edge => known.has(edge.source) && known.has(edge.target) && edge.source !== edge.target);
  const shell = {...(base || {}), ...(incoming || {}), nodes, edges};
  shell.name = incoming?.name || base?.name;
  return shell;
}

// A canvas with steps is edited in place; an empty one gets a new graph.
async function beginAIOrchestration() {
  const hasGraph = !!state.workflow && (state.workflow.nodes || []).length > 0;
  if (hasGraph) {
    const token = ++aiBuildToken;
    finishAIBuildVisuals();
    aiBuild = {
      token, projectId: state.projectFilter, workflowId: state.workflow.id, selectedNode: state.selectedNode,
      buffer: '', thought: '',
      placed: new Map(state.workflow.nodes.map(node => [node.id, node])),
      wired: new Set((state.workflow.edges || []).map(edge => edge.source + '→' + edge.target)),
      pendingEdges: [], queue: [], draining: null,
      ghostText: '', ghostHidden: true, freePositions: false,
      mode: 'edit', base: aiClone(state.workflow)
    };
    if ((state.currentView || activeViewName()) !== 'flow') switchView('flow');
    openAIOrchestrator();
    $('#canvas')?.classList.add('ai-building');
    setAIBuildStatus('正在按当前流程修改，画布先保持原样');
    return aiBuild;
  }
  const current = state.workflow;
  const build = await startAIBuild({
    id: current?.id,
    name: current?.name || 'AI 编排中…',
    projectId: current?.projectId,
    sandboxProfileId: current?.sandboxProfileId,
    revision: current?.revision,
    version: current?.version,
    owner: current?.owner,
    tags: current?.tags,
    description: current?.description,
    preview: current?.preview
  }, {confirmed: true, ghost: 'AI 正在理解你的需求…'});
  setAIBuildStatus('正在连接 AI…');
  placeAIGhost();
  return build;
}

async function completeAIEdit(build, finalRaw) {
  if (!isCurrentAIBuild(build)) return false;
  if (!finalRaw || !Array.isArray(finalRaw.nodes) || !finalRaw.nodes.length) {
    abortAIBuild();
    return false;
  }
  const merged = mergeAIEdit(build.base, finalRaw);
  const oldIds = new Set((build.base.nodes || []).map(node => node.id));
  const streamed = new Map((state.workflow.nodes || []).map(node => [node.id, node]));
  // Nodes already placed while the edit streamed keep where they landed.
  merged.nodes.forEach(node => {
    if (!node.position && streamed.get(node.id)?.position) node.position = streamed.get(node.id).position;
  });
  const kept = merged.nodes.filter(node => oldIds.has(node.id) || streamed.has(node.id));
  const keptIds = new Set(kept.map(node => node.id));
  const newcomers = merged.nodes.filter(node => !keptIds.has(node.id));
  const immediateEdges = merged.edges.filter(edge => keptIds.has(edge.source) && keptIds.has(edge.target));
  const laterEdges = merged.edges.filter(edge => !keptIds.has(edge.source) || !keptIds.has(edge.target));
  state.workflow = {...merged, nodes: kept.slice(), edges: immediateEdges.slice()};
  build.placed = new Map(kept.map(node => [node.id, node]));
  build.wired = new Set(immediateEdges.map(edge => edge.source + '→' + edge.target));
  build.queue = [];
  build.pendingEdges = [];
  renderWorkflowMeta();
  renderCanvas();
  renderProperties();
  if (newcomers.length || laterEdges.length) {
    setAIBuildStatus('补上新的步骤…');
    ingestStreamingWorkflow({name: merged.name, nodes: newcomers, edges: laterEdges});
    await drainAIBuild();
  }
  if (!await validateAIBuildRoles(build)) return false;
  build.ghostHidden = true;
  placeAIGhost();
  finishAIBuildVisuals();
  aiBuild = null;
  dirty();
  renderWorkflowMeta();
  renderCanvas();
  renderProperties();
  setAIBuildStatus('已改在当前流程上，确认后点保存');
  return (state.workflow.nodes || []).length > 0;
}

function restoreAIBuildFailure(build) {
  if (!build) return;
  state.workflow = build.mode === 'edit' ? build.base : build.previous;
  state.selectedNode = build.selectedNode;
  renderWorkflowMeta();
  renderCanvas();
  renderProperties();
}

function aiDraftObject(raw) {
  if (typeof raw !== 'string') return raw && typeof raw === 'object' ? raw : null;
  try { return JSON.parse(raw); } catch { return null; }
}

// The turn's reply arrived. An edit keeps the SOP's id; a body with another
// id is a new SOP and replaces the canvas only after the user agrees.
async function finishAIOrchestration(raw) {
  const build = aiBuild;
  if (!isCurrentAIBuild(build)) {
    if (build) abortAIBuild();
    return false;
  }
  const body = aiDraftObject(raw);
  if (!body) {
    abortAIBuild();
    return false;
  }
  if (build.mode === 'edit' && body?.id && build.base?.id && body.id !== build.base.id && Array.isArray(body.nodes)) {
    abortAIBuild();
    return playWorkflowOnCanvas(body, {confirmed: false});
  }
  const ok = await completeAIBuild(build, body);
  if (!ok && build.token === aiBuildToken) setAIBuildStatus(build.mode === 'edit' ? '这轮没有改动画布，当前流程还在' : '这轮没有生成可放置的 SOP，可以换个说法再试');
  return ok;
}

// "重新放到画布" on an earlier reply.
async function playAIDraft(raw) {
  const body = aiDraftObject(raw);
  if (!body) return false;
  if (state.workflow && body.id && body.id === state.workflow.id && (state.workflow.nodes || []).length) {
    const build = await beginAIOrchestration();
    return build ? completeAIEdit(build, body) : false;
  }
  return playWorkflowOnCanvas(body, {confirmed: false});
}

// Replays a finished draft (e.g. from the side assistant) with the same builder.
async function playWorkflowOnCanvas(raw, opts = {}) {
  const graph = normalizeAIWorkflow(raw || {});
  if (!graph.nodes.length) {
    toast('AI 没有给出可放置的节点');
    return false;
  }
  const build = await startAIBuild(raw || {}, {confirmed: opts.confirmed, ghost: 'AI 正在放置节点…'});
  if (!build) return false;
  const ok = await completeAIBuild(build, raw);
  if (ok && opts.source === 'assistant') toast('AI 已在画布上逐个放好节点');
  return ok;
}

bindOrchestrator();
