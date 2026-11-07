import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('bot editor validates, creates, selects a target, sends a test, and renders records', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const bot = {
    id: 'bot-new', projectId: 'project-1', ownerId: 'user-1', name: 'Release Bot',
    botId: 'wx-1', model: 'glm-5.3-flash-ioa', enabled: true, interactionEnabled: false,
    secretConfigured: true, health: 'ready', target: null,
  };
  app.router.post('/api/notification-bots', (call) => ({ ...bot, ...call.body }));
  app.router.get('/api/notification-bots/bot-new/sessions', {
    sessions: [
      { id: 'chat-1', name: '研发群', type: 'group', lastMessageTime: 'today' },
      { id: 'chat-2', name: 'Alice', type: 'single', lastMessageTime: 'yesterday' },
    ],
    total: 2,
  });
  app.router.put('/api/notification-bots/bot-new', (call) => ({
    ...bot, ...call.body, target: call.body.targetId ? { id: call.body.targetId, name: '研发群', type: 'group' } : null,
  }));
  app.router.post('/api/notification-bots/bot-new/test', { targetName: '研发群' });
  app.router.post('/api/notification-deliveries/delivery-1/retry', { ok: true });
  app.router.delete('/api/notification-bots/bot-new', { ok: true });
  app.router.get('/api/notification-bots/bot-new/interactions', {
    connection: { state: 'connected' }, conversations: [{ id: 'c1' }],
    messages: [{ createdAt: '2026-10-20T10:00:00Z', senderId: 'alice', status: 'replied', content: 'hello', reply: 'world' }],
  });
  app.router.get('/api/notification-deliveries', []);
  app.evaluate(`botUI.projectId=state.projectFilter;botUI.items=[];botUI.loading=false;renderBots();botOpenEditor(null)`);
  const host = () => app.document.querySelector('#botsContent');
  const form = host().querySelector('#botForm');
  form.elements.name.value = 'Release Bot';
  form.elements.botId.value = 'wx-1';
  form.elements.secret.value = 'secret-value';
  form.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(8);
  assert.equal(app.router.calls.find(c => c.method === 'POST' && c.path === '/api/notification-bots').body.name, 'Release Bot');
  assert.equal(app.evaluate(`botUI.step`), 2);

  host().querySelector('#botRefreshSessions').click();
  await settle(8);
  assert.match(host().textContent, /研发群/);
  const target = host().querySelector('input[value="chat-1"]');
  target.checked = true;
  target.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  host().querySelector('#botSaveTarget').click();
  await settle(8);
  assert.equal(app.evaluate(`botUI.step`), 3);
  assert.equal(app.router.calls.find(c => c.method === 'PUT' && c.path.endsWith('/bot-new')).body.targetId, 'chat-1');
  host().querySelector('#botSendTest').click();
  await settle(8);
  assert.match(host().textContent, /测试通知已发送到 研发群/);

  app.evaluate(`
    botUI.tab='records';
    botUI.recordsTab='deliveries';
    botUI.deliveries=[{id:'delivery-1',botId:'bot-new',targetName:'研发群',sourceName:'Nightly',
      sourceType:'schedule_job',eventType:'failed',status:'failed',contentSource:'ai',
      lastError:'network down',createdAt:'2026-10-20T10:00:00Z'}];
    renderBots();
  `);
  assert.match(host().textContent, /发送失败/);
  assert.match(host().textContent, /network down/);
  host().querySelector('[data-delivery-retry="delivery-1"]').click();
  await settle(4);
  assert.equal(app.evaluate(`botUI.deliveries[0].status`), 'pending');
  app.evaluate(`botUI.recordsTab='interactions';botUI.interactionBotId='bot-new';renderBots()`);
  await app.evaluate(`loadBotInteractions()`);
  await settle(4);
  assert.match(host().textContent, /hello|world/);
  await app.evaluate(`botDeleteRequest(botUI.items[0],null,botUI.projectId,botUI.revision)`);
  assert.equal(app.evaluate(`botUI.items.length`), 0);
});

test('bot pure helpers enforce permissions, filtering, and status labels', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const values = app.evaluate(`({
    filtered:filteredBotSessions([{name:'研发群'},{name:'销售群'}],'研发').map(x=>x.name),
    type:botTypeLabel('group'),
    failed:botStatusLabel('failed'),
    message:botMessageBadge('reply_failed'),
    target:botTargetHTML({name:'Alice',type:'single'}),
    allowed:botStepAllowed({id:'b',target:{id:'c'}},3)
  })`);
  assert.equal(JSON.stringify(values.filtered), JSON.stringify(['研发群']));
  assert.equal(values.type, '群聊');
  assert.equal(values.failed, '发送失败');
  assert.match(values.message, /回复失败/);
  assert.match(values.target, /Alice/);
  assert.equal(values.allowed, true);
});
