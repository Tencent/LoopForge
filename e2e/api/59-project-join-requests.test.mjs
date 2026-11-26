// The ordinary second account applies; the project owner reviews the request.
// Every request is settled, and the original project roster is restored.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { api, get, post, list, track, cleanup, ensureProject, tokenOwner, tokenBSkip, asB, withProjectMember } from '../lib/api.mjs';
import { cfg } from '../lib/env.mjs';

before(ensureProject);
after(cleanup);

test('project join requests withdraw, reject and approve with a versioned member roster', { skip: tokenBSkip }, async () => {
  const owner = await tokenOwner();
  const applicant = await tokenOwner(cfg.tokenB);
  assert.notEqual(applicant.id, owner.id, 'the applicant must be a separate account');
  assert.equal(applicant.isPlatformAdmin, false, 'the applicant must be an ordinary user');
  await withProjectMember(applicant.id, null);
  const projectPath = `/api/projects/${encodeURIComponent(cfg.projectId)}`;
  const listPath = `/api/project-join-requests?projectId=${encodeURIComponent(cfg.projectId)}`;
  const create = async () => {
    const request = await post(`${projectPath}/join-requests`, { role: 'viewer', reason: `e2e join ${cfg.runTag}` }, asB());
    track(`join request ${request.id}`, () => post(`/api/project-join-requests/${request.id}/withdraw`, {}, asB({ expect: [200, 404, 409] })));
    assert.equal(request.projectId, cfg.projectId);
    assert.equal(request.userId, applicant.id);
    assert.equal(request.role, 'viewer');
    assert.equal(request.status, 'pending');
    return request;
  };

  const withdrawn = await create();
  await post(`${projectPath}/join-requests`, {}, asB({ expect: 409 }));
  assert.ok(list(await get(`${listPath}&status=pending`, asB())).some(r => r.id === withdrawn.id));
  assert.ok(list(await get(`${listPath}&status=pending`)).some(r => r.id === withdrawn.id));
  assert.equal((await post(`/api/project-join-requests/${withdrawn.id}/withdraw`, {}, asB())).status, 'withdrawn');

  const rejected = await create();
  assert.equal((await post(`/api/project-join-requests/${rejected.id}/reject`, {})).status, 'rejected');
  assert.ok(list(await get(`${listPath}&status=rejected`, asB())).some(r => r.id === rejected.id));

  const approved = await create();
  const beforeApproval = await get(projectPath);
  const result = await post(`/api/project-join-requests/${approved.id}/approve`, { role: 'viewer' });
  assert.equal(result.status, 'approved');
  assert.equal(result.role, 'viewer');
  const current = await get(projectPath);
  assert.equal(current.members.find(m => m.userId === applicant.id)?.role, 'viewer');
  assert.notEqual(current.membersVersion, beforeApproval.membersVersion);
  assert.ok(list(await get(`${listPath}&status=approved`)).some(r => r.id === approved.id));
  await post(`${projectPath}/join-requests`, {}, asB({ expect: 409 }));
  await api('PUT', '/api/projects', { ...beforeApproval, members: beforeApproval.members || [], description: 'e2e stale roster' }, { expect: 409 });
  assert.equal((await get(projectPath)).description, beforeApproval.description, 'a stale save must not replace the project roster or settings');
});
