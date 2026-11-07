import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('platform admin panel loads metrics, searches, filters, grants, revokes, and navigates', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.currentUser={...state.data.users[0],isGuest:false,isPlatformAdmin:true}`);
  const initial = [
    { userId: 1, loginName: 'example-maintainer', chineseName: '', source: 'builtin', revocable: false },
    { userId: 2, loginName: 'reviewer', chineseName: '审阅者', source: 'granted', revocable: true },
  ];
  app.router.get('/api/platform/admins', initial);
  app.router.put('/api/platform/admins', (call) => call.body.role === 'admin'
    ? [...initial, { loginName: call.body.loginName, source: 'granted', revocable: true }]
    : initial.filter((row) => row.loginName !== call.body.loginName));
  app.evaluate(`renderPlatformAdminPanel()`);
  await settle(8);
  const host = app.document.querySelector('#platformView');
  assert.match(host.textContent, /示例用户|审阅者|2 位管理员/);
  const search = host.querySelector('#platformAdminSearch');
  search.value = '不存在';
  search.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.match(host.textContent, /未找到/);
  search.value = '';
  search.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  host.querySelector('[data-source-filter="builtin"]')?.click();
  host.querySelector('[data-source-filter="granted"]')?.click();
  host.querySelector('[data-source-filter="all"]')?.click();

  const form = host.querySelector('#platformAdminForm');
  form.elements.loginName.value = 'newadmin';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(6);
  assert.match(host.textContent, /newadmin/);

  host.querySelector('[data-revoke-admin="reviewer"]')?.click();
  await settle();
  app.document.querySelector('#confirmOkBtn').click();
  await settle(6);
  assert.ok(app.router.calls.filter((call) => call.path === '/api/platform/admins' && call.method === 'PUT').length >= 2);
  host.querySelector('[data-platform-nav="agents"]')?.click();
  assert.equal(app.evaluate(`state.currentView`), 'agents');
});
