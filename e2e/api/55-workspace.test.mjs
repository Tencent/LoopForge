// Real git workspace, optimistic writes, PTY and checkpoint restoration.
import {test, before, after} from 'node:test';
import assert from 'node:assert/strict';
import {api, get, post, put, cleanup, ensureProject, waitRun, waitFor} from '../lib/api.mjs';
import {createWorkflow, script, approval, startRun} from '../lib/fixtures.mjs';

before(ensureProject);
after(cleanup);

test('workspace: git history, protected files, conflicting edits, shell, restore and resume', async () => {
  const wf = await createWorkflow('workspace-edit', [
    script('seed', `git init -q && git config user.email e2e@example.com && git config user.name E2E
printf 'original\\n' > note.txt
git add note.txt && git commit -qm initial
printf 'hidden-e2e-value\\n' > .env
ln -s /etc/passwd outside-link`),
    approval('gate', 'seed'),
    script('verify', 'grep -q edited note.txt && test -f terminal.txt && echo EDIT_ACCEPTED'),
  ]);
  const {id} = await startRun(wf);
  await waitRun(id, r => r.nodes?.gate?.status === 'waiting_approval');
  const root = `/api/runs/${id}/workspace`;
  await post(`${root}/takeover`, {});
  const info = await get(root);
  assert.equal(info.available, true);
  const history = await get(`${root}/log`);
  assert.equal(history.commits[0].message, 'initial');
  assert.ok((await get(`${root}/commit?id=${history.commits[0].id}`)).files.some(f => f.path === 'note.txt'));
  const old = await get(`${root}/file?path=note.txt`);
  assert.equal(old.content, 'original\n');
  const edited = await put(`${root}/file`, {path: 'note.txt', content: 'edited\n', expectedSha256: old.sha256});
  assert.ok(edited.sha256);
  await api('PUT', `${root}/file`, {path: 'note.txt', content: 'stale\n', expectedSha256: old.sha256}, {expect:409});
  const diff = await get(`${root}/diff?path=note.txt`);
  assert.match(diff.diff, /\+edited/);
  const tree = JSON.stringify(await get(`${root}/tree`));
  assert.doesNotMatch(tree, /hidden-e2e-value|"path":"\.env"/);
  for (const path of ['.env', '../outside', 'outside-link']) {
    await api('GET', `${root}/file?path=${encodeURIComponent(path)}`, undefined, {expect:[400,403,404]});
  }
  await api('PUT', `${root}/file`, {path:'../outside', content:'bad'}, {expect:409});
  const terminal = await post(`${root}/terminal`, {operation:'open'});
  assert.ok(terminal.id);
  await post(`${root}/terminal`, {operation:'input', id:terminal.id, data:"printf 'shell-result\\n' > terminal.txt\nprintf 'PTY_RESULT\\n'\n"});
  let output='';
  await waitFor('PTY output', async () => {
    const result = await post(`${root}/terminal`, {operation:'read',id:terminal.id});
    output += result.data ? Buffer.from(result.data,'base64').toString() : '';
    return /PTY_RESULT/.test(output);
  }, {timeoutMs:20_000,intervalMs:300});
  await waitFor('shell file', async () => (await get(`${root}/file?path=terminal.txt`, {expect:[200,400,404]})).content === 'shell-result\n');
  await post(`${root}/release`, {});
  await api('POST', `${root}/terminal`, {operation:'read',id:terminal.id},{expect:409});
  // Remove the intentionally unsafe symlink before the checkpoint is captured.
  await post(`${root}/takeover`,{});
  const shell = await post(`${root}/terminal`,{operation:'open'});
  await post(`${root}/terminal`,{operation:'input',id:shell.id,data:'rm outside-link && touch removed.txt\n'});
  await waitFor('symlink removed',async()=> (await get(`${root}/file?path=removed.txt`,{expect:[200,400,404]})).content === '',{timeoutMs:10_000,intervalMs:300});
  await post(`${root}/release`,{});
  // Resuming an editor must never bypass an ordinary human approval gate.
  await api('POST',`/api/runs/${id}/resume`,{}, {expect:400});
  assert.equal((await get(`/api/runs/${id}`)).nodes.gate.status,'waiting_approval');
  await post(`/api/runs/${id}/approve`,{nodeId:'gate'});
  await waitRun(id,r=>r.status==='succeeded');
  assert.match((await api('GET',`/api/runs/${id}/logs.txt`,undefined,{raw:true})).text,/EDIT_ACCEPTED/);
  await post(`${root}/restore`,{});
  assert.equal((await get(`${root}/file?path=note.txt`)).content,'edited\n');
  await post(`${root}/use`,{});
});
