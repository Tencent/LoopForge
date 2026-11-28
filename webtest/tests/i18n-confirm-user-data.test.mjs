import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import { JSDOM } from 'jsdom';
import { loadApp, WEB_ROOT } from '../lib/load-app.mjs';

// In English mode i18n.js rewrites Chinese copy fragment by fragment. A
// confirmation dialog that quotes a project/repository/LOOP name must mark its
// message as user data (messageData) so only whole-string dictionary hits
// apply; otherwise the name is mangled right inside the "delete" prompt.

const NAME = '修复 Inventory.remove 的两个缺陷';
const RAW = `确定删除项目「${NAME}」吗？此操作不可恢复。`;

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

test('root cause: English mode mangles a quoted name unless the text is marked as user data', async (t) => {
  const window = await englishPage();
  t.after(() => window.close());

  // The dictionary really does rewrite fragments of this message.
  assert.notEqual(window.lfTranslate(RAW), RAW);

  const plain = await rendered(window, `<div class="plain">${RAW}</div>`);
  assert.notEqual(plain.querySelector('.plain').textContent, RAW,
    'without data-i18n-data the interpolated name is rewritten');

  const marked = await rendered(window, `<div data-i18n-data class="marked">${RAW}</div>`);
  assert.equal(marked.querySelector('.marked').textContent, RAW,
    'data-i18n-data keeps the whole message, including the user-typed name');
});

test('afConfirm marks the message as user data only when messageData is set', async (t) => {
  const app = await loadApp();
  t.after(app.close);

  app.evaluate(`afConfirm({message:'确定删除项目「${NAME}」吗？',messageData:true,danger:true})`);
  const marked = app.evaluate(`(document.getElementById('confirmMessage').getAttribute('data-i18n-data')!==null)`);
  assert.equal(marked, true, 'messageData:true must set data-i18n-data on #confirmMessage');
  app.evaluate(`document.getElementById('confirmCancelBtn')?.click()`);

  app.evaluate(`afConfirm({message:'确定删除项目「${NAME}」吗？',danger:true})`);
  const unmarked = app.evaluate(`(document.getElementById('confirmMessage').getAttribute('data-i18n-data')!==null)`);
  assert.equal(unmarked, false, 'without messageData the message stays translatable');
  app.evaluate(`document.getElementById('confirmCancelBtn')?.click()`);
});

test('afConfirm treats isDanger like danger', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`afConfirm({message:'确认删除角色「Developer」？',isDanger:true})`);
  assert.equal(app.evaluate(`document.getElementById('confirmTitle').textContent`), '危险操作确认');
  assert.match(app.evaluate(`document.getElementById('confirmOkBtn').className`), /danger-btn/);
  app.evaluate(`document.getElementById('confirmCancelBtn')?.click()`);
});

test('delete-project and delete-repository confirmations quote the name and pass messageData', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.data.projects=[{id:'project-1',name:${JSON.stringify(NAME)}}];
    state.data.repositories=[{id:'repo-1',projectId:'project-1',name:${JSON.stringify(NAME)}}];
    window.__captured=[];
    afConfirm=(options)=>{window.__captured.push(options);return Promise.resolve(false)};
  `);
  await app.evaluate(`deleteProjectAdmin('project-1')`);
  await app.evaluate(`deleteProjectRepository('repo-1')`);
  const captured = app.evaluate(`window.__captured`);
  assert.equal(captured.length, 2);
  for (const opt of captured) {
    assert.equal(opt.messageData, true, 'destructive confirm must protect the quoted name');
    assert.match(opt.message, new RegExp(NAME.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
  }
});
