let aiBuildToken = 0;
let aiBuild = null;
let aiAbort = null;
const aiHistory = {};

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

// ---- 场景推荐与面板 ----

const AI_ORCHESTRATE_SCENARIOS = [
  ['需求交付 · 澄清到放行', '搭一条需求交付流程：需求澄清、方案设计、编码实现、代码评审、人工放行，按顺序连起来'],
  ['缺陷修复 · 定位到确认', '搭一条缺陷修复流程：定位问题、修改代码、跑单测和 lint、人工确认'],
  ['发布前检查 · 变更清单', '搭一条发布前检查：整理变更说明、回归检查、人工批准发布'],
  ['PR 评审 · 自动预审', '搭一条 PR 评审流程：读取变更、AI 预审给出问题清单、人工复核'],
  ['前后端并行开发', '搭一条流程：需求拆解后，前端实现和后端实现并行，再汇合做联调测试和人工验收'],
  ['技术方案评审', '搭一条技术方案流程：调研现状、输出方案文档、架构评审、人工拍板'],
  ['线上问题复盘', '搭一条复盘流程：收集日志与时间线、根因分析、改进项整理、人工确认'],
  ['文档与测试补齐', '搭一条流程：阅读代码、补充单元测试、补充接口文档、评审']
];
let aiScenarioPage = 0;

function renderAIScenarios() {
  const host = $('#aiOrchestratorSuggest');
  if (!host) return;
  const size = 4;
  const pages = Math.ceil(AI_ORCHESTRATE_SCENARIOS.length / size);
  const start = (aiScenarioPage % pages) * size;
  host.innerHTML = AI_ORCHESTRATE_SCENARIOS.slice(start, start + size).map(([label, prompt]) => `<button type="button" data-ai-prompt="${escapeHTML(prompt)}">${escapeHTML(label)}</button>`).join('');
  host.querySelectorAll('[data-ai-prompt]').forEach(button => {
    button.onclick = () => {
      const input = $('#aiOrchestratorInput');
      input.value = button.dataset.aiPrompt;
      input.focus();
    };
  });
}

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

function setAIOrchestratorOpen(open) {
  const panel = $('#aiOrchestrator');
  if (!panel) return;
  panel.hidden = !open;
  panel.closest('.flow-workbench')?.classList.toggle('ai-open', open);
  const input = $('#aiOrchestratorInput');
  if (input && !input.value) {
    input.placeholder = (state.workflow?.nodes || []).length
      ? '直接说要改哪里，例如：补上构建静态站点，其它步骤保持不变'
      : '告诉我你的目标，我来搞定…';
  }
  if (open) input?.focus();
}

function openAIOrchestrator() {
  setAIOrchestratorOpen(true);
}

function resetAIOrchestrator() {
  aiBuildToken++;
  aiAbort?.abort();
  finishAIBuildVisuals();
  if (state.projectFilter) delete aiHistory[state.projectFilter];
  const log = $('#aiOrchestratorLog');
  const intro = $('#aiOrchestratorIntro');
  if (log) {
    [...log.children].forEach(item => { if (item !== intro) item.remove(); });
    if (intro) intro.hidden = false;
  }
  setAIBuildStatus('');
}

function syncCanvasAIEntry() {
  const entry = $('#canvasAIEntry');
  if (!entry) return;
  entry.hidden = !!aiBuild || !state.workflow || (state.workflow.nodes || []).length > 0;
}

function bindOrchestrator() {
  const open = $('#openAIOrchestrator');
  const form = $('#aiOrchestratorForm');
  if (!open || open.dataset.bound) return;
  open.dataset.bound = '1';
  open.onclick = () => setAIOrchestratorOpen($('#aiOrchestrator')?.hidden !== false);
  $('#closeAIOrchestrator').onclick = () => setAIOrchestratorOpen(false);
  $('#aiOrchestratorReset').onclick = resetAIOrchestrator;
  $('#aiSuggestMore').onclick = () => { aiScenarioPage++; renderAIScenarios(); };
  renderAIScenarios();
  $('#canvasAIEntry').onclick = openAIOrchestrator;
  if (typeof renderCanvas === 'function' && !renderCanvas.aiWrapped) {
    const base = renderCanvas;
    renderCanvas = function () {
      const out = base.apply(this, arguments);
      syncCanvasAIEntry();
      placeAIGhost();
      return out;
    };
    renderCanvas.aiWrapped = true;
  }
  syncCanvasAIEntry();
  const input = $('#aiOrchestratorInput');
  input.addEventListener('keydown', event => {
    if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
      event.preventDefault();
      runAIOrchestrator();
    }
  });
  form.onsubmit = event => {
    event.preventDefault();
    runAIOrchestrator();
  };
}

function appendOrchestratorNote(text, kind) {
  const log = $('#aiOrchestratorLog');
  if (!log) return null;
  const intro = $('#aiOrchestratorIntro');
  if (intro) intro.hidden = true;
  const item = document.createElement('p');
  item.className = kind || '';
  item.textContent = text;
  log.append(item);
  log.scrollTop = log.scrollHeight;
  return item;
}

function aiLiveThought() {
  const log = $('#aiOrchestratorLog');
  if (!log) return null;
  let box = log.querySelector('.ai-live-thought.live');
  if (!box) {
    box = document.createElement('div');
    box.className = 'ai-live-thought live';
    box.innerHTML = '<b><i></i>AI 正在思考</b><span></span>';
    log.append(box);
  }
  return box;
}

function pushAIThought(ev) {
  if (!aiBuild) return;
  switch (ev.kind) {
    case 'thinking':
    case 'answer': {
      const box = aiLiveThought();
      if (!box) return;
      aiBuild.thought = (aiBuild.thought + (ev.text || '')).slice(-600);
      box.querySelector('span').textContent = aiBuild.thought.slice(-220);
      $('#aiOrchestratorLog').scrollTop = 1e9;
      return;
    }
    case 'tool':
      appendOrchestratorNote('查询 ' + (ev.detail ? ev.tool + ' · ' + ev.detail : ev.tool), 'step tool');
      return;
    case 'status':
      if (ev.text) setAIBuildStatus(ev.text);
      return;
    case 'draftStart':
      setAIBuildStatus('AI 开始落笔，节点会一个个出现…');
      setAIGhostText('AI 正在写第一个节点…');
      return;
    case 'draftDelta':
      aiBuild.buffer += ev.detail || '';
      ingestStreamingWorkflow(parseStreamingWorkflow(aiBuild.buffer));
  }
}

// ---- 画布构建：队列逐个放节点、画连线，镜头跟随 ----

function aiSleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms));
}

function aiCanvasFollow(x, y) {
  const canvas = $('#canvas');
  if (!canvas) return;
  canvas.scrollTo({left: Math.max(0, x - canvas.clientWidth / 2 + 114), top: Math.max(0, y - canvas.clientHeight / 2 + 46), behavior: 'smooth'});
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
  aiBuild = {token, buffer: '', thought: '', placed: new Map(), wired: new Set(), pendingEdges: [], queue: [], draining: null, ghostText: opts.ghost || 'AI 正在理解你的需求…', ghostHidden: false, freePositions: true, mode: 'create', previous, spawned: 0, goal: opts.goal || '', referenced: opts.referenced || []};
  state.workflow = shellForAIWorkflow(raw || {});
  state.selectedNode = null;
  if ((state.currentView || activeViewName()) !== 'flow') switchView('flow');
  renderWorkflowMeta();
  renderCanvas();
  renderProperties();
  openAIOrchestrator();
  $('#canvas')?.classList.add('ai-building');
  $('#canvas')?.scrollTo({left: 0, top: 0});
  return aiBuild;
}

function ingestStreamingWorkflow(graph) {
  const build = aiBuild;
  if (!build) return;
  if (graph.name && state.workflow && state.workflow.name !== graph.name) {
    state.workflow.name = graph.name;
    renderWorkflowMeta();
  }
  graph.nodes.forEach((node, index) => {
    const id = aiResolveNodeId(node, build);
    if (id && id !== node.id) node = {...node, id};
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
      while (aiBuild === build && build.token === aiBuildToken) {
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
  build.spawned = (build.spawned || 0) + 1;
  state.selectedNode = node.id;
  renderCanvas();
  const el = document.querySelector('.flow-node[data-id="' + (CSS.escape ? CSS.escape(node.id) : node.id) + '"]');
  el?.classList.add('ai-just-built');
  aiCanvasFollow(node.position.x, node.position.y);
  const agent = node.agentId ? (state.data?.agents || []).find(a => a.id === node.agentId) : null;
  appendOrchestratorNote('创建节点「' + node.name + '」' + (agent ? ' · ' + agent.name : ''), 'step');
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
  appendOrchestratorNote('连线 ' + (from?.name || edge.source) + ' → ' + (to?.name || edge.target), 'step');
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

// Briefly zoom the node/edge layers out so the whole flow is visible, then settle back.
async function aiOverview() {
  const canvas = $('#canvas');
  const layers = [$('#nodes'), $('#edges')].filter(Boolean);
  const nodes = state.workflow?.nodes || [];
  if (!canvas || !layers.length || !nodes.length) return;
  const width = Math.max(...nodes.map(n => (n.position?.x || 0) + 300));
  const height = Math.max(...nodes.map(n => (n.position?.y || 0) + 130));
  const scale = Math.min(1, (canvas.clientWidth - 40) / width, (canvas.clientHeight - 40) / height);
  if (scale > 0.95) return;
  canvas.scrollTo({left: 0, top: 0});
  const apply = value => layers.forEach(el => {
    el.style.transformOrigin = '0 0';
    el.style.transition = 'transform .55s cubic-bezier(.2,.8,.2,1)';
    el.style.transform = value;
  });
  let restored = false;
  const restore = () => {
    if (restored) return;
    restored = true;
    canvas.removeEventListener('pointerdown', restore, true);
    apply('');
    setTimeout(() => layers.forEach(el => { el.style.transition = ''; el.style.transformOrigin = ''; }), 600);
  };
  canvas.addEventListener('pointerdown', restore, true);
  apply('scale(' + Math.max(0.3, scale).toFixed(3) + ')');
  await aiSleep(2200);
  restore();
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
    finalRaw = ensurePreviewCarried(finalRaw, build.referenced || [], build.goal);
  }
  if (finalRaw && Array.isArray(finalRaw.nodes)) {
    ingestStreamingWorkflow({name: finalRaw.name || '', nodes: finalRaw.nodes, edges: finalRaw.edges || []});
    const finalById = new Map(finalRaw.nodes.filter(n => n.id).map(n => [n.id, n]));
    state.workflow.nodes.forEach(node => {
      const fin = finalById.get(node.id);
      if (fin) Object.assign(node, {...fin, position: node.position});
    });
    if (finalRaw.description) state.workflow.description = finalRaw.description;
  }
  await drainAIBuild();
  if (aiBuild !== build || build.token !== aiBuildToken) return false;
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
  const canvas = $('#canvas');
  canvas?.scrollTo({left: 0, top: 0, behavior: 'smooth'});
  finishAIBuildVisuals();
  canvas?.classList.add('ai-done');
  setTimeout(() => canvas?.classList.remove('ai-done'), 1600);
  aiBuild = null;
  dirty();
  renderWorkflowMeta();
  renderCanvas();
  renderProperties();
  aiOverview();
  const agentIds = new Set((state.data?.agents || []).map(a => a.id));
  const unbound = state.workflow.nodes.filter(n => n.type === 'agent' && (!n.agentId || !agentIds.has(n.agentId)));
  if (unbound.length) appendOrchestratorNote(unbound.length + ' 个 Agent 节点还没绑定有效角色：' + unbound.map(n => n.name).join('、') + '，保存前在右侧属性里选一下', 'warn');
  setAIBuildStatus(state.workflow.edges.length ? '节点和连线都已放好，确认后点保存' : '节点已放好，确认后点保存');
  return true;
}

function aiRolesHint() {
  const agents = catalogAgents().slice(0, 40);
  if (!agents.length) return '当前项目还没有角色，请只用 approval（人工确认）和 script 节点。';
  return '当前项目可用角色（agentId：名称）：\n' + agents.map(a => '- ' + a.id + '：' + a.name + (a.description ? '（' + String(a.description).slice(0, 40) + '）' : '')).join('\n');
}

function aiClone(value) {
  return value == null ? value : JSON.parse(JSON.stringify(value));
}

function aiWantsFreshWorkflow(goal) {
  return /重新(搭建|生成|编排|做一条)|从头(开始|搭)|新建一(条|个)(?!节点)|清空(画布|当前|这个)/.test(String(goal || ''));
}

function aiWantsPreviewRemoved(goal) {
  const text = String(goal || '');
  const drop = '去掉|移除|删除|取消|不用|不要';
  const node = '静态站点|前端预览|预览节点|af-preview-build';
  return new RegExp('(?:' + drop + ')[^，。]{0,12}(?:' + node + ')|(?:' + node + ')[^，。]{0,8}(?:' + drop + ')').test(text);
}

function compactWorkflowForAI(wf) {
  if (!wf) return null;
  return {
    id: wf.id,
    name: wf.name,
    description: wf.description || '',
    sandboxProfileId: wf.sandboxProfileId || '',
    preview: wf.preview || undefined,
    nodes: (wf.nodes || []).map(node => {
      const copy = {...node};
      delete copy.position;
      if (typeof copy.script === 'string' && copy.script.length > 400) copy.script = '';
      return copy;
    }),
    edges: (wf.edges || []).map(edge => ({id: edge.id, source: edge.source, target: edge.target, when: edge.when || 'success'}))
  };
}

function aiReferencedWorkflows(goal) {
  const folded = String(goal || '').replace(/\s+/g, '').toLowerCase();
  if (!folded) return [];
  return (state.data?.workflows || []).filter(wf => {
    const name = String(wf.name || '').replace(/\s+/g, '').toLowerCase();
    const core = name.replace(/^通用/, '');
    return (name.length >= 2 && folded.includes(name)) || (core.length >= 3 && folded.includes(core));
  }).slice(0, 2);
}

function aiResolveNodeId(node, build) {
  if (!node) return '';
  if (node.id && build.placed.has(node.id)) return node.id;
  if (build.mode === 'edit' && node.name) {
    for (const [id, prev] of build.placed) {
      if (prev.name === node.name) return id;
    }
  }
  return node.id || '';
}

function ensurePreviewCarried(next, sources, goal) {
  next = next || {};
  next.nodes = Array.isArray(next.nodes) ? next.nodes.slice() : [];
  next.edges = Array.isArray(next.edges) ? next.edges.slice() : [];
  if (aiWantsPreviewRemoved(goal)) return next;
  const donors = (sources || []).filter(Boolean);
  if (next.nodes.some(node => node.id === 'af-preview-build')) {
    if (!next.preview) {
      const donor = donors.find(item => item.preview);
      if (donor) next.preview = aiClone(donor.preview);
    }
    if (!next.edges.some(edge => edge.target === 'af-preview-build')) {
      const donor = donors.find(item => (item.edges || []).some(edge => edge.target === 'af-preview-build'));
      (donor?.edges || []).filter(edge => edge.target === 'af-preview-build' && next.nodes.some(node => node.id === edge.source)).forEach(edge => next.edges.push(aiClone(edge)));
    }
    return next;
  }
  const donor = donors.find(item => (item.nodes || []).some(node => node.id === 'af-preview-build') || item.preview?.enabled);
  if (!donor) return next;
  const preview = (donor.nodes || []).find(node => node.id === 'af-preview-build') || {
    id: 'af-preview-build', name: '构建静态站点', type: 'agent', agentId: 'af-preview-builder'
  };
  next.nodes.push(aiClone(preview));
  if (donor.preview) next.preview = aiClone(donor.preview);
  const carried = (donor.edges || []).filter(edge => edge.target === 'af-preview-build' && next.nodes.some(node => node.id === edge.source));
  if (carried.length) {
    carried.forEach(edge => next.edges.push(aiClone(edge)));
    return next;
  }
  next.nodes.filter(node => node.id !== 'af-preview-build' && !next.edges.some(edge => edge.source === node.id && edge.when !== 'failure')).forEach((node, index) => {
    next.edges.push({id: 'af-preview-build-edge-' + (index + 1), source: node.id, target: 'af-preview-build', when: 'success'});
  });
  return next;
}

function mergeAIEdit(base, incoming, goal, referenced) {
  const prevNodes = base?.nodes || [];
  const idMap = new Map();
  const used = new Set();
  const nodes = (Array.isArray(incoming?.nodes) ? incoming.nodes : []).map((raw, index) => {
    const normalized = normalizeAINode(raw, index);
    let prev = prevNodes.find(node => node.id === normalized.id && !used.has(node.id));
    if (!prev && normalized.name) prev = prevNodes.find(node => node.name === normalized.name && !used.has(node.id));
    if (!prev) return normalized;
    used.add(prev.id);
    if (normalized.id && normalized.id !== prev.id) idMap.set(normalized.id, prev.id);
    const merged = {...prev, ...normalized, id: prev.id};
    if (!raw.script) merged.script = prev.script;
    if (!raw.position || !Number.isFinite(raw.position.x)) merged.position = prev.position;
    return merged;
  });
  const known = new Set(nodes.map(node => node.id));
  const edges = (Array.isArray(incoming?.edges) ? incoming.edges : []).map(edge => {
    const normalized = normalizeAIEdge(edge);
    normalized.source = idMap.get(normalized.source) || normalized.source;
    normalized.target = idMap.get(normalized.target) || normalized.target;
    return normalized;
  }).filter(edge => known.has(edge.source) && known.has(edge.target) && edge.source !== edge.target);
  const shell = {...(base || {}), nodes, edges};
  shell.name = incoming?.name || shell.name;
  shell.description = incoming?.description ?? shell.description;
  if (incoming?.preview) shell.preview = incoming.preview;
  return ensurePreviewCarried(shell, [base, ...(referenced || [])], goal);
}

function aiOrchestratePrompt(goal, canvas, referenced) {
  const editing = !!(canvas && (canvas.nodes || []).length);
  const lines = [
    editing
      ? '在当前画布上的这份 SOP 上按要求修改，重新提交完整的 workflows_save 草稿。这是增量修改：用户没点名的节点、连线、id 和脚本都保持原样，不要另起一条流程。'
      : '请在当前项目搭建一条新的 SOP。',
    '只提交一个 workflows_save 草稿，不要发布，也不要调用其它写入工具。',
    'drafts[0].body 必须是 JSON 对象（不要序列化成字符串），字段按 name、description、nodes、edges 的顺序输出；已有节点沿用原来的 id，只有新增节点才用新 id。',
    'agent 节点的 agentId 只能从下面的已有角色里选；没有合适角色时改用 approval（人工确认）或 script 节点。',
    '平台节点「构建静态站点」不在角色列表里：id 固定为 af-preview-build，agentId 为 af-preview-builder。复制或修改已有 SOP 时必须保留它、它的连线和 preview 字段，除非用户明确要求去掉前端预览。',
    'edges 用 {"source","target","when":"success"} 按成功路径连线，可以并行分支再汇合。',
    aiRolesHint()
  ];
  if (editing) lines.push('当前画布上的 SOP：\n' + JSON.stringify(compactWorkflowForAI(canvas)));
  (referenced || []).forEach(wf => lines.push('用户点名要参考的已有 SOP「' + wf.name + '」：\n' + JSON.stringify(compactWorkflowForAI(wf))));
  lines.push('目标：' + goal);
  return lines.join('\n');
}

async function beginAIOrchestration(goal) {
  const hasGraph = !!state.workflow && (state.workflow.nodes || []).length > 0;
  const fresh = aiWantsFreshWorkflow(goal);
  if (hasGraph && !fresh) {
    const token = ++aiBuildToken;
    finishAIBuildVisuals();
    aiBuild = {
      token, buffer: '', thought: '',
      placed: new Map(state.workflow.nodes.map(node => [node.id, node])),
      wired: new Set((state.workflow.edges || []).map(edge => edge.source + '→' + edge.target)),
      pendingEdges: [], queue: [], draining: null,
      ghostText: '', ghostHidden: true, freePositions: false,
      mode: 'edit', base: aiClone(state.workflow), spawned: 0, goal, referenced: []
    };
    if ((state.currentView || activeViewName()) !== 'flow') switchView('flow');
    openAIOrchestrator();
    $('#canvas')?.classList.add('ai-building');
    return aiBuild;
  }
  const current = state.workflow;
  return startAIBuild({
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
  }, {confirmed: !fresh, goal, ghost: 'AI 正在理解你的需求…'});
}

async function completeAIEdit(build, finalRaw) {
  if (!finalRaw || !Array.isArray(finalRaw.nodes) || !finalRaw.nodes.length) {
    state.workflow = build.base;
    finishAIBuildVisuals();
    aiBuild = null;
    syncCanvasAIEntry();
    renderWorkflowMeta();
    renderCanvas();
    renderProperties();
    return false;
  }
  const merged = mergeAIEdit(build.base, finalRaw, build.goal, build.referenced);
  const oldIds = new Set((build.base.nodes || []).map(node => node.id));
  const onCanvas = new Set((state.workflow.nodes || []).map(node => node.id));
  const kept = merged.nodes.filter(node => oldIds.has(node.id) || onCanvas.has(node.id));
  const keptIds = new Set(kept.map(node => node.id));
  const newcomers = merged.nodes.filter(node => !keptIds.has(node.id));
  const immediateEdges = merged.edges.filter(edge => keptIds.has(edge.source) && keptIds.has(edge.target));
  const laterEdges = merged.edges.filter(edge => !keptIds.has(edge.source) || !keptIds.has(edge.target));
  state.workflow = {...merged, nodes: kept.slice(), edges: immediateEdges.slice()};
  build.placed = new Map(kept.map(node => [node.id, node]));
  build.wired = new Set(immediateEdges.map(edge => edge.source + '→' + edge.target));
  renderWorkflowMeta();
  renderCanvas();
  renderProperties();
  if (newcomers.length || laterEdges.length) {
    setAIBuildStatus('补上新的步骤…');
    ingestStreamingWorkflow({name: merged.name, nodes: newcomers, edges: laterEdges});
    await drainAIBuild();
  }
  if (aiBuild !== build || build.token !== aiBuildToken) return false;
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
  const previous = build.mode === 'edit' ? build.base : (build.spawned ? null : build.previous);
  if (!previous) return;
  state.workflow = previous;
  renderWorkflowMeta();
  renderCanvas();
  renderProperties();
}

async function readAssistantSSE(res, onEvent) {
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '', result = null, error = null;
  for (;;) {
    const {value, done} = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, {stream: true});
    let idx;
    while ((idx = buffer.indexOf('\n\n')) >= 0) {
      const chunk = buffer.slice(0, idx);
      buffer = buffer.slice(idx + 2);
      let event = 'message', data = '';
      chunk.split('\n').forEach(line => {
        if (line.startsWith('event:')) event = line.slice(6).trim();
        else if (line.startsWith('data:')) data += (data ? '\n' : '') + line.slice(5).trimStart();
      });
      let payload = {};
      try { payload = JSON.parse(data); } catch { continue; }
      if (event === 'thought') onEvent(payload);
      else if (event === 'result') result = payload;
      else if (event === 'error') error = payload.error || '生成失败';
    }
  }
  if (error) throw new Error(error);
  if (!result) throw new Error('AI 没有返回结果，请重试');
  return result;
}

async function runAIOrchestrator() {
  const input = $('#aiOrchestratorInput');
  const goal = input?.value.trim();
  if (!goal) return toast('先描述要编排的流程');
  if (!state.projectFilter) return toast('先在顶栏选择项目');
  const button = $('#aiOrchestratorForm')?.querySelector('button[type="submit"]');
  if (button?.disabled) return;
  const project = state.projectFilter;
  const history = aiHistory[project] || {sessionId: '', messages: []};
  const editing = !!state.workflow && (state.workflow.nodes || []).length > 0 && !aiWantsFreshWorkflow(goal);
  const canvas = editing ? aiClone(state.workflow) : null;
  const referenced = aiReferencedWorkflows(goal).filter(wf => !canvas || wf.id !== canvas.id);
  const build = await beginAIOrchestration(goal);
  if (!build) return;
  build.goal = goal;
  build.referenced = referenced;
  if (button) button.disabled = true;
  input.value = '';
  appendOrchestratorNote(goal, 'mine');
  setAIBuildStatus(editing ? '正在按当前流程修改，画布先保持原样' : '正在连接 AI…');
  if (!editing) placeAIGhost();
  const messages = history.messages.concat([{role: 'user', content: aiOrchestratePrompt(goal, canvas, referenced)}]);
  aiAbort = new AbortController();
  try {
    const res = await fetch('/api/assistant/chat', {
      method: 'POST',
      headers: {'Content-Type': 'application/json', 'Accept': 'text/event-stream'},
      signal: aiAbort.signal,
      body: JSON.stringify({projectId: project, sessionId: history.sessionId, model: localStorage.getItem('agentflow.assistantModel') || '', approvalMode: 'confirm', messages})
    });
    if (!res.ok) {
      let msg = 'HTTP ' + res.status;
      try { msg = (await res.json()).error || msg; } catch {}
      throw new Error(msg);
    }
    const reply = (res.headers.get('Content-Type') || '').includes('text/event-stream') ? await readAssistantSSE(res, pushAIThought) : await res.json();
    if (build.token !== aiBuildToken) return;
    aiHistory[project] = {sessionId: reply.sessionId || history.sessionId, messages: messages.concat([{role: 'assistant', content: reply.message || '已提交草稿'}]).slice(-8)};
    document.querySelectorAll('.ai-live-thought.live').forEach(box => box.classList.remove('live'));
    if (reply.message) appendOrchestratorNote(reply.message, '');
    const draft = (reply.drafts || []).find(item => item.tool === 'workflows_save' && item.body);
    (draft?.warnings || []).forEach(w => appendOrchestratorNote(w, 'warn'));
    let body = draft?.body;
    if (typeof body === 'string') { try { body = JSON.parse(body); } catch { body = null; } }
    const ok = await completeAIBuild(build, body);
    if (!ok && build.token === aiBuildToken) setAIBuildStatus(build.mode === 'edit' ? '这轮没有改动画布，当前流程还在' : '这轮没有生成可放置的 SOP，可以换个说法再试');
  } catch (error) {
    if (build.token !== aiBuildToken) return;
    restoreAIBuildFailure(build);
    finishAIBuildVisuals();
    aiBuild = null;
    syncCanvasAIEntry();
    setAIBuildStatus('');
    if (error.name !== 'AbortError') {
      appendOrchestratorNote(error.message, 'error');
      toast(error.message);
    }
  } finally {
    if (button) button.disabled = false;
    aiAbort = null;
  }
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
