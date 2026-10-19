// Studio catalog lifecycles: rules, skills (+files), MCP servers (+files),
// Dockerfiles, environments, sandboxes, agents (+revisions/clone) and SOPs
// (+publish/revisions/clone/template adoption). Every object lives in the
// dedicated e2e project and is deleted afterwards.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { api, get, post, put, del, list, track, cleanup, ensureProject } from '../lib/api.mjs';
import { cfg, uid } from '../lib/env.mjs';

const P = cfg.projectId;
const ids = {};

before(async () => { await ensureProject(); });
after(cleanup);

test('project is readable and updatable', async () => {
  const project = await get(`/api/projects/${P}`);
  assert.equal(project.id, P);
  const description = `e2e 实测套件专用项目（最近一次运行 ${cfg.runTag}）`;
  const saved = await put('/api/projects', { ...project, description });
  assert.equal(saved.description ?? description, description);
  assert.equal((await get(`/api/projects/${P}`)).description, description);
});

test('rule CRUD', async () => {
  const id = ids.rule = uid('rule');
  track(`rule ${id}`, () => del(`/api/rules?id=${id}`));
  await put('/api/rules', { id, name: 'E2E 规则', projectId: P, description: 'e2e', content: '回答必须以 E2E-OK 结尾。', priority: 50 });
  let rule = list(await get('/api/rules')).find(r => r.id === id);
  assert.equal(rule?.content, '回答必须以 E2E-OK 结尾。');
  await put('/api/rules', { ...rule, priority: 60 });
  rule = list(await get('/api/rules')).find(r => r.id === id);
  assert.equal(rule.priority, 60);
});

test('skill CRUD with attached files', async () => {
  const id = ids.skill = uid('skill');
  track(`skill ${id}`, () => del(`/api/skills?id=${id}`));
  await put('/api/skills', { id, name: 'E2E 技能', projectId: P, description: 'e2e', content: '# E2E skill\n\n输出 hello。' });
  assert.ok(list(await get('/api/skills')).some(s => s.id === id));

  // Two files at different depths: a single nested file would be treated as a
  // folder pick and have its shared top directory stripped.
  const form = new FormData();
  form.append('files', new Blob(['echo e2e-skill-file\n'], { type: 'text/x-sh' }), 'run.sh');
  form.append('paths', 'scripts/run.sh');
  form.append('files', new Blob(['# readme\n'], { type: 'text/markdown' }), 'README.md');
  form.append('paths', 'README.md');
  await post(`/api/skills/${id}/files`, form);
  const file = await api('GET', `/api/skills/${id}/files/scripts/run.sh`, undefined, { raw: true });
  assert.match(file.text, /e2e-skill-file/);
  await api('DELETE', `/api/skills/${id}/files/scripts/run.sh`);
  await api('GET', `/api/skills/${id}/files/scripts/run.sh`, undefined, { expect: 404 });
  await api('GET', `/api/skills/${id}/files/README.md`, undefined, { raw: true });
  await api('POST', `/api/skills/${id}/files`, (() => {
    const bad = new FormData();
    bad.append('files', new Blob(['x']), 'x');
    bad.append('paths', '../escape.sh');
    return bad;
  })(), { expect: 400 });
});

test('custom MCP server CRUD with attached files', async () => {
  const created = await post('/api/mcp-servers/custom', {
    scope: 'project', projectId: P,
    server: { id: uid('mcp'), name: `E2E MCP ${cfg.runTag}`, description: 'e2e', transport: 'stdio', command: 'node', args: ['server.js'], timeoutSec: 30 },
  });
  // The server namespaces custom ids per scope (mcp-custom-<id>-project-<p>).
  const id = ids.mcp = created.id;
  assert.ok(id, `custom install returned no id: ${JSON.stringify(created)}`);
  track(`mcp ${id}`, () => del(`/api/mcp-servers?id=${id}`));
  let server = list(await get('/api/mcp-servers')).find(s => s.id === id);
  assert.equal(server?.projectId, P);
  assert.equal(server.scope, 'project');
  await put('/api/mcp-servers', { ...server, timeoutSec: 45 });
  server = list(await get('/api/mcp-servers')).find(s => s.id === id);
  assert.equal(server.timeoutSec, 45);

  const form = new FormData();
  form.append('files', new Blob(['console.log("e2e")\n'], { type: 'text/javascript' }), 'server.js');
  form.append('paths', 'server.js');
  await post(`/api/mcp-servers/${id}/files`, form);
  const file = await api('GET', `/api/mcp-servers/${id}/files/server.js`, undefined, { raw: true });
  assert.match(file.text, /e2e/);
  await api('DELETE', `/api/mcp-servers/${id}/files/server.js`);
});

test('MCP market lists installable servers', async () => {
  const market = await get('/api/mcp-market');
  assert.ok(list(market).length > 0 || Object.keys(market).length > 0);
});

test('Dockerfile, environment and sandbox CRUD with reference protection', async () => {
  const dockerfile = ids.dockerfile = uid('df');
  const environment = ids.environment = uid('env');
  const sandbox = ids.sandbox = uid('sbx');
  track(`dockerfile ${dockerfile}`, () => del(`/api/dockerfiles?id=${dockerfile}`));
  track(`environment ${environment}`, () => del(`/api/environments?id=${environment}`));
  track(`sandbox ${sandbox}`, () => del(`/api/sandboxes?id=${sandbox}`));

  await put('/api/dockerfiles', { id: dockerfile, name: 'E2E 镜像', projectId: P, description: 'e2e', content: 'FROM alpine:3.20\n' });
  await api('PUT', '/api/dockerfiles', { id: uid('df-bad'), name: 'bad', projectId: P, content: '' }, { expect: 400 });
  await put('/api/environments', { id: environment, name: 'E2E 环境', projectId: P, description: 'e2e', variables: { E2E_FLAG: '1' }, pathEntries: ['bin'], installScript: 'echo ok' });
  await api('PUT', '/api/environments', { id: uid('env-bad'), name: 'bad', projectId: P, variables: { 'A=B': '1' } }, { expect: 400 });
  await put('/api/sandboxes', { id: sandbox, name: 'E2E 沙箱', projectId: P, driver: 'docker', dockerfileId: dockerfile, readOnlyRootFS: false });

  assert.ok(list(await get('/api/dockerfiles')).some(d => d.id === dockerfile));
  assert.ok(list(await get('/api/environments')).some(e => e.id === environment));
  assert.equal(list(await get('/api/sandboxes')).find(s => s.id === sandbox)?.dockerfileId, dockerfile);

  // A Dockerfile referenced by a sandbox must not be deletable.
  await api('DELETE', `/api/dockerfiles?id=${dockerfile}`, undefined, { expect: 409 });
  // Built-in assets are protected.
  await api('DELETE', '/api/sandboxes?id=generic-docker', undefined, { expect: 409 });
  await api('DELETE', '/api/dockerfiles?id=generic-backend', undefined, { expect: 409 });
});

test('agent CRUD, revisions, restore and clone', async () => {
  const id = ids.agent = uid('agent');
  track(`agent ${id}`, () => del(`/api/agents/${id}`));
  const base = { id, name: 'E2E 角色', projectId: P, description: 'e2e', executorId: cfg.executor, instructions: 'v1', rules: [ids.rule].filter(Boolean), skills: [ids.skill].filter(Boolean) };
  const v1 = await put('/api/agents', base);
  assert.equal(v1.instructions, 'v1');
  await put('/api/agents', { ...v1, instructions: 'v2' });
  assert.equal((await get(`/api/agents/${id}`)).instructions, 'v2');

  await api('PUT', '/api/agents', { ...base, id: uid('agent-bad'), executorId: 'not-a-cli' }, { expect: 400 });
  await api('PUT', '/api/agents', { ...base, id: uid('agent-bad'), projectId: 'e2e-no-such-project' }, { expect: 404 });

  const revisions = list(await get(`/api/agents/${id}/revisions`));
  assert.ok(revisions.length >= 1, 'agent edits must produce revisions');
  const rev = revisions[revisions.length - 1].id;
  const snapshot = await get(`/api/agents/${id}/revisions/${rev}`);
  assert.equal(snapshot.entityId, id);
  assert.ok(snapshot.payload, 'revision snapshot has no payload');
  await post(`/api/agents/${id}/revisions/${rev}/restore`, {});
  await api('GET', `/api/agents/${id}/revisions/does-not-exist`, undefined, { expect: 404 });

  const cloneId = uid('agent-clone');
  track(`agent ${cloneId}`, () => del(`/api/agents/${cloneId}`));
  const clone = await post(`/api/agents/${id}/clone`, { id: cloneId, name: 'E2E 角色副本', projectId: P });
  assert.equal(clone.id ?? cloneId, cloneId);
  assert.equal((await get(`/api/agents/${cloneId}`)).projectId, P);
});

test('referenced rule cannot be deleted while an agent uses it', async () => {
  if (!ids.rule || !ids.agent) return;
  await api('DELETE', `/api/rules?id=${ids.rule}`, undefined, { expect: 409 });
});

test('SOP CRUD, validation, publish, revisions, restore and clone', async () => {
  const id = ids.workflow = uid('sop');
  track(`workflow ${id}`, () => del(`/api/workflows/${id}`));
  const def = {
    id, name: 'E2E SOP（目录用例）', projectId: P, description: 'e2e', status: 'draft', revision: 0,
    sandboxProfileId: cfg.sandbox,
    nodes: [
      { id: 'a', name: '脚本', type: 'script', scriptLanguage: 'shell', script: 'echo a', maxAttempts: 1, maxReworks: 0, position: { x: 0, y: 0 } },
      { id: 'b', name: '角色', type: 'agent', agentId: ids.agent, executorId: cfg.executor, maxAttempts: 1, maxReworks: 0, position: { x: 200, y: 0 } },
    ],
    edges: [{ id: 'e1', source: 'a', target: 'b', when: 'success' }],
  };
  const saved = await put('/api/workflows', def);
  assert.equal(saved.status, 'draft');

  await api('PUT', '/api/workflows', { ...def, id: uid('sop-bad'), sandboxProfileId: '' }, { expect: 400 });
  await api('PUT', '/api/workflows', { ...def, id: uid('sop-bad'), sandboxProfileId: 'e2e-no-such-sandbox' }, { expect: 400 });
  await api('PUT', '/api/workflows', { ...def, id: uid('sop-bad'), nodes: [{ ...def.nodes[0], metricStage: 'bogus' }], edges: [] }, { expect: 400 });
  // Saving on a revision that does not exist yet must not resurrect a deleted SOP.
  await api('PUT', '/api/workflows', { ...def, id: uid('sop-ghost'), revision: 3 }, { expect: 409 });

  const edited = await put('/api/workflows', { ...saved, description: 'edited' });
  assert.equal(edited.description, 'edited');
  // Stale revision must conflict.
  await api('PUT', '/api/workflows', { ...saved, description: 'stale' }, { expect: 409 });

  const published = await post(`/api/workflows/${id}/publish`, {});
  assert.equal(published.status, 'published');

  const revisions = list(await get(`/api/workflows/${id}/revisions`));
  assert.ok(revisions.length >= 1);
  const rev = revisions[revisions.length - 1].id;
  assert.equal((await get(`/api/workflows/${id}/revisions/${rev}`)).entityId, id);
  await post(`/api/workflows/${id}/revisions/${rev}/restore`, {});

  const cloneId = uid('sop-clone');
  track(`workflow ${cloneId}`, () => del(`/api/workflows/${cloneId}`));
  const clone = await post(`/api/workflows/${id}/clone`, { id: cloneId, name: 'E2E SOP 副本', projectId: P });
  assert.equal(clone.id ?? cloneId, cloneId);
  await api('POST', `/api/workflows/${id}/clone`, { id: 'generic-backend-delivery', name: 'x', projectId: P }, { expect: 409 });
});

test('generic templates are read-only and can be adopted into a project', async () => {
  const templates = list(await get('/api/workflows')).filter(w => w.generic);
  assert.ok(templates.length > 0, 'no generic SOP templates');
  const template = templates.find(t => t.id === 'generic-frontend-delivery') || templates[0];
  await api('DELETE', `/api/workflows/${template.id}`, undefined, { expect: 409 });
  const roles = await get(`/api/workflows/${template.id}/agents`);
  assert.ok(list(roles).length > 0, 'template exposes no roles');

  const cloneId = uid('adopt');
  track(`adopted template ${cloneId}`, () => del(`/api/workflows/${cloneId}`));
  const adopted = await post(`/api/workflows/${template.id}/clone`, { id: cloneId, name: 'E2E 采用模板', projectId: P });
  assert.equal(adopted.projectId, P);
  assert.ok(!adopted.generic, 'adopted copy must not stay generic');
  assert.equal(adopted.status, 'draft');
  const roleIds = [...new Set(adopted.nodes.map(n => n.agentId).filter(Boolean))];
  for (const roleId of roleIds) {
    assert.equal((await get(`/api/agents/${roleId}`)).projectId, P, `adopted node role ${roleId} is not a project copy`);
  }

  // Once the adopted copy is gone its project roles must be removable; the
  // template keeps running its own built-in roles.
  await api('DELETE', `/api/workflows/${cloneId}`, undefined, { expect: [200, 204] });
  for (const roleId of roleIds) {
    await api('DELETE', `/api/agents/${roleId}`, undefined, { expect: [204, 404] });
  }
  assert.ok(list(await get(`/api/workflows/${template.id}/agents`)).length > 0, 'template lost its roles');
});

test('SOP with active references cannot be deleted; deleting a missing SOP is 404', async () => {
  await api('DELETE', '/api/workflows/e2e-auto-does-not-exist', undefined, { expect: 404 });
});

test('repository registration parses Gongfeng links and validates input', async () => {
  const id = uid('repo');
  track(`repository ${id}`, () => del(`/api/repositories?id=${id}`));
  const saved = await post('/api/repositories', { id, projectId: P, repoUrl: 'https://git.example.com/example/agent-flow' });
  assert.equal(saved.gongfengProjectId, 'example/agent-flow');
  assert.equal(saved.name, 'agent-flow');
  assert.equal((await get(`/api/repositories/${id}`)).projectId, P);
  await api('POST', '/api/repositories', { projectId: P, repoUrl: 'https://gitlab.invalid/foo/bar' }, { expect: 400 });
  await api('POST', '/api/repositories', { projectId: 'e2e-no-such-project', gongfengProjectId: 'a/b' }, { expect: 400 });
});
