import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

// #198 item 7: the CodeBuddy row on a runner card shows the same network
// environment as 个人配置 → CodeBuddy, so an iOA key used with the wrong
// environment is visible before the machine answers 401.
function renderCodeBuddyRow(app, user) {
  const runner = {
    id: 'runner-cb', name: 'Laptop', ownerUserId: 1, status: 'online', os: 'linux', arch: 'amd64',
    lastSeenAt: new Date().toISOString(),
    runtimes: [
      { provider: 'codebuddy', status: 'online', authMode: 'personal', version: '2.0' },
      { provider: 'codex', status: 'online', authMode: 'local', version: '1.0' },
    ],
  };
  app.evaluate(`
    state.currentUser={id:1,loginName:'owner'};
    state.data.users=[${JSON.stringify({ id: 1, loginName: 'owner', ...user })}];
    state.runners=[${JSON.stringify(runner)}];state.defaultRunnerId='';state.cloudPool={configured:true,online:1};
    renderRunnersView();
  `);
  const list = app.document.querySelector('#runnersList');
  const fixed = list.querySelectorAll('.runner-auth-fixed');
  assert.equal(fixed.length, 1, 'only the CodeBuddy row has a fixed credential label');
  assert.ok(list.querySelector('[data-runner-auth="runner-cb|codex"]'), 'Codex keeps its auth-source select');
  return fixed[0];
}

test('runner card shows the CodeBuddy network environment from personal config', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  for (const [value, label] of [['', '国际站'], ['internal', '国内站'], ['ioa', 'iOA']]) {
    const el = renderCodeBuddyRow(app, { codeBuddyApiKeyConfigured: true, codeBuddyInternetEnvironment: value });
    assert.equal(el.textContent, `个人凭证 · ${label}`);
    assert.equal(el.classList.contains('missing'), false);
    assert.match(el.getAttribute('title'), new RegExp(`网络环境：${label}`));
  }
});

test('runner card flags an unrecognized CodeBuddy network environment', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const el = renderCodeBuddyRow(app, { codeBuddyApiKeyConfigured: true, codeBuddyInternetEnvironment: 'china<b>' });
  assert.equal(el.textContent, '个人凭证 · 网络环境无法识别');
  assert.ok(el.classList.contains('missing'));
  assert.match(el.getAttribute('title'), /「china<b>」无法识别，CodeBuddy 会按国际站连接/);
  assert.equal(el.querySelector('b'), null, 'stored value is escaped');
});

test('runner card keeps the missing-key label when CodeBuddy is not configured', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const el = renderCodeBuddyRow(app, { codeBuddyApiKeyConfigured: false, codeBuddyInternetEnvironment: 'ioa' });
  assert.equal(el.textContent, '个人凭证（未配置）');
  assert.ok(el.classList.contains('missing'));
});
