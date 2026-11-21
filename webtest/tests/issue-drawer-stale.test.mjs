import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('a late detail reply for an issue the reader left does not replace the open one', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const base = app.evaluate('structuredClone(state.data.issues[0])');
  const slow = { ...base, id: 'issue-slow', title: 'Slow issue', tasks: [] };
  const fast = { ...base, id: 'issue-fast', title: 'Fast issue', tasks: [] };
  let releaseSlow;
  app.router.get('/api/issues/issue-slow', () => new Promise((resolve) => {
    releaseSlow = () => resolve({ issue: slow, comments: [] });
  }));
  app.router.get('/api/issues/issue-fast', { issue: fast, comments: [] });

  const opening = app.evaluate(`openIssueDetail('issue-slow','overview')`);
  await settle(2);
  await app.evaluate(`openIssueDetail('issue-fast','overview')`);
  await settle(3);
  assert.equal(app.evaluate('state.issueDetailData?.issue?.id'), 'issue-fast');

  releaseSlow();
  await opening;
  await settle(3);
  assert.equal(app.evaluate('state.selectedIssue'), 'issue-fast');
  assert.equal(app.evaluate('state.issueDetailData?.issue?.id'), 'issue-fast');
  assert.equal(app.document.querySelector('#issueDetailTitle').textContent, 'Fast issue');
});
