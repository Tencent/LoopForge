import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { loadApp, settle, chooseOption, WEB_ROOT } from '../lib/load-app.mjs';

const commit = 'abcdef1234567890abcdef1234567890abcdef12';
const hostedSource = {
  id: 'kb-hosted-project-1', baseId: 'kb-project-1', kind: 'hosted', name: '托管文档',
  status: 'ready', indexStatus: 'ready', docCount: 1,
};
const gitSource = {
  id: 'kb-git-demo', baseId: 'kb-project-1', kind: 'git', name: '公开样例',
  gitUrl: 'https://github.com/example/repo.git', branch: 'main', pathPrefix: '',
  status: 'ready', indexStatus: 'ready', docCount: 1,
};
const brokenSource = {
  id: 'kb-git-broken', baseId: 'kb-project-1', kind: 'git', name: '失败来源',
  gitUrl: 'https://github.com/example/missing.git', branch: 'main', pathPrefix: 'docs/',
  status: 'error', indexStatus: 'error', docCount: 0, lastError: '仓库不存在，或该地址需要凭据',
};
const overview = {
  bases: [{
    id: 'kb-project-1', projectId: 'project-1', name: '项目知识库', layer: 'project',
    sources: [hostedSource, gitSource, brokenSource],
  }],
};
const hostedDocs = [{ path: 'guides/new.md', title: '新文档', lineCount: 2, commit: '3', revision: 3 }];
const gitDocs = [{ path: 'docs/guide.md', title: '指南', lineCount: 2, commit }];
const usage = {
  agents: [{ id: 'agent-1', name: 'Developer', workflows: ['Delivery Flow'] }],
  totalAgents: 1,
  activity: [],
};

const docReply = (call) => {
  const source = call.url.searchParams.get('source');
  const pathname = call.url.searchParams.get('path');
  const text = source === 'kb-git-demo' ? '# 指南\n青鸾新羽替换了原来的标记。' : '# 新文档\n第一行正文。';
  return {
    source, path: pathname, commit: source === 'kb-git-demo' ? commit : '3', revision: source === 'kb-git-demo' ? 0 : 3,
    start_line: 1, end_line: 2, line_count: 2,
    citation: `${source}@${source === 'kb-git-demo' ? commit : '3'}:${pathname}#L1-L2`,
    lines: [{ number: 1, text: text.split('\n')[0] }, { number: 2, text: text.split('\n')[1] }],
  };
};

test('knowledge center lists source names, browses Git docs, links citations, and validates input', async (t) => {
  const app = await loadApp({ routes: [
    { method: 'GET', path: '/api/knowledge/bases?projectId=project-1', body: overview },
    { method: 'GET', path: '/api/knowledge/usage?projectId=project-1', body: usage },
    { method: 'GET', path: '/api/knowledge/submissions?projectId=project-1', body: [] },
    { method: 'GET', path: '/api/knowledge/candidates?projectId=project-1', body: [] },
    { method: 'GET', path: /^\/api\/knowledge\/bases\/documents\?/, body: (call) => {
      const source = call.url.searchParams.get('source');
      return source === 'kb-git-demo' ? gitDocs : source === 'kb-hosted-project-1' ? hostedDocs : [];
    } },
    { method: 'GET', path: /^\/api\/knowledge\/bases\/document\?/, body: docReply },
    { method: 'POST', path: '/api/knowledge/bases/documents', body: { source: 'kb-hosted-project-1', path: 'guides/new.md', commit: '4' } },
    { method: 'POST', path: '/api/knowledge/bases/sources', body: { id: 'kb-git-new', kind: 'git', status: 'pending' } },
    { method: 'POST', path: '/api/knowledge/bases/sync', body: { id: 'kb-git-demo', status: 'pending' } },
    { method: 'DELETE', path: /^\/api\/knowledge\/bases\/sources\?/, body: { deleted: true } },
    { method: 'POST', path: '/api/knowledge/bases/search', body: {
      latencyMs: 8,
      sources: ['kb-git-demo'],
      result: { query: '青鸾', mode: 'lexical', degraded: false, results: [{
        source: 'kb-git-demo', commit, path: 'docs/guide.md', title: '指南',
        start_line: 1, end_line: 2, snippet: '青鸾新羽替换了原来的标记。',
        citation: `kb-git-demo@${commit}:docs/guide.md#L1-L2`,
      }] },
    } },
  ] });
  t.after(app.close);
  await settle(3);

  await app.evaluate(`renderKnowledgeView()`);
  await settle(4);
  const body = app.document.querySelector('#knowledgeBody');
  assert.match(body.textContent, /LoopForge|项目知识库/);
  // 来源列表用登记名称，ID 作为次要信息；Git 来源有删除入口，托管来源没有。
  assert.match(body.textContent, /公开样例/);
  assert.match(body.textContent, /失败来源/);
  assert.match(body.textContent, /仓库不存在，或该地址需要凭据/);
  assert.ok(body.querySelector('[data-kb-del="kb-git-broken"]'));
  assert.equal(body.querySelector('[data-kb-del="kb-hosted-project-1"]'), null);
  assert.match(body.textContent, /仓库根目录/);

  // 文档页可以按来源浏览 Git 文档；Git 来源没有新建按钮。
  body.querySelector('[data-kb-tab="docs"]').click();
  await settle(4);
  const docsTab = app.document.querySelector('#knowledgeBody');
  const sourceSelect = docsTab.querySelector('#kbDocsSource');
  assert.ok(sourceSelect);
  assert.deepEqual([...sourceSelect.options].map((o) => o.value), ['kb-hosted-project-1', 'kb-git-demo', 'kb-git-broken']);
  assert.ok(docsTab.querySelector('#kbNewDoc'), '托管来源可以新建文档');
  chooseOption(sourceSelect, 'kb-git-demo');
  await settle(5);
  const gitList = app.document.querySelector('#kbDocsList');
  assert.match(gitList.textContent, /docs\/guide\.md/);
  assert.equal(app.document.querySelector('#kbNewDoc'), null);
  gitList.querySelector('[data-kb-doc="0"]').click();
  await settle(4);
  assert.match(app.document.querySelector('#kbDrawerBody').textContent, /青鸾新羽替换了原来的标记/);
  const cite = app.document.querySelector('#kbDrawerSub a[data-kb-cite]');
  assert.ok(cite, 'Git 出处应渲染成链接');
  assert.match(cite.getAttribute('href'), /^https:\/\/github\.com\/example\/repo\/blob\/abcdef1/);
  assert.match(cite.getAttribute('href'), /docs\/guide\.md#L1-L2$/);
  app.document.querySelector('#kbDrawerClose').click();
  await settle(2);

  // 托管来源的文档列表显示文档修订号，不是全库序号。
  chooseOption(app.document.querySelector('#kbDocsSource'), 'kb-hosted-project-1');
  await settle(5);
  assert.match(app.document.querySelector('#kbDocsList').textContent, /修订 3/);

  // 越界路径与错误后缀只显示一条页面内提示，不发起写入。
  app.document.querySelector('#kbNewDoc').click();
  await settle(2);
  const form = app.document.querySelector('#kbDocForm');
  form.elements.path.value = '../secret.md';
  form.elements.content.value = '# 越界';
  app.document.querySelector('#kbDocSave').click();
  await settle(2);
  assert.equal(app.document.querySelector('#kbDocPathError').hidden, false);
  assert.match(app.document.querySelector('#kbDocPathError').textContent, /不能包含/);
  form.elements.path.value = 'notes.txt';
  app.document.querySelector('#kbDocSave').click();
  await settle(2);
  assert.match(app.document.querySelector('#kbDocPathError').textContent, /\.md/);
  assert.equal(app.router.calls.filter((c) => c.method === 'POST' && c.path === '/api/knowledge/bases/documents').length, 0);
  app.document.querySelector('#kbDrawerClose').click();

  // 试搜说明不出现 MySQL/ngram；Git 结果出处是同一段 commit 链接。
  body.querySelector('[data-kb-tab="search"]').click();
  await settle(3);
  const searchTab = app.document.querySelector('#knowledgeBody');
  const note = searchTab.querySelector('.kb-search-note').textContent;
  assert.match(note, /少于两个字/);
  assert.doesNotMatch(note, /MySQL|ngram/i);
  const search = searchTab.querySelector('#kbSearchForm');
  search.elements.q.value = '青鸾';
  search.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(4);
  const hitCite = app.document.querySelector('#kbHits cite a[data-kb-cite]');
  assert.ok(hitCite, '搜索结果里的 Git 出处应可点');
  assert.match(hitCite.getAttribute('href'), /github\.com\/example\/repo\/blob\/abcdef1.*docs\/guide\.md#L1-L2$/);

  // Git 登记地址不触发浏览器原生气泡：不支持的协议和空地址都用页面中文提示。
  body.querySelector('[data-kb-tab="bases"]').click();
  await settle(3);
  const gitForm = app.document.querySelector('#kbGitForm');
  const before = app.router.calls.filter((c) => c.method === 'POST' && c.path === '/api/knowledge/bases/sources').length;
  gitForm.elements.gitUrl.value = 'ftp://example.com/repo.git';
  gitForm.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(2);
  assert.match(app.document.querySelector('#kbGitResult').textContent, /HTTPS 或 SSH 仓库地址/);
  gitForm.elements.gitUrl.value = '';
  gitForm.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(2);
  assert.match(app.document.querySelector('#kbGitResult').textContent, /必填/);
  assert.equal(app.router.calls.filter((c) => c.method === 'POST' && c.path === '/api/knowledge/bases/sources').length, before);

  // 审核空态准确说明：knowledge.md 在闭环后自动入队，Agent 仍只读。
  body.querySelector('[data-kb-tab="distill"]').click();
  await settle(2);
  const audit = app.document.querySelector('#knowledgeBody').textContent;
  assert.match(audit, /knowledge\.md/);
  assert.match(audit, /自动入队/);

  // 角色列表的「配置」列会显示知识库开关，不再与之矛盾。
  const summary = app.evaluate(`agentConfigSummary({knowledgeBase:true})`);
  assert.match(summary, /知识库/);
  assert.doesNotMatch(summary, /true/);
  const flows = readFileSync(path.join(WEB_ROOT, 'app/flows.js'), 'utf8');
  assert.match(flows, /只读检索本项目知识库/);
  assert.match(flows, /自动加入审核队列/);
});

test('shared libraries can be loaded, browsed read-only and managed separately by admins', async (t) => {
  let loaded = false;
  const shared = { id: 'shared', name: '公共规范', layer: 'shared', sources: [{ id: 'shared-docs', baseId: 'shared', kind: 'hosted', name: '公共文档', status: 'ready' }] };
  const app = await loadApp({ routes: [
    { method: 'GET', path: '/api/knowledge/bases?projectId=project-1', body: () => ({ bases: [...overview.bases, ...(loaded ? [shared] : [])] }) },
    { method: 'GET', path: '/api/knowledge/bases?projectId=', body: { bases: [shared] } },
    { method: 'GET', path: '/api/knowledge/bases/bindings?projectId=project-1', body: () => [{ id: 'shared', name: '公共规范', layer: 'shared', loaded }] },
    { method: 'PUT', path: '/api/knowledge/bases/bindings', body: () => { loaded = true; return { updated: true }; } },
    { method: 'GET', path: /^\/api\/knowledge\/bases\/documents\?/, body: gitDocs },
  ] });
  t.after(app.close);
  await app.evaluate('renderKnowledgeView()');
  const toggle = app.document.querySelector('[data-kb-binding="shared"]');
  assert.ok(toggle, '项目管理者能勾选公共库');
  toggle.click(); await settle(6);
  const bindingCall = app.router.calls.find((c) => c.method === 'PUT' && c.url.pathname === '/api/knowledge/bases/bindings');
  assert.deepEqual(bindingCall.body, { projectId: 'project-1', baseId: 'shared', enabled: true });
  app.document.querySelector('[data-kb-tab="docs"]').click(); await settle(3);
  chooseOption(app.document.querySelector('#kbDocsSource'), 'shared-docs'); await settle(3);
  assert.equal(app.document.querySelector('#kbNewDoc'), null, '公共库文档仅可读');
  assert.match(app.document.querySelector('#kbDocsSource').textContent, /公共规范/);
  app.document.querySelector('#kbSharedManage').click(); await settle(5);
  assert.ok(app.document.querySelector('#kbSharedCreateForm'), '管理员在独立范围管理公共库');
  assert.equal(app.document.querySelector('[data-kb-binding]'), null);
});

test('ordinary project members see loaded libraries without management controls', async (t) => {
  const app = await loadApp({ routes: [
    { method: 'GET', path: '/api/knowledge/bases?projectId=project-1', body: overview },
    { method: 'GET', path: '/api/knowledge/bases/bindings?projectId=project-1', body: [{ id: 'shared', name: '公共规范', layer: 'shared', loaded: true }] },
  ] });
  t.after(app.close);
  await app.evaluate('state.authRequired=true;state.currentUser={id:22,isPlatformAdmin:false};state.data.projects=[{id:"project-1",ownerId:1,members:[{userId:22,role:"member"}]}];renderKnowledgeView()');
  assert.equal(app.document.querySelector('#kbSharedManage'), null);
  const toggle = app.document.querySelector('[data-kb-binding="shared"]');
  assert.ok(toggle.checked);
  assert.ok(toggle.disabled);
});

test('knowledge engine settings switch between builtin and LightCloud views', async (t) => {
  const external = { provider: 'lightcloud', url: 'https://kb.example.test/mcp', tokenConfigured: true, enabled: true, writeEnabled: true, defaultSources: ['alpha'], projectSources: { 'project-1': ['alpha'] }, ready: true, canManage: true };
  let settings = { ...external, provider: 'builtin' };
  const app = await loadApp({ routes: [
    { method: 'GET', path: '/api/knowledge/settings', body: () => settings },
    { method: 'PUT', path: '/api/knowledge/settings', body: (call) => {
      settings = { ...external, provider: call.body.provider };
      return settings;
    } },
    { method: 'GET', path: '/api/knowledge/bases?projectId=project-1', body: overview },
    { method: 'GET', path: '/api/knowledge/sources?projectId=project-1', body: [
      { id: 'alpha', name: 'External docs', allowed: true, doc_count: 2 },
      { id: 'beta', name: 'Hidden docs', allowed: false },
    ] },
    { method: 'GET', path: '/api/knowledge/usage?projectId=project-1', body: usage },
  ] });
  t.after(app.close);
  await settle(3);
  await app.evaluate("switchView('knowledge')");
  await settle(8);
  const doc = app.document;
  doc.querySelector('#kbSettings').click();
  assert.equal(doc.querySelector('#kbExternalConfig').hidden, true);
  chooseOption(doc.querySelector('#kbProvider'), 'lightcloud');
  assert.equal(doc.querySelector('#kbExternalConfig').hidden, false);
  assert.equal(doc.querySelector('#kbToken').value, '');
  doc.querySelector('#kbSaveSettings').click();
  await settle(8);
  const body = doc.querySelector('#knowledgeBody');
  assert.match(body.textContent, /External docs/);
  assert.doesNotMatch(body.textContent, /Hidden docs/);
  for (const sel of ['[data-kb-tab="governance"]', '[data-kb-tab="docs"]', '#kbSharedManage']) {
    assert.equal(body.querySelector(sel), null);
  }
  assert.ok(body.querySelector('[data-kb-tab="distill"]'));
  doc.querySelector('#kbSettings').click();
  chooseOption(doc.querySelector('#kbProvider'), 'builtin');
  doc.querySelector('#kbSaveSettings').click();
  await settle(8);
  assert.ok(body.querySelector('[data-kb-tab="governance"]'));
  assert.ok(body.querySelector('[data-kb-tab="docs"]'));
  assert.match(body.textContent, /公开样例/);
});
