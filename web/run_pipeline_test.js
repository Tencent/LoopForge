const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const bundle = fs.readFileSync(process.argv[2], 'utf8');

function source(name) {
  const match = new RegExp('^(?:async )?function ' + name + '\\(', 'm').exec(bundle);
  assert(match, name + ' not found in the app bundle');
  const tail = bundle.slice(match.index);
  const next = /\n(?:async )?function |\n(?:let|const) [A-Za-z]|\n\/\/ /.exec(tail);
  return next ? tail.slice(0, next.index) : tail;
}

const context = {
  escapeHTML: (value) => String(value ?? ''),
  statusText: (status) => ({ pending: '待执行', running: '运行中', succeeded: '已完成', failed: '失败', waiting_approval: '待审批', cancelled: '已取消' }[status] || ''),
  formatTokens: (n) => String(n ?? 0),
  runElapsed: () => '12m',
};
vm.createContext(context);
for (const name of ['workflowStages', 'formatPipelineDuration', 'formatPipelineBytes', 'runPipelineNodeDurationMs', 'runPipelineArtifactsByNode', 'runPipelineOrderedNodes', 'runPipelineSummary', 'runPipelineNodeDetailText', 'runPipelineRowHTML', 'runPipelineSummaryHTML', 'runPipelineTimelineHTML', 'runPipelinePaneHTML']) {
  vm.runInContext(source(name), context);
}

assert.equal(context.formatPipelineDuration(0), '0ms');
assert.equal(context.formatPipelineDuration(1500), '1.5s');
assert.equal(context.formatPipelineDuration(65000), '1m5s');
assert.equal(context.formatPipelineDuration(3700000), '1h1m');
assert.equal(context.formatPipelineBytes(512), '512 B');
assert.equal(context.formatPipelineBytes(2048), '2.0 KB');
assert.equal(context.formatPipelineBytes(1572864), '1.5 MB');

const run = {
  status: 'failed',
  usage: { totalTokens: 12345, inputTokens: 10000, outputTokens: 2345 },
  createdAt: '2026-01-01T00:00:00Z',
  nodes: {
    clarify: { status: 'succeeded', attempt: 1, toolCalls: 4, usage: { totalTokens: 1000 }, startedAt: '2026-01-01T00:00:00Z', finishedAt: '2026-01-01T00:00:30Z', output: '需求已整理' },
    design: { status: 'succeeded', attempt: 2, reworkCount: 1, toolCalls: 6, usage: { totalTokens: 4000 }, startedAt: '2026-01-01T00:00:30Z', finishedAt: '2026-01-01T00:05:30Z' },
    implement: { status: 'failed', attempt: 2, toolCalls: 9, usage: { totalTokens: 7000 }, startedAt: '2026-01-01T00:05:30Z', finishedAt: '2026-01-01T00:15:30Z', error: '写入节点终态失败：Error 1406 Data too long', activity: '运行中' },
    ghost: { status: 'running', attempt: 1, startedAt: '2026-01-01T00:15:30Z', activity: 'read_image /workspace/refs/a.png' },
  },
  artifacts: [
    { id: 'a1', nodeId: 'clarify', name: 'requirement.md', path: 'requirement.md', size: 84741 },
    { id: 'a2', nodeId: 'implement', name: 'implementation.md', path: 'implementation.md', size: 2048 },
  ],
  events: [
    { timestamp: '2026-01-01T00:00:30Z', nodeId: 'clarify', type: 'node.succeeded', message: 'clarify 已完成' },
    { timestamp: '2026-01-01T00:15:30Z', nodeId: 'implement', type: 'agent.loop.exhausted', message: 'Agent 自修正次数已耗尽' },
  ],
};
const workflowNodes = [
  { id: 'clarify', name: '需求定型' },
  { id: 'design', name: '方案设计' },
  { id: 'implement', name: '实现' },
];
const ordered = context.runPipelineOrderedNodes(run, workflowNodes, [
  { source: 'clarify', target: 'design' },
  { source: 'design', target: 'implement' },
]);
assert.deepEqual(Array.from(ordered, (node) => node.id), ['clarify', 'design', 'implement', 'ghost']);

const summary = context.runPipelineSummary(run);
assert.equal(summary.total, 4);
assert.equal(summary.succeeded, 2);
assert.equal(summary.failed, 1);
assert.equal(summary.running, 1);
assert.equal(summary.tokens, 12345);
assert.equal(summary.toolCalls, 19);
assert.equal(summary.retries, 3);
assert.equal(summary.artifacts, 2);
assert.equal(summary.artifactBytes, 84741 + 2048);

const artifactsByNode = context.runPipelineArtifactsByNode(run);
assert.equal(artifactsByNode.clarify.length, 1);
const failedRow = context.runPipelineRowHTML(run, workflowNodes[2], run.nodes.implement, artifactsByNode.implement || []);
assert(failedRow.includes('is-failed'));
assert(failedRow.includes('Error 1406'), 'failed row carries the real error');
assert(failedRow.includes('data-run-pipeline-node="implement"'));
assert(failedRow.includes('实现'));
assert(failedRow.includes('1 个 · 2.0 KB'));
const runningRow = context.runPipelineRowHTML(run, ordered[3], run.nodes.ghost, []);
assert(runningRow.includes('进行中'), 'running node shows progress instead of a duration');

const pane = context.runPipelinePaneHTML(run, workflowNodes, [{ source: 'clarify', target: 'design' }, { source: 'design', target: 'implement' }]);
assert(pane.includes('运行流水'));
assert(pane.includes('事件时间线'));
assert(pane.includes('agent.loop.exhausted'), 'timeline renders run events');
assert(pane.includes('写入节点终态失败'), 'expanded payload has the error');
assert(pane.includes('需求定型'), 'rows follow the workflow order');
assert(pane.includes('retry-badge'), 'retry badge appears when retries exist');
