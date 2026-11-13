// Journeys through the AI workbench conversation list, the personal credential
// wizard, notification bots and what a project
// viewer or member is offered on the bot page. Fixtures are created through the
// API and removed afterwards; nothing here calls a model or WeCom.
import { test, expect, watchErrors, openView, openPageAs } from './fixtures.mjs';
import { api, get, post, put, del, list, track, cleanup, tokenOwner, tokenBSkip, asB, withProjectMember } from '../lib/api.mjs';
import { cfg, uid } from '../lib/env.mjs';

const P = cfg.projectId;
test.afterAll(cleanup);

const stamp = () => `${cfg.runTag}-${Date.now() % 100000}`;
const userById = async id => list(await get('/api/users')).find(u => u.id === id);

async function createBot(name, extra = {}) {
  const bot = await post('/api/notification-bots', { projectId: P, name, botId: uid('ui-bot'), secret: `e2e-${cfg.runTag}`, enabled: false, ...extra });
  track(`notification bot ${bot.id}`, () => del(`/api/notification-bots/${bot.id}`));
  return bot;
}

test('AI workbench: a saved conversation is listed, opened, searched, renamed, archived and deleted', async ({ page }) => {
  const id = `af-e2e-ui-${stamp()}`;
  const title = `E2E UI 会话 ${stamp()}`;
  const renamed = `${title} 改`;
  await put(`/api/assistant/sessions/${id}`, {
    title, projectId: P,
    messages: [{ role: 'user', content: 'UI 第一问' }, { role: 'assistant', content: 'UI 第一答' }],
  });
  track(`assistant session ${id}`, () => del(`/api/assistant/sessions/${id}`, { expect: [200, 404] }));

  const errors = watchErrors(page);
  await openView(page, 'collaboration');
  const app = page.frameLocator('#chatNextFrame');
  const row = text => app.locator('.session-row').filter({ hasText: text });
  const dialog = app.getByRole('dialog');

  await expect(row(title)).toBeVisible({ timeout: 30_000 });
  await row(title).locator('.session-link').click();
  await expect(app.locator('.message.user').filter({ hasText: 'UI 第一问' })).toBeVisible();
  await expect(app.locator('.message.assistant').filter({ hasText: 'UI 第一答' })).toBeVisible();

  await app.getByRole('button', { name: '搜索会话', exact: true }).click();
  const search = dialog.getByRole('textbox', { name: '搜索会话名称' });
  await search.fill(title);
  await expect(dialog.locator('.search-results button').filter({ hasText: title })).toBeVisible();
  await search.fill(`不存在的会话 ${stamp()}`);
  await expect(dialog.locator('.search-results')).toContainText('没有匹配的会话');
  await dialog.getByRole('button', { name: '关闭' }).click();
  await expect(dialog).toHaveCount(0);

  await row(title).locator('.session-link').click({ button: 'right' });
  await app.getByRole('menuitem', { name: '重命名' }).click();
  await dialog.locator('input').fill(renamed);
  await dialog.getByRole('button', { name: '保存', exact: true }).click();
  await expect.poll(async () => (await get(`/api/assistant/sessions/${id}`)).title).toBe(renamed);
  expect((await get(`/api/assistant/sessions/${id}`)).titleEdited).toBe(true);
  await expect(row(renamed)).toBeVisible();

  await row(renamed).locator('.session-link').click({ button: 'right' });
  await app.getByRole('menuitem', { name: '归档' }).click();
  await expect.poll(async () => (await get(`/api/assistant/sessions/${id}`)).archived).toBe(true);
  await expect(row(renamed)).toHaveCount(0);
  await app.getByRole('button', { name: '筛选会话' }).click();
  await app.getByRole('menuitemradio', { name: '已归档' }).click();
  await expect(row(renamed)).toBeVisible();
  // Known defect: when archiving finishes within the menu's 160 ms close
  // animation, the row unmounts first and its context-menu state survives, so
  // the menu pops up again over the row in 已归档. Not asserted (it depends on
  // server latency); dismiss it like a user would.
  const staleMenu = app.getByRole('menu', { name: '会话操作' });
  if (await staleMenu.count()) {
    await page.keyboard.press('Escape');
    await expect(staleMenu).toHaveCount(0);
  }

  await row(renamed).locator('.session-link').click({ button: 'right' });
  await app.getByRole('menuitem', { name: '删除' }).click();
  await dialog.getByRole('button', { name: '删除', exact: true }).click();
  await expect.poll(async () => (await api('GET', `/api/assistant/sessions/${id}`, undefined, { expect: [200, 404], raw: true })).status).toBe(404);
  await expect(row(renamed)).toHaveCount(0);
  expect(errors).toEqual([]);
});

test('personal credentials: the add wizard validates, saves write-only, and the row clears it', async ({ page, identity }) => {
  const me = identity.user;
  test.skip(!!(await userById(me.id))?.codexApiKeyConfigured, 'the token owner already has a Codex API Key; not overwriting a real credential');
  track('Codex API Key', () => put(`/api/users/${me.id}/credentials`, { codexApiKeyClear: true }));

  const errors = watchErrors(page);
  await openView(page, 'settings');
  await page.locator('[data-credential-add]').click();
  const wizard = page.locator('#credentialWizard');
  const pane = wizard.locator('.credential-pane:not(.is-leaving)');
  const next = wizard.locator('[data-wizard-next]');
  await expect(wizard.getByRole('dialog', { name: '添加凭证' })).toBeVisible();
  await expect(wizard.locator('[data-wizard-step]')).toHaveText('1 / 3');
  await wizard.locator('[data-wizard-kind="codexApiKey"]').click();
  await expect(wizard.locator('[data-wizard-kind="codexApiKey"]')).toHaveAttribute('aria-checked', 'true');
  await next.click();

  await expect(wizard.locator('[data-wizard-step]')).toHaveText('2 / 3');
  await expect(next).toBeDisabled();
  const key = `sk-e2e-${cfg.runTag}`;
  await pane.locator('input[name="secret"]').fill(key);
  const baseURL = pane.locator('input[name="extra"]');
  await baseURL.fill('not-a-url');
  await expect(pane.locator('[data-credential-hint="extra"]')).toContainText('http://');
  await expect(next).toBeDisabled();
  await baseURL.fill('https://gateway.example.com/v1');
  await expect(next).toBeEnabled();
  await next.click();

  await expect(wizard.locator('[data-wizard-step]')).toHaveText('3 / 3');
  await expect(pane).not.toContainText(key);
  await next.click();
  await expect(wizard.locator('[data-wizard-done]')).toBeVisible({ timeout: 20_000 });
  const saved = await userById(me.id);
  expect(saved.codexApiKeyConfigured).toBe(true);
  expect(JSON.stringify(saved)).not.toContain(key);
  await wizard.locator('[data-wizard-done]').click();
  await expect(wizard.getByRole('dialog')).toHaveCount(0);

  const row = page.locator('#credentialPanel [data-credential-row="codexApiKey"]');
  await expect(row).toHaveClass(/is-configured/);
  await row.locator('.credential-row-main').click();
  await expect(row).toHaveClass(/is-open/);
  await row.locator('[data-credential-act="delete"]').click();
  await row.locator('[data-credential-act="confirm-delete"]').click();
  await expect(row).not.toHaveClass(/is-configured/, { timeout: 20_000 });
  expect((await userById(me.id)).codexApiKeyConfigured).toBeFalsy();
  expect(errors).toEqual([]);
});

test('personal credentials: the 代码仓库 row opens read-only and swaps in the token form on demand', async ({ page, identity }) => {
  const me = identity.user;
  test.skip(!(await userById(me.id))?.gitTokenConfigured, 'the token owner has no 工蜂 Git Token configured');

  const errors = watchErrors(page);
  await openView(page, 'settings');
  const row = page.locator('#credentialPanel [data-credential-row="gitToken"]');
  const secret = row.locator('input[name="secret"]');
  const replace = row.locator('[data-credential-act="replace"]');
  await row.locator('.credential-row-main').click();
  await expect(row).toHaveClass(/is-open/);
  await expect(row.locator('[data-git-detail]')).toContainText('权限');
  await expect(row.locator('[data-git-detail]')).toContainText('最近校验');
  await expect(replace).toBeVisible();
  await expect(secret).toBeHidden();

  await replace.click();
  await expect(secret).toBeVisible();
  await expect(secret).toBeFocused();
  await secret.fill(`typed-then-cancelled-${cfg.runTag}`);
  await expect(row.locator('[data-credential-act="save"]')).toBeEnabled();
  await row.locator('[data-credential-act="cancel"]').click();
  await expect(row).toHaveClass(/is-open/);
  await expect(replace).toBeVisible();
  await expect(secret).toBeHidden();
  await expect(secret).toHaveValue('');

  // Delete only asks; backing out returns to the action bar with the token untouched.
  await row.locator('[data-credential-act="delete"]').click();
  await expect(row.locator('[data-credential-act="undelete"]')).toBeFocused();
  await row.locator('[data-credential-act="undelete"]').click();
  await expect(replace).toBeVisible();
  await row.locator('.credential-row-main').click();
  await expect(row).not.toHaveClass(/is-open/);
  expect((await userById(me.id)).gitTokenConfigured).toBe(true);
  expect(errors).toEqual([]);
});

test('notification bots: create through the editor, list, delete', async ({ page }) => {
  const name = `E2E UI 机器人 ${stamp()}`;
  const botId = uid('ui-bot');
  const errors = watchErrors(page);
  await openView(page, 'bots');
  await page.locator('#botNew:visible, #botsContent [data-bot-new]').first().click();
  const form = page.locator('#botForm');
  await form.locator('input[name="name"]').fill(name);
  await form.locator('input[name="botId"]').fill(botId);
  await form.locator('input[name="secret"]').fill(`e2e-secret-${cfg.runTag}`);
  // Disabled: an enabled bot keeps a live WeCom connection.
  await form.locator('input[name="enabled"]').setChecked(false);
  await form.locator('input[name="interactionEnabled"]').setChecked(false);
  await form.locator('button[type="submit"]').click();

  let bot;
  await expect.poll(async () => {
    bot = list(await get(`/api/notification-bots?projectId=${P}`)).find(b => b.botId === botId);
    return bot?.id || '';
  }, { timeout: 20_000 }).not.toBe('');
  track(`notification bot ${bot.id}`, () => del(`/api/notification-bots/${bot.id}`));
  expect(bot).toMatchObject({ name, enabled: false, secretConfigured: true });
  // Saved credentials move the editor to step ② (pick a recipient).
  await expect(page.locator('#botRefreshSessions')).toBeVisible();

  await page.locator('#botsContent [data-bot-back]').first().click();
  const row = page.locator(`[data-bot-row="${bot.id}"]`);
  await expect(row).toContainText(name);
  await expect(row).toContainText('已停用');
  await row.locator(`[data-bot-delete="${bot.id}"]`).click();
  await page.locator('#confirmModal.open #confirmOkBtn').click();
  await expect(row).toHaveCount(0, { timeout: 20_000 });
  expect(list(await get(`/api/notification-bots?projectId=${P}`)).some(b => b.id === bot.id)).toBe(false);
  expect(errors).toEqual([]);
});

test('bot page: a viewer is read-only, a member can add but not manage others\' bots', async ({ browser }) => {
  test.skip(!!tokenBSkip, tokenBSkip);
  const userB = await tokenOwner(cfg.tokenB);
  const bot = await createBot(`E2E UI 只读机器人 ${stamp()}`);

  const asRole = async role => {
    const restore = await withProjectMember(userB.id, role);
    const { context, page } = await openPageAs(browser, { token: cfg.tokenB, user: userB, projectId: P });
    try {
      const errors = watchErrors(page);
      await openView(page, 'bots');
      const row = page.locator(`[data-bot-row="${bot.id}"]`);
      await expect(row).toBeVisible();
      await expect(row.locator('[data-bot-delete]')).toHaveCount(0);
      await expect(page.locator('#botNew')).toBeVisible();
      if (role === 'viewer') await expect(page.locator('#botNew')).toBeDisabled();
      else await expect(page.locator('#botNew')).toBeEnabled();
      await row.locator(`[data-bot-edit="${bot.id}"]`).click();
      await expect(page.locator('#botForm input[name="name"]')).toBeDisabled();
      await expect(page.locator('#botForm button[type="submit"]')).toBeDisabled();
      await expect(page.locator('#botsContent')).toContainText('仅创建者或项目管理员可修改凭据和接收目标');
      await expect(page.locator('#botDelete')).toHaveCount(0);
      expect(errors).toEqual([]);
    } finally {
      await context.close();
      await restore();
    }
  };
  await asRole('viewer');
  await asRole('member');

  // The server agrees with what the page offers.
  const restore = await withProjectMember(userB.id, 'member');
  await api('DELETE', `/api/notification-bots/${bot.id}`, undefined, asB({ expect: 403 }));
  await restore();
});
