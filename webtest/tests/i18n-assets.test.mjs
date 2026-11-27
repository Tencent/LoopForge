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

// Chinese left in product copy. User data (data-i18n-data, data-i18n-skip,
// attributes listed in data-i18n-data-attrs, form values) may stay Chinese.
function leftoverChinese(root) {
  const left = [];
  for (const el of [root, ...root.querySelectorAll('*')]) {
    if (el.closest('[data-i18n-data],[data-i18n-skip],code,textarea,[hidden]')) continue;
    for (const node of el.childNodes) if (node.nodeType === 3 && HAN.test(node.nodeValue)) left.push(node.nodeValue.trim());
    const dataAttrs = (el.getAttribute('data-i18n-data-attrs') || '').split(/\s+/);
    for (const name of ATTRS) {
      const value = el.getAttribute(name);
      if (value && HAN.test(value) && !dataAttrs.includes(name)) left.push(`${name}="${value}"`);
    }
  }
  return left;
}

// Names a Chinese team types: a dictionary word next to a space or ASCII is
// exactly what the fragment translation used to rewrite.
const PROJECT = '全量走查项目';
const RULE = { id: 'py-rule', projectId: 'project-1', name: 'Python 规则', description: '适用于 Python 项目：提交前运行 pytest', content: '# r', priority: 5 };
const SKILL = { id: 'pr-skill', projectId: 'project-1', name: 'PR 澄清', description: '根据 需求 生成验收标准', files: { 'SKILL.md': 'x' } };
const SANDBOX = { id: 'sweep-native', projectId: 'project-1', name: '走查 native 沙箱', description: '用于全量走查的本地沙箱', driver: 'native' };
const SHARED = { id: 'shared-docker', projectId: 'project-2', shared: true, name: '通用后端 容器 沙箱', description: '其他项目 共享', driver: 'docker', dockerfile: { content: 'FROM a\nRUN b\n' } };
const FLOW = '走查 LOOP-CodeBuddy';
const MCP = { id: 'mcp-kb', scope: 'project', projectId: 'project-1', name: 'GitHub 知识库', description: '检索 GitHub 项目 文档', transport: 'stdio', command: 'echo' };

async function assetApp(t) {
  const app = await loadApp();
  t.after(app.close);
  // The app page runs i18n.js in Chinese; lfT answers in English like a page loaded with data-lang="en".
  app.evaluate("window.lfT = (zh, en) => en; toast = () => {};");
  app.evaluate(`
    state.projectFilter='project-1';
    state.data.projects=[{...state.data.projects[0],name:${JSON.stringify(PROJECT)}},{id:'project-2',name:'后端 项目'}];
    state.data.rules=[${JSON.stringify(RULE)}];
    state.data.skills=[${JSON.stringify(SKILL)}];
    state.data.sandboxes=[${JSON.stringify(SANDBOX)},${JSON.stringify(SHARED)}];
    state.data.agents=[{...state.data.agents[0],name:'后端 角色',rules:['py-rule'],skills:['pr-skill']}];
    state.data.workflows=[{...state.data.workflows[0],name:${JSON.stringify(FLOW)},sandboxProfileId:'sweep-native'}];
    state.data.mcpServers=[${JSON.stringify(MCP)}];
  `);
  return app;
}

async function inEnglish(t, html) {
  const en = await englishPage();
  t.after(() => en.close());
  const root = en.document.createElement('div');
  root.innerHTML = html;
  en.document.body.append(root);
  await new Promise((resolve) => en.setTimeout(resolve, 0));
  return root;
}

const pane = (app, sel) => app.document.querySelector(sel).outerHTML;

for (const [type, asset, scopeKey] of [['rules', RULE, 'ruleScope'], ['skills', SKILL, 'skillScope'], ['sandboxes', SANDBOX, 'sandboxScope']]) {
  test(`English ${type} pages keep the names and descriptions people typed and translate the rest`, async (t) => {
    const app = await assetApp(t);
    app.evaluate(`state.assetType='${type}';state.${scopeKey}='project';switchView('${type}');renderAssets()`);
    const list = await inEnglish(t, pane(app, '#assetListPane'));
    const card = list.querySelector(`[data-asset-id="${asset.id}"]`);
    assert.equal(card.querySelector('h3').textContent, asset.name);
    assert.equal(card.querySelector('.asset-card-desc').textContent, asset.description);
    assert.match(card.getAttribute('aria-label'), new RegExp(`^Edit (rule|skill|sandbox) "${asset.name}"$`));
    assert.deepEqual(leftoverChinese(list), []);

    app.evaluate(`state.${scopeKey}='shared';renderAssets()`);
    const shared = await inEnglish(t, pane(app, '#assetListPane'));
    assert.deepEqual(leftoverChinese(shared), []);
    if (type === 'sandboxes') {
      const foreign = shared.querySelector(`[data-asset-id="${SHARED.id}"]`);
      assert.equal(foreign.querySelector('h3').textContent, SHARED.name);
      assert.equal(foreign.getAttribute('aria-label'), `View sandbox "${SHARED.name}"`);
      assert.match(foreign.textContent, /From 后端 项目/, 'the owning project name stays as typed');
      assert.match(foreign.textContent, /Dockerfile · 2 lines/);
    }

    app.evaluate(`state.${scopeKey}='project';openAssetDetail('${asset.id}')`);
    await settle(2);
    const detail = await inEnglish(t, pane(app, '#assetDetailPane'));
    assert.equal(detail.querySelector('.asset-dname').textContent, asset.name);
    assert.equal(detail.querySelector('.asset-dtitle-text p').textContent, asset.description);
    assert.equal(detail.querySelector('#assetCrumbs .is-current').textContent, asset.name);
    const refs = [...detail.querySelectorAll('.asset-refs li span')].map((el) => el.textContent);
    assert.deepEqual(refs, [type === 'sandboxes' ? FLOW : '后端 角色']);
    assert.deepEqual(leftoverChinese(detail), []);

    app.evaluate(`closeAssetDetail({force:true})`);
    await settle(2);
    app.evaluate(`openAssetDetail('__new__')`);
    await settle(2);
    const creating = await inEnglish(t, pane(app, '#assetDetailPane'));
    assert.deepEqual(leftoverChinese(creating), []);
  });
}

test('English asset copy that only appears on demand is translated', async (t) => {
  const app = await assetApp(t);
  const en = await englishPage();
  t.after(() => en.close());
  const texts = JSON.parse(app.evaluate(`
    const out=[];
    for(const type of ['rules','skills','sandboxes']){
      state.assetType=type;
      out.push(assetEmptyHint(),assetSharedDescription(type),assetPageCopy(type).desc,assetGridEmptyHTML());
      state[assetScopeKey(type)]='shared';out.push(assetGridEmptyHTML());state[assetScopeKey(type)]='project';
      out.push(assetDetailHeadHTML({id:'x',name:'n',projectId:'project-1',shared:true},{}));
      out.push(assetDetailHeadHTML({id:'x',name:'n',projectId:'project-2',shared:true},{}));
      out.push(assetRefsCardHTML({id:'none'}));
      out.push(assetOverviewCardHTML({id:'x',projectId:'project-1'}),assetOverviewCardHTML({id:'x',projectId:'project-1',shared:true}));
    }
    JSON.stringify(out);
  `));
  const root = en.document.createElement('div');
  root.innerHTML = texts.map((s) => `<div>${s}</div>`).join('');
  en.document.body.append(root);
  await new Promise((resolve) => en.setTimeout(resolve, 0));
  assert.deepEqual(leftoverChinese(root), []);
});

test('English MCP page keeps server, project and user names as typed', async (t) => {
  const app = await assetApp(t);
  app.evaluate(`state.currentUser={...state.currentUser,loginName:'wang',chineseName:'王 本地',isGuest:false};state.mcpScope='project';switchView('mcp');renderMCPManagement()`);
  const view = await inEnglish(t, pane(app, '#mcpView'));
  const card = view.querySelector('.mcp-market-card.is-installed');
  assert.equal(card.querySelector('h3').textContent, MCP.name);
  assert.equal(card.querySelector('p').textContent, MCP.description);
  assert.equal(card.querySelector('small').textContent, PROJECT);
  assert.equal(view.querySelector('#mcpScopeHint span').textContent, `Added to "${PROJECT}"; it won't appear in other projects.`);
  assert.deepEqual(leftoverChinese(view), []);

  app.evaluate(`state.mcpScope='personal';renderMCPManagement()`);
  const personal = await inEnglish(t, pane(app, '#mcpView'));
  assert.equal(personal.querySelector('#mcpScopeHint span').textContent, 'Only visible to wang(王 本地).', 'a Chinese display name is not rewritten');
  assert.deepEqual(leftoverChinese(personal), []);

  app.evaluate(`openCustomMCPModal()`);
  await settle(2);
  const modal = await inEnglish(t, pane(app, '#customMcpModal'));
  assert.deepEqual(leftoverChinese(modal), []);
});

test('data-i18n-data-attrs keeps only the listed attributes as typed', async (t) => {
  const en = await englishPage();
  t.after(() => en.close());
  const root = en.document.createElement('div');
  root.innerHTML = '<article aria-label="Edit sandbox &quot;走查 native 沙箱&quot;" title="沙箱 管理" data-i18n-data-attrs="aria-label"><span>未共享</span></article>';
  en.document.body.append(root);
  await new Promise((resolve) => en.setTimeout(resolve, 0));
  const card = root.querySelector('article');
  assert.equal(card.getAttribute('aria-label'), 'Edit sandbox "走查 native 沙箱"');
  assert.doesNotMatch(card.getAttribute('title'), HAN, 'unlisted attributes are still product copy');
  assert.doesNotMatch(card.querySelector('span').textContent, HAN, 'child text is still product copy');
});

test('afConfirm marks a message that quotes user data, and clears the mark for the next dialog', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const msg = () => app.document.querySelector('#confirmMessage');
  const first = app.evaluate(`afConfirm({message:'确认删除沙箱「走查 native 沙箱」？',messageData:true})`);
  await settle(2);
  assert.equal(msg().hasAttribute('data-i18n-data'), true);
  app.document.querySelector('#confirmCancelBtn').click();
  assert.equal(await first, false);
  const second = app.evaluate(`afConfirm({message:'确定要继续吗？'})`);
  await settle(2);
  assert.equal(msg().hasAttribute('data-i18n-data'), false);
  app.document.querySelector('#confirmCancelBtn').click();
  await second;
  // afPrompt / afAlert write the shared message node without going through afConfirm.
  for (const open of [`afPrompt({message:'粘贴内容包含 2 个 server'},'')`, `afAlert({message:'粘贴内容包含 2 个 server'})`]) {
    const marked = app.evaluate(`afConfirm({message:'确认删除规则「规则」？',messageData:true})`);
    await settle(2);
    app.document.querySelector('#confirmCancelBtn').click();
    await marked;
    const next = app.evaluate(open);
    await settle(2);
    assert.equal(msg().hasAttribute('data-i18n-data'), false, `${open} is translated normally after a messageData confirm`);
    app.document.querySelector('#confirmCancelBtn').hidden ? app.document.querySelector('#confirmOkBtn').click() : app.document.querySelector('#confirmCancelBtn').click();
    await next;
  }
});

// Keys of 6+ characters are replaced inside any sentence, so this PR must not add
// ones that other pages embed in longer copy without a whole-sentence entry.
test('asset copy adds no dictionary fragments that split other pages\' sentences', async (t) => {
  const en = await englishPage();
  t.after(() => en.close());
  for (const zh of ['放弃未保存的修改？', '已放弃未保存的修改', '跳过会放弃本步骤未保存的修改。', '支持 Markdown 和粘贴图片']) {
    const out = en.lfTranslate(zh);
    assert.ok(out === zh || !HAN.test(out), `${zh} -> ${out}`);
  }
});

// A name or description that is itself a dictionary key («沙箱», «代码审查»)
// still has to stay as typed: data-i18n-data allows whole-string hits, so user
// data is marked data-i18n-skip. Placeholders («暂无说明», «平台») still translate.
test('English asset and MCP pages keep names that are dictionary keys as typed', async (t) => {
  const app = await assetApp(t);
  app.evaluate(`
    state.data.projects=[{...state.data.projects[0],name:'测试'},{id:'project-2',name:'共享'}];
    state.data.sandboxes=[{id:'dict-sbx',projectId:'project-1',name:'沙箱',description:'规则',driver:'native'},{id:'dict-shared',projectId:'project-2',shared:true,name:'代码审查',driver:'native'},{id:'plat',projectId:'platform',shared:true,name:'共享',driver:'native'}];
    state.data.workflows=[{...state.data.workflows[0],name:'执行记录',sandboxProfileId:'dict-sbx'}];
    state.data.mcpServers=[{id:'mcp-dict',scope:'project',projectId:'project-1',name:'代码审查',description:'测试',transport:'stdio',command:'echo'},{id:'mcp-nodesc',scope:'project',projectId:'project-1',name:'规则',transport:'stdio',command:'echo'}];
    state.assetType='sandboxes';state.sandboxScope='project';switchView('sandboxes');renderAssets();
  `);
  const list = await inEnglish(t, pane(app, '#assetListPane'));
  const card = list.querySelector('[data-asset-id="dict-sbx"]');
  assert.equal(card.querySelector('h3').textContent, '沙箱');
  assert.equal(card.querySelector('.asset-card-desc').textContent, '规则');
  assert.equal(card.getAttribute('aria-label'), 'Edit sandbox "沙箱"');
  assert.match(list.querySelector('#assetCount').textContent, /^Sandboxes in this project\d/);

  app.evaluate(`state.sandboxScope='shared';renderAssets()`);
  const shared = await inEnglish(t, pane(app, '#assetListPane'));
  assert.match(shared.querySelector('#assetCount').textContent, /^Shared sandboxes\d/);
  const foreign = shared.querySelector('[data-asset-id="dict-shared"]');
  assert.equal(foreign.querySelector('h3').textContent, '代码审查');
  assert.equal(foreign.querySelector('.asset-card-desc').textContent, 'No description', 'an empty description shows the translated placeholder');
  assert.match(foreign.querySelector('.asset-status').textContent, /^From 共享$/, 'a project named 共享 is not renamed to Shared');
  assert.match(shared.querySelector('[data-asset-id="plat"] .asset-status').textContent, /^From Platform$/, 'platform-owned assets read as Platform, not the raw id');

  app.evaluate(`state.sandboxScope='project';openAssetDetail('dict-sbx')`);
  await settle(2);
  const detail = await inEnglish(t, pane(app, '#assetDetailPane'));
  assert.equal(detail.querySelector('.asset-dname').textContent, '沙箱');
  assert.equal(detail.querySelector('.asset-dtitle-text p').textContent, '规则');
  assert.equal(detail.querySelector('#assetCrumbs .is-current').textContent, '沙箱');
  assert.deepEqual([...detail.querySelectorAll('.asset-refs li span')].map((el) => el.textContent), ['执行记录']);

  const overview = await inEnglish(t, app.evaluate(`assetOverviewCardHTML({id:'x',projectId:'project-2',shared:true})`));
  assert.deepEqual([...overview.querySelectorAll('dd')].map((el) => el.textContent), ['共享', 'Shared by another project']);

  app.evaluate(`state.mcpScope='project';switchView('mcp');renderMCPManagement()`);
  const mcp = await inEnglish(t, pane(app, '#mcpView'));
  const [dict, nodesc] = mcp.querySelectorAll('.mcp-market-card.is-installed');
  assert.equal(dict.querySelector('h3').textContent, '代码审查');
  assert.equal(dict.querySelector('p').textContent, '测试');
  assert.equal(dict.querySelector('small').textContent, '测试', 'a project named 测试 is not renamed to Testing');
  assert.equal(nodesc.querySelector('h3').textContent, '规则');
  assert.equal(nodesc.querySelector('p').textContent, 'No description');
});
