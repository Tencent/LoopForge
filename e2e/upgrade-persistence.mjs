// Real release upgrade fixture. State contains disposable passwords/cookies;
// it stays in a protected temporary directory and is never uploaded or logged.
import assert from 'node:assert/strict';
import {randomBytes} from 'node:crypto';
import fs from 'node:fs';

const [phase, stateFile] = process.argv.slice(2);
const base = process.env.LOOPFORGE_TEST_URL;
if (!['prepare', 'verify'].includes(phase) || !base || !stateFile) {
  throw new Error('Expected prepare/verify, a protected state path and LOOPFORGE_TEST_URL');
}
let state = phase === 'prepare' ? {} : JSON.parse(fs.readFileSync(stateFile, 'utf8'));
async function request(method, path, body, cookie = state.cookie) {
  const headers = {Origin: base};
  if (cookie) headers.Cookie = cookie;
  const form = body instanceof FormData;
  if (body !== undefined && !form) headers['Content-Type'] = 'application/json';
  const response = await fetch(base + path, {
    method, headers, body: body === undefined ? undefined : form ? body : JSON.stringify(body),
    signal: AbortSignal.timeout(15_000),
  });
  assert.ok(response.ok, `${method} ${path}: HTTP ${response.status}`);
  return response;
}
async function json(method, path, body) {
  return (await request(method, path, body)).json();
}
async function waitRun(id, status) {
  const deadline = Date.now() + 120_000;
  while (Date.now() < deadline) {
    const run = await json('GET', '/api/runs/' + id);
    if (run.status === status) return run;
    assert.ok(!['failed', 'cancelled'].includes(run.status), `Run ${id}: ${run.status}`);
    await new Promise(resolve => setTimeout(resolve, 1000));
  }
  throw new Error(`Run ${id} did not reach ${status}`);
}
function save() {
  fs.writeFileSync(stateFile, JSON.stringify(state), {mode: 0o600});
  fs.chmodSync(stateFile, 0o600);
}
const projectId = 'upgrade-persistence';
const workflowId = 'upgrade-persistence';
const workspaceText = 'workspace retained across upgrade\n';
const conversationText = '用户升级前保存的对话';
const attachmentText = 'attachment retained across upgrade\n';

if (phase === 'prepare') {
  assert.equal((await json('GET', '/api/auth/setup')).needsSetup, true);
  state.login = 'upgrade-admin';
  state.password = randomBytes(24).toString('base64url');
  const registration = await request('POST', '/api/auth/register', {login: state.login, password: state.password});
  const account = await registration.json();
  assert.equal(account.admin, true);
  state.userId = account.user.id;
  state.cookie = registration.headers.get('set-cookie').split(';')[0];
  await json('POST', '/api/projects', {
    id: projectId, name: '升级保留项目', description: 'User data created on the previous release', ownerId: state.userId,
  });
  state.issue = await json('POST', '/api/issues', {projectId, title: '升级保留需求', description: 'Original requirement', kind: 'issue', status: 'todo'});
  await json('POST', `/api/issues/${state.issue.id}/comments`, {body: '升级前的评论'});
  const rules = await json('GET', '/api/rules');
  const rule = (Array.isArray(rules) ? rules : rules.items).find(item => item.id === 'public-engineering');
  assert.ok(rule, 'The old installer must install its starter catalog');
  state.ruleContent = rule.content + '\nOperator customization before upgrading.';
  await json('PUT', '/api/rules', {...rule, projectId, content: state.ruleContent});
  state.sessionId = 'upgrade-persistence';
  await json('PUT', '/api/assistant/sessions/' + state.sessionId, {
    projectId, title: '升级保留对话', messages: [{role: 'user', content: conversationText}],
  });
  const form = new FormData();
  form.set('projectId', projectId);
  form.set('file', new Blob([attachmentText]), 'upgrade.txt');
  state.attachmentId = (await json('POST', '/api/assistant/attachments', form)).id;
  assert.ok(state.attachmentId);
  await json('PUT', '/api/workflows', {
    id: workflowId, projectId, name: '升级前后脚本与审批', status: 'draft', sandboxProfileId: 'local',
    nodes: [
      {id: 'prepare', name: 'Write workspace', type: 'script', scriptLanguage: 'shell', script: "printf 'workspace retained across upgrade\\n' > upgrade.txt", maxAttempts: 1},
      {id: 'gate', name: 'Wait across upgrade', type: 'approval', maxAttempts: 1},
      {id: 'finish', name: 'Read retained workspace', type: 'script', scriptLanguage: 'shell', script: 'test "$(cat upgrade.txt)" = "workspace retained across upgrade"', maxAttempts: 1},
    ],
    edges: [{id: 'e1', source: 'prepare', target: 'gate', when: 'success'}, {id: 'e2', source: 'gate', target: 'finish', when: 'success'}],
  });
  await json('POST', `/api/workflows/${workflowId}/publish`, {});
  const run = await json('POST', '/api/runs', {workflowId, projectId, input: 'Upgrade acceptance without a model key'});
  state.runId = run.id;
  await waitRun(state.runId, 'waiting');
  save();
  console.log('Prepared account, project, requirement/comment, customized catalog, conversation, attachment and waiting SOP.');
} else {
  assert.equal((await json('GET', '/api/auth/setup')).needsSetup, false, 'An upgrade must not reset first-account setup');
  assert.equal((await json('GET', '/api/auth/current')).user.id, state.userId, 'The pre-upgrade login cookie must still work');
  const login = await json('POST', '/api/auth/password', {login: state.login, password: state.password});
  assert.equal(login.user.id, state.userId, 'The old password must still work');
  assert.equal(login.user.isPlatformAdmin, true, 'The existing administrator must retain administration');
  const project = await json('GET', '/api/projects/' + projectId);
  assert.equal(project.ownerId, state.userId);
  assert.equal(project.description, 'User data created on the previous release');
  const {issue, comments} = await json('GET', '/api/issues/' + state.issue.id);
  assert.equal(issue.title, state.issue.title);
  assert.equal(issue.number, state.issue.number);
  assert.ok(comments.some(comment => comment.body === '升级前的评论'));
  const rules = await json('GET', '/api/rules');
  assert.equal((Array.isArray(rules) ? rules : rules.items).find(rule => rule.id === 'public-engineering')?.content, state.ruleContent, 'Reinstalling seeds must preserve operator edits');
  const session = await json('GET', '/api/assistant/sessions/' + state.sessionId);
  assert.equal(session.messages[0].content, conversationText);
  assert.equal(await (await request('GET', '/api/assistant/attachments/' + state.attachmentId)).text(), attachmentText);
  const file = await json('GET', `/api/runs/${state.runId}/workspace/file?path=upgrade.txt`);
  assert.equal(file.content, workspaceText);
  if (!state.completed) {
    const waiting = await json('GET', '/api/runs/' + state.runId);
    assert.equal(waiting.status, 'waiting');
    assert.equal(waiting.nodes.gate.status, 'waiting_approval');
    await json('POST', `/api/runs/${state.runId}/approve`, {nodeId: 'gate'});
    await waitRun(state.runId, 'succeeded');
    const fresh = await json('POST', '/api/runs', {workflowId, projectId, input: 'New task after upgrading'});
    state.freshRunId = fresh.id;
    await waitRun(fresh.id, 'waiting');
    await json('POST', `/api/runs/${fresh.id}/approve`, {nodeId: 'gate'});
    await waitRun(fresh.id, 'succeeded');
    state.completed = true;
    save();
  } else {
    assert.equal((await json('GET', '/api/runs/' + state.runId)).status, 'succeeded');
    assert.equal((await json('GET', '/api/runs/' + state.freshRunId)).status, 'succeeded');
  }
  console.log('Verified existing login/password and user data, preserved catalog edits, resumed old SOP and completed a new SOP.');
}
