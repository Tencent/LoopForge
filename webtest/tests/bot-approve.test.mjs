import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';

const root = new URL('../../web/', import.meta.url);
const html = readFileSync(new URL('bot-approve.html', root), 'utf8').replace(/<script[^>]*><\/script>/, '');
const script = readFileSync(new URL('bot-approve.js', root), 'utf8');
const id = 'ab'.repeat(16);

function page(routes, hash = `#draft=${id})`) {
  const dom = new JSDOM(html, { url: `http://127.0.0.1/bot-approve.html${hash}`, runScripts: 'outside-only' });
  const calls = [];
  dom.window.fetch = async (path, options = {}) => {
    const method = options.method || 'GET';
    calls.push({ path, method, body: options.body ? JSON.parse(options.body) : undefined });
    const key = `${method} ${String(path).split('?')[0]}`;
    const [status, body] = routes[key] || [404, { error: 'not found' }];
    return { ok: status < 300, status, json: async () => structuredClone(typeof body === 'function' ? body() : body) };
  };
  dom.window.eval(script);
  return { dom, calls, text: () => dom.window.document.getElementById('content').textContent, $: (s) => dom.window.document.querySelector(s) };
}
const settle = async () => { for (let i = 0; i < 20; i++) await new Promise((r) => setTimeout(r, 0)); };
const pending = { id, status: 'pending', botName: '研发 Bot', projectName: '项目一', operations: ['1. 保存需求：登录优化'], expiresAt: '2026-11-19T12:00:00Z' };

test('approval page confirms once and shows the result', async () => {
  let state = pending;
  const p = page({
    'GET /api/auth/current': [200, { user: { id: 43 } }],
    [`GET /api/bot-drafts/${id}`]: [200, () => state],
    [`POST /api/bot-drafts/${id}/confirm`]: [200, () => (state = { ...pending, status: 'executed', results: ['已完成：保存需求：登录优化'] })],
  });
  await settle();
  assert.match(p.text(), /保存需求：登录优化/);
  assert.match(p.text(), /研发 Bot/);
  assert.equal(p.dom.window.location.hash, '', 'draft id removed from the address bar');
  p.$('#confirm').click();
  await settle();
  assert.match(p.text(), /已执行/);
  assert.match(p.text(), /已完成：保存需求/);
  assert.equal(p.calls.filter((c) => c.method === 'POST').length, 1);
  assert.equal(p.$('#confirm'), null);
});

test('approval page shows invalidation and login requirements', async () => {
  const invalid = page({
    'GET /api/auth/current': [200, { user: { id: 43 } }],
    [`GET /api/bot-drafts/${id}`]: [200, pending],
    [`POST /api/bot-drafts/${id}/confirm`]: [409, { error: '操作已失效：聊天账号的绑定已解除或已变化', code: 'draft_invalidated', draft: { ...pending, status: 'cancelled', reason: '聊天账号的绑定已解除或已变化' } }],
  });
  await settle();
  invalid.$('#confirm').click();
  await settle();
  assert.match(invalid.text(), /已取消/);
  assert.match(invalid.text(), /操作已失效/);
  const guest = page({ 'GET /api/auth/current': [200, { user: null, loginUrl: '/login' }] });
  await settle();
  assert.match(guest.text(), /请登录/);
  const bad = page({}, '#draft=xyz');
  await settle();
  assert.match(bad.text(), /确认链接无效/);
});

test('approval page lets the user pick the SOP of a new requirement and start it', async () => {
  const draft = { ...pending, newIssues: [{ index: 0, title: '保存需求：登录优化', workflowId: '', defaultWorkflowId: 'wf-dev', defaultWorkflowName: '研发交付' }], workflows: [{ id: 'wf-dev', name: '研发交付' }, { id: 'wf-fix', name: '缺陷修复' }, { id: 'tpl', name: '通用 · 后端交付' }] };
  const p = page({
    'GET /api/auth/current': [200, { user: { id: 43 } }],
    [`GET /api/bot-drafts/${id}`]: [200, draft],
    [`POST /api/bot-drafts/${id}/confirm`]: [200, { ...pending, status: 'executed', results: ['已完成：保存需求：登录优化（SOP：缺陷修复）', '已启动：缺陷修复（运行 run-1）'] }],
  });
  await settle();
  const select = p.$('.sop select.wf');
  assert.equal(p.$('.sop select.repo'), null, 'no repository field without repositories');
  assert.equal(select.value, '', 'project default selected');
  assert.equal(select.options[0].textContent, '项目默认 SOP（研发交付）');
  assert.equal(select.options.length, 4);
  select.value = 'wf-fix';
  p.$('.sop input[type=checkbox]').checked = true;
  p.$('#confirm').click();
  await settle();
  const post = p.calls.find((c) => c.method === 'POST');
  assert.deepEqual(post.body, { choices: [{ index: 0, workflowId: 'wf-fix', start: true }] });
  assert.match(p.text(), /已启动：缺陷修复/);
  assert.equal(p.dom.window.document.querySelectorAll('li.ok').length, 2);
});

test('approval page lets the user pick the repository of a new requirement', async () => {
  const draft = { ...pending, newIssues: [{ index: 0, title: '保存需求：登录优化', workflowId: '', defaultWorkflowName: '研发交付', repositoryId: '', defaultRepositoryName: 'lf-loop-sandbox' }], workflows: [{ id: 'wf-dev', name: '研发交付' }], repositories: [{ id: 'repo-main', name: 'lf-loop-sandbox' }, { id: 'repo-web', name: 'web-console' }] };
  const p = page({
    'GET /api/auth/current': [200, { user: { id: 43 } }],
    [`GET /api/bot-drafts/${id}`]: [200, draft],
    [`POST /api/bot-drafts/${id}/confirm`]: [200, { ...pending, status: 'executed', results: ['已完成：保存需求：登录优化（仓库：web-console）（SOP：研发交付）'] }],
  });
  await settle();
  assert.match(p.text(), /新需求使用的仓库和 SOP/);
  const repo = p.$('.sop select.repo');
  assert.equal(repo.value, '', 'project default repository selected');
  assert.equal(repo.options[0].textContent, '项目默认仓库（lf-loop-sandbox）');
  assert.equal(repo.options.length, 3);
  repo.value = 'repo-web';
  p.$('#confirm').click();
  await settle();
  const post = p.calls.find((c) => c.method === 'POST');
  assert.deepEqual(post.body, { choices: [{ index: 0, workflowId: '', start: false, repositoryId: 'repo-web' }] });
  assert.match(p.text(), /仓库：web-console/);
});

test('approval page offers no default repository when the project has none', async () => {
  const draft = { ...pending, newIssues: [{ index: 0, title: '保存需求：登录优化', workflowId: '', defaultWorkflowName: '研发交付', repositoryId: 'repo-web' }], repositories: [{ id: 'repo-web', name: 'web-console' }] };
  const p = page({ 'GET /api/auth/current': [200, { user: { id: 43 } }], [`GET /api/bot-drafts/${id}`]: [200, draft] });
  await settle();
  const repo = p.$('.sop select.repo');
  assert.equal(repo.options[0].textContent, '不绑定仓库（项目未设默认仓库）');
  assert.equal(repo.value, 'repo-web', 'repository named in chat preselected');
});
