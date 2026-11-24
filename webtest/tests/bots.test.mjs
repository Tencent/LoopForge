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

test('feishu bot is created read-only and confirms its enterprise', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const created = {
    id: 'bot-fs', projectId: 'project-1', ownerId: 'user-1', name: 'Feishu Bot', channel: 'feishu',
    botId: 'cli_app', model: 'glm-5.3-flash-ioa', enabled: true, interactionEnabled: true, writeApproval: 'readonly',
    secretConfigured: true, health: 'ready', configVersion: 1, tenantState: 'awaiting_confirmation',
  };
  app.router.post('/api/notification-bots', (call) => ({ ...created, ...call.body, secret: undefined }));
  app.router.get('/api/notification-bots/bot-fs/tenant', {
    tenantState: 'awaiting_confirmation', configVersion: 1,
    candidate: { tenantKey: 'tenant-1', receivedAt: '2026-11-18T03:00:00Z', credentialVersion: 1 },
    queried: { tenantKey: 'tenant-1', tenantName: '测试企业' },
  });
  app.router.post('/api/notification-bots/bot-fs/tenant/confirm', (call) => ({ ...created, configVersion: 2, tenantState: 'locked', tenantKey: call.body.tenantKey, tenantName: '测试企业' }));
  app.router.get('/api/notification-deliveries', []);
  app.evaluate(`botUI.projectId=state.projectFilter;botUI.items=[];botUI.loading=false;renderBots();botOpenEditor(null)`);
  const host = () => app.document.querySelector('#botsContent');
  let form = host().querySelector('#botForm');
  form.elements.channel.value = 'feishu';
  form.elements.channel.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  await settle(4);
  form = host().querySelector('#botForm');
  assert.match(host().textContent, /App ID/);
  assert.match(host().textContent, /只读查询/);
  // Feishu/DingTalk: read-only or web confirmation, never automatic writes.
  assert.deepEqual([...form.elements.writeApproval.options].map(o => o.value), ['readonly', 'confirm']);
  assert.equal(form.elements.writeApproval.value, 'readonly');
  form.elements.writeApproval.value = 'confirm';
  assert.equal(host().querySelector('option[value="dingtalk"]').disabled, false);
  form.elements.name.value = 'Feishu Bot';
  form.elements.botId.value = 'cli_app';
  form.elements.secret.value = 'app-secret';
  form.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(8);
  const post = app.router.calls.find(c => c.method === 'POST' && c.path === '/api/notification-bots').body;
  assert.equal(post.channel, 'feishu');
  assert.equal(post.writeApproval, 'confirm');
  assert.equal(app.evaluate(`botUI.step`), 2);
  await settle(6);
  assert.match(host().textContent, /tenant-1/);
  assert.match(host().textContent, /与平台查询一致/);
  host().querySelector('#botTenantConfirm').click();
  await settle(8);
  const confirm = app.router.calls.find(c => c.path === '/api/notification-bots/bot-fs/tenant/confirm').body;
  assert.equal(confirm.tenantKey, 'tenant-1');
  assert.equal(confirm.configVersion, 1);
  assert.match(host().textContent, /企业已确认/);
});

test('records offer confirm and resend for unknown deliveries', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const bot = { id: 'bot-a', projectId: 'project-1', ownerId: 'user-1', name: 'A', channel: 'wecom', botId: 'wx', enabled: true, interactionEnabled: true, configVersion: 3 };
  app.router.get('/api/notification-bots/bot-a/interactions', {
    connection: { state: 'connected' }, conversations: [],
    messages: [
      { id: 'i-unknown', createdAt: '2026-11-18T03:00:00Z', senderId: 'u', status: 'reply_unknown', terminalReason: 'delivery_unknown', content: 'q1', reply: 'r1' },
      { id: 'i-expired', createdAt: '2026-11-18T03:00:00Z', senderId: 'u', status: 'reply_failed', terminalReason: 'route_expired', content: 'q2', reply: 'r2' },
    ],
  });
  app.router.post('/api/bot-interactions/i-unknown/confirm', { id: 'i-unknown', status: 'replied' });
  app.evaluate(`botUI.projectId=state.projectFilter;botUI.items=[${JSON.stringify(bot)}];botUI.loading=false;botUI.tab='records';botUI.recordsTab='interactions';botUI.interactionBotId='bot-a';renderBots()`);
  await app.evaluate(`loadBotInteractions()`);
  await settle(4);
  const host = app.document.querySelector('#botsContent');
  assert.match(host.textContent, /送达待核对/);
  assert.match(host.textContent, /会话回复授权已过期/);
  assert.ok(host.querySelector('[data-interaction-confirm="i-unknown"]'));
  assert.ok(host.querySelector('[data-interaction-resend="i-unknown"]'));
  assert.equal(host.querySelector('[data-interaction-resend="i-expired"]'), null);
  host.querySelector('[data-interaction-confirm="i-unknown"]').click();
  await settle(6);
  const call = app.router.calls.find(c => c.path === '/api/bot-interactions/i-unknown/confirm');
  assert.ok(call && call.body.actionKey.length >= 16);
});

test('dingtalk bot requires a CorpId and shows automatic enterprise confirmation', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const created = {
    id: 'bot-dt', projectId: 'project-1', ownerId: 'user-1', name: 'DingTalk Bot', channel: 'dingtalk',
    botId: 'ding-app', model: 'glm-5.3-flash-ioa', enabled: true, interactionEnabled: true, writeApproval: 'readonly',
    secretConfigured: true, health: 'ready', configVersion: 1, tenantState: 'awaiting_confirmation', tenantExpected: 'ding-corp',
  };
  app.router.post('/api/notification-bots', (call) => ({ ...created, tenantExpected: call.body.tenantKey }));
  app.router.get('/api/notification-bots/bot-dt/tenant', { tenantState: 'awaiting_confirmation', configVersion: 1, queried: { tenantKey: '', tenantName: '' } });
  app.router.get('/api/notification-deliveries', []);
  app.evaluate(`botUI.projectId=state.projectFilter;botUI.items=[];botUI.loading=false;renderBots();botOpenEditor(null)`);
  const host = () => app.document.querySelector('#botsContent');
  let form = host().querySelector('#botForm');
  form.elements.channel.value = 'dingtalk';
  form.elements.channel.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  await settle(4);
  form = host().querySelector('#botForm');
  assert.match(host().textContent, /Client ID/);
  assert.ok(form.elements.tenantKey, 'CorpId field');
  form.elements.name.value = 'DingTalk Bot';
  form.elements.botId.value = 'ding-app';
  form.elements.secret.value = 'secret';
  form.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(4);
  assert.equal(app.router.calls.filter(c => c.method === 'POST' && c.path === '/api/notification-bots').length, 0, 'saved without CorpId');
  form = host().querySelector('#botForm');
  form.elements.tenantKey.value = 'ding-corp';
  assert.ok(form.elements.cardTemplateId, 'card template field');
  form.elements.cardTemplateId.value = ' tpl-1.schema ';
  form.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(8);
  const post = app.router.calls.find(c => c.method === 'POST' && c.path === '/api/notification-bots').body;
  assert.equal(post.channel, 'dingtalk');
  assert.equal(post.tenantKey, 'ding-corp');
  assert.equal(post.cardTemplateId, 'tpl-1.schema');
  assert.equal(post.writeApproval, 'readonly');
  assert.equal(app.evaluate(`botUI.step`), 2);
  await settle(6);
  assert.match(host().textContent, /ding-corp/);
  assert.match(host().textContent, /自动确认/);
  assert.equal(host().querySelector('#botTenantConfirm'), null, 'no manual confirm for DingTalk');
});

test('dingtalk notification recipient is saved after a trial and pending deliveries need a choice', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const bot = {
    id: 'bot-dt', projectId: 'project-1', ownerId: 'user-1', name: 'DingTalk Bot', channel: 'dingtalk',
    botId: 'ding-app', model: 'glm-5.3-flash-ioa', enabled: true, interactionEnabled: false, writeApproval: 'readonly',
    secretConfigured: true, health: 'ready', configVersion: 4, tenantState: 'locked', tenantKey: 'ding-corp', tenantExpected: 'ding-corp',
    target: { id: 'staff-old', name: '旧成员 · st***ld', type: 'single', verifiedAt: '2026-11-19T02:00:00Z' },
  };
  const member = { id: 'staff-1', name: '成员（member） · st***-1', type: 'single' };
  app.router.get('/api/notification-bots/bot-dt/tenant', { tenantState: 'locked', configVersion: 4, queried: {} });
  app.router.get('/api/notification-bots/bot-dt/sessions', { sessions: [member], total: 1 });
  app.router.get('/api/notification-deliveries', [
    { id: 'd-unknown', botId: 'bot-dt', status: 'send_unknown', eventType: 'failed', targetName: '旧成员', createdAt: '2026-11-19T02:00:00Z', lastError: '钉钉返回服务端错误，送达结果未知' },
  ]);
  let puts = 0;
  app.router.put('/api/notification-bots/bot-dt', (call) => {
    puts++;
    if (!call.body.pendingTargetAction) return { status: 409, body: { error: '原接收人还有 2 条待发通知，请选择取消或改发到新接收人', code: 'pending_deliveries', pending: 2 } };
    return { ...bot, configVersion: 5, target: { ...member, verifiedAt: '2026-11-19T03:00:00Z' } };
  });
  app.router.post('/api/notification-deliveries/d-unknown/confirm', {});
  app.evaluate(`afConfirm=()=>Promise.resolve(true);botUI.projectId=state.projectFilter;botUI.items=[${JSON.stringify(bot)}];botUI.loading=false;renderBots();botOpenEditor(botUI.items[0])`);
  await settle(4);
  app.evaluate(`botGoStep(3)`);
  await settle(4);
  const host = () => app.document.querySelector('#botsContent');
  assert.match(host().textContent, /通知接收人/);
  assert.match(host().textContent, /试发成功/);
  host().querySelector('#botRefreshSessions').click();
  await settle(6);
  const radio = host().querySelector('input[name="targetId"][value="staff-1"]');
  assert.ok(radio, 'bound member listed');
  radio.checked = true;
  radio.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  await settle(2);
  host().querySelector('#botSaveTarget').click();
  await settle(10);
  const calls = app.router.calls.filter(c => c.method === 'PUT' && c.path === '/api/notification-bots/bot-dt');
  assert.equal(calls.length, 2);
  assert.equal(calls[0].body.targetId, 'staff-1');
  assert.equal(calls[0].body.pendingTargetAction, '');
  assert.equal(calls[1].body.pendingTargetAction, 'migrate');
  assert.equal(app.evaluate(`botUI.selected.target.id`), 'staff-1');
  assert.match(host().textContent, /试发消息已发送到/);
  // Records: an unknown delivery can be confirmed as received.
  app.evaluate(`botUI.tab='records';botUI.recordsTab='deliveries';botUI.deliveries=[{id:'d-unknown',botId:'bot-dt',status:'send_unknown',eventType:'failed',targetName:'旧成员',createdAt:'2026-11-19T02:00:00Z'}];renderBots()`);
  await settle(4);
  assert.ok(host().querySelector('[data-delivery-retry="d-unknown"]'), 'resend offered');
  host().querySelector('[data-delivery-confirm="d-unknown"]').click();
  await settle(6);
  assert.ok(app.router.calls.find(c => c.method === 'POST' && c.path === '/api/notification-deliveries/d-unknown/confirm'));
  assert.equal(puts, 2);
});

test('expired dingtalk replies offer a proactive resend only for verified senders', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const bot = { id: 'bot-dt', projectId: 'project-1', ownerId: 'user-1', name: 'DT', channel: 'dingtalk', botId: 'ding', enabled: true, interactionEnabled: true, configVersion: 3, tenantState: 'locked' };
  app.router.get('/api/notification-bots/bot-dt/interactions', {
    connection: { state: 'connected' }, conversations: [],
    messages: [
      { id: 'i-ready', createdAt: '2026-11-19T03:00:00Z', senderId: 'staff-1', status: 'reply_failed', terminalReason: 'route_expired', content: 'q1', reply: 'r1', pushResendAvailable: true },
      { id: 'i-unverified', createdAt: '2026-11-19T03:00:00Z', senderId: 'staff-2', status: 'reply_failed', terminalReason: 'route_expired', content: 'q2', reply: 'r2' },
    ],
  });
  app.router.post('/api/bot-interactions/i-ready/push-resend', { id: 'i-ready', status: 'ready', sendRoute: 'push' });
  app.router.post('/api/notification-bots/bot-dt/targets/verify', { id: 'staff-2', verifiedAt: '2026-11-19T04:00:00Z' });
  app.evaluate(`afConfirm=()=>Promise.resolve(true);botUI.projectId=state.projectFilter;botUI.items=[${JSON.stringify(bot)}];botUI.loading=false;botUI.tab='records';botUI.recordsTab='interactions';botUI.interactionBotId='bot-dt';renderBots()`);
  await app.evaluate(`loadBotInteractions()`);
  await settle(4);
  const host = app.document.querySelector('#botsContent');
  assert.ok(host.querySelector('[data-interaction-push-resend="i-ready"]'));
  assert.equal(host.querySelector('[data-interaction-push-resend="i-unverified"]'), null, 'no push resend without a verified target');
  assert.equal(host.querySelector('[data-interaction-resend="i-ready"]'), null, 'webhook resend hidden after expiry');
  host.querySelector('[data-interaction-push-verify="staff-2"]').click();
  await settle(6);
  assert.equal(app.router.calls.find(c => c.path === '/api/notification-bots/bot-dt/targets/verify').body.targetId, 'staff-2');
  app.document.querySelector('#botsContent [data-interaction-push-resend="i-ready"]').click();
  await settle(6);
  const call = app.router.calls.find(c => c.path === '/api/bot-interactions/i-ready/push-resend');
  assert.ok(call && call.body.actionKey.length >= 16);
});

test('records hide manual actions for text removed by the retention cleanup', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const bot = { id: 'bot-r', projectId: 'project-1', ownerId: 'user-1', name: 'R', channel: 'wecom', botId: 'w', enabled: true, interactionEnabled: true, configVersion: 1 };
  app.router.get('/api/notification-bots/bot-r/interactions', {
    connection: { state: 'connected' }, conversations: [],
    messages: [
      { id: 'i-purged', createdAt: '2026-01-01T03:00:00Z', senderId: 's', status: 'reply_failed', bodyPurgedAt: '2026-04-01T00:00:00Z' },
      { id: 'i-live', createdAt: '2026-11-19T03:00:00Z', senderId: 's', status: 'reply_failed', content: 'q', reply: 'r' },
    ],
  });
  app.evaluate(`botUI.projectId=state.projectFilter;botUI.items=[${JSON.stringify(bot)}];botUI.loading=false;botUI.tab='records';botUI.recordsTab='interactions';botUI.interactionBotId='bot-r';renderBots()`);
  await app.evaluate(`loadBotInteractions()`);
  await settle(4);
  const host = app.document.querySelector('#botsContent');
  assert.match(host.textContent, /正文已按保留期清理/);
  assert.equal(host.querySelector('[data-interaction-resend="i-purged"]'), null);
  assert.ok(host.querySelector('[data-interaction-resend="i-live"]'));
  app.evaluate(`botUI.recordsTab='deliveries';botUI.deliveries=[{id:'d-purged',botId:'bot-r',status:'failed',eventType:'failed',targetName:'群',contentSource:'purged',createdAt:'2026-01-01T00:00:00Z'}];renderBots()`);
  await settle(4);
  assert.ok(app.document.querySelector('#botsContent [data-delivery-row="d-purged"]'), 'delivery row rendered');
  assert.match(app.document.querySelector('#botsContent').textContent, /正文已按保留期清理/);
  assert.equal(app.document.querySelector('#botsContent [data-delivery-retry="d-purged"]'), null);
});

test('new bot form picks an executor and model and submits the concrete model', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/assistant/models', {
    default: 'glm-test',
    models: [{ id: 'glm-test', name: 'GLM Test', family: 'glm' }, { id: 'codex:gpt-x', name: 'GPT X', family: 'gpt', engine: 'codex' }],
    engines: [{ id: 'codebuddy', name: 'CodeBuddy', configured: true }, { id: 'codex', name: 'Codex', configured: true }],
  });
  app.router.post('/api/notification-bots', (call) => ({ id: 'bot-fs2', projectId: 'project-1', ownerId: 'user-1', name: call.body.name, channel: 'feishu', botId: call.body.botId, model: call.body.model, enabled: true, interactionEnabled: true, writeApproval: 'readonly', secretConfigured: true, configVersion: 1, tenantState: 'awaiting_confirmation' }));
  app.router.get('/api/notification-bots/bot-fs2/tenant', { tenantState: 'awaiting_confirmation', configVersion: 1, queried: {} });
  app.router.get('/api/notification-deliveries', []);
  await app.evaluate(`window.AgentFlowAssistantModels.load()`);
  app.evaluate(`botUI.projectId=state.projectFilter;botUI.items=[];botUI.loading=false;renderBots();botOpenEditor(null)`);
  await settle(4);
  const host = () => app.document.querySelector('#botsContent');
  let form = host().querySelector('#botForm');
  form.elements.channel.value = 'feishu';
  form.elements.channel.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  await settle(4);
  assert.match(host().textContent, /执行器与模型/);
  const executor = () => host().querySelector('#botModelPicker [data-feature-executor]');
  const model = () => host().querySelector('#botModelPicker [data-feature-model]');
  assert.deepEqual([...executor().options].map(o => o.value), ['codebuddy', 'codex']);
  assert.equal(model().value, 'codebuddy:glm-test', 'the provider default is preselected');
  executor().value = 'codex';
  executor().dispatchEvent(new app.window.Event('change', { bubbles: true }));
  await settle(2);
  assert.deepEqual([...model().options].map(o => o.value), ['codex:gpt-x']);
  assert.equal(model().value, 'codex:gpt-x');
  form = host().querySelector('#botForm');
  form.elements.name.value = '飞书 Bot';
  form.elements.botId.value = 'cli_x';
  form.elements.secret.value = 's';
  form.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(8);
  const post = app.router.calls.find(c => c.method === 'POST' && c.path === '/api/notification-bots');
  assert.equal(post.body.model, 'codex:gpt-x');
});
