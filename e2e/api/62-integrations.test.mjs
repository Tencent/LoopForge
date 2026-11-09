// Project integrations that are configured but never call out of the
// platform here: notification bots (credentials are write-only, sending needs
// a selected recipient), external insight sources (scoped ingest tokens),
// business domains with project ownership history, and WeCom identity
// bindings. Nothing in this file contacts WeCom.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { api, get, post, put, del, list, track, cleanup, ensureProject, tokenOwner, browserSession } from '../lib/api.mjs';
import { cfg, uid } from '../lib/env.mjs';

const P = cfg.projectId;
let me;
before(async () => {
  await ensureProject();
  me = await tokenOwner();
});
after(cleanup);

test('notification bots: create, update, secret stays write-only, delete', async () => {
  const name = `E2E 机器人 ${cfg.runTag}`;
  const botId = uid('wecom-bot');
  await api('POST', '/api/notification-bots', { projectId: P, name: '', botId, secret: 's' }, { expect: 400 });
  await api('POST', '/api/notification-bots', { projectId: P, name, botId: '', secret: 's' }, { expect: 400 });
  await api('POST', '/api/notification-bots', { projectId: P, name: 'x'.repeat(81), botId, secret: 's' }, { expect: 400 });
  await api('POST', '/api/notification-bots', { projectId: P, name, botId }, { expect: 400 });
  await api('POST', '/api/notification-bots', { projectId: P, name, botId, secret: 's', model: 'not-a-model' }, { expect: 400 });
  await api('POST', '/api/notification-bots', { projectId: 'e2e-missing-project', name, botId, secret: 's' }, { expect: 404 });

  const secret = `e2e-secret-${cfg.runTag}`;
  const res = await api('POST', '/api/notification-bots', { projectId: P, name, botId, secret, enabled: false }, { raw: true });
  const bot = res.data;
  track(`notification bot ${bot.id}`, () => del(`/api/notification-bots/${bot.id}`));
  assert.ok(!res.text.includes(secret), 'the bot secret must never be echoed');
  assert.equal(bot.secretConfigured, true);
  assert.equal(bot.ownerId, me.id);
  assert.equal(bot.health, 'unchecked');
  assert.ok(bot.model, 'a default report model is assigned');

  const bots = await api('GET', `/api/notification-bots?projectId=${P}`, undefined, { raw: true });
  assert.ok(list(bots.data).some(b => b.id === bot.id));
  assert.ok(!bots.text.includes(secret), 'listing must not leak the secret');

  const renamed = await put(`/api/notification-bots/${bot.id}`, { name: `${name} 改`, botId, enabled: false });
  assert.equal(renamed.name, `${name} 改`);
  assert.equal(renamed.secretConfigured, true, 'an update without a secret keeps the stored one');
  await api('PUT', `/api/notification-bots/${bot.id}`, { name, botId: `${botId}-x`, enabled: false }, { expect: 400 });
  await api('PUT', `/api/notification-bots/${bot.id}`, { name, botId, enabled: false, targetId: 'chat-x', secret: 'new' }, { expect: 400 });
  // Sending needs a recipient chosen from the provider list first.
  await api('POST', `/api/notification-bots/${bot.id}/test`, {}, { expect: 400 });
  await api('PUT', '/api/notification-bots/bot-does-not-exist', { name, botId }, { expect: 404 });

  const interactions = await get(`/api/notification-bots/${bot.id}/interactions`, { expect: [200] });
  assert.ok(interactions);

  await del(`/api/notification-bots/${bot.id}`, { expect: [200, 204] });
  assert.ok(!list(await get(`/api/notification-bots?projectId=${P}`)).some(b => b.id === bot.id));
});

test('notification preview renders a binding and deliveries are listed', async () => {
  const preview = await post('/api/notification-preview', {
    projectId: P, sourceType: 'workflow',
    binding: { botId: 'bot-preview', events: ['succeeded'], scope: 'run', template: '{{project}}：{{workflow}} {{status}}' },
  });
  assert.ok(JSON.stringify(preview).length > 2, 'preview returned nothing');
  await api('POST', '/api/notification-preview', { projectId: 'e2e-missing-project', binding: {} }, { expect: 404 });
  assert.ok(Array.isArray(list(await get(`/api/notification-deliveries?projectId=${P}`))));
  await api('GET', '/api/notification-deliveries?projectId=e2e-missing-project', undefined, { expect: 404 });
  await api('POST', `/api/notification-deliveries/delivery-missing/retry?projectId=${P}`, {}, { expect: 404 });
});

const execution = (id, extra = {}) => ({
  id, type: 'workflow_execution', externalExecutionId: `${id}-exec`,
  workflow: { id: 'e2e-ext-wf', name: 'E2E 外部工作流' }, status: 'succeeded',
  startedAt: new Date(Date.now() - 60_000).toISOString(), finishedAt: new Date().toISOString(),
  usage: [{ provider: 'e2e', model: 'e2e-model', inputTokens: 100, outputTokens: 20, cacheReadTokens: 10, cacheWriteTokens: 0, reasoningTokens: 5, costUsd: 0.01, costStatus: 'reported' }],
  ...extra,
});
const ingest = (token, body, expect) => api('POST', '/api/insights/v1/ingest', body, { auth: false, headers: token ? { Authorization: `Bearer ${token}` } : {}, expect });

test('insight sources: scoped ingest token, validation, idempotency and revocation', async () => {
  const base = '/api/insights/v1/sources';
  await api('POST', base, { projectId: P, name: '' }, { expect: 400 });
  await api('POST', base, { projectId: P, name: 'x', expiresInDays: 400 }, { expect: 400 });
  await api('POST', base, { projectId: P, name: 'x', unknownField: 1 }, { expect: 400 });
  await api('POST', base, { projectId: P, name: 'x', userMappings: { ext: 987654321 } }, { expect: 400 });
  await api('POST', base, { projectId: 'e2e-missing-project', name: 'x' }, { expect: 404 });

  const created = await api('POST', base, { projectId: P, name: `E2E 数据源 ${cfg.runTag}`, userMappings: { 'ext-owner': me.id }, expiresInDays: 1 }, { expect: 201, raw: true });
  const { source, token } = created.data;
  track(`insight source ${source.id}`, () => del(`${base}/${source.id}`));
  assert.match(token, /^afi_/);
  assert.equal(created.headers.get('cache-control'), 'no-store');
  const sources = await api('GET', `${base}?projectId=${P}`, undefined, { raw: true });
  assert.ok(list(sources.data).some(s => s.id === source.id && s.tokenPrefix && token.startsWith(s.tokenPrefix)));
  assert.ok(!sources.text.includes(token), 'listing must not leak the ingest token');

  await ingest('', execution('x'), 401);
  await ingest('afi_forged', execution('x'), 401);
  // The ingest token is not a platform API token.
  await api('GET', '/api/projects', undefined, { token, expect: 401 });

  const id = uid('insight');
  await ingest(token, execution(id, { type: 'other' }), 400);
  await ingest(token, execution(id, { status: 'running' }), 400);
  await ingest(token, execution(id, { finishedAt: new Date(Date.now() + 3_600_000).toISOString() }), 400);
  await ingest(token, execution(id, { url: 'https://user:pw@example.com/x' }), 400);
  await ingest(token, execution(id, { usage: [] }), 400);
  await ingest(token, execution(id, { externalUserId: 'ext-unmapped' }), 400);
  await ingest(token, execution(id, { usage: [{ provider: 'e2e', model: 'm', inputTokens: 1, outputTokens: 1, cacheReadTokens: 5, cacheWriteTokens: 0, reasoningTokens: 0, costUsd: null, costStatus: 'unknown' }] }), 400);
  await ingest(token, { ...execution(id), extra: true }, 400);

  const body = execution(id, { externalUserId: 'ext-owner', url: 'https://example.com/run/1' });
  assert.equal((await ingest(token, body, 201)).status, 'accepted');
  assert.equal((await ingest(token, body, 200)).status, 'duplicate', 'a retried report is idempotent');
  await ingest(token, { ...body, status: 'failed' }, 409);

  await del(`${base}/${source.id}`, { expect: 200 });
  await ingest(token, execution(uid('insight')), 401);
  await api('DELETE', `${base}/src-does-not-exist`, undefined, { expect: 404 });
});

test('business domains: validation, optimistic upsert and report filters', async () => {
  const id = 'e2e-auto-domain';
  const name = 'E2E 自动化业务域';
  const domains = await get('/api/business-domains');
  assert.ok(Array.isArray(domains.items) && 'canAdmin' in domains);
  for (const bad of [{ id: 'all', name }, { id: 'unclassified', name }, { id: 'bad id!', name }, { id, name: '' }, { id, name: 'x'.repeat(101) }]) {
    await api('PUT', '/api/business-domains', bad, { expect: [400, 403] });
  }
  await api('GET', '/api/business-domain-report?businessDomainId=bd-does-not-exist', undefined, { expect: 404 });
  await api('GET', '/api/business-domain-report?grain=year', undefined, { expect: 400 });
  await api('GET', '/api/business-domain-report?timeZone=Mars/Base', undefined, { expect: 400 });
  await api('GET', '/api/business-domain-report?from=yesterday', undefined, { expect: 400 });
  await api('GET', '/api/business-domain-report?from=2026-02-01T00:00:00Z&to=2026-01-01T00:00:00Z', undefined, { expect: 400 });
  assert.ok(await get('/api/business-domain-report?grain=week&timeZone=Asia/Shanghai'));
  if (!domains.canAdmin) return;

  // Domains have no delete endpoint: one fixed domain is reused across runs.
  const saved = await put('/api/business-domains', { id, name, description: 'e2e 自动化复用（勿删）', active: true, managerIds: [], viewerIds: [] });
  assert.equal(saved.id, id);
  assert.equal(saved.active, true);
  await api('PUT', '/api/business-domains?createOnly=true', { id, name, active: true }, { expect: 409 });
  await api('PUT', '/api/business-domains?createOnly=true', { id: uid('bd'), name, active: true }, { expect: 409 });
  await api('PUT', '/api/business-domains', { id, name, active: true, managerIds: [me.id, me.id] }, { expect: 400 });
  await api('PUT', '/api/business-domains', { id, name, active: true, viewerIds: [987654321] }, { expect: 400 });
  const items = (await get('/api/business-domains')).items;
  assert.ok(items.some(i => i.domain.id === id && i.canManage && i.canView));
  const report = await get(`/api/business-domain-report?businessDomainId=${id}&grain=day`);
  assert.ok(report);
});

test('project business-domain assignment: compare-and-set and history (platform admin)', async t => {
  if (!(await get('/api/business-domains')).canAdmin) return t.skip('token owner is not a platform admin');
  const id = 'e2e-auto-domain';
  const original = (await get(`/api/projects/${P}`)).businessDomainId || '';
  const restore = () => put(`/api/projects/${P}/business-domain`, { businessDomainId: original, expectedDomainId: id, reason: 'e2e 恢复' }, { expect: [200, 409] });
  if (original !== id) {
    await api('PUT', `/api/projects/${P}/business-domain`, { businessDomainId: id, expectedDomainId: `${original}-stale`, reason: 'e2e' }, { expect: 409 });
    await api('PUT', `/api/projects/${P}/business-domain`, { businessDomainId: id, expectedDomainId: original, reason: 'x'.repeat(501) }, { expect: 400 });
    track(`project ${P} business domain`, restore);
    const assigned = await put(`/api/projects/${P}/business-domain`, { businessDomainId: id, expectedDomainId: original, reason: `e2e ${cfg.runTag}` });
    assert.equal(assigned.businessDomainId ?? assigned.project?.businessDomainId, id);
  }
  assert.equal((await get(`/api/projects/${P}`)).businessDomainId, id);
  const history = list(await get(`/api/projects/${P}/business-domain/history`));
  assert.ok(history.some(h => h.toDomainId === id), 'assignment must be recorded in the history');
  await api('PUT', '/api/projects/e2e-missing-project/business-domain', { businessDomainId: id }, { expect: 404 });
});

test('WeCom identity bindings: browser session only, malformed claim and cross-site revoke are refused', async t => {
  // Binding confirms a person, so a Bearer token is never accepted.
  await api('GET', '/api/bot-bindings', undefined, { expect: 401 });
  await api('POST', '/api/bot-bindings/claim', { token: 'a'.repeat(64) }, { expect: 401 });
  const cookie = await browserSession();
  if (!cookie) return t.skip('acceptance login disabled (run through local.sh)');
  const s = opts => ({ cookie, ...opts });
  assert.ok(Array.isArray(list(await get('/api/bot-bindings', s()))));
  await api('POST', '/api/bot-bindings/claim', { token: 'short' }, s({ expect: 400 }));
  await api('POST', '/api/bot-bindings/status', { token: 'a'.repeat(64) }, s({ expect: 400 }));
  // Revoking needs the platform page as origin, so a forged cross-site request fails.
  await api('DELETE', '/api/bot-bindings/binding-x', undefined, s({ headers: { Origin: 'https://evil.example' }, expect: 403 }));
  await api('DELETE', '/api/bot-bindings/binding-x', undefined, s({ headers: { Origin: cfg.base }, expect: 404 }));
});
