// SOP proposals and AI assistant preferences against a real MySQL platform.
import {test, before, after} from 'node:test';
import assert from 'node:assert/strict';
import {api, get, post, put, del, track, cleanup, ensureProject} from '../lib/api.mjs';
import {cfg, uid} from '../lib/env.mjs';

before(ensureProject);
after(cleanup);

function proposalSession(draft) {
  return {
    projectId: cfg.projectId,
    title: 'SOP proposal',
    messages: [
      {role: 'user', content: '做一个积分排行榜'},
      {role: 'assistant', content: JSON.stringify({message: '建议走 SOP', drafts: [draft]})},
    ],
  };
}

const startProposal = {
  tool: 'issues_dispatch',
  path: {id: 'issue-1'},
  proposal: {reason: '整件需求走 SOP', timing: 'start'},
};

test('assistant preferences persist and reject an oversized rule', async () => {
  const initial = await get('/api/assistant/preferences');
  assert.equal(initial.proactiveRoles, false);
  assert.equal(initial.proactiveSops, false);
  const saved = await put('/api/assistant/preferences', {
    proactiveRoles: true,
    proactiveSops: false,
    instructions: '回复先给结论',
  });
  assert.equal(saved.proactiveRoles, true);
  assert.equal(saved.proactiveSops, false);
  assert.equal(saved.instructions, '回复先给结论');
  const again = await get('/api/assistant/preferences');
  assert.deepEqual(
    {proactiveRoles: again.proactiveRoles, proactiveSops: again.proactiveSops, instructions: again.instructions},
    {proactiveRoles: true, proactiveSops: false, instructions: '回复先给结论'},
  );
  await api('PUT', '/api/assistant/preferences', {proactiveRoles: false, proactiveSops: false, instructions: '字'.repeat(4001)}, {expect: 400});
  const kept = await get('/api/assistant/preferences');
  assert.equal(kept.instructions, '回复先给结论');
  await put('/api/assistant/preferences', {proactiveRoles: false, proactiveSops: false, instructions: ''});
});

test('declining a start proposal returns one follow-up and then stays closed', async () => {
  const id = uid('proposal');
  track(`session ${id}`, () => del(`/api/assistant/sessions/${id}`));
  await put(`/api/assistant/sessions/${id}`, proposalSession(startProposal));
  const declined = await post(`/api/assistant/sessions/${id}/proposals`, {
    messageIndex: 1,
    draftIndex: 0,
    decision: 'declined',
  });
  assert.equal(declined.followUp.text, '不走 SOP，这件事由你直接完成。');
  assert.equal(JSON.parse(declined.session.messages[1].content).drafts[0].decision, 'declined');
  await api('POST', `/api/assistant/sessions/${id}/proposals`, {
    messageIndex: 1,
    draftIndex: 0,
    decision: 'accepted',
  }, {expect: 409});
});

test('accepting a proposal records the decision without a follow-up', async () => {
  const id = uid('proposal');
  track(`session ${id}`, () => del(`/api/assistant/sessions/${id}`));
  await put(`/api/assistant/sessions/${id}`, proposalSession(startProposal));
  const accepted = await post(`/api/assistant/sessions/${id}/proposals`, {
    messageIndex: 1,
    draftIndex: 0,
    decision: 'accepted',
  });
  assert.equal(accepted.followUp, null);
  assert.equal(JSON.parse(accepted.session.messages[1].content).drafts[0].decision, 'accepted');
});

test('muting an optional proposal does not continue the turn, and a plain draft is rejected', async () => {
  const id = uid('proposal');
  track(`session ${id}`, () => del(`/api/assistant/sessions/${id}`));
  await put(`/api/assistant/sessions/${id}`, proposalSession({
    tool: 'issues_save',
    body: {projectId: cfg.projectId, title: '积分排行榜', kind: 'requirement', workflowId: 'delivery'},
    proposal: {reason: '可选的后续工作', timing: 'end'},
  }));
  const muted = await post(`/api/assistant/sessions/${id}/proposals`, {
    messageIndex: 1,
    draftIndex: 0,
    decision: 'muted',
  });
  assert.equal(muted.followUp, null);
  assert.equal(JSON.parse(muted.session.messages[1].content).drafts[0].decision, 'muted');

  const plain = uid('proposal');
  track(`session ${plain}`, () => del(`/api/assistant/sessions/${plain}`));
  await put(`/api/assistant/sessions/${plain}`, proposalSession({tool: 'issues_dispatch', path: {id: 'issue-1'}}));
  await api('POST', `/api/assistant/sessions/${plain}/proposals`, {
    messageIndex: 1,
    draftIndex: 0,
    decision: 'declined',
  }, {expect: 400});
  await api('POST', `/api/assistant/sessions/${id}/proposals`, {
    messageIndex: 1,
    draftIndex: 0,
    decision: 'nope',
  }, {expect: 400});
});
