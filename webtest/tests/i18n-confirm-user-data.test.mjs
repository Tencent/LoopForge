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

// The bot delete and manual-pass confirmations also quote user-entered names.
test('bot delete and manual-pass confirmations quote the name in English and pass messageData', async (t) => {
  const app = await loadApp({ lang: 'en' });
  t.after(app.close);
  app.evaluate(`
    window.__captured=[];
    afConfirm=(options)=>{window.__captured.push(options);return Promise.resolve(false)};
    botUI.busy=false;botUI.deletingId='';
    state.data.runs=[{id:'run-pass',workflowSnapshot:{nodes:[{id:'node-1',name:${JSON.stringify(NAME)},type:'script'}]}}];
  `);
  await app.evaluate(`botDelete({id:'bot-1',name:${JSON.stringify(NAME)}},null)`);
  await app.evaluate(`passFailedNode('run-pass','node-1',null)`);
  const captured = app.evaluate(`window.__captured`);
  assert.equal(captured.length, 2, 'both confirmations must be shown');
  for (const opt of captured) {
    assert.equal(opt.messageData, true);
    assert.ok(opt.message.includes(`"${NAME}"`), opt.message);
    assert.doesNotMatch(opt.message.replace(NAME, ''), /[一-鿿]/, opt.message);
  }
  assert.equal(captured[0].danger, true, 'deleting a bot is destructive');
});

test('English requirement delete prompts keep sentence spacing and English name lists', async (t) => {
  const app = await loadApp({ lang: 'en' });
  t.after(app.close);
  for (const force of [false, true]) {
    for (const external of [false, true]) {
      const text = app.evaluate(`issueDeletionWarningEn(${force},${external})`);
      assert.doesNotMatch(text, /\.\S/, `missing space after a sentence: ${text}`);
    }
  }
  app.evaluate(`
    window.__captured=[];
    afConfirm=(options)=>{window.__captured.push(options);return Promise.resolve(false)};
    state.data.issues=Array.from({length:6},(_,i)=>({id:'bulk-'+i,title:'需求 '+i,projectId:state.projectFilter,status:'backlog',source:'local',deletionAllowed:true}));
    state.issueSelection=new Set(state.data.issues.map(i=>i.id));state.issueView='table';setIssueSelectionMode(true);
    state.issueSelection=new Set(state.data.issues.map(i=>i.id));
  `);
  await app.evaluate(`deleteSelectedIssues()`);
  const [opt] = app.evaluate(`window.__captured`);
  assert.ok(opt, 'bulk delete must ask for confirmation');
  assert.equal(opt.messageData, true);
  assert.match(opt.message, /\("需求 0", "需求 1", "需求 2", "需求 3", "需求 4", and more\)\? The requirement/);
  assert.doesNotMatch(opt.message, /[、等]|\.\S/);
});
