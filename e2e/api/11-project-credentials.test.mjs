import { after, test } from 'node:test';
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { cleanup, del, ensureProject, get, post, put, track } from '../lib/api.mjs';
import { cfg, uid } from '../lib/env.mjs';

after(cleanup);

test('project credential creation, renaming, and deletion expose only metadata', async () => {
  await ensureProject();
  const path = `/api/projects/${encodeURIComponent(cfg.projectId)}/credentials`;
  const secret = `test-only-${randomUUID()}`;
  const input = {
    name: uid('credential'),
    kind: 'token',
    fields: [{ name: 'ARTIFACT_TOKEN', value: secret }],
  };
  const created = await post(path, input, { expect: 201, raw: true });
  const { id } = created.data;
  assert.ok(id);
  const itemPath = `${path}/${encodeURIComponent(id)}`;
  track(`credential ${id}`, () => del(itemPath));
  assert.equal(created.data.projectId, cfg.projectId);
  assert.deepEqual(created.data.variables, ['ARTIFACT_TOKEN']);

  const renamed = await put(itemPath, {
    name: input.name,
    kind: input.kind,
    disabled: true,
    fields: [{ name: 'RELEASE_TOKEN', previousName: 'ARTIFACT_TOKEN' }],
  }, { expect: 200, raw: true });
  assert.deepEqual(renamed.data.variables, ['RELEASE_TOKEN']);
  assert.equal(renamed.data.disabled, true);

  const listed = await get(path, { expect: 200, raw: true });
  assert.equal(listed.data.canManage, true);
  const metadata = listed.data.credentials.find(item => item.id === id);
  assert.equal(metadata?.name, input.name);
  assert.equal(metadata.kind, input.kind);
  assert.deepEqual(metadata.variables, ['RELEASE_TOKEN']);
  assert.equal(metadata.disabled, true);
  for (const response of [created, renamed, listed]) {
    assert.equal(response.headers.get('cache-control'), 'no-store');
    assert.ok(!response.text.includes(secret), 'credential values must stay write-only');
  }

  await del(itemPath, { expect: 204 });
  assert.ok(!(await get(path)).credentials.some(item => item.id === id));
});
