import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { put, post, del, track, get, list, waitFor } from './api.mjs';
import { cfg, uid } from './env.mjs';

let x = 0;
const at = () => ({ x: (x += 220), y: 200 });

export const script = (id, body, extra = {}) => ({
  id, name: extra.name || id, type: 'script', scriptLanguage: 'shell', script: body,
  maxAttempts: 1, maxReworks: 0, position: at(), ...extra,
});
export const approval = (id, rejectTo, extra = {}) => ({
  id, name: extra.name || id, type: 'approval', rejectTo, maxAttempts: 1, maxReworks: 3, position: at(), ...extra,
});
export const agentNode = (id, agentId, extra = {}) => ({
  id, name: extra.name || id, type: 'agent', agentId, executorId: cfg.executor, maxAttempts: 1, maxReworks: 0, position: at(), ...extra,
});
export const sopNode = (id, workflowId, extra = {}) => ({
  id, name: extra.name || id, type: 'sop', sopWorkflowId: workflowId, maxAttempts: 1, maxReworks: 0, position: at(), ...extra,
});

export function chain(nodes) {
  return nodes.slice(1).map((n, i) => ({ id: `e${i}`, source: nodes[i].id, target: n.id, when: 'success' }));
}

// createWorkflow saves and publishes a project SOP and registers its deletion.
export async function createWorkflow(label, nodes, extra = {}) {
  const id = uid(label);
  track(`workflow ${id}`, () => del(`/api/workflows/${id}`));
  await put('/api/workflows', {
    id, name: `E2E ${label}`, projectId: cfg.projectId, description: 'e2e 自动化用例（自动清理）',
    status: 'draft', revision: 0, sandboxProfileId: cfg.sandbox, nodes, edges: chain(nodes), ...extra,
  });
  await post(`/api/workflows/${id}/publish`, {});
  return id;
}

export async function createAgent(label, prompt, extra = {}) {
  const id = uid(label);
  track(`agent ${id}`, () => del(`/api/agents/${id}`));
  await put('/api/agents', {
    id, name: `E2E ${label}`, projectId: cfg.projectId, description: 'e2e', executorId: cfg.executor, prompt, ...(cfg.modelID ? {model:cfg.modelID} : {}), ...extra,
  });
  return id;
}

// startRun starts a manual run and registers cancel + delete so no container
// or workspace outlives the suite.
export async function startRun(workflowId, input = 'e2e') {
  const run = await post('/api/runs', { workflowId, input, projectId: cfg.projectId });
  trackRun(run.id);
  return run;
}

export function trackRun(id) {
  track(`run ${id}`, async () => {
    const run = await get(`/api/runs/${id}`, { expect: [200, 404] });
    if (run?.status === 'running' || run?.status === 'waiting' || run?.status === 'paused' || run?.status === 'pending') {
      await post(`/api/runs/${id}/cancel`, {}, { expect: [200, 204, 400, 409] });
      await new Promise(r => setTimeout(r, 2000));
    }
    if (run?.id) await del(`/api/runs/${id}`, { expect: [200, 204, 400, 404, 409] });
  });
}

export const repoSkip = cfg.repoCloneURL ? false : 'AGENTFLOW_E2E_REPO_CLONE_URL not set';

// repoScript runs inside the bound repository's checkout instead of the
// workspace root.
export const repoScript = (id, body, extra = {}) => script(id, body, { executionScope: 'repositories', ...extra });

// createRepository registers AGENTFLOW_E2E_REPO_CLONE_URL in the e2e project.
export async function createRepository(label = 'repo') {
  const id = uid(label);
  track(`repository ${id}`, () => del(`/api/repositories?id=${id}`));
  await post('/api/repositories', {
    id, projectId: cfg.projectId, provider: 'gongfeng', name: id,
    gongfengProjectId: `e2e-auto/${id}`, cloneUrl: cfg.repoCloneURL, defaultBranch: cfg.repoBranch,
  });
  return id;
}

// dispatchWithRepository runs workflowId for a fresh requirement bound to
// repositoryId, the path that freezes repositories into the run.
export async function dispatchWithRepository(workflowId, repositoryId, title = 'repository run') {
  const issue = await post('/api/issues', {
    projectId: cfg.projectId, title: `E2E ${title} ${cfg.runTag}`, description: 'e2e 自动化（会被关闭）',
    priority: 'medium', kind: 'requirement', status: 'todo', workflowId, repositoryIds: [repositoryId],
  });
  track(`issue ${issue.id}`, () => post(`/api/issues/${issue.id}/close`, {}, { expect: [200, 404, 409] }));
  await post(`/api/issues/${issue.id}/dispatch`, { workflowId });
  const runs = await waitFor('issue run', async () => {
    const items = list(await get(`/api/issues/${issue.id}/runs`));
    return items.length ? items : null;
  }, { timeoutMs: 60_000 });
  const runId = runs[0].id || runs[0].runId;
  trackRun(runId);
  return runId;
}

// localRemoteBranches lists the remote's branches when the clone URL is a
// path on this machine, or null when it cannot be inspected from here.
export function localRemoteBranches() {
  if (!cfg.repoCloneURL || !existsSync(cfg.repoCloneURL)) return null;
  return execFileSync('git', ['-C', cfg.repoCloneURL, 'for-each-ref', '--format=%(refname) %(objectname)', 'refs/heads'], { encoding: 'utf8' }).trim();
}

// assertCodeStaysLocal checks a waiting run's checkout: the expected files
// exist and no node committed anything, so nothing can have been pushed.
export async function assertCodeStaysLocal(run, files) {
  const repo = (run.repositories && run.repositories[0]) || run.repository;
  assert.ok(repo?.repositoryId, `run ${run.id} has no repository`);
  const dir = repo.workspaceDir ? `${repo.workspaceDir}/` : '';
  for (const [file, content] of Object.entries(files)) {
    const got = await get(`/api/runs/${run.id}/workspace/file?path=${encodeURIComponent(dir + file)}`);
    assert.equal(String(got?.content ?? got).trim(), content, `${dir}${file}`);
  }
  const log = await get(`/api/runs/${run.id}/workspace/log?repository=${encodeURIComponent(repo.workspaceDir || repo.name || '')}`);
  const nodeCommits = (log.commits || []).filter(c => /^agentflow:/.test(c.message || ''));
  assert.deepEqual(nodeCommits.map(c => c.message), [], 'a node committed to the work branch');
}

// childSOPHandoffScenario: a repository-bound parent writes a file, a child
// SOP must see it and write another, and the parent must see the child's
// file. Nodes never commit or push; code travels through the shared checkout
// only. `nodes` overrides the child/parent writers (e.g. real Agent nodes).
export async function childSOPHandoffScenario({ label = 'handoff', extra = {}, childNodes, timeoutMs = 600_000, waitRun } = {}) {
  const before = localRemoteBranches();
  const repositoryId = await createRepository(`${label}-repo`);
  const child = await createWorkflow(`${label}-child`, childNodes || [
    repoScript('c1', 'test "$(cat parent.txt)" = parent && echo child > child.txt && echo CHILD_SAW_PARENT'),
  ], extra);
  const parent = await createWorkflow(`${label}-parent`, [
    repoScript('p1', 'echo parent > parent.txt'),
    sopNode('sub', child),
    repoScript('p2', 'test "$(cat child.txt)" = child && echo PARENT_SAW_CHILD'),
    approval('hold', ''),
  ], extra);
  const runId = await dispatchWithRepository(parent, repositoryId, `${label} 子 SOP 交接`);
  const waiting = await waitRun(runId, r => r.nodes?.hold?.status === 'waiting_approval', { label: 'parent at the final gate', timeoutMs });
  const childRun = await get(`/api/runs/${waiting.nodes.sub.childRunId}`);
  assert.equal(childRun.status, 'succeeded', `child run ${childRun.id}`);
  await assertCodeStaysLocal(waiting, { 'parent.txt': 'parent', 'child.txt': 'child' });
  assert.equal(childRun.executionContext?.workspaceRunId, runId, 'child must work in the parent workspace');
  await post(`/api/runs/${runId}/approve`, { nodeId: 'hold' });
  const done = await waitRun(runId, r => r.status === 'succeeded', { label: 'parent succeeded', timeoutMs: 120_000 });
  if (before !== null) assert.equal(localRemoteBranches(), before, 'the remote repository changed');
  return { run: done, child: childRun };
}
