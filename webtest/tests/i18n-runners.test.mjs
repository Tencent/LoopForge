import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const HAN = /[㐀-鿿]/;
const ATTRS = ['placeholder', 'title', 'aria-label', 'data-tip', 'data-tooltip'];

const runner = {
  id: 'runner-mac', name: 'mac-mini', ownerUserId: 1, status: 'online', os: 'darwin', arch: 'arm64',
  runnerVersion: '0.9.0', runLimit: 10, lastSeenAt: new Date().toISOString(), canUseInProject: true,
  allowPrivilegedSandbox: false, sharedProjectIds: ['p1'],
  load: { runningRuns: 1, occupiedRuns: 2, queuedRuns: 1 },
  runtimes: [
    { provider: 'codex', status: 'online', authMode: 'local', version: '1.0' },
    { provider: 'opencode', status: 'online', authMode: 'personal', version: '0.5' },
  ],
};
const offline = { ...runner, id: 'runner-old', name: 'old-box', status: 'offline', runnerVersion: '0.1.0', runtimes: [], sharedProjectIds: [] };
const shared = { ...runner, id: 'runner-team', name: 'team-box', ownerUserId: 2, runtimes: [runner.runtimes[0]] };
const platformRunner = { name: '平台执行机器（云端）', status: 'online', runLimit: 10, canEditLimit: true, allowPrivilegedSandbox: true, load: { runningRuns: 0, occupiedRuns: 1 } };

async function runnersPage(lang, defaults, runners = [runner, offline, shared]) {
  const app = await loadApp({ lang });
  app.router.post('/api/runners/bind-codes', { code: 'BIND123', expiresAt: '2099-01-01T00:00:00Z' });
  app.router.get('/api/runners?projectId=p1', runners);
  app.router.get('/api/runners/default?projectId=p1', { minRunnerVersion: '0.8.0', platformRunner, ...defaults });
  app.evaluate(`
    state.currentUser={id:1,loginName:'owner'};
    state.data.users=[{id:1,loginName:'owner',codexApiKeyConfigured:true}];
    state.data.projects=[{id:'p1',name:'Shop',ownerId:1,members:[]}];
    state.projectFilter='p1';
    document.querySelector('#runnersView').classList.add('active');
  `);
  await app.evaluate('refreshRunners(true)');
  await settle(6);
  return app;
}

// What the English page still shows in Chinese once i18n.js has rewritten the
// text the app rendered (its MutationObserver is stubbed out in these tests).
function leftoverChinese(window, root) {
  const left = [];
  for (const el of [root, ...root.querySelectorAll('*')]) {
    if (el.closest('[data-i18n-skip]')) continue;
    for (const node of el.childNodes) {
      if (node.nodeType === 3 && HAN.test(window.lfTranslate(node.nodeValue))) left.push(node.nodeValue.trim());
    }
    for (const name of ATTRS) {
      const value = el.getAttribute(name);
      if (value && HAN.test(window.lfTranslate(value))) left.push(`${name}="${value}"`);
    }
  }
  return left;
}

for (const [mode, defaults] of [
  ['personal default', { runnerId: 'runner-mac', personalRunnerId: 'runner-mac' }],
  ['project default', { runnerId: 'runner-mac', projectRunnerId: 'runner-mac' }],
  ['auto-assign', { projectRunnerMode: 'auto' }],
  ['offline project default', { runnerId: 'runner-old', projectRunnerId: 'runner-old' }],
]) {
  test(`English 执行机器 page leaves no Chinese (${mode})`, async (t) => {
    const app = await runnersPage('en', defaults);
    t.after(app.close);
    const view = app.document.querySelector('#runnersView');
    assert.match(view.querySelector('#runnersList').textContent, /mac-mini/);
    assert.deepEqual(leftoverChinese(app.window, view), []);
  });
}

test('English 执行机器 page renders whole English phrases, not mixed fragments', async (t) => {
  const app = await runnersPage('en', { runnerId: 'runner-mac', personalRunnerId: 'runner-mac' });
  t.after(app.close);
  const { document } = app;
  const limit = document.querySelector('.runner-platform-card .runner-run-limit label');
  // The label wraps the input; CSS gap spaces the words around it.
  assert.deepEqual([...limit.childNodes].filter(n => n.nodeType === 3).map(n => n.nodeValue), ['Max concurrent', 'runs']);
  assert.equal(limit.querySelector('input').placeholder, 'Unlimited');
  assert.match(document.querySelector('.runner-platform-card .runner-tip').dataset.tip, /^Defaults to 10, shared by every project on the platform\. .*Enter 0 or leave blank for no limit\.$/);
  assert.equal(document.querySelector('.runner-platform-card .runner-load').textContent, 'Running 0 · Occupied 1 / 10');
  assert.equal(document.querySelector('.runner-platform-card .runner-card-name b').textContent, 'Platform runner (cloud)');
  assert.equal(document.querySelector('#runnerPoolStatus').textContent, 'Personal default · mac-mini');
  assert.equal(app.evaluate(`runnerConcurrencyLabel({runLimit:1})`), 'Up to 1 concurrent run');
  assert.equal(app.evaluate(`runnerDefaultBlocker({status:'online',runnerVersion:'0.1.0'})`), 'Runner version 0.1.0 is too old; upgrade to 0.8.0 or later');
  assert.equal(app.evaluate(`runnerLastSeen(new Date(Date.now()-120000).toISOString())`), '2 min ago');
  for (const text of [app.evaluate('runnerKeyDescription("zcode")'), app.evaluate('runnerKeyDescription("pi")'),
    app.evaluate('runnerAuthDescription.local'), app.evaluate('runnerAuthDescription.personal')]) {
    assert.doesNotMatch(text, HAN, text);
  }
});

// A personal Runner that reports a local A2A service gets an extra runtime row.
const a2aRuntime = { provider: 'a2a', status: 'online', authMode: 'local', version: '0.3' };
const a2aRunner = { ...runner, runtimes: [...runner.runtimes, a2aRuntime] };
const a2aDisabled = { ...runner, id: 'runner-off', name: 'off-box', runtimes: [{ ...a2aRuntime, status: 'disabled' }] };
const a2aShared = { ...shared, runtimes: [a2aRuntime] };

test('English 执行机器 page names the local A2A Agent row in English', async (t) => {
  const app = await runnersPage('en', { runnerId: 'runner-mac', personalRunnerId: 'runner-mac' }, [a2aRunner, a2aDisabled, a2aShared]);
  t.after(app.close);
  const { document, window } = app;
  const view = document.querySelector('#runnersView');
  const rows = [...view.querySelectorAll('#runnersList .runner-runtime-row')].filter(row => /A2A/.test(row.textContent));
  // Only the owner's enabled A2A runtime shows; disabled and shared runners do not.
  assert.equal(rows.length, 1);
  assert.equal(rows[0].querySelector('.runner-runtime-name b').textContent, 'Local A2A Agent');
  assert.equal(rows[0].querySelector('.runner-auth-fixed').textContent, 'Local service');
  assert.doesNotMatch(window.lfTranslate(rows[0].textContent), HAN);
  assert.ok(rows[0].closest('.runner-card').textContent.includes('mac-mini'));
  assert.deepEqual(leftoverChinese(window, view), []);
});

test('Chinese 执行机器 page keeps the local A2A Agent row in Chinese', async (t) => {
  const app = await runnersPage('', { runnerId: 'runner-mac', personalRunnerId: 'runner-mac' }, [a2aRunner]);
  t.after(app.close);
  const rows = [...app.document.querySelectorAll('#runnersList .runner-runtime-row')].filter(row => /A2A/.test(row.textContent));
  assert.equal(rows.length, 1);
  assert.equal(rows[0].querySelector('.runner-runtime-name b').textContent, '本机 A2A Agent');
  assert.equal(rows[0].querySelector('.runner-auth-fixed').textContent, '本机服务');
});

test('English 执行机器 dialogs: share, remove and the auth help are English', async (t) => {
  const app = await runnersPage('en', { runnerId: 'runner-mac', personalRunnerId: 'runner-mac' });
  t.after(app.close);
  const { document } = app;
  document.querySelector('[data-runner-auth-help]').click();
  await settle(2);
  assert.equal(document.querySelector('#confirmTitle').textContent, 'Model authentication');
  assert.doesNotMatch(document.querySelector('#confirmMessage').textContent, HAN);
  document.querySelector('#confirmOkBtn').click();
  await settle(4);
  app.evaluate(`window.__share=editRunnerSharing(state.runners[0])`);
  await settle(2);
  assert.equal(document.querySelector('#confirmTitle').textContent, 'Share mac-mini');
  assert.doesNotMatch(document.querySelector('#confirmMessage').textContent, HAN);
  document.querySelector('#confirmCancelBtn').click();
  await app.evaluate('window.__share');
  await settle(4);
  app.evaluate(`window.__remove=removeRunner('runner-old')`);
  await settle(2);
  assert.equal(document.querySelector('#confirmTitle').textContent, 'Remove runner');
  assert.match(document.querySelector('#confirmMessage').textContent, /^Remove old-box\?/);
  document.querySelector('#confirmCancelBtn').click();
  await app.evaluate('window.__remove');
});

test('English 执行机器 page keeps runner and project names as typed', async (t) => {
  const app = await loadApp({ lang: 'en' });
  t.after(app.close);
  app.router.post('/api/runners/bind-codes', { code: 'BIND123', expiresAt: '2099-01-01T00:00:00Z' });
  app.router.get('/api/runners?projectId=p1', [{ ...runner, name: '测试 机器' }]);
  app.router.get('/api/runners/default?projectId=p1', { runnerId: 'runner-mac', personalRunnerId: 'runner-mac', platformRunner });
  app.evaluate(`
    state.currentUser={id:1,loginName:'owner'};
    state.data.users=[{id:1,loginName:'owner',codexApiKeyConfigured:true}];
    state.data.projects=[{id:'p1',name:'Shop 项目',ownerId:1,members:[]}];
    state.projectFilter='p1';
  `);
  await app.evaluate('refreshRunners(true)');
  await settle(6);
  const { document, window } = app;
  // Run i18n.js over the rendered page the way its MutationObserver would.
  const english = el => el.closest('[data-i18n-data]') ? el.textContent : window.lfTranslate(el.textContent);
  const name = document.querySelector('#runnersList .runner-card-name b');
  assert.equal(english(name), '测试 机器');
  assert.equal(english(document.querySelector('#runnerPoolStatus')), 'Personal default · 测试 机器');
  assert.ok(document.querySelector('[data-runner-run-limit]').closest('[data-i18n-data]'), 'the aria-label carries the runner name');
  app.evaluate(`window.__share=editRunnerSharing(state.runners[0])`);
  await settle(2);
  assert.equal(document.querySelector('#confirmTitle').textContent, 'Share 测试 机器');
  assert.equal(english(document.querySelector('#confirmTitle span')), '测试 机器');
  assert.equal(english(document.querySelector('.runner-share-projects label span')), 'Shop 项目');
  document.querySelector('#confirmCancelBtn').click();
  await app.evaluate('window.__share');
});

test('English mode translates the runner default toasts the server sends', async (t) => {
  const app = await loadApp({ lang: 'en', dispatchReady: false });
  t.after(app.close);
  for (const message of ['已设为默认执行机器，你的任务和沙箱将在这台机器上执行', '已切回云端执行', '已设为你在此项目的默认执行机器',
    '已开启此项目 LOOP 自动分配', '已设为此项目使用平台执行机器', '已清除此项目的默认执行机器',
    '自动分配 · 等待可用机器', '自动分配 · 平台云端', '自动分配失败']) {
    assert.doesNotMatch(app.window.lfTranslate(message), HAN, message);
  }
});

test('Chinese 执行机器 page keeps its Chinese copy', async (t) => {
  const app = await runnersPage('', { runnerId: 'runner-mac', personalRunnerId: 'runner-mac' });
  t.after(app.close);
  const { document } = app;
  assert.equal(document.documentElement.dataset.lang, 'zh');
  const limit = document.querySelector('.runner-platform-card .runner-run-limit label');
  assert.deepEqual([...limit.childNodes].filter(n => n.nodeType === 3).map(n => n.nodeValue), ['最多同时运行', '条']);
  assert.equal(document.querySelector('.runner-platform-card .runner-load').textContent, '运行中 0 · 已占 1 / 10');
  assert.equal(document.querySelector('#runnerPoolStatus').textContent, '个人默认 · mac-mini');
  assert.match(document.querySelector('#runnersList').textContent, /还没有机器|mac-mini/);
  assert.equal(app.evaluate(`runnerConcurrencyLabel({runLimit:3})`), '最多同时运行 3 条');
});
