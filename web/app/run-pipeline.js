// Run 流水视图：把节点状态、耗时、Token、重试、产物与失败原因聚合到一屏。
// 只读前端聚合，数据来自 GET /api/runs/:id（run.nodes / run.artifacts / run.events / run.usage）。

function formatPipelineDuration(ms) {
  ms = Math.max(0, Number(ms) || 0);
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(seconds < 10 ? 1 : 0)}s`;
  const minutes = Math.floor(seconds / 60);
  const restSeconds = Math.round(seconds % 60);
  if (minutes < 60) return restSeconds ? `${minutes}m${restSeconds}s` : `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h${minutes % 60}m`;
}

function formatPipelineBytes(bytes) {
  let value = Math.max(0, Number(bytes) || 0);
  if (value >= 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(value >= 10 * 1024 * 1024 ? 0 : 1)} MB`;
  if (value >= 1024) return `${(value / 1024).toFixed(value >= 10 * 1024 ? 0 : 1)} KB`;
  return `${value} B`;
}

function runPipelineNodeDurationMs(nr) {
  if (!nr) return 0;
  const start = nr.startedAt ? Date.parse(nr.startedAt) : NaN;
  if (!Number.isFinite(start)) return 0;
  const finished = nr.finishedAt ? Date.parse(nr.finishedAt) : NaN;
  return Math.max(0, (Number.isFinite(finished) ? finished : Date.now()) - start);
}

function runPipelineArtifactsByNode(run) {
  const byNode = {};
  for (const artifact of run?.artifacts || []) {
    const key = artifact.nodeId || '';
    (byNode[key] = byNode[key] || []).push(artifact);
  }
  return byNode;
}

// 节点执行顺序优先跟随 workflow 拓扑（同阶段并行节点按定义顺序），
// workflow 里找不到的 run 节点（例如历史 run 的已删除节点）追加在末尾。
function runPipelineOrderedNodes(run, workflowNodes, edges) {
  const nodes = Array.isArray(workflowNodes) ? workflowNodes.filter(Boolean) : [];
  const ordered = [];
  const seen = new Set();
  const stages = typeof workflowStages === 'function'
    ? workflowStages({ nodes, edges: Array.isArray(edges) ? edges : [] })
    : [nodes];
  for (const stage of stages || []) {
    for (const node of stage || []) {
      if (!node || !node.id || seen.has(node.id)) continue;
      seen.add(node.id);
      ordered.push(node);
    }
  }
  for (const [id, nr] of Object.entries(run?.nodes || {})) {
    if (!nr || seen.has(id)) continue;
    seen.add(id);
    ordered.push({ id, name: id });
  }
  return ordered;
}

function runPipelineSummary(run, orderedCount) {
  const nodes = Object.values(run?.nodes || {}).filter(Boolean);
  const count = (status) => nodes.filter((node) => node.status === status).length;
  const usage = run?.usage || {};
  const artifacts = run?.artifacts || [];
  const artifactBytes = artifacts.reduce((sum, artifact) => sum + Number(artifact.size || 0), 0);
  const toolCalls = nodes.reduce((sum, node) => sum + Number(node.toolCalls || 0), 0);
  const retries = nodes.reduce((sum, node) => sum
    + Math.max(0, Number(node.attempt || 0) - 1)
    + Math.max(0, Number(node.reworkCount || 0)), 0);
  const nodeTokens = nodes.reduce((sum, node) => sum + Number(node.usage?.totalTokens || 0), 0);
  return {
    total: Math.max(nodes.length, Number(orderedCount) || 0),
    succeeded: count('succeeded'),
    failed: count('failed'),
    running: count('running'),
    waiting: count('waiting_approval'),
    cancelled: count('cancelled'),
    pending: nodes.filter((node) => !node.status || node.status === 'pending').length,
    tokens: Number(usage.totalTokens || nodeTokens || 0),
    inputTokens: Number(usage.inputTokens || 0),
    outputTokens: Number(usage.outputTokens || 0),
    toolCalls,
    retries,
    artifacts: artifacts.length,
    artifactBytes,
  };
}

function runPipelineNodeDetailText(run, nr, artifacts) {
  if (!nr) return '尚未开始';
  if (nr.status === 'failed' && nr.error) return nr.error;
  if (nr.activity) return nr.activity;
  if (nr.error) return nr.error;
  if (nr.status === 'waiting_approval') return '等待人工审批';
  if (nr.status === 'succeeded') {
    const parts = [];
    if (artifacts.length) parts.push(`${artifacts.length} 个产物`);
    if (nr.gateResult?.decision) parts.push(`门禁 ${nr.gateResult.decision}`);
    return parts.join(' · ') || '已完成';
  }
  return '';
}

function runPipelineRowHTML(run, node, nr, artifacts) {
  const status = nr?.status || 'pending';
  const retries = Math.max(0, Number(nr?.attempt || 0) - 1) + Math.max(0, Number(nr?.reworkCount || 0));
  const tokens = Number(nr?.usage?.totalTokens || 0);
  const artifactBytes = artifacts.reduce((sum, artifact) => sum + Number(artifact.size || 0), 0);
  const detail = runPipelineNodeDetailText(run, nr, artifacts);
  const detailClass = status === 'failed' ? ' is-error' : (status === 'running' ? ' is-running' : '');
  const duration = status === 'running' ? '进行中' : formatPipelineDuration(runPipelineNodeDurationMs(nr));
  const artifactsCell = artifacts.length ? `${artifacts.length} 个 · ${formatPipelineBytes(artifactBytes)}` : '—';
  const artifactItems = artifacts.map((artifact) => `<button type="button" class="run-pipeline-artifact" data-run-pipeline-artifact="${escapeHTML(artifact.id)}"><b>${escapeHTML(artifact.name || 'artifact')}</b><span>${escapeHTML(artifact.path || '')} · ${formatPipelineBytes(artifact.size || 0)}</span></button>`).join('');
  const output = nr?.output ? `<pre class="run-pipeline-output">${escapeHTML(String(nr.output).slice(0, 1200))}</pre>` : '';
  return `<div class="run-pipeline-item is-${escapeHTML(status)}">
    <button type="button" class="run-pipeline-row" data-run-pipeline-node="${escapeHTML(node.id)}" aria-expanded="false">
      <span class="run-pipeline-node"><i class="run-pipeline-dot"></i><b>${escapeHTML(node.name || node.id)}</b><small>${escapeHTML(node.id)}</small></span>
      <span class="run-pipeline-status is-${escapeHTML(status)}">${escapeHTML(statusText(status) || '待执行')}</span>
      <span class="run-pipeline-metric" title="节点耗时">${escapeHTML(duration)}</span>
      <span class="run-pipeline-metric" title="执行次数 / 返工次数">${nr?.attempt || 0}${retries ? ` / ${retries}` : ' / 0'}</span>
      <span class="run-pipeline-metric" title="Token 用量">${formatTokens(tokens)}</span>
      <span class="run-pipeline-metric" title="工具调用次数">${Number(nr?.toolCalls || 0)}</span>
      <span class="run-pipeline-metric" title="产物数量与大小">${escapeHTML(artifactsCell)}</span>
      <span class="run-pipeline-detail${detailClass}">${escapeHTML(detail)}</span>
      <span class="run-pipeline-chevron" aria-hidden="true">›</span>
    </button>
    <div class="run-pipeline-extra" hidden>
      ${nr?.error ? `<div class="run-pipeline-extra-block is-error"><b>失败原因</b><pre>${escapeHTML(nr.error)}</pre></div>` : ''}
      ${nr?.activity ? `<div class="run-pipeline-extra-block"><b>当前活动</b><p>${escapeHTML(nr.activity)}</p></div>` : ''}
      <div class="run-pipeline-extra-block"><b>产物</b>${artifactItems || '<p>该节点没有产物</p>'}</div>
      ${output ? `<div class="run-pipeline-extra-block"><b>节点输出（截断）</b>${output}</div>` : ''}
    </div>
  </div>`;
}

function runPipelineSummaryHTML(run, summary) {
  const headline = summary.failed ? `失败 ${summary.failed}`
    : summary.running ? '运行中'
    : summary.waiting ? '待审批'
    : summary.cancelled ? '已取消'
    : summary.pending ? '等待执行'
    : '已完成';
  const cells = [
    ['状态', headline],
    ['节点', `${summary.succeeded}/${summary.total} 完成${summary.failed ? ` · ${summary.failed} 失败` : ''}`],
    ['耗时', typeof runElapsed === 'function' ? runElapsed(run) : '—'],
    ['Token', formatTokens(summary.tokens)],
    ['工具调用', String(summary.toolCalls)],
    ['重试/返工', String(summary.retries)],
    ['产物', `${summary.artifacts} 个 · ${formatPipelineBytes(summary.artifactBytes)}`],
  ];
  return `<div class="run-pipeline-summary">${cells.map(([label, value]) => `<div class="run-pipeline-summary-cell"><span>${escapeHTML(label)}</span><b>${escapeHTML(value)}</b></div>`).join('')}</div>`;
}

function runPipelineTimelineHTML(run) {
  const events = (run?.events || []).slice(-40);
  if (!events.length) return '<div class="empty-inline"><p>暂无运行事件</p></div>';
  return events.map((event) => {
    const time = event.timestamp ? new Date(event.timestamp).toLocaleTimeString() : '';
    const level = String(event.type || '').includes('fail') || String(event.type || '').includes('exhausted') ? ' is-error' : '';
    return `<div class="run-pipeline-timeline-item${level}">
      <time>${escapeHTML(time)}</time>
      ${event.nodeId ? `<span class="run-pipeline-timeline-node">${escapeHTML(event.nodeId)}</span>` : ''}
      <span class="run-pipeline-timeline-type">${escapeHTML(event.type || '')}</span>
      <span class="run-pipeline-timeline-message">${escapeHTML(event.message || '')}</span>
    </div>`;
  }).join('');
}

function runPipelinePaneHTML(run, workflowNodes, edges) {
  const ordered = runPipelineOrderedNodes(run, workflowNodes, edges);
  const artifactsByNode = runPipelineArtifactsByNode(run);
  const summary = runPipelineSummary(run, ordered.length);
  const rows = ordered.map((node) => runPipelineRowHTML(run, node, run?.nodes?.[node.id], artifactsByNode[node.id] || [])).join('');
  return `<section class="run-pipeline-section">
    <div class="section-heading"><div><h3>运行流水</h3><span>每个节点的状态、耗时、Token、重试、产物与失败原因</span></div>${summary.retries ? `<span class="run-pipeline-retry-badge">${summary.retries} 次重试/返工</span>` : ''}</div>
    ${runPipelineSummaryHTML(run, summary)}
    <div class="run-pipeline-columns"><span>节点</span><span>状态</span><span>耗时</span><span>次数/返工</span><span>Token</span><span>工具</span><span>产物</span></div>
    <div class="run-pipeline-list">${rows || '<div class="empty-inline"><p>这次运行还没有节点记录</p></div>'}</div>
  </section>
  <section class="run-pipeline-timeline-section">
    <div class="section-heading"><div><h3>事件时间线</h3><span>最近 ${Math.min((run?.events || []).length, 40)} 条事件</span></div></div>
    <div class="run-pipeline-timeline">${runPipelineTimelineHTML(run)}</div>
  </section>`;
}

function bindRunPipelinePane(run, host) {
  const pane = host.querySelector('[data-run-pane="pipeline"]');
  if (!pane) return;
  pane.querySelectorAll('[data-run-pipeline-node]').forEach((row) => {
    row.onclick = () => {
      const expanded = row.getAttribute('aria-expanded') === 'true';
      row.setAttribute('aria-expanded', String(!expanded));
      row.closest('.run-pipeline-item')?.querySelector('.run-pipeline-extra')?.toggleAttribute('hidden', expanded);
    };
  });
  pane.querySelectorAll('[data-run-pipeline-artifact]').forEach((button) => {
    button.onclick = (event) => {
      event.stopPropagation();
      if (typeof previewArtifact === 'function') previewArtifact(run, button.dataset.runPipelineArtifact);
    };
  });
}
