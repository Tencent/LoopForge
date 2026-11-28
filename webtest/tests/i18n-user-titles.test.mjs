import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import { JSDOM } from 'jsdom';
import { loadApp, WEB_ROOT } from '../lib/load-app.mjs';

// In English mode i18n.js rewrites Chinese copy fragment by fragment. Text a
// person typed — a requirement title or description — may only be replaced as
// a whole dictionary entry, otherwise "修复 X 的两个缺陷" turns into
// "Fix X 的两个缺陷" wherever the title is shown.
async function englishPage() {
  const dom = new JSDOM('<html data-lang="en"><body></body></html>', { runScripts: 'outside-only', url: 'http://127.0.0.1/' });
  for (const file of ['i18n-en.js', 'i18n.js']) {
    new vm.Script(await readFile(path.join(WEB_ROOT, file), 'utf8'), { filename: file }).runInContext(dom.getInternalVMContext());
  }
  return dom.window;
}

async function rendered(window, html) {
  const host = window.document.createElement('div');
  host.innerHTML = html;
  window.document.body.append(host);
  await new Promise((resolve) => window.setTimeout(resolve, 0));
  return host;
}

const TITLE = '修复 Inventory.remove 的两个缺陷';
const DESCRIPTION = `${TITLE}\n\n- 对不存在的 SKU 调用 remove 时直接抛 KeyError\n- qty <= 0 也要拒绝\n\n验收：补充 pytest 单测，全部通过。`;

async function fixture(t) {
  const app = await loadApp();
  t.after(app.close);
  const window = await englishPage();
  t.after(() => window.close());
  // The dictionary does rewrite these fragments in UI copy, so the assertions
  // below are only meaningful while that stays true.
  assert.notEqual(window.lfTranslate(TITLE), TITLE);
  app.evaluate(`
    state.data.issues=[{id:'iss-1',projectId:'project-1',number:7,title:${JSON.stringify(TITLE)},description:${JSON.stringify(DESCRIPTION)},status:'in_progress'}];
    state.data.tasks=[{id:'task-1',issueId:'iss-1',runId:'run-9'}];
  `);
  return { app, window };
}

const run = { id: 'run-9', status: 'waiting', workflowId: 'flow-1', executionContext: { issueId: 'iss-1', projectId: 'project-1' },
  workflowSnapshot: { id: 'flow-1', name: 'Delivery Flow', nodes: [{ id: 'gate', name: 'Review', type: 'approval' }] },
  nodes: { gate: { status: 'waiting_approval' } }, createdAt: '2026-11-26T02:32:06Z' };

test('English mode keeps a requirement title as typed in the run list, the kanban board and the approval queue', async (t) => {
  const { app, window } = await fixture(t);
  const row = await rendered(window, app.evaluate(`runRowHTML(${JSON.stringify(run)},state.data.workflows)`));
  assert.equal(row.querySelector('.run-row-title').textContent, TITLE);
  assert.equal(row.querySelector('.run-row-title').getAttribute('title'), TITLE);
  const card = await rendered(window, app.evaluate(`issueKanbanCard(state.data.issues[0])`));
  assert.equal(card.querySelector('h4').textContent, TITLE);
  const approval = await rendered(window, app.evaluate(`approvalCardHTML(${JSON.stringify(run)})`));
  assert.equal(approval.querySelector('.ap-head b').textContent, TITLE);
});

test('English mode keeps the requirement description as typed in the run header', async (t) => {
  const { app, window } = await fixture(t);
  const header = await rendered(window, app.evaluate(`runHeaderDescription(${JSON.stringify(DESCRIPTION)},${JSON.stringify(TITLE)},false)`));
  assert.equal(header.querySelector('.run-header-copy').textContent, DESCRIPTION.slice(TITLE.length).trim());
  const short = await rendered(window, app.evaluate(`runHeaderDescription('验收：补充单测',${JSON.stringify(TITLE)},false)`));
  assert.equal(short.querySelector('.run-header-copy').textContent, '验收：补充单测');
});

test('English mode still translates a requirement title that is a whole dictionary entry and the scheduled baseline title', async (t) => {
  const { app, window } = await fixture(t);
  const align = { ...run, workflowId: 'scheduled-baseline-align', workflowSnapshot: { id: 'scheduled-baseline-align', name: 'align', nodes: [] }, repository: { name: 'atlas' } };
  const approval = await rendered(window, app.evaluate(`approvalCardHTML(${JSON.stringify(align)})`));
  assert.doesNotMatch(approval.querySelector('.ap-head b').textContent, /[㐀-鿿]/);
  app.evaluate(`state.data.issues[0].title='自由协作'`);
  const row = await rendered(window, app.evaluate(`runRowHTML(${JSON.stringify(run)},state.data.workflows)`));
  assert.doesNotMatch(row.querySelector('.run-row-title').textContent, /[㐀-鿿]/);
});
