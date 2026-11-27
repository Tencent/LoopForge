import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import { JSDOM } from 'jsdom';
import { loadApp, settle, WEB_ROOT } from '../lib/load-app.mjs';

const HAN = /[㐀-鿿]/;
const ATTRS = ['placeholder', 'title', 'aria-label', 'data-tip', 'data-tooltip'];

async function englishPage() {
  const dom = new JSDOM('<html data-lang="en"><body></body></html>', { runScripts: 'outside-only', url: 'http://127.0.0.1/' });
  for (const file of ['i18n-en.js', 'i18n.js']) {
    new vm.Script(await readFile(path.join(WEB_ROOT, file), 'utf8'), { filename: file }).runInContext(dom.getInternalVMContext());
  }
  return dom.window;
}

// Chinese left in product copy; user data (data-i18n-data) may stay Chinese.
function leftoverChinese(root) {
  const left = [];
  for (const el of [root, ...root.querySelectorAll('*')]) {
    if (el.closest('[data-i18n-data],[data-i18n-skip],code')) continue;
    for (const node of el.childNodes) if (node.nodeType === 3 && HAN.test(node.nodeValue)) left.push(node.nodeValue.trim());
    for (const name of ATTRS) {
      const value = el.getAttribute(name);
      if (value && HAN.test(value)) left.push(`${name}="${value}"`);
    }
  }
  return left;
}

const CHAT_TITLE = '先查一下库存估值相关的规则（INV-RULE-9），然后实现';
const ROUTES = [
  { method: 'GET', path: '/api/knowledge/connectors', body: [{
    id: 'kb_obs', name: '团队 Obsidian 笔记', type: 'obsidian', endpoint: 'http://127.0.0.1:27123',
    enabled: true, secretConfigured: true, config: { timeoutSec: 30 },
  }] },
  { method: 'GET', path: '/api/knowledge/usage', body: {
    totalCalls: 6, totalDocumentCalls: 4, totalCitedCount: 1,
    connectors: [{ connectorId: 'kb_obs', connectorName: '团队 Obsidian 笔记', callCount: 6 }],
    documents: [{
      connectorId: 'kb_obs', connectorName: '团队 Obsidian 笔记', sourceName: '库存系统', documentId: 'doc-1',
      title: '库存估值说明', searchHitCount: 2, readCount: 2, citedCount: 1, callCount: 4,
      topCallers: [
        { kind: 'sop', label: '库存缺陷修复 LOOP', subLabel: '开发实现 · 开发实现', callCount: 2, runId: 'run-1', nodeId: 'implement' },
        { kind: 'chat', label: CHAT_TITLE, subLabel: '个人对话', callCount: 1, sessionId: 'ses-1' },
      ],
    }],
  } },
  { method: 'GET', path: /^\/api\/knowledge\/usage\/events/, body: {
    total: 2, hasMore: false,
    items: [
      { id: 1, connectorId: 'kb_obs', connectorName: '团队 Obsidian 笔记', sourceName: '库存系统', documentId: 'doc-1',
        title: '库存估值说明', action: 'read', query: '库存估值', cited: true, calledAt: '2026-11-27T10:30:00Z',
        caller: { kind: 'sop', workflowName: '库存缺陷修复 LOOP', agentName: '开发实现', nodeName: '开发实现', runId: 'run-1', nodeId: 'implement' } },
      { id: 2, connectorId: 'kb_obs', connectorName: '团队 Obsidian 笔记', documentId: 'doc-1', title: '库存估值说明',
        action: 'search', calledAt: '2026-11-27T10:31:00Z', caller: { kind: 'chat', sessionTitle: CHAT_TITLE, sessionId: 'ses-1' } },
    ],
  } },
];

async function renderEnglishKnowledge(t, prepare) {
  const app = await loadApp({ routes: ROUTES });
  t.after(app.close);
  // The app page runs i18n.js in Chinese; switch its lfT to English like a page loaded with data-lang="en".
  app.evaluate("window.lfT = (zh, en) => en; toast = () => {};");
  app.evaluate("switchView('knowledge')");
  await settle(12);
  if (prepare) await prepare(app);
  const en = await englishPage();
  t.after(() => en.close());
  const root = en.document.createElement('div');
  root.innerHTML = app.document.querySelector('#kbHeadDesc').outerHTML + app.document.querySelector('#knowledgeRoot').innerHTML;
  en.document.body.append(root);
  await new Promise(resolve => en.setTimeout(resolve, 0));
  return { app, root };
}

test('English mode leaves no Chinese copy in 我的知识库 (gallery, connector card, usage by document)', async (t) => {
  const { root } = await renderEnglishKnowledge(t);
  assert.deepEqual(leftoverChinese(root), []);
  const text = root.textContent.replace(/\s+/g, ' ');
  assert.match(text, /6 calls in total · 4 document hits\/reads · cited 1 times/);
  assert.match(text, /Timeout: 30s/);
  assert.match(text, /Popular knowledge bases · Quick setup/);
  // User data is shown as written, not run through fragment translation.
  assert.ok(root.querySelector('.kb-card h3, h3[data-i18n-data]').textContent.includes('团队 Obsidian 笔记'));
  const pills = [...root.querySelectorAll('.kb-usage-caller-name')].map(el => el.textContent);
  assert.deepEqual(pills, ['库存缺陷修复 LOOP · 开发实现', CHAT_TITLE]);
});

test('English mode leaves no Chinese copy in the knowledge call history and keeps user titles intact', async (t) => {
  const { root } = await renderEnglishKnowledge(t, async (app) => {
    app.document.querySelector('[data-kb-usage-tab="events"]').click();
    await settle(12);
  });
  assert.ok(root.querySelector('[data-kb-usage-events-table]'));
  assert.deepEqual(leftoverChinese(root), []);
  const text = root.textContent.replace(/\s+/g, ' ');
  assert.match(text, /Showing 2 of 2 \(up to 2,000 records from the last 30 days are kept per user\)/);
  const callers = [...root.querySelectorAll('.kb-usage-caller-main')].map(el => el.textContent);
  assert.deepEqual(callers, ['库存缺陷修复 LOOP', CHAT_TITLE]);
  const subs = [...root.querySelectorAll('.kb-usage-caller-jump .kb-usage-doc-sub')].map(el => el.textContent);
  assert.deepEqual(subs, ['Implementation']);
});

test('caller sub-labels drop repeated role/node names and the kind already shown on the badge', async (t) => {
  const app = await loadApp({ routes: ROUTES });
  t.after(app.close);
  app.evaluate("switchView('knowledge')");
  await settle(12);
  const names = [...app.document.querySelectorAll('.kb-usage-caller-name')].map(el => el.textContent);
  assert.deepEqual(names, ['库存缺陷修复 LOOP · 开发实现', CHAT_TITLE]);
});
