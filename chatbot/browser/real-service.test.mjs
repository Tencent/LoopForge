// Start the opt-in SQLite fixture with TestWorkbenchBrowserServer first.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { chromium } from '@playwright/test';

const base = process.env.LOOPFORGE_BROWSER_TEST_URL || 'http://127.0.0.1:18089';
test('real Go service: links, session recovery, drafts, settings and navigation', async () => {
  const browser = await chromium.launch({channel:'chromium', headless:true, args:['--no-sandbox']});
  try {
    const page = await browser.newPage({viewport:{width:1440,height:900}});
    const errors=[];
    page.on('pageerror', e => errors.push(e.message));
    await page.goto(`${base}/login.html`);
    await page.locator('[name="login"]').fill('browser-test');
    await page.locator('[name="password"]').fill('disposable-browser-password');
    await page.locator('#loginSubmit').click();
    await page.waitForURL(url => url.pathname === '/');
    const response = await page.goto(`${base}/chatbot/?session=chat-browser-test`);
    assert.equal(response.status(), 200);
    await page.getByText('原有会话消息').first().waitFor({timeout:20000});
    assert.equal(new URL(page.url()).searchParams.get('chatSession'),'chat-browser-test');
    const input=page.locator('#chatRoot .composer-column textarea');
    await input.fill('真实服务下保留草稿');
    await page.evaluate(()=>window.loopforgePlatform.navigate('settings'));
    await page.locator('#settingsView.active').waitFor();
    await page.evaluate(()=>window.loopforgePlatform.setTheme('dark'));
    assert.equal(await page.locator('html').getAttribute('data-theme'),'dark');
    await page.goBack();
    await input.waitFor({state:'visible'});
    assert.equal(await input.inputValue(),'真实服务下保留草稿');
    await page.reload();
    await page.getByText('原有会话消息').first().waitFor({timeout:20000});
    await input.waitFor({state:'visible'});
    await page.waitForFunction(()=>document.querySelector('#chatRoot .composer-column textarea')?.value==='真实服务下保留草稿');
    assert.equal(await page.locator('#chatNextFrame').count(),0);
    await page.setViewportSize({width:390,height:844});
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth <= innerWidth),true);
    assert.deepEqual(errors,[]);
  } finally { await browser.close(); }
});
