import { put, post, del, track, get } from './api.mjs';
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

export async function createAgent(label, instructions, extra = {}) {
  const id = uid(label);
  track(`agent ${id}`, () => del(`/api/agents/${id}`));
  await put('/api/agents', {
    id, name: `E2E ${label}`, projectId: cfg.projectId, description: 'e2e', executorId: cfg.executor, instructions, ...(cfg.modelID ? {model:cfg.modelID} : {}), ...extra,
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
