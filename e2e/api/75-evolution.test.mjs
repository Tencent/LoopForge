// RSI evolution workbench API lifecycle and route coverage:
// overview, diagnosis, per-SOP policies, asset versions & rollback, autonomy
// policies, samples, and deterministic validation/404 edges for proposals,
// findings, queue items, and run feedback/pins. Never triggers a real model call.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { api, get, post, put, del, track, cleanup, ensureProject } from '../lib/api.mjs';
import { cfg, uid } from '../lib/env.mjs';

const P = cfg.projectId;
const MISSING = `${cfg.prefix}-evo-missing-${cfg.runTag}`;

before(async () => { await ensureProject(); });
after(cleanup);

test('evolution overview, diagnosis, SOP policies, asset versions and rollback', async () => {
  const agentId = uid('evo-agent');
  track(`agent ${agentId}`, () => del(`/api/agents/${agentId}`));
  const baseAgent = {
    id: agentId,
    name: 'E2E 进化角色',
    projectId: P,
    description: 'e2e evolution',
    executorId: cfg.executor,
    prompt: '版本一：请输出简洁结论。',
  };
  const v1 = await put('/api/agents', baseAgent);
  // Edit prompt once so immutable asset_versions records baseline v1 + manual v2.
  await put('/api/agents', { ...v1, prompt: '版本二：先看目录摘要再定位问题。' });

  const wfId = uid('evo-sop');
  track(`workflow ${wfId}`, () => del(`/api/workflows/${wfId}`));
  await put('/api/workflows', {
    id: wfId,
    name: 'E2E 进化 SOP',
    projectId: P,
    description: 'e2e evolution workflow',
    status: 'draft',
    revision: 0,
    sandboxProfileId: cfg.sandbox,
    nodes: [
      { id: 'work', name: '处理', type: 'agent', agentId, executorId: cfg.executor, maxAttempts: 1, maxReworks: 0, position: { x: 0, y: 0 } },
    ],
    edges: [],
  });

  // Overview, list, findings, proposals, policies, samples
  const overview = await get(`/api/evolution/overview?projectId=${P}`);
  assert.equal(overview.projectId, P);
  assert.ok(Array.isArray(overview.sops));
  assert.ok(overview.sops.some(s => s.workflowId === wfId));

  const sops = await get(`/api/evolution/sops?projectId=${P}`);
  assert.ok(Array.isArray(sops));
  const detail = await get(`/api/evolution/sops/${wfId}?projectId=${P}`);
  assert.equal(detail.workflowId, wfId);
  await api('GET', `/api/evolution/sops/${MISSING}?projectId=${P}`, undefined, { expect: 404 });

  assert.ok(Array.isArray(await get(`/api/evolution/findings?projectId=${P}`)));
  assert.ok(Array.isArray(await get(`/api/evolution/proposals?projectId=${P}`)));
  assert.ok(Array.isArray(await get(`/api/evolution/policies?projectId=${P}`)));
  assert.ok(Array.isArray(await get(`/api/evolution/samples?projectId=${P}`)));

  // Deterministic diagnosis (no model call) & legacy project SOP detection toggle off
  const diag = await post('/api/evolution/diagnose', { projectId: P });
  assert.ok(Array.isArray(diag.findings));
  const detect = await put(`/api/evolution/sop-detection?projectId=${P}`, { enabled: false, model: '' });
  assert.equal(detect.enabled, false);

  // Per-SOP policy save (kept disabled so no background optimization fires) + validation edges
  const savedPolicy = await put(`/api/evolution/sops/${wfId}/policy`, {
    projectId: P,
    enabled: false,
    preferences: ['tokens', 'tools'],
    batchSize: 15,
    model: '',
  });
  assert.equal(savedPolicy.enabled, false);
  assert.equal(savedPolicy.batchSize, 15);
  assert.deepEqual(savedPolicy.preferences, ['tokens', 'tools']);

  await api('PUT', `/api/evolution/sops/${MISSING}/policy`, { projectId: P, enabled: false, batchSize: 10 }, { expect: 404 });
  await api('PUT', `/api/evolution/sops/${wfId}/policy`, { projectId: P, enabled: false, model: 'bad model with spaces!' }, { expect: 400 });

  // Optimize refuses disabled SOP (409) and unknown SOP (404) without invoking any model
  await api('POST', `/api/evolution/sops/${wfId}/optimize`, { projectId: P }, { expect: [400, 409] });
  await api('POST', `/api/evolution/sops/${MISSING}/optimize`, { projectId: P }, { expect: 404 });

  // Per-asset autonomy policy update
  const pol = await put(`/api/evolution/policies/agent/${agentId}`, { level: 1 });
  assert.equal(pol.level, 1);

  // Immutable asset versions timeline & rollback to v1
  const versions = await get(`/api/evolution/assets/agent/${agentId}/versions`);
  assert.equal(versions.assetId, agentId);
  assert.ok((versions.versions || []).length >= 2, 'editing agent must record baseline + manual versions');
  const rolled = await post(`/api/evolution/assets/agent/${agentId}/rollback`, { version: 1, note: 'e2e rollback' });
  assert.equal(rolled.source, 'rollback');
  assert.equal((await get(`/api/agents/${agentId}`)).prompt, baseAgent.prompt);
});

test('evolution findings, proposals, queue and run feedback/pin edges reject missing resources cleanly', async () => {
  await api('GET', `/api/evolution/findings/${MISSING}/runs`, undefined, { expect: 404 });
  await api('POST', `/api/evolution/findings/${MISSING}/dismiss`, { reason: 'e2e' }, { expect: 404 });
  await api('POST', `/api/evolution/findings/${MISSING}/snooze`, { days: 7 }, { expect: 404 });

  await api('POST', '/api/evolution/proposals', { projectId: P, findingId: MISSING }, { expect: [400, 404] });
  await api('GET', `/api/evolution/proposals/${MISSING}`, undefined, { expect: 404 });
  await api('GET', `/api/evolution/proposals/${MISSING}/replays/${MISSING}/compare`, undefined, { expect: 404 });
  await api('POST', `/api/evolution/proposals/${MISSING}/approve`, {}, { expect: 404 });
  await api('POST', `/api/evolution/proposals/${MISSING}/canary/traffic`, { percent: 25 }, { expect: 404 });
  await api('POST', `/api/evolution/proposals/${MISSING}/reject`, { note: 'e2e' }, { expect: 404 });

  await api('POST', `/api/evolution/queue/${MISSING}/start`, {}, { expect: 404 });
  await api('POST', `/api/evolution/queue/${MISSING}/reject`, {}, { expect: 404 });

  await api('POST', `/api/runs/${MISSING}/feedback`, { detail: 'e2e feedback' }, { expect: [400, 404] });
  await api('POST', `/api/runs/${MISSING}/pin-sample`, { note: 'e2e' }, { expect: 404 });
  await api('DELETE', `/api/runs/${MISSING}/pin-sample`, undefined, { expect: [200, 204, 404] });
  await api('GET', `/api/runs/${MISSING}/asset-pins`, undefined, { expect: [200, 404] });
});
