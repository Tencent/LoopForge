// Human editing of a run workspace: a repository-bound run waits at an
// approval gate while a user edits files with compare-and-swap hashes, like an
// AI IDE — editing never pauses the SOP ("takeover" is a no-op kept for older
// clients). The user inspects the diff and hands the workspace back; the run
// then continues and finishes with the edit, without pushing. Editing is not
// exclusive to one person: any non-viewer member may edit, and the content
// hash is what prevents lost updates.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { api, get, post, cleanup, ensureProject, tokenOwner, waitRun, tokenBSkip, asB, withProjectMember } from '../lib/api.mjs';
import { createWorkflow, createRepository, dispatchWithRepository, repoScript, approval, repoSkip, localRemoteBranches } from '../lib/fixtures.mjs';
import { cfg } from '../lib/env.mjs';

let runId;
let dir;
let remoteBefore;
before(ensureProject);
after(cleanup);

const ws = (path = '') => `/api/runs/${runId}/workspace${path}`;
const file = path => get(`${ws('/file')}?path=${encodeURIComponent(dir + path)}`);
const write = (path, content, expectedSha256, opts = {}) => api('PUT', ws('/file'), { path: dir + path, content, expectedSha256 }, opts);

test('setup: a repository-bound run waits at the gate with a written file', { skip: repoSkip, timeout: 600_000 }, async () => {
  remoteBefore = localRemoteBranches();
  const repositoryId = await createRepository('ws-edit-repo');
  const wf = await createWorkflow('ws-edit', [
    repoScript('w', 'echo one > edit.txt'),
    approval('hold', ''),
    // "three" when user B also edited, "two" otherwise.
    repoScript('check', 'grep -qx -e two -e three edit.txt && test "$(cat added.txt)" = "new" && grep -q human README.md && echo HUMAN_EDIT_SEEN'),
  ]);
  runId = await dispatchWithRepository(wf, repositoryId, '人工编辑工作区');
  const run = await waitRun(runId, r => r.nodes?.hold?.status === 'waiting_approval', { label: 'run at the gate' });
  const repo = run.repositories?.[0] || run.repository;
  dir = repo?.workspaceDir ? `${repo.workspaceDir}/` : '';
  assert.equal(String((await file('edit.txt')).content).trim(), 'one');
});


test('viewers cannot write', { skip: repoSkip || tokenBSkip }, async () => {
  const userB = await tokenOwner(cfg.tokenB);
  const restore = await withProjectMember(userB.id, 'viewer');
  await api('POST', ws('/takeover'), {}, asB({ expect: 403 }));
  await api('PUT', ws('/file'), { path: `${dir}edit.txt`, content: 'x' }, asB({ expect: 403 }));
  await api('POST', ws('/release'), {}, asB({ expect: 403 }));
  await restore();
  await api('POST', ws('/takeover'), {}, asB({ expect: 403 }));
  assert.ok(!(await get(`/api/runs/${runId}`)).pauseRequested, 'a refused takeover must not pause the run');
});

test('editing never pauses the run, edits use compare-and-swap, unsafe paths are refused', { skip: repoSkip, timeout: 120_000 }, async () => {
  const me = await tokenOwner();
  assert.equal((await get(ws())).readOnly, false, 'a live SOP workspace must open editable');
  const lease = await post(ws('/takeover'), {});
  assert.equal(lease.editorId, me.id);
  assert.equal((await get(`/api/runs/${runId}`)).pauseRequested, false, 'editing must not pause agents');

  const current = await file('edit.txt');
  assert.match(current.sha256, /^[0-9a-f]{64}$/);
  await write('edit.txt', 'stale\n', '0'.repeat(64), { expect: 409 });
  const written = await write('edit.txt', 'two\n', current.sha256);
  assert.match(written.sha256, /^[0-9a-f]{64}$/);
  assert.notEqual(written.sha256, current.sha256);
  await write('edit.txt', 'lost update\n', current.sha256, { expect: 409 });
  // New files must declare that nothing is there yet.
  await write('added.txt', 'new\n', 'missing');
  await write('added.txt', 'again\n', 'missing', { expect: 409 });
  const readme = await file('README.md');
  await write('README.md', `${readme.content}human\n`, readme.sha256);
  await write('.env', 'SECRET=1\n', 'missing', { expect: 409 });
  await api('PUT', ws('/file'), { path: '../escape.txt', content: 'x\n', expectedSha256: 'missing' }, { expect: 409 });
  await api('PUT', ws('/file'), { path: `${dir}../../escape.txt`, content: 'x\n', expectedSha256: 'missing' }, { expect: 409 });
  await api('PUT', ws('/file'), { path: '/etc/escape.txt', content: 'x\n', expectedSha256: 'missing' }, { expect: 409 });
  assert.equal(String((await file('edit.txt')).content).trim(), 'two');
  assert.equal(String((await file('added.txt')).content).trim(), 'new');
});

test('another member may edit the workspace; the hash prevents lost updates', { skip: repoSkip || tokenBSkip }, async () => {
  const userB = await tokenOwner(cfg.tokenB);
  const restore = await withProjectMember(userB.id, 'member');
  const mine = await file('edit.txt');
  const theirs = await api('PUT', ws('/file'), { path: `${dir}edit.txt`, content: 'three\n', expectedSha256: mine.sha256 }, asB());
  assert.match(theirs.sha256, /^[0-9a-f]{64}$/);
  // My copy is now stale, so my write must not silently overwrite theirs.
  await write('edit.txt', 'mine\n', mine.sha256, { expect: 409 });
  assert.equal(String((await file('edit.txt')).content).trim(), 'three');
  await restore();
  await api('PUT', ws('/file'), { path: `${dir}edit.txt`, content: 'x', expectedSha256: theirs.sha256 }, asB({ expect: 403 }));
});

test('workspace projections show the human edit', { skip: repoSkip }, async () => {
  const diff = await get(`${ws('/diff')}?path=${encodeURIComponent(`${dir}README.md`)}`);
  assert.match(diff.diff, /\+human/);
  // The original is the committed README of whichever repository the suite
  // uses; it is non-empty and does not carry the edit.
  assert.ok(String(diff.original).length > 0 && !/human/.test(String(diff.original)), 'original must be the committed README');
  assert.match(String(diff.modified), /human/);
  const all = await get(ws('/diff'));
  assert.match(all.diff, /README\.md/);
  const found = await get(`${ws('/search')}?q=human`);
  assert.ok(found.results.length > 0, 'search must find the human edit');
  await api('GET', `${ws('/search')}?q=x`, undefined, { expect: 400 });
  const tree = await get(`${ws('/tree')}?path=${encodeURIComponent(dir)}`);
  assert.ok(JSON.stringify(tree).includes('added.txt'), 'the new file is missing from the tree');
  const prep = await api('GET', ws('/preparation'), undefined, { expect: [200, 409] });
  assert.ok(prep.status, 'preparation state must carry a status');
  assert.ok(await get(ws('/restore')));
});

test('per-file diff of a file the human created shows an empty original', { skip: repoSkip }, async () => {
  const diff = await get(`${ws('/diff')}?path=${encodeURIComponent(`${dir}added.txt`)}`);
  assert.equal(diff.original, '');
  assert.equal(String(diff.modified).trim(), 'new');
});

// Files created by a node are registered with `git add -N`: in the index but
// not in HEAD, so their diff base is empty.
test('per-file diff of a file a node created shows an empty original', { skip: repoSkip }, async () => {
  const diff = await get(`${ws('/diff')}?path=${encodeURIComponent(`${dir}edit.txt`)}`);
  assert.equal(diff.original, '');
  assert.match(String(diff.modified), /two|three/);
});

test('release hands back; resume cannot skip the human gate, approval continues with the edit, nothing pushed', { skip: repoSkip, timeout: 300_000 }, async () => {
  await post(ws('/release'), {});
  // Resume must not skip the human gate.
  await api('POST', `/api/runs/${runId}/resume`, {}, { expect: 400 });
  assert.equal((await get(`/api/runs/${runId}`)).nodes.hold.status, 'waiting_approval');
  await post(`/api/runs/${runId}/approve`, { nodeId: 'hold' });
  // The check node fails unless it reads all the human edits.
  const done = await waitRun(runId, r => r.status === 'succeeded', { label: 'run succeeded after the human edit' });
  assert.equal(done.nodes.check.status, 'succeeded');
  if (remoteBefore !== null) assert.equal(localRemoteBranches(), remoteBefore, 'the remote repository changed');
});
