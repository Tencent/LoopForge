import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const settings = {
  ready: true, enabled: true, writeEnabled: true, canManage: true,
  url: 'https://knowledge.example/mcp', tokenConfigured: true,
  defaultSources: ['handbook'], projectSources: { 'project-1': ['handbook'] },
  usageRule: '只读检索，引用来源。', updatedAt: '2026-10-30T08:00:00Z', updatedBy: 'admin',
};
const sources = [
  { id: 'handbook', name: 'Team Handbook', allowed: true, status: 'ready', doc_count: 12 },
  { id: 'drafts', name: 'Drafts', allowed: false, status: 'indexing', doc_count: 0 },
];
const usage = {
  agents: [{ id: 'agent-1', name: 'Developer', workflows: ['Delivery Flow'] }],
  activity: [{ agentId: 'agent-1', tool: 'kb_search', query: 'release checklist', ok: true, hits: 2, at: '2026-10-30T08:00:00Z' }],
};
const submission = { id: 'sub-1', title: 'Release guide', source: 'handbook', path: 'guides/release.md', content: '# Release', reviewNote: '', status: 'pending', runId: 'run-1', updatedAt: '2026-10-30T08:00:00Z' };

test('knowledge center manages connection, roles, review, search, and source documents', async (t) => {
  const app = await loadApp({ routes: [
    { method: 'GET', path: '/api/knowledge/settings', body: settings },
    { method: 'GET', path: '/api/knowledge/usage?projectId=project-1', body: usage },
    { method: 'GET', path: '/api/knowledge/sources?projectId=project-1', body: sources },
    { method: 'GET', path: '/api/knowledge/submissions?projectId=project-1', body: [submission] },
    { method: 'GET', path: '/api/knowledge/candidates?projectId=project-1', body: [{ artifact: { id: 'art-1', name: 'knowledge.md', path: 'runs/knowledge.md', runId: 'run-1', nodeId: 'node-1', createdAt: '2026-10-30T07:00:00Z' } }] },
    { method: 'POST', path: '/api/knowledge/check', body: { ok: true, latencyMs: 18, sources, tools: ['kb_search', 'kb_fetch'] } },
    { method: 'GET', path: '/api/agents/agent-1', body: { id: 'agent-1', name: 'Developer', version: 2, knowledgeBase: true } },
    { method: 'GET', path: '/api/knowledge/submissions/sub-1', body: submission },
    { method: 'PUT', path: '/api/knowledge/submissions/sub-1', body: { ...submission, title: 'Updated release guide' } },
    { method: 'POST', path: '/api/knowledge/search', body: { latencyMs: 21, result: { degraded: true, degrade_reason: 'vector unavailable', results: [{ title: 'Release checklist', heading: 'Before release', source: 'handbook', path: 'guides/release.md', snippet: 'Run all checks', citation: 'handbook:guides/release.md#L2-L3', start_line: 2, end_line: 3 }] } } },
    { method: 'GET', path: '/api/knowledge/document?source=handbook&path=guides%2Frelease.md&projectId=project-1', body: { lines: [{ number: 1, text: '# Release' }, { number: 2, text: 'Run all checks' }, { number: 3, text: 'Publish artifacts' }] } },
  ] });
  t.after(app.close);

  await app.evaluate(`renderKnowledgeView()`);
  assert.match(app.document.querySelector('#knowledgeBody').textContent, /连接配置|本项目可用来源|Team Handbook|handbook/);
  const form = app.document.querySelector('#kbSettingsForm');
  form.elements.url.value = 'https://new-kb.example/mcp';
  form.querySelector('#kbCheckBtn').click();
  await settle(3);
  assert.match(app.document.querySelector('#kbCheckResult').textContent, /连接正常 · 18ms · 2 个来源/);

  app.document.querySelector('[data-kb-tab="usage"]').click();
  assert.match(app.document.querySelector('#knowledgeBody').textContent, /Developer|Delivery Flow|4 个只读工具/);
  app.document.querySelector('[data-kb-inject]').click();
  assert.match(app.document.querySelector('#kbDrawerBody').textContent, /kb_search|kb_write_document|只读检索/);
  app.document.querySelector('#kbDrawerClose').click();

  app.document.querySelector('[data-kb-tab="distill"]').click();
  assert.match(app.document.querySelector('#knowledgeBody').textContent, /knowledge\.md|Release guide|新建/);
  app.document.querySelector('[data-kb-sub="sub-1"]').click();
  await settle(3);
  const editor = app.document.querySelector('#kbEditorForm');
  assert.equal(editor.elements.path.value, 'guides/release.md');
  editor.elements.title.value = 'Updated release guide';
  app.document.querySelector('[data-kb-act="save"]').click();
  await settle(4);
  const saved = app.router.calls.find(c => c.method === 'PUT' && c.path === '/api/knowledge/submissions/sub-1');
  assert.equal(saved.body.title, 'Updated release guide');

  app.evaluate(`kbDrawerClose();kbGoTab('search')`);
  const search = app.document.querySelector('#kbSearchForm');
  search.elements.q.value = 'release checklist';
  search.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(4);
  assert.match(app.document.querySelector('#kbHits').textContent, /Release checklist|语义检索已降级为词法检索|Run all checks/);
  app.document.querySelector('[data-kb-hit="0"]').click();
  await settle(4);
  const doc = app.document.querySelector('#kbDrawerBody');
  assert.match(doc.textContent, /Run all checks|Publish artifacts/);
  assert.ok(doc.querySelector('#kbDocAnchor'));
  const searchCall = app.router.calls.find(c => c.method === 'POST' && c.path === '/api/knowledge/search');
  assert.deepEqual(searchCall.body, { query: 'release checklist', projectId: 'project-1', sources: [] });
});
