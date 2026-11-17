// Opt-in real-provider acceptance. Start TestWorkbenchBrowserServer with
// LOOPFORGE_BROWSER_DEEPSEEK_KEY; this test never reads or stores the key.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { chromium } from '@playwright/test';

const base = process.env.LOOPFORGE_BROWSER_TEST_URL || 'http://127.0.0.1:18089';
test('DeepSeek reply survives navigation and reload in the unified workbench', { timeout: 300_000 }, async () => {
  const browser = await chromium.launch({ channel: 'chromium', headless: true, args: ['--no-sandbox'] });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(`${base}/login.html`);
    await page.locator('[name="login"]').fill('browser-test');
    await page.locator('[name="password"]').fill('disposable-browser-password');
    await page.locator('#loginSubmit').click();
    await page.waitForURL(url => url.pathname === '/');
    const root = page.locator('#chatRoot');
    const input = root.locator('.composer-column textarea');
    await input.waitFor({ state: 'visible', timeout: 30_000 });
    await root.getByRole('button', { name: 'CLI', exact: true }).click();
    await root.locator('.permission-menu [role="menuitemradio"]').filter({ hasText: 'DeepSeek Harness' }).click();
    await root.getByRole('button', { name: '选择模型', exact: true }).click();
    const model = root.locator('.grasp-model-menu [role="menuitemradio"]').filter({ hasText: /deepseek.*flash/i }).first();
    await model.waitFor({ state: 'visible', timeout: 30_000 });
    await model.click();
    const marker = `LIVE-ISSUE118-${Date.now()}`;
    await input.fill(`这是只读测试。请只回复 ${marker}，不要访问、修改或创建文件和平台对象。`);
    const started = page.waitForResponse(response => new URL(response.url()).pathname === '/api/assistant/generations' && response.request().method() === 'POST');
    await root.locator('.send-button').click();
    const response = await started;
    assert.equal(response.status(), 202, `generation returned HTTP ${response.status()}`);
    const { sessionId } = await response.json();
    assert.ok(sessionId);
    await page.evaluate(() => window.loopforgePlatform.navigate('settings'));
    await page.locator('#settingsView.active').waitFor();
    await page.evaluate(() => { location.hash = '#/collaboration'; });
    await input.waitFor({ state: 'visible' });
    const deadline = Date.now() + 240_000;
    let generation;
    while (Date.now() < deadline) {
      const snapshot = await page.request.get(`${base}/api/assistant/sessions/${encodeURIComponent(sessionId)}/generation`);
      assert.equal(snapshot.status(), 200);
      generation = await snapshot.json();
      if (generation.status !== 'running') break;
      await page.waitForTimeout(1000);
    }
    assert.equal(generation?.status, 'completed', JSON.stringify({status:generation?.status, error:generation?.error, failure:generation?.failure}));
    await root.locator('.message.assistant').last().getByText(marker, { exact: false }).waitFor({ timeout: 30_000 });
    await page.reload();
    await root.locator('.message.assistant').last().getByText(marker, { exact: false }).waitFor({ timeout: 30_000 });
    const saved = await page.request.get(`${base}/api/assistant/sessions/${encodeURIComponent(sessionId)}`);
    assert.ok(saved.ok());
    assert.ok((await saved.json()).messages.some(message => message.role === 'assistant' && message.content.includes(marker)));
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});
